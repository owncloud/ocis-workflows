package service

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/webdavstore"
)

// WebhookToken handles GET /me/workflows/{id}/webhook-token — the deliberate "reveal"
// action for a webhook trigger's token/URL. The raw token is intentionally never included
// in the normal workflow GET/List/Patch responses (which may be logged, cached, or synced
// more broadly than a one-off explicit reveal click in the NDV); it's only readable here,
// behind the same bearer-token auth as every other /me/workflows route.
func (h *WorkflowsHandler) WebhookToken(w http.ResponseWriter, r *http.Request) {
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
		h.log.Error("get webhook token: get workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not read workflow")
		return
	}
	if wf.Trigger.Type != "webhook" {
		writeError(w, http.StatusBadRequest, "notWebhookTrigger", "workflow does not have a webhook trigger")
		return
	}

	entry, err := h.triggerIndex.GetTriggerIndexEntry(r.Context(), id)
	if err != nil || entry.WebhookToken == "" {
		writeError(w, http.StatusNotFound, "webhookTokenNotFound", "no webhook token has been generated for this workflow yet")
		return
	}

	writeJSON(w, http.StatusOK, model.WebhookTokenResponse{Token: entry.WebhookToken, Path: webhookPath(id, entry.WebhookToken)})
}

// RotateWebhookToken handles POST /me/workflows/{id}/webhook-token/rotate — replaces the
// workflow's webhook token with a freshly generated one, immediately invalidating the
// previous URL for any external caller still using it. A deliberate action, distinct from
// the token that's auto-generated (and preserved) by syncTriggerIndex on ordinary saves.
func (h *WorkflowsHandler) RotateWebhookToken(w http.ResponseWriter, r *http.Request) {
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
		h.log.Error("rotate webhook token: get workflow", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not read workflow")
		return
	}
	if wf.Trigger.Type != "webhook" {
		writeError(w, http.StatusBadRequest, "notWebhookTrigger", "workflow does not have a webhook trigger")
		return
	}

	userID, err := h.users.Me(r.Context(), "Bearer "+token)
	if err != nil {
		h.log.Error("rotate webhook token: resolve user", "error", err)
		writeError(w, http.StatusBadGateway, "ocisUnavailable", "could not resolve current user")
		return
	}

	newToken, err := localdb.NewWebhookToken()
	if err != nil {
		h.log.Error("rotate webhook token: generate token", "error", err)
		writeError(w, http.StatusInternalServerError, "internalError", "could not generate a new webhook token")
		return
	}

	if err := h.triggerIndex.UpsertTriggerIndexEntry(r.Context(), localdb.TriggerIndexEntry{
		WorkflowID: id, UserID: userID, TriggerType: "webhook", WebhookToken: newToken,
	}); err != nil {
		h.log.Error("rotate webhook token: store entry", "error", err)
		writeError(w, http.StatusBadGateway, "storeUnavailable", "could not store new webhook token")
		return
	}

	writeJSON(w, http.StatusOK, model.WebhookTokenResponse{Token: newToken, Path: webhookPath(id, newToken)})
}

func webhookPath(workflowID, token string) string {
	return "/hooks/" + workflowID + "/" + token
}
