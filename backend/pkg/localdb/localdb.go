// Package localdb is the sidecar's own local operational state — never user content, never
// synced through any oCIS API. Holds encrypted app-passwords for users who've enabled
// scheduled/event automation, and a small denormalized index of which workflows have those
// triggers enabled (so the scheduler/SSE matcher don't have to scan every user's WebDAV
// space on every tick).
package localdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

// TriggerIndexEntry is a denormalized pointer to a workflow with an active schedule/event/
// webhook trigger.
type TriggerIndexEntry struct {
	WorkflowID  string
	UserID      string
	TriggerType string // schedule | event | webhook
	Schedule    string
	EventType   string
	PathPrefix  string // event trigger filter, mirrors model.EventFilters
	Extension   string // event trigger filter, mirrors model.EventFilters
	// WebhookToken is the bearer credential for the webhook trigger's URL
	// (POST /hooks/{workflowId}/{token}). Empty for non-webhook entries. Encrypted at rest
	// the same way Automation.AppPassword is (see encryptWebhookToken/decryptWebhookToken)
	// — never logged, never returned by any endpoint except the deliberate reveal/rotate
	// actions in pkg/service.
	WebhookToken string
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
	`); err != nil {
		return err
	}

	// CREATE TABLE IF NOT EXISTS only handles brand-new databases — a trigger_index table
	// created before path_prefix/extension/webhook_token existed needs these added
	// explicitly. webhook_token's default of plaintext '' is deliberate (see
	// decryptWebhookToken): it means "no token", not "empty ciphertext".
	for _, col := range []string{"path_prefix", "extension", "webhook_token"} {
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
// whenever a workflow with a schedule/event/webhook trigger is created or updated.
func (db *DB) UpsertTriggerIndexEntry(ctx context.Context, e TriggerIndexEntry) error {
	encryptedToken, err := db.encryptWebhookToken(e.WebhookToken)
	if err != nil {
		return fmt.Errorf("encrypt webhook token: %w", err)
	}
	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO trigger_index (workflow_id, user_id, trigger_type, schedule, event_type, path_prefix, extension, webhook_token)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workflow_id) DO UPDATE SET
			user_id = excluded.user_id,
			trigger_type = excluded.trigger_type,
			schedule = excluded.schedule,
			event_type = excluded.event_type,
			path_prefix = excluded.path_prefix,
			extension = excluded.extension,
			webhook_token = excluded.webhook_token
	`, e.WorkflowID, e.UserID, e.TriggerType, e.Schedule, e.EventType, e.PathPrefix, e.Extension, encryptedToken)
	return err
}

// GetTriggerIndexEntry returns the single trigger index entry for workflowID, used by the
// webhook route to look up and verify a caller-supplied token without listing every trigger
// in the database on every request.
func (db *DB) GetTriggerIndexEntry(ctx context.Context, workflowID string) (*TriggerIndexEntry, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT workflow_id, user_id, trigger_type, schedule, event_type, path_prefix, extension, webhook_token
		FROM trigger_index WHERE workflow_id = ?
	`, workflowID)

	var e TriggerIndexEntry
	var encryptedToken string
	if err := row.Scan(&e.WorkflowID, &e.UserID, &e.TriggerType, &e.Schedule, &e.EventType, &e.PathPrefix, &e.Extension, &encryptedToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	token, err := db.decryptWebhookToken(encryptedToken)
	if err != nil {
		return nil, fmt.Errorf("decrypt webhook token: %w", err)
	}
	e.WebhookToken = token
	return &e, nil
}

// encryptWebhookToken seals a webhook token with the same secretbox used for app-passwords.
// An empty token (every non-webhook trigger, and any webhook trigger whose token hasn't
// been generated yet) is stored as plaintext "" rather than a sealed empty string, so
// pre-existing rows added via ALTER TABLE ... DEFAULT ” (see migrate) don't need a
// backfill and decryptWebhookToken doesn't need to distinguish "" from a real ciphertext.
func (db *DB) encryptWebhookToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	return db.box.Seal(token)
}

// decryptWebhookToken is encryptWebhookToken's inverse. See its comment for why "" is
// special-cased rather than run through Open.
func (db *DB) decryptWebhookToken(encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}
	return db.box.Open(encrypted)
}

// NewWebhookToken generates a fresh random webhook trigger token: 32 bytes of crypto/rand,
// hex-encoded (64 chars) — comparable entropy to a typical bearer API key. Matched against
// an incoming request's token in constant time by the hooks handler (see pkg/service).
func NewWebhookToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
		SELECT workflow_id, user_id, trigger_type, schedule, event_type, path_prefix, extension, webhook_token
		FROM trigger_index WHERE trigger_type = ?
	`, triggerType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TriggerIndexEntry
	for rows.Next() {
		var e TriggerIndexEntry
		var encryptedToken string
		if err := rows.Scan(&e.WorkflowID, &e.UserID, &e.TriggerType, &e.Schedule, &e.EventType, &e.PathPrefix, &e.Extension, &encryptedToken); err != nil {
			return nil, err
		}
		token, err := db.decryptWebhookToken(encryptedToken)
		if err != nil {
			return nil, fmt.Errorf("decrypt webhook token for workflow %s: %w", e.WorkflowID, err)
		}
		e.WebhookToken = token
		out = append(out, e)
	}
	return out, rows.Err()
}
