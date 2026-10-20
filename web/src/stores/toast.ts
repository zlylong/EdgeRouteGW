import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ToastKind = 'info' | 'ok' | 'warn' | 'error'
export interface Toast { id: number; kind: ToastKind; title: string; detail?: string; ttl: number }

let seq = 0
export const useToast = defineStore('toast', () => {
  const items = ref<Toast[]>([])
  function push(kind: ToastKind, title: string, detail?: string, ttl = kind === 'error' ? 8000 : 4000) {
    const t: Toast = { id: ++seq, kind, title, detail, ttl }
    items.value.push(t)
    if (items.value.length > 5) items.value.shift()
    if (ttl > 0) setTimeout(() => dismiss(t.id), ttl)
    return t.id
  }
  function dismiss(id: number) { items.value = items.value.filter(t => t.id !== id) }
  return {
    items, dismiss,
    info: (t: string, d?: string) => push('info', t, d),
    ok: (t: string, d?: string) => push('ok', t, d),
    warn: (t: string, d?: string) => push('warn', t, d),
    error: (t: string, d?: string) => push('error', t, d),
  }
})
