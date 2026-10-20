<script setup lang="ts">
// Deploy wizard: pick kind and parameters, then follow the install log live.
import { computed, reactive, ref, watch } from 'vue'
import Dialog from '@/components/ui/Dialog.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Field from '@/components/ui/Field.vue'
import Badge from '@/components/ui/Badge.vue'
import { hosts as hostsApi } from '@/api/endpoints'
import type { DeployLogLine, RemoteHost } from '@/api/types'
import { useToast } from '@/stores/toast'
import { useGatewayState } from '@/stores/state'

const props = defineProps<{ host: RemoteHost | null; deployId?: string | null }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ finished: [] }>()
const toast = useToast()
const gw = useGatewayState()

const step = ref<'form' | 'log'>('form')
const form = reactive({ kind: 'vless' as 'vless' | 'wg', port: 443, server_name: 'www.apple.com', dest: '', node_name: '' })
const busy = ref(false)
const deployId = ref<string | null>(null)
const lines = ref<DeployLogLine[]>([])
const state = ref<string>('running')
const error = ref('')
const logEl = ref<HTMLElement | null>(null)
let poll: number | null = null

watch(open, (o) => {
  if (!o) { stopPoll(); return }
  lines.value = []; error.value = ''; state.value = 'running'
  if (props.deployId) { deployId.value = props.deployId; step.value = 'log'; startPoll() }
  else { step.value = 'form'; deployId.value = null; Object.assign(form, { kind: 'vless', port: 443, server_name: 'www.apple.com', dest: '', node_name: props.host?.name ?? '' }) }
})
watch(() => form.kind, k => { form.port = k === 'wg' ? 51820 : 443 })

async function start() {
  if (!props.host) return
  busy.value = true
  try {
    const r = await hostsApi.deploy(props.host.id, { kind: form.kind, port: Number(form.port), server_name: form.server_name, dest: form.dest, node_name: form.node_name })
    deployId.value = r.deploy_id
    step.value = 'log'
    startPoll()
  } catch (e) { toast.error('无法开始部署', (e as Error).message) } finally { busy.value = false }
}

// Live lines arrive over SSE; polling fills gaps (and history on reopen).
watch(() => gw.lastEvent, (e) => {
  if (!e || !deployId.value) return
  const d = e.data as { deploy_id?: string; id?: number; stream?: string; line?: string; at?: string; state?: string; error?: string }
  if (d.deploy_id !== deployId.value) return
  if (e.type === 'provision.log' && d.line !== undefined && !lines.value.some(l => l.id === d.id)) {
    lines.value.push({ id: d.id ?? 0, at: d.at ?? e.at, stream: (d.stream ?? 'stdout') as DeployLogLine['stream'], line: d.line })
    scroll()
  }
  if (e.type === 'provision.state' && d.state) { state.value = d.state; error.value = d.error ?? ''; if (d.state !== 'running') { stopPoll(); emit('finished') } }
})
function startPoll() {
  stopPoll()
  const tick = async () => {
    if (!deployId.value) return
    try {
      const last = lines.value.length ? lines.value[lines.value.length - 1].id : 0
      const r = await hostsApi.deployLogs(deployId.value, last)
      if (r.items.length) { lines.value.push(...r.items.filter(x => !lines.value.some(l => l.id === x.id))); scroll() }
      const d = await hostsApi.deploy_get(deployId.value)
      state.value = d.item.state; error.value = d.item.error ?? ''
      if (d.item.state !== 'running' && d.item.state !== 'queued') { stopPoll(); emit('finished') }
    } catch { /* transient */ }
  }
  void tick()
  poll = window.setInterval(tick, 2000)
}
function stopPoll() { if (poll) { window.clearInterval(poll); poll = null } }
function scroll() { requestAnimationFrame(() => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight }) }
const stateTone = computed(() => ({ succeeded: 'ok', failed: 'danger', running: 'info', queued: 'neutral', rolled_back: 'warn' } as const)[state.value as 'succeeded'] ?? 'neutral')
const stateLabel: Record<string, string> = { queued: '排队中', running: '进行中', succeeded: '成功', failed: '失败', rolled_back: '已回滚' }
</script>

<template>
  <Dialog v-model:open="open" :title="step === 'form' ? `部署到 ${host?.name ?? ''}` : `部署日志 · ${host?.name ?? ''}`" size="lg">
    <template v-if="step === 'form'">
      <div class="grid gap-3 sm:grid-cols-2">
        <button type="button" :class="['rounded-lg border p-3 text-left', form.kind === 'vless' ? 'border-accent bg-accent-soft/40' : 'border-border hover:bg-surface-2']" @click="form.kind = 'vless'">
          <p class="font-medium">VLESS + REALITY</p>
          <p class="mt-1 text-xs text-fg-muted">安装 Xray,TCP 443,伪装成真实站点的 TLS;部署后做一次真实握手自检。</p>
        </button>
        <button type="button" :class="['rounded-lg border p-3 text-left', form.kind === 'wg' ? 'border-accent bg-accent-soft/40' : 'border-border hover:bg-surface-2']" @click="form.kind = 'wg'">
          <p class="font-medium">WireGuard</p>
          <p class="mt-1 text-xs text-fg-muted">内核态 UDP 隧道,延迟低、吞吐高;更容易被识别为 WireGuard。</p>
        </button>
      </div>
      <div class="mt-4 grid gap-4 sm:grid-cols-2">
        <Field label="端口"><Input v-model="form.port" type="number" class="font-mono" /></Field>
        <Field label="出站节点名称" hint="部署成功后自动加入节点列表"><Input v-model="form.node_name" /></Field>
        <template v-if="form.kind === 'vless'">
          <Field label="伪装站点 (SNI)" hint="必须是能通过 REALITY 握手的站点"><Input v-model="form.server_name" placeholder="www.apple.com" /></Field>
          <Field label="dest" hint="留空 = SNI:443"><Input v-model="form.dest" class="font-mono" placeholder="www.apple.com:443" /></Field>
        </template>
      </div>
      <p class="mt-4 text-xs text-fg-subtle">脚本通过 SSH 标准输入执行,不在远端落盘;Xray 二进制按 XTLS 官方摘要校验后才安装。</p>
    </template>
    <template v-else>
      <div class="mb-2 flex items-center gap-2 text-sm">
        <Badge :tone="stateTone">{{ stateLabel[state] ?? state }}</Badge>
        <span v-if="error" class="truncate text-xs text-danger" :title="error">{{ error }}</span>
      </div>
      <div ref="logEl" class="scroll-thin h-80 overflow-auto rounded-lg border border-border bg-[#0b1020] p-3 font-mono text-[11px] leading-5 text-slate-200">
        <div v-for="l in lines" :key="l.id" :class="{ 'text-amber-300': l.stream === 'stderr', 'text-sky-300': l.stream === 'system' }">{{ l.line }}</div>
        <div v-if="!lines.length" class="text-slate-500">等待输出…</div>
      </div>
    </template>
    <template #footer>
      <Button variant="secondary" @click="open = false">{{ step === 'form' ? '取消' : '关闭' }}</Button>
      <Button v-if="step === 'form'" :loading="busy" @click="start">开始部署</Button>
    </template>
  </Dialog>
</template>
