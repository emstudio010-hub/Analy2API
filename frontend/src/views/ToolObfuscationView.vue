<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'

import { toMessage } from '@/api/client'
import { sessionGuardApi, toolObfuscationApi } from '@/api/endpoints'
import type {
  GroupToolObfMode,
  GroupToolObfuscationSummary,
  SessionGuardConfig,
  SessionGuardResponse,
  ToolObfuscationConfig,
  ToolObfuscationGroupOverride,
  ToolObfuscationMode,
  ToolObfuscationPreviewResponse,
  ToolObfuscationResponse,
  ToolObfuscationScope,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
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
import { PLATFORMS } from '@/utils/platforms'

const toast = useToastStore()

const loading = ref(true)
const saving = ref(false)
const rotatingSalt = ref(false)
const rotateConfirmOpen = ref(false)
const error = ref('')

const viewData = ref<ToolObfuscationResponse | null>(null)
const aliasRefOpen = ref(false)
const aliasSearch = ref('')

interface CustomMapRow {
  source: string
  target: string
}

const form = reactive({
  enabled: true,
  mode: 'hash' as ToolObfuscationMode,
  prefix: 'ess_t_',
  salt: '',
  scope: 'all' as ToolObfuscationScope,
  platforms: ['workbuddy', 'antigravity', 'commandcode', 'opencode'] as string[],
  customRows: [] as CustomMapRow[],
  excludeText: '',
  rewriteToolRefs: false,
})

const modeOptions = [
  { label: 'Hash 确定性短名（默认 · 最强隐藏 & KV-Cache 友好）', value: 'hash' },
  { label: 'Alias 中性语义别名（未知工具自动回退 Hash）', value: 'alias' },
  { label: 'Prefix 前缀封装（保留语义便于调试排障）', value: 'prefix' },
]

const scopeOptions = [
  { label: '全部工具（覆盖内置 / MCP / 自定义函数）', value: 'all' },
  { label: '仅高风险 Coding Agent 工具（Bash / Edit / Read / mcp__ 等）', value: 'sensitive_only' },
]

const modeBadgeMap: Record<string, { label: string; desc: string }> = {
  hash: { label: 'Hash 哈希短名', desc: 'prefix + sha256(salt:orig)[:10]' },
  alias: { label: 'Alias 中性别名', desc: 'exec_task / fetch_entry 等中性语义' },
  prefix: { label: 'Prefix 前缀伪装', desc: 'prefix + orig（超 64 位确定性截断）' },
}

function hydrateForm(res: ToolObfuscationResponse): void {
  viewData.value = res
  const cfg = res.config
  form.enabled = Boolean(cfg.enabled)
  form.mode = (cfg.mode || 'hash') as ToolObfuscationMode
  form.prefix = cfg.prefix || 'ess_t_'
  form.salt = cfg.salt || ''
  form.scope = (cfg.scope || 'all') as ToolObfuscationScope
  form.platforms = [...(cfg.platforms ?? [])]
  form.customRows = Object.entries(cfg.custom_map ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([source, target]) => ({ source, target }))
  form.excludeText = (cfg.exclude_tools ?? []).join('\n')
  form.rewriteToolRefs = Boolean(cfg.rewrite_tool_refs)
}

function buildConfigPayload(): ToolObfuscationConfig {
  const custom_map: Record<string, string> = {}
  for (const row of form.customRows) {
    const src = row.source.trim()
    const tgt = row.target.trim()
    if (src && tgt) {
      custom_map[src] = tgt
    }
  }
  const exclude_tools = form.excludeText
    .split(/[\n,;]+/)
    .map((s) => s.trim())
    .filter(Boolean)

  return {
    enabled: form.enabled,
    mode: form.mode,
    prefix: form.prefix.trim() || 'ess_t_',
    salt: form.salt.trim(),
    scope: form.scope,
    platforms: [...form.platforms],
    custom_map,
    exclude_tools,
    rewrite_tool_refs: form.rewriteToolRefs,
  }
}

async function loadConfig(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await toolObfuscationApi.get()
    hydrateForm(res)
    syncGroupObfDrafts(res.groups)
  } catch (err) {
    error.value = toMessage(err, '加载工具名混淆配置失败')
  } finally {
    loading.value = false
  }
}

async function saveConfig(): Promise<void> {
  if (saving.value) return
  saving.value = true
  try {
    const updated = await toolObfuscationApi.update(buildConfigPayload())
    hydrateForm(updated)
    toast.success('工具名混淆策略已保存并热生效')
    void runPreview()
  } catch (err) {
    toast.error('保存配置失败', toMessage(err))
  } finally {
    saving.value = false
  }
}

async function confirmRotateSalt(): Promise<void> {
  if (rotatingSalt.value) return
  rotatingSalt.value = true
  try {
    const updated = await toolObfuscationApi.rotateSalt()
    hydrateForm(updated)
    rotateConfirmOpen.value = false
    toast.success('随机盐值已轮换', '哈希短名指纹已立即刷新生效')
    void runPreview()
  } catch (err) {
    toast.error('轮换盐值失败', toMessage(err))
  } finally {
    rotatingSalt.value = false
  }
}

function togglePlatform(plat: string): void {
  const idx = form.platforms.indexOf(plat)
  if (idx >= 0) {
    form.platforms.splice(idx, 1)
  } else {
    form.platforms.push(plat)
  }
}

function addCustomRow(): void {
  form.customRows.push({ source: '', target: '' })
}

function removeCustomRow(idx: number): void {
  form.customRows.splice(idx, 1)
}

async function copySalt(): Promise<void> {
  if (!form.salt) return
  const ok = await copyText(form.salt)
  if (ok) toast.success('盐值已复制到剪贴板')
}

const filteredAliases = computed(() => {
  const entries = Object.entries(viewData.value?.builtin_aliases ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([orig, alias]) => ({ orig, alias }))
  const q = aliasSearch.value.trim().toLowerCase()
  if (!q) return entries
  return entries.filter(
    (item) => item.orig.toLowerCase().includes(q) || item.alias.toLowerCase().includes(q),
  )
})

const activeCustomCount = computed(
  () => form.customRows.filter((r) => r.source.trim() && r.target.trim()).length,
)

const activeExcludeCount = computed(
  () =>
    form.excludeText
      .split(/[\n,;]+/)
      .map((s) => s.trim())
      .filter(Boolean).length,
)

// ---- 各分组独立工具名混淆策略矩阵 ----
interface GroupObfDraft {
  mode: GroupToolObfMode
  prefix: string
  scope: string
  saving: boolean
}
const groupObfDrafts = reactive<Record<number, GroupObfDraft>>({})
const groupModeOptions: Array<{ label: string; value: GroupToolObfMode }> = [
  { label: '继承全局策略 (inherit)', value: 'inherit' },
  { label: '本组关闭 (off)', value: 'off' },
  { label: 'Hash 确定性短名 (hash)', value: 'hash' },
  { label: 'Alias 中性语义别名 (alias)', value: 'alias' },
  { label: 'Prefix 前缀封装 (prefix)', value: 'prefix' },
]
const groupScopeOptions = [
  { label: '继承全局范围', value: '' },
  { label: '全部工具 (all)', value: 'all' },
  { label: '仅敏感工具 (sensitive_only)', value: 'sensitive_only' },
]

function syncGroupObfDrafts(groups: GroupToolObfuscationSummary[] | undefined): void {
  if (!groups) return
  for (const g of groups) {
    groupObfDrafts[g.group_id] = {
      mode: (g.override?.mode || 'inherit') as GroupToolObfMode,
      prefix: g.override?.prefix || '',
      scope: g.override?.scope || '',
      saving: false,
    }
  }
}

async function saveGroupObfOverride(g: GroupToolObfuscationSummary): Promise<void> {
  const draft = groupObfDrafts[g.group_id]
  if (!draft || draft.saving) return
  draft.saving = true
  try {
    const existing = g.override ?? { mode: 'inherit' }
    const payload: ToolObfuscationGroupOverride = {
      ...existing,
      mode: draft.mode,
      prefix: draft.prefix.trim() || undefined,
      scope: draft.scope || undefined,
    }
    const updated = await toolObfuscationApi.updateGroup(g.group_id, payload)
    hydrateForm(updated)
    syncGroupObfDrafts(updated.groups)
    toast.success(`分组「${g.group_name}」工具混淆策略已保存并生效`)
  } catch (err) {
    toast.error('保存分组工具混淆策略失败', toMessage(err))
  } finally {
    draft.saving = false
  }
}

function previewThisGroup(groupId: number): void {
  previewGroupId.value = String(groupId)
  void runPreview()
}

// ---- 非账号额度不足 429 会话暂停保护（5分钟）与请求体 Log 审计中心 ----
const sgLoading = ref(false)
const sgSaving = ref(false)
const sgData = ref<SessionGuardResponse | null>(null)
const sgForm = reactive({
  enabled: true,
  pauseSeconds: 300,
  logEnabled: true,
  logDir: './data/logs/session_429',
  maxLogFiles: 200,
  abortCurrentRetry: true,
  fallbackFingerprint: true,
  quotaKeywordsText: '',
})
const logModalOpen = ref(false)
const logModalFilename = ref('')
const logModalContent = ref('')
const logModalLoading = ref(false)

function hydrateSessionGuard(res: SessionGuardResponse): void {
  sgData.value = res
  const cfg = res.config
  sgForm.enabled = Boolean(cfg.enabled)
  sgForm.pauseSeconds = Number(cfg.pause_seconds) || 300
  sgForm.logEnabled = Boolean(cfg.log_enabled)
  sgForm.logDir = cfg.log_dir || './data/logs/session_429'
  sgForm.maxLogFiles = Number(cfg.max_log_files ?? 200)
  sgForm.abortCurrentRetry = Boolean(cfg.abort_current_retry)
  sgForm.fallbackFingerprint = Boolean(cfg.fallback_fingerprint)
  sgForm.quotaKeywordsText = (cfg.quota_keywords ?? []).join(', ')
}

async function loadSessionGuard(): Promise<void> {
  sgLoading.value = true
  try {
    const res = await sessionGuardApi.get()
    hydrateSessionGuard(res)
  } catch {
    /* 容错处理 */
  } finally {
    sgLoading.value = false
  }
}

async function saveSessionGuard(): Promise<void> {
  if (sgSaving.value) return
  sgSaving.value = true
  try {
    const keywords = sgForm.quotaKeywordsText
      .split(/[\n,;]+/)
      .map((s) => s.trim())
      .filter(Boolean)
    const cfg: SessionGuardConfig = {
      enabled: sgForm.enabled,
      pause_seconds: Math.max(5, Number(sgForm.pauseSeconds) || 300),
      log_enabled: sgForm.logEnabled,
      log_dir: sgForm.logDir.trim() || './data/logs/session_429',
      max_log_files: Math.max(0, Number(sgForm.maxLogFiles) || 200),
      abort_current_retry: sgForm.abortCurrentRetry,
      fallback_fingerprint: sgForm.fallbackFingerprint,
      quota_keywords: keywords,
    }
    const updated = await sessionGuardApi.updateConfig(cfg)
    hydrateSessionGuard(updated)
    toast.success('非额度 429 会话暂停配置已热生效', `暂停时长 ${updated.config.pause_seconds} 秒`)
  } catch (err) {
    toast.error('保存 429 会话保护配置失败', toMessage(err))
  } finally {
    sgSaving.value = false
  }
}

async function resumePausedSession(sid: string): Promise<void> {
  try {
    const res = await sessionGuardApi.resumeSession(sid)
    if (res.view) hydrateSessionGuard(res.view)
    toast.success('已恢复会话调度', sid)
  } catch (err) {
    toast.error('恢复会话失败', toMessage(err))
  }
}

async function clearAllPausedSessions(): Promise<void> {
  try {
    const res = await sessionGuardApi.clearAllSessions()
    if (res.view) hydrateSessionGuard(res.view)
    toast.success(`已清空全部暂停会话 (${res.cleared})`)
  } catch (err) {
    toast.error('清空暂停列表失败', toMessage(err))
  }
}

async function openLogFile(filename?: string): Promise<void> {
  if (!filename) return
  logModalOpen.value = true
  logModalFilename.value = filename
  logModalContent.value = ''
  logModalLoading.value = true
  try {
    const res = await sessionGuardApi.readLog(filename)
    logModalContent.value = res.content
  } catch (err) {
    logModalContent.value = `读取日志文件失败: ${toMessage(err)}`
  } finally {
    logModalLoading.value = false
  }
}

// ---- 双向混淆与还原交互测试台 ----
const previewGroupId = ref('')
const previewGroupOptions = computed(() => [
  { label: '按全局策略预览', value: '' },
  ...(viewData.value?.groups ?? []).map((g) => ({
    label: `分组 #${g.group_id} ${g.group_name} (${g.override?.mode || 'inherit'})`,
    value: String(g.group_id),
  })),
])
const previewMode = ref<'list' | 'json'>('list')
const previewModeTabs = [
  { value: 'list', label: '工具名列表模式' },
  { value: 'json', label: '完整请求 JSON 模式' },
]

const previewProtocol = ref<'openai' | 'anthropic' | 'responses'>('openai')
const previewProtocolOptions = [
  { label: 'OpenAI (/v1/chat/completions)', value: 'openai' },
  { label: 'Anthropic (/v1/messages)', value: 'anthropic' },
  { label: 'Responses (/v1/responses)', value: 'responses' },
]

const toolNamesInput = ref(
  [
    'Bash',
    'Read',
    'Edit',
    'Write',
    'Grep',
    'Glob',
    'apply_patch',
    'mcp__github__create_pull_request_review_comment_thread',
    'weather_lookup',
  ].join('\n'),
)

const SAMPLE_JSON_BY_PROTO: Record<'openai' | 'anthropic' | 'responses', string> = {
  openai: JSON.stringify(
    {
      model: 'claude-sonnet-4-6',
      tools: [
        {
          type: 'function',
          function: {
            name: 'Bash',
            description: 'Execute shell command via `Bash` cleanly; check files with `Read`.',
            parameters: { type: 'object', properties: { command: { type: 'string' } } },
          },
        },
        {
          type: 'function',
          function: {
            name: 'Edit',
            description: 'Patch target file cleanly.',
            parameters: { type: 'object', properties: { file: { type: 'string' } } },
          },
        },
        {
          type: 'function',
          function: {
            name: 'mcp__github__enterprise_long_action_create_pull_request_thread_item',
            description: 'MCP GitHub action',
            parameters: { type: 'object', properties: {} },
          },
        },
      ],
      tool_choice: { type: 'function', function: { name: 'Bash' } },
      messages: [
        { role: 'user', content: 'Please check `Edit` and run pwd via `Bash`.' },
      ],
    },
    null,
    2,
  ),
  anthropic: JSON.stringify(
    {
      model: 'claude-sonnet-4-6',
      max_tokens: 1024,
      tools: [
        {
          name: 'Bash',
          description: 'Execute task with `Bash`',
          input_schema: { type: 'object', properties: { command: { type: 'string' } } },
        },
        {
          name: 'Read',
          description: 'Read target path',
          input_schema: { type: 'object', properties: { path: { type: 'string' } } },
        },
      ],
      tool_choice: { type: 'tool', name: 'Bash' },
      messages: [{ role: 'user', content: 'Run tests via `Bash`.' }],
    },
    null,
    2,
  ),
  responses: JSON.stringify(
    {
      model: 'gpt-5.3-codex',
      tools: [
        {
          type: 'function',
          name: 'exec_command',
          description: 'Run terminal job',
          parameters: { type: 'object', properties: { cmd: { type: 'string' } } },
        },
        {
          type: 'function',
          name: 'apply_patch',
          description: 'Apply unified diff patch',
          parameters: { type: 'object', properties: { patch: { type: 'string' } } },
        },
      ],
      tool_choice: { type: 'function', name: 'exec_command' },
      input: 'Check files and apply patch.',
    },
    null,
    2,
  ),
}

const jsonPayloadInput = ref(SAMPLE_JSON_BY_PROTO.openai)
const previewLoading = ref(false)
const previewError = ref('')
const previewResult = ref<ToolObfuscationPreviewResponse | null>(null)

function loadSamplePayload(proto: 'openai' | 'anthropic' | 'responses'): void {
  previewProtocol.value = proto
  jsonPayloadInput.value = SAMPLE_JSON_BY_PROTO[proto]
  previewMode.value = 'json'
  void runPreview()
}

async function runPreview(): Promise<void> {
  if (previewLoading.value) return
  previewLoading.value = true
  previewError.value = ''
  try {
    const draft = buildConfigPayload()
    const gid = previewGroupId.value ? Number(previewGroupId.value) : undefined
    if (previewMode.value === 'json') {
      previewResult.value = await toolObfuscationApi.preview({
        group_id: gid,
        payload: jsonPayloadInput.value,
        protocol: previewProtocol.value,
        config: draft,
      })
    } else {
      const tool_names = toolNamesInput.value
        .split(/[\n,]+/)
        .map((s) => s.trim())
        .filter(Boolean)
      previewResult.value = await toolObfuscationApi.preview({
        group_id: gid,
        tool_names,
        config: draft,
      })
    }
  } catch (err) {
    previewError.value = toMessage(err, '执行双向混淆演练失败')
  } finally {
    previewLoading.value = false
  }
}

onMounted(async () => {
  await Promise.all([loadConfig(), loadSessionGuard()])
  if (!error.value) {
    await runPreview()
  }
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部 KPI 概览 -->
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <MetricTile
        label="工具名混淆状态"
        :value="form.enabled ? '已启用' : '已停用'"
        :hint="form.enabled ? '出站伪装 · 回流双射无损还原' : '当前原样透传所有工具名'"
        icon="lock"
        :tone="form.enabled ? 'success' : 'neutral'"
      />
      <MetricTile
        label="当前混淆策略"
        :value="modeBadgeMap[form.mode]?.label ?? form.mode"
        :hint="`${modeBadgeMap[form.mode]?.desc ?? ''} · ${form.scope === 'all' ? '全部工具' : '仅敏感工具'}`"
        icon="sparkles"
        tone="accent"
      />
      <MetricTile
        label="生效上游平台"
        :value="`${form.platforms.length} / 4`"
        hint="WorkBuddy · AntiGravity · Command Code · OpenCode"
        icon="layers"
        tone="info"
      />
      <MetricTile
        label="映射与排除规则"
        :value="`${activeCustomCount} 自定义 / ${activeExcludeCount} 排除`"
        :hint="`内置 ${viewData?.stats?.builtin_aliases_count ?? 0} 条中性别名 · ${viewData?.stats?.sensitive_tools_count ?? 0} 个识别词`"
        icon="shield"
        tone="neutral"
      />
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="loadConfig">重试</Button>
    </p>

    <!-- 核心策略配置 -->
    <Card
      title="核心混淆策略与平台作用域"
      subtitle="基于确定性双射会话表，在保护 Coding Agent / MCP 工具特征的同时保持多轮对话 KV-Cache 稳定命中"
      body-class="p-4 space-y-4"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-3">
          <Switch
            v-model="form.enabled"
            size="sm"
            :label="form.enabled ? '全局混淆已开启' : '全局混淆已关闭'"
          />
          <Button variant="secondary" size="sm" :loading="loading" @click="loadConfig">
            <template #leading><Icon name="refresh" :size="13" /></template>
            重新加载
          </Button>
          <Button variant="primary" size="sm" :loading="saving" @click="saveConfig">
            <template #leading><Icon name="check" :size="13" /></template>
            保存并热生效
          </Button>
        </div>
      </template>

      <div v-if="loading" class="space-y-2.5">
        <Skeleton class="h-9 w-full" />
        <Skeleton class="h-24 w-full" />
      </div>

      <template v-else>
        <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
          <div class="space-y-1">
            <label class="block text-2xs font-medium text-muted">混淆模式 (Mode)</label>
            <Select v-model="form.mode" size="sm" :options="modeOptions" />
          </div>

          <div class="space-y-1">
            <label class="block text-2xs font-medium text-muted">作用范围 (Scope)</label>
            <Select v-model="form.scope" size="sm" :options="scopeOptions" />
          </div>

          <div class="space-y-1">
            <label class="block text-2xs font-medium text-muted">中性前缀 (Prefix)</label>
            <Input
              v-model="form.prefix"
              size="sm"
              mono
              placeholder="ess_t_"
            />
          </div>

          <div class="space-y-1">
            <label class="block text-2xs font-medium text-muted">确定性盐值 (Salt)</label>
            <div class="flex items-center gap-1.5">
              <Input
                v-model="form.salt"
                size="sm"
                mono
                placeholder="自动生成 32 位随机 Hex"
                class="flex-1"
              />
              <Button variant="ghost" size="sm" title="复制盐值" @click="copySalt">
                <Icon name="copy" :size="13" />
              </Button>
              <Button
                variant="secondary"
                size="sm"
                title="轮换盐值"
                @click="rotateConfirmOpen = true"
              >
                轮换
              </Button>
            </div>
          </div>
        </div>

        <!-- 四平台复选栏 + Prompt 反引号重写开关 -->
        <div class="grid grid-cols-1 gap-3 lg:grid-cols-12">
          <div class="space-y-1.5 lg:col-span-8">
            <label class="block text-2xs font-medium text-muted">生效上游平台</label>
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-4">
              <button
                v-for="plat in PLATFORMS"
                :key="plat.value"
                type="button"
                class="flex items-center justify-between rounded-md border px-3 py-2 text-left transition"
                :class="
                  form.platforms.includes(plat.value)
                    ? 'border-accent/60 bg-accent/8 text-fg'
                    : 'border-line bg-canvas text-muted hover:border-line-strong'
                "
                @click="togglePlatform(plat.value)"
              >
                <div class="flex min-w-0 items-center gap-2">
                  <PlatformBadge :platform="plat.value" />
                </div>
                <span
                  class="flex h-4 w-4 shrink-0 items-center justify-center rounded border text-[10px]"
                  :class="
                    form.platforms.includes(plat.value)
                      ? 'border-accent bg-accent text-white'
                      : 'border-line bg-surface text-transparent'
                  "
                >
                  <Icon name="check" :size="11" />
                </span>
              </button>
            </div>
          </div>

          <div class="flex flex-col justify-between rounded-md border border-line bg-raised/50 p-3 lg:col-span-4">
            <div class="space-y-1">
              <div class="text-xs font-medium text-fg">同步重写反引号工具引用</div>
              <p class="text-2xs leading-relaxed text-subtle">
                开启后，仅对消息与工具描述中被单个反引号 <code class="font-mono text-fg">`ToolName`</code> 包裹的精确工具名同步替换为混淆名，不误伤普通文本或代码块。
              </p>
            </div>
            <div class="mt-2.5 flex justify-end">
              <Switch
                v-model="form.rewriteToolRefs"
                size="sm"
                :label="form.rewriteToolRefs ? '已开启引用重写' : '仅重写结构化字段'"
              />
            </div>
          </div>
        </div>
      </template>
    </Card>

      <!-- 各分组工具混淆独立策略矩阵 -->
      <Card
        title="各分组工具混淆独立策略矩阵 (Per-Group Override)"
        subtitle="每个分组可独立覆盖全局工具混淆算法、前缀与作用域（无需重启，保存后立即作用于路由调度与混淆双向还原）"
      >
        <div v-if="(viewData?.groups?.length ?? 0) === 0" class="py-4 text-center text-xs text-[var(--text-muted)]">
          暂无分组数据，可前往分组管理创建分组。
        </div>
        <div v-else class="overflow-x-auto">
          <table class="w-full text-left text-xs">
            <thead>
              <tr class="border-b border-[var(--border-subtle)] text-[var(--text-muted)]">
                <th class="py-2 pr-3 font-medium">分组</th>
                <th class="py-2 pr-3 font-medium">平台</th>
                <th class="py-2 pr-3 font-medium">当前生效</th>
                <th class="py-2 pr-3 font-medium">策略模式 (Mode)</th>
                <th class="py-2 pr-3 font-medium">分组前缀 (Prefix)</th>
                <th class="py-2 pr-3 font-medium">作用域 (Scope)</th>
                <th class="py-2 text-right font-medium">操作</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border-subtle)]">
              <tr v-for="g in viewData?.groups ?? []" :key="g.group_id">
                <td class="py-2.5 pr-3 font-medium text-[var(--text-primary)]">
                  {{ g.group_name }}
                </td>
                <td class="py-2.5 pr-3">
                  <Badge variant="neutral">{{ g.platform }}</Badge>
                </td>
                <td class="py-2.5 pr-3">
                  <Badge :variant="g.effective_enabled ? 'success' : 'neutral'">
                    {{ g.effective_enabled ? g.effective_mode : 'off' }}
                  </Badge>
                </td>
                <td class="py-2.5 pr-3 min-w-[170px]">
                  <Select
                    v-if="groupObfDrafts[g.group_id]"
                    v-model="groupObfDrafts[g.group_id].mode"
                    size="sm"
                    :options="groupModeOptions"
                  />
                </td>
                <td class="py-2.5 pr-3 min-w-[130px]">
                  <Input
                    v-if="groupObfDrafts[g.group_id]"
                    v-model="groupObfDrafts[g.group_id].prefix"
                    size="sm"
                    placeholder="继承或输入前缀"
                    :disabled="groupObfDrafts[g.group_id].mode !== 'prefix'"
                  />
                </td>
                <td class="py-2.5 pr-3 min-w-[155px]">
                  <Select
                    v-if="groupObfDrafts[g.group_id]"
                    v-model="groupObfDrafts[g.group_id].scope"
                    size="sm"
                    :options="groupScopeOptions"
                  />
                </td>
                <td class="py-2.5 text-right">
                  <div class="inline-flex items-center gap-1.5">
                    <Button variant="secondary" size="sm" @click="previewThisGroup(g.group_id)">
                      预览该组
                    </Button>
                    <Button
                      variant="primary"
                      size="sm"
                      :loading="groupObfDrafts[g.group_id]?.saving"
                      @click="saveGroupObfOverride(g)"
                    >
                      保存
                    </Button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </Card>

      <!-- 自定义覆盖映射 + 排除白名单 -->
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-12">
      <Card
        class="lg:col-span-7"
        title="自定义覆盖映射 (Custom Map)"
        subtitle="优先级高于 Hash / Alias / Prefix；目标名须符合 ^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$ 且不可重名"
        body-class="p-4 space-y-3"
      >
        <template #actions>
          <Button variant="secondary" size="xs" @click="addCustomRow">
            <template #leading><Icon name="plus" :size="12" /></template>
            新增映射
          </Button>
        </template>

        <div
          v-if="form.customRows.length === 0"
          class="rounded-md border border-dashed border-line px-4 py-6 text-center text-2xs text-subtle"
        >
          暂无自定义覆盖规则。点击右上角「新增映射」可显式固定某个工具名（例如 <code class="font-mono text-fg">Bash → exec_task</code>）。
        </div>

        <div v-else class="space-y-2">
          <div
            v-for="(row, idx) in form.customRows"
            :key="idx"
            class="flex items-center gap-2"
          >
            <Input
              v-model="row.source"
              size="sm"
              mono
              placeholder="原始工具名（如 Bash）"
              class="flex-1"
            />
            <span class="font-mono text-xs text-subtle">→</span>
            <Input
              v-model="row.target"
              size="sm"
              mono
              placeholder="上游目标名（如 exec_task）"
              class="flex-1"
            />
            <Button variant="ghost" size="xs" title="移除此条" @click="removeCustomRow(idx)">
              <Icon name="trash" :size="13" />
            </Button>
          </div>
        </div>
      </Card>

      <Card
        class="lg:col-span-5"
        title="排除名单 (Exclude Tools)"
        subtitle="每行或逗号分隔填写；命中工具将原样透传且自动预占命名空间，防止混淆碰撞"
        body-class="p-4 space-y-2.5"
      >
        <Textarea
          v-model="form.excludeText"
          :rows="6"
          mono
          placeholder="例如：&#10;ping_health&#10;get_weather"
        />
        <p class="text-2xs text-subtle">
          已配置 <span class="font-mono font-medium text-fg">{{ activeExcludeCount }}</span> 个透传排除工具（大小写不敏感匹配）。
        </p>
      </Card>
    </div>

    <!-- 内置语义别名参考表（可折叠） -->
    <Card
      title="内置语义别名词表与敏感工具识别目录"
      :subtitle="`Alias 模式下自动将 ${viewData?.stats?.builtin_aliases_count ?? 0} 个常用 Coding Agent 工具映射为中性操作名；未命中词表的工具自动回退前缀+摘要`"
      body-class="p-4"
    >
      <template #actions>
        <Button variant="ghost" size="xs" @click="aliasRefOpen = !aliasRefOpen">
          <template #leading>
            <Icon :name="aliasRefOpen ? 'chevronDown' : 'chevronRight'" :size="13" />
          </template>
          {{ aliasRefOpen ? '收起参考表' : `展开参考表 (${filteredAliases.length})` }}
        </Button>
      </template>

      <div v-if="!aliasRefOpen" class="text-2xs text-subtle">
        覆盖 Claude Code / Codex CLI / OpenCode / Cline 等常见内置工具（<code class="font-mono text-fg">Bash / Read / Edit / Grep / apply_patch / exec_command / mcp__*</code>）。点击右侧按钮可检索完整对照表。
      </div>

      <div v-else class="space-y-3">
        <div class="w-full sm:w-64">
          <Input v-model="aliasSearch" size="sm" placeholder="筛选原名或中性别名…" />
        </div>
        <div class="grid max-h-64 grid-cols-1 gap-2 overflow-y-auto pr-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          <div
            v-for="item in filteredAliases"
            :key="item.orig"
            class="flex items-center justify-between gap-2 rounded border border-line bg-canvas px-2.5 py-1.5 font-mono text-2xs"
          >
            <span class="truncate text-fg" :title="item.orig">{{ item.orig }}</span>
            <span class="shrink-0 text-subtle">→</span>
            <span class="truncate font-medium text-accent" :title="item.alias">{{ item.alias }}</span>
          </div>
        </div>
      </div>
    </Card>

    <!-- 双向混淆与还原交互测试台 -->
    <Card
      title="双向混淆与还原交互测试台 (Round-Trip Playground)"
      subtitle="直接按当前表单草稿（无需先保存）验证「原工具名 → 上游混淆名 → 下游还原名」全链路 100% 无损双射"
      body-class="p-4 space-y-4"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <div class="w-56">
            <Select v-model="previewGroupId" size="sm" :options="previewGroupOptions" />
          </div>
          <SegmentedTabs v-model="previewMode" :items="previewModeTabs" />
          <Button variant="primary" size="sm" :loading="previewLoading" @click="runPreview">
            <template #leading><Icon name="play" :size="13" /></template>
            运行双向预览
          </Button>
        </div>
      </template>

      <div v-if="previewMode === 'list'" class="space-y-2">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <label class="text-2xs font-medium text-muted">输入待测工具名（每行或逗号分隔）</label>
          <div class="flex items-center gap-1.5 text-2xs">
            <span class="text-subtle">快捷加载完整报文：</span>
            <Button variant="ghost" size="xs" @click="loadSamplePayload('openai')">OpenAI 示例</Button>
            <Button variant="ghost" size="xs" @click="loadSamplePayload('anthropic')">Anthropic 示例</Button>
            <Button variant="ghost" size="xs" @click="loadSamplePayload('responses')">Responses 示例</Button>
          </div>
        </div>
        <Textarea
          v-model="toolNamesInput"
          :rows="4"
          mono
          placeholder="Bash&#10;Read&#10;Edit&#10;mcp__github__create_issue"
        />
      </div>

      <div v-else class="space-y-2.5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="w-60">
            <Select v-model="previewProtocol" size="sm" :options="previewProtocolOptions" />
          </div>
          <div class="flex items-center gap-1.5">
            <Button variant="ghost" size="xs" @click="loadSamplePayload('openai')">OpenAI 示例</Button>
            <Button variant="ghost" size="xs" @click="loadSamplePayload('anthropic')">Anthropic 示例</Button>
            <Button variant="ghost" size="xs" @click="loadSamplePayload('responses')">Responses 示例</Button>
          </div>
        </div>
        <Textarea
          v-model="jsonPayloadInput"
          :rows="8"
          mono
          placeholder="粘贴完整 OpenAI / Anthropic / Responses 请求 JSON…"
        />
      </div>

      <p
        v-if="previewError"
        class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
      >
        <Icon name="alertCircle" :size="14" />
        <span>{{ previewError }}</span>
      </p>

      <div v-if="previewResult" class="space-y-3">
        <div class="flex flex-wrap items-center gap-2">
          <Badge :tone="previewResult.restored_verified ? 'success' : 'danger'" dot>
            {{ previewResult.restored_verified ? '100% 双射无损校验通过' : '存在双射校验异常' }}
          </Badge>
          <Badge tone="neutral">
            已改写 {{ previewResult.changed_count }} / {{ previewResult.total_count }} 个工具
          </Badge>
          <Badge tone="info">
            {{ previewResult.mode }} · {{ previewResult.scope }}
          </Badge>
        </div>

        <!-- 映射结果对比表 -->
        <div class="overflow-x-auto rounded-md border border-line">
          <table class="min-w-full divide-y divide-line text-left text-xs">
            <thead class="bg-raised/70 text-2xs font-medium text-muted">
              <tr>
                <th class="px-3 py-2">客户端原始工具名</th>
                <th class="px-3 py-2">发往上游的混淆名 (Wire)</th>
                <th class="px-3 py-2">回流还原工具名</th>
                <th class="px-3 py-2">状态</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-line bg-surface font-mono text-2xs">
              <tr v-for="m in previewResult.mappings" :key="m.original">
                <td class="max-w-xs truncate px-3 py-2 text-fg" :title="m.original">
                  {{ m.original }}
                </td>
                <td
                  class="max-w-xs truncate px-3 py-2"
                  :class="m.changed ? 'font-semibold text-accent' : 'text-subtle'"
                  :title="m.obfuscated"
                >
                  {{ m.obfuscated }}
                </td>
                <td
                  class="max-w-xs truncate px-3 py-2"
                  :class="m.restored === m.original ? 'text-success' : 'text-danger'"
                  :title="m.restored"
                >
                  {{ m.restored }}
                </td>
                <td class="px-3 py-2 font-sans">
                  <Badge :tone="m.changed ? 'accent' : 'neutral'">
                    {{ m.changed ? '已混淆' : '原样透传' }}
                  </Badge>
                </td>
              </tr>
              <tr v-if="previewResult.mappings.length === 0">
                <td colspan="4" class="px-3 py-4 text-center font-sans text-subtle">
                  未检测到工具定义或待测工具名
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <CodeBlock
          v-if="previewResult.obfuscated_payload"
          title="发往上游的标准化混淆请求 JSON（敏感工具声明 / tool_choice / 历史均已替换）"
          language="json"
          :code="previewResult.obfuscated_payload"
          max-height="22rem"
        />
      </div>
    </Card>

    <!-- 非账号额度不足 429 会话暂停保护与请求体 Log 审计中心 -->
    <Card
      title="非账号额度不足 429 会话暂停保护（5 分钟）与请求体 Log 审计中心"
      subtitle="上游返回 HTTP 429 但非账号额度/点数耗尽（如系统限流/疑似暴露原始工具名风控）时，立即暂停该会话 ID 调度并把完整入站/出站请求体落盘为 .log 文件以便排查未混淆字段"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <Button variant="secondary" size="sm" :loading="sgLoading" @click="loadSessionGuard">
            <template #leading><Icon name="refresh" :size="13" /></template>
            刷新状态
          </Button>
          <Button
            v-if="(sgData?.paused_sessions?.length ?? 0) > 0"
            variant="secondary"
            size="sm"
            @click="clearAllPausedSessions"
          >
            清空全部暂停
          </Button>
          <Button variant="primary" size="sm" :loading="sgSaving" @click="saveSessionGuard">
            <template #leading><Icon name="check" :size="13" /></template>
            保存 429 保护策略
          </Button>
        </div>
      </template>

      <div class="space-y-4">
        <div class="grid gap-3 md:grid-cols-4">
          <label class="flex items-center justify-between rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-subtle)] px-3 py-2 text-xs">
            <span class="font-medium text-[var(--text-primary)]">启用非额度 429 暂停</span>
            <input v-model="sgForm.enabled" type="checkbox" class="h-4 w-4 accent-[var(--accent)]" />
          </label>
          <label class="flex items-center justify-between rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-subtle)] px-3 py-2 text-xs">
            <span class="font-medium text-[var(--text-primary)]">落盘完整请求体 .log</span>
            <input v-model="sgForm.logEnabled" type="checkbox" class="h-4 w-4 accent-[var(--accent)]" />
          </label>
          <label class="flex items-center justify-between rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-subtle)] px-3 py-2 text-xs">
            <span class="font-medium text-[var(--text-primary)]">命中即中止同请求换号重试</span>
            <input v-model="sgForm.abortCurrentRetry" type="checkbox" class="h-4 w-4 accent-[var(--accent)]" />
          </label>
          <label class="flex items-center justify-between rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-subtle)] px-3 py-2 text-xs">
            <span class="font-medium text-[var(--text-primary)]">无 session_id 时回退指纹</span>
            <input v-model="sgForm.fallbackFingerprint" type="checkbox" class="h-4 w-4 accent-[var(--accent)]" />
          </label>
        </div>

        <div class="grid gap-3 md:grid-cols-3">
          <div>
            <div class="mb-1 text-xs font-medium text-[var(--text-secondary)]">暂停时长（秒，默认 300 = 5 分钟）</div>
            <Input
              :model-value="String(sgForm.pauseSeconds)"
              type="number"
              size="sm"
              placeholder="300"
              @update:model-value="sgForm.pauseSeconds = Number($event) || 0"
            />
          </div>
          <div>
            <div class="mb-1 text-xs font-medium text-[var(--text-secondary)]">最大保留 .log 文件数</div>
            <Input
              :model-value="String(sgForm.maxLogFiles)"
              type="number"
              size="sm"
              placeholder="200"
              @update:model-value="sgForm.maxLogFiles = Number($event) || 0"
            />
          </div>
          <div>
            <div class="mb-1 text-xs font-medium text-[var(--text-secondary)]">.log 存储目录（留空则 data/session_429_logs）</div>
            <Input v-model="sgForm.logDir" size="sm" placeholder="data/session_429_logs" />
          </div>
        </div>

        <div>
          <div class="mb-1 text-xs font-medium text-[var(--text-secondary)]">
            账号额度不足豁免关键词（每行一个；命中这些关键词的 429 仍走常规换号，不暂停会话）
          </div>
          <textarea
            v-model="sgForm.quotaKeywordsText"
            rows="3"
            class="w-full rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 py-2 font-mono text-xs text-[var(--text-primary)] focus:border-[var(--accent)] focus:outline-none"
          />
        </div>

        <!-- 当前暂停中的会话列表 -->
        <div class="rounded-[var(--radius-md)] border border-[var(--border-subtle)] p-3">
          <div class="mb-2 flex items-center justify-between">
            <span class="text-xs font-semibold text-[var(--text-primary)]">
              当前暂停调度的会话 ({{ sgData?.paused_sessions?.length ?? 0 }})
            </span>
            <span class="text-[11px] text-[var(--text-muted)]">
              暂停期间同会话请求直接返回 429 保护提示，不消耗上游或账号池
            </span>
          </div>
          <div v-if="(sgData?.paused_sessions?.length ?? 0) === 0" class="py-3 text-center text-xs text-[var(--text-muted)]">
            暂无被暂停的会话 ID
          </div>
          <div v-else class="overflow-x-auto">
            <table class="w-full text-left text-xs">
              <thead>
                <tr class="border-b border-[var(--border-subtle)] text-[var(--text-muted)]">
                  <th class="py-1.5 pr-3">Session ID</th>
                  <th class="py-1.5 pr-3">剩余暂停</th>
                  <th class="py-1.5 pr-3">混淆检查</th>
                  <th class="py-1.5 pr-3">日志文件</th>
                  <th class="py-1.5 text-right">操作</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-[var(--border-subtle)]">
                <tr v-for="s in sgData?.paused_sessions ?? []" :key="s.session_id">
                  <td class="py-2 pr-3 font-mono text-[var(--text-primary)]">{{ s.session_id }}</td>
                  <td class="py-2 pr-3">
                    <Badge variant="warning">{{ s.remaining_seconds }}s</Badge>
                  </td>
                  <td class="py-2 pr-3">
                    <Badge :variant="s.suspected_unobfuscated ? 'danger' : 'success'">
                      {{ s.suspected_unobfuscated ? '疑似存在未混淆工具' : '未检出未混淆' }}
                    </Badge>
                  </td>
                  <td class="py-2 pr-3">
                    <button
                      v-if="s.log_file"
                      type="button"
                      class="font-mono text-[11px] text-[var(--accent)] hover:underline"
                      @click="openLogFile(s.log_file)"
                    >
                      {{ s.log_file }}
                    </button>
                    <span v-else class="text-[var(--text-muted)]">-</span>
                  </td>
                  <td class="py-2 text-right">
                    <Button variant="secondary" size="sm" @click="resumePausedSession(s.session_id)">
                      立即解封
                    </Button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <!-- 最近落盘的 429 请求体 .log 文件 -->
        <div class="rounded-[var(--radius-md)] border border-[var(--border-subtle)] p-3">
          <div class="mb-2 text-xs font-semibold text-[var(--text-primary)]">
            最近落盘的 429 请求体审计日志文件 ({{ sgData?.log_files?.length ?? 0 }})
          </div>
          <div v-if="(sgData?.log_files?.length ?? 0) === 0" class="py-2 text-center text-xs text-[var(--text-muted)]">
            尚无非额度 429 请求体日志文件
          </div>
          <div v-else class="flex flex-wrap gap-2">
            <button
              v-for="lf in sgData?.log_files ?? []"
              :key="lf.filename"
              type="button"
              class="inline-flex items-center gap-1.5 rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-subtle)] px-2.5 py-1 font-mono text-[11px] text-[var(--text-primary)] hover:border-[var(--accent)]"
              @click="openLogFile(lf.filename)"
            >
              <Icon name="fileText" :size="12" />
              <span>{{ lf.filename }}</span>
            </button>
          </div>
        </div>
      </div>
    </Card>

    <Modal
      :open="logModalOpen"
      :title="`请求体审计日志 · ${logModalFilename}`"
      size="lg"
      @close="logModalOpen = false"
    >
      <div v-if="logModalLoading" class="py-8 text-center text-xs text-[var(--text-muted)]">
        正在加载日志文件...
      </div>
      <CodeBlock v-else :code="logModalContent" lang="log" max-height="460px" />
    </Modal>

    <ConfirmDialog
      :open="rotateConfirmOpen"
      title="确认轮换混淆盐值？"
      message="轮换盐值后，Hash 模式及超长工具截断生成的短标识符将立即变化；对新建会话无影响，建议在低峰期或无长对话 KV-Cache 依赖时操作。"
      confirm-text="立即轮换并生效"
      tone="danger"
      :loading="rotatingSalt"
      @update:open="rotateConfirmOpen = $event"
      @confirm="confirmRotateSalt"
    />
  </div>
</template>
