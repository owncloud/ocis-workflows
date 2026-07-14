// Package automation lets a user enable background execution of their scheduled/event
// workflows. Enabling it mints an oCIS app-password (via auth-app) using the user's own
// live token in the same request — no separate consent redirect needed — and stores it
// encrypted in the sidecar's local database for the scheduler/SSE consumer to use later.
package automation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LukasHirt/ocis-workflows/pkg/localdb"
	"github.com/LukasHirt/ocis-workflows/pkg/model"
)

// defaultExpiry is how long a minted app-password lives before it must be re-connected.
// oCIS's auth-app requires an expiry — there is no non-expiring option.
const defaultExpiry = 90 * 24 * time.Hour

const tokenLabel = "workflows"

// GraphClient is the subset of ocisclient.Client automation needs.
type GraphClient interface {
	Me(ctx context.Context, authHeader string) (string, error)
	Username(ctx context.Context, authHeader string) (string, error)
	MintAppPassword(ctx context.Context, authHeader string, expiry time.Duration, label string) (token string, expiresAt time.Time, err error)
	RevokeAppPassword(ctx context.Context, authHeader, token string) error
}

// Service implements the /me/automation status/connect/disconnect operations.
type Service struct {
	graph GraphClient
	db    *localdb.DB
	log   *slog.Logger
}

// New builds a Service.
func New(graph GraphClient, db *localdb.DB, log *slog.Logger) *Service {
	return &Service{graph: graph, db: db, log: log}
}

func toStatus(a *localdb.Automation) *model.AutomationStatus {
	if a == nil {
		return &model.AutomationStatus{Connected: false}
	}
	return &model.AutomationStatus{
		Connected:          true,
		ExpirationDateTime: a.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// Status returns whether userID currently has automation enabled.
func (s *Service) Status(ctx context.Context, userID string) (*model.AutomationStatus, error) {
	a, err := s.db.GetAutomation(ctx, userID)
	if err != nil {
		if err == localdb.ErrNotFound {
			return toStatus(nil), nil
		}
		return nil, err
	}
	return toStatus(a), nil
}

// Connect mints a fresh app-password for the caller (identified by authHeader) and stores
// it, replacing any existing one.
func (s *Service) Connect(ctx context.Context, authHeader string) (*model.AutomationStatus, error) {
	userID, err := s.graph.Me(ctx, authHeader)
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}
	username, err := s.graph.Username(ctx, authHeader)
	if err != nil {
		return nil, fmt.Errorf("resolve current username: %w", err)
	}

	token, expiresAt, err := s.graph.MintAppPassword(ctx, authHeader, defaultExpiry, tokenLabel)
	if err != nil {
		return nil, fmt.Errorf("mint app password: %w", err)
	}

	a := localdb.Automation{
		UserID:      userID,
		Username:    username,
		AppPassword: token,
		ExpiresAt:   expiresAt,
		ConnectedAt: time.Now(),
	}
	if err := s.db.UpsertAutomation(ctx, a); err != nil {
		return nil, fmt.Errorf("store automation credential: %w", err)
	}

	s.log.Info("automation connected", "userID", userID)
	return toStatus(&a), nil
}

// Disconnect revokes and forgets the caller's stored app-password, if any.
func (s *Service) Disconnect(ctx context.Context, authHeader string) error {
	userID, err := s.graph.Me(ctx, authHeader)
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}

	existing, err := s.db.GetAutomation(ctx, userID)
	if err != nil {
		if err == localdb.ErrNotFound {
			return nil
		}
		return err
	}

	if err := s.graph.RevokeAppPassword(ctx, authHeader, existing.AppPassword); err != nil {
		// Not fatal — the token may already be expired/revoked server-side. Still forget
		// it locally so the user isn't stuck unable to reconnect.
		s.log.Warn("revoke app password failed, forgetting it locally anyway", "userID", userID, "error", err)
	}

	if err := s.db.DeleteAutomation(ctx, userID); err != nil {
		return fmt.Errorf("delete stored automation credential: %w", err)
	}
	s.log.Info("automation disconnected", "userID", userID)
	return nil
}
