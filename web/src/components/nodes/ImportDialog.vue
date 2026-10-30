<script setup lang="ts">
import { ref, watch } from 'vue'
import Dialog from '@/components/ui/Dialog.vue'
import Button from '@/components/ui/Button.vue'
import Textarea from '@/components/ui/Textarea.vue'
import Badge from '@/components/ui/Badge.vue'
import { nodes as nodesApi } from '@/api/endpoints'
import type { ImportResult } from '@/api/types'
import { useToast } from '@/stores/toast'

const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ imported: [] }>()
const toast = useToast()
const text = ref('')
const preview = ref<ImportResult[] | null>(null)
const busy = ref(false)

watch(open, o => { if (o) { text.value = ''; preview.value = null } })
const links = () => text.value.split(/\r?\n/).map(s => s.trim()).filter(Boolean)

async function doPreview() {
  busy.value = true
  try { preview.value = (await nodesApi.import(links(), true)).results } catch (e) { toast.error('解析失败', (e as Error).message) } finally { busy.value = false }
}
async function doImport() {
  busy.value = true
  try {
    const res = await nodesApi.import(links(), false)
    toast.ok(`已导入 ${res.imported} 个节点`)
    open.value = false
    emit('imported')
  } catch (e) { toast.error('导入失败', (e as Error).message) } finally { busy.value = false }
}
</script>

<template>
  <Dialog v-model:open="open" title="导入分享链接" description="支持 vless:// vmess:// trojan:// ss:// wireguard:// hysteria2://,每行一条。先预览解析结果,确认后再入库。" size="lg">
    <Textarea v-model="text" rows="6" class="font-mono text-xs" placeholder="vless://uuid@host:443?security=reality&sni=...#名称" @input="preview = null" />
    <div v-if="preview" class="mt-4 space-y-1.5">
      <div v-for="(r, i) in preview" :key="i" class="flex items-center gap-2 rounded-md border border-border px-3 py-2 text-sm">
        <template v-if="r.node">
          <Badge tone="ok">可导入</Badge>
          <span class="font-medium">{{ r.node.name }}</span>
          <span class="text-fg-muted uppercase text-xs">{{ r.node.protocol }}</span>
          <span class="ml-auto truncate text-xs text-fg-subtle">{{ r.node.address }}:{{ r.node.port }}</span>
        </template>
        <template v-else>
          <Badge tone="danger">无法解析</Badge>
          <span class="truncate text-xs text-fg-muted">{{ r.link }}</span>
          <span class="ml-auto text-xs text-danger">{{ r.error }}</span>
        </template>
      </div>
    </div>
    <template #footer>
      <Button variant="secondary" @click="open = false">取消</Button>
      <Button v-if="!preview" variant="secondary" :loading="busy" :disabled="!links().length" @click="doPreview">预览</Button>
      <Button v-else :loading="busy" :disabled="!preview.some(r => r.node)" @click="doImport">导入 {{ preview.filter(r => r.node).length }} 个</Button>
    </template>
  </Dialog>
</template>
