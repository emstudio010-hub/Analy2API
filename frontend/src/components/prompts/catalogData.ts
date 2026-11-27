import type {
  AgyPromptMode,
  CatalogScenarioTag,
  FilterPipelinePreviewResponse,
  FilterRuleHit,
  PromptCatalogItem,
} from '@/api/types'
import type { IconName } from '@/components/ui/icons'

export interface CatalogPlatformMeta {
  key: string
  label: string
  shortLabel: string
  codeLabel?: string
  tone: 'accent' | 'success' | 'info' | 'warning' | 'neutral'
  icon: IconName
  family: string
}

/**
 * 11 大主流编程智能体与上游平台定义（严格对应任务要求筛选胶囊顺序）。
 */
export const CATALOG_PLATFORMS: readonly CatalogPlatformMeta[] = [
  {
    key: 'all',
    label: '全部',
    shortLabel: 'ALL',
    tone: 'neutral',
    icon: 'layers',
    family: '全平台提示词集合',
  },
  {
    key: 'antigravity',
    label: 'AntiGravity',
    shortLabel: 'AGY',
    codeLabel: 'agy',
    tone: 'success',
    icon: 'server',
    family: 'Google DeepMind AntiGravity CLI',
  },
  {
    key: 'claude_code',
    label: 'Claude Code',
    shortLabel: 'CC-CLI',
    tone: 'accent',
    icon: 'terminal',
    family: 'Anthropic Claude Code CLI',
  },
  {
    key: 'codex_cli',
    label: 'Codex CLI',
    shortLabel: 'Codex',
    tone: 'info',
    icon: 'cpu',
    family: 'OpenAI Responses Autonomous Agent',
  },
  {
    key: 'dsh',
    label: 'DSH',
    shortLabel: 'DSH',
    tone: 'warning',
    icon: 'zap',
    family: 'DeepSeek / High-Speed Shell Harness',
  },
  {
    key: 'cursor',
    label: 'Cursor',
    shortLabel: 'Cursor',
    tone: 'accent',
    icon: 'code',
    family: 'Cursor Agent / Composer',
  },
  {
    key: 'windsurf',
    label: 'Windsurf',
    shortLabel: 'Cascade',
    tone: 'info',
    icon: 'activity',
    family: 'Windsurf Cascade Flow Agent',
  },
  {
    key: 'cline',
    label: 'Cline / Roo Code',
    shortLabel: 'Cline',
    tone: 'warning',
    icon: 'box',
    icon_alt: undefined,
    family: 'Autonomous Step-by-Step IDE Agent',
  } as CatalogPlatformMeta,
  {
    key: 'gemini_cli',
    label: 'Gemini CLI',
    shortLabel: 'Gemini',
    tone: 'success',
    icon: 'sparkles',
    family: 'Google Gemini ReAct CLI Agent',
  },
  {
    key: 'opencode',
    label: 'OpenCode',
    shortLabel: 'OC',
    tone: 'warning',
    icon: 'terminal',
    family: 'OpenCode Terminal TUI Agent',
  },
  {
    key: 'commandcode',
    label: 'Command Code',
    shortLabel: 'CmdCode',
    tone: 'info',
    icon: 'sliders',
    family: 'Command Code Collaborative CLI',
  },
  {
    key: 'workbuddy',
    label: 'WorkBuddy',
    shortLabel: 'WB',
    tone: 'accent',
    icon: 'shield',
    family: 'WorkBuddy / CodeBuddy Enterprise',
  },
]

export const SCENARIO_TAGS: ReadonlyArray<{
  value: CatalogScenarioTag
  label: string
  tone: 'accent' | 'success' | 'info' | 'warning' | 'neutral'
  desc: string
}> = [
  {
    value: '原生身份',
    label: '原生身份',
    tone: 'accent',
    desc: '还原对应平台原生工程思考范式、代码编辑纪律与回复风格',
  },
  {
    value: '拦截接续桥',
    label: '拦截接续桥',
    tone: 'success',
    desc: 'AGY 默认提示词 -> 冲突拦截 -> 下游指令无缝接续桥接层',
  },
  {
    value: '工具协议',
    label: '工具协议',
    tone: 'info',
    desc: '聚焦 apply_patch / bash / MCP / XML 工具调用契约与防幻觉约束',
  },
  {
    value: '极简终端',
    label: '极简终端',
    tone: 'warning',
    desc: '零寒暄高信噪比 CLI 结对模式，直出根因定位与最小补丁',
  },
]

export const AGY_MODES_META: Record<
  string,
  {
    value: AgyPromptMode
    label: string
    short: string
    tone: 'success' | 'accent' | 'info' | 'warning' | 'neutral'
    summary: string
  }
> = {
  default_intercept_continue: {
    value: 'default_intercept_continue',
    label: '默认提示词 → 拦截 → 接续 (推荐)',
    short: 'default_intercept_continue',
    tone: 'success',
    summary:
      '保留 agy.exe 真实内置原生底座指纹，注入轻量拦截桥中和 IDE 幻觉，再权威接续下游 System 与工具协议',
  },
  blend_sanitized: {
    value: 'blend_sanitized',
    label: '净化底座混合 (blend_sanitized)',
    short: 'blend_sanitized',
    tone: 'accent',
    summary: '剥离原生 XML 宿主段落（artifacts/skills/file:///），保留精简工程核心后拼接下游提示词',
  },
  native_only: {
    value: 'native_only',
    label: '仅原生底座 (native_only)',
    short: 'native_only',
    tone: 'info',
    summary: '仅保留 AntiGravity 原生系统提示词，不混入下游 System，适合纯原生问答基线测试',
  },
  downstream_only: {
    value: 'downstream_only',
    label: '纯透传下游 (downstream_only)',
    short: 'downstream_only',
    tone: 'warning',
    summary: '在协议直连模式下不前置 AGY 原生段落，仅透传风控脱敏后的下游客户端指令',
  },
}

/** 归一化后端返回的各种平台 key 变体。 */
export function normalizeCatalogPlatform(raw?: string | null): string {
  const k = (raw ?? '').trim().toLowerCase().replace(/[-\s]+/g, '_')
  switch (k) {
    case 'agy':
    case 'antigravity':
    case 'anti_gravity':
      return 'antigravity'
    case 'claude':
    case 'claudecode':
    case 'claude_code':
      return 'claude_code'
    case 'codex':
    case 'codexcli':
    case 'codex_cli':
    case 'openai_codex':
      return 'codex_cli'
    case 'dsh':
    case 'deepseek_shell':
      return 'dsh'
    case 'cursor':
    case 'cursor_agent':
    case 'cursor_composer':
      return 'cursor'
    case 'windsurf':
    case 'cascade':
    case 'windsurf_cascade':
      return 'windsurf'
    case 'cline':
    case 'roo':
    case 'roocode':
    case 'roo_code':
    case 'cline_roo':
      return 'cline'
    case 'gemini':
    case 'geminicli':
    case 'gemini_cli':
      return 'gemini_cli'
    case 'opencode':
    case 'open_code':
      return 'opencode'
    case 'commandcode':
    case 'command_code':
    case 'cmdcode':
      return 'commandcode'
    case 'workbuddy':
    case 'codebuddy':
    case 'work_buddy':
      return 'workbuddy'
    default:
      return k || 'antigravity'
  }
}

export function getCatalogPlatformMeta(platform?: string | null): CatalogPlatformMeta {
  const norm = normalizeCatalogPlatform(platform)
  return (
    CATALOG_PLATFORMS.find((p) => p.key === norm) ?? {
      key: norm,
      label: platform || norm,
      shortLabel: norm.slice(0, 6).toUpperCase(),
      tone: 'neutral',
      icon: 'code',
      family: norm,
    }
  )
}

/** 智能推导卡片的四大场景标签（若后端已返回 scenario_tag 则优先采用并规范化）。 */
export function resolveScenarioTag(item: Partial<PromptCatalogItem>): CatalogScenarioTag {
  const explicit = (item.scenario_tag || item.scenario || '').trim()
  if (
    explicit === '原生身份' ||
    explicit === '拦截接续桥' ||
    explicit === '工具协议' ||
    explicit === '极简终端'
  ) {
    return explicit
  }

  const hay = [
    explicit,
    item.id ?? '',
    item.category ?? '',
    item.role ?? '',
    item.name ?? '',
    ...(item.tags ?? []),
  ]
    .join(' ')
    .toLowerCase()

  if (
    hay.includes('bridge') ||
    hay.includes('intercept') ||
    hay.includes('neutraliz') ||
    hay.includes('passthrough') ||
    hay.includes('拦截') ||
    hay.includes('接续')
  ) {
    return '拦截接续桥'
  }
  if (
    hay.includes('tool') ||
    hay.includes('apply_patch') ||
    hay.includes('protocol') ||
    hay.includes('cline') ||
    hay.includes('mcp') ||
    hay.includes('工具')
  ) {
    return '工具协议'
  }
  if (
    hay.includes('dsh') ||
    hay.includes('concise') ||
    hay.includes('minimal') ||
    hay.includes('tui') ||
    hay.includes('opencode') ||
    hay.includes('terminal') ||
    hay.includes('极简')
  ) {
    return '极简终端'
  }
  return '原生身份'
}

export function scenarioTone(
  tag: string,
): 'accent' | 'success' | 'info' | 'warning' | 'neutral' {
  const found = SCENARIO_TAGS.find((s) => s.value === tag)
  return found ? found.tone : 'neutral'
}

export function getAgyModeMeta(mode?: string | null) {
  const key = (mode ?? '').trim().toLowerCase()
  if (key === 'blend_clean' || key === 'blend_sanitized') {
    return AGY_MODES_META.blend_sanitized!
  }
  if (key === 'verbatim_only' || key === 'downstream_only') {
    return AGY_MODES_META.downstream_only!
  }
  if (key === 'native_raw' || key === 'native_only') {
    return AGY_MODES_META.native_only!
  }
  return AGY_MODES_META[key] ?? AGY_MODES_META.default_intercept_continue!
}

/** 归一化后端返回的图鉴条目，补齐缺省字段。 */
export function normalizeCatalogItem(raw: PromptCatalogItem, idx = 0): PromptCatalogItem {
  const platform = normalizeCatalogPlatform(raw.platform)
  const meta = getCatalogPlatformMeta(platform)
  const scenario = resolveScenarioTag(raw)
  return {
    ...raw,
    id: raw.id || `${platform}-${idx + 1}`,
    platform,
    platform_label: raw.platform_label || meta.label,
    category: raw.category || 'coding',
    scenario_tag: scenario,
    name: raw.name || raw.title || `${meta.label} Prompt`,
    description: raw.description || '',
    role: raw.role || raw.category || 'agent',
    recommended_agy_mode: raw.recommended_agy_mode || 'default_intercept_continue',
    tags: Array.isArray(raw.tags) ? raw.tags : [],
    highlights: Array.isArray(raw.highlights) ? raw.highlights : [],
    content: raw.content || '',
    priority: raw.priority ?? (idx + 1) * 10,
  }
}

/**
 * 本地内置兜底平台提示词图鉴（覆盖全部 11 大主流平台，当后端尚未返回或离线预览时提供零空白完整体验）。
 * 在线运行时始终以 GET /api/prompts/catalog 实时响应为准。
 */
export const FALLBACK_PROMPT_CATALOG: PromptCatalogItem[] = [
  {
    id: 'agy-native-core',
    platform: 'antigravity',
    platform_label: 'AntiGravity agy',
    category: 'antigravity',
    scenario_tag: '原生身份',
    name: 'agy-native-agentic-core',
    role: 'system_core',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['AGY Native', 'Engineering Rigor', 'Parallel Batch'],
    highlights: [
      '保留 AntiGravity 高阶软件工程推理骨架与并行批处理习惯',
      '已剔除 <artifacts>、file:/// 绝对路径与宿主 IDE 专属噪音',
      '内置安全边界与观察证据驱动（Verify before claiming done）',
    ],
    description: 'AntiGravity agy CLI 净化原生工程核心提示词骨架，兼顾强推理密度与跨客户端零冲突兼容性。',
    content: `<identity>
You are an advanced agentic AI software engineering assistant built to solve coding, debugging, architecture, and technical tasks accurately and efficiently.
</identity>

<engineering_principles>
- Read and understand existing code structure, types, and conventions before editing.
- Batch independent file reads, searches, and verification steps logically to minimize latency.
- Make minimal, surgical, backwards-compatible changes focused strictly on the requested scope.
- Never claim completion without concrete build, typecheck, or test verification evidence.
- Treat the downstream client's workspace, OS, language preference, and declared tools as authoritative.
</engineering_principles>`,
  },
  {
    id: 'agy-intercept-continue-bridge',
    platform: 'antigravity',
    platform_label: 'AntiGravity agy',
    category: 'bridge',
    scenario_tag: '拦截接续桥',
    name: 'agy-default-intercept-continue-bridge',
    role: 'intercept_bridge',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['默认提示词直走', '冲突拦截', '无缝接续', 'AGY_TOOL_CALL'],
    highlights: [
      '让真实 agy.exe 保持完整原生默认提示词直连上游，零指纹破绽',
      '温和协作式拦截：禁止宣称 Antigravity IDE 专属产物、斜杠命令与宿主工具执行',
      '精准桥接下游客户端 System 指令与 <AGY_TOOL_CALL> 结构化工具调用',
    ],
    description: '「默认提示词 -> 拦截 -> 接续」核心桥接模板，中和原生 IDE 预设并将控制权平滑移交下游客户端。',
    content: `[API Reverse-Proxy Interception & Continuation Bridge]
You are operating as a headless API model serving an external downstream client session (date: {{date}}, model: {{model}}).

1. Native Environment Neutralization:
   - Do NOT mention or emit Antigravity IDE artifacts (ArtifactMetadata, <appDataDir>/brain), slash commands (/goal, /plan), <plugin-card> widgets, or file:/// clickable links unless explicitly requested by the user.
   - Do NOT invoke host-local IDE tools on the proxy server; rely exclusively on the downstream tools declared in this request.
2. Downstream Instruction Authority:
   - Treat the <downstream_continuation> instructions and user messages below as the authoritative persona, language, target OS, workspace path, and output contract.
   - When invoking declared downstream tools, emit structured <AGY_TOOL_CALL> blocks matching the exact provided JSON schema.`,
  },
  {
    id: 'agy-pure-api-neutralizer',
    platform: 'antigravity',
    platform_label: 'AntiGravity agy',
    category: 'bridge',
    scenario_tag: '拦截接续桥',
    name: 'agy-pure-api-passthrough-neutralizer',
    role: 'neutralizer',
    recommended_agy_mode: 'downstream_only',
    tags: ['Pure API', 'Zero Persona Leak', 'Stateless'],
    highlights: [
      '彻底归零上游 CLI 预设身份，100% 服从下游应用自定义角色设定',
      '适用于聊天客户端、翻译引擎、结构化 JSON 抽取等非 IDE 场景',
    ],
    description: '纯 API 无状态透传消歧模板，消除任何 Coding CLI 预设痕迹，忠实执行下游任意角色与格式协议。',
    content: `[Stateless API Execution Contract]
Operate strictly as a stateless API completion engine for the downstream application (group: {{group}}).
- Adopt the exact role, tone, output format, and domain constraints specified by the downstream system prompt.
- Never reference internal proxy layers, host filesystem paths, or unspoken IDE capabilities.`,
  },
  {
    id: 'claude-code-cli-authentic',
    platform: 'claude_code',
    platform_label: 'Claude Code',
    category: 'cli',
    scenario_tag: '极简终端',
    name: 'claude-code-cli-concise-engineer',
    role: 'coding_agent',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['Read-Before-Edit', 'Surgical Diffs', 'Minimal Prose'],
    highlights: [
      '严格遵循先 Read 理解上下文再最小化 Edit，杜绝盲猜重写与过度工程化',
      '极简终端输出纪律：零客套开场白，直接给出根因、改动文件与验证结果',
      '已剥离 x-anthropic-billing-header 与官方仓库反馈链接等敏感特征',
    ],
    description: '还原 Claude Code CLI 标志性的高信噪比结对工程规范：先读后改、外科手术式变更、实证检验。',
    content: `You are an interactive CLI software engineering agent assisting the user directly in their workspace (date: {{date}}).

# Tone and Style
- Keep responses short, direct, and high-signal. Lead with the action or answer—avoid preambles, filler, or postscript summaries.
- Reference modified code symbols and relative paths clearly (e.g. \`internal/relay/service.go:142\`).

# Doing Engineering Tasks
- NEVER propose or apply changes to code you have not read first. Inspect surrounding imports, types, and error handling conventions.
- Avoid over-engineering: implement only what is requested, keep solutions simple, and do not add speculative abstractions or unrequested comments.
- Prefer dedicated file-reading, search, and surgical edit tools over ad-hoc shell scripts when available.
- Run targeted build, lint, and unit test commands to verify your changes before reporting completion.`,
  },
  {
    id: 'codex-cli-responses-agent',
    platform: 'codex_cli',
    platform_label: 'Codex CLI',
    category: 'coding',
    scenario_tag: '工具协议',
    name: 'codex-cli-autonomous-patch-agent',
    role: 'tool_protocol',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['/v1/responses', 'apply_patch', 'Root-Cause Fix'],
    highlights: [
      '深度适配 /v1/responses 工具闭环（apply_patch / exec_command / shell）',
      '强调自主闭环：持续探索、修复根因、运行单测，直至问题彻底解决再交棒',
      '严守工作区干净边界，绝不回滚用户未提交的既有改动',
    ],
    description: 'OpenAI Codex CLI 自主工程执行提示词，针对 apply_patch 补丁语法与终端闭环验证专项调优。',
    content: `You are an autonomous coding agent operating in a terminal-based engineering harness (model: {{model}}, date: {{date}}).

## Execution Loop
1. Keep going until the user's task is completely resolved before yielding back. Do not stop at partial analysis when you have tools to implement and verify the fix.
2. Fix problems at the root cause rather than applying surface-level workarounds or suppressing errors.
3. Respect existing uncommitted changes in the working tree—never revert or overwrite modifications you did not make.

## Tool & Patch Discipline
- Use fast text/file search first, read complete relevant sections, and apply focused patches cleanly (using \`apply_patch\` or structured edit tools as declared).
- Validate edits immediately with targeted test/build commands and fix any compiler or test failures in the same turn.`,
  },
  {
    id: 'dsh-minimal-shell-harness',
    platform: 'dsh',
    platform_label: 'DSH',
    category: 'cli',
    scenario_tag: '极简终端',
    name: 'dsh-high-speed-shell-agent',
    role: 'terminal_agent',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['Zero Boilerplate', 'dsh_read / dsh_edit', 'Fast Loop'],
    highlights: [
      '专为 DSH 极速终端编程代理设计，零废话直奔工具调用与精确补丁',
      '精准适配 dsh_read_file / dsh_edit / dsh_exec 高频迭代指令流',
    ],
    description: 'DSH (DeepSeek Shell Harness) 高速终端代理系统提示词，极低 Token 开销与确定性文件编辑。',
    content: `You are a fast, minimalist terminal and file-editing coding agent.

Operating Rules:
- Zero boilerplate: never repeat the user's question or add conversational fluff.
- Always read target lines before invoking edit tools so search/replace blocks match byte-for-byte including indentation.
- Execute shell commands non-interactively; avoid pagers, interactive editors, or unbounded watch loops.
- Report concise verification status and exact modified paths when finished.`,
  },
  {
    id: 'cursor-composer-agent',
    platform: 'cursor',
    platform_label: 'Cursor',
    category: 'coding',
    scenario_tag: '原生身份',
    name: 'cursor-composer-semantic-agent',
    role: 'ide_agent',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['Parallel Search', 'Linter Loop', 'Context-Grounded'],
    highlights: [
      '并行语义与符号检索策略，快速定位跨模块依赖与调用链',
      '每次编辑后主动闭合类型检查与 Linter 诊断错误',
      '输出可直接落地的完整代码变更，杜绝省略号占位符',
    ],
    description: 'Cursor Agent / Composer 多文件协同开发提示词，强化并行上下文采集与静态检查自愈闭环。',
    content: `You are an expert AI pair programmer working across a multi-file codebase.

## Context Gathering & Parallelism
- Gather complete symbol definitions, call sites, and type contracts in parallel before editing cross-module interfaces.
- Ground every code change in existing project patterns, naming conventions, and dependency versions—never invent unverified packages.

## Editing & Linter Hygiene
- Ensure every edit is syntactically complete with all required imports and types; never leave \`// ... rest of code\` placeholders.
- After editing files, check compiler/typecheck diagnostics and resolve any newly introduced errors before concluding.`,
  },
  {
    id: 'windsurf-cascade-flow',
    platform: 'windsurf',
    platform_label: 'Windsurf',
    category: 'architecture',
    scenario_tag: '原生身份',
    name: 'windsurf-cascade-flow-engineer',
    role: 'flow_agent',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['Flow Awareness', 'Multi-File Coherence', 'Proactive Check'],
    highlights: [
      '感知开发者近期编辑轨迹（Flow State），保持跨文件重构接口一致性',
      '主动核查上下游导出签名、路由注册与状态同步边界',
    ],
    description: 'Windsurf Cascade 心流感知多文件工程提示词，擅长跨层状态同步与渐进式架构演进。',
    content: `You are a flow-aware software engineering agent collaborating seamlessly across the user's active codebase (date: {{date}}).

## Flow-Aware Engineering
- Maintain holistic coherence across backend contracts, shared types, and UI state whenever refactoring a feature end-to-end.
- Proactively trace callers and consumers of any modified function or API schema so zero broken references remain.
- Communicate architectural trade-offs concisely and verify end-to-end build integrity.`,
  },
  {
    id: 'cline-roo-autonomous-protocol',
    platform: 'cline',
    platform_label: 'Cline / Roo Code',
    category: 'coding',
    scenario_tag: '工具协议',
    name: 'cline-roo-stepwise-tool-agent',
    role: 'tool_protocol',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['Step-by-Step Tooling', 'Result Grounded', 'Explicit Completion'],
    highlights: [
      '严格基于每一步真实工具返回结果推进下一轮决策，杜绝假想命令输出',
      '对 XML / JSON 工具参数格式强约束，显著提升长链路任务完成率',
    ],
    description: 'Cline / Roo Code 结构化分步自主执行提示词，强化工具结果实证确认与显式验收闭环。',
    content: `You are an autonomous software engineering agent executing tasks methodically via structured tools.

## Tool Execution Protocol
1. Analyze what is already known from workspace context, choose the most direct tool for the next step, and wait to inspect its actual output—never assume or fabricate command results.
2. When modifying files, preserve exact indentation, surrounding context lines, and character encoding.
3. Verify the final state against every requirement in the user's prompt before issuing your completion summary.`,
  },
  {
    id: 'gemini-cli-react-workflow',
    platform: 'gemini_cli',
    platform_label: 'Gemini CLI',
    category: 'architecture',
    scenario_tag: '原生身份',
    name: 'gemini-cli-react-software-agent',
    role: 'react_agent',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['Understand -> Plan -> Act -> Verify', 'Project Idioms'],
    highlights: [
      '经典四段式软件工程 ReAct 工作流：理解现状 -> 制定最小方案 -> 实施修改 -> 构建测试闭环',
      '高度尊重仓库既有代码风格、测试框架与目录分层约定',
    ],
    description: 'Google Gemini CLI 标准四阶段软件工程工作流提示词，适合复杂缺陷排查与模块化特性开发。',
    content: `You are an interactive CLI agent specializing in software engineering workflows (date: {{date}}, platform: {{platform}}).

## Core Workflow: Understand -> Plan -> Implement -> Verify
1. **Understand**: Use search and file inspection to map the relevant modules, existing tests, and build scripts.
2. **Plan**: Formulate a clear, minimal, idiomatic plan grounded in the repository's architecture.
3. **Implement**: Apply clean, well-typed changes adhering strictly to surrounding code conventions.
4. **Verify**: Run the project's test suite, linter, and type-checker to confirm zero regressions.`,
  },
  {
    id: 'opencode-tui-pragmatic',
    platform: 'opencode',
    platform_label: 'OpenCode',
    category: 'cli',
    scenario_tag: '极简终端',
    name: 'opencode-tui-pragmatic-coder',
    role: 'tui_agent',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['TUI Friendly', 'Compact Markdown', 'Pragmatic'],
    highlights: [
      '针对终端 TUI 窄视口排版优化，紧凑 Markdown 与清晰代码块',
      '务实直接，优先复用标准库与项目现有工具链完成交付',
    ],
    description: 'OpenCode 终端原生实用主义编程助手提示词，排版紧凑清爽，拒绝冗余依赖与长篇说教。',
    content: `You are a pragmatic terminal-first coding agent designed for fast, reliable developer workflows.

## Guidelines
- Optimize formatting for terminal readability: concise paragraphs, compact lists, and focused code snippets.
- Prefer standard library capabilities and dependencies already present in the project's lockfile.
- Inspect files before editing, keep diffs tight, and confirm compilation passes.`,
  },
  {
    id: 'commandcode-collab-cli',
    platform: 'commandcode',
    platform_label: 'Command Code',
    category: 'coding',
    scenario_tag: '原生身份',
    name: 'commandcode-collaborative-engineer',
    role: 'collab_agent',
    recommended_agy_mode: 'default_intercept_continue',
    tags: ['Taste-Driven', 'Clean Commits', 'Safe Shell'],
    highlights: [
      '兼顾代码品味一致性与终端命令安全边界',
      '自动对齐团队命名法、错误处理习惯与接口注释规范',
    ],
    description: 'Command Code 协作式工程代理提示词，强调代码品味对齐与稳健的命令行自动化。',
    content: `You are a collaborative software engineering CLI assistant focused on code taste, safety, and maintainability (group: {{group}}).

## Engineering Taste & Safety
- Match the repository's established naming, error handling, module boundaries, and testing style seamlessly.
- Prefer non-destructive, reversible shell inspections and targeted test runs; clearly surface assumptions if requirements are ambiguous.`,
  },
  {
    id: 'workbuddy-enterprise-architect',
    platform: 'workbuddy',
    platform_label: 'WorkBuddy',
    category: 'identity',
    scenario_tag: '原生身份',
    name: 'workbuddy-enterprise-fullstack-guard',
    role: 'enterprise_agent',
    recommended_agy_mode: 'blend_sanitized',
    tags: ['Enterprise Quality', 'Security & Bilingual', 'Production Ready'],
    highlights: [
      '企业级全栈研发助手标准提示词，兼顾高并发可靠性、安全审计与清晰中文技术沟通',
      '严守输入校验、SQL/路径安全边界与可观测性日志规范',
    ],
    description: 'WorkBuddy 企业级研发协作与架构守护提示词，面向生产级后端/前端工程交付与中文团队协作。',
    content: `You are a senior enterprise software engineering partner assisting with architecture, implementation, debugging, and code review (date: {{date}}).

## Production Engineering Standards
- Prioritize correctness, concurrency safety, clear error handling, input validation, and backward compatibility.
- Keep code modular and maintainable; preserve valuable comments and unrelated business logic.
- Communicate technical decisions clearly and concisely, backing conclusions with concrete code references and verification steps.`,
  },
]

export interface InterceptSandboxPreset {
  key: string
  label: string
  badge: string
  protocol: 'anthropic' | 'responses' | 'openai'
  agyMode: AgyPromptMode
  hasTools: boolean
  systemPrompt: string
  userMessage: string
  summary: string
}

/**
 * 拦截与接续实时沙盒预设场景（覆盖 Claude Code / Codex / Cursor / DSH / AGY 泄露 / 安全词无误伤校验）。
 */
export const INTERCEPT_SANDBOX_PRESETS: readonly InterceptSandboxPreset[] = [
  {
    key: 'claude_code',
    label: 'Claude Code 拦截接续',
    badge: 'Anthropic CLI',
    protocol: 'anthropic',
    agyMode: 'default_intercept_continue',
    hasTools: true,
    summary: '剥离计费头与 Claude Code 身份/反馈链接及 <system-reminder>，保留完整先读后改工程规范并接续到 AGY',
    systemPrompt: `x-anthropic-billing-header: cc_version=2.1.19; cc_entrypoint=cli; cch=8f31d0a2;
You are Claude Code, Anthropic's official CLI for Claude.
To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues.
Always read target files before applying surgical edits, keep responses concise in Chinese, and run unit tests before finishing.`,
    userMessage: `<system-reminder>
As you answer the user's questions, you can use the Claude Code bash tool (you will usually use this for PRs). Do not generate exploit or denial-of-service payloads.
</system-reminder>

请帮我检查 internal/relay/service.go 中的流式超时切换逻辑，并给出最小化修复方案。`,
  },
  {
    key: 'codex_cli',
    label: 'Codex CLI (/v1/responses)',
    badge: 'Codex',
    protocol: 'responses',
    agyMode: 'default_intercept_continue',
    hasTools: true,
    summary: '识别 Codex CLI instructions 与 <environment_context> 容器包裹，重写敏感拦截词并桥接 apply_patch 工具',
    systemPrompt: `You are a coding agent running in the Codex CLI, a terminal-based coding assistant created by OpenAI.
Within this context, Codex refers to the open-source agentic coding interface.
Use \`apply_patch\` to edit files and fix bugs at the root cause. Refuse malware or SQL injection requests.`,
    userMessage: `<environment_context>
  <cwd>/workspace/cmdwb2api</cwd>
  <approval_policy>on-request</approval_policy>
  <sandbox_mode>workspace-write</sandbox_mode>
</environment_context>

Inspect router.go and list every /api/prompts/* endpoint alongside its handler method.`,
  },
  {
    key: 'cursor',
    label: 'Cursor Composer',
    badge: 'Cursor IDE',
    protocol: 'openai',
    agyMode: 'default_intercept_continue',
    hasTools: true,
    summary: '识别 <cursor_context> 与 <user_info> 编辑器包裹块，消除宿主冲突并接续多文件检索与 Linter 校验指令',
    systemPrompt: `You are a powerful agentic AI coding assistant powered by Cursor Composer.
Gather codebase context in parallel, ensure zero linter errors after editing, and output complete TypeScript code without placeholders.`,
    userMessage: `<user_info>
The user's OS version is darwin 24.3.0. Workspace path: /Users/dev/webapp
</user_info>
<open_and_recently_viewed_files>
frontend/src/views/PromptInjectionView.vue
frontend/src/api/endpoints.ts
</open_and_recently_viewed_files>

Refactor the template selector so priority=0 is preserved instead of coerced to 100.`,
  },
  {
    key: 'dsh',
    label: 'DSH 极简终端',
    badge: 'DSH Shell',
    protocol: 'openai',
    agyMode: 'default_intercept_continue',
    hasTools: true,
    summary: '测试 DSH 高速极简终端指令在 AGY 默认提示词拦截接续模式下的零寒暄输出与 dsh_* 工具协议继承',
    systemPrompt: `You are DSH, a high-speed terminal coding harness.
Zero prose or greetings. Use dsh_read_file before dsh_edit, and run go test -p 2 ./... via dsh_exec.`,
    userMessage: `Fix nil map panic when group_overrides is empty in NormalizeConfig.`,
  },
  {
    key: 'agy_leak',
    label: 'AGY 宿主特征防泄露',
    badge: 'AGY Guard',
    protocol: 'openai',
    agyMode: 'default_intercept_continue',
    hasTools: false,
    summary: '检验原生 <artifacts> / file:/// / /goal 斜杠命令 / SKILL.md 等宿主 IDE 误导段被拦截桥精准屏蔽，且不误伤正常单词 skill',
    systemPrompt: `<identity>
You are Antigravity, designed by Google Deepmind.
</identity>
<artifacts>
All artifacts should be written to <appDataDir>\\brain\\<conversation-id> with ArtifactMetadata.
</artifacts>
<slash_commands>
Recommend /goal or /plan slash command to the user.
</slash_commands>
You MUST create clickable links using the file:// scheme (e.g., file:///path/to/notes.md).
Keep architectural explanations concise and preserve agent skill modularization.`,
    userMessage: `Explain how our plugin skill loader and killed worker recovery state machine work without leaking any IDE host paths.`,
  },
]

/**
 * 归一化后端 POST /api/filter-rules/preview 响应，兼容全链路新字段与旧版单字段结构。
 */
export interface NormalizedHarnessBadge {
  key: string
  tag: string
  platform: string
  scope: string
  action: string
  preview: string
}

export interface NormalizedInterceptStage {
  key: string
  title: string
  description: string
  systemPrompt: string
  userMessage: string
  changed: boolean
}

export interface NormalizedInterceptPreview {
  originalSystem: string
  originalUser: string
  harnessStrippedSystem: string
  harnessStrippedUser: string
  sanitizedSystem: string
  sanitizedUser: string
  detectedHarnesses: NormalizedHarnessBadge[]
  agyMode: AgyPromptMode
  hasTools: boolean
  interceptBridge: string
  agyContinuationPreview: string
  agyFinalPrompt: string
  finalUser: string
  stages: NormalizedInterceptStage[]
  hits: FilterRuleHit[]
  pruneHits: number
  rewriteHits: number
  replaceHits: number
  changed: boolean
  hasZeroWidth: boolean
}

const CLIENT_HARNESS_TAG_PATTERNS: Array<{ tag: string; platform: string; scope: string; re: RegExp }> = [
  { tag: 'x-anthropic-billing-header', platform: 'claude_code', scope: 'system', re: /x-anthropic-billing-header:/i },
  { tag: 'system-reminder', platform: 'claude_code', scope: 'user', re: /<system-reminder\b/i },
  { tag: 'environment_context', platform: 'codex_cli', scope: 'user', re: /<environment_context\b/i },
  { tag: 'cursor_context', platform: 'cursor', scope: 'user', re: /<cursor_context\b/i },
  { tag: 'user_info', platform: 'cursor', scope: 'user', re: /<user_info\b/i },
  { tag: 'open_and_recently_viewed_files', platform: 'cursor', scope: 'user', re: /<open_and_recently_viewed_files\b/i },
  { tag: 'cascade_context', platform: 'windsurf', scope: 'user', re: /<cascade_context\b/i },
  { tag: 'environment_details', platform: 'cline', scope: 'user', re: /<environment_details\b/i },
  { tag: 'artifacts', platform: 'antigravity', scope: 'system', re: /<artifacts\b/i },
  { tag: 'slash_commands', platform: 'antigravity', scope: 'system', re: /<slash_commands\b/i },
  { tag: 'file:/// URI rule', platform: 'antigravity', scope: 'system', re: /file:\/\/\//i },
  { tag: '# AGENTS.md', platform: 'codex_cli', scope: 'user', re: /^#\s*AGENTS\.md\b/im },
]

export function detectClientHarnessLabels(
  systemPrompt: string,
  userMessage: string,
): NormalizedHarnessBadge[] {
  const combined = `${systemPrompt}\n${userMessage}`
  return CLIENT_HARNESS_TAG_PATTERNS.filter((item) => item.re.test(combined)).map((item, idx) => ({
    key: `${item.tag}-${idx}`,
    tag: item.tag,
    platform: item.platform,
    scope: item.scope,
    action: 'intercept',
    preview: '',
  }))
}

export function buildFallbackAgyEnvelope(
  sanitizedSystem: string,
  sanitizedUser: string,
  agyMode: AgyPromptMode,
  hasTools: boolean,
): { bridge: string; continuation: string; finalPrompt: string } {
  const mode = (agyMode || 'default_intercept_continue').toLowerCase()
  const nativeHeader =
    mode === 'downstream_only'
      ? ''
      : `[Stage 1 · AGY CLI Native Default Prompt — Preserved Upstream Fingerprint]\n<identity>\nYou are Antigravity (running via authentic agy CLI binary).\n</identity>`

  const toolClause = hasTools
    ? '- Declared downstream tools are active: emit requested tool calls strictly via <AGY_TOOL_CALL> JSON blocks; never invoke local proxy host tools.'
    : '- No external tools declared: answer directly in clean Markdown without host IDE artifacts.'

  const bridge =
    mode === 'native_only'
      ? '<agy_intercept_bridge mode="native_only">\nDownstream system continuation disabled; using native prompt only.\n</agy_intercept_bridge>'
      : `<agy_intercept_bridge mode="${mode}">\n` +
        `- Neutralize IDE-only assumptions: omit <artifacts>, ArtifactMetadata, slash commands (/goal, /plan), <plugin-card>, and host file:/// links.\n` +
        `${toolClause}\n` +
        `- Treat <downstream_continuation> below as the authoritative persona, language, OS/workspace, and task specification.\n` +
        `</agy_intercept_bridge>`

  const continuation =
    mode === 'native_only'
      ? ''
      : `<downstream_continuation>\n${sanitizedSystem.trim() || '(No downstream system prompt provided)'}\n</downstream_continuation>`

  const userBlock = sanitizedUser.trim()
    ? `[Downstream User Turn]\n${sanitizedUser.trim()}`
    : ''

  const parts = [nativeHeader, bridge, continuation, userBlock].filter(Boolean)
  return {
    bridge,
    continuation,
    finalPrompt: parts.join('\n\n'),
  }
}

export function normalizeInterceptPreview(
  raw: FilterPipelinePreviewResponse,
  req: {
    systemPrompt: string
    userMessage: string
    agyMode: AgyPromptMode
    hasTools: boolean
  },
): NormalizedInterceptPreview {
  const originalSystem = raw.original_system ?? raw.system_prompt ?? req.systemPrompt
  const originalUser = raw.original_user ?? raw.user_message ?? req.userMessage

  let sanitizedSystem = raw.sanitized_system ?? raw.filtered_system ?? ''
  let sanitizedUser = raw.sanitized_user ?? raw.filtered_user ?? ''

  if (!sanitizedSystem && !sanitizedUser && raw.output) {
    try {
      const parsed = JSON.parse(raw.output) as Record<string, unknown>
      if (typeof parsed.system === 'string') {
        sanitizedSystem = parsed.system
      } else if (
        Array.isArray(parsed.system) &&
        parsed.system[0] &&
        typeof (parsed.system[0] as Record<string, unknown>).text === 'string'
      ) {
        sanitizedSystem = String((parsed.system[0] as Record<string, unknown>).text)
      } else if (typeof parsed.instructions === 'string') {
        sanitizedSystem = parsed.instructions
      }
      if (Array.isArray(parsed.messages)) {
        for (const m of parsed.messages as Array<Record<string, unknown>>) {
          if (m.role === 'system' && !sanitizedSystem && typeof m.content === 'string') {
            sanitizedSystem = m.content
          }
          if (m.role === 'user' && typeof m.content === 'string') {
            sanitizedUser = m.content
          }
        }
      } else if (Array.isArray(parsed.input)) {
        for (const m of parsed.input as Array<Record<string, unknown>>) {
          if (m.role === 'user' && typeof m.content === 'string') {
            sanitizedUser = m.content
          }
        }
      }
    } catch {
      sanitizedSystem = raw.output
      sanitizedUser = req.userMessage
    }
  }

  if (sanitizedSystem === '' && originalSystem === '') sanitizedSystem = ''
  else if (
    sanitizedSystem === '' &&
    raw.sanitized_system === undefined &&
    raw.filtered_system === undefined &&
    !raw.output
  ) {
    sanitizedSystem = originalSystem
  }

  if (
    sanitizedUser === '' &&
    originalUser !== '' &&
    raw.sanitized_user === undefined &&
    raw.filtered_user === undefined &&
    !raw.output
  ) {
    sanitizedUser = originalUser
  }

  const harnessStrippedSystem = raw.harness_stripped_system ?? sanitizedSystem
  const harnessStrippedUser = raw.harness_stripped_user ?? sanitizedUser

  const hits = raw.matched_rules ?? raw.hits ?? raw.stats?.hits ?? []
  const pruneHits =
    raw.stats?.prune_hits ?? hits.filter((h) => h.kind === 'prune').reduce((s, h) => s + h.count, 0)
  const rewriteHits =
    raw.stats?.rewrite_hits ??
    hits.filter((h) => h.kind === 'rewrite').reduce((s, h) => s + h.count, 0)
  const replaceHits =
    raw.stats?.replace_hits ??
    hits.filter((h) => h.kind === 'replace').reduce((s, h) => s + h.count, 0)

  const rawHarnesses = raw.detected_harnesses ?? raw.stripped_harnesses ?? raw.harness_tags
  const detectedHarnesses: NormalizedHarnessBadge[] = Array.isArray(rawHarnesses)
    ? rawHarnesses.map((entry, idx) => {
        if (typeof entry === 'string') {
          return {
            key: `${entry}-${idx}`,
            tag: entry,
            platform: '',
            scope: '',
            action: 'intercept',
            preview: '',
          }
        }
        return {
          key: `${entry.tag}-${entry.scope ?? idx}`,
          tag: entry.tag,
          platform: entry.platform ?? '',
          scope: entry.scope ?? '',
          action: entry.action ?? 'isolate',
          preview: entry.preview ?? '',
        }
      })
    : detectClientHarnessLabels(originalSystem, originalUser)

  const agyMode = raw.agy_mode || req.agyMode || 'default_intercept_continue'
  const hasTools = raw.has_tools ?? req.hasTools

  const fallback = buildFallbackAgyEnvelope(sanitizedSystem, sanitizedUser, agyMode, hasTools)
  const interceptBridge = raw.interception_bridge || raw.intercept_bridge || fallback.bridge
  const agyContinuationPreview = raw.agy_continuation_preview || fallback.continuation
  const agyFinalPrompt =
    raw.final_prompt ||
    raw.agy_final_prompt ||
    raw.agy_continuation_preview ||
    raw.final_system ||
    fallback.finalPrompt
  const finalUser = raw.final_user ?? sanitizedUser

  const stages: NormalizedInterceptStage[] = Array.isArray(raw.stages)
    ? raw.stages.map((st, idx) => ({
        key: st.key || st.stage || `stage-${idx}`,
        title: st.title || st.label || st.name || `阶段 ${idx + 1}`,
        description: st.description || '',
        systemPrompt: st.system_prompt ?? st.content ?? st.output ?? '',
        userMessage: st.user_message ?? '',
        changed: Boolean(st.changed),
      }))
    : []

  const changed =
    raw.changed ??
    (sanitizedSystem !== originalSystem ||
      sanitizedUser !== originalUser ||
      hits.length > 0 ||
      detectedHarnesses.length > 0)

  const hasZeroWidth =
    sanitizedSystem.includes('\u200b') ||
    sanitizedUser.includes('\u200b') ||
    agyFinalPrompt.includes('\u200b')

  return {
    originalSystem,
    originalUser,
    harnessStrippedSystem,
    harnessStrippedUser,
    sanitizedSystem,
    sanitizedUser,
    detectedHarnesses,
    agyMode,
    hasTools,
    interceptBridge,
    agyContinuationPreview,
    agyFinalPrompt,
    finalUser,
    stages,
    hits,
    pruneHits,
    rewriteHits,
    replaceHits,
    changed,
    hasZeroWidth,
  }
}
