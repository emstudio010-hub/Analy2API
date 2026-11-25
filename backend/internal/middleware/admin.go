// Package middleware 提供管理后台鉴权与下游密钥鉴权。
package middleware

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"cmd2api/ent"
	"cmd2api/ent/adminkey"
	"cmd2api/internal/auth"
	"cmd2api/internal/domain"

	"github.com/gin-gonic/gin"
)

// ctxKeyAdminClaims 是登录声明在 gin.Context 里的键名。
const ctxKeyAdminClaims = "cmd2api.admin_claims"

// ctxKeyAdminKey 是远程管理密钥实体在 gin.Context 里的键名。
const ctxKeyAdminKey = "cmd2api.admin_key"

const (
	adminKeyCacheTTL     = 5 * time.Second
	adminKeyTouchBackoff = 30 * time.Second
)

type cachedAdminKeyEntry struct {
	ak        *ent.AdminKey
	expiresAt time.Time
	gen       uint64
}

var (
	adminKeyTouchMu   sync.Mutex
	adminKeyLastTouch = make(map[*ent.Client]map[int64]time.Time)

	adminKeyGenMu sync.RWMutex
	adminKeyGens  = make(map[*ent.Client]*atomic.Uint64)
)

func adminKeyGenCounter(client *ent.Client) *atomic.Uint64 {
	adminKeyGenMu.RLock()
	g, ok := adminKeyGens[client]
	adminKeyGenMu.RUnlock()
	if ok {
		return g
	}
	adminKeyGenMu.Lock()
	defer adminKeyGenMu.Unlock()
	if g, ok = adminKeyGens[client]; ok {
		return g
	}
	g = &atomic.Uint64{}
	adminKeyGens[client] = g
	return g
}

// InvalidateAdminKeyCache 立即使指定 DB 实例上的 AdminKey 鉴权缓存失效。
func InvalidateAdminKeyCache(client *ent.Client) {
	if client == nil {
		return
	}
	adminKeyGenCounter(client).Add(1)
}

type adminAuthConfig struct {
	client       *ent.Client
	bootstrapKey string
	cacheMu      sync.RWMutex
	cache        map[string]cachedAdminKeyEntry
}

// AdminAuthOption 配置 AdminAuth 的可选扩展能力（如远程管理密钥鉴权）。
type AdminAuthOption func(*adminAuthConfig)

// WithAdminKeyStore 启用数据库管理密钥（adm-...）与初始环境变量管理密钥（ADMIN_API_KEY）双轨鉴权。
func WithAdminKeyStore(client *ent.Client, bootstrapKey string) AdminAuthOption {
	return func(cfg *adminAuthConfig) {
		cfg.client = client
		cfg.bootstrapKey = strings.TrimSpace(bootstrapKey)
	}
}

// HashAdminKey 计算管理密钥的小写十六进制 SHA-256 哈希。
func HashAdminKey(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

// GenerateAdminKey 生成新的远程管理密钥：
// plain 形如 "adm-" + 48 位随机小写 hex，prefix 为前 12 个字符（如 "adm-a1b2c3d4"），hash 为其 SHA-256 hex。
func GenerateAdminKey() (plain string, prefix string, hash string, err error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("生成管理密钥随机数: %w", err)
	}
	plain = domain.AdminKeyPrefix + hex.EncodeToString(buf)
	prefix = plain[:12]
	hash = HashAdminKey(plain)
	return plain, prefix, hash, nil
}

// AdminAuth 校验管理后台请求：支持 JWT Bearer 令牌与远程管理密钥（X-Management-Key / X-Admin-Key / Bearer adm-...）双轨鉴权。
func AdminAuth(issuer *auth.Issuer, opts ...AdminAuthOption) gin.HandlerFunc {
	var cfg adminAuthConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return func(c *gin.Context) {
		if rawAdminKey, ok := extractAdminKey(c, cfg.bootstrapKey); ok {
			handleAdminKeyAuth(c, &cfg, rawAdminKey)
			return
		}

		raw := bearerToken(c)
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "缺少登录令牌"})
			return
		}
		if issuer == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "登录令牌无效或已过期"})
			return
		}
		claims, err := issuer.Verify(raw)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "登录令牌无效或已过期"})
			return
		}
		if claims.Role != domain.RoleAdmin {
			// 仅管理员模式：非管理员一律拒绝，不存在「普通用户能看自己数据」的路径。
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "需要管理员权限"})
			return
		}
		c.Set(ctxKeyAdminClaims, claims)
		c.Next()
	}
}

func handleAdminKeyAuth(c *gin.Context, cfg *adminAuthConfig, rawKey string) {
	syntheticClaims := &auth.Claims{
		UserID: 0,
		Email:  "admin-key@system",
		Role:   domain.RoleAdmin,
	}

	// 1. 优先常量时间比对非空 bootstrapKey（ADMIN_API_KEY）。
	if cfg.bootstrapKey != "" && constantTimeEquals(rawKey, cfg.bootstrapKey) {
		c.Set(ctxKeyAdminClaims, syntheticClaims)
		c.Next()
		return
	}

	// 2. 查询数据库 ent.AdminKey。
	if cfg.client == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "管理密钥无效或已停用"})
		return
	}

	hash := HashAdminKey(rawKey)
	now := time.Now()
	curGen := adminKeyGenCounter(cfg.client).Load()
	var ak *ent.AdminKey

	cfg.cacheMu.RLock()
	if cfg.cache != nil {
		if entry, ok := cfg.cache[hash]; ok && entry.gen == curGen && now.Before(entry.expiresAt) {
			ak = entry.ak
		}
	}
	cfg.cacheMu.RUnlock()

	if ak == nil {
		var err error
		ak, err = cfg.client.AdminKey.Query().
			Where(adminkey.KeyHashEQ(hash)).
			Only(c.Request.Context())
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "管理密钥无效或已停用"})
			return
		}
		cfg.cacheMu.Lock()
		if cfg.cache == nil || len(cfg.cache) > 1024 {
			cfg.cache = make(map[string]cachedAdminKeyEntry)
		}
		cfg.cache[hash] = cachedAdminKeyEntry{
			ak:        ak,
			expiresAt: now.Add(adminKeyCacheTTL),
			gen:       curGen,
		}
		cfg.cacheMu.Unlock()
	}

	if ak.Status != domain.StatusActive {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "管理密钥无效或已停用"})
		return
	}

	if ak.ExpiresAt != nil && time.Now().After(*ak.ExpiresAt) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "管理密钥已过期"})
		return
	}

	if whitelist := splitAllowedIPs(ak.AllowedIps); len(whitelist) > 0 {
		if !ipAllowed(c.ClientIP(), whitelist) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "当前来源 IP 不在管理密钥白名单内"})
			return
		}
	}

	scope := strings.ToLower(strings.TrimSpace(ak.Scopes))
	if scope == domain.AdminKeyScopeReadOnly {
		if !isReadOnlyHTTPMethod(c.Request.Method) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "只读管理密钥无权执行写操作"})
			return
		}
	}
	if scope == domain.AdminKeyScopePushAccounts {
		if !isRemoteAccountPushPath(c.Request.URL.Path) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "远程补号专用密钥仅允许调用远程推号接口 (/api/remote/accounts/push)"})
			return
		}
	}

	touchAdminKey(c.Request.Context(), cfg.client, ak.ID)
	c.Set(ctxKeyAdminKey, ak)
	c.Set(ctxKeyAdminClaims, syntheticClaims)
	c.Next()
}

func isRemoteAccountPushPath(urlPath string) bool {
	p := strings.TrimRight(strings.ToLower(strings.TrimSpace(urlPath)), "/")
	return p == "/api/remote/accounts/push" ||
		p == "/api/remote/accounts/push/info" ||
		p == "/api/remote/accounts/push/proxy" ||
		p == "/api/remote/accounts/push/proxy/fetch" ||
		p == "/api/accounts/push" ||
		p == "/api/accounts/push/info" ||
		p == "/api/accounts/push/proxy" ||
		p == "/api/accounts/push/proxy/fetch"
}

// extractAdminKey 按优先级提取管理密钥：
//  1. X-Management-Key 请求头
//  2. X-Admin-Key 请求头
//  3. X-Push-Key 请求头
//  4. Authorization: Bearer <tok>（或裸 <tok>）以 adm- 开头，或与非空 bootstrapKey 匹配且非 JWT 格式
//  5. 仅在远程补号路径下兼容 ?key= / ?admin_key= 查询参数（便于简单 webhook/脚本补号）
func extractAdminKey(c *gin.Context, bootstrapKey string) (string, bool) {
	for _, hdr := range []string{"X-Management-Key", "X-Admin-Key", "X-Push-Key"} {
		if v := strings.TrimSpace(c.GetHeader(hdr)); v != "" {
			return v, true
		}
	}
	tok := bearerToken(c)
	if tok != "" {
		if domain.HasAdminKeyPrefix(tok) {
			return tok, true
		}
		if bootstrapKey != "" && !looksLikeJWT(tok) && constantTimeEquals(tok, bootstrapKey) {
			return tok, true
		}
	}
	if isRemoteAccountPushPath(c.Request.URL.Path) {
		for _, qk := range []string{"admin_key", "key"} {
			if qv := strings.TrimSpace(c.Query(qk)); qv != "" {
				return qv, true
			}
		}
	}
	return "", false
}

func constantTimeEquals(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func looksLikeJWT(tok string) bool {
	return strings.Count(tok, ".") == 2 && strings.HasPrefix(tok, "eyJ")
}

func splitAllowedIPs(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
	if len(parts) == 0 {
		return nil
	}
	return parts
}

func isReadOnlyHTTPMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func touchAdminKey(ctx context.Context, client *ent.Client, id int64) {
	if client == nil || id <= 0 {
		return
	}
	now := time.Now()
	adminKeyTouchMu.Lock()
	perClient, ok := adminKeyLastTouch[client]
	if !ok {
		perClient = make(map[int64]time.Time)
		adminKeyLastTouch[client] = perClient
	}
	last := perClient[id]
	if !last.IsZero() && now.Sub(last) < adminKeyTouchBackoff {
		adminKeyTouchMu.Unlock()
		return
	}
	perClient[id] = now
	adminKeyTouchMu.Unlock()

	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = client.AdminKey.UpdateOneID(id).
		SetLastUsedAt(now).
		Save(bg)
}

// AdminClaimsFrom 取出当前请求的登录声明。
func AdminClaimsFrom(c *gin.Context) (*auth.Claims, bool) {
	v, ok := c.Get(ctxKeyAdminClaims)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*auth.Claims)
	return claims, ok
}

// AdminKeyFrom 取出当前请求命中的远程管理密钥实体（若通过数据库管理密钥鉴权）。
func AdminKeyFrom(c *gin.Context) (*ent.AdminKey, bool) {
	v, ok := c.Get(ctxKeyAdminKey)
	if !ok {
		return nil, false
	}
	ak, ok := v.(*ent.AdminKey)
	return ak, ok
}

// bearerToken 从 Authorization 头里取出令牌。
func bearerToken(c *gin.Context) string {
	h := strings.TrimSpace(c.GetHeader("Authorization"))
	if h == "" {
		return ""
	}
	if len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	// 有些客户端（curl 手测）会直接填令牌，容错一下。
	return h
}
