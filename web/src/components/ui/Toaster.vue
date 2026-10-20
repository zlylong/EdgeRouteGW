<script setup lang="ts">
import { AlertTriangle, CheckCircle2, Info, X, XCircle } from 'lucide-vue-next'
import { useToast } from '@/stores/toast'
const toast = useToast()
const icons = { info: Info, ok: CheckCircle2, warn: AlertTriangle, error: XCircle }
const tones = { info: 'border-info/40', ok: 'border-ok/40', warn: 'border-warn/50', error: 'border-danger/50' }
const colors = { info: 'text-info', ok: 'text-ok', warn: 'text-warn', error: 'text-danger' }
</script>

<template>
  <div class="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-80 flex-col gap-2" role="status" aria-live="polite">
    <TransitionGroup enter-active-class="transition duration-200" enter-from-class="translate-y-2 opacity-0" leave-active-class="transition duration-150" leave-to-class="opacity-0">
      <div v-for="t in toast.items" :key="t.id" :class="['pointer-events-auto flex items-start gap-2 rounded-lg border bg-surface p-3 shadow-lg', tones[t.kind]]">
        <component :is="icons[t.kind]" :class="['mt-0.5 h-4 w-4 shrink-0', colors[t.kind]]" />
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium">{{ t.title }}</p>
          <p v-if="t.detail" class="mt-0.5 break-words text-xs text-fg-muted">{{ t.detail }}</p>
        </div>
        <button class="rounded p-0.5 text-fg-subtle hover:text-fg" aria-label="关闭" @click="toast.dismiss(t.id)"><X class="h-3.5 w-3.5" /></button>
      </div>
    </TransitionGroup>
  </div>
</template>
