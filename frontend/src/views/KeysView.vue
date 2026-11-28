<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import { groupsApi, keysApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { ApiKey, Group } from '@/api/types'
import ApiKeyFormModal from '@/components/keys/ApiKeyFormModal.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import StatusPill from '@/components/ui/StatusPill.vue'
import Switch from '@/components/ui/Switch.vue'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'
import {
  formatCost,
  formatDateTime,
  formatExpiry,
  formatNumber,
  formatRelative,
  isExpired,
} from '@/utils/format'

const toast = useToastStore()

const items = ref<ApiKey[]>([])
const groups = ref<Group[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')

const keyword = ref('')
const status = ref('')
const groupFilter = ref('')

const formOpen = ref(false)
const editing = ref<ApiKey | null>(null)
const justCreated = ref<ApiKey | null>(null)
const revealed = ref<number[]>([])
const resetTarget = ref<ApiKey | null>(null)
const resetting = ref(false)
const deleteTarget = ref<ApiKey | null>(null)
const deleting = ref(false)
const copiedId = ref<number | null>(null)
const togglingId = ref<number | null>(null)

// ---- 三协议快速接入代码弹窗 & 底部面板 ----
type ProtocolTab = 'chat' | 'messages' | 'responses'
const protocolTab = ref<ProtocolTab>('messages')
const selectedGuideKeyId = ref('')
const guideModalOpen = ref(false)

const protocolTabItems = [
  { value: 'messages', label: 'Anthropic (/v1/messages)', icon: 'terminal' as const },
  { value: 'chat', label: 'OpenAI (/v1/chat/completions)', icon: 'code' as const },
  { value: 'responses', label: 'Codex (/v1/responses)', icon: 'cpu' as const },
]

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '禁用', value: 'disabled' },
]

const groupMap = computed(() => {
  const m = new Map<number, Group>()
  for (const g of groups.value) m.set(g.id, g)
  return m
})

const groupOptions = computed(() =>
  groups.value.map((g) => ({ label: g.name, value: String(g.id) })),
)

const keySelectOptions = computed(() =>
  items.value.map((k) => ({
    label: `${k.name}${k.group_name ? ` (${k.group_name})` : ''}`,
    value: String(k.id),
  })),
)

const activeGuideKeyObj = computed<ApiKey | null>(() => {
  if (selectedGuideKeyId.value) {
    const matched = items.value.find((k) => String(k.id) === selectedGuideKeyId.value)
    if (matched) return matched
  }
  if (justCreated.value) return justCreated.value
  return items.value.find((k) => k.status === 'active') ?? items.value[0] ?? null
})

const activeGuideKey = computed(
  () => activeGuideKeyObj.value?.key ?? 'sk-wb2a-your-downstream-api-key',
)

const gatewayOrigin = computed(() => {
  if (typeof window !== 'undefined' && window.location?.origin) {
    return window.location.origin
  }
  return 'http://127.0.0.1:8080'
})

const clientGuide = computed(() => {
  const origin = gatewayOrigin.value
  const key = activeGuideKey.value

  if (protocolTab.value === 'messages') {
    return {
      title: 'Anthropic Messages 协议（Claude Code CLI / Anthropic SDK）',
      endpoint: `${origin}/v1/messages`,
      language: 'bash',
      note: '默认自动启用内容风控脱敏与分组前置 System 提示词注入。',
      snippet: `# 1. Claude Code CLI 环境变量（Linux / macOS）
export ANTHROPIC_BASE_URL="${origin}"
export ANTHROPIC_AUTH_TOKEN="${key}"
claude

# 2. Windows PowerShell
$env:ANTHROPIC_BASE_URL="${origin}"
$env:ANTHROPIC_AUTH_TOKEN="${key}"
claude

# 3. 原生 /v1/messages cURL 流式验证
curl -N "${origin}/v1/messages" \\
  -H "x-api-key: ${key}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "content-type: application/json" \\
  -d '{"model":"claude-sonnet-4-6","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Hello"}]}'`,
    }
  }

  if (protocolTab.value === 'responses') {
    return {
      title: 'OpenAI Responses 协议（Codex CLI / OpenAI Responses SDK）',
      endpoint: `${origin}/v1/responses`,
      language: 'toml',
      note: '支持推理思考块透传、工具调用与 instructions 自动风控清洗。',
      snippet: `# 1. ~/.codex/config.toml
model_provider = "analy2api"
model = "gemini-4-neon-high"

[model_providers.analy2api]
name = "Analy2API Gateway"
base_url = "${origin}/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"

# 导出密钥并启动 Codex CLI
export OPENAI_API_KEY="${key}"
codex

# 2. 原生 /v1/responses cURL 调用验证
curl -sS "${origin}/v1/responses" \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"gemini-4-neon-high","input":"Write a quick sort in Go","stream":false}'`,
    }
  }

  return {
    title: 'OpenAI Chat Completions 协议（Cursor / Cherry Studio / OpenCode / Python SDK）',
    endpoint: `${origin}/v1/chat/completions`,
    language: 'bash',
    note: '标准 OpenAI /v1/chat/completions 兼容入口，无缝对接任意第三方 AI 客户端。',
    snippet: `# 1. 通用环境变量
OPENAI_BASE_URL="${origin}/v1"
OPENAI_API_KEY="${key}"

# 2. /v1/chat/completions cURL 验证
curl -sS "${origin}/v1/chat/completions" \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"ping"}],"stream":false}'

# 3. Python OpenAI SDK
from openai import OpenAI
client = OpenAI(base_url="${origin}/v1", api_key="${key}")
res = client.chat.completions.create(
    model="gemini-2.5-pro",
    messages=[{"role": "user", "content": "Hello from Analy2API!"}],
)
print(res.choices[0].message.content)`,
  }
})

const filtersActive = computed(
  () => Boolean(keyword.value.trim()) || Boolean(status.value) || Boolean(groupFilter.value),
)

const truncated = computed(() => total.value > items.value.length)

let searchTimer: ReturnType<typeof setTimeout> | null = null
let copyTimer: ReturnType<typeof setTimeout> | null = null
let suppressFilterWatch = false

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const response = await keysApi.list({
      keyword: keyword.value.trim() || undefined,
      status: status.value || undefined,
      group_id: groupFilter.value ? Number(groupFilter.value) : undefined,
    })
    items.value = response.items ?? []
    total.value = response.total ?? 0
    const ids = new Set(items.value.map((item) => item.id))
    revealed.value = revealed.value.filter((id) => ids.has(id))
    if (
      (!selectedGuideKeyId.value || !items.value.some((k) => String(k.id) === selectedGuideKeyId.value)) &&
      items.value.length > 0
    ) {
      const active = items.value.find((k) => k.status === 'active') ?? items.value[0]
      if (active) selectedGuideKeyId.value = String(active.id)
    }
  } catch (err) {
    error.value = toMessage(err, '加载密钥列表失败')
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
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

onMounted(async () => {
  await Promise.all([load(), loadGroups()])
})

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
  if (copyTimer) clearTimeout(copyTimer)
})

watch(keyword, () => {
  if (suppressFilterWatch) return
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => void load(), 300)
})

watch([status, groupFilter], () => {
  if (suppressFilterWatch) return
  void load()
})

function openCreate(): void {
  editing.value = null
  formOpen.value = true
}

function openEdit(item: ApiKey): void {
  editing.value = item
  formOpen.value = true
}

function openGuideForKey(item?: ApiKey): void {
  if (item) {
    selectedGuideKeyId.value = String(item.id)
  }
  guideModalOpen.value = true
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
  queueMicrotask(() => {
    suppressFilterWatch = false
    void load()
  })
}

async function toggleKeyStatus(item: ApiKey): Promise<void> {
  if (togglingId.value !== null) return
  togglingId.value = item.id
  const next = item.status === 'active' ? 'disabled' : 'active'
  try {
    const updated = await keysApi.update(item.id, {
      name: item.name,
      group_id: item.group_id,
      quota: item.quota,
      ip_whitelist: item.ip_whitelist ?? [],
      status: next,
    })
    const idx = items.value.findIndex((k) => k.id === item.id)
    if (idx >= 0) items.value[idx] = updated
    else await load()
    toast.success(next === 'active' ? '密钥已启用' : '密钥已禁用', item.name)
  } catch (err) {
    toast.error('切换密钥状态失败', toMessage(err))
  } finally {
    togglingId.value = null
  }
}

function onCreated(created: ApiKey): void {
  justCreated.value = created
  selectedGuideKeyId.value = String(created.id)
  if (!revealed.value.includes(created.id)) revealed.value = [...revealed.value, created.id]
}

function toggleReveal(id: number): void {
  revealed.value = revealed.value.includes(id)
    ? revealed.value.filter((item) => item !== id)
    : [...revealed.value, id]
}

async function copy(value: string, id: number): Promise<void> {
  const ok = await copyText(value)
  if (ok) {
    copiedId.value = id
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => {
      if (copiedId.value === id) copiedId.value = null
    }, 1600)
    toast.success('密钥已复制到剪贴板')
  } else {
    toast.error('复制失败', '请手动选中密钥文本复制')
  }
}

function quotaPercent(item: ApiKey): number {
  if (item.quota <= 0) return 0
  return Math.min((item.quota_used / item.quota) * 100, 100)
}

function quotaTone(item: ApiKey): string {
  const percent = quotaPercent(item)
  if (percent >= 100) return 'bg-danger'
  if (percent >= 80) return 'bg-warning'
  return 'bg-accent'
}

async function confirmReset(): Promise<void> {
  const target = resetTarget.value
  if (!target) return
  resetting.value = true
  try {
    await keysApi.resetQuota(target.id)
    toast.success('已重置已用额度', target.name)
    resetTarget.value = null
    await load()
  } catch (err) {
    toast.error('重置额度失败', toMessage(err))
  } finally {
    resetting.value = false
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  try {
    await keysApi.remove(target.id)
    toast.success('密钥已删除', target.name)
    if (justCreated.value?.id === target.id) justCreated.value = null
    deleteTarget.value = null
    await load()
  } catch (err) {
    toast.error('删除密钥失败', toMessage(err))
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <!-- 无分组提示 -->
    <div
      v-if="!loading && groups.length === 0"
      class="flex flex-wrap items-center gap-2 rounded-panel border border-warning/30 bg-warning/10 px-3 py-2.5 text-2xs text-warning"
    >
      <Icon name="alert" :size="14" />
      <span>尚未创建任何路由分组，无法签发下游密钥。请先创建一个分组并绑定上游平台池。</span>
      <RouterLink :to="{ name: 'groups' }" class="ml-auto shrink-0">
        <Button variant="secondary" size="xs">前往创建分组</Button>
      </RouterLink>
    </div>

    <!-- 新建密钥高亮横幅 -->
    <div
      v-if="justCreated"
      class="rounded-panel border border-success/30 bg-success/10 p-3 space-y-2"
    >
      <div class="flex flex-wrap items-center gap-2">
        <Icon name="checkCircle" :size="14" class="text-success" />
        <span class="text-[13px] font-medium text-fg">密钥「{{ justCreated.name }}」已签发</span>
        <span class="text-2xs text-muted">可立即复制密钥或打开三协议快速接入代码</span>
        <div class="ml-auto flex items-center gap-2">
          <Button variant="secondary" size="xs" @click="openGuideForKey(justCreated)">
            <template #leading><Icon name="code" :size="12" /></template>
            快速接入代码
          </Button>
          <Button variant="primary" size="xs" @click="copy(justCreated.key, justCreated.id)">
            <template #leading><Icon name="copy" :size="12" /></template>
            复制密钥
          </Button>
          <Button variant="ghost" size="xs" icon title="收起" @click="justCreated = null">
            <Icon name="close" :size="13" />
          </Button>
        </div>
      </div>
      <CodeBlock :code="justCreated.key" language="api-key" />
    </div>

    <!-- 顶部筛选与操作栏 -->
    <div class="flex flex-wrap items-center gap-2.5">
      <div class="w-full min-w-[12rem] sm:w-56">
        <Input v-model="keyword" size="sm" placeholder="搜索密钥名称…" />
      </div>

      <div class="w-32">
        <Select v-model="status" size="sm" :options="statusOptions" placeholder="全部状态" />
      </div>

      <div class="w-44">
        <Select v-model="groupFilter" size="sm" :options="groupOptions" placeholder="全部分组" />
      </div>

      <Button v-if="filtersActive" variant="ghost" size="sm" @click="clearFilters">
        清空筛选
      </Button>

      <div class="ml-auto flex flex-wrap items-center gap-2">
        <Button variant="secondary" size="sm" @click="openGuideForKey()">
          <template #leading><Icon name="terminal" :size="13" /></template>
          快速接入代码
        </Button>
        <Button variant="secondary" size="sm" :loading="loading" @click="load">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新
        </Button>
        <Button
          variant="primary"
          size="sm"
          :disabled="groups.length === 0"
          :title="groups.length === 0 ? '请先创建分组' : ''"
          @click="openCreate"
        >
          <template #leading><Icon name="plus" :size="13" /></template>
          签发密钥
        </Button>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="13" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="load">重试</Button>
    </p>

    <DataTable :columns="9" :loading="loading" :empty="items.length === 0" min-width="1200px" hoverable>
      <template #head>
        <th class="th">名称</th>
        <th class="th">密钥</th>
        <th class="th">绑定分组与平台</th>
        <th class="th">状态</th>
        <th class="th w-[13rem]">已用额度 ($)</th>
        <th class="th">最近调用</th>
        <th class="th">过期时间</th>
        <th class="th">创建时间</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="key"
          :title="filtersActive ? '没有匹配的下游密钥' : '还没有下游调用密钥'"
          :description="
            filtersActive
              ? '当前筛选条件下没有结果，可清空筛选条件后重试。'
              : '签发下游密钥给 Claude Code CLI、Codex CLI、Cursor 或 OpenAI SDK 客户端调用三大网关协议。'
          "
        >
          <Button v-if="filtersActive" variant="secondary" size="sm" @click="clearFilters">
            清空筛选
          </Button>
          <Button
            v-else
            variant="primary"
            size="sm"
            :disabled="groups.length === 0"
            @click="openCreate"
          >
            <template #leading><Icon name="plus" :size="13" /></template>
            签发密钥
          </Button>
        </EmptyState>
      </template>

      <template #body>
        <tr v-for="item in items" :key="item.id">
          <td class="td max-w-[13rem]">
            <div class="flex flex-col gap-0.5">
              <span class="truncate font-medium text-fg" :title="item.name">{{ item.name }}</span>
              <div class="flex items-center gap-1.5">
                <span class="tnum text-2xs text-subtle">#{{ item.id }}</span>
                <Badge
                  v-if="item.ip_whitelist && item.ip_whitelist.length > 0"
                  tone="info"
                  mono
                  :title="`来源 IP 白名单：${item.ip_whitelist.join(', ')}`"
                >
                  限 {{ item.ip_whitelist.length }} IP
                </Badge>
              </div>
            </div>
          </td>

          <td class="td">
            <div class="flex items-center gap-1.5">
              <code
                class="max-w-[14rem] truncate rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-muted"
                :title="revealed.includes(item.id) ? item.key : undefined"
              >
                {{ revealed.includes(item.id) ? item.key : `${item.key.slice(0, 10)}••••••••` }}
              </code>
              <button
                type="button"
                class="rounded p-0.5 text-subtle transition hover:text-fg"
                :aria-label="revealed.includes(item.id) ? '隐藏密钥' : '显示密钥'"
                @click="toggleReveal(item.id)"
              >
                <Icon :name="revealed.includes(item.id) ? 'eyeOff' : 'eye'" :size="13" />
              </button>
              <button
                type="button"
                class="rounded p-0.5 transition hover:text-fg"
                :class="copiedId === item.id ? 'text-success' : 'text-subtle'"
                aria-label="复制密钥"
                @click="copy(item.key, item.id)"
              >
                <Icon :name="copiedId === item.id ? 'check' : 'copy'" :size="13" />
              </button>
            </div>
          </td>

          <td class="td max-w-[13rem]">
            <div v-if="item.group_id && groupMap.get(item.group_id)" class="flex items-center gap-1.5">
              <PlatformBadge :platform="groupMap.get(item.group_id)!.platform" short />
              <Badge tone="neutral" class="max-w-[9rem] truncate">
                {{ item.group_name || groupMap.get(item.group_id)!.name }}
              </Badge>
            </div>
            <Badge v-else-if="item.group_name" tone="neutral" class="max-w-[10rem] truncate">
              {{ item.group_name }}
            </Badge>
            <span v-else class="text-2xs text-warning">未绑定分组</span>
          </td>

          <td class="td">
            <div class="flex flex-col items-start gap-1">
              <div class="flex items-center gap-1.5">
                <Switch
                  size="sm"
                  :model-value="item.status === 'active'"
                  :disabled="togglingId === item.id"
                  @update:model-value="toggleKeyStatus(item)"
                />
                <StatusPill :status="item.status" />
              </div>
              <span
                v-if="item.expires_at && isExpired(item.expires_at)"
                class="text-2xs text-danger"
              >
                已过期
              </span>
            </div>
          </td>

          <td class="td">
            <div v-if="item.quota > 0" class="space-y-1">
              <div class="flex items-center justify-between gap-2 text-2xs">
                <span class="tnum font-mono text-muted">
                  ${{ formatCost(item.quota_used) }} / ${{ formatCost(item.quota) }}
                </span>
                <span class="tnum text-subtle">{{ quotaPercent(item).toFixed(1) }}%</span>
              </div>
              <div class="h-1.5 w-full overflow-hidden rounded-full bg-raised">
                <div
                  class="h-full rounded-full"
                  :class="quotaTone(item)"
                  :style="{ width: `${quotaPercent(item)}%` }"
                />
              </div>
            </div>
            <Badge v-else tone="neutral">不限额</Badge>
          </td>

          <td class="td text-muted">{{ formatRelative(item.last_used_at) }}</td>

          <td class="td">
            <span
              class="text-2xs"
              :class="item.expires_at && isExpired(item.expires_at) ? 'text-danger' : 'text-muted'"
              :title="item.expires_at ? formatDateTime(item.expires_at) : ''"
            >
              {{ formatExpiry(item.expires_at) }}
            </span>
          </td>

          <td class="td tnum text-muted">{{ formatDateTime(item.created_at) }}</td>

          <td class="td">
            <div class="flex items-center justify-end gap-1">
              <Button
                variant="ghost"
                size="xs"
                title="生成此密钥的三协议快速接入代码"
                @click="openGuideForKey(item)"
              >
                <template #leading><Icon name="code" :size="12" /></template>
                接入
              </Button>
              <Button
                variant="ghost"
                size="xs"
                :disabled="item.quota <= 0"
                :title="item.quota > 0 ? '清零已用额度' : '不限额密钥无需重置'"
                @click="resetTarget = item"
              >
                重置额度
              </Button>
              <Button variant="ghost" size="xs" icon title="编辑密钥" @click="openEdit(item)">
                <Icon name="edit" :size="13" />
              </Button>
              <Button
                variant="ghost"
                size="xs"
                icon
                class="hover:bg-danger/10 hover:text-danger"
                title="删除密钥"
                @click="deleteTarget = item"
              >
                <Icon name="trash" :size="13" />
              </Button>
            </div>
          </td>
        </tr>
      </template>

      <template #footer>
        <div class="flex flex-wrap items-center gap-2 border-t border-line px-3 py-2 text-2xs text-subtle">
          <span class="tnum">共 {{ formatNumber(total) }} 把下游调用密钥</span>
          <span v-if="truncated" class="text-warning">
            （列表最多展示 200 条，请用关键词收窄范围）
          </span>
        </div>
      </template>
    </DataTable>

    <!-- 页面常驻三协议快速接入面板 -->
    <Card
      title="三协议客户端快速接入指南"
      subtitle="同时兼容 /v1/messages、/v1/chat/completions 与 /v1/responses，自动代入所选密钥"
      body-class="p-4 space-y-3"
    >
      <template #actions>
        <div v-if="keySelectOptions.length > 0" class="w-56">
          <Select
            v-model="selectedGuideKeyId"
            size="sm"
            :options="keySelectOptions"
            placeholder="选择填入代码的密钥"
          />
        </div>
      </template>

      <div class="flex flex-wrap items-center justify-between gap-2">
        <SegmentedTabs v-model="protocolTab" :items="protocolTabItems" />
        <Badge tone="success" dot>内容风控与分组提示词自动生效</Badge>
      </div>

      <p class="text-2xs text-subtle">{{ clientGuide.note }}</p>

      <CodeBlock
        :title="`${clientGuide.title} · ${clientGuide.endpoint}`"
        :language="clientGuide.language"
        max-height="18rem"
        :code="clientGuide.snippet"
      />
    </Card>

    <!-- 独立快速接入代码弹窗（点击工具栏或行内「接入」打开） -->
    <Modal
      :open="guideModalOpen"
      size="xl"
      title="下游客户端快速接入代码"
      :subtitle="
        activeGuideKeyObj
          ? `当前已填入密钥「${activeGuideKeyObj.name}」`
          : '覆盖 Anthropic Messages、OpenAI Chat Completions 与 OpenAI Responses 三大协议'
      "
      @update:open="guideModalOpen = $event"
    >
      <div class="space-y-3.5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <SegmentedTabs v-model="protocolTab" :items="protocolTabItems" />
          <div v-if="keySelectOptions.length > 0" class="w-56">
            <Select
              v-model="selectedGuideKeyId"
              size="sm"
              :options="keySelectOptions"
            />
          </div>
        </div>

        <p class="text-2xs text-muted">{{ clientGuide.note }}</p>

        <CodeBlock
          :title="clientGuide.endpoint"
          :language="clientGuide.language"
          max-height="22rem"
          :code="clientGuide.snippet"
        />
      </div>

      <template #footer>
        <Button variant="primary" size="sm" @click="guideModalOpen = false">完成</Button>
      </template>
    </Modal>

    <ApiKeyFormModal
      v-model:open="formOpen"
      :api-key="editing"
      :groups="groups"
      @saved="load"
      @created="onCreated"
    />

    <ConfirmDialog
      :open="resetTarget !== null"
      title="重置已用额度"
      :message="`将把「${resetTarget?.name ?? ''}」的已用额度清零（当前已用 $${formatCost(resetTarget?.quota_used ?? 0)}）。此操作不可撤销。`"
      confirm-text="重置"
      :loading="resetting"
      @update:open="resetTarget = null"
      @confirm="confirmReset"
    />

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除密钥"
      :message="`确定删除「${deleteTarget?.name ?? ''}」吗？删除后使用该密钥的客户端将立即收到 401。`"
      confirm-text="删除"
      tone="danger"
      :loading="deleting"
      @update:open="deleteTarget = null"
      @confirm="confirmDelete"
    />
  </div>
</template>
