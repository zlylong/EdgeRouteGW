import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from '@/stores/auth'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('@/pages/LoginPage.vue'), meta: { public: true, bare: true } },
    { path: '/change-password', name: 'change-password', component: () => import('@/pages/ChangePasswordPage.vue'), meta: { bare: true } },
    { path: '/', name: 'dashboard', component: () => import('@/pages/DashboardPage.vue'), meta: { title: '总览' } },
    { path: '/nodes', name: 'nodes', component: () => import('@/pages/NodesPage.vue'), meta: { title: '节点' } },
    { path: '/routing', name: 'routing', component: () => import('@/pages/RoutingPage.vue'), meta: { title: '分流' } },
    { path: '/dns', name: 'dns', component: () => import('@/pages/DnsPage.vue'), meta: { title: 'DNS' } },
    { path: '/network', name: 'network', component: () => import('@/pages/NetworkPage.vue'), meta: { title: '网络与模式' } },
    { path: '/observe', name: 'observe', component: () => import('@/pages/ObservePage.vue'), meta: { title: '观测' } },
    { path: '/system', name: 'system', component: () => import('@/pages/SystemPage.vue'), meta: { title: '系统' } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuth()
  if (!auth.loaded) {
    try { await auth.refresh() } catch { auth.forceLoggedOut() }
  }
  const authed = !!auth.me?.authenticated
  if (to.meta.public) {
    return authed ? { name: 'dashboard' } : true
  }
  if (!authed) return { name: 'login', query: to.fullPath !== '/' ? { next: to.fullPath } : {} }
  if (auth.me?.must_change_password && to.name !== 'change-password') return { name: 'change-password' }
  return true
})

router.afterEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} · EdgeRouteGW` : 'EdgeRouteGW'
})
