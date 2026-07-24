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
- Keep background workflows running continuously for an active user without
  ever requiring them to notice or act on credential expiry.
- Keep a discoverable way to see status and revoke the credential.

## Non-goals

- Changing the underlying oCIS auth-app mechanism or credential lifetime.
- Building our own app-password management UI beyond what's described below
  (we rely on oCIS's own account security settings as the authoritative place
  to inspect/revoke all app-passwords, ours included).
- Handling the case where a user is offline/never opens the app for >90 days
  gracefully beyond "it silently reconnects next time they do."

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

`automation.Service.Status` (called by the frontend's existing
`getAutomationStatus()` on every `WorkflowList` mount) is extended: if the
stored credential is within 14 days of `ExpiresAt`, it mints a fresh
app-password and upserts it (same operation as `Connect`) before returning the
status. This reuses an existing, already-frequent call site — no new
endpoint, no background job/middleware needed. As long as the user opens the
Workflows app at least once every ~76 days, the credential never lapses.
Renewal is silent — no toast — since it isn't a new consent event, just
maintenance of a previously granted one.

If the user does not return within the renewal window, the credential expires
naturally; background workflows stop firing until the user's next visit
triggers the mount-time reconciliation in (1).

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

### 6. Testing

Rewrite `frontend/tests/e2e/automation.spec.ts`:

- Create a schedule-triggered workflow, activate it, assert the status line
  appears with no button click involved.
- Assert the one-time toast appears on first connect only.
- Assert "manage" → "Disconnect" with an active schedule workflow shows the
  warning, and disconnecting flips the status line off.
- (Backend) unit test for `Service.Status` renewal-threshold behavior: a
  credential expiring within 14 days gets replaced with a new `ExpiresAt`;
  one expiring later is left untouched.

## Risks / open questions

- Renewal piggybacking on `Status()` means renewal only happens on page load,
  not on a fixed schedule — acceptable per Goals (only needs to cover an
  active user), but worth confirming no other code path relies on `Status()`
  being a pure read.
- We're relying on oCIS's own account security UI as the place a
  security-conscious user would go to fully audit/revoke the "workflows"
  app-password outside this app; we have not verified that UI's exact
  location/wording and should confirm during implementation.
