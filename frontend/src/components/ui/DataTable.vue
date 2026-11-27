<script setup lang="ts">
import { computed } from 'vue'

import EmptyState from './EmptyState.vue'
import Skeleton from './Skeleton.vue'

// 表格外壳：统一边框、横向滚动、骨架加载态、后台刷新进度条与空态兜底。
const props = withDefaults(
  defineProps<{
    columns: number
    loading?: boolean
    /** 保留已有数据行并展示顶部轻量刷新条。 */
    refreshing?: boolean
    empty?: boolean
    emptyTitle?: string
    emptyDescription?: string
    skeletonRows?: number
    /** 行悬停高亮：行可点击或高密度审阅时打开。 */
    hoverable?: boolean
    /** 单元格最小宽度，避免中文列在窄屏被压成多行。 */
    minWidth?: string
    /** 紧凑行距模式。 */
    compact?: boolean
  }>(),
  {
    loading: false,
    refreshing: false,
    empty: false,
    emptyTitle: '暂无记录',
    emptyDescription: '当前查询条件下没有可展示的数据。',
    skeletonRows: 6,
    hoverable: false,
    minWidth: '100%',
    compact: false,
  },
)

const tableStyle = computed(() => ({ minWidth: props.minWidth }))

function skeletonWidthClass(col: number): string {
  if (col === 1) return 'w-32'
  if (col % 3 === 0) return 'w-24'
  if (col % 2 === 0) return 'w-16'
  return 'w-14'
}
</script>

<template>
  <div class="relative overflow-hidden rounded-panel border border-line bg-surface">
    <!-- 平滑局部刷新指示条 -->
    <div
      v-if="refreshing && !loading"
      class="pointer-events-none absolute inset-x-0 top-0 z-10 h-0.5 overflow-hidden bg-accent/15"
      aria-hidden="true"
    >
      <div class="h-full w-2/5 rounded-full bg-accent animate-progress-bar" />
    </div>

    <div class="scroll-thin overflow-x-auto">
      <table
        class="w-full border-collapse text-left"
        :class="compact ? '[&_.td]:py-1.5 [&_.th]:py-1.5' : ''"
        :style="tableStyle"
      >
        <thead class="border-b border-line bg-raised/70">
          <tr>
            <slot name="head" />
          </tr>
        </thead>

        <tbody v-if="loading" class="divide-y divide-line" aria-busy="true">
          <tr v-for="row in skeletonRows" :key="row">
            <td v-for="col in columns" :key="col" class="px-3 py-2.5">
              <Skeleton class="h-3.5" :class="skeletonWidthClass(col)" />
            </td>
          </tr>
        </tbody>

        <tbody v-else-if="empty">
          <tr>
            <td :colspan="columns" class="px-4 py-10">
              <slot name="empty">
                <EmptyState :title="emptyTitle" :description="emptyDescription" />
              </slot>
            </td>
          </tr>
        </tbody>

        <tbody
          v-else
          class="divide-y divide-line"
          :class="
            hoverable
              ? '[&>tr]:transition-colors [&>tr]:duration-100 [&>tr:hover]:bg-raised/65'
              : ''
          "
        >
          <slot name="body" />
        </tbody>
      </table>
    </div>

    <slot name="footer" />
  </div>
</template>
