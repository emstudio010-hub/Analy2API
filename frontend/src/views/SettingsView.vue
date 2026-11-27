<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { metaApi, updateRuntimeSettings } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { RuntimeSettingsPayload, SettingsResponse } from '@/api/types'
import ChangeEmailModal from '@/components/layout/ChangeEmailModal.vue'
import ChangePasswordModal from '@/components/layout/ChangePasswordModal.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Skeleton from '@/components/ui/Skeleton.vue'
import Switch from '@/components/ui/Switch.vue'
import { useAuthStore } from '@/stores/auth'
import { useToastStore } from '@/stores/toast'
import { formatDateTime } from '@/utils/format'

interface SettingRow {
  label: string
  value: string
  hint?: string
  mono?: boolean
  tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'accent'
}

const auth = useAuthStore()
const toast = useToastStore()

const settings = ref<SettingsResponse | null>(null)
const loading = ref(true)
const error = ref('')
const savingRuntime = ref(false)

const emailModalOpen = ref(false)
const passwordModalOpen = ref(false)

// ---- 在线热配置表单 ----
const runtimeForm = reactive({
  firstTokenSeconds: '300',
  streamIdleSeconds: '180',
  nonstreamIdleSeconds: '600',
  upstreamProxy: '',
  emptySystemPlaceholder: true,
  autoHarvestEnabled: false,
  autoHarvestInterval: '12h',
})

function parseDurationToSeconds(raw: string | undefined, fallback: number): number {
  if (!raw) return fallback
  const trimmed = raw.trim().toLowerCase()
  const direct = Number(trimmed)
  if (Number.isFinite(direct) && direct > 0) return Math.round(direct)
  let total = 0
  const re = /(\d+(?:\.\d+)?)(h|m|s)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(trimmed)) !== null) {
    const val = Number(m[1])
    const unit = m[2]
    if (unit === 'h') total += val * 3600
    else if (unit === 'm') total += val * 60
    else total += val
  }
  return total > 0 ? Math.round(total) : fallback
}

function hydrateFormFromSettings(s: SettingsResponse): void {
  const t = s.timeouts
  const up = s.upstream
  runtimeForm.firstTokenSeconds = String(
    t?.first_token_seconds ?? parseDurationToSeconds(t?.first_token_timeout ?? up?.first_token_timeout, 300),
  )
  runtimeForm.streamIdleSeconds = String(
    t?.stream_idle_seconds ?? parseDurationToSeconds(t?.stream_idle_timeout ?? up?.stream_idle_timeout, 180),
  )
  runtimeForm.nonstreamIdleSeconds = String(
    t?.nonstream_idle_seconds ??
      parseDurationToSeconds(t?.nonstream_idle_timeout ?? up?.nonstream_idle_timeout, 600),
  )
  runtimeForm.upstreamProxy = up?.upstream_proxy ?? ''
  runtimeForm.emptySystemPlaceholder = Boolean(up?.empty_system_placeholder ?? true)
  runtimeForm.autoHarvestEnabled = Boolean(up?.auto_harvest_enabled ?? false)
  runtimeForm.autoHarvestInterval = up?.auto_harvest_interval || '12h'
}

async function loadSettings(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await metaApi.settings()
    settings.value = res
    hydrateFormFromSettings(res)
  } catch (err) {
    error.value = toMessage(err, '加载系统运行配置失败')
    settings.value = null
  } finally {
    loading.value = false
  }
}

async function saveRuntimeConfig(): Promise<void> {
  const ft = Math.max(5, Math.round(Number(runtimeForm.firstTokenSeconds) || 300))
  const si = Math.max(5, Math.round(Number(runtimeForm.streamIdleSeconds) || 180))
  const nsi = Math.max(10, Math.round(Number(runtimeForm.nonstreamIdleSeconds) || 600))

  savingRuntime.value = true
  try {
    const payload = {
      first_token_seconds: ft,
      first_token_timeout: `${ft}s`,
      stream_idle_seconds: si,
      stream_idle_timeout: `${si}s`,
      nonstream_idle_seconds: nsi,
      nonstream_idle_timeout: `${nsi}s`,
      upstream_proxy: runtimeForm.upstreamProxy.trim(),
      empty_system_placeholder: runtimeForm.emptySystemPlaceholder,
      auto_harvest_enabled: runtimeForm.autoHarvestEnabled,
      auto_harvest_interval: runtimeForm.autoHarvestInterval.trim() || '12h',
      settings: {
        first_token_timeout: `${ft}s`,
        stream_idle_timeout: `${si}s`,
        nonstream_idle_timeout: `${nsi}s`,
        upstream_proxy: runtimeForm.upstreamProxy.trim(),
        empty_system_placeholder: String(runtimeForm.emptySystemPlaceholder),
        auto_harvest_enabled: String(runtimeForm.autoHarvestEnabled),
        auto_harvest_interval: runtimeForm.autoHarvestInterval.trim() || '12h',
      },
    } as RuntimeSettingsPayload

    const updated = await updateRuntimeSettings(payload)
    if (updated && updated.upstream) {
      settings.value = updated
      hydrateFormFromSettings(updated)
    } else {
      await loadSettings()
    }
    toast.success('运行态与分级超时配置已热更新生效', '无需重启网关进程')
  } catch (err) {
    toast.error('保存运行态配置失败', toMessage(err))
  } finally {
    savingRuntime.value = false
  }
}

function resetRecommendedTimeouts(): void {
  runtimeForm.firstTokenSeconds = '300'
  runtimeForm.streamIdleSeconds = '180'
  runtimeForm.nonstreamIdleSeconds = '600'
}

const envOverviewRows = computed<SettingRow[]>(() => {
  const s = settings.value
  if (!s) return []
  const up = s.upstream
  const rows: SettingRow[] = [
    {
      label: '数据库存储引擎',
      value: (s.database?.driver || 'sqlite').toUpperCase(),
      tone: 'accent',
      mono: true,
      hint: s.database?.sqlite_path
        ? `数据文件：${s.database.sqlite_path}`
        : s.database?.data_dir
          ? `数据目录：${s.database.data_dir}`
          : '双模兼容 SQLite WAL / PostgreSQL',
    },
    {
      label: 'WorkBuddy 默认域名',
      value: up.base_url || 'https://www.codebuddy.cn',
      mono: true,
      hint: '账号未单独覆盖 domain 时使用',
    },
    {
      label: '单次请求体上限',
      value: `${up.max_body_mb} MB`,
      mono: true,
      hint: '支持大上下文代码库与多模态图片 Base64',
    },
    {
      label: '模型目录缓存 TTL',
      value: up.model_cache_ttl,
      mono: true,
    },
  ]

  if (s.antigravity) {
    rows.push({
      label: 'AntiGravity Docker 环境',
      value: s.antigravity.docker_available
        ? `就绪 (${s.antigravity.docker_version || 'Docker'})`
        : '仅远程节点模式',
      tone: s.antigravity.docker_available ? 'success' : 'neutral',
      hint: s.antigravity.default_image ? `默认镜像：${s.antigravity.default_image}` : undefined,
    })
  }

  return rows
})

const securityAndHealthRows = computed<SettingRow[]>(() => {
  const s = settings.value
  if (!s) return []
  const filterEnabled = s.filter?.enabled ?? true
  const zeroWidthDisabled = s.filter?.zero_width_disabled ?? true
  const hc = s.health_check

  return [
    {
      label: '全入口内容风控清洗',
      value: filterEnabled ? '强制开启' : '已关闭',
      tone: filterEnabled ? 'success' : 'warning',
      hint: `已启用规则 ${s.filter?.enabled_rules ?? 0} / ${s.filter?.rules_count ?? 0} 条`,
    },
    {
      label: '零宽字符安全约束',
      value: zeroWidthDisabled ? '零 \\u200b 纯净改写' : '未启用',
      tone: zeroWidthDisabled ? 'success' : 'warning',
    },
    {
      label: '全局前置 System 注入',
      value: s.filter?.global_system_prompt_enabled ? '已启用' : '关闭',
      tone: s.filter?.global_system_prompt_enabled ? 'accent' : 'neutral',
      hint: '可在「前置提示词注入」工作台管理全局与分组模板',
    },
    {
      label: '自动健康检查巡检',
      value: hc.enabled ? `开启（每 ${hc.interval}）` : '关闭',
      tone: hc.enabled ? 'success' : 'neutral',
      hint: `连续失败 ${hc.failure_threshold} 次熔断隔离 · 单次探测超时 ${hc.timeout}`,
    },
    {
      label: 'JWT 会话有效期',
      value: s.auth.token_ttl,
      mono: true,
    },
  ]
})

onMounted(() => {
  void loadSettings()
})
</script>

<template>
  <div class="space-y-4">
    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="loadSettings">重试</Button>
    </p>

    <!-- ==================== 核心：运行态与分级超时在线热配置 ==================== -->
    <Card
      title="运行态与分级超时热配置 (Live Runtime & Tiered Timeouts)"
      subtitle="针对大上下文预填充、Claude / DeepSeek / GPT 深度思考与长代码流式输出做三级超时隔离，保存后即刻落库并热生效"
      body-class="p-4 space-y-4"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <Button variant="ghost" size="sm" @click="resetRecommendedTimeouts">
            填入推荐超时 (300s / 180s / 600s)
          </Button>
          <Button variant="primary" size="sm" :loading="savingRuntime" @click="saveRuntimeConfig">
            <template #leading><Icon name="check" :size="13" /></template>
            保存热生效
          </Button>
        </div>
      </template>

      <div v-if="loading" class="grid gap-3.5 md:grid-cols-3">
        <Skeleton v-for="i in 6" :key="i" class="h-16" />
      </div>

      <form v-else class="space-y-4" @submit.prevent="saveRuntimeConfig">
        <!-- 三级超时数值输入 -->
        <div class="grid gap-3.5 md:grid-cols-3">
          <div class="panel-inset p-3 space-y-2">
            <Field
              label="1. 首字等待超时 (First Token / 秒)"
              hint="等待上游大上下文 Prefill 与深度推理思考首包的最大秒数（默认 300s）"
            >
              <Input v-model="runtimeForm.firstTokenSeconds" type="number" min="5" step="10" mono />
            </Field>
            <div class="flex gap-1.5">
              <button
                v-for="preset in ['120', '300', '600']"
                :key="preset"
                type="button"
                :aria-pressed="runtimeForm.firstTokenSeconds === preset"
                class="rounded border px-2 py-0.5 font-mono text-2xs font-medium transition"
                :class="
                  runtimeForm.firstTokenSeconds === preset
                    ? 'border-accent bg-accent/12 text-accent'
                    : 'border-line bg-surface text-subtle hover:border-line-strong hover:text-fg'
                "
                @click="runtimeForm.firstTokenSeconds = preset"
              >
                {{ preset }}s
              </button>
            </div>
          </div>

          <div class="panel-inset p-3 space-y-2">
            <Field
              label="2. 流式分片空闲超时 (Stream Idle / 秒)"
              hint="SSE 流建立后相邻两个 chunk 之间的最大静默间隔（默认 180s）"
            >
              <Input v-model="runtimeForm.streamIdleSeconds" type="number" min="5" step="10" mono />
            </Field>
            <div class="flex gap-1.5">
              <button
                v-for="preset in ['60', '180', '300']"
                :key="preset"
                type="button"
                :aria-pressed="runtimeForm.streamIdleSeconds === preset"
                class="rounded border px-2 py-0.5 font-mono text-2xs font-medium transition"
                :class="
                  runtimeForm.streamIdleSeconds === preset
                    ? 'border-accent bg-accent/12 text-accent'
                    : 'border-line bg-surface text-subtle hover:border-line-strong hover:text-fg'
                "
                @click="runtimeForm.streamIdleSeconds = preset"
              >
                {{ preset }}s
              </button>
            </div>
          </div>

          <div class="panel-inset p-3 space-y-2">
            <Field
              label="3. 非流式总超时 (Non-Stream Total / 秒)"
              hint="stream=false 同步长文生成等待完整 JSON 返回的总上限（默认 600s）"
            >
              <Input
                v-model="runtimeForm.nonstreamIdleSeconds"
                type="number"
                min="10"
                step="30"
                mono
              />
            </Field>
            <div class="flex gap-1.5">
              <button
                v-for="preset in ['180', '600', '900']"
                :key="preset"
                type="button"
                :aria-pressed="runtimeForm.nonstreamIdleSeconds === preset"
                class="rounded border px-2 py-0.5 font-mono text-2xs font-medium transition"
                :class="
                  runtimeForm.nonstreamIdleSeconds === preset
                    ? 'border-accent bg-accent/12 text-accent'
                    : 'border-line bg-surface text-subtle hover:border-line-strong hover:text-fg'
                "
                @click="runtimeForm.nonstreamIdleSeconds = preset"
              >
                {{ preset }}s
              </button>
            </div>
          </div>
        </div>

        <!-- 全局代理与调度开关 -->
        <div class="grid gap-3.5 md:grid-cols-12">
          <div class="md:col-span-6">
            <Field
              label="全局默认回退上游代理 (upstream_proxy)"
              hint="账号若绑定了独立 proxy_url 始终优先走账号独立代理；留空表示默认直连（本机 127.0.0.1 容器永远自动直连）"
            >
              <Input
                v-model="runtimeForm.upstreamProxy"
                mono
                placeholder="例如 http://127.0.0.1:7890 或 socks5://user:pass@ip:1080（留空直连）"
              />
            </Field>
          </div>

          <div class="md:col-span-3">
            <Field label="自动收割轮询间隔" hint="例如 12h、6h、24h">
              <Input
                v-model="runtimeForm.autoHarvestInterval"
                mono
                :disabled="!runtimeForm.autoHarvestEnabled"
                placeholder="12h"
              />
            </Field>
          </div>

          <div class="flex flex-col justify-center gap-2.5 rounded-md border border-line bg-raised/40 px-3.5 py-2.5 md:col-span-3">
            <Switch
              v-model="runtimeForm.emptySystemPlaceholder"
              size="sm"
              label="空 System 安全占位"
              description="无 system 时注入空白占位"
            />
            <Switch
              v-model="runtimeForm.autoHarvestEnabled"
              size="sm"
              label="后台定时自动收割"
              description="内置守护协程定期领奖"
            />
          </div>
        </div>
      </form>
    </Card>

    <!-- ==================== 管理员账户安全 + 系统只读概览 ==================== -->
    <div class="grid gap-4 lg:grid-cols-12">
      <!-- 管理员身份与鉴权管理 -->
      <div class="lg:col-span-4">
        <Card
          title="管理员账户与远程鉴权"
          subtitle="修改面板登录邮箱、密码或管理外部脚本专用 adm- 密钥"
          body-class="p-4 space-y-3.5"
        >
          <div class="panel-inset space-y-2 p-3 text-xs">
            <div class="flex items-center justify-between">
              <span class="text-subtle">当前管理员邮箱</span>
              <span class="font-mono font-medium text-fg">{{ auth.user?.email || 'admin' }}</span>
            </div>
            <div class="flex items-center justify-between">
              <span class="text-subtle">角色权限</span>
              <Badge tone="accent">{{ auth.user?.role || 'admin' }}</Badge>
            </div>
            <div v-if="auth.user?.last_login_at" class="flex items-center justify-between">
              <span class="text-subtle">最近登录</span>
              <span class="font-mono text-2xs text-muted">
                {{ formatDateTime(auth.user.last_login_at) }}
              </span>
            </div>
          </div>

          <div class="grid gap-2 sm:grid-cols-2">
            <Button variant="secondary" size="sm" block @click="emailModalOpen = true">
              <template #leading><Icon name="users" :size="13" /></template>
              修改管理员邮箱
            </Button>
            <Button variant="secondary" size="sm" block @click="passwordModalOpen = true">
              <template #leading><Icon name="lock" :size="13" /></template>
              修改登录密码
            </Button>
          </div>

          <div class="border-t border-line pt-3">
            <div class="flex items-center justify-between gap-2">
              <div class="min-w-0">
                <p class="text-xs font-medium text-fg">远程管理密钥 (Admin Keys)</p>
                <p class="mt-0.5 text-2xs text-subtle">独立签发 adm- 读写/只读管理凭证</p>
              </div>
              <RouterLink :to="{ name: 'admin-keys' }">
                <Button variant="subtle" size="xs">
                  <template #leading><Icon name="shieldKey" :size="12" /></template>
                  管理密钥
                </Button>
              </RouterLink>
            </div>
          </div>
        </Card>
      </div>

      <!-- 基础设施与环境只读概览 -->
      <div class="grid gap-4 sm:grid-cols-2 lg:col-span-8">
        <Card title="网关与存储环境" subtitle="启动配置与基础设施状态概览" body-class="px-4 py-2">
          <div v-if="loading" class="space-y-3 py-2">
            <Skeleton v-for="i in 5" :key="i" class="h-4" />
          </div>
          <dl v-else class="divide-y divide-line">
            <div
              v-for="row in envOverviewRows"
              :key="row.label"
              class="flex items-start justify-between gap-3 py-2.5"
            >
              <dt class="min-w-0">
                <span class="text-xs text-muted">{{ row.label }}</span>
                <span v-if="row.hint" class="mt-0.5 block text-2xs text-subtle">{{ row.hint }}</span>
              </dt>
              <dd class="shrink-0 text-right">
                <Badge v-if="row.tone" :tone="row.tone" :mono="row.mono">{{ row.value }}</Badge>
                <span
                  v-else
                  class="text-xs text-fg"
                  :class="row.mono ? 'font-mono text-2xs' : ''"
                >
                  {{ row.value }}
                </span>
              </dd>
            </div>
          </dl>
        </Card>

        <Card title="风控、注入与健康巡检" subtitle="在线策略快捷跳转与熔断阈值" body-class="px-4 py-2">
          <template #actions>
            <div class="flex items-center gap-1.5">
              <RouterLink :to="{ name: 'prompts' }">
                <Button variant="ghost" size="xs">提示词</Button>
              </RouterLink>
              <RouterLink :to="{ name: 'filter-rules' }">
                <Button variant="ghost" size="xs">风控词库</Button>
              </RouterLink>
              <RouterLink :to="{ name: 'models' }">
                <Button variant="ghost" size="xs">模型广场</Button>
              </RouterLink>
            </div>
          </template>

          <div v-if="loading" class="space-y-3 py-2">
            <Skeleton v-for="i in 5" :key="i" class="h-4" />
          </div>
          <dl v-else class="divide-y divide-line">
            <div
              v-for="row in securityAndHealthRows"
              :key="row.label"
              class="flex items-start justify-between gap-3 py-2.5"
            >
              <dt class="min-w-0">
                <span class="text-xs text-muted">{{ row.label }}</span>
                <span v-if="row.hint" class="mt-0.5 block text-2xs text-subtle">{{ row.hint }}</span>
              </dt>
              <dd class="shrink-0 text-right">
                <Badge v-if="row.tone" :tone="row.tone" :mono="row.mono">{{ row.value }}</Badge>
                <span
                  v-else
                  class="text-xs text-fg"
                  :class="row.mono ? 'font-mono text-2xs' : ''"
                >
                  {{ row.value }}
                </span>
              </dd>
            </div>
          </dl>
        </Card>
      </div>
    </div>

    <ChangeEmailModal v-model:open="emailModalOpen" />
    <ChangePasswordModal v-model:open="passwordModalOpen" />
  </div>
</template>
