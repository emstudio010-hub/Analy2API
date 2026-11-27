package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cmd2api/ent/group"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
)

func TestAntigravityAdapterService_ConvertImportPushAndAuditFixes(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	clientDB, err := db.Open(ctx, config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "adapter_svc.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer clientDB.Close()

	vault, err := crypto.NewVault([]byte(strings.Repeat("v", 32)))
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	sched := scheduler.New(clientDB, vault, 3, logger)
	relayCli := relay.NewClient(config.CommandCodeConfig{}, logger)
	dataRoot := filepath.Join(t.TempDir(), "agy-data")

	agSvc := NewAntigravityService(clientDB, vault, antigravity.NewClient("", logger), nil, dataRoot, "", logger)
	accSvc := NewAccountService(clientDB, vault, relayCli, sched, logger)
	accSvc.SetAntigravityService(agSvc)

	// --- Audit Fix #1: UpdateNode 脱敏代理密码 `***` 防覆盖验证 ---
	node, err := agSvc.CreateNode(ctx, CreateContainerNodeInput{
		Name:     "node-redact-guard",
		Mode:     domain.ContainerModeLocalDocker,
		HostIP:   "127.0.0.1",
		HostPort: 18901,
		ProxyURL: "http://proxyuser:TopSecretPass99@10.20.30.40:8080",
	})
	if err != nil {
		t.Fatalf("CreateNode failed: %v", err)
	}
	if strings.Contains(node.ProxyURL, "TopSecretPass99") || !strings.Contains(node.ProxyURL, "***") {
		t.Fatalf("expected ContainerNodeView.ProxyURL to redact password, got %q", node.ProxyURL)
	}

	// 前端编辑弹窗原样回传脱敏后的 `http://proxyuser:***@10.20.30.40:8080`
	redactedEcho := node.ProxyURL
	newName := "node-redact-guard-renamed"
	_, err = agSvc.UpdateNode(ctx, node.ID, UpdateContainerNodeInput{
		Name:     &newName,
		ProxyURL: &redactedEcho,
	})
	if err != nil {
		t.Fatalf("UpdateNode with redacted proxy failed: %v", err)
	}
	dbNode, err := clientDB.ContainerNode.Get(ctx, node.ID)
	if err != nil {
		t.Fatalf("get node from db: %v", err)
	}
	if dbNode.ProxyURL != "http://proxyuser:TopSecretPass99@10.20.30.40:8080" {
		t.Fatalf("expected real proxy password preserved in DB, got %q", dbNode.ProxyURL)
	}

	// --- Audit Fix #2: 软删除同名分组后远程补号自动建组不触发 UNIQUE 崩溃 ---
	oldGrp, err := clientDB.Group.Create().
		SetName("agy-prod-pool").
		SetPlatform(domain.PlatformAntigravity).
		SetStatus(domain.StatusDisabled).
		SetDeletedAt(time.Now()).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed soft-deleted group: %v", err)
	}
	_ = oldGrp

	// 同时测试远程推送多格式 Antigravity 载荷（`email----password----1//...` + Google ADC JSON）
	adcRaw := json.RawMessage(`{"type":"authorized_user","client_id":"cid.apps.googleusercontent.com","client_secret":"GOCSPX-sec","refresh_token":"1//0gPushADCToken11","quota_project_id":"push-gcp-01"}`)
	pushRes, err := accSvc.PushAccounts(ctx, RemotePushAccountsInput{
		DefaultPlatform:      domain.PlatformAntigravity,
		DefaultGroupName:     "agy-prod-pool",
		DefaultUpsert:        true,
		DefaultAutoProvision: true,
		Items: []RemotePushAccountItem{
			{
				APIKey: "push1@example.com----LoginPass123----1//0gPushDelimToken22",
			},
			{
				Email:    "push-adc@example.com",
				AuthJSON: adcRaw,
			},
		},
	})
	if err != nil {
		t.Fatalf("PushAccounts failed: %v", err)
	}
	if !pushRes.OK || pushRes.Created != 2 || pushRes.Failed != 0 {
		t.Fatalf("expected 2 created accounts via universal push, got %+v", pushRes)
	}

	// 验证隔离容器目录中已写入真实 agy CLI 识别的 `.gemini/oauth_creds.json` 与 `google_accounts.json`
	pushedNode, err := clientDB.ContainerNode.Get(ctx, pushRes.Results[0].ContainerNodeID)
	if err != nil {
		t.Fatalf("get pushed container node: %v", err)
	}
	oauthCredsPath := filepath.Join(pushedNode.AuthDir, ".gemini", "oauth_creds.json")
	rawOAuth, err := os.ReadFile(oauthCredsPath)
	if err != nil || !strings.Contains(string(rawOAuth), "1//0gPushDelimToken22") || strings.Contains(string(rawOAuth), "LoginPass123") {
		t.Fatalf("unexpected isolated .gemini/oauth_creds.json: err=%v content=%s", err, string(rawOAuth))
	}

	// 验证分组已重建且原软删记录被安全归档重命名
	activeGrp, err := clientDB.Group.Query().
		Where(group.DeletedAtIsNil(), group.NameEQ("agy-prod-pool")).
		Only(ctx)
	if err != nil || activeGrp == nil {
		t.Fatalf("expected recreated active group agy-prod-pool: %v", err)
	}

	// --- Audit Fix #3: AllocateFreeHostPort 高并发无碰撞验证 ---
	const workers = 16
	ports := make([]int, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ports[idx], errs[idx] = agSvc.AllocateFreeHostPort(ctx)
		}(i)
	}
	wg.Wait()
	seenPorts := make(map[int]struct{}, workers)
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d AllocateFreeHostPort failed: %v", i, errs[i])
		}
		if _, dup := seenPorts[ports[i]]; dup {
			t.Fatalf("concurrent AllocateFreeHostPort returned duplicate port %d: all=%v", ports[i], ports)
		}
		seenPorts[ports[i]] = struct{}{}
	}

	// --- ConvertAndImportAccounts 一键全格式转换并自动入池 + 幂等更新 ---
	convOut, err := agSvc.ConvertAndImportAccounts(ctx, ConvertAccountsInput{
		Files: []antigravity.AdapterInputFile{
			{
				Filename: "gemini_creds.json",
				Content:  []byte(`{"access_token":"ya29.imp1","refresh_token":"1//0gImportedToken99","token_type":"Bearer","expiry_date":1893456000000}`),
			},
			{
				Filename: "google_accounts.json",
				Content:  []byte(`{"active":"imported-cli@example.com","old":[]}`),
			},
		},
		AutoImport: true,
		GroupIDs:   []int64{activeGrp.ID},
	})
	if err != nil {
		t.Fatalf("ConvertAndImportAccounts failed: %v", err)
	}
	if convOut.ImportResult == nil || convOut.ImportResult.Created != 1 || len(convOut.ImportResult.Accounts) != 1 {
		t.Fatalf("unexpected ImportResult: %+v", convOut.ImportResult)
	}
	impAcc := convOut.ImportResult.Accounts[0]
	if impAcc.Email != "imported-cli@example.com" {
		t.Fatalf("expected paired email imported-cli@example.com, got %q", impAcc.Email)
	}
	// 确认 refresh_token 已加密落库在 Account.Credentials（严禁明文）
	if _, hasEnc := impAcc.Credentials["refresh_token_encrypted"]; !hasEnc {
		t.Fatalf("expected refresh_token_encrypted in Account.Credentials, got %+v", impAcc.Credentials)
	}
	if _, hasPlain := impAcc.Credentials["refresh_token"]; hasPlain {
		t.Fatalf("expected plaintext refresh_token to be omitted when vault is enabled")
	}
}
