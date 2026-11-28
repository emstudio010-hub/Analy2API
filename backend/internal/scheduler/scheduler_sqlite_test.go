package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"cmd2api/internal/config"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
)

func TestMultiPlatformSchedulerAcquireAndTargets(t *testing.T) {
	ctx := context.Background()
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: ":memory:",
	})
	if err != nil {
		t.Fatalf("open sqlite memory db: %v", err)
	}
	defer client.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sched := New(client, nil, 3, logger)

	// 1. AntiGravity 分组与账号（含 ProxyURL、BaseURL、NodeID、AuthFile）
	agyGrp, err := client.Group.Create().
		SetName("agy-pool").
		SetPlatform(domain.PlatformAntigravity).
		Save(ctx)
	if err != nil {
		t.Fatalf("create agy group: %v", err)
	}

	agyAcc, err := client.Account.Create().
		SetName("agy-node-acc-1").
		SetPlatform(domain.PlatformAntigravity).
		SetProxyURL("http://10.10.0.1:7890").
		SetCredentials(map[string]any{
			"api_key":        "sk-agy-infer-1",
			"management_key": "mgmt-agy-1",
			"base_url":       "http://127.0.0.1:18317",
		}).
		SetExtra(map[string]any{
			"node_id":   int64(99),
			"auth_file": "antigravity-user@example.com.json",
		}).
		AddGroupIDs(agyGrp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create agy account: %v", err)
	}

	lease, err := sched.Acquire(ctx, agyGrp.ID)
	if err != nil {
		t.Fatalf("Acquire antigravity failed: %v", err)
	}
	defer lease.Release()

	if lease.Platform != domain.PlatformAntigravity {
		t.Fatalf("expected platform %q, got %q", domain.PlatformAntigravity, lease.Platform)
	}
	if lease.BaseURL != "http://127.0.0.1:18317" {
		t.Fatalf("expected BaseURL http://127.0.0.1:18317, got %q", lease.BaseURL)
	}
	if lease.Credentials.APIKey != "sk-agy-infer-1" {
		t.Fatalf("expected APIKey sk-agy-infer-1, got %q", lease.Credentials.APIKey)
	}
	if lease.ProxyURL != "http://10.10.0.1:7890" {
		t.Fatalf("expected ProxyURL http://10.10.0.1:7890, got %q", lease.ProxyURL)
	}
	if lease.NodeID != 99 || lease.AuthFile != "antigravity-user@example.com.json" {
		t.Fatalf("unexpected NodeID/AuthFile: node_id=%d auth_file=%q", lease.NodeID, lease.AuthFile)
	}
	spec := lease.AntigravityNodeSpec()
	if spec.HostIP != "http://127.0.0.1:18317" || spec.APIKey != "sk-agy-infer-1" || spec.ManagementKey != "mgmt-agy-1" {
		t.Fatalf("unexpected AntigravityNodeSpec: %+v", spec)
	}

	anyTgt, err := sched.AnyTarget(ctx, agyGrp.ID)
	if err != nil || anyTgt.AccountID != agyAcc.ID || anyTgt.ProxyURL != "http://10.10.0.1:7890" || anyTgt.NodeID != 99 {
		t.Fatalf("AnyTarget antigravity unexpected: %+v, err=%v", anyTgt, err)
	}

	platTgt, err := sched.AnyTargetByPlatform(ctx, "agy")
	if err != nil || platTgt.Platform != domain.PlatformAntigravity {
		t.Fatalf("AnyTargetByPlatform(agy) unexpected: %+v, err=%v", platTgt, err)
	}

	// 2. CommandCode 与 OpenCode 账号的 TargetByAccountID 与 ProxyURL 透传验证
	ccGrp, _ := client.Group.Create().SetName("cc-pool").SetPlatform(domain.PlatformCommandCode).Save(ctx)
	ccAcc, err := client.Account.Create().
		SetName("cc-acc").
		SetPlatform(domain.PlatformCommandCode).
		SetProxyURL("socks5://127.0.0.1:1080").
		SetCredentials(map[string]any{"api_key": "user_cc_123"}).
		AddGroupIDs(ccGrp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create cc account: %v", err)
	}

	ccTgt, err := sched.TargetByAccountID(ctx, ccAcc.ID)
	if err != nil || ccTgt.Platform != domain.PlatformCommandCode || ccTgt.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("TargetByAccountID commandcode unexpected: %+v, err=%v", ccTgt, err)
	}

	ocGrp, _ := client.Group.Create().SetName("oc-pool").SetPlatform(domain.PlatformOpenCode).Save(ctx)
	ocAcc, err := client.Account.Create().
		SetName("oc-acc").
		SetPlatform(domain.PlatformOpenCode).
		SetProxyURL("http://10.20.0.2:8080").
		SetCredentials(map[string]any{"api_key": "sk-oc-123"}).
		SetExtra(map[string]any{"account_mode": domain.AccountModeGo}).
		AddGroupIDs(ocGrp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create oc account: %v", err)
	}

	ocLease, err := sched.Acquire(ctx, ocGrp.ID)
	if err != nil {
		t.Fatalf("Acquire opencode failed: %v", err)
	}
	defer ocLease.Release()
	if ocLease.Account.ID != ocAcc.ID || ocLease.ProxyURL != "http://10.20.0.2:8080" || ocLease.BaseURL != domain.OpenCodeBaseURLGo {
		t.Fatalf("unexpected opencode lease: %+v", ocLease)
	}

	// 3. 回归验证：OAuth 账号即使历史遗留了已过期的短效 access_token expires_at，只要持有 refresh_token 或容器节点仍可正常调度；
	//    而无 refresh_token 的普通硬过期 Key 仍会被正确拦截。
	past := time.Now().Add(-2 * time.Hour)
	_, err = client.Account.UpdateOneID(agyAcc.ID).SetExpiresAt(past).Save(ctx)
	if err != nil {
		t.Fatalf("set past expires_at on agyAcc: %v", err)
	}
	sched.Invalidate()
	agyLeaseAfterTTL, err := sched.Acquire(ctx, agyGrp.ID)
	if err != nil {
		t.Fatalf("expected OAuth/container agy account with past access_token expiry to remain schedulable, got: %v", err)
	}
	agyLeaseAfterTTL.Release()

	_, err = client.Account.UpdateOneID(ccAcc.ID).SetExpiresAt(past).Save(ctx)
	if err != nil {
		t.Fatalf("set past expires_at on ccAcc: %v", err)
	}
	sched.Invalidate()
	if _, err := sched.Acquire(ctx, ccGrp.ID); !errors.Is(err, ErrNoAccountAvailable) {
		t.Fatalf("expected hard-expired static key account to fail with ErrNoAccountAvailable, got: %v", err)
	}
}
