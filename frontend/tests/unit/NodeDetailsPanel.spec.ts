import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
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

const getWebhookToken = vi.fn()
const rotateWebhookToken = vi.fn()
vi.mock('../../src/composables/useWorkflowsApi', () => ({
  useWorkflowsApi: () => ({ getWebhookToken, rotateWebhookToken })
}))

const webhookNode = (): WorkflowNode => ({
  id: 'trigger',
  type: 'trigger',
  position: { x: 0, y: 0 },
  data: { triggerType: 'webhook' }
})

describe('NodeDetailsPanel — webhook trigger', () => {
  beforeEach(() => {
    getWebhookToken.mockReset()
    rotateWebhookToken.mockReset()
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true
    })
  })

  it('prompts to save first when the workflow has no id yet', () => {
    const wrapper = mount(NodeDetailsPanel, {
      props: { node: webhookNode(), nodes: [], edges: [], workflowId: '', backendUrl: 'https://example.test/workflows/api/v1beta1' },
      global: { plugins: [gettext] }
    })

    expect(wrapper.text()).toContain('Save the workflow to generate its webhook URL.')
    expect(getWebhookToken).not.toHaveBeenCalled()
  })

  it('treats the "new" route param the same as an unsaved workflow', () => {
    const wrapper = mount(NodeDetailsPanel, {
      props: { node: webhookNode(), nodes: [], edges: [], workflowId: 'new', backendUrl: 'https://example.test/workflows/api/v1beta1' },
      global: { plugins: [gettext] }
    })

    expect(wrapper.text()).toContain('Save the workflow to generate its webhook URL.')
    expect(getWebhookToken).not.toHaveBeenCalled()
  })

  it('fetches and shows a masked URL once the workflow is saved, revealing it on demand', async () => {
    getWebhookToken.mockResolvedValue({ token: 'abc123', url: 'https://example.test/workflows/hooks/wf-1/abc123' })

    const wrapper = mount(NodeDetailsPanel, {
      props: { node: webhookNode(), nodes: [], edges: [], workflowId: 'wf-1', backendUrl: 'https://example.test/workflows/api/v1beta1' },
      global: { plugins: [gettext] }
    })
    await flushAll()

    expect(getWebhookToken).toHaveBeenCalledWith('wf-1')
    const value = wrapper.get('[data-test="webhook-url"]')
    expect(value.text()).not.toContain('abc123')
    expect(value.text()).toMatch(/•+/)

    await wrapper.get('[data-test="webhook-reveal"]').trigger('click')
    await flushAll()

    expect(wrapper.get('[data-test="webhook-url"]').text()).toBe('https://example.test/workflows/hooks/wf-1/abc123')
  })

  it('copies the URL to the clipboard without requiring reveal first', async () => {
    getWebhookToken.mockResolvedValue({ token: 'abc123', url: 'https://example.test/workflows/hooks/wf-1/abc123' })

    const wrapper = mount(NodeDetailsPanel, {
      props: { node: webhookNode(), nodes: [], edges: [], workflowId: 'wf-1', backendUrl: 'https://example.test/workflows/api/v1beta1' },
      global: { plugins: [gettext] }
    })
    await flushAll()

    await wrapper.get('[data-test="webhook-copy"]').trigger('click')
    await flushAll()

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('https://example.test/workflows/hooks/wf-1/abc123')
    // Copying must not force-reveal the value in the UI.
    expect(wrapper.get('[data-test="webhook-url"]').text()).not.toContain('abc123')
  })

  it('rotates the token, replacing the displayed value', async () => {
    getWebhookToken.mockResolvedValue({ token: 'abc123', url: 'https://example.test/workflows/hooks/wf-1/abc123' })
    rotateWebhookToken.mockResolvedValue({ token: 'rotated', url: 'https://example.test/workflows/hooks/wf-1/rotated' })

    const wrapper = mount(NodeDetailsPanel, {
      props: { node: webhookNode(), nodes: [], edges: [], workflowId: 'wf-1', backendUrl: 'https://example.test/workflows/api/v1beta1' },
      global: { plugins: [gettext] }
    })
    await flushAll()

    await wrapper.get('[data-test="webhook-rotate"]').trigger('click')
    await flushAll()

    expect(rotateWebhookToken).toHaveBeenCalledWith('wf-1')
    expect(wrapper.get('[data-test="webhook-url"]').text()).toBe('https://example.test/workflows/hooks/wf-1/rotated')
  })
})

// A couple of microtask ticks: one for the API promise, one for Vue's reactivity flush.
const flushAll = async () => {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}
