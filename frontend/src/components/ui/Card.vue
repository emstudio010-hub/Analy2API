<script setup lang="ts">
import Spinner from './Spinner.vue'

// 面板容器。header / footer 用插槽，正文用默认插槽，
// padding 由调用方通过 bodyClass 决定（表格要贴着边，表单要留白）。
withDefaults(
  defineProps<{
    title?: string
    subtitle?: string
    bodyClass?: string
    loading?: boolean
  }>(),
  { title: '', subtitle: '', bodyClass: 'p-4', loading: false },
)
</script>

<template>
  <section class="card overflow-hidden">
    <header
      v-if="title || $slots.header || $slots.actions"
      class="flex flex-wrap items-center justify-between gap-3 border-b border-line bg-raised/25 px-4 py-3"
    >
      <div class="min-w-0 flex-1">
        <slot name="header">
          <div class="flex items-center gap-2">
            <h2 class="truncate text-[13.5px] font-semibold leading-5 text-fg">{{ title }}</h2>
            <Spinner v-if="loading" :size="12" class="text-accent" />
          </div>
          <p v-if="subtitle" class="mt-0.5 text-2xs leading-relaxed text-subtle">{{ subtitle }}</p>
        </slot>
      </div>
      <div v-if="$slots.actions" class="flex flex-wrap shrink-0 items-center gap-2">
        <slot name="actions" />
      </div>
    </header>

    <div :class="bodyClass">
      <slot />
    </div>

    <footer v-if="$slots.footer" class="border-t border-line bg-raised/30 px-4 py-2.5">
      <slot name="footer" />
    </footer>
  </section>
</template>
