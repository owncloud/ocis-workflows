# Automation Connect UX Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the manual "Connect automation" button from the Workflows app and replace it with silent connect-on-activation, a backend self-renewal job that never depends on a live user session, and a quieter status/manage affordance.

**Architecture:** Backend gets a new periodic self-renewal loop on `automation.Service` that mints a replacement app-password using Basic auth built from the stored credential itself (no live bearer token), wired into the same errgroup that already runs the scheduler and SSE manager in `cmd/workflows`'s server command. Frontend gets a small `useAutomationConnect` composable (silent connect + one-time toast) shared by `WorkflowBuilder.vue` (connect on activating a schedule/event workflow) and `WorkflowList.vue` (self-heal on mount + a status line replacing the old pill/button), plus a new `AutomationPanel.vue` side panel for status/expiry/disconnect.

**Tech Stack:** Go 1.25 backend (chi router, `modernc.org/sqlite`), Vue 3 + `<script setup>` + TypeScript frontend using `@ownclouders/web-pkg` (auth store, `useMessages` toast store), vitest for unit tests, Playwright for e2e.

## Global Constraints

- Go module: `github.com/owncloud/ocis-workflows`, Go 1.25.11. From the `backend/` directory: `go build ./...`, `go vet ./...`, `go test ./...`.
- Frontend package manager is `pnpm`, run from the `frontend/` directory. Unit tests: `pnpm exec vitest run <pattern>`. E2e: `pnpm test:e2e <file>`.
- App-password label is always `"workflows"` (`automation.tokenLabel`); default expiry is 90 days (`automation.defaultExpiry`). The renewal window is 14 days before expiry — do not change either value as part of this work.
- The scheduler already authenticates as an automation owner via `"Basic " + base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", automation.Username, automation.AppPassword))` (`backend/pkg/scheduler/scheduler.go:136`) — the renewal job must build its auth header the same way, not introduce a second convention.
- Revoking an old app-password after minting a replacement is best-effort: log a warning and continue on failure, exactly like the existing `Disconnect` method (`backend/pkg/automation/automation.go:112`). A revoke failure must never undo an otherwise-successful renewal.
- This codebase has no existing use of `vue3-gettext`'s pluralization/interpolation helpers (`$ngettext`, `%{}` placeholders) — do not introduce them. Build any count-dependent string with plain `$gettext(...)` calls and JS string concatenation, matching every existing call site.
- Only `variation="primary"` is used anywhere on `oc-button` in this codebase today — do not introduce unverified variation values (e.g. `"danger"`). Convey severity through text/color classes (`.oc-text-input-danger` is the existing convention), not unverified button props.
- Views/composables in this repo split their test coverage as: composables get vitest unit tests (see `frontend/tests/unit/useWorkflowsApi.spec.ts`), views/components get Playwright e2e coverage only (no `@vue/test-utils` component-mount tests exist anywhere yet) — follow that split, don't introduce a new testing pattern.

---

## Task 1: Backend self-renewal job

**Files:**
- Create: `backend/pkg/automation/renew.go`
- Test: `backend/pkg/automation/renew_test.go`

**Interfaces:**
- Consumes: `automation.Service` (`backend/pkg/automation/automation.go` — fields `graph GraphClient`, `db *localdb.DB`, `log *slog.Logger`; consts `defaultExpiry`, `tokenLabel`), `localdb.Automation` / `(*localdb.DB).ListAutomations` / `(*localdb.DB).UpsertAutomation` (`backend/pkg/localdb/localdb.go`), `GraphClient.MintAppPassword` / `GraphClient.RevokeAppPassword` (`backend/pkg/automation/automation.go:24-29`).
- Produces: `(*automation.Service).StartRenewalLoop(ctx context.Context, interval time.Duration)` — consumed by Task 2.

- [ ] **Step 1: Write the failing tests**

Create `backend/pkg/automation/renew_test.go`:

```go
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
	svc := New(graph, db, discardLogger())

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
	svc := New(graph, db, discardLogger())

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
	svc := New(graph, db, discardLogger())

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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./pkg/automation/... -v`
Expected: build failure — `svc.renewDue undefined (type *Service has no field or method renewDue)` (and `New` / `defaultExpiry` resolve fine since those already exist in `automation.go`).

- [ ] **Step 3: Write the implementation**

Create `backend/pkg/automation/renew.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./pkg/automation/... -v`
Expected: `PASS` for `TestRenewDueRenewsAutomationNearingExpiry`, `TestRenewDueSkipsAutomationNotNearingExpiry`, `TestRenewDueContinuesPastAFailedRenewal`.

Also run: `cd backend && go vet ./pkg/automation/...`
Expected: no output (clean).

- [ ] **Step 5: Commit**

```bash
git add backend/pkg/automation/renew.go backend/pkg/automation/renew_test.go
git commit -m "feat(automation): add backend self-renewal job for app-passwords"
```

---

## Task 2: Wire the renewal loop into the server command

**Files:**
- Modify: `backend/pkg/command/server.go`

**Interfaces:**
- Consumes: `(*automation.Service).StartRenewalLoop(ctx context.Context, interval time.Duration)` from Task 1; existing `automationService` variable already constructed in `RunServer` (`automationService := automation.New(ocisClient, db, log)`).
- Produces: nothing new consumed by later tasks — this is the last backend task.

- [ ] **Step 1: Add the renewal tick interval constant**

In `backend/pkg/command/server.go`, change:

```go
// scheduleTickInterval controls how often the scheduler checks for due schedule triggers.
const scheduleTickInterval = 10 * time.Second

// sseReconcileInterval controls how often the SSE manager checks which users need an
// active event-trigger consumer.
const sseReconcileInterval = 30 * time.Second
```

to:

```go
// scheduleTickInterval controls how often the scheduler checks for due schedule triggers.
const scheduleTickInterval = 10 * time.Second

// sseReconcileInterval controls how often the SSE manager checks which users need an
// active event-trigger consumer.
const sseReconcileInterval = 30 * time.Second

// renewalTickInterval controls how often the automation service checks for app-passwords
// nearing expiry. Daily is frequent enough given the 14-day renewal window and 90-day
// credential lifetime.
const renewalTickInterval = 24 * time.Hour
```

- [ ] **Step 2: Start the renewal loop alongside the scheduler and SSE manager**

Change:

```go
	g.Go(func() error {
		log.Info("starting sse event-trigger manager", "reconcileInterval", sseReconcileInterval)
		sseManager.Start(gCtx)
		return nil
	})

	g.Go(func() error {
		<-gCtx.Done()
```

to:

```go
	g.Go(func() error {
		log.Info("starting sse event-trigger manager", "reconcileInterval", sseReconcileInterval)
		sseManager.Start(gCtx)
		return nil
	})

	g.Go(func() error {
		log.Info("starting automation renewal loop", "interval", renewalTickInterval)
		automationService.StartRenewalLoop(gCtx, renewalTickInterval)
		return nil
	})

	g.Go(func() error {
		<-gCtx.Done()
```

- [ ] **Step 3: Verify the backend builds and all tests still pass**

Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: build succeeds, vet is clean, all tests (including Task 1's new ones) pass.

- [ ] **Step 4: Manually confirm the loop starts (optional but recommended)**

```bash
docker compose up -d --build workflows-backend
sleep 5
docker compose logs workflows-backend --since 1m | grep "starting automation renewal loop"
```

Expected output includes a line like:
```
workflows-backend-1  | ... msg="starting automation renewal loop" interval=24h0m0s
```

- [ ] **Step 5: Commit**

```bash
git add backend/pkg/command/server.go
git commit -m "feat(automation): start the self-renewal loop from the server command"
```

---

## Task 3: `useAutomationConnect` composable

**Files:**
- Create: `frontend/src/composables/useAutomationConnect.ts`
- Test: `frontend/tests/unit/useAutomationConnect.spec.ts`

**Interfaces:**
- Consumes: `AutomationStatus` type (`frontend/src/types/workflow.ts:99-102`), `useMessages` from `@ownclouders/web-pkg` (`showMessage(data: Omit<Message, 'id'>)`), `useGettext` from `vue3-gettext`.
- Produces: `useAutomationConnect(api: { connectAutomation: () => Promise<AutomationStatus> }): { connectWithNotice: () => Promise<AutomationStatus> }` — consumed by Task 5 (`WorkflowList.vue`) and Task 6 (`WorkflowBuilder.vue`).

- [ ] **Step 1: Write the failing test**

Create `frontend/tests/unit/useAutomationConnect.spec.ts`:

```ts
import { describe, expect, it, vi } from 'vitest'

const showMessage = vi.fn()
vi.mock('@ownclouders/web-pkg', () => ({
  useMessages: () => ({ showMessage })
}))
vi.mock('vue3-gettext', () => ({
  useGettext: () => ({ $gettext: (msg: string) => msg })
}))

import { useAutomationConnect } from '../../src/composables/useAutomationConnect'

describe('useAutomationConnect', () => {
  it('connects and shows a one-time toast', async () => {
    const connectAutomation = vi.fn().mockResolvedValue({ connected: true, expirationDateTime: '2026-10-01T00:00:00Z' })
    const { connectWithNotice } = useAutomationConnect({ connectAutomation })

    const status = await connectWithNotice()

    expect(status).toEqual({ connected: true, expirationDateTime: '2026-10-01T00:00:00Z' })
    expect(connectAutomation).toHaveBeenCalledOnce()
    expect(showMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        title: 'Background execution enabled for your account',
        status: 'success'
      })
    )
  })

  it('propagates a failed connect without showing a toast', async () => {
    showMessage.mockClear()
    const connectAutomation = vi.fn().mockRejectedValue(new Error('boom'))
    const { connectWithNotice } = useAutomationConnect({ connectAutomation })

    await expect(connectWithNotice()).rejects.toThrow('boom')
    expect(showMessage).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && pnpm exec vitest run useAutomationConnect`
Expected: FAIL — `Cannot find module '../../src/composables/useAutomationConnect'`.

- [ ] **Step 3: Write the implementation**

Create `frontend/src/composables/useAutomationConnect.ts`:

```ts
import { useGettext } from 'vue3-gettext'
import { useMessages } from '@ownclouders/web-pkg'
import type { AutomationStatus } from '../types/workflow'

interface AutomationApi {
  connectAutomation: () => Promise<AutomationStatus>
}

/** Silently connects background automation and shows a one-time toast for the transition.
 *  Callers are responsible for checking whether automation is already connected before
 *  calling this — it always connects unconditionally. */
export function useAutomationConnect(api: AutomationApi) {
  const { $gettext } = useGettext()
  const { showMessage } = useMessages()

  const connectWithNotice = async (): Promise<AutomationStatus> => {
    const status = await api.connectAutomation()
    showMessage({
      title: $gettext('Background execution enabled for your account'),
      desc: $gettext('This workflow will keep running even when you are signed out.'),
      status: 'success'
    })
    return status
  }

  return { connectWithNotice }
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd frontend && pnpm exec vitest run useAutomationConnect`
Expected: `PASS` — both tests green.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/composables/useAutomationConnect.ts frontend/tests/unit/useAutomationConnect.spec.ts
git commit -m "feat(frontend): add useAutomationConnect composable"
```

---

## Task 4: `AutomationPanel.vue` component

**Files:**
- Create: `frontend/src/components/AutomationPanel.vue`

**Interfaces:**
- Consumes: `useWorkflowsApi` (`frontend/src/composables/useWorkflowsApi.ts` — `disconnectAutomation()`).
- Produces: `<AutomationPanel :backend-url="string" :automated-workflow-count="number" :expiration-date-time="string | undefined" @close @disconnected />` — consumed by Task 5 (`WorkflowList.vue`).

- [ ] **Step 1: Write the component**

Create `frontend/src/components/AutomationPanel.vue`:

```vue
<template>
  <!-- eslint-disable-next-line vuejs-accessibility/click-events-have-key-events, vuejs-accessibility/no-static-element-interactions -->
  <div class="workflows-automation-overlay" @click.self="$emit('close')" @keydown.esc="$emit('close')">
    <div class="workflows-automation-panel" role="dialog" aria-modal="true" :aria-label="$gettext('Background execution')">
      <div class="workflows-automation-panel-header">
        <h2>{{ $gettext('Background execution') }}</h2>
        <oc-button appearance="raw" :aria-label="$gettext('Close')" @click="$emit('close')">
          <oc-icon name="close" fill-type="line" />
        </oc-button>
      </div>

      <p>
        {{
          $gettext(
            'This account has a background credential that lets scheduled and file-event workflows run even when you are signed out.'
          )
        }}
      </p>
      <p v-if="expirationDateTime">{{ $gettext('Renews automatically. Current expiry:') }} {{ formatDate(expirationDateTime) }}</p>

      <p v-if="disconnectError" class="oc-text-input-danger">{{ disconnectError }}</p>

      <template v-if="!confirming">
        <oc-button appearance="outline" :disabled="disconnecting" @click="onDisconnectClick">
          {{ $gettext('Disconnect') }}
        </oc-button>
      </template>
      <template v-else>
        <p class="oc-text-input-danger">{{ disconnectWarning }}</p>
        <div class="workflows-automation-panel-actions">
          <oc-button appearance="outline" :disabled="disconnecting" @click="confirming = false">
            {{ $gettext('Cancel') }}
          </oc-button>
          <oc-button appearance="outline" :disabled="disconnecting" @click="disconnect">
            {{ $gettext('Yes, disconnect') }}
          </oc-button>
        </div>
      </template>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { useWorkflowsApi } from '../composables/useWorkflowsApi'

const props = defineProps<{
  backendUrl: string
  automatedWorkflowCount: number
  expirationDateTime?: string
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'disconnected'): void }>()

const { $gettext } = useGettext()
const api = useWorkflowsApi(props.backendUrl)

const confirming = ref(false)
const disconnecting = ref(false)
const disconnectError = ref('')

const disconnectWarning = computed(() =>
  props.automatedWorkflowCount === 1
    ? $gettext('1 workflow will stop running in the background.')
    : `${props.automatedWorkflowCount} ${$gettext('workflows will stop running in the background.')}`
)

const onDisconnectClick = () => {
  if (props.automatedWorkflowCount > 0) {
    confirming.value = true
    return
  }
  disconnect()
}

const disconnect = async () => {
  disconnecting.value = true
  disconnectError.value = ''
  try {
    await api.disconnectAutomation()
    emit('disconnected')
  } catch (e) {
    disconnectError.value = e instanceof Error ? e.message : String(e)
  } finally {
    disconnecting.value = false
  }
}

const formatDate = (iso: string) => new Date(iso).toLocaleString()
</script>

<style scoped>
.workflows-automation-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.35);
  display: flex;
  justify-content: flex-end;
  z-index: 100;
}
.workflows-automation-panel {
  width: 420px;
  max-width: 100%;
  height: 100%;
  background: var(--oc-color-swatch-brand-contrastText, #fff);
  box-shadow: -2px 0 12px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 1rem;
  overflow-y: auto;
}
.workflows-automation-panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.workflows-automation-panel-actions {
  display: flex;
  gap: 0.5rem;
}
</style>
```

- [ ] **Step 2: Verify types and lint are clean**

Run: `cd frontend && pnpm check:types && pnpm lint`
Expected: no errors (a pre-existing warning count unrelated to this file is fine; there must be zero errors/warnings pointing at `AutomationPanel.vue`).

- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/AutomationPanel.vue
git commit -m "feat(frontend): add AutomationPanel status/disconnect component"
```

---

## Task 5: Rewrite `WorkflowList.vue`

**Files:**
- Modify: `frontend/src/views/WorkflowList.vue`

**Interfaces:**
- Consumes: `useAutomationConnect` (Task 3), `AutomationPanel.vue` (Task 4), existing `useWorkflowsApi` (`getAutomationStatus`, `connectAutomation`, `listWorkflows`, `deleteWorkflow`).
- Produces: the status line text (`"Background execution active"`, `"Background execution off"`) and the `"manage"` button — consumed by Task 7's e2e test.

- [ ] **Step 1: Replace the whole file**

Replace the full contents of `frontend/src/views/WorkflowList.vue` with:

```vue
<template>
  <main class="oc-p workflows-list">
    <div class="workflows-list-header">
      <h1>{{ $gettext('Workflows') }}</h1>
      <div class="workflows-list-header-actions">
        <span v-if="automationConnected" class="workflows-automation-status">
          {{ $gettext('Background execution active') }}
          <button type="button" class="workflows-automation-manage-link" @click="automationPanelOpen = true">
            {{ $gettext('manage') }}
          </button>
        </span>
        <span v-else-if="hasAutomatedWorkflows" class="workflows-automation-status is-inactive">
          {{ $gettext('Background execution off') }}
        </span>
        <oc-button variation="primary" @click="createNew">
          {{ $gettext('Add workflow') }}
        </oc-button>
      </div>
    </div>
    <p v-if="automationError" class="oc-text-input-danger">{{ automationError }}</p>

    <p v-if="loadError" class="oc-text-input-danger">{{ loadError }}</p>
    <p v-else-if="loading">{{ $gettext('Loading workflows...') }}</p>
    <p v-else-if="!workflows.length" class="workflows-list-empty">
      {{ $gettext('No workflows yet. Add one to get started.') }}
    </p>

    <table v-else class="workflows-list-table">
      <thead>
        <tr>
          <th>{{ $gettext('Name') }}</th>
          <th>{{ $gettext('Trigger') }}</th>
          <th>{{ $gettext('Status') }}</th>
          <th>{{ $gettext('Last updated') }}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <tr v-for="workflow in workflows" :key="workflow.id">
          <td>
            <a :href="builderPath(workflow.id)">{{ workflow.name }}</a>
          </td>
          <td>{{ workflow.trigger.type }}</td>
          <td>
            <span class="workflows-status-pill" :class="workflow.enabled ? 'is-active' : 'is-inactive'">
              {{ workflow.enabled ? $gettext('Active') : $gettext('Inactive') }}
            </span>
          </td>
          <td>{{ formatDate(workflow.lastModifiedDateTime) }}</td>
          <td>
            <oc-button appearance="raw" @click="remove(workflow.id)">
              {{ $gettext('Delete') }}
            </oc-button>
          </td>
        </tr>
      </tbody>
    </table>

    <AutomationPanel
      v-if="automationPanelOpen"
      :backend-url="appConfig.backendUrl"
      :automated-workflow-count="automatedWorkflowCount"
      :expiration-date-time="automationExpiresAt"
      @close="automationPanelOpen = false"
      @disconnected="onAutomationDisconnected"
    />
  </main>
</template>

<script lang="ts" setup>
import { computed, onMounted, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import { useAppConfig } from '../composables/useAppConfig'
import { useAutomationConnect } from '../composables/useAutomationConnect'
import { builderPath } from '../router'
import AutomationPanel from '../components/AutomationPanel.vue'
import type { WorkflowDefinition } from '../types/workflow'

const { $gettext } = useGettext()
const appConfig = useAppConfig()
const api = useWorkflowsApi(appConfig.backendUrl)
const { connectWithNotice } = useAutomationConnect(api)

const workflows = ref<WorkflowDefinition[]>([])
const loading = ref(true)
const loadError = ref('')
const automationConnected = ref(false)
const automationExpiresAt = ref('')
const automationError = ref('')
const automationPanelOpen = ref(false)

const hasAutomatedWorkflows = computed(() =>
  workflows.value.some((w) => w.enabled && (w.trigger.type === 'schedule' || w.trigger.type === 'event'))
)
const automatedWorkflowCount = computed(
  () => workflows.value.filter((w) => w.enabled && (w.trigger.type === 'schedule' || w.trigger.type === 'event')).length
)

const load = async () => {
  loading.value = true
  loadError.value = ''
  try {
    workflows.value = await api.listWorkflows()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const loadAutomationStatus = async () => {
  try {
    const status = await api.getAutomationStatus()
    automationConnected.value = status.connected
    automationExpiresAt.value = status.expirationDateTime ?? ''
  } catch (e) {
    automationError.value = e instanceof Error ? e.message : String(e)
  }
}

// Self-heals installs where a schedule/event workflow is active but automation isn't
// connected (e.g. after a manual disconnect, or a lapsed credential) — silently reconnects
// rather than showing a dead-end "off" state with no way to fix it.
const reconcileAutomation = async () => {
  if (automationConnected.value || !hasAutomatedWorkflows.value) {
    return
  }
  try {
    const status = await connectWithNotice()
    automationConnected.value = status.connected
    automationExpiresAt.value = status.expirationDateTime ?? ''
  } catch (e) {
    automationError.value = e instanceof Error ? e.message : String(e)
  }
}

const onAutomationDisconnected = () => {
  automationConnected.value = false
  automationExpiresAt.value = ''
  automationPanelOpen.value = false
}

const createNew = () => {
  window.location.assign(builderPath('new'))
}

const remove = async (id: string) => {
  await api.deleteWorkflow(id)
  await load()
}

const formatDate = (iso: string) => new Date(iso).toLocaleString()

onMounted(async () => {
  await Promise.all([load(), loadAutomationStatus()])
  await reconcileAutomation()
})
</script>

<style scoped>
.workflows-list-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1.5rem;
}
.workflows-list-header-actions {
  display: flex;
  align-items: center;
  gap: 1rem;
}
.workflows-automation-status {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
  opacity: 0.8;
}
.workflows-automation-status.is-inactive {
  color: #b3261e;
  opacity: 1;
}
.workflows-automation-manage-link {
  border: none;
  background: transparent;
  color: var(--oc-color-swatch-brand-default, #1a5fb4);
  text-decoration: underline;
  cursor: pointer;
  padding: 0;
  font-size: inherit;
}
.workflows-list-empty {
  opacity: 0.7;
}
.workflows-list-table {
  width: 100%;
  border-collapse: collapse;
}
.workflows-list-table th {
  text-align: left;
  font-size: 0.8rem;
  text-transform: uppercase;
  opacity: 0.6;
  padding: 0.5rem;
  border-bottom: 1px solid var(--oc-color-border, #ddd);
}
.workflows-list-table td {
  padding: 0.6rem 0.5rem;
  border-bottom: 1px solid var(--oc-color-border, #eee);
}
.workflows-status-pill {
  display: inline-block;
  padding: 0.15rem 0.6rem;
  border-radius: 999px;
  font-size: 0.75rem;
  font-weight: 600;
}
.workflows-status-pill.is-active {
  background: #e3f5e9;
  color: #1a7f37;
}
.workflows-status-pill.is-inactive {
  background: #f0f0f0;
  color: #666;
}
</style>
```

- [ ] **Step 2: Verify types and lint are clean**

Run: `cd frontend && pnpm check:types && pnpm lint`
Expected: no errors pointing at `WorkflowList.vue` (unused `$gettext` import would be a lint error — confirm it's still used by the template's `$gettext(...)` calls, which it is).

- [ ] **Step 3: Commit**

```bash
git add frontend/src/views/WorkflowList.vue
git commit -m "feat(frontend): replace automation pill/button with status line + manage panel"
```

---

## Task 6: Wire silent connect into `WorkflowBuilder.vue`

**Files:**
- Modify: `frontend/src/views/WorkflowBuilder.vue`

**Interfaces:**
- Consumes: `useAutomationConnect` (Task 3), existing `api.getAutomationStatus()`.
- Produces: the "connect on activation, block save on failure" behavior consumed by Task 7's e2e test.

- [ ] **Step 1: Import the composable and instantiate it**

In `frontend/src/views/WorkflowBuilder.vue`, change:

```ts
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import { useAppConfig } from '../composables/useAppConfig'
```

to:

```ts
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import { useAppConfig } from '../composables/useAppConfig'
import { useAutomationConnect } from '../composables/useAutomationConnect'
```

Then change:

```ts
const appConfig = useAppConfig()
const api = useWorkflowsApi(appConfig.backendUrl)
const { addNodes, addEdges, fitView } = useVueFlow()
```

to:

```ts
const appConfig = useAppConfig()
const api = useWorkflowsApi(appConfig.backendUrl)
const { connectWithNotice } = useAutomationConnect(api)
const { addNodes, addEdges, fitView } = useVueFlow()
```

- [ ] **Step 2: Extract the current trigger type and a "needs automation" check**

Change:

```ts
const triggerPayload = () => {
  const triggerNode = nodes.value.find((n) => n.type === 'trigger')
  const triggerType: TriggerType = triggerNode?.data.triggerType ?? 'manual'
  return {
    type: triggerType,
    schedule: triggerType === 'schedule' ? triggerNode?.data.schedule : undefined,
    event: triggerType === 'event' ? triggerNode?.data.event : undefined
  }
}
```

to:

```ts
const currentTriggerType = (): TriggerType => nodes.value.find((n) => n.type === 'trigger')?.data.triggerType ?? 'manual'

const needsAutomation = () => {
  const triggerType = currentTriggerType()
  return enabled.value && (triggerType === 'schedule' || triggerType === 'event')
}

const triggerPayload = () => {
  const triggerNode = nodes.value.find((n) => n.type === 'trigger')
  const triggerType = currentTriggerType()
  return {
    type: triggerType,
    schedule: triggerType === 'schedule' ? triggerNode?.data.schedule : undefined,
    event: triggerType === 'event' ? triggerNode?.data.event : undefined
  }
}
```

- [ ] **Step 3: Connect (if needed) before saving, blocking the save on failure**

Change:

```ts
const save = async () => {
  saving.value = true
  saveError.value = ''
  try {
    const payload = {
      name: name.value,
      enabled: enabled.value,
      trigger: triggerPayload(),
      graph: { nodes: nodes.value, edges: edges.value }
    }
    if (isNew()) {
```

to:

```ts
const save = async () => {
  saving.value = true
  saveError.value = ''
  try {
    if (needsAutomation()) {
      const status = await api.getAutomationStatus()
      if (!status.connected) {
        await connectWithNotice()
      }
    }

    const payload = {
      name: name.value,
      enabled: enabled.value,
      trigger: triggerPayload(),
      graph: { nodes: nodes.value, edges: edges.value }
    }
    if (isNew()) {
```

(The rest of `save()` — the `if (isNew()) { ... } else { ... }` block, `catch`, and `finally` — is unchanged.)

- [ ] **Step 4: Verify types and lint are clean**

Run: `cd frontend && pnpm check:types && pnpm lint`
Expected: no errors pointing at `WorkflowBuilder.vue`.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/WorkflowBuilder.vue
git commit -m "feat(frontend): silently connect automation when activating a schedule/event workflow"
```

---

## Task 7: Rewrite the automation e2e test

**Files:**
- Modify: `frontend/tests/e2e/automation.spec.ts`

**Interfaces:**
- Consumes: UI text/roles produced by Task 5 (`"Background execution active"`, `"Background execution off"`, `"manage"` button) and Task 6 (silent connect on save, `"Background execution enabled for your account"` toast text from Task 3's composable).

- [ ] **Step 1: Replace the whole file**

Replace the full contents of `frontend/tests/e2e/automation.spec.ts` with:

```ts
import { test, expect } from '@playwright/test'
import { login } from './support/auth'

test('background execution connects automatically and can be disconnected', async ({ page }) => {
  await login(page)
  await page.goto('/workflows/workflows')
  await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible()

  // Start from a known state regardless of what earlier runs left behind: delete any
  // leftover workflows from this test, then disconnect automation if it's still connected
  // (safe to do unconditionally once those workflows are gone — nothing left to warn about).
  for (const row of await page.getByRole('row').filter({ hasText: 'e2e automation workflow' }).all()) {
    await row.getByRole('button', { name: 'Delete' }).click()
  }
  if (await page.getByRole('button', { name: 'manage' }).isVisible()) {
    await page.getByRole('button', { name: 'manage' }).click()
    await page.getByRole('button', { name: 'Disconnect' }).click()
    await expect(page.getByText('Background execution active')).toBeHidden()
  }

  // Build a workflow with a manual trigger first and save it — this exercises the "existing
  // workflow" update path (no hard navigation), which is where we can reliably observe the
  // one-time connect toast. Creating a workflow with a schedule trigger from scratch instead
  // hard-navigates to the new workflow's URL immediately after save, before a toast could be
  // observed.
  await page.getByRole('button', { name: 'Add workflow' }).click()
  await page.waitForURL(/\/workflows\/workflows\/new$/)

  await page.getByRole('button', { name: 'Add trigger' }).click()
  await page.getByRole('button', { name: 'Manual Trigger', exact: true }).click()
  await expect(page.locator('.workflows-node-trigger')).toBeVisible()

  const workflowName = `e2e automation workflow ${Date.now()}`
  await page.getByRole('button', { name: 'Untitled workflow' }).click()
  await page.getByLabel('Workflow name').fill(workflowName)
  await page.getByLabel('Workflow name').press('Enter')

  await page.getByRole('button', { name: 'Save' }).click()
  await page.waitForURL(/\/workflows\/workflows\/(?!new$)[\w-]+$/)

  // Still a manual trigger — no automation involved yet.
  await expect(page.getByText('Background execution enabled for your account')).toBeHidden()

  // Switch to a schedule trigger and save again — the "existing workflow" path, where
  // silent connect + the one-time toast fire with no button click involved.
  await page.locator('.workflows-node-trigger').click()
  await page.getByLabel('Trigger type').selectOption('schedule')
  await page.getByRole('button', { name: 'Close' }).click()
  await page.getByRole('button', { name: 'Save' }).click()

  await expect(page.getByText('Background execution enabled for your account')).toBeVisible()

  await page.goto('/workflows/workflows')
  await expect(page.getByText('Background execution active')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect automation' })).toHaveCount(0)

  // Disconnecting while the workflow is still active shows the warning.
  await page.getByRole('button', { name: 'manage' }).click()
  await page.getByRole('button', { name: 'Disconnect' }).click()
  await expect(page.getByText('will stop running in the background', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Yes, disconnect' }).click()
  await expect(page.getByText('Background execution active')).toBeHidden()
  await expect(page.getByText('Background execution off')).toBeVisible()

  // Clean up via the UI's own delete flow.
  const row = page.getByRole('row').filter({ hasText: workflowName })
  await row.getByRole('button', { name: 'Delete' }).click()
  await expect(row).toBeHidden()
})
```

- [ ] **Step 2: Run the e2e test**

Run: `cd frontend && pnpm test:e2e automation.spec.ts`
Expected: 1 passed.

- [ ] **Step 3: Run the full test suite as a regression check**

Run: `cd frontend && pnpm test:unit && pnpm test:e2e && pnpm check:types && pnpm lint`
Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: everything green — this confirms Tasks 1-7 compose correctly end to end.

- [ ] **Step 4: Commit**

```bash
git add frontend/tests/e2e/automation.spec.ts
git commit -m "test(e2e): rewrite automation.spec.ts for the silent-connect flow"
```
