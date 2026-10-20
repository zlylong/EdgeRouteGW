<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'
const props = defineProps<{ tone?: 'neutral' | 'ok' | 'warn' | 'danger' | 'info' | 'accent'; class?: string }>()
const cls = computed(() => cn(
  'inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium',
  {
    neutral: 'bg-surface-2 text-fg-muted',
    ok: 'bg-ok-soft text-ok',
    warn: 'bg-warn-soft text-warn',
    danger: 'bg-danger-soft text-danger',
    info: 'bg-info-soft text-info',
    accent: 'bg-accent-soft text-accent',
  }[props.tone ?? 'neutral'],
  props.class,
))
</script>

<template><span :class="cls"><slot /></span></template>
