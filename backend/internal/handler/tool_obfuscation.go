package handler

import (
	"net/http"

	"cmd2api/internal/service"
	"cmd2api/internal/toolobf"

	"github.com/gin-gonic/gin"
)

// GetToolObfuscation 返回工具名混淆配置、内置别名词表、敏感工具目录及统计（GET /api/tool-obfuscation）。
func (h *Handler) GetToolObfuscation(c *gin.Context) {
	svc := h.toolObfSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "工具名混淆服务未初始化")
		return
	}
	view, err := svc.GetView(c.Request.Context())
	if err != nil {
		h.failInternal(c, "读取工具名混淆配置失败", err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// UpdateToolObfuscation 校验、持久化并立即热生效工具名混淆配置（PUT /api/tool-obfuscation）。
func (h *Handler) UpdateToolObfuscation(c *gin.Context) {
	svc := h.toolObfSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "工具名混淆服务未初始化")
		return
	}
	var req toolobf.Config
	if !bindJSON(c, &req) {
		return
	}
	view, err := svc.UpdateConfig(c.Request.Context(), req)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, view)
}

// RotateToolObfuscationSalt 轮换工具名混淆随机盐值并持久化热生效（POST /api/tool-obfuscation/rotate-salt）。
func (h *Handler) RotateToolObfuscationSalt(c *gin.Context) {
	svc := h.toolObfSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "工具名混淆服务未初始化")
		return
	}
	view, err := svc.RegenerateSalt(c.Request.Context())
	if err != nil {
		h.failInternal(c, "轮换工具名混淆盐值失败", err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// PreviewToolObfuscation 在线演练工具名“混淆 -> 双射还原”效果（POST /api/tool-obfuscation/preview）。
func (h *Handler) PreviewToolObfuscation(c *gin.Context) {
	svc := h.toolObfSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "工具名混淆服务未初始化")
		return
	}
	var req service.PreviewToolObfuscationInput
	if !bindJSON(c, &req) {
		return
	}
	out, err := svc.Preview(c.Request.Context(), req)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// UpdateGroupToolObfuscation 更新单个分组的工具名混淆独立策略并立即热生效（PUT /api/tool-obfuscation/groups/:id）。
func (h *Handler) UpdateGroupToolObfuscation(c *gin.Context) {
	svc := h.toolObfSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "工具名混淆服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req toolobf.GroupOverride
	if !bindJSON(c, &req) {
		return
	}
	view, err := svc.UpdateGroupOverride(c.Request.Context(), id, req)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *Handler) buildToolObfuscationSummary() gin.H {
	cfg := toolobf.DefaultConfig()
	if svc := h.toolObfSvc(); svc != nil && svc.Engine() != nil {
		cfg = svc.Engine().Config()
	} else if h.relay != nil && h.relay.ToolObfuscator() != nil {
		cfg = h.relay.ToolObfuscator().Config()
	}
	return gin.H{
		"enabled":               cfg.Enabled,
		"mode":                  cfg.Mode,
		"prefix":                cfg.Prefix,
		"scope":                 cfg.Scope,
		"platforms":             cfg.Platforms,
		"custom_map_count":      len(cfg.CustomMap),
		"exclude_count":         len(cfg.ExcludeTools),
		"rewrite_refs":          cfg.RewriteToolRefs,
		"group_overrides_count": len(cfg.GroupOverrides),
	}
}
