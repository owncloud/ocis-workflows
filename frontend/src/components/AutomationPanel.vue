<template>
  <!-- eslint-disable-next-line vuejs-accessibility/click-events-have-key-events, vuejs-accessibility/no-static-element-interactions -->
  <div class="workflows-automation-overlay" @click.self="$emit('close')" @keydown.esc="$emit('close')">
    <div class="workflows-automation-panel" role="dialog" aria-modal="true" :aria-label="$gettext('Background execution')">
      <div class="workflows-automation-panel-header">
        <h2>{{ $gettext('Background execution') }}</h2>
        <oc-button appearance="raw" :aria-label="$gettext('Close')" @click="$emit('close')">
          <oc-icon name="close" fill-type="line" />
        </oc-button>
      </div>

      <p>
        {{
          $gettext(
            'This account has a background credential that lets scheduled and file-event workflows run even when you are signed out.'
          )
        }}
      </p>
      <p v-if="expirationDateTime">{{ $gettext('Renews automatically. Current expiry:') }} {{ formatDate(expirationDateTime) }}</p>

      <p v-if="disconnectError" class="oc-text-input-danger">{{ disconnectError }}</p>

      <template v-if="!confirming">
        <oc-button appearance="outline" :disabled="disconnecting" @click="onDisconnectClick">
          {{ $gettext('Disconnect') }}
        </oc-button>
      </template>
      <template v-else>
        <p class="oc-text-input-danger">{{ disconnectWarning }}</p>
        <div class="workflows-automation-panel-actions">
          <oc-button appearance="outline" :disabled="disconnecting" @click="confirming = false">
            {{ $gettext('Cancel') }}
          </oc-button>
          <oc-button appearance="outline" :disabled="disconnecting" @click="disconnect">
            {{ $gettext('Yes, disconnect') }}
          </oc-button>
        </div>
      </template>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { useWorkflowsApi } from '../composables/useWorkflowsApi'

const props = defineProps<{
  backendUrl: string
  automatedWorkflowCount: number
  expirationDateTime?: string
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'disconnected'): void }>()

const { $gettext } = useGettext()
const api = useWorkflowsApi(props.backendUrl)

const confirming = ref(false)
const disconnecting = ref(false)
const disconnectError = ref('')

const disconnectWarning = computed(() =>
  props.automatedWorkflowCount === 1
    ? $gettext('1 workflow will stop running in the background.')
    : `${props.automatedWorkflowCount} ${$gettext('workflows will stop running in the background.')}`
)

const onDisconnectClick = () => {
  if (props.automatedWorkflowCount > 0) {
    confirming.value = true
    return
  }
  disconnect()
}

const disconnect = async () => {
  disconnecting.value = true
  disconnectError.value = ''
  try {
    await api.disconnectAutomation()
    emit('disconnected')
  } catch (e) {
    disconnectError.value = e instanceof Error ? e.message : String(e)
  } finally {
    disconnecting.value = false
  }
}

const formatDate = (iso: string) => new Date(iso).toLocaleString()
</script>

<style scoped>
.workflows-automation-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.35);
  display: flex;
  justify-content: flex-end;
  z-index: 100;
}
.workflows-automation-panel {
  width: 420px;
  max-width: 100%;
  height: 100%;
  background: var(--oc-color-swatch-brand-contrastText, #fff);
  box-shadow: -2px 0 12px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 1rem;
  overflow-y: auto;
}
.workflows-automation-panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.workflows-automation-panel-actions {
  display: flex;
  gap: 0.5rem;
}
</style>
