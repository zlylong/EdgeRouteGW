import { defineStore } from 'pinia'
import { ref } from 'vue'
import { auth as authApi } from '@/api/endpoints'
import { ApiError, setUnauthenticatedHandler } from '@/api/client'
import type { Me } from '@/api/types'

export const useAuth = defineStore('auth', () => {
  const me = ref<Me | null>(null)
  const loaded = ref(false)
  let loading: Promise<Me> | null = null

  async function refresh(): Promise<Me> {
    if (!loading) {
      loading = authApi.me().then(m => { me.value = m; loaded.value = true; return m }).finally(() => { loading = null })
    }
    return loading
  }
  async function login(password: string) {
    const m = await authApi.login(password)
    me.value = m
    loaded.value = true
    return m
  }
  async function logout() {
    try { await authApi.logout() } catch { /* already gone */ }
    me.value = { authenticated: false, must_change_password: false }
  }
  function forceLoggedOut() {
    if (me.value?.authenticated) me.value = { authenticated: false, must_change_password: false }
    loaded.value = true
  }
  setUnauthenticatedHandler(forceLoggedOut)

  return { me, loaded, refresh, login, logout, forceLoggedOut, ApiError }
})
