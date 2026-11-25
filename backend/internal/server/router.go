// Package server 组装 HTTP 路由与静态资源。
package server

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cmd2api/ent"
	"cmd2api/internal/auth"
	"cmd2api/internal/handler"
	"cmd2api/internal/middleware"

	"github.com/gin-gonic/gin"
)

// Options 是构建路由所需的参数。
type Options struct {
	Handler              *handler.Handler
	Issuer               *auth.Issuer
	Client               *ent.Client
	BootstrapAdminAPIKey string
	// StaticDir 是前端构建产物目录。为空或不存在时只提供 API。
	StaticDir string
	Mode      string
	// Version 是服务端构建版本号（注入自 -ldflags）。
	Version string
}

// NewRouter 构建 gin 引擎。
func NewRouter(opts Options) *gin.Engine {
	gin.SetMode(opts.Mode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(securityHeaders())
	r.Use(accessLog())

	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "1.0.0"
	}

	// 健康检查放在鉴权之外，供容器探针和反向代理使用。
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"service": "Analy2API",
			"version": version,
		})
	})

	h := opts.Handler

	// ---- 管理后台（支持 JWT Bearer 与 adm-... 远程管理密钥双轨鉴权）----
	api := r.Group("/api")
	{
		// 登录本身与携带随机 state 的 OAuth 浏览器回调不要求已带管理 JWT 头。
		api.POST("/auth/login", h.Login)
		api.GET("/oauth/callback", h.HandleOAuthBrowserCallback)

		admin := api.Group("")
		admin.Use(middleware.AdminAuth(opts.Issuer, middleware.WithAdminKeyStore(opts.Client, opts.BootstrapAdminAPIKey)))
		{
			admin.GET("/auth/me", h.Me)
			admin.POST("/auth/password", h.ChangePassword)
			admin.PUT("/auth/profile", h.UpdateEmail)

			admin.GET("/dashboard", h.Dashboard)

			// 全平台统一 OAuth 授权与回调闭环中心
			admin.POST("/oauth/sessions", h.CreateOAuthSession)
			admin.GET("/oauth/sessions", h.ListOAuthSessions)
			admin.POST("/oauth/callback", h.CompleteOAuthCallback)

			// 远程管理密钥 (adm-...)
			admin.GET("/admin-keys", h.ListAdminKeys)
			admin.POST("/admin-keys", h.CreateAdminKey)
			admin.PUT("/admin-keys/:id", h.UpdateAdminKey)
			admin.DELETE("/admin-keys/:id", h.DeleteAdminKey)

			// AntiGravity 容器池与 OAuth 登录 JSON 管理
			admin.GET("/antigravity/env", h.GetAntigravityEnv)
			admin.GET("/antigravity/nodes", h.ListAntigravityNodes)
			admin.POST("/antigravity/nodes", h.CreateAntigravityNode)
			admin.PUT("/antigravity/nodes/:id", h.UpdateAntigravityNode)
			admin.DELETE("/antigravity/nodes/:id", h.DeleteAntigravityNode)
			admin.POST("/antigravity/nodes/:id/action", h.RunAntigravityNodeAction)
			admin.GET("/antigravity/nodes/:id/logs", h.GetAntigravityNodeLogs)
			admin.GET("/antigravity/nodes/:id/auth-files", h.ListAntigravityAuthFiles)
			admin.POST("/antigravity/nodes/:id/auth-files", h.UploadAntigravityAuthFile)
			admin.DELETE("/antigravity/nodes/:id/auth-files/:filename", h.DeleteAntigravityAuthFile)
			admin.POST("/antigravity/nodes/:id/oauth-url", h.RequestAntigravityOAuthURL)
			admin.POST("/antigravity/nodes/:id/oauth-callback", h.CompleteAntigravityOAuthCallback)
			admin.POST("/antigravity/nodes/:id/sync-accounts", h.SyncAntigravityAccounts)
			admin.POST("/antigravity/nodes/:id/rotate-fingerprint", h.RotateAntigravityFingerprint)
			admin.POST("/antigravity/nodes/:id/check-ip", h.CheckAntigravityNodeExitIP)
			admin.POST("/antigravity/accounts/:id/provision-container", h.ProvisionAntigravityAccountContainer)

			// 四端上游账号池与远程补号推送入口（支持 full 与最小权限 push_accounts 补号专用密钥）
			admin.POST("/remote/accounts/push", h.RemotePushAccounts)
			admin.GET("/remote/accounts/push/info", h.GetRemotePushInfo)
			admin.GET("/remote/accounts/push/proxy", h.GetRemotePushProxyConfig)
			admin.PUT("/remote/accounts/push/proxy", h.UpdateRemotePushProxyConfig)
			admin.POST("/remote/accounts/push/proxy/fetch", h.FetchRemotePushProxyPool)
			admin.POST("/accounts/push", h.RemotePushAccounts)
			admin.GET("/accounts/push/info", h.GetRemotePushInfo)
			admin.GET("/accounts/push/proxy", h.GetRemotePushProxyConfig)
			admin.PUT("/accounts/push/proxy", h.UpdateRemotePushProxyConfig)
			admin.POST("/accounts/push/proxy/fetch", h.FetchRemotePushProxyPool)

			// 代理 IP 池管理、远程拉取代理源与轮询/独占自动分配
			admin.GET("/proxy-pool", h.GetProxyPool)
			admin.PUT("/proxy-pool", h.UpdateProxyPool)
			admin.POST("/proxy-pool/entries", h.AddProxyPoolEntries)
			admin.DELETE("/proxy-pool/entries/:id", h.DeleteProxyPoolEntry)
			admin.POST("/proxy-pool/fetch", h.FetchProxyPool)
			admin.POST("/proxy-pool/test", h.ProbeProxyPool)
			admin.POST("/proxy-pool/allocate", h.AllocateProxyPoolPreview)

			admin.GET("/accounts", h.ListAccounts)
			admin.POST("/accounts", h.CreateAccount)
			admin.POST("/accounts/batch", h.BatchImportAccounts)
			admin.POST("/accounts/scan-local", h.ScanLocalAccounts)
			admin.GET("/accounts/export", h.ExportAccounts)
			admin.POST("/accounts/harvest-all", h.HarvestAllAccounts)
			admin.GET("/accounts/:id", h.GetAccount)
			admin.PUT("/accounts/:id", h.UpdateAccount)
			admin.DELETE("/accounts/:id", h.DeleteAccount)
			admin.POST("/accounts/:id/check", h.CheckAccount)
			admin.POST("/accounts/:id/balance", h.RefreshAccountBalance)
			admin.POST("/accounts/balance/refresh-all", h.RefreshAllAccountBalances)
			admin.POST("/accounts/:id/proxy-test", h.TestAccountProxy)
			admin.POST("/accounts/:id/rotate-device", h.RotateAccountDevice)
			admin.POST("/accounts/:id/login-link", h.IssueAccountLoginLink)
			admin.POST("/accounts/:id/refresh-token", h.RefreshAccountToken)
			admin.POST("/accounts/:id/harvest", h.HarvestAccount)

			// 前置系统提示词注入中心
			admin.GET("/prompts/config", h.GetPromptConfig)
			admin.PUT("/prompts/global", h.UpdateGlobalPrompt)
			admin.GET("/prompts/templates", h.ListPromptTemplates)
			admin.POST("/prompts/templates", h.CreatePromptTemplate)
			admin.POST("/prompts/templates/reset", h.ResetPromptTemplates)
			admin.PUT("/prompts/templates/:id", h.UpdatePromptTemplate)
			admin.DELETE("/prompts/templates/:id", h.DeletePromptTemplate)
			admin.PUT("/prompts/groups/:id", h.UpdateGroupPrompt)
			admin.POST("/prompts/preview", h.PreviewPromptInjection)

			// 内容风控与词库
			admin.GET("/filter-rules", h.ListFilterRules)
			admin.POST("/filter-rules", h.CreateFilterRule)
			admin.POST("/filter-rules/reset", h.ResetFilterRules)
			admin.POST("/filter-rules/preview", h.PreviewFilterRules)
			admin.PUT("/filter-rules/:id", h.UpdateFilterRule)
			admin.DELETE("/filter-rules/:id", h.DeleteFilterRule)

			// 工具名混淆（双向确定性 KV-Cache 友好混淆引擎 + 各分组覆盖）
			admin.GET("/tool-obfuscation", h.GetToolObfuscation)
			admin.PUT("/tool-obfuscation", h.UpdateToolObfuscation)
			admin.PUT("/tool-obfuscation/groups/:id", h.UpdateGroupToolObfuscation)
			admin.POST("/tool-obfuscation/rotate-salt", h.RotateToolObfuscationSalt)
			admin.POST("/tool-obfuscation/preview", h.PreviewToolObfuscation)

			// 非账号额度 429 会话暂停保护与请求体审计 Log
			admin.GET("/session-guard", h.GetSessionGuard)
			admin.PUT("/session-guard/config", h.UpdateSessionGuardConfig)
			admin.DELETE("/session-guard/sessions", h.ClearPausedSessions)
			admin.DELETE("/session-guard/sessions/:session_id", h.ResumePausedSession)
			admin.GET("/session-guard/logs/:filename", h.ReadSession429Log)

			// 统一领奖与保活中心
			admin.GET("/rewards/overview", h.GetRewardsOverview)
			admin.POST("/rewards/run", h.RunRewardsAction)

			// Cron 定时任务调度
			admin.GET("/cron-jobs", h.ListCronJobs)
			admin.POST("/cron-jobs", h.CreateCronJob)
			admin.POST("/cron-jobs/preview", h.PreviewCronExpr)
			admin.POST("/cron-jobs/reset", h.ResetCronJobs)
			admin.PUT("/cron-jobs/:id", h.UpdateCronJob)
			admin.DELETE("/cron-jobs/:id", h.DeleteCronJob)
			admin.POST("/cron-jobs/:id/run", h.RunCronJobNow)

			// 分组与倍率路由
			admin.GET("/groups", h.ListGroups)
			admin.POST("/groups", h.CreateGroup)
			admin.PUT("/groups/:id", h.UpdateGroup)
			admin.DELETE("/groups/:id", h.DeleteGroup)

			// 下游调用密钥
			admin.GET("/keys", h.ListAPIKeys)
			admin.POST("/keys", h.CreateAPIKey)
			admin.PUT("/keys/:id", h.UpdateAPIKey)
			admin.DELETE("/keys/:id", h.DeleteAPIKey)
			admin.POST("/keys/:id/reset-quota", h.ResetAPIKeyQuota)

			// 调用明细日志
			admin.GET("/logs", h.ListUsageLogs)

			// 模型广场与标准/自定义定价库
			admin.GET("/models", h.AdminListModels)
			admin.GET("/models/catalog", h.GetModelCatalog)
			admin.GET("/models/prices", h.ListModelPrices)
			admin.PUT("/models/prices", h.UpsertModelPrice)
			admin.DELETE("/models/prices/:id", h.DeleteModelPrice)

			// 系统与分级超时配置
			admin.GET("/settings", h.Settings)
			admin.PUT("/settings/runtime", h.UpdateRuntimeSettings)
		}
	}

	// ---- 下游 API（用 cmd2api 签发的密钥鉴权）----
	v1 := r.Group("/v1")
	v1.Use(middleware.APIKeyAuth(opts.Client))
	{
		v1.POST("/chat/completions", h.ChatCompletions)
		v1.POST("/messages", h.Messages)
		v1.POST("/responses", h.Responses)
		v1.GET("/models", h.ListModels)
	}

	attachStatic(r, opts.StaticDir)
	return r
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Next()
	}
}

// attachStatic 挂载前端静态资源，并为前端路由提供 SPA fallback。
// 带内容哈希的 /assets/* 启用 1 年 immutable 强缓存，SPA 入口 index.html 强制不缓存，保证升级即时生效。
func attachStatic(r *gin.Engine, dir string) {
	if dir == "" {
		return
	}
	indexPath := filepath.Join(dir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return
	}

	assetsGroup := r.Group("/assets")
	assetsGroup.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Next()
	})
	assetsGroup.Static("/", filepath.Join(dir, "assets"))

	r.NoRoute(func(c *gin.Context) {
		urlPath := c.Request.URL.Path
		if strings.HasPrefix(urlPath, "/api/") || strings.HasPrefix(urlPath, "/v1/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "接口不存在"})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}

		if rel, ok := safeStaticPath(dir, urlPath); ok {
			if info, err := os.Stat(rel); err == nil && !info.IsDir() {
				c.Header("Cache-Control", "public, max-age=86400")
				c.File(rel)
				return
			}
		}

		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.File(indexPath)
	})
}

func safeStaticPath(root, urlPath string) (string, bool) {
	cleaned := path.Clean("/" + urlPath)
	if cleaned == "/" || cleaned == "." {
		return "", false
	}
	rel := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	absRel, err := filepath.Abs(rel)
	if err != nil {
		return "", false
	}
	if absRel != absRoot && !strings.HasPrefix(absRel, absRoot+string(filepath.Separator)) {
		return "", false
	}
	return absRel, true
}
