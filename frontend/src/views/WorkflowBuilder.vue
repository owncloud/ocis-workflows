<template>
  <div class="workflows-builder">
    <div class="workflows-builder-topbar">
      <a :href="listPathHref" class="workflows-builder-back" :aria-label="$gettext('Back to workflows')">
        <oc-icon name="arrow-left-line" />
      </a>

      <input
        v-if="editingName"
        ref="nameInputRef"
        v-model="name"
        class="workflows-builder-name-input"
        :aria-label="$gettext('Workflow name')"
        @blur="editingName = false"
        @keyup.enter="editingName = false"
      />
      <button v-else type="button" class="workflows-builder-name" @click="startEditingName">
        {{ name }}
      </button>

      <div class="workflows-builder-topbar-spacer" />

      <label class="workflows-builder-toggle">
        <input v-model="enabled" type="checkbox" />
        <span>{{ enabled ? $gettext('Active') : $gettext('Inactive') }}</span>
      </label>

      <oc-button variation="primary" :disabled="saving" @click="save">
        {{ $gettext('Save') }}
      </oc-button>
    </div>

    <p v-if="loadError" class="oc-text-input-danger workflows-builder-banner">{{ loadError }}</p>
    <p v-if="saveError" class="oc-text-input-danger workflows-builder-banner">{{ saveError }}</p>

    <div class="workflows-canvas">
      <VueFlow v-model:nodes="nodes" v-model:edges="edges" fit-view-on-init :default-viewport="{ zoom: 1 }">
        <Background />
        <Controls />
        <MiniMap pannable zoomable />
        <template #node-trigger="nodeProps">
          <TriggerNode
            v-bind="nodeProps"
            @configure="selectedNodeId = nodeProps.id"
            @add-next="openPicker(nodeProps.id)"
          />
        </template>
        <template #node-llm="nodeProps">
          <LlmNode
            v-bind="nodeProps"
            @configure="selectedNodeId = nodeProps.id"
            @add-next="openPicker(nodeProps.id)"
          />
        </template>
        <template #node-action="nodeProps">
          <ActionNode
            v-bind="nodeProps"
            @configure="selectedNodeId = nodeProps.id"
            @add-next="openPicker(nodeProps.id)"
          />
        </template>
      </VueFlow>

      <div v-if="!nodes.length" class="workflows-empty-state">
        <p>{{ $gettext('Add a trigger to start this workflow') }}</p>
        <oc-button variation="primary" @click="openPicker(null, [TRIGGER_CATEGORY])">
          {{ $gettext('Add trigger') }}
        </oc-button>
      </div>
    </div>

    <NodePicker
      v-if="pickerOpen"
      :allowed-categories="pickerAllowedCategories"
      @select="onPickNodeType"
      @close="pickerOpen = false"
    />

    <NodeDetailsPanel
      v-if="selectedNode"
      :node="selectedNode"
      @update="(data) => updateNodeData(selectedNode!.id, data)"
      @close="selectedNodeId = null"
    />
  </div>
</template>

<script lang="ts" setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRoute } from '@ownclouders/web-pkg'
import { useGettext } from 'vue3-gettext'
import { VueFlow, useVueFlow } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import '@vue-flow/minimap/dist/style.css'
import '../styles/canvas.css'
import TriggerNode from '../components/nodes/TriggerNode.vue'
import LlmNode from '../components/nodes/LlmNode.vue'
import ActionNode from '../components/nodes/ActionNode.vue'
import NodePicker from '../components/NodePicker.vue'
import NodeDetailsPanel from '../components/NodeDetailsPanel.vue'
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import { useAppConfig } from '../composables/useAppConfig'
import { builderPath, listPath } from '../router'
import { findNodeType, TRIGGER_CATEGORY, AI_CATEGORY, ACTION_CATEGORY } from '../nodeTypes'
import type { TriggerType, WorkflowEdge, WorkflowNode, WorkflowNodeData } from '../types/workflow'

const props = defineProps<{ id: string }>()

const { $gettext } = useGettext()
const route = useRoute()
const appConfig = useAppConfig()
const api = useWorkflowsApi(appConfig.backendUrl)
const { addNodes, addEdges, fitView } = useVueFlow()

const listPathHref = listPath()

const isNew = () => (props.id || (route.value.params.id as string)) === 'new'
const currentId = () => route.value.params.id as string

const name = ref($gettext('Untitled workflow'))
const editingName = ref(false)
const nameInputRef = ref<HTMLInputElement>()
const enabled = ref(true)
const nodes = ref<WorkflowNode[]>([])
const edges = ref<WorkflowEdge[]>([])
const loadError = ref('')
const saveError = ref('')
const saving = ref(false)

const pickerOpen = ref(false)
const pickerConnectFrom = ref<string | null>(null)
const pickerAllowedCategories = ref<string[] | undefined>(undefined)
const selectedNodeId = ref<string | null>(null)
const selectedNode = computed(() => nodes.value.find((n) => n.id === selectedNodeId.value) ?? null)

const startEditingName = () => {
  editingName.value = true
  nextTick(() => nameInputRef.value?.focus())
}

const load = async () => {
  if (isNew()) {
    return
  }
  try {
    const workflow = await api.getWorkflow(currentId())
    name.value = workflow.name
    enabled.value = workflow.enabled
    nodes.value = workflow.graph.nodes
    edges.value = workflow.graph.edges
    fitViewSoon()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
}

let nodeCounter = 0
const nextNodeId = (prefix: string) => `${prefix}-${++nodeCounter}`

// fitView needs Vue Flow to have measured the (just-added) node's actual DOM dimensions
// first; nextTick alone isn't reliably enough time for that measurement to land.
const fitViewSoon = () => {
  nextTick(() => setTimeout(() => fitView({ padding: 0.4 }), 50))
}

const openPicker = (fromNodeId: string | null, allowedCategories?: string[]) => {
  pickerConnectFrom.value = fromNodeId
  pickerAllowedCategories.value =
    allowedCategories ?? (nodes.value.some((n) => n.type === 'trigger') ? [AI_CATEGORY, ACTION_CATEGORY] : [TRIGGER_CATEGORY])
  pickerOpen.value = true
}

const onPickNodeType = (typeId: string) => {
  const def = findNodeType(typeId)
  pickerOpen.value = false
  if (!def) return

  const source = nodes.value.find((n) => n.id === pickerConnectFrom.value)
  const position = source
    ? { x: source.position.x + 260, y: source.position.y }
    : { x: 40, y: 40 + nodes.value.length * 120 }

  const node: WorkflowNode = {
    id: def.nodeKind === 'trigger' ? 'trigger' : nextNodeId(def.nodeKind),
    type: def.nodeKind,
    position,
    data: { ...def.defaultData }
  }
  nodes.value.push(node)
  addNodes([node])

  if (source) {
    const edge: WorkflowEdge = { id: `${source.id}-${node.id}`, source: source.id, target: node.id }
    edges.value.push(edge)
    addEdges([edge])
  }

  fitViewSoon()

  pickerConnectFrom.value = null
}

const updateNodeData = (nodeId: string, data: WorkflowNodeData) => {
  const target = nodes.value.find((n) => n.id === nodeId)
  if (target) {
    target.data = data
  }
}

const triggerPayload = () => {
  const triggerNode = nodes.value.find((n) => n.type === 'trigger')
  const triggerType: TriggerType = triggerNode?.data.triggerType ?? 'manual'
  return {
    type: triggerType,
    schedule: triggerType === 'schedule' ? triggerNode?.data.schedule : undefined,
    event: triggerType === 'event' ? triggerNode?.data.event : undefined
  }
}

const save = async () => {
  saving.value = true
  saveError.value = ''
  try {
    const payload = {
      name: name.value,
      enabled: enabled.value,
      trigger: triggerPayload(),
      graph: { nodes: nodes.value, edges: edges.value }
    }
    if (isNew()) {
      const created = await api.createWorkflow(payload)
      window.location.assign(builderPath(created.id))
    } else {
      await api.updateWorkflow(currentId(), payload)
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
.workflows-builder {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 80vh;
}
.workflows-builder-topbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--oc-color-border, #ddd);
}
.workflows-builder-back {
  display: inline-flex;
}
.workflows-builder-name,
.workflows-builder-name-input {
  font-size: 1.1rem;
  font-weight: 600;
  border: none;
  background: transparent;
  padding: 0.25rem 0.4rem;
  border-radius: 4px;
}
.workflows-builder-name:hover {
  background: var(--oc-color-background-hover, rgba(0, 0, 0, 0.05));
}
.workflows-builder-name-input {
  border: 1px solid var(--oc-color-border, #ccc);
}
.workflows-builder-topbar-spacer {
  flex: 1;
}
.workflows-builder-toggle {
  display: flex;
  align-items: center;
  gap: 0.4rem;
}
.workflows-builder-banner {
  padding: 0 1rem;
}
.workflows-canvas {
  position: relative;
  flex: 1;
}
.workflows-empty-state {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 1rem;
  pointer-events: none;
}
.workflows-empty-state > * {
  pointer-events: auto;
}
</style>
