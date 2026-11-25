<script setup lang="ts">
import { computed } from 'vue'

import Badge from './Badge.vue'

// 统一映射账号、密钥、Docker 容器节点与 Cron 任务状态。
const props = withDefaults(
  defineProps<{
    status?: string | null
    /** 覆盖默认文案，例如账号用「正常」而不是「启用」。 */
    label?: string
    dot?: boolean
    fallback?: string
  }>(),
  { status: '', label: '', dot: true, fallback: '未知' },
)

type Tone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info'

const table: Record<string, { tone: Tone; label: string }> = {
  active: { tone: 'success', label: '启用' },
  enabled: { tone: 'success', label: '启用' },
  ok: { tone: 'success', label: '正常' },
  success: { tone: 'success', label: '成功' },
  running: { tone: 'success', label: '运行中' },
  online: { tone: 'success', label: '在线' },
  disabled: { tone: 'neutral', label: '已禁用' },
  inactive: { tone: 'neutral', label: '未启用' },
  stopped: { tone: 'neutral', label: '已停止' },
  exited: { tone: 'neutral', label: '已退出' },
  offline: { tone: 'warning', label: '离线' },
  partial: { tone: 'warning', label: '部分成功' },
  expired: { tone: 'warning', label: '已过期' },
  warning: { tone: 'warning', label: '警告' },
  starting: { tone: 'info', label: '启动中' },
  pulling: { tone: 'info', label: '拉取中' },
  error: { tone: 'danger', label: '异常' },
  failed: { tone: 'danger', label: '失败' },
}

const entry = computed(() => {
  const key = (props.status ?? '').toLowerCase()
  return table[key] ?? null
})

const tone = computed<Tone>(() => entry.value?.tone ?? 'neutral')
const text = computed(() => props.label || entry.value?.label || props.status || props.fallback)
</script>

<template>
  <Badge :tone="tone" :dot="dot">{{ text }}</Badge>
</template>
