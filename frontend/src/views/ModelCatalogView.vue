<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'

import {
  deleteModelPrice,
  fetchModelCatalog,
  listModelPrices,
  upsertModelPrice,
} from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { CatalogModelItem, ModelPrice } from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import Modal from '@/components/ui/Modal.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'
import { formatCompact, formatDateTime, formatPricePerMillion } from '@/utils/format'
import { ALL_PLATFORMS, normalizePlatform } from '@/utils/platforms'

const toast = useToastStore()

const loading = ref(true)
const refreshingLive = ref(false)
const error = ref('')

const catalogItems = ref<CatalogModelItem[]>([])
const customPrices = ref<ModelPrice[]>([])
const refreshedAt = ref<string | null>(null)
const platformErrors = ref<Record<string, string>>({})

// ---- 筛选条件 ----
const platformTab = ref('all')
const keyword = ref('')
const vendorFilter = ref('')
const sourceFilter = ref('')
const capVision = ref(false)
const capReasoning = ref(false)
const capTool = ref(false)

const copiedModelId = ref<string | null>(null)
let copyTimer: ReturnType<typeof setTimeout> | null = null

const platformTabs = computed(() => [
  { value: 'all', label: '全部平台', badge: mergedModels.value.length },
  ...ALL_PLATFORMS.map((p) => ({
    value: p.value,
    label: p.label,
    icon: p.icon,
    badge: mergedModels.value.filter((m) => m.normalizedPlatforms.includes(p.value)).length,
  })),
])

const sourceOptions = [
  { label: '官方标准牌价', value: 'official' },
  { label: '自定义改价覆盖', value: 'custom' },
  { label: '未定价模型', value: 'unpriced' },
]

function inferVendor(modelId: string, rawVendor?: string): string {
  if (rawVendor && rawVendor.trim()) return rawVendor.trim()
  const lower = modelId.toLowerCase()
  if (lower.includes('claude')) return 'Anthropic'
  if (lower.includes('gpt') || /^o[134]/.test(lower) || lower.includes('codex')) return 'OpenAI'
  if (lower.includes('gemini')) return 'Google'
  if (lower.includes('deepseek')) return 'DeepSeek'
  if (lower.includes('glm')) return 'Zhipu'
  if (lower.includes('qwen')) return 'Qwen'
  if (lower.includes('kimi') || lower.includes('moonshot')) return 'Moonshot'
  if (lower.includes('minimax')) return 'MiniMax'
  if (lower.includes('grok')) return 'xAI'
  if (lower.includes('mimo')) return 'Xiaomi'
  if (lower.includes('hunyuan') || lower.includes('hy3') || lower.includes('hy4')) return 'Tencent'
  if (lower.includes('step')) return 'StepFun'
  return 'Other'
}

function normalizePriceSource(
  item: CatalogModelItem,
  override?: ModelPrice,
): 'custom' | 'official' | 'unpriced' {
  if (override || item.price_source === 'custom') return 'custom'
  const src = (item.price_source ?? '').toLowerCase()
  if (src === 'official' || src === 'builtin' || src === 'standard' || src === 'upstream') {
    return 'official'
  }
  if (src === 'unpriced') return 'unpriced'
  if ((item.input_price ?? 0) > 0 || (item.output_price ?? 0) > 0 || item.id.endsWith('-free')) {
    return 'official'
  }
  return 'unpriced'
}

interface EnrichedModelRow extends CatalogModelItem {
  vendorLabel: string
  normalizedPlatforms: string[]
  effectiveSource: 'custom' | 'official' | 'unpriced'
  overrideRecord: ModelPrice | null
  effectiveInput: number | undefined
  effectiveOutput: number | undefined
  effectiveCacheRead: number | undefined
  effectiveCacheWrite: number | undefined
}

const customPriceMap = computed(() => {
  const m = new Map<string, ModelPrice>()
  for (const p of customPrices.value) {
    m.set(p.model.toLowerCase(), p)
  }
  return m
})

const mergedModels = computed<EnrichedModelRow[]>(() => {
  const seen = new Set<string>()
  const rows: EnrichedModelRow[] = []

  for (const item of catalogItems.value) {
    const key = item.id.toLowerCase()
    seen.add(key)
    const override = customPriceMap.value.get(key) ?? null
    const rawPlatforms =
      Array.isArray(item.platforms) && item.platforms.length > 0
        ? item.platforms
        : item.platform
          ? [item.platform]
          : []
    const normalizedPlatforms = Array.from(new Set(rawPlatforms.map((p) => normalizePlatform(p))))
    const effectiveSource = normalizePriceSource(item, override ?? undefined)

    rows.push({
      ...item,
      vendorLabel: inferVendor(item.id, item.vendor),
      normalizedPlatforms,
      effectiveSource,
      overrideRecord: override,
      effectiveInput: override ? override.input_price : item.input_price,
      effectiveOutput: override ? override.output_price : item.output_price,
      effectiveCacheRead: override ? override.cache_read_price : item.cache_read_price,
      effectiveCacheWrite: override ? override.cache_write_price : item.cache_write_price,
    })
  }

  // 把仅存在于自定义价格表但尚未出现在实时模型目录中的模型也合并展示，方便随时管理
  for (const p of customPrices.value) {
    const key = p.model.toLowerCase()
    if (seen.has(key)) continue
    rows.push({
      id: p.model,
      name: p.model,
      vendorLabel: inferVendor(p.model),
      platforms: [],
      normalizedPlatforms: [],
      effectiveSource: 'custom',
      overrideRecord: p,
      effectiveInput: p.input_price,
      effectiveOutput: p.output_price,
      effectiveCacheRead: p.cache_read_price,
      effectiveCacheWrite: p.cache_write_price,
      description: p.notes,
    })
  }

  return rows
})

const vendorOptions = computed(() => {
  const set = new Set<string>()
  for (const m of mergedModels.value) {
    if (m.vendorLabel) set.add(m.vendorLabel)
  }
  return Array.from(set)
    .sort()
    .map((v) => ({ label: v, value: v }))
})

const filteredModels = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return mergedModels.value.filter((m) => {
    if (platformTab.value !== 'all' && !m.normalizedPlatforms.includes(platformTab.value as never)) {
      return false
    }
    if (vendorFilter.value && m.vendorLabel !== vendorFilter.value) return false
    if (sourceFilter.value && m.effectiveSource !== sourceFilter.value) return false
    if (capVision.value && !m.supports_images) return false
    if (capReasoning.value && !m.supports_reasoning) return false
    if (capTool.value && !m.supports_tool_call) return false
    if (!q) return true
    return (
      m.id.toLowerCase().includes(q) ||
      (m.name ?? '').toLowerCase().includes(q) ||
      m.vendorLabel.toLowerCase().includes(q) ||
      (m.overrideRecord?.notes ?? '').toLowerCase().includes(q)
    )
  })
})

const hasActiveFilter = computed(
  () =>
    platformTab.value !== 'all' ||
    Boolean(keyword.value.trim()) ||
    Boolean(vendorFilter.value) ||
    Boolean(sourceFilter.value) ||
    capVision.value ||
    capReasoning.value ||
    capTool.value,
)

function resetFilters(): void {
  platformTab.value = 'all'
  keyword.value = ''
  vendorFilter.value = ''
  sourceFilter.value = ''
  capVision.value = false
  capReasoning.value = false
  capTool.value = false
}

async function loadData(forceRefresh = false): Promise<void> {
  if (forceRefresh) refreshingLive.value = true
  else loading.value = true
  error.value = ''
  try {
    const [catalogRes, pricesRes] = await Promise.all([
      fetchModelCatalog(forceRefresh, ''),
      listModelPrices().catch(() => ({ items: [] as ModelPrice[], total: 0 })),
    ])
    catalogItems.value = catalogRes.items ?? []
    customPrices.value = pricesRes.items?.length ? pricesRes.items : (catalogRes.prices ?? [])
    refreshedAt.value = catalogRes.refreshed_at ?? null
    platformErrors.value = catalogRes.errors ?? {}
    if (forceRefresh) {
      toast.success('已从四平台实时同步模型目录', `当前聚合 ${catalogItems.value.length} 个模型`)
    }
  } catch (err) {
    error.value = toMessage(err, '加载模型目录与价格库失败')
  } finally {
    loading.value = false
    refreshingLive.value = false
  }
}

async function copyModelId(id: string): Promise<void> {
  const ok = await copyText(id)
  if (!ok) {
    toast.error('复制模型 ID 失败')
    return
  }
  copiedModelId.value = id
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => {
    copyTimer = null
    if (copiedModelId.value === id) copiedModelId.value = null
  }, 1500)
  toast.success('已复制模型 ID', id)
}

// ---- 改价与恢复官方标准价弹窗 ----
const priceModalOpen = ref(false)
const savingPrice = ref(false)
const priceError = ref('')
const editingRow = ref<EnrichedModelRow | null>(null)
const revertTarget = ref<EnrichedModelRow | null>(null)
const reverting = ref(false)

const priceForm = reactive({
  model: '',
  input_price: '0',
  output_price: '0',
  cache_read_price: '0',
  cache_write_price: '0',
  notes: '',
})

function openEditPrice(row?: EnrichedModelRow): void {
  editingRow.value = row ?? null
  priceError.value = ''
  priceForm.model = row?.id ?? ''
  priceForm.input_price = String(row?.effectiveInput ?? 0)
  priceForm.output_price = String(row?.effectiveOutput ?? 0)
  priceForm.cache_read_price = String(row?.effectiveCacheRead ?? 0)
  priceForm.cache_write_price = String(row?.effectiveCacheWrite ?? 0)
  priceForm.notes = row?.overrideRecord?.notes ?? ''
  priceModalOpen.value = true
}

function fillStandardCacheRatio(): void {
  const inp = Number(priceForm.input_price)
  if (!Number.isFinite(inp) || inp <= 0) return
  priceForm.cache_read_price = String(Number((inp * 0.1).toFixed(4)))
  priceForm.cache_write_price = String(Number((inp * 1.25).toFixed(4)))
}

async function submitPrice(): Promise<void> {
  const modelName = priceForm.model.trim()
  if (!modelName) {
    priceError.value = '请输入模型 ID'
    return
  }
  const inp = Number(priceForm.input_price)
  const out = Number(priceForm.output_price)
  const cr = Number(priceForm.cache_read_price)
  const cw = Number(priceForm.cache_write_price)
  if ([inp, out, cr, cw].some((n) => !Number.isFinite(n) || n < 0)) {
    priceError.value = '四项单价必须为不小于 0 的合法数字'
    return
  }

  savingPrice.value = true
  priceError.value = ''
  try {
    await upsertModelPrice({
      id: editingRow.value?.overrideRecord?.id,
      model: modelName,
      input_price: inp,
      output_price: out,
      cache_read_price: cr,
      cache_write_price: cw,
      notes: priceForm.notes.trim(),
    })
    toast.success('模型自定义单价已保存并实时生效', modelName)
    priceModalOpen.value = false
    await loadData(false)
  } catch (err) {
    priceError.value = toMessage(err, '保存模型价格失败')
  } finally {
    savingPrice.value = false
  }
}

async function confirmRevertOfficial(row?: EnrichedModelRow | null): Promise<void> {
  const target = row ?? revertTarget.value
  if (!target?.overrideRecord) return
  reverting.value = true
  try {
    await deleteModelPrice(target.overrideRecord.id)
    toast.success('已恢复官方标准牌价', target.id)
    revertTarget.value = null
    priceModalOpen.value = false
    await loadData(false)
  } catch (err) {
    toast.error('恢复官方标准牌价失败', toMessage(err))
  } finally {
    reverting.value = false
  }
}

onMounted(() => {
  void loadData(false)
})

onBeforeUnmount(() => {
  if (copyTimer) {
    clearTimeout(copyTimer)
    copyTimer = null
  }
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部指标概览 -->
    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <MetricTile
        label="聚合可用模型总数"
        :value="mergedModels.length"
        :hint="refreshedAt ? `上次同步 ${formatDateTime(refreshedAt)}` : '支持四源实时探测与去重合并'"
        icon="cpu"
        tone="accent"
        :loading="loading && catalogItems.length === 0"
      />
      <MetricTile
        label="官方标准牌价命中"
        :value="mergedModels.filter((m) => m.effectiveSource === 'official').length"
        hint="智能兼容 vendor/ 前缀与点号/横线版本号"
        icon="checkCircle"
        tone="success"
        :loading="loading && catalogItems.length === 0"
      />
      <MetricTile
        label="自定义覆盖改价"
        :value="customPrices.length"
        hint="数据库热生效，支持随时一键恢复官方价"
        icon="coins"
        tone="warning"
        :loading="loading && catalogItems.length === 0"
      />
      <MetricTile
        label="多模态与推理模型"
        :value="mergedModels.filter((m) => m.supports_reasoning || m.supports_images).length"
        :hint="`推理 ${mergedModels.filter((m) => m.supports_reasoning).length} · 视觉 ${mergedModels.filter((m) => m.supports_images).length}`"
        icon="sparkles"
        tone="info"
        :loading="loading && catalogItems.length === 0"
      />
    </div>

    <!-- 平台错误轻提示 -->
    <div
      v-if="Object.keys(platformErrors).length > 0"
      class="flex flex-wrap items-center gap-2 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-2xs text-warning"
    >
      <Icon name="alert" :size="14" />
      <span>部分平台实时拉取跳过或使用缓存：</span>
      <span
        v-for="(msg, plat) in platformErrors"
        :key="plat"
        class="rounded border border-warning/30 bg-surface px-1.5 py-0.5 font-mono"
      >
        {{ plat }}: {{ msg }}
      </span>
    </div>

    <!-- 筛选与操作工作栏 -->
    <div class="card space-y-3 p-3.5">
      <div class="flex flex-wrap items-center justify-between gap-2.5">
        <SegmentedTabs v-model="platformTab" :items="platformTabs" />

        <div class="flex flex-wrap items-center gap-2">
          <Button variant="secondary" size="sm" @click="openEditPrice()">
            <template #leading><Icon name="plus" :size="13" /></template>
            手动新增定价
          </Button>
          <Button
            variant="primary"
            size="sm"
            :loading="refreshingLive"
            @click="loadData(true)"
          >
            <template #leading><Icon name="refresh" :size="13" /></template>
            从四平台实时刷新拉取模型
          </Button>
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-2.5">
        <div class="w-full min-w-[14rem] sm:w-64">
          <Input
            v-model="keyword"
            size="sm"
            placeholder="搜索模型 ID / 显示名 / 厂商…"
          />
        </div>

        <div class="w-40">
          <Select
            v-model="vendorFilter"
            size="sm"
            placeholder="全部厂商"
            :options="vendorOptions"
          />
        </div>

        <div class="w-44">
          <Select
            v-model="sourceFilter"
            size="sm"
            placeholder="全部价格来源"
            :options="sourceOptions"
          />
        </div>

        <!-- 能力筛选胶囊按钮 -->
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            :aria-pressed="capVision"
            class="inline-flex h-7 items-center gap-1 rounded-md border px-2.5 text-xs font-medium transition"
            :class="
              capVision
                ? 'border-accent bg-accent/12 text-accent'
                : 'border-line bg-raised text-muted hover:border-line-strong hover:text-fg'
            "
            @click="capVision = !capVision"
          >
            <Icon name="eye" :size="12" />
            <span>视觉</span>
          </button>

          <button
            type="button"
            :aria-pressed="capReasoning"
            class="inline-flex h-7 items-center gap-1 rounded-md border px-2.5 text-xs font-medium transition"
            :class="
              capReasoning
                ? 'border-accent bg-accent/12 text-accent'
                : 'border-line bg-raised text-muted hover:border-line-strong hover:text-fg'
            "
            @click="capReasoning = !capReasoning"
          >
            <Icon name="sparkles" :size="12" />
            <span>推理</span>
          </button>

          <button
            type="button"
            :aria-pressed="capTool"
            class="inline-flex h-7 items-center gap-1 rounded-md border px-2.5 text-xs font-medium transition"
            :class="
              capTool
                ? 'border-accent bg-accent/12 text-accent'
                : 'border-line bg-raised text-muted hover:border-line-strong hover:text-fg'
            "
            @click="capTool = !capTool"
          >
            <Icon name="terminal" :size="12" />
            <span>工具调用</span>
          </button>
        </div>

        <Button v-if="hasActiveFilter" variant="ghost" size="sm" @click="resetFilters">
          清空筛选
        </Button>

        <span class="ml-auto tnum text-2xs text-subtle">
          当前展示 {{ filteredModels.length }} / {{ mergedModels.length }} 个模型
        </span>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="loadData(false)">重试</Button>
    </p>

    <!-- 高密度模型与标准牌价表格 -->
    <DataTable
      :columns="11"
      :loading="loading"
      :empty="filteredModels.length === 0"
      :skeleton-rows="10"
      min-width="1360px"
      hoverable
    >
      <template #head>
        <th class="th">模型 ID / 显示名</th>
        <th class="th">厂商</th>
        <th class="th">覆盖平台</th>
        <th class="th text-right">上下文 / 最大输出</th>
        <th class="th">能力标签</th>
        <th class="th text-right">输入 ($/1M)</th>
        <th class="th text-right">输出 ($/1M)</th>
        <th class="th text-right">缓存读 ($/1M)</th>
        <th class="th text-right">缓存写 ($/1M)</th>
        <th class="th">价格来源</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="cpu"
          :title="hasActiveFilter ? '没有符合筛选条件的模型' : '模型目录为空'"
          :description="
            hasActiveFilter
              ? '请放宽平台、厂商或能力筛选条件。'
              : '点击右上角「从四平台实时刷新拉取模型」主动探测四端上游模型列表。'
          "
        >
          <Button v-if="hasActiveFilter" variant="secondary" size="sm" @click="resetFilters">
            清空筛选
          </Button>
          <Button v-else variant="primary" size="sm" :loading="refreshingLive" @click="loadData(true)">
            实时拉取模型
          </Button>
        </EmptyState>
      </template>

      <template #body>
        <tr v-for="m in filteredModels" :key="m.id">
          <td class="td max-w-[18rem]">
            <div class="flex items-center gap-1.5">
              <code class="truncate font-mono text-xs font-semibold text-fg" :title="m.id">
                {{ m.id }}
              </code>
              <button
                type="button"
                class="shrink-0 rounded p-0.5 text-subtle transition hover:bg-raised hover:text-fg"
                title="复制模型 ID"
                @click="copyModelId(m.id)"
              >
                <Icon
                  :name="copiedModelId === m.id ? 'check' : 'copy'"
                  :size="12"
                  :class="copiedModelId === m.id ? 'text-success' : ''"
                />
              </button>
            </div>
            <p
              v-if="m.name && m.name !== m.id"
              class="mt-0.5 truncate text-2xs text-subtle"
              :title="m.name"
            >
              {{ m.name }}
            </p>
          </td>

          <td class="td">
            <Badge tone="neutral">{{ m.vendorLabel }}</Badge>
          </td>

          <td class="td">
            <div v-if="m.normalizedPlatforms.length > 0" class="flex flex-wrap gap-1">
              <PlatformBadge
                v-for="plat in m.normalizedPlatforms"
                :key="plat"
                :platform="plat"
                short
                :show-icon="false"
              />
            </div>
            <span v-else class="text-2xs text-subtle">仅价格规则</span>
          </td>

          <td class="td tnum text-right font-mono text-2xs text-muted">
            <template v-if="m.context_window || m.max_output_tokens">
              <span>{{ m.context_window ? formatCompact(m.context_window) : '—' }}</span>
              <span class="text-subtle"> / </span>
              <span>{{ m.max_output_tokens ? formatCompact(m.max_output_tokens) : '—' }}</span>
            </template>
            <span v-else class="text-subtle">—</span>
          </td>

          <td class="td">
            <div class="flex items-center gap-1">
              <span
                v-if="m.supports_images"
                class="inline-flex items-center gap-0.5 rounded border border-info/30 bg-info/10 px-1.5 py-px text-2xs text-info"
                title="支持视觉图片输入"
              >
                <Icon name="eye" :size="11" />
                视觉
              </span>
              <span
                v-if="m.supports_reasoning"
                class="inline-flex items-center gap-0.5 rounded border border-accent/30 bg-accent/10 px-1.5 py-px text-2xs text-accent"
                title="支持思维链深度推理"
              >
                <Icon name="sparkles" :size="11" />
                推理
              </span>
              <span
                v-if="m.supports_tool_call"
                class="inline-flex items-center gap-0.5 rounded border border-success/30 bg-success/10 px-1.5 py-px text-2xs text-success"
                title="支持 Function / Tool Calling"
              >
                <Icon name="terminal" :size="11" />
                工具
              </span>
              <span
                v-if="!m.supports_images && !m.supports_reasoning && !m.supports_tool_call"
                class="text-2xs text-subtle"
              >
                文本对话
              </span>
            </div>
          </td>

          <td class="td tnum text-right font-mono text-xs text-fg">
            {{ formatPricePerMillion(m.effectiveInput) }}
          </td>

          <td class="td tnum text-right font-mono text-xs text-fg">
            {{ formatPricePerMillion(m.effectiveOutput) }}
          </td>

          <td class="td tnum text-right font-mono text-xs text-muted">
            {{ formatPricePerMillion(m.effectiveCacheRead) }}
          </td>

          <td class="td tnum text-right font-mono text-xs text-muted">
            {{ formatPricePerMillion(m.effectiveCacheWrite) }}
          </td>

          <td class="td">
            <Badge v-if="m.effectiveSource === 'custom'" tone="warning" dot>自定义改价</Badge>
            <Badge v-else-if="m.effectiveSource === 'official'" tone="success" dot>官方标准</Badge>
            <Badge v-else tone="neutral">未定价</Badge>
          </td>

          <td class="td text-right">
            <div class="inline-flex items-center justify-end gap-1">
              <Button variant="secondary" size="xs" @click="openEditPrice(m)">
                <template #leading><Icon name="edit" :size="11" /></template>
                改价
              </Button>
              <Button
                v-if="m.overrideRecord"
                variant="ghost"
                size="xs"
                title="删除自定义改价，恢复官方标准牌价"
                @click="revertTarget = m"
              >
                <Icon name="rotate" :size="12" class="text-warning" />
              </Button>
            </div>
          </td>
        </tr>
      </template>
    </DataTable>

    <!-- 改价弹窗 -->
    <Modal
      :open="priceModalOpen"
      size="md"
      :title="editingRow ? `设置模型单价 · ${editingRow.id}` : '新增自定义模型定价'"
      subtitle="计价单位均为「美元 / 1,000,000 Tokens ($/1M)」，保存后对网关计费器实时生效"
      @update:open="priceModalOpen = $event"
    >
      <form class="space-y-3.5" @submit.prevent="submitPrice">
        <p
          v-if="priceError"
          class="flex items-center gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="13" />
          <span>{{ priceError }}</span>
        </p>

        <Field label="模型 ID (model)" required :hint="editingRow ? '精确绑定此模型标识' : '例如 claude-sonnet-4-6 或 gemini-4-neon-high'">
          <Input
            v-model="priceForm.model"
            mono
            :readonly="Boolean(editingRow)"
            placeholder="输入完整模型 ID"
          />
        </Field>

        <div class="grid gap-3 sm:grid-cols-2">
          <Field label="输入单价 ($ / 1M Tokens)" required>
            <Input v-model="priceForm.input_price" type="number" min="0" step="0.0001" mono />
          </Field>
          <Field label="输出单价 ($ / 1M Tokens)" required>
            <Input v-model="priceForm.output_price" type="number" min="0" step="0.0001" mono />
          </Field>
        </div>

        <div class="flex items-center justify-between">
          <span class="text-2xs text-subtle">缓存单价配置</span>
          <button
            type="button"
            class="text-2xs font-medium text-accent hover:underline"
            @click="fillStandardCacheRatio"
          >
            按 Anthropic 标准比例推算（读 0.1x / 写 1.25x）
          </button>
        </div>

        <div class="grid gap-3 sm:grid-cols-2">
          <Field label="缓存命中读 ($ / 1M Tokens)">
            <Input v-model="priceForm.cache_read_price" type="number" min="0" step="0.0001" mono />
          </Field>
          <Field label="缓存写入 ($ / 1M Tokens)">
            <Input v-model="priceForm.cache_write_price" type="number" min="0" step="0.0001" mono />
          </Field>
        </div>

        <Field label="改价备注">
          <Textarea v-model="priceForm.notes" :rows="2" placeholder="可选：记录折扣原因或渠道说明" />
        </Field>
      </form>

      <template #footer>
        <div class="flex w-full items-center justify-between gap-2">
          <div>
            <Button
              v-if="editingRow?.overrideRecord"
              variant="danger"
              size="sm"
              :loading="reverting"
              @click="confirmRevertOfficial(editingRow)"
            >
              <template #leading><Icon name="rotate" :size="12" /></template>
              恢复官方标准价
            </Button>
          </div>
          <div class="flex items-center gap-2">
            <Button variant="ghost" :disabled="savingPrice" @click="priceModalOpen = false">取消</Button>
            <Button variant="primary" :loading="savingPrice" @click="submitPrice">保存定价</Button>
          </div>
        </div>
      </template>
    </Modal>

    <ConfirmDialog
      :open="revertTarget !== null"
      title="恢复官方标准牌价"
      :message="`确定移除「${revertTarget?.id ?? ''}」的自定义单价覆盖并恢复使用内置官方标准牌价吗？`"
      confirm-text="恢复官方价"
      :loading="reverting"
      @update:open="revertTarget = null"
      @confirm="confirmRevertOfficial(null)"
    />
  </div>
</template>
