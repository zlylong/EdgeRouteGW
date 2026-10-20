<script setup lang="ts">
// Live "where would this go?" box. Debounced; result highlights the matched
// rule through the `matched` emit so the table can scroll to it.
import { ref, watch } from 'vue'
import { Crosshair } from 'lucide-vue-next'
import Input from '@/components/ui/Input.vue'
import Badge from '@/components/ui/Badge.vue'
import { system } from '@/api/endpoints'
import type { TraceResult } from '@/api/types'

const emit = defineEmits<{ matched: [ruleId: string | null] }>()
const target = ref('')
const result = ref<TraceResult | null>(null)
const error = ref('')
let timer: number | null = null
let ctrl: AbortController | null = null

watch(target, (t) => {
  if (timer) window.clearTimeout(timer)
  ctrl?.abort()
  if (!t.trim()) { result.value = null; error.value = ''; emit('matched', null); return }
  timer = window.setTimeout(async () => {
    ctrl = new AbortController()
    try {
      const r = await system.trace(t.trim())
      result.value = r; error.value = ''
      emit('matched', r.matched_rule?.id ?? null)
    } catch (e) { error.value = (e as Error).message; result.value = null }
  }, 250)
})
const outboundTone = (o: string) => o === 'direct' ? 'ok' : o === 'block' ? 'danger' : 'accent'
</script>

<template>
  <div class="rounded-lg border border-border bg-surface p-3">
    <div class="flex items-center gap-2">
      <Crosshair class="h-4 w-4 shrink-0 text-fg-muted" />
      <Input v-model="target" placeholder="输入域名或 IP,实时查看会命中哪条规则、走哪个出站" class="font-mono" />
    </div>
    <div v-if="result" class="mt-2 flex flex-wrap items-center gap-2 text-sm">
      <Badge :tone="outboundTone(result.outbound)">{{ result.outbound }}</Badge>
      <span v-if="result.via" class="text-fg-muted">经 {{ result.via }}</span>
      <span class="text-fg-muted">·</span>
      <span v-if="result.matched_rule" class="text-fg-muted">命中「{{ result.rule_set }}」中的 <code class="rounded bg-surface-2 px-1 font-mono text-xs">{{ result.matched_rule.match_kind }}:{{ result.matched_rule.match_value }}</code></span>
      <span v-else class="text-fg-muted">未命中任何规则,走兜底</span>
      <span class="text-xs text-fg-subtle">— {{ result.reason }}(评估 {{ result.considered }} 条)</span>
      <span v-for="n in result.notes" :key="n" class="basis-full text-xs text-warn">{{ n }}</span>
    </div>
    <p v-else-if="error" class="mt-2 text-xs text-danger">{{ error }}</p>
  </div>
</template>
