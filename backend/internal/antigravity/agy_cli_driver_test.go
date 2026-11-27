package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseAgyModelsOutput(t *testing.T) {
	raw := "Fetching available models...\n" +
		"gemini-4-neon-medium\tGemini 4 Neon (Medium)\n" +
		"gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n" +
		"claude-sonnet-5-5-medium\tClaude Sonnet 5.5 (Medium)\n" +
		"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n"

	items := ParseAgyModelsOutput(raw)
	if len(items) != 4 {
		t.Fatalf("期望解析出 4 个真实 agy 模型，实际 %d: %+v", len(items), items)
	}
	if items[0].ID != "gemini-4-neon-medium" || items[0].Vendor != "google" {
		t.Fatalf("首条模型解析异常: %+v", items[0])
	}
	if items[2].ID != "claude-sonnet-5-5-medium" || items[2].Vendor != "anthropic" {
		t.Fatalf("Claude 模型解析异常: %+v", items[2])
	}
}

func TestStreamAgyNDJSONToOpenAISSE_TextToolAndOutboundIntercept(t *testing.T) {
	// 1. 普通文本 + thinking_delta + 跨分片泄漏内部桥接标记（0 工具 -> 实时剥离泄漏标记并透传正文）
	ndjsonText := strings.Join([]string{
		`{"event":"init","conversation_id":"conv-11111111","init":{"model":"gemini-3.8-flash-low"}}`,
		`{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","thinking_delta":"<!-- cmdwb2api-isolated-agy-proxy-rules -->thinking..."}}`,
		`{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","text_delta":"<agy_inter"}}`,
		`{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","text_delta":"cept_bridge>secret</agy_intercept_bridge>OK"}}`,
		`{"event":"result","result":{"conversation_id":"conv-11111111","status":"SUCCESS","response":"OK","usage":{"input_tokens":100,"output_tokens":5,"thinking_tokens":3,"cache_read_tokens":20,"total_tokens":108}}}`,
	}, "\n")

	var out bytes.Buffer
	convID := streamAgyNDJSONToOpenAISSE(context.Background(), strings.NewReader(ndjsonText), &out, "gemini-3.8-flash-low", "chatcmpl-1", 1700000000, AdaptedToolsSpec{Tier: ToolTierNone})
	if convID != "conv-11111111" {
		t.Fatalf("期望捕获 conversation_id=conv-11111111，实际 %q", convID)
	}
	s := out.String()
	if strings.Contains(s, "agy_intercept_bridge") || strings.Contains(s, "cmdwb2api-isolated") || strings.Contains(s, "secret") {
		t.Fatalf("流式 SSE 泄漏了内部桥接标记:\n%s", s)
	}
	for _, want := range []string{
		`"reasoning_content":"thinking..."`,
		`"content":"OK"`,
		`"finish_reason":"stop"`,
		`"cached_tokens":20`,
		`data: [DONE]`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("SSE 输出缺少 %q:\n%s", want, s)
		}
	}

	// 2. 携带外部工具时：同时支持 <AGY_TOOL_CALL> 与 <tool_call>、超大 >100KB 工具参数，且屏蔽未声明宿主工具幻觉
	toolsSpec := AdaptToolsForAgyCLI([]any{
		map[string]any{"type": "function", "function": map[string]any{"name": "exec_task", "parameters": map[string]any{"type": "object"}}},
		map[string]any{"type": "custom", "name": "apply_patch", "description": "Apply patch"},
	}, "auto")

	bigPatch := strings.Repeat("+line_of_rust_code();\n", 4000) // ~84KB > 默认 64KB bufio.Scanner 上限
	patchJSON, _ := json.Marshal(map[string]any{"name": "apply_patch", "input": bigPatch})
	stepText := fmt.Sprintf("<downstream_continuation>\nChecking files first.\n<AGY_TOOL_CALL>{\"name\":\"exec_task\",\"arguments\":{\"cmd\":\"ls\"}}</AGY_TOOL_CALL>\n<AGY_TOOL_CALL>{\"name\":\"run_command\",\"arguments\":{\"cmd\":\"whoami\"}}</AGY_TOOL_CALL>\n<tool_call>%s</tool_call>\n</downstream_continuation>", string(patchJSON))
	stepLineBytes, _ := json.Marshal(map[string]any{
		"event": "step_update",
		"step_update": map[string]any{
			"state":      "ACTIVE",
			"step_type":  "agent_response",
			"text_delta": stepText,
		},
	})
	ndjsonTool := string(stepLineBytes) + "\n" + `{"event":"result","result":{"conversation_id":"conv-22222222","status":"SUCCESS","usage":{"input_tokens":50,"output_tokens":12,"total_tokens":62}}}`

	var outTool bytes.Buffer
	streamAgyNDJSONToOpenAISSE(context.Background(), strings.NewReader(ndjsonTool), &outTool, "gemini-3.8-flash-low", "chatcmpl-2", 1700000000, toolsSpec)
	st := outTool.String()
	if strings.Contains(st, "downstream_continuation") {
		t.Fatalf("工具流 SSE 泄漏了 <downstream_continuation>:\n%s", st)
	}
	if strings.Contains(st, `"name":"run_command"`) {
		t.Fatalf("未声明的宿主内置工具 run_command 不应作为外部 tool_calls 透传:\n%s", st[:500])
	}
	for _, want := range []string{
		`"content":"Checking files first.`,
		`"tool_calls"`,
		`"name":"exec_task"`,
		`"name":"apply_patch"`,
		`"finish_reason":"tool_calls"`,
		`data: [DONE]`,
	} {
		if !strings.Contains(st, want) {
			t.Fatalf("工具调用 SSE 输出缺少 %q", want)
		}
	}

	// 3. 验证非流式聚合器（stream=false）：保留 reasoning_content + tool_calls 并剥离内部标记
	nonStreamBytes, statusCode := aggregateOpenAISSEToChatCompletionJSON(strings.NewReader(st))
	if statusCode != http.StatusOK {
		t.Fatalf("非流式聚合状态码应为 200，实际 %d: %s", statusCode, string(nonStreamBytes))
	}
	var nonStreamObj map[string]any
	if err := json.Unmarshal(nonStreamBytes, &nonStreamObj); err != nil {
		t.Fatalf("非流式 JSON 解析失败: %v", err)
	}
	if nonStreamObj["object"] != "chat.completion" {
		t.Fatalf("期望 object=chat.completion，实际 %v", nonStreamObj["object"])
	}
	choices, _ := nonStreamObj["choices"].([]any)
	ch0, _ := choices[0].(map[string]any)
	msg0, _ := ch0["message"].(map[string]any)
	tcs0, _ := msg0["tool_calls"].([]any)
	if len(tcs0) != 2 || ch0["finish_reason"] != "tool_calls" {
		t.Fatalf("非流式聚合 tool_calls 数量或 finish_reason 错误: %+v", ch0)
	}
}

func TestCancelOnCloseReadCloser(t *testing.T) {
	var canceled atomic.Bool
	rc := &cancelOnCloseReadCloser{
		ReadCloser: io.NopCloser(strings.NewReader("ok")),
		cancel:     func() { canceled.Store(true) },
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}
	if !canceled.Load() {
		t.Fatal("关闭响应 Body 时必须立即调用底层子进程 cancel 函数")
	}
}

func TestBuildIsolatedCLIEnvAndBridgeEndpoints(t *testing.T) {
	defer SetRuntimePromptMode("")
	dir := filepath.Join(t.TempDir(), "agy-iso-home")
	env := buildIsolatedCLIEnv(NodeSpec{
		AuthDir:  dir,
		ProxyURL: "http://10.8.0.1:7890",
		Fingerprint: NodeFingerprint{
			FingerprintID: "fp_single_cli_01",
		},
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HOME=" + dir,
		"USERPROFILE=" + dir,
		"HTTP_PROXY=http://10.8.0.1:7890",
		"HTTPS_PROXY=http://10.8.0.1:7890",
		"AGY_FINGERPRINT_ID=fp_single_cli_01",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("隔离环境变量缺少 %q", want)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := NewAgyBridgeHandlerWithContext(ctx, BridgeConfig{
		APIKey:        "sk-bridge-secret",
		ManagementKey: "mgmt-secret",
		AuthDir:       dir,
	})
	rrHealth := httptest.NewRecorder()
	h.ServeHTTP(rrHealth, httptest.NewRequest(http.MethodGet, "/health", nil))
	healthBody := rrHealth.Body.String()
	for _, want := range []string{
		`"driver":"real-agy-cli"`,
		`"session_retention":"stateless"`,
		`"prompt_mode":"default_intercept_continue"`,
		`"prompt_passthrough":"verbatim"`,
		`"adaptive_tools":true`,
	} {
		if rrHealth.Code != http.StatusOK || !strings.Contains(healthBody, want) {
			t.Fatalf("/health 响应异常，缺少 %q: code=%d body=%s", want, rrHealth.Code, healthBody)
		}
	}

	// 测试 /v0/management/prompt-modes GET 与 PUT 热切换
	reqModes := httptest.NewRequest(http.MethodGet, "/v0/management/prompt-modes?has_tools=true", nil)
	reqModes.Header.Set("X-Management-Key", "mgmt-secret")
	rrModes := httptest.NewRecorder()
	h.ServeHTTP(rrModes, reqModes)
	if rrModes.Code != http.StatusOK || !strings.Contains(rrModes.Body.String(), `"active_mode":"default_intercept_continue"`) {
		t.Fatalf("GET /v0/management/prompt-modes 响应异常: code=%d body=%s", rrModes.Code, rrModes.Body.String())
	}

	putModes := httptest.NewRequest(http.MethodPut, "/v0/management/prompt-modes", strings.NewReader(`{"mode":"blend_sanitized","downstream_system":"You are DSH.","has_tools":true}`))
	putModes.Header.Set("X-Management-Key", "mgmt-secret")
	rrPutModes := httptest.NewRecorder()
	h.ServeHTTP(rrPutModes, putModes)
	if rrPutModes.Code != http.StatusOK || !strings.Contains(rrPutModes.Body.String(), `"active_mode":"blend_sanitized"`) {
		t.Fatalf("PUT /v0/management/prompt-modes 切换失败: code=%d body=%s", rrPutModes.Code, rrPutModes.Body.String())
	}
	SetRuntimePromptMode("")

	rrUnauth := httptest.NewRecorder()
	h.ServeHTTP(rrUnauth, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rrUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("未携带 API Key 访问 /v1/models 应返回 401，实际 %d", rrUnauth.Code)
	}

	reqSess := httptest.NewRequest(http.MethodGet, "/v0/management/sessions", nil)
	reqSess.Header.Set("X-Management-Key", "mgmt-secret")
	rrSess := httptest.NewRecorder()
	h.ServeHTTP(rrSess, reqSess)
	if rrSess.Code != http.StatusOK || !strings.Contains(rrSess.Body.String(), `"count":0`) || !strings.Contains(rrSess.Body.String(), `"retention_mode":"stateless"`) {
		t.Fatalf("/v0/management/sessions 响应异常: code=%d body=%s", rrSess.Code, rrSess.Body.String())
	}
}

func TestCLISessionStoreAndDeltaPrompt(t *testing.T) {
	store := NewCLISessionStore(0, 10)

	store.Bind("up-sess-A", "agy-si-11111111", "gemini-3.8-flash-low", 1)
	store.Bind("up-sess-B", "agy-si-22222222", "gemini-3.8-flash-low", 1)

	entA, okA := store.Lookup("up-sess-A")
	entB, okB := store.Lookup("up-sess-B")
	if !okA || !okB || entA.AgyConversationID == entB.AgyConversationID {
		t.Fatalf("不同上游 session_id 必须映射到不同 agy si: A=%+v B=%+v", entA, entB)
	}

	body := []byte(`{
		"model":"gemini-3.8-flash-low",
		"messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":"turn 1 question"},
			{"role":"assistant","content":"turn 1 answer"},
			{"role":"user","content":"turn 2 follow-up"}
		]
	}`)
	built, err := buildAgyCLIPrompt(body, 2)
	if err != nil {
		t.Fatalf("buildAgyCLIPrompt 失败: %v", err)
	}
	if built.TotalNonSysMsgs != 3 {
		t.Fatalf("非 system 消息总数应为 3，实际 %d", built.TotalNonSysMsgs)
	}
	if strings.Contains(built.Prompt, "turn 1 question") || !strings.Contains(built.Prompt, "turn 2 follow-up") {
		t.Fatalf("续接增量 prompt 不正确: %q", built.Prompt)
	}

	if !store.Delete("up-sess-A") {
		t.Fatal("Delete(up-sess-A) 应返回 true")
	}
	if _, ok := store.Lookup("up-sess-A"); ok {
		t.Fatal("删除后 Lookup(up-sess-A) 应返回 false")
	}
}

func createMockAgyConversationFiles(t *testing.T, homeDir, convID string) {
	t.Helper()
	cliRoot := filepath.Join(homeDir, ".gemini", "antigravity-cli")
	for _, sub := range []string{"conversations", filepath.Join("brain", convID), "annotations", "implicit"} {
		if err := os.MkdirAll(filepath.Join(cliRoot, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", sub, err)
		}
	}
	files := map[string][]byte{
		filepath.Join(cliRoot, "conversations", convID+".db"):     bytes.Repeat([]byte("SQLite"), 200),
		filepath.Join(cliRoot, "conversations", convID+".db-wal"): []byte("wal"),
		filepath.Join(cliRoot, "conversations", convID+".db-shm"): []byte("shm"),
		filepath.Join(cliRoot, "brain", convID, "artifact.md"):    []byte("# temp"),
		filepath.Join(cliRoot, "annotations", convID+".json"):     []byte(`{}`),
		filepath.Join(cliRoot, "implicit", convID+".state"):       []byte(`ok`),
	}
	for p, data := range files {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("write %s failed: %v", p, err)
		}
	}
}

func TestCleanupConversationDiskArtifactsAndEvictHooks(t *testing.T) {
	home := t.TempDir()
	targetID := "11111111-2222-3333-4444-555555555555"
	keepID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	createMockAgyConversationFiles(t, home, targetID)
	createMockAgyConversationFiles(t, home, keepID)

	for _, bad := range []string{"", "short", "../escape-dir", "a/b/cdefgh", `a\b\cdefgh`, "bad*wildcard"} {
		if removed, _ := CleanupConversationDiskArtifacts(home, bad); removed != 0 {
			t.Fatalf("非法 convID %q 不应删除任何文件，实际删除 %d", bad, removed)
		}
	}

	removed, freed := CleanupConversationDiskArtifacts(home, targetID)
	if removed == 0 || freed == 0 {
		t.Fatalf("期望清理 targetID 产物，实际 removed=%d freed=%d", removed, freed)
	}
	cliRoot := filepath.Join(home, ".gemini", "antigravity-cli")
	for _, gone := range []string{
		filepath.Join(cliRoot, "conversations", targetID+".db"),
		filepath.Join(cliRoot, "conversations", targetID+".db-wal"),
		filepath.Join(cliRoot, "brain", targetID),
		filepath.Join(cliRoot, "annotations", targetID+".json"),
		filepath.Join(cliRoot, "implicit", targetID+".state"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("targetID 磁盘产物 %s 应已删除", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(cliRoot, "conversations", keepID+".db")); err != nil {
		t.Fatalf("未指定的 keepID 磁盘产物不应被误删: %v", err)
	}

	id1 := "22222222-1111-1111-1111-111111111111"
	id2 := "33333333-1111-1111-1111-111111111111"
	id3 := "44444444-1111-1111-1111-111111111111"
	createMockAgyConversationFiles(t, home, id1)
	createMockAgyConversationFiles(t, home, id2)
	createMockAgyConversationFiles(t, home, id3)

	store := NewCLISessionStore(time.Hour, 2)
	store.SetOnEvict(func(ent CLISessionEntry) {
		CleanupConversationDiskArtifacts(home, ent.AgyConversationID)
	})
	store.Bind("sess-1", id1, "gemini-3.8-flash-low", 1)
	time.Sleep(5 * time.Millisecond)
	store.Bind("sess-2", id2, "gemini-3.8-flash-low", 1)
	time.Sleep(5 * time.Millisecond)
	store.Bind("sess-3", id3, "gemini-3.8-flash-low", 1)

	if _, err := os.Stat(filepath.Join(cliRoot, "conversations", id1+".db")); !os.IsNotExist(err) {
		t.Fatalf("LRU 淘汰后 id1 磁盘文件应已自动清理")
	}
	if _, err := os.Stat(filepath.Join(cliRoot, "conversations", id2+".db")); err != nil {
		t.Fatalf("活跃会话 id2 磁盘文件应保留")
	}

	swept, _ := SweepOrphanedConversationDiskArtifacts(home, store.ActiveConversationIDs(), 0)
	if swept != 1 {
		t.Fatalf("期望清理 1 个孤儿会话 keepID，实际 swept=%d", swept)
	}
	if _, err := os.Stat(filepath.Join(cliRoot, "conversations", keepID+".db")); !os.IsNotExist(err) {
		t.Fatalf("孤儿会话 keepID 应已被扫描清理")
	}

	store.Clear()
	for _, id := range []string{id2, id3} {
		if _, err := os.Stat(filepath.Join(cliRoot, "conversations", id+".db")); !os.IsNotExist(err) {
			t.Fatalf("Clear 后会话 %s 磁盘文件应全部清理", id)
		}
	}
}

func TestDefaultInterceptContinueAndRequestMetadataOverride(t *testing.T) {
	defer SetRuntimePromptMode("")

	// 下游可能是 dsh / codex / Claude Code / OpenCode，领域指令与人设在 default_intercept_continue 下必须完整接续
	sysPrompt := "You are Claude Code, Anthropic's official CLI for Claude.\nYou are an interactive CLI tool for dsh/codex engineers. Always prefix replies with [DSH_CODEX_OK]."
	body, _ := json.Marshal(map[string]any{
		"model": "gemini-3.8-flash-low",
		"messages": []map[string]any{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": "hello"},
		},
	})

	prepared, err := PrepareOpenAIPayloadForCLI(body)
	if err != nil {
		t.Fatalf("PrepareOpenAIPayloadForCLI 失败: %v", err)
	}

	built, err := buildAgyCLIPrompt(prepared, 0)
	if err != nil {
		t.Fatalf("buildAgyCLIPrompt 失败: %v", err)
	}
	if built.PromptBlendMode != PromptModeDefaultInterceptContinue {
		t.Fatalf("默认模式应为 %q，实际 %q", PromptModeDefaultInterceptContinue, built.PromptBlendMode)
	}
	if !strings.Contains(built.Prompt, sysPrompt) {
		t.Fatalf("buildAgyCLIPrompt 未完整接续下游 system prompt:\n%s", built.Prompt)
	}
	bridgeIdx := strings.Index(built.Prompt, `<agy_intercept_bridge mode="default_intercept_continue">`)
	contIdx := strings.Index(built.Prompt, `<downstream_continuation>`)
	downstreamIdx := strings.Index(built.Prompt, sysPrompt)
	if bridgeIdx < 0 || contIdx <= bridgeIdx || downstreamIdx <= contIdx {
		t.Fatalf("顺序错误：拦截桥必须在最前，<downstream_continuation> 紧随其后包裹下游提示词: bridge=%d cont=%d downstream=%d",
			bridgeIdx, contIdx, downstreamIdx)
	}

	// 测试请求 metadata.agy_prompt_mode 覆盖为 downstream_only
	overrideBody, _ := json.Marshal(map[string]any{
		"model":    "gemini-3.8-flash-low",
		"metadata": map[string]any{"agy_prompt_mode": "downstream_only"},
		"messages": []map[string]any{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": "hello"},
		},
	})
	builtOverride, err := buildAgyCLIPrompt(overrideBody, 0)
	if err != nil {
		t.Fatalf("buildAgyCLIPrompt metadata 覆盖失败: %v", err)
	}
	if builtOverride.PromptBlendMode != PromptModeDownstreamOnly || strings.Contains(builtOverride.Prompt, "<agy_intercept_bridge") {
		t.Fatalf("请求级 metadata.agy_prompt_mode=downstream_only 未生效: mode=%q prompt=%s", builtOverride.PromptBlendMode, builtOverride.Prompt)
	}
}

func TestAdaptToolsForAgyCLI_MultiClientTiers(t *testing.T) {
	spec0 := AdaptToolsForAgyCLI(nil, nil)
	if spec0.Tier != ToolTierNone || spec0.HasTools() {
		t.Fatalf("0 工具应返回 tier=none，实际 %+v", spec0)
	}

	codexTools := []any{
		map[string]any{"type": "function", "name": "exec_command", "description": "Run shell command", "parameters": map[string]any{
			"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}, "required": []any{"cmd"},
		}},
		map[string]any{"type": "custom", "name": "apply_patch", "description": "Apply a freeform diff patch"},
		map[string]any{"type": "local_shell"},
		map[string]any{"type": "web_search_preview"},
	}
	specFull := AdaptToolsForAgyCLI(codexTools, "auto")
	if specFull.Tier != ToolTierFull || len(specFull.Tools) != 4 {
		t.Fatalf("Codex 4 工具集应为 tier=full 且全量保留 4 个工具，实际 tier=%s count=%d", specFull.Tier, len(specFull.Tools))
	}
	for _, wantName := range []string{"exec_command", "apply_patch", "local_shell", "web_search_preview"} {
		if _, ok := specFull.ResolveToolName(wantName); !ok {
			t.Fatalf("下游工具 %q 未被透传保留", wantName)
		}
	}

	specNone := AdaptToolsForAgyCLI(codexTools, "none")
	if specNone.Tier != ToolTierNone || specNone.HasTools() {
		t.Fatalf("tool_choice=none 应禁用工具解析，实际 %+v", specNone)
	}

	medTools := make([]any, 0, 20)
	for i := 0; i < 20; i++ {
		medTools = append(medTools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        fmt.Sprintf("dsh_tool_%02d", i),
				"description": strings.Repeat("Detailed operational description for tool. ", 10),
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "Target file path"},
					},
					"required": []any{"path"},
				},
			},
		})
	}
	specCompact := AdaptToolsForAgyCLI(medTools, "auto")
	if specCompact.Tier != ToolTierCompact || len(specCompact.Tools) != 20 {
		t.Fatalf("20 工具集应自适应降级为 tier=compact，实际 tier=%s count=%d", specCompact.Tier, len(specCompact.Tools))
	}

	bigTools := make([]any, 0, 60)
	for i := 0; i < 60; i++ {
		bigTools = append(bigTools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        fmt.Sprintf("mcp_server_action_%02d", i),
				"description": "Perform an external MCP operation across cluster resources.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":   map[string]any{"type": "string"},
						"mode": map[string]any{"type": "string", "enum": []any{"fast", "safe"}},
					},
					"required": []any{"id"},
				},
			},
		})
	}
	specSig := AdaptToolsForAgyCLI(bigTools, "auto")
	if specSig.Tier != ToolTierSignature || len(specSig.Tools) != 60 {
		t.Fatalf("60 工具集应自适应降级为 tier=signature，实际 tier=%s count=%d", specSig.Tier, len(specSig.Tools))
	}
	if !strings.Contains(specSig.PromptBlock, `mcp_server_action_00(id*: string, mode?: "fast"|"safe")`) {
		t.Fatalf("signature 层级渲染格式不符:\n%s", specSig.PromptBlock)
	}

	specForced := AdaptToolsForAgyCLI(bigTools, map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "mcp_server_action_42"},
	})
	if specForced.Tier != ToolTierFull || specForced.ForcedToolName != "mcp_server_action_42" {
		t.Fatalf("强制单工具应聚焦为 full 层级，实际 tier=%s forced=%q", specForced.Tier, specForced.ForcedToolName)
	}

	// 同时校验 <AGY_TOOL_CALL> 与 <tool_call> 提取，且过滤未声明宿主内置工具
	prose, calls := ExtractInterceptionAwareToolCalls(
		`Let me check. <AGY_TOOL_CALL>{"name":"run_command","arguments":{"cmd":"whoami"}}</AGY_TOOL_CALL> <AGY_TOOL_CALL>{"name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}</AGY_TOOL_CALL>`,
		specFull,
	)
	if len(calls) != 1 || calls[0].Name != "exec_command" || calls[0].Arguments != `{"cmd":"pwd"}` {
		t.Fatalf("期望仅提取声明工具 exec_command 并解包字符串 JSON 参数，实际 calls=%+v prose=%q", calls, prose)
	}
}

func TestBuildAgyStreamInputLine(t *testing.T) {
	line, err := buildAgyStreamInputLine("hello\nworld")
	if err != nil {
		t.Fatalf("buildAgyStreamInputLine 失败: %v", err)
	}
	if !strings.HasSuffix(string(line), "\n") {
		t.Fatalf("stream-json 输入必须以换行符结束")
	}
	var parsed map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(line), &parsed); err != nil {
		t.Fatalf("stream-json 无法反序列化: %v", err)
	}
	if parsed["event"] != "user" {
		t.Fatalf("期望 event=user，实际 %v", parsed["event"])
	}
	msg, _ := parsed["message"].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "hello\nworld" {
		t.Fatalf("message 结构异常: %+v", msg)
	}
}

func TestExtractRandomToolCallsHardenedAndHTTP2Env(t *testing.T) {
	spec := AdaptToolsForAgyCLI([]any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "meow_random_9981",
				"description": "Arbitrary user-invented tool",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "integer"}}},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":       "小喵自定义工具_42",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}}},
			},
		},
	}, "auto")

	_, calls1 := ExtractInterceptionAwareToolCalls("<AGY_TOOL_CALL>\n```json\n{\"name\":\"meow_random_9981\",\"arguments\":{\"x\":7}}\n```\n</AGY_TOOL_CALL>", spec)
	if len(calls1) != 1 || calls1[0].Name != "meow_random_9981" || calls1[0].Arguments != `{"x":7}` {
		t.Fatalf("<AGY_TOOL_CALL> 内嵌 json 围栏提取失败: %+v", calls1)
	}

	_, calls2 := ExtractInterceptionAwareToolCalls(`<AGY_TOOL_CALL>[{"name":"meow_random_9981","arguments":{"x":1}},{"name":"小喵自定义工具_42","arguments":{"msg":"hi"}}]</AGY_TOOL_CALL>`, spec)
	if len(calls2) != 2 || calls2[0].Name != "meow_random_9981" || calls2[1].Name != "小喵自定义工具_42" {
		t.Fatalf("<AGY_TOOL_CALL> JSON 数组并行工具提取失败: %+v", calls2)
	}

	_, calls3 := ExtractInterceptionAwareToolCalls("```json\n{\"name\":\"小喵自定义工具_42\",\"arguments\":{\"msg\":\"ok\"}}\n```", spec)
	if len(calls3) != 1 || calls3[0].Name != "小喵自定义工具_42" {
		t.Fatalf("精确声明 json 围栏提取失败: %+v", calls3)
	}
	_, calls4 := ExtractInterceptionAwareToolCalls(`{"name":"meow_random_9981","arguments":{"x":99}}`, spec)
	if len(calls4) != 1 || calls4[0].Name != "meow_random_9981" {
		t.Fatalf("裸声明工具 JSON 提取失败: %+v", calls4)
	}
	prose5, calls5 := ExtractInterceptionAwareToolCalls("```json\n{\"name\":\"unknown_data\",\"val\":1}\n```", spec)
	if len(calls5) != 0 || prose5 == "" {
		t.Fatalf("未声明普通 JSON 不应被误判为工具调用: calls=%+v prose=%q", calls5, prose5)
	}

	env := buildIsolatedCLIEnv(NodeSpec{AuthDir: t.TempDir()})
	foundH2 := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "GODEBUG=") && strings.Contains(kv, "http2client=1") {
			foundH2 = true
			break
		}
	}
	if !foundH2 {
		t.Fatalf("buildIsolatedCLIEnv 未注入 GODEBUG=http2client=1")
	}
}

func TestLiveRealAgyCLI_DefaultInterceptContinue(t *testing.T) {
	if os.Getenv("AGY_LIVE_VERIFY") != "1" {
		t.Skip("跳过真实 agy.exe 在线联调（设置 AGY_LIVE_VERIFY=1 开启验证）")
	}

	realHome, err := os.UserHomeDir()
	if err != nil || realHome == "" {
		t.Skip("无法定位当前用户主目录")
	}
	agyBin := `C:\Users\Administrator\AppData\Roaming\Antigravity\bin\agy.exe`
	if envBin := strings.TrimSpace(os.Getenv(AgyCLIBinEnv)); envBin != "" {
		agyBin = envBin
	}
	if _, err := os.Stat(agyBin); err != nil {
		b, lErr := ResolveAgyCLIBinary("")
		if lErr != nil {
			t.Skipf("本机未找到 agy 可执行文件: %v", lErr)
		}
		agyBin = b
	}

	// 将本机已有 OAuth 凭证只读复制到临时隔离沙盒目录，严禁修改真实 ~/.gemini 或 %APPDATA%\Antigravity
	tmpRoot, err := os.MkdirTemp("", "agy-live-verify-*")
	if err != nil {
		t.Fatalf("创建临时隔离根目录失败: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 6; i++ {
			if os.RemoveAll(tmpRoot) == nil {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	})
	isoAuthDir := filepath.Join(tmpRoot, "isolated-agy-home")
	if err := os.MkdirAll(filepath.Join(isoAuthDir, ".gemini", "antigravity-cli"), 0o755); err != nil {
		t.Fatalf("创建临时隔离目录失败: %v", err)
	}
	copiedCreds := 0
	for _, f := range []string{"oauth_creds.json", "google_accounts.json", "installation_id", "settings.json", "state.json"} {
		src := filepath.Join(realHome, ".gemini", f)
		if data, rErr := os.ReadFile(src); rErr == nil {
			_ = os.WriteFile(filepath.Join(isoAuthDir, ".gemini", f), data, 0o600)
			if f == "oauth_creds.json" {
				copiedCreds++
			}
		}
	}
	for _, f := range []string{"jetski_state.pbtxt", "installation_id", "settings.json"} {
		src := filepath.Join(realHome, ".gemini", "antigravity-cli", f)
		if data, rErr := os.ReadFile(src); rErr == nil {
			_ = os.WriteFile(filepath.Join(isoAuthDir, ".gemini", "antigravity-cli", f), data, 0o600)
			if f == "jetski_state.pbtxt" {
				copiedCreds++
			}
		}
	}
	if copiedCreds == 0 {
		t.Skip("本机未检测到已登录的 agy 凭证，跳过在线验证")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	bridgeSrv := httptest.NewServer(NewAgyBridgeHandlerWithContext(ctx, BridgeConfig{
		CLIBinary:        agyBin,
		AuthDir:          isoAuthDir,
		APIKey:           "sk-live-test",
		ManagementKey:    "mgmt-live-test",
		SessionRetention: SessionRetentionStateless,
		PromptBlendMode:  PromptModeDefaultInterceptContinue,
	}))
	defer bridgeSrv.Close()

	// 用例 A（非流式 JSON）：验证原生默认 -> 拦截 -> 下游人设接续，不泄漏 "Antigravity" / "Google Deepmind" 或桥接标签
	personaReqBody, _ := json.Marshal(map[string]any{
		"model":  "gemini-3.8-flash-low",
		"stream": false,
		"messages": []map[string]any{
			{
				"role": "system",
				"content": "x-anthropic-billing-header: cc_version=2.1; cch=test\n" +
					"<artifacts>Write to <appDataDir>\\brain\\999 with ArtifactMetadata</artifacts>\n" +
					"You are NovaBot, a concise distributed systems assistant. " +
					"Always begin your reply with the exact tag [NOVA_OK] and identify yourself strictly as NovaBot. " +
					"Never mention Antigravity or Google Deepmind.",
			},
			{
				"role":    "user",
				"content": "Who are you? Reply in one short sentence.",
			},
		},
	})

	reqA, _ := http.NewRequestWithContext(ctx, http.MethodPost, bridgeSrv.URL+"/v1/chat/completions", bytes.NewReader(personaReqBody))
	reqA.Header.Set("Authorization", "Bearer sk-live-test")
	reqA.Header.Set("Content-Type", "application/json")
	respA, err := http.DefaultClient.Do(reqA)
	if err != nil {
		t.Fatalf("真实 agy.exe 人设接续请求失败: %v", err)
	}
	rawA, _ := io.ReadAll(respA.Body)
	_ = respA.Body.Close()

	if respA.StatusCode != http.StatusOK {
		t.Fatalf("真实 agy.exe 人设接续返回非 200 (%d): %s", respA.StatusCode, string(rawA))
	}
	if gotMode := respA.Header.Get("X-Agy-Prompt-Mode"); gotMode != PromptModeDefaultInterceptContinue {
		t.Fatalf("期望 X-Agy-Prompt-Mode=%s，实际 %q", PromptModeDefaultInterceptContinue, gotMode)
	}

	var parsedA struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rawA, &parsedA); err != nil || len(parsedA.Choices) == 0 {
		t.Fatalf("解析非流式响应失败: err=%v body=%s", err, string(rawA))
	}
	replyText := parsedA.Choices[0].Message.Content
	t.Logf("[Live Case A - Persona Continuation Reply]: %s", replyText)

	if !strings.Contains(replyText, "NovaBot") || !strings.Contains(replyText, "[NOVA_OK]") {
		t.Fatalf("下游人设与规则未生效，实际回复: %q", replyText)
	}
	for _, forbidden := range []string{"Antigravity", "Google Deepmind", "<agy_intercept_bridge", "<downstream_continuation"} {
		if strings.Contains(strings.ToLower(replyText), strings.ToLower(forbidden)) {
			t.Fatalf("真实 agy.exe 回复泄漏了应被拦截的内容 %q: %q", forbidden, replyText)
		}
	}

	// 用例 B（流式 SSE）：验证 default_intercept_continue 下自定义外部下游工具端到端调用与解析
	toolReqBody, _ := json.Marshal(map[string]any{
		"model":       "gemini-3.8-flash-low",
		"stream":      true,
		"tool_choice": "required",
		"messages": []map[string]any{
			{
				"role":    "system",
				"content": "You are a cluster diagnostic agent. Use the provided external tool to inspect shards.",
			},
			{
				"role":    "user",
				"content": "Inspect shard 'shard-alpha-09' in region 'ap-east-1' right now using quantum_shard_probe_77.",
			},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "quantum_shard_probe_77",
					"description": "Probe health metrics of a distributed quantum KV shard in the caller's environment",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"shard_id": map[string]any{"type": "string", "description": "Target shard identifier"},
							"region":   map[string]any{"type": "string", "description": "Deployment region"},
						},
						"required": []any{"shard_id", "region"},
					},
				},
			},
		},
	})

	reqB, _ := http.NewRequestWithContext(ctx, http.MethodPost, bridgeSrv.URL+"/v1/chat/completions", bytes.NewReader(toolReqBody))
	reqB.Header.Set("Authorization", "Bearer sk-live-test")
	reqB.Header.Set("Content-Type", "application/json")
	respB, err := http.DefaultClient.Do(reqB)
	if err != nil {
		t.Fatalf("真实 agy.exe 自定义工具调用请求失败: %v", err)
	}
	sseBytes, _ := io.ReadAll(respB.Body)
	_ = respB.Body.Close()

	sseStr := string(sseBytes)
	t.Logf("[Live Case B - Custom Tool SSE Output]:\n%s", sseStr)

	if respB.StatusCode != http.StatusOK {
		t.Fatalf("真实 agy.exe 工具请求返回非 200 (%d): %s", respB.StatusCode, sseStr)
	}
	for _, want := range []string{
		`"tool_calls"`,
		`"name":"quantum_shard_probe_77"`,
		`shard-alpha-09`,
		`ap-east-1`,
		`"finish_reason":"tool_calls"`,
		`data: [DONE]`,
	} {
		if !strings.Contains(sseStr, want) {
			t.Fatalf("真实 agy.exe 流式工具响应缺少预期片段 %q:\n%s", want, sseStr)
		}
	}
	for _, leaked := range []string{"<agy_intercept_bridge", "<downstream_continuation", `"name":"run_command"`} {
		if strings.Contains(sseStr, leaked) {
			t.Fatalf("真实 agy.exe 流式工具响应包含泄漏内容 %q:\n%s", leaked, sseStr)
		}
	}
}
