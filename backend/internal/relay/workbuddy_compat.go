package relay

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"cmd2api/internal/workbuddy"
)

// NormalizeCompat 将标准 OpenAI Chat Completions 请求体规范化为 WorkBuddy `/v2/chat/completions`
// 严格兼容的形状：
//  1. role: "developer" -> "system"
//  2. tool_choice 对象转字符串形态（"auto" / "required" / 函数名 / "none" 删除 tools）
//  3. CleanupOrphanToolCalls 清理未配对的 assistant.tool_calls 与孤儿 role:tool 消息
//  4. DeepSeek 模型历史 reasoning_content 一致性回填 + thinking intent / reasoning_effort 归一
//  5. 强制 stream: true 与 stream_options: {"include_usage": true}
//  6. 保留 prompt_cache_key、metadata、tools 等下游元信息
func NormalizeCompat(body map[string]any) (map[string]any, error) {
	if body == nil {
		return nil, errors.New("请求体不能为空")
	}

	out := cloneJSONMap(body)
	model, _ := out["model"].(string)

	// 1. 归一化消息角色并清理不兼容或孤立工具调用
	msgs := extractMessageMaps(out["messages"])
	msgs = normalizeRoles(msgs)
	msgs = CleanupOrphanToolCalls(msgs)
	msgs = sanitizeToolCallArguments(msgs)

	// 2. 思维开关与 reasoning_effort 归一
	out = applyThinkingIntent(out, model)
	out = normalizeReasoningEffort(out, model)

	// 3. DeepSeek 模型回填 reasoning_content，防止多轮工具调用报错
	msgs = backfillReasoningContent(msgs, out, model)
	out["messages"] = msgs

	// 4. tool_choice 对象转换为上游字符串形态
	out = normalizeToolChoice(out)

	// 5. 强制流式 + usage 统计
	out["stream"] = true
	so, _ := out["stream_options"].(map[string]any)
	if so == nil {
		so = map[string]any{}
	} else {
		so = cloneJSONMap(so)
	}
	so["include_usage"] = true
	out["stream_options"] = so

	return out, nil
}

// CleanupOrphanToolCalls 按“整条 assistant 消息全留或全清”的原则清理孤儿 tool_calls 和孤儿 tool 结果。
//
// 仅当一条 assistant 消息里的所有 tool_calls 在后续/全部消息中都有对应的 role:"tool" 响应时才保留其 tool_calls；
// 清理后重算全部存活的 tool_call_id，并过滤掉没有对应 assistant tool_call 的 role:"tool" 消息。
func CleanupOrphanToolCalls(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return messages
	}

	// Pass 1: 收集全部存在的 tool 结果 ID。
	toolResultIDs := make(map[string]struct{})
	for _, msg := range messages {
		if stringField(msg, "role") != "tool" {
			continue
		}
		if id := strings.TrimSpace(stringField(msg, "tool_call_id")); id != "" {
			toolResultIDs[id] = struct{}{}
		}
	}

	// Pass 2: 逐条校验 assistant.tool_calls，不完整配对的整段剥除 tool_calls。
	cleaned := make([]map[string]any, 0, len(messages))
	survivingCallIDs := make(map[string]struct{})

	for _, rawMsg := range messages {
		msg := cloneJSONMap(rawMsg)
		role := stringField(msg, "role")
		if role == "assistant" {
			callIDs := extractAssistantToolCallIDs(msg)
			if len(callIDs) > 0 {
				allMatched := true
				for _, cid := range callIDs {
					if _, ok := toolResultIDs[cid]; !ok {
						allMatched = false
						break
					}
				}
				if !allMatched {
					delete(msg, "tool_calls")
					if msg["content"] == nil {
						msg["content"] = ""
					}
					// 若这条 assistant 消息只剩空 content 且没有 reasoning_content，
					// 跳过它避免产生连续空 assistant 轮次。
					if !hasNonEmptyAssistantPayload(msg) {
						continue
					}
				} else {
					for _, cid := range callIDs {
						survivingCallIDs[cid] = struct{}{}
					}
				}
			}
		}
		cleaned = append(cleaned, msg)
	}

	// Pass 3: 过滤掉没有对应存活 assistant.tool_call 的孤儿 role:"tool" 消息。
	finalMsgs := make([]map[string]any, 0, len(cleaned))
	for _, msg := range cleaned {
		if stringField(msg, "role") == "tool" {
			id := strings.TrimSpace(stringField(msg, "tool_call_id"))
			if id == "" {
				continue
			}
			if _, ok := survivingCallIDs[id]; !ok {
				continue
			}
		}
		finalMsgs = append(finalMsgs, msg)
	}
	return finalMsgs
}

func normalizeRoles(messages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, raw := range messages {
		msg := cloneJSONMap(raw)
		if stringField(msg, "role") == "developer" {
			msg["role"] = "system"
		}
		out = append(out, msg)
	}
	return out
}

func normalizeToolChoice(body map[string]any) map[string]any {
	tc, exists := body["tool_choice"]
	if !exists || tc == nil {
		delete(body, "tool_choice")
		return body
	}

	switch v := tc.(type) {
	case string:
		norm := strings.ToLower(strings.TrimSpace(v))
		switch norm {
		case "":
			delete(body, "tool_choice")
		case "none":
			delete(body, "tool_choice")
			delete(body, "tools")
			delete(body, "functions")
		case "auto", "required":
			body["tool_choice"] = norm
		case "any":
			body["tool_choice"] = "required"
		default:
			body["tool_choice"] = v
		}
	case map[string]any:
		t := strings.ToLower(strings.TrimSpace(stringField(v, "type")))
		switch t {
		case "none":
			delete(body, "tool_choice")
			delete(body, "tools")
			delete(body, "functions")
		case "auto":
			body["tool_choice"] = "auto"
		case "required", "any":
			body["tool_choice"] = "required"
		case "function", "tool":
			fnName := stringField(v, "name")
			if fn, ok := v["function"].(map[string]any); ok {
				if name := stringField(fn, "name"); name != "" {
					fnName = name
				}
			}
			if fnName != "" {
				body["tool_choice"] = fnName
			} else {
				delete(body, "tool_choice")
			}
		default:
			if name := stringField(v, "name"); name != "" {
				body["tool_choice"] = name
			} else {
				delete(body, "tool_choice")
			}
		}
	}
	return body
}

func applyThinkingIntent(body map[string]any, model string) map[string]any {
	effort := strings.ToLower(strings.TrimSpace(stringField(body, "reasoning_effort")))
	switch effort {
	case "off", "disabled", "none":
		delete(body, "reasoning_effort")
		if _, hasThinking := body["thinking"]; !hasThinking {
			body["thinking"] = map[string]any{"type": "disabled"}
		}
	}
	if _, ok := body["reasoning"]; ok {
		if rMap, isMap := body["reasoning"].(map[string]any); isMap {
			if re := strings.ToLower(strings.TrimSpace(stringField(rMap, "effort"))); re != "" && body["reasoning_effort"] == nil {
				if re == "off" || re == "disabled" || re == "none" {
					body["thinking"] = map[string]any{"type": "disabled"}
				} else {
					body["reasoning_effort"] = re
				}
			}
		}
		delete(body, "reasoning")
	}
	return body
}

func normalizeReasoningEffort(body map[string]any, model string) map[string]any {
	effort, ok := body["reasoning_effort"].(string)
	if !ok {
		return body
	}
	lower := strings.ToLower(strings.TrimSpace(effort))
	switch lower {
	case "":
		delete(body, "reasoning_effort")
	case "minimal":
		body["reasoning_effort"] = "low"
	case "xhigh":
		if isDeepSeekModel(model) || strings.Contains(strings.ToLower(model), "claude") {
			body["reasoning_effort"] = "high"
		}
	}
	return body
}

func backfillReasoningContent(messages []map[string]any, body map[string]any, model string) []map[string]any {
	hasReasoningSignal := isDeepSeekModel(model) && !isThinkingDisabled(body)
	if !hasReasoningSignal {
		for _, msg := range messages {
			if stringField(msg, "role") != "assistant" {
				continue
			}
			if stringField(msg, "reasoning_content") != "" || stringField(msg, "reasoning") != "" {
				hasReasoningSignal = true
				break
			}
		}
	}

	out := make([]map[string]any, 0, len(messages))
	for _, raw := range messages {
		msg := cloneJSONMap(raw)
		if stringField(msg, "role") == "assistant" {
			if r := stringField(msg, "reasoning"); r != "" && stringField(msg, "reasoning_content") == "" {
				msg["reasoning_content"] = r
			}
			delete(msg, "reasoning")
			if isDeepSeekModel(model) && hasReasoningSignal {
				if _, ok := msg["reasoning_content"]; !ok {
					msg["reasoning_content"] = ""
				}
			}
		}
		out = append(out, msg)
	}
	return out
}

func sanitizeToolCallArguments(messages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, raw := range messages {
		msg := cloneJSONMap(raw)
		if tcs, ok := msg["tool_calls"].([]any); ok {
			validTCs := make([]any, 0, len(tcs))
			for _, tcRaw := range tcs {
				tc, ok := tcRaw.(map[string]any)
				if !ok {
					continue
				}
				tcCopy := cloneJSONMap(tc)
				if fn, ok := tcCopy["function"].(map[string]any); ok {
					fnCopy := cloneJSONMap(fn)
					switch args := fnCopy["arguments"].(type) {
					case string:
						trimmed := strings.TrimSpace(args)
						if trimmed == "" || !json.Valid([]byte(trimmed)) {
							fnCopy["arguments"] = "{}"
						}
					case nil:
						fnCopy["arguments"] = "{}"
					default:
						b, err := json.Marshal(args)
						if err != nil {
							fnCopy["arguments"] = "{}"
						} else {
							fnCopy["arguments"] = string(b)
						}
					}
					tcCopy["function"] = fnCopy
				}
				validTCs = append(validTCs, tcCopy)
			}
			if len(validTCs) == 0 {
				delete(msg, "tool_calls")
			} else {
				msg["tool_calls"] = validTCs
			}
		}
		out = append(out, msg)
	}
	return out
}

func isDeepSeekModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "deepseek")
}

func isThinkingDisabled(body map[string]any) bool {
	if t, ok := body["thinking"].(map[string]any); ok {
		return strings.EqualFold(stringField(t, "type"), "disabled")
	}
	return false
}

func extractMessageMaps(v any) []map[string]any {
	switch s := v.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(s))
		for _, item := range s {
			if item != nil {
				out = append(out, item)
			}
		}
		return out
	case []any:
		return messageList(s)
	default:
		return nil
	}
}

func extractAssistantToolCallIDs(msg map[string]any) []string {
	raw, ok := msg["tool_calls"].([]any)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		tc, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id := strings.TrimSpace(stringField(tc, "id")); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func hasNonEmptyAssistantPayload(msg map[string]any) bool {
	if stringField(msg, "reasoning_content") != "" || stringField(msg, "reasoning") != "" {
		return true
	}
	switch c := msg["content"].(type) {
	case string:
		return strings.TrimSpace(c) != ""
	case []any:
		return len(c) > 0
	default:
		return c != nil
	}
}

func cloneJSONMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	b, err := json.Marshal(src)
	if err != nil {
		cp := make(map[string]any, len(src))
		for k, v := range src {
			cp[k] = v
		}
		return cp
	}
	var dst map[string]any
	if err := json.Unmarshal(b, &dst); err != nil {
		cp := make(map[string]any, len(src))
		for k, v := range src {
			cp[k] = v
		}
		return cp
	}
	return dst
}

// MapWorkBuddyError 将 *workbuddy.APIError 或通用网络/上游错误映射为网关统一的 *MappedError。
func MapWorkBuddyError(err error) *MappedError {
	if err == nil {
		return nil
	}
	var apiErr *workbuddy.APIError
	if errors.As(err, &apiErr) {
		msg := strings.TrimSpace(apiErr.Message)
		if msg == "" {
			msg = apiErr.Error()
		}
		codeStr := ""
		if apiErr.Code != 0 {
			codeStr = jsonNumberString(apiErr.Code)
		}
		status := apiErr.Status
		if status == 0 {
			status = http.StatusBadGateway
		}

		// 先按 WorkBuddy 业务码细分
		switch {
		case apiErr.IsModelUnauthorized():
			// 11102: 当前账号无该模型权限，允许调度层尝试下一个账号。
			return &MappedError{
				Status:         http.StatusForbidden,
				Type:           "permission_error",
				Message:        msg,
				Code:           codeStr,
				ReportedStatus: http.StatusForbidden,
			}
		case apiErr.IsNotSupported():
			return &MappedError{
				Status:         http.StatusBadRequest,
				Type:           "invalid_request_error",
				Message:        msg,
				Code:           codeStr,
				ReportedStatus: http.StatusBadRequest,
			}
		case apiErr.Code == 11128:
			// 11128: 积分/额度不足或套餐受限
			return &MappedError{
				Status:         http.StatusTooManyRequests,
				Type:           "rate_limit_error",
				Message:        msg,
				Code:           codeStr,
				ReportedStatus: http.StatusPaymentRequired,
				RetryAfter:     defaultRetryAfter,
			}
		case apiErr.IsAuthFailure():
			return &MappedError{
				Status:         http.StatusUnauthorized,
				Type:           "authentication_error",
				Message:        msg,
				Code:           codeStr,
				ReportedStatus: http.StatusUnauthorized,
			}
		}

		mapped, ok := ccStatusMap[status]
		if !ok {
			mapped = statusMapping{http.StatusBadGateway, "upstream_error"}
		}
		out := &MappedError{
			Status:         mapped.Status,
			Type:           mapped.Type,
			Message:        msg,
			Code:           codeStr,
			ReportedStatus: status,
		}
		if out.Status == http.StatusTooManyRequests {
			out.RetryAfter = defaultRetryAfter
		}
		lowerMsg := strings.ToLower(msg)
		if strings.Contains(lowerMsg, "credit") || strings.Contains(lowerMsg, "quota") || strings.Contains(lowerMsg, "insufficient") {
			out.Status = http.StatusTooManyRequests
			out.Type = "rate_limit_error"
			out.ReportedStatus = http.StatusTooManyRequests
			out.RetryAfter = defaultRetryAfter
		}
		return out
	}

	return &MappedError{
		Status:         http.StatusBadGateway,
		Type:           "upstream_error",
		Message:        err.Error(),
		ReportedStatus: http.StatusBadGateway,
	}
}

func jsonNumberString(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
