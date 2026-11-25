<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import { toMessage } from '@/api/client'
import { accountsApi, groupsApi, oauthApi } from '@/api/endpoints'
import type {
  Account,
  AccountBalanceWindow,
  BatchImportResult,
  CreateAccountPayload,
  Group,
  OAuthSession,
  ProxyTestResult,
  UpdateAccountPayload,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import Modal from '@/components/ui/Modal.vue'
import Pagination from '@/components/ui/Pagination.vue'
import Select from '@/components/ui/Select.vue'
import Spinner from '@/components/ui/Spinner.vue'
import StatusPill from '@/components/ui/StatusPill.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import {
  formatDate,
  formatDateTime,
  formatMoney,
  formatRelative,
  redactProxyDisplay,
} from '@/utils/format'

const toast = useToastStore()

const items = ref<Account[]>([])
const groups = ref<Group[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')

const page = ref(1)
const pageSize = ref(20)
const keyword = ref('')
const status = ref('')
const groupFilter = ref('')

// 行操作与批量操作状态
const checkingId = ref<number | null>(null)
const balanceLoadingId = ref<number | null>(null)
const proxyTestingId = ref<number | null>(null)
const togglingId = ref<number | null>(null)
const balanceAllLoading = ref(false)
const batchChecking = ref(false)
const exporting = ref(false)

const proxyTests = ref<Record<number, ProxyTestResult>>({})

// 删除确认
const deleteTarget = ref<Account | null>(null)
const deleting = ref(false)

// 单号新建/编辑弹窗状态
const formOpen = ref(false)
const editing = ref<Account | null>(null)
const formName = ref('')
const formApiKey = ref('')
const formShowKey = ref(false)
const formProxyUrl = ref('')
const formConcurrency = ref('5')
const formPriority = ref('50')
const formMultiplier = ref('1')
const formStatus = ref('active')
const formSchedulable = ref(true)
const formNotes = ref('')
const formGroupIds = ref<number[]>([])
const formSubmitting = ref(false)
const formError = ref('')

// 批量导入弹窗状态
const batchOpen = ref(false)
const batchRaw = ref('')
const batchProxyUrl = ref('')
const batchConcurrency = ref('5')
const batchPriority = ref('50')
const batchUpsert = ref(true)
const batchGroupIds = ref<number[]>([])
const batchSubmitting = ref(false)
const batchError = ref('')
const batchResult = ref<BatchImportResult | null>(null)

const statusOptions = [
  { label: '正常', value: 'active' },
  { label: '已禁用', value: 'disabled' },
  { label: '异常', value: 'error' },
]

const ccGroups = computed(() => groups.value.filter((g) => g.platform === 'commandcode'))
const groupOptions = computed(() =>
  ccGroups.value.map((g) => ({ label: g.name, value: String(g.id) })),
)
const groupNameById = computed(() => {
  const map = new Map<number, string>()
  for (const g of groups.value) map.set(g.id, g.name)
  return map
})

const filtersActive = computed(
  () => Boolean(keyword.value.trim()) || Boolean(status.value) || Boolean(groupFilter.value),
)

const poolStats = computed(() => {
  let active = 0
  let remainingUsd = 0
  let totalUsd = 0
  let windowExceeded = 0
  let withProxy = 0
  for (const acc of items.value) {
    if (acc.status === 'active') active += 1
    if (acc.balance?.remaining !== null && acc.balance?.remaining !== undefined) {
      remainingUsd += acc.balance.remaining
    }
    if (acc.balance?.total !== null && acc.balance?.total !== undefined) {
      totalUsd += acc.balance.total
    }
    if (acc.balance?.five_hour?.exceeded || acc.balance?.weekly?.exceeded) {
      windowExceeded += 1
    }
    if (acc.proxy_url && acc.proxy_url.toLowerCase() !== 'direct') {
      withProxy += 1
    }
  }
  return { active, remainingUsd, totalUsd, windowExceeded, withProxy }
})

let searchTimer: ReturnType<typeof setTimeout> | null = null

async function loadGroups(): Promise<void> {
  try {
    const res = await groupsApi.list()
    groups.value = res.items
  } catch (err) {
    toast.error('分组加载失败', toMessage(err))
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await accountsApi.list({
      platform: 'commandcode',
      page: page.value,
      page_size: pageSize.value,
      status: status.value || undefined,
      keyword: keyword.value.trim() || undefined,
      group_id: groupFilter.value ? Number(groupFilter.value) : undefined,
    })
    items.value = res.items
    total.value = res.total
  } catch (err) {
    error.value = toMessage(err, '加载 Command Code 账号池失败')
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

watch(keyword, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    page.value = 1
    void load()
  }, 300)
})

watch([status, groupFilter], () => {
  page.value = 1
  void load()
})

watch([page, pageSize], () => {
  void load()
})

onMounted(async () => {
  await Promise.all([load(), loadGroups()])
})

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
})

function clearFilters(): void {
  keyword.value = ''
  status.value = ''
  groupFilter.value = ''
  page.value = 1
  void load()
}

function replaceAccountInPlace(updated: Account): void {
  const idx = items.value.findIndex((item) => item.id === updated.id)
  if (idx >= 0) items.value[idx] = updated
}

// ---- 滚动窗口计算辅助 ----
function windowPercent(win?: AccountBalanceWindow | null): number {
  if (!win || !win.cap || win.cap <= 0) return 0
  return Math.min(100, Math.max(0, (win.used / win.cap) * 100))
}

function windowBarClass(win?: AccountBalanceWindow | null): string {
  if (!win) return 'bg-subtle'
  const pct = windowPercent(win)
  if (win.exceeded || pct >= 92) return 'bg-danger'
  if (pct >= 75) return 'bg-warning'
  return 'bg-info'
}

// ---- 单行与全池操作 ----
async function checkOne(account: Account): Promise<void> {
  if (checkingId.value !== null) return
  checkingId.value = account.id
  try {
    const res = await accountsApi.check(account.id)
    if (res.ok) {
      toast.success(`「${account.name}」探活正常`, `延迟 ${res.latency_ms} ms`)
    } else {
      toast.error(`「${account.name}」探活失败`, res.error || '上游响应异常')
    }
    await load()
  } catch (err) {
    toast.error('探活请求失败', toMessage(err))
  } finally {
    checkingId.value = null
  }
}

async function checkAllVisible(): Promise<void> {
  if (batchChecking.value || items.value.length === 0) return
  batchChecking.value = true
  let ok = 0
  let fail = 0
  try {
    for (const acc of items.value) {
      try {
        const res = await accountsApi.check(acc.id)
        if (res.ok) ok += 1
        else fail += 1
      } catch {
        fail += 1
      }
    }
    toast.info('Command Code 批量探活完成', `正常 ${ok} 个 · 异常 ${fail} 个`)
    await load()
  } finally {
    batchChecking.value = false
  }
}

async function refreshBalanceOne(account: Account): Promise<void> {
  if (balanceLoadingId.value !== null) return
  balanceLoadingId.value = account.id
  try {
    const res = await accountsApi.refreshBalance(account.id)
    if (res.ok) {
      if (res.account) replaceAccountInPlace(res.account)
      else await load()
      toast.success(
        `「${account.name}」额度已更新`,
        `剩余 ${formatMoney(res.account?.balance?.remaining ?? account.balance?.remaining)}`,
      )
    } else {
      toast.error(`「${account.name}」额度同步失败`, res.error || '上游未返回余额')
    }
  } catch (err) {
    toast.error('刷新额度失败', toMessage(err))
  } finally {
    balanceLoadingId.value = null
  }
}

async function refreshAllBalances(): Promise<void> {
  if (balanceAllLoading.value) return
  balanceAllLoading.value = true
  try {
    const res = await accountsApi.refreshAllBalances('commandcode')
    await load()
    if (res.failed > 0) {
      toast.warning('全池额度同步完成（含失败项）', `成功 ${res.refreshed} · 失败 ${res.failed}`)
    } else {
      toast.success('Command Code 全池额度已刷新', `成功同步 ${res.refreshed} 个账号`)
    }
  } catch (err) {
    toast.error('全池额度刷新失败', toMessage(err))
  } finally {
    balanceAllLoading.value = false
  }
}

async function testProxyOne(account: Account): Promise<void> {
  if (proxyTestingId.value !== null) return
  proxyTestingId.value = account.id
  try {
    const res = await accountsApi.testProxy(account.id)
    proxyTests.value = { ...proxyTests.value, [account.id]: res }
    if (res.ok) {
      toast.success(`「${account.name}」代理正常`, `出口 ${res.exit_ip} · ${res.latency_ms} ms`)
    } else {
      toast.error(`「${account.name}」代理异常`, res.error || '连接失败')
    }
  } catch (err) {
    toast.error('代理测试失败', toMessage(err))
  } finally {
    proxyTestingId.value = null
  }
}

async function toggleStatus(account: Account): Promise<void> {
  if (togglingId.value !== null) return
  const next = account.status === 'active' ? 'disabled' : 'active'
  togglingId.value = account.id
  try {
    await accountsApi.update(account.id, { status: next })
    toast.success(next === 'active' ? '账号已启用' : '账号已禁用', account.name)
    await load()
  } catch (err) {
    toast.error('更新状态失败', toMessage(err))
  } finally {
    togglingId.value = null
  }
}

async function exportPool(): Promise<void> {
  if (exporting.value) return
  exporting.value = true
  try {
    const res = await accountsApi.exportAccounts('commandcode')
    const blob = new Blob([JSON.stringify(res, null, 2)], { type: 'application/json;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `commandcode-accounts-${new Date().toISOString().slice(0, 10)}.json`
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    URL.revokeObjectURL(url)
    toast.success('导出完成', `已导出 ${res.count} 个账号`)
  } catch (err) {
    toast.error('导出失败', toMessage(err))
  } finally {
    exporting.value = false
  }
}

async function confirmDelete(): Promise<void> {
  if (!deleteTarget.value || deleting.value) return
  deleting.value = true
  try {
    await accountsApi.remove(deleteTarget.value.id)
    toast.success('账号已删除', deleteTarget.value.name)
    deleteTarget.value = null
    await load()
  } catch (err) {
    toast.error('删除失败', toMessage(err))
  } finally {
    deleting.value = false
  }
}

// ---- 新建 / 编辑弹窗 ----
function openCreate(): void {
  editing.value = null
  formName.value = ''
  formApiKey.value = ''
  formShowKey.value = false
  formProxyUrl.value = ''
  formConcurrency.value = '5'
  formPriority.value = '50'
  formMultiplier.value = '1'
  formStatus.value = 'active'
  formSchedulable.value = true
  formNotes.value = ''
  formGroupIds.value = ccGroups.value[0] ? [ccGroups.value[0].id] : []
  formError.value = ''
  formOpen.value = true
}

function openEdit(acc: Account): void {
  editing.value = acc
  formName.value = acc.name
  formApiKey.value = ''
  formShowKey.value = false
  formProxyUrl.value = acc.proxy_url || ''
  formConcurrency.value = String(acc.concurrency ?? 5)
  formPriority.value = String(acc.priority ?? 50)
  formMultiplier.value = String(acc.rate_multiplier ?? 1)
  formStatus.value = acc.status || 'active'
  formSchedulable.value = acc.schedulable ?? true
  formNotes.value = acc.notes || ''
  formGroupIds.value = [...acc.group_ids]
  formError.value = ''
  formOpen.value = true
}

function toggleFormGroup(id: number, checked: boolean): void {
  if (checked) {
    if (!formGroupIds.value.includes(id)) formGroupIds.value.push(id)
  } else {
    formGroupIds.value = formGroupIds.value.filter((g) => g !== id)
  }
}

const formKeyError = computed(() => {
  const k = formApiKey.value.trim()
  if (!editing.value && !k) return '请输入 user_ 开头的 Command Code 密钥'
  if (k && !k.startsWith('user_')) return 'Command Code 密钥需以 user_ 开头'
  return ''
})

const canSubmitForm = computed(
  () => Boolean(formName.value.trim()) && !formKeyError.value,
)

async function submitForm(): Promise<void> {
  if (!canSubmitForm.value || formSubmitting.value) return
  formSubmitting.value = true
  formError.value = ''
  try {
    if (editing.value) {
      const payload: UpdateAccountPayload = {
        name: formName.value.trim(),
        platform: 'commandcode',
        proxy_url: formProxyUrl.value.trim(),
        concurrency: Number(formConcurrency.value) || 5,
        priority: Number(formPriority.value) || 50,
        rate_multiplier: Number(formMultiplier.value) || 1,
        status: formStatus.value,
        schedulable: formSchedulable.value,
        notes: formNotes.value.trim(),
        group_ids: formGroupIds.value,
      }
      if (formApiKey.value.trim()) {
        payload.api_key = formApiKey.value.trim()
      }
      await accountsApi.update(editing.value.id, payload)
      toast.success('Command Code 账号已更新', payload.name)
    } else {
      const payload: CreateAccountPayload = {
        name: formName.value.trim(),
        platform: 'commandcode',
        api_key: formApiKey.value.trim(),
        proxy_url: formProxyUrl.value.trim(),
        concurrency: Number(formConcurrency.value) || 5,
        priority: Number(formPriority.value) || 50,
        rate_multiplier: Number(formMultiplier.value) || 1,
        status: formStatus.value,
        schedulable: formSchedulable.value,
        notes: formNotes.value.trim(),
        group_ids: formGroupIds.value,
      }
      await accountsApi.create(payload)
      toast.success('Command Code 账号已创建', payload.name)
    }
    formOpen.value = false
    await load()
  } catch (err) {
    formError.value = toMessage(err, '保存账号失败')
  } finally {
    formSubmitting.value = false
  }
}

// ---- 批量导入弹窗 ----
function openBatchModal(): void {
  batchRaw.value = ''
  batchProxyUrl.value = ''
  batchConcurrency.value = '5'
  batchPriority.value = '50'
  batchUpsert.value = true
  batchGroupIds.value = ccGroups.value[0] ? [ccGroups.value[0].id] : []
  batchError.value = ''
  batchResult.value = null
  batchOpen.value = true
}

function toggleBatchGroup(id: number, checked: boolean): void {
  if (checked) {
    if (!batchGroupIds.value.includes(id)) batchGroupIds.value.push(id)
  } else {
    batchGroupIds.value = batchGroupIds.value.filter((g) => g !== id)
  }
}

const batchLineCount = computed(() => {
  const trimmed = batchRaw.value.trim()
  if (!trimmed) return 0
  if (trimmed.startsWith('[') || trimmed.startsWith('{')) return 1
  return trimmed
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith('#')).length
})

async function submitBatch(): Promise<void> {
  if (!batchRaw.value.trim() || batchSubmitting.value) return
  batchSubmitting.value = true
  batchError.value = ''
  try {
    const res = await accountsApi.batchImport({
      platform: 'commandcode',
      keys: batchRaw.value.trim(),
      proxy_url: batchProxyUrl.value.trim() || undefined,
      group_ids: batchGroupIds.value.length > 0 ? batchGroupIds.value : undefined,
      concurrency: Number(batchConcurrency.value) || 5,
      priority: Number(batchPriority.value) || 50,
      upsert: batchUpsert.value,
    })
    batchResult.value = res
    toast.success(
      '批量导入完成',
      `新增 ${res.created} · 更新 ${res.updated}${res.failed ? ` · 失败 ${res.failed}` : ''}`,
    )
    await load()
    if (res.failed === 0) {
      batchOpen.value = false
    }
  } catch (err) {
    batchError.value = toMessage(err, '批量导入失败')
  } finally {
    batchSubmitting.value = false
  }
}

const oauthOpen = ref(false)
const oauthProxyUrl = ref('')
const oauthAccountName = ref('')
const oauthGroupIds = ref<number[]>([])
const oauthRedirectMode = ref(true)
const oauthSession = ref<OAuthSession | null>(null)
const oauthCallbackInput = ref('')
const oauthCreating = ref(false)
const oauthSubmitting = ref(false)
const oauthError = ref('')

function openOAuthModal(): void {
  oauthError.value = ''
  oauthAccountName.value = ''
  oauthCallbackInput.value = ''
  oauthSession.value = null
  oauthGroupIds.value = ccGroups.value.slice(0, 1).map((g) => g.id)
  oauthOpen.value = true
}

function toggleOAuthGroup(groupId: number): void {
  const idx = oauthGroupIds.value.indexOf(groupId)
  if (idx >= 0) {
    oauthGroupIds.value.splice(idx, 1)
  } else {
    oauthGroupIds.value.push(groupId)
  }
}

async function startCcOAuth(): Promise<void> {
  if (oauthCreating.value) return
  oauthCreating.value = true
  oauthError.value = ''
  try {
    const res = await oauthApi.createSession({
      platform: 'commandcode',
      proxy_url: oauthProxyUrl.value.trim() || undefined,
      group_ids: oauthGroupIds.value.length > 0 ? oauthGroupIds.value : undefined,
      redirect_mode: oauthRedirectMode.value,
      account_name: oauthAccountName.value.trim() || undefined,
    })
    oauthSession.value = res.session
    toast.success('OAuth 会话已创建', '请点击授权链接登录 Studio 后提交回调 URL 或 user_ Token')
  } catch (err) {
    oauthError.value = toMessage(err, '生成 Command Code OAuth 会话失败')
  } finally {
    oauthCreating.value = false
  }
}

async function copyOAuthUrl(): Promise<void> {
  if (!oauthSession.value?.auth_url) return
  try {
    await navigator.clipboard.writeText(oauthSession.value.auth_url)
    toast.success('已复制授权链接', '')
  } catch {
    toast.error('复制失败', '请手动复制输入框内的链接')
  }
}

async function completeCcOAuth(): Promise<void> {
  if (!oauthSession.value || !oauthCallbackInput.value.trim() || oauthSubmitting.value) return
  oauthSubmitting.value = true
  oauthError.value = ''
  try {
    const raw = oauthCallbackInput.value.trim()
    const payload = raw.startsWith('http://') || raw.startsWith('https://') || raw.includes('=')
      ? { session_id: oauthSession.value.session_id, callback_url: raw }
      : { session_id: oauthSession.value.session_id, api_key: raw }
    const res = await oauthApi.completeCallback(payload)
    if (res.session) {
      oauthSession.value = res.session
    }
    toast.success('Studio 授权完成并已同步入池', res.account?.name ? `账号 ${res.account.name}` : res.message)
    await load()
    oauthOpen.value = false
  } catch (err) {
    oauthError.value = toMessage(err, '完成 Command Code OAuth 回调失败')
  } finally {
    oauthSubmitting.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部池水位指标 -->
    <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <MetricTile
        label="Command Code 账号池"
        :value="`${poolStats.active} / ${total}`"
        hint="正常可调度 / 总账号数"
        icon="terminal"
        tone="info"
      />
      <MetricTile
        label="全池月度剩余额度"
        :value="formatMoney(poolStats.remainingUsd)"
        :hint="`总额度上限 ${formatMoney(poolStats.totalUsd)}`"
        icon="coins"
        tone="success"
      />
      <MetricTile
        label="滚动窗口限流预警"
        :value="String(poolStats.windowExceeded)"
        hint="5h 窗口或周窗口触顶账号数"
        icon="alert"
        :tone="poolStats.windowExceeded > 0 ? 'danger' : 'neutral'"
      />
      <MetricTile
        label="独立出口代理覆盖"
        :value="`${poolStats.withProxy} / ${items.length}`"
        hint="支持每账号独立 HTTP / SOCKS5 代理"
        icon="globe"
        tone="accent"
      />
    </div>

    <!-- 筛选与操作工具栏 -->
    <div class="card flex flex-wrap items-center gap-2.5 px-3.5 py-2.5">
      <div class="w-full min-w-[12rem] sm:w-56">
        <Input v-model="keyword" size="sm" placeholder="搜索账号名称 / 备注 / 代理" />
      </div>
      <div class="w-32">
        <Select v-model="status" size="sm" :options="statusOptions" placeholder="全部状态" />
      </div>
      <div class="w-40">
        <Select v-model="groupFilter" size="sm" :options="groupOptions" placeholder="全部分组" />
      </div>
      <Button v-if="filtersActive" variant="ghost" size="sm" @click="clearFilters">
        清空筛选
      </Button>

      <div class="ml-auto flex flex-wrap items-center gap-2">
        <Button
          variant="secondary"
          size="sm"
          :loading="balanceAllLoading"
          @click="refreshAllBalances"
        >
          <template #leading><Icon name="coins" :size="13" /></template>
          刷新全池额度
        </Button>

        <Button
          variant="secondary"
          size="sm"
          :loading="batchChecking"
          :disabled="items.length === 0"
          @click="checkAllVisible"
        >
          <template #leading><Icon name="zap" :size="13" /></template>
          批量探活
        </Button>

        <Button variant="secondary" size="sm" @click="openOAuthModal">
          <template #leading><Icon name="link" :size="13" /></template>
          Studio OAuth 授权
        </Button>
        <Button variant="secondary" size="sm" @click="openBatchModal">
          <template #leading><Icon name="upload" :size="13" /></template>
          批量导入 user_
        </Button>

        <Button variant="secondary" size="sm" :loading="exporting" @click="exportPool">
          <template #leading><Icon name="download" :size="13" /></template>
          导出 JSON
        </Button>

        <Button variant="primary" size="sm" @click="openCreate">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建账号
        </Button>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3.5 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="load">重试</Button>
    </p>

    <!-- 高密度账号与额度窗口表 -->
    <DataTable
      :columns="9"
      :loading="loading"
      :empty="items.length === 0"
      :skeleton-rows="6"
      min-width="1420px"
      hoverable
    >
      <template #head>
        <th class="th">账号 / 掩码密钥</th>
        <th class="th">状态 / 探活延迟</th>
        <th class="th">绑定分组</th>
        <th class="th">月度剩余额度 ($)</th>
        <th class="th">5 小时滚动窗口</th>
        <th class="th">周滚动窗口</th>
        <th class="th">独立代理 IP</th>
        <th class="th">调度权重</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="terminal"
          :title="filtersActive ? '没有匹配的 Command Code 账号' : '尚未接入任何 Command Code 账号'"
          description="支持添加单把 user_ 密钥或批量导入多把密钥，并为每个账号绑定独立出口代理 IP 与分组倍率。"
        >
          <Button variant="secondary" size="sm" @click="openBatchModal">
            <template #leading><Icon name="upload" :size="13" /></template>
            批量导入 user_ 密钥
          </Button>
          <Button variant="primary" size="sm" @click="openCreate">
            <template #leading><Icon name="plus" :size="13" /></template>
            新建账号
          </Button>
        </EmptyState>
      </template>

      <template #body>
        <tr v-for="acc in items" :key="acc.id">
          <!-- 1. 账号 -->
          <td class="td max-w-[15rem]">
            <div class="flex items-center gap-1.5">
              <span class="truncate font-semibold text-fg">{{ acc.name }}</span>
              <Badge v-if="!acc.schedulable" tone="warning">暂停调度</Badge>
            </div>
            <div class="mt-0.5 font-mono text-2xs text-subtle">
              {{ acc.masked_key || '未配置密钥' }}
            </div>
            <p v-if="acc.notes" class="mt-0.5 truncate text-2xs text-subtle">{{ acc.notes }}</p>
          </td>

          <!-- 2. 状态与健康 -->
          <td class="td">
            <div class="flex flex-col items-start gap-1">
              <StatusPill :status="acc.status" :label="acc.status === 'active' ? '正常' : undefined" />
              <span
                v-if="acc.last_health_check_at"
                class="tnum font-mono text-2xs"
                :class="acc.last_health_check_ok ? 'text-success' : 'text-danger'"
              >
                {{
                  acc.last_health_check_ok
                    ? `${acc.latency_ms ?? 0} ms`
                    : '探活失败'
                }}
              </span>
              <span
                v-if="acc.error_message || acc.last_health_check_error"
                class="max-w-[9rem] truncate text-2xs text-danger"
                :title="acc.error_message || acc.last_health_check_error || ''"
              >
                {{ acc.error_message || acc.last_health_check_error }}
              </span>
            </div>
          </td>

          <!-- 3. 分组 -->
          <td class="td max-w-[9rem]">
            <div v-if="acc.group_ids.length" class="flex flex-wrap gap-1">
              <Badge v-for="gid in acc.group_ids" :key="gid" tone="neutral">
                {{ groupNameById.get(gid) ?? `#${gid}` }}
              </Badge>
            </div>
            <span v-else class="text-2xs text-warning">未绑分组</span>
          </td>

          <!-- 4. 月度剩余额度与重置时间 -->
          <td class="td min-w-[10rem]">
            <div class="flex items-baseline gap-1.5">
              <span class="tnum text-[13px] font-semibold text-fg">
                {{ formatMoney(acc.balance?.remaining) }}
              </span>
              <span v-if="acc.balance?.total" class="tnum text-2xs text-subtle">
                / {{ formatMoney(acc.balance.total) }}
              </span>
            </div>
            <div class="mt-0.5 flex flex-wrap items-center gap-1.5 text-2xs text-subtle">
              <span v-if="acc.balance?.plan_name">{{ acc.balance.plan_name }}</span>
              <span v-if="acc.balance?.period_end">
                · 重置 {{ formatDate(acc.balance.period_end) }}
              </span>
              <span v-else-if="acc.balance?.fetched_at">
                · {{ formatRelative(acc.balance.fetched_at) }}
              </span>
            </div>
          </td>

          <!-- 5. 5 小时滚动窗口 -->
          <td class="td min-w-[10.5rem]">
            <template v-if="acc.balance?.five_hour">
              <div class="flex items-center justify-between gap-2 text-2xs">
                <span class="tnum font-mono text-fg">
                  {{ formatMoney(acc.balance.five_hour.used) }} /
                  {{ formatMoney(acc.balance.five_hour.cap) }}
                </span>
                <span
                  class="tnum font-mono"
                  :class="acc.balance.five_hour.exceeded ? 'font-semibold text-danger' : 'text-subtle'"
                >
                  {{ windowPercent(acc.balance.five_hour).toFixed(0) }}%
                </span>
              </div>
              <div class="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-raised">
                <div
                  class="h-full rounded-full transition-[width] duration-300"
                  :class="windowBarClass(acc.balance.five_hour)"
                  :style="{ width: `${windowPercent(acc.balance.five_hour)}%` }"
                />
              </div>
              <p v-if="acc.balance.five_hour.reset_at" class="mt-0.5 text-2xs text-subtle">
                重置 {{ formatDateTime(acc.balance.five_hour.reset_at).slice(11, 16) }}
              </p>
            </template>
            <span v-else class="text-2xs text-subtle">待刷新同步</span>
          </td>

          <!-- 6. 周窗口 -->
          <td class="td min-w-[10.5rem]">
            <template v-if="acc.balance?.weekly">
              <div class="flex items-center justify-between gap-2 text-2xs">
                <span class="tnum font-mono text-fg">
                  {{ formatMoney(acc.balance.weekly.used) }} /
                  {{ formatMoney(acc.balance.weekly.cap) }}
                </span>
                <span
                  class="tnum font-mono"
                  :class="acc.balance.weekly.exceeded ? 'font-semibold text-danger' : 'text-subtle'"
                >
                  {{ windowPercent(acc.balance.weekly).toFixed(0) }}%
                </span>
              </div>
              <div class="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-raised">
                <div
                  class="h-full rounded-full transition-[width] duration-300"
                  :class="windowBarClass(acc.balance.weekly)"
                  :style="{ width: `${windowPercent(acc.balance.weekly)}%` }"
                />
              </div>
              <p v-if="acc.balance.weekly.reset_at" class="mt-0.5 text-2xs text-subtle">
                重置 {{ formatDate(acc.balance.weekly.reset_at) }}
              </p>
            </template>
            <span v-else class="text-2xs text-subtle">待刷新同步</span>
          </td>

          <!-- 7. 独立代理状态与测速 -->
          <td class="td max-w-[13rem]">
            <div class="flex items-center gap-1.5">
              <Badge v-if="!acc.proxy_url" tone="neutral">默认/直连</Badge>
              <span
                v-else
                class="max-w-[8.5rem] truncate font-mono text-2xs text-fg"
                :title="redactProxyDisplay(acc.proxy_url)"
              >
                {{ redactProxyDisplay(acc.proxy_url) }}
              </span>
              <Button
                variant="ghost"
                size="xs"
                class="ml-auto shrink-0"
                :disabled="proxyTestingId !== null"
                @click="testProxyOne(acc)"
              >
                <Spinner v-if="proxyTestingId === acc.id" :size="11" />
                <template v-else>测速</template>
              </Button>
            </div>
            <p
              v-if="proxyTests[acc.id]"
              class="mt-0.5 truncate font-mono text-2xs"
              :class="proxyTests[acc.id]?.ok ? 'text-success' : 'text-danger'"
            >
              {{
                proxyTests[acc.id]?.ok
                  ? `${proxyTests[acc.id]?.exit_ip} · ${proxyTests[acc.id]?.latency_ms}ms`
                  : proxyTests[acc.id]?.error || '测速失败'
              }}
            </p>
          </td>

          <!-- 8. 调度参数 -->
          <td class="td text-2xs">
            <div class="tnum text-fg">
              P{{ acc.priority }} · 并发{{ acc.concurrency }} · {{ acc.rate_multiplier }}x
            </div>
            <div class="text-subtle">最近: {{ formatRelative(acc.last_used_at) }}</div>
          </td>

          <!-- 9. 操作 -->
          <td class="td">
            <div class="flex items-center justify-end gap-1">
              <Button
                variant="secondary"
                size="xs"
                :disabled="balanceLoadingId !== null"
                title="同步月度余额与 5h/周滚动窗口"
                @click="refreshBalanceOne(acc)"
              >
                <Spinner v-if="balanceLoadingId === acc.id" :size="11" />
                <template v-else>刷额度</template>
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                title="探活检测"
                :disabled="checkingId !== null"
                @click="checkOne(acc)"
              >
                <Spinner v-if="checkingId === acc.id" :size="11" />
                <Icon v-else name="zap" :size="13" />
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                :title="acc.status === 'active' ? '禁用账号' : '启用账号'"
                :disabled="togglingId !== null"
                @click="toggleStatus(acc)"
              >
                <Icon :name="acc.status === 'active' ? 'lock' : 'checkCircle'" :size="13" />
              </Button>

              <Button variant="ghost" size="xs" icon title="编辑账号" @click="openEdit(acc)">
                <Icon name="edit" :size="13" />
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                class="hover:bg-danger/10 hover:text-danger"
                title="删除账号"
                @click="deleteTarget = acc"
              >
                <Icon name="trash" :size="13" />
              </Button>
            </div>
          </td>
        </tr>
      </template>

      <template #footer>
        <Pagination v-model:page="page" v-model:page-size="pageSize" :total="total" />
      </template>
    </DataTable>

    <!-- 单个新建/编辑弹窗 -->
    <Modal
      :open="formOpen"
      :title="editing ? `编辑 Command Code 账号 · ${editing.name}` : '新建 Command Code 账号'"
      subtitle="配置 user_ 凭证、独立代理出口 IP、并发权重与 Command Code 分组绑定"
      size="lg"
      :persistent="formSubmitting"
      @update:open="formOpen = $event"
    >
      <form class="space-y-4" @submit.prevent="submitForm">
        <p
          v-if="formError"
          class="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
          <span>{{ formError }}</span>
        </p>

        <div class="grid gap-3.5 sm:grid-cols-2">
          <Field label="账号名称" required>
            <Input v-model="formName" placeholder="例如：cc-pro-01" />
          </Field>

          <Field
            label="Command Code 密钥 (user_...)"
            :required="!editing"
            :error="formKeyError"
            :hint="editing ? `留空保留原密钥（${editing.masked_key}）` : '必填，以 user_ 开头'"
          >
            <div class="relative">
              <Input
                v-model="formApiKey"
                :type="formShowKey ? 'text' : 'password'"
                mono
                placeholder="user_xxxxxxxxxxxx"
                class="pr-8"
              />
              <button
                type="button"
                class="absolute right-2 top-1/2 -translate-y-1/2 text-subtle hover:text-fg"
                @click="formShowKey = !formShowKey"
              >
                <Icon :name="formShowKey ? 'eyeOff' : 'eye'" :size="13" />
              </button>
            </div>
          </Field>
        </div>

        <Field
          label="账号独立代理 IP (proxy_url)"
          hint="支持 http://user:pass@ip:port 或 socks5://...；留空走全局默认代理或直连"
        >
          <Input v-model="formProxyUrl" mono placeholder="socks5://user:pass@1.2.3.4:1080" />
        </Field>

        <div class="grid grid-cols-3 gap-3">
          <Field label="并发上限">
            <Input v-model="formConcurrency" type="number" min="1" />
          </Field>
          <Field label="调度优先级">
            <Input v-model="formPriority" type="number" min="0" />
          </Field>
          <Field label="计费倍率">
            <Input v-model="formMultiplier" type="number" min="0.01" step="0.1" />
          </Field>
        </div>

        <div class="panel-inset space-y-2 p-3">
          <div class="flex items-center justify-between">
            <span class="text-xs font-medium text-fg">绑定 Command Code 分组</span>
            <RouterLink :to="{ name: 'groups' }" class="text-2xs text-accent hover:underline">
              去管理分组 →
            </RouterLink>
          </div>
          <div v-if="ccGroups.length > 0" class="flex flex-wrap gap-3">
            <Checkbox
              v-for="g in ccGroups"
              :key="g.id"
              :model-value="formGroupIds.includes(g.id)"
              :label="`${g.name} (${g.rate_multiplier}x)`"
              @update:model-value="toggleFormGroup(g.id, $event)"
            />
          </div>
          <p v-else class="text-2xs text-subtle">
            暂无 `commandcode` 平台分组，请先前往「分组与倍率路由」创建。
          </p>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-3">
          <Switch v-model="formSchedulable" label="允许参与网关流量调度" />
          <div class="w-36">
            <Select v-model="formStatus" size="sm" :options="statusOptions" />
          </div>
        </div>

        <Field label="备注">
          <Input v-model="formNotes" placeholder="可选备注（归属团队、续费日期等）" />
        </Field>
      </form>

      <template #footer>
        <Button variant="ghost" size="md" :disabled="formSubmitting" @click="formOpen = false">
          取消
        </Button>
        <Button
          variant="primary"
          size="md"
          :loading="formSubmitting"
          :disabled="!canSubmitForm"
          @click="submitForm"
        >
          {{ editing ? '保存修改' : '创建账号' }}
        </Button>
      </template>
    </Modal>

    <!-- 多行 user_ 密钥 / JSON 批量导入弹窗 -->
    <Modal
      :open="batchOpen"
      title="批量导入 Command Code 账号"
      subtitle="支持每行一把 user_... 密钥（或 name|user_xxx）及导出 JSON 数组批量导入"
      size="lg"
      :persistent="batchSubmitting"
      @update:open="batchOpen = $event"
    >
      <div class="space-y-4">
        <p
          v-if="batchError"
          class="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
          <span>{{ batchError }}</span>
        </p>

        <Field
          :label="`密钥文本 / JSON（已识别 ${batchLineCount} 条）`"
          required
          hint="每行一条 user_xxxxxxxx，或粘贴导出的 JSON 数组"
        >
          <Textarea
            v-model="batchRaw"
            :rows="7"
            mono
            placeholder="user_a1b2c3d4e5f6...&#10;cc-team-2|user_f6e5d4c3b2a1..."
          />
        </Field>

        <Field label="默认独立代理 IP (proxy_url，可选)">
          <Input v-model="batchProxyUrl" mono placeholder="http://user:pass@ip:port" />
        </Field>

        <div class="grid grid-cols-2 gap-3">
          <Field label="默认并发数">
            <Input v-model="batchConcurrency" type="number" min="1" size="sm" />
          </Field>
          <Field label="默认优先级">
            <Input v-model="batchPriority" type="number" min="0" size="sm" />
          </Field>
        </div>

        <div class="panel-inset space-y-2 p-3">
          <p class="text-xs font-medium text-fg">绑定 Command Code 分组</p>
          <div v-if="ccGroups.length > 0" class="flex flex-wrap gap-3">
            <Checkbox
              v-for="g in ccGroups"
              :key="g.id"
              :model-value="batchGroupIds.includes(g.id)"
              :label="g.name"
              @update:model-value="toggleBatchGroup(g.id, $event)"
            />
          </div>
          <p v-else class="text-2xs text-subtle">暂无 `commandcode` 分组</p>
        </div>

        <Switch v-model="batchUpsert" label="遇到相同密钥时自动覆盖更新已有账号 (upsert)" />

        <div
          v-if="batchResult && batchResult.failures?.length"
          class="rounded-md border border-warning/35 bg-warning/10 p-3 text-2xs text-warning"
        >
          <p class="font-semibold">部分行导入失败（{{ batchResult.failed }} 条）：</p>
          <ul class="mt-1 list-disc space-y-0.5 pl-4 font-mono">
            <li v-for="f in batchResult.failures.slice(0, 5)" :key="f.line">
              第 {{ f.line }} 行: {{ f.error }}
            </li>
          </ul>
        </div>
      </div>

      <template #footer>
        <Button variant="ghost" size="md" :disabled="batchSubmitting" @click="batchOpen = false">
          关闭
        </Button>
        <Button
          variant="primary"
          size="md"
          :loading="batchSubmitting"
          :disabled="batchLineCount === 0"
          @click="submitBatch"
        >
          开始导入
        </Button>
      </template>
    </Modal>

    <Modal
      :open="oauthOpen"
      title="Command Code Studio OAuth 统一授权入池"
      subtitle="创建带固定出口代理的 OAuth 会话，自动完成 Whoami 校验、Token 加密与账号池同步"
      size="lg"
      @close="oauthOpen = false"
    >
      <div class="space-y-4">
        <div
          v-if="oauthError"
          class="rounded-[var(--radius-md)] border border-[var(--danger)]/40 bg-[var(--danger-subtle)] px-3 py-2 text-xs text-[var(--danger)]"
        >
          {{ oauthError }}
        </div>

        <div class="grid gap-3 md:grid-cols-2">
          <Field label="独立出口代理 (Proxy URL · 可选)">
            <Input
              v-model="oauthProxyUrl"
              size="sm"
              placeholder="http://user:pass@ip:port 或 socks5://..."
            />
          </Field>
          <Field label="账号备注名（可选）">
            <Input
              v-model="oauthAccountName"
              size="sm"
              placeholder="留空则按 Whoami 邮箱/用户名自动命名"
            />
          </Field>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-2 rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--bg-subtle)] px-3 py-2 text-xs">
          <span>自动绑定网关回调路由 (/api/oauth/callback)</span>
          <Switch v-model="oauthRedirectMode" />
        </div>

        <div v-if="ccGroups.length > 0">
          <div class="mb-1.5 text-xs font-medium text-[var(--text-secondary)]">授权后自动绑定分组</div>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="g in ccGroups"
              :key="g.id"
              type="button"
              class="rounded-[var(--radius-md)] border px-2.5 py-1 text-xs transition"
              :class="
                oauthGroupIds.includes(g.id)
                  ? 'border-[var(--accent)] bg-[var(--accent-subtle)] text-[var(--accent)]'
                  : 'border-[var(--border-default)] bg-[var(--bg-surface)] text-[var(--text-secondary)]'
              "
              @click="toggleOAuthGroup(g.id)"
            >
              {{ g.name }}
            </button>
          </div>
        </div>

        <div class="flex items-center justify-end">
          <Button variant="primary" size="sm" :loading="oauthCreating" @click="startCcOAuth">
            <template #leading><Icon name="key" :size="13" /></template>
            {{ oauthSession ? '重新生成授权会话' : '1. 生成 Studio OAuth 授权链接' }}
          </Button>
        </div>

        <div
          v-if="oauthSession"
          class="space-y-3 rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-subtle)] p-3"
        >
          <div class="flex items-center justify-between gap-2">
            <div class="text-xs font-medium text-[var(--text-primary)]">
              会话 ID：<span class="font-mono">{{ oauthSession.session_id }}</span>
            </div>
            <Badge variant="info">{{ oauthSession.status }}</Badge>
          </div>

          <div class="flex flex-wrap items-center gap-2">
            <Input :model-value="oauthSession.auth_url" size="sm" readonly class="flex-1 font-mono text-xs" />
            <Button variant="secondary" size="sm" @click="copyOAuthUrl">
              <template #leading><Icon name="copy" :size="12" /></template>
              复制
            </Button>
            <a
              :href="oauthSession.auth_url"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex h-8 items-center gap-1 rounded-[var(--radius-md)] bg-[var(--accent)] px-3 text-xs font-medium text-white hover:opacity-90"
            >
              <Icon name="external" :size="12" />
              打开 Studio 登录
            </a>
          </div>

          <Field label="2. 粘贴浏览器回调 URL（含 ?token=...&state=...）或直接粘贴 user_ Token">
            <Input
              v-model="oauthCallbackInput"
              size="sm"
              placeholder="http://localhost:.../callback?token=user_xxx&state=... 或 user_xxx"
            />
          </Field>
        </div>
      </div>

      <template #footer>
        <Button variant="secondary" size="sm" @click="oauthOpen = false">取消</Button>
        <Button
          variant="primary"
          size="sm"
          :loading="oauthSubmitting"
          :disabled="!oauthSession || !oauthCallbackInput.trim()"
          @click="completeCcOAuth"
        >
          验证 Whoami 并入池
        </Button>
      </template>
    </Modal>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除 Command Code 账号"
      :message="`确认删除账号「${deleteTarget?.name ?? ''}」吗？`"
      confirm-text="删除"
      tone="danger"
      :loading="deleting"
      @update:open="deleteTarget = null"
      @confirm="confirmDelete"
    />
  </div>
</template>
