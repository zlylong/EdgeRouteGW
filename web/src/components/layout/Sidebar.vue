<script setup lang="ts">
import { Activity, Gauge, Globe, LayoutDashboard, Network, Route, Settings2, Waypoints } from 'lucide-vue-next'
import { useGatewayState } from '@/stores/state'

defineProps<{ open: boolean }>()
defineEmits<{ navigate: [] }>()
const gw = useGatewayState()

const items = [
  { to: '/', label: '总览', icon: LayoutDashboard },
  { to: '/nodes', label: '节点', icon: Network },
  { to: '/routing', label: '分流', icon: Route },
  { to: '/dns', label: 'DNS', icon: Globe },
  { to: '/network', label: '网络与模式', icon: Waypoints },
  { to: '/observe', label: '观测', icon: Activity },
  { to: '/system', label: '系统', icon: Settings2 },
]
</script>

<template>
  <aside
    :class="['fixed inset-y-0 left-0 z-40 flex w-60 flex-col border-r border-border bg-surface transition-transform lg:static lg:translate-x-0', open ? 'translate-x-0' : '-translate-x-full']"
  >
    <div class="flex h-14 items-center gap-2 border-b border-border px-5">
      <Gauge class="h-5 w-5 text-accent" />
      <span class="text-base font-semibold tracking-tight">EdgeRouteGW</span>
      <span class="ml-auto rounded bg-surface-2 px-1.5 py-0.5 text-[10px] text-fg-muted">2.0</span>
    </div>
    <nav class="flex-1 space-y-1 p-3">
      <RouterLink
        v-for="it in items" :key="it.to" :to="it.to"
        class="flex items-center gap-3 rounded-md px-3 py-2 text-sm text-fg-muted transition-colors hover:bg-surface-2 hover:text-fg"
        active-class="!bg-accent-soft !text-accent font-medium"
        :exact-active-class="it.to === '/' ? '!bg-accent-soft !text-accent font-medium' : undefined"
        @click="$emit('navigate')"
      >
        <component :is="it.icon" class="h-4 w-4" />
        {{ it.label }}
      </RouterLink>
    </nav>
    <div class="border-t border-border p-4 text-[11px] leading-5 text-fg-subtle">
      <div>版本 {{ gw.state?.version.version ?? '-' }}</div>
      <div class="truncate" :title="gw.state?.version.commit">commit {{ gw.state?.version.commit?.slice(0, 7) ?? '-' }}</div>
      <a class="hover:text-fg" href="https://github.com/zlylong/EdgeRouteGW" target="_blank" rel="noreferrer">GitHub</a>
    </div>
  </aside>
</template>
