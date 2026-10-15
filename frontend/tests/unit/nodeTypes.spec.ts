import { describe, expect, it } from 'vitest'
import {
  ACTION_CATEGORY,
  AI_CATEGORY,
  LOGIC_CATEGORY,
  NODE_TYPES,
  NOTIFICATION_CATEGORY,
  TRIGGER_CATEGORY,
  findNodeType,
  findNodeTypeForNode,
  type CanvasNodeKind
} from '../../src/nodeTypes'

describe('node type categorization', () => {
  it('gives Send Notification its own category, distinct from the file-manipulation actions', () => {
    const notify = findNodeType('action-notify')

    expect(notify?.category).toBe(NOTIFICATION_CATEGORY)
    expect(notify?.category).not.toBe(ACTION_CATEGORY)
  })

  it('keeps the file-manipulation actions (tag, comment, move, copy, rename) under Actions', () => {
    const fileActionIds = ['action-tag', 'action-comment', 'action-move', 'action-copy', 'action-rename']

    for (const id of fileActionIds) {
      expect(findNodeType(id)?.category).toBe(ACTION_CATEGORY)
    }
  })

  it('surfaces four distinct non-trigger categories for the node picker to group by', () => {
    const nonTriggerCategories = new Set(
      NODE_TYPES.filter((t) => t.category !== TRIGGER_CATEGORY).map((t) => t.category)
    )

    expect(nonTriggerCategories).toEqual(
      new Set([AI_CATEGORY, LOGIC_CATEGORY, ACTION_CATEGORY, NOTIFICATION_CATEGORY])
    )
  })
})

describe('nodeTypes', () => {
  it('declares "condition" as a valid CanvasNodeKind', () => {
    // Compile-time check: this assignment only type-checks if 'condition' is part of
    // the CanvasNodeKind union. If it isn't, `npm run check:types` fails on this line.
    const kind: CanvasNodeKind = 'condition'
    expect(kind).toBe('condition')
  })

  it('registers a picker entry for the condition node', () => {
    const entry = findNodeType('condition')
    expect(entry).toBeDefined()
    expect(entry?.nodeKind).toBe('condition')
    expect(entry?.category).toBe(LOGIC_CATEGORY)
  })

  it('defaults condition node data to a comparison with the "equals" operator', () => {
    const entry = findNodeType('condition')
    expect(entry?.defaultData).toEqual({ left: '', operator: 'equals', right: '' })
  })

  it('resolves the condition node type for an existing canvas node', () => {
    const resolved = findNodeTypeForNode('condition')
    expect(resolved?.id).toBe('condition')
  })

  it('only ever registers one condition node type entry', () => {
    expect(NODE_TYPES.filter((t) => t.nodeKind === 'condition')).toHaveLength(1)
  })
})

describe('nodeTypes', () => {
  it('registers an Extract Text node in the AI category', () => {
    const entry = NODE_TYPES.find((t) => t.nodeKind === 'extractText')

    expect(entry).toBeDefined()
    expect(entry?.category).toBe(AI_CATEGORY)
    expect(entry?.icon).toBeTruthy()
    expect(entry?.label).toBeTruthy()
  })

  it('resolves the Extract Text node type definition for a canvas node of that kind', () => {
    const found = findNodeTypeForNode('extractText')

    expect(found).toBeDefined()
    expect(found?.nodeKind).toBe('extractText')
  })
})
