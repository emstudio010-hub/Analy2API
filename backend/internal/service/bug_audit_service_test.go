package service_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/service"
	"cmd2api/internal/workbuddy"
)

func TestPreserveRedactedProxyPassword_AccountAndProxyPool(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "bug_audit_svc.db")
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sched := scheduler.New(client, vault, 5, logger)
	relayCli := relay.NewClient(config.CommandCodeConfig{}, logger)
	accSvc := service.NewAccountService(client, vault, relayCli, sched, logger)

	realProxy := "http://alice:SuperSecret99@198.51.100.7:8080"
	redactedProxy := workbuddy.RedactProxyURL(realProxy)
	if redactedProxy != "http://alice:***@198.51.100.7:8080" {
		t.Fatalf("unexpected redacted proxy: %q", redactedProxy)
	}

	created, err := accSvc.Create(ctx, service.CreateAccountInput{
		Name:     "wb-proxy-preserve",
		Platform: domain.PlatformWorkBuddy,
		APIKey:   "wb_tok_1",
		ProxyURL: realProxy,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}

	// 模拟前端 AccountFormModal 原样提交脱敏后的 proxy_url 修改账号优先级：绝不能把真实密码覆盖为 "***"
	newPrio := 10
	updated, err := accSvc.Update(ctx, created.ID, service.UpdateAccountInput{
		Priority: &newPrio,
		ProxyURL: &redactedProxy,
	})
	if err != nil {
		t.Fatalf("Update account: %v", err)
	}
	if updated.ProxyURL != realProxy {
		t.Fatalf("AccountService.Update corrupted proxy password: got %q, want %q", updated.ProxyURL, realProxy)
	}

	// 修改为全新密码时应正常更新
	changedProxy := "http://alice:NewSecret77@198.51.100.7:8080"
	updated2, err := accSvc.Update(ctx, created.ID, service.UpdateAccountInput{
		ProxyURL: &changedProxy,
	})
	if err != nil {
		t.Fatalf("Update account with new password: %v", err)
	}
	if updated2.ProxyURL != changedProxy {
		t.Fatalf("expected updated proxy %q, got %q", changedProxy, updated2.ProxyURL)
	}

	// 验证 ProxyPoolService 视图脱敏与回传保存时的密码无损保留 + 并发分配安全
	poolSvc := service.NewProxyPoolService(client, logger)
	if err := poolSvc.EnsureSeeded(ctx); err != nil {
		t.Fatalf("EnsureSeeded: %v", err)
	}

	vendorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "203.0.113.50:8080:vu:vp #us\n203.0.113.51:8080:vu:vp #us\n")
	}))
	defer vendorSrv.Close()

	view1, err := poolSvc.UpdateConfig(ctx, service.ProxyPoolConfig{
		Enabled:  true,
		Strategy: service.ProxyStrategyRoundRobin,
		Fetch: service.ProxyFetchConfig{
			Enabled:          true,
			URL:              vendorSrv.URL,
			DefaultScheme:    "http",
			Username:         "defuser",
			Password:         "VendorTopSecret",
			AutoFetchOnEmpty: true,
			TimeoutSeconds:   5,
		},
	}, "203.0.113.10:8080:puser:PoolSecret1 #us\n203.0.113.11:8080:puser:PoolSecret2 #us", "us")
	if err != nil {
		t.Fatalf("UpdateConfig initial: %v", err)
	}
	for _, e := range view1.Config.Entries {
		if strings.Contains(e.URL, "PoolSecret") {
			t.Fatalf("GetView leaked plaintext entry password: %+v", e)
		}
	}
	if view1.Config.Fetch.Password != "***" {
		t.Fatalf("GetView should mask Fetch.Password as ***, got %q", view1.Config.Fetch.Password)
	}

	savedKP, err := poolSvc.SetKeyPolicy(ctx, 77, service.AdminKeyProxyPolicy{
		Mode:          service.KeyProxyModeFixed,
		FixedProxyURL: "192.0.2.88:9000:kuser:KeySecret88",
	})
	if err != nil {
		t.Fatalf("SetKeyPolicy: %v", err)
	}
	if strings.Contains(savedKP.FixedProxyURL, "KeySecret88") {
		t.Fatalf("SetKeyPolicy response leaked plaintext FixedProxyURL: %+v", savedKP)
	}
	gotKP := poolSvc.GetKeyPolicy(ctx, 77)
	if strings.Contains(gotKP.FixedProxyURL, "KeySecret88") || gotKP.FixedProxyURL != "http://kuser:***@192.0.2.88:9000" {
		t.Fatalf("GetKeyPolicy should return masked FixedProxyURL, got %+v", gotKP)
	}

	// 模拟前端 AdminKeysView 原样把脱敏后的 view1.Config 与 gotKP 回传保存
	if _, err := poolSvc.UpdateConfig(ctx, view1.Config, "", ""); err != nil {
		t.Fatalf("UpdateConfig roundtrip: %v", err)
	}
	if _, err := poolSvc.SetKeyPolicy(ctx, 77, gotKP); err != nil {
		t.Fatalf("SetKeyPolicy roundtrip: %v", err)
	}

	// 验证 Key #77 实际推号分配时依然拿到真实密码 KeySecret88
	allocKP, err := poolSvc.AllocateForPush(ctx, service.ProxyAllocationRequest{
		AdminKeyID: 77,
		Platform:   domain.PlatformWorkBuddy,
	})
	if err != nil {
		t.Fatalf("AllocateForPush key 77: %v", err)
	}
	if allocKP.ProxyURL != "http://kuser:KeySecret88@192.0.2.88:9000" {
		t.Fatalf("SetKeyPolicy roundtrip lost real password: got %q", allocKP.ProxyURL)
	}

	// 并发从池分配 12 次，验证全部返回真实未损坏密码且无竞态
	var wg sync.WaitGroup
	allocURLs := make([]string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, aErr := poolSvc.AllocateForPush(ctx, service.ProxyAllocationRequest{
				Platform: domain.PlatformCommandCode,
			})
			if aErr == nil {
				allocURLs[idx] = res.ProxyURL
			}
		}(i)
	}
	wg.Wait()
	for i, u := range allocURLs {
		if !strings.Contains(u, "PoolSecret") {
			t.Fatalf("concurrent allocation %d lost real pool password: %q", i, u)
		}
	}
}
