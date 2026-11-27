<script setup lang="ts">
import { ref, watch } from 'vue'
import { RouterView, useRoute, useRouter } from 'vue-router'

import AppSidebar from '@/components/layout/AppSidebar.vue'
import ChangeEmailModal from '@/components/layout/ChangeEmailModal.vue'
import ChangePasswordModal from '@/components/layout/ChangePasswordModal.vue'
import TopBar from '@/components/layout/TopBar.vue'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { useToastStore } from '@/stores/toast'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const theme = useThemeStore()
const toast = useToastStore()

const sidebarOpen = ref(false)
const passwordOpen = ref(false)
const emailOpen = ref(false)

watch(
  () => route.fullPath,
  () => {
    sidebarOpen.value = false
  },
)

function logout(): void {
  auth.clearSession()
  toast.info('已退出登录')
  router.replace({ name: 'login' })
}
</script>

<template>
  <div class="min-h-screen bg-canvas">
    <a
      href="#main-workspace"
      class="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-[90] focus:rounded-md focus:bg-accent focus:px-3 focus:py-1.5 focus:text-xs focus:font-medium focus:text-accent-fg focus:shadow-pop"
    >
      跳转到主工作区
    </a>

    <AppSidebar v-model:open="sidebarOpen" />

    <div
      class="flex min-h-screen min-w-0 flex-col transition-[padding-left] duration-150"
      :class="theme.sidebarCollapsed ? 'nav:pl-16' : 'nav:pl-60'"
    >
      <TopBar
        @toggle-sidebar="sidebarOpen = !sidebarOpen"
        @change-password="passwordOpen = true"
        @change-email="emailOpen = true"
        @logout="logout"
      />

      <main id="main-workspace" class="flex-1 min-w-0 px-3 py-4 sm:px-5 sm:py-5">
        <div class="mx-auto w-full max-w-[1640px]">
          <RouterView v-slot="{ Component }">
            <Transition name="fade" mode="out-in">
              <component :is="Component" />
            </Transition>
          </RouterView>
        </div>
      </main>
    </div>

    <ChangePasswordModal v-model:open="passwordOpen" />
    <ChangeEmailModal v-model:open="emailOpen" />
  </div>
</template>
