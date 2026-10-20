<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import Dialog from '@/components/ui/Dialog.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Textarea from '@/components/ui/Textarea.vue'
import Select from '@/components/ui/Select.vue'
import Field from '@/components/ui/Field.vue'
import { hosts as hostsApi } from '@/api/endpoints'
import type { RemoteHost } from '@/api/types'
import { ApiError } from '@/api/client'
import { useToast } from '@/stores/toast'

const props = defineProps<{ host?: RemoteHost | null }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ saved: [] }>()
const toast = useToast()
const form = reactive({ name: '', ssh_host: '', ssh_port: 22, ssh_user: 'root', ssh_auth_kind: 'password' as 'password' | 'key', credential: '', region: '', remark: '' })
const errors = ref<Record<string, string>>({})
const busy = ref(false)
watch(open, o => {
  if (!o) return
  errors.value = {}
  const h = props.host
  Object.assign(form, { name: h?.name ?? '', ssh_host: h?.ssh_host ?? '', ssh_port: h?.ssh_port ?? 22, ssh_user: h?.ssh_user ?? 'root', ssh_auth_kind: h?.ssh_auth_kind ?? 'password', credential: '', region: h?.region ?? '', remark: h?.remark ?? '' })
})
async function save() {
  errors.value = {}; busy.value = true
  try {
    const input = { ...form, ssh_port: Number(form.ssh_port), credential: form.credential || undefined }
    if (props.host) await hostsApi.update(props.host.id, input); else await hostsApi.create(input)
    toast.ok(props.host ? '主机已更新' : '主机已添加', '先「检查连接」钉定主机指纹,再部署')
    open.value = false; emit('saved')
  } catch (e) {
    if (e instanceof ApiError && e.field) errors.value[e.field] = e.message
    else toast.error('保存失败', (e as Error).message)
  } finally { busy.value = false }
}
</script>

<template>
  <Dialog v-model:open="open" :title="host ? '编辑主机' : '添加托管主机'" description="海外 Linux 主机(Debian/Ubuntu,root 或可 sudo 的用户)。凭证加密存储,只写不读。">
    <div class="grid gap-4 sm:grid-cols-2">
      <Field label="名称" :error="errors.name"><Input v-model="form.name" placeholder="如 SG-Vultr" autofocus /></Field>
      <Field label="地区" :error="errors.region"><Input v-model="form.region" placeholder="可选,如 新加坡" /></Field>
      <Field label="SSH 地址" :error="errors.ssh_host"><Input v-model="form.ssh_host" class="font-mono" placeholder="IP 或域名" /></Field>
      <Field label="SSH 端口" :error="errors.ssh_port"><Input v-model="form.ssh_port" type="number" /></Field>
      <Field label="用户名" :error="errors.ssh_user"><Input v-model="form.ssh_user" /></Field>
      <Field label="认证方式" :error="errors.ssh_auth_kind"><Select v-model="form.ssh_auth_kind" :options="[{ value: 'password', label: '密码' }, { value: 'key', label: '私钥' }]" /></Field>
      <Field class="sm:col-span-2" :label="form.ssh_auth_kind === 'key' ? '私钥(PEM)' : '密码'" :error="errors.credential" :hint="host?.has_credential ? '留空则保留现有凭证' : undefined">
        <Textarea v-if="form.ssh_auth_kind === 'key'" v-model="form.credential" rows="5" class="font-mono text-xs" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
        <Input v-else v-model="form.credential" type="password" autocomplete="off" />
      </Field>
      <Field class="sm:col-span-2" label="备注"><Input v-model="form.remark" /></Field>
    </div>
    <template #footer>
      <Button variant="secondary" @click="open = false">取消</Button>
      <Button :loading="busy" :disabled="!form.name || !form.ssh_host" @click="save">保存</Button>
    </template>
  </Dialog>
</template>
