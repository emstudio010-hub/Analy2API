// Package antigravity 实现 AntiGravity CLI / CLIProxyAPI 双模容器编排器、
// 宿主机 OAuth 登录凭证 JSON 管理、以及远程管理与 OpenAI 兼容流式推理客户端。
//
// 本包保持与业务数据库/ORM 零耦合，仅依赖 Go 标准库与 cmd2api/internal/workbuddy 独立代理池。
package antigravity

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"cmd2api/internal/workbuddy"
)

const (
	// 节点运行模式常量
	ModeLocalDocker = "local_docker"
	ModeRemoteAPI   = "remote_api"
	ModeDirectCLI   = "direct_cli"
	ModeLocalCLI    = "local_cli"

	// 登录凭证文件来源常量
	SourceRemoteAPI = "remote_api"
	SourceLocalDir  = "local_dir"

	// 默认容器与网络参数（内置 Analy2API 单账号真实 agy CLI 镜像，无需宿主机本地安装 agy）
	DefaultDockerImage   = "analy2api-agy-cli:latest"
	DefaultHostIP        = "127.0.0.1"
	DefaultHostPort      = 8317
	DefaultContainerPort = 8317

	// 容器内标准挂载路径
	ContainerAuthDir          = "/app/auths"
	ContainerGeminiDir        = "/root/.gemini"
	ContainerConfigFile       = "/app/config.yaml"
	ContainerLegacyConfigFile = "/CLIProxyAPI/config.yaml"

	// 默认 OAuth 提供方
	DefaultOAuthProvider = "antigravity"

	// agy CLI 会话磁盘保留策略（反代默认无状态即用即清，防止 conversations/*.db 与 brain/* 撑爆磁盘）
	SessionRetentionStateless = "stateless"
	SessionRetentionEphemeral = "ephemeral"
)

// NormalizeSessionRetention 规范化 agy CLI 会话磁盘保留策略（默认 stateless 零磁盘残留）。
func NormalizeSessionRetention(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case SessionRetentionEphemeral, "sticky", "keep", "session", "reuse":
		return SessionRetentionEphemeral
	default:
		return SessionRetentionStateless
	}
}

var containerNameSanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// NodeSpec 描述单个 AntiGravity / CLIProxyAPI 节点的完整编排与连接参数。
type NodeSpec struct {
	ID               int64           `json:"id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Mode             string          `json:"mode,omitempty"` // "local_docker" | "remote_api" | "local_cli"
	DockerImage      string          `json:"docker_image,omitempty"`
	ContainerName    string          `json:"container_name,omitempty"`
	HostIP           string          `json:"host_ip,omitempty"`
	HostPort         int             `json:"host_port,omitempty"`
	AuthDir          string          `json:"auth_dir,omitempty"`
	ConfigDir        string          `json:"config_dir,omitempty"`
	ManagementKey    string          `json:"management_key,omitempty"`
	APIKey           string          `json:"api_key,omitempty"`
	ProjectID        string          `json:"project_id,omitempty"`
	CLIBinary        string          `json:"cli_binary,omitempty"`
	ProxyURL         string          `json:"proxy_url,omitempty"`
	SessionRetention string          `json:"session_retention,omitempty"` // "stateless" (默认) | "ephemeral"
	PromptBlendMode  string          `json:"prompt_blend_mode,omitempty"` // "blend_clean" (默认) | "verbatim_only" | "native_raw"
	NativePrompt     string          `json:"native_prompt,omitempty"`     // 可选自定义 AGY 原生提示词底本（blend_clean 下会自动删误导段）
	StrictIsolation  bool            `json:"strict_isolation,omitempty"`
	Fingerprint      NodeFingerprint `json:"fingerprint,omitempty"`
}

// NormalizedMode 返回规范化后的节点模式（默认 local_docker）。
func (s NodeSpec) NormalizedMode() string {
	switch strings.ToLower(strings.TrimSpace(s.Mode)) {
	case ModeLocalCLI, "agy_cli", "agy", "cli":
		return ModeLocalCLI
	case ModeDirectCLI, "direct", "v1internal":
		return ModeDirectCLI
	case ModeRemoteAPI, "remote", "api":
		return ModeRemoteAPI
	default:
		return ModeLocalDocker
	}
}

// IsLocalDocker 判断是否为本机 Docker 托管模式。
func (s NodeSpec) IsLocalDocker() bool {
	return s.NormalizedMode() == ModeLocalDocker
}

// EffectiveImage 返回实际使用的 Docker 镜像名（为空时回退默认镜像）。
func (s NodeSpec) EffectiveImage() string {
	if img := strings.TrimSpace(s.DockerImage); img != "" {
		return img
	}
	return DefaultDockerImage
}

// EffectiveContainerName 返回合法的容器名称。
func (s NodeSpec) EffectiveContainerName() string {
	if c := SanitizeContainerName(s.ContainerName); c != "" {
		return c
	}
	if s.ID > 0 {
		return fmt.Sprintf("cmd2api-agy-%d", s.ID)
	}
	if n := SanitizeContainerName(s.Name); n != "" {
		return "cmd2api-agy-" + n
	}
	return "cmd2api-agy-node"
}

// EffectiveHostIP 返回节点绑定/访问的宿主机地址（默认 127.0.0.1）。
func (s NodeSpec) EffectiveHostIP() string {
	if h := strings.TrimSpace(s.HostIP); h != "" {
		return h
	}
	return DefaultHostIP
}

// EffectiveHostPort 返回节点映射/访问端口（默认 8317）。
func (s NodeSpec) EffectiveHostPort() int {
	if s.HostPort > 0 && s.HostPort <= 65535 {
		return s.HostPort
	}
	return DefaultHostPort
}

// SanitizeContainerName 将任意名称清洗为符合 Docker 命名规范的容器名。
func SanitizeContainerName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = containerNameSanitizeRe.ReplaceAllString(s, "-")
	s = strings.TrimLeft(s, ".-_")
	s = strings.TrimRight(s, ".-_")
	if len(s) > 96 {
		s = strings.TrimRight(s[:96], ".-_")
	}
	return s
}

// DockerEnvStatus 表示宿主机 Docker 引擎可用性与版本探测结果。
type DockerEnvStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	ServerOS  string `json:"server_os,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ContainerStatus 表示单个 Docker 容器的存在性与运行期状态快照。
type ContainerStatus struct {
	Exists      bool   `json:"exists"`
	Running     bool   `json:"running"`
	ContainerID string `json:"container_id,omitempty"`
	State       string `json:"state,omitempty"`
	StatusText  string `json:"status_text,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
}

// AuthFileInfo 表示归一化后的 AntiGravity / Gemini / Codex / Claude OAuth 登录凭证信息。
type AuthFileInfo struct {
	Filename       string         `json:"filename"`
	Provider       string         `json:"provider,omitempty"`
	Email          string         `json:"email,omitempty"`
	ProjectID      string         `json:"project_id,omitempty"`
	AccountType    string         `json:"account_type,omitempty"`
	Priority       int            `json:"priority"`
	ExcludedModels []string       `json:"excluded_models,omitempty"`
	ExpiresAt      string         `json:"expires_at,omitempty"`
	Expired        bool           `json:"expired"`
	Disabled       bool           `json:"disabled"`
	SizeBytes      int64          `json:"size_bytes,omitempty"`
	UpdatedAt      string         `json:"updated_at,omitempty"`
	Source         string         `json:"source,omitempty"` // "remote_api" | "local_dir"
	Raw            map[string]any `json:"raw,omitempty"`
}

// OAuthURLResult 表示从节点管理接口获取的 OAuth 交互式授权链接与状态参数。
type OAuthURLResult struct {
	AuthURL      string `json:"auth_url"`
	State        string `json:"state,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

// ModelItem 表示节点暴露的模型目录条目及智能推断的模型能力元数据。
type ModelItem struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Vendor            string `json:"vendor,omitempty"`
	ContextWindow     int    `json:"context_window,omitempty"`
	MaxOutputTokens   int    `json:"max_output_tokens,omitempty"`
	SupportsImages    bool   `json:"supports_images"`
	SupportsReasoning bool   `json:"supports_reasoning"`
	SupportsToolCall  bool   `json:"supports_tool_call"`
}

// APIError 表示节点管理接口或 /v1 推理接口返回的非 2xx 结构化错误。
type APIError struct {
	Status  int    `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Raw     string `json:"raw,omitempty"`
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	var parts []string
	if e.Status > 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.Status))
	}
	if strings.TrimSpace(e.Code) != "" {
		parts = append(parts, "code="+strings.TrimSpace(e.Code))
	}
	if strings.TrimSpace(e.Message) != "" {
		parts = append(parts, strings.TrimSpace(e.Message))
	} else if strings.TrimSpace(e.Raw) != "" {
		parts = append(parts, strings.TrimSpace(e.Raw))
	} else {
		parts = append(parts, "antigravity upstream error")
	}
	return strings.Join(parts, " ")
}

// IsAuthFailure 判断是否为 401/403 鉴权失败。
func (e *APIError) IsAuthFailure() bool {
	if e == nil {
		return false
	}
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// IsNotFound 判断是否为 404 端点或资源不存在。
func (e *APIError) IsNotFound() bool {
	return e != nil && e.Status == http.StatusNotFound
}

// IsRateLimited 判断是否为 429 限流错误。
func (e *APIError) IsRateLimited() bool {
	return e != nil && e.Status == http.StatusTooManyRequests
}

// ValidateProxyURL 复用 workbuddy.ValidateProxyURL 校验代理地址合法性。
func ValidateProxyURL(raw string) error {
	return workbuddy.ValidateProxyURL(raw)
}

// RedactProxyURL 复用 workbuddy.RedactProxyURL 对代理口令进行脱敏展示。
func RedactProxyURL(raw string) string {
	return workbuddy.RedactProxyURL(raw)
}
