package service

import (
	"context"
	"net/http"

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/model"
)

// AutomationService implements the /me/automation status/connect/disconnect operations.
// Satisfied by *automation.Service.
type AutomationService interface {
	Status(ctx context.Context, userID string) (*model.AutomationStatus, error)
	Connect(ctx context.Context, authHeader string) (*model.AutomationStatus, error)
	Disconnect(ctx context.Context, authHeader string) error
}

// UserResolver resolves the current user's id from an auth header. Satisfied by *ocisclient.Client.
type UserResolver interface {
	Me(ctx context.Context, authHeader string) (string, error)
}

// AutomationHandler implements the /me/automation Graph-shaped REST API.
type AutomationHandler struct {
	automation AutomationService
	users      UserResolver
}

// NewAutomationHandler builds an AutomationHandler.
func NewAutomationHandler(automation AutomationService, users UserResolver) *AutomationHandler {
	return &AutomationHandler{automation: automation, users: users}
}

// Get handles GET /me/automation.
func (h *AutomationHandler) Get(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	userID, err := h.users.Me(r.Context(), "Bearer "+token)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ocisUnavailable", "could not resolve current user")
		return
	}

	status, err := h.automation.Status(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "automationUnavailable", "could not read automation status")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// Connect handles POST /me/automation.
func (h *AutomationHandler) Connect(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	status, err := h.automation.Connect(r.Context(), "Bearer "+token)
	if err != nil {
		writeError(w, http.StatusBadGateway, "automationUnavailable", "could not enable automation: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// Disconnect handles DELETE /me/automation.
func (h *AutomationHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	if err := h.automation.Disconnect(r.Context(), "Bearer "+token); err != nil {
		writeError(w, http.StatusBadGateway, "automationUnavailable", "could not disable automation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
