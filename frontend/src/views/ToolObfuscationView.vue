<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'

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
  excludeTools: [] as string[],
  rewriteToolRefs: false,
})

const newExcludeInput = ref('')
const VALID_TARGET_RE = /^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$/

const COMMON_SAFE_ALLOWLIST = [
  'web_search',
  'get_weather',
  'ping_health',
  'calculator',
  'fetch_docs',
]

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
  hash: { label: 'Hash 确定性短名', desc: 'prefix + sha256(salt:orig)[:10]' },
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
  form.excludeTools = Array.from(
    new Set((cfg.exclude_tools ?? []).map((s) => s.trim()).filter(Boolean)),
  )
  form.rewriteToolRefs = Boolean(cfg.rewrite_tool_refs)
}

const customMapValidationError = computed(() => {
  const seenTargets = new Set<string>()
  for (const row of form.customRows) {
    const src = row.source.trim()
    const tgt = row.target.trim()
    if (!src && !tgt) continue
    if (!src || !tgt) return '自定义映射的「原工具名」与「目标名」必须成对填写'
    if (!VALID_TARGET_RE.test(tgt)) {
      return `目标工具名「${tgt}」不合法，须符合 ^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`
    }
    const lower = tgt.toLowerCase()
    if (seenTargets.has(lower)) {
      return `目标工具名「${tgt}」存在重复，会破坏一对一双射还原`
    }
    seenTargets.add(lower)
  }
  return ''
})

function buildConfigPayload(): ToolObfuscationConfig {
  const custom_map: Record<string, string> = {}
  for (const row of form.customRows) {
    const src = row.source.trim()
    const tgt = row.target.trim()
    if (src && tgt && VALID_TARGET_RE.test(tgt)) {
      custom_map[src] = tgt
    }
  }

  return {
    enabled: form.enabled,
    mode: form.mode,
    prefix: form.prefix.trim() || 'ess_t_',
    salt: form.salt.trim(),
    scope: form.scope,
    platforms: [...form.platforms],
    custom_map,
    exclude_tools: [...form.excludeTools],
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
  if (customMapValidationError.value) {
    toast.warning('请先修正自定义映射配置', customMapValidationError.value)
    return
  }
  saving.value = true
  try {
    const updated = await toolObfuscationApi.update(buildConfigPayload())
    hydrateForm(updated)
    syncGroupObfDrafts(updated.groups)
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
    toast.success('随机盐值已轮换', '确定性哈希短名指纹已立即刷新生效')
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

function addCustomRow(source = '', target = ''): void {
  form.customRows.push({ source, target })
}

function removeCustomRow(idx: number): void {
  form.customRows.splice(idx, 1)
}

function addExcludeChip(raw?: string): void {
  const input = (raw ?? newExcludeInput.value).trim()
  if (!input) return
  const tokens = input
    .split(/[\n,;\s]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  const existingLower = new Set(form.excludeTools.map((t) => t.toLowerCase()))
  for (const tok of tokens) {
    if (!existingLower.has(tok.toLowerCase())) {
      form.excludeTools.push(tok)
      existingLower.add(tok.toLowerCase())
    }
  }
  if (raw === undefined) {
    newExcludeInput.value = ''
  }
}

function removeExcludeChip(idx: number): void {
  form.excludeTools.splice(idx, 1)
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

const activeExcludeCount = computed(() => form.excludeTools.length)

// ---- 各分组独立工具名混淆策略矩阵（修复 Badge tone、未定义 CSS 变量及 Hash/Alias 前缀禁用 bug） ----
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
    if (previewGroupId.value === String(g.group_id)) {
      void runPreview()
    }
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
      max_log_files: Math.max(0, Number(sgForm.maxLogFiles) ?? 200),
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

// ---- 双向混淆与还原实时预览台 ----
const previewGroupId = ref('')
const previewGroupOptions = computed(() => [
  { label: '按全局草稿策略预览', value: '' },
  ...(viewData.value?.groups ?? []).map((g) => ({
    label: `分组 #${g.group_id} ${g.group_name} (${g.override?.mode || 'inherit'})`,
    value: String(g.group_id),
  })),
])
const previewMode = ref<'list' | 'json'>('list')
const previewModeTabs = [
  { value: 'list', label: '工具名列表模式' },
  { value: 'json', label: '完整三协议报文模式' },
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
    'apply_patch',
    'dsh_edit',
    'mcp__github__create_pull_request_review_comment_thread',
    'custom_user_defined_tool_x9',
    'web_search',
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
      messages: [{ role: 'user', content: 'Please check `Edit` and run pwd via `Bash`.' }],
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
let previewDebounceTimer: ReturnType<typeof setTimeout> | null = null

function scheduleAutoPreview(): void {
  if (loading.value) return
  if (previewDebounceTimer) clearTimeout(previewDebounceTimer)
  previewDebounceTimer = setTimeout(() => {
    void runPreview()
  }, 320)
}

watch(
  () => [
    form.enabled,
    form.mode,
    form.prefix,
    form.salt,
    form.scope,
    form.rewriteToolRefs,
    form.excludeTools.length,
    activeCustomCount.value,
    previewGroupId.value,
  ],
  () => {
    scheduleAutoPreview()
  },
)

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

onBeforeUnmount(() => {
  if (previewDebounceTimer) clearTimeout(previewDebounceTimer)
})

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
        label="工具名双射混淆状态"
        :value="form.enabled ? '已启用' : '已停用'"
        :hint="form.enabled ? '出站确定性伪装 · 回流双射无损还原' : '当前原样透传所有工具名'"
        icon="lock"
        :tone="form.enabled ? 'success' : 'neutral'"
      />
      <MetricTile
        label="当前生效算法"
        :value="modeBadgeMap[form.mode]?.label ?? form.mode"
        :hint="`${modeBadgeMap[form.mode]?.desc ?? ''} · ${form.scope === 'all' ? '全部工具' : '仅敏感工具'}`"
        icon="sparkles"
        tone="accent"
      />
      <MetricTile
        label="覆盖上游平台"
        :value="`${form.platforms.length} / 4`"
        hint="WorkBuddy · AntiGravity · Command Code · OpenCode"
        icon="layers"
        tone="info"
      />
      <MetricTile
        label="自定义映射 / 白名单透传"
        :value="`${activeCustomCount} 映射 / ${activeExcludeCount} 透传`"
        :hint="`内置 ${viewData?.stats?.builtin_aliases_count ?? 0} 条中性别名 · 任意随机工具自动兼容`"
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

    <!-- 确定性双射与任意随机/自定义工具透传原理说明横幅 -->
    <div class="grid gap-3 rounded-panel border border-line bg-surface p-3.5 shadow-panel lg:grid-cols-3">
      <div class="flex items-start gap-2.5">
        <span class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded border border-accent/30 bg-accent/10 text-accent">
          <Icon name="lock" :size="13" />
        </span>
        <div>
          <div class="text-xs font-semibold text-fg">确定性 KV-Cache 友好混淆</div>
          <p class="mt-0.5 text-2xs leading-relaxed text-muted">
            相同的 <code class="font-mono text-fg">(salt, mode, prefix, tool_name)</code> 在多轮对话与跨账号换号重试中恒定生成同一短标识符，保障上游 Prompt Cache 100% 稳定命中。
          </p>
        </div>
      </div>

      <div class="flex items-start gap-2.5 border-t border-line pt-2.5 lg:border-l lg:border-t-0 lg:pl-3.5 lg:pt-0">
        <span class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded border border-success/30 bg-success/10 text-success">
          <Icon name="rotate" :size="13" />
        </span>
        <div>
          <div class="text-xs font-semibold text-fg">任意随机 / 自定义工具 100% 双射还原</div>
          <p class="mt-0.5 text-2xs leading-relaxed text-muted">
            无论是内置工具（<code class="font-mono text-fg">Bash / apply_patch</code>）、超长 MCP 工具或完全随机的用户自定义工具名，单请求独立双射表在流式 SSE 与非流式回包时均精确逆向还原。
          </p>
        </div>
      </div>

      <div class="flex items-start gap-2.5 border-t border-line pt-2.5 lg:border-l lg:border-t-0 lg:pl-3.5 lg:pt-0">
        <span class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded border border-info/30 bg-info/10 text-info">
          <Icon name="shield" :size="13" />
        </span>
        <div>
          <div class="text-xs font-semibold text-fg">命名空间预占防碰撞机制</div>
          <p class="mt-0.5 text-2xs leading-relaxed text-muted">
            白名单透传工具自动预占混淆表槽位，超 64 字符上限或别名冲突时自动附加确定性消歧后缀，杜绝任何双射覆盖风险。
          </p>
        </div>
      </div>
    </div>

    <!-- 核心策略配置 -->
    <Card
      title="核心混淆策略与平台作用域"
      subtitle="控制全局混淆算法、前缀、盐值与反引号工具引用同步改写"
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
          <Field label="混淆模式 (Mode)">
            <Select v-model="form.mode" size="sm" :options="modeOptions" />
          </Field>

          <Field label="作用范围 (Scope)">
            <Select v-model="form.scope" size="sm" :options="scopeOptions" />
          </Field>

          <Field label="中性前缀 (Prefix)" hint="作用于 Hash 短名、Prefix 模式及未知工具回退">
            <Input v-model="form.prefix" size="sm" mono placeholder="ess_t_" />
          </Field>

          <Field label="确定性盐值 (Salt)" hint="轮换盐值可一键刷新全站工具哈希指纹">
            <div class="flex items-center gap-1.5">
              <Input
                v-model="form.salt"
                size="sm"
                mono
                placeholder="自动生成 32 位随机 Hex"
                class="flex-1"
              />
              <Button variant="ghost" size="sm" icon title="复制盐值" @click="copySalt">
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
          </Field>
        </div>

        <!-- 四平台复选栏 + Prompt 反引号重写开关 -->
        <div class="grid grid-cols-1 gap-3 lg:grid-cols-12">
          <div class="space-y-1.5 lg:col-span-8">
            <label class="block text-xs font-medium text-muted">生效上游平台</label>
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-4">
              <button
                v-for="plat in PLATFORMS"
                :key="plat.value"
                type="button"
                class="flex items-center justify-between rounded-md border px-3 py-2 text-left transition"
                :class="
                  form.platforms.includes(plat.value)
                    ? 'border-accent/60 bg-accent/10 text-fg'
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
                      ? 'border-accent bg-accent text-accent-fg'
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
                开启后，对提示词与工具描述中被单反引号 <code class="font-mono text-fg">`ToolName`</code> 包裹的精确工具名同步替换为混淆名，不误伤普通散文。
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

    <!-- 自定义覆盖映射 + 透传白名单标签编辑器 -->
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-12">
      <Card
        class="lg:col-span-7"
        title="自定义固定映射 (Custom Bijective Map)"
        subtitle="优先级高于 Hash / Alias / Prefix；目标名须符合 ^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$ 且一对一唯一"
        body-class="p-4 space-y-3"
      >
        <template #actions>
          <div class="flex items-center gap-1.5">
            <Button
              variant="ghost"
              size="xs"
              @click="addCustomRow('Bash', 'exec_task')"
            >
              + 示例映射
            </Button>
            <Button variant="secondary" size="xs" @click="addCustomRow()">
              <template #leading><Icon name="plus" :size="12" /></template>
              新增映射
            </Button>
          </div>
        </template>

        <p
          v-if="customMapValidationError"
          class="flex items-center gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="13" />
          <span>{{ customMapValidationError }}</span>
        </p>

        <div
          v-if="form.customRows.length === 0"
          class="rounded-md border border-dashed border-line bg-raised/25 px-4 py-6 text-center text-2xs text-subtle"
        >
          暂无自定义固定映射。默认由所选混淆算法自动为全部工具生成确定性映射；点击右上角可固定特定工具名（如 <code class="font-mono text-fg">Bash → exec_task</code>）。
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
              :invalid="Boolean(row.target.trim() && !VALID_TARGET_RE.test(row.target.trim()))"
              placeholder="上游目标名（如 exec_task）"
              class="flex-1"
            />
            <Button
              variant="ghost"
              size="xs"
              icon
              class="hover:bg-danger/10 hover:text-danger"
              title="移除此条"
              @click="removeCustomRow(idx)"
            >
              <Icon name="trash" :size="13" />
            </Button>
          </div>
        </div>
      </Card>

      <Card
        class="lg:col-span-5"
        title="原样透传白名单 (Passthrough Allowlist Chips)"
        subtitle="白名单内工具不作改名原样发往上游，并自动预占双射命名空间以防碰撞"
        body-class="p-4 space-y-3"
      >
        <div class="flex items-center gap-2">
          <Input
            v-model="newExcludeInput"
            size="sm"
            mono
            placeholder="输入工具名回车或逗号批量添加…"
            class="flex-1"
            @keydown.enter.prevent="addExcludeChip()"
          />
          <Button
            variant="secondary"
            size="sm"
            :disabled="!newExcludeInput.trim()"
            @click="addExcludeChip()"
          >
            添加
          </Button>
        </div>

        <!-- 快捷推荐白名单标签 -->
        <div class="flex flex-wrap items-center gap-1 text-2xs">
          <span class="text-subtle">快捷加入无敏感工具：</span>
          <button
            v-for="presetTool in COMMON_SAFE_ALLOWLIST"
            :key="presetTool"
            type="button"
            class="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-[11px] text-muted transition hover:border-accent/50 hover:text-accent"
            @click="addExcludeChip(presetTool)"
          >
            + {{ presetTool }}
          </button>
        </div>

        <!-- 已激活白名单 Chips -->
        <div
          v-if="form.excludeTools.length === 0"
          class="rounded-md border border-dashed border-line bg-raised/25 px-3 py-5 text-center text-2xs text-subtle"
        >
          当前无白名单排除项（大小写不敏感匹配）。所有在作用域内的工具均受混淆保护。
        </div>
        <div v-else class="flex flex-wrap gap-1.5 rounded-md border border-line bg-canvas p-2.5">
          <span
            v-for="(tool, idx) in form.excludeTools"
            :key="tool"
            class="inline-flex items-center gap-1 rounded border border-line-strong bg-surface px-2 py-0.5 font-mono text-xs text-fg"
          >
            <span>{{ tool }}</span>
            <button
              type="button"
              class="rounded p-0.5 text-subtle transition hover:bg-danger/15 hover:text-danger"
              :title="`移除 ${tool}`"
              @click="removeExcludeChip(idx)"
            >
              <Icon name="close" :size="11" />
            </button>
          </span>
        </div>

        <div class="flex items-center justify-between text-2xs text-subtle">
          <span>已配置 <strong class="tnum font-mono text-fg">{{ activeExcludeCount }}</strong> 个透传白名单工具</span>
          <Button
            v-if="form.excludeTools.length > 0"
            variant="ghost"
            size="xs"
            @click="form.excludeTools = []"
          >
            清空全部
          </Button>
        </div>
      </Card>
    </div>

    <!-- 各分组工具混淆独立策略矩阵 -->
    <Card
      title="各分组工具混淆独立策略矩阵 (Per-Group Override)"
      subtitle="每个路由分组可独立覆盖全局混淆算法、专属前缀（Hash / Alias / Prefix 均生效）与作用域，保存后立即热生效"
      body-class="p-0"
    >
      <div
        v-if="(viewData?.groups?.length ?? 0) === 0"
        class="px-4 py-6 text-center text-xs text-subtle"
      >
        暂无分组数据，可前往分组与倍率路由创建分组。
      </div>
      <div v-else class="overflow-x-auto">
        <table class="w-full min-w-[860px] border-collapse text-left">
          <thead class="border-b border-line bg-raised/60">
            <tr>
              <th class="th">路由分组</th>
              <th class="th w-28">平台</th>
              <th class="th w-32">当前生效</th>
              <th class="th w-52">策略模式 (Mode)</th>
              <th class="th w-40">分组前缀 (Prefix)</th>
              <th class="th w-44">作用域 (Scope)</th>
              <th class="th w-40 text-right">操作</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-line">
            <tr v-for="g in viewData?.groups ?? []" :key="g.group_id" class="hover:bg-raised/35">
              <td class="td font-medium text-fg">
                <span class="tnum mr-1 font-mono text-2xs text-subtle">#{{ g.group_id }}</span>
                <span>{{ g.group_name }}</span>
              </td>
              <td class="td">
                <PlatformBadge :platform="g.platform" short />
              </td>
              <td class="td">
                <Badge :tone="g.effective_enabled ? 'success' : 'neutral'" dot mono>
                  {{ g.effective_enabled ? g.effective_mode : 'off' }}
                </Badge>
              </td>
              <td class="td">
                <Select
                  v-if="groupObfDrafts[g.group_id]"
                  v-model="groupObfDrafts[g.group_id]!.mode"
                  size="sm"
                  :options="groupModeOptions"
                />
              </td>
              <td class="td">
                <Input
                  v-if="groupObfDrafts[g.group_id]"
                  v-model="groupObfDrafts[g.group_id]!.prefix"
                  size="sm"
                  mono
                  placeholder="留空继承全局前缀"
                  :disabled="
                    groupObfDrafts[g.group_id]!.mode === 'inherit' ||
                    groupObfDrafts[g.group_id]!.mode === 'off'
                  "
                />
              </td>
              <td class="td">
                <Select
                  v-if="groupObfDrafts[g.group_id]"
                  v-model="groupObfDrafts[g.group_id]!.scope"
                  size="sm"
                  :disabled="groupObfDrafts[g.group_id]!.mode === 'off'"
                  :options="groupScopeOptions"
                />
              </td>
              <td class="td text-right">
                <div class="inline-flex items-center gap-1.5">
                  <Button variant="ghost" size="xs" @click="previewThisGroup(g.group_id)">
                    <template #leading><Icon name="zap" :size="12" /></template>
                    预览
                  </Button>
                  <Button
                    variant="primary"
                    size="xs"
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

    <!-- 双向混淆与还原实时映射测试台 -->
    <Card
      title="实时工具名映射与双射还原演练台 (Live Bijective Mapping Preview)"
      subtitle="按当前页面草稿实时计算（修改参数自动刷新，无需先保存）：验证「原工具名 → 上游 Wire 混淆名 → 下游无损还原名」"
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
          <label class="text-xs font-medium text-muted">
            输入待测工具名（支持内置工具、MCP 超长名或任意随机自定义工具名）
          </label>
          <div class="flex items-center gap-1.5 text-2xs">
            <span class="text-subtle">切换协议报文演练：</span>
            <Button variant="ghost" size="xs" @click="loadSamplePayload('openai')">OpenAI</Button>
            <Button variant="ghost" size="xs" @click="loadSamplePayload('anthropic')">Anthropic</Button>
            <Button variant="ghost" size="xs" @click="loadSamplePayload('responses')">Responses</Button>
          </div>
        </div>
        <Textarea
          v-model="toolNamesInput"
          :rows="4"
          mono
          placeholder="Bash&#10;Read&#10;apply_patch&#10;my_custom_tool_v1"
          @input="scheduleAutoPreview"
        />
      </div>

      <div v-else class="space-y-2.5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="w-60">
            <Select v-model="previewProtocol" size="sm" :options="previewProtocolOptions" />
          </div>
          <div class="flex items-center gap-1.5">
            <Button variant="secondary" size="xs" @click="loadSamplePayload('openai')">OpenAI 示例</Button>
            <Button variant="secondary" size="xs" @click="loadSamplePayload('anthropic')">Anthropic 示例</Button>
            <Button variant="secondary" size="xs" @click="loadSamplePayload('responses')">Responses 示例</Button>
          </div>
        </div>
        <Textarea
          v-model="jsonPayloadInput"
          :rows="7"
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
            {{ previewResult.restored_verified ? '100% 双射无损还原校验通过' : '存在双射校验异常' }}
          </Badge>
          <Badge tone="accent">
            已混淆 {{ previewResult.changed_count }} / {{ previewResult.total_count }} 个工具
          </Badge>
          <Badge tone="info" mono>
            mode={{ previewResult.mode }} · scope={{ previewResult.scope }}
          </Badge>
        </div>

        <div class="overflow-x-auto rounded-md border border-line">
          <table class="min-w-full divide-y divide-line text-left text-xs">
            <thead class="bg-raised/70">
              <tr>
                <th class="th">客户端原始工具名 (Downstream)</th>
                <th class="th">发往上游混淆名 (Upstream Wire)</th>
                <th class="th">SSE / 响应回流还原名 (Restored)</th>
                <th class="th w-28">映射状态</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-line bg-surface font-mono text-xs">
              <tr v-for="m in previewResult.mappings" :key="m.original" class="hover:bg-raised/35">
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
                    {{ m.changed ? '双射混淆' : '白名单透传' }}
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
          title="发往上游的标准化混淆请求 JSON（tools / tool_choice / 历史 tool_calls 均已同步改写）"
          language="json"
          :code="previewResult.obfuscated_payload"
          max-height="20rem"
        />
      </div>
    </Card>

    <!-- 内置语义别名参考表（可折叠） -->
    <Card
      title="内置语义别名词表与敏感工具识别目录"
      :subtitle="`Alias 模式下自动将 ${viewData?.stats?.builtin_aliases_count ?? 0} 个常用 Coding Agent 工具映射为中性操作名；未命中词表的任意随机工具自动回退前缀+摘要`"
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
        覆盖 Claude Code / Codex CLI / DSH / OpenCode / Cline 等常见内置工具（<code class="font-mono text-fg">Bash / Read / Edit / Grep / apply_patch / exec_command / dsh_* / mcp__*</code>）。点击右侧按钮可检索完整对照表。
      </div>

      <div v-else class="space-y-3">
        <div class="w-full sm:w-64">
          <Input v-model="aliasSearch" size="sm" placeholder="筛选原名或中性别名…" />
        </div>
        <div class="scroll-thin grid max-h-64 grid-cols-1 gap-2 overflow-y-auto pr-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
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

    <!-- 非账号额度不足 429 会话暂停保护与请求体 Log 审计中心（修复全部深浅双色语义令牌与组件属性） -->
    <Card
      title="非账号额度不足 429 会话暂停保护与请求体 Log 审计中心"
      subtitle="上游返回 HTTP 429 但非账号额度耗尽（如系统限流或疑似暴露原始工具名）时，自动暂停该会话调度并落盘完整请求体 .log 供审计排查"
      body-class="p-4 space-y-4"
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

      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div class="rounded-md border border-line bg-raised/45 p-3">
          <Switch
            v-model="sgForm.enabled"
            size="sm"
            label="启用非额度 429 暂停"
            description="识别风控类 429 自动冻结当前会话"
          />
        </div>
        <div class="rounded-md border border-line bg-raised/45 p-3">
          <Switch
            v-model="sgForm.logEnabled"
            size="sm"
            label="落盘完整请求体 .log"
            description="记录入站与混淆后出站报文便于排障"
          />
        </div>
        <div class="rounded-md border border-line bg-raised/45 p-3">
          <Switch
            v-model="sgForm.abortCurrentRetry"
            size="sm"
            label="命中即中止换号重试"
            description="避免问题报文继续连坐污染池内其他账号"
          />
        </div>
        <div class="rounded-md border border-line bg-raised/45 p-3">
          <Switch
            v-model="sgForm.fallbackFingerprint"
            size="sm"
            label="无 session_id 时回退指纹"
            description="按请求摘要定位同一客户端对话流"
          />
        </div>
      </div>

      <div class="grid gap-3 md:grid-cols-3">
        <Field label="暂停时长（秒，默认 300 = 5 分钟）">
          <Input
            :model-value="String(sgForm.pauseSeconds)"
            type="number"
            size="sm"
            placeholder="300"
            @update:model-value="sgForm.pauseSeconds = Number($event) || 0"
          />
        </Field>
        <Field label="最大保留 .log 文件数">
          <Input
            :model-value="String(sgForm.maxLogFiles)"
            type="number"
            size="sm"
            placeholder="200"
            @update:model-value="sgForm.maxLogFiles = Number($event) || 0"
          />
        </Field>
        <Field label=".log 存储目录">
          <Input v-model="sgForm.logDir" size="sm" mono placeholder="./data/logs/session_429" />
        </Field>
      </div>

      <Field
        label="账号额度不足豁免关键词"
        hint="逗号或换行分隔；命中此类正常配额耗尽关键词的 429 仍走常规自动换号，不暂停会话"
      >
        <Textarea
          v-model="sgForm.quotaKeywordsText"
          :rows="2"
          mono
          placeholder="insufficient_quota, quota exceeded, credit balance is too low..."
        />
      </Field>

      <!-- 当前暂停中的会话列表 -->
      <div class="rounded-md border border-line bg-raised/30 p-3 space-y-2">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-xs font-semibold text-fg">
            当前暂停调度的会话 ({{ sgData?.paused_sessions?.length ?? 0 }})
          </span>
          <span class="text-2xs text-subtle">
            暂停期间同会话请求直接返回保护提示，不消耗上游账号池
          </span>
        </div>

        <div
          v-if="(sgData?.paused_sessions?.length ?? 0) === 0"
          class="py-3 text-center text-2xs text-subtle"
        >
          暂无被暂停的会话 ID
        </div>
        <div v-else class="overflow-x-auto rounded border border-line bg-surface">
          <table class="w-full border-collapse text-left text-xs">
            <thead class="border-b border-line bg-raised/60">
              <tr>
                <th class="th">Session ID</th>
                <th class="th">剩余暂停</th>
                <th class="th">混淆审计</th>
                <th class="th">审计日志文件</th>
                <th class="th text-right">操作</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-line">
              <tr v-for="s in sgData?.paused_sessions ?? []" :key="s.session_id">
                <td class="td font-mono text-xs text-fg">{{ s.session_id }}</td>
                <td class="td">
                  <Badge tone="warning" mono>{{ s.remaining_seconds }}s</Badge>
                </td>
                <td class="td">
                  <Badge :tone="s.suspected_unobfuscated ? 'danger' : 'success'" dot>
                    {{ s.suspected_unobfuscated ? '疑似存在未混淆工具' : '未检出未混淆工具' }}
                  </Badge>
                </td>
                <td class="td">
                  <button
                    v-if="s.log_file"
                    type="button"
                    class="font-mono text-2xs text-accent hover:underline"
                    @click="openLogFile(s.log_file)"
                  >
                    {{ s.log_file }}
                  </button>
                  <span v-else class="text-subtle">-</span>
                </td>
                <td class="td text-right">
                  <Button variant="secondary" size="xs" @click="resumePausedSession(s.session_id)">
                    立即解封
                  </Button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 最近落盘的 429 请求体 .log 文件 -->
      <div class="rounded-md border border-line bg-raised/30 p-3 space-y-2">
        <div class="text-xs font-semibold text-fg">
          最近落盘的 429 请求体审计日志文件 ({{ sgData?.log_files?.length ?? 0 }})
        </div>
        <div
          v-if="(sgData?.log_files?.length ?? 0) === 0"
          class="py-2 text-center text-2xs text-subtle"
        >
          尚无非额度 429 请求体审计日志
        </div>
        <div v-else class="flex flex-wrap gap-2">
          <button
            v-for="lf in sgData?.log_files ?? []"
            :key="lf.filename"
            type="button"
            class="inline-flex items-center gap-1.5 rounded-md border border-line bg-surface px-2.5 py-1 font-mono text-2xs text-fg transition hover:border-accent/60 hover:text-accent"
            @click="openLogFile(lf.filename)"
          >
            <Icon name="fileText" :size="12" />
            <span>{{ lf.filename }}</span>
          </button>
        </div>
      </div>
    </Card>

    <Modal
      :open="logModalOpen"
      :title="`请求体审计日志 · ${logModalFilename}`"
      size="xl"
      @update:open="logModalOpen = $event"
    >
      <div v-if="logModalLoading" class="py-8 text-center text-xs text-subtle">
        正在加载审计日志文件…
      </div>
      <CodeBlock v-else :code="logModalContent" language="log" max-height="28rem" />
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
