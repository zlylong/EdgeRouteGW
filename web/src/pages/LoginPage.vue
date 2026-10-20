<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Gauge } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import { useAuth } from '@/stores/auth'
import { ApiError } from '@/api/client'

const auth = useAuth()
const router = useRouter()
const route = useRoute()
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    const me = await auth.login(password.value)
    if (me.must_change_password) router.replace({ name: 'change-password' })
    else router.replace(typeof route.query.next === 'string' ? route.query.next : '/')
  } catch (e) {
    if (e instanceof ApiError) {
      error.value = e.status === 401 ? '密码错误' : e.status === 429 ? '失败次数过多,请 15 分钟后再试' : e.message
    } else error.value = String(e)
  } finally { busy.value = false }
}
</script>

<template>
  <div class="grid min-h-full place-items-center bg-bg px-4">
    <form class="w-full max-w-sm rounded-xl border border-border bg-surface p-7 shadow-lg" @submit.prevent="submit">
      <div class="mb-6 flex items-center gap-2">
        <Gauge class="h-6 w-6 text-accent" />
        <div>
          <h1 class="text-lg font-semibold leading-tight">EdgeRouteGW</h1>
          <p class="text-xs text-fg-muted">透明代理网关 · 管理控制台</p>
        </div>
      </div>
      <label class="mb-1 block text-xs font-medium text-fg-muted" for="pw">管理员密码</label>
      <Input id="pw" v-model="password" type="password" autocomplete="current-password" autofocus :invalid="!!error" placeholder="首次登录请查看 bootstrap_password.txt" />
      <p v-if="error" class="mt-2 text-xs text-danger">{{ error }}</p>
      <Button type="submit" class="mt-5 w-full" :loading="busy" :disabled="!password">登录</Button>
      <p class="mt-4 text-center text-[11px] text-fg-subtle">初始密码位于网关的 <code class="rounded bg-surface-2 px-1">/var/lib/edgeroutegw/bootstrap_password.txt</code></p>
    </form>
  </div>
</template>
