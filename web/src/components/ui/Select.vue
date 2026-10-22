<script setup lang="ts">
import { cn } from '@/lib/utils'
const props = defineProps<{ class?: string; options: { value: string; label: string; disabled?: boolean }[] }>()
const model = defineModel<string>()
</script>

<template>
  <select
    v-model="model"
    v-bind="$attrs"
    :class="cn('h-9 w-full rounded-md border border-border bg-surface px-2.5 text-sm text-fg focus:outline-none focus:ring-2 focus:ring-accent/50 disabled:opacity-50', props.class)"
  >
    <!-- v-memo: Vue re-sets option.value on every patch, and Chromium closes an
         open native popup on any option mutation; skip untouched options. -->
    <option v-for="o in options" :key="o.value" v-memo="[o.value, o.label, o.disabled]" :value="o.value" :disabled="o.disabled">{{ o.label }}</option>
  </select>
</template>
