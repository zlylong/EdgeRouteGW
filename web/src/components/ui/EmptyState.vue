<script setup lang="ts">
defineProps<{ title: string; hint?: string }>()
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border py-12 text-center">
    <p class="text-sm font-medium text-fg-muted">{{ title }}</p>
    <p v-if="hint" class="max-w-md text-xs text-fg-subtle">{{ hint }}</p>
    <div v-if="$slots.default" class="mt-2"><slot /></div>
  </div>
</template>
