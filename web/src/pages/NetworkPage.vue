<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { AlertTriangle, CheckCircle2, RefreshCw, RotateCcw } from 'lucide-vue-next'
import Tabs from '@/components/ui/Tabs.vue'
import Card from '@/components/ui/Card.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Input from '@/components/ui/Input.vue'
import Textarea from '@/components/ui/Textarea.vue'
import Select from '@/components/ui/Select.vue'
import Field from '@/components/ui/Field.vue'
import Table from '@/components/ui/Table.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import { config, network } from '@/api/endpoints'
import type { Mode, NetworkConfig, PreflightResult } from '@/api/types'
import { ApiError } from '@/api/client'
import { useToast } from '@/stores/toast'
import { useGatewayState } from '@/stores/state'
import { fmtAgo } from '@/lib/utils'

const qc = useQueryClient()
const toast = useToast()
const gw = useGatewayState()
const tab = ref('mode')

const netQ = useQuery({ queryKey: ['network'], queryFn: config.network })
const ifQ = useQuery({ queryKey: ['interfaces'], queryFn: network.interfaces })
const ospfQ = useQuery({ queryKey: ['ospf'], queryFn: network.ospf, refetchInterval: 10_000 })
const routeState = ref(''); const routeQuery = ref('')
const routesQ = useQuery({ queryKey: ['routes', routeState, routeQuery], queryFn: () => network.routes({ state: routeState.value, q: routeQuery.value, limit: 500 }), enabled: computed(() => tab.value === 'ospf') })
watch(() => gw.lastEvent, e => { if (e?.type === 'apply.finished') { qc.invalidateQueries({ queryKey: ['routes'] }); qc.invalidateQueries({ queryKey: ['ospf'] }) } })

const current = computed(() => netQ.data.value?.item)

// --- Mode wizard ---
const modes: { id: Mode; name: string; tagline: string; fit: string; steps: string[]; loop: string }[] = [
  { id: 'A', name: 'Mode A · 全局网关', tagline: '最简单:把本机当旁路由,接管所有流量', fit: '主路由是普通家用路由器,不会 OSPF',
    steps: ['在主路由的 DHCP 里把默认网关和 DNS 都改成本机 IP(或只给某台设备手动设置)', '本机用 TPROXY 把所有流量交给 Xray,按规则分流', '设备策略、绕过目标只在此模式生效'], loop: '无环路风险。' },
  { id: 'B', name: 'Mode B · Fake-IP 旁路', tagline: '零延迟:只有命中代理规则的流量被引到本机', fit: '主路由支持 OSPF(RouterOS / OpenWrt / VyOS 等),不想折腾防环路',
    steps: ['设备网关仍指向主路由,但 DHCP 下发的 DNS 必须指向本机', '本机通过 OSPF 向主路由宣告 198.18.0.0/16 和 IP / GeoIP 规则', '命中代理域名时 DNS 返回 Fake-IP,主路由据此把流量送来'], loop: '节点真实 IP 不可能落在 198.18.0.0/16,天然免疫环路;IP 规则若包含节点地址会被自动排除。' },
  { id: 'C', name: 'Mode C · 真实 IP 旁路', tagline: '不用 Fake-IP:按真实目标 IP 分流', fit: '对 Fake-IP 敏感的应用(严格校验目标 IP 的游戏、P2P)',
    steps: ['设备网关和 DNS 都可以指向主路由', '域名规则周期解析成真实 IP,与 IP / GeoIP 规则一起经 OSPF 宣告', '主路由按真实 IP 把流量送到本机'], loop: '必须在主路由配置源地址绕过(PBR):来自本机 IP 的流量强制走 WAN,否则节点流量会被踢回本机形成死循环。' },
]
const selected = ref<Mode>('A')
watch(current, c => { if (c) selected.value = c.mode }, { immediate: true })
const preflight = ref<PreflightResult | null>(null)
const preflightBusy = ref(false)
watch(selected, async (m) => {
  preflight.value = null
  if (!current.value || m === current.value.mode) return
  preflightBusy.value = true
  try { preflight.value = await network.preflight(m) } catch (e) { toast.error('预检失败', (e as Error).message) } finally { preflightBusy.value = false }
}, { immediate: true })
const switchDialog = ref(false)
async function doSwitch() {
  if (!current.value) return
  await config.putNetwork({ ...current.value, mode: selected.value })
  toast.ok(`已切换到 Mode ${selected.value}`)
  qc.invalidateQueries({ queryKey: ['network'] }); qc.invalidateQueries({ queryKey: ['ospf'] })
}

// --- Settings form ---
const form = reactive<NetworkConfig>({ mode: 'A', management_iface: '', service_iface: '', fakeip_pool: '198.18.0.0/16', tproxy_port: 12345, lan_default_policy: 'proxy', ospf_push_batch: 500, ospf_push_interval_s: 2, ospf_resolve_workers: 8, ospf_publish_allowlist: [], node_failover_mode: 'normal' })
const allowText = ref('')
const errors = ref<Record<string, string>>({})
const busy = ref(false)
const dirty = ref(false)
watch(current, c => { if (c && !dirty.value) { Object.assign(form, c); allowText.value = c.ospf_publish_allowlist.join('\n') } }, { immediate: true })
watch([form, allowText], () => { dirty.value = true }, { deep: true })
const ifaceOptions = computed(() => [{ value: '', label: '未设置' }, ...(ifQ.data.value?.items ?? []).filter(i => !i.loopback).map(i => ({ value: i.name, label: `${i.name}${i.addresses.length ? ' · ' + i.addresses.filter(a => !a.includes(':')).join(', ') : ''}${i.up ? '' : '(down)'}` }))])
async function saveSettings() {
  errors.value = {}; busy.value = true
  try {
    await config.putNetwork({ ...form, mode: current.value?.mode ?? form.mode, tproxy_port: Number(form.tproxy_port), ospf_push_batch: Number(form.ospf_push_batch), ospf_push_interval_s: Number(form.ospf_push_interval_s), ospf_resolve_workers: Number(form.ospf_resolve_workers), ospf_publish_allowlist: allowText.value.split(/[\n,]/).map(s => s.trim()).filter(Boolean) })
    dirty.value = false
    toast.ok('网络设置已保存')
    qc.invalidateQueries({ queryKey: ['network'] }); qc.invalidateQueries({ queryKey: ['ospf'] })
  } catch (e) {
    if (e instanceof ApiError && e.field) errors.value[e.field] = e.message
    else toast.error('保存失败', (e as Error).message)
  } finally { busy.value = false }
}

// --- OSPF panel ---
const resetDialog = ref(false)
const stateTone = (s: string) => ({ published: 'ok', pending: 'warn', failed: 'danger', protected: 'info' } as const)[s as 'published'] ?? 'neutral'
const stateLabel: Record<string, string> = { published: '已宣告', pending: '待宣告', failed: '策略拒绝', protected: '受保护' }
const routerosHint = computed(() => `/routing rule add src-address=${ospfQ.data.value?.lan_prefix?.split('/')[0] ?? '<本机IP>'}/32 action=lookup-only-in-table table=bypass_proxy`)
</script>

<template>
  <div class="space-y-4">
    <Tabs v-model="tab" :tabs="[{ value: 'mode', label: '接管模式' }, { value: 'settings', label: '网卡与参数' }, { value: 'ospf', label: 'OSPF 状态' }]" />

    <!-- Mode wizard -->
    <div v-if="tab === 'mode'" class="space-y-4">
      <div class="grid gap-3 lg:grid-cols-3">
        <button v-for="m in modes" :key="m.id" type="button"
                :class="['rounded-lg border p-4 text-left transition-colors', selected === m.id ? 'border-accent bg-accent-soft/40' : 'border-border bg-surface hover:bg-surface-2']"
                @click="selected = m.id">
          <div class="flex items-center justify-between">
            <span class="font-semibold">{{ m.name }}</span>
            <Badge v-if="current?.mode === m.id" tone="ok">当前</Badge>
          </div>
          <p class="mt-1 text-sm text-fg-muted">{{ m.tagline }}</p>
          <p class="mt-2 text-xs text-fg-subtle">适合:{{ m.fit }}</p>
        </button>
      </div>

      <div v-for="m in modes.filter(x => x.id === selected)" :key="m.id" class="grid gap-4 lg:grid-cols-[1fr_22rem]">
        <Card :title="`${m.name}:主路由侧需要做什么`">
          <ol class="list-decimal space-y-1.5 pl-5 text-sm">
            <li v-for="s in m.steps" :key="s">{{ s }}</li>
          </ol>
          <p class="mt-3 rounded-md bg-surface-2 p-2.5 text-xs" :class="m.id === 'C' ? 'text-warn' : 'text-fg-muted'"><strong>环路:</strong>{{ m.loop }}</p>
          <div v-if="m.id === 'C'" class="mt-3">
            <p class="mb-1 text-xs text-fg-muted">RouterOS v7 示例(先建一张指向 WAN 的路由表 bypass_proxy):</p>
            <CodeBlock :code="routerosHint" />
          </div>
        </Card>
        <Card title="切换预检">
          <div v-if="current?.mode === selected" class="text-sm text-fg-muted">当前已是 Mode {{ selected }}。</div>
          <div v-else-if="preflightBusy" class="text-sm text-fg-muted">正在计算候选路由与冲突…</div>
          <div v-else-if="preflight" class="space-y-3 text-sm">
            <div class="flex items-center gap-2">
              <CheckCircle2 v-if="preflight.ok" class="h-4 w-4 text-ok" /><AlertTriangle v-else class="h-4 w-4 text-danger" />
              <span class="font-medium">{{ preflight.ok ? '可以切换' : '存在阻塞项' }}</span>
            </div>
            <p v-if="selected !== 'A'" class="text-xs text-fg-muted">切换后将宣告 {{ preflight.routes }} 条路由,{{ preflight.rejected }} 条候选被策略排除。</p>
            <ul v-if="preflight.problems?.length" class="list-disc space-y-1 pl-5 text-xs text-danger">
              <li v-for="p in preflight.problems" :key="p">{{ p }}</li>
            </ul>
            <ul v-if="preflight.warnings?.length" class="list-disc space-y-1 pl-5 text-xs text-warn">
              <li v-for="w in preflight.warnings" :key="w">{{ w }}</li>
            </ul>
            <details v-if="preflight.protected_conflicts?.length" class="text-xs">
              <summary class="cursor-pointer text-fg-muted">被排除的受保护前缀({{ preflight.protected_conflicts.length }})</summary>
              <ul class="mt-1 space-y-0.5 font-mono"><li v-for="c in preflight.protected_conflicts" :key="c">{{ c }}</li></ul>
            </details>
            <p v-if="!preflight.ok && preflight.problems?.some(p => p.includes('service interface'))" class="text-xs text-fg-muted">先到「网卡与参数」选择服务网卡。</p>
            <Button class="w-full" :disabled="!preflight.ok" :variant="selected === 'A' ? 'primary' : 'danger'" @click="switchDialog = true">切换到 Mode {{ selected }}</Button>
          </div>
        </Card>
      </div>
    </div>

    <!-- Settings -->
    <div v-else-if="tab === 'settings'" class="grid gap-4 lg:grid-cols-2">
      <Card title="网卡角色">
        <div class="space-y-4">
          <Field label="服务网卡(LAN 侧)" hint="OSPF 在此网段建邻居,TPROXY 从此口收流量" :error="errors.service_iface"><Select v-model="form.service_iface" :options="ifaceOptions" /></Field>
          <Field label="管理网卡" hint="可选;仅用于展示" :error="errors.management_iface"><Select v-model="form.management_iface" :options="ifaceOptions" /></Field>
          <Button variant="ghost" size="sm" @click="ifQ.refetch()"><RefreshCw class="h-3.5 w-3.5" /> 重新扫描网卡</Button>
        </div>
      </Card>
      <Card title="数据面参数">
        <div class="grid gap-4 sm:grid-cols-2">
          <Field label="TPROXY 端口" :error="errors.tproxy_port"><Input v-model="form.tproxy_port" type="number" class="font-mono" /></Field>
          <Field label="Fake-IP 池(Mode B)" :error="errors.fakeip_pool"><Input v-model="form.fakeip_pool" class="font-mono" /></Field>
          <Field label="LAN 默认策略" hint="未命中规则的流量" :error="errors.lan_default_policy"><Select v-model="form.lan_default_policy" :options="[{ value: 'proxy', label: '代理(Mode B/C 默认出站)' }, { value: 'direct', label: '直连' }]" /></Field>
          <Field label="节点失效策略" :error="errors.node_failover_mode"><Select v-model="form.node_failover_mode" :options="[{ value: 'normal', label: '普通:退回默认出站' }, { value: 'strict', label: '严格:拦截(宁断勿漏)' }]" /></Field>
        </div>
      </Card>
      <Card title="OSPF 宣告参数" class="lg:col-span-2">
        <div class="grid gap-4 sm:grid-cols-3">
          <Field label="每批路由数" :error="errors.ospf_push_batch"><Input v-model="form.ospf_push_batch" type="number" /></Field>
          <Field label="批间隔(秒)" :error="errors.ospf_push_interval_s"><Input v-model="form.ospf_push_interval_s" type="number" /></Field>
          <Field label="解析并发" :error="errors.ospf_resolve_workers"><Input v-model="form.ospf_resolve_workers" type="number" /></Field>
        </div>
        <Field class="mt-4" label="宣告白名单" hint="可选;非空时只宣告落在这些网段内的路由,每行一个 CIDR" :error="errors.ospf_publish_allowlist">
          <Textarea v-model="allowText" rows="3" class="font-mono text-xs" placeholder="留空 = 不限制" />
        </Field>
        <div class="mt-4 flex justify-end"><Button :loading="busy" :disabled="!dirty" @click="saveSettings">保存</Button></div>
      </Card>
    </div>

    <!-- OSPF panel -->
    <div v-else class="space-y-4">
      <div v-if="ospfQ.data.value && !ospfQ.data.value.enabled" class="rounded-lg border border-border bg-surface-2 p-3 text-sm text-fg-muted">当前为 Mode A,不宣告任何路由。切换到 Mode B/C 后这里显示邻居与路由。</div>
      <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Card v-for="s in ['published', 'pending', 'failed', 'protected'] as const" :key="s" body-class="p-4">
          <p class="text-xs text-fg-muted">{{ stateLabel[s] }}</p>
          <p class="mt-1 text-2xl font-semibold" :class="s === 'failed' && (ospfQ.data.value?.routes[s] ?? 0) > 0 ? 'text-danger' : ''">{{ ospfQ.data.value?.routes[s] ?? '-' }}</p>
        </Card>
      </div>
      <div class="grid gap-4 lg:grid-cols-[1fr_20rem]">
        <Card title="OSPF 邻居">
          <template #actions><span class="text-xs text-fg-subtle">{{ ospfQ.data.value?.service_iface || '未设服务网卡' }} {{ ospfQ.data.value?.lan_prefix ? '· ' + ospfQ.data.value.lan_prefix : '' }}</span></template>
          <p v-if="ospfQ.data.value?.neighbors_error" class="text-xs text-danger">{{ ospfQ.data.value.neighbors_error }}</p>
          <EmptyState v-else-if="!ospfQ.data.value?.neighbors.length" title="没有邻居" hint="主路由需要在同一网段启用 OSPF area 0;邻居出现后 1 分钟内应到 Full。" />
          <Table v-else :cols="[{ key: 'id', label: 'Router ID' }, { key: 'addr', label: '地址' }, { key: 'if', label: '接口' }, { key: 'st', label: '状态' }, { key: 'dead', label: 'Dead' }]" dense>
            <tr v-for="n in ospfQ.data.value!.neighbors" :key="n.router_id + n.address" class="font-mono text-xs">
              <td class="px-3 py-1.5">{{ n.router_id }}</td><td class="px-3 py-1.5">{{ n.address }}</td><td class="px-3 py-1.5">{{ n.interface }}</td>
              <td class="px-3 py-1.5"><Badge :tone="n.state.startsWith('Full') ? 'ok' : 'warn'">{{ n.state }}</Badge></td><td class="px-3 py-1.5">{{ n.dead_time }}</td>
            </tr>
          </Table>
        </Card>
        <Card title="域名解析">
          <dl class="grid grid-cols-2 gap-y-1 text-sm">
            <dt class="text-fg-muted">缓存条目</dt><dd>{{ ospfQ.data.value?.resolver.cached ?? '-' }}</dd>
            <dt class="text-fg-muted">已过期</dt><dd>{{ ospfQ.data.value?.resolver.expired ?? '-' }}</dd>
            <dt class="text-fg-muted">解析失败</dt><dd :class="(ospfQ.data.value?.resolver.failing ?? 0) > 0 ? 'text-warn' : ''">{{ ospfQ.data.value?.resolver.failing ?? '-' }}</dd>
          </dl>
          <p class="mt-3 text-xs text-fg-subtle">Mode C 通过本机 Mosdns 解析域名规则,TTL 夹在 5 分钟到 1 小时,到期自动刷新并更新宣告。</p>
          <Button variant="secondary" size="sm" class="mt-3" @click="resetDialog = true"><RotateCcw class="h-3.5 w-3.5" /> 清空并重新宣告</Button>
        </Card>
      </div>
      <Card title="路由" body-class="p-3">
        <template #actions>
          <Select v-model="routeState" class="w-32" :options="[{ value: '', label: '全部状态' }, ...Object.entries(stateLabel).map(([v, l]) => ({ value: v, label: l }))]" />
          <Input v-model="routeQuery" class="w-56 font-mono" placeholder="按前缀或来源过滤" />
        </template>
        <EmptyState v-if="!routesQ.data.value?.items.length" title="没有路由记录" />
        <Table v-else :cols="[{ key: 'c', label: '前缀' }, { key: 's', label: '状态' }, { key: 'src', label: '来源' }, { key: 'd', label: '说明' }, { key: 't', label: '更新' }]" dense>
          <tr v-for="r in routesQ.data.value!.items" :key="r.cidr" class="text-xs">
            <td class="px-3 py-1.5 font-mono">{{ r.cidr }}</td>
            <td class="px-3 py-1.5"><Badge :tone="stateTone(r.state)">{{ stateLabel[r.state] ?? r.state }}</Badge></td>
            <td class="px-3 py-1.5 text-fg-muted">{{ r.source }}</td>
            <td class="px-3 py-1.5 text-fg-muted break-words">{{ r.detail }}</td>
            <td class="px-3 py-1.5 text-fg-subtle">{{ fmtAgo(r.last_seen) }}</td>
          </tr>
        </Table>
      </Card>
    </div>

    <ConfirmDialog v-model:open="switchDialog" :title="`切换到 Mode ${selected}?`" :description="selected === 'A' ? '将撤回所有 OSPF 宣告并停止 FRR,流量接管改为依赖设备网关指向本机。' : '将重新生成 Xray / Mosdns / nftables 配置并开始 OSPF 宣告。请确认主路由侧已按说明配置。'" confirm-text="切换" :danger="selected !== 'A'" :action="doSwitch" />
    <ConfirmDialog v-model:open="resetDialog" title="清空路由记录并重新宣告?" description="已宣告的路由会在下一轮 reconcile 重新计算并对齐内核与 FRR,期间主路由可能短暂丢失部分路由。" confirm-text="清空" danger :action="network.resetRoutes" @done="qc.invalidateQueries({ queryKey: ['routes'] })" />
  </div>
</template>
