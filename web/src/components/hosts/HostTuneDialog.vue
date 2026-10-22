<script setup lang="ts">
// Tuning report of a managed host (same items as the gateway's own page).
import Dialog from '@/components/ui/Dialog.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import type { HostTuneReport, RemoteHost } from '@/api/types'
import { fmtAgo } from '@/lib/utils'

defineProps<{ host: RemoteHost | null; report: HostTuneReport | null; busy: boolean }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ rerun: [] }>()
const tone: Record<string, 'ok' | 'info' | 'warn' | 'danger' | 'neutral'> = { ok: 'ok', applied: 'ok', info: 'neutral', fallback: 'warn', unavailable: 'warn' }
const label: Record<string, string> = { ok: '已生效', applied: '本次应用', info: '说明', fallback: '已回退', unavailable: '需外部处理' }
</script>

<template>
  <Dialog v-model:open="open" :title="`${host?.name ?? ''} · 主机调优`" size="md">
    <p class="mb-3 text-xs text-fg-subtle">通过 SSH 在主机上探测并应用:BBR + fq、缓冲区与 TCP 参数、打开文件数、网卡多队列或 RPS、conntrack(用 NAT 时)、内核 WireGuard。每次部署后自动跑一遍;这里可随时重跑。</p>
    <p v-if="report?.error" class="mb-3 rounded-md bg-danger-soft p-2 text-xs text-danger">{{ report.error }}</p>
    <p v-else-if="!report" class="text-sm text-fg-muted">还没有调优记录。</p>
    <template v-else>
      <p class="mb-2 text-xs text-fg-subtle">上次运行 {{ fmtAgo(report.at) }}</p>
      <ul class="divide-y divide-border/60">
        <li v-for="it in report.items" :key="it.key" class="flex flex-wrap items-start gap-3 py-2">
          <Badge :tone="tone[it.status] ?? 'neutral'" class="mt-0.5 w-20 justify-center">{{ label[it.status] ?? it.status }}</Badge>
          <div class="min-w-0 flex-1">
            <div class="text-sm">{{ it.title }}</div>
            <div class="text-xs text-fg-muted">{{ it.detail }}</div>
            <div v-if="it.hint" class="mt-0.5 text-xs text-warn">{{ it.hint }}</div>
          </div>
        </li>
      </ul>
    </template>
    <template #footer>
      <Button variant="secondary" @click="open = false">关闭</Button>
      <Button :loading="busy" :disabled="!host" @click="emit('rerun')">重新检测并应用</Button>
    </template>
  </Dialog>
</template>
