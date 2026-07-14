package localdb

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	key := make([]byte, 32)
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestAutomationRoundTripIsEncryptedAtRest(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	err := db.UpsertAutomation(ctx, Automation{
		UserID:      "user-1",
		Username:    "admin",
		AppPassword: "s3cr3t",
		ExpiresAt:   now.Add(90 * 24 * time.Hour),
		ConnectedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}

	got, err := db.GetAutomation(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetAutomation: %v", err)
	}
	if got.AppPassword != "s3cr3t" || got.Username != "admin" {
		t.Fatalf("GetAutomation() = %+v", got)
	}

	// The raw column must not contain the plaintext secret.
	var raw string
	if err := db.sql.QueryRow(`SELECT encrypted_app_password FROM automations WHERE user_id = ?`, "user-1").Scan(&raw); err != nil {
		t.Fatalf("read raw column: %v", err)
	}
	if raw == "s3cr3t" {
		t.Fatal("app password stored in plaintext")
	}

	if err := db.DeleteAutomation(ctx, "user-1"); err != nil {
		t.Fatalf("DeleteAutomation: %v", err)
	}
	if _, err := db.GetAutomation(ctx, "user-1"); err != ErrNotFound {
		t.Fatalf("GetAutomation after delete: expected ErrNotFound, got %v", err)
	}
}

func TestUpsertAutomationReplacesExisting(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	base := Automation{UserID: "user-1", Username: "admin", AppPassword: "first", ExpiresAt: time.Now(), ConnectedAt: time.Now()}
	if err := db.UpsertAutomation(ctx, base); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}
	base.AppPassword = "second"
	if err := db.UpsertAutomation(ctx, base); err != nil {
		t.Fatalf("UpsertAutomation (replace): %v", err)
	}

	got, err := db.GetAutomation(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetAutomation: %v", err)
	}
	if got.AppPassword != "second" {
		t.Fatalf("expected replaced password, got %q", got.AppPassword)
	}

	all, err := db.ListAutomations(ctx)
	if err != nil {
		t.Fatalf("ListAutomations: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 automation after upsert-replace, got %d", len(all))
	}
}

func TestTriggerIndex(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	if err := db.UpsertTriggerIndexEntry(ctx, TriggerIndexEntry{
		WorkflowID: "wf-1", UserID: "user-1", TriggerType: "schedule", Schedule: "0 * * * *",
	}); err != nil {
		t.Fatalf("UpsertTriggerIndexEntry: %v", err)
	}
	if err := db.UpsertTriggerIndexEntry(ctx, TriggerIndexEntry{
		WorkflowID: "wf-2", UserID: "user-1", TriggerType: "event", EventType: "upload",
		PathPrefix: "/Invoices", Extension: ".pdf",
	}); err != nil {
		t.Fatalf("UpsertTriggerIndexEntry: %v", err)
	}

	schedules, err := db.ListScheduleTriggers(ctx)
	if err != nil {
		t.Fatalf("ListScheduleTriggers: %v", err)
	}
	if len(schedules) != 1 || schedules[0].WorkflowID != "wf-1" {
		t.Fatalf("ListScheduleTriggers() = %+v", schedules)
	}

	events, err := db.ListEventTriggers(ctx)
	if err != nil {
		t.Fatalf("ListEventTriggers: %v", err)
	}
	if len(events) != 1 || events[0].WorkflowID != "wf-2" {
		t.Fatalf("ListEventTriggers() = %+v", events)
	}
	if events[0].PathPrefix != "/Invoices" || events[0].Extension != ".pdf" {
		t.Fatalf("ListEventTriggers() filters = %+v", events[0])
	}

	if err := db.DeleteTriggerIndexEntry(ctx, "wf-1"); err != nil {
		t.Fatalf("DeleteTriggerIndexEntry: %v", err)
	}
	schedules, _ = db.ListScheduleTriggers(ctx)
	if len(schedules) != 0 {
		t.Fatalf("expected schedule trigger removed, got %+v", schedules)
	}
}

// TestMigrateAddsColumnsToExistingTable regression-tests a real bug: CREATE TABLE IF NOT
// EXISTS is a no-op against a trigger_index table that already exists from before
// path_prefix/extension were added, so opening an existing (pre-M4) database used to fail
// every trigger_index query with "no such column: path_prefix".
func TestMigrateAddsColumnsToExistingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE trigger_index (
			workflow_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			trigger_type TEXT NOT NULL,
			schedule TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		t.Fatalf("create old-shape table: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	db, err := Open(path, make([]byte, 32))
	if err != nil {
		t.Fatalf("Open on pre-existing old-shape database: %v", err)
	}
	defer db.Close()

	ctx := t.Context()
	if err := db.UpsertTriggerIndexEntry(ctx, TriggerIndexEntry{
		WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", PathPrefix: "/Invoices",
	}); err != nil {
		t.Fatalf("UpsertTriggerIndexEntry after migration: %v", err)
	}

	events, err := db.ListEventTriggers(ctx)
	if err != nil {
		t.Fatalf("ListEventTriggers after migration: %v", err)
	}
	if len(events) != 1 || events[0].PathPrefix != "/Invoices" {
		t.Fatalf("ListEventTriggers() after migration = %+v", events)
	}
}
