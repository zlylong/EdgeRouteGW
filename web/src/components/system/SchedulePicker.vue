<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQueryClient } from '@tanstack/vue-query'
import Select from '@/components/ui/Select.vue'
import Input from '@/components/ui/Input.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import { components as compApi } from '@/api/endpoints'
import { useToast } from '@/stores/toast'

// Per-component auto-update schedule. Spec strings mirror the server:
//   off | daily HH:MM | weekly <mon..sun> HH:MM | monthly <1..28> HH:MM
const props = defineProps<{ name: string; label: string; schedule?: string; hint: string; disabled?: boolean }>()
const qc = useQueryClient()
const toast = useToast()

const KINDS = [{ value: 'off', label: '关闭' }, { value: 'daily', label: '每天' }, { value: 'weekly', label: '每周' }, { value: 'monthly', label: '每月' }]
const WEEKDAYS = [
  { value: 'mon', label: '周一' }, { value: 'tue', label: '周二' }, { value: 'wed', label: '周三' }, { value: 'thu', label: '周四' },
  { value: 'fri', label: '周五' }, { value: 'sat', label: '周六' }, { value: 'sun', label: '周日' },
]
const DAYS = Array.from({ length: 28 }, (_, i) => ({ value: String(i + 1), label: `${i + 1} 日` }))
const NUM2DAY: Record<string, string> = { '1': 'mon', '2': 'tue', '3': 'wed', '4': 'thu', '5': 'fri', '6': 'sat', '7': 'sun' }

const kind = ref('off'); const weekday = ref('sun'); const day = ref('1'); const time = ref('04:30'); const saving = ref(false)
function load(spec?: string) {
  const f = (spec ?? 'off').trim().toLowerCase().split(/\s+/)
  kind.value = ['daily', 'weekly', 'monthly'].includes(f[0]) ? f[0] : 'off'
  if (kind.value === 'daily') time.value = f[1] ?? '04:30'
  if (kind.value === 'weekly') { weekday.value = NUM2DAY[f[1]] ?? f[1] ?? 'sun'; time.value = f[2] ?? '04:30' }
  if (kind.value === 'monthly') { day.value = f[1] ?? '1'; time.value = f[2] ?? '04:30' }
}
watch(() => props.schedule, load, { immediate: true })

const spec = computed(() => {
  if (kind.value === 'off') return 'off'
  if (kind.value === 'daily') return `daily ${time.value}`
  if (kind.value === 'weekly') return `weekly ${weekday.value} ${time.value}`
  return `monthly ${day.value} ${time.value}`
})
const dirty = computed(() => spec.value !== (props.schedule ?? 'off'))
const summary = computed(() => {
  const s = (props.schedule ?? 'off').split(/\s+/)
  if (s[0] === 'daily') return `每天 ${s[1]}`
  if (s[0] === 'weekly') return `${WEEKDAYS.find(w => w.value === (NUM2DAY[s[1]] ?? s[1]))?.label ?? s[1]} ${s[2]}`
  if (s[0] === 'monthly') return `每月 ${s[1]} 日 ${s[2]}`
  return '关闭'
})
async function save() {
  saving.value = true
  try { await compApi.setSchedule(props.name, spec.value); toast.ok(`${props.label} 自动更新已保存`); qc.invalidateQueries({ queryKey: ['components'] }) } catch (e) { toast.error('保存失败', (e as Error).message) } finally { saving.value = false }
}
</script>

<template>
  <div class="mt-4 rounded-md border border-border bg-surface-2/40 p-3">
    <div class="flex items-center justify-between">
      <span class="text-xs font-medium text-fg">自动更新</span>
      <Badge :tone="(props.schedule ?? 'off') === 'off' ? 'neutral' : 'ok'">{{ summary }}</Badge>
    </div>
    <div class="mt-2 flex flex-wrap items-center gap-2">
      <Select v-model="kind" class="w-24" :options="KINDS" :disabled="disabled" />
      <Select v-if="kind === 'weekly'" v-model="weekday" class="w-24" :options="WEEKDAYS" :disabled="disabled" />
      <Select v-if="kind === 'monthly'" v-model="day" class="w-24" :options="DAYS" :disabled="disabled" />
      <Input v-if="kind !== 'off'" v-model="time" type="time" class="w-28" :disabled="disabled" />
      <Button size="sm" variant="secondary" :disabled="!dirty || disabled" :loading="saving" @click="save">保存</Button>
    </div>
    <p class="mt-2 text-xs text-fg-subtle">{{ hint }}</p>
  </div>
</template>
