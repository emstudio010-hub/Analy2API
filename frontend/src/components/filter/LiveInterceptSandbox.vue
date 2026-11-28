<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'

import { toMessage } from '@/api/client'
import { filterRulesApi, previewFilterPipeline } from '@/api/endpoints'
import type {
  AgyPromptMode,
  FilterPreviewMode,
  FilterPreviewProtocol,
  FilterRuleHit,
  FilterRulePreviewResult,
} from '@/api/types'
import {
  INTERCEPT_SANDBOX_PRESETS,
  type InterceptSandboxPreset,
  type NormalizedInterceptPreview,
  getAgyModeMeta,
  normalizeInterceptPreview,
} from '@/components/prompts/catalogData'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'

// 顶层双模式切换：1. AGY 拦截与接续三栏实时沙盒（默认主推）  2. 原始三协议 JSON / Diff 校验台
const sandboxTab = ref<string>('pipeline')
const sandboxTabItems = [
  { value: 'pipeline', label: 'AGY 拦截与接续三栏沙盒', icon: 'zap' as const },
  { value: 'raw_json', label: '原始协议 JSON 与逐行 Diff', icon: 'code' as const },
]

// ---- 模式 1：拦截与接续实时沙盒状态 ----
const activePresetKey = ref<string>('claude_code')
const pipelineForm = reactive({
  agyMode: 'default_intercept_continue' as AgyPromptMode,
  protocol: 'anthropic' as FilterPreviewProtocol,
  hasTools: true,
  systemPrompt: INTERCEPT_SANDBOX_PRESETS[0]!.systemPrompt,
  userMessage: INTERCEPT_SANDBOX_PRESETS[0]!.userMessage,
})

const pipelineLoading = ref(false)
const pipelineError = ref('')
const pipelineResult = ref<NormalizedInterceptPreview | null>(null)
const finalViewTab = ref<string>('final')

const agyModeOptions = [
  { label: '默认提示词 → 拦截 → 接续 (default_intercept_continue)', value: 'default_intercept_continue' },
  { label: '净化底座混合 (blend_sanitized)', value: 'blend_sanitized' },
  { label: '仅原生底座 (native_only)', value: 'native_only' },
  { label: '纯透传下游 (downstream_only)', value: 'downstream_only' },
]

const protocolOptions = [
  { label: 'Anthropic (/v1/messages)', value: 'anthropic' },
  { label: 'Responses (/v1/responses)', value: 'responses' },
  { label: 'OpenAI (/v1/chat/completions)', value: 'openai' },
]

function kindBadge(kind: string): {
  tone: 'warning' | 'info' | 'success' | 'neutral'
  label: string
} {
  if (kind === 'prune') return { tone: 'warning', label: 'Prune' }
  if (kind === 'rewrite') return { tone: 'info', label: 'Rewrite' }
  if (kind === 'replace') return { tone: 'success', label: 'Replace' }
  return { tone: 'neutral', label: kind }
}

function selectPreset(preset: InterceptSandboxPreset): void {
  activePresetKey.value = preset.key
  pipelineForm.agyMode = preset.agyMode
  pipelineForm.protocol = preset.protocol
  pipelineForm.hasTools = preset.hasTools
  pipelineForm.systemPrompt = preset.systemPrompt
  pipelineForm.userMessage = preset.userMessage
  void runPipelinePreview()
}

function loadSafeWordCheck(): void {
  activePresetKey.value = 'safe_words'
  pipelineForm.agyMode = 'default_intercept_continue'
  pipelineForm.protocol = 'openai'
  pipelineForm.hasTools = false
  pipelineForm.systemPrompt =
    'You are a senior backend architect. Focus on clean modular skill boundaries and graceful worker shutdown.'
  pipelineForm.userMessage =
    'Please explain how our plugin skill registry, killed worker recovery state machine, and SQL parameterized queries work.'
  void runPipelinePreview()
}

async function runPipelinePreview(): Promise<void> {
  if (pipelineLoading.value) return
  pipelineLoading.value = true
  pipelineError.value = ''
  try {
    // 同时构造兼容旧版 JSON 兜底载荷，确保无论后端是否已切换到新版 pipeline handler 都能零报错返回真实规则命中
    const fallbackJsonInput = JSON.stringify(
      pipelineForm.protocol === 'responses'
        ? {
            model: 'gemini-4-neon-high',
            instructions: pipelineForm.systemPrompt,
            input: [{ role: 'user', content: pipelineForm.userMessage }],
          }
        : pipelineForm.protocol === 'anthropic'
          ? {
              model: 'claude-sonnet-4-6',
              system: [{ type: 'text', text: pipelineForm.systemPrompt }],
              messages: [{ role: 'user', content: pipelineForm.userMessage }],
            }
          : {
              model: 'claude-sonnet-4-6',
              messages: [
                ...(pipelineForm.systemPrompt
                  ? [{ role: 'system', content: pipelineForm.systemPrompt }]
                  : []),
                { role: 'user', content: pipelineForm.userMessage },
              ],
            },
      null,
      2,
    )

    const rawRes = await previewFilterPipeline({
      system_prompt: pipelineForm.systemPrompt,
      user_message: pipelineForm.userMessage,
      agy_mode: pipelineForm.agyMode,
      has_tools: pipelineForm.hasTools,
      protocol: pipelineForm.protocol,
      input: fallbackJsonInput,
    })

    pipelineResult.value = normalizeInterceptPreview(rawRes, {
      systemPrompt: pipelineForm.systemPrompt,
      userMessage: pipelineForm.userMessage,
      agyMode: pipelineForm.agyMode,
      hasTools: pipelineForm.hasTools,
    })
  } catch (err) {
    pipelineError.value = toMessage(err, '执行拦截与接续预览失败')
  } finally {
    pipelineLoading.value = false
  }
}

const currentPresetSummary = computed(
  () => INTERCEPT_SANDBOX_PRESETS.find((p) => p.key === activePresetKey.value)?.summary ?? '',
)

// ---- 模式 2：原始三协议 JSON / 局部文本与逐行 Diff 状态 ----
const SAMPLE_CLAUDE_JSON = JSON.stringify(
  {
    model: 'claude-sonnet-4-6',
    system: [
      {
        type: 'text',
        text: "x-anthropic-billing-header: cc_version=2.1.4; cc_entrypoint=cli; cch=9f8a12;\nYou are Claude Code, Anthropic's official CLI for Claude.\nTo give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues\nDo not generate denial-of-service or exploit code.",
      },
    ],
    tools: [
      {
        name: 'bash',
        description: 'Execute shell commands in sandboxed environment (you will usually use this for PRs).',
        input_schema: { type: 'object', properties: { command: { type: 'string' } } },
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

const SAMPLE_CODEX_JSON = JSON.stringify(
  {
    model: 'gemini-4-neon-high',
    instructions:
      'You are a coding agent running in the Codex CLI, a terminal-based coding assistant. Refuse malware, backdoor, and SQL injection generation.',
    input: [
      {
        role: 'developer',
        content: '# AGENTS.md instructions\nCheck codebase for vulnerabilities and brute force vectors.',
      },
      { role: 'user', content: 'List all HTTP routes defined in router.go.' },
    ],
  },
  null,
  2,
)

const rawInput = ref(SAMPLE_CLAUDE_JSON)
const rawMode = ref<FilterPreviewMode>('json')
const rawProtocol = ref<FilterPreviewProtocol>('anthropic')
const rawLoading = ref(false)
const rawError = ref('')
const rawResult = ref<FilterRulePreviewResult | null>(null)
const rawViewMode = ref<string>('split')

const rawModeOptions = [
  { label: '自动识别 (auto)', value: 'auto' },
  { label: '完整请求 JSON (json)', value: 'json' },
  { label: '系统提示词 (system)', value: 'system' },
  { label: '工具描述 (tool)', value: 'tool' },
  { label: '用户 Harness 块 (user_harness)', value: 'user_harness' },
]

const rawHits = computed<FilterRuleHit[]>(
  () => rawResult.value?.hits ?? rawResult.value?.stats?.hits ?? [],
)

interface DiffLine {
  type: 'same' | 'del' | 'add'
  text: string
}

const diffLines = computed<DiffLine[]>(() => {
  const res = rawResult.value
  if (!res) return []
  const a = (res.input || rawInput.value).split('\n')
  const b = (res.output || '').split('\n')
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

function loadRawSample(kind: 'claude' | 'codex'): void {
  if (kind === 'claude') {
    rawInput.value = SAMPLE_CLAUDE_JSON
    rawProtocol.value = 'anthropic'
    rawMode.value = 'json'
  } else {
    rawInput.value = SAMPLE_CODEX_JSON
    rawProtocol.value = 'responses'
    rawMode.value = 'json'
  }
  void runRawPreview()
}

async function runRawPreview(): Promise<void> {
  if (!rawInput.value.trim() || rawLoading.value) return
  rawLoading.value = true
  rawError.value = ''
  try {
    rawResult.value = await filterRulesApi.preview({
      input: rawInput.value,
      mode: rawMode.value,
      protocol: rawProtocol.value,
    })
  } catch (err) {
    rawError.value = toMessage(err, '执行 JSON 清洗预览失败')
  } finally {
    rawLoading.value = false
  }
}

onMounted(() => {
  void runPipelinePreview()
})

defineExpose({
  refreshPreview: runPipelinePreview,
})
</script>

<template>
  <Card
    title="拦截与接续实时沙盒 (Live Intercept & Continuation Tester)"
    subtitle="实时调用 POST /api/filter-rules/preview，并排审阅「原始输入 → 命中拦截规则与容器剥离 → AGY 默认提示词拦截接续后终态」"
    body-class="p-4 space-y-4"
  >
    <template #actions>
      <SegmentedTabs v-model="sandboxTab" :items="sandboxTabItems" />
    </template>

    <!-- ==================== 模式 1：拦截与接续全链路三栏沙盒 ==================== -->
    <template v-if="sandboxTab === 'pipeline'">
      <!-- 预设样例选择胶囊条 -->
      <div class="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-raised/50 px-3 py-2">
        <div class="flex flex-wrap items-center gap-1.5">
          <span class="text-2xs font-medium text-subtle">场景预设：</span>
          <button
            v-for="preset in INTERCEPT_SANDBOX_PRESETS"
            :key="preset.key"
            type="button"
            class="inline-flex items-center gap-1 rounded border px-2 py-0.5 text-2xs font-medium transition"
            :class="
              activePresetKey === preset.key
                ? 'border-accent/60 bg-accent/12 text-accent'
                : 'border-line bg-surface text-muted hover:border-line-strong hover:text-fg'
            "
            @click="selectPreset(preset)"
          >
            <span>{{ preset.label }}</span>
          </button>
          <button
            type="button"
            class="inline-flex items-center gap-1 rounded border px-2 py-0.5 text-2xs font-medium transition"
            :class="
              activePresetKey === 'safe_words'
                ? 'border-success/60 bg-success/12 text-success'
                : 'border-line bg-surface text-muted hover:border-line-strong hover:text-fg'
            "
            title="验证 skill / killed worker / SQL query 等正常工程词汇零误伤"
            @click="loadSafeWordCheck"
          >
            <Icon name="checkCircle" :size="11" />
            <span>安全词汇无误伤校验</span>
          </button>
        </div>

        <span v-if="currentPresetSummary" class="text-2xs text-subtle">
          {{ currentPresetSummary }}
        </span>
      </div>

      <!-- 参数控制条 -->
      <div class="grid gap-3 md:grid-cols-12">
        <div class="md:col-span-5">
          <Field label="AGY 运行管线模式 (agy_mode)">
            <Select v-model="pipelineForm.agyMode" size="sm" :options="agyModeOptions" />
          </Field>
        </div>
        <div class="md:col-span-3">
          <Field label="入口协议 (protocol)">
            <Select v-model="pipelineForm.protocol" size="sm" :options="protocolOptions" />
          </Field>
        </div>
        <div class="flex items-end pb-1 md:col-span-2">
          <Switch
            v-model="pipelineForm.hasTools"
            size="sm"
            :label="pipelineForm.hasTools ? '携带外部 Tools' : '无外部 Tools'"
          />
        </div>
        <div class="flex items-end justify-end md:col-span-2">
          <Button
            variant="primary"
            size="sm"
            block
            :loading="pipelineLoading"
            @click="runPipelinePreview"
          >
            <template #leading><Icon name="zap" :size="13" /></template>
            运行管线演练
          </Button>
        </div>
      </div>

      <!-- 双输入编辑区 -->
      <div class="grid gap-3.5 lg:grid-cols-2">
        <Field label="输入 1 · 下游客户端 System / Instructions 提示词 (system_prompt)">
          <Textarea
            v-model="pipelineForm.systemPrompt"
            :rows="5"
            mono
            placeholder="粘贴 Claude Code / Codex / Cursor / DSH 等客户端原始 System 提示词…"
            @input="activePresetKey = 'custom'"
          />
        </Field>
        <Field label="输入 2 · 下游用户消息与容器注入块 (user_message)">
          <Textarea
            v-model="pipelineForm.userMessage"
            :rows="5"
            mono
            placeholder="粘贴包含 <system-reminder>、<environment_context> 或纯用户提问的消息文本…"
            @input="activePresetKey = 'custom'"
          />
        </Field>
      </div>

      <p
        v-if="pipelineError"
        class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
      >
        <Icon name="alertCircle" :size="13" />
        <span>{{ pipelineError }}</span>
      </p>

      <!-- 三栏并排全链路透视面板：原始输入 -> 命中拦截规则明细 -> AGY 拦截接续后终态 -->
      <div v-if="pipelineResult" class="grid gap-3.5 xl:grid-cols-3">
        <!-- 第 1 栏：原始输入 -->
        <div class="flex flex-col rounded-panel border border-line bg-raised/30 p-3 space-y-2.5">
          <div class="flex items-center justify-between border-b border-line pb-2">
            <div class="flex items-center gap-1.5">
              <Badge tone="neutral" mono>1</Badge>
              <span class="text-xs font-semibold text-fg">原始输入 (Original Input)</span>
            </div>
            <span class="tnum font-mono text-2xs text-subtle">
              {{ pipelineResult.originalSystem.length + pipelineResult.originalUser.length }} chars
            </span>
          </div>

          <CodeBlock
            title="Original System Prompt"
            language="sys"
            max-height="10.5rem"
            :code="pipelineResult.originalSystem"
            empty-text="（未提供 System Prompt）"
          />

          <CodeBlock
            title="Original User Message"
            language="user"
            max-height="10.5rem"
            :code="pipelineResult.originalUser"
            empty-text="（未提供 User Message）"
          />
        </div>

        <!-- 第 2 栏：命中拦截规则与容器剥离明细 -->
        <div class="flex flex-col rounded-panel border border-line bg-raised/30 p-3 space-y-2.5">
          <div class="flex flex-wrap items-center justify-between gap-1.5 border-b border-line pb-2">
            <div class="flex items-center gap-1.5">
              <Badge tone="warning" mono>2</Badge>
              <span class="text-xs font-semibold text-fg">命中拦截规则明细</span>
            </div>
            <div class="flex items-center gap-1.5 text-2xs">
              <Badge :tone="pipelineResult.hasZeroWidth ? 'danger' : 'success'" dot>
                {{ pipelineResult.hasZeroWidth ? '含零宽字符' : '零 \\u200b' }}
              </Badge>
            </div>
          </div>

          <!-- 计数指标条 -->
          <div class="grid grid-cols-3 gap-1.5 text-center">
            <div class="rounded border border-line bg-surface px-2 py-1.5">
              <div class="text-[10px] text-subtle">Prune 删减</div>
              <div class="tnum font-mono text-xs font-semibold text-warning">
                {{ pipelineResult.pruneHits }}
              </div>
            </div>
            <div class="rounded border border-line bg-surface px-2 py-1.5">
              <div class="text-[10px] text-subtle">Rewrite 重写</div>
              <div class="tnum font-mono text-xs font-semibold text-info">
                {{ pipelineResult.rewriteHits }}
              </div>
            </div>
            <div class="rounded border border-line bg-surface px-2 py-1.5">
              <div class="text-[10px] text-subtle">Replace 替换</div>
              <div class="tnum font-mono text-xs font-semibold text-success">
                {{ pipelineResult.replaceHits }}
              </div>
            </div>
          </div>

          <!-- 识别出的客户端 Harness 包裹标签 -->
          <div class="rounded border border-line bg-surface px-2.5 py-2 space-y-1.5">
            <div class="flex items-center justify-between text-2xs">
              <span class="font-medium text-muted">识别的客户端 Harness / 宿主特征</span>
              <span class="tnum font-mono text-subtle">{{ pipelineResult.detectedHarnesses.length }} 项</span>
            </div>
            <div v-if="pipelineResult.detectedHarnesses.length > 0" class="flex flex-wrap gap-1">
              <Badge
                v-for="h in pipelineResult.detectedHarnesses"
                :key="h.key"
                tone="info"
                mono
                :title="h.preview || `${h.platform} · ${h.scope} · ${h.action}`"
              >
                {{ h.tag }}{{ h.platform ? ` (${h.platform})` : '' }}
              </Badge>
            </div>
            <p v-else class="text-2xs text-subtle">
              未检测到客户端注入包裹块，纯用户提问字节级原样保护。
            </p>
          </div>

          <!-- 命中规则列表 -->
          <div class="scroll-thin max-h-40 overflow-y-auto rounded border border-line bg-surface">
            <div
              v-if="pipelineResult.hits.length === 0"
              class="px-3 py-4 text-center text-2xs text-subtle"
            >
              当前输入未触发风控替换规则（0 误伤通过）
            </div>
            <table v-else class="w-full border-collapse text-left">
              <thead class="border-b border-line bg-raised/60">
                <tr>
                  <th class="px-2.5 py-1.5 text-[10px] font-semibold text-subtle">规则</th>
                  <th class="px-2 py-1.5 text-[10px] font-semibold text-subtle">类型</th>
                  <th class="px-2 py-1.5 text-right text-[10px] font-semibold text-subtle">次数</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-line text-2xs">
                <tr v-for="(hit, hIdx) in pipelineResult.hits" :key="`${hit.rule_id}-${hIdx}`">
                  <td class="px-2.5 py-1.5">
                    <div class="font-medium text-fg">{{ hit.name }}</div>
                    <div class="max-w-[13rem] truncate font-mono text-[10px] text-subtle" :title="`${hit.pattern} → ${hit.replacement || '（删空）'}`">
                      {{ hit.pattern }} → {{ hit.replacement || '（删空）' }}
                    </div>
                  </td>
                  <td class="px-2 py-1.5">
                    <Badge :tone="kindBadge(hit.kind).tone">{{ kindBadge(hit.kind).label }}</Badge>
                  </td>
                  <td class="tnum px-2 py-1.5 text-right font-mono text-accent">{{ hit.count }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <CodeBlock
            title="清洗脱敏后下游 System (sanitized_system)"
            language="clean"
            max-height="6.5rem"
            :code="pipelineResult.sanitizedSystem"
            empty-text="（清洗后为空）"
          />
        </div>

        <!-- 第 3 栏：AGY 默认提示词拦截接续后终态 -->
        <div class="flex flex-col rounded-panel border border-accent/35 bg-accent/[0.02] p-3 space-y-2.5">
          <div class="flex flex-wrap items-center justify-between gap-1.5 border-b border-line pb-2">
            <div class="flex items-center gap-1.5">
              <Badge tone="success" mono>3</Badge>
              <span class="text-xs font-semibold text-fg">AGY 拦截接续后终态</span>
            </div>
            <Badge :tone="getAgyModeMeta(pipelineResult.agyMode).tone" mono>
              {{ pipelineResult.agyMode }}
            </Badge>
          </div>

          <div class="flex flex-wrap items-center justify-between gap-1.5">
            <div class="flex items-center gap-1">
              <button
                type="button"
                class="rounded border px-2 py-0.5 text-2xs font-medium transition"
                :class="
                  finalViewTab === 'final'
                    ? 'border-accent/60 bg-accent/12 text-accent'
                    : 'border-line bg-surface text-muted hover:text-fg'
                "
                @click="finalViewTab = 'final'"
              >
                完整接续信封
              </button>
              <button
                type="button"
                class="rounded border px-2 py-0.5 text-2xs font-medium transition"
                :class="
                  finalViewTab === 'bridge'
                    ? 'border-accent/60 bg-accent/12 text-accent'
                    : 'border-line bg-surface text-muted hover:text-fg'
                "
                @click="finalViewTab = 'bridge'"
              >
                拦截桥接段
              </button>
              <button
                type="button"
                class="rounded border px-2 py-0.5 text-2xs font-medium transition"
                :class="
                  finalViewTab === 'user'
                    ? 'border-accent/60 bg-accent/12 text-accent'
                    : 'border-line bg-surface text-muted hover:text-fg'
                "
                @click="finalViewTab = 'user'"
              >
                清洗后用户轮次
              </button>
              <button
                v-if="pipelineResult.stages.length > 0"
                type="button"
                class="rounded border px-2 py-0.5 text-2xs font-medium transition"
                :class="
                  finalViewTab === 'stages'
                    ? 'border-accent/60 bg-accent/12 text-accent'
                    : 'border-line bg-surface text-muted hover:text-fg'
                "
                @click="finalViewTab = 'stages'"
              >
                四阶段快照 ({{ pipelineResult.stages.length }})
              </button>
            </div>
            <Badge v-if="pipelineResult.hasTools" tone="info" mono>&lt;AGY_TOOL_CALL&gt;</Badge>
          </div>

          <CodeBlock
            v-if="finalViewTab === 'final'"
            :title="`AGY Continuation Envelope (${pipelineResult.agyFinalPrompt.length} chars)`"
            language="envelope"
            max-height="19.5rem"
            :code="pipelineResult.agyFinalPrompt"
          />

          <CodeBlock
            v-else-if="finalViewTab === 'bridge'"
            title="Interception & Neutralization Bridge"
            language="bridge"
            max-height="19.5rem"
            :code="pipelineResult.interceptBridge"
          />

          <div
            v-else-if="finalViewTab === 'stages'"
            class="scroll-thin max-h-[21.5rem] space-y-2 overflow-y-auto pr-0.5"
          >
            <div
              v-for="st in pipelineResult.stages"
              :key="st.key"
              class="rounded border border-line bg-surface p-2.5 space-y-1"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="text-2xs font-semibold text-fg">{{ st.title }}</span>
                <Badge :tone="st.changed ? 'accent' : 'neutral'" mono>
                  {{ st.changed ? 'changed' : 'unchanged' }}
                </Badge>
              </div>
              <p v-if="st.description" class="text-[11px] text-subtle">{{ st.description }}</p>
              <pre
                v-if="st.systemPrompt"
                class="scroll-thin max-h-24 overflow-y-auto whitespace-pre-wrap break-words rounded border border-line bg-canvas px-2 py-1 font-mono text-[11px] text-muted"
              >{{ st.systemPrompt }}</pre>
            </div>
          </div>

          <CodeBlock
            v-else
            title="Sanitized Downstream User Turn"
            language="user"
            max-height="19.5rem"
            :code="pipelineResult.finalUser"
            empty-text="（无用户消息）"
          />
        </div>
      </div>
    </template>

    <!-- ==================== 模式 2：传统原始协议 JSON 与逐行 Diff ==================== -->
    <template v-else>
      <div class="flex flex-wrap items-end justify-between gap-3">
        <div class="flex flex-wrap items-end gap-3">
          <div class="w-48">
            <Field label="检测模式 (mode)">
              <Select v-model="rawMode" size="sm" :options="rawModeOptions" />
            </Field>
          </div>
          <div class="w-52">
            <Field label="入口协议 (protocol)">
              <Select v-model="rawProtocol" size="sm" :options="protocolOptions" />
            </Field>
          </div>
          <div class="flex items-center gap-2 pb-0.5">
            <Button
              variant="primary"
              size="sm"
              :loading="rawLoading"
              :disabled="!rawInput.trim()"
              @click="runRawPreview"
            >
              <template #leading><Icon name="zap" :size="13" /></template>
              执行 JSON 清洗
            </Button>
            <SegmentedTabs
              v-model="rawViewMode"
              :items="[
                { value: 'split', label: '左右对比' },
                { value: 'diff', label: '逐行 Diff' },
              ]"
            />
          </div>
        </div>

        <div class="flex items-center gap-1.5">
          <Button variant="secondary" size="xs" @click="loadRawSample('claude')">
            Claude Code JSON
          </Button>
          <Button variant="secondary" size="xs" @click="loadRawSample('codex')">
            Codex Responses JSON
          </Button>
        </div>
      </div>

      <p
        v-if="rawError"
        class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
      >
        <Icon name="alertCircle" :size="13" />
        <span>{{ rawError }}</span>
      </p>

      <div v-if="rawViewMode === 'split'" class="grid gap-3.5 lg:grid-cols-2">
        <Field label="输入原始请求 JSON 或单段文本">
          <Textarea v-model="rawInput" :rows="11" mono />
        </Field>
        <Field label="清洗脱敏后输出">
          <CodeBlock
            v-if="rawResult"
            title="Sanitized Output"
            language="json"
            max-height="16.5rem"
            :code="rawResult.output"
          />
          <div
            v-else
            class="flex h-[16.5rem] items-center justify-center rounded-md border border-dashed border-line bg-raised/30 px-4 text-center text-2xs text-subtle"
          >
            点击「执行 JSON 清洗」查看脱敏输出与命中规则统计
          </div>
        </Field>
      </div>

      <div v-else class="space-y-3">
        <Field label="输入编辑区">
          <Textarea v-model="rawInput" :rows="5" mono />
        </Field>
        <div v-if="rawResult" class="overflow-hidden rounded-md border border-line bg-surface">
          <div class="border-b border-line bg-raised/70 px-3 py-1.5 text-2xs font-medium text-muted">
            逐行 Diff 差异对比（共命中 {{ rawHits.length }} 条规则）
          </div>
          <div class="scroll-thin max-h-72 overflow-y-auto p-2 font-mono text-2xs leading-relaxed">
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
    </template>
  </Card>
</template>
