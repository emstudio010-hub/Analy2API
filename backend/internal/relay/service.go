package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/config"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/prompt"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"
)

// Protocol 标识入站请求说的是哪种协议。
type Protocol string

const (
	// ProtocolOpenAI 是 OpenAI Chat Completions（/v1/chat/completions）。
	ProtocolOpenAI Protocol = "openai"
	// ProtocolAnthropic 是 Anthropic Messages（/v1/messages）。
	ProtocolAnthropic Protocol = "anthropic"
	// ProtocolResponses 是 OpenAI Responses API（/v1/responses，如 Codex CLI）。
	ProtocolResponses Protocol = "responses"

	defaultFirstTokenTimeout    = 300 * time.Second
	defaultStreamIdleTimeout    = 180 * time.Second
	defaultNonStreamIdleTimeout = 600 * time.Second
)

// UsageRecord 是一次请求的用量记录。
type UsageRecord struct {
	UserID    int64
	APIKeyID  int64
	AccountID int64
	GroupID   *int64

	Platform      string
	RequestID     string
	Model         string
	UpstreamModel string

	// InputTokens 为不含缓存命中的纯输入 Token（InputTokens = max(0, PromptTokens - CachedTokens)）。
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	ReasoningTokens     int

	TotalCost      float64
	RateMultiplier float64

	Stream       bool
	DurationMs   int
	FirstTokenMs *int
	StatusCode   int
	ErrorMessage string
	UserAgent    string
	IPAddress    string
}

// UsageRecorder 落库一次用量。
type UsageRecorder interface {
	Record(ctx context.Context, rec UsageRecord)
}

// RelayRequest 是一次中转请求的输入。
type RelayRequest struct {
	Protocol Protocol
	// Body 是原始请求体（已读全）。
	Body []byte

	APIKeyID int64
	UserID   int64
	GroupID  int64

	RequestID string
	UserAgent string
	ClientIP  string
	// SessionHints 是请求头里带的会话标识，按优先级排列。
	SessionHints []string

	// RateMultiplier 是分组倍率，用于结算。
	RateMultiplier float64

	// 内部运行态上下文（由 Service.Relay 填充，供非额度 429 会话暂停与请求体 Log 落盘使用）。
	rawInboundBody    []byte
	resolvedSessionID string
	lastOutboundBody  []byte
}

// TokenUpdater 用于在上游返回 401 并成功通过 RefreshToken 续期后，把最新凭证持久化回数据库。
type TokenUpdater interface {
	UpdateTokens(ctx context.Context, accountID int64, accessToken, refreshToken string) error
}

// ServiceOption 配置 relay.Service 的可选依赖。
type ServiceOption func(*Service)

// WithFilterEngine 注入全链路内容风控引擎。
func WithFilterEngine(engine *filter.Engine) ServiceOption {
	return func(s *Service) {
		if engine != nil {
			s.filterEngine = engine
		}
	}
}

// WithPromptEngine 注入多级前置系统提示词合成引擎。
func WithPromptEngine(pe *prompt.Engine) ServiceOption {
	return func(s *Service) {
		if pe != nil {
			s.promptEngine = pe
		}
	}
}

// WithToolObfuscator 注入双向确定性工具名混淆引擎。
func WithToolObfuscator(to *toolobf.Engine) ServiceOption {
	return func(s *Service) {
		if to != nil {
			s.toolObfuscator = to
		}
	}
}

// WithWorkBuddyClient 注入 WorkBuddy 上游客户端（含独立代理池与设备指纹引擎）。
func WithWorkBuddyClient(wb *workbuddy.Client) ServiceOption {
	return func(s *Service) {
		if wb != nil {
			s.wbClient = wb
		}
	}
}

// WithAntigravityClient 注入 AntiGravity 容器/远程节点协议客户端。
func WithAntigravityClient(ag *antigravity.Client) ServiceOption {
	return func(s *Service) {
		if ag != nil {
			s.agClient = ag
		}
	}
}

// WithTokenUpdater 注入 Token 自动续期持久化回调。
func WithTokenUpdater(updater TokenUpdater) ServiceOption {
	return func(s *Service) {
		s.tokenUpdater = updater
	}
}

// WithSessionGuard 注入「非账号额度不足 429」会话暂停保护与请求体审计日志服务。
func WithSessionGuard(sg *SessionGuard) ServiceOption {
	return func(s *Service) {
		if sg != nil {
			s.sessionGuard = sg
		}
	}
}

// Service 编排一次中转请求的完整流程。
type Service struct {
	mu           sync.RWMutex
	client       *Client
	sched        *scheduler.Scheduler
	recorder     UsageRecorder
	cfg          *config.Config
	logger       *slog.Logger
	filterEngine   *filter.Engine
	promptEngine   *prompt.Engine
	toolObfuscator *toolobf.Engine
	sessionGuard   *SessionGuard
	wbClient       *workbuddy.Client
	agClient       *antigravity.Client
	tokenUpdater   TokenUpdater
	wbRefreshLocks sync.Map // map[int64]*sync.Mutex
	wbRefreshCache sync.Map // map[int64]wbCachedRefresh

	firstTokenOverride    time.Duration
	streamIdleOverride    time.Duration
	nonStreamIdleOverride time.Duration
	emptySysOverride      *bool
}

// NewService 构造 Service。
func NewService(client *Client, sched *scheduler.Scheduler, recorder UsageRecorder, cfg *config.Config, logger *slog.Logger, opts ...ServiceOption) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		client:   client,
		sched:    sched,
		recorder: recorder,
		cfg:      cfg,
		logger:   logger,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.wbClient == nil && client != nil {
		s.wbClient = client.WorkBuddy()
	}
	if s.wbClient == nil {
		s.wbClient = workbuddy.DefaultClient()
	}
	if s.agClient == nil && client != nil && client.agClient != nil {
		s.agClient = client.Antigravity()
	}
	if s.agClient == nil {
		s.agClient = antigravity.NewClientWithPool(s.wbClient.ProxyPool(), s.logger)
	}
	if s.filterEngine == nil {
		s.filterEngine = filter.NewDefaultEngine()
	}
	return s
}

// SetTokenUpdater 设置 Token 自动续期持久化回调（支持初始化后装配）。
func (s *Service) SetTokenUpdater(updater TokenUpdater) {
	s.mu.Lock()
	s.tokenUpdater = updater
	s.mu.Unlock()
}

// FilterEngine 返回当前服务绑定的内容风控引擎。
func (s *Service) FilterEngine() *filter.Engine {
	return s.filterEngine
}

// SetPromptEngine 动态注入或替换前置系统提示词引擎。
func (s *Service) SetPromptEngine(pe *prompt.Engine) {
	s.mu.Lock()
	s.promptEngine = pe
	s.mu.Unlock()
}

// PromptEngine 返回当前绑定的前置系统提示词引擎（未绑定时为 nil）。
func (s *Service) PromptEngine() *prompt.Engine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptEngine
}

// SetToolObfuscator 动态注入或替换工具名混淆引擎。
func (s *Service) SetToolObfuscator(to *toolobf.Engine) {
	s.mu.Lock()
	s.toolObfuscator = to
	s.mu.Unlock()
}

// ToolObfuscator 返回当前绑定的工具名混淆引擎（未绑定时为 nil）。
func (s *Service) ToolObfuscator() *toolobf.Engine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.toolObfuscator
}

// SetSessionGuard 动态注入或替换非额度 429 会话暂停保护器。
func (s *Service) SetSessionGuard(sg *SessionGuard) {
	s.mu.Lock()
	s.sessionGuard = sg
	s.mu.Unlock()
}

// SessionGuard 返回当前绑定的会话暂停保护器（未绑定时为 nil）。
func (s *Service) SessionGuard() *SessionGuard {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionGuard
}

// SetAntigravityClient 动态注入 AntiGravity 客户端。
func (s *Service) SetAntigravityClient(ag *antigravity.Client) {
	if ag == nil {
		return
	}
	s.mu.Lock()
	s.agClient = ag
	s.mu.Unlock()
}

// AntigravityClient 返回当前绑定的 AntiGravity 客户端。
func (s *Service) AntigravityClient() *antigravity.Client {
	return s.getAGClient()
}

// WorkBuddyClient 返回当前绑定的 WorkBuddy 客户端。
func (s *Service) WorkBuddyClient() *workbuddy.Client {
	return s.getWBClient()
}

// UpdateDefaultProxy 同步更新全局默认代理地址到所有共享上游客户端（热生效）。
func (s *Service) UpdateDefaultProxy(proxyURL string) error {
	if s == nil {
		return nil
	}
	wb := s.getWBClient()
	if wb != nil {
		if err := wb.SetDefaultProxy(proxyURL); err != nil {
			return err
		}
	}
	if s.client != nil {
		_ = s.client.SetDefaultProxy(proxyURL)
	}
	if ag := s.getAGClient(); ag != nil {
		_ = ag.SetDefaultProxy(proxyURL)
	}
	return nil
}

// UpdateTimeouts 在线热更新三级超时参数（>0 时覆盖静态配置）。
func (s *Service) UpdateTimeouts(firstToken, streamIdle, nonStreamIdle time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if firstToken > 0 {
		s.firstTokenOverride = firstToken
		if s.cfg != nil {
			s.cfg.CommandCC.FirstTokenTimeout = firstToken
		}
	}
	if streamIdle > 0 {
		s.streamIdleOverride = streamIdle
		if s.cfg != nil {
			s.cfg.CommandCC.StreamIdleTimeout = streamIdle
		}
	}
	if nonStreamIdle > 0 {
		s.nonStreamIdleOverride = nonStreamIdle
		if s.cfg != nil {
			s.cfg.CommandCC.NonStreamIdleTimeout = nonStreamIdle
		}
	}
}

// Timeouts 返回当前生效的三级超时配置：(首字等待超时, 流式分片空闲超时, 非流式总空闲超时)。
func (s *Service) Timeouts() (firstToken, streamIdle, nonStreamIdle time.Duration) {
	s.mu.RLock()
	ft := s.firstTokenOverride
	si := s.streamIdleOverride
	nsi := s.nonStreamIdleOverride
	cfg := s.cfg
	s.mu.RUnlock()

	if ft <= 0 && cfg != nil && cfg.CommandCC.FirstTokenTimeout > 0 {
		ft = cfg.CommandCC.FirstTokenTimeout
	}
	if ft <= 0 {
		ft = defaultFirstTokenTimeout
	}

	if si <= 0 && cfg != nil && cfg.CommandCC.StreamIdleTimeout > 0 {
		si = cfg.CommandCC.StreamIdleTimeout
	}
	if si <= 0 {
		si = defaultStreamIdleTimeout
	}

	if nsi <= 0 && cfg != nil && cfg.CommandCC.NonStreamIdleTimeout > 0 {
		nsi = cfg.CommandCC.NonStreamIdleTimeout
	}
	if nsi <= 0 {
		nsi = defaultNonStreamIdleTimeout
	}
	return ft, si, nsi
}

// SetEmptySystemPlaceholder 在线热更新空 System 占位开关。
func (s *Service) SetEmptySystemPlaceholder(enabled bool) {
	s.mu.Lock()
	b := enabled
	s.emptySysOverride = &b
	if s.cfg != nil {
		s.cfg.CommandCC.EmptySystemPlaceholder = enabled
	}
	s.mu.Unlock()
}

func (s *Service) emptySystemPlaceholder() bool {
	s.mu.RLock()
	ov := s.emptySysOverride
	cfg := s.cfg
	s.mu.RUnlock()
	if ov != nil {
		return *ov
	}
	if cfg != nil {
		return cfg.CommandCC.EmptySystemPlaceholder
	}
	return false
}

// resolveTimeouts 根据下游请求是否为流式，计算首字前看门狗超时与出字后分片空闲超时。
func (s *Service) resolveTimeouts(streaming bool) (firstTokenTimeout, idleTimeout time.Duration) {
	ft, si, nsi := s.Timeouts()
	if streaming {
		return ft, si
	}
	if ft > nsi {
		return ft, nsi
	}
	return nsi, nsi
}

// Models 返回可用于该分组的模型列表。
func (s *Service) Models(ctx context.Context, groupID int64) ([]Model, error) {
	target, err := s.sched.AnyTarget(ctx, groupID)
	if err != nil {
		if errors.Is(err, scheduler.ErrNoAccountAvailable) {
			if grpPlat, gErr := s.sched.GroupPlatform(ctx, groupID); gErr == nil {
				switch grpPlat {
				case domain.PlatformAntigravity:
					return convertAntigravityModels(antigravity.DefaultModels()), nil
				case domain.PlatformCommandCode:
					return cloneModelsSlice(hardcodedModels), nil
				case domain.PlatformOpenCode:
					return defaultOpenCodeModels(), nil
				}
			}
			return convertWorkBuddyModels(workbuddy.DefaultModels()), nil
		}
		return nil, err
	}
	switch domain.NormalizePlatform(target.Platform) {
	case domain.PlatformAntigravity:
		return s.agModels(ctx, target.AntigravityNodeSpec())
	case domain.PlatformOpenCode:
		items := s.client.openCodeModelsWithProxy(ctx, target.BaseURL, target.APIKey, target.ProxyURL)
		if len(items) == 0 {
			return defaultOpenCodeModels(), nil
		}
		return items, nil
	case domain.PlatformCommandCode:
		return s.client.ModelsWithProxy(ctx, target.APIKey, target.ProxyURL), nil
	default:
		return s.wbModels(ctx, target.WorkBuddyCreds())
	}
}

const maxAccountAttempts = 3

// Relay 处理一次中转请求。
func (s *Service) Relay(ctx context.Context, w http.ResponseWriter, req *RelayRequest) {
	started := time.Now()
	if req == nil {
		s.writeClientError(w, ProtocolOpenAI, http.StatusBadRequest, "invalid_request_error", "请求不能为空", 0)
		return
	}
	reqCopy := *req
	if len(req.Body) > 0 {
		reqCopy.rawInboundBody = append([]byte(nil), req.Body...)
	}
	req = &reqCopy

	// 1. 全客户端入站风控清洗
	if s.filterEngine != nil && len(req.Body) > 0 {
		if sanitized, _, fErr := s.filterEngine.SanitizePayload(string(req.Protocol), req.Body); fErr == nil && len(sanitized) > 0 {
			req.Body = sanitized
		}
	}

	// 2. 协议解析归一化
	normalized, err := s.parse(req)
	if err != nil {
		s.writeClientError(w, req.Protocol, http.StatusBadRequest, "invalid_request_error", err.Error(), 0)
		return
	}

	// 2.5 会话标识解析与「非账号额度不足 429」暂停调度拦截
	if sg := s.SessionGuard(); sg != nil {
		sgCfg := sg.Config()
		if sgCfg.Enabled {
			req.resolvedSessionID = ExtractSessionID(req, normalized, sgCfg.FallbackFingerprint)
			if paused, isPaused := sg.CheckPaused(req.resolvedSessionID); isPaused {
				msg := fmt.Sprintf("会话 %s 因非账号额度不足 429 已暂停调度（剩余 %d 秒）；请求体诊断日志: %s",
					paused.SessionID, paused.RemainingSeconds, nonEmptyStr(paused.LogFile, "未落盘"))
				s.recordForPlatform(req, 0, "", started, nil, normalized.Stream, 0, 0, 0, 0, req.RateMultiplier, http.StatusTooManyRequests, msg)
				s.writeClientError(w, req.Protocol, http.StatusTooManyRequests, "rate_limit_error", msg, paused.RemainingSeconds)
				return
			}
		}
	}

	// 3. 多级前置系统提示词合成注入（若未装配 promptEngine，回退旧全局提示词配置保持 100% 兼容）
	if pe := s.PromptEngine(); pe != nil {
		updatedMsgs := pe.ApplyToMessages(req.GroupID, normalized.Messages, prompt.Vars{
			Model: normalized.Model,
			Date:  time.Now().UTC().Format("2006-01-02"),
		})
		syncNormalizedMessages(normalized, updatedMsgs)
	} else if s.cfg != nil && s.cfg.CommandCC.GlobalSystemPromptEnabled {
		injectGlobalSystemPrompt(normalized, s.cfg.CommandCC.GlobalSystemPrompt)
	}

	// 客户端自带的会话标识优先于自动分配。
	if len(req.SessionHints) > 0 {
		normalized.SessionHints = append(req.SessionHints, normalized.SessionHints...)
	}

	var (
		firstErr *MappedError
		tried    []int64
	)

	for attempt := 0; attempt < maxAccountAttempts; attempt++ {
		lease, err := s.sched.Acquire(ctx, req.GroupID, tried...)
		if err != nil {
			if errors.Is(err, scheduler.ErrNoAccountAvailable) {
				if firstErr != nil {
					break
				}
				s.writeClientError(w, req.Protocol, http.StatusServiceUnavailable,
					"api_error", "分组内没有可用账号，请检查账号状态或稍后重试", 15)
				return
			}
			s.logger.Error("调度账号失败", "err", err, "group_id", req.GroupID)
			s.writeClientError(w, req.Protocol, http.StatusInternalServerError,
				"api_error", "调度上游账号失败", 0)
			return
		}

		tried = append(tried, lease.Account.ID)
		done, mappedErr := s.attempt(ctx, w, req, normalized, lease, started)
		lease.Release()

		if done {
			return
		}

		if firstErr == nil {
			firstErr = mappedErr
		}
		if mappedErr != nil {
			s.logger.Warn("上游账号失败，换下一个账号重试",
				"request_id", req.RequestID, "account_id", lease.Account.ID,
				"account_name", lease.Account.Name, "platform", lease.Platform,
				"attempt", attempt+1, "status", mappedErr.Status,
				"code", mappedErr.Code, "message", mappedErr.Message)

			if !mappedErr.Retryable() {
				break
			}
		}
	}

	if firstErr == nil {
		firstErr = &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error",
			Message: "上游请求失败", ReportedStatus: http.StatusBadGateway,
		}
	}
	s.writeClientError(w, req.Protocol, firstErr.Status, firstErr.Type, firstErr.Message, firstErr.RetryAfter)
}

// attempt 按账号所属平台分发到对应的协议处理。
func (s *Service) attempt(
	ctx context.Context,
	w http.ResponseWriter,
	req *RelayRequest,
	normalized *normalizedRequest,
	lease *scheduler.Lease,
	started time.Time,
) (done bool, mappedErr *MappedError) {
	effNorm, toolSess := obfuscateNormalizedRequest(s.ToolObfuscator(), req.GroupID, lease.Platform, normalized)
	switch domain.NormalizePlatform(lease.Platform) {
	case domain.PlatformAntigravity:
		return s.attemptAntigravity(ctx, w, req, effNorm, lease, started, toolSess)
	case domain.PlatformOpenCode:
		return s.attemptOpenCode(ctx, w, req, effNorm, lease, started, toolSess)
	case domain.PlatformCommandCode:
		return s.attemptCommandCode(ctx, w, req, effNorm, lease, started, toolSess)
	default:
		return s.attemptWorkBuddy(ctx, w, req, effNorm, lease, started, toolSess)
	}
}

// attemptOpenCode 走 OpenAI 兼容上游（支持账号独立 ProxyURL、三级超时与三协议输出）。
func (s *Service) attemptOpenCode(
	ctx context.Context,
	w http.ResponseWriter,
	req *RelayRequest,
	normalized *normalizedRequest,
	lease *scheduler.Lease,
	started time.Time,
	toolSess *toolobf.Session,
) (done bool, mappedErr *MappedError) {
	baseURL := lease.BaseURL
	if baseURL == "" {
		baseURL = domain.DefaultOpenCodeBaseURL(lease.AccountMode)
	}

	body, err := normalized.toOpenAIBody()
	if err != nil {
		s.logger.Error("构造 OpenCode 请求失败", "err", err, "request_id", req.RequestID)
		s.writeClientError(w, req.Protocol, http.StatusInternalServerError, "api_error", "构造上游请求失败", 0)
		return true, nil
	}
	req.lastOutboundBody = body

	resp, err := s.client.openCodeGenerateWithProxy(ctx, baseURL, lease.Credentials.APIKey, lease.ProxyURL, body)
	if err != nil {
		if ctx.Err() != nil {
			return true, nil
		}
		wrapped := fmt.Errorf("请求 OpenCode 上游失败: %w", err)
		s.logger.Warn("请求 OpenCode 失败",
			"err", err, "request_id", req.RequestID, "account_id", lease.Account.ID)
		s.sched.MarkFailure(ctx, lease.Account.ID, wrapped)
		s.recordForPlatform(req, lease.Account.ID, domain.PlatformOpenCode, started, nil, normalized.Stream,
			0, 0, 0, 0, req.RateMultiplier, http.StatusBadGateway, err.Error())
		return false, &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error",
			Message: "无法连接 OpenCode 上游", ReportedStatus: http.StatusBadGateway,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		mapped := mapOpenCodeError(resp.StatusCode, raw)
		s.applyAccountPenalty(ctx, lease.Account.ID, mapped)
		abortRetry, pauseMsg := s.maybePauseSessionOn429(req, lease.Account.ID, domain.PlatformOpenCode, normalized.Model, mapped, toolSess)
		errMsg := mapped.Message
		if pauseMsg != "" {
			errMsg = pauseMsg
		}
		s.recordForPlatform(req, lease.Account.ID, domain.PlatformOpenCode, started, nil, normalized.Stream,
			0, 0, 0, 0, req.RateMultiplier, mapped.Status, errMsg)
		if abortRetry {
			s.writeClientError(w, req.Protocol, http.StatusTooManyRequests, "rate_limit_error", pauseMsg, mapped.RetryAfter)
			return true, mapped
		}
		if mapped.Retryable() {
			return false, mapped
		}
		s.writeClientError(w, req.Protocol, mapped.Status, mapped.Type, mapped.Message, mapped.RetryAfter)
		return true, mapped
	}

	firstTokenTimeout, idleTimeout := s.resolveTimeouts(normalized.Stream)
	adapter := newOpenAIStreamAdapter()
	var sawVisible bool
	read := func(handle func(*CCEvent)) error {
		wrappedHandle := func(ev *CCEvent) {
			if hasVisibleContent(ev) {
				sawVisible = true
			}
			handle(ev)
		}
		err := s.client.readOpenAISSEWithTimeouts(ctx, resp.Body, firstTokenTimeout, idleTimeout, func() bool {
			return sawVisible
		}, adapter.emitTo(wrappedHandle))

		if adapter.SawCompletion() {
			for _, ev := range adapter.Flush() {
				wrappedHandle(ev)
			}
		}
		return err
	}

	return s.streamEvents(ctx, w, req, normalized, lease.Account.ID, domain.PlatformOpenCode, started, toolSess, read)
}

// attemptCommandCode 走 Command Code 自有协议（支持账号独立 ProxyURL、三级超时与三协议输出）。
func (s *Service) attemptCommandCode(
	ctx context.Context,
	w http.ResponseWriter,
	req *RelayRequest,
	normalized *normalizedRequest,
	lease *scheduler.Lease,
	started time.Time,
	toolSess *toolobf.Session,
) (done bool, mappedErr *MappedError) {
	apiKey := lease.Credentials.APIKey
	device := DeviceProfileFor("")

	threadID := ""
	if len(normalized.SessionHints) > 0 {
		threadID = normalized.SessionHints[0]
	} else {
		threadID = s.client.sessionFor(apiKey)
	}

	envelope, err := buildEnvelope(normalized, "agent", device, threadID, s.emptySystemPlaceholder())
	if err != nil {
		s.logger.Error("构造上游请求失败", "err", err, "request_id", req.RequestID)
		s.recordForPlatform(req, lease.Account.ID, domain.PlatformCommandCode, started, nil, normalized.Stream,
			0, 0, 0, 0, req.RateMultiplier, http.StatusInternalServerError, err.Error())
		s.writeClientError(w, req.Protocol, http.StatusInternalServerError, "api_error", "构造上游请求失败", 0)
		return true, nil
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		s.logger.Error("序列化上游请求失败", "err", err, "request_id", req.RequestID)
		s.writeClientError(w, req.Protocol, http.StatusInternalServerError, "api_error", "序列化上游请求失败", 0)
		return true, nil
	}
	req.lastOutboundBody = payload

	s.client.EnsureInitializedWithProxy(ctx, apiKey, lease.ProxyURL)

	resp, err := s.client.GenerateWithProxy(ctx, apiKey, normalized.SessionHints, payload, lease.ProxyURL)
	if err != nil {
		if ctx.Err() != nil {
			return true, nil
		}
		wrapped := fmt.Errorf("请求上游失败: %w", err)
		s.logger.Warn("请求上游失败", "err", err, "request_id", req.RequestID, "account_id", lease.Account.ID)
		s.sched.MarkFailure(ctx, lease.Account.ID, wrapped)
		s.recordForPlatform(req, lease.Account.ID, domain.PlatformCommandCode, started, nil, normalized.Stream,
			0, 0, 0, 0, req.RateMultiplier, http.StatusBadGateway, err.Error())
		return false, &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error",
			Message: "无法连接上游服务", ReportedStatus: http.StatusBadGateway,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		mapped := MapHTTPError(resp.StatusCode, body)

		s.applyAccountPenalty(ctx, lease.Account.ID, mapped)
		abortRetry, pauseMsg := s.maybePauseSessionOn429(req, lease.Account.ID, domain.PlatformCommandCode, normalized.Model, mapped, toolSess)
		errMsg := mapped.Message
		if pauseMsg != "" {
			errMsg = pauseMsg
		}
		s.recordForPlatform(req, lease.Account.ID, domain.PlatformCommandCode, started, nil, normalized.Stream,
			0, 0, 0, 0, req.RateMultiplier, mapped.Status, errMsg)

		if abortRetry {
			s.writeClientError(w, req.Protocol, http.StatusTooManyRequests, "rate_limit_error", pauseMsg, mapped.RetryAfter)
			return true, mapped
		}
		if mapped.Retryable() {
			return false, mapped
		}
		s.writeClientError(w, req.Protocol, mapped.Status, mapped.Type, mapped.Message, mapped.RetryAfter)
		return true, mapped
	}

	firstTokenTimeout, idleTimeout := s.resolveTimeouts(normalized.Stream)
	read := func(handle func(*CCEvent)) error {
		return s.readNDJSONWithTimeouts(ctx, resp.Body, firstTokenTimeout, idleTimeout, handle)
	}
	return s.streamEvents(ctx, w, req, normalized, lease.Account.ID, domain.PlatformCommandCode, started, toolSess, read)
}

// applyAccountPenalty 按错误类型给账号记账。
func (s *Service) applyAccountPenalty(ctx context.Context, accountID int64, mapped *MappedError) {
	switch {
	case mapped.IsRateLimit():
		s.sched.MarkRateLimited(ctx, accountID, nil)
	default:
		s.sched.MarkFailure(ctx, accountID, errors.New(mapped.Message))
	}
}

// maybePauseSessionOn429 检查是否属于「非账号额度不足 429」；若是且开关启用，则暂停该会话调度并落盘完整请求体 .log。
func (s *Service) maybePauseSessionOn429(
	req *RelayRequest,
	accountID int64,
	platform string,
	model string,
	mapped *MappedError,
	toolSess *toolobf.Session,
) (abortRetry bool, pauseNotice string) {
	sg := s.SessionGuard()
	if sg == nil || mapped == nil || req == nil {
		return false, ""
	}
	cfg := sg.Config()
	if !cfg.Enabled || !mapped.IsNonQuotaRateLimit(cfg.QuotaKeywords) {
		return false, ""
	}
	sid := strings.TrimSpace(req.resolvedSessionID)
	if sid == "" {
		return false, ""
	}

	var (
		obfOn   bool
		obfMode string
	)
	if to := s.ToolObfuscator(); to != nil {
		effCfg, _ := to.ResolveGroupConfig(req.GroupID)
		obfOn = to.EnabledForGroupPlatform(req.GroupID, platform)
		obfMode = effCfg.Mode
		if !obfOn {
			obfMode = "off"
		}
	}

	paused := sg.PauseAndLog(PauseAndLogInput{
		SessionID:        sid,
		RequestID:        req.RequestID,
		GroupID:          req.GroupID,
		AccountID:        accountID,
		Platform:         platform,
		Protocol:         string(req.Protocol),
		Model:            model,
		MappedErr:        mapped,
		RawInboundBody:   req.rawInboundBody,
		OutboundBody:     req.lastOutboundBody,
		ToolSession:      toolSess,
		ObfGlobalEnabled: obfOn,
		ObfEffectiveMode: obfMode,
	})
	if paused.RemainingSeconds > 0 {
		mapped.RetryAfter = paused.RemainingSeconds
	}
	notice := fmt.Sprintf("上游返回非账号额度不足 429，已暂停会话 %s 调度 %d 秒；诊断日志: %s（未混淆工具数: %d/%d）",
		paused.SessionID, paused.RemainingSeconds, nonEmptyStr(paused.LogFile, "未开启落盘"),
		paused.UnobfuscatedToolsCount, paused.TotalToolsCount)
	if s.logger != nil {
		s.logger.Warn("触发非额度 429 会话暂停并落盘请求体",
			"session_id", paused.SessionID,
			"account_id", accountID,
			"platform", platform,
			"pause_seconds", paused.RemainingSeconds,
			"log_file", paused.LogFile,
			"unobfuscated_tools", paused.UnobfuscatedToolsCount,
			"total_tools", paused.TotalToolsCount,
		)
	}
	return cfg.AbortCurrentRetry, notice
}

// streamEvents 是 CommandCode 与 OpenCode 共用的下游处理链路：
//   - 完整支持 OpenAI (/v1/chat/completions)、Anthropic (/v1/messages) 与 Responses (/v1/responses) 三协议流式及非流式响应；
//   - 通过 StreamCommitBuffer 延迟提交 HTTP 200 头，若首帧产生前遇到流内可重试错误则无缝换号重试；
//   - 统一记录非缓存 InputTokens = max(0, PromptTokens - CachedTokens)、ReasoningTokens 与 Platform。
func (s *Service) streamEvents(
	ctx context.Context,
	w http.ResponseWriter,
	req *RelayRequest,
	normalized *normalizedRequest,
	accountID int64,
	platform string,
	started time.Time,
	toolSess *toolobf.Session,
	read func(handle func(*CCEvent)) error,
) (bool, *MappedError) {
	streaming := normalized.Stream
	model := normalized.Model
	created := time.Now().Unix()
	requestID := req.RequestID
	if strings.TrimSpace(requestID) == "" {
		requestID = GenerateRequestID()
	}

	var (
		commitBuf    *StreamCommitBuffer
		openaiEnc    *OpenAIStreamEncoder
		anthropicEnc *AnthropicStreamEncoder
		respEnc      *ResponsesStreamEncoder
	)
	if streaming {
		commitBuf = NewStreamCommitBuffer(w, true)
		switch req.Protocol {
		case ProtocolAnthropic:
			anthropicEnc = NewAnthropicStreamEncoder(model, "msg_"+requestID)
		case ProtocolResponses:
			respEnc = NewResponsesStreamEncoder(model, requestID, created)
			respEnc.SetCustomTools(normalized.ResponsesCustomTools)
		default:
			openaiEnc = NewOpenAIStreamEncoder(model, "chatcmpl-"+requestID, created)
		}
	}

	var (
		fullText        strings.Builder
		fullReasoning   strings.Builder
		events          []*CCEvent
		firstTokenAt    *time.Time
		markedUsed      bool
		respToolIdx     int
		inStreamErr     *MappedError
	)

	readErr := read(func(ev *CCEvent) {
		if ev == nil {
			return
		}
		if toolSess != nil && ev.ToolName != "" {
			evCopy := *ev
			evCopy.ToolName = toolSess.Restore(ev.ToolName)
			ev = &evCopy
		}
		visible := hasVisibleContent(ev)
		if visible {
			if firstTokenAt == nil {
				now := time.Now()
				firstTokenAt = &now
			}
			if !markedUsed {
				markedUsed = true
				s.sched.MarkUsed(ctx, accountID)
			}
		}
		events = append(events, ev)

		switch ev.Type {
		case "text-delta":
			fullText.WriteString(pickText(ev))
		case "reasoning-delta":
			fullReasoning.WriteString(ev.Text)
		case "error":
			inStreamErr = MapEventError(ev)
		}

		if !streaming || commitBuf == nil || commitBuf.WriteError() != nil {
			return
		}

		commitNow := visible || ev.Type == "finish" || ev.Type == "finish-step"
		switch {
		case anthropicEnc != nil:
			for _, frame := range anthropicEnc.Encode(ev) {
				if err := commitBuf.WriteFrame(frame, commitNow); err != nil {
					break
				}
			}
		case respEnc != nil:
			if chunk := ccEventToNormalizedChunk(ev, &respToolIdx); chunk != nil {
				for _, frame := range respEnc.EncodeChunk(chunk) {
					if err := commitBuf.WriteFrame(frame, commitNow); err != nil {
						break
					}
				}
			}
		case openaiEnc != nil:
			for _, frame := range openaiEnc.Encode(ev) {
				if err := commitBuf.WriteFrame(frame, commitNow); err != nil {
					break
				}
			}
		}
	})

	if streaming && commitBuf != nil && commitBuf.WriteError() != nil {
		s.recordWithUsage(req, accountID, platform, started, openaiEnc, anthropicEnc, respEnc, firstTokenAt,
			499, commitBuf.WriteError().Error())
		return true, nil
	}

	// 检查流内错误
	var upstreamErr *MappedError
	if openaiEnc != nil && openaiEnc.UpstreamError != nil {
		upstreamErr = openaiEnc.UpstreamError
	} else if anthropicEnc != nil && anthropicEnc.UpstreamError() != nil {
		upstreamErr = mapAnthropicStreamError(anthropicEnc.UpstreamError())
	} else if inStreamErr != nil {
		upstreamErr = inStreamErr
	}

	if upstreamErr != nil {
		committed := streaming && commitBuf != nil && commitBuf.IsCommitted()
		s.applyAccountPenalty(ctx, accountID, upstreamErr)
		abortRetry, pauseMsg := s.maybePauseSessionOn429(req, accountID, platform, model, upstreamErr, toolSess)
		errMsg := upstreamErr.Message
		if pauseMsg != "" {
			errMsg = pauseMsg
		}
		if streaming {
			s.recordWithUsage(req, accountID, platform, started, openaiEnc, anthropicEnc, respEnc, firstTokenAt,
				upstreamErr.Status, errMsg)
		} else {
			s.recordForPlatform(req, accountID, platform, started, firstTokenAt, false,
				0, 0, 0, 0, req.RateMultiplier, upstreamErr.Status, errMsg)
		}

		if !committed {
			if abortRetry {
				s.writeClientError(w, req.Protocol, http.StatusTooManyRequests, "rate_limit_error", pauseMsg, upstreamErr.RetryAfter)
				return true, upstreamErr
			}
			if upstreamErr.Retryable() {
				return false, upstreamErr
			}
			s.writeClientError(w, req.Protocol, upstreamErr.Status, upstreamErr.Type, upstreamErr.Message, upstreamErr.RetryAfter)
			return true, upstreamErr
		}
		s.writeStreamError(w, req.Protocol, upstreamErr)
		return true, upstreamErr
	}

	if readErr != nil {
		if ctx.Err() != nil {
			return true, nil
		}
		committed := streaming && commitBuf != nil && commitBuf.IsCommitted()
		if !committed && fullText.Len() == 0 && fullReasoning.Len() == 0 {
			s.sched.MarkFailure(ctx, accountID, readErr)
			s.recordForPlatform(req, accountID, platform, started, firstTokenAt, streaming,
				0, 0, 0, 0, req.RateMultiplier, http.StatusBadGateway, readErr.Error())
			return false, &MappedError{
				Status: http.StatusBadGateway, Type: "upstream_error",
				Message: "读取上游响应失败", ReportedStatus: http.StatusBadGateway,
			}
		}
		s.logger.Warn("读取上游流中断", "err", readErr, "request_id", requestID, "account_id", accountID)
	}

	// 非流式请求收尾：聚合事件并按目标协议输出完整 JSON
	if !streaming {
		sawFinish, finishDetail := checkCCEventsTruncation(events)
		finishReason, usage := finalOutcome(events)
		nonCachedInput := anthropicInputTokens(usage, nil)

		if !sawFinish {
			msg := "上游流未正常结束（" + finishDetail + "），响应可能被截断"
			s.recordForPlatform(req, accountID, platform, started, firstTokenAt, false,
				nonCachedInput, usage.OutputTokens, usage.CachedInputTokens, usage.ReasoningTokens,
				req.RateMultiplier, http.StatusBadGateway, msg)
			s.writeClientError(w, req.Protocol, http.StatusBadGateway, "upstream_error", msg, 10)
			return true, nil
		}

		if !markedUsed {
			s.sched.MarkUsed(ctx, accountID)
		}
		s.recordForPlatform(req, accountID, platform, started, firstTokenAt, false,
			nonCachedInput, usage.OutputTokens, usage.CachedInputTokens, usage.ReasoningTokens,
			req.RateMultiplier, http.StatusOK, "")

		toolCalls := collectToolCalls(events)
		switch req.Protocol {
		case ProtocolResponses:
			normUsage := &NormalizedUsage{
				PromptTokens:     usage.InputTokens,
				CompletionTokens: usage.OutputTokens,
				TotalTokens:      usage.InputTokens + usage.OutputTokens,
				CachedTokens:     usage.CachedInputTokens,
				ReasoningTokens:  usage.ReasoningTokens,
			}
			s.writeResponsesFullResponse(w, model, requestID, created, fullText.String(), fullReasoning.String(),
				toolCalls, finishReason, normUsage, normalized.ResponsesCustomTools)
		case ProtocolAnthropic:
			s.writeAnthropicFullResponse(w, model, requestID, fullText.String(), fullReasoning.String(),
				toolCalls, finishReason, usage)
		default:
			s.writeOpenAIFullResponse(w, model, requestID, created, fullText.String(), fullReasoning.String(),
				toolCalls, finishReason, usage)
		}
		return true, nil
	}

	// 流式请求收尾
	var truncated bool
	var detail string
	if !markedUsed {
		s.sched.MarkUsed(ctx, accountID)
	}
	if err := commitBuf.CommitSSE(); err != nil {
		s.recordWithUsage(req, accountID, platform, started, openaiEnc, anthropicEnc, respEnc, firstTokenAt, 499, err.Error())
		return true, nil
	}

	switch {
	case anthropicEnc != nil:
		frames, trunc, _ := anthropicEnc.Finish()
		for _, frame := range frames {
			if err := commitBuf.WriteFrame(frame, true); err != nil {
				break
			}
		}
		truncated = trunc
	case respEnc != nil:
		if !respEnc.SawFinish() {
			truncated, detail = true, "no finish event"
		}
		for _, frame := range respEnc.Finish() {
			if err := commitBuf.WriteFrame(frame, true); err != nil {
				break
			}
		}
	case openaiEnc != nil:
		truncated, detail = openaiEnc.Finish()
		_ = commitBuf.WriteFrame(openaiEnc.DoneFrame(), true)
	}

	if truncated {
		s.logger.Warn("上游流被截断", "detail", detail, "request_id", requestID)
	}
	s.recordWithUsage(req, accountID, platform, started, openaiEnc, anthropicEnc, respEnc, firstTokenAt, http.StatusOK, "")

	if commitBuf.WriteError() != nil && ctx.Err() == nil {
		s.logger.Debug("向客户端写响应失败（通常是客户端已断开）", "err", commitBuf.WriteError(), "request_id", requestID)
	}
	return true, nil
}

func ccEventToNormalizedChunk(ev *CCEvent, toolIdx *int) *NormalizedChunk {
	if ev == nil {
		return nil
	}
	switch ev.Type {
	case "text-delta":
		txt := pickText(ev)
		if txt == "" {
			return nil
		}
		return &NormalizedChunk{Content: txt}
	case "reasoning-delta":
		if ev.Text == "" {
			return nil
		}
		return &NormalizedChunk{ReasoningContent: ev.Text}
	case "tool-call":
		idx := 0
		if toolIdx != nil {
			idx = *toolIdx
			*toolIdx++
		}
		args := "{}"
		if len(ev.Input) > 0 {
			var asStr string
			if err := json.Unmarshal(ev.Input, &asStr); err == nil {
				args = asStr
			} else {
				args = string(ev.Input)
			}
		}
		return &NormalizedChunk{
			ToolCalls: []ToolCallDelta{{
				Index:     idx,
				ID:        ev.ToolCallID,
				Type:      "function",
				Name:      ev.ToolName,
				Arguments: args,
			}},
		}
	case "finish-step":
		chunk := &NormalizedChunk{FinishReason: ev.FinishReason}
		if ev.Usage != nil {
			chunk.Usage = ccUsageToNormalized(ev.Usage)
		}
		return chunk
	case "finish":
		chunk := &NormalizedChunk{FinishReason: ev.FinishReason}
		if chunk.FinishReason == "" {
			chunk.FinishReason = "stop"
		}
		if ev.TotalUsage != nil {
			uCopy := *ev.TotalUsage
			normalizeUsage(&uCopy)
			chunk.Usage = ccUsageToNormalized(&uCopy)
		}
		return chunk
	default:
		return nil
	}
}

func ccUsageToNormalized(u *CCUsage) *NormalizedUsage {
	if u == nil {
		return nil
	}
	return &NormalizedUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
		CachedTokens:     u.CachedInputTokens,
		ReasoningTokens:  u.ReasoningTokens,
	}
}

func checkCCEventsTruncation(events []*CCEvent) (ok bool, detail string) {
	sawFinish := false
	finishReason := ""
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ev.Type == "finish" || ev.Type == "finish-step" {
			sawFinish = true
			if ev.FinishReason != "" {
				finishReason = mapFinishReason(ev.FinishReason)
			}
		}
	}
	if !sawFinish {
		return false, "no finish event"
	}
	if finishReason == "upstream_error" {
		return false, "provider reported an upstream connection failure"
	}
	return true, ""
}

// readNDJSON 逐行读上游的 NDJSON 流（向后兼容单超时参数签名）。
func (s *Service) readNDJSON(ctx context.Context, body io.ReadCloser, idle time.Duration, handle func(*CCEvent)) error {
	if idle <= 0 {
		idle = defaultStreamIdleTimeout
	}
	return s.readNDJSONWithTimeouts(ctx, body, idle, idle, handle)
}

// readNDJSONWithTimeouts 逐行读上游的 NDJSON 流，首帧到达前使用 firstTokenTimeout，产生可见内容后切换为 idleTimeout。
func (s *Service) readNDJSONWithTimeouts(
	ctx context.Context,
	body io.ReadCloser,
	firstTokenTimeout time.Duration,
	idleTimeout time.Duration,
	handle func(*CCEvent),
) error {
	if idleTimeout <= 0 {
		idleTimeout = defaultStreamIdleTimeout
	}
	if firstTokenTimeout <= 0 {
		firstTokenTimeout = defaultFirstTokenTimeout
	}
	watchdog := time.AfterFunc(firstTokenTimeout, func() { _ = body.Close() })
	defer watchdog.Stop()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	sawVisible := false
	for scanner.Scan() {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if sawVisible {
			watchdog.Reset(idleTimeout)
		} else {
			watchdog.Reset(firstTokenTimeout)
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "[DONE]" || strings.HasPrefix(line, ":") {
			continue
		}
		var ev CCEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			s.logger.Debug("跳过无法解析的上游行", "line", truncate(line, 200))
			continue
		}
		if !sawVisible && hasVisibleContent(&ev) {
			sawVisible = true
			watchdog.Reset(idleTimeout)
		}
		handle(&ev)
	}
	return scanner.Err()
}

// parse 按协议解析请求体。
func (s *Service) parse(req *RelayRequest) (*normalizedRequest, error) {
	switch req.Protocol {
	case ProtocolAnthropic:
		return ParseAnthropicMessagesRequest(req.Body)
	case ProtocolResponses:
		return ParseResponsesRequest(req.Body)
	default:
		return ParseOpenAIChatRequest(req.Body)
	}
}

func pickText(ev *CCEvent) string {
	if ev.Text != "" {
		return ev.Text
	}
	return ev.Delta
}

// hasVisibleContent 判断这个事件是否算「看到了第一个 token」。
func hasVisibleContent(ev *CCEvent) bool {
	switch ev.Type {
	case "text-delta", "reasoning-delta", "tool-call":
		return true
	}
	return false
}

// finalOutcome 从攒下的所有事件里推出结束原因和用量。
func finalOutcome(events []*CCEvent) (string, *CCUsage) {
	finishReason := "stop"
	usage := &CCUsage{}
	for _, ev := range events {
		switch ev.Type {
		case "finish-step":
			if ev.FinishReason != "" {
				finishReason = mapFinishReason(ev.FinishReason)
			}
			if ev.Usage != nil {
				usage = ev.Usage
			}
		case "finish":
			if ev.FinishReason != "" {
				finishReason = mapFinishReason(ev.FinishReason)
			}
			if ev.TotalUsage != nil {
				usage = ev.TotalUsage
			}
		}
	}
	normalizeUsage(usage)
	return finishReason, usage
}

// record 记一笔用量（保留兼容）。
func (s *Service) record(req *RelayRequest, accountID int64, started time.Time,
	inputTokens, outputTokens, cachedTokens int, multiplier float64,
	statusCode int, errMsg string) {
	s.recordForPlatform(req, accountID, "", started, nil, false,
		inputTokens, outputTokens, cachedTokens, 0, multiplier, statusCode, errMsg)
}

func (s *Service) recordForPlatform(
	req *RelayRequest,
	accountID int64,
	platform string,
	started time.Time,
	firstTokenAt *time.Time,
	stream bool,
	inputTokens, outputTokens, cachedTokens, reasoningTokens int,
	multiplier float64,
	statusCode int,
	errMsg string,
) {
	if s.recorder == nil {
		return
	}

	rec := newUsageRecord(req, accountID, started, statusCode, errMsg)
	rec.Platform = domain.NormalizePlatform(platform)
	rec.Stream = stream
	rec.Model = requestModel(req.Body)
	if firstTokenAt != nil {
		ms := int(firstTokenAt.Sub(started).Milliseconds())
		rec.FirstTokenMs = &ms
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	rec.InputTokens = inputTokens
	rec.OutputTokens = outputTokens
	rec.CacheReadTokens = cachedTokens
	rec.ReasoningTokens = reasoningTokens
	rec.RateMultiplier = multiplier

	if err := safeRecord(context.WithoutCancel(context.Background()), s.recorder, rec); err != nil {
		s.logger.Warn("记录用量失败", "err", err, "request_id", req.RequestID)
	}
}

func newUsageRecord(req *RelayRequest, accountID int64, started time.Time,
	statusCode int, errMsg string) UsageRecord {
	groupID := req.GroupID
	return UsageRecord{
		UserID:         req.UserID,
		APIKeyID:       req.APIKeyID,
		AccountID:      accountID,
		GroupID:        &groupID,
		RequestID:      req.RequestID,
		Stream:         true,
		DurationMs:     int(time.Since(started).Milliseconds()),
		StatusCode:     statusCode,
		ErrorMessage:   errMsg,
		UserAgent:      req.UserAgent,
		IPAddress:      req.ClientIP,
		RateMultiplier: req.RateMultiplier,
	}
}

func requestModel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.Model)
}

// recordWithUsage 记一笔流式用量，统一确保 InputTokens 为扣除缓存后的纯输入 Token。
func (s *Service) recordWithUsage(
	req *RelayRequest,
	accountID int64,
	platform string,
	started time.Time,
	openaiEnc *OpenAIStreamEncoder,
	anthropicEnc *AnthropicStreamEncoder,
	respEnc *ResponsesStreamEncoder,
	firstTokenAt *time.Time,
	statusCode int,
	errMsg string,
) {
	if s.recorder == nil {
		return
	}

	rec := newUsageRecord(req, accountID, started, statusCode, errMsg)
	rec.Platform = domain.NormalizePlatform(platform)

	if firstTokenAt != nil {
		ms := int(firstTokenAt.Sub(started).Milliseconds())
		rec.FirstTokenMs = &ms
	}

	switch {
	case openaiEnc != nil:
		nonCached := openaiEnc.InputTokens - openaiEnc.CachedInputTokens
		if nonCached < 0 {
			nonCached = 0
		}
		rec.InputTokens = nonCached
		rec.OutputTokens = openaiEnc.OutputTokens
		rec.CacheReadTokens = openaiEnc.CachedInputTokens
		rec.ReasoningTokens = openaiEnc.ReasoningTokens
		rec.Model = openaiEnc.model
	case anthropicEnc != nil:
		// anthropicEnc.Usage().InputTokens 已为扣除缓存后的非缓存部分
		u := anthropicEnc.Usage()
		rec.InputTokens = u.InputTokens
		rec.OutputTokens = u.OutputTokens
		rec.CacheReadTokens = u.CacheReadInputTokens
		rec.CacheCreationTokens = u.CacheCreationInputTokens
		rec.Model = anthropicEnc.model
	case respEnc != nil:
		u := respEnc.Usage()
		nonCached := u.PromptTokens - u.CachedTokens
		if nonCached < 0 {
			nonCached = 0
		}
		rec.InputTokens = nonCached
		rec.OutputTokens = u.CompletionTokens
		rec.CacheReadTokens = u.CachedTokens
		rec.ReasoningTokens = u.ReasoningTokens
		rec.Model = respEnc.model
	}

	if rec.Model == "" {
		rec.Model = requestModel(req.Body)
	}

	if err := safeRecord(context.WithoutCancel(context.Background()), s.recorder, rec); err != nil {
		s.logger.Warn("记录用量失败", "err", err, "request_id", req.RequestID)
	}
}

func safeRecord(ctx context.Context, r UsageRecorder, rec UsageRecord) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("recorder panic: %v", v)
		}
	}()
	r.Record(ctx, rec)
	return nil
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func syncNormalizedMessages(normalized *normalizedRequest, updated []map[string]any) {
	if normalized == nil {
		return
	}
	normalized.Messages = updated
	if normalized.RawOpenAIBody != nil {
		outAny := make([]any, 0, len(updated))
		for _, m := range updated {
			outAny = append(outAny, m)
		}
		normalized.RawOpenAIBody["messages"] = outAny
	}
}

func injectGlobalSystemPrompt(normalized *normalizedRequest, promptText string) {
	if normalized == nil {
		return
	}
	trimmed := strings.TrimSpace(promptText)
	if trimmed == "" {
		return
	}
	sysMsg := map[string]any{
		"role":    "system",
		"content": trimmed,
	}
	updated := prependSystemPromptMessage(normalized.Messages, sysMsg, trimmed)
	syncNormalizedMessages(normalized, updated)
}

func prependSystemPromptMessage(msgs []map[string]any, sysMsg map[string]any, trimmedPrompt string) []map[string]any {
	if len(msgs) > 0 {
		role, _ := msgs[0]["role"].(string)
		content, _ := msgs[0]["content"].(string)
		if strings.EqualFold(strings.TrimSpace(role), "system") && strings.TrimSpace(content) == trimmedPrompt {
			return msgs
		}
	}
	out := make([]map[string]any, 0, len(msgs)+1)
	out = append(out, sysMsg)
	out = append(out, msgs...)
	return out
}
