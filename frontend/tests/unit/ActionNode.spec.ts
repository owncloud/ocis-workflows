import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createGettext } from 'vue3-gettext'
import ActionNode from '../../src/components/nodes/ActionNode.vue'

const gettext = createGettext({ availableLanguages: { en: 'English' }, defaultLanguage: 'en' })

describe('ActionNode', () => {
  it('renders the delete action with its label and "Not configured" subtitle (no params)', () => {
    const wrapper = mount(ActionNode, {
      props: { id: 'a', data: { actionType: 'delete' } },
      global: {
        plugins: [gettext],
        stubs: { 'oc-icon': true, Handle: true }
      }
    })

    expect(wrapper.find('.workflows-node-card-title').text()).toBe('Delete File')
    expect(wrapper.find('.workflows-node-card-subtitle').text()).toBe('Not configured')
  })
})
