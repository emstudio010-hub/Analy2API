<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import Select from '@/components/ui/Select.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { filterRulesApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { FilterRule, FilterRuleKind, FilterRulePayload } from '@/api/types'
import { useToastStore } from '@/stores/toast'

const props = defineProps<{
  open: boolean
  rule: FilterRule | null
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'saved'): void
}>()

const toast = useToastStore()

const isEdit = computed(() => props.rule !== null)

const name = ref('')
const kind = ref<FilterRuleKind>('replace')
const pattern = ref('')
const replacement = ref('')
const isRegex = ref(false)
const caseSensitive = ref(false)
const wordBoundary = ref(true)
const scope = ref('all_injected')
const priority = ref('100')
const enabled = ref(true)
const description = ref('')

const submitting = ref(false)
const serverError = ref('')

const kindOptions: Array<{
  value: FilterRuleKind
  label: string
  hint: string
}> = [
  { value: 'prune', label: 'Prune 删减', hint: '直接移除冗余或高危特征片段（通常替换为空）' },
  { value: 'rewrite', label: 'Rewrite 重写', hint: '整句/标识模板重写为 WorkBuddy 语境' },
  { value: 'replace', label: 'Replace 同义词替换', hint: '高危安全词汇替换为中性工程词汇' },
]

const scopeOptions = [
  { label: '全部注入上下文 (all_injected)', value: 'all_injected' },
  { label: '仅系统提示词 (system)', value: 'system' },
  { label: '仅工具 Schema 描述 (tool)', value: 'tool' },
  { label: '仅用户 Harness 注入块 (user)', value: 'user' },
]

const effectiveScopeOptions = computed(() => {
  if (!scope.value || scopeOptions.some((o) => o.value === scope.value)) return scopeOptions
  return [...scopeOptions, { label: scope.value, value: scope.value }]
})

const errors = computed(() => {
  const out: Record<string, string> = {}
  if (!name.value.trim()) out.name = '请输入规则名称'
  if (!pattern.value) out.pattern = '请输入匹配模式或关键词'
  if (isRegex.value && pattern.value) {
    try {
      new RegExp(pattern.value)
    } catch {
      // Go regexp 与 JS RegExp 语法略有差异（如 (?i) 内联修饰），提示但不强行拦住 Go 正则
      if (!pattern.value.startsWith('(?')) {
        out.pattern = '正则表达式语法似乎有误，请检查括号与转义'
      }
    }
  }
  if (replacement.value.includes('\u200b')) {
    out.replacement = '严禁在替换文本中使用 \\u200b 零宽字符'
  }
  const prio = Number(priority.value)
  if (!Number.isFinite(prio)) out.priority = '优先级必须是数字'
  return out
})

const valid = computed(() => Object.keys(errors.value).length === 0)

watch(
  () => [props.open, props.rule] as const,
  () => {
    if (!props.open) return
    const r = props.rule
    name.value = r?.name ?? ''
    kind.value = r?.kind ?? 'replace'
    pattern.value = r?.pattern ?? ''
    replacement.value = r?.replacement ?? ''
    isRegex.value = r?.is_regex ?? false
    caseSensitive.value = r?.case_sensitive ?? false
    wordBoundary.value = r?.word_boundary ?? true
    const rawScope = r?.scope || 'all_injected'
    scope.value = rawScope === 'user_harness' ? 'user' : rawScope
    priority.value = String(r?.priority ?? 100)
    enabled.value = r?.enabled ?? true
    description.value = r?.description ?? ''
    serverError.value = ''
  },
  { immediate: true },
)

function selectKind(next: FilterRuleKind): void {
  kind.value = next
  if (!isEdit.value && next === 'prune' && !replacement.value) {
    wordBoundary.value = false
  }
}

function close(): void {
  emit('update:open', false)
}

async function submit(): Promise<void> {
  if (!valid.value || submitting.value) return
  submitting.value = true
  serverError.value = ''
  try {
    const payload: FilterRulePayload = {
      name: name.value.trim(),
      kind: kind.value,
      pattern: pattern.value,
      replacement: replacement.value,
      is_regex: isRegex.value,
      case_sensitive: caseSensitive.value,
      word_boundary: wordBoundary.value,
      scope: scope.value || 'all_injected',
      priority: Number(priority.value) || 100,
      enabled: enabled.value,
      description: description.value.trim(),
    }

    if (isEdit.value && props.rule) {
      await filterRulesApi.update(props.rule.id, payload)
      toast.success('风控规则已更新', payload.name)
    } else {
      await filterRulesApi.create(payload)
      toast.success('风控规则已创建', payload.name)
    }
    emit('saved')
    close()
  } catch (err) {
    serverError.value = toMessage(err, isEdit.value ? '更新规则失败' : '创建规则失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Modal
    :open="open"
    :title="isEdit ? '编辑风控规则' : '新建风控规则'"
    subtitle="所有规则对全部下游客户端默认强制生效，严禁包含 \\u200b 零宽字符"
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

      <Field label="规则类型" required>
        <div class="grid gap-2 sm:grid-cols-3">
          <button
            v-for="item in kindOptions"
            :key="item.value"
            type="button"
            class="rounded-md border px-2.5 py-2 text-left transition"
            :class="
              kind === item.value
                ? 'border-accent bg-accent/10'
                : 'border-line bg-raised/40 hover:border-line-strong'
            "
            @click="selectKind(item.value)"
          >
            <div class="flex items-center gap-1.5">
              <span
                class="h-1.5 w-1.5 rounded-full"
                :class="kind === item.value ? 'bg-accent' : 'bg-subtle'"
              />
              <span class="text-[13px] font-medium text-fg">{{ item.label }}</span>
            </div>
            <p class="mt-0.5 text-2xs leading-4 text-subtle">{{ item.hint }}</p>
          </button>
        </div>
      </Field>

      <div class="grid gap-3.5 sm:grid-cols-3">
        <div class="sm:col-span-2">
          <Field label="规则名称" required :error="errors.name">
            <Input v-model="name" placeholder="例如：屏蔽 Claude Code 标识" maxlength="120" />
          </Field>
        </div>
        <Field label="优先级" :error="errors.priority" hint="数值越小越先匹配执行">
          <Input v-model="priority" type="number" step="1" />
        </Field>
      </div>

      <Field
        label="匹配模式 (pattern)"
        required
        :error="errors.pattern"
        :hint="isRegex ? 'Go RE2 正则表达式语法' : '字面量文本或词组'"
      >
        <Textarea
          v-model="pattern"
          :rows="2"
          mono
          :invalid="Boolean(errors.pattern)"
          placeholder="输入要删减/重写的文本或正则表达式"
        />
      </Field>

      <Field
        label="替换为 (replacement)"
        :error="errors.replacement"
        hint="Prune 类型留空表示直接删除；禁止包含 \\u200b 零宽字符"
      >
        <Textarea
          v-model="replacement"
          :rows="2"
          mono
          :invalid="Boolean(errors.replacement)"
          placeholder="留空表示删减为空串"
        />
      </Field>

      <div class="grid gap-3.5 sm:grid-cols-2">
        <Field label="作用域 (scope)" hint="默认仅清洗注入上下文，绝不改纯用户提问">
          <Select v-model="scope" :options="effectiveScopeOptions" />
        </Field>
        <Field label="说明" hint="选填，备注这条规则应对的风控场景">
          <Input v-model="description" placeholder="例如：防止上游 code=11128 拦截" maxlength="500" />
        </Field>
      </div>

      <div class="grid gap-2.5 rounded-md border border-line bg-raised/40 p-3 sm:grid-cols-2">
        <Checkbox
          v-model="isRegex"
          label="启用正则表达式 (is_regex)"
          description="按正则引擎解析 pattern"
        />
        <Checkbox
          v-model="caseSensitive"
          label="区分大小写 (case_sensitive)"
          description="关闭时默认忽略大小写"
        />
        <Checkbox
          v-model="wordBoundary"
          label="全词边界匹配 (word_boundary)"
          description="避免误伤长单词内部子串（非正则时生效）"
        />
        <Checkbox
          v-model="enabled"
          label="立即启用规则"
          description="关闭后保留词条但不参与清洗"
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
        {{ isEdit ? '保存修改' : '创建规则' }}
      </Button>
    </template>
  </Modal>
</template>
