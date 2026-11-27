<script setup lang="ts">
import { storeToRefs } from 'pinia'

import Icon from './Icon.vue'
import { useToastStore } from '@/stores/toast'
import type { ToastTone } from '@/stores/toast'

const toast = useToastStore()
const { items } = storeToRefs(toast)

const styles: Record<
  ToastTone,
  { border: string; badge: string; icon: 'checkCircle' | 'alertCircle' | 'info' | 'alert' }
> = {
  success: {
    border: 'border-success/35',
    badge: 'bg-success/12 text-success',
    icon: 'checkCircle',
  },
  error: {
    border: 'border-danger/40',
    badge: 'bg-danger/12 text-danger',
    icon: 'alertCircle',
  },
  info: {
    border: 'border-info/35',
    badge: 'bg-info/12 text-info',
    icon: 'info',
  },
  warning: {
    border: 'border-warning/40',
    badge: 'bg-warning/12 text-warning',
    icon: 'alert',
  },
}
</script>

<template>
  <Teleport to="body">
    <div
      class="pointer-events-none fixed right-3.5 top-3.5 z-[100] flex w-[min(92vw,23rem)] flex-col gap-2"
      role="status"
      aria-live="polite"
    >
      <TransitionGroup
        enter-active-class="transition duration-150 ease-out"
        enter-from-class="translate-y-1.5 opacity-0 scale-[0.98]"
        leave-active-class="transition duration-150 ease-in"
        leave-to-class="translate-x-3 opacity-0"
      >
        <div
          v-for="item in items"
          :key="item.id"
          class="pointer-events-auto flex items-start gap-2.5 rounded-panel border bg-surface/95 px-3 py-2.5 shadow-pop backdrop-blur-md"
          :class="styles[item.tone].border"
        >
          <span
            class="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-md"
            :class="styles[item.tone].badge"
          >
            <Icon :name="styles[item.tone].icon" :size="13" />
          </span>
          <div class="min-w-0 flex-1">
            <p class="text-[13px] font-medium leading-4.5 text-fg">{{ item.title }}</p>
            <p
              v-if="item.description"
              class="mt-0.5 break-words text-2xs leading-relaxed text-muted"
            >
              {{ item.description }}
            </p>
          </div>
          <button
            type="button"
            class="-mr-0.5 -mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded text-subtle transition hover:bg-raised hover:text-fg"
            aria-label="关闭提示"
            @click="toast.dismiss(item.id)"
          >
            <Icon name="close" :size="13" />
          </button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>
