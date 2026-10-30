<script setup lang="ts">
// Create/edit a node. Protocol-specific fields are shown as a form; the raw
// params JSON stays available under "高级" for anything the form lacks.
import { computed, reactive, ref, watch } from 'vue'
import Dialog from '@/components/ui/Dialog.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Select from '@/components/ui/Select.vue'
import Field from '@/components/ui/Field.vue'
import Textarea from '@/components/ui/Textarea.vue'
import Switch from '@/components/ui/Switch.vue'
import { nodes as nodesApi } from '@/api/endpoints'
import type { Node, NodeInput, Protocol } from '@/api/types'
import { ApiError } from '@/api/client'
import { useToast } from '@/stores/toast'

const props = defineProps<{ node?: Node | null }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ saved: [] }>()
const toast = useToast()

const protocols: { value: Protocol; label: string }[] = [
  { value: 'vless', label: 'VLESS' }, { value: 'vmess', label: 'VMess' }, { value: 'trojan', label: 'Trojan' },
  { value: 'shadowsocks', label: 'Shadowsocks' }, { value: 'hysteria', label: 'Hysteria 2' }, { value: 'socks', label: 'SOCKS5' }, { value: 'http', label: 'HTTP' },
  { value: 'wireguard', label: 'WireGuard' }, { value: 'custom', label: '自定义 (Xray JSON)' },
]
const transports = ['tcp', 'ws', 'grpc', 'xhttp', 'h2'].map(v => ({ value: v, label: v }))
const securities = [{ value: 'none', label: '无' }, { value: 'tls', label: 'TLS' }, { value: 'reality', label: 'REALITY' }]
const obfsKinds = [{ value: 'none', label: '无' }, { value: 'salamander', label: 'Salamander' }]
const ssMethods = ['aes-256-gcm', 'aes-128-gcm', 'chacha20-ietf-poly1305', '2022-blake3-aes-256-gcm', '2022-blake3-aes-128-gcm'].map(v => ({ value: v, label: v }))

const form = reactive({
  name: '', protocol: 'vless' as Protocol, address: '', port: 443, enabled: true, credential: '',
  type: 'tcp', security: 'reality', sni: '', fp: 'chrome', pbk: '', sid: '', flow: 'xtls-rprx-vision', method: 'aes-256-gcm',
  pin: '', obfs: 'none', obfsPassword: '', up: '', down: '', hop: '',
  rawParams: '{}', advanced: false,
})
const errors = ref<Record<string, string>>({})
const busy = ref(false)
const editing = computed(() => !!props.node)
const credentialLabel = computed(() => ({
  vmess: 'UUID', vless: 'UUID', trojan: '密码', shadowsocks: '密码', socks: '用户名:密码(可选)', http: '用户名:密码(可选)', hysteria: '认证密码', wireguard: '客户端私钥', custom: '凭证(可选,注入 settings 时使用)',
}[form.protocol]))
const showTransport = computed(() => ['vless', 'vmess', 'trojan'].includes(form.protocol))
const needsSettings = computed(() => form.protocol === 'wireguard' || form.protocol === 'custom')

watch(open, (o) => { if (o) reset() })

function reset() {
  errors.value = {}
  const n = props.node
  Object.assign(form, { name: '', protocol: 'vless', address: '', port: 443, enabled: true, credential: '', type: 'tcp', security: 'reality', sni: '', fp: 'chrome', pbk: '', sid: '', flow: 'xtls-rprx-vision', method: 'aes-256-gcm', pin: '', obfs: 'none', obfsPassword: '', up: '', down: '', hop: '', rawParams: '{}', advanced: false })
  if (!n) return
  Object.assign(form, { name: n.name, protocol: n.protocol, address: n.address, port: n.port, enabled: n.enabled, rawParams: n.params || '{}' })
  try {
    const p = JSON.parse(n.params || '{}') as Record<string, unknown>
    const str = (k: string) => (typeof p[k] === 'string' ? (p[k] as string) : '')
    if (str('type')) form.type = str('type')
    if (str('security')) form.security = str('security')
    else if (!('streamSettings' in p)) form.security = 'none'
    form.sni = str('sni'); form.fp = str('fp') || form.fp; form.pbk = str('pbk'); form.sid = str('sid'); form.flow = str('flow'); form.method = str('method') || form.method
    form.pin = str('pin'); form.obfs = str('obfs') || 'none'; form.obfsPassword = str('obfs_password'); form.up = str('up'); form.down = str('down'); form.hop = str('hop')
    // Anything beyond the shorthand keys means the form cannot represent it.
    const known = new Set(['type', 'security', 'sni', 'fp', 'pbk', 'sid', 'flow', 'method', 'pin', 'obfs', 'obfs_password', 'up', 'down', 'hop'])
    form.advanced = Object.keys(p).some(k => !known.has(k)) || needsSettings.value
  } catch { form.advanced = true }
}

function buildParams(): string {
  if (form.advanced || needsSettings.value) return form.rawParams.trim() || '{}'
  const p: Record<string, unknown> = {}
  if (showTransport.value) {
    p.type = form.type
    if (form.security !== 'none') {
      p.security = form.security
      if (form.sni) p.sni = form.sni
      if (form.fp) p.fp = form.fp
      if (form.security === 'reality') { p.pbk = form.pbk; p.sid = form.sid }
    }
    if (form.protocol === 'vless' && form.flow) p.flow = form.flow
  }
  if (form.protocol === 'shadowsocks') p.method = form.method
  if (form.protocol === 'hysteria') {
    if (form.sni) p.sni = form.sni
    if (form.pin) p.pin = form.pin
    if (form.obfs !== 'none') { p.obfs = form.obfs; p.obfs_password = form.obfsPassword }
    if (form.up) p.up = form.up
    if (form.down) p.down = form.down
    if (form.hop) p.hop = form.hop
  }
  return JSON.stringify(p)
}

async function save() {
  errors.value = {}
  const input: NodeInput = { name: form.name, protocol: form.protocol, address: form.address, port: Number(form.port), params: buildParams(), enabled: form.enabled }
  if (form.credential || !editing.value) input.credential = form.credential
  busy.value = true
  try {
    if (props.node) await nodesApi.update(props.node.id, input)
    else await nodesApi.create(input)
    toast.ok(editing.value ? '节点已更新' : '节点已添加')
    open.value = false
    emit('saved')
  } catch (e) {
    if (e instanceof ApiError && e.field) errors.value[e.field] = e.message
    else toast.error('保存失败', (e as Error).message)
  } finally { busy.value = false }
}
</script>

<template>
  <Dialog v-model:open="open" :title="editing ? '编辑节点' : '添加节点'" size="lg">
    <div class="grid gap-4 sm:grid-cols-2">
      <Field label="名称" :error="errors.name"><Input v-model="form.name" placeholder="如 SG-01" autofocus /></Field>
      <Field label="协议" :error="errors.protocol"><Select v-model="form.protocol" :options="protocols" :disabled="editing" /></Field>
      <Field label="地址" :error="errors.address"><Input v-model="form.address" placeholder="域名或 IP" /></Field>
      <Field label="端口" :error="errors.port"><Input v-model="form.port" type="number" min="1" max="65535" /></Field>
      <Field class="sm:col-span-2" :label="credentialLabel" :error="errors.credential" :hint="editing && node?.has_credential ? '留空则保留现有凭证' : undefined">
        <Input v-model="form.credential" type="password" autocomplete="off" :placeholder="editing && node?.has_credential ? '••••••••' : ''" />
      </Field>
    </div>

    <div v-if="showTransport && !form.advanced" class="mt-4 grid gap-4 rounded-lg border border-border bg-surface-2/50 p-4 sm:grid-cols-2">
      <Field label="传输"><Select v-model="form.type" :options="transports" /></Field>
      <Field label="安全层"><Select v-model="form.security" :options="securities" /></Field>
      <template v-if="form.security !== 'none'">
        <Field label="SNI"><Input v-model="form.sni" placeholder="www.apple.com" /></Field>
        <Field label="指纹 (fp)"><Input v-model="form.fp" placeholder="chrome" /></Field>
      </template>
      <template v-if="form.security === 'reality'">
        <Field label="公钥 (pbk)"><Input v-model="form.pbk" class="font-mono" /></Field>
        <Field label="Short ID (sid)"><Input v-model="form.sid" class="font-mono" /></Field>
      </template>
      <Field v-if="form.protocol === 'vless'" label="Flow"><Input v-model="form.flow" placeholder="xtls-rprx-vision 或留空" /></Field>
    </div>
    <div v-if="form.protocol === 'hysteria' && !form.advanced" class="mt-4 grid gap-4 rounded-lg border border-border bg-surface-2/50 p-4 sm:grid-cols-2">
      <Field label="SNI" hint="留空用地址"><Input v-model="form.sni" placeholder="与服务端证书一致" /></Field>
      <Field label="证书指纹 (pinSHA256)" hint="自签证书必填"><Input v-model="form.pin" class="font-mono" placeholder="sha256 hex,可带冒号" /></Field>
      <Field label="混淆"><Select v-model="form.obfs" :options="obfsKinds" /></Field>
      <Field v-if="form.obfs === 'salamander'" label="混淆密码"><Input v-model="form.obfsPassword" type="password" autocomplete="off" /></Field>
      <Field label="上行带宽 (Brutal)" hint="留空用 BBR"><Input v-model="form.up" placeholder="50 mbps" /></Field>
      <Field label="下行带宽 (Brutal)" hint="留空用 BBR"><Input v-model="form.down" placeholder="200 mbps" /></Field>
      <Field label="端口跳跃范围" hint="服务端开了才填"><Input v-model="form.hop" placeholder="20000-50000" /></Field>
      <p class="text-xs text-fg-subtle sm:col-span-2">Xray 原生 Hysteria 2 出站(QUIC + TLS,ALPN h3)。Xray 不再支持跳过证书校验(insecure),自签证书请填指纹。</p>
    </div>
    <div v-if="form.protocol === 'shadowsocks' && !form.advanced" class="mt-4">
      <Field label="加密方式"><Select v-model="form.method" :options="ssMethods" /></Field>
    </div>

    <div class="mt-4">
      <div class="flex items-center justify-between">
        <label class="text-xs font-medium text-fg-muted">高级:原始参数 JSON</label>
        <Switch v-model="form.advanced" :disabled="needsSettings" label="高级模式" />
      </div>
      <p v-if="needsSettings" class="mt-1 text-xs text-fg-subtle">{{ form.protocol === 'wireguard' ? 'WireGuard 需要 params.settings(peers、address 等),私钥由上面的凭证注入为 secretKey。' : '自定义节点把 params 原样作为 Xray 出站片段,必须包含 settings。' }}</p>
      <Textarea v-if="form.advanced || needsSettings" v-model="form.rawParams" rows="7" class="mt-2 font-mono text-xs" :invalid="!!errors.params" />
      <p v-if="errors.params" class="mt-1 text-xs text-danger">{{ errors.params }}</p>
    </div>

    <label class="mt-4 flex items-center gap-2 text-sm"><Switch v-model="form.enabled" label="启用" /> 启用此节点</label>

    <template #footer>
      <Button variant="secondary" @click="open = false">取消</Button>
      <Button :loading="busy" @click="save">保存</Button>
    </template>
  </Dialog>
</template>
