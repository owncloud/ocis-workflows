package service

import (
	"context"
	"log/slog"
	"testing"

	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/model"
)

type fakeUserResolver struct{ userID string }

func (f *fakeUserResolver) Me(context.Context, string) (string, error) { return f.userID, nil }

type fakeTriggerIndexer struct {
	entries map[string]localdb.TriggerIndexEntry
	deletes int
}

func newFakeTriggerIndexer() *fakeTriggerIndexer {
	return &fakeTriggerIndexer{entries: map[string]localdb.TriggerIndexEntry{}}
}

func (f *fakeTriggerIndexer) UpsertTriggerIndexEntry(_ context.Context, e localdb.TriggerIndexEntry) error {
	f.entries[e.WorkflowID] = e
	return nil
}
func (f *fakeTriggerIndexer) DeleteTriggerIndexEntry(_ context.Context, workflowID string) error {
	delete(f.entries, workflowID)
	f.deletes++
	return nil
}
func (f *fakeTriggerIndexer) GetTriggerIndexEntry(_ context.Context, workflowID string) (*localdb.TriggerIndexEntry, error) {
	e, ok := f.entries[workflowID]
	if !ok {
		return nil, localdb.ErrNotFound
	}
	return &e, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestSyncTriggerIndexGeneratesWebhookTokenOnFirstSave(t *testing.T) {
	idx := newFakeTriggerIndexer()
	h := NewWorkflowsHandler(nil, nil, &fakeUserResolver{userID: "user-1"}, idx, discardLogger())

	wf := model.WorkflowDefinition{ID: "wf-1", Enabled: true, Trigger: model.WorkflowTrigger{Type: "webhook"}}
	h.syncTriggerIndex(t.Context(), "Bearer x", wf)

	entry, err := idx.GetTriggerIndexEntry(t.Context(), "wf-1")
	if err != nil {
		t.Fatalf("GetTriggerIndexEntry: %v", err)
	}
	if entry.TriggerType != "webhook" {
		t.Fatalf("TriggerType = %q, want webhook", entry.TriggerType)
	}
	if entry.WebhookToken == "" {
		t.Fatal("expected a webhook token to be generated on first save")
	}
	if entry.UserID != "user-1" {
		t.Fatalf("UserID = %q, want user-1", entry.UserID)
	}
}

func TestSyncTriggerIndexPreservesWebhookTokenAcrossUpdates(t *testing.T) {
	idx := newFakeTriggerIndexer()
	h := NewWorkflowsHandler(nil, nil, &fakeUserResolver{userID: "user-1"}, idx, discardLogger())

	wf := model.WorkflowDefinition{ID: "wf-1", Enabled: true, Trigger: model.WorkflowTrigger{Type: "webhook"}}
	h.syncTriggerIndex(t.Context(), "Bearer x", wf)
	first, _ := idx.GetTriggerIndexEntry(t.Context(), "wf-1")

	// A later save (e.g. renaming the workflow, or just re-patching it) must not silently
	// mint a new token — that would break whatever external caller already configured the
	// old URL, with no user action requesting a rotation.
	h.syncTriggerIndex(t.Context(), "Bearer x", wf)
	second, _ := idx.GetTriggerIndexEntry(t.Context(), "wf-1")

	if first.WebhookToken != second.WebhookToken {
		t.Fatalf("webhook token changed across saves: %q -> %q", first.WebhookToken, second.WebhookToken)
	}
}

func TestSyncTriggerIndexKeepsWebhookTokenWhileDisabled(t *testing.T) {
	idx := newFakeTriggerIndexer()
	h := NewWorkflowsHandler(nil, nil, &fakeUserResolver{userID: "user-1"}, idx, discardLogger())

	enabled := model.WorkflowDefinition{ID: "wf-1", Enabled: true, Trigger: model.WorkflowTrigger{Type: "webhook"}}
	h.syncTriggerIndex(t.Context(), "Bearer x", enabled)
	before, err := idx.GetTriggerIndexEntry(t.Context(), "wf-1")
	if err != nil {
		t.Fatalf("GetTriggerIndexEntry: %v", err)
	}

	// Unlike schedule/event triggers, toggling a webhook trigger's workflow off must not
	// delete its index entry — the entry is the token/URL's identity, and the hooks
	// handler itself (not the index) is what refuses to actually run a disabled workflow.
	disabled := enabled
	disabled.Enabled = false
	h.syncTriggerIndex(t.Context(), "Bearer x", disabled)

	after, err := idx.GetTriggerIndexEntry(t.Context(), "wf-1")
	if err != nil {
		t.Fatalf("expected webhook trigger index entry to survive being disabled, got: %v", err)
	}
	if after.WebhookToken != before.WebhookToken {
		t.Fatalf("webhook token changed after disabling: %q -> %q", before.WebhookToken, after.WebhookToken)
	}
}

func TestSyncTriggerIndexDeletesEntryWhenTriggerTypeChangesAway(t *testing.T) {
	idx := newFakeTriggerIndexer()
	h := NewWorkflowsHandler(nil, nil, &fakeUserResolver{userID: "user-1"}, idx, discardLogger())

	webhook := model.WorkflowDefinition{ID: "wf-1", Enabled: true, Trigger: model.WorkflowTrigger{Type: "webhook"}}
	h.syncTriggerIndex(t.Context(), "Bearer x", webhook)

	manual := webhook
	manual.Trigger = model.WorkflowTrigger{Type: "manual"}
	h.syncTriggerIndex(t.Context(), "Bearer x", manual)

	if _, err := idx.GetTriggerIndexEntry(t.Context(), "wf-1"); err != localdb.ErrNotFound {
		t.Fatalf("expected trigger index entry removed after switching away from webhook, got err=%v", err)
	}
}

func TestSyncTriggerIndexScheduleStillDeletedWhenDisabled(t *testing.T) {
	idx := newFakeTriggerIndexer()
	h := NewWorkflowsHandler(nil, nil, &fakeUserResolver{userID: "user-1"}, idx, discardLogger())

	wf := model.WorkflowDefinition{ID: "wf-1", Enabled: true, Trigger: model.WorkflowTrigger{Type: "schedule", Schedule: "0 * * * *"}}
	h.syncTriggerIndex(t.Context(), "Bearer x", wf)
	if _, err := idx.GetTriggerIndexEntry(t.Context(), "wf-1"); err != nil {
		t.Fatalf("expected schedule entry present while enabled: %v", err)
	}

	wf.Enabled = false
	h.syncTriggerIndex(t.Context(), "Bearer x", wf)
	if _, err := idx.GetTriggerIndexEntry(t.Context(), "wf-1"); err != localdb.ErrNotFound {
		t.Fatalf("expected schedule entry removed once disabled (pre-existing behavior), got err=%v", err)
	}
}
