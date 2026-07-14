// Package service implements the Graph-shaped HTTP handlers for workflow CRUD.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/LukasHirt/ocis-workflows/pkg/auth"
	"github.com/LukasHirt/ocis-workflows/pkg/localdb"
	"github.com/LukasHirt/ocis-workflows/pkg/model"
	"github.com/LukasHirt/ocis-workflows/pkg/webdavstore"
)

// Executor runs a workflow's graph. Satisfied by *executor.Executor; an interface here so
// this package doesn't need to depend on executor's own dependencies (llm, webdavfile, ...).
type Executor interface {
	Run(ctx context.Context, token string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord
}

// TriggerIndexer keeps the sidecar's local schedule/event trigger index (see localdb) in
// sync with workflow definitions, so the cron scheduler and SSE consumer manager don't need
// to scan every user's WebDAV space on every tick.
type TriggerIndexer interface {
	UpsertTriggerIndexEntry(ctx context.Context, e localdb.TriggerIndexEntry) error
	DeleteTriggerIndexEntry(ctx context.Context, workflowID string) error
}

// WorkflowsHandler implements the /me/workflows Graph-shaped REST API.
type WorkflowsHandler struct {
	store        *webdavstore.Store
	executor     Executor
	users        UserResolver
	triggerIndex TriggerIndexer
	log          *slog.Logger
	now          func() time.Time
}

// NewWorkflowsHandler builds a WorkflowsHandler backed by the given store and executor.
func NewWorkflowsHandler(store *webdavstore.Store, executor Executor, users UserResolver, triggerIndex TriggerIndexer, log *slog.Logger) *WorkflowsHandler {
	return &WorkflowsHandler{store: store, executor: executor, users: users, triggerIndex: triggerIndex, log: log, now: time.Now}
}

// syncTriggerIndex keeps the local trigger index in sync with a workflow's current
// enabled/trigger state — called after every successful create/update/delete. Best-effort:
// a failure here only means schedule/event triggers won't fire, it never breaks the CRUD
// operation the caller is waiting on, so errors are logged, not returned.
func (h *WorkflowsHandler) syncTriggerIndex(ctx context.Context, authHeader string, wf model.WorkflowDefinition) {
	if !wf.Enabled || (wf.Trigger.Type != "schedule" && wf.Trigger.Type != "event") {
		if err := h.triggerIndex.DeleteTriggerIndexEntry(ctx, wf.ID); err != nil {
			h.log.Error("remove trigger index entry", "workflowID", wf.ID, "error", err)
		}
		return
	}

	userID, err := h.users.Me(ctx, authHeader)
	if err != nil {
		h.log.Error("sync trigger index: resolve user", "workflowID", wf.ID, "error", err)
		return
	}

	entry := localdb.TriggerIndexEntry{WorkflowID: wf.ID, UserID: userID, TriggerType: wf.Trigger.Type}
	if wf.Trigger.Type == "schedule" {
		entry.Schedule = wf.Trigger.Schedule
	}
	if wf.Trigger.Type == "event" && wf.Trigger.Event != nil {
		entry.EventType = wf.Trigger.Event.Type
	}
	if err := h.triggerIndex.UpsertTriggerIndexEntry(ctx, entry); err != nil {
		h.log.Error("update trigger index entry", "workflowID", wf.ID, "error", err)
	}
}

// List handles GET /me/workflows.
func (h *WorkflowsHandler) List(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	workflows, err := h.store.List(r.Context(), "Bearer "+token)
	if err != nil {
		h.log.Error("list workflows", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not list workflows")
		return
	}

	writeJSON(w, http.StatusOK, model.Collection[model.WorkflowDefinition]{Value: workflows})
}

// Create handles POST /me/workflows.
func (h *WorkflowsHandler) Create(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	var patch model.WorkflowPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalidRequest", "request body is not valid JSON")
		return
	}
	if patch.Name == nil || *patch.Name == "" {
		writeError(w, http.StatusBadRequest, "invalidRequest", "name is required")
		return
	}

	now := h.now().UTC().Format(time.RFC3339Nano)
	wf := model.WorkflowDefinition{
		ID:                   uuid.NewString(),
		Name:                 *patch.Name,
		Enabled:              patch.Enabled != nil && *patch.Enabled,
		CreatedDateTime:      now,
		LastModifiedDateTime: now,
	}
	if patch.Description != nil {
		wf.Description = *patch.Description
	}
	if patch.Trigger != nil {
		wf.Trigger = *patch.Trigger
	} else {
		wf.Trigger = model.WorkflowTrigger{Type: "manual"}
	}
	if patch.Graph != nil {
		wf.Graph = *patch.Graph
	}

	if err := h.store.Put(r.Context(), "Bearer "+token, wf); err != nil {
		h.log.Error("create workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not create workflow")
		return
	}
	h.syncTriggerIndex(r.Context(), "Bearer "+token, wf)

	writeJSON(w, http.StatusCreated, wf)
}

// Get handles GET /me/workflows/{id}.
func (h *WorkflowsHandler) Get(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	id := chi.URLParam(r, "id")
	wf, err := h.store.Get(r.Context(), "Bearer "+token, id)
	if err != nil {
		if errors.Is(err, webdavstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflowNotFound", "the requested workflow was not found")
			return
		}
		h.log.Error("get workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not read workflow")
		return
	}

	writeJSON(w, http.StatusOK, wf)
}

// Patch handles PATCH /me/workflows/{id}.
func (h *WorkflowsHandler) Patch(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	id := chi.URLParam(r, "id")
	existing, err := h.store.Get(r.Context(), "Bearer "+token, id)
	if err != nil {
		if errors.Is(err, webdavstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflowNotFound", "the requested workflow was not found")
			return
		}
		h.log.Error("patch workflow: get existing", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not read workflow")
		return
	}

	var patch model.WorkflowPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalidRequest", "request body is not valid JSON")
		return
	}

	if patch.Name != nil {
		existing.Name = *patch.Name
	}
	if patch.Description != nil {
		existing.Description = *patch.Description
	}
	if patch.Enabled != nil {
		existing.Enabled = *patch.Enabled
	}
	if patch.Trigger != nil {
		existing.Trigger = *patch.Trigger
	}
	if patch.Graph != nil {
		existing.Graph = *patch.Graph
	}
	existing.LastModifiedDateTime = h.now().UTC().Format(time.RFC3339Nano)

	if err := h.store.Put(r.Context(), "Bearer "+token, *existing); err != nil {
		h.log.Error("patch workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not update workflow")
		return
	}
	h.syncTriggerIndex(r.Context(), "Bearer "+token, *existing)

	writeJSON(w, http.StatusOK, existing)
}

// Delete handles DELETE /me/workflows/{id}.
func (h *WorkflowsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.store.Delete(r.Context(), "Bearer "+token, id); err != nil {
		if errors.Is(err, webdavstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflowNotFound", "the requested workflow was not found")
			return
		}
		h.log.Error("delete workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not delete workflow")
		return
	}
	if err := h.triggerIndex.DeleteTriggerIndexEntry(r.Context(), id); err != nil {
		h.log.Error("remove trigger index entry", "workflowID", id, "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

type runRequest struct {
	ResourcePath string `json:"resourcePath"`
}

// Run handles POST /me/workflows/{id}/run. Runs synchronously (LLM/WebDAV calls are
// timeout-bounded) but responds the way an async Graph action would: 202 + Location
// pointing at the resulting execution resource, no body.
func (h *WorkflowsHandler) Run(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	id := chi.URLParam(r, "id")
	wf, err := h.store.Get(r.Context(), "Bearer "+token, id)
	if err != nil {
		if errors.Is(err, webdavstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflowNotFound", "the requested workflow was not found")
			return
		}
		h.log.Error("run workflow: get workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not read workflow")
		return
	}

	var req runRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalidRequest", "request body is not valid JSON")
			return
		}
	}

	record := h.executor.Run(r.Context(), "Bearer "+token, *wf, "manual", req.ResourcePath)

	if err := h.store.PutExecution(r.Context(), "Bearer "+token, *record); err != nil {
		h.log.Error("run workflow: store execution record", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "workflow ran but the execution record could not be saved")
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/me/workflows/%s/executions/%s", id, record.ID))
	w.WriteHeader(http.StatusAccepted)
}

// ListExecutions handles GET /me/workflows/{id}/executions.
func (h *WorkflowsHandler) ListExecutions(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	id := chi.URLParam(r, "id")
	executions, err := h.store.ListExecutions(r.Context(), "Bearer "+token, id)
	if err != nil {
		h.log.Error("list executions", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not list executions")
		return
	}

	writeJSON(w, http.StatusOK, model.Collection[model.ExecutionRecord]{Value: executions})
}

// GetExecution handles GET /me/workflows/{id}/executions/{execId}.
func (h *WorkflowsHandler) GetExecution(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	id := chi.URLParam(r, "id")
	execID := chi.URLParam(r, "execId")
	record, err := h.store.GetExecution(r.Context(), "Bearer "+token, id, execID)
	if err != nil {
		if errors.Is(err, webdavstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "executionNotFound", "the requested execution was not found")
			return
		}
		h.log.Error("get execution", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not read execution")
		return
	}

	writeJSON(w, http.StatusOK, record)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, model.ErrorResponse{Error: model.ErrorDetail{Code: code, Message: message}})
}
