import { describe, expect, it } from 'vitest'
import type { WorkflowGraph } from '../../src/types/workflow'

describe('Workflows app', () => {
  it('models a workflow graph with a single trigger, an LLM step, and an action', () => {
    const graph: WorkflowGraph = {
      nodes: [
        { id: 'trigger', type: 'trigger', position: { x: 0, y: 0 }, data: {} },
        { id: 'llm-1', type: 'llm', position: { x: 200, y: 0 }, data: { prompt: 'Summarize' } },
        { id: 'action-1', type: 'action', position: { x: 400, y: 0 }, data: { actionType: 'tag' } }
      ],
      edges: [
        { id: 'e1', source: 'trigger', target: 'llm-1' },
        { id: 'e2', source: 'llm-1', target: 'action-1' }
      ]
    }

    expect(graph.nodes).toHaveLength(3)
    expect(graph.nodes.filter((n) => n.type === 'trigger')).toHaveLength(1)
    expect(graph.edges.map((e) => e.source)).toEqual(['trigger', 'llm-1'])
  })
})
