<script setup lang="ts">
import { useAttrs } from 'vue'

defineOptions({ inheritAttrs: false })

withDefaults(
  defineProps<{
    placeholder?: string
    disabled?: boolean
    rows?: number
    invalid?: boolean
    mono?: boolean
    id?: string
  }>(),
  { placeholder: '', disabled: false, rows: 5, invalid: false, mono: false },
)

const model = defineModel<string>({ default: '' })
const attrs = useAttrs()
</script>

<template>
  <textarea
    :id="id"
    :value="model"
    :rows="rows"
    :placeholder="placeholder"
    :disabled="disabled"
    :aria-invalid="invalid || undefined"
    :class="[
      'field resize-y leading-relaxed',
      mono ? 'font-mono' : '',
      invalid ? 'border-danger focus:border-danger focus:ring-danger/25' : '',
      attrs.class,
    ]"
    v-bind="{ ...attrs, class: undefined }"
    @input="model = ($event.target as HTMLTextAreaElement).value"
  />
</template>
