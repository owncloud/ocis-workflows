<template>
  <main class="oc-p workflows-list">
    <div class="workflows-list-header">
      <h1>{{ $gettext('Workflows') }}</h1>
      <oc-button variation="primary" @click="createNew">
        {{ $gettext('New workflow') }}
      </oc-button>
    </div>

    <p v-if="loadError" class="oc-text-input-danger">{{ loadError }}</p>
    <p v-else-if="loading">{{ $gettext('Loading workflows...') }}</p>
    <p v-else-if="!workflows.length">
      {{ $gettext('No workflows yet. Create one to get started.') }}
    </p>

    <ul v-else class="workflows-list-items">
      <li v-for="workflow in workflows" :key="workflow.id" class="workflows-list-item">
        <a :href="builderPath(workflow.id)">{{ workflow.name }}</a>
        <span class="workflows-list-item-meta">
          {{ workflow.trigger.type }} · {{ workflow.enabled ? $gettext('enabled') : $gettext('disabled') }}
        </span>
        <oc-button appearance="raw" @click="remove(workflow.id)">
          {{ $gettext('Delete') }}
        </oc-button>
      </li>
    </ul>
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

onMounted(load)
</script>
