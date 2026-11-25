<script setup lang="ts">
import Icon from './Icon.vue'
import type { IconName } from './icons'

type Tone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info'

withDefaults(
  defineProps<{
    label: string
    value: string | number
    hint?: string
    icon?: IconName
    tone?: Tone
    mono?: boolean
  }>(),
  { hint: '', tone: 'neutral', mono: false },
)

const dotTones: Record<Tone, string> = {
  neutral: 'bg-subtle',
  accent: 'bg-accent',
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
  info: 'bg-info',
}

const iconTones: Record<Tone, string> = {
  neutral: 'text-subtle bg-raised',
  accent: 'text-accent bg-accent/10',
  success: 'text-success bg-success/10',
  warning: 'text-warning bg-warning/10',
  danger: 'text-danger bg-danger/10',
  info: 'text-info bg-info/10',
}
</script>

<template>
  <div class="card flex items-start justify-between gap-3 px-3.5 py-3">
    <div class="min-w-0 flex-1">
      <div class="flex items-center gap-1.5 text-2xs font-medium text-muted">
        <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="dotTones[tone]" />
        <span class="truncate">{{ label }}</span>
      </div>
      <p
        class="tnum mt-1.5 truncate text-lg font-semibold leading-6 text-fg"
        :class="mono ? 'font-mono text-base' : ''"
      >
        {{ value }}
      </p>
      <p v-if="hint || $slots.hint" class="mt-0.5 truncate text-2xs text-subtle">
        <slot name="hint">{{ hint }}</slot>
      </p>
    </div>

    <span
      v-if="icon"
      class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-line/70"
      :class="iconTones[tone]"
    >
      <Icon :name="icon" :size="15" />
    </span>
  </div>
</template>
