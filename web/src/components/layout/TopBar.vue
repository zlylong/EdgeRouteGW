<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LogOut, Menu, Moon, Sun } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import StatusDot from '@/components/ui/StatusDot.vue'
import Button from '@/components/ui/Button.vue'
import ChangeStatus from './ChangeStatus.vue'
import { useGatewayState } from '@/stores/state'
import { useAuth } from '@/stores/auth'

defineEmits<{ menu: [] }>()
const route = useRoute()
const router = useRouter()
const gw = useGatewayState()
const auth = useAuth()

const modeLabel: Record<string, string> = { A: 'Mode A · TPROXY 网关', B: 'Mode B · Fake-IP + OSPF', C: 'Mode C · 真实 IP OSPF' }
const compLabel: Record<string, string> = { xray: 'Xray', mosdns: 'Mosdns', nftables: 'nftables', frr: 'FRR' }
const title = computed(() => (route.meta.title as string) ?? '')
const dark = computed(() => document.documentElement.classList.contains('dark'))

function toggleTheme() {
  const next = !document.documentElement.classList.contains('dark')
  document.documentElement.classList.toggle('dark', next)
  localStorage.setItem('ergw.theme', next ? 'dark' : 'light')
}
async function logout() {
  await auth.logout()
  router.push({ name: 'login' })
}
</script>

<template>
  <header class="flex h-14 items-center gap-3 border-b border-border bg-surface px-4 sm:px-6">
    <Button variant="ghost" size="icon" class="lg:hidden" aria-label="菜单" @click="$emit('menu')"><Menu class="h-4 w-4" /></Button>
    <h1 class="text-base font-semibold">{{ title }}</h1>

    <div class="ml-auto flex items-center gap-3">
      <Badge v-if="gw.state" tone="accent" class="hidden sm:inline-flex">{{ modeLabel[gw.state.mode] ?? gw.state.mode }}</Badge>
      <div class="hidden items-center gap-3 md:flex" aria-label="组件状态">
        <span v-for="c in gw.components" :key="c.name" class="flex items-center gap-1.5 text-xs text-fg-muted" :title="c.last_error || c.status">
          <StatusDot :status="c.status" /> {{ compLabel[c.name] ?? c.name }}
        </span>
      </div>
      <ChangeStatus />
      <Button variant="ghost" size="icon" :aria-label="dark ? '切换到浅色' : '切换到深色'" @click="toggleTheme">
        <Sun v-if="dark" class="h-4 w-4" /><Moon v-else class="h-4 w-4" />
      </Button>
      <Button variant="ghost" size="icon" aria-label="退出登录" @click="logout"><LogOut class="h-4 w-4" /></Button>
    </div>
  </header>
</template>
