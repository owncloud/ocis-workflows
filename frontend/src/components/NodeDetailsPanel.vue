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
          <template v-else-if="node.data.actionType === 'share'">
            <oc-text-input
              v-model="paramRecipient"
              class="workflows-ndv-field"
              :label="$gettext('Recipient (user email or group name)')"
              placeholder="accounting@example.com"
            />
            <label class="workflows-ndv-label" for="ndv-role">{{ $gettext('Role') }}</label>
            <select id="ndv-role" v-model="paramRole" class="workflows-ndv-select">
              <option value="viewer">{{ $gettext('Viewer') }}</option>
              <option value="editor">{{ $gettext('Editor') }}</option>
            </select>
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

        <div v-if="outputHint" class="workflows-ndv-output">
          <p class="workflows-ndv-label">{{ $gettext('Output') }}</p>
          <p class="workflows-ndv-output-description">{{ outputHint.description }}</p>
          <p class="workflows-ndv-output-tokens">
            <code v-for="token in outputHint.tokens" :key="token" class="workflows-ndv-output-token">{{
              token
            }}</code>
          </p>
        </div>

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
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { findNodeTypeForNode } from '../nodeTypes'
import { hasUpstreamFileSource, isFileDependentActionType } from '../utils/flowValidation'
import type { EventTriggerType, ShareRole, WorkflowEdge, WorkflowNode, WorkflowNodeData } from '../types/workflow'

const props = defineProps<{ node: WorkflowNode; nodes: WorkflowNode[]; edges: WorkflowEdge[] }>()
const emit = defineEmits<{ (e: 'update', data: WorkflowNodeData): void; (e: 'close'): void }>()
const { $gettext } = useGettext()

const nodeType = computed(() => findNodeTypeForNode(props.node.type, props.node.data.actionType))

const showFileSourceWarning = computed(
  () =>
    props.node.type === 'action' &&
    isFileDependentActionType(props.node.data.actionType) &&
    !hasUpstreamFileSource(props.node.id, props.nodes, props.edges)
)

// Every syntax shown here must be a variable key the backend executor (backend/pkg/executor/
// executor.go) actually writes into its shared "vars" map before rendering a downstream node's
// {{...}} templates — never a token that only looks plausible. Today that's:
//   - "file.name" / "file.content", set once per run from the triggering resource
//   - "llm.output", set by runLLM() after every LLM node
//   - "<actionType>.output" (tag/comment/move/copy/rename/notify), set by runAction() per node
interface OutputHint {
  tokens: string[]
  description: string
}

const actionOutputHints: Record<string, OutputHint> = {
  tag: {
    tokens: ['{{tag.output}}'],
    description: $gettext('The tag value that was applied to the file.')
  },
  comment: {
    tokens: ['{{comment.output}}'],
    description: $gettext('The comment text that was added to the file.')
  },
  move: {
    tokens: ['{{move.output}}'],
    description: $gettext('The path the file was moved to.')
  },
  copy: {
    tokens: ['{{copy.output}}'],
    description: $gettext('The path of the copy that was created.')
  },
  rename: {
    tokens: ['{{rename.output}}'],
    description: $gettext('The file path after the rename.')
  },
  notify: {
    tokens: ['{{notify.output}}'],
    description: $gettext('A fixed "sent" status once the notification has gone out.')
  }
}

const outputHint = computed<OutputHint | null>(() => {
  if (props.node.type === 'trigger') {
    return {
      tokens: ['{{file.name}}', '{{file.content}}'],
      description: $gettext(
        'Available in every downstream node whenever this workflow runs against a specific file — e.g. a file-event trigger, or a manual run targeting a file.'
      )
    }
  }
  if (props.node.type === 'llm') {
    return {
      tokens: ['{{llm.output}}'],
      description: $gettext("The LLM's response text.")
    }
  }
  if (props.node.type === 'action' && props.node.data.actionType) {
    return actionOutputHints[props.node.data.actionType] ?? null
  }
  return null
})

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
const paramRecipient = actionParam('recipient')

const paramRole = computed<ShareRole>({
  get: () => (props.node.data.actionParams?.role as ShareRole) ?? 'viewer',
  set: (value) => patch({ actionParams: { ...props.node.data.actionParams, role: value } })
})
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
.workflows-ndv-output {
  border: 1px solid var(--oc-color-border, #d9d9d9);
  border-radius: 4px;
  padding: 0.75rem;
  background: var(--oc-color-background-muted, rgba(0, 0, 0, 0.03));
}
.workflows-ndv-output-description {
  opacity: 0.7;
  margin: 0.25rem 0 0.5rem;
}
.workflows-ndv-output-tokens {
  margin: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
.workflows-ndv-output-token {
  font-family: monospace;
  background: var(--oc-color-background-highlight, rgba(0, 0, 0, 0.08));
  padding: 0.15rem 0.4rem;
  border-radius: 4px;
}
</style>
