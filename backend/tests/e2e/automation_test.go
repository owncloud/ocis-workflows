//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"testing"
	"time"
)

func automationStatus(t *testing.T) map[string]any {
	t.Helper()
	res := doRequest(t, http.MethodGet, "/me/automation", nil, true)
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("get automation status: expected 200, got %d: %s", res.StatusCode, body)
	}
	return decodeJSON[map[string]any](t, res)
}

// TestAutomationConnectDisconnect exercises the full app-password lifecycle against a real
// oCIS instance: connect mints a real auth-app token, status reflects it, disconnect
// revokes it. No mocks.
func TestAutomationConnectDisconnect(t *testing.T) {
	// Start from a clean slate regardless of what earlier tests/runs left behind.
	_ = doRequest(t, http.MethodDelete, "/me/automation", nil, true).Body.Close()

	status := automationStatus(t)
	if connected, _ := status["connected"].(bool); connected {
		t.Fatal("expected automation to start disconnected")
	}

	connectRes := doRequest(t, http.MethodPost, "/me/automation", nil, true)
	if connectRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(connectRes.Body)
		t.Fatalf("connect automation: expected 200, got %d: %s", connectRes.StatusCode, body)
	}
	connected := decodeJSON[map[string]any](t, connectRes)
	if ok, _ := connected["connected"].(bool); !ok {
		t.Fatalf("connect automation: expected connected=true, got %+v", connected)
	}
	if _, ok := connected["expirationDateTime"].(string); !ok {
		t.Fatalf("connect automation: expected an expirationDateTime, got %+v", connected)
	}

	status = automationStatus(t)
	if ok, _ := status["connected"].(bool); !ok {
		t.Fatalf("expected status connected=true after connect, got %+v", status)
	}

	disconnectRes := doRequest(t, http.MethodDelete, "/me/automation", nil, true)
	defer disconnectRes.Body.Close()
	if disconnectRes.StatusCode != http.StatusNoContent {
		t.Fatalf("disconnect automation: expected 204, got %d", disconnectRes.StatusCode)
	}

	status = automationStatus(t)
	if connected, _ := status["connected"].(bool); connected {
		t.Fatal("expected automation to be disconnected after DELETE /me/automation")
	}
}

// TestScheduledWorkflowRunsInBackground connects automation, creates a workflow scheduled
// to run every second, and waits for it to actually fire on its own — authenticated with
// the stored app-password over HTTP Basic, with no live session involved in the run itself.
func TestScheduledWorkflowRunsInBackground(t *testing.T) {
	connectRes := doRequest(t, http.MethodPost, "/me/automation", nil, true)
	connectRes.Body.Close()
	if connectRes.StatusCode != http.StatusOK {
		t.Fatalf("connect automation: expected 200, got %d", connectRes.StatusCode)
	}
	t.Cleanup(func() {
		res := doRequest(t, http.MethodDelete, "/me/automation", nil, true)
		res.Body.Close()
	})

	newWorkflow := map[string]any{
		"name":    "e2e scheduled workflow",
		"enabled": true,
		"trigger": map[string]string{"type": "schedule", "schedule": "* * * * * *"}, // every second
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "trigger", "type": "trigger", "position": map[string]int{"x": 0, "y": 0}, "data": map[string]any{
					"triggerType": "schedule", "schedule": "* * * * * *",
				}},
				{"id": "llm-1", "type": "llm", "position": map[string]int{"x": 200, "y": 0}, "data": map[string]any{
					"prompt": "Say hi",
				}},
			},
			"edges": []map[string]string{{"id": "e1", "source": "trigger", "target": "llm-1"}},
		},
	}

	createRes := doRequest(t, http.MethodPost, "/me/workflows", newWorkflow, true)
	if createRes.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createRes.Body)
		t.Fatalf("create workflow: expected 201, got %d: %s", createRes.StatusCode, body)
	}
	workflow := decodeJSON[struct {
		ID string `json:"id"`
	}](t, createRes)
	t.Cleanup(func() {
		res := doRequest(t, http.MethodDelete, "/me/workflows/"+workflow.ID, nil, true)
		res.Body.Close()
	})

	// The scheduler ticks every 10s (see command.scheduleTickInterval) — a workflow first
	// seen on tick N is only *due* on tick N+1, and the SSE event-trigger manager's own
	// reconcile/consumer goroutines share this process, so worst-case first-fire latency
	// runs comfortably past 2 tick intervals under load. Poll well past that, well short of
	// the test's own timeout.
	deadline := time.Now().Add(45 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		listRes := doRequest(t, http.MethodGet, "/me/workflows/"+workflow.ID+"/executions", nil, true)
		list := decodeJSON[struct {
			Value []struct {
				TriggeredBy string `json:"triggeredBy"`
				Status      string `json:"status"`
			} `json:"value"`
		}](t, listRes)

		for _, exec := range list.Value {
			if exec.TriggeredBy == "schedule" && exec.Status == "succeeded" {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(2 * time.Second)
	}

	if !found {
		t.Fatal("expected at least one successful schedule-triggered execution within 45s, found none")
	}
}

// TestEventTriggeredWorkflowRunsOnUpload connects automation, creates a workflow with an
// upload event trigger scoped to a path prefix and extension, uploads a matching file
// through WebDAV, and waits for the SSE-driven consumer to fire it on its own — no
// manual/scheduled trigger involved, exactly the flow verified live against a real oCIS
// instance during development (see pkg/sse's package doc for the known coverage gap:
// tag-added/tag-removed events aren't forwarded through SSE).
func TestEventTriggeredWorkflowRunsOnUpload(t *testing.T) {
	token := testToken(t)

	connectRes := doRequest(t, http.MethodPost, "/me/automation", nil, true)
	connectRes.Body.Close()
	if connectRes.StatusCode != http.StatusOK {
		t.Fatalf("connect automation: expected 200, got %d", connectRes.StatusCode)
	}
	t.Cleanup(func() {
		res := doRequest(t, http.MethodDelete, "/me/automation", nil, true)
		res.Body.Close()
	})

	newWorkflow := map[string]any{
		"name":    "e2e event workflow",
		"enabled": true,
		"trigger": map[string]any{
			"type": "event",
			"event": map[string]any{
				"type":    "upload",
				"filters": map[string]string{"pathPrefix": "/e2e-sse-test", "extension": ".txt"},
			},
		},
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "trigger", "type": "trigger", "position": map[string]int{"x": 0, "y": 0}, "data": map[string]any{
					"triggerType": "event", "eventType": "upload",
				}},
				{"id": "llm-1", "type": "llm", "position": map[string]int{"x": 200, "y": 0}, "data": map[string]any{
					"prompt": "Say hi",
				}},
			},
			"edges": []map[string]string{{"id": "e1", "source": "trigger", "target": "llm-1"}},
		},
	}

	createRes := doRequest(t, http.MethodPost, "/me/workflows", newWorkflow, true)
	if createRes.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createRes.Body)
		t.Fatalf("create workflow: expected 201, got %d: %s", createRes.StatusCode, body)
	}
	workflow := decodeJSON[struct {
		ID string `json:"id"`
	}](t, createRes)
	t.Cleanup(func() {
		res := doRequest(t, http.MethodDelete, "/me/workflows/"+workflow.ID, nil, true)
		res.Body.Close()
	})

	// The SSE manager reconciles which users need an active consumer every
	// sseReconcileInterval (30s, see command.sseReconcileInterval) — give it time to open
	// the stream before uploading, so the upload isn't missed by a connection that hasn't
	// started yet.
	time.Sleep(35 * time.Second)

	mkdir(t, token, "/e2e-sse-test")
	uploadFile(t, token, "/e2e-sse-test/hello.txt", "hello from the event-trigger e2e test")

	deadline := time.Now().Add(30 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		listRes := doRequest(t, http.MethodGet, "/me/workflows/"+workflow.ID+"/executions", nil, true)
		list := decodeJSON[struct {
			Value []struct {
				TriggeredBy string `json:"triggeredBy"`
				Status      string `json:"status"`
			} `json:"value"`
		}](t, listRes)

		for _, exec := range list.Value {
			if exec.TriggeredBy == "event" && exec.Status == "succeeded" {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(3 * time.Second)
	}

	if !found {
		t.Fatal("expected at least one successful event-triggered execution within 30s of upload, found none")
	}
}
