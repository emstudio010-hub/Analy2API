<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { groupsApi, updateGroupPrompt } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { Group, GroupPayload, GroupToolObfMode, PromptInjectMode, UpstreamPlatform } from '@/api/types'
import Button from '@/components/ui/Button.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import PlatformPicker from '@/components/ui/PlatformPicker.vue'
import Select from '@/components/ui/Select.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { useToastStore } from '@/stores/toast'
import { ALL_PLATFORMS, DEFAULT_PLATFORM } from '@/utils/platforms'

const props = defineProps<{
  open: boolean
  group: Group | null
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'saved'): void
}>()

const toast = useToastStore()

const isEdit = computed(() => props.group !== null)

const platform = ref<UpstreamPlatform>(DEFAULT_PLATFORM)
const name = ref('')
const description = ref('')
const multiplier = ref('1')
const status = ref('active')
const promptInjectMode = ref<PromptInjectMode>('append_global')
const systemPrompt = ref('')
const toolObfMode = ref<GroupToolObfMode>('inherit')
const toolObfPrefix = ref('')
const submitting = ref(false)
const serverError = ref('')

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '禁用', value: 'disabled' },
]

const injectModeOptions: Array<{ label: string; value: PromptInjectMode }> = [
  { label: '叠加在全局底线后 (append_global · 默认)', value: 'append_global' },
  { label: '覆盖全局底线 (override_global)', value: 'override_global' },
  { label: '强制接管并剥离客户端原 System (replace_all)', value: 'replace_all' },
  { label: '关闭前置系统提示词注入 (off)', value: 'off' },
]

const toolObfModeOptions: Array<{ label: string; value: GroupToolObfMode }> = [
  { label: '跟随全局策略 (inherit · 默认)', value: 'inherit' },
  { label: '强制关闭工具名混淆 (off)', value: 'off' },
  { label: '强随机双字母混淆 (hash · 每周/会话)', value: 'hash' },
  { label: '语义中性化别名 (alias · exec_task 等)', value: 'alias' },
  { label: '自定义统一前缀 (prefix · 如 wb_)', value: 'prefix' },
]

const errors = computed(() => {
  const out: Record<string, string> = {}
  if (!platform.value) out.platform = '请选择分组所属上游平台'
  if (!name.value.trim()) out.name = '请输入分组名称'
  const parsed = Number(multiplier.value)
  if (!Number.isFinite(parsed) || parsed <= 0) out.multiplier = '倍率必须大于 0'
  return out
})

const valid = computed(() => Object.keys(errors.value).length === 0)

function onPlatformChange(value: string): void {
  const next = ALL_PLATFORMS.find((item) => item.value === value)
  if (!next || next.value === platform.value) return
  platform.value = next.value
}

watch(
  () => [props.open, props.group] as const,
  () => {
    if (!props.open) return
    platform.value = props.group ? props.group.platform || DEFAULT_PLATFORM : DEFAULT_PLATFORM
    name.value = props.group?.name ?? ''
    description.value = props.group?.description ?? ''
    multiplier.value = String(props.group?.rate_multiplier ?? 1)
    status.value = props.group?.status ?? 'active'
    promptInjectMode.value = (props.group?.prompt_inject_mode as PromptInjectMode) || 'append_global'
    systemPrompt.value = props.group?.system_prompt ?? ''
    toolObfMode.value = (props.group?.tool_obf_mode as GroupToolObfMode) || 'inherit'
    toolObfPrefix.value = props.group?.tool_obf_prefix ?? ''
    serverError.value = ''
  },
  { immediate: true },
)

function close(): void {
  emit('update:open', false)
}

async function submit(): Promise<void> {
  if (!valid.value || submitting.value) return
  const selectedPlatform = platform.value
  if (!selectedPlatform) return
  submitting.value = true
  serverError.value = ''
  try {
    const payload: GroupPayload = {
      name: name.value.trim(),
      description: description.value.trim(),
      platform: selectedPlatform,
      rate_multiplier: Number(multiplier.value),
      status: status.value,
      prompt_inject_mode: promptInjectMode.value,
      system_prompt: systemPrompt.value,
      tool_obf_mode: toolObfMode.value,
      tool_obf_prefix: toolObfPrefix.value.trim(),
    }
    let savedGroup: Group
    if (isEdit.value && props.group) {
      savedGroup = await groupsApi.update(props.group.id, payload)
    } else {
      savedGroup = await groupsApi.create(payload)
    }

    // 若后端 /groups CRUD 未内联落盘提示词字段，同步补调一次 /prompts/groups/:id 确保一致
    if (
      savedGroup.prompt_inject_mode !== promptInjectMode.value ||
      (systemPrompt.value && savedGroup.system_prompt !== systemPrompt.value)
    ) {
      try {
        await updateGroupPrompt(savedGroup.id, {
          prompt_inject_mode: promptInjectMode.value,
          system_prompt: systemPrompt.value,
          prompt_template_id: props.group?.prompt_template_id ?? null,
        })
      } catch {
        /* 若 /groups 已处理或接口尚未挂载则静默忽略 */
      }
    }

    toast.success(isEdit.value ? '分组已更新' : '分组已创建', payload.name)
    emit('saved')
    close()
  } catch (err) {
    serverError.value = toMessage(err, isEdit.value ? '更新分组失败' : '创建分组失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Modal
    :open="open"
    :title="isEdit ? '编辑路由分组' : '新建路由分组'"
    subtitle="分组隔离四大上游平台账号池、计费倍率与前置系统提示词注入策略"
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

      <Field
        label="所属上游平台"
        :required="!isEdit"
        :error="errors.platform"
        :hint="isEdit ? '分组创建后不可切换平台，以防跨平台账号混绑' : '选择该分组调度的目标上游平台'"
      >
        <PlatformPicker
          :model-value="platform"
          :disabled="isEdit"
          :invalid="Boolean(errors.platform)"
          @update:model-value="onPlatformChange"
        />
      </Field>

      <div class="grid gap-3.5 sm:grid-cols-3">
        <div class="sm:col-span-2">
          <Field label="分组名称" required :error="errors.name">
            <Input
              v-model="name"
              placeholder="例如：AGY 旗舰池 / WorkBuddy 主力组"
              maxlength="100"
            />
          </Field>
        </div>

        <Field label="状态" hint="禁用后组内密钥暂停调度">
          <Select v-model="status" :options="statusOptions" />
        </Field>
      </div>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field label="计费倍率" :error="errors.multiplier" hint="在账号自身倍率之上再乘一次">
          <Input v-model="multiplier" type="number" min="0.01" step="0.01" />
        </Field>

        <Field label="前置提示词注入模式" hint="控制该组流量如何注入系统提示词">
          <Select v-model="promptInjectMode" :options="injectModeOptions" />
        </Field>
      </div>

      <Field
        label="分组专属系统提示词（选填）"
        hint="更细粒度的模板绑定与合成预览可在「前置提示词注入」页面配置"
      >
        <Textarea
          v-model="systemPrompt"
          :rows="3"
          mono
          :disabled="promptInjectMode === 'off'"
          placeholder="可选：填写该分组专属的 System Prompt（支持 {{date}} / {{model}} / {{platform}} / {{group}}）"
        />
      </Field>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field label="分组工具名混淆策略" hint="覆盖全局工具混淆设置（支持独立开启/关闭或换模式）">
          <Select v-model="toolObfMode" :options="toolObfModeOptions" />
        </Field>

        <Field label="分组工具混淆前缀（可选）" hint="留空则继承全局 Prefix（如 ess_t_）">
          <Input
            v-model="toolObfPrefix"
            mono
            :disabled="toolObfMode === 'off'"
            placeholder="留空继承全局，例如 grp_a_"
          />
        </Field>
      </div>

      <Field label="分组说明" hint="选填，备注该分组对应的团队、客户端或代理策略">
        <Input v-model="description" placeholder="可选备注说明" maxlength="255" />
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
        {{ isEdit ? '保存修改' : '创建分组' }}
      </Button>
    </template>
  </Modal>
</template>
