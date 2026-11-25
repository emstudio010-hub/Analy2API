<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import {
  createPromptTemplate,
  deletePromptTemplate,
  fetchPromptConfig,
  listPromptTemplates,
  previewPromptInjection,
  resetPromptTemplates,
  updateGlobalPrompt,
  updateGroupPrompt,
  updatePromptTemplate,
} from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type {
  FilterPreviewProtocol,
  Group,
  PromptInjectMode,
  PromptLayerApplied,
  PromptPreviewResponse,
  PromptTemplate,
} from '@/api/types'
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
import { ALL_PLATFORMS, normalizePlatform } from '@/utils/platforms'

const route = useRoute()
const toast = useToastStore()

// ---- 视图分区切换 ----
const sectionTab = ref('all')
const sectionTabItems = computed(() => [
  { value: 'all', label: '全量工作台', icon: 'layers' as const },
  { value: 'global', label: '全局底线指令', icon: 'shield' as const },
  {
    value: 'groups',
    label: '分组注入矩阵',
    icon: 'folder' as const,
    badge: groups.value.length,
  },
  {
    value: 'templates_sandbox',
    label: '模板库与合成沙盒',
    icon: 'sparkles' as const,
    badge: templates.value.length,
  },
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

// ---- 核心配置状态 ----
const loading = ref(true)
const error = ref('')

const globalEnabled = ref(true)
const globalPrompt = ref('')
const savedGlobalSnapshot = ref({ enabled: true, prompt: '' })
const savingGlobal = ref(false)

const groups = ref<Group[]>([])
const templates = ref<PromptTemplate[]>([])

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
  groupDrafts[group.id] = {
    mode: (group.prompt_inject_mode as PromptInjectMode) || 'append_global',
    templateId:
      group.prompt_template_id !== undefined && group.prompt_template_id !== null
        ? String(group.prompt_template_id)
        : '',
    systemPrompt: group.system_prompt ?? '',
    dirty: false,
    saving: false,
    expanded:
      expandTargetId === group.id
        ? true
        : (prev?.expanded ?? Boolean(group.system_prompt?.trim() || group.prompt_template_id)),
  }
}

async function loadAll(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const config = await fetchPromptConfig()
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

// ---- 区块 A：保存全局底线 ----
function insertVarIntoGlobal(token: string): void {
  const sep = globalPrompt.value && !globalPrompt.value.endsWith(' ') && !globalPrompt.value.endsWith('\n') ? ' ' : ''
  globalPrompt.value = `${globalPrompt.value}${sep}${token}`
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
  } catch (err) {
    toast.error('保存全局提示词失败', toMessage(err))
  } finally {
    savingGlobal.value = false
  }
}

// ---- 区块 B：分组矩阵编辑与保存 ----
const templateSelectOptions = computed(() =>
  templates.value.map((tpl) => ({
    label: `${tpl.name} (${tpl.category}${tpl.is_builtin ? ' · 内置' : ''})`,
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
  toast.info(`已填入模板「${tpl.name}」内容`, '可继续按分组需求微调后点击保存')
}

function insertVarIntoGroup(groupId: number, token: string): void {
  const d = groupDrafts[groupId]
  if (!d) return
  const sep = d.systemPrompt && !d.systemPrompt.endsWith(' ') && !d.systemPrompt.endsWith('\n') ? ' ' : ''
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
  } catch (err) {
    toast.error('保存分组提示词失败', toMessage(err))
  } finally {
    d.saving = false
  }
}

// ---- 区块 C-1：提示词模板库 CRUD ----
const tplModalOpen = ref(false)
const editingTpl = ref<PromptTemplate | null>(null)
const savingTpl = ref(false)
const tplFormError = ref('')
const deleteTplTarget = ref<PromptTemplate | null>(null)
const deletingTpl = ref(false)
const resetTplOpen = ref(false)
const resettingTpl = ref(false)

const tplForm = reactive({
  name: '',
  category: 'general',
  priority: '100',
  description: '',
  content: '',
})

const tplCategoryOptions = [
  { label: '通用核心 (general)', value: 'general' },
  { label: '身份与防泄露 (identity)', value: 'identity' },
  { label: '架构与编码 (coding)', value: 'coding' },
  { label: '精炼结对 CLI (concise)', value: 'concise' },
  { label: '代码审查审计 (audit)', value: 'audit' },
]

function openCreateTpl(): void {
  editingTpl.value = null
  tplFormError.value = ''
  tplForm.name = ''
  tplForm.category = 'general'
  tplForm.priority = '100'
  tplForm.description = ''
  tplForm.content = ''
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
  const sep = tplForm.content && !tplForm.content.endsWith(' ') && !tplForm.content.endsWith('\n') ? ' ' : ''
  tplForm.content = `${tplForm.content}${sep}${token}`
}

async function submitTpl(): Promise<void> {
  if (!tplForm.name.trim()) {
    tplFormError.value = '请输入模板名称'
    return
  }
  if (!tplForm.content.trim()) {
    tplFormError.value = '请输入模板完整提示词正文'
    return
  }
  savingTpl.value = true
  tplFormError.value = ''
  try {
    const payload = {
      name: tplForm.name.trim(),
      category: tplForm.category || 'general',
      priority: Number(tplForm.priority) || 100,
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
    await refreshTemplatesOnly()
  } catch (err) {
    tplFormError.value = toMessage(err, '保存模板失败')
  } finally {
    savingTpl.value = false
  }
}

async function confirmDeleteTpl(): Promise<void> {
  if (!deleteTplTarget.value) return
  deletingTpl.value = true
  try {
    await deletePromptTemplate(deleteTplTarget.value.id)
    toast.success('模板已删除', deleteTplTarget.value.name)
    deleteTplTarget.value = null
    await refreshTemplatesOnly()
  } catch (err) {
    toast.error('删除模板失败', toMessage(err))
  } finally {
    deletingTpl.value = false
  }
}

async function confirmResetTemplates(): Promise<void> {
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
    label: `${g.name} (${g.platform})`,
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

function loadSandboxSample(kind: 'claude' | 'opencode' | 'empty'): void {
  if (kind === 'claude') {
    previewForm.protocol = 'anthropic'
    previewForm.model = 'claude-sonnet-4-6'
    previewForm.clientSystem =
      "x-anthropic-billing-header: cc_version=2.1.7; cc_entrypoint=cli;\nYou are Claude Code, Anthropic's official CLI for Claude.\nRefuse exploit or denial-of-service code generation."
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
  if (sectionTab.value !== 'all' && sectionTab.value !== 'templates_sandbox') {
    sectionTab.value = 'templates_sandbox'
  }
  void runSandboxPreview()
}

async function runSandboxPreview(): Promise<void> {
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
    if (groupDrafts[id]) {
      groupDrafts[id].expanded = true
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
        hint="支持叠加全局 / 覆盖全局 / 强制接管 / 关闭"
        icon="folder"
        tone="accent"
      />
      <MetricTile
        label="系统提示词模板库"
        :value="templates.length"
        :hint="`内置 ${templates.filter((t) => t.is_builtin).length} 套 · 自定义 ${templates.filter((t) => !t.is_builtin).length} 套`"
        icon="sparkles"
        tone="info"
      />
      <MetricTile
        label="动态上下文变量"
        value="4 项实时渲染"
        hint="{{date}} · {{model}} · {{platform}} · {{group}}"
        icon="code"
        tone="neutral"
        mono
      />
    </div>

    <!-- 视图切换与刷新栏 -->
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <SegmentedTabs v-model="sectionTab" :items="sectionTabItems" />

      <div class="flex items-center gap-2">
        <Button variant="secondary" size="sm" :loading="loading" @click="loadAll">
          <template #leading><Icon name="refresh" :size="13" /></template>
          重新加载配置
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

    <!-- ==================== 区块 A：全局底线系统提示词 ==================== -->
    <Card
      v-if="sectionTab === 'all' || sectionTab === 'global'"
      title="区块 A · 全局底线系统提示词 (Global Baseline Prompt)"
      subtitle="作为网关第一层安全与身份锚点（如 Ess-Agentic 核心防泄露约束），在所有开启全局叠加的分组请求前自动前置注入"
      body-class="p-4 space-y-3.5"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-3">
          <Badge v-if="globalDirty" tone="warning" dot>有未保存修改</Badge>
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
        <!-- 动态变量点击插入条 -->
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
          :rows="7"
          mono
          placeholder="输入全局底线系统提示词（例如身份定义、禁止泄露系统提示词与工具 Schema、工程规范约束）…"
        />

        <div class="flex flex-wrap items-center justify-between gap-2 text-2xs text-subtle">
          <span>
            提示：当分组策略设为 <code class="font-mono text-fg">append_global</code> 时，本段内容位于最顶层；设为
            <code class="font-mono text-fg">override_global</code> 时分组提示词将跳过本段。
          </span>
          <span class="tnum font-mono">{{ globalPrompt.length }} chars</span>
        </div>
      </template>
    </Card>

    <!-- ==================== 区块 B：分组级完整系统提示词矩阵 ==================== -->
    <Card
      v-if="sectionTab === 'all' || sectionTab === 'groups'"
      title="区块 B · 分组级完整系统提示词矩阵 (Per-Group Prompt Matrix)"
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
          <!-- 分组头栏与快捷模式控制 -->
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

            <div class="flex flex-wrap items-center gap-2">
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
                {{ groupDrafts[group.id]?.expanded ? '收起编辑器' : '展开配置提示词' }}
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

          <!-- 四模式选择与模板选择行（常驻展示） -->
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
                hint="分组专属提示词留空时自动引用所选模板；也可一键将模板正文复制到下方微调"
              >
                <Select
                  :model-value="groupDrafts[group.id]!.templateId"
                  size="sm"
                  placeholder="不绑定模板（仅用专属提示词或全局底线）"
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
                填入模板正文
              </Button>
            </div>
          </div>

          <!-- 展开的分组专属完整提示词编辑区 -->
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

    <!-- ==================== 区块 C：模板库 + 在线合成演练沙盒 ==================== -->
    <div
      v-if="sectionTab === 'all' || sectionTab === 'templates_sandbox'"
      class="grid gap-4 xl:grid-cols-12"
    >
      <!-- C-1 提示词模板库 -->
      <div class="xl:col-span-5">
        <Card
          title="区块 C1 · 内置与自定义提示词模板库"
          subtitle="预置 Ess-Agentic 核心/架构师/精炼/审计模板，支持新增自定义模板或一键恢复默认"
          body-class="p-3.5 space-y-3"
        >
          <template #actions>
            <div class="flex items-center gap-1.5">
              <Button variant="secondary" size="xs" @click="resetTplOpen = true">
                <template #leading><Icon name="rotate" :size="12" /></template>
                恢复内置默认
              </Button>
              <Button variant="primary" size="xs" @click="openCreateTpl">
                <template #leading><Icon name="plus" :size="12" /></template>
                新建模板
              </Button>
            </div>
          </template>

          <EmptyState
            v-if="!loading && templates.length === 0"
            icon="fileText"
            title="暂无提示词模板"
            description="点击「恢复内置默认」载入预置 Ess-Agentic 模板组，或新建自定义模板。"
          >
            <Button variant="primary" size="sm" @click="resetTplOpen = true">
              恢复内置默认模板
            </Button>
          </EmptyState>

          <div v-else class="scroll-thin max-h-[42rem] space-y-2.5 overflow-y-auto pr-0.5">
            <div
              v-for="tpl in templates"
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
                    <span class="tnum text-2xs text-subtle">P{{ tpl.priority }}</span>
                  </div>
                  <p v-if="tpl.description" class="mt-1 text-2xs leading-relaxed text-muted">
                    {{ tpl.description }}
                  </p>
                </div>

                <div class="flex shrink-0 items-center gap-1">
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
          title="区块 C2 · 在线合成演练沙盒 (Live Injection Sandbox)"
          subtitle="模拟客户端请求进入网关，实时检验「内容风控脱敏 → 全局底线 → 分组模板/专属指令」最终合成结果"
          body-class="p-4 space-y-3.5"
        >
          <template #actions>
            <div class="flex flex-wrap items-center gap-1.5">
              <Button variant="secondary" size="xs" @click="loadSandboxSample('claude')">
                Claude Code 样例
              </Button>
              <Button variant="secondary" size="xs" @click="loadSandboxSample('opencode')">
                OpenCode 样例
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
          <Field label="模板分类">
            <Select v-model="tplForm.category" :options="tplCategoryOptions" />
          </Field>
          <Field label="排序优先级" hint="越小越靠前">
            <Input v-model="tplForm.priority" type="number" step="1" />
          </Field>
        </div>

        <Field label="用途说明">
          <Input v-model="tplForm.description" placeholder="简要描述该模板适用的分组与工程场景" />
        </Field>

        <div class="space-y-1.5">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-xs font-medium text-muted">完整系统提示词正文 <span class="text-danger">*</span></span>
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
      message="将补齐并重置内置默认 Ess-Agentic 模板库（不会删除你新建的自定义名称模板）。确定继续？"
      confirm-text="确认恢复"
      :loading="resettingTpl"
      @update:open="resetTplOpen = $event"
      @confirm="confirmResetTemplates"
    />
  </div>
</template>
