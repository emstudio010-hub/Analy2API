// Package handler 实现管理后台的 HTTP 接口与下游 API 的中转入口。
package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/auth"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/middleware"
	"cmd2api/internal/prompt"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/service"
	"cmd2api/internal/toolobf"

	"github.com/gin-gonic/gin"
)

// Handler 汇总所有接口共用的依赖。
type Handler struct {
	client          *ent.Client
	vault           *crypto.Vault
	issuer          *auth.Issuer
	sched           *scheduler.Scheduler
	relay           *relay.Service
	accounts        *service.AccountService
	filters         *service.FilterService
	crons           *service.CronService
	pricing         *service.PricingService
	prompts         *service.PromptService
	adminKeys       *service.AdminKeyService
	antigravity     *service.AntigravityService
	rewards         *service.RewardsService
	runtimeSettings *service.RuntimeSettingsService
	toolObf         *service.ToolObfuscationService
	sessionGuard    *service.SessionGuardService
	oauth           *service.OAuthService
	proxyPool       *service.ProxyPoolService
	cfg             *config.Config
	logger          *slog.Logger

	lazyMu sync.Mutex
}

// New 构造 Handler。
//
// relayService 允许为 nil：管理后台的接口不依赖它，这样只跑后台、
// 或者上游还没配置好的情况下服务也能起来并给出可读的报错。
func New(
	client *ent.Client,
	vault *crypto.Vault,
	issuer *auth.Issuer,
	sched *scheduler.Scheduler,
	relayService *relay.Service,
	accounts *service.AccountService,
	cfg *config.Config,
	logger *slog.Logger,
	filters ...*service.FilterService,
) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var filterSvc *service.FilterService
	if len(filters) > 0 {
		filterSvc = filters[0]
	}
	return &Handler{
		client:   client,
		vault:    vault,
		issuer:   issuer,
		sched:    sched,
		relay:    relayService,
		accounts: accounts,
		filters:  filterSvc,
		cfg:      cfg,
		logger:   logger,
	}
}

// SetCronService 注入 Cron 定时任务调度服务。
func (h *Handler) SetCronService(crons *service.CronService) {
	h.lazyMu.Lock()
	h.crons = crons
	h.lazyMu.Unlock()
}

// SetPricingService 注入模型定价与自定义覆盖服务。
func (h *Handler) SetPricingService(pricing *service.PricingService) {
	h.lazyMu.Lock()
	h.pricing = pricing
	h.lazyMu.Unlock()
}

// SetPromptService 注入前置系统提示词管理与预览服务。
func (h *Handler) SetPromptService(prompts *service.PromptService) {
	h.lazyMu.Lock()
	h.prompts = prompts
	h.lazyMu.Unlock()
}

// SetAdminKeyService 注入远程管理密钥服务。
func (h *Handler) SetAdminKeyService(adminKeys *service.AdminKeyService) {
	h.lazyMu.Lock()
	h.adminKeys = adminKeys
	h.lazyMu.Unlock()
}

// SetAntigravityService 注入 AntiGravity 容器与登录凭证管理服务。
func (h *Handler) SetAntigravityService(agy *service.AntigravityService) {
	h.lazyMu.Lock()
	h.antigravity = agy
	h.lazyMu.Unlock()
}

// SetRewardsService 注入四平台统一领奖与保活服务。
func (h *Handler) SetRewardsService(rewards *service.RewardsService) {
	h.lazyMu.Lock()
	h.rewards = rewards
	h.lazyMu.Unlock()
}

// SetRuntimeSettingsService 注入运行态配置与分级超时热更新服务。
func (h *Handler) SetRuntimeSettingsService(rs *service.RuntimeSettingsService) {
	h.lazyMu.Lock()
	h.runtimeSettings = rs
	h.lazyMu.Unlock()
}

// SetToolObfuscationService 注入双向工具名混淆服务。
func (h *Handler) SetToolObfuscationService(to *service.ToolObfuscationService) {
	h.lazyMu.Lock()
	h.toolObf = to
	h.lazyMu.Unlock()
}

// SetSessionGuardService 注入非额度 429 会话暂停保护服务。
func (h *Handler) SetSessionGuardService(sg *service.SessionGuardService) {
	h.lazyMu.Lock()
	h.sessionGuard = sg
	h.lazyMu.Unlock()
}

// SetOAuthService 注入跨平台统一 OAuth 会话与回调闭环服务。
func (h *Handler) SetOAuthService(oauth *service.OAuthService) {
	h.lazyMu.Lock()
	h.oauth = oauth
	h.lazyMu.Unlock()
}

// SetProxyPoolService 注入代理 IP 池与远程补号自动分配服务。
func (h *Handler) SetProxyPoolService(pp *service.ProxyPoolService) {
	h.lazyMu.Lock()
	h.proxyPool = pp
	if h.accounts != nil && pp != nil {
		h.accounts.SetProxyPoolService(pp)
	}
	h.lazyMu.Unlock()
}

func (h *Handler) pricingSvc() *service.PricingService {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.pricing == nil && h.client != nil {
		h.pricing = service.NewPricingService(h.client, h.logger)
	}
	return h.pricing
}

func (h *Handler) promptSvc() *service.PromptService {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.prompts == nil && h.client != nil {
		var pe *prompt.Engine
		if h.relay != nil && h.relay.PromptEngine() != nil {
			pe = h.relay.PromptEngine()
		} else {
			pe = prompt.NewDefaultEngine()
			if h.relay != nil {
				h.relay.SetPromptEngine(pe)
			}
		}
		var fe *filter.Engine
		if h.filters != nil {
			fe = h.filters.Engine()
		}
		h.prompts = service.NewPromptService(h.client, pe, fe, h.logger)
	}
	return h.prompts
}

func (h *Handler) adminKeySvc() *service.AdminKeyService {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.adminKeys == nil && h.client != nil {
		h.adminKeys = service.NewAdminKeyService(h.client)
	}
	return h.adminKeys
}

func (h *Handler) antigravitySvc() *service.AntigravityService {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.antigravity == nil && h.client != nil {
		dataRoot := "./data/antigravity"
		defaultImg := antigravity.DefaultDockerImage
		if h.cfg != nil {
			if h.cfg.AntigravityDataDir != "" {
				dataRoot = h.cfg.AntigravityDataDir
			} else if h.cfg.CommandCC.AntigravityDataDir != "" {
				dataRoot = h.cfg.CommandCC.AntigravityDataDir
			}
			if h.cfg.AntigravityDefaultImage != "" {
				defaultImg = h.cfg.AntigravityDefaultImage
			} else if h.cfg.CommandCC.AntigravityDefaultImage != "" {
				defaultImg = h.cfg.CommandCC.AntigravityDefaultImage
			}
		}
		var agCli *antigravity.Client
		if h.relay != nil && h.relay.AntigravityClient() != nil {
			agCli = h.relay.AntigravityClient()
		}
		h.antigravity = service.NewAntigravityService(h.client, h.vault, agCli, nil, dataRoot, defaultImg, h.logger)
	}
	return h.antigravity
}

func (h *Handler) rewardsSvc() *service.RewardsService {
	agy := h.antigravitySvc()
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.rewards == nil && h.client != nil {
		h.rewards = service.NewRewardsService(h.client, h.accounts, agy, h.logger)
	}
	return h.rewards
}

func (h *Handler) runtimeSettingsSvc() *service.RuntimeSettingsService {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.runtimeSettings == nil && h.client != nil {
		h.runtimeSettings = service.NewRuntimeSettingsService(h.client, h.logger)
	}
	return h.runtimeSettings
}

func (h *Handler) toolObfSvc() *service.ToolObfuscationService {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.toolObf == nil && h.client != nil {
		var eng *toolobf.Engine
		if h.relay != nil && h.relay.ToolObfuscator() != nil {
			eng = h.relay.ToolObfuscator()
		} else {
			eng = toolobf.NewDefaultEngine()
			if h.relay != nil {
				h.relay.SetToolObfuscator(eng)
			}
		}
		h.toolObf = service.NewToolObfuscationService(h.client, eng, h.logger)
	}
	return h.toolObf
}

func (h *Handler) sessionGuardSvc() *service.SessionGuardService {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.sessionGuard == nil && h.client != nil {
		var sg *relay.SessionGuard
		if h.relay != nil && h.relay.SessionGuard() != nil {
			sg = h.relay.SessionGuard()
		} else {
			sg = relay.NewDefaultSessionGuard()
			if h.relay != nil {
				h.relay.SetSessionGuard(sg)
			}
		}
		h.sessionGuard = service.NewSessionGuardService(h.client, sg, h.logger)
	}
	return h.sessionGuard
}

func (h *Handler) oauthSvc() *service.OAuthService {
	agy := h.antigravitySvc()
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.oauth == nil && h.client != nil {
		var relayCli *relay.Client
		if h.accounts != nil && h.relay != nil {
			// relay.Client 已注入到 accountService 内
			relayCli = nil
		}
		h.oauth = service.NewOAuthService(h.client, h.accounts, agy, relayCli, h.logger)
	}
	return h.oauth
}

func (h *Handler) proxyPoolSvc() *service.ProxyPoolService {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.proxyPool == nil {
		if h.accounts != nil && h.accounts.ProxyPoolService() != nil {
			h.proxyPool = h.accounts.ProxyPoolService()
		} else if h.client != nil {
			h.proxyPool = service.NewProxyPoolService(h.client, h.logger)
		}
	}
	if h.proxyPool != nil && h.accounts != nil && h.accounts.ProxyPoolService() == nil {
		h.accounts.SetProxyPoolService(h.proxyPool)
	}
	return h.proxyPool
}

// ---- 通用响应 helper ----

// fail 返回统一格式的错误。
func fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": msg})
}

// failInternal 记录真实错误，但只把笼统信息返回给调用方。
//
// 不把内部错误直接透出去：数据库报错里常带表名、约束名甚至连接串片段，
// 这些对排查有用，对攻击者同样有用。
func (h *Handler) failInternal(c *gin.Context, msg string, err error) {
	h.logger.Error(msg, "err", err, "path", c.FullPath(), "method", c.Request.Method)
	fail(c, http.StatusInternalServerError, msg)
}

// bindJSON 解析请求体，失败时直接写回 400。
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		fail(c, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return false
	}
	return true
}

// pathID 取出路径里的 :id 并转成 int64。
func pathID(c *gin.Context) (int64, bool) {
	raw := c.Param("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "路径参数 id 无效")
		return 0, false
	}
	return id, true
}

// queryInt 读取查询参数里的整数，缺省时用 def。
func queryInt(c *gin.Context, name string, def int) int {
	if v := c.Query(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// pageParams 归一化分页参数，把越界值夹到合理范围。
func pageParams(c *gin.Context) (limit, offset int) {
	const (
		defaultSize = 50
		maxSize     = 200
	)
	page := queryInt(c, "page", 1)
	if page < 1 {
		page = 1
	}
	size := queryInt(c, "page_size", defaultSize)
	if size < 1 {
		size = defaultSize
	}
	if size > maxSize {
		size = maxSize
	}
	return size, (page - 1) * size
}

// timePtr 把可空时间转成 JSON 友好的形式。
func timePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// platformLabel 返回平台的展示名，用于自动生成账号名与统计排行标签。
func platformLabel(platform string) string {
	switch domain.NormalizePlatform(platform) {
	case domain.PlatformWorkBuddy:
		return "WorkBuddy"
	case domain.PlatformAntigravity:
		return "AntiGravity"
	case domain.PlatformOpenCode:
		return "OpenCode"
	case domain.PlatformCommandCode:
		return "Command Code"
	default:
		return platform
	}
}

// adminClaims 取出当前登录声明，未登录时直接写回 401。
func adminClaims(c *gin.Context) (*auth.Claims, bool) {
	claims, ok := middleware.AdminClaimsFrom(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "未登录")
		return nil, false
	}
	return claims, true
}

// isNotFound 判断 ent 是否返回了「记录不存在」。
func isNotFound(err error) bool {
	return ent.IsNotFound(err)
}

// isConstraint 判断是否命中唯一约束冲突。
func isConstraint(err error) bool {
	return ent.IsConstraintError(err)
}

// errNotFound 是内部用的哨兵，便于 handler 层统一处理。
var errNotFound = errors.New("记录不存在")
