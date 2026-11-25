<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { accountsApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { BatchImportResult, Group } from '@/api/types'
import { useToastStore } from '@/stores/toast'
import { DEFAULT_PLATFORM } from '@/utils/platforms'

const DEFAULT_DOMAIN = 'https://www.codebuddy.cn'

const props = defineProps<{
  open: boolean
  groups: Group[]
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'imported'): void
}>()

const toast = useToastStore()

const raw = ref('')
const domain = ref(DEFAULT_DOMAIN)
const proxyUrl = ref('')
const upsert = ref(true)
const concurrency = ref('3')
const priority = ref('50')
const groupIds = ref<number[]>([])
const submitting = ref(false)
const serverError = ref('')
const result = ref<BatchImportResult | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

const workbuddyGroups = computed(() =>
  props.groups.filter((group) => (group.platform || DEFAULT_PLATFORM) === DEFAULT_PLATFORM),
)
const hiddenGroupCount = computed(() => props.groups.length - workbuddyGroups.value.length)

interface ParsedLine {
  line: number
  name: string
  key: string
  extra: string
  valid: boolean
}

function looksLikeJson(text: string): boolean {
  const trimmed = text.trimStart()
  return trimmed.startsWith('{') || trimmed.startsWith('[')
}

const isJsonInput = computed(() => looksLikeJson(raw.value))

const jsonValid = computed(() => {
  if (!isJsonInput.value) return false
  try {
    JSON.parse(raw.value)
    return true
  } catch {
    return false
  }
})

const parsed = computed<ParsedLine[]>(() => {
  if (isJsonInput.value) return []

  const out: ParsedLine[] = []
  const lines = raw.value.split('\n')
  lines.forEach((line, index) => {
    const trimmed = line.trim()
    if (!trimmed || trimmed.startsWith('#')) return

    // 支持 uid|access_token|refresh_token|proxy_url
    if (trimmed.includes('|')) {
      const parts = trimmed.split('|').map((p) => p.trim())
      const uidPart = parts[0] ?? ''
      const tokenPart = parts[1] ?? ''
      const extraParts: string[] = []
      if (parts[2]) extraParts.push('含 Refresh')
      if (parts[3]) extraParts.push(`代理 ${parts[3]}`)
      out.push({
        line: index + 1,
        name: uidPart ? `UID:${uidPart}` : '',
        key: tokenPart,
        extra: extraParts.join(' · '),
        valid: tokenPart.length > 0,
      })
      return
    }

    // 支持 名称,access_token 或 裸 access_token
    let name = ''
    let key = trimmed
    const comma = trimmed.lastIndexOf(',')
    if (comma >= 0) {
      const candidateName = trimmed.slice(0, comma).trim()
      const candidateKey = trimmed.slice(comma + 1).trim()
      if (candidateKey) {
        name = candidateName
        key = candidateKey
      }
    }
    out.push({
      line: index + 1,
      name,
      key,
      extra: '',
      valid: key.length > 0,
    })
  })
  return out
})

const validCount = computed(() => parsed.value.filter((item) => item.valid).length)
const previewLines = computed(() => parsed.value.slice(0, 50))

const proxyError = computed(() => {
  const v = proxyUrl.value.trim()
  if (!v || v.toLowerCase() === 'direct') return ''
  try {
    const u = new URL(v)
    if (!['http:', 'https:', 'socks5:', 'socks5h:'].includes(u.protocol)) {
      return '代理仅支持 http://、https://、socks5://、socks5h:// 或 direct'
    }
    if (!u.hostname) return '代理地址缺少主机名'
    return ''
  } catch {
    return '代理格式不正确，示例：http://user:pass@ip:port 或 socks5://127.0.0.1:1080'
  }
})

const canSubmit = computed(() => {
  if (submitting.value || proxyError.value) return false
  if (isJsonInput.value) return raw.value.trim().length > 0
  return validCount.value > 0
})

function maskKey(key: string): string {
  if (key.length <= 14) return key
  return `${key.slice(0, 10)}…${key.slice(-4)}`
}

watch(
  () => props.open,
  (open) => {
    if (!open) return
    raw.value = ''
    domain.value = DEFAULT_DOMAIN
    proxyUrl.value = ''
    upsert.value = true
    concurrency.value = '3'
    priority.value = '50'
    groupIds.value = []
    serverError.value = ''
    result.value = null
  },
)

function toggleGroup(id: number, checked: boolean): void {
  if (checked) {
    if (!groupIds.value.includes(id)) groupIds.value = [...groupIds.value, id]
  } else {
    groupIds.value = groupIds.value.filter((item) => item !== id)
  }
}

function selectedGroupIds(): number[] {
  if (props.groups.length === 0) return groupIds.value
  const allowed = new Set(workbuddyGroups.value.map((group) => group.id))
  return groupIds.value.filter((id) => allowed.has(id))
}

function toNumber(value: string, fallback: number): number {
  const parsedNum = Number(value)
  return Number.isFinite(parsedNum) ? parsedNum : fallback
}

function pickLocalFile(): void {
  fileInput.value?.click()
}

async function onFileChange(event: Event): Promise<void> {
  const target = event.target as HTMLInputElement
  const file = target.files?.[0]
  if (!file) return
  try {
    raw.value = await file.text()
    toast.info('已载入文件内容', `${file.name}（${raw.value.length} 字符）`)
  } catch (err) {
    toast.error('读取文件失败', toMessage(err))
  } finally {
    target.value = ''
  }
}

async function submit(): Promise<void> {
  if (!canSubmit.value) return
  submitting.value = true
  serverError.value = ''
  try {
    const response = await accountsApi.batchImport({
      keys: raw.value,
      platform: DEFAULT_PLATFORM,
      domain: domain.value.trim() || DEFAULT_DOMAIN,
      proxy_url: proxyUrl.value.trim() || undefined,
      concurrency: toNumber(concurrency.value, 3),
      priority: toNumber(priority.value, 50),
      group_ids: selectedGroupIds(),
      upsert: upsert.value,
    })
    result.value = response
    const changedTotal = (response.created ?? 0) + (response.updated ?? 0)
    if (response.failed === 0) {
      toast.success(
        '批量导入完成',
        `新建 ${response.created} 个 · 更新 ${response.updated ?? 0} 个`,
      )
    } else if (changedTotal === 0) {
      toast.error('全部导入失败', '请查看失败行明细后重试')
    } else {
      toast.warning(
        '导入部分完成',
        `新建 ${response.created} · 更新 ${response.updated ?? 0} · 失败 ${response.failed}`,
      )
    }
    if (changedTotal > 0) emit('imported')
  } catch (err) {
    serverError.value = toMessage(err, '批量导入失败')
  } finally {
    submitting.value = false
  }
}

function close(): void {
  emit('update:open', false)
}
</script>

<template>
  <Modal
    :open="open"
    title="批量导入 WorkBuddy 账号"
    subtitle="支持直接粘贴 ~/.agent-switch/accounts.json、~/.workbuddy-sdk/accounts.json、workbuddy-desktop.info 或多行凭证"
    size="lg"
    @update:open="emit('update:open', $event)"
  >
    <div class="space-y-4">
      <p
        v-if="serverError"
        class="flex items-start gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs leading-relaxed text-danger"
      >
        <Icon name="alertCircle" :size="13" class="mt-px shrink-0" />
        <span>{{ serverError }}</span>
      </p>

      <!-- 导入结果 -->
      <div
        v-if="result"
        class="space-y-2 rounded-md border border-line bg-raised/50 p-3"
      >
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-[13px] font-medium text-fg">导入结果</span>
          <span
            class="rounded border border-success/25 bg-success/12 px-1.5 py-px text-2xs text-success"
          >
            新建 {{ result.created }}
          </span>
          <span
            class="rounded border border-info/25 bg-info/12 px-1.5 py-px text-2xs text-info"
          >
            更新 {{ result.updated ?? 0 }}
          </span>
          <span
            v-if="result.failed > 0"
            class="rounded border border-danger/25 bg-danger/12 px-1.5 py-px text-2xs text-danger"
          >
            失败 {{ result.failed }}
          </span>
        </div>

        <ul
          v-if="result.failures.length > 0"
          class="scroll-thin max-h-40 space-y-1 overflow-y-auto text-2xs"
        >
          <li
            v-for="failure in result.failures"
            :key="`${failure.line}-${failure.key}`"
            class="flex items-start gap-2"
          >
            <span class="tnum shrink-0 text-subtle">第 {{ failure.line }} 行</span>
            <span class="shrink-0 font-mono text-muted">{{ failure.key }}</span>
            <span class="text-danger">{{ failure.error }}</span>
          </li>
        </ul>
      </div>

      <Field
        label="JSON 或凭证列表"
        hint="支持 accounts.json / workbuddy-desktop.info 完整 JSON，或每行一条 token / 名称,token / uid|access_token|refresh_token|proxy_url（# 开头为注释）"
      >
        <div class="mb-1.5 flex flex-wrap items-center justify-between gap-2">
          <span class="text-2xs text-subtle">可直接粘贴文本，或从本地选择 JSON 文件载入</span>
          <div>
            <input
              ref="fileInput"
              type="file"
              accept=".json,.info,.txt"
              class="hidden"
              @change="onFileChange"
            />
            <Button variant="secondary" size="xs" type="button" @click="pickLocalFile">
              <template #leading><Icon name="upload" :size="12" /></template>
              选择本地文件
            </Button>
          </div>
        </div>

        <Textarea
          v-model="raw"
          :rows="7"
          mono
          placeholder='粘贴 ~/.agent-switch/accounts.json、~/.workbuddy-sdk/accounts.json、workbuddy-desktop.info 内容，或每行一条 token'
        />
      </Field>

      <p
        v-if="isJsonInput"
        class="flex items-start gap-1.5 rounded-md border px-2.5 py-2 text-2xs leading-relaxed"
        :class="
          jsonValid
            ? 'border-accent/30 bg-accent/8 text-accent'
            : 'border-warning/30 bg-warning/10 text-warning'
        "
      >
        <Icon :name="jsonValid ? 'checkCircle' : 'alert'" :size="13" class="mt-px shrink-0" />
        <span>
          {{
            jsonValid
              ? '已识别为 JSON 配置（支持 accounts.json / workbuddy-desktop.info / auth_raw 嵌套格式），将交由后端完整解析 UID、Token、设备指纹与独立代理。'
              : '检测到以 { 或 [ 开头，若为多个 JSON 片段拼接请直接提交由后端逐段识别；若格式有误将在导入结果中提示。'
          }}
        </span>
      </p>

      <div v-if="parsed.length > 0" class="flex flex-wrap items-center gap-3 text-2xs">
        <span class="text-subtle">按行预解析</span>
        <span class="tnum text-fg">{{ parsed.length }} 行</span>
        <span class="tnum text-success">有效 {{ validCount }}</span>
      </div>

      <div
        v-if="parsed.length > 0"
        class="scroll-thin max-h-44 overflow-y-auto rounded-md border border-line"
      >
        <table class="w-full border-collapse text-left">
          <thead class="sticky top-0 border-b border-line bg-surface">
            <tr>
              <th class="th w-14">行号</th>
              <th class="th">名称 / UID</th>
              <th class="th">Access Token</th>
              <th class="th">附加字段</th>
              <th class="th w-14">校验</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-line">
            <tr v-for="item in previewLines" :key="item.line">
              <td class="td tnum text-subtle">{{ item.line }}</td>
              <td class="td">{{ item.name || '（自动命名）' }}</td>
              <td class="td font-mono text-muted">{{ item.key ? maskKey(item.key) : '—' }}</td>
              <td class="td text-2xs text-subtle">{{ item.extra || '—' }}</td>
              <td class="td">
                <Icon
                  :name="item.valid ? 'checkCircle' : 'alertCircle'"
                  :size="14"
                  :class="item.valid ? 'text-success' : 'text-danger'"
                />
              </td>
            </tr>
          </tbody>
        </table>
        <p
          v-if="parsed.length > previewLines.length"
          class="border-t border-line px-3 py-1.5 text-2xs text-subtle"
        >
          仅预览前 {{ previewLines.length }} 行，其余 {{ parsed.length - previewLines.length }} 行同样会被导入。
        </p>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field label="默认 Domain" hint="账号未自带 domain 时使用">
          <Input v-model="domain" :mono="true" :placeholder="DEFAULT_DOMAIN" autocomplete="off" />
        </Field>
        <Field
          label="默认独立代理 (proxy_url)"
          :error="proxyError"
          hint="选填；仅作用于未单独指定 proxy_url 的账号"
        >
          <Input
            v-model="proxyUrl"
            :mono="true"
            :invalid="Boolean(proxyError)"
            placeholder="http://user:pass@ip:port 或 socks5://127.0.0.1:1080"
            autocomplete="off"
          />
        </Field>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field label="统一并发数" hint="应用到本次导入的所有账号">
          <Input v-model="concurrency" type="number" min="1" step="1" />
        </Field>
        <Field label="统一优先级" hint="数值越小越优先">
          <Input v-model="priority" type="number" min="0" step="1" />
        </Field>
      </div>

      <div class="rounded-md border border-line bg-raised/40 px-3 py-2.5">
        <Checkbox
          v-model="upsert"
          label="按 UID 去重更新 (upsert)"
          description="开启后，若库内已存在相同 UID 的 WorkBuddy 账号，将直接刷新其 Token 与资料而不是重复创建"
        />
      </div>

      <Field label="目标分组" hint="只列出 WorkBuddy 分组；留空时账号暂不绑定分组">
        <div
          v-if="workbuddyGroups.length === 0"
          class="space-y-1.5 rounded-md border border-warning/30 bg-warning/10 px-2.5 py-2 text-2xs text-warning"
        >
          <p>
            还没有 WorkBuddy 分组，本次导入的账号将处于未分组状态，暂时不会被下游密钥使用。
          </p>
          <RouterLink
            :to="{ name: 'groups' }"
            class="inline-flex items-center gap-1 font-medium text-accent hover:underline"
            @click="close"
          >
            去创建 WorkBuddy 分组
            <Icon name="external" :size="11" />
          </RouterLink>
        </div>
        <div v-else class="rounded-md border border-line bg-raised/40 p-2.5">
          <div class="scroll-thin max-h-32 space-y-2 overflow-y-auto">
            <Checkbox
              v-for="group in workbuddyGroups"
              :key="group.id"
              :model-value="groupIds.includes(group.id)"
              :label="group.name"
              :description="`${group.account_count} 个账号`"
              @update:model-value="toggleGroup(group.id, $event)"
            />
          </div>
          <p v-if="hiddenGroupCount > 0" class="mt-1.5 text-2xs text-subtle">
            另有 {{ hiddenGroupCount }} 个其它平台的历史分组未列出。
          </p>
        </div>
      </Field>
    </div>

    <template #footer>
      <Button variant="ghost" size="md" :disabled="submitting" @click="close">关闭</Button>
      <Button
        variant="primary"
        size="md"
        :loading="submitting"
        :disabled="!canSubmit"
        @click="submit"
      >
        {{ isJsonInput ? '导入 JSON 账号' : `导入 ${validCount} 个账号` }}
      </Button>
    </template>
  </Modal>
</template>
