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
