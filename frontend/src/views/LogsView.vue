<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { accountsApi, groupsApi, keysApi, logsApi, metaApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { Account, ApiKey, Group, ModelInfo, UsageLog } from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Pagination from '@/components/ui/Pagination.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import { useTimeRange } from '@/composables/useTimeRange'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'
import {
  formatCompact,
  formatCost,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatPercent,
  formatTps,
} from '@/utils/format'
import { ALL_PLATFORMS } from '@/utils/platforms'

const toast = useToastStore()

const items = ref<UsageLog[]>([])
const total = ref(0)
const loading = ref(true)
const refreshing = ref(false)
const auxLoading = ref(false)
const error = ref('')

const page = ref(1)
const pageSize = ref(50)

const {
  mode: rangeMode,
  preset: presetHours,
  customStart,
  customEnd,
  error: rangeError,
  range,
  refresh: refreshRange,
  useCustom,
  usePreset,
} = useTimeRange(24)

const rangeTabs = [
  { label: '1h', value: '1' },
  { label: '6h', value: '6' },
  { label: '24h', value: '24' },
  { label: '7d', value: '168' },
  { label: '30d', value: '720' },
]

const platformFilter = ref('')
const groupFilter = ref('')
const statusFilter = ref('')
const modelFilter = ref('')
const requestIdFilter = ref('')
const apiKeyFilter = ref('')
const accountFilter = ref('')

const models = ref<ModelInfo[]>([])
const groups = ref<Group[]>([])
const apiKeys = ref<ApiKey[]>([])
const accounts = ref<Account[]>([])

const detailIndex = ref<number | null>(null)
const detail = computed<UsageLog | null>(() => {
  if (detailIndex.value === null) return null
  return items.value[detailIndex.value] ?? null
})

const statusOptions = [
  { label: '正常 (<400)', value: 'ok' },
  { label: '异常 (>=400)', value: 'error' },
]

const platformOptions = ALL_PLATFORMS.map((p) => ({
  label: `${p.label} (${p.shortLabel})`,
  value: p.value,
}))

const groupOptions = computed(() =>
  groups.value.map((g) => ({
    label: `${g.name} (${g.platform})`,
    value: String(g.id),
  })),
)

const keyOptions = computed(() =>
  apiKeys.value.map((k) => ({
    label: `${k.name}${k.group_name ? ` · ${k.group_name}` : ''}`,
    value: String(k.id),
  })),
)

const accountMap = computed(() => {
  const m = new Map<number, Account>()
  for (const acc of accounts.value) m.set(acc.id, acc)
  return m
})

const accountOptions = computed(() =>
  accounts.value.map((acc) => ({
    label: `${acc.name} (${acc.platform})`,
    value: String(acc.id),
  })),
)

const activeFilterCount = computed(() => {
  let count = 0
  if (platformFilter.value) count += 1
  if (groupFilter.value) count += 1
  if (statusFilter.value) count += 1
  if (modelFilter.value.trim()) count += 1
  if (requestIdFilter.value.trim()) count += 1
  if (apiKeyFilter.value) count += 1
  if (accountFilter.value) count += 1
  if (rangeMode.value === 'custom') count += 1
  return count
})

const filtersActive = computed(() => activeFilterCount.value > 0)

const pageMetrics = computed(() => {
  const list = items.value
  if (list.length === 0) return { errors: 0, avgTtft: null as number | null }
  const errors = list.filter((x) => x.status_code >= 400).length
  const ttfts = list
    .map((x) => x.first_token_ms)
    .filter((v): v is number => typeof v === 'number' && Number.isFinite(v) && v > 0)
  const avgTtft =
    ttfts.length > 0 ? ttfts.reduce((sum, v) => sum + v, 0) / ttfts.length : null
  return { errors, avgTtft }
})

function resolvePlatform(log: UsageLog): string {
  if (log.platform) return log.platform
  return accountMap.value.get(log.account_id)?.platform ?? 'workbuddy'
}

/** 识别上游链路协议（HTTP/2 多路复用 vs 本地回环 HTTP/1.1）。 */
function resolveHttpTransport(log: UsageLog): { label: string; isH2: boolean } {
  const rawProto = (log as unknown as Record<string, unknown>).http_version ??
    (log as unknown as Record<string, unknown>).upstream_proto
  if (typeof rawProto === 'string' && rawProto.trim()) {
    const upper = rawProto.trim().toUpperCase()
    return { label: upper, isH2: upper.includes('2') || upper === 'H2' }
  }
  const acc = accountMap.value.get(log.account_id)
  const baseUrl = (acc?.base_url ?? '').toLowerCase()
  if (baseUrl.startsWith('http://127.0.0.1') || baseUrl.startsWith('http://localhost')) {
    return { label: 'H1·Local', isH2: false }
  }
  return { label: 'HTTP/2', isH2: true }
}

/** 根据 User-Agent 识别下游调用客户端。 */
function detectClientLabel(ua: string | null | undefined): string {
  const raw = (ua ?? '').toLowerCase()
  if (!raw) return 'API Client'
  if (raw.includes('analy2api-console-test')) return '控制台测活'
  if (raw.includes('new-api') || raw.includes('one-api')) return 'NewAPI'
  if (raw.includes('claude-cli') || raw.includes('claude-code')) return 'Claude Code'
  if (raw.includes('codex')) return 'Codex CLI'
  if (raw.includes('antigravity') || raw.includes('agy')) return 'agy CLI'
  if (raw.includes('anthropic')) return 'Anthropic SDK'
  if (raw.includes('openai')) return 'OpenAI SDK'
  if (raw.includes('curl')) return 'cURL'
  if (raw.includes('go-http-client')) return 'Go / NewAPI'
  return 'HTTP Client'
}

/** 异常状态码智能根因诊断与排查建议。 */
function diagnoseError(log: UsageLog): { title: string; advice: string; tone: 'warning' | 'danger' } | null {
  if (log.status_code < 400 && !log.error_message) return null
  const msg = (log.error_message ?? '').toLowerCase()
  const code = log.status_code

  if (code === 401 || code === 403 || msg.includes('unauthorized') || msg.includes('invalid api key')) {
    return {
      title: '凭证鉴权或权限拒绝 (401/403)',
      advice: '建议前往对应账号池或「领奖与保活中心」执行 Token / OAuth 凭证续期，或检查分组绑定的上游账号状态。',
      tone: 'danger',
    }
  }
  if (code === 429 || msg.includes('rate limit') || msg.includes('quota') || msg.includes('too many')) {
    return {
      title: '上游速率限流或额度窗口触顶 (429)',
      advice: '当前命中账号触发上游 RPM/TPM 限流或 5h/周滚动窗口上限，网关已自动记录并切换备选账号；可补充同组并发账号或检查会话暂停保护。',
      tone: 'warning',
    }
  }
  if (code === 400 || code === 422) {
    return {
      title: '请求载荷或协议参数不兼容 (400/422)',
      advice: '请检查模型 ID 映射、上下文 Token 是否超限，或在「内容风控与词库 / 工具名混淆」中预览改写后的请求载荷。',
      tone: 'warning',
    }
  }
  if (code === 502 || code === 503 || code === 504 || msg.includes('timeout') || msg.includes('context deadline')) {
    return {
      title: '上游网络超时或出口代理断连 (5xx)',
      advice: '若为深度推理长思考请求，可在「系统与超时配置」调大首字等待超时（推荐 300s+）；若绑定了独立代理，请执行代理出口连通性探测。',
      tone: 'danger',
    }
  }
  return {
    title: `网关或上游返回异常 (HTTP ${code || 'Error'})`,
    advice: '请结合下方原始错误响应与 Request ID 定位上游节点状态。',
    tone: code >= 500 ? 'danger' : 'warning',
  }
}

function ttftClass(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms) || ms <= 0) return 'text-subtle'
  if (ms <= 1500) return 'text-success font-medium'
  if (ms <= 5000) return 'text-warning font-medium'
  return 'text-danger font-semibold'
}

function resolveTps(log: UsageLog): number | null {
  if (log.tps !== undefined && log.tps > 0) return log.tps
  if (!log.output_tokens || !log.duration_ms || log.duration_ms <= 0) return null
  const genMs =
    log.first_token_ms && log.duration_ms > log.first_token_ms
      ? log.duration_ms - log.first_token_ms
      : log.duration_ms
  if (genMs <= 0) return null
  return log.output_tokens / (genMs / 1000)
}

function resolveCacheHitRatio(log: UsageLog): number | null {
  if (log.cache_hit_rate !== undefined && log.cache_hit_rate !== null && !Number.isNaN(log.cache_hit_rate)) {
    return log.cache_hit_rate
  }
  const denom =
    (log.input_tokens || 0) + (log.cache_read_tokens || 0) + (log.cache_creation_tokens || 0)
  if (denom <= 0) return null
  return (log.cache_read_tokens || 0) / denom
}

function cacheTooltip(log: UsageLog): string {
  const read = log.cache_read_tokens || 0
  const write = log.cache_creation_tokens || 0
  const input = log.input_tokens || 0
  const totalIn = input + read + write
  if (read > 0) {
    return `缓存命中 ${formatNumber(read)} / 总输入 ${formatNumber(totalIn)} Tokens（缓存写 ${formatNumber(write)}）`
  }
  if (totalIn > 0 && totalIn < 1024) {
    return `本次总输入仅 ${formatNumber(totalIn)} Tokens；上游 KV 缓存通常需共享前缀 ≥ 1,024~2,048 Tokens 且处于多轮会话时触发`
  }
  return `缓存读取 ${formatNumber(read)} / 总输入 ${formatNumber(totalIn)} Tokens（多轮长上下文第 2 轮起自动命中粘性缓存）`
}

function costTooltip(log: UsageLog): string {
  const parts = [`总费用: $${formatCost(log.total_cost)}`, `计费倍率: ${log.rate_multiplier}x`]
  if (log.input_cost !== undefined) parts.push(`输入成本: $${formatCost(log.input_cost)}`)
  if (log.output_cost !== undefined) parts.push(`输出成本: $${formatCost(log.output_cost)}`)
  if (log.cache_cost !== undefined) parts.push(`缓存成本: $${formatCost(log.cache_cost)}`)
  return parts.join(' | ')
}

function statusTone(code: number): 'success' | 'warning' | 'danger' {
  if (code < 400) return 'success'
  if (code < 500) return 'warning'
  return 'danger'
}

let searchTimer: ReturnType<typeof setTimeout> | null = null
let loadSeq = 0

async function load(soft = false): Promise<void> {
  if (rangeError.value) return
  if (rangeMode.value === 'preset') refreshRange()
  const seq = ++loadSeq
  const keepRows = soft && items.value.length > 0
  if (keepRows) refreshing.value = true
  else loading.value = true
  error.value = ''

  try {
    const response = await logsApi.list({
      page: page.value,
      page_size: pageSize.value,
      start: range.value.start,
      end: range.value.end,
      platform: platformFilter.value || undefined,
      group_id: groupFilter.value ? Number(groupFilter.value) : undefined,
      status: (statusFilter.value || undefined) as 'ok' | 'error' | undefined,
      model: modelFilter.value.trim() || undefined,
      request_id: requestIdFilter.value.trim() || undefined,
      api_key_id: apiKeyFilter.value ? Number(apiKeyFilter.value) : undefined,
      account_id: accountFilter.value ? Number(accountFilter.value) : undefined,
    })
    if (seq !== loadSeq) return
    items.value = response.items ?? []
    total.value = response.total ?? 0
    if (detailIndex.value !== null && detailIndex.value >= items.value.length) {
      detailIndex.value = null
    }
  } catch (err) {
    if (seq !== loadSeq) return
    error.value = toMessage(err, '加载调用日志失败')
    if (!keepRows) {
      items.value = []
      total.value = 0
    }
  } finally {
    if (seq === loadSeq) {
      loading.value = false
      refreshing.value = false
    }
  }
}

function applyFilters(): void {
  page.value = 1
  void load(true)
}

function selectPreset(val: string): void {
  presetHours.value = val
  if (rangeMode.value !== 'preset') usePreset()
  applyFilters()
}

async function loadAuxiliary(): Promise<void> {
  auxLoading.value = true
  try {
    const [keysRes, accRes, grpRes, modRes] = await Promise.all([
      keysApi.list().catch(() => ({ items: [] as ApiKey[], total: 0 })),
      accountsApi.list({ page: 1, page_size: 200 }).catch(() => ({
        items: [] as Account[],
        total: 0,
        page: 1,
        page_size: 200,
      })),
      groupsApi.list().catch(() => ({ items: [] as Group[], total: 0 })),
      metaApi.models().catch(() => ({ items: [] as ModelInfo[] })),
    ])
    apiKeys.value = keysRes.items ?? []
    accounts.value = accRes.items ?? []
    groups.value = grpRes.items ?? []
    models.value = modRes.items ?? []
  } finally {
    auxLoading.value = false
  }
}

function onDrawerKeydown(event: KeyboardEvent): void {
  if (detailIndex.value === null) return
  if (event.key === 'Escape') {
    event.preventDefault()
    detailIndex.value = null
  } else if (event.key === 'ArrowUp' || event.key === 'ArrowLeft') {
    if (detailIndex.value > 0) {
      event.preventDefault()
      detailIndex.value -= 1
    }
  } else if (event.key === 'ArrowDown' || event.key === 'ArrowRight') {
    if (detailIndex.value < items.value.length - 1) {
      event.preventDefault()
      detailIndex.value += 1
    }
  }
}

onMounted(() => {
  void load(false)
  void loadAuxiliary()
  document.addEventListener('keydown', onDrawerKeydown)
})

watch([platformFilter, groupFilter, statusFilter, apiKeyFilter, accountFilter], () => {
  applyFilters()
})

watch([page, pageSize], () => {
  void load(true)
})

watch([requestIdFilter, modelFilter], () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(applyFilters, 320)
})

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
  document.removeEventListener('keydown', onDrawerKeydown)
})

function resetFilters(): void {
  platformFilter.value = ''
  groupFilter.value = ''
  statusFilter.value = ''
  modelFilter.value = ''
  requestIdFilter.value = ''
  apiKeyFilter.value = ''
  accountFilter.value = ''
  usePreset()
  presetHours.value = '24'
  page.value = 1
  void load(true)
}

async function copyReqId(reqId: string): Promise<void> {
  if (!reqId) return
  const ok = await copyText(reqId)
  if (ok) toast.success('已复制 Request ID', reqId)
}

async function copyLogJson(log: UsageLog): Promise<void> {
  const ok = await copyText(JSON.stringify(log, null, 2))
  if (ok) toast.success('已复制完整审计 JSON', `#${log.id}`)
}
</script>

<template>
  <div class="space-y-4">
    <!-- 多维组合筛选与审计工作栏 -->
    <Card
      title="调用明细多维检索"
      subtitle="支持按时间窗口、上游平台、路由分组、密钥、账号、模型与 Request ID 联合过滤，点击任意记录展开右侧链路诊断抽屉"
      :loading="refreshing"
      body-class="p-3.5 space-y-3"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <Badge v-if="items.length > 0" tone="neutral" mono>
            本页均首字 {{ pageMetrics.avgTtft ? formatDuration(pageMetrics.avgTtft) : '—' }}
          </Badge>
          <Badge v-if="pageMetrics.errors > 0" tone="danger" dot>
            本页异常 {{ pageMetrics.errors }} 条
          </Badge>
          <Button v-if="filtersActive" variant="ghost" size="sm" @click="resetFilters">
            重置筛选 ({{ activeFilterCount }})
          </Button>
          <Button variant="secondary" size="sm" :loading="loading || refreshing" @click="load(true)">
            <template #leading><Icon name="refresh" :size="13" /></template>
            刷新
          </Button>
          <Button variant="primary" size="sm" :loading="loading || refreshing" @click="applyFilters">
            <template #leading><Icon name="search" :size="13" /></template>
            查询日志
          </Button>
        </div>
      </template>

      <!-- 第一层：快捷时间段切换 + 精确搜索 -->
      <div class="flex flex-wrap items-center justify-between gap-2.5">
        <div class="flex flex-wrap items-center gap-2">
          <SegmentedTabs
            :model-value="rangeMode === 'preset' ? presetHours : ''"
            :items="rangeTabs"
            size="sm"
            @update:model-value="selectPreset"
          />
          <Button
            v-if="rangeMode !== 'custom'"
            variant="ghost"
            size="sm"
            @click="useCustom(Number(presetHours) || 24)"
          >
            <template #leading><Icon name="clock" :size="13" /></template>
            自定义区间
          </Button>
          <Button v-else variant="subtle" size="sm" @click="selectPreset(presetHours)">
            返回预设时段
          </Button>
        </div>

        <div class="grid w-full gap-2 sm:w-auto sm:grid-cols-2">
          <div class="sm:w-56">
            <Input
              v-model="modelFilter"
              size="sm"
              mono
              list="log-model-datalist"
              placeholder="模型过滤：如 claude-sonnet-4-6"
              @keyup.enter="applyFilters"
            />
            <datalist id="log-model-datalist">
              <option v-for="m in models" :key="m.id" :value="m.id">{{ m.name }}</option>
            </datalist>
          </div>
          <div class="sm:w-60">
            <Input
              v-model="requestIdFilter"
              size="sm"
              mono
              placeholder="Request ID 精确追踪…"
              @keyup.enter="applyFilters"
            />
          </div>
        </div>
      </div>

      <!-- 自定义起止时间展开栏 -->
      <div
        v-if="rangeMode === 'custom'"
        class="flex flex-wrap items-end gap-2.5 rounded-md border border-line bg-raised/40 p-2.5"
      >
        <div class="w-52">
          <Field label="开始时间">
            <Input v-model="customStart" size="sm" type="datetime-local" />
          </Field>
        </div>
        <div class="w-52">
          <Field label="结束时间" :error="rangeError">
            <Input
              v-model="customEnd"
              size="sm"
              type="datetime-local"
              :invalid="Boolean(rangeError)"
            />
          </Field>
        </div>
        <Button variant="primary" size="sm" :disabled="Boolean(rangeError)" @click="applyFilters">
          应用区间
        </Button>
      </div>

      <!-- 第二层：整齐五列维度选择器 -->
      <div class="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-5">
        <Field label="上游平台">
          <Select
            v-model="platformFilter"
            size="sm"
            :options="platformOptions"
            placeholder="全部平台"
          />
        </Field>

        <Field label="路由分组">
          <Select
            v-model="groupFilter"
            size="sm"
            :loading="auxLoading"
            :options="groupOptions"
            placeholder="全部分组"
            empty-text="暂无分组"
          />
        </Field>

        <Field label="响应状态">
          <Select
            v-model="statusFilter"
            size="sm"
            :options="statusOptions"
            placeholder="全部状态"
          />
        </Field>

        <Field label="下游调用密钥">
          <Select
            v-model="apiKeyFilter"
            size="sm"
            :loading="auxLoading"
            :options="keyOptions"
            placeholder="全部密钥"
            empty-text="暂无密钥"
          />
        </Field>

        <Field label="命中上游账号">
          <Select
            v-model="accountFilter"
            size="sm"
            :loading="auxLoading"
            :options="accountOptions"
            placeholder="全部账号"
            empty-text="暂无账号"
          />
        </Field>
      </div>
    </Card>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="load(false)">重试</Button>
    </p>

    <!-- 11 列高密度调用审计表 -->
    <DataTable
      :columns="11"
      :loading="loading"
      :refreshing="refreshing"
      :empty="items.length === 0"
      :skeleton-rows="10"
      min-width="1460px"
      hoverable
    >
      <template #head>
        <th class="th">时间</th>
        <th class="th">平台</th>
        <th class="th">请求模型 &amp; 传输协议</th>
        <th class="th">命中账号 / 分组 / Key</th>
        <th class="th text-right">首字 TTFT</th>
        <th class="th text-right">总耗时</th>
        <th class="th text-right">吞吐 TPS</th>
        <th class="th text-right">Tokens (入 / 出 / 缓读 / 推理)</th>
        <th class="th text-right">缓存率</th>
        <th class="th text-right">折算费用</th>
        <th class="th">状态与诊断</th>
      </template>

      <template #empty>
        <EmptyState
          icon="activity"
          :title="filtersActive ? '未找到符合筛选条件的调用记录' : '当前时间窗口暂无调用明细'"
          :description="
            filtersActive
              ? '请放宽时间跨度或点击「重置筛选」。'
              : '下游客户端通过 /v1/messages、/v1/chat/completions 或 /v1/responses 发起调用后，此处将实时记录完整性能与计费指标。'
          "
        >
          <Button v-if="filtersActive" variant="secondary" size="sm" @click="resetFilters">
            重置筛选
          </Button>
        </EmptyState>
      </template>

      <template #body>
        <tr
          v-for="(log, idx) in items"
          :key="log.id"
          class="cursor-pointer"
          :class="[
            detailIndex === idx ? 'bg-accent/10' : log.status_code >= 400 ? 'bg-danger/[0.03]' : '',
          ]"
          @click="detailIndex = idx"
        >
          <td class="td tnum font-mono text-2xs text-muted">
            {{ formatDateTime(log.created_at) }}
          </td>

          <td class="td">
            <PlatformBadge :platform="resolvePlatform(log)" short />
          </td>

          <td class="td max-w-[16rem]">
            <div class="flex items-center gap-1.5">
              <span class="truncate font-mono text-xs font-medium text-fg" :title="log.model">
                {{ log.model || '—' }}
              </span>
            </div>
            <div class="mt-0.5 flex items-center gap-1">
              <span
                class="rounded border px-1 py-px font-mono text-[10px] leading-3"
                :class="
                  resolveHttpTransport(log).isH2
                    ? 'border-accent/30 bg-accent/10 text-accent'
                    : 'border-line bg-raised text-subtle'
                "
              >
                {{ resolveHttpTransport(log).label }}
              </span>
              <span
                class="rounded border px-1 py-px font-mono text-[10px] leading-3"
                :class="
                  log.stream
                    ? 'border-info/30 bg-info/10 text-info'
                    : 'border-line bg-raised text-subtle'
                "
              >
                {{ log.stream ? 'SSE 流式' : 'Sync 同步' }}
              </span>
              <span class="truncate text-[10.5px] text-subtle">
                · {{ detectClientLabel(log.user_agent) }}
              </span>
            </div>
          </td>

          <td class="td max-w-[15rem]">
            <div class="truncate text-xs font-medium text-fg">
              {{ log.account_name || `#${log.account_id}` }}
              <span v-if="log.group_name" class="font-normal text-2xs text-subtle">
                · {{ log.group_name }}
              </span>
            </div>
            <div class="truncate text-2xs text-subtle">
              Key: {{ log.api_key_name || `#${log.api_key_id}` }}
            </div>
          </td>

          <td class="td tnum text-right font-mono text-xs" :class="ttftClass(log.first_token_ms)">
            {{ formatDuration(log.first_token_ms) }}
          </td>

          <td class="td tnum text-right font-mono text-xs text-muted">
            {{ formatDuration(log.duration_ms) }}
          </td>

          <td class="td tnum text-right font-mono text-xs text-fg">
            {{ formatTps(resolveTps(log)) }}
          </td>

          <td class="td tnum text-right font-mono text-2xs">
            <span class="text-fg" title="输入 Tokens">{{ formatCompact(log.input_tokens) }}</span>
            <span class="text-subtle"> / </span>
            <span class="text-fg" title="输出 Tokens">{{ formatCompact(log.output_tokens) }}</span>
            <span class="text-subtle"> / </span>
            <span
              :class="log.cache_read_tokens > 0 ? 'font-medium text-success' : 'text-subtle'"
              title="缓存读取 Tokens"
            >
              {{ formatCompact(log.cache_read_tokens) }}
            </span>
            <span class="text-subtle"> / </span>
            <span
              :class="(log.reasoning_tokens ?? 0) > 0 ? 'font-medium text-accent' : 'text-subtle'"
              title="思维链推理 Tokens"
            >
              {{ formatCompact(log.reasoning_tokens ?? 0) }}
            </span>
          </td>

          <td class="td tnum text-right font-mono text-xs" :title="cacheTooltip(log)">
            <span
              :class="
                (resolveCacheHitRatio(log) ?? 0) > 0.2 ? 'font-medium text-success' : 'text-muted'
              "
            >
              {{ formatPercent(resolveCacheHitRatio(log)) }}
            </span>
          </td>

          <td
            class="td tnum text-right font-mono text-xs font-medium text-fg"
            :title="costTooltip(log)"
          >
            ${{ formatCost(log.total_cost) }}
          </td>

          <td class="td max-w-[12rem]">
            <div class="flex items-center gap-1.5">
              <Badge :tone="statusTone(log.status_code)" dot mono>
                {{ log.status_code }}
              </Badge>
              <Icon
                v-if="log.error_message"
                name="alertCircle"
                :size="13"
                class="shrink-0 text-danger"
                :title="log.error_message"
              />
            </div>
            <p
              v-if="log.error_message"
              class="mt-0.5 max-w-[10.5rem] truncate text-[10.5px] text-danger"
              :title="log.error_message"
            >
              {{ log.error_message }}
            </p>
          </td>
        </tr>
      </template>

      <template #footer>
        <Pagination v-model:page="page" v-model:page-size="pageSize" :total="total" />
      </template>
    </DataTable>

    <p class="px-1 text-2xs text-subtle">
      共 {{ formatNumber(total) }} 条调用明细 · 点击任意行打开右侧详情诊断抽屉（支持方向键切换上/下一条记录）。
    </p>

    <!-- 右侧滑出式完整调用链路与错误诊断抽屉 -->
    <Teleport to="body">
      <Transition name="fade">
        <div
          v-if="detail"
          class="fixed inset-0 z-50 bg-black/55 backdrop-blur-[2px]"
          aria-hidden="true"
          @click="detailIndex = null"
        />
      </Transition>

      <Transition name="drawer">
        <aside
          v-if="detail"
          role="dialog"
          aria-modal="true"
          aria-label="调用链路与错误诊断抽屉"
          class="fixed inset-y-0 right-0 z-[55] flex w-full max-w-2xl flex-col border-l border-line bg-surface shadow-pop"
        >
          <!-- 抽屉顶栏 -->
          <header class="flex shrink-0 items-start justify-between gap-3 border-b border-line bg-surface px-4 py-3.5">
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <h2 class="text-sm font-semibold text-fg">调用链路与诊断详情</h2>
                <Badge tone="neutral" mono>#{{ detail.id }}</Badge>
                <span class="font-mono text-2xs text-subtle">
                  {{ formatDateTime(detail.created_at) }}
                </span>
              </div>
              <div class="mt-1.5 flex flex-wrap items-center gap-1.5">
                <Badge :tone="statusTone(detail.status_code)" dot mono>
                  HTTP {{ detail.status_code }}
                </Badge>
                <PlatformBadge :platform="resolvePlatform(detail)" />
                <Badge :tone="resolveHttpTransport(detail).isH2 ? 'accent' : 'neutral'" mono>
                  {{ resolveHttpTransport(detail).label }}
                </Badge>
                <Badge :tone="detail.stream ? 'info' : 'neutral'" mono>
                  {{ detail.stream ? 'SSE 流式' : 'Sync 同步' }}
                </Badge>
                <Badge tone="neutral" mono>{{ detail.model || '—' }}</Badge>
              </div>
            </div>

            <div class="flex shrink-0 items-center gap-1">
              <Button
                variant="secondary"
                size="xs"
                icon
                title="上一条记录 (↑)"
                :disabled="detailIndex === null || detailIndex <= 0"
                @click="detailIndex !== null && detailIndex > 0 && (detailIndex -= 1)"
              >
                <Icon name="chevronLeft" :size="14" />
              </Button>
              <Button
                variant="secondary"
                size="xs"
                icon
                title="下一条记录 (↓)"
                :disabled="detailIndex === null || detailIndex >= items.length - 1"
                @click="
                  detailIndex !== null && detailIndex < items.length - 1 && (detailIndex += 1)
                "
              >
                <Icon name="chevronRight" :size="14" />
              </Button>
              <Button
                variant="ghost"
                size="xs"
                icon
                title="关闭抽屉 (Esc)"
                @click="detailIndex = null"
              >
                <Icon name="close" :size="15" />
              </Button>
            </div>
          </header>

          <!-- 抽屉主体 -->
          <div class="scroll-thin min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
            <!-- Request ID 条 -->
            <div class="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-raised/55 px-3 py-2">
              <div class="min-w-0 flex-1">
                <span class="text-2xs text-subtle">Request ID：</span>
                <code class="ml-1 break-all font-mono text-xs text-fg">
                  {{ detail.request_id || '未生成 request_id' }}
                </code>
              </div>
              <div class="flex shrink-0 items-center gap-1.5">
                <Button
                  v-if="detail.request_id"
                  variant="secondary"
                  size="xs"
                  @click="copyReqId(detail.request_id)"
                >
                  <template #leading><Icon name="copy" :size="11" /></template>
                  复制 ID
                </Button>
                <Button variant="ghost" size="xs" @click="copyLogJson(detail)">
                  复制 JSON
                </Button>
              </div>
            </div>

            <!-- 智能异常诊断面板（仅当 >=400 或存在 error_message 时展示） -->
            <div
              v-if="diagnoseError(detail)"
              class="rounded-md border p-3.5"
              :class="
                diagnoseError(detail)?.tone === 'danger'
                  ? 'border-danger/35 bg-danger/10'
                  : 'border-warning/35 bg-warning/10'
              "
            >
              <div class="flex items-start gap-2.5">
                <Icon
                  name="alertCircle"
                  :size="16"
                  class="mt-0.5 shrink-0"
                  :class="
                    diagnoseError(detail)?.tone === 'danger' ? 'text-danger' : 'text-warning'
                  "
                />
                <div class="min-w-0 flex-1">
                  <p
                    class="text-xs font-semibold"
                    :class="
                      diagnoseError(detail)?.tone === 'danger' ? 'text-danger' : 'text-warning'
                    "
                  >
                    {{ diagnoseError(detail)?.title }}
                  </p>
                  <p class="mt-1 text-2xs leading-relaxed text-fg/90">
                    {{ diagnoseError(detail)?.advice }}
                  </p>
                </div>
              </div>
            </div>

            <!-- 错误原文展示 -->
            <CodeBlock
              v-if="detail.error_message"
              title="上游 / 网关错误响应原文 (error_message)"
              language="error"
              max-height="14rem"
              :code="detail.error_message"
            />

            <!-- 性能 / Token / 费用 三宫格拆解 -->
            <div class="grid gap-3 sm:grid-cols-3">
              <div class="panel-inset space-y-1.5 p-3">
                <p class="text-2xs font-semibold uppercase tracking-wider text-subtle">
                  延迟与传输表现
                </p>
                <div class="flex justify-between text-xs">
                  <span class="text-muted">首字响应 (TTFT)</span>
                  <span class="tnum font-mono" :class="ttftClass(detail.first_token_ms)">
                    {{ formatDuration(detail.first_token_ms) }}
                  </span>
                </div>
                <div class="flex justify-between text-xs">
                  <span class="text-muted">端到端总耗时</span>
                  <span class="tnum font-mono text-fg">{{ formatDuration(detail.duration_ms) }}</span>
                </div>
                <div class="flex justify-between text-xs">
                  <span class="text-muted">输出吞吐速率</span>
                  <span class="tnum font-mono text-accent">{{ formatTps(resolveTps(detail)) }}</span>
                </div>
              </div>

              <div class="panel-inset space-y-1.5 p-3">
                <p class="text-2xs font-semibold uppercase tracking-wider text-subtle">
                  Token 消耗明细
                </p>
                <div class="flex justify-between text-xs">
                  <span class="text-muted">输入 / 输出</span>
                  <span class="tnum font-mono text-fg">
                    {{ formatNumber(detail.input_tokens) }} / {{ formatNumber(detail.output_tokens) }}
                  </span>
                </div>
                <div class="flex justify-between text-xs">
                  <span class="text-muted">缓存读 / 缓存写</span>
                  <span class="tnum font-mono text-success">
                    {{ formatNumber(detail.cache_read_tokens) }} /
                    {{ formatNumber(detail.cache_creation_tokens) }}
                  </span>
                </div>
                <div class="flex justify-between text-xs">
                  <span class="text-muted">推理 / 命中率</span>
                  <span class="tnum font-mono text-fg">
                    {{ formatNumber(detail.reasoning_tokens ?? 0) }} ·
                    {{ formatPercent(resolveCacheHitRatio(detail)) }}
                  </span>
                </div>
              </div>

              <div class="panel-inset space-y-1.5 p-3">
                <p class="text-2xs font-semibold uppercase tracking-wider text-subtle">
                  计费与倍率拆解
                </p>
                <div class="flex justify-between text-xs">
                  <span class="text-muted">折算总费用</span>
                  <span class="tnum font-mono font-semibold text-fg">
                    ${{ formatCost(detail.total_cost) }}
                  </span>
                </div>
                <div class="flex justify-between text-2xs">
                  <span class="text-muted">入 / 出 / 缓</span>
                  <span class="tnum font-mono text-muted">
                    ${{ formatCost(detail.input_cost ?? 0) }} / ${{
                      formatCost(detail.output_cost ?? 0)
                    }}
                    / ${{ formatCost(detail.cache_cost ?? 0) }}
                  </span>
                </div>
                <div class="flex justify-between text-2xs">
                  <span class="text-muted">分组倍率</span>
                  <span class="tnum font-mono text-fg">{{ detail.rate_multiplier }}x</span>
                </div>
              </div>
            </div>

            <!-- 路由上下文与客户端环境 -->
            <dl
              class="grid gap-x-6 gap-y-2.5 rounded-md border border-line bg-surface p-3.5 text-xs sm:grid-cols-2"
            >
              <div class="flex justify-between gap-3 border-b border-line/65 pb-1.5">
                <dt class="text-subtle">上游命中账号</dt>
                <dd class="truncate font-medium text-fg">
                  {{ detail.account_name || `#${detail.account_id}` }} (#{{ detail.account_id }})
                </dd>
              </div>
              <div class="flex justify-between gap-3 border-b border-line/65 pb-1.5">
                <dt class="text-subtle">所属路由分组</dt>
                <dd class="truncate text-fg">
                  {{ detail.group_name || (detail.group_id != null ? `#${detail.group_id}` : '默认未分组') }}
                </dd>
              </div>
              <div class="flex justify-between gap-3 border-b border-line/65 pb-1.5">
                <dt class="text-subtle">下游调用密钥</dt>
                <dd class="truncate text-fg">
                  {{ detail.api_key_name || `#${detail.api_key_id}` }} (#{{ detail.api_key_id }})
                </dd>
              </div>
              <div class="flex justify-between gap-3 border-b border-line/65 pb-1.5">
                <dt class="text-subtle">客户端来源 IP</dt>
                <dd class="font-mono text-fg">{{ detail.ip_address || '—' }}</dd>
              </div>
              <div class="flex justify-between gap-3 border-b border-line/65 pb-1.5">
                <dt class="text-subtle">客户端类型</dt>
                <dd class="font-medium text-fg">{{ detectClientLabel(detail.user_agent) }}</dd>
              </div>
              <div class="flex justify-between gap-3 border-b border-line/65 pb-1.5">
                <dt class="text-subtle">传输模式</dt>
                <dd class="font-mono text-fg">
                  {{ resolveHttpTransport(detail).label }} · {{ detail.stream ? 'SSE' : 'JSON' }}
                </dd>
              </div>
              <div class="flex flex-col gap-1 pt-0.5 sm:col-span-2">
                <dt class="text-subtle">客户端 User-Agent</dt>
                <dd
                  class="break-all rounded border border-line bg-raised/60 px-2.5 py-1.5 font-mono text-2xs text-muted"
                >
                  {{ detail.user_agent || '—' }}
                </dd>
              </div>
            </dl>
          </div>

          <!-- 抽屉底栏 -->
          <footer
            class="flex shrink-0 items-center justify-between gap-2 border-t border-line bg-raised/45 px-4 py-3"
          >
            <span class="tnum text-2xs text-subtle">
              第 {{ (detailIndex ?? 0) + 1 }} / {{ items.length }} 条（本页）
            </span>
            <div class="flex items-center gap-2">
              <Button
                v-if="detail.model"
                variant="secondary"
                size="sm"
                @click="
                  () => {
                    modelFilter = detail?.model ?? ''
                    detailIndex = null
                    applyFilters()
                  }
                "
              >
                仅看该模型
              </Button>
              <Button variant="primary" size="sm" @click="detailIndex = null">关闭</Button>
            </div>
          </footer>
        </aside>
      </Transition>
    </Teleport>
  </div>
</template>
