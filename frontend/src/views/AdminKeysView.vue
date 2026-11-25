<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'

import {
  createAdminKey,
  deleteAdminKey,
  listAdminKeys,
  updateAdminKey,
} from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { AdminKey, AdminKeyScope } from '@/api/types'
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
const connectionHeaders = ref<Record<string, string>>({})

const snippetLang = ref('push_curl')
const snippetTabs = [
  { value: 'push_curl', label: '远程补号 (cURL)', icon: 'upload' as const },
  { value: 'push_python', label: '远程补号 (Python)', icon: 'code' as const },
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
})

const scopeOptions: Array<{ label: string; value: AdminKeyScope }> = [
  { label: '远程补号专用 (push_accounts · 仅允许推号/刷新账号入池，最小权限最安全)', value: 'push_accounts' },
  { label: '读写全权 (full · 可远程补号/领奖/建号/容器启停/改配置)', value: 'full' },
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
  const k = demoKeyText.value
  return `# 0. 探测推号连通性与可用分组（支持 X-Push-Key / X-Management-Key / Authorization: Bearer）
curl -sS "${pushUrl}/info" -H "X-Push-Key: ${k}"

# 1. 推送单个 AntiGravity (AGY) 账号（自动建组 + 自动分配 1:1 独立 agy 容器 + 幂等刷新）
curl -sS -X POST "${pushUrl}" \\
  -H "X-Push-Key: ${k}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "platform": "antigravity",
    "email": "acc01@gmail.com",
    "refresh_token": "1//0g_your_google_refresh_token",
    "proxy_url": "http://user:pass@1.2.3.4:7890",
    "group_name": "antigravity",
    "upsert": true,
    "auto_provision": true
  }'

# 2. 批量推送多平台账号（WorkBuddy / Command Code / OpenCode / AntiGravity 混合一次补齐）
curl -sS -X POST "${pushUrl}" \\
  -H "X-Push-Key: ${k}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "upsert": true,
    "accounts": [
      {"platform":"workbuddy","email":"wb01@example.com","access_token":"wb_tok_xxx","refresh_token":"wb_rt_xxx"},
      {"platform":"commandcode","name":"cc-01","api_key":"user_xxxxxxxxxxxx"},
      {"platform":"opencode","name":"oc-go-01","account_mode":"go","api_key":"oc_key_xxx"}
    ]
  }'`
})

const pushPythonSnippet = computed(() => {
  const pushUrl = effectivePushUrl.value
  const k = demoKeyText.value
  return `import requests

PUSH_URL = "${pushUrl}"
HEADERS = {
    "X-Push-Key": "${k}",          # 也可使用 X-Management-Key 或 Authorization: Bearer
    "Content-Type": "application/json",
}

# 1. 单条推送 AntiGravity / WorkBuddy / CommandCode / OpenCode 账号（同名/同邮箱自动刷新）
res = requests.post(
    PUSH_URL,
    headers=HEADERS,
    json={
        "platform": "antigravity",
        "email": "pool-01@gmail.com",
        "refresh_token": "1//0g_xxx",
        "proxy_url": "http://10.0.0.2:7890",
        "group_name": "antigravity",
        "upsert": True,
        "auto_provision": True,
    },
    timeout=60,
)
print("Push Result:", res.status_code, res.json())`
})

const curlSnippet = computed(() => {
  const base = effectiveBaseUrl.value
  const k = demoKeyText.value
  return `# 1. 查询四平台账号池列表（也可使用 Authorization: Bearer ${k}）
curl -sS "${base}/api/accounts?page=1&page_size=20" \\
  -H "X-Management-Key: ${k}"

# 2. 远程拉取四端聚合模型目录与价格
curl -sS "${base}/api/models/catalog?refresh=false" \\
  -H "X-Management-Key: ${k}"

# 3. 远程触发 WorkBuddy 全池签到领奖（需 full 读写权限）
curl -sS -X POST "${base}/api/rewards/run" \\
  -H "X-Management-Key: ${k}" \\
  -H "Content-Type: application/json" \\
  -d '{"platform":"workbuddy","action":"harvest","do_chat":false}'

# 4. 查询 AntiGravity 容器节点运行状态
curl -sS "${base}/api/antigravity/nodes" \\
  -H "X-Management-Key: ${k}"`
})

const pythonSnippet = computed(() => {
  const base = effectiveBaseUrl.value
  const k = demoKeyText.value
  return `import requests

BASE_URL = "${base}"
HEADERS = {
    "X-Management-Key": "${k}",
    "Content-Type": "application/json",
}

# 1. 拉取网关性能总览与账号水位
resp = requests.get(f"{BASE_URL}/api/dashboard", headers=HEADERS, timeout=30)
resp.raise_for_status()
print("Summary:", resp.json().get("summary"))

# 2. 查看 AntiGravity 容器节点列表
nodes = requests.get(f"{BASE_URL}/api/antigravity/nodes", headers=HEADERS, timeout=30).json()
print("Nodes:", [n["name"] for n in nodes.get("items", [])])

# 3. 触发批量领奖保活任务（长任务建议 timeout=300）
harvest = requests.post(
    f"{BASE_URL}/api/rewards/run",
    headers=HEADERS,
    json={"platform": "workbuddy", "action": "harvest", "do_chat": False},
    timeout=300,
).json()
print("Harvest Result:", harvest)`
})

function splitAllowedIps(raw: string): string[] {
  return (raw || '')
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean)
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await listAdminKeys()
    items.value = res.items ?? []
    connectionBaseUrl.value = res.connection?.base_url ?? ''
    connectionPushUrl.value = res.connection?.push_accounts_url ?? ''
    connectionHeaders.value = res.connection?.headers ?? {}
  } catch (err) {
    error.value = toMessage(err, '加载远程管理密钥失败')
    items.value = []
  } finally {
    loading.value = false
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
  formOpen.value = true
}

async function submitForm(): Promise<void> {
  if (!form.name.trim()) {
    formError.value = '请输入管理密钥名称'
    return
  }
  const ips = splitAllowedIps(form.allowed_ips)
  const invalidIp = ips.find((entry) => !/^[0-9a-fA-F:.]{3,45}(\/\d{1,3})?$/.test(entry))
  if (invalidIp) {
    formError.value = `IP 或 CIDR 格式不合法：${invalidIp}`
    return
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
        ...(expiresAt !== undefined ? { expires_at: expiresAt } : {}),
        ...(clearExpiry ? { clear_expiry: true } : {}),
      })
      toast.success('远程管理密钥已更新', form.name.trim())
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
  void load()
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部远程管理与远程补号推送接入卡片 -->
    <Card
      title="远程管理 & 远程补号推送接入指南 (Push URL + Admin / Push Key)"
      subtitle="外部补号脚本、发卡/号池采集器或运维平台，拿着下方推号网址 + adm- 密钥即可一键补号进池（支持四平台、自动建组、幂等刷新与 AGY 独立容器分配）"
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
            <p class="text-2xs text-subtle">管理 API 根路径 (Base URL)</p>
            <p class="mt-0.5 truncate font-mono text-xs font-semibold text-fg">
              {{ effectiveBaseUrl }}/api
            </p>
          </div>
          <Button
            variant="ghost"
            size="xs"
            icon
            title="复制 Base URL"
            @click="copyEndpoint(`${effectiveBaseUrl}/api`, 'API 根地址')"
          >
            <Icon name="copy" :size="13" />
          </Button>
        </div>

        <div class="panel-inset flex items-center justify-between gap-2 px-3 py-2.5">
          <div class="min-w-0">
            <p class="text-2xs text-subtle">兼容鉴权方式</p>
            <p class="mt-0.5 truncate font-mono text-2xs text-accent">
              {{ connectionHeaders['X-Push-Key'] ? 'X-Push-Key / X-Management-Key / Bearer' : 'X-Management-Key / Bearer adm-...' }}
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
            <code class="font-mono text-fg">push_accounts</code> 仅放行推号入池，无法删除账号或修改系统设置；<code class="font-mono text-fg">full</code> 同样支持直接补号
          </span>
        </div>

        <CodeBlock
          v-if="snippetLang === 'push_curl'"
          title="cURL 远程补号推送（单条 / 批量 / 四平台通用）"
          language="bash"
          max-height="18rem"
          :code="pushCurlSnippet"
        />
        <CodeBlock
          v-else-if="snippetLang === 'push_python'"
          title="Python 远程补号脚本示例"
          language="python"
          max-height="18rem"
          :code="pushPythonSnippet"
        />
        <CodeBlock
          v-else-if="snippetLang === 'curl'"
          title="cURL 远程调取账号 / 模型 / 领奖 / 容器状态"
          language="bash"
          max-height="15rem"
          :code="curlSnippet"
        />
        <CodeBlock
          v-else
          title="Python requests 远程运维自动化示例"
          language="python"
          max-height="15rem"
          :code="pythonSnippet"
        />
      </div>
    </Card>

    <!-- 管理密钥列表工具栏 -->
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex items-center gap-2 text-xs text-muted">
        <Icon name="shieldKey" :size="15" class="text-accent" />
        <span>已签发管理密钥列表（共 {{ items.length }} 把）</span>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="secondary" size="sm" :loading="loading" @click="load">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新列表
        </Button>
        <Button variant="primary" size="sm" @click="openCreate('full')">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建管理密钥
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
      :columns="8"
      :loading="loading"
      :empty="items.length === 0"
      min-width="1080px"
      hoverable
    >
      <template #head>
        <th class="th">密钥名称</th>
        <th class="th">前缀标识</th>
        <th class="th">权限范围</th>
        <th class="th">IP / CIDR 白名单</th>
        <th class="th">启用状态</th>
        <th class="th">最近调用</th>
        <th class="th">有效期至</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="shieldKey"
          title="尚未签发远程管理密钥"
          description="签发 adm- 前缀管理密钥后，外部运维脚本可通过 X-Management-Key 请求头安全访问本节点管理 API。"
        >
          <Button variant="primary" size="sm" @click="openCreate('push_accounts')">
            <template #leading><Icon name="plus" :size="13" /></template>
            立即签发第一把远程补号 / 管理密钥
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

          <td class="td max-w-[18rem]">
            <div v-if="splitAllowedIps(item.allowed_ips).length > 0" class="flex flex-wrap gap-1">
              <Badge
                v-for="cidr in splitAllowedIps(item.allowed_ips).slice(0, 3)"
                :key="cidr"
                tone="neutral"
                mono
              >
                {{ cidr }}
              </Badge>
              <Badge v-if="splitAllowedIps(item.allowed_ips).length > 3" tone="neutral">
                +{{ splitAllowedIps(item.allowed_ips).length - 3 }}
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
                title="修改权限范围与 IP 白名单"
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

    <!-- 新建/编辑管理密钥弹窗 -->
    <Modal
      :open="formOpen"
      size="md"
      :title="editing ? `编辑管理密钥 · ${editing.name}` : '签发远程管理密钥'"
      subtitle="管理密钥以 SHA-256 单向哈希存库；新建成功后明文仅展示一次"
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

        <Field label="密钥备注名称" required>
          <Input v-model="form.name" placeholder="例如：Prometheus 巡检探针 / 运维总控台" maxlength="120" />
        </Field>

        <Field label="权限范围 (Scopes)" required>
          <Select v-model="form.scopes" :options="scopeOptions" />
        </Field>

        <Field
          label="允许访问的 IP / CIDR 白名单"
          hint="每行或逗号分隔填写精确 IP（如 10.0.0.5）或网段（如 192.168.1.0/24）；留空不限制"
        >
          <Textarea
            v-model="form.allowed_ips"
            :rows="3"
            mono
            :placeholder="'127.0.0.1\n10.0.0.0/8'"
          />
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
