<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { RotateCcw } from 'lucide-vue-next'
import Card from '@/components/ui/Card.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Textarea from '@/components/ui/Textarea.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Field from '@/components/ui/Field.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import { config, network, system } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import { useToast } from '@/stores/toast'
import { useGatewayState } from '@/stores/state'

const qc = useQueryClient()
const toast = useToast()
const gw = useGatewayState()
const dnsQ = useQuery({ queryKey: ['dns'], queryFn: config.dns })
const cfgQ = useQuery({ queryKey: ['artifact', 'mosdns'], queryFn: () => system.artifact('mosdns') })
const form = reactive({ local: '', remote: '', cache_size: 10240, lazy_cache: true, lazy_cache_ttl: 86400, log_level: 'info', hijack: true, block_dot: true })
const errors = ref<Record<string, string>>({})
const busy = ref(false)
const dirty = ref(false)

watch(() => dnsQ.data.value, (d) => {
  if (!d || dirty.value) return
  const c = d.item
  Object.assign(form, { local: c.local_upstreams.join('\n'), remote: c.remote_upstreams.join('\n'), cache_size: c.cache_size, lazy_cache: c.lazy_cache, lazy_cache_ttl: c.lazy_cache_ttl, log_level: c.log_level, hijack: c.hijack ?? true, block_dot: c.block_dot ?? true })
}, { immediate: true })
watch(form, () => { dirty.value = true })

const lines = (s: string) => s.split(/[\n,]/).map(x => x.trim()).filter(Boolean)
async function save() {
  errors.value = {}; busy.value = true
  try {
    await config.putDns({ local_upstreams: lines(form.local), remote_upstreams: lines(form.remote), cache_size: Number(form.cache_size), lazy_cache: form.lazy_cache, lazy_cache_ttl: Number(form.lazy_cache_ttl), log_level: form.log_level, hijack: form.hijack, block_dot: form.block_dot })
    dirty.value = false
    toast.ok('DNS 设置已保存', '上游变化时解析缓存会被清空并重新解析')
    qc.invalidateQueries({ queryKey: ['dns'] }); qc.invalidateQueries({ queryKey: ['artifact'] })
  } catch (e) {
    if (e instanceof ApiError && e.field) errors.value[e.field] = e.message
    else toast.error('保存失败', (e as Error).message)
  } finally { busy.value = false }
}
async function clearCache() {
  try { await network.clearDnsCache(); toast.ok('解析缓存已清空', 'Mode C 的域名会在下一轮重新解析') } catch (e) { toast.error('操作失败', (e as Error).message) }
}
function restoreDefaults() {
  Object.assign(form, { local: '119.29.29.29\n223.5.5.5', remote: '1.1.1.1\n8.8.8.8', cache_size: 10240, lazy_cache: true, lazy_cache_ttl: 86400, log_level: 'info', hijack: true, block_dot: true })
}
</script>

<template>
  <div class="grid gap-4 lg:grid-cols-[1fr_1fr]">
    <Card title="上游与缓存">
      <template #actions><Button variant="ghost" size="sm" @click="restoreDefaults">恢复推荐值</Button></template>
      <div class="space-y-4">
        <Field label="本地上游(国内域名)" hint="每行一个;IP、IP:端口或 tls:// https:// 地址" :error="errors.local_upstreams">
          <Textarea v-model="form.local" rows="3" class="font-mono text-xs" />
        </Field>
        <Field label="远端上游(其它域名)" hint="公网地址自动经本机 SOCKS 代理并强制 TCP,避免污染" :error="errors.remote_upstreams">
          <Textarea v-model="form.remote" rows="3" class="font-mono text-xs" />
        </Field>
        <div class="grid gap-4 md:grid-cols-3">
          <Field label="缓存条数" :error="errors.cache_size"><Input v-model="form.cache_size" type="number" min="0" /></Field>
          <Field label="惰性缓存 TTL(秒)" :error="errors.lazy_cache_ttl"><Input v-model="form.lazy_cache_ttl" type="number" min="0" :disabled="!form.lazy_cache" /></Field>
          <Field label="日志级别" :error="errors.log_level"><Select v-model="form.log_level" :options="['debug', 'info', 'warn', 'error'].map(v => ({ value: v, label: v }))" /></Field>
        </div>
        <label class="flex items-center gap-2 text-sm"><Switch v-model="form.lazy_cache" label="惰性缓存" /> 惰性缓存:上游超时时先返回过期答案再后台刷新</label>
        <div class="mt-4 rounded-lg border border-border bg-surface-2/40 p-3">
          <div class="mb-2 text-xs font-semibold uppercase tracking-wide text-fg-muted">防 DNS 泄露</div>
          <label class="flex items-center gap-2 text-sm"><Switch v-model="form.hijack" label="劫持 LAN DNS" /> 劫持 LAN 的 DNS 查询:经过网关的所有 53 端口查询都交给本机 Mosdns,无论设备配了什么 DNS(私网地址的内部 DNS 除外)</label>
          <label class="mt-2 flex items-center gap-2 text-sm"><Switch v-model="form.block_dot" label="拦截 DoT" /> 拦截 DNS-over-TLS / QUIC(853):让这类客户端退回普通 DNS,从而被劫持。DoH(443)无法通用识别,不在此列</label>
        </div>
        <p class="text-xs text-fg-subtle">
          当前模式 {{ gw.state?.mode }}:命中代理规则的域名{{ gw.state?.mode === 'B' ? '返回 Fake-IP(198.18.0.0/16)' : '经远端上游解析' }};geosite:cn 走本地上游;其余走远端上游。
        </p>
      </div>
      <div class="mt-5 flex justify-between">
        <Button variant="secondary" @click="clearCache"><RotateCcw class="h-4 w-4" /> 清空解析缓存</Button>
        <Button :loading="busy" :disabled="!dirty" @click="save">保存</Button>
      </div>
    </Card>
    <Card title="生成的 Mosdns 配置" body-class="p-2">
      <CodeBlock :code="cfgQ.data.value ?? ''" max-height="70vh" />
    </Card>
  </div>
</template>
