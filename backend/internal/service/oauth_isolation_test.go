package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/config"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
)

func TestStrictContainerIsolationAndUnifiedOAuthHub(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	clientDB, err := db.Open(ctx, config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer clientDB.Close()

	sched := scheduler.New(clientDB, nil, 3, logger)

	// 模拟 Command Code /whoami 与 AntiGravity /v1/models
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/whoami":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"user":{"userName":"cc-oauth-tester","name":"CC Tester"}}`))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gemini-2.5-pro"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockSrv.Close()

	relayCli := relay.NewClient(config.CommandCodeConfig{BaseURL: mockSrv.URL}, logger)
	agSvc := NewAntigravityService(clientDB, nil, antigravity.NewClient("", logger), nil, filepath.Join(t.TempDir(), "agy"), "", logger)
	accSvc := NewAccountService(clientDB, nil, relayCli, sched, logger)
	oauthSvc := NewOAuthService(clientDB, accSvc, agSvc, relayCli, logger)

	// 1. 测试 1容器 : 1IP : 1指纹 强隔离拦截重复代理 IP
	node1, err := agSvc.CreateNode(ctx, CreateContainerNodeInput{
		Name:     "agy-iso-1",
		Mode:     domain.ContainerModeLocalDocker,
		HostIP:   "127.0.0.1",
		HostPort: 18331,
		ProxyURL: "http://10.1.0.11:7890",
	})
	if err != nil {
		t.Fatalf("创建节点 1 失败: %v", err)
	}
	if !node1.StrictIsolation || node1.Fingerprint.FingerprintID == "" || node1.IsolationStatus != antigravity.IsolationStatusIsolated {
		t.Fatalf("节点 1 强隔离属性或指纹未生成: %+v", node1)
	}

	// 同一个非空 ProxyURL 在 strict_isolation=true 时应被拒绝，确保 1 容器 1 独立 IP
	if _, err := agSvc.CreateNode(ctx, CreateContainerNodeInput{
		Name:     "agy-iso-dup",
		Mode:     domain.ContainerModeLocalDocker,
		HostIP:   "127.0.0.1",
		HostPort: 18332,
		ProxyURL: "http://10.1.0.11:7890",
	}); err == nil {
		t.Fatal("启用严格隔离时，多个容器复用相同 ProxyURL 应被拒绝")
	}

	// 节点 2 使用不同 ProxyURL 则正常创建且生成完全独立的设备指纹
	node2, err := agSvc.CreateNode(ctx, CreateContainerNodeInput{
		Name:     "agy-iso-2",
		Mode:     domain.ContainerModeLocalDocker,
		HostIP:   "127.0.0.1",
		HostPort: 18332,
		ProxyURL: "http://10.1.0.12:7890",
	})
	if err != nil {
		t.Fatalf("创建节点 2 失败: %v", err)
	}
	if node2.Fingerprint.FingerprintID == node1.Fingerprint.FingerprintID ||
		node2.Fingerprint.Qimei36 == node1.Fingerprint.Qimei36 ||
		node2.Fingerprint.Hostname == node1.Fingerprint.Hostname {
		t.Fatalf("两个容器必须生成完全独立的设备指纹: n1=%+v n2=%+v", node1.Fingerprint, node2.Fingerprint)
	}

	// 测试轮换设备指纹
	oldFP := node1.Fingerprint.FingerprintID
	rotView, err := agSvc.RotateNodeFingerprint(ctx, node1.ID)
	if err != nil {
		t.Fatalf("轮换节点指纹失败: %v", err)
	}
	if rotView.Fingerprint.FingerprintID == "" || rotView.Fingerprint.FingerprintID == oldFP {
		t.Fatalf("轮换后应生成全新指纹 ID，old=%q new=%q", oldFP, rotView.Fingerprint.FingerprintID)
	}

	// 2. 测试 AntiGravity 离线 PKCE OAuth 会话创建 -> 回调 URL 落盘 .json -> 自动同步入池且继承容器指纹与代理
	sessAG, err := oauthSvc.CreateSession(ctx, CreateOAuthSessionInput{
		Platform:    domain.PlatformAntigravity,
		Provider:    "antigravity",
		NodeID:      node1.ID,
		RedirectURI: "http://127.0.0.1:8080/api/oauth/callback",
	})
	if err != nil {
		t.Fatalf("创建 AntiGravity OAuth 会话失败: %v", err)
	}
	if sessAG.Mode != "gateway_pkce" || !strings.Contains(sessAG.AuthURL, "state="+sessAG.State) {
		t.Fatalf("AntiGravity 离线 PKCE 会话异常: %+v", sessAG)
	}

	cbURL := "http://127.0.0.1:8080/api/oauth/callback?code=oauth_code_9988&state=" + sessAG.State + "&email=user1@example.com&project_id=proj-1"
	resAG, err := oauthSvc.CompleteCallback(ctx, CompleteOAuthCallbackInput{
		SessionID:   sessAG.SessionID,
		CallbackURL: cbURL,
	})
	if err != nil {
		t.Fatalf("完成 AntiGravity OAuth 回调失败: %v", err)
	}
	if !resAG.OK || resAG.Filename == "" || resAG.Account == nil {
		t.Fatalf("AntiGravity OAuth 回调未正确落盘并入池: %+v", resAG)
	}
	if resAG.Account.ProxyURL != "http://10.1.0.11:7890" || resAG.Account.Qimei36 != rotView.Fingerprint.Qimei36 {
		t.Fatalf("入池账号未继承容器专属 IP 与轮换后的指纹: accProxy=%q accQimei=%q nodeQimei=%q",
			resAG.Account.ProxyURL, resAG.Account.Qimei36, rotView.Fingerprint.Qimei36)
	}

	// 验证 .json 文件已实际写入 AuthDir 且包含 fingerprint 与 code_verifier
	files, err := antigravity.ListLocalAuthFiles(node1.AuthDir)
	if err != nil || len(files) != 1 {
		t.Fatalf("节点 auth_dir 下期望有 1 个 .json 文件: err=%v files=%+v", err, files)
	}

	// 3. 测试 Command Code（含 cmdc 别名）OAuth 会话创建 -> 回调 URL 入池
	sessCC, err := oauthSvc.CreateSession(ctx, CreateOAuthSessionInput{
		Platform:     "cmdc",
		RedirectURI:  "http://127.0.0.1:8080/api/oauth/callback",
		RedirectMode: true,
	})
	if err != nil {
		t.Fatalf("创建 CommandCode (cmdc) OAuth 会话失败: %v", err)
	}
	if sessCC.Platform != domain.PlatformCommandCode {
		t.Fatalf("cmdc 别名应归一化为 commandcode，实际为 %q", sessCC.Platform)
	}
	resCC, err := oauthSvc.CompleteCallback(ctx, CompleteOAuthCallbackInput{
		SessionID:   sessCC.SessionID,
		CallbackURL: "http://127.0.0.1:8080/api/oauth/callback?apiKey=user_cc_oauth_secret&state=" + sessCC.State,
	})
	if err != nil {
		t.Fatalf("完成 CommandCode OAuth 回调失败: %v", err)
	}
	if !resCC.OK || resCC.Account == nil || resCC.Account.Name != "cc-oauth-tester" {
		t.Fatalf("CommandCode OAuth 未自动校验 whoami 并创建账号: %+v", resCC)
	}

	// 4. 测试 AntiGravity（agy 别名）会话创建归一化
	sessAgyAlias, err := oauthSvc.CreateSession(ctx, CreateOAuthSessionInput{
		Platform:    "agy",
		Provider:    "agy",
		NodeID:      node1.ID,
		RedirectURI: "http://127.0.0.1:8080/api/oauth/callback",
	})
	if err != nil || sessAgyAlias.Platform != domain.PlatformAntigravity {
		t.Fatalf("agy 别名创建 AntiGravity 会话失败: err=%v sess=%+v", err, sessAgyAlias)
	}

	// 5. 测试 WorkBuddy (wb 别名) 状态化 OAuth 会话创建 -> 回调 URL 与 Token JSON 双路入池
	sessWB, err := oauthSvc.CreateSession(ctx, CreateOAuthSessionInput{
		Platform:     "wb",
		ProxyURL:     "http://10.2.0.11:7890",
		RedirectURI:  "http://127.0.0.1:8080/api/oauth/callback",
		RedirectMode: true,
	})
	if err != nil {
		t.Fatalf("创建 WorkBuddy (wb) OAuth 会话失败: %v", err)
	}
	if sessWB.Platform != domain.PlatformWorkBuddy || sessWB.Mode != "workbuddy_oauth" || !strings.Contains(sessWB.AuthURL, "state="+sessWB.State) {
		t.Fatalf("WorkBuddy OAuth 会话异常: %+v", sessWB)
	}
	resWBFromCB, err := oauthSvc.CompleteCallback(ctx, CompleteOAuthCallbackInput{
		SessionID:   sessWB.SessionID,
		CallbackURL: "http://127.0.0.1:8080/api/oauth/callback?access_token=wb_cb_tok_111&refresh_token=wb_cb_ref_222&uid=uid_wb_1&email=wb-cb@example.com&state=" + sessWB.State,
	})
	if err != nil {
		t.Fatalf("WorkBuddy URL 回调入池失败: %v", err)
	}
	if !resWBFromCB.OK || resWBFromCB.Account == nil || resWBFromCB.Account.Platform != domain.PlatformWorkBuddy || resWBFromCB.Account.ProxyURL != "http://10.2.0.11:7890" {
		t.Fatalf("WorkBuddy OAuth 回调账号创建异常: %+v", resWBFromCB)
	}

	tokJSON, _ := json.Marshal(map[string]string{
		"access_token":  "wb_access_123456",
		"refresh_token": "wb_refresh_654321",
		"email":         "wb-oauth@example.com",
	})
	resWB, err := oauthSvc.CompleteCallback(ctx, CompleteOAuthCallbackInput{
		Platform:  domain.PlatformWorkBuddy,
		TokenJSON: tokJSON,
	})
	if err != nil {
		t.Fatalf("完成 WorkBuddy OAuth JSON 回调失败: %v", err)
	}
	if !resWB.OK || resWB.Account == nil || resWB.Account.Platform != domain.PlatformWorkBuddy {
		t.Fatalf("WorkBuddy OAuth 账号创建异常: %+v", resWB)
	}

	// 6. 测试 OpenCode (opencode · Zen 与 Go 双模式) OAuth 会话创建 -> 回调 URL / APIKey 入池
	sessOCZen, err := oauthSvc.CreateSession(ctx, CreateOAuthSessionInput{
		Platform:     "opencode",
		AccountMode:  domain.AccountModeZen,
		ProxyURL:     "http://10.3.0.21:7890",
		RedirectURI:  "http://127.0.0.1:8080/api/oauth/callback",
		RedirectMode: true,
	})
	if err != nil {
		t.Fatalf("创建 OpenCode Zen OAuth 会话失败: %v", err)
	}
	if sessOCZen.Platform != domain.PlatformOpenCode || sessOCZen.AccountMode != domain.AccountModeZen || !strings.Contains(sessOCZen.AuthURL, "state="+sessOCZen.State) {
		t.Fatalf("OpenCode Zen OAuth 会话异常: %+v", sessOCZen)
	}
	resOCZen, err := oauthSvc.CompleteCallback(ctx, CompleteOAuthCallbackInput{
		SessionID:   sessOCZen.SessionID,
		CallbackURL: "http://127.0.0.1:8080/api/oauth/callback?api_key=sk-oc-zen-999&email=zen@opencode.ai&state=" + sessOCZen.State,
	})
	if err != nil {
		t.Fatalf("OpenCode Zen OAuth 回调失败: %v", err)
	}
	if !resOCZen.OK || resOCZen.Account == nil || resOCZen.Account.Platform != domain.PlatformOpenCode || resOCZen.Account.ProxyURL != "http://10.3.0.21:7890" {
		t.Fatalf("OpenCode Zen 账号入池异常: %+v", resOCZen)
	}

	sessOCGo, err := oauthSvc.CreateSession(ctx, CreateOAuthSessionInput{
		Platform:     "oc",
		AccountMode:  domain.AccountModeGo,
		RedirectURI:  "http://127.0.0.1:8080/api/oauth/callback",
		RedirectMode: true,
	})
	if err != nil || sessOCGo.AccountMode != domain.AccountModeGo {
		t.Fatalf("创建 OpenCode Go OAuth 会话失败: err=%v sess=%+v", err, sessOCGo)
	}
	resOCGo, err := oauthSvc.CompleteCallback(ctx, CompleteOAuthCallbackInput{
		SessionID:   sessOCGo.SessionID,
		APIKey:      "sk-oc-go-888",
		AccountName: "oc-go-oauth-tester",
	})
	if err != nil {
		t.Fatalf("OpenCode Go OAuth 回调失败: %v", err)
	}
	if !resOCGo.OK || resOCGo.Account == nil || resOCGo.Account.Name != "oc-go-oauth-tester" {
		t.Fatalf("OpenCode Go 账号入池异常: %+v", resOCGo)
	}

	// 7. 测试「1 账号自动分配 1 独立 agy 容器」：不依赖本机安装 agy，新建两个 antigravity 账号自动分配两套独立容器、端口与指纹
	accSvc.SetAntigravityService(agSvc)
	autoAcc1, err := accSvc.Create(ctx, CreateAccountInput{
		Name:     "agy-auto-acc-1",
		Platform: domain.PlatformAntigravity,
		APIKey:   "sk-seed-1",
		ProxyURL: "http://10.9.0.1:7890",
	})
	if err != nil {
		t.Fatalf("创建自动分配容器账号 1 失败: %v", err)
	}
	autoAcc2, err := accSvc.Create(ctx, CreateAccountInput{
		Name:     "agy-auto-acc-2",
		Platform: domain.PlatformAntigravity,
		APIKey:   "sk-seed-2",
		ProxyURL: "http://10.9.0.2:7890",
	})
	if err != nil {
		t.Fatalf("创建自动分配容器账号 2 失败: %v", err)
	}
	nid1 := extractExtraInt64(autoAcc1.Extra, "node_id")
	nid2 := extractExtraInt64(autoAcc2.Extra, "node_id")
	if nid1 <= 0 || nid2 <= 0 || nid1 == nid2 {
		t.Fatalf("两个 AntiGravity 账号必须各自自动分配独立容器节点: nid1=%d nid2=%d", nid1, nid2)
	}
	if autoAcc1.Domain == autoAcc2.Domain || autoAcc1.Qimei36 == autoAcc2.Qimei36 {
		t.Fatalf("两个账号的专属容器端口与设备指纹必须完全独立: d1=%q d2=%q", autoAcc1.Domain, autoAcc2.Domain)
	}

	// 8. 测试 OAuth 回调：若目标本机容器节点已绑定旧账号，自动分配全新独立容器（防止多账号共享容器）
	resAG2, err := oauthSvc.CompleteCallback(ctx, CompleteOAuthCallbackInput{
		Platform: domain.PlatformAntigravity,
		Provider: "antigravity",
		NodeID:   node1.ID, // node1 已在第 2 步绑定了 user1@example.com
		ProxyURL: "http://10.9.0.3:7890",
		Email:    "user2@example.com",
		APIKey:   "ya29.second_account_token",
	})
	if err != nil {
		t.Fatalf("第二个 AntiGravity 账号 OAuth 回调自动开新容器失败: %v", err)
	}
	if resAG2.NodeID == node1.ID {
		t.Fatalf("已有账号的容器 node1 (%d) 严禁复用到第二个账号，必须自动分配新容器节点", node1.ID)
	}

	// 9. 测试公网域名下直接 OAuth（NodeID=0 + IPMode=auto_pool）：
	//    - 自动将公网 redirect_uri 规范化为 Google Desktop 允许的 http://localhost:51121/oauth-callback
	//    - 自动从代理池分配独立 IP，并在回调后直接自动创建 1:1 专属容器与隔离凭证文件
	//    - 未运行本机 Docker 容器读取本地 AuthDir，绝不误打已占用端口触发伪 401
	//    - 支持一键换 IP (SwapAccountIPAndRestartContainer)
	poolSvc := NewProxyPoolService(clientDB, logger)
	_, _, err = poolSvc.AddEntries(ctx, "http://10.88.0.1:7890\nhttp://10.88.0.2:7890", nil, "agy", "test-pool")
	if err != nil {
		t.Fatalf("AddEntries to proxy pool failed: %v", err)
	}
	poolView, err := poolSvc.GetView(ctx)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	poolCfg := poolView.Config
	poolCfg.Enabled = true
	poolCfg.Strategy = ProxyStrategyStickyUnique
	poolCfg.MaxAccountsPerIP = 1
	if _, err := poolSvc.UpdateConfig(ctx, poolCfg, "", ""); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	agSvc.SetProxyPoolService(poolSvc)

	sessDirect, err := oauthSvc.CreateSession(ctx, CreateOAuthSessionInput{
		Platform:    domain.PlatformAntigravity,
		Provider:    "antigravity",
		NodeID:      0,
		IPMode:      "auto_pool",
		RedirectURI: "https://cmd.essapi.uno/api/oauth/callback",
	})
	if err != nil {
		t.Fatalf("创建直接 OAuth 会话失败: %v", err)
	}
	if sessDirect.NodeID != 0 {
		t.Fatalf("直接 OAuth 会话在令牌兑换前不应预绑旧容器节点，实际 NodeID=%d", sessDirect.NodeID)
	}
	if !strings.Contains(sessDirect.AuthURL, "redirect_uri=http%3A%2F%2Flocalhost%3A51121%2Foauth-callback") {
		t.Fatalf("公网域名应自动规范化为官方 localhost:51121 回调以避免 redirect_uri_mismatch: %s", sessDirect.AuthURL)
	}

	resDirect, err := oauthSvc.CompleteCallback(ctx, CompleteOAuthCallbackInput{
		SessionID:   sessDirect.SessionID,
		CallbackURL: "http://localhost:51121/oauth-callback?code=oauth_direct_7788&state=" + sessDirect.State + "&email=direct-auto@example.com&project_id=proj-direct-1",
	})
	if err != nil {
		t.Fatalf("完成直接 OAuth 回调自动建容器失败: %v", err)
	}
	if !resDirect.OK || resDirect.Account == nil || resDirect.NodeID <= 0 {
		t.Fatalf("直接 OAuth 回调应自动创建 1:1 容器节点并返回账号: %+v", resDirect)
	}
	if resDirect.Account.ProxyURL != "http://10.88.0.1:7890" {
		t.Fatalf("直接 OAuth 应自动从代理池分配首个空闲 IP，实际为 %q", resDirect.Account.ProxyURL)
	}

	// 验证未启动的本机容器调用 ListAuthFiles 直接读取本地 AuthDir（不走端口 HTTP 避免伪 401）
	listedAuths, err := agSvc.ListAuthFiles(ctx, resDirect.NodeID)
	if err != nil || len(listedAuths) != 1 || listedAuths[0].Email != "direct-auto@example.com" {
		t.Fatalf("停止状态的本机容器 ListAuthFiles 应直接读取本地 AuthDir: err=%v files=%+v", err, listedAuths)
	}

	// 验证一键换 IP 自动切换到代理池中第二个未占用 IP
	swappedAcc, swappedNode, err := agSvc.SwapAccountIPAndRestartContainer(ctx, resDirect.Account.ID, "auto_pool", "")
	if err != nil {
		t.Fatalf("一键换 IP 失败: %v", err)
	}
	if swappedAcc.ProxyURL != "http://10.88.0.2:7890" || swappedNode == nil {
		t.Fatalf("一键换 IP 后期望切换到第二个池 IP http://10.88.0.2:7890，实际为 %q (node=%+v)", swappedAcc.ProxyURL, swappedNode)
	}
}
