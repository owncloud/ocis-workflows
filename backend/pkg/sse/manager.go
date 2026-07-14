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

// Manager keeps one SSE consumer goroutine running per user with an active event trigger,
// reconciling against the trigger index on a fixed interval.
type Manager struct {
	db       TriggerStore
	store    WorkflowStore
	paths    PathResolver
	executor Executor
	ocisURL  string
	insecure bool
	interval time.Duration
	log      *slog.Logger

	httpClient *http.Client

	mu     sync.Mutex
	active map[string]context.CancelFunc // userID -> cancel
}

// New builds a Manager.
func New(db TriggerStore, store WorkflowStore, paths PathResolver, executor Executor, ocisURL string, insecure bool, interval time.Duration, log *slog.Logger) *Manager {
	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-only opt-in
	}
	return &Manager{
		db:         db,
		store:      store,
		paths:      paths,
		executor:   executor,
		ocisURL:    strings.TrimRight(ocisURL, "/"),
		insecure:   insecure,
		interval:   interval,
		log:        log,
		httpClient: &http.Client{Transport: transport}, // no overall Timeout: this is a long-lived stream
		active:     map[string]context.CancelFunc{},
	}
}

// Start blocks, reconciling active consumers every interval, until ctx is done.
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

	for userID, cancel := range m.active {
		if !wanted[userID] {
			cancel()
			delete(m.active, userID)
		}
	}
	for userID := range wanted {
		if _, ok := m.active[userID]; ok {
			continue
		}
		cctx, cancel := context.WithCancel(ctx)
		m.active[userID] = cancel
		go m.consumeForUser(cctx, userID)
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for userID, cancel := range m.active {
		cancel()
		delete(m.active, userID)
	}
}

// consumeForUser holds one SSE connection open for userID, reconnecting with backoff if it
// drops, until ctx is cancelled (the user's last event trigger was removed, or shutdown).
func (m *Manager) consumeForUser(ctx context.Context, userID string) {
	automation, err := m.db.GetAutomation(ctx, userID)
	if err != nil {
		m.log.Warn("sse manager: user has an event trigger but no automation connected", "userID", userID)
		return
	}
	authHeader := "Basic " + base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", automation.Username, automation.AppPassword))

	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := m.streamOnce(ctx, userID, authHeader); err != nil && ctx.Err() == nil {
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

func (m *Manager) streamOnce(ctx context.Context, userID, authHeader string) error {
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

		if e.PathPrefix != "" && !strings.HasPrefix(resolvedPath, e.PathPrefix) {
			continue
		}
		if e.Extension != "" && !strings.HasSuffix(resolvedPath, e.Extension) {
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
