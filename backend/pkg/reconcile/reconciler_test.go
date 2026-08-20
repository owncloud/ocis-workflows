package reconcile

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/ocisclient"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type fakeTriggerStore struct {
	mu      sync.Mutex
	entries []localdb.TriggerIndexEntry
	cursors map[string]localdb.EventCursor // key: userID+"|"+driveID
}

func newFakeTriggerStore(entries []localdb.TriggerIndexEntry) *fakeTriggerStore {
	return &fakeTriggerStore{entries: entries, cursors: map[string]localdb.EventCursor{}}
}

func (f *fakeTriggerStore) ListEventTriggers(context.Context) ([]localdb.TriggerIndexEntry, error) {
	return f.entries, nil
}

func (f *fakeTriggerStore) GetEventCursor(_ context.Context, userID, driveID string) (*localdb.EventCursor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cursors[userID+"|"+driveID]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return &c, nil
}

func (f *fakeTriggerStore) UpsertEventCursor(_ context.Context, c localdb.EventCursor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors[c.UserID+"|"+c.DriveID] = c
	return nil
}

func (f *fakeTriggerStore) cursor(userID, driveID string) (localdb.EventCursor, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cursors[userID+"|"+driveID]
	return c, ok
}

type fakeDriveLister struct {
	drives []ocisclient.Drive
}

func (f *fakeDriveLister) ListDrives(context.Context, string) ([]ocisclient.Drive, error) {
	return f.drives, nil
}

type fakeActivityLister struct {
	mu        sync.Mutex
	byDrive   map[string][]ocisclient.Activity
	err       error
	callCount atomic.Int32
	lastSince map[string]time.Time
}

func newFakeActivityLister(byDrive map[string][]ocisclient.Activity) *fakeActivityLister {
	return &fakeActivityLister{byDrive: byDrive, lastSince: map[string]time.Time{}}
}

func (f *fakeActivityLister) ListActivities(_ context.Context, _, driveID string, since time.Time) ([]ocisclient.Activity, error) {
	f.callCount.Add(1)
	f.mu.Lock()
	f.lastSince[driveID] = since
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.byDrive[driveID], nil
}

type fakePathResolver struct {
	pathsByItemID map[string]string
}

func (f *fakePathResolver) ItemPath(_ context.Context, _, _, itemID string) (string, error) {
	return f.pathsByItemID[itemID], nil
}

type fakeExecutor struct {
	runs atomic.Int32
}

func (f *fakeExecutor) Run(_ context.Context, _ string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord {
	f.runs.Add(1)
	return &model.ExecutionRecord{ID: "exec-1", WorkflowID: wf.ID, TriggeredBy: triggeredBy, Status: "succeeded"}
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

// itemID "opaque-1" resolves to a compound resource id via fakePathResolver keyed on the
// opaque id portion — see splitResourceID's expected input shape ("storageid$spaceid!opaqueid").
const testResourceID = "storage1$drive-1!opaque-1"

func TestReconcileFirstEverCallSeedsCursorWithoutBackfill(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{
		"drive-1": {{ID: "act-1", Message: "{user} added {resource} to {folder}", Resource: ocisclient.ActivityResource{ID: testResourceID}}},
	})
	exec := &fakeExecutor{}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	paths := &fakePathResolver{pathsByItemID: map[string]string{"opaque-1": "/Invoices/foo.pdf"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, 5*time.Second, 5*time.Second, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	if activities.callCount.Load() != 0 {
		t.Fatalf("expected no activitylog query on first-ever call (nothing to backfill), got %d calls", activities.callCount.Load())
	}
	if exec.runs.Load() != 0 {
		t.Fatalf("expected no workflow run on first-ever call, got %d", exec.runs.Load())
	}
	if _, ok := triggers.cursor("user-1", "drive-1"); !ok {
		t.Fatal("expected a cursor to be seeded for (user-1, drive-1)")
	}
}

func TestReconcileDispatchesMatchingActivity(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	// Seed a stale cursor so this isn't treated as a first-ever call.
	staleTime := time.Now().Add(-time.Hour)
	if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: staleTime, LastStatus: "full"}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	activities := newFakeActivityLister(map[string][]ocisclient.Activity{
		"drive-1": {{ID: "act-1", Message: "{user} added {resource} to {folder}", Resource: ocisclient.ActivityResource{ID: testResourceID}, RecordedTime: time.Now()}},
	})
	exec := &fakeExecutor{}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	paths := &fakePathResolver{pathsByItemID: map[string]string{"opaque-1": "/Invoices/foo.pdf"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, 5*time.Second, 5*time.Second, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	waitFor(t, time.Second, func() bool { return exec.runs.Load() == 1 })

	cursor, ok := triggers.cursor("user-1", "drive-1")
	if !ok {
		t.Fatal("expected cursor to still exist")
	}
	if !cursor.LastChecked.After(staleTime) {
		t.Errorf("cursor.LastChecked = %v, want advanced past %v", cursor.LastChecked, staleTime)
	}
	if cursor.LastStatus != "full" {
		t.Errorf("cursor.LastStatus = %q, want %q", cursor.LastStatus, "full")
	}
}

func TestReconcileSkipsUnmappedMessage(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: time.Now().Add(-time.Hour), LastStatus: "full"}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{
		"drive-1": {{ID: "act-1", Message: "{user} shared {resource} with {sharee}", Resource: ocisclient.ActivityResource{ID: testResourceID}}},
	})
	exec := &fakeExecutor{}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	paths := &fakePathResolver{pathsByItemID: map[string]string{"opaque-1": "/Invoices/foo.pdf"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, 5*time.Second, 5*time.Second, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	time.Sleep(50 * time.Millisecond)
	if exec.runs.Load() != 0 {
		t.Fatalf("expected 0 runs for an unmapped message (share), got %d", exec.runs.Load())
	}
}

func TestReconcileSkipsInternalBookkeepingPath(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"}, // no path filter — would match anything
	})
	if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: time.Now().Add(-time.Hour), LastStatus: "full"}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{
		"drive-1": {{ID: "act-1", Message: "{user} added {resource} to {folder}", Resource: ocisclient.ActivityResource{ID: testResourceID}}},
	})
	exec := &fakeExecutor{}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	paths := &fakePathResolver{pathsByItemID: map[string]string{"opaque-1": "/.workflows/executions/wf-1/exec-1.json"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, 5*time.Second, 5*time.Second, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	time.Sleep(50 * time.Millisecond)
	if exec.runs.Load() != 0 {
		t.Fatalf("expected 0 runs for a write under .workflows/, got %d", exec.runs.Load())
	}
}

func TestReconcileDebouncesWithinGracePeriod(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: time.Now(), LastStatus: "full"}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{})
	r := New(triggers, &fakeDriveLister{}, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, time.Hour, 5*time.Second, 10, discardLogger())

	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	if activities.callCount.Load() != 0 {
		t.Fatalf("expected no activitylog query within the grace period, got %d calls", activities.callCount.Load())
	}
}

func TestReconcileOnActivityErrorMarksCursorDegraded(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	staleTime := time.Now().Add(-time.Hour)
	if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: staleTime, LastStatus: "full"}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	activities := newFakeActivityLister(nil)
	activities.err = context.DeadlineExceeded

	r := New(triggers, &fakeDriveLister{}, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, 5*time.Second, 5*time.Second, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	cursor, ok := triggers.cursor("user-1", "drive-1")
	if !ok {
		t.Fatal("expected cursor to still exist after a failed query")
	}
	if cursor.LastStatus != "sse-only" {
		t.Errorf("cursor.LastStatus = %q, want %q after a failed activitylog query", cursor.LastStatus, "sse-only")
	}
	if !cursor.LastChecked.Equal(staleTime) {
		t.Errorf("cursor.LastChecked = %v, want unchanged at %v (couldn't verify, don't advance)", cursor.LastChecked, staleTime)
	}
}

func TestReconcileUnscopedTriggerEnumeratesAllDrives(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload"}, // no SpaceID — "any space"
	})
	for _, drive := range []string{"drive-1", "drive-2"} {
		if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: "user-1", DriveID: drive, LastChecked: time.Now().Add(-time.Hour), LastStatus: "full"}); err != nil {
			t.Fatalf("seed cursor(%s): %v", drive, err)
		}
	}
	drives := &fakeDriveLister{drives: []ocisclient.Drive{
		{ID: "drive-1", Name: "Personal", DriveType: "personal"},
		{ID: "drive-2", Name: "Project", DriveType: "project"},
		{ID: "drive-virtual", Name: "Shares", DriveType: "virtual"},
	}}
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{})

	r := New(triggers, drives, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, 5*time.Second, 5*time.Second, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	if activities.callCount.Load() != 2 {
		t.Fatalf("expected exactly 2 activitylog queries (drive-1, drive-2; virtual excluded), got %d", activities.callCount.Load())
	}
}

func TestReconcileScopedTriggerOnlyQueriesItsOwnDrive(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: time.Now().Add(-time.Hour), LastStatus: "full"}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	drives := &fakeDriveLister{drives: []ocisclient.Drive{
		{ID: "drive-1", Name: "Personal", DriveType: "personal"},
		{ID: "drive-2", Name: "Other", DriveType: "project"},
	}}
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{})

	r := New(triggers, drives, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, 5*time.Second, 5*time.Second, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	if activities.callCount.Load() != 1 {
		t.Fatalf("expected exactly 1 activitylog query (only the scoped drive), got %d", activities.callCount.Load())
	}
	if got := activities.lastSince["drive-1"]; got.IsZero() {
		t.Fatalf("expected drive-1 to have been queried")
	}
}

func TestReconcileConcurrencyIsBounded(t *testing.T) {
	maxConcurrent := 2
	var (
		mu        sync.Mutex
		active    int
		maxActive int
	)
	activities := &blockingActivityLister{
		onCall: func() {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
		},
	}

	triggers := newFakeTriggerStore(nil) // per-user entries added below
	users := []string{"user-1", "user-2", "user-3", "user-4", "user-5"}
	for _, u := range users {
		triggers.entries = append(triggers.entries, localdb.TriggerIndexEntry{WorkflowID: "wf-" + u, UserID: u, TriggerType: "event", EventType: "upload", SpaceID: "drive-1"})
		if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: u, DriveID: "drive-1", LastChecked: time.Now().Add(-time.Hour), LastStatus: "full"}); err != nil {
			t.Fatalf("seed cursor(%s): %v", u, err)
		}
	}

	r := New(triggers, &fakeDriveLister{}, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, 5*time.Second, 5*time.Second, maxConcurrent, discardLogger())

	var wg sync.WaitGroup
	for _, u := range users {
		wg.Add(1)
		go func(userID string) {
			defer wg.Done()
			r.Reconcile(t.Context(), userID, "Basic dGVzdA==")
		}(u)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxActive > maxConcurrent {
		t.Errorf("observed maxActive = %d concurrent reconciliations, want <= %d", maxActive, maxConcurrent)
	}
}

type blockingActivityLister struct {
	onCall func()
}

func (b *blockingActivityLister) ListActivities(context.Context, string, string, time.Time) ([]ocisclient.Activity, error) {
	b.onCall()
	return nil, nil
}

// splitResourceID isn't exported — this test exercises it indirectly via Reconcile above
// (fakePathResolver keys on the opaque-id portion it extracts), but a direct unit test
// pins the parsing itself.
func TestSplitResourceID(t *testing.T) {
	cases := []struct {
		in        string
		wantSpace string
		wantItem  string
		wantOK    bool
	}{
		{"storage1$drive-1!opaque-1", "storage1$drive-1", "opaque-1", true},
		{"", "", "", false},
		{"no-bang-here", "", "", false},
	}
	for _, c := range cases {
		gotSpace, gotItem, ok := splitResourceID(c.in)
		if gotSpace != c.wantSpace || gotItem != c.wantItem || ok != c.wantOK {
			t.Errorf("splitResourceID(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, gotSpace, gotItem, ok, c.wantSpace, c.wantItem, c.wantOK)
		}
	}
}
