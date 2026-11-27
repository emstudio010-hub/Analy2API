<script lang="ts">
// 模块级共享滚动锁与弹窗栈：确保多层 Modal / ConfirmDialog 叠加时
// 1) 关闭顶层弹窗不会提前释放底层弹窗的 body 滚动锁；
// 2) 按下 Escape 仅关闭最顶层弹窗，不会把底层表单弹窗一并误关；
// 3) 动态分配递增 z-index，不受 Vue Teleport 模板声明锚点顺序影响。
let sharedLockCount = 0
let nextZIndex = 50
let savedBodyOverflow = ''
let savedBodyPaddingRight = ''
const openModalStack: symbol[] = []

function acquireBodyLock(token: symbol): number {
  if (sharedLockCount === 0) {
    nextZIndex = 50
  }
  if (!openModalStack.includes(token)) {
    openModalStack.push(token)
  }
  if (typeof document !== 'undefined') {
    if (sharedLockCount === 0) {
      savedBodyOverflow = document.body.style.overflow
      savedBodyPaddingRight = document.body.style.paddingRight
      const scrollbarWidth = window.innerWidth - document.documentElement.clientWidth
      if (scrollbarWidth > 0) {
        document.body.style.paddingRight = `${scrollbarWidth}px`
      }
      document.body.style.overflow = 'hidden'
    }
  }
  sharedLockCount += 1
  nextZIndex += 2
  return nextZIndex
}

function releaseBodyLock(token: symbol): void {
  const idx = openModalStack.indexOf(token)
  if (idx >= 0) {
    openModalStack.splice(idx, 1)
  }
  sharedLockCount = Math.max(0, sharedLockCount - 1)
  if (sharedLockCount === 0) {
    nextZIndex = 50
    if (typeof document !== 'undefined') {
      document.body.style.overflow = savedBodyOverflow
      document.body.style.paddingRight = savedBodyPaddingRight
    }
  }
}

function isTopModal(token: symbol): boolean {
  return openModalStack.length > 0 && openModalStack[openModalStack.length - 1] === token
}
</script>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title?: string
    subtitle?: string
    size?: 'sm' | 'md' | 'lg' | 'xl' | '2xl'
    closeOnBackdrop?: boolean
    /** 破坏性操作进行中时禁止随手关掉。 */
    persistent?: boolean
    /** 自定义内容容器间距，默认 px-4 py-4。 */
    bodyClass?: string
  }>(),
  {
    title: '',
    subtitle: '',
    size: 'md',
    closeOnBackdrop: true,
    persistent: false,
    bodyClass: 'px-4 py-4',
  },
)

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'close'): void
}>()

const instanceToken = Symbol('modal')
const panel = ref<HTMLElement | null>(null)
const modalZIndex = ref(50)
let lockedBySelf = false

const widths = {
  sm: 'max-w-sm',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
  xl: 'max-w-4xl',
  '2xl': 'max-w-5xl',
} as const

const panelClass = computed(() => widths[props.size])

function lock(): void {
  if (lockedBySelf) return
  lockedBySelf = true
  modalZIndex.value = acquireBodyLock(instanceToken)
}

function unlock(): void {
  if (!lockedBySelf) return
  lockedBySelf = false
  releaseBodyLock(instanceToken)
}

function close(): void {
  if (props.persistent) return
  emit('update:open', false)
  emit('close')
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && isTopModal(instanceToken)) {
    event.preventDefault()
    event.stopPropagation()
    close()
  }
}

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      lock()
      if (typeof document !== 'undefined') {
        document.addEventListener('keydown', onKeydown)
      }
      await nextTick()
      const focusable = panel.value?.querySelector<HTMLElement>(
        'input:not([type="hidden"]):not([disabled]), select:not([disabled]), textarea:not([disabled]), button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
      )
      ;(focusable ?? panel.value)?.focus()
    } else {
      if (typeof document !== 'undefined') {
        document.removeEventListener('keydown', onKeydown)
      }
      unlock()
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  if (typeof document !== 'undefined') {
    document.removeEventListener('keydown', onKeydown)
  }
  unlock()
})
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div
        v-if="open"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-3 backdrop-blur-[2px] sm:p-5"
        :style="{ zIndex: modalZIndex }"
        role="presentation"
        @mousedown.self="closeOnBackdrop && close()"
      >
        <Transition name="pop" appear>
          <div
            ref="panel"
            class="relative flex max-h-[calc(100vh-1.5rem)] w-full flex-col overflow-hidden rounded-panel border border-line bg-surface shadow-pop outline-none sm:max-h-[calc(100vh-3rem)]"
            :class="panelClass"
            role="dialog"
            aria-modal="true"
            :aria-label="title || undefined"
            tabindex="-1"
          >
            <header
              v-if="title || $slots.header || !persistent"
              class="flex shrink-0 items-start justify-between gap-4 border-b border-line bg-surface px-4 py-3"
            >
              <div class="min-w-0 flex-1">
                <slot name="header">
                  <h2 class="truncate text-sm font-semibold text-fg">{{ title }}</h2>
                  <p v-if="subtitle" class="mt-0.5 text-2xs leading-relaxed text-subtle">
                    {{ subtitle }}
                  </p>
                </slot>
              </div>
              <button
                v-if="!persistent"
                type="button"
                class="-mr-1 -mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-subtle transition hover:bg-raised hover:text-fg"
                aria-label="关闭"
                @click="close"
              >
                <Icon name="close" :size="15" />
              </button>
            </header>

            <div class="scroll-thin min-h-0 flex-1 overflow-y-auto" :class="bodyClass">
              <slot />
            </div>

            <footer
              v-if="$slots.footer"
              class="flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-line bg-raised/50 px-4 py-3"
            >
              <slot name="footer" />
            </footer>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>
