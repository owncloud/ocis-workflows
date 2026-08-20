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

// dispatch maps an activitylog entry to a trigger type, resolves its path, and runs every
// enabled workflow whose trigger matches — mirroring sse.Manager.handleEvent's matching
// logic exactly (same MatchesFilters helper, same internal-path guard) so the two event
// paths can't drift apart on what counts as a match. driveID (the drive this activity was
// queried from, already known and already in the same plain form TriggerIndexEntry.SpaceID
// stores) is used as the space scope throughout — not a value reparsed out of the
// activity's own resourceId, which carries an unrelated storage-provider prefix.
func (r *Reconciler) dispatch(ctx context.Context, userID, authHeader, driveID string, a ocisclient.Activity) {
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

	entries, err := r.db.ListEventTriggers(ctx)
	if err != nil {
		r.log.Warn("reconcile: list event triggers", "error", err)
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
// into (spaceID, itemID), where spaceID is everything before the last "!" and itemID is
// everything after it. spaceID is returned for completeness/validation but callers should
// prefer an already-known driveID over it when one is available (see dispatch) — it carries
// a storage-provider prefix that doesn't match the plain drive id used elsewhere
// (TriggerIndexEntry.SpaceID, the driveID ListActivities was queried with).
func splitResourceID(id string) (spaceID, itemID string, ok bool) {
	idx := strings.LastIndex(id, "!")
	if idx < 0 {
		return "", "", false
	}
	return id[:idx], id[idx+1:], true
}
