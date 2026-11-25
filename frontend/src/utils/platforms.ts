// 四大上游平台的展示元数据与归一化工具。
//
// 统一管理 AntiGravity / WorkBuddy / Command Code / OpenCode 的徽标配色、
// 名称、简写、图标、默认路由及密钥校验规则。

import type { AccountMode, UpstreamPlatform } from '@/api/types'
import type { IconName } from '@/components/ui/icons'

/** 平台徽标的配色，取值对应 ui/Badge 的 tone。 */
export type PlatformTone = 'accent' | 'success' | 'info' | 'warning' | 'neutral'

export interface PlatformMeta {
  value: UpstreamPlatform
  label: string
  shortLabel: string
  /** 选择卡片与提示浮层上的一句话说明。 */
  description: string
  /** 密钥前缀要求。空串表示不强制固定前缀。 */
  keyPrefix: string
  /** 密钥输入框的占位文案。 */
  keyPlaceholder: string
  /** 密钥格式提示。 */
  keyHint: string
  /** 是否使用 account_mode / base_url 扩展字段（OpenCode Zen/Go）。 */
  usesMode: boolean
  /** 是否支持容器与登录 JSON 文件池编排（AntiGravity）。 */
  supportsContainer: boolean
  /** 是否支持每日签到与积分奖励收割（WorkBuddy）。 */
  supportsHarvest: boolean
  tone: PlatformTone
  icon: IconName
  routeName: string
}

export const DEFAULT_PLATFORM: UpstreamPlatform = 'workbuddy'

/** 归一化任意平台别名到标准 UpstreamPlatform。 */
export function normalizePlatform(raw: string | null | undefined): UpstreamPlatform {
  const key = (raw ?? '').trim().toLowerCase()
  if (
    key === 'antigravity' ||
    key === 'agy' ||
    key === 'anti-gravity' ||
    key === 'anti_gravity' ||
    key === 'cliproxy' ||
    key === 'cliproxyapi'
  ) {
    return 'antigravity'
  }
  if (key === 'commandcode' || key === 'command-code' || key === 'command_code' || key === 'cc' || key === 'cmdc') {
    return 'commandcode'
  }
  if (key === 'opencode' || key === 'open-code' || key === 'open_code' || key === 'oc') {
    return 'opencode'
  }
  return 'workbuddy'
}

/** 四大核心上游平台元数据表（分组创建、路由筛选、全局选择器统一使用）。 */
export const PLATFORMS: readonly PlatformMeta[] = [
  {
    value: 'workbuddy',
    label: 'WorkBuddy',
    shortLabel: 'WB',
    description: 'WorkBuddy / CodeBuddy 账号池，支持独立代理 IP、成对设备指纹与自动签到收割。',
    keyPrefix: '',
    keyPlaceholder: '粘贴 WorkBuddy Access Token',
    keyHint: '填写有效的 WorkBuddy Access Token；配合 Refresh Token 可自动续期与生成免密登录链接',
    usesMode: false,
    supportsContainer: false,
    supportsHarvest: true,
    tone: 'accent',
    icon: 'layers',
    routeName: 'accounts-workbuddy',
  },
  {
    value: 'antigravity',
    label: 'AntiGravity',
    shortLabel: 'AGY',
    description: 'CLIProxyAPI 双模容器节点与 OAuth JSON 登录文件池（Gemini / Claude / GPT）。',
    keyPrefix: '',
    keyPlaceholder: '粘贴节点推理 API Key 或 OAuth 凭证',
    keyHint: '支持本机 Docker 容器自动编排与远程 CLIProxyAPI 节点统一调度',
    usesMode: false,
    supportsContainer: true,
    supportsHarvest: false,
    tone: 'success',
    icon: 'server',
    routeName: 'antigravity',
  },
  {
    value: 'commandcode',
    label: 'Command Code',
    shortLabel: 'CC',
    description: 'Command Code 账号池，支持独立出口代理、健康巡检与三协议流式转发。',
    keyPrefix: 'user_',
    keyPlaceholder: 'user_xxxxxxxxxxxx',
    keyHint: '密钥以 user_ 开头，支持独立 HTTP / SOCKS5 代理绑定',
    usesMode: false,
    supportsContainer: false,
    supportsHarvest: false,
    tone: 'info',
    icon: 'terminal',
    routeName: 'accounts-commandcode',
  },
  {
    value: 'opencode',
    label: 'OpenCode',
    shortLabel: 'OC',
    description: 'OpenCode Zen / Go 账号池，支持按量计费与订阅窗口双模式调度。',
    keyPrefix: '',
    keyPlaceholder: '粘贴 OpenCode API Key',
    keyHint: '原样粘贴 OpenCode 密钥，可选 Zen 按量付费或 Go 订阅额度模式',
    usesMode: true,
    supportsContainer: false,
    supportsHarvest: false,
    tone: 'warning',
    icon: 'code',
    routeName: 'accounts-opencode',
  },
]

/** 兼容全部平台遍历的别名导出。 */
export const ALL_PLATFORMS: readonly PlatformMeta[] = PLATFORMS

export interface AccountModeMeta {
  value: AccountMode
  label: string
  description: string
  defaultBaseUrl: string
}

export const ACCOUNT_MODES: readonly AccountModeMeta[] = [
  {
    value: 'zen',
    label: 'Zen 按量付费',
    description: '按实际 token 用量计费',
    defaultBaseUrl: 'https://opencode.ai/zen/v1',
  },
  {
    value: 'go',
    label: 'Go 订阅额度',
    description: '消耗 OpenCode Go 订阅额度',
    defaultBaseUrl: 'https://opencode.ai/zen/go/v1',
  },
]

/** 取平台元数据（兼容任意大小写与别名，未命中时回落到 WorkBuddy）。 */
export function platformMeta(platform: string | null | undefined): PlatformMeta {
  const normalized = normalizePlatform(platform)
  return ALL_PLATFORMS.find((item) => item.value === normalized) ?? ALL_PLATFORMS[0]!
}

/** 取平台显示名称。 */
export function platformLabel(platform: string | null | undefined, short = false): string {
  const meta = platformMeta(platform)
  return short ? meta.shortLabel : meta.label
}

/** 取计费模式元数据（OpenCode Zen / Go）。 */
export function accountModeMeta(mode: string): AccountModeMeta {
  return ACCOUNT_MODES.find((item) => item.value === mode) ?? ACCOUNT_MODES[0]!
}

/** 校验单把密钥是否符合对应平台的前缀与非空要求。 */
export function isValidKey(platform: string, key: string): boolean {
  const trimmed = key.trim()
  const prefix = platformMeta(platform).keyPrefix
  return prefix ? trimmed.startsWith(prefix) : trimmed.length > 0
}
