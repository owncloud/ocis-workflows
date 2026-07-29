//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestListSpaces exercises GET /me/spaces against a real oCIS instance — every user has at
// least a personal space, so this doesn't depend on any project space having been created.
func TestListSpaces(t *testing.T) {
	res := doRequest(t, http.MethodGet, "/me/spaces", nil, true)
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("list spaces: expected 200, got %d: %s", res.StatusCode, body)
	}

	list := decodeJSON[struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}](t, res)

	if len(list.Value) == 0 {
		t.Fatal("expected at least the caller's personal space, got none")
	}
	for _, s := range list.Value {
		if s.ID == "" || s.Name == "" {
			t.Fatalf("space entry missing id/name: %+v", s)
		}
	}
}

// TestEventTriggeredWorkflowRespectsMatchingSpaceScope connects automation, resolves the
// caller's own (personal) space id via GET /me/spaces, creates a workflow with an upload
// event trigger scoped to exactly that space, uploads a matching file, and confirms it still
// fires — proving the SpaceID persisted through the trigger index actually matches the real
// spaceid oCIS's SSE events carry for uploads into that space.
func TestEventTriggeredWorkflowRespectsMatchingSpaceScope(t *testing.T) {
	token := testToken(t)

	spacesRes := doRequest(t, http.MethodGet, "/me/spaces", nil, true)
	spaces := decodeJSON[struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}](t, spacesRes)
	if len(spaces.Value) == 0 {
		t.Fatal("expected at least one space to scope the trigger to")
	}
	spaceID := spaces.Value[0].ID

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
		"name":    "e2e space-scoped event workflow",
		"enabled": true,
		"trigger": map[string]any{
			"type": "event",
			"event": map[string]any{
				"type":    "upload",
				"filters": map[string]string{"pathPrefix": "/e2e-space-scope-test", "spaceId": spaceID},
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

	// Same reconcile-interval wait as TestEventTriggeredWorkflowRunsOnUpload — give the SSE
	// manager time to open the stream before uploading.
	time.Sleep(35 * time.Second)

	mkdir(t, token, "/e2e-space-scope-test")
	uploadFile(t, token, "/e2e-space-scope-test/hello.txt", "hello from the space-scope e2e test")

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
		t.Fatal("expected at least one successful event-triggered execution scoped to the caller's own space within 30s of upload, found none")
	}
}
