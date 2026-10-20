<script setup lang="ts">
import { computed } from 'vue'
import { cva, type VariantProps } from 'class-variance-authority'
import { Loader2 } from 'lucide-vue-next'
import { cn } from '@/lib/utils'

const button = cva(
  'inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60 disabled:pointer-events-none disabled:opacity-50',
  {
    variants: {
      variant: {
        primary: 'bg-accent text-accent-fg hover:bg-accent/90',
        secondary: 'bg-surface-2 text-fg hover:bg-border/70 border border-border',
        ghost: 'text-fg-muted hover:bg-surface-2 hover:text-fg',
        danger: 'bg-danger text-white hover:bg-danger/90',
        outline: 'border border-border bg-transparent hover:bg-surface-2',
      },
      size: {
        sm: 'h-8 px-2.5 text-xs',
        md: 'h-9 px-3.5',
        lg: 'h-10 px-5',
        icon: 'h-8 w-8 p-0',
      },
    },
    defaultVariants: { variant: 'primary', size: 'md' },
  },
)
type Variants = VariantProps<typeof button>

const props = withDefaults(defineProps<{
  variant?: Variants['variant']
  size?: Variants['size']
  loading?: boolean
  disabled?: boolean
  type?: 'button' | 'submit' | 'reset'
  class?: string
}>(), { type: 'button' })

const classes = computed(() => cn(button({ variant: props.variant, size: props.size }), props.class))
</script>

<template>
  <button :type="type" :class="classes" :disabled="disabled || loading">
    <Loader2 v-if="loading" class="h-4 w-4 animate-spin" aria-hidden="true" />
    <slot />
  </button>
</template>
