<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import AppShell from '@/components/layout/AppShell.vue'
import Toaster from '@/components/ui/Toaster.vue'
import { useAuth } from '@/stores/auth'
import { useGatewayState } from '@/stores/state'

const route = useRoute()
const auth = useAuth()
const gw = useGatewayState()
const bare = computed(() => !!route.meta.bare)

// Open the live stream while logged in; close it on logout.
watch(() => auth.me?.authenticated, (authed) => {
  if (authed) { gw.load().catch(() => {}); gw.connect() } else gw.disconnect()
}, { immediate: true })
</script>

<template>
  <RouterView v-if="bare" />
  <AppShell v-else />
  <Toaster />
</template>
