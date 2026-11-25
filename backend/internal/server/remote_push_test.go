package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/auth"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/handler"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/server"
	"cmd2api/internal/service"
)

func TestRemoteAccountPushKeyScopeAndMultiPlatformPush(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "remote_push.db")
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:       "sqlite",
		SQLitePath:   dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer client.Close()

	vault, err := crypto.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	issuer := auth.NewIssuer("test-jwt-secret-0123456789abcdef", time.Hour)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		Server: config.ServerConfig{Mode: "test"},
	}

	sched := scheduler.New(client, vault, 5, logger)
	relayCli := relay.NewClient(config.CommandCodeConfig{}, logger)
	accSvc := service.NewAccountService(client, vault, relayCli, sched, logger)
	agCli := antigravity.NewClient("", logger)
	agSvc := service.NewAntigravityService(client, vault, agCli, agCli.Docker(), t.TempDir(), antigravity.DefaultDockerImage, logger)
	accSvc.SetAntigravityService(agSvc)
	adminKeySvc := service.NewAdminKeyService(client)

	h := handler.New(client, vault, issuer, sched, nil, accSvc, cfg, logger)
	h.SetAdminKeyService(adminKeySvc)
	h.SetAntigravityService(agSvc)

	r := server.NewRouter(server.Options{
		Handler: h,
		Issuer:  issuer,
		Client:  client,
		Mode:    "test",
		Version: "1.0.0",
	})

	// 签发三把不同权限的远程管理密钥
	pushKey, _, err := adminKeySvc.Create(ctx, "远端补号机", domain.AdminKeyScopePushAccounts, "", 0)
	if err != nil {
		t.Fatalf("Create push_accounts key: %v", err)
	}
	fullKey, _, err := adminKeySvc.Create(ctx, "全权管理员Key", domain.AdminKeyScopeFull, "", 0)
	if err != nil {
		t.Fatalf("Create full key: %v", err)
	}
	roKey, _, err := adminKeySvc.Create(ctx, "只读探针Key", domain.AdminKeyScopeReadOnly, "", 0)
	if err != nil {
		t.Fatalf("Create readonly key: %v", err)
	}

	doReq := func(method, path, keyHeader, keyVal string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if keyHeader != "" {
			req.Header.Set(keyHeader, keyVal)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	// 1. push_accounts 专用 Key 可探测推号信息，但严格禁止访问其它管理端点（最小权限）
	rrInfo := doReq(http.MethodGet, "/api/remote/accounts/push/info", "X-Push-Key", pushKey, nil)
	if rrInfo.Code != http.StatusOK || !strings.Contains(rrInfo.Body.String(), `"service":"Analy2API"`) {
		t.Fatalf("push_accounts Key 访问 push/info 失败: code=%d body=%s", rrInfo.Code, rrInfo.Body.String())
	}
	rrDeniedList := doReq(http.MethodGet, "/api/accounts", "X-Push-Key", pushKey, nil)
	if rrDeniedList.Code != http.StatusForbidden {
		t.Fatalf("push_accounts Key 访问 /api/accounts 应返回 403，实际 %d: %s", rrDeniedList.Code, rrDeniedList.Body.String())
	}
	rrDeniedSettings := doReq(http.MethodGet, "/api/settings", "Authorization", "Bearer "+pushKey, nil)
	if rrDeniedSettings.Code != http.StatusForbidden {
		t.Fatalf("push_accounts Key 访问 /api/settings 应返回 403，实际 %d", rrDeniedSettings.Code)
	}

	// 2. readonly Key 不可推送补号（403）
	rrRoPush := doReq(http.MethodPost, "/api/remote/accounts/push", "X-Management-Key", roKey, map[string]any{
		"platform": "commandcode",
		"api_key":  "user_readonly_denied",
	})
	if rrRoPush.Code != http.StatusForbidden {
		t.Fatalf("readonly Key 推号应返回 403，实际 %d", rrRoPush.Code)
	}

	// 3. push_accounts Key 批量推送四平台账号（含自动按 group_name 建组 & AGY 自动容器分配）
	rrBatch := doReq(http.MethodPost, "/api/remote/accounts/push", "X-Push-Key", pushKey, map[string]any{
		"upsert": true,
		"accounts": []map[string]any{
			{
				"platform":      "antigravity",
				"email":         "agy-remote@gmail.com",
				"refresh_token": "1//0g_remote_refresh_1",
				"group_name":    "agy-pool-remote",
			},
			{
				"platform":      "workbuddy",
				"email":         "wb-remote@example.com",
				"access_token":  "wb_tok_remote_1",
				"refresh_token": "wb_rt_remote_1",
			},
			{
				"api_key": "user_remote_cmdc_12345",
			},
			{
				"platform":     "opencode",
				"name":         "oc-remote-go",
				"account_mode": "go",
				"api_key":      "oc_tok_remote_1",
			},
		},
	})
	if rrBatch.Code != http.StatusCreated {
		t.Fatalf("批量推号失败: code=%d body=%s", rrBatch.Code, rrBatch.Body.String())
	}
	var batchOut service.RemotePushAccountsResult
	if err := json.Unmarshal(rrBatch.Body.Bytes(), &batchOut); err != nil {
		t.Fatalf("解析批量推号响应失败: %v", err)
	}
	if !batchOut.OK || batchOut.Created != 4 || batchOut.Failed != 0 {
		t.Fatalf("期望创建 4 个四平台账号，实际 %+v", batchOut)
	}
	if batchOut.Results[0].Platform != domain.PlatformAntigravity || batchOut.Results[0].ContainerNodeID == 0 {
		t.Fatalf("AntiGravity 远程推号应自动分配独立容器节点: %+v", batchOut.Results[0])
	}
	if batchOut.Results[2].Platform != domain.PlatformCommandCode {
		t.Fatalf("user_ 前缀密钥应自动识别为 commandcode 平台: %+v", batchOut.Results[2])
	}

	// 4. 拿着普通全权管理员 Key + ?key= 幂等二次推送同邮箱账号 -> 自动触发 updated 刷新凭证，绝不重复建号
	rrUpsert := doReq(http.MethodPost, "/api/remote/accounts/push?key="+fullKey, "", "", map[string]any{
		"platform":     "workbuddy",
		"email":        "wb-remote@example.com",
		"access_token": "wb_tok_remote_refreshed_v2",
	})
	if rrUpsert.Code != http.StatusOK {
		t.Fatalf("管理员 Key 幂等刷新推号失败: code=%d body=%s", rrUpsert.Code, rrUpsert.Body.String())
	}
	var upsertOut service.RemotePushAccountsResult
	_ = json.Unmarshal(rrUpsert.Body.Bytes(), &upsertOut)
	if !upsertOut.OK || upsertOut.Updated != 1 || upsertOut.Created != 0 {
		t.Fatalf("同邮箱二次推送应触发 updated=1 created=0，实际 %+v", upsertOut)
	}
	if strings.Contains(rrUpsert.Body.String(), "wb_tok_remote_refreshed_v2") {
		t.Fatal("推号响应绝不可回显明文密钥")
	}
}

func TestRemotePushProxyPoolAndKeyPolicyEndToEnd(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "remote_push_proxy_pool.db")
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:       "sqlite",
		SQLitePath:   dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer client.Close()

	// 模拟外部代理商提取 IP 接口
	fetchCalls := 0
	mockProxyVendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"list":[{"ip":"198.51.100.10","port":8080,"user":"vuser","pass":"vpass","region":"jp"},{"proxy":"socks5://198.51.100.11:1080","region":"us"}]}}`)
	}))
	defer mockProxyVendor.Close()

	vault, err := crypto.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	issuer := auth.NewIssuer("test-jwt-secret-0123456789abcdef", time.Hour)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Server: config.ServerConfig{Mode: "test"}}

	sched := scheduler.New(client, vault, 5, logger)
	relayCli := relay.NewClient(config.CommandCodeConfig{}, logger)
	accSvc := service.NewAccountService(client, vault, relayCli, sched, logger)
	proxyPoolSvc := service.NewProxyPoolService(client, logger)
	if err := proxyPoolSvc.EnsureSeeded(ctx); err != nil {
		t.Fatalf("EnsureSeeded proxy pool: %v", err)
	}
	accSvc.SetProxyPoolService(proxyPoolSvc)
	adminKeySvc := service.NewAdminKeyService(client)

	h := handler.New(client, vault, issuer, sched, nil, accSvc, cfg, logger)
	h.SetAdminKeyService(adminKeySvc)
	h.SetProxyPoolService(proxyPoolSvc)

	r := server.NewRouter(server.Options{
		Handler: h,
		Issuer:  issuer,
		Client:  client,
		Mode:    "test",
		Version: "1.0.0",
	})

	pushKey, _, err := adminKeySvc.Create(ctx, "补号机-自动IP池", domain.AdminKeyScopePushAccounts, "", 0)
	if err != nil {
		t.Fatalf("Create push key: %v", err)
	}
	fullKey, _, err := adminKeySvc.Create(ctx, "全权管理员", domain.AdminKeyScopeFull, "", 0)
	if err != nil {
		t.Fatalf("Create full key: %v", err)
	}

	doReq := func(method, path, keyHeader, keyVal string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if keyHeader != "" {
			req.Header.Set(keyHeader, keyVal)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	// 0. push_accounts Key 严禁直接访问全局管理 /api/proxy-pool（最小权限守卫）
	if rr := doReq(http.MethodGet, "/api/proxy-pool", "X-Push-Key", pushKey, nil); rr.Code != http.StatusForbidden {
		t.Fatalf("push_accounts Key 访问 /api/proxy-pool 应返回 403，实际 %d", rr.Code)
	}

	// 1. 管理员配置全局代理 IP 池：2 条手动代理 + max_accounts_per_ip=1 + 远程拉取源（池满自动拉取）
	rrPool := doReq(http.MethodPut, "/api/proxy-pool", "X-Management-Key", fullKey, map[string]any{
		"enabled":             true,
		"strategy":            "round_robin",
		"max_accounts_per_ip": 1,
		"raw_proxies":         "203.0.113.1:8080:u1:secret1 #us\n203.0.113.2:8080 #us",
		"fetch": map[string]any{
			"enabled":             true,
			"url":                 mockProxyVendor.URL,
			"method":              "GET",
			"default_scheme":      "http",
			"default_tag":         "remote",
			"json_path":           "data.list",
			"auto_fetch_on_empty": true,
			"timeout_seconds":     10,
		},
	})
	if rrPool.Code != http.StatusOK {
		t.Fatalf("更新代理 IP 池失败: code=%d body=%s", rrPool.Code, rrPool.Body.String())
	}

	// 2. 批量推 3 个账号且不传 proxy_url：前 2 个轮询吃掉手动池 2 条 IP，第 3 个触发自动从远程代理商拉取新 IP！
	rrPush3 := doReq(http.MethodPost, "/api/remote/accounts/push", "X-Push-Key", pushKey, map[string]any{
		"platform": "commandcode",
		"accounts": []map[string]any{
			{"name": "cc-pool-1", "api_key": "user_pool_key_1"},
			{"name": "cc-pool-2", "api_key": "user_pool_key_2"},
			{"name": "cc-pool-3", "api_key": "user_pool_key_3"},
		},
	})
	if rrPush3.Code != http.StatusCreated {
		t.Fatalf("批量走 IP 池推号失败: code=%d body=%s", rrPush3.Code, rrPush3.Body.String())
	}
	if strings.Contains(rrPush3.Body.String(), "secret1") || strings.Contains(rrPush3.Body.String(), "vpass") {
		t.Fatalf("响应绝不能泄露明文代理密码: %s", rrPush3.Body.String())
	}
	var push3Out service.RemotePushAccountsResult
	_ = json.Unmarshal(rrPush3.Body.Bytes(), &push3Out)
	if push3Out.Created != 3 || fetchCalls == 0 {
		t.Fatalf("期望创建 3 条并触发远程拉取，实际 created=%d fetchCalls=%d out=%+v", push3Out.Created, fetchCalls, push3Out)
	}
	if push3Out.Results[0].AssignedProxy != "http://u1:***@203.0.113.1:8080" ||
		push3Out.Results[1].AssignedProxy != "http://203.0.113.2:8080" ||
		!strings.Contains(push3Out.Results[2].AssignedProxy, "198.51.100.") {
		t.Fatalf("3 个账号的轮询+自动拉取 IP 分配结果不符预期: %+v", push3Out.Results)
	}

	// 3. 补号 Key 自助调用 PUT /api/remote/accounts/push/proxy 切换自身默认固定 IP，再推号自动生效
	rrSetKeyIP := doReq(http.MethodPut, "/api/remote/accounts/push/proxy", "X-Push-Key", pushKey, map[string]any{
		"mode":            "fixed",
		"fixed_proxy_url": "192.0.2.88:9000:kuser:kpass",
	})
	if rrSetKeyIP.Code != http.StatusOK {
		t.Fatalf("补号 Key 调整自身代理策略失败: code=%d body=%s", rrSetKeyIP.Code, rrSetKeyIP.Body.String())
	}

	rrPushFixed := doReq(http.MethodPost, "/api/remote/accounts/push", "X-Push-Key", pushKey, map[string]any{
		"platform": "opencode",
		"name":     "oc-key-fixed-ip",
		"api_key":  "oc_fixed_ip_key_1",
	})
	if rrPushFixed.Code != http.StatusCreated {
		t.Fatalf("Key 固定代理推号失败: code=%d body=%s", rrPushFixed.Code, rrPushFixed.Body.String())
	}
	var fixedOut service.RemotePushAccountsResult
	_ = json.Unmarshal(rrPushFixed.Body.Bytes(), &fixedOut)
	if fixedOut.Results[0].AssignedProxy != "http://kuser:***@192.0.2.88:9000" || fixedOut.Results[0].ProxySource != "key_fixed" {
		t.Fatalf("期望使用 Key 固定代理，实际 %+v", fixedOut.Results[0])
	}

	// 4. 补号 Key 显式在推号里传 proxy_list / ip 覆盖 Key 默认配置
	rrPushExplicit := doReq(http.MethodPost, "/api/remote/accounts/push", "X-Push-Key", pushKey, map[string]any{
		"platform":   "opencode",
		"proxy_list": []string{"192.0.2.91:8080", "socks5://192.0.2.92:1080"},
		"accounts": []map[string]any{
			{"name": "oc-list-1", "api_key": "oc_list_key_1"},
			{"name": "oc-list-2", "api_key": "oc_list_key_2"},
		},
	})
	if rrPushExplicit.Code != http.StatusCreated {
		t.Fatalf("proxy_list 推号失败: code=%d body=%s", rrPushExplicit.Code, rrPushExplicit.Body.String())
	}
	var listOut service.RemotePushAccountsResult
	_ = json.Unmarshal(rrPushExplicit.Body.Bytes(), &listOut)
	if listOut.Results[0].AssignedProxy != "http://192.0.2.91:8080" || listOut.Results[1].AssignedProxy != "socks5://192.0.2.92:1080" {
		t.Fatalf("proxy_list 顺序轮询分配异常: %+v", listOut.Results)
	}

	// 5. 补号 Key 远程给已有账号动态更换 IP（支持指定 IP 或从池轮换）
	targetAccID := fixedOut.Results[0].AccountID
	rrReassign := doReq(http.MethodPut, "/api/remote/accounts/push/proxy", "X-Push-Key", pushKey, map[string]any{
		"account_id": targetAccID,
		"ip":         "192.0.2.99:7777:swapu:swapp",
	})
	if rrReassign.Code != http.StatusOK {
		t.Fatalf("补号 Key 更换已有账号代理失败: code=%d body=%s", rrReassign.Code, rrReassign.Body.String())
	}
	if !strings.Contains(rrReassign.Body.String(), "http://swapu:***@192.0.2.99:7777") {
		t.Fatalf("更换账号代理返回值不符预期: %s", rrReassign.Body.String())
	}
}
