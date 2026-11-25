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
    <AppSidebar v-model:open="sidebarOpen" />

    <div
      class="flex min-h-screen flex-col transition-[padding-left] duration-150"
      :class="theme.sidebarCollapsed ? 'nav:pl-16' : 'nav:pl-60'"
    >
      <TopBar
        @toggle-sidebar="sidebarOpen = !sidebarOpen"
        @change-password="passwordOpen = true"
        @change-email="emailOpen = true"
        @logout="logout"
      />

      <main class="flex-1 px-3 py-4 sm:px-5 sm:py-4">
        <div class="mx-auto w-full max-w-[1600px]">
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
