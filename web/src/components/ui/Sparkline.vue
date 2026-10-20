<script setup lang="ts">
// Dependency-free SVG area chart for byte series; two stacked-looking lines
// (up/down) with a hover readout. Good enough for a dashboard glance.
import { computed, ref } from 'vue'
import type { TrafficPoint } from '@/api/types'
import { fmtBytes } from '@/lib/utils'

const props = withDefaults(defineProps<{ points: TrafficPoint[]; height?: number; bucket?: number }>(), { height: 80, bucket: 60 })
const W = 600
const hover = ref<number | null>(null)
const max = computed(() => Math.max(1, ...props.points.map(p => Math.max(p.up, p.down))))
const x = (i: number) => props.points.length < 2 ? W / 2 : (i / (props.points.length - 1)) * W
const y = (v: number) => props.height - 4 - (v / max.value) * (props.height - 8)
const path = (key: 'up' | 'down') => props.points.length ? props.points.map((p, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(p[key]).toFixed(1)}`).join(' ') : ''
const area = (key: 'up' | 'down') => props.points.length ? `${path(key)} L${W},${props.height} L0,${props.height} Z` : ''
function onMove(e: MouseEvent) {
  const rect = (e.currentTarget as SVGElement).getBoundingClientRect()
  const i = Math.round(((e.clientX - rect.left) / rect.width) * (props.points.length - 1))
  hover.value = Number.isFinite(i) && props.points.length ? Math.max(0, Math.min(props.points.length - 1, i)) : null
}
const hovered = computed(() => hover.value === null ? null : props.points[hover.value])
</script>

<template>
  <div class="relative">
    <svg :viewBox="`0 0 ${W} ${height}`" preserveAspectRatio="none" class="h-20 w-full" @mousemove="onMove" @mouseleave="hover = null">
      <path :d="area('down')" class="fill-accent/15" />
      <path :d="path('down')" class="fill-none stroke-accent" stroke-width="1.5" vector-effect="non-scaling-stroke" />
      <path :d="area('up')" class="fill-info/15" />
      <path :d="path('up')" class="fill-none stroke-info" stroke-width="1.5" vector-effect="non-scaling-stroke" />
      <line v-if="hover !== null" :x1="x(hover)" :x2="x(hover)" y1="0" :y2="height" class="stroke-fg-subtle" stroke-dasharray="3 3" vector-effect="non-scaling-stroke" />
    </svg>
    <div v-if="hovered" class="pointer-events-none absolute right-0 top-0 rounded bg-surface-2 px-2 py-0.5 text-[11px] text-fg-muted">
      {{ new Date(hovered.ts * 1000).toLocaleTimeString('zh-CN', { hour12: false }) }} · ↓{{ fmtBytes(hovered.down) }} ↑{{ fmtBytes(hovered.up) }}
    </div>
    <div v-else-if="!points.length" class="pointer-events-none absolute inset-0 grid place-items-center text-xs text-fg-subtle">暂无流量数据</div>
  </div>
</template>
