import { describe, expect, it } from 'vitest'

import { computeNewNodePosition } from '../../src/utils/nodeLayout'
import type { WorkflowNode } from '../../src/types/workflow'

const makeNode = (id: string, x: number, y: number): WorkflowNode => ({
  id,
  type: 'action',
  position: { x, y },
  data: {}
})

describe('computeNewNodePosition', () => {
  it('returns the straightforward position when nothing occupies the candidate spot', () => {
    const source = makeNode('source', 100, 200)
    const existingNodes: WorkflowNode[] = [source]

    const position = computeNewNodePosition(existingNodes, source)

    expect(position).toEqual({ x: 360, y: 200 })
  })

  it('moves the position down by one step when the candidate spot is already occupied', () => {
    const source = makeNode('source', 100, 200)
    const blocker = makeNode('blocker', 360, 200)
    const existingNodes: WorkflowNode[] = [source, blocker]

    const position = computeNewNodePosition(existingNodes, source)

    expect(position.x).toBe(360)
    expect(position.y).toBe(320)
  })

  it('cascades further down when two nodes are already stacked below the source', () => {
    const source = makeNode('source', 100, 200)
    const blocker1 = makeNode('blocker1', 360, 200)
    const blocker2 = makeNode('blocker2', 360, 320)
    const existingNodes: WorkflowNode[] = [source, blocker1, blocker2]

    const position = computeNewNodePosition(existingNodes, source)

    expect(position.x).toBe(360)
    expect(position.y).toBe(440)
  })

  it('is not affected by nodes that are far away and do not overlap', () => {
    const source = makeNode('source', 100, 200)
    const farAway = makeNode('far', 900, 900)
    const sameXDifferentY = makeNode('sameXFar', 360, 900)
    const existingNodes: WorkflowNode[] = [source, farAway, sameXDifferentY]

    const position = computeNewNodePosition(existingNodes, source)

    expect(position).toEqual({ x: 360, y: 200 })
  })
})
