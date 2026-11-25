<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Icon from '@/components/ui/Icon.vue'
import Modal from '@/components/ui/Modal.vue'
import type { HarvestAllResult, HarvestStep, HarvestSummary } from '@/api/types'
import { formatCredits, formatDateTime } from '@/utils/format'

const props = defineProps<{
  open: boolean
  title: string
  subtitle?: string
  summary: HarvestSummary | null
  totalCredits?: number | null
  harvestedAt?: string | null
  batchResult?: HarvestAllResult | null
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
}>()

const selectedAccountId = ref<number | null>(null)

watch(
  () => [props.open, props.batchResult] as const,
  ([isOpen, batch]) => {
    if (!isOpen) return
    selectedAccountId.value = batch?.items?.[0]?.id ?? null
  },
  { immediate: true },
)

const activeBatchItem = computed(() => {
  if (!props.batchResult?.items?.length) return null
  return (
    props.batchResult.items.find((item) => item.id === selectedAccountId.value) ??
    props.batchResult.items[0] ??
    null
  )
})

const displaySteps = computed<HarvestStep[]>(() => {
  if (activeBatchItem.value) return activeBatchItem.value.steps ?? []
  return props.summary?.steps ?? []
})

function stepTone(kind: string): 'success' | 'neutral' | 'danger' {
  if (kind === 'ok') return 'success'
  if (kind === 'fail') return 'danger'
  return 'neutral'
}

function stepLabel(kind: string): string {
  if (kind === 'ok') return '完成'
  if (kind === 'fail') return '失败'
  return '跳过'
}

function close(): void {
  emit('update:open', false)
}
</script>

<template>
  <Modal
    :open="open"
    :title="title"
    :subtitle="subtitle"
    size="lg"
    @update:open="emit('update:open', $event)"
  >
    <div class="space-y-4">
      <!-- 全量收割汇总 -->
      <template v-if="batchResult">
        <div class="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
          <div class="rounded-md border border-line bg-raised/40 px-3 py-2">
            <p class="text-2xs text-subtle">处理账号</p>
            <p class="tnum mt-0.5 text-base font-semibold text-fg">{{ batchResult.total }}</p>
          </div>
          <div class="rounded-md border border-line bg-raised/40 px-3 py-2">
            <p class="text-2xs text-subtle">成功 / 失败</p>
            <p class="tnum mt-0.5 text-base font-semibold">
              <span class="text-success">{{ batchResult.succeeded }}</span>
              <span class="text-subtle"> / </span>
              <span :class="batchResult.failed > 0 ? 'text-danger' : 'text-muted'">
                {{ batchResult.failed }}
              </span>
            </p>
          </div>
          <div class="col-span-2 rounded-md border border-line bg-raised/40 px-3 py-2">
            <p class="text-2xs text-subtle">本次累计收割积分</p>
            <p class="tnum mt-0.5 text-base font-semibold text-accent">
              +{{ formatCredits(batchResult.credit_gained) }}
            </p>
          </div>
        </div>

        <div class="flex flex-wrap gap-1.5">
          <button
            v-for="item in batchResult.items"
            :key="item.id"
            type="button"
            class="flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-2xs transition"
            :class="
              activeBatchItem?.id === item.id
                ? 'border-accent bg-accent/12 text-accent'
                : 'border-line bg-raised/40 text-muted hover:text-fg'
            "
            @click="selectedAccountId = item.id"
          >
            <span
              class="h-1.5 w-1.5 rounded-full"
              :class="item.ok ? 'bg-success' : 'bg-danger'"
            />
            <span class="font-medium">{{ item.name }}</span>
            <span class="tnum">+{{ formatCredits(item.credit_gained) }}</span>
          </button>
        </div>

        <p
          v-if="activeBatchItem?.error"
          class="flex items-start gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="13" class="mt-px shrink-0" />
          <span>{{ activeBatchItem.error }}</span>
        </p>
      </template>

      <!-- 单账号收割摘要 -->
      <template v-else-if="summary">
        <div class="flex flex-wrap items-center justify-between gap-3 rounded-md border border-line bg-raised/50 px-3.5 py-2.5">
          <div>
            <p class="text-2xs text-subtle">本次新增积分</p>
            <p class="tnum text-lg font-semibold text-accent">
              +{{ formatCredits(summary.credit_gained ?? 0) }}
            </p>
          </div>
          <div v-if="totalCredits !== undefined && totalCredits !== null" class="text-right">
            <p class="text-2xs text-subtle">收割后总积分</p>
            <p class="tnum text-base font-semibold text-fg">{{ formatCredits(totalCredits) }}</p>
          </div>
          <div v-if="harvestedAt" class="text-right">
            <p class="text-2xs text-subtle">执行时间</p>
            <p class="text-2xs text-muted">{{ formatDateTime(harvestedAt) }}</p>
          </div>
        </div>

        <p
          v-if="summary.error"
          class="flex items-start gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="13" class="mt-px shrink-0" />
          <span>{{ summary.error }}</span>
        </p>
      </template>

      <!-- 步骤明细 -->
      <div v-if="displaySteps.length > 0" class="scroll-thin max-h-72 overflow-y-auto rounded-md border border-line">
        <table class="w-full border-collapse text-left">
          <thead class="sticky top-0 border-b border-line bg-surface">
            <tr>
              <th class="th">步骤</th>
              <th class="th w-20">状态</th>
              <th class="th w-24 text-right">积分</th>
              <th class="th">详情</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-line">
            <tr v-for="(step, index) in displaySteps" :key="`${step.name}-${index}`">
              <td class="td font-mono text-2xs text-fg">{{ step.name }}</td>
              <td class="td">
                <Badge :tone="stepTone(step.kind)">{{ stepLabel(step.kind) }}</Badge>
              </td>
              <td class="td tnum text-right" :class="step.credit > 0 ? 'font-medium text-success' : 'text-subtle'">
                {{ step.credit > 0 ? `+${formatCredits(step.credit)}` : '0' }}
              </td>
              <td class="td max-w-[18rem] whitespace-normal break-words text-2xs text-muted">
                {{ step.detail || '—' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="rounded-md border border-line bg-raised/30 px-3 py-6 text-center text-2xs text-subtle">
        暂无步骤执行明细
      </p>
    </div>

    <template #footer>
      <Button variant="ghost" size="md" @click="close">关闭</Button>
    </template>
  </Modal>
</template>
