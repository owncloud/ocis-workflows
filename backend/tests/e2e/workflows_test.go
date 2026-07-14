//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/LukasHirt/ocis-workflows/pkg/model"
)

var (
	tokenOnce sync.Once
	token     string
)

func testToken(t *testing.T) string {
	t.Helper()
	tokenOnce.Do(func() { token = login(t) })
	if token == "" {
		t.Fatal("no token available (login failed in an earlier test)")
	}
	return token
}

func doRequest(t *testing.T, method, path string, body any, withAuth bool) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, baseURL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+testToken(t))
	}

	res, err := httpClient().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

func decodeJSON[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	defer res.Body.Close()
	var v T
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return v
}

func TestWorkflowsRequireAuth(t *testing.T) {
	res := doRequest(t, http.MethodGet, "/me/workflows", nil, false)
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a bearer token, got %d", res.StatusCode)
	}
}

func TestWorkflowsCRUD(t *testing.T) {
	newWorkflow := map[string]any{
		"name":    "e2e test workflow",
		"enabled": true,
		"trigger": map[string]string{"type": "manual"},
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "trigger", "type": "trigger", "position": map[string]int{"x": 0, "y": 0}, "data": map[string]any{}},
			},
			"edges": []any{},
		},
	}

	// Create
	createRes := doRequest(t, http.MethodPost, "/me/workflows", newWorkflow, true)
	if createRes.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createRes.Body)
		t.Fatalf("create: expected 201, got %d: %s", createRes.StatusCode, body)
	}
	created := decodeJSON[model.WorkflowDefinition](t, createRes)
	if created.ID == "" {
		t.Fatal("create: response has no id")
	}
	if created.Name != "e2e test workflow" {
		t.Fatalf("create: expected name %q, got %q", "e2e test workflow", created.Name)
	}
	t.Cleanup(func() {
		res := doRequest(t, http.MethodDelete, "/me/workflows/"+created.ID, nil, true)
		res.Body.Close()
	})

	// List must include it, Graph-shaped as {"value": [...]}
	listRes := doRequest(t, http.MethodGet, "/me/workflows", nil, true)
	if listRes.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", listRes.StatusCode)
	}
	list := decodeJSON[model.Collection[model.WorkflowDefinition]](t, listRes)
	found := false
	for _, wf := range list.Value {
		if wf.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list: created workflow %s not present in %+v", created.ID, list.Value)
	}

	// Get
	getRes := doRequest(t, http.MethodGet, "/me/workflows/"+created.ID, nil, true)
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", getRes.StatusCode)
	}
	got := decodeJSON[model.WorkflowDefinition](t, getRes)
	if got.ID != created.ID {
		t.Fatalf("get: expected id %s, got %s", created.ID, got.ID)
	}

	// Patch
	patchRes := doRequest(t, http.MethodPatch, "/me/workflows/"+created.ID, map[string]string{"name": "renamed"}, true)
	if patchRes.StatusCode != http.StatusOK {
		t.Fatalf("patch: expected 200, got %d", patchRes.StatusCode)
	}
	patched := decodeJSON[model.WorkflowDefinition](t, patchRes)
	if patched.Name != "renamed" {
		t.Fatalf("patch: expected name %q, got %q", "renamed", patched.Name)
	}
	if patched.LastModifiedDateTime == created.LastModifiedDateTime {
		t.Fatal("patch: lastModifiedDateTime was not updated")
	}

	// Delete
	deleteRes := doRequest(t, http.MethodDelete, "/me/workflows/"+created.ID, nil, true)
	if deleteRes.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", deleteRes.StatusCode)
	}
	deleteRes.Body.Close()

	// Get after delete must 404 with a Graph-shaped error body
	afterDeleteRes := doRequest(t, http.MethodGet, "/me/workflows/"+created.ID, nil, true)
	if afterDeleteRes.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: expected 404, got %d", afterDeleteRes.StatusCode)
	}
	errBody := decodeJSON[model.ErrorResponse](t, afterDeleteRes)
	if errBody.Error.Code == "" {
		t.Fatal("get after delete: expected a non-empty Graph-shaped error code")
	}
}

func TestGetMissingWorkflowReturnsGraphShapedError(t *testing.T) {
	res := doRequest(t, http.MethodGet, "/me/workflows/does-not-exist", nil, true)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", res.StatusCode)
	}
	body := decodeJSON[model.ErrorResponse](t, res)
	if body.Error.Code != "workflowNotFound" {
		t.Fatalf("expected error code %q, got %q", "workflowNotFound", body.Error.Code)
	}
}
