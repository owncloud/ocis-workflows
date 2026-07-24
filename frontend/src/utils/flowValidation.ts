import type { ActionType, WorkflowEdge, WorkflowNode } from '../types/workflow'

// Action types whose backend implementation (backend/pkg/executor/executor.go, runAction)
// requires a non-empty `currentPath` and fails the node otherwise. `notify` is the only
// action that doesn't touch a file at all, so it's deliberately excluded.
const FILE_DEPENDENT_ACTION_TYPES: ReadonlySet<ActionType> = new Set(['tag', 'comment', 'move', 'copy', 'rename'])

/** Whether `actionType` operates on a target file and therefore needs `currentPath` to be set. */
export function isFileDependentActionType(actionType?: ActionType): boolean {
  return !!actionType && FILE_DEPENDENT_ACTION_TYPES.has(actionType)
}

/**
 * Walks the graph backwards from `nodeId` to the trigger it descends from and reports
 * whether that trigger reliably supplies a file.
 *
 * This mirrors real backend behavior (backend/pkg/executor/executor.go): `vars["file.*"]`
 * is only populated when a non-empty `resourcePath` reaches Executor.Run, and `currentPath`
 * for file actions comes from that same value. Only the File Event Trigger is guaranteed to
 * carry one — the SSE event manager always passes the actual path of the file that fired the
 * event (backend/pkg/sse/manager.go). A Schedule Trigger never does: the scheduler always
 * calls Run with an empty resourcePath (backend/pkg/scheduler/scheduler.go). A Manual Trigger
 * only *might*: "Run now" lets a user optionally type a WebDAV path into a free-text field,
 * but nothing about the graph guarantees it's filled in, so it's treated the same as having
 * no file source.
 *
 * If no trigger is reachable upstream at all (e.g. a disconnected/orphan node), this returns
 * true — there's nothing to flag against yet, and other validation should own that concern.
 */
export function hasUpstreamFileSource(nodeId: string, nodes: WorkflowNode[], edges: WorkflowEdge[]): boolean {
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
      return node.data.triggerType === 'event'
    }

    for (const source of incoming.get(id) ?? []) {
      queue.push(source)
    }
  }

  // No trigger found upstream at all.
  return true
}
