<template>
  <div
    class="workflows-node-card workflows-node-trigger"
    role="button"
    tabindex="0"
    @click="$emit('configure')"
    @keydown.enter="$emit('configure')"
    @keydown.space.prevent="$emit('configure')"
  >
    <oc-icon :name="nodeType?.icon ?? 'play-circle'" fill-type="line" />
    <div class="workflows-node-card-text">
      <span class="workflows-node-card-title">{{ nodeType?.label ?? $gettext('Trigger') }}</span>
      <span class="workflows-node-card-subtitle">{{ subtitle }}</span>
    </div>
    <Handle type="source" :position="Position.Right" />
    <NodeAddButton @add-next="$emit('add-next')" />
  </div>
</template>

<script lang="ts" setup>
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import { useGettext } from 'vue3-gettext'
import NodeAddButton from './NodeAddButton.vue'
import { findNodeTypeForNode } from '../../nodeTypes'
import type { WorkflowNodeData } from '../../types/workflow'

const props = defineProps<{ id: string; data: WorkflowNodeData }>()
defineEmits<{ (e: 'configure'): void; (e: 'add-next'): void }>()
const { $gettext } = useGettext()

const nodeType = computed(() => findNodeTypeForNode('trigger', props.data.triggerType))
const subtitle = computed(() => {
  if (props.data.triggerType === 'schedule') return props.data.schedule || $gettext('Schedule')
  if (props.data.triggerType === 'event') return props.data.event?.type ?? $gettext('File event')
  return $gettext('Manual')
})
</script>
