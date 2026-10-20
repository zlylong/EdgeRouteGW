<script setup lang="ts">
// Plain table shell with sticky header; rows are provided by the caller.
defineProps<{ cols: { key: string; label: string; class?: string }[]; dense?: boolean }>()
</script>

<template>
  <div class="overflow-x-auto rounded-lg border border-border">
    <table class="w-full text-left text-sm">
      <thead class="sticky top-0 bg-surface-2 text-xs uppercase tracking-wide text-fg-muted">
        <tr>
          <th v-for="c in cols" :key="c.key" :class="['whitespace-nowrap px-3 font-medium', dense ? 'py-1.5' : 'py-2', c.class]">{{ c.label }}</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border bg-surface"><slot /></tbody>
    </table>
  </div>
</template>
