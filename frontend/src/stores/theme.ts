import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { readStore, writeStore } from '@/utils/storage'

export type ThemeMode = 'dark' | 'light'

const THEME_KEY = 'analy2api.theme'
const SIDEBAR_COLLAPSED_KEY = 'analy2api.sidebar_collapsed'

function initialMode(): ThemeMode {
  const saved = readStore(THEME_KEY) ?? readStore('cmdwb2api.theme') ?? readStore('cmd2api.theme')
  return saved === 'light' ? 'light' : 'dark'
}

function initialSidebarCollapsed(): boolean {
  const saved = readStore(SIDEBAR_COLLAPSED_KEY) ?? readStore('cmdwb2api.sidebar_collapsed')
  return saved === '1'
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(initialMode())
  const sidebarCollapsed = ref<boolean>(initialSidebarCollapsed())

  const isDark = computed(() => mode.value === 'dark')

  /**
   * 同步主题到 <html> 的 `.dark` / `.light` class 与移动端状态栏颜色，并持久化到 localStorage。
   */
  function apply(): void {
    if (typeof document === 'undefined') return
    const root = document.documentElement
    const dark = mode.value === 'dark'
    root.classList.toggle('dark', dark)
    root.classList.toggle('light', !dark)

    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) {
      meta.setAttribute('content', dark ? '#0b0d11' : '#f4f6f9')
    }

    writeStore(THEME_KEY, mode.value)
  }

  function setMode(next: ThemeMode): void {
    mode.value = next
    apply()
  }

  function toggle(): void {
    setMode(mode.value === 'dark' ? 'light' : 'dark')
  }

  function setSidebarCollapsed(collapsed: boolean): void {
    sidebarCollapsed.value = collapsed
    writeStore(SIDEBAR_COLLAPSED_KEY, collapsed ? '1' : '0')
  }

  function toggleSidebarCollapsed(): void {
    setSidebarCollapsed(!sidebarCollapsed.value)
  }

  function init(): void {
    apply()
  }

  return {
    mode,
    isDark,
    sidebarCollapsed,
    setMode,
    toggle,
    setSidebarCollapsed,
    toggleSidebarCollapsed,
    init,
  }
})
