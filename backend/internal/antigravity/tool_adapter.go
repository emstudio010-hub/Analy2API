package antigravity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	// 自适应外部工具渲染层级（面向 dsh / codex / Claude Code / MCP 等差异巨大的下游客户端）
	ToolTierNone      = "none"      // 0 工具或 tool_choice=none：零工具开销，实时逐字流式透传
	ToolTierFull      = "full"      // <= 12 工具且 schema <= 16KB（如 dsh / codex）：完整 JSON Schema + 描述
	ToolTierCompact   = "compact"   // 13..35 工具或 16..48KB（如中型 Agent + 少量 MCP）：精简描述 + 完整参数结构
	ToolTierSignature = "signature" // > 35 工具或 > 48KB（如 Claude Code + 大量 MCP）：高密度类型签名表

	fullTierMaxTools    = 12
	fullTierMaxBytes    = 16 * 1024
	compactTierMaxTools = 35
	compactTierMaxBytes = 48 * 1024
)

// AdaptedTool 表示归一化后的单条下游外部工具定义（兼容 OpenAI function / Responses flat / Codex custom & local_shell / Anthropic）。
type AdaptedTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	RawType     string         `json:"raw_type,omitempty"`
}

// AdaptedToolsSpec 描述经自适应分级处理后的下游外部工具集与调用约束。
type AdaptedToolsSpec struct {
	Tier            string
	ToolChoiceMode  string // "auto" | "none" | "required" | "specific"
	ForcedToolName  string
	Tools           []AdaptedTool
	DeclaredByLower map[string]string
	PromptBlock     string
	RawSchemaBytes  int
	RenderedBytes   int
}

// HasTools 返回当前请求是否需要开启外部工具调用解析。
func (s AdaptedToolsSpec) HasTools() bool {
	return s.Tier != "" && s.Tier != ToolTierNone && len(s.Tools) > 0
}

// ResolveToolName 将模型输出的 tool name 对齐回下游声明的精确工具名；
// 若下游声明了工具清单且模型幻觉调用了不在清单中的名称（例如 agy 内置 IDE 工具），返回 ("", false)。
func (s AdaptedToolsSpec) ResolveToolName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", false
	}
	if len(s.DeclaredByLower) == 0 {
		return name, true
	}
	if exact, ok := s.DeclaredByLower[strings.ToLower(name)]; ok {
		return exact, true
	}
	return "", false
}

// ExtractedToolCall 是从 agy 输出中解析并校验通过的一条外部工具调用。
type ExtractedToolCall struct {
	Name      string
	Arguments string // 合法 JSON 对象字符串
}

// AdaptToolsForAgyCLI 针对任意下游客户端（0 工具、dsh/codex 精简工具、或 60~150 个 MCP 工具）自动选择最优工具提示渲染层级，
// 并精准尊重 tool_choice（auto / none / required / specific）。
func AdaptToolsForAgyCLI(rawTools []any, toolChoice any) AdaptedToolsSpec {
	spec := AdaptedToolsSpec{
		Tier:            ToolTierNone,
		ToolChoiceMode:  "auto",
		DeclaredByLower: make(map[string]string),
	}

	// 1. 解析并归一化所有工具（支持 OpenAI function / Responses flat / Codex custom & local_shell / Anthropic）
	seen := make(map[string]struct{}, len(rawTools))
	for _, item := range rawTools {
		tool, ok := normalizeSingleToolForCLI(item)
		if !ok || tool.Name == "" {
			continue
		}
		lower := strings.ToLower(tool.Name)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		spec.Tools = append(spec.Tools, tool)
		spec.DeclaredByLower[lower] = tool.Name
	}

	// 2. 解析 tool_choice（注意 NormalizeCompat 可能会把指定工具对象折叠为纯函数名字符串）
	mode, forced := parseToolChoiceForCLI(toolChoice, spec.DeclaredByLower)
	spec.ToolChoiceMode = mode
	spec.ForcedToolName = forced

	if mode == "none" || len(spec.Tools) == 0 {
		spec.Tier = ToolTierNone
		return spec
	}

	// 3. 若 tool_choice 强制指定单一工具，仅聚焦渲染该工具的完整 Schema，节省海量上下文并提高准确率
	renderTools := spec.Tools
	if mode == "specific" && forced != "" {
		for _, t := range spec.Tools {
			if strings.EqualFold(t.Name, forced) {
				renderTools = []AdaptedTool{t}
				break
			}
		}
	}

	// 4. 计算完整 JSON Schema 体积并自动判定渲染层级
	rawBytes := estimateAdaptedToolsBytes(renderTools)
	spec.RawSchemaBytes = rawBytes

	switch {
	case len(renderTools) <= fullTierMaxTools && rawBytes <= fullTierMaxBytes:
		spec.Tier = ToolTierFull
	case len(renderTools) <= compactTierMaxTools && rawBytes <= compactTierMaxBytes:
		spec.Tier = ToolTierCompact
	default:
		spec.Tier = ToolTierSignature
	}

	// 5. 渲染客户端中立的外部工具声明块
	spec.PromptBlock = renderAdaptedToolsPromptBlock(spec.Tier, spec.ToolChoiceMode, spec.ForcedToolName, renderTools)
	spec.RenderedBytes = len(spec.PromptBlock)
	return spec
}

func normalizeSingleToolForCLI(raw any) (AdaptedTool, bool) {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return AdaptedTool{}, false
	}

	rawType := strings.ToLower(strings.TrimSpace(fmt.Sprint(m["type"])))
	if rawType == "<nil>" {
		rawType = ""
	}

	// A. 标准 OpenAI {"type":"function", "function": {...}}
	if fn, ok := m["function"].(map[string]any); ok && fn != nil {
		name := strings.TrimSpace(stringFromAny(fn["name"]))
		if name == "" {
			return AdaptedTool{}, false
		}
		var params map[string]any
		if p, ok := fn["parameters"].(map[string]any); ok && p != nil {
			params = CleanJSONSchema(p)
		} else {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		return AdaptedTool{
			Name:        name,
			Description: strings.TrimSpace(stringFromAny(fn["description"])),
			Parameters:  params,
			RawType:     "function",
		}, true
	}

	// B. Codex / Responses `custom` 自由文本工具（如 apply_patch）
	if rawType == "custom" {
		name := strings.TrimSpace(stringFromAny(m["name"]))
		if name == "" {
			return AdaptedTool{}, false
		}
		desc := strings.TrimSpace(stringFromAny(m["description"]))
		var params map[string]any
		if p, ok := m["parameters"].(map[string]any); ok && p != nil {
			params = CleanJSONSchema(p)
		} else {
			params = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{
						"type":        "string",
						"description": "Raw text/patch payload for this custom tool",
					},
				},
				"required": []any{"input"},
			}
		}
		return AdaptedTool{
			Name:        name,
			Description: desc,
			Parameters:  params,
			RawType:     "custom",
		}, true
	}

	// C. Codex `local_shell` / `shell` 工具
	if rawType == "local_shell" || rawType == "shell" {
		name := strings.TrimSpace(stringFromAny(m["name"]))
		if name == "" {
			name = rawType
		}
		desc := strings.TrimSpace(stringFromAny(m["description"]))
		if desc == "" {
			desc = "Execute a shell command in the caller's environment"
		}
		var params map[string]any
		if p, ok := m["parameters"].(map[string]any); ok && p != nil {
			params = CleanJSONSchema(p)
		} else {
			params = map[string]any{
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
						"description": "Optional execution timeout in milliseconds",
					},
				},
				"required": []any{"command"},
			}
		}
		return AdaptedTool{
			Name:        name,
			Description: desc,
			Parameters:  params,
			RawType:     rawType,
		}, true
	}

	// D. 扁平结构（OpenAI Responses function、Anthropic input_schema 或任意其它客户端工具类型——绝不丢弃任何下游工具）
	name := firstNonEmptyStr(stringFromAny(m["name"]), stringFromAny(m["server_label"]), rawType)
	if name == "" {
		return AdaptedTool{}, false
	}
	var params map[string]any
	if p, ok := m["parameters"].(map[string]any); ok && p != nil {
		params = CleanJSONSchema(p)
	} else if p, ok := m["input_schema"].(map[string]any); ok && p != nil {
		params = CleanJSONSchema(p)
	} else {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if rawType == "" {
		rawType = "function"
	}
	desc := strings.TrimSpace(stringFromAny(m["description"]))
	if desc == "" && rawType != "function" {
		desc = "External client tool (" + rawType + ")"
	}
	return AdaptedTool{
		Name:        name,
		Description: desc,
		Parameters:  params,
		RawType:     rawType,
	}, true
}

func parseToolChoiceForCLI(raw any, declaredByLower map[string]string) (mode string, forced string) {
	if raw == nil {
		return "auto", ""
	}
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		switch strings.ToLower(s) {
		case "", "auto":
			return "auto", ""
		case "none":
			return "none", ""
		case "required", "any":
			return "required", ""
		default:
			if exact, ok := declaredByLower[strings.ToLower(s)]; ok {
				return "specific", exact
			}
			return "specific", s
		}
	case map[string]any:
		t := strings.ToLower(strings.TrimSpace(stringFromAny(v["type"])))
		switch t {
		case "none":
			return "none", ""
		case "auto":
			return "auto", ""
		case "required", "any":
			return "required", ""
		case "function", "tool", "custom":
			var name string
			if fn, ok := v["function"].(map[string]any); ok && fn != nil {
				name = strings.TrimSpace(stringFromAny(fn["name"]))
			}
			if name == "" {
				name = strings.TrimSpace(stringFromAny(v["name"]))
			}
			if name != "" {
				if exact, ok := declaredByLower[strings.ToLower(name)]; ok {
					return "specific", exact
				}
				return "specific", name
			}
		case "local_shell", "shell":
			if exact, ok := declaredByLower[t]; ok {
				return "specific", exact
			}
			return "specific", t
		}
	}
	return "auto", ""
}

func estimateAdaptedToolsBytes(tools []AdaptedTool) int {
	b, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return len(b)
}

func renderAdaptedToolsPromptBlock(tier, choiceMode, forcedName string, tools []AdaptedTool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[Caller External Tools — count=%d tier=%s]\n", len(tools), tier))
	b.WriteString("IMPORTANT: NEVER execute your own local/built-in IDE tools (e.g., run_command, view_file, write_to_file, grep_search, browser_*). ")
	b.WriteString("You are proxying for an external API client (such as dsh, codex, Claude Code, or another agent) that executes its own tools.\n")
	b.WriteString("To invoke one or more caller tools below, emit XML blocks in this exact format:\n")
	b.WriteString("<tool_call>{\"name\":\"<exact_tool_name>\",\"arguments\":{...}}</tool_call>\n")

	switch choiceMode {
	case "required":
		b.WriteString("TOOL CHOICE REQUIREMENT: The caller REQUIRES at least one tool call. You MUST emit at least one <tool_call>...</tool_call> block using the declared tool names below.\n")
	case "specific":
		b.WriteString(fmt.Sprintf("TOOL CHOICE REQUIREMENT: The caller REQUIRES invoking tool %q. You MUST emit <tool_call>{\"name\":%q,\"arguments\":{...}}</tool_call>.\n", forcedName, forcedName))
	default:
		b.WriteString("If no tool is needed, reply normally without <tool_call> tags. You may emit multiple <tool_call> blocks for parallel calls.\n")
	}

	switch tier {
	case ToolTierFull:
		compactList := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			entry := map[string]any{
				"name":       t.Name,
				"parameters": t.Parameters,
			}
			if d := truncateCompactText(t.Description, 1200); d != "" {
				entry["description"] = d
			}
			compactList = append(compactList, entry)
		}
		j, _ := json.Marshal(compactList)
		b.WriteString("Declared tools (full schema):\n")
		b.Write(j)
		b.WriteString("\n")

	case ToolTierCompact:
		compactList := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			entry := map[string]any{
				"name":       t.Name,
				"parameters": compactToolSchema(t.Parameters, 0),
			}
			if d := truncateCompactText(t.Description, 220); d != "" {
				entry["description"] = d
			}
			compactList = append(compactList, entry)
		}
		j, _ := json.Marshal(compactList)
		b.WriteString("Declared tools (compact schema):\n")
		b.Write(j)
		b.WriteString("\n")

	default: // ToolTierSignature
		b.WriteString("Declared tools (compact signatures; `*` denotes required parameter):\n")
		for _, t := range tools {
			b.WriteString("- ")
			b.WriteString(formatToolSignature(t))
			b.WriteByte('\n')
		}
	}

	return b.String()
}

func compactToolSchema(schema map[string]any, depth int) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	out := make(map[string]any, 6)
	if t, ok := schema["type"]; ok {
		out["type"] = t
	}
	if req, ok := schema["required"]; ok {
		out["required"] = req
	}
	if en, ok := schema["enum"]; ok {
		out["enum"] = en
	}
	if depth > 0 {
		if d := truncateCompactText(stringFromAny(schema["description"]), 100); d != "" {
			out["description"] = d
		}
	}
	if depth < 3 {
		if props, ok := schema["properties"].(map[string]any); ok && len(props) > 0 {
			cp := make(map[string]any, len(props))
			for k, v := range props {
				if sub, ok := v.(map[string]any); ok {
					cp[k] = compactToolSchema(sub, depth+1)
				}
			}
			out["properties"] = cp
		}
		if items, ok := schema["items"].(map[string]any); ok && items != nil {
			out["items"] = compactToolSchema(items, depth+1)
		}
	}
	return out
}

func formatToolSignature(t AdaptedTool) string {
	reqSet := make(map[string]struct{})
	if reqRaw, ok := t.Parameters["required"].([]any); ok {
		for _, r := range reqRaw {
			if s, ok := r.(string); ok {
				reqSet[s] = struct{}{}
			}
		}
	} else if reqStrs, ok := t.Parameters["required"].([]string); ok {
		for _, s := range reqStrs {
			reqSet[s] = struct{}{}
		}
	}

	var params []string
	if props, ok := t.Parameters["properties"].(map[string]any); ok && len(props) > 0 {
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			_, ri := reqSet[keys[i]]
			_, rj := reqSet[keys[j]]
			if ri != rj {
				return ri
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			sub, _ := props[k].(map[string]any)
			typeLabel := summarizeSchemaType(sub)
			marker := "?"
			if _, isReq := reqSet[k]; isReq {
				marker = "*"
			}
			params = append(params, fmt.Sprintf("%s%s: %s", k, marker, typeLabel))
		}
	}

	sig := fmt.Sprintf("%s(%s)", t.Name, strings.Join(params, ", "))
	if desc := truncateCompactText(t.Description, 120); desc != "" {
		sig += " — " + desc
	}
	return sig
}

func summarizeSchemaType(sub map[string]any) string {
	if sub == nil {
		return "any"
	}
	if en, ok := sub["enum"].([]any); ok && len(en) > 0 && len(en) <= 6 {
		vals := make([]string, 0, len(en))
		for _, v := range en {
			vals = append(vals, fmt.Sprintf("%q", fmt.Sprint(v)))
		}
		return strings.Join(vals, "|")
	}
	t := strings.TrimSpace(stringFromAny(sub["type"]))
	if t == "array" {
		if items, ok := sub["items"].(map[string]any); ok {
			return summarizeSchemaType(items) + "[]"
		}
		return "any[]"
	}
	if t == "" {
		return "any"
	}
	return t
}

func truncateCompactText(s string, maxRunes int) string {
	cleaned := strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if cleaned == "" || maxRunes <= 0 {
		return cleaned
	}
	runes := []rune(cleaned)
	if len(runes) <= maxRunes {
		return cleaned
	}
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

var (
	xmlToolCallBlockRe    = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)
	fencedToolCallBlockRe = regexp.MustCompile("(?s)```(?:tool_call|toolcall)\\s*(\\{.*?\\})\\s*```")
)

// ExtractToolCallsFromModelText 从 agy 响应文本中精准提取零个或多个外部工具调用，
// 并分离保留工具标签外的自然语言正文（prose），支持混合正文 + 并行工具调用。
func ExtractToolCallsFromModelText(raw string, spec AdaptedToolsSpec) (prose string, calls []ExtractedToolCall) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	re := xmlToolCallBlockRe
	locs := re.FindAllStringSubmatchIndex(raw, -1)
	if len(locs) == 0 {
		re = fencedToolCallBlockRe
		locs = re.FindAllStringSubmatchIndex(raw, -1)
	}
	if len(locs) == 0 {
		return raw, nil
	}

	var proseBuf strings.Builder
	lastEnd := 0

	for _, loc := range locs {
		if len(loc) < 4 {
			continue
		}
		matchStart, matchEnd := loc[0], loc[1]
		jsonBody := strings.TrimSpace(raw[loc[2]:loc[3]])

		parsed, ok := parseSingleExtractedToolCall(jsonBody, spec)
		if !ok {
			// 非法或未声明工具块保留原文（避免误吞用户正文）
			proseBuf.WriteString(raw[lastEnd:matchEnd])
			lastEnd = matchEnd
			continue
		}
		proseBuf.WriteString(raw[lastEnd:matchStart])
		lastEnd = matchEnd
		calls = append(calls, parsed)
	}
	if lastEnd < len(raw) {
		proseBuf.WriteString(raw[lastEnd:])
	}

	if len(calls) == 0 {
		return raw, nil
	}

	cleanProse := strings.TrimSpace(proseBuf.String())
	// 去除仅剩孤立围栏 ```xml / ``` 的残留外壳
	if cleanProse == "```" || cleanProse == "```xml" || cleanProse == "```json" || cleanProse == "```xml\n```" {
		cleanProse = ""
	}
	return cleanProse, calls
}

func parseSingleExtractedToolCall(jsonBody string, spec AdaptedToolsSpec) (ExtractedToolCall, bool) {
	var rawObj map[string]any
	if err := json.Unmarshal([]byte(jsonBody), &rawObj); err != nil || rawObj == nil {
		return ExtractedToolCall{}, false
	}

	rawName := strings.TrimSpace(stringFromAny(rawObj["name"]))
	if rawName == "" {
		if fn, ok := rawObj["function"].(map[string]any); ok && fn != nil {
			rawName = strings.TrimSpace(stringFromAny(fn["name"]))
			if rawObj["arguments"] == nil && fn["arguments"] != nil {
				rawObj["arguments"] = fn["arguments"]
			}
		}
	}
	resolvedName, ok := spec.ResolveToolName(rawName)
	if !ok {
		return ExtractedToolCall{}, false
	}

	var argVal any
	switch {
	case rawObj["arguments"] != nil:
		argVal = rawObj["arguments"]
	case rawObj["parameters"] != nil:
		argVal = rawObj["parameters"]
	case rawObj["input"] != nil:
		if m, isMap := rawObj["input"].(map[string]any); isMap {
			argVal = m
		} else {
			argVal = map[string]any{"input": rawObj["input"]}
		}
	default:
		argVal = map[string]any{}
	}

	argStr := normalizeExtractedArguments(argVal)
	return ExtractedToolCall{
		Name:      resolvedName,
		Arguments: argStr,
	}, true
}

func normalizeExtractedArguments(val any) string {
	switch v := val.(type) {
	case nil:
		return "{}"
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return "{}"
		}
		// 若模型把 JSON 对象编码成字符串，直接解包避免双重转义
		if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
			(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
			if json.Valid([]byte(trimmed)) {
				return trimmed
			}
		}
		b, _ := json.Marshal(map[string]any{"input": trimmed})
		return string(b)
	default:
		b, err := json.Marshal(v)
		if err != nil || len(b) == 0 || string(b) == "null" {
			return "{}"
		}
		return string(b)
	}
}
