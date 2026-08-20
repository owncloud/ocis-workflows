package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
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

	gracePeriod          time.Duration
	overlap              time.Duration
	firstConnectLookback time.Duration
	sem                  chan struct{}
	log                  *slog.Logger

	// passMu guards lastPassStart, a process-local (lost on restart) rate limit on top of
	// the durable per-drive cursor debounce below — see shouldStartPass.
	passMu        sync.Mutex
	lastPassStart map[string]time.Time // userID -> wall-clock time the last pass started
}

// New builds a Reconciler. gracePeriod is both the minimum cursor age before a
// reconciliation pass is worth running (avoids re-querying a drive that was just checked)
// and the debounce window for flapping SSE reconnects. overlap is subtracted from the
// stored cursor before querying, trading a rare double-fire for never missing a boundary
// event. firstConnectLookback is how far back a (user, drive) pair with no prior cursor
// looks on its very first reconciliation pass — generous relative to realistic SSE
// reconnect delays (so it actually covers the "brand-new trigger racing the first SSE
// connection" scenario this backstop exists for), but far short of activitylog's retention
// (so it doesn't flood-dispatch a busy drive's history the first time it's ever checked). It
// doubles as the query floor for every other pass too — see reconcileDrive. maxConcurrent
// bounds how many reconciliation passes run at once instance-wide.
func New(db TriggerStore, drives DriveLister, activities ActivityLister, paths PathResolver, executor Executor, store WorkflowStore, gracePeriod, overlap, firstConnectLookback time.Duration, maxConcurrent int, log *slog.Logger) *Reconciler {
	return &Reconciler{
		db:                   db,
		drives:               drives,
		activities:           activities,
		paths:                paths,
		executor:             executor,
		store:                store,
		gracePeriod:          gracePeriod,
		overlap:              overlap,
		firstConnectLookback: firstConnectLookback,
		sem:                  make(chan struct{}, maxConcurrent),
		log:                  log,
		lastPassStart:        map[string]time.Time{},
	}
}

// Reconcile runs one reconciliation pass for userID across every drive implied by their
// event triggers (see drivesForUser), authenticating oCIS calls with authHeader. Blocks
// until a semaphore slot is free, so callers that don't want to block their own goroutine
// (e.g. sse.Manager's reconnect hook) should call this via `go r.Reconcile(...)`. If ctx is
// cancelled while still waiting for a slot (e.g. the caller's SSE consumer shut down before
// this queued pass got to run), it bails out without acquiring a slot or touching any cursor.
//
// Before doing anything else it checks shouldStartPass: a lightweight, process-local rate
// limit so a flapping connection reconnecting faster than gracePeriod doesn't fire a
// ListDrives request (a real GET /me/drives call, needed for unscoped triggers) on every
// single attempt — the durable per-drive cursor further down is what actually prevents
// duplicate dispatch; this is purely to avoid the wasted round trip before it even gets
// there.
func (r *Reconciler) Reconcile(ctx context.Context, userID, authHeader string) {
	if !r.shouldStartPass(userID) {
		return
	}

	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-r.sem }()

	// Fetched once per pass and threaded through drivesForUser/reconcileDrive/dispatch below
	// instead of each independently re-querying — a pass that finds N activities used to run
	// this exact same query N+1 times.
	entries, err := r.db.ListEventTriggers(ctx)
	if err != nil {
		r.log.Warn("reconcile: list event triggers", "userID", userID, "error", err)
		return
	}

	drives, err := r.drivesForUser(ctx, entries, authHeader, userID)
	if err != nil {
		r.log.Warn("reconcile: could not determine drive scope", "userID", userID, "error", err)
		return
	}

	for _, driveID := range drives {
		r.reconcileDrive(ctx, entries, userID, authHeader, driveID)
	}
}

// shouldStartPass reports whether a pass for userID may proceed: true (and records now as
// its start time) unless a pass for the same user already started within gracePeriod.
func (r *Reconciler) shouldStartPass(userID string) bool {
	now := time.Now()
	r.passMu.Lock()
	defer r.passMu.Unlock()
	if last, ok := r.lastPassStart[userID]; ok && now.Sub(last) < r.gracePeriod {
		return false
	}
	r.lastPassStart[userID] = now
	return true
}

// drivesForUser derives which drives need backstopping for userID from entries (this pass's
// already-fetched event triggers): exactly the drives referenced by their space-scoped event
// triggers, plus — only if at least one trigger is unscoped ("any space") — every drive
// ListDrives returns (excluding "virtual" aggregate drives, which aren't a real place events
// originate in).
func (r *Reconciler) drivesForUser(ctx context.Context, entries []localdb.TriggerIndexEntry, authHeader, userID string) ([]string, error) {
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
// workflows (using entries, this pass's already-fetched event triggers) and then advancing
// the cursor. A brand-new (user, driveID) pair — no prior cursor at all — is exactly the
// scenario this backstop exists for: a trigger just created, racing the very first SSE
// connection. So instead of seeding the cursor at "now" and skipping the query (which would
// silently miss precisely that race), it synthesizes a starting cursor firstConnectLookback
// in the past and falls through to the same query/dispatch/advance logic below as any other
// pass — bounded so a first-ever check on an old, busy drive doesn't flood-dispatch its
// whole history, just the genuine just-created-trigger window.
//
// firstEver tracks whether this is that synthesized case explicitly, rather than relying on
// firstConnectLookback happening to exceed gracePeriod (production defaults: 5m vs 5s) for
// the debounce check below to behave correctly — if that relationship were ever inverted by
// a config change, an implicit reliance would debounce the synthesized cursor away before it
// was ever persisted, and every subsequent pass would resynthesize and re-debounce forever,
// silently. A cursor younger than gracePeriod is otherwise left alone (debounce). A failed
// activitylog query marks the cursor "sse-only" without advancing LastChecked, so the next
// attempt retries the same window instead of silently skipping it — unless the failure is
// just ctx being cancelled out from under us (e.g. the caller's SSE consumer shut down while
// this call was in flight), in which case nothing was actually wrong with activitylog and we
// leave the cursor untouched entirely rather than falsely marking it degraded.
//
// The query floor (since) is clamped to never look back further than firstConnectLookback
// from now, regardless of how stale the stored cursor actually is. Without this, a
// long-lived healthy SSE connection (normal operation: cursors are only ever written from
// here, never touched by the live SSE path itself) reconnecting after hours of uptime, a
// backend restart after extended downtime, or recovery after an extended activitylog outage
// (the degraded path above deliberately doesn't advance the cursor) would each compute a
// since arbitrarily far in the past — and every activity in that whole window gets
// dispatched again with a real ItemPath call, a workflow load, and a synchronous execution,
// even though SSE already delivered all of it live (there's no cross-path dedup, by design;
// see the design doc). This clamp bounds the blast radius to a fixed window; it doesn't
// close the gap for the stretch of a long-lived connection beyond that window — the proper
// fix (refreshing the cursor periodically while the connection stays healthy) is a larger
// change to sse's read loop, deferred as a documented follow-up.
//
// On success the cursor advances to now — the wall-clock time this pass started, not the
// latest activity's RecordedTime. Advancing to the latest activity's own timestamp would let
// the cursor get stuck there forever on a drive that goes quiet after that activity: since
// the grace-period debounce compares against wall-clock now, a cursor frozen in the past
// stays "old enough" on every subsequent pass, re-querying (and with the overlap window,
// re-dispatching) that same already-handled activity on every future reconnect. Advancing to
// now means the very next pass is freshly debounced instead, and overlap still covers the
// only real gap: activity recorded between the query firing and now being captured.
func (r *Reconciler) reconcileDrive(ctx context.Context, entries []localdb.TriggerIndexEntry, userID, authHeader, driveID string) {
	now := time.Now()

	firstEver := false
	cursor, err := r.db.GetEventCursor(ctx, userID, driveID)
	if err != nil {
		if !errors.Is(err, localdb.ErrNotFound) {
			r.log.Warn("reconcile: read cursor", "userID", userID, "driveID", driveID, "error", err)
			return
		}
		// No prior cursor for this (user, drive) pair — this is the very first
		// reconciliation pass ever run for it, which is exactly the scenario the
		// backstop exists to cover: a brand-new event trigger whose upload raced the
		// SSE connection. Look back a bounded window instead of skipping the query
		// entirely, so that race is actually recoverable on the very first pass.
		firstEver = true
		cursor = &localdb.EventCursor{UserID: userID, DriveID: driveID, LastChecked: now.Add(-r.firstConnectLookback), LastStatus: "full"}
	}

	if !firstEver && now.Sub(cursor.LastChecked) < r.gracePeriod {
		return
	}

	since := cursor.LastChecked.Add(-r.overlap)
	if floor := now.Add(-r.firstConnectLookback); since.Before(floor) {
		since = floor
	}
	activities, err := r.activities.ListActivities(ctx, authHeader, driveID, since)
	if err != nil {
		r.log.Warn("reconcile: list activities", "userID", userID, "driveID", driveID, "error", err)
		if ctx.Err() != nil {
			// Our own context was cancelled mid-flight — activitylog isn't actually
			// degraded, so don't mark the cursor as such.
			return
		}
		if err := r.db.UpsertEventCursor(ctx, localdb.EventCursor{UserID: userID, DriveID: driveID, LastChecked: cursor.LastChecked, LastStatus: "sse-only"}); err != nil {
			r.log.Warn("reconcile: mark cursor degraded", "userID", userID, "driveID", driveID, "error", err)
		}
		return
	}

	seen := map[string]bool{}
	for _, a := range activities {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		r.dispatch(ctx, entries, userID, authHeader, driveID, a)
	}

	if err := r.db.UpsertEventCursor(ctx, localdb.EventCursor{UserID: userID, DriveID: driveID, LastChecked: now, LastStatus: "full"}); err != nil {
		r.log.Warn("reconcile: advance cursor", "userID", userID, "driveID", driveID, "error", err)
	}
}

// dispatch maps an activitylog entry to a trigger type, resolves its path, and runs every
// enabled workflow whose trigger matches, from entries (this pass's already-fetched event
// triggers — dispatch is called once per activity, so it must not re-query them itself) —
// mirroring sse.Manager.handleEvent's matching logic exactly (same MatchesFilters helper,
// same internal-path guard) so the two event paths can't drift apart on what counts as a
// match. driveID (the drive this activity was queried from, already known and already in
// the same plain form TriggerIndexEntry.SpaceID stores) is used as the space scope
// throughout — not a value reparsed out of the activity's own resourceId, which carries an
// unrelated storage-provider prefix.
func (r *Reconciler) dispatch(ctx context.Context, entries []localdb.TriggerIndexEntry, userID, authHeader, driveID string, a ocisclient.Activity) {
	triggerType, ok := TriggerType(a.Message)
	if !ok || a.Resource.ID == "" {
		return
	}

	_, itemID, ok := splitResourceID(a.Resource.ID)
	if !ok {
		return
	}

	path, err := r.paths.ItemPath(ctx, authHeader, driveID, itemID)
	if err != nil {
		r.log.Warn("reconcile: resolve activity path", "userID", userID, "error", err)
		return
	}
	if webdavstore.IsInternalPath(path) {
		return
	}

	for _, e := range entries {
		if e.UserID != userID || e.EventType != triggerType {
			continue
		}
		if !e.MatchesFilters(path, driveID) {
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
// into (spaceID, itemID). spaceID is everything before the last "!"; itemID is the *whole*
// original id, unchanged — oCIS's Graph API requires the full compound resourceID as the
// itemID path segment (verified live: /graph/v1.0/drives/{spaceID}/items/{opaqueOnly} 404s
// with itemNotFound, while .../items/{fullCompoundID} resolves), it does not accept the bare
// opaque suffix. spaceID is returned for completeness/validation but callers should prefer
// an already-known driveID over it when one is available (see dispatch): driveID is already
// exactly this same "storageid$spaceid" format, so the parsed value isn't wrong-format, it's
// just redundant with something the caller already has authoritatively in hand.
func splitResourceID(id string) (spaceID, itemID string, ok bool) {
	idx := strings.LastIndex(id, "!")
	if idx < 0 {
		return "", "", false
	}
	return id[:idx], id, true
}
