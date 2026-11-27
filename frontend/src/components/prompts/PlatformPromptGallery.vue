<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'

import { toMessage } from '@/api/client'
import {
  applyPromptCatalogItem,
  createPromptTemplate,
  updateGroupPrompt,
} from '@/api/endpoints'
import type {
  ApplyPromptCatalogPayload,
  ApplyPromptCatalogResponse,
  Group,
  PromptCatalogItem,
  PromptInjectMode,
  PromptTemplate,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import Skeleton from '@/components/ui/Skeleton.vue'
import Switch from '@/components/ui/Switch.vue'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'

import {
  CATALOG_PLATFORMS,
  SCENARIO_TAGS,
  getAgyModeMeta,
  getCatalogPlatformMeta,
  normalizeCatalogPlatform,
  scenarioTone,
} from './catalogData'

const props = withDefaults(
  defineProps<{
    items: PromptCatalogItem[]
    loading?: boolean
    groups: Group[]
    templates: PromptTemplate[]
    initialPlatform?: string
  }>(),
  {
    loading: false,
    initialPlatform: 'all',
  },
)

const emit = defineEmits<{
  (event: 'refresh'): void
  (event: 'applied', payload: ApplyPromptCatalogResponse): void
  (event: 'open-guide'): void
}>()

const toast = useToastStore()

const activePlatform = ref<string>(props.initialPlatform || 'all')
const activeScenario = ref<string>('all')
const searchQuery = ref('')
const copiedId = ref<string | null>(null)
let copyTimer: ReturnType<typeof setTimeout> | null = null

watch(
  () => props.initialPlatform,
  (val) => {
    if (val) activePlatform.value = val
  },
)

// 统计每个平台收录条数
const platformCounts = computed<Record<string, number>>(() => {
  const counts: Record<string, number> = { all: props.items.length }
  for (const item of props.items) {
    const k = normalizeCatalogPlatform(item.platform)
    counts[k] = (counts[k] ?? 0) + 1
  }
  return counts
})

const filteredItems = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  return props.items.filter((item) => {
    const plat = normalizeCatalogPlatform(item.platform)
    if (activePlatform.value !== 'all' && plat !== activePlatform.value) {
      return false
    }
    if (activeScenario.value !== 'all' && item.scenario_tag !== activeScenario.value) {
      return false
    }
    if (!q) return true
    const haystack = [
      item.id,
      item.name,
      item.platform,
      item.platform_label ?? '',
      item.category,
      item.role ?? '',
      item.scenario_tag ?? '',
      item.description,
      ...(item.tags ?? []),
      ...(item.highlights ?? []),
      item.content,
    ]
      .join(' ')
      .toLowerCase()
    return haystack.includes(q)
  })
})

function isAlreadyInTemplates(item: PromptCatalogItem): boolean {
  if (item.already_imported) return true
  const targetId = item.id.trim().toLowerCase()
  const targetName = item.name.trim().toLowerCase()
  return props.templates.some((tpl) => {
    const n = tpl.name.trim().toLowerCase()
    return n === targetId || n === targetName
  })
}

function resetFilters(): void {
  activePlatform.value = 'all'
  activeScenario.value = 'all'
  searchQuery.value = ''
}

async function handleCopyPrompt(item: PromptCatalogItem): Promise<void> {
  const ok = await copyText(item.content)
  if (!ok) {
    toast.error('复制失败，请打开全文预览手动复制')
    return
  }
  copiedId.value = item.id
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => {
    if (copiedId.value === item.id) copiedId.value = null
  }, 1800)
  toast.success('提示词已复制到剪贴板', `${item.name} (${item.content.length} chars)`)
}

// ---- 全文结构化 / 源码预览弹窗 ----
const previewItem = ref<PromptCatalogItem | null>(null)
const previewTab = ref<string>('structured')
const previewTabItems = [
  { value: 'structured', label: '结构化章节视图', icon: 'fileText' as const },
  { value: 'raw', label: '原始提示词源码', icon: 'code' as const },
]

interface ParsedPromptBlock {
  kind: 'xml' | 'heading' | 'list' | 'paragraph'
  title?: string
  lines: string[]
}

const parsedPreviewBlocks = computed<ParsedPromptBlock[]>(() => {
  const raw = previewItem.value?.content ?? ''
  if (!raw.trim()) return []

  const blocks: ParsedPromptBlock[] = []
  const lines = raw.replace(/\r\n/g, '\n').split('\n')
  let idx = 0

  while (idx < lines.length) {
    const line = lines[idx]!
    const trimmed = line.trim()
    if (!trimmed) {
      idx += 1
      continue
    }

    // 识别顶层 <tag ...> ... </tag> 块
    const xmlMatch = trimmed.match(/^<([a-zA-Z0-9_-]+)(\s[^>]*)?>$/)
    if (xmlMatch) {
      const tagName = xmlMatch[1]!
      const closeTag = `</${tagName}>`
      const inner: string[] = []
      idx += 1
      while (idx < lines.length && lines[idx]!.trim() !== closeTag) {
        inner.push(lines[idx]!)
        idx += 1
      }
      if (idx < lines.length && lines[idx]!.trim() === closeTag) {
        idx += 1
      }
      blocks.push({
        kind: 'xml',
        title: `<${tagName}>`,
        lines: inner,
      })
      continue
    }

    // 识别 Markdown 标题或方括号分段头
    const headingMatch = trimmed.match(/^(#{1,3})\s+(.+)$/) || trimmed.match(/^\[(.+)\]$/)
    if (headingMatch) {
      const title = headingMatch[2] ?? headingMatch[1] ?? trimmed
      blocks.push({ kind: 'heading', title, lines: [] })
      idx += 1
      continue
    }

    // 识别连续列表项
    if (/^(?:[-*]|\d+\.)\s+/.test(trimmed)) {
      const itemsList: string[] = []
      while (idx < lines.length && /^(?:[-*]|\d+\.)\s+/.test(lines[idx]!.trim())) {
        itemsList.push(lines[idx]!.trim().replace(/^(?:[-*]|\d+\.)\s+/, ''))
        idx += 1
      }
      blocks.push({ kind: 'list', lines: itemsList })
      continue
    }

    // 普通段落
    const para: string[] = []
    while (
      idx < lines.length &&
      lines[idx]!.trim() !== '' &&
      !/^(?:[-*]|\d+\.)\s+/.test(lines[idx]!.trim()) &&
      !/^(#{1,3})\s+/.test(lines[idx]!.trim()) &&
      !/^<([a-zA-Z0-9_-]+)(\s[^>]*)?>$/.test(lines[idx]!.trim())
    ) {
      para.push(lines[idx]!)
      idx += 1
    }
    blocks.push({ kind: 'paragraph', lines: para })
  }
  return blocks
})

function openPreview(item: PromptCatalogItem): void {
  previewItem.value = item
  previewTab.value = 'structured'
}

function lineCount(text: string): number {
  if (!text) return 0
  return text.split(/\r?\n/).length
}

// ---- 一键导入到模板库（可选绑定分组 & 启用开关） ----
const applyModalOpen = ref(false)
const applyTarget = ref<PromptCatalogItem | null>(null)
const applying = ref(false)
const applyError = ref('')

type BindTargetMode = 'library_only' | 'single_group' | 'platform_groups'

const applyForm = reactive({
  name: '',
  priority: '20',
  bindTarget: 'library_only' as BindTargetMode,
  groupId: '',
  injectMode: 'append_global' as PromptInjectMode,
  enabled: true,
})

const injectModeOptions: Array<{ label: string; value: PromptInjectMode }> = [
  { label: '叠加在全局底线后 (append_global)', value: 'append_global' },
  { label: '覆盖全局底线 (override_global)', value: 'override_global' },
  { label: '强制接管并剥离客户端 System (replace_all)', value: 'replace_all' },
]

const groupSelectOptions = computed(() =>
  props.groups.map((g) => ({
    label: `#${g.id} ${g.name} (${g.platform})`,
    value: String(g.id),
  })),
)

const gatewayBindablePlatforms = new Set(['antigravity', 'workbuddy', 'commandcode', 'opencode'])

const canBindPlatformGroups = computed(() => {
  const plat = normalizeCatalogPlatform(applyTarget.value?.platform)
  return gatewayBindablePlatforms.has(plat) && props.groups.some((g) => g.platform === plat)
})

function toSlugTemplateName(item: PromptCatalogItem): string {
  const raw = item.id || item.name
  return raw.trim().slice(0, 110)
}

function openApplyModal(item: PromptCatalogItem): void {
  // 关闭全文预览弹窗避免嵌套弹窗滚动锁叠加
  if (previewItem.value) {
    previewItem.value = null
  }
  applyTarget.value = item
  applyError.value = ''
  applyForm.name = toSlugTemplateName(item)
  applyForm.priority = String(item.priority ?? 25)
  applyForm.injectMode = (item.recommended_inject_mode as PromptInjectMode) || 'append_global'
  applyForm.enabled = true

  const normPlat = normalizeCatalogPlatform(item.platform)
  const matchingGroup = props.groups.find((g) => g.platform === normPlat)
  if (matchingGroup) {
    applyForm.groupId = String(matchingGroup.id)
  } else if (props.groups[0]) {
    applyForm.groupId = String(props.groups[0].id)
  } else {
    applyForm.groupId = ''
  }
  applyForm.bindTarget = 'library_only'
  applyModalOpen.value = true
}

onBeforeUnmount(() => {
  if (copyTimer) clearTimeout(copyTimer)
})

async function submitApply(): Promise<void> {
  const item = applyTarget.value
  if (!item || applying.value) return
  if (applyForm.bindTarget === 'single_group' && !applyForm.groupId) {
    applyError.value = '请选择要绑定的目标路由分组'
    return
  }

  applying.value = true
  applyError.value = ''
  try {
    const parsedPriority =
      applyForm.priority.trim() !== '' && Number.isFinite(Number(applyForm.priority))
        ? Math.trunc(Number(applyForm.priority))
        : (item.priority ?? 25)

    const payload: ApplyPromptCatalogPayload = {
      catalog_id: item.id,
      name: applyForm.name.trim() || item.id,
      priority: parsedPriority,
      enabled: applyForm.enabled,
      inject_mode: applyForm.injectMode,
    }

    if (applyForm.bindTarget === 'single_group' && applyForm.groupId) {
      payload.group_id = Number(applyForm.groupId)
    } else if (applyForm.bindTarget === 'platform_groups') {
      payload.platform = normalizeCatalogPlatform(item.platform)
    }

    let result: ApplyPromptCatalogResponse
    try {
      result = await applyPromptCatalogItem(payload)
    } catch (apiErr) {
      // 兼容旧版尚未挂载 /prompts/catalog/apply 路由的后端进程：自动降级为标准创建模板 + 分组绑定
      const msg = toMessage(apiErr)
      if (!msg.includes('404') && !msg.toLowerCase().includes('not found')) {
        throw apiErr
      }
      const createdTpl = await createPromptTemplate({
        name: payload.name || item.id,
        category: item.category || 'coding',
        priority: parsedPriority,
        description: `[${item.platform_label || item.platform}] ${item.description}`.slice(0, 480),
        content: item.content,
      })
      let updatedGroup: Group | null = null
      if (payload.group_id) {
        const targetGrp = props.groups.find((g) => g.id === payload.group_id)
        updatedGroup = await updateGroupPrompt(payload.group_id, {
          prompt_inject_mode: applyForm.enabled ? applyForm.injectMode : 'off',
          system_prompt: targetGrp?.system_prompt ?? '',
          prompt_template_id: createdTpl.id,
        })
      }
      result = {
        ok: true,
        created: true,
        catalog_id: item.id,
        template: createdTpl,
        group: updatedGroup,
        message: updatedGroup
          ? `已导入模板「${createdTpl.name}」并应用到分组「${updatedGroup.name}」`
          : `已导入模板「${createdTpl.name}」到系统提示词模板库`,
      }
    }

    toast.success(
      result.message || '平台提示词已一键导入生效',
      result.template?.name ? `模板标识：${result.template.name}` : item.name,
    )
    applyModalOpen.value = false
    if (previewItem.value?.id === item.id) {
      previewItem.value = null
    }
    emit('applied', result)
  } catch (err) {
    applyError.value = toMessage(err, '导入平台提示词失败')
  } finally {
    applying.value = false
  }
}
</script>

<template>
  <Card
    title="各平台系统提示词图鉴库 (Mainstream Platform Prompt Collection)"
    subtitle="收录 11 大主流编程智能体与 CLI 原生指令骨架、AGY 拦截接续桥与工具调用协议，支持全文检索、结构化审阅与一键导入绑定"
    body-class="p-4 space-y-4"
  >
    <template #actions>
      <div class="flex flex-wrap items-center gap-2">
        <Button variant="subtle" size="xs" @click="emit('open-guide')">
          <template #leading><Icon name="shield" :size="12" /></template>
          查看 AGY 拦截接续架构说明
        </Button>
        <Button variant="secondary" size="xs" :loading="loading" @click="emit('refresh')">
          <template #leading><Icon name="refresh" :size="12" /></template>
          同步图鉴库
        </Button>
      </div>
    </template>

    <!-- 11 大平台胶囊筛选栏 -->
    <div class="space-y-2.5">
      <div class="flex flex-wrap items-center gap-1.5" role="tablist" aria-label="平台提示词筛选">
        <button
          v-for="plat in CATALOG_PLATFORMS"
          :key="plat.key"
          type="button"
          role="tab"
          :aria-selected="activePlatform === plat.key"
          class="inline-flex h-7 items-center gap-1.5 rounded-md border px-2.5 text-xs font-medium transition select-none"
          :class="
            activePlatform === plat.key
              ? 'border-accent/60 bg-accent/12 text-accent shadow-panel'
              : 'border-line bg-raised/70 text-muted hover:border-line-strong hover:text-fg'
          "
          @click="activePlatform = plat.key"
        >
          <Icon :name="plat.icon" :size="12" />
          <span>{{ plat.label }}</span>
          <code
            v-if="plat.codeLabel"
            class="rounded border border-current/20 bg-surface/60 px-1 py-px font-mono text-[10px]"
          >
            {{ plat.codeLabel }}
          </code>
          <span
            class="tnum rounded px-1 py-px font-mono text-[10px] leading-3"
            :class="
              activePlatform === plat.key
                ? 'bg-accent/20 text-accent'
                : 'bg-overlay text-subtle'
            "
          >
            {{ platformCounts[plat.key] ?? 0 }}
          </span>
        </button>
      </div>

      <!-- 场景标签 + 搜索框复合工具行 -->
      <div class="flex flex-wrap items-center justify-between gap-2.5 rounded-md border border-line bg-raised/45 px-3 py-2">
        <div class="flex flex-wrap items-center gap-1.5">
          <span class="text-2xs font-medium text-subtle">场景定位：</span>
          <button
            type="button"
            class="rounded border px-2 py-0.5 text-2xs font-medium transition"
            :class="
              activeScenario === 'all'
                ? 'border-line-strong bg-surface text-fg shadow-panel'
                : 'border-transparent text-muted hover:text-fg'
            "
            @click="activeScenario = 'all'"
          >
            全部场景
          </button>
          <button
            v-for="sc in SCENARIO_TAGS"
            :key="sc.value"
            type="button"
            :title="sc.desc"
            class="rounded border px-2 py-0.5 text-2xs font-medium transition"
            :class="
              activeScenario === sc.value
                ? 'border-accent/50 bg-accent/12 text-accent'
                : 'border-transparent text-muted hover:bg-surface/60 hover:text-fg'
            "
            @click="activeScenario = sc.value"
          >
            {{ sc.label }}
          </button>
        </div>

        <div class="flex w-full items-center gap-2 sm:w-auto">
          <div class="relative flex-1 sm:w-72">
            <Icon
              name="search"
              :size="13"
              class="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-subtle"
            />
            <input
              v-model="searchQuery"
              type="text"
              class="field field-sm pl-7"
              placeholder="搜索平台、标签、apply_patch、提示词正文…"
            />
          </div>
          <Button
            v-if="activePlatform !== 'all' || activeScenario !== 'all' || searchQuery.trim()"
            variant="ghost"
            size="xs"
            @click="resetFilters"
          >
            重置
          </Button>
        </div>
      </div>
    </div>

    <!-- 骨架态 -->
    <div v-if="loading && items.length === 0" class="grid gap-3.5 md:grid-cols-2 2xl:grid-cols-3">
      <Skeleton v-for="n in 6" :key="n" class="h-64 w-full" />
    </div>

    <!-- 空状态 -->
    <EmptyState
      v-else-if="filteredItems.length === 0"
      icon="search"
      title="未找到匹配的平台提示词"
      description="尝试切换上方平台胶囊、场景标签或清空检索关键词。"
    >
      <Button variant="secondary" size="sm" @click="resetFilters">显示全部收录提示词</Button>
    </EmptyState>

    <!-- 图鉴卡片矩阵 -->
    <div v-else class="grid gap-3.5 md:grid-cols-2 2xl:grid-cols-3">
      <article
        v-for="item in filteredItems"
        :key="item.id"
        class="flex flex-col justify-between rounded-panel border border-line bg-surface p-3.5 shadow-panel transition hover:border-line-strong"
      >
        <div class="space-y-2.5">
          <!-- 顶部徽标栏 -->
          <div class="flex flex-wrap items-center justify-between gap-1.5">
            <div class="flex flex-wrap items-center gap-1.5">
              <Badge :tone="getCatalogPlatformMeta(item.platform).tone" dot>
                {{ item.platform_label || getCatalogPlatformMeta(item.platform).label }}
              </Badge>
              <Badge :tone="scenarioTone(item.scenario_tag || '')">
                {{ item.scenario_tag || '原生身份' }}
              </Badge>
              <Badge v-if="item.role" tone="neutral" mono>{{ item.role }}</Badge>
            </div>

            <Badge v-if="isAlreadyInTemplates(item)" tone="success" dot>已入模板库</Badge>
            <span v-else class="tnum font-mono text-2xs text-subtle">
              {{ item.content.length }} chars
            </span>
          </div>

          <!-- 标题与工程定位说明 -->
          <div>
            <div class="flex items-baseline justify-between gap-2">
              <h3 class="truncate text-[13px] font-semibold text-fg" :title="item.name">
                {{ item.name }}
              </h3>
              <span class="tnum shrink-0 font-mono text-[11px] text-subtle">
                {{ lineCount(item.content) }} 行
              </span>
            </div>
            <p class="mt-1 line-clamp-2 text-xs leading-relaxed text-muted" :title="item.description">
              {{ item.description }}
            </p>
          </div>

          <!-- 推荐 AGY 管线模式条 -->
          <div class="flex flex-wrap items-center justify-between gap-1.5 rounded border border-line bg-raised/55 px-2.5 py-1.5 text-2xs">
            <span class="text-subtle">推荐 AGY 管线：</span>
            <Badge :tone="getAgyModeMeta(item.recommended_agy_mode).tone" mono>
              {{ item.recommended_agy_mode || 'default_intercept_continue' }}
            </Badge>
          </div>

          <!-- 核心特性亮点 -->
          <ul v-if="(item.highlights?.length ?? 0) > 0" class="space-y-1">
            <li
              v-for="(hl, hIdx) in item.highlights!.slice(0, 3)"
              :key="hIdx"
              class="flex items-start gap-1.5 text-2xs leading-4 text-muted"
            >
              <Icon name="check" :size="12" class="mt-0.5 shrink-0 text-success" />
              <span class="line-clamp-1" :title="hl">{{ hl }}</span>
            </li>
          </ul>

          <!-- 源码片段速览 -->
          <pre
            class="line-clamp-3 overflow-hidden whitespace-pre-wrap break-words rounded border border-line bg-canvas px-2.5 py-1.5 font-mono text-[11px] leading-relaxed text-muted cursor-pointer hover:border-line-strong"
            title="点击审阅完整系统提示词"
            @click="openPreview(item)"
          >{{ item.content }}</pre>

          <!-- 标签组 -->
          <div v-if="(item.tags?.length ?? 0) > 0" class="flex flex-wrap gap-1">
            <span
              v-for="tag in item.tags!.slice(0, 4)"
              :key="tag"
              class="rounded border border-line bg-raised/60 px-1.5 py-px font-mono text-[10px] text-subtle"
            >
              {{ tag }}
            </span>
          </div>
        </div>

        <!-- 底部动作栏 -->
        <div class="mt-3 flex flex-wrap items-center justify-between gap-1.5 border-t border-line pt-2.5">
          <Button variant="ghost" size="xs" @click="openPreview(item)">
            <template #leading><Icon name="eye" :size="12" /></template>
            全文预览
          </Button>

          <div class="flex items-center gap-1.5">
            <Button variant="secondary" size="xs" @click="handleCopyPrompt(item)">
              <template #leading>
                <Icon
                  :name="copiedId === item.id ? 'check' : 'copy'"
                  :size="12"
                  :class="copiedId === item.id ? 'text-success' : ''"
                />
              </template>
              {{ copiedId === item.id ? '已复制' : '复制提示词' }}
            </Button>

            <Button variant="primary" size="xs" @click="openApplyModal(item)">
              <template #leading><Icon name="download" :size="12" /></template>
              一键导入模板库
            </Button>
          </div>
        </div>
      </article>
    </div>

    <!-- 全文结构化 / Markdown 源码审阅弹窗 -->
    <Modal
      :open="previewItem !== null"
      size="xl"
      :title="previewItem ? `${previewItem.name}` : '提示词全文预览'"
      :subtitle="previewItem?.description ?? ''"
      @update:open="(val) => !val && (previewItem = null)"
    >
      <div v-if="previewItem" class="space-y-3.5">
        <div class="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-raised/60 px-3 py-2">
          <div class="flex flex-wrap items-center gap-1.5">
            <Badge :tone="getCatalogPlatformMeta(previewItem.platform).tone" dot>
              {{ previewItem.platform_label || getCatalogPlatformMeta(previewItem.platform).label }}
            </Badge>
            <Badge :tone="scenarioTone(previewItem.scenario_tag || '')">
              {{ previewItem.scenario_tag || '原生身份' }}
            </Badge>
            <Badge :tone="getAgyModeMeta(previewItem.recommended_agy_mode).tone" mono>
              AGY: {{ previewItem.recommended_agy_mode || 'default_intercept_continue' }}
            </Badge>
            <span class="tnum font-mono text-2xs text-subtle">
              {{ previewItem.content.length }} chars · {{ lineCount(previewItem.content) }} lines
            </span>
          </div>

          <SegmentedTabs v-model="previewTab" :items="previewTabItems" />
        </div>

        <!-- 亮点提要 -->
        <div
          v-if="(previewItem.highlights?.length ?? 0) > 0"
          class="grid gap-2 sm:grid-cols-3"
        >
          <div
            v-for="(hl, idx) in previewItem.highlights"
            :key="idx"
            class="flex items-start gap-2 rounded-md border border-line bg-raised/35 px-2.5 py-2 text-2xs text-muted"
          >
            <Icon name="checkCircle" :size="13" class="mt-0.5 shrink-0 text-success" />
            <span class="leading-relaxed">{{ hl }}</span>
          </div>
        </div>

        <!-- 结构化阅读模式 -->
        <div
          v-if="previewTab === 'structured'"
          class="scroll-thin max-h-[26rem] space-y-2.5 overflow-y-auto rounded-md border border-line bg-canvas p-3.5"
        >
          <template v-for="(blk, bIdx) in parsedPreviewBlocks" :key="bIdx">
            <div
              v-if="blk.kind === 'xml'"
              class="rounded-md border border-accent/25 bg-surface p-3 space-y-1.5"
            >
              <div class="flex items-center justify-between">
                <Badge tone="accent" mono>{{ blk.title }}</Badge>
                <span class="text-2xs text-subtle">XML 指令段</span>
              </div>
              <pre class="whitespace-pre-wrap break-words font-mono text-xs leading-relaxed text-fg">{{ blk.lines.join('\n') }}</pre>
            </div>

            <h4
              v-else-if="blk.kind === 'heading'"
              class="border-b border-line pb-1 pt-1 text-xs font-semibold text-fg"
            >
              {{ blk.title }}
            </h4>

            <ul v-else-if="blk.kind === 'list'" class="space-y-1.5 pl-1">
              <li
                v-for="(li, lIdx) in blk.lines"
                :key="lIdx"
                class="flex items-start gap-2 text-xs leading-relaxed text-fg"
              >
                <span class="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-accent" />
                <span>{{ li }}</span>
              </li>
            </ul>

            <p
              v-else
              class="whitespace-pre-wrap text-xs leading-relaxed text-muted"
            >
              {{ blk.lines.join('\n') }}
            </p>
          </template>
        </div>

        <!-- 原始提示词源码模式 -->
        <CodeBlock
          v-else
          :title="`${previewItem.id} · Raw System Prompt`"
          language="prompt"
          max-height="26rem"
          :code="previewItem.content"
        />
      </div>

      <template #footer>
        <Button variant="ghost" @click="previewItem = null">关闭</Button>
        <Button
          v-if="previewItem"
          variant="secondary"
          @click="handleCopyPrompt(previewItem)"
        >
          <template #leading><Icon name="copy" :size="13" /></template>
          复制提示词
        </Button>
        <Button
          v-if="previewItem"
          variant="primary"
          @click="openApplyModal(previewItem)"
        >
          <template #leading><Icon name="download" :size="13" /></template>
          一键导入到模板库
        </Button>
      </template>
    </Modal>

    <!-- 一键导入到模板库并可选绑定分组弹窗 -->
    <Modal
      :open="applyModalOpen"
      size="md"
      title="一键导入到系统提示词模板库"
      :subtitle="applyTarget ? `来源图鉴：${applyTarget.platform_label || applyTarget.platform} · ${applyTarget.name}` : ''"
      @update:open="applyModalOpen = $event"
    >
      <form v-if="applyTarget" class="space-y-3.5" @submit.prevent="submitApply">
        <p
          v-if="applyError"
          class="flex items-center gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="13" />
          <span>{{ applyError }}</span>
        </p>

        <div class="grid gap-3 sm:grid-cols-3">
          <div class="sm:col-span-2">
            <Field label="导入后模板标识名" required hint="同名模板将幂等更新为最新内容">
              <Input v-model="applyForm.name" mono placeholder="例如：claude-code-cli-concise" />
            </Field>
          </div>
          <Field label="排序优先级" hint="数值越小越靠前（支持 0）">
            <Input v-model="applyForm.priority" type="number" step="1" />
          </Field>
        </div>

        <Field label="应用范围与分组绑定（可选）">
          <div class="space-y-2">
            <button
              type="button"
              class="flex w-full items-start justify-between gap-2 rounded-md border px-3 py-2 text-left transition"
              :class="
                applyForm.bindTarget === 'library_only'
                  ? 'border-accent bg-accent/10'
                  : 'border-line bg-raised/40 hover:border-line-strong'
              "
              @click="applyForm.bindTarget = 'library_only'"
            >
              <div>
                <div class="text-xs font-medium text-fg">仅导入到提示词模板库</div>
                <div class="mt-0.5 text-2xs text-subtle">
                  保存为可复用模板，稍后可在任意路由分组下拉框中按需选用
                </div>
              </div>
              <Badge v-if="applyForm.bindTarget === 'library_only'" tone="accent">当前选择</Badge>
            </button>

            <button
              type="button"
              :disabled="groups.length === 0"
              class="flex w-full items-start justify-between gap-2 rounded-md border px-3 py-2 text-left transition disabled:cursor-not-allowed disabled:opacity-50"
              :class="
                applyForm.bindTarget === 'single_group'
                  ? 'border-accent bg-accent/10'
                  : 'border-line bg-raised/40 hover:border-line-strong'
              "
              @click="groups.length > 0 && (applyForm.bindTarget = 'single_group')"
            >
              <div>
                <div class="text-xs font-medium text-fg">导入并立即绑定到指定路由分组</div>
                <div class="mt-0.5 text-2xs text-subtle">
                  自动将该模板关联到所选分组，并同步设置启用状态与前置注入模式
                </div>
              </div>
              <Badge v-if="applyForm.bindTarget === 'single_group'" tone="accent">当前选择</Badge>
            </button>

            <button
              v-if="canBindPlatformGroups"
              type="button"
              class="flex w-full items-start justify-between gap-2 rounded-md border px-3 py-2 text-left transition"
              :class="
                applyForm.bindTarget === 'platform_groups'
                  ? 'border-accent bg-accent/10'
                  : 'border-line bg-raised/40 hover:border-line-strong'
              "
              @click="applyForm.bindTarget = 'platform_groups'"
            >
              <div>
                <div class="text-xs font-medium text-fg">
                  导入并批量应用到全部 {{ normalizeCatalogPlatform(applyTarget.platform) }} 平台分组
                </div>
                <div class="mt-0.5 text-2xs text-subtle">
                  一键为同平台所有活跃分组绑定该提示词模板并热更新引擎
                </div>
              </div>
              <Badge v-if="applyForm.bindTarget === 'platform_groups'" tone="accent">当前选择</Badge>
            </button>
          </div>
        </Field>

        <div
          v-if="applyForm.bindTarget !== 'library_only'"
          class="space-y-3 rounded-md border border-line bg-raised/50 p-3"
        >
          <Field v-if="applyForm.bindTarget === 'single_group'" label="选择目标分组" required>
            <Select v-model="applyForm.groupId" size="sm" :options="groupSelectOptions" />
          </Field>

          <Field label="分组注入模式 (inject_mode)">
            <Select
              v-model="applyForm.injectMode"
              size="sm"
              :disabled="!applyForm.enabled"
              :options="injectModeOptions"
            />
          </Field>

          <div class="pt-0.5">
            <Switch
              v-model="applyForm.enabled"
              size="sm"
              :label="applyForm.enabled ? '导入后立即启用分组注入' : '仅预绑定模板，暂不启用分组注入 (mode=off)'"
              description="开启后即时在网关路由转发层热生效，无需重启服务"
            />
          </div>
        </div>
      </form>

      <template #footer>
        <Button variant="ghost" :disabled="applying" @click="applyModalOpen = false">取消</Button>
        <Button variant="primary" :loading="applying" @click="submitApply">
          <template #leading><Icon name="check" :size="13" /></template>
          确认导入{{ applyForm.bindTarget !== 'library_only' ? '并应用' : '' }}
        </Button>
      </template>
    </Modal>
  </Card>
</template>
