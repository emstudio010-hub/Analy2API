package prompt

import (
	"strings"
	"sync"
	"time"

	"cmd2api/internal/domain"
)

// GlobalConfig 描述全局底线系统提示词开关与内容。
type GlobalConfig struct {
	Enabled bool   `json:"enabled"`
	Prompt  string `json:"prompt"`
}

// GroupConfig 描述单个分组的提示词注入策略快照。
type GroupConfig struct {
	GroupID      int64  `json:"group_id"`
	GroupName    string `json:"group_name"`
	Platform     string `json:"platform"`
	Mode         string `json:"mode"`
	SystemPrompt string `json:"system_prompt"`
	TemplateID   *int64 `json:"template_id,omitempty"`
}

// Vars 是提示词模板渲染时可用的上下文变量。
type Vars struct {
	Date     string `json:"date"`
	Model    string `json:"model"`
	Platform string `json:"platform"`
	Group    string `json:"group"`
}

type snapshot struct {
	global    GlobalConfig
	groups    map[int64]GroupConfig
	templates map[int64]string
}

// Engine 是并发安全的前置系统提示词分层解析与消息注入引擎。
type Engine struct {
	mu   sync.RWMutex
	snap snapshot
}

// NewEngine 使用给定的全局配置、分组配置与模板表初始化引擎。
func NewEngine(global GlobalConfig, groups []GroupConfig, templates map[int64]string) *Engine {
	e := &Engine{}
	e.Reload(global, groups, templates)
	return e
}

// NewDefaultEngine 构造默认开启 Ess-Agentic 全局底线提示词的引擎。
func NewDefaultEngine() *Engine {
	return NewEngine(
		GlobalConfig{
			Enabled: true,
			Prompt:  DefaultGlobalPrompt(),
		},
		nil,
		nil,
	)
}

// Reload 以线程安全方式原子替换当前生效的提示词配置快照。
func (e *Engine) Reload(global GlobalConfig, groups []GroupConfig, templates map[int64]string) {
	if e == nil {
		return
	}

	groupMap := make(map[int64]GroupConfig, len(groups))
	for _, g := range groups {
		cloned := g
		if g.TemplateID != nil {
			idCopy := *g.TemplateID
			cloned.TemplateID = &idCopy
		}
		cloned.Mode = normalizeEffectiveMode(g.Mode)
		groupMap[g.GroupID] = cloned
	}

	tplMap := make(map[int64]string, len(templates))
	for id, content := range templates {
		tplMap[id] = content
	}

	e.mu.Lock()
	e.snap = snapshot{
		global:    global,
		groups:    groupMap,
		templates: tplMap,
	}
	e.mu.Unlock()
}

// Global 返回当前全局提示词配置快照。
func (e *Engine) Global() GlobalConfig {
	if e == nil {
		return GlobalConfig{}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snap.global
}

// Group 返回指定分组的提示词配置快照。
func (e *Engine) Group(groupID int64) (GroupConfig, bool) {
	if e == nil {
		return GroupConfig{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	g, ok := e.snap.groups[groupID]
	if !ok {
		return GroupConfig{}, false
	}
	if g.TemplateID != nil {
		idCopy := *g.TemplateID
		g.TemplateID = &idCopy
	}
	return g, true
}

func (e *Engine) currentSnapshot() snapshot {
	if e == nil {
		return snapshot{}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snap
}

type resolvedLayers struct {
	mode        string
	globalText  string
	groupText   string
	groupLayer  string // "group" 或 "template"
}

func (e *Engine) evaluateLayers(groupID int64, vars Vars) resolvedLayers {
	snap := e.currentSnapshot()
	grp, hasGroup := snap.groups[groupID]

	mode := domain.PromptModeAppendGlobal
	if hasGroup {
		mode = normalizeEffectiveMode(grp.Mode)
	}

	rv := fillVars(vars, grp, hasGroup)

	var globalText string
	if snap.global.Enabled && strings.TrimSpace(snap.global.Prompt) != "" {
		globalText = strings.TrimSpace(renderVars(snap.global.Prompt, rv))
	}

	var groupText string
	groupLayer := "group"
	if hasGroup {
		rawGroup := strings.TrimSpace(grp.SystemPrompt)
		if rawGroup == "" && grp.TemplateID != nil {
			if tpl, ok := snap.templates[*grp.TemplateID]; ok && strings.TrimSpace(tpl) != "" {
				rawGroup = strings.TrimSpace(tpl)
				groupLayer = "template"
			}
		}
		if rawGroup != "" {
			groupText = strings.TrimSpace(renderVars(rawGroup, rv))
		}
	}

	return resolvedLayers{
		mode:       mode,
		globalText: globalText,
		groupText:  groupText,
		groupLayer: groupLayer,
	}
}

// ResolveSystemPrompt 根据分组策略合成最终系统提示词：
//   - off：不注入任何额外提示词（replaceClient=false, layers=["off"]）
//   - append_global：[全局提示词] + [分组/模板提示词] + [客户端提示词]
//   - override_global：[分组/模板提示词] + [客户端提示词]
//   - replace_all：replaceClient=true，丢弃客户端提示词，仅使用 [分组/模板提示词（若空回退全局提示词）]
func (e *Engine) ResolveSystemPrompt(groupID int64, clientSystem string, vars Vars) (finalSystem string, replaceClient bool, layers []string) {
	rl := e.evaluateLayers(groupID, vars)
	trimmedClient := strings.TrimSpace(clientSystem)

	switch rl.mode {
	case domain.PromptModeOff:
		return trimmedClient, false, []string{domain.PromptModeOff}

	case domain.PromptModeReplaceAll:
		if rl.groupText != "" {
			return rl.groupText, true, []string{rl.groupLayer}
		}
		if rl.globalText != "" {
			return rl.globalText, true, []string{"global"}
		}
		return "", true, []string{}

	case domain.PromptModeOverrideGlobal:
		parts := make([]string, 0, 2)
		layers = make([]string, 0, 2)
		if rl.groupText != "" && !strings.Contains(trimmedClient, rl.groupText) {
			parts = append(parts, rl.groupText)
			layers = append(layers, rl.groupLayer)
		} else if rl.groupText != "" {
			layers = append(layers, rl.groupLayer)
		}
		if trimmedClient != "" {
			parts = append(parts, trimmedClient)
			layers = append(layers, "client")
		}
		return strings.Join(parts, "\n\n"), false, layers

	default: // domain.PromptModeAppendGlobal
		parts := make([]string, 0, 3)
		layers = make([]string, 0, 3)
		if rl.globalText != "" && !strings.Contains(trimmedClient, rl.globalText) {
			parts = append(parts, rl.globalText)
			layers = append(layers, "global")
		} else if rl.globalText != "" {
			layers = append(layers, "global")
		}
		if rl.groupText != "" && !strings.Contains(trimmedClient, rl.groupText) {
			parts = append(parts, rl.groupText)
			layers = append(layers, rl.groupLayer)
		} else if rl.groupText != "" {
			layers = append(layers, rl.groupLayer)
		}
		if trimmedClient != "" {
			parts = append(parts, trimmedClient)
			layers = append(layers, "client")
		}
		return strings.Join(parts, "\n\n"), false, layers
	}
}

// ApplyToMessages 对归一化后的 messages（含 role: "system" / "developer"）应用 4 种注入模式：
//   - off：保持原样（返回浅拷贝切片，不篡改调用方底层数组）；
//   - replace_all：剥离全部原有 system / developer 消息，前置注入合成后的单条 role: "system" 消息；
//   - append_global / override_global：前置注入额外提示词（若尚未出现在已有 system/developer 消息中，自动防重）。
func (e *Engine) ApplyToMessages(groupID int64, messages []map[string]any, vars Vars) []map[string]any {
	rl := e.evaluateLayers(groupID, vars)

	switch rl.mode {
	case domain.PromptModeOff:
		return cloneMessagesSlice(messages)

	case domain.PromptModeReplaceAll:
		injected := rl.groupText
		if injected == "" {
			injected = rl.globalText
		}
		filtered := make([]map[string]any, 0, len(messages)+1)
		if injected != "" {
			filtered = append(filtered, map[string]any{
				"role":    "system",
				"content": injected,
			})
		}
		for _, m := range messages {
			if isSystemOrDeveloperRole(messageRole(m)) {
				continue
			}
			filtered = append(filtered, cloneMessageMap(m))
		}
		return filtered

	case domain.PromptModeOverrideGlobal:
		return prependSystemWithDedup(messages, "", rl.groupText)

	default: // domain.PromptModeAppendGlobal
		return prependSystemWithDedup(messages, rl.globalText, rl.groupText)
	}
}

func prependSystemWithDedup(messages []map[string]any, globalText, groupText string) []map[string]any {
	existingSys := collectExistingSystemText(messages)
	toInjectParts := make([]string, 0, 2)
	if globalText != "" && !strings.Contains(existingSys, globalText) {
		toInjectParts = append(toInjectParts, globalText)
	}
	if groupText != "" && !strings.Contains(existingSys, groupText) {
		if globalText == "" || groupText != globalText {
			toInjectParts = append(toInjectParts, groupText)
		}
	}

	if len(toInjectParts) == 0 {
		return cloneMessagesSlice(messages)
	}

	injectedMsg := map[string]any{
		"role":    "system",
		"content": strings.Join(toInjectParts, "\n\n"),
	}
	out := make([]map[string]any, 0, len(messages)+1)
	out = append(out, injectedMsg)
	for _, m := range messages {
		out = append(out, cloneMessageMap(m))
	}
	return out
}

func normalizeEffectiveMode(mode string) string {
	norm := domain.NormalizePromptInjectMode(mode)
	if !domain.IsValidPromptInjectMode(norm) {
		return domain.PromptModeAppendGlobal
	}
	return norm
}

func fillVars(v Vars, grp GroupConfig, hasGroup bool) Vars {
	out := Vars{
		Date:     strings.TrimSpace(v.Date),
		Model:    strings.TrimSpace(v.Model),
		Platform: strings.TrimSpace(v.Platform),
		Group:    strings.TrimSpace(v.Group),
	}
	if out.Date == "" {
		out.Date = time.Now().UTC().Format("2006-01-02")
	}
	if hasGroup {
		if out.Platform == "" {
			out.Platform = strings.TrimSpace(grp.Platform)
		}
		if out.Group == "" {
			out.Group = strings.TrimSpace(grp.GroupName)
		}
	}
	return out
}

func renderVars(text string, v Vars) string {
	if text == "" || !strings.Contains(text, "{{") {
		return text
	}
	r := strings.NewReplacer(
		"{{date}}", v.Date,
		"{{ date }}", v.Date,
		"{{model}}", v.Model,
		"{{ model }}", v.Model,
		"{{platform}}", v.Platform,
		"{{ platform }}", v.Platform,
		"{{group}}", v.Group,
		"{{ group }}", v.Group,
	)
	return r.Replace(text)
}

func messageRole(m map[string]any) string {
	if m == nil {
		return ""
	}
	r, _ := m["role"].(string)
	return strings.ToLower(strings.TrimSpace(r))
}

func isSystemOrDeveloperRole(role string) bool {
	return role == "system" || role == "developer"
}

func collectExistingSystemText(messages []map[string]any) string {
	var b strings.Builder
	for _, m := range messages {
		if !isSystemOrDeveloperRole(messageRole(m)) {
			continue
		}
		txt := extractMessageContentText(m)
		if txt != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(txt)
		}
	}
	return b.String()
}

func extractMessageContentText(m map[string]any) string {
	if m == nil {
		return ""
	}
	switch v := m["content"].(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if blk, ok := item.(map[string]any); ok {
				if s, ok := blk["text"].(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	case []map[string]any:
		var parts []string
		for _, blk := range v {
			if s, ok := blk["text"].(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	}
	if s, ok := m["text"].(string); ok {
		return s
	}
	return ""
}

func cloneMessagesSlice(messages []map[string]any) []map[string]any {
	if messages == nil {
		return nil
	}
	out := make([]map[string]any, len(messages))
	for i, m := range messages {
		out[i] = cloneMessageMap(m)
	}
	return out
}

func cloneMessageMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
