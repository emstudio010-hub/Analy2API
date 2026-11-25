<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'

import { accountsApi, cronJobsApi, groupsApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type {
  Account,
  CronAccountResult,
  CronAction,
  CronJob,
  CronJobPayload,
  CronJobRunRecord,
  CronPreviewResult,
  CronRunStatus,
  CronTargetScope,
  Group,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import type { IconName } from '@/components/ui/icons'
import Input from '@/components/ui/Input.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import Modal from '@/components/ui/Modal.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import { formatCredits, formatDateTime, formatDuration, formatRelative } from '@/utils/format'

const toast = useToastStore()

const items = ref<CronJob[]>([])
const stats = ref({ total: 0, enabled: 0, running: 0 })
const groups = ref<Group[]>([])
const accounts = ref<Account[]>([])

const loading = ref(true)
const error = ref('')
const filterAction = ref('')
const keyword = ref('')

const runningId = ref<number | null>(null)
const togglingId = ref<number | null>(null)
const deleteTarget = ref<CronJob | null>(null)
const deleting = ref(false)
const resetOpen = ref(false)
const resetting = ref(false)

const formOpen = ref(false)
const editing = ref<CronJob | null>(null)
const saving = ref(false)
const formError = ref('')

const form = reactive({
  name: '',
  description: '',
  cron_expr: '0 8,20 * * *',
  timezone: 'Asia/Shanghai',
  action: 'harvest' as CronAction,
  target_scope: 'all' as CronTargetScope,
  group_id: '',
  account_ids: [] as number[],
  do_chat: false,
  auto_recover: true,
  harvest_on_check_fail: false,
  concurrency: '4',
  enabled: true,
})

const previewLoading = ref(false)
const previewData = ref<CronPreviewResult | null>(null)
let previewTimer: ReturnType<typeof setTimeout> | null = null

const detailJob = ref<CronJob | null>(null)
const detailOpen = ref(false)

const actionOptions: Array<{ label: string; value: CronAction }> = [
  { label: '定时领奖励 / 签到收割 (harvest)', value: 'harvest' },
  { label: '定时自动测试 / 模型探活 (account_check)', value: 'account_check' },
  { label: '组合：先自动测试再领奖励 (check_and_harvest)', value: 'check_and_harvest' },
  { label: '定时独立代理 IP 巡检 (proxy_test)', value: 'proxy_test' },
  { label: '定时刷新积分与额度窗口 (balance_refresh)', value: 'balance_refresh' },
  { label: '定时续期账号 Token (token_refresh)', value: 'token_refresh' },
]

const scopeOptions: Array<{ label: string; value: CronTargetScope }> = [
  { label: '全部未停用账号', value: 'all' },
  { label: '指定分组内账号', value: 'group' },
  { label: '手动勾选目标账号', value: 'accounts' },
]

const timezoneOptions = [
  { label: 'Asia/Shanghai (北京时间 UTC+8)', value: 'Asia/Shanghai' },
  { label: 'UTC (协调世界时)', value: 'UTC' },
  { label: 'Asia/Tokyo (东京 UTC+9)', value: 'Asia/Tokyo' },
  { label: 'America/New_York (美东)', value: 'America/New_York' },
  { label: 'Europe/London (伦敦)', value: 'Europe/London' },
]

const cronPresets = [
  { label: '早晚8点领奖', expr: '0 8,20 * * *' },
  { label: '每天8点', expr: '0 8 * * *' },
  { label: '每30分钟自动测试', expr: '*/30 * * * *' },
  { label: '每2小时巡检', expr: '0 */2 * * *' },
  { label: '每4小时刷额度', expr: '15 */4 * * *' },
  { label: '工作日9点', expr: '0 9 * * 1-5' },
  { label: '每15分钟 (@every)', expr: '@every 15m' },
]

const groupMap = computed(() => {
  const m = new Map<number, Group>()
  for (const g of groups.value) m.set(g.id, g)
  return m
})

const groupSelectOptions = computed(() =>
  groups.value.map((g) => ({
    label: `[${g.platform}] ${g.name} (${g.account_count} 个账号)`,
    value: String(g.id),
  })),
)

const filteredItems = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return items.value.filter((job) => {
    if (filterAction.value && job.action !== filterAction.value) return false
    if (!q) return true
    return (
      job.name.toLowerCase().includes(q) ||
      job.cron_expr.toLowerCase().includes(q) ||
      job.description.toLowerCase().includes(q) ||
      job.last_summary.toLowerCase().includes(q)
    )
  })
})

const nextSoonJob = computed(() => {
  const enabled = items.value.filter((j) => j.enabled && j.next_run_at)
  if (enabled.length === 0) return null
  return enabled
    .slice()
    .sort((a, b) => new Date(a.next_run_at!).getTime() - new Date(b.next_run_at!).getTime())[0]
})

function actionMeta(action: string): {
  label: string
  tone: 'success' | 'info' | 'accent' | 'warning' | 'neutral'
  icon: IconName
} {
  switch (action) {
    case 'harvest':
      return { label: '定时领奖励', tone: 'success', icon: 'gift' }
    case 'account_check':
      return { label: '自动测试探活', tone: 'info', icon: 'zap' }
    case 'check_and_harvest':
      return { label: '测试+领奖励', tone: 'success', icon: 'checkCircle' }
    case 'proxy_test':
      return { label: '代理 IP 巡检', tone: 'accent', icon: 'globe' }
    case 'balance_refresh':
      return { label: '刷新积分额度', tone: 'warning', icon: 'refresh' }
    case 'token_refresh':
      return { label: 'Token 续期', tone: 'neutral', icon: 'key' }
    default:
      return { label: action, tone: 'neutral', icon: 'clock' }
  }
}

function statusMeta(status: CronRunStatus | string): {
  label: string
  tone: 'success' | 'warning' | 'danger' | 'info' | 'neutral'
} {
  switch (status) {
    case 'running':
      return { label: '执行中', tone: 'info' }
    case 'success':
      return { label: '成功', tone: 'success' }
    case 'partial':
      return { label: '部分成功', tone: 'warning' }
    case 'error':
      return { label: '失败', tone: 'danger' }
    default:
      return { label: '未执行', tone: 'neutral' }
  }
}

function targetScopeLabel(job: CronJob): string {
  if (job.target_scope === 'group' && job.group_id) {
    const grp = groupMap.value.get(job.group_id)
    return grp ? `分组 · ${grp.name}` : `分组 #${job.group_id}`
  }
  if (job.target_scope === 'accounts') {
    return `指定 ${job.account_ids?.length ?? 0} 个账号`
  }
  return '全部未停用账号'
}

function formatCountdown(iso: string | null): string {
  if (!iso) return '—'
  const diff = new Date(iso).getTime() - Date.now()
  if (diff <= 0) return '即将执行'
  const secs = Math.floor(diff / 1000)
  if (secs < 60) return `${secs} 秒后`
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins} 分钟后`
  const hours = Math.floor(mins / 60)
  const remMins = mins % 60
  if (hours < 24) return remMins > 0 ? `${hours} 小时 ${remMins} 分后` : `${hours} 小时后`
  const days = Math.floor(hours / 24)
  return `${days} 天后`
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [jobsRes, groupsRes, accountsRes] = await Promise.all([
      cronJobsApi.list(),
      groupsApi.list(),
      accountsApi.list({ page: 1, page_size: 200 }),
    ])
    items.value = jobsRes.items ?? []
    stats.value = jobsRes.stats ?? {
      total: items.value.length,
      enabled: items.value.filter((x) => x.enabled).length,
      running: items.value.filter((x) => x.running).length,
    }
    groups.value = groupsRes.items ?? []
    accounts.value = accountsRes.items ?? []

    if (detailOpen.value && detailJob.value) {
      const updated = items.value.find((x) => x.id === detailJob.value?.id)
      if (updated) detailJob.value = updated
    }
  } catch (err) {
    error.value = toMessage(err, '加载 Cron 定时任务失败')
  } finally {
    loading.value = false
  }
}

function openCreate(
  preset?: Partial<{ name: string; action: CronAction; cron_expr: string; description: string }>,
): void {
  editing.value = null
  formError.value = ''
  form.name = preset?.name ?? ''
  form.description = preset?.description ?? ''
  form.cron_expr = preset?.cron_expr ?? '0 8,20 * * *'
  form.timezone = 'Asia/Shanghai'
  form.action = preset?.action ?? 'harvest'
  form.target_scope = 'all'
  form.group_id = groups.value[0] ? String(groups.value[0].id) : ''
  form.account_ids = []
  form.do_chat = false
  form.auto_recover = true
  form.harvest_on_check_fail = false
  form.concurrency = form.action === 'account_check' || form.action === 'check_and_harvest' ? '2' : '4'
  form.enabled = true
  formOpen.value = true
  void runPreview()
}

function openEdit(job: CronJob): void {
  editing.value = job
  formError.value = ''
  form.name = job.name
  form.description = job.description
  form.cron_expr = job.cron_expr
  form.timezone = job.timezone || 'Asia/Shanghai'
  form.action = job.action
  form.target_scope = job.target_scope
  form.group_id = job.group_id ? String(job.group_id) : groups.value[0] ? String(groups.value[0].id) : ''
  form.account_ids = [...(job.account_ids ?? [])]
  form.do_chat = Boolean(job.options?.do_chat)
  form.auto_recover = job.options?.auto_recover !== false
  form.harvest_on_check_fail = Boolean(job.options?.harvest_on_check_fail)
  const rawConc = Number(job.options?.concurrency)
  form.concurrency = Number.isFinite(rawConc) && rawConc > 0 ? String(rawConc) : '4'
  form.enabled = job.enabled
  formOpen.value = true
  void runPreview()
}

function applyPreset(expr: string): void {
  form.cron_expr = expr
  void runPreview()
}

function toggleAccountSelection(id: number): void {
  const idx = form.account_ids.indexOf(id)
  if (idx >= 0) form.account_ids.splice(idx, 1)
  else form.account_ids.push(id)
}

async function runPreview(): Promise<void> {
  const expr = form.cron_expr.trim()
  if (!expr) {
    previewData.value = {
      valid: false,
      cron_expr: '',
      timezone: form.timezone,
      error: '请输入 Cron 表达式',
      next_runs: [],
    }
    return
  }
  previewLoading.value = true
  try {
    previewData.value = await cronJobsApi.preview(expr, form.timezone, 5)
  } catch (err) {
    previewData.value = {
      valid: false,
      cron_expr: expr,
      timezone: form.timezone,
      error: toMessage(err, '校验 Cron 表达式失败'),
      next_runs: [],
    }
  } finally {
    previewLoading.value = false
  }
}

watch(
  () => [form.cron_expr, form.timezone, formOpen.value] as const,
  ([, , isOpen]) => {
    if (!isOpen) return
    if (previewTimer) clearTimeout(previewTimer)
    previewTimer = setTimeout(() => {
      void runPreview()
    }, 280)
  },
)

watch(
  () => form.action,
  (nextAct) => {
    if (!editing.value) {
      form.concurrency = nextAct === 'account_check' || nextAct === 'check_and_harvest' ? '2' : '4'
    }
  },
)

async function saveForm(): Promise<void> {
  formError.value = ''
  if (!form.name.trim()) {
    formError.value = '请填写任务名称'
    return
  }
  if (!form.cron_expr.trim()) {
    formError.value = '请填写 Cron 表达式'
    return
  }
  if (previewData.value && !previewData.value.valid) {
    formError.value = previewData.value.error || 'Cron 表达式语法无效'
    return
  }
  if (form.target_scope === 'group' && !form.group_id) {
    formError.value = '请选择目标分组'
    return
  }
  if (form.target_scope === 'accounts' && form.account_ids.length === 0) {
    formError.value = '请至少勾选 1 个目标账号'
    return
  }

  const conc = Math.min(8, Math.max(1, Number.parseInt(form.concurrency, 10) || 4))
  const options: Record<string, unknown> = { concurrency: conc }
  if (form.action === 'harvest' || form.action === 'check_and_harvest') {
    options.do_chat = form.do_chat
  }
  if (form.action === 'account_check' || form.action === 'check_and_harvest') {
    options.auto_recover = form.auto_recover
  }
  if (form.action === 'check_and_harvest') {
    options.harvest_on_check_fail = form.harvest_on_check_fail
  }

  const payload: CronJobPayload = {
    name: form.name.trim(),
    description: form.description.trim(),
    cron_expr: form.cron_expr.trim(),
    timezone: form.timezone,
    action: form.action,
    target_scope: form.target_scope,
    group_id: form.target_scope === 'group' ? Number(form.group_id) : null,
    clear_group: form.target_scope !== 'group',
    account_ids: form.target_scope === 'accounts' ? form.account_ids : [],
    options,
    enabled: form.enabled,
  }

  saving.value = true
  try {
    if (editing.value) {
      await cronJobsApi.update(editing.value.id, payload)
      toast.success('已更新定时任务')
    } else {
      await cronJobsApi.create(payload)
      toast.success('已创建定时任务')
    }
    formOpen.value = false
    await load()
  } catch (err) {
    formError.value = toMessage(err, '保存定时任务失败')
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(job: CronJob): Promise<void> {
  togglingId.value = job.id
  try {
    const updated = await cronJobsApi.update(job.id, { enabled: !job.enabled })
    const idx = items.value.findIndex((x) => x.id === job.id)
    if (idx >= 0) items.value[idx] = updated
    stats.value.enabled = items.value.filter((x) => x.enabled).length
    toast.success(updated.enabled ? `已启用「${updated.name}」` : `已暂停「${updated.name}」`)
  } catch (err) {
    toast.error(toMessage(err, '切换启停状态失败'))
  } finally {
    togglingId.value = null
  }
}

async function runNow(job: CronJob): Promise<void> {
  runningId.value = job.id
  try {
    const res = await cronJobsApi.runNow(job.id)
    if (res.job) {
      const idx = items.value.findIndex((x) => x.id === job.id)
      if (idx >= 0) items.value[idx] = res.job
    }
    const r = res.result
    if (r.status === 'error') {
      toast.error(`${job.name} 执行失败：${r.summary}`)
    } else if (r.status === 'partial') {
      toast.warning(`${job.name} 部分成功：${r.summary}`)
    } else {
      toast.success(`${job.name} 完成：${r.summary}`)
    }
    if (res.job) {
      detailJob.value = res.job
      detailOpen.value = true
    }
  } catch (err) {
    toast.error(toMessage(err, '立即执行定时任务失败'))
  } finally {
    runningId.value = null
  }
}

function openDetail(job: CronJob): void {
  detailJob.value = job
  detailOpen.value = true
}

async function confirmDelete(): Promise<void> {
  if (!deleteTarget.value) return
  deleting.value = true
  try {
    await cronJobsApi.remove(deleteTarget.value.id)
    toast.success(`已删除「${deleteTarget.value.name}」`)
    deleteTarget.value = null
    await load()
  } catch (err) {
    toast.error(toMessage(err, '删除定时任务失败'))
  } finally {
    deleting.value = false
  }
}

async function confirmReset(): Promise<void> {
  resetting.value = true
  try {
    const res = await cronJobsApi.reset()
    toast.success(res.message || '已补齐默认定时任务')
    resetOpen.value = false
    await load()
  } catch (err) {
    toast.error(toMessage(err, '恢复默认任务失败'))
  } finally {
    resetting.value = false
  }
}

const lastItems = computed<CronAccountResult[]>(() => detailJob.value?.last_result?.items ?? [])
const historyRows = computed<CronJobRunRecord[]>(() => detailJob.value?.run_history ?? [])

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部指标与快捷模板磁贴 -->
    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <MetricTile
        label="Cron 调度任务总数"
        :value="stats.total"
        :hint="`启用 ${stats.enabled} · 执行中 ${stats.running}`"
        icon="clock"
        tone="accent"
      />
      <MetricTile
        label="最近下一次排期触发"
        :value="nextSoonJob ? formatCountdown(nextSoonJob.next_run_at) : '暂无排期'"
        :hint="
          nextSoonJob
            ? `${nextSoonJob.name} · ${formatDateTime(nextSoonJob.next_run_at)}`
            : '启用或创建 Cron 任务后自动计算'
        "
        icon="zap"
        tone="info"
      />

      <div class="card flex flex-col justify-between p-3.5">
        <div>
          <div class="flex items-center justify-between text-2xs text-muted">
            <span>快捷排期 · 每日领奖</span>
            <Icon name="gift" :size="14" class="text-success" />
          </div>
          <p class="mt-1 text-2xs text-subtle">早晚 08:00 / 20:00 自动签到、盲盒与激励收割</p>
        </div>
        <Button
          variant="secondary"
          size="xs"
          class="mt-2.5 self-start"
          @click="
            openCreate({
              name: '每日定时自动领奖励',
              action: 'harvest',
              cron_expr: '0 8,20 * * *',
              description: '每天 08:00 与 20:00 自动执行签到与奖励收割',
            })
          "
        >
          <template #leading><Icon name="plus" :size="12" /></template>
          一键填入领奖排期
        </Button>
      </div>

      <div class="card flex flex-col justify-between p-3.5">
        <div>
          <div class="flex items-center justify-between text-2xs text-muted">
            <span>快捷排期 · 自动探活恢复</span>
            <Icon name="globe" :size="14" class="text-warning" />
          </div>
          <p class="mt-1 text-2xs text-subtle">每 30 分钟自动巡检账号健康度并恢复异常池</p>
        </div>
        <Button
          variant="secondary"
          size="xs"
          class="mt-2.5 self-start"
          @click="
            openCreate({
              name: '定时自动测试与恢复',
              action: 'account_check',
              cron_expr: '*/30 * * * *',
              description: '每 30 分钟自动探测账号池可用性并恢复异常账号',
            })
          "
        >
          <template #leading><Icon name="plus" :size="12" /></template>
          一键填入探活排期
        </Button>
      </div>
    </div>

    <!-- 工具栏 -->
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <div class="flex flex-wrap items-center gap-2">
        <div class="w-56">
          <Input v-model="keyword" size="sm" placeholder="搜索任务名 / Cron 语句 / 摘要…" />
        </div>
        <div class="w-52">
          <Select
            v-model="filterAction"
            size="sm"
            placeholder="全部动作类型"
            :options="actionOptions"
          />
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <Button variant="secondary" size="sm" :loading="loading" @click="load">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新
        </Button>
        <Button variant="secondary" size="sm" @click="resetOpen = true">
          <template #leading><Icon name="rotate" :size="13" /></template>
          恢复默认任务
        </Button>
        <Button variant="primary" size="sm" @click="openCreate()">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建 Cron 任务
        </Button>
      </div>
    </div>

    <!-- 主任务列表 -->
    <DataTable
      :columns="7"
      :loading="loading"
      :empty="!loading && filteredItems.length === 0"
      min-width="1060px"
      hoverable
    >
      <template #head>
        <th class="th">任务名称</th>
        <th class="th">动作类型</th>
        <th class="th">Cron 语句 / 范围</th>
        <th class="th">下次计划运行</th>
        <th class="th">最近执行结果</th>
        <th class="th">启用</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          v-if="error"
          icon="alertCircle"
          tone="danger"
          title="加载 Cron 定时任务失败"
          :description="error"
        >
          <Button variant="secondary" size="sm" @click="load">重试</Button>
        </EmptyState>
        <EmptyState
          v-else
          icon="clock"
          title="暂无匹配的定时任务"
          description="可通过上方快捷模板创建新的自动巡检或定时领奖任务，或恢复默认内置任务。"
        >
          <Button variant="primary" size="sm" @click="openCreate()">新建 Cron 任务</Button>
        </EmptyState>
      </template>

      <template #body>
        <tr v-for="job in filteredItems" :key="job.id">
          <td class="td max-w-[16rem] whitespace-normal">
            <div class="flex items-center gap-1.5">
              <span class="text-[13px] font-medium text-fg">{{ job.name }}</span>
              <Badge v-if="job.is_builtin" tone="neutral">内置</Badge>
            </div>
            <p v-if="job.description" class="mt-0.5 text-2xs leading-relaxed text-subtle">
              {{ job.description }}
            </p>
          </td>

          <td class="td">
            <Badge :tone="actionMeta(job.action).tone">
              <Icon :name="actionMeta(job.action).icon" :size="11" />
              {{ actionMeta(job.action).label }}
            </Badge>
            <div v-if="job.options?.do_chat" class="mt-1 text-2xs text-subtle">含对话打卡</div>
          </td>

          <td class="td">
            <Badge mono tone="neutral">{{ job.cron_expr }}</Badge>
            <p class="mt-1 text-2xs text-muted">{{ targetScopeLabel(job) }}</p>
            <p class="font-mono text-2xs text-subtle">{{ job.timezone }}</p>
          </td>

          <td class="td">
            <template v-if="job.enabled && job.next_run_at">
              <p class="text-xs font-medium text-fg">{{ formatCountdown(job.next_run_at) }}</p>
              <p class="mt-0.5 font-mono text-2xs text-subtle">
                {{ job.next_runs?.[0]?.local || formatDateTime(job.next_run_at) }}
              </p>
            </template>
            <span v-else class="text-2xs text-subtle">已暂停调度</span>
          </td>

          <td class="td max-w-[18rem] whitespace-normal">
            <div class="flex flex-wrap items-center gap-1.5">
              <Badge :tone="statusMeta(job.running ? 'running' : job.last_status).tone" dot>
                {{ statusMeta(job.running ? 'running' : job.last_status).label }}
              </Badge>
              <span v-if="job.last_duration_ms > 0" class="tnum font-mono text-2xs text-subtle">
                {{ formatDuration(job.last_duration_ms) }}
              </span>
              <span v-if="job.last_run_at" class="text-2xs text-subtle">
                · {{ formatRelative(job.last_run_at) }}
              </span>
            </div>
            <p v-if="job.last_summary" class="mt-1 text-2xs leading-relaxed text-muted">
              {{ job.last_summary }}
            </p>
          </td>

          <td class="td">
            <Switch
              size="sm"
              :model-value="job.enabled"
              :disabled="togglingId === job.id"
              @update:model-value="toggleEnabled(job)"
            />
          </td>

          <td class="td text-right">
            <div class="inline-flex items-center justify-end gap-1">
              <Button
                variant="secondary"
                size="xs"
                :loading="runningId === job.id || job.running"
                title="立即触发执行一次（内置 300s 长任务超时保护）"
                @click="runNow(job)"
              >
                <template #leading><Icon name="play" :size="11" /></template>
                立即执行
              </Button>
              <Button
                variant="ghost"
                size="xs"
                title="查看最近运行记录与单账号明细"
                @click="openDetail(job)"
              >
                记录
              </Button>
              <Button variant="ghost" size="xs" icon title="编辑任务" @click="openEdit(job)">
                <Icon name="edit" :size="13" />
              </Button>
              <Button
                variant="ghost"
                size="xs"
                icon
                class="hover:bg-danger/10 hover:text-danger"
                title="删除任务"
                @click="deleteTarget = job"
              >
                <Icon name="trash" :size="13" />
              </Button>
            </div>
          </td>
        </tr>
      </template>
    </DataTable>

    <!-- Cron 表达式语法参考卡片 -->
    <Card
      title="Cron 表达式语法说明"
      subtitle="标准 5 段格式为「分 时 日 月 周」，同时兼容 6 段「秒 分 时 日 月 周」与 @daily / @hourly / @every 描述符"
      body-class="p-4"
    >
      <div class="grid gap-3 text-xs text-muted sm:grid-cols-2 lg:grid-cols-4">
        <div class="panel-inset p-2.5">
          <code class="font-mono text-fg">0 8,20 * * *</code>
          <p class="mt-1 text-2xs text-subtle">每天 08:00 与 20:00 触发一次（推荐用于定时签到领奖）</p>
        </div>
        <div class="panel-inset p-2.5">
          <code class="font-mono text-fg">*/30 * * * *</code>
          <p class="mt-1 text-2xs text-subtle">每隔 30 分钟触发一次（推荐用于多源账号自动探活）</p>
        </div>
        <div class="panel-inset p-2.5">
          <code class="font-mono text-fg">0 */2 * * *</code>
          <p class="mt-1 text-2xs text-subtle">每 2 小时整点触发一次（推荐用于独立代理 IP 巡检）</p>
        </div>
        <div class="panel-inset p-2.5">
          <code class="font-mono text-fg">@every 15m</code>
          <p class="mt-1 text-2xs text-subtle">固定周期每 15 分钟触发一次（支持 s / m / h 单位）</p>
        </div>
      </div>
    </Card>

    <!-- 新建/编辑 Cron 任务弹窗 -->
    <Modal
      :open="formOpen"
      size="lg"
      :title="editing ? `编辑定时任务 · ${editing.name}` : '新建 Cron 定时任务'"
      subtitle="配置 Cron 表达式、执行动作与目标账号范围，实时预览未来 5 次触发时刻"
      @update:open="formOpen = $event"
    >
      <form class="space-y-4" @submit.prevent="saveForm">
        <div
          v-if="formError"
          class="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger"
        >
          {{ formError }}
        </div>

        <div class="grid gap-3 sm:grid-cols-2">
          <Field label="任务名称" required>
            <Input v-model="form.name" placeholder="例如：每日早8点签到领奖" />
          </Field>

          <Field label="任务动作" required>
            <Select v-model="form.action" :options="actionOptions" />
          </Field>
        </div>

        <div class="grid gap-3 sm:grid-cols-3">
          <div class="sm:col-span-2">
            <Field label="Cron 表达式" required hint="5 段 / 6 段或 @every 15m">
              <Input v-model="form.cron_expr" mono placeholder="0 8,20 * * *" />
            </Field>
          </div>
          <Field label="调度时区">
            <Select v-model="form.timezone" :options="timezoneOptions" />
          </Field>
        </div>

        <div class="space-y-1.5">
          <p class="text-2xs font-medium text-subtle">常用 Cron 预设（点击填入）：</p>
          <div class="flex flex-wrap gap-1.5">
            <button
              v-for="p in cronPresets"
              :key="p.expr"
              type="button"
              class="rounded border px-2 py-1 text-2xs transition"
              :class="
                form.cron_expr.trim() === p.expr
                  ? 'border-accent bg-accent/10 text-accent'
                  : 'border-line bg-raised text-muted hover:border-line-strong hover:text-fg'
              "
              @click="applyPreset(p.expr)"
            >
              <span class="font-mono">{{ p.expr }}</span> · {{ p.label }}
            </button>
          </div>
        </div>

        <!-- 实时 Cron 解析预览 -->
        <div class="panel-inset p-3">
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs font-medium text-fg">未来 5 次执行时刻预览（{{ form.timezone }}）</span>
            <Badge v-if="previewLoading" tone="info">校验中…</Badge>
            <Badge v-else-if="previewData?.valid" tone="success">Cron 语法有效</Badge>
            <Badge v-else tone="danger">语法错误</Badge>
          </div>

          <p v-if="previewData && !previewData.valid" class="mt-1.5 text-xs text-danger">
            {{ previewData.error }}
          </p>
          <ul v-else-if="previewData?.next_runs?.length" class="mt-2 grid gap-1.5 sm:grid-cols-2">
            <li
              v-for="(run, idx) in previewData.next_runs"
              :key="run.rfc3339"
              class="flex items-center gap-2 font-mono text-2xs text-muted"
            >
              <span class="inline-flex h-4 w-4 items-center justify-center rounded bg-overlay text-subtle">
                {{ idx + 1 }}
              </span>
              <span>{{ run.local }}</span>
            </li>
          </ul>
        </div>

        <div class="grid gap-3 sm:grid-cols-2">
          <Field label="目标账号范围">
            <Select v-model="form.target_scope" :options="scopeOptions" />
          </Field>

          <Field label="每批并发上限 (1-8)" hint="自动测试建议 1-2，领奖或刷积分可用 4">
            <Input v-model="form.concurrency" type="number" min="1" max="8" mono />
          </Field>
        </div>

        <Field v-if="form.target_scope === 'group'" label="选择目标分组" required>
          <Select v-model="form.group_id" :options="groupSelectOptions" placeholder="请选择分组" />
        </Field>

        <div v-if="form.target_scope === 'accounts'" class="space-y-1.5">
          <div class="flex items-center justify-between">
            <span class="text-xs font-medium text-muted">
              勾选目标账号（已选 {{ form.account_ids.length }} 个）
            </span>
            <div class="flex gap-2">
              <button
                type="button"
                class="text-2xs text-accent hover:underline"
                @click="form.account_ids = accounts.map((a) => a.id)"
              >
                全选
              </button>
              <button
                type="button"
                class="text-2xs text-subtle hover:underline"
                @click="form.account_ids = []"
              >
                清空
              </button>
            </div>
          </div>
          <div class="scroll-thin max-h-44 space-y-1 overflow-y-auto rounded-md border border-line bg-raised/40 p-2">
            <label
              v-for="acc in accounts"
              :key="acc.id"
              class="flex cursor-pointer items-center justify-between rounded px-2 py-1.5 text-xs transition hover:bg-surface"
            >
              <span class="flex items-center gap-2">
                <input
                  type="checkbox"
                  class="h-3.5 w-3.5 rounded border-line-strong bg-surface accent-blue-600"
                  :checked="form.account_ids.includes(acc.id)"
                  @change="toggleAccountSelection(acc.id)"
                />
                <PlatformBadge :platform="acc.platform" short :show-icon="false" />
                <span class="font-medium text-fg">{{ acc.name }}</span>
                <span class="text-2xs text-subtle">{{ acc.email || acc.uid || `#${acc.id}` }}</span>
              </span>
              <Badge :tone="acc.status === 'active' ? 'success' : acc.status === 'error' ? 'danger' : 'neutral'">
                {{ acc.status }}
              </Badge>
            </label>
          </div>
        </div>

        <div class="panel-inset space-y-2 p-3">
          <p class="text-xs font-medium text-fg">执行选项</p>

          <Checkbox
            v-if="form.action === 'harvest' || form.action === 'check_and_harvest'"
            v-model="form.do_chat"
            label="领奖励时附带执行一次对话任务 (do_chat)"
            description="勾选后会发起极短 Fast 模型对话完成每日对话激励任务"
          />

          <Checkbox
            v-if="form.action === 'account_check' || form.action === 'check_and_harvest'"
            v-model="form.auto_recover"
            label="探活包含 error 状态账号并自动恢复"
            description="若探活成功则自动清零连续失败计数并恢复调度"
          />

          <Checkbox
            v-if="form.action === 'check_and_harvest'"
            v-model="form.harvest_on_check_fail"
            label="即使对话探活失败也继续执行领奖励流水线"
          />

          <Checkbox v-model="form.enabled" label="保存后立即启用 Cron 自动调度" />
        </div>

        <Field label="任务备注">
          <Textarea v-model="form.description" :rows="2" placeholder="可选：说明此定时任务的用途…" />
        </Field>
      </form>

      <template #footer>
        <Button variant="ghost" @click="formOpen = false">取消</Button>
        <Button variant="primary" :loading="saving" @click="saveForm">保存任务</Button>
      </template>
    </Modal>

    <!-- 执行记录与单账号明细弹窗 -->
    <Modal
      :open="detailOpen"
      size="xl"
      :title="detailJob ? `执行明细与历史 · ${detailJob.name}` : '执行明细与历史'"
      :subtitle="detailJob ? `${actionMeta(detailJob.action).label} · Cron: ${detailJob.cron_expr}` : ''"
      @update:open="detailOpen = $event"
    >
      <div v-if="detailJob" class="space-y-5">
        <div class="space-y-2">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-xs font-semibold text-fg">最近一次单账号执行明细</h3>
            <div class="flex items-center gap-2 text-2xs text-subtle">
              <Badge :tone="statusMeta(detailJob.last_status).tone" dot>
                {{ statusMeta(detailJob.last_status).label }}
              </Badge>
              <span v-if="detailJob.last_run_at">{{ formatDateTime(detailJob.last_run_at) }}</span>
              <span v-if="detailJob.last_duration_ms > 0">
                · 耗时 {{ formatDuration(detailJob.last_duration_ms) }}
              </span>
            </div>
          </div>

          <p v-if="detailJob.last_summary" class="panel-inset px-3 py-2 text-xs text-fg">
            {{ detailJob.last_summary }}
          </p>

          <div
            v-if="lastItems.length === 0"
            class="rounded border border-line px-3 py-6 text-center text-xs text-subtle"
          >
            暂无账号级执行明细，可点击「立即执行」试跑一次。
          </div>

          <div v-else class="scroll-thin max-h-72 overflow-auto rounded-md border border-line">
            <table class="w-full border-collapse text-left">
              <thead class="border-b border-line bg-raised/70">
                <tr>
                  <th class="th">账号</th>
                  <th class="th">结果</th>
                  <th class="th">耗时 / 延迟</th>
                  <th class="th">积分 / 出口 IP</th>
                  <th class="th">详情与步骤</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-line">
                <tr v-for="item in lastItems" :key="item.account_id" class="align-top">
                  <td class="td">
                    <div class="flex items-center gap-1.5">
                      <PlatformBadge :platform="item.platform" short :show-icon="false" />
                      <span class="font-medium text-fg">{{ item.account_name }}</span>
                    </div>
                    <span class="text-2xs text-subtle">#{{ item.account_id }}</span>
                  </td>
                  <td class="td">
                    <Badge v-if="item.skipped" tone="neutral">已跳过</Badge>
                    <Badge v-else-if="item.ok" tone="success">成功</Badge>
                    <Badge v-else tone="danger">失败</Badge>
                  </td>
                  <td class="td tnum font-mono text-2xs text-muted">
                    <div>{{ formatDuration(item.duration_ms) }}</div>
                    <div v-if="item.latency_ms" class="text-subtle">延迟 {{ item.latency_ms }}ms</div>
                  </td>
                  <td class="td text-2xs">
                    <div v-if="item.credit_gained" class="tnum font-semibold text-success">
                      +{{ item.credit_gained }} 积分
                    </div>
                    <div
                      v-if="item.total_credits !== undefined && item.total_credits !== null"
                      class="tnum text-muted"
                    >
                      余额 {{ formatCredits(item.total_credits) }}
                    </div>
                    <div v-if="item.exit_ip" class="font-mono text-info">IP: {{ item.exit_ip }}</div>
                  </td>
                  <td class="td whitespace-normal text-2xs">
                    <p v-if="item.error" class="text-danger">{{ item.error }}</p>
                    <p v-else-if="item.detail" class="text-muted">{{ item.detail }}</p>
                    <div v-if="item.steps?.length" class="mt-1 flex flex-wrap gap-1">
                      <Badge
                        v-for="st in item.steps"
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
        </div>

        <div class="space-y-2">
          <h3 class="text-xs font-semibold text-fg">最近运行历史（最多保留 20 次）</h3>
          <div
            v-if="historyRows.length === 0"
            class="rounded border border-line px-3 py-4 text-center text-xs text-subtle"
          >
            暂无历史运行记录
          </div>
          <div v-else class="scroll-thin max-h-56 overflow-auto rounded-md border border-line">
            <table class="w-full border-collapse text-left">
              <thead class="border-b border-line bg-raised/70">
                <tr>
                  <th class="th">触发时间</th>
                  <th class="th">触发方式</th>
                  <th class="th">状态</th>
                  <th class="th">耗时</th>
                  <th class="th">汇总摘要</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-line">
                <tr v-for="(h, idx) in historyRows" :key="`${h.started_at}-${idx}`">
                  <td class="td font-mono text-2xs text-muted">{{ formatDateTime(h.started_at) }}</td>
                  <td class="td">
                    <Badge :tone="h.trigger === 'cron' ? 'info' : 'neutral'">
                      {{ h.trigger === 'cron' ? 'Cron 定时' : '手动触发' }}
                    </Badge>
                  </td>
                  <td class="td">
                    <Badge :tone="statusMeta(h.status).tone" dot>{{ statusMeta(h.status).label }}</Badge>
                  </td>
                  <td class="td tnum font-mono text-2xs text-subtle">
                    {{ formatDuration(h.duration_ms) }}
                  </td>
                  <td class="td whitespace-normal text-2xs text-fg">{{ h.summary }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <template #footer>
        <div class="flex w-full justify-between">
          <Button
            v-if="detailJob"
            variant="secondary"
            size="sm"
            :loading="runningId === detailJob.id || detailJob.running"
            @click="runNow(detailJob)"
          >
            <template #leading><Icon name="play" :size="12" /></template>
            立即再执行一次
          </Button>
          <Button variant="primary" size="sm" @click="detailOpen = false">关闭</Button>
        </div>
      </template>
    </Modal>

    <ConfirmDialog
      :open="Boolean(deleteTarget)"
      tone="danger"
      title="删除 Cron 定时任务"
      :message="deleteTarget ? `确认删除定时任务「${deleteTarget.name}」？此操作不可撤销。` : ''"
      confirm-text="确认删除"
      :loading="deleting"
      @update:open="!$event && (deleteTarget = null)"
      @confirm="confirmDelete"
    />

    <ConfirmDialog
      :open="resetOpen"
      title="恢复默认 Cron 任务"
      message="将自动补齐缺失的默认内置任务（每日定时领奖励、自动测试探活、代理 IP 巡检、积分刷新），不会影响自定义的 Cron 任务。"
      confirm-text="确认恢复"
      :loading="resetting"
      @update:open="resetOpen = $event"
      @confirm="confirmReset"
    />
  </div>
</template>
