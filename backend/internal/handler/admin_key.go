package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/internal/middleware"
	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

type adminKeyDTO struct {
	ID          int64                        `json:"id"`
	Name        string                       `json:"name"`
	KeyPrefix   string                       `json:"key_prefix"`
	MaskedKey   string                       `json:"masked_key"`
	Scopes      string                       `json:"scopes"`
	AllowedIPs  string                       `json:"allowed_ips"`
	Status      string                       `json:"status"`
	LastUsedAt  *string                      `json:"last_used_at"`
	ExpiresAt   *string                      `json:"expires_at"`
	CreatedAt   time.Time                    `json:"created_at"`
	UpdatedAt   time.Time                    `json:"updated_at"`
	ProxyPolicy *service.AdminKeyProxyPolicy `json:"proxy_policy,omitempty"`
}

func toAdminKeyDTO(row *ent.AdminKey, policy ...service.AdminKeyProxyPolicy) adminKeyDTO {
	dto := adminKeyDTO{
		ID:         row.ID,
		Name:       row.Name,
		KeyPrefix:  row.KeyPrefix,
		MaskedKey:  row.KeyPrefix + "****",
		Scopes:     row.Scopes,
		AllowedIPs: row.AllowedIps,
		Status:     row.Status,
		LastUsedAt: timePtr(row.LastUsedAt),
		ExpiresAt:  timePtr(row.ExpiresAt),
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
	if len(policy) > 0 {
		p := policy[0]
		dto.ProxyPolicy = &p
	}
	return dto
}

func (h *Handler) deriveConnectionBaseURL(c *gin.Context) string {
	if h.cfg != nil && strings.TrimSpace(h.cfg.Server.PublicBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(h.cfg.Server.PublicBaseURL), "/")
	}
	scheme := "http"
	if proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if c.Request != nil && c.Request.TLS != nil {
		scheme = "https"
	}
	host := ""
	if c.Request != nil {
		host = strings.TrimSpace(c.Request.Host)
	}
	if host == "" {
		host = "127.0.0.1:8080"
	}
	return scheme + "://" + host
}

// ListAdminKeys 返回全部远程管理密钥及当前节点连接指引（GET /api/admin-keys）。
func (h *Handler) ListAdminKeys(c *gin.Context) {
	svc := h.adminKeySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "管理密钥服务未初始化")
		return
	}

	rows, err := svc.List(c.Request.Context())
	if err != nil {
		h.failInternal(c, "查询远程管理密钥列表失败", err)
		return
	}

	var policies map[int64]service.AdminKeyProxyPolicy
	if pp := h.proxyPoolSvc(); pp != nil {
		policies = pp.GetAllKeyPolicies(c.Request.Context())
	}

	items := make([]adminKeyDTO, 0, len(rows))
	for _, r := range rows {
		if p, ok := policies[r.ID]; ok {
			items = append(items, toAdminKeyDTO(r, p))
		} else if pp := h.proxyPoolSvc(); pp != nil {
			items = append(items, toAdminKeyDTO(r, pp.GetKeyPolicy(c.Request.Context(), r.ID)))
		} else {
			items = append(items, toAdminKeyDTO(r))
		}
	}

	baseURL := h.deriveConnectionBaseURL(c)
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
		"connection": gin.H{
			"base_url":          baseURL,
			"push_accounts_url": baseURL + "/api/remote/accounts/push",
			"push_proxy_url":    baseURL + "/api/remote/accounts/push/proxy",
			"headers": gin.H{
				"X-Management-Key": "adm-<your_secret_key>",
				"X-Push-Key":       "adm-<your_secret_key>",
				"Authorization":    "Bearer adm-<your_secret_key>",
			},
		},
	})
}

type createAdminKeyRequest struct {
	Name          string                       `json:"name"`
	Scopes        string                       `json:"scopes"`
	AllowedIPs    string                       `json:"allowed_ips"`
	ExpiresInDays int                          `json:"expires_in_days"`
	ExpiresAt     *string                      `json:"expires_at"`
	Status        string                       `json:"status"`
	ProxyPolicy   *service.AdminKeyProxyPolicy `json:"proxy_policy,omitempty"`
}

// CreateAdminKey 签发新的远程管理密钥并一次性返回明文（POST /api/admin-keys）。
func (h *Handler) CreateAdminKey(c *gin.Context) {
	svc := h.adminKeySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "管理密钥服务未初始化")
		return
	}

	var req createAdminKeyRequest
	if !bindJSON(c, &req) {
		return
	}

	var expPtr *time.Time
	if req.ExpiresAt != nil && strings.TrimSpace(*req.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*req.ExpiresAt))
		if err != nil {
			fail(c, http.StatusBadRequest, "expires_at 需为合法 RFC3339 时间字符串")
			return
		}
		expPtr = &parsed
	}

	plainKey, row, err := svc.CreateWithInput(c.Request.Context(), service.CreateAdminKeyInput{
		Name:          req.Name,
		Scopes:        req.Scopes,
		AllowedIPs:    req.AllowedIPs,
		ExpiresInDays: req.ExpiresInDays,
		ExpiresAt:     expPtr,
		Status:        req.Status,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	var dto adminKeyDTO
	if pp := h.proxyPoolSvc(); pp != nil {
		if req.ProxyPolicy != nil {
			savedPolicy, pErr := pp.SetKeyPolicy(c.Request.Context(), row.ID, *req.ProxyPolicy)
			if pErr != nil {
				// 代理策略不合法时回滚刚建的密钥避免产生半成品
				_ = svc.Delete(c.Request.Context(), row.ID)
				fail(c, http.StatusBadRequest, pErr.Error())
				return
			}
			dto = toAdminKeyDTO(row, savedPolicy)
		} else {
			dto = toAdminKeyDTO(row, pp.GetKeyPolicy(c.Request.Context(), row.ID))
		}
	} else {
		dto = toAdminKeyDTO(row)
	}

	c.JSON(http.StatusCreated, gin.H{
		"key":  plainKey,
		"item": dto,
	})
}

type updateAdminKeyRequest struct {
	Name        *string                      `json:"name"`
	Scopes      *string                      `json:"scopes"`
	AllowedIPs  *string                      `json:"allowed_ips"`
	ExpiresAt   *string                      `json:"expires_at"`
	ClearExpiry bool                         `json:"clear_expiry"`
	Status      *string                      `json:"status"`
	ProxyPolicy *service.AdminKeyProxyPolicy `json:"proxy_policy,omitempty"`
}

// UpdateAdminKey 更新远程管理密钥权限、IP 白名单、有效期、代理分配策略或状态（PUT /api/admin-keys/:id）。
func (h *Handler) UpdateAdminKey(c *gin.Context) {
	svc := h.adminKeySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "管理密钥服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req updateAdminKeyRequest
	if !bindJSON(c, &req) {
		return
	}

	var expPtr *time.Time
	clearExpiry := req.ClearExpiry
	if req.ExpiresAt != nil {
		trimmed := strings.TrimSpace(*req.ExpiresAt)
		if trimmed == "" {
			clearExpiry = true
		} else {
			parsed, err := time.Parse(time.RFC3339, trimmed)
			if err != nil {
				fail(c, http.StatusBadRequest, "expires_at 需为合法 RFC3339 时间字符串")
				return
			}
			expPtr = &parsed
		}
	}

	updated, err := svc.Update(c.Request.Context(), id, service.UpdateAdminKeyInput{
		Name:        req.Name,
		Scopes:      req.Scopes,
		AllowedIPs:  req.AllowedIPs,
		Status:      req.Status,
		ExpiresAt:   expPtr,
		ClearExpiry: clearExpiry,
	})
	if err != nil {
		if errors.Is(err, service.ErrAdminKeyNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	var dto adminKeyDTO
	if pp := h.proxyPoolSvc(); pp != nil {
		if req.ProxyPolicy != nil {
			savedPolicy, pErr := pp.SetKeyPolicy(c.Request.Context(), id, *req.ProxyPolicy)
			if pErr != nil {
				fail(c, http.StatusBadRequest, pErr.Error())
				return
			}
			dto = toAdminKeyDTO(updated, savedPolicy)
		} else {
			dto = toAdminKeyDTO(updated, pp.GetKeyPolicy(c.Request.Context(), id))
		}
	} else {
		dto = toAdminKeyDTO(updated)
	}

	middleware.InvalidateAdminKeyCache(h.client)
	c.JSON(http.StatusOK, dto)
}

// DeleteAdminKey 吊销并删除远程管理密钥（DELETE /api/admin-keys/:id）。
func (h *Handler) DeleteAdminKey(c *gin.Context) {
	svc := h.adminKeySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "管理密钥服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	if err := svc.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrAdminKeyNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		h.failInternal(c, "删除管理密钥失败", err)
		return
	}
	if pp := h.proxyPoolSvc(); pp != nil {
		pp.DeleteKeyPolicy(c.Request.Context(), id)
	}

	middleware.InvalidateAdminKeyCache(h.client)
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "已删除"})
}
