<script setup lang="ts">
import { cn } from '@/lib/utils'
const props = defineProps<{ title?: string; class?: string; bodyClass?: string }>()
</script>

<template>
  <section :class="cn('min-w-0 rounded-lg border border-border bg-surface shadow-sm', props.class)">
    <header v-if="title || $slots.actions" class="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
      <h3 class="text-sm font-semibold">{{ title }}</h3>
      <div class="flex items-center gap-2"><slot name="actions" /></div>
    </header>
    <div :class="cn('p-4', props.bodyClass)"><slot /></div>
  </section>
</template>
