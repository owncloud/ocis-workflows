// Package localdb is the sidecar's own local operational state — never user content, never
// synced through any oCIS API. Holds encrypted app-passwords for users who've enabled
// scheduled/event automation, and a small denormalized index of which workflows have those
// triggers enabled (so the scheduler/SSE matcher don't have to scan every user's WebDAV
// space on every tick).
package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers as "sqlite"

	"github.com/LukasHirt/ocis-workflows/pkg/secretbox"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// Automation is a user's stored background-execution credential.
type Automation struct {
	UserID      string
	Username    string
	AppPassword string // plaintext once decrypted by Get; never logged
	ExpiresAt   time.Time
	ConnectedAt time.Time
}

// TriggerIndexEntry is a denormalized pointer to a workflow with an active schedule/event trigger.
type TriggerIndexEntry struct {
	WorkflowID  string
	UserID      string
	TriggerType string // schedule | event
	Schedule    string
	EventType   string
}

// DB is the sidecar's local SQLite-backed store.
type DB struct {
	sql *sql.DB
	box *secretbox.Box
}

// Open opens (creating if needed) the local database at path, encrypting app-passwords
// with the given key (see secretbox.New for its constraints).
func Open(path string, encryptionKey []byte) (*DB, error) {
	box, err := secretbox.New(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("init secretbox: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1) // modernc.org/sqlite + concurrent writers need serialization

	db := &DB{sql: sqlDB, box: box}
	if err := db.migrate(); err != nil {
		return nil, err
	}
	return db, nil
}

// Close closes the underlying database.
func (db *DB) Close() error {
	return db.sql.Close()
}

func (db *DB) migrate() error {
	_, err := db.sql.Exec(`
		CREATE TABLE IF NOT EXISTS automations (
			user_id TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			encrypted_app_password TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			connected_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS trigger_index (
			workflow_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			trigger_type TEXT NOT NULL,
			schedule TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL DEFAULT ''
		);
	`)
	return err
}

// UpsertAutomation stores or replaces a user's automation credential.
func (db *DB) UpsertAutomation(ctx context.Context, a Automation) error {
	encrypted, err := db.box.Seal(a.AppPassword)
	if err != nil {
		return fmt.Errorf("encrypt app password: %w", err)
	}
	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO automations (user_id, username, encrypted_app_password, expires_at, connected_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			username = excluded.username,
			encrypted_app_password = excluded.encrypted_app_password,
			expires_at = excluded.expires_at,
			connected_at = excluded.connected_at
	`, a.UserID, a.Username, encrypted, a.ExpiresAt.UTC().Format(time.RFC3339), a.ConnectedAt.UTC().Format(time.RFC3339))
	return err
}

// GetAutomation returns a user's stored automation credential, decrypted.
func (db *DB) GetAutomation(ctx context.Context, userID string) (*Automation, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT user_id, username, encrypted_app_password, expires_at, connected_at
		FROM automations WHERE user_id = ?
	`, userID)

	var a Automation
	var encrypted, expiresAt, connectedAt string
	if err := row.Scan(&a.UserID, &a.Username, &encrypted, &expiresAt, &connectedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	plaintext, err := db.box.Open(encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt app password: %w", err)
	}
	a.AppPassword = plaintext
	a.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	a.ConnectedAt, _ = time.Parse(time.RFC3339, connectedAt)
	return &a, nil
}

// DeleteAutomation removes a user's stored automation credential.
func (db *DB) DeleteAutomation(ctx context.Context, userID string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM automations WHERE user_id = ?`, userID)
	return err
}

// ListAutomations returns every stored automation credential (used by the cron scheduler
// and, in a future milestone, the SSE consumer manager).
func (db *DB) ListAutomations(ctx context.Context) ([]Automation, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT user_id, username, encrypted_app_password, expires_at, connected_at FROM automations
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Automation
	for rows.Next() {
		var a Automation
		var encrypted, expiresAt, connectedAt string
		if err := rows.Scan(&a.UserID, &a.Username, &encrypted, &expiresAt, &connectedAt); err != nil {
			return nil, err
		}
		plaintext, err := db.box.Open(encrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt app password for user %s: %w", a.UserID, err)
		}
		a.AppPassword = plaintext
		a.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
		a.ConnectedAt, _ = time.Parse(time.RFC3339, connectedAt)
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertTriggerIndexEntry stores or replaces a workflow's trigger index entry. Called
// whenever a workflow with a schedule/event trigger is created or updated.
func (db *DB) UpsertTriggerIndexEntry(ctx context.Context, e TriggerIndexEntry) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO trigger_index (workflow_id, user_id, trigger_type, schedule, event_type)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(workflow_id) DO UPDATE SET
			user_id = excluded.user_id,
			trigger_type = excluded.trigger_type,
			schedule = excluded.schedule,
			event_type = excluded.event_type
	`, e.WorkflowID, e.UserID, e.TriggerType, e.Schedule, e.EventType)
	return err
}

// DeleteTriggerIndexEntry removes a workflow's trigger index entry (called when a workflow
// is deleted, or updated to a manual trigger / disabled).
func (db *DB) DeleteTriggerIndexEntry(ctx context.Context, workflowID string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM trigger_index WHERE workflow_id = ?`, workflowID)
	return err
}

// ListScheduleTriggers returns every indexed workflow with an active schedule trigger.
func (db *DB) ListScheduleTriggers(ctx context.Context) ([]TriggerIndexEntry, error) {
	return db.listTriggers(ctx, "schedule")
}

// ListEventTriggers returns every indexed workflow with an active event trigger.
func (db *DB) ListEventTriggers(ctx context.Context) ([]TriggerIndexEntry, error) {
	return db.listTriggers(ctx, "event")
}

func (db *DB) listTriggers(ctx context.Context, triggerType string) ([]TriggerIndexEntry, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT workflow_id, user_id, trigger_type, schedule, event_type
		FROM trigger_index WHERE trigger_type = ?
	`, triggerType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TriggerIndexEntry
	for rows.Next() {
		var e TriggerIndexEntry
		if err := rows.Scan(&e.WorkflowID, &e.UserID, &e.TriggerType, &e.Schedule, &e.EventType); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
