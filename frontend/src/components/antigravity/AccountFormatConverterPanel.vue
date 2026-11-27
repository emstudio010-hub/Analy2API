<script setup lang="ts">
import { computed, ref } from 'vue'

import { toMessage } from '@/api/client'
import {
  convertAntigravityAccounts,
  convertAntigravityAccountsMultipart,
} from '@/api/endpoints'
import type {
  AntigravityConvertAccountsResponse,
  ContainerNode,
  ConvertedAGYAccount,
  Group,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import { formatBytes, formatExpiry } from '@/utils/format'

const props = defineProps<{
  nodes: ContainerNode[]
  selectedNode: ContainerNode | null
  groups: Group[]
}>()

const emit = defineEmits<{
  (event: 'imported'): void
}>()

const toast = useToastStore()

const SUPPORTED_FORMAT_CHIPS = [
  { key: 'gcloud_adc', label: 'Google ADC', hint: 'application_default_credentials.json (authorized_user)' },
  { key: 'gemini_cli', label: 'Gemini CLI oauth_creds', hint: '~/.gemini/oauth_creds.json & google_accounts.json' },
  { key: 'cpa_export', label: 'CLIProxyAPI / sub2api', hint: 'CPA / sub2api / OneAPI 导出的嵌套 accounts JSON' },
  { key: 'agy_native', label: 'AGY Native', hint: 'AntiGravity IDE / agy CLI 原生 auth.json 凭证' },
  { key: 'json_array', label: 'JSON Array / JSONL', hint: '异构多账号 JSON 数组或每行一条 NDJSON (.jsonl)' },
  { key: 'csv_tsv', label: 'CSV', hint: 'email,refresh_token,client_id,client_secret,project_id,proxy' },
  { key: 'delimited_text', label: 'email----refresh_token', hint: '支持 email----pwd----1//...、email:1//...、label|1//...' },
  { key: 'bare_token', label: '1// 裸 Token', hint: '每行一条 Google OAuth 1//... Refresh Token' },
  { key: 'zip_archive', label: 'Base64 / ZIP', hint: 'Base64 编码凭证串或含多格式文件的 .zip 压缩包' },
] as const

const SAMPLE_MULTI_FORMAT_TEXT = `# 支持同批混贴任意格式（自动嗅探并转为 agy CLI 标准文件结构）：
{"type":"authorized_user","client_id":"1071006060591-demo.apps.googleusercontent.com","client_secret":"GOCSPX-demo","refresh_token":"1//0g_demo_gcloud_adc_token_001","quota_project_id":"agy-cloud-01"}
dev-team-02@gmail.com----1//0g_demo_delimited_refresh_token_002
1//0g_demo_bare_refresh_token_003`

const fileInput = ref<HTMLInputElement | null>(null)
const isDragging = ref(false)
const uploadedFiles = ref<File[]>([])
const rawText = ref('')
const targetNodeId = ref<string>('')
const selectedGroupIds = ref<number[]>([])
const defaultProxyUrl = ref('')
const deduplicate = ref(true)
const autoProvision = ref(true)

const parsing = ref(false)
const importing = ref(false)
const convertError = ref('')
const convertResult = ref<AntigravityConvertAccountsResponse | null>(null)

// 预览当前选中账号的原生 agy CLI 生成文件
const inspectingIndex = ref<number>(0)
const activeCliFileTab = ref<string>('')

const agyGroups = computed(() => props.groups.filter((g) => g.platform === 'antigravity'))

// 初始化默认选中第一个 antigravity 分组
const effectiveGroupIds = computed(() => {
  if (selectedGroupIds.value.length > 0) return selectedGroupIds.value
  return agyGroups.value[0] ? [agyGroups.value[0].id] : []
})

const nodeSelectOptions = computed(() => [
  {
    label: props.selectedNode
      ? `当前选中节点：${props.selectedNode.name} (#${props.selectedNode.id})`
      : '自动分配隔离容器 / 写入网关 AGY 号池',
    value: props.selectedNode ? String(props.selectedNode.id) : '0',
  },
  ...(props.selectedNode
    ? [{ label: '不指定固定节点（按网关策略自动分配隔离目录）', value: '0' }]
    : []),
  ...props.nodes
    .filter((n) => n.id !== props.selectedNode?.id)
    .map((n) => ({
      label: `节点：${n.name} (${n.mode === 'local_docker' ? 'Docker' : 'Remote'} · #${n.id})`,
      value: String(n.id),
    })),
])

const resolvedTargetNodeId = computed<number | undefined>(() => {
  if (targetNodeId.value !== '') {
    const n = Number(targetNodeId.value)
    return Number.isFinite(n) && n > 0 ? n : undefined
  }
  return props.selectedNode?.id
})

const hasAnyInput = computed(
  () => uploadedFiles.value.length > 0 || rawText.value.trim().length > 0,
)

const previewAccounts = computed<ConvertedAGYAccount[]>(
  () => convertResult.value?.accounts ?? convertResult.value?.items ?? [],
)

const validAccountsCount = computed(() => {
  if (!convertResult.value) return 0
  return (
    convertResult.value.valid_count ??
    convertResult.value.summary?.valid_count ??
    previewAccounts.value.filter((a) => a.valid && !a.duplicate).length
  )
})

const inspectedAccount = computed<ConvertedAGYAccount | null>(() => {
  if (previewAccounts.value.length === 0) return null
  return (
    previewAccounts.value.find((a) => a.index === inspectingIndex.value) ??
    previewAccounts.value.find((a) => a.valid) ??
    previewAccounts.value[0] ??
    null
  )
})

const inspectedCliFileTabs = computed(() => {
  const files = inspectedAccount.value?.agy_cli_files ?? {}
  return Object.keys(files).map((path) => ({
    value: path,
    label: path,
  }))
})

const currentCliFilePath = computed(() => {
  const tabs = inspectedCliFileTabs.value
  if (tabs.length === 0) return ''
  if (activeCliFileTab.value && tabs.some((t) => t.value === activeCliFileTab.value)) {
    return activeCliFileTab.value
  }
  return tabs[0]!.value
})

const currentCliFileContent = computed(() => {
  const path = currentCliFilePath.value
  if (!path || !inspectedAccount.value?.agy_cli_files) return ''
  return inspectedAccount.value.agy_cli_files[path] ?? ''
})

function formatBadgeMeta(
  format: string | undefined,
  innerFormat?: string,
): { label: string; tone: 'accent' | 'success' | 'info' | 'warning' | 'neutral' } {
  const key = (format || '').toLowerCase()
  const inner = innerFormat && innerFormat !== format ? ` -> ${innerFormat}` : ''
  switch (key) {
    case 'gcloud_adc':
      return { label: `Google ADC${inner}`, tone: 'info' }
    case 'gemini_cli':
      return { label: `Gemini CLI${inner}`, tone: 'accent' }
    case 'agy_native':
      return { label: `AGY Native${inner}`, tone: 'success' }
    case 'cpa_export':
      return { label: `CPA / sub2api${inner}`, tone: 'warning' }
    case 'json_array':
      return { label: `JSON Array${inner}`, tone: 'accent' }
    case 'ndjson':
      return { label: `JSONL${inner}`, tone: 'info' }
    case 'csv_tsv':
      return { label: `CSV/TSV${inner}`, tone: 'neutral' }
    case 'delimited_text':
      return { label: `分隔文本/裸Token${inner}`, tone: 'warning' }
    case 'base64_blob':
      return { label: `Base64${inner}`, tone: 'info' }
    case 'zip_archive':
      return { label: `ZIP 包${inner}`, tone: 'success' }
    default:
      return { label: format || '自动识别', tone: 'neutral' }
  }
}

function addFiles(incoming: File[]): void {
  if (incoming.length === 0) return
  const existingKeys = new Set(uploadedFiles.value.map((f) => `${f.name}:${f.size}:${f.lastModified}`))
  const next = [...uploadedFiles.value]
  for (const file of incoming) {
    const k = `${file.name}:${file.size}:${file.lastModified}`
    if (!existingKeys.has(k)) {
      existingKeys.add(k)
      next.push(file)
    }
  }
  uploadedFiles.value = next
}

function onFileInputChange(event: Event): void {
  const input = event.target as HTMLInputElement
  addFiles(Array.from(input.files ?? []))
  input.value = ''
}

function onDrop(event: DragEvent): void {
  isDragging.value = false
  const dropped = Array.from(event.dataTransfer?.files ?? [])
  addFiles(dropped)
}

function removeFile(idx: number): void {
  uploadedFiles.value.splice(idx, 1)
}

function clearAllInputs(): void {
  uploadedFiles.value = []
  rawText.value = ''
  convertResult.value = null
  convertError.value = ''
}

function fillSampleText(): void {
  rawText.value = SAMPLE_MULTI_FORMAT_TEXT
}

function toggleGroup(id: number, checked: boolean): void {
  const base = selectedGroupIds.value.length > 0 ? [...selectedGroupIds.value] : [...effectiveGroupIds.value]
  if (checked) {
    if (!base.includes(id)) base.push(id)
    selectedGroupIds.value = base
  } else {
    selectedGroupIds.value = base.filter((g) => g !== id)
  }
}

async function runConvert(autoImport: boolean): Promise<void> {
  if (!hasAnyInput.value || parsing.value || importing.value) return
  if (autoImport) importing.value = true
  else parsing.value = true
  convertError.value = ''

  try {
    let res: AntigravityConvertAccountsResponse
    const nodeId = resolvedTargetNodeId.value
    const groupIds = effectiveGroupIds.value

    if (uploadedFiles.value.length > 0) {
      const fd = new FormData()
      for (const file of uploadedFiles.value) {
        fd.append('files', file)
      }
      if (rawText.value.trim()) {
        fd.append('raw_text', rawText.value.trim())
      }
      fd.append('auto_import', autoImport ? 'true' : 'false')
      fd.append('deduplicate', deduplicate.value ? 'true' : 'false')
      fd.append('auto_provision', autoProvision.value ? 'true' : 'false')
      if (nodeId) fd.append('node_id', String(nodeId))
      if (groupIds.length > 0) fd.append('group_ids', JSON.stringify(groupIds))
      if (defaultProxyUrl.value.trim()) fd.append('proxy_url', defaultProxyUrl.value.trim())
      res = await convertAntigravityAccountsMultipart(fd)
    } else {
      res = await convertAntigravityAccounts({
        raw_text: rawText.value.trim(),
        auto_import: autoImport,
        deduplicate: deduplicate.value,
        auto_provision: autoProvision.value,
        node_id: nodeId,
        group_ids: groupIds.length > 0 ? groupIds : undefined,
        proxy_url: defaultProxyUrl.value.trim() || undefined,
      })
    }

    convertResult.value = res
    const firstValid = (res.accounts ?? res.items ?? []).find((a) => a.valid) ?? (res.accounts ?? res.items ?? [])[0]
    if (firstValid) {
      inspectingIndex.value = firstValid.index
      const firstFileKey = Object.keys(firstValid.agy_cli_files ?? {})[0]
      if (firstFileKey) activeCliFileTab.value = firstFileKey
    }

    if (autoImport) {
      const imp = res.import_result
      toast.success(
        '已转换并一键导入 AGY 账号池',
        imp
          ? `导入 ${imp.imported} 个（新建 ${imp.created} · 更新 ${imp.updated}${imp.failed ? ` · 失败 ${imp.failed}` : ''}）`
          : `共处理 ${res.valid_count} 个有效账号`,
      )
      emit('imported')
    } else {
      toast.success(
        '多格式凭证自动识别与转换完成',
        `识别 ${res.total} 条 · 有效 ${res.valid_count} 条${res.duplicate_count ? ` · 去重 ${res.duplicate_count} 条` : ''}`,
      )
    }
  } catch (err) {
    convertError.value = toMessage(err, autoImport ? '导入 AGY 账号失败' : '解析转换账号文件失败')
    toast.error(autoImport ? '导入失败' : '解析失败', convertError.value)
  } finally {
    parsing.value = false
    importing.value = false
  }
}

function downloadBundle(): void {
  const res = convertResult.value
  if (!res) return

  if (res.bundle_zip_base64) {
    try {
      const binary = atob(res.bundle_zip_base64)
      const bytes = new Uint8Array(binary.length)
      for (let i = 0; i < binary.length; i += 1) {
        bytes[i] = binary.charCodeAt(i)
      }
      const blob = new Blob([bytes], { type: 'application/zip' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = res.bundle_filename || `agy-cli-credentials-${new Date().toISOString().slice(0, 10)}.zip`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      toast.success('已下载 AGY CLI 标准凭证包 (.zip)')
      return
    } catch {
      /* 若 Base64 解码异常，降级下载 JSON 凭证包 */
    }
  }

  const jsonPayload =
    typeof res.bundle_json === 'string'
      ? res.bundle_json
      : JSON.stringify(
          res.bundle_json ?? {
            exported_at: new Date().toISOString(),
            count: previewAccounts.value.filter((a) => a.valid).length,
            accounts: previewAccounts.value.filter((a) => a.valid),
          },
          null,
          2,
        )
  const blob = new Blob([jsonPayload], { type: 'application/json;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `agy-cli-credentials-${new Date().toISOString().slice(0, 10)}.json`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  toast.success('已下载转换后的 AGY CLI 凭证包 (.json)')
}

function downloadSingleCliFile(acc: ConvertedAGYAccount, relPath: string): void {
  const content = acc.agy_cli_files?.[relPath]
  if (!content) return
  const safeBase = relPath.split('/').pop() || 'oauth_creds.json'
  const prefix = (acc.email || acc.name || `acc-${acc.index}`).replace(/[^a-zA-Z0-9._@-]+/g, '_')
  const blob = new Blob([content], { type: 'application/json;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${prefix}-${safeBase}`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

function inspectAccountFiles(acc: ConvertedAGYAccount): void {
  inspectingIndex.value = acc.index
  const firstKey = Object.keys(acc.agy_cli_files ?? {})[0]
  if (firstKey) activeCliFileTab.value = firstKey
}
</script>

<template>
  <Card
    title="多账号全格式自动转 AGY CLI 工作台"
    subtitle="拖入任意主流导出文件或粘贴混排凭证，自动嗅探格式并转换为 agy CLI 原生可识别目录结构（.gemini/oauth_creds.json · google_accounts.json · auth.json）"
    body-class="p-4 space-y-4"
  >
    <template #actions>
      <div class="flex flex-wrap items-center gap-2">
        <Button variant="ghost" size="sm" @click="fillSampleText">
          <template #leading><Icon name="sparkles" :size="13" /></template>
          填入多格式示例
        </Button>
        <Button
          v-if="hasAnyInput || convertResult"
          variant="ghost"
          size="sm"
          @click="clearAllInputs"
        >
          清空工作台
        </Button>
      </div>
    </template>

    <!-- 支持的 9 大输入格式 Chips 条 -->
    <div class="flex flex-wrap items-center gap-1.5">
      <span class="mr-1 text-2xs font-medium text-subtle">自动识别输入源：</span>
      <span
        v-for="chip in SUPPORTED_FORMAT_CHIPS"
        :key="chip.key"
        :title="chip.hint"
        class="inline-flex items-center gap-1 rounded border border-line bg-raised/70 px-2 py-0.5 font-mono text-2xs text-muted transition hover:border-line-strong hover:text-fg"
      >
        <Icon name="check" :size="10" class="text-success" />
        {{ chip.label }}
      </span>
    </div>

    <!-- 双栏输入区：左拖拽多文件上传 + 右多行混排粘贴 -->
    <div class="grid gap-4 lg:grid-cols-12">
      <!-- 左栏：多文件拖拽上传器 -->
      <div class="flex flex-col gap-2.5 lg:col-span-5">
        <input
          ref="fileInput"
          type="file"
          multiple
          accept=".json,.jsonl,.csv,.tsv,.txt,.zip,application/json,application/zip,text/plain,text/csv"
          class="hidden"
          @change="onFileInputChange"
        />

        <div
          role="button"
          tabindex="0"
          class="flex min-h-[10.5rem] flex-1 cursor-pointer flex-col items-center justify-center gap-2 rounded-md border border-dashed px-4 py-5 text-center transition"
          :class="
            isDragging
              ? 'border-accent bg-accent/10'
              : 'border-line-strong bg-raised/40 hover:border-accent hover:bg-accent/[0.04]'
          "
          @click="fileInput?.click()"
          @keydown.enter.prevent="fileInput?.click()"
          @keydown.space.prevent="fileInput?.click()"
          @dragover.prevent="isDragging = true"
          @dragleave.prevent="isDragging = false"
          @drop.prevent="onDrop"
        >
          <div class="flex h-9 w-9 items-center justify-center rounded-md border border-line bg-surface text-accent">
            <Icon name="upload" :size="18" />
          </div>
          <div class="space-y-0.5">
            <p class="text-xs font-semibold text-fg">
              拖拽多个账号文件至此，或点击选择文件
            </p>
            <p class="text-2xs text-subtle">
              支持 <code class="font-mono text-muted">.json</code> ·
              <code class="font-mono text-muted">.jsonl</code> ·
              <code class="font-mono text-muted">.csv</code> ·
              <code class="font-mono text-muted">.txt</code> ·
              <code class="font-mono text-muted">.zip</code> 混合批量投喂
            </p>
          </div>
        </div>

        <div v-if="uploadedFiles.length > 0" class="scroll-thin max-h-36 space-y-1.5 overflow-y-auto pr-1">
          <div
            v-for="(f, idx) in uploadedFiles"
            :key="`${f.name}-${idx}`"
            class="flex items-center justify-between gap-2 rounded border border-line bg-raised/60 px-2.5 py-1.5 text-2xs"
          >
            <div class="flex min-w-0 items-center gap-1.5">
              <Icon name="fileText" :size="12" class="shrink-0 text-accent" />
              <span class="truncate font-mono font-medium text-fg" :title="f.name">{{ f.name }}</span>
              <span class="tnum shrink-0 text-subtle">{{ formatBytes(f.size) }}</span>
            </div>
            <button
              type="button"
              class="rounded p-0.5 text-subtle transition hover:bg-overlay hover:text-danger"
              title="移除文件"
              @click="removeFile(idx)"
            >
              <Icon name="close" :size="12" />
            </button>
          </div>
        </div>
      </div>

      <!-- 右栏：多行原始文本混排粘贴框 -->
      <div class="lg:col-span-7">
        <Field
          label="多格式原始凭证文本粘贴框（可与左侧上传文件同时解析）"
          hint="支持粘贴 ADC JSON、oauth_creds.json、CPA/sub2api 导出 JSON、CSV、email----refresh_token、1// 裸 Token 或 Base64"
        >
          <Textarea
            v-model="rawText"
            :rows="6"
            mono
            placeholder='粘贴任意格式凭证，例如：&#10;1) {"type":"authorized_user","client_id":"...","refresh_token":"1//..."}&#10;2) user@gmail.com----1//0g_your_refresh_token&#10;3) 1//0g_bare_refresh_token_line'
          />
        </Field>
      </div>
    </div>

    <!-- 入池目标配置与操作主按钮栏 -->
    <div class="panel-inset flex flex-col gap-3 p-3.5">
      <div class="grid gap-3 md:grid-cols-12">
        <div class="md:col-span-4">
          <Field label="导入目标节点 / 容器（可选）">
            <Select
              :model-value="targetNodeId || (selectedNode ? String(selectedNode.id) : '0')"
              size="sm"
              :options="nodeSelectOptions"
              @update:model-value="targetNodeId = $event"
            />
          </Field>
        </div>

        <div class="md:col-span-4">
          <Field label="默认出口代理 (proxy_url · 可选)">
            <Input
              v-model="defaultProxyUrl"
              size="sm"
              mono
              placeholder="留空保留原账号代理或走 IP 池"
            />
          </Field>
        </div>

        <div class="flex flex-wrap items-end gap-4 pb-1 md:col-span-4">
          <Switch v-model="deduplicate" size="sm" label="按邮箱/Token 自动去重" />
          <Switch v-model="autoProvision" size="sm" label="自动落盘隔离目录" />
        </div>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-3 border-t border-line/70 pt-3">
        <div class="flex flex-wrap items-center gap-2.5">
          <span class="text-2xs font-medium text-muted">绑定 AntiGravity 分组：</span>
          <template v-if="agyGroups.length > 0">
            <Checkbox
              v-for="g in agyGroups"
              :key="g.id"
              :model-value="effectiveGroupIds.includes(g.id)"
              :label="`${g.name} (${g.rate_multiplier}x)`"
              @update:model-value="toggleGroup(g.id, $event)"
            />
          </template>
          <span v-else class="text-2xs text-subtle">导入时自动归入默认 `antigravity` 分组</span>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <Button
            variant="secondary"
            size="sm"
            :loading="parsing"
            :disabled="!hasAnyInput || importing"
            @click="runConvert(false)"
          >
            <template #leading><Icon name="scan" :size="13" /></template>
            1. 先解析预览 / 自动识别
          </Button>

          <Button
            v-if="convertResult && validAccountsCount > 0"
            variant="secondary"
            size="sm"
            @click="downloadBundle"
          >
            <template #leading><Icon name="download" :size="13" /></template>
            下载 AGY CLI 标准凭证包
          </Button>

          <Button
            variant="primary"
            size="sm"
            :loading="importing"
            :disabled="!hasAnyInput || parsing"
            @click="runConvert(true)"
          >
            <template #leading><Icon name="upload" :size="13" /></template>
            一键转换并导入 AGY 号池/容器
          </Button>
        </div>
      </div>
    </div>

    <!-- 错误提示 -->
    <p
      v-if="convertError"
      class="flex items-start gap-2 rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
      <span>{{ convertError }}</span>
    </p>

    <!-- 解析预览结果与原生文件检视台 -->
    <div v-if="convertResult" class="space-y-3.5">
      <!-- 汇总统计条 -->
      <div class="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-raised/50 px-3.5 py-2.5">
        <div class="flex flex-wrap items-center gap-2">
          <Badge tone="accent" dot>共解析 {{ convertResult.total }} 条</Badge>
          <Badge tone="success">有效 {{ convertResult.valid_count }} 条</Badge>
          <Badge v-if="convertResult.duplicate_count > 0" tone="warning">
            重复去重 {{ convertResult.duplicate_count }} 条
          </Badge>
          <Badge v-if="convertResult.invalid_count > 0" tone="danger">
            格式错误 {{ convertResult.invalid_count }} 条
          </Badge>
          <template v-if="convertResult.detected_formats?.length">
            <span class="ml-1 text-2xs text-subtle">命中的源格式：</span>
            <Badge
              v-for="fmt in convertResult.detected_formats"
              :key="fmt"
              :tone="formatBadgeMeta(fmt).tone"
              mono
            >
              {{ formatBadgeMeta(fmt).label }}
            </Badge>
          </template>
        </div>

        <div v-if="convertResult.import_result" class="flex items-center gap-1.5 text-2xs text-success">
          <Icon name="checkCircle" :size="13" />
          <span>
            已入池：新增 {{ convertResult.import_result.created }} · 更新 {{ convertResult.import_result.updated }}
            <template v-if="convertResult.import_result.failed">
              · 失败 {{ convertResult.import_result.failed }}
            </template>
          </span>
        </div>
      </div>

      <!-- 逐账号识别预览表 -->
      <DataTable
        :columns="7"
        :empty="previewAccounts.length === 0"
        min-width="980px"
        hoverable
      >
        <template #head>
          <th class="th w-12">#</th>
          <th class="th">识别源格式 / 来源文件</th>
          <th class="th">邮箱 / 账号别名</th>
          <th class="th">Token 凭证状态</th>
          <th class="th">Project ID</th>
          <th class="th">校验与告警</th>
          <th class="th text-right">原生文件预览</th>
        </template>

        <template #empty>
          <EmptyState
            icon="fileText"
            title="未从输入内容中识别出账号条目"
            description="请检查上传的文件内容或粘贴的凭证格式是否包含 refresh_token / access_token。"
          />
        </template>

        <template #body>
          <tr
            v-for="acc in previewAccounts"
            :key="`${acc.index}-${acc.email}-${acc.auth_filename}`"
            class="cursor-pointer transition"
            :class="inspectedAccount?.index === acc.index ? 'bg-accent/[0.06]' : ''"
            @click="inspectAccountFiles(acc)"
          >
            <td class="td tnum font-mono text-2xs text-subtle">{{ acc.index + 1 }}</td>

            <td class="td">
              <div class="flex flex-col items-start gap-0.5">
                <Badge :tone="formatBadgeMeta(acc.source_format, acc.inner_format).tone" mono>
                  {{ formatBadgeMeta(acc.source_format, acc.inner_format).label }}
                </Badge>
                <span v-if="acc.source_file" class="max-w-[11rem] truncate font-mono text-2xs text-subtle" :title="acc.source_file">
                  {{ acc.source_file }}
                </span>
              </div>
            </td>

            <td class="td max-w-[14rem]">
              <div class="truncate font-medium text-fg" :title="acc.email || acc.name">
                {{ acc.email || acc.name || '（未命名账号）' }}
              </div>
              <div v-if="acc.auth_filename" class="truncate font-mono text-2xs text-subtle">
                {{ acc.auth_filename }}
              </div>
            </td>

            <td class="td">
              <div class="flex flex-wrap items-center gap-1">
                <Badge v-if="acc.has_refresh_token" tone="success" dot>Refresh</Badge>
                <Badge v-if="acc.has_access_token" tone="info">Access</Badge>
                <Badge v-if="!acc.has_refresh_token && !acc.has_access_token" tone="danger">缺失 Token</Badge>
              </div>
              <div v-if="acc.masked_refresh_token" class="mt-0.5 font-mono text-2xs text-subtle">
                {{ acc.masked_refresh_token }}
              </div>
              <div v-else-if="acc.expires_at" class="mt-0.5 text-2xs text-subtle">
                {{ formatExpiry(acc.expires_at) }}
              </div>
            </td>

            <td class="td font-mono text-2xs text-muted">
              {{ acc.project_id || '—' }}
            </td>

            <td class="td max-w-[15rem]">
              <div class="flex flex-col items-start gap-0.5">
                <Badge v-if="!acc.valid" tone="danger" dot>格式无效</Badge>
                <Badge v-else-if="acc.duplicate" tone="warning" dot>重复已跳过</Badge>
                <Badge v-else tone="success" dot>可转 AGY CLI</Badge>

                <span
                  v-if="acc.error"
                  class="max-w-[14rem] truncate text-2xs text-danger"
                  :title="acc.error"
                >
                  {{ acc.error }}
                </span>
                <span
                  v-for="(warn, wIdx) in (acc.warnings ?? []).slice(0, 2)"
                  :key="wIdx"
                  class="max-w-[14rem] truncate text-2xs text-warning"
                  :title="warn"
                >
                  {{ warn }}
                </span>
              </div>
            </td>

            <td class="td text-right" @click.stop>
              <Button
                v-if="acc.agy_cli_files && Object.keys(acc.agy_cli_files).length > 0"
                :variant="inspectedAccount?.index === acc.index ? 'subtle' : 'ghost'"
                size="xs"
                @click="inspectAccountFiles(acc)"
              >
                <template #leading><Icon name="code" :size="12" /></template>
                预览 ({{ Object.keys(acc.agy_cli_files).length }})
              </Button>
              <span v-else class="text-2xs text-subtle">—</span>
            </td>
          </tr>
        </template>
      </DataTable>

      <!-- 选中账号生成的 agy CLI 原生文件结构实时检视器 -->
      <div
        v-if="inspectedAccount && inspectedCliFileTabs.length > 0"
        class="panel-inset space-y-2.5 p-3.5"
      >
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex flex-wrap items-center gap-2">
            <Icon name="folder" :size="14" class="text-accent" />
            <span class="text-xs font-semibold text-fg">
              生成的 <code class="font-mono text-accent">agy</code> CLI 原生凭证文件预览 ·
              {{ inspectedAccount.email || inspectedAccount.name || `#${inspectedAccount.index + 1}` }}
            </span>
          </div>

          <div class="flex items-center gap-2">
            <Button
              variant="secondary"
              size="xs"
              @click="downloadSingleCliFile(inspectedAccount, currentCliFilePath)"
            >
              <template #leading><Icon name="download" :size="12" /></template>
              下载当前文件
            </Button>
            <Button variant="primary" size="xs" @click="downloadBundle">
              <template #leading><Icon name="download" :size="12" /></template>
              下载全量凭证包 (.zip)
            </Button>
          </div>
        </div>

        <SegmentedTabs
          :model-value="currentCliFilePath"
          :items="inspectedCliFileTabs"
          size="sm"
          @update:model-value="activeCliFileTab = $event"
        />

        <CodeBlock
          :title="currentCliFilePath"
          language="json"
          max-height="16rem"
          :code="currentCliFileContent"
        />
      </div>
    </div>
  </Card>
</template>
