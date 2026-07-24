import type { WorkflowNode } from '../types/workflow'

export interface NodePosition {
  x: number
  y: number
}

// Horizontal gap between a source node and a newly connected node.
export const HORIZONTAL_STEP = 260

// Vertical spacing used both to stack nodes that have no source, and to
// cascade a new node further down when its preferred slot is already taken.
export const VERTICAL_STEP = 120

// Node cards are ~180px wide (see styles/canvas.css `min-width`); treat
// anything within this distance on either axis as "occupying" the slot.
const COLLISION_THRESHOLD_X = 180
const COLLISION_THRESHOLD_Y = VERTICAL_STEP

const isOccupied = (existingNodes: WorkflowNode[], candidate: NodePosition): boolean =>
  existingNodes.some(
    (n) =>
      Math.abs(n.position.x - candidate.x) < COLLISION_THRESHOLD_X &&
      Math.abs(n.position.y - candidate.y) < COLLISION_THRESHOLD_Y
  )

/**
 * Computes where a newly created node should be placed so it doesn't
 * overlap any existing node. When `sourceNode` is given, the candidate
 * position starts directly to the right of it and cascades downward in
 * `VERTICAL_STEP` increments until a free slot is found. When there is no
 * source, nodes stack vertically from the top-left corner.
 */
export const computeNewNodePosition = (
  existingNodes: WorkflowNode[],
  sourceNode: WorkflowNode | null | undefined
): NodePosition => {
  if (!sourceNode) {
    let candidate: NodePosition = { x: 40, y: 40 }
    while (isOccupied(existingNodes, candidate)) {
      candidate = { x: candidate.x, y: candidate.y + VERTICAL_STEP }
    }
    return candidate
  }

  let candidate: NodePosition = {
    x: sourceNode.position.x + HORIZONTAL_STEP,
    y: sourceNode.position.y
  }

  while (isOccupied(existingNodes, candidate)) {
    candidate = { x: candidate.x, y: candidate.y + VERTICAL_STEP }
  }

  return candidate
}
