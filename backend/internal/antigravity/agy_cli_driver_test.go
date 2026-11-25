package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestStreamAgyNDJSONToOpenAISSE_TextAndToolCall(t *testing.T) {
	// 1. 普通文本 + thinking_delta + usage（0 工具 -> 实时透传）
	ndjsonText := strings.Join([]string{
		`{"event":"init","conversation_id":"conv-11111111","init":{"model":"gemini-3.8-flash-low"}}`,
		`{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","thinking_delta":"thinking..."}}`,
		`{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","text_delta":"OK"}}`,
		`{"event":"result","result":{"conversation_id":"conv-11111111","status":"SUCCESS","response":"OK","usage":{"input_tokens":100,"output_tokens":5,"thinking_tokens":3,"cache_read_tokens":20,"total_tokens":108}}}`,
	}, "\n")

	var out bytes.Buffer
	convID := streamAgyNDJSONToOpenAISSE(context.Background(), strings.NewReader(ndjsonText), &out, "gemini-3.8-flash-low", "chatcmpl-1", 1700000000, AdaptedToolsSpec{Tier: ToolTierNone})
	if convID != "conv-11111111" {
		t.Fatalf("期望捕获 conversation_id=conv-11111111，实际 %q", convID)
	}
	s := out.String()
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

	// 2. 携带外部工具时：支持混合正文 + 并行多工具调用，且屏蔽未声明的 agy 内置工具幻觉
	toolsSpec := AdaptToolsForAgyCLI([]any{
		map[string]any{"type": "function", "function": map[string]any{"name": "exec_task", "parameters": map[string]any{"type": "object"}}},
		map[string]any{"type": "custom", "name": "apply_patch", "description": "Apply patch"},
	}, "auto")

	ndjsonTool := strings.Join([]string{
		`{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","text_delta":"Checking files first.\n<tool_call>{\"name\":\"exec_task\",\"arguments\":{\"cmd\":\"ls\"}}</tool_call>\n<tool_call>{\"name\":\"apply_patch\",\"input\":\"*** Begin Patch\\n*** End Patch\"}</tool_call>"}}`,
		`{"event":"result","result":{"conversation_id":"conv-22222222","status":"SUCCESS","usage":{"input_tokens":50,"output_tokens":12,"total_tokens":62}}}`,
	}, "\n")

	var outTool bytes.Buffer
	streamAgyNDJSONToOpenAISSE(context.Background(), strings.NewReader(ndjsonTool), &outTool, "gemini-3.8-flash-low", "chatcmpl-2", 1700000000, toolsSpec)
	st := outTool.String()
	for _, want := range []string{
		`"content":"Checking files first."`,
		`"tool_calls"`,
		`"name":"exec_task"`,
		`"name":"apply_patch"`,
		`"finish_reason":"tool_calls"`,
		`data: [DONE]`,
	} {
		if !strings.Contains(st, want) {
			t.Fatalf("工具调用 SSE 输出缺少 %q:\n%s", want, st)
		}
	}
}

func TestBuildIsolatedCLIEnvAndBridgeHealth(t *testing.T) {
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

	// 测试单容器 agy-bridge HTTP Handler 鉴权与 /health（默认 stateless 零磁盘残留 + verbatim 提示词透传 + 自适应工具）
	h := NewAgyBridgeHandler(BridgeConfig{
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
		`"prompt_passthrough":"verbatim"`,
		`"adaptive_tools":true`,
	} {
		if rrHealth.Code != http.StatusOK || !strings.Contains(healthBody, want) {
			t.Fatalf("/health 响应异常，缺少 %q: code=%d body=%s", want, rrHealth.Code, healthBody)
		}
	}

	rrUnauth := httptest.NewRecorder()
	h.ServeHTTP(rrUnauth, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rrUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("未携带 API Key 访问 /v1/models 应返回 401，实际 %d", rrUnauth.Code)
	}

	// 测试 /v0/management/sessions 鉴权与会话列表查询
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

	// 1. 两个不同上游 session_id 绑定各自独立的 agy conversation_id (si)
	store.Bind("up-sess-A", "agy-si-11111111", "gemini-3.8-flash-low", 1)
	store.Bind("up-sess-B", "agy-si-22222222", "gemini-3.8-flash-low", 1)

	entA, okA := store.Lookup("up-sess-A")
	entB, okB := store.Lookup("up-sess-B")
	if !okA || !okB || entA.AgyConversationID == entB.AgyConversationID {
		t.Fatalf("不同上游 session_id 必须映射到不同 agy si: A=%+v B=%+v", entA, entB)
	}

	// 2. 续接同一上游 session_id 时，buildAgyCLIPrompt 仅发送新增轮次的增量内容
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

	// 3. 删除后自动变为未绑定，下次自动开新 si
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
		filepath.Join(cliRoot, "conversations", convID+".db"):      bytes.Repeat([]byte("SQLite"), 200),
		filepath.Join(cliRoot, "conversations", convID+".db-wal"):  []byte("wal"),
		filepath.Join(cliRoot, "conversations", convID+".db-shm"):  []byte("shm"),
		filepath.Join(cliRoot, "brain", convID, "artifact.md"):     []byte("# temp"),
		filepath.Join(cliRoot, "annotations", convID+".json"):      []byte(`{}`),
		filepath.Join(cliRoot, "implicit", convID+".state"):        []byte(`ok`),
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

	// 1. 路径穿越与非法 ID 严格拒绝
	for _, bad := range []string{"", "short", "../escape-dir", "a/b/cdefgh", `a\b\cdefgh`, "bad*wildcard"} {
		if removed, _ := CleanupConversationDiskArtifacts(home, bad); removed != 0 {
			t.Fatalf("非法 convID %q 不应删除任何文件，实际删除 %d", bad, removed)
		}
	}

	// 2. 精准删除 targetID，且绝不影响 keepID
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

	// 3. CLISessionStore OnEvict（LRU 淘汰 + Delete + Clear）自动联动磁盘清理
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
	// 容量为 2，绑定第 3 个将 LRU 淘汰 sess-1 并自动删除 id1 磁盘文件
	store.Bind("sess-3", id3, "gemini-3.8-flash-low", 1)

	if _, err := os.Stat(filepath.Join(cliRoot, "conversations", id1+".db")); !os.IsNotExist(err) {
		t.Fatalf("LRU 淘汰后 id1 磁盘文件应已自动清理")
	}
	if _, err := os.Stat(filepath.Join(cliRoot, "conversations", id2+".db")); err != nil {
		t.Fatalf("活跃会话 id2 磁盘文件应保留")
	}

	// 4. 孤儿扫描清理：保留活跃会话 id2/id3，清理孤儿 keepID
	swept, _ := SweepOrphanedConversationDiskArtifacts(home, store.ActiveConversationIDs(), 0)
	if swept != 1 {
		t.Fatalf("期望清理 1 个孤儿会话 keepID，实际 swept=%d", swept)
	}
	if _, err := os.Stat(filepath.Join(cliRoot, "conversations", keepID+".db")); !os.IsNotExist(err) {
		t.Fatalf("孤儿会话 keepID 应已被扫描清理")
	}

	// 5. Clear 清空后 id2/id3 也全部从磁盘移除
	store.Clear()
	for _, id := range []string{id2, id3} {
		if _, err := os.Stat(filepath.Join(cliRoot, "conversations", id+".db")); !os.IsNotExist(err) {
			t.Fatalf("Clear 后会话 %s 磁盘文件应全部清理", id)
		}
	}
}

func TestVerbatimDownstreamPromptPassthrough(t *testing.T) {
	// 下游可能是 dsh / codex / Claude Code / OpenCode，系统提示词必须一字不差原样透传
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
	if !strings.Contains(string(prepared), "You are Claude Code") || !strings.Contains(string(prepared), "You are an interactive CLI tool") {
		t.Fatalf("PrepareOpenAIPayloadForCLI 不应篡改或剥离下游系统提示词: %s", string(prepared))
	}

	built, err := buildAgyCLIPrompt(prepared, 0)
	if err != nil {
		t.Fatalf("buildAgyCLIPrompt 失败: %v", err)
	}
	if !strings.Contains(built.Prompt, sysPrompt) {
		t.Fatalf("buildAgyCLIPrompt 未原样透传下游 system prompt:\n%s", built.Prompt)
	}
	if !strings.Contains(built.Prompt, "[API Reverse-Proxy Delivery Context]") || !strings.Contains(built.Prompt, "[Downstream Client System Instructions — Follow Verbatim]") {
		t.Fatalf("缺少净化反代协作边界与下游提示词后接标记:\n%s", built.Prompt)
	}
	ctxIdx := strings.Index(built.Prompt, "[API Reverse-Proxy Delivery Context]")
	downstreamIdx := strings.Index(built.Prompt, sysPrompt)
	if ctxIdx < 0 || downstreamIdx <= ctxIdx {
		t.Fatalf("顺序错误：反代净化上下文必须在前面，下游用户提示词必须紧接其后: ctx=%d downstream=%d", ctxIdx, downstreamIdx)
	}
}

func TestAdaptToolsForAgyCLI_MultiClientTiers(t *testing.T) {
	// 1. 0 工具 -> none
	spec0 := AdaptToolsForAgyCLI(nil, nil)
	if spec0.Tier != ToolTierNone || spec0.HasTools() {
		t.Fatalf("0 工具应返回 tier=none，实际 %+v", spec0)
	}

	// 2. dsh / codex 轻量工具集（含 custom apply_patch + local_shell + function）-> full
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

	// tool_choice=none 应彻底关闭工具解析
	specNone := AdaptToolsForAgyCLI(codexTools, "none")
	if specNone.Tier != ToolTierNone || specNone.HasTools() {
		t.Fatalf("tool_choice=none 应禁用工具解析，实际 %+v", specNone)
	}

	// 3. 中等工具集（20 个工具）-> compact
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

	// 4. 超大 MCP 工具集（60 个工具）-> signature
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

	// 5. 指定单一 tool_choice 时聚焦渲染该工具为 full 层级，即使总工具数高达 60
	specForced := AdaptToolsForAgyCLI(bigTools, map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "mcp_server_action_42"},
	})
	if specForced.Tier != ToolTierFull || specForced.ForcedToolName != "mcp_server_action_42" {
		t.Fatalf("强制单工具应聚焦为 full 层级，实际 tier=%s forced=%q", specForced.Tier, specForced.ForcedToolName)
	}

	// 6. 校验未声明的 agy 内置 IDE 工具不会被当作外部工具发给下游
	prose, calls := ExtractToolCallsFromModelText(
		`Let me check. <tool_call>{"name":"run_command","arguments":{"cmd":"whoami"}}</tool_call> <tool_call>{"name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}</tool_call>`,
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
