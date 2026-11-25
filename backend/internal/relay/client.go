package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/config"
	"cmd2api/internal/workbuddy"
)

// 与 commandcode-proxy 对齐的时序常量。
const (
	// sessionDuration 是一个伪造会话的有效期。CLI 真机上会话不会短到分钟级，
	// 太频繁地换 session 本身就是异常信号。
	sessionDuration = 12 * time.Hour
	sessionJitter   = 1 * time.Hour

	// initRefresh 是「指纹 + 生命周期」两个预请求的重发周期。
	initRefresh = 8 * time.Hour
	initJitter  = 2 * time.Hour
)

// protocolVersion 是本实现**实际对齐**的 CLI wire 协议版本。
//
// 刻意写死而不是跟着 npm 上跑：真机发出去的永远是「形状 + 版本号自洽」的组合。
// 如果版本号跟着 npm 涨、形状却没改，就变成「自称最新版、却说旧方言」——
// 这比版本号略旧更容易被行为分析挑出来。升级前先读包对齐，再改这个常量。
//
// 1.66.0 已逐项比对过（command-code@1.66.0 的 dist/cli.mjs），形状没有变化：
//   - 头：Content-Type / User-Agent "cli" / x-command-code-version /
//     x-cli-environment / x-project-slug / x-taste-learning / x-session-id /
//     Authorization: Bearer，与 buildCommandAuthHeaders 一致。
//     1.66.0 新增的 x-oauth-token、x-oauth-provider、x-oss-primary-provider、
//     x-cmd-zdr、x-cmd-provider-deepseek-internal 都是条件发送的
//     （OAuth 登录、BYOK、ZDR、内部 provider），走 API key 这条路径不带。
//   - 会话 ID：sess_ + UUID 去横线后的前 16 位（generateSessionId）。
//   - 指纹体：{thumbmark, components}，盐 "command-code:device-fingerprint:v1"，
//     collectorVersion 1，runtime "cli"。
//   - 生命周期事件：{eventType:"cli_session_exists", metadata:{sessionId,
//     cliVersion, mode, os}}。
//   - 路径：/alpha/generate、/alpha/fingerprint/record、/alpha/lifecycle-events、
//     /alpha/billing/credits 全部未变。
//
// 唯一改掉的是会话 ID 的形状：原来 sessionFor 直接发一个带横线的 UUID，
// 而 CLI 发的是 sess_ 前缀那种。这个比版本号显眼得多。
const protocolVersion = "1.66.0"

// hardcodedModels 是拿不到动态模型列表时的兜底：GET /provider/v1/models 拉不到、
// 或者账号还没探活时用它，免得客户端拿到一个空列表。
//
// 内容直接抄自 CLI 自己的模型目录（command-code@1.66.0 的 dist/cli.mjs 里那张
// {SONNET_5:{id,name,...}} 表），按上游声明的顺序原样排列，只去掉带 hidden 的条目。
// 上一版是手挑的 26 条，一个版本就被甩下了（Opus 5.5、GPT-6 Luna/Sol、
// Grok 4.7 全没跟上），所以这次不手挑：整表照搬。升 CLI 时重跑
//
//	python backend/scripts/refresh_models.py
//
// 把它打出来的块换到下面即可。这是数据不是协议形状，不影响 wire 兼容性。
var hardcodedModels = []Model{
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6"},
	{ID: "claude-fable-5-1", Name: "Claude Fable 5.1"},
	{ID: "claude-fable-5", Name: "Claude Fable 5"},
	{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"},
	{ID: "claude-opus-5", Name: "Claude Opus 5"},
	{ID: "claude-opus-4-8", Name: "Claude Opus 4.8"},
	{ID: "claude-opus-4-7", Name: "Claude Opus 4.7"},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5"},
	{ID: "gpt-6-astra", Name: "GPT-6 Astra"},
	{ID: "gpt-6-sol", Name: "GPT-6 Sol"},
	{ID: "gpt-6-luna", Name: "GPT-6 Luna"},
	{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol"},
	{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra"},
	{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna"},
	{ID: "gpt-5.5", Name: "GPT-5.5"},
	{ID: "gpt-5.4", Name: "GPT-5.4"},
	{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex"},
	{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini"},
	{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro (latest)"},
	{ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash (latest)"},
	{ID: "deepseek/deepseek-v4-flash-vision-exp", Name: "DeepSeek V4 Flash Vision (exp)"},
	{ID: "deepseek/deepseek-v4-flash-fast", Name: "DeepSeek V4 Flash Fast"},
	{ID: "deepseek/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"},
	{ID: "moonshotai/Kimi-K3", Name: "Kimi K3"},
	{ID: "moonshotai/Kimi-K2.7-Code", Name: "Kimi K2.7 Code"},
	{ID: "moonshotai/Kimi-K2.7-Code-Highspeed", Name: "Kimi K2.7 Code HighSpeed"},
	{ID: "moonshotai/Kimi-K2.6", Name: "Kimi K2.6"},
	{ID: "moonshotai/Kimi-K2.5", Name: "Kimi K2.5"},
	{ID: "z-ai/glm-5.3-flash", Name: "GLM-5.3 Flash"},
	{ID: "z-ai/glm-5.3-flashx", Name: "GLM-5.3 FlashX"},
	{ID: "zai-org/GLM-5.3", Name: "GLM-5.3"},
	{ID: "zai-org/GLM-5.2", Name: "GLM-5.2"},
	{ID: "zai-org/GLM-5.2-Fast", Name: "GLM-5.2 Fast"},
	{ID: "zai-org/GLM-5.1", Name: "GLM-5.1"},
	{ID: "zai-org/GLM-5", Name: "GLM-5"},
	{ID: "MiniMaxAI/MiniMax-M3", Name: "MiniMax M3"},
	{ID: "MiniMaxAI/MiniMax-M2.7", Name: "MiniMax M2.7"},
	{ID: "MiniMaxAI/MiniMax-M2.5", Name: "MiniMax M2.5"},
	{ID: "xiaomi/mimo-v2.6-pro", Name: "MiMo V2.6 Pro"},
	{ID: "xiaomi/mimo-v2.6-pro-ultraspeed", Name: "MiMo V2.6 Pro UltraSpeed"},
	{ID: "xiaomi/mimo-v2.6-flash", Name: "MiMo V2.6 Flash"},
	{ID: "xiaomi/mimo-v2.5-pro", Name: "MiMo V2.5 Pro"},
	{ID: "xiaomi/mimo-v2.5", Name: "MiMo V2.5"},
	{ID: "Qwen/Qwen3.8-Omni-Flash", Name: "Qwen 3.8 Omni Flash"},
	{ID: "Qwen/Qwen3.8-Max-0902", Name: "Qwen 3.8 Max 0902"},
	{ID: "Qwen/Qwen3.8-Max", Name: "Qwen 3.8 Max"},
	{ID: "Qwen/Qwen3.8-27B", Name: "Qwen 3.8 27B"},
	{ID: "Qwen/Qwen3.8-Flash", Name: "Qwen 3.8 Flash"},
	{ID: "Qwen/Qwen3.7-Max", Name: "Qwen 3.7 Max"},
	{ID: "Qwen/Qwen3.7-Plus", Name: "Qwen 3.7 Plus"},
	{ID: "Qwen/Qwen3.7-Flash", Name: "Qwen 3.7 Flash"},
	{ID: "Qwen/Qwen3.6-Max-Preview", Name: "Qwen 3.6 Max Preview"},
	{ID: "Qwen/Qwen3.6-Plus", Name: "Qwen 3.6 Plus"},
	{ID: "meituan/LongCat-2.0", Name: "LongCat 2.0"},
	{ID: "stepfun/Step-5-Preview", Name: "Step 5 Preview"},
	{ID: "stepfun/Step-3.7-Flash", Name: "Step 3.7 Flash"},
	{ID: "stepfun/Step-3.5-Flash", Name: "Step 3.5 Flash"},
	{ID: "tencent/hy3-paid", Name: "Tencent Hy3"},
	{ID: "tencent/hy4-preview", Name: "Tencent Hy4 Preview"},
	{ID: "google/gemini-3.8-flash", Name: "Gemini 3.8 Flash"},
	{ID: "google/gemini-3.7-flash", Name: "Gemini 3.7 Flash"},
	{ID: "google/gemini-3.6-flash", Name: "Gemini 3.6 Flash"},
	{ID: "google/gemini-3.5-flash", Name: "Gemini 3.5 Flash"},
	{ID: "google/gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite"},
	{ID: "google/gemini-3.1-flash-lite", Name: "Gemini 3.1 Flash Lite"},
	{ID: "sakana/fugu-ultra", Name: "Fugu Ultra"},
	{ID: "nvidia/nemotron-3-ultra-550b-a55b", Name: "Nemotron 3 Ultra"},
	{ID: "thinkingmachines/inkling", Name: "Inkling"},
	{ID: "thinkingmachines/inkling-small", Name: "Inkling Small"},
	{ID: "stealth/space-bunny-alpha", Name: "Space Bunny Alpha"},
	{ID: "stealth/pixel-canary", Name: "Pixel Canary"},
	{ID: "poolside/laguna-s-2.1-free", Name: "Laguna S 2.1"},
	{ID: "inclusionai/ling-3.0-flash-sante:free", Name: "Ling 3.0 Flash Sante"},
	{ID: "meta/muse-spark-1.1", Name: "Muse Spark 1.1"},
	{ID: "meta/muse-spark-1.2", Name: "Muse Spark 1.2"},
	{ID: "meta/muse-spark-1.2-contributor", Name: "Muse Spark 1.2 Contributor"},
	{ID: "meta/muse-spark-1.3", Name: "Muse Spark 1.3"},
	{ID: "meta/muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor"},
	{ID: "xai/grok-4.5", Name: "Grok 4.5"},
	{ID: "xai/grok-4.6", Name: "Grok 4.6"},
	{ID: "xai/grok-4.7", Name: "Grok 4.7"},
}

// Model 是对外暴露的模型条目。
type Model struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Vendor            string   `json:"vendor,omitempty"`
	Description       string   `json:"description,omitempty"`
	Credits           string   `json:"credits,omitempty"`
	CreditMultiplier  *float64 `json:"credit_multiplier,omitempty"`
	ContextWindow     int      `json:"context_window,omitempty"`
	MaxOutputTokens   int      `json:"max_output_tokens,omitempty"`
	SupportsImages    bool     `json:"supports_images,omitempty"`
	SupportsReasoning bool     `json:"supports_reasoning,omitempty"`
	SupportsToolCall  bool     `json:"supports_tool_call,omitempty"`
	Tags              []string `json:"tags,omitempty"`
}

// keyState 是每个上游账号各自的会话/指纹状态。
//
// 状态按 apiKey 分组而不是按账号 ID：换绑账号时如果复用了同一把 key，
// 上游看到的仍应是同一台设备。
type keyState struct {
	fingerprint      *Fingerprint
	nextInitAt       time.Time
	sessionID        string
	sessionExpiresAt time.Time
}

// Client 是上游客户端，同时持有 WorkBuddy 5.9.3、AntiGravity 及兼容层状态。
type Client struct {
	cfg      config.CommandCodeConfig
	http     *http.Client
	wbClient *workbuddy.Client
	agClient *antigravity.Client
	logger   *slog.Logger
	device   DeviceProfile

	mu    sync.Mutex
	state map[string]*keyState

	modelsMu     sync.Mutex
	cachedModels []Model
	modelsAt     time.Time
}

// NewClient 构造上游客户端。
func NewClient(cfg config.CommandCodeConfig, logger *slog.Logger) *Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	}

	// 走代理访问上游。刻意用显式配置而不是 http.ProxyFromEnvironment：
	// 后者会让服务悄悄继承宿主的 HTTP_PROXY，行为随部署环境漂移，
	// 出问题时很难看出是代理在起作用。
	if cfg.UpstreamProxy != "" {
		if proxyURL, err := url.Parse(cfg.UpstreamProxy); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
			logger.Info("上游请求将走代理", "proxy", redactProxyURL(cfg.UpstreamProxy))
		} else {
			// config 层已经校验过，走到这里说明校验和构造用的不是同一份值。
			logger.Error("上游代理地址无法解析，将直连", "err", err)
		}
	}

	wb := workbuddy.NewClient(cfg.UpstreamProxy, cfg.BaseURL, logger)
	return &Client{
		cfg:      cfg,
		logger:   logger,
		wbClient: wb,
		agClient: antigravity.NewClientWithPool(wb.ProxyPool(), logger),
		device:   DeviceProfileFor(""),
		state:    make(map[string]*keyState),
		http: &http.Client{
			// 不设 Client.Timeout：流式响应可以持续很久，整体超时会把
			// 正常的长回答掐断。空闲超时由看门狗单独管。
			Transport: transport,
		},
	}
}

// WorkBuddy 返回底层 WorkBuddy 协议客户端（按账号独立绑定代理与设备指纹）。
func (c *Client) WorkBuddy() *workbuddy.Client {
	if c == nil {
		return workbuddy.DefaultClient()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wbClient == nil {
		c.wbClient = workbuddy.NewClient(c.cfg.UpstreamProxy, c.cfg.BaseURL, c.logger)
	}
	return c.wbClient
}

// SetWorkBuddyClient 注入共享的 WorkBuddy 客户端实例。
func (c *Client) SetWorkBuddyClient(wb *workbuddy.Client) {
	if c != nil && wb != nil {
		c.mu.Lock()
		c.wbClient = wb
		if c.agClient == nil {
			c.agClient = antigravity.NewClientWithPool(wb.ProxyPool(), c.logger)
		}
		c.mu.Unlock()
	}
}

// Antigravity 返回共享同一 ProxyPool 的 AntiGravity 客户端实例。
func (c *Client) Antigravity() *antigravity.Client {
	if c == nil {
		return antigravity.DefaultClient()
	}
	wb := c.WorkBuddy()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.agClient == nil {
		c.agClient = antigravity.NewClientWithPool(wb.ProxyPool(), c.logger)
	}
	return c.agClient
}

// SetAntigravityClient 注入共享的 AntiGravity 客户端实例。
func (c *Client) SetAntigravityClient(ag *antigravity.Client) {
	if c != nil && ag != nil {
		c.mu.Lock()
		c.agClient = ag
		c.mu.Unlock()
	}
}

// SetDefaultProxy 热更新客户端全局默认兜底代理（不覆盖账号独立 proxy_url）。
func (c *Client) SetDefaultProxy(proxyURL string) error {
	if c == nil {
		return nil
	}
	if err := c.WorkBuddy().SetDefaultProxy(proxyURL); err != nil {
		return err
	}
	norm, err := workbuddy.NormalizeProxyURL(proxyURL)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cfg.UpstreamProxy = norm
	if tr, ok := c.http.Transport.(*http.Transport); ok && tr != nil {
		if norm == "" {
			tr.Proxy = nil
		} else if u, parseErr := url.Parse(norm); parseErr == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) httpClientForProxy(accountProxyURL string) (*http.Client, error) {
	trimmed := strings.TrimSpace(accountProxyURL)
	if trimmed == "" {
		return c.http, nil
	}
	return c.WorkBuddy().ProxyPool().Client(trimmed)
}

// redactProxyURL 去掉代理 URL 里的用户名口令再打日志。
func redactProxyURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(无法解析)"
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}

// keyStateFor 取（必要时创建）某个 key 的状态。
func (c *Client) keyStateFor(apiKey string) *keyState {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.state[apiKey]
	if !ok {
		st = &keyState{
			fingerprint: GenerateFingerprint(apiKey, c.cfg.FingerprintSalt, ""),
		}
		c.state[apiKey] = st
	}
	return st
}

// ForgetKey 在账号被删除时清掉它的状态，避免无界增长。
func (c *Client) ForgetKey(apiKey string) {
	c.mu.Lock()
	delete(c.state, apiKey)
	c.mu.Unlock()
}

// sessionFor 返回该 key 的会话 ID，过期则新建。
func (c *Client) sessionFor(apiKey string) string {
	st := c.keyStateFor(apiKey)
	c.mu.Lock()
	defer c.mu.Unlock()
	if st.sessionID != "" && time.Now().Before(st.sessionExpiresAt) {
		return st.sessionID
	}
	st.sessionID = newSessionID()
	st.sessionExpiresAt = time.Now().Add(sessionDuration + randomJitter(sessionJitter))
	return st.sessionID
}

// newSessionID 按 CLI 的格式造一个会话 ID。
//
// CLI 是 `sess_` + UUID 去掉横线后的前 16 位十六进制（generateSessionId）。
// 这里必须照着来：会话 ID 出现在每一个请求的 x-session-id 头上，形状不对
// 是一条一眼就能看出来的差异——比版本号旧明显得多。
func newSessionID() string {
	compact := strings.ReplaceAll(randomUUID(), "-", "")
	if len(compact) > 16 {
		compact = compact[:16]
	}
	return "sess_" + compact
}

// resolveSessionID 决定本次请求用哪个会话 ID。
//
// 优先采用客户端自带的会话标识：同一个 CLI 会话在多次请求间保持同一个
// session 是正常行为，由 cmd2api 擅自重新分配反而制造出「每次请求都是新会话」
// 的异常模式。只有客户端没给才按 key 兜底。
func (c *Client) resolveSessionID(apiKey string, hints ...string) string {
	for _, h := range hints {
		// 太短的标识不像会话 ID，忽略。
		if len(h) >= 8 {
			return h
		}
	}
	return c.sessionFor(apiKey)
}

// initWaitBudget 是预请求最多让生成请求等多久。
//
// 正常路径下这两个请求几十毫秒就结束，这个上限只是兜底：它们是辅助信号，
// 绝不能因为上游慢而把用户的生成请求一起拖住。
const initWaitBudget = 3 * time.Second

// initRetryBackoff 是预请求失败后的重试间隔。
//
// 不能像成功那样等 8 小时——失败了就该早点再试，否则这段时间里所有生成请求
// 都缺少「这个账号的 CLI 活着」这个信号。也不能每次请求都重试，那会在上游
// 持续不可用时形成请求风暴。
const initRetryBackoff = 60 * time.Second

// EnsureInitialized 按需发送两个预请求：上报设备指纹 + 上报一次生命周期事件。
func (c *Client) EnsureInitialized(ctx context.Context, apiKey string) {
	c.EnsureInitializedWithProxy(ctx, apiKey, "")
}

// EnsureInitializedWithProxy 按账号独立代理发送设备指纹与生命周期预请求。
func (c *Client) EnsureInitializedWithProxy(ctx context.Context, apiKey, accountProxyURL string) {
	st := c.keyStateFor(apiKey)
	now := time.Now()

	c.mu.Lock()
	if now.Before(st.nextInitAt) {
		c.mu.Unlock()
		return
	}
	st.nextInitAt = now.Add(initRefresh + randomJitter(initJitter))
	fp := st.fingerprint
	c.mu.Unlock()

	sessionID := c.sessionFor(apiKey)
	headers := c.baseHeaders(apiKey, sessionID)

	fpBody, err := json.Marshal(fp)
	if err != nil {
		c.logger.Warn("序列化设备指纹失败", "err", err)
		return
	}

	lifecycle := map[string]any{
		"eventType": "cli_session_exists",
		"metadata": map[string]any{
			"sessionId":  sessionID,
			"cliVersion": protocolVersion,
			"mode":       "interactive",
			"os":         fp.Components.Platform + "-" + fp.Components.Arch,
		},
	}
	lcBody, err := json.Marshal(lifecycle)
	if err != nil {
		c.logger.Warn("序列化生命周期事件失败", "err", err)
		return
	}

	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)

	result := make(chan bool, 1)
	go func() {
		defer cancel()
		var wg sync.WaitGroup
		okFp, okLc := true, true
		wg.Add(2)
		go func() {
			defer wg.Done()
			okFp = c.postWithProxy(postCtx, "/alpha/fingerprint/record", headers, fpBody, accountProxyURL, "上报设备指纹")
		}()
		go func() {
			defer wg.Done()
			okLc = c.postWithProxy(postCtx, "/alpha/lifecycle-events", headers, lcBody, accountProxyURL, "上报生命周期事件")
		}()
		wg.Wait()
		result <- okFp && okLc
	}()

	timer := time.NewTimer(initWaitBudget)
	defer timer.Stop()

	select {
	case ok := <-result:
		if !ok {
			c.mu.Lock()
			if st.nextInitAt.After(time.Now().Add(initRetryBackoff)) {
				st.nextInitAt = time.Now().Add(initRetryBackoff)
			}
			c.mu.Unlock()
			c.logger.Debug("预请求失败，稍后重试")
		}
	case <-timer.C:
		c.logger.Debug("预请求超时，不阻塞生成请求")
	case <-ctx.Done():
	}
}

// Generate 发起一次生成请求，返回未读的响应体供调用方按 NDJSON 解析。
func (c *Client) Generate(ctx context.Context, apiKey string, sessionHints []string, body []byte) (*http.Response, error) {
	return c.GenerateWithProxy(ctx, apiKey, sessionHints, body, "")
}

// GenerateWithProxy 按账号独立代理发起一次 Command Code 生成请求。
func (c *Client) GenerateWithProxy(ctx context.Context, apiKey string, sessionHints []string, body []byte, accountProxyURL string) (*http.Response, error) {
	sessionID := c.resolveSessionID(apiKey, sessionHints...)

	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "cli")
	h.Set("x-command-code-version", protocolVersion)
	h.Set("x-cli-environment", "production")
	h.Set("x-project-slug", slugifyProjectPath(c.device.ProjectDir))
	h.Set("x-taste-learning", "false")
	h.Set("x-session-id", sessionID)
	h.Set("Authorization", "Bearer "+apiKey)
	h.Set("traceparent", generateTraceparent())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/alpha/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造生成请求: %w", err)
	}
	req.Header = h

	httpCli, err := c.httpClientForProxy(accountProxyURL)
	if err != nil {
		return nil, fmt.Errorf("选择账号代理客户端: %w", err)
	}
	return httpCli.Do(req)
}

// Models 取模型列表：优先动态拉取，失败退回内置列表。
func (c *Client) Models(ctx context.Context, apiKey string) []Model {
	return c.ModelsWithProxy(ctx, apiKey, "")
}

// ModelsWithProxy 按账号独立代理动态拉取 Command Code 模型列表，失败退回内置列表。
func (c *Client) ModelsWithProxy(ctx context.Context, apiKey, accountProxyURL string) []Model {
	c.modelsMu.Lock()
	if len(c.cachedModels) > 0 && time.Since(c.modelsAt) < c.cfg.ModelCacheTTL {
		cached := c.cachedModels
		c.modelsMu.Unlock()
		return cached
	}
	c.modelsMu.Unlock()

	models := c.fetchModelsWithProxy(ctx, apiKey, accountProxyURL)

	c.modelsMu.Lock()
	c.cachedModels = models
	c.modelsAt = time.Now()
	c.modelsMu.Unlock()
	return models
}

// InvalidateModels 清掉模型缓存，供后台手动刷新。
func (c *Client) InvalidateModels() {
	c.modelsMu.Lock()
	c.cachedModels = nil
	c.modelsAt = time.Time{}
	c.modelsMu.Unlock()
}

func (c *Client) fetchModels(ctx context.Context, apiKey string) []Model {
	return c.fetchModelsWithProxy(ctx, apiKey, "")
}

func (c *Client) fetchModelsWithProxy(ctx context.Context, apiKey, accountProxyURL string) []Model {
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, c.cfg.BaseURL+"/provider/v1/models", nil)
	if err != nil {
		c.logger.Warn("构造模型列表请求失败", "err", err)
		return hardcodedModels
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-command-code-version", protocolVersion)
	req.Header.Set("x-session-id", c.sessionFor(apiKey))

	httpCli, err := c.httpClientForProxy(accountProxyURL)
	if err != nil {
		c.logger.Warn("选择模型列表请求代理失败，使用内置列表", "err", err)
		return hardcodedModels
	}

	resp, err := httpCli.Do(req)
	if err != nil {
		c.logger.Warn("拉取模型列表失败，使用内置列表", "err", err)
		return hardcodedModels
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logger.Warn("拉取模型列表返回非 200，使用内置列表", "status", resp.StatusCode)
		return hardcodedModels
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.logger.Warn("读取模型列表失败，使用内置列表", "err", err)
		return hardcodedModels
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Data) == 0 {
		c.logger.Warn("模型列表格式不符合预期，使用内置列表", "err", err)
		return hardcodedModels
	}

	out := make([]Model, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, Model{ID: m.ID, Name: m.ID})
	}
	if len(out) == 0 {
		return hardcodedModels
	}
	c.logger.Info("已从上游拉取模型列表", "count", len(out))
	return out
}

func (c *Client) baseHeaders(apiKey, sessionID string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "cli")
	h.Set("x-cli-environment", "production")
	h.Set("x-project-slug", slugifyProjectPath(c.device.ProjectDir))
	h.Set("x-taste-learning", "false")
	h.Set("x-session-id", sessionID)
	h.Set("Authorization", "Bearer "+apiKey)
	h.Set("x-command-code-version", protocolVersion)
	return h
}

func (c *Client) post(ctx context.Context, path string, headers http.Header, body []byte, what string) bool {
	return c.postWithProxy(ctx, path, headers, body, "", what)
}

func (c *Client) postWithProxy(ctx context.Context, path string, headers http.Header, body []byte, accountProxyURL, what string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		c.logger.Warn(what+"：构造请求失败", "err", err)
		return false
	}
	req.Header = headers

	httpCli, err := c.httpClientForProxy(accountProxyURL)
	if err != nil {
		c.logger.Debug(what+"：代理配置异常", "err", err)
		return false
	}

	resp, err := httpCli.Do(req)
	if err != nil {
		c.logger.Debug(what+"：请求失败，稍后会重试", "err", err)
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 300 {
		c.logger.Warn(what+"：上游返回非 2xx", "status", resp.StatusCode)
		return false
	}
	c.logger.Debug(what + "：成功")
	return true
}

// ---- 小工具 ----

// randomUUID 生成 v4 UUID，用于 threadId / sessionId。
func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败在正常系统上不该发生；退化成时间戳也比起 panic 好。
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // 版本 4
	b[8] = (b[8] & 0x3f) | 0x80 // 变体 RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// generateTraceparent 生成 W3C traceparent 头。格式：00-<32hex>-<16hex>-01。
func generateTraceparent() string {
	return "00-" + randomHex(16) + "-" + randomHex(8) + "-01"
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// randomJitter 返回 [0, d) 内的随机时长。
func randomJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}
