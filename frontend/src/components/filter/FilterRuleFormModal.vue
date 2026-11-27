<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { toMessage } from '@/api/client'
import { filterRulesApi } from '@/api/endpoints'
import type { FilterRule, FilterRuleKind, FilterRulePayload } from '@/api/types'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import Field from '@/components/ui/Field.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import Textarea from '@/components/ui/Textarea.vue'
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
const allInjectedScope = ref(true)
const subScopes = ref<string[]>(['system', 'tool', 'user'])
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
  { value: 'prune', label: 'Prune 删减', hint: '直接移除冗余计费头或敏感客户端签名（通常替换为空）' },
  { value: 'rewrite', label: 'Rewrite 重写', hint: '整句/标识模板平滑改写为通用工程语境' },
  { value: 'replace', label: 'Replace 同义替换', hint: '高危风控敏感词替换为中性工程词（零 \\u200b）' },
]

const subScopeItems = [
  { value: 'system', label: 'System / Instructions', desc: '系统与开发者指令层' },
  { value: 'tool', label: 'Tools Schema', desc: '工具定义与参数描述' },
  { value: 'user', label: 'User Harness 注入块', desc: '仅包裹块，绝不改纯用户提问' },
]

function hydrateScope(raw?: string | null): void {
  const s = (raw ?? 'all_injected').trim().toLowerCase()
  if (!s || s === 'all' || s === 'all_injected') {
    allInjectedScope.value = true
    subScopes.value = ['system', 'tool', 'user']
    return
  }
  const parts = s
    .split(',')
    .map((p) => p.trim())
    .filter(Boolean)
    .map((p) => (p === 'user_harness' ? 'user' : p === 'tools' ? 'tool' : p))
  if (parts.includes('all_injected') || parts.includes('all') || parts.length === 0) {
    allInjectedScope.value = true
    subScopes.value = ['system', 'tool', 'user']
  } else {
    allInjectedScope.value = false
    subScopes.value = Array.from(new Set(parts))
  }
}

function toggleSubScope(target: string): void {
  const idx = subScopes.value.indexOf(target)
  if (idx >= 0) {
    if (subScopes.value.length > 1) {
      subScopes.value.splice(idx, 1)
    }
  } else {
    subScopes.value.push(target)
  }
}

const serializedScope = computed(() => {
  if (allInjectedScope.value || subScopes.value.length >= 3) {
    return 'all_injected'
  }
  return subScopes.value.join(',')
})

const errors = computed(() => {
  const out: Record<string, string> = {}
  if (!name.value.trim()) out.name = '请输入规则名称'
  if (!pattern.value) out.pattern = '请输入匹配模式或关键词'
  if (isRegex.value && pattern.value) {
    try {
      new RegExp(pattern.value)
    } catch {
      if (!pattern.value.startsWith('(?')) {
        out.pattern = '正则表达式语法似乎有误，请检查括号与转义'
      }
    }
  }
  if (replacement.value.includes('\u200b')) {
    out.replacement = '严禁在替换文本中使用 \\u200b 零宽字符'
  }
  const rawPrio = String(priority.value).trim()
  if (rawPrio === '' || !Number.isFinite(Number(rawPrio))) {
    out.priority = '优先级必须是有效整数（支持 0）'
  }
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
    hydrateScope(r?.scope)
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
    const parsedPriority = Math.trunc(Number(String(priority.value).trim()))
    const payload: FilterRulePayload = {
      name: name.value.trim(),
      kind: kind.value,
      pattern: pattern.value,
      replacement: replacement.value,
      is_regex: isRegex.value,
      case_sensitive: caseSensitive.value,
      word_boundary: wordBoundary.value,
      scope: serializedScope.value,
      priority: parsedPriority,
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
    :title="isEdit ? '编辑风控拦截规则' : '新建风控拦截规则'"
    subtitle="所有规则按 Prune → Rewrite → Replace 顺序热生效，严禁包含 \\u200b 零宽字符"
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
            <Input v-model="name" placeholder="例如：prune-claude-billing-header" maxlength="120" />
          </Field>
        </div>
        <Field label="优先级" :error="errors.priority" hint="越小越先执行（支持 0）">
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
        hint="Prune 类型留空表示直接物理删空；禁止包含 \\u200b 零宽字符"
      >
        <Textarea
          v-model="replacement"
          :rows="2"
          mono
          :invalid="Boolean(errors.replacement)"
          placeholder="留空表示删减为空串"
        />
      </Field>

      <!-- 精细化多选作用域 -->
      <Field label="生效作用域 (scope)" hint="仅清洗客户端注入上下文与容器标签，绝不篡改纯用户提问与代码块">
        <div class="space-y-2 rounded-md border border-line bg-raised/35 p-2.5">
          <Checkbox
            v-model="allInjectedScope"
            label="全部注入上下文 (all_injected)"
            description="同时覆盖 System 指令、Tools 描述与客户端 User Harness 包裹块"
          />
          <div v-if="!allInjectedScope" class="grid gap-2 pt-1 sm:grid-cols-3">
            <button
              v-for="sc in subScopeItems"
              :key="sc.value"
              type="button"
              class="rounded border px-2.5 py-1.5 text-left transition"
              :class="
                subScopes.includes(sc.value)
                  ? 'border-accent/60 bg-accent/10 text-fg'
                  : 'border-line bg-surface text-muted hover:border-line-strong'
              "
              @click="toggleSubScope(sc.value)"
            >
              <div class="flex items-center justify-between text-xs font-medium">
                <span>{{ sc.label }}</span>
                <Icon
                  v-if="subScopes.includes(sc.value)"
                  name="check"
                  :size="12"
                  class="text-accent"
                />
              </div>
              <div class="mt-0.5 text-2xs text-subtle">{{ sc.desc }}</div>
            </button>
          </div>
        </div>
      </Field>

      <Field label="规则说明" hint="选填，备注该条规则拦截的客户端特征或误报场景">
        <Input v-model="description" placeholder="例如：剥离客户端自动注入的仓库反馈链接与计费头" maxlength="500" />
      </Field>

      <div class="grid gap-2.5 rounded-md border border-line bg-raised/40 p-3 sm:grid-cols-2">
        <Checkbox
          v-model="isRegex"
          label="启用正则表达式 (is_regex)"
          description="按 Go RE2 正则引擎编译匹配"
        />
        <Checkbox
          v-model="caseSensitive"
          label="区分大小写 (case_sensitive)"
          description="关闭时默认忽略大小写"
        />
        <Checkbox
          v-model="wordBoundary"
          label="全词边界匹配 (word_boundary)"
          description="避免如 kill 误伤 skill / killed 等正常单词（非正则时生效）"
        />
        <Checkbox
          v-model="enabled"
          label="保存后立即启用"
          description="关闭后保留词条但不参与热清洗"
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
