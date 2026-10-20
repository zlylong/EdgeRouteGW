<script setup lang="ts">
import { ref } from 'vue'
import { Check, Copy } from 'lucide-vue-next'
defineProps<{ code: string; lang?: string; maxHeight?: string }>()
const copied = ref(false)
async function copy(text: string) {
  try { await navigator.clipboard.writeText(text); copied.value = true; setTimeout(() => (copied.value = false), 1500) } catch { /* clipboard unavailable */ }
}
</script>

<template>
  <div class="relative rounded-lg border border-border bg-surface-2">
    <button class="absolute right-2 top-2 rounded p-1 text-fg-subtle hover:bg-surface hover:text-fg" aria-label="复制" @click="copy(code)">
      <Check v-if="copied" class="h-3.5 w-3.5 text-ok" /><Copy v-else class="h-3.5 w-3.5" />
    </button>
    <pre class="scroll-thin overflow-auto p-3 font-mono text-xs leading-5" :style="{ maxHeight: maxHeight ?? '60vh' }"><code>{{ code }}</code></pre>
  </div>
</template>
