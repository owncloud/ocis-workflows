package service

import (
	"context"
	"net/http"

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/ocisclient"
)

// DriveLister is the subset of ocisclient.Client SpacesHandler needs. Satisfied by
// *ocisclient.Client.
type DriveLister interface {
	ListDrives(ctx context.Context, authHeader string) ([]ocisclient.Drive, error)
}

// SpacesHandler implements the /me/spaces Graph-shaped REST API — a read-only list of the
// caller's spaces, used to populate the event-trigger "scope to a space" picker.
type SpacesHandler struct {
	drives DriveLister
}

// NewSpacesHandler builds a SpacesHandler.
func NewSpacesHandler(drives DriveLister) *SpacesHandler {
	return &SpacesHandler{drives: drives}
}

// List handles GET /me/spaces. "virtual" drives (e.g. the aggregate "Shares" view) are
// excluded — they're not a real place file events originate "in," so scoping a trigger to
// one wouldn't mean anything coherent.
func (h *SpacesHandler) List(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	drives, err := h.drives.ListDrives(r.Context(), "Bearer "+token)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ocisUnavailable", "could not list spaces")
		return
	}

	spaces := make([]model.Space, 0, len(drives))
	for _, d := range drives {
		if d.DriveType == "virtual" {
			continue
		}
		spaces = append(spaces, model.Space{ID: d.ID, Name: d.Name})
	}

	writeJSON(w, http.StatusOK, model.Collection[model.Space]{Value: spaces})
}
