package automation

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
)

func testDB(t *testing.T) *localdb.DB {
	t.Helper()
	db, err := localdb.Open(filepath.Join(t.TempDir(), "test.db"), make([]byte, 32))
	if err != nil {
		t.Fatalf("localdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type fakeGraphClient struct {
	mintCalls  []string // authHeader values MintAppPassword was called with
	mintToken  string
	mintExpiry time.Time
	mintErr    error

	revokeCalls []string // old-password values RevokeAppPassword was called with
}

func (f *fakeGraphClient) Me(context.Context, string) (string, error)       { return "", nil }
func (f *fakeGraphClient) Username(context.Context, string) (string, error) { return "", nil }

func (f *fakeGraphClient) MintAppPassword(_ context.Context, authHeader string, _ time.Duration, _ string) (string, time.Time, error) {
	f.mintCalls = append(f.mintCalls, authHeader)
	if f.mintErr != nil {
		return "", time.Time{}, f.mintErr
	}
	return f.mintToken, f.mintExpiry, nil
}

func (f *fakeGraphClient) RevokeAppPassword(_ context.Context, _, token string) error {
	f.revokeCalls = append(f.revokeCalls, token)
	return nil
}

func TestRenewDueRenewsAutomationNearingExpiry(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID:      "user-1",
		Username:    "admin",
		AppPassword: "old-password",
		ExpiresAt:   time.Now().Add(10 * 24 * time.Hour), // within the 14-day renewal window
		ConnectedAt: time.Now().Add(-80 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}

	newExpiry := time.Now().Add(defaultExpiry).Truncate(time.Second)
	graph := &fakeGraphClient{mintToken: "new-password", mintExpiry: newExpiry}
	svc := New(graph, db, nil, discardLogger())

	svc.renewDue(ctx)

	if len(graph.mintCalls) != 1 {
		t.Fatalf("expected 1 MintAppPassword call, got %d", len(graph.mintCalls))
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:old-password"))
	if graph.mintCalls[0] != wantAuth {
		t.Fatalf("MintAppPassword authHeader = %q, want %q", graph.mintCalls[0], wantAuth)
	}

	got, err := db.GetAutomation(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetAutomation: %v", err)
	}
	if got.AppPassword != "new-password" {
		t.Fatalf("AppPassword after renewal = %q, want %q", got.AppPassword, "new-password")
	}
	if !got.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("ExpiresAt after renewal = %v, want %v", got.ExpiresAt, newExpiry)
	}
	if len(graph.revokeCalls) != 1 || graph.revokeCalls[0] != "old-password" {
		t.Fatalf("expected RevokeAppPassword to be called with the old password, got %v", graph.revokeCalls)
	}
}

func TestRenewDueSkipsAutomationNotNearingExpiry(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	if err := db.UpsertAutomation(ctx, localdb.Automation{
		UserID:      "user-1",
		Username:    "admin",
		AppPassword: "still-fresh",
		ExpiresAt:   time.Now().Add(60 * 24 * time.Hour), // well outside the 14-day window
		ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertAutomation: %v", err)
	}

	graph := &fakeGraphClient{}
	svc := New(graph, db, nil, discardLogger())

	svc.renewDue(ctx)

	if len(graph.mintCalls) != 0 {
		t.Fatalf("expected 0 MintAppPassword calls, got %d", len(graph.mintCalls))
	}
	got, err := db.GetAutomation(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetAutomation: %v", err)
	}
	if got.AppPassword != "still-fresh" {
		t.Fatalf("AppPassword changed unexpectedly: %q", got.AppPassword)
	}
}

type selectiveFailGraphClient struct {
	failForUsername string
	mintToken       string
	mintExpiry      time.Time
}

func (f *selectiveFailGraphClient) Me(context.Context, string) (string, error)       { return "", nil }
func (f *selectiveFailGraphClient) Username(context.Context, string) (string, error) { return "", nil }

func (f *selectiveFailGraphClient) MintAppPassword(_ context.Context, authHeader string, _ time.Duration, _ string) (string, time.Time, error) {
	decoded, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
	username := strings.SplitN(string(decoded), ":", 2)[0]
	if username == f.failForUsername {
		return "", time.Time{}, errors.New("simulated mint failure")
	}
	return f.mintToken, f.mintExpiry, nil
}

func (f *selectiveFailGraphClient) RevokeAppPassword(context.Context, string, string) error { return nil }

func TestRenewDueContinuesPastAFailedRenewal(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()

	for _, a := range []localdb.Automation{
		{UserID: "user-fails", Username: "admin", AppPassword: "will-fail", ExpiresAt: time.Now().Add(time.Hour), ConnectedAt: time.Now()},
		{UserID: "user-ok", Username: "marie", AppPassword: "will-succeed", ExpiresAt: time.Now().Add(time.Hour), ConnectedAt: time.Now()},
	} {
		if err := db.UpsertAutomation(ctx, a); err != nil {
			t.Fatalf("UpsertAutomation(%s): %v", a.UserID, err)
		}
	}

	newExpiry := time.Now().Add(defaultExpiry).Truncate(time.Second)
	graph := &selectiveFailGraphClient{failForUsername: "admin", mintToken: "renewed", mintExpiry: newExpiry}
	svc := New(graph, db, nil, discardLogger())

	svc.renewDue(ctx) // must not panic or stop early

	failed, err := db.GetAutomation(ctx, "user-fails")
	if err != nil {
		t.Fatalf("GetAutomation(user-fails): %v", err)
	}
	if failed.AppPassword != "will-fail" {
		t.Fatalf("expected user-fails' password to be left untouched, got %q", failed.AppPassword)
	}

	ok, err := db.GetAutomation(ctx, "user-ok")
	if err != nil {
		t.Fatalf("GetAutomation(user-ok): %v", err)
	}
	if ok.AppPassword != "renewed" {
		t.Fatalf("expected user-ok to be renewed, got %q", ok.AppPassword)
	}
}
