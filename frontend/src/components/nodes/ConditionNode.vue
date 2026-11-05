<template>
  <div
    class="workflows-node-card workflows-node-condition"
    role="button"
    tabindex="0"
    @click="$emit('configure')"
    @keydown.enter="$emit('configure')"
    @keydown.space.prevent="$emit('configure')"
  >
    <Handle type="target" :position="Position.Left" />
    <oc-icon name="git-branch" fill-type="line" />
    <div class="workflows-node-card-text">
      <span class="workflows-node-card-title">{{ $gettext('Condition') }}</span>
      <span class="workflows-node-card-subtitle">{{ subtitle }}</span>
    </div>

    <Handle id="true" type="source" :position="Position.Right" />
    <Handle id="false" type="source" :position="Position.Right" />

    <div class="workflows-node-branch-buttons">
      <button
        type="button"
        class="workflows-node-add-button workflows-node-add-button-true"
        :aria-label="$gettext('Add step for the true branch')"
        @click.stop="$emit('add-next', 'true')"
      >
        <span>{{ $gettext('True') }}</span>+
      </button>
      <button
        type="button"
        class="workflows-node-add-button workflows-node-add-button-false"
        :aria-label="$gettext('Add step for the false branch')"
        @click.stop="$emit('add-next', 'false')"
      >
        <span>{{ $gettext('False') }}</span>+
      </button>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import { useGettext } from 'vue3-gettext'
import type { ConditionOperator, WorkflowNodeData } from '../../types/workflow'

const props = defineProps<{ id: string; data: WorkflowNodeData }>()
defineEmits<{ (e: 'configure'): void; (e: 'add-next', handle: 'true' | 'false'): void }>()
const { $gettext } = useGettext()

const operatorSymbol: Record<ConditionOperator, string> = {
  equals: '==',
  notEquals: '!=',
  contains: 'contains',
  notContains: 'not contains',
  matches: 'matches'
}

const subtitle = computed(() => {
  const { left, operator, right } = props.data
  if (!left) {
    return $gettext('Not configured')
  }
  const symbol = operatorSymbol[operator ?? 'equals']
  return `${left} ${symbol} ${right ?? ''}`.trim()
})
</script>
