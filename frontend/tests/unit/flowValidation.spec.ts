import { describe, expect, it } from 'vitest'
import { canUpstreamProvideFile, hasUpstreamFileSource, isFileDependentActionType } from '../../src/utils/flowValidation'
import type { WorkflowEdge, WorkflowNode } from '../../src/types/workflow'

const trigger = (id: string, data: WorkflowNode['data']): WorkflowNode => ({
  id,
  type: 'trigger',
  position: { x: 0, y: 0 },
  data
})
const llm = (id: string): WorkflowNode => ({
  id,
  type: 'llm',
  position: { x: 0, y: 0 },
  data: { prompt: 'summarize {{file.content}}' }
})
const action = (id: string, actionType: 'move' | 'tag'): WorkflowNode => ({
  id,
  type: 'action',
  position: { x: 0, y: 0 },
  data: { actionType }
})
const edge = (source: string, target: string): WorkflowEdge => ({ id: `${source}-${target}`, source, target })

// Only the File Event Trigger reliably supplies a file to the run's template-variable
// context (backend/pkg/executor/executor.go only fills `vars["file.*"]` when a non-empty
// resourcePath is passed to Executor.Run, and only the SSE event path always provides one:
// - the scheduler always calls Run(..., "schedule", "") — resourcePath is hardcoded empty.
// - the manual "Run now" panel sends whatever the user optionally typed into a free-text
//   field, so it isn't guaranteed either.
// - the SSE event manager passes the actual file path from the triggering event.
describe('hasUpstreamFileSource', () => {
  it('flags a manual trigger chained directly into a move-file action', () => {
    const nodes = [trigger('trigger', { triggerType: 'manual' }), action('action-1', 'move')]
    const edges = [edge('trigger', 'action-1')]

    expect(hasUpstreamFileSource('action-1', nodes, edges)).toBe(false)
  })

  it('does not flag an event trigger chained directly into a move-file action', () => {
    const nodes = [trigger('trigger', { triggerType: 'event', event: { type: 'upload' } }), action('action-1', 'move')]
    const edges = [edge('trigger', 'action-1')]

    expect(hasUpstreamFileSource('action-1', nodes, edges)).toBe(true)
  })

  it('still flags manual trigger -> llm -> move-file (no file anywhere upstream)', () => {
    const nodes = [trigger('trigger', { triggerType: 'manual' }), llm('llm-1'), action('action-1', 'move')]
    const edges = [edge('trigger', 'llm-1'), edge('llm-1', 'action-1')]

    expect(hasUpstreamFileSource('action-1', nodes, edges)).toBe(false)
  })

  it('does not flag event trigger -> llm -> move-file', () => {
    const nodes = [
      trigger('trigger', { triggerType: 'event', event: { type: 'upload' } }),
      llm('llm-1'),
      action('action-1', 'move')
    ]
    const edges = [edge('trigger', 'llm-1'), edge('llm-1', 'action-1')]

    expect(hasUpstreamFileSource('action-1', nodes, edges)).toBe(true)
  })

  it('flags a schedule trigger too, since the scheduler always runs with an empty resourcePath', () => {
    const nodes = [trigger('trigger', { triggerType: 'schedule', schedule: '0 * * * *' }), action('action-1', 'move')]
    const edges = [edge('trigger', 'action-1')]

    expect(hasUpstreamFileSource('action-1', nodes, edges)).toBe(false)
  })

  it('returns true for a node with no upstream trigger at all (nothing to flag against)', () => {
    const nodes = [action('action-1', 'move')]
    expect(hasUpstreamFileSource('action-1', nodes, [])).toBe(true)
  })
})

// canUpstreamProvideFile is the looser check used to decide *hard* restrictions (e.g. disabling
// node-picker entries). Unlike hasUpstreamFileSource, it must NOT flag a Manual Trigger — the
// "Run now" flow can supply a file via its free-text WebDAV-path field, so Manual Trigger +
// file action is a legitimate, common workflow shape that a hard block would incorrectly break.
// Only a Schedule Trigger is structurally incapable of ever providing one.
describe('canUpstreamProvideFile', () => {
  it('does not flag a manual trigger chained directly into a move-file action', () => {
    const nodes = [trigger('trigger', { triggerType: 'manual' }), action('action-1', 'move')]
    const edges = [edge('trigger', 'action-1')]

    expect(canUpstreamProvideFile('action-1', nodes, edges)).toBe(true)
  })

  it('does not flag manual trigger -> llm -> move-file', () => {
    const nodes = [trigger('trigger', { triggerType: 'manual' }), llm('llm-1'), action('action-1', 'move')]
    const edges = [edge('trigger', 'llm-1'), edge('llm-1', 'action-1')]

    expect(canUpstreamProvideFile('action-1', nodes, edges)).toBe(true)
  })

  it('does not flag an event trigger chained directly into a move-file action', () => {
    const nodes = [trigger('trigger', { triggerType: 'event', event: { type: 'upload' } }), action('action-1', 'move')]
    const edges = [edge('trigger', 'action-1')]

    expect(canUpstreamProvideFile('action-1', nodes, edges)).toBe(true)
  })

  it('flags a schedule trigger chained directly into a move-file action', () => {
    const nodes = [trigger('trigger', { triggerType: 'schedule', schedule: '0 * * * *' }), action('action-1', 'move')]
    const edges = [edge('trigger', 'action-1')]

    expect(canUpstreamProvideFile('action-1', nodes, edges)).toBe(false)
  })

  it('still flags schedule trigger -> llm -> move-file', () => {
    const nodes = [trigger('trigger', { triggerType: 'schedule', schedule: '0 * * * *' }), llm('llm-1'), action('action-1', 'move')]
    const edges = [edge('trigger', 'llm-1'), edge('llm-1', 'action-1')]

    expect(canUpstreamProvideFile('action-1', nodes, edges)).toBe(false)
  })

  it('returns true for a node with no upstream trigger at all (nothing to flag against)', () => {
    const nodes = [action('action-1', 'move')]
    expect(canUpstreamProvideFile('action-1', nodes, [])).toBe(true)
  })
})

describe('isFileDependentActionType', () => {
  it('flags actions that operate on a target file', () => {
    expect(isFileDependentActionType('tag')).toBe(true)
    expect(isFileDependentActionType('comment')).toBe(true)
    expect(isFileDependentActionType('move')).toBe(true)
    expect(isFileDependentActionType('copy')).toBe(true)
    expect(isFileDependentActionType('rename')).toBe(true)
  })

  it('does not flag notify, which needs no target file', () => {
    expect(isFileDependentActionType('notify')).toBe(false)
  })
})
