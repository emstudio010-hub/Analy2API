<script setup lang="ts">
import Icon from './Icon.vue'
import type { TabItem } from './types'

withDefaults(
  defineProps<{
    items: readonly TabItem[]
    size?: 'sm' | 'md'
    block?: boolean
  }>(),
  { size: 'sm', block: false },
)

const model = defineModel<string>({ default: '' })
</script>

<template>
  <div
    class="inline-flex items-center gap-0.5 rounded-md border border-line bg-raised p-0.5"
    :class="block ? 'flex w-full' : ''"
    role="tablist"
  >
    <button
      v-for="item in items"
      :key="item.value"
      type="button"
      role="tab"
      :disabled="item.disabled"
      :aria-selected="model === item.value"
      class="inline-flex items-center justify-center gap-1.5 rounded font-medium transition select-none disabled:cursor-not-allowed disabled:opacity-45"
      :class="[
        size === 'md' ? 'h-8 px-3 text-[13px]' : 'h-7 px-2.5 text-xs',
        block ? 'flex-1' : '',
        model === item.value
          ? 'bg-surface text-fg shadow-panel border border-line/80'
          : 'border border-transparent text-muted hover:text-fg',
      ]"
      @click="!item.disabled && (model = item.value)"
    >
      <Icon v-if="item.icon" :name="item.icon" :size="size === 'md' ? 14 : 13" />
      <span class="truncate">{{ item.label }}</span>
      <span
        v-if="item.badge !== undefined && item.badge !== ''"
        class="tnum rounded px-1 py-px text-2xs leading-3"
        :class="model === item.value ? 'bg-accent/12 text-accent' : 'bg-overlay text-subtle'"
      >
        {{ item.badge }}
      </span>
    </button>
  </div>
</template>
