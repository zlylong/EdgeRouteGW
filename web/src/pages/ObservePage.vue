<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { Pause, Play, RefreshCw, RotateCcw } from 'lucide-vue-next'
import Sparkline from '@/components/ui/Sparkline.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import { useQuery as useQ } from '@tanstack/vue-query'
import { nodes as nodesApi } from '@/api/endpoints'
import Tabs from '@/components/ui/Tabs.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Input from '@/components/ui/Input.vue'
import Select from '@/components/ui/Select.vue'
import Table from '@/components/ui/Table.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { system } from '@/api/endpoints'
import type { Connection } from '@/api/types'
import { useGatewayState } from '@/stores/state'
import { fmtAgo, fmtBytes, fmtTime } from '@/lib/utils'
import type { Changeset, TrafficRates } from '@/api/types'

const qc = useQueryClient()
const gw = useGatewayState()
const tab = ref('connections')

// Connections: seed from REST, then live from SSE unless paused.
const connQ = useQuery({ queryKey: ['connections'], queryFn: () => system.connections(300) })
const paused = ref(false)
const filter = ref('')
const live = ref<Connection[]>([])
watch(() => connQ.data.value, d => { if (d) live.value = d.items }, { immediate: true })
watch(() => gw.liveConnections.length, () => {
  if (paused.value) return
  const newest = gw.liveConnections[0]
  if (newest && (!live.value[0] || live.value[0].at !== newest.at || live.value[0].source !== newest.source)) {
    live.value = [newest, ...live.value].slice(0, 500)
  }
})
const connections = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return q ? live.value.filter(c => (c.source + ' ' + c.target + ' ' + c.outbound).toLowerCase().includes(q)) : live.value
})
const outboundTone = (o: string) => o === 'direct' ? 'ok' : o === 'block' ? 'danger' : o.startsWith('group-') ? 'info' : 'accent'

// Events
const module = ref(''); const level = ref('')
const eventsQ = useQuery({ queryKey: ['events', module, level], queryFn: () => system.events({ module: module.value, level: level.value, limit: 300 }), refetchInterval: 10_000 })
const levelTone: Record<string, 'danger' | 'warn' | 'info' | 'neutral'> = { error: 'danger', warn: 'warn', info: 'info', debug: 'neutral' }

// Changesets & apply log
const changesetsQ = useQuery({ queryKey: ['changesets'], queryFn: () => system.changesets(200) })
const applyQ = useQuery({ queryKey: ['apply-log'], queryFn: () => system.applyLog(undefined, 300) })
watch(() => gw.lastEvent, (e) => {
  if (e?.type === 'apply.finished' || e?.type === 'apply.failed') { qc.invalidateQueries({ queryKey: ['apply-log'] }); qc.invalidateQueries({ queryKey: ['events'] }) }
  if (e?.type === 'state.changed') qc.invalidateQueries({ queryKey: ['changesets'] })
})
// Traffic
const range = ref<'1h' | '24h' | '7d' | '30d'>('24h')
const trafficQ = useQ({ queryKey: ['traffic', range], queryFn: () => system.traffic(range.value), refetchInterval: 60_000 })
const nodesQ = useQ({ queryKey: ['nodes'], queryFn: nodesApi.list })
const liveRates = ref<TrafficRates | null>(null)
watch(() => gw.lastEvent, e => { if (e?.type === 'traffic.sample') liveRates.value = e.data as TrafficRates })
const nodeName = (id: string) => nodesQ.data.value?.items.find(n => n.id === id)?.name ?? id.slice(0, 8)
const rate = (bps: number) => fmtBytes(Math.round(bps)) + '/s'
// Revert
const revertTarget = ref<Changeset | null>(null)
async function doRevert() {
  if (!revertTarget.value) return
  await system.revert(revertTarget.value.id)
  qc.invalidateQueries({ queryKey: ['changesets'] })
  for (const k of ['nodes', 'groups', 'rule-sets', 'rules', 'device-policies', 'bypass', 'network', 'dns']) qc.invalidateQueries({ queryKey: [k] })
}
const actionTone = (a: string) => a.includes('restart') ? 'warn' : a.includes('hot') ? 'ok' : 'info'
</script>

<template>
  <div class="space-y-4">
    <Tabs v-model="tab" :tabs="[{ value: 'connections', label: '连接' }, { value: 'events', label: '事件' }, { value: 'changes', label: '变更历史' }, { value: 'apply', label: '下发记录' }, { value: 'traffic', label: '流量' }]" />

    <div v-if="tab === 'connections'">
      <div class="mb-3 flex flex-wrap items-center gap-2">
        <Input v-model="filter" placeholder="按客户端、目标或出站过滤" class="w-72 font-mono" />
        <Button variant="secondary" size="sm" @click="paused = !paused">
          <Pause v-if="!paused" class="h-3.5 w-3.5" /><Play v-else class="h-3.5 w-3.5" /> {{ paused ? '继续' : '暂停' }}
        </Button>
        <span class="text-xs text-fg-subtle">{{ connections.length }} 条 · 来自 Xray access log,实时推送</span>
      </div>
      <EmptyState v-if="!connections.length" title="还没有连接记录" hint="Xray 启动并有流量经过后,这里会实时显示每条连接的来源、目标和出站。" />
      <Table v-else :cols="[{ key: 't', label: '时间', class: 'w-24' }, { key: 's', label: '客户端' }, { key: 'd', label: '目标' }, { key: 'o', label: '出站' }]" dense>
        <tr v-for="(c, i) in connections" :key="c.at + c.source + i" :class="['font-mono text-xs', !c.accepted && 'text-fg-subtle']">
          <td class="px-3 py-1 text-fg-subtle" :title="fmtTime(c.at)">{{ fmtAgo(c.at) }}</td>
          <td class="px-3 py-1">{{ c.source }}</td>
          <td class="px-3 py-1"><span class="mr-1 rounded bg-surface-2 px-1 text-[10px] uppercase text-fg-muted">{{ c.network }}</span>{{ c.target }}</td>
          <td class="px-3 py-1"><Badge :tone="outboundTone(c.outbound)">{{ c.outbound }}</Badge></td>
        </tr>
      </Table>
    </div>

    <div v-else-if="tab === 'events'">
      <div class="mb-3 flex flex-wrap items-center gap-2">
        <Select v-model="module" class="w-40" :options="[{ value: '', label: '全部模块' }, { value: 'reconcile', label: 'reconcile' }, { value: 'auth', label: 'auth' }]" />
        <Select v-model="level" class="w-32" :options="[{ value: '', label: '全部级别' }, { value: 'error', label: 'error' }, { value: 'warn', label: 'warn' }, { value: 'info', label: 'info' }]" />
        <Button variant="ghost" size="icon" aria-label="刷新" @click="eventsQ.refetch()"><RefreshCw class="h-4 w-4" /></Button>
      </div>
      <EmptyState v-if="!eventsQ.data.value?.items.length" title="没有事件" />
      <Table v-else :cols="[{ key: 't', label: '时间', class: 'w-40' }, { key: 'l', label: '级别', class: 'w-20' }, { key: 'm', label: '模块', class: 'w-28' }, { key: 'msg', label: '消息' }]" dense>
        <tr v-for="e in eventsQ.data.value!.items" :key="e.id" class="text-xs">
          <td class="px-3 py-1.5 text-fg-subtle">{{ fmtTime(e.ts) }}</td>
          <td class="px-3 py-1.5"><Badge :tone="levelTone[e.level]">{{ e.level }}</Badge></td>
          <td class="px-3 py-1.5 text-fg-muted">{{ e.module }}/{{ e.type }}</td>
          <td class="px-3 py-1.5 break-words">{{ e.message }}<span v-if="e.actor" class="ml-2 text-fg-subtle">by {{ e.actor }}</span></td>
        </tr>
      </Table>
    </div>

    <div v-else-if="tab === 'changes'">
      <p class="mb-3 text-sm text-fg-muted">每次写操作生成一个 generation,并保存当时的完整配置快照(最近 200 次)。「回滚到此」会把配置恢复成该次变更之后的样子,作为一次新的变更下发。</p>
      <EmptyState v-if="!changesetsQ.data.value?.items.length" title="还没有变更" />
      <Table v-else :cols="[{ key: 'g', label: 'gen', class: 'w-16' }, { key: 't', label: '时间', class: 'w-40' }, { key: 'a', label: '操作者', class: 'w-32' }, { key: 's', label: '摘要' }, { key: 'st', label: '状态', class: 'w-24' }, { key: 'ops', label: '', class: 'w-28' }]" dense>
        <tr v-for="(c, i) in changesetsQ.data.value!.items" :key="c.id" :class="['text-xs', c.reverted_by && 'text-fg-subtle']">
          <td class="px-3 py-1.5 font-mono">{{ c.generation }}</td>
          <td class="px-3 py-1.5 text-fg-subtle">{{ fmtTime(c.created_at) }}</td>
          <td class="px-3 py-1.5 text-fg-muted">{{ c.actor || '-' }}</td>
          <td class="px-3 py-1.5">{{ c.summary }}<span v-if="c.reverted_by" class="ml-2 text-fg-subtle">(已被回滚)</span></td>
          <td class="px-3 py-1.5"><Badge :tone="(gw.state?.last_applied_generation ?? 0) >= c.generation ? 'ok' : 'warn'">{{ (gw.state?.last_applied_generation ?? 0) >= c.generation ? '已生效' : '待下发' }}</Badge></td>
          <td class="px-3 py-1.5 text-right"><Button v-if="i > 0 && c.diff === 'snapshot'" variant="ghost" size="sm" @click="revertTarget = c"><RotateCcw class="h-3.5 w-3.5" /> 回滚到此</Button></td>
        </tr>
      </Table>
    </div>

    <div v-else-if="tab === 'traffic'" class="space-y-4">
      <div class="flex flex-wrap items-center gap-3">
        <Select v-model="range" class="w-32" :options="[{ value: '1h', label: '近 1 小时' }, { value: '24h', label: '近 24 小时' }, { value: '7d', label: '近 7 天' }, { value: '30d', label: '近 30 天' }]" />
        <span v-if="liveRates?.available" class="text-sm text-fg-muted">实时 ↓ {{ rate(liveRates.down_bps) }} · ↑ {{ rate(liveRates.up_bps) }}</span>
        <span v-else class="text-sm text-fg-subtle">Xray 未运行或统计不可用(统计每 10 秒采样,每分钟落库)</span>
        <span class="ml-auto text-sm">合计 ↓ {{ fmtBytes(trafficQ.data.value?.total_down ?? 0) }} ↑ {{ fmtBytes(trafficQ.data.value?.total_up ?? 0) }}</span>
      </div>
      <div class="rounded-lg border border-border bg-surface p-4"><Sparkline :points="trafficQ.data.value?.series ?? []" :height="120" /></div>
      <EmptyState v-if="!trafficQ.data.value?.nodes.length" title="没有按节点的流量" />
      <Table v-else :cols="[{ key: 'n', label: '节点' }, { key: 'd', label: '下行' }, { key: 'u', label: '上行' }, { key: 'r', label: '实时' }]" dense>
        <tr v-for="n in trafficQ.data.value!.nodes" :key="n.node_id" class="text-xs">
          <td class="px-3 py-1.5 font-medium">{{ nodeName(n.node_id) }}</td>
          <td class="px-3 py-1.5">{{ fmtBytes(n.down) }}</td>
          <td class="px-3 py-1.5">{{ fmtBytes(n.up) }}</td>
          <td class="px-3 py-1.5 text-fg-muted">{{ liveRates?.nodes['node-' + n.node_id] ? `↓ ${rate(liveRates.nodes['node-' + n.node_id].down_bps)} ↑ ${rate(liveRates.nodes['node-' + n.node_id].up_bps)}` : '-' }}</td>
        </tr>
      </Table>
    </div>

    <div v-else>
      <EmptyState v-if="!applyQ.data.value?.items.length" title="还没有下发记录" hint="Reconciler 每对一个组件执行动作就会记一条。" />
      <Table v-else :cols="[{ key: 't', label: '时间', class: 'w-40' }, { key: 'g', label: 'gen', class: 'w-16' }, { key: 'c', label: '组件', class: 'w-24' }, { key: 'a', label: '动作' }, { key: 'd', label: '耗时', class: 'w-20' }, { key: 'r', label: '结果' }]" dense>
        <tr v-for="a in applyQ.data.value!.items" :key="a.id" class="text-xs">
          <td class="px-3 py-1.5 text-fg-subtle">{{ fmtTime(a.ts) }}</td>
          <td class="px-3 py-1.5 font-mono">{{ a.generation }}</td>
          <td class="px-3 py-1.5 uppercase">{{ a.component }}</td>
          <td class="px-3 py-1.5"><Badge :tone="actionTone(a.action)">{{ a.action }}</Badge></td>
          <td class="px-3 py-1.5 text-fg-muted">{{ a.duration_ms }} ms</td>
          <td class="px-3 py-1.5"><Badge v-if="a.ok" tone="ok">成功</Badge><span v-else class="text-danger break-words">{{ a.error }}</span></td>
        </tr>
      </Table>
    </div>
    <ConfirmDialog v-if="revertTarget" :open="!!revertTarget" :title="`回滚到 gen ${revertTarget.generation}?`" :description="`配置会恢复成「${revertTarget.summary}」之后的状态,并作为新的变更立即下发。`" confirm-text="回滚" danger :action="doRevert" @update:open="v => { if (!v) revertTarget = null }" />
  </div>
</template>
