// 与后端 DTO 一一对应的类型定义。
// 字段名保持与后端 JSON 完全一致（snake_case），中间不做转换。

/** 账号状态。与 internal/domain 的常量一致。 */
export type AccountStatus = 'active' | 'disabled' | 'error'

/** 四大上游平台标识。 */
export type UpstreamPlatform = 'workbuddy' | 'antigravity' | 'commandcode' | 'opencode'

/** OpenCode 计费模式。 */
export type AccountMode = 'zen' | 'go'

/** 分组 / 密钥状态。 */
export type CommonStatus = 'active' | 'disabled'

/** 分组提示词前置注入策略模式。 */
export type PromptInjectMode = 'append_global' | 'override_global' | 'replace_all' | 'off'

/** AntiGravity 容器节点部署模式。 */
export type ContainerNodeMode = 'local_docker' | 'remote_api'

/** AntiGravity 容器节点运维动作。 */
export type ContainerNodeAction = 'start' | 'stop' | 'restart' | 'pull' | 'check'

/** 远程管理密钥权限范围。 */
export type AdminKeyScope = 'full' | 'push_accounts' | 'readonly'

export interface AdminUser {
  id: number
  email: string
  role: string
  status: string
  last_login_at: string | null
  created_at: string
}

export interface LoginResponse {
  token: string
  expires_at: string
  user: AdminUser
}

export interface MeResponse {
  user: AdminUser
}

/** 分页请求的公共参数。 */
export interface PageQuery {
  page?: number
  page_size?: number
}

// ---- 监控总览与性能看板 ----

export interface DashboardSummary {
  requests: number
  failed: number
  /** 0~1 之间的小数，不是百分数。 */
  success_rate: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
  cache_hit_rate: number
  p50_first_token_ms: number
  p95_first_token_ms: number
  avg_tps: number
  total_cost: number
  input_cost: number
  output_cost: number
  cache_cost: number
  avg_duration_ms: number
  avg_first_token_ms: number
}

export interface DashboardSeriesPoint {
  bucket: string
  requests: number
  failed?: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens?: number
  cache_creation_tokens?: number
  reasoning_tokens?: number
  avg_first_token_ms?: number
  avg_tps?: number
  total_cost: number
}

export interface BreakdownItem {
  key: string
  label: string
  requests: number
  failed?: number
  tokens: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens?: number
  reasoning_tokens?: number
  cache_hit_rate: number
  total_cost: number
  avg_first_token_ms: number
  p95_first_token_ms: number
  avg_duration_ms: number
  avg_tps: number
  success_rate: number
}

/** 向后兼容旧命名 DashboardBreakdown。 */
export type DashboardBreakdown = BreakdownItem

export interface DashboardRuntime {
  accounts_total: number
  accounts_active: number
  api_keys_total: number
  container_nodes_total?: number
  container_nodes_running?: number
  bucket: 'hour' | 'day' | string
  /** 各平台的账号数，形如 {"workbuddy": 3, "antigravity": 2}。 */
  accounts_by_platform: Record<string, number>
}

export interface DashboardResponse {
  summary: DashboardSummary
  series: DashboardSeriesPoint[]
  models: BreakdownItem[]
  accounts: BreakdownItem[]
  by_platform?: BreakdownItem[]
  by_group?: BreakdownItem[]
  runtime: DashboardRuntime
  start: string
  end: string
}

// ---- 上游账号池 ----

/** 兼容保留的滚动额度窗口类型。 */
export interface AccountBalanceWindow {
  used: number
  cap: number
  exceeded: boolean
  reset_at: string | null
}

/** WorkBuddy 资源包明细。 */
export interface AccountCreditPackage {
  package_code: string
  package_name: string
  kind: string
  remain: number
  total: number
  expired_at: string | null
}

/**
 * 上游账号的积分与额度快照。
 */
export interface AccountBalance {
  supported: boolean
  fetched_at: string | null
  error: string | null
  remaining: number | null
  total: number | null
  total_credits: number | null
  packages: AccountCreditPackage[]
  plan_id: string
  plan_name: string
  period_end?: string | null
  five_hour?: AccountBalanceWindow | null
  weekly?: AccountBalanceWindow | null
}

/** 自动收割单个步骤详情。 */
export interface HarvestStep {
  name: string
  kind: 'ok' | 'skip' | 'fail'
  credit: number
  detail: string
}

/** 最近一次收割摘要。 */
export interface HarvestSummary {
  credit_gained?: number
  error?: string
  steps?: HarvestStep[]
}

/** 手动刷新积分/余额的响应。 */
export interface AccountBalanceRefreshResult {
  ok: boolean
  error?: string | null
  account?: Account
}

/** 一键刷新全部账号积分/余额的响应。 */
export interface AccountBalanceRefreshAllResult {
  ok: boolean
  total: number
  refreshed: number
  unsupported: number
  failed: number
  items: Array<{ id: number; name: string; supported: boolean; error?: string }>
  accounts: Account[]
}

export interface Account {
  id: number
  name: string
  notes: string
  platform: UpstreamPlatform
  account_mode: string
  base_url: string
  type: string
  status: AccountStatus | string
  error_message: string | null
  masked_key: string
  /** 账号独立代理 IP，如 http://user:pass@ip:port 或 socks5://... */
  proxy_url: string
  /** 成对设备指纹 qimei36（36 位 hex）。 */
  qimei36: string
  /** 成对设备标识 machineId（UUID）。 */
  machine_id: string
  uid: string
  nickname: string
  email: string
  enterprise_id: string
  enterprise_name: string
  domain: string
  last_checkin_date: string
  checkin_streak: number
  last_harvest_at: string | null
  last_harvest_summary: HarvestSummary | null
  concurrency: number
  priority: number
  rate_multiplier: number
  schedulable: boolean
  consecutive_failures: number
  last_used_at: string | null
  expires_at: string | null
  last_health_check_at: string | null
  last_health_check_ok: boolean
  last_health_check_error: string | null
  latency_ms: number | null
  group_ids: number[]
  balance: AccountBalance
  created_at: string
  updated_at: string
}

export interface AccountListResponse {
  items: Account[]
  total: number
  page: number
  page_size: number
}

export interface AccountQuery extends PageQuery {
  status?: string
  keyword?: string
  group_id?: number | null
  platform?: string
}

export interface CreateAccountPayload {
  name: string
  platform?: UpstreamPlatform
  account_mode?: string
  base_url?: string
  notes?: string
  api_key: string
  refresh_token?: string
  uid?: string
  nickname?: string
  email?: string
  domain?: string
  proxy_url?: string
  qimei36?: string
  machine_id?: string
  concurrency?: number
  priority?: number
  rate_multiplier?: number
  status?: string
  schedulable?: boolean
  group_ids?: number[]
  expires_at?: string | null
}

export interface UpdateAccountPayload {
  name?: string
  platform?: UpstreamPlatform
  account_mode?: string
  base_url?: string
  notes?: string
  api_key?: string
  refresh_token?: string
  uid?: string
  nickname?: string
  email?: string
  domain?: string
  proxy_url?: string
  qimei36?: string
  machine_id?: string
  concurrency?: number
  priority?: number
  rate_multiplier?: number
  status?: string
  schedulable?: boolean
  group_ids?: number[]
  expires_at?: string | null
}

export interface AccountCheckResult {
  ok: boolean
  latency_ms: number
  error: string | null
}

export interface ScanLocalPayload {
  platform?: UpstreamPlatform | string
  group_ids?: number[]
  auto_assign_default_group?: boolean
}

export interface ScanLocalResult {
  imported: number
  updated: number
  sources: string[]
  accounts: Account[]
  errors?: string[]
}

export interface ExportAccountsResult {
  exported_at: string
  count: number
  accounts: Record<string, unknown>[]
}

export interface ProxyTestResult {
  ok: boolean
  exit_ip: string
  latency_ms: number
  proxy_url: string
  error?: string
}

export interface RotateDeviceResult {
  ok: boolean
  qimei36: string
  machine_id: string
  account: Account
}

export interface LoginLinkResult {
  ok: boolean
  device_code: string
  expires_in: number
  login_url: string
}

export interface HarvestResult {
  ok: boolean
  credit_gained: number
  total_credits: number | null
  steps: HarvestStep[]
  account?: Account
  error?: string
}

export interface HarvestAllItem {
  id: number
  name: string
  ok: boolean
  credit_gained: number
  steps: HarvestStep[]
  error?: string
}

export interface HarvestAllResult {
  ok: boolean
  total: number
  succeeded: number
  failed: number
  credit_gained: number
  items: HarvestAllItem[]
  accounts: Account[]
}

export interface TokenRefreshResult {
  ok: boolean
  account?: Account
  error?: string
}

export interface EmailChangeResponse {
  message: string
  token: string
  expires_at: string
  user: AdminUser
}

export interface BatchImportPayload {
  keys: string
  platform?: UpstreamPlatform
  domain?: string
  proxy_url?: string
  account_mode?: string
  base_url?: string
  group_ids?: number[]
  concurrency?: number
  priority?: number
  upsert?: boolean
}

export interface BatchImportFailure {
  line: number
  key: string
  error: string
}

export interface BatchImportResult {
  created: number
  updated: number
  created_ids: number[]
  failed: number
  failures: BatchImportFailure[]
}

// ---- 分组与倍率路由 ----

export interface Group {
  id: number
  name: string
  description: string
  platform: UpstreamPlatform
  rate_multiplier: number
  status: CommonStatus | string
  system_prompt?: string
  prompt_inject_mode?: PromptInjectMode
  prompt_template_id?: number | null
  tool_obf_mode?: GroupToolObfMode
  tool_obf_prefix?: string
  tool_obf_scope?: string
  tool_obf_custom_map?: Record<string, string>
  tool_obf_exclude_tools?: string[]
  tool_obf_rewrite_refs?: boolean | null
  tool_obf_effective_on?: boolean
  account_count: number
  api_key_count: number
  created_at: string
  updated_at: string
}

export interface GroupListResponse {
  items: Group[]
  total: number
}

export interface GroupPayload {
  name: string
  description?: string
  platform?: UpstreamPlatform
  rate_multiplier?: number
  status?: string
  system_prompt?: string
  prompt_inject_mode?: PromptInjectMode
  prompt_template_id?: number | null
  tool_obf_mode?: GroupToolObfMode
  tool_obf_prefix?: string
  tool_obf_scope?: string
  tool_obf_custom_map?: Record<string, string>
  tool_obf_exclude_tools?: string[]
  tool_obf_rewrite_refs?: boolean
}

// ---- 下游调用密钥 ----

export interface ApiKey {
  id: number
  name: string
  key: string
  user_id: number
  group_id: number | null
  group_name: string
  status: CommonStatus | string
  quota: number
  quota_used: number
  ip_whitelist: string[] | null
  last_used_at: string | null
  expires_at: string | null
  created_at: string
  updated_at: string
}

export interface ApiKeyListResponse {
  items: ApiKey[]
  total: number
}

export interface ApiKeyQuery {
  group_id?: number | null
  status?: string
  keyword?: string
}

export interface ApiKeyPayload {
  name?: string
  group_id?: number | null
  quota?: number
  ip_whitelist?: string[]
  expires_at?: string
  status?: string
}

// ---- 远程管理密钥 (/admin-keys) & 代理 IP 池 (/proxy-pool) ----

export type ProxyPoolStrategy = 'round_robin' | 'least_used' | 'sticky_unique' | 'random'
export type KeyProxyMode = 'inherit' | 'pool' | 'fixed' | 'fetch' | 'direct'

export interface AdminKeyProxyPolicy {
  mode: KeyProxyMode | string
  fixed_proxy_url?: string
  fixed_proxy_masked?: string
  pool_tag?: string
  strategy?: ProxyPoolStrategy | 'inherit' | string
  fetch_url_override?: string
  allow_override_in_push?: boolean
}

export interface ProxyPoolEntry {
  id: string
  url: string
  masked_url?: string
  tag?: string
  note?: string
  enabled: boolean
  source?: 'manual' | 'remote_fetch' | string
  added_at?: string
  last_used_at?: string
  last_check_ok?: boolean | null
  last_exit_ip?: string
  last_latency_ms?: number
  last_error?: string
  bound_accounts?: number
}

export interface ProxyFetchConfig {
  enabled: boolean
  url: string
  method: 'GET' | 'POST' | string
  headers?: Record<string, string>
  body?: string
  default_scheme: 'http' | 'https' | 'socks5' | 'socks5h' | string
  username?: string
  password?: string
  default_tag?: string
  json_path?: string
  auto_fetch_on_empty: boolean
  replace_on_fetch: boolean
  interval_minutes: number
  timeout_seconds: number
  last_fetched_at?: string
  last_fetch_count?: number
  last_fetch_error?: string
}

export interface ProxyPoolConfig {
  enabled: boolean
  strategy: ProxyPoolStrategy | string
  max_accounts_per_ip: number
  apply_platforms?: string[]
  entries: ProxyPoolEntry[]
  fetch: ProxyFetchConfig
  key_policies?: Record<number, AdminKeyProxyPolicy>
  cursor?: number
}

export interface ProxyPoolStats {
  total_entries: number
  enabled_entries: number
  manual_entries: number
  fetched_entries: number
  bound_total: number
  tags: string[]
}

export interface ProxyPoolView {
  config: ProxyPoolConfig
  stats: ProxyPoolStats
}

export interface ProxyFetchResult {
  ok: boolean
  fetched_url: string
  parsed_count: number
  added_count: number
  updated_count: number
  total_pool_size: number
  dry_run: boolean
  sample_proxies: string[]
  error?: string
}

export interface ProxyProbeResult {
  id?: string
  masked_url: string
  ok: boolean
  exit_ip?: string
  latency_ms: number
  error?: string
}

export interface AdminKey {
  id: number
  name: string
  key_prefix: string
  masked_key?: string
  scopes: AdminKeyScope | string
  allowed_ips: string
  status: CommonStatus | string
  last_used_at: string | null
  expires_at: string | null
  is_bootstrap?: boolean
  created_at: string
  updated_at: string
  proxy_policy?: AdminKeyProxyPolicy
}

export interface AdminKeyConnectionInfo {
  base_url: string
  push_accounts_url?: string
  push_proxy_url?: string
  headers: Record<string, string>
  examples?: Record<string, string>
}

export interface AdminKeyListResponse {
  items: AdminKey[]
  total?: number
  connection: AdminKeyConnectionInfo
}

export interface CreateAdminKeyPayload {
  name: string
  scopes?: AdminKeyScope | string
  allowed_ips?: string
  expires_at?: string | null
  status?: CommonStatus | string
  proxy_policy?: AdminKeyProxyPolicy
}

export interface CreateAdminKeyResponse {
  key: string
  item: AdminKey
}

export interface UpdateAdminKeyPayload {
  name?: string
  scopes?: AdminKeyScope | string
  allowed_ips?: string
  expires_at?: string | null
  clear_expiry?: boolean
  status?: CommonStatus | string
  proxy_policy?: AdminKeyProxyPolicy
}

// ---- 调用明细日志 ----

export interface UsageLog {
  id: number
  model: string
  platform?: string
  request_id: string
  user_id: number
  api_key_id: number
  api_key_name: string
  account_id: number
  account_name: string
  group_id: number | null
  group_name?: string
  input_tokens: number
  output_tokens: number
  cache_creation_tokens: number
  cache_read_tokens: number
  reasoning_tokens?: number
  total_cost: number
  input_cost?: number
  output_cost?: number
  cache_cost?: number
  cache_hit_rate?: number
  tps?: number
  rate_multiplier: number
  stream: boolean
  duration_ms: number | null
  first_token_ms: number | null
  status_code: number
  error_message: string | null
  ip_address: string | null
  user_agent: string | null
  created_at: string
}

export interface UsageLogListResponse {
  items: UsageLog[]
  total: number
  page: number
  page_size: number
  start: string
  end: string
}

export type LogStatusFilter = '' | 'ok' | 'error'

export interface UsageLogQuery extends PageQuery {
  start?: string
  end?: string
  model?: string
  platform?: string
  group_id?: number | null
  api_key_id?: number | null
  account_id?: number | null
  status?: LogStatusFilter
  request_id?: string
}

// ---- 模型广场与标准价格库 (/models/...) ----

export interface ModelInfo {
  id: string
  name: string
}

export interface ModelListResponse {
  items: ModelInfo[]
  total?: number
  hint?: string
}

export interface ModelPrice {
  id: number
  model: string
  input_price: number
  output_price: number
  cache_read_price: number
  cache_write_price: number
  notes: string
  is_builtin?: boolean
  created_at?: string
  updated_at?: string
}

export interface ModelPriceListResponse {
  items: ModelPrice[]
  total: number
}

export interface UpsertModelPricePayload {
  id?: number
  model: string
  input_price: number
  output_price: number
  cache_read_price?: number
  cache_write_price?: number
  notes?: string
}

export interface CatalogModelItem {
  id: string
  name: string
  vendor?: string
  platforms: string[]
  platform?: string
  context_window?: number
  max_output_tokens?: number
  supports_images?: boolean
  supports_reasoning?: boolean
  supports_tool_call?: boolean
  supports_cache?: boolean
  input_price?: number
  output_price?: number
  cache_read_price?: number
  cache_write_price?: number
  price_source?: 'custom' | 'builtin' | 'upstream' | string
  account_count?: number
  description?: string
}

export interface ModelCatalogResponse {
  items: CatalogModelItem[]
  total: number
  by_platform?: Record<string, number>
  refreshed_at?: string | null
  cached?: boolean
  errors?: Record<string, string>
  prices?: ModelPrice[]
}

// ---- 系统与超时配置 (/settings) ----

export interface SettingsUpstream {
  base_url: string
  project_slug?: string
  cli_version?: string
  fingerprint_salt_set?: boolean
  fingerprint_salt_masked?: string
  empty_system_placeholder: boolean
  first_token_timeout?: string
  stream_idle_timeout: string
  nonstream_idle_timeout: string
  max_body_mb: number
  model_cache_ttl: string
  upstream_proxy?: string
  upstream_proxy_set?: boolean
  upstream_proxy_masked?: string
  auto_harvest_enabled?: boolean
  auto_harvest_interval?: string
}

export interface SettingsAuth {
  token_ttl: string
  bootstrap_admin_key_set?: boolean
  admin_keys_count?: number
}

export interface SettingsHealthCheck {
  enabled: boolean
  interval: string
  failure_threshold: number
  timeout: string
}

export interface SettingsDatabase {
  driver?: string
  sqlite_path?: string
  data_dir?: string
}

export interface SettingsFilter {
  enabled?: boolean
  zero_width_disabled?: boolean
  rules_count?: number
  enabled_rules?: number
  global_system_prompt_enabled?: boolean
  global_system_prompt?: string
}

export interface SettingsTimeouts {
  first_token_timeout: string
  first_token_seconds: number
  stream_idle_timeout: string
  stream_idle_seconds: number
  nonstream_idle_timeout: string
  nonstream_idle_seconds: number
}

export interface SettingsAntigravity {
  docker_available?: boolean
  docker_version?: string
  default_image?: string
  data_dir?: string
  nodes_count?: number
}

export interface SettingsResponse {
  upstream: SettingsUpstream
  auth: SettingsAuth
  health_check: SettingsHealthCheck
  database?: SettingsDatabase
  filter?: SettingsFilter
  timeouts?: SettingsTimeouts
  antigravity?: SettingsAntigravity
  tool_obfuscation?: SettingsToolObfuscation
  runtime_overrides?: Record<string, string>
}

export interface RuntimeSettingsPayload {
  first_token_timeout?: string
  first_token_seconds?: number
  stream_idle_timeout?: string
  stream_idle_seconds?: number
  nonstream_idle_timeout?: string
  nonstream_idle_seconds?: number
  upstream_proxy?: string
  auto_harvest_enabled?: boolean
  auto_harvest_interval?: string
  global_system_prompt_enabled?: boolean
  global_system_prompt?: string
  settings?: Record<string, string>
}

// ---- AntiGravity 容器节点与登录文件 (/antigravity/...) ----

export interface AntigravityEnvResponse {
  docker_available: boolean
  docker_version: string
  server_os?: string
  error?: string
  default_image: string
  data_root: string
}

export interface ContainerRuntimeStatus {
  exists: boolean
  running: boolean
  container_id: string
  state: string
  status_text: string
  started_at: string
}

export interface NodeFingerprint {
  fingerprint_id: string
  qimei36: string
  machine_id: string
  hostname: string
  mac_address: string
  user_agent: string
  client_id: string
  os: string
  arch: string
  rotated_at: string
}

export interface ContainerNode {
  id: number
  name: string
  mode: ContainerNodeMode | string
  docker_image: string
  container_name: string
  container_id: string
  host_ip: string
  host_port: number
  base_url?: string
  auth_dir: string
  has_management_key?: boolean
  masked_management_key?: string
  has_api_key?: boolean
  masked_api_key?: string
  proxy_url: string
  strict_isolation?: boolean
  fingerprint?: NodeFingerprint
  isolation_status?: 'isolated' | 'duplicate_proxy' | 'no_proxy' | string
  isolation_warnings?: string[]
  last_exit_ip?: string
  last_ip_checked_at?: string
  status: 'running' | 'stopped' | 'error' | 'online' | 'offline' | string
  auth_files_count: number
  models_count?: number
  latency_ms?: number | null
  runtime?: ContainerRuntimeStatus | null
  last_sync_at: string | null
  last_error: string | null
  extra?: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface ContainerNodeListResponse {
  items: ContainerNode[]
  total?: number
}

export interface ContainerNodePayload {
  name?: string
  mode?: ContainerNodeMode | string
  docker_image?: string
  container_name?: string
  host_ip?: string
  host_port?: number
  auth_dir?: string
  management_key?: string
  api_key?: string
  proxy_url?: string
  strict_isolation?: boolean
  auto_start?: boolean
  extra?: Record<string, unknown>
}

export interface NodeExitIPResponse {
  ok: boolean
  exit_ip?: string
  proxy_used?: string
  direct: boolean
  latency_ms: number
  endpoint?: string
  error?: string
  node_id: number
  node_name: string
  isolation_status: string
  shared_with_nodes?: string[]
  node?: ContainerNode
}

export interface ContainerNodeActionResult {
  ok: boolean
  action: ContainerNodeAction | string
  message?: string
  error?: string | null
  latency_ms?: number
  models_count?: number
  auth_files_count?: number
  node?: ContainerNode
}

export interface ContainerNodeLogsResponse {
  node_id?: number
  container_name?: string
  tail?: number
  logs: string
}

export interface AntigravityAuthFile {
  filename: string
  provider: string
  email: string
  project_id: string
  account_type: string
  priority: number
  excluded_models: string[]
  expires_at: string
  expired: boolean
  disabled: boolean
  size_bytes: number
  updated_at: string
  source: 'remote_api' | 'local_dir' | string
  raw?: Record<string, unknown>
}

export interface AntigravityAuthFileListResponse {
  items: AntigravityAuthFile[]
  total?: number
}

export interface UploadAntigravityAuthFilePayload {
  filename: string
  content?: string | Record<string, unknown>
  raw_json?: string
  sync_account?: boolean
  group_ids?: number[]
}

export interface UploadAntigravityAuthFileResponse {
  ok?: boolean
  file?: AntigravityAuthFile
  item?: AntigravityAuthFile
  account?: Account | null
}

export interface RequestAntigravityOAuthPayload {
  provider: string
  redirect_uri?: string
  group_ids?: number[]
}

export interface AntigravityOAuthURLResponse {
  session_id?: string
  auth_url: string
  url?: string
  state: string
  provider: string
  mode?: string
  callback_url?: string
  fingerprint?: NodeFingerprint
  instructions?: string
}

export interface OAuthSession {
  session_id: string
  platform: string
  provider: string
  mode: string
  node_id?: number
  node_name?: string
  account_id?: number
  group_ids?: number[]
  proxy_url?: string
  account_mode?: AccountMode | string
  base_url?: string
  auth_url: string
  redirect_uri: string
  state: string
  code_challenge?: string
  fingerprint?: NodeFingerprint
  instructions: string
  status: 'pending' | 'completed' | 'failed' | string
  error?: string
  result_filename?: string
  result_account_id?: number
  created_at: string
  expires_at: string
  completed_at?: string | null
}

export interface CreateOAuthSessionPayload {
  platform?: string
  provider?: string
  node_id?: number
  account_id?: number
  group_ids?: number[]
  proxy_url?: string
  redirect_uri?: string
  redirect_mode?: boolean
  account_name?: string
  account_mode?: AccountMode | string
  base_url?: string
}

export interface CompleteOAuthCallbackPayload {
  session_id?: string
  state?: string
  platform?: string
  provider?: string
  node_id?: number
  group_ids?: number[]
  proxy_url?: string
  callback_url?: string
  redirect_url?: string
  redirect_uri?: string
  code?: string
  api_key?: string
  refresh_token?: string
  uid?: string
  nickname?: string
  domain?: string
  email?: string
  project_id?: string
  account_name?: string
  account_mode?: AccountMode | string
  base_url?: string
  filename?: string
  token_json?: Record<string, unknown> | string
  raw_json?: string
}

export interface OAuthCallbackResponse {
  ok: boolean
  session?: OAuthSession
  platform: string
  provider: string
  node_id?: number
  filename?: string
  auth_file?: AntigravityAuthFile
  account?: Account | null
  accounts?: Account[]
  remote_result?: Record<string, unknown>
  message: string
}

export interface SyncAntigravityAccountsPayload {
  group_ids?: number[]
  proxy_url?: string
  concurrency?: number
  priority?: number
  rate_multiplier?: number
  auto_assign_default_group?: boolean
}

export interface SyncAntigravityAccountsResponse {
  ok?: boolean
  created: number
  updated: number
  total?: number
  files_count?: number
  accounts?: Account[]
  node?: ContainerNode
  errors?: string[]
}

// ---- 前置提示词注入中心 (/prompts/...) ----

export interface PromptTemplate {
  id: number
  name: string
  category: string
  content: string
  description: string
  is_builtin: boolean
  priority: number
  created_at: string
  updated_at: string
}

export interface PromptTemplateListResponse {
  items: PromptTemplate[]
  total?: number
}

export interface PromptTemplatePayload {
  name?: string
  category?: string
  content?: string
  description?: string
  priority?: number
}

export interface PromptConfigResponse {
  global_enabled: boolean
  global_prompt: string
  groups: Group[]
  templates: PromptTemplate[]
}

export interface UpdateGlobalPromptPayload {
  enabled: boolean
  prompt: string
}

export interface UpdateGroupPromptPayload {
  prompt_inject_mode: PromptInjectMode
  system_prompt: string
  prompt_template_id?: number | null
  tool_obf_mode?: GroupToolObfMode
  tool_obf_prefix?: string
  tool_obf_scope?: string
  tool_obf_custom_map?: Record<string, string>
  tool_obf_exclude_tools?: string[]
  tool_obf_rewrite_refs?: boolean
}

export interface PromptPreviewPayload {
  group_id?: number | null
  client_system?: string
  model?: string
  protocol?: FilterPreviewProtocol | string
}

export interface PromptLayerApplied {
  layer?: string
  name?: string
  source?: string
  applied?: boolean
  length?: number
  detail?: string
}

export interface PromptPreviewResponse {
  final_system: string
  sanitized_client_system: string
  layers_applied: Array<string | PromptLayerApplied>
  inject_mode?: PromptInjectMode | string
  filter_hits?: FilterRuleHit[]
}

// ---- 领奖与保活中心 (/rewards/...) ----

export type RewardsAction =
  | 'harvest'
  | 'check'
  | 'balance_refresh'
  | 'token_refresh'
  | 'proxy_test'
  | 'check_and_harvest'

export interface RewardsPlatformSummary {
  platform: UpstreamPlatform | string
  total_accounts: number
  active_accounts: number
  error_accounts: number
  checked_in_today?: number
  total_credits?: number
  supports_harvest?: boolean
  last_harvest_at?: string | null
}

export interface RewardsOverviewSummary {
  total_accounts: number
  active_accounts: number
  workbuddy_total?: number
  checked_in_today: number
  unchecked_today?: number
  total_credits: number
  today_credits_gained?: number
  recent_harvest_count?: number
}

export interface RewardsOverviewResponse {
  summary?: RewardsOverviewSummary
  by_platform?: RewardsPlatformSummary[]
  accounts: Account[]
  cron_jobs?: CronJob[]
}

export interface RunRewardsActionPayload {
  platform?: UpstreamPlatform | string
  action: RewardsAction | string
  account_ids?: number[]
  group_id?: number | null
  do_chat?: boolean
}

export interface RewardsRunItem {
  account_id: number
  id?: number
  account_name: string
  name?: string
  platform: string
  ok: boolean
  skipped?: boolean
  duration_ms?: number
  latency_ms?: number
  credit_gained?: number
  total_credits?: number | null
  exit_ip?: string
  steps?: HarvestStep[]
  detail?: string
  error?: string
}

export interface RunRewardsActionResponse {
  ok: boolean
  action: string
  platform?: string
  total: number
  succeeded: number
  failed: number
  skipped?: number
  credit_gained?: number
  credits_gained?: number
  duration_ms?: number
  summary?: string
  items: RewardsRunItem[]
  accounts?: Account[]
}

// ---- 内容风控与词库 ----

export type FilterRuleKind = 'prune' | 'rewrite' | 'replace'
export type FilterPreviewMode = 'auto' | 'json' | 'system' | 'tool' | 'user_harness'
export type FilterPreviewProtocol = 'openai' | 'anthropic' | 'responses'

export interface FilterRule {
  id: number
  name: string
  kind: FilterRuleKind
  pattern: string
  replacement: string
  is_regex: boolean
  case_sensitive: boolean
  word_boundary: boolean
  scope: string
  priority: number
  enabled: boolean
  is_builtin: boolean
  description: string
  created_at: string
  updated_at: string
}

export interface FilterRuleListResponse {
  items: FilterRule[]
  total: number
  stats: {
    enabled: number
    prune: number
    rewrite: number
    replace: number
  }
}

export interface FilterRulePayload {
  name?: string
  kind?: FilterRuleKind
  pattern?: string
  replacement?: string
  is_regex?: boolean
  case_sensitive?: boolean
  word_boundary?: boolean
  scope?: string
  priority?: number
  enabled?: boolean
  description?: string
}

export interface FilterRuleResetResponse {
  items: FilterRule[]
  total: number
  message: string
}

export interface FilterRuleHit {
  rule_id: number
  name: string
  kind: string
  count: number
  pattern: string
  replacement: string
}

export interface FilterRulePreviewPayload {
  input: string
  mode?: FilterPreviewMode
  protocol?: FilterPreviewProtocol
}

export interface FilterRulePreviewResult {
  input: string
  output: string
  mode: string
  changed: boolean
  hits?: FilterRuleHit[]
  stats: {
    prune_hits: number
    rewrite_hits: number
    replace_hits: number
    fields_touched?: number
    hits?: FilterRuleHit[]
  }
}

// ---- 定时任务 (Cron) ----

export type CronAction =
  | 'harvest'
  | 'account_check'
  | 'proxy_test'
  | 'balance_refresh'
  | 'token_refresh'
  | 'check_and_harvest'

export type CronTargetScope = 'all' | 'group' | 'accounts'

export type CronRunStatus = '' | 'running' | 'success' | 'partial' | 'error'

export interface CronPreviewTime {
  rfc3339: string
  local: string
}

export interface CronAccountResult {
  account_id: number
  account_name: string
  platform: string
  ok: boolean
  skipped?: boolean
  duration_ms: number
  latency_ms?: number
  credit_gained?: number
  total_credits?: number | null
  exit_ip?: string
  proxy_url?: string
  steps?: HarvestStep[]
  detail?: string
  error?: string
}

export interface CronRunOutput {
  job_id: number
  job_name: string
  action: CronAction | string
  trigger: 'cron' | 'manual' | string
  status: CronRunStatus
  started_at: string
  finished_at: string
  duration_ms: number
  total: number
  succeeded: number
  failed: number
  skipped: number
  credits_gained: number
  summary: string
  items: CronAccountResult[]
}

export interface CronJobRunRecord {
  trigger: string
  status: CronRunStatus
  started_at: string
  finished_at: string
  duration_ms: number
  total: number
  succeeded: number
  failed: number
  skipped: number
  credits_gained: number
  summary: string
}

export interface CronJobLastResult extends CronJobRunRecord {
  items?: CronAccountResult[]
}

export interface CronJob {
  id: number
  name: string
  description: string
  cron_expr: string
  timezone: string
  action: CronAction
  target_scope: CronTargetScope
  group_id: number | null
  account_ids: number[]
  options: Record<string, unknown>
  enabled: boolean
  is_builtin: boolean
  running: boolean
  next_run_at: string | null
  next_runs: CronPreviewTime[]
  last_run_at: string | null
  last_status: CronRunStatus
  last_duration_ms: number
  last_summary: string
  last_result: CronJobLastResult | null
  run_history: CronJobRunRecord[]
  created_at: string
  updated_at: string
}

export interface CronJobListResponse {
  items: CronJob[]
  total: number
  stats: {
    total: number
    enabled: number
    running: number
  }
}

export interface CronJobPayload {
  name?: string
  description?: string
  cron_expr?: string
  timezone?: string
  action?: CronAction
  target_scope?: CronTargetScope
  group_id?: number | null
  clear_group?: boolean
  account_ids?: number[]
  options?: Record<string, unknown>
  enabled?: boolean
}

export interface CronPreviewResult {
  valid: boolean
  cron_expr: string
  timezone: string
  error?: string
  next_runs: CronPreviewTime[]
}

export interface CronRunNowResponse {
  ok: boolean
  result: CronRunOutput
  job: CronJob | null
}

export interface CronJobResetResponse {
  items: CronJob[]
  total: number
  message: string
}

// ---- 工具名混淆引擎 (/tool-obfuscation) ----

export type ToolObfuscationMode = 'hash' | 'alias' | 'prefix'
export type GroupToolObfMode = 'inherit' | 'off' | 'hash' | 'alias' | 'prefix'
export type ToolObfuscationScope = 'all' | 'sensitive_only'

export interface ToolObfuscationGroupOverride {
  mode: GroupToolObfMode
  prefix?: string
  scope?: string
  custom_map?: Record<string, string>
  exclude_tools?: string[]
  rewrite_tool_refs?: boolean | null
}

export interface GroupToolObfuscationSummary {
  group_id: number
  group_name: string
  platform: string
  override: ToolObfuscationGroupOverride
  effective_mode: string
  effective_enabled: boolean
}

export interface SettingsToolObfuscation {
  enabled: boolean
  mode: ToolObfuscationMode | string
  prefix: string
  scope: ToolObfuscationScope | string
  platforms: string[]
  custom_map_count: number
  exclude_count: number
  rewrite_refs: boolean
  group_overrides_count?: number
}

export interface ToolObfuscationConfig {
  enabled: boolean
  mode: ToolObfuscationMode | string
  prefix: string
  salt: string
  scope: ToolObfuscationScope | string
  platforms: string[]
  custom_map: Record<string, string>
  exclude_tools: string[]
  rewrite_tool_refs: boolean
  group_overrides?: Record<string, ToolObfuscationGroupOverride>
}

export interface ToolObfuscationStats {
  platforms_count: number
  custom_map_count: number
  exclude_count: number
  builtin_aliases_count: number
  sensitive_tools_count: number
  group_overrides_count?: number
}

export interface ToolObfuscationResponse {
  config: ToolObfuscationConfig
  builtin_aliases: Record<string, string>
  sensitive_tools: string[]
  groups?: GroupToolObfuscationSummary[]
  stats: ToolObfuscationStats
}

export interface ToolObfuscationPreviewPayload {
  group_id?: number
  tool_names?: string[]
  payload?: string
  protocol?: 'openai' | 'anthropic' | 'responses'
  platform?: string
  config?: ToolObfuscationConfig
}

export interface ToolObfuscationPreviewMappingEntry {
  original: string
  obfuscated: string
  restored: string
  changed: boolean
}

export interface ToolObfuscationPreviewResponse {
  mappings: ToolObfuscationPreviewMappingEntry[]
  obfuscated_payload?: string
  restored_verified: boolean
  changed_count: number
  total_count: number
  mode: string
  scope: string
}

// ---- 非额度 429 会话暂停保护 & 请求体 Log 审计 (/session-guard) ----

export interface SessionGuardConfig {
  enabled: boolean
  pause_seconds: number
  log_enabled: boolean
  log_dir: string
  max_log_files: number
  abort_current_retry: boolean
  fallback_fingerprint: boolean
  quota_keywords: string[]
}

export interface PausedSessionInfo {
  session_id: string
  paused_at: string
  paused_until: string
  remaining_seconds: number
  request_id: string
  group_id: number
  account_id: number
  platform: string
  protocol: string
  model: string
  reported_status: number
  error_code: string
  error_message: string
  log_file?: string
  log_path?: string
  obfuscated_tools_count: number
  unobfuscated_tools_count: number
  total_tools_count: number
  suspected_unobfuscated: boolean
}

export interface Session429LogFile {
  filename: string
  size_bytes: number
  mod_time: string
}

export interface SessionGuardResponse {
  config: SessionGuardConfig
  paused_sessions: PausedSessionInfo[]
  log_files: Session429LogFile[]
  paused_count: number
  log_files_count: number
}

// === Antigravity Format Adapter & Prompt Mode ===

export type AgyAccountSourceFormat =
  | 'agy_native'
  | 'gcloud_adc'
  | 'gemini_cli'
  | 'cpa_export'
  | 'json_array'
  | 'ndjson'
  | 'csv_tsv'
  | 'delimited_text'
  | 'base64_blob'
  | 'zip_archive'
  | string

export interface ConvertedAGYAccount {
  index: number
  source_format: AgyAccountSourceFormat
  inner_format?: AgyAccountSourceFormat
  source_file?: string
  email: string
  name: string
  client_id?: string
  has_client_secret?: boolean
  project_id?: string
  masked_refresh_token?: string
  masked_access_token?: string
  has_refresh_token?: boolean
  has_access_token?: boolean
  expires_at?: string
  expiry_date_ms?: number
  expired?: boolean
  proxy?: string
  valid: boolean
  duplicate?: boolean
  error?: string
  warnings?: string[]
  auth_filename?: string
  agy_cli_files?: Record<string, string>
}

export interface AntigravityConvertSummary {
  total: number
  valid_count: number
  invalid_count: number
  duplicate_count: number
  detected_formats?: string[]
}

export interface AntigravityConvertImportResult {
  imported: number
  created: number
  updated: number
  failed: number
  node_id?: number
  group_ids?: number[]
  accounts?: Account[]
  errors?: string[]
}

export interface AntigravityConvertAccountsPayload {
  raw_text?: string
  filename?: string
  files?: Array<{ filename: string; content: string }>
  auto_import?: boolean
  node_id?: number
  group_ids?: number[]
  proxy_url?: string
  auto_provision?: boolean
  deduplicate?: boolean
}

export interface AntigravityConvertAccountsResponse {
  ok: boolean
  total: number
  valid_count: number
  invalid_count: number
  duplicate_count: number
  detected_formats?: string[]
  summary?: AntigravityConvertSummary
  accounts: ConvertedAGYAccount[]
  items?: ConvertedAGYAccount[]
  bundle_filename?: string
  bundle_zip_base64?: string
  bundle_json?: Record<string, unknown> | string
  import_result?: AntigravityConvertImportResult | null
}

/** AGY CLI 提示词运行管线模式。 */
export type AgyPromptMode =
  | 'default_intercept_continue'
  | 'blend_sanitized'
  | 'native_only'
  | 'downstream_only'
  | string

export interface AgyPromptStageMeta {
  step?: number
  id?: string
  key?: string
  title: string
  summary?: string
  description?: string
  content?: string
}

export interface AgyPromptModeOption {
  mode: AgyPromptMode
  name?: string
  label?: string
  description: string
  recommended?: boolean
  aliases?: string[]
  stages?: Array<string | AgyPromptStageMeta>
}

export interface AgyInterceptContinuePreview {
  ok?: boolean
  mode: AgyPromptMode
  has_tools?: boolean
  tool_tag?: string
  original_downstream?: string
  sanitized_downstream?: string
  inbound_intercept_hits?: string[]
  native_stage?: string
  intercept_stage?: string
  continue_stage?: string
  gemini_md?: string
  final_prompt?: string
  outbound_filter_rules?: string[]
  stages?: AgyPromptStageMeta[]
}

export interface AgyPromptModesResponse {
  ok?: boolean
  active_mode: AgyPromptMode
  default_mode: AgyPromptMode
  runtime_override?: string
  env_override?: string
  modes: AgyPromptModeOption[]
  pipeline_stages?: AgyPromptStageMeta[]
  preview?: AgyInterceptContinuePreview
}

export interface UpdateAgyPromptModePayload {
  mode: AgyPromptMode
  downstream_system?: string
  has_tools?: boolean
}

export interface PreviewAgyPromptModePayload {
  mode?: AgyPromptMode
  downstream_system: string
  user_message?: string
  has_tools?: boolean
}

// === Platform Prompt Catalog & Pipeline Preview ===

/** 主流编程智能体与上游平台标识（提示词图鉴库筛选用）。 */
export type CatalogPlatformKey =
  | 'antigravity'
  | 'claude_code'
  | 'codex_cli'
  | 'dsh'
  | 'cursor'
  | 'windsurf'
  | 'cline'
  | 'gemini_cli'
  | 'opencode'
  | 'commandcode'
  | 'workbuddy'
  | string

/** 提示词图鉴卡片场景分类标签。 */
export type CatalogScenarioTag = '原生身份' | '拦截接续桥' | '工具协议' | '极简终端' | string

/** 各平台系统提示词图鉴单条元数据（GET /api/prompts/catalog）。 */
export interface PromptCatalogItem {
  id: string
  platform: CatalogPlatformKey
  platform_label?: string
  category: string
  scenario_tag?: CatalogScenarioTag
  scenario?: CatalogScenarioTag
  name: string
  title?: string
  description: string
  role?: string
  recommended_agy_mode?: AgyPromptMode
  recommended_inject_mode?: PromptInjectMode
  tags?: string[]
  highlights?: string[]
  content: string
  priority?: number
  is_builtin?: boolean
  template_id?: number | null
  already_imported?: boolean
}

export interface PromptCatalogPlatformSummary {
  platform: string
  label?: string
  platform_label?: string
  count: number
}

export interface PromptCatalogQuery {
  platform?: string
  q?: string
  keyword?: string
  search?: string
  category?: string
  role?: string
}

export interface PromptCatalogResponse {
  items: PromptCatalogItem[]
  total?: number
  platforms?: PromptCatalogPlatformSummary[] | Record<string, number>
  categories?: string[]
}

/** 一键将图鉴条目导入模板库并可选绑定到分组（POST /api/prompts/catalog/apply）。 */
export interface ApplyPromptCatalogPayload {
  catalog_id: string
  id?: string
  group_id?: number | null
  platform?: string
  enabled?: boolean
  inject_mode?: PromptInjectMode
  name?: string
  category?: string
  priority?: number
}

export interface ApplyPromptCatalogResponse {
  ok?: boolean
  created?: boolean
  updated?: boolean
  catalog_id?: string
  template: PromptTemplate
  group?: Group | null
  bound_groups?: Group[]
  enabled?: boolean
  inject_mode?: PromptInjectMode | string
  message?: string
}

/** 识别出的客户端 Harness 包裹标签条目。 */
export interface DetectedHarnessItem {
  tag: string
  platform?: string
  scope?: string
  action?: string
  preview?: string
}

/** 拦截与接续分步管线阶段快照。 */
export interface FilterPipelineStage {
  stage?: string
  key?: string
  name?: string
  title?: string
  label?: string
  description?: string
  system_prompt?: string
  user_message?: string
  content?: string
  output?: string
  changed?: boolean
}

/** 实时清洗与 AGY 默认提示词拦截接续统一预览载荷（POST /api/filter-rules/preview）。 */
export interface FilterPipelinePreviewPayload {
  /** 兼容传统单文本 / JSON 载荷预览模式。 */
  input?: string
  mode?: FilterPreviewMode | string
  protocol?: FilterPreviewProtocol | string
  /** 拦截与接续实时沙盒双输入参数。 */
  system_prompt?: string
  user_message?: string
  agy_mode?: AgyPromptMode
  has_tools?: boolean
  group_id?: number | null
}

/** 实时清洗与 AGY 拦截接续预览响应（同时兼容旧版单字段与新版全链路管线字段）。 */
export interface FilterPipelinePreviewResponse {
  input?: string
  output?: string
  mode?: string
  protocol?: string
  changed?: boolean
  hits?: FilterRuleHit[]
  matched_rules?: FilterRuleHit[]
  stats?: {
    prune_hits: number
    rewrite_hits: number
    replace_hits: number
    harness_spans?: number
    fields_touched?: number
    hits?: FilterRuleHit[]
  }
  agy_mode?: AgyPromptMode
  has_tools?: boolean
  original_system?: string
  system_prompt?: string
  original_user?: string
  user_message?: string
  harness_stripped_system?: string
  harness_stripped_user?: string
  detected_harnesses?: Array<DetectedHarnessItem | string>
  stripped_harnesses?: Array<DetectedHarnessItem | string>
  harness_tags?: string[]
  sanitized_system?: string
  filtered_system?: string
  sanitized_user?: string
  filtered_user?: string
  agy_native_prompt?: string
  intercept_bridge?: string
  interception_bridge?: string
  agy_continuation_preview?: string
  agy_final_prompt?: string
  final_prompt?: string
  final_user?: string
  final_system?: string
  stages?: FilterPipelineStage[]
}
