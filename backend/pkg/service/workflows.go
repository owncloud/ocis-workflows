// Package service implements the Graph-shaped HTTP handlers for workflow CRUD.
package service

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/webdavstore"
)

// WorkflowsHandler implements the /me/workflows Graph-shaped REST API.
type WorkflowsHandler struct {
	store *webdavstore.Store
	log   *slog.Logger
	now   func() time.Time
}

// NewWorkflowsHandler builds a WorkflowsHandler backed by the given store.
func NewWorkflowsHandler(store *webdavstore.Store, log *slog.Logger) *WorkflowsHandler {
	return &WorkflowsHandler{store: store, log: log, now: time.Now}
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, model.ErrorResponse{Error: model.ErrorDetail{Code: code, Message: message}})
}
