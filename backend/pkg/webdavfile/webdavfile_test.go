package webdavfile

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/owncloud/ocis-workflows/pkg/ocisclient"
)

// fakeOCIS is a minimal in-memory stand-in for oCIS's Graph "/me" endpoint and its WebDAV
// MKCOL endpoint, just enough to exercise Client's CreateFolder request shape without a real
// oCIS instance (that's what the e2e suite covers instead). mkcolStatus lets a test simulate
// the "folder already exists" responses oCIS returns (405/409), which CreateFolder must treat
// as success just like the pre-existing internal mkcol usage in Comment does.
type fakeOCIS struct {
	mkcolStatus int
	mkcolPaths  []string
}

func (f *fakeOCIS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/graph/v1.0/me":
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "user1"})
	case r.Method == "MKCOL":
		f.mkcolPaths = append(f.mkcolPaths, r.URL.Path)
		status := f.mkcolStatus
		if status == 0 {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newTestClient(t *testing.T, mkcolStatus int) (*Client, *fakeOCIS) {
	t.Helper()
	fake := &fakeOCIS{mkcolStatus: mkcolStatus}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client := New(srv.URL, ocisclient.New(srv.URL, false), false)
	return client, fake
}

func TestCreateFolderSuccess(t *testing.T) {
	client, fake := newTestClient(t, http.StatusCreated)

	if err := client.CreateFolder(t.Context(), "Bearer token", "/Processed/Invoices"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if len(fake.mkcolPaths) != 1 || fake.mkcolPaths[0] != "/remote.php/dav/files/user1/Processed/Invoices" {
		t.Fatalf("unexpected MKCOL request path(s): %v", fake.mkcolPaths)
	}
}

func TestCreateFolderTreatsAlreadyExistsAsSuccess(t *testing.T) {
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusConflict} {
		client, _ := newTestClient(t, status)
		if err := client.CreateFolder(t.Context(), "Bearer token", "/Processed"); err != nil {
			t.Fatalf("CreateFolder with MKCOL status %d: expected idempotent success, got error: %v", status, err)
		}
	}
}

func TestCreateFolderPropagatesUnexpectedStatus(t *testing.T) {
	client, _ := newTestClient(t, http.StatusInternalServerError)
	if err := client.CreateFolder(t.Context(), "Bearer token", "/Processed"); err == nil {
		t.Fatal("expected error for unexpected MKCOL status, got nil")
	}
}
