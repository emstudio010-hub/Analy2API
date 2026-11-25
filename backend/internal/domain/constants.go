// Package domain 存放跨层共享的枚举和常量。
package domain

import "strings"

// 状态常量。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusError    = "error"
)

// 角色常量。cmd2api 仅管理员模式，注册入口关闭，user 角色只为将来扩展保留。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// 上游平台。
//
// 平台决定了路由目标与上游协议形态：
//
//	workbuddy   —— WorkBuddy 桌面端逆向协议（默认，/v2/chat/completions + 设备指纹 + 积分收割）。
//	antigravity —— AntiGravity / CLIProxyAPI 双模容器与远程节点（OpenAI 兼容 + 管理 API + OAuth JSON）。
//	commandcode —— 兼容保留：Command Code 自有协议。
//	opencode    —— 兼容保留：OpenAI 兼容协议。
//
// 分组绑定平台（见 Group.platform），请求按分组路由到对应平台的账号池，
// 避免「某个模型只在一个平台上有，却被路由到另一个平台」。
const (
	PlatformWorkBuddy   = "workbuddy"
	PlatformAntigravity = "antigravity"
	PlatformCommandCode = "commandcode"
	PlatformOpenCode    = "opencode"
)

// WorkBuddyDefaultDomain 是 WorkBuddy 默认上游域名。
const WorkBuddyDefaultDomain = "https://www.codebuddy.cn"

// 账号认证类型。
const (
	AccountTypeWorkBuddy = "workbuddy_oauth"
	AccountTypeAPIKey    = "apikey"
	AccountTypeOAuthJSON = "oauth_json"
)

// CommandCodeKeyPrefix 是 Command Code 密钥的前缀。
// 只对 commandcode 平台做这个校验——OpenCode 的密钥格式不同。
const CommandCodeKeyPrefix = "user_"

// OpenCode 的两种计费模式，存在账号的 extra.account_mode 里。
//
// 两者只是 base_url 不同，协议与鉴权一样；分开是为了让后台能按模式分组展示，
// 也方便将来针对订阅额度做窗口监控。
const (
	// AccountModeZen 是按量付费：https://opencode.ai/zen/v1
	AccountModeZen = "zen"
	// AccountModeGo 是订阅额度：https://opencode.ai/zen/go/v1
	AccountModeGo = "go"
)

// OpenCodeBaseURLZen / OpenCodeBaseURLGo 是两种模式的默认上游地址。
//
// 账号里可以覆盖（存 extra.base_url），适用于自建中转或私有部署的场景。
const (
	OpenCodeBaseURLZen = "https://opencode.ai/zen/v1"
	OpenCodeBaseURLGo  = "https://opencode.ai/zen/go/v1"
)

// DefaultOpenCodeBaseURL 按计费模式返回默认上游地址。
func DefaultOpenCodeBaseURL(accountMode string) string {
	if accountMode == AccountModeGo {
		return OpenCodeBaseURLGo
	}
	return OpenCodeBaseURLZen
}

// NormalizePlatform 规范化平台取值，空白输入默认回退到 workbuddy。
func NormalizePlatform(p string) string {
	s := strings.ToLower(strings.TrimSpace(p))
	switch s {
	case "", "wb", "work-buddy", "work_buddy", "codebuddy", PlatformWorkBuddy:
		return PlatformWorkBuddy
	case "agy", "anti-gravity", "anti_gravity", "cliproxy", "cliproxyapi", PlatformAntigravity:
		return PlatformAntigravity
	case "cc", "cmdc", "command-code", "command_code", PlatformCommandCode:
		return PlatformCommandCode
	case "oc", "open-code", "open_code", PlatformOpenCode:
		return PlatformOpenCode
	default:
		return s
	}
}

// IsValidPlatform 判断平台取值是否受支持。
func IsValidPlatform(p string) bool {
	return p == PlatformWorkBuddy ||
		p == PlatformAntigravity ||
		p == PlatformCommandCode ||
		p == PlatformOpenCode
}

// APIKeyPrefix 是签发给下游客户端的密钥前缀，便于在日志里一眼认出来源。
// LegacyAPIKeyPrefix 为兼容旧版签发的密钥前缀。
const (
	APIKeyPrefix       = "sk-wb2a-"
	LegacyAPIKeyPrefix = "sk-c2a-"
)

// HasValidAPIKeyPrefix 检查下游 API Key 是否具备合法前缀（兼容 sk-wb2a- 与 sk-c2a-）。
func HasValidAPIKeyPrefix(k string) bool {
	return strings.HasPrefix(k, APIKeyPrefix) || strings.HasPrefix(k, LegacyAPIKeyPrefix)
}

// AdminKeyPrefix 是远程管理 API 密钥前缀。
const AdminKeyPrefix = "adm-"

// 管理密钥权限范围常量。
const (
	AdminKeyScopeFull         = "full"
	AdminKeyScopeReadOnly     = "readonly"
	AdminKeyScopePushAccounts = "push_accounts"
)

// HasAdminKeyPrefix 检查密钥是否具备管理密钥前缀 adm-。
func HasAdminKeyPrefix(k string) bool {
	return strings.HasPrefix(k, AdminKeyPrefix)
}

// 分组系统提示词注入模式常量。
const (
	// PromptModeAppendGlobal 默认模式：全局底线提示词 + 分组完整提示词 + 客户端已清洗 system。
	PromptModeAppendGlobal = "append_global"
	// PromptModeOverrideGlobal 分组完整提示词覆盖全局底线 + 客户端已清洗 system。
	PromptModeOverrideGlobal = "override_global"
	// PromptModeReplaceAll 强制接管：仅用分组/全局提示词，剥离客户端原 system。
	PromptModeReplaceAll = "replace_all"
	// PromptModeOff 关闭额外提示词注入。
	PromptModeOff = "off"
)

// NormalizePromptInjectMode 规范化提示词注入模式取值，空白输入默认回退到 append_global。
func NormalizePromptInjectMode(m string) string {
	s := strings.ToLower(strings.TrimSpace(m))
	switch s {
	case "", "append", "append-global", PromptModeAppendGlobal:
		return PromptModeAppendGlobal
	case "override", "override-global", PromptModeOverrideGlobal:
		return PromptModeOverrideGlobal
	case "replace", "replace-all", PromptModeReplaceAll:
		return PromptModeReplaceAll
	case "none", "disable", "disabled", PromptModeOff:
		return PromptModeOff
	default:
		return s
	}
}

// IsValidPromptInjectMode 判断提示词注入模式是否受支持。
func IsValidPromptInjectMode(m string) bool {
	return m == PromptModeAppendGlobal ||
		m == PromptModeOverrideGlobal ||
		m == PromptModeReplaceAll ||
		m == PromptModeOff
}

// 容器节点运行模式常量。
const (
	ContainerModeLocalDocker = "local_docker"
	ContainerModeRemoteAPI   = "remote_api"
)

// NormalizeContainerMode 规范化容器节点运行模式，空白输入默认回退到 local_docker。
func NormalizeContainerMode(m string) string {
	s := strings.ToLower(strings.TrimSpace(m))
	switch s {
	case "", "local", "docker", "local-docker", ContainerModeLocalDocker:
		return ContainerModeLocalDocker
	case "remote", "api", "remote-api", ContainerModeRemoteAPI:
		return ContainerModeRemoteAPI
	default:
		return s
	}
}

// IsValidContainerMode 判断容器节点运行模式是否受支持。
func IsValidContainerMode(m string) bool {
	return m == ContainerModeLocalDocker || m == ContainerModeRemoteAPI
}

// 内容风控规则类型常量。
const (
	FilterKindPrune   = "prune"
	FilterKindRewrite = "rewrite"
	FilterKindReplace = "replace"
)

// UsageLog 里 billing_mode 的取值。
const (
	BillingModeToken = "token"
)
