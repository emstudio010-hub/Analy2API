<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import AccountFormModal from '@/components/accounts/AccountFormModal.vue'
import BatchImportModal from '@/components/accounts/BatchImportModal.vue'
import HarvestSummaryModal from '@/components/accounts/HarvestSummaryModal.vue'
import LoginLinkModal from '@/components/accounts/LoginLinkModal.vue'
import PackagesModal from '@/components/accounts/PackagesModal.vue'
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
import { accountsApi, groupsApi, oauthApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type {
  Account,
  Group,
  HarvestAllResult,
  HarvestSummary,
  LoginLinkResult,
  OAuthSession,
  ProxyTestResult,
  ScanLocalResult,
} from '@/api/types'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'
import {
  formatCredits,
  formatDateTime,
  formatExpiry,
  formatFingerprint,
  formatRelative,
  isExpired,
  isTodayDate,
  redactProxyDisplay,
} from '@/utils/format'
import { platformMeta } from '@/utils/platforms'

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

const formOpen = ref(false)
const editing = ref<Account | null>(null)
const batchOpen = ref(false)
const deleteTarget = ref<Account | null>(null)
const deleting = ref(false)

// 单行操作状态
const checkingId = ref<number | null>(null)
const togglingId = ref<number | null>(null)
const balanceLoadingId = ref<number | null>(null)
const proxyTestingId = ref<number | null>(null)
const rotatingId = ref<number | null>(null)
const refreshingTokenId = ref<number | null>(null)
const harvestingId = ref<number | null>(null)

// 全局操作状态
const balanceAllLoading = ref(false)
const scanLoading = ref(false)
const harvestAllLoading = ref(false)
const exporting = ref(false)
const scanNotice = ref<ScanLocalResult | null>(null)

// 行内缓存的代理测速结果
const proxyTests = ref<Record<number, ProxyTestResult>>({})

// 官网免密登录弹窗
const loginModalOpen = ref(false)
const loginTarget = ref<Account | null>(null)
const loginResult = ref<LoginLinkResult | null>(null)
const loginLoading = ref(false)
const loginError = ref('')

// 资源包明细弹窗
const packagesAccount = ref<Account | null>(null)

// 收割摘要弹窗（支持单账号与全量批量）
const harvestModalOpen = ref(false)
const harvestModalTitle = ref('')
const harvestModalSubtitle = ref('')
const harvestModalSummary = ref<HarvestSummary | null>(null)
const harvestModalCredits = ref<number | null>(null)
const harvestModalAt = ref<string | null>(null)
const harvestModalBatch = ref<HarvestAllResult | null>(null)

const statusOptions = [
  { label: '正常', value: 'active' },
  { label: '已禁用', value: 'disabled' },
  { label: '异常', value: 'error' },
]

const workbuddyGroups = computed(() =>
  groups.value.filter((g) => !g.platform || g.platform === 'workbuddy'),
)

const groupOptions = computed(() =>
  workbuddyGroups.value.map((g) => ({ label: g.name, value: String(g.id) })),
)

const poolStats = computed(() => {
  let active = 0
  let checkedToday = 0
  let credits = 0
  let withProxy = 0
  for (const acc of items.value) {
    if (acc.status === 'active') active += 1
    if (isTodayDate(acc.last_checkin_date)) checkedToday += 1
    const c = acc.balance?.total_credits ?? acc.balance?.remaining ?? 0
    if (c > 0) credits += c
    if (acc.proxy_url && acc.proxy_url.toLowerCase() !== 'direct') withProxy += 1
  }
  return { active, checkedToday, credits, withProxy }
})

const filtersActive = computed(
  () => Boolean(keyword.value.trim()) || Boolean(status.value) || Boolean(groupFilter.value),
)

const groupNameById = computed(() => {
  const map = new Map<number, string>()
  for (const group of groups.value) map.set(group.id, group.name)
  return map
})

const emptyDescription = computed(() =>
  filtersActive.value
    ? '当前筛选条件下没有账号，试试放宽关键词或清空筛选。'
    : '还没有 WorkBuddy 账号。你可以点击「扫描本机账号」一键导入本地已登录会话，或者通过「批量导入 JSON/Token」与「新建账号」接入。',
)

let searchTimer: ReturnType<typeof setTimeout> | null = null
let suppressFilterWatch = false

const selectedIds = ref<number[]>([])
const batchOperating = ref(false)

const allVisibleSelected = computed(
  () => items.value.length > 0 && items.value.every((acc) => selectedIds.value.includes(acc.id)),
)

function toggleSelectAll(checked: boolean): void {
  if (checked) {
    selectedIds.value = items.value.map((acc) => acc.id)
  } else {
    selectedIds.value = []
  }
}

function toggleSelectRow(id: number, checked: boolean): void {
  if (checked) {
    if (!selectedIds.value.includes(id)) selectedIds.value = [...selectedIds.value, id]
  } else {
    selectedIds.value = selectedIds.value.filter((x) => x !== id)
  }
}

async function runBatchStatus(nextStatus: 'active' | 'disabled'): Promise<void> {
  if (batchOperating.value || selectedIds.value.length === 0) return
  batchOperating.value = true
  let ok = 0
  let fail = 0
  try {
    for (const id of selectedIds.value) {
      try {
        await accountsApi.update(id, { status: nextStatus })
        ok += 1
      } catch {
        fail += 1
      }
    }
    toast.success(
      nextStatus === 'active' ? '批量启用完成' : '批量禁用完成',
      `成功 ${ok} 个${fail > 0 ? ` · 失败 ${fail} 个` : ''}`,
    )
    selectedIds.value = []
    await load()
  } finally {
    batchOperating.value = false
  }
}

async function runBatchCheck(): Promise<void> {
  if (batchOperating.value || selectedIds.value.length === 0) return
  batchOperating.value = true
  let ok = 0
  let fail = 0
  try {
    for (const id of selectedIds.value) {
      try {
        const res = await accountsApi.check(id)
        if (res.ok) ok += 1
        else fail += 1
      } catch {
        fail += 1
      }
    }
    toast.info('批量探活完成', `正常 ${ok} 个 · 异常 ${fail} 个`)
    await load()
  } finally {
    batchOperating.value = false
  }
}

async function loadGroups(): Promise<void> {
  try {
    const response = await groupsApi.list()
    groups.value = response.items ?? []
  } catch (err) {
    toast.error('分组列表加载失败', toMessage(err))
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const response = await accountsApi.list({
      platform: 'workbuddy',
      page: page.value,
      page_size: pageSize.value,
      status: status.value || undefined,
      keyword: keyword.value.trim() || undefined,
      group_id: groupFilter.value ? Number(groupFilter.value) : undefined,
    })
    items.value = response.items ?? []
    total.value = response.total ?? 0
    const visibleSet = new Set(items.value.map((a) => a.id))
    selectedIds.value = selectedIds.value.filter((id) => visibleSet.has(id))
    // 若资源包弹窗正开着，顺带同步最新账号引用
    if (packagesAccount.value) {
      const updated = items.value.find((item) => item.id === packagesAccount.value?.id)
      if (updated) packagesAccount.value = updated
    }
  } catch (err) {
    error.value = toMessage(err, '加载账号列表失败')
    items.value = []
    total.value = 0
    selectedIds.value = []
  } finally {
    loading.value = false
  }
}

function reload(): void {
  void load()
}

function replaceAccountInPlace(updated: Account): void {
  const index = items.value.findIndex((item) => item.id === updated.id)
  if (index >= 0) {
    items.value[index] = updated
  }
  if (packagesAccount.value?.id === updated.id) {
    packagesAccount.value = updated
  }
}

watch(keyword, () => {
  if (suppressFilterWatch) return
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    if (page.value !== 1) page.value = 1
    else void load()
  }, 300)
})

watch([status, groupFilter], () => {
  if (suppressFilterWatch) return
  if (page.value !== 1) page.value = 1
  else void load()
})

watch([page, pageSize], () => {
  if (suppressFilterWatch) return
  void load()
})

onMounted(async () => {
  await Promise.all([load(), loadGroups()])
})

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
})

function openCreate(): void {
  editing.value = null
  formOpen.value = true
}

function openEdit(account: Account): void {
  editing.value = account
  formOpen.value = true
}

function clearFilters(): void {
  if (searchTimer) {
    clearTimeout(searchTimer)
    searchTimer = null
  }
  suppressFilterWatch = true
  keyword.value = ''
  status.value = ''
  groupFilter.value = ''
  page.value = 1
  queueMicrotask(() => {
    suppressFilterWatch = false
    void load()
  })
}

async function scanLocalAccounts(): Promise<void> {
  if (scanLoading.value) return
  scanLoading.value = true
  try {
    const res = await accountsApi.scanLocal({
      platform: 'workbuddy',
      auto_assign_default_group: true,
    })
    scanNotice.value = res
    const msg = `新增 ${res.imported} 个 · 更新 ${res.updated} 个`
    if (res.errors?.length) {
      toast.warning('本机扫描完成（部分来源有警告）', `${msg} · ${res.errors[0]}`)
    } else {
      toast.success('本机账号扫描完成', msg)
    }
    await Promise.all([load(), loadGroups()])
  } catch (err) {
    toast.error('扫描本机账号失败', toMessage(err))
  } finally {
    scanLoading.value = false
  }
}

async function exportAllAccounts(): Promise<void> {
  if (exporting.value) return
  exporting.value = true
  try {
    const res = await accountsApi.exportAccounts('workbuddy')
    const payload = JSON.stringify(res, null, 2)
    const blob = new Blob([payload], { type: 'application/json;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19)
    link.href = url
    link.download = `analy2api-accounts-${stamp}.json`
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    URL.revokeObjectURL(url)
    toast.success('导出成功', `已导出 ${res.count} 个账号配置`)
  } catch (err) {
    toast.error('导出账号失败', toMessage(err))
  } finally {
    exporting.value = false
  }
}

async function testAccountProxy(account: Account): Promise<void> {
  if (proxyTestingId.value !== null) return
  proxyTestingId.value = account.id
  try {
    const res = await accountsApi.testProxy(account.id)
    proxyTests.value = { ...proxyTests.value, [account.id]: res }
    if (res.ok) {
      toast.success(
        `「${account.name}」代理正常`,
        `出口 IP: ${res.exit_ip || '未知'} · 延迟 ${res.latency_ms} ms`,
      )
    } else {
      toast.error(`「${account.name}」代理异常`, res.error || '代理连通性测试失败')
    }
  } catch (err) {
    toast.error('代理测速失败', toMessage(err))
  } finally {
    proxyTestingId.value = null
  }
}

async function rotateAccountDevice(account: Account): Promise<void> {
  if (rotatingId.value !== null) return
  rotatingId.value = account.id
  try {
    const res = await accountsApi.rotateDevice(account.id)
    if (res.account) replaceAccountInPlace(res.account)
    else await load()
    toast.success(
      `「${account.name}」设备指纹已轮换`,
      `新 qimei36: ${formatFingerprint(res.qimei36, 12)}`,
    )
  } catch (err) {
    toast.error('轮换设备指纹失败', toMessage(err))
  } finally {
    rotatingId.value = null
  }
}

async function openLoginLink(account: Account, target = ''): Promise<void> {
  loginTarget.value = account
  loginModalOpen.value = true
  loginLoading.value = true
  loginError.value = ''
  loginResult.value = null
  try {
    loginResult.value = await accountsApi.loginLink(account.id, target || undefined)
  } catch (err) {
    loginError.value = toMessage(err, '生成免密登录链接失败')
  } finally {
    loginLoading.value = false
  }
}

async function regenerateLoginLink(target: string): Promise<void> {
  if (!loginTarget.value || loginLoading.value) return
  await openLoginLink(loginTarget.value, target)
}

async function harvestOne(account: Account): Promise<void> {
  if (harvestingId.value !== null) return
  harvestingId.value = account.id
  try {
    const res = await accountsApi.harvest(account.id, false)
    if (res.account) replaceAccountInPlace(res.account)
    else await load()

    if (res.ok) {
      toast.success(
        `「${account.name}」签到收割完成`,
        `获得 +${formatCredits(res.credit_gained)} 积分${res.total_credits !== null ? ` · 当前总积分 ${formatCredits(res.total_credits)}` : ''}`,
      )
    } else {
      toast.error(`「${account.name}」签到收割异常`, res.error || '部分步骤失败')
    }

    harvestModalTitle.value = `收割结果 · ${account.name}`
    harvestModalSubtitle.value = account.uid ? `UID ${account.uid}` : ''
    harvestModalBatch.value = null
    harvestModalSummary.value = {
      credit_gained: res.credit_gained,
      error: res.error,
      steps: res.steps,
    }
    harvestModalCredits.value = res.total_credits
    harvestModalAt.value = res.account?.last_harvest_at ?? new Date().toISOString()
    harvestModalOpen.value = true
  } catch (err) {
    toast.error('签到收割失败', toMessage(err))
  } finally {
    harvestingId.value = null
  }
}

async function runHarvestAll(): Promise<void> {
  if (harvestAllLoading.value) return
  harvestAllLoading.value = true
  try {
    const res = await accountsApi.harvestAll(false)
    if (Array.isArray(res.accounts) && res.accounts.length > 0) {
      const byId = new Map(res.accounts.map((acc) => [acc.id, acc]))
      items.value = items.value.map((item) => byId.get(item.id) ?? item)
    } else {
      await load()
    }

    if (res.failed > 0) {
      toast.warning(
        '全量签到收割完成（有部分失败）',
        `成功 ${res.succeeded} / ${res.total} · 累计 +${formatCredits(res.credit_gained)} 积分`,
      )
    } else {
      toast.success(
        '全量签到收割完成',
        `共 ${res.succeeded} 个账号 · 累计 +${formatCredits(res.credit_gained)} 积分`,
      )
    }

    harvestModalTitle.value = '一键全量签到收割结果'
    harvestModalSubtitle.value = `共处理 ${res.total} 个账号`
    harvestModalBatch.value = res
    harvestModalSummary.value = null
    harvestModalCredits.value = null
    harvestModalAt.value = null
    harvestModalOpen.value = true
  } catch (err) {
    toast.error('全量签到收割失败', toMessage(err))
  } finally {
    harvestAllLoading.value = false
  }
}

function viewLastHarvest(account: Account): void {
  if (!account.last_harvest_summary) return
  harvestModalTitle.value = `上次收割摘要 · ${account.name}`
  harvestModalSubtitle.value = account.uid ? `UID ${account.uid}` : ''
  harvestModalBatch.value = null
  harvestModalSummary.value = account.last_harvest_summary
  harvestModalCredits.value = totalCreditsOf(account)
  harvestModalAt.value = account.last_harvest_at
  harvestModalOpen.value = true
}

async function refreshAccountToken(account: Account): Promise<void> {
  if (refreshingTokenId.value !== null) return
  refreshingTokenId.value = account.id
  try {
    const res = await accountsApi.refreshToken(account.id)
    if (res.account) replaceAccountInPlace(res.account)
    else await load()
    if (res.ok) {
      toast.success(`「${account.name}」Token 已刷新`)
    } else {
      toast.error(`「${account.name}」刷新 Token 失败`, res.error || '请检查 Refresh Token 是否有效')
    }
  } catch (err) {
    toast.error('刷新 Token 失败', toMessage(err))
  } finally {
    refreshingTokenId.value = null
  }
}

async function check(account: Account): Promise<void> {
  if (checkingId.value !== null) return
  checkingId.value = account.id
  try {
    const result = await accountsApi.check(account.id)
    if (result.ok) {
      toast.success(`「${account.name}」探活正常`, `耗时 ${result.latency_ms} ms`)
    } else {
      toast.error(`「${account.name}」探活失败`, result.error ?? '上游未返回可用响应')
    }
    await load()
  } catch (err) {
    toast.error('探活失败', toMessage(err))
  } finally {
    checkingId.value = null
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
    toast.error('修改状态失败', toMessage(err))
  } finally {
    togglingId.value = null
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  try {
    await accountsApi.remove(target.id)
    toast.success('账号已删除', target.name)
    deleteTarget.value = null
    if (items.value.length === 1 && page.value > 1) page.value -= 1
    else await load()
  } catch (err) {
    toast.error('删除账号失败', toMessage(err))
  } finally {
    deleting.value = false
  }
}

async function refreshBalance(account: Account): Promise<void> {
  if (balanceLoadingId.value !== null) return
  balanceLoadingId.value = account.id
  try {
    const result = await accountsApi.refreshBalance(account.id)
    if (result.ok) {
      const updated = result.account ?? account
      if (result.account) replaceAccountInPlace(result.account)
      toast.success(
        `「${account.name}」积分已刷新`,
        `总积分 ${formatCredits(totalCreditsOf(updated))} · 资源包 ${updated.balance?.packages?.length ?? 0} 个`,
      )
    } else {
      toast.error(`「${account.name}」积分获取失败`, result.error ?? '上游未返回可用数据')
      await load()
    }
  } catch (err) {
    toast.error('刷新积分失败', toMessage(err))
  } finally {
    balanceLoadingId.value = null
  }
}

async function refreshAllBalances(): Promise<void> {
  if (balanceAllLoading.value) return
  balanceAllLoading.value = true
  try {
    const result = await accountsApi.refreshAllBalances('workbuddy')
    if (Array.isArray(result.accounts) && result.accounts.length > 0) {
      const byId = new Map(result.accounts.map((acc) => [acc.id, acc]))
      items.value = items.value.map((item) => byId.get(item.id) ?? item)
    }
    const parts = [`成功 ${result.refreshed}`]
    if (result.failed) parts.push(`失败 ${result.failed}`)
    if (result.failed) {
      toast.error('刷新全部积分：有账号失败', parts.join(' / '))
    } else {
      toast.success(`已刷新 ${result.total} 个账号的积分`, parts.join(' / '))
    }
  } catch (err) {
    toast.error('刷新全部积分失败', toMessage(err))
  } finally {
    balanceAllLoading.value = false
  }
}

function totalCreditsOf(account: Account): number | null {
  return account.balance?.total_credits ?? account.balance?.remaining ?? null
}

function packagesOf(account: Account) {
  return account.balance?.packages ?? []
}

function healthOf(account: Account): { tone: 'success' | 'danger' | 'neutral'; text: string } {
  if (!account.last_health_check_at) return { tone: 'neutral', text: '未检测' }
  if (account.last_health_check_ok) {
    return {
      tone: 'success',
      text: account.latency_ms !== null ? `正常 ${account.latency_ms} ms` : '正常',
    }
  }
  return { tone: 'danger', text: '异常' }
}

function formatDomainHost(domain: string): string {
  if (!domain) return ''
  return domain.replace(/^https?:\/\//i, '').replace(/\/$/, '')
}

// WorkBuddy 统一 OAuth 授权入池向导
const wbOAuthOpen = ref(false)
const wbOAuthProxyUrl = ref('')
const wbOAuthDomain = ref('https://www.codebuddy.cn')
const wbOAuthAccountName = ref('')
const wbOAuthRedirectMode = ref(true)
const wbOAuthGroupIds = ref<number[]>([])
const wbOAuthSession = ref<OAuthSession | null>(null)
const wbOAuthCallbackInput = ref('')
const wbOAuthRefreshToken = ref('')
const wbOAuthUid = ref('')
const wbOAuthCreating = ref(false)
const wbOAuthSubmitting = ref(false)
const wbOAuthError = ref('')

function openWbOAuthModal(): void {
  wbOAuthError.value = ''
  wbOAuthProxyUrl.value = ''
  wbOAuthDomain.value = 'https://www.codebuddy.cn'
  wbOAuthAccountName.value = ''
  wbOAuthRedirectMode.value = true
  wbOAuthCallbackInput.value = ''
  wbOAuthRefreshToken.value = ''
  wbOAuthUid.value = ''
  wbOAuthSession.value = null
  wbOAuthGroupIds.value = workbuddyGroups.value.slice(0, 1).map((g) => g.id)
  wbOAuthOpen.value = true
}

function toggleWbOAuthGroup(groupId: number): void {
  const idx = wbOAuthGroupIds.value.indexOf(groupId)
  if (idx >= 0) {
    wbOAuthGroupIds.value.splice(idx, 1)
  } else {
    wbOAuthGroupIds.value.push(groupId)
  }
}

async function startWbOAuth(): Promise<void> {
  if (wbOAuthCreating.value) return
  wbOAuthCreating.value = true
  wbOAuthError.value = ''
  try {
    const res = await oauthApi.createSession({
      platform: 'workbuddy',
      proxy_url: wbOAuthProxyUrl.value.trim() || undefined,
      group_ids: wbOAuthGroupIds.value.length > 0 ? wbOAuthGroupIds.value : undefined,
      redirect_mode: wbOAuthRedirectMode.value,
      account_name: wbOAuthAccountName.value.trim() || undefined,
    })
    wbOAuthSession.value = res.session
    toast.success('WorkBuddy OAuth 会话已创建', '请点击授权链接登录或粘贴回调 URL / AccessToken / JSON')
  } catch (err) {
    wbOAuthError.value = toMessage(err, '生成 WorkBuddy OAuth 会话失败')
  } finally {
    wbOAuthCreating.value = false
  }
}

async function copyWbOAuthUrl(): Promise<void> {
  if (!wbOAuthSession.value?.auth_url) return
  const ok = await copyText(wbOAuthSession.value.auth_url)
  if (ok) toast.success('已复制授权链接')
  else toast.error('复制失败', '请手动复制输入框内的链接')
}

async function completeWbOAuth(): Promise<void> {
  if (!wbOAuthSession.value || !wbOAuthCallbackInput.value.trim() || wbOAuthSubmitting.value) return
  wbOAuthSubmitting.value = true
  wbOAuthError.value = ''
  try {
    const raw = wbOAuthCallbackInput.value.trim()
    const basePayload = {
      session_id: wbOAuthSession.value.session_id,
      proxy_url: wbOAuthProxyUrl.value.trim() || undefined,
      domain: wbOAuthDomain.value.trim() || undefined,
      account_name: wbOAuthAccountName.value.trim() || undefined,
      refresh_token: wbOAuthRefreshToken.value.trim() || undefined,
      uid: wbOAuthUid.value.trim() || undefined,
    }
    const payload =
      raw.startsWith('{')
        ? { ...basePayload, raw_json: raw }
        : raw.startsWith('http://') || raw.startsWith('https://') || raw.includes('=')
          ? { ...basePayload, callback_url: raw }
          : { ...basePayload, api_key: raw }
    const res = await oauthApi.completeCallback(payload)
    if (res.session) {
      wbOAuthSession.value = res.session
    }
    toast.success('WorkBuddy OAuth 授权入池完成', res.account?.name ? `账号 ${res.account.name}` : res.message)
    await load()
    wbOAuthOpen.value = false
  } catch (err) {
    wbOAuthError.value = toMessage(err, '完成 WorkBuddy OAuth 回调失败')
  } finally {
    wbOAuthSubmitting.value = false
  }
}
</script>

<template>
  <div class="space-y-3.5">
    <!-- WorkBuddy 池概览指标 -->
    <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <MetricTile
        label="WorkBuddy 账号池"
        :value="`${poolStats.active} / ${total}`"
        hint="当前页正常可用 / 总账号数"
        icon="layers"
        tone="accent"
      />
      <MetricTile
        label="当前页可用总积分"
        :value="formatCredits(poolStats.credits)"
        hint="汇总各账号剩余积分包额度"
        icon="coins"
        tone="success"
      />
      <MetricTile
        label="今日签到进度"
        :value="`${poolStats.checkedToday} / ${items.length}`"
        hint="支持一键全量零成本签到收割"
        icon="gift"
        tone="info"
      />
      <MetricTile
        label="独立代理隔离"
        :value="`${poolStats.withProxy} / ${items.length}`"
        hint="成对绑定 qimei36 与独立出口 IP"
        icon="globe"
        tone="neutral"
      />
    </div>

    <!-- 顶部工具条 -->
    <div class="card flex flex-wrap items-end gap-2.5 px-3.5 py-2.5">
      <div class="w-full min-w-[12rem] sm:w-56">
        <Input v-model="keyword" size="sm" placeholder="搜索名称 / UID / 邮箱 / 代理" />
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
          :loading="scanLoading"
          title="扫描本机 WorkBuddy / agent-switch / workbuddy-sdk 已登录账号并自动导入"
          @click="scanLocalAccounts"
        >
          <template #leading><Icon name="scan" :size="13" /></template>
          扫描本机账号
        </Button>

        <Button
          variant="secondary"
          size="sm"
          :loading="harvestAllLoading"
          title="对全部可用账号执行一次零成本签到、任务领取与盲盒收割"
          @click="runHarvestAll"
        >
          <template #leading><Icon name="gift" :size="13" /></template>
          一键全量签到收割
        </Button>

        <Button
          variant="secondary"
          size="sm"
          :loading="balanceAllLoading"
          title="只读查询全部账号的总积分与资源包明细，不消耗额度"
          @click="refreshAllBalances"
        >
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新全部积分
        </Button>

        <Button variant="secondary" size="sm" @click="openWbOAuthModal">
          <template #leading><Icon name="link" :size="13" /></template>
          OAuth 授权入池
        </Button>

        <Button variant="secondary" size="sm" @click="batchOpen = true">
          <template #leading><Icon name="upload" :size="13" /></template>
          批量导入 JSON/Token
        </Button>

        <Button
          variant="secondary"
          size="sm"
          :loading="exporting"
          @click="exportAllAccounts"
        >
          <template #leading><Icon name="download" :size="13" /></template>
          导出 JSON
        </Button>

        <Button variant="secondary" size="sm" icon title="刷新列表" :loading="loading" @click="reload">
          <Icon name="refresh" :size="13" />
        </Button>

        <Button variant="primary" size="sm" @click="openCreate">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建账号
        </Button>
      </div>
    </div>

    <!-- 扫描本机结果回执 -->
    <div
      v-if="scanNotice"
      class="flex flex-wrap items-center gap-2 rounded-panel border border-accent/30 bg-accent/8 px-3 py-2 text-2xs"
    >
      <Icon name="checkCircle" :size="14" class="shrink-0 text-accent" />
      <span class="font-medium text-fg">
        本机扫描完成：新增 {{ scanNotice.imported }} 个 · 更新 {{ scanNotice.updated }} 个
      </span>
      <span v-if="scanNotice.sources?.length" class="truncate font-mono text-subtle">
        来源：{{ scanNotice.sources.join(' , ') }}
      </span>
      <button
        type="button"
        class="ml-auto rounded p-0.5 text-subtle transition hover:bg-raised hover:text-fg"
        aria-label="关闭提示"
        @click="scanNotice = null"
      >
        <Icon name="close" :size="13" />
      </button>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="13" />
      {{ error }}
      <Button variant="ghost" size="xs" class="ml-auto" @click="reload">重试</Button>
    </p>

    <!-- 批量选中操作条 -->
    <div
      v-if="selectedIds.length > 0"
      class="flex flex-wrap items-center justify-between gap-2 rounded-panel border border-accent/35 bg-accent/10 px-3.5 py-2 text-xs"
    >
      <div class="flex items-center gap-2 text-fg">
        <Badge tone="accent" dot>已选 {{ selectedIds.length }} 个账号</Badge>
        <span class="text-2xs text-muted">可一键批量探活或启停选中账号</span>
      </div>
      <div class="flex flex-wrap items-center gap-1.5">
        <Button variant="secondary" size="xs" :loading="batchOperating" @click="runBatchCheck">
          <template #leading><Icon name="zap" :size="12" /></template>
          批量探活
        </Button>
        <Button variant="secondary" size="xs" :loading="batchOperating" @click="runBatchStatus('active')">
          <template #leading><Icon name="checkCircle" :size="12" /></template>
          批量启用
        </Button>
        <Button variant="secondary" size="xs" :loading="batchOperating" @click="runBatchStatus('disabled')">
          <template #leading><Icon name="lock" :size="12" /></template>
          批量禁用
        </Button>
        <Button variant="ghost" size="xs" :disabled="batchOperating" @click="selectedIds = []">
          取消选择
        </Button>
      </div>
    </div>

    <DataTable
      :columns="10"
      :loading="loading"
      :empty="items.length === 0"
      :skeleton-rows="8"
      min-width="1580px"
      hoverable
    >
      <template #head>
        <th class="th w-9">
          <Checkbox :model-value="allVisibleSelected" @update:model-value="toggleSelectAll" />
        </th>
        <th class="th">账号</th>
        <th class="th">状态 / 健康</th>
        <th class="th">分组</th>
        <th class="th">独立代理 IP</th>
        <th class="th">设备指纹 (qimei36)</th>
        <th class="th">积分与资源包</th>
        <th class="th">签到与收割</th>
        <th class="th">调度 / 过期</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="layers"
          :title="filtersActive ? '没有匹配的账号' : '还没有任何 WorkBuddy 上游账号'"
          :description="emptyDescription"
        >
          <template v-if="filtersActive">
            <Button variant="secondary" size="sm" @click="clearFilters">清空筛选</Button>
          </template>
          <template v-else>
            <Button variant="secondary" size="sm" :loading="scanLoading" @click="scanLocalAccounts">
              <template #leading><Icon name="scan" :size="13" /></template>
              扫描本机账号
            </Button>
            <Button variant="secondary" size="sm" @click="batchOpen = true">
              <template #leading><Icon name="upload" :size="13" /></template>
              批量导入 JSON/Token
            </Button>
            <Button variant="primary" size="sm" @click="openCreate">
              <template #leading><Icon name="plus" :size="13" /></template>
              新建账号
            </Button>
          </template>
        </EmptyState>
      </template>

      <template #body>
        <tr
          v-for="account in items"
          :key="account.id"
          :class="selectedIds.includes(account.id) ? 'bg-accent/5' : ''"
        >
          <td class="td w-9">
            <Checkbox
              :model-value="selectedIds.includes(account.id)"
              @update:model-value="toggleSelectRow(account.id, $event)"
            />
          </td>
          <!-- 账号 -->
          <td class="td max-w-[17rem]">
            <div class="flex flex-col gap-0.5">
              <div class="flex items-center gap-1.5">
                <span class="truncate font-medium text-fg">{{ account.name }}</span>
                <Badge :tone="platformMeta(account.platform).tone" dot>
                  {{ platformMeta(account.platform).label }}
                </Badge>
                <Badge v-if="!account.schedulable" tone="warning">不调度</Badge>
              </div>

              <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-2xs text-muted">
                <span v-if="account.uid" class="font-mono text-fg/80" title="UID">
                  UID: {{ account.uid }}
                </span>
                <span v-if="account.email || account.nickname" class="truncate">
                  {{ account.email || account.nickname }}
                </span>
                <span v-if="account.enterprise_name" class="truncate text-subtle">
                  {{ account.enterprise_name }}
                </span>
              </div>

              <div class="flex flex-wrap items-center gap-1.5 text-2xs">
                <template v-if="account.masked_key">
                  <span class="font-mono text-subtle">{{ account.masked_key }}</span>
                </template>
                <template v-else>
                  <span
                    class="inline-flex items-center gap-1 text-warning"
                    title="库里的凭证无法解密，请重新编辑填写 Access Token。"
                  >
                    <Icon name="alert" :size="11" />
                    凭证无法解密
                  </span>
                </template>
                <span v-if="account.domain" class="font-mono text-subtle">
                  · {{ formatDomainHost(account.domain) }}
                </span>
              </div>

              <p v-if="account.notes" class="truncate text-2xs text-subtle">{{ account.notes }}</p>
            </div>
          </td>

          <!-- 状态与健康 -->
          <td class="td">
            <div class="flex flex-col items-start gap-1">
              <StatusPill
                :status="account.status"
                :label="account.status === 'active' ? '正常' : undefined"
              />
              <span
                class="inline-flex items-center gap-1 text-2xs"
                :class="{
                  'text-success': healthOf(account).tone === 'success',
                  'text-danger': healthOf(account).tone === 'danger',
                  'text-subtle': healthOf(account).tone === 'neutral',
                }"
              >
                <span class="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" />
                {{ healthOf(account).text }}
              </span>
              <span
                v-if="account.error_message || account.last_health_check_error"
                class="max-w-[10rem] truncate text-2xs text-danger"
                :title="account.error_message || account.last_health_check_error || ''"
              >
                {{ account.error_message || account.last_health_check_error }}
              </span>
              <span v-if="account.consecutive_failures > 0" class="text-2xs text-warning">
                连续失败 {{ account.consecutive_failures }} 次
              </span>
            </div>
          </td>

          <!-- 分组 -->
          <td class="td max-w-[10rem]">
            <div v-if="(account.group_ids ?? []).length" class="flex flex-wrap gap-1">
              <Badge
                v-for="groupId in (account.group_ids ?? []).slice(0, 2)"
                :key="groupId"
                tone="neutral"
              >
                {{ groupNameById.get(groupId) ?? `#${groupId}` }}
              </Badge>
              <Badge v-if="(account.group_ids ?? []).length > 2" tone="neutral">
                +{{ (account.group_ids ?? []).length - 2 }}
              </Badge>
            </div>
            <span v-else class="text-2xs text-warning">未绑定分组</span>
          </td>

          <!-- 独立代理 IP -->
          <td class="td max-w-[14rem]">
            <div class="flex flex-col gap-1">
              <div class="flex items-center gap-1.5">
                <Badge
                  v-if="!account.proxy_url"
                  tone="neutral"
                >
                  默认/直连
                </Badge>
                <span
                  v-else
                  class="max-w-[9.5rem] truncate font-mono text-2xs text-fg"
                  :title="redactProxyDisplay(account.proxy_url)"
                >
                  {{ redactProxyDisplay(account.proxy_url) }}
                </span>

                <Button
                  variant="ghost"
                  size="xs"
                  class="ml-auto shrink-0"
                  title="通过该账号独立代理测速并查看出口 IP"
                  :disabled="proxyTestingId !== null"
                  @click="testAccountProxy(account)"
                >
                  <Spinner v-if="proxyTestingId === account.id" :size="11" />
                  <template v-else>测速</template>
                </Button>
              </div>

              <span
                v-if="proxyTests[account.id]"
                class="truncate font-mono text-2xs"
                :class="proxyTests[account.id]?.ok ? 'text-success' : 'text-danger'"
                :title="proxyTests[account.id]?.error || `出口 IP: ${proxyTests[account.id]?.exit_ip}`"
              >
                {{
                  proxyTests[account.id]?.ok
                    ? `${proxyTests[account.id]?.exit_ip || '连通'} · ${proxyTests[account.id]?.latency_ms}ms`
                    : `失败：${proxyTests[account.id]?.error || '不可达'}`
                }}
              </span>
            </div>
          </td>

          <!-- 设备指纹 -->
          <td class="td">
            <div class="flex items-center gap-1.5">
              <code
                class="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-muted"
                :title="`qimei36: ${account.qimei36 || '未生成'}\nmachine_id: ${account.machine_id || '未生成'}`"
              >
                {{ formatFingerprint(account.qimei36, 8) }}
              </code>
              <Button
                variant="ghost"
                size="xs"
                title="立即轮换该账号的 qimei36 与 machine_id 设备指纹对"
                :disabled="rotatingId !== null"
                @click="rotateAccountDevice(account)"
              >
                <Spinner v-if="rotatingId === account.id" :size="11" />
                <template v-else>轮换</template>
              </Button>
            </div>
          </td>

          <!-- 积分与资源包 -->
          <td class="td min-w-[11rem]">
            <div class="flex flex-col gap-0.5">
              <div class="flex items-center gap-1.5">
                <span class="tnum text-[13px] font-semibold text-fg">
                  {{ formatCredits(totalCreditsOf(account)) }}
                </span>
                <span class="text-2xs text-subtle">积分</span>
                <Button
                  variant="ghost"
                  size="xs"
                  icon
                  class="ml-auto"
                  title="刷新积分与资源包"
                  :disabled="balanceLoadingId !== null"
                  @click="refreshBalance(account)"
                >
                  <Spinner v-if="balanceLoadingId === account.id" :size="11" />
                  <Icon v-else name="refresh" :size="11" />
                </Button>
              </div>

              <div class="flex flex-wrap items-center gap-1.5">
                <button
                  v-if="packagesOf(account).length > 0"
                  type="button"
                  class="text-2xs font-medium text-accent transition hover:underline"
                  @click="packagesAccount = account"
                >
                  资源包 ({{ packagesOf(account).length }})
                </button>
                <span v-else class="text-2xs text-subtle">无资源包明细</span>
              </div>

              <span
                v-if="account.balance?.error"
                class="max-w-[11rem] truncate text-2xs text-danger"
                :title="account.balance.error"
              >
                {{ account.balance.error }}
              </span>
              <span v-else-if="account.balance?.fetched_at" class="text-2xs text-subtle">
                {{ formatRelative(account.balance.fetched_at) }}
              </span>
              <span v-else class="text-2xs text-subtle">未获取</span>
            </div>
          </td>

          <!-- 签到与收割 -->
          <td class="td">
            <div class="flex flex-col items-start gap-0.5">
              <div class="flex items-center gap-1.5">
                <Badge v-if="isTodayDate(account.last_checkin_date)" tone="success" dot>
                  今日已签
                </Badge>
                <Badge v-else tone="neutral">
                  {{ account.last_checkin_date || '未签到' }}
                </Badge>
                <span v-if="account.checkin_streak > 0" class="tnum text-2xs text-muted">
                  连签 {{ account.checkin_streak }} 天
                </span>
              </div>

              <div class="flex items-center gap-1.5 text-2xs">
                <span v-if="account.last_harvest_at" class="text-subtle">
                  {{ formatRelative(account.last_harvest_at) }}
                </span>
                <button
                  v-if="account.last_harvest_summary"
                  type="button"
                  class="font-medium text-accent transition hover:underline"
                  @click="viewLastHarvest(account)"
                >
                  摘要
                  <template v-if="account.last_harvest_summary.credit_gained !== undefined">
                    (+{{ formatCredits(account.last_harvest_summary.credit_gained) }})
                  </template>
                </button>
              </div>
            </div>
          </td>

          <!-- 调度与过期 -->
          <td class="td">
            <div class="flex flex-col gap-0.5 text-2xs">
              <span class="tnum text-fg">
                P{{ account.priority }} · 并发{{ account.concurrency }} · {{ account.rate_multiplier }}x
              </span>
              <span class="text-subtle">
                最近：{{ formatRelative(account.last_used_at) }}
              </span>
              <span
                :class="account.expires_at && isExpired(account.expires_at) ? 'text-danger' : 'text-subtle'"
                :title="account.expires_at ? formatDateTime(account.expires_at) : ''"
              >
                {{ formatExpiry(account.expires_at) }}
              </span>
            </div>
          </td>

          <!-- 操作 -->
          <td class="td">
            <div class="flex flex-wrap items-center justify-end gap-1">
              <Button
                variant="secondary"
                size="xs"
                :disabled="harvestingId !== null"
                title="执行签到、礼包、分享上报与盲盒收割"
                @click="harvestOne(account)"
              >
                <Spinner v-if="harvestingId === account.id" :size="11" />
                <template v-else>签到收割</template>
              </Button>

              <Button
                variant="secondary"
                size="xs"
                title="生成 300 秒一次性免密登录授权码与官网跳转链接"
                @click="openLoginLink(account)"
              >
                免密登录
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                title="刷新积分"
                :disabled="balanceLoadingId !== null"
                @click="refreshBalance(account)"
              >
                <Spinner v-if="balanceLoadingId === account.id" :size="11" />
                <Icon v-else name="database" :size="13" />
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                title="刷新 Access Token"
                :disabled="refreshingTokenId !== null"
                @click="refreshAccountToken(account)"
              >
                <Spinner v-if="refreshingTokenId === account.id" :size="11" />
                <Icon v-else name="rotate" :size="13" />
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                title="发送最小请求探活（消耗极少额度）"
                :disabled="checkingId !== null"
                @click="check(account)"
              >
                <Spinner v-if="checkingId === account.id" :size="11" />
                <Icon v-else name="zap" :size="13" />
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                :title="account.status === 'active' ? '禁用该账号' : '启用该账号'"
                :disabled="togglingId !== null"
                @click="toggleStatus(account)"
              >
                <Spinner v-if="togglingId === account.id" :size="11" />
                <Icon v-else :name="account.status === 'active' ? 'lock' : 'checkCircle'" :size="13" />
              </Button>

              <Button variant="ghost" size="xs" icon title="编辑账号" @click="openEdit(account)">
                <Icon name="edit" :size="13" />
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                class="hover:bg-danger/10 hover:text-danger"
                title="删除账号"
                @click="deleteTarget = account"
              >
                <Icon name="trash" :size="13" />
              </Button>
            </div>
          </td>
        </tr>
      </template>

      <template #footer>
        <Pagination
          v-model:page="page"
          v-model:page-size="pageSize"
          :total="total"
        />
      </template>
    </DataTable>

    <AccountFormModal
      v-model:open="formOpen"
      :account="editing"
      :groups="groups"
      @saved="load"
    />

    <BatchImportModal
      v-model:open="batchOpen"
      :groups="groups"
      @imported="load"
    />

    <LoginLinkModal
      v-model:open="loginModalOpen"
      :account="loginTarget"
      :result="loginResult"
      :loading="loginLoading"
      :error="loginError"
      @regenerate="regenerateLoginLink"
    />

    <PackagesModal
      :open="packagesAccount !== null"
      :account="packagesAccount"
      :refreshing="balanceLoadingId !== null"
      @update:open="packagesAccount = null"
      @refresh="refreshBalance"
    />

    <HarvestSummaryModal
      v-model:open="harvestModalOpen"
      :title="harvestModalTitle"
      :subtitle="harvestModalSubtitle"
      :summary="harvestModalSummary"
      :total-credits="harvestModalCredits"
      :harvested-at="harvestModalAt"
      :batch-result="harvestModalBatch"
    />

    <Modal
      :open="wbOAuthOpen"
      title="WorkBuddy OAuth 统一授权入池"
      subtitle="创建带独立代理与成对设备指纹的 WorkBuddy OAuth 会话，支持浏览器回调、AccessToken 或 Token JSON 入池"
      size="lg"
      :persistent="wbOAuthCreating || wbOAuthSubmitting"
      @update:open="wbOAuthOpen = $event"
    >
      <div class="space-y-4">
        <div
          v-if="wbOAuthError"
          class="flex items-start gap-2 rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
          <span>{{ wbOAuthError }}</span>
        </div>

        <div class="grid gap-3 md:grid-cols-2">
          <Field label="独立出口代理 (Proxy URL · 可选)">
            <Input
              v-model="wbOAuthProxyUrl"
              size="sm"
              mono
              placeholder="http://user:pass@ip:port 或 socks5://..."
            />
          </Field>
          <Field label="账号备注名（可选）">
            <Input
              v-model="wbOAuthAccountName"
              size="sm"
              placeholder="留空则按邮箱/昵称自动命名"
            />
          </Field>
        </div>

        <div class="grid gap-3 md:grid-cols-2">
          <Field label="WorkBuddy 站点域名（可选）">
            <Input v-model="wbOAuthDomain" size="sm" mono placeholder="https://www.codebuddy.cn" />
          </Field>
          <div class="flex items-end">
            <div class="panel-inset flex w-full items-center justify-between gap-2 px-3 py-2 text-xs text-fg">
              <span>自动回传网关回调 (/api/oauth/callback)</span>
              <Switch v-model="wbOAuthRedirectMode" size="sm" />
            </div>
          </div>
        </div>

        <div v-if="workbuddyGroups.length > 0" class="panel-inset space-y-2 p-3">
          <div class="text-xs font-medium text-fg">授权后自动绑定分组</div>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="g in workbuddyGroups"
              :key="g.id"
              type="button"
              class="rounded-md border px-2.5 py-1 text-2xs font-medium transition"
              :class="
                wbOAuthGroupIds.includes(g.id)
                  ? 'border-accent bg-accent/12 text-accent'
                  : 'border-line bg-surface text-muted hover:text-fg'
              "
              @click="toggleWbOAuthGroup(g.id)"
            >
              {{ g.name }}
            </button>
          </div>
        </div>

        <div class="flex items-center justify-end">
          <Button variant="primary" size="sm" :loading="wbOAuthCreating" @click="startWbOAuth">
            <template #leading><Icon name="key" :size="13" /></template>
            {{ wbOAuthSession ? '重新生成授权会话' : '1. 生成 WorkBuddy OAuth 授权会话' }}
          </Button>
        </div>

        <div
          v-if="wbOAuthSession"
          class="panel-inset space-y-3 p-3.5"
        >
          <div class="flex items-center justify-between gap-2">
            <div class="text-xs font-medium text-fg">
              会话 ID：<span class="font-mono text-muted">{{ wbOAuthSession.session_id }}</span>
            </div>
            <Badge
              :tone="
                wbOAuthSession.status === 'completed'
                  ? 'success'
                  : wbOAuthSession.status === 'failed'
                    ? 'danger'
                    : 'info'
              "
              dot
            >
              {{ wbOAuthSession.status }}
            </Badge>
          </div>

          <div class="flex flex-wrap items-center gap-2">
            <Input :model-value="wbOAuthSession.auth_url" size="sm" readonly mono class="flex-1 text-xs" />
            <Button variant="secondary" size="sm" @click="copyWbOAuthUrl">
              <template #leading><Icon name="copy" :size="12" /></template>
              复制
            </Button>
            <a
              :href="wbOAuthSession.auth_url"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex h-8 items-center gap-1.5 rounded-md bg-accent px-3 text-xs font-medium text-accent-fg shadow-panel transition hover:bg-accent/90"
            >
              <Icon name="external" :size="12" />
              打开登录页
            </a>
          </div>

          <Field label="2. 粘贴浏览器回调 URL、AccessToken 或 OAuth Token JSON">
            <Textarea
              v-model="wbOAuthCallbackInput"
              :rows="3"
              mono
              placeholder="http://localhost:8080/api/oauth/callback?access_token=...&state=... 或粘贴 Token JSON / AccessToken"
            />
          </Field>

          <div class="grid gap-3 md:grid-cols-2">
            <Field label="Refresh Token（可选，用于自动续期）">
              <Input v-model="wbOAuthRefreshToken" size="sm" mono placeholder="可选" />
            </Field>
            <Field label="User ID / UID（可选）">
              <Input v-model="wbOAuthUid" size="sm" mono placeholder="可选" />
            </Field>
          </div>
        </div>
      </div>

      <template #footer>
        <Button variant="ghost" size="sm" :disabled="wbOAuthSubmitting" @click="wbOAuthOpen = false">取消</Button>
        <Button
          variant="primary"
          size="sm"
          :loading="wbOAuthSubmitting"
          :disabled="!wbOAuthSession || !wbOAuthCallbackInput.trim()"
          @click="completeWbOAuth"
        >
          验证并同步入池
        </Button>
      </template>
    </Modal>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除账号"
      :message="`确定删除「${deleteTarget?.name ?? ''}」吗？删除后正在进行的请求不会中断，但不会再被分配新请求。`"
      confirm-text="删除"
      tone="danger"
      :loading="deleting"
      @update:open="deleteTarget = null"
      @confirm="confirmDelete"
    />
  </div>
</template>
