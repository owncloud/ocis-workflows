# Event-Trigger Reliability Backstop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the silent event-loss window in SSE-driven file-event triggers by adding an activitylog-based reconciliation backstop that runs whenever a user's SSE connection reconnects after a gap.

**Architecture:** A new `backend/pkg/reconcile` package holds a `Reconciler` that, given a user, derives which oCIS drives their event triggers care about (from the trigger index, reusing PR #31's `SpaceID` field and `ListDrives`), queries oCIS's activitylog for anything that happened on those drives since a persisted per-`(user, drive)` cursor, maps the small fixed set of activitylog message templates back to this backend's trigger vocabulary, and dispatches matching workflows exactly like `sse.Manager.handleEvent` already does. `sse.Manager` gains one hook: after `streamOnce` re-establishes a connection, it asynchronously calls into the `Reconciler`. A concurrency-capped semaphore inside the `Reconciler` keeps a fleet-wide reconnect event (e.g. oCIS's own `sse` service restarting) from firing unbounded simultaneous requests. Degraded reliability (activitylog unavailable) is persisted per cursor row and surfaced through a new `reliability` field on `GET /me/automation`.

**Tech Stack:** Go (existing backend), SQLite via `modernc.org/sqlite` (existing `localdb`), oCIS Graph API extensions (`activitylog`), no new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-20-event-trigger-reliability-design.md`

## Global Constraints

- **Precondition, not a task in this plan:** [PR #31](https://github.com/owncloud/ocis-workflows/pull/31) ("scope file-event triggers to a specific oCIS space") must be merged to `main` first, and this feature branch based on top of that merge. This plan's diffs to `backend/pkg/localdb/localdb.go` and `backend/pkg/sse/manager.go` assume the post-PR#31 shape of those files (i.e. `TriggerIndexEntry.SpaceID` already exists and is persisted/matched). Task 1, Step 1 verifies this precondition before touching anything.
- Non-goals from the spec (do not implement): `share`/`lock` trigger backstopping, direct NATS consumption, frontend surfacing of the new `reliability` field (backend contract only).
- Tunable defaults (all from the spec, all `const` in Go, not user-configurable in this iteration): reconciliation grace period / debounce window = `5 * time.Second`; cursor overlap window = `5 * time.Second`; max concurrent reconciliation requests instance-wide = `10`.
- `reliability` field values are exactly the strings `"full"` and `"sse-only"` — used verbatim in Go code, SQL default, and JSON.
- Message-template strings used for mapping must match the exact oCIS constants verified live: `{user} added {resource} to {folder}` (upload), `{user} moved {resource} to {folder}` (move), `{user} renamed {oldResource} to {resource}` (move). Copy these exactly — a single character difference silently breaks matching with no compiler error.
- Every new/modified file follows this codebase's existing conventions: `context.Context` as first param, `log/slog` for logging, `fmt.Errorf("...: %w", err)` for wrapping, small per-package interfaces over concrete types for testability (see `sse.TriggerStore`, `sse.WorkflowStore` for the established pattern), table-free plain Go for control flow, no doc comments beyond a one-line "what/why" on exported identifiers.

---

## Task 1: Shared trigger-filter matcher (DRY prep, no new behavior)

Both the existing SSE path and the new reconciliation path need to test a `localdb.TriggerIndexEntry`'s path-prefix/extension/space filters against a resolved path. Extracting this now means the two paths can't silently drift apart later. This task is a pure refactor — behavior is unchanged, only proven by keeping all existing tests green plus one new direct test of the extracted method.

**Files:**
- Modify: `backend/pkg/localdb/localdb.go`
- Modify: `backend/pkg/localdb/localdb_test.go`
- Modify: `backend/pkg/sse/manager.go`

**Interfaces:**
- Produces: `func (e TriggerIndexEntry) MatchesFilters(path, spaceID string) bool` on `localdb.TriggerIndexEntry` — used by Task 5's `reconcile.Reconciler` and by the refactored `sse.Manager.handleEvent`.

- [ ] **Step 0: Verify the PR #31 precondition**

Run: `grep -n "SpaceID" backend/pkg/localdb/localdb.go`
Expected: a `SpaceID` field on `TriggerIndexEntry` and `space_id` references in `UpsertTriggerIndexEntry`/`listTriggers`. If this doesn't exist yet, STOP — merge PR #31 into `main` and rebase this branch before continuing.

- [ ] **Step 1: Write the failing test**

Add to `backend/pkg/localdb/localdb_test.go`:

```go
func TestTriggerIndexEntryMatchesFilters(t *testing.T) {
	cases := []struct {
		name    string
		entry   TriggerIndexEntry
		path    string
		spaceID string
		want    bool
	}{
		{"no filters matches anything", TriggerIndexEntry{}, "/any/path.txt", "space-1", true},
		{"path prefix matches", TriggerIndexEntry{PathPrefix: "/Invoices"}, "/Invoices/foo.pdf", "space-1", true},
		{"path prefix rejects", TriggerIndexEntry{PathPrefix: "/Invoices"}, "/Photos/foo.jpg", "space-1", false},
		{"extension matches", TriggerIndexEntry{Extension: ".pdf"}, "/foo.pdf", "space-1", true},
		{"extension rejects", TriggerIndexEntry{Extension: ".pdf"}, "/foo.jpg", "space-1", false},
		{"space id matches", TriggerIndexEntry{SpaceID: "space-1"}, "/foo.pdf", "space-1", true},
		{"space id rejects", TriggerIndexEntry{SpaceID: "space-1"}, "/foo.pdf", "space-2", false},
		{"all filters combined, all pass", TriggerIndexEntry{PathPrefix: "/Invoices", Extension: ".pdf", SpaceID: "space-1"}, "/Invoices/foo.pdf", "space-1", true},
		{"all filters combined, one fails", TriggerIndexEntry{PathPrefix: "/Invoices", Extension: ".pdf", SpaceID: "space-1"}, "/Invoices/foo.pdf", "space-2", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.entry.MatchesFilters(c.path, c.spaceID); got != c.want {
				t.Errorf("MatchesFilters(%q, %q) = %v, want %v", c.path, c.spaceID, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./pkg/localdb/... -run TestTriggerIndexEntryMatchesFilters -v`
Expected: FAIL with `entry.MatchesFilters undefined`.

- [ ] **Step 3: Implement `MatchesFilters`**

Add to `backend/pkg/localdb/localdb.go`, near the `TriggerIndexEntry` type definition (needs `"strings"` added to the existing import block):

```go
// MatchesFilters reports whether e's path-prefix, extension, and space filters (any of
// which may be unset, meaning "no restriction") admit an event at the given resolved
// WebDAV path and originating space id. Does not check UserID or EventType — callers
// filter on those first, since it's cheaper than resolving a path.
func (e TriggerIndexEntry) MatchesFilters(path, spaceID string) bool {
	if e.PathPrefix != "" && !strings.HasPrefix(path, e.PathPrefix) {
		return false
	}
	if e.Extension != "" && !strings.HasSuffix(path, e.Extension) {
		return false
	}
	if e.SpaceID != "" && e.SpaceID != spaceID {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./pkg/localdb/... -run TestTriggerIndexEntryMatchesFilters -v`
Expected: PASS, all subtests.

- [ ] **Step 5: Refactor `sse.Manager.handleEvent` to use it**

In `backend/pkg/sse/manager.go`, replace the three separate filter checks inside `handleEvent`'s loop:

```go
		if e.PathPrefix != "" && !strings.HasPrefix(resolvedPath, e.PathPrefix) {
			continue
		}
		if e.Extension != "" && !strings.HasSuffix(resolvedPath, e.Extension) {
			continue
		}
		if e.SpaceID != "" && e.SpaceID != payload.SpaceID {
			continue
		}
```

with:

```go
		if !e.MatchesFilters(resolvedPath, payload.SpaceID) {
			continue
		}
```

If this leaves `"strings"` unused in `manager.go`, remove it from the import block; check with `go build` (imports elsewhere in the file, e.g. `strings.TrimRight`/`strings.TrimSpace`/`strings.HasPrefix` in `handleEvent`'s SSE parsing, likely still need it — verify before removing).

- [ ] **Step 6: Run full existing SSE test suite to verify no regression**

Run: `cd backend && go test ./pkg/sse/... -v`
Expected: PASS, every existing test (`TestStreamOnceDispatchesMatchingEventTrigger`, `TestHandleEventSkipsInternalBookkeepingPath`, `TestHandleEventSkipsNonMatchingPathPrefix`, `TestHandleEventIgnoresUnmappedSSEEventType`, `TestReconcileRetriesConsumerAfterTransientGetAutomationFailure`, `TestKickTriggersImmediateReconcile`, `TestReconcileStartsAndStopsConsumersAsTriggersChange`) still green, unchanged behavior.

- [ ] **Step 7: Commit**

```bash
cd backend && go build ./... && go vet ./...
git add pkg/localdb/localdb.go pkg/localdb/localdb_test.go pkg/sse/manager.go
git commit -s -m "refactor(backend): extract TriggerIndexEntry.MatchesFilters

Shared by sse.Manager today and reconcile.Reconciler in a later commit,
so the two event paths can't silently drift apart on filter semantics."
```

---

## Task 2: Event-cursor storage in localdb

**Files:**
- Modify: `backend/pkg/localdb/localdb.go`
- Modify: `backend/pkg/localdb/localdb_test.go`

**Interfaces:**
- Produces: `type EventCursor struct { UserID, DriveID string; LastChecked time.Time; LastStatus string }`, `func (db *DB) GetEventCursor(ctx context.Context, userID, driveID string) (*EventCursor, error)` (returns `ErrNotFound` if no row), `func (db *DB) UpsertEventCursor(ctx context.Context, c EventCursor) error`, `func (db *DB) GetReliability(ctx context.Context, userID string) (string, error)` (returns `"full"` or `"sse-only"`, never errors on "no rows found" — that means `"full"`).
- Consumes: existing `db.migrate()`/`db.addColumnIfMissing` pattern, `ErrNotFound`.

- [ ] **Step 1: Write the failing tests**

Add to `backend/pkg/localdb/localdb_test.go`:

```go
func TestEventCursorRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	checked := time.Now().Truncate(time.Second)
	err := db.UpsertEventCursor(ctx, EventCursor{
		UserID: "user-1", DriveID: "drive-1", LastChecked: checked, LastStatus: "full",
	})
	if err != nil {
		t.Fatalf("UpsertEventCursor: %v", err)
	}

	got, err := db.GetEventCursor(ctx, "user-1", "drive-1")
	if err != nil {
		t.Fatalf("GetEventCursor: %v", err)
	}
	if !got.LastChecked.Equal(checked) {
		t.Errorf("LastChecked = %v, want %v", got.LastChecked, checked)
	}
	if got.LastStatus != "full" {
		t.Errorf("LastStatus = %q, want %q", got.LastStatus, "full")
	}
}

func TestGetEventCursorNotFound(t *testing.T) {
	db := testDB(t)
	if _, err := db.GetEventCursor(t.Context(), "user-1", "drive-1"); err != ErrNotFound {
		t.Fatalf("GetEventCursor: err = %v, want ErrNotFound", err)
	}
}

func TestUpsertEventCursorOverwritesExisting(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	first := time.Now().Add(-time.Hour).Truncate(time.Second)
	second := time.Now().Truncate(time.Second)

	if err := db.UpsertEventCursor(ctx, EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: first, LastStatus: "full"}); err != nil {
		t.Fatalf("first UpsertEventCursor: %v", err)
	}
	if err := db.UpsertEventCursor(ctx, EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: second, LastStatus: "sse-only"}); err != nil {
		t.Fatalf("second UpsertEventCursor: %v", err)
	}

	got, err := db.GetEventCursor(ctx, "user-1", "drive-1")
	if err != nil {
		t.Fatalf("GetEventCursor: %v", err)
	}
	if !got.LastChecked.Equal(second) {
		t.Errorf("LastChecked = %v, want the second write %v", got.LastChecked, second)
	}
	if got.LastStatus != "sse-only" {
		t.Errorf("LastStatus = %q, want %q", got.LastStatus, "sse-only")
	}
}

func TestGetReliabilityFullWhenNoCursorsExist(t *testing.T) {
	db := testDB(t)
	got, err := db.GetReliability(t.Context(), "user-1")
	if err != nil {
		t.Fatalf("GetReliability: %v", err)
	}
	if got != "full" {
		t.Errorf("GetReliability = %q, want %q", got, "full")
	}
}

func TestGetReliabilityFullWhenAllCursorsFull(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	now := time.Now().Truncate(time.Second)
	for _, drive := range []string{"drive-1", "drive-2"} {
		if err := db.UpsertEventCursor(ctx, EventCursor{UserID: "user-1", DriveID: drive, LastChecked: now, LastStatus: "full"}); err != nil {
			t.Fatalf("UpsertEventCursor(%s): %v", drive, err)
		}
	}
	got, err := db.GetReliability(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetReliability: %v", err)
	}
	if got != "full" {
		t.Errorf("GetReliability = %q, want %q", got, "full")
	}
}

func TestGetReliabilityDegradedWhenAnyCursorIsSSEOnly(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	now := time.Now().Truncate(time.Second)
	if err := db.UpsertEventCursor(ctx, EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: now, LastStatus: "full"}); err != nil {
		t.Fatalf("UpsertEventCursor(drive-1): %v", err)
	}
	if err := db.UpsertEventCursor(ctx, EventCursor{UserID: "user-1", DriveID: "drive-2", LastChecked: now, LastStatus: "sse-only"}); err != nil {
		t.Fatalf("UpsertEventCursor(drive-2): %v", err)
	}
	got, err := db.GetReliability(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetReliability: %v", err)
	}
	if got != "sse-only" {
		t.Errorf("GetReliability = %q, want %q", got, "sse-only")
	}
}

func TestGetReliabilityScopedPerUser(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	now := time.Now().Truncate(time.Second)
	if err := db.UpsertEventCursor(ctx, EventCursor{UserID: "user-2", DriveID: "drive-1", LastChecked: now, LastStatus: "sse-only"}); err != nil {
		t.Fatalf("UpsertEventCursor: %v", err)
	}
	got, err := db.GetReliability(ctx, "user-1") // different user, no rows of their own
	if err != nil {
		t.Fatalf("GetReliability: %v", err)
	}
	if got != "full" {
		t.Errorf("GetReliability(user-1) = %q, want %q — should not see user-2's degraded row", got, "full")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./pkg/localdb/... -run 'TestEventCursor|TestGetEventCursor|TestUpsertEventCursor|TestGetReliability' -v`
Expected: FAIL to compile — `EventCursor`, `GetEventCursor`, `UpsertEventCursor`, `GetReliability` undefined.

- [ ] **Step 3: Implement**

Add the `event_cursors` table to `db.migrate()`'s existing `CREATE TABLE IF NOT EXISTS` SQL block in `backend/pkg/localdb/localdb.go` (append after the `trigger_index` table definition, inside the same `db.sql.Exec` call):

```go
		CREATE TABLE IF NOT EXISTS event_cursors (
			user_id TEXT NOT NULL,
			drive_id TEXT NOT NULL,
			last_checked TEXT NOT NULL,
			last_status TEXT NOT NULL DEFAULT 'full',
			PRIMARY KEY (user_id, drive_id)
		);
```

Add the type and methods (near `TriggerIndexEntry`/its CRUD methods):

```go
// EventCursor tracks the last time this backend successfully reconciled a user's drive
// against oCIS's activitylog, closing any gap the SSE connection may have missed while it
// was down. LastStatus records whether that reconciliation attempt actually succeeded
// ("full") or the activitylog query itself failed ("sse-only") — surfaced to the user via
// GET /me/automation's reliability field.
type EventCursor struct {
	UserID      string
	DriveID     string
	LastChecked time.Time
	LastStatus  string // "full" | "sse-only"
}

// GetEventCursor returns the stored cursor for (userID, driveID), or ErrNotFound if this
// pair has never been reconciled.
func (db *DB) GetEventCursor(ctx context.Context, userID, driveID string) (*EventCursor, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT user_id, drive_id, last_checked, last_status
		FROM event_cursors WHERE user_id = ? AND drive_id = ?
	`, userID, driveID)

	var c EventCursor
	var lastChecked string
	if err := row.Scan(&c.UserID, &c.DriveID, &lastChecked, &c.LastStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.LastChecked, _ = time.Parse(time.RFC3339, lastChecked)
	return &c, nil
}

// UpsertEventCursor stores or replaces the cursor for (c.UserID, c.DriveID).
func (db *DB) UpsertEventCursor(ctx context.Context, c EventCursor) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO event_cursors (user_id, drive_id, last_checked, last_status)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, drive_id) DO UPDATE SET
			last_checked = excluded.last_checked,
			last_status = excluded.last_status
	`, c.UserID, c.DriveID, c.LastChecked.UTC().Format(time.RFC3339), c.LastStatus)
	return err
}

// GetReliability reports "sse-only" if userID has any event-cursor row currently marked
// degraded, "full" otherwise (including when userID has no cursor rows at all — nothing
// has been found unreliable yet).
func (db *DB) GetReliability(ctx context.Context, userID string) (string, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT 1 FROM event_cursors WHERE user_id = ? AND last_status = 'sse-only' LIMIT 1
	`, userID)

	var found int
	err := row.Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return "full", nil
	}
	if err != nil {
		return "", err
	}
	return "sse-only", nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./pkg/localdb/... -v`
Expected: PASS, all tests including the new ones and every pre-existing one.

- [ ] **Step 5: Commit**

```bash
cd backend && go build ./... && go vet ./...
git add pkg/localdb/localdb.go pkg/localdb/localdb_test.go
git commit -s -m "feat(backend): add event_cursors table for reconciliation state

Per-(user, drive) cursor tracking the last time this backend checked
oCIS's activitylog for events the SSE connection may have missed, plus
whether that check itself succeeded — the basis for the reliability
field added to GET /me/automation in a later commit."
```

---

## Task 3: oCIS activitylog client

**Files:**
- Create: `backend/pkg/ocisclient/activities.go`
- Create: `backend/pkg/ocisclient/activities_test.go`

**Interfaces:**
- Produces: `type Activity struct { ID, Message string; Resource ActivityResource; RecordedTime time.Time }`, `type ActivityResource struct { ID, Name string }`, `func (c *Client) ListActivities(ctx context.Context, authHeader, driveID string, since time.Time) ([]Activity, error)`.
- Consumes: `Client.baseURL`, `Client.httpClient` (both already private fields on `*ocisclient.Client`, used the same way `ItemPath`/`ResolveItemID`/`ListDrives` already do).

- [ ] **Step 1: Write the failing test**

Create `backend/pkg/ocisclient/activities_test.go`:

```go
package ocisclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// activitiesFixture is a real captured response shape (verified live against a running
// oCIS instance: an upload followed by a rename), used to pin this client's parsing against
// what oCIS actually sends rather than an idealized shape.
const activitiesFixture = `{"value":[
	{"id":"c7099e86-e426-4731-8669-fa5a835bfced","template":{"message":"{user} added {resource} to {folder}","variables":{"folder":{"id":"","name":"Admin"},"resource":{"id":"","name":"spike-move-src.txt"},"user":{"id":"7ef1babf-8c0d-43b8-936d-08c18cbe5769","displayName":"Admin"}}},"times":{"recordedTime":"2026-08-20T15:12:49.130904089Z"}},
	{"id":"0af2221b-4d6e-4e47-aa7f-670853178c63","template":{"message":"{user} renamed {oldResource} to {resource}","variables":{"folder":{"id":"","name":"Admin"},"oldResource":{"id":"","name":"spike-move-src.txt"},"resource":{"id":"31887342-c711-4c0f-973a-bf2a23400fd9$7ef1babf-8c0d-43b8-936d-08c18cbe5769!f3432faf-bf88-42d8-821f-7bfeb409e6b2","name":"spike-move-dst.txt"},"user":{"id":"7ef1babf-8c0d-43b8-936d-08c18cbe5769","displayName":"Admin"}}},"times":{"recordedTime":"2026-08-20T15:12:49.17125613Z"}}
]}`

func TestListActivitiesParsesRealResponseShape(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(activitiesFixture))
	}))
	defer srv.Close()

	c := New(srv.URL, false)
	since := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)
	activities, err := c.ListActivities(t.Context(), "Basic dGVzdA==", "drive-1", since)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}

	if gotAuth != "Basic dGVzdA==" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Basic dGVzdA==")
	}
	if want := "/graph/v1beta1/extensions/org.libregraph/activities"; !containsPrefix(gotPath, want) {
		t.Errorf("request path = %q, want prefix %q", gotPath, want)
	}
	if !containsAll(gotPath, "itemid:drive-1", "depth:-1", "timestamp>2026-08-20T15:00:00Z") {
		t.Errorf("query kql missing expected clauses: %q", gotPath)
	}

	if len(activities) != 2 {
		t.Fatalf("len(activities) = %d, want 2", len(activities))
	}

	if activities[0].Message != "{user} added {resource} to {folder}" {
		t.Errorf("activities[0].Message = %q", activities[0].Message)
	}
	if activities[0].ID != "c7099e86-e426-4731-8669-fa5a835bfced" {
		t.Errorf("activities[0].ID = %q", activities[0].ID)
	}
	wantTime := time.Date(2026, 8, 20, 15, 12, 49, 130904089, time.UTC)
	if !activities[0].RecordedTime.Equal(wantTime) {
		t.Errorf("activities[0].RecordedTime = %v, want %v", activities[0].RecordedTime, wantTime)
	}

	if activities[1].Message != "{user} renamed {oldResource} to {resource}" {
		t.Errorf("activities[1].Message = %q", activities[1].Message)
	}
	wantResourceID := "31887342-c711-4c0f-973a-bf2a23400fd9$7ef1babf-8c0d-43b8-936d-08c18cbe5769!f3432faf-bf88-42d8-821f-7bfeb409e6b2"
	if activities[1].Resource.ID != wantResourceID {
		t.Errorf("activities[1].Resource.ID = %q, want %q", activities[1].Resource.ID, wantResourceID)
	}
	if activities[1].Resource.Name != "spike-move-dst.txt" {
		t.Errorf("activities[1].Resource.Name = %q", activities[1].Resource.Name)
	}

	// The first activity's resource.id is empty in the real fixture (oCIS omits it for a
	// freshly-added file whose parent lookup raced the write) — must not crash or panic.
	if activities[0].Resource.ID != "" {
		t.Errorf("activities[0].Resource.ID = %q, want empty (matches real fixture)", activities[0].Resource.ID)
	}
}

func TestListActivitiesNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL, false)
	_, err := c.ListActivities(t.Context(), "Basic dGVzdA==", "drive-1", time.Now())
	if err == nil {
		t.Fatal("ListActivities: err = nil, want an error for a non-200 response")
	}
}

func containsPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./pkg/ocisclient/... -run TestListActivities -v`
Expected: FAIL to compile — `ListActivities`, `Activity`, `ActivityResource` undefined.

- [ ] **Step 3: Implement**

Create `backend/pkg/ocisclient/activities.go`:

```go
package ocisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ActivityResource is the resource an activitylog entry refers to. ID arrives pre-formatted
// as the compound "storageid$spaceid!opaqueid" resourceId — the same format ItemPath
// already consumes — but can be empty for some entry types (e.g. observed live: a
// freshly-added file's own "resource" variable sometimes has no id, only a name).
type ActivityResource struct {
	ID   string
	Name string
}

// Activity is one entry from oCIS's activitylog service. Message is one of a small, fixed
// set of untranslated template strings (verified against oCIS source: the message is never
// translated server-side, only some auxiliary variables are) — safe to match by exact
// string equality.
type Activity struct {
	ID           string
	Message      string
	Resource     ActivityResource
	RecordedTime time.Time
}

type activitiesResponse struct {
	Value []struct {
		ID       string `json:"id"`
		Template struct {
			Message   string `json:"message"`
			Variables struct {
				Resource struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"resource"`
			} `json:"variables"`
		} `json:"template"`
		Times struct {
			RecordedTime time.Time `json:"recordedTime"`
		} `json:"times"`
	} `json:"value"`
}

// ListActivities returns every activitylog entry recorded for driveID (and everything
// under it, via depth:-1) strictly after since, via oCIS's activitylog Graph extension.
func (c *Client) ListActivities(ctx context.Context, authHeader, driveID string, since time.Time) ([]Activity, error) {
	kql := fmt.Sprintf("itemid:%s AND depth:-1 AND timestamp>%s", driveID, since.UTC().Format(time.RFC3339))
	u := c.baseURL + "/graph/v1beta1/extensions/org.libregraph/activities?" +
		url.Values{"kql": {kql}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list activities returned status %d", res.StatusCode)
	}

	var parsed activitiesResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	activities := make([]Activity, 0, len(parsed.Value))
	for _, v := range parsed.Value {
		activities = append(activities, Activity{
			ID:      v.ID,
			Message: v.Template.Message,
			Resource: ActivityResource{
				ID:   v.Template.Variables.Resource.ID,
				Name: v.Template.Variables.Resource.Name,
			},
			RecordedTime: v.Times.RecordedTime,
		})
	}
	return activities, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./pkg/ocisclient/... -v`
Expected: PASS, all tests including pre-existing ones for `ResolveItemID`/`AssignTag`/`ItemPath`/`ListDrives`.

- [ ] **Step 5: Commit**

```bash
cd backend && go build ./... && go vet ./...
git add pkg/ocisclient/activities.go pkg/ocisclient/activities_test.go
git commit -s -m "feat(backend): add ListActivities oCIS activitylog client

Parsing pinned against a real captured response (upload + rename)
from a live oCIS instance, including the empty resource.id case
observed on a freshly-added file."
```

---

## Task 4: Activitylog message-to-trigger-type mapping

**Files:**
- Create: `backend/pkg/reconcile/mapping.go`
- Create: `backend/pkg/reconcile/mapping_test.go`

**Interfaces:**
- Produces: `func TriggerType(message string) (triggerType string, ok bool)`.

- [ ] **Step 1: Write the failing test**

Create `backend/pkg/reconcile/mapping_test.go`:

```go
package reconcile

import "testing"

func TestTriggerType(t *testing.T) {
	cases := []struct {
		message string
		want    string
		wantOK  bool
	}{
		{"{user} added {resource} to {folder}", "upload", true},
		{"{user} moved {resource} to {folder}", "move", true},
		{"{user} renamed {oldResource} to {resource}", "move", true},
		{"{user} shared {resource} with {sharee}", "", false},       // share — not backstopped yet
		{"{user} deleted {resource} from {folder}", "", false},      // trashed — not a trigger type
		{"", "", false},
		{"some unrecognized future message", "", false},
	}
	for _, c := range cases {
		got, ok := TriggerType(c.message)
		if got != c.want || ok != c.wantOK {
			t.Errorf("TriggerType(%q) = (%q, %v), want (%q, %v)", c.message, got, ok, c.want, c.wantOK)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./pkg/reconcile/... -v`
Expected: FAIL to compile — package `reconcile` / `TriggerType` doesn't exist yet.

- [ ] **Step 3: Implement**

Create `backend/pkg/reconcile/mapping.go`:

```go
// Package reconcile backstops oCIS's SSE notification stream, which is live-only and
// silently drops any event that fires while a user's connection is down or reconnecting
// (see sse.Manager's package doc). It queries oCIS's activitylog service — which persists
// event history independently of whether this backend was listening — for anything that
// happened since the last time a given user's drive was checked, and dispatches matching
// workflows the same way sse.Manager.handleEvent does for live events.
package reconcile

// messageToTriggerType maps oCIS activitylog's fixed, untranslated message-template
// constants (verified against oCIS source: services/activitylog/pkg/service/response.go's
// NewActivity never translates the message itself) to this backend's own trigger-type
// vocabulary. Only upload/move are covered in this iteration — share and lock triggers stay
// SSE-only (lock has no activitylog message at all to map from).
var messageToTriggerType = map[string]string{
	"{user} added {resource} to {folder}":       "upload",
	"{user} moved {resource} to {folder}":       "move",
	"{user} renamed {oldResource} to {resource}": "move",
}

// TriggerType returns the trigger type an activitylog message maps to, and whether it's
// one this backstop handles at all — any other message (share, trash, unrecognized future
// additions) is deliberately ignored, not an error.
func TriggerType(message string) (string, bool) {
	t, ok := messageToTriggerType[message]
	return t, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./pkg/reconcile/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && go build ./... && go vet ./...
git add pkg/reconcile/mapping.go pkg/reconcile/mapping_test.go
git commit -s -m "feat(backend): add activitylog message-to-trigger-type mapping"
```

---

## Task 5: Reconciler core

**Files:**
- Create: `backend/pkg/reconcile/reconciler.go`
- Create: `backend/pkg/reconcile/reconciler_test.go`

**Interfaces:**
- Consumes: `localdb.TriggerIndexEntry` (+ `.MatchesFilters` from Task 1), `localdb.EventCursor`, `localdb.ErrNotFound` (Task 2), `ocisclient.Activity`/`ActivityResource` (Task 3), `reconcile.TriggerType` (Task 4), `model.WorkflowDefinition`, `model.ExecutionRecord`, `webdavstore.IsInternalPath`.
- Produces: `type Reconciler struct{...}`, `func New(db TriggerStore, drives DriveLister, activities ActivityLister, paths PathResolver, executor Executor, store WorkflowStore, gracePeriod, overlap time.Duration, maxConcurrent int, log *slog.Logger) *Reconciler`, `func (r *Reconciler) Reconcile(ctx context.Context, userID, authHeader string)` — the exact method `sse.Manager` will call in Task 6.

- [ ] **Step 1: Write the failing tests**

Create `backend/pkg/reconcile/reconciler_test.go`:

```go
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
		in           string
		wantSpace    string
		wantItem     string
		wantOK       bool
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./pkg/reconcile/... -v`
Expected: FAIL to compile — `New`, `Reconciler`, `Reconcile`, `splitResourceID`, and the `TriggerStore`/`DriveLister`/`ActivityLister`/`PathResolver`/`Executor`/`WorkflowStore` interfaces don't exist yet.

- [ ] **Step 3: Implement**

Create `backend/pkg/reconcile/reconciler.go`:

```go
package reconcile

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/ocisclient"
	"github.com/owncloud/ocis-workflows/pkg/webdavstore"
)

// TriggerStore is the subset of localdb.DB the Reconciler needs.
type TriggerStore interface {
	ListEventTriggers(ctx context.Context) ([]localdb.TriggerIndexEntry, error)
	GetEventCursor(ctx context.Context, userID, driveID string) (*localdb.EventCursor, error)
	UpsertEventCursor(ctx context.Context, c localdb.EventCursor) error
}

// DriveLister enumerates a user's accessible drives, needed only for triggers with no
// SpaceID filter ("any space"). Satisfied by *ocisclient.Client.
type DriveLister interface {
	ListDrives(ctx context.Context, authHeader string) ([]ocisclient.Drive, error)
}

// ActivityLister queries oCIS's activitylog for a drive's activity since a cursor.
// Satisfied by *ocisclient.Client.
type ActivityLister interface {
	ListActivities(ctx context.Context, authHeader, driveID string, since time.Time) ([]ocisclient.Activity, error)
}

// PathResolver resolves a resource's space+item id to a WebDAV path. Satisfied by
// *ocisclient.Client.
type PathResolver interface {
	ItemPath(ctx context.Context, authHeader, spaceID, itemID string) (string, error)
}

// Executor runs a workflow's graph. Satisfied by *executor.Executor.
type Executor interface {
	Run(ctx context.Context, authHeader string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord
}

// WorkflowStore loads workflow definitions and stores execution records. Satisfied by
// *webdavstore.Store.
type WorkflowStore interface {
	Get(ctx context.Context, authHeader, id string) (*model.WorkflowDefinition, error)
	PutExecution(ctx context.Context, authHeader string, rec model.ExecutionRecord) error
}

// Reconciler backstops SSE by catching up on anything oCIS's activitylog recorded while a
// user's SSE connection was down. Safe for concurrent use — Reconcile is meant to be called
// from a fresh goroutine per SSE reconnect, and internally serializes via a bounded
// semaphore so a fleet-wide reconnect event can't fire unbounded simultaneous requests.
type Reconciler struct {
	db         TriggerStore
	drives     DriveLister
	activities ActivityLister
	paths      PathResolver
	executor   Executor
	store      WorkflowStore

	gracePeriod time.Duration
	overlap     time.Duration
	sem         chan struct{}
	log         *slog.Logger
}

// New builds a Reconciler. gracePeriod is both the minimum cursor age before a
// reconciliation pass is worth running (avoids re-querying a drive that was just checked)
// and the debounce window for flapping SSE reconnects. overlap is subtracted from the
// stored cursor before querying, trading a rare double-fire for never missing a boundary
// event. maxConcurrent bounds how many reconciliation passes run at once instance-wide.
func New(db TriggerStore, drives DriveLister, activities ActivityLister, paths PathResolver, executor Executor, store WorkflowStore, gracePeriod, overlap time.Duration, maxConcurrent int, log *slog.Logger) *Reconciler {
	return &Reconciler{
		db:          db,
		drives:      drives,
		activities:  activities,
		paths:       paths,
		executor:    executor,
		store:       store,
		gracePeriod: gracePeriod,
		overlap:     overlap,
		sem:         make(chan struct{}, maxConcurrent),
		log:         log,
	}
}

// Reconcile runs one reconciliation pass for userID across every drive implied by their
// event triggers (see drivesForUser), authenticating oCIS calls with authHeader. Blocks
// until a semaphore slot is free, so callers that don't want to block their own goroutine
// (e.g. sse.Manager's reconnect hook) should call this via `go r.Reconcile(...)`.
func (r *Reconciler) Reconcile(ctx context.Context, userID, authHeader string) {
	r.sem <- struct{}{}
	defer func() { <-r.sem }()

	drives, err := r.drivesForUser(ctx, userID, authHeader)
	if err != nil {
		r.log.Warn("reconcile: could not determine drive scope", "userID", userID, "error", err)
		return
	}

	for _, driveID := range drives {
		r.reconcileDrive(ctx, userID, authHeader, driveID)
	}
}

// drivesForUser derives which drives need backstopping for userID: exactly the drives
// referenced by their space-scoped event triggers, plus — only if at least one trigger is
// unscoped ("any space") — every drive ListDrives returns (excluding "virtual" aggregate
// drives, which aren't a real place events originate in).
func (r *Reconciler) drivesForUser(ctx context.Context, userID, authHeader string) ([]string, error) {
	entries, err := r.db.ListEventTriggers(ctx)
	if err != nil {
		return nil, err
	}

	scoped := map[string]bool{}
	needsAll := false
	for _, e := range entries {
		if e.UserID != userID {
			continue
		}
		if e.SpaceID == "" {
			needsAll = true
			continue
		}
		scoped[e.SpaceID] = true
	}

	if !needsAll {
		drives := make([]string, 0, len(scoped))
		for id := range scoped {
			drives = append(drives, id)
		}
		return drives, nil
	}

	all, err := r.drives.ListDrives(ctx, authHeader)
	if err != nil {
		return nil, err
	}
	drives := make([]string, 0, len(all))
	for _, d := range all {
		if d.DriveType == "virtual" {
			continue
		}
		drives = append(drives, d.ID)
	}
	return drives, nil
}

// reconcileDrive checks driveID for anything since its stored cursor, dispatching matching
// workflows and then advancing the cursor. A brand-new (user, driveID) pair seeds its
// cursor at "now" without querying — nothing to backfill for a trigger that was just
// created. A cursor younger than gracePeriod is left alone (debounce). A failed
// activitylog query marks the cursor "sse-only" without advancing LastChecked, so the next
// attempt retries the same window instead of silently skipping it.
func (r *Reconciler) reconcileDrive(ctx context.Context, userID, authHeader, driveID string) {
	now := time.Now()

	cursor, err := r.db.GetEventCursor(ctx, userID, driveID)
	if err != nil {
		if err != localdb.ErrNotFound {
			r.log.Warn("reconcile: read cursor", "userID", userID, "driveID", driveID, "error", err)
			return
		}
		if err := r.db.UpsertEventCursor(ctx, localdb.EventCursor{UserID: userID, DriveID: driveID, LastChecked: now, LastStatus: "full"}); err != nil {
			r.log.Warn("reconcile: seed cursor", "userID", userID, "driveID", driveID, "error", err)
		}
		return
	}

	if now.Sub(cursor.LastChecked) < r.gracePeriod {
		return
	}

	since := cursor.LastChecked.Add(-r.overlap)
	activities, err := r.activities.ListActivities(ctx, authHeader, driveID, since)
	if err != nil {
		r.log.Warn("reconcile: list activities", "userID", userID, "driveID", driveID, "error", err)
		if err := r.db.UpsertEventCursor(ctx, localdb.EventCursor{UserID: userID, DriveID: driveID, LastChecked: cursor.LastChecked, LastStatus: "sse-only"}); err != nil {
			r.log.Warn("reconcile: mark cursor degraded", "userID", userID, "driveID", driveID, "error", err)
		}
		return
	}

	latest := cursor.LastChecked
	seen := map[string]bool{}
	for _, a := range activities {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		if a.RecordedTime.After(latest) {
			latest = a.RecordedTime
		}
		r.dispatch(ctx, userID, authHeader, driveID, a)
	}

	if err := r.db.UpsertEventCursor(ctx, localdb.EventCursor{UserID: userID, DriveID: driveID, LastChecked: latest, LastStatus: "full"}); err != nil {
		r.log.Warn("reconcile: advance cursor", "userID", userID, "driveID", driveID, "error", err)
	}
}

// dispatch maps a from activitylog to a trigger type, resolves its path, and runs every
// enabled workflow whose trigger matches — mirroring sse.Manager.handleEvent's matching
// logic exactly (same MatchesFilters helper, same internal-path guard) so the two event
// paths can't drift apart on what counts as a match.
func (r *Reconciler) dispatch(ctx context.Context, userID, authHeader, driveID string, a ocisclient.Activity) {
	triggerType, ok := TriggerType(a.Message)
	if !ok || a.Resource.ID == "" {
		return
	}

	spaceID, itemID, ok := splitResourceID(a.Resource.ID)
	if !ok {
		return
	}

	path, err := r.paths.ItemPath(ctx, authHeader, spaceID, itemID)
	if err != nil {
		r.log.Warn("reconcile: resolve activity path", "userID", userID, "error", err)
		return
	}
	if webdavstore.IsInternalPath(path) {
		return
	}

	entries, err := r.db.ListEventTriggers(ctx)
	if err != nil {
		r.log.Warn("reconcile: list event triggers", "error", err)
		return
	}

	for _, e := range entries {
		if e.UserID != userID || e.EventType != triggerType {
			continue
		}
		if !e.MatchesFilters(path, spaceID) {
			continue
		}
		r.runWorkflow(ctx, authHeader, e.WorkflowID, path)
	}
}

func (r *Reconciler) runWorkflow(ctx context.Context, authHeader, workflowID, resourcePath string) {
	wf, err := r.store.Get(ctx, authHeader, workflowID)
	if err != nil {
		r.log.Error("reconcile: load workflow", "workflowID", workflowID, "error", err)
		return
	}
	if !wf.Enabled {
		return
	}

	record := r.executor.Run(ctx, authHeader, *wf, "event", resourcePath)
	if err := r.store.PutExecution(ctx, authHeader, *record); err != nil {
		r.log.Error("reconcile: store execution record", "workflowID", workflowID, "error", err)
	}
}

// splitResourceID splits activitylog's compound "storageid$spaceid!opaqueid" resource id
// into the (spaceID, itemID) pair ItemPath expects — spaceID is everything before the
// last "!", itemID is the whole compound id (ItemPath's own drives/{spaceID}/items/{itemID}
// route expects the full compound id as itemID, matching how SSE-sourced events already
// use it).
func splitResourceID(id string) (spaceID, itemID string, ok bool) {
	idx := strings.LastIndex(id, "!")
	if idx < 0 {
		return "", "", false
	}
	return id[:idx], id, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./pkg/reconcile/... -v -race`
Expected: PASS, all tests including the concurrency-bound test under `-race`.

- [ ] **Step 5: Commit**

```bash
cd backend && go build ./... && go vet ./...
git add pkg/reconcile/reconciler.go pkg/reconcile/reconciler_test.go
git commit -s -m "feat(backend): add Reconciler core (drive scope, query, dispatch)

Derives drive scope from the trigger index (reusing PR #31's SpaceID),
queries activitylog per drive since a persisted cursor, maps messages
to trigger types, and dispatches matching workflows using the same
MatchesFilters/IsInternalPath logic sse.Manager already applies to
live events. Bounded by a bare-metal semaphore so a fleet-wide SSE
reconnect can't fire unbounded concurrent requests."
```

---

## Task 6: Wire the Reconciler into sse.Manager's reconnect path

**Files:**
- Modify: `backend/pkg/sse/manager.go`
- Modify: `backend/pkg/sse/manager_test.go`

**Interfaces:**
- Consumes: `reconcile.Reconciler.Reconcile(ctx, userID, authHeader string)` (Task 5) — referenced only through a new local `Reconciler` interface in the `sse` package (matching this package's existing style of small local interfaces over concrete dependency types), never an import of `pkg/reconcile` into `pkg/sse` itself (avoids any risk of a future import cycle if `reconcile` ever needs something from `sse`).
- Modifies: `sse.New`'s signature (adds a `reconciler Reconciler` parameter) and `sse.Manager.streamOnce`'s signature (adds an `onConnected func()` parameter) — every existing call site in `manager_test.go` must be updated.

- [ ] **Step 1: Write the failing test**

Add to `backend/pkg/sse/manager_test.go` (new fake + new test):

```go
type fakeReconciler struct {
	calls atomic.Int32
}

func (f *fakeReconciler) Reconcile(context.Context, string, string) {
	f.calls.Add(1)
}
```

Add a `reconciler Reconciler` argument to every existing `New(...)` call site in this file (7 total): each becomes `New(triggers, store, paths, exec, &fakeReconciler{}, srv.URL, ...)` (or `&fakeReconciler{}` in place of the previously-missing argument, in the same position — right after `exec`/`executor`). Then add:

```go
// TestStreamOnceCallsReconcilerOnConnect regression-tests the exact race this backstop
// exists to close: a brand-new SSE consumer must trigger a reconciliation pass for its
// user as soon as the connection is actually established, not just periodically or never.
func TestStreamOnceCallsReconcilerOnConnect(t *testing.T) {
	sseBody := "data: {}\nevent: some-unrecognized-event\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()

	triggers := &fakeTriggerStore{
		entries:     []localdb.TriggerIndexEntry{{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload"}},
		automations: map[string]*localdb.Automation{"user-1": {UserID: "user-1", Username: "admin", AppPassword: "secret"}},
	}
	reconciler := &fakeReconciler{}
	m := New(triggers, &fakeWorkflowStore{}, &fakePathResolver{}, &fakeExecutor{}, reconciler, srv.URL, false, time.Hour, discardLogger())

	err := m.streamOnce(t.Context(), "user-1", "Basic dGVzdA==", func() { reconciler.Reconcile(t.Context(), "user-1", "Basic dGVzdA==") })
	if err != nil {
		t.Fatalf("streamOnce: %v", err)
	}

	waitFor(t, time.Second, func() bool { return reconciler.calls.Load() == 1 })
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./pkg/sse/... -v`
Expected: FAIL to compile — `New` doesn't accept a `Reconciler` argument yet, `streamOnce` doesn't accept a 4th `onConnected` argument yet.

- [ ] **Step 3: Implement**

In `backend/pkg/sse/manager.go`:

Add the `Reconciler` interface near the other small local interfaces (`TriggerStore`, `WorkflowStore`, `PathResolver`, `Executor`):

```go
// Reconciler runs an activitylog-based backstop pass for a user, catching up on anything
// their SSE connection may have missed while it was down. Satisfied by
// *reconcile.Reconciler.
type Reconciler interface {
	Reconcile(ctx context.Context, userID, authHeader string)
}
```

Add a `reconciler Reconciler` field to the `Manager` struct (next to the other injected dependencies), and update `New`:

```go
func New(db TriggerStore, store WorkflowStore, paths PathResolver, executor Executor, reconciler Reconciler, ocisURL string, insecure bool, interval time.Duration, log *slog.Logger) *Manager {
	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-only opt-in
	}
	return &Manager{
		db:         db,
		store:      store,
		paths:      paths,
		executor:   executor,
		reconciler: reconciler,
		ocisURL:    strings.TrimRight(ocisURL, "/"),
		insecure:   insecure,
		interval:   interval,
		log:        log,
		httpClient: &http.Client{Transport: transport},
		active:     map[string]activeConsumer{},
		kick:       make(chan struct{}, 1),
	}
}
```

Change `streamOnce`'s signature to accept and invoke `onConnected` right after the connection is confirmed established (200 OK, before entering the read loop):

```go
func (m *Manager) streamOnce(ctx context.Context, userID, authHeader string, onConnected func()) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		m.ocisURL+"/ocs/v2.php/apps/notifications/api/v1/notifications/sse", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "text/event-stream")

	res, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("sse endpoint returned status %d", res.StatusCode)
	}

	if onConnected != nil {
		onConnected()
	}

	scanner := bufio.NewScanner(res.Body)
	var eventType, data string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			if data != "" {
				m.handleEvent(ctx, userID, authHeader, eventType, data)
			}
			eventType, data = "", ""
		}
	}
	return scanner.Err()
}
```

Update `consumeForUser` to build the `onConnected` closure and pass it through:

```go
func (m *Manager) consumeForUser(ctx context.Context, userID string, id uint64) {
	automation, err := m.db.GetAutomation(ctx, userID)
	if err != nil {
		m.log.Warn("sse manager: user has an event trigger but no automation connected", "userID", userID)
		m.deactivate(userID, id)
		return
	}
	authHeader := "Basic " + base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", automation.Username, automation.AppPassword))

	onConnected := func() {
		if m.reconciler != nil {
			go m.reconciler.Reconcile(ctx, userID, authHeader)
		}
	}

	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := m.streamOnce(ctx, userID, authHeader, onConnected); err != nil && ctx.Err() == nil {
			m.log.Warn("sse manager: stream ended, reconnecting", "userID", userID, "error", err, "backoff", backoff)
		}
		if ctx.Err() != nil {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
```

Update the 7 pre-existing `New(...)` call sites in `manager_test.go` to pass `&fakeReconciler{}` (or, where a test doesn't care about it at all, `nil` is also valid since `onConnected` guards `m.reconciler != nil` — prefer `&fakeReconciler{}` everywhere for consistency and to make a future accidental nil-dependency mistake loud in other tests). Update the two `streamOnce(...)` call sites (`TestStreamOnceDispatchesMatchingEventTrigger` and the new test above) to pass a 4th argument — `nil` is fine for `TestStreamOnceDispatchesMatchingEventTrigger` since it doesn't assert on reconciliation.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./pkg/sse/... -v -race`
Expected: PASS, every test including the new `TestStreamOnceCallsReconcilerOnConnect`.

- [ ] **Step 5: Commit**

```bash
cd backend && go build ./... && go vet ./...
git add pkg/sse/manager.go pkg/sse/manager_test.go
git commit -s -m "feat(backend): trigger reconciliation on SSE (re)connect

streamOnce now calls an onConnected hook once a connection is
established (200 OK, before the read loop), which consumeForUser
wires to reconcile.Reconciler.Reconcile — closing the exact race
from the original bug report, where an upload landing in the gap
before a fresh SSE consumer finishes connecting was silently lost."
```

---

## Task 7: Surface reliability via GET /me/automation

**Files:**
- Modify: `backend/pkg/model/workflow.go`
- Modify: `backend/pkg/automation/automation.go`
- Modify: `backend/pkg/automation/automation_test.go`

**Interfaces:**
- Modifies: `model.AutomationStatus` gains `Reliability string`.
- Consumes: `localdb.DB.GetReliability` (Task 2), already available on `automation.Service`'s existing `db *localdb.DB` field.

- [ ] **Step 1: Write the failing test**

Add to `backend/pkg/automation/automation_test.go`:

```go
func TestStatusReportsFullReliabilityWithNoCursors(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID: "user-1", Username: "admin", AppPassword: "fresh", ExpiresAt: expiresAt, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}

	svc := New(&fakeGraphClient{}, db, nil, discardLogger())
	status, err := svc.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Reliability != "full" {
		t.Errorf("Reliability = %q, want %q", status.Reliability, "full")
	}
}

func TestStatusReportsDegradedReliabilityWhenReconciliationFailed(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID: "user-1", Username: "admin", AppPassword: "fresh", ExpiresAt: expiresAt, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}
	if err := db.UpsertEventCursor(ctx, localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: time.Now(), LastStatus: "sse-only"}); err != nil {
		t.Fatalf("UpsertEventCursor: %v", err)
	}

	svc := New(&fakeGraphClient{}, db, nil, discardLogger())
	status, err := svc.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Reliability != "sse-only" {
		t.Errorf("Reliability = %q, want %q", status.Reliability, "sse-only")
	}
}

func TestStatusOmitsReliabilityWhenDisconnected(t *testing.T) {
	db := testDB(t)
	svc := New(&fakeGraphClient{}, db, nil, discardLogger())

	status, err := svc.Status(t.Context(), "never-connected-user")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Connected {
		t.Fatalf("expected Connected = false")
	}
	if status.Reliability != "" {
		t.Errorf("Reliability = %q, want empty when not connected", status.Reliability)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./pkg/automation/... -run 'TestStatusReportsFullReliability|TestStatusReportsDegradedReliability|TestStatusOmitsReliability' -v`
Expected: FAIL — `status.Reliability` doesn't compile (field doesn't exist yet), or the value is empty where "full"/"sse-only" is expected.

- [ ] **Step 3: Implement**

Add to `model.AutomationStatus` in `backend/pkg/model/workflow.go`:

```go
// AutomationStatus is a Graph-style singleton resource (like /me/mailboxSettings)
// describing whether a user has enabled scheduled/event-triggered automation.
type AutomationStatus struct {
	Connected          bool   `json:"connected"`
	ExpirationDateTime string `json:"expirationDateTime,omitempty"`
	// Reliability is "full" when event triggers are backed by both live SSE delivery and
	// the activitylog reconciliation backstop, or "sse-only" when the last reconciliation
	// attempt failed (e.g. activitylog unavailable/disabled) — meaning events are only as
	// reliable as the live SSE connection, with no catch-up if it drops. Empty when
	// Connected is false.
	Reliability string `json:"reliability,omitempty"`
}
```

Update `Status` in `backend/pkg/automation/automation.go`:

```go
// Status returns whether userID currently has automation enabled.
func (s *Service) Status(ctx context.Context, userID string) (*model.AutomationStatus, error) {
	a, err := s.db.GetAutomation(ctx, userID)
	if err != nil {
		if err == localdb.ErrNotFound {
			return toStatus(nil), nil
		}
		return nil, err
	}

	status := toStatus(a)
	if status.Connected {
		reliability, err := s.db.GetReliability(ctx, userID)
		if err != nil {
			s.log.Warn("read event-trigger reliability", "userID", userID, "error", err)
		} else {
			status.Reliability = reliability
		}
	}
	return status, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./pkg/automation/... -v`
Expected: PASS, all tests including pre-existing ones.

- [ ] **Step 5: Commit**

```bash
cd backend && go build ./... && go vet ./...
git add pkg/model/workflow.go pkg/automation/automation.go pkg/automation/automation_test.go
git commit -s -m "feat(backend): surface event-trigger reliability on GET /me/automation

Backend contract only — frontend surfacing of the new field is a
follow-up, not part of this change (see spec's Risks/open questions)."
```

---

## Task 8: Wire the Reconciler into the running server

**Files:**
- Modify: `backend/pkg/command/server.go`

**Interfaces:**
- Consumes: `reconcile.New` (Task 5), `sse.New`'s updated signature (Task 6).

- [ ] **Step 1: Implement** (wiring-only task — no new unit-testable behavior beyond what Tasks 1–7 already covered; verified by the full test suite plus the e2e test in Task 9)

Add new tunable consts near the existing ones in `backend/pkg/command/server.go`:

```go
// reconcileGracePeriod is both the minimum cursor age before a reconciliation pass runs
// and the debounce window for flapping SSE reconnects.
const reconcileGracePeriod = 5 * time.Second

// reconcileOverlapWindow is subtracted from a stored cursor before querying activitylog,
// trading a rare double-fire for never missing a boundary event.
const reconcileOverlapWindow = 5 * time.Second

// reconcileMaxConcurrent bounds how many reconciliation passes run at once instance-wide,
// so a fleet-wide SSE reconnect (e.g. oCIS's own sse service restarting) can't fire
// unbounded simultaneous activitylog queries.
const reconcileMaxConcurrent = 10
```

Add the import:

```go
	"github.com/owncloud/ocis-workflows/pkg/reconcile"
```

Construct the `Reconciler` before `sseManager` (since `sse.New` now needs it), and thread it through:

```go
	reconciler := reconcile.New(db, ocisClient, ocisClient, ocisClient, graphExecutor, store,
		reconcileGracePeriod, reconcileOverlapWindow, reconcileMaxConcurrent, log)

	// sseManager is constructed before the handlers below so its Kick method can be wired
	// into them: both a workflow's event trigger being added and a user's automation being
	// connected should nudge the SSE manager to reconcile immediately instead of waiting for
	// its next periodic tick (up to sseReconcileInterval later).
	sseManager := sse.New(db, store, ocisClient, graphExecutor, reconciler, cfg.OCISURL, cfg.OCISInsecure, sseReconcileInterval, log)
```

(`ocisClient` satisfies `reconcile.DriveLister`, `reconcile.ActivityLister`, and `reconcile.PathResolver` simultaneously — same instance passed three times, matching how it already independently satisfies `sse.PathResolver` and `executor.GraphClient` elsewhere in this same function.)

- [ ] **Step 2: Verify the full build and test suite**

Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: builds clean, all tests pass.

- [ ] **Step 3: Manual smoke check against the dev stack**

Run: `docker compose up -d --build workflows-backend` (from repo root), then `docker logs -f ocis-workflows-workflows-backend-1` and confirm the process starts without error (no wiring/nil-pointer panics from the new `reconciler` dependency).

- [ ] **Step 4: Commit**

```bash
cd backend && go build ./... && go vet ./...
git add pkg/command/server.go
git commit -s -m "feat(backend): wire the reconciliation backstop into the running server"
```

---

## Task 9: e2e proof that the backstop recovers a missed upload

**Files:**
- Modify: `backend/tests/e2e/automation_test.go`

**Interfaces:**
- Consumes: existing e2e helpers `testToken`, `doRequest`, `decodeJSON`, `mkdir`, `uploadFile` (all already used by `TestEventTriggeredWorkflowRunsOnUpload` in this file).

- [ ] **Step 1: Write the test**

Add to `backend/tests/e2e/automation_test.go`:

```go
// TestEventTriggeredWorkflowRecoversFromMissedSSEEvent proves the actual bug this backstop
// exists to fix: connect automation, create an event-triggered workflow, but upload the
// matching file *before* giving the SSE manager's periodic reconcile tick (up to
// sseReconcileInterval, 30s — see command.sseReconcileInterval) any chance to open a live
// connection for this workflow's trigger. Live SSE alone would silently lose this event
// forever (see pkg/sse's package doc). The reconciliation backstop should still surface it
// once the SSE connection does eventually establish and calls its onConnected hook.
func TestEventTriggeredWorkflowRecoversFromMissedSSEEvent(t *testing.T) {
	token := testToken(t)

	connectRes := doRequest(t, http.MethodPost, "/me/automation", nil, true)
	connectRes.Body.Close()
	if connectRes.StatusCode != http.StatusOK {
		t.Fatalf("connect automation: expected 200, got %d", connectRes.StatusCode)
	}
	t.Cleanup(func() {
		res := doRequest(t, http.MethodDelete, "/me/automation", nil, true)
		res.Body.Close()
	})

	newWorkflow := map[string]any{
		"name":    "e2e reconciliation-backstop workflow",
		"enabled": true,
		"trigger": map[string]any{
			"type": "event",
			"event": map[string]any{
				"type":    "upload",
				"filters": map[string]string{"pathPrefix": "/e2e-reconcile-test", "extension": ".txt"},
			},
		},
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "trigger", "type": "trigger", "position": map[string]int{"x": 0, "y": 0}, "data": map[string]any{
					"triggerType": "event", "eventType": "upload",
				}},
				{"id": "llm-1", "type": "llm", "position": map[string]int{"x": 200, "y": 0}, "data": map[string]any{
					"prompt": "Say hi",
				}},
			},
			"edges": []map[string]string{{"id": "e1", "source": "trigger", "target": "llm-1"}},
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

	// Deliberately upload immediately — no wait for the SSE manager to open a connection —
	// to land inside the exact gap this backstop is meant to cover. Live SSE misses this;
	// only the reconciliation pass on the eventual connect can recover it.
	mkdir(t, token, "/e2e-reconcile-test")
	uploadFile(t, token, "/e2e-reconcile-test/hello.txt", "hello from the reconciliation-backstop e2e test")

	// Poll well past sseReconcileInterval (30s) plus the reconciliation grace period (5s),
	// short of the test's own timeout — long enough for the SSE consumer to eventually
	// connect and its onConnected hook to run a reconciliation pass.
	deadline := time.Now().Add(50 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		listRes := doRequest(t, http.MethodGet, "/me/workflows/"+workflow.ID+"/executions", nil, true)
		list := decodeJSON[struct {
			Value []struct {
				TriggeredBy string `json:"triggeredBy"`
				Status      string `json:"status"`
			} `json:"value"`
		}](t, listRes)

		for _, exec := range list.Value {
			if exec.TriggeredBy == "event" && exec.Status == "succeeded" {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(3 * time.Second)
	}

	if !found {
		t.Fatal("expected the reconciliation backstop to recover the missed upload within 50s, found no successful event-triggered execution")
	}
}
```

- [ ] **Step 2: Run it against the dev stack**

From repo root: `LLM_ENDPOINT=http://fake-llm:8080/v1 LLM_MODEL=fake-model LLM_API_KEY= docker compose --profile test up -d --build`, then:

Run: `cd backend && go test -tags=e2e ./tests/e2e/... -run TestEventTriggeredWorkflowRecoversFromMissedSSEEvent -v`
Expected: PASS within the 50s deadline. If it fails, check `docker logs ocis-workflows-workflows-backend-1` for reconciliation warnings (activitylog unreachable, cursor errors) before assuming the code is wrong — this test depends on a real running oCIS with activitylog enabled (the default; see spec's Context section).

- [ ] **Step 3: Run the full e2e suite to confirm no regressions**

Run: `cd backend && go test -tags=e2e ./tests/e2e/... -v`
Expected: PASS, including the pre-existing `TestEventTriggeredWorkflowRunsOnUpload`.

- [ ] **Step 4: Commit**

```bash
cd backend
git add tests/e2e/automation_test.go
git commit -s -m "test(e2e): prove the reconciliation backstop recovers a missed upload

Uploads immediately after creating an event trigger, deliberately not
waiting for the SSE manager to open its connection first — the exact
race from the original bug report — and asserts the workflow still
fires once reconciliation runs on the eventual (re)connect."
```

---

## Self-Review Notes

- **Spec coverage:** §1 (drive scope) → Task 5's `drivesForUser`. §2 (reconnect-triggered, grace period, debounce, mass-reconnect worker pool) → Tasks 5 (semaphore, grace-period check doubling as debounce) + 6 (reconnect hook). §3 (query shape, message mapping, self-trigger guard) → Tasks 3, 4, 5. §4 (dedup, cursor advancement, overlap) → Task 5. §5 (degradation visibility) → Task 7. §6 (storage) → Task 2. Testing section → Tasks 1–9 each carry their own tests plus Task 9's dedicated e2e proof.
- **Placeholder scan:** none found — every step has real, complete code.
- **Type consistency:** `TriggerType(message string) (string, bool)` (Task 4) used identically in Task 5's `dispatch`. `Reconciler.Reconcile(ctx, userID, authHeader string)` (Task 5) matches the `sse.Reconciler` interface method signature exactly (Task 6). `localdb.EventCursor` fields (`UserID`, `DriveID`, `LastChecked`, `LastStatus`) used consistently across Tasks 2, 5, 7. `model.AutomationStatus.Reliability` values are the literal strings `"full"`/`"sse-only"` throughout, matching `localdb.GetReliability`'s return values exactly.
- **Explicitly deferred, per spec Non-goals:** `share` trigger backstop coverage (mapping table in Task 4 only covers upload/move), `lock` trigger coverage (no activitylog message exists), direct NATS consumption, frontend UI for the new `reliability` field.
