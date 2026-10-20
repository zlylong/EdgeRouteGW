<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import Dialog from '@/components/ui/Dialog.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Select from '@/components/ui/Select.vue'
import Field from '@/components/ui/Field.vue'
import { routing } from '@/api/endpoints'
import type { DeviceAction, DeviceKind, DevicePolicy } from '@/api/types'
import { ApiError } from '@/api/client'
import { useToast } from '@/stores/toast'

const props = defineProps<{ policy?: DevicePolicy | null }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ saved: [] }>()
const toast = useToast()
const form = reactive({ kind: 'mac' as DeviceKind, value: '', action: 'direct' as DeviceAction, remark: '' })
const errors = ref<Record<string, string>>({})
const busy = ref(false)
watch(open, o => { if (o) { errors.value = {}; Object.assign(form, { kind: props.policy?.kind ?? 'mac', value: props.policy?.value ?? '', action: props.policy?.action ?? 'direct', remark: props.policy?.remark ?? '' }) } })

async function save() {
  errors.value = {}; busy.value = true
  try {
    const input = { kind: form.kind === 'cidr' ? 'ip' as DeviceKind : form.kind, value: form.value, action: form.action, remark: form.remark }
    if (props.policy) await routing.updateDevice(props.policy.id, input); else await routing.createDevice(input)
    toast.ok('设备策略已保存'); open.value = false; emit('saved')
  } catch (e) {
    if (e instanceof ApiError && e.field) errors.value[e.field] = e.message
    else toast.error('保存失败', (e as Error).message)
  } finally { busy.value = false }
}
</script>

<template>
  <Dialog v-model:open="open" :title="policy ? '编辑设备策略' : '添加设备策略'" description="按来源 MAC 或 IP 决定设备流量:强制代理、绕过代理直连,或直接拦截断网。仅 Mode A 生效。" size="sm">
    <div class="space-y-4">
      <Field label="识别方式"><Select v-model="form.kind" :options="[{ value: 'mac', label: 'MAC 地址' }, { value: 'ip', label: 'IP 地址或网段' }]" /></Field>
      <Field label="值" :error="errors.value"><Input v-model="form.value" class="font-mono" :placeholder="form.kind === 'mac' ? 'aa:bb:cc:dd:ee:ff' : '192.168.1.10 或 192.168.1.0/24'" /></Field>
      <Field label="动作" :error="errors.action"><Select v-model="form.action" :options="[{ value: 'proxy', label: '代理' }, { value: 'direct', label: '直连(绕过)' }, { value: 'reject', label: '拦截(断网)' }]" /></Field>
      <Field label="备注"><Input v-model="form.remark" placeholder="如 客厅电视" /></Field>
    </div>
    <template #footer>
      <Button variant="secondary" @click="open = false">取消</Button>
      <Button :loading="busy" :disabled="!form.value" @click="save">保存</Button>
    </template>
  </Dialog>
</template>
