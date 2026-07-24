package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/ratelimit"
)

type fakeHooksTriggerStore struct {
	entries map[string]localdb.TriggerIndexEntry
}

func (f *fakeHooksTriggerStore) GetTriggerIndexEntry(_ context.Context, workflowID string) (*localdb.TriggerIndexEntry, error) {
	e, ok := f.entries[workflowID]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return &e, nil
}

type fakeHooksAutomationStore struct {
	automations map[string]*localdb.Automation
}

func (f *fakeHooksAutomationStore) GetAutomation(_ context.Context, userID string) (*localdb.Automation, error) {
	a, ok := f.automations[userID]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return a, nil
}

type fakeHooksWorkflowStore struct {
	workflows map[string]model.WorkflowDefinition
	putCount  int
	lastPut   model.ExecutionRecord
}

func (f *fakeHooksWorkflowStore) Get(_ context.Context, _, id string) (*model.WorkflowDefinition, error) {
	wf, ok := f.workflows[id]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return &wf, nil
}
func (f *fakeHooksWorkflowStore) PutExecution(_ context.Context, _ string, rec model.ExecutionRecord) error {
	f.putCount++
	f.lastPut = rec
	return nil
}

type fakeHooksExecutor struct {
	runs      int
	lastVars  map[string]string
	lastPath  string
	lastTrig  string
	returnRec *model.ExecutionRecord
}

func (f *fakeHooksExecutor) RunWithVars(_ context.Context, _ string, wf model.WorkflowDefinition, triggeredBy, resourcePath string, extraVars map[string]string) *model.ExecutionRecord {
	f.runs++
	f.lastVars = extraVars
	f.lastPath = resourcePath
	f.lastTrig = triggeredBy
	if f.returnRec != nil {
		return f.returnRec
	}
	return &model.ExecutionRecord{ID: "exec-1", WorkflowID: wf.ID, TriggeredBy: triggeredBy, Status: "succeeded"}
}

func newTestHooksHandler(t *testing.T) (*HooksHandler, *fakeHooksTriggerStore, *fakeHooksAutomationStore, *fakeHooksWorkflowStore, *fakeHooksExecutor) {
	t.Helper()
	triggers := &fakeHooksTriggerStore{entries: map[string]localdb.TriggerIndexEntry{
		"wf-1": {WorkflowID: "wf-1", UserID: "user-1", TriggerType: "webhook", WebhookToken: "correct-token"},
	}}
	automations := &fakeHooksAutomationStore{automations: map[string]*localdb.Automation{
		"user-1": {UserID: "user-1", Username: "admin", AppPassword: "app-secret"},
	}}
	store := &fakeHooksWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-1": {ID: "wf-1", Enabled: true, Trigger: model.WorkflowTrigger{Type: "webhook"}},
	}}
	exec := &fakeHooksExecutor{}
	limiter := ratelimit.New(1000, time.Minute)
	h := NewHooksHandler(triggers, automations, store, exec, limiter, discardLogger())
	return h, triggers, automations, store, exec
}

// router builds a minimal chi router mounting only the hooks route, mirroring how
// pkg/server/http/server.go wires it (outside any auth middleware group) — enough to
// exercise chi.URLParam extraction the same way a real request would.
func router(h *HooksHandler) http.Handler {
	r := chi.NewRouter()
	r.Post("/hooks/{workflowId}/{token}", h.Trigger)
	return r
}

func doPost(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHooksTriggerRejectsWrongToken(t *testing.T) {
	h, _, _, _, exec := newTestHooksHandler(t)
	rec := doPost(t, router(h), "/hooks/wf-1/wrong-token", "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if exec.runs != 0 {
		t.Fatal("expected no run for a wrong token")
	}
}

func TestHooksTriggerRejectsMissingWorkflow(t *testing.T) {
	h, _, _, _, exec := newTestHooksHandler(t)
	rec := doPost(t, router(h), "/hooks/does-not-exist/correct-token", "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (must not distinguish unknown workflow from wrong token)", rec.Code)
	}
	if exec.runs != 0 {
		t.Fatal("expected no run for an unknown workflow id")
	}
}

func TestHooksTriggerRejectsNonWebhookTrigger(t *testing.T) {
	triggers := &fakeHooksTriggerStore{entries: map[string]localdb.TriggerIndexEntry{
		"wf-2": {WorkflowID: "wf-2", UserID: "user-1", TriggerType: "schedule"},
	}}
	automations := &fakeHooksAutomationStore{automations: map[string]*localdb.Automation{}}
	store := &fakeHooksWorkflowStore{workflows: map[string]model.WorkflowDefinition{}}
	exec := &fakeHooksExecutor{}
	h := NewHooksHandler(triggers, automations, store, exec, ratelimit.New(1000, time.Minute), discardLogger())

	rec := doPost(t, router(h), "/hooks/wf-2/anything", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if exec.runs != 0 {
		t.Fatal("expected no run for a non-webhook trigger entry")
	}
}

func TestHooksTriggerValidTokenRunsWithFlattenedJSONBody(t *testing.T) {
	h, _, _, store, exec := newTestHooksHandler(t)

	body := `{"status":"approved","count":3,"active":true,"nested":{"a":1}}`
	rec := doPost(t, router(h), "/hooks/wf-1/correct-token", body)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}
	if exec.runs != 1 {
		t.Fatalf("expected exactly 1 run, got %d", exec.runs)
	}
	if exec.lastTrig != "webhook" {
		t.Fatalf("triggeredBy = %q, want webhook", exec.lastTrig)
	}
	if exec.lastVars["webhook.body"] != body {
		t.Fatalf("vars[webhook.body] = %q, want raw body", exec.lastVars["webhook.body"])
	}
	if exec.lastVars["webhook.body.status"] != "approved" {
		t.Fatalf("vars[webhook.body.status] = %q, want approved", exec.lastVars["webhook.body.status"])
	}
	if exec.lastVars["webhook.body.count"] != "3" {
		t.Fatalf("vars[webhook.body.count] = %q, want 3", exec.lastVars["webhook.body.count"])
	}
	if exec.lastVars["webhook.body.active"] != "true" {
		t.Fatalf("vars[webhook.body.active] = %q, want true", exec.lastVars["webhook.body.active"])
	}
	if store.putCount != 1 {
		t.Fatalf("expected the execution record to be stored once, got %d", store.putCount)
	}
}

func TestHooksTriggerResourcePathDefaultsEmptyUnlessQueryParamGiven(t *testing.T) {
	h, _, _, _, exec := newTestHooksHandler(t)

	doPost(t, router(h), "/hooks/wf-1/correct-token", "{}")
	if exec.lastPath != "" {
		t.Fatalf("expected empty resourcePath by default, got %q", exec.lastPath)
	}

	doPost(t, router(h), "/hooks/wf-1/correct-token?path=/foo/bar.txt", "{}")
	if exec.lastPath != "/foo/bar.txt" {
		t.Fatalf("expected resourcePath from ?path= query param, got %q", exec.lastPath)
	}
}

func TestHooksTriggerNonJSONBodyStillFiresWithRawStringOnly(t *testing.T) {
	h, _, _, _, exec := newTestHooksHandler(t)

	rec := doPost(t, router(h), "/hooks/wf-1/correct-token", "not json at all { garbage")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (malformed body must not fail the trigger), body=%s", rec.Code, rec.Body.String())
	}
	if exec.runs != 1 {
		t.Fatalf("expected exactly 1 run, got %d", exec.runs)
	}
	if exec.lastVars["webhook.body"] != "not json at all { garbage" {
		t.Fatalf("vars[webhook.body] = %q, want raw string preserved", exec.lastVars["webhook.body"])
	}
	for k := range exec.lastVars {
		if k != "webhook.body" {
			t.Fatalf("expected no flattened keys for a non-JSON body, found %q", k)
		}
	}
}

func TestHooksTriggerEmptyBodyDoesNotCrash(t *testing.T) {
	h, _, _, _, exec := newTestHooksHandler(t)

	rec := doPost(t, router(h), "/hooks/wf-1/correct-token", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if exec.lastVars["webhook.body"] != "" {
		t.Fatalf("vars[webhook.body] = %q, want empty", exec.lastVars["webhook.body"])
	}
}

func TestHooksTriggerJSONArrayBodySkipsFlatteningButKeepsRawString(t *testing.T) {
	h, _, _, _, exec := newTestHooksHandler(t)

	body := `[1,2,3]`
	rec := doPost(t, router(h), "/hooks/wf-1/correct-token", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if exec.lastVars["webhook.body"] != body {
		t.Fatalf("vars[webhook.body] = %q, want raw body", exec.lastVars["webhook.body"])
	}
	if len(exec.lastVars) != 1 {
		t.Fatalf("expected only webhook.body for a JSON array body, got %+v", exec.lastVars)
	}
}

func TestHooksTriggerDisabledWorkflowDoesNotRun(t *testing.T) {
	triggers := &fakeHooksTriggerStore{entries: map[string]localdb.TriggerIndexEntry{
		"wf-1": {WorkflowID: "wf-1", UserID: "user-1", TriggerType: "webhook", WebhookToken: "correct-token"},
	}}
	automations := &fakeHooksAutomationStore{automations: map[string]*localdb.Automation{
		"user-1": {UserID: "user-1", Username: "admin", AppPassword: "app-secret"},
	}}
	store := &fakeHooksWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-1": {ID: "wf-1", Enabled: false, Trigger: model.WorkflowTrigger{Type: "webhook"}},
	}}
	exec := &fakeHooksExecutor{}
	h := NewHooksHandler(triggers, automations, store, exec, ratelimit.New(1000, time.Minute), discardLogger())

	rec := doPost(t, router(h), "/hooks/wf-1/correct-token", "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 even when disabled (must not leak enabled state)", rec.Code)
	}
	if exec.runs != 0 {
		t.Fatal("expected no run for a disabled workflow")
	}
}

func TestHooksTriggerMissingAutomationFailsGracefully(t *testing.T) {
	triggers := &fakeHooksTriggerStore{entries: map[string]localdb.TriggerIndexEntry{
		"wf-1": {WorkflowID: "wf-1", UserID: "user-without-automation", TriggerType: "webhook", WebhookToken: "correct-token"},
	}}
	automations := &fakeHooksAutomationStore{automations: map[string]*localdb.Automation{}}
	store := &fakeHooksWorkflowStore{}
	exec := &fakeHooksExecutor{}
	h := NewHooksHandler(triggers, automations, store, exec, ratelimit.New(1000, time.Minute), discardLogger())

	rec := doPost(t, router(h), "/hooks/wf-1/correct-token", "{}")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if exec.runs != 0 {
		t.Fatal("expected no run when the owner has no automation connected")
	}
}

func TestHooksTriggerRateLimitsAfterNRequests(t *testing.T) {
	triggers := &fakeHooksTriggerStore{entries: map[string]localdb.TriggerIndexEntry{
		"wf-1": {WorkflowID: "wf-1", UserID: "user-1", TriggerType: "webhook", WebhookToken: "correct-token"},
	}}
	automations := &fakeHooksAutomationStore{automations: map[string]*localdb.Automation{
		"user-1": {UserID: "user-1", Username: "admin", AppPassword: "app-secret"},
	}}
	store := &fakeHooksWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-1": {ID: "wf-1", Enabled: true, Trigger: model.WorkflowTrigger{Type: "webhook"}},
	}}
	exec := &fakeHooksExecutor{}
	const limit = 3
	h := NewHooksHandler(triggers, automations, store, exec, ratelimit.New(limit, time.Minute), discardLogger())
	r := router(h)

	for i := 0; i < limit; i++ {
		rec := doPost(t, r, "/hooks/wf-1/correct-token", "{}")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: status = %d, want 202", i+1, rec.Code)
		}
	}

	rec := doPost(t, r, "/hooks/wf-1/correct-token", "{}")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d: status = %d, want 429", limit+1, rec.Code)
	}
	if exec.runs != limit {
		t.Fatalf("expected exactly %d runs before rate limiting kicked in, got %d", limit, exec.runs)
	}

	// A different token must not be affected by another workflow's rate limit budget.
	triggers.entries["wf-2"] = localdb.TriggerIndexEntry{WorkflowID: "wf-2", UserID: "user-1", TriggerType: "webhook", WebhookToken: "other-token"}
	store.workflows["wf-2"] = model.WorkflowDefinition{ID: "wf-2", Enabled: true, Trigger: model.WorkflowTrigger{Type: "webhook"}}
	rec = doPost(t, r, "/hooks/wf-2/other-token", "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a different token's own budget must be unaffected, status = %d", rec.Code)
	}
}
