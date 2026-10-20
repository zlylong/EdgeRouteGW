<script setup lang="ts">
// Confirmation with an optional preview slot (plans, affected items). The
// confirm button disables while the action runs and the dialog closes only on
// success, so a failure keeps the context on screen.
import { ref } from 'vue'
import Dialog from './Dialog.vue'
import Button from './Button.vue'
import { useToast } from '@/stores/toast'
import { ApiError } from '@/api/client'

const props = withDefaults(defineProps<{
  title: string
  description?: string
  confirmText?: string
  danger?: boolean
  action: () => Promise<unknown>
}>(), { confirmText: '确认' })
const open = defineModel<boolean>('open', { default: false })
const busy = ref(false)
const toast = useToast()
const emit = defineEmits<{ done: [] }>()

async function run() {
  busy.value = true
  try {
    await props.action()
    open.value = false
    emit('done')
  } catch (e) {
    toast.error('操作失败', e instanceof ApiError ? e.message : String(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open" :title="title" :description="description" size="sm">
    <slot />
    <template #footer>
      <Button variant="secondary" :disabled="busy" @click="open = false">取消</Button>
      <Button :variant="danger ? 'danger' : 'primary'" :loading="busy" @click="run">{{ confirmText }}</Button>
    </template>
  </Dialog>
</template>
