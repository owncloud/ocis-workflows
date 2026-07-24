<template>
  <button
    type="button"
    class="workflows-node-add-button"
    :aria-label="$gettext('Add next step')"
    @click.stop="$emit('add-next')"
  >
    +
  </button>
</template>

<script lang="ts" setup>
import { useGettext } from 'vue3-gettext'

defineEmits<{ (e: 'add-next'): void }>()
const { $gettext } = useGettext()
</script>
