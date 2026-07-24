# Automation connect UX redesign

Date: 2026-07-24
Status: Approved (pending spec review)

## Context

Scheduled/event-triggered workflows only run in the background if the sidecar
holds a stored oCIS app-password for the workflow owner (see
`backend/pkg/automation`). Today, obtaining that credential is a manual,
separate step: `WorkflowList.vue` shows an "Automation not connected" pill and
a "Connect automation" button in the page header, independent of any specific
workflow. Users find this confusing — the button's purpose isn't self-evident,
and it's an extra required click before scheduled workflows actually work.

The credential itself is a real oCIS app-password, minted via
`POST /auth-app/tokens` (`backend/pkg/ocisclient/authapp.go`) with the label
`"workflows"`, expiring after 90 days (`automation.defaultExpiry`) — oCIS's
auth-app has no non-expiring option. Minting requires the caller's live bearer
token, which the frontend already holds on every authenticated request
(`useWorkflowsApi.ts` — `authStore.accessToken`). There is no technical
requirement for a dedicated user click; the token is available whenever the
user is using the app at all.

## Goals

- Remove the standalone "Connect automation" step from the normal user flow.
- Preserve a real, visible moment of awareness when a background credential is
  created — silent connect, not invisible connect.
- Keep background workflows running continuously without ever requiring the
  user to notice or act on credential expiry — regardless of whether they
  revisit the Workflows app itself.
- Keep a discoverable way to see status and revoke the credential.

## Non-goals

- Changing the underlying oCIS auth-app mechanism or credential lifetime.
- Building a general-purpose app-password management UI. oCIS core has no
  built-in "app passwords" settings page of its own — that exists only as a
  separate community-maintained app, which we don't assume is installed. Our
  own "manage" panel (section 4) is the sole supported way to inspect/revoke
  the `"workflows"` credential from within this app, and is self-sufficient
  for that purpose.
- Auto-disconnecting automation when a user's last schedule/event workflow is
  disabled. The self-renewal job (section 3) will keep renewing a connected-
  but-idle automation indefinitely until the user explicitly disconnects; this
  is a pre-existing gap the current manual-connect flow has too, not something
  this redesign needs to newly solve.

## Design

### 1. Trigger point

`WorkflowList.vue` drops the header pill and "Connect automation" button.
Instead:

- Whenever a workflow is saved/activated with a `schedule` or `event` trigger,
  the frontend checks automation status; if `connected: false`, it calls
  `connectAutomation()` silently as part of completing the activation.
- On `WorkflowList` mount, after loading the workflow list, if any workflow is
  `enabled` with a `schedule`/`event` trigger and automation is not connected,
  the same silent connect runs. This self-heals installs where automation was
  previously disconnected (manually, or via credential expiry beyond the
  renewal window) while such workflows remain active.
- Users who never create a schedule/event-triggered workflow never trigger a
  connect and never see any automation UI at all.

### 2. Consent / notification

Immediately after a successful silent connect, show a one-time toast:

> "Background execution enabled for your account — this workflow will keep
> running even when you're signed out."

This is the real consent moment: it's tied to the specific action the user
took (activating a scheduled/event workflow), not a separate abstract toggle.
It is not shown again on subsequent silent renewals (see below).

### 3. Renewal

Renewal must not depend on the user opening the Workflows app — that would
silently reintroduce the "must visit within N days" gap this redesign is
meant to remove. Instead it's a backend self-renewal job, run the same way
the existing scheduler operates (`backend/pkg/scheduler`): a periodic sweep
(e.g. daily) over all stored `localdb.Automation` rows, authenticating as
each row's owner via `Basic base64(username:appPassword)` — exactly the auth
header the scheduler already builds at `scheduler.go:136` to run the user's
workflows — with no live user session or bearer token involved at all.

For any automation within 14 days of `ExpiresAt`, the job calls
`graph.MintAppPassword` using that Basic-auth header (the endpoint only cares
that the caller is authenticated as themselves, per
`ocisclient.MintAppPassword`'s doc comment — it doesn't require a live OIDC
bearer token specifically) to mint a replacement, `UpsertAutomation`s it, and
revokes the old token. This means once connected, a `"workflows"` automation
renews itself indefinitely without the user ever needing to revisit the app —
it only stops if explicitly disconnected, or if renewal itself starts failing
(e.g. the stored app-password was revoked out-of-band).

`automation.Service.Status` no longer performs any renewal side effect — it
stays a pure read. Renewal failures are logged; if a renewal attempt fails
enough times to leave a credential within its final day of validity, treat it
the same as a lapsed credential (see failure handling below).

### 4. Visibility / manage

The header gets a quiet, non-interactive status line replacing the pill:

- `Background execution active · manage` when connected.
- `Background execution off` only if the account has at least one
  schedule/event-triggered workflow but automation isn't connected (should be
  transient/error state given (1), but shown rather than hidden so it isn't
  silently broken).
- Nothing at all if the account has no schedule/event-triggered workflows.

"manage" opens a small panel showing: connection status, expiry date, and a
"Disconnect" button. Disconnecting while schedule/event workflows are active
shows a confirmation warning ("N workflows will stop running in the
background").

### 5. Failure handling

If the silent connect during activation fails (e.g. `MintAppPassword`
errors), the activation itself is blocked and the existing inline error
pattern (`automationError`) is shown. An "Active" workflow that silently isn't
actually running in the background would be a worse failure mode than an
upfront error.

If the background self-renewal job fails repeatedly and a credential actually
expires, this is indistinguishable from a manual disconnect: background
workflows stop firing, and the mount-time reconciliation in (1) transparently
reconnects the next time the user opens the Workflows app.

### 6. Testing

Rewrite `frontend/tests/e2e/automation.spec.ts`:

- Create a schedule-triggered workflow, activate it, assert the status line
  appears with no button click involved.
- Assert the one-time toast appears on first connect only.
- Assert "manage" → "Disconnect" with an active schedule workflow shows the
  warning, and disconnecting flips the status line off.

Backend unit tests for the new renewal job:

- An automation within 14 days of `ExpiresAt` gets a new token minted and
  upserted (old token revoked), authenticated via Basic auth built from its
  own stored username/app-password — no live/bearer credential involved.
- An automation with more than 14 days left is left untouched.
- A renewal call that fails (e.g. the stored app-password was already revoked
  out-of-band) is logged and does not crash the sweep for other users.

## Risks / open questions

- This assumes oCIS's `/auth-app/tokens` endpoint accepts Basic auth with an
  existing app-password as authorization to mint a new one for that same
  user — the scheduler already relies on app-password Basic auth being valid
  for Graph/WebDAV calls (`scheduler.go:136`), but we have not specifically
  confirmed the auth-app endpoint itself accepts it rather than requiring an
  OIDC bearer token. Needs verification against the target oCIS version early
  in implementation; if unsupported, renewal would have to fall back to
  something session-dependent again.
- The renewal job needs a place to run periodically — likely the same process
  hosting the existing scheduler, on its own ticker, rather than new
  infrastructure. Implementation should confirm this fits the current process
  model before adding a second background loop.
- A connected-but-idle automation (no active schedule/event workflows) will
  still renew forever until manually disconnected (see Non-goals) — not a
  regression, but worth surfacing to users somehow in a future iteration if it
  turns out to matter (e.g. a stale-automation nudge).
