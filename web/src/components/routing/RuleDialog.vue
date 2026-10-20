<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import Dialog from '@/components/ui/Dialog.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Textarea from '@/components/ui/Textarea.vue'
import Select from '@/components/ui/Select.vue'
import Field from '@/components/ui/Field.vue'
import { routing } from '@/api/endpoints'
import type { MatchKind, Node, OutboundGroup, Rule, RuleAction, RuleSet } from '@/api/types'
import { ApiError } from '@/api/client'
import { useToast } from '@/stores/toast'
import { useGatewayState } from '@/stores/state'

const props = defineProps<{ rule?: Rule | null; setId: string; sets: RuleSet[]; nodes: Node[]; groups: OutboundGroup[] }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ saved: [] }>()
const toast = useToast()
const gw = useGatewayState()

const form = reactive({ set_id: '', match_kind: 'domain' as MatchKind, match_value: '', action_kind: 'proxy' as RuleAction, action_ref: '', remark: '' })
const errors = ref<Record<string, string>>({})
const busy = ref(false)
const editing = computed(() => !!props.rule)

watch(open, (o) => {
  if (!o) return
  errors.value = {}
  const r = props.rule
  Object.assign(form, { set_id: r?.set_id ?? props.setId, match_kind: r?.match_kind ?? 'domain', match_value: r?.match_value ?? '', action_kind: r?.action_kind ?? 'proxy', action_ref: r?.action_ref ?? '', remark: r?.remark ?? '' })
})

const kinds = [
  { value: 'domain', label: '域名' }, { value: 'geosite', label: 'GeoSite 标签' }, { value: 'ip', label: 'IP / CIDR' }, { value: 'geoip', label: 'GeoIP 标签' },
]
const actions = computed(() => [
  { value: 'proxy', label: '代理(默认出站)' }, { value: 'direct', label: '直连' }, { value: 'block', label: '拦截' },
  { value: 'node', label: '指定节点', disabled: !props.nodes.length }, { value: 'group', label: '指定出站组', disabled: !props.groups.length },
])
const refs = computed(() => form.action_kind === 'node'
  ? props.nodes.map(n => ({ value: n.id, label: n.name + (n.enabled ? '' : '(已停用)') }))
  : props.groups.map(g => ({ value: g.id, label: g.name })))
const placeholder = computed(() => ({
  domain: editing.value ? 'example.com' : 'example.com\n**.google.com\n*.cdn.example.org\n(可多行或逗号分隔批量添加)',
  geosite: 'cn / netflix / category-ads-all',
  ip: '1.1.1.1 或 10.0.0.0/8',
  geoip: 'cn / telegram / private',
}[form.match_kind]))
const wildcardWarn = computed(() => form.match_kind === 'domain' && gw.state?.mode !== 'A' && /(^|\s|,)\*/.test(form.match_value))

watch(() => form.action_kind, (k) => { if (k !== 'node' && k !== 'group') form.action_ref = ''; else if (!form.action_ref && refs.value.length) form.action_ref = refs.value[0].value })

async function save() {
  errors.value = {}
  busy.value = true
  const input = { set_id: form.set_id, match_kind: form.match_kind, match_value: form.match_value, action_kind: form.action_kind, action_ref: form.action_ref || undefined, remark: form.remark || undefined }
  try {
    if (props.rule) {
      await routing.updateRule(props.rule.id, input)
      if (props.rule.set_id !== form.set_id) await routing.moveRule(props.rule.id, form.set_id)
      toast.ok('规则已更新')
    } else {
      const res = await routing.createRules(input)
      toast.ok(`已添加 ${res.items.length} 条规则`)
    }
    open.value = false
    emit('saved')
  } catch (e) {
    if (e instanceof ApiError && e.field) errors.value[e.field] = e.message
    else toast.error('保存失败', (e as Error).message)
  } finally { busy.value = false }
}
</script>

<template>
  <Dialog v-model:open="open" :title="editing ? '编辑规则' : '添加规则'" size="md">
    <div class="space-y-4">
      <div class="grid gap-4 sm:grid-cols-2">
        <Field label="规则集" :error="errors.set_id"><Select v-model="form.set_id" :options="sets.map(s => ({ value: s.id, label: s.name }))" /></Field>
        <Field label="匹配类型" :error="errors.match_kind"><Select v-model="form.match_kind" :options="kinds" /></Field>
      </div>
      <Field label="匹配值" :error="errors.match_value" :hint="form.match_kind === 'domain' ? 'c.com 仅根域 · **.c.com 任意子域 · *.c.com 一层子域' : undefined">
        <Textarea v-if="form.match_kind === 'domain' && !editing" v-model="form.match_value" rows="4" class="font-mono text-xs" :placeholder="placeholder" :invalid="!!errors.match_value" autofocus />
        <Input v-else v-model="form.match_value" class="font-mono" :placeholder="placeholder" :invalid="!!errors.match_value" />
        <p v-if="wildcardWarn" class="mt-1 text-xs text-warn">当前为 Mode {{ gw.state?.mode }},通配域名无法转成路由宣告,后端会拒绝。</p>
      </Field>
      <div class="grid gap-4 sm:grid-cols-2">
        <Field label="动作" :error="errors.action_kind"><Select v-model="form.action_kind" :options="actions" /></Field>
        <Field v-if="form.action_kind === 'node' || form.action_kind === 'group'" :label="form.action_kind === 'node' ? '节点' : '出站组'" :error="errors.action_ref">
          <Select v-model="form.action_ref" :options="refs" />
        </Field>
      </div>
      <Field label="备注" :error="errors.remark"><Input v-model="form.remark" placeholder="可选" /></Field>
    </div>
    <template #footer>
      <Button variant="secondary" @click="open = false">取消</Button>
      <Button :loading="busy" :disabled="!form.match_value.trim() || !form.set_id" @click="save">保存</Button>
    </template>
  </Dialog>
</template>
