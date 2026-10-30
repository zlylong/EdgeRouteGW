<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { Plus, Trash2 } from 'lucide-vue-next'
import Tabs from '@/components/ui/Tabs.vue'
import Card from '@/components/ui/Card.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Input from '@/components/ui/Input.vue'
import Select from '@/components/ui/Select.vue'
import Field from '@/components/ui/Field.vue'
import Table from '@/components/ui/Table.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import TuningPanel from '@/components/system/TuningPanel.vue'
import RetentionPanel from '@/components/system/RetentionPanel.vue'
import Dialog from '@/components/ui/Dialog.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { auth as authApi, backup as backupApi, system } from '@/api/endpoints'
import ComponentsPanel from '@/components/system/ComponentsPanel.vue'
import { useGatewayState } from '@/stores/state'
import { useToast } from '@/stores/toast'
import { fmtAgo, fmtTime } from '@/lib/utils'

const qc = useQueryClient()
const gw = useGatewayState()
const toast = useToast()
const tab = ref('security')

const sessionsQ = useQuery({ queryKey: ['sessions'], queryFn: authApi.sessions })
const tokensQ = useQuery({ queryKey: ['tokens'], queryFn: authApi.tokens })
const tokenDialog = ref(false)
const tokenForm = ref({ name: '', scope: 'read' as 'read' | 'write', expires_in: '' })
const newToken = ref('')
const tokenBusy = ref(false)
async function createToken() {
  tokenBusy.value = true
  try {
    const r = await authApi.createToken(tokenForm.value.name, tokenForm.value.scope, tokenForm.value.expires_in)
    newToken.value = r.token
    tokenForm.value = { name: '', scope: 'read', expires_in: '' }
    qc.invalidateQueries({ queryKey: ['tokens'] })
  } catch (e) { toast.error('创建失败', (e as Error).message) } finally { tokenBusy.value = false }
}
async function revokeToken(id: string) { try { await authApi.deleteToken(id); qc.invalidateQueries({ queryKey: ['tokens'] }) } catch (e) { toast.error('撤销失败', (e as Error).message) } }
async function kickSession(id: string) { try { await authApi.deleteSession(id); qc.invalidateQueries({ queryKey: ['sessions'] }) } catch (e) { toast.error('操作失败', (e as Error).message) } }
watch(tokenDialog, o => { if (!o) newToken.value = '' })

// Backup & restore
const restoreFile = ref<File | null>(null)
const restoreDialog = ref(false)
async function downloadBackup(secrets: boolean) {
  const res = await fetch(backupApi.exportUrl(secrets), { credentials: 'same-origin' })
  if (!res.ok) { toast.error('导出失败', `HTTP ${res.status}`); return }
  const blob = await res.blob()
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = (res.headers.get('content-disposition')?.match(/filename="([^"]+)"/)?.[1]) ?? 'edgeroutegw-backup.json'
  a.click(); URL.revokeObjectURL(a.href)
}
async function doRestore() {
  if (!restoreFile.value) return
  const text = await restoreFile.value.text()
  let doc: unknown
  try { doc = JSON.parse(text) } catch { throw new Error('不是合法的 JSON 文件') }
  const r = await backupApi.restore(doc)
  toast.ok('已恢复', `${r.nodes} 个节点、${r.rules} 条规则${r.secrets_restored ? '' : ';备份不含凭证,节点需要重新填写'}`)
  gw.load().catch(() => {})
}

// Generated configs
const artifact = ref<'xray' | 'mosdns' | 'mosdns-domains' | 'nftables'>('xray')
const artifactQ = useQuery({ queryKey: ['artifact', artifact], queryFn: () => system.artifact(artifact.value), enabled: computed(() => tab.value === 'configs') })
watch(() => gw.lastEvent, e => { if (e?.type === 'apply.finished') qc.invalidateQueries({ queryKey: ['artifact'] }) })
</script>

<template>
  <div class="space-y-4">
    <Tabs v-model="tab" :tabs="[{ value: 'security', label: '安全' }, { value: 'components', label: '组件更新' }, { value: 'tuning', label: '系统调优' }, { value: 'retention', label: '日志与保留' }, { value: 'configs', label: '生成的配置' }, { value: 'about', label: '关于' }]" />

    <div v-if="tab === 'security'" class="grid gap-4">
      <Card title="账户与会话">
        <template #actions><RouterLink to="/change-password"><Button variant="secondary" size="sm">修改密码</Button></RouterLink></template>
        <p class="mb-3 text-xs text-fg-muted">单管理员账户 <span class="font-mono">admin</span>;更换密码会注销其它所有会话。下面是当前已登录的会话。</p>
        <EmptyState v-if="!sessionsQ.data.value?.items.length" title="无会话" />
        <ul v-else class="divide-y divide-border text-sm">
          <li v-for="s in sessionsQ.data.value!.items" :key="s.id" class="flex items-center gap-3 py-2">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2"><span class="font-mono text-xs">{{ s.remote_ip }}</span><Badge v-if="s.current" tone="accent">当前</Badge></div>
              <p class="truncate text-xs text-fg-subtle" :title="s.user_agent">{{ s.user_agent || '-' }} · 活跃于 {{ fmtAgo(s.last_seen_at) }}</p>
            </div>
            <Button v-if="!s.current" variant="ghost" size="sm" class="text-danger" @click="kickSession(s.id)">注销</Button>
          </li>
        </ul>
      </Card>
      <Card title="备份与恢复">
        <p class="text-sm text-fg-muted">导出当前期望状态(节点、出站组、规则、设备策略、DNS 与网络设置)为 JSON。含凭证的导出包含节点密码和私钥,请妥善保管。恢复会整体替换现有配置。</p>
        <div class="mt-3 flex flex-wrap items-center gap-2">
          <Button variant="secondary" @click="downloadBackup(false)">导出(不含凭证)</Button>
          <Button variant="secondary" @click="downloadBackup(true)">导出(含凭证)</Button>
          <label class="ml-4 text-sm"><input type="file" accept="application/json" class="text-xs" @change="restoreFile = ($event.target as HTMLInputElement).files?.[0] ?? null"></label>
          <Button variant="danger" :disabled="!restoreFile" @click="restoreDialog = true">恢复</Button>
        </div>
      </Card>
      <Card title="API Token">
        <template #actions><Button size="sm" @click="tokenDialog = true"><Plus class="h-4 w-4" /> 新建</Button></template>
        <p class="mb-3 text-sm text-fg-muted">给脚本和自动化用的长期凭证,以 <code class="rounded bg-surface-2 px-1 font-mono text-xs">Authorization: Bearer ergw_…</code> 调用。只读 token 只能发起 GET。</p>
        <EmptyState v-if="!tokensQ.data.value?.items.length" title="没有 token" />
        <Table v-else :cols="[{ key: 'n', label: '名称' }, { key: 's', label: '权限' }, { key: 'c', label: '创建' }, { key: 'u', label: '最近使用' }, { key: 'e', label: '过期' }, { key: 'ops', label: '', class: 'w-16' }]" dense>
          <tr v-for="t in tokensQ.data.value!.items" :key="t.id" class="text-xs">
            <td class="px-3 py-1.5 font-medium">{{ t.name }}</td>
            <td class="px-3 py-1.5"><Badge :tone="t.scope === 'write' ? 'warn' : 'neutral'">{{ t.scope }}</Badge></td>
            <td class="px-3 py-1.5 text-fg-muted">{{ fmtTime(t.created_at) }}</td>
            <td class="px-3 py-1.5 text-fg-muted">{{ t.last_used_at ? fmtAgo(t.last_used_at) : '从未' }}</td>
            <td class="px-3 py-1.5 text-fg-muted">{{ t.expires_at ? fmtTime(t.expires_at) : '永不' }}</td>
            <td class="px-3 py-1.5 text-right"><Button variant="ghost" size="icon" class="text-danger" aria-label="撤销" @click="revokeToken(t.id)"><Trash2 class="h-3.5 w-3.5" /></Button></td>
          </tr>
        </Table>
      </Card>
    </div>

    <ComponentsPanel v-else-if="tab === 'components'" />
    <TuningPanel v-else-if="tab === 'tuning'" />
    <RetentionPanel v-else-if="tab === 'retention'" />

    <div v-else-if="tab === 'configs'">
      <div class="mb-3 flex items-center gap-3">
        <Select v-model="artifact" class="w-56" :options="[{ value: 'xray', label: 'Xray config.json' }, { value: 'mosdns', label: 'Mosdns config.yaml' }, { value: 'mosdns-domains', label: 'Mosdns proxy_domains.txt' }, { value: 'nftables', label: 'nftables ruleset' }]" />
        <span class="text-xs text-fg-subtle">按当前期望状态实时渲染;下发后与运行中的配置一致。</span>
      </div>
      <CodeBlock :code="artifactQ.data.value ?? (artifactQ.isPending.value ? '加载中…' : '')" max-height="70vh" />
    </div>

    <div v-else class="grid gap-4 lg:grid-cols-2">
      <Card title="版本">
        <dl class="grid grid-cols-[6rem_1fr] gap-y-1.5 text-sm">
          <dt class="text-fg-muted">EdgeRouteGW</dt><dd>{{ gw.state?.version.version }}</dd>
          <dt class="text-fg-muted">Commit</dt><dd class="font-mono text-xs">{{ gw.state?.version.commit }}</dd>
          <dt class="text-fg-muted">构建时间</dt><dd>{{ gw.state?.version.build_time || '-' }}</dd>
          <dt class="text-fg-muted">Go</dt><dd>{{ gw.state?.version.go }} {{ gw.state?.version.os }}/{{ gw.state?.version.arch }}</dd>
          <dt class="text-fg-muted">数据库</dt><dd>{{ gw.state?.schema_version }}</dd>
          <dt class="text-fg-muted">运行时长</dt><dd>{{ Math.floor((gw.state?.uptime_s ?? 0) / 3600) }} 小时 {{ Math.floor(((gw.state?.uptime_s ?? 0) % 3600) / 60) }} 分</dd>
        </dl>
      </Card>
      <Card title="组件">
        <ul class="space-y-1.5 text-sm">
          <li v-for="c in gw.components" :key="c.name" class="flex items-center justify-between">
            <span class="uppercase">{{ c.name }}</span>
            <span class="text-xs text-fg-muted">{{ c.version || c.status }}</span>
          </li>
        </ul>
        <p class="mt-3 text-xs text-fg-subtle">备份导入与 1.x 数据迁移将在 M5 提供。</p>
      </Card>
    </div>

    <ConfirmDialog v-model:open="restoreDialog" title="用备份替换当前配置?" description="现有节点、规则、设备策略、DNS 与网络设置会被备份内容整体替换,并立即下发。" confirm-text="恢复" danger :action="doRestore" />
    <Dialog v-model:open="tokenDialog" title="新建 API Token" size="sm">
      <template v-if="newToken">
        <p class="mb-2 text-sm">请立即复制,关闭后不再显示:</p>
        <CodeBlock :code="newToken" />
      </template>
      <div v-else class="space-y-4">
        <Field label="名称"><Input v-model="tokenForm.name" placeholder="如 监控脚本" /></Field>
        <Field label="权限"><Select v-model="tokenForm.scope" :options="[{ value: 'read', label: '只读' }, { value: 'write', label: '读写' }]" /></Field>
        <Field label="有效期" hint="Go 时长格式,留空永不过期"><Input v-model="tokenForm.expires_in" placeholder="720h" /></Field>
      </div>
      <template #footer>
        <Button variant="secondary" @click="tokenDialog = false">{{ newToken ? '关闭' : '取消' }}</Button>
        <Button v-if="!newToken" :loading="tokenBusy" :disabled="!tokenForm.name" @click="createToken">创建</Button>
      </template>
    </Dialog>
  </div>
</template>
