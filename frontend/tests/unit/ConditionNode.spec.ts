import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createGettext } from 'vue3-gettext'
import { VueFlow } from '@vue-flow/core'
import { defineComponent, h } from 'vue'
import ConditionNode from '../../src/components/nodes/ConditionNode.vue'
import type { WorkflowNode } from '../../src/types/workflow'

// ConditionNode (like every other canvas node) uses Vue Flow's <Handle>, which needs to
// be rendered inside a <VueFlow> instance with its node registered — mounting it in
// isolation throws ("Vue Flow: Node not found"). This mirrors how the app itself renders
// node components via WorkflowBuilder.vue's #node-condition slot.
const mountInFlow = async (node: WorkflowNode) => {
  const Wrapper = defineComponent({
    setup() {
      return () =>
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        h(VueFlow, { nodes: [node] }, { 'node-condition': (props: any) => h(ConditionNode, props) })
    }
  })
  const wrapper = mount(Wrapper, {
    global: { plugins: [createGettext({ availableLanguages: { en: 'English' }, defaultLanguage: 'en' })] }
  })
  // Vue Flow measures node DOM dimensions post-mount before it renders the node slot.
  await new Promise((resolve) => setTimeout(resolve, 50))
  await wrapper.vm.$nextTick()
  return wrapper
}

describe('ConditionNode', () => {
  it('renders a target handle and two source handles, one per branch', async () => {
    const node: WorkflowNode = {
      id: 'cond-1',
      type: 'condition',
      position: { x: 0, y: 0 },
      data: { left: '{{llm.output}}', operator: 'equals', right: 'invoice' }
    }
    const wrapper = await mountInFlow(node)

    expect(wrapper.find('[data-handleid="true"]').exists()).toBe(true)
    expect(wrapper.find('[data-handleid="false"]').exists()).toBe(true)
    expect(wrapper.findAll('.vue-flow__handle.target')).toHaveLength(1)
    expect(wrapper.findAll('.vue-flow__handle.source')).toHaveLength(2)
  })

  it('shows the configured comparison in the subtitle', async () => {
    const node: WorkflowNode = {
      id: 'cond-1',
      type: 'condition',
      position: { x: 0, y: 0 },
      data: { left: '{{llm.output}}', operator: 'contains', right: 'invoice' }
    }
    const wrapper = await mountInFlow(node)
    expect(wrapper.text()).toContain('{{llm.output}}')
    expect(wrapper.text()).toContain('contains')
    expect(wrapper.text()).toContain('invoice')
  })

  it('shows "Not configured" when the left value is empty', async () => {
    const node: WorkflowNode = {
      id: 'cond-1',
      type: 'condition',
      position: { x: 0, y: 0 },
      data: { left: '', operator: 'equals', right: '' }
    }
    const wrapper = await mountInFlow(node)
    expect(wrapper.text()).toContain('Not configured')
  })

  it('emits add-next with the branch handle id when a branch button is clicked', async () => {
    const node: WorkflowNode = {
      id: 'cond-1',
      type: 'condition',
      position: { x: 0, y: 0 },
      data: { left: '{{llm.output}}', operator: 'equals', right: 'invoice' }
    }
    const wrapper = await mountInFlow(node)
    const conditionNode = wrapper.findComponent(ConditionNode)

    await conditionNode.find('.workflows-node-add-button-true').trigger('click')
    await conditionNode.find('.workflows-node-add-button-false').trigger('click')

    expect(conditionNode.emitted('add-next')).toEqual([['true'], ['false']])
  })
})
