<script setup lang="ts">
import { TabsList, TabsRoot, TabsTrigger } from 'reka-ui'
defineProps<{ tabs: { value: string; label: string; count?: number }[] }>()
const model = defineModel<string>({ required: true })
</script>

<template>
  <TabsRoot v-model="model">
    <TabsList class="inline-flex h-9 items-center gap-1 rounded-md bg-surface-2 p-1 text-sm">
      <TabsTrigger
        v-for="t in tabs" :key="t.value" :value="t.value"
        class="inline-flex items-center gap-1.5 rounded px-3 py-1 text-fg-muted transition-colors hover:text-fg data-[state=active]:bg-surface data-[state=active]:text-fg data-[state=active]:shadow-sm"
      >
        {{ t.label }}
        <span v-if="t.count !== undefined" class="rounded-full bg-border/80 px-1.5 text-[10px] text-fg-muted">{{ t.count }}</span>
      </TabsTrigger>
    </TabsList>
    <div class="mt-4"><slot /></div>
  </TabsRoot>
</template>
