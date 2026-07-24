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
