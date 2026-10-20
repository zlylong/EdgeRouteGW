import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { VueQueryPlugin, QueryClient } from '@tanstack/vue-query'
import App from './App.vue'
import { router } from './router'
import './style.css'

// Theme: follow the system unless the user picked one.
const saved = localStorage.getItem('ergw.theme')
const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
document.documentElement.classList.toggle('dark', saved ? saved === 'dark' : prefersDark)

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 5_000, retry: 1, refetchOnWindowFocus: false } },
})

createApp(App)
  .use(createPinia())
  .use(router)
  .use(VueQueryPlugin, { queryClient })
  .mount('#app')
