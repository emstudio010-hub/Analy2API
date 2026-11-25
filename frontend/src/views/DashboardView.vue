<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
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

const data = ref<DashboardResponse | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')

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

const metric = ref('requests')
const metricTabs = [
  { label: '请求数', value: 'requests', icon: 'activity' as const },
  { label: 'Token 量', value: 'tokens', icon: 'database' as const },
  { label: '估算费用', value: 'cost', icon: 'coins' as const },
]

const shareTab = ref('platform')
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
  data.value?.runtime.bucket === 'day' ? '按天聚合' : '按小时聚合',
)

const hasTraffic = computed(() => (summary.value?.requests ?? 0) > 0)

const successTone = computed<'success' | 'warning' | 'danger'>(() => {
  const rate = summary.value?.success_rate ?? 1
  if (rate >= 0.98) return 'success'
  if (rate >= 0.9) return 'warning'
  return 'danger'
})

const ttftTone = computed<'success' | 'warning' | 'danger' | 'info'>(() => {
  const ms = summary.value?.avg_first_token_ms ?? 0
  if (ms <= 0) return 'info'
  if (ms < 1500) return 'success'
  if (ms < 4000) return 'warning'
  return 'danger'
})

const cacheHitPercent = computed(() => {
  const raw = summary.value?.cache_hit_rate
  if (raw !== undefined && raw !== null && !Number.isNaN(raw)) {
    return Math.min(100, Math.max(0, raw * 100))
  }
  const cacheRead = summary.value?.cache_read_tokens ?? 0
  const totalIn =
    (summary.value?.input_tokens ?? 0) +
    cacheRead +
    (summary.value?.cache_creation_tokens ?? 0)
  if (totalIn <= 0) return 0
  return Math.min(100, Math.max(0, (cacheRead / totalIn) * 100))
})

const totalTokensVolume = computed(() => {
  const s = summary.value
  if (!s) return 0
  return (
    (s.input_tokens ?? 0) +
    (s.output_tokens ?? 0) +
    (s.cache_read_tokens ?? 0) +
    (s.cache_creation_tokens ?? 0)
  )
})

const platformPoolBadges = computed(() => {
  const raw = runtime.value?.accounts_by_platform ?? {}
  return ALL_PLATFORMS.map((meta) => ({
    meta,
    count: raw[meta.value] ?? 0,
  }))
})

const chartTone = computed<'accent' | 'info' | 'success'>(() => {
  if (metric.value === 'cost') return 'success'
  if (metric.value === 'tokens') return 'info'
  return 'accent'
})

const chartPoints = computed<ChartPoint[]>(() =>
  (data.value?.series ?? []).map((point) => ({
    label: formatDateTimeShort(point.bucket),
    tooltip: formatDateTime(point.bucket),
    value:
      metric.value === 'requests'
        ? point.requests
        : metric.value === 'cost'
          ? point.total_cost
          : point.input_tokens +
            point.output_tokens +
            (point.cache_read_tokens ?? 0) +
            (point.cache_creation_tokens ?? 0),
  })),
)

const chartFormatter = computed(() => {
  if (metric.value === 'cost') return (value: number) => `$${formatCost(value)}`
  if (metric.value === 'tokens') return (value: number) => formatCompact(value)
  return (value: number) => formatNumber(value)
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
    activeShareRows.value.reduce((acc, row) => acc + (row.requests || 0), 0),
    1,
  ),
)

function latencyColorClass(ms: number | undefined | null): string {
  if (!ms || ms <= 0) return 'text-subtle'
  if (ms < 1500) return 'text-success'
  if (ms < 4000) return 'text-warning'
  return 'text-danger'
}

async function load(isRefresh = false): Promise<void> {
  if (rangeError.value) return
  if (isRefresh) refreshing.value = true
  else loading.value = true
  error.value = ''
  try {
    const { start, end } = range.value
    data.value = await dashboardApi.fetch(start, end)
  } catch (err) {
    error.value = toMessage(err, '加载性能看板数据失败')
    data.value = null
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function selectPreset(val: string): void {
  presetHours.value = val
  if (rangeMode.value !== 'preset') {
    usePreset()
  }
  void load()
}

watch(presetHours, () => {
  if (rangeMode.value === 'preset') void load()
})

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部高密度时间控制条 -->
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
      </div>

      <div v-if="rangeMode === 'custom'" class="flex flex-wrap items-end gap-2">
        <div class="w-48">
          <Field label="起始">
            <Input v-model="customStart" size="sm" type="datetime-local" />
          </Field>
        </div>
        <div class="w-48">
          <Field label="截止" :error="rangeError">
            <Input v-model="customEnd" size="sm" type="datetime-local" />
          </Field>
        </div>
        <Button variant="primary" size="sm" :disabled="Boolean(rangeError)" @click="load()">
          查询
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
    <Card v-if="error" body-class="p-0">
      <EmptyState
        icon="alertCircle"
        tone="danger"
        title="性能看板数据加载失败"
        :description="error"
      >
        <Button variant="primary" size="sm" @click="load()">重试加载</Button>
      </EmptyState>
    </Card>

    <template v-else>
      <!-- 8 宫格高密度核心指标（MetricTile） -->
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
          :value="`${formatNumber(summary?.requests ?? 0)} · ${formatPercent(summary?.success_rate ?? 0, 1)}`"
          icon="activity"
          :tone="hasTraffic ? successTone : 'neutral'"
        >
          <template #hint>
            <span>
              成功 {{ formatNumber((summary?.requests ?? 0) - (summary?.failed ?? 0)) }} ·
              <span :class="(summary?.failed ?? 0) > 0 ? 'text-danger font-medium' : ''">
                失败 {{ formatNumber(summary?.failed ?? 0) }}
              </span>
            </span>
          </template>
        </MetricTile>

        <!-- 2. 估算总成本 -->
        <MetricTile
          label="估算总成本 (USD)"
          :value="`$${formatCost(summary?.total_cost ?? 0)}`"
          icon="coins"
          tone="accent"
        >
          <template #hint>
            <span class="tnum">
              输入 ${{ formatCost(summary?.input_cost ?? 0) }} · 输出 ${{
                formatCost(summary?.output_cost ?? 0)
              }}
              · 缓存 ${{ formatCost(summary?.cache_cost ?? 0) }}
            </span>
          </template>
        </MetricTile>

        <!-- 3. 首字延迟 TTFT (Avg + P50/P95) -->
        <MetricTile
          label="首字延迟 TTFT"
          :value="formatDuration(summary?.avg_first_token_ms ?? 0)"
          icon="zap"
          :tone="ttftTone"
        >
          <template #hint>
            <span class="tnum font-mono">
              P50 {{ formatDuration(summary?.p50_first_token_ms ?? 0) }} / P95
              {{ formatDuration(summary?.p95_first_token_ms ?? 0) }}
            </span>
          </template>
        </MetricTile>

        <!-- 4. 平均总用时 & 平均输出速率 TPS -->
        <MetricTile
          label="平均总用时 & 输出速率"
          :value="`${formatDuration(summary?.avg_duration_ms ?? 0)} · ${formatTps(summary?.avg_tps)}`"
          icon="clock"
          tone="info"
        >
          <template #hint>
            <span class="tnum">
              端到端均耗 {{ formatDuration(summary?.avg_duration_ms ?? 0) }} · 吞吐
              {{ formatTps(summary?.avg_tps) }}
            </span>
          </template>
        </MetricTile>

        <!-- 5. 全局缓存命中率 (带醒目进度条) -->
        <div class="card flex flex-col justify-between px-3.5 py-3">
          <div class="flex items-start justify-between gap-2">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-1.5 text-2xs font-medium text-muted">
                <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-success" />
                <span>全局缓存命中率</span>
              </div>
              <p class="tnum mt-1 text-lg font-semibold leading-6 text-fg">
                {{ cacheHitPercent.toFixed(1) }}%
              </p>
            </div>
            <span
              class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-line/70 bg-success/10 text-success"
            >
              <Icon name="layers" :size="15" />
            </span>
          </div>
          <div class="mt-1.5 space-y-1">
            <div class="h-1.5 w-full overflow-hidden rounded-full bg-raised">
              <div
                class="h-full rounded-full bg-success transition-[width] duration-300"
                :style="{ width: `${cacheHitPercent}%` }"
              />
            </div>
            <p class="tnum truncate text-2xs text-subtle">
              读 {{ formatCompact(summary?.cache_read_tokens ?? 0) }} · 建
              {{ formatCompact(summary?.cache_creation_tokens ?? 0) }} · 入
              {{ formatCompact(summary?.input_tokens ?? 0) }}
            </p>
          </div>
        </div>

        <!-- 6. Token 吞吐总量 -->
        <MetricTile
          label="Token 吞吐总量"
          :value="formatCompact(totalTokensVolume)"
          icon="database"
          tone="accent"
        >
          <template #hint>
            <span class="tnum">
              输入 {{ formatCompact(summary?.input_tokens ?? 0) }} · 输出
              {{ formatCompact(summary?.output_tokens ?? 0) }} · 思维链
              {{ formatCompact(summary?.reasoning_tokens ?? 0) }}
            </span>
          </template>
        </MetricTile>

        <!-- 7. 在线账号池健康水位 -->
        <div class="card flex flex-col justify-between px-3.5 py-3">
          <div class="flex items-start justify-between gap-2">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-1.5 text-2xs font-medium text-muted">
                <span
                  class="h-1.5 w-1.5 shrink-0 rounded-full"
                  :class="(runtime?.accounts_active ?? 0) > 0 ? 'bg-success' : 'bg-warning'"
                />
                <span>在线账号池健康水位</span>
              </div>
              <p class="tnum mt-1 text-lg font-semibold leading-6 text-fg">
                {{ runtime?.accounts_active ?? 0 }}
                <span class="text-xs font-normal text-subtle">
                  / {{ runtime?.accounts_total ?? 0 }} 可调度
                </span>
              </p>
            </div>
            <span
              class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-line/70 bg-accent/10 text-accent"
            >
              <Icon name="server" :size="15" />
            </span>
          </div>
          <div class="mt-1.5 flex flex-wrap items-center gap-1">
            <span
              v-for="p in platformPoolBadges"
              :key="p.meta.value"
              class="tnum inline-flex items-center gap-1 rounded border border-line bg-raised px-1.5 py-px font-mono text-[10px] text-muted"
            >
              <span>{{ p.meta.shortLabel }}</span>
              <strong class="text-fg">{{ p.count }}</strong>
            </span>
          </div>
        </div>

        <!-- 8. 活跃 API Key 与分组状态 -->
        <MetricTile
          label="下游密钥与活跃分组"
          :value="`${formatNumber(runtime?.api_keys_total ?? 0)} Key · ${formatNumber(groupBreakdown.length)} 活跃组`"
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
            </span>
          </template>
        </MetricTile>
      </div>

      <!-- 中部：时序趋势图 + 平台/分组流量占比卡 -->
      <div class="grid gap-4 lg:grid-cols-12">
        <Card
          class="lg:col-span-8"
          title="流量、Token 与费用时序趋势"
          :subtitle="rangeLabel ? `${rangeLabel}（${bucketLabel}）` : '实时网关吞吐走势'"
          body-class="px-3.5 pb-3.5 pt-2.5"
        >
          <template #actions>
            <SegmentedTabs v-model="metric" :items="metricTabs" size="sm" />
          </template>

          <div v-if="loading && !data" class="flex h-[240px] items-end gap-1.5 px-2">
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
            :empty-text="hasTraffic ? '当前聚合粒度暂无序列点' : '所选时段内暂无网关请求流量'"
          />
        </Card>

        <Card
          class="lg:col-span-4"
          title="平台 & 分组流量占比"
          subtitle="请求分发权重、缓存率与成本占比"
          body-class="p-3.5"
        >
          <template #actions>
            <SegmentedTabs v-model="shareTab" :items="shareTabs" size="sm" />
          </template>

          <div v-if="loading && !data" class="space-y-3">
            <Skeleton v-for="i in 4" :key="i" class="h-11 w-full" />
          </div>

          <div
            v-else-if="activeShareRows.length === 0"
            class="flex h-[225px] flex-col items-center justify-center rounded-md border border-dashed border-line bg-raised/30 px-4 text-center"
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
              class="rounded-md border border-line bg-raised/40 px-3 py-2"
            >
              <div class="flex items-center justify-between gap-2">
                <div class="flex min-w-0 items-center gap-1.5">
                  <PlatformBadge
                    v-if="shareTab === 'platform'"
                    :platform="row.key"
                    short
                  />
                  <span class="truncate text-xs font-medium text-fg">{{ row.label || row.key }}</span>
                </div>
                <span class="tnum shrink-0 font-mono text-xs font-semibold text-fg">
                  {{ formatNumber(row.requests) }} 次
                  <span class="font-normal text-subtle">
                    ({{ ((row.requests / shareTotalRequests) * 100).toFixed(1) }}%)
                  </span>
                </span>
              </div>

              <div class="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-overlay">
                <div
                  class="h-full rounded-full bg-accent transition-[width] duration-300"
                  :style="{
                    width: `${Math.max((row.requests / shareTotalRequests) * 100, 2)}%`,
                  }"
                />
              </div>

              <div class="tnum mt-1.5 flex flex-wrap items-center justify-between gap-2 text-2xs text-subtle">
                <span>
                  {{ formatCompact(row.tokens) }} tok · 缓存 {{ formatPercent(row.cache_hit_rate, 0) }}
                </span>
                <span>
                  TTFT {{ formatDuration(row.avg_first_token_ms) }} · ${{ formatCost(row.total_cost) }}
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
          :empty="modelRows.length === 0"
          :skeleton-rows="5"
          min-width="1050px"
          hoverable
          class="border-0 rounded-none"
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
                <span class="font-medium text-fg">{{ formatNumber(m.requests) }}</span>
                <span
                  v-if="m.success_rate !== undefined"
                  class="ml-1.5 text-2xs"
                  :class="m.success_rate >= 0.98 ? 'text-success' : 'text-warning'"
                >
                  {{ formatPercent(m.success_rate, 1) }}
                </span>
              </td>
              <td class="td tnum text-right">
                <div class="font-medium text-fg">{{ formatCompact(m.tokens) }}</div>
                <div class="text-2xs text-subtle">
                  入 {{ formatCompact(m.input_tokens) }} / 出 {{ formatCompact(m.output_tokens) }}
                </div>
              </td>
              <td class="td min-w-[9rem]">
                <div class="flex items-center gap-2">
                  <div class="h-1.5 w-16 overflow-hidden rounded-full bg-raised">
                    <div
                      class="h-full rounded-full bg-success"
                      :style="{ width: `${Math.min(100, Math.max(0, (m.cache_hit_rate ?? 0) * 100))}%` }"
                    />
                  </div>
                  <span class="tnum text-xs font-medium text-fg">
                    {{ formatPercent(m.cache_hit_rate ?? 0, 1) }}
                  </span>
                </div>
                <div v-if="m.cache_read_tokens > 0" class="tnum text-2xs text-subtle">
                  命中 {{ formatCompact(m.cache_read_tokens) }}
                </div>
              </td>
              <td class="td tnum text-right font-mono text-xs" :class="latencyColorClass(m.avg_first_token_ms)">
                {{ formatDuration(m.avg_first_token_ms) }}
              </td>
              <td class="td tnum text-right font-mono text-xs" :class="latencyColorClass(m.p95_first_token_ms)">
                {{ formatDuration(m.p95_first_token_ms) }}
              </td>
              <td class="td tnum text-right font-mono text-xs text-muted">
                {{ formatDuration(m.avg_duration_ms) }}
              </td>
              <td class="td tnum text-right font-mono text-xs text-fg">
                {{ formatTps(m.avg_tps) }}
              </td>
              <td class="td tnum text-right font-mono text-xs font-semibold text-fg">
                ${{ formatCost(m.total_cost) }}
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
          :empty="accountRows.length === 0"
          :skeleton-rows="4"
          min-width="860px"
          hoverable
          class="border-0 rounded-none"
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
              <td class="td tnum text-right font-medium text-fg">{{ formatNumber(acc.requests) }}</td>
              <td class="td tnum text-right">
                <Badge
                  :tone="(acc.success_rate ?? 1) >= 0.98 ? 'success' : (acc.success_rate ?? 1) >= 0.9 ? 'warning' : 'danger'"
                >
                  {{ formatPercent(acc.success_rate ?? 1, 1) }}
                </Badge>
              </td>
              <td class="td tnum text-right text-muted">{{ formatCompact(acc.tokens) }}</td>
              <td class="td tnum text-right text-muted">
                {{ formatPercent(acc.cache_hit_rate ?? 0, 1) }}
              </td>
              <td class="td tnum text-right font-mono text-xs text-muted">
                {{ formatDuration(acc.avg_first_token_ms) }} · {{ formatTps(acc.avg_tps) }}
              </td>
              <td class="td tnum text-right font-mono text-xs font-semibold text-fg">
                ${{ formatCost(acc.total_cost) }}
              </td>
            </tr>
          </template>
        </DataTable>
      </Card>
    </template>
  </div>
</template>
