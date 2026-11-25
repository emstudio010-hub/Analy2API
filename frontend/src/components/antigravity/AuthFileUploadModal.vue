<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { toMessage } from '@/api/client'
import { uploadAntigravityAuthFile } from '@/api/endpoints'
import type { ContainerNode, Group } from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
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

const tab = ref('files')
const tabs = [
  { label: '本地多文件批量上传 (.json)', value: 'files', icon: 'upload' as const },
  { label: '粘贴单份 JSON 文本', value: 'paste', icon: 'code' as const },
]

interface PendingFile {
  name: string
  size: number
  text: string
  valid: boolean
  email: string
}

const pendingFiles = ref<PendingFile[]>([])
const pasteFilename = ref('antigravity-oauth.json')
const pasteContent = ref('')
const syncAccount = ref(true)
const selectedGroupIds = ref<number[]>([])
const submitting = ref(false)
const serverError = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

const agyGroups = computed(() =>
  props.groups.filter((g) => g.platform === 'antigravity'),
)

watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    pendingFiles.value = []
    pasteFilename.value = 'antigravity-oauth.json'
    pasteContent.value = ''
    syncAccount.value = true
    selectedGroupIds.value = agyGroups.value[0] ? [agyGroups.value[0].id] : []
    serverError.value = ''
  },
)

function parseJsonMeta(text: string): { valid: boolean; email: string } {
  try {
    const obj = JSON.parse(text) as Record<string, unknown>
    if (!obj || typeof obj !== 'object' || Array.isArray(obj)) {
      return { valid: false, email: '' }
    }
    const email = typeof obj.email === 'string' ? obj.email : ''
    return { valid: true, email }
  } catch {
    return { valid: false, email: '' }
  }
}

async function onFilesPicked(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  if (files.length === 0) return

  const loaded: PendingFile[] = []
  for (const file of files) {
    const text = await file.text()
    const meta = parseJsonMeta(text)
    const normalizedName = file.name.toLowerCase().endsWith('.json')
      ? file.name
      : `${file.name}.json`
    loaded.push({
      name: normalizedName,
      size: file.size,
      text,
      valid: meta.valid,
      email: meta.email,
    })
  }
  pendingFiles.value = [...pendingFiles.value, ...loaded]
  input.value = ''
}

function removePending(index: number): void {
  pendingFiles.value.splice(index, 1)
}

function toggleGroup(id: number, checked: boolean): void {
  if (checked) {
    if (!selectedGroupIds.value.includes(id)) selectedGroupIds.value.push(id)
  } else {
    selectedGroupIds.value = selectedGroupIds.value.filter((item) => item !== id)
  }
}

const pasteValid = computed(() => {
  if (!pasteFilename.value.trim() || !pasteContent.value.trim()) return false
  return parseJsonMeta(pasteContent.value).valid
})

const canSubmit = computed(() => {
  if (!props.node) return false
  if (tab.value === 'files') {
    return pendingFiles.value.length > 0 && pendingFiles.value.every((f) => f.valid)
  }
  return pasteValid.value
})

async function submit(): Promise<void> {
  if (!props.node || !canSubmit.value || submitting.value) return
  submitting.value = true
  serverError.value = ''

  try {
    if (tab.value === 'files') {
      let okCount = 0
      const errs: string[] = []
      for (const item of pendingFiles.value) {
        try {
          await uploadAntigravityAuthFile(props.node.id, {
            filename: item.name,
            content: item.text,
            raw_json: item.text,
            sync_account: syncAccount.value,
            group_ids: syncAccount.value ? selectedGroupIds.value : undefined,
          })
          okCount += 1
        } catch (err) {
          errs.push(`${item.name}: ${toMessage(err)}`)
        }
      }
      if (errs.length > 0) {
        toast.warning(
          `已上传 ${okCount} 个文件，${errs.length} 个失败`,
          errs[0],
        )
      } else {
        toast.success('凭证文件上传成功', `共导入 ${okCount} 个 OAuth JSON 文件`)
      }
    } else {
      const cleanName = pasteFilename.value.trim().endsWith('.json')
        ? pasteFilename.value.trim()
        : `${pasteFilename.value.trim()}.json`
      await uploadAntigravityAuthFile(props.node.id, {
        filename: cleanName,
        content: pasteContent.value.trim(),
        raw_json: pasteContent.value.trim(),
        sync_account: syncAccount.value,
        group_ids: syncAccount.value ? selectedGroupIds.value : undefined,
      })
      toast.success('凭证文件已保存并热加载', cleanName)
    }

    emit('uploaded')
    emit('update:open', false)
  } catch (err) {
    serverError.value = toMessage(err, '上传登录文件失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Modal
    :open="open"
    :title="`导入 OAuth 登录 JSON · ${node?.name ?? ''}`"
    subtitle="支持 AntiGravity / Gemini CLI / Claude / Codex 标准 OAuth JSON 凭证落盘与容器热加载"
    size="lg"
    :persistent="submitting"
    @update:open="emit('update:open', $event)"
  >
    <div class="space-y-4">
      <SegmentedTabs v-model="tab" :items="tabs" size="sm" block />

      <p
        v-if="serverError"
        class="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
      >
        <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
        <span>{{ serverError }}</span>
      </p>

      <!-- 模式 1：多文件选择 -->
      <div v-if="tab === 'files'" class="space-y-3">
        <input
          ref="fileInput"
          type="file"
          accept=".json,application/json"
          multiple
          class="hidden"
          @change="onFilesPicked"
        />
        <button
          type="button"
          class="flex w-full flex-col items-center justify-center gap-1.5 rounded-md border border-dashed border-line-strong bg-raised/40 px-4 py-6 text-center transition hover:border-accent hover:bg-accent/5"
          @click="fileInput?.click()"
        >
          <Icon name="upload" :size="20" class="text-accent" />
          <span class="text-xs font-medium text-fg">点击选择一个或多个 `.json` 登录凭证文件</span>
          <span class="text-2xs text-subtle">
            支持一次性批量导入多个账号的 OAuth 凭证文件，前端将自动校验 JSON 合法性
          </span>
        </button>

        <div v-if="pendingFiles.length > 0" class="scroll-thin max-h-48 space-y-1.5 overflow-y-auto pr-1">
          <div
            v-for="(file, idx) in pendingFiles"
            :key="`${file.name}-${idx}`"
            class="flex items-center justify-between gap-2 rounded-md border border-line bg-raised/50 px-3 py-1.5 text-xs"
          >
            <div class="flex min-w-0 items-center gap-2">
              <Badge :tone="file.valid ? 'success' : 'danger'">
                {{ file.valid ? 'JSON 有效' : '格式错误' }}
              </Badge>
              <span class="truncate font-mono text-fg">{{ file.name }}</span>
              <span v-if="file.email" class="truncate text-2xs text-muted">({{ file.email }})</span>
              <span class="tnum text-2xs text-subtle">{{ formatBytes(file.size) }}</span>
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

      <!-- 模式 2：直接粘贴 JSON -->
      <div v-else class="space-y-3">
        <Field label="保存文件名 (.json)" required hint="严禁包含路径分隔符，自动补全 .json 后缀">
          <Input v-model="pasteFilename" mono placeholder="user-account-01.json" />
        </Field>

        <Field
          label="OAuth JSON 原文"
          required
          :error="pasteContent.trim() && !pasteValid ? 'JSON 格式不合法，请检查括号与引号' : ''"
        >
          <Textarea
            v-model="pasteContent"
            :rows="7"
            mono
            placeholder='{"type":"antigravity","email":"dev@example.com","refresh_token":"...","project_id":"..."}'
          />
        </Field>
      </div>

      <!-- 同步至网关调度池设置 -->
      <div class="panel-inset space-y-3 p-3.5">
        <Switch
          v-model="syncAccount"
          label="上传后自动同步至 AntiGravity 调度账号池"
          description="开启后将自动把凭证注册为网关可调度账号，可直接绑定分组供下游 API Key 调用"
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
      <Button variant="ghost" size="md" :disabled="submitting" @click="emit('update:open', false)">
        取消
      </Button>
      <Button
        variant="primary"
        size="md"
        :loading="submitting"
        :disabled="!canSubmit"
        @click="submit"
      >
        上传并加载凭证
      </Button>
    </template>
  </Modal>
</template>
