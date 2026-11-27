<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { toMessage } from '@/api/client'
import { dashboardApi } from '@/api/endpoints'
import type { BreakdownItem, DashboardResponse } from '@/api/types'
import AreaChart from '@/components/charts/AreaChart.vue'
import type { ChartPoint } from '@/components/charts/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Skeleton from '@/components/ui/Skeleton.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { useTimeRange } from '@/composables/useTimeRange'
import {
  formatCompact,
  formatCost,
  formatDateTime,
  formatDateTimeShort,
  formatDuration,
  formatNumber,
  formatPercent,
  formatTps,
} from '@/utils/format'
import { ALL_PLATFORMS } from '@/utils/platforms'

/** 安全数值守卫：任何 null / undefined / NaN 一律归一为 0，杜绝空库出现 NaN 或 undefined。 */
function num(val: unknown): number {
  return typeof val === 'number' && Number.isFinite(val) ? val : 0
}

const data = ref<DashboardResponse | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
let requestSeq = 0

const {
  mode: rangeMode,
  preset: presetHours,
  customStart,
  customEnd,
  error: rangeError,
  range,
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

const metric = ref<'requests' | 'tokens' | 'cost' | 'ttft'>('requests')
const metricTabs = [
  { label: '请求吞吐', value: 'requests', icon: 'activity' as const },
  { label: 'Token 量', value: 'tokens', icon: 'database' as const },
  { label: '估算费用', value: 'cost', icon: 'coins' as const },
  { label: '首字延迟', value: 'ttft', icon: 'zap' as const },
]

const shareTab = ref<'platform' | 'group'>('platform')
const shareTabs = [
  { label: '按平台流量', value: 'platform' },
  { label: '按分组路由', value: 'group' },
]

const summary = computed(() => data.value?.summary)
const runtime = computed(() => data.value?.runtime)

const rangeLabel = computed(() => {
  if (!data.value) return ''
  return `${formatDateTime(data.value.start)} → ${formatDateTime(data.value.end)}`
})

const bucketLabel = computed(() =>
  data.value?.runtime?.bucket === 'day' ? '按天聚合' : '按小时聚合',
)

const totalRequests = computed(() => num(summary.value?.requests))
const failedRequests = computed(() => Math.min(totalRequests.value, num(summary.value?.failed)))
const succeededRequests = computed(() => Math.max(0, totalRequests.value - failedRequests.value))
const hasTraffic = computed(() => totalRequests.value > 0)

const safeSuccessRate = computed<number | null>(() => {
  if (!hasTraffic.value) return null
  const raw = summary.value?.success_rate
  if (typeof raw === 'number' && Number.isFinite(raw)) {
    return Math.min(1, Math.max(0, raw))
  }
  return succeededRequests.value / totalRequests.value
})

const successTone = computed<'success' | 'warning' | 'danger' | 'neutral'>(() => {
  if (safeSuccessRate.value === null) return 'neutral'
  if (safeSuccessRate.value >= 0.98) return 'success'
  if (safeSuccessRate.value >= 0.9) return 'warning'
  return 'danger'
})

const ttftTone = computed<'success' | 'warning' | 'danger' | 'neutral'>(() => {
  const ms = num(summary.value?.avg_first_token_ms)
  if (!hasTraffic.value || ms <= 0) return 'neutral'
  if (ms < 1500) return 'success'
  if (ms < 4000) return 'warning'
  return 'danger'
})

const cacheHitPercent = computed(() => {
  if (!hasTraffic.value) return 0
  const raw = summary.value?.cache_hit_rate
  if (typeof raw === 'number' && Number.isFinite(raw)) {
    return Math.min(100, Math.max(0, raw * 100))
  }
  const cacheRead = num(summary.value?.cache_read_tokens)
  const totalIn =
    num(summary.value?.input_tokens) + cacheRead + num(summary.value?.cache_creation_tokens)
  if (totalIn <= 0) return 0
  return Math.min(100, Math.max(0, (cacheRead / totalIn) * 100))
})

const tokenBreakdown = computed(() => {
  const input = num(summary.value?.input_tokens)
  const output = num(summary.value?.output_tokens)
  const cacheRead = num(summary.value?.cache_read_tokens)
  const cacheCreate = num(summary.value?.cache_creation_tokens)
  const reasoning = num(summary.value?.reasoning_tokens)
  const total = input + output + cacheRead + cacheCreate
  const denom = Math.max(total, 1)
  return {
    input,
    output,
    cacheRead,
    cacheCreate,
    reasoning,
    total,
    inputPct: total > 0 ? (input / denom) * 100 : 0,
    outputPct: total > 0 ? (output / denom) * 100 : 0,
    cacheReadPct: total > 0 ? (cacheRead / denom) * 100 : 0,
    cacheCreatePct: total > 0 ? (cacheCreate / denom) * 100 : 0,
  }
})

const platformPoolCards = computed(() => {
  const counts = runtime.value?.accounts_by_platform ?? {}
  const trafficMap = new Map<string, BreakdownItem>()
  for (const item of data.value?.by_platform ?? []) {
    if (item.key) trafficMap.set(item.key.toLowerCase(), item)
  }
  return ALL_PLATFORMS.map((meta) => {
    const traffic = trafficMap.get(meta.value)
    return {
      meta,
      accountCount: num(counts[meta.value]),
      requests: num(traffic?.requests),
      cost: num(traffic?.total_cost),
      successRate: traffic && num(traffic.requests) > 0 ? num(traffic.success_rate) : null,
      ttft: traffic && num(traffic.requests) > 0 ? num(traffic.avg_first_token_ms) : null,
    }
  })
})

const chartTone = computed<'accent' | 'info' | 'success' | 'warning'>(() => {
  if (metric.value === 'cost') return 'success'
  if (metric.value === 'tokens') return 'info'
  if (metric.value === 'ttft') return 'warning'
  return 'accent'
})

const chartPoints = computed<ChartPoint[]>(() =>
  (data.value?.series ?? []).map((point) => ({
    label: formatDateTimeShort(point.bucket),
    tooltip: formatDateTime(point.bucket),
    value:
      metric.value === 'requests'
        ? num(point.requests)
        : metric.value === 'cost'
          ? num(point.total_cost)
          : metric.value === 'ttft'
            ? num(point.avg_first_token_ms)
            : num(point.input_tokens) +
              num(point.output_tokens) +
              num(point.cache_read_tokens) +
              num(point.cache_creation_tokens),
  })),
)

const chartPeak = computed(() =>
  chartPoints.value.reduce((max, pt) => Math.max(max, num(pt.value)), 0),
)

const chartFormatter = computed(() => {
  if (metric.value === 'cost') return (value: number) => `$${formatCost(num(value))}`
  if (metric.value === 'tokens') return (value: number) => formatCompact(num(value))
  if (metric.value === 'ttft') return (value: number) => formatDuration(num(value))
  return (value: number) => formatNumber(num(value))
})

const platformBreakdown = computed<BreakdownItem[]>(() => data.value?.by_platform ?? [])
const groupBreakdown = computed<BreakdownItem[]>(() => data.value?.by_group ?? [])
const modelRows = computed<BreakdownItem[]>(() => data.value?.models ?? [])
const accountRows = computed<BreakdownItem[]>(() => data.value?.accounts ?? [])

const activeShareRows = computed<BreakdownItem[]>(() =>
  shareTab.value === 'platform' ? platformBreakdown.value : groupBreakdown.value,
)

const shareTotalRequests = computed(() =>
  Math.max(
    activeShareRows.value.reduce((acc, row) => acc + num(row.requests), 0),
    1,
  ),
)

function sharePercent(row: BreakdownItem): string {
  const reqs = num(row.requests)
  if (reqs <= 0) return '0.0%'
  return `${((reqs / shareTotalRequests.value) * 100).toFixed(1)}%`
}

function shareWidth(row: BreakdownItem): string {
  const reqs = num(row.requests)
  if (reqs <= 0) return '0%'
  return `${Math.min(100, Math.max((reqs / shareTotalRequests.value) * 100, 2))}%`
}

function latencyColorClass(ms: number | undefined | null): string {
  const clean = num(ms)
  if (clean <= 0) return 'text-subtle'
  if (clean < 1500) return 'text-success'
  if (clean < 4000) return 'text-warning'
  return 'text-danger'
}

async function load(softRefresh = false): Promise<void> {
  if (rangeError.value) return
  const seq = ++requestSeq
  const keepContent = softRefresh && Boolean(data.value)
  if (keepContent) refreshing.value = true
  else loading.value = true
  error.value = ''

  try {
    const { start, end } = range.value
    const next = await dashboardApi.fetch(start, end)
    if (seq !== requestSeq) return
    data.value = next
  } catch (err) {
    if (seq !== requestSeq) return
    error.value = toMessage(err, '加载性能看板数据失败')
    if (!keepContent) data.value = null
  } finally {
    if (seq === requestSeq) {
      loading.value = false
      refreshing.value = false
    }
  }
}

function selectPreset(val: string): void {
  if (rangeMode.value === 'preset' && presetHours.value === val && data.value) return
  presetHours.value = val
  if (rangeMode.value !== 'preset') {
    usePreset()
  }
  void load(true)
}

onMounted(() => {
  void load(false)
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部时间窗口与聚合控制条 -->
    <div class="card flex flex-wrap items-center justify-between gap-2.5 px-3.5 py-2.5">
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
          自定义时段
        </Button>
        <Button v-else variant="subtle" size="sm" @click="selectPreset(presetHours)">
          返回预设
        </Button>

        <span
          v-if="refreshing"
          class="inline-flex items-center gap-1.5 rounded-md border border-accent/25 bg-accent/10 px-2 py-0.5 text-2xs font-medium text-accent"
        >
          <Spinner :size="11" />
          同步中…
        </span>
      </div>

      <div v-if="rangeMode === 'custom'" class="flex flex-wrap items-end gap-2">
        <div class="w-48">
          <Field label="起始时间">
            <Input v-model="customStart" size="sm" type="datetime-local" />
          </Field>
        </div>
        <div class="w-48">
          <Field label="截止时间" :error="rangeError">
            <Input
              v-model="customEnd"
              size="sm"
              type="datetime-local"
              :invalid="Boolean(rangeError)"
            />
          </Field>
        </div>
        <Button variant="primary" size="sm" :disabled="Boolean(rangeError)" @click="load(true)">
          应用查询
        </Button>
      </div>

      <div class="ml-auto flex items-center gap-2">
        <Badge v-if="data" tone="neutral" mono class="hidden xl:inline-flex">
          {{ rangeLabel }} · {{ bucketLabel }}
        </Badge>
        <Button variant="secondary" size="sm" :loading="refreshing || loading" @click="load(true)">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新看板
        </Button>
      </div>
    </div>

    <!-- 异常回退 -->
    <Card v-if="error && !data" body-class="p-0">
      <EmptyState
        icon="alertCircle"
        tone="danger"
        title="性能看板数据加载失败"
        :description="error"
      >
        <Button variant="primary" size="sm" @click="load(false)">重试加载</Button>
      </EmptyState>
    </Card>

    <template v-else>
      <!-- 8 宫格核心 KPI 指标（零 NaN / 空库语义友好） -->
      <div v-if="loading && !data" class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div v-for="i in 8" :key="i" class="card space-y-2 px-3.5 py-3">
          <Skeleton class="h-3.5 w-28" />
          <Skeleton class="h-6 w-24" />
          <Skeleton class="h-3 w-40" />
        </div>
      </div>

      <div v-else class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <!-- 1. 总请求数 & 成功率 -->
        <MetricTile
          label="总请求数 & 成功率"
          :value="
            hasTraffic
              ? `${formatNumber(totalRequests)} · ${formatPercent(safeSuccessRate, 1)}`
              : '0 次请求'
          "
          icon="activity"
          :tone="successTone"
        >
          <template #hint>
            <span v-if="hasTraffic" class="tnum">
              成功 {{ formatNumber(succeededRequests) }} ·
              <span :class="failedRequests > 0 ? 'font-medium text-danger' : ''">
                异常 {{ formatNumber(failedRequests) }}
              </span>
            </span>
            <span v-else>所选时间窗口内暂无下游请求样本</span>
          </template>
        </MetricTile>

        <!-- 2. 估算总成本 -->
        <MetricTile
          label="估算总成本 (USD)"
          :value="`$${formatCost(num(summary?.total_cost))}`"
          icon="coins"
          tone="accent"
        >
          <template #hint>
            <span class="tnum">
              入 ${{ formatCost(num(summary?.input_cost)) }} · 出 ${{
                formatCost(num(summary?.output_cost))
              }}
              · 缓 ${{ formatCost(num(summary?.cache_cost)) }}
            </span>
          </template>
        </MetricTile>

        <!-- 3. 首字延迟 TTFT -->
        <MetricTile
          label="首字延迟 TTFT"
          :value="
            hasTraffic && num(summary?.avg_first_token_ms) > 0
              ? formatDuration(num(summary?.avg_first_token_ms))
              : '—'
          "
          icon="zap"
          :tone="ttftTone"
        >
          <template #hint>
            <span v-if="hasTraffic" class="tnum font-mono">
              P50 {{ formatDuration(num(summary?.p50_first_token_ms) || null) }} / P95
              {{ formatDuration(num(summary?.p95_first_token_ms) || null) }}
            </span>
            <span v-else>发起流式调用后自动统计 P50 / P95</span>
          </template>
        </MetricTile>

        <!-- 4. 平均总耗时 & 输出吞吐速率 -->
        <MetricTile
          label="端到端均耗 & 输出速率"
          :value="
            hasTraffic && num(summary?.avg_duration_ms) > 0
              ? `${formatDuration(num(summary?.avg_duration_ms))} · ${formatTps(num(summary?.avg_tps))}`
              : '—'
          "
          icon="clock"
          tone="info"
        >
          <template #hint>
            <span v-if="hasTraffic" class="tnum">
              均耗 {{ formatDuration(num(summary?.avg_duration_ms)) }} · 吞吐
              {{ formatTps(num(summary?.avg_tps)) }}
            </span>
            <span v-else>等待首个完成响应以计算吞吐速率</span>
          </template>
        </MetricTile>

        <!-- 5. 全局缓存命中率 -->
        <div class="card flex flex-col justify-between px-3.5 py-3">
          <div class="flex items-start justify-between gap-2">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-1.5 text-2xs font-medium text-muted">
                <span
                  class="h-1.5 w-1.5 shrink-0 rounded-full"
                  :class="hasTraffic ? 'bg-success' : 'bg-subtle'"
                />
                <span>全局缓存命中率</span>
              </div>
              <p class="tnum mt-1.5 text-lg font-semibold leading-6 text-fg">
                {{ hasTraffic ? `${cacheHitPercent.toFixed(1)}%` : '—' }}
              </p>
            </div>
            <span
              class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-line/70 bg-success/10 text-success"
            >
              <Icon name="layers" :size="15" />
            </span>
          </div>
          <div class="mt-2 space-y-1">
            <div class="h-1.5 w-full overflow-hidden rounded-full bg-raised">
              <div
                class="h-full rounded-full bg-success transition-[width] duration-200"
                :style="{ width: `${cacheHitPercent}%` }"
              />
            </div>
            <p class="tnum truncate text-2xs text-subtle">
              读 {{ formatCompact(tokenBreakdown.cacheRead) }} · 建
              {{ formatCompact(tokenBreakdown.cacheCreate) }} · 入
              {{ formatCompact(tokenBreakdown.input) }}
            </p>
          </div>
        </div>

        <!-- 6. Token 吞吐构成 -->
        <div class="card flex flex-col justify-between px-3.5 py-3">
          <div class="flex items-start justify-between gap-2">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-1.5 text-2xs font-medium text-muted">
                <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-accent" />
                <span>Token 吞吐与构成</span>
              </div>
              <p class="tnum mt-1.5 text-lg font-semibold leading-6 text-fg">
                {{ formatCompact(tokenBreakdown.total) }}
              </p>
            </div>
            <span
              class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-line/70 bg-accent/10 text-accent"
            >
              <Icon name="database" :size="15" />
            </span>
          </div>
          <div class="mt-2 space-y-1">
            <div class="flex h-1.5 w-full overflow-hidden rounded-full bg-raised">
              <div class="bg-accent" :style="{ width: `${tokenBreakdown.inputPct}%` }" title="输入" />
              <div class="bg-info" :style="{ width: `${tokenBreakdown.outputPct}%` }" title="输出" />
              <div
                class="bg-success"
                :style="{ width: `${tokenBreakdown.cacheReadPct}%` }"
                title="缓存读"
              />
              <div
                class="bg-warning"
                :style="{ width: `${tokenBreakdown.cacheCreatePct}%` }"
                title="缓存写"
              />
            </div>
            <p class="tnum truncate text-2xs text-subtle">
              入 {{ formatCompact(tokenBreakdown.input) }} · 出
              {{ formatCompact(tokenBreakdown.output) }} · 推理
              {{ formatCompact(tokenBreakdown.reasoning) }}
            </p>
          </div>
        </div>

        <!-- 7. 在线账号与容器水位 -->
        <MetricTile
          label="在线账号与容器池水位"
          :value="`${num(runtime?.accounts_active)} / ${num(runtime?.accounts_total)} 可调度`"
          icon="server"
          :tone="num(runtime?.accounts_active) > 0 ? 'success' : 'warning'"
        >
          <template #hint>
            <span v-if="runtime?.container_nodes_total !== undefined" class="tnum">
              容器节点 {{ num(runtime?.container_nodes_running) }}/{{
                num(runtime?.container_nodes_total)
              }}
              在线 · 四端并发就绪
            </span>
            <span v-else class="tnum">
              活跃率
              {{
                num(runtime?.accounts_total) > 0
                  ? `${((num(runtime?.accounts_active) / num(runtime?.accounts_total)) * 100).toFixed(0)}%`
                  : '—'
              }}
              · 自动熔断巡检常驻
            </span>
          </template>
        </MetricTile>

        <!-- 8. 下游密钥与分组路由 -->
        <MetricTile
          label="下游密钥与分组路由"
          :value="`${formatNumber(num(runtime?.api_keys_total))} Key · ${formatNumber(groupBreakdown.length)} 活跃组`"
          icon="key"
          tone="neutral"
        >
          <template #hint>
            <span class="inline-flex items-center gap-2">
              <RouterLink :to="{ name: 'keys' }" class="text-accent hover:underline">
                管理密钥
              </RouterLink>
              <span>·</span>
              <RouterLink :to="{ name: 'groups' }" class="text-accent hover:underline">
                分组路由
              </RouterLink>
              <span>·</span>
              <RouterLink :to="{ name: 'logs' }" class="text-accent hover:underline">
                明细日志
              </RouterLink>
            </span>
          </template>
        </MetricTile>
      </div>

      <!-- 四源上游平台健康与流量态势条 -->
      <Card
        title="四源上游平台健康与调度概览"
        subtitle="实时汇总 WorkBuddy、AntiGravity、Command Code 与 OpenCode 的池内账号规模、时段命中请求与延迟表现"
        body-class="p-3.5"
      >
        <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <RouterLink
            v-for="item in platformPoolCards"
            :key="item.meta.value"
            :to="{ name: item.meta.routeName }"
            class="group flex flex-col justify-between rounded-md border border-line bg-raised/45 p-3 transition hover:border-line-strong hover:bg-raised"
          >
            <div class="flex items-center justify-between gap-2">
              <div class="flex items-center gap-2">
                <PlatformBadge :platform="item.meta.value" />
              </div>
              <span class="inline-flex items-center gap-1 text-2xs text-subtle group-hover:text-accent">
                <span>进入工作台</span>
                <Icon name="arrowUpRight" :size="12" />
              </span>
            </div>

            <div class="mt-2.5 grid grid-cols-3 gap-2 border-t border-line/65 pt-2 text-2xs">
              <div>
                <p class="text-subtle">池内账号</p>
                <p class="tnum mt-0.5 font-mono text-xs font-semibold text-fg">
                  {{ formatNumber(item.accountCount) }}
                </p>
              </div>
              <div>
                <p class="text-subtle">时段请求</p>
                <p class="tnum mt-0.5 font-mono text-xs font-semibold text-fg">
                  {{ formatNumber(item.requests) }}
                </p>
              </div>
              <div class="text-right">
                <p class="text-subtle">首字 / 费用</p>
                <p class="tnum mt-0.5 font-mono text-xs text-muted">
                  {{ item.ttft ? formatDuration(item.ttft) : `$${formatCost(item.cost)}` }}
                </p>
              </div>
            </div>
          </RouterLink>
        </div>
      </Card>

      <!-- 中部：时序趋势图 + 平台/分组流量占比卡 -->
      <div class="grid gap-4 lg:grid-cols-12">
        <Card
          class="lg:col-span-8"
          title="流量、Token、费用与首字延迟趋势"
          :subtitle="rangeLabel ? `${rangeLabel}（${bucketLabel}）` : '实时网关吞吐走势'"
          :loading="refreshing"
          body-class="px-3.5 pb-3.5 pt-2.5"
        >
          <template #actions>
            <div class="flex flex-wrap items-center gap-2">
              <Badge v-if="hasTraffic && chartPeak > 0" tone="neutral" mono>
                峰值 {{ chartFormatter(chartPeak) }}
              </Badge>
              <SegmentedTabs v-model="metric" :items="metricTabs" size="sm" />
            </div>
          </template>

          <div v-if="loading && !data" class="flex h-[245px] items-end gap-1.5 px-2">
            <Skeleton
              v-for="i in 24"
              :key="i"
              class="flex-1"
              :style="{ height: `${18 + ((i * 37) % 72)}%` }"
            />
          </div>
          <AreaChart
            v-else
            :points="chartPoints"
            :tone="chartTone"
            :format-value="chartFormatter"
            :height="245"
            :empty-text="hasTraffic ? '当前指标在所选时段数值为 0' : '所选时间窗口暂无网关请求流量'"
          />
        </Card>

        <Card
          class="lg:col-span-4"
          title="平台 & 分组流量占比"
          subtitle="请求分发权重、缓存率与成本拆分"
          body-class="p-3.5"
        >
          <template #actions>
            <SegmentedTabs v-model="shareTab" :items="shareTabs" size="sm" />
          </template>

          <div v-if="loading && !data" class="space-y-3">
            <Skeleton v-for="i in 4" :key="i" class="h-12 w-full" />
          </div>

          <div
            v-else-if="activeShareRows.length === 0"
            class="flex h-[235px] flex-col items-center justify-center rounded-md border border-dashed border-line bg-raised/35 px-4 text-center"
          >
            <Icon name="layers" :size="18" class="text-subtle" />
            <p class="mt-2 text-xs font-medium text-muted">
              {{ shareTab === 'platform' ? '暂无平台流量分布' : '暂无分组路由调用记录' }}
            </p>
            <p class="mt-0.5 text-2xs text-subtle">发起推理调用后自动按维度聚合请求与费用</p>
          </div>

          <ul v-else class="scroll-thin max-h-[245px] space-y-2.5 overflow-y-auto pr-1">
            <li
              v-for="row in activeShareRows"
              :key="row.key"
              class="rounded-md border border-line bg-raised/45 px-3 py-2"
            >
              <div class="flex items-center justify-between gap-2">
                <div class="flex min-w-0 items-center gap-1.5">
                  <PlatformBadge v-if="shareTab === 'platform'" :platform="row.key" short />
                  <span class="truncate text-xs font-medium text-fg">
                    {{ row.label || row.key || '未命名' }}
                  </span>
                </div>
                <span class="tnum shrink-0 font-mono text-xs font-semibold text-fg">
                  {{ formatNumber(num(row.requests)) }} 次
                  <span class="font-normal text-subtle">({{ sharePercent(row) }})</span>
                </span>
              </div>

              <div class="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-overlay">
                <div
                  class="h-full rounded-full bg-accent transition-[width] duration-200"
                  :style="{ width: shareWidth(row) }"
                />
              </div>

              <div
                class="tnum mt-1.5 flex flex-wrap items-center justify-between gap-2 text-2xs text-subtle"
              >
                <span>
                  {{ formatCompact(num(row.tokens)) }} tok · 缓存
                  {{ formatPercent(num(row.cache_hit_rate), 0) }}
                </span>
                <span>
                  TTFT {{ num(row.avg_first_token_ms) > 0 ? formatDuration(row.avg_first_token_ms) : '—' }}
                  · ${{ formatCost(num(row.total_cost)) }}
                </span>
              </div>
            </li>
          </ul>
        </Card>
      </div>

      <!-- 底部 1：模型性能与缓存命中率深度排行表 -->
      <Card
        title="模型性能与缓存命中率深度排行"
        subtitle="涵盖请求成功率、缓存命中率、TTFT 首字均值/P95、端到端耗时、TPS 吞吐与估算成本"
        body-class="p-0"
      >
        <DataTable
          :columns="9"
          :loading="loading && !data"
          :refreshing="refreshing"
          :empty="modelRows.length === 0"
          :skeleton-rows="5"
          min-width="1050px"
          hoverable
          class="rounded-none border-0"
        >
          <template #head>
            <th class="th">模型名称</th>
            <th class="th text-right">请求数 / 成功率</th>
            <th class="th text-right">总 Tokens (入/出)</th>
            <th class="th">缓存命中率</th>
            <th class="th text-right">平均首字 TTFT</th>
            <th class="th text-right">P95 首字</th>
            <th class="th text-right">平均总耗时</th>
            <th class="th text-right">平均 TPS</th>
            <th class="th text-right">估算总成本</th>
          </template>

          <template #empty>
            <EmptyState
              icon="cpu"
              title="当前时间窗口暂无模型调用记录"
              description="通过下游 API Key 请求任意模型后，此处将实时统计各模型的首字延迟、缓存命中率与 TPS。"
            />
          </template>

          <template #body>
            <tr v-for="m in modelRows" :key="m.key">
              <td class="td">
                <span class="font-mono text-xs font-medium text-fg">{{ m.label || m.key }}</span>
              </td>
              <td class="td tnum text-right">
                <span class="font-medium text-fg">{{ formatNumber(num(m.requests)) }}</span>
                <span
                  v-if="num(m.requests) > 0"
                  class="ml-1.5 text-2xs"
                  :class="num(m.success_rate) >= 0.98 ? 'text-success' : 'text-warning'"
                >
                  {{ formatPercent(num(m.success_rate), 1) }}
                </span>
              </td>
              <td class="td tnum text-right">
                <div class="font-medium text-fg">{{ formatCompact(num(m.tokens)) }}</div>
                <div class="text-2xs text-subtle">
                  入 {{ formatCompact(num(m.input_tokens)) }} / 出
                  {{ formatCompact(num(m.output_tokens)) }}
                </div>
              </td>
              <td class="td min-w-[9rem]">
                <div class="flex items-center gap-2">
                  <div class="h-1.5 w-16 overflow-hidden rounded-full bg-raised">
                    <div
                      class="h-full rounded-full bg-success"
                      :style="{
                        width: `${Math.min(100, Math.max(0, num(m.cache_hit_rate) * 100))}%`,
                      }"
                    />
                  </div>
                  <span class="tnum text-xs font-medium text-fg">
                    {{ formatPercent(num(m.cache_hit_rate), 1) }}
                  </span>
                </div>
                <div v-if="num(m.cache_read_tokens) > 0" class="tnum text-2xs text-subtle">
                  命中 {{ formatCompact(num(m.cache_read_tokens)) }}
                </div>
              </td>
              <td
                class="td tnum text-right font-mono text-xs"
                :class="latencyColorClass(m.avg_first_token_ms)"
              >
                {{ num(m.avg_first_token_ms) > 0 ? formatDuration(m.avg_first_token_ms) : '—' }}
              </td>
              <td
                class="td tnum text-right font-mono text-xs"
                :class="latencyColorClass(m.p95_first_token_ms)"
              >
                {{ num(m.p95_first_token_ms) > 0 ? formatDuration(m.p95_first_token_ms) : '—' }}
              </td>
              <td class="td tnum text-right font-mono text-xs text-muted">
                {{ num(m.avg_duration_ms) > 0 ? formatDuration(m.avg_duration_ms) : '—' }}
              </td>
              <td class="td tnum text-right font-mono text-xs text-fg">
                {{ formatTps(num(m.avg_tps)) }}
              </td>
              <td class="td tnum text-right font-mono text-xs font-semibold text-fg">
                ${{ formatCost(num(m.total_cost)) }}
              </td>
            </tr>
          </template>
        </DataTable>
      </Card>

      <!-- 底部 2：上游账号消耗排行表 -->
      <Card
        title="上游账号消耗与性能排行"
        subtitle="按调度命中次数排序，实时追踪单号负载、响应延迟与成本分布"
        body-class="p-0"
      >
        <DataTable
          :columns="7"
          :loading="loading && !data"
          :refreshing="refreshing"
          :empty="accountRows.length === 0"
          :skeleton-rows="4"
          min-width="860px"
          hoverable
          class="rounded-none border-0"
        >
          <template #head>
            <th class="th">上游账号</th>
            <th class="th text-right">调度次数</th>
            <th class="th text-right">成功率</th>
            <th class="th text-right">总 Tokens</th>
            <th class="th text-right">缓存命中率</th>
            <th class="th text-right">平均 TTFT / TPS</th>
            <th class="th text-right">折算成本</th>
          </template>

          <template #empty>
            <EmptyState
              icon="layers"
              title="当前时间窗口暂无上游账号消耗记录"
              description="可前往 AntiGravity 容器池或 WorkBuddy / Command Code / OpenCode 账号池添加并启用上游资源。"
            >
              <RouterLink :to="{ name: 'antigravity' }">
                <Button variant="secondary" size="sm">AntiGravity 容器池</Button>
              </RouterLink>
              <RouterLink :to="{ name: 'accounts-workbuddy' }">
                <Button variant="primary" size="sm">WorkBuddy 账号池</Button>
              </RouterLink>
            </EmptyState>
          </template>

          <template #body>
            <tr v-for="acc in accountRows" :key="acc.key">
              <td class="td font-medium text-fg">{{ acc.label || acc.key }}</td>
              <td class="td tnum text-right font-medium text-fg">
                {{ formatNumber(num(acc.requests)) }}
              </td>
              <td class="td tnum text-right">
                <Badge
                  :tone="
                    num(acc.success_rate) >= 0.98
                      ? 'success'
                      : num(acc.success_rate) >= 0.9
                        ? 'warning'
                        : 'danger'
                  "
                >
                  {{ formatPercent(num(acc.success_rate), 1) }}
                </Badge>
              </td>
              <td class="td tnum text-right text-muted">{{ formatCompact(num(acc.tokens)) }}</td>
              <td class="td tnum text-right text-muted">
                {{ formatPercent(num(acc.cache_hit_rate), 1) }}
              </td>
              <td class="td tnum text-right font-mono text-xs text-muted">
                {{ num(acc.avg_first_token_ms) > 0 ? formatDuration(acc.avg_first_token_ms) : '—' }}
                · {{ formatTps(num(acc.avg_tps)) }}
              </td>
              <td class="td tnum text-right font-mono text-xs font-semibold text-fg">
                ${{ formatCost(num(acc.total_cost)) }}
              </td>
            </tr>
          </template>
        </DataTable>
      </Card>
    </template>
  </div>
</template>
