<template>
  <div class="workflows-node workflows-node-llm">
    <Handle type="target" :position="Position.Left" />
    <label :for="`${props.id}-prompt`" class="workflows-node-title">{{ $gettext('LLM prompt') }}</label>
    <textarea
      :id="`${props.id}-prompt`"
      v-model="promptModel"
      class="workflows-node-prompt"
      rows="3"
      :placeholder="$gettext('Summarize {{file.content}}...')"
    />
    <Handle type="source" :position="Position.Right" />
  </div>
</template>

<script lang="ts" setup>
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import { useGettext } from 'vue3-gettext'
import type { WorkflowNodeData } from '../../types/workflow'

const props = defineProps<{ id: string; data: WorkflowNodeData }>()
const emit = defineEmits<{ (e: 'update', data: WorkflowNodeData): void }>()
const { $gettext } = useGettext()

const promptModel = computed({
  get: () => props.data.prompt ?? '',
  set: (value: string) => emit('update', { ...props.data, prompt: value })
})
</script>
