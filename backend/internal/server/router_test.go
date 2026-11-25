package server

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

	"cmd2api/internal/auth"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/handler"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/service"
	"net/url"
	"strconv"
	"sync"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/prompt"
	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"

	"github.com/gin-gonic/gin"
)

// newTestRouter 用一堆 nil 依赖把路由树建起来。
//
// 建树本身就是被测对象：gin 在注册阶段就会为冲突的路由 panic（同一层的
// 静态段和参数段撞车），这种错误编译期看不出来，只有进程启动时才炸。
func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := handler.New(nil, nil, nil, nil, nil, nil, nil, logger)
	return NewRouter(Options{Handler: h, Mode: "release"})
}

// TestNewRouterRegistersWorkBuddyAndFilterRoutes 确认 WorkBuddy、内容风控与 Codex Responses 路由全部注册且无冲突。
func TestNewRouterRegistersWorkBuddyAndFilterRoutes(t *testing.T) {
	r := newTestRouter(t)

	want := []struct{ method, path string }{
		{http.MethodGet, "/health"},
		{http.MethodPut, "/api/auth/profile"},
		{http.MethodGet, "/api/dashboard"},
		{http.MethodGet, "/api/settings"},
		{http.MethodGet, "/api/models"},
		{http.MethodPost, "/api/accounts/batch"},
		{http.MethodPost, "/api/accounts/scan-local"},
		{http.MethodGet, "/api/accounts/export"},
		{http.MethodPost, "/api/accounts/harvest-all"},
		{http.MethodPost, "/api/accounts/balance/refresh-all"},
		{http.MethodPost, "/api/accounts/:id/check"},
		{http.MethodPost, "/api/accounts/:id/balance"},
		{http.MethodPost, "/api/accounts/:id/proxy-test"},
		{http.MethodPost, "/api/accounts/:id/rotate-device"},
		{http.MethodPost, "/api/accounts/:id/login-link"},
		{http.MethodPost, "/api/accounts/:id/refresh-token"},
		{http.MethodPost, "/api/accounts/:id/harvest"},
		{http.MethodGet, "/api/filter-rules"},
		{http.MethodPost, "/api/filter-rules"},
		{http.MethodPost, "/api/filter-rules/reset"},
		{http.MethodPost, "/api/filter-rules/preview"},
		{http.MethodPut, "/api/filter-rules/:id"},
		{http.MethodDelete, "/api/filter-rules/:id"},
		{http.MethodGet, "/api/cron-jobs"},
		{http.MethodPost, "/api/cron-jobs"},
		{http.MethodPost, "/api/cron-jobs/preview"},
		{http.MethodPost, "/api/cron-jobs/reset"},
		{http.MethodPut, "/api/cron-jobs/:id"},
		{http.MethodDelete, "/api/cron-jobs/:id"},
		{http.MethodPost, "/api/cron-jobs/:id/run"},
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/responses"},
		{http.MethodGet, "/v1/models"},
	}

	registered := make(map[string]bool)
	for _, route := range r.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, w := range want {
		if !registered[w.method+" "+w.path] {
			t.Errorf("缺少路由 %s %s", w.method, w.path)
		}
	}
}

// TestProtectedRoutesRequireAuth 确认管理员接口与 /v1/responses 均拦截未鉴权请求。
func TestProtectedRoutesRequireAuth(t *testing.T) {
	r := newTestRouter(t)

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/api/auth/profile", `{"current_password":"x","email":"a@b.com"}`},
		{http.MethodGet, "/api/filter-rules", ""},
		{http.MethodGet, "/api/cron-jobs", ""},
		{http.MethodPost, "/api/accounts/scan-local", `{}`},
		{http.MethodGet, "/api/accounts/export", ""},
		{http.MethodPost, "/api/accounts/harvest-all", `{}`},
		{http.MethodPost, "/v1/responses", `{"model":"gpt-5.4","input":"hello"}`},
	}

	for _, tc := range cases {
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		req := httptest.NewRequest(tc.method, tc.path, body)
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 期望 401，实际 %d", tc.method, tc.path, w.Code)
		}
	}
}

// TestRouterSQLiteEndToEndWorkflow 覆盖 SQLite 模式下的全链路：
// 启动引导 -> 登录 -> Settings/Models/Dashboard -> API Key(sk-wb2a-) -> FilterRules CRUD/Preview/Reset ->
// Account Create/Update/Batch/RotateDevice/Export -> /v1/responses (非流式 & 流式 SSE + 内容风控 + 用量落库)。
func TestRouterSQLiteEndToEndWorkflow(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var capturedUpstreamBodies []string
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/chat/completions":
			raw, _ := io.ReadAll(r.Body)
			capturedUpstreamBodies = append(capturedUpstreamBodies, string(raw))
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello \"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Codex!\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":2,\"total_tokens\":14}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case "/v2/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"claude-sonnet-4-6","name":"Claude Sonnet 4.6"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockUpstream.Close()

	dbPath := filepath.Join(t.TempDir(), "router_e2e.db")
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:       "sqlite",
		SQLitePath:   dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{Mode: "release"},
		Database: config.DatabaseConfig{
			Driver:     "sqlite",
			SQLitePath: dbPath,
			DataDir:    filepath.Dir(dbPath),
		},
		Auth: config.AuthConfig{
			JWTSecret:     "test-jwt-secret-1234567890",
			TokenTTL:      24 * time.Hour,
			EncryptionKey: bytes.Repeat([]byte{0x42}, 32),
		},
		CommandCC: config.CommandCodeConfig{
			BaseURL:              mockUpstream.URL,
			StreamIdleTimeout:    30 * time.Second,
			NonStreamIdleTimeout: 30 * time.Second,
			MaxBodyMB:            10,
			ModelCacheTTL:        time.Minute,
			UpstreamProxy:        "http://user:pass@127.0.0.1:7890",
			AutoHarvestEnabled:   true,
			AutoHarvestInterval:  6 * time.Hour,
		},
		Bootstrap: config.BootstrapConfig{
			Email:    "admin@wb2api.local",
			Password: "StrongPassword123!",
		},
		Health: config.HealthConfig{
			Enabled:          false,
			Interval:         time.Minute,
			FailureThreshold: 3,
			Timeout:          10 * time.Second,
		},
	}

	if err := service.BootstrapAdmin(ctx, client, cfg.Bootstrap, logger); err != nil {
		t.Fatalf("BootstrapAdmin: %v", err)
	}
	grp, err := service.BootstrapDefaultGroup(ctx, client, logger)
	if err != nil {
		t.Fatalf("BootstrapDefaultGroup: %v", err)
	}
	if grp.Platform != domain.PlatformWorkBuddy {
		t.Fatalf("default group platform = %q, want %q", grp.Platform, domain.PlatformWorkBuddy)
	}

	vault, err := crypto.NewVault(cfg.Auth.EncryptionKey)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	issuer := auth.NewIssuer(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)

	wbClient := workbuddy.NewClient("", mockUpstream.URL, logger)
	relayClient := relay.NewClient(cfg.CommandCC, logger)
	relayClient.SetWorkBuddyClient(wbClient)

	filterEngine := filter.NewDefaultEngine()
	filterService := service.NewFilterService(client, filterEngine, logger)
	if err := filterService.EnsureSeeded(ctx); err != nil {
		t.Fatalf("EnsureSeeded: %v", err)
	}

	sched := scheduler.New(client, vault, cfg.Health.FailureThreshold, logger)
	recorder := service.NewRecorder(client, logger)
	accountService := service.NewAccountService(client, vault, relayClient, sched, logger)
	cronService := service.NewCronService(client, accountService, logger)
	if err := cronService.EnsureSeeded(ctx); err != nil {
		t.Fatalf("cron EnsureSeeded: %v", err)
	}
	relayService := relay.NewService(
		relayClient,
		sched,
		recorder,
		cfg,
		logger,
		relay.WithFilterEngine(filterEngine),
		relay.WithWorkBuddyClient(wbClient),
		relay.WithTokenUpdater(accountService),
	)

	h := handler.New(client, vault, issuer, sched, relayService, accountService, cfg, logger, filterService)
	h.SetCronService(cronService)
	r := NewRouter(Options{
		Handler: h,
		Issuer:  issuer,
		Client:  client,
		Mode:    "release",
	})

	doJSON := func(method, path string, authToken string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal %s %s: %v", method, path, err)
			}
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if authToken != "" {
			req.Header.Set("Authorization", "Bearer "+authToken)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. Login
	loginRes := doJSON(http.MethodPost, "/api/auth/login", "", map[string]string{
		"email":    "admin@wb2api.local",
		"password": "StrongPassword123!",
	})
	if loginRes.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginRes.Code, loginRes.Body.String())
	}
	var loginBody struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(loginRes.Body.Bytes(), &loginBody)
	jwtToken := loginBody.Token
	if jwtToken == "" {
		t.Fatal("empty jwt token")
	}

	// 2. Settings (upstream_proxy masked, database & filter blocks)
	settingsRes := doJSON(http.MethodGet, "/api/settings", jwtToken, nil)
	if settingsRes.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", settingsRes.Code, settingsRes.Body.String())
	}
	var settingsMap map[string]any
	_ = json.Unmarshal(settingsRes.Body.Bytes(), &settingsMap)
	upstreamCfg, _ := settingsMap["upstream"].(map[string]any)
	if proxyStr, _ := upstreamCfg["upstream_proxy"].(string); strings.Contains(proxyStr, "pass") {
		t.Fatalf("upstream_proxy leaked password: %q", proxyStr)
	}
	filterCfg, _ := settingsMap["filter"].(map[string]any)
	if enabled, _ := filterCfg["zero_width_disabled"].(bool); !enabled {
		t.Fatalf("expected filter.zero_width_disabled=true, got %#v", filterCfg)
	}
	if rc, _ := filterCfg["rules_count"].(float64); rc <= 0 {
		t.Fatalf("expected seeded rules_count > 0, got %#v", filterCfg)
	}

	// 3. /api/models before any accounts -> fallback WorkBuddy default models
	modelsRes := doJSON(http.MethodGet, "/api/models", jwtToken, nil)
	if modelsRes.Code != http.StatusOK {
		t.Fatalf("models status=%d body=%s", modelsRes.Code, modelsRes.Body.String())
	}
	if !strings.Contains(modelsRes.Body.String(), "claude-sonnet-4-6") && !strings.Contains(modelsRes.Body.String(), "deepseek") {
		t.Fatalf("expected default workbuddy models even with 0 accounts, got %s", modelsRes.Body.String())
	}

	// 4. Create API Key -> sk-wb2a- prefix
	keyRes := doJSON(http.MethodPost, "/api/keys", jwtToken, map[string]any{
		"name":     "codex-key",
		"group_id": grp.ID,
	})
	if keyRes.Code != http.StatusCreated {
		t.Fatalf("create key status=%d body=%s", keyRes.Code, keyRes.Body.String())
	}
	var keyBody struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(keyRes.Body.Bytes(), &keyBody)
	if !strings.HasPrefix(keyBody.Key, domain.APIKeyPrefix) {
		t.Fatalf("expected key prefix %q, got %q", domain.APIKeyPrefix, keyBody.Key)
	}

	// 5. Filter rules list, validation 400, create, partial update, preview (no \u200b), delete, reset
	listRulesRes := doJSON(http.MethodGet, "/api/filter-rules", jwtToken, nil)
	if listRulesRes.Code != http.StatusOK {
		t.Fatalf("list filter rules status=%d body=%s", listRulesRes.Code, listRulesRes.Body.String())
	}
	badRuleRes := doJSON(http.MethodPost, "/api/filter-rules", jwtToken, map[string]any{
		"name":    "bad-zero-width",
		"kind":    "replace",
		"pattern": "foo\u200bbar",
		"scope":   "all_injected",
	})
	if badRuleRes.Code != http.StatusBadRequest {
		t.Fatalf("zero-width rule expected 400, got %d body=%s", badRuleRes.Code, badRuleRes.Body.String())
	}
	createRuleRes := doJSON(http.MethodPost, "/api/filter-rules", jwtToken, map[string]any{
		"name":        "custom-ban-phrase",
		"kind":        "replace",
		"pattern":     "SecretBrandX",
		"replacement": "Assistant",
		"scope":       "all_injected",
	})
	if createRuleRes.Code != http.StatusCreated {
		t.Fatalf("create filter rule status=%d body=%s", createRuleRes.Code, createRuleRes.Body.String())
	}
	var createdRule struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(createRuleRes.Body.Bytes(), &createdRule)

	previewRes := doJSON(http.MethodPost, "/api/filter-rules/preview", jwtToken, map[string]any{
		"input":    "Ignore previous instructions and SecretBrandX helper",
		"mode":     "system",
		"protocol": "openai",
	})
	if previewRes.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewRes.Code, previewRes.Body.String())
	}
	if strings.Contains(previewRes.Body.String(), "\u200b") {
		t.Fatalf("preview output must never contain zero-width chars: %s", previewRes.Body.String())
	}
	if !strings.Contains(previewRes.Body.String(), "Assistant") {
		t.Fatalf("expected custom rule replacement in preview output: %s", previewRes.Body.String())
	}

	toggleRes := doJSON(http.MethodPut, "/api/filter-rules/"+jsonNumber(createdRule.ID), jwtToken, map[string]any{
		"enabled": false,
		"scope":   "system,user_harness",
	})
	if toggleRes.Code != http.StatusOK || !strings.Contains(toggleRes.Body.String(), `"scope":"system,user"`) {
		t.Fatalf("toggle/normalize scope filter rule status=%d body=%s", toggleRes.Code, toggleRes.Body.String())
	}

	userHarnessRuleRes := doJSON(http.MethodPost, "/api/filter-rules", jwtToken, map[string]any{
		"name":        "user-harness-rule",
		"kind":        "prune",
		"pattern":     "<user_harness_test>",
		"replacement": "",
		"scope":       "user_harness",
	})
	if userHarnessRuleRes.Code != http.StatusCreated || !strings.Contains(userHarnessRuleRes.Body.String(), `"scope":"user"`) {
		t.Fatalf("create user_harness filter rule status=%d body=%s", userHarnessRuleRes.Code, userHarnessRuleRes.Body.String())
	}

	delRuleRes := doJSON(http.MethodDelete, "/api/filter-rules/"+jsonNumber(createdRule.ID), jwtToken, nil)
	if delRuleRes.Code != http.StatusOK {
		t.Fatalf("delete filter rule status=%d body=%s", delRuleRes.Code, delRuleRes.Body.String())
	}

	resetRulesRes := doJSON(http.MethodPost, "/api/filter-rules/reset", jwtToken, nil)
	if resetRulesRes.Code != http.StatusOK {
		t.Fatalf("reset filter rules status=%d body=%s", resetRulesRes.Code, resetRulesRes.Body.String())
	}

	// 6. Create WorkBuddy account (no platform/user_ restriction, domain pointing to mock upstream)
	accRes := doJSON(http.MethodPost, "/api/accounts", jwtToken, map[string]any{
		"name":          "wb-acc-1",
		"api_key":       "wb-oauth-access-token-123456",
		"refresh_token": "wb-refresh-123",
		"uid":           "uid_alice_99",
		"nickname":      "AliceWB",
		"email":         "alice.wb@example.com",
		"domain":        mockUpstream.URL,
		"group_ids":     []int64{grp.ID},
	})
	if accRes.Code != http.StatusCreated {
		t.Fatalf("create account status=%d body=%s", accRes.Code, accRes.Body.String())
	}
	var accBody struct {
		ID        int64  `json:"id"`
		Platform  string `json:"platform"`
		Qimei36   string `json:"qimei36"`
		MachineID string `json:"machine_id"`
	}
	_ = json.Unmarshal(accRes.Body.Bytes(), &accBody)
	if accBody.Platform != domain.PlatformWorkBuddy || len(accBody.Qimei36) != 36 || accBody.MachineID == "" {
		t.Fatalf("unexpected created account dto: %s", accRes.Body.String())
	}

	// Keyword search matches name / nickname / uid / email / proxy_url
	schedFalse := false
	proxyAccRes := doJSON(http.MethodPost, "/api/accounts", jwtToken, map[string]any{
		"name":        "wb-proxy-only",
		"api_key":     "wb-proxy-tok-000",
		"proxy_url":   "http://10.20.30.40:18888",
		"domain":      mockUpstream.URL,
		"schedulable": &schedFalse,
		"group_ids":   []int64{grp.ID},
	})
	if proxyAccRes.Code != http.StatusCreated {
		t.Fatalf("create proxy-only account status=%d body=%s", proxyAccRes.Code, proxyAccRes.Body.String())
	}
	for _, tc := range []struct {
		kw   string
		want string
	}{
		{"uid_alice_99", "wb-acc-1"},
		{"alice.wb@example.com", "wb-acc-1"},
		{"AliceWB", "wb-acc-1"},
		{"10.20.30.40", "wb-proxy-only"},
	} {
		searchRes := doJSON(http.MethodGet, "/api/accounts?keyword="+tc.kw, jwtToken, nil)
		if searchRes.Code != http.StatusOK || !strings.Contains(searchRes.Body.String(), tc.want) {
			t.Fatalf("account keyword=%q search status=%d body=%s", tc.kw, searchRes.Code, searchRes.Body.String())
		}
	}
	var proxyAccBody struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(proxyAccRes.Body.Bytes(), &proxyAccBody)
	_ = doJSON(http.MethodDelete, "/api/accounts/"+jsonNumber(proxyAccBody.ID), jwtToken, nil)

	// Update with empty api_key keeps token valid
	updAccRes := doJSON(http.MethodPut, "/api/accounts/"+jsonNumber(accBody.ID), jwtToken, map[string]any{
		"api_key": "",
		"notes":   "updated without wiping key",
	})
	if updAccRes.Code != http.StatusOK {
		t.Fatalf("update account status=%d body=%s", updAccRes.Code, updAccRes.Body.String())
	}

	// Rotate device + Export accounts
	rotRes := doJSON(http.MethodPost, "/api/accounts/"+jsonNumber(accBody.ID)+"/rotate-device", jwtToken, nil)
	if rotRes.Code != http.StatusOK {
		t.Fatalf("rotate-device status=%d body=%s", rotRes.Code, rotRes.Body.String())
	}
	expRes := doJSON(http.MethodGet, "/api/accounts/export", jwtToken, nil)
	if expRes.Code != http.StatusOK || !strings.Contains(expRes.Body.String(), "wb-acc-1") {
		t.Fatalf("export status=%d body=%s", expRes.Code, expRes.Body.String())
	}

	// 7. Batch import (JSON format) + POST /v1/responses non-streaming & streaming + verify filter sanitization & SQLite dashboard
	batchRes := doJSON(http.MethodPost, "/api/accounts/batch", jwtToken, map[string]any{
		"keys":      `[{"name":"wb-batch-1","access_token":"wb-batch-tok-999","refresh_token":"wb-batch-ref-999","uid":"u_batch"}]`,
		"domain":    mockUpstream.URL,
		"group_ids": []int64{grp.ID},
		"upsert":    true,
	})
	if batchRes.Code != http.StatusOK || !strings.Contains(batchRes.Body.String(), `"created":1`) {
		t.Fatalf("batch import status=%d body=%s", batchRes.Code, batchRes.Body.String())
	}

	respNonStream := doJSON(http.MethodPost, "/v1/responses", keyBody.Key, map[string]any{
		"model":        "claude-sonnet-4-6",
		"instructions": "You are Claude Code by Anthropic.",
		"input":        "Hello Codex",
		"stream":       false,
	})
	if respNonStream.Code != http.StatusOK {
		t.Fatalf("/v1/responses non-stream status=%d body=%s", respNonStream.Code, respNonStream.Body.String())
	}
	if !strings.Contains(respNonStream.Body.String(), `"object":"response"`) || !strings.Contains(respNonStream.Body.String(), "Hello Codex!") {
		t.Fatalf("unexpected /v1/responses non-stream body: %s", respNonStream.Body.String())
	}

	respStream := doJSON(http.MethodPost, "/v1/responses", keyBody.Key, map[string]any{
		"model":        "claude-sonnet-4-6",
		"instructions": "You are Claude Code by Anthropic.",
		"input":        "Stream hello",
		"stream":       true,
	})
	if respStream.Code != http.StatusOK {
		t.Fatalf("/v1/responses stream status=%d body=%s", respStream.Code, respStream.Body.String())
	}
	sse := respStream.Body.String()
	for _, ev := range []string{"event: response.created", "event: response.output_text.delta", "event: response.completed"} {
		if !strings.Contains(sse, ev) {
			t.Fatalf("missing SSE event %q in /v1/responses stream: %s", ev, sse)
		}
	}

	if len(capturedUpstreamBodies) < 2 {
		t.Fatalf("expected 2 upstream calls captured, got %d", len(capturedUpstreamBodies))
	}
	for _, upBody := range capturedUpstreamBodies {
		if strings.Contains(upBody, "\u200b") {
			t.Fatalf("upstream body must not contain zero-width chars: %s", upBody)
		}
		if strings.Contains(upBody, "Claude Code") || strings.Contains(upBody, "Anthropic") {
			t.Fatalf("expected filter sanitization on /v1/responses payload, got: %s", upBody)
		}
		if !strings.Contains(upBody, "Ess-Agentic") || !strings.Contains(upBody, "Provider") {
			t.Fatalf("expected default filter rewrite in /v1/responses payload, got: %s", upBody)
		}
	}

	// Give async usage recorder goroutine (if any) a brief moment, then check /api/dashboard on SQLite
	time.Sleep(100 * time.Millisecond)
	dashRes := doJSON(http.MethodGet, "/api/dashboard", jwtToken, nil)
	if dashRes.Code != http.StatusOK {
		t.Fatalf("dashboard SQLite status=%d body=%s", dashRes.Code, dashRes.Body.String())
	}
	var dashBody struct {
		Summary struct {
			Requests int64 `json:"requests"`
		} `json:"summary"`
		Runtime struct {
			AccountsByPlatform map[string]int `json:"accounts_by_platform"`
		} `json:"runtime"`
	}
	_ = json.Unmarshal(dashRes.Body.Bytes(), &dashBody)
	if dashBody.Summary.Requests < 2 {
		t.Fatalf("expected >= 2 recorded requests in dashboard summary, got %+v (body=%s)", dashBody.Summary, dashRes.Body.String())
	}
	if dashBody.Runtime.AccountsByPlatform[domain.PlatformWorkBuddy] != 2 {
		t.Fatalf("expected 2 workbuddy accounts in runtime, got %+v", dashBody.Runtime.AccountsByPlatform)
	}

	// 9. Cron jobs API end-to-end: list seeded -> preview -> create -> run -> update -> delete -> reset
	cronListRes := doJSON(http.MethodGet, "/api/cron-jobs", jwtToken, nil)
	if cronListRes.Code != http.StatusOK {
		t.Fatalf("list cron jobs status=%d body=%s", cronListRes.Code, cronListRes.Body.String())
	}
	var cronListBody struct {
		Total int `json:"total"`
		Items []struct {
			ID       int64  `json:"id"`
			Action   string `json:"action"`
			CronExpr string `json:"cron_expr"`
		} `json:"items"`
	}
	_ = json.Unmarshal(cronListRes.Body.Bytes(), &cronListBody)
	if cronListBody.Total != 4 {
		t.Fatalf("expected 4 seeded cron jobs, got %d", cronListBody.Total)
	}

	cronPreviewRes := doJSON(http.MethodPost, "/api/cron-jobs/preview", jwtToken, map[string]any{
		"cron_expr": "0 8,20 * * *",
		"timezone":  "Asia/Shanghai",
		"count":     5,
	})
	if cronPreviewRes.Code != http.StatusOK {
		t.Fatalf("cron preview status=%d body=%s", cronPreviewRes.Code, cronPreviewRes.Body.String())
	}
	var cronPreviewBody struct {
		Valid    bool `json:"valid"`
		NextRuns []any `json:"next_runs"`
	}
	_ = json.Unmarshal(cronPreviewRes.Body.Bytes(), &cronPreviewBody)
	if !cronPreviewBody.Valid || len(cronPreviewBody.NextRuns) != 5 {
		t.Fatalf("expected valid cron preview with 5 runs, got %s", cronPreviewRes.Body.String())
	}

	cronCreateRes := doJSON(http.MethodPost, "/api/cron-jobs", jwtToken, map[string]any{
		"name":         "HTTP E2E Auto Check",
		"cron_expr":    "*/10 * * * *",
		"action":       "account_check",
		"target_scope": "all",
		"options":      map[string]any{"auto_recover": true, "concurrency": 2},
	})
	if cronCreateRes.Code != http.StatusCreated {
		t.Fatalf("create cron job status=%d body=%s", cronCreateRes.Code, cronCreateRes.Body.String())
	}
	var createdCron struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cronCreateRes.Body.Bytes(), &createdCron)

	cronRunRes := doJSON(http.MethodPost, "/api/cron-jobs/"+jsonNumber(createdCron.ID)+"/run", jwtToken, nil)
	if cronRunRes.Code != http.StatusOK {
		t.Fatalf("run cron job status=%d body=%s", cronRunRes.Code, cronRunRes.Body.String())
	}

	cronUpdateRes := doJSON(http.MethodPut, "/api/cron-jobs/"+jsonNumber(createdCron.ID), jwtToken, map[string]any{
		"enabled": false,
	})
	if cronUpdateRes.Code != http.StatusOK {
		t.Fatalf("update cron job status=%d body=%s", cronUpdateRes.Code, cronUpdateRes.Body.String())
	}

	cronDelRes := doJSON(http.MethodDelete, "/api/cron-jobs/"+jsonNumber(createdCron.ID), jwtToken, nil)
	if cronDelRes.Code != http.StatusOK {
		t.Fatalf("delete cron job status=%d body=%s", cronDelRes.Code, cronDelRes.Body.String())
	}

	cronResetRes := doJSON(http.MethodPost, "/api/cron-jobs/reset", jwtToken, nil)
	if cronResetRes.Code != http.StatusOK {
		t.Fatalf("reset cron jobs status=%d body=%s", cronResetRes.Code, cronResetRes.Body.String())
	}
}

func jsonNumber(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestOmniEndToEndWorkflow(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var mu sync.Mutex
	storedFiles := make(map[string]map[string]any)
	var capturedBodies []string

	const nodeMgmtKey = "agy-mgmt-e2e-secret"
	const nodeAPIKey = "agy-infer-e2e-secret"

	mockAgy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
			if r.Header.Get("Authorization") != "Bearer "+nodeAPIKey {
				http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gemini-2.5-pro"},{"id":"claude-sonnet-4-6"}]}`)

		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			if r.Header.Get("X-Management-Key") != nodeMgmtKey {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			mu.Lock()
			list := make([]map[string]any, 0, len(storedFiles))
			for _, f := range storedFiles {
				list = append(list, f)
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"files": list})

		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodPost:
			if r.Header.Get("X-Management-Key") != nodeMgmtKey {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
				return
			}
			fn := r.URL.Query().Get("filename")
			if fn == "" {
				fn = "agy-1.json"
			}
			raw["filename"] = fn
			mu.Lock()
			storedFiles[fn] = raw
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "file": raw})

		case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
			if r.Header.Get("Authorization") != "Bearer "+nodeAPIKey {
				http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
				return
			}
			rawReq, _ := io.ReadAll(r.Body)
			mu.Lock()
			capturedBodies = append(capturedBodies, string(rawReq))
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-omni\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-omni\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Omni OK\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-omni\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40,\"total_tokens\":140,\"prompt_tokens_details\":{\"cached_tokens\":60},\"completion_tokens_details\":{\"reasoning_tokens\":20}}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockAgy.Close()

	parsedURL, err := url.Parse(mockAgy.URL)
	if err != nil {
		t.Fatalf("parse mockAgy URL: %v", err)
	}
	mockPort, err := strconv.Atoi(parsedURL.Port())
	if err != nil {
		t.Fatalf("parse mockAgy port: %v", err)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "omni_e2e.db")
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:       "sqlite",
		SQLitePath:   dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{Mode: "release"},
		Database: config.DatabaseConfig{
			Driver:     "sqlite",
			SQLitePath: dbPath,
			DataDir:    tmpDir,
		},
		Auth: config.AuthConfig{
			JWTSecret:     "omni-test-jwt-secret-1234567890",
			TokenTTL:      24 * time.Hour,
			EncryptionKey: bytes.Repeat([]byte{0x53}, 32),
		},
		CommandCC: config.CommandCodeConfig{
			BaseURL:              mockAgy.URL,
			FirstTokenTimeout:    300 * time.Second,
			StreamIdleTimeout:    180 * time.Second,
			NonStreamIdleTimeout: 600 * time.Second,
			MaxBodyMB:            10,
			ModelCacheTTL:        time.Minute,
			AutoHarvestEnabled:   true,
			AutoHarvestInterval:  6 * time.Hour,
		},
		Bootstrap: config.BootstrapConfig{
			Email:    "omni-admin@wb2api.local",
			Password: "StrongPassword123!",
		},
		Health: config.HealthConfig{
			Enabled:          false,
			Interval:         time.Minute,
			FailureThreshold: 3,
			Timeout:          10 * time.Second,
		},
	}

	if err := service.BootstrapAdmin(ctx, client, cfg.Bootstrap, logger); err != nil {
		t.Fatalf("BootstrapAdmin: %v", err)
	}
	if _, err := service.BootstrapDefaultGroup(ctx, client, logger); err != nil {
		t.Fatalf("BootstrapDefaultGroup: %v", err)
	}

	vault, err := crypto.NewVault(cfg.Auth.EncryptionKey)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	issuer := auth.NewIssuer(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)

	wbClient := workbuddy.NewClient("", mockAgy.URL, logger)
	agClient := antigravity.NewClientWithPool(wbClient.ProxyPool(), logger)
	dockerMgr := antigravity.NewDockerManager(antigravity.CommandRunnerFunc(func(_ context.Context, _ string, args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "version" {
			return "27.3.1|linux", "", nil
		}
		return "", "", nil
	}))
	relayClient := relay.NewClient(cfg.CommandCC, logger)
	relayClient.SetWorkBuddyClient(wbClient)
	relayClient.SetAntigravityClient(agClient)

	filterEngine := filter.NewDefaultEngine()
	filterService := service.NewFilterService(client, filterEngine, logger)
	if err := filterService.EnsureSeeded(ctx); err != nil {
		t.Fatalf("filter EnsureSeeded: %v", err)
	}

	promptEngine := prompt.NewDefaultEngine()
	promptService := service.NewPromptService(client, promptEngine, filterEngine, logger)
	if err := promptService.EnsureSeeded(ctx); err != nil {
		t.Fatalf("prompt EnsureSeeded: %v", err)
	}

	pricingSvc := service.NewPricingService(client, logger)
	if err := pricingSvc.LoadFromDB(ctx); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	sched := scheduler.New(client, vault, cfg.Health.FailureThreshold, logger)
	recorder := service.NewRecorder(client, logger, service.WithPricingService(pricingSvc))
	accountService := service.NewAccountService(client, vault, relayClient, sched, logger)
	cronService := service.NewCronService(client, accountService, logger)
	if err := cronService.EnsureSeeded(ctx); err != nil {
		t.Fatalf("cron EnsureSeeded: %v", err)
	}
	adminKeyService := service.NewAdminKeyService(client)
	agyService := service.NewAntigravityService(client, vault, agClient, dockerMgr, tmpDir, "", logger)
	rewardsService := service.NewRewardsService(client, accountService, agyService, logger)
	runtimeSettingsService := service.NewRuntimeSettingsService(client, logger)

	relayService := relay.NewService(
		relayClient,
		sched,
		recorder,
		cfg,
		logger,
		relay.WithFilterEngine(filterEngine),
		relay.WithPromptEngine(promptEngine),
		relay.WithWorkBuddyClient(wbClient),
		relay.WithAntigravityClient(agClient),
		relay.WithTokenUpdater(accountService),
	)

	h := handler.New(client, vault, issuer, sched, relayService, accountService, cfg, logger, filterService)
	h.SetCronService(cronService)
	h.SetPricingService(pricingSvc)
	h.SetPromptService(promptService)
	h.SetAdminKeyService(adminKeyService)
	h.SetAntigravityService(agyService)
	h.SetRewardsService(rewardsService)
	h.SetRuntimeSettingsService(runtimeSettingsService)

	r := NewRouter(Options{
		Handler:              h,
		Issuer:               issuer,
		Client:               client,
		BootstrapAdminAPIKey: "adm-bootstrap-test-secret",
		Mode:                 "release",
	})

	doReq := func(method, path string, headers map[string]string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal %s %s: %v", method, path, err)
			}
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. Login -> JWT
	loginRes := doReq(http.MethodPost, "/api/auth/login", nil, map[string]string{
		"email":    "omni-admin@wb2api.local",
		"password": "StrongPassword123!",
	})
	if loginRes.Code != http.StatusOK {
		t.Fatalf("login failed: status=%d body=%s", loginRes.Code, loginRes.Body.String())
	}
	var loginOut struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(loginRes.Body.Bytes(), &loginOut)
	if loginOut.Token == "" {
		t.Fatal("empty jwt token")
	}
	jwtHeaders := map[string]string{"Authorization": "Bearer " + loginOut.Token}

	// 2. Issue full & readonly adm-... management keys
	fullKeyRes := doReq(http.MethodPost, "/api/admin-keys", jwtHeaders, map[string]any{
		"name":            "omni-full-key",
		"scopes":          "full",
		"allowed_ips":     "",
		"expires_in_days": 30,
	})
	if fullKeyRes.Code != http.StatusCreated {
		t.Fatalf("create full admin key status=%d body=%s", fullKeyRes.Code, fullKeyRes.Body.String())
	}
	var fullKeyOut struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(fullKeyRes.Body.Bytes(), &fullKeyOut)
	if !strings.HasPrefix(fullKeyOut.Key, domain.AdminKeyPrefix) {
		t.Fatalf("expected adm- prefix, got %q", fullKeyOut.Key)
	}

	roKeyRes := doReq(http.MethodPost, "/api/admin-keys", jwtHeaders, map[string]any{
		"name":            "omni-ro-key",
		"scopes":          "readonly",
		"allowed_ips":     "",
		"expires_in_days": 30,
	})
	if roKeyRes.Code != http.StatusCreated {
		t.Fatalf("create readonly admin key status=%d body=%s", roKeyRes.Code, roKeyRes.Body.String())
	}
	var roKeyOut struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(roKeyRes.Body.Bytes(), &roKeyOut)
	if !strings.HasPrefix(roKeyOut.Key, domain.AdminKeyPrefix) {
		t.Fatalf("expected adm- prefix for readonly key, got %q", roKeyOut.Key)
	}

	mgmtFullHeaders := map[string]string{"X-Management-Key": fullKeyOut.Key}
	mgmtROHeaders := map[string]string{"X-Management-Key": roKeyOut.Key}

	// 3. Authenticate with full X-Management-Key (no JWT)
	for _, ep := range []string{"/api/dashboard", "/api/settings", "/api/models/catalog", "/api/rewards/overview"} {
		res := doReq(http.MethodGet, ep, mgmtFullHeaders, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s with full management key status=%d body=%s", ep, res.Code, res.Body.String())
		}
	}

	// 4. Readonly management key allows GET /api/dashboard, rejects write with 403
	if res := doReq(http.MethodGet, "/api/dashboard", mgmtROHeaders, nil); res.Code != http.StatusOK {
		t.Fatalf("GET /api/dashboard with readonly key status=%d body=%s", res.Code, res.Body.String())
	}
	roWriteRes := doReq(http.MethodPut, "/api/prompts/global", mgmtROHeaders, map[string]any{
		"enabled": true,
		"prompt":  "blocked-by-readonly",
	})
	if roWriteRes.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for readonly key on PUT /api/prompts/global, got status=%d body=%s", roWriteRes.Code, roWriteRes.Body.String())
	}

	// 5. Prompt Injection Center + AntiGravity remote node + auth file upload + sync
	globalUpRes := doReq(http.MethodPut, "/api/prompts/global", mgmtFullHeaders, map[string]any{
		"enabled": true,
		"prompt":  "GLOBAL_OMNI_GUARD::Ess-Agentic[{{platform}}/{{group}}]",
	})
	if globalUpRes.Code != http.StatusOK {
		t.Fatalf("PUT /api/prompts/global status=%d body=%s", globalUpRes.Code, globalUpRes.Body.String())
	}

	tplRes := doReq(http.MethodPost, "/api/prompts/templates", mgmtFullHeaders, map[string]any{
		"name":        "omni-agy-tpl",
		"category":    "gateway",
		"content":     "GROUP_TPL_DIRECTIVE::{{model}}",
		"description": "Omni test template",
	})
	if tplRes.Code != http.StatusCreated {
		t.Fatalf("POST /api/prompts/templates status=%d body=%s", tplRes.Code, tplRes.Body.String())
	}
	var tplOut struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(tplRes.Body.Bytes(), &tplOut)
	if tplOut.ID <= 0 {
		t.Fatalf("invalid created template id in %s", tplRes.Body.String())
	}

	nodeRes := doReq(http.MethodPost, "/api/antigravity/nodes", mgmtFullHeaders, map[string]any{
		"name":           "remote-agy-e2e",
		"mode":           domain.ContainerModeRemoteAPI,
		"host_ip":        parsedURL.Hostname(),
		"host_port":      mockPort,
		"management_key": nodeMgmtKey,
		"api_key":        nodeAPIKey,
	})
	if nodeRes.Code != http.StatusCreated {
		t.Fatalf("POST /api/antigravity/nodes status=%d body=%s", nodeRes.Code, nodeRes.Body.String())
	}
	var nodeOut struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(nodeRes.Body.Bytes(), &nodeOut)
	if nodeOut.ID <= 0 {
		t.Fatalf("invalid antigravity node id in %s", nodeRes.Body.String())
	}

	uploadRes := doReq(http.MethodPost, "/api/antigravity/nodes/"+jsonNumber(nodeOut.ID)+"/auth-files", mgmtFullHeaders, map[string]any{
		"filename": "agy-1.json",
		"content": map[string]any{
			"type":          "antigravity",
			"email":         "agy-e2e@example.com",
			"project_id":    "proj-e2e",
			"refresh_token": "rt-e2e",
			"priority":      10,
		},
		"auto_sync": true,
	})
	if uploadRes.Code != http.StatusOK {
		t.Fatalf("upload auth file status=%d body=%s", uploadRes.Code, uploadRes.Body.String())
	}

	syncRes := doReq(http.MethodPost, "/api/antigravity/nodes/"+jsonNumber(nodeOut.ID)+"/sync-accounts", mgmtFullHeaders, map[string]any{})
	if syncRes.Code != http.StatusOK {
		t.Fatalf("sync-accounts status=%d body=%s", syncRes.Code, syncRes.Body.String())
	}
	var syncOut struct {
		OK      bool  `json:"ok"`
		GroupID int64 `json:"group_id"`
	}
	_ = json.Unmarshal(syncRes.Body.Bytes(), &syncOut)
	if !syncOut.OK || syncOut.GroupID <= 0 {
		t.Fatalf("expected ok=true and group_id>0 from sync-accounts, got %s", syncRes.Body.String())
	}

	bindRes := doReq(http.MethodPut, "/api/prompts/groups/"+jsonNumber(syncOut.GroupID), mgmtFullHeaders, map[string]any{
		"prompt_inject_mode": domain.PromptModeAppendGlobal,
		"system_prompt":      "",
		"prompt_template_id": tplOut.ID,
	})
	if bindRes.Code != http.StatusOK {
		t.Fatalf("PUT /api/prompts/groups/%d status=%d body=%s", syncOut.GroupID, bindRes.Code, bindRes.Body.String())
	}

	previewRes := doReq(http.MethodPost, "/api/prompts/preview", mgmtFullHeaders, map[string]any{
		"group_id":      syncOut.GroupID,
		"client_system": "You are Claude Code built by Anthropic.",
		"model":         "gemini-2.5-pro",
		"protocol":      "openai",
	})
	if previewRes.Code != http.StatusOK {
		t.Fatalf("POST /api/prompts/preview status=%d body=%s", previewRes.Code, previewRes.Body.String())
	}
	var previewOut struct {
		FinalSystem           string `json:"final_system"`
		SanitizedClientSystem string `json:"sanitized_client_system"`
	}
	_ = json.Unmarshal(previewRes.Body.Bytes(), &previewOut)
	if strings.Contains(previewOut.SanitizedClientSystem, "Claude Code") || strings.Contains(previewOut.FinalSystem, "Claude Code") {
		t.Fatalf("expected filter sanitization before prompt injection, got preview=%+v", previewOut)
	}
	if !strings.Contains(previewOut.FinalSystem, "GLOBAL_OMNI_GUARD::Ess-Agentic") ||
		!strings.Contains(previewOut.FinalSystem, "GROUP_TPL_DIRECTIVE::gemini-2.5-pro") ||
		!strings.Contains(previewOut.FinalSystem, "Ess-Agentic") {
		t.Fatalf("unexpected FinalSystem in preview: %q", previewOut.FinalSystem)
	}

	// 6. Issue downstream key bound to synced AntiGravity group and send OpenAI SSE + Responses requests
	apiKeyRes := doReq(http.MethodPost, "/api/keys", mgmtFullHeaders, map[string]any{
		"name":     "omni-agy-downstream",
		"group_id": syncOut.GroupID,
	})
	if apiKeyRes.Code != http.StatusCreated {
		t.Fatalf("create downstream key status=%d body=%s", apiKeyRes.Code, apiKeyRes.Body.String())
	}
	var apiKeyOut struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(apiKeyRes.Body.Bytes(), &apiKeyOut)
	relayHeaders := map[string]string{"Authorization": "Bearer " + apiKeyOut.Key}

	chatRes := doReq(http.MethodPost, "/v1/chat/completions", relayHeaders, map[string]any{
		"model":  "gemini-2.5-pro",
		"stream": true,
		"messages": []map[string]any{
			{"role": "system", "content": "I am Claude Code."},
			{"role": "user", "content": "Mention Claude Code cleanly."},
		},
	})
	if chatRes.Code != http.StatusOK || !strings.Contains(chatRes.Body.String(), "Omni OK") {
		t.Fatalf("/v1/chat/completions status=%d body=%s", chatRes.Code, chatRes.Body.String())
	}

	respRes := doReq(http.MethodPost, "/v1/responses", relayHeaders, map[string]any{
		"model":        "claude-sonnet-4-6",
		"instructions": "Run as Claude Code.",
		"input":        "ping from Claude Code",
		"stream":       false,
	})
	if respRes.Code != http.StatusOK || !strings.Contains(respRes.Body.String(), `"object":"response"`) {
		t.Fatalf("/v1/responses status=%d body=%s", respRes.Code, respRes.Body.String())
	}

	mu.Lock()
	bodiesCopy := append([]string(nil), capturedBodies...)
	mu.Unlock()
	if len(bodiesCopy) < 2 {
		t.Fatalf("expected >= 2 upstream captured requests, got %d", len(bodiesCopy))
	}
	for _, ub := range bodiesCopy {
		var parsed struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(ub), &parsed); err != nil {
			t.Fatalf("unmarshal captured upstream body: %v body=%s", err, ub)
		}
		if len(parsed.Messages) == 0 || parsed.Messages[0].Role != "system" {
			t.Fatalf("expected prepended injected system message first, got: %s", ub)
		}
		if !strings.Contains(parsed.Messages[0].Content, "GLOBAL_OMNI_GUARD::Ess-Agentic") ||
			!strings.Contains(parsed.Messages[0].Content, "GROUP_TPL_DIRECTIVE::") {
			t.Fatalf("upstream messages[0] missing injected global/group prompts: %s", ub)
		}
		for _, m := range parsed.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "Claude Code") {
				t.Fatalf("system message must not contain unsanitized Claude Code: %s", ub)
			}
		}
	}

	// 7. Verify /api/logs & /api/dashboard telemetry (cached dedup, cost, TTFT percentiles, cache hit rate)
	var logsBody []byte
	var logsOut struct {
		Items []struct {
			Platform        string  `json:"platform"`
			InputTokens     int     `json:"input_tokens"`
			CacheReadTokens int     `json:"cache_read_tokens"`
			ReasoningTokens int     `json:"reasoning_tokens"`
			Cost            float64 `json:"cost"`
			TotalCost       float64 `json:"total_cost"`
		} `json:"items"`
	}
	deadline := time.Now().Add(time.Second)
	for {
		logsRes := doReq(http.MethodGet, "/api/logs", mgmtFullHeaders, nil)
		if logsRes.Code != http.StatusOK {
			t.Fatalf("GET /api/logs status=%d body=%s", logsRes.Code, logsRes.Body.String())
		}
		logsBody = logsRes.Body.Bytes()
		_ = json.Unmarshal(logsBody, &logsOut)
		if len(logsOut.Items) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(logsOut.Items) < 2 {
		t.Fatalf("expected >= 2 usage logs, got %s", string(logsBody))
	}
	for _, item := range logsOut.Items {
		if item.Platform != domain.PlatformAntigravity {
			t.Fatalf("expected log platform=%q, got %q (%s)", domain.PlatformAntigravity, item.Platform, string(logsBody))
		}
		if item.InputTokens != 40 || item.CacheReadTokens != 60 || item.ReasoningTokens != 20 {
			t.Fatalf("unexpected cached/reasoning tokens in log %+v (%s)", item, string(logsBody))
		}
		effCost := item.Cost
		if effCost == 0 {
			effCost = item.TotalCost
		}
		if effCost <= 0 {
			t.Fatalf("expected positive cost in usage log, got %+v (%s)", item, string(logsBody))
		}
	}

	dashRes := doReq(http.MethodGet, "/api/dashboard", mgmtFullHeaders, nil)
	if dashRes.Code != http.StatusOK {
		t.Fatalf("GET /api/dashboard status=%d body=%s", dashRes.Code, dashRes.Body.String())
	}
	var dashOut struct {
		Summary struct {
			Requests        int     `json:"requests"`
			CacheHitRate    float64 `json:"cache_hit_rate"`
			AvgTPS          float64 `json:"avg_tps"`
			P50FirstTokenMs float64 `json:"p50_first_token_ms"`
			P95FirstTokenMs float64 `json:"p95_first_token_ms"`
		} `json:"summary"`
		ByPlatform []struct {
			Key      string `json:"key"`
			Requests int    `json:"requests"`
		} `json:"by_platform"`
	}
	_ = json.Unmarshal(dashRes.Body.Bytes(), &dashOut)
	if dashOut.Summary.Requests < 2 || dashOut.Summary.CacheHitRate <= 0 || dashOut.Summary.AvgTPS < 0 ||
		dashOut.Summary.P50FirstTokenMs <= 0 || dashOut.Summary.P95FirstTokenMs <= 0 ||
		len(dashOut.ByPlatform) == 0 || dashOut.ByPlatform[0].Key != domain.PlatformAntigravity {
		t.Fatalf("unexpected dashboard telemetry: %+v raw=%s", dashOut, dashRes.Body.String())
	}

	// 8. Model catalog, price override/delete, and runtime timeout updates
	catRes := doReq(http.MethodGet, "/api/models/catalog", mgmtFullHeaders, nil)
	if catRes.Code != http.StatusOK {
		t.Fatalf("GET /api/models/catalog status=%d body=%s", catRes.Code, catRes.Body.String())
	}
	var catOut struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(catRes.Body.Bytes(), &catOut)
	if catOut.Total <= 0 {
		t.Fatalf("expected positive catalog total, got %s", catRes.Body.String())
	}

	pricePutRes := doReq(http.MethodPut, "/api/models/prices", mgmtFullHeaders, map[string]any{
		"model":             "gemini-2.5-pro",
		"input_price":       9.9,
		"output_price":      19.9,
		"cache_read_price":  1.1,
		"cache_write_price": 2.2,
		"notes":             "e2e override",
	})
	if pricePutRes.Code != http.StatusOK {
		t.Fatalf("PUT /api/models/prices status=%d body=%s", pricePutRes.Code, pricePutRes.Body.String())
	}
	var priceOut struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(pricePutRes.Body.Bytes(), &priceOut)
	if priceOut.ID <= 0 {
		t.Fatalf("expected created price id, got %s", pricePutRes.Body.String())
	}

	priceDelRes := doReq(http.MethodDelete, "/api/models/prices/"+jsonNumber(priceOut.ID), mgmtFullHeaders, nil)
	if priceDelRes.Code != http.StatusOK {
		t.Fatalf("DELETE /api/models/prices/%d status=%d body=%s", priceOut.ID, priceDelRes.Code, priceDelRes.Body.String())
	}

	rtRes := doReq(http.MethodPut, "/api/settings/runtime", mgmtFullHeaders, map[string]any{
		"first_token_seconds":    360,
		"stream_idle_seconds":    210,
		"nonstream_idle_seconds": 720,
	})
	if rtRes.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings/runtime status=%d body=%s", rtRes.Code, rtRes.Body.String())
	}
	afterSettingsRes := doReq(http.MethodGet, "/api/settings", mgmtFullHeaders, nil)
	if afterSettingsRes.Code != http.StatusOK {
		t.Fatalf("GET /api/settings after runtime update status=%d", afterSettingsRes.Code)
	}
	var afterSettings struct {
		Timeouts struct {
			FirstTokenSeconds    int `json:"first_token_seconds"`
			StreamIdleSeconds    int `json:"stream_idle_seconds"`
			NonStreamIdleSeconds int `json:"nonstream_idle_seconds"`
		} `json:"timeouts"`
	}
	_ = json.Unmarshal(afterSettingsRes.Body.Bytes(), &afterSettings)
	if afterSettings.Timeouts.FirstTokenSeconds != 360 ||
		afterSettings.Timeouts.StreamIdleSeconds != 210 ||
		afterSettings.Timeouts.NonStreamIdleSeconds != 720 {
		t.Fatalf("unexpected updated timeout settings: %+v raw=%s", afterSettings, afterSettingsRes.Body.String())
	}
}

func TestToolObfuscationEndToEndWorkflow(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var mu sync.Mutex
	var capturedBodies []string
	var lastReplyToolName string

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"claude-sonnet-4-6"}]}`)
		case "/v1/chat/completions":
			raw, _ := io.ReadAll(r.Body)
			var parsed struct {
				Tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}
			_ = json.Unmarshal(raw, &parsed)
			wireTool := "unknown_tool"
			if len(parsed.Tools) > 0 && parsed.Tools[0].Function.Name != "" {
				wireTool = parsed.Tools[0].Function.Name
			}
			mu.Lock()
			capturedBodies = append(capturedBodies, string(raw))
			lastReplyToolName = wireTool
			mu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-e2e-obf\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_e2e_1\",\"type\":\"function\",\"function\":{\"name\":\""+wireTool+"\",\"arguments\":\"{\\\"cmd\\\":\\\"id\\\"}\"}}]}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-e2e-obf\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":8}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockUpstream.Close()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "toolobf_e2e.db")
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:       "sqlite",
		SQLitePath:   dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{Mode: "release"},
		Database: config.DatabaseConfig{
			Driver:     "sqlite",
			SQLitePath: dbPath,
			DataDir:    tmpDir,
		},
		Auth: config.AuthConfig{
			JWTSecret:     "toolobf-test-jwt-secret-1234567890",
			TokenTTL:      24 * time.Hour,
			EncryptionKey: bytes.Repeat([]byte{0x61}, 32),
		},
		CommandCC: config.CommandCodeConfig{
			BaseURL:              mockUpstream.URL,
			FirstTokenTimeout:    30 * time.Second,
			StreamIdleTimeout:    30 * time.Second,
			NonStreamIdleTimeout: 30 * time.Second,
		},
		Bootstrap: config.BootstrapConfig{
			Email:    "obf-admin@wb2api.local",
			Password: "StrongPassword123!",
		},
	}

	if err := service.BootstrapAdmin(ctx, client, cfg.Bootstrap, logger); err != nil {
		t.Fatal(err)
	}
	grp, err := client.Group.Create().SetName("agy-obf-e2e").SetPlatform(domain.PlatformAntigravity).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Account.Create().
		SetName("agy-obf-acc").
		SetPlatform(domain.PlatformAntigravity).
		SetCredentials(map[string]any{"api_key": "k-e2e", "base_url": mockUpstream.URL}).
		AddGroupIDs(grp.ID).
		Save(ctx); err != nil {
		t.Fatal(err)
	}

	vault, _ := crypto.NewVault(cfg.Auth.EncryptionKey)
	issuer := auth.NewIssuer(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)
	wbClient := workbuddy.NewClient("", mockUpstream.URL, logger)
	agClient := antigravity.NewClientWithPool(wbClient.ProxyPool(), logger)
	relayClient := relay.NewClient(cfg.CommandCC, logger)
	relayClient.SetWorkBuddyClient(wbClient)
	relayClient.SetAntigravityClient(agClient)

	filterEngine := filter.NewDefaultEngine()
	filterService := service.NewFilterService(client, filterEngine, logger)
	_ = filterService.EnsureSeeded(ctx)

	promptEngine := prompt.NewDefaultEngine()
	promptService := service.NewPromptService(client, promptEngine, filterEngine, logger)
	_ = promptService.EnsureSeeded(ctx)

	toolObfEngine := toolobf.NewDefaultEngine()
	toolObfService := service.NewToolObfuscationService(client, toolObfEngine, logger)
	if err := toolObfService.EnsureSeeded(ctx); err != nil {
		t.Fatalf("toolobf EnsureSeeded: %v", err)
	}

	sched := scheduler.New(client, vault, 3, logger)
	recorder := service.NewRecorder(client, logger)
	accountService := service.NewAccountService(client, vault, relayClient, sched, logger)
	relayService := relay.NewService(
		relayClient,
		sched,
		recorder,
		cfg,
		logger,
		relay.WithFilterEngine(filterEngine),
		relay.WithPromptEngine(promptEngine),
		relay.WithToolObfuscator(toolObfEngine),
		relay.WithWorkBuddyClient(wbClient),
		relay.WithAntigravityClient(agClient),
		relay.WithTokenUpdater(accountService),
	)

	h := handler.New(client, vault, issuer, sched, relayService, accountService, cfg, logger, filterService)
	h.SetPromptService(promptService)
	h.SetAdminKeyService(service.NewAdminKeyService(client))
	h.SetToolObfuscationService(toolObfService)

	r := NewRouter(Options{
		Handler:              h,
		Issuer:               issuer,
		Client:               client,
		BootstrapAdminAPIKey: "adm-toolobf-bootstrap",
		Mode:                 "release",
	})

	doReq := func(method, path string, headers map[string]string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	mgmtHdr := map[string]string{"X-Management-Key": "adm-toolobf-bootstrap"}

	// 1. GET /api/tool-obfuscation + GET /api/settings summary
	getRes := doReq(http.MethodGet, "/api/tool-obfuscation", mgmtHdr, nil)
	if getRes.Code != http.StatusOK || !strings.Contains(getRes.Body.String(), `"enabled":true`) {
		t.Fatalf("GET /api/tool-obfuscation failed: status=%d body=%s", getRes.Code, getRes.Body.String())
	}
	settingsRes := doReq(http.MethodGet, "/api/settings", mgmtHdr, nil)
	if settingsRes.Code != http.StatusOK || !strings.Contains(settingsRes.Body.String(), `"tool_obfuscation"`) {
		t.Fatalf("GET /api/settings missing tool_obfuscation block: status=%d body=%s", settingsRes.Code, settingsRes.Body.String())
	}

	// 2. PUT /api/tool-obfuscation -> update to alias mode with custom_map + exclude_tools
	putRes := doReq(http.MethodPut, "/api/tool-obfuscation", mgmtHdr, map[string]any{
		"enabled":           true,
		"mode":              "alias",
		"prefix":            "ess_t_",
		"scope":             "all",
		"platforms":         []string{"antigravity", "workbuddy", "commandcode", "opencode"},
		"custom_map":        map[string]string{"Bash": "shell_runner_custom"},
		"exclude_tools":     []string{"KeepRawTool"},
		"rewrite_tool_refs": true,
	})
	if putRes.Code != http.StatusOK || !strings.Contains(putRes.Body.String(), `"shell_runner_custom"`) {
		t.Fatalf("PUT /api/tool-obfuscation failed: status=%d body=%s", putRes.Code, putRes.Body.String())
	}

	// 3. POST /api/tool-obfuscation/preview (full JSON + tool list)
	pvRes := doReq(http.MethodPost, "/api/tool-obfuscation/preview", mgmtHdr, map[string]any{
		"tool_names": []string{"Bash", "Read", "KeepRawTool"},
	})
	if pvRes.Code != http.StatusOK {
		t.Fatalf("POST /api/tool-obfuscation/preview status=%d body=%s", pvRes.Code, pvRes.Body.String())
	}
	var pvOut struct {
		RestoredVerified bool `json:"restored_verified"`
		Mappings         []struct {
			Original   string `json:"original"`
			Obfuscated string `json:"obfuscated"`
			Restored   string `json:"restored"`
		} `json:"mappings"`
	}
	_ = json.Unmarshal(pvRes.Body.Bytes(), &pvOut)
	if !pvOut.RestoredVerified || len(pvOut.Mappings) != 3 {
		t.Fatalf("unexpected preview output: %s", pvRes.Body.String())
	}

	// 4. POST /api/tool-obfuscation/rotate-salt
	rotRes := doReq(http.MethodPost, "/api/tool-obfuscation/rotate-salt", mgmtHdr, nil)
	if rotRes.Code != http.StatusOK {
		t.Fatalf("POST /api/tool-obfuscation/rotate-salt status=%d body=%s", rotRes.Code, rotRes.Body.String())
	}

	// 5. Issue downstream key and test /v1/chat/completions, /v1/messages, /v1/responses live
	keyRes := doReq(http.MethodPost, "/api/keys", mgmtHdr, map[string]any{
		"name":     "toolobf-downstream",
		"group_id": grp.ID,
	})
	if keyRes.Code != http.StatusCreated {
		t.Fatalf("create downstream key status=%d body=%s", keyRes.Code, keyRes.Body.String())
	}
	var keyOut struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(keyRes.Body.Bytes(), &keyOut)
	relayHdr := map[string]string{"Authorization": "Bearer " + keyOut.Key}

	r1 := doReq(http.MethodPost, "/v1/chat/completions", relayHdr, map[string]any{
		"model":       "claude-sonnet-4-6",
		"stream":      true,
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "Bash", "parameters": map[string]any{"type": "object"}}}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}},
		"messages":    []any{map[string]any{"role": "user", "content": "run"}},
	})
	if r1.Code != http.StatusOK || !strings.Contains(r1.Body.String(), `"Bash"`) || strings.Contains(r1.Body.String(), "shell_runner_custom") {
		t.Fatalf("/v1/chat/completions tool restore failed: code=%d body=%s", r1.Code, r1.Body.String())
	}

	r2 := doReq(http.MethodPost, "/v1/messages", relayHdr, map[string]any{
		"model":       "claude-sonnet-4-6",
		"max_tokens":  512,
		"stream":      false,
		"tools":       []any{map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}}},
		"tool_choice": map[string]any{"type": "tool", "name": "Bash"},
		"messages":    []any{map[string]any{"role": "user", "content": "run"}},
	})
	if r2.Code != http.StatusOK || !strings.Contains(r2.Body.String(), `"Bash"`) || strings.Contains(r2.Body.String(), "shell_runner_custom") {
		t.Fatalf("/v1/messages tool restore failed: code=%d body=%s", r2.Code, r2.Body.String())
	}

	r3 := doReq(http.MethodPost, "/v1/responses", relayHdr, map[string]any{
		"model":       "claude-sonnet-4-6",
		"stream":      true,
		"tools":       []any{map[string]any{"type": "function", "name": "Bash", "parameters": map[string]any{"type": "object"}}},
		"tool_choice": map[string]any{"type": "function", "name": "Bash"},
		"input":       "run",
	})
	if r3.Code != http.StatusOK || !strings.Contains(r3.Body.String(), `"Bash"`) || strings.Contains(r3.Body.String(), "shell_runner_custom") {
		t.Fatalf("/v1/responses tool restore failed: code=%d body=%s", r3.Code, r3.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(capturedBodies) != 3 || lastReplyToolName != "shell_runner_custom" {
		t.Fatalf("expected 3 obfuscated upstream calls with shell_runner_custom, got count=%d wire=%q", len(capturedBodies), lastReplyToolName)
	}
	for i, b := range capturedBodies {
		if strings.Contains(b, `"Bash"`) || !strings.Contains(b, `"shell_runner_custom"`) {
			t.Fatalf("upstream call %d failed to obfuscate Bash -> shell_runner_custom: %s", i+1, b)
		}
	}
}
