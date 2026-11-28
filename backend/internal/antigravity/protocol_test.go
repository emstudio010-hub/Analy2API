package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitizeSystemTextAndSchemaCleaner(t *testing.T) {
	// 1. 系统提示词净化：剥离 billing-header 与竞品 CLI 身份句
	rawSys := "x-anthropic-billing-header: cc_version=2.1; cch=abc\nYou are Claude Code, Anthropic's official CLI for Claude. Always help with Go."
	cleanedSys := SanitizeSystemText(rawSys)
	if strings.Contains(cleanedSys, "x-anthropic-billing-header") {
		t.Fatalf("未剥离 x-anthropic-billing-header: %q", cleanedSys)
	}
	if strings.Contains(cleanedSys, "Claude Code") {
		t.Fatalf("未中性化 Claude Code 开头身份声明: %q", cleanedSys)
	}
	if !strings.Contains(cleanedSys, "Always help with Go.") {
		t.Fatalf("误删真实用户指令: %q", cleanedSys)
	}

	// 2. JSON Schema 清洗：$ref/$defs 展开、allOf 合并、const->enum、移除不支持关键字
	rawSchema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs": map[string]any{
			"Target": map[string]any{
				"type":                 []any{"string", "null"},
				"additionalProperties": false,
				"minLength":            1,
				"description":          "file path",
			},
		},
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{"$ref": "#/$defs/Target"},
			"mode": map[string]any{"const": "fast"},
		},
		"required": []any{"path", "non_existent_key"},
	}
	cleaned := CleanJSONSchema(rawSchema)
	for _, banned := range []string{"$schema", "$defs", "additionalProperties"} {
		if _, exists := cleaned[banned]; exists {
			t.Fatalf("CleanJSONSchema 未移除不支持字段 %q: %+v", banned, cleaned)
		}
	}
	props, _ := cleaned["properties"].(map[string]any)
	pathProp, _ := props["path"].(map[string]any)
	if pathProp["type"] != "string" {
		t.Fatalf("$ref 与 nullable type 未正确展开为 string: %+v", pathProp)
	}
	if _, hasMinLen := pathProp["minLength"]; hasMinLen {
		t.Fatalf("子属性 minLength 未清理: %+v", pathProp)
	}
	modeProp, _ := props["mode"].(map[string]any)
	if enumVals, ok := modeProp["enum"].([]any); !ok || len(enumVals) != 1 || enumVals[0] != "fast" {
		t.Fatalf("const 未转换为单元素 enum: %+v", modeProp)
	}
	reqArr, _ := cleaned["required"].([]any)
	if len(reqArr) != 1 || reqArr[0] != "path" {
		t.Fatalf("required 应过滤不存在的字段: %+v", reqArr)
	}
}

func TestNativeV1InternalDirectCLIRoundTrip(t *testing.T) {
	var (
		gotPath    string
		gotQuery   string
		gotHeaders http.Header
		gotBody    map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		// 返回标准 Google v1internal SSE：thought + functionCall + usageMetadata
		chunk1 := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"let me check","thought":true},{"functionCall":{"name":"exec_task","id":"fc_1","args":{"cmd":"ls -la"}}}]}}]}}`
		chunk2 := `data: {"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"cachedContentTokenCount":20,"candidatesTokenCount":15,"thoughtsTokenCount":5}}}`
		_, _ = w.Write([]byte(chunk1 + "\n\n" + chunk2 + "\n\n"))
	}))
	defer srv.Close()

	fp := GenerateNodeFingerprint("direct-cli-1")
	spec := NodeSpec{
		Name:        "agy-direct-1",
		Mode:        ModeDirectCLI,
		HostIP:      srv.URL,
		APIKey:      "ya29.test_oauth_access_token",
		ProjectID:   "proj-agy-001",
		Fingerprint: fp,
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []map[string]any{
			{"role": "system", "content": "x-anthropic-billing-header: foo=bar\nYou are Claude Code, Anthropic's official CLI for Claude. Help user."},
			{"role": "user", "content": "list files"},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "exec_task",
					"description": "run command",
					"parameters": map[string]any{
						"$schema":              "https://json-schema.org/draft/2020-12/schema",
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"cmd": map[string]any{"type": "string"},
						},
						"required": []any{"cmd"},
					},
				},
			},
		},
	})

	cli := NewClient("", nil)
	resp, wireBytes, err := cli.ChatStreamWithSession(context.Background(), spec, reqBody, "sess-direct-1")
	if err != nil {
		t.Fatalf("ChatStreamWithSession 失败: %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/v1internal:streamGenerateContent" || gotQuery != "alt=sse" {
		t.Fatalf("原生 v1internal 端点错误: path=%q query=%q", gotPath, gotQuery)
	}
	if !strings.HasPrefix(gotHeaders.Get("User-Agent"), "antigravity/") {
		t.Fatalf("User-Agent 非官方 AntiGravity CLI 格式: %q", gotHeaders.Get("User-Agent"))
	}
	if gotHeaders.Get("Authorization") != "Bearer ya29.test_oauth_access_token" {
		t.Fatalf("Authorization 头错误: %q", gotHeaders.Get("Authorization"))
	}
	for _, forbidden := range []string{"X-Qimei36", "X-Machine-Id", "X-Device-Fingerprint", "X-Client-Id"} {
		if gotHeaders.Get(forbidden) != "" {
			t.Fatalf("v1internal 请求泄漏非官方头 %s", forbidden)
		}
	}

	// 验证 v1internal 信封结构
	if gotBody["project"] != "proj-agy-001" || gotBody["userAgent"] != "antigravity" || gotBody["requestType"] != "agent" {
		t.Fatalf("v1internal 信封字段不正确: %+v", gotBody)
	}
	reqID, _ := gotBody["requestId"].(string)
	if !strings.HasPrefix(reqID, "agent-") {
		t.Fatalf("requestId 应以 agent- 开头，实际为 %q", reqID)
	}
	wireStr := string(wireBytes)
	if strings.Contains(wireStr, "x-anthropic-billing-header") || strings.Contains(wireStr, "additionalProperties") {
		t.Fatalf("出站 v1internal 请求体仍包含未清洗字段: %s", wireStr)
	}
	if !strings.Contains(wireStr, `"VALIDATED"`) || !strings.Contains(wireStr, `"exec_task"`) {
		t.Fatalf("v1internal 缺少 toolConfig VALIDATED 或 functionDeclarations: %s", wireStr)
	}

	// 验证 sessionId 已规范化为 Google v1internal 粘性 KV 缓存所需的负 64 位整数十进制字符串
	innerReq, _ := gotBody["request"].(map[string]any)
	gotSessID, _ := innerReq["sessionId"].(string)
	if !negativeInt64SessionRe.MatchString(gotSessID) {
		t.Fatalf("v1internal request.sessionId 应规范化为负 64 位整数字符串，实际为 %q", gotSessID)
	}
	if gotSessID != normalizeV1InternalSessionID("sess-direct-1", "") {
		t.Fatalf("v1internal request.sessionId 哈希不稳定: %q", gotSessID)
	}

	// 验证 v1internal SSE 已无损转换为标准 OpenAI SSE（包含 reasoning_content、tool_calls、cached_tokens 与 reasoning_tokens）
	sseOut, _ := io.ReadAll(resp.Body)
	sseText := string(sseOut)
	if !strings.Contains(sseText, `"reasoning_content":"let me check"`) {
		t.Fatalf("转换后的 OpenAI SSE 缺少 reasoning_content: %s", sseText)
	}
	if !strings.Contains(sseText, `"name":"exec_task"`) || !strings.Contains(sseText, `"finish_reason":"tool_calls"`) {
		t.Fatalf("转换后的 OpenAI SSE 缺少 tool_calls 或 tool_calls finish_reason: %s", sseText)
	}
	if !strings.Contains(sseText, `"cached_tokens":20`) || !strings.Contains(sseText, `"reasoning_tokens":5`) || !strings.Contains(sseText, "data: [DONE]") {
		t.Fatalf("转换后的 OpenAI SSE 缺少 cached_tokens / reasoning_tokens 或 [DONE]: %s", sseText)
	}
}

func TestGoogleDesktopOAuthRedirectAndBridgeHeadlessFallback(t *testing.T) {
	// 1. 验证公网域名自动规范化为 Google Desktop 允许的 localhost 回调，而本机地址保持原样
	if got := NormalizeGoogleDesktopRedirectURI("https://cmd.essapi.uno/api/oauth/callback"); got != OfficialOAuthDefaultRedirectURI {
		t.Fatalf("remote redirect_uri must fallback to %q, got %q", OfficialOAuthDefaultRedirectURI, got)
	}
	if got := NormalizeGoogleDesktopRedirectURI("http://localhost:8080/api/oauth/callback"); got != "http://localhost:8080/api/oauth/callback" {
		t.Fatalf("loopback redirect_uri should be preserved, got %q", got)
	}

	// 2. 验证内置公开客户端令牌参数解码有效，且支持环境变量覆盖
	defaultSec := EffectiveOAuthClientSecret()
	if len(defaultSec) != 35 {
		t.Fatalf("expected 35-char decoded desktop client parameter, got len=%d", len(defaultSec))
	}
	t.Setenv(AntigravityOAuthClientSecretEnv, "custom-override-secret")
	if got := EffectiveOAuthClientSecret(); got != "custom-override-secret" {
		t.Fatalf("expected env override secret, got %q", got)
	}

	// 3. 验证单账号容器桥接 (agy-bridge) 在无 Linux Keyring / 未装 agy CLI 二进制时，
	//    能自动用 /app/auths 凭证刷新 OAuth 令牌并无缝降级到内置 HTTP/2 v1internal 流式推理
	var refreshed, codeAssisted, streamed bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			refreshed = true
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_secret") == "" {
				http.Error(w, "missing refresh form fields", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"ya29.refreshed_container_tok","expires_in":3600,"token_type":"Bearer"}`))
		case "/v1internal:loadCodeAssist":
			codeAssisted = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-headless-01"}`))
		case "/v1internal:streamGenerateContent":
			streamed = true
			if r.Header.Get("Authorization") != "Bearer ya29.refreshed_container_tok" {
				http.Error(w, "unexpected bearer", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"headless container ok\"}]}}]}}\n\ndata: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":4}}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	t.Setenv("AGY_OAUTH_TOKEN_URL", upstream.URL+"/token")
	t.Setenv("AGY_UPSTREAM_BASE_URL", upstream.URL)

	authDir := t.TempDir()
	expiredMs := time.Now().Add(-10 * time.Minute).UnixMilli()
	credJSON, _ := json.Marshal(map[string]any{
		"type":          "antigravity",
		"email":         "headless@example.com",
		"refresh_token": "1//0gHeadlessRefreshToken123",
		"expiry_date":   expiredMs,
	})
	if err := os.WriteFile(filepath.Join(authDir, "auth.json"), credJSON, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	bridgeHandler := NewAgyBridgeHandler(BridgeConfig{
		CLIBinary:     filepath.Join(authDir, "nonexistent-agy-binary"),
		AuthDir:       authDir,
		ProxyURL:      "direct",
		APIKey:        "sk-bridge-key",
		ManagementKey: "mgmt-bridge-key",
	})
	bridgeSrv := httptest.NewServer(bridgeHandler)
	defer bridgeSrv.Close()

	reqPayload := []byte(`{"model":"gemini-2.5-pro","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	req, _ := http.NewRequest(http.MethodPost, bridgeSrv.URL+"/v1/chat/completions", bytes.NewReader(reqPayload))
	req.Header.Set("Authorization", "Bearer sk-bridge-key")
	req.Header.Set("Content-Type", "application/json")
	directHTTPCli := &http.Client{Transport: &http.Transport{}}
	resp, err := directHTTPCli.Do(req)
	if err != nil {
		t.Fatalf("bridge request failed: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from headless bridge fallback, got %d: %s", resp.StatusCode, string(bodyBytes))
	}
	if !refreshed || !codeAssisted || !streamed {
		t.Fatalf("expected refresh+loadCodeAssist+stream all called, got refreshed=%v codeAssisted=%v streamed=%v", refreshed, codeAssisted, streamed)
	}
	if !strings.Contains(string(bodyBytes), "headless container ok") || !strings.Contains(string(bodyBytes), "[DONE]") {
		t.Fatalf("unexpected bridge SSE output: %s", string(bodyBytes))
	}
}
