<script setup lang="ts">
import { computed } from 'vue'

import { ALL_PLATFORMS, PLATFORMS } from '@/utils/platforms'

import Icon from './Icon.vue'

withDefaults(
  defineProps<{
    disabled?: boolean
    invalid?: boolean
  }>(),
  { disabled: false, invalid: false },
)

const model = defineModel<string>({ default: 'workbuddy' })

const displayedPlatforms = computed(() => {
  if (PLATFORMS.some((p) => p.value === model.value)) return PLATFORMS
  const matched = ALL_PLATFORMS.find((p) => p.value === model.value)
  return matched ? [matched, ...PLATFORMS] : PLATFORMS
})
</script>

<template>
  <div class="grid gap-2" :class="displayedPlatforms.length > 1 ? 'sm:grid-cols-2' : 'grid-cols-1'">
    <button
      v-for="item in displayedPlatforms"
      :key="item.value"
      type="button"
      :disabled="disabled"
      :aria-pressed="model === item.value ? 'true' : 'false'"
      class="rounded-md border px-3 py-2.5 text-left transition"
      :class="[
        model === item.value
          ? 'border-accent bg-accent/10'
          : invalid
            ? 'border-danger bg-raised/40'
            : 'border-line bg-raised/40 hover:border-line-strong hover:bg-raised',
        disabled ? 'cursor-not-allowed opacity-70' : 'cursor-pointer',
      ]"
      @click="model = item.value"
    >
      <span class="flex items-center gap-1.5">
        <Icon
          :name="item.icon"
          :size="14"
          :class="model === item.value ? 'text-accent' : 'text-subtle'"
        />
        <span class="text-[13px] font-medium text-fg">{{ item.label }}</span>
        <span
          class="rounded border px-1 py-px font-mono text-[10px] leading-3"
          :class="
            model === item.value
              ? 'border-accent/30 bg-accent/10 text-accent'
              : 'border-line bg-surface text-subtle'
          "
        >
          {{ item.shortLabel }}
        </span>
        <Icon
          v-if="model === item.value"
          name="checkCircle"
          :size="13"
          class="ml-auto text-accent"
        />
        <Icon v-else-if="disabled" name="lock" :size="12" class="ml-auto text-subtle" />
      </span>
      <span class="mt-1 block text-2xs leading-4 text-subtle">{{ item.description }}</span>
    </button>
  </div>
</template>
