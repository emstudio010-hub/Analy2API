import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

import AppShell from '@/components/layout/AppShell.vue'
import { useAuthStore } from '@/stores/auth'
import type { UpstreamPlatform } from '@/api/types'

declare module 'vue-router' {
  interface RouteMeta {
    section?: string
    title?: string
    subtitle?: string
    platform?: UpstreamPlatform
    /** 免登录页面。目前只有登录页。 */
    public?: boolean
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { title: '登录', public: true },
  },
  {
    path: '/',
    component: AppShell,
    children: [
      {
        path: '',
        redirect: { name: 'dashboard' },
      },
      // 1. 监控总览
      {
        path: 'dashboard',
        name: 'dashboard',
        component: () => import('@/views/DashboardView.vue'),
        meta: {
          section: '监控总览',
          title: '总览与性能看板',
          subtitle: '四源网关吞吐、首字延迟 P50/P95、缓存命中率与成本拆分',
        },
      },
      // 2. 上游平台工作台
      {
        path: 'antigravity',
        name: 'antigravity',
        component: () => import('@/views/AntigravityView.vue'),
        meta: {
          section: '上游平台工作台',
          title: 'AntiGravity 容器池',
          subtitle: '宿主机 Docker 容器编排、远程 CLIProxyAPI 节点与 OAuth JSON 登录文件池',
          platform: 'antigravity',
        },
      },
      {
        path: 'accounts',
        name: 'accounts',
        redirect: (to) => ({ name: 'accounts-workbuddy', query: to.query }),
      },
      {
        path: 'accounts/workbuddy',
        name: 'accounts-workbuddy',
        component: () => import('@/views/AccountsView.vue'),
        meta: {
          section: '上游平台工作台',
          title: 'WorkBuddy 账号池',
          subtitle: 'WorkBuddy / CodeBuddy 账号凭证、独立代理 IP、成对设备指纹与每日签到领奖',
          platform: 'workbuddy',
        },
      },
      {
        path: 'accounts/commandcode',
        name: 'accounts-commandcode',
        component: () => import('@/views/CommandCodeView.vue'),
        meta: {
          section: '上游平台工作台',
          title: 'Command Code 池',
          subtitle: 'Command Code 上游账号池、独立出口代理与健康探测',
          platform: 'commandcode',
        },
      },
      {
        path: 'accounts/opencode',
        name: 'accounts-opencode',
        component: () => import('@/views/OpenCodeView.vue'),
        meta: {
          section: '上游平台工作台',
          title: 'OpenCode 池',
          subtitle: 'OpenCode Zen 按量与 Go 订阅额度双模式账号池管理',
          platform: 'opencode',
        },
      },
      // 3. 路由与提示词策略
      {
        path: 'prompts',
        name: 'prompts',
        component: () => import('@/views/PromptInjectionView.vue'),
        meta: {
          section: '路由与提示词策略',
          title: '前置提示词注入',
          subtitle: '全局底线 System 指令、分组四级注入策略、内置模板库与合成预览',
        },
      },
      {
        path: 'groups',
        name: 'groups',
        component: () => import('@/views/GroupsView.vue'),
        meta: {
          section: '路由与提示词策略',
          title: '分组与倍率路由',
          subtitle: '按上游平台隔离调度池、计费倍率与分组专属提示词策略',
        },
      },
      {
        path: 'filter-rules',
        name: 'filter-rules',
        component: () => import('@/views/FilterRulesView.vue'),
        meta: {
          section: '路由与提示词策略',
          title: '内容风控与词库',
          subtitle: '全客户端默认强制启用 · 纯删减、重写与安全同义词替换（零 \\u200b）',
        },
      },
      {
        path: 'tool-obfuscation',
        name: 'tool-obfuscation',
        component: () => import('@/views/ToolObfuscationView.vue'),
        meta: {
          section: '路由与提示词策略',
          title: '工具名混淆',
          subtitle: '确定性、KV-Cache 友好、可逆双射工具名伪装（四端全协议无缝还原）',
        },
      },
      // 4. 运营与模型中心
      {
        path: 'rewards',
        name: 'rewards',
        component: () => import('@/views/RewardsCenterView.vue'),
        meta: {
          section: '运营与模型中心',
          title: '领奖与保活中心',
          subtitle: '批量签到领奖、Token 自动续期、积分刷新与全池健康巡检',
        },
      },
      {
        path: 'cron-jobs',
        name: 'cron-jobs',
        component: () => import('@/views/CronJobsView.vue'),
        meta: {
          section: '运营与模型中心',
          title: 'Cron 定时调度',
          subtitle: '按 Cron 表达式定时巡检账号、测试独立代理、自动续期与每日收割',
        },
      },
      {
        path: 'models',
        name: 'models',
        component: () => import('@/views/ModelCatalogView.vue'),
        meta: {
          section: '运营与模型中心',
          title: '模型广场与定价',
          subtitle: '四端上游实时模型目录拉取、能力画像与百万 Token 标准定价库',
        },
      },
      // 5. 接入与系统审计
      {
        path: 'keys',
        name: 'keys',
        component: () => import('@/views/KeysView.vue'),
        meta: {
          section: '接入与系统审计',
          title: '下游调用密钥',
          subtitle: '签发给 Claude Code / Codex CLI / OpenAI 兼容客户端的 API Key',
        },
      },
      {
        path: 'admin-keys',
        name: 'admin-keys',
        component: () => import('@/views/AdminKeysView.vue'),
        meta: {
          section: '接入与系统审计',
          title: '远程管理密钥',
          subtitle: '签发 adm- 前缀远程管理密钥，支持读写权限分级与 IP 白名单',
        },
      },
      {
        path: 'logs',
        name: 'logs',
        component: () => import('@/views/LogsView.vue'),
        meta: {
          section: '接入与系统审计',
          title: '调用明细日志',
          subtitle: '逐条请求的平台路由、Token 缓存/推理明细、首字延迟、TPS 与费用审计',
        },
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('@/views/SettingsView.vue'),
        meta: {
          section: '接入与系统审计',
          title: '系统与超时配置',
          subtitle: '三级流式超时（首字 / 流空闲 / 非流式）、全局代理与运行态热更新配置',
        },
      },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: { name: 'dashboard' } },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior(_to, _from, savedPosition) {
    return savedPosition ?? { top: 0 }
  },
})

router.beforeEach((to) => {
  const auth = useAuthStore()

  if (to.meta.public) {
    if (auth.isAuthenticated && to.name === 'login') {
      return { name: 'dashboard' }
    }
    return true
  }

  if (!auth.isAuthenticated) {
    return {
      name: 'login',
      query: to.fullPath === '/' || to.fullPath === '/dashboard' ? {} : { redirect: to.fullPath },
    }
  }

  return true
})

router.afterEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} · cmd2api` : 'cmd2api · 多源 AI 网关控制台'
})

export default router
