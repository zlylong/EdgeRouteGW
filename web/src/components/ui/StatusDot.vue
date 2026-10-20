<script setup lang="ts">
import { computed } from 'vue'
import type { ComponentStatus } from '@/api/types'
const props = defineProps<{ status: ComponentStatus | 'ok' | 'bad' | 'pending' }>()
const color = computed(() => ({
  active: 'bg-ok', ok: 'bg-ok',
  degraded: 'bg-warn', pending: 'bg-warn',
  down: 'bg-danger', bad: 'bg-danger',
  disabled: 'bg-fg-subtle/40', unknown: 'bg-fg-subtle/40',
}[props.status]))
const pulse = computed(() => props.status === 'pending' || props.status === 'degraded')
</script>

<template>
  <span class="relative inline-flex h-2.5 w-2.5">
    <span v-if="pulse" :class="['absolute inline-flex h-full w-full animate-ping rounded-full opacity-60', color]" />
    <span :class="['relative inline-flex h-2.5 w-2.5 rounded-full', color]" />
  </span>
</template>
