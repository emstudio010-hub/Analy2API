<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import type { Account, LoginLinkResult } from '@/api/types'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'

const props = defineProps<{
  open: boolean
  account: Account | null
  result: LoginLinkResult | null
  loading: boolean
  error: string
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'regenerate', target: string): void
}>()

const toast = useToastStore()

const targetPath = ref('')
const remaining = ref(0)
const copiedCode = ref(false)
const copiedUrl = ref(false)

let timer: ReturnType<typeof setInterval> | null = null
let copyCodeTimer: ReturnType<typeof setTimeout> | null = null
let copyUrlTimer: ReturnType<typeof setTimeout> | null = null

function stopTimer(): void {
  if (timer !== null) {
    clearInterval(timer)
    timer = null
  }
}

function clearCopyTimers(): void {
  if (copyCodeTimer !== null) {
    clearTimeout(copyCodeTimer)
    copyCodeTimer = null
  }
  if (copyUrlTimer !== null) {
    clearTimeout(copyUrlTimer)
    copyUrlTimer = null
  }
}

function startCountdown(seconds: number): void {
  stopTimer()
  remaining.value = Math.max(0, Math.floor(seconds || 300))
  if (remaining.value <= 0) return
  timer = setInterval(() => {
    if (remaining.value <= 1) {
      remaining.value = 0
      stopTimer()
    } else {
      remaining.value -= 1
    }
  }, 1000)
}

watch(
  () => props.account?.id,
  () => {
    targetPath.value = ''
    copiedCode.value = false
    copiedUrl.value = false
  },
)

watch(
  () => [props.open, props.result] as const,
  ([isOpen, res]) => {
    if (!isOpen) {
      stopTimer()
      clearCopyTimers()
      copiedCode.value = false
      copiedUrl.value = false
      return
    }
    if (res) {
      startCountdown(res.expires_in || 300)
    } else {
      stopTimer()
      remaining.value = 0
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  stopTimer()
  clearCopyTimers()
})

const expired = computed(() => Boolean(props.result) && remaining.value <= 0)
const totalDuration = computed(() => Math.max(props.result?.expires_in || 300, 1))
const progressPercent = computed(() =>
  Math.min(100, Math.max(0, (remaining.value / totalDuration.value) * 100)),
)

const formattedRemaining = computed(() => {
  const s = remaining.value
  const mm = Math.floor(s / 60)
  const ss = s % 60
  return `${String(mm).padStart(2, '0')}:${String(ss).padStart(2, '0')}（${s}s）`
})

const countdownTone = computed(() => {
  if (expired.value) return 'danger'
  if (remaining.value <= 60) return 'warning'
  return 'success'
})

async function copyCode(): Promise<void> {
  if (!props.result?.device_code) return
  const ok = await copyText(props.result.device_code)
  if (ok) {
    copiedCode.value = true
    if (copyCodeTimer) clearTimeout(copyCodeTimer)
    copyCodeTimer = setTimeout(() => {
      copiedCode.value = false
    }, 1500)
    toast.success('已复制授权码')
  } else {
    toast.error('复制失败', '请手动选中授权码复制')
  }
}

async function copyUrl(): Promise<void> {
  if (!props.result?.login_url) return
  const ok = await copyText(props.result.login_url)
  if (ok) {
    copiedUrl.value = true
    if (copyUrlTimer) clearTimeout(copyUrlTimer)
    copyUrlTimer = setTimeout(() => {
      copiedUrl.value = false
    }, 1500)
    toast.success('已复制免密登录链接')
  } else {
    toast.error('复制失败', '请手动选中链接复制')
  }
}

function openInNewTab(): void {
  if (!props.result?.login_url) return
  window.open(props.result.login_url, '_blank', 'noopener,noreferrer')
}

function close(): void {
  emit('update:open', false)
}
</script>

<template>
  <Modal
    :open="open"
    title="WorkBuddy 官网免密登录"
    :subtitle="account ? `账号：${account.name}${account.uid ? ` · UID ${account.uid}` : ''}` : ''"
    size="md"
    @update:open="emit('update:open', $event)"
  >
    <div class="space-y-4">
      <div class="flex flex-wrap items-end gap-2">
        <div class="min-w-0 flex-1">
          <Field label="跳转目标路径（可选）" hint="留空默认控制台首页，例如 /billing 或 /profile">
            <Input
              v-model="targetPath"
              size="sm"
              mono
              placeholder="/billing"
              :disabled="loading"
            />
          </Field>
        </div>
        <Button
          variant="secondary"
          size="sm"
          :loading="loading"
          @click="emit('regenerate', targetPath.trim())"
        >
          <template #leading><Icon name="refresh" :size="13" /></template>
          {{ result ? '重新获取' : '生成链接' }}
        </Button>
      </div>

      <p
        v-if="error"
        class="flex items-start gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs leading-relaxed text-danger"
      >
        <Icon name="alertCircle" :size="13" class="mt-px shrink-0" />
        <span>{{ error }}</span>
      </p>

      <div
        v-else-if="loading && !result"
        class="flex items-center justify-center gap-2 rounded-md border border-line bg-raised/40 py-8 text-2xs text-muted"
      >
        <Icon name="refresh" :size="14" class="animate-spin text-accent" />
        <span>正在向 WorkBuddy 上游签发一次性免密登录授权码…</span>
      </div>

      <template v-else-if="result">
        <div class="rounded-md border border-line bg-raised/40 p-3 space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-2xs font-medium text-muted">一次性授权码 (Device Code)</span>
            <Badge :tone="countdownTone" dot>
              {{ expired ? '已过期，请重新获取' : `有效期 ${formattedRemaining}` }}
            </Badge>
          </div>

          <div class="flex items-center justify-between gap-2 rounded-md border border-line bg-surface px-3 py-2">
            <code class="select-all font-mono text-base font-semibold tracking-wider text-fg">
              {{ result.device_code }}
            </code>
            <Button variant="ghost" size="xs" @click="copyCode">
              <template #leading>
                <Icon :name="copiedCode ? 'check' : 'copy'" :size="12" />
              </template>
              {{ copiedCode ? '已复制' : '复制授权码' }}
            </Button>
          </div>

          <div class="h-1.5 w-full overflow-hidden rounded-full bg-raised">
            <div
              class="h-full rounded-full transition-all duration-500"
              :class="
                expired
                  ? 'bg-danger'
                  : remaining <= 60
                    ? 'bg-warning'
                    : 'bg-success'
              "
              :style="{ width: `${progressPercent}%` }"
            />
          </div>
        </div>

        <Field label="免密登录链接" hint="点击「新标签页打开」或在无痕窗口粘贴链接完成官网免密登录">
          <div class="rounded-md border border-line bg-raised/40 p-2.5">
            <p class="break-all font-mono text-2xs leading-relaxed text-fg">
              {{ result.login_url }}
            </p>
          </div>
        </Field>

        <div class="flex flex-wrap items-center gap-2">
          <Button variant="secondary" size="sm" :disabled="expired" @click="copyUrl">
            <template #leading>
              <Icon :name="copiedUrl ? 'check' : 'copy'" :size="13" />
            </template>
            {{ copiedUrl ? '已复制链接' : '一键复制链接' }}
          </Button>
          <Button variant="primary" size="sm" :disabled="expired" @click="openInNewTab">
            <template #leading><Icon name="external" :size="13" /></template>
            新标签页打开
          </Button>
        </div>
      </template>
    </div>

    <template #footer>
      <Button variant="ghost" size="md" @click="close">关闭</Button>
    </template>
  </Modal>
</template>
