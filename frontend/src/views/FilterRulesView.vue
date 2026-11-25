<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { filterRulesApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type {
  FilterPreviewMode,
  FilterPreviewProtocol,
  FilterRule,
  FilterRuleHit,
  FilterRuleKind,
  FilterRulePreviewResult,
} from '@/api/types'
import FilterRuleFormModal from '@/components/filter/FilterRuleFormModal.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'

const toast = useToastStore()

// ---- 规则库状态 ----
const items = ref<FilterRule[]>([])
const stats = ref({ enabled: 0, prune: 0, rewrite: 0, replace: 0 })
const loading = ref(true)
const error = ref('')

type TabKind = 'all' | FilterRuleKind
const activeTab = ref<string>('all')
const keyword = ref('')

const formOpen = ref(false)
const editing = ref<FilterRule | null>(null)
const togglingId = ref<number | null>(null)
const deleteTarget = ref<FilterRule | null>(null)
const deleting = ref(false)
const resetOpen = ref(false)
const resetting = ref(false)

// ---- 实时清洗测试台状态 ----
const previewInput = ref('')
const previewMode = ref<FilterPreviewMode>('auto')
const previewProtocol = ref<FilterPreviewProtocol>('anthropic')
const previewLoading = ref(false)
const previewError = ref('')
const previewResult = ref<FilterRulePreviewResult | null>(null)
const viewMode = ref<string>('split')

const modeOptions = [
  { label: '自动识别 (auto)', value: 'auto' },
  { label: '完整请求 JSON (json)', value: 'json' },
  { label: '系统提示词 (system)', value: 'system' },
  { label: '工具描述 (tool)', value: 'tool' },
  { label: '用户 Harness 块 (user_harness)', value: 'user_harness' },
]

const protocolOptions = [
  { label: 'Anthropic (/v1/messages)', value: 'anthropic' },
  { label: 'Responses (/v1/responses)', value: 'responses' },
  { label: 'OpenAI (/v1/chat/completions)', value: 'openai' },
]

const tabItems = computed(() => [
  { value: 'all', label: '全部规则', badge: items.value.length },
  { value: 'prune', label: 'Prune 删减', badge: stats.value.prune },
  { value: 'rewrite', label: 'Rewrite 重写', badge: stats.value.rewrite },
  { value: 'replace', label: 'Replace 同义替换', badge: stats.value.replace },
])

const viewModeTabs = [
  { value: 'split', label: '左右分栏对比' },
  { value: 'diff', label: '逐行 Diff 高亮' },
]

const filteredItems = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return items.value.filter((rule) => {
    if (activeTab.value !== 'all' && rule.kind !== (activeTab.value as TabKind)) return false
    if (!q) return true
    return (
      rule.name.toLowerCase().includes(q) ||
      rule.pattern.toLowerCase().includes(q) ||
      rule.replacement.toLowerCase().includes(q) ||
      rule.description.toLowerCase().includes(q)
    )
  })
})

function kindBadge(kind: string): {
  tone: 'warning' | 'info' | 'success' | 'neutral'
  label: string
} {
  if (kind === 'prune') return { tone: 'warning', label: 'Prune 删减' }
  if (kind === 'rewrite') return { tone: 'info', label: 'Rewrite 重写' }
  if (kind === 'replace') return { tone: 'success', label: 'Replace 替换' }
  return { tone: 'neutral', label: kind }
}

function scopeLabel(scope: string): string {
  const parts = (scope || 'all_injected')
    .split(',')
    .map((p) => p.trim())
    .filter(Boolean)
  if (parts.length === 0) return '全部注入上下文'
  return parts
    .map((part) => {
      switch (part) {
        case 'all_injected':
          return '全部注入上下文'
        case 'system':
          return '仅 System'
        case 'tool':
          return '仅 Tools'
        case 'user':
        case 'user_harness':
          return '用户 Harness 块'
        default:
          return part
      }
    })
    .join(' + ')
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await filterRulesApi.list()
    items.value = res.items ?? []
    stats.value = res.stats ?? {
      enabled: items.value.filter((r) => r.enabled).length,
      prune: items.value.filter((r) => r.kind === 'prune').length,
      rewrite: items.value.filter((r) => r.kind === 'rewrite').length,
      replace: items.value.filter((r) => r.kind === 'replace').length,
    }
  } catch (err) {
    error.value = toMessage(err, '加载风控规则失败')
    items.value = []
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})

function openCreate(): void {
  editing.value = null
  formOpen.value = true
}

function openEdit(rule: FilterRule): void {
  editing.value = rule
  formOpen.value = true
}

async function toggleEnabled(rule: FilterRule): Promise<void> {
  if (togglingId.value !== null) return
  togglingId.value = rule.id
  const next = !rule.enabled
  try {
    const updated = await filterRulesApi.update(rule.id, { enabled: next })
    const idx = items.value.findIndex((item) => item.id === rule.id)
    if (idx >= 0) items.value[idx] = updated
    stats.value.enabled = items.value.filter((item) => item.enabled).length
    toast.success(next ? '已启用规则' : '已停用规则', rule.name)
  } catch (err) {
    toast.error('切换规则状态失败', toMessage(err))
  } finally {
    togglingId.value = null
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  try {
    await filterRulesApi.remove(target.id)
    toast.success('规则已删除', target.name)
    deleteTarget.value = null
    await load()
  } catch (err) {
    toast.error('删除规则失败', toMessage(err))
  } finally {
    deleting.value = false
  }
}

async function confirmReset(): Promise<void> {
  resetting.value = true
  try {
    const res = await filterRulesApi.reset()
    toast.success('已恢复内置默认规则库', `共 ${res.total} 条内置规则`)
    resetOpen.value = false
    await load()
  } catch (err) {
    toast.error('恢复默认规则失败', toMessage(err))
  } finally {
    resetting.value = false
  }
}

// ---- 实时清洗测试台 ----

const SAMPLE_CLAUDE_CODE = JSON.stringify(
  {
    model: 'claude-sonnet-4-6',
    system: [
      {
        type: 'text',
        text:
          "x-anthropic-billing-header: cc_version=2.1.4; cc_entrypoint=cli; cch=9f8a12;\nYou are Claude Code, Anthropic's official CLI for Claude.\nTo give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues\nDo not generate denial-of-service or exploit code (ref 11128).",
      },
    ],
    tools: [
      {
        name: 'bash',
        description:
          'Execute shell commands in sandboxed environment (you will usually use this for PRs). Supports kill on hung jobs.',
        input_schema: {
          type: 'object',
          properties: {
            command: { type: 'string', description: 'Shell command to execute' },
          },
        },
      },
    ],
    messages: [
      {
        role: 'user',
        content:
          '<system-reminder>\nNote: Claude Code sandboxed runtime blocks destructive DoS attacks.\n</system-reminder>\n\nPlease help me refactor internal/relay/service.go cleanly.',
      },
    ],
  },
  null,
  2,
)

const SAMPLE_CODEX_CLI = JSON.stringify(
  {
    model: 'gpt-5.3-codex',
    instructions:
      'You are a coding agent running in the Codex CLI, a terminal-based coding assistant. Refuse malware, backdoor, and SQL injection generation.',
    tools: [
      {
        type: 'function',
        name: 'shell',
        description: 'Execute command in unsandboxed or sandboxed mode; avoid privilege escalation.',
      },
    ],
    input: [
      {
        role: 'developer',
        content: '# AGENTS.md instructions\nCheck codebase for vulnerabilities and brute force vectors.',
      },
      {
        role: 'user',
        content: 'List all HTTP routes defined in router.go.',
      },
    ],
  },
  null,
  2,
)

const SAMPLE_PURE_USER = JSON.stringify(
  {
    model: 'claude-sonnet-4-6',
    messages: [
      {
        role: 'user',
        content:
          'Please explain how DoS protection, SQL injection sanitization, exploit mitigation, and kill_process() work in Linux.',
      },
    ],
  },
  null,
  2,
)

function loadSample(kind: 'claude' | 'codex' | 'user'): void {
  if (kind === 'claude') {
    previewInput.value = SAMPLE_CLAUDE_CODE
    previewProtocol.value = 'anthropic'
    previewMode.value = 'json'
  } else if (kind === 'codex') {
    previewInput.value = SAMPLE_CODEX_CLI
    previewProtocol.value = 'responses'
    previewMode.value = 'json'
  } else {
    previewInput.value = SAMPLE_PURE_USER
    previewProtocol.value = 'openai'
    previewMode.value = 'json'
  }
  void runPreview()
}

async function runPreview(): Promise<void> {
  if (!previewInput.value.trim() || previewLoading.value) return
  previewLoading.value = true
  previewError.value = ''
  try {
    previewResult.value = await filterRulesApi.preview({
      input: previewInput.value,
      mode: previewMode.value,
      protocol: previewProtocol.value,
    })
  } catch (err) {
    previewError.value = toMessage(err, '执行清洗预览失败')
  } finally {
    previewLoading.value = false
  }
}

const hasZeroWidthInOutput = computed(() =>
  previewResult.value ? previewResult.value.output.includes('\u200b') : false,
)

const previewHits = computed<FilterRuleHit[]>(
  () => previewResult.value?.hits ?? previewResult.value?.stats?.hits ?? [],
)

interface DiffLine {
  type: 'same' | 'del' | 'add'
  text: string
}

const diffLines = computed<DiffLine[]>(() => {
  const res = previewResult.value
  if (!res) return []
  const a = res.input.split('\n')
  const b = res.output.split('\n')
  if (a.length * b.length > 40_000) {
    return [
      ...a.map((text) => ({ type: 'del' as const, text })),
      ...b.map((text) => ({ type: 'add' as const, text })),
    ]
  }
  const m = a.length
  const n = b.length
  const dp: number[][] = Array.from({ length: m + 1 }, () => new Array<number>(n + 1).fill(0))
  for (let i = m - 1; i >= 0; i -= 1) {
    for (let j = n - 1; j >= 0; j -= 1) {
      dp[i]![j] =
        a[i] === b[j]
          ? (dp[i + 1]![j + 1] ?? 0) + 1
          : Math.max(dp[i + 1]![j] ?? 0, dp[i]![j + 1] ?? 0)
    }
  }
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < m && j < n) {
    if (a[i] === b[j]) {
      out.push({ type: 'same', text: a[i]! })
      i += 1
      j += 1
    } else if ((dp[i + 1]![j] ?? 0) >= (dp[i]![j + 1] ?? 0)) {
      out.push({ type: 'del', text: a[i]! })
      i += 1
    } else {
      out.push({ type: 'add', text: b[j]! })
      j += 1
    }
  }
  while (i < m) {
    out.push({ type: 'del', text: a[i]! })
    i += 1
  }
  while (j < n) {
    out.push({ type: 'add', text: b[j]! })
    j += 1
  }
  return out
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部指标磁贴 -->
    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <MetricTile
        label="已启用 / 词库总规则"
        :value="`${stats.enabled} / ${items.length}`"
        hint="全入口强制生效 · 绝不修改纯用户提问"
        icon="shield"
        tone="accent"
      />
      <MetricTile
        label="Prune 物理删减规则"
        :value="stats.prune"
        hint="剥离计费请求头与冗余客户端签名"
        icon="trash"
        tone="warning"
      />
      <MetricTile
        label="Rewrite 模板重写规则"
        :value="stats.rewrite"
        hint="客户端身份平滑改写为通用工程语境"
        icon="edit"
        tone="info"
      />
      <MetricTile
        label="Replace 安全同义替换"
        :value="stats.replace"
        hint="零 \\u200b 零宽字符 · 规避上游误报拦截"
        icon="checkCircle"
        tone="success"
      />
    </div>

    <!-- 规则列表工具条 -->
    <div class="flex flex-wrap items-center gap-2.5">
      <SegmentedTabs v-model="activeTab" :items="tabItems" />

      <div class="w-full min-w-[12rem] sm:w-64">
        <Input v-model="keyword" size="sm" placeholder="搜索规则名 / 模式 / 替换词…" />
      </div>

      <div class="ml-auto flex flex-wrap items-center gap-2">
        <Button variant="secondary" size="sm" :loading="loading" @click="load">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新
        </Button>
        <Button variant="secondary" size="sm" @click="resetOpen = true">
          <template #leading><Icon name="rotate" :size="13" /></template>
          恢复内置默认规则
        </Button>
        <Button variant="primary" size="sm" @click="openCreate">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建规则
        </Button>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="13" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="load">重试</Button>
    </p>

    <!-- 规则表格 -->
    <DataTable
      :columns="8"
      :loading="loading"
      :empty="filteredItems.length === 0"
      :skeleton-rows="6"
      min-width="1160px"
      hoverable
    >
      <template #head>
        <th class="th w-16">启用</th>
        <th class="th w-16 text-right">优先级</th>
        <th class="th w-32">类型</th>
        <th class="th">规则名称</th>
        <th class="th">匹配模式 (Pattern)</th>
        <th class="th">替换为 (Replacement)</th>
        <th class="th w-36">作用域 / 标志</th>
        <th class="th w-24 text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="shield"
          :title="keyword.trim() || activeTab !== 'all' ? '没有匹配的风控规则' : '规则库为空'"
          :description="
            keyword.trim() || activeTab !== 'all'
              ? '清空搜索词或切换到「全部规则」标签。'
              : '可新建自定义规则，或点击「恢复内置默认规则」载入预置词库。'
          "
        >
          <Button
            v-if="keyword.trim() || activeTab !== 'all'"
            variant="secondary"
            size="sm"
            @click="
              () => {
                keyword = ''
                activeTab = 'all'
              }
            "
          >
            重置筛选
          </Button>
          <Button v-else variant="primary" size="sm" @click="resetOpen = true">
            恢复内置默认规则
          </Button>
        </EmptyState>
      </template>

      <template #body>
        <tr
          v-for="rule in filteredItems"
          :key="rule.id"
          :class="!rule.enabled ? 'opacity-60' : ''"
        >
          <td class="td">
            <Switch
              size="sm"
              :model-value="rule.enabled"
              :disabled="togglingId === rule.id"
              @update:model-value="toggleEnabled(rule)"
            />
          </td>

          <td class="td tnum text-right font-mono text-muted">{{ rule.priority }}</td>

          <td class="td">
            <Badge :tone="kindBadge(rule.kind).tone">{{ kindBadge(rule.kind).label }}</Badge>
          </td>

          <td class="td max-w-[14rem]">
            <div class="flex flex-col gap-0.5">
              <div class="flex items-center gap-1.5">
                <span class="truncate font-medium text-fg">{{ rule.name }}</span>
                <Badge v-if="rule.is_builtin" tone="neutral">内置</Badge>
              </div>
              <span
                v-if="rule.description"
                class="truncate text-2xs text-subtle"
                :title="rule.description"
              >
                {{ rule.description }}
              </span>
            </div>
          </td>

          <td class="td max-w-[18rem]">
            <code
              class="block truncate rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-fg"
              :title="rule.pattern"
            >
              {{ rule.pattern }}
            </code>
          </td>

          <td class="td max-w-[16rem]">
            <Badge v-if="!rule.replacement" tone="neutral">（直接删空）</Badge>
            <code
              v-else
              class="block truncate rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-success"
              :title="rule.replacement"
            >
              {{ rule.replacement }}
            </code>
          </td>

          <td class="td">
            <div class="flex flex-col gap-1">
              <span class="text-2xs text-muted">{{ scopeLabel(rule.scope) }}</span>
              <div class="flex flex-wrap gap-1">
                <Badge v-if="rule.is_regex" tone="info">正则</Badge>
                <Badge v-if="rule.case_sensitive" tone="neutral">区分大小写</Badge>
                <Badge v-if="rule.word_boundary && !rule.is_regex" tone="neutral">词边界</Badge>
              </div>
            </div>
          </td>

          <td class="td">
            <div class="flex items-center justify-end gap-1">
              <Button variant="ghost" size="xs" icon title="编辑规则" @click="openEdit(rule)">
                <Icon name="edit" :size="13" />
              </Button>
              <Button
                variant="ghost"
                size="xs"
                icon
                class="hover:bg-danger/10 hover:text-danger"
                title="删除规则"
                @click="deleteTarget = rule"
              >
                <Icon name="trash" :size="13" />
              </Button>
            </div>
          </td>
        </tr>
      </template>
    </DataTable>

    <!-- 实时清洗测试台 -->
    <Card
      title="实时清洗测试台 (Filter Sanitizer Preview)"
      subtitle="调用 POST /api/filter-rules/preview 检验三协议请求体的脱敏输出与零宽字符合规性"
      body-class="p-4 space-y-4"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-1.5">
          <Button variant="secondary" size="xs" @click="loadSample('claude')">
            Claude Code 样例
          </Button>
          <Button variant="secondary" size="xs" @click="loadSample('codex')">
            Codex CLI 样例
          </Button>
          <Button variant="secondary" size="xs" @click="loadSample('user')">
            纯用户提问样例
          </Button>
        </div>
      </template>

      <div class="flex flex-wrap items-end gap-3">
        <div class="w-52">
          <Field label="检测模式 (mode)">
            <Select v-model="previewMode" size="sm" :options="modeOptions" />
          </Field>
        </div>
        <div class="w-56">
          <Field label="入口协议 (protocol)">
            <Select v-model="previewProtocol" size="sm" :options="protocolOptions" />
          </Field>
        </div>
        <div class="flex items-center gap-2 pb-0.5">
          <Button
            variant="primary"
            size="sm"
            :loading="previewLoading"
            :disabled="!previewInput.trim()"
            @click="runPreview"
          >
            <template #leading><Icon name="zap" :size="13" /></template>
            执行清洗预览
          </Button>
          <SegmentedTabs v-model="viewMode" :items="viewModeTabs" />
        </div>
      </div>

      <p
        v-if="previewError"
        class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
      >
        <Icon name="alertCircle" :size="13" />
        <span>{{ previewError }}</span>
      </p>

      <div v-if="viewMode === 'split'" class="grid gap-3.5 lg:grid-cols-2">
        <Field label="输入文本或请求 JSON Payload">
          <Textarea
            v-model="previewInput"
            :rows="12"
            mono
            placeholder="点击上方样例按钮，或直接粘贴要检测的请求 JSON / 系统提示词 / 工具描述"
          />
        </Field>

        <Field label="清洗后输出">
          <CodeBlock
            v-if="previewResult"
            title="Sanitized Output"
            language="output"
            max-height="17.5rem"
            :code="previewResult.output"
          />
          <div
            v-else
            class="flex h-[17.5rem] items-center justify-center rounded-md border border-dashed border-line bg-raised/30 px-4 text-center text-2xs text-subtle"
          >
            点击「加载样例」或「执行清洗预览」，在此查看脱敏输出与命中规则统计
          </div>
        </Field>
      </div>

      <div v-else class="space-y-3">
        <Field label="输入编辑区">
          <Textarea v-model="previewInput" :rows="6" mono placeholder="粘贴要测试的文本或 JSON" />
        </Field>

        <div v-if="previewResult" class="overflow-hidden rounded-md border border-line bg-surface">
          <div class="border-b border-line bg-raised/70 px-3 py-1.5 text-2xs font-medium text-muted">
            逐行 Diff 差异对比
          </div>
          <div class="scroll-thin max-h-80 overflow-y-auto p-2 font-mono text-2xs leading-relaxed">
            <div
              v-for="(line, idx) in diffLines"
              :key="idx"
              class="whitespace-pre-wrap break-all rounded px-2 py-0.5"
              :class="{
                'bg-danger/12 text-danger': line.type === 'del',
                'bg-success/12 text-success': line.type === 'add',
                'text-muted': line.type === 'same',
              }"
            >
              <span class="mr-2 select-none opacity-60">
                {{ line.type === 'del' ? '-' : line.type === 'add' ? '+' : ' ' }}
              </span>
              <span>{{ line.text }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 命中规则统计 -->
      <div v-if="previewResult" class="panel-inset space-y-3 p-3">
        <div class="flex flex-wrap items-center gap-2">
          <Badge :tone="previewResult.changed ? 'accent' : 'neutral'" dot>
            {{ previewResult.changed ? '内容已脱敏清洗' : '内容原样保留（未命中或纯用户提问）' }}
          </Badge>
          <Badge :tone="hasZeroWidthInOutput ? 'danger' : 'success'" dot>
            {{ hasZeroWidthInOutput ? '警告：检测到 \\u200b 零宽字符' : '已校验：零 \\u200b 零宽字符' }}
          </Badge>
          <Badge tone="neutral" mono>mode={{ previewResult.mode }}</Badge>
          <span class="ml-auto flex flex-wrap items-center gap-3 text-2xs text-muted">
            <span>Prune <strong class="tnum text-warning">{{ previewResult.stats.prune_hits }}</strong></span>
            <span>Rewrite <strong class="tnum text-info">{{ previewResult.stats.rewrite_hits }}</strong></span>
            <span>Replace <strong class="tnum text-success">{{ previewResult.stats.replace_hits }}</strong></span>
          </span>
        </div>

        <div
          v-if="previewHits.length > 0"
          class="scroll-thin max-h-48 overflow-y-auto rounded border border-line bg-surface"
        >
          <table class="w-full border-collapse text-left">
            <thead class="border-b border-line bg-raised/60">
              <tr>
                <th class="th">命中规则</th>
                <th class="th w-28">类型</th>
                <th class="th w-20 text-right">次数</th>
                <th class="th">匹配模式 → 替换结果</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-line">
              <tr v-for="(hit, idx) in previewHits" :key="`${hit.rule_id}-${idx}`">
                <td class="td font-medium text-fg">{{ hit.name }}</td>
                <td class="td">
                  <Badge :tone="kindBadge(hit.kind).tone">{{ hit.kind }}</Badge>
                </td>
                <td class="td tnum text-right text-accent">{{ hit.count }}</td>
                <td class="td max-w-[26rem] truncate font-mono text-2xs text-muted">
                  {{ hit.pattern }} → {{ hit.replacement || '（删空）' }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </Card>

    <FilterRuleFormModal v-model:open="formOpen" :rule="editing" @saved="load" />

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除风控规则"
      :message="`确定删除规则「${deleteTarget?.name ?? ''}」吗？删除后立即从热更新词库移除。`"
      confirm-text="删除"
      tone="danger"
      :loading="deleting"
      @update:open="deleteTarget = null"
      @confirm="confirmDelete"
    />

    <ConfirmDialog
      :open="resetOpen"
      title="恢复内置默认风控词库"
      message="该操作会重新写入内置默认规则并覆盖被修改过的内置项（不会删除你自定义的额外规则）。确定继续吗？"
      confirm-text="恢复默认"
      :loading="resetting"
      @update:open="resetOpen = $event"
      @confirm="confirmReset"
    />
  </div>
</template>
