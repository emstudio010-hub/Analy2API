<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { toMessage } from '@/api/client'
import { createAntigravityNode, updateAntigravityNode } from '@/api/endpoints'
import type { ContainerNode, ContainerNodeMode, ContainerNodePayload } from '@/api/types'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Switch from '@/components/ui/Switch.vue'
import { useToastStore } from '@/stores/toast'

const props = defineProps<{
  open: boolean
  node: ContainerNode | null
  dockerAvailable: boolean
  defaultImage: string
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'saved', node: ContainerNode): void
}>()

const toast = useToastStore()

const isEdit = computed(() => props.node !== null)

const modeTabs = [
  { label: '本机 Docker 容器 (local_docker)', value: 'local_docker', icon: 'box' as const },
  { label: '外部远程节点 (remote_api)', value: 'remote_api', icon: 'globe' as const },
]

const mode = ref<ContainerNodeMode>('local_docker')
const name = ref('')
const dockerImage = ref('')
const containerName = ref('')
const hostIp = ref('127.0.0.1')
const hostPort = ref('8317')
const managementKey = ref('')
const apiKey = ref('')
const proxyUrl = ref('')
const strictIsolation = ref(true)
const autoStart = ref(true)

const showMgmtKey = ref(false)
const showApiKey = ref(false)
const submitting = ref(false)
const serverError = ref('')

function randomSecret(prefix: string, hexLen = 32): string {
  const bytes = new Uint8Array(Math.ceil(hexLen / 2))
  crypto.getRandomValues(bytes)
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0'))
    .join('')
    .slice(0, hexLen)
  return `${prefix}${hex}`
}

function generateMgmtKey(): void {
  managementKey.value = randomSecret('agy-mgmt-', 32)
  showMgmtKey.value = true
}

function generateInferenceKey(): void {
  apiKey.value = randomSecret('sk-agy-', 32)
  showApiKey.value = true
}

function slugifyContainerName(raw: string): string {
  const slug = raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_.-]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return slug ? `agy-${slug}` : ''
}

watch(
  () => [props.open, props.node] as const,
  ([isOpen, node]) => {
    if (!isOpen) return
    serverError.value = ''
    showMgmtKey.value = false
    showApiKey.value = false

    if (node) {
      mode.value = (node.mode === 'remote_api' ? 'remote_api' : 'local_docker')
      name.value = node.name
      dockerImage.value = node.docker_image || props.defaultImage || 'routerforme/cliproxyapi:latest'
      containerName.value = node.container_name || ''
      hostIp.value = node.host_ip || '127.0.0.1'
      hostPort.value = String(node.host_port || 8317)
      managementKey.value = ''
      apiKey.value = ''
      proxyUrl.value = node.proxy_url || ''
      strictIsolation.value = node.strict_isolation ?? true
      autoStart.value = false
    } else {
      mode.value = props.dockerAvailable ? 'local_docker' : 'remote_api'
      name.value = ''
      dockerImage.value = props.defaultImage || 'routerforme/cliproxyapi:latest'
      containerName.value = ''
      hostIp.value = mode.value === 'local_docker' ? '127.0.0.1' : ''
      hostPort.value = '8317'
      managementKey.value = ''
      apiKey.value = ''
      proxyUrl.value = ''
      strictIsolation.value = true
      autoStart.value = props.dockerAvailable
    }
  },
  { immediate: true },
)

watch(mode, (next) => {
  if (isEdit.value) return
  if (next === 'local_docker' && !hostIp.value.trim()) {
    hostIp.value = '127.0.0.1'
  } else if (next === 'remote_api' && hostIp.value === '127.0.0.1') {
    hostIp.value = ''
  }
})

const errors = computed(() => {
  const out: Record<string, string> = {}
  if (!name.value.trim()) out.name = '请输入节点名称'
  if (!hostIp.value.trim()) {
    out.hostIp = mode.value === 'local_docker' ? '请输入宿主机绑定 IP' : '请输入远端 IP 或域名'
  }
  const port = Number(hostPort.value)
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    out.hostPort = '端口需在 1 ~ 65535 之间'
  }
  if (mode.value === 'remote_api' && !isEdit.value && !apiKey.value.trim()) {
    out.apiKey = '远程节点需填写推理 API Key'
  }
  return out
})

const valid = computed(() => Object.keys(errors.value).length === 0)

function close(): void {
  if (submitting.value) return
  emit('update:open', false)
}

async function submit(): Promise<void> {
  if (!valid.value || submitting.value) return
  submitting.value = true
  serverError.value = ''
  try {
    const payload: ContainerNodePayload = {
      name: name.value.trim(),
      mode: mode.value,
      host_ip: hostIp.value.trim(),
      host_port: Number(hostPort.value),
      proxy_url: proxyUrl.value.trim(),
      strict_isolation: strictIsolation.value,
    }

    if (mode.value === 'local_docker') {
      payload.docker_image =
        dockerImage.value.trim() || props.defaultImage || 'routerforme/cliproxyapi:latest'
      payload.container_name =
        containerName.value.trim() || (!isEdit.value ? slugifyContainerName(name.value) : undefined)
      if (!isEdit.value) {
        payload.auto_start = autoStart.value
      }
    }

    if (managementKey.value.trim()) {
      payload.management_key = managementKey.value.trim()
    }
    if (apiKey.value.trim()) {
      payload.api_key = apiKey.value.trim()
    }

    let saved: ContainerNode
    if (isEdit.value && props.node) {
      saved = await updateAntigravityNode(props.node.id, payload)
      toast.success('节点配置已更新', saved.name)
    } else {
      saved = await createAntigravityNode(payload)
      toast.success(
        'AntiGravity 节点已创建',
        mode.value === 'local_docker' && autoStart.value
          ? `${saved.name} · 已请求拉起容器`
          : saved.name,
      )
    }
    emit('saved', saved)
    emit('update:open', false)
  } catch (err) {
    serverError.value = toMessage(err, isEdit.value ? '更新节点失败' : '创建节点失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Modal
    :open="open"
    :title="isEdit ? `编辑节点 · ${node?.name ?? ''}` : '新建 AntiGravity 容器 / 远程节点'"
    subtitle="支持宿主机 Docker 自动编排（CLIProxyAPI）或接入任意外部远端推理节点"
    size="lg"
    :persistent="submitting"
    @update:open="emit('update:open', $event)"
  >
    <form class="space-y-4" @submit.prevent="submit">
      <SegmentedTabs
        v-model="mode"
        :items="modeTabs"
        size="md"
        block
      />

      <div
        v-if="mode === 'local_docker' && !dockerAvailable"
        class="flex items-start gap-2 rounded-md border border-warning/35 bg-warning/10 px-3 py-2 text-2xs leading-relaxed text-warning"
      >
        <Icon name="alert" :size="14" class="mt-0.5 shrink-0" />
        <span>
          当前宿主机未检测到可用的 Docker Engine。你仍可先保存本地节点元数据，但拉起容器前需安装并启动 Docker，或切换至「外部远程节点」模式。
        </span>
      </div>

      <p
        v-if="serverError"
        class="flex items-start gap-2 rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-2xs leading-relaxed text-danger"
      >
        <Icon name="alertCircle" :size="14" class="mt-0.5 shrink-0" />
        <span>{{ serverError }}</span>
      </p>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field label="节点名称" required :error="errors.name" hint="用于控制台识别，例如 agy-hk-01">
          <Input v-model="name" placeholder="例如：本机主力容器 #1" />
        </Field>

        <Field
          v-if="mode === 'local_docker'"
          label="容器名称 (Container Name)"
          hint="留空将按节点名自动生成（如 agy-node-1）"
        >
          <Input v-model="containerName" mono placeholder="agy-main" />
        </Field>
        <div v-else class="flex items-end pb-1">
          <Badge tone="info" dot>远程模式直连外部 CLIProxyAPI 实例，不在本机创建容器</Badge>
        </div>
      </div>

      <Field
        v-if="mode === 'local_docker'"
        label="Docker 镜像"
        hint="默认使用官方 routerforme/cliproxyapi:latest 镜像"
      >
        <Input
          v-model="dockerImage"
          mono
          :placeholder="defaultImage || 'routerforme/cliproxyapi:latest'"
        />
      </Field>

      <div class="grid gap-3.5 sm:grid-cols-3">
        <div class="sm:col-span-2">
          <Field
            :label="mode === 'local_docker' ? '宿主机绑定 IP' : '远端节点 IP / 域名'"
            required
            :error="errors.hostIp"
            :hint="
              mode === 'local_docker'
                ? '本机回环填 127.0.0.1（网关访问本地回环自动绕过外部代理）'
                : '支持 IP 或带 http(s):// 的主机地址'
            "
          >
            <Input
              v-model="hostIp"
              mono
              :placeholder="mode === 'local_docker' ? '127.0.0.1' : '192.168.1.50 或 https://agy.example.com'"
            />
          </Field>
        </div>

        <Field label="服务端口" required :error="errors.hostPort" hint="CLIProxyAPI 默认 8317">
          <Input v-model="hostPort" type="number" min="1" max="65535" mono placeholder="8317" />
        </Field>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field
          label="Management Key（远程管理密钥）"
          :hint="
            isEdit
              ? `留空保留原密钥（${node?.masked_management_key || '已配置'}）`
              : mode === 'local_docker'
                ? '留空将自动生成并写入容器 config.yaml'
                : '用于调取 /v0/management/auth-files 与 OAuth 授权链接'
          "
        >
          <div class="flex gap-1.5">
            <div class="relative flex-1">
              <Input
                v-model="managementKey"
                :type="showMgmtKey ? 'text' : 'password'"
                mono
                placeholder="留空自动生成或输入密钥"
                class="pr-8"
              />
              <button
                type="button"
                class="absolute right-2 top-1/2 -translate-y-1/2 text-subtle hover:text-fg"
                @click="showMgmtKey = !showMgmtKey"
              >
                <Icon :name="showMgmtKey ? 'eyeOff' : 'eye'" :size="13" />
              </button>
            </div>
            <Button variant="secondary" size="md" @click="generateMgmtKey">生成</Button>
          </div>
        </Field>

        <Field
          label="推理 API Key（/v1 接口凭证）"
          :required="mode === 'remote_api' && !isEdit"
          :error="errors.apiKey"
          :hint="
            isEdit
              ? `留空保留原密钥（${node?.masked_api_key || '已配置'}）`
              : mode === 'local_docker'
                ? '留空将自动生成并注入容器 api-keys'
                : '必填：远端节点的 /v1/chat/completions Bearer Key'
          "
        >
          <div class="flex gap-1.5">
            <div class="relative flex-1">
              <Input
                v-model="apiKey"
                :type="showApiKey ? 'text' : 'password'"
                mono
                placeholder="留空自动生成或输入 sk-..."
                class="pr-8"
              />
              <button
                type="button"
                class="absolute right-2 top-1/2 -translate-y-1/2 text-subtle hover:text-fg"
                @click="showApiKey = !showApiKey"
              >
                <Icon :name="showApiKey ? 'eyeOff' : 'eye'" :size="13" />
              </button>
            </div>
            <Button variant="secondary" size="md" @click="generateInferenceKey">生成</Button>
          </div>
        </Field>
      </div>

      <Field
        label="独立上游出口代理 (proxy_url)"
        hint="强隔离下每个容器建议绑定唯一代理 IP（http://user:pass@ip:port 或 socks5://...）；本机容器将写入 config.yaml 与环境"
      >
        <Input v-model="proxyUrl" mono placeholder="http://127.0.0.1:7890 或 socks5://..." />
      </Field>

      <div class="panel-inset flex flex-col gap-2 px-3.5 py-2.5">
        <Switch
          v-model="strictIsolation"
          label="启用 1容器 : 1独立IP : 1独立设备指纹 强隔离"
          description="禁止与其他节点共用同一 ProxyURL，并为容器独立分配 Qimei36、MachineID、Hostname、MAC 与 User-Agent"
        />
        <p
          v-if="strictIsolation && !proxyUrl.trim()"
          class="text-2xs text-warning"
        >
          提示：当前尚未填写 proxy_url，多个本机容器可能共走宿主机默认公网 IP；建议为每个容器填写独立的 ProxyURL。
        </p>
      </div>

      <div
        v-if="mode === 'local_docker' && !isEdit"
        class="panel-inset flex items-center justify-between px-3.5 py-2.5"
      >
        <Switch
          v-model="autoStart"
          :disabled="!dockerAvailable"
          label="创建后立即拉取镜像并启动容器"
          description="首次拉取镜像可能耗时 1~3 分钟，客户端已自动启用 300s 长超时保护"
        />
      </div>
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
        {{ isEdit ? '保存节点修改' : mode === 'local_docker' && autoStart ? '创建并启动容器' : '创建节点' }}
      </Button>
    </template>
  </Modal>
</template>
