package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cmd2api/ent"
	"cmd2api/internal/auth"
	"cmd2api/internal/config"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	client, err := db.Open(context.Background(), config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: ":memory:",
	})
	if err != nil {
		t.Fatalf("打开测试 SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func newAdminTestRouter(mw gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	// 信任测试用回环代理或直连 RemoteAddr，使 c.ClientIP() 准确反映请求来源 IP。
	_ = r.SetTrustedProxies(nil)
	r.Any("/admin/test", mw, func(c *gin.Context) {
		claims, ok := AdminClaimsFrom(c)
		if !ok || claims == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "missing claims"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"uid":   claims.UserID,
			"email": claims.Email,
			"role":  claims.Role,
		})
	})
	return r
}

func decodeErrorMsg(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应 JSON 失败 (%s): %v", rec.Body.String(), err)
	}
	msg, _ := body["error"].(string)
	return msg
}

func TestGenerateAndHashAdminKey(t *testing.T) {
	plain, prefix, hash, err := GenerateAdminKey()
	if err != nil {
		t.Fatalf("GenerateAdminKey() 报错: %v", err)
	}
	if !strings.HasPrefix(plain, domain.AdminKeyPrefix) {
		t.Errorf("plain 应以 %q 开头，得到 %q", domain.AdminKeyPrefix, plain)
	}
	if len(plain) != len(domain.AdminKeyPrefix)+48 {
		t.Errorf("plain 长度应为 52 (adm- + 48 hex)，得到 %d (%q)", len(plain), plain)
	}
	if len(prefix) != 12 || prefix != plain[:12] {
		t.Errorf("prefix = %q, want %q", prefix, plain[:12])
	}
	if len(hash) != 64 || hash != HashAdminKey(plain) {
		t.Errorf("hash 不匹配: got %q, want %q", hash, HashAdminKey(plain))
	}
}

func TestAdminAuth_JWTNormalExpiredAndNonAdmin(t *testing.T) {
	issuer := auth.NewIssuer("0123456789abcdefghij", time.Hour)
	// 不传任何 AdminAuthOption，验证原有单参调用签名 100% 兼容。
	r := newAdminTestRouter(AdminAuth(issuer))

	// 1. 缺少令牌 -> 401
	req := httptest.NewRequest(http.MethodGet, "/admin/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少令牌应返回 401，得到 %d", rec.Code)
	}

	// 2. 合法管理员 JWT -> 200
	tok, _, err := issuer.Issue(42, "admin@cmdwb2api.local", domain.RoleAdmin)
	if err != nil {
		t.Fatalf("签发 JWT 失败: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法管理员 JWT 应返回 200，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 3. 过期 JWT -> 401
	expiredIssuer := auth.NewIssuer("0123456789abcdefghij", -time.Minute)
	expiredTok, _, err := expiredIssuer.Issue(42, "admin@cmdwb2api.local", domain.RoleAdmin)
	if err != nil {
		t.Fatalf("签发过期 JWT 失败: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer "+expiredTok)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("过期 JWT 应返回 401，得到 %d", rec.Code)
	}

	// 4. 非管理员角色 JWT -> 403
	userTok, _, _ := issuer.Issue(7, "user@cmdwb2api.local", domain.RoleUser)
	req = httptest.NewRequest(http.MethodGet, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer "+userTok)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通用户角色应返回 403，得到 %d", rec.Code)
	}
}

func TestAdminAuth_BootstrapKeyAndHeaderPrecedence(t *testing.T) {
	client := openTestDB(t)
	issuer := auth.NewIssuer("0123456789abcdefghij", time.Hour)
	const bootKey = "custom-bootstrap-admin-secret-2026"

	r := newAdminTestRouter(AdminAuth(issuer, WithAdminKeyStore(client, bootKey)))

	for _, setter := range []struct {
		name  string
		apply func(*http.Request)
	}{
		{
			name:  "X-Management-Key",
			apply: func(req *http.Request) { req.Header.Set("X-Management-Key", bootKey) },
		},
		{
			name:  "X-Admin-Key",
			apply: func(req *http.Request) { req.Header.Set("X-Admin-Key", bootKey) },
		},
		{
			name:  "Authorization Bearer non-JWT bootstrapKey",
			apply: func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+bootKey) },
		},
	} {
		t.Run(setter.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
			setter.apply(req)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s 携带 bootstrapKey 应放行 200，得到 %d: %s", setter.name, rec.Code, rec.Body.String())
			}
			var payload map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &payload)
			if payload["email"] != "admin-key@system" || payload["role"] != domain.RoleAdmin {
				t.Errorf("合成管理员声明不对: %+v", payload)
			}
		})
	}
}

func TestAdminAuth_DatabaseAdminKeyScopesExpiryStatusAndIPWhitelist(t *testing.T) {
	ctx := context.Background()
	client := openTestDB(t)
	issuer := auth.NewIssuer("0123456789abcdefghij", time.Hour)
	r := newAdminTestRouter(AdminAuth(issuer, WithAdminKeyStore(client, "")))

	// 1. 全权限数据库管理密钥（Bearer adm-...）
	fullPlain, fullPrefix, fullHash, err := GenerateAdminKey()
	if err != nil {
		t.Fatal(err)
	}
	fullEntity, err := client.AdminKey.Create().
		SetName("full-key").
		SetKeyPrefix(fullPrefix).
		SetKeyHash(fullHash).
		SetScopes(domain.AdminKeyScopeFull).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer "+fullPlain)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("full 权限管理密钥 POST 应返回 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	updatedFull, err := client.AdminKey.Get(ctx, fullEntity.ID)
	if err != nil || updatedFull.LastUsedAt == nil {
		t.Errorf("验证通过后应更新 LastUsedAt，err=%v last_used_at=%v", err, updatedFull.LastUsedAt)
	}

	// 2. 只读管理密钥：GET/HEAD/OPTIONS 放行，POST/PUT/DELETE 拦截 403
	roPlain, roPrefix, roHash, _ := GenerateAdminKey()
	if _, err := client.AdminKey.Create().
		SetName("readonly-key").
		SetKeyPrefix(roPrefix).
		SetKeyHash(roHash).
		SetScopes(domain.AdminKeyScopeReadOnly).
		Save(ctx); err != nil {
		t.Fatal(err)
	}

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req = httptest.NewRequest(method, "/admin/test", nil)
		req.Header.Set("X-Admin-Key", roPlain)
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("只读密钥 %s 应放行 200，得到 %d", method, rec.Code)
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("X-Admin-Key", roPlain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || decodeErrorMsg(t, rec) != "只读管理密钥无权执行写操作" {
		t.Errorf("只读密钥 POST 应返回 403 只读错误，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 3. 已过期管理密钥 -> 401
	expPlain, expPrefix, expHash, _ := GenerateAdminKey()
	past := time.Now().Add(-5 * time.Minute)
	if _, err := client.AdminKey.Create().
		SetName("expired-key").
		SetKeyPrefix(expPrefix).
		SetKeyHash(expHash).
		SetExpiresAt(past).
		Save(ctx); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/admin/test", nil)
	req.Header.Set("X-Management-Key", expPlain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || decodeErrorMsg(t, rec) != "管理密钥已过期" {
		t.Errorf("过期管理密钥应返回 401 管理密钥已过期，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 4. 停用或不存在的管理密钥 -> 401
	disPlain, disPrefix, disHash, _ := GenerateAdminKey()
	if _, err := client.AdminKey.Create().
		SetName("disabled-key").
		SetKeyPrefix(disPrefix).
		SetKeyHash(disHash).
		SetStatus(domain.StatusDisabled).
		Save(ctx); err != nil {
		t.Fatal(err)
	}
	for _, badKey := range []string{disPlain, "adm-nonexistent000000000000000000000000"} {
		req = httptest.NewRequest(http.MethodGet, "/admin/test", nil)
		req.Header.Set("X-Management-Key", badKey)
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || decodeErrorMsg(t, rec) != "管理密钥无效或已停用" {
			t.Errorf("停用/无效密钥应返回 401 管理密钥无效或已停用，得到 %d: %s", rec.Code, rec.Body.String())
		}
	}

	// 5. IP 与 CIDR 白名单校验（逗号与空白混合分隔）
	ipPlain, ipPrefix, ipHash, _ := GenerateAdminKey()
	if _, err := client.AdminKey.Create().
		SetName("ip-restricted-key").
		SetKeyPrefix(ipPrefix).
		SetKeyHash(ipHash).
		SetAllowedIps("192.168.50.10, 10.20.0.0/16\n127.0.0.1").
		Save(ctx); err != nil {
		t.Fatal(err)
	}

	// 5a. 精确 IP 命中 (192.168.50.10)
	req = httptest.NewRequest(http.MethodGet, "/admin/test", nil)
	req.RemoteAddr = "192.168.50.10:54321"
	req.Header.Set("X-Management-Key", ipPlain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("精确白名单 IP 应返回 200，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 5b. CIDR 网段命中 (10.20.88.9)
	req = httptest.NewRequest(http.MethodGet, "/admin/test", nil)
	req.RemoteAddr = "10.20.88.9:54321"
	req.Header.Set("X-Management-Key", ipPlain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("CIDR 白名单 IP 应返回 200，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 5c. 非白名单来源 IP (203.0.113.5) -> 403
	req = httptest.NewRequest(http.MethodGet, "/admin/test", nil)
	req.RemoteAddr = "203.0.113.5:54321"
	req.Header.Set("X-Management-Key", ipPlain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || decodeErrorMsg(t, rec) != "当前来源 IP 不在管理密钥白名单内" {
		t.Errorf("非白名单 IP 应返回 403 白名单拦截，得到 %d: %s", rec.Code, rec.Body.String())
	}
}
