package service

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/ratelimit"
)

// maxWebhookBodyBytes caps how much of an incoming webhook request body is read into
// memory — this route is publicly reachable and token-gated rather than behind the
// platform's own reverse-proxy limits, so an unbounded read would be a trivial memory-
// exhaustion vector.
const maxWebhookBodyBytes = 1 << 20 // 1 MiB

// HooksTriggerStore is the subset of localdb.DB the webhook route needs to look up and
// verify a caller-supplied token.
type HooksTriggerStore interface {
	GetTriggerIndexEntry(ctx context.Context, workflowID string) (*localdb.TriggerIndexEntry, error)
}

// HooksAutomationStore is the subset of localdb.DB needed to build the same
// "Basic "+base64(username:apppassword) auth header the cron scheduler and SSE consumer
// manager already construct for running a workflow without a live user request.
type HooksAutomationStore interface {
	GetAutomation(ctx context.Context, userID string) (*localdb.Automation, error)
}

// HooksWorkflowStore is the subset of webdavstore.Store needed to run a workflow.
type HooksWorkflowStore interface {
	Get(ctx context.Context, authHeader, id string) (*model.WorkflowDefinition, error)
	PutExecution(ctx context.Context, authHeader string, rec model.ExecutionRecord) error
}

// HooksExecutor runs a workflow's graph with extra seed vars. Satisfied by *executor.Executor.
type HooksExecutor interface {
	RunWithVars(ctx context.Context, authHeader string, wf model.WorkflowDefinition, triggeredBy, resourcePath string, extraVars map[string]string) *model.ExecutionRecord
}

// HooksHandler implements the public webhook trigger route,
// POST /hooks/{workflowId}/{token} — deliberately outside the /api/v1beta1 route group and
// its Validator.Middleware bearer-token gate (see pkg/server/http/server.go): an external
// caller (a CI pipeline, another SaaS's outgoing webhook, a form submission) has no oCIS
// session to present a bearer token from. The per-workflow token in the URL is the only
// credential, checked in constant time; a request-rate limiter is the compensating control
// against token-guessing/flooding since normal auth doesn't gate this route.
type HooksHandler struct {
	triggers   HooksTriggerStore
	automation HooksAutomationStore
	store      HooksWorkflowStore
	executor   HooksExecutor
	limiter    *ratelimit.Limiter
	log        *slog.Logger
}

// NewHooksHandler builds a HooksHandler.
func NewHooksHandler(triggers HooksTriggerStore, automation HooksAutomationStore, store HooksWorkflowStore, executor HooksExecutor, limiter *ratelimit.Limiter, log *slog.Logger) *HooksHandler {
	return &HooksHandler{triggers: triggers, automation: automation, store: store, executor: executor, limiter: limiter, log: log}
}

// Trigger handles POST /hooks/{workflowId}/{token}.
func (h *HooksHandler) Trigger(w http.ResponseWriter, r *http.Request) {
	workflowID := chi.URLParam(r, "workflowId")
	token := chi.URLParam(r, "token")

	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing webhook token")
		return
	}

	entry, err := h.triggers.GetTriggerIndexEntry(r.Context(), workflowID)
	// Deliberately identical response whether the workflow id is unknown, isn't a webhook
	// trigger, has no token generated yet, or the token plain doesn't match — never let a
	// caller distinguish "wrong token" from "wrong/invalid workflow id", which would leak
	// which workflow ids are live webhook targets to anyone probing.
	if err != nil || entry.TriggerType != "webhook" || entry.WebhookToken == "" {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid webhook token")
		return
	}

	// Rate-limit keyed by workflowID, not the caller-supplied token, and only once we know
	// workflowID names a real webhook-triggered workflow: workflowID's cardinality is
	// bounded by how many such workflows actually exist, so an attacker flooding garbage
	// tokens (or garbage workflow ids) against this route can't grow the limiter's map
	// without bound the way keying on the raw, attacker-controlled token would. This still
	// rate-limits repeated wrong-token guesses against a real workflow — the actual brute-
	// force scenario this exists to stop — just via a key an attacker can't inflate.
	if h.limiter != nil && !h.limiter.Allow(workflowID) {
		writeError(w, http.StatusTooManyRequests, "rateLimited", "too many requests for this webhook token")
		return
	}

	if subtle.ConstantTimeCompare([]byte(token), []byte(entry.WebhookToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid webhook token")
		return
	}

	automationCred, err := h.automation.GetAutomation(r.Context(), entry.UserID)
	if err != nil {
		h.log.Warn("hooks: workflow has a webhook trigger but its owner has no automation connected", "workflowID", workflowID)
		writeError(w, http.StatusServiceUnavailable, "automationNotConnected", "workflow owner has not connected automation")
		return
	}
	authHeader := "Basic " + base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", automationCred.Username, automationCred.AppPassword))

	wf, err := h.store.Get(r.Context(), authHeader, workflowID)
	if err != nil {
		h.log.Error("hooks: load workflow", "workflowID", workflowID, "error", err)
		writeError(w, http.StatusNotFound, "workflowNotFound", "workflow not found")
		return
	}

	// Read one byte past the cap so a body that exactly fills it can be told apart from one
	// that overflows it — LimitReader alone can't distinguish "exactly maxWebhookBodyBytes"
	// from "more, silently cut off", which would otherwise feed a truncated (and likely
	// unparseable) body into the executor with no indication to the caller.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidRequest", "could not read request body")
		return
	}
	if len(body) > maxWebhookBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "requestTooLarge", "request body exceeds the maximum allowed size")
		return
	}

	vars := webhookVars(body)
	resourcePath := r.URL.Query().Get("path")

	if !wf.Enabled {
		// Never let a valid-token caller distinguish "ran" from "workflow currently
		// disabled" via status code — same 202 either way, matching how the scheduler/SSE
		// manager silently skip a disabled workflow rather than erroring.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	record := h.executor.RunWithVars(r.Context(), authHeader, *wf, "webhook", resourcePath, vars)
	if err := h.store.PutExecution(r.Context(), authHeader, *record); err != nil {
		h.log.Error("hooks: store execution record", "workflowID", workflowID, "error", err)
	}

	w.WriteHeader(http.StatusAccepted)
}

// webhookVars builds the vars a webhook-triggered run seeds the graph with:
// vars["webhook.body"] always holds the raw request body as a string; if it parses as a
// JSON object, each top-level key is also flattened into vars["webhook.body.<key>"]. A
// non-JSON, malformed, or non-object (array/scalar) body is not an error — flattening is
// simply skipped, matching the executor's own tolerant vars/render() model.
func webhookVars(body []byte) map[string]string {
	vars := map[string]string{"webhook.body": string(body)}
	if len(body) == 0 {
		return vars
	}

	var asObject map[string]any
	if err := json.Unmarshal(body, &asObject); err != nil {
		return vars
	}
	for key, value := range asObject {
		vars["webhook.body."+key] = stringifyJSONValue(value)
	}
	return vars
}

// stringifyJSONValue renders a decoded JSON value as the string vars/render() expects:
// strings pass through unchanged; anything else (numbers, bools, null, nested
// objects/arrays) is re-marshaled to its JSON text form.
func stringifyJSONValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
