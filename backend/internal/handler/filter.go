package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"cmd2api/ent"
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
	Input    string `json:"input" binding:"required"`
	Mode     string `json:"mode"`
	Protocol string `json:"protocol"`
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

// PreviewFilterRules 对样本文本或协议载荷试跑当前生效的内容风控规则。
func (h *Handler) PreviewFilterRules(c *gin.Context) {
	if h.filters == nil {
		fail(c, http.StatusServiceUnavailable, "内容风控服务未启用")
		return
	}
	var req previewFilterRuleRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.filters.Preview(req.Input, req.Mode, req.Protocol)
	if err != nil {
		h.writeFilterError(c, err, "预览过滤效果失败")
		return
	}
	c.JSON(http.StatusOK, out)
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
