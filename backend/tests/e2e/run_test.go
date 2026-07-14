//go:build e2e

package e2e

import (
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func davClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // dev-only self-signed certs
		Timeout:   30 * time.Second,
	}
}

func mkdir(t *testing.T, token, davPath string) {
	t.Helper()
	req, err := http.NewRequest("MKCOL", ocisURL+"/remote.php/dav/files/admin"+davPath, nil)
	if err != nil {
		t.Fatalf("build mkcol request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := davClient().Do(req)
	if err != nil {
		t.Fatalf("mkcol %s: %v", davPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("mkcol %s: expected 201, got %d", davPath, res.StatusCode)
	}
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, ocisURL+"/remote.php/dav/files/admin"+davPath, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := davClient().Do(req)
		if err == nil {
			res.Body.Close()
		}
	})
}

func uploadFile(t *testing.T, token, davPath, content string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ocisURL+"/remote.php/dav/files/admin"+davPath, strings.NewReader(content))
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := davClient().Do(req)
	if err != nil {
		t.Fatalf("upload %s: %v", davPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusNoContent {
		t.Fatalf("upload %s: expected 201/204, got %d", davPath, res.StatusCode)
	}
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, ocisURL+"/remote.php/dav/files/admin"+davPath, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := davClient().Do(req)
		if err == nil {
			res.Body.Close()
		}
	})
}

func fileTags(t *testing.T, token, davPath string) string {
	t.Helper()
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:tags/></d:prop></d:propfind>`
	req, err := http.NewRequest("PROPFIND", ocisURL+"/remote.php/dav/files/admin"+davPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build PROPFIND request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")

	res, err := davClient().Do(req)
	if err != nil {
		t.Fatalf("PROPFIND %s: %v", davPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 207 {
		t.Fatalf("PROPFIND %s: expected 207, got %d", davPath, res.StatusCode)
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read PROPFIND body: %v", err)
	}

	var ms struct {
		Responses []struct {
			Propstat struct {
				Prop struct {
					Tags string `xml:"tags"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(data, &ms); err != nil {
		t.Fatalf("decode PROPFIND response: %v", err)
	}
	if len(ms.Responses) == 0 {
		return ""
	}
	return ms.Responses[0].Propstat.Prop.Tags
}

// TestRunManualWorkflowAppliesRealTag runs a full trigger -> llm -> tag graph through the
// real API against a real oCIS instance (backed by the fake-llm fixture, not a real LLM
// provider — see docker-compose.yml) and asserts the tag actually landed on the file, by
// reading it back over WebDAV. No component of this path is mocked.
func TestRunManualWorkflowAppliesRealTag(t *testing.T) {
	token := testToken(t)

	uploadFile(t, token, "/e2e-run-test.txt", "Invoice #42 for widgets, total $500.")

	newWorkflow := map[string]any{
		"name":    "e2e run test workflow",
		"enabled": true,
		"trigger": map[string]string{"type": "manual"},
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "trigger", "type": "trigger", "position": map[string]int{"x": 0, "y": 0}, "data": map[string]any{}},
				{"id": "llm-1", "type": "llm", "position": map[string]int{"x": 200, "y": 0}, "data": map[string]any{
					"prompt": "Summarize {{file.content}}",
				}},
				{"id": "action-1", "type": "action", "position": map[string]int{"x": 400, "y": 0}, "data": map[string]any{
					"actionType":   "tag",
					"actionParams": map[string]string{"tag": "e2e-tested"},
				}},
			},
			"edges": []map[string]string{
				{"id": "e1", "source": "trigger", "target": "llm-1"},
				{"id": "e2", "source": "llm-1", "target": "action-1"},
			},
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

	runRes := doRequest(t, http.MethodPost, "/me/workflows/"+workflow.ID+"/run", map[string]string{"resourcePath": "/e2e-run-test.txt"}, true)
	defer runRes.Body.Close()
	if runRes.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(runRes.Body)
		t.Fatalf("run workflow: expected 202, got %d: %s", runRes.StatusCode, body)
	}

	location := runRes.Header.Get("Location")
	if location == "" {
		t.Fatal("run workflow: response had no Location header")
	}
	execID := location[strings.LastIndex(location, "/")+1:]

	execRes := doRequest(t, http.MethodGet, fmt.Sprintf("/me/workflows/%s/executions/%s", workflow.ID, execID), nil, true)
	if execRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(execRes.Body)
		t.Fatalf("get execution: expected 200, got %d: %s", execRes.StatusCode, body)
	}
	execution := decodeJSON[struct {
		Status      string `json:"status"`
		NodeResults []struct {
			NodeID string `json:"nodeId"`
			Status string `json:"status"`
		} `json:"nodeResults"`
	}](t, execRes)

	if execution.Status != "succeeded" {
		t.Fatalf("expected execution status succeeded, got %q", execution.Status)
	}
	if len(execution.NodeResults) != 2 {
		t.Fatalf("expected 2 node results, got %d", len(execution.NodeResults))
	}

	tags := fileTags(t, token, "/e2e-run-test.txt")
	if !strings.Contains(tags, "e2e-tested") {
		t.Fatalf("expected file to be tagged \"e2e-tested\", oc:tags was %q", tags)
	}

	listRes := doRequest(t, http.MethodGet, "/me/workflows/"+workflow.ID+"/executions", nil, true)
	if listRes.StatusCode != http.StatusOK {
		t.Fatalf("list executions: expected 200, got %d", listRes.StatusCode)
	}
	list := decodeJSON[struct {
		Value []json.RawMessage `json:"value"`
	}](t, listRes)
	if len(list.Value) != 1 {
		t.Fatalf("expected 1 execution in history, got %d", len(list.Value))
	}
}
