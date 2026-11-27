<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import Icon from '@/components/ui/Icon.vue'
import type { IconName } from '@/components/ui/icons'
import { useThemeStore } from '@/stores/theme'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ (event: 'update:open', value: boolean): void }>()

const route = useRoute()
const theme = useThemeStore()

interface NavItem {
  name: string
  to: string
  label: string
  icon: IconName
  tag?: string
  aliases?: string[]
}

interface NavGroup {
  id: string
  title: string
  items: NavItem[]
}

// 四大清晰运营板块分组（保持全部现有路由 path 与 name 100% 不变）
const navGroups: NavGroup[] = [
  {
    id: 'overview',
    title: '概览监控',
    items: [
      {
        name: 'dashboard',
        to: '/dashboard',
        label: '总览与性能看板',
        icon: 'dashboard',
      },
      {
        name: 'logs',
        to: '/logs',
        label: '调用明细日志',
        icon: 'activity',
      },
    ],
  },
  {
    id: 'pools',
    title: '账号与容器池',
    items: [
      {
        name: 'antigravity',
        to: '/antigravity',
        label: 'AntiGravity 容器池',
        icon: 'server',
        tag: 'AGY',
      },
      {
        name: 'accounts-workbuddy',
        to: '/accounts/workbuddy',
        label: 'WorkBuddy 账号池',
        icon: 'layers',
        tag: 'WB',
        aliases: ['accounts'],
      },
      {
        name: 'accounts-commandcode',
        to: '/accounts/commandcode',
        label: 'Command Code 池',
        icon: 'terminal',
        tag: 'CC',
      },
      {
        name: 'accounts-opencode',
        to: '/accounts/opencode',
        label: 'OpenCode 池',
        icon: 'code',
        tag: 'OC',
      },
      {
        name: 'groups',
        to: '/groups',
        label: '分组与倍率路由',
        icon: 'folder',
      },
      {
        name: 'keys',
        to: '/keys',
        label: '下游调用密钥',
        icon: 'key',
      },
    ],
  },
  {
    id: 'governance',
    title: '提示词与协议治理',
    items: [
      {
        name: 'prompts',
        to: '/prompts',
        label: '前置提示词注入',
        icon: 'fileText',
      },
      {
        name: 'filter-rules',
        to: '/filter-rules',
        label: '内容风控与词库',
        icon: 'shield',
      },
      {
        name: 'tool-obfuscation',
        to: '/tool-obfuscation',
        label: '工具名混淆',
        icon: 'lock',
      },
      {
        name: 'models',
        to: '/models',
        label: '模型广场与定价',
        icon: 'cpu',
      },
    ],
  },
  {
    id: 'ops',
    title: '系统运维',
    items: [
      {
        name: 'rewards',
        to: '/rewards',
        label: '领奖与保活中心',
        icon: 'gift',
      },
      {
        name: 'cron-jobs',
        to: '/cron-jobs',
        label: 'Cron 定时调度',
        icon: 'clock',
      },
      {
        name: 'admin-keys',
        to: '/admin-keys',
        label: '远程管理密钥',
        icon: 'shieldKey',
      },
      {
        name: 'settings',
        to: '/settings',
        label: '系统与超时配置',
        icon: 'sliders',
      },
    ],
  },
]

const groupCollapsed = reactive<Record<string, boolean>>({})

const activeName = computed(() => String(route.name ?? ''))
const collapsed = computed(() => theme.sidebarCollapsed)

function isActive(item: NavItem): boolean {
  if (activeName.value === item.name) return true
  if (item.aliases?.includes(activeName.value)) return true
  return route.path === item.to
}

function groupHasActive(group: NavGroup): boolean {
  return group.items.some((item) => isActive(item))
}

function toggleGroup(id: string): void {
  groupCollapsed[id] = !groupCollapsed[id]
}

function close(): void {
  emit('update:open', false)
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && props.open) {
    close()
  }
}

let prevBodyOverflow = ''
let bodyLocked = false

function setMobileScrollLock(lock: boolean): void {
  if (typeof document === 'undefined') return
  if (lock && !bodyLocked) {
    prevBodyOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    bodyLocked = true
  } else if (!lock && bodyLocked) {
    document.body.style.overflow = prevBodyOverflow
    bodyLocked = false
  }
}

watch(
  () => props.open,
  (isOpen) => {
    if (typeof window !== 'undefined' && window.innerWidth < 960) {
      setMobileScrollLock(isOpen)
    } else if (!isOpen) {
      setMobileScrollLock(false)
    }
    if (isOpen) {
      document.addEventListener('keydown', onKeydown)
    } else {
      document.removeEventListener('keydown', onKeydown)
    }
  },
)

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
  setMobileScrollLock(false)
})
</script>

<template>
  <!-- 移动端遮罩层 -->
  <Transition name="fade">
    <div
      v-if="props.open"
      class="fixed inset-0 z-30 bg-black/60 backdrop-blur-[2px] nav:hidden"
      aria-hidden="true"
      @click="close"
    />
  </Transition>

  <aside
    aria-label="主导航"
    class="fixed inset-y-0 left-0 z-40 flex flex-col border-r border-line bg-surface shadow-pop transition-[width,transform] duration-150 nav:translate-x-0 nav:shadow-none"
    :class="[
      props.open ? 'w-64 translate-x-0' : 'w-64 -translate-x-full',
      collapsed ? 'nav:w-16' : 'nav:w-60',
    ]"
  >
    <!-- 控制台品牌栏（固定 h-14 与 TopBar 严丝合缝对齐） -->
    <div
      class="flex h-14 shrink-0 items-center border-b border-line px-3.5"
      :class="collapsed ? 'nav:justify-center nav:px-2' : 'gap-2.5'"
    >
      <img
        src="/logo.png"
        alt="Analy2API"
        width="28"
        height="28"
        class="h-7 w-7 shrink-0 rounded-md border border-line/70 object-cover shadow-panel"
      />
      <div class="min-w-0 flex-1" :class="collapsed ? 'nav:hidden' : ''">
        <div class="flex items-center gap-1.5">
          <p class="truncate text-[13px] font-semibold leading-4 text-fg">Analy2API</p>
          <span
            class="rounded border border-line bg-raised px-1 py-px font-mono text-[10px] font-medium leading-3 text-subtle"
          >
            GATEWAY
          </span>
        </div>
        <p class="mt-0.5 truncate text-2xs leading-3.5 text-subtle">多源 AI 网关与容器调度台</p>
      </div>
      <button
        type="button"
        class="-mr-1 flex h-7 w-7 items-center justify-center rounded-md text-subtle transition hover:bg-raised hover:text-fg nav:hidden"
        aria-label="关闭导航抽屉"
        @click="close"
      >
        <Icon name="close" :size="15" />
      </button>
    </div>

    <!-- 四大板块分组导航：概览监控 / 账号与容器池 / 提示词与协议治理 / 系统运维 -->
    <nav class="scroll-thin flex-1 overflow-y-auto px-2.5 py-3">
      <div
        v-for="(group, index) in navGroups"
        :key="group.id"
        :class="index > 0 ? 'mt-3.5 border-t border-line/65 pt-3' : ''"
      >
        <button
          type="button"
          class="mb-1 flex w-full items-center justify-between rounded px-2 py-0.5 text-left text-[11px] font-semibold tracking-wider text-subtle transition hover:text-muted select-none"
          :class="collapsed ? 'nav:hidden' : ''"
          :aria-expanded="!groupCollapsed[group.id] || groupHasActive(group)"
          @click="toggleGroup(group.id)"
        >
          <span>{{ group.title }}</span>
          <Icon
            name="chevronDown"
            :size="12"
            class="text-subtle/75 transition-transform duration-150"
            :class="groupCollapsed[group.id] && !groupHasActive(group) ? '-rotate-90' : ''"
          />
        </button>

        <ul
          v-show="collapsed || !groupCollapsed[group.id] || groupHasActive(group)"
          class="space-y-0.5"
        >
          <li v-for="item in group.items" :key="item.name">
            <RouterLink
              :to="item.to"
              :title="collapsed ? `${group.title} · ${item.label}` : undefined"
              :aria-current="isActive(item) ? 'page' : undefined"
              class="group relative flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[12.5px] leading-5 transition"
              :class="[
                collapsed ? 'nav:justify-center nav:px-0 nav:py-2' : '',
                isActive(item)
                  ? 'border border-accent/25 bg-accent/12 font-medium text-accent'
                  : 'border border-transparent text-muted hover:bg-raised hover:text-fg',
              ]"
              @click="close"
            >
              <Icon
                :name="item.icon"
                :size="15"
                :class="isActive(item) ? 'text-accent' : 'text-subtle group-hover:text-fg'"
              />
              <span class="flex-1 truncate" :class="collapsed ? 'nav:hidden' : ''">
                {{ item.label }}
              </span>
              <span
                v-if="item.tag"
                class="rounded border px-1 py-px font-mono text-[10px] leading-3"
                :class="[
                  collapsed ? 'nav:hidden' : '',
                  isActive(item)
                    ? 'border-accent/30 bg-accent/15 text-accent'
                    : 'border-line bg-raised text-subtle group-hover:border-line-strong',
                ]"
              >
                {{ item.tag }}
              </span>
            </RouterLink>
          </li>
        </ul>
      </div>
    </nav>

    <!-- 底部网关状态与折叠按钮 -->
    <div class="shrink-0 border-t border-line bg-raised/35 px-3 py-2.5">
      <div
        class="flex items-center justify-between gap-2"
        :class="collapsed ? 'nav:justify-center' : ''"
      >
        <div class="min-w-0 flex-1" :class="collapsed ? 'nav:hidden' : ''">
          <div class="flex items-center gap-1.5 text-2xs font-medium text-fg">
            <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-success" aria-hidden="true" />
            <span class="truncate">四源聚合网关在线</span>
          </div>
          <p class="mt-0.5 truncate font-mono text-[10.5px] text-subtle">
            AGY · WB · CC · OC
          </p>
        </div>

        <button
          type="button"
          class="hidden h-7 w-7 shrink-0 items-center justify-center rounded-md border border-line bg-surface text-subtle transition hover:border-line-strong hover:bg-raised hover:text-fg nav:inline-flex"
          :title="collapsed ? '展开侧边栏' : '折叠侧边栏为紧凑图标栏'"
          :aria-label="collapsed ? '展开侧边栏' : '折叠侧边栏'"
          @click="theme.toggleSidebarCollapsed()"
        >
          <Icon :name="collapsed ? 'chevronRight' : 'chevronLeft'" :size="14" />
        </button>
      </div>
    </div>
  </aside>
</template>
