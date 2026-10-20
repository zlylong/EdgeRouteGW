<script setup lang="ts">
import { cn } from '@/lib/utils'
const props = defineProps<{ class?: string; invalid?: boolean }>()
const model = defineModel<string | number>()
</script>

<template>
  <input
    v-model="model"
    v-bind="$attrs"
    :class="cn('h-9 w-full rounded-md border bg-surface px-3 text-sm text-fg placeholder:text-fg-subtle focus:outline-none focus:ring-2 focus:ring-accent/50 disabled:opacity-50', props.invalid ? 'border-danger' : 'border-border', props.class)"
  >
</template>
