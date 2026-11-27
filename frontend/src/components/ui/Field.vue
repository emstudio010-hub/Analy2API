<script setup lang="ts">
// 表单行：标签 + 控件 + 说明/错误。让所有弹窗与页面的表单间距和内联校验提示高度一致。
withDefaults(
  defineProps<{
    label?: string
    hint?: string
    error?: string
    required?: boolean
    htmlFor?: string
  }>(),
  { label: '', hint: '', error: '', required: false },
)
</script>

<template>
  <div class="space-y-1.5">
    <label
      v-if="label"
      :for="htmlFor"
      class="flex items-center gap-1 text-xs font-medium leading-4 text-muted"
    >
      <span>{{ label }}</span>
      <span v-if="required" class="text-danger" aria-hidden="true">*</span>
    </label>

    <slot />

    <p v-if="error" class="text-2xs font-medium leading-4 text-danger" role="alert">
      {{ error }}
    </p>
    <p v-else-if="hint" class="text-2xs leading-4 text-subtle">{{ hint }}</p>
  </div>
</template>
