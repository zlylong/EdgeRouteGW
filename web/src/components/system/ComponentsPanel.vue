<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { Download, RefreshCw, RotateCcw } from 'lucide-vue-next'
import Card from '@/components/ui/Card.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Select from '@/components/ui/Select.vue'
import Input from '@/components/ui/Input.vue'
import Field from '@/components/ui/Field.vue'
import Dialog from '@/components/ui/Dialog.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import SchedulePicker from '@/components/system/SchedulePicker.vue'
import { components as compApi } from '@/api/endpoints'
import type { ComponentInfo, Release, UpdateJob } from '@/api/types'
import { useToast } from '@/stores/toast'
import { useGatewayState } from '@/stores/state'
import { fmtAgo } from '@/lib/utils'

const qc = useQueryClient()
const toast = useToast()
const gw = useGatewayState()
const compQ = useQuery({ queryKey: ['components'], queryFn: () => compApi.list(false) })
const refreshing = ref(false)
async function refresh() { refreshing.value = true; try { qc.setQueryData(['components'], await compApi.list(true)) } catch (e) { toast.error('检查失败', (e as Error).message) } finally { refreshing.value = false } }

const dlQ = useQuery({ queryKey: ['download-settings'], queryFn: compApi.downloadSettings })
const dl = ref({ proxy: '', github_mirror: '' })
watch(() => dlQ.data.value, d => { if (d) dl.value = { ...d } }, { immediate: true })
async function saveDownload() {
  try { await compApi.setDownloadSettings(dl.value); toast.ok('下载设置已保存'); qc.invalidateQueries({ queryKey: ['download-settings'] }) } catch (e) { toast.error('保存失败', (e as Error).message) }
}
const uploadFor = ref(''); const uploadFile = ref<File | null>(null); const uploadSha = ref(''); const uploadBusy = ref(false)
async function doUpload() {
  if (!uploadFile.value) return
  uploadBusy.value = true
  try { await compApi.upload(uploadFor.value, uploadFile.value, uploadSha.value.trim()); uploadFor.value = ''; uploadFile.value = null; uploadSha.value = ''; toast.info('开始安装上传的压缩包'); qc.invalidateQueries({ queryKey: ['components'] }) } catch (e) { toast.error('上传失败', (e as Error).message) } finally { uploadBusy.value = false }
}
async function installBundled(name: string) {
  try { await compApi.installBundled(name); toast.info('正在从内置包安装'); qc.invalidateQueries({ queryKey: ['components'] }) } catch (e) { toast.error('无法安装', (e as Error).message) }
}
const meta: Record<string, { label: string; hint: string }> = {
  xray: { label: 'Xray-core', hint: '代理核心。按 XTLS 发布的 .dgst 校验,安装前跑 version 冒烟,单元重启失败自动回退。' },
  mosdns: { label: 'Mosdns', hint: 'DNS 分流。按 GitHub 发布资产的 sha256 摘要校验。' },
  geodata: { label: 'GeoData', hint: 'geoip.dat / geosite.dat(Loyalsoldier/v2ray-rules-dat),按 rules.zip.sha256sum 校验。更新后自动重新渲染 DNS 列表与路由。' },
  edgeroutegw: { label: 'EdgeRouteGW', hint: '网关本体。从 GitHub Release 下载 .deb,按 SHA256SUMS 校验后安装并重启。' },
}
const jobs = ref<Record<string, UpdateJob>>({})
watch(() => gw.lastEvent, (e) => {
  if (!e) return
  if (e.type === 'update.progress' || e.type === 'update.done') {
    const j = e.data as UpdateJob
    jobs.value = { ...jobs.value, [j.component]: j }
    if (e.type === 'update.done') {
      if (j.state === 'succeeded') toast.ok(`${meta[j.component]?.label ?? j.component} ${j.action === 'rollback' ? '已回退到' : '已更新到'} ${j.version}`)
      else toast.error(`${meta[j.component]?.label ?? j.component} 更新失败`, j.error)
      qc.invalidateQueries({ queryKey: ['components'] })
    }
  }
})
const jobOf = (c: ComponentInfo) => jobs.value[c.name]?.state === 'running' ? jobs.value[c.name] : (c.job?.state === 'running' ? c.job : undefined)

const pickDialog = ref(false); const pickFor = ref<string>(''); const versions = ref<Release[]>([]); const pickVersion = ref(''); const loadingVersions = ref(false)
async function openPick(name: string) {
  pickFor.value = name; pickVersion.value = ''; versions.value = []; pickDialog.value = true; loadingVersions.value = true
  try { versions.value = (await compApi.versions(name)).items; pickVersion.value = versions.value.find(v => !v.prerelease)?.tag ?? '' } catch (e) { toast.error('读取版本列表失败', (e as Error).message) } finally { loadingVersions.value = false }
}
async function startUpdate(name: string, version?: string) {
  try { await compApi.update(name, version); pickDialog.value = false; toast.info('更新已开始', '进度会实时显示') ; qc.invalidateQueries({ queryKey: ['components'] }) } catch (e) { toast.error('无法开始更新', (e as Error).message) }
}
const rollbackFor = ref<string | null>(null)
const doRollback = () => compApi.rollback(rollbackFor.value!)

// Options arrays are computed so the native <select> is not re-rendered (and
// its open popup closed) every time the dialog re-renders.
const versionOptions = computed(() => versions.value.map(v => ({ value: v.tag, label: v.tag + (v.prerelease ? '(预发布)' : '') })))
const schedHint: Record<string, string> = {
  xray: '到点先查 GitHub,只有出现新版本才下载、校验并重启 Xray(时间按本机时区)。',
  mosdns: '到点先查 GitHub,只有出现新版本才下载、校验并重启 Mosdns(时间按本机时区)。',
  geodata: '到点检查规则数据,有新版本才更新,更新后自动重新渲染 DNS 列表与路由(时间按本机时区)。',
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <p class="text-sm text-fg-muted">下载全部经校验和验证;二进制替换前保留上一版本,可一键回退。</p>
      <Button variant="secondary" size="sm" :loading="refreshing" @click="refresh"><RefreshCw class="h-3.5 w-3.5" /> 检查新版本</Button>
    </div>
    <div v-if="!compQ.data.value?.items.length" class="rounded-lg border border-border bg-surface-2 p-3 text-sm text-fg-muted">当前实例以 -no-reconcile 运行或更新模块未启用。</div>
    <div class="grid gap-4 lg:grid-cols-3">
      <Card v-for="c in compQ.data.value?.items ?? []" :key="c.name" :title="meta[c.name]?.label ?? c.name">
        <p class="text-xs text-fg-subtle">{{ meta[c.name]?.hint }}</p>
        <dl class="mt-3 grid grid-cols-[5rem_1fr] gap-y-1 text-sm">
          <dt class="text-fg-muted">已安装</dt><dd class="font-mono">{{ c.installed || '未安装' }}</dd>
          <dt class="text-fg-muted">最新</dt>
          <dd class="font-mono">{{ c.latest || '-' }} <Badge v-if="c.latest && c.installed && c.latest !== c.installed" tone="warn">可更新</Badge><Badge v-else-if="c.latest && c.installed" tone="ok">最新</Badge></dd>
          <dt class="text-fg-muted">检查于</dt><dd class="text-xs text-fg-subtle">{{ c.checked_at ? fmtAgo(c.checked_at) : '-' }}</dd>
        </dl>
        <div v-if="jobOf(c)" class="mt-3">
          <div class="flex items-center justify-between text-xs"><span>{{ jobOf(c)!.step }}</span><span>{{ jobOf(c)!.percent }}%</span></div>
          <div class="mt-1 h-1.5 overflow-hidden rounded bg-surface-2"><div class="h-full bg-accent transition-all" :style="{ width: jobOf(c)!.percent + '%' }" /></div>
        </div>
        <p v-if="c.last_error" class="mt-2 break-words text-xs text-danger">{{ c.last_error }}</p>
        <SchedulePicker v-if="schedHint[c.name]" :name="c.name" :label="meta[c.name]?.label ?? c.name" :schedule="c.schedule" :hint="schedHint[c.name]" :disabled="!!jobOf(c)" />
        <div class="mt-4 flex flex-wrap gap-2">
          <Button size="sm" :disabled="!!jobOf(c)" @click="startUpdate(c.name)"><Download class="h-3.5 w-3.5" /> {{ c.installed ? '更新到最新' : '在线安装' }}</Button>
          <Button v-if="c.bundled" size="sm" variant="secondary" :disabled="!!jobOf(c)" :title="`内置包版本 ${c.bundled.version}`" @click="installBundled(c.name)">从内置包安装 {{ c.bundled.version }}</Button>
          <Button v-if="c.name !== 'geodata' && c.name !== 'edgeroutegw'" size="sm" variant="secondary" :disabled="!!jobOf(c)" @click="openPick(c.name)">选择版本</Button>
          <Button v-if="c.name !== 'edgeroutegw'" size="sm" variant="ghost" :disabled="!!jobOf(c)" @click="uploadFor = c.name">本地上传</Button>
          <Button v-if="c.name !== 'edgeroutegw'" size="sm" variant="ghost" :disabled="!c.has_previous || !!jobOf(c)" @click="rollbackFor = c.name"><RotateCcw class="h-3.5 w-3.5" /> 回退</Button>
        </div>
      </Card>
    </div>
    <Card title="下载通道">
      <p class="mb-3 text-sm text-fg-muted">GitHub 不可达时的三条路:装 <code class="rounded bg-surface-2 px-1 font-mono text-xs">edgeroutegw-components</code> 内置包(离线 full 包自带);配置下载代理,例如网关自己的隧道 <code class="rounded bg-surface-2 px-1 font-mono text-xs">socks5://127.0.0.1:10808</code>(先要有一个能用的节点);或者用镜像前缀改写 GitHub 地址。也可以在上面每个组件点「本地上传」直接安装官方压缩包。</p>
      <div class="grid gap-4 sm:grid-cols-2">
        <Field label="下载代理" hint="http(s):// 或 socks5://,留空直连"><Input v-model="dl.proxy" class="font-mono" placeholder="socks5://127.0.0.1:10808" /></Field>
        <Field label="GitHub 镜像前缀" hint="会拼在 GitHub URL 前面,留空不用"><Input v-model="dl.github_mirror" class="font-mono" placeholder="https://ghproxy.example/" /></Field>
      </div>
      <div class="mt-3 flex justify-end"><Button variant="secondary" @click="saveDownload">保存</Button></div>
    </Card>

    <Dialog :open="!!uploadFor" :title="`本地上传安装 ${meta[uploadFor]?.label ?? uploadFor}`" size="sm" @update:open="v => { if (!v) uploadFor = '' }">
      <p class="mb-3 text-sm text-fg-muted">选择官方发布的压缩包:Xray 的 <code class="font-mono text-xs">Xray-linux-64.zip</code>、Mosdns 的 <code class="font-mono text-xs">mosdns-linux-amd64.zip</code> 或 GeoData 的 <code class="font-mono text-xs">rules.zip</code>。填入从发布页取得的 SHA-256 可在安装前校验;留空则按未校验安装并记录事件。</p>
      <div class="space-y-3">
        <input type="file" accept=".zip,application/zip" class="text-sm" @change="uploadFile = ($event.target as HTMLInputElement).files?.[0] ?? null">
        <Field label="SHA-256(可选)"><Input v-model="uploadSha" class="font-mono text-xs" placeholder="64 位十六进制" /></Field>
      </div>
      <template #footer>
        <Button variant="secondary" @click="uploadFor = ''">取消</Button>
        <Button :loading="uploadBusy" :disabled="!uploadFile" @click="doUpload">安装</Button>
      </template>
    </Dialog>
    <Dialog v-model:open="pickDialog" :title="`选择 ${meta[pickFor]?.label ?? pickFor} 版本`" size="sm">
      <p v-if="loadingVersions" class="text-sm text-fg-muted">读取 GitHub 发布列表…</p>
      <Field v-else label="版本"><Select v-model="pickVersion" :options="versionOptions" /></Field>
      <template #footer>
        <Button variant="secondary" @click="pickDialog = false">取消</Button>
        <Button :disabled="!pickVersion" @click="startUpdate(pickFor, pickVersion)">安装</Button>
      </template>
    </Dialog>
    <ConfirmDialog v-if="rollbackFor" :open="!!rollbackFor" :title="`回退 ${meta[rollbackFor]?.label}?`" description="恢复上一版本并重启对应服务。" confirm-text="回退" :action="doRollback" @update:open="v => { if (!v) rollbackFor = null }" />
  </div>
</template>
