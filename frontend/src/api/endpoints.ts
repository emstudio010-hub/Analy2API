import { del, delLong, get, getLong, post, postLong, put, putLong } from './client'
import type {
  Account,
  AccountBalanceRefreshAllResult,
  AccountBalanceRefreshResult,
  AccountCheckResult,
  AccountListResponse,
  AccountQuery,
  AdminKey,
  AdminKeyListResponse,
  AntigravityAuthFileListResponse,
  AntigravityEnvResponse,
  AntigravityOAuthURLResponse,
  ApiKey,
  ApiKeyListResponse,
  ApiKeyPayload,
  ApiKeyQuery,
  BatchImportPayload,
  BatchImportResult,
  CompleteOAuthCallbackPayload,
  ContainerNode,
  ContainerNodeAction,
  ContainerNodeActionResult,
  ContainerNodeListResponse,
  ContainerNodeLogsResponse,
  ContainerNodePayload,
  CreateAccountPayload,
  CreateAdminKeyPayload,
  CreateAdminKeyResponse,
  CreateOAuthSessionPayload,
  CronJob,
  CronJobListResponse,
  CronJobPayload,
  CronJobResetResponse,
  CronPreviewResult,
  CronRunNowResponse,
  DashboardResponse,
  EmailChangeResponse,
  ExportAccountsResult,
  FilterRule,
  FilterRuleListResponse,
  FilterRulePayload,
  FilterRulePreviewPayload,
  FilterRulePreviewResult,
  FilterRuleResetResponse,
  Group,
  GroupListResponse,
  GroupPayload,
  HarvestAllResult,
  HarvestResult,
  LoginLinkResult,
  LoginResponse,
  MeResponse,
  ModelCatalogResponse,
  ModelListResponse,
  ModelPrice,
  ModelPriceListResponse,
  NodeExitIPResponse,
  OAuthCallbackResponse,
  OAuthSession,
  PromptConfigResponse,
  PromptPreviewPayload,
  PromptPreviewResponse,
  PromptTemplate,
  PromptTemplateListResponse,
  PromptTemplatePayload,
  ProxyFetchResult,
  ProxyPoolConfig,
  ProxyPoolView,
  ProxyProbeResult,
  ProxyTestResult,
  RequestAntigravityOAuthPayload,
  RewardsOverviewResponse,
  RotateDeviceResult,
  RunRewardsActionPayload,
  RunRewardsActionResponse,
  RuntimeSettingsPayload,
  ScanLocalPayload,
  ScanLocalResult,
  SessionGuardConfig,
  SessionGuardResponse,
  SettingsResponse,
  SyncAntigravityAccountsPayload,
  SyncAntigravityAccountsResponse,
  TokenRefreshResult,
  ToolObfuscationConfig,
  ToolObfuscationGroupOverride,
  ToolObfuscationPreviewPayload,
  ToolObfuscationPreviewResponse,
  ToolObfuscationResponse,
  UpdateAccountPayload,
  UpdateAdminKeyPayload,
  UpdateGlobalPromptPayload,
  UpdateGroupPromptPayload,
  UploadAntigravityAuthFilePayload,
  UploadAntigravityAuthFileResponse,
  UpsertModelPricePayload,
  UsageLogListResponse,
  UsageLogQuery,
} from './types'

/** 去掉 undefined / null / 空串的参数，避免后端把空串当成有效筛选值。 */
function clean(params: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    out[key] = value
  }
  return out
}

// ---- 管理员认证 ----

export const authApi = {
  login(email: string, password: string) {
    return post<LoginResponse>('/auth/login', { email, password })
  },
  me() {
    return get<MeResponse>('/auth/me')
  },
  changePassword(currentPassword: string, newPassword: string) {
    return post<{ message: string }>('/auth/password', {
      current_password: currentPassword,
      new_password: newPassword,
    })
  },
  changeEmail(currentPassword: string, email: string) {
    return put<EmailChangeResponse>('/auth/profile', {
      current_password: currentPassword,
      email,
    })
  },
}

// ---- 监控总览 ----

export const dashboardApi = {
  fetch(start: string, end: string) {
    return get<DashboardResponse>('/dashboard', { params: clean({ start, end }) })
  },
}

// ---- 四端上游账号池 ----

export const accountsApi = {
  list(query: AccountQuery = {}) {
    return get<AccountListResponse>('/accounts', { params: clean({ ...query }) })
  },
  detail(id: number) {
    return get<Account>(`/accounts/${id}`)
  },
  create(payload: CreateAccountPayload) {
    return post<Account>('/accounts', payload)
  },
  update(id: number, payload: UpdateAccountPayload) {
    return put<Account>(`/accounts/${id}`, payload)
  },
  remove(id: number) {
    return del<{ message: string }>(`/accounts/${id}`)
  },
  /** 向上游发真实极小探测请求，统一使用 300s 长超时。 */
  check(id: number) {
    return postLong<AccountCheckResult>(`/accounts/${id}/check`)
  },
  refreshBalance(id: number) {
    return postLong<AccountBalanceRefreshResult>(`/accounts/${id}/balance`)
  },
  refreshAllBalances(platform?: string) {
    return postLong<AccountBalanceRefreshAllResult>(
      '/accounts/balance/refresh-all',
      platform ? { platform } : undefined,
    )
  },
  batchImport(payload: BatchImportPayload) {
    return postLong<BatchImportResult>('/accounts/batch', payload)
  },
  scanLocal(payload: ScanLocalPayload = {}) {
    return postLong<ScanLocalResult>('/accounts/scan-local', payload)
  },
  exportAccounts(platform?: string) {
    return get<ExportAccountsResult>('/accounts/export', { params: clean({ platform }) })
  },
  testProxy(id: number, proxyUrl?: string) {
    return post<ProxyTestResult>(
      `/accounts/${id}/proxy-test`,
      proxyUrl !== undefined ? { proxy_url: proxyUrl } : {},
    )
  },
  rotateDevice(id: number) {
    return post<RotateDeviceResult>(`/accounts/${id}/rotate-device`)
  },
  loginLink(id: number, target?: string) {
    return post<LoginLinkResult>(
      `/accounts/${id}/login-link`,
      target !== undefined ? { target } : {},
    )
  },
  harvest(id: number, doChat?: boolean) {
    return postLong<HarvestResult>(
      `/accounts/${id}/harvest`,
      doChat !== undefined ? { do_chat: doChat } : {},
    )
  },
  harvestAll(doChat?: boolean) {
    return postLong<HarvestAllResult>(
      '/accounts/harvest-all',
      doChat !== undefined ? { do_chat: doChat } : {},
    )
  },
  refreshToken(id: number) {
    return postLong<TokenRefreshResult>(`/accounts/${id}/refresh-token`)
  },
}

// ---- AntiGravity 容器与登录文件 API (/antigravity/...) ----

export function fetchAntigravityEnv() {
  return get<AntigravityEnvResponse>('/antigravity/env')
}

export function listAntigravityNodes() {
  return get<ContainerNodeListResponse>('/antigravity/nodes')
}

export function createAntigravityNode(payload: ContainerNodePayload) {
  return postLong<ContainerNode>('/antigravity/nodes', payload)
}

export function updateAntigravityNode(id: number, payload: ContainerNodePayload) {
  return put<ContainerNode>(`/antigravity/nodes/${id}`, payload)
}

export function deleteAntigravityNode(id: number, removeContainer = false) {
  return delLong<{ message?: string; ok?: boolean }>(`/antigravity/nodes/${id}`, {
    params: { remove_container: removeContainer },
  })
}

export function runAntigravityNodeAction(id: number, action: ContainerNodeAction | string) {
  return postLong<ContainerNodeActionResult>(`/antigravity/nodes/${id}/action`, { action })
}

export function fetchAntigravityNodeLogs(id: number, tail = 200) {
  return get<ContainerNodeLogsResponse>(`/antigravity/nodes/${id}/logs`, {
    params: { tail },
  })
}

export function listAntigravityAuthFiles(nodeId: number) {
  return get<AntigravityAuthFileListResponse>(`/antigravity/nodes/${nodeId}/auth-files`)
}

export function uploadAntigravityAuthFile(
  nodeId: number,
  payload: UploadAntigravityAuthFilePayload,
) {
  return post<UploadAntigravityAuthFileResponse>(
    `/antigravity/nodes/${nodeId}/auth-files`,
    payload,
  )
}

export function deleteAntigravityAuthFile(nodeId: number, filename: string) {
  return del<{ message?: string; ok?: boolean }>(
    `/antigravity/nodes/${nodeId}/auth-files/${encodeURIComponent(filename)}`,
  )
}

export function requestAntigravityOAuthURL(
  nodeId: number,
  payload: RequestAntigravityOAuthPayload,
) {
  return post<AntigravityOAuthURLResponse>(`/antigravity/nodes/${nodeId}/oauth-url`, payload)
}

export function syncAntigravityAccounts(
  nodeId: number,
  payload: SyncAntigravityAccountsPayload = {},
) {
  return postLong<SyncAntigravityAccountsResponse>(
    `/antigravity/nodes/${nodeId}/sync-accounts`,
    payload,
  )
}

export function completeAntigravityOAuthCallback(
  nodeId: number,
  payload: CompleteOAuthCallbackPayload,
) {
  return postLong<OAuthCallbackResponse>(`/antigravity/nodes/${nodeId}/oauth-callback`, payload)
}

export function rotateAntigravityFingerprint(nodeId: number) {
  return post<{ ok: boolean; node: ContainerNode; message?: string }>(
    `/antigravity/nodes/${nodeId}/rotate-fingerprint`,
  )
}

export function checkAntigravityNodeExitIP(nodeId: number) {
  return postLong<NodeExitIPResponse>(`/antigravity/nodes/${nodeId}/check-ip`)
}

export const antigravityApi = {
  env: fetchAntigravityEnv,
  listNodes: listAntigravityNodes,
  createNode: createAntigravityNode,
  updateNode: updateAntigravityNode,
  deleteNode: deleteAntigravityNode,
  runNodeAction: runAntigravityNodeAction,
  fetchNodeLogs: fetchAntigravityNodeLogs,
  listAuthFiles: listAntigravityAuthFiles,
  uploadAuthFile: uploadAntigravityAuthFile,
  deleteAuthFile: deleteAntigravityAuthFile,
  requestOAuthURL: requestAntigravityOAuthURL,
  completeOAuthCallback: completeAntigravityOAuthCallback,
  syncAccounts: syncAntigravityAccounts,
  rotateFingerprint: rotateAntigravityFingerprint,
  checkIP: checkAntigravityNodeExitIP,
}

// ---- 全平台统一 OAuth 会话与回调闭环 API (/oauth/...) ----

export const oauthApi = {
  createSession(payload: CreateOAuthSessionPayload) {
    return post<{
      session: OAuthSession
      session_id: string
      auth_url: string
      url: string
      state: string
      platform: string
      provider: string
      mode: string
      callback_url: string
      instructions: string
    }>('/oauth/sessions', payload)
  },
  listSessions() {
    return get<{ items: OAuthSession[]; total: number }>('/oauth/sessions')
  },
  completeCallback(payload: CompleteOAuthCallbackPayload) {
    return postLong<OAuthCallbackResponse>('/oauth/callback', payload)
  },
}

// ---- 前置提示词注入中心 API (/prompts/...) ----

export function fetchPromptConfig() {
  return get<PromptConfigResponse>('/prompts/config')
}

export function updateGlobalPrompt(payload: UpdateGlobalPromptPayload) {
  return put<{
    enabled?: boolean
    global_enabled?: boolean
    prompt?: string
    global_prompt?: string
    message?: string
  }>('/prompts/global', payload)
}

export function listPromptTemplates() {
  return get<PromptTemplateListResponse>('/prompts/templates')
}

export function createPromptTemplate(payload: PromptTemplatePayload) {
  return post<PromptTemplate>('/prompts/templates', payload)
}

export function updatePromptTemplate(id: number, payload: PromptTemplatePayload) {
  return put<PromptTemplate>(`/prompts/templates/${id}`, payload)
}

export function deletePromptTemplate(id: number) {
  return del<{ message?: string }>(`/prompts/templates/${id}`)
}

export function resetPromptTemplates() {
  return post<{ items: PromptTemplate[]; total?: number; message?: string }>(
    '/prompts/templates/reset',
  )
}

export function updateGroupPrompt(groupId: number, payload: UpdateGroupPromptPayload) {
  return put<Group>(`/prompts/groups/${groupId}`, payload)
}

export function previewPromptInjection(payload: PromptPreviewPayload) {
  return post<PromptPreviewResponse>('/prompts/preview', payload)
}

export const promptsApi = {
  config: fetchPromptConfig,
  updateGlobal: updateGlobalPrompt,
  listTemplates: listPromptTemplates,
  createTemplate: createPromptTemplate,
  updateTemplate: updatePromptTemplate,
  deleteTemplate: deletePromptTemplate,
  resetTemplates: resetPromptTemplates,
  updateGroup: updateGroupPrompt,
  preview: previewPromptInjection,
}

// ---- 领奖与保活中心 API (/rewards/...) ----

export function fetchRewardsOverview() {
  return get<RewardsOverviewResponse>('/rewards/overview')
}

export function runRewardsAction(payload: RunRewardsActionPayload) {
  return postLong<RunRewardsActionResponse>('/rewards/run', payload)
}

export const rewardsApi = {
  overview: fetchRewardsOverview,
  run: runRewardsAction,
}

// ---- 模型广场与标准价格库 API (/models/...) ----

export function fetchModelCatalog(refresh = false, platform = '') {
  return getLong<ModelCatalogResponse>('/models/catalog', {
    params: {
      refresh,
      ...(platform ? { platform } : {}),
    },
  })
}

export function listModelPrices() {
  return get<ModelPriceListResponse>('/models/prices')
}

export function upsertModelPrice(payload: UpsertModelPricePayload) {
  return put<ModelPrice>('/models/prices', payload)
}

export function deleteModelPrice(id: number) {
  return del<{ message?: string }>(`/models/prices/${id}`)
}

export const modelsApi = {
  catalog: fetchModelCatalog,
  listPrices: listModelPrices,
  upsertPrice: upsertModelPrice,
  deletePrice: deleteModelPrice,
}

// ---- 远程管理密钥 API (/admin-keys) ----

export function listAdminKeys() {
  return get<AdminKeyListResponse>('/admin-keys')
}

export function createAdminKey(payload: CreateAdminKeyPayload) {
  return post<CreateAdminKeyResponse>('/admin-keys', payload)
}

export function updateAdminKey(id: number, payload: UpdateAdminKeyPayload) {
  return put<AdminKey>(`/admin-keys/${id}`, payload)
}

export function deleteAdminKey(id: number) {
  return del<{ message?: string }>(`/admin-keys/${id}`)
}

export const adminKeysApi = {
  list: listAdminKeys,
  create: createAdminKey,
  update: updateAdminKey,
  remove: deleteAdminKey,
}

// ---- 代理 IP 池 & 远程拉取 (/proxy-pool) ----

export function getProxyPool() {
  return get<ProxyPoolView>('/proxy-pool')
}

export function updateProxyPool(payload: ProxyPoolConfig & { raw_proxies?: string; raw_tag?: string }) {
  return put<ProxyPoolView>('/proxy-pool', payload)
}

export function addProxyPoolEntries(payload: { raw_text?: string; proxies?: string[]; tag?: string; note?: string }) {
  return post<{ ok: boolean; added: number; view: ProxyPoolView }>('/proxy-pool/entries', payload)
}

export function deleteProxyPoolEntry(id: string) {
  return del<ProxyPoolView>(`/proxy-pool/entries/${encodeURIComponent(id)}`)
}

export function fetchProxyPool(payload?: { url?: string; dry_run?: boolean }) {
  return post<{ ok: boolean; result: ProxyFetchResult; view?: ProxyPoolView; error?: string }>('/proxy-pool/fetch', payload ?? {})
}

export function probeProxyPool(payload?: { entry_id?: string; proxy_url?: string; limit?: number }) {
  return post<{ ok: boolean; results: ProxyProbeResult[] }>('/proxy-pool/test', payload ?? {})
}

export function allocateProxyPoolPreview(payload?: { admin_key_id?: number; platform?: string; tag?: string; strategy?: string; mode?: string; fetch_url?: string }) {
  return post<{ ok: boolean; masked_proxy_url: string; source: string; tag?: string; entry_id?: string; warning?: string }>('/proxy-pool/allocate', payload ?? {})
}

// ---- 内容风控与词库 ----

export const filterRulesApi = {
  list() {
    return get<FilterRuleListResponse>('/filter-rules')
  },
  create(payload: FilterRulePayload) {
    return post<FilterRule>('/filter-rules', payload)
  },
  update(id: number, payload: FilterRulePayload) {
    return put<FilterRule>(`/filter-rules/${id}`, payload)
  },
  remove(id: number) {
    return del<{ message: string }>(`/filter-rules/${id}`)
  },
  reset() {
    return post<FilterRuleResetResponse>('/filter-rules/reset')
  },
  preview(payload: FilterRulePreviewPayload) {
    return post<FilterRulePreviewResult>('/filter-rules/preview', payload)
  },
}

// ---- 分组与倍率路由 ----

export const groupsApi = {
  list() {
    return get<GroupListResponse>('/groups')
  },
  create(payload: GroupPayload) {
    return post<Group>('/groups', payload)
  },
  update(id: number, payload: GroupPayload) {
    return put<Group>(`/groups/${id}`, payload)
  },
  remove(id: number) {
    return del<{ message: string }>(`/groups/${id}`)
  },
}

// ---- 下游调用密钥 ----

export const keysApi = {
  list(query: ApiKeyQuery = {}) {
    return get<ApiKeyListResponse>('/keys', { params: clean({ ...query }) })
  },
  create(payload: ApiKeyPayload) {
    return post<ApiKey>('/keys', payload)
  },
  update(id: number, payload: ApiKeyPayload) {
    return put<ApiKey>(`/keys/${id}`, payload)
  },
  remove(id: number) {
    return del<{ message: string }>(`/keys/${id}`)
  },
  resetQuota(id: number) {
    return post<{ message: string }>(`/keys/${id}/reset-quota`)
  },
}

// ---- 调用明细日志 ----

export const logsApi = {
  list(query: UsageLogQuery = {}) {
    return get<UsageLogListResponse>('/logs', { params: clean({ ...query }) })
  },
}

// ---- 系统与超时配置 (/settings) ----

export function updateRuntimeSettings(payload: RuntimeSettingsPayload) {
  return putLong<SettingsResponse>('/settings/runtime', payload)
}

export const metaApi = {
  models() {
    return get<ModelListResponse>('/models')
  },
  settings() {
    return get<SettingsResponse>('/settings')
  },
  updateRuntime: updateRuntimeSettings,
}

export const settingsApi = {
  fetch() {
    return get<SettingsResponse>('/settings')
  },
  updateRuntime: updateRuntimeSettings,
}

// ---- Cron 定时调度 ----

export const cronJobsApi = {
  list() {
    return get<CronJobListResponse>('/cron-jobs')
  },
  create(payload: CronJobPayload) {
    return post<CronJob>('/cron-jobs', payload)
  },
  update(id: number, payload: CronJobPayload) {
    return put<CronJob>(`/cron-jobs/${id}`, payload)
  },
  remove(id: number) {
    return del<{ message: string }>(`/cron-jobs/${id}`)
  },
  runNow(id: number) {
    return postLong<CronRunNowResponse>(`/cron-jobs/${id}/run`)
  },
  preview(cronExpr: string, timezone = 'Asia/Shanghai', count = 5) {
    return post<CronPreviewResult>('/cron-jobs/preview', {
      cron_expr: cronExpr,
      timezone,
      count,
    })
  },
  reset() {
    return post<CronJobResetResponse>('/cron-jobs/reset')
  },
}

// ---- 工具名混淆引擎 API (/tool-obfuscation) ----

export const toolObfuscationApi = {
  get() {
    return get<ToolObfuscationResponse>('/tool-obfuscation')
  },
  update(payload: ToolObfuscationConfig) {
    return put<ToolObfuscationResponse>('/tool-obfuscation', payload)
  },
  updateGroup(groupId: number, payload: ToolObfuscationGroupOverride) {
    return put<ToolObfuscationResponse>(`/tool-obfuscation/groups/${groupId}`, payload)
  },
  rotateSalt() {
    return post<ToolObfuscationResponse>('/tool-obfuscation/rotate-salt')
  },
  preview(payload: ToolObfuscationPreviewPayload) {
    return post<ToolObfuscationPreviewResponse>('/tool-obfuscation/preview', payload)
  },
}

// ---- 非账号额度 429 会话暂停保护与请求体审计 Log API (/session-guard) ----

export const sessionGuardApi = {
  get() {
    return get<SessionGuardResponse>('/session-guard')
  },
  updateConfig(payload: SessionGuardConfig) {
    return put<SessionGuardResponse>('/session-guard/config', payload)
  },
  resumeSession(sessionId: string) {
    return del<{ ok: boolean; resumed: boolean; session_id: string; view: SessionGuardResponse }>(
      `/session-guard/sessions/${encodeURIComponent(sessionId)}`,
    )
  },
  clearAllSessions() {
    return del<{ ok: boolean; cleared: number; view: SessionGuardResponse }>(
      '/session-guard/sessions',
    )
  },
  readLog(filename: string) {
    return get<{ filename: string; content: string }>(
      `/session-guard/logs/${encodeURIComponent(filename)}`,
    )
  },
}

// === Platform Prompt Catalog & Pipeline Preview ===

import type {
  ApplyPromptCatalogPayload,
  ApplyPromptCatalogResponse,
  FilterPipelinePreviewPayload,
  FilterPipelinePreviewResponse,
  PromptCatalogQuery,
  PromptCatalogResponse,
} from './types'

export function fetchPromptCatalog(query: PromptCatalogQuery = {}) {
  return get<PromptCatalogResponse>('/prompts/catalog', { params: clean({ ...query }) })
}

export function applyPromptCatalogItem(payload: ApplyPromptCatalogPayload) {
  return post<ApplyPromptCatalogResponse>('/prompts/catalog/apply', payload)
}

export function previewFilterPipeline(payload: FilterPipelinePreviewPayload) {
  return post<FilterPipelinePreviewResponse>('/filter-rules/preview', payload)
}

export const promptCatalogApi = {
  list: fetchPromptCatalog,
  apply: applyPromptCatalogItem,
  previewPipeline: previewFilterPipeline,
}

export * from './types'
