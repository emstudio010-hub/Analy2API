<script setup lang="ts">
withDefaults(
  defineProps<{
    disabled?: boolean
    size?: 'sm' | 'md'
    label?: string
    description?: string
  }>(),
  { disabled: false, size: 'md', label: '', description: '' },
)

const model = defineModel<boolean>({ default: false })
</script>

<template>
  <label
    class="inline-flex items-start gap-2.5 select-none"
    :class="disabled ? 'cursor-not-allowed opacity-55' : 'cursor-pointer'"
  >
    <button
      type="button"
      role="switch"
      :aria-checked="model"
      :disabled="disabled"
      class="relative mt-0.5 inline-flex shrink-0 items-center rounded-full border transition"
      :class="[
        size === 'sm' ? 'h-4 w-7' : 'h-5 w-9',
        model
          ? 'border-accent bg-accent'
          : 'border-line-strong bg-raised hover:border-subtle',
      ]"
      @click="!disabled && (model = !model)"
    >
      <span
        class="inline-block rounded-full bg-white shadow-sm transition-transform"
        :class="[
          size === 'sm' ? 'h-3 w-3' : 'h-3.5 w-3.5',
          model
            ? size === 'sm'
              ? 'translate-x-3'
              : 'translate-x-4'
            : 'translate-x-0.5',
        ]"
      />
    </button>
    <span v-if="label || description" class="min-w-0 leading-4">
      <span v-if="label" class="block text-[13px] font-medium text-fg">{{ label }}</span>
      <span v-if="description" class="mt-0.5 block text-2xs leading-relaxed text-subtle">
        {{ description }}
      </span>
    </span>
  </label>
</template>
