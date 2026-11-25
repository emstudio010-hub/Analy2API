package relay

import (
	"encoding/json"
	"strings"
	"testing"

	"cmd2api/internal/toolobf"
)

func TestUniversalToolsPassthrough_CodexResponses(t *testing.T) {
	// Codex CLI 通过 /v1/responses 发送混合工具（function + custom apply_patch + local_shell + web_search_preview + mcp）
	// 以及多轮 custom_tool_call / local_shell_call 历史，网关必须 100% 全量透传，绝不丢弃任何工具。
	raw := []byte(`{
		"model": "gpt-5.3-codex",
		"instructions": "You are Codex CLI running in user's shell.",
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"description": "Run a shell command",
				"parameters": {
					"type": "object",
					"properties": {"cmd": {"type": "string"}},
					"required": ["cmd"]
				}
			},
			{
				"type": "custom",
				"name": "apply_patch",
				"description": "Apply a patch to files",
				"format": {"type": "grammar", "syntax": "lark", "definition": "start: /.+/"}
			},
			{
				"type": "local_shell"
			},
			{
				"type": "web_search_preview"
			},
			{
				"type": "mcp",
				"server_label": "github_mcp",
				"description": "GitHub MCP bridge"
			}
		],
		"tool_choice": {"type": "custom", "name": "apply_patch"},
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "fix bug"}]},
			{"type": "custom_tool_call", "call_id": "call_p1", "name": "apply_patch", "input": "*** Begin Patch\n*** End Patch"},
			{"type": "custom_tool_call_output", "call_id": "call_p1", "output": "patched ok"},
			{"type": "local_shell_call", "call_id": "call_s1", "action": {"type": "exec", "command": ["go", "test"]}},
			{"type": "local_shell_call_output", "call_id": "call_s1", "output": "PASS"}
		]
	}`)

	norm, err := ParseResponsesRequest(raw)
	if err != nil {
		t.Fatalf("ParseResponsesRequest 失败: %v", err)
	}

	if len(norm.Tools) != 5 {
		t.Fatalf("期望 5 个异构工具全部透传，实际仅保留 %d 个: %+v", len(norm.Tools), norm.Tools)
	}
	gotNames := map[string]bool{}
	for _, tool := range norm.Tools {
		fn, _ := tool["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok {
			gotNames[name] = true
		}
	}
	for _, want := range []string{"exec_command", "apply_patch", "local_shell", "web_search_preview", "github_mcp"} {
		if !gotNames[want] {
			t.Fatalf("工具 %q 在 Responses 入站解析时被丢弃，实际保留: %+v", want, gotNames)
		}
	}
	if !norm.ResponsesCustomTools["apply_patch"] {
		t.Fatalf("ResponsesCustomTools 应记录 custom 工具 apply_patch: %+v", norm.ResponsesCustomTools)
	}

	// 验证多轮 custom_tool_call 与 local_shell_call 均完整转换到 messages 历史
	var toolRoleCount, assistantCallCount int
	for _, msg := range norm.Messages {
		role, _ := msg["role"].(string)
		if role == "tool" {
			toolRoleCount++
		}
		if role == "assistant" {
			if tcs, ok := msg["tool_calls"].([]any); ok {
				assistantCallCount += len(tcs)
			}
		}
	}
	if assistantCallCount != 2 || toolRoleCount != 2 {
		t.Fatalf("期望还原 2 次工具调用与 2 条工具结果，实际 calls=%d results=%d msgs=%+v", assistantCallCount, toolRoleCount, norm.Messages)
	}

	// 验证 CommandCode 信封转换同样全量保留这 5 个工具
	env, err := buildEnvelope(norm, "agent", DeviceProfile{Platform: "linux", ProjectDir: "/workspace"}, "", false)
	if err != nil {
		t.Fatalf("buildEnvelope 失败: %v", err)
	}
	if len(env.Params.Tools) != 5 {
		t.Fatalf("CommandCode 信封应全量保留 5 个工具，实际 %d", len(env.Params.Tools))
	}
}

func TestResponsesOutput_CustomToolCallRoundtrip(t *testing.T) {
	customSet := map[string]bool{"apply_patch": true}

	// 1. 流式编码：custom 工具输出 custom_tool_call，普通工具输出 function_call
	enc := NewResponsesStreamEncoder("gpt-5.3-codex", "req123", 1700000000)
	enc.SetCustomTools(customSet)

	var frames []string
	frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
		ToolCalls: []ToolCallDelta{
			{Index: 0, ID: "call_patch_1", Name: "apply_patch", Arguments: `{"input":"*** Begin Patch\n*** End Patch"}`},
			{Index: 1, ID: "call_cmd_2", Name: "exec_command", Arguments: `{"cmd":"ls -la"}`},
		},
	})...)
	frames = append(frames, enc.Finish()...)
	joined := strings.Join(frames, "")

	for _, want := range []string{
		`"type":"custom_tool_call"`,
		`response.custom_tool_call_input.done`,
		`"input":"*** Begin Patch\n*** End Patch"`,
		`"type":"function_call"`,
		`"name":"exec_command"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Responses 流式输出缺少 %q:\n%s", want, joined)
		}
	}

	// 2. 非流式编码：同样还原 custom_tool_call 与 function_call
	var tcPatch, tcCmd openAIToolCall
	tcPatch.ID = "call_patch_1"
	tcPatch.Type = "function"
	tcPatch.Function.Name = "apply_patch"
	tcPatch.Function.Arguments = `{"input":"*** Begin Patch"}`
	tcCmd.ID = "call_cmd_2"
	tcCmd.Type = "function"
	tcCmd.Function.Name = "exec_command"
	tcCmd.Function.Arguments = `{"cmd":"pwd"}`

	full := BuildResponsesFullResponseWithCustomTools(
		"gpt-5.3-codex", "req123", 1700000000,
		"", "",
		[]openAIToolCall{tcPatch, tcCmd},
		"tool_calls", &NormalizedUsage{PromptTokens: 10, CompletionTokens: 5},
		customSet,
	)
	b, _ := json.Marshal(full)
	s := string(b)
	if !strings.Contains(s, `"type":"custom_tool_call"`) || !strings.Contains(s, `"input":"*** Begin Patch"`) || !strings.Contains(s, `"type":"function_call"`) {
		t.Fatalf("Responses 非流式输出异常:\n%s", s)
	}
}

func TestUniversalToolsPassthrough_AnthropicBuiltinTools(t *testing.T) {
	raw := []byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"system": "You are dsh agent.",
		"tools": [
			{"type": "bash_20250124", "name": "bash"},
			{"type": "text_editor_20250429", "name": "str_replace_based_edit_editor"},
			{"type": "custom_runner"},
			{"name": "dsh_inspect", "description": "Inspect env", "input_schema": {"type": "object", "properties": {"key": {"type": "string"}}}}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	norm, err := ParseAnthropicMessagesRequest(raw)
	if err != nil {
		t.Fatalf("ParseAnthropicMessagesRequest 失败: %v", err)
	}
	if len(norm.Tools) != 4 {
		t.Fatalf("期望 Anthropic 全部 4 个工具透传保留，实际 %d: %+v", len(norm.Tools), norm.Tools)
	}
}

func TestSpecialTools_DirectObfuscationAndRestoreRoundtrip(t *testing.T) {
	// 1. 直接向 /v1/chat/completions 发送各类异构特殊工具（custom apply_patch / local_shell / web_search_preview / bash_20250124 / mcp__* / dsh_exec）
	rawChat := []byte(`{
		"model": "claude-sonnet-4-6",
		"tools": [
			{"type": "custom", "name": "apply_patch", "description": "Apply diff patch"},
			{"type": "local_shell"},
			{"type": "web_search_preview"},
			{"type": "bash_20250124", "name": "bash"},
			{"type": "function", "function": {"name": "mcp__github__create_issue", "parameters": {"type": "object"}}},
			{"type": "function", "function": {"name": "dsh_exec", "parameters": {"type": "object"}}}
		],
		"tool_choice": {"type": "local_shell"},
		"messages": [
			{"role": "user", "content": "patch and run tests"},
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "apply_patch", "arguments": "{\"input\":\"*** Begin Patch\"}"}},
					{"id": "call_2", "type": "function", "function": {"name": "local_shell", "arguments": "{\"command\":[\"go\",\"test\"]}"}}
				]
			},
			{"role": "tool", "tool_call_id": "call_1", "name": "apply_patch", "content": "ok"},
			{"role": "tool", "tool_call_id": "call_2", "name": "local_shell", "content": "PASS"}
		]
	}`)

	norm, err := ParseOpenAIChatRequest(rawChat)
	if err != nil {
		t.Fatalf("ParseOpenAIChatRequest 失败: %v", err)
	}
	if len(norm.Tools) != 6 {
		t.Fatalf("期望 6 个特殊工具全部规范化保留，实际 %d: %+v", len(norm.Tools), norm.Tools)
	}

	// 2. 开启全工具直接混淆（ModeHash & ScopeAll）
	eng := toolobf.NewDefaultEngine()
	obfNorm, sess := obfuscateNormalizedRequest(eng, 1, "antigravity", norm)
	if sess == nil || sess.Count() != 6 {
		t.Fatalf("期望 6 个特殊工具全部被直接混淆，实际 count=%v", sess)
	}

	rawNames := []string{"apply_patch", "local_shell", "web_search_preview", "bash", "mcp__github__create_issue", "dsh_exec"}
	obfJSON, _ := json.Marshal(obfNorm.Tools)
	obfStr := string(obfJSON)
	for _, orig := range rawNames {
		if strings.Contains(obfStr, `"`+orig+`"`) {
			t.Fatalf("特殊工具 %q 未被混淆，仍暴露在出站 tools 中: %s", orig, obfStr)
		}
		obfName := sess.Obfuscate(orig)
		if obfName == orig {
			t.Fatalf("工具 %q 混淆名与原名相同", orig)
		}
		if restored := sess.Restore(obfName); restored != orig {
			t.Fatalf("工具 %q 混淆还原失败: obf=%q restored=%q", orig, obfName, restored)
		}
	}

	// 验证特殊 tool_choice {"type":"local_shell"} 也被正确混淆
	tcJSON, _ := json.Marshal(obfNorm.ToolChoice)
	if strings.Contains(string(tcJSON), "local_shell") || !strings.Contains(string(tcJSON), sess.Obfuscate("local_shell")) {
		t.Fatalf("特殊 tool_choice 未被混淆: %s", string(tcJSON))
	}

	// 验证历史 messages 中的 tool_calls 与 tool.name 也全部同步混淆
	msgsJSON, _ := json.Marshal(obfNorm.Messages)
	if strings.Contains(string(msgsJSON), `"apply_patch"`) || strings.Contains(string(msgsJSON), `"local_shell"`) {
		t.Fatalf("历史消息中的特殊工具名未同步混淆: %s", string(msgsJSON))
	}

	// 3. 验证 Responses custom 工具（apply_patch）经混淆 -> 还原 -> 原生 custom_tool_call 全链路
	rawResp := []byte(`{
		"model": "gpt-5.3-codex",
		"tools": [{"type": "custom", "name": "apply_patch", "description": "Apply patch"}],
		"tool_choice": {"type": "custom", "name": "apply_patch"},
		"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "fix"}]}]
	}`)
	respNorm, err := ParseResponsesRequest(rawResp)
	if err != nil {
		t.Fatalf("ParseResponsesRequest 失败: %v", err)
	}
	obfResp, respSess := obfuscateNormalizedRequest(eng, 1, "antigravity", respNorm)
	if respSess == nil || !obfResp.ResponsesCustomTools["apply_patch"] {
		t.Fatalf("Responses custom 工具元数据未在混淆后保留: %+v", obfResp.ResponsesCustomTools)
	}
	obfPatchName := respSess.Obfuscate("apply_patch")
	restoredPatchName := respSess.Restore(obfPatchName)
	enc := NewResponsesStreamEncoder("gpt-5.3-codex", "r1", 1700000000)
	enc.SetCustomTools(obfResp.ResponsesCustomTools)
	frames := strings.Join(append(
		enc.EncodeChunk(&NormalizedChunk{ToolCalls: []ToolCallDelta{{Index: 0, ID: "c1", Name: restoredPatchName, Arguments: `{"input":"*** Begin Patch"}`}}}),
		enc.Finish()...,
	), "")
	if !strings.Contains(frames, `"type":"custom_tool_call"`) || !strings.Contains(frames, `"name":"apply_patch"`) || !strings.Contains(frames, `"input":"*** Begin Patch"`) {
		t.Fatalf("特殊 custom 工具混淆还原后未正确编码为 custom_tool_call:\n%s", frames)
	}
}
