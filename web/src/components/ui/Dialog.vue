<script setup lang="ts">
import { DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import { X } from 'lucide-vue-next'
import { cn } from '@/lib/utils'
const props = defineProps<{ title: string; description?: string; size?: 'sm' | 'md' | 'lg' | 'xl' }>()
const open = defineModel<boolean>('open', { default: false })
const widths = { sm: 'max-w-sm', md: 'max-w-lg', lg: 'max-w-2xl', xl: 'max-w-4xl' }

// reka-ui focuses the first tabbable element (often the close button) when
// the dialog opens, which beats the browser's own autofocus. Honour an
// explicit [autofocus] inside the content instead.
function onOpenAutoFocus(e: Event) {
  const root = e.currentTarget as HTMLElement | null
  const target = root?.querySelector<HTMLElement>('[autofocus]')
  if (target) { e.preventDefault(); target.focus() }
}
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-40 bg-black/50 backdrop-blur-[1px] data-[state=open]:animate-in data-[state=open]:fade-in-0" />
      <DialogContent
        :class="cn('fixed left-1/2 top-1/2 z-50 w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-surface p-5 shadow-xl focus:outline-none max-h-[90vh] overflow-y-auto scroll-thin', widths[props.size ?? 'md'])"
        @open-auto-focus="onOpenAutoFocus"
      >
        <div class="mb-4 flex items-start justify-between gap-4">
          <div>
            <DialogTitle class="text-base font-semibold">{{ title }}</DialogTitle>
            <DialogDescription v-if="description" class="mt-1 text-sm text-fg-muted">{{ description }}</DialogDescription>
          </div>
          <DialogClose class="rounded-md p-1 text-fg-muted hover:bg-surface-2 hover:text-fg" aria-label="关闭"><X class="h-4 w-4" /></DialogClose>
        </div>
        <slot />
        <div v-if="$slots.footer" class="mt-5 flex justify-end gap-2"><slot name="footer" /></div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
