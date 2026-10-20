<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { Pause, Play, RefreshCw } from 'lucide-vue-next'
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
import { fmtAgo, fmtTime } from '@/lib/utils'

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
const actionTone = (a: string) => a.includes('restart') ? 'warn' : a.includes('hot') ? 'ok' : 'info'
</script>

<template>
  <div class="space-y-4">
    <Tabs v-model="tab" :tabs="[{ value: 'connections', label: '连接' }, { value: 'events', label: '事件' }, { value: 'changes', label: '变更历史' }, { value: 'apply', label: '下发记录' }]" />

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
      <p class="mb-3 text-sm text-fg-muted">每次写操作生成一个 generation。撤销功能将在后续版本提供。</p>
      <EmptyState v-if="!changesetsQ.data.value?.items.length" title="还没有变更" />
      <Table v-else :cols="[{ key: 'g', label: 'gen', class: 'w-16' }, { key: 't', label: '时间', class: 'w-40' }, { key: 'a', label: '操作者', class: 'w-32' }, { key: 's', label: '摘要' }, { key: 'st', label: '状态', class: 'w-24' }]" dense>
        <tr v-for="c in changesetsQ.data.value!.items" :key="c.id" class="text-xs">
          <td class="px-3 py-1.5 font-mono">{{ c.generation }}</td>
          <td class="px-3 py-1.5 text-fg-subtle">{{ fmtTime(c.created_at) }}</td>
          <td class="px-3 py-1.5 text-fg-muted">{{ c.actor || '-' }}</td>
          <td class="px-3 py-1.5">{{ c.summary }}</td>
          <td class="px-3 py-1.5"><Badge :tone="(gw.state?.last_applied_generation ?? 0) >= c.generation ? 'ok' : 'warn'">{{ (gw.state?.last_applied_generation ?? 0) >= c.generation ? '已生效' : '待下发' }}</Badge></td>
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
  </div>
</template>
