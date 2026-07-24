import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'
import { createGettext } from 'vue3-gettext'

// WorkflowBuilder pulls useRoute/useAuthStore from the host Web app's shared package.
// Outside of that host there is no router/store context to inject, so we stub the bits
// the component actually touches: a "new" route (no workflow to load) and a harmless
// auth store (no token needed since we never hit the network in this test).
vi.mock('@ownclouders/web-pkg', () => ({
  useRoute: () => ref({ params: { id: 'new' } }),
  useAuthStore: () => ({ accessToken: 'test-token' })
}))

// The canvas itself (pan/zoom/measuring DOM nodes) is irrelevant to this behavior and
// isn't meaningfully renderable under happy-dom, so it's stubbed out. Everything this
// test exercises — the empty-state "Add trigger" button, the node picker, and the
// details panel — lives outside of <VueFlow> in WorkflowBuilder's template.
vi.mock('@vue-flow/core', async () => {
  const actual = await vi.importActual<typeof import('@vue-flow/core')>('@vue-flow/core')
  return {
    ...actual,
    VueFlow: { template: '<div><slot /></div>' },
    useVueFlow: () => ({ addNodes: vi.fn(), addEdges: vi.fn(), fitView: vi.fn() })
  }
})
vi.mock('@vue-flow/background', () => ({ Background: { template: '<div />' } }))
vi.mock('@vue-flow/controls', () => ({ Controls: { template: '<div />' } }))
vi.mock('@vue-flow/minimap', () => ({ MiniMap: { template: '<div />' } }))

import WorkflowBuilder from '../../src/views/WorkflowBuilder.vue'

describe('WorkflowBuilder', () => {
  it('opens the node details panel immediately after a new node is added', async () => {
    const wrapper = mount(WorkflowBuilder, {
      props: { id: 'new' },
      global: { plugins: [createGettext({ availableLanguages: { en: 'English' }, defaultLanguage: 'en' })] }
    })

    // No trigger yet: the empty-state "Add trigger" button is the entry point into the
    // node picker.
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)

    await wrapper.find('.workflows-empty-state oc-button').trigger('click')

    // Picking the "Manual Trigger" entry in the node picker should create the node...
    const pickerItem = wrapper.find('.workflows-picker-item[aria-label="Manual Trigger"]')
    expect(pickerItem.exists()).toBe(true)
    await pickerItem.trigger('click')

    // ...and, without any further clicks, its config modal should already be open.
    const panel = wrapper.find('[role="dialog"]')
    expect(panel.exists()).toBe(true)
    expect(panel.attributes('aria-label')).toBe('Manual Trigger')
  })
})
