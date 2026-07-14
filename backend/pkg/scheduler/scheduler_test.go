package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LukasHirt/ocis-workflows/pkg/localdb"
	"github.com/LukasHirt/ocis-workflows/pkg/model"
)

type fakeTriggerStore struct {
	entries     []localdb.TriggerIndexEntry
	automations map[string]*localdb.Automation
}

func (f *fakeTriggerStore) ListScheduleTriggers(context.Context) ([]localdb.TriggerIndexEntry, error) {
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
	mu        sync.Mutex
	putCount  int
}

func (f *fakeWorkflowStore) Get(_ context.Context, _, id string) (*model.WorkflowDefinition, error) {
	wf, ok := f.workflows[id]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return &wf, nil
}
func (f *fakeWorkflowStore) PutExecution(context.Context, string, model.ExecutionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCount++
	return nil
}

type fakeExecutor struct {
	runs atomic.Int32
}

func (f *fakeExecutor) Run(_ context.Context, _ string, wf model.WorkflowDefinition, triggeredBy, _ string) *model.ExecutionRecord {
	f.runs.Add(1)
	return &model.ExecutionRecord{ID: "exec-1", WorkflowID: wf.ID, TriggeredBy: triggeredBy, Status: "succeeded"}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestTickRunsDueScheduleAndSkipsUsersWithoutAutomation(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-due", UserID: "user-connected", Schedule: "* * * * * *"}, // every second
			{WorkflowID: "wf-no-auto", UserID: "user-disconnected", Schedule: "* * * * * *"},
		},
		automations: map[string]*localdb.Automation{
			"user-connected": {UserID: "user-connected", Username: "admin", AppPassword: "secret"},
		},
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-due":     {ID: "wf-due", Enabled: true},
		"wf-no-auto": {ID: "wf-no-auto", Enabled: true},
	}}
	exec := &fakeExecutor{}

	s := New(triggers, store, exec, time.Hour, discardLogger())
	// Prime lastRun to the past so the "every second" schedule is immediately due.
	s.lastRun["wf-due"] = time.Now().Add(-2 * time.Second)
	s.lastRun["wf-no-auto"] = time.Now().Add(-2 * time.Second)

	s.tick(t.Context())
	// runOne is spawned in a goroutine — give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for exec.runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if got := exec.runs.Load(); got != 1 {
		t.Fatalf("expected exactly 1 run (the connected user's), got %d", got)
	}
}

func TestTickSkipsNotYetDueSchedule(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-future", UserID: "user-1", Schedule: "0 0 1 1 *"}, // once a year, Jan 1st
		},
		automations: map[string]*localdb.Automation{
			"user-1": {UserID: "user-1", Username: "admin", AppPassword: "secret"},
		},
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-future": {ID: "wf-future", Enabled: true},
	}}
	exec := &fakeExecutor{}

	s := New(triggers, store, exec, time.Hour, discardLogger())
	s.tick(t.Context())

	time.Sleep(50 * time.Millisecond)
	if got := exec.runs.Load(); got != 0 {
		t.Fatalf("expected 0 runs for a not-yet-due schedule, got %d", got)
	}
}

func TestTickIgnoresInvalidCronExpression(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-bad", UserID: "user-1", Schedule: "not a cron expression"},
		},
		automations: map[string]*localdb.Automation{
			"user-1": {UserID: "user-1", Username: "admin", AppPassword: "secret"},
		},
	}
	store := &fakeWorkflowStore{}
	exec := &fakeExecutor{}

	s := New(triggers, store, exec, time.Hour, discardLogger())
	s.tick(t.Context()) // must not panic

	if got := exec.runs.Load(); got != 0 {
		t.Fatalf("expected 0 runs for an invalid schedule, got %d", got)
	}
}
