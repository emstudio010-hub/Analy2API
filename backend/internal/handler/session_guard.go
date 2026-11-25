package handler

import (
	"net/http"
	"net/url"

	"cmd2api/internal/relay"

	"github.com/gin-gonic/gin"
)

// GetSessionGuard 返回非额度 429 会话暂停配置、当前暂停中的会话及审计日志列表（GET /api/session-guard）。
func (h *Handler) GetSessionGuard(c *gin.Context) {
	svc := h.sessionGuardSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "会话保护服务未初始化")
		return
	}
	c.JSON(http.StatusOK, svc.GetView(c.Request.Context()))
}

// UpdateSessionGuardConfig 校验、持久化并热更新非额度 429 会话保护策略（PUT /api/session-guard/config）。
func (h *Handler) UpdateSessionGuardConfig(c *gin.Context) {
	svc := h.sessionGuardSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "会话保护服务未初始化")
		return
	}
	var req relay.SessionGuardConfig
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

// ResumePausedSession 手动解除指定会话 ID 的暂停调度状态（DELETE /api/session-guard/sessions/:session_id）。
func (h *Handler) ResumePausedSession(c *gin.Context) {
	svc := h.sessionGuardSvc()
	if svc == nil || svc.Guard() == nil {
		fail(c, http.StatusServiceUnavailable, "会话保护服务未初始化")
		return
	}
	raw := c.Param("session_id")
	sid, err := url.PathUnescape(raw)
	if err != nil || sid == "" {
		sid = raw
	}
	resumed := svc.Guard().ResumeSession(sid)
	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"resumed":    resumed,
		"session_id": sid,
		"view":       svc.GetView(c.Request.Context()),
	})
}

// ClearPausedSessions 清空全部处于暂停中的会话（DELETE /api/session-guard/sessions）。
func (h *Handler) ClearPausedSessions(c *gin.Context) {
	svc := h.sessionGuardSvc()
	if svc == nil || svc.Guard() == nil {
		fail(c, http.StatusServiceUnavailable, "会话保护服务未初始化")
		return
	}
	cleared := svc.Guard().ClearAllPaused()
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"cleared": cleared,
		"view":    svc.GetView(c.Request.Context()),
	})
}

// ReadSession429Log 读取指定非额度 429 请求体审计 .log 文件全文（GET /api/session-guard/logs/:filename）。
func (h *Handler) ReadSession429Log(c *gin.Context) {
	svc := h.sessionGuardSvc()
	if svc == nil || svc.Guard() == nil {
		fail(c, http.StatusServiceUnavailable, "会话保护服务未初始化")
		return
	}
	filename := c.Param("filename")
	content, err := svc.Guard().ReadLogFile(filename)
	if err != nil {
		fail(c, http.StatusNotFound, "读取日志文件失败: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"filename": filename,
		"content":  content,
	})
}

func (h *Handler) buildSessionGuardSummary() gin.H {
	cfg := relay.DefaultSessionGuardConfig()
	pausedCount := 0
	logFilesCount := 0
	if svc := h.sessionGuardSvc(); svc != nil && svc.Guard() != nil {
		cfg = svc.Guard().Config()
		pausedCount = len(svc.Guard().PausedSessions())
		if logs, err := svc.Guard().ListLogFiles(500); err == nil {
			logFilesCount = len(logs)
		}
	} else if h.relay != nil && h.relay.SessionGuard() != nil {
		cfg = h.relay.SessionGuard().Config()
		pausedCount = len(h.relay.SessionGuard().PausedSessions())
	}
	return gin.H{
		"enabled":              cfg.Enabled,
		"pause_seconds":        cfg.PauseSeconds,
		"log_enabled":          cfg.LogEnabled,
		"log_dir":              cfg.LogDir,
		"max_log_files":        cfg.MaxLogFiles,
		"abort_current_retry":  cfg.AbortCurrentRetry,
		"fallback_fingerprint": cfg.FallbackFingerprint,
		"quota_keywords_count": len(cfg.QuotaKeywords),
		"paused_count":         pausedCount,
		"log_files_count":      logFilesCount,
	}
}
