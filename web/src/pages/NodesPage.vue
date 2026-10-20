<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { Download, MoreHorizontal, Plus, Star } from 'lucide-vue-next'
import { DropdownMenuContent, DropdownMenuItem, DropdownMenuPortal, DropdownMenuRoot, DropdownMenuTrigger } from 'reka-ui'
import Tabs from '@/components/ui/Tabs.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Switch from '@/components/ui/Switch.vue'
import Table from '@/components/ui/Table.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Spinner from '@/components/ui/Spinner.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import NodeDialog from '@/components/nodes/NodeDialog.vue'
import ImportDialog from '@/components/nodes/ImportDialog.vue'
import GroupDialog from '@/components/nodes/GroupDialog.vue'
import HostDialog from '@/components/hosts/HostDialog.vue'
import DeployDialog from '@/components/hosts/DeployDialog.vue'
import { groups as groupsApi, hosts as hostsApi, nodes as nodesApi } from '@/api/endpoints'
import type { Node, OutboundGroup, RemoteHost } from '@/api/types'
import { useGatewayState } from '@/stores/state'
import { watch } from 'vue'
import { fmtAgo } from '@/lib/utils'
import { useToast } from '@/stores/toast'

const qc = useQueryClient()
const toast = useToast()
const tab = ref('nodes')
const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: nodesApi.list })
const groupsQ = useQuery({ queryKey: ['groups'], queryFn: groupsApi.list })
const nodes = computed(() => nodesQ.data.value?.items ?? [])
const groups = computed(() => groupsQ.data.value?.items ?? [])
const refresh = () => { qc.invalidateQueries({ queryKey: ['nodes'] }); qc.invalidateQueries({ queryKey: ['groups'] }); qc.invalidateQueries({ queryKey: ['hosts'] }) }

const hostsQ = useQuery({ queryKey: ['hosts'], queryFn: hostsApi.list })
const hosts = computed(() => hostsQ.data.value?.items ?? [])
const gw = useGatewayState()
watch(() => gw.lastEvent, e => { if (e?.type === 'provision.host' || e?.type === 'provision.state') qc.invalidateQueries({ queryKey: ['hosts'] }) })
const hostDialog = ref(false); const editHost = ref<RemoteHost | null>(null)
const deployDialog = ref(false); const deployHost = ref<RemoteHost | null>(null); const viewDeployId = ref<string | null>(null)
const checking = ref<string | null>(null)
async function checkHost(h: RemoteHost) {
  checking.value = h.id
  try { const r = await hostsApi.check(h.id); if (r.status === 'online') toast.ok(`${h.name} 在线`); else toast.warn(`${h.name}:${r.status}`, r.reason); qc.invalidateQueries({ queryKey: ['hosts'] }) } catch (e) { toast.error('检查失败', (e as Error).message) } finally { checking.value = null }
}
function openDeploy(h: RemoteHost, deployId: string | null = null) { deployHost.value = h; viewDeployId.value = deployId; deployDialog.value = true }
function removeHost(h: RemoteHost) {
  confirm.value = { title: `删除主机 ${h.name}?`, desc: '会尝试通过 SSH 卸载远端服务;已注册的出站节点保留,需要时手动删除。', action: () => hostsApi.remove(h.id, true) }
}
async function rollbackHost(h: RemoteHost) {
  if (!h.last_deploy) return
  try { const r = await hostsApi.rollback(h.last_deploy.id); openDeploy(h, r.deploy_id) } catch (e) { toast.error('无法回滚', (e as Error).message) }
}
const hostTone = (s: string) => ({ online: 'ok', deploying: 'info', failed: 'danger', offline: 'danger', unknown: 'neutral' } as const)[s as 'online'] ?? 'neutral'
const hostLabel: Record<string, string> = { online: '在线', deploying: '部署中', failed: '失败', offline: '离线', unknown: '未检查' }
const deployLabel: Record<string, string> = { queued: '排队', running: '进行中', succeeded: '成功', failed: '失败', rolled_back: '已回滚' }

const nodeDialog = ref(false); const editNode = ref<Node | null>(null)
const importDialog = ref(false)
const groupDialog = ref(false); const editGroup = ref<OutboundGroup | null>(null)
const confirm = ref<{ title: string; desc: string; action: () => Promise<unknown> } | null>(null)

const menuItem = 'flex cursor-pointer select-none items-center rounded px-2 py-1.5 text-sm outline-none data-[highlighted]:bg-surface-2'

async function toggle(n: Node, enabled: boolean) {
  try { await nodesApi.setEnabled(n.id, enabled); refresh() } catch (e) { toast.error('操作失败', (e as Error).message) }
}
async function setDefault(n: Node) {
  try { await nodesApi.setDefault(n.id); toast.ok(`默认节点:${n.name}`); refresh() } catch (e) { toast.error('操作失败', (e as Error).message) }
}
function removeNode(n: Node) {
  confirm.value = { title: `删除节点 ${n.name}?`, desc: '指向该节点的规则会改为走默认出站;所属出站组会移除该成员。', action: () => nodesApi.remove(n.id) }
}
function removeGroup(g: OutboundGroup) {
  confirm.value = { title: `删除出站组 ${g.name}?`, desc: '指向该组的规则会改为走默认出站。', action: () => groupsApi.remove(g.id) }
}
const nodeName = (id: string) => nodes.value.find(n => n.id === id)?.name ?? id.slice(0, 8)
const latency = (n: Node) => n.latency_ms === undefined ? '-' : n.latency_ms < 0 ? '不可达' : `${n.latency_ms} ms`
</script>

<template>
  <div>
    <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
      <Tabs v-model="tab" :tabs="[{ value: 'nodes', label: '出站节点', count: nodes.length }, { value: 'groups', label: '出站组', count: groups.length }, { value: 'hosts', label: '托管主机', count: hosts.length }]" />
      <div class="flex gap-2">
        <template v-if="tab === 'nodes'">
          <Button variant="secondary" @click="importDialog = true"><Download class="h-4 w-4" /> 导入链接</Button>
          <Button @click="editNode = null; nodeDialog = true"><Plus class="h-4 w-4" /> 添加节点</Button>
        </template>
        <Button v-else-if="tab === 'groups'" :disabled="!nodes.length" @click="editGroup = null; groupDialog = true"><Plus class="h-4 w-4" /> 新建出站组</Button>
        <Button v-else @click="editHost = null; hostDialog = true"><Plus class="h-4 w-4" /> 添加主机</Button>
      </div>
    </div>

    <template v-if="tab === 'nodes'">
      <Spinner v-if="nodesQ.isPending.value" />
      <EmptyState v-else-if="!nodes.length" title="还没有节点" hint="添加一个节点,或粘贴分享链接批量导入。至少一个启用的节点后,规则里的「代理」才有去处。" />
      <Table v-else :cols="[{ key: 'name', label: '名称' }, { key: 'proto', label: '协议' }, { key: 'addr', label: '地址' }, { key: 'lat', label: '延迟' }, { key: 'on', label: '启用', class: 'w-20' }, { key: 'act', label: '', class: 'w-12' }]">
        <tr v-for="n in nodes" :key="n.id" class="hover:bg-surface-2/60">
          <td class="px-3 py-2">
            <div class="flex items-center gap-2">
              <span class="font-medium" :class="!n.enabled && 'text-fg-subtle'">{{ n.name }}</span>
              <Badge v-if="n.is_default" tone="accent"><Star class="h-3 w-3" /> 默认</Badge>
              <Badge v-if="!n.has_credential && ['vless','vmess','trojan','shadowsocks','wireguard'].includes(n.protocol)" tone="danger">缺凭证</Badge>
            </div>
          </td>
          <td class="px-3 py-2 text-xs uppercase text-fg-muted">{{ n.protocol }}</td>
          <td class="px-3 py-2 font-mono text-xs text-fg-muted">{{ n.address }}:{{ n.port }}</td>
          <td class="px-3 py-2 text-xs text-fg-muted">{{ latency(n) }}</td>
          <td class="px-3 py-2"><Switch :model-value="n.enabled" :label="`启用 ${n.name}`" @update:model-value="v => toggle(n, v)" /></td>
          <td class="px-3 py-2 text-right">
            <DropdownMenuRoot>
              <DropdownMenuTrigger as-child><Button variant="ghost" size="icon" aria-label="更多"><MoreHorizontal class="h-4 w-4" /></Button></DropdownMenuTrigger>
              <DropdownMenuPortal>
                <DropdownMenuContent align="end" class="z-50 min-w-36 rounded-md border border-border bg-surface p-1 shadow-lg">
                  <DropdownMenuItem :class="menuItem" @select="editNode = n; nodeDialog = true">编辑</DropdownMenuItem>
                  <DropdownMenuItem :class="menuItem" :disabled="n.is_default" @select="setDefault(n)">设为默认</DropdownMenuItem>
                  <DropdownMenuItem :class="[menuItem, 'text-danger']" @select="removeNode(n)">删除</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenuPortal>
            </DropdownMenuRoot>
          </td>
        </tr>
      </Table>
    </template>

    <template v-else-if="tab === 'groups'">
      <Spinner v-if="groupsQ.isPending.value" />
      <EmptyState v-else-if="!groups.length" title="还没有出站组" hint="出站组让一条规则指向「主用 A、备用 B」或在多个节点间分担,取代 1.x 里写进规则策略的 HA 对。" />
      <Table v-else :cols="[{ key: 'name', label: '名称' }, { key: 'kind', label: '类型' }, { key: 'members', label: '成员(按序)' }, { key: 'act', label: '', class: 'w-40' }]">
        <tr v-for="g in groups" :key="g.id" class="hover:bg-surface-2/60">
          <td class="px-3 py-2 font-medium">{{ g.name }}</td>
          <td class="px-3 py-2"><Badge :tone="g.kind === 'failover' ? 'info' : 'accent'">{{ g.kind === 'failover' ? '故障转移' : '负载均衡' }}</Badge></td>
          <td class="px-3 py-2 text-sm text-fg-muted">
            <span v-for="(id, i) in g.member_ids" :key="id">{{ i ? ' → ' : '' }}{{ nodeName(id) }}</span>
          </td>
          <td class="px-3 py-2 text-right">
            <Button variant="ghost" size="sm" @click="editGroup = g; groupDialog = true">编辑</Button>
            <Button variant="ghost" size="sm" class="text-danger" @click="removeGroup(g)">删除</Button>
          </td>
        </tr>
      </Table>
    </template>

    <template v-else>
      <Spinner v-if="hostsQ.isPending.value" />
      <EmptyState v-else-if="!hosts.length" title="还没有托管主机" hint="录入海外主机的 SSH 信息,由网关一键部署 WireGuard 或 VLESS-REALITY 服务端,成功后自动加入出站节点。" />
      <Table v-else :cols="[{ key: 'name', label: '主机' }, { key: 'ssh', label: 'SSH' }, { key: 'st', label: '状态' }, { key: 'dep', label: '最近部署' }, { key: 'act', label: '', class: 'w-72' }]">
        <tr v-for="h in hosts" :key="h.id" class="hover:bg-surface-2/60">
          <td class="px-3 py-2">
            <div class="font-medium">{{ h.name }}</div>
            <div class="text-xs text-fg-subtle">{{ h.region || h.remark }}</div>
          </td>
          <td class="px-3 py-2 font-mono text-xs text-fg-muted">{{ h.ssh_user }}@{{ h.ssh_host }}:{{ h.ssh_port }}<div v-if="h.host_key" class="truncate text-[10px] text-fg-subtle" :title="h.host_key">{{ h.host_key }}</div></td>
          <td class="px-3 py-2"><Badge :tone="hostTone(h.status)">{{ hostLabel[h.status] ?? h.status }}</Badge></td>
          <td class="px-3 py-2 text-xs">
            <template v-if="h.last_deploy">
              <button class="hover:underline" @click="openDeploy(h, h.last_deploy.id)">
                <Badge :tone="h.last_deploy.state === 'succeeded' ? 'ok' : h.last_deploy.state === 'failed' ? 'danger' : 'info'">{{ h.last_deploy.kind === 'wg' ? 'WireGuard' : 'VLESS-REALITY' }} · {{ deployLabel[h.last_deploy.state] }}</Badge>
              </button>
              <div class="mt-0.5 text-fg-subtle">{{ fmtAgo(h.last_deploy.finished_at ?? h.last_deploy.created_at) }}</div>
            </template>
            <span v-else class="text-fg-subtle">尚未部署</span>
          </td>
          <td class="px-3 py-2 text-right whitespace-nowrap">
            <Button variant="ghost" size="sm" :loading="checking === h.id" @click="checkHost(h)">检查连接</Button>
            <Button variant="secondary" size="sm" :disabled="h.status === 'deploying'" @click="openDeploy(h)">部署</Button>
            <Button variant="ghost" size="sm" :disabled="!h.last_deploy || h.last_deploy.state !== 'succeeded' || h.status === 'deploying'" @click="rollbackHost(h)">重新下发</Button>
            <Button variant="ghost" size="sm" @click="editHost = h; hostDialog = true">编辑</Button>
            <Button variant="ghost" size="sm" class="text-danger" @click="removeHost(h)">删除</Button>
          </td>
        </tr>
      </Table>
    </template>
    <HostDialog v-model:open="hostDialog" :host="editHost" @saved="refresh" />
    <DeployDialog v-model:open="deployDialog" :host="deployHost" :deploy-id="viewDeployId" @finished="refresh" />

    <NodeDialog v-model:open="nodeDialog" :node="editNode" @saved="refresh" />
    <ImportDialog v-model:open="importDialog" @imported="refresh" />
    <GroupDialog v-model:open="groupDialog" :group="editGroup" :nodes="nodes" @saved="refresh" />
    <ConfirmDialog v-if="confirm" :open="!!confirm" :title="confirm.title" :description="confirm.desc" confirm-text="删除" danger :action="confirm.action" @update:open="v => { if (!v) confirm = null }" @done="refresh" />
  </div>
</template>
