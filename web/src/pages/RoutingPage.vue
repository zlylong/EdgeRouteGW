<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { ChevronDown, ChevronUp, GripVertical, Pencil, Plus, Trash2 } from 'lucide-vue-next'
import Tabs from '@/components/ui/Tabs.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Switch from '@/components/ui/Switch.vue'
import Input from '@/components/ui/Input.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Spinner from '@/components/ui/Spinner.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Field from '@/components/ui/Field.vue'
import Table from '@/components/ui/Table.vue'
import RuleDialog from '@/components/routing/RuleDialog.vue'
import TraceBox from '@/components/routing/TraceBox.vue'
import DevicePolicyDialog from '@/components/routing/DevicePolicyDialog.vue'
import { config as cfgApi, groups as groupsApi, nodes as nodesApi, routing } from '@/api/endpoints'
import type { BypassDestination, DevicePolicy, Rule, RuleSet } from '@/api/types'
import { useToast } from '@/stores/toast'
import { useGatewayState } from '@/stores/state'

const qc = useQueryClient()
const toast = useToast()
const gw = useGatewayState()
const tab = ref('rules')

const setsQ = useQuery({ queryKey: ['rule-sets'], queryFn: routing.sets })
const rulesQ = useQuery({ queryKey: ['rules'], queryFn: () => routing.rules() })
const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: nodesApi.list })
const groupsQ = useQuery({ queryKey: ['groups'], queryFn: groupsApi.list })
const devicesQ = useQuery({ queryKey: ['device-policies'], queryFn: routing.devices })
const bypassQ = useQuery({ queryKey: ['bypass'], queryFn: routing.bypass })
const refresh = () => ['rule-sets', 'rules', 'device-policies', 'bypass'].forEach(k => qc.invalidateQueries({ queryKey: [k] }))

const sets = computed(() => setsQ.data.value?.items ?? [])
const allRules = computed(() => rulesQ.data.value?.items ?? [])
const nodes = computed(() => nodesQ.data.value?.items ?? [])
const groups = computed(() => groupsQ.data.value?.items ?? [])
const selectedSet = ref<string>('')
watch(sets, (s) => { if (!selectedSet.value || !s.some(x => x.id === selectedSet.value)) selectedSet.value = s[0]?.id ?? '' }, { immediate: true })
const rules = computed(() => allRules.value.filter(r => r.set_id === selectedSet.value))
const countBySet = computed(() => allRules.value.reduce<Record<string, number>>((m, r) => { m[r.set_id] = (m[r.set_id] ?? 0) + 1; return m }, {}))

// Dialog state
const ruleDialog = ref(false); const editRule = ref<Rule | null>(null)
const setDialog = ref(false); const setName = ref(''); const setBusy = ref(false)
const deviceDialog = ref(false); const editDevice = ref<DevicePolicy | null>(null)
const bypassForm = ref({ cidr: '', remark: '' }); const bypassBusy = ref(false)
const confirm = ref<{ title: string; desc: string; action: () => Promise<unknown> } | null>(null)
const highlight = ref<string | null>(null)

const actionLabel = (r: Rule) => {
  switch (r.action_kind) {
    case 'node': return `节点 · ${nodes.value.find(n => n.id === r.action_ref)?.name ?? r.action_ref?.slice(0, 8)}`
    case 'group': return `出站组 · ${groups.value.find(g => g.id === r.action_ref)?.name ?? r.action_ref?.slice(0, 8)}`
    case 'direct': return '直连'
    case 'block': return '拦截'
    default: return '代理'
  }
}
const actionTone = (k: string) => ({ direct: 'ok', block: 'danger', proxy: 'accent', node: 'accent', group: 'info' } as const)[k as 'direct'] ?? 'neutral'
const kindLabel: Record<string, string> = { domain: '域名', geosite: 'geosite', ip: 'IP', geoip: 'geoip' }

async function createSet() {
  setBusy.value = true
  try { const r = await routing.createSet(setName.value); setDialog.value = false; setName.value = ''; refresh(); selectedSet.value = r.item?.id ?? selectedSet.value } catch (e) { toast.error('创建失败', (e as Error).message) } finally { setBusy.value = false }
}
async function toggleSet(s: RuleSet, enabled: boolean) { try { await routing.updateSet(s.id, { enabled }); refresh() } catch (e) { toast.error('操作失败', (e as Error).message) } }
function removeSet(s: RuleSet) { confirm.value = { title: `删除规则集「${s.name}」?`, desc: `其中 ${countBySet.value[s.id] ?? 0} 条规则会一起删除。`, action: () => routing.removeSet(s.id) } }
async function moveSet(i: number, d: number) {
  const ids = sets.value.map(s => s.id); const j = i + d
  if (j < 0 || j >= ids.length) return
  if (sets.value[i].builtin || sets.value[j].builtin) { toast.warn('内置默认规则集永远排在第一位'); return }
  ;[ids[i], ids[j]] = [ids[j], ids[i]]
  try { await routing.reorderSets(ids); refresh() } catch (e) { toast.error('排序失败', (e as Error).message) }
}
// The final fallback ("none of the rules matched") is the LAN default
// policy; it is shown as the last, fixed row of the built-in set.
const netQ = useQuery({ queryKey: ['network'], queryFn: cfgApi.network })
const fallback = computed(() => netQ.data.value?.item.lan_default_policy ?? 'direct')
const fallbackBusy = ref(false)
async function setFallback(v: string) {
  const cur = netQ.data.value?.item
  if (!cur || (v !== 'direct' && v !== 'proxy')) return
  fallbackBusy.value = true
  try { await cfgApi.putNetwork({ ...cur, lan_default_policy: v }); qc.invalidateQueries({ queryKey: ['network'] }); toast.ok(v === 'direct' ? '未命中的流量将直连' : '未命中的流量将走代理') } catch (e) { toast.error('修改失败', (e as Error).message) } finally { fallbackBusy.value = false }
}
const selectedIsBuiltin = computed(() => !!sets.value.find(s => s.id === selectedSet.value)?.builtin)
async function toggleRule(r: Rule, enabled: boolean) {
  try { await routing.updateRule(r.id, { set_id: r.set_id, match_kind: r.match_kind, match_value: r.match_value, action_kind: r.action_kind, action_ref: r.action_ref || undefined, remark: r.remark, enabled }); refresh() } catch (e) { toast.error('操作失败', (e as Error).message) }
}
function removeRule(r: Rule) { confirm.value = { title: '删除这条规则?', desc: `${kindLabel[r.match_kind]}:${r.match_value}`, action: () => routing.removeRule(r.id) } }

// Drag-and-drop reorder within the selected set; one request per drop.
const dragId = ref<string | null>(null); const overId = ref<string | null>(null)
function onDrop() {
  if (!dragId.value || !overId.value || dragId.value === overId.value) { dragId.value = overId.value = null; return }
  const ids = rules.value.map(r => r.id)
  const from = ids.indexOf(dragId.value); const to = ids.indexOf(overId.value)
  ids.splice(to, 0, ids.splice(from, 1)[0])
  dragId.value = overId.value = null
  reorder(ids)
}
async function moveRule(i: number, d: number) {
  const ids = rules.value.map(r => r.id); const j = i + d
  if (j < 0 || j >= ids.length) return
  ;[ids[i], ids[j]] = [ids[j], ids[i]]
  await reorder(ids)
}
async function reorder(ids: string[]) {
  // Optimistic: reflect the order immediately, then confirm with the server.
  qc.setQueryData(['rules'], (old: { items: Rule[] } | undefined) => old && ({ items: [...old.items].sort((a, b) => a.set_id !== b.set_id ? 0 : ids.indexOf(a.id) - ids.indexOf(b.id)) }))
  try { await routing.reorderRules(selectedSet.value, ids) } catch (e) { toast.error('排序失败', (e as Error).message) } finally { refresh() }
}
function onMatched(id: string | null) {
  highlight.value = id
  if (!id) return
  const r = allRules.value.find(x => x.id === id)
  if (r && r.set_id !== selectedSet.value) selectedSet.value = r.set_id
  nextTick(() => document.getElementById(`rule-${id}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }))
}

const devices = computed(() => devicesQ.data.value?.items ?? [])
const bypass = computed(() => bypassQ.data.value?.items ?? [])
function removeDevice(d: DevicePolicy) { confirm.value = { title: '删除设备策略?', desc: `${d.value} · ${d.action}`, action: () => routing.removeDevice(d.id) } }
function removeBypass(b: BypassDestination) { confirm.value = { title: '删除绕过目标?', desc: b.cidr, action: () => routing.removeBypass(b.id) } }
async function addBypass() {
  bypassBusy.value = true
  try { await routing.createBypass(bypassForm.value.cidr, bypassForm.value.remark); bypassForm.value = { cidr: '', remark: '' }; refresh() } catch (e) { toast.error('添加失败', (e as Error).message) } finally { bypassBusy.value = false }
}
const deviceActionTone = (a: string) => ({ proxy: 'accent', direct: 'ok', reject: 'danger' } as const)[a as 'proxy'] ?? 'neutral'
</script>

<template>
  <div class="space-y-4">
    <TraceBox @matched="onMatched" />
    <Tabs v-model="tab" :tabs="[{ value: 'rules', label: '规则', count: allRules.length }, { value: 'devices', label: '设备策略', count: devices.length }, { value: 'bypass', label: '绕过目标', count: bypass.length }]" />

    <div v-if="tab === 'rules'" class="grid gap-4 lg:grid-cols-[16rem_1fr]">
      <!-- Rule sets -->
      <aside class="rounded-lg border border-border bg-surface">
        <div class="flex items-center justify-between border-b border-border px-3 py-2">
          <span class="text-xs font-semibold uppercase tracking-wide text-fg-muted">规则集(按序匹配)</span>
          <Button variant="ghost" size="icon" aria-label="新建规则集" @click="setDialog = true"><Plus class="h-4 w-4" /></Button>
        </div>
        <Spinner v-if="setsQ.isPending.value" />
        <ul v-else class="p-1.5">
          <li v-for="(s, i) in sets" :key="s.id"
              :class="['group flex items-center gap-1.5 rounded-md px-2 py-1.5 text-sm cursor-pointer', s.id === selectedSet ? 'bg-accent-soft text-accent' : 'hover:bg-surface-2']"
              @click="selectedSet = s.id">
            <span class="truncate flex-1" :class="!s.enabled && 'line-through text-fg-subtle'">{{ s.name }}<span v-if="s.builtin" class="ml-1 rounded bg-surface-2 px-1 text-[10px] font-normal text-fg-muted" title="内置默认规则集:永远第一个匹配,不能删除或移动;其中的规则可以编辑">内置</span></span>
            <span class="text-[10px] text-fg-subtle">{{ countBySet[s.id] ?? 0 }}</span>
            <span v-if="!s.builtin" class="hidden gap-0.5 group-hover:flex">
              <button class="rounded p-0.5 hover:bg-surface" aria-label="上移" @click.stop="moveSet(i, -1)"><ChevronUp class="h-3.5 w-3.5" /></button>
              <button class="rounded p-0.5 hover:bg-surface" aria-label="下移" @click.stop="moveSet(i, 1)"><ChevronDown class="h-3.5 w-3.5" /></button>
            </span>
            <Switch :model-value="s.enabled" :label="`启用 ${s.name}`" @update:model-value="v => toggleSet(s, v)" @click.stop />
          </li>
          <li v-if="!sets.length" class="px-2 py-6 text-center text-xs text-fg-subtle">还没有规则集</li>
        </ul>
      </aside>

      <!-- Rules of the selected set -->
      <section>
        <div class="mb-3 flex items-center justify-between">
          <div class="text-sm text-fg-muted">
            <template v-if="selectedSet">「{{ sets.find(s => s.id === selectedSet)?.name }}」· {{ rules.length }} 条,拖动行或用箭头调整顺序</template>
          </div>
          <div class="flex gap-2">
            <Button v-if="selectedSet && !sets.find(s => s.id === selectedSet)?.builtin" variant="ghost" size="sm" class="text-danger" @click="removeSet(sets.find(s => s.id === selectedSet)!)"><Trash2 class="h-4 w-4" /> 删除规则集</Button>
            <Button :disabled="!selectedSet" @click="editRule = null; ruleDialog = true"><Plus class="h-4 w-4" /> 添加规则</Button>
          </div>
        </div>
        <Spinner v-if="rulesQ.isPending.value" />
        <EmptyState v-else-if="!selectedSet" title="先创建一个规则集" hint="规则集是有序的规则分组;流量按规则集顺序、再按集内顺序匹配,第一条命中即生效。">
          <Button @click="setDialog = true"><Plus class="h-4 w-4" /> 新建规则集</Button>
        </EmptyState>
        <EmptyState v-else-if="!rules.length && !selectedIsBuiltin" title="这个规则集还没有规则" hint="添加域名、GeoSite、IP 或 GeoIP 规则;所有规则集都没命中时按「默认」集最后一行的兜底处理。" />
        <Table v-else-if="rules.length" :cols="[{ key: 'drag', label: '', class: 'w-8' }, { key: 'match', label: '匹配' }, { key: 'action', label: '动作' }, { key: 'remark', label: '备注' }, { key: 'on', label: '启用', class: 'w-16' }, { key: 'ops', label: '', class: 'w-36' }]" dense>
          <tr v-for="(r, i) in rules" :id="`rule-${r.id}`" :key="r.id"
              draggable="true"
              :class="['hover:bg-surface-2/60 transition-colors', dragId === r.id && 'dragging', overId === r.id && 'drag-over', highlight === r.id && 'bg-accent-soft/60']"
              @dragstart="dragId = r.id" @dragover.prevent="overId = r.id" @dragleave="overId === r.id && (overId = null)" @drop.prevent="onDrop" @dragend="dragId = overId = null">
            <td class="px-2 py-1.5 text-fg-subtle cursor-grab"><GripVertical class="h-4 w-4" /></td>
            <td class="px-3 py-1.5">
              <span class="mr-1.5 rounded bg-surface-2 px-1.5 py-0.5 text-[10px] uppercase text-fg-muted">{{ kindLabel[r.match_kind] }}</span>
              <code class="font-mono text-xs" :class="!r.enabled && 'text-fg-subtle line-through'">{{ r.match_value }}</code>
            </td>
            <td class="px-3 py-1.5"><Badge :tone="actionTone(r.action_kind)">{{ actionLabel(r) }}</Badge></td>
            <td class="px-3 py-1.5 text-xs text-fg-muted">{{ r.remark }}</td>
            <td class="px-3 py-1.5"><Switch :model-value="r.enabled" label="启用" @update:model-value="v => toggleRule(r, v)" /></td>
            <td class="px-2 py-1.5 text-right whitespace-nowrap">
              <Button variant="ghost" size="icon" aria-label="上移" @click="moveRule(i, -1)"><ChevronUp class="h-3.5 w-3.5" /></Button>
              <Button variant="ghost" size="icon" aria-label="下移" @click="moveRule(i, 1)"><ChevronDown class="h-3.5 w-3.5" /></Button>
              <Button variant="ghost" size="icon" aria-label="编辑" @click="editRule = r; ruleDialog = true"><Pencil class="h-3.5 w-3.5" /></Button>
              <Button variant="ghost" size="icon" class="text-danger" aria-label="删除" @click="removeRule(r)"><Trash2 class="h-3.5 w-3.5" /></Button>
            </td>
          </tr>
        </Table>
        <!-- Fallback row: fixed, lives in the built-in set -->
        <div v-if="selectedIsBuiltin" class="mt-2 flex flex-wrap items-center gap-3 rounded-lg border border-dashed border-border bg-surface-2/40 px-3 py-2 text-sm">
          <span class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] uppercase text-fg-muted">兜底</span>
          <span class="text-fg-muted">所有规则集都没有命中的流量</span>
          <span class="text-fg-subtle">&rarr;</span>
          <select :value="fallback" :disabled="fallbackBusy" class="h-8 rounded-md border border-border bg-surface px-2 text-sm" @change="setFallback(($event.target as HTMLSelectElement).value)">
            <option value="direct">直连</option>
            <option value="proxy">代理(默认节点)</option>
          </select>
          <span class="text-xs text-fg-subtle">固定为最后一条,不能删除;等同于「网络与模式」里的 LAN 默认策略</span>
        </div>
      </section>
    </div>

    <div v-else-if="tab === 'devices'">
      <div class="mb-3 flex items-center justify-between">
        <p class="text-sm text-fg-muted">按来源设备覆盖默认策略。{{ gw.state?.mode !== 'A' ? `当前为 Mode ${gw.state?.mode},设备策略不会下发(主路由把流量引到本机,绕过会形成环路)。` : '' }}</p>
        <Button @click="editDevice = null; deviceDialog = true"><Plus class="h-4 w-4" /> 添加设备策略</Button>
      </div>
      <EmptyState v-if="!devices.length" title="没有设备策略" hint="例如:电视直连不走代理、访客设备拦截、某台电脑强制代理。" />
      <Table v-else :cols="[{ key: 'v', label: '设备' }, { key: 'a', label: '动作' }, { key: 'r', label: '备注' }, { key: 'ops', label: '', class: 'w-24' }]">
        <tr v-for="d in devices" :key="d.id" class="hover:bg-surface-2/60">
          <td class="px-3 py-2"><span class="mr-1.5 rounded bg-surface-2 px-1.5 py-0.5 text-[10px] uppercase text-fg-muted">{{ d.kind }}</span><code class="font-mono text-xs">{{ d.value }}</code></td>
          <td class="px-3 py-2"><Badge :tone="deviceActionTone(d.action)">{{ { proxy: '代理', direct: '直连', reject: '拦截' }[d.action] }}</Badge></td>
          <td class="px-3 py-2 text-xs text-fg-muted">{{ d.remark }}</td>
          <td class="px-3 py-2 text-right whitespace-nowrap">
            <Button variant="ghost" size="icon" aria-label="编辑" @click="editDevice = d; deviceDialog = true"><Pencil class="h-3.5 w-3.5" /></Button>
            <Button variant="ghost" size="icon" class="text-danger" aria-label="删除" @click="removeDevice(d)"><Trash2 class="h-3.5 w-3.5" /></Button>
          </td>
        </tr>
      </Table>
    </div>

    <div v-else>
      <p class="mb-3 text-sm text-fg-muted">Mode A 下这些目标 IP 永远直连、不进 Xray,防止规则改错把管理入口切断。{{ gw.state?.mode !== 'A' ? '当前模式不下发此列表。' : '' }}</p>
      <form class="mb-4 flex flex-wrap items-end gap-2" @submit.prevent="addBypass">
        <Field label="IPv4 / CIDR" class="w-56"><Input v-model="bypassForm.cidr" class="font-mono" placeholder="203.0.113.5 或 10.0.0.0/8" /></Field>
        <Field label="备注" class="w-56"><Input v-model="bypassForm.remark" placeholder="可选" /></Field>
        <Button type="submit" :loading="bypassBusy" :disabled="!bypassForm.cidr"><Plus class="h-4 w-4" /> 添加</Button>
      </form>
      <EmptyState v-if="!bypass.length" title="没有绕过目标" />
      <Table v-else :cols="[{ key: 'c', label: 'CIDR' }, { key: 'r', label: '备注' }, { key: 'ops', label: '', class: 'w-16' }]">
        <tr v-for="b in bypass" :key="b.id" class="hover:bg-surface-2/60">
          <td class="px-3 py-2 font-mono text-xs">{{ b.cidr }}</td>
          <td class="px-3 py-2 text-xs text-fg-muted">{{ b.remark }}</td>
          <td class="px-3 py-2 text-right"><Button variant="ghost" size="icon" class="text-danger" aria-label="删除" @click="removeBypass(b)"><Trash2 class="h-3.5 w-3.5" /></Button></td>
        </tr>
      </Table>
    </div>

    <RuleDialog v-model:open="ruleDialog" :rule="editRule" :set-id="selectedSet" :sets="sets" :nodes="nodes" :groups="groups" @saved="refresh" />
    <DevicePolicyDialog v-model:open="deviceDialog" :policy="editDevice" @saved="refresh" />
    <Dialog v-model:open="setDialog" title="新建规则集" size="sm">
      <Field label="名称"><Input v-model="setName" placeholder="如 流媒体" autofocus @keyup.enter="setName && createSet()" /></Field>
      <template #footer>
        <Button variant="secondary" @click="setDialog = false">取消</Button>
        <Button :loading="setBusy" :disabled="!setName.trim()" @click="createSet">创建</Button>
      </template>
    </Dialog>
    <ConfirmDialog v-if="confirm" :open="!!confirm" :title="confirm.title" :description="confirm.desc" confirm-text="删除" danger :action="confirm.action" @update:open="v => { if (!v) confirm = null }" @done="refresh" />
  </div>
</template>
