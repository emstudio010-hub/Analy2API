<script setup lang="ts">
import { computed, useAttrs } from 'vue'

defineOptions({ inheritAttrs: false })

const props = withDefaults(
  defineProps<{
    type?: string
    placeholder?: string
    disabled?: boolean
    readonly?: boolean
    size?: 'sm' | 'md'
    /** 校验失败时的红色描边。 */
    invalid?: boolean
    /** 等宽字体：密钥、IP、ID 这类字段用它更好读。 */
    mono?: boolean
    min?: number | string
    max?: number | string
    step?: number | string
    autocomplete?: string
    id?: string
    spellcheck?: boolean
  }>(),
  {
    type: 'text',
    placeholder: '',
    disabled: false,
    readonly: false,
    size: 'md',
    invalid: false,
    mono: false,
    spellcheck: false,
  },
)

const model = defineModel<string>({ default: '' })

const attrs = useAttrs()

const classes = computed(() => [
  props.size === 'sm' ? 'field field-sm' : 'field',
  props.mono ? 'font-mono' : '',
  props.readonly ? 'bg-raised/45 text-muted' : '',
  props.invalid ? 'border-danger focus:border-danger focus:ring-danger/25' : '',
  attrs.class,
])
</script>

<template>
  <input
    :id="id"
    :type="type"
    :value="model"
    :placeholder="placeholder"
    :disabled="disabled"
    :readonly="readonly"
    :min="min"
    :max="max"
    :step="step"
    :autocomplete="autocomplete"
    :spellcheck="spellcheck"
    :aria-invalid="invalid || undefined"
    :class="classes"
    v-bind="{ ...attrs, class: undefined }"
    @input="model = ($event.target as HTMLInputElement).value"
  />
</template>
