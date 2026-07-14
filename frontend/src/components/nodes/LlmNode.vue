<template>
  <div
    class="workflows-node-card workflows-node-llm"
    role="button"
    tabindex="0"
    @click="$emit('configure')"
    @keydown.enter="$emit('configure')"
    @keydown.space.prevent="$emit('configure')"
  >
    <Handle type="target" :position="Position.Left" />
    <oc-icon name="chat-3-line" />
    <div class="workflows-node-card-text">
      <span class="workflows-node-card-title">{{ $gettext('LLM Prompt') }}</span>
      <span class="workflows-node-card-subtitle">{{ subtitle }}</span>
    </div>
    <Handle type="source" :position="Position.Right" />
    <button
      type="button"
      class="workflows-node-add-button"
      :aria-label="$gettext('Add next step')"
      @click.stop="$emit('add-next')"
    >
      +
    </button>
  </div>
</template>

<script lang="ts" setup>
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import { useGettext } from 'vue3-gettext'
import type { WorkflowNodeData } from '../../types/workflow'

const props = defineProps<{ id: string; data: WorkflowNodeData }>()
defineEmits<{ (e: 'configure'): void; (e: 'add-next'): void }>()
const { $gettext } = useGettext()

const subtitle = computed(() => props.data.prompt || $gettext('No prompt set'))
</script>
