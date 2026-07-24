import type { ActionType, TriggerType, WorkflowEdge, WorkflowNode } from '../types/workflow'

// Action types whose backend implementation (backend/pkg/executor/executor.go, runAction)
// requires a non-empty `currentPath` and fails the node otherwise. `notify` is the only
// action that doesn't touch a file at all, so it's deliberately excluded.
const FILE_DEPENDENT_ACTION_TYPES: ReadonlySet<ActionType> = new Set(['tag', 'comment', 'move', 'copy', 'rename'])

/** Whether `actionType` operates on a target file and therefore needs `currentPath` to be set. */
export function isFileDependentActionType(actionType?: ActionType): boolean {
  return !!actionType && FILE_DEPENDENT_ACTION_TYPES.has(actionType)
}

/**
 * Walks the graph backwards from `nodeId` to the trigger node it descends from.
 * Returns `undefined` if no trigger is reachable upstream at all (e.g. a disconnected/orphan
 * node) — that's a different, unowned validation concern.
 */
function upstreamTriggerType(nodeId: string, nodes: WorkflowNode[], edges: WorkflowEdge[]): TriggerType | undefined {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const incoming = new Map<string, string[]>()
  for (const e of edges) {
    const list = incoming.get(e.target)
    if (list) {
      list.push(e.source)
    } else {
      incoming.set(e.target, [e.source])
    }
  }

  const visited = new Set<string>()
  const queue = [nodeId]
  while (queue.length) {
    const id = queue.shift()!
    if (visited.has(id)) continue
    visited.add(id)

    const node = byId.get(id)
    if (node?.type === 'trigger') {
      return node.data.triggerType
    }

    for (const source of incoming.get(id) ?? []) {
      queue.push(source)
    }
  }

  return undefined
}

/**
 * Whether `nodeId`'s upstream trigger *reliably* supplies a file, every single run.
 *
 * This mirrors real backend behavior (backend/pkg/executor/executor.go): `vars["file.*"]`
 * is only populated when a non-empty `resourcePath` reaches Executor.Run, and `currentPath`
 * for file actions comes from that same value. Only the File Event Trigger is guaranteed to
 * carry one — the SSE event manager always passes the actual path of the file that fired the
 * event (backend/pkg/sse/manager.go).
 *
 * Both other trigger types fall short of "reliable": a Schedule Trigger never provides one
 * (the scheduler always calls Run with an empty resourcePath — backend/pkg/scheduler/scheduler.go),
 * and a Manual Trigger only *might* — "Run now" lets a user optionally type a WebDAV path into
 * a free-text field, but nothing about the graph guarantees it's filled in.
 *
 * Intended for informational nudges (e.g. NodeDetailsPanel's warning) where flagging the
 * "might not, depends on the user" Manual Trigger case is useful. For deciding whether to
 * hard-disable an option (e.g. the node picker), use `canUpstreamProvideFile` instead — that
 * one only rules out the *structurally impossible* Schedule Trigger case, so it doesn't block
 * the legitimate "Manual Trigger + supply the file at run time" workflow shape.
 *
 * Returns `true` when no trigger is reachable upstream at all — nothing to flag against yet.
 */
export function hasUpstreamFileSource(nodeId: string, nodes: WorkflowNode[], edges: WorkflowEdge[]): boolean {
  const triggerType = upstreamTriggerType(nodeId, nodes, edges)
  return triggerType === undefined || triggerType === 'event'
}

/**
 * Whether `nodeId`'s upstream trigger could *plausibly* supply a file, i.e. it isn't
 * structurally impossible. Only a Schedule Trigger rules this out — see `hasUpstreamFileSource`
 * for the full reasoning on why Manual Trigger doesn't (it's conditional on user-supplied input
 * at run time, not guaranteed, but not impossible either).
 *
 * Use this for hard restrictions (e.g. disabling picker entries) where blocking a Manual
 * Trigger would break a legitimate, common workflow shape.
 */
export function canUpstreamProvideFile(nodeId: string, nodes: WorkflowNode[], edges: WorkflowEdge[]): boolean {
  const triggerType = upstreamTriggerType(nodeId, nodes, edges)
  return triggerType !== 'schedule'
}
