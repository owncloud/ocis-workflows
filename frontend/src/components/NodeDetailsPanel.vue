<template>
  <!-- eslint-disable-next-line vuejs-accessibility/click-events-have-key-events, vuejs-accessibility/no-static-element-interactions -->
  <div class="workflows-ndv-overlay" @click.self="$emit('close')" @keydown.esc="$emit('close')">
    <div class="workflows-ndv" role="dialog" aria-modal="true" :aria-label="nodeType?.label">
      <div class="workflows-ndv-header">
        <oc-icon v-if="nodeType" :name="nodeType.icon" fill-type="line" />
        <h2>{{ nodeType?.label ?? node.type }}</h2>
        <oc-button appearance="raw" :aria-label="$gettext('Close')" @click="$emit('close')">
          <oc-icon name="close" fill-type="line" />
        </oc-button>
      </div>
      <p v-if="nodeType" class="workflows-ndv-description">{{ nodeType.description }}</p>
      <p v-if="showFileSourceWarning" class="workflows-ndv-warning">
        {{
          $gettext(
            'This action needs a file to act on, but nothing upstream reliably provides one. Add a File Event Trigger upstream, or make sure a file path is supplied when this workflow runs.'
          )
        }}
      </p>

      <div class="workflows-ndv-body">
        <template v-if="node.type === 'trigger'">
          <label class="workflows-ndv-label" for="ndv-trigger-type">{{ $gettext('Trigger type') }}</label>
          <select id="ndv-trigger-type" v-model="triggerType" class="workflows-ndv-select">
            <option value="manual">{{ $gettext('Manual') }}</option>
            <option value="schedule">{{ $gettext('Schedule') }}</option>
            <option value="event">{{ $gettext('File event') }}</option>
            <option value="webhook">{{ $gettext('Webhook') }}</option>
          </select>

          <template v-if="triggerType === 'schedule'">
            <oc-text-input
              v-model="schedule"
              class="workflows-ndv-field"
              :label="$gettext('Cron expression')"
              :description-message="$gettext('e.g. 0 * * * * runs every hour')"
            />
          </template>

          <template v-if="triggerType === 'event'">
            <label class="workflows-ndv-label" for="ndv-event-type">{{ $gettext('Event') }}</label>
            <select id="ndv-event-type" v-model="eventType" class="workflows-ndv-select">
              <option value="upload">{{ $gettext('File uploaded') }}</option>
              <option value="move">{{ $gettext('File moved') }}</option>
              <option value="share">{{ $gettext('File shared') }}</option>
              <option value="lock">{{ $gettext('File locked') }}</option>
            </select>
            <oc-text-input
              v-model="eventPathPrefix"
              class="workflows-ndv-field"
              :label="$gettext('Only for files under path (optional)')"
              placeholder="/Invoices"
            />
          </template>

          <template v-if="triggerType === 'webhook'">
            <!-- Same "you need a saved workflow id first" problem the manual run/executions
                 actions already have (see WorkflowBuilder's isNew()/currentId()) — the
                 webhook URL is per-workflow-id, so it can't exist before the first save. -->
            <p v-if="!isWorkflowSaved" class="workflows-ndv-webhook-hint">
              {{ $gettext('Save the workflow to generate its webhook URL.') }}
            </p>
            <template v-else>
              <label class="workflows-ndv-label" for="ndv-webhook-url">{{ $gettext('Webhook URL') }}</label>
              <div class="workflows-ndv-webhook-row">
                <code id="ndv-webhook-url" class="workflows-ndv-webhook-value" data-test="webhook-url">
                  {{ webhookRevealed && webhookInfo ? webhookInfo.url : webhookMaskedValue }}
                </code>
                <oc-button
                  appearance="raw"
                  :disabled="webhookLoading"
                  data-test="webhook-reveal"
                  @click="toggleWebhookRevealed"
                >
                  {{ webhookRevealed ? $gettext('Hide') : $gettext('Reveal') }}
                </oc-button>
                <oc-button appearance="raw" :disabled="webhookLoading" data-test="webhook-copy" @click="copyWebhookUrl">
                  {{ webhookCopied ? $gettext('Copied!') : $gettext('Copy') }}
                </oc-button>
              </div>
              <p class="workflows-ndv-description">
                {{
                  $gettext(
                    'POST a request here (a JSON object body is optional) to run this workflow. The body is exposed to the graph as vars["webhook.body"], plus vars["webhook.body.<key>"] for each top-level JSON key.'
                  )
                }}
              </p>
              <oc-button
                appearance="outline"
                :disabled="webhookLoading"
                data-test="webhook-rotate"
                @click="rotateWebhookTokenNow"
              >
                {{ $gettext('Rotate token') }}
              </oc-button>
              <p v-if="webhookError" class="oc-text-input-danger">{{ webhookError }}</p>
            </template>
          </template>
        </template>

        <template v-else-if="node.type === 'llm'">
          <label class="workflows-ndv-label" for="ndv-prompt">{{ $gettext('Prompt') }}</label>
          <textarea
            id="ndv-prompt"
            v-model="prompt"
            class="workflows-ndv-textarea"
            rows="8"
            :placeholder="$gettext('Summarize {{file.content}} in three bullet points...')"
          />
          <oc-text-input
            v-model="model"
            class="workflows-ndv-field"
            :label="$gettext('Model override (optional)')"
            placeholder="gpt-4o-mini"
          />
        </template>

        <template v-else-if="node.type === 'extractText'">
          <oc-text-input
            v-model="outputVariable"
            class="workflows-ndv-field"
            :label="$gettext('Output variable (optional)')"
            :description-message="$gettext('Variable name the extracted text is stored under (defaults to file.text)')"
            placeholder="file.text"
          />
        </template>

        <template v-else-if="node.type === 'action'">
          <template v-if="node.data.actionType === 'tag'">
            <oc-text-input v-model="paramTag" class="workflows-ndv-field" :label="$gettext('Tag')" placeholder="reviewed" />
          </template>
          <template v-else-if="node.data.actionType === 'comment'">
            <label class="workflows-ndv-label" for="ndv-comment">{{ $gettext('Comment') }}</label>
            <textarea
              id="ndv-comment"
              v-model="paramComment"
              class="workflows-ndv-textarea"
              rows="4"
              :placeholder="$gettext('Comment text, can reference {{llm.output}}')"
            />
          </template>
          <template v-else-if="node.data.actionType === 'move' || node.data.actionType === 'copy'">
            <oc-text-input
              v-model="paramDestination"
              class="workflows-ndv-field"
              :label="$gettext('Destination path')"
              placeholder="/Archive"
            />
          </template>
          <template v-else-if="node.data.actionType === 'rename'">
            <oc-text-input
              v-model="paramNewName"
              class="workflows-ndv-field"
              :label="$gettext('New name')"
              :placeholder="'{{file.name}}-reviewed'"
            />
          </template>
          <template v-else-if="node.data.actionType === 'notify'">
            <oc-text-input
              v-model="paramTarget"
              class="workflows-ndv-field"
              :label="$gettext('Target')"
              placeholder="slack://token@channel"
            />
            <label class="workflows-ndv-label" for="ndv-message">{{ $gettext('Message') }}</label>
            <textarea
              id="ndv-message"
              v-model="paramMessage"
              class="workflows-ndv-textarea"
              rows="4"
              :placeholder="$gettext('Message, can reference {{llm.output}}')"
            />
          </template>
        </template>

        <oc-text-input
          v-model="condition"
          class="workflows-ndv-field"
          :label="$gettext('Run only if (optional condition)')"
          placeholder="llm.output.category == &quot;invoice&quot;"
        />
      </div>

      <div class="workflows-ndv-footer">
        <oc-button variation="primary" @click="$emit('close')">
          {{ $gettext('Done') }}
        </oc-button>
      </div>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { computed, ref, watch } from 'vue'
import { useGettext } from 'vue3-gettext'
import { findNodeTypeForNode } from '../nodeTypes'
import { hasUpstreamFileSource, isFileDependentActionType } from '../utils/flowValidation'
import { useWorkflowsApi } from '../composables/useWorkflowsApi'
import type {
  EventTriggerType,
  WebhookTokenInfo,
  WorkflowEdge,
  WorkflowNode,
  WorkflowNodeData
} from '../types/workflow'

const props = defineProps<{
  node: WorkflowNode
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  workflowId?: string
  backendUrl?: string
}>()
const emit = defineEmits<{ (e: 'update', data: WorkflowNodeData): void; (e: 'close'): void }>()
const { $gettext } = useGettext()
const api = useWorkflowsApi(props.backendUrl ?? '')

const nodeType = computed(() => findNodeTypeForNode(props.node.type, props.node.data.actionType))

const showFileSourceWarning = computed(
  () =>
    props.node.type === 'action' &&
    isFileDependentActionType(props.node.data.actionType) &&
    !hasUpstreamFileSource(props.node.id, props.nodes, props.edges)
)

const patch = (partial: Partial<WorkflowNodeData>) => emit('update', { ...props.node.data, ...partial })

const field = <K extends keyof WorkflowNodeData>(key: K) =>
  computed<WorkflowNodeData[K]>({
    get: () => props.node.data[key],
    set: (value) => patch({ [key]: value } as Partial<WorkflowNodeData>)
  })

const triggerType = field('triggerType')
const schedule = field('schedule')
const prompt = field('prompt')
const model = field('model')
const outputVariable = field('outputVariable')
const condition = field('condition')

const eventType = computed<EventTriggerType>({
  get: () => props.node.data.event?.type ?? 'upload',
  set: (value) => patch({ event: { ...props.node.data.event, type: value } })
})
const eventPathPrefix = computed<string>({
  get: () => props.node.data.event?.filters?.pathPrefix ?? '',
  set: (value) =>
    patch({
      event: {
        type: props.node.data.event?.type ?? 'upload',
        filters: { ...props.node.data.event?.filters, pathPrefix: value }
      }
    })
})

const actionParam = (key: string) =>
  computed<string>({
    get: () => (props.node.data.actionParams?.[key] as string) ?? '',
    set: (value) => patch({ actionParams: { ...props.node.data.actionParams, [key]: value } })
  })

const paramTag = actionParam('tag')
const paramComment = actionParam('comment')
const paramDestination = actionParam('destination')
const paramNewName = actionParam('newName')
const paramTarget = actionParam('target')
const paramMessage = actionParam('message')

// Webhook trigger: URL/token reveal + rotate. Same "need a saved workflow id first"
// constraint as the manual run/executions actions already have — a webhook URL is
// per-workflow-id, so there's nothing to show until the workflow has been saved at least
// once (see WorkflowBuilder's isNew()/currentId()).
const isWorkflowSaved = computed(() => !!props.workflowId && props.workflowId !== 'new')
const webhookMaskedValue = '••••••••••••••••••••••••••••••••'

const webhookInfo = ref<WebhookTokenInfo | null>(null)
const webhookRevealed = ref(false)
const webhookLoading = ref(false)
const webhookError = ref('')
const webhookCopied = ref(false)

const loadWebhookToken = async () => {
  if (!isWorkflowSaved.value) {
    return
  }
  webhookLoading.value = true
  webhookError.value = ''
  try {
    webhookInfo.value = await api.getWebhookToken(props.workflowId!)
  } catch (e) {
    webhookError.value = e instanceof Error ? e.message : String(e)
  } finally {
    webhookLoading.value = false
  }
}

const toggleWebhookRevealed = async () => {
  if (!webhookInfo.value) {
    await loadWebhookToken()
  }
  webhookRevealed.value = !webhookRevealed.value
}

const copyWebhookUrl = async () => {
  if (!webhookInfo.value) {
    await loadWebhookToken()
  }
  if (!webhookInfo.value) {
    return
  }
  await navigator.clipboard.writeText(webhookInfo.value.url)
  webhookCopied.value = true
  setTimeout(() => {
    webhookCopied.value = false
  }, 2000)
}

const rotateWebhookTokenNow = async () => {
  webhookLoading.value = true
  webhookError.value = ''
  try {
    webhookInfo.value = await api.rotateWebhookToken(props.workflowId!)
    webhookRevealed.value = true
  } catch (e) {
    webhookError.value = e instanceof Error ? e.message : String(e)
  } finally {
    webhookLoading.value = false
  }
}

// Fetch eagerly (but keep it masked until "Reveal" is clicked) whenever the panel opens on
// a saved workflow's webhook trigger — mirrors ExecutionsPanel's onMounted(load).
watch(
  () => [props.node.id, triggerType.value, isWorkflowSaved.value] as const,
  ([, type, saved]) => {
    webhookInfo.value = null
    webhookRevealed.value = false
    webhookError.value = ''
    if (type === 'webhook' && saved) {
      loadWebhookToken()
    }
  },
  { immediate: true }
)
</script>

<style scoped>
.workflows-ndv-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.35);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}
.workflows-ndv {
  width: min(640px, 92vw);
  max-height: 85vh;
  overflow-y: auto;
  background: var(--oc-color-swatch-brand-contrastText, #fff);
  border-radius: 8px;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.25);
  padding: 1.5rem;
}
.workflows-ndv-header {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.workflows-ndv-header h2 {
  flex: 1;
  margin: 0;
}
.workflows-ndv-description {
  opacity: 0.7;
  margin-top: 0.25rem;
}
.workflows-ndv-warning {
  margin-top: 0.75rem;
  padding: 0.6rem 0.8rem;
  border-radius: 4px;
  background: var(--oc-color-swatch-warning-default, #fff4e5);
  color: var(--oc-color-swatch-warning-contrastText, #7a4a00);
  font-size: 0.9rem;
}
.workflows-ndv-body {
  margin-top: 1.5rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.workflows-ndv-label {
  font-weight: 600;
  margin-bottom: -0.5rem;
}
.workflows-ndv-select {
  padding: 0.5rem;
}
.workflows-ndv-textarea {
  width: 100%;
  font-family: monospace;
  padding: 0.5rem;
}
.workflows-ndv-footer {
  display: flex;
  justify-content: flex-end;
  margin-top: 1.5rem;
  padding-top: 1rem;
  border-top: 1px solid var(--oc-color-border, rgba(0, 0, 0, 0.1));
}
.workflows-ndv-webhook-hint {
  opacity: 0.7;
}
.workflows-ndv-webhook-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.workflows-ndv-webhook-value {
  flex: 1;
  overflow-x: auto;
  white-space: nowrap;
  padding: 0.4rem 0.6rem;
  background: var(--oc-color-background-muted, #f5f5f5);
  border-radius: 4px;
  font-size: 0.85rem;
}
</style>
