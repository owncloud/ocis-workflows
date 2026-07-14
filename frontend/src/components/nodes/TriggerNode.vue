<template>
  <div class="workflows-node workflows-node-trigger">
    <div class="workflows-node-title">{{ $gettext('Trigger') }}</div>
    <div class="workflows-node-body">{{ data.label || $gettext('Manual') }}</div>
    <Handle type="source" :position="Position.Right" />
  </div>
</template>

<script lang="ts" setup>
import { Handle, Position } from '@vue-flow/core'
import { useGettext } from 'vue3-gettext'
import type { WorkflowNodeData } from '../../types/workflow'

defineProps<{ data: WorkflowNodeData }>()
const { $gettext } = useGettext()
</script>
