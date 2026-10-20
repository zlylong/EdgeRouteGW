<script setup lang="ts">
import { SwitchRoot, SwitchThumb } from 'reka-ui'
defineProps<{ disabled?: boolean; label?: string }>()
const model = defineModel<boolean>({ default: false })
</script>

<template>
  <SwitchRoot
    v-model="model" :disabled="disabled" :aria-label="label"
    class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-transparent bg-border transition-colors data-[state=checked]:bg-accent disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60"
  >
    <SwitchThumb class="block h-4 w-4 translate-x-0.5 rounded-full bg-white shadow transition-transform data-[state=checked]:translate-x-[18px]" />
  </SwitchRoot>
</template>
