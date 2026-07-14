package webdavstore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LukasHirt/ocis-workflows/pkg/model"
	"github.com/LukasHirt/ocis-workflows/pkg/ocisclient"
)

// fakeOCIS is a minimal in-memory stand-in for oCIS's Graph "/me" endpoint and its WebDAV
// files endpoint, just enough to exercise Store's request shapes end-to-end without a real
// oCIS instance (that's what the e2e suite covers instead).
type fakeOCIS struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newFakeOCIS() *fakeOCIS {
	return &fakeOCIS{files: map[string][]byte{}}
}

func (f *fakeOCIS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/graph/v1.0/me":
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "user1"})
	case r.Method == "MKCOL":
		w.WriteHeader(http.StatusCreated)
	case r.Method == "PROPFIND":
		f.mu.Lock()
		defer f.mu.Unlock()
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
		for name := range f.files {
			if strings.HasPrefix(name, r.URL.Path+"/") {
				_, _ = fmt.Fprintf(&sb, `<d:response><d:href>%s</d:href></d:response>`, name)
			}
		}
		sb.WriteString(`</d:multistatus>`)
		w.WriteHeader(207)
		_, _ = w.Write([]byte(sb.String()))
	case r.Method == http.MethodPut:
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		f.mu.Lock()
		f.files[r.URL.Path] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodGet:
		f.mu.Lock()
		body, ok := f.files[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	case r.Method == http.MethodDelete:
		f.mu.Lock()
		_, ok := f.files[r.URL.Path]
		delete(f.files, r.URL.Path)
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	fake := newFakeOCIS()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client := ocisclient.New(srv.URL, false)
	store := New(srv.URL, client, false)
	return store, "test-token"
}

func TestStorePutGetDelete(t *testing.T) {
	store, token := newTestStore(t)
	ctx := t.Context()

	wf := model.WorkflowDefinition{
		ID:      "abc123",
		Name:    "My workflow",
		Enabled: true,
		Trigger: model.WorkflowTrigger{Type: "manual"},
	}

	if err := store.Put(ctx, token, wf); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, token, "abc123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "My workflow" {
		t.Fatalf("Get returned wrong name: %q", got.Name)
	}

	if err := store.Delete(ctx, token, "abc123"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := store.Get(ctx, token, "abc123"); err != ErrNotFound {
		t.Fatalf("Get after Delete: expected ErrNotFound, got %v", err)
	}
}

func TestStoreList(t *testing.T) {
	store, token := newTestStore(t)
	ctx := t.Context()

	for _, id := range []string{"one", "two"} {
		wf := model.WorkflowDefinition{ID: id, Name: "wf-" + id, Trigger: model.WorkflowTrigger{Type: "manual"}}
		if err := store.Put(ctx, token, wf); err != nil {
			t.Fatalf("Put(%s): %v", id, err)
		}
	}

	list, err := store.List(ctx, token)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 workflows, got %d", len(list))
	}
}

func TestStoreGetMissing(t *testing.T) {
	store, token := newTestStore(t)
	if _, err := store.Get(t.Context(), token, "does-not-exist"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
