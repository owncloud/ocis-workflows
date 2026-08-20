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
	// filterBySince, when true, filters byDrive[driveID] down to activities recorded after
	// since before returning — modeling the *real* server-side filtering
	// ocisclient.ListActivities gets from oCIS's activitylog ("timestamp>since" KQL; see
	// activities.go). Off by default (returns the whole canned list unconditionally, like
	// the original tests in this file assume) so existing tests are unaffected; tests that
	// actually need to exercise cursor-driven filtering across multiple passes opt in.
	filterBySince bool
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
	all := f.byDrive[driveID]
	if !f.filterBySince {
		return all, nil
	}
	filtered := make([]ocisclient.Activity, 0, len(all))
	for _, a := range all {
		if a.RecordedTime.After(since) {
			filtered = append(filtered, a)
		}
	}
	return filtered, nil
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

// oCIS's Graph API requires the full compound resourceID as the itemID path segment (a bare
// opaque suffix 404s: itemNotFound) — so fakePathResolver below is keyed on the whole
// testResourceID, matching what splitResourceID now returns as itemID.
const testResourceID = "storage1$drive-1!opaque-1"

// TestReconcileFirstEverPassDispatchesActivityWithinLookback proves a (user, drive) pair
// with no prior cursor at all doesn't just seed a cursor and skip the query — it's exactly
// the scenario this backstop exists for (a brand-new event trigger whose upload raced the
// very first SSE connection), so the very first pass must actually look back and catch it.
func TestReconcileFirstEverPassDispatchesActivityWithinLookback(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	// No cursor seeded for (user-1, drive-1) — this is a genuine first-ever pass.
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{
		"drive-1": {{ID: "act-1", Message: "{user} added {resource} to {folder}", Resource: ocisclient.ActivityResource{ID: testResourceID}, RecordedTime: time.Now().Add(-10 * time.Second)}},
	})
	exec := &fakeExecutor{}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	paths := &fakePathResolver{pathsByItemID: map[string]string{testResourceID: "/Invoices/foo.pdf"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, 5*time.Second, 5*time.Second, 5*time.Minute, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	waitFor(t, time.Second, func() bool { return exec.runs.Load() == 1 })

	if activities.callCount.Load() != 1 {
		t.Fatalf("expected exactly 1 activitylog query on the first-ever pass (not skipped), got %d", activities.callCount.Load())
	}
	if _, ok := triggers.cursor("user-1", "drive-1"); !ok {
		t.Fatal("expected a cursor to be seeded for (user-1, drive-1)")
	}
}

// TestReconcileFirstEverPassExcludesActivityBeforeLookback proves the first-ever lookback is
// actually bounded — a first-ever pass on an old/busy drive must not flood-dispatch its
// entire history, only the genuine just-created-trigger window.
func TestReconcileFirstEverPassExcludesActivityBeforeLookback(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	// No cursor seeded — genuine first-ever pass.
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{
		"drive-1": {{ID: "act-1", Message: "{user} added {resource} to {folder}", Resource: ocisclient.ActivityResource{ID: testResourceID}, RecordedTime: time.Now().Add(-time.Hour)}},
	})
	activities.filterBySince = true // model oCIS's real server-side "timestamp>since" filtering
	exec := &fakeExecutor{}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	paths := &fakePathResolver{pathsByItemID: map[string]string{testResourceID: "/Invoices/foo.pdf"}}

	// A short lookback (a few seconds, not the 5-minute production default) keeps this test
	// fast — the activity was recorded an hour ago, well outside any realistic lookback, so
	// the exact magnitude doesn't matter as long as it's meaningfully bounded rather than
	// unbounded.
	firstConnectLookback := 2 * time.Second
	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, time.Second, time.Second, firstConnectLookback, 10, discardLogger())
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	if activities.callCount.Load() != 1 {
		t.Fatalf("expected the first-ever pass to query activitylog (bounded, not skipped), got %d calls", activities.callCount.Load())
	}
	if exec.runs.Load() != 0 {
		t.Fatalf("expected 0 runs for an activity recorded well before the lookback window, got %d", exec.runs.Load())
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
	paths := &fakePathResolver{pathsByItemID: map[string]string{testResourceID: "/Invoices/foo.pdf"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, 5*time.Second, 5*time.Second, time.Hour, 10, discardLogger())
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

// TestReconcileDoesNotRepeatDispatchOnSubsequentPass proves the cursor advances to
// wall-clock "now" rather than getting stuck at the last-seen activity's own RecordedTime.
// The bug this guards against: if the cursor instead tracked
// max(activity.RecordedTime) seen, a drive that goes quiet after one upload would never
// advance its cursor past that upload's timestamp, so every future pass would recompute
// since = thatTimestamp - overlap (a fixed value, independent of how much real time has
// passed) and the same activity would fall inside it *forever* — a permanent, unbounded
// repeat dispatch on every subsequent reconnect, not just an occasional one.
//
// This is deliberately not modeled by manually rewriting the stored cursor backwards
// in time: since is a pure function of the stored cursor value and overlap, so pushing the
// cursor further into the past only widens the query window and would reproduce a
// re-dispatch under *either* the buggy or the fixed cursor semantics — it can't
// discriminate between them. What actually distinguishes the two is letting real elapsed
// time separate (a) the activity's own timestamp from the pass that first discovers it, and
// (b) that first pass from a later one — mirroring how the design doc (section 4) frames
// this: an occasional single re-fire right at the query boundary is an accepted trade-off,
// but it must not recur on every pass forever. So this test uses small, real gracePeriod/
// overlap values and real (short) sleeps for that separation, and turns on the fake
// ActivityLister's since-filtering (off by default in other tests) to model what oCIS's
// real activitylog actually does with the since parameter (see ListActivities's
// "timestamp>since" KQL, ocisclient/activities.go) — without that, the fake would keep
// returning the activity forever regardless of the fix, and this test couldn't tell the two
// implementations apart either.
func TestReconcileDoesNotRepeatDispatchOnSubsequentPass(t *testing.T) {
	triggers := newFakeTriggerStore([]localdb.TriggerIndexEntry{
		{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "drive-1"},
	})
	// Seed a stale cursor so the first call isn't treated as a first-ever call.
	if err := triggers.UpsertEventCursor(t.Context(), localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: time.Now().Add(-time.Hour), LastStatus: "full"}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	gracePeriod := 50 * time.Millisecond
	overlap := 50 * time.Millisecond
	margin := 100 * time.Millisecond // comfortably larger than typical goroutine scheduling jitter

	activityTime := time.Now()
	activities := newFakeActivityLister(map[string][]ocisclient.Activity{
		"drive-1": {{ID: "act-1", Message: "{user} added {resource} to {folder}", Resource: ocisclient.ActivityResource{ID: testResourceID}, RecordedTime: activityTime}},
	})
	activities.filterBySince = true

	exec := &fakeExecutor{}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{"wf-1": {ID: "wf-1", Enabled: true}}}
	paths := &fakePathResolver{pathsByItemID: map[string]string{testResourceID: "/Invoices/foo.pdf"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, gracePeriod, overlap, time.Hour, 10, discardLogger())

	// Give the activity a head start past the overlap window before the reconciler ever
	// sees it, so pass 1's cursor (advanced to "now") already sits more than `overlap` past
	// the activity's own timestamp — realistic (uploads aren't discovered instantaneously)
	// and necessary for the math below to actually separate the two cases.
	time.Sleep(overlap + margin)

	// Pass 1: discovers and dispatches the activity; advances the cursor to (approximately)
	// its own wall-clock "now", not to the activity's RecordedTime.
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")
	waitFor(t, time.Second, func() bool { return exec.runs.Load() == 1 })

	// Wait past the grace period so the next pass isn't debounced — simulating another SSE
	// reconnect on an otherwise-quiet drive.
	time.Sleep(gracePeriod + margin)

	// Pass 2: nothing new has happened on the drive. Under the fix, since (derived from
	// pass 1's now-based cursor) has moved past the activity's own timestamp by more than
	// overlap, so the (since-filtering) activitylog no longer returns it — it must not be
	// redispatched.
	r.Reconcile(t.Context(), "user-1", "Basic dGVzdA==")

	if got := exec.runs.Load(); got != 1 {
		t.Fatalf("exec.runs = %d after a second reconnect pass with no new activity, want 1 (no repeat dispatch on an already-handled, still-quiet drive)", got)
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
	paths := &fakePathResolver{pathsByItemID: map[string]string{testResourceID: "/Invoices/foo.pdf"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, 5*time.Second, 5*time.Second, time.Hour, 10, discardLogger())
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
	paths := &fakePathResolver{pathsByItemID: map[string]string{testResourceID: "/.workflows/executions/wf-1/exec-1.json"}}

	r := New(triggers, &fakeDriveLister{}, activities, paths, exec, store, 5*time.Second, 5*time.Second, time.Hour, 10, discardLogger())
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
	r := New(triggers, &fakeDriveLister{}, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, time.Hour, 5*time.Second, time.Hour, 10, discardLogger())

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

	r := New(triggers, &fakeDriveLister{}, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, 5*time.Second, 5*time.Second, time.Hour, 10, discardLogger())
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

	r := New(triggers, drives, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, 5*time.Second, 5*time.Second, time.Hour, 10, discardLogger())
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

	r := New(triggers, drives, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, 5*time.Second, 5*time.Second, time.Hour, 10, discardLogger())
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

	r := New(triggers, &fakeDriveLister{}, activities, &fakePathResolver{}, &fakeExecutor{}, &fakeWorkflowStore{}, 5*time.Second, 5*time.Second, time.Hour, maxConcurrent, discardLogger())

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
// (fakePathResolver keys on the full compound id it returns as itemID, since oCIS's Graph
// API requires the full compound resourceID as the itemID path segment), but a direct unit
// test pins the parsing itself.
func TestSplitResourceID(t *testing.T) {
	cases := []struct {
		in        string
		wantSpace string
		wantItem  string
		wantOK    bool
	}{
		{"storage1$drive-1!opaque-1", "storage1$drive-1", "storage1$drive-1!opaque-1", true},
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
