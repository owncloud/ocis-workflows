<template>
  <main class="oc-p workflows-builder">
    <div class="workflows-builder-header">
      <oc-text-input v-model="name" :label="$gettext('Workflow name')" />
      <label for="workflows-trigger-type" class="oc-invisible-sr">{{ $gettext('Trigger') }}</label>
      <select id="workflows-trigger-type" v-model="triggerType" class="workflows-node-select">
        <option value="manual">{{ $gettext('Manual') }}</option>
        <option value="schedule">{{ $gettext('Schedule') }}</option>
        <option value="event">{{ $gettext('Event') }}</option>
      </select>
      <oc-button @click="addLlmNode">{{ $gettext('Add LLM step') }}</oc-button>
      <oc-button @click="addActionNode">{{ $gettext('Add action') }}</oc-button>
      <oc-button variation="primary" :disabled="saving" @click="save">
        {{ $gettext('Save') }}
      </oc-button>
    </div>

    <p v-if="loadError" class="oc-text-input-danger">{{ loadError }}</p>
    <p v-if="saveError" class="oc-text-input-danger">{{ saveError }}</p>

    <div class="workflows-canvas">
      <VueFlow v-model:nodes="nodes" v-model:edges="edges" fit-view-on-init>
        <Background />
        <Controls />
        <template #node-trigger="nodeProps">
          <TriggerNode v-bind="nodeProps" />
        </template>
        <template #node-llm="nodeProps">
          <LlmNode v-bind="nodeProps" @update="(data) => updateNodeData(nodeProps.id, data)" />
        </template>
        <template #node-action="nodeProps">
          <ActionNode v-bind="nodeProps" @update="(data) => updateNodeData(nodeProps.id, data)" />
        </template>
      </VueFlow>
    </div>
  </main>
</template>

<script lang="ts" setup>
import { onMounted, ref } from 'vue'
import { useRoute } from '@ownclouders/web-pkg'
import { useGettext } from 'vue3-gettext'
import { VueFlow, useVueFlow } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import TriggerNode from '../components/nodes/TriggerNode.vue'
import LlmNode from '../components/nodes/LlmNode.vue'
import ActionNode from '../components/nodes/ActionNode.vue'
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import { useAppConfig } from '../composables/useAppConfig'
import { builderPath } from '../router'
import type { TriggerType, WorkflowEdge, WorkflowNode, WorkflowNodeData } from '../types/workflow'

const props = defineProps<{ id: string }>()

const { $gettext } = useGettext()
const route = useRoute()
const appConfig = useAppConfig()
const api = useWorkflowsApi(appConfig.backendUrl)
const { addNodes } = useVueFlow()

const isNew = () => (props.id || (route.value.params.id as string)) === 'new'

const name = ref($gettext('Untitled workflow'))
const triggerType = ref<TriggerType>('manual')
const nodes = ref<WorkflowNode[]>([])
const edges = ref<WorkflowEdge[]>([])
const loadError = ref('')
const saveError = ref('')
const saving = ref(false)

const defaultTriggerNode = (): WorkflowNode => ({
  id: 'trigger',
  type: 'trigger',
  position: { x: 0, y: 100 },
  data: { label: $gettext('Manual') }
})

const load = async () => {
  if (isNew()) {
    nodes.value = [defaultTriggerNode()]
    edges.value = []
    return
  }
  try {
    const workflow = await api.getWorkflow(route.value.params.id as string)
    name.value = workflow.name
    triggerType.value = workflow.trigger.type
    nodes.value = workflow.graph.nodes.length ? workflow.graph.nodes : [defaultTriggerNode()]
    edges.value = workflow.graph.edges
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
}

const nextNodeId = (prefix: string) => `${prefix}-${nodes.value.length + 1}`

const addLlmNode = () => {
  const node: WorkflowNode = {
    id: nextNodeId('llm'),
    type: 'llm',
    position: { x: 260, y: 40 + nodes.value.length * 40 },
    data: { prompt: '' }
  }
  nodes.value.push(node)
  addNodes([node])
}

const addActionNode = () => {
  const node: WorkflowNode = {
    id: nextNodeId('action'),
    type: 'action',
    position: { x: 520, y: 40 + nodes.value.length * 40 },
    data: { actionType: 'tag' }
  }
  nodes.value.push(node)
  addNodes([node])
}

const updateNodeData = (nodeId: string, data: WorkflowNodeData) => {
  const target = nodes.value.find((n) => n.id === nodeId)
  if (target) {
    target.data = data
  }
}

const save = async () => {
  saving.value = true
  saveError.value = ''
  try {
    const payload = {
      name: name.value,
      enabled: true,
      trigger: { type: triggerType.value },
      graph: { nodes: nodes.value, edges: edges.value }
    }
    if (isNew()) {
      const created = await api.createWorkflow(payload)
      window.location.assign(builderPath(created.id))
    } else {
      await api.updateWorkflow(route.value.params.id as string, payload)
    }
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.workflows-builder-header {
  display: flex;
  gap: 0.5rem;
  align-items: center;
  margin-bottom: 1rem;
}
.workflows-canvas {
  height: 70vh;
  border: 1px solid var(--oc-color-border, #ccc);
}
</style>
