package automation

import (
	"testing"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
)

func TestStatusReportsConnectedForFutureExpiry(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID:      "user-1",
		Username:    "admin",
		AppPassword: "still-fresh",
		ExpiresAt:   expiresAt,
		ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}

	svc := New(&fakeGraphClient{}, db, nil, discardLogger())

	status, err := svc.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Connected {
		t.Fatalf("Connected = false, want true")
	}
	want := expiresAt.UTC().Format(time.RFC3339)
	if status.ExpirationDateTime != want {
		t.Fatalf("ExpirationDateTime = %q, want %q", status.ExpirationDateTime, want)
	}
}

func TestStatusReportsDisconnectedForPastExpiry(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID:      "user-1",
		Username:    "admin",
		AppPassword: "expired-in-place",
		ExpiresAt:   time.Now().Add(-time.Hour), // already expired, but the row was never deleted
		ConnectedAt: time.Now().Add(-100 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}

	svc := New(&fakeGraphClient{}, db, nil, discardLogger())

	status, err := svc.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Connected {
		t.Fatalf("Connected = true, want false for a past-expiry credential")
	}
	if status.ExpirationDateTime != "" {
		t.Fatalf("ExpirationDateTime = %q, want empty", status.ExpirationDateTime)
	}
}

func TestStatusReportsDisconnectedWhenNeverConnected(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	svc := New(&fakeGraphClient{}, db, nil, discardLogger())

	status, err := svc.Status(ctx, "never-connected-user")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Connected {
		t.Fatalf("Connected = true, want false for a user with no stored automation")
	}
	if status.ExpirationDateTime != "" {
		t.Fatalf("ExpirationDateTime = %q, want empty", status.ExpirationDateTime)
	}
}

func TestStatusReportsFullReliabilityWithNoCursors(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID: "user-1", Username: "admin", AppPassword: "fresh", ExpiresAt: expiresAt, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}

	svc := New(&fakeGraphClient{}, db, nil, discardLogger())
	status, err := svc.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Reliability != "full" {
		t.Errorf("Reliability = %q, want %q", status.Reliability, "full")
	}
}

func TestStatusReportsDegradedReliabilityWhenReconciliationFailed(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID: "user-1", Username: "admin", AppPassword: "fresh", ExpiresAt: expiresAt, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}
	if err := db.UpsertEventCursor(ctx, localdb.EventCursor{UserID: "user-1", DriveID: "drive-1", LastChecked: time.Now(), LastStatus: "sse-only"}); err != nil {
		t.Fatalf("UpsertEventCursor: %v", err)
	}

	svc := New(&fakeGraphClient{}, db, nil, discardLogger())
	status, err := svc.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Reliability != "sse-only" {
		t.Errorf("Reliability = %q, want %q", status.Reliability, "sse-only")
	}
}

func TestStatusOmitsReliabilityWhenDisconnected(t *testing.T) {
	db := testDB(t)
	svc := New(&fakeGraphClient{}, db, nil, discardLogger())

	status, err := svc.Status(t.Context(), "never-connected-user")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Connected {
		t.Fatalf("expected Connected = false")
	}
	if status.Reliability != "" {
		t.Errorf("Reliability = %q, want empty when not connected", status.Reliability)
	}
}

// TestDisconnectAlsoClearsEventCursors regression-tests a real gap: a drive marked
// "sse-only" (degraded) that then falls out of scope (user disconnects automation, deletes
// the workflow, or narrows an unscoped trigger) used to leave its cursor row untouched
// forever — GetReliability would keep reporting "sse-only" with no way to clear it, and rows
// would just accumulate indefinitely. Disconnect must clear them alongside the automation
// credential itself.
func TestDisconnectAlsoClearsEventCursors(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID: "user-1", Username: "admin", AppPassword: "secret",
		ExpiresAt: time.Now().Add(24 * time.Hour), ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}
	if err := db.UpsertEventCursor(ctx, localdb.EventCursor{
		UserID: "user-1", DriveID: "drive-1", LastChecked: time.Now(), LastStatus: "sse-only",
	}); err != nil {
		t.Fatalf("UpsertEventCursor: %v", err)
	}
	// A different user's cursor must survive user-1's disconnect.
	if err := db.UpsertEventCursor(ctx, localdb.EventCursor{
		UserID: "user-2", DriveID: "drive-1", LastChecked: time.Now(), LastStatus: "full",
	}); err != nil {
		t.Fatalf("UpsertEventCursor(user-2): %v", err)
	}

	graph := &fakeGraphClient{meUserID: "user-1"}
	svc := New(graph, db, nil, discardLogger())

	if err := svc.Disconnect(ctx, "Basic dGVzdA=="); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if _, err := db.GetEventCursor(ctx, "user-1", "drive-1"); err != localdb.ErrNotFound {
		t.Errorf("GetEventCursor(user-1) after Disconnect: err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetAutomation(ctx, "user-1"); err != localdb.ErrNotFound {
		t.Errorf("GetAutomation(user-1) after Disconnect: err = %v, want ErrNotFound", err)
	}

	reliability, err := db.GetReliability(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetReliability(user-1): %v", err)
	}
	if reliability != "full" {
		t.Errorf("GetReliability(user-1) after Disconnect = %q, want %q (no cursors left to be degraded)", reliability, "full")
	}

	if _, err := db.GetEventCursor(ctx, "user-2", "drive-1"); err != nil {
		t.Errorf("GetEventCursor(user-2) after user-1's Disconnect: %v (should be untouched)", err)
	}
}
