<script setup lang="ts">
// Retention: how long history is kept, how big the runtime logs and the
// systemd journal may grow, what all of it occupies now, and a button to
// clean up at once.
import { computed, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import Card from '@/components/ui/Card.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Field from '@/components/ui/Field.vue'
import Badge from '@/components/ui/Badge.vue'
import Spinner from '@/components/ui/Spinner.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import { system } from '@/api/endpoints'
import type { RetentionSettings, RetentionView } from '@/api/types'
import { fmtAgo, fmtBytes } from '@/lib/utils'
import { useToast } from '@/stores/toast'

const qc = useQueryClient()
const toast = useToast()
const q = useQuery({ queryKey: ['retention'], queryFn: system.retention })

type FormKey = keyof RetentionSettings
const form = ref<Record<FormKey, string>>({ history_days: '90', traffic_days: '90', runlog_mb: '64', journal_days: '30', journal_mb: '500' })
watch(() => q.data.value, d => {
  if (!d) return
  for (const k of Object.keys(form.value) as FormKey[]) form.value[k] = String(d.settings[k])
}, { immediate: true })

const fields: { key: FormKey; label: string; unit: string; hint: string; min: number; max: number }[] = [
  { key: 'history_days', label: '历史记录', unit: '天', hint: '事件、变更记录、下发记录、托管主机部署输出;最近 200 次变更始终保留以便回滚', min: 1, max: 3650 },
  { key: 'traffic_days', label: '流量样本', unit: '天', hint: '每分钟一条的上下行与按节点流量,总览与观测页的流量图读它', min: 1, max: 3650 },
  { key: 'runlog_mb', label: '运行时日志上限', unit: 'MB', hint: '/run 下 Xray access log 与 Mosdns 日志各自的上限,超过即原地清空(它们只是实时视图,持久数据都在库里)', min: 8, max: 4096 },
  { key: 'journal_days', label: '系统日志保留', unit: '天', hint: 'systemd journal 的 MaxRetentionSec,写入 /etc/systemd/journald.conf.d/60-edgeroutegw.conf', min: 1, max: 3650 },
  { key: 'journal_mb', label: '系统日志上限', unit: 'MB', hint: 'systemd journal 的 SystemMaxUse;改动后立即 vacuum 到新上限', min: 50, max: 65536 },
]
const errors = computed(() => {
  const e: Partial<Record<FormKey, string>> = {}
  for (const f of fields) {
    const n = Number(form.value[f.key])
    if (!Number.isInteger(n) || n < f.min || n > f.max) e[f.key] = `${f.min} ~ ${f.max}`
  }
  return e
})
const dirty = computed(() => {
  const s = q.data.value?.settings
  return !!s && fields.some(f => String(s[f.key]) !== form.value[f.key])
})

const saving = ref(false)
async function save() {
  if (Object.keys(errors.value).length) return
  saving.value = true
  try {
    const body = Object.fromEntries(fields.map(f => [f.key, Number(form.value[f.key])])) as unknown as RetentionSettings
    const r = await system.setRetention(body)
    qc.setQueryData(['retention'], r)
    toast.ok('已保存', describe(r))
  } catch (e) { toast.error('保存失败', (e as Error).message) } finally { saving.value = false }
}

const cleanDialog = ref(false)
async function cleanup() {
  const r = await system.cleanupRetention()
  qc.setQueryData(['retention'], r)
  toast.ok('已清理', describe(r))
}
function describe(r: RetentionView): string {
  const rep = r.report
  if (!rep) return ''
  const parts: string[] = []
  const rows = rep.pruned.events + rep.pruned.apply_log + rep.pruned.changesets + rep.pruned.deploys + rep.pruned.deploy_logs + rep.pruned.traffic
  if (rows) parts.push(`删除 ${rows} 条记录`)
  if (rep.runtime_log_bytes_freed) parts.push(`运行时日志释放 ${fmtBytes(rep.runtime_log_bytes_freed)}`)
  if (rep.journal_bytes_before > rep.journal_bytes_after) parts.push(`系统日志 ${fmtBytes(rep.journal_bytes_before)} → ${fmtBytes(rep.journal_bytes_after)}`)
  if (rep.db_bytes_before > rep.db_bytes_after) parts.push(`数据库 ${fmtBytes(rep.db_bytes_before)} → ${fmtBytes(rep.db_bytes_after)}`)
  if (rep.warnings?.length) parts.push(`警告:${rep.warnings.join(';')}`)
  return parts.length ? parts.join(',') : '没有需要清理的内容'
}
const runPct = computed(() => {
  const u = q.data.value?.usage
  return u && u.run_fs_total ? Math.round(u.run_fs_used / u.run_fs_total * 100) : 0
})
const baseName = (p: string) => p.split('/').pop() ?? p
</script>

<template>
  <div class="space-y-4">
    <Card title="当前占用">
      <Spinner v-if="q.isPending.value" />
      <template v-else-if="q.data.value">
        <dl class="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2 lg:grid-cols-3">
          <div>
            <dt class="text-xs text-fg-muted">数据库</dt>
            <dd>{{ fmtBytes(q.data.value.usage.db.db_bytes) }} <span class="text-xs text-fg-subtle">+ WAL {{ fmtBytes(q.data.value.usage.db.wal_bytes) }}</span></dd>
          </div>
          <div>
            <dt class="text-xs text-fg-muted">历史记录</dt>
            <dd class="text-xs">事件 {{ q.data.value.usage.db.events }} · 变更 {{ q.data.value.usage.db.changesets }} · 下发 {{ q.data.value.usage.db.apply_log }} · 部署输出 {{ q.data.value.usage.db.deploy_logs }}</dd>
          </div>
          <div>
            <dt class="text-xs text-fg-muted">流量样本</dt>
            <dd class="text-xs">合计 {{ q.data.value.usage.db.traffic_samples }} · 按节点 {{ q.data.value.usage.db.node_traffic_samples }}</dd>
          </div>
          <div>
            <dt class="text-xs text-fg-muted">运行时日志(/run,已用 {{ runPct }}%)</dt>
            <dd class="text-xs"><span v-for="f in q.data.value.usage.runtime_logs" :key="f.path" class="mr-3">{{ baseName(f.path) }} {{ fmtBytes(f.bytes) }}</span></dd>
          </div>
          <div>
            <dt class="text-xs text-fg-muted">系统日志(journal)</dt>
            <dd>{{ fmtBytes(q.data.value.usage.journal_bytes) }} <Badge v-if="!q.data.value.usage.journal_managed" tone="neutral" class="ml-1">未托管</Badge></dd>
          </div>
          <div>
            <dt class="text-xs text-fg-muted">上次自动清理</dt>
            <dd>{{ q.data.value.usage.last_prune_at ? fmtAgo(q.data.value.usage.last_prune_at) : '尚未运行' }}</dd>
          </div>
        </dl>
      </template>
    </Card>

    <Card title="保留策略">
      <template #actions>
        <Button variant="secondary" size="sm" @click="cleanDialog = true">立即清理</Button>
        <Button size="sm" :loading="saving" :disabled="!dirty || Object.keys(errors).length > 0" @click="save">保存</Button>
      </template>
      <p class="mb-4 text-xs text-fg-subtle">每小时按下面的设置自动清理一次;保存后立即执行一次。「立即清理」额外清空运行时日志、压缩数据库文件并把系统日志收到上限以内。</p>
      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Field v-for="f in fields" :key="f.key" :label="f.label" :hint="f.unit" :error="errors[f.key]">
          <Input v-model="form[f.key]" type="number" :min="f.min" :max="f.max" :invalid="!!errors[f.key]" />
          <p class="mt-1 text-xs text-fg-subtle">{{ f.hint }}</p>
        </Field>
      </div>
    </Card>

    <ConfirmDialog v-model:open="cleanDialog" title="立即清理?" description="删除超过保留期的历史记录,清空 /run 下的 Xray / Mosdns 日志(实时连接列表会从空开始),压缩数据库,并把系统日志 vacuum 到设置的上限。" confirm-text="清理" :action="cleanup" />
  </div>
</template>
