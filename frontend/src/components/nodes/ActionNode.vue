<template>
  <div
    class="workflows-node-card workflows-node-action"
    role="button"
    tabindex="0"
    @click="$emit('configure')"
    @keydown.enter="$emit('configure')"
    @keydown.space.prevent="$emit('configure')"
  >
    <Handle type="target" :position="Position.Left" />
    <oc-icon :name="nodeType?.icon ?? 'flashlight'" fill-type="line" />
    <div class="workflows-node-card-text">
      <span class="workflows-node-card-title">{{ nodeType?.label ?? $gettext('Action') }}</span>
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

const nodeType = computed(() => findNodeTypeForNode('action', props.data.actionType))
const subtitle = computed(() => {
  const params = props.data.actionParams ?? {}
  const first = Object.values(params).find((v) => typeof v === 'string' && v)
  return (first as string) || $gettext('Not configured')
})
</script>
