<template>
  <div class="workflows-node workflows-node-action">
    <Handle type="target" :position="Position.Left" />
    <label :for="`${props.id}-action-type`" class="workflows-node-title">{{ $gettext('Action') }}</label>
    <select :id="`${props.id}-action-type`" v-model="actionTypeModel" class="workflows-node-select">
      <option value="tag">{{ $gettext('Add tag') }}</option>
      <option value="comment">{{ $gettext('Add comment') }}</option>
      <option value="move">{{ $gettext('Move file') }}</option>
      <option value="copy">{{ $gettext('Copy file') }}</option>
      <option value="rename">{{ $gettext('Rename file') }}</option>
      <option value="notify">{{ $gettext('Send notification') }}</option>
    </select>
  </div>
</template>

<script lang="ts" setup>
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import { useGettext } from 'vue3-gettext'
import type { ActionType, WorkflowNodeData } from '../../types/workflow'

const props = defineProps<{ id: string; data: WorkflowNodeData }>()
const emit = defineEmits<{ (e: 'update', data: WorkflowNodeData): void }>()
const { $gettext } = useGettext()

const actionTypeModel = computed({
  get: () => props.data.actionType ?? 'tag',
  set: (value: ActionType) => emit('update', { ...props.data, actionType: value })
})
</script>
