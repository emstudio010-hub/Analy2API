package handler

import (
	"net/http"
	"strings"

	"cmd2api/internal/middleware"
	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

type updateProxyPoolRequest struct {
	service.ProxyPoolConfig
	RawProxies string `json:"raw_proxies,omitempty"`
	RawTag     string `json:"raw_tag,omitempty"`
}

type addProxyPoolEntriesRequest struct {
	RawText string   `json:"raw_text,omitempty"`
	Proxies []string `json:"proxies,omitempty"`
	Tag     string   `json:"tag,omitempty"`
	Note    string   `json:"note,omitempty"`
}

type fetchProxyPoolRequest struct {
	URL    string `json:"url,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

type probeProxyPoolRequest struct {
	EntryID  string `json:"entry_id,omitempty"`
	ProxyURL string `json:"proxy_url,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type allocateProxyPreviewRequest struct {
	AdminKeyID int64  `json:"admin_key_id,omitempty"`
	Platform   string `json:"platform,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
	Mode       string `json:"mode,omitempty"`
	FetchURL   string `json:"fetch_url,omitempty"`
}

type remotePushProxyUpdateRequest struct {
	// 1. 调整当前补号 Key 自身的默认代理策略
	Mode                string `json:"mode,omitempty"`
	FixedProxyURL       string `json:"fixed_proxy_url,omitempty"`
	ProxyURL            string `json:"proxy_url,omitempty"`
	Proxy               string `json:"proxy,omitempty"`
	IP                  string `json:"ip,omitempty"`
	PoolTag             string `json:"pool_tag,omitempty"`
	ProxyTag            string `json:"proxy_tag,omitempty"`
	Strategy            string `json:"strategy,omitempty"`
	ProxyStrategy       string `json:"proxy_strategy,omitempty"`
	FetchURLOverride    string `json:"fetch_url_override,omitempty"`
	FetchURL            string `json:"fetch_url,omitempty"`
	AllowOverrideInPush *bool  `json:"allow_override_in_push,omitempty"`

	// 2. 可选：直接给指定已入池账号更换/轮换代理 IP
	AccountID      int64  `json:"account_id,omitempty"`
	AccountName    string `json:"account_name,omitempty"`
	Email          string `json:"email,omitempty"`
	RotateFromPool bool   `json:"rotate_from_pool,omitempty"`
}

// GetProxyPool 返回全局代理 IP 池配置、条目列表（含实时绑定账号数）与统计摘要（GET /api/proxy-pool）。
func (h *Handler) GetProxyPool(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	view, err := svc.GetView(c.Request.Context())
	if err != nil {
		h.failInternal(c, "读取代理 IP 池失败", err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// UpdateProxyPool 更新全局代理 IP 池配置、轮询策略与远程拉取 API（PUT /api/proxy-pool）。
func (h *Handler) UpdateProxyPool(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	var req updateProxyPoolRequest
	if !bindJSON(c, &req) {
		return
	}
	view, err := svc.UpdateConfig(c.Request.Context(), req.ProxyPoolConfig, req.RawProxies, req.RawTag)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, view)
}

// AddProxyPoolEntries 批量导入代理 IP 条目到代理池（POST /api/proxy-pool/entries）。
func (h *Handler) AddProxyPoolEntries(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	var req addProxyPoolEntriesRequest
	if !bindJSON(c, &req) {
		return
	}
	view, added, err := svc.AddEntries(c.Request.Context(), req.RawText, req.Proxies, req.Tag, req.Note)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":    true,
		"added": added,
		"view":  view,
	})
}

// DeleteProxyPoolEntry 删除单条代理池条目（DELETE /api/proxy-pool/entries/:id）。
func (h *Handler) DeleteProxyPoolEntry(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	view, err := svc.DeleteEntry(c.Request.Context(), c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, err.Error())
		return
	}
	c.JSON(http.StatusOK, view)
}

// FetchProxyPool 立即从配置的远程代理商 API 拉取代理 IP 进池（POST /api/proxy-pool/fetch）。
func (h *Handler) FetchProxyPool(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	var req fetchProxyPoolRequest
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &req) {
			return
		}
	}
	res, err := svc.FetchFromRemote(c.Request.Context(), req.URL, req.DryRun)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"ok":     false,
			"error":  err.Error(),
			"result": res,
		})
		return
	}
	view, _ := svc.GetView(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{
		"ok":     true,
		"result": res,
		"view":   view,
	})
}

// ProbeProxyPool 探测单条代理或批量池内代理的连通性与真实出口 IP（POST /api/proxy-pool/test）。
func (h *Handler) ProbeProxyPool(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	var req probeProxyPoolRequest
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &req) {
			return
		}
	}
	results, err := svc.ProbeEntries(c.Request.Context(), req.EntryID, req.ProxyURL, req.Limit)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"results": results,
	})
}

// AllocateProxyPoolPreview 按当前策略试抽一条代理 IP（POST /api/proxy-pool/allocate）。
func (h *Handler) AllocateProxyPoolPreview(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	var req allocateProxyPreviewRequest
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &req) {
			return
		}
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = service.KeyProxyModePool
	}
	res, err := svc.AllocateForPush(c.Request.Context(), service.ProxyAllocationRequest{
		AdminKeyID:       req.AdminKeyID,
		Platform:         req.Platform,
		RequestMode:      mode,
		RequestTag:       req.Tag,
		RequestStrategy:  req.Strategy,
		FetchURLOverride: req.FetchURL,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":               true,
		"masked_proxy_url": res.MaskedProxyURL,
		"source":           res.Source,
		"tag":              res.Tag,
		"entry_id":         res.EntryID,
		"warning":          res.Warning,
	})
}

// GetRemotePushProxyConfig 供补号专用 Key / 管理员查询当前 Key 自身的代理策略及全局 IP 池脱敏摘要
// （GET /api/remote/accounts/push/proxy 及 GET /api/accounts/push/proxy）。
func (h *Handler) GetRemotePushProxyConfig(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	view, err := svc.GetView(c.Request.Context())
	if err != nil {
		h.failInternal(c, "读取代理 IP 池摘要失败", err)
		return
	}

	var keyID int64
	var keyName string
	if ak, ok := middleware.AdminKeyFrom(c); ok && ak != nil {
		keyID = ak.ID
		keyName = ak.Name
	}
	policy := svc.GetKeyPolicy(c.Request.Context(), keyID)

	c.JSON(http.StatusOK, gin.H{
		"ok":               true,
		"admin_key_id":     keyID,
		"admin_key_name":   keyName,
		"key_proxy_policy": policy,
		"pool_summary": gin.H{
			"enabled":             view.Config.Enabled,
			"strategy":            view.Config.Strategy,
			"max_accounts_per_ip": view.Config.MaxAccountsPerIP,
			"total_entries":       view.Stats.TotalEntries,
			"enabled_entries":     view.Stats.EnabledEntries,
			"bound_total":         view.Stats.BoundTotal,
			"tags":                view.Stats.Tags,
			"remote_fetch_ready":  view.Config.Fetch.Enabled && strings.TrimSpace(view.Config.Fetch.URL) != "",
			"last_fetched_at":     view.Config.Fetch.LastFetchedAt,
		},
	})
}

// UpdateRemotePushProxyConfig 允许远程补号 Key 随时调整：
//  1. 给已入池的某个账号更换/从池中轮换出口 IP（当提供 account_id / account_name / email 时）；
//  2. 调整当前补号 Key 自身的默认代理策略（fixed 固定 IP / pool 指定标签轮询 / fetch 实时拉取 / direct 直连 / inherit 跟随全局）。
//
// （PUT /api/remote/accounts/push/proxy 及 PUT /api/accounts/push/proxy）
func (h *Handler) UpdateRemotePushProxyConfig(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	var req remotePushProxyUpdateRequest
	if !bindJSON(c, &req) {
		return
	}

	var keyID int64
	if ak, ok := middleware.AdminKeyFrom(c); ok && ak != nil {
		keyID = ak.ID
	}

	// 分支 A：指定了目标账号 -> 调整该账号的代理 IP
	if req.AccountID > 0 || strings.TrimSpace(req.AccountName) != "" || strings.TrimSpace(req.Email) != "" {
		rawProxy := firstNonEmptyStr(req.FixedProxyURL, req.ProxyURL, req.Proxy, req.IP)
		mode := strings.TrimSpace(req.Mode)
		if req.RotateFromPool && mode == "" {
			mode = service.KeyProxyModePool
		}
		res, err := h.accounts.ReassignAccountProxy(c.Request.Context(), service.ReassignAccountProxyInput{
			AdminKeyID:       keyID,
			AccountID:        req.AccountID,
			AccountName:      req.AccountName,
			Email:            req.Email,
			ProxyRaw:         rawProxy,
			ProxyMode:        mode,
			ProxyTag:         firstNonEmptyStr(req.ProxyTag, req.PoolTag),
			ProxyStrategy:    firstNonEmptyStr(req.ProxyStrategy, req.Strategy),
			FetchURLOverride: firstNonEmptyStr(req.FetchURLOverride, req.FetchURL),
		})
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, res)
		return
	}

	// 分支 B：调整当前补号 Key 自身的默认代理策略
	if keyID <= 0 {
		fail(c, http.StatusBadRequest, "当前请求未使用数据库签发的远程补号/管理密钥；如需管理全局 IP 池请调用 PUT /api/proxy-pool")
		return
	}

	cur := svc.GetKeyPolicy(c.Request.Context(), keyID)
	next := cur
	fixedRaw := firstNonEmptyStr(req.FixedProxyURL, req.ProxyURL, req.Proxy, req.IP)
	if fixedRaw != "" {
		next.FixedProxyURL = fixedRaw
		if strings.TrimSpace(req.Mode) == "" {
			next.Mode = service.KeyProxyModeFixed
		}
	}
	if strings.TrimSpace(req.Mode) != "" {
		next.Mode = strings.TrimSpace(req.Mode)
	}
	if tag := firstNonEmptyStr(req.PoolTag, req.ProxyTag); tag != "" || strings.EqualFold(next.Mode, service.KeyProxyModeInherit) {
		next.PoolTag = tag
	}
	if st := firstNonEmptyStr(req.Strategy, req.ProxyStrategy); st != "" {
		next.Strategy = st
	}
	if fu := firstNonEmptyStr(req.FetchURLOverride, req.FetchURL); fu != "" {
		next.FetchURLOverride = fu
		if strings.TrimSpace(req.Mode) == "" && fixedRaw == "" {
			next.Mode = service.KeyProxyModeFetch
		}
	}
	if req.AllowOverrideInPush != nil {
		next.AllowOverrideInPush = req.AllowOverrideInPush
	}

	saved, err := svc.SetKeyPolicy(c.Request.Context(), keyID, next)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":               true,
		"admin_key_id":     keyID,
		"key_proxy_policy": saved,
	})
}

// FetchRemotePushProxyPool 允许补号专用 Key 按需触发一次远程拉取代理 IP 进池
// （POST /api/remote/accounts/push/proxy/fetch 及 POST /api/accounts/push/proxy/fetch）。
func (h *Handler) FetchRemotePushProxyPool(c *gin.Context) {
	svc := h.proxyPoolSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "代理 IP 池服务未初始化")
		return
	}
	var req fetchProxyPoolRequest
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &req) {
			return
		}
	}
	urlOverride := strings.TrimSpace(req.URL)
	if urlOverride == "" {
		if ak, ok := middleware.AdminKeyFrom(c); ok && ak != nil {
			kp := svc.GetKeyPolicy(c.Request.Context(), ak.ID)
			if kp.FetchURLOverride != "" {
				urlOverride = kp.FetchURLOverride
			}
		}
	}
	res, err := svc.FetchFromRemote(c.Request.Context(), urlOverride, req.DryRun)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"ok":     false,
			"error":  err.Error(),
			"result": res,
		})
		return
	}
	c.JSON(http.StatusOK, res)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
