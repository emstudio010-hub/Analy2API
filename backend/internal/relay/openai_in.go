package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// normalizedRequest 是归一化后的请求。无论客户端说 OpenAI 还是 Anthropic，
// 走到信封构造这一步都是这个形状。
//
// 用 map 装 messages/tools 而不是强类型 struct：客户端可能带任意扩展字段，
// 强类型化会静默丢字段，而透传是这里的正确行为。
type normalizedRequest struct {
	Model             string
	Messages          []map[string]any
	MaxTokens         *int
	Temperature       *float64
	Tools             []map[string]any
	Stream            bool
	ReasoningEffort   *string
	ToolChoice        any
	ParallelToolCalls *bool
	PromptCacheKey    string

	// SessionHints 是客户端自带的会话标识候选，按优先级排列。
	SessionHints []string

	// RawOpenAIBody 保存已归一化到 OpenAI Chat Completions 形状的请求体，
	// 供 WorkBuddy NormalizeCompat 直接清洗并保留扩展字段。
	RawOpenAIBody map[string]any

	// ResponsesCustomTools 记录 /v1/responses 入站请求中声明为 type="custom" 的自由文本工具名（如 Codex apply_patch），
	// 用于响应编码时原样以 custom_tool_call 格式回传。
	ResponsesCustomTools map[string]bool
}

// errEmptyMessages 表示请求里没有可用的对话消息。
var errEmptyMessages = errors.New("messages 不能为空")

// toOpenAIBody 把归一化请求渲染回 OpenAI Chat Completions 形状。
//
// 给 OpenCode 这类 OpenAI 兼容上游用：它们要的就是 OpenAI 格式，
// 不需要经过 Command Code 的信封包装。这样入站无论是哪种协议，
// 到 OpenCode 这一路都会先被归一成 OpenAI 形状再发出去。
func (r *normalizedRequest) toOpenAIBody() ([]byte, error) {
	body := map[string]any{
		"model":    r.Model,
		"messages": r.Messages,
		// 内部一律按流处理，所以向 OpenCode 也始终要流式。
		"stream": true,
		// 必须显式要求上游在最后一片里带用量，否则 token 统计拿不到，
		// 用量日志会全是 0。非 OpenAI 官方实现可能忽略这个字段，但没有副作用。
		"stream_options": map[string]any{"include_usage": true},
	}
	if r.MaxTokens != nil {
		body["max_tokens"] = *r.MaxTokens
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if len(r.Tools) > 0 {
		body["tools"] = r.Tools
	}
	if r.ToolChoice != nil {
		body["tool_choice"] = r.ToolChoice
	}
	if r.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *r.ParallelToolCalls
	}
	if r.ReasoningEffort != nil {
		body["reasoning_effort"] = *r.ReasoningEffort
	}
	if r.PromptCacheKey != "" {
		body["prompt_cache_key"] = r.PromptCacheKey
	}
	return json.Marshal(body)
}

// ParseOpenAIChatRequest 解析 OpenAI Chat Completions 请求。
func ParseOpenAIChatRequest(raw []byte) (*normalizedRequest, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("解析请求体: %w", err)
	}
	req, err := normalizeOpenAIShape(body)
	if err != nil {
		return nil, err
	}
	req.RawOpenAIBody = body
	return req, nil
}

// ParseAnthropicMessagesRequest 解析 Anthropic Messages 请求。
//
// 先转成 OpenAI 形状再走同一条归一化路径——这样两种协议在后续所有环节
// 共享同一套代码，不必维护两条平行的流水线。
func ParseAnthropicMessagesRequest(raw []byte) (*normalizedRequest, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("解析请求体: %w", err)
	}
	converted, err := ConvertAnthropicToOpenAI(body)
	if err != nil {
		return nil, fmt.Errorf("转换 Anthropic 请求: %w", err)
	}
	req, err := normalizeOpenAIShape(converted)
	if err != nil {
		return nil, err
	}
	req.RawOpenAIBody = converted
	return req, nil
}

// normalizeOpenAIShape 把 OpenAI 形状的 body 读进 normalizedRequest。
func normalizeOpenAIShape(body map[string]any) (*normalizedRequest, error) {
	req := &normalizedRequest{
		PromptCacheKey: stringField(body, "prompt_cache_key"),
	}

	req.Model = stringField(body, "model")
	if req.Model == "" {
		return nil, errors.New("缺少 model 字段")
	}

	req.Messages = messageList(body["messages"])
	if len(req.Messages) == 0 {
		return nil, errEmptyMessages
	}

	req.MaxTokens = intField(body, "max_tokens", "max_completion_tokens")
	req.Temperature = floatField(body, "temperature")
	req.ReasoningEffort = stringFieldPtr(body, "reasoning_effort")
	req.ToolChoice = body["tool_choice"]
	req.ParallelToolCalls = boolFieldPtr(body, "parallel_tool_calls")
	req.Tools = toolList(body["tools"])

	// 客户端没显式给 stream 时按非流式处理；但到底怎么回由 service 决定，
	// CC 上游永远是流式的。
	req.Stream, _ = body["stream"].(bool)

	req.SessionHints = sessionHints(body)
	return req, nil
}

// sessionHints 收集客户端自带的会话标识。
//
// 沿用同一个会话 ID 是正常 CLI 的行为；cmd2api 擅自重新分配反而会制造出
// 「每次请求都是新会话」的异常模式。
func sessionHints(body map[string]any) []string {
	var hints []string
	for _, key := range []string{"session_id", "x-session-id", "x-claude-code-session-id"} {
		if v := stringField(body, key); v != "" {
			hints = append(hints, v)
		}
	}
	if v := stringField(body, "prompt_cache_key"); v != "" {
		hints = append(hints, v)
	}
	// metadata.user_id 是 Anthropic SDK 常用的会话标记位置。
	if md, ok := body["metadata"].(map[string]any); ok {
		if v := stringField(md, "user_id"); v != "" {
			hints = append(hints, v)
		}
	}
	return hints
}

// ---- 宽松的类型读取 ----
//
// JSON 里数字可能是 float64 也可能是 json.Number，还可能是字符串；
// 客户端五花八门，这里统一容错，不做严格校验。

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func stringFieldPtr(m map[string]any, key string) *string {
	if v, ok := m[key].(string); ok && v != "" {
		return &v
	}
	return nil
}

func floatField(m map[string]any, key string) *float64 {
	switch v := m[key].(type) {
	case float64:
		return &v
	case int:
		f := float64(v)
		return &f
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return &f
		}
	}
	return nil
}

func boolFieldPtr(m map[string]any, key string) *bool {
	if v, ok := m[key].(bool); ok {
		return &v
	}
	return nil
}

func intField(m map[string]any, keys ...string) *int {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			n := int(v)
			return &n
		case int:
			n := v
			return &n
		case json.Number:
			if n, err := v.Int64(); err == nil {
				i := int(n)
				return &i
			}
		}
	}
	return nil
}

func messageList(v any) []map[string]any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func toolList(v any) []map[string]any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			if norm := normalizeInboundSpecialTool(m); norm != nil {
				out = append(out, norm)
			}
		}
	}
	return out
}

// normalizeInboundSpecialTool 统一适配来自 Codex / DSH / Claude Code / OpenAI / Anthropic 的全部特殊工具定义，
// 将非标准 function 形态（custom / local_shell / shell / web_search_preview / bash_* / text_editor_* / mcp / 扁平 input_schema 等）
// 无损规整为标准 OpenAI function 工具，确保跨四大上游直接混淆透传且零丢弃。
func normalizeInboundSpecialTool(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	if fn, ok := m["function"].(map[string]any); ok && fn != nil {
		return m
	}

	rawType := strings.ToLower(strings.TrimSpace(stringField(m, "type")))
	switch rawType {
	case "custom":
		name := firstNonEmptyStr(stringField(m, "name"), "custom_tool")
		desc := strings.TrimSpace(stringField(m, "description"))
		if fmtMap, ok := m["format"].(map[string]any); ok && fmtMap != nil {
			if fmtDef := strings.TrimSpace(stringField(fmtMap, "definition")); fmtDef != "" {
				if desc != "" {
					desc += "\n\nFormat grammar:\n" + fmtDef
				} else {
					desc = "Format grammar:\n" + fmtDef
				}
			}
		}
		fn := map[string]any{"name": name}
		if desc != "" {
			fn["description"] = desc
		}
		if params, ok := m["parameters"]; ok && params != nil {
			fn["parameters"] = params
		} else {
			fn["parameters"] = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{
						"type":        "string",
						"description": "Raw input payload for this custom tool",
					},
				},
				"required": []any{"input"},
			}
		}
		return map[string]any{"type": "function", "function": fn}

	case "local_shell", "shell":
		name := firstNonEmptyStr(stringField(m, "name"), rawType)
		desc := firstNonEmptyStr(stringField(m, "description"), "Execute a shell command in the caller's environment")
		fn := map[string]any{"name": name, "description": desc}
		if params, ok := m["parameters"]; ok && params != nil {
			fn["parameters"] = params
		} else {
			fn["parameters"] = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Command and arguments to execute",
					},
					"working_directory": map[string]any{"type": "string"},
					"timeout_ms":        map[string]any{"type": "integer"},
				},
				"required": []any{"command"},
			}
		}
		return map[string]any{"type": "function", "function": fn}

	default:
		name := firstNonEmptyStr(stringField(m, "name"), stringField(m, "server_label"), rawType)
		if name == "" {
			return m
		}
		fn := map[string]any{"name": name}
		if desc := strings.TrimSpace(stringField(m, "description")); desc != "" {
			fn["description"] = desc
		} else if rawType != "" && rawType != "function" {
			fn["description"] = "Caller tool (" + rawType + ")"
		}
		if params, ok := m["parameters"]; ok && params != nil {
			fn["parameters"] = params
		} else if schema, ok := m["input_schema"]; ok && schema != nil {
			fn["parameters"] = schema
		} else {
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		return map[string]any{"type": "function", "function": fn}
	}
}
