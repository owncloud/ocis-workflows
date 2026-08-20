# Event trigger space scoping

Date: 2026-07-24
Status: Approved (pending spec review)

## Context

A file-event trigger (`upload`/`move`/`share`/`lock`) can already be filtered by
`pathPrefix` and `extension`. The type contract also declares a third filter,
`event.filters.spaceId` (`frontend/src/types/workflow.ts:9-17`,
`backend/pkg/model/workflow.go` `EventFilters.SpaceID`), but it is entirely
dead: there's no UI for it, `backend/pkg/service/workflows.go`'s
`syncTriggerIndex` never copies it into the persisted `TriggerIndexEntry`, and
`backend/pkg/sse/manager.go`'s `handleEvent` never matches on it. This work
wires it up end to end so a user can optionally restrict a trigger to a single
oCIS space (what the Graph API calls a "drive").

Verified against a live oCIS instance (`owncloud/ocis-rolling:latest`):
`GET /graph/v1.0/me/drives` returns `{"value": [{"id": "...", "name": "...",
"driveType": "personal"|"project"|"virtual", ...}]}`. The `id` field (a
compound `storage-id$item-id` string) is the same value that already arrives
as `spaceid` on incoming SSE events (`backend/pkg/sse/manager.go`'s
`eventPayload`, already parsed but only used to resolve a WebDAV path today).

## Goals

- Let a user optionally restrict a file-event trigger to one specific space,
  picked from a dropdown of their actual spaces (not a raw ID they'd have to
  look up elsewhere).
- Close the existing gap where `spaceId` is silently dropped before
  persistence, so setting it via the API would already have looked like it
  worked while doing nothing.

## Non-goals

- Multiple spaces per trigger — one optional space only, matching how
  `pathPrefix`/`extension` are each a single value.
- Space scoping for schedule triggers — schedule triggers aren't tied to a
  space the way file events are.
- A general-purpose space browser/picker component — just a plain `<select>`
  for this one field.

## Design

### 1. Backend: oCIS client — list drives

New `ocisclient.ListDrives(ctx, authHeader) ([]Drive, error)`, `GET
/graph/v1.0/me/drives`, parsed into `Drive{ID, Name, DriveType string}`. Auth
header forwarded exactly like the existing `Me`/`ItemPath` client methods
(same file, same conventions).

### 2. Backend: `GET /me/spaces` endpoint

A small new handler mirroring `AutomationHandler`'s shape: resolves the
bearer token, calls `ListDrives`, filters out `driveType == "virtual"` (the
"Shares" pseudo-space — not a real place file events originate "in", and
scoping a trigger to it wouldn't mean anything coherent), maps the rest to
`{id, name}`, and returns the existing Graph-collection envelope
(`model.Collection[T]{Value: [...]}`) the app already uses for
`/me/workflows`. Wired into `backend/pkg/server/http/server.go`'s router and
`backend/pkg/command/server.go`'s handler construction, following the same
`Options`-struct pattern as `Automation`/`Workflows`.

### 3. Backend: persist `SpaceID` through the trigger index

- `backend/pkg/localdb/localdb.go`: add `SpaceID` to `TriggerIndexEntry`; add
  a `space_id TEXT NOT NULL DEFAULT ''` column via `addColumnIfMissing` (same
  migration pattern already used for `path_prefix`/`extension`); include it
  in `UpsertTriggerIndexEntry`'s INSERT/UPDATE and `listTriggers`'s
  SELECT/Scan.
- `backend/pkg/service/workflows.go`'s `syncTriggerIndex`: copy
  `Filters.SpaceID` into the entry — this is the actual fix for the
  already-existing gap described in Context.

### 4. Backend: match on space in the SSE handler

`backend/pkg/sse/manager.go`'s `handleEvent` loops `for _, e := range entries`
or with `continue`-to-skip semantics; alongside the existing checks (`manager.go:285-288`):

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
```

Empty `SpaceID` means "any space," matching the existing "unset filter passes
everything" convention.

### 5. Frontend: fetch spaces, pass down as a prop

- `useWorkflowsApi.ts`: add `listSpaces(): Promise<Space[]>` calling `GET
  /me/spaces`, unwrapping the collection envelope the same way
  `listWorkflows` does.
- New type `Space { id: string; name: string }` in `types/workflow.ts`.
- `WorkflowBuilder.vue`: fetch spaces once on mount (parallel with the
  existing `load()` call), store in a `spaces` ref, pass down as
  `:spaces="spaces"` to `<NodeDetailsPanel>`.
- `NodeDetailsPanel.vue` stays a pure presentational component (no API
  access of its own, consistent with its current design) — it just accepts
  the new `spaces: Space[]` prop. Renders a `<select>` under the existing
  path-prefix field, for the `event` trigger type only: a default "Any space"
  option (empty-string value) plus one `<option>` per space. Bound via a new
  `eventSpaceId` computed property following the exact get/set pattern
  `eventPathPrefix` already uses, writing into `event.filters.spaceId`.

### 6. Error handling

If `listSpaces()` fails (e.g. the Graph API call errors), `WorkflowBuilder.vue`
degrades silently: `spaces` stays empty, the dropdown shows only "Any space,"
and nothing else in the builder is blocked — this is an optional, decorative
filter, not core functionality, so it doesn't warrant the same blocking-error
treatment as, say, a failed workflow load.

### 7. Testing

- Backend: a unit test for `syncTriggerIndex` confirming `Filters.SpaceID`
  is copied into the persisted `TriggerIndexEntry` (this is the regression
  test for the bug this whole feature closes). Unit tests for
  `handleEvent`'s new space-matching branch in `backend/pkg/sse/manager_test.go`,
  following that file's existing conventions for the path-prefix/extension
  matching tests (matching space fires, mismatched space doesn't, empty
  `SpaceID` matches regardless of the event's actual space).
- Frontend: `listSpaces()` gets a focused unit test in
  `useWorkflowsApi.spec.ts` mirroring the existing `listWorkflows` envelope
  test. No component test for `NodeDetailsPanel.vue`/`WorkflowBuilder.vue`
  (views/components get e2e coverage only, per this repo's established
  split). E2e: extend the existing
  `frontend/tests/e2e/event-trigger.spec.ts` (which already builds an
  event-triggered workflow and asserts `pathPrefix`/`eventType` persist) to
  also pick a space from the dropdown and assert it persists across a reload,
  the same way the path-prefix assertion already works.

## Risks / open questions

- The live verification of `/graph/v1.0/me/drives` used HTTP Basic auth
  (`admin:admin`) for convenience, not a forwarded bearer token — the actual
  Go client code will forward `Authorization: Bearer <token>` exactly like
  the existing `Me`/`ItemPath` calls, so this isn't expected to behave
  differently, but is worth a quick sanity check early in implementation
  against the real request path this app uses.
- The test admin account in the local dev stack only has a "personal" space
  and the virtual "Shares" drive — no real "project" space to test against
  end-to-end locally without first creating one. The e2e test should create
  a project space if needed, or the plan should note this as a setup step.
