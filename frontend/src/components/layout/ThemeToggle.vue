<script setup lang="ts">
import Icon from '@/components/ui/Icon.vue'
import { useThemeStore } from '@/stores/theme'

const theme = useThemeStore()
</script>

<template>
  <button
    type="button"
    class="inline-flex h-8 items-center gap-1.5 rounded-md border border-line bg-raised px-2.5 text-xs font-medium text-muted transition hover:border-line-strong hover:bg-overlay hover:text-fg"
    :title="theme.isDark ? '切换到浅色工业主题' : '切换到深色控制台主题'"
    :aria-label="theme.isDark ? '切换到浅色工业主题' : '切换到深色控制台主题'"
    @click="theme.toggle()"
  >
    <Icon :name="theme.isDark ? 'moon' : 'sun'" :size="14" class="text-accent" />
    <span class="hidden sm:inline">{{ theme.isDark ? '深色' : '浅色' }}</span>
  </button>
</template>
