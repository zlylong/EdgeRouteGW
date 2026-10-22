<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { ArrowDown, ArrowUp, X } from 'lucide-vue-next'
import Dialog from '@/components/ui/Dialog.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Select from '@/components/ui/Select.vue'
import Field from '@/components/ui/Field.vue'
import { groups as groupsApi } from '@/api/endpoints'
import type { GroupKind, Node, OutboundGroup } from '@/api/types'
import { ApiError } from '@/api/client'
import { useToast } from '@/stores/toast'

const props = defineProps<{ group?: OutboundGroup | null; nodes: Node[] }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ saved: [] }>()
const toast = useToast()
const form = reactive({ name: '', kind: 'failover' as GroupKind, members: [] as string[], strategy: 'leastPing' })

// Explanations shown under the selectors. Xray probes every member every
// 30 s against http://cp.cloudflare.com/ (observatory); leastPing/leastLoad
// decide from those measurements.
const kindHelp: Record<string, string> = {
  failover: '始终走第 1 个启用成员;它探测失败时切到第 2 个,恢复后切回。只用前两个成员,第 3 个起忽略。适合"主线路 + 备用线路"。',
  loadbalance: '每条新连接在所有启用成员之间按策略挑一个;已建立的连接不迁移。适合多条等价线路分摊,或按延迟自动择优。',
}
const strategyOptions = [
  { value: 'leastPing', label: 'leastPing · 最低延迟' },
  { value: 'leastLoad', label: 'leastLoad · 最稳定' },
  { value: 'random', label: 'random · 随机' },
  { value: 'roundRobin', label: 'roundRobin · 轮询' },
]
const strategyHelp: Record<string, string> = {
  leastPing: '新连接走最近一次探测往返时间最短的成员。Xray 每 30 秒探测一次所有成员;探测失败的成员不会被选中。线路延迟差异明显时用它。',
  leastLoad: '综合最近多次探测的延迟及其波动,优先挑延迟低且稳定的成员,并在几个相近的成员间分摊。线路时好时坏时比 leastPing 更稳。',
  random: '每条新连接随机挑一个成员,不看延迟。成员等价、只想均摊流量时用它;探测失败的成员仍会被排除。',
  roundRobin: '按成员顺序依次分配新连接,各成员连接数大致相等。和随机类似,但分配更均匀。',
}
const errors = ref<Record<string, string>>({})
const busy = ref(false)
const addId = ref('')

watch(open, (o) => {
  if (!o) return
  errors.value = {}
  const g = props.group
  form.name = g?.name ?? ''; form.kind = g?.kind ?? 'failover'; form.members = [...(g?.member_ids ?? [])]
  try { form.strategy = (JSON.parse(g?.strategy || '{}').strategy as string) || 'leastPing' } catch { form.strategy = 'leastPing' }
})
const nodeName = (id: string) => props.nodes.find(n => n.id === id)?.name ?? id.slice(0, 8)
const candidates = () => props.nodes.filter(n => !form.members.includes(n.id)).map(n => ({ value: n.id, label: n.name + (n.enabled ? '' : '(已停用)') }))
function add() { if (addId.value) { form.members.push(addId.value); addId.value = '' } }
function move(i: number, d: number) { const j = i + d; if (j < 0 || j >= form.members.length) return; [form.members[i], form.members[j]] = [form.members[j], form.members[i]] }

async function save() {
  errors.value = {}
  busy.value = true
  const input = { name: form.name, kind: form.kind, member_ids: form.members, strategy: form.kind === 'loadbalance' ? JSON.stringify({ strategy: form.strategy }) : '{}' }
  try {
    if (props.group) await groupsApi.update(props.group.id, input)
    else await groupsApi.create(input)
    toast.ok('出站组已保存')
    open.value = false
    emit('saved')
  } catch (e) {
    if (e instanceof ApiError && e.field) errors.value[e.field] = e.message
    else toast.error('保存失败', (e as Error).message)
  } finally { busy.value = false }
}
</script>

<template>
  <Dialog v-model:open="open" :title="group ? '编辑出站组' : '新建出站组'" description="规则可以指向出站组。故障转移组按顺序取前两个启用成员为主/备;负载组在所有启用成员间按策略分配。">
    <div class="space-y-4">
      <Field label="名称" :error="errors.name"><Input v-model="form.name" placeholder="如 海外主备" /></Field>
      <Field label="类型" :error="errors.kind" :hint="kindHelp[form.kind]">
        <Select v-model="form.kind" :options="[{ value: 'failover', label: '故障转移(主/备)' }, { value: 'loadbalance', label: '负载均衡' }]" />
      </Field>
      <Field v-if="form.kind === 'loadbalance'" label="策略" :hint="strategyHelp[form.strategy]">
        <Select v-model="form.strategy" :options="strategyOptions" />
      </Field>
      <Field label="成员(有序)" :error="errors.member_ids">
        <ul class="space-y-1">
          <li v-for="(id, i) in form.members" :key="id" class="flex items-center gap-2 rounded-md border border-border px-2 py-1.5 text-sm">
            <span class="w-5 text-center text-xs text-fg-subtle">{{ i + 1 }}</span>
            <span class="flex-1 truncate">{{ nodeName(id) }}</span>
            <span v-if="form.kind === 'failover' && i < 2" class="text-[10px] text-fg-subtle">{{ i === 0 ? '主' : '备' }}</span>
            <Button variant="ghost" size="icon" aria-label="上移" @click="move(i, -1)"><ArrowUp class="h-3.5 w-3.5" /></Button>
            <Button variant="ghost" size="icon" aria-label="下移" @click="move(i, 1)"><ArrowDown class="h-3.5 w-3.5" /></Button>
            <Button variant="ghost" size="icon" aria-label="移除" @click="form.members.splice(i, 1)"><X class="h-3.5 w-3.5" /></Button>
          </li>
        </ul>
        <div class="mt-2 flex gap-2">
          <Select v-model="addId" :options="[{ value: '', label: '选择节点…' }, ...candidates()]" class="flex-1" />
          <Button variant="secondary" :disabled="!addId" @click="add">添加</Button>
        </div>
      </Field>
    </div>
    <template #footer>
      <Button variant="secondary" @click="open = false">取消</Button>
      <Button :loading="busy" :disabled="!form.name || !form.members.length" @click="save">保存</Button>
    </template>
  </Dialog>
</template>
