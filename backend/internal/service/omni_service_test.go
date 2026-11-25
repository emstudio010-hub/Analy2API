package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/group"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/prompt"
	"cmd2api/internal/workbuddy"
)

func openOmniTestDB(t *testing.T) (*ent.Client, *crypto.Vault) {
	t.Helper()
	client, err := db.Open(context.Background(), config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: ":memory:",
	})
	if err != nil {
		t.Fatalf("打开测试内存 SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	vault, err := crypto.NewVault([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("构造测试 Vault 失败: %v", err)
	}
	return client, vault
}

func TestPromptService_SeedCrudGroupAndPreview(t *testing.T) {
	ctx := context.Background()
	client, _ := openOmniTestDB(t)

	filterEng := filter.NewDefaultEngine()
	promptEng := prompt.NewDefaultEngine()
	svc := NewPromptService(client, promptEng, filterEng, nil)

	if err := svc.EnsureSeeded(ctx); err != nil {
		t.Fatalf("EnsureSeeded 失败: %v", err)
	}

	// 二次 EnsureSeeded 必须幂等
	if err := svc.EnsureSeeded(ctx); err != nil {
		t.Fatalf("二次 EnsureSeeded 应幂等成功: %v", err)
	}

	cfgOut, err := svc.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig 失败: %v", err)
	}
	if !cfgOut.GlobalEnabled || !strings.Contains(cfgOut.GlobalPrompt, "Ess-Agentic") {
		t.Errorf("默认全局提示词配置不对: enabled=%v prompt=%q", cfgOut.GlobalEnabled, cfgOut.GlobalPrompt)
	}
	if len(cfgOut.Templates) < 4 {
		t.Fatalf("播种模板数量不足: %d", len(cfgOut.Templates))
	}

	// 更新全局提示词
	if err := svc.UpdateGlobal(ctx, true, "GLOBAL_BASELINE[{{model}}]"); err != nil {
		t.Fatalf("UpdateGlobal 失败: %v", err)
	}

	// 新建分组并绑定模板 / 分组自定义提示词
	grp, err := client.Group.Create().
		SetName("agy-dev").
		SetPlatform(domain.PlatformAntigravity).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	customTpl, err := svc.CreateTemplate(ctx, CreatePromptTemplateInput{
		Name:        "team-rust",
		Category:    "backend",
		Content:     "RUST_TEMPLATE[{{group}}]",
		Description: "Rust 规范",
	})
	if err != nil {
		t.Fatalf("CreateTemplate 失败: %v", err)
	}

	// 绑定模板（SystemPrompt 留空走模板回退）
	updatedGrp, err := svc.UpdateGroupPrompt(ctx, grp.ID, domain.PromptModeAppendGlobal, "", &customTpl.ID)
	if err != nil {
		t.Fatalf("UpdateGroupPrompt 失败: %v", err)
	}
	if updatedGrp.PromptTemplateID == nil || *updatedGrp.PromptTemplateID != customTpl.ID {
		t.Fatalf("分组 PromptTemplateID 未生效: %+v", updatedGrp)
	}

	// 在线预览：客户端 system 含 x-anthropic-billing-header 和 Claude Code，应先被 filterEngine 脱敏清洗，再合成多层提示词
	rawClientSys := "x-anthropic-billing-header: cc_version=1.2.3;\nYou are Claude Code, Anthropic's official CLI for Claude."
	preview, err := svc.Preview(ctx, grp.ID, rawClientSys, "gemini-2.5-pro", "anthropic")
	if err != nil {
		t.Fatalf("Preview 失败: %v", err)
	}
	if strings.Contains(strings.ToLower(preview.SanitizedClientSystem), "x-anthropic-billing-header") {
		t.Errorf("Preview 应先剔除计费头，得到: %q", preview.SanitizedClientSystem)
	}
	if !strings.Contains(preview.FinalSystem, "GLOBAL_BASELINE[gemini-2.5-pro]") ||
		!strings.Contains(preview.FinalSystem, "RUST_TEMPLATE[agy-dev]") {
		t.Errorf("Preview 最终合成内容缺少全局或模板层: %q", preview.FinalSystem)
	}
	if len(preview.LayersApplied) != 3 {
		t.Errorf("LayersApplied = %v, want 3 layers", preview.LayersApplied)
	}

	// ResetTemplates 应移除自定义模板、清理分组失效引用并保留内置模板
	afterReset, err := svc.ResetTemplates(ctx)
	if err != nil {
		t.Fatalf("ResetTemplates 失败: %v", err)
	}
	if len(afterReset) != len(prompt.DefaultTemplates()) {
		t.Errorf("ResetTemplates 后数量=%d, want %d", len(afterReset), len(prompt.DefaultTemplates()))
	}
}

func TestAdminKeyService_CreateValidateUpdateDelete(t *testing.T) {
	ctx := context.Background()
	client, _ := openOmniTestDB(t)
	svc := NewAdminKeyService(client)

	// 非法 scope 与非法 IP/CIDR 必须拦截报错
	if _, _, err := svc.Create(ctx, "bad-scope", "superadmin", "", 0); err == nil {
		t.Fatal("非法 scopes 应报错")
	}
	if _, _, err := svc.Create(ctx, "bad-ip", "full", "127.0.0.1, not-an-ip", 0); err == nil {
		t.Fatal("非法 allowedIPs 应报错")
	}
	if _, _, err := svc.Create(ctx, "bad-cidr", "full", "10.0.0.0/99", 0); err == nil {
		t.Fatal("非法 CIDR 应报错")
	}

	plain, row, err := svc.Create(ctx, "ops-automation", "readonly", "127.0.0.1, 10.10.0.0/16", 30)
	if err != nil {
		t.Fatalf("合法 Create 失败: %v", err)
	}
	if !strings.HasPrefix(plain, domain.AdminKeyPrefix) || row.KeyPrefix != plain[:12] {
		t.Errorf("签发密钥前缀不匹配: plain=%q prefix=%q", plain, row.KeyPrefix)
	}
	if row.Scopes != domain.AdminKeyScopeReadOnly || row.ExpiresAt == nil {
		t.Errorf("row 字段不符合预期: %+v", row)
	}

	items, err := svc.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("List 失败: err=%v len=%d", err, len(items))
	}

	newStatus := domain.StatusDisabled
	newIPs := "192.168.1.1"
	updated, err := svc.Update(ctx, row.ID, UpdateAdminKeyInput{
		Status:      &newStatus,
		AllowedIPs:  &newIPs,
		ClearExpiry: true,
	})
	if err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if updated.Status != domain.StatusDisabled || updated.AllowedIps != "192.168.1.1" || updated.ExpiresAt != nil {
		t.Errorf("Update 后字段不符合预期: %+v", updated)
	}

	if err := svc.Delete(ctx, row.ID); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
}

func TestAntigravityService_RemoteMockUploadSyncAndOverview(t *testing.T) {
	ctx := context.Background()
	client, vault := openOmniTestDB(t)

	const mgmtSecret = "mgmt-test-key-999"
	const inferSecret = "sk-infer-test-888"

	var mu sync.Mutex
	storedFiles := map[string]map[string]any{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
			if r.Header.Get("Authorization") != "Bearer "+inferSecret {
				http.Error(w, `{"error":"bad api key"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gemini-2.5-pro"},{"id":"claude-sonnet-4-6"}]}`))

		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			if r.Header.Get("X-Management-Key") != mgmtSecret {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			mu.Lock()
			list := make([]map[string]any, 0, len(storedFiles))
			for _, v := range storedFiles {
				list = append(list, v)
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"files": list})

		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodPost:
			if r.Header.Get("X-Management-Key") != mgmtSecret {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			var raw map[string]any
			_ = json.NewDecoder(r.Body).Decode(&raw)
			fn := r.URL.Query().Get("filename")
			raw["filename"] = fn
			mu.Lock()
			storedFiles[fn] = raw
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "file": raw})

		case strings.HasPrefix(r.URL.Path, "/v8/management/oauth/auth-url"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auth_url":"https://accounts.google.com/o/oauth2/auth?state=st-1","state":"st-1","provider":"antigravity"}`))

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())

	agClient := antigravity.NewClient("", nil)
	dockerMgr := antigravity.NewDockerManager(antigravity.CommandRunnerFunc(func(_ context.Context, _ string, args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "version" {
			return "27.3.1|linux", "", nil
		}
		return "", "", nil
	}))
	agySvc := NewAntigravityService(client, vault, agClient, dockerMgr, t.TempDir(), "", nil)

	envInfo := agySvc.DetectEnv(ctx)
	if !envInfo.DockerAvailable || envInfo.DockerVersion != "27.3.1" {
		t.Errorf("DetectEnv 不符合预期: %+v", envInfo)
	}

	// 1. 创建远程节点
	nodeView, err := agySvc.CreateNode(ctx, CreateContainerNodeInput{
		Name:          "remote-agy-1",
		Mode:          domain.ContainerModeRemoteAPI,
		HostIP:        u.Hostname(),
		HostPort:      port,
		ManagementKey: mgmtSecret,
		APIKey:        inferSecret,
	})
	if err != nil {
		t.Fatalf("CreateNode 失败: %v", err)
	}
	if nodeView.Status != "online" || !nodeView.HasManagementKey || !nodeView.HasAPIKey {
		t.Errorf("CreateNode 返回状态不符合预期: %+v", nodeView)
	}

	// 验证数据库中密钥确实已用 Vault 加密而非明文保存
	rawNodeRow, err := client.ContainerNode.Get(ctx, nodeView.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rawNodeRow.ManagementKeyEnc == mgmtSecret || rawNodeRow.APIKeyEnc == inferSecret {
		t.Fatal("ContainerNode 密钥必须加密入库，严禁存明文")
	}

	// 2. 获取在线 OAuth 授权地址
	oauthOut, err := agySvc.RequestOAuthURL(ctx, nodeView.ID, "antigravity", "")
	if err != nil || oauthOut.State != "st-1" {
		t.Fatalf("RequestOAuthURL 失败: err=%v out=%+v", err, oauthOut)
	}

	// 3. 上传 .json 登录凭证并开启 autoSync（groupIDs 留空自动创建默认 antigravity 分组！）
	credJSON := []byte(`{"type":"antigravity","email":"dev1@example.com","project_id":"proj-alpha","refresh_token":"rt-xxx","priority":25}`)
	upOut, err := agySvc.UploadAuthFile(ctx, nodeView.ID, "dev1.json", credJSON, true, nil)
	if err != nil {
		t.Fatalf("UploadAuthFile 失败: %v", err)
	}
	if upOut.Account == nil || upOut.File.AccountID == nil {
		t.Fatalf("autoSync=true 后应返回已同步的 Account 及 AccountID: %+v", upOut)
	}
	if upOut.Account.Platform != domain.PlatformAntigravity || upOut.Account.Type != domain.AccountTypeOAuthJSON {
		t.Errorf("同步账号 platform/type 不对: platform=%q type=%q", upOut.Account.Platform, upOut.Account.Type)
	}
	if upOut.Account.Email != "dev1@example.com" || upOut.Account.Priority != 25 {
		t.Errorf("同步账号元数据不对: email=%q priority=%d", upOut.Account.Email, upOut.Account.Priority)
	}

	// 验证默认分组 "antigravity" 已自动创建并绑定
	agyGroup, err := client.Group.Query().
		Where(group.PlatformEQ(domain.PlatformAntigravity), group.DeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		t.Fatalf("应自动创建默认 antigravity 分组: %v", err)
	}
	if len(upOut.Account.Edges.Groups) != 1 || upOut.Account.Edges.Groups[0].ID != agyGroup.ID {
		t.Errorf("同步账号未绑定到默认 antigravity 分组: %+v", upOut.Account.Edges.Groups)
	}

	// 4. 再次执行 SyncNodeAccounts 应为幂等 Update（Created=0, Updated=1）
	syncAgain, err := agySvc.SyncNodeAccounts(ctx, nodeView.ID, nil)
	if err != nil {
		t.Fatalf("二次 SyncNodeAccounts 失败: %v", err)
	}
	if syncAgain.Created != 0 || syncAgain.Updated != 1 {
		t.Errorf("二次同步应为幂等更新: created=%d updated=%d", syncAgain.Created, syncAgain.Updated)
	}

	// 5. 验证 RewardsService Overview 聚合统计
	rewardsSvc := NewRewardsService(client, nil, agySvc, nil)
	ov, err := rewardsSvc.Overview(ctx)
	if err != nil {
		t.Fatalf("RewardsService.Overview 失败: %v", err)
	}
	agyPlat := ov.Platforms[domain.PlatformAntigravity]
	if agyPlat.TotalAccounts != 1 || agyPlat.NodesTotal != 1 || agyPlat.NodesOnline != 1 || agyPlat.AuthFilesTotal != 1 {
		t.Errorf("RewardsService AntiGravity 聚合统计不符合预期: %+v", agyPlat)
	}
}

func TestRuntimeSettingsService_PersistHotUpdateAndReload(t *testing.T) {
	ctx := context.Background()
	client, _ := openOmniTestDB(t)
	svc := NewRuntimeSettingsService(client, nil)

	cfg := &config.Config{
		CommandCC: config.CommandCodeConfig{
			FirstTokenTimeout:      300 * time.Second,
			StreamIdleTimeout:      180 * time.Second,
			NonStreamIdleTimeout:   600 * time.Second,
			EmptySystemPlaceholder: true,
			AutoHarvestEnabled:     true,
			AutoHarvestInterval:    6 * time.Hour,
		},
	}
	wbClient := workbuddy.NewClient("", "https://www.codebuddy.cn", nil)

	ft := 420
	si := 240
	nsi := 900
	proxy := "socks5://127.0.0.1:1080"
	emptyPh := false
	autoHarvest := false
	intervalStr := "12h"

	savedMap, err := svc.Update(ctx, cfg, wbClient, RuntimeSettingsInput{
		FirstTokenSeconds:      &ft,
		StreamIdleSeconds:      &si,
		NonStreamIdleSeconds:   &nsi,
		UpstreamProxy:          &proxy,
		EmptySystemPlaceholder: &emptyPh,
		AutoHarvestEnabled:     &autoHarvest,
		AutoHarvestInterval:    &intervalStr,
	})
	if err != nil {
		t.Fatalf("RuntimeSettingsService.Update 失败: %v", err)
	}
	if savedMap[SettingKeyFirstTokenSeconds] != "420" {
		t.Errorf("持久化键值不对: %+v", savedMap)
	}

	// 验证内存 *config.Config 已被即时热更新
	if cfg.CommandCC.FirstTokenTimeout != 420*time.Second ||
		cfg.CommandCC.StreamIdleTimeout != 240*time.Second ||
		cfg.CommandCC.NonStreamIdleTimeout != 900*time.Second ||
		cfg.CommandCC.UpstreamProxy != "socks5://127.0.0.1:1080" ||
		cfg.CommandCC.EmptySystemPlaceholder ||
		cfg.CommandCC.AutoHarvestEnabled ||
		cfg.CommandCC.AutoHarvestInterval != 12*time.Hour {
		t.Errorf("内存 cfg 热更新不符合预期: %+v", cfg.CommandCC)
	}

	// 模拟进程重启后 LoadIntoConfig 从数据库恢复覆盖
	freshCfg := &config.Config{}
	if err := svc.LoadIntoConfig(ctx, freshCfg); err != nil {
		t.Fatalf("LoadIntoConfig 失败: %v", err)
	}
	if freshCfg.CommandCC.FirstTokenTimeout != 420*time.Second ||
		freshCfg.CommandCC.StreamIdleTimeout != 240*time.Second ||
		freshCfg.CommandCC.NonStreamIdleTimeout != 900*time.Second ||
		freshCfg.CommandCC.UpstreamProxy != "socks5://127.0.0.1:1080" {
		t.Errorf("LoadIntoConfig 恢复结果不对: %+v", freshCfg.CommandCC)
	}
}
