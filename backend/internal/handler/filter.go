package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/filter"
	"cmd2api/internal/prompt"
	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

// filterRuleDTO 对齐前端 FilterRule 结构。
type filterRuleDTO struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Pattern       string `json:"pattern"`
	Replacement   string `json:"replacement"`
	IsRegex       bool   `json:"is_regex"`
	CaseSensitive bool   `json:"case_sensitive"`
	WordBoundary  bool   `json:"word_boundary"`
	Scope         string `json:"scope"`
	Priority      int    `json:"priority"`
	Enabled       bool   `json:"enabled"`
	IsBuiltin     bool   `json:"is_builtin"`
	Description   string `json:"description"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func toFilterRuleDTO(r *ent.FilterRule) filterRuleDTO {
	return filterRuleDTO{
		ID:            r.ID,
		Name:          r.Name,
		Kind:          r.Kind,
		Pattern:       r.Pattern,
		Replacement:   r.Replacement,
		IsRegex:       r.IsRegex,
		CaseSensitive: r.CaseSensitive,
		WordBoundary:  r.WordBoundary,
		Scope:         r.Scope,
		Priority:      r.Priority,
		Enabled:       r.Enabled,
		IsBuiltin:     r.IsBuiltin,
		Description:   r.Description,
		CreatedAt:     r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type createFilterRuleRequest struct {
	Name          string `json:"name" binding:"required"`
	Kind          string `json:"kind" binding:"required"`
	Pattern       string `json:"pattern" binding:"required"`
	Replacement   string `json:"replacement"`
	IsRegex       bool   `json:"is_regex"`
	CaseSensitive bool   `json:"case_sensitive"`
	WordBoundary  *bool  `json:"word_boundary"`
	Scope         string `json:"scope"`
	Priority      *int   `json:"priority"`
	Enabled       *bool  `json:"enabled"`
	Description   string `json:"description"`
}

type updateFilterRuleRequest struct {
	Name          *string `json:"name"`
	Kind          *string `json:"kind"`
	Pattern       *string `json:"pattern"`
	Replacement   *string `json:"replacement"`
	IsRegex       *bool   `json:"is_regex"`
	CaseSensitive *bool   `json:"case_sensitive"`
	WordBoundary  *bool   `json:"word_boundary"`
	Scope         *string `json:"scope"`
	Priority      *int    `json:"priority"`
	Enabled       *bool   `json:"enabled"`
	Description   *string `json:"description"`
}

type previewFilterRuleRequest struct {
	// 兼容旧版单文本框预览参数：
	Input    string `json:"input"`
	Mode     string `json:"mode"`
	Protocol string `json:"protocol"`

	// 实时四阶段拦截与 AGY 接续管线沙盒参数：
	SystemPrompt string `json:"system_prompt"`
	UserMessage  string `json:"user_message"`
	AgyMode      string `json:"agy_mode"`
	HasTools     *bool  `json:"has_tools"`
	GroupID      *int64 `json:"group_id"`
}

type pipelineStageDTO struct {
	Key          string `json:"key"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	SystemPrompt string `json:"system_prompt"`
	UserMessage  string `json:"user_message"`
	Changed      bool   `json:"changed"`
}

// ListFilterRules 列出所有内容风控规则与启用分类统计。
func (h *Handler) ListFilterRules(c *gin.Context) {
	if h.filters == nil {
		fail(c, http.StatusServiceUnavailable, "内容风控服务未启用")
		return
	}
	rules, stats, err := h.filters.List(c.Request.Context())
	if err != nil {
		h.failInternal(c, "查询过滤规则失败", err)
		return
	}
	items := make([]filterRuleDTO, 0, len(rules))
	for _, r := range rules {
		items = append(items, toFilterRuleDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
		"stats": stats,
	})
}

// CreateFilterRule 新增内容风控规则并热更新规则引擎。
func (h *Handler) CreateFilterRule(c *gin.Context) {
	if h.filters == nil {
		fail(c, http.StatusServiceUnavailable, "内容风控服务未启用")
		return
	}
	var req createFilterRuleRequest
	if !bindJSON(c, &req) {
		return
	}
	rule, err := h.filters.Create(c.Request.Context(), service.CreateFilterRuleInput{
		Name:          req.Name,
		Kind:          req.Kind,
		Pattern:       req.Pattern,
		Replacement:   req.Replacement,
		IsRegex:       req.IsRegex,
		CaseSensitive: req.CaseSensitive,
		WordBoundary:  req.WordBoundary,
		Scope:         req.Scope,
		Priority:      req.Priority,
		Enabled:       req.Enabled,
		Description:   req.Description,
	})
	if err != nil {
		h.writeFilterError(c, err, "创建过滤规则失败")
		return
	}
	c.JSON(http.StatusCreated, toFilterRuleDTO(rule))
}

// UpdateFilterRule 局部更新内容风控规则并热更新规则引擎。
func (h *Handler) UpdateFilterRule(c *gin.Context) {
	if h.filters == nil {
		fail(c, http.StatusServiceUnavailable, "内容风控服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req updateFilterRuleRequest
	if !bindJSON(c, &req) {
		return
	}
	rule, err := h.filters.Update(c.Request.Context(), id, service.UpdateFilterRuleInput{
		Name:          req.Name,
		Kind:          req.Kind,
		Pattern:       req.Pattern,
		Replacement:   req.Replacement,
		IsRegex:       req.IsRegex,
		CaseSensitive: req.CaseSensitive,
		WordBoundary:  req.WordBoundary,
		Scope:         req.Scope,
		Priority:      req.Priority,
		Enabled:       req.Enabled,
		Description:   req.Description,
	})
	if err != nil {
		h.writeFilterError(c, err, "更新过滤规则失败")
		return
	}
	c.JSON(http.StatusOK, toFilterRuleDTO(rule))
}

// DeleteFilterRule 删除指定过滤规则并热更新规则引擎。
func (h *Handler) DeleteFilterRule(c *gin.Context) {
	if h.filters == nil {
		fail(c, http.StatusServiceUnavailable, "内容风控服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.filters.Delete(c.Request.Context(), id); err != nil {
		h.writeFilterError(c, err, "删除过滤规则失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ResetFilterRules 清空并恢复默认内置内容风控规则库。
func (h *Handler) ResetFilterRules(c *gin.Context) {
	if h.filters == nil {
		fail(c, http.StatusServiceUnavailable, "内容风控服务未启用")
		return
	}
	rules, err := h.filters.ResetDefaults(c.Request.Context())
	if err != nil {
		h.failInternal(c, "重置过滤规则失败", err)
		return
	}
	items := make([]filterRuleDTO, 0, len(rules))
	for _, r := range rules {
		items = append(items, toFilterRuleDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{
		"items":   items,
		"total":   len(items),
		"message": "已恢复默认内置规则",
	})
}

// PreviewFilterRules 同时兼容单输入脱敏测试（input/mode/protocol）与四阶段实时拦截接续沙盒
// （original -> harness stripped -> filter rules applied -> AGY continuation preview）。
func (h *Handler) PreviewFilterRules(c *gin.Context) {
	if h.filters == nil {
		fail(c, http.StatusServiceUnavailable, "内容风控服务未启用")
		return
	}
	var req previewFilterRuleRequest
	if !bindJSON(c, &req) {
		return
	}

	if strings.TrimSpace(req.Input) == "" &&
		strings.TrimSpace(req.SystemPrompt) == "" &&
		strings.TrimSpace(req.UserMessage) == "" {
		fail(c, http.StatusBadRequest, "input、system_prompt 或 user_message 至少提供一项")
		return
	}

	isLegacyOnly := req.Input != "" &&
		req.SystemPrompt == "" &&
		req.UserMessage == "" &&
		strings.TrimSpace(req.AgyMode) == "" &&
		req.HasTools == nil

	if isLegacyOnly {
		legacyOut, err := h.filters.Preview(req.Input, req.Mode, req.Protocol)
		if err != nil {
			h.writeFilterError(c, err, "预览过滤效果失败")
			return
		}
		origSys := req.Input
		origUser := ""
		if legacyOut.Mode == "user_harness" {
			origSys = ""
			origUser = req.Input
		}
		pipe := h.runFilterContinuationPipeline(origSys, origUser, "default_intercept_continue", true, req.GroupID)
		c.JSON(http.StatusOK, gin.H{
			"input":                    legacyOut.Input,
			"output":                   legacyOut.Output,
			"mode":                     legacyOut.Mode,
			"changed":                  legacyOut.Changed,
			"hits":                     legacyOut.Hits,
			"stats":                    legacyOut.Stats,
			"original_system":          pipe.origSys,
			"original_user":            pipe.origUser,
			"harness_stripped_system":  pipe.sysHarnessStripped,
			"harness_stripped_user":    pipe.userHarnessStripped,
			"detected_harnesses":       pipe.detectedHarnesses,
			"sanitized_system":         pipe.sanitizedSys,
			"sanitized_user":           pipe.sanitizedUser,
			"agy_mode":                 pipe.agyMode,
			"has_tools":                pipe.hasTools,
			"interception_bridge":      pipe.interceptionBridge,
			"agy_continuation_preview": pipe.finalPrompt,
			"final_prompt":             pipe.finalPrompt,
			"final_user":               pipe.sanitizedUser,
			"stages":                   pipe.stages,
		})
		return
	}

	origSys := req.SystemPrompt
	origUser := req.UserMessage
	if origSys == "" && origUser == "" && req.Input != "" {
		if strings.EqualFold(strings.TrimSpace(req.Mode), "user_harness") ||
			strings.EqualFold(strings.TrimSpace(req.Mode), "user") {
			origUser = req.Input
		} else {
			origSys = req.Input
		}
	}

	hasTools := true
	if req.HasTools != nil {
		hasTools = *req.HasTools
	}

	pipe := h.runFilterContinuationPipeline(origSys, origUser, req.AgyMode, hasTools, req.GroupID)

	combinedInput := origSys
	if origUser != "" {
		if combinedInput != "" {
			combinedInput += "\n\n---\n\n" + origUser
		} else {
			combinedInput = origUser
		}
	}
	primaryOutput := pipe.finalPrompt
	if origSys == "" && strings.TrimSpace(req.AgyMode) == "" {
		primaryOutput = pipe.sanitizedUser
	}

	c.JSON(http.StatusOK, gin.H{
		"input":                    combinedInput,
		"output":                   primaryOutput,
		"mode":                     "pipeline",
		"changed":                  pipe.anyChanged,
		"hits":                     pipe.hits,
		"stats":                    pipe.stats,
		"original_system":          pipe.origSys,
		"original_user":            pipe.origUser,
		"harness_stripped_system":  pipe.sysHarnessStripped,
		"harness_stripped_user":    pipe.userHarnessStripped,
		"detected_harnesses":       pipe.detectedHarnesses,
		"sanitized_system":         pipe.sanitizedSys,
		"sanitized_user":           pipe.sanitizedUser,
		"agy_mode":                 pipe.agyMode,
		"has_tools":                pipe.hasTools,
		"interception_bridge":      pipe.interceptionBridge,
		"agy_continuation_preview": pipe.finalPrompt,
		"final_prompt":             pipe.finalPrompt,
		"final_user":               pipe.sanitizedUser,
		"stages":                   pipe.stages,
	})
}

type pipelinePreviewComputed struct {
	origSys             string
	origUser            string
	sysHarnessStripped  string
	userHarnessStripped string
	detectedHarnesses   []filter.DetectedHarness
	sanitizedSys        string
	sanitizedUser       string
	agyMode             string
	hasTools            bool
	interceptionBridge  string
	finalPrompt         string
	hits                []filter.RuleHit
	stats               filter.Stats
	stages              []pipelineStageDTO
	anyChanged          bool
}

func (h *Handler) runFilterContinuationPipeline(
	origSys string,
	origUser string,
	rawAgyMode string,
	hasTools bool,
	groupID *int64,
) pipelinePreviewComputed {
	sysStripped, sysHarnesses := filter.StripConflictingSystemHarnesses(origSys)
	userStripped, userHarnesses := filter.StripUserHarnesses(origUser)

	detected := make([]filter.DetectedHarness, 0, len(sysHarnesses)+len(userHarnesses))
	detected = append(detected, sysHarnesses...)
	detected = append(detected, userHarnesses...)

	eng := h.filters.Engine()
	sanitizedSys, sysHits := eng.SanitizeText(filter.ScopeSystem, sysStripped)
	sanitizedUser, userHits := eng.SanitizeText(filter.ScopeUser, origUser)

	effectiveDownstreamSys := sanitizedSys
	if groupID != nil && *groupID > 0 {
		if pSvc := h.promptSvc(); pSvc != nil && pSvc.Engine() != nil {
			resolvedSys, _, _ := pSvc.Engine().ResolveSystemPrompt(*groupID, sanitizedSys, prompt.Vars{
				Date: time.Now().UTC().Format("2006-01-02"),
			})
			effectiveDownstreamSys = resolvedSys
		}
	}

	hits := mergeRuleHits(sysHits, userHits)
	fieldsTouched := 0
	if sanitizedSys != origSys {
		fieldsTouched++
	}
	if sanitizedUser != origUser {
		fieldsTouched++
	}
	stats := filter.Stats{
		FieldsTouched: fieldsTouched,
		Hits:          hits,
	}
	for _, hit := range hits {
		switch hit.Kind {
		case filter.KindPrune:
			stats.PruneHits += hit.Count
		case filter.KindRewrite:
			stats.RewriteHits += hit.Count
		case filter.KindReplace:
			stats.ReplaceHits += hit.Count
		}
	}

	agyMode := normalizePipelineAgyMode(rawAgyMode)
	interceptionBridge, finalPrompt := buildAgyContinuationPreview(agyMode, effectiveDownstreamSys, hasTools)

	stage2Changed := sysStripped != origSys || userStripped != origUser || len(detected) > 0
	stage3Changed := sanitizedSys != sysStripped || sanitizedUser != origUser || len(hits) > 0
	stage4Changed := finalPrompt != effectiveDownstreamSys

	stages := []pipelineStageDTO{
		{
			Key:          "original",
			Title:        "1. 原始输入 (Original)",
			Description:  "下游客户端传入的原始 System 提示词与 User 消息载荷",
			SystemPrompt: origSys,
			UserMessage:  origUser,
			Changed:      false,
		},
		{
			Key:          "harness_stripped",
			Title:        "2. Harness 识别与冲突剥离",
			Description:  "自动识别并剥离 Cursor / Windsurf / Cline / Claude Code / Codex / AGY / DSH 平台专属宿主包装块，严格保留用户正文与代码块",
			SystemPrompt: sysStripped,
			UserMessage:  userStripped,
			Changed:      stage2Changed,
		},
		{
			Key:          "filter_applied",
			Title:        "3. 三级风控规则脱敏 (Prune -> Rewrite -> Replace)",
			Description:  "清理计费遥测头、中和跨平台身份声明与敏感词，零宽字符禁用且不误伤代码标识符与常见工程词汇",
			SystemPrompt: sanitizedSys,
			UserMessage:  sanitizedUser,
			Changed:      stage3Changed,
		},
		{
			Key:          "agy_continuation",
			Title:        "4. AGY 默认提示词拦截与接续终态",
			Description:  "在保留/按模式处理 AGY 原生提示词基础上注入无感拦截桥并无缝接续下游指令与工具协议",
			SystemPrompt: finalPrompt,
			UserMessage:  sanitizedUser,
			Changed:      stage4Changed,
		},
	}

	anyChanged := stage2Changed || stage3Changed || stage4Changed
	return pipelinePreviewComputed{
		origSys:             origSys,
		origUser:            origUser,
		sysHarnessStripped:  sysStripped,
		userHarnessStripped: userStripped,
		detectedHarnesses:   detected,
		sanitizedSys:        sanitizedSys,
		sanitizedUser:       sanitizedUser,
		agyMode:             agyMode,
		hasTools:            hasTools,
		interceptionBridge:  interceptionBridge,
		finalPrompt:         finalPrompt,
		hits:                hits,
		stats:               stats,
		stages:              stages,
		anyChanged:          anyChanged,
	}
}

func normalizePipelineAgyMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "default", "intercept_continue", "default_intercept_continue":
		return "default_intercept_continue"
	case "blend_sanitized", "blend_clean", "clean":
		return "blend_sanitized"
	case "native_only", "native_raw", "native":
		return "native_only"
	case "downstream_only", "verbatim_only", "verbatim", "passthrough", "off":
		return "downstream_only"
	default:
		return "default_intercept_continue"
	}
}

func buildAgyContinuationPreview(agyMode, sanitizedDownstream string, hasTools bool) (string, string) {
	trimmedDownstream := strings.TrimSpace(sanitizedDownstream)

	toolRule := "- No downstream external tools are declared for this turn; answer directly in plain markdown/text and do NOT invoke host IDE/shell tools."
	if hasTools {
		toolRule = "- Downstream external tools are active: NEVER execute host-side IDE/shell tools on the proxy; emit requested tool invocations strictly via `<AGY_TOOL_CALL>` matching the caller's tool schemas."
	}

	bridge := "<agy_intercept_bridge mode=\"" + agyMode + "\">\n" +
		"Headless API Reverse-Proxy Interception & Continuation Rules:\n" +
		"- Keep core engineering reasoning active while neutralizing IDE-only defaults (`ArtifactMetadata`, `.system_generated`, `/goal` slash commands, `<plugin-card>`, and `file:///` links).\n" +
		"- Do NOT self-identify as Antigravity IDE unless explicitly requested below.\n" +
		toolRule + "\n" +
		"- Treat `<downstream_continuation>` below as the authoritative persona, OS/workspace, formatting, and task specification.\n" +
		"</agy_intercept_bridge>"

	switch agyMode {
	case "native_only":
		return "", "[AGY Native Prompt Only — Downstream System Prompt Omitted]\n" + antigravity.CleanAgyNativePromptCore

	case "downstream_only":
		if trimmedDownstream == "" {
			return "", "[Downstream Passthrough — Empty System Prompt]"
		}
		return "", trimmedDownstream

	case "blend_sanitized":
		blended := antigravity.BlendAgyNativeAndDownstreamPrompt(
			antigravity.CleanAgyNativePromptCore,
			[]string{trimmedDownstream},
			antigravity.PromptBlendClean,
			false,
		)
		return bridge, blended

	default: // "default_intercept_continue"
		parts := []string{
			"[Stage 1: Authentic AGY CLI Built-in Default System Prompt — Preserved Intact Upstream]",
			bridge,
		}
		if trimmedDownstream != "" {
			parts = append(parts, "<downstream_continuation>\n"+trimmedDownstream+"\n</downstream_continuation>")
		} else {
			parts = append(parts, "<downstream_continuation>\n(No custom downstream system prompt provided; reply directly and cleanly to the user request.)\n</downstream_continuation>")
		}
		return bridge, strings.Join(parts, "\n\n")
	}
}

func mergeRuleHits(a, b []filter.RuleHit) []filter.RuleHit {
	if len(a) == 0 && len(b) == 0 {
		return []filter.RuleHit{}
	}
	out := make([]filter.RuleHit, 0, len(a)+len(b))
	idxByRule := make(map[int64]int, len(a)+len(b))
	for _, h := range a {
		if pos, exists := idxByRule[h.RuleID]; exists {
			out[pos].Count += h.Count
			continue
		}
		idxByRule[h.RuleID] = len(out)
		out = append(out, h)
	}
	for _, h := range b {
		if pos, exists := idxByRule[h.RuleID]; exists {
			out[pos].Count += h.Count
			continue
		}
		idxByRule[h.RuleID] = len(out)
		out = append(out, h)
	}
	return out
}

func (h *Handler) writeFilterError(c *gin.Context, err error, fallback string) {
	switch {
	case isNotFound(err):
		fail(c, http.StatusNotFound, "过滤规则不存在")
	case isConstraint(err):
		fail(c, http.StatusConflict, "过滤规则名称已存在")
	case errors.Is(err, service.ErrInvalidFilterRule):
		fail(c, http.StatusBadRequest, err.Error())
	default:
		msg := err.Error()
		if strings.Contains(msg, "不能为空") ||
			strings.Contains(msg, "无效") ||
			strings.Contains(msg, "不支持") ||
			strings.Contains(msg, "零宽") ||
			strings.Contains(msg, "正则") ||
			strings.Contains(msg, "regex") ||
			strings.Contains(msg, "pattern") {
			fail(c, http.StatusBadRequest, msg)
			return
		}
		h.failInternal(c, fallback, err)
	}
}
