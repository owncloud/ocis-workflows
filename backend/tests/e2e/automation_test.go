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

	// The scheduler ticks every 10s (see command.scheduleTickInterval) — poll well past
	// that, well short of the test's own timeout, for at least one background-triggered
	// execution to show up.
	deadline := time.Now().Add(25 * time.Second)
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
		t.Fatal("expected at least one successful schedule-triggered execution within 25s, found none")
	}
}
