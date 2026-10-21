<script setup lang="ts">
// Searchable picker for geosite / geoip tags read from the installed
// geodata. Multi-select by default (values join with commas, which the
// rules API accepts for batch creation); `single` restricts to one value
// when editing an existing rule.
import { computed, ref, watch } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import Input from '@/components/ui/Input.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { geo } from '@/api/endpoints'

const props = defineProps<{ kind: 'geosite' | 'geoip'; single?: boolean; invalid?: boolean }>()
const model = defineModel<string>({ default: '' })

const q = useQuery({ queryKey: ['geo-tags', () => props.kind], queryFn: () => geo.tags(props.kind), staleTime: 5 * 60_000 })
// Single mode (editing one rule) starts filtered on the current value so it
// shows up selected at the top instead of somewhere in 1500 rows.
const query = ref(props.single ? model.value : '')
const manual = ref(false)

const selected = computed<string[]>(() => model.value.split(/[\s,;]+/).map(s => s.trim().toLowerCase()).filter(Boolean))
const tags = computed(() => q.data.value?.items ?? [])
const tagSet = computed(() => new Set(tags.value))
const filtered = computed(() => {
  const needle = query.value.trim().toLowerCase()
  const list = needle ? tags.value.filter(t => t.includes(needle)) : tags.value
  // exact hits first, then prefix, then substring
  return [...list].sort((a, b) => rank(a, needle) - rank(b, needle) || a.localeCompare(b)).slice(0, 300)
})
function rank(t: string, n: string) { return !n ? 0 : t === n ? 0 : t.startsWith(n) ? 1 : 2 }
const unknown = computed(() => selected.value.filter(t => !tagSet.value.has(t.replace(/^!/, ''))))

function toggle(tag: string) {
  if (props.single) { model.value = tag; query.value = tag; return }
  const cur = selected.value
  model.value = (cur.includes(tag) ? cur.filter(t => t !== tag) : [...cur, tag]).join(',')
}
function remove(tag: string) { model.value = selected.value.filter(t => t !== tag).join(','); if (props.single) query.value = '' }
function onEnter() {
  // Enter on a query that is itself a valid tag (or a negation like !cn) adds it.
  const v = query.value.trim().toLowerCase()
  if (!v) return
  if (tagSet.value.has(v) || tagSet.value.has(v.replace(/^!/, ''))) { toggle(v); query.value = '' }
}
watch(() => props.kind, () => { query.value = '' })
</script>

<template>
  <div class="space-y-2">
    <!-- geodata missing or user opted out: plain text -->
    <template v-if="manual || (q.isSuccess.value && !q.data.value?.installed)">
      <Input v-model="model" class="font-mono" :placeholder="kind === 'geosite' ? 'cn, netflix, category-ads-all' : 'cn, telegram, private'" :invalid="invalid" />
      <p class="text-xs text-fg-muted">
        <template v-if="q.isSuccess.value && !q.data.value?.installed">未安装 GeoData,无法列出标签;到「系统 → 组件更新」安装后可按列表选择。</template>
        <template v-else><button type="button" class="underline" @click="manual = false">改为从列表选择</button></template>
      </p>
    </template>
    <template v-else>
      <div v-if="selected.length" class="flex flex-wrap gap-1.5">
        <span v-for="t in selected" :key="t" class="inline-flex items-center gap-1 rounded-md bg-accent/10 px-2 py-0.5 font-mono text-xs text-accent">
          {{ t }}<button type="button" class="opacity-60 hover:opacity-100" :aria-label="`移除 ${t}`" @click="remove(t)">×</button>
        </span>
      </div>
      <Input v-model="query" :placeholder="single ? `搜索 ${kind} 标签,选一个` : `搜索 ${kind} 标签,回车添加`" :invalid="invalid" autofocus @keydown.enter.prevent="onEnter" />
      <div class="max-h-56 overflow-y-auto rounded-md border border-border bg-surface">
        <div v-if="q.isPending.value" class="flex items-center gap-2 p-3 text-xs text-fg-muted"><Spinner class="h-3.5 w-3.5" /> 读取标签列表…</div>
        <div v-else-if="!filtered.length" class="p-3 text-xs text-fg-muted">没有匹配的标签</div>
        <ul v-else class="divide-y divide-border/60">
          <li v-for="t in filtered" :key="t">
            <label class="flex cursor-pointer items-center gap-2 px-2.5 py-1.5 text-sm hover:bg-surface-2">
              <input :type="single ? 'radio' : 'checkbox'" class="accent-accent" :checked="selected.includes(t)" @change="toggle(t)">
              <span class="font-mono text-xs">{{ t }}</span>
            </label>
          </li>
        </ul>
      </div>
      <p class="flex flex-wrap items-center gap-x-3 text-xs text-fg-muted">
        <span>{{ tags.length }} 个标签{{ q.data.value?.version ? ` · GeoData ${q.data.value.version}` : '' }}{{ filtered.length === 300 ? ' · 只显示前 300 个,请继续输入缩小范围' : '' }}</span>
        <span v-if="unknown.length" class="text-warn">列表中没有:{{ unknown.join(', ') }}</span>
        <button type="button" class="underline" @click="manual = true">手工输入</button>
      </p>
    </template>
  </div>
</template>
