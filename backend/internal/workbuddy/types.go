// Package workbuddy 实现 WorkBuddy 5.9.3 协议上游客户端。
//
// 仅依赖 Go 标准库与 golang.org/x/net/proxy，保持与业务数据库/框架完全解耦。
// 核心能力：
//   - 每账号独立代理 IP 池（http / https / socks5 / socks5h，禁用环境变量串流）
//   - 成对设备指纹（qimei36 + machineId）生成与校验
//   - WorkBuddy 5.9.3 桌面端请求头、会话聊天头与鉴权头
//   - Token 刷新、官网 300s 免密登录链接（DeviceAuth）
//   - PascalCase 账单积分高精度查询
//   - 全自动零成本积分收割流水线（Harvest，含 skill_share 四事件漏斗上报）
//   - 本机账号扫描与多格式批量导入解析
package workbuddy

import (
	"fmt"
	"net/http"
	"strings"
)

// 站点、客户端身份与路径常量（对齐 WorkBuddy 5.9.3.42398134 实测逆向口径）。
const (
	DefaultDomain     = "https://www.codebuddy.cn"
	DefaultDomainHost = "www.codebuddy.cn"
	DefaultFastModel  = "deepseek-v4.1-flash"

	UserAgentDesktop = "WorkBuddy/5.9.3 WorkBuddy/5.9.3"
	IDEType          = "WorkBuddy"
	IDEName          = "WorkBuddy"
	IDEVersion       = "5.9.3"
	ProductName      = "SaaS"
	ClientCommit     = "d46924219e79332f49c37ccebf27292f87001b19"

	PathChatCompletions   = "/v2/chat/completions"
	PathAnthropicMessages = "/v1/messages"
	PathReport            = "/v2/report"
	PathTokenRefresh      = "/v2/plugin/auth/token/refresh"
	PathDeviceAuthCode    = "/v2/plugin/device/auth/code"
	PathClientLogin       = "/console/client-login"
	PathBillingSummary    = "/billing/meter/get-user-resource-summary"
	PathDailyCheckin      = "/billing/meter/daily-checkin"
	PathGiftStatus        = "/billing/meter/check-gift-claimed"
	PathClaimGift         = "/billing/meter/claim-gift"
	PathCompStatus        = "/billing/meter/compensation-status"
	PathClaimComp         = "/billing/meter/claim-compensation"
	PathGrowthSubscribe   = "/v2/activity/growth/subscribe-task/status"
	PathGrowthTasks       = "/v2/activity/growth/tasks"
	PathGrowthTasksAccept = "/activity/growth/tasks/accept"
	PathGrowthStreak      = "/activity/growth/streak"
	PathGrowthRedeem      = "/activity/growth/redeem"
	PathGrowthBuddyQuota  = "/activity/growth/buddy/quota"
	PathGrowthBuddyOpen   = "/activity/growth/buddy/open"
	PathGrowthLottery     = "/activity/growth/lottery/summary"
	PathGrowthLotteryDraw = "/activity/growth/lottery/draw"

	// 已知业务返回码
	CodeOK               = 0
	CodeAlreadyCheckedIn = 10001
	CodeRefreshEmpty     = 10034
	CodeNonStreamDenied  = 11101
	CodeModelUnauth      = 11102
)

// AccountCreds 描述单个 WorkBuddy 上游账号的完整凭据与网络/设备隔离参数。
type AccountCreds struct {
	AccountID      int64  `json:"account_id,omitempty"`
	Name           string `json:"name,omitempty"`
	UID            string `json:"uid,omitempty"`
	Nickname       string `json:"nickname,omitempty"`
	Email          string `json:"email,omitempty"`
	EnterpriseID   string `json:"enterprise_id,omitempty"`
	EnterpriseName string `json:"enterprise_name,omitempty"`
	Domain         string `json:"domain,omitempty"`
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	ProxyURL       string `json:"proxy_url,omitempty"`
	Qimei36        string `json:"qimei36,omitempty"`
	MachineID      string `json:"machine_id,omitempty"`
}

// ModelInfo 表示过滤并归一化后的 WorkBuddy 对话模型信息。
type ModelInfo struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Vendor             string   `json:"vendor,omitempty"`
	Description        string   `json:"description,omitempty"`
	Credits            string   `json:"credits,omitempty"`
	CreditMultiplier   *float64 `json:"credit_multiplier,omitempty"`
	ContextWindow      int      `json:"context_window,omitempty"`
	MaxOutputTokens    int      `json:"max_output_tokens,omitempty"`
	MaxAllowedSize     int      `json:"max_allowed_size,omitempty"`
	SupportsImages     bool     `json:"supports_images,omitempty"`
	SupportsReasoning  bool     `json:"supports_reasoning,omitempty"`
	SupportsToolCall   bool     `json:"supports_tool_call,omitempty"`
	OnlyReasoning      bool     `json:"only_reasoning,omitempty"`
	CanDisableThinking *bool    `json:"can_disable_thinking,omitempty"`
	SupportedEfforts   []string `json:"reasoning_supported_efforts,omitempty"`
	DefaultEffort      string   `json:"reasoning_default_effort,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	IconURL            string   `json:"icon_url,omitempty"`
}

// CreditGroup 表示上游返回的积分分组明细（plan_base / plan_bonus / addon / benefit）。
type CreditGroup struct {
	Kind                string  `json:"kind"`
	Name                string  `json:"name"`
	CycleTotalCapacity  float64 `json:"cycle_total_capacity"`
	CycleRemainCapacity float64 `json:"cycle_remain_capacity"`
	CycleUsedCapacity   float64 `json:"cycle_used_capacity"`
}

// CreditPackage 表示单个资源包的剩余/总量与过期状态。
type CreditPackage struct {
	PackageCode  string   `json:"package_code"`
	PackageName  string   `json:"package_name"`
	DisplayName  string   `json:"display_name,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	Total        float64  `json:"total"`
	Remain       float64  `json:"remain"`
	Used         float64  `json:"used"`
	TotalCount   int      `json:"total_count,omitempty"`
	ExpiredAt    *string  `json:"expired_at,omitempty"`
	Expired      bool     `json:"expired,omitempty"`
	ExpiringSoon bool     `json:"expiring_soon,omitempty"`
	DaysLeft     *float64 `json:"days_left,omitempty"`
}

// BillingSummary 表示 get-user-resource-summary 返回的高精度积分快照。
type BillingSummary struct {
	TotalCredits            float64         `json:"total_credits"`
	SubscriptionPackageName string          `json:"subscription_package_name,omitempty"`
	IsPaidUser              bool            `json:"is_paid_user"`
	CreditGroups            []CreditGroup   `json:"credit_groups,omitempty"`
	Packages                []CreditPackage `json:"packages,omitempty"`
	Raw                     map[string]any  `json:"raw,omitempty"`
}

// DeviceAuthResult 表示一次性 300s 官网免密登录授权码与落地链接。
type DeviceAuthResult struct {
	DeviceCode string `json:"device_code"`
	ExpiresIn  int    `json:"expires_in"`
	LoginURL   string `json:"login_url"`
	Target     string `json:"target"`
}

// HarvestStep 表示积分收割流水线中的单个执行步骤结果。
type HarvestStep struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // "ok" | "skip" | "fail"
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped"`
	Credit  int    `json:"credit"`
	Detail  string `json:"detail"`
}

// HarvestResult 表示单个账号完整一次 Harvest 流水线的汇总结果。
type HarvestResult struct {
	OK            bool          `json:"ok"`
	AccountUID    string        `json:"account_uid"`
	CreditGained  int           `json:"credit_gained"`
	Streak        int           `json:"streak"`
	CheckedInDate string        `json:"checked_in_date,omitempty"`
	Steps         []HarvestStep `json:"steps"`
	Error         string        `json:"error,omitempty"`
}

// ImportedAccount 表示从批量文本、JSON 或本机扫描提取出的标准化账号条目。
type ImportedAccount struct {
	Name           string `json:"name"`
	UID            string `json:"uid,omitempty"`
	Nickname       string `json:"nickname,omitempty"`
	Email          string `json:"email,omitempty"`
	EnterpriseID   string `json:"enterprise_id,omitempty"`
	EnterpriseName string `json:"enterprise_name,omitempty"`
	Domain         string `json:"domain,omitempty"`
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	ProxyURL       string `json:"proxy_url,omitempty"`
	Qimei36        string `json:"qimei36,omitempty"`
	MachineID      string `json:"machine_id,omitempty"`
	DeviceName     string `json:"device_name,omitempty"`
	Source         string `json:"source,omitempty"`
}

// LocalScanResult 表示本机账号与设备指纹扫描结果。
type LocalScanResult struct {
	Sources  []string          `json:"sources"`
	Accounts []ImportedAccount `json:"accounts"`
	Errors   []string          `json:"errors,omitempty"`
}

// TokenRefreshResult 表示刷新 Token 后返回的新凭据与过期时间戳。
type TokenRefreshResult struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	Domain           string `json:"domain,omitempty"`
	ExpiresIn        int64  `json:"expires_in,omitempty"`
	RefreshExpiresIn int64  `json:"refresh_expires_in,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
	RefreshExpiresAt int64  `json:"refresh_expires_at,omitempty"`
	TokenType        string `json:"token_type,omitempty"`
}

// ProxyTestResult 表示独立代理连通性与出口 IP 探测结果。
type ProxyTestResult struct {
	OK        bool   `json:"ok"`
	ProxyURL  string `json:"proxy_url"`
	ExitIP    string `json:"exit_ip,omitempty"`
	LatencyMs int64  `json:"latency_ms"`
	Status    int    `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

// APIError 表示上游返回非 2xx HTTP 或非 0 业务 code 的结构化错误。
type APIError struct {
	Status    int
	Code      int
	Message   string
	RequestID string
	Raw       string
}

func (e *APIError) Error() string {
	var parts []string
	if e.Status > 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.Status))
	}
	if e.Code != 0 {
		parts = append(parts, fmt.Sprintf("code=%d", e.Code))
	}
	if strings.TrimSpace(e.Message) != "" {
		parts = append(parts, strings.TrimSpace(e.Message))
	} else if strings.TrimSpace(e.Raw) != "" {
		parts = append(parts, strings.TrimSpace(e.Raw))
	} else {
		parts = append(parts, "upstream error")
	}
	if e.RequestID != "" {
		parts = append(parts, "requestId="+e.RequestID)
	}
	return strings.Join(parts, " ")
}

// IsAuthFailure 判断是否为 401/403 或 refresh_token 失效类鉴权错误。
func (e *APIError) IsAuthFailure() bool {
	if e == nil {
		return false
	}
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden || e.Code == CodeRefreshEmpty
}

// IsNotSupported 判断是否为非流式被拒（11101）。
func (e *APIError) IsNotSupported() bool {
	return e != nil && e.Code == CodeNonStreamDenied
}

// IsModelUnauthorized 判断是否为模型未授权（11102）。
func (e *APIError) IsModelUnauthorized() bool {
	return e != nil && e.Code == CodeModelUnauth
}

// IsAlreadyDone 判断是否为已签到/活动已领/已过期等终态码（10001）。
func (e *APIError) IsAlreadyDone() bool {
	return e != nil && e.Code == CodeAlreadyCheckedIn
}
