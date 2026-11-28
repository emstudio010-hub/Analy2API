package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/domain"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"
)

func (s *Service) getWBClient() *workbuddy.Client {
	if s.wbClient != nil {
		return s.wbClient
	}
	if s.client != nil && s.client.WorkBuddy() != nil {
		return s.client.WorkBuddy()
	}
	return workbuddy.DefaultClient()
}

func (s *Service) wbModels(ctx context.Context, creds workbuddy.AccountCreds) ([]Model, error) {
	wb := s.getWBClient()
	items, err := wb.Models(ctx, creds)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return convertWorkBuddyModels(workbuddy.DefaultModels()), nil
	}
	return convertWorkBuddyModels(items), nil
}

func convertWorkBuddyModels(items []workbuddy.ModelInfo) []Model {
	out := make([]Model, 0, len(items))
	for _, m := range items {
		out = append(out, Model{
			ID:                m.ID,
			Name:              m.Name,
			Vendor:            m.Vendor,
			Description:       m.Description,
			Credits:           m.Credits,
			CreditMultiplier:  m.CreditMultiplier,
			ContextWindow:     m.ContextWindow,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportsImages:    m.SupportsImages,
			SupportsReasoning: m.SupportsReasoning,
			SupportsToolCall:  m.SupportsToolCall,
			Tags:              m.Tags,
		})
	}
	return out
}

type wbCachedRefresh struct {
	accessToken  string
	refreshToken string
	domain       string
	at           time.Time
}

func (s *Service) wbRefreshMutex(accountID int64) *sync.Mutex {
	val, _ := s.wbRefreshLocks.LoadOrStore(accountID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// refreshWorkBuddyCredsSingleflight 按账号串行化 401 Token 续期：
// 当多个并发请求在同一过期账号上同时遇到 401 时，仅首个协程向远端发起 RefreshToken（避免单次有效 refresh_token 被并发作废），
// 其余等待协程直接复用刚刚刷新成功的最新凭证重试。
func (s *Service) refreshWorkBuddyCredsSingleflight(
	ctx context.Context,
	accountID int64,
	wb *workbuddy.Client,
	creds workbuddy.AccountCreds,
) (workbuddy.AccountCreds, bool) {
	mu := s.wbRefreshMutex(accountID)
	mu.Lock()
	defer mu.Unlock()

	if raw, ok := s.wbRefreshCache.Load(accountID); ok {
		cached := raw.(wbCachedRefresh)
		if cached.accessToken != "" && cached.accessToken != creds.AccessToken && time.Since(cached.at) < 30*time.Second {
			creds.AccessToken = cached.accessToken
			if cached.refreshToken != "" {
				creds.RefreshToken = cached.refreshToken
			}
			if cached.domain != "" {
				creds.Domain = cached.domain
			}
			return creds, true
		}
	}

	refCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	refRes, refErr := wb.RefreshToken(refCtx, creds)
	cancel()
	if refErr != nil || strings.TrimSpace(refRes.AccessToken) == "" {
		return creds, false
	}

	creds.AccessToken = strings.TrimSpace(refRes.AccessToken)
	if strings.TrimSpace(refRes.RefreshToken) != "" {
		creds.RefreshToken = strings.TrimSpace(refRes.RefreshToken)
	}
	if strings.TrimSpace(refRes.Domain) != "" {
		creds.Domain = strings.TrimSpace(refRes.Domain)
	}
	s.wbRefreshCache.Store(accountID, wbCachedRefresh{
		accessToken:  creds.AccessToken,
		refreshToken: creds.RefreshToken,
		domain:       creds.Domain,
		at:           time.Now(),
	})
	if s.tokenUpdater != nil {
		if upErr := s.tokenUpdater.UpdateTokens(context.WithoutCancel(ctx), accountID, creds.AccessToken, creds.RefreshToken); upErr != nil && s.logger != nil {
			s.logger.Warn("写回 WorkBuddy 续期 Token 失败", "account_id", accountID, "err", upErr)
		}
	}
	return creds, true
}
// attemptWorkBuddy 通过 WorkBuddy `/v2/chat/completions` 上游中转单次账号尝试：
//   - 对标准化请求执行 NormalizeCompat（developer->system、tool_choice、孤儿 tool_calls、DeepSeek reasoning_content、stream:true）
//   - 严格使用账号绑定的 ProxyURL 与 Qimei36/MachineID
//   - 若遇到 401 且账号拥有 RefreshToken，在同账号上先自动续期 Token 写回数据库并原地重试一次
//   - 消费 SSE 时通过 StreamDeltaNormalizer 清洗空占位符与心跳，并借助 StreamCommitBuffer 在首个有效帧前支持无缝切换下一个账号
func (s *Service) attemptWorkBuddy(
	ctx context.Context,
	w http.ResponseWriter,
	req *RelayRequest,
	normalized *normalizedRequest,
	lease *scheduler.Lease,
	started time.Time,
	toolSess *toolobf.Session,
) (done bool, mappedErr *MappedError) {
	rawOpenAI := buildWorkBuddyBaseBody(normalized)
	compatBody, err := NormalizeCompat(rawOpenAI)
	if err != nil {
		s.writeClientError(w, req.Protocol, http.StatusBadRequest, "invalid_request_error", err.Error(), 0)
		return true, nil
	}

	payloadBytes, err := json.Marshal(compatBody)
	if err != nil {
		s.logger.Error("序列化 WorkBuddy 请求失败", "err", err, "request_id", req.RequestID)
		s.writeClientError(w, req.Protocol, http.StatusInternalServerError, "api_error", "构造上游请求失败", 0)
		return true, nil
	}
	req.lastOutboundBody = payloadBytes

	wb := s.getWBClient()
	creds := lease.WorkBuddyCreds()

	resp, err := wb.ChatStream(ctx, creds, payloadBytes)
	if err != nil {
		var apiErr *workbuddy.APIError
		if errors.As(err, &apiErr) && apiErr.IsAuthFailure() && strings.TrimSpace(creds.RefreshToken) != "" {
			if nextCreds, refreshed := s.refreshWorkBuddyCredsSingleflight(ctx, lease.Account.ID, wb, creds); refreshed {
				creds = nextCreds
				resp, err = wb.ChatStream(ctx, creds, payloadBytes)
			}
		}
	}

	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.recordWorkBuddyUsage(req, lease.Account.ID, started, normalized.Model, nil, normalized.Stream, 499, "client closed request", nil)
			return true, nil
		}
		var apiErr *workbuddy.APIError
		if errors.As(err, &apiErr) {
			mapped := MapWorkBuddyError(err)
			s.applyAccountPenalty(ctx, lease.Account.ID, mapped)
			abortRetry, pauseMsg := s.maybePauseSessionOn429(req, lease.Account.ID, domain.PlatformWorkBuddy, normalized.Model, mapped, toolSess)
			errMsg := mapped.Message
			if pauseMsg != "" {
				errMsg = pauseMsg
			}
			s.recordWorkBuddyUsage(req, lease.Account.ID, started, normalized.Model, nil, normalized.Stream, mapped.ReportedStatus, errMsg, nil)
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

		mapped := MapWorkBuddyError(err)
		if s.logger != nil {
			s.logger.Warn("请求 WorkBuddy 上游失败", "err", err, "request_id", req.RequestID, "account_id", lease.Account.ID)
		}
		s.sched.MarkFailure(ctx, lease.Account.ID, fmt.Errorf("workbuddy request failed: %w", err))
		s.recordWorkBuddyUsage(req, lease.Account.ID, started, normalized.Model, nil, normalized.Stream, mapped.ReportedStatus, mapped.Message, nil)
		return false, mapped
	}
	defer resp.Body.Close()

	return s.consumeWorkBuddyStream(ctx, w, req, normalized, lease, started, resp.Body, toolSess)
}

func buildWorkBuddyBaseBody(normalized *normalizedRequest) map[string]any {
	if normalized.RawOpenAIBody != nil {
		cloned := cloneJSONMap(normalized.RawOpenAIBody)
		if normalized.Model != "" {
			cloned["model"] = normalized.Model
		}
		return cloned
	}
	body := map[string]any{
		"model":    normalized.Model,
		"messages": normalized.Messages,
		"stream":   true,
	}
	if len(normalized.Tools) > 0 {
		body["tools"] = normalized.Tools
	}
	if normalized.MaxTokens != nil && *normalized.MaxTokens > 0 {
		body["max_tokens"] = *normalized.MaxTokens
	}
	return body
}

func (s *Service) consumeWorkBuddyStream(
	ctx context.Context,
	w http.ResponseWriter,
	req *RelayRequest,
	normalized *normalizedRequest,
	lease *scheduler.Lease,
	started time.Time,
	body io.ReadCloser,
	toolSess *toolobf.Session,
) (done bool, mappedErr *MappedError) {
	reqID := req.RequestID
	if strings.TrimSpace(reqID) == "" {
		reqID = GenerateRequestID()
	}
	completionID := "chatcmpl-" + reqID
	created := time.Now().Unix()
	model := normalized.Model
	if model == "" {
		model = requestModel(req.Body)
	}
	clientStream := normalized.Stream
	platform := domain.NormalizePlatform(lease.Platform)
	if platform == "" {
		platform = domain.PlatformWorkBuddy
	}

	firstTokenTimeout, idleTimeout := s.resolveTimeouts(clientStream)

	normalizer := NewStreamDeltaNormalizer(model, completionID, created)
	normalizer.SetToolSession(toolSess)
	var commitBuf *StreamCommitBuffer
	if clientStream {
		commitBuf = NewStreamCommitBuffer(w, true)
	}

	var respEnc *ResponsesStreamEncoder
	var anthEnc *wbAnthropicStreamEncoder
	if clientStream {
		switch req.Protocol {
		case ProtocolResponses:
			respEnc = NewResponsesStreamEncoder(model, reqID, created)
			respEnc.SetCustomTools(normalized.ResponsesCustomTools)
		case ProtocolAnthropic:
			anthEnc = newWBAnthropicStreamEncoder(model, "msg_"+reqID)
		}
	}

	var (
		fullText      strings.Builder
		fullReasoning strings.Builder
		toolCallMap   = make(map[int]*openAIToolCall)
		toolOrder     []int
		finishReason  = "stop"
		usage         NormalizedUsage
		hasUsage      bool
		hasVisible    bool
		markedUsed    bool
		firstTokenAt  *time.Time
		streamErr     *MappedError
	)

	readErr := normalizer.StreamWithTimeouts(ctx, body, firstTokenTimeout, idleTimeout, func(chunk *NormalizedChunk) {
		if chunk == nil {
			return
		}
		if chunk.Err != nil {
			streamErr = chunk.Err
			return
		}
		if chunk.Usage != nil {
			hasUsage = true
			if chunk.Usage.PromptTokens > 0 {
				usage.PromptTokens = chunk.Usage.PromptTokens
			}
			if chunk.Usage.CompletionTokens > 0 {
				usage.CompletionTokens = chunk.Usage.CompletionTokens
			}
			if chunk.Usage.TotalTokens > 0 {
				usage.TotalTokens = chunk.Usage.TotalTokens
			}
			if chunk.Usage.CachedTokens > 0 {
				usage.CachedTokens = chunk.Usage.CachedTokens
			}
			if chunk.Usage.CacheCreationTokens > 0 {
				usage.CacheCreationTokens = chunk.Usage.CacheCreationTokens
			}
			if chunk.Usage.ReasoningTokens > 0 {
				usage.ReasoningTokens = chunk.Usage.ReasoningTokens
			}
			if chunk.Usage.Credit > 0 {
				usage.Credit = chunk.Usage.Credit
			}
		}
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
		if chunk.Content != "" {
			fullText.WriteString(chunk.Content)
		}
		if chunk.ReasoningContent != "" {
			fullReasoning.WriteString(chunk.ReasoningContent)
		}
		for _, tc := range chunk.ToolCalls {
			st, exists := toolCallMap[tc.Index]
			if !exists {
				st = &openAIToolCall{
					ID:   tc.ID,
					Type: "function",
				}
				st.Function.Name = tc.Name
				toolCallMap[tc.Index] = st
				toolOrder = append(toolOrder, tc.Index)
			} else {
				if tc.ID != "" && st.ID == "" {
					st.ID = tc.ID
				}
				if tc.Name != "" {
					st.Function.Name += tc.Name
				}
			}
			if tc.Arguments != "" {
				st.Function.Arguments += tc.Arguments
			}
		}

		commitNow := chunk.HasVisibleContent()
		if commitNow {
			hasVisible = true
			if firstTokenAt == nil {
				now := time.Now()
				firstTokenAt = &now
			}
			if !markedUsed {
				markedUsed = true
				s.sched.MarkUsed(ctx, lease.Account.ID)
			}
		}

		if clientStream && commitBuf != nil && commitBuf.WriteError() == nil {
			switch req.Protocol {
			case ProtocolResponses:
				for _, frame := range respEnc.EncodeChunk(chunk) {
					if err := commitBuf.WriteFrame(frame, commitNow); err != nil {
						break
					}
				}
			case ProtocolAnthropic:
				for _, frame := range anthEnc.EncodeChunk(chunk) {
					if err := commitBuf.WriteFrame(frame, commitNow); err != nil {
						break
					}
				}
			default:
				if len(chunk.CleanJSON) > 0 {
					frame := "data: " + string(chunk.CleanJSON) + "\n\n"
					_ = commitBuf.WriteFrame(frame, commitNow)
				}
			}
		}
	})

	if clientStream && commitBuf != nil && commitBuf.WriteError() != nil {
		s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, true, 499, commitBuf.WriteError().Error(), &usage)
		return true, nil
	}

	if streamErr != nil {
		committed := clientStream && commitBuf != nil && commitBuf.IsCommitted()
		if !committed {
			s.applyAccountPenalty(ctx, lease.Account.ID, streamErr)
			abortRetry, pauseMsg := s.maybePauseSessionOn429(req, lease.Account.ID, platform, model, streamErr, toolSess)
			errMsg := streamErr.Message
			if pauseMsg != "" {
				errMsg = pauseMsg
			}
			s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, clientStream, streamErr.ReportedStatus, errMsg, &usage)
			if abortRetry {
				s.writeClientError(w, req.Protocol, http.StatusTooManyRequests, "rate_limit_error", pauseMsg, streamErr.RetryAfter)
				return true, streamErr
			}
			if streamErr.Retryable() {
				return false, streamErr
			}
			s.writeClientError(w, req.Protocol, streamErr.Status, streamErr.Type, streamErr.Message, streamErr.RetryAfter)
			return true, streamErr
		}
		s.writeStreamError(w, req.Protocol, streamErr)
		s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, true, streamErr.ReportedStatus, streamErr.Message, &usage)
		return true, nil
	}

	if readErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, clientStream, 499, "client closed request", &usage)
			return true, nil
		}
		committed := clientStream && commitBuf != nil && commitBuf.IsCommitted()
		mapped := &MappedError{
			Status:         http.StatusBadGateway,
			Type:           "upstream_error",
			Message:        readErr.Error(),
			ReportedStatus: http.StatusBadGateway,
		}
		if !committed {
			s.sched.MarkFailure(ctx, lease.Account.ID, readErr)
			s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, clientStream, mapped.ReportedStatus, mapped.Message, &usage)
			return false, mapped
		}
		s.writeStreamError(w, req.Protocol, mapped)
		s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, true, mapped.ReportedStatus, mapped.Message, &usage)
		return true, nil
	}

	committed := clientStream && commitBuf != nil && commitBuf.IsCommitted()
	if !hasVisible && !committed {
		emptyErr := &MappedError{
			Status:         http.StatusBadGateway,
			Type:           "upstream_error",
			Message:        "upstream closed stream without output",
			ReportedStatus: http.StatusBadGateway,
		}
		s.sched.MarkFailure(ctx, lease.Account.ID, errors.New(emptyErr.Message))
		s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, clientStream, emptyErr.ReportedStatus, emptyErr.Message, &usage)
		return false, emptyErr
	}

	if !markedUsed {
		s.sched.MarkUsed(ctx, lease.Account.ID)
	}

	if hasUsage && usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	toolCalls := orderedToolCalls(toolOrder, toolCallMap)

	if clientStream && commitBuf != nil {
		if err := commitBuf.CommitSSE(); err != nil {
			s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, true, 499, err.Error(), &usage)
			return true, nil
		}
		switch req.Protocol {
		case ProtocolResponses:
			for _, frame := range respEnc.Finish() {
				if err := commitBuf.WriteFrame(frame, true); err != nil {
					break
				}
			}
		case ProtocolAnthropic:
			for _, frame := range anthEnc.Finish(finishReason, &usage) {
				if err := commitBuf.WriteFrame(frame, true); err != nil {
					break
				}
			}
		default:
			_ = commitBuf.WriteFrame("data: [DONE]\n\n", true)
		}
		s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, true, http.StatusOK, "", &usage)
		return true, nil
	}

	// 非流式路径：已收集完整上游 SSE，按入站协议聚合输出标准 JSON 响应。
	switch req.Protocol {
	case ProtocolResponses:
		s.writeResponsesFullResponse(w, model, reqID, created, fullText.String(), fullReasoning.String(), toolCalls, finishReason, &usage, normalized.ResponsesCustomTools)
	case ProtocolAnthropic:
		ccUsage := normalizedUsageToCC(&usage)
		s.writeAnthropicFullResponse(w, model, reqID, fullText.String(), fullReasoning.String(), toolCalls, finishReason, ccUsage)
	default:
		ccUsage := normalizedUsageToCC(&usage)
		s.writeOpenAIFullResponse(w, model, reqID, created, fullText.String(), fullReasoning.String(), toolCalls, finishReason, ccUsage)
	}
	s.recordPlatformUsage(req, lease.Account.ID, platform, started, model, firstTokenAt, false, http.StatusOK, "", &usage)
	return true, nil
}

func orderedToolCalls(order []int, byIdx map[int]*openAIToolCall) []openAIToolCall {
	out := make([]openAIToolCall, 0, len(order))
	for _, idx := range order {
		if tc := byIdx[idx]; tc != nil {
			out = append(out, *tc)
		}
	}
	return out
}

func normalizedUsageToCC(u *NormalizedUsage) *CCUsage {
	if u == nil {
		return &CCUsage{}
	}
	noCache := u.PromptTokens - u.CachedTokens
	if noCache < 0 {
		noCache = u.PromptTokens
	}
	return &CCUsage{
		InputTokens:       u.PromptTokens,
		OutputTokens:      u.CompletionTokens,
		CachedInputTokens: u.CachedTokens,
		InputTokenDetails: &InputTokenDetail{
			NoCacheTokens:   noCache,
			CacheReadTokens: u.CachedTokens,
		},
	}
}

func (s *Service) recordWorkBuddyUsage(
	req *RelayRequest,
	accountID int64,
	started time.Time,
	model string,
	firstTokenAt *time.Time,
	stream bool,
	statusCode int,
	errMsg string,
	u *NormalizedUsage,
) {
	s.recordPlatformUsage(req, accountID, domain.PlatformWorkBuddy, started, model, firstTokenAt, stream, statusCode, errMsg, u)
}

func (s *Service) recordPlatformUsage(
	req *RelayRequest,
	accountID int64,
	platform string,
	started time.Time,
	model string,
	firstTokenAt *time.Time,
	stream bool,
	statusCode int,
	errMsg string,
	u *NormalizedUsage,
) {
	if s.recorder == nil {
		return
	}
	rec := newUsageRecord(req, accountID, started, statusCode, errMsg)
	rec.Platform = domain.NormalizePlatform(platform)
	rec.Stream = stream
	rec.Model = model
	if rec.Model == "" {
		rec.Model = requestModel(req.Body)
	}
	if firstTokenAt != nil {
		ms := int(firstTokenAt.Sub(started).Milliseconds())
		rec.FirstTokenMs = &ms
	}
	if u != nil {
		// OpenAI / WorkBuddy / AntiGravity 返回的 prompt_tokens 已包含 cached_tokens，
		// 统一折算为非缓存 InputTokens = max(0, PromptTokens - CachedTokens)，避免重复计费。
		nonCached := u.PromptTokens - u.CachedTokens
		if nonCached < 0 {
			nonCached = 0
		}
		rec.InputTokens = nonCached
		rec.OutputTokens = u.CompletionTokens
		rec.CacheReadTokens = u.CachedTokens
		rec.CacheCreationTokens = u.CacheCreationTokens
		rec.ReasoningTokens = u.ReasoningTokens
		if u.Credit > 0 {
			rec.TotalCost = u.Credit * req.RateMultiplier
		}
	}
	if err := safeRecord(context.WithoutCancel(context.Background()), s.recorder, rec); err != nil && s.logger != nil {
		s.logger.Warn("记录上游用量失败", "platform", rec.Platform, "err", err, "request_id", req.RequestID)
	}
}

type wbAnthToolState struct {
	id       string
	name     string
	args     strings.Builder
	blockIdx int
	started  bool
	closed   bool
}

// wbAnthropicStreamEncoder 直接把 NormalizedChunk 编码为 Anthropic Messages SSE，
// 避免 CompletionTokens 缺失时误判为空响应。
type wbAnthropicStreamEncoder struct {
	model     string
	messageID string

	started        bool
	nextBlockIndex int
	openBlockType  string
	openBlockIndex int
	openToolIdx    int
	thinkingBuf    strings.Builder
	toolStates     map[int]*wbAnthToolState
	toolOrder      []int
	hasToolCalls   bool
}

func newWBAnthropicStreamEncoder(model, messageID string) *wbAnthropicStreamEncoder {
	return &wbAnthropicStreamEncoder{
		model:       model,
		messageID:   messageID,
		openToolIdx: -1,
		toolStates:  make(map[int]*wbAnthToolState),
	}
}

func (e *wbAnthropicStreamEncoder) ensureStarted() []string {
	if e.started {
		return nil
	}
	e.started = true
	return []string{
		anthropicSSEFrame("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            e.messageID,
				"type":          "message",
				"role":          "assistant",
				"model":         e.model,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  0,
					"output_tokens": 0,
				},
			},
		}),
	}
}

func (e *wbAnthropicStreamEncoder) closeActiveBlock() []string {
	if e.openBlockType == "" {
		return nil
	}
	var out []string
	if e.openBlockType == "thinking" {
		out = append(out, anthropicSSEFrame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.openBlockIndex,
			"delta": map[string]any{
				"type":      "signature_delta",
				"signature": FakeThinkingSignature(e.thinkingBuf.String()),
			},
		}))
	}
	if e.openBlockType == "tool_use" && e.openToolIdx >= 0 {
		if st := e.toolStates[e.openToolIdx]; st != nil {
			st.closed = true
		}
		e.openToolIdx = -1
	}
	out = append(out, anthropicSSEFrame("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": e.openBlockIndex,
	}))
	e.openBlockType = ""
	return out
}

func (e *wbAnthropicStreamEncoder) startToolBlock(tcIdx int, st *wbAnthToolState) []string {
	var out []string
	out = append(out, e.closeActiveBlock()...)
	blockIdx := e.nextBlockIndex
	e.nextBlockIndex++
	st.blockIdx = blockIdx
	st.started = true
	e.openBlockType = "tool_use"
	e.openBlockIndex = blockIdx
	e.openToolIdx = tcIdx

	callID := st.id
	if callID == "" {
		callID = fmt.Sprintf("toolu_%s_%d", e.messageID, tcIdx)
		st.id = callID
	}
	name := st.name
	if name == "" {
		name = "tool"
	}
	out = append(out, anthropicSSEFrame("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": blockIdx,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    callID,
			"name":  name,
			"input": map[string]any{},
		},
	}))
	if st.args.Len() > 0 {
		buffered := st.args.String()
		st.args.Reset()
		out = append(out, anthropicSSEFrame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": blockIdx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": buffered,
			},
		}))
	}
	return out
}

func (e *wbAnthropicStreamEncoder) EncodeChunk(chunk *NormalizedChunk) []string {
	if chunk == nil {
		return nil
	}
	var out []string
	if chunk.HasVisibleContent() {
		out = append(out, e.ensureStarted()...)
	}

	if chunk.ReasoningContent != "" {
		if e.openBlockType != "thinking" {
			out = append(out, e.closeActiveBlock()...)
			e.openBlockType = "thinking"
			e.openBlockIndex = e.nextBlockIndex
			e.nextBlockIndex++
			out = append(out, anthropicSSEFrame("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": e.openBlockIndex,
				"content_block": map[string]any{
					"type":     "thinking",
					"thinking": "",
				},
			}))
		}
		e.thinkingBuf.WriteString(chunk.ReasoningContent)
		out = append(out, anthropicSSEFrame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.openBlockIndex,
			"delta": map[string]any{
				"type":     "thinking_delta",
				"thinking": chunk.ReasoningContent,
			},
		}))
	}

	if chunk.Content != "" {
		if e.openBlockType != "text" {
			out = append(out, e.closeActiveBlock()...)
			e.openBlockType = "text"
			e.openBlockIndex = e.nextBlockIndex
			e.nextBlockIndex++
			out = append(out, anthropicSSEFrame("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": e.openBlockIndex,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			}))
		}
		out = append(out, anthropicSSEFrame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.openBlockIndex,
			"delta": map[string]any{
				"type": "text_delta",
				"text": chunk.Content,
			},
		}))
	}

	for _, tc := range chunk.ToolCalls {
		e.hasToolCalls = true
		st, exists := e.toolStates[tc.Index]
		if !exists {
			st = &wbAnthToolState{
				id:   tc.ID,
				name: tc.Name,
			}
			e.toolStates[tc.Index] = st
			e.toolOrder = append(e.toolOrder, tc.Index)
		} else {
			if tc.ID != "" && st.id == "" {
				st.id = tc.ID
			}
			if tc.Name != "" {
				st.name += tc.Name
			}
		}

		if st.started {
			if !st.closed && tc.Arguments != "" {
				out = append(out, anthropicSSEFrame("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": st.blockIdx,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": tc.Arguments,
					},
				}))
			}
			continue
		}

		if tc.Arguments != "" {
			st.args.WriteString(tc.Arguments)
		}
		// 若首片仅携带 id 而尚未送达工具名，或当前已有另一个正在等待参数的并行 tool_use 块，暂缓开启新块以避免空名或提前关闭前序块。
		if st.name == "" {
			continue
		}
		if e.openBlockType == "tool_use" && e.openToolIdx != tc.Index && tc.Arguments == "" {
			continue
		}
		out = append(out, e.startToolBlock(tc.Index, st)...)
	}
	return out
}

func (e *wbAnthropicStreamEncoder) Finish(finishReason string, usage *NormalizedUsage) []string {
	var out []string
	out = append(out, e.ensureStarted()...)
	for _, idx := range e.toolOrder {
		if st := e.toolStates[idx]; st != nil && !st.started {
			out = append(out, e.startToolBlock(idx, st)...)
		}
	}
	out = append(out, e.closeActiveBlock()...)

	if e.hasToolCalls && (finishReason == "" || finishReason == "stop") {
		finishReason = "tool_calls"
	}
	if usage == nil {
		usage = &NormalizedUsage{}
	}
	inputNonCached := usage.PromptTokens - usage.CachedTokens
	if inputNonCached < 0 {
		inputNonCached = usage.PromptTokens
	}
	out = append(out, anthropicSSEFrame("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   MapAnthropicStopReason(finishReason),
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"input_tokens":                inputNonCached,
			"output_tokens":               usage.CompletionTokens,
			"cache_read_input_tokens":     usage.CachedTokens,
			"cache_creation_input_tokens": usage.CacheCreationTokens,
		},
	}))
	out = append(out, anthropicSSEFrame("message_stop", map[string]any{
		"type": "message_stop",
	}))
	return out
}
