<script setup lang="ts">
import { ref } from 'vue'
import Sidebar from './Sidebar.vue'
import TopBar from './TopBar.vue'
import { useGatewayState } from '@/stores/state'

const gw = useGatewayState()
const sidebarOpen = ref(false)
</script>

<template>
  <div class="flex h-full">
    <!-- Mobile scrim -->
    <div v-if="sidebarOpen" class="fixed inset-0 z-30 bg-black/40 lg:hidden" @click="sidebarOpen = false" />
    <Sidebar :open="sidebarOpen" @navigate="sidebarOpen = false" />
    <div class="flex min-w-0 flex-1 flex-col">
      <TopBar @menu="sidebarOpen = !sidebarOpen" />
      <div v-if="gw.stream === 'retrying'" class="bg-danger px-4 py-1 text-center text-xs text-white">与网关的实时连接已中断,正在重试…</div>
      <main class="scroll-thin flex-1 overflow-y-auto px-4 py-5 sm:px-6">
        <RouterView v-slot="{ Component }">
          <Transition enter-active-class="transition duration-150" enter-from-class="opacity-0" mode="out-in">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </main>
    </div>
  </div>
</template>
