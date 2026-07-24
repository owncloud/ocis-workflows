import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createGettext } from 'vue3-gettext'
import NodePicker from '../../src/components/NodePicker.vue'
import type { WorkflowNode } from '../../src/types/workflow'

const stubs = {
  'oc-icon': true,
  'oc-button': true,
  'oc-text-input': true
}

const gettext = createGettext({ availableLanguages: { en: 'English' }, defaultLanguage: 'en' })

const mountPicker = (sourceNodeId: string | null, nodes: WorkflowNode[]) =>
  mount(NodePicker, {
    props: { sourceNodeId, nodes, edges: [] },
    global: { plugins: [gettext], stubs }
  })

const findItemButton = (wrapper: ReturnType<typeof mountPicker>, label: string) =>
  wrapper.findAll('.workflows-picker-item').find((btn) => btn.text().includes(label))

describe('NodePicker file-source restriction', () => {
  // A Manual Trigger's "Run now" flow can supply a file via its free-text WebDAV-path field
  // (backend/pkg/service/workflows.go's runRequest.ResourcePath), so it must NOT be hard-blocked
  // in the picker — that's a legitimate, common workflow shape. Only a Schedule Trigger is
  // structurally incapable of ever providing a file (the scheduler always calls
  // Run(..., "schedule", "") — backend/pkg/scheduler/scheduler.go).
  it('does not disable Move File when the source is a manual trigger', () => {
    const nodes: WorkflowNode[] = [
      { id: 'trigger', type: 'trigger', position: { x: 0, y: 0 }, data: { triggerType: 'manual' } }
    ]
    const wrapper = mountPicker('trigger', nodes)

    const moveButton = findItemButton(wrapper, 'Move File')
    expect(moveButton).toBeTruthy()
    expect(moveButton!.attributes('disabled')).toBeUndefined()
  })

  it('disables file-dependent actions (e.g. Move File) when the source is a schedule trigger', () => {
    const nodes: WorkflowNode[] = [
      { id: 'trigger', type: 'trigger', position: { x: 0, y: 0 }, data: { triggerType: 'schedule', schedule: '0 * * * *' } }
    ]
    const wrapper = mountPicker('trigger', nodes)

    const moveButton = findItemButton(wrapper, 'Move File')
    expect(moveButton).toBeTruthy()
    expect(moveButton!.attributes('disabled')).toBeDefined()
    expect(moveButton!.text()).toMatch(/requires a file/i)
  })

  it('does not disable Move File when the source descends from a File Event Trigger', () => {
    const nodes: WorkflowNode[] = [
      {
        id: 'trigger',
        type: 'trigger',
        position: { x: 0, y: 0 },
        data: { triggerType: 'event', event: { type: 'upload' } }
      }
    ]
    const wrapper = mountPicker('trigger', nodes)

    const moveButton = findItemButton(wrapper, 'Move File')
    expect(moveButton).toBeTruthy()
    expect(moveButton!.attributes('disabled')).toBeUndefined()
  })

  it('does not fire select when clicking a disabled entry', async () => {
    const nodes: WorkflowNode[] = [
      { id: 'trigger', type: 'trigger', position: { x: 0, y: 0 }, data: { triggerType: 'schedule', schedule: '0 * * * *' } }
    ]
    const wrapper = mountPicker('trigger', nodes)

    const moveButton = findItemButton(wrapper, 'Move File')
    await moveButton!.trigger('click')

    expect(wrapper.emitted('select')).toBeUndefined()
  })

  it('leaves non-file actions (e.g. Send Notification) enabled regardless of upstream trigger', () => {
    const nodes: WorkflowNode[] = [
      { id: 'trigger', type: 'trigger', position: { x: 0, y: 0 }, data: { triggerType: 'schedule', schedule: '0 * * * *' } }
    ]
    const wrapper = mountPicker('trigger', nodes)

    const notifyButton = findItemButton(wrapper, 'Send Notification')
    expect(notifyButton).toBeTruthy()
    expect(notifyButton!.attributes('disabled')).toBeUndefined()
  })
})
