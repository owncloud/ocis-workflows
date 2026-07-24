package automation

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
)

// renewalWindow is how close to expiry a stored automation must be before StartRenewalLoop
// mints a replacement. 14 days gives plenty of margin against a daily sweep interval, well
// within the 90-day defaultExpiry.
const renewalWindow = 14 * 24 * time.Hour

// StartRenewalLoop blocks, checking for automations nearing expiry every interval, until ctx
// is done. Renewal happens entirely server-side — no live user session is involved, only the
// stored app-password itself (see renewOne) — so background execution keeps working
// indefinitely without the user ever needing to revisit the app.
func (s *Service) StartRenewalLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.renewDue(ctx)
		}
	}
}

func (s *Service) renewDue(ctx context.Context) {
	automations, err := s.db.ListAutomations(ctx)
	if err != nil {
		s.log.Error("automation: list automations for renewal", "error", err)
		return
	}

	now := time.Now()
	for _, a := range automations {
		if a.ExpiresAt.Sub(now) > renewalWindow {
			continue
		}
		s.renewOne(ctx, a)
	}
}

// renewOne mints a replacement app-password for a, authenticating with a's own stored
// app-password over Basic auth — the same auth header the scheduler builds to run workflows
// (see scheduler.runOne) — rather than any live bearer token.
func (s *Service) renewOne(ctx context.Context, a localdb.Automation) {
	authHeader := "Basic " + base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", a.Username, a.AppPassword))

	token, expiresAt, err := s.graph.MintAppPassword(ctx, authHeader, defaultExpiry, tokenLabel)
	if err != nil {
		s.log.Error("automation: renew app password", "userID", a.UserID, "error", err)
		return
	}

	renewed := localdb.Automation{
		UserID:      a.UserID,
		Username:    a.Username,
		AppPassword: token,
		ExpiresAt:   expiresAt,
		ConnectedAt: a.ConnectedAt,
	}
	if err := s.db.UpsertAutomation(ctx, renewed); err != nil {
		s.log.Error("automation: store renewed app password", "userID", a.UserID, "error", err)
		return
	}

	// Best-effort — the old token being unrevokable (already expired/invalidated
	// out-of-band) shouldn't undo the renewal we just successfully stored.
	if err := s.graph.RevokeAppPassword(ctx, authHeader, a.AppPassword); err != nil {
		s.log.Warn("automation: revoke old app password after renewal, ignoring", "userID", a.UserID, "error", err)
	}

	s.log.Info("automation: renewed app password", "userID", a.UserID, "expiresAt", expiresAt)
}
