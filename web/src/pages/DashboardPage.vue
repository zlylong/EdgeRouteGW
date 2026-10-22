<script setup lang="ts">
import { computed } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { AlertTriangle, ArrowRight } from 'lucide-vue-next'
import Card from '@/components/ui/Card.vue'
import Badge from '@/components/ui/Badge.vue'
import StatusDot from '@/components/ui/StatusDot.vue'
import Button from '@/components/ui/Button.vue'
import Sparkline from '@/components/ui/Sparkline.vue'
import { nodes as nodesApi, system } from '@/api/endpoints'
import { useGatewayState } from '@/stores/state'
import { fmtAgo, fmtBytes, fmtTime } from '@/lib/utils'
import { watch, ref } from 'vue'
import type { TrafficRates } from '@/api/types'

const gw = useGatewayState()
const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: nodesApi.list })
const trafficQ = useQuery({ queryKey: ['traffic', '1h'], queryFn: () => system.traffic('1h'), refetchInterval: 60_000 })
const live = ref<TrafficRates | null>(null)
watch(() => gw.lastEvent, e => { if (e?.type === 'traffic.sample') live.value = e.data as TrafficRates })
const rate = (bps: number) => fmtBytes(Math.round(bps)) + '/s'
const nodeName = (id: string) => nodesQ.data.value?.items.find(n => n.id === id)?.name ?? id.slice(0, 8)
const eventsQ = useQuery({ queryKey: ['events', 'recent'], queryFn: () => system.events({ limit: 10 }), refetchInterval: 15_000 })

const compMeta: Record<string, { label: string; hint: string }> = {
  xray: { label: 'Xray', hint: '代理核心 · 出站与路由' },
  mosdns: { label: 'Mosdns', hint: 'DNS 分流 · 53 端口' },
  nftables: { label: 'nftables', hint: 'TPROXY 劫持与设备策略' },
  frr: { label: 'FRR / OSPF', hint: 'Mode B/C 路由宣告' },
  wireguard: { label: 'WireGuard(内核)', hint: 'WireGuard 节点的内核隧道接口' },
}
const statusLabel: Record<string, string> = { active: '运行中', degraded: '降级', down: '未运行', disabled: '未启用', unknown: '未知' }
const statusTone: Record<string, 'ok' | 'warn' | 'danger' | 'neutral'> = { active: 'ok', degraded: 'warn', down: 'danger', disabled: 'neutral', unknown: 'neutral' }
const modeText: Record<string, { name: string; desc: string }> = {
  A: { name: 'Mode A · 全局网关', desc: 'LAN 设备把网关和 DNS 指向本机,所有流量经 TPROXY 进入 Xray,按规则分流。' },
  B: { name: 'Mode B · Fake-IP 旁路', desc: '主路由通过 OSPF 学到 198.18.0.0/16,只有命中代理域名的流量被引到本机。' },
  C: { name: 'Mode C · 真实 IP 旁路', desc: '域名规则周期解析为真实 IP 并经 OSPF 宣告,主路由按真实 IP 分流。' },
}
const enabledNodes = computed(() => (nodesQ.data.value?.items ?? []).filter(n => n.enabled))
const levelTone: Record<string, 'danger' | 'warn' | 'info' | 'neutral'> = { error: 'danger', warn: 'warn', info: 'info', debug: 'neutral' }
</script>

<template>
  <div class="space-y-5">
    <div v-if="gw.state?.last_error" class="flex items-start gap-3 rounded-lg border border-danger/40 bg-danger-soft p-3 text-sm">
      <AlertTriangle class="mt-0.5 h-4 w-4 shrink-0 text-danger" />
      <div class="min-w-0">
        <p class="font-medium text-danger">最近一次下发失败{{ gw.state.retry_in_s ? `,${gw.state.retry_in_s} 秒后自动重试` : '' }}</p>
        <p class="mt-0.5 break-words text-xs text-fg-muted">{{ gw.state.last_error }}</p>
      </div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <Card v-for="c in gw.components" :key="c.name" body-class="p-4">
        <div class="flex items-start justify-between">
          <div>
            <p class="text-sm font-semibold">{{ compMeta[c.name]?.label ?? c.name }}</p>
            <p class="text-xs text-fg-subtle">{{ compMeta[c.name]?.hint }}</p>
          </div>
          <StatusDot :status="c.status" />
        </div>
        <div class="mt-3 flex items-center justify-between">
          <Badge :tone="statusTone[c.status]">{{ statusLabel[c.status] }}</Badge>
          <span class="text-[11px] text-fg-subtle">{{ c.last_apply ? `下发 ${fmtAgo(c.last_apply)}` : '' }}</span>
        </div>
        <p v-if="c.last_error" class="mt-2 line-clamp-2 text-xs text-danger" :title="c.last_error">{{ c.last_error }}</p>
      </Card>
    </div>

    <Card title="流量(近 1 小时)">
      <template #actions>
        <span v-if="live?.available" class="text-xs text-fg-muted">↓ {{ rate(live.down_bps) }} · ↑ {{ rate(live.up_bps) }}</span>
        <span v-else class="text-xs text-fg-subtle">Xray 未运行或无统计</span>
        <RouterLink to="/observe"><Button size="sm" variant="ghost">详情</Button></RouterLink>
      </template>
      <Sparkline :points="trafficQ.data.value?.series ?? []" />
      <div class="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-xs text-fg-muted">
        <span>本小时 ↓ {{ fmtBytes(trafficQ.data.value?.total_down ?? 0) }} ↑ {{ fmtBytes(trafficQ.data.value?.total_up ?? 0) }}</span>
        <span v-for="n in (trafficQ.data.value?.nodes ?? []).slice(0, 5)" :key="n.node_id">{{ nodeName(n.node_id) }}: {{ fmtBytes(n.up + n.down) }}</span>
      </div>
    </Card>

    <div class="grid gap-4 lg:grid-cols-3">
      <Card title="接管模式" class="lg:col-span-1">
        <template v-if="gw.state">
          <p class="text-base font-semibold">{{ modeText[gw.state.mode]?.name }}</p>
          <p class="mt-1 text-sm text-fg-muted">{{ modeText[gw.state.mode]?.desc }}</p>
          <RouterLink to="/network" class="mt-4 inline-flex items-center gap-1 text-sm text-accent hover:underline">网络与模式设置 <ArrowRight class="h-3.5 w-3.5" /></RouterLink>
        </template>
      </Card>

      <Card title="节点" class="lg:col-span-1">
        <template #actions><RouterLink to="/nodes"><Button size="sm" variant="ghost">管理</Button></RouterLink></template>
        <div v-if="nodesQ.isPending.value" class="text-sm text-fg-muted">加载中…</div>
        <div v-else-if="!nodesQ.data.value?.items.length" class="text-sm text-fg-muted">还没有节点。先到「节点」页添加或导入分享链接。</div>
        <ul v-else class="space-y-1.5">
          <li v-for="n in nodesQ.data.value!.items" :key="n.id" class="flex items-center gap-2 text-sm">
            <StatusDot :status="n.enabled ? (n.latency_ms === undefined ? 'unknown' : n.latency_ms < 0 ? 'bad' : 'ok') : 'disabled'" />
            <span class="truncate" :class="!n.enabled && 'text-fg-subtle line-through'">{{ n.name }}</span>
            <Badge v-if="n.is_default" tone="accent">默认</Badge>
            <span class="ml-auto text-xs text-fg-subtle uppercase">{{ n.protocol }}</span>
          </li>
        </ul>
        <p class="mt-3 text-xs text-fg-subtle">{{ enabledNodes.length }} 个启用 / {{ nodesQ.data.value?.items.length ?? 0 }} 个节点</p>
      </Card>

      <Card title="最近事件" class="lg:col-span-1">
        <template #actions><RouterLink to="/observe"><Button size="sm" variant="ghost">全部</Button></RouterLink></template>
        <ul v-if="eventsQ.data.value?.items.length" class="space-y-2">
          <li v-for="e in eventsQ.data.value!.items" :key="e.id" class="text-xs">
            <div class="flex items-center gap-2">
              <Badge :tone="levelTone[e.level]">{{ e.level }}</Badge>
              <span class="text-fg-subtle">{{ e.module }}</span>
              <span class="ml-auto text-fg-subtle" :title="fmtTime(e.ts)">{{ fmtAgo(e.ts) }}</span>
            </div>
            <p class="mt-0.5 break-words text-fg-muted">{{ e.message }}</p>
          </li>
        </ul>
        <p v-else class="text-sm text-fg-muted">暂无事件。</p>
      </Card>
    </div>

    <Card v-if="gw.state?.warnings.length" title="渲染警告">
      <ul class="list-disc space-y-1 pl-5 text-sm text-warn">
        <li v-for="w in gw.state!.warnings" :key="w">{{ w }}</li>
      </ul>
    </Card>
  </div>
</template>
