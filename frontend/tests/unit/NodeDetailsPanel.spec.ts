import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createGettext } from 'vue3-gettext'
import NodeDetailsPanel from '../../src/components/NodeDetailsPanel.vue'
import type { WorkflowEdge, WorkflowNode } from '../../src/types/workflow'

const gettext = createGettext({ availableLanguages: { en: 'English' }, defaultLanguage: 'en' })

const mountPanel = (node: WorkflowNode, nodes: WorkflowNode[] = [], edges: WorkflowEdge[] = []) =>
  mount(NodeDetailsPanel, {
    props: { node, nodes, edges },
    global: {
      plugins: [gettext]
    }
  })

describe('NodeDetailsPanel', () => {
  const sampleNode: WorkflowNode = {
    id: 'action-1',
    type: 'action',
    position: { x: 0, y: 0 },
    data: { actionType: 'tag', actionParams: { tag: 'reviewed' } }
  }

  it('renders a confirm/close button so the user has an explicit way to finish configuring the node', () => {
    const wrapper = mountPanel(sampleNode)

    const confirmButton = wrapper.findAll('oc-button').find((button) => button.text() === 'Done')

    expect(confirmButton).toBeTruthy()
  })

  it('emits "close" when the confirm button is clicked', async () => {
    const wrapper = mountPanel(sampleNode)

    const confirmButton = wrapper.findAll('oc-button').find((button) => button.text() === 'Done')
    await confirmButton!.trigger('click')

    expect(wrapper.emitted('close')).toBeTruthy()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})

// oc-* components come from the host web app's design system and aren't registered in
// this unit-test environment; stub them with minimal look-alikes so v-model bindings
// and the props we assert on (label/placeholder) behave predictably.
const globalStubs = {
  'oc-icon': true,
  'oc-button': true,
  'oc-text-input': {
    props: ['modelValue', 'label', 'placeholder', 'descriptionMessage'],
    emits: ['update:modelValue'],
    template:
      '<input :aria-label="label" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
  }
}

const extractTextNode: WorkflowNode = {
  id: 'extract-1',
  type: 'extractText',
  position: { x: 0, y: 0 },
  data: {}
}

describe('NodeDetailsPanel — Extract Text node', () => {
  it('renders an optional output-variable-override field defaulting to file.text', () => {
    const wrapper = mount(NodeDetailsPanel, {
      props: { node: extractTextNode, nodes: [], edges: [] },
      global: { stubs: globalStubs, plugins: [gettext] }
    })

    const input = wrapper.find('input[placeholder="file.text"]')
    expect(input.exists()).toBe(true)
  })

  it('emits an update with the custom outputVariable when the field changes', async () => {
    const wrapper = mount(NodeDetailsPanel, {
      props: { node: extractTextNode, nodes: [], edges: [] },
      global: { stubs: globalStubs, plugins: [gettext] }
    })

    const input = wrapper.find('input[placeholder="file.text"]')
    await input.setValue('myCustomVar')

    const emitted = wrapper.emitted('update')
    expect(emitted).toBeTruthy()
    const lastPayload = emitted![emitted!.length - 1][0] as { outputVariable?: string }
    expect(lastPayload.outputVariable).toBe('myCustomVar')
  })
})

describe('NodeDetailsPanel file-source warning', () => {
  const moveAction: WorkflowNode = {
    id: 'action-1',
    type: 'action',
    position: { x: 0, y: 0 },
    data: { actionType: 'move' }
  }

  it('warns when configuring a move action fed only by a manual trigger', () => {
    const nodes: WorkflowNode[] = [
      { id: 'trigger', type: 'trigger', position: { x: 0, y: 0 }, data: { triggerType: 'manual' } },
      moveAction
    ]
    const edges: WorkflowEdge[] = [{ id: 'e1', source: 'trigger', target: 'action-1' }]

    const wrapper = mountPanel(moveAction, nodes, edges)

    expect(wrapper.find('.workflows-ndv-warning').exists()).toBe(true)
  })

  it('does not warn when configuring a move action fed by a File Event Trigger', () => {
    const nodes: WorkflowNode[] = [
      {
        id: 'trigger',
        type: 'trigger',
        position: { x: 0, y: 0 },
        data: { triggerType: 'event', event: { type: 'upload' } }
      },
      moveAction
    ]
    const edges: WorkflowEdge[] = [{ id: 'e1', source: 'trigger', target: 'action-1' }]

    const wrapper = mountPanel(moveAction, nodes, edges)

    expect(wrapper.find('.workflows-ndv-warning').exists()).toBe(false)
  })

  it('does not warn for a non-file-dependent action such as notify', () => {
    const notifyAction: WorkflowNode = {
      id: 'action-1',
      type: 'action',
      position: { x: 0, y: 0 },
      data: { actionType: 'notify' }
    }
    const nodes: WorkflowNode[] = [
      { id: 'trigger', type: 'trigger', position: { x: 0, y: 0 }, data: { triggerType: 'manual' } },
      notifyAction
    ]
    const edges: WorkflowEdge[] = [{ id: 'e1', source: 'trigger', target: 'action-1' }]

    const wrapper = mountPanel(notifyAction, nodes, edges)

    expect(wrapper.find('.workflows-ndv-warning').exists()).toBe(false)
  })
})

// oc-* components come from ownCloud's design system, registered globally by the host shell
// at runtime. They aren't available in this unit test environment, so they're stubbed out —
// we only care about the plain-HTML "Output" hint markup this panel renders itself.
const outputHintStubs = {
  'oc-icon': true,
  'oc-button': true,
  'oc-text-input': true
}

function mountOutputHintPanel(node: WorkflowNode) {
  return mount(NodeDetailsPanel, {
    props: { node, nodes: [], edges: [] },
    global: {
      plugins: [createGettext({ translations: {} })],
      stubs: outputHintStubs
    }
  })
}

describe('NodeDetailsPanel output hint', () => {
  it('shows {{llm.output}} for an LLM node', () => {
    const node: WorkflowNode = {
      id: 'llm-1',
      type: 'llm',
      position: { x: 0, y: 0 },
      data: { prompt: 'Summarize {{file.content}}' }
    }
    const wrapper = mountOutputHintPanel(node)
    const text = wrapper.text()
    expect(text).toContain('Output')
    expect(text).toContain('{{llm.output}}')
  })

  it('shows {{tag.output}} for a tag action node, matching the executor-wired vars key', () => {
    const node: WorkflowNode = {
      id: 'action-1',
      type: 'action',
      position: { x: 0, y: 0 },
      data: { actionType: 'tag' }
    }
    const wrapper = mountOutputHintPanel(node)
    const text = wrapper.text()
    expect(text).toContain('Output')
    expect(text).toContain('{{tag.output}}')
  })

  it('shows {{move.output}} for a move action node', () => {
    const node: WorkflowNode = {
      id: 'action-2',
      type: 'action',
      position: { x: 0, y: 0 },
      data: { actionType: 'move' }
    }
    const wrapper = mountOutputHintPanel(node)
    expect(wrapper.text()).toContain('{{move.output}}')
  })

  it('shows {{notify.output}} for a notify action node', () => {
    const node: WorkflowNode = {
      id: 'action-3',
      type: 'action',
      position: { x: 0, y: 0 },
      data: { actionType: 'notify' }
    }
    const wrapper = mountOutputHintPanel(node)
    expect(wrapper.text()).toContain('{{notify.output}}')
  })

  it('shows {{file.name}} and {{file.content}} for a trigger node', () => {
    const node: WorkflowNode = {
      id: 'trigger-1',
      type: 'trigger',
      position: { x: 0, y: 0 },
      data: { triggerType: 'event', event: { type: 'upload' } }
    }
    const wrapper = mountOutputHintPanel(node)
    const text = wrapper.text()
    expect(text).toContain('{{file.name}}')
    expect(text).toContain('{{file.content}}')
  })
})

const mountPanelWithStubs = (node: WorkflowNode) =>
  mount(NodeDetailsPanel, {
    props: { node, nodes: [], edges: [] },
    global: {
      plugins: [gettext],
      stubs: {
        'oc-icon': true,
        'oc-button': true,
        'oc-text-input': {
          template:
            '<input :id="$attrs.id" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
          props: ['modelValue', 'label', 'placeholder', 'descriptionMessage']
        }
      }
    }
  })

describe('NodeDetailsPanel - share action', () => {
  const shareNode: WorkflowNode = {
    id: 'action-1',
    type: 'action',
    position: { x: 0, y: 0 },
    data: { actionType: 'share', actionParams: { recipient: 'accounting@example.com', role: 'editor' } }
  }

  it('renders a text input wired to actionParams.recipient', async () => {
    const wrapper = mountPanelWithStubs(shareNode)

    const input = wrapper.find('input')
    expect(input.exists()).toBe(true)
    expect(input.element.value).toBe('accounting@example.com')

    await input.setValue('new-recipient@example.com')
    const updateEvents = wrapper.emitted('update')
    expect(updateEvents).toBeTruthy()
    const lastUpdate = updateEvents![updateEvents!.length - 1][0] as { actionParams?: Record<string, unknown> }
    expect(lastUpdate.actionParams?.recipient).toBe('new-recipient@example.com')
  })

  it('renders a role select wired to actionParams.role, defaulting to viewer', async () => {
    const wrapper = mountPanelWithStubs(shareNode)

    const select = wrapper.find('#ndv-role')
    expect(select.exists()).toBe(true)
    expect((select.element as HTMLSelectElement).value).toBe('editor')

    await select.setValue('viewer')
    const updateEvents = wrapper.emitted('update')
    const lastUpdate = updateEvents![updateEvents!.length - 1][0] as { actionParams?: Record<string, unknown> }
    expect(lastUpdate.actionParams?.role).toBe('viewer')
  })

  it('defaults the role field to viewer when unset', () => {
    const node: WorkflowNode = {
      id: 'action-2',
      type: 'action',
      position: { x: 0, y: 0 },
      data: { actionType: 'share', actionParams: {} }
    }
    const wrapper = mountPanelWithStubs(node)
    const select = wrapper.find('#ndv-role')
    expect((select.element as HTMLSelectElement).value).toBe('viewer')
  })
})
