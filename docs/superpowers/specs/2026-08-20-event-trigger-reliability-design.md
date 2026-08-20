# Event-trigger reliability: SSE + activitylog reconciliation backstop

Date: 2026-08-20
Status: Approved (pending spec review)

## Context

Event-triggered workflows (`trigger.type: "event"`) fire via a single live SSE
connection per user to oCIS's notification endpoint
(`backend/pkg/sse/manager.go`). This is a live-only push mechanism: if the
connection is down or still reconnecting when an oCIS event fires, that event
is lost silently and permanently — no error, no failed execution record,
nothing. The user sees a workflow that appears correctly configured and
enabled, and simply never runs.

This was diagnosed from a real bug report ("created an upload→tag workflow,
uploaded a file, nothing happened") and confirmed live against this project's
dev stack: backend logs at container startup show the SSE connection failing
(`EOF`, then `401`) for a few seconds before stabilizing — exactly the kind of
window in which an upload immediately after enabling a trigger would be
missed.

Two alternative fixes were investigated and ruled out:

- **oCIS Graph delta query** — doesn't exist. No `/drives/{id}/root/delta`
  route anywhere in oCIS's route table or the libre-graph-api spec.
- **Hardening SSE itself** — not possible from our side. oCIS's `sse`
  service is the only service in its codebase that opens its NATS consumer
  with `disableDurability: true`, and at the HTTP layer explicitly disables
  replay (`stream.AutoReplay = false`, no event IDs assigned, so even a
  client sending `Last-Event-ID` gets nothing). This is a deliberate upstream
  decision (see `owncloud/ocis#7604`: durable per-client SSE consumers used
  to leak hundreds of orphaned NATS consumers in production; the maintainer's
  stated fix rationale was *"if the SSE service is down, there is no need to
  persist messages for it... users cannot subscribe if the service is
  down"*). There is no reasonable path to contribute replay upstream.

A third option, consuming oCIS's internal NATS/JetStream bus directly, was
also investigated and rejected for now:

- Becoming a registered oCIS service is not required for NATS access (that
  mechanism is for gRPC/HTTP discovery only), but staying an independent
  sidecar means the embedded NATS would need to be exposed beyond loopback —
  and oCIS's embedded NATS server has **no authentication mechanism at all**
  (`services/nats/pkg/config/config.go`'s `Nats` struct has no
  username/password/token field; `services/nats/pkg/command/server.go` shows
  `EnableTLS` defaults false, i.e. `AllowNonTLS(true)` — plaintext,
  unauthenticated by default). Even with TLS forced on, the strictest mode
  available (`RequireAndVerifyClientCert`) is a single shared cert/key trust
  anchor with full read+publish rights to the entire bus, not scoped,
  per-client, or least-privilege. This is only safe against a **separately
  secured external NATS cluster** an operator already runs and points oCIS
  at — which isn't guaranteed to exist, and reintroduces a real security
  discussion this design doesn't need to have. Direct NATS consumption
  remains a viable *future* opt-in mode for operators in that specific
  situation, but is out of scope here.

This leaves a reconciliation backstop using oCIS's **activitylog** service
(`GET /graph/v1beta1/extensions/org.libregraph/activities`) as the pragmatic
option: it ships in oCIS's default service set (registered in the mandatory
`reg(...)` group in `ocis/pkg/runtime/service/service.go`, same tier as
`postprocessing`/`webdav` — not opt-in like `auth-app`/`antivirus`), uses
only public, already-authenticated HTTP APIs, and was verified live against
this project's dev stack to return correct, queryable activity history. It is
individually excludable via `OCIS_EXCLUDE_RUN_SERVICES=activitylog` (real
deployments doing this for performance reasons have been observed), so this
design must degrade *visibly*, not assume universal availability.

## Goals

- Close the SSE gap for `upload` and `move` triggers, correctly scoped to
  both personal and project spaces.
- Keep request volume to oCIS bounded by actual instability, not by instance
  size — must not degrade into a fixed-interval poll-every-user design at a
  few-thousand-user instance.
- Make degraded reliability (activitylog unavailable) visible to the user
  instead of silently falling back to SSE-only guarantees.
- Stay a pure sidecar: no new exposure, no dependency on NATS, no change to
  the existing bearer-token/app-password auth model.

## Non-goals

- Backstopping `share` triggers in this iteration. Activitylog does record
  `MessageShareCreated`/`MessageLinkCreated`, so this is mechanically
  possible, but scoping the design to `upload`/`move` first keeps the initial
  implementation and its e2e coverage smaller; extending the mapping table to
  `share` is a small, low-risk follow-up once the mechanism is proven.
- Backstopping `lock` triggers. Activitylog has no `FileLocked` message
  constant anywhere in its source — there is nothing to query. `lock`
  triggers stay exactly as reliable as they are today (SSE-only,
  best-effort). Not a regression; explicitly out of scope.
- Direct NATS/JetStream consumption (see Context above) — a possible future
  opt-in mode for operators with a properly secured external NATS cluster,
  not part of this design.
- Real-time delivery for backstop-recovered events. This is a catch-up
  mechanism bounded by the reconnect-triggered cadence below, not a
  replacement for SSE's latency.
- Changing SSE's own behavior or reconnect/backoff logic
  (`sse.Manager.consumeForUser`) beyond adding the one reconciliation hook
  described below.

## Dependency

This design assumes [PR #31](https://github.com/owncloud/ocis-workflows/pull/31)
("scope file-event triggers to a specific oCIS space") is merged first. It
adds two things this design reuses directly rather than re-implementing:

- `localdb.TriggerIndexEntry.SpaceID`, persisted correctly through
  `UpsertTriggerIndexEntry`/`listTriggers` (currently dead code without that
  PR — `EventFilters.SpaceID` exists in the model but is silently dropped
  before persistence).
- `ocisclient.ListDrives` / `GET /me/spaces`, used here to enumerate a user's
  full drive set when a trigger is unscoped ("any space").

## Design

### 1. Drive scope, derived from the trigger index

For each user with at least one enabled event trigger (`localdb.TriggerIndexEntry`
rows with `TriggerType == "event"`, grouped by `UserID`):

- If every one of that user's event triggers has a non-empty `SpaceID`, the
  backstop only needs to cover exactly those drives.
- If any trigger has an empty `SpaceID` ("any space" — matches every space
  the user has access to, same as SSE does today post-PR#31), enumerate that
  user's full drive list via `ListDrives` and cover all of them.

This keeps request volume tied to how specifically a user's triggers are
scoped rather than blindly enumerating every drive for every user with any
event trigger at all.

### 2. When reconciliation runs — reconnect-triggered, not polled

`sse.Manager.consumeForUser` gets one new hook: after `streamOnce` returns
having successfully established a connection (i.e. after a reconnect, not on
every loop iteration), check whether there's a recorded gap for that user —
a stored cursor older than "now minus a grace period" (e.g. 5s; tunable)
indicating the connection was down for a meaningful span. If so, run one
reconciliation pass across that user's derived drive scope (§1), then
advance the cursor.

- **First-ever connect for a user** (no stored cursor row) is exactly the
  original bug's scenario — a brand-new trigger whose upload can race the
  very first SSE connection — so it must not be treated as "nothing to
  backfill." Instead of seeding the cursor at "now" and skipping the query,
  the first pass looks back a bounded window (`firstConnectLookback`, 5m
  default — generous versus realistic reconnect delays, short versus
  activitylog's multi-day retention so an old/busy drive's first-ever pass
  doesn't flood-dispatch weeks of history) and otherwise runs the exact same
  query/dispatch/advance-to-now logic as a warm pass. (Caught late, during
  the implementation pass, by an e2e test whose first isolated run against a
  byte-fresh database failed for exactly this reason — an earlier version of
  this section's reasoning was wrong in the same way §4's cursor-advancement
  reasoning was: not thinking through the exact timing relative to the event
  being recovered.)
- **Flapping reconnects** debounce: if reconnects happen repeatedly in a
  short window, coalesce into a single reconciliation pass once the
  connection has been stable for e.g. 5s (tunable, same order as the grace
  period above), rather than firing one pass per reconnect attempt.
- **Correlated mass-reconnect** (e.g. oCIS's own `sse` service restarting,
  which drops every active per-user consumer at once): reconciliation passes
  are submitted to a small bounded worker pool (a fixed concurrency cap —
  e.g. 10 concurrent reconciliation requests instance-wide, tunable — not one
  goroutine per user) so a fleet-wide reconnect event doesn't hammer oCIS
  with thousands of simultaneous activitylog queries at the exact moment
  it's already recovering.

This makes request volume scale with *instability events*, not with total
user or drive count — a healthy instance where connections rarely drop
generates near-zero backstop traffic.

### 3. Query shape and event mapping

For each `(user, drive)` pair needing reconciliation:

```
GET /graph/v1beta1/extensions/org.libregraph/activities
    ?kql=itemid:{driveID} AND depth:-1 AND timestamp>{cursor}
```

Verified live against the dev stack: the `timestamp>` filter correctly
excludes events at/before the cursor and correctly returns everything after
it, including across a real upload/rename/delete sequence.

The `template.message` field is **not** translated, user-locale text — it's
one of a small, fixed set of untranslated template constants, verified both
by reading oCIS's source (`services/activitylog/pkg/service/response.go`:
`NewActivity` stores the raw `l10n.Template(...)` string verbatim; nothing
calls `.Translate()` on the message itself, only on a couple of fallback
*variable* values) and by observing exact matches live:

| `template.message` (exact string) | maps to trigger type |
|---|---|
| `{user} added {resource} to {folder}` (`MessageResourceCreated`) | `upload` |
| `{user} moved {resource} to {folder}` (`MessageResourceMoved`) | `move` |
| `{user} renamed {oldResource} to {resource}` (`MessageResourceRenamed`) | `move` |

Any other message value is ignored (not one of our supported trigger types
in this iteration — see Non-goals for `share`).

`template.variables.resource.id` arrives pre-formatted as
`storageid$spaceid!opaqueid` (verified live) — the same compound ID format
`ocisclient.ItemPath` already consumes for SSE-sourced events, so path
resolution for `pathPrefix`/`extension` filter matching reuses the existing
`PathResolver` interface with no new client method.

**Self-trigger guard.** Verified live: this backend's own `.workflows`
bookkeeping writes (workflow definitions, execution records) appear in the
activitylog feed exactly like user uploads do. The same
`webdavstore.IsInternalPath` check SSE's `handleEvent` already applies must
be applied here too, or a workflow with no path filter would retrigger
itself on every execution it records.

### 4. Dedup and cursor advancement

- Cursor is `(user_id, drive_id) -> last_checked` (a timestamp), advanced to
  the latest `recordedTime` seen in a reconciliation pass, not to "now" —
  so a burst of activity right at the query boundary isn't skipped on the
  next pass.
- Within a single pass, dedup by activity `id` (each activity has a unique
  `id` field, confirmed in every live response).
- A small overlap window (e.g. 5s, subtracted from the stored cursor before
  querying; tunable) trades an occasional double-fire for never missing a
  boundary event. Workflow actions here (tag/comment/move/copy/rename) are
  not destructive enough for this to be a meaningful risk — this project's
  own action nodes already tolerate re-running (e.g. `AssignTag` on an
  already-tagged file is a no-op at the API level).
- No dedup is attempted between what SSE already delivered and what
  reconciliation independently finds — a double-fire across the two paths is
  possible in principle (SSE delivers, connection drops before we'd know to
  skip it in reconciliation) but rare, and the cost is the same "safe to
  re-run" actions as above. Not worth the complexity of cross-path dedup in
  this iteration.

### 5. Degradation visibility

`GET /me/automation` (`model.AutomationStatus`) gains a `reliability` field:
`"full"` when the last reconciliation attempt for that user succeeded (or
none was needed yet), `"sse-only"` when the last attempt errored (activitylog
unreachable/disabled, or a non-200 response). This flips per-user based on
real attempts, not a global instance-wide flag — an admin who disabled
activitylog affects every user's field the first time each of them needs a
reconciliation pass.

Frontend surfaces this the same way `WorkflowList.vue` already surfaces
automation connection state (see `2026-07-24-automation-connect-ux-design.md`
for the existing pattern) — a status line change, not a new panel: something
like "Background execution active" (full) vs. a distinguishable degraded
state, is a frontend-side follow-up call, not fixed by this backend design.

### 6. Storage

New `localdb` table, added via the existing `addColumnIfMissing`/migration
pattern already used for `path_prefix`/`extension`/`space_id`:

```sql
CREATE TABLE IF NOT EXISTS event_cursors (
    user_id      TEXT NOT NULL,
    drive_id     TEXT NOT NULL,
    last_checked TEXT NOT NULL,
    last_status  TEXT NOT NULL DEFAULT 'full', -- 'full' | 'sse-only'
    PRIMARY KEY (user_id, drive_id)
);
```

## Testing

- Unit tests for the message-template → trigger-type mapping table
  (including: unknown message values are ignored, not errored).
- Unit tests for cursor/dedup logic: advances to latest `recordedTime` not
  "now", dedups by activity `id` within a pass, applies the overlap window.
- Unit test for the debounce/coalesce behavior on rapid reconnects.
- Unit test for the bounded worker pool under a simulated mass-reconnect
  (many users' reconciliation requests queued, concurrency capped).
- e2e test mirroring the existing `TestEventTriggeredWorkflowRunsOnUpload`
  pattern (`backend/tests/e2e/automation_test.go`), but deliberately
  interrupting the SSE connection before uploading — simulating the exact
  race from the original bug report — and asserting the workflow still fires
  once reconciliation runs after reconnect.
- e2e (or integration, if a live activitylog-disabled instance isn't
  practical in CI) test that a failing reconciliation query flips
  `reliability` to `"sse-only"` in `GET /me/automation`.

## Risks / open questions

- **Verified, not a risk**: the activitylog message-template stability, the
  `timestamp>` cursor filter behavior, the `resource.id` format, and the
  self-trigger visibility of `.workflows` writes were all confirmed live
  against a real running instance during this design's research, not just
  reasoned from source.
- Cross-path dedup between SSE and reconciliation is explicitly not
  attempted (§4) — if a future action type is added that isn't safely
  re-runnable, this should be revisited.
- The exact frontend surfacing of `reliability: "sse-only"` (copy, whether
  it's a distinct visual state from "Background execution off") is left to
  implementation/design judgment when that piece is built — the backend
  contract (the field existing and flipping correctly) is what this design
  fixes.
- This design depends on PR #31 merging first (see Dependency). If its
  `SpaceID`/`ListDrives` shape changes materially during review, §1's drive
  scope derivation should be re-checked against the merged version before
  implementation starts.
