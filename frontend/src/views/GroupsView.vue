<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { groupsApi } from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type { Group } from '@/api/types'
import GroupFormModal from '@/components/groups/GroupFormModal.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import DataTable from '@/components/ui/DataTable.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Icon from '@/components/ui/Icon.vue'
import Input from '@/components/ui/Input.vue'
import PlatformBadge from '@/components/ui/PlatformBadge.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'
import StatusPill from '@/components/ui/StatusPill.vue'
import { useToastStore } from '@/stores/toast'
import { formatDateTime, formatNumber } from '@/utils/format'
import { ALL_PLATFORMS, normalizePlatform, platformMeta } from '@/utils/platforms'

const toast = useToastStore()

const items = ref<Group[]>([])
const loading = ref(true)
const error = ref('')

const platformTab = ref('all')
const keyword = ref('')

const formOpen = ref(false)
const editing = ref<Group | null>(null)
const deleteTarget = ref<Group | null>(null)
const deleting = ref(false)

const platformTabs = computed(() => [
  { value: 'all', label: '全部平台', badge: items.value.length },
  ...ALL_PLATFORMS.map((p) => ({
    value: p.value,
    label: p.label,
    icon: p.icon,
    badge: items.value.filter((g) => normalizePlatform(g.platform) === p.value).length,
  })),
])

const filteredItems = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return items.value.filter((g) => {
    if (platformTab.value !== 'all' && normalizePlatform(g.platform) !== platformTab.value) {
      return false
    }
    if (!q) return true
    return g.name.toLowerCase().includes(q) || (g.description ?? '').toLowerCase().includes(q)
  })
})

function injectModeBadge(group: Group): {
  tone: 'accent' | 'info' | 'warning' | 'neutral'
  label: string
} {
  switch (group.prompt_inject_mode) {
    case 'override_global':
      return { tone: 'info', label: '覆盖全局' }
    case 'replace_all':
      return { tone: 'warning', label: '强制接管' }
    case 'off':
      return { tone: 'neutral', label: '关闭注入' }
    default:
      return { tone: 'accent', label: '叠加全局' }
  }
}

function platformPoolRoute(platform: string): { name: string } {
  return { name: platformMeta(platform).routeName }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const response = await groupsApi.list()
    items.value = response.items ?? []
  } catch (err) {
    error.value = toMessage(err, '加载分组失败')
    items.value = []
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})

function openCreate(): void {
  editing.value = null
  formOpen.value = true
}

function openEdit(group: Group): void {
  editing.value = group
  formOpen.value = true
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  try {
    await groupsApi.remove(target.id)
    toast.success('分组已删除', target.name)
    deleteTarget.value = null
    await load()
  } catch (err) {
    toast.error('删除分组失败', toMessage(err), 8000)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部平台切换与操作栏 -->
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <div class="flex flex-wrap items-center gap-2">
        <SegmentedTabs v-model="platformTab" :items="platformTabs" />
        <div class="w-52">
          <Input v-model="keyword" size="sm" placeholder="搜索分组名称或描述…" />
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <RouterLink :to="{ name: 'prompts' }">
          <Button variant="secondary" size="sm">
            <template #leading><Icon name="fileText" :size="13" /></template>
            提示词注入中心
          </Button>
        </RouterLink>
        <Button variant="secondary" size="sm" :loading="loading" @click="load">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新
        </Button>
        <Button variant="primary" size="sm" @click="openCreate">
          <template #leading><Icon name="plus" :size="13" /></template>
          新建分组
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

    <DataTable
      :columns="10"
      :loading="loading"
      :empty="filteredItems.length === 0"
      min-width="1180px"
      hoverable
    >
      <template #head>
        <th class="th">分组名称</th>
        <th class="th">所属平台</th>
        <th class="th">提示词注入策略</th>
        <th class="th">描述</th>
        <th class="th text-right">计费倍率</th>
        <th class="th">状态</th>
        <th class="th text-right">池内账号</th>
        <th class="th text-right">下游密钥</th>
        <th class="th">创建时间</th>
        <th class="th text-right">操作</th>
      </template>

      <template #empty>
        <EmptyState
          icon="folder"
          :title="platformTab !== 'all' || keyword.trim() ? '没有匹配的路由分组' : '还没有任何路由分组'"
          :description="
            platformTab !== 'all' || keyword.trim()
              ? '可切换平台标签或清空关键词后重试。'
              : '创建分组以绑定 WorkBuddy、AntiGravity、Command Code 或 OpenCode 账号池，并为下游密钥分配计费倍率与系统提示词策略。'
          "
        >
          <Button
            v-if="platformTab !== 'all' || keyword.trim()"
            variant="secondary"
            size="sm"
            @click="
              () => {
                platformTab = 'all'
                keyword = ''
              }
            "
          >
            重置筛选
          </Button>
          <Button v-else variant="primary" size="sm" @click="openCreate">
            <template #leading><Icon name="plus" :size="13" /></template>
            新建分组
          </Button>
        </EmptyState>
      </template>

      <template #body>
        <tr v-for="group in filteredItems" :key="group.id">
          <td class="td max-w-[14rem]">
            <div class="flex flex-col gap-0.5">
              <span class="truncate font-medium text-fg">{{ group.name }}</span>
              <span class="tnum text-2xs text-subtle">#{{ group.id }}</span>
            </div>
          </td>

          <td class="td">
            <PlatformBadge :platform="group.platform" />
          </td>

          <td class="td">
            <div class="flex items-center gap-1.5">
              <Badge :tone="injectModeBadge(group).tone" dot>
                {{ injectModeBadge(group).label }}
              </Badge>
              <Badge
                v-if="group.system_prompt?.trim()"
                tone="neutral"
                title="已配置分组专属 System Prompt"
              >
                专属指令
              </Badge>
              <Badge
                v-else-if="group.prompt_template_id"
                tone="neutral"
                mono
                title="已绑定提示词模板"
              >
                TPL#{{ group.prompt_template_id }}
              </Badge>
            </div>
          </td>

          <td class="td max-w-[18rem]">
            <span class="block truncate text-muted" :title="group.description">
              {{ group.description || '—' }}
            </span>
          </td>

          <td class="td tnum text-right font-mono text-fg">{{ group.rate_multiplier }}x</td>

          <td class="td">
            <StatusPill :status="group.status" />
          </td>

          <td class="td text-right">
            <RouterLink
              :to="platformPoolRoute(group.platform)"
              class="tnum font-medium text-accent transition hover:underline"
              title="前往对应平台账号/容器工作台"
            >
              {{ formatNumber(group.account_count) }}
            </RouterLink>
          </td>

          <td class="td text-right">
            <RouterLink
              :to="{ name: 'keys' }"
              class="tnum font-medium text-accent transition hover:underline"
            >
              {{ formatNumber(group.api_key_count) }}
            </RouterLink>
          </td>

          <td class="td tnum text-muted">{{ formatDateTime(group.created_at) }}</td>

          <td class="td">
            <div class="flex items-center justify-end gap-1">
              <RouterLink :to="{ name: 'prompts', query: { group_id: String(group.id) } }">
                <Button variant="ghost" size="xs" title="在提示词注入中心编辑完整指令与沙盒预览">
                  <template #leading><Icon name="fileText" :size="12" /></template>
                  提示词
                </Button>
              </RouterLink>
              <Button variant="ghost" size="xs" icon title="编辑分组" @click="openEdit(group)">
                <Icon name="edit" :size="13" />
              </Button>
              <Button
                variant="ghost"
                size="xs"
                icon
                class="hover:bg-danger/10 hover:text-danger"
                :title="group.api_key_count > 0 ? '该分组下还有密钥，需先迁移' : '删除分组'"
                @click="deleteTarget = group"
              >
                <Icon name="trash" :size="13" />
              </Button>
            </div>
          </td>
        </tr>
      </template>
    </DataTable>

    <GroupFormModal v-model:open="formOpen" :group="editing" @saved="load" />

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除分组"
      :message="`确定删除分组「${deleteTarget?.name ?? ''}」吗？`"
      confirm-text="删除"
      tone="danger"
      :loading="deleting"
      @update:open="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p v-if="deleteTarget && deleteTarget.api_key_count > 0" class="text-danger">
        该分组下还有 {{ deleteTarget.api_key_count }} 把下游密钥，后端会拒绝删除。请先迁移或删除这些密钥。
      </p>
      <p v-else class="text-muted">
        分组下的上游账号不会被删除，仅解除与该分组的路由绑定。
      </p>
    </ConfirmDialog>
  </div>
</template>
