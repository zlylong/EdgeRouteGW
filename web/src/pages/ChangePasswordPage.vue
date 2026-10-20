<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Field from '@/components/ui/Field.vue'
import { auth as authApi } from '@/api/endpoints'
import { useAuth } from '@/stores/auth'
import { useToast } from '@/stores/toast'
import { ApiError } from '@/api/client'

const auth = useAuth()
const router = useRouter()
const toast = useToast()
const forced = computed(() => !!auth.me?.must_change_password)
const oldPw = ref(''); const newPw = ref(''); const again = ref('')
const errors = ref<{ old?: string; new?: string }>({})
const busy = ref(false)

async function submit() {
  errors.value = {}
  if (newPw.value.length < 10) { errors.value.new = '至少 10 个字符'; return }
  if (newPw.value !== again.value) { errors.value.new = '两次输入不一致'; return }
  busy.value = true
  try {
    await authApi.changePassword(oldPw.value, newPw.value)
    await auth.refresh()
    toast.ok('密码已更新', '其它登录会话已被注销')
    router.replace('/')
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) errors.value.old = '当前密码错误'
    else if (e instanceof ApiError) errors.value.new = e.message
    else errors.value.new = String(e)
  } finally { busy.value = false }
}
</script>

<template>
  <div class="grid min-h-full place-items-center bg-bg px-4">
    <form class="w-full max-w-sm rounded-xl border border-border bg-surface p-7 shadow-lg" @submit.prevent="submit">
      <h1 class="text-lg font-semibold">{{ forced ? '请设置新密码' : '修改密码' }}</h1>
      <p class="mt-1 mb-5 text-xs text-fg-muted">
        {{ forced ? '你正在使用系统生成的初始密码。继续之前必须更换,更换后初始密码文件会被删除。' : '更换后其它设备上的登录会话会被注销。' }}
      </p>
      <div class="space-y-4">
        <Field label="当前密码" :error="errors.old"><Input v-model="oldPw" type="password" autocomplete="current-password" /></Field>
        <Field label="新密码" hint="至少 10 个字符" :error="errors.new"><Input v-model="newPw" type="password" autocomplete="new-password" /></Field>
        <Field label="再次输入新密码"><Input v-model="again" type="password" autocomplete="new-password" /></Field>
      </div>
      <div class="mt-6 flex gap-2">
        <Button v-if="!forced" variant="secondary" class="flex-1" @click="router.back()">取消</Button>
        <Button type="submit" class="flex-1" :loading="busy" :disabled="!oldPw || !newPw">保存</Button>
      </div>
    </form>
  </div>
</template>
