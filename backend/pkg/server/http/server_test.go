package http_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/ratelimit"
	httpserver "github.com/owncloud/ocis-workflows/pkg/server/http"
	"github.com/owncloud/ocis-workflows/pkg/service"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type fakeTriggerStore struct {
	entries map[string]localdb.TriggerIndexEntry
}

func (f *fakeTriggerStore) GetTriggerIndexEntry(_ context.Context, workflowID string) (*localdb.TriggerIndexEntry, error) {
	e, ok := f.entries[workflowID]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return &e, nil
}

type fakeAutomationStore struct {
	automations map[string]*localdb.Automation
}

func (f *fakeAutomationStore) GetAutomation(_ context.Context, userID string) (*localdb.Automation, error) {
	a, ok := f.automations[userID]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return a, nil
}

type fakeWorkflowStore struct {
	workflows map[string]model.WorkflowDefinition
}

func (f *fakeWorkflowStore) Get(_ context.Context, _, id string) (*model.WorkflowDefinition, error) {
	wf, ok := f.workflows[id]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return &wf, nil
}
func (f *fakeWorkflowStore) PutExecution(context.Context, string, model.ExecutionRecord) error {
	return nil
}

type fakeExecutor struct{ runs int }

func (f *fakeExecutor) RunWithVars(_ context.Context, _ string, wf model.WorkflowDefinition, triggeredBy, _ string, _ map[string]string) *model.ExecutionRecord {
	f.runs++
	return &model.ExecutionRecord{ID: "exec-1", WorkflowID: wf.ID, TriggeredBy: triggeredBy, Status: "succeeded"}
}

// buildTestRouter assembles the real router via httpserver.New, with a real (but
// unreachable-from-these-tests) Validator so /api/v1beta1/* requests must go through it,
// and a fully faked HooksHandler so /hooks/* requests can be exercised without any network
// dependency.
func buildTestRouter(t *testing.T) (http.Handler, *fakeExecutor) {
	t.Helper()

	triggers := &fakeTriggerStore{entries: map[string]localdb.TriggerIndexEntry{
		"wf-1": {WorkflowID: "wf-1", UserID: "user-1", TriggerType: "webhook", WebhookToken: "correct-token"},
	}}
	automations := &fakeAutomationStore{automations: map[string]*localdb.Automation{
		"user-1": {UserID: "user-1", Username: "admin", AppPassword: "app-secret"},
	}}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-1": {ID: "wf-1", Enabled: true, Trigger: model.WorkflowTrigger{Type: "webhook"}},
	}}
	exec := &fakeExecutor{}
	hooks := service.NewHooksHandler(triggers, automations, store, exec, ratelimit.New(1000, time.Minute), discardLogger())

	handler := httpserver.New(httpserver.Options{
		AllowedOrigin: "https://example.test",
		Validator:     auth.NewValidator("https://ocis.example.test", "https://example.test", false),
		Workflows:     nil,
		Automation:    nil,
		Hooks:         hooks,
		Logger:        discardLogger(),
	})
	return handler, exec
}

func TestHooksRouteBypassesBearerAuth(t *testing.T) {
	handler, exec := buildTestRouter(t)

	// No Authorization header at all — a request from an external caller with no oCIS
	// session, exactly the case this route exists for. It must still reach HooksHandler
	// (and succeed, given the correct URL token), never get intercepted by
	// Validator.Middleware the way every /api/v1beta1/* route would.
	req := httptest.NewRequest(http.MethodPost, "/hooks/wf-1/correct-token", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (request must reach HooksHandler without a bearer token), body=%s", rec.Code, rec.Body.String())
	}
	if exec.runs != 1 {
		t.Fatalf("expected the webhook route to have triggered a run, got %d runs", exec.runs)
	}
}

func TestHooksRouteWrongTokenStillReachesHandlerAndIsRejected(t *testing.T) {
	handler, exec := buildTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/hooks/wf-1/wrong-token", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 from HooksHandler's own token check", rec.Code)
	}
	if exec.runs != 0 {
		t.Fatal("expected no run for a wrong token")
	}
}

func TestAPIRoutesStillRequireBearerAuth(t *testing.T) {
	handler, _ := buildTestRouter(t)

	// No Authorization header, hitting a route inside the /api/v1beta1 group: must be
	// rejected by Validator.Middleware before ever reaching a handler (both Workflows and
	// Automation are nil above — a 401 here proves the middleware ran first and never
	// dereferenced them).
	req := httptest.NewRequest(http.MethodGet, "/api/v1beta1/me/workflows", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unauthenticated /api/v1beta1 request", rec.Code)
	}
}
