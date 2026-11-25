package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"cmd2api/internal/toolobf"
)

// obfuscateNormalizedRequest 在请求发送至上游前，按分组覆盖策略与目标平台配置对工具定义、tool_choice
// 以及多轮对话历史里的 tool_calls / tool_use / tool_result 名称执行确定性、无损双射混淆。
//
// 每次都会对 normalizedRequest 做深拷贝，从而保证：
//  1. 同一请求的多个上游账号重试（429/5xx 故障切换）绝不会重复二次混淆；
//  2. 各平台拿到各自的混淆会话 Session，用于精准还原上游流式/非流式响应中的工具名。
func obfuscateNormalizedRequest(engine *toolobf.Engine, groupID int64, platform string, src *normalizedRequest) (*normalizedRequest, *toolobf.Session) {
	if engine == nil || src == nil || !engine.EnabledForGroupPlatform(groupID, platform) {
		return src, nil
	}
	sess := engine.NewSessionForGroup(groupID, platform)
	if sess == nil {
		return src, nil
	}

	dst := cloneNormalizedRequestDeep(src)

	// Pass 0: 先扫描全部工具声明，为需要原样透传（excluded / 非敏感）的工具预占保留名，
	// 防止随后生成的混淆别名与原样透传的工具意外碰撞。
	registerPassthroughTools(sess, dst.Tools)
	if dst.RawOpenAIBody != nil {
		registerPassthroughFromAny(sess, dst.RawOpenAIBody["tools"])
		registerPassthroughFromAny(sess, dst.RawOpenAIBody["functions"])
	}

	// Pass 1: 混淆工具声明列表（dst.Tools + RawOpenAIBody.tools + RawOpenAIBody.functions）。
	obfuscateToolDefsSlice(sess, dst.Tools)
	if dst.RawOpenAIBody != nil {
		obfuscateToolDefsAny(sess, dst.RawOpenAIBody["tools"])
		obfuscateToolDefsAny(sess, dst.RawOpenAIBody["functions"])
	}

	// Pass 2: 混淆 tool_choice / function_call 及多轮消息历史中的所有工具名引用。
	dst.ToolChoice = obfuscateToolChoiceValue(sess, dst.ToolChoice)
	obfuscateMessagesSlice(sess, dst.Messages)
	if dst.RawOpenAIBody != nil {
		if tc, exists := dst.RawOpenAIBody["tool_choice"]; exists && tc != nil {
			dst.RawOpenAIBody["tool_choice"] = obfuscateToolChoiceValue(sess, tc)
		}
		if fc, exists := dst.RawOpenAIBody["function_call"]; exists && fc != nil {
			dst.RawOpenAIBody["function_call"] = obfuscateFunctionCallValue(sess, fc)
		}
		obfuscateMessagesAny(sess, dst.RawOpenAIBody["messages"])
	}

	// Pass 3: 若开启 RewriteToolRefs，同步重写工具 description 中反引号包裹的精确工具名引用。
	if sess.Config().RewriteToolRefs && sess.Count() > 0 {
		rewriteToolDefsDescriptions(sess, dst.Tools)
		if dst.RawOpenAIBody != nil {
			rewriteToolDefsDescriptionsAny(sess, dst.RawOpenAIBody["tools"])
			rewriteToolDefsDescriptionsAny(sess, dst.RawOpenAIBody["functions"])
		}
	}

	// 无任何工具名发生重写时，直接返回原始请求且不挂接 Session，避免额外开销。
	if sess.Count() == 0 {
		return src, nil
	}

	// 同步 dst.Messages / dst.Tools / dst.ToolChoice 回 RawOpenAIBody，确保所有平台发包口径一致。
	syncObfuscatedFieldsToRawBody(dst)
	return dst, sess
}

func registerPassthroughTools(sess *toolobf.Session, tools []map[string]any) {
	cfg := sess.Config()
	exclSet := make(map[string]struct{}, len(cfg.ExcludeTools))
	for _, t := range cfg.ExcludeTools {
		exclSet[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
	}
	for _, t := range tools {
		name := extractToolDefName(t)
		if name == "" {
			continue
		}
		if isToolPassthroughByConfig(cfg, exclSet, name) {
			sess.RegisterPassthrough(name)
		}
	}
}

func registerPassthroughFromAny(sess *toolobf.Session, raw any) {
	items, ok := raw.([]any)
	if !ok {
		return
	}
	cfg := sess.Config()
	exclSet := make(map[string]struct{}, len(cfg.ExcludeTools))
	for _, t := range cfg.ExcludeTools {
		exclSet[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
	}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := extractToolDefName(m)
		if name == "" {
			continue
		}
		if isToolPassthroughByConfig(cfg, exclSet, name) {
			sess.RegisterPassthrough(name)
		}
	}
}

func isToolPassthroughByConfig(cfg toolobf.Config, exclLower map[string]struct{}, name string) bool {
	if _, ok := exclLower[strings.ToLower(strings.TrimSpace(name))]; ok {
		return true
	}
	if cfg.Scope == toolobf.ScopeSensitiveOnly {
		if _, custom := cfg.CustomMap[name]; !custom && !toolobf.IsSensitiveAgentTool(name) {
			return true
		}
	}
	return false
}

func extractToolDefName(m map[string]any) string {
	if m == nil {
		return ""
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if n, _ := fn["name"].(string); n != "" {
			return n
		}
	}
	if n, _ := m["name"].(string); n != "" {
		return n
	}
	if n, _ := m["server_label"].(string); n != "" {
		return n
	}
	if t, _ := m["type"].(string); t != "" && !strings.EqualFold(strings.TrimSpace(t), "function") {
		return strings.TrimSpace(t)
	}
	return ""
}

func obfuscateToolDefsSlice(sess *toolobf.Session, tools []map[string]any) {
	for _, t := range tools {
		obfuscateSingleToolDef(sess, t)
	}
}

func obfuscateToolDefsAny(sess *toolobf.Session, raw any) {
	items, ok := raw.([]any)
	if !ok {
		return
	}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			obfuscateSingleToolDef(sess, m)
		}
	}
}

func obfuscateSingleToolDef(sess *toolobf.Session, m map[string]any) {
	if m == nil {
		return
	}
	obfuscated := false
	if fn, ok := m["function"].(map[string]any); ok {
		if name, _ := fn["name"].(string); name != "" {
			fn["name"] = sess.Obfuscate(name)
			obfuscated = true
		}
	}
	if name, _ := m["name"].(string); name != "" {
		m["name"] = sess.Obfuscate(name)
		obfuscated = true
	}
	if !obfuscated {
		if sl, _ := m["server_label"].(string); sl != "" {
			m["server_label"] = sess.Obfuscate(sl)
			return
		}
		if t, _ := m["type"].(string); t != "" && !strings.EqualFold(strings.TrimSpace(t), "function") {
			// 无 name 的特殊工具（如裸 {"type":"local_shell"}）：升级为标准 function 形态并混淆工具名
			obfName := sess.Obfuscate(strings.TrimSpace(t))
			m["type"] = "function"
			m["function"] = map[string]any{
				"name":        obfName,
				"description": "Caller tool (" + strings.TrimSpace(t) + ")",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			}
		}
	}
}

func rewriteToolDefsDescriptions(sess *toolobf.Session, tools []map[string]any) {
	for _, t := range tools {
		rewriteSingleToolDefDescription(sess, t)
	}
}

func rewriteToolDefsDescriptionsAny(sess *toolobf.Session, raw any) {
	items, ok := raw.([]any)
	if !ok {
		return
	}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			rewriteSingleToolDefDescription(sess, m)
		}
	}
}

func rewriteSingleToolDefDescription(sess *toolobf.Session, m map[string]any) {
	if m == nil {
		return
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if desc, _ := fn["description"].(string); desc != "" {
			fn["description"] = sess.RewriteBacktickRefs(desc)
		}
	}
	if desc, _ := m["description"].(string); desc != "" {
		m["description"] = sess.RewriteBacktickRefs(desc)
	}
}

func obfuscateToolChoiceValue(sess *toolobf.Session, raw any) any {
	switch v := raw.(type) {
	case nil:
		return nil
	case string:
		lower := strings.ToLower(strings.TrimSpace(v))
		switch lower {
		case "", "auto", "none", "required", "any":
			return v
		default:
			return sess.Obfuscate(v)
		}
	case map[string]any:
		out := deepCloneMap(v)
		if fn, ok := out["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				fn["name"] = sess.Obfuscate(name)
			}
		}
		if name, _ := out["name"].(string); name != "" {
			tLower := strings.ToLower(strings.TrimSpace(stringField(out, "type")))
			if tLower == "" || tLower == "function" || tLower == "tool" || tLower == "custom" {
				out["name"] = sess.Obfuscate(name)
			}
		} else if tLower := strings.ToLower(strings.TrimSpace(stringField(out, "type"))); tLower != "" &&
			tLower != "auto" && tLower != "none" && tLower != "required" && tLower != "any" && tLower != "function" {
			// 例如 {"type":"local_shell"} / {"type":"web_search_preview"}
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": sess.Obfuscate(tLower)},
			}
		}
		return out
	default:
		return raw
	}
}

func obfuscateFunctionCallValue(sess *toolobf.Session, raw any) any {
	switch v := raw.(type) {
	case map[string]any:
		out := deepCloneMap(v)
		if name, _ := out["name"].(string); name != "" {
			out["name"] = sess.Obfuscate(name)
		}
		return out
	default:
		return raw
	}
}

func obfuscateMessagesSlice(sess *toolobf.Session, msgs []map[string]any) {
	for _, m := range msgs {
		obfuscateSingleMessage(sess, m)
	}
}

func obfuscateMessagesAny(sess *toolobf.Session, raw any) {
	items, ok := raw.([]any)
	if !ok {
		return
	}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			obfuscateSingleMessage(sess, m)
		}
	}
}

func obfuscateSingleMessage(sess *toolobf.Session, msg map[string]any) {
	if msg == nil {
		return
	}
	role := strings.ToLower(strings.TrimSpace(stringField(msg, "role")))
	switch role {
	case "assistant":
		if calls, ok := msg["tool_calls"].([]any); ok {
			for _, cRaw := range calls {
				c, ok := cRaw.(map[string]any)
				if !ok {
					continue
				}
				if fn, ok := c["function"].(map[string]any); ok {
					if name, _ := fn["name"].(string); name != "" {
						fn["name"] = sess.Obfuscate(name)
					}
				}
				if name, _ := c["name"].(string); name != "" {
					c["name"] = sess.Obfuscate(name)
				}
			}
		}
		if fc, ok := msg["function_call"].(map[string]any); ok {
			if name, _ := fc["name"].(string); name != "" {
				fc["name"] = sess.Obfuscate(name)
			}
		}
		if contentBlocks, ok := msg["content"].([]any); ok {
			for _, bRaw := range contentBlocks {
				blk, ok := bRaw.(map[string]any)
				if !ok {
					continue
				}
				bt := strings.ToLower(strings.TrimSpace(stringField(blk, "type")))
				if bt == "tool_use" || bt == "server_tool_use" || bt == "custom_tool_call" || bt == "local_shell_call" {
					if name, _ := blk["name"].(string); name != "" {
						blk["name"] = sess.Obfuscate(name)
					}
				}
			}
		}
	case "tool", "function":
		if name, _ := msg["name"].(string); name != "" {
			msg["name"] = sess.Obfuscate(name)
		}
	}

	if sess.Config().RewriteToolRefs {
		switch content := msg["content"].(type) {
		case string:
			if content != "" {
				msg["content"] = sess.RewriteBacktickRefs(content)
			}
		case []any:
			for _, bRaw := range content {
				blk, ok := bRaw.(map[string]any)
				if !ok {
					continue
				}
				bt := strings.ToLower(strings.TrimSpace(stringField(blk, "type")))
				if bt == "text" || bt == "input_text" || bt == "" {
					if txt, ok := blk["text"].(string); ok && txt != "" {
						blk["text"] = sess.RewriteBacktickRefs(txt)
					}
				}
			}
		}
	}
}

func syncObfuscatedFieldsToRawBody(dst *normalizedRequest) {
	if dst == nil || dst.RawOpenAIBody == nil {
		return
	}
	if dst.Messages != nil {
		msgAny := make([]any, 0, len(dst.Messages))
		for _, m := range dst.Messages {
			msgAny = append(msgAny, m)
		}
		dst.RawOpenAIBody["messages"] = msgAny
	}
	if dst.Tools != nil {
		toolsAny := make([]any, 0, len(dst.Tools))
		for _, t := range dst.Tools {
			toolsAny = append(toolsAny, t)
		}
		dst.RawOpenAIBody["tools"] = toolsAny
	}
	if dst.ToolChoice != nil {
		dst.RawOpenAIBody["tool_choice"] = dst.ToolChoice
	}
}

func cloneNormalizedRequestDeep(src *normalizedRequest) *normalizedRequest {
	if src == nil {
		return nil
	}
	cp := *src
	cp.Messages = deepCloneMapSlice(src.Messages)
	cp.Tools = deepCloneMapSlice(src.Tools)
	cp.ToolChoice = deepCloneValue(src.ToolChoice)
	if len(src.SessionHints) > 0 {
		cp.SessionHints = append([]string(nil), src.SessionHints...)
	}
	if src.RawOpenAIBody != nil {
		cp.RawOpenAIBody = deepCloneMap(src.RawOpenAIBody)
	}
	if len(src.ResponsesCustomTools) > 0 {
		cp.ResponsesCustomTools = make(map[string]bool, len(src.ResponsesCustomTools))
		for k, v := range src.ResponsesCustomTools {
			cp.ResponsesCustomTools[k] = v
		}
	}
	return &cp
}

func deepCloneMapSlice(in []map[string]any) []map[string]any {
	if in == nil {
		return nil
	}
	out := make([]map[string]any, len(in))
	for i, m := range in {
		out[i] = deepCloneMap(m)
	}
	return out
}

func deepCloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCloneValue(v)
	}
	return out
}

func deepCloneValue(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		return deepCloneMap(tv)
	case []map[string]any:
		return deepCloneMapSlice(tv)
	case []any:
		out := make([]any, len(tv))
		for i, elem := range tv {
			out[i] = deepCloneValue(elem)
		}
		return out
	default:
		return v
	}
}

// ObfuscatePayloadForPreview 按实际网关入站解析→混淆路径，将给定协议与分组策略的完整请求 JSON
// 执行工具名混淆并返回美化后的混淆 JSON 与双向映射明细（供控制台演练预览使用）。
func ObfuscatePayloadForPreview(engine *toolobf.Engine, groupID int64, protocol string, raw []byte) ([]byte, []toolobf.MappingEntry, error) {
	if engine == nil {
		return nil, nil, fmt.Errorf("工具名混淆引擎未初始化")
	}
	proto := strings.ToLower(strings.TrimSpace(protocol))
	var (
		norm *normalizedRequest
		err  error
	)
	switch proto {
	case string(ProtocolAnthropic), "messages":
		norm, err = ParseAnthropicMessagesRequest(raw)
	case string(ProtocolResponses):
		norm, err = ParseResponsesRequest(raw)
	default:
		norm, err = ParseOpenAIChatRequest(raw)
	}
	if err != nil {
		return nil, nil, err
	}

	obfNorm, sess := obfuscateNormalizedRequest(engine, groupID, "", norm)
	var entries []toolobf.MappingEntry
	if sess != nil {
		entries = sess.Entries()
	}

	bodyMap := buildWorkBuddyBaseBody(obfNorm)
	compact, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact, "", "  "); err != nil {
		return compact, entries, nil
	}
	return pretty.Bytes(), entries, nil
}
