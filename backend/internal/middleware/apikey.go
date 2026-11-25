package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/apikey"
	"cmd2api/internal/domain"

	"github.com/gin-gonic/gin"
)

// ctxKeyAPIKey 是校验通过的下游密钥在 gin.Context 里的键名。
const (
	ctxKeyAPIKey       = "cmd2api.api_key"
	apiKeyCacheTTL     = 5 * time.Second
	apiKeyTouchBackoff = 30 * time.Second
)

type cachedAPIKeyEntry struct {
	key       *ent.APIKey
	expiresAt time.Time
	gen       uint64
}

var (
	apiKeyTouchMu   sync.Mutex
	apiKeyLastTouch = make(map[*ent.Client]map[int64]time.Time)

	apiKeyGenMu sync.RWMutex
	apiKeyGens  = make(map[*ent.Client]*atomic.Uint64)
)

func apiKeyGenCounter(client *ent.Client) *atomic.Uint64 {
	apiKeyGenMu.RLock()
	g, ok := apiKeyGens[client]
	apiKeyGenMu.RUnlock()
	if ok {
		return g
	}
	apiKeyGenMu.Lock()
	defer apiKeyGenMu.Unlock()
	if g, ok = apiKeyGens[client]; ok {
		return g
	}
	g = &atomic.Uint64{}
	apiKeyGens[client] = g
	return g
}

// InvalidateAPIKeyCache 立即使指定 DB 实例上的 APIKeyAuth 鉴权缓存失效。
func InvalidateAPIKeyCache(client *ent.Client) {
	if client == nil {
		return
	}
	apiKeyGenCounter(client).Add(1)
}

// APIKeyAuth 校验下游客户端带来的 cmd2api 密钥（带 5s 内存 TTL 缓存与 O(1) 变更失效）。
func APIKeyAuth(client *ent.Client) gin.HandlerFunc {
	var (
		cacheMu sync.RWMutex
		cache   = make(map[string]cachedAPIKeyEntry)
	)

	return func(c *gin.Context) {
		raw := clientAPIKey(c)
		if raw == "" {
			// Anthropic 客户端拿到 401 会重试，所以错误体按 Anthropic 的
			// 结构返回，让 Claude Code 能正常显示错误而不是解析失败。
			abortWithError(c, http.StatusUnauthorized, "authentication_error", "缺少 API Key")
			return
		}

		now := time.Now()
		curGen := apiKeyGenCounter(client).Load()
		var key *ent.APIKey

		cacheMu.RLock()
		if entry, ok := cache[raw]; ok && entry.gen == curGen && now.Before(entry.expiresAt) {
			key = entry.key
		}
		cacheMu.RUnlock()

		if key == nil {
			var err error
			key, err = client.APIKey.Query().
				Where(
					apikey.KeyEQ(raw),
					apikey.DeletedAtIsNil(),
				).
				WithUser().
				WithGroup().
				Only(c.Request.Context())
			if err != nil {
				abortWithError(c, http.StatusUnauthorized, "authentication_error", "API Key 无效")
				return
			}
			// 仅对无配额上限（Quota <= 0）的 Key 缓存 5s；有配额上限的 Key 实时查 DB 以防超额穿透。
			if key.Quota <= 0 {
				cacheMu.Lock()
				if len(cache) > 4096 {
					cache = make(map[string]cachedAPIKeyEntry)
				}
				cache[raw] = cachedAPIKeyEntry{
					key:       key,
					expiresAt: now.Add(apiKeyCacheTTL),
					gen:       curGen,
				}
				cacheMu.Unlock()
			}
		}

		if key.Status != domain.StatusActive {
			abortWithError(c, http.StatusForbidden, "permission_error", "API Key 已被禁用")
			return
		}
		if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now()) {
			abortWithError(c, http.StatusForbidden, "permission_error", "API Key 已过期")
			return
		}
		if key.Edges.User != nil && key.Edges.User.Status != domain.StatusActive {
			abortWithError(c, http.StatusForbidden, "permission_error", "账号已被禁用")
			return
		}
		if key.Quota > 0 && key.QuotaUsed >= key.Quota {
			abortWithError(c, http.StatusForbidden, "permission_error", "API Key 额度已用尽")
			return
		}
		if !ipAllowed(c.ClientIP(), key.IPWhitelist) {
			abortWithError(c, http.StatusForbidden, "permission_error", "来源 IP 不在白名单内")
			return
		}
		if key.GroupID == nil {
			// 没有绑分组就没法决定去哪个账号池取号，属于配置缺失，
			// 不是客户端的错，所以返回 500 并说清原因。
			abortWithError(c, http.StatusInternalServerError, "api_error", "该 API Key 未绑定分组，请在后台配置")
			return
		}
		if key.Edges.Group != nil && key.Edges.Group.Status != domain.StatusActive {
			abortWithError(c, http.StatusForbidden, "permission_error", "API Key 所属分组已停用")
			return
		}

		c.Set(ctxKeyAPIKey, key)
		c.Next()
	}
}

// APIKeyFrom 取出当前请求的密钥实体。
func APIKeyFrom(c *gin.Context) (*ent.APIKey, bool) {
	v, ok := c.Get(ctxKeyAPIKey)
	if !ok {
		return nil, false
	}
	key, ok := v.(*ent.APIKey)
	return key, ok
}

// clientAPIKey 从请求头里提取下游密钥。
func clientAPIKey(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader("x-api-key")); v != "" {
		return v
	}
	h := c.GetHeader("Authorization")
	if h == "" {
		return ""
	}
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(h)
}

// ipAllowed 判断来源 IP 是否命中白名单。白名单为空表示不限制。
//
// 支持单个 IP 和 CIDR 两种写法，另外 ::ffff:1.2.3.4 这类 IPv4-mapped IPv6
// 需要归一化后再比对，否则明明配了 1.2.3.4 却匹配不上。
func ipAllowed(clientIP string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	for _, entry := range whitelist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil {
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		if allowed := net.ParseIP(entry); allowed != nil {
			if a4 := allowed.To4(); a4 != nil {
				allowed = a4
			}
			if allowed.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// abortWithError 按 Anthropic 的错误信封返回，两种协议的客户端都能读懂。
func abortWithError(c *gin.Context, status int, errType, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// TouchAPIKey 更新密钥的最近使用时间（每 Key 30s 节流一次写库，大幅减轻 SQLite 并发写锁压力）。
func TouchAPIKey(ctx context.Context, client *ent.Client, keyID int64) {
	if client == nil || keyID <= 0 {
		return
	}
	now := time.Now()
	apiKeyTouchMu.Lock()
	perClient, ok := apiKeyLastTouch[client]
	if !ok {
		perClient = make(map[int64]time.Time)
		apiKeyLastTouch[client] = perClient
	}
	last := perClient[keyID]
	if !last.IsZero() && now.Sub(last) < apiKeyTouchBackoff {
		apiKeyTouchMu.Unlock()
		return
	}
	perClient[keyID] = now
	apiKeyTouchMu.Unlock()

	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, _ = client.APIKey.Update().
		Where(apikey.ID(keyID)).
		SetLastUsedAt(now).
		Save(bg)
}
