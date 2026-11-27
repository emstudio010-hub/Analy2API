<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { toMessage } from '@/api/client'
import { filterRulesApi } from '@/api/endpoints'
import type { FilterRule, FilterRuleKind } from '@/api/types'
import FilterRuleFormModal from '@/components/filter/FilterRuleFormModal.vue'
import LiveInterceptSandbox from '@/components/filter/LiveInterceptSandbox.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import MetricTile from '@/components/ui/MetricTile.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import Switch from '@/components/ui/Switch.vue'
import { useToastStore } from '@/stores/toast'

const toast = useToastStore()

// 工作台视图切换：全量视图 / 实时拦截接续沙盒 / 规则词库管理
const pageSection = ref<string>('all')
const pageSectionTabs = computed(() => [
  { value: 'all', label: '沙盒与规则全览', icon: 'layers' as const },
  { value: 'sandbox', label: '拦截与接续实时沙盒', icon: 'zap' as const },
  { value: 'rules', label: '三级风控规则词库', icon: 'shield' as const, badge: items.value.length },
])

const sandboxRef = ref<InstanceType<typeof LiveInterceptSandbox> | null>(null)

// ---- 规则库状态 ----
const items = ref<FilterRule[]>([])
const loading = ref(true)
const error = ref('')

type TabKind = 'all' | FilterRuleKind
const activeTab = ref<string>('all')
const keyword = ref('')

const formOpen = ref(false)
const editing = ref<FilterRule | null>(null)
const togglingIds = ref<Set<number>>(new Set())
const deleteTarget = ref<FilterRule | null>(null)
const deleting = ref(false)
const resetOpen = ref(false)
const resetting = ref(false)

const computedStats = computed(() => ({
  enabled: items.value.filter((r) => r.enabled).length,
  prune: items.value.filter((r) => r.kind === 'prune').length,
  rewrite: items.value.filter((r) => r.kind === 'rewrite').length,
  replace: items.value.filter((r) => r.kind === 'replace').length,
}))

const tabItems = computed(() => [
  { value: 'all', label: '全部规则', badge: items.value.length },
  { value: 'prune', label: 'Prune 删减', badge: computedStats.value.prune },
  { value: 'rewrite', label: 'Rewrite 重写', badge: computedStats.value.rewrite },
  { value: 'replace', label: 'Replace 同义替换', badge: computedStats.value.replace },
])

function kindOrder(kind: string): number {
  if (kind === 'prune') return 0
  if (kind === 'rewrite') return 1
  if (kind === 'replace') return 2
  return 3
}

const filteredItems = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return items.value
    .filter((rule) => {
      if (activeTab.value !== 'all' && rule.kind !== (activeTab.value as TabKind)) return false
      if (!q) return true
      return (
        rule.name.toLowerCase().includes(q) ||
        rule.pattern.toLowerCase().includes(q) ||
        rule.replacement.toLowerCase().includes(q) ||
        (rule.description ?? '').toLowerCase().includes(q) ||
        (rule.scope ?? '').toLowerCase().includes(q)
      )
    })
    .sort((a, b) => {
      const ka = kindOrder(a.kind)
      const kb = kindOrder(b.kind)
      if (ka !== kb) return ka - kb
      if (a.priority !== b.priority) return a.priority - b.priority
      return a.id - b.id
    })
})

function kindBadge(kind: string): {
  tone: 'warning' | 'info' | 'success' | 'neutral'
  label: string
} {
  if (kind === 'prune') return { tone: 'warning', label: 'Prune 删减' }
  if (kind === 'rewrite') return { tone: 'info', label: 'Rewrite 重写' }
  if (kind === 'replace') return { tone: 'success', label: 'Replace 替换' }
  return { tone: 'neutral', label: kind }
}

function scopeLabel(scope: string): string {
  const parts = (scope || 'all_injected')
    .split(',')
    .map((p) => p.trim())
    .filter(Boolean)
  if (parts.length === 0) return '全部注入上下文'
  return parts
    .map((part) => {
      switch (part) {
        case 'all_injected':
        case 'all':
          return '全部注入上下文'
        case 'system':
          return '仅 System'
        case 'tool':
        case 'tools':
          return '仅 Tools'
        case 'user':
        case 'user_harness':
          return '用户 Harness 块'
        default:
          return part
      }
    })
    .join(' + ')
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await filterRulesApi.list()
    items.value = Array.isArray(res.items) ? res.items : []
  } catch (err) {
    error.value = toMessage(err, '加载风控规则失败')
    items.value = []
  } finally {
    loading.value = false
  }
}

async function handleRuleSaved(): Promise<void> {
  await load()
  sandboxRef.value?.refreshPreview()
}

onMounted(() => {
  void load()
})

function openCreate(): void {
  editing.value = null
  formOpen.value = true
}

function openEdit(rule: FilterRule): void {
  editing.value = rule
  formOpen.value = true
}

async function toggleEnabled(rule: FilterRule): Promise<void> {
  if (togglingIds.value.has(rule.id)) return
  togglingIds.value.add(rule.id)
  const next = !rule.enabled
  try {
    const updated = await filterRulesApi.update(rule.id, { enabled: next })
    const idx = items.value.findIndex((item) => item.id === rule.id)
    if (idx >= 0) items.value[idx] = updated
    toast.success(next ? '已启用规则' : '已停用规则', rule.name)
    sandboxRef.value?.refreshPreview()
  } catch (err) {
    toast.error('切换规则状态失败', toMessage(err))
  } finally {
    togglingIds.value.delete(rule.id)
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleting.value) return
  deleting.value = true
  try {
    await filterRulesApi.remove(target.id)
    toast.success('规则已删除', target.name)
    deleteTarget.value = null
    await load()
    sandboxRef.value?.refreshPreview()
  } catch (err) {
    toast.error('删除规则失败', toMessage(err))
  } finally {
    deleting.value = false
  }
}

async function confirmReset(): Promise<void> {
  if (resetting.value) return
  resetting.value = true
  try {
    const res = await filterRulesApi.reset()
    toast.success('已恢复内置默认规则库', `共 ${res.total} 条内置规则`)
    resetOpen.value = false
    await load()
    sandboxRef.value?.refreshPreview()
  } catch (err) {
    toast.error('恢复默认规则失败', toMessage(err))
  } finally {
    resetting.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部指标磁贴 -->
    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <MetricTile
        label="已启用 / 词库总规则"
        :value="`${computedStats.enabled} / ${items.length}`"
        hint="全入口强制生效 · 绝不篡改纯用户提问与代码"
        icon="shield"
        tone="accent"
      />
      <MetricTile
        label="Prune 物理删减规则"
        :value="computedStats.prune"
        hint="剥离计费请求头与跨平台容器遥测指纹"
        icon="trash"
        tone="warning"
      />
      <MetricTile
        label="Rewrite 身份模板重写"
        :value="computedStats.rewrite"
        hint="冲突客户端身份平滑归并为通用工程语境"
        icon="edit"
        tone="info"
      />
      <MetricTile
        label="Replace 安全同义替换"
        :value="computedStats.replace"
        hint="零 \\u200b 零宽字符 · 词边界保护零误伤"
        icon="checkCircle"
        tone="success"
      />
    </div>

    <!-- 顶层分区切换栏 -->
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <SegmentedTabs v-model="pageSection" size="md" :items="pageSectionTabs" />

      <div class="flex flex-wrap items-center gap-2">
        <Button variant="secondary" size="sm" :loading="loading" @click="load">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新词库
        </Button>
        <Button variant="secondary" size="sm" @click="resetOpen = true">
          <template #leading><Icon name="rotate" :size="13" /></template>
          恢复内置默认规则
        </Button>
        <Button variant="primary" size="sm" @click="openCreate">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建规则
        </Button>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
    >
      <Icon name="alertCircle" :size="13" />
      <span>{{ error }}</span>
      <Button variant="ghost" size="xs" class="ml-auto" @click="load">重试</Button>
    </p>

    <!-- 1. 拦截与接续实时沙盒（置顶直达，支持预设场景与三栏并排对比） -->
    <LiveInterceptSandbox
      v-show="pageSection === 'all' || pageSection === 'sandbox'"
      ref="sandboxRef"
    />

    <!-- 2. 三级风控与容器脱敏规则词库表格 -->
    <div v-if="pageSection === 'all' || pageSection === 'rules'" class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-2.5">
        <SegmentedTabs v-model="activeTab" :items="tabItems" />

        <div class="flex items-center gap-2">
          <div class="w-full min-w-[14rem] sm:w-72">
            <Input v-model="keyword" size="sm" placeholder="搜索规则名 / 正则模式 / 替换词 / 作用域…" />
          </div>
          <Button
            v-if="keyword.trim() || activeTab !== 'all'"
            variant="ghost"
            size="xs"
            @click="
              () => {
                keyword = ''
                activeTab = 'all'
              }
            "
          >
            重置筛选
          </Button>
        </div>
      </div>

      <DataTable
        :columns="8"
        :loading="loading"
        :empty="filteredItems.length === 0"
        :skeleton-rows="6"
        min-width="1160px"
        hoverable
      >
        <template #head>
          <th class="th w-16">启用</th>
          <th class="th w-16 text-right">优先级</th>
          <th class="th w-32">类型</th>
          <th class="th">规则名称</th>
          <th class="th">匹配模式 (Pattern)</th>
          <th class="th">替换为 (Replacement)</th>
          <th class="th w-40">作用域 / 标志</th>
          <th class="th w-24 text-right">操作</th>
        </template>

        <template #empty>
          <EmptyState
            icon="shield"
            :title="keyword.trim() || activeTab !== 'all' ? '没有匹配的风控规则' : '规则库为空'"
            :description="
              keyword.trim() || activeTab !== 'all'
                ? '清空搜索词或切换到「全部规则」标签。'
                : '可新建自定义规则，或点击「恢复内置默认规则」载入预置词库。'
            "
          >
            <Button
              v-if="keyword.trim() || activeTab !== 'all'"
              variant="secondary"
              size="sm"
              @click="
                () => {
                  keyword = ''
                  activeTab = 'all'
                }
              "
            >
              重置筛选
            </Button>
            <Button v-else variant="primary" size="sm" @click="resetOpen = true">
              恢复内置默认规则
            </Button>
          </EmptyState>
        </template>

        <template #body>
          <tr
            v-for="rule in filteredItems"
            :key="rule.id"
            :class="!rule.enabled ? 'opacity-60' : ''"
          >
            <td class="td">
              <Switch
                size="sm"
                :model-value="rule.enabled"
                :disabled="togglingIds.has(rule.id)"
                @update:model-value="toggleEnabled(rule)"
              />
            </td>

            <td class="td tnum text-right font-mono text-muted">{{ rule.priority }}</td>

            <td class="td">
              <Badge :tone="kindBadge(rule.kind).tone">{{ kindBadge(rule.kind).label }}</Badge>
            </td>

            <td class="td max-w-[15rem]">
              <div class="flex flex-col gap-0.5">
                <div class="flex items-center gap-1.5">
                  <span class="truncate font-medium text-fg" :title="rule.name">{{ rule.name }}</span>
                  <Badge v-if="rule.is_builtin" tone="neutral">内置</Badge>
                </div>
                <span
                  v-if="rule.description"
                  class="truncate text-2xs text-subtle"
                  :title="rule.description"
                >
                  {{ rule.description }}
                </span>
              </div>
            </td>

            <td class="td max-w-[18rem]">
              <code
                class="block truncate rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-fg"
                :title="rule.pattern"
              >
                {{ rule.pattern }}
              </code>
            </td>

            <td class="td max-w-[16rem]">
              <Badge v-if="!rule.replacement" tone="neutral">（物理删空）</Badge>
              <code
                v-else
                class="block truncate rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-2xs text-success"
                :title="rule.replacement"
              >
                {{ rule.replacement }}
              </code>
            </td>

            <td class="td">
              <div class="flex flex-col gap-1">
                <span class="text-2xs text-muted">{{ scopeLabel(rule.scope) }}</span>
                <div class="flex flex-wrap gap-1">
                  <Badge v-if="rule.is_regex" tone="info">正则</Badge>
                  <Badge v-if="rule.case_sensitive" tone="neutral">区分大小写</Badge>
                  <Badge v-if="rule.word_boundary && !rule.is_regex" tone="neutral">词边界</Badge>
                </div>
              </div>
            </td>

            <td class="td">
              <div class="flex items-center justify-end gap-1">
                <Button variant="ghost" size="xs" icon title="编辑规则" @click="openEdit(rule)">
                  <Icon name="edit" :size="13" />
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  icon
                  class="hover:bg-danger/10 hover:text-danger"
                  title="删除规则"
                  @click="deleteTarget = rule"
                >
                  <Icon name="trash" :size="13" />
                </Button>
              </div>
            </td>
          </tr>
        </template>
      </DataTable>
    </div>

    <FilterRuleFormModal v-model:open="formOpen" :rule="editing" @saved="handleRuleSaved" />

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除风控规则"
      :message="`确定删除规则「${deleteTarget?.name ?? ''}」吗？删除后立即从热更新词库移除。`"
      confirm-text="删除"
      tone="danger"
      :loading="deleting"
      @update:open="deleteTarget = null"
      @confirm="confirmDelete"
    />

    <ConfirmDialog
      :open="resetOpen"
      title="恢复内置默认风控词库"
      message="该操作会重新写入内置默认规则并覆盖被修改过的内置项（不会删除你自定义的额外规则）。确定继续吗？"
      confirm-text="恢复默认"
      :loading="resetting"
      @update:open="resetOpen = $event"
      @confirm="confirmReset"
    />
  </div>
</template>
