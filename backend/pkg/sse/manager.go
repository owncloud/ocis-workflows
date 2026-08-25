// Package sse maintains one persistent SSE connection per user who has at least one
// enabled event-triggered workflow, using oCIS's public sse notification endpoint — never
// a NATS client. It's the only mechanism any oCIS service exposes over HTTP for reacting to
// file activity — a known coverage gap: tags aren't forwarded through SSE, so
// tag-added/tag-removed triggers aren't supported yet.
package sse

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/webdavstore"
)

// eventTypeMap translates the SSE "event:" field (see clientlog in oCIS core) to our own
// trigger vocabulary. Only "postprocessing-finished" (upload) has been verified against a
// live oCIS instance; the rest follow the same naming convention but are not individually
// e2e-tested — an unrecognized event type is simply ignored, not treated as an error.
var eventTypeMap = map[string]string{
	"postprocessing-finished": "upload",
	"item-renamed":            "move",
	"share-created":           "share",
	"link-created":            "share",
	"file-locked":             "lock",
}

type eventPayload struct {
	ItemID  string `json:"itemid"`
	SpaceID string `json:"spaceid"`
}

// TriggerStore is the subset of localdb.DB the SSE manager reads.
type TriggerStore interface {
	ListEventTriggers(ctx context.Context) ([]localdb.TriggerIndexEntry, error)
	GetAutomation(ctx context.Context, userID string) (*localdb.Automation, error)
}

// WorkflowStore is the subset of webdavstore.Store needed to run a workflow.
type WorkflowStore interface {
	Get(ctx context.Context, authHeader, id string) (*model.WorkflowDefinition, error)
	PutExecution(ctx context.Context, authHeader string, rec model.ExecutionRecord) error
}

// PathResolver resolves an SSE event's item to a WebDAV path. Satisfied by *ocisclient.Client.
type PathResolver interface {
	ItemPath(ctx context.Context, authHeader, spaceID, itemID string) (string, error)
}

// Executor runs a workflow's graph. Satisfied by *executor.Executor.
type Executor interface {
	Run(ctx context.Context, authHeader string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord
}

// Reconciler runs an activitylog-based backstop pass for a user, catching up on anything
// their SSE connection may have missed while it was down. Satisfied by
// *reconcile.Reconciler.
type Reconciler interface {
	Reconcile(ctx context.Context, userID, authHeader string)
}

// Manager keeps one SSE consumer goroutine running per user with an active event trigger,
// reconciling against the trigger index on a fixed interval.
type Manager struct {
	db         TriggerStore
	store      WorkflowStore
	paths      PathResolver
	executor   Executor
	reconciler Reconciler
	ocisURL    string
	insecure   bool
	interval   time.Duration
	log        *slog.Logger

	httpClient *http.Client

	mu     sync.Mutex
	active map[string]activeConsumer // userID -> consumer
	nextID uint64                    // monotonically increasing id, tags each activeConsumer

	kick chan struct{}
}

// activeConsumer tracks one userID's running consumeForUser goroutine. id disambiguates
// consumeForUser's self-removal (see deactivate) from a newer goroutine that reconcile may
// have already started for the same userID by the time the old one gets around to cleaning
// up after itself — without it, that self-removal could delete a live entry that isn't even
// the one it owns.
type activeConsumer struct {
	cancel context.CancelFunc
	id     uint64
}

// New builds a Manager.
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
		httpClient: &http.Client{Transport: transport}, // no overall Timeout: this is a long-lived stream
		active:     map[string]activeConsumer{},
		kick:       make(chan struct{}, 1),
	}
}

// Kick requests an immediate reconcile pass instead of waiting for the next periodic tick.
// Callers that just made a change that could affect the wanted-consumer set — a workflow's
// event trigger being added/enabled, or a user's automation getting connected — should call
// this so the corresponding SSE consumer starts promptly instead of within the next interval
// (up to sseReconcileInterval later). Non-blocking and safe to call from any goroutine; if a
// kick is already pending, this is a no-op since one reconcile pass picks up all pending
// changes anyway.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Start blocks, reconciling active consumers every interval — or immediately whenever Kick
// is called — until ctx is done.
func (m *Manager) Start(ctx context.Context) {
	m.reconcile(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.reconcile(ctx)
		case <-m.kick:
			m.reconcile(ctx)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	entries, err := m.db.ListEventTriggers(ctx)
	if err != nil {
		m.log.Error("sse manager: list event triggers", "error", err)
		return
	}

	wanted := map[string]bool{}
	for _, e := range entries {
		wanted[e.UserID] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for userID, c := range m.active {
		if !wanted[userID] {
			c.cancel()
			delete(m.active, userID)
		}
	}
	for userID := range wanted {
		if _, ok := m.active[userID]; ok {
			continue
		}
		cctx, cancel := context.WithCancel(ctx)
		m.nextID++
		id := m.nextID
		m.active[userID] = activeConsumer{cancel: cancel, id: id}
		go m.consumeForUser(cctx, userID, id)
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for userID, c := range m.active {
		c.cancel()
		delete(m.active, userID)
	}
}

// deactivate removes userID's active-consumer entry, but only if it still belongs to the
// caller (identified by id) — i.e. reconcile hasn't already replaced it with a newer
// consumer for the same userID in the meantime. See consumeForUser and activeConsumer.
func (m *Manager) deactivate(userID string, id uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.active[userID]; ok && c.id == id {
		delete(m.active, userID)
	}
}

// consumeForUser holds one SSE connection open for userID, reconnecting with backoff if it
// drops, until ctx is cancelled (the user's last event trigger was removed, or shutdown). id
// is the activeConsumer id reconcile assigned when it started this goroutine.
//
// Note on m.active bookkeeping: below this point, every return happens because ctx was
// cancelled — by reconcile (userID dropped out of wanted) or by stopAll (shutdown) — and
// both of those already remove the m.active entry themselves under the lock before
// cancelling, so consumeForUser must not also delete it there; doing so could race with a
// fresh entry a later reconcile pass has already installed for the same userID. The
// GetAutomation failure below is the one return path ctx did NOT cause, so it's the one case
// where this goroutine is responsible for cleaning up after itself.
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
			// This is a fire-and-forget background pass — a panic anywhere inside
			// Reconcile (or anything it calls) must not crash the whole process just
			// because one reconciliation attempt for one user went wrong.
			go func() {
				defer func() {
					if r := recover(); r != nil {
						m.log.Error("sse manager: reconciliation panicked", "userID", userID, "panic", r)
					}
				}()
				m.reconciler.Reconcile(ctx, userID, authHeader)
			}()
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

	// Proper SSE block parsing: a live oCIS instance was observed emitting "data:" *before*
	// "event:" within the same block, so both fields must be buffered and only dispatched
	// once the blank-line block terminator is reached — dispatching immediately on "data:"
	// (as if it always came last) would silently drop the event type whenever the server
	// orders fields that way.
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

func (m *Manager) handleEvent(ctx context.Context, userID, authHeader, sseEventType, data string) {
	triggerType, ok := eventTypeMap[sseEventType]
	if !ok {
		return
	}

	var payload eventPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		m.log.Warn("sse manager: could not decode event payload", "eventType", sseEventType, "error", err)
		return
	}

	entries, err := m.db.ListEventTriggers(ctx)
	if err != nil {
		m.log.Error("sse manager: list event triggers", "error", err)
		return
	}

	var resolvedPath string
	var resolvedOnce bool

	for _, e := range entries {
		if e.UserID != userID || e.EventType != triggerType {
			continue
		}

		if !resolvedOnce {
			resolvedOnce = true
			if payload.SpaceID != "" && payload.ItemID != "" {
				p, err := m.paths.ItemPath(ctx, authHeader, payload.SpaceID, payload.ItemID)
				if err != nil {
					m.log.Warn("sse manager: could not resolve event item path", "error", err)
				} else {
					resolvedPath = p
				}
			}
		}

		// This backend's own bookkeeping writes (workflow definitions, execution records)
		// live in the same user space and are indistinguishable from a real upload over
		// SSE — never match them, or an unfiltered event trigger would retrigger itself on
		// every execution it records.
		if webdavstore.IsInternalPath(resolvedPath) {
			continue
		}

		if !e.MatchesFilters(resolvedPath, payload.SpaceID) {
			continue
		}

		go m.runWorkflow(ctx, authHeader, e.WorkflowID, resolvedPath)
	}
}

func (m *Manager) runWorkflow(ctx context.Context, authHeader, workflowID, resourcePath string) {
	wf, err := m.store.Get(ctx, authHeader, workflowID)
	if err != nil {
		m.log.Error("sse manager: load workflow", "workflowID", workflowID, "error", err)
		return
	}
	if !wf.Enabled {
		return
	}

	record := m.executor.Run(ctx, authHeader, *wf, "event", resourcePath)
	if err := m.store.PutExecution(ctx, authHeader, *record); err != nil {
		m.log.Error("sse manager: store execution record", "workflowID", workflowID, "error", err)
	}
}
