<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import { toMessage } from '@/api/client'
import { accountsApi, groupsApi, oauthApi } from '@/api/endpoints'
import type {
  Account,
  AccountMode,
  BatchImportResult,
  CreateAccountPayload,
  Group,
  OAuthSession,
  ProxyTestResult,
  UpdateAccountPayload,
} from '@/api/types'
import AccountModePicker from '@/components/ui/AccountModePicker.vue'
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
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import Spinner from '@/components/ui/Spinner.vue'
import StatusPill from '@/components/ui/StatusPill.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'
import { formatRelative, redactProxyDisplay } from '@/utils/format'
import { accountModeMeta } from '@/utils/platforms'

const toast = useToastStore()

const items = ref<Account[]>([])
const groups = ref<Group[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')

const page = ref(1)
const pageSize = ref(20)
const modeFilter = ref<'all' | 'zen' | 'go'>('all')
const keyword = ref('')
const status = ref('')
const groupFilter = ref('')

const modeTabs = [
  { label: '全部模式', value: 'all' },
  { label: 'Zen（按量付费）', value: 'zen' },
  { label: 'Go（订阅额度）', value: 'go' },
]

// 操作状态
const checkingId = ref<number | null>(null)
const proxyTestingId = ref<number | null>(null)
const togglingId = ref<number | null>(null)
const batchChecking = ref(false)
const exporting = ref(false)
const proxyTests = ref<Record<number, ProxyTestResult>>({})

// 删除确认
const deleteTarget = ref<Account | null>(null)
const deleting = ref(false)

// 新建 / 编辑弹窗状态
const formOpen = ref(false)
const editing = ref<Account | null>(null)
const formName = ref('')
const formMode = ref<AccountMode>('zen')
const formBaseUrl = ref('')
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
const batchMode = ref<AccountMode>('zen')
const batchBaseUrl = ref('')
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

const ocGroups = computed(() => groups.value.filter((g) => g.platform === 'opencode'))
const groupOptions = computed(() =>
  ocGroups.value.map((g) => ({ label: g.name, value: String(g.id) })),
)
const groupNameById = computed(() => {
  const map = new Map<number, string>()
  for (const g of groups.value) map.set(g.id, g.name)
  return map
})

function effectiveMode(acc: Account): AccountMode {
  return acc.account_mode === 'go' ? 'go' : 'zen'
}

function effectiveBaseUrl(acc: Account): string {
  if (acc.base_url && acc.base_url.trim()) return acc.base_url.trim()
  return accountModeMeta(effectiveMode(acc)).defaultBaseUrl
}

const displayedItems = computed(() => {
  if (modeFilter.value === 'all') return items.value
  return items.value.filter((acc) => effectiveMode(acc) === modeFilter.value)
})

const filtersActive = computed(
  () =>
    modeFilter.value !== 'all' ||
    Boolean(keyword.value.trim()) ||
    Boolean(status.value) ||
    Boolean(groupFilter.value),
)

const poolStats = computed(() => {
  let active = 0
  let zen = 0
  let go = 0
  let healthy = 0
  for (const acc of items.value) {
    if (acc.status === 'active') active += 1
    if (effectiveMode(acc) === 'go') go += 1
    else zen += 1
    if (acc.last_health_check_ok) healthy += 1
  }
  return { active, zen, go, healthy }
})

let searchTimer: ReturnType<typeof setTimeout> | null = null
let suppressFilterWatch = false

async function loadGroups(): Promise<void> {
  try {
    const res = await groupsApi.list()
    groups.value = res.items ?? []
  } catch (err) {
    toast.error('加载分组失败', toMessage(err))
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await accountsApi.list({
      platform: 'opencode',
      page: page.value,
      page_size: pageSize.value,
      status: status.value || undefined,
      keyword: keyword.value.trim() || undefined,
      group_id: groupFilter.value ? Number(groupFilter.value) : undefined,
    })
    items.value = res.items ?? []
    total.value = res.total ?? 0
  } catch (err) {
    error.value = toMessage(err, '加载 OpenCode 账号池失败')
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
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

function clearFilters(): void {
  if (searchTimer) {
    clearTimeout(searchTimer)
    searchTimer = null
  }
  suppressFilterWatch = true
  modeFilter.value = 'all'
  keyword.value = ''
  status.value = ''
  groupFilter.value = ''
  page.value = 1
  queueMicrotask(() => {
    suppressFilterWatch = false
    void load()
  })
}

// ---- 动态模型探活与代理测速 ----
async function checkOne(acc: Account): Promise<void> {
  if (checkingId.value !== null) return
  checkingId.value = acc.id
  try {
    const res = await accountsApi.check(acc.id)
    if (res.ok) {
      toast.success(`「${acc.name}」模型探活正常`, `响应延迟 ${res.latency_ms} ms`)
    } else {
      toast.error(`「${acc.name}」探活失败`, res.error || '上游未返回可用响应')
    }
    await load()
  } catch (err) {
    toast.error('探活请求异常', toMessage(err))
  } finally {
    checkingId.value = null
  }
}

async function probeAllVisible(): Promise<void> {
  if (batchChecking.value || displayedItems.value.length === 0) return
  batchChecking.value = true
  let ok = 0
  let fail = 0
  try {
    for (const acc of displayedItems.value) {
      try {
        const res = await accountsApi.check(acc.id)
        if (res.ok) ok += 1
        else fail += 1
      } catch {
        fail += 1
      }
    }
    toast.info('OpenCode 批量模型探活完成', `正常 ${ok} 个 · 失败 ${fail} 个`)
    await load()
  } finally {
    batchChecking.value = false
  }
}

async function testProxyOne(acc: Account): Promise<void> {
  if (proxyTestingId.value !== null) return
  proxyTestingId.value = acc.id
  try {
    const res = await accountsApi.testProxy(acc.id)
    proxyTests.value = { ...proxyTests.value, [acc.id]: res }
    if (res.ok) {
      toast.success(`「${acc.name}」代理正常`, `出口 ${res.exit_ip} · ${res.latency_ms} ms`)
    } else {
      toast.error(`「${acc.name}」代理不可达`, res.error || '连接失败')
    }
  } catch (err) {
    toast.error('代理测速失败', toMessage(err))
  } finally {
    proxyTestingId.value = null
  }
}

async function toggleStatus(acc: Account): Promise<void> {
  if (togglingId.value !== null) return
  const next = acc.status === 'active' ? 'disabled' : 'active'
  togglingId.value = acc.id
  try {
    await accountsApi.update(acc.id, { status: next })
    toast.success(next === 'active' ? '账号已启用' : '账号已禁用', acc.name)
    await load()
  } catch (err) {
    toast.error('修改状态失败', toMessage(err))
  } finally {
    togglingId.value = null
  }
}

async function exportPool(): Promise<void> {
  if (exporting.value) return
  exporting.value = true
  try {
    const res = await accountsApi.exportAccounts('opencode')
    const blob = new Blob([JSON.stringify(res, null, 2)], { type: 'application/json;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `opencode-accounts-${new Date().toISOString().slice(0, 10)}.json`
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    URL.revokeObjectURL(url)
    toast.success('导出完成', `共导出 ${res.count} 个 OpenCode 账号`)
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
    if (items.value.length === 1 && page.value > 1) page.value -= 1
    else await load()
  } catch (err) {
    toast.error('删除失败', toMessage(err))
  } finally {
    deleting.value = false
  }
}

// ---- 新建 / 编辑弹窗 ----
const defaultFormBaseUrl = computed(() => accountModeMeta(formMode.value).defaultBaseUrl)
const defaultBatchBaseUrl = computed(() => accountModeMeta(batchMode.value).defaultBaseUrl)

function openCreate(): void {
  editing.value = null
  formName.value = ''
  formMode.value = modeFilter.value === 'go' ? 'go' : 'zen'
  formBaseUrl.value = ''
  formApiKey.value = ''
  formShowKey.value = false
  formProxyUrl.value = ''
  formConcurrency.value = '5'
  formPriority.value = '50'
  formMultiplier.value = '1'
  formStatus.value = 'active'
  formSchedulable.value = true
  formNotes.value = ''
  formGroupIds.value = ocGroups.value[0] ? [ocGroups.value[0].id] : []
  formError.value = ''
  formOpen.value = true
}

function openEdit(acc: Account): void {
  editing.value = acc
  formName.value = acc.name
  formMode.value = effectiveMode(acc)
  formBaseUrl.value = acc.base_url || ''
  formApiKey.value = ''
  formShowKey.value = false
  formProxyUrl.value = acc.proxy_url || ''
  formConcurrency.value = String(acc.concurrency ?? 5)
  formPriority.value = String(acc.priority ?? 50)
  formMultiplier.value = String(acc.rate_multiplier ?? 1)
  formStatus.value = acc.status || 'active'
  formSchedulable.value = acc.schedulable ?? true
  formNotes.value = acc.notes || ''
  formGroupIds.value = [...(acc.group_ids ?? [])]
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

const canSubmitForm = computed(
  () => Boolean(formName.value.trim()) && (Boolean(editing.value) || Boolean(formApiKey.value.trim())),
)

async function submitForm(): Promise<void> {
  if (!canSubmitForm.value || formSubmitting.value) return
  formSubmitting.value = true
  formError.value = ''
  try {
    if (editing.value) {
      const payload: UpdateAccountPayload = {
        name: formName.value.trim(),
        platform: 'opencode',
        account_mode: formMode.value,
        base_url: formBaseUrl.value.trim(),
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
      toast.success('OpenCode 账号已更新', payload.name)
    } else {
      const payload: CreateAccountPayload = {
        name: formName.value.trim(),
        platform: 'opencode',
        account_mode: formMode.value,
        base_url: formBaseUrl.value.trim() || undefined,
        api_key: formApiKey.value.trim(),
        proxy_url: formProxyUrl.value.trim() || undefined,
        concurrency: Number(formConcurrency.value) || 5,
        priority: Number(formPriority.value) || 50,
        rate_multiplier: Number(formMultiplier.value) || 1,
        status: formStatus.value,
        schedulable: formSchedulable.value,
        notes: formNotes.value.trim(),
        group_ids: formGroupIds.value,
      }
      await accountsApi.create(payload)
      toast.success('OpenCode 账号已创建', payload.name)
    }
    formOpen.value = false
    await load()
  } catch (err) {
    formError.value = toMessage(err, '保存 OpenCode 账号失败')
  } finally {
    formSubmitting.value = false
  }
}

// ---- 批量导入 ----
function openBatchModal(): void {
  batchMode.value = modeFilter.value === 'go' ? 'go' : 'zen'
  batchBaseUrl.value = ''
  batchRaw.value = ''
  batchProxyUrl.value = ''
  batchConcurrency.value = '5'
  batchPriority.value = '50'
  batchUpsert.value = true
  batchGroupIds.value = ocGroups.value[0] ? [ocGroups.value[0].id] : []
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
      platform: 'opencode',
      account_mode: batchMode.value,
      base_url: batchBaseUrl.value.trim() || undefined,
      keys: batchRaw.value.trim(),
      proxy_url: batchProxyUrl.value.trim() || undefined,
      group_ids: batchGroupIds.value.length > 0 ? batchGroupIds.value : undefined,
      concurrency: Number(batchConcurrency.value) || 5,
      priority: Number(batchPriority.value) || 50,
      upsert: batchUpsert.value,
    })
    batchResult.value = res
    toast.success(
      'OpenCode 批量导入完成',
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

// OpenCode (Zen / Go) 统一 OAuth 授权入池向导
const ocOAuthOpen = ref(false)
const ocOAuthMode = ref<AccountMode>('zen')
const ocOAuthBaseUrl = ref('')
const ocOAuthProxyUrl = ref('')
const ocOAuthAccountName = ref('')
const ocOAuthRedirectMode = ref(true)
const ocOAuthGroupIds = ref<number[]>([])
const ocOAuthSession = ref<OAuthSession | null>(null)
const ocOAuthCallbackInput = ref('')
const ocOAuthCreating = ref(false)
const ocOAuthSubmitting = ref(false)
const ocOAuthError = ref('')

const defaultOcOAuthBaseUrl = computed(() => accountModeMeta(ocOAuthMode.value).defaultBaseUrl)

function openOcOAuthModal(): void {
  ocOAuthError.value = ''
  ocOAuthMode.value = modeFilter.value === 'go' ? 'go' : 'zen'
  ocOAuthBaseUrl.value = ''
  ocOAuthProxyUrl.value = ''
  ocOAuthAccountName.value = ''
  ocOAuthRedirectMode.value = true
  ocOAuthCallbackInput.value = ''
  ocOAuthSession.value = null
  ocOAuthGroupIds.value = ocGroups.value.slice(0, 1).map((g) => g.id)
  ocOAuthOpen.value = true
}

function toggleOcOAuthGroup(groupId: number, checked: boolean): void {
  if (checked) {
    if (!ocOAuthGroupIds.value.includes(groupId)) ocOAuthGroupIds.value.push(groupId)
  } else {
    ocOAuthGroupIds.value = ocOAuthGroupIds.value.filter((g) => g !== groupId)
  }
}

async function startOcOAuth(): Promise<void> {
  if (ocOAuthCreating.value) return
  ocOAuthCreating.value = true
  ocOAuthError.value = ''
  try {
    const res = await oauthApi.createSession({
      platform: 'opencode',
      account_mode: ocOAuthMode.value,
      base_url: ocOAuthBaseUrl.value.trim() || undefined,
      proxy_url: ocOAuthProxyUrl.value.trim() || undefined,
      group_ids: ocOAuthGroupIds.value.length > 0 ? ocOAuthGroupIds.value : undefined,
      redirect_mode: ocOAuthRedirectMode.value,
      account_name: ocOAuthAccountName.value.trim() || undefined,
    })
    ocOAuthSession.value = res.session
    toast.success('OpenCode OAuth 会话已创建', '请点击授权链接登录或粘贴回调 URL / API Key')
  } catch (err) {
    ocOAuthError.value = toMessage(err, '生成 OpenCode OAuth 会话失败')
  } finally {
    ocOAuthCreating.value = false
  }
}

async function copyOcOAuthUrl(): Promise<void> {
  if (!ocOAuthSession.value?.auth_url) return
  const ok = await copyText(ocOAuthSession.value.auth_url)
  if (ok) toast.success('已复制授权链接')
  else toast.error('复制失败', '请手动复制输入框内的链接')
}

async function completeOcOAuth(): Promise<void> {
  if (!ocOAuthSession.value || !ocOAuthCallbackInput.value.trim() || ocOAuthSubmitting.value) return
  ocOAuthSubmitting.value = true
  ocOAuthError.value = ''
  try {
    const raw = ocOAuthCallbackInput.value.trim()
    const basePayload = {
      session_id: ocOAuthSession.value.session_id,
      account_mode: ocOAuthMode.value,
      base_url: ocOAuthBaseUrl.value.trim() || undefined,
      proxy_url: ocOAuthProxyUrl.value.trim() || undefined,
      account_name: ocOAuthAccountName.value.trim() || undefined,
    }
    const payload =
      raw.startsWith('{')
        ? { ...basePayload, raw_json: raw }
        : raw.startsWith('http://') || raw.startsWith('https://') || raw.includes('=')
          ? { ...basePayload, callback_url: raw }
          : { ...basePayload, api_key: raw }
    const res = await oauthApi.completeCallback(payload)
    if (res.session) {
      ocOAuthSession.value = res.session
    }
    toast.success('OpenCode OAuth 授权入池完成', res.account?.name ? `账号 ${res.account.name}` : res.message)
    await load()
    ocOAuthOpen.value = false
  } catch (err) {
    ocOAuthError.value = toMessage(err, '完成 OpenCode OAuth 回调失败')
  } finally {
    ocOAuthSubmitting.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部池概览指标 -->
    <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <MetricTile
        label="OpenCode 账号池"
        :value="`${poolStats.active} / ${total}`"
        hint="正常可调度 / 总账号数"
        icon="code"
        tone="warning"
      />
      <MetricTile
        label="Zen 按量付费池"
        :value="String(poolStats.zen)"
        hint="opencode.ai/zen/v1 按量计费"
        icon="layers"
        tone="accent"
      />
      <MetricTile
        label="Go 订阅额度池"
        :value="String(poolStats.go)"
        hint="opencode.ai/zen/go/v1 订阅通道"
        icon="zap"
        tone="info"
      />
      <MetricTile
        label="探活健康账号"
        :value="`${poolStats.healthy} / ${items.length}`"
        hint="支持单号与批量动态模型探活"
        icon="checkCircle"
        tone="success"
      />
    </div>

    <!-- 筛选与工具栏 -->
    <div class="card flex flex-wrap items-center gap-2.5 px-3.5 py-2.5">
      <SegmentedTabs
        :model-value="modeFilter"
        :items="modeTabs"
        size="sm"
        @update:model-value="modeFilter = $event as 'all' | 'zen' | 'go'"
      />

      <div class="w-full min-w-[11rem] sm:w-52">
        <Input v-model="keyword" size="sm" placeholder="搜索名称 / 备注 / 代理" />
      </div>

      <div class="w-32">
        <Select v-model="status" size="sm" :options="statusOptions" placeholder="全部状态" />
      </div>

      <div class="w-36">
        <Select v-model="groupFilter" size="sm" :options="groupOptions" placeholder="全部分组" />
      </div>

      <Button v-if="filtersActive" variant="ghost" size="sm" @click="clearFilters">
        重置
      </Button>

      <div class="ml-auto flex flex-wrap items-center gap-2">
        <Button
          variant="secondary"
          size="sm"
          :loading="batchChecking"
          :disabled="displayedItems.length === 0"
          @click="probeAllVisible"
        >
          <template #leading><Icon name="zap" :size="13" /></template>
          批量模型探活
        </Button>

        <Button variant="secondary" size="sm" @click="openOcOAuthModal">
          <template #leading><Icon name="link" :size="13" /></template>
          OAuth 授权
        </Button>

        <Button variant="secondary" size="sm" @click="openBatchModal">
          <template #leading><Icon name="upload" :size="13" /></template>
          批量导入
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

    <!-- 高密度 OpenCode 账号表 -->
    <DataTable
      :columns="7"
      :loading="loading"
      :empty="displayedItems.length === 0"
      :skeleton-rows="6"
      min-width="1160px"
      hoverable
    >
      <template #head>
        <th class="th">账号 / 掩码密钥</th>
        <th class="th">模式 (Zen/Go) & 推理端点</th>
        <th class="th">状态 / 动态探活</th>
        <th class="th">绑定分组</th>
        <th class="th">独立代理 IP</th>
        <th class="th">调度参数</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="code"
          :title="filtersActive ? '没有匹配的 OpenCode 账号' : '尚未添加任何 OpenCode 上游账号'"
          description="支持接入 OpenCode Zen 按量付费密钥与 Go 订阅模式密钥，并配置自定义网关端点与独立代理 IP。"
        >
          <Button variant="secondary" size="sm" @click="openBatchModal">
            <template #leading><Icon name="upload" :size="13" /></template>
            批量导入密钥
          </Button>
          <Button variant="primary" size="sm" @click="openCreate">
            <template #leading><Icon name="plus" :size="13" /></template>
            新建 OpenCode 账号
          </Button>
        </EmptyState>
      </template>

      <template #body>
        <tr v-for="acc in displayedItems" :key="acc.id">
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

          <!-- 2. 模式与 Base URL -->
          <td class="td max-w-[18rem]">
            <div class="flex items-center gap-1.5">
              <Badge :tone="effectiveMode(acc) === 'go' ? 'info' : 'accent'" mono>
                {{ effectiveMode(acc) === 'go' ? 'GO 订阅' : 'ZEN 按量' }}
              </Badge>
              <Badge v-if="acc.base_url" tone="neutral">自定义端点</Badge>
            </div>
            <div
              class="mt-1 truncate font-mono text-2xs text-muted"
              :title="effectiveBaseUrl(acc)"
            >
              {{ effectiveBaseUrl(acc) }}
            </div>
          </td>

          <!-- 3. 状态与探活 -->
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
                    ? `正常 · ${acc.latency_ms ?? 0} ms`
                    : '探活异常'
                }}
              </span>
              <span v-else class="text-2xs text-subtle">未探活</span>
              <span
                v-if="acc.error_message || acc.last_health_check_error"
                class="max-w-[10rem] truncate text-2xs text-danger"
                :title="acc.error_message || acc.last_health_check_error || ''"
              >
                {{ acc.error_message || acc.last_health_check_error }}
              </span>
            </div>
          </td>

          <!-- 4. 分组 -->
          <td class="td max-w-[10rem]">
            <div v-if="(acc.group_ids ?? []).length" class="flex flex-wrap gap-1">
              <Badge v-for="gid in acc.group_ids ?? []" :key="gid" tone="neutral">
                {{ groupNameById.get(gid) ?? `#${gid}` }}
              </Badge>
            </div>
            <span v-else class="text-2xs text-warning">未绑分组</span>
          </td>

          <!-- 5. 独立代理 -->
          <td class="td max-w-[14rem]">
            <div class="flex items-center gap-1.5">
              <Badge v-if="!acc.proxy_url" tone="neutral">默认/直连</Badge>
              <span
                v-else
                class="max-w-[9rem] truncate font-mono text-2xs text-fg"
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
                  : proxyTests[acc.id]?.error || '不可达'
              }}
            </p>
          </td>

          <!-- 6. 调度参数 -->
          <td class="td text-2xs">
            <div class="tnum text-fg">
              P{{ acc.priority }} · 并发{{ acc.concurrency }} · {{ acc.rate_multiplier }}x
            </div>
            <div class="text-subtle">最近: {{ formatRelative(acc.last_used_at) }}</div>
          </td>

          <!-- 7. 操作 -->
          <td class="td">
            <div class="flex items-center justify-end gap-1">
              <Button
                variant="secondary"
                size="xs"
                :disabled="checkingId !== null"
                title="发送最小推理请求动态探活"
                @click="checkOne(acc)"
              >
                <Spinner v-if="checkingId === acc.id" :size="11" />
                <template v-else>探活</template>
              </Button>

              <Button
                variant="ghost"
                size="xs"
                icon
                :title="acc.status === 'active' ? '禁用账号' : '启用账号'"
                :disabled="togglingId !== null"
                @click="toggleStatus(acc)"
              >
                <Spinner v-if="togglingId === acc.id" :size="11" />
                <Icon v-else :name="acc.status === 'active' ? 'lock' : 'checkCircle'" :size="13" />
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
        <div>
          <div
            v-if="modeFilter !== 'all'"
            class="flex items-center justify-between border-t border-line bg-raised/30 px-3 py-1.5 text-2xs text-subtle"
          >
            <span>当前页按「{{ modeFilter.toUpperCase() }}」模式筛选出 {{ displayedItems.length }} / {{ items.length }} 条</span>
            <button type="button" class="text-accent hover:underline" @click="modeFilter = 'all'">
              显示全部模式
            </button>
          </div>
          <Pagination v-model:page="page" v-model:page-size="pageSize" :total="total" />
        </div>
      </template>
    </DataTable>

    <!-- 新建 / 编辑 OpenCode 账号弹窗 -->
    <Modal
      :open="formOpen"
      :title="editing ? `编辑 OpenCode 账号 · ${editing.name}` : '新建 OpenCode 账号'"
      subtitle="选择 Zen / Go 计费模式，支持自定义上游 Base URL 与独立代理 IP"
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

        <Field label="OpenCode 计费模式 (account_mode)" required>
          <AccountModePicker
            :model-value="formMode"
            @update:model-value="formMode = ($event === 'go' ? 'go' : 'zen')"
          />
        </Field>

        <div class="grid gap-3.5 sm:grid-cols-2">
          <Field label="账号名称" required>
            <Input v-model="formName" placeholder="例如：oc-zen-main" />
          </Field>

          <Field
            label="OpenCode API Key"
            :required="!editing"
            :hint="editing ? `留空保留原密钥（${editing.masked_key}）` : '原样粘贴 OpenCode 密钥'"
          >
            <div class="relative">
              <Input
                v-model="formApiKey"
                :type="formShowKey ? 'text' : 'password'"
                mono
                placeholder="sk-..."
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
          label="自定义上游端点 (base_url，可选)"
          :hint="`留空默认使用 ${defaultFormBaseUrl}`"
        >
          <Input v-model="formBaseUrl" mono :placeholder="defaultFormBaseUrl" />
        </Field>

        <Field
          label="账号独立代理 IP (proxy_url，可选)"
          hint="支持 http://user:pass@ip:port 或 socks5://...；留空走全局代理或直连"
        >
          <Input v-model="formProxyUrl" mono placeholder="http://127.0.0.1:7890" />
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
            <span class="text-xs font-medium text-fg">绑定 OpenCode 分组</span>
            <RouterLink :to="{ name: 'groups' }" class="text-2xs text-accent hover:underline">
              去管理分组 →
            </RouterLink>
          </div>
          <div v-if="ocGroups.length > 0" class="flex flex-wrap gap-3">
            <Checkbox
              v-for="g in ocGroups"
              :key="g.id"
              :model-value="formGroupIds.includes(g.id)"
              :label="`${g.name} (${g.rate_multiplier}x)`"
              @update:model-value="toggleFormGroup(g.id, $event)"
            />
          </div>
          <p v-else class="text-2xs text-subtle">
            暂无 `opencode` 平台分组，请先在「分组与倍率路由」创建。
          </p>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-3">
          <Switch v-model="formSchedulable" label="允许参与网关流量调度" />
          <div class="w-36">
            <Select v-model="formStatus" size="sm" :options="statusOptions" />
          </div>
        </div>

        <Field label="备注">
          <Input v-model="formNotes" placeholder="可选备注" />
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

    <!-- 批量导入 OpenCode 弹窗 -->
    <Modal
      :open="batchOpen"
      title="批量导入 OpenCode 账号"
      subtitle="支持按 Zen / Go 模式批量导入多行密钥或 JSON 数组"
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

        <Field label="默认计费模式 (account_mode)">
          <AccountModePicker
            :model-value="batchMode"
            @update:model-value="batchMode = ($event === 'go' ? 'go' : 'zen')"
          />
        </Field>

        <Field
          :label="`密钥文本 / JSON（已识别 ${batchLineCount} 条）`"
          required
          hint="每行一把密钥（或 name|api_key），也支持粘贴导出的 JSON"
        >
          <Textarea
            v-model="batchRaw"
            :rows="6"
            mono
            placeholder="sk-opencode-xxxx...&#10;oc-go-02|sk-opencode-yyyy..."
          />
        </Field>

        <div class="grid gap-3 sm:grid-cols-2">
          <Field label="自定义端点 (base_url，可选)" :hint="`留空默认 ${defaultBatchBaseUrl}`">
            <Input v-model="batchBaseUrl" size="sm" mono :placeholder="defaultBatchBaseUrl" />
          </Field>
          <Field label="默认独立代理 (proxy_url，可选)">
            <Input v-model="batchProxyUrl" size="sm" mono placeholder="socks5://127.0.0.1:1080" />
          </Field>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <Field label="默认并发">
            <Input v-model="batchConcurrency" type="number" min="1" size="sm" />
          </Field>
          <Field label="默认优先级">
            <Input v-model="batchPriority" type="number" min="0" size="sm" />
          </Field>
        </div>

        <div class="panel-inset space-y-2 p-3">
          <p class="text-xs font-medium text-fg">绑定 OpenCode 分组</p>
          <div v-if="ocGroups.length > 0" class="flex flex-wrap gap-3">
            <Checkbox
              v-for="g in ocGroups"
              :key="g.id"
              :model-value="batchGroupIds.includes(g.id)"
              :label="g.name"
              @update:model-value="toggleBatchGroup(g.id, $event)"
            />
          </div>
          <p v-else class="text-2xs text-subtle">暂无 `opencode` 分组</p>
        </div>

        <Switch v-model="batchUpsert" label="遇到相同密钥时覆盖更新已有账号 (upsert)" />

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
      :open="ocOAuthOpen"
      title="OpenCode (Zen / Go) OAuth 统一授权入池"
      subtitle="创建带 Zen/Go 计费模式、独立代理与设备指纹的 OAuth 会话，自动完成凭证校验与入池"
      size="lg"
      :persistent="ocOAuthCreating || ocOAuthSubmitting"
      @update:open="ocOAuthOpen = $event"
    >
      <div class="space-y-4">
        <div
          v-if="ocOAuthError"
          class="flex items-start gap-2 rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
          <span>{{ ocOAuthError }}</span>
        </div>

        <Field label="计费模式">
          <AccountModePicker
            :model-value="ocOAuthMode"
            @update:model-value="ocOAuthMode = ($event === 'go' ? 'go' : 'zen')"
          />
        </Field>

        <div class="grid gap-3 md:grid-cols-2">
          <Field label="自定义推理端点 (base_url · 可选)" :hint="`留空默认 ${defaultOcOAuthBaseUrl}`">
            <Input v-model="ocOAuthBaseUrl" size="sm" mono :placeholder="defaultOcOAuthBaseUrl" />
          </Field>
          <Field label="独立出口代理 (Proxy URL · 可选)">
            <Input v-model="ocOAuthProxyUrl" size="sm" mono placeholder="socks5://127.0.0.1:1080" />
          </Field>
        </div>

        <div class="grid gap-3 md:grid-cols-2">
          <Field label="账号备注名（可选）">
            <Input v-model="ocOAuthAccountName" size="sm" placeholder="留空则按邮箱或模式自动命名" />
          </Field>
          <div class="flex items-end">
            <div class="panel-inset flex w-full items-center justify-between gap-2 px-3 py-2 text-xs text-fg">
              <span>自动绑定网关回调 (/api/oauth/callback)</span>
              <Switch v-model="ocOAuthRedirectMode" size="sm" />
            </div>
          </div>
        </div>

        <div class="panel-inset space-y-2 p-3">
          <p class="text-xs font-medium text-fg">授权后绑定 OpenCode 分组</p>
          <div v-if="ocGroups.length > 0" class="flex flex-wrap gap-3">
            <Checkbox
              v-for="g in ocGroups"
              :key="g.id"
              :model-value="ocOAuthGroupIds.includes(g.id)"
              :label="g.name"
              @update:model-value="toggleOcOAuthGroup(g.id, $event)"
            />
          </div>
          <p v-else class="text-2xs text-subtle">暂无 opencode 分组（回调时将自动创建默认分组）</p>
        </div>

        <div class="flex items-center justify-end">
          <Button variant="primary" size="sm" :loading="ocOAuthCreating" @click="startOcOAuth">
            <template #leading><Icon name="key" :size="13" /></template>
            {{ ocOAuthSession ? '重新生成授权会话' : '1. 生成 OpenCode OAuth 授权链接' }}
          </Button>
        </div>

        <div
          v-if="ocOAuthSession"
          class="panel-inset space-y-3 p-3.5"
        >
          <div class="flex items-center justify-between gap-2">
            <div class="text-xs font-medium text-fg">
              会话 ID：<span class="font-mono text-muted">{{ ocOAuthSession.session_id }}</span>
            </div>
            <Badge
              :tone="
                ocOAuthSession.status === 'completed'
                  ? 'success'
                  : ocOAuthSession.status === 'failed'
                    ? 'danger'
                    : 'info'
              "
              dot
            >
              {{ ocOAuthSession.status }}
            </Badge>
          </div>

          <div class="flex flex-wrap items-center gap-2">
            <Input :model-value="ocOAuthSession.auth_url" size="sm" readonly mono class="flex-1 text-xs" />
            <Button variant="secondary" size="sm" @click="copyOcOAuthUrl">
              <template #leading><Icon name="copy" :size="12" /></template>
              复制
            </Button>
            <a
              :href="ocOAuthSession.auth_url"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex h-8 items-center gap-1.5 rounded-md bg-accent px-3 text-xs font-medium text-accent-fg shadow-panel transition hover:bg-accent/90"
            >
              <Icon name="external" :size="12" />
              打开 OpenCode 授权
            </a>
          </div>

          <Field label="2. 粘贴浏览器回调 URL（含 ?api_key=/token=/code=）、API Key 或 JSON 凭证">
            <Textarea
              v-model="ocOAuthCallbackInput"
              :rows="3"
              mono
              placeholder="http://localhost:8080/api/oauth/callback?api_key=sk-oc-...&state=... 或 sk-opencode-..."
            />
          </Field>
        </div>
      </div>

      <template #footer>
        <Button variant="ghost" size="sm" :disabled="ocOAuthSubmitting" @click="ocOAuthOpen = false">取消</Button>
        <Button
          variant="primary"
          size="sm"
          :loading="ocOAuthSubmitting"
          :disabled="!ocOAuthSession || !ocOAuthCallbackInput.trim()"
          @click="completeOcOAuth"
        >
          完成授权并同步入池
        </Button>
      </template>
    </Modal>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除 OpenCode 账号"
      :message="`确认删除账号「${deleteTarget?.name ?? ''}」吗？`"
      confirm-text="删除"
      tone="danger"
      :loading="deleting"
      @update:open="deleteTarget = null"
      @confirm="confirmDelete"
    />
  </div>
</template>
