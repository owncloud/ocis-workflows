<template>
  <!-- eslint-disable-next-line vuejs-accessibility/click-events-have-key-events, vuejs-accessibility/no-static-element-interactions -->
  <div class="workflows-picker-overlay" @click.self="$emit('close')" @keydown.esc="$emit('close')">
    <div class="workflows-picker" role="dialog" aria-modal="true" :aria-label="$gettext('Add a step')">
      <div class="workflows-picker-header">
        <h2>{{ $gettext('What happens next?') }}</h2>
        <oc-button appearance="raw" :aria-label="$gettext('Close')" @click="$emit('close')">
          <oc-icon name="close-line" />
        </oc-button>
      </div>
      <oc-text-input
        v-model="query"
        class="workflows-picker-search"
        :label="$gettext('Search nodes')"
        label-hidden
        :placeholder="$gettext('Search nodes...')"
      />
      <div class="workflows-picker-groups">
        <div v-for="group in groups" :key="group.category" class="workflows-picker-group">
          <h3>{{ group.category }}</h3>
          <ul>
            <li v-for="item in group.items" :key="item.id">
              <button
                type="button"
                class="workflows-picker-item"
                :aria-label="item.label"
                @click="$emit('select', item.id)"
              >
                <oc-icon :name="item.icon" />
                <span class="workflows-picker-item-text">
                  <span class="workflows-picker-item-label">{{ item.label }}</span>
                  <span class="workflows-picker-item-description">{{ item.description }}</span>
                </span>
              </button>
            </li>
          </ul>
        </div>
        <p v-if="!groups.length" class="workflows-picker-empty">
          {{ $gettext('No matching nodes.') }}
        </p>
      </div>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { NODE_TYPES, type NodeTypeDefinition } from '../nodeTypes'

const props = defineProps<{ allowedCategories?: string[] }>()
defineEmits<{ (e: 'select', id: string): void; (e: 'close'): void }>()

const { $gettext } = useGettext()
const query = ref('')

const groups = computed(() => {
  const q = query.value.trim().toLowerCase()
  const byCategory = new Map<string, NodeTypeDefinition[]>()

  for (const item of NODE_TYPES) {
    if (props.allowedCategories && !props.allowedCategories.includes(item.category)) continue
    if (q && !item.label.toLowerCase().includes(q) && !item.description.toLowerCase().includes(q)) continue

    const existing = byCategory.get(item.category) ?? []
    existing.push(item)
    byCategory.set(item.category, existing)
  }

  return Array.from(byCategory.entries()).map(([category, items]) => ({ category, items }))
})
</script>

<style scoped>
.workflows-picker-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.35);
  display: flex;
  justify-content: flex-end;
  z-index: 100;
}
.workflows-picker {
  width: 380px;
  max-width: 100%;
  height: 100%;
  background: var(--oc-color-swatch-brand-contrastText, #fff);
  box-shadow: -2px 0 12px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  padding: 1rem;
  overflow-y: auto;
}
.workflows-picker-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0.75rem;
}
.workflows-picker-search {
  margin-bottom: 1rem;
}
.workflows-picker-group h3 {
  font-size: 0.8rem;
  text-transform: uppercase;
  opacity: 0.6;
  margin: 1rem 0 0.5rem;
}
.workflows-picker-group ul {
  list-style: none;
  margin: 0;
  padding: 0;
}
.workflows-picker-item {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  width: 100%;
  padding: 0.6rem;
  border: none;
  background: transparent;
  border-radius: 6px;
  text-align: left;
  cursor: pointer;
}
.workflows-picker-item:hover,
.workflows-picker-item:focus-visible {
  background: var(--oc-color-background-hover, rgba(0, 0, 0, 0.05));
}
.workflows-picker-item-text {
  display: flex;
  flex-direction: column;
}
.workflows-picker-item-label {
  font-weight: 600;
}
.workflows-picker-item-description {
  font-size: 0.8rem;
  opacity: 0.7;
}
.workflows-picker-empty {
  opacity: 0.6;
  padding: 1rem 0;
}
</style>
