<script setup lang="ts">
// Environment tuning: what the host offers, what the service applied, and
// what the operator can still do (hypervisor queues, kernel modules).
import { ref } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import Card from '@/components/ui/Card.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { system } from '@/api/endpoints'
import { fmtAgo } from '@/lib/utils'
import { useToast } from '@/stores/toast'

const qc = useQueryClient()
const toast = useToast()
const q = useQuery({ queryKey: ['tuning'], queryFn: system.tuning })
const busy = ref(false)
async function apply() {
  busy.value = true
  try { const r = await system.applyTuning(); qc.setQueryData(['tuning'], r); const n = r.items.filter(i => i.status === 'applied').length; toast.ok('已重新检测', n ? `应用了 ${n} 项` : '没有需要改动的项') } catch (e) { toast.error('失败', (e as Error).message) } finally { busy.value = false }
}
const tone: Record<string, 'ok' | 'info' | 'warn' | 'danger' | 'neutral'> = { ok: 'ok', applied: 'ok', info: 'neutral', fallback: 'warn', unavailable: 'warn' }
const label: Record<string, string> = { ok: '已生效', applied: '本次应用', info: '说明', fallback: '已回退', unavailable: '不可用' }
const virtLabel = (v: string) => v === 'none' ? '物理机' : `虚拟机(${v})`
</script>

<template>
  <div class="space-y-4">
    <Card title="环境调优">
      <div class="mb-3 flex flex-wrap items-center gap-3 text-sm text-fg-muted">
        <template v-if="q.data.value">
          <span>{{ virtLabel(q.data.value.host.virt) }} · {{ q.data.value.host.cpus }} CPU · 内核 {{ q.data.value.host.kernel }} · 网卡 {{ q.data.value.host.iface || '?' }}</span>
          <span class="text-fg-subtle">· {{ q.data.value.applied ? '上次应用' : '仅检测' }} {{ fmtAgo(q.data.value.at) }}</span>
        </template>
        <Button class="ml-auto" variant="secondary" :loading="busy" @click="apply">重新检测并应用</Button>
      </div>
      <p class="mb-3 text-xs text-fg-subtle">固定值的内核参数由包里的 sysctl 文件无条件写入;依赖硬件或内核能力的项(拥塞控制、网卡队列、WireGuard 模块、conntrack)在服务启动时探测后决定做什么,做不了的给出替代方案和你能做的事。</p>
      <Spinner v-if="q.isPending.value" />
      <ul v-else class="divide-y divide-border/60">
        <li v-for="it in q.data.value?.items ?? []" :key="it.key" class="flex flex-wrap items-start gap-3 py-2.5">
          <Badge :tone="tone[it.status] ?? 'neutral'" class="mt-0.5 w-16 justify-center">{{ label[it.status] ?? it.status }}</Badge>
          <div class="min-w-0 flex-1">
            <div class="text-sm">{{ it.title }}</div>
            <div class="text-xs text-fg-muted">{{ it.detail }}</div>
            <div v-if="it.hint" class="mt-0.5 text-xs text-warn">{{ it.hint }}</div>
          </div>
        </li>
      </ul>
    </Card>
  </div>
</template>
