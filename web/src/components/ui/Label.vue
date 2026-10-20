<script setup lang="ts">
defineProps<{ for?: string; hint?: string }>()
</script>

<template>
  <label :for="$props.for" class="mb-1 block text-xs font-medium text-fg-muted">
    <slot />
    <span v-if="hint" class="ml-1 font-normal text-fg-subtle">{{ hint }}</span>
  </label>
</template>
