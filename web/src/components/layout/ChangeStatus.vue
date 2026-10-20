<script setup lang="ts">
// The "change status" indicator: replaces 1.x's "apply config" button. It shows
// whether the applied generation matches the desired one, and opens a popover
// with the dry-run plan per component.
import { computed, ref, watch } from 'vue'
import { PopoverContent, PopoverPortal, PopoverRoot, PopoverTrigger } from 'reka-ui'
import { CheckCircle2, Loader2, AlertTriangle, RefreshCw } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import { system } from '@/api/endpoints'
import type { PlanResponse } from '@/api/types'
import { useGatewayState } from '@/stores/state'
import { useToast } from '@/stores/toast'

const gw = useGatewayState()
const toast = useToast()
const open = ref(false)
const plan = ref<PlanResponse | null>(null)
const loadingPlan = ref(false)
const applying = ref(false)

type Kind = 'applied' | 'pending' | 'applying' | 'failed'
const kind = computed<Kind>(() => {
  const s = gw.state
  if (!s) return 'pending'
  if (s.applying) return 'applying'
  if (s.last_error) return 'failed'
  if (s.pending) return 'pending'
  return 'applied'
})
const label = computed(() => {
  const s = gw.state
  if (!s) return '…'
  switch (kind.value) {
    case 'applying': return '正在下发…'
    case 'failed': return `下发失败 · ${s.retry_in_s ? `${s.retry_in_s}s 后重试` : '等待重试'}`
    case 'pending': return `待下发 gen ${s.generation}`
    default: return `已生效 gen ${s.last_applied_generation}`
  }
})
const tone = computed(() => ({ applied: 'ok', pending: 'warn', applying: 'info', failed: 'danger' } as const)[kind.value])

watch(open, async (o) => {
  if (!o) return
  loadingPlan.value = true
  try { plan.value = await system.plan() } catch (e) { toast.error('读取变更计划失败', String(e)) } finally { loadingPlan.value = false }
})

async function applyNow() {
  applying.value = true
  try {
    await system.reconcile()
    toast.ok('已下发')
    plan.value = await system.plan()
  } catch (e) {
    toast.error('下发失败', (e as Error).message)
  } finally { applying.value = false }
}

const kindLabel: Record<string, string> = { noop: '无变化', write: '写文件', hot: '热更新', restart: '重启', start: '启动', exec: '执行', skipped: '跳过' }
const kindTone: Record<string, 'neutral' | 'ok' | 'warn' | 'danger' | 'info'> = { noop: 'neutral', write: 'info', hot: 'ok', restart: 'warn', start: 'warn', exec: 'info', skipped: 'neutral' }
</script>

<template>
  <PopoverRoot v-model:open="open">
    <PopoverTrigger as-child>
      <button :class="['inline-flex h-8 items-center gap-1.5 rounded-full border px-3 text-xs font-medium transition-colors hover:bg-surface-2', {
        applied: 'border-ok/40 text-ok', pending: 'border-warn/50 text-warn', applying: 'border-info/40 text-info', failed: 'border-danger/50 text-danger',
      }[kind]]" aria-label="变更状态">
        <Loader2 v-if="kind === 'applying'" class="h-3.5 w-3.5 animate-spin" />
        <AlertTriangle v-else-if="kind === 'failed'" class="h-3.5 w-3.5" />
        <RefreshCw v-else-if="kind === 'pending'" class="h-3.5 w-3.5" />
        <CheckCircle2 v-else class="h-3.5 w-3.5" />
        <span class="hidden sm:inline">{{ label }}</span>
      </button>
    </PopoverTrigger>
    <PopoverPortal>
      <PopoverContent align="end" :side-offset="8" class="z-50 w-[28rem] max-w-[calc(100vw-2rem)] rounded-lg border border-border bg-surface p-4 shadow-xl">
        <div class="mb-3 flex items-center justify-between">
          <div>
            <p class="text-sm font-semibold">变更状态</p>
            <p class="text-xs text-fg-muted">期望 gen {{ gw.state?.generation ?? '-' }} · 已生效 gen {{ gw.state?.last_applied_generation ?? '-' }}</p>
          </div>
          <Badge :tone="tone">{{ label }}</Badge>
        </div>
        <p v-if="gw.state?.last_error" class="mb-3 rounded-md bg-danger-soft p-2 text-xs text-danger break-words">{{ gw.state.last_error }}</p>
        <div v-if="loadingPlan" class="py-4 text-center text-xs text-fg-muted">正在计算每个组件将执行的动作…</div>
        <div v-else-if="plan" class="space-y-2">
          <div v-for="p in plan.plans" :key="p.component" class="rounded-md border border-border p-2.5">
            <div class="flex flex-wrap items-center gap-1.5">
              <span class="text-xs font-semibold uppercase">{{ p.component }}</span>
              <Badge v-for="(a, i) in p.actions" :key="i" :tone="kindTone[a.kind] ?? 'neutral'" :title="a.detail">{{ kindLabel[a.kind] ?? a.kind }}</Badge>
            </div>
            <details v-if="p.diff" class="mt-1.5">
              <summary class="cursor-pointer text-xs text-fg-muted hover:text-fg">查看 diff</summary>
              <pre class="scroll-thin mt-1 max-h-48 overflow-auto rounded bg-surface-2 p-2 font-mono text-[11px] leading-4">{{ p.diff }}</pre>
            </details>
          </div>
          <ul v-if="plan.warnings.length" class="list-disc pl-5 text-xs text-warn">
            <li v-for="w in plan.warnings" :key="w">{{ w }}</li>
          </ul>
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-fg-subtle">
          <span>写入后自动下发;此按钮用于立即重试或强制对齐。</span>
          <Button size="sm" variant="secondary" :loading="applying" @click="applyNow">立即下发</Button>
        </div>
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>
