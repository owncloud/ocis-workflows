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
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers as "sqlite"

	"github.com/owncloud/ocis-workflows/pkg/secretbox"
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
	PathPrefix  string // event trigger filter, mirrors model.EventFilters
	Extension   string // event trigger filter, mirrors model.EventFilters
	SpaceID     string // event trigger filter, mirrors model.EventFilters
}

// MatchesFilters reports whether e's path-prefix, extension, and space filters (any of
// which may be unset, meaning "no restriction") admit an event at the given resolved
// WebDAV path and originating space id. Does not check UserID or EventType — callers
// filter on those first, since it's cheaper than resolving a path.
func (e TriggerIndexEntry) MatchesFilters(path, spaceID string) bool {
	if e.PathPrefix != "" && !strings.HasPrefix(path, e.PathPrefix) {
		return false
	}
	if e.Extension != "" && !strings.HasSuffix(path, e.Extension) {
		return false
	}
	if e.SpaceID != "" && e.SpaceID != spaceID {
		return false
	}
	return true
}

// EventCursor tracks the last time this backend successfully reconciled a user's drive
// against oCIS's activitylog, closing any gap the SSE connection may have missed while it
// was down. LastStatus records whether that reconciliation attempt actually succeeded
// ("full") or the activitylog query itself failed ("sse-only") — surfaced to the user via
// GET /me/automation's reliability field.
type EventCursor struct {
	UserID      string
	DriveID     string
	LastChecked time.Time
	LastStatus  string // "full" | "sse-only"
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
	if _, err := db.sql.Exec(`
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
		CREATE TABLE IF NOT EXISTS event_cursors (
			user_id TEXT NOT NULL,
			drive_id TEXT NOT NULL,
			last_checked TEXT NOT NULL,
			last_status TEXT NOT NULL DEFAULT 'full',
			PRIMARY KEY (user_id, drive_id)
		);
	`); err != nil {
		return err
	}

	// CREATE TABLE IF NOT EXISTS only handles brand-new databases — a trigger_index table
	// created before path_prefix/extension/space_id existed needs these added explicitly.
	for _, col := range []string{"path_prefix", "extension", "space_id"} {
		if err := db.addColumnIfMissing("trigger_index", col, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) addColumnIfMissing(table, column, definition string) error {
	rows, err := db.sql.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.sql.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
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
// and the SSE consumer manager).
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
		INSERT INTO trigger_index (workflow_id, user_id, trigger_type, schedule, event_type, path_prefix, extension, space_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workflow_id) DO UPDATE SET
			user_id = excluded.user_id,
			trigger_type = excluded.trigger_type,
			schedule = excluded.schedule,
			event_type = excluded.event_type,
			path_prefix = excluded.path_prefix,
			extension = excluded.extension,
			space_id = excluded.space_id
	`, e.WorkflowID, e.UserID, e.TriggerType, e.Schedule, e.EventType, e.PathPrefix, e.Extension, e.SpaceID)
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
		SELECT workflow_id, user_id, trigger_type, schedule, event_type, path_prefix, extension, space_id
		FROM trigger_index WHERE trigger_type = ?
	`, triggerType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TriggerIndexEntry
	for rows.Next() {
		var e TriggerIndexEntry
		if err := rows.Scan(&e.WorkflowID, &e.UserID, &e.TriggerType, &e.Schedule, &e.EventType, &e.PathPrefix, &e.Extension, &e.SpaceID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEventCursor returns the stored cursor for (userID, driveID), or ErrNotFound if this
// pair has never been reconciled.
func (db *DB) GetEventCursor(ctx context.Context, userID, driveID string) (*EventCursor, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT user_id, drive_id, last_checked, last_status
		FROM event_cursors WHERE user_id = ? AND drive_id = ?
	`, userID, driveID)

	var c EventCursor
	var lastChecked string
	if err := row.Scan(&c.UserID, &c.DriveID, &lastChecked, &c.LastStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.LastChecked, _ = time.Parse(time.RFC3339, lastChecked)
	return &c, nil
}

// UpsertEventCursor stores or replaces the cursor for (c.UserID, c.DriveID).
func (db *DB) UpsertEventCursor(ctx context.Context, c EventCursor) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO event_cursors (user_id, drive_id, last_checked, last_status)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, drive_id) DO UPDATE SET
			last_checked = excluded.last_checked,
			last_status = excluded.last_status
	`, c.UserID, c.DriveID, c.LastChecked.UTC().Format(time.RFC3339), c.LastStatus)
	return err
}

// DeleteEventCursors removes every stored event cursor for userID (mirrors DeleteAutomation).
// Called when a user disconnects automation, so a drive that was marked "sse-only" doesn't
// keep reporting degraded reliability forever with no way to clear it, and so cursor rows
// for a user who's disconnected don't just accumulate indefinitely.
func (db *DB) DeleteEventCursors(ctx context.Context, userID string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM event_cursors WHERE user_id = ?`, userID)
	return err
}

// GetReliability reports "sse-only" if userID has any event-cursor row currently marked
// degraded, "full" otherwise (including when userID has no cursor rows at all — nothing
// has been found unreliable yet).
func (db *DB) GetReliability(ctx context.Context, userID string) (string, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT 1 FROM event_cursors WHERE user_id = ? AND last_status = 'sse-only' LIMIT 1
	`, userID)

	var found int
	err := row.Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return "full", nil
	}
	if err != nil {
		return "", err
	}
	return "sse-only", nil
}
