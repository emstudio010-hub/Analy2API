<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { toMessage } from '@/api/client'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
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
const emailTouched = ref(false)
const passwordTouched = ref(false)
const capsLockOn = ref(false)
const error = ref('')

const redirect = computed(() => {
  const raw = route.query.redirect
  return typeof raw === 'string' && raw.startsWith('/') ? raw : '/dashboard'
})

const emailError = computed(() => {
  if (!emailTouched.value) return ''
  const trimmed = email.value.trim()
  if (!trimmed) return '请输入管理员登录邮箱'
  if (!trimmed.includes('@')) return '请输入有效的邮箱格式（如 admin@example.com）'
  return ''
})

const passwordError = computed(() => {
  if (!passwordTouched.value) return ''
  if (!password.value) return '请输入管理员访问密码'
  return ''
})

const canSubmit = computed(
  () =>
    Boolean(email.value.trim()) &&
    email.value.trim().includes('@') &&
    Boolean(password.value),
)

function checkCapsLock(event: KeyboardEvent): void {
  if (typeof event.getModifierState === 'function') {
    capsLockOn.value = event.getModifierState('CapsLock')
  }
}

onMounted(() => {
  const el = document.querySelector<HTMLInputElement>('#login-email')
  el?.focus()
})

async function submit(): Promise<void> {
  emailTouched.value = true
  passwordTouched.value = true
  if (!canSubmit.value || auth.loggingIn) return
  error.value = ''
  try {
    const user = await auth.login(email.value.trim(), password.value)
    toast.success('登录成功', `欢迎回来，${user.email}`)
    await router.replace(redirect.value)
  } catch (err) {
    error.value = toMessage(err, '登录失败，请检查管理员邮箱与密码')
    password.value = ''
    passwordTouched.value = false
  }
}
</script>

<template>
  <div class="relative flex min-h-screen flex-col items-center justify-center bg-canvas px-4 py-12">
    <!-- 顶部环境标识与深浅主题切换 -->
    <header class="fixed inset-x-0 top-0 z-10 flex h-14 items-center justify-between border-b border-line/60 bg-surface/80 px-5 backdrop-blur-md">
      <div class="flex items-center gap-2 text-2xs text-muted">
        <span class="h-1.5 w-1.5 rounded-full bg-success" aria-hidden="true" />
        <span class="font-mono font-semibold text-fg">Analy2API</span>
        <span class="hidden text-subtle sm:inline">
          · Multi-Platform AI Gateway &amp; Container Orchestrator
        </span>
      </div>
      <ThemeToggle />
    </header>

    <div class="w-full max-w-[26rem] pt-6">
      <!-- 控制台标识头 -->
      <div class="mb-5 flex flex-col items-center text-center">
        <div
          class="mb-3 flex h-12 w-12 items-center justify-center rounded-xl border border-line bg-surface p-1.5 shadow-panel"
        >
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
        <p class="mt-1 text-xs text-muted">多源 AI 网关与容器集群运维调度台</p>
        <div class="mt-2.5 flex flex-wrap items-center justify-center gap-1.5">
          <PlatformBadge platform="antigravity" short />
          <PlatformBadge platform="workbuddy" short />
          <PlatformBadge platform="commandcode" short />
          <PlatformBadge platform="opencode" short />
        </div>
      </div>

      <!-- 登录卡片（统一 Card Header 规范） -->
      <Card
        title="管理员身份认证"
        subtitle="请输入部署初始化时配置的管理员账号凭证进入调度工作台"
        body-class="p-5"
      >
        <form class="space-y-4" novalidate @submit.prevent="submit">
          <Field
            label="管理员邮箱"
            required
            html-for="login-email"
            :error="emailError"
          >
            <Input
              id="login-email"
              v-model="email"
              type="email"
              autocomplete="username"
              placeholder="admin@example.com"
              :invalid="Boolean(emailError)"
              :disabled="auth.loggingIn"
              @blur="emailTouched = true"
            />
          </Field>

          <Field
            label="访问密码"
            required
            html-for="login-password"
            :error="passwordError"
            :hint="capsLockOn ? '提示：当前大写锁定 (Caps Lock) 已开启' : ''"
          >
            <div class="relative">
              <Input
                id="login-password"
                v-model="password"
                :type="showPassword ? 'text' : 'password'"
                autocomplete="current-password"
                placeholder="请输入管理员密码"
                class="pr-9"
                :invalid="Boolean(passwordError)"
                :disabled="auth.loggingIn"
                @blur="passwordTouched = true"
                @keydown="checkCapsLock"
                @keyup="checkCapsLock"
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
            role="alert"
            class="flex items-start gap-2 rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-2xs leading-relaxed text-danger"
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
            :disabled="auth.loggingIn"
          >
            {{ auth.loggingIn ? '正在验证凭证…' : '登录网关控制台' }}
          </Button>
        </form>
      </Card>

      <!-- 底部协议与安全说明 -->
      <div class="mt-4 flex flex-col items-center gap-1 text-center text-2xs text-subtle">
        <p class="font-mono text-[11px]">
          OpenAI /v1/chat/completions · Anthropic /v1/messages · Codex /v1/responses
        </p>
        <p>仅限授权管理员与 adm- 远程管理密钥访问 · 全链路内容风控已启用</p>
      </div>
    </div>
  </div>
</template>
