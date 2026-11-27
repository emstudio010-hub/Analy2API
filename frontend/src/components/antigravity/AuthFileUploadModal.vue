<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { toMessage } from '@/api/client'
import {
  convertAntigravityAccounts,
  convertAntigravityAccountsMultipart,
  uploadAntigravityAuthFile,
} from '@/api/endpoints'
import type {
  AntigravityConvertAccountsResponse,
  ContainerNode,
  ConvertedAGYAccount,
  Group,
} from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import { formatBytes } from '@/utils/format'

const props = defineProps<{
  open: boolean
  node: ContainerNode | null
  groups: Group[]
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'uploaded'): void
}>()

const toast = useToastStore()

const FORMAT_CHIPS = [
  'Google ADC',
  'Gemini CLI oauth_creds',
  'CLIProxyAPI / sub2api',
  'AGY Native',
  'JSON Array / JSONL',
  'CSV',
  'email----refresh_token',
  '1// 裸 Token',
  'Base64 / ZIP',
]

const tab = ref('files')
const tabs = [
  { label: '多格式文件拖拽上传 (.json/.jsonl/.csv/.txt/.zip)', value: 'files', icon: 'upload' as const },
  { label: '多行混排文本 / JSON 粘贴', value: 'paste', icon: 'code' as const },
]

const isDragging = ref(false)
const pendingFiles = ref<File[]>([])
const pasteFilename = ref('')
const pasteContent = ref('')
const proxyUrl = ref('')
const syncAccount = ref(true)
const selectedGroupIds = ref<number[]>([])

const parsing = ref(false)
const submitting = ref(false)
const serverError = ref('')
const previewResult = ref<AntigravityConvertAccountsResponse | null>(null)
const inspectedIndex = ref(0)
const activeCliFile = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

const agyGroups = computed(() =>
  props.groups.filter((g) => g.platform === 'antigravity'),
)

watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    isDragging.value = false
    pendingFiles.value = []
    pasteFilename.value = ''
    pasteContent.value = ''
    proxyUrl.value = ''
    syncAccount.value = true
    selectedGroupIds.value = agyGroups.value[0] ? [agyGroups.value[0].id] : []
    serverError.value = ''
    previewResult.value = null
    inspectedIndex.value = 0
    activeCliFile.value = ''
  },
)

function addPickedFiles(files: File[]): void {
  if (files.length === 0) return
  const seen = new Set(pendingFiles.value.map((f) => `${f.name}:${f.size}:${f.lastModified}`))
  const next = [...pendingFiles.value]
  for (const f of files) {
    const k = `${f.name}:${f.size}:${f.lastModified}`
    if (!seen.has(k)) {
      seen.add(k)
      next.push(f)
    }
  }
  pendingFiles.value = next
  previewResult.value = null
}

function onFilesPicked(event: Event): void {
  const input = event.target as HTMLInputElement
  addPickedFiles(Array.from(input.files ?? []))
  input.value = ''
}

function onDrop(event: DragEvent): void {
  isDragging.value = false
  addPickedFiles(Array.from(event.dataTransfer?.files ?? []))
}

function removePending(index: number): void {
  pendingFiles.value.splice(index, 1)
  previewResult.value = null
}

function toggleGroup(id: number, checked: boolean): void {
  if (checked) {
    if (!selectedGroupIds.value.includes(id)) selectedGroupIds.value.push(id)
  } else {
    selectedGroupIds.value = selectedGroupIds.value.filter((item) => item !== id)
  }
}

const hasInput = computed(() => {
  if (tab.value === 'files') return pendingFiles.value.length > 0
  return pasteContent.value.trim().length > 0
})

const previewAccounts = computed<ConvertedAGYAccount[]>(
  () => previewResult.value?.accounts ?? previewResult.value?.items ?? [],
)

const inspectedAccount = computed<ConvertedAGYAccount | null>(() => {
  if (previewAccounts.value.length === 0) return null
  return (
    previewAccounts.value.find((a) => a.index === inspectedIndex.value) ??
    previewAccounts.value.find((a) => a.valid) ??
    previewAccounts.value[0] ??
    null
  )
})

const cliFileTabs = computed(() => {
  const files = inspectedAccount.value?.agy_cli_files ?? {}
  return Object.keys(files).map((k) => ({ label: k, value: k }))
})

const selectedCliFile = computed(() => {
  const list = cliFileTabs.value
  if (list.length === 0) return ''
  if (activeCliFile.value && list.some((t) => t.value === activeCliFile.value)) {
    return activeCliFile.value
  }
  return list[0]!.value
})

async function runConvertRequest(autoImport: boolean): Promise<AntigravityConvertAccountsResponse> {
  if (tab.value === 'files') {
    const fd = new FormData()
    for (const f of pendingFiles.value) {
      fd.append('files', f)
    }
    fd.append('auto_import', autoImport ? 'true' : 'false')
    fd.append('deduplicate', 'true')
    fd.append('auto_provision', 'true')
    if (props.node?.id) fd.append('node_id', String(props.node.id))
    if (syncAccount.value && selectedGroupIds.value.length > 0) {
      fd.append('group_ids', JSON.stringify(selectedGroupIds.value))
    }
    if (proxyUrl.value.trim()) fd.append('proxy_url', proxyUrl.value.trim())
    return convertAntigravityAccountsMultipart(fd)
  }

  return convertAntigravityAccounts({
    raw_text: pasteContent.value.trim(),
    filename: pasteFilename.value.trim() || undefined,
    auto_import: autoImport,
    deduplicate: true,
    auto_provision: true,
    node_id: props.node?.id,
    group_ids: syncAccount.value && selectedGroupIds.value.length > 0 ? selectedGroupIds.value : undefined,
    proxy_url: proxyUrl.value.trim() || undefined,
  })
}

async function parsePreview(): Promise<void> {
  if (!hasInput.value || parsing.value || submitting.value) return
  parsing.value = true
  serverError.value = ''
  try {
    const res = await runConvertRequest(false)
    previewResult.value = res
    const first = (res.accounts ?? res.items ?? []).find((a) => a.valid) ?? (res.accounts ?? res.items ?? [])[0]
    if (first) {
      inspectAccount(first)
    }
    toast.success(
      '解析识别完成',
      `共识别 ${res.total} 条 · 有效 ${res.valid_count} 条`,
    )
  } catch (err) {
    serverError.value = toMessage(err, '解析凭证文件失败')
  } finally {
    parsing.value = false
  }
}

function inspectAccount(acc: ConvertedAGYAccount): void {
  inspectedIndex.value = acc.index
  const firstFile = Object.keys(acc.agy_cli_files ?? {})[0]
  if (firstFile) activeCliFile.value = firstFile
}

function downloadConvertedBundle(): void {
  const res = previewResult.value
  if (!res) return
  if (res.bundle_zip_base64) {
    try {
      const bin = atob(res.bundle_zip_base64)
      const bytes = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i += 1) bytes[i] = bin.charCodeAt(i)
      const blob = new Blob([bytes], { type: 'application/zip' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = res.bundle_filename || 'agy-cli-credentials-bundle.zip'
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      toast.success('已下载转换后的 AGY CLI 凭证包 (.zip)')
      return
    } catch {
      /* fallback below */
    }
  }
  const payload = JSON.stringify(res.bundle_json ?? previewAccounts.value, null, 2)
  const blob = new Blob([payload], { type: 'application/json;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'agy-cli-credentials-bundle.json'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  toast.success('已下载转换后的 AGY CLI 凭证包 (.json)')
}

async function submit(): Promise<void> {
  if (!hasInput.value || submitting.value || parsing.value) return
  submitting.value = true
  serverError.value = ''

  try {
    const res = await runConvertRequest(true)
    previewResult.value = res
    const imp = res.import_result
    toast.success(
      '凭证已自动转换并导入',
      imp
        ? `新增 ${imp.created} 个 · 更新 ${imp.updated} 个账号`
        : `有效转换 ${res.valid_count} 个账号`,
    )
    emit('uploaded')
    emit('update:open', false)
  } catch (err) {
    // 若单份 JSON 在旧节点接口下回退兼容
    if (props.node && tab.value === 'paste' && pasteContent.value.trim().startsWith('{')) {
      try {
        const cleanName = (pasteFilename.value.trim() || 'antigravity-oauth.json').replace(/(\.json)?$/i, '.json')
        await uploadAntigravityAuthFile(props.node.id, {
          filename: cleanName,
          content: pasteContent.value.trim(),
          raw_json: pasteContent.value.trim(),
          sync_account: syncAccount.value,
          group_ids: syncAccount.value ? selectedGroupIds.value : undefined,
        })
        toast.success('凭证文件已保存并热加载', cleanName)
        emit('uploaded')
        emit('update:open', false)
        return
      } catch {
        /* 保留主转换报错信息 */
      }
    }
    serverError.value = toMessage(err, '转换并导入凭证失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Modal
    :open="open"
    :title="node ? `全格式凭证转 AGY CLI 并导入 · ${node.name}` : '全格式账号自动转 AGY CLI 导入'"
    subtitle="支持 Google ADC、Gemini CLI oauth_creds、CLIProxyAPI、CSV、email----refresh_token、1// 裸 Token 与 ZIP 包自动识别并生成原生 agy CLI 凭证"
    size="xl"
    :persistent="submitting || parsing"
    @update:open="emit('update:open', $event)"
  >
    <div class="space-y-4">
      <div class="flex flex-wrap items-center gap-1.5">
        <span
          v-for="chip in FORMAT_CHIPS"
          :key="chip"
          class="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-muted"
        >
          {{ chip }}
        </span>
      </div>

      <SegmentedTabs v-model="tab" :items="tabs" size="sm" block />

      <p
        v-if="serverError"
        class="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
      >
        <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
        <span>{{ serverError }}</span>
      </p>

      <!-- 模式 1：多文件拖拽上传 -->
      <div v-if="tab === 'files'" class="space-y-3">
        <input
          ref="fileInput"
          type="file"
          accept=".json,.jsonl,.csv,.tsv,.txt,.zip,application/json,application/zip,text/plain,text/csv"
          multiple
          class="hidden"
          @change="onFilesPicked"
        />
        <div
          role="button"
          tabindex="0"
          class="flex w-full cursor-pointer flex-col items-center justify-center gap-1.5 rounded-md border border-dashed px-4 py-6 text-center transition"
          :class="
            isDragging
              ? 'border-accent bg-accent/10'
              : 'border-line-strong bg-raised/40 hover:border-accent hover:bg-accent/5'
          "
          @click="fileInput?.click()"
          @keydown.enter.prevent="fileInput?.click()"
          @dragover.prevent="isDragging = true"
          @dragleave.prevent="isDragging = false"
          @drop.prevent="onDrop"
        >
          <Icon name="upload" :size="20" class="text-accent" />
          <span class="text-xs font-medium text-fg">
            拖拽或点击选择多个账号凭证文件（.json / .jsonl / .csv / .txt / .zip）
          </span>
          <span class="text-2xs text-subtle">
            自动解包并识别异构单号/多号结构，统一输出为 `.gemini/oauth_creds.json` + `google_accounts.json` + `auth.json`
          </span>
        </div>

        <div v-if="pendingFiles.length > 0" class="scroll-thin max-h-40 space-y-1.5 overflow-y-auto pr-1">
          <div
            v-for="(file, idx) in pendingFiles"
            :key="`${file.name}-${idx}`"
            class="flex items-center justify-between gap-2 rounded-md border border-line bg-raised/50 px-3 py-1.5 text-xs"
          >
            <div class="flex min-w-0 items-center gap-2">
              <Badge tone="accent" mono>{{ file.name.split('.').pop()?.toUpperCase() || 'FILE' }}</Badge>
              <span class="truncate font-mono text-fg">{{ file.name }}</span>
              <span class="tnum shrink-0 text-2xs text-subtle">{{ formatBytes(file.size) }}</span>
            </div>
            <button
              type="button"
              class="rounded p-1 text-subtle hover:bg-overlay hover:text-danger"
              @click="removePending(idx)"
            >
              <Icon name="trash" :size="13" />
            </button>
          </div>
        </div>
      </div>

      <!-- 模式 2：粘贴任意格式文本 -->
      <div v-else class="space-y-3">
        <div class="grid gap-3 sm:grid-cols-2">
          <Field label="来源文件名提示 (可选)" hint="留空将按内容自动嗅探单号/多号格式">
            <Input v-model="pasteFilename" size="sm" mono placeholder="例如：oauth_creds.json 或 accounts.csv" />
          </Field>
          <Field label="默认出口代理 (proxy_url · 可选)">
            <Input v-model="proxyUrl" size="sm" mono placeholder="http://user:pass@ip:port 或留空走 IP 池" />
          </Field>
        </div>

        <Field
          label="多格式凭证内容"
          required
          hint="支持粘贴单份/数组 JSON、NDJSON、CSV、email----refresh_token、1// 裸 Token 或 Base64"
        >
          <Textarea
            v-model="pasteContent"
            :rows="6"
            mono
            placeholder='{"type":"authorized_user","client_id":"...","refresh_token":"1//..."}&#10;或 user@gmail.com----1//0g_refresh_token'
          />
        </Field>
      </div>

      <!-- 解析预览区 -->
      <div v-if="previewResult" class="panel-inset space-y-3 p-3.5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex flex-wrap items-center gap-1.5">
            <Badge tone="accent" dot>解析 {{ previewResult.total }} 条</Badge>
            <Badge tone="success">有效 {{ previewResult.valid_count }} 条</Badge>
            <Badge v-if="previewResult.duplicate_count > 0" tone="warning">
              重复 {{ previewResult.duplicate_count }} 条
            </Badge>
            <Badge v-if="previewResult.invalid_count > 0" tone="danger">
              无效 {{ previewResult.invalid_count }} 条
            </Badge>
          </div>
          <Button
            v-if="previewResult.valid_count > 0"
            variant="secondary"
            size="xs"
            @click="downloadConvertedBundle"
          >
            <template #leading><Icon name="download" :size="12" /></template>
            下载 AGY CLI 标准凭证包
          </Button>
        </div>

        <div v-if="previewAccounts.length > 0" class="scroll-thin max-h-40 space-y-1 overflow-y-auto pr-1">
          <div
            v-for="acc in previewAccounts"
            :key="acc.index"
            class="flex cursor-pointer flex-wrap items-center justify-between gap-2 rounded border px-2.5 py-1.5 text-2xs transition"
            :class="
              inspectedAccount?.index === acc.index
                ? 'border-accent bg-accent/10'
                : 'border-line bg-surface hover:border-line-strong'
            "
            @click="inspectAccount(acc)"
          >
            <div class="flex min-w-0 items-center gap-2">
              <Badge :tone="acc.valid ? 'success' : 'danger'" mono>{{ acc.source_format }}</Badge>
              <span class="truncate font-medium text-fg">{{ acc.email || acc.name || `#${acc.index + 1}` }}</span>
              <span v-if="acc.project_id" class="font-mono text-subtle">{{ acc.project_id }}</span>
            </div>
            <div class="flex items-center gap-2">
              <span v-if="acc.masked_refresh_token" class="font-mono text-subtle">
                {{ acc.masked_refresh_token }}
              </span>
              <Badge v-if="acc.warnings?.length" tone="warning">{{ acc.warnings.length }} 提示</Badge>
            </div>
          </div>
        </div>

        <div v-if="inspectedAccount && cliFileTabs.length > 0" class="space-y-2 border-t border-line pt-2.5">
          <SegmentedTabs
            :model-value="selectedCliFile"
            :items="cliFileTabs"
            size="sm"
            @update:model-value="activeCliFile = $event"
          />
          <CodeBlock
            :title="selectedCliFile"
            language="json"
            max-height="11rem"
            :code="inspectedAccount.agy_cli_files?.[selectedCliFile] || ''"
          />
        </div>
      </div>

      <!-- 同步至网关调度池设置 -->
      <div class="panel-inset space-y-3 p-3.5">
        <Switch
          v-model="syncAccount"
          label="导入后同步至 AntiGravity 调度账号池"
          description="开启后将自动生成隔离目录原生 agy CLI 凭证并注册为可调度账号"
        />

        <div v-if="syncAccount" class="space-y-1.5 border-t border-line/70 pt-2.5">
          <p class="text-2xs font-medium text-muted">绑定 AntiGravity 路由分组（可选）：</p>
          <div v-if="agyGroups.length > 0" class="flex flex-wrap gap-3">
            <Checkbox
              v-for="g in agyGroups"
              :key="g.id"
              :model-value="selectedGroupIds.includes(g.id)"
              :label="`${g.name} (${g.rate_multiplier}x)`"
              @update:model-value="toggleGroup(g.id, $event)"
            />
          </div>
          <p v-else class="text-2xs text-subtle">
            当前暂无 `antigravity` 平台分组，同步时后端将自动创建或归入默认 AntiGravity 分组。
          </p>
        </div>
      </div>
    </div>

    <template #footer>
      <Button
        variant="ghost"
        size="md"
        :disabled="submitting || parsing"
        @click="emit('update:open', false)"
      >
        取消
      </Button>
      <Button
        variant="secondary"
        size="md"
        :loading="parsing"
        :disabled="!hasInput || submitting"
        @click="parsePreview"
      >
        <template #leading><Icon name="scan" :size="13" /></template>
        先解析预览
      </Button>
      <Button
        variant="primary"
        size="md"
        :loading="submitting"
        :disabled="!hasInput || parsing"
        @click="submit"
      >
        <template #leading><Icon name="upload" :size="13" /></template>
        一键转换并导入
      </Button>
    </template>
  </Modal>
</template>
