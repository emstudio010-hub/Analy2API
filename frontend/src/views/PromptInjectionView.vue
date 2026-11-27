<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { toMessage } from '@/api/client'
import {
  createPromptTemplate,
  deletePromptTemplate,
  fetchPromptCatalog,
  fetchPromptConfig,
  listPromptTemplates,
  previewPromptInjection,
  resetPromptTemplates,
  updateGlobalPrompt,
  updateGroupPrompt,
  updatePromptTemplate,
} from '@/api/endpoints'
import type {
  ApplyPromptCatalogResponse,
  FilterPreviewProtocol,
  Group,
  PromptCatalogItem,
  PromptInjectMode,
  PromptLayerApplied,
  PromptPreviewResponse,
  PromptTemplate,
} from '@/api/types'
import AgyInterceptGuide from '@/components/prompts/AgyInterceptGuide.vue'
import PlatformPromptGallery from '@/components/prompts/PlatformPromptGallery.vue'
import {
  FALLBACK_PROMPT_CATALOG,
  normalizeCatalogItem,
} from '@/components/prompts/catalogData'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import Modal from '@/components/ui/Modal.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import Skeleton from '@/components/ui/Skeleton.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'
import { ALL_PLATFORMS, normalizePlatform } from '@/utils/platforms'

const route = useRoute()
const toast = useToastStore()

// ---- 顶层三大工作台标签页 ----
type WorkbenchTab = 'active' | 'catalog' | 'agy_guide'
const workbenchTab = ref<string>(
  route.query.tab === 'catalog' || route.query.tab === 'agy_guide'
    ? String(route.query.tab)
    : 'active',
)
const catalogPlatformFocus = ref<string>('all')

const workbenchTabItems = computed(() => [
  {
    value: 'active' satisfies WorkbenchTab,
    label: '已配置注入模板',
    icon: 'layers' as const,
    badge: templates.value.length,
  },
  {
    value: 'catalog' satisfies WorkbenchTab,
    label: '各平台提示词图鉴库',
    icon: 'sparkles' as const,
    badge: catalogItems.value.length,
  },
  {
    value: 'agy_guide' satisfies WorkbenchTab,
    label: 'AGY 拦截接续说明',
    icon: 'shield' as const,
  },
])

// 已配置注入模板子视图切换
const activeSubSection = ref<string>('all')
const activeSubSectionItems = computed(() => [
  { value: 'all', label: '全量视图' },
  { value: 'global', label: '全局底线指令' },
  { value: 'groups', label: '分组注入矩阵', badge: groups.value.length },
  { value: 'templates_sandbox', label: '模板库与合成沙盒', badge: templates.value.length },
])

// ---- 动态模板变量 ----
const DYNAMIC_VARS = [
  { token: '{{date}}', desc: '当前 UTC 日期 (YYYY-MM-DD)' },
  { token: '{{model}}', desc: '下游请求模型名' },
  { token: '{{platform}}', desc: '命中的上游平台' },
  { token: '{{group}}', desc: '所属路由分组名' },
] as const

const INJECT_MODES: Array<{
  value: PromptInjectMode
  label: string
  short: string
  tone: 'accent' | 'info' | 'warning' | 'neutral'
  hint: string
}> = [
  {
    value: 'append_global',
    label: '叠加在全局后 (append_global)',
    short: '叠加全局',
    tone: 'accent',
    hint: '合成顺序：[全局底线提示词] + [分组完整提示词] + [风控清洗后客户端 System]',
  },
  {
    value: 'override_global',
    label: '覆盖全局底线 (override_global)',
    short: '覆盖全局',
    tone: 'info',
    hint: '忽略全局底线，仅拼接：[分组完整提示词] + [风控清洗后客户端 System]',
  },
  {
    value: 'replace_all',
    label: '强制接管并剥离客户端 System (replace_all)',
    short: '强制接管',
    tone: 'warning',
    hint: '彻底剥离客户端原带 system/developer/instructions，仅使用分组/全局提示词',
  },
  {
    value: 'off',
    label: '关闭前置注入 (off)',
    short: '关闭注入',
    tone: 'neutral',
    hint: '不追加额外系统提示词，仅保留默认内容风控脱敏清洗',
  },
]

const modeSelectOptions = INJECT_MODES.map((m) => ({ label: m.label, value: m.value }))

function modeMeta(mode?: string | null) {
  return INJECT_MODES.find((m) => m.value === mode) ?? INJECT_MODES[0]!
}

// ---- 核心配置与图鉴库状态 ----
const loading = ref(true)
const catalogLoading = ref(false)
const error = ref('')

const globalEnabled = ref(true)
const globalPrompt = ref('')
const savedGlobalSnapshot = ref({ enabled: true, prompt: '' })
const savingGlobal = ref(false)

const groups = ref<Group[]>([])
const templates = ref<PromptTemplate[]>([])
const catalogItems = ref<PromptCatalogItem[]>(
  FALLBACK_PROMPT_CATALOG.map((it, idx) => normalizeCatalogItem(it, idx)),
)

interface GroupDraft {
  mode: PromptInjectMode
  templateId: string
  systemPrompt: string
  dirty: boolean
  saving: boolean
  expanded: boolean
}

const groupDrafts = reactive<Record<number, GroupDraft>>({})
const groupPlatformFilter = ref('')
const groupKeyword = ref('')

const globalDirty = computed(
  () =>
    globalEnabled.value !== savedGlobalSnapshot.value.enabled ||
    globalPrompt.value !== savedGlobalSnapshot.value.prompt,
)

function syncGroupDraft(group: Group, expandTargetId?: number): void {
  const prev = groupDrafts[group.id]
  const rawTplId =
    group.prompt_template_id !== undefined && group.prompt_template_id !== null
      ? String(group.prompt_template_id)
      : ''
  const validTplId =
    rawTplId && templates.value.some((t) => String(t.id) === rawTplId) ? rawTplId : ''

  groupDrafts[group.id] = {
    mode: (group.prompt_inject_mode as PromptInjectMode) || 'append_global',
    templateId: validTplId,
    systemPrompt: group.system_prompt ?? '',
    dirty: false,
    saving: false,
    expanded:
      expandTargetId === group.id
        ? true
        : (prev?.expanded ?? Boolean(group.system_prompt?.trim() || validTplId)),
  }
}

async function loadCatalog(): Promise<void> {
  catalogLoading.value = true
  try {
    const res = await fetchPromptCatalog()
    if (Array.isArray(res.items) && res.items.length > 0) {
      catalogItems.value = res.items.map((item, idx) => normalizeCatalogItem(item, idx))
    }
  } catch {
    // 后端路由若尚未热升级则静默保留本地 11 平台完整图鉴库
  } finally {
    catalogLoading.value = false
  }
}

async function loadAll(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [config] = await Promise.all([fetchPromptConfig(), loadCatalog()])
    globalEnabled.value = Boolean(config.global_enabled)
    globalPrompt.value = config.global_prompt ?? ''
    savedGlobalSnapshot.value = {
      enabled: globalEnabled.value,
      prompt: globalPrompt.value,
    }
    groups.value = config.groups ?? []
    templates.value = config.templates ?? []

    const focusId = route.query.group_id ? Number(route.query.group_id) : undefined
    for (const g of groups.value) {
      syncGroupDraft(g, focusId)
    }

    if (focusId && groups.value.some((g) => g.id === focusId)) {
      previewForm.groupId = String(focusId)
    } else if (!previewForm.groupId && groups.value[0]) {
      previewForm.groupId = String(groups.value[0].id)
    }
  } catch (err) {
    error.value = toMessage(err, '加载前置提示词配置失败')
  } finally {
    loading.value = false
  }
}

async function refreshTemplatesOnly(): Promise<void> {
  try {
    const res = await listPromptTemplates()
    templates.value = res.items ?? []
  } catch (err) {
    toast.error('刷新模板列表失败', toMessage(err))
  }
}

async function handleCatalogApplied(res: ApplyPromptCatalogResponse): Promise<void> {
  await loadAll()
  if (res.group && groupDrafts[res.group.id]) {
    groupDrafts[res.group.id]!.expanded = true
    previewForm.groupId = String(res.group.id)
    void runSandboxPreview()
  }
}

function jumpToAgyCatalog(): void {
  catalogPlatformFocus.value = 'antigravity'
  workbenchTab.value = 'catalog'
}

// ---- 区块 A：全局底线提示词 ----
function insertVarIntoGlobal(token: string): void {
  const sep =
    globalPrompt.value &&
    !globalPrompt.value.endsWith(' ') &&
    !globalPrompt.value.endsWith('\n')
      ? ' '
      : ''
  globalPrompt.value = `${globalPrompt.value}${sep}${token}`
}

function revertGlobalDraft(): void {
  globalEnabled.value = savedGlobalSnapshot.value.enabled
  globalPrompt.value = savedGlobalSnapshot.value.prompt
}

async function saveGlobal(): Promise<void> {
  savingGlobal.value = true
  try {
    await updateGlobalPrompt({
      enabled: globalEnabled.value,
      prompt: globalPrompt.value,
    })
    savedGlobalSnapshot.value = {
      enabled: globalEnabled.value,
      prompt: globalPrompt.value,
    }
    toast.success('全局底线提示词已热更新生效')
    void runSandboxPreview()
  } catch (err) {
    toast.error('保存全局提示词失败', toMessage(err))
  } finally {
    savingGlobal.value = false
  }
}

// ---- 区块 B：分组矩阵编辑与保存 ----
const sortedTemplates = computed(() => {
  const copy = [...templates.value]
  if (tplSortBy.value === 'name') {
    return copy.sort((a, b) => a.name.localeCompare(b.name))
  }
  if (tplSortBy.value === 'updated') {
    return copy.sort((a, b) => (b.updated_at || '').localeCompare(a.updated_at || ''))
  }
  return copy.sort((a, b) => {
    const pa = a.priority ?? 100
    const pb = b.priority ?? 100
    if (pa !== pb) return pa - pb
    return a.id - b.id
  })
})

const templateSelectOptions = computed(() =>
  sortedTemplates.value.map((tpl) => ({
    label: `[P${tpl.priority}] ${tpl.name} (${categoryLabel(tpl.category)}${tpl.is_builtin ? ' · 内置' : ''})`,
    value: String(tpl.id),
  })),
)

const filteredGroups = computed(() => {
  const q = groupKeyword.value.trim().toLowerCase()
  return groups.value.filter((g) => {
    if (groupPlatformFilter.value && normalizePlatform(g.platform) !== groupPlatformFilter.value) {
      return false
    }
    if (!q) return true
    const draft = groupDrafts[g.id]
    return (
      g.name.toLowerCase().includes(q) ||
      (g.description ?? '').toLowerCase().includes(q) ||
      (draft?.systemPrompt ?? '').toLowerCase().includes(q)
    )
  })
})

const activeInjectedGroupsCount = computed(
  () =>
    groups.value.filter((g) => {
      const mode = groupDrafts[g.id]?.mode ?? g.prompt_inject_mode ?? 'append_global'
      return mode !== 'off'
    }).length,
)

function markGroupDirty(groupId: number): void {
  const d = groupDrafts[groupId]
  if (d) d.dirty = true
}

function resetSingleGroupDraft(group: Group): void {
  syncGroupDraft(group, groupDrafts[group.id]?.expanded ? group.id : undefined)
}

function applyTemplateContentToGroup(groupId: number): void {
  const d = groupDrafts[groupId]
  if (!d || !d.templateId) {
    toast.warning('请先在下拉框中选择一个模板')
    return
  }
  const tpl = templates.value.find((t) => String(t.id) === d.templateId)
  if (!tpl) return
  d.systemPrompt = tpl.content
  d.expanded = true
  d.dirty = true
  toast.info(`已填入模板「${tpl.name}」正文`, '可继续按分组需求微调后点击保存策略')
}

function insertVarIntoGroup(groupId: number, token: string): void {
  const d = groupDrafts[groupId]
  if (!d) return
  const sep =
    d.systemPrompt && !d.systemPrompt.endsWith(' ') && !d.systemPrompt.endsWith('\n') ? ' ' : ''
  d.systemPrompt = `${d.systemPrompt}${sep}${token}`
  d.expanded = true
  d.dirty = true
}

async function saveGroupStrategy(group: Group): Promise<void> {
  const d = groupDrafts[group.id]
  if (!d || d.saving) return
  d.saving = true
  try {
    const updated = await updateGroupPrompt(group.id, {
      prompt_inject_mode: d.mode,
      system_prompt: d.systemPrompt,
      prompt_template_id: d.templateId ? Number(d.templateId) : null,
    })
    const idx = groups.value.findIndex((item) => item.id === group.id)
    if (idx >= 0) {
      groups.value[idx] = { ...groups.value[idx]!, ...updated }
    }
    d.dirty = false
    toast.success(`分组「${group.name}」提示词策略已生效`)
    if (previewForm.groupId === String(group.id)) {
      void runSandboxPreview()
    }
  } catch (err) {
    toast.error('保存分组提示词失败', toMessage(err))
  } finally {
    d.saving = false
  }
}

// ---- 区块 C-1：提示词模板库 CRUD（修复分类选择器缺失、priority=0 丢失、删除残留引用与表单重置） ----
const tplModalOpen = ref(false)
const editingTpl = ref<PromptTemplate | null>(null)
const savingTpl = ref(false)
const tplFormError = ref('')
const deleteTplTarget = ref<PromptTemplate | null>(null)
const deletingTpl = ref(false)
const resetTplOpen = ref(false)
const resettingTpl = ref(false)

const tplSearch = ref('')
const tplCategoryFilter = ref('')
const tplSortBy = ref<'priority' | 'updated' | 'name'>('priority')

const BASE_TPL_CATEGORIES: ReadonlyArray<{ label: string; value: string }> = [
  { label: '身份与防泄露 (identity)', value: 'identity' },
  { label: '全栈架构师 (architecture)', value: 'architecture' },
  { label: '精炼终端 CLI (cli)', value: 'cli' },
  { label: '代码审查审计 (review)', value: 'review' },
  { label: 'AntiGravity 底座 (antigravity)', value: 'antigravity' },
  { label: 'AGY 拦截接续桥 (intercept_bridge)', value: 'intercept_bridge' },
  { label: '工具调用协议 (tool_protocol)', value: 'tool_protocol' },
  { label: '智能体编码规范 (coding)', value: 'coding' },
  { label: '通用核心指令 (general)', value: 'general' },
]

function categoryLabel(cat?: string | null): string {
  if (!cat) return 'general'
  const hit = BASE_TPL_CATEGORIES.find((c) => c.value === cat)
  return hit ? hit.value : cat
}

const tplForm = reactive({
  name: '',
  category: 'general',
  priority: '100',
  description: '',
  content: '',
})

const effectiveTplCategoryOptions = computed(() => {
  const cur = tplForm.category.trim()
  if (!cur || BASE_TPL_CATEGORIES.some((opt) => opt.value === cur)) {
    return [...BASE_TPL_CATEGORIES]
  }
  return [...BASE_TPL_CATEGORIES, { label: `自定义分类 (${cur})`, value: cur }]
})

const tplFilterCategoryOptions = computed(() => {
  const seen = new Set(BASE_TPL_CATEGORIES.map((c) => c.value))
  const extra: Array<{ label: string; value: string }> = []
  for (const t of templates.value) {
    if (t.category && !seen.has(t.category)) {
      seen.add(t.category)
      extra.push({ label: t.category, value: t.category })
    }
  }
  return [...BASE_TPL_CATEGORIES, ...extra]
})

const filteredTemplates = computed(() => {
  const q = tplSearch.value.trim().toLowerCase()
  return sortedTemplates.value.filter((tpl) => {
    if (tplCategoryFilter.value && tpl.category !== tplCategoryFilter.value) return false
    if (!q) return true
    return (
      tpl.name.toLowerCase().includes(q) ||
      tpl.category.toLowerCase().includes(q) ||
      (tpl.description ?? '').toLowerCase().includes(q) ||
      tpl.content.toLowerCase().includes(q)
    )
  })
})

function resetTplFormState(): void {
  editingTpl.value = null
  tplFormError.value = ''
  tplForm.name = ''
  tplForm.category = 'general'
  tplForm.priority = '100'
  tplForm.description = ''
  tplForm.content = ''
}

watch(tplModalOpen, (isOpen) => {
  if (!isOpen && !savingTpl.value) {
    tplFormError.value = ''
  }
})

function openCreateTpl(): void {
  resetTplFormState()
  tplModalOpen.value = true
}

function openEditTpl(tpl: PromptTemplate): void {
  editingTpl.value = tpl
  tplFormError.value = ''
  tplForm.name = tpl.name
  tplForm.category = tpl.category || 'general'
  tplForm.priority = String(tpl.priority ?? 100)
  tplForm.description = tpl.description ?? ''
  tplForm.content = tpl.content ?? ''
  tplModalOpen.value = true
}

function insertVarIntoTplForm(token: string): void {
  const sep =
    tplForm.content && !tplForm.content.endsWith(' ') && !tplForm.content.endsWith('\n')
      ? ' '
      : ''
  tplForm.content = `${tplForm.content}${sep}${token}`
}

async function copyTplContent(tpl: PromptTemplate): Promise<void> {
  const ok = await copyText(tpl.content)
  if (ok) toast.success('模板内容已复制', tpl.name)
}

async function submitTpl(): Promise<void> {
  if (savingTpl.value) return
  if (!tplForm.name.trim()) {
    tplFormError.value = '请输入模板名称'
    return
  }
  if (!tplForm.content.trim()) {
    tplFormError.value = '请输入模板完整提示词正文'
    return
  }

  const rawPrio = String(tplForm.priority).trim()
  const parsedPrio = rawPrio !== '' && Number.isFinite(Number(rawPrio)) ? Math.trunc(Number(rawPrio)) : 100

  savingTpl.value = true
  tplFormError.value = ''
  try {
    const payload = {
      name: tplForm.name.trim(),
      category: tplForm.category.trim() || 'general',
      priority: parsedPrio,
      description: tplForm.description.trim(),
      content: tplForm.content,
    }
    if (editingTpl.value) {
      await updatePromptTemplate(editingTpl.value.id, payload)
      toast.success('提示词模板已更新', payload.name)
    } else {
      await createPromptTemplate(payload)
      toast.success('提示词模板已创建', payload.name)
    }
    tplModalOpen.value = false
    resetTplFormState()
    await refreshTemplatesOnly()
  } catch (err) {
    tplFormError.value = toMessage(err, '保存模板失败')
  } finally {
    savingTpl.value = false
  }
}

async function confirmDeleteTpl(): Promise<void> {
  const target = deleteTplTarget.value
  if (!target || deletingTpl.value) return
  deletingTpl.value = true
  try {
    const deletedIdStr = String(target.id)
    await deletePromptTemplate(target.id)
    // 清理分组草稿中指向已删除模板的悬空引用，防止后续保存报 404
    for (const gid of Object.keys(groupDrafts)) {
      const draft = groupDrafts[Number(gid)]
      if (draft && draft.templateId === deletedIdStr) {
        draft.templateId = ''
      }
    }
    toast.success('模板已删除', target.name)
    deleteTplTarget.value = null
    await refreshTemplatesOnly()
  } catch (err) {
    toast.error('删除模板失败', toMessage(err))
  } finally {
    deletingTpl.value = false
  }
}

async function confirmResetTemplates(): Promise<void> {
  if (resettingTpl.value) return
  resettingTpl.value = true
  try {
    const res = await resetPromptTemplates()
    templates.value = res.items ?? []
    toast.success(res.message || '已恢复内置默认提示词模板库')
    resetTplOpen.value = false
    await loadAll()
  } catch (err) {
    toast.error('恢复默认模板失败', toMessage(err))
  } finally {
    resettingTpl.value = false
  }
}

// ---- 区块 C-2：在线合成演练沙盒 ----
const previewForm = reactive({
  groupId: '',
  model: 'claude-sonnet-4-6',
  protocol: 'anthropic' as FilterPreviewProtocol,
  clientSystem:
    "x-anthropic-billing-header: cc_version=2.1.7; cc_entrypoint=cli;\nYou are Claude Code, Anthropic's official CLI for Claude.\nFollow repository conventions and answer concisely.",
})

const previewLoading = ref(false)
const previewError = ref('')
const previewResult = ref<PromptPreviewResponse | null>(null)

const protocolOptions = [
  { label: 'Anthropic (/v1/messages)', value: 'anthropic' },
  { label: 'OpenAI (/v1/chat/completions)', value: 'openai' },
  { label: 'Responses (/v1/responses)', value: 'responses' },
]

const sandboxGroupOptions = computed(() =>
  groups.value.map((g) => ({
    label: `#${g.id} ${g.name} (${g.platform})`,
    value: String(g.id),
  })),
)

const normalizedLayers = computed(() => {
  const raw = previewResult.value?.layers_applied ?? []
  return raw.map((entry: string | PromptLayerApplied, idx: number) => {
    if (typeof entry === 'string') {
      return { key: `${entry}-${idx}`, label: entry, detail: '' }
    }
    return {
      key: `${entry.layer || entry.name || idx}`,
      label: entry.layer || entry.name || entry.source || `Layer #${idx + 1}`,
      detail: entry.detail || (entry.length ? `${entry.length} chars` : ''),
    }
  })
})

function loadSandboxSample(kind: 'claude' | 'codex' | 'opencode' | 'empty'): void {
  if (kind === 'claude') {
    previewForm.protocol = 'anthropic'
    previewForm.model = 'claude-sonnet-4-6'
    previewForm.clientSystem =
      "x-anthropic-billing-header: cc_version=2.1.7; cc_entrypoint=cli;\nYou are Claude Code, Anthropic's official CLI for Claude.\nRefuse exploit or denial-of-service code generation."
  } else if (kind === 'codex') {
    previewForm.protocol = 'responses'
    previewForm.model = 'gpt-5.3-codex'
    previewForm.clientSystem =
      'You are a coding agent running in the Codex CLI. Use apply_patch for focused surgical edits and run unit tests.'
  } else if (kind === 'opencode') {
    previewForm.protocol = 'openai'
    previewForm.model = 'gpt-5.3-codex'
    previewForm.clientSystem =
      'You are OpenCode, an interactive CLI coding agent running inside the user terminal.\nAlways inspect files before editing.'
  } else {
    previewForm.protocol = 'openai'
    previewForm.model = 'gemini-2.5-pro'
    previewForm.clientSystem = ''
  }
  void runSandboxPreview()
}

function sendGroupToSandbox(group: Group): void {
  previewForm.groupId = String(group.id)
  workbenchTab.value = 'active'
  if (activeSubSection.value !== 'all' && activeSubSection.value !== 'templates_sandbox') {
    activeSubSection.value = 'templates_sandbox'
  }
  void runSandboxPreview()
}

async function runSandboxPreview(): Promise<void> {
  if (previewLoading.value) return
  previewLoading.value = true
  previewError.value = ''
  try {
    previewResult.value = await previewPromptInjection({
      group_id: previewForm.groupId ? Number(previewForm.groupId) : null,
      client_system: previewForm.clientSystem,
      model: previewForm.model.trim() || 'claude-sonnet-4-6',
      protocol: previewForm.protocol,
    })
  } catch (err) {
    previewError.value = toMessage(err, '合成预览执行失败')
  } finally {
    previewLoading.value = false
  }
}

watch(
  () => route.query.group_id,
  (val) => {
    if (!val) return
    const id = Number(val)
    workbenchTab.value = 'active'
    if (groupDrafts[id]) {
      groupDrafts[id]!.expanded = true
      previewForm.groupId = String(id)
    }
  },
)

onMounted(async () => {
  await loadAll()
  void runSandboxPreview()
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部指标磁贴 -->
    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <MetricTile
        label="全局底线提示词"
        :value="globalEnabled ? '已启用热注入' : '已关闭'"
        :hint="`${globalPrompt.length} 字符 · 全部协议入口前置生效`"
        icon="shield"
        :tone="globalEnabled ? 'success' : 'neutral'"
      />
      <MetricTile
        label="启用注入策略分组"
        :value="`${activeInjectedGroupsCount} / ${groups.length}`"
        hint="叠加全局 / 覆盖全局 / 强制接管 / 关闭"
        icon="folder"
        tone="accent"
      />
      <MetricTile
        label="已配置模板 / 平台图鉴"
        :value="`${templates.length} / ${catalogItems.length}`"
        :hint="`内置 ${templates.filter((t) => t.is_builtin).length} 套 · 11 大主流平台图鉴一键导入`"
        icon="sparkles"
        tone="info"
      />
      <MetricTile
        label="AGY 推荐运行管线"
        value="默认 → 拦截 → 接续"
        hint="保留原生指纹 · 剥离 IDE 幻觉 · 接续下游工具"
        icon="zap"
        tone="success"
      />
    </div>

    <!-- 主工作台三分区切换栏 -->
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <div class="flex flex-wrap items-center gap-2">
        <SegmentedTabs v-model="workbenchTab" size="md" :items="workbenchTabItems" />

        <SegmentedTabs
          v-if="workbenchTab === 'active'"
          v-model="activeSubSection"
          size="sm"
          :items="activeSubSectionItems"
        />
      </div>

      <div class="flex items-center gap-2">
        <Button
          v-if="workbenchTab !== 'catalog'"
          variant="subtle"
          size="sm"
          @click="workbenchTab = 'catalog'"
        >
          <template #leading><Icon name="sparkles" :size="13" /></template>
          浏览 11 平台提示词图鉴
        </Button>
        <Button variant="secondary" size="sm" :loading="loading" @click="loadAll">
          <template #leading><Icon name="refresh" :size="13" /></template>
          重新加载
        </Button>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="loadAll">重试</Button>
    </p>

    <!-- ==================== 分区 2：各平台提示词图鉴库 ==================== -->
    <PlatformPromptGallery
      v-if="workbenchTab === 'catalog'"
      :items="catalogItems"
      :loading="catalogLoading"
      :groups="groups"
      :templates="templates"
      :initial-platform="catalogPlatformFocus"
      @refresh="loadCatalog"
      @applied="handleCatalogApplied"
      @open-guide="workbenchTab = 'agy_guide'"
    />

    <!-- ==================== 分区 3：AGY 拦截接续说明 ==================== -->
    <AgyInterceptGuide
      v-else-if="workbenchTab === 'agy_guide'"
      @browse-agy-catalog="jumpToAgyCatalog"
      @open-templates="workbenchTab = 'active'"
    />

    <!-- ==================== 分区 1：已配置注入模板与分层矩阵 ==================== -->
    <template v-else>
      <!-- 区块 A：全局底线系统提示词 -->
      <Card
        v-if="activeSubSection === 'all' || activeSubSection === 'global'"
        title="全局底线系统提示词 (Global Baseline Prompt)"
        subtitle="作为网关第一层安全与身份锚点（如 Ess-Agentic 核心防泄露约束），在所有开启全局叠加的分组请求前自动前置注入"
        body-class="p-4 space-y-3.5"
      >
        <template #actions>
          <div class="flex flex-wrap items-center gap-2.5">
            <Badge v-if="globalDirty" tone="warning" dot>有未保存修改</Badge>
            <Button
              v-if="globalDirty"
              variant="ghost"
              size="xs"
              @click="revertGlobalDraft"
            >
              放弃修改
            </Button>
            <Switch
              v-model="globalEnabled"
              size="sm"
              :label="globalEnabled ? '全局注入已开启' : '全局注入已停用'"
            />
            <Button
              variant="primary"
              size="sm"
              :loading="savingGlobal"
              @click="saveGlobal"
            >
              <template #leading><Icon name="check" :size="13" /></template>
              保存热生效
            </Button>
          </div>
        </template>

        <div v-if="loading" class="space-y-2.5">
          <Skeleton class="h-6 w-72" />
          <Skeleton class="h-36 w-full" />
        </div>

        <template v-else>
          <div class="flex flex-wrap items-center gap-1.5 rounded-md border border-line bg-raised/60 px-3 py-2">
            <span class="text-2xs font-medium text-muted">点击插入动态变量：</span>
            <button
              v-for="v in DYNAMIC_VARS"
              :key="v.token"
              type="button"
              class="inline-flex items-center gap-1 rounded border border-line bg-surface px-2 py-0.5 font-mono text-2xs text-accent transition hover:border-accent/50 hover:bg-accent/10"
              :title="v.desc"
              @click="insertVarIntoGlobal(v.token)"
            >
              <span>{{ v.token }}</span>
              <span class="font-sans text-subtle">· {{ v.desc }}</span>
            </button>
          </div>

          <Textarea
            v-model="globalPrompt"
            :rows="6"
            mono
            placeholder="输入全局底线系统提示词（例如身份定义、禁止泄露系统提示词与工具 Schema、工程规范约束）…"
          />

          <div class="flex flex-wrap items-center justify-between gap-2 text-2xs text-subtle">
            <span>
              当分组策略设为 <code class="font-mono text-fg">append_global</code> 时本段位于最顶层；设为
              <code class="font-mono text-fg">override_global</code> 时分组提示词将跳过本段。
            </span>
            <span class="tnum font-mono">{{ globalPrompt.length }} chars</span>
          </div>
        </template>
      </Card>

      <!-- 区块 B：分组级完整系统提示词矩阵 -->
      <Card
        v-if="activeSubSection === 'all' || activeSubSection === 'groups'"
        title="分组级完整系统提示词矩阵 (Per-Group Prompt Matrix)"
        subtitle="按业务分组精细控制 4 种前置注入模式、绑定提示词模板或编写分组专属完整 System 指令，独立保存即时生效"
        body-class="p-4 space-y-3.5"
      >
        <template #actions>
          <div class="flex flex-wrap items-center gap-2">
            <div class="w-40">
              <Select
                v-model="groupPlatformFilter"
                size="sm"
                placeholder="全部平台"
                :options="ALL_PLATFORMS.map((p) => ({ label: p.label, value: p.value }))"
              />
            </div>
            <div class="w-48">
              <Input v-model="groupKeyword" size="sm" placeholder="筛选分组名称或提示词…" />
            </div>
          </div>
        </template>

        <div v-if="loading" class="space-y-3">
          <Skeleton v-for="i in 3" :key="i" class="h-28 w-full" />
        </div>

        <EmptyState
          v-else-if="filteredGroups.length === 0"
          icon="folder"
          title="没有匹配的路由分组"
          description="请先在「分组与倍率路由」中创建至少一个分组，或清空当前筛选条件。"
        />

        <div v-else class="space-y-3">
          <div
            v-for="group in filteredGroups"
            :key="group.id"
            class="rounded-panel border bg-raised/35 transition"
            :class="
              groupDrafts[group.id]?.dirty
                ? 'border-accent/50 bg-accent/[0.03]'
                : 'border-line hover:border-line-strong'
            "
          >
            <div class="flex flex-wrap items-center justify-between gap-3 px-3.5 py-2.5">
              <div class="flex min-w-0 flex-wrap items-center gap-2">
                <PlatformBadge :platform="group.platform" short />
                <span class="text-[13px] font-semibold text-fg">{{ group.name }}</span>
                <span class="tnum text-2xs text-subtle">
                  #{{ group.id }} · 倍率 {{ group.rate_multiplier }}x · {{ group.account_count }} 账号 / {{ group.api_key_count }} Key
                </span>
                <Badge :tone="modeMeta(groupDrafts[group.id]?.mode).tone" dot>
                  {{ modeMeta(groupDrafts[group.id]?.mode).short }}
                </Badge>
                <Badge v-if="groupDrafts[group.id]?.dirty" tone="warning">未保存</Badge>
              </div>

              <div class="flex flex-wrap items-center gap-1.5">
                <Button
                  v-if="groupDrafts[group.id]?.dirty"
                  variant="ghost"
                  size="xs"
                  @click="resetSingleGroupDraft(group)"
                >
                  重置
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  @click="sendGroupToSandbox(group)"
                >
                  <template #leading><Icon name="zap" :size="12" /></template>
                  沙盒演练
                </Button>
                <Button
                  variant="secondary"
                  size="xs"
                  @click="
                    groupDrafts[group.id] &&
                    (groupDrafts[group.id]!.expanded = !groupDrafts[group.id]!.expanded)
                  "
                >
                  <template #leading>
                    <Icon
                      :name="groupDrafts[group.id]?.expanded ? 'chevronDown' : 'chevronRight'"
                      :size="12"
                    />
                  </template>
                  {{ groupDrafts[group.id]?.expanded ? '收起编辑器' : '展开专属指令' }}
                </Button>
                <Button
                  variant="primary"
                  size="xs"
                  :loading="groupDrafts[group.id]?.saving"
                  @click="saveGroupStrategy(group)"
                >
                  <template #leading><Icon name="check" :size="12" /></template>
                  保存策略
                </Button>
              </div>
            </div>

            <div
              v-if="groupDrafts[group.id]"
              class="grid gap-3 border-t border-line/70 px-3.5 py-2.5 md:grid-cols-12"
            >
              <div class="md:col-span-5">
                <Field label="注入策略模式" :hint="modeMeta(groupDrafts[group.id]!.mode).hint">
                  <Select
                    :model-value="groupDrafts[group.id]!.mode"
                    size="sm"
                    :options="modeSelectOptions"
                    @update:model-value="
                      (val) => {
                        groupDrafts[group.id]!.mode = val as PromptInjectMode
                        markGroupDirty(group.id)
                      }
                    "
                  />
                </Field>
              </div>

              <div class="md:col-span-5">
                <Field
                  label="关联提示词模板"
                  hint="专属指令留空时自动采用所选模板；也可将模板正文拷贝到下方按需微调"
                >
                  <Select
                    :model-value="groupDrafts[group.id]!.templateId"
                    size="sm"
                    placeholder="不绑定模板（仅用专属指令或全局底线）"
                    :options="templateSelectOptions"
                    @update:model-value="
                      (val) => {
                        groupDrafts[group.id]!.templateId = val
                        markGroupDirty(group.id)
                      }
                    "
                  />
                </Field>
              </div>

              <div class="flex items-end pb-4 md:col-span-2">
                <Button
                  variant="secondary"
                  size="sm"
                  block
                  :disabled="!groupDrafts[group.id]!.templateId"
                  title="将所选模板内容拷贝到本分组专属编辑器中"
                  @click="applyTemplateContentToGroup(group.id)"
                >
                  <template #leading><Icon name="copy" :size="12" /></template>
                  填入正文
                </Button>
              </div>
            </div>

            <div
              v-if="groupDrafts[group.id]?.expanded"
              class="space-y-2 border-t border-line/70 bg-surface/70 px-3.5 py-3"
            >
              <div class="flex flex-wrap items-center justify-between gap-2">
                <span class="text-xs font-medium text-muted">
                  分组专属完整 System 提示词（优先级高于关联模板默认内容）
                </span>
                <div class="flex flex-wrap items-center gap-1">
                  <button
                    v-for="v in DYNAMIC_VARS"
                    :key="v.token"
                    type="button"
                    class="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-accent transition hover:border-accent/40"
                    :title="`插入 ${v.desc}`"
                    @click="insertVarIntoGroup(group.id, v.token)"
                  >
                    + {{ v.token }}
                  </button>
                </div>
              </div>

              <Textarea
                :model-value="groupDrafts[group.id]!.systemPrompt"
                :rows="5"
                mono
                :disabled="groupDrafts[group.id]!.mode === 'off'"
                placeholder="编写该分组专属的完整系统提示词；若留空且已选择关联模板，引擎将自动使用模板内容…"
                @update:model-value="
                  (val) => {
                    groupDrafts[group.id]!.systemPrompt = val
                    markGroupDirty(group.id)
                  }
                "
              />
            </div>
          </div>
        </div>
      </Card>

      <!-- 区块 C：已入库模板管理 + 在线合成演练沙盒 -->
      <div
        v-if="activeSubSection === 'all' || activeSubSection === 'templates_sandbox'"
        class="grid gap-4 xl:grid-cols-12"
      >
        <!-- C-1 提示词模板库 CRUD -->
        <div class="xl:col-span-5">
          <Card
            title="已配置提示词模板库"
            subtitle="管理内置与从平台图鉴导入的模板，按优先级有序展示"
            body-class="p-3.5 space-y-3"
          >
            <template #actions>
              <div class="flex flex-wrap items-center gap-1.5">
                <Button variant="subtle" size="xs" @click="workbenchTab = 'catalog'">
                  <template #leading><Icon name="sparkles" :size="12" /></template>
                  从图鉴导入
                </Button>
                <Button variant="secondary" size="xs" @click="resetTplOpen = true">
                  <template #leading><Icon name="rotate" :size="12" /></template>
                  恢复内置
                </Button>
                <Button variant="primary" size="xs" @click="openCreateTpl">
                  <template #leading><Icon name="plus" :size="12" /></template>
                  新建模板
                </Button>
              </div>
            </template>

            <!-- 模板搜索与分类/排序工具栏 -->
            <div class="grid gap-2 sm:grid-cols-12">
              <div class="sm:col-span-5">
                <Input v-model="tplSearch" size="sm" placeholder="检索模板名称或内容…" />
              </div>
              <div class="sm:col-span-4">
                <Select
                  v-model="tplCategoryFilter"
                  size="sm"
                  placeholder="全部分类"
                  :options="tplFilterCategoryOptions"
                />
              </div>
              <div class="sm:col-span-3">
                <Select
                  v-model="tplSortBy"
                  size="sm"
                  :options="[
                    { label: '按优先级', value: 'priority' },
                    { label: '按最近更新', value: 'updated' },
                    { label: '按名称 A-Z', value: 'name' },
                  ]"
                />
              </div>
            </div>

            <EmptyState
              v-if="!loading && filteredTemplates.length === 0"
              icon="fileText"
              :title="templates.length === 0 ? '暂无提示词模板' : '没有匹配的提示词模板'"
              :description="
                templates.length === 0
                  ? '点击「从图鉴导入」选用 11 大主流平台提示词，或恢复默认内置模板组。'
                  : '请调整分类筛选或清空搜索关键词。'
              "
            >
              <div class="flex items-center gap-2">
                <Button
                  v-if="templates.length > 0"
                  variant="secondary"
                  size="sm"
                  @click="
                    () => {
                      tplSearch = ''
                      tplCategoryFilter = ''
                    }
                  "
                >
                  清空筛选
                </Button>
                <Button variant="primary" size="sm" @click="workbenchTab = 'catalog'">
                  打开各平台提示词图鉴库
                </Button>
              </div>
            </EmptyState>

            <div v-else class="scroll-thin max-h-[40rem] space-y-2.5 overflow-y-auto pr-0.5">
              <div
                v-for="tpl in filteredTemplates"
                :key="tpl.id"
                class="rounded-md border border-line bg-raised/45 p-3 transition hover:border-line-strong"
              >
                <div class="flex items-start justify-between gap-2">
                  <div class="min-w-0">
                    <div class="flex flex-wrap items-center gap-1.5">
                      <span class="font-mono text-xs font-semibold text-fg">{{ tpl.name }}</span>
                      <Badge :tone="tpl.is_builtin ? 'accent' : 'neutral'">
                        {{ tpl.is_builtin ? '内置' : '自定义' }}
                      </Badge>
                      <Badge tone="info" mono>{{ tpl.category }}</Badge>
                      <span class="tnum rounded border border-line bg-surface px-1.5 py-px font-mono text-[10px] text-subtle">
                        P{{ tpl.priority }}
                      </span>
                    </div>
                    <p v-if="tpl.description" class="mt-1 text-2xs leading-relaxed text-muted">
                      {{ tpl.description }}
                    </p>
                  </div>

                  <div class="flex shrink-0 items-center gap-0.5">
                    <Button
                      variant="ghost"
                      size="xs"
                      icon
                      title="复制模板内容"
                      @click="copyTplContent(tpl)"
                    >
                      <Icon name="copy" :size="13" />
                    </Button>
                    <Button variant="ghost" size="xs" icon title="编辑模板" @click="openEditTpl(tpl)">
                      <Icon name="edit" :size="13" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      icon
                      class="hover:bg-danger/10 hover:text-danger"
                      title="删除模板"
                      @click="deleteTplTarget = tpl"
                    >
                      <Icon name="trash" :size="13" />
                    </Button>
                  </div>
                </div>

                <pre
                  class="scroll-thin mt-2 max-h-24 overflow-y-auto whitespace-pre-wrap break-words rounded border border-line bg-surface px-2.5 py-1.5 font-mono text-[11px] leading-relaxed text-muted"
                >{{ tpl.content }}</pre>
              </div>
            </div>
          </Card>
        </div>

        <!-- C-2 在线合成演练沙盒 -->
        <div class="xl:col-span-7">
          <Card
            title="多层注入合成演练沙盒 (Live Composition Sandbox)"
            subtitle="模拟客户端请求进入网关，实时检验「内容风控脱敏 → 全局底线 → 分组模板/专属指令」最终合成结果"
            body-class="p-4 space-y-3.5"
          >
            <template #actions>
              <div class="flex flex-wrap items-center gap-1.5">
                <Button variant="secondary" size="xs" @click="loadSandboxSample('claude')">
                  Claude Code
                </Button>
                <Button variant="secondary" size="xs" @click="loadSandboxSample('codex')">
                  Codex CLI
                </Button>
                <Button variant="secondary" size="xs" @click="loadSandboxSample('opencode')">
                  OpenCode
                </Button>
                <Button variant="secondary" size="xs" @click="loadSandboxSample('empty')">
                  空客户端 System
                </Button>
              </div>
            </template>

            <div class="grid gap-3 sm:grid-cols-3">
              <Field label="模拟目标分组">
                <Select
                  v-model="previewForm.groupId"
                  size="sm"
                  placeholder="无分组（仅全局底线）"
                  :options="sandboxGroupOptions"
                />
              </Field>
              <Field label="模拟请求模型 ({{model}})">
                <Input v-model="previewForm.model" size="sm" mono placeholder="claude-sonnet-4-6" />
              </Field>
              <Field label="入口协议">
                <Select v-model="previewForm.protocol" size="sm" :options="protocolOptions" />
              </Field>
            </div>

            <Field label="模拟客户端原始 System / Instructions（含敏感客户端身份或计费头）">
              <Textarea
                v-model="previewForm.clientSystem"
                :rows="4"
                mono
                placeholder="粘贴客户端请求自带的原始 system / developer / instructions 文本…"
              />
            </Field>

            <div class="flex flex-wrap items-center justify-between gap-2">
              <div class="flex flex-wrap items-center gap-1.5">
                <span class="text-2xs text-subtle">生效注入层级：</span>
                <template v-if="normalizedLayers.length > 0">
                  <Badge
                    v-for="layer in normalizedLayers"
                    :key="layer.key"
                    tone="accent"
                    mono
                  >
                    {{ layer.label }}{{ layer.detail ? ` (${layer.detail})` : '' }}
                  </Badge>
                </template>
                <span v-else class="text-2xs text-subtle">尚未运行或无额外注入层</span>
              </div>

              <Button
                variant="primary"
                size="sm"
                :loading="previewLoading"
                @click="runSandboxPreview"
              >
                <template #leading><Icon name="zap" :size="13" /></template>
                执行实时合成预览
              </Button>
            </div>

            <p
              v-if="previewError"
              class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
            >
              <Icon name="alertCircle" :size="13" />
              <span>{{ previewError }}</span>
            </p>

            <div v-if="previewResult" class="space-y-3">
              <CodeBlock
                title="Step 1 · 风控清洗后的客户端提示词 (sanitized_client_system)"
                language="sanitized"
                max-height="9rem"
                :code="previewResult.sanitized_client_system"
                empty-text="（空 — 客户端未传 System 或已被 replace_all 策略剥离）"
              />

              <CodeBlock
                :title="`Step 2 · 最终发往上游的完整 System 提示词 (${previewResult.final_system.length} chars)`"
                language="final system"
                max-height="18rem"
                :code="previewResult.final_system"
                empty-text="（最终 System 为空）"
              />
            </div>
          </Card>
        </div>
      </div>
    </template>

    <!-- 新建/编辑提示词模板弹窗 -->
    <Modal
      :open="tplModalOpen"
      size="lg"
      :title="editingTpl ? `编辑提示词模板 · ${editingTpl.name}` : '新建提示词模板'"
      subtitle="支持 {{date}}、{{model}}、{{platform}}、{{group}} 动态变量占位符"
      @update:open="tplModalOpen = $event"
    >
      <form class="space-y-3.5" @submit.prevent="submitTpl">
        <p
          v-if="tplFormError"
          class="flex items-center gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="13" />
          <span>{{ tplFormError }}</span>
        </p>

        <div class="grid gap-3 sm:grid-cols-3">
          <Field label="模板标识名称" required>
            <Input v-model="tplForm.name" mono placeholder="例如：ess-team-backend" />
          </Field>
          <Field label="角色与场景分类">
            <Select v-model="tplForm.category" :options="effectiveTplCategoryOptions" />
          </Field>
          <Field label="排序优先级" hint="越小越靠前（支持 0）">
            <Input v-model="tplForm.priority" type="number" step="1" />
          </Field>
        </div>

        <Field label="用途说明">
          <Input v-model="tplForm.description" placeholder="简要描述该模板适用的分组与工程场景" />
        </Field>

        <div class="space-y-1.5">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-xs font-medium text-muted">
              完整系统提示词正文 <span class="text-danger">*</span>
            </span>
            <div class="flex flex-wrap gap-1">
              <button
                v-for="v in DYNAMIC_VARS"
                :key="v.token"
                type="button"
                class="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-accent hover:border-accent/40"
                @click="insertVarIntoTplForm(v.token)"
              >
                + {{ v.token }}
              </button>
            </div>
          </div>
          <Textarea
            v-model="tplForm.content"
            :rows="10"
            mono
            placeholder="编写完整的系统提示词正文…"
          />
        </div>
      </form>

      <template #footer>
        <Button variant="ghost" :disabled="savingTpl" @click="tplModalOpen = false">取消</Button>
        <Button variant="primary" :loading="savingTpl" @click="submitTpl">
          {{ editingTpl ? '保存修改' : '创建模板' }}
        </Button>
      </template>
    </Modal>

    <ConfirmDialog
      :open="deleteTplTarget !== null"
      tone="danger"
      title="删除提示词模板"
      :message="`确定删除模板「${deleteTplTarget?.name ?? ''}」吗？已绑定该模板的分组将回退使用分组专属提示词或全局底线。`"
      confirm-text="删除模板"
      :loading="deletingTpl"
      @update:open="deleteTplTarget = null"
      @confirm="confirmDeleteTpl"
    />

    <ConfirmDialog
      :open="resetTplOpen"
      title="恢复默认内置提示词模板"
      message="将补齐并重置内置默认模板库（不会删除你新建的自定义名称模板）。确定继续？"
      confirm-text="确认恢复"
      :loading="resettingTpl"
      @update:open="resetTplOpen = $event"
      @confirm="confirmResetTemplates"
    />
  </div>
</template>
