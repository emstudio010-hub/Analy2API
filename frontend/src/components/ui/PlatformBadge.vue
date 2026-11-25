<script setup lang="ts">
import { computed } from 'vue'

import { platformMeta } from '@/utils/platforms'

import Badge from './Badge.vue'
import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{
    platform?: string | null
    /** 显示紧凑简写（WB / AGY / CC / OC）。 */
    short?: boolean
    /** 显示平台图标。 */
    showIcon?: boolean
    dot?: boolean
  }>(),
  { platform: 'workbuddy', short: false, showIcon: true, dot: false },
)

const meta = computed(() => platformMeta(props.platform))
const label = computed(() => (props.short ? meta.value.shortLabel : meta.value.label))
</script>

<template>
  <Badge :tone="meta.tone" :dot="dot" :title="meta.description">
    <Icon v-if="showIcon" :name="meta.icon" :size="11" />
    <span>{{ label }}</span>
  </Badge>
</template>
