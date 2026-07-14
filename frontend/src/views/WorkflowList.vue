<template>
  <main class="oc-p workflows-list">
    <div class="workflows-list-header">
      <h1>{{ $gettext('Workflows') }}</h1>
      <oc-button variation="primary" @click="createNew">
        {{ $gettext('Add workflow') }}
      </oc-button>
    </div>

    <p v-if="loadError" class="oc-text-input-danger">{{ loadError }}</p>
    <p v-else-if="loading">{{ $gettext('Loading workflows...') }}</p>
    <p v-else-if="!workflows.length" class="workflows-list-empty">
      {{ $gettext('No workflows yet. Add one to get started.') }}
    </p>

    <table v-else class="workflows-list-table">
      <thead>
        <tr>
          <th>{{ $gettext('Name') }}</th>
          <th>{{ $gettext('Trigger') }}</th>
          <th>{{ $gettext('Status') }}</th>
          <th>{{ $gettext('Last updated') }}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <tr v-for="workflow in workflows" :key="workflow.id">
          <td>
            <a :href="builderPath(workflow.id)">{{ workflow.name }}</a>
          </td>
          <td>{{ workflow.trigger.type }}</td>
          <td>
            <span class="workflows-status-pill" :class="workflow.enabled ? 'is-active' : 'is-inactive'">
              {{ workflow.enabled ? $gettext('Active') : $gettext('Inactive') }}
            </span>
          </td>
          <td>{{ formatDate(workflow.lastModifiedDateTime) }}</td>
          <td>
            <oc-button appearance="raw" @click="remove(workflow.id)">
              {{ $gettext('Delete') }}
            </oc-button>
          </td>
        </tr>
      </tbody>
    </table>
  </main>
</template>

<script lang="ts" setup>
import { onMounted, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import { useAppConfig } from '../composables/useAppConfig'
import { builderPath } from '../router'
import type { WorkflowDefinition } from '../types/workflow'

const { $gettext } = useGettext()
const appConfig = useAppConfig()
const api = useWorkflowsApi(appConfig.backendUrl)

const workflows = ref<WorkflowDefinition[]>([])
const loading = ref(true)
const loadError = ref('')

const load = async () => {
  loading.value = true
  loadError.value = ''
  try {
    workflows.value = await api.listWorkflows()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const createNew = () => {
  window.location.assign(builderPath('new'))
}

const remove = async (id: string) => {
  await api.deleteWorkflow(id)
  await load()
}

const formatDate = (iso: string) => new Date(iso).toLocaleString()

onMounted(load)
</script>

<style scoped>
.workflows-list-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1.5rem;
}
.workflows-list-empty {
  opacity: 0.7;
}
.workflows-list-table {
  width: 100%;
  border-collapse: collapse;
}
.workflows-list-table th {
  text-align: left;
  font-size: 0.8rem;
  text-transform: uppercase;
  opacity: 0.6;
  padding: 0.5rem;
  border-bottom: 1px solid var(--oc-color-border, #ddd);
}
.workflows-list-table td {
  padding: 0.6rem 0.5rem;
  border-bottom: 1px solid var(--oc-color-border, #eee);
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
</style>
