package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"cmd2api/internal/middleware"
	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

type remotePushEnvelope struct {
	Platform      string                          `json:"platform,omitempty"`
	GroupID       int64                           `json:"group_id,omitempty"`
	GroupIDs      []int64                         `json:"group_ids,omitempty"`
	GroupName     string                          `json:"group_name,omitempty"`
	ProxyURL      string                          `json:"proxy_url,omitempty"`
	Proxy         string                          `json:"proxy,omitempty"`
	IP            string                          `json:"ip,omitempty"`
	ProxyIP       string                          `json:"proxy_ip,omitempty"`
	ProxyList     []string                        `json:"proxy_list,omitempty"`
	IPs           []string                        `json:"ips,omitempty"`
	ProxyMode     string                          `json:"proxy_mode,omitempty"`
	ProxyTag      string                          `json:"proxy_tag,omitempty"`
	ProxyStrategy string                          `json:"proxy_strategy,omitempty"`
	ProxyFetchURL string                          `json:"proxy_fetch_url,omitempty"`
	AccountMode   string                          `json:"account_mode,omitempty"`
	Upsert        *bool                           `json:"upsert,omitempty"`
	AutoProvision *bool                           `json:"auto_provision,omitempty"`
	Accounts      []service.RemotePushAccountItem `json:"accounts,omitempty"`
	Items         []service.RemotePushAccountItem `json:"items,omitempty"`
	RawText       string                          `json:"raw_text,omitempty"`
}

// GetRemotePushInfo 返回远程补号接口的连通性状态、支持平台、活跃分组清单及代理 IP 池/当前 Key 策略摘要
// （GET /api/remote/accounts/push/info 及 GET /api/accounts/push/info）。
func (h *Handler) GetRemotePushInfo(c *gin.Context) {
	info, err := h.accounts.GetRemotePushInfo(c.Request.Context())
	if err != nil {
		h.failInternal(c, "查询远程推号元信息失败", err)
		return
	}
	baseURL := h.deriveConnectionBaseURL(c)
	resp := gin.H{
		"ok":                  info.OK,
		"service":             info.Service,
		"push_url":            baseURL + "/api/remote/accounts/push",
		"push_proxy_url":      baseURL + "/api/remote/accounts/push/proxy",
		"supported_platforms": info.SupportedPlatforms,
		"groups":              info.Groups,
	}
	if pp := h.proxyPoolSvc(); pp != nil {
		if view, vErr := pp.GetView(c.Request.Context()); vErr == nil && view != nil {
			resp["proxy_pool"] = gin.H{
				"enabled":             view.Config.Enabled,
				"strategy":            view.Config.Strategy,
				"max_accounts_per_ip": view.Config.MaxAccountsPerIP,
				"enabled_entries":     view.Stats.EnabledEntries,
				"tags":                view.Stats.Tags,
				"remote_fetch_ready":  view.Config.Fetch.Enabled && strings.TrimSpace(view.Config.Fetch.URL) != "",
			}
		}
		if ak, ok := middleware.AdminKeyFrom(c); ok && ak != nil {
			resp["key_proxy_policy"] = pp.GetKeyPolicy(c.Request.Context(), ak.ID)
		}
	}
	c.JSON(http.StatusOK, resp)
}

// RemotePushAccounts 远程推送补号接口（POST /api/remote/accounts/push 及 POST /api/accounts/push）：
// 拿着节点网址 + 管理员 Key（full 或专用 push_accounts 权限）即可单条或批量补号进池：
//   - 支持单账号 JSON 对象：{ "platform": "antigravity", "email": "...", "refresh_token": "...", ... }
//   - 支持批量数组信封：{ "platform": "workbuddy", "group_name": "prod", "proxy_list": [...], "accounts": [ ... ] }
//   - 支持直接裸 JSON 数组：[ { ... }, { ... } ]
//   - 支持原始 Command Code auth.json / 多行 `名称,user_xxx` 文本：{ "raw_text": "..." }
//   - 代理 IP 自动按「单条传参 -> 批量 proxy_list/proxy_url -> Key 专属策略 -> 全局 IP 池轮询/自动远程拉取」分配。
func (h *Handler) RemotePushAccounts(c *gin.Context) {
	// 确保 ProxyPoolService 已懒加载并绑定至 AccountService
	_ = h.proxyPoolSvc()

	rawBody, err := io.ReadAll(io.LimitReader(c.Request.Body, 8<<20))
	if err != nil {
		fail(c, http.StatusBadRequest, "读取请求体失败")
		return
	}
	trimmed := bytes.TrimSpace(rawBody)
	if len(trimmed) == 0 {
		fail(c, http.StatusBadRequest, "请求体不能为空")
		return
	}

	var adminKeyID int64
	if ak, ok := middleware.AdminKeyFrom(c); ok && ak != nil {
		adminKeyID = ak.ID
	}

	input := service.RemotePushAccountsInput{
		AdminKeyID:           adminKeyID,
		DefaultProxyURL:      firstNonEmptyStr(c.Query("proxy_url"), c.Query("proxy"), c.Query("ip")),
		DefaultProxyMode:     strings.TrimSpace(c.Query("proxy_mode")),
		DefaultProxyTag:      strings.TrimSpace(c.Query("proxy_tag")),
		DefaultProxyStrategy: strings.TrimSpace(c.Query("proxy_strategy")),
		DefaultProxyFetchURL: strings.TrimSpace(c.Query("proxy_fetch_url")),
		DefaultUpsert:        true,
		DefaultAutoProvision: true,
	}

	if bytes.HasPrefix(trimmed, []byte("[")) {
		var list []service.RemotePushAccountItem
		if err := json.Unmarshal(trimmed, &list); err != nil {
			fail(c, http.StatusBadRequest, "解析账号数组 JSON 失败: "+err.Error())
			return
		}
		input.Items = list
	} else {
		var env remotePushEnvelope
		if err := json.Unmarshal(trimmed, &env); err != nil {
			fail(c, http.StatusBadRequest, "请求体必须是合法 JSON: "+err.Error())
			return
		}
		input.DefaultPlatform = strings.TrimSpace(env.Platform)
		input.DefaultGroupName = strings.TrimSpace(env.GroupName)
		input.DefaultProxyURL = firstNonEmptyStr(env.ProxyURL, env.Proxy, env.IP, env.ProxyIP, input.DefaultProxyURL)
		if len(env.ProxyList) > 0 {
			input.DefaultProxyList = append(input.DefaultProxyList, env.ProxyList...)
		} else if len(env.IPs) > 0 {
			input.DefaultProxyList = append(input.DefaultProxyList, env.IPs...)
		}
		input.DefaultProxyMode = firstNonEmptyStr(env.ProxyMode, input.DefaultProxyMode)
		input.DefaultProxyTag = firstNonEmptyStr(env.ProxyTag, input.DefaultProxyTag)
		input.DefaultProxyStrategy = firstNonEmptyStr(env.ProxyStrategy, input.DefaultProxyStrategy)
		input.DefaultProxyFetchURL = firstNonEmptyStr(env.ProxyFetchURL, input.DefaultProxyFetchURL)
		input.DefaultAccountMode = strings.TrimSpace(env.AccountMode)
		if len(env.GroupIDs) > 0 {
			input.DefaultGroupIDs = append(input.DefaultGroupIDs, env.GroupIDs...)
		}
		if env.GroupID > 0 {
			input.DefaultGroupIDs = append(input.DefaultGroupIDs, env.GroupID)
		}
		if env.Upsert != nil {
			input.DefaultUpsert = *env.Upsert
		}
		if env.AutoProvision != nil {
			input.DefaultAutoProvision = *env.AutoProvision
		}

		switch {
		case len(env.Accounts) > 0:
			input.Items = env.Accounts
		case len(env.Items) > 0:
			input.Items = env.Items
		case strings.TrimSpace(env.RawText) != "":
			parsed := parseKeyLines(env.RawText)
			for _, p := range parsed {
				if strings.TrimSpace(p.Key) == "" {
					continue
				}
				input.Items = append(input.Items, service.RemotePushAccountItem{
					Platform: input.DefaultPlatform,
					Name:     p.Name,
					APIKey:   p.Key,
				})
			}
			if len(input.Items) == 0 {
				fail(c, http.StatusBadRequest, "raw_text 中未解析到有效账号密钥")
				return
			}
		default:
			var single service.RemotePushAccountItem
			if err := json.Unmarshal(trimmed, &single); err != nil {
				fail(c, http.StatusBadRequest, "解析单条账号 JSON 失败: "+err.Error())
				return
			}
			input.Items = []service.RemotePushAccountItem{single}
		}
	}

	result, err := h.accounts.PushAccounts(c.Request.Context(), input)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	statusCode := http.StatusOK
	if result.Created > 0 && result.Failed == 0 {
		statusCode = http.StatusCreated
	}
	c.JSON(statusCode, result)
}
