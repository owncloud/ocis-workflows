import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createGettext } from 'vue3-gettext'
import NodeDetailsPanel from '../../src/components/NodeDetailsPanel.vue'
import type { WorkflowNode } from '../../src/types/workflow'

const gettext = createGettext({ availableLanguages: { en: 'English' }, defaultLanguage: 'en' })

const mountPanel = (node: WorkflowNode) =>
  mount(NodeDetailsPanel, {
    props: { node },
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
