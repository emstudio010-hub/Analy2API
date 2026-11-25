<script setup lang="ts">
import { ref } from 'vue'

import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'

import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{
    code: string
    title?: string
    language?: string
    maxHeight?: string
    wrap?: boolean
    copyable?: boolean
    emptyText?: string
  }>(),
  {
    title: '',
    language: '',
    maxHeight: '20rem',
    wrap: true,
    copyable: true,
    emptyText: '暂无内容',
  },
)

const toast = useToastStore()
const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | null = null

async function handleCopy(): Promise<void> {
  if (!props.code) return
  const ok = await copyText(props.code)
  if (!ok) {
    toast.error('复制失败，请手动选中复制')
    return
  }
  copied.value = true
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => {
    copied.value = false
  }, 1800)
}
</script>

<template>
  <div class="overflow-hidden rounded-md border border-line bg-canvas">
    <div
      v-if="title || language || copyable || $slots.actions"
      class="flex items-center justify-between gap-2 border-b border-line bg-raised/70 px-3 py-1.5"
    >
      <div class="flex min-w-0 items-center gap-2">
        <span v-if="title" class="truncate text-2xs font-medium text-muted">{{ title }}</span>
        <span
          v-if="language"
          class="rounded border border-line bg-surface px-1.5 py-px font-mono text-2xs uppercase text-subtle"
        >
          {{ language }}
        </span>
      </div>

      <div class="flex shrink-0 items-center gap-1.5">
        <slot name="actions" />
        <button
          v-if="copyable && code"
          type="button"
          class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-2xs text-muted transition hover:bg-overlay hover:text-fg"
          @click="handleCopy"
        >
          <Icon :name="copied ? 'check' : 'copy'" :size="12" :class="copied ? 'text-success' : ''" />
          <span>{{ copied ? '已复制' : '复制' }}</span>
        </button>
      </div>
    </div>

    <pre
      class="scroll-thin overflow-auto px-3 py-2.5 font-mono text-xs leading-relaxed text-fg"
      :class="wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'"
      :style="{ maxHeight }"
    ><code v-if="code">{{ code }}</code><span v-else class="font-sans text-subtle">{{ emptyText }}</span></pre>
  </div>
</template>
