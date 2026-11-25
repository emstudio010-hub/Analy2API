<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import Select from '@/components/ui/Select.vue'
import { accountsApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type {
  Account,
  CreateAccountPayload,
  Group,
  ProxyTestResult,
  UpdateAccountPayload,
  UpstreamPlatform,
} from '@/api/types'
import { useToastStore } from '@/stores/toast'
import { fromLocalInputValue, toLocalInputValue } from '@/utils/format'
import { DEFAULT_PLATFORM, platformMeta } from '@/utils/platforms'

const DEFAULT_DOMAIN = 'https://www.codebuddy.cn'

const props = defineProps<{
  open: boolean
  /** null 表示新建。 */
  account: Account | null
  groups: Group[]
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'saved'): void
}>()

const toast = useToastStore()

const isEdit = computed(() => props.account !== null)

const subtitle = computed(() =>
  isEdit.value
    ? 'Access Token / Refresh Token 留空表示保留原凭证；可随时修改独立代理 IP 与设备指纹'
    : '配置 WorkBuddy 账号凭证、独立代理 IP（http/socks5）与成对设备指纹',
)

const platform = ref<UpstreamPlatform>(DEFAULT_PLATFORM)
const name = ref('')
const domain = ref(DEFAULT_DOMAIN)
const apiKey = ref('')
const refreshToken = ref('')
const uid = ref('')
const nickname = ref('')
const email = ref('')
const proxyUrl = ref('')
const qimei36 = ref('')
const machineId = ref('')
const notes = ref('')
const concurrency = ref('3')
const priority = ref('50')
const multiplier = ref('1')
const groupIds = ref<number[]>([])
const expiresAt = ref('')
const clearExpiry = ref(false)
const status = ref('active')
const schedulable = ref(true)

const submitting = ref(false)
const serverError = ref('')
const showKey = ref(false)
const showRefreshToken = ref(false)

const proxyTesting = ref(false)
const proxyTestResult = ref<ProxyTestResult | null>(null)
const proxyTestError = ref('')

const statusOptions = [
  { label: '正常', value: 'active' },
  { label: '已禁用', value: 'disabled' },
  { label: '异常', value: 'error' },
]

function toNumber(value: string): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function validateProxyInput(raw: string): string {
  const v = raw.trim()
  if (!v || v.toLowerCase() === 'direct') return ''
  try {
    const parsed = new URL(v)
    if (!['http:', 'https:', 'socks5:', 'socks5h:'].includes(parsed.protocol)) {
      return '代理协议仅支持 http://、https://、socks5://、socks5h:// 或 direct'
    }
    if (!parsed.hostname) return '代理地址缺少主机名或 IP'
    return ''
  } catch {
    return '代理格式不正确，示例：http://user:pass@ip:port 或 socks5://127.0.0.1:1080'
  }
}

const meta = computed(() => platformMeta(platform.value))

/** 只列同平台的分组（WorkBuddy 账号绑定 WorkBuddy 分组）。 */
const platformGroups = computed(() =>
  props.groups.filter((group) => (group.platform || DEFAULT_PLATFORM) === platform.value),
)
const hiddenGroupCount = computed(() => props.groups.length - platformGroups.value.length)

const errors = computed(() => {
  const out: Record<string, string> = {}
  if (!name.value.trim()) out.name = '请输入账号名称'

  const key = apiKey.value.trim()
  if (!isEdit.value && !key) {
    out.apiKey = '请输入 Access Token'
  }

  const proxyErr = validateProxyInput(proxyUrl.value)
  if (proxyErr) out.proxyUrl = proxyErr

  if (toNumber(concurrency.value) <= 0) out.concurrency = '并发数必须大于 0'
  if (toNumber(priority.value) < 0) out.priority = '优先级不能为负数'
  if (toNumber(multiplier.value) <= 0) out.multiplier = '倍率必须大于 0'
  return out
})

const valid = computed(() => Object.keys(errors.value).length === 0)

function randomHex(length: number): string {
  const bytes = new Uint8Array(Math.ceil(length / 2))
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0'))
    .join('')
    .slice(0, length)
}

function generateDevicePair(): void {
  qimei36.value = randomHex(36)
  if (typeof crypto.randomUUID === 'function') {
    machineId.value = crypto.randomUUID().toLowerCase()
  } else {
    const h = randomHex(32)
    machineId.value = `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}`
  }
}

function reset(): void {
  const account = props.account
  platform.value = account?.platform || DEFAULT_PLATFORM
  name.value = account?.name ?? ''
  domain.value = account?.domain || DEFAULT_DOMAIN
  apiKey.value = ''
  refreshToken.value = ''
  uid.value = account?.uid ?? ''
  nickname.value = account?.nickname ?? ''
  email.value = account?.email ?? ''
  proxyUrl.value = account?.proxy_url ?? ''
  qimei36.value = account?.qimei36 ?? ''
  machineId.value = account?.machine_id ?? ''
  notes.value = account?.notes ?? ''
  concurrency.value = String(account?.concurrency ?? 3)
  priority.value = String(account?.priority ?? 50)
  multiplier.value = String(account?.rate_multiplier ?? 1)
  groupIds.value = account ? [...account.group_ids] : []
  expiresAt.value = account?.expires_at ? toLocalInputValue(account.expires_at) : ''
  clearExpiry.value = false
  status.value = account?.status ?? 'active'
  schedulable.value = account?.schedulable ?? true
  serverError.value = ''
  showKey.value = false
  showRefreshToken.value = false
  proxyTestResult.value = null
  proxyTestError.value = ''
}

function selectedGroupIds(): number[] {
  if (props.groups.length === 0) return groupIds.value
  const allowed = new Set(platformGroups.value.map((group) => group.id))
  return groupIds.value.filter((id) => allowed.has(id))
}

watch(
  () => [props.open, props.account] as const,
  () => {
    if (props.open) reset()
  },
  { immediate: true },
)

watch(proxyUrl, () => {
  proxyTestResult.value = null
  proxyTestError.value = ''
})

function toggleGroup(id: number, checked: boolean): void {
  if (checked) {
    if (!groupIds.value.includes(id)) groupIds.value = [...groupIds.value, id]
  } else {
    groupIds.value = groupIds.value.filter((item) => item !== id)
  }
}

function onExpiryChange(value: string): void {
  expiresAt.value = value
  clearExpiry.value = false
}

function clearExpiryTime(): void {
  expiresAt.value = ''
  clearExpiry.value = true
}

function close(): void {
  emit('update:open', false)
}

async function runProxyTest(): Promise<void> {
  if (errors.value.proxyUrl || proxyTesting.value) return
  if (!isEdit.value || !props.account) {
    toast.info('格式校验通过', '保存创建账号后可立即点击测速验证真实出口 IP')
    return
  }
  proxyTesting.value = true
  proxyTestResult.value = null
  proxyTestError.value = ''
  try {
    const res = await accountsApi.testProxy(props.account.id, proxyUrl.value.trim())
    proxyTestResult.value = res
    if (res.ok) {
      toast.success('代理连通性正常', `出口 IP: ${res.exit_ip || '未知'} · ${res.latency_ms} ms`)
    } else {
      proxyTestError.value = res.error || '代理连通性测试失败'
    }
  } catch (err) {
    proxyTestError.value = toMessage(err, '代理测试失败')
  } finally {
    proxyTesting.value = false
  }
}

async function submit(): Promise<void> {
  if (!valid.value || submitting.value) return
  submitting.value = true
  serverError.value = ''
  try {
    const expires = clearExpiry.value ? '' : fromLocalInputValue(expiresAt.value) || undefined
    const key = apiKey.value.trim()
    const refresh = refreshToken.value.trim()
    const groups = selectedGroupIds()
    const cleanDomain = domain.value.trim() || DEFAULT_DOMAIN

    if (isEdit.value && props.account) {
      const payload: UpdateAccountPayload = {
        name: name.value.trim(),
        domain: cleanDomain,
        uid: uid.value.trim(),
        nickname: nickname.value.trim(),
        email: email.value.trim(),
        proxy_url: proxyUrl.value.trim(),
        qimei36: qimei36.value.trim(),
        machine_id: machineId.value.trim(),
        notes: notes.value,
        concurrency: toNumber(concurrency.value),
        priority: toNumber(priority.value),
        rate_multiplier: toNumber(multiplier.value),
        status: status.value,
        schedulable: schedulable.value,
        group_ids: groups,
      }
      if (key) payload.api_key = key
      if (refresh) payload.refresh_token = refresh
      if (clearExpiry.value) payload.expires_at = ''
      else if (expires) payload.expires_at = expires

      await accountsApi.update(props.account.id, payload)
      toast.success('账号已更新', name.value.trim())
    } else {
      const payload: CreateAccountPayload = {
        name: name.value.trim(),
        platform: platform.value,
        domain: cleanDomain,
        api_key: key,
        notes: notes.value,
        concurrency: toNumber(concurrency.value),
        priority: toNumber(priority.value),
        rate_multiplier: toNumber(multiplier.value),
        group_ids: groups,
      }
      if (refresh) payload.refresh_token = refresh
      if (uid.value.trim()) payload.uid = uid.value.trim()
      if (nickname.value.trim()) payload.nickname = nickname.value.trim()
      if (email.value.trim()) payload.email = email.value.trim()
      if (proxyUrl.value.trim()) payload.proxy_url = proxyUrl.value.trim()
      if (qimei36.value.trim()) payload.qimei36 = qimei36.value.trim()
      if (machineId.value.trim()) payload.machine_id = machineId.value.trim()
      if (expires) payload.expires_at = expires

      await accountsApi.create(payload)
      toast.success('账号已创建', name.value.trim())
    }

    emit('saved')
    close()
  } catch (err) {
    serverError.value = toMessage(err, isEdit.value ? '更新账号失败' : '创建账号失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Modal
    :open="open"
    :title="isEdit ? '编辑 WorkBuddy 账号' : '添加 WorkBuddy 账号'"
    :subtitle="subtitle"
    size="lg"
    @update:open="emit('update:open', $event)"
  >
    <form class="space-y-4" @submit.prevent="submit">
      <p
        v-if="serverError"
        class="flex items-start gap-1.5 rounded-md border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs leading-relaxed text-danger"
      >
        <Icon name="alertCircle" :size="13" class="mt-px shrink-0" />
        <span>{{ serverError }}</span>
      </p>

      <div class="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-raised/50 px-3 py-2">
        <div class="flex items-center gap-2">
          <Badge :tone="meta.tone" dot>{{ meta.label }}</Badge>
          <span class="text-2xs text-subtle">
            {{ isEdit ? '账号绑定独立代理与设备指纹，互不串流' : '默认接入 WorkBuddy / CodeBuddy 协议' }}
          </span>
        </div>
        <span v-if="account?.uid" class="font-mono text-2xs text-muted">UID: {{ account.uid }}</span>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field label="账号名称" required :error="errors.name">
          <Input v-model="name" placeholder="例如：WorkBuddy 主力号 01" maxlength="100" />
        </Field>

        <Field label="Domain 端点" hint="默认 https://www.codebuddy.cn，支持填裸域名自动补全">
          <Input v-model="domain" :mono="true" :placeholder="DEFAULT_DOMAIN" autocomplete="off" />
        </Field>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field
          label="Access Token"
          :required="!isEdit"
          :error="errors.apiKey"
          :hint="isEdit ? '留空表示不修改当前 Access Token' : 'WorkBuddy 登录后的 Access Token'"
        >
          <div class="relative">
            <Input
              v-model="apiKey"
              :type="showKey ? 'text' : 'password'"
              :mono="true"
              class="pr-8"
              :placeholder="isEdit ? '••••••••（留空不修改）' : '粘贴 Access Token'"
              autocomplete="off"
            />
            <button
              type="button"
              class="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-1 text-subtle transition hover:text-fg"
              :aria-label="showKey ? '隐藏 Token' : '显示 Token'"
              @click="showKey = !showKey"
            >
              <Icon :name="showKey ? 'eyeOff' : 'eye'" :size="14" />
            </button>
          </div>
        </Field>

        <Field
          label="Refresh Token"
          :hint="isEdit ? '留空不修改；用于续期与生成官网免密登录链接' : '选填，填写后支持自动刷新 Token 与免密登录'"
        >
          <div class="relative">
            <Input
              v-model="refreshToken"
              :type="showRefreshToken ? 'text' : 'password'"
              :mono="true"
              class="pr-8"
              :placeholder="isEdit ? '••••••••（留空不修改）' : '粘贴 Refresh Token（选填）'"
              autocomplete="off"
            />
            <button
              type="button"
              class="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-1 text-subtle transition hover:text-fg"
              :aria-label="showRefreshToken ? '隐藏 Refresh Token' : '显示 Refresh Token'"
              @click="showRefreshToken = !showRefreshToken"
            >
              <Icon :name="showRefreshToken ? 'eyeOff' : 'eye'" :size="14" />
            </button>
          </div>
        </Field>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-3">
        <Field label="UID" hint="用户唯一标识（选填）">
          <Input v-model="uid" :mono="true" placeholder="例如：100012345" autocomplete="off" />
        </Field>
        <Field label="昵称" hint="便于识别（选填）">
          <Input v-model="nickname" placeholder="展示昵称" autocomplete="off" />
        </Field>
        <Field label="邮箱" hint="账号关联邮箱（选填）">
          <Input v-model="email" type="email" placeholder="user@example.com" autocomplete="off" />
        </Field>
      </div>

      <!-- 账号独立代理 IP -->
      <Field
        label="独立代理 IP (proxy_url)"
        :error="errors.proxyUrl"
        hint="支持 http://user:pass@ip:port、https://...、socks5://127.0.0.1:1080 或 direct；留空走全局代理/直连"
      >
        <div class="flex items-center gap-2">
          <Input
            v-model="proxyUrl"
            :mono="true"
            :invalid="Boolean(errors.proxyUrl)"
            placeholder="http://user:pass@ip:port 或 socks5://127.0.0.1:1080"
            autocomplete="off"
          />
          <Button
            variant="secondary"
            size="sm"
            type="button"
            class="shrink-0"
            :loading="proxyTesting"
            :disabled="Boolean(errors.proxyUrl)"
            :title="isEdit ? '通过当前输入的代理立即探测上游连通性与出口 IP' : '校验代理格式（保存后可测真实出口 IP）'"
            @click="runProxyTest"
          >
            <template #leading><Icon name="globe" :size="13" /></template>
            {{ isEdit ? '测试代理' : '校验格式' }}
          </Button>
        </div>

        <div
          v-if="proxyTestResult?.ok"
          class="mt-1.5 flex flex-wrap items-center gap-2 rounded border border-success/30 bg-success/10 px-2.5 py-1.5 text-2xs text-success"
        >
          <Icon name="checkCircle" :size="12" />
          <span>代理正常 · 出口 IP：<code class="font-mono">{{ proxyTestResult.exit_ip || '—' }}</code></span>
          <span class="tnum text-muted">耗时 {{ proxyTestResult.latency_ms }} ms</span>
        </div>
        <p
          v-else-if="proxyTestError"
          class="mt-1.5 flex items-center gap-1.5 rounded border border-danger/30 bg-danger/10 px-2.5 py-1.5 text-2xs text-danger"
        >
          <Icon name="alertCircle" :size="12" />
          <span>{{ proxyTestError }}</span>
        </p>
      </Field>

      <!-- 成对设备指纹 -->
      <div class="rounded-md border border-line bg-raised/40 p-3 space-y-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div>
            <p class="text-xs font-medium text-fg">设备指纹（qimei36 + machine_id）</p>
            <p class="text-2xs text-subtle">留空时后端自动生成成对指纹；与账号长期绑定防止风控飘移</p>
          </div>
          <Button variant="subtle" size="xs" type="button" @click="generateDevicePair">
            <template #leading><Icon name="rotate" :size="12" /></template>
            随机生成一对指纹
          </Button>
        </div>

        <div class="grid gap-3 sm:grid-cols-2">
          <Field label="qimei36" hint="36 位小写十六进制指纹">
            <Input v-model="qimei36" :mono="true" placeholder="留空自动生成 36 位 hex" autocomplete="off" />
          </Field>
          <Field label="machine_id" hint="UUID v4 设备标识">
            <Input v-model="machineId" :mono="true" placeholder="留空自动生成 UUID" autocomplete="off" />
          </Field>
        </div>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-3">
        <Field label="并发数" :error="errors.concurrency" hint="同时在途请求上限">
          <Input v-model="concurrency" type="number" min="1" step="1" />
        </Field>
        <Field label="优先级" :error="errors.priority" hint="数值越小越优先被调度">
          <Input v-model="priority" type="number" min="0" step="1" />
        </Field>
        <Field label="费用倍率" :error="errors.multiplier" hint="1 表示原价折算">
          <Input v-model="multiplier" type="number" min="0" step="0.01" />
        </Field>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field label="过期时间" hint="留空表示永不过期">
          <div class="flex items-center gap-2">
            <Input
              :model-value="expiresAt"
              type="datetime-local"
              :disabled="clearExpiry"
              @update:model-value="onExpiryChange"
            />
            <Button
              v-if="isEdit"
              variant="ghost"
              size="sm"
              type="button"
              :disabled="clearExpiry"
              @click="clearExpiryTime"
            >
              清除
            </Button>
          </div>
        </Field>

        <Field label="备注" hint="给自己看的来源/用途说明">
          <Input v-model="notes" placeholder="可选" />
        </Field>
      </div>

      <div v-if="isEdit" class="grid gap-3.5 sm:grid-cols-2">
        <Field label="状态" hint="禁用后该账号不再参与调度">
          <Select v-model="status" :options="statusOptions" />
        </Field>
        <div class="flex items-end pb-1.5">
          <Checkbox
            v-model="schedulable"
            label="参与调度"
            description="关闭后保留账号与签到功能，但不分配对话请求"
          />
        </div>
      </div>

      <Field
        label="所属分组"
        hint="不选分组时该账号不会被任何下游密钥路由；同一分组仅匹配同平台账号"
      >
        <div
          v-if="platformGroups.length === 0"
          class="space-y-1.5 rounded-md border border-warning/30 bg-warning/10 px-2.5 py-2 text-2xs text-warning"
        >
          <p>
            还没有 {{ meta.label }} 平台的分组，请先到「分组」页创建一个，否则该账号无法被下游调用。
          </p>
          <RouterLink
            :to="{ name: 'groups' }"
            class="inline-flex items-center gap-1 font-medium text-accent hover:underline"
            @click="close"
          >
            去创建 {{ meta.label }} 分组
            <Icon name="external" :size="11" />
          </RouterLink>
        </div>

        <div v-else class="rounded-md border border-line bg-raised/40 p-2.5">
          <div class="scroll-thin max-h-36 space-y-2 overflow-y-auto">
            <Checkbox
              v-for="group in platformGroups"
              :key="group.id"
              :model-value="groupIds.includes(group.id)"
              :label="group.name"
              :description="`${group.account_count} 个账号 · ${group.api_key_count} 把密钥`"
              @update:model-value="toggleGroup(group.id, $event)"
            />
          </div>
          <p v-if="hiddenGroupCount > 0" class="mt-1.5 text-2xs text-subtle">
            另有 {{ hiddenGroupCount }} 个其它平台的历史分组未列出。
          </p>
        </div>
      </Field>
    </form>

    <template #footer>
      <Button variant="ghost" size="md" :disabled="submitting" @click="close">取消</Button>
      <Button
        variant="primary"
        size="md"
        :loading="submitting"
        :disabled="!valid"
        @click="submit"
      >
        {{ isEdit ? '保存修改' : '创建账号' }}
      </Button>
    </template>
  </Modal>
</template>
