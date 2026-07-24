package sse

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type fakeTriggerStore struct {
	entries     []localdb.TriggerIndexEntry
	automations map[string]*localdb.Automation
}

func (f *fakeTriggerStore) ListEventTriggers(context.Context) ([]localdb.TriggerIndexEntry, error) {
	return f.entries, nil
}
func (f *fakeTriggerStore) GetAutomation(_ context.Context, userID string) (*localdb.Automation, error) {
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

type fakePathResolver struct {
	path string
}

func (f *fakePathResolver) ItemPath(context.Context, string, string, string) (string, error) {
	return f.path, nil
}

type fakeExecutor struct {
	runs atomic.Int32
}

func (f *fakeExecutor) Run(_ context.Context, _ string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord {
	f.runs.Add(1)
	return &model.ExecutionRecord{ID: "exec-1", WorkflowID: wf.ID, TriggeredBy: triggeredBy, Status: "succeeded"}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestStreamOnceDispatchesMatchingEventTrigger drives the manager's SSE line-parsing and
// event-matching logic against a fake SSE server emitting a real captured event shape
// (postprocessing-finished, from a live oCIS instance — see docker-compose e2e notes).
func TestStreamOnceDispatchesMatchingEventTrigger(t *testing.T) {
	sseBody := "data: {\"itemid\":\"space1!item1\",\"spaceid\":\"space1\"}\nevent: postprocessing-finished\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()

	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", PathPrefix: "/Invoices"},
		},
		automations: map[string]*localdb.Automation{
			"user-1": {UserID: "user-1", Username: "admin", AppPassword: "secret"},
		},
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-1": {ID: "wf-1", Enabled: true},
	}}
	paths := &fakePathResolver{path: "/Invoices/foo.pdf"}
	exec := &fakeExecutor{}

	m := New(triggers, store, paths, exec, srv.URL, false, time.Hour, discardLogger())
	err := m.streamOnce(t.Context(), "user-1", "Basic dGVzdA==")
	if err != nil {
		t.Fatalf("streamOnce: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return exec.runs.Load() == 1 })
}

// TestHandleEventSkipsInternalBookkeepingPath regression-tests a real feedback-loop bug: an
// event trigger with no path filter (matches any upload) was observed re-triggering itself
// on every execution record this backend writes into .workflows/, since those writes are
// indistinguishable from a user upload over SSE — starving the scheduler of its single
// sqlite connection under the resulting load.
func TestHandleEventSkipsInternalBookkeepingPath(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload"}, // no filters — would match anything
		},
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	paths := &fakePathResolver{path: "/.workflows/executions/wf-1/exec-1.json"}
	exec := &fakeExecutor{}

	m := New(triggers, store, paths, exec, "http://unused", false, time.Hour, discardLogger())
	m.handleEvent(t.Context(), "user-1", "Basic dGVzdA==", "postprocessing-finished", `{"itemid":"i","spaceid":"s"}`)

	time.Sleep(50 * time.Millisecond)
	if got := exec.runs.Load(); got != 0 {
		t.Fatalf("expected 0 runs for a write under .workflows/, got %d", got)
	}
}

func TestHandleEventSkipsNonMatchingPathPrefix(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", PathPrefix: "/Invoices"},
		},
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-1": {ID: "wf-1", Enabled: true},
	}}
	paths := &fakePathResolver{path: "/Photos/vacation.jpg"} // does not match /Invoices
	exec := &fakeExecutor{}

	m := New(triggers, store, paths, exec, "http://unused", false, time.Hour, discardLogger())
	m.handleEvent(t.Context(), "user-1", "Basic dGVzdA==", "postprocessing-finished", `{"itemid":"i","spaceid":"s"}`)

	time.Sleep(50 * time.Millisecond)
	if got := exec.runs.Load(); got != 0 {
		t.Fatalf("expected 0 runs for non-matching path prefix, got %d", got)
	}
}

func TestHandleEventIgnoresUnmappedSSEEventType(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload"},
		},
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	exec := &fakeExecutor{}

	m := New(triggers, store, &fakePathResolver{}, exec, "http://unused", false, time.Hour, discardLogger())
	m.handleEvent(t.Context(), "user-1", "Basic dGVzdA==", "some-unrecognized-event", `{}`)

	time.Sleep(50 * time.Millisecond)
	if got := exec.runs.Load(); got != 0 {
		t.Fatalf("expected 0 runs for an unmapped SSE event type, got %d", got)
	}
}

// TestReconcileRetriesConsumerAfterTransientGetAutomationFailure regression-tests the bug
// where consumeForUser inserted its m.active entry *before* calling GetAutomation, but never
// removed it if GetAutomation failed (e.g. the user has an event trigger but hasn't connected
// automation yet). Because the entry stayed in m.active forever, every subsequent reconcile
// pass saw the userID as "already active" and never retried it, even after automation was
// connected later.
func TestReconcileRetriesConsumerAfterTransientGetAutomationFailure(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries:     []localdb.TriggerIndexEntry{{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload"}},
		automations: map[string]*localdb.Automation{}, // automation not connected yet
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	m := New(triggers, store, &fakePathResolver{}, &fakeExecutor{}, "http://unused-will-fail-fast", false, time.Hour, discardLogger())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	m.reconcile(ctx)
	// consumeForUser should observe the GetAutomation failure, log, and return — and in doing
	// so remove its own now-dead entry from m.active so a later reconcile pass can retry it.
	waitFor(t, time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.active["user-1"]
		return !ok
	})

	// Automation gets connected sometime later.
	triggers.automations["user-1"] = &localdb.Automation{UserID: "user-1", Username: "admin", AppPassword: "secret"}

	// The next reconcile pass (in production, either the next tick or a Kick()) must retry
	// the consumer now that the entry was cleared, instead of treating it as still active.
	m.reconcile(ctx)
	waitFor(t, time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.active["user-1"]
		return ok
	})
}

// TestKickTriggersImmediateReconcile regression-tests the up-to-30s blind window: without an
// event-driven reconcile hook, Start only reconciles on its periodic ticker, so a trigger
// added right after Start began running would have to wait for the next tick to pick up. Kick
// lets callers (workflow trigger-index writes, automation Connect) request an immediate pass.
func TestKickTriggersImmediateReconcile(t *testing.T) {
	// A pre-existing "warm" trigger lets the test prove Start's initial synchronous
	// reconcile — and hence its select loop — has actually run, without racing: checking
	// for an *absence* of state right after `go m.Start(ctx)` would trivially succeed
	// before Start's goroutine is even scheduled, proving nothing.
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{{WorkflowID: "wf-warm", UserID: "user-warm", TriggerType: "event", EventType: "upload"}},
		automations: map[string]*localdb.Automation{
			"user-warm": {UserID: "user-warm", Username: "admin", AppPassword: "x"},
			"user-1":    {UserID: "user-1", Username: "admin", AppPassword: "x"},
		},
	}
	m := New(triggers, &fakeWorkflowStore{}, &fakePathResolver{}, &fakeExecutor{}, "http://unused-will-fail-fast", false, time.Hour, discardLogger())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go m.Start(ctx)
	waitFor(t, time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.active["user-warm"]
		return ok
	})

	// Only now, after reconcile() is known to have returned and the select loop entered,
	// mutate the wanted set — simulating a workflow with an event trigger being created for
	// user-1 — and ask for an immediate reconcile instead of waiting for the ticker.
	triggers.entries = append(triggers.entries, localdb.TriggerIndexEntry{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload"})
	m.Kick()

	// This must succeed well within the 1-hour ticker interval, proving Kick — not the
	// ticker — is what triggered the reconcile.
	waitFor(t, time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.active["user-1"]
		return ok
	})
}

func TestReconcileStartsAndStopsConsumersAsTriggersChange(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries:     []localdb.TriggerIndexEntry{{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload"}},
		automations: map[string]*localdb.Automation{"user-1": {UserID: "user-1", Username: "admin", AppPassword: "x"}},
	}
	m := New(triggers, &fakeWorkflowStore{}, &fakePathResolver{}, &fakeExecutor{}, "http://unused-will-fail-fast", false, time.Hour, discardLogger())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	m.reconcile(ctx)
	waitFor(t, time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.active) == 1
	})

	triggers.entries = nil
	m.reconcile(ctx)
	waitFor(t, time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.active) == 0
	})
}
