# New workflow node types: proposal

Date: 2026-07-24
Status: Proposal (for discussion — no implementation yet)

## Context

The current node catalog (`frontend/src/nodeTypes.ts`) is small and linear:

- **Triggers (3):** manual, schedule, file-event (`upload` / `move` / `share` / `lock`,
  filterable by `pathPrefix`, `extension`, and — once
  `2026-07-24-event-trigger-space-scope-design.md` lands — `spaceId`).
- **AI (1):** LLM Prompt — renders `{{file.name}}` / `{{file.content}}` /
  `{{llm.output}}`-style template variables into a prompt and calls the configured LLM.
- **Actions (6):** tag, comment, move, copy, rename, notify.

The executor (`backend/pkg/executor/executor.go`) is a strict BFS walk from the trigger
node: it loads the target file's content into `vars["file.name"]` /
`vars["file.content"]` once at the start of the run, then executes every reachable node
in order — there is no branching, no batching, and no pausing. Two details in the
existing code matter a lot for what belongs on this list:

1. **Conditions are already modeled but dead.** `WorkflowNodeData.condition` and
   `WorkflowEdge.data.condition` exist in both the frontend types
   (`frontend/src/types/workflow.ts`) and the backend model
   (`backend/pkg/model/workflow.go`'s `EdgeData.Condition`), and `orderedNodes`'s doc
   comment says outright: *"Node/edge 'condition' fields are stored but not yet
   evaluated — every reachable node always runs. Deferred, not forgotten."* Any proposal
   for branching should build on this rather than invent a parallel mechanism.
2. **Scheduled/event runs already have a non-interactive auth story.** `pkg/scheduler` and
   `pkg/sse/manager.go` both synthesize the run's `authHeader` from a stored automation
   app password (`"Basic " + base64(automation.Username + ":" + automation.AppPassword)`),
   the same one minted by `POST /me/automation` (`pkg/ocisclient/authapp.go`'s
   `MintAppPassword`). Any new trigger that fires without a live user request (e.g. a
   webhook) can reuse this exact mechanism instead of inventing new auth plumbing.

This doc proposes a small, high-value shortlist of additions, grounded in what the
executor and file/graph clients can already do (or can trivially be extended to do),
rather than a generic "port every n8n node" list.

## Goals

- Fill the most conspicuous gaps in the current trigger/action catalog (things a user
  would expect and can't do today: delete a file, create a folder, branch on a
  condition).
- Prefer proposals that reuse existing plumbing (`FileClient`, `GraphClient`, the
  automation app-password auth path, the already-declared-but-unused `condition`
  fields) over ones that require new subsystems.
- Be explicit about which "obvious n8n-style" ideas were considered and rejected for this
  product, and why.

## Non-goals

- Implementing any of this. This is a proposal document only; no frontend or backend
  code changes accompany it.
- An exhaustive n8n-parity catalog. Quality over quantity — six proposals, each
  reasoned through, not twenty stubs.
- Re-litigating `event.filters.spaceId` — that work is already speced and approved in
  `2026-07-24-event-trigger-space-scope-design.md`.

## Design

### 1. Conditional Branch node (control-flow)

**What it does:** A new `nodeKind: 'condition'` canvas node with a single input and two
labeled outputs ("true"/"false"). Its config is one comparison: a left-hand template
string, an operator, and a right-hand value. At run time the executor renders the
left-hand template against `vars` (exactly like every action param already is, via
`render()`) and evaluates the comparison, then continues traversal down only the
matching outgoing edge.

**Why valuable here:** It's the single most-requested workflow-automation primitive
("if the LLM said 'invoice', tag it; otherwise notify a human") and, unlike every other
proposal below, it isn't blocked on new external API integration — it's blocked only on
the executor actually reading fields it already persists. Every other action node in
this system is a single, unconditional step; without branching, an LLM-classification
node's output can only ever feed a fixed, unconditional chain of actions, which caps how
useful the LLM node can be.

**Config fields:**
```
{
  left: string      // template, e.g. "{{llm.output}}"
  operator: 'equals' | 'contains' | 'notEquals' | 'notContains' | 'matches' (regex)
  right: string      // literal to compare against (also template-rendered)
}
```

**Output:** `result.Output` set to the boolean outcome (`"true"`/`"false"`), for
visibility in the execution log; no new `vars` entries.

**Implementation considerations:**
- Backend: `orderedNodes` currently does a plain BFS ignoring edge identity beyond
  source/target. It needs to become "follow only the edge from this node's chosen
  output handle" for condition nodes specifically (other node kinds keep today's
  "follow every outgoing edge" behavior, since they still only have one output). This
  is the biggest structural change proposed in this doc, but it's localized to
  `orderedNodes`/`Run`'s traversal loop, plus a new `runCondition` alongside
  `runLLM`/`runAction`.
- Frontend: needs a new node kind with two source handles in the Vue Flow canvas
  (today's canvas nodes are single-output), plus a details-panel form for the
  left/operator/right fields, and the two outgoing edges need a way to record which
  handle they came from (Vue Flow's `sourceHandle` already exists for this and can map
  to `WorkflowEdge` — worth confirming during implementation planning whether
  `WorkflowEdge` needs a new field to persist it or whether handle-id-as-edge-id
  convention already carries it).
- Truly cyclic graphs (loops) are out of scope here — this only prunes which
  *single* path through an otherwise-still-linear-per-branch DAG gets taken.

### 2. Webhook Trigger

**What it does:** A trigger, alongside manual/schedule/event, that fires when an
external HTTP request hits a per-workflow URL:
`POST /api/v1beta1/hooks/{workflowId}/{token}`. The request body (if JSON) is exposed to
the graph as `vars["webhook.body"]`/per-field `vars["webhook.body.<key>"]`, and the
resolved file path is left empty unless the caller also passes one explicitly (e.g.
`?path=/foo/bar.txt`) — most webhook-triggered workflows won't have "the file" the same
way an event trigger does.

**Why valuable here:** It's the one integration point every one of the "obvious"
trigger ideas (folder-created, share-created, user-added-to-space) is really trying to
approximate from the outside: a way for something outside oCIS's own event bus — a CI
pipeline, a form submission, another SaaS tool's outgoing webhook — to kick off a
workflow. Rather than guessing at oCIS internal event types this repo hasn't verified
against a live instance yet (see Non-goals below), a webhook trigger covers that need
generically and immediately.

**Config fields:**
```
{ } // no user-configured fields beyond a generated, read-only token shown once
```
Rotate/reveal affordance lives in the NDV, same spirit as the automation
connect/disconnect affordance already in the UI.

**Output:** `vars["webhook.body"]` (raw string), plus flattened top-level JSON keys as
`vars["webhook.body.<key>"]` when the body parses as a JSON object.

**Implementation considerations:**
- Backend: extend `localdb.TriggerIndexEntry` with `TriggerType: "webhook"` and a new
  `WebhookToken` column (random, generated on save, stored the way `AppPassword` already
  is via `secretbox`); add `GET`-free `POST /hooks/{workflowId}/{token}` route in
  `pkg/server/http/server.go` *outside* the `Validator.Middleware`-guarded
  `/api/v1beta1` group (the token itself is the auth), matching token against the stored
  value in constant time. Reuses the exact same `automation.Username` +
  `automation.AppPassword` → Basic-auth-header construction that `scheduler.go` and
  `sse/manager.go` already do — no new "run without a live request" auth path needed,
  it already exists twice.
- Frontend: new `trigger-webhook` entry in `nodeTypes.ts`; NDV panel shows the
  generated URL with a copy button once the workflow has been saved (parallel problem
  to "you need a workflow id before you can construct the URL," same as `.../run`
  already implies).
- Security: the token must be treated as a bearer credential (never logged, never in
  URLs surfaced anywhere read-only-shareable) — same care already taken with
  `AppPassword` in `secretbox` and with target URLs in `pkg/notify` ("treated as
  secrets by callers").

### 3. Delete File (action)

**What it does:** `actionType: 'delete'`. Issues WebDAV `DELETE` against the current
resource path.

**Why valuable here:** Move/copy/rename all exist; delete is the one basic file
operation conspicuously missing, and it's the natural terminal step for cleanup
workflows ("if a temp file is older than N days, delete it"; "if LLM classifies as spam,
delete it" — this is also the most immediate beneficiary of proposal #1's branching).

**Config fields:** none beyond the implicit current file (mirrors `tag`/`comment`,
which also take no destination — compare to `move`/`copy`/`rename`, which need a
`destination`/`newName` param).

**Output:** `result.Output = currentPath` (the path that was deleted), and
`currentPath` becomes `""` for any node after it in the chain (mirroring how `move`
already reassigns `currentPath` to the new location) — a defensive check should make
any subsequent file-needing action fail clearly ("no target file") rather than operate
on a stale path, the same failure shape `tag`/`comment`/`move`/`copy` already produce
when `currentPath == ""`.

**Implementation considerations:**
- Backend: add `Delete(ctx, authHeader, davPath) error` to `webdavfile.Client`
  (trivial — same shape as `copyOrMove`, just `http.MethodDelete` with no
  `Destination` header) and to the `FileClient` interface in `executor.go`; add a
  `case "delete":` arm to `runAction`.
- Frontend: new `action-delete` entry, `trash` (or similar) icon, `ACTION_CATEGORY`.
- oCIS's WebDAV `DELETE` moves the resource to the space's trash rather than
  hard-deleting it, matching the platform's normal WebDAV client behavior — worth
  confirming during implementation, but this is oCIS's job, not something this action
  needs to implement itself either way.

### 4. Create Folder (action)

**What it does:** `actionType: 'createFolder'`. Issues WebDAV `MKCOL` at a
template-rendered path.

**Why valuable here:** This is close to free: `webdavfile.Client.mkcol` already exists
and is already idempotent (treats `201`, `405`, and `409` all as success), it's just
private and only called internally by `Comment` to ensure its sidecar directories
exist. Exposing it as a first-class action lets workflows organize output (e.g. "create
`/Processed/{{file.name}}/`" style dated or per-classification folders) without a
separate product needing to build that idempotency logic from scratch.

**Config fields:**
```
{ path: string }   // template-rendered, e.g. "/Archive/{{llm.output}}"
```

**Output:** `result.Output = renderedPath`. Unlike move/copy/delete, this action does
not change `currentPath` — it creates a location, it doesn't relocate the file under
consideration to it (a follow-up `move`/`copy` action does that, using the same
rendered path as its `destination`).

**Implementation considerations:**
- Backend: export the existing `mkcol` as `CreateFolder(ctx, authHeader, davPath) error`
  (or add a thin exported wrapper) on `webdavfile.Client`; add to `FileClient`; add a
  `case "createFolder":` arm. No new HTTP surface — it's the same client already used
  by every other file action.
- Frontend: new `action-create-folder` entry, `folder-add` (or similar) icon.

### 5. Share File (action)

**What it does:** `actionType: 'share'`. Grants a permission (viewer by default) on the
current file to a specified user or group via oCIS's Graph permissions API.

**Why valuable here:** `tag`/`comment` already prove out the "Graph-API action that
isn't a WebDAV verb" pattern via `GraphClient.ResolveItemID` +
`GraphClient.AssignTag`; sharing is the next most obviously useful Graph-only
capability in this same family — e.g. "when an invoice is uploaded to `/Incoming`,
share it (read-only) with `accounting@`."

**Config fields:**
```
{
  recipient: string          // template-rendered email or group name
  role: 'viewer' | 'editor'  // default 'viewer'
}
```

**Output:** `result.Output = recipient` (mirrors `tag`'s `result.Output = tag`).

**Implementation considerations:**
- Backend: new `GraphClient` method, e.g. `Share(ctx, authHeader, itemID, recipient,
  role string) error`, calling oCIS's
  `POST /graph/v1.0/drives/{drive-id}/items/{item-id}/invite`-style endpoint (exact
  route/payload shape needs verifying against a live oCIS instance the same way the
  space-scoping doc verified `/me/drives` — this doc does not claim that verification
  has been done). Resolving the recipient to a Graph user/group id will likely need a
  small `/graph/v1.0/users?$filter=...`-style lookup, another new `GraphClient` method.
- Frontend: new `action-share` entry, recipient + role fields in the NDV, `share`-style
  icon.
- This is the proposal with the most backend surface area of the three action nodes
  above (new Graph endpoints, not just a new arm on existing clients), so it's a
  reasonable one to defer a cycle behind delete/create-folder if the list needs
  trimming further.

### 6. Extract Text (AI/content)

**What it does:** A new AI-category node, `nodeKind: 'extractText'` (or an `actionType`
if it turns out cheaper to model as an action — implementation detail), that takes the
current file's raw content and produces a plain-text rendering into a new
`vars["file.text"]` variable, for formats where the file's raw bytes aren't already
readable text.

**Why valuable here:** This isn't a "nice to have" — it's arguably a latent bug fix.
`Executor.Run` today does exactly one thing with a triggering file's bytes:
`vars["file.content"] = string(content)` (`executor.go:74`), unconditionally, for every
file type. For a `.txt` or `.md` file that's correct. For a PDF, `.docx`, or image, an
LLM Prompt node referencing `{{file.content}}` today gets that binary format's raw byte
soup stringified — not the document's actual text. Since the LLM node is the marquee AI
feature of this whole product, and "summarize this uploaded PDF" is an entirely
reasonable workflow to want, this gap is worth calling out and fixing with a real node
rather than working around silently.

**Config fields:** none required; optional `outputVariable: string` (defaulting to
`file.text`) to match the existing `outputVariable` field already declared (if unused)
on `WorkflowNodeData` for the LLM node.

**Output:** `vars["file.text"]` populated with extracted plain text;
`result.Output` set to a short preview/character count (mirroring `llm.output`'s pattern
of also writing into `result.Output`).

**Implementation considerations:**
- Backend: needs a text-extraction library per format (PDF text extraction, DOCX
  XML unzip-and-parse, etc.) — this is genuinely new dependency surface, not a
  reshuffling of existing clients like proposals #3/#4 are. Image OCR specifically
  would need either a bundled OCR engine (e.g. tesseract bindings) or an external
  service call, which is a meaningfully bigger lift than plain-document parsing; the
  document-parsing half of this (PDF/DOCX/RTF → text) is the higher-value, lower-cost
  slice and the one worth building first if this is picked up.
- Frontend: new `AI_CATEGORY` entry, `file-text` icon, no config fields beyond the
  optional output-variable override.
- Should run before any LLM Prompt node in the chain that wants `{{file.text}}`,
  same ordering responsibility already on the user for `{{llm.output}}` today (nothing
  in this system validates node ordering/dependencies; this isn't a new problem this
  proposal introduces).

## Considered and set aside

- **Folder-created / share-created / user-added-to-space triggers.** `share` is
  already a first-class file-event trigger type today (`share-created`/`link-created`
  both map to it in `pkg/sse/manager.go`'s `eventTypeMap`) — a separate
  "share-created trigger" would be a straight duplicate. Folder-creation and
  space-membership events would need verifying against oCIS's actual SSE event
  catalog first (the same live-instance verification the space-scoping doc did for
  `/me/drives`); this doc doesn't claim that verification and won't propose new event
  *types* without it. The Webhook Trigger (#2) covers the "let something external kick
  this off" need generically in the meantime.
- **Document classification, translation, image analysis.** All three are just
  different prompts to the existing LLM Prompt node ("classify this document as one
  of: invoice, contract, other" / "translate this to French" / send an image and ask
  what's in it, if/when the LLM node supports multimodal input) — none need a new node
  type, only good prompt-writing, once paired with Conditional Branch (#1) to act on
  the result.
- **Restore from trash.** A natural pair with Delete (#3) but meaningfully bigger:
  needs a trash-listing/trash-item API this repo has no client for yet at all (unlike
  delete, which is one new WebDAV verb on infrastructure that already exists).
  Worth a follow-up proposal once Delete ships and there's a real user need to restore
  from it.
- **Archive extraction.** Requires reading a zip's central directory server-side and
  re-uploading each member file — real engineering weight for a use case this product
  has no signal is common yet. Deferred.
- **Virus/malware scan hook.** No evidence in this codebase of any existing scanner
  integration point (ICAP, ClamAV, or otherwise) to hook into; proposing a node here
  would be speculating about infrastructure that doesn't exist yet, not extending
  infrastructure that does.
- **Delay/wait and Approval/human-in-the-loop nodes.** Both need the same missing
  capability: the executor as written (`Run`) is a single synchronous pass with no
  concept of pausing and resuming later — there's no persisted "this execution is
  paused at node X" state anywhere in `model.ExecutionRecord`. Adding either is really
  "make the executor resumable," a bigger architectural project than a new node type,
  and probably deserves its own design doc rather than a bullet in this one.
- **Loop-over-files / batch nodes.** The executor's `vars`/`currentPath` model assumes
  exactly one resource per run throughout (`currentPath` is a single string, threaded
  linearly). Looping would mean either running the remaining graph once per item and
  merging N execution results, or reworking `vars` to hold collections — again a
  structural change bigger than "one new node," better scoped as its own proposal.

## Risks / open questions

- Proposal #1 (Conditional Branch) is the only one that changes `orderedNodes`'s
  traversal semantics rather than just adding a case to `runAction`/`runLLM`-style
  switches; it should get its own focused design pass (edge/handle persistence, exact
  operator set, how a condition node with no matching outgoing edge for its result
  should be treated — dead-end vs. error) before implementation, not be implemented
  directly off this doc's config-field sketch.
- Proposal #2 (Webhook Trigger) needs a decision on rate limiting / abuse handling for
  a publicly-reachable, token-gated endpoint (the existing `Validator.Middleware`
  bearer-token gate doesn't apply to it by design, so a wrong-guess brute-force
  surface is new here in a way it isn't for the rest of the API).
- Proposal #5 (Share File)'s exact Graph API request/response shapes are asserted from
  general Graph API knowledge, not verified against a live oCIS instance the way the
  space-scoping doc verified `/me/drives` — flagged explicitly in its own section above,
  repeated here because it's the biggest unverified-claim risk in this document.
- Proposal #6 (Extract Text)'s OCR half (image → text) is meaningfully more expensive
  than its document-parsing half (PDF/DOCX → text); if picked up, splitting it into two
  separate, separately-prioritized pieces of work is likely the right call rather than
  treating "Extract Text" as one atomic deliverable.
