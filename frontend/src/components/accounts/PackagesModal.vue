<script setup lang="ts">
import { computed } from 'vue'

import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Icon from '@/components/ui/Icon.vue'
import Modal from '@/components/ui/Modal.vue'
import type { Account, AccountCreditPackage } from '@/api/types'
import { formatCredits, formatDateTime, formatExpiry, formatRelative, isExpired } from '@/utils/format'

const props = defineProps<{
  open: boolean
  account: Account | null
  refreshing: boolean
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'refresh', account: Account): void
}>()

const totalCredits = computed(() => {
  const bal = props.account?.balance
  if (!bal) return null
  return bal.total_credits ?? bal.remaining ?? null
})

const packages = computed<AccountCreditPackage[]>(() => props.account?.balance?.packages ?? [])

function packageRemainPercent(pkg: AccountCreditPackage): number {
  if (!pkg.total || pkg.total <= 0) return 0
  return Math.min(100, Math.max(0, (pkg.remain / pkg.total) * 100))
}

function packageTone(pkg: AccountCreditPackage): string {
  const pct = packageRemainPercent(pkg)
  if (pct <= 10) return 'bg-danger'
  if (pct <= 30) return 'bg-warning'
  return 'bg-accent'
}

function close(): void {
  emit('update:open', false)
}
</script>

<template>
  <Modal
    :open="open"
    title="积分与资源包明细"
    :subtitle="account ? `${account.name}${account.uid ? ` · UID ${account.uid}` : ''}` : ''"
    size="lg"
    @update:open="emit('update:open', $event)"
  >
    <div v-if="account" class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3 rounded-md border border-line bg-raised/50 px-3.5 py-3">
        <div>
          <p class="text-2xs text-subtle">当前可用总积分 (TotalCredits)</p>
          <div class="mt-0.5 flex items-baseline gap-2">
            <span class="tnum text-xl font-semibold text-fg">
              {{ formatCredits(totalCredits) }}
            </span>
            <Badge v-if="account.balance?.plan_name" tone="accent">
              {{ account.balance.plan_name }}
            </Badge>
          </div>
          <p class="mt-1 text-2xs text-subtle">
            最近同步：{{ account.balance?.fetched_at ? `${formatRelative(account.balance.fetched_at)}（${formatDateTime(account.balance.fetched_at)}）` : '尚未同步' }}
          </p>
        </div>

        <Button
          variant="secondary"
          size="sm"
          :loading="refreshing"
          @click="emit('refresh', account)"
        >
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新积分
        </Button>
      </div>

      <p
        v-if="account.balance?.error"
        class="flex items-start gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs text-danger"
      >
        <Icon name="alertCircle" :size="13" class="mt-px shrink-0" />
        <span>最近刷新异常：{{ account.balance.error }}</span>
      </p>

      <div v-if="packages.length === 0" class="rounded-md border border-line bg-raised/30 px-4 py-8 text-center">
        <p class="text-[13px] font-medium text-muted">暂无资源包明细</p>
        <p class="mt-1 text-2xs text-subtle">
          点击右上角「刷新积分」从 WorkBuddy 账单接口拉取最新的 Packages 列表。
        </p>
      </div>

      <div v-else class="scroll-thin max-h-80 space-y-2.5 overflow-y-auto pr-1">
        <div
          v-for="(pkg, idx) in packages"
          :key="`${pkg.package_code}-${idx}`"
          class="rounded-md border border-line bg-raised/35 p-3 space-y-2"
        >
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-1.5">
                <span class="text-[13px] font-medium text-fg">
                  {{ pkg.package_name || pkg.package_code || '资源包' }}
                </span>
                <Badge v-if="pkg.kind" tone="info">{{ pkg.kind }}</Badge>
                <Badge v-if="pkg.package_code" tone="neutral" mono>{{ pkg.package_code }}</Badge>
              </div>
              <p
                class="mt-0.5 text-2xs"
                :class="pkg.expired_at && isExpired(pkg.expired_at) ? 'text-danger' : 'text-subtle'"
              >
                到期时间：{{ formatExpiry(pkg.expired_at) }}
              </p>
            </div>

            <div class="text-right">
              <p class="tnum text-[13px] font-semibold text-fg">
                {{ formatCredits(pkg.remain) }}
                <span class="text-2xs font-normal text-subtle">/ {{ formatCredits(pkg.total) }}</span>
              </p>
              <p v-if="pkg.total > 0" class="tnum text-2xs text-subtle">
                剩余 {{ packageRemainPercent(pkg).toFixed(1) }}%
              </p>
            </div>
          </div>

          <div v-if="pkg.total > 0" class="h-1.5 w-full overflow-hidden rounded-full bg-raised">
            <div
              class="h-full rounded-full"
              :class="packageTone(pkg)"
              :style="{ width: `${packageRemainPercent(pkg)}%` }"
            />
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <Button variant="ghost" size="md" @click="close">关闭</Button>
    </template>
  </Modal>
</template>
