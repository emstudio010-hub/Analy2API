<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'

import {
  addProxyPoolEntries,
  allocateProxyPoolPreview,
  createAdminKey,
  deleteAdminKey,
  deleteProxyPoolEntry,
  fetchProxyPool,
  getProxyPool,
  listAdminKeys,
  probeProxyPool,
  updateAdminKey,
  updateProxyPool,
} from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type {
  AdminKey,
  AdminKeyProxyPolicy,
  AdminKeyScope,
  KeyProxyMode,
  ProxyPoolConfig,
  ProxyPoolEntry,
  ProxyPoolStats,
  ProxyPoolStrategy,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import StatusPill from '@/components/ui/StatusPill.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'
import { formatDateTime, formatExpiry, formatRelative, isExpired } from '@/utils/format'

const toast = useToastStore()

const loading = ref(true)
const error = ref('')

const items = ref<AdminKey[]>([])
const connectionBaseUrl = ref('')
const connectionPushUrl = ref('')
const connectionPushProxyUrl = ref('')
const connectionHeaders = ref<Record<string, string>>({})

// ---- 代理 IP 池状态 ----
const poolLoading = ref(true)
const poolSaving = ref(false)
const poolFetching = ref(false)
const poolProbing = ref(false)
const probingEntryId = ref<string | null>(null)
const poolAdding = ref(false)
const poolAllocating = ref(false)
const poolBatchText = ref('')
const poolBatchTag = ref('')
const poolFetchFeedback = ref('')
const poolKeyword = ref('')
const poolTagFilter = ref('all')
const togglingEntryId = ref<string | null>(null)

const poolStats = ref<ProxyPoolStats>({
  total_entries: 0,
  enabled_entries: 0,
  manual_entries: 0,
  fetched_entries: 0,
  bound_total: 0,
  tags: [],
})

const poolConfig = reactive<ProxyPoolConfig>({
  enabled: false,
  strategy: 'round_robin',
  max_accounts_per_ip: 0,
  apply_platforms: ['antigravity', 'workbuddy', 'commandcode', 'opencode'],
  entries: [],
  fetch: {
    enabled: false,
    url: '',
    method: 'GET',
    default_scheme: 'http',
    username: '',
    password: '',
    default_tag: 'remote',
    json_path: '',
    auto_fetch_on_empty: true,
    replace_on_fetch: false,
    interval_minutes: 0,
    timeout_seconds: 15,
  },
})

const strategyOptions: Array<{ label: string; value: ProxyPoolStrategy }> = [
  { label: '顺序轮询 (round_robin · 依次均匀分配)', value: 'round_robin' },
  { label: '最少占用优先 (least_used · 优先选绑定账号最少的 IP)', value: 'least_used' },
  { label: '独占优先 (sticky_unique · 优先未绑定 IP，用完自动拉新)', value: 'sticky_unique' },
  { label: '随机抽取 (random)', value: 'random' },
]

const keyProxyModeOptions: Array<{ label: string; value: KeyProxyMode }> = [
  { label: '跟随全局 IP 池 (inherit · 池开启时自动轮询)', value: 'inherit' },
  { label: '指定 IP 池分配 (pool · 可按标签/策略覆盖)', value: 'pool' },
  { label: '固定绑定代理 IP (fixed · 此 Key 推号统一走指定代理)', value: 'fixed' },
  { label: '实时远程拉取 (fetch · 推号时先拉新 IP 再分配)', value: 'fetch' },
  { label: '强制直连 (direct · 不挂代理)', value: 'direct' },
]

const keyStrategyOptions = [
  { label: '继承全局轮询策略 (inherit)', value: 'inherit' },
  ...strategyOptions,
]

const schemeOptions = [
  { label: 'HTTP (http://)', value: 'http' },
  { label: 'HTTPS (https://)', value: 'https' },
  { label: 'SOCKS5 (socks5://)', value: 'socks5' },
  { label: 'SOCKS5H 远端解析 (socks5h://)', value: 'socks5h' },
]

const fetchMethodOptions = [
  { label: 'GET', value: 'GET' },
  { label: 'POST', value: 'POST' },
]

const allPlatformOptions = [
  { value: 'antigravity', label: 'AntiGravity' },
  { value: 'workbuddy', label: 'WorkBuddy' },
  { value: 'commandcode', label: 'CommandCode' },
  { value: 'opencode', label: 'OpenCode' },
]

const snippetLang = ref('push_curl')
const snippetTabs = [
  { value: 'push_curl', label: '远程补号 + IP池 (cURL)', icon: 'upload' as const },
  { value: 'push_python', label: '远程补号 + IP池 (Python)', icon: 'code' as const },
  { value: 'curl', label: '全量运维 (cURL)', icon: 'terminal' as const },
  { value: 'python', label: '全量运维 (Python)', icon: 'code' as const },
]

// 一次性展示刚创建的明文密钥
const createdPlainKey = ref<{ name: string; key: string; scopes: string } | null>(null)
const togglingId = ref<number | null>(null)
const deleteTarget = ref<AdminKey | null>(null)
const deleting = ref(false)

// 新建 / 编辑弹窗
const formOpen = ref(false)
const editing = ref<AdminKey | null>(null)
const saving = ref(false)
const formError = ref('')

const form = reactive({
  name: '',
  scopes: 'push_accounts' as AdminKeyScope,
  allowed_ips: '',
  ttlDays: '0',
  status: 'active',
  proxy_mode: 'inherit' as KeyProxyMode,
  fixed_proxy_url: '',
  pool_tag: '',
  proxy_strategy: 'inherit',
  fetch_url_override: '',
  allow_override_in_push: true,
})

const scopeOptions: Array<{ label: string; value: AdminKeyScope }> = [
  { label: '远程补号专用 (push_accounts · 仅允许推号与调自身代理，最小权限最安全)', value: 'push_accounts' },
  { label: '读写全权 (full · 可远程补号/管IP池/领奖/建号/容器启停/改配置)', value: 'full' },
  { label: '只读审计 (readonly · 仅允许 GET 查询监控/日志/账号/模型)', value: 'readonly' },
]

const ttlPresetOptions = [
  { label: '永不过期', value: '0' },
  { label: '7 天有效', value: '7' },
  { label: '30 天有效', value: '30' },
  { label: '90 天有效', value: '90' },
  { label: '365 天有效', value: '365' },
]

const effectiveBaseUrl = computed(() => {
  if (connectionBaseUrl.value) return connectionBaseUrl.value.replace(/\/$/, '')
  if (typeof window !== 'undefined' && window.location?.origin) {
    return window.location.origin
  }
  return 'http://127.0.0.1:8080'
})

const effectivePushUrl = computed(() => {
  if (connectionPushUrl.value) return connectionPushUrl.value.replace(/\/$/, '')
  return `${effectiveBaseUrl.value}/api/remote/accounts/push`
})

const effectivePushProxyUrl = computed(() => {
  if (connectionPushProxyUrl.value) return connectionPushProxyUrl.value.replace(/\/$/, '')
  return `${effectiveBaseUrl.value}/api/remote/accounts/push/proxy`
})

const hostPortLabel = computed(() => {
  try {
    const u = new URL(effectiveBaseUrl.value)
    return u.host
  } catch {
    return effectiveBaseUrl.value
  }
})

const demoKeyText = computed(() => {
  if (createdPlainKey.value?.key) return createdPlainKey.value.key
  const pushFirst = items.value.find((x) => x.scopes === 'push_accounts') ?? items.value[0]
  if (pushFirst?.key_prefix) return `${pushFirst.key_prefix}<your_secret_suffix>`
  return 'adm-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'
})

const pushCurlSnippet = computed(() => {
  const pushUrl = effectivePushUrl.value
  const pushProxyUrl = effectivePushProxyUrl.value
  const k = demoKeyText.value
  return `# 1. 自动走 IP 池轮询分配代理（支持 AGY 全格式自动转换：ADC / Gemini CLI / CPA / email----refresh_token）
curl -sS -X POST "${pushUrl}" \\
  -H "X-Push-Key: ${k}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "platform": "antigravity",
    "email": "acc01@gmail.com",
    "refresh_token": "1//0g_your_google_refresh_token",
    "proxy_tag": "us",
    "group_name": "antigravity"
  }'

# 2. 批量推送任意混合格式 AGY 账号（raw_text 自动嗅探 ADC / CSV / JSONL / email----1//... 并生成 agy CLI 原生凭证）
curl -sS -X POST "${pushUrl}" \\
  -H "X-Push-Key: ${k}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "platform": "antigravity",
    "raw_text": "dev1@gmail.com----1//0g_token_1\\ndev2@gmail.com----1//0g_token_2",
    "proxy_mode": "pool",
    "group_name": "antigravity"
  }'

# 3. 推号时直接传 IP 或批量 proxy_list（支持 ip:port / ip:port:user:pass / socks5://...）
curl -sS -X POST "${pushUrl}" \\
  -H "X-Push-Key: ${k}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "platform": "commandcode",
    "proxy_list": ["1.2.3.4:8080:user:pass", "socks5://5.6.7.8:1080"],
    "accounts": [
      {"name": "cc-01", "api_key": "user_xxxx1"},
      {"name": "cc-02", "api_key": "user_xxxx2"}
    ]
  }'

# 4. 用补号 Key 自助调整自身默认代理 IP（或传 account_id 给已有账号换 IP / 轮换池内新 IP）
curl -sS -X PUT "${pushProxyUrl}" \\
  -H "X-Push-Key: ${k}" \\
  -H "Content-Type: application/json" \\
  -d '{"mode":"fixed","fixed_proxy_url":"1.2.3.4:8080:user:pass"}'`
})

const pushPythonSnippet = computed(() => {
  const pushUrl = effectivePushUrl.value
  const pushProxyUrl = effectivePushProxyUrl.value
  const k = demoKeyText.value
  return `import requests

PUSH_URL = "${pushUrl}"
PUSH_PROXY_URL = "${pushProxyUrl}"
HEADERS = {
    "X-Push-Key": "${k}",
    "Content-Type": "application/json",
}

# 1. 自动从代理 IP 池轮询分配（若池用完且配了远程拉取源，会自动拉取新 IP）
res = requests.post(
    PUSH_URL,
    headers=HEADERS,
    json={
        "platform": "antigravity",
        "email": "pool-01@gmail.com",
        "refresh_token": "1//0g_xxx",
        "proxy_mode": "pool",     # 可选: auto / pool / fetch(实时拉新) / direct
        "proxy_tag": "us",        # 可选: 按地区/标签筛选子池
        "group_name": "antigravity",
    },
    timeout=60,
)
print("Push Result:", res.status_code, res.json())

# 2. 远程给已有账号从 IP 池轮换新出口代理（AGY 自动同步到对应独立容器）
swap = requests.put(
    PUSH_PROXY_URL,
    headers=HEADERS,
    json={"account_name": "pool-01@gmail.com", "rotate_from_pool": True},
    timeout=30,
)
print("Swap IP:", swap.status_code, swap.json())`
})

const curlSnippet = computed(() => {
  const base = effectiveBaseUrl.value
  const k = demoKeyText.value
  return `# 1. 查询代理 IP 池配置与绑定统计
curl -sS "${base}/api/proxy-pool" -H "X-Management-Key: ${k}"

# 2. 立即触发从远程代理商 API 拉取新 IP 进池
curl -sS -X POST "${base}/api/proxy-pool/fetch" -H "X-Management-Key: ${k}"

# 3. 查询四平台账号池列表
curl -sS "${base}/api/accounts?page=1&page_size=20" -H "X-Management-Key: ${k}"`
})

const pythonSnippet = computed(() => {
  const base = effectiveBaseUrl.value
  const k = demoKeyText.value
  return `import requests

BASE_URL = "${base}"
HEADERS = {"X-Management-Key": "${k}", "Content-Type": "application/json"}

# 拉取新代理 IP 进池 + 查看当前池水位
fetched = requests.post(f"{BASE_URL}/api/proxy-pool/fetch", headers=HEADERS, timeout=30).json()
print("Fetched:", fetched)

pool = requests.get(f"{BASE_URL}/api/proxy-pool", headers=HEADERS, timeout=30).json()
print("Pool Stats:", pool.get("stats"))`
})

function splitAllowedIps(raw: string): string[] {
  return (raw || '')
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean)
}

function applyPoolView(view: { config: ProxyPoolConfig; stats: ProxyPoolStats } | undefined): void {
  if (!view) return
  poolConfig.enabled = Boolean(view.config.enabled)
  poolConfig.strategy = view.config.strategy || 'round_robin'
  poolConfig.max_accounts_per_ip = Number(view.config.max_accounts_per_ip || 0)
  poolConfig.apply_platforms = Array.isArray(view.config.apply_platforms)
    ? [...view.config.apply_platforms]
    : ['antigravity', 'workbuddy', 'commandcode', 'opencode']
  poolConfig.entries = Array.isArray(view.config.entries) ? view.config.entries.map((e) => ({ ...e })) : []
  poolConfig.cursor = view.config.cursor ?? 0
  poolConfig.fetch = {
    enabled: Boolean(view.config.fetch?.enabled),
    url: view.config.fetch?.url ?? '',
    method: view.config.fetch?.method || 'GET',
    headers: view.config.fetch?.headers ?? {},
    body: view.config.fetch?.body ?? '',
    default_scheme: view.config.fetch?.default_scheme || 'http',
    username: view.config.fetch?.username ?? '',
    password: view.config.fetch?.password ?? '',
    default_tag: view.config.fetch?.default_tag ?? 'remote',
    json_path: view.config.fetch?.json_path ?? '',
    auto_fetch_on_empty: view.config.fetch?.auto_fetch_on_empty ?? true,
    replace_on_fetch: Boolean(view.config.fetch?.replace_on_fetch),
    interval_minutes: Number(view.config.fetch?.interval_minutes || 0),
    timeout_seconds: Number(view.config.fetch?.timeout_seconds || 15),
    last_fetched_at: view.config.fetch?.last_fetched_at,
    last_fetch_count: view.config.fetch?.last_fetch_count,
    last_fetch_error: view.config.fetch?.last_fetch_error,
  }
  poolStats.value = view.stats ?? {
    total_entries: poolConfig.entries.length,
    enabled_entries: poolConfig.entries.filter((e) => e.enabled).length,
    manual_entries: 0,
    fetched_entries: 0,
    bound_total: 0,
    tags: [],
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await listAdminKeys()
    items.value = res.items ?? []
    connectionBaseUrl.value = res.connection?.base_url ?? ''
    connectionPushUrl.value = res.connection?.push_accounts_url ?? ''
    connectionPushProxyUrl.value = res.connection?.push_proxy_url ?? ''
    connectionHeaders.value = res.connection?.headers ?? {}
  } catch (err) {
    error.value = toMessage(err, '加载远程管理密钥失败')
    items.value = []
  } finally {
    loading.value = false
  }
}

async function loadProxyPool(): Promise<void> {
  poolLoading.value = true
  try {
    const view = await getProxyPool()
    applyPoolView(view)
  } catch (err) {
    toast.error('加载代理 IP 池失败', toMessage(err))
  } finally {
    poolLoading.value = false
  }
}

function togglePoolPlatform(platform: string): void {
  const cur = poolConfig.apply_platforms ?? []
  if (cur.includes(platform)) {
    poolConfig.apply_platforms = cur.filter((p) => p !== platform)
  } else {
    poolConfig.apply_platforms = [...cur, platform]
  }
}

async function saveProxyPool(): Promise<void> {
  poolSaving.value = true
  try {
    const payload: ProxyPoolConfig & { raw_proxies?: string; raw_tag?: string } = {
      enabled: poolConfig.enabled,
      strategy: poolConfig.strategy,
      max_accounts_per_ip: Math.max(0, Number(poolConfig.max_accounts_per_ip) || 0),
      apply_platforms: [...(poolConfig.apply_platforms ?? [])],
      entries: poolConfig.entries.map((e) => ({ ...e })),
      fetch: {
        ...poolConfig.fetch,
        interval_minutes: Math.max(0, Number(poolConfig.fetch.interval_minutes) || 0),
        timeout_seconds: Math.max(3, Math.min(60, Number(poolConfig.fetch.timeout_seconds) || 15)),
      },
      cursor: poolConfig.cursor,
    }
    const view = await updateProxyPool(payload)
    applyPoolView(view)
    toast.success('代理 IP 池配置已保存并立即生效')
  } catch (err) {
    toast.error('保存代理 IP 池失败', toMessage(err))
  } finally {
    poolSaving.value = false
  }
}

async function importBatchProxies(): Promise<void> {
  if (!poolBatchText.value.trim()) {
    toast.error('请先粘贴至少一条代理地址')
    return
  }
  poolAdding.value = true
  try {
    const res = await addProxyPoolEntries({
      raw_text: poolBatchText.value,
      tag: poolBatchTag.value.trim(),
    })
    applyPoolView(res.view)
    poolBatchText.value = ''
    toast.success(`成功导入 ${res.added} 条代理 IP（池内共 ${res.view.stats.total_entries} 条）`)
  } catch (err) {
    toast.error('导入代理失败', toMessage(err))
  } finally {
    poolAdding.value = false
  }
}

const filteredPoolEntries = computed(() => {
  const q = poolKeyword.value.trim().toLowerCase()
  return poolConfig.entries.filter((e) => {
    if (poolTagFilter.value === '__ok' && e.last_check_ok !== true) return false
    if (poolTagFilter.value === '__fail' && e.last_check_ok !== false) return false
    if (
      poolTagFilter.value !== 'all' &&
      poolTagFilter.value !== '__ok' &&
      poolTagFilter.value !== '__fail' &&
      (e.tag || '') !== poolTagFilter.value
    ) {
      return false
    }
    if (!q) return true
    return (
      (e.masked_url || e.url || '').toLowerCase().includes(q) ||
      (e.tag || '').toLowerCase().includes(q) ||
      (e.last_exit_ip || '').toLowerCase().includes(q)
    )
  })
})

async function toggleEntryEnabled(entry: ProxyPoolEntry): Promise<void> {
  if (togglingEntryId.value !== null || poolSaving.value) return
  const prev = entry.enabled
  entry.enabled = !prev
  togglingEntryId.value = entry.id
  try {
    const view = await updateProxyPool({
      enabled: poolConfig.enabled,
      strategy: poolConfig.strategy,
      max_accounts_per_ip: Math.max(0, Number(poolConfig.max_accounts_per_ip) || 0),
      apply_platforms: [...(poolConfig.apply_platforms ?? [])],
      entries: poolConfig.entries.map((e) => ({ ...e })),
      fetch: {
        ...poolConfig.fetch,
        interval_minutes: Math.max(0, Number(poolConfig.fetch.interval_minutes) || 0),
        timeout_seconds: Math.max(3, Math.min(60, Number(poolConfig.fetch.timeout_seconds) || 15)),
      },
      cursor: poolConfig.cursor,
    })
    applyPoolView(view)
  } catch (err) {
    entry.enabled = prev
    toast.error('切换代理状态失败', toMessage(err))
  } finally {
    togglingEntryId.value = null
  }
}

async function removeEntry(entry: ProxyPoolEntry): Promise<void> {
  try {
    const view = await deleteProxyPoolEntry(entry.id)
    applyPoolView(view)
    toast.success('已从 IP 池移除代理', entry.masked_url || entry.url)
  } catch (err) {
    toast.error('删除代理失败', toMessage(err))
  }
}

async function triggerRemoteFetch(): Promise<void> {
  if (!poolConfig.fetch.url.trim()) {
    toast.error('请先填写远程代理拉取 API 地址')
    return
  }
  poolFetching.value = true
  poolFetchFeedback.value = ''
  try {
    // 先保存拉取配置再执行拉取，确保默认协议/账密/JSON路径立即生效
    await saveProxyPool()
    const res = await fetchProxyPool({ url: poolConfig.fetch.url.trim(), dry_run: false })
    if (res.view) applyPoolView(res.view)
    poolFetchFeedback.value = `拉取成功：解析 ${res.result.parsed_count} 条，新增 ${res.result.added_count} 条，当前池总量 ${res.result.total_pool_size} 条`
    toast.success('远程代理拉取完成', poolFetchFeedback.value)
  } catch (err) {
    const msg = toMessage(err, '拉取远程代理失败')
    poolFetchFeedback.value = `拉取失败：${msg}`
    toast.error('远程拉取失败', msg)
  } finally {
    poolFetching.value = false
  }
}

async function probeEntry(entry?: ProxyPoolEntry): Promise<void> {
  if (entry) {
    probingEntryId.value = entry.id
  } else {
    poolProbing.value = true
  }
  try {
    const res = await probeProxyPool(entry ? { entry_id: entry.id } : { limit: 20 })
    const okCount = res.results.filter((r) => r.ok).length
    toast.success(`代理探测完成：${okCount}/${res.results.length} 可用`)
    await loadProxyPool()
  } catch (err) {
    toast.error('代理探测失败', toMessage(err))
  } finally {
    probingEntryId.value = null
    poolProbing.value = false
  }
}

async function testAllocateNext(): Promise<void> {
  poolAllocating.value = true
  try {
    const res = await allocateProxyPoolPreview({ mode: 'pool' })
    toast.success(
      `试抽结果：${res.masked_proxy_url || '直连'}`,
      `来源: ${res.source}${res.tag ? ` · 标签 #${res.tag}` : ''}`,
    )
    await loadProxyPool()
  } catch (err) {
    toast.error('试抽 IP 失败', toMessage(err))
  } finally {
    poolAllocating.value = false
  }
}

function keyPolicySummary(policy?: AdminKeyProxyPolicy): { label: string; tone: 'neutral' | 'accent' | 'success' | 'info' | 'warning' } {
  if (!policy || !policy.mode || policy.mode === 'inherit') {
    return { label: '跟随全局 IP 池', tone: 'neutral' }
  }
  switch (policy.mode) {
    case 'fixed':
      return {
        label: `固定 · ${policy.fixed_proxy_masked || policy.fixed_proxy_url || '已设'}`,
        tone: 'accent',
      }
    case 'pool':
      return {
        label: `池子${policy.pool_tag ? ` #${policy.pool_tag}` : ''}${policy.strategy && policy.strategy !== 'inherit' ? ` · ${policy.strategy}` : ''}`,
        tone: 'success',
      }
    case 'fetch':
      return { label: '实时远程拉取', tone: 'info' }
    case 'direct':
      return { label: '强制直连', tone: 'warning' }
    default:
      return { label: '跟随全局 IP 池', tone: 'neutral' }
  }
}

async function copyEndpoint(text: string, label: string): Promise<void> {
  const ok = await copyText(text)
  if (ok) toast.success(`已复制${label}`, text)
  else toast.error('复制失败')
}

function openCreate(defaultScope: AdminKeyScope = 'full'): void {
  editing.value = null
  formError.value = ''
  form.name = defaultScope === 'push_accounts' ? '远程自动补号 Key' : ''
  form.scopes = defaultScope
  form.allowed_ips = ''
  form.ttlDays = '0'
  form.status = 'active'
  form.proxy_mode = 'inherit'
  form.fixed_proxy_url = ''
  form.pool_tag = ''
  form.proxy_strategy = 'inherit'
  form.fetch_url_override = ''
  form.allow_override_in_push = true
  formOpen.value = true
}

function openEdit(item: AdminKey): void {
  editing.value = item
  formError.value = ''
  form.name = item.name
  if (item.scopes === 'readonly' || item.scopes === 'push_accounts') {
    form.scopes = item.scopes
  } else {
    form.scopes = 'full'
  }
  form.allowed_ips = splitAllowedIps(item.allowed_ips).join('\n')
  form.ttlDays = 'keep'
  form.status = item.status || 'active'
  const p = item.proxy_policy
  form.proxy_mode = (p?.mode as KeyProxyMode) || 'inherit'
  form.fixed_proxy_url = p?.fixed_proxy_url || ''
  form.pool_tag = p?.pool_tag || ''
  form.proxy_strategy = p?.strategy || 'inherit'
  form.fetch_url_override = p?.fetch_url_override || ''
  form.allow_override_in_push = p?.allow_override_in_push ?? true
  formOpen.value = true
}

function isValidIpOrCidr(entry: string): boolean {
  const [ipPart, cidrPart, extra] = entry.split('/')
  if (!ipPart || extra !== undefined) return false
  const isV4 = /^(\d{1,3}\.){3}\d{1,3}$/.test(ipPart) && ipPart.split('.').every((n) => Number(n) <= 255)
  const isV6 = ipPart.includes(':') && /^[0-9a-fA-F:]{2,39}$/.test(ipPart)
  if (!isV4 && !isV6) return false
  if (cidrPart === undefined) return true
  if (!/^\d{1,3}$/.test(cidrPart)) return false
  const prefix = Number(cidrPart)
  return isV4 ? prefix >= 0 && prefix <= 32 : prefix >= 0 && prefix <= 128
}

async function submitForm(): Promise<void> {
  if (!form.name.trim()) {
    formError.value = '请输入管理密钥名称'
    return
  }
  const ips = splitAllowedIps(form.allowed_ips)
  const invalidIp = ips.find((entry) => !isValidIpOrCidr(entry))
  if (invalidIp) {
    formError.value = `IP 或 CIDR 格式不合法：${invalidIp}`
    return
  }
  if (form.proxy_mode === 'fixed' && !form.fixed_proxy_url.trim()) {
    formError.value = '固定代理模式必须填写代理地址（例如 1.2.3.4:8080 或 http://user:pass@1.2.3.4:8080）'
    return
  }

  const proxyPolicy: AdminKeyProxyPolicy = {
    mode: form.proxy_mode,
    fixed_proxy_url: form.fixed_proxy_url.trim(),
    pool_tag: form.pool_tag.trim(),
    strategy: form.proxy_strategy,
    fetch_url_override: form.fetch_url_override.trim(),
    allow_override_in_push: form.allow_override_in_push,
  }

  saving.value = true
  formError.value = ''
  try {
    const normalizedIps = ips.join(',')
    if (editing.value) {
      let expiresAt: string | null | undefined = undefined
      let clearExpiry = false
      if (form.ttlDays === '0') {
        clearExpiry = true
        expiresAt = null
      } else if (form.ttlDays !== 'keep') {
        const days = Number(form.ttlDays)
        if (days > 0) {
          expiresAt = new Date(Date.now() + days * 86_400_000).toISOString()
        }
      }
      await updateAdminKey(editing.value.id, {
        name: form.name.trim(),
        scopes: form.scopes,
        allowed_ips: normalizedIps,
        status: form.status,
        proxy_policy: proxyPolicy,
        ...(expiresAt !== undefined ? { expires_at: expiresAt } : {}),
        ...(clearExpiry ? { clear_expiry: true } : {}),
      })
      toast.success('远程管理密钥与代理策略已更新', form.name.trim())
      formOpen.value = false
      await load()
    } else {
      const days = Number(form.ttlDays)
      const expiresAt =
        Number.isFinite(days) && days > 0
          ? new Date(Date.now() + days * 86_400_000).toISOString()
          : null
      const created = await createAdminKey({
        name: form.name.trim(),
        scopes: form.scopes,
        allowed_ips: normalizedIps,
        expires_at: expiresAt,
        proxy_policy: proxyPolicy,
      })
      createdPlainKey.value = {
        name: created.item?.name || form.name.trim(),
        key: created.key,
        scopes: created.item?.scopes || form.scopes,
      }
      toast.success('管理密钥签发成功，请立即复制保存明文密钥')
      formOpen.value = false
      await load()
    }
  } catch (err) {
    formError.value = toMessage(err, '保存远程管理密钥失败')
  } finally {
    saving.value = false
  }
}

async function toggleStatus(item: AdminKey): Promise<void> {
  if (togglingId.value !== null) return
  togglingId.value = item.id
  const nextStatus = item.status === 'active' ? 'disabled' : 'active'
  try {
    const updated = await updateAdminKey(item.id, { status: nextStatus })
    const idx = items.value.findIndex((x) => x.id === item.id)
    if (idx >= 0) items.value[idx] = updated
    toast.success(nextStatus === 'active' ? '已启用管理密钥' : '已停用管理密钥', item.name)
  } catch (err) {
    toast.error('状态切换失败', toMessage(err))
  } finally {
    togglingId.value = null
  }
}

async function confirmDelete(): Promise<void> {
  if (!deleteTarget.value) return
  deleting.value = true
  try {
    await deleteAdminKey(deleteTarget.value.id)
    toast.success('管理密钥已吊销删除', deleteTarget.value.name)
    deleteTarget.value = null
    await load()
  } catch (err) {
    toast.error('吊销密钥失败', toMessage(err))
  } finally {
    deleting.value = false
  }
}

onMounted(() => {
  void Promise.all([load(), loadProxyPool()])
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部远程管理与远程补号推送接入卡片 -->
    <Card
      title="远程管理 & 远程补号推送接入指南 (Push URL + IP 池自动分配)"
      subtitle="外部补号脚本、发卡机或号池采集器，拿着推号网址 + adm- 密钥即可一键补号进池；支持直接传 IP、Key 独立绑定 IP、全局 IP 池轮询与远程代理商自动拉取"
      body-class="p-4 space-y-4"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <Button variant="secondary" size="sm" @click="openCreate('push_accounts')">
            <template #leading><Icon name="upload" :size="13" /></template>
            签发补号专用 Key
          </Button>
          <Button variant="primary" size="sm" @click="openCreate('full')">
            <template #leading><Icon name="plus" :size="13" /></template>
            签发管理密钥
          </Button>
        </div>
      </template>

      <!-- 连接四要素条 -->
      <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
        <div class="panel-inset flex items-center justify-between gap-2 px-3 py-2.5 border-accent/30">
          <div class="min-w-0">
            <p class="text-2xs font-medium text-accent">远程补号推送网址 (POST)</p>
            <p class="mt-0.5 truncate font-mono text-xs font-semibold text-fg">
              {{ effectivePushUrl }}
            </p>
          </div>
          <Button
            variant="ghost"
            size="xs"
            icon
            title="复制远程补号网址"
            @click="copyEndpoint(effectivePushUrl, '远程补号网址')"
          >
            <Icon name="copy" :size="13" />
          </Button>
        </div>

        <div class="panel-inset flex items-center justify-between gap-2 px-3 py-2.5">
          <div class="min-w-0">
            <p class="text-2xs text-subtle">Key 自助调 IP 网址 (GET/PUT)</p>
            <p class="mt-0.5 truncate font-mono text-xs font-semibold text-fg">
              {{ effectivePushProxyUrl }}
            </p>
          </div>
          <Button
            variant="ghost"
            size="xs"
            icon
            title="复制 Key 调 IP 接口地址"
            @click="copyEndpoint(effectivePushProxyUrl, 'Key 调 IP 地址')"
          >
            <Icon name="copy" :size="13" />
          </Button>
        </div>

        <div class="panel-inset flex items-center justify-between gap-2 px-3 py-2.5">
          <div class="min-w-0">
            <p class="text-2xs text-subtle">节点 IP:端口 (Host:Port)</p>
            <p class="mt-0.5 truncate font-mono text-xs font-semibold text-fg">{{ hostPortLabel }}</p>
          </div>
          <Button
            variant="ghost"
            size="xs"
            icon
            title="复制 Host:Port"
            @click="copyEndpoint(hostPortLabel, '节点地址')"
          >
            <Icon name="copy" :size="13" />
          </Button>
        </div>

        <div class="panel-inset flex items-center justify-between gap-2 px-3 py-2.5">
          <div class="min-w-0">
            <p class="text-2xs text-subtle">兼容鉴权方式</p>
            <p class="mt-0.5 truncate font-mono text-2xs text-accent">
              {{ connectionHeaders['X-Push-Key'] ? 'X-Push-Key / X-Management-Key / ?key=' : 'X-Management-Key / Bearer adm-...' }}
            </p>
          </div>
          <Badge tone="success" dot>SHA-256 存库</Badge>
        </div>
      </div>

      <!-- 一键复制 cURL / Python 示例 -->
      <div class="space-y-2.5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <SegmentedTabs v-model="snippetLang" :items="snippetTabs" />
          <span class="text-2xs text-subtle">
            分配优先级：<code class="font-mono text-fg">请求显式 IP/proxy_list</code> → <code class="font-mono text-fg">Key 专属策略</code> → <code class="font-mono text-fg">全局 IP 池轮询 / 远程拉取</code>
          </span>
        </div>

        <CodeBlock
          v-if="snippetLang === 'push_curl'"
          title="cURL 远程补号 + IP 池自动分配 / 传 IP / Key 自助换 IP"
          language="bash"
          max-height="18rem"
          :code="pushCurlSnippet"
        />
        <CodeBlock
          v-else-if="snippetLang === 'push_python'"
          title="Python 远程补号与 IP 池轮换示例"
          language="python"
          max-height="18rem"
          :code="pushPythonSnippet"
        />
        <CodeBlock
          v-else-if="snippetLang === 'curl'"
          title="cURL 代理池运维与账号查询"
          language="bash"
          max-height="15rem"
          :code="curlSnippet"
        />
        <CodeBlock
          v-else
          title="Python requests 代理池与远程运维示例"
          language="python"
          max-height="15rem"
          :code="pythonSnippet"
        />
      </div>
    </Card>

    <!-- 代理 IP 池 & 远程拉取自动分配控制台 -->
    <Card
      title="远程补号代理 IP 池 & 动态拉取自动分配"
      subtitle="维护静态出口代理列表或配置外部代理商提取 API；远程补号未指定 proxy_url 时自动按策略分配 IP，并支持池空/达上限时自动拉取新 IP"
      body-class="p-4 space-y-4"
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <Badge :tone="poolConfig.enabled ? 'success' : 'neutral'" dot>
            {{ poolConfig.enabled ? '自动分配已开启' : '自动分配未开启' }}
          </Badge>
          <Badge tone="info">
            可用 {{ poolStats.enabled_entries }} / {{ poolStats.total_entries }} 条 · 已绑 {{ poolStats.bound_total }} 号
          </Badge>
          <Button variant="secondary" size="sm" :loading="poolAllocating" @click="testAllocateNext">
            <template #leading><Icon name="zap" :size="13" /></template>
            试抽下一个 IP
          </Button>
          <Button variant="secondary" size="sm" :loading="poolProbing" @click="probeEntry()">
            <template #leading><Icon name="globe" :size="13" /></template>
            批量测活
          </Button>
          <Button variant="primary" size="sm" :loading="poolSaving" @click="saveProxyPool">
            <template #leading><Icon name="check" :size="13" /></template>
            保存 IP 池配置
          </Button>
        </div>
      </template>

      <!-- 核心策略配置行 -->
      <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
        <div class="panel-inset flex items-center justify-between gap-3 px-3 py-2.5">
          <div>
            <p class="text-xs font-medium text-fg">启用远程补号自动分配 IP</p>
            <p class="text-2xs text-subtle">补号未显式传 IP 时自动从池分配</p>
          </div>
          <Switch v-model="poolConfig.enabled" size="sm" />
        </div>

        <Field label="IP 池分配策略">
          <Select v-model="poolConfig.strategy" :options="strategyOptions" />
        </Field>

        <Field label="每条 IP 最大绑定账号数" hint="0 = 不限；填 1 则严格一账号一独立 IP">
          <Input
            :model-value="String(poolConfig.max_accounts_per_ip)"
            type="number"
            min="0"
            placeholder="0"
            @update:model-value="poolConfig.max_accounts_per_ip = Math.max(0, Number($event) || 0)"
          />
        </Field>

        <Field label="自动分配适用平台">
          <div class="flex flex-wrap gap-1.5 pt-1">
            <button
              v-for="p in allPlatformOptions"
              :key="p.value"
              type="button"
              class="rounded-md border px-2 py-1 text-2xs font-medium transition-colors"
              :class="
                (poolConfig.apply_platforms ?? []).includes(p.value)
                  ? 'border-accent bg-accent/15 text-accent'
                  : 'border-line bg-raised text-subtle hover:text-fg'
              "
              @click="togglePoolPlatform(p.value)"
            >
              {{ p.label }}
            </button>
          </div>
        </Field>
      </div>

      <!-- 双栏：左侧手动批量导入，右侧远程代理商 API 动态拉取 -->
      <div class="grid gap-4 lg:grid-cols-2">
        <!-- 手动批量导入代理 IP -->
        <div class="panel-inset space-y-3 p-3.5">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-1.5 text-xs font-semibold text-fg">
              <Icon name="server" :size="14" class="text-accent" />
              <span>手动批量添加代理 IP 到池</span>
            </div>
            <span class="text-2xs text-subtle">自动去重与规范化</span>
          </div>

          <Textarea
            v-model="poolBatchText"
            :rows="4"
            mono
            :placeholder="'每行一条，支持多种常见格式与 #标签：\nhttp://user:pass@1.2.3.4:8080 #us\nsocks5://5.6.7.8:1080 #jp\n9.10.11.12:9000:user:pass\n13.14.15.16:8888'"
          />

          <div class="flex flex-wrap items-center gap-2">
            <div class="w-40">
              <Input v-model="poolBatchTag" placeholder="默认标签，如 us / jp" />
            </div>
            <Button variant="secondary" size="sm" :loading="poolAdding" @click="importBatchProxies">
              <template #leading><Icon name="plus" :size="13" /></template>
              解析并导入 IP 池
            </Button>
          </div>
        </div>

        <!-- 远程代理商 API 动态拉取配置 -->
        <div class="panel-inset space-y-3 p-3.5">
          <div class="flex items-center justify-between gap-2">
            <div class="flex items-center gap-1.5 text-xs font-semibold text-fg">
              <Icon name="download" :size="14" class="text-accent" />
              <span>远程代理商 API 自动拉取配置</span>
            </div>
            <div class="flex items-center gap-2">
              <span class="text-2xs text-subtle">启用拉取</span>
              <Switch v-model="poolConfig.fetch.enabled" size="sm" />
            </div>
          </div>

          <div class="grid gap-2.5 sm:grid-cols-4">
            <div class="sm:col-span-3">
              <Field label="远程提取 API 网址 (支持纯文本或 JSON 响应)">
                <Input
                  v-model="poolConfig.fetch.url"
                  mono
                  placeholder="https://api.proxy-vendor.com/get?num=20&format=txt"
                />
              </Field>
            </div>
            <Field label="方法">
              <Select v-model="poolConfig.fetch.method" :options="fetchMethodOptions" />
            </Field>
          </div>

          <div class="grid gap-2.5 sm:grid-cols-3">
            <Field label="纯 IP:端口 默认协议">
              <Select v-model="poolConfig.fetch.default_scheme" :options="schemeOptions" />
            </Field>
            <Field label="统一标签 (Tag)">
              <Input v-model="poolConfig.fetch.default_tag" placeholder="remote / us" />
            </Field>
            <Field label="JSON 字段路径 (可选)">
              <Input v-model="poolConfig.fetch.json_path" mono placeholder="留空自动识别，如 data.list" />
            </Field>
          </div>

          <div class="grid gap-2.5 sm:grid-cols-3">
            <Field label="默认代理账号 (可选)">
              <Input v-model="poolConfig.fetch.username" placeholder="纯 ip:port 源可填" />
            </Field>
            <Field label="默认代理密码 (可选)">
              <Input v-model="poolConfig.fetch.password" type="password" placeholder="保存后脱敏" />
            </Field>
            <Field label="定时拉取间隔 (分钟)" hint="0 = 仅手动或池空时自动拉">
              <Input
                :model-value="String(poolConfig.fetch.interval_minutes)"
                type="number"
                min="0"
                @update:model-value="poolConfig.fetch.interval_minutes = Math.max(0, Number($event) || 0)"
              />
            </Field>
          </div>

          <div class="flex flex-wrap items-center justify-between gap-2 pt-1">
            <div class="flex flex-wrap items-center gap-4 text-2xs text-muted">
              <label class="inline-flex cursor-pointer items-center gap-1.5">
                <Switch v-model="poolConfig.fetch.auto_fetch_on_empty" size="sm" />
                <span>池空/达上限时补号自动拉新</span>
              </label>
              <label class="inline-flex cursor-pointer items-center gap-1.5">
                <Switch v-model="poolConfig.fetch.replace_on_fetch" size="sm" />
                <span>拉取时替换旧远程 IP</span>
              </label>
            </div>
            <Button variant="secondary" size="sm" :loading="poolFetching" @click="triggerRemoteFetch">
              <template #leading><Icon name="download" :size="13" /></template>
              立即拉取 IP
            </Button>
          </div>

          <p v-if="poolFetchFeedback || poolConfig.fetch.last_fetched_at" class="text-2xs text-subtle">
            <span v-if="poolFetchFeedback" class="text-fg">{{ poolFetchFeedback }}</span>
            <span v-else-if="poolConfig.fetch.last_fetch_error" class="text-danger">
              最近拉取异常（{{ formatRelative(poolConfig.fetch.last_fetched_at) }}）：{{ poolConfig.fetch.last_fetch_error }}
            </span>
            <span v-else>
              最近拉取：{{ formatRelative(poolConfig.fetch.last_fetched_at) }} · 上次解析 {{ poolConfig.fetch.last_fetch_count || 0 }} 条
            </span>
          </p>
        </div>
      </div>

      <!-- 代理池筛选条 -->
      <div
        v-if="poolConfig.entries.length > 0"
        class="flex flex-wrap items-center justify-between gap-2 pt-1"
      >
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            class="rounded-md border px-2 py-0.5 text-2xs font-medium transition"
            :class="
              poolTagFilter === 'all'
                ? 'border-accent bg-accent/12 text-accent'
                : 'border-line bg-raised text-subtle hover:text-fg'
            "
            @click="poolTagFilter = 'all'"
          >
            全部 ({{ poolConfig.entries.length }})
          </button>
          <button
            type="button"
            class="rounded-md border px-2 py-0.5 text-2xs font-medium transition"
            :class="
              poolTagFilter === '__ok'
                ? 'border-success bg-success/12 text-success'
                : 'border-line bg-raised text-subtle hover:text-fg'
            "
            @click="poolTagFilter = '__ok'"
          >
            探测可用 ({{ poolConfig.entries.filter((e) => e.last_check_ok === true).length }})
          </button>
          <button
            type="button"
            class="rounded-md border px-2 py-0.5 text-2xs font-medium transition"
            :class="
              poolTagFilter === '__fail'
                ? 'border-danger bg-danger/12 text-danger'
                : 'border-line bg-raised text-subtle hover:text-fg'
            "
            @click="poolTagFilter = '__fail'"
          >
            探测异常 ({{ poolConfig.entries.filter((e) => e.last_check_ok === false).length }})
          </button>
          <button
            v-for="tag in poolStats.tags ?? []"
            :key="tag"
            type="button"
            class="rounded-md border px-2 py-0.5 font-mono text-2xs font-medium transition"
            :class="
              poolTagFilter === tag
                ? 'border-accent bg-accent/12 text-accent'
                : 'border-line bg-raised text-subtle hover:text-fg'
            "
            @click="poolTagFilter = tag"
          >
            #{{ tag }}
          </button>
        </div>

        <div class="w-56">
          <Input v-model="poolKeyword" size="sm" placeholder="搜索代理 IP / 出口 / 标签…" />
        </div>
      </div>

      <!-- 代理池条目明细表 -->
      <DataTable
        :columns="7"
        :loading="poolLoading"
        :empty="filteredPoolEntries.length === 0"
        min-width="920px"
        hoverable
      >
        <template #head>
          <th class="th">代理地址 (密码已脱敏)</th>
          <th class="th">标签 / 来源</th>
          <th class="th">已绑定账号</th>
          <th class="th">最近探测出口 IP</th>
          <th class="th">最近分配</th>
          <th class="th">启用</th>
          <th class="th text-right">操作</th>
        </template>

        <template #empty>
          <EmptyState
            icon="globe"
            :title="poolConfig.entries.length > 0 ? '没有匹配的代理条目' : '代理 IP 池为空'"
            :description="
              poolConfig.entries.length > 0
                ? '可清空上方标签或搜索词查看全部代理条目。'
                : '在上方手动粘贴代理列表，或填写远程代理商提取 API 点击「立即拉取 IP」即可建立可自动轮询的出口 IP 池。'
            "
          />
        </template>

        <template #body>
          <tr v-for="entry in filteredPoolEntries" :key="entry.id">
            <td class="td max-w-[18rem]">
              <div class="flex items-center gap-1.5">
                <code
                  class="block max-w-[15rem] truncate font-mono text-2xs text-fg"
                  :title="entry.masked_url || entry.url"
                >
                  {{ entry.masked_url || entry.url }}
                </code>
                <button
                  type="button"
                  class="shrink-0 rounded p-0.5 text-subtle transition hover:text-fg"
                  title="复制脱敏代理地址"
                  @click="copyEndpoint(entry.masked_url || entry.url, '代理地址')"
                >
                  <Icon name="copy" :size="12" />
                </button>
              </div>
            </td>
            <td class="td">
              <div class="flex flex-wrap items-center gap-1">
                <Badge v-if="entry.tag" tone="accent">#{{ entry.tag }}</Badge>
                <Badge :tone="entry.source === 'remote_fetch' ? 'info' : 'neutral'">
                  {{ entry.source === 'remote_fetch' ? '远程拉取' : '手动导入' }}
                </Badge>
              </div>
            </td>
            <td class="td tnum text-xs font-medium text-fg">
              {{ entry.bound_accounts ?? 0 }}
              <span v-if="poolConfig.max_accounts_per_ip > 0" class="text-2xs text-subtle">
                / {{ poolConfig.max_accounts_per_ip }}
              </span>
            </td>
            <td class="td text-2xs">
              <span v-if="entry.last_check_ok === true" class="text-success font-mono">
                {{ entry.last_exit_ip || 'OK' }} ({{ entry.last_latency_ms || 0 }}ms)
              </span>
              <span v-else-if="entry.last_check_ok === false" class="text-danger" :title="entry.last_error">
                不可用 · {{ (entry.last_error || '').slice(0, 28) }}
              </span>
              <span v-else class="text-subtle">未探测</span>
            </td>
            <td class="td text-2xs text-muted">
              {{ entry.last_used_at ? formatRelative(entry.last_used_at) : '未使用' }}
            </td>
            <td class="td">
              <Switch
                size="sm"
                :model-value="entry.enabled"
                :disabled="togglingEntryId === entry.id || poolSaving"
                @update:model-value="toggleEntryEnabled(entry)"
              />
            </td>
            <td class="td text-right">
              <div class="inline-flex items-center gap-1">
                <Button
                  variant="ghost"
                  size="xs"
                  :loading="probingEntryId === entry.id"
                  title="探测此代理连通性与出口 IP"
                  @click="probeEntry(entry)"
                >
                  <Icon name="globe" :size="13" />
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  class="hover:bg-danger/10 hover:text-danger"
                  title="从池中移除"
                  @click="removeEntry(entry)"
                >
                  <Icon name="trash" :size="13" />
                </Button>
              </div>
            </td>
          </tr>
        </template>
      </DataTable>
    </Card>

    <!-- 管理密钥列表工具栏 -->
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex items-center gap-2 text-xs text-muted">
        <Icon name="shieldKey" :size="15" class="text-accent" />
        <span>已签发远程补号 / 管理密钥列表（共 {{ items.length }} 把）</span>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="secondary" size="sm" :loading="loading" @click="load">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新列表
        </Button>
        <Button variant="primary" size="sm" @click="openCreate('push_accounts')">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建补号 / 管理密钥
        </Button>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="load">重试</Button>
    </p>

    <DataTable
      :columns="9"
      :loading="loading"
      :empty="items.length === 0"
      min-width="1180px"
      hoverable
    >
      <template #head>
        <th class="th">密钥名称</th>
        <th class="th">前缀标识</th>
        <th class="th">权限范围</th>
        <th class="th">补号代理策略</th>
        <th class="th">来源 IP 白名单</th>
        <th class="th">启用状态</th>
        <th class="th">最近调用</th>
        <th class="th">有效期至</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="shieldKey"
          title="尚未签发远程管理密钥"
          description="签发 adm- 前缀密钥后，可为每把补号 Key 单独绑定固定 IP、指定 IP 池标签或实时拉取源。"
        >
          <Button variant="primary" size="sm" @click="openCreate('push_accounts')">
            <template #leading><Icon name="plus" :size="13" /></template>
            立即签发第一把远程补号 Key
          </Button>
        </EmptyState>
      </template>

      <template #body>
        <tr v-for="item in items" :key="item.id">
          <td class="td max-w-[14rem]">
            <div class="flex items-center gap-1.5">
              <span class="truncate font-medium text-fg">{{ item.name }}</span>
              <Badge v-if="item.is_bootstrap" tone="info">环境变量预设</Badge>
            </div>
            <span class="tnum text-2xs text-subtle">
              #{{ item.id }} · 创建于 {{ formatDateTime(item.created_at).slice(0, 10) }}
            </span>
          </td>

          <td class="td">
            <code class="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-muted">
              {{ item.masked_key || `${item.key_prefix}••••••••••••` }}
            </code>
          </td>

          <td class="td">
            <Badge v-if="item.scopes === 'push_accounts'" tone="success" dot>远程补号 (push_accounts)</Badge>
            <Badge v-else-if="item.scopes === 'readonly'" tone="info" dot>只读审计 (readonly)</Badge>
            <Badge v-else tone="accent" dot>读写全权 (full)</Badge>
          </td>

          <td class="td max-w-[16rem]">
            <Badge
              :tone="keyPolicySummary(item.proxy_policy).tone"
              mono
              class="max-w-[14rem] truncate"
              :title="keyPolicySummary(item.proxy_policy).label"
            >
              {{ keyPolicySummary(item.proxy_policy).label }}
            </Badge>
          </td>

          <td class="td max-w-[14rem]">
            <div v-if="splitAllowedIps(item.allowed_ips).length > 0" class="flex flex-wrap gap-1">
              <Badge
                v-for="cidr in splitAllowedIps(item.allowed_ips).slice(0, 2)"
                :key="cidr"
                tone="neutral"
                mono
              >
                {{ cidr }}
              </Badge>
              <Badge v-if="splitAllowedIps(item.allowed_ips).length > 2" tone="neutral">
                +{{ splitAllowedIps(item.allowed_ips).length - 2 }}
              </Badge>
            </div>
            <span v-else class="text-2xs text-subtle">不限来源 IP</span>
          </td>

          <td class="td">
            <div class="flex items-center gap-2">
              <Switch
                size="sm"
                :model-value="item.status === 'active'"
                :disabled="togglingId === item.id || Boolean(item.is_bootstrap)"
                @update:model-value="toggleStatus(item)"
              />
              <StatusPill :status="item.status" />
            </div>
          </td>

          <td class="td text-2xs text-muted" :title="formatDateTime(item.last_used_at)">
            {{ formatRelative(item.last_used_at) }}
          </td>

          <td class="td">
            <span
              class="text-2xs"
              :class="item.expires_at && isExpired(item.expires_at) ? 'font-medium text-danger' : 'text-muted'"
            >
              {{ formatExpiry(item.expires_at) }}
            </span>
          </td>

          <td class="td text-right">
            <div class="inline-flex items-center justify-end gap-1">
              <Button
                variant="ghost"
                size="xs"
                :disabled="Boolean(item.is_bootstrap)"
                title="修改权限范围、补号代理策略与 IP 白名单"
                @click="openEdit(item)"
              >
                <Icon name="edit" :size="13" />
              </Button>
              <Button
                variant="ghost"
                size="xs"
                class="hover:bg-danger/10 hover:text-danger"
                :disabled="Boolean(item.is_bootstrap)"
                title="吊销并永久删除"
                @click="deleteTarget = item"
              >
                <Icon name="trash" :size="13" />
              </Button>
            </div>
          </td>
        </tr>
      </template>
    </DataTable>

    <!-- 新建/编辑管理密钥弹窗（含 Key 专属代理 IP 分配策略） -->
    <Modal
      :open="formOpen"
      size="lg"
      :persistent="saving"
      :title="editing ? `编辑密钥与代理策略 · ${editing.name}` : '签发远程补号 / 管理密钥'"
      subtitle="支持为每把密钥独立指定固定代理 IP、IP 池标签子池、实时拉取源或是否允许请求体覆盖代理"
      @update:open="formOpen = $event"
    >
      <form class="space-y-4" @submit.prevent="submitForm">
        <p
          v-if="formError"
          class="flex items-center gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="13" />
          <span>{{ formError }}</span>
        </p>

        <div class="grid gap-3 sm:grid-cols-2">
          <Field label="密钥备注名称" required>
            <Input v-model="form.name" placeholder="例如：美国号池自动补号机 / 运维总控台" maxlength="120" />
          </Field>

          <Field :label="editing ? '重置有效期' : '有效期限'">
            <Select
              v-model="form.ttlDays"
              :options="
                editing
                  ? [{ label: '保持原过期时间不变', value: 'keep' }, ...ttlPresetOptions]
                  : ttlPresetOptions
              "
            />
          </Field>
        </div>

        <Field label="权限范围 (Scopes)" required>
          <Select v-model="form.scopes" :options="scopeOptions" />
        </Field>

        <!-- Key 专属代理 IP 分配策略区 -->
        <div class="panel-inset space-y-3 p-3.5">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-1.5 text-xs font-semibold text-fg">
              <Icon name="globe" :size="14" class="text-accent" />
              <span>此密钥的远程补号代理 IP 分配策略</span>
            </div>
            <label class="inline-flex cursor-pointer items-center gap-1.5 text-2xs text-muted">
              <Switch v-model="form.allow_override_in_push" size="sm" />
              <span>允许推号请求体显式传 IP 覆盖</span>
            </label>
          </div>

          <div class="grid gap-3 sm:grid-cols-2">
            <Field label="代理分配模式">
              <Select v-model="form.proxy_mode" :options="keyProxyModeOptions" />
            </Field>

            <Field v-if="form.proxy_mode === 'fixed'" label="固定出口代理地址" required>
              <Input
                v-model="form.fixed_proxy_url"
                mono
                placeholder="http://user:pass@1.2.3.4:8080 或 1.2.3.4:8080:u:p"
              />
            </Field>

            <Field v-else-if="form.proxy_mode === 'pool' || form.proxy_mode === 'inherit'" label="限定 IP 池标签 (可选)" hint="例如填 us 则此 Key 只抽带 #us 标签的代理">
              <Input v-model="form.pool_tag" placeholder="留空表示全池轮询" />
            </Field>

            <Field v-else-if="form.proxy_mode === 'fetch'" label="独立远程拉取 API 地址 (可选)" hint="留空则使用全局 IP 池配置的拉取 API">
              <Input v-model="form.fetch_url_override" mono placeholder="https://api.vendor.com/get?region=us" />
            </Field>
          </div>

          <div v-if="form.proxy_mode === 'pool' || form.proxy_mode === 'fetch'" class="grid gap-3 sm:grid-cols-2">
            <Field label="覆盖轮询策略">
              <Select v-model="form.proxy_strategy" :options="keyStrategyOptions" />
            </Field>
            <Field v-if="form.proxy_mode === 'pool'" label="池空时专用拉取 API (可选)">
              <Input v-model="form.fetch_url_override" mono placeholder="留空则跟随全局远程拉取源" />
            </Field>
          </div>
        </div>

        <Field
          label="允许访问的来源 IP / CIDR 白名单"
          hint="限制哪些服务器 IP 能使用此密钥调用管理/补号接口；留空不限制"
        >
          <Textarea
            v-model="form.allowed_ips"
            :rows="2"
            mono
            :placeholder="'127.0.0.1\n10.0.0.0/8'"
          />
        </Field>
      </form>

      <template #footer>
        <Button variant="ghost" :disabled="saving" @click="formOpen = false">取消</Button>
        <Button variant="primary" :loading="saving" @click="submitForm">
          {{ editing ? '保存修改' : '立即签发' }}
        </Button>
      </template>
    </Modal>

    <!-- 一次性明文密钥高亮弹窗 -->
    <Modal
      :open="createdPlainKey !== null"
      size="md"
      title="远程管理密钥已签发（仅展示一次）"
      subtitle="数据库仅保存该密钥的 SHA-256 哈希值，关闭弹窗后将无法再次查看完整明文，请立即妥善复制保存！"
      persistent
      @update:open="createdPlainKey = null"
    >
      <div v-if="createdPlainKey" class="space-y-3.5">
        <div class="flex items-center justify-between gap-2 rounded-md border border-success/30 bg-success/10 px-3 py-2 text-xs text-success">
          <div class="flex items-center gap-1.5 font-medium">
            <Icon name="checkCircle" :size="15" />
            <span>{{ createdPlainKey.name }}</span>
          </div>
          <Badge :tone="createdPlainKey.scopes === 'readonly' ? 'info' : 'accent'">
            {{ createdPlainKey.scopes }}
          </Badge>
        </div>

        <CodeBlock
          title="明文 Management / Push Key"
          language="secret"
          :code="createdPlainKey.key"
        />
        <CodeBlock
          title="远程补号推送网址 (POST)"
          language="text"
          :code="effectivePushUrl"
        />
        <CodeBlock
          title="Key 自助调整代理 IP 网址 (GET / PUT)"
          language="text"
          :code="effectivePushProxyUrl"
        />
      </div>

      <template #footer>
        <Button
          v-if="createdPlainKey"
          variant="secondary"
          @click="copyEndpoint(createdPlainKey.key, '完整管理密钥')"
        >
          <template #leading><Icon name="copy" :size="13" /></template>
          一键复制密钥
        </Button>
        <Button variant="primary" @click="createdPlainKey = null">我已妥善保存，关闭</Button>
      </template>
    </Modal>

    <ConfirmDialog
      :open="deleteTarget !== null"
      tone="danger"
      title="吊销远程管理密钥"
      :message="`确定立即吊销并删除管理密钥「${deleteTarget?.name ?? ''}」（${deleteTarget?.key_prefix ?? ''}…）吗？正在使用该密钥的外部脚本将立即收到 401。`"
      confirm-text="确认吊销"
      :loading="deleting"
      @update:open="deleteTarget = null"
      @confirm="confirmDelete"
    />
  </div>
</template>
