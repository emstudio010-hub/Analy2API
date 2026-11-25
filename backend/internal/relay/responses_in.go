package relay

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ParseResponsesRequest 解析 OpenAI Responses API (/v1/responses) 请求（如 Codex CLI）。
func ParseResponsesRequest(raw []byte) (*normalizedRequest, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("解析请求体: %w", err)
	}
	converted, customTools, err := convertResponsesToOpenAIWithMeta(body)
	if err != nil {
		return nil, fmt.Errorf("转换 Responses 请求: %w", err)
	}
	req, err := normalizeOpenAIShape(converted)
	if err != nil {
		return nil, err
	}
	req.RawOpenAIBody = converted
	if len(customTools) > 0 {
		req.ResponsesCustomTools = customTools
	}
	return req, nil
}

// ConvertResponsesToOpenAI 将 OpenAI Responses API 请求体转换为标准 OpenAI Chat Completions 形状。
func ConvertResponsesToOpenAI(body map[string]any) (map[string]any, error) {
	out, _, err := convertResponsesToOpenAIWithMeta(body)
	return out, err
}

func convertResponsesToOpenAIWithMeta(body map[string]any) (map[string]any, map[string]bool, error) {
	if body == nil {
		return nil, nil, fmt.Errorf("请求体不能为空")
	}
	model := strings.TrimSpace(stringField(body, "model"))
	if model == "" {
		return nil, nil, fmt.Errorf("缺少 model 字段")
	}

	out := map[string]any{
		"model": model,
	}
	if stream, ok := body["stream"].(bool); ok {
		out["stream"] = stream
	}
	if temp, ok := body["temperature"]; ok && temp != nil {
		out["temperature"] = temp
	}
	if topP, ok := body["top_p"]; ok && topP != nil {
		out["top_p"] = topP
	}
	if maxOut := intField(body, "max_output_tokens", "max_tokens"); maxOut != nil {
		out["max_tokens"] = *maxOut
	}
	if ptc, ok := body["parallel_tool_calls"]; ok && ptc != nil {
		out["parallel_tool_calls"] = ptc
	}
	if pck := stringField(body, "prompt_cache_key"); pck != "" {
		out["prompt_cache_key"] = pck
	}
	if md, ok := body["metadata"]; ok && md != nil {
		out["metadata"] = md
	}
	if user := stringField(body, "user"); user != "" {
		out["user"] = user
	}

	// reasoning: {"effort": "high", ...} -> reasoning_effort
	if rMap, ok := body["reasoning"].(map[string]any); ok {
		if effort := strings.TrimSpace(stringField(rMap, "effort")); effort != "" {
			out["reasoning_effort"] = effort
		}
	} else if effort := strings.TrimSpace(stringField(body, "reasoning_effort")); effort != "" {
		out["reasoning_effort"] = effort
	}

	// tools: 完整兼容 Responses 扁平 function、Codex custom (如 apply_patch)、local_shell/shell 与任意命名客户端工具
	tools, customTools := convertResponsesToolsWithMeta(body["tools"])
	if len(tools) > 0 {
		out["tools"] = tools
	}

	// tool_choice: 兼容字符串、{type:"function", name:"..."}、{type:"custom", name:"..."} 及 {type:"local_shell"}
	if tc := convertResponsesToolChoice(body["tool_choice"]); tc != nil {
		out["tool_choice"] = tc
	}

	messages := convertResponsesInputToMessages(body["instructions"], body["input"])
	msgAny := make([]any, 0, len(messages))
	for _, m := range messages {
		msgAny = append(msgAny, m)
	}
	out["messages"] = msgAny

	// 透传会话线索供 sessionHints 提取
	for _, k := range []string{"session_id", "x-session-id", "x-claude-code-session-id", "previous_response_id"} {
		if v := stringField(body, k); v != "" {
			if k == "previous_response_id" && out["session_id"] == nil {
				out["session_id"] = v
			} else {
				out[k] = v
			}
		}
	}

	return out, customTools, nil
}

func convertResponsesInputToMessages(instructionsRaw any, inputRaw any) []map[string]any {
	messages := make([]map[string]any, 0, 8)

	if inst, ok := instructionsRaw.(string); ok && strings.TrimSpace(inst) != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": inst,
		})
	}

	// 暂存连续的 assistant 输出项（reasoning / assistant message / function_call），
	// 合并为一条符合 OpenAI Chat 语义的 assistant 消息，避免工具调用和思考字段分裂。
	var pendingAssistant map[string]any
	flushAssistant := func() {
		if pendingAssistant == nil {
			return
		}
		if _, hasContent := pendingAssistant["content"]; !hasContent {
			pendingAssistant["content"] = ""
		}
		messages = append(messages, pendingAssistant)
		pendingAssistant = nil
	}
	ensurePendingAssistant := func() map[string]any {
		if pendingAssistant == nil {
			pendingAssistant = map[string]any{"role": "assistant"}
		}
		return pendingAssistant
	}

	switch inp := inputRaw.(type) {
	case string:
		if inp != "" || len(messages) == 0 {
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": inp,
			})
		}
	case []any:
		for i, rawItem := range inp {
			item, ok := rawItem.(map[string]any)
			if !ok {
				if s, isStr := rawItem.(string); isStr && s != "" {
					flushAssistant()
					messages = append(messages, map[string]any{
						"role":    "user",
						"content": s,
					})
				}
				continue
			}

			itemType := strings.ToLower(strings.TrimSpace(stringField(item, "type")))
			role := strings.ToLower(strings.TrimSpace(stringField(item, "role")))

			switch {
			case itemType == "input_text":
				flushAssistant()
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": stringField(item, "text"),
				})

			case itemType == "output_text":
				pa := ensurePendingAssistant()
				appendAssistantContent(pa, stringField(item, "text"))

			case itemType == "reasoning":
				rc := extractReasoningItemText(item)
				if rc != "" {
					pa := ensurePendingAssistant()
					if existing := stringField(pa, "reasoning_content"); existing != "" {
						pa["reasoning_content"] = existing + "\n" + rc
					} else {
						pa["reasoning_content"] = rc
					}
				}

			case itemType == "function_call":
				callID := firstNonEmptyStr(stringField(item, "call_id"), stringField(item, "id"))
				if callID == "" {
					callID = "call_" + strconv.Itoa(i)
				}
				name := stringField(item, "name")
				args := stringifyJSONField(item["arguments"], "{}")
				pa := ensurePendingAssistant()
				tcs, _ := pa["tool_calls"].([]any)
				tcs = append(tcs, map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": args,
					},
				})
				pa["tool_calls"] = tcs

			case itemType == "custom_tool_call":
				callID := firstNonEmptyStr(stringField(item, "call_id"), stringField(item, "id"))
				if callID == "" {
					callID = "call_" + strconv.Itoa(i)
				}
				name := firstNonEmptyStr(stringField(item, "name"), "custom_tool")
				args := wrapCustomToolInputAsJSON(item["input"])
				pa := ensurePendingAssistant()
				tcs, _ := pa["tool_calls"].([]any)
				tcs = append(tcs, map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": args,
					},
				})
				pa["tool_calls"] = tcs

			case itemType == "local_shell_call" || itemType == "shell_call":
				callID := firstNonEmptyStr(stringField(item, "call_id"), stringField(item, "id"))
				if callID == "" {
					callID = "call_" + strconv.Itoa(i)
				}
				name := firstNonEmptyStr(stringField(item, "name"), strings.TrimSuffix(itemType, "_call"))
				var rawArgs any = item["action"]
				if rawArgs == nil {
					rawArgs = item["arguments"]
				}
				args := stringifyJSONField(rawArgs, "{}")
				pa := ensurePendingAssistant()
				tcs, _ := pa["tool_calls"].([]any)
				tcs = append(tcs, map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": args,
					},
				})
				pa["tool_calls"] = tcs

			case itemType == "function_call_output" || itemType == "custom_tool_call_output" || itemType == "local_shell_call_output" || itemType == "shell_call_output":
				flushAssistant()
				callID := firstNonEmptyStr(stringField(item, "call_id"), stringField(item, "id"))
				outputContent := extractFunctionCallOutput(item["output"])
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": callID,
					"content":      outputContent,
				})

			case itemType == "message" || role != "":
				if role == "" {
					role = "user"
				}
				if role == "developer" {
					role = "system"
				}
				content := extractResponsesMessageContent(item["content"])
				if role == "assistant" {
					pa := ensurePendingAssistant()
					if str, ok := content.(string); ok {
						appendAssistantContent(pa, str)
					} else if content != nil {
						pa["content"] = content
					}
				} else {
					flushAssistant()
					messages = append(messages, map[string]any{
						"role":    role,
						"content": content,
					})
				}
			}
		}
		flushAssistant()
	}

	return messages
}

func appendAssistantContent(pa map[string]any, text string) {
	if text == "" {
		if _, ok := pa["content"]; !ok {
			pa["content"] = ""
		}
		return
	}
	if prev, ok := pa["content"].(string); ok && prev != "" {
		pa["content"] = prev + text
	} else {
		pa["content"] = text
	}
}

func extractReasoningItemText(item map[string]any) string {
	var parts []string
	if summary, ok := item["summary"].([]any); ok {
		for _, sRaw := range summary {
			if sMap, ok := sRaw.(map[string]any); ok {
				if t := firstNonEmptyStr(stringField(sMap, "text"), stringField(sMap, "thinking")); t != "" {
					parts = append(parts, t)
				}
			} else if sStr, ok := sRaw.(string); ok && sStr != "" {
				parts = append(parts, sStr)
			}
		}
	}
	if len(parts) == 0 {
		if c, ok := item["content"].([]any); ok {
			for _, cRaw := range c {
				if cMap, ok := cRaw.(map[string]any); ok {
					if t := firstNonEmptyStr(stringField(cMap, "text"), stringField(cMap, "thinking")); t != "" {
						parts = append(parts, t)
					}
				}
			}
		} else if cStr, ok := item["content"].(string); ok && cStr != "" {
			parts = append(parts, cStr)
		} else if t := stringField(item, "text"); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

func extractResponsesMessageContent(raw any) any {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var textParts []string
		var multiParts []any
		hasNonText := false
		for _, pRaw := range v {
			p, ok := pRaw.(map[string]any)
			if !ok {
				if s, isStr := pRaw.(string); isStr {
					textParts = append(textParts, s)
					multiParts = append(multiParts, map[string]any{"type": "text", "text": s})
				}
				continue
			}
			pt := strings.ToLower(strings.TrimSpace(stringField(p, "type")))
			switch pt {
			case "input_text", "output_text", "text", "":
				txt := stringField(p, "text")
				textParts = append(textParts, txt)
				multiParts = append(multiParts, map[string]any{"type": "text", "text": txt})
			case "refusal":
				txt := firstNonEmptyStr(stringField(p, "refusal"), stringField(p, "text"))
				textParts = append(textParts, txt)
				multiParts = append(multiParts, map[string]any{"type": "text", "text": txt})
			case "input_image":
				hasNonText = true
				imgObj := map[string]any{}
				switch iu := p["image_url"].(type) {
				case string:
					imgObj["url"] = iu
				case map[string]any:
					imgObj = cloneJSONMap(iu)
				}
				if d := stringField(p, "detail"); d != "" && imgObj["detail"] == nil {
					imgObj["detail"] = d
				}
				multiParts = append(multiParts, map[string]any{
					"type":      "image_url",
					"image_url": imgObj,
				})
			case "image_url":
				hasNonText = true
				multiParts = append(multiParts, cloneJSONMap(p))
			}
		}
		if hasNonText {
			return multiParts
		}
		return strings.Join(textParts, "")
	case nil:
		return ""
	default:
		return stringifyJSONField(v, "")
	}
}

func extractFunctionCallOutput(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var textParts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if txt := firstNonEmptyStr(stringField(m, "text"), stringField(m, "output")); txt != "" {
					textParts = append(textParts, txt)
					continue
				}
			}
			b, err := json.Marshal(item)
			if err == nil {
				textParts = append(textParts, string(b))
			}
		}
		return strings.Join(textParts, "\n")
	default:
		return stringifyJSONField(v, "")
	}
}

func stringifyJSONField(v any, fallback string) string {
	switch t := v.(type) {
	case nil:
		return fallback
	case string:
		if strings.TrimSpace(t) == "" {
			return fallback
		}
		return t
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fallback
		}
		return string(b)
	}
}

func wrapCustomToolInputAsJSON(raw any) string {
	switch v := raw.(type) {
	case nil:
		return "{}"
	case string:
		b, err := json.Marshal(map[string]any{"input": v})
		if err != nil {
			return "{}"
		}
		return string(b)
	default:
		return stringifyJSONField(v, "{}")
	}
}

func convertResponsesTools(raw any) []any {
	tools, _ := convertResponsesToolsWithMeta(raw)
	return tools
}

// convertResponsesToolsWithMeta 完整透传全部 Responses API 工具类型（脱离于任何特定上游，绝不丢弃下游工具）：
//   - function（扁平或嵌套）：原样转 OpenAI function
//   - custom（如 Codex apply_patch 自由文本工具）：完整保留名称、描述、format 提示与 input 参数并登记到 customTools
//   - local_shell / shell（如 Codex 本地终端工具）：完整保留命令/工作目录/超时参数结构
//   - 其它任意客户端或扩展工具类型（mcp / computer_use / bash_* / web_search* / 未来新类型）：均按名称/类型无损透传
func convertResponsesToolsWithMeta(raw any) ([]any, map[string]bool) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, nil
	}
	out := make([]any, 0, len(items))
	var customTools map[string]bool

	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(stringField(m, "type")))

		// 1. 嵌套标准 OpenAI function
		if fnMap, ok := m["function"].(map[string]any); ok && fnMap != nil {
			out = append(out, map[string]any{
				"type":     "function",
				"function": cloneJSONMap(fnMap),
			})
			continue
		}

		// 2. Codex / Responses `custom` 自由文本工具（如 apply_patch）
		if t == "custom" {
			name := strings.TrimSpace(stringField(m, "name"))
			if name == "" {
				name = "custom_tool"
			}
			if customTools == nil {
				customTools = make(map[string]bool)
			}
			customTools[name] = true

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
			fn := map[string]any{
				"name": name,
			}
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
			out = append(out, map[string]any{
				"type":     "function",
				"function": fn,
			})
			continue
		}

		// 3. Codex `local_shell` / `shell`
		if t == "local_shell" || t == "shell" {
			name := firstNonEmptyStr(stringField(m, "name"), t)
			desc := firstNonEmptyStr(stringField(m, "description"), "Execute a shell command in the caller's environment")
			fn := map[string]any{
				"name":        name,
				"description": desc,
			}
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
						"working_directory": map[string]any{
							"type":        "string",
							"description": "Optional working directory",
						},
						"timeout_ms": map[string]any{
							"type":        "integer",
							"description": "Optional timeout in milliseconds",
						},
					},
					"required": []any{"command"},
				}
			}
			out = append(out, map[string]any{
				"type":     "function",
				"function": fn,
			})
			continue
		}

		// 4. 扁平 function 或任意其它工具类型（绝不丢弃任何下游工具）
		name := firstNonEmptyStr(stringField(m, "name"), stringField(m, "server_label"), t)
		if name == "" {
			continue
		}
		fn := map[string]any{
			"name": name,
		}
		if desc := stringField(m, "description"); desc != "" {
			fn["description"] = desc
		} else if t != "" && t != "function" {
			fn["description"] = "Caller tool of type " + t
		}
		if params, ok := m["parameters"]; ok && params != nil {
			fn["parameters"] = params
		} else if schema, ok := m["input_schema"]; ok && schema != nil {
			fn["parameters"] = schema
		} else {
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type":     "function",
			"function": fn,
		})
	}
	return out, customTools
}

func convertResponsesToolChoice(raw any) any {
	switch v := raw.(type) {
	case nil:
		return nil
	case string:
		return v
	case map[string]any:
		t := strings.ToLower(strings.TrimSpace(stringField(v, "type")))
		switch t {
		case "auto", "none", "required":
			return t
		case "any":
			return "required"
		case "function", "custom", "tool":
			if fn, ok := v["function"].(map[string]any); ok {
				if name := stringField(fn, "name"); name != "" {
					return map[string]any{
						"type":     "function",
						"function": map[string]any{"name": name},
					}
				}
			}
			if name := stringField(v, "name"); name != "" {
				return map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		default:
			if name := firstNonEmptyStr(stringField(v, "name"), t); name != "" {
				return map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		}
	}
	return nil
}
