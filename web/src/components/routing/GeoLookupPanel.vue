<script setup lang="ts">
// GeoData query: a domain or IP -> the geosite/geoip tags containing it;
// a tag -> its contents. Reachable from the connections view by clicking a
// tag chip (?tab=geo&q=geoip:cn).
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import Input from '@/components/ui/Input.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { useQuery } from '@tanstack/vue-query'
import { geo, routing, system } from '@/api/endpoints'
import type { GeoEntries, GeoLookup, Rule, TraceResult } from '@/api/types'
import { useToast } from '@/stores/toast'

const route = useRoute()
const toast = useToast()
const q = ref('')
const busy = ref(false)
const lookup = ref<GeoLookup | null>(null)
const entries = ref<GeoEntries | null>(null)
const entryFilter = ref('')
const trace = ref<TraceResult | null>(null)

// Rules referencing geo tags, so a looked-up tag shows "already used by
// rule X" and the target shows which rule it currently hits.
const setsQ = useQuery({ queryKey: ['rule-sets'], queryFn: routing.sets })
const rulesQ = useQuery({ queryKey: ['rules'], queryFn: () => routing.rules() })
const setName = (id: string) => setsQ.data.value?.items.find(s => s.id === id)?.name ?? '?'
const rulesByTag = computed(() => {
  const m: Record<string, Rule[]> = {}
  for (const r of rulesQ.data.value?.items ?? []) {
    if (r.match_kind !== 'geosite' && r.match_kind !== 'geoip') continue
    const key = `${r.match_kind}:${r.match_value.toLowerCase().replace(/^!/, '')}`
    ;(m[key] ??= []).push(r)
  }
  return m
})
const refs = (kind: 'geosite' | 'geoip', tag: string) => rulesByTag.value[`${kind}:${tag.toLowerCase()}`] ?? []
const actionText = (r: Rule) => ({ direct: '直连', block: '拦截', proxy: '代理', node: '指定节点', group: '出站组' } as Record<string, string>)[r.action_kind] ?? r.action_kind
const traceText = computed(() => {
  const t = trace.value
  if (!t) return ''
  if (t.matched_rule) return `命中「${t.rule_set ?? ''}」里的 ${t.matched_rule.match_kind}:${t.matched_rule.match_value} → ${actionText(t.matched_rule)}${t.outbound ? `(${t.outbound})` : ''}`
  return `没有规则命中,按兜底处理 → ${t.action === 'direct' ? '直连' : t.outbound || t.action}`
})

const isTagQuery = (s: string) => /^(geosite|geoip):[a-z0-9._@!-]+$/i.test(s.trim())
async function run(input = q.value) {
  const s = input.trim()
  if (!s) return
  q.value = s
  busy.value = true
  try {
    if (isTagQuery(s)) {
      const [kind, tag] = s.toLowerCase().split(':', 2) as ['geosite' | 'geoip', string]
      lookup.value = null
      entries.value = await geo.entries(kind, tag, entryFilter.value)
    } else {
      entries.value = null
      const [lk, tr] = await Promise.all([geo.lookup(s), system.trace(s).catch(() => null)])
      lookup.value = lk
      trace.value = tr
    }
  } catch (e) { toast.error('查询失败', (e as Error).message) } finally { busy.value = false }
}
async function openTag(kind: 'geosite' | 'geoip', tag: string) { entryFilter.value = ''; await run(`${kind}:${tag}`) }
let filterTimer: ReturnType<typeof setTimeout> | undefined
watch(entryFilter, () => { if (!entries.value) return; clearTimeout(filterTimer); filterTimer = setTimeout(() => run(`${entries.value!.kind}:${entries.value!.tag}`), 300) })
watch(() => route.query.q, v => { if (typeof v === 'string' && v) run(v) }, { immediate: true })
const hint = computed(() => entries.value ? `${entries.value.total} 条${entries.value.items.length < entries.value.total ? `,只显示前 ${entries.value.items.length} 条,可用过滤框缩小` : ''}` : '')
</script>

<template>
  <div class="space-y-4">
    <form class="flex flex-wrap items-end gap-2" @submit.prevent="run()">
      <div class="w-full max-w-md">
        <Input v-model="q" class="font-mono" placeholder="域名、IP,或标签如 geosite:google / geoip:cn" autofocus />
      </div>
      <Button type="submit" :loading="busy">查询</Button>
      <span class="text-xs text-fg-subtle">查域名或 IP 得到所属标签;查标签看它包含什么。数据来自已安装的 GeoData。</span>
    </form>

    <div v-if="lookup" class="rounded-lg border border-border bg-surface p-4">
      <div class="mb-3 text-sm"><span class="text-fg-muted">{{ lookup.kind === 'ip' ? 'IP' : '域名' }}</span> <code class="font-mono">{{ lookup.target }}</code></div>
      <template v-if="lookup.kind === 'domain'">
        <div class="mb-1 text-xs font-semibold uppercase tracking-wide text-fg-muted">geosite</div>
        <div v-if="lookup.geosite.length" class="flex flex-wrap gap-1.5">
          <span v-for="t in lookup.geosite" :key="t" class="inline-flex items-center gap-1">
            <button type="button" class="rounded-md bg-accent/10 px-2 py-0.5 font-mono text-xs text-accent hover:bg-accent/20" @click="openTag('geosite', t)">{{ t }}</button>
            <span v-for="r in refs('geosite', t)" :key="r.id" class="rounded bg-ok/15 px-1 text-[10px] text-ok" :title="`规则集「${setName(r.set_id)}」· ${r.match_value} → ${actionText(r)}${r.enabled ? '' : '(已停用)'}`">已引用:{{ setName(r.set_id) }} → {{ actionText(r) }}</span>
          </span>
        </div>
        <p v-else class="text-xs text-fg-subtle">没有任何 geosite 标签包含它(或 GeoData 未安装)。</p>
      </template>
      <template v-else>
        <div class="mb-1 text-xs font-semibold uppercase tracking-wide text-fg-muted">geoip</div>
        <div v-if="lookup.geoip.length" class="flex flex-wrap gap-1.5">
          <span v-for="t in lookup.geoip" :key="t" class="inline-flex items-center gap-1">
            <button type="button" class="rounded-md bg-accent/10 px-2 py-0.5 font-mono text-xs text-accent hover:bg-accent/20" @click="openTag('geoip', t)">{{ t }}</button>
            <span v-for="r in refs('geoip', t)" :key="r.id" class="rounded bg-ok/15 px-1 text-[10px] text-ok" :title="`规则集「${setName(r.set_id)}」· ${r.match_value} → ${actionText(r)}${r.enabled ? '' : '(已停用)'}`">已引用:{{ setName(r.set_id) }} → {{ actionText(r) }}</span>
          </span>
        </div>
        <p v-else class="text-xs text-fg-subtle">没有任何 geoip 标签包含它(或 GeoData 未安装)。</p>
      </template>
      <div v-if="trace" class="mt-3 rounded-md bg-surface-2/60 px-3 py-2 text-xs" :class="trace.matched_rule ? 'text-fg' : 'text-fg-muted'">
        <span class="mr-1 rounded bg-surface-2 px-1 text-[10px] uppercase text-fg-muted">当前分流</span>{{ traceText }}
      </div>
      <p class="mt-3 text-xs text-fg-subtle">点标签查看内容;标签后的「已引用」表示已有规则用到它。写规则时在「匹配类型」里选对应类型并勾选这个标签即可。</p>
    </div>

    <div v-if="entries" class="rounded-lg border border-border bg-surface">
      <div class="flex flex-wrap items-center gap-3 border-b border-border px-4 py-2">
        <Badge tone="accent">{{ entries.kind }}:{{ entries.tag }}</Badge>
        <span class="text-xs text-fg-muted">{{ hint }}</span>
        <template v-if="refs(entries.kind, entries.tag).length">
          <span v-for="r in refs(entries.kind, entries.tag)" :key="r.id" class="rounded bg-ok/15 px-1.5 py-0.5 text-[10px] text-ok">已引用:「{{ setName(r.set_id) }}」{{ r.match_value }} → {{ actionText(r) }}{{ r.enabled ? '' : '(已停用)' }}</span>
        </template>
        <span v-else class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] text-fg-subtle">尚无规则引用这个标签</span>
        <Input v-model="entryFilter" class="ml-auto h-8 w-56 font-mono text-xs" placeholder="过滤内容" />
      </div>
      <div v-if="busy" class="p-4"><Spinner /></div>
      <ul v-else class="max-h-[32rem] overflow-y-auto divide-y divide-border/60 font-mono text-xs">
        <li v-for="it in entries.items" :key="it" class="px-4 py-1">{{ it }}</li>
        <li v-if="!entries.items.length" class="px-4 py-6 text-center text-fg-subtle">没有匹配的内容</li>
      </ul>
    </div>
  </div>
</template>
