<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useSlots, watch } from 'vue'

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

const slots = useSlots()
const toast = useToastStore()
const copied = ref(false)
const localWrap = ref(props.wrap)
let copyTimer: ReturnType<typeof setTimeout> | null = null

watch(
  () => props.wrap,
  (val) => {
    localWrap.value = val
  },
)

const hasLongContent = computed(() => Boolean(props.code) && props.code.length > 90)
const showHeader = computed(
  () =>
    Boolean(props.title) ||
    Boolean(props.language) ||
    Boolean(slots.actions) ||
    (props.copyable && Boolean(props.code)),
)

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

onBeforeUnmount(() => {
  if (copyTimer) clearTimeout(copyTimer)
})
</script>

<template>
  <div class="min-w-0 max-w-full overflow-hidden rounded-md border border-line bg-canvas">
    <div
      v-if="showHeader"
      class="flex flex-wrap items-center justify-between gap-2 border-b border-line bg-raised/75 px-3 py-1.5"
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

      <div class="ml-auto flex shrink-0 items-center gap-1.5">
        <slot name="actions" />
        <button
          v-if="hasLongContent"
          type="button"
          class="inline-flex items-center gap-1 rounded border border-transparent px-1.5 py-0.5 text-2xs text-subtle transition hover:bg-overlay hover:text-fg"
          :title="localWrap ? '切换为单行横向滚动' : '切换为自动折行'"
          @click="localWrap = !localWrap"
        >
          <Icon name="wrapText" :size="11" />
          <span>{{ localWrap ? '折行' : '横滚' }}</span>
        </button>
        <button
          v-if="copyable && code"
          type="button"
          class="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-2xs font-medium transition"
          :class="
            copied
              ? 'border-success/35 bg-success/12 text-success'
              : 'border-line bg-surface text-muted hover:border-line-strong hover:text-fg'
          "
          @click="handleCopy"
        >
          <Icon :name="copied ? 'check' : 'copy'" :size="12" />
          <span>{{ copied ? '已复制' : '复制' }}</span>
        </button>
      </div>
    </div>

    <pre
      class="scroll-thin w-full overflow-auto px-3 py-2.5 font-mono text-xs leading-relaxed text-fg"
      :class="localWrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'"
      :style="{ maxHeight }"
    ><code v-if="code">{{ code }}</code><span v-else class="font-sans text-subtle">{{ emptyText }}</span></pre>
  </div>
</template>
