import type { IconName } from './icons'

// 跨组件共享的 UI 层类型。放在 .ts 里是因为 `<script setup>` 不允许导出。
export interface SelectOption {
  label: string
  /** 一律用字符串：option 的 value 在 DOM 里本来就是字符串。 */
  value: string
  disabled?: boolean
}

export interface TabItem {
  value: string
  label: string
  icon?: IconName
  badge?: string | number
  disabled?: boolean
}
