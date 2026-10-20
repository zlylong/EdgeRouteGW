<script setup lang="ts">
import { Loader2 } from 'lucide-vue-next'
defineProps<{ label?: string }>()
</script>

<template>
  <div class="flex items-center gap-2 py-8 text-sm text-fg-muted justify-center">
    <Loader2 class="h-4 w-4 animate-spin" /> {{ label ?? '加载中…' }}
  </div>
</template>
