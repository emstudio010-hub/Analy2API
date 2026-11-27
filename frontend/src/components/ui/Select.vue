<script setup lang="ts">
import { computed, useAttrs } from 'vue'

import Spinner from './Spinner.vue'
import type { SelectOption } from './types'

defineOptions({ inheritAttrs: false })

const props = withDefaults(
  defineProps<{
    options: SelectOption[]
    disabled?: boolean
    loading?: boolean
    size?: 'sm' | 'md'
    invalid?: boolean
    /** 值为空时显示的首项文案。空串代表「不筛选」。 */
    placeholder?: string
    /** 当 options 为空且未处于加载态时展示的占位提示。 */
    emptyText?: string
    id?: string
  }>(),
  {
    disabled: false,
    loading: false,
    size: 'md',
    invalid: false,
    placeholder: '',
    emptyText: '暂无可选项',
  },
)

const model = defineModel<string>({ default: '' })

const attrs = useAttrs()
const isDisabled = computed(() => props.disabled || props.loading)
const classes = computed(() => [
  props.size === 'sm' ? 'field field-sm' : 'field',
  'cursor-pointer appearance-none bg-no-repeat pr-7',
  props.invalid ? 'border-danger focus:border-danger focus:ring-danger/25' : '',
  !model.value && props.placeholder ? 'text-muted' : 'text-fg',
  attrs.class,
])
</script>

<template>
  <div class="relative inline-flex w-full">
    <select
      :id="id"
      :value="model"
      :disabled="isDisabled"
      :aria-busy="loading || undefined"
      :aria-invalid="invalid || undefined"
      :class="classes"
      v-bind="{ ...attrs, class: undefined }"
      @change="model = ($event.target as HTMLSelectElement).value"
    >
      <option v-if="loading && !placeholder && options.length === 0" value="" disabled>
        加载中…
      </option>
      <option v-if="placeholder" value="">{{ loading ? `${placeholder}（加载中…）` : placeholder }}</option>
      <option
        v-if="!loading && options.length === 0"
        value=""
        disabled
      >
        {{ emptyText }}
      </option>
      <option
        v-for="option in options"
        :key="option.value"
        :value="option.value"
        :disabled="option.disabled"
      >
        {{ option.label }}
      </option>
    </select>

    <span
      class="pointer-events-none absolute right-2.5 top-1/2 flex -translate-y-1/2 items-center text-subtle"
      aria-hidden="true"
    >
      <Spinner v-if="loading" :size="12" />
      <svg
        v-else
        class="h-3.5 w-3.5"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <path d="M6 9l6 6 6-6" />
      </svg>
    </span>
  </div>
</template>
