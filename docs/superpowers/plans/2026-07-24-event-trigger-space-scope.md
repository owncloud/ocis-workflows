# Event Trigger Space Scoping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user optionally restrict a file-event trigger to one specific oCIS space, closing the existing gap where `event.filters.spaceId` is declared in the API/type contract but silently dropped before persistence and never matched.

**Architecture:** A new `ocisclient.ListDrives` method proxies oCIS's Graph API (`GET /graph/v1.0/me/drives`), exposed through a new minimal `GET /me/spaces` endpoint. `SpaceID` gets threaded through the existing trigger-index storage/sync path (currently dropped) and matched in the SSE handler alongside the existing path-prefix/extension filters. The frontend fetches the space list once in `WorkflowBuilder.vue` and passes it down as a prop to the already-pure-presentational `NodeDetailsPanel.vue`, which renders a plain `<select>`.

**Tech Stack:** Go 1.25 backend (chi router, `modernc.org/sqlite`), Vue 3 + `<script setup>` + TypeScript frontend, vitest, Playwright, Go's built-in e2e suite (`-tags=e2e`) against a real docker-compose oCIS stack.

## Global Constraints

- Go module: `github.com/owncloud/ocis-workflows`, Go 1.25.11. From `backend/`: `go build ./...`, `go vet ./...`, `go test ./...`. Backend e2e (needs a live docker-compose stack, see Task 5): `go test -tags=e2e ./tests/e2e/...`.
- Frontend package manager is `pnpm`, run from `frontend/`. Unit tests: `pnpm exec vitest run <pattern>`. E2e: `pnpm test:e2e <file>`.
- Verified live against `owncloud/ocis-rolling:latest`: `GET /graph/v1.0/me/drives` returns `{"value": [{"id": "...", "name": "...", "driveType": "personal"|"project"|"virtual", ...}]}`. The `id` is the same compound `storage-id$item-id` string that already arrives as `spaceid` on SSE events.
- Exclude `driveType == "virtual"` (the aggregate "Shares" view) from the space picker — it's not a real place file events originate "in."
- One optional space per trigger — no multi-select, matching how `pathPrefix`/`extension` are each a single value. Empty `SpaceID` means "any space," matching the existing "unset filter passes everything" convention used by `PathPrefix`/`Extension`.
- Space scoping applies to event triggers only, not schedule triggers.
- `backend/pkg/service` and `backend/pkg/ocisclient` have **no existing unit-test files** — this codebase's convention is that these thin HTTP-handler/HTTP-client packages are covered by the black-box e2e suite (`backend/tests/e2e/`), not package-level unit tests. Do not introduce a new unit-test pattern for either package in this work — coverage for the new endpoint/client method comes from Task 5's e2e tests.
- `frontend/src/components/NodeDetailsPanel.vue` is a pure presentational component today (no API access, no `useWorkflowsApi`/`useAppConfig` usage) — keep it that way. Spaces are fetched once by `WorkflowBuilder.vue` and passed down as a prop.
- Views/components in the frontend get Playwright e2e coverage only (no `@vue/test-utils` component-mount tests exist anywhere in this repo); composables get vitest unit tests. Follow that split.

---

## Task 1: oCIS client — list drives, and the `Space` API model

**Files:**
- Create: `backend/pkg/ocisclient/drives.go`
- Modify: `backend/pkg/model/workflow.go`

**Interfaces:**
- Consumes: `Client.baseURL`, `Client.httpClient` (`backend/pkg/ocisclient/client.go`), `Client.httpClient.Do` (same package, same conventions as `Me`/`ItemPath`).
- Produces: `(*ocisclient.Client).ListDrives(ctx context.Context, authHeader string) ([]ocisclient.Drive, error)` where `Drive{ID, Name, DriveType string}`, and `model.Space{ID, Name string}` — both consumed by Task 2.

- [ ] **Step 1: Add `model.Space`**

In `backend/pkg/model/workflow.go`, change:

```go
// AutomationStatus is a Graph-style singleton resource (like /me/mailboxSettings)
// describing whether a user has enabled scheduled/event-triggered automation.
type AutomationStatus struct {
	Connected          bool   `json:"connected"`
	ExpirationDateTime string `json:"expirationDateTime,omitempty"`
}

// Collection wraps a list response in Graph's "value" envelope.
```

to:

```go
// AutomationStatus is a Graph-style singleton resource (like /me/mailboxSettings)
// describing whether a user has enabled scheduled/event-triggered automation.
type AutomationStatus struct {
	Connected          bool   `json:"connected"`
	ExpirationDateTime string `json:"expirationDateTime,omitempty"`
}

// Space is a simplified view of an oCIS space (drive), exposed so a trigger's event filter
// can be scoped to one. Deliberately minimal — id and name are all a picker UI needs.
type Space struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Collection wraps a list response in Graph's "value" envelope.
```

- [ ] **Step 2: Add `ListDrives` to the oCIS client**

Create `backend/pkg/ocisclient/drives.go`:

```go
package ocisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Drive is an oCIS space as returned by the Graph API's list-drives endpoint.
type Drive struct {
	ID        string
	Name      string
	DriveType string
}

type drivesResponse struct {
	Value []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		DriveType string `json:"driveType"`
	} `json:"value"`
}

// ListDrives returns every space (drive) the caller (identified by authHeader) can access,
// via oCIS's Graph API.
func (c *Client) ListDrives(ctx context.Context, authHeader string) ([]Drive, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/graph/v1.0/me/drives", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list drives returned status %d", res.StatusCode)
	}

	var parsed drivesResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	drives := make([]Drive, 0, len(parsed.Value))
	for _, d := range parsed.Value {
		drives = append(drives, Drive{ID: d.ID, Name: d.Name, DriveType: d.DriveType})
	}
	return drives, nil
}
```

- [ ] **Step 3: Verify the build**

Run: `cd backend && go build ./... && go vet ./...`
Expected: clean (no output).

Per Global Constraints, `ocisclient` has no unit-test file convention in this repo — no test file is added in this task; `ListDrives` gets real coverage from Task 5's e2e test.

- [ ] **Step 4: Commit**

```bash
git add backend/pkg/ocisclient/drives.go backend/pkg/model/workflow.go
git commit -m "feat(backend): add ListDrives client method and Space API model"
```

---

## Task 2: `GET /me/spaces` endpoint

**Files:**
- Create: `backend/pkg/service/spaces.go`
- Modify: `backend/pkg/server/http/server.go`
- Modify: `backend/pkg/command/server.go`

**Interfaces:**
- Consumes: `ocisclient.Drive` / `(*ocisclient.Client).ListDrives` (Task 1), `model.Space` / `model.Collection[T]` (Task 1 / existing), `auth.TokenFromContext` (`backend/pkg/auth`, already used identically in `backend/pkg/service/automation.go`), package-level `writeJSON`/`writeError` helpers (`backend/pkg/service/workflows.go:341,347` — same package, no import needed).
- Produces: `service.NewSpacesHandler(drives DriveLister) *SpacesHandler` with a `List(w, r)` method wired to `GET /me/spaces` — no later task depends on this beyond the route existing.

- [ ] **Step 1: Add the handler**

Create `backend/pkg/service/spaces.go`:

```go
package service

import (
	"context"
	"net/http"

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/ocisclient"
)

// DriveLister is the subset of ocisclient.Client SpacesHandler needs. Satisfied by
// *ocisclient.Client.
type DriveLister interface {
	ListDrives(ctx context.Context, authHeader string) ([]ocisclient.Drive, error)
}

// SpacesHandler implements the /me/spaces Graph-shaped REST API — a read-only list of the
// caller's spaces, used to populate the event-trigger "scope to a space" picker.
type SpacesHandler struct {
	drives DriveLister
}

// NewSpacesHandler builds a SpacesHandler.
func NewSpacesHandler(drives DriveLister) *SpacesHandler {
	return &SpacesHandler{drives: drives}
}

// List handles GET /me/spaces. "virtual" drives (e.g. the aggregate "Shares" view) are
// excluded — they're not a real place file events originate "in," so scoping a trigger to
// one wouldn't mean anything coherent.
func (h *SpacesHandler) List(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.TokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
		return
	}

	drives, err := h.drives.ListDrives(r.Context(), "Bearer "+token)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ocisUnavailable", "could not list spaces")
		return
	}

	spaces := make([]model.Space, 0, len(drives))
	for _, d := range drives {
		if d.DriveType == "virtual" {
			continue
		}
		spaces = append(spaces, model.Space{ID: d.ID, Name: d.Name})
	}

	writeJSON(w, http.StatusOK, model.Collection[model.Space]{Value: spaces})
}
```

- [ ] **Step 2: Wire the route**

In `backend/pkg/server/http/server.go`, change:

```go
// Options configures the HTTP server's router.
type Options struct {
	AllowedOrigin string
	Validator     *auth.Validator
	Workflows     *service.WorkflowsHandler
	Automation    *service.AutomationHandler
	Logger        *slog.Logger
}
```

to:

```go
// Options configures the HTTP server's router.
type Options struct {
	AllowedOrigin string
	Validator     *auth.Validator
	Workflows     *service.WorkflowsHandler
	Automation    *service.AutomationHandler
	Spaces        *service.SpacesHandler
	Logger        *slog.Logger
}
```

Then change:

```go
		r.Route("/me/automation", func(r chi.Router) {
			r.Get("/", opts.Automation.Get)
			r.Post("/", opts.Automation.Connect)
			r.Delete("/", opts.Automation.Disconnect)
		})
	})

	return r
}
```

to:

```go
		r.Route("/me/automation", func(r chi.Router) {
			r.Get("/", opts.Automation.Get)
			r.Post("/", opts.Automation.Connect)
			r.Delete("/", opts.Automation.Disconnect)
		})

		r.Route("/me/spaces", func(r chi.Router) {
			r.Get("/", opts.Spaces.List)
		})
	})

	return r
}
```

- [ ] **Step 3: Construct and pass the handler**

In `backend/pkg/command/server.go`, change:

```go
	automationService := automation.New(ocisClient, db, log)

	workflowsHandler := service.NewWorkflowsHandler(store, graphExecutor, ocisClient, db, log)
	automationHandler := service.NewAutomationHandler(automationService, ocisClient)

	apiHandler := httpserver.New(httpserver.Options{
		AllowedOrigin: cfg.AllowedOrigin,
		Validator:     validator,
		Workflows:     workflowsHandler,
		Automation:    automationHandler,
		Logger:        log,
	})
```

to:

```go
	automationService := automation.New(ocisClient, db, log)

	workflowsHandler := service.NewWorkflowsHandler(store, graphExecutor, ocisClient, db, log)
	automationHandler := service.NewAutomationHandler(automationService, ocisClient)
	spacesHandler := service.NewSpacesHandler(ocisClient)

	apiHandler := httpserver.New(httpserver.Options{
		AllowedOrigin: cfg.AllowedOrigin,
		Validator:     validator,
		Workflows:     workflowsHandler,
		Automation:    automationHandler,
		Spaces:        spacesHandler,
		Logger:        log,
	})
```

- [ ] **Step 4: Verify the build**

Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: build succeeds, vet clean, all existing tests still pass (this task adds no new unit tests, per Global Constraints — `ocisClient` already satisfies `DriveLister` structurally since Task 1 added `ListDrives` with a matching signature).

- [ ] **Step 5: Commit**

```bash
git add backend/pkg/service/spaces.go backend/pkg/server/http/server.go backend/pkg/command/server.go
git commit -m "feat(backend): add GET /me/spaces endpoint"
```

---

## Task 3: Persist `SpaceID` through the trigger index

**Files:**
- Modify: `backend/pkg/localdb/localdb.go`
- Test: `backend/pkg/localdb/localdb_test.go`
- Modify: `backend/pkg/service/workflows.go`

**Interfaces:**
- Consumes: existing `TriggerIndexEntry`, `addColumnIfMissing`, `UpsertTriggerIndexEntry`, `listTriggers` (`backend/pkg/localdb/localdb.go`); `model.EventFilters.SpaceID` (already exists).
- Produces: `TriggerIndexEntry.SpaceID` field, correctly persisted and returned — consumed by Task 4 (SSE matching) and Task 5 (e2e).

- [ ] **Step 1: Write the failing test**

In `backend/pkg/localdb/localdb_test.go`, change `TestTriggerIndex`'s second `UpsertTriggerIndexEntry` call and its corresponding assertion:

```go
	if err := db.UpsertTriggerIndexEntry(ctx, TriggerIndexEntry{
		WorkflowID: "wf-2", UserID: "user-1", TriggerType: "event", EventType: "upload",
		PathPrefix: "/Invoices", Extension: ".pdf",
	}); err != nil {
		t.Fatalf("UpsertTriggerIndexEntry: %v", err)
	}
```

to:

```go
	if err := db.UpsertTriggerIndexEntry(ctx, TriggerIndexEntry{
		WorkflowID: "wf-2", UserID: "user-1", TriggerType: "event", EventType: "upload",
		PathPrefix: "/Invoices", Extension: ".pdf", SpaceID: "space-1",
	}); err != nil {
		t.Fatalf("UpsertTriggerIndexEntry: %v", err)
	}
```

and change:

```go
	if events[0].PathPrefix != "/Invoices" || events[0].Extension != ".pdf" {
		t.Fatalf("ListEventTriggers() filters = %+v", events[0])
	}
```

to:

```go
	if events[0].PathPrefix != "/Invoices" || events[0].Extension != ".pdf" || events[0].SpaceID != "space-1" {
		t.Fatalf("ListEventTriggers() filters = %+v", events[0])
	}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./pkg/localdb/... -run TestTriggerIndex -v`
Expected: build failure — `unknown field SpaceID in struct literal of type TriggerIndexEntry`.

- [ ] **Step 3: Add the `SpaceID` field, column, and wiring**

In `backend/pkg/localdb/localdb.go`, change:

```go
// TriggerIndexEntry is a denormalized pointer to a workflow with an active schedule/event trigger.
type TriggerIndexEntry struct {
	WorkflowID  string
	UserID      string
	TriggerType string // schedule | event
	Schedule    string
	EventType   string
	PathPrefix  string // event trigger filter, mirrors model.EventFilters
	Extension   string // event trigger filter, mirrors model.EventFilters
}
```

to:

```go
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
```

Change:

```go
	// CREATE TABLE IF NOT EXISTS only handles brand-new databases — a trigger_index table
	// created before path_prefix/extension existed needs these added explicitly.
	for _, col := range []string{"path_prefix", "extension"} {
		if err := db.addColumnIfMissing("trigger_index", col, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}
```

to:

```go
	// CREATE TABLE IF NOT EXISTS only handles brand-new databases — a trigger_index table
	// created before path_prefix/extension/space_id existed needs these added explicitly.
	for _, col := range []string{"path_prefix", "extension", "space_id"} {
		if err := db.addColumnIfMissing("trigger_index", col, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}
```

Change:

```go
// UpsertTriggerIndexEntry stores or replaces a workflow's trigger index entry. Called
// whenever a workflow with a schedule/event trigger is created or updated.
func (db *DB) UpsertTriggerIndexEntry(ctx context.Context, e TriggerIndexEntry) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO trigger_index (workflow_id, user_id, trigger_type, schedule, event_type, path_prefix, extension)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workflow_id) DO UPDATE SET
			user_id = excluded.user_id,
			trigger_type = excluded.trigger_type,
			schedule = excluded.schedule,
			event_type = excluded.event_type,
			path_prefix = excluded.path_prefix,
			extension = excluded.extension
	`, e.WorkflowID, e.UserID, e.TriggerType, e.Schedule, e.EventType, e.PathPrefix, e.Extension)
	return err
}
```

to:

```go
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
```

Change:

```go
func (db *DB) listTriggers(ctx context.Context, triggerType string) ([]TriggerIndexEntry, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT workflow_id, user_id, trigger_type, schedule, event_type, path_prefix, extension
		FROM trigger_index WHERE trigger_type = ?
	`, triggerType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TriggerIndexEntry
	for rows.Next() {
		var e TriggerIndexEntry
		if err := rows.Scan(&e.WorkflowID, &e.UserID, &e.TriggerType, &e.Schedule, &e.EventType, &e.PathPrefix, &e.Extension); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
```

to:

```go
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd backend && go test ./pkg/localdb/... -v`
Expected: `PASS` for `TestTriggerIndex` and every other existing test in the package (including `TestMigrateAddsColumnsToExistingTable`, which exercises the same `addColumnIfMissing` loop against a pre-existing old-shape table and now implicitly covers `space_id` being added too).

- [ ] **Step 5: Fix the actual bug — copy `Filters.SpaceID` in `syncTriggerIndex`**

In `backend/pkg/service/workflows.go`, change:

```go
	if wf.Trigger.Type == "event" && wf.Trigger.Event != nil {
		entry.EventType = wf.Trigger.Event.Type
		if wf.Trigger.Event.Filters != nil {
			entry.PathPrefix = wf.Trigger.Event.Filters.PathPrefix
			entry.Extension = wf.Trigger.Event.Filters.Extension
		}
	}
```

to:

```go
	if wf.Trigger.Type == "event" && wf.Trigger.Event != nil {
		entry.EventType = wf.Trigger.Event.Type
		if wf.Trigger.Event.Filters != nil {
			entry.PathPrefix = wf.Trigger.Event.Filters.PathPrefix
			entry.Extension = wf.Trigger.Event.Filters.Extension
			entry.SpaceID = wf.Trigger.Event.Filters.SpaceID
		}
	}
```

- [ ] **Step 6: Run the full backend test suite**

Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: everything green.

- [ ] **Step 7: Commit**

```bash
git add backend/pkg/localdb/localdb.go backend/pkg/localdb/localdb_test.go backend/pkg/service/workflows.go
git commit -m "fix(backend): persist EventFilters.SpaceID through the trigger index"
```

---

## Task 4: Match on space in the SSE handler

**Files:**
- Modify: `backend/pkg/sse/manager.go`
- Test: `backend/pkg/sse/manager_test.go`

**Interfaces:**
- Consumes: `TriggerIndexEntry.SpaceID` (Task 3), `eventPayload.SpaceID` (already exists, `backend/pkg/sse/manager.go:38-41`).
- Produces: nothing new consumed by later tasks — this closes the matching gap for Task 5's e2e test to exercise.

- [ ] **Step 1: Write the failing tests**

In `backend/pkg/sse/manager_test.go`, add these two tests after `TestHandleEventSkipsNonMatchingPathPrefix`:

```go
func TestHandleEventSkipsNonMatchingSpace(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "space-a"},
		},
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-1": {ID: "wf-1", Enabled: true},
	}}
	exec := &fakeExecutor{}

	m := New(triggers, store, &fakePathResolver{path: "/Invoices/foo.pdf"}, exec, "http://unused", false, time.Hour, discardLogger())
	m.handleEvent(t.Context(), "user-1", "Basic dGVzdA==", "postprocessing-finished", `{"itemid":"i","spaceid":"space-b"}`)

	time.Sleep(50 * time.Millisecond)
	if got := exec.runs.Load(); got != 0 {
		t.Fatalf("expected 0 runs for a non-matching space, got %d", got)
	}
}

func TestHandleEventMatchesSpecificSpace(t *testing.T) {
	triggers := &fakeTriggerStore{
		entries: []localdb.TriggerIndexEntry{
			{WorkflowID: "wf-1", UserID: "user-1", TriggerType: "event", EventType: "upload", SpaceID: "space-a"},
		},
	}
	store := &fakeWorkflowStore{workflows: map[string]model.WorkflowDefinition{
		"wf-1": {ID: "wf-1", Enabled: true},
	}}
	exec := &fakeExecutor{}

	m := New(triggers, store, &fakePathResolver{path: "/Invoices/foo.pdf"}, exec, "http://unused", false, time.Hour, discardLogger())
	m.handleEvent(t.Context(), "user-1", "Basic dGVzdA==", "postprocessing-finished", `{"itemid":"i","spaceid":"space-a"}`)

	waitFor(t, 2*time.Second, func() bool { return exec.runs.Load() == 1 })
}
```

- [ ] **Step 2: Run the tests to verify the new one fails**

Run: `cd backend && go test ./pkg/sse/... -run TestHandleEventSkipsNonMatchingSpace -v`
Expected: FAIL — `exec.runs.Load()` is `1`, not `0` (the trigger has no space filter enforced yet, so it matches regardless of space).

- [ ] **Step 3: Add the space-matching check**

In `backend/pkg/sse/manager.go`, change:

```go
		if e.PathPrefix != "" && !strings.HasPrefix(resolvedPath, e.PathPrefix) {
			continue
		}
		if e.Extension != "" && !strings.HasSuffix(resolvedPath, e.Extension) {
			continue
		}

		go m.runWorkflow(ctx, authHeader, e.WorkflowID, resolvedPath)
```

to:

```go
		if e.PathPrefix != "" && !strings.HasPrefix(resolvedPath, e.PathPrefix) {
			continue
		}
		if e.Extension != "" && !strings.HasSuffix(resolvedPath, e.Extension) {
			continue
		}
		if e.SpaceID != "" && e.SpaceID != payload.SpaceID {
			continue
		}

		go m.runWorkflow(ctx, authHeader, e.WorkflowID, resolvedPath)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./pkg/sse/... -v`
Expected: `PASS` for `TestHandleEventSkipsNonMatchingSpace`, `TestHandleEventMatchesSpecificSpace`, and every other existing test in the package.

- [ ] **Step 5: Run the full backend test suite**

Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: everything green.

- [ ] **Step 6: Commit**

```bash
git add backend/pkg/sse/manager.go backend/pkg/sse/manager_test.go
git commit -m "feat(backend): match event triggers against a scoped SpaceID"
```

---

## Task 5: Backend e2e coverage

**Files:**
- Create: `backend/tests/e2e/spaces_test.go`

**Interfaces:**
- Consumes: `doRequest`, `decodeJSON`, `testToken` (`backend/tests/e2e/workflows_test.go`), `mkdir`, `uploadFile` (`backend/tests/e2e/run_test.go`) — all same package, no imports needed beyond what a new file in `package e2e` requires.
- Produces: nothing consumed by later tasks — this is backend verification only.

This is black-box coverage against a real, running docker-compose stack (see `backend/tests/e2e/main_test.go`'s package doc for how to bring it up). It requires no second oCIS space to exist: the personal space is always present for any user, which is enough to prove the two things that matter — the endpoint returns real data, and a filter scoped to the *correct* space still lets the real event through. The negative case (wrong space is correctly filtered out) is already covered cheaply and deterministically at the unit level by Task 4 — this task doesn't duplicate that with a second, real project space.

- [ ] **Step 1: Write `TestListSpaces`**

Create `backend/tests/e2e/spaces_test.go`:

```go
//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"testing"
)

// TestListSpaces exercises GET /me/spaces against a real oCIS instance — every user has at
// least a personal space, so this doesn't depend on any project space having been created.
func TestListSpaces(t *testing.T) {
	res := doRequest(t, http.MethodGet, "/me/spaces", nil, true)
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("list spaces: expected 200, got %d: %s", res.StatusCode, body)
	}

	list := decodeJSON[struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}](t, res)

	if len(list.Value) == 0 {
		t.Fatal("expected at least the caller's personal space, got none")
	}
	for _, s := range list.Value {
		if s.ID == "" || s.Name == "" {
			t.Fatalf("space entry missing id/name: %+v", s)
		}
	}
}
```

- [ ] **Step 2: Write `TestEventTriggeredWorkflowRespectsMatchingSpaceScope`**

In the same file, add:

```go
// TestEventTriggeredWorkflowRespectsMatchingSpaceScope connects automation, resolves the
// caller's own (personal) space id via GET /me/spaces, creates a workflow with an upload
// event trigger scoped to exactly that space, uploads a matching file, and confirms it still
// fires — proving the SpaceID persisted through the trigger index actually matches the real
// spaceid oCIS's SSE events carry for uploads into that space.
func TestEventTriggeredWorkflowRespectsMatchingSpaceScope(t *testing.T) {
	token := testToken(t)

	spacesRes := doRequest(t, http.MethodGet, "/me/spaces", nil, true)
	spaces := decodeJSON[struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}](t, spacesRes)
	if len(spaces.Value) == 0 {
		t.Fatal("expected at least one space to scope the trigger to")
	}
	spaceID := spaces.Value[0].ID

	connectRes := doRequest(t, http.MethodPost, "/me/automation", nil, true)
	connectRes.Body.Close()
	if connectRes.StatusCode != http.StatusOK {
		t.Fatalf("connect automation: expected 200, got %d", connectRes.StatusCode)
	}
	t.Cleanup(func() {
		res := doRequest(t, http.MethodDelete, "/me/automation", nil, true)
		res.Body.Close()
	})

	newWorkflow := map[string]any{
		"name":    "e2e space-scoped event workflow",
		"enabled": true,
		"trigger": map[string]any{
			"type": "event",
			"event": map[string]any{
				"type":    "upload",
				"filters": map[string]string{"pathPrefix": "/e2e-space-scope-test", "spaceId": spaceID},
			},
		},
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "trigger", "type": "trigger", "position": map[string]int{"x": 0, "y": 0}, "data": map[string]any{
					"triggerType": "event", "eventType": "upload",
				}},
				{"id": "llm-1", "type": "llm", "position": map[string]int{"x": 200, "y": 0}, "data": map[string]any{
					"prompt": "Say hi",
				}},
			},
			"edges": []map[string]string{{"id": "e1", "source": "trigger", "target": "llm-1"}},
		},
	}

	createRes := doRequest(t, http.MethodPost, "/me/workflows", newWorkflow, true)
	if createRes.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createRes.Body)
		t.Fatalf("create workflow: expected 201, got %d: %s", createRes.StatusCode, body)
	}
	workflow := decodeJSON[struct {
		ID string `json:"id"`
	}](t, createRes)
	t.Cleanup(func() {
		res := doRequest(t, http.MethodDelete, "/me/workflows/"+workflow.ID, nil, true)
		res.Body.Close()
	})

	// Same reconcile-interval wait as TestEventTriggeredWorkflowRunsOnUpload — give the SSE
	// manager time to open the stream before uploading.
	time.Sleep(35 * time.Second)

	mkdir(t, token, "/e2e-space-scope-test")
	uploadFile(t, token, "/e2e-space-scope-test/hello.txt", "hello from the space-scope e2e test")

	deadline := time.Now().Add(30 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		listRes := doRequest(t, http.MethodGet, "/me/workflows/"+workflow.ID+"/executions", nil, true)
		list := decodeJSON[struct {
			Value []struct {
				TriggeredBy string `json:"triggeredBy"`
				Status      string `json:"status"`
			} `json:"value"`
		}](t, listRes)

		for _, exec := range list.Value {
			if exec.TriggeredBy == "event" && exec.Status == "succeeded" {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(3 * time.Second)
	}

	if !found {
		t.Fatal("expected at least one successful event-triggered execution scoped to the caller's own space within 30s of upload, found none")
	}
}
```

Add `"time"` to the file's import block (needed for `time.Sleep`/`time.Now`/`time.Second`).

- [ ] **Step 3: Bring up the e2e stack and run the new tests**

From the repo root:

```bash
LLM_ENDPOINT=http://fake-llm:8080/v1 LLM_MODEL=fake-model LLM_API_KEY='' docker compose --profile test up -d --build
```

Then from `backend/`:

```bash
go test -tags=e2e ./tests/e2e/... -run 'TestListSpaces|TestEventTriggeredWorkflowRespectsMatchingSpaceScope' -v
```

Expected: both `PASS` (the second one takes roughly a minute, matching the existing `TestEventTriggeredWorkflowRunsOnUpload`'s timing).

- [ ] **Step 4: Run the full backend e2e suite as a regression check**

Run: `go test -tags=e2e ./tests/e2e/... -v`
Expected: all tests green (this confirms nothing in Tasks 1-4 broke the existing automation/workflow e2e coverage).

- [ ] **Step 5: Commit**

```bash
git add backend/tests/e2e/spaces_test.go
git commit -m "test(e2e): add backend e2e coverage for /me/spaces and space-scoped event triggers"
```

---

## Task 6: Frontend — `Space` type and `listSpaces()`

**Files:**
- Modify: `frontend/src/types/workflow.ts`
- Modify: `frontend/src/composables/useWorkflowsApi.ts`
- Test: `frontend/tests/unit/useWorkflowsApi.spec.ts`

**Interfaces:**
- Consumes: existing `request`/`GraphCollection` machinery in `useWorkflowsApi.ts`.
- Produces: `Space { id: string; name: string }` type, and `listSpaces(): Promise<Space[]>` on the object `useWorkflowsApi` returns — consumed by Task 7.

- [ ] **Step 1: Add the `Space` type**

In `frontend/src/types/workflow.ts`, change:

```ts
export interface AutomationStatus {
  connected: boolean
  expirationDateTime?: string
}

export interface GraphCollection<T> {
```

to:

```ts
export interface AutomationStatus {
  connected: boolean
  expirationDateTime?: string
}

export interface Space {
  id: string
  name: string
}

export interface GraphCollection<T> {
```

- [ ] **Step 2: Write the failing test**

In `frontend/tests/unit/useWorkflowsApi.spec.ts`, add this test right after the `'unwraps the Graph-style collection envelope on list'` test:

```ts
  it('unwraps the Graph-style collection envelope when listing spaces', async () => {
    ;(fetch as unknown as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ value: [{ id: 'space-1', name: 'Admin' }] })
    })

    const api = useWorkflowsApi('https://example.test/api/v1beta1')
    const result = await api.listSpaces()

    expect(result).toEqual([{ id: 'space-1', name: 'Admin' }])
    expect(fetch).toHaveBeenCalledWith(
      'https://example.test/api/v1beta1/me/spaces',
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: 'Bearer test-token' })
      })
    )
  })
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd frontend && pnpm exec vitest run useWorkflowsApi`
Expected: FAIL — `api.listSpaces is not a function`.

- [ ] **Step 4: Add `listSpaces`**

In `frontend/src/composables/useWorkflowsApi.ts`, change the import block:

```ts
import type {
  AutomationStatus,
  ExecutionRecord,
  GraphCollection,
  GraphError,
  NewWorkflowDefinition,
  WorkflowDefinition
} from '../types/workflow'
```

to:

```ts
import type {
  AutomationStatus,
  ExecutionRecord,
  GraphCollection,
  GraphError,
  NewWorkflowDefinition,
  Space,
  WorkflowDefinition
} from '../types/workflow'
```

Change:

```ts
  const disconnectAutomation = (): Promise<void> => request<void>('/me/automation', { method: 'DELETE' })

  return {
    listWorkflows,
    getWorkflow,
    createWorkflow,
    updateWorkflow,
    deleteWorkflow,
    runWorkflow,
    listExecutions,
    getExecution,
    getAutomationStatus,
    connectAutomation,
    disconnectAutomation
  }
}
```

to:

```ts
  const disconnectAutomation = (): Promise<void> => request<void>('/me/automation', { method: 'DELETE' })

  const listSpaces = (): Promise<Space[]> => request<GraphCollection<Space>>('/me/spaces').then((c) => c.value)

  return {
    listWorkflows,
    getWorkflow,
    createWorkflow,
    updateWorkflow,
    deleteWorkflow,
    runWorkflow,
    listExecutions,
    getExecution,
    getAutomationStatus,
    connectAutomation,
    disconnectAutomation,
    listSpaces
  }
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd frontend && pnpm exec vitest run useWorkflowsApi`
Expected: `PASS` — all tests in the file, including the new one.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/types/workflow.ts frontend/src/composables/useWorkflowsApi.ts frontend/tests/unit/useWorkflowsApi.spec.ts
git commit -m "feat(frontend): add listSpaces() to useWorkflowsApi"
```

---

## Task 7: Space picker in the trigger config UI

**Files:**
- Modify: `frontend/src/views/WorkflowBuilder.vue`
- Modify: `frontend/src/components/NodeDetailsPanel.vue`

**Interfaces:**
- Consumes: `Space` type and `listSpaces()` (Task 6).
- Produces: a rendered "Space (optional)" `<select>` for event triggers, and `event.filters.spaceId` correctly read/written — consumed by Task 8's e2e test.

- [ ] **Step 1: Fetch spaces in `WorkflowBuilder.vue` and pass them down**

In `frontend/src/views/WorkflowBuilder.vue`, change the import block:

```ts
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import { useAppConfig } from '../composables/useAppConfig'
import { builderPath, listPath } from '../router'
import { findNodeType, TRIGGER_CATEGORY, AI_CATEGORY, ACTION_CATEGORY } from '../nodeTypes'
import type { TriggerType, WorkflowEdge, WorkflowNode, WorkflowNodeData } from '../types/workflow'
```

to:

```ts
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import { useAppConfig } from '../composables/useAppConfig'
import { builderPath, listPath } from '../router'
import { findNodeType, TRIGGER_CATEGORY, AI_CATEGORY, ACTION_CATEGORY } from '../nodeTypes'
import type { Space, TriggerType, WorkflowEdge, WorkflowNode, WorkflowNodeData } from '../types/workflow'
```

Change:

```ts
const name = ref($gettext('Untitled workflow'))
const editingName = ref(false)
const nameInputRef = ref<HTMLInputElement>()
const enabled = ref(true)
const nodes = ref<WorkflowNode[]>([])
const edges = ref<WorkflowEdge[]>([])
const loadError = ref('')
const saveError = ref('')
const saving = ref(false)
```

to:

```ts
const name = ref($gettext('Untitled workflow'))
const editingName = ref(false)
const nameInputRef = ref<HTMLInputElement>()
const enabled = ref(true)
const nodes = ref<WorkflowNode[]>([])
const edges = ref<WorkflowEdge[]>([])
const loadError = ref('')
const saveError = ref('')
const saving = ref(false)
const spaces = ref<Space[]>([])
```

Change:

```ts
const load = async () => {
  if (isNew()) {
    return
  }
  try {
    const workflow = await api.getWorkflow(currentId())
    name.value = workflow.name
    enabled.value = workflow.enabled
    nodes.value = workflow.graph.nodes
    edges.value = workflow.graph.edges
    fitViewSoon()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
}
```

to:

```ts
const load = async () => {
  if (isNew()) {
    return
  }
  try {
    const workflow = await api.getWorkflow(currentId())
    name.value = workflow.name
    enabled.value = workflow.enabled
    nodes.value = workflow.graph.nodes
    edges.value = workflow.graph.edges
    fitViewSoon()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
}

// Space scoping is an optional, decorative filter on event triggers — if the list can't be
// loaded, the picker just falls back to "Any space" rather than blocking the rest of the
// builder.
const loadSpaces = async () => {
  try {
    spaces.value = await api.listSpaces()
  } catch {
    // intentionally silent — see comment above
  }
}
```

Change:

```ts
onMounted(load)
```

to:

```ts
onMounted(() => {
  load()
  loadSpaces()
})
```

In the template, change:

```html
    <NodeDetailsPanel
      v-if="selectedNode"
      :node="selectedNode"
      @update="(data) => updateNodeData(selectedNode!.id, data)"
      @close="selectedNodeId = null"
    />
```

to:

```html
    <NodeDetailsPanel
      v-if="selectedNode"
      :node="selectedNode"
      :spaces="spaces"
      @update="(data) => updateNodeData(selectedNode!.id, data)"
      @close="selectedNodeId = null"
    />
```

- [ ] **Step 2: Add the space picker to `NodeDetailsPanel.vue`**

In `frontend/src/components/NodeDetailsPanel.vue`, change the template's event-trigger block:

```html
          <template v-if="triggerType === 'event'">
            <label class="workflows-ndv-label" for="ndv-event-type">{{ $gettext('Event') }}</label>
            <select id="ndv-event-type" v-model="eventType" class="workflows-ndv-select">
              <option value="upload">{{ $gettext('File uploaded') }}</option>
              <option value="move">{{ $gettext('File moved') }}</option>
              <option value="share">{{ $gettext('File shared') }}</option>
              <option value="lock">{{ $gettext('File locked') }}</option>
            </select>
            <oc-text-input
              v-model="eventPathPrefix"
              class="workflows-ndv-field"
              :label="$gettext('Only for files under path (optional)')"
              placeholder="/Invoices"
            />
          </template>
```

to:

```html
          <template v-if="triggerType === 'event'">
            <label class="workflows-ndv-label" for="ndv-event-type">{{ $gettext('Event') }}</label>
            <select id="ndv-event-type" v-model="eventType" class="workflows-ndv-select">
              <option value="upload">{{ $gettext('File uploaded') }}</option>
              <option value="move">{{ $gettext('File moved') }}</option>
              <option value="share">{{ $gettext('File shared') }}</option>
              <option value="lock">{{ $gettext('File locked') }}</option>
            </select>
            <oc-text-input
              v-model="eventPathPrefix"
              class="workflows-ndv-field"
              :label="$gettext('Only for files under path (optional)')"
              placeholder="/Invoices"
            />
            <label class="workflows-ndv-label" for="ndv-event-space">{{ $gettext('Space (optional)') }}</label>
            <select id="ndv-event-space" v-model="eventSpaceId" class="workflows-ndv-select">
              <option value="">{{ $gettext('Any space') }}</option>
              <option v-for="space in spaces" :key="space.id" :value="space.id">{{ space.name }}</option>
            </select>
          </template>
```

Change the script's imports and props:

```ts
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { findNodeTypeForNode } from '../nodeTypes'
import type { EventTriggerType, WorkflowNode, WorkflowNodeData } from '../types/workflow'

const props = defineProps<{ node: WorkflowNode }>()
```

to:

```ts
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { findNodeTypeForNode } from '../nodeTypes'
import type { EventTriggerType, Space, WorkflowNode, WorkflowNodeData } from '../types/workflow'

const props = defineProps<{ node: WorkflowNode; spaces: Space[] }>()
```

Change:

```ts
const eventPathPrefix = computed<string>({
  get: () => props.node.data.event?.filters?.pathPrefix ?? '',
  set: (value) =>
    patch({
      event: {
        type: props.node.data.event?.type ?? 'upload',
        filters: { ...props.node.data.event?.filters, pathPrefix: value }
      }
    })
})
```

to:

```ts
const eventPathPrefix = computed<string>({
  get: () => props.node.data.event?.filters?.pathPrefix ?? '',
  set: (value) =>
    patch({
      event: {
        type: props.node.data.event?.type ?? 'upload',
        filters: { ...props.node.data.event?.filters, pathPrefix: value }
      }
    })
})
const eventSpaceId = computed<string>({
  get: () => props.node.data.event?.filters?.spaceId ?? '',
  set: (value) =>
    patch({
      event: {
        type: props.node.data.event?.type ?? 'upload',
        filters: { ...props.node.data.event?.filters, spaceId: value }
      }
    })
})
```

- [ ] **Step 3: Verify types and lint are clean**

Run: `cd frontend && pnpm check:types && pnpm lint`
Expected: no errors pointing at either file.

Per Global Constraints, views/components get e2e coverage only — no unit test is added in this task; Task 8 covers the picker end to end.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/views/WorkflowBuilder.vue frontend/src/components/NodeDetailsPanel.vue
git commit -m "feat(frontend): add a space picker to the event trigger config panel"
```

---

## Task 8: Frontend e2e coverage

**Files:**
- Modify: `frontend/tests/e2e/event-trigger.spec.ts`

**Interfaces:**
- Consumes: the `Space (optional)` label/select produced by Task 7, `GET /me/spaces` (Tasks 1-2, filters out `virtual` drives).

The local dev stack's `admin` demo account has exactly one non-virtual drive (its personal space, named after the account — verified live) and one virtual "Shares" drive, which the backend already excludes. So the dropdown has exactly 2 options: "Any space" plus that one real space — this test asserts that count rather than hardcoding the space's display name, so it isn't coupled to demo-account naming details.

- [ ] **Step 1: Extend the test**

In `frontend/tests/e2e/event-trigger.spec.ts`, change:

```ts
  // Configure the event trigger via its Node Details panel: event type + path filter.
  await page.locator('.workflows-node-trigger').click()
  await page.getByLabel('Event').selectOption('move')
  await page.getByLabel('Only for files under path (optional)').fill('/Invoices')
  await page.getByRole('button', { name: 'Close' }).click()
```

to:

```ts
  // Configure the event trigger via its Node Details panel: event type + path filter + space.
  await page.locator('.workflows-node-trigger').click()
  await page.getByLabel('Event').selectOption('move')
  await page.getByLabel('Only for files under path (optional)').fill('/Invoices')

  // Exactly 2 options in this dev stack: "Any space" plus the admin account's one real
  // (non-virtual) space — assert the count rather than hardcoding the space's display name.
  const spaceSelect = page.getByLabel('Space (optional)')
  await expect(spaceSelect.locator('option')).toHaveCount(2)
  const spaceValue = await spaceSelect.locator('option').nth(1).getAttribute('value')
  await spaceSelect.selectOption({ index: 1 })

  await page.getByRole('button', { name: 'Close' }).click()
```

Change:

```ts
  await page.goto(workflowUrl)
  await page.locator('.workflows-node-trigger').click()
  await expect(page.getByLabel('Trigger type')).toHaveValue('event')
  await expect(page.getByLabel('Event')).toHaveValue('move')
  await expect(page.getByLabel('Only for files under path (optional)')).toHaveValue('/Invoices')
  await page.getByRole('button', { name: 'Close' }).click()
```

to:

```ts
  await page.goto(workflowUrl)
  await page.locator('.workflows-node-trigger').click()
  await expect(page.getByLabel('Trigger type')).toHaveValue('event')
  await expect(page.getByLabel('Event')).toHaveValue('move')
  await expect(page.getByLabel('Only for files under path (optional)')).toHaveValue('/Invoices')
  await expect(page.getByLabel('Space (optional)')).toHaveValue(spaceValue!)
  await page.getByRole('button', { name: 'Close' }).click()
```

- [ ] **Step 2: Ensure the stack is up to date and run the test**

```bash
cd frontend && pnpm build
cd .. && docker compose up -d --build workflows-backend
docker compose restart ocis
cd frontend && pnpm test:e2e event-trigger.spec.ts
```

Expected: 1 passed.

- [ ] **Step 3: Run the full frontend regression suite**

Run: `cd frontend && pnpm test:unit && pnpm test:e2e && pnpm check:types && pnpm lint`
Expected: everything green — this confirms Tasks 6-7 compose correctly with the rest of the frontend.

- [ ] **Step 4: Commit**

```bash
git add frontend/tests/e2e/event-trigger.spec.ts
git commit -m "test(e2e): extend event-trigger.spec.ts to cover space scoping"
```
