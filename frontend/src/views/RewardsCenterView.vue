<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { accountsApi, fetchRewardsOverview, runRewardsAction } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type {
  Account,
  AccountBalanceWindow,
  RewardsPlatformSummary,
  RewardsRunItem,
  RunRewardsActionResponse,
  UpstreamPlatform,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Icon from '@/components/ui/Icon.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Skeleton from '@/components/ui/Skeleton.vue'
import StatusPill from '@/components/ui/StatusPill.vue'
import { useToastStore } from '@/stores/toast'
import {
  formatCredits,
  formatDateTime,
  formatDuration,
  formatMoney,
  formatRelative,
  isTodayDate,
} from '@/utils/format'
import { normalizePlatform } from '@/utils/platforms'

const toast = useToastStore()

const loading = ref(true)
const error = ref('')

const allAccounts = ref<Account[]>([])
const platformSummaries = ref<RewardsPlatformSummary[]>([])
const doChat = ref(false)

const activePanel = ref('all')
const panelTabs = computed(() => [
  { value: 'all', label: '全平台总览', icon: 'layers' as const },
  {
    value: 'workbuddy',
    label: 'WorkBuddy 领奖台',
    icon: 'gift' as const,
    badge: wbAccounts.value.length,
  },
  {
    value: 'commandcode',
    label: 'Command Code 额度保活',
    icon: 'terminal' as const,
    badge: ccAccounts.value.length,
  },
  {
    value: 'agy_oc',
    label: 'AGY & OpenCode 巡检',
    icon: 'server' as const,
    badge: agyAccounts.value.length + ocAccounts.value.length,
  },
])

// 正在执行的动作锁（支持区分全局动作和单号动作）
const runningKey = ref<string | null>(null)
const lastRunResult = ref<RunRewardsActionResponse | null>(null)

// ---- 按平台切分账号列表 ----
const wbAccounts = computed(() =>
  allAccounts.value.filter((a) => normalizePlatform(a.platform) === 'workbuddy'),
)
const ccAccounts = computed(() =>
  allAccounts.value.filter((a) => normalizePlatform(a.platform) === 'commandcode'),
)
const agyAccounts = computed(() =>
  allAccounts.value.filter((a) => normalizePlatform(a.platform) === 'antigravity'),
)
const ocAccounts = computed(() =>
  allAccounts.value.filter((a) => normalizePlatform(a.platform) === 'opencode'),
)

// ---- 四大平台派生统计 ----
const wbStats = computed(() => {
  const list = wbAccounts.value
  const active = list.filter((a) => a.status === 'active').length
  const checkedToday = list.filter((a) => isTodayDate(a.last_checkin_date)).length
  const uncheckedToday = Math.max(0, active - checkedToday)
  const maxStreak = list.reduce((max, a) => Math.max(max, a.checkin_streak || 0), 0)
  const totalCredits = list.reduce(
    (sum, a) => sum + (a.balance?.total_credits ?? a.balance?.remaining ?? 0),
    0,
  )
  return {
    total: list.length,
    active,
    checkedToday,
    uncheckedToday,
    maxStreak,
    totalCredits,
  }
})

const ccStats = computed(() => {
  const list = ccAccounts.value
  const active = list.filter((a) => a.status === 'active').length
  const totalRemaining = list.reduce((sum, a) => sum + (a.balance?.remaining ?? 0), 0)
  const totalCap = list.reduce((sum, a) => sum + (a.balance?.total ?? 0), 0)
  const windowExceeded = list.filter(
    (a) => a.balance?.five_hour?.exceeded || a.balance?.weekly?.exceeded,
  ).length
  return { total: list.length, active, totalRemaining, totalCap, windowExceeded }
})

const agyStats = computed(() => {
  const summaryFromApi = platformSummaries.value.find(
    (s) => normalizePlatform(s.platform) === 'antigravity',
  )
  const list = agyAccounts.value
  const active = summaryFromApi?.active_accounts ?? list.filter((a) => a.status === 'active').length
  const total = summaryFromApi?.total_accounts ?? list.length
  const errored = summaryFromApi?.error_accounts ?? list.filter((a) => a.status === 'error').length
  return { total, active, errored }
})

const ocStats = computed(() => {
  const list = ocAccounts.value
  const active = list.filter((a) => a.status === 'active').length
  const zenCount = list.filter((a) => (a.account_mode || 'zen') === 'zen').length
  const goCount = list.filter((a) => a.account_mode === 'go').length
  return { total: list.length, active, zenCount, goCount }
})

function windowRatio(win?: AccountBalanceWindow | null): number {
  if (!win || !win.cap || win.cap <= 0) return 0
  return Math.min(100, Math.max(0, (win.used / win.cap) * 100))
}

function windowBarClass(win?: AccountBalanceWindow | null): string {
  if (!win) return 'bg-subtle'
  if (win.exceeded || windowRatio(win) >= 95) return 'bg-danger'
  if (windowRatio(win) >= 75) return 'bg-warning'
  return 'bg-info'
}

async function loadOverview(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await fetchRewardsOverview()
    const rawAny = res as unknown as Record<string, unknown>
    platformSummaries.value = res.by_platform ?? []

    if (Array.isArray(res.accounts) && res.accounts.length > 0) {
      allAccounts.value = res.accounts
    } else {
      // 兼容后端按平台字段拆分返回或空列表时回退拉取账号池全貌
      const merged: Account[] = []
      for (const k of ['workbuddy_accounts', 'commandcode_accounts', 'antigravity_accounts', 'opencode_accounts']) {
        const arr = rawAny[k]
        if (Array.isArray(arr)) merged.push(...(arr as Account[]))
      }
      if (merged.length > 0) {
        allAccounts.value = merged
      } else {
        const listRes = await accountsApi.list({ page: 1, page_size: 200 })
        allAccounts.value = listRes.items ?? []
      }
    }
  } catch (err) {
    // 若 /rewards/overview 尚未就绪或失败，尝试回退读取 /accounts 保证页面可查看账号状态
    try {
      const fallback = await accountsApi.list({ page: 1, page_size: 200 })
      allAccounts.value = fallback.items ?? []
      error.value = toMessage(err, '领奖概览接口返回异常，已展示基础账号池快照')
    } catch (innerErr) {
      error.value = toMessage(innerErr, '加载领奖与保活中心数据失败')
      allAccounts.value = []
    }
  } finally {
    loading.value = false
  }
}

async function triggerAction(opts: {
  key: string
  platform: UpstreamPlatform
  action: string
  accountIds?: number[]
  label: string
}): Promise<void> {
  if (runningKey.value) return
  runningKey.value = opts.key
  try {
    const res = await runRewardsAction({
      platform: opts.platform,
      action: opts.action,
      account_ids: opts.accountIds,
      do_chat: opts.platform === 'workbuddy' ? doChat.value : undefined,
    })
    lastRunResult.value = res
    const gained = res.credit_gained ?? res.credits_gained ?? 0
    const gainText = gained > 0 ? `，新增 +${formatCredits(gained)} 积分` : ''
    if (res.failed > 0 && res.succeeded > 0) {
      toast.warning(
        `${opts.label}部分完成`,
        `成功 ${res.succeeded} / 失败 ${res.failed}${gainText}`,
      )
    } else if (res.failed > 0 && res.succeeded === 0) {
      toast.error(`${opts.label}执行失败`, res.summary || `共 ${res.failed} 个目标异常`)
    } else {
      toast.success(
        `${opts.label}执行完成`,
        `成功 ${res.succeeded} / 共 ${res.total}${gainText}`,
      )
    }
    await loadOverview()
  } catch (err) {
    toast.error(`${opts.label}失败`, toMessage(err))
  } finally {
    runningKey.value = null
  }
}

const resultItems = computed<RewardsRunItem[]>(() => lastRunResult.value?.items ?? [])

onMounted(() => {
  void loadOverview()
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部四大平台状态汇总卡 -->
    <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <MetricTile
        label="WorkBuddy · 签到与积分池"
        :value="`${formatCredits(wbStats.totalCredits)} 积分`"
        :hint="`今日已签 ${wbStats.checkedToday} · 未签 ${wbStats.uncheckedToday} · 最高连签 ${wbStats.maxStreak} 天`"
        icon="gift"
        tone="accent"
        :loading="loading && allAccounts.length === 0"
      />
      <MetricTile
        label="Command Code · 额度与会话窗口"
        :value="formatMoney(ccStats.totalRemaining)"
        :hint="`启用 ${ccStats.active}/${ccStats.total} · 窗口超限 ${ccStats.windowExceeded} 个`"
        icon="terminal"
        :tone="ccStats.windowExceeded > 0 ? 'warning' : 'info'"
        :loading="loading && allAccounts.length === 0"
      />
      <MetricTile
        label="AntiGravity · 容器 OAuth 凭证"
        :value="`${agyStats.active} / ${agyStats.total} 活跃`"
        :hint="agyStats.errored > 0 ? `${agyStats.errored} 个凭证异常待刷新` : '支持容器与本地 JSON OAuth 巡检'"
        icon="server"
        :tone="agyStats.errored > 0 ? 'danger' : 'success'"
        :loading="loading && allAccounts.length === 0"
      />
      <MetricTile
        label="OpenCode · Zen / Go 保活池"
        :value="`${ocStats.active} / ${ocStats.total} 在线`"
        :hint="`Zen 按量 ${ocStats.zenCount} · Go 订阅 ${ocStats.goCount}`"
        icon="code"
        tone="warning"
        :loading="loading && allAccounts.length === 0"
      />
    </div>

    <!-- 分区导航与全局工具栏 -->
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <SegmentedTabs v-model="activePanel" :items="panelTabs" />

      <div class="flex flex-wrap items-center gap-2">
        <RouterLink :to="{ name: 'cron-jobs' }">
          <Button variant="secondary" size="sm">
            <template #leading><Icon name="clock" :size="13" /></template>
            配置 Cron 定时自动化
          </Button>
        </RouterLink>
        <Button variant="secondary" size="sm" :loading="loading" @click="loadOverview">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新概览
        </Button>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-2xs text-warning"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="loadOverview">重试</Button>
    </p>

    <!-- 本次批量/单号操作实时回执面板 -->
    <Card
      v-if="lastRunResult"
      title="最近一次任务执行回执明细"
      :subtitle="
        lastRunResult.summary ||
        `动作 ${lastRunResult.action} · 共 ${lastRunResult.total} 个目标，成功 ${lastRunResult.succeeded}，失败 ${lastRunResult.failed}`
      "
      body-class="p-3.5 space-y-2.5"
    >
      <template #actions>
        <div class="flex items-center gap-2">
          <Badge :tone="lastRunResult.failed > 0 ? 'warning' : 'success'" dot>
            成功 {{ lastRunResult.succeeded }} / {{ lastRunResult.total }}
          </Badge>
          <Badge
            v-if="(lastRunResult.credit_gained ?? lastRunResult.credits_gained ?? 0) > 0"
            tone="success"
          >
            +{{ formatCredits(lastRunResult.credit_gained ?? lastRunResult.credits_gained ?? 0) }} 积分
          </Badge>
          <Button variant="ghost" size="xs" @click="lastRunResult = null">收起回执</Button>
        </div>
      </template>

      <div
        v-if="resultItems.length > 0"
        class="scroll-thin max-h-60 overflow-auto rounded-md border border-line bg-raised/40"
      >
        <table class="w-full border-collapse text-left">
          <thead class="border-b border-line bg-surface">
            <tr>
              <th class="th">账号</th>
              <th class="th">平台</th>
              <th class="th">状态</th>
              <th class="th text-right">耗时 / 延迟</th>
              <th class="th text-right">收益 / 余额</th>
              <th class="th">步骤与输出摘要</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-line">
            <tr v-for="(it, idx) in resultItems" :key="`${it.account_id || it.id || idx}`">
              <td class="td font-medium text-fg">
                {{ it.account_name || it.name || `#${it.account_id || it.id}` }}
              </td>
              <td class="td">
                <PlatformBadge :platform="it.platform" short />
              </td>
              <td class="td">
                <Badge v-if="it.skipped" tone="neutral">已跳过</Badge>
                <Badge v-else-if="it.ok" tone="success" dot>成功</Badge>
                <Badge v-else tone="danger" dot>失败</Badge>
              </td>
              <td class="td tnum text-right font-mono text-2xs text-muted">
                {{ formatDuration(it.duration_ms ?? it.latency_ms) }}
              </td>
              <td class="td tnum text-right text-xs">
                <span v-if="it.credit_gained" class="font-semibold text-success">
                  +{{ formatCredits(it.credit_gained) }}
                </span>
                <span v-else-if="it.total_credits != null" class="text-muted">
                  {{ formatCredits(it.total_credits) }}
                </span>
                <span v-else class="text-subtle">—</span>
              </td>
              <td class="td max-w-[26rem] whitespace-normal text-2xs">
                <span v-if="it.error" class="text-danger">{{ it.error }}</span>
                <span v-else-if="it.detail" class="text-muted">{{ it.detail }}</span>
                <div v-if="it.steps?.length" class="mt-1 flex flex-wrap gap-1">
                  <Badge
                    v-for="st in it.steps"
                    :key="st.name"
                    :tone="st.kind === 'ok' ? 'success' : st.kind === 'skip' ? 'neutral' : 'danger'"
                  >
                    {{ st.name }}<template v-if="st.credit > 0"> +{{ st.credit }}</template>
                  </Badge>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </Card>

    <!-- ==================== 1. WorkBuddy 积分收割领奖台 ==================== -->
    <Card
      v-if="activePanel === 'all' || activePanel === 'workbuddy'"
      title="WorkBuddy 积分收割领奖台"
      :subtitle="`今日已签 ${wbStats.checkedToday} 个 · 待签 ${wbStats.uncheckedToday} 个 · 最高连签 ${wbStats.maxStreak} 天 · 池内总计 ${formatCredits(wbStats.totalCredits)} 积分`"
      body-class="p-0"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-3">
          <Checkbox
            v-model="doChat"
            label="附带对话激励打卡 (do_chat)"
          />
          <Button
            variant="secondary"
            size="sm"
            :disabled="wbAccounts.length === 0 || Boolean(runningKey)"
            :loading="runningKey === 'wb-token'"
            @click="
              triggerAction({
                key: 'wb-token',
                platform: 'workbuddy',
                action: 'token_refresh',
                label: 'WorkBuddy 批量续期 Token',
              })
            "
          >
            <template #leading><Icon name="rotate" :size="13" /></template>
            全池续期 Token
          </Button>
          <Button
            variant="primary"
            size="sm"
            :disabled="wbAccounts.length === 0 || Boolean(runningKey)"
            :loading="runningKey === 'wb-harvest-all'"
            @click="
              triggerAction({
                key: 'wb-harvest-all',
                platform: 'workbuddy',
                action: 'harvest',
                label: 'WorkBuddy 全池签到领奖',
              })
            "
          >
            <template #leading><Icon name="gift" :size="13" /></template>
            一键全池签到领奖
          </Button>
        </div>
      </template>

      <DataTable
        :columns="7"
        :loading="loading"
        :empty="wbAccounts.length === 0"
        min-width="1060px"
        hoverable
        class="border-0 rounded-none"
      >
        <template #head>
          <th class="th">WorkBuddy 账号</th>
          <th class="th">状态</th>
          <th class="th">连签进度</th>
          <th class="th">最后签到日期</th>
          <th class="th text-right">当前可用积分</th>
          <th class="th">最近收割摘要</th>
          <th class="th text-right">单号操作</th>
        </template>

        <template #empty>
          <EmptyState
            icon="gift"
            title="暂无 WorkBuddy 账号"
            description="前往 WorkBuddy 账号池导入或本机扫描账号后，即可在此统一执行每日签到、盲盒与激励收割。"
          >
            <RouterLink :to="{ name: 'accounts-workbuddy' }">
              <Button variant="secondary" size="sm">前往 WorkBuddy 账号池</Button>
            </RouterLink>
          </EmptyState>
        </template>

        <template #body>
          <tr v-for="acc in wbAccounts" :key="acc.id">
            <td class="td max-w-[14rem]">
              <div class="flex flex-col gap-0.5">
                <span class="truncate font-medium text-fg">{{ acc.name }}</span>
                <span class="truncate font-mono text-2xs text-subtle">
                  {{ acc.email || acc.uid || `#${acc.id}` }}
                </span>
              </div>
            </td>

            <td class="td">
              <StatusPill :status="acc.status" :label="acc.status === 'active' ? '正常' : undefined" />
            </td>

            <td class="td">
              <Badge :tone="acc.checkin_streak > 0 ? 'accent' : 'neutral'" mono>
                连签 {{ acc.checkin_streak || 0 }} 天
              </Badge>
            </td>

            <td class="td">
              <div class="flex items-center gap-1.5">
                <Badge v-if="isTodayDate(acc.last_checkin_date)" tone="success" dot>今日已签</Badge>
                <Badge v-else tone="warning" dot>今日待签</Badge>
                <span class="font-mono text-2xs text-muted">{{ acc.last_checkin_date || '从未' }}</span>
              </div>
            </td>

            <td class="td tnum text-right font-mono font-semibold text-fg">
              {{ formatCredits(acc.balance?.total_credits ?? acc.balance?.remaining) }}
            </td>

            <td class="td max-w-[22rem]">
              <div v-if="acc.last_harvest_summary" class="space-y-1">
                <div class="flex flex-wrap items-center gap-1.5 text-2xs">
                  <span
                    v-if="acc.last_harvest_summary.credit_gained"
                    class="tnum font-semibold text-success"
                  >
                    +{{ formatCredits(acc.last_harvest_summary.credit_gained) }}
                  </span>
                  <span v-if="acc.last_harvest_at" class="text-subtle">
                    {{ formatRelative(acc.last_harvest_at) }}
                  </span>
                  <span
                    v-if="acc.last_harvest_summary.error"
                    class="truncate text-danger"
                    :title="acc.last_harvest_summary.error"
                  >
                    {{ acc.last_harvest_summary.error }}
                  </span>
                </div>
                <div v-if="acc.last_harvest_summary.steps?.length" class="flex flex-wrap gap-1">
                  <Badge
                    v-for="step in acc.last_harvest_summary.steps.slice(0, 4)"
                    :key="step.name"
                    :tone="step.kind === 'ok' ? 'success' : step.kind === 'skip' ? 'neutral' : 'danger'"
                  >
                    {{ step.name }}
                  </Badge>
                </div>
              </div>
              <span v-else class="text-2xs text-subtle">尚未执行收割</span>
            </td>

            <td class="td text-right">
              <Button
                variant="secondary"
                size="xs"
                :disabled="Boolean(runningKey)"
                :loading="runningKey === `wb-${acc.id}`"
                @click="
                  triggerAction({
                    key: `wb-${acc.id}`,
                    platform: 'workbuddy',
                    action: 'harvest',
                    accountIds: [acc.id],
                    label: `账号「${acc.name}」签到领奖`,
                  })
                "
              >
                <template #leading><Icon name="gift" :size="12" /></template>
                立即领奖
              </Button>
            </td>
          </tr>
        </template>
      </DataTable>
    </Card>

    <!-- ==================== 2. Command Code 额度窗口同步与会话保活 ==================== -->
    <Card
      v-if="activePanel === 'all' || activePanel === 'commandcode'"
      title="Command Code 额度窗口同步与会话保活"
      :subtitle="`池内剩余总额度 ${formatMoney(ccStats.totalRemaining)} · 支持 5 小时窗口与周滚动窗口实时同步及每日会话保活上报`"
      body-class="p-0"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <Button
            variant="secondary"
            size="sm"
            :disabled="ccAccounts.length === 0 || Boolean(runningKey)"
            :loading="runningKey === 'cc-sync'"
            @click="
              triggerAction({
                key: 'cc-sync',
                platform: 'commandcode',
                action: 'sync_balance',
                label: 'Command Code 全池额度与窗口同步',
              })
            "
          >
            <template #leading><Icon name="refresh" :size="13" /></template>
            同步余额与窗口 (sync_balance)
          </Button>
          <Button
            variant="primary"
            size="sm"
            :disabled="ccAccounts.length === 0 || Boolean(runningKey)"
            :loading="runningKey === 'cc-keepalive'"
            @click="
              triggerAction({
                key: 'cc-keepalive',
                platform: 'commandcode',
                action: 'keepalive',
                label: 'Command Code 会话保活上报',
              })
            "
          >
            <template #leading><Icon name="zap" :size="13" /></template>
            每日会话保活 (keepalive)
          </Button>
        </div>
      </template>

      <DataTable
        :columns="7"
        :loading="loading"
        :empty="ccAccounts.length === 0"
        min-width="1040px"
        hoverable
        class="border-0 rounded-none"
      >
        <template #head>
          <th class="th">Command Code 账号</th>
          <th class="th">状态</th>
          <th class="th text-right">月度剩余额度</th>
          <th class="th w-48">5 小时窗口使用率</th>
          <th class="th w-48">周滚动窗口使用率</th>
          <th class="th">最近探活 / 同步</th>
          <th class="th text-right">单号操作</th>
        </template>

        <template #empty>
          <EmptyState
            icon="terminal"
            title="暂无 Command Code 账号"
            description="在 Command Code 池中添加 user_ 密钥后，可在此一键同步 5h/周额度窗口与执行会话保活。"
          >
            <RouterLink :to="{ name: 'accounts-commandcode' }">
              <Button variant="secondary" size="sm">前往 Command Code 池</Button>
            </RouterLink>
          </EmptyState>
        </template>

        <template #body>
          <tr v-for="acc in ccAccounts" :key="acc.id">
            <td class="td max-w-[14rem]">
              <div class="flex flex-col gap-0.5">
                <span class="truncate font-medium text-fg">{{ acc.name }}</span>
                <span class="truncate font-mono text-2xs text-subtle">
                  {{ acc.balance?.plan_name || acc.masked_key || `#${acc.id}` }}
                </span>
              </div>
            </td>

            <td class="td">
              <StatusPill :status="acc.status" :label="acc.status === 'active' ? '正常' : undefined" />
            </td>

            <td class="td tnum text-right font-mono">
              <span class="font-semibold text-fg">{{ formatMoney(acc.balance?.remaining) }}</span>
              <span v-if="acc.balance?.total != null" class="text-2xs text-subtle">
                / {{ formatMoney(acc.balance.total) }}
              </span>
            </td>

            <td class="td">
              <div v-if="acc.balance?.five_hour" class="space-y-1">
                <div class="flex justify-between text-2xs">
                  <span class="tnum font-mono text-muted">
                    {{ formatMoney(acc.balance.five_hour.used) }} / {{ formatMoney(acc.balance.five_hour.cap) }}
                  </span>
                  <span class="tnum text-subtle">{{ windowRatio(acc.balance.five_hour).toFixed(0) }}%</span>
                </div>
                <div class="h-1.5 w-full overflow-hidden rounded-full bg-raised">
                  <div
                    class="h-full rounded-full transition-all"
                    :class="windowBarClass(acc.balance.five_hour)"
                    :style="{ width: `${windowRatio(acc.balance.five_hour)}%` }"
                  />
                </div>
              </div>
              <span v-else class="text-2xs text-subtle">未同步</span>
            </td>

            <td class="td">
              <div v-if="acc.balance?.weekly" class="space-y-1">
                <div class="flex justify-between text-2xs">
                  <span class="tnum font-mono text-muted">
                    {{ formatMoney(acc.balance.weekly.used) }} / {{ formatMoney(acc.balance.weekly.cap) }}
                  </span>
                  <span class="tnum text-subtle">{{ windowRatio(acc.balance.weekly).toFixed(0) }}%</span>
                </div>
                <div class="h-1.5 w-full overflow-hidden rounded-full bg-raised">
                  <div
                    class="h-full rounded-full transition-all"
                    :class="windowBarClass(acc.balance.weekly)"
                    :style="{ width: `${windowRatio(acc.balance.weekly)}%` }"
                  />
                </div>
              </div>
              <span v-else class="text-2xs text-subtle">未同步</span>
            </td>

            <td class="td text-2xs text-muted">
              <div>{{ formatRelative(acc.balance?.fetched_at || acc.last_health_check_at) }}</div>
              <div v-if="acc.latency_ms != null" class="tnum font-mono text-subtle">
                {{ acc.latency_ms }} ms
              </div>
            </td>

            <td class="td text-right">
              <div class="inline-flex items-center gap-1">
                <Button
                  variant="ghost"
                  size="xs"
                  :disabled="Boolean(runningKey)"
                  :loading="runningKey === `cc-sync-${acc.id}`"
                  @click="
                    triggerAction({
                      key: `cc-sync-${acc.id}`,
                      platform: 'commandcode',
                      action: 'sync_balance',
                      accountIds: [acc.id],
                      label: `同步「${acc.name}」窗口额度`,
                    })
                  "
                >
                  同步
                </Button>
                <Button
                  variant="secondary"
                  size="xs"
                  :disabled="Boolean(runningKey)"
                  :loading="runningKey === `cc-ka-${acc.id}`"
                  @click="
                    triggerAction({
                      key: `cc-ka-${acc.id}`,
                      platform: 'commandcode',
                      action: 'keepalive',
                      accountIds: [acc.id],
                      label: `保活「${acc.name}」`,
                    })
                  "
                >
                  保活
                </Button>
              </div>
            </td>
          </tr>
        </template>
      </DataTable>
    </Card>

    <!-- ==================== 3. AntiGravity & OpenCode 保活巡检区 ==================== -->
    <div
      v-if="activePanel === 'all' || activePanel === 'agy_oc'"
      class="grid gap-4 lg:grid-cols-2"
    >
      <!-- AntiGravity 容器 OAuth 保活 -->
      <Card
        title="AntiGravity 容器 OAuth 凭证刷新巡检"
        subtitle="检测容器节点与宿主机 .json OAuth 凭证有效期，提前触发令牌续期防止流式调用 401"
        body-class="p-4 space-y-3"
      >
        <template #actions>
          <div class="flex items-center gap-2">
            <RouterLink :to="{ name: 'antigravity' }">
              <Button variant="ghost" size="xs">容器工作台</Button>
            </RouterLink>
            <Button
              variant="primary"
              size="sm"
              :disabled="Boolean(runningKey)"
              :loading="runningKey === 'agy-oauth'"
              @click="
                triggerAction({
                  key: 'agy-oauth',
                  platform: 'antigravity',
                  action: 'refresh_oauth',
                  label: 'AntiGravity OAuth 凭证刷新巡检',
                })
              "
            >
              <template #leading><Icon name="rotate" :size="13" /></template>
              刷新 OAuth 凭证 (refresh_oauth)
            </Button>
          </div>
        </template>

        <div v-if="loading && allAccounts.length === 0" class="space-y-2 rounded-md border border-line bg-raised/30 p-3">
          <Skeleton v-for="i in 3" :key="i" class="h-8 w-full" />
        </div>

        <div v-else-if="agyAccounts.length === 0" class="rounded-md border border-dashed border-line bg-raised/30 p-4 text-center text-2xs text-subtle">
          当前账号池暂无已同步的 AntiGravity 账号，可前往「AntiGravity 容器池」上传 OAuth JSON 并同步至调度池，或直接点击上方按钮触发节点巡检。
        </div>

        <div v-else class="scroll-thin max-h-64 divide-y divide-line overflow-y-auto rounded-md border border-line bg-raised/30">
          <div
            v-for="acc in agyAccounts"
            :key="acc.id"
            class="flex items-center justify-between gap-3 px-3 py-2 text-xs"
          >
            <div class="min-w-0">
              <p class="truncate font-medium text-fg">{{ acc.name }}</p>
              <p class="truncate font-mono text-2xs text-subtle">
                {{ acc.email || acc.base_url || `#${acc.id}` }}
              </p>
            </div>
            <div class="flex shrink-0 items-center gap-2">
              <span v-if="acc.expires_at" class="font-mono text-2xs text-subtle">
                到期 {{ formatDateTime(acc.expires_at).slice(0, 10) }}
              </span>
              <StatusPill :status="acc.status" :label="acc.status === 'active' ? '正常' : undefined" />
            </div>
          </div>
        </div>
      </Card>

      <!-- OpenCode Zen / Go 低成本探活打卡 -->
      <Card
        title="OpenCode Zen / Go 批量低成本探活打卡"
        subtitle="发起极小 Token 探测请求验证 Zen 按量与 Go 订阅账号连通性，并自动恢复已就绪的异常账号"
        body-class="p-4 space-y-3"
      >
        <template #actions>
          <div class="flex items-center gap-2">
            <RouterLink :to="{ name: 'cron-jobs' }">
              <Button variant="ghost" size="xs">
                <template #leading><Icon name="clock" :size="12" /></template>
                定时巡检
              </Button>
            </RouterLink>
            <Button
              variant="primary"
              size="sm"
              :disabled="Boolean(runningKey)"
              :loading="runningKey === 'oc-probe'"
              @click="
                triggerAction({
                  key: 'oc-probe',
                  platform: 'opencode',
                  action: 'probe',
                  label: 'OpenCode 批量探活打卡',
                })
              "
            >
              <template #leading><Icon name="zap" :size="13" /></template>
              批量探活打卡 (probe)
            </Button>
          </div>
        </template>

        <div v-if="loading && allAccounts.length === 0" class="space-y-2 rounded-md border border-line bg-raised/30 p-3">
          <Skeleton v-for="i in 3" :key="i" class="h-8 w-full" />
        </div>

        <div v-else-if="ocAccounts.length === 0" class="rounded-md border border-dashed border-line bg-raised/30 p-4 text-center text-2xs text-subtle">
          当前暂无 OpenCode 账号。可在「OpenCode 池」导入 Zen / Go 密钥，并通过 Cron 定时任务设置无人值守探活。
        </div>

        <div v-else class="scroll-thin max-h-64 divide-y divide-line overflow-y-auto rounded-md border border-line bg-raised/30">
          <div
            v-for="acc in ocAccounts"
            :key="acc.id"
            class="flex items-center justify-between gap-3 px-3 py-2 text-xs"
          >
            <div class="min-w-0">
              <div class="flex items-center gap-1.5">
                <span class="truncate font-medium text-fg">{{ acc.name }}</span>
                <Badge :tone="acc.account_mode === 'go' ? 'info' : 'neutral'" mono>
                  {{ (acc.account_mode || 'zen').toUpperCase() }}
                </Badge>
              </div>
              <p class="truncate font-mono text-2xs text-subtle">
                {{ acc.base_url || acc.masked_key }}
              </p>
            </div>
            <div class="flex shrink-0 items-center gap-2">
              <span v-if="acc.latency_ms != null" class="tnum font-mono text-2xs text-muted">
                {{ acc.latency_ms }} ms
              </span>
              <StatusPill :status="acc.status" :label="acc.status === 'active' ? '正常' : undefined" />
            </div>
          </div>
        </div>
      </Card>
    </div>
  </div>
</template>
