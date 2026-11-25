package handler

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

type createOAuthSessionRequest struct {
	Platform     string  `json:"platform"`
	Provider     string  `json:"provider"`
	NodeID       int64   `json:"node_id"`
	AccountID    int64   `json:"account_id"`
	GroupIDs     []int64 `json:"group_ids"`
	ProxyURL     string  `json:"proxy_url"`
	RedirectURI  string  `json:"redirect_uri"`
	RedirectMode bool    `json:"redirect_mode"`
	AccountName  string  `json:"account_name"`
	AccountMode  string  `json:"account_mode"`
	BaseURL      string  `json:"base_url"`
}

type completeOAuthCallbackRequest struct {
	SessionID    string          `json:"session_id"`
	State        string          `json:"state"`
	Platform     string          `json:"platform"`
	Provider     string          `json:"provider"`
	NodeID       int64           `json:"node_id"`
	GroupIDs     []int64         `json:"group_ids"`
	ProxyURL     string          `json:"proxy_url"`
	CallbackURL  string          `json:"callback_url"`
	RedirectURL  string          `json:"redirect_url"`
	RedirectURI  string          `json:"redirect_uri"`
	Code         string          `json:"code"`
	APIKey       string          `json:"api_key"`
	RefreshToken string          `json:"refresh_token"`
	UID          string          `json:"uid"`
	Nickname     string          `json:"nickname"`
	Domain       string          `json:"domain"`
	Email        string          `json:"email"`
	ProjectID    string          `json:"project_id"`
	AccountName  string          `json:"account_name"`
	AccountMode  string          `json:"account_mode"`
	BaseURL      string          `json:"base_url"`
	Filename     string          `json:"filename"`
	TokenJSON    json.RawMessage `json:"token_json"`
	RawJSON      string          `json:"raw_json"`
}

// CreateOAuthSession 创建跨平台统一 OAuth 授权会话（POST /api/oauth/sessions）。
func (h *Handler) CreateOAuthSession(c *gin.Context) {
	svc := h.oauthSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "OAuth 服务未初始化")
		return
	}
	var req createOAuthSessionRequest
	if c.Request.ContentLength != 0 {
		if !bindJSON(c, &req) {
			return
		}
	}
	redirectURI := strings.TrimSpace(req.RedirectURI)
	if redirectURI == "" {
		redirectURI = inferExternalOAuthCallbackURL(c)
	}
	sess, err := svc.CreateSession(c.Request.Context(), service.CreateOAuthSessionInput{
		Platform:     req.Platform,
		Provider:     req.Provider,
		NodeID:       req.NodeID,
		AccountID:    req.AccountID,
		GroupIDs:     req.GroupIDs,
		ProxyURL:     req.ProxyURL,
		RedirectURI:  redirectURI,
		RedirectMode: req.RedirectMode,
		AccountName:  req.AccountName,
		AccountMode:  req.AccountMode,
		BaseURL:      req.BaseURL,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"session":      sess,
		"session_id":   sess.SessionID,
		"auth_url":     sess.AuthURL,
		"url":          sess.AuthURL,
		"state":        sess.State,
		"platform":     sess.Platform,
		"provider":     sess.Provider,
		"mode":         sess.Mode,
		"callback_url": sess.RedirectURI,
		"instructions": sess.Instructions,
	})
}

// ListOAuthSessions 列出近期与进行中的 OAuth 授权会话（GET /api/oauth/sessions）。
func (h *Handler) ListOAuthSessions(c *gin.Context) {
	svc := h.oauthSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "OAuth 服务未初始化")
		return
	}
	items := svc.ListSessions()
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
	})
}

// CompleteOAuthCallback 提交 OAuth 回调 URL、授权码或凭据 JSON 完成落盘与账号入池（POST /api/oauth/callback）。
func (h *Handler) CompleteOAuthCallback(c *gin.Context) {
	svc := h.oauthSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "OAuth 服务未初始化")
		return
	}
	var req completeOAuthCallbackRequest
	if !bindJSON(c, &req) {
		return
	}
	cbURL := strings.TrimSpace(req.CallbackURL)
	if cbURL == "" {
		cbURL = strings.TrimSpace(req.RedirectURL)
	}
	res, err := svc.CompleteCallback(c.Request.Context(), service.CompleteOAuthCallbackInput{
		SessionID:    req.SessionID,
		State:        req.State,
		Platform:     req.Platform,
		Provider:     req.Provider,
		NodeID:       req.NodeID,
		GroupIDs:     req.GroupIDs,
		ProxyURL:     req.ProxyURL,
		CallbackURL:  cbURL,
		RedirectURI:  req.RedirectURI,
		Code:         req.Code,
		APIKey:       req.APIKey,
		RefreshToken: req.RefreshToken,
		UID:          req.UID,
		Nickname:     req.Nickname,
		Domain:       req.Domain,
		Email:        req.Email,
		ProjectID:    req.ProjectID,
		AccountName:  req.AccountName,
		AccountMode:  req.AccountMode,
		BaseURL:      req.BaseURL,
		Filename:     req.Filename,
		TokenJSON:    req.TokenJSON,
		RawJSON:      req.RawJSON,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if h.sched != nil {
		h.sched.Invalidate()
	}
	h.respondOAuthCallbackJSON(c, res)
}

// HandleOAuthBrowserCallback 接收浏览器从 OAuth 提供商重定向回网关的 GET 请求（GET /api/oauth/callback），
// 严格通过随机高熵 state 匹配事先创建的授权会话；返回可视化中文结果页面（或 ?format=json）。
func (h *Handler) HandleOAuthBrowserCallback(c *gin.Context) {
	svc := h.oauthSvc()
	if svc == nil {
		c.String(http.StatusServiceUnavailable, "OAuth service not initialized")
		return
	}
	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	apiKey := ""
	for _, k := range []string{"apiKey", "api_key", "token", "access_token", "key"} {
		if v := strings.TrimSpace(c.Query(k)); v != "" {
			apiKey = v
			break
		}
	}
	if state == "" {
		renderOAuthHTMLResult(c, http.StatusBadRequest, false, "缺少授权 state 参数，请从管理控制台发起 OAuth 会话后再试。", "")
		return
	}

	fullURL := c.Request.URL.String()
	res, err := svc.CompleteCallback(c.Request.Context(), service.CompleteOAuthCallbackInput{
		State:        state,
		CallbackURL:  fullURL,
		Code:         code,
		APIKey:       apiKey,
		RefreshToken: strings.TrimSpace(c.Query("refresh_token")),
		UID:          strings.TrimSpace(c.Query("uid")),
		AccountMode:  strings.TrimSpace(c.Query("mode")),
		Email:        strings.TrimSpace(c.Query("email")),
		ProjectID:    strings.TrimSpace(c.Query("project_id")),
	})
	if err != nil {
		if c.Query("format") == "json" {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		renderOAuthHTMLResult(c, http.StatusBadRequest, false, "OAuth 授权回调处理失败", err.Error())
		return
	}
	if h.sched != nil {
		h.sched.Invalidate()
	}
	if c.Query("format") == "json" {
		h.respondOAuthCallbackJSON(c, res)
		return
	}
	renderOAuthHTMLResult(c, http.StatusOK, true, res.Message, fmt.Sprintf("平台: %s · 提供商: %s · 凭据文件: %s",
		res.Platform, res.Provider, res.Filename))
}

func (h *Handler) respondOAuthCallbackJSON(c *gin.Context, res *service.OAuthCallbackResult) {
	var accDTO *accountDTO
	if res.Account != nil {
		dto := h.toAccountDTO(res.Account)
		accDTO = &dto
	}
	accDTOs := make([]accountDTO, 0, len(res.Accounts))
	for _, a := range res.Accounts {
		accDTOs = append(accDTOs, h.toAccountDTO(a))
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":            res.OK,
		"session":       res.Session,
		"platform":      res.Platform,
		"provider":      res.Provider,
		"node_id":       res.NodeID,
		"filename":      res.Filename,
		"auth_file":     res.AuthFile,
		"account":       accDTO,
		"accounts":      accDTOs,
		"remote_result": res.RemoteResult,
		"message":       res.Message,
	})
}

func inferExternalOAuthCallbackURL(c *gin.Context) string {
	scheme := "http"
	if proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); proto != "" {
		scheme = proto
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(c.Request.Host)
	}
	if host == "" {
		host = "127.0.0.1:8080"
	}
	return fmt.Sprintf("%s://%s/api/oauth/callback", scheme, host)
}

func renderOAuthHTMLResult(c *gin.Context, status int, ok bool, title, detail string) {
	color := "#059669"
	badge := "授权成功"
	if !ok {
		color = "#DC2626"
		badge = "授权失败"
	}
	body := fmt.Sprintf(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>%s - cmd2api OAuth</title>
<style>body{font-family:system-ui,-apple-system,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}
.card{background:#1e293b;border:1px solid #334155;border-radius:14px;padding:28px 32px;max-width:520px;box-shadow:0 20px 40px rgba(0,0,0,.35)}
.badge{display:inline-block;padding:4px 12px;border-radius:999px;background:%s;color:#fff;font-size:13px;font-weight:600;margin-bottom:14px}
h1{font-size:20px;margin:0 0 10px}p{font-size:14px;line-height:1.6;color:#94a3b8;margin:0}</style></head>
<body><div class="card"><div class="badge">%s</div><h1>%s</h1><p>%s</p></div></body></html>`,
		html.EscapeString(badge), color, html.EscapeString(badge), html.EscapeString(title), html.EscapeString(detail))
	c.Data(status, "text/html; charset=utf-8", []byte(body))
}
