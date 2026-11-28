package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/domain"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"
)

func (s *Service) getAGClient() *antigravity.Client {
	s.mu.RLock()
	ag := s.agClient
	s.mu.RUnlock()
	if ag != nil {
		return ag
	}
	if s.client != nil {
		return s.client.Antigravity()
	}
	return antigravity.NewClientWithPool(s.getWBClient().ProxyPool(), s.logger)
}

func (s *Service) agModels(ctx context.Context, spec antigravity.NodeSpec) ([]Model, error) {
	ag := s.getAGClient()
	items, err := ag.Models(ctx, spec)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if s.logger != nil {
			s.logger.Warn("拉取 AntiGravity 节点模型列表失败，使用权威默认目录", "base_url", antigravity.BaseURL(spec), "err", err)
		}
		return convertAntigravityModels(antigravity.DefaultModels()), nil
	}
	if len(items) == 0 {
		return convertAntigravityModels(antigravity.DefaultModels()), nil
	}
	return convertAntigravityModels(items), nil
}

func convertAntigravityModels(items []antigravity.ModelItem) []Model {
	out := make([]Model, 0, len(items))
	for _, m := range items {
		out = append(out, Model{
			ID:                m.ID,
			Name:              m.Name,
			Vendor:            m.Vendor,
			ContextWindow:     m.ContextWindow,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportsImages:    m.SupportsImages,
			SupportsReasoning: m.SupportsReasoning,
			SupportsToolCall:  m.SupportsToolCall,
		})
	}
	return out
}

// attemptAntigravity 通过 AntiGravity / CLIProxyAPI 节点的 `/v1/chat/completions` 执行单次尝试：
//   - 请求经 NormalizeCompat 归一化清洗（developer->system、tool_choice、孤儿 tool_calls）
//   - 自动遵守本机回环（127.0.0.1/localhost）直连绕过外部代理、远程节点按 lease.ProxyURL 独立出口隔离
//   - 直接复用 consumeWorkBuddyStream，天然支持 OpenAI / Anthropic / Responses 三协议流式与非流式、
//     首帧前可重试错误无缝换号、三级超时看门狗、首字 TTFT 与缓存去重计费统计。
func (s *Service) attemptAntigravity(
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
		s.logger.Error("序列化 AntiGravity 请求失败", "err", err, "request_id", req.RequestID)
		s.writeClientError(w, req.Protocol, http.StatusInternalServerError, "api_error", "构造上游请求失败", 0)
		return true, nil
	}
	req.lastOutboundBody = payloadBytes

	spec := lease.AntigravityNodeSpec()
	if strings.TrimSpace(spec.HostIP) == "" {
		spec.HostIP = antigravity.BaseURL(antigravity.NodeSpec{})
	}
	spec.ProxyURL = lease.ProxyURL

	sessionID := ExtractSessionID(req, normalized, true)
	resp, wireBody, err := s.getAGClient().ChatStreamWithSession(ctx, spec, payloadBytes, sessionID)
	if len(wireBody) > 0 {
		req.lastOutboundBody = wireBody
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.recordPlatformUsage(req, lease.Account.ID, domain.PlatformAntigravity, started, normalized.Model, nil, normalized.Stream, 499, "client closed request", nil)
			return true, nil
		}

		mapped := MapAntigravityError(err)
		var apiErr *antigravity.APIError
		if errors.As(err, &apiErr) {
			s.applyAccountPenalty(ctx, lease.Account.ID, mapped)
			abortRetry, pauseMsg := s.maybePauseSessionOn429(req, lease.Account.ID, domain.PlatformAntigravity, normalized.Model, mapped, toolSess)
			errMsg := mapped.Message
			if pauseMsg != "" {
				errMsg = pauseMsg
			}
			s.recordPlatformUsage(req, lease.Account.ID, domain.PlatformAntigravity, started, normalized.Model, nil, normalized.Stream, mapped.ReportedStatus, errMsg, nil)
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

		if s.logger != nil {
			s.logger.Warn("请求 AntiGravity 节点失败", "err", err, "request_id", req.RequestID, "account_id", lease.Account.ID)
		}
		s.sched.MarkFailure(ctx, lease.Account.ID, fmt.Errorf("antigravity request failed: %w", err))
		s.recordPlatformUsage(req, lease.Account.ID, domain.PlatformAntigravity, started, normalized.Model, nil, normalized.Stream, mapped.ReportedStatus, mapped.Message, nil)
		return false, mapped
	}
	defer resp.Body.Close()

	return s.consumeWorkBuddyStream(ctx, w, req, normalized, lease, started, resp.Body, toolSess)
}

// MapAntigravityError 将 AntiGravity 客户端返回的错误映射为统一的 MappedError。
func MapAntigravityError(err error) *MappedError {
	if err == nil {
		return nil
	}
	var apiErr *antigravity.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		rawBody, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"code":    apiErr.Code,
				"message": apiErr.Message,
			},
		})
		mapped := MapHTTPError(apiErr.Status, rawBody)
		if strings.TrimSpace(apiErr.Message) != "" {
			mapped.Message = strings.TrimSpace(apiErr.Message)
		}
		if strings.TrimSpace(apiErr.Code) != "" {
			mapped.Code = strings.TrimSpace(apiErr.Code)
		}
		return mapped
	}
	return &MappedError{
		Status:         http.StatusBadGateway,
		Type:           "upstream_error",
		Message:        "无法连接 AntiGravity 节点: " + err.Error(),
		ReportedStatus: http.StatusBadGateway,
	}
}

// probeAntigravity 探活单个 AntiGravity 账号节点（优先动态挑选轻量模型发起 1-token 流式验证）。
func (c *Client) probeAntigravity(ctx context.Context, target *scheduler.Target) ProbeResult {
	started := time.Now()
	probeCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
	}

	ag := c.Antigravity()
	spec := target.AntigravityNodeSpec()

	models, err := ag.Models(probeCtx, spec)
	if err != nil {
		if ctx.Err() != nil {
			return ProbeResult{Latency: time.Since(started), Err: &MappedError{
				Status: http.StatusGatewayTimeout, Type: "upstream_error", Message: "探活超时",
			}}
		}
		return ProbeResult{Latency: time.Since(started), Err: MapAntigravityError(err)}
	}

	probeModel := "gemini-3.8-flash-low"
	if len(models) > 0 {
		probeModel = models[0].ID
		agProbePrefs := []string{"gemini-3.8-flash-low", "gemini-4-argon-low", "gemini-3.5-flash-low", "flash", "low"}
		for _, pref := range agProbePrefs {
			found := false
			for _, m := range models {
				if strings.Contains(strings.ToLower(m.ID), pref) {
					probeModel = m.ID
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"model":      probeModel,
		"messages":   []map[string]string{{"role": "user", "content": probePrompt}},
		"max_tokens": 1,
		"stream":     true,
	})

	resp, err := ag.ChatStream(probeCtx, spec, payload)
	if err != nil {
		if ctx.Err() != nil {
			return ProbeResult{Latency: time.Since(started), Err: &MappedError{
				Status: http.StatusGatewayTimeout, Type: "upstream_error", Message: "探活超时",
			}}
		}
		mapped := MapAntigravityError(err)
		mapped.Message = fmt.Sprintf("%s（探活模型：%s）", mapped.Message, probeModel)
		return ProbeResult{Latency: time.Since(started), Err: mapped}
	}
	defer resp.Body.Close()

	norm := NewStreamDeltaNormalizer(probeModel, "chatcmpl-probe", time.Now().Unix())
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		chunk, done, skip := norm.NormalizeLine(scanner.Text())
		if done {
			return ProbeResult{Latency: time.Since(started)}
		}
		if skip || chunk == nil {
			continue
		}
		if chunk.Err != nil {
			return ProbeResult{Latency: time.Since(started), Err: chunk.Err}
		}
		if chunk.HasVisibleContent() {
			return ProbeResult{Latency: time.Since(started)}
		}
	}
	if err := scanner.Err(); err != nil {
		return ProbeResult{Latency: time.Since(started), Err: &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error", Message: "读取 AntiGravity 探活流失败: " + err.Error(),
		}}
	}
	return ProbeResult{Latency: time.Since(started)}
}

// PlatformModels 按指定平台（或传 "" / "all" 聚合四大平台）拉取可用模型目录。
// 当某平台当前无活跃账号时自动回退该平台权威内置模型列表，方便模型广场聚合展示。
func (s *Service) PlatformModels(ctx context.Context, platform string) ([]Model, error) {
	trimmed := strings.TrimSpace(platform)
	if trimmed == "" || strings.EqualFold(trimmed, "all") {
		platforms := []string{
			domain.PlatformWorkBuddy,
			domain.PlatformAntigravity,
			domain.PlatformCommandCode,
			domain.PlatformOpenCode,
		}
		seen := make(map[string]struct{}, 128)
		merged := make([]Model, 0, 128)
		for _, p := range platforms {
			items, err := s.singlePlatformModels(ctx, p)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			for _, m := range items {
				if m.ID == "" {
					continue
				}
				if _, dup := seen[m.ID]; dup {
					continue
				}
				seen[m.ID] = struct{}{}
				merged = append(merged, m)
			}
		}
		return merged, nil
	}
	return s.singlePlatformModels(ctx, domain.NormalizePlatform(trimmed))
}

func (s *Service) singlePlatformModels(ctx context.Context, normPlatform string) ([]Model, error) {
	var target *scheduler.Target
	if s.sched != nil {
		target, _ = s.sched.AnyTargetByPlatform(ctx, normPlatform)
	}

	switch normPlatform {
	case domain.PlatformAntigravity:
		if target != nil {
			return s.agModels(ctx, target.AntigravityNodeSpec())
		}
		return convertAntigravityModels(antigravity.DefaultModels()), nil

	case domain.PlatformCommandCode:
		if target != nil && s.client != nil {
			return enrichModelsMetadata(s.client.ModelsWithProxy(ctx, target.APIKey, target.ProxyURL)), nil
		}
		return enrichModelsMetadata(cloneModelsSlice(hardcodedModels)), nil

	case domain.PlatformOpenCode:
		if target != nil && s.client != nil {
			items := s.client.openCodeModelsWithProxy(ctx, target.BaseURL, target.APIKey, target.ProxyURL)
			if len(items) > 0 {
				return enrichModelsMetadata(items), nil
			}
		}
		return defaultOpenCodeModels(), nil

	case domain.PlatformWorkBuddy:
		if target != nil {
			return s.wbModels(ctx, target.WorkBuddyCreds())
		}
		return convertWorkBuddyModels(workbuddy.DefaultModels()), nil

	default:
		return nil, fmt.Errorf("不支持的上游平台: %s", normPlatform)
	}
}

func cloneModelsSlice(in []Model) []Model {
	out := make([]Model, len(in))
	copy(out, in)
	return out
}

func enrichModelsMetadata(items []Model) []Model {
	out := make([]Model, 0, len(items))
	for _, m := range items {
		if m.Vendor == "" || m.ContextWindow == 0 {
			inferred, ok := antigravity.InferModelItem(map[string]any{
				"id":           m.ID,
				"display_name": m.Name,
				"vendor":       m.Vendor,
			})
			if ok {
				if m.Vendor == "" {
					m.Vendor = inferred.Vendor
				}
				if m.Name == "" || m.Name == m.ID {
					m.Name = inferred.Name
				}
				if m.ContextWindow == 0 {
					m.ContextWindow = inferred.ContextWindow
				}
				if m.MaxOutputTokens == 0 {
					m.MaxOutputTokens = inferred.MaxOutputTokens
				}
				if !m.SupportsImages {
					m.SupportsImages = inferred.SupportsImages
				}
				if !m.SupportsReasoning {
					m.SupportsReasoning = inferred.SupportsReasoning
				}
				if !m.SupportsToolCall {
					m.SupportsToolCall = inferred.SupportsToolCall
				}
			}
		}
		out = append(out, m)
	}
	return out
}

func defaultOpenCodeModels() []Model {
	raw := []Model{
		{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5"},
		{ID: "claude-opus-4-5", Name: "Claude Opus 4.5"},
		{ID: "gpt-5", Name: "GPT-5"},
		{ID: "gpt-5-mini", Name: "GPT-5 Mini"},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"},
		{ID: "minimax-m2.7", Name: "MiniMax M2.7"},
		{ID: "kimi-k2.5", Name: "Kimi K2.5"},
		{ID: "glm-5", Name: "GLM-5"},
	}
	return enrichModelsMetadata(raw)
}
