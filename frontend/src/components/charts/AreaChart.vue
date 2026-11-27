<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'

import type { ChartPoint } from './types'

const props = withDefaults(
  defineProps<{
    points: ChartPoint[]
    height?: number
    tone?: 'accent' | 'success' | 'warning' | 'info'
    formatValue?: (value: number) => string
    yTicks?: number
    emptyText?: string
  }>(),
  {
    height: 240,
    tone: 'accent',
    formatValue: (value: number) => String(Math.round(value)),
    yTicks: 4,
    emptyText: '当前时间范围内没有数据',
  },
)

const target = ref<HTMLElement | null>(null)
const width = ref(0)
let observer: ResizeObserver | null = null

function connectObserver(node: HTMLElement | null): void {
  observer?.disconnect()
  observer = null
  if (!node) return
  width.value = node.clientWidth
  if (typeof ResizeObserver === 'undefined') return
  observer = new ResizeObserver((entries) => {
    for (const entry of entries) {
      width.value = Math.floor(entry.contentRect.width)
    }
  })
  observer.observe(node)
}

watch(target, (node) => connectObserver(node), { immediate: true })

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
})

const padding = { top: 16, right: 16, bottom: 26, left: 58 }

const toneClass: Record<string, string> = {
  accent: 'text-accent',
  success: 'text-success',
  warning: 'text-warning',
  info: 'text-info',
}

const uid = useId()
const gradientId = `area-grad-${uid.replace(/[^a-zA-Z0-9_-]/g, '')}`

const hoverIndex = ref<number | null>(null)

function safeNumber(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : 0
}

const normalizedPoints = computed(() =>
  props.points.map((pt) => ({
    label: pt.label ?? '',
    tooltip: pt.tooltip ?? pt.label ?? '',
    value: safeNumber(pt.value),
  })),
)

const hasData = computed(() => normalizedPoints.value.length > 0)
const rawMax = computed(() =>
  normalizedPoints.value.reduce((max, pt) => Math.max(max, pt.value), 0),
)
const allZero = computed(() => hasData.value && rawMax.value <= 0)

const plotWidth = computed(() => Math.max(width.value - padding.left - padding.right, 10))
const plotHeight = computed(() => Math.max(props.height - padding.top - padding.bottom, 10))

function niceMax(value: number, ticks: number): number {
  if (!Number.isFinite(value) || value <= 0) return Math.max(ticks, 4)
  const exponent = Math.floor(Math.log10(value))
  const base = Math.pow(10, exponent)
  const normalized = value / base
  const step =
    normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 2.5 ? 2.5 : normalized <= 5 ? 5 : 10
  const candidate = step * base
  // 若数值为小整数（例如 1~3 次请求），保证上限至少可被刻度整除，杜绝纵轴出现重复刻度标签
  if (candidate < ticks && Number.isInteger(value)) {
    return ticks
  }
  return candidate
}

const tickCount = computed(() => Math.max(props.yTicks, 2))
const maxValue = computed(() => niceMax(rawMax.value, tickCount.value))

function xAt(index: number): number {
  const count = normalizedPoints.value.length
  if (count <= 1) return padding.left + plotWidth.value / 2
  return padding.left + (index / (count - 1)) * plotWidth.value
}

function yAt(value: number): number {
  const ratio = maxValue.value <= 0 ? 0 : Math.min(Math.max(value / maxValue.value, 0), 1)
  return padding.top + plotHeight.value - ratio * plotHeight.value
}

const linePath = computed(() => {
  if (!hasData.value || width.value <= 0) return ''
  const pts = normalizedPoints.value
  if (pts.length === 1) {
    const y = yAt(pts[0]!.value).toFixed(2)
    return `M${padding.left.toFixed(2)} ${y} L${(padding.left + plotWidth.value).toFixed(2)} ${y}`
  }
  return pts
    .map(
      (point, index) =>
        `${index === 0 ? 'M' : 'L'}${xAt(index).toFixed(2)} ${yAt(point.value).toFixed(2)}`,
    )
    .join(' ')
})

const areaPath = computed(() => {
  if (!linePath.value || allZero.value) return ''
  const pts = normalizedPoints.value
  const baseline = (padding.top + plotHeight.value).toFixed(2)
  const first = (pts.length === 1 ? padding.left : xAt(0)).toFixed(2)
  const last = (pts.length === 1 ? padding.left + plotWidth.value : xAt(pts.length - 1)).toFixed(2)
  return `${linePath.value} L${last} ${baseline} L${first} ${baseline} Z`
})

const gridLines = computed(() => {
  const ticks = tickCount.value
  return Array.from({ length: ticks + 1 }, (_, index) => {
    const ratio = index / ticks
    const val = allZero.value ? (index === ticks ? 0 : (ticks - index)) : maxValue.value * (1 - ratio)
    return {
      y: padding.top + plotHeight.value * ratio,
      label: allZero.value && index < ticks ? '' : props.formatValue(val),
    }
  })
})

const xLabels = computed(() => {
  const count = normalizedPoints.value.length
  if (count === 0) return []
  const desired = Math.max(2, Math.min(6, Math.floor(plotWidth.value / 100)))
  const step = Math.max(1, Math.ceil(count / desired))
  const out: { x: number; text: string }[] = []
  for (let index = 0; index < count; index += step) {
    const pt = normalizedPoints.value[index]
    if (pt) out.push({ x: xAt(index), text: pt.label })
  }
  const lastIndex = count - 1
  const lastPt = normalizedPoints.value[lastIndex]
  const prevX = out[out.length - 1]?.x ?? -999
  if (lastPt && xAt(lastIndex) - prevX > 56) {
    out.push({ x: xAt(lastIndex), text: lastPt.label })
  }
  return out
})

const marker = computed(() => {
  const index = hoverIndex.value
  if (index === null) return null
  const point = normalizedPoints.value[index]
  if (!point) return null
  return { x: xAt(index), y: yAt(point.value), point }
})

const tooltipStyle = computed(() => {
  if (!marker.value || width.value <= 0) return {}
  const margin = 84
  const clamped = Math.min(Math.max(marker.value.x, margin), Math.max(width.value - margin, margin))
  return { left: `${clamped}px` }
})

function onPointerMove(event: PointerEvent): void {
  if (!hasData.value) return
  const rect = (event.currentTarget as SVGElement).getBoundingClientRect()
  const x = event.clientX - rect.left
  const count = normalizedPoints.value.length
  if (count <= 1) {
    hoverIndex.value = 0
    return
  }
  const ratio = (x - padding.left) / plotWidth.value
  const index = Math.round(ratio * (count - 1))
  hoverIndex.value = Math.min(Math.max(index, 0), count - 1)
}
</script>

<template>
  <div
    ref="target"
    class="relative min-w-0 w-full overflow-hidden select-none"
    :style="{ height: `${height}px` }"
  >
    <div
      v-if="!hasData"
      class="flex h-full flex-col items-center justify-center gap-1 rounded-md border border-dashed border-line bg-raised/35 px-4 text-center text-2xs text-subtle"
    >
      <span>{{ emptyText }}</span>
    </div>

    <template v-else-if="width > 0">
      <svg
        :viewBox="`0 0 ${width} ${height}`"
        :height="height"
        class="block w-full"
        :class="toneClass[tone]"
        @pointermove="onPointerMove"
        @pointerleave="hoverIndex = null"
      >
        <defs>
          <linearGradient :id="gradientId" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="currentColor" stop-opacity="0.22" />
            <stop offset="100%" stop-color="currentColor" stop-opacity="0.01" />
          </linearGradient>
        </defs>

        <!-- 自适应深浅主题参考网格与纵轴刻度 -->
        <g>
          <template v-for="(line, index) in gridLines" :key="index">
            <line
              :x1="padding.left"
              :x2="width - padding.right"
              :y1="line.y"
              :y2="line.y"
              stroke="rgb(var(--c-line))"
              stroke-width="1"
              :stroke-dasharray="index === gridLines.length - 1 ? undefined : '3 4'"
            />
            <text
              v-if="line.label"
              :x="padding.left - 8"
              :y="line.y + 3.5"
              text-anchor="end"
              fill="rgb(var(--c-subtle))"
              class="tnum font-mono text-[10px]"
            >
              {{ line.label }}
            </text>
          </template>
        </g>

        <path v-if="areaPath" :d="areaPath" :fill="`url(#${gradientId})`" />
        <path
          :d="linePath"
          fill="none"
          stroke="currentColor"
          :stroke-opacity="allZero ? 0.45 : 1"
          stroke-width="1.8"
          stroke-linejoin="round"
          stroke-linecap="round"
        />

        <!-- 单数据点时绘制显式圆点，避免视觉空白 -->
        <circle
          v-if="normalizedPoints.length === 1 && !allZero"
          :cx="xAt(0)"
          :cy="yAt(normalizedPoints[0]!.value)"
          r="3.5"
          fill="currentColor"
          stroke="rgb(var(--c-surface))"
          stroke-width="2"
        />

        <g v-if="marker">
          <line
            :x1="marker.x"
            :x2="marker.x"
            :y1="padding.top"
            :y2="padding.top + plotHeight"
            stroke="rgb(var(--c-line-strong))"
            stroke-width="1"
            stroke-dasharray="2 2"
          />
          <circle
            :cx="marker.x"
            :cy="marker.y"
            r="4"
            fill="currentColor"
            stroke="rgb(var(--c-surface))"
            stroke-width="2"
          />
        </g>

        <g>
          <text
            v-for="(label, index) in xLabels"
            :key="index"
            :x="label.x"
            :y="height - 6"
            text-anchor="middle"
            fill="rgb(var(--c-subtle))"
            class="tnum font-mono text-[10px]"
          >
            {{ label.text }}
          </text>
        </g>
      </svg>

      <!-- 全零序列轻提示：保留时间轴网格同时明确告知无波动 -->
      <div
        v-if="allZero && !marker"
        class="pointer-events-none absolute inset-0 flex items-center justify-center pb-4"
      >
        <span
          class="rounded-md border border-line bg-surface/90 px-2.5 py-1 text-2xs text-subtle shadow-panel"
        >
          {{ emptyText }}
        </span>
      </div>

      <div
        v-if="marker"
        class="pointer-events-none absolute top-1.5 z-10 -translate-x-1/2 whitespace-nowrap rounded-md border border-line-strong bg-surface/95 px-2.5 py-1.5 shadow-pop backdrop-blur-sm"
        :style="tooltipStyle"
      >
        <p class="font-mono text-[10px] text-subtle">
          {{ marker.point.tooltip || marker.point.label }}
        </p>
        <p class="tnum mt-0.5 text-xs font-semibold text-fg">
          {{ formatValue(marker.point.value) }}
        </p>
      </div>
    </template>
  </div>
</template>
