<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { accountsApi, keysApi, logsApi, metaApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { Account, ApiKey, ModelInfo, UsageLog } from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import Pagination from '@/components/ui/Pagination.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
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
const error = ref('')

const page = ref(1)
const pageSize = ref(50)

const {
  mode: rangeMode,
  preset: presetHours,
  presetOptions,
  customStart,
  customEnd,
  error: rangeError,
  range,
  useCustom,
  usePreset,
} = useTimeRange(24)

const platformFilter = ref('')
const statusFilter = ref('')
const modelFilter = ref('')
const requestIdFilter = ref('')
const apiKeyFilter = ref('')
const accountFilter = ref('')

const models = ref<ModelInfo[]>([])
const apiKeys = ref<ApiKey[]>([])
const accounts = ref<Account[]>([])

const detail = ref<UsageLog | null>(null)

const statusOptions = [
  { label: '正常 (<400)', value: 'ok' },
  { label: '异常 (>=400)', value: 'error' },
]

const platformOptions = ALL_PLATFORMS.map((p) => ({
  label: `${p.label} (${p.shortLabel})`,
  value: p.value,
}))

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

const filtersActive = computed(
  () =>
    Boolean(platformFilter.value) ||
    Boolean(statusFilter.value) ||
    Boolean(modelFilter.value.trim()) ||
    Boolean(requestIdFilter.value.trim()) ||
    Boolean(apiKeyFilter.value) ||
    Boolean(accountFilter.value),
)

function resolvePlatform(log: UsageLog): string {
  if (log.platform) return log.platform
  return accountMap.value.get(log.account_id)?.platform ?? 'workbuddy'
}

function ttftClass(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return 'text-subtle'
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
  if (log.cache_hit_rate !== undefined && log.cache_hit_rate !== null) {
    return log.cache_hit_rate
  }
  const denom =
    (log.input_tokens || 0) + (log.cache_read_tokens || 0) + (log.cache_creation_tokens || 0)
  if (denom <= 0) return null
  return (log.cache_read_tokens || 0) / denom
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

async function load(): Promise<void> {
  if (rangeError.value) return
  loading.value = true
  error.value = ''
  try {
    const response = await logsApi.list({
      page: page.value,
      page_size: pageSize.value,
      start: range.value.start,
      end: range.value.end,
      platform: platformFilter.value || undefined,
      status: (statusFilter.value || undefined) as 'ok' | 'error' | undefined,
      model: modelFilter.value.trim() || undefined,
      request_id: requestIdFilter.value.trim() || undefined,
      api_key_id: apiKeyFilter.value ? Number(apiKeyFilter.value) : undefined,
      account_id: accountFilter.value ? Number(accountFilter.value) : undefined,
    })
    items.value = response.items ?? []
    total.value = response.total ?? 0
  } catch (err) {
    error.value = toMessage(err, '加载调用日志失败')
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function applyFilters(): void {
  page.value = 1
  void load()
}

async function loadAuxiliary(): Promise<void> {
  try {
    const res = await keysApi.list()
    apiKeys.value = res.items ?? []
  } catch {
    apiKeys.value = []
  }
  try {
    const res = await accountsApi.list({ page: 1, page_size: 200 })
    accounts.value = res.items ?? []
  } catch {
    accounts.value = []
  }
  try {
    const res = await metaApi.models()
    models.value = res.items ?? []
  } catch {
    models.value = []
  }
}

onMounted(() => {
  void load()
  void loadAuxiliary()
})

watch([platformFilter, statusFilter, apiKeyFilter, accountFilter, presetHours], () => {
  if (rangeMode.value === 'preset') applyFilters()
})

watch([page, pageSize], () => {
  void load()
})

watch(requestIdFilter, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(applyFilters, 350)
})

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
})

function resetFilters(): void {
  platformFilter.value = ''
  statusFilter.value = ''
  modelFilter.value = ''
  requestIdFilter.value = ''
  apiKeyFilter.value = ''
  accountFilter.value = ''
  usePreset()
  presetHours.value = '24'
  page.value = 1
  void load()
}

function backToPreset(): void {
  usePreset()
  applyFilters()
}

async function copyReqId(reqId: string): Promise<void> {
  if (!reqId) return
  const ok = await copyText(reqId)
  if (ok) toast.success('已复制 Request ID', reqId)
}
</script>

<template>
  <div class="space-y-3.5">
    <!-- 高密度多维筛选面板 -->
    <div class="card space-y-3 p-3.5">
      <div class="flex flex-wrap items-end gap-2.5">
        <div class="w-36">
          <Field label="时间跨度">
            <Select
              v-model="presetHours"
              size="sm"
              :options="presetOptions"
              :disabled="rangeMode === 'custom'"
            />
          </Field>
        </div>

        <div class="pb-0.5">
          <Button v-if="rangeMode === 'custom'" variant="subtle" size="sm" @click="backToPreset">
            预设时间
          </Button>
          <Button v-else variant="ghost" size="sm" @click="useCustom(24)">
            <template #leading><Icon name="clock" :size="13" /></template>
            自定义区间
          </Button>
        </div>

        <div class="w-40">
          <Field label="上游平台">
            <Select
              v-model="platformFilter"
              size="sm"
              :options="platformOptions"
              placeholder="全部平台"
            />
          </Field>
        </div>

        <div class="w-36">
          <Field label="响应状态">
            <Select v-model="statusFilter" size="sm" :options="statusOptions" placeholder="全部状态" />
          </Field>
        </div>

        <div class="w-44">
          <Field label="下游密钥">
            <Select v-model="apiKeyFilter" size="sm" :options="keyOptions" placeholder="全部密钥" />
          </Field>
        </div>

        <div class="w-48">
          <Field label="上游账号">
            <Select
              v-model="accountFilter"
              size="sm"
              :options="accountOptions"
              placeholder="全部账号"
            />
          </Field>
        </div>
      </div>

      <div v-if="rangeMode === 'custom'" class="flex flex-wrap items-end gap-2.5 border-t border-line/60 pt-2.5">
        <div class="w-52">
          <Field label="开始时间">
            <Input v-model="customStart" size="sm" type="datetime-local" />
          </Field>
        </div>
        <div class="w-52">
          <Field label="结束时间" :error="rangeError">
            <Input v-model="customEnd" size="sm" type="datetime-local" />
          </Field>
        </div>
        <div class="pb-0.5">
          <Button variant="primary" size="sm" :disabled="Boolean(rangeError)" @click="applyFilters">
            应用区间
          </Button>
        </div>
      </div>

      <div class="flex flex-wrap items-end gap-2.5">
        <div class="w-full sm:w-60">
          <Field label="模型名称">
            <Input
              v-model="modelFilter"
              size="sm"
              mono
              list="log-model-datalist"
              placeholder="例如 claude-sonnet-4-6"
              @keyup.enter="applyFilters"
            />
            <datalist id="log-model-datalist">
              <option v-for="m in models" :key="m.id" :value="m.id">{{ m.name }}</option>
            </datalist>
          </Field>
        </div>

        <div class="w-full sm:w-68">
          <Field label="Request ID 追踪">
            <Input
              v-model="requestIdFilter"
              size="sm"
              mono
              placeholder="输入 request_id 精确定位"
              @keyup.enter="applyFilters"
            />
          </Field>
        </div>

        <div class="ml-auto flex items-center gap-2 pb-0.5">
          <Button v-if="filtersActive" variant="ghost" size="sm" @click="resetFilters">
            重置筛选
          </Button>
          <Button variant="secondary" size="sm" :loading="loading" @click="load">
            <template #leading><Icon name="refresh" :size="13" /></template>
            刷新
          </Button>
          <Button variant="primary" size="sm" :loading="loading" @click="applyFilters">
            <template #leading><Icon name="search" :size="13" /></template>
            查询日志
          </Button>
        </div>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="load">重试</Button>
    </p>

    <!-- 11 列高密度调用审计表 -->
    <DataTable
      :columns="11"
      :loading="loading"
      :empty="items.length === 0"
      :skeleton-rows="10"
      min-width="1440px"
      hoverable
    >
      <template #head>
        <th class="th">时间</th>
        <th class="th">平台</th>
        <th class="th">请求模型</th>
        <th class="th">账号 / 分组 / Key</th>
        <th class="th text-right">首字 TTFT</th>
        <th class="th text-right">总用时</th>
        <th class="th text-right">吞吐 TPS</th>
        <th class="th text-right">Tokens (入 / 出 / 缓读 / 推理)</th>
        <th class="th text-right">缓存命中率</th>
        <th class="th text-right">总费用 ($)</th>
        <th class="th">状态码</th>
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
          v-for="log in items"
          :key="log.id"
          class="cursor-pointer"
          @click="detail = log"
        >
          <td class="td tnum font-mono text-2xs text-muted">
            {{ formatDateTime(log.created_at) }}
          </td>

          <td class="td">
            <PlatformBadge :platform="resolvePlatform(log)" short />
          </td>

          <td class="td max-w-[14rem]">
            <div class="flex items-center gap-1.5">
              <span class="truncate font-mono text-xs font-medium text-fg" :title="log.model">
                {{ log.model || '—' }}
              </span>
              <span
                class="rounded border px-1 py-px text-[10px] leading-3"
                :class="
                  log.stream
                    ? 'border-info/30 bg-info/10 text-info'
                    : 'border-line bg-raised text-subtle'
                "
              >
                {{ log.stream ? 'SSE' : 'Sync' }}
              </span>
            </div>
          </td>

          <td class="td max-w-[15rem]">
            <div class="truncate text-xs text-fg">
              {{ log.account_name || `#${log.account_id}` }}
              <span v-if="log.group_name" class="text-2xs text-subtle">· {{ log.group_name }}</span>
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
              :class="log.cache_read_tokens > 0 ? 'text-success font-medium' : 'text-subtle'"
              title="缓存读取 Tokens"
            >
              {{ formatCompact(log.cache_read_tokens) }}
            </span>
            <span class="text-subtle"> / </span>
            <span
              :class="(log.reasoning_tokens ?? 0) > 0 ? 'text-accent font-medium' : 'text-subtle'"
              title="思维链推理 Tokens"
            >
              {{ formatCompact(log.reasoning_tokens ?? 0) }}
            </span>
          </td>

          <td class="td tnum text-right font-mono text-xs">
            <span
              :class="
                (resolveCacheHitRatio(log) ?? 0) > 0.2
                  ? 'text-success font-medium'
                  : 'text-muted'
              "
            >
              {{ formatPercent(resolveCacheHitRatio(log)) }}
            </span>
          </td>

          <td class="td tnum text-right font-mono text-xs font-medium text-fg" :title="costTooltip(log)">
            ${{ formatCost(log.total_cost) }}
          </td>

          <td class="td">
            <Badge :tone="statusTone(log.status_code)" dot mono>
              {{ log.status_code }}
            </Badge>
          </td>
        </tr>
      </template>

      <template #footer>
        <Pagination v-model:page="page" v-model:page-size="pageSize" :total="total" />
      </template>
    </DataTable>

    <p class="px-1 text-2xs text-subtle">
      共 {{ formatNumber(total) }} 条调用明细 · 悬停费用列可速览输入/输出/缓存成本拆分，点击任意行展开完整链路审计面板。
    </p>

    <!-- 单条调用完整审计详情弹窗 -->
    <Modal
      :open="detail !== null"
      size="xl"
      title="调用链路与计费审计详情"
      :subtitle="detail ? `记录 #${detail.id} · ${formatDateTime(detail.created_at)}` : ''"
      @update:open="detail = null"
    >
      <div v-if="detail" class="space-y-4">
        <!-- 顶部核心摘要条 -->
        <div class="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-raised/50 px-3.5 py-2.5">
          <div class="flex flex-wrap items-center gap-2">
            <Badge :tone="statusTone(detail.status_code)" dot mono>
              HTTP {{ detail.status_code }}
            </Badge>
            <PlatformBadge :platform="resolvePlatform(detail)" />
            <Badge tone="neutral" mono>{{ detail.model }}</Badge>
            <Badge :tone="detail.stream ? 'info' : 'neutral'">
              {{ detail.stream ? 'SSE 流式传输' : '非流式同步' }}
            </Badge>
          </div>
          <div class="flex items-center gap-1.5">
            <code class="font-mono text-2xs text-muted">{{ detail.request_id || '无 request_id' }}</code>
            <Button
              v-if="detail.request_id"
              variant="ghost"
              size="xs"
              icon
              title="复制 Request ID"
              @click="copyReqId(detail.request_id)"
            >
              <Icon name="copy" :size="12" />
            </Button>
          </div>
        </div>

        <!-- Token / 性能 / 费用三联拆解面板 -->
        <div class="grid gap-3 md:grid-cols-3">
          <div class="panel-inset space-y-1.5 p-3">
            <p class="text-2xs font-semibold uppercase tracking-wider text-subtle">延迟与吞吐表现</p>
            <div class="flex justify-between text-xs">
              <span class="text-muted">首字响应 (TTFT)</span>
              <span class="tnum font-mono" :class="ttftClass(detail.first_token_ms)">
                {{ formatDuration(detail.first_token_ms) }}
              </span>
            </div>
            <div class="flex justify-between text-xs">
              <span class="text-muted">请求总用时</span>
              <span class="tnum font-mono text-fg">{{ formatDuration(detail.duration_ms) }}</span>
            </div>
            <div class="flex justify-between text-xs">
              <span class="text-muted">输出吞吐速率</span>
              <span class="tnum font-mono text-accent">{{ formatTps(resolveTps(detail)) }}</span>
            </div>
          </div>

          <div class="panel-inset space-y-1.5 p-3">
            <p class="text-2xs font-semibold uppercase tracking-wider text-subtle">Token 消耗明细</p>
            <div class="flex justify-between text-xs">
              <span class="text-muted">输入 / 输出</span>
              <span class="tnum font-mono text-fg">
                {{ formatNumber(detail.input_tokens) }} / {{ formatNumber(detail.output_tokens) }}
              </span>
            </div>
            <div class="flex justify-between text-xs">
              <span class="text-muted">缓存读 / 缓存写</span>
              <span class="tnum font-mono text-success">
                {{ formatNumber(detail.cache_read_tokens) }} / {{ formatNumber(detail.cache_creation_tokens) }}
              </span>
            </div>
            <div class="flex justify-between text-xs">
              <span class="text-muted">思维链 / 命中率</span>
              <span class="tnum font-mono text-fg">
                {{ formatNumber(detail.reasoning_tokens ?? 0) }} · {{ formatPercent(resolveCacheHitRatio(detail)) }}
              </span>
            </div>
          </div>

          <div class="panel-inset space-y-1.5 p-3">
            <p class="text-2xs font-semibold uppercase tracking-wider text-subtle">费用与倍率拆解</p>
            <div class="flex justify-between text-xs">
              <span class="text-muted">总计折算费用</span>
              <span class="tnum font-mono font-semibold text-fg">${{ formatCost(detail.total_cost) }}</span>
            </div>
            <div class="flex justify-between text-2xs">
              <span class="text-muted">输入 / 输出 / 缓存</span>
              <span class="tnum font-mono text-muted">
                ${{ formatCost(detail.input_cost ?? 0) }} / ${{ formatCost(detail.output_cost ?? 0) }} / ${{ formatCost(detail.cache_cost ?? 0) }}
              </span>
            </div>
            <div class="flex justify-between text-2xs">
              <span class="text-muted">生效倍率</span>
              <span class="tnum font-mono text-fg">{{ detail.rate_multiplier }}x</span>
            </div>
          </div>
        </div>

        <!-- 路由与客户端元数据 -->
        <dl class="grid gap-x-6 gap-y-2 rounded-md border border-line bg-surface p-3.5 text-xs sm:grid-cols-2">
          <div class="flex justify-between gap-3 border-b border-line/60 pb-1.5">
            <dt class="text-subtle">上游命中账号</dt>
            <dd class="font-medium text-fg">{{ detail.account_name || `#${detail.account_id}` }} (#{{ detail.account_id }})</dd>
          </div>
          <div class="flex justify-between gap-3 border-b border-line/60 pb-1.5">
            <dt class="text-subtle">路由分组</dt>
            <dd class="text-fg">{{ detail.group_name || (detail.group_id != null ? `#${detail.group_id}` : '—') }}</dd>
          </div>
          <div class="flex justify-between gap-3 border-b border-line/60 pb-1.5">
            <dt class="text-subtle">下游调用密钥</dt>
            <dd class="text-fg">{{ detail.api_key_name || `#${detail.api_key_id}` }} (#{{ detail.api_key_id }})</dd>
          </div>
          <div class="flex justify-between gap-3 border-b border-line/60 pb-1.5">
            <dt class="text-subtle">客户端来源 IP</dt>
            <dd class="font-mono text-fg">{{ detail.ip_address || '—' }}</dd>
          </div>
          <div class="sm:col-span-2 flex flex-col gap-1 pt-0.5">
            <dt class="text-subtle">客户端 User-Agent</dt>
            <dd class="break-all rounded border border-line bg-raised/60 px-2.5 py-1.5 font-mono text-2xs text-muted">
              {{ detail.user_agent || '—' }}
            </dd>
          </div>
        </dl>

        <!-- 错误原文展示 -->
        <CodeBlock
          v-if="detail.error_message"
          title="上游/网关错误响应原文 (error_message)"
          language="error"
          max-height="12rem"
          :code="detail.error_message"
        />
      </div>

      <template #footer>
        <Button variant="primary" size="sm" @click="detail = null">关闭</Button>
      </template>
    </Modal>
  </div>
</template>
