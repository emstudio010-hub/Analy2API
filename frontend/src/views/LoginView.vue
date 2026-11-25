<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { toMessage } from '@/api/client'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import { useAuthStore } from '@/stores/auth'
import { useToastStore } from '@/stores/toast'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const toast = useToastStore()

const email = ref('')
const password = ref('')
const showPassword = ref(false)
const error = ref('')

const redirect = computed(() => {
  const raw = route.query.redirect
  return typeof raw === 'string' && raw.startsWith('/') ? raw : '/dashboard'
})

const canSubmit = computed(() => Boolean(email.value.trim()) && Boolean(password.value))

onMounted(() => {
  const el = document.querySelector<HTMLInputElement>('#login-email')
  el?.focus()
})

async function submit(): Promise<void> {
  if (!canSubmit.value || auth.loggingIn) return
  error.value = ''
  try {
    const user = await auth.login(email.value.trim(), password.value)
    toast.success('登录成功', `欢迎回来，${user.email}`)
    await router.replace(redirect.value)
  } catch (err) {
    error.value = toMessage(err, '登录失败，请检查管理员邮箱与密码')
    password.value = ''
  }
}
</script>

<template>
  <div class="relative flex min-h-screen flex-col items-center justify-center bg-canvas px-4 py-10">
    <!-- 顶部环境标识与深浅主题切换 -->
    <div class="fixed inset-x-0 top-0 flex items-center justify-between px-5 py-3.5">
      <div class="flex items-center gap-2 text-2xs text-subtle">
        <span class="h-1.5 w-1.5 rounded-full bg-success" aria-hidden="true" />
        <span class="font-mono">Analy2API</span>
        <span class="hidden sm:inline">· Multi-Platform AI Gateway &amp; Container Orchestrator</span>
      </div>
      <ThemeToggle />
    </div>

    <div class="w-full max-w-[25rem]">
      <!-- 控制台标识头 -->
      <div class="mb-5 flex flex-col items-center text-center">
        <div class="mb-3 flex h-12 w-12 items-center justify-center rounded-xl border border-line bg-surface p-1.5 shadow-panel">
          <img
            src="/logo.png"
            alt="Analy2API"
            width="36"
            height="36"
            class="h-9 w-9 rounded-lg object-cover"
          />
        </div>
        <div class="flex items-center gap-2">
          <h1 class="text-lg font-semibold tracking-tight text-fg">Analy2API</h1>
          <Badge tone="accent" mono>CONSOLE</Badge>
        </div>
        <p class="mt-1 font-mono text-2xs text-muted">
          Multi-Platform AI Gateway &amp; Container Orchestrator
        </p>
        <div class="mt-2.5 flex flex-wrap items-center justify-center gap-1.5">
          <PlatformBadge platform="antigravity" short />
          <PlatformBadge platform="workbuddy" short />
          <PlatformBadge platform="commandcode" short />
          <PlatformBadge platform="opencode" short />
        </div>
      </div>

      <!-- 登录表单面板 -->
      <form class="card space-y-4 p-5 sm:p-6" @submit.prevent="submit">
        <div class="border-b border-line pb-3">
          <h2 class="text-[13px] font-semibold text-fg">管理员身份认证</h2>
          <p class="mt-0.5 text-2xs text-subtle">
            请输入部署初始化时配置的管理员账号凭证进入调度工作台
          </p>
        </div>

        <Field label="管理员邮箱" required html-for="login-email">
          <Input
            id="login-email"
            v-model="email"
            type="email"
            autocomplete="username"
            placeholder="admin@example.com"
            :disabled="auth.loggingIn"
          />
        </Field>

        <Field label="访问密码" required html-for="login-password">
          <div class="relative">
            <Input
              id="login-password"
              v-model="password"
              :type="showPassword ? 'text' : 'password'"
              autocomplete="current-password"
              placeholder="请输入管理员密码"
              class="pr-9"
              :disabled="auth.loggingIn"
            />
            <button
              type="button"
              class="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-subtle transition hover:bg-overlay hover:text-fg"
              :aria-label="showPassword ? '隐藏密码' : '显示密码'"
              @click="showPassword = !showPassword"
            >
              <Icon :name="showPassword ? 'eyeOff' : 'eye'" :size="14" />
            </button>
          </div>
        </Field>

        <p
          v-if="error"
          class="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs leading-relaxed text-danger"
        >
          <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
          <span>{{ error }}</span>
        </p>

        <Button
          type="submit"
          variant="primary"
          size="md"
          block
          :loading="auth.loggingIn"
          :disabled="!canSubmit"
        >
          登录网关控制台
        </Button>
      </form>

      <!-- 底部协议与安全说明 -->
      <div class="mt-4 flex flex-col items-center gap-1.5 text-center text-2xs text-subtle">
        <p class="font-mono text-[11px]">
          OpenAI `/v1/chat/completions` · Anthropic `/v1/messages` · `/v1/responses`
        </p>
        <p>仅限授权管理员与 `adm-` 远程管理密钥访问 · 全链路风控已启用</p>
      </div>
    </div>
  </div>
</template>
