<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'

import { toMessage } from '@/api/client'
import {
  accountsApi,
  checkAntigravityNodeExitIP,
  completeAntigravityOAuthCallback,
  deleteAntigravityAuthFile,
  deleteAntigravityNode,
  fetchAntigravityEnv,
  groupsApi,
  listAntigravityAuthFiles,
  listAntigravityNodes,
  requestAntigravityOAuthURL,
  rotateAntigravityFingerprint,
  runAntigravityNodeAction,
  syncAntigravityAccounts,
} from '@/api/endpoints'
import type {
  Account,
  AgyPromptMode,
  AntigravityAuthFile,
  AntigravityEnvResponse,
  AntigravityOAuthURLResponse,
  ContainerNode,
  ContainerNodeAction,
  Group,
} from '@/api/types'
import AccountFormatConverterPanel from '@/components/antigravity/AccountFormatConverterPanel.vue'
import AgyPromptPipelinePanel from '@/components/antigravity/AgyPromptPipelinePanel.vue'
import AuthFileUploadModal from '@/components/antigravity/AuthFileUploadModal.vue'
import NodeFormModal from '@/components/antigravity/NodeFormModal.vue'
import NodeLogsModal from '@/components/antigravity/NodeLogsModal.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import Modal from '@/components/ui/Modal.vue'
import Select from '@/components/ui/Select.vue'
import Spinner from '@/components/ui/Spinner.vue'
import StatusPill from '@/components/ui/StatusPill.vue'
import { useToastStore } from '@/stores/toast'
import { copyText } from '@/utils/clipboard'
import {
  formatBytes,
  formatDateTime,
  formatExpiry,
  formatRelative,
  isExpired,
  redactProxyDisplay,
} from '@/utils/format'

const toast = useToastStore()

// ---- 环境与节点状态 ----
const env = ref<AntigravityEnvResponse | null>(null)
const envLoading = ref(true)
const activePromptMode = ref<AgyPromptMode>('default_intercept_continue')

const nodes = ref<ContainerNode[]>([])
const nodesLoading = ref(true)
const nodesError = ref('')
const selectedNodeId = ref<number | null>(null)

// 节点动作加载锁：{ `${nodeId}:${action}`: true }
const actionKey = ref<string | null>(null)

// 节点表单 / 日志 / 删除弹窗
const nodeFormOpen = ref(false)
const editingNode = ref<ContainerNode | null>(null)
const logsNode = ref<ContainerNode | null>(null)
const deleteTarget = ref<ContainerNode | null>(null)
const removeDockerContainer = ref(true)
const deletingNode = ref(false)

// ---- 选中节点的 OAuth 与 .json 凭证文件 ----
const authFiles = ref<AntigravityAuthFile[]>([])
const authLoading = ref(false)
const authError = ref('')
const uploadModalOpen = ref(false)
const deletingFilename = ref<string | null>(null)

// OAuth 授权链接生成与回调闭环
const oauthProvider = ref('antigravity')
const oauthRedirectUri = ref('')
const oauthCallbackInput = ref('')
const oauthLoading = ref(false)
const oauthCompleting = ref(false)
const oauthResult = ref<AntigravityOAuthURLResponse | null>(null)
const rotatingFpId = ref<number | null>(null)
const checkingIpId = ref<number | null>(null)

const providerOptions = [
  { label: 'AntiGravity (默认)', value: 'antigravity' },
  { label: 'Gemini CLI', value: 'gemini-cli' },
  { label: 'Claude Code OAuth', value: 'claude' },
  { label: 'OpenAI Codex OAuth', value: 'codex' },
]

// 关联分组与账号池（用于映射已绑定账号 ID + 一键同步弹窗）
const groups = ref<Group[]>([])
const agyAccounts = ref<Account[]>([])
const agyAccountsTotal = ref(0)
const syncModalOpen = ref(false)
const syncGroupIds = ref<number[]>([])
const syncConcurrency = ref('5')
const syncPriority = ref('50')
const syncMultiplier = ref('1')
const syncing = ref(false)

const selectedNode = computed<ContainerNode | null>(
  () => nodes.value.find((n) => n.id === selectedNodeId.value) ?? nodes.value[0] ?? null,
)

const agyGroups = computed(() => groups.value.filter((g) => g.platform === 'antigravity'))

const runningNodesCount = computed(
  () => nodes.value.filter((n) => n.status === 'running' || n.status === 'online').length,
)

const activeAgyAccountsCount = computed(
  () => agyAccounts.value.filter((a) => a.status === 'active').length,
)

/** 根据 email / filename 匹配已同步到网关的 AntiGravity 账号 ID。 */
const linkedAccountMap = computed(() => {
  const byEmail = new Map<string, Account>()
  const byHint = new Map<string, Account>()
  for (const acc of agyAccounts.value) {
    if (acc.email) byEmail.set(acc.email.toLowerCase(), acc)
    if (acc.name) byHint.set(acc.name.toLowerCase(), acc)
    if (acc.uid) byHint.set(acc.uid.toLowerCase(), acc)
  }
  return { byEmail, byHint }
})

function findLinkedAccountId(file: AntigravityAuthFile): number | null {
  const rawId = file.raw?.account_id
  if (typeof rawId === 'number' && rawId > 0) return rawId
  if (file.email) {
    const hit = linkedAccountMap.value.byEmail.get(file.email.toLowerCase())
    if (hit) return hit.id
  }
  const stem = file.filename.replace(/\.json$/i, '').toLowerCase()
  const byName =
    linkedAccountMap.value.byHint.get(file.filename.toLowerCase()) ??
    linkedAccountMap.value.byHint.get(stem)
  return byName?.id ?? null
}

async function loadEnv(): Promise<void> {
  envLoading.value = true
  try {
    env.value = await fetchAntigravityEnv()
  } catch {
    env.value = null
  } finally {
    envLoading.value = false
  }
}

async function loadMetaPools(): Promise<void> {
  try {
    const [gRes, aRes] = await Promise.all([
      groupsApi.list(),
      accountsApi.list({ platform: 'antigravity', page: 1, page_size: 200 }),
    ])
    groups.value = gRes.items ?? []
    agyAccounts.value = aRes.items ?? []
    agyAccountsTotal.value = aRes.total ?? agyAccounts.value.length
  } catch {
    /* 静默容错，不影响主流程 */
  }
}

async function loadNodes(): Promise<void> {
  nodesLoading.value = true
  nodesError.value = ''
  try {
    const res = await listAntigravityNodes()
    nodes.value = res.items ?? []
    if (nodes.value.length > 0) {
      const exists = nodes.value.some((n) => n.id === selectedNodeId.value)
      if (!exists) {
        selectedNodeId.value = nodes.value[0]!.id
      }
    } else {
      selectedNodeId.value = null
    }
  } catch (err) {
    nodesError.value = toMessage(err, '加载容器节点列表失败')
    nodes.value = []
  } finally {
    nodesLoading.value = false
  }
}

async function loadAuthFiles(nodeId?: number): Promise<void> {
  const targetId = nodeId ?? selectedNode.value?.id
  if (!targetId) {
    authFiles.value = []
    return
  }
  authLoading.value = true
  authError.value = ''
  try {
    const res = await listAntigravityAuthFiles(targetId)
    authFiles.value = res.items ?? []
  } catch (err) {
    authError.value = toMessage(err, '读取节点登录凭证列表失败')
    authFiles.value = []
  } finally {
    authLoading.value = false
  }
}

async function refreshAll(): Promise<void> {
  await Promise.all([loadEnv(), loadNodes(), loadMetaPools()])
  if (selectedNode.value) {
    await loadAuthFiles(selectedNode.value.id)
  }
}

watch(
  () => selectedNode.value?.id,
  (id) => {
    oauthResult.value = null
    if (id) {
      void loadAuthFiles(id)
    } else {
      authFiles.value = []
    }
  },
)

onMounted(async () => {
  await refreshAll()
})

function openCreateNode(): void {
  editingNode.value = null
  nodeFormOpen.value = true
}

function openEditNode(node: ContainerNode): void {
  editingNode.value = node
  nodeFormOpen.value = true
}

async function onNodeSaved(saved: ContainerNode): Promise<void> {
  await loadNodes()
  selectedNodeId.value = saved.id
  await loadAuthFiles(saved.id)
}

function isActionRunning(nodeId: number, act?: string): boolean {
  if (!actionKey.value) return false
  if (!act) return actionKey.value.startsWith(`${nodeId}:`)
  return actionKey.value === `${nodeId}:${act}`
}

async function triggerNodeAction(node: ContainerNode, action: ContainerNodeAction): Promise<void> {
  if (actionKey.value) return
  actionKey.value = `${node.id}:${action}`
  const actionLabels: Record<ContainerNodeAction, string> = {
    start: '启动容器',
    stop: '停止容器',
    restart: '重启容器',
    pull: '拉取镜像',
    check: '连通性检测',
  }
  try {
    const res = await runAntigravityNodeAction(node.id, action)
    if (res.node) {
      const idx = nodes.value.findIndex((item) => item.id === node.id)
      if (idx >= 0) nodes.value[idx] = res.node
    } else {
      await loadNodes()
    }

    if (res.ok) {
      const extra =
        action === 'check'
          ? `延迟 ${res.latency_ms ?? 0} ms · 模型 ${res.models_count ?? 0} 个 · 凭证 ${res.auth_files_count ?? 0} 份`
          : res.message || undefined
      toast.success(`${node.name} · ${actionLabels[action]}成功`, extra)
      if (selectedNode.value?.id === node.id) {
        await loadAuthFiles(node.id)
      }
    } else {
      toast.error(`${node.name} · ${actionLabels[action]}失败`, res.error || res.message || '请查看容器日志')
    }
  } catch (err) {
    toast.error(`${actionLabels[action]}异常`, toMessage(err))
  } finally {
    actionKey.value = null
  }
}

function askDeleteNode(node: ContainerNode): void {
  deleteTarget.value = node
  removeDockerContainer.value = node.mode === 'local_docker'
}

async function confirmDeleteNode(): Promise<void> {
  if (!deleteTarget.value || deletingNode.value) return
  deletingNode.value = true
  const target = deleteTarget.value
  try {
    await deleteAntigravityNode(
      target.id,
      target.mode === 'local_docker' ? removeDockerContainer.value : false,
    )
    toast.success('节点已删除', target.name)
    deleteTarget.value = null
    await loadNodes()
  } catch (err) {
    toast.error('删除节点失败', toMessage(err))
  } finally {
    deletingNode.value = false
  }
}

// ---- 在线 OAuth 授权流与回调闭环 ----
async function generateOAuthLink(): Promise<void> {
  if (!selectedNode.value || oauthLoading.value) return
  oauthLoading.value = true
  try {
    const res = await requestAntigravityOAuthURL(selectedNode.value.id, {
      provider: oauthProvider.value,
      redirect_uri: oauthRedirectUri.value.trim() || undefined,
      group_ids: agyGroups.value[0] ? [agyGroups.value[0].id] : undefined,
    })
    oauthResult.value = res
    toast.success('已生成 OAuth 授权链接', '请在新窗口完成登录后复制回调 URL / Code 提交入池')
  } catch (err) {
    toast.error('获取 OAuth 授权链接失败', toMessage(err))
  } finally {
    oauthLoading.value = false
  }
}

async function submitOAuthCallback(): Promise<void> {
  if (!selectedNode.value || oauthCompleting.value) return
  const raw = oauthCallbackInput.value.trim()
  if (!raw) {
    toast.error('请先粘贴浏览器回调 URL、授权码 code 或 Token JSON')
    return
  }
  oauthCompleting.value = true
  try {
    const looksLikeJson = raw.startsWith('{')
    const looksLikeUrl = raw.includes('://') || raw.includes('?') || raw.includes('localhost') || raw.includes('code=')
    const res = await completeAntigravityOAuthCallback(selectedNode.value.id, {
      session_id: oauthResult.value?.session_id,
      state: oauthResult.value?.state,
      provider: oauthProvider.value,
      group_ids: agyGroups.value[0] ? [agyGroups.value[0].id] : undefined,
      callback_url: looksLikeUrl && !looksLikeJson ? raw : undefined,
      code: !looksLikeUrl && !looksLikeJson ? raw : undefined,
      raw_json: looksLikeJson ? raw : undefined,
    })
    oauthCallbackInput.value = ''
    toast.success('OAuth 授权回调成功', res.message)
    await Promise.all([loadNodes(), loadMetaPools(), loadAuthFiles(selectedNode.value.id)])
  } catch (err) {
    toast.error('提交 OAuth 回调失败', toMessage(err))
  } finally {
    oauthCompleting.value = false
  }
}

async function rotateNodeFingerprintAction(node: ContainerNode): Promise<void> {
  if (rotatingFpId.value !== null) return
  rotatingFpId.value = node.id
  try {
    const res = await rotateAntigravityFingerprint(node.id)
    const idx = nodes.value.findIndex((item) => item.id === node.id)
    if (idx >= 0 && res.node) nodes.value[idx] = res.node
    toast.success(
      `${node.name} · 设备指纹已轮换`,
      `新指纹 ${res.node?.fingerprint?.fingerprint_id ?? ''}（Hostname: ${res.node?.fingerprint?.hostname ?? ''}）`,
    )
  } catch (err) {
    toast.error('轮换节点指纹失败', toMessage(err))
  } finally {
    rotatingFpId.value = null
  }
}

async function checkNodeExitIPAction(node: ContainerNode): Promise<void> {
  if (checkingIpId.value !== null) return
  checkingIpId.value = node.id
  try {
    const res = await checkAntigravityNodeExitIP(node.id)
    if (res.node) {
      const idx = nodes.value.findIndex((item) => item.id === node.id)
      if (idx >= 0) nodes.value[idx] = res.node
    }
    if (res.ok) {
      if (res.shared_with_nodes?.length) {
        toast.warning(
          `${node.name} · 检测到出口 IP 碰撞`,
          `出口 IP ${res.exit_ip} 与节点 ${res.shared_with_nodes.join(', ')} 重复！`,
        )
      } else {
        toast.success(`${node.name} · 独立出口 IP 正常`, `出口 IP: ${res.exit_ip} (${res.latency_ms} ms)`)
      }
    } else {
      toast.error(`${node.name} · 探测出口 IP 失败`, res.error || '请检查 ProxyURL 连通性')
    }
  } catch (err) {
    toast.error('出口 IP 探测请求失败', toMessage(err))
  } finally {
    checkingIpId.value = null
  }
}

async function copyOAuthLink(): Promise<void> {
  const url = oauthResult.value?.auth_url || oauthResult.value?.url || ''
  if (!url) return
  const ok = await copyText(url)
  if (ok) toast.success('授权链接已复制')
  else toast.error('复制失败，请手动选中复制')
}

function openOAuthWindow(): void {
  const url = oauthResult.value?.auth_url || oauthResult.value?.url || ''
  if (!url) return
  window.open(url, '_blank', 'noopener,noreferrer')
}

async function handleDeleteAuthFile(file: AntigravityAuthFile): Promise<void> {
  if (!selectedNode.value || deletingFilename.value) return
  deletingFilename.value = file.filename
  try {
    await deleteAntigravityAuthFile(selectedNode.value.id, file.filename)
    toast.success('凭证文件已移除', file.filename)
    await loadAuthFiles(selectedNode.value.id)
  } catch (err) {
    toast.error('删除凭证文件失败', toMessage(err))
  } finally {
    deletingFilename.value = null
  }
}

function openSyncModal(): void {
  syncGroupIds.value = agyGroups.value[0] ? [agyGroups.value[0].id] : []
  syncModalOpen.value = true
}

function toggleSyncGroup(id: number, checked: boolean): void {
  if (checked) {
    if (!syncGroupIds.value.includes(id)) syncGroupIds.value.push(id)
  } else {
    syncGroupIds.value = syncGroupIds.value.filter((g) => g !== id)
  }
}

async function runSyncAccounts(): Promise<void> {
  if (!selectedNode.value || syncing.value) return
  syncing.value = true
  try {
    const res = await syncAntigravityAccounts(selectedNode.value.id, {
      group_ids: syncGroupIds.value.length > 0 ? syncGroupIds.value : undefined,
      concurrency: Number(syncConcurrency.value) || 5,
      priority: Number(syncPriority.value) || 50,
      rate_multiplier: Number(syncMultiplier.value) || 1,
      auto_assign_default_group: syncGroupIds.value.length === 0,
    })
    toast.success(
      '凭证已同步至调度账号池',
      `新建 ${res.created} 个 · 更新 ${res.updated} 个账号`,
    )
    syncModalOpen.value = false
    await Promise.all([loadNodes(), loadMetaPools(), loadAuthFiles(selectedNode.value.id)])
  } catch (err) {
    toast.error('同步账号池失败', toMessage(err))
  } finally {
    syncing.value = false
  }
}

function nodeEndpoint(node: ContainerNode): string {
  if (node.base_url) return node.base_url
  const host = node.host_ip || '127.0.0.1'
  if (host.startsWith('http://') || host.startsWith('https://')) {
    return `${host.replace(/\/$/, '')}:${node.host_port}`
  }
  return `http://${host}:${node.host_port}`
}

async function onAccountsConverted(): Promise<void> {
  await Promise.all([loadNodes(), loadMetaPools()])
  if (selectedNode.value) {
    await loadAuthFiles(selectedNode.value.id)
  }
}
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部池水位与运行态势概览 -->
    <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <MetricTile
        label="AGY 调度账号池"
        :value="`${activeAgyAccountsCount} / ${agyAccountsTotal}`"
        hint="正常可调度 / 已入库 AGY 账号"
        icon="users"
        tone="success"
      />
      <MetricTile
        label="容器与远程节点"
        :value="`${runningNodesCount} / ${nodes.length}`"
        :hint="env?.docker_available ? `宿主机 Docker v${env.docker_version || '就绪'}` : '本机直连 agy CLI / 远程节点模式'"
        icon="server"
        tone="accent"
      />
      <MetricTile
        label="AGY 提示词管线"
        :value="activePromptMode === 'default_intercept_continue' ? '默认->拦截->接续' : activePromptMode"
        hint="保留原生指纹 · 双向拦截 · 下游接续"
        icon="shield"
        tone="info"
      />
      <MetricTile
        label="当前节点凭证文件"
        :value="selectedNode ? `${authFiles.length} 份` : '未选节点'"
        :hint="selectedNode ? `节点：${selectedNode.name}` : '支持无容器直接全格式转 AGY 入池'"
        icon="fileText"
        tone="neutral"
      />
    </div>

    <!-- 顶部宿主机 Docker 环境与快捷操作条 -->
    <div class="card flex flex-wrap items-center justify-between gap-3 px-4 py-3">
      <div class="flex flex-wrap items-center gap-2.5">
        <Badge
          v-if="envLoading"
          tone="neutral"
          dot
        >
          正在检测宿主机环境…
        </Badge>
        <Badge
          v-else-if="env?.docker_available"
          tone="success"
          dot
        >
          宿主机 Docker 已就绪 · v{{ env.docker_version || 'unknown' }}
        </Badge>
        <Badge
          v-else
          tone="info"
          dot
          :title="env?.error || '未检测到 Docker 守护进程，可使用本机 agy CLI 驱动或远程节点'"
        >
          本机原生 agy CLI 隔离驱动 / 远程节点模式
        </Badge>

        <span v-if="env?.default_image" class="hidden items-center gap-1 text-2xs text-subtle md:inline-flex">
          <span>默认镜像:</span>
          <code class="rounded border border-line bg-raised px-1.5 py-px font-mono text-muted">
            {{ env.default_image }}
          </code>
        </span>

        <span v-if="env?.data_root" class="hidden items-center gap-1 text-2xs text-subtle xl:inline-flex">
          <span>隔离数据目录:</span>
          <code class="max-w-[18rem] truncate rounded border border-line bg-raised px-1.5 py-px font-mono text-muted" :title="env.data_root">
            {{ env.data_root }}
          </code>
        </span>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <Button
          variant="secondary"
          size="sm"
          :loading="nodesLoading || envLoading"
          @click="refreshAll"
        >
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新状态
        </Button>
        <Button variant="secondary" size="sm" @click="uploadModalOpen = true">
          <template #leading><Icon name="upload" :size="13" /></template>
          快速全格式导入弹窗
        </Button>
        <Button variant="primary" size="sm" @click="openCreateNode">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建容器 / 远程节点
        </Button>
      </div>
    </div>

    <!-- 核心面板 1：AGY CLI 提示词运行管线面板 (默认提示词 -> 拦截 -> 接续) -->
    <AgyPromptPipelinePanel @mode-changed="activePromptMode = $event" />

    <!-- 核心面板 2：多账号全格式自动转 AGY CLI 工作台 -->
    <AccountFormatConverterPanel
      :nodes="nodes"
      :selected-node="selectedNode"
      :groups="groups"
      @imported="onAccountsConverted"
    />

    <!-- 错误提示 -->
    <p
      v-if="nodesError"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3.5 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" />
      <span>{{ nodesError }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="loadNodes">重试</Button>
    </p>

    <!-- 板块 3：容器与远程节点列表 -->
    <Card
      title="AntiGravity 容器与远程节点池"
      subtitle="点击任意节点切换下方 OAuth 在线授权与节点级登录凭证池"
      body-class="p-0"
    >
      <DataTable
        :columns="7"
        :loading="nodesLoading"
        :empty="nodes.length === 0"
        :skeleton-rows="3"
        min-width="1080px"
        hoverable
        class="border-0 rounded-none"
      >
        <template #head>
          <th class="th">节点名称 / 模式</th>
          <th class="th">状态</th>
          <th class="th">服务地址 / 容器标识</th>
          <th class="th">1容器 · 1IP · 1指纹 隔离状态</th>
          <th class="th text-right">凭证文件 / 模型</th>
          <th class="th">最近同步</th>
          <th class="th text-right">容器与节点运维</th>
        </template>

        <template #empty>
          <EmptyState
            icon="server"
            title="尚未配置额外 Docker / 远程容器节点"
            description="本机 agy CLI 可直接使用上方「多账号全格式自动转 AGY CLI 工作台」导入的隔离账号池运行；如需多容器并发编排或接入外部远端节点，请点击下方创建。"
          >
            <Button variant="primary" size="sm" @click="openCreateNode">
              <template #leading><Icon name="plus" :size="13" /></template>
              创建第一个容器 / 远程节点
            </Button>
          </EmptyState>
        </template>

        <template #body>
          <tr
            v-for="node in nodes"
            :key="node.id"
            class="cursor-pointer transition"
            :class="selectedNode?.id === node.id ? 'bg-accent/[0.06]' : ''"
            @click="selectedNodeId = node.id"
          >
            <td class="td">
              <div class="flex items-center gap-2">
                <span
                  class="h-2 w-2 shrink-0 rounded-full"
                  :class="selectedNode?.id === node.id ? 'bg-accent' : 'bg-transparent'"
                />
                <div>
                  <div class="flex items-center gap-1.5">
                    <span class="font-semibold text-fg">{{ node.name }}</span>
                    <Badge :tone="node.mode === 'local_docker' ? 'accent' : 'info'" mono>
                      {{ node.mode === 'local_docker' ? 'Docker' : 'Remote' }}
                    </Badge>
                  </div>
                  <p v-if="node.mode === 'local_docker'" class="mt-0.5 max-w-[14rem] truncate font-mono text-2xs text-subtle" :title="node.docker_image">
                    {{ node.docker_image }}
                  </p>
                </div>
              </div>
            </td>

            <td class="td">
              <div class="flex flex-col items-start gap-1">
                <StatusPill :status="node.status" />
                <span v-if="node.latency_ms" class="tnum font-mono text-2xs text-success">
                  {{ node.latency_ms }} ms
                </span>
                <span
                  v-if="node.last_error"
                  class="max-w-[11rem] truncate text-2xs text-danger"
                  :title="node.last_error"
                >
                  {{ node.last_error }}
                </span>
              </div>
            </td>

            <td class="td max-w-[14rem]">
              <div class="truncate font-mono text-xs text-fg" :title="nodeEndpoint(node)">
                {{ nodeEndpoint(node) }}
              </div>
              <div v-if="node.container_name" class="truncate font-mono text-2xs text-subtle">
                容器: {{ node.container_name }}
              </div>
            </td>

            <td class="td max-w-[16rem]">
              <div class="space-y-1">
                <div class="flex flex-wrap items-center gap-1.5">
                  <Badge
                    :tone="
                      node.isolation_status === 'isolated'
                        ? 'success'
                        : node.isolation_status === 'duplicate_proxy'
                          ? 'danger'
                          : 'warning'
                    "
                    dot
                  >
                    {{
                      node.isolation_status === 'isolated'
                        ? '1容器·1IP·1指纹'
                        : node.isolation_status === 'duplicate_proxy'
                          ? 'IP/代理冲突'
                          : '直连(未绑IP)'
                    }}
                  </Badge>
                  <span v-if="node.last_exit_ip" class="font-mono text-2xs text-success">
                    {{ node.last_exit_ip }}
                  </span>
                </div>
                <div class="truncate font-mono text-2xs text-muted" :title="redactProxyDisplay(node.proxy_url)">
                  代理: {{ redactProxyDisplay(node.proxy_url) }}
                </div>
                <div v-if="node.fingerprint?.fingerprint_id" class="truncate font-mono text-2xs text-subtle">
                  指纹: {{ node.fingerprint.fingerprint_id }} · {{ node.fingerprint.hostname }}
                </div>
              </div>
            </td>

            <td class="td tnum text-right">
              <span class="font-semibold text-fg">{{ node.auth_files_count ?? 0 }}</span>
              <span class="text-2xs text-subtle"> 份 JSON</span>
              <div v-if="node.models_count !== undefined" class="text-2xs text-subtle">
                {{ node.models_count }} 个模型
              </div>
            </td>

            <td class="td text-2xs text-muted">
              {{ node.last_sync_at ? formatRelative(node.last_sync_at) : '未同步' }}
            </td>

            <td class="td" @click.stop>
              <div class="flex flex-wrap items-center justify-end gap-1">
                <template v-if="node.mode === 'local_docker'">
                  <Button
                    variant="secondary"
                    size="xs"
                    :disabled="isActionRunning(node.id)"
                    title="启动容器"
                    @click="triggerNodeAction(node, 'start')"
                  >
                    <Spinner v-if="isActionRunning(node.id, 'start')" :size="11" />
                    <Icon v-else name="play" :size="11" />
                    <span>启动</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    :disabled="isActionRunning(node.id)"
                    title="停止容器"
                    @click="triggerNodeAction(node, 'stop')"
                  >
                    <Spinner v-if="isActionRunning(node.id, 'stop')" :size="11" />
                    <Icon v-else name="stop" :size="11" />
                    <span>停止</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    :disabled="isActionRunning(node.id)"
                    title="重启容器"
                    @click="triggerNodeAction(node, 'restart')"
                  >
                    <Spinner v-if="isActionRunning(node.id, 'restart')" :size="11" />
                    <Icon v-else name="rotate" :size="12" />
                    <span>重启</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    :disabled="isActionRunning(node.id)"
                    title="拉取最新 Docker 镜像"
                    @click="triggerNodeAction(node, 'pull')"
                  >
                    <Spinner v-if="isActionRunning(node.id, 'pull')" :size="11" />
                    <Icon v-else name="download" :size="12" />
                    <span>拉镜像</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    title="查看容器实时日志"
                    @click="logsNode = node"
                  >
                    <Icon name="terminal" :size="12" />
                    <span>日志</span>
                  </Button>
                </template>

                <Button
                  variant="ghost"
                  size="xs"
                  :disabled="checkingIpId !== null"
                  title="通过节点 ProxyURL 探测出口 IP 并检测碰撞"
                  @click="checkNodeExitIPAction(node)"
                >
                  <Spinner v-if="checkingIpId === node.id" :size="11" />
                  <Icon v-else name="globe" :size="12" />
                  <span>测IP</span>
                </Button>

                <Button
                  variant="ghost"
                  size="xs"
                  :disabled="rotatingFpId !== null"
                  title="为该容器轮换全新隔离设备指纹（Qimei36 / MachineID / Hostname / MAC）"
                  @click="rotateNodeFingerprintAction(node)"
                >
                  <Spinner v-if="rotatingFpId === node.id" :size="11" />
                  <Icon v-else name="sparkles" :size="12" />
                  <span>换指纹</span>
                </Button>

                <Button
                  variant="secondary"
                  size="xs"
                  :disabled="isActionRunning(node.id)"
                  title="探测节点管理端与 /v1/models 连通性"
                  @click="triggerNodeAction(node, 'check')"
                >
                  <Spinner v-if="isActionRunning(node.id, 'check')" :size="11" />
                  <Icon v-else name="zap" :size="12" />
                  <span>检测</span>
                </Button>

                <Button variant="ghost" size="xs" icon title="编辑节点" @click="openEditNode(node)">
                  <Icon name="edit" :size="13" />
                </Button>

                <Button
                  variant="ghost"
                  size="xs"
                  icon
                  class="hover:bg-danger/10 hover:text-danger"
                  title="删除节点"
                  @click="askDeleteNode(node)"
                >
                  <Icon name="trash" :size="13" />
                </Button>
              </div>
            </td>
          </tr>
        </template>
      </DataTable>
    </Card>

    <!-- 板块 4：选中节点的 OAuth 在线授权与 .json 登录文件池 -->
    <template v-if="selectedNode">
      <div class="grid gap-4 lg:grid-cols-12">
        <!-- 左栏：一键在线 OAuth 授权台 -->
        <Card
          class="lg:col-span-4"
          :title="`在线 OAuth 登录 · ${selectedNode.name}`"
          subtitle="通过容器内置 OAuth 端点生成登录链接，完成后自动落盘为原生凭证"
        >
          <div class="space-y-3.5">
            <Field label="授权提供商 (Provider)">
              <Select v-model="oauthProvider" size="sm" :options="providerOptions" />
            </Field>

            <Field label="自定义回调地址 (可选)" hint="留空使用容器默认回调地址">
              <Input
                v-model="oauthRedirectUri"
                size="sm"
                mono
                placeholder="http://localhost:8317/oauth/callback"
              />
            </Field>

            <Button
              variant="primary"
              size="sm"
              block
              :loading="oauthLoading"
              @click="generateOAuthLink"
            >
              <template #leading><Icon name="link" :size="13" /></template>
              生成 OAuth 授权链接
            </Button>

            <div v-if="oauthResult" class="panel-inset space-y-2.5 p-3">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="flex items-center gap-1.5">
                  <Badge tone="success" dot>授权链接已就绪</Badge>
                  <Badge v-if="oauthResult.mode" tone="info" mono>{{ oauthResult.mode }}</Badge>
                </div>
                <span v-if="oauthResult.state" class="font-mono text-2xs text-subtle">
                  state: {{ oauthResult.state.slice(0, 12) }}
                </span>
              </div>

              <p class="break-all rounded border border-line bg-surface p-2 font-mono text-2xs leading-relaxed text-fg">
                {{ oauthResult.auth_url || oauthResult.url }}
              </p>

              <p class="text-2xs leading-relaxed text-subtle">
                {{
                  oauthResult.instructions ||
                  '点击「新窗口打开授权」完成登录，随后将浏览器跳转后的完整回调 URL / Code / Token JSON 粘贴到下方完成入池。'
                }}
              </p>

              <div class="flex flex-wrap gap-2">
                <Button variant="secondary" size="xs" @click="copyOAuthLink">
                  <template #leading><Icon name="copy" :size="12" /></template>
                  复制链接
                </Button>
                <Button variant="primary" size="xs" @click="openOAuthWindow">
                  <template #leading><Icon name="external" :size="12" /></template>
                  新窗口打开授权
                </Button>
              </div>
            </div>

            <div class="space-y-2 border-t border-line pt-3">
              <Field
                label="提交回调 URL / 授权码 Code / Token JSON"
                hint="支持直接粘贴浏览器回调链接（含 ?code=...）、授权码或 OAuth JSON，提交后自动生成原生凭证并入池"
              >
                <Input
                  v-model="oauthCallbackInput"
                  size="sm"
                  mono
                  placeholder="http://localhost:8080/api/oauth/callback?code=... 或粘贴 JSON"
                />
              </Field>
              <Button
                variant="secondary"
                size="sm"
                block
                :loading="oauthCompleting"
                @click="submitOAuthCallback"
              >
                <template #leading><Icon name="check" :size="13" /></template>
                提交回调并自动落盘入池
              </Button>
            </div>
          </div>
        </Card>

        <!-- 右栏：.json 凭证文件列表与一键同步 -->
        <Card
          class="lg:col-span-8"
          :title="`OAuth 登录凭证池 · ${selectedNode.name}`"
          :subtitle="`共 ${authFiles.length} 份凭证文件${selectedNode.auth_dir ? ` · 目录 ${selectedNode.auth_dir}` : ''}`"
          body-class="p-0"
        >
          <template #actions>
            <Button
              variant="ghost"
              size="sm"
              :loading="authLoading"
              @click="loadAuthFiles(selectedNode.id)"
            >
              <template #leading><Icon name="refresh" :size="13" /></template>
              刷新凭证列表
            </Button>
            <Button variant="secondary" size="sm" @click="uploadModalOpen = true">
              <template #leading><Icon name="upload" :size="13" /></template>
              全格式上传 / 转换
            </Button>
            <Button
              variant="primary"
              size="sm"
              :disabled="authFiles.length === 0"
              @click="openSyncModal"
            >
              <template #leading><Icon name="rotate" :size="13" /></template>
              一键同步至调度账号池
            </Button>
          </template>

          <p
            v-if="authError"
            class="m-3 flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
          >
            <Icon name="alertCircle" :size="14" />
            <span>{{ authError }}</span>
          </p>

          <DataTable
            :columns="7"
            :loading="authLoading"
            :empty="authFiles.length === 0"
            :skeleton-rows="4"
            min-width="820px"
            hoverable
            class="border-0 rounded-none"
          >
            <template #head>
              <th class="th">文件名</th>
              <th class="th">邮箱 / Project ID</th>
              <th class="th">Provider / 来源</th>
              <th class="th">过期时间与状态</th>
              <th class="th text-right">优先级</th>
              <th class="th">关联账号 ID</th>
              <th class="th text-right">操作</th>
            </template>

            <template #empty>
              <EmptyState
                icon="fileText"
                title="当前节点尚无登录凭证文件"
                description="可通过左侧「在线 OAuth 登录」生成，或点击「全格式上传 / 转换」批量导入任意格式账号文件。"
              >
                <Button variant="primary" size="sm" @click="uploadModalOpen = true">
                  <template #leading><Icon name="upload" :size="13" /></template>
                  全格式导入登录凭证
                </Button>
              </EmptyState>
            </template>

            <template #body>
              <tr v-for="file in authFiles" :key="file.filename">
                <td class="td max-w-[13rem]">
                  <div class="truncate font-mono text-xs font-medium text-fg" :title="file.filename">
                    {{ file.filename }}
                  </div>
                  <div class="tnum text-2xs text-subtle">{{ formatBytes(file.size_bytes) }}</div>
                </td>

                <td class="td max-w-[14rem]">
                  <div class="truncate text-xs text-fg" :title="file.email">{{ file.email || '—' }}</div>
                  <div v-if="file.project_id" class="truncate font-mono text-2xs text-subtle">
                    {{ file.project_id }}
                  </div>
                </td>

                <td class="td">
                  <div class="flex flex-wrap items-center gap-1">
                    <Badge tone="success" mono>{{ file.provider || 'antigravity' }}</Badge>
                    <Badge tone="neutral">
                      {{ file.source === 'remote_api' ? '远程热加载' : '本地卷' }}
                    </Badge>
                  </div>
                </td>

                <td class="td">
                  <div class="flex flex-col items-start gap-0.5">
                    <Badge
                      v-if="file.disabled"
                      tone="neutral"
                      dot
                    >
                      已禁用
                    </Badge>
                    <Badge
                      v-else-if="file.expired || (file.expires_at && isExpired(file.expires_at))"
                      tone="warning"
                      dot
                    >
                      Token 待自动续期
                    </Badge>
                    <Badge v-else tone="success" dot>有效</Badge>
                    <span v-if="file.expires_at" class="text-2xs text-subtle" :title="formatDateTime(file.expires_at)">
                      {{ formatExpiry(file.expires_at) }}
                    </span>
                  </div>
                </td>

                <td class="td tnum text-right font-mono text-xs text-fg">
                  P{{ file.priority ?? 0 }}
                  <div v-if="(file.excluded_models ?? []).length" class="text-2xs text-subtle">
                    排除 {{ (file.excluded_models ?? []).length }} 模型
                  </div>
                </td>

                <td class="td">
                  <Badge v-if="findLinkedAccountId(file)" tone="accent" mono>
                    #{{ findLinkedAccountId(file) }}
                  </Badge>
                  <span v-else class="text-2xs text-subtle">待同步</span>
                </td>

                <td class="td text-right">
                  <Button
                    variant="ghost"
                    size="xs"
                    icon
                    class="hover:bg-danger/10 hover:text-danger"
                    title="删除该凭证文件"
                    :disabled="deletingFilename !== null"
                    @click="handleDeleteAuthFile(file)"
                  >
                    <Spinner v-if="deletingFilename === file.filename" :size="11" />
                    <Icon v-else name="trash" :size="13" />
                  </Button>
                </td>
              </tr>
            </template>
          </DataTable>
        </Card>
      </div>
    </template>

    <!-- 弹窗组件群 -->
    <NodeFormModal
      v-model:open="nodeFormOpen"
      :node="editingNode"
      :docker-available="Boolean(env?.docker_available)"
      :default-image="env?.default_image || 'routerforme/cliproxyapi:latest'"
      @saved="onNodeSaved"
    />

    <NodeLogsModal
      :open="logsNode !== null"
      :node="logsNode"
      @update:open="logsNode = null"
    />

    <AuthFileUploadModal
      v-model:open="uploadModalOpen"
      :node="selectedNode"
      :groups="groups"
      @uploaded="onAccountsConverted"
    />

    <!-- 一键同步凭证至调度账号池弹窗 -->
    <Modal
      :open="syncModalOpen"
      :title="`同步凭证至调度账号池 · ${selectedNode?.name ?? ''}`"
      subtitle="将节点下的可用 OAuth 凭证注册或更新为网关 AntiGravity 调度账号"
      size="md"
      :persistent="syncing"
      @update:open="syncModalOpen = $event"
    >
      <div class="space-y-4">
        <div class="panel-inset space-y-2 p-3">
          <p class="text-xs font-medium text-fg">选择目标 AntiGravity 分组</p>
          <div v-if="agyGroups.length > 0" class="flex flex-wrap gap-3">
            <Checkbox
              v-for="g in agyGroups"
              :key="g.id"
              :model-value="syncGroupIds.includes(g.id)"
              :label="`${g.name} (${g.rate_multiplier}x)`"
              @update:model-value="toggleSyncGroup(g.id, $event)"
            />
          </div>
          <p v-else class="text-2xs text-subtle">
            当前尚无 `antigravity` 分组，同步时系统将自动创建并绑定默认分组。
          </p>
        </div>

        <div class="grid grid-cols-3 gap-3">
          <Field label="单号并发">
            <Input v-model="syncConcurrency" type="number" min="1" size="sm" />
          </Field>
          <Field label="调度优先级">
            <Input v-model="syncPriority" type="number" min="0" size="sm" />
          </Field>
          <Field label="计费倍率">
            <Input v-model="syncMultiplier" type="number" min="0.01" step="0.1" size="sm" />
          </Field>
        </div>
      </div>

      <template #footer>
        <Button variant="ghost" size="md" :disabled="syncing" @click="syncModalOpen = false">
          取消
        </Button>
        <Button variant="primary" size="md" :loading="syncing" @click="runSyncAccounts">
          立即同步 ({{ authFiles.length }} 份凭证)
        </Button>
      </template>
    </Modal>

    <!-- 删除节点确认弹窗 -->
    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除 AntiGravity 节点"
      :message="`确认删除节点「${deleteTarget?.name ?? ''}」吗？${deleteTarget?.mode === 'local_docker' && removeDockerContainer ? '将同时停止并移除宿主机同名 Docker 容器（本地凭证文件仍安全保留）。' : '仅移除面板记录。'}`"
      confirm-text="确认删除"
      tone="danger"
      :loading="deletingNode"
      @update:open="deleteTarget = null"
      @confirm="confirmDeleteNode"
    />
  </div>
</template>
