package antigravity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	// 验证 v1internal SSE 已无损转换为标准 OpenAI SSE（包含 reasoning_content、tool_calls 与 usage）
	sseOut, _ := io.ReadAll(resp.Body)
	sseText := string(sseOut)
	if !strings.Contains(sseText, `"reasoning_content":"let me check"`) {
		t.Fatalf("转换后的 OpenAI SSE 缺少 reasoning_content: %s", sseText)
	}
	if !strings.Contains(sseText, `"name":"exec_task"`) || !strings.Contains(sseText, `"finish_reason":"tool_calls"`) {
		t.Fatalf("转换后的 OpenAI SSE 缺少 tool_calls 或 tool_calls finish_reason: %s", sseText)
	}
	if !strings.Contains(sseText, `"cached_tokens":20`) || !strings.Contains(sseText, "data: [DONE]") {
		t.Fatalf("转换后的 OpenAI SSE 缺少 usage 或 [DONE]: %s", sseText)
	}
}
