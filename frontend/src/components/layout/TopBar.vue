<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import Icon from '@/components/ui/Icon.vue'
import { useThemeStore } from '@/stores/theme'

import AccountMenu from './AccountMenu.vue'
import ThemeToggle from './ThemeToggle.vue'

const emit = defineEmits<{
  (event: 'toggle-sidebar'): void
  (event: 'change-password'): void
  (event: 'change-email'): void
  (event: 'logout'): void
}>()

const route = useRoute()
const theme = useThemeStore()

const section = computed(() => route.meta.section ?? '')
const title = computed(() => route.meta.title ?? '')
const subtitle = computed(() => route.meta.subtitle ?? '')
</script>

<template>
  <header
    class="sticky top-0 z-20 flex h-13 items-center gap-3 border-b border-line bg-surface/90 px-3 py-2 backdrop-blur-md sm:px-5"
  >
    <!-- 移动端抽屉开关 -->
    <button
      type="button"
      class="-ml-1 flex h-8 w-8 items-center justify-center rounded-md text-muted transition hover:bg-raised hover:text-fg nav:hidden"
      aria-label="展开导航"
      @click="emit('toggle-sidebar')"
    >
      <Icon name="menu" :size="17" />
    </button>

    <!-- 桌面端侧栏紧凑切换 -->
    <button
      type="button"
      class="-ml-1 hidden h-8 w-8 items-center justify-center rounded-md text-subtle transition hover:bg-raised hover:text-fg nav:inline-flex"
      :title="theme.sidebarCollapsed ? '展开导航栏' : '收起导航栏'"
      aria-label="切换导航栏宽度"
      @click="theme.toggleSidebarCollapsed()"
    >
      <Icon name="panelLeft" :size="15" />
    </button>

    <div class="min-w-0 flex-1">
      <div class="flex items-center gap-1.5">
        <span v-if="section" class="hidden text-xs font-medium text-subtle md:inline">
          {{ section }}
        </span>
        <Icon
          v-if="section"
          name="chevronRight"
          :size="12"
          class="hidden text-subtle/70 md:inline"
        />
        <h1 class="truncate text-[14px] font-semibold leading-5 text-fg">{{ title }}</h1>
      </div>
      <p v-if="subtitle" class="hidden truncate text-2xs leading-4 text-subtle sm:block">
        {{ subtitle }}
      </p>
    </div>

    <div class="flex shrink-0 items-center gap-2">
      <ThemeToggle />
      <AccountMenu
        @change-password="emit('change-password')"
        @change-email="emit('change-email')"
        @logout="emit('logout')"
      />
    </div>
  </header>
</template>
