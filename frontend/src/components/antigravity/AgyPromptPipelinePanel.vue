<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { toMessage } from '@/api/client'
import {
  fetchAntigravityPromptModes,
  previewAntigravityPromptMode,
  updateAntigravityPromptMode,
} from '@/api/endpoints'
import type {
  AgyInterceptContinuePreview,
  AgyPromptMode,
  AgyPromptModeOption,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'

const emit = defineEmits<{
  (event: 'mode-changed', mode: AgyPromptMode): void
}>()

const toast = useToastStore()

const FALLBACK_MODES: AgyPromptModeOption[] = [
  {
    mode: 'default_intercept_continue',
    name: '默认提示词 -> 拦截 -> 接续',
    label: '默认提示词 -> 拦截 -> 接续（推荐默认）',
    description:
      '保持 agy CLI 完整内置默认系统提示词与真实上游指纹直走，经双向冲突拦截层剥离 IDE 专属假设后，权威接续下游系统指令与工具调用。',
    recommended: true,
  },
  {
    mode: 'blend_sanitized',
    name: '净化混合模式 (blend_sanitized)',
    label: '净化原生底本 + 下游混合',
    description:
      '先裁剪 AGY 原生提示词中的宿主产物、斜杠命令与本地路径段落，再顺序拼接下游客户端系统提示词。',
    recommended: false,
  },
  {
    mode: 'native_only',
    name: '纯原生模式 (native_only)',
    label: '仅保留 AGY 原生默认提示词',
    description:
      '完全使用 agy CLI 原生内置系统提示词运行，不拼接外部人设覆盖，适合纯原生能力基准回归测试。',
    recommended: false,
  },
  {
    mode: 'downstream_only',
    name: '纯下游透传 (downstream_only)',
    label: '仅透传下游客户端提示词',
    description:
      '不附带自定义 AGY 核心片段，仅附加最小反代环境声明并原样透传下游客户端传入的 System 指令。',
    recommended: false,
  },
]

const SAMPLE_PRESETS = [
  {
    id: 'claude_code',
    label: 'Claude Code CLI',
    hasTools: true,
    prompt: `You are Claude Code, Anthropic's official CLI for software engineering.
x-anthropic-billing-header: cc_version=2.1.0
Always read files before editing, make surgical changes, and invoke provided tools (Bash, Read, Edit, Grep) to verify builds.`,
  },
  {
    id: 'codex',
    label: 'Codex (/v1/responses)',
    hasTools: true,
    prompt: `You are Codex, an autonomous coding agent running in the user's terminal.
Use apply_patch to edit files and shell to run tests. Never claim to be a GUI IDE or reference browser panes.`,
  },
  {
    id: 'dsh',
    label: 'DSH 极简终端 Agent',
    hasTools: true,
    prompt: `You are DSH, a fast terminal coding harness.
Keep responses terse, use dsh_read_file and dsh_edit for code changes, and reply in Chinese.`,
  },
  {
    id: 'custom_api',
    label: '自定义架构师人设',
    hasTools: false,
    prompt: `你是一位资深 Go 分布式系统架构师，只输出简洁的中文技术方案与可编译代码，禁止暴露底层宿主环境或提及 Antigravity IDE。`,
  },
]

const loading = ref(true)
const savingMode = ref<string | null>(null)
const previewLoading = ref(false)
const sandboxExpanded = ref(false)

const activeMode = ref<AgyPromptMode>('default_intercept_continue')
const selectedPreviewMode = ref<AgyPromptMode>('default_intercept_continue')
const envOverride = ref('')
const modes = ref<AgyPromptModeOption[]>(FALLBACK_MODES)

const sampleSystem = ref(SAMPLE_PRESETS[0]!.prompt)
const sampleHasTools = ref(true)
const previewTab = ref('stages')
const previewData = ref<AgyInterceptContinuePreview | null>(null)

const previewTabs = [
  { value: 'stages', label: '三段式接续拆解', icon: 'layers' as const },
  { value: 'final', label: '合成提示词终态', icon: 'code' as const },
  { value: 'gemini_md', label: '隔离 .gemini/GEMINI.md', icon: 'fileText' as const },
]

const pipelineStages = [
  {
    step: 'STAGE 1',
    icon: 'terminal' as const,
    tone: 'success' as const,
    title: '1. AGY CLI 原生默认提示词直走',
    detail: '保留 agy.exe 真实内置系统提示词与请求特征指纹，不破坏原生工具推理链路与上下文缓存。',
  },
  {
    step: 'STAGE 2',
    icon: 'shield' as const,
    tone: 'accent' as const,
    title: '2. 双向冲突拦截与环境脱敏',
    detail: '入站清洗冲突计费头/宿主标签；注入无侵入反代拦截桥屏蔽 IDE 产物与宿主机命令；出站剥离内部桥接标记。',
  },
  {
    step: 'STAGE 3',
    icon: 'zap' as const,
    tone: 'info' as const,
    title: '3. 下游系统提示词与工具无缝接续',
    detail: '将客户端 System 指令作为当前会话权威规范接续，并通过 <AGY_TOOL_CALL> 闭环映射下游工具调用。',
  },
]

function buildLocalFallbackPreview(
  mode: AgyPromptMode,
  downstream: string,
  hasTools: boolean,
): AgyInterceptContinuePreview {
  const cleaned = downstream
    .split('\n')
    .filter((line) => !/x-anthropic-billing-header/i.test(line))
    .join('\n')
    .trim()
  const hits: string[] = []
  if (/x-anthropic-billing-header/i.test(downstream)) {
    hits.push('strip:x-anthropic-billing-header')
  }
  if (/You are Claude Code|You are Codex/i.test(downstream)) {
    hits.push('reframe:conflicting-agent-identity')
  }

  const nativeStage =
    mode === 'downstream_only'
      ? '（纯下游透传模式：跳过额外原生提示词拼接，仅保留最小运行时容器承载）'
      : '<identity>\nYou are Antigravity, a powerful agentic AI assistant...\n[agy.exe 内置默认系统提示词完整直走]\n</identity>'

  const interceptStage =
    mode === 'native_only'
      ? '（纯原生模式：未启用下游冲突拦截桥）'
      : `<agy_intercept_bridge mode="${mode}">\n- Headless API proxy mode: omit IDE artifacts, /slash suggestions, <plugin-card>, and file:/// host links.\n- Do NOT invoke host-local shell/file tools on proxy server.${hasTools ? '\n- Emit caller-declared tool invocations strictly via <AGY_TOOL_CALL>.' : ''}\n</agy_intercept_bridge>`

  const continueStage =
    mode === 'native_only'
      ? '（纯原生模式：不接续下游客户端 System 指令）'
      : `<downstream_continuation authoritative="true">\n${cleaned || '（下游未传入额外 System Prompt）'}\n</downstream_continuation>`

  const geminiMd = `<!-- cmdwb2api-isolated-agy-proxy-rules -->\n# Headless API Reverse-Proxy Operating Rules (mode=${mode})\n1. Follow <downstream_continuation> persona, OS, workspace, and output language.\n2. Suppress Antigravity IDE-only artifacts, slash commands, and host file:/// links.\n3. Route external tool calls exclusively via <AGY_TOOL_CALL> XML blocks.`

  const finalPrompt = [nativeStage, interceptStage, continueStage].join('\n\n')

  return {
    ok: true,
    mode,
    has_tools: hasTools,
    tool_tag: '<AGY_TOOL_CALL>',
    original_downstream: downstream,
    sanitized_downstream: cleaned,
    inbound_intercept_hits: hits,
    native_stage: nativeStage,
    intercept_stage: interceptStage,
    continue_stage: continueStage,
    gemini_md: geminiMd,
    final_prompt: finalPrompt,
    outbound_filter_rules: [
      '<agy_intercept_bridge>...</agy_intercept_bridge>',
      '<downstream_continuation>',
      '<!-- cmdwb2api-isolated-agy-proxy-rules -->',
    ],
  }
}

const effectivePreview = computed<AgyInterceptContinuePreview>(() => {
  if (previewData.value && previewData.value.mode === selectedPreviewMode.value) {
    return previewData.value
  }
  return buildLocalFallbackPreview(
    selectedPreviewMode.value,
    sampleSystem.value,
    sampleHasTools.value,
  )
})

const activeModeMeta = computed(
  () => modes.value.find((m) => m.mode === activeMode.value) ?? FALLBACK_MODES[0]!,
)

async function loadModes(): Promise<void> {
  loading.value = true
  try {
    const res = await fetchAntigravityPromptModes({
      downstream_system: sampleSystem.value,
      has_tools: sampleHasTools.value,
    })
    if (res.active_mode) {
      activeMode.value = res.active_mode
      selectedPreviewMode.value = res.active_mode
      emit('mode-changed', res.active_mode)
    }
    envOverride.value = res.env_override || ''
    if (Array.isArray(res.modes) && res.modes.length > 0) {
      modes.value = res.modes
    }
    if (res.preview) {
      previewData.value = res.preview
    }
  } catch {
    // 后端尚未挂载时使用内置默认元数据兜底展示，不阻塞操作台
    previewData.value = buildLocalFallbackPreview(
      selectedPreviewMode.value,
      sampleSystem.value,
      sampleHasTools.value,
    )
  } finally {
    loading.value = false
  }
}

async function selectAndApplyMode(mode: AgyPromptMode): Promise<void> {
  selectedPreviewMode.value = mode
  if (savingMode.value) return
  savingMode.value = mode
  try {
    const res = await updateAntigravityPromptMode({
      mode,
      downstream_system: sampleSystem.value,
      has_tools: sampleHasTools.value,
    })
    activeMode.value = res.active_mode || mode
    selectedPreviewMode.value = activeMode.value
    envOverride.value = res.env_override || ''
    if (res.preview) {
      previewData.value = res.preview
    } else {
      await runLivePreview(activeMode.value)
    }
    emit('mode-changed', activeMode.value)
    toast.success('AGY 提示词运行管线已切换', `当前生效模式：${modeLabel(activeMode.value)}`)
  } catch (err) {
    toast.error('切换 AGY 提示词模式失败', toMessage(err))
  } finally {
    savingMode.value = null
  }
}

async function runLivePreview(targetMode?: AgyPromptMode): Promise<void> {
  const mode = targetMode || selectedPreviewMode.value
  previewLoading.value = true
  try {
    const res = await previewAntigravityPromptMode({
      mode,
      downstream_system: sampleSystem.value,
      has_tools: sampleHasTools.value,
    })
    previewData.value = res
  } catch {
    previewData.value = buildLocalFallbackPreview(mode, sampleSystem.value, sampleHasTools.value)
  } finally {
    previewLoading.value = false
  }
}

function applyPreset(preset: (typeof SAMPLE_PRESETS)[number]): void {
  sampleSystem.value = preset.prompt
  sampleHasTools.value = preset.hasTools
  void runLivePreview()
}

function modeLabel(mode: AgyPromptMode): string {
  const hit = modes.value.find((m) => m.mode === mode) ?? FALLBACK_MODES.find((m) => m.mode === mode)
  return hit?.name || hit?.label || mode
}

onMounted(() => {
  void loadModes()
})
</script>

<template>
  <Card
    title="AGY CLI 提示词运行管线 (默认提示词 -> 拦截 -> 接续)"
    subtitle="保持 agy.exe 原生内置提示词直走以维系真实指纹，通过双向拦截层消除 IDE 假设并权威接续下游系统指令与工具协议"
    body-class="p-4 space-y-4"
  >
    <template #actions>
      <div class="flex flex-wrap items-center gap-2">
        <Badge tone="success" dot mono>
          生效中: {{ activeMode }}
        </Badge>
        <Badge v-if="envOverride" tone="warning" mono title="检测到环境变量覆盖">
          ENV: {{ envOverride }}
        </Badge>
        <Button
          variant="secondary"
          size="sm"
          @click="
            sandboxExpanded = !sandboxExpanded;
            if (sandboxExpanded && !previewData) runLivePreview();
          "
        >
          <template #leading>
            <Icon :name="sandboxExpanded ? 'chevronDown' : 'eye'" :size="13" />
          </template>
          {{ sandboxExpanded ? '收起实时预览沙盒' : '实时管线预览沙盒' }}
        </Button>
      </div>
    </template>

    <!-- 三阶段可视化管线横幅 -->
    <div class="grid gap-2.5 lg:grid-cols-11">
      <template v-for="(stage, idx) in pipelineStages" :key="stage.step">
        <div
          class="panel-inset flex flex-col justify-between gap-2 p-3 lg:col-span-3"
          :class="
            activeMode === 'default_intercept_continue'
              ? 'border-accent/30 bg-accent/[0.04]'
              : ''
          "
        >
          <div class="flex items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <span
                class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-line bg-surface text-accent"
              >
                <Icon :name="stage.icon" :size="13" />
              </span>
              <span class="text-xs font-semibold text-fg">{{ stage.title }}</span>
            </div>
            <Badge :tone="stage.tone" mono>{{ stage.step }}</Badge>
          </div>
          <p class="text-2xs leading-relaxed text-muted">
            {{ stage.detail }}
          </p>
        </div>

        <div
          v-if="idx < pipelineStages.length - 1"
          class="hidden items-center justify-center text-subtle lg:col-span-1 lg:flex"
          aria-hidden="true"
        >
          <div class="flex items-center gap-1 rounded-full border border-line bg-raised px-2 py-1">
            <Icon name="chevronRight" :size="14" class="text-accent" />
          </div>
        </div>
      </template>
    </div>

    <!-- 四种运行模式切换器 -->
    <div class="grid gap-2.5 sm:grid-cols-2 xl:grid-cols-4">
      <div
        v-for="item in modes"
        :key="item.mode"
        class="flex flex-col justify-between gap-2.5 rounded-md border p-3 transition"
        :class="
          activeMode === item.mode
            ? 'border-accent bg-accent/[0.07] shadow-panel'
            : 'border-line bg-raised/40 hover:border-line-strong'
        "
      >
        <div class="space-y-1.5">
          <div class="flex flex-wrap items-center justify-between gap-1.5">
            <code class="font-mono text-2xs font-semibold text-fg">{{ item.mode }}</code>
            <div class="flex items-center gap-1">
              <Badge
                v-if="item.recommended || item.mode === 'default_intercept_continue'"
                tone="success"
              >
                推荐默认
              </Badge>
              <Badge v-if="activeMode === item.mode" tone="accent" dot>当前生效</Badge>
            </div>
          </div>
          <p class="text-xs font-medium text-fg">{{ item.name || item.label }}</p>
          <p class="text-2xs leading-relaxed text-subtle">
            {{ item.description }}
          </p>
        </div>

        <div class="flex items-center justify-between gap-2 border-t border-line/70 pt-2">
          <button
            type="button"
            class="text-2xs font-medium text-accent hover:underline"
            @click="
              selectedPreviewMode = item.mode;
              sandboxExpanded = true;
              runLivePreview(item.mode);
            "
          >
            预览此模式
          </button>

          <Button
            :variant="activeMode === item.mode ? 'subtle' : 'secondary'"
            size="xs"
            :loading="savingMode === item.mode"
            :disabled="activeMode === item.mode || savingMode !== null"
            @click="selectAndApplyMode(item.mode)"
          >
            {{ activeMode === item.mode ? '已启用' : '设为全局生效' }}
          </Button>
        </div>
      </div>
    </div>

    <!-- 当前生效说明条 -->
    <div class="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-raised/40 px-3 py-2 text-2xs text-muted">
      <div class="flex flex-wrap items-center gap-2">
        <Icon name="info" :size="13" class="shrink-0 text-accent" />
        <span>
          当前全局模式：<strong class="text-fg">{{ activeModeMeta.name || activeModeMeta.label }}</strong>
          · 支持单次请求通过请求头 <code class="rounded border border-line bg-surface px-1 font-mono text-fg">X-Agy-Prompt-Mode</code> 按需覆盖。
        </span>
      </div>
      <button
        type="button"
        class="font-medium text-accent hover:underline"
        @click="sandboxExpanded = !sandboxExpanded"
      >
        {{ sandboxExpanded ? '隐藏实时接续沙盒' : '展开测试下游提示词接续效果 →' }}
      </button>
    </div>

    <!-- 实时拦截与接续沙盒预览 -->
    <div v-if="sandboxExpanded" class="panel-inset space-y-3.5 p-3.5">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="flex flex-wrap items-center gap-1.5">
          <span class="text-xs font-semibold text-fg">实时管线接续沙盒：</span>
          <button
            v-for="preset in SAMPLE_PRESETS"
            :key="preset.id"
            type="button"
            class="rounded border border-line bg-surface px-2 py-0.5 text-2xs font-medium text-muted transition hover:border-accent hover:text-accent"
            @click="applyPreset(preset)"
          >
            {{ preset.label }}
          </button>
        </div>

        <div class="flex flex-wrap items-center gap-3">
          <Switch
            v-model="sampleHasTools"
            size="sm"
            label="携带下游工具定义 (<AGY_TOOL_CALL>)"
            @update:model-value="runLivePreview()"
          />
          <Button
            variant="primary"
            size="xs"
            :loading="previewLoading"
            @click="runLivePreview()"
          >
            <template #leading><Icon name="refresh" :size="12" /></template>
            演算管线终态
          </Button>
        </div>
      </div>

      <div class="grid gap-3.5 lg:grid-cols-12">
        <!-- 左侧输入 -->
        <div class="space-y-2.5 lg:col-span-5">
          <Field
            :label="`模拟下游传入 System Prompt（预览模式: ${selectedPreviewMode}）`"
            hint="测试 Claude Code / Codex / Cursor / DSH 系统提示词经过拦截接续后的真实形态"
          >
            <Textarea
              v-model="sampleSystem"
              :rows="7"
              mono
              placeholder="粘贴任意下游客户端 System Prompt…"
            />
          </Field>

          <div class="flex flex-wrap items-center gap-1.5 text-2xs">
            <Badge tone="info" mono>
              工具桥标签: {{ effectivePreview.tool_tag || '<AGY_TOOL_CALL>' }}
            </Badge>
            <template v-if="effectivePreview.inbound_intercept_hits?.length">
              <Badge
                v-for="hit in effectivePreview.inbound_intercept_hits"
                :key="hit"
                tone="warning"
                mono
              >
                命中拦截: {{ hit }}
              </Badge>
            </template>
            <Badge v-else tone="neutral">入站无冲突头命中</Badge>
          </div>
        </div>

        <!-- 右侧三段输出与终态 -->
        <div class="space-y-2.5 lg:col-span-7">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <SegmentedTabs v-model="previewTab" :items="previewTabs" size="sm" />
            <Badge v-if="effectivePreview.outbound_filter_rules?.length" tone="success" dot>
              出站剥离保护 ({{ effectivePreview.outbound_filter_rules.length }} 项规则)
            </Badge>
          </div>

          <div v-if="previewTab === 'stages'" class="space-y-2">
            <CodeBlock
              title="Stage 1 · AGY CLI 原生默认层 (native_stage)"
              language="xml"
              max-height="6.5rem"
              :code="effectivePreview.native_stage || '（当前模式未启用原生默认层）'"
            />
            <CodeBlock
              title="Stage 2 · 双向冲突拦截桥 (intercept_stage)"
              language="xml"
              max-height="6.5rem"
              :code="effectivePreview.intercept_stage || '（当前模式无拦截桥注入）'"
            />
            <CodeBlock
              title="Stage 3 · 下游权威指令与工具接续 (continue_stage)"
              language="xml"
              max-height="7.5rem"
              :code="effectivePreview.continue_stage || effectivePreview.sanitized_downstream || '（无下游接续内容）'"
            />
          </div>

          <div v-else-if="previewTab === 'final'">
            <CodeBlock
              :title="`合成提示词终态 (${effectivePreview.mode})`"
              language="markdown"
              max-height="19rem"
              :code="effectivePreview.final_prompt || ''"
            />
          </div>

          <div v-else>
            <CodeBlock
              title="隔离目录 <authDir>/.gemini/GEMINI.md 自动写入内容"
              language="markdown"
              max-height="19rem"
              :code="effectivePreview.gemini_md || ''"
            />
          </div>
        </div>
      </div>
    </div>
  </Card>
</template>
