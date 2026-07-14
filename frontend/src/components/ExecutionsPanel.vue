<template>
  <!-- eslint-disable-next-line vuejs-accessibility/click-events-have-key-events, vuejs-accessibility/no-static-element-interactions -->
  <div class="workflows-executions-overlay" @click.self="$emit('close')" @keydown.esc="$emit('close')">
    <div class="workflows-executions" role="dialog" aria-modal="true" :aria-label="$gettext('Executions')">
      <div class="workflows-executions-header">
        <h2>{{ $gettext('Executions') }}</h2>
        <oc-button appearance="raw" :aria-label="$gettext('Close')" @click="$emit('close')">
          <oc-icon name="close-line" />
        </oc-button>
      </div>

      <div class="workflows-executions-run-form">
        <oc-text-input
          v-model="resourcePath"
          class="workflows-ndv-field"
          :label="$gettext('File to run against (WebDAV path)')"
          placeholder="/Invoices/foo.pdf"
        />
        <oc-button variation="primary" :disabled="running" @click="run">
          {{ running ? $gettext('Running...') : $gettext('Run now') }}
        </oc-button>
      </div>
      <p v-if="runError" class="oc-text-input-danger">{{ runError }}</p>

      <p v-if="loading">{{ $gettext('Loading executions...') }}</p>
      <p v-else-if="!executions.length" class="workflows-executions-empty">
        {{ $gettext('No runs yet.') }}
      </p>
      <ul v-else class="workflows-executions-list">
        <li v-for="execution in executions" :key="execution.id" class="workflows-execution">
          <div class="workflows-execution-summary">
            <span class="workflows-status-pill" :class="statusClass(execution.status)">
              {{ execution.status }}
            </span>
            <span>{{ execution.triggeredBy }}</span>
            <span>{{ formatDate(execution.startedDateTime) }}</span>
          </div>
          <p v-if="execution.error" class="oc-text-input-danger">{{ execution.error.message }}</p>
          <ul class="workflows-execution-nodes">
            <li v-for="nodeResult in execution.nodeResults" :key="nodeResult.nodeId">
              <strong>{{ nodeResult.nodeId }}</strong> — {{ nodeResult.status }}
              <span v-if="nodeResult.output">: {{ nodeResult.output }}</span>
              <span v-if="nodeResult.error" class="oc-text-input-danger">{{ nodeResult.error.message }}</span>
            </li>
          </ul>
        </li>
      </ul>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { onMounted, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import type { ExecutionRecord } from '../types/workflow'

const props = defineProps<{ backendUrl: string; workflowId: string }>()
defineEmits<{ (e: 'close'): void }>()

const { $gettext } = useGettext()
const api = useWorkflowsApi(props.backendUrl)

const executions = ref<ExecutionRecord[]>([])
const loading = ref(true)
const resourcePath = ref('')
const running = ref(false)
const runError = ref('')

const load = async () => {
  loading.value = true
  try {
    executions.value = await api.listExecutions(props.workflowId)
  } finally {
    loading.value = false
  }
}

const run = async () => {
  running.value = true
  runError.value = ''
  try {
    await api.runWorkflow(props.workflowId, resourcePath.value || undefined)
    await load()
  } catch (e) {
    runError.value = e instanceof Error ? e.message : String(e)
  } finally {
    running.value = false
  }
}

const statusClass = (status: string) => (status === 'succeeded' ? 'is-active' : status === 'failed' ? 'is-failed' : 'is-inactive')
const formatDate = (iso: string) => new Date(iso).toLocaleString()

onMounted(load)
</script>

<style scoped>
.workflows-executions-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.35);
  display: flex;
  justify-content: flex-end;
  z-index: 100;
}
.workflows-executions {
  width: 480px;
  max-width: 100%;
  height: 100%;
  background: var(--oc-color-swatch-brand-contrastText, #fff);
  box-shadow: -2px 0 12px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  padding: 1rem;
  overflow-y: auto;
}
.workflows-executions-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.workflows-executions-run-form {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  margin: 1rem 0;
  padding-bottom: 1rem;
  border-bottom: 1px solid var(--oc-color-border, #ddd);
}
.workflows-executions-empty {
  opacity: 0.7;
}
.workflows-executions-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.workflows-execution {
  padding: 0.75rem 0;
  border-bottom: 1px solid var(--oc-color-border, #eee);
}
.workflows-execution-summary {
  display: flex;
  gap: 0.5rem;
  align-items: center;
}
.workflows-execution-nodes {
  margin: 0.5rem 0 0;
  padding-left: 1.2rem;
  font-size: 0.85rem;
}
.workflows-status-pill {
  display: inline-block;
  padding: 0.15rem 0.6rem;
  border-radius: 999px;
  font-size: 0.75rem;
  font-weight: 600;
}
.workflows-status-pill.is-active {
  background: #e3f5e9;
  color: #1a7f37;
}
.workflows-status-pill.is-inactive {
  background: #f0f0f0;
  color: #666;
}
.workflows-status-pill.is-failed {
  background: #fbe4e4;
  color: #b3261e;
}
</style>
