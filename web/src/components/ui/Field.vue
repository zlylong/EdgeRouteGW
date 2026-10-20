<script setup lang="ts">
// Label + control + error line. `error` comes from ApiError.field matching.
defineProps<{ label: string; error?: string; hint?: string; for?: string }>()
</script>

<template>
  <div>
    <label :for="$props.for" class="mb-1 block text-xs font-medium text-fg-muted">
      {{ label }}<span v-if="hint" class="ml-1 font-normal text-fg-subtle">{{ hint }}</span>
    </label>
    <slot />
    <p v-if="error" class="mt-1 text-xs text-danger">{{ error }}</p>
  </div>
</template>
