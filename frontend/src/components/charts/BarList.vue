<script setup lang="ts">
import { computed } from 'vue'

import type { BarItem } from './types'

const props = withDefaults(
  defineProps<{
    items: BarItem[]
    tone?: 'accent' | 'success' | 'warning' | 'info'
    formatValue?: (value: number) => string
    emptyText?: string
    /** 固定分母。不传时按最大值归一，让最长的一条占满。 */
    max?: number
  }>(),
  {
    tone: 'accent',
    formatValue: (value: number) => String(value),
    emptyText: '暂无数据',
    max: 0,
  },
)

const toneClass: Record<string, string> = {
  accent: 'bg-accent/75',
  success: 'bg-success/75',
  warning: 'bg-warning/75',
  info: 'bg-info/75',
}

function safeVal(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : 0
}

const denominator = computed(() => {
  if (props.max > 0) return props.max
  return Math.max(...props.items.map((item) => safeVal(item.value)), 0)
})

function barWidth(value: number): string {
  const clean = safeVal(value)
  const total = denominator.value
  if (total <= 0 || clean <= 0) return '0%'
  const ratio = Math.min(Math.max(clean / total, 0), 1)
  return `${Math.max(ratio * 100, 2)}%`
}
</script>

<template>
  <div
    v-if="items.length === 0"
    class="rounded-md border border-dashed border-line bg-raised/30 py-6 text-center text-2xs text-subtle"
  >
    {{ emptyText }}
  </div>

  <ul v-else class="space-y-2.5">
    <li v-for="item in items" :key="item.key">
      <div class="flex items-baseline justify-between gap-3">
        <span class="truncate font-mono text-[12px] text-fg" :title="item.label">
          {{ item.label }}
        </span>
        <span class="tnum shrink-0 text-[12px] text-muted">
          {{ formatValue(safeVal(item.value)) }}
          <span v-if="item.secondary" class="ml-1.5 text-2xs text-subtle">{{ item.secondary }}</span>
        </span>
      </div>
      <div class="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-raised">
        <div
          class="h-full rounded-full transition-[width] duration-200"
          :class="toneClass[tone]"
          :style="{ width: barWidth(item.value) }"
        />
      </div>
    </li>
  </ul>
</template>
