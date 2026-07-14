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

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/webdavstore"
)

// Executor runs a workflow's graph. Satisfied by *executor.Executor; an interface here so
// this package doesn't need to depend on executor's own dependencies (llm, webdavfile, ...).
type Executor interface {
	Run(ctx context.Context, token string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord
}

// WorkflowsHandler implements the /me/workflows Graph-shaped REST API.
type WorkflowsHandler struct {
	store    *webdavstore.Store
	executor Executor
	log      *slog.Logger
	now      func() time.Time
}

// NewWorkflowsHandler builds a WorkflowsHandler backed by the given store and executor.
func NewWorkflowsHandler(store *webdavstore.Store, executor Executor, log *slog.Logger) *WorkflowsHandler {
	return &WorkflowsHandler{store: store, executor: executor, log: log, now: time.Now}
}

// List handles GET /me/workflows.
func (h *WorkflowsHandler) List(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	workflows, err := h.store.List(r.Context(), token)
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

	if err := h.store.Put(r.Context(), token, wf); err != nil {
		h.log.Error("create workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not create workflow")
		return
	}

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
	wf, err := h.store.Get(r.Context(), token, id)
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
	existing, err := h.store.Get(r.Context(), token, id)
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

	if err := h.store.Put(r.Context(), token, *existing); err != nil {
		h.log.Error("patch workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not update workflow")
		return
	}

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
	if err := h.store.Delete(r.Context(), token, id); err != nil {
		if errors.Is(err, webdavstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflowNotFound", "the requested workflow was not found")
			return
		}
		h.log.Error("delete workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not delete workflow")
		return
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
	wf, err := h.store.Get(r.Context(), token, id)
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

	record := h.executor.Run(r.Context(), token, *wf, "manual", req.ResourcePath)

	if err := h.store.PutExecution(r.Context(), token, *record); err != nil {
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
	executions, err := h.store.ListExecutions(r.Context(), token, id)
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
	record, err := h.store.GetExecution(r.Context(), token, id, execID)
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
