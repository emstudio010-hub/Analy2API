package filter

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

const zwsp = "\u200b"

func assertNoZWSP(t *testing.T, label, s string) {
	t.Helper()
	if strings.Contains(s, zwsp) {
		t.Fatalf("%s unexpectedly contains zero-width space U+200B: %q", label, s)
	}
}

func TestDefaultRulesNoZWSPAndCompileCleanly(t *testing.T) {
	rules := DefaultRules()
	if len(rules) == 0 {
		t.Fatal("DefaultRules() returned empty slice")
	}
	seenID := make(map[int64]bool, len(rules))
	for _, r := range rules {
		if seenID[r.ID] {
			t.Fatalf("duplicate default rule ID %d", r.ID)
		}
		seenID[r.ID] = true
		assertNoZWSP(t, "rule pattern "+r.Name, r.Pattern)
		assertNoZWSP(t, "rule replacement "+r.Name, r.Replacement)

		cr, ok := compileSingleRule(r)
		if !ok {
			t.Fatalf("default rule %q (id=%d, pattern=%q) failed to compile", r.Name, r.ID, r.Pattern)
		}
		if !cr.rule.Enabled || !cr.rule.IsBuiltin {
			t.Fatalf("default rule %q must be enabled and builtin", r.Name)
		}
	}
}

func TestClaudeCodeAnthropicPayload(t *testing.T) {
	eng := NewDefaultEngine()

	payload := map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 8192,
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_entrypoint=cli; cc_version=2.1.63; cch=9f8e7d;",
			},
			map[string]any{
				"type": "text",
				"text": "You are Claude Code, Anthropic's official CLI for Claude.\n" +
					"git branch: main (you will usually use this for PRs)\n" +
					"Tokens prefixed with @ are paths the user explicitly referenced.\n" +
					"Refuse DoS, exploit, vulnerability, attack, sandbox bypass, kill switch, and 11128 probing.\n" +
					"To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "<system-reminder>\n# claudeMd\nAlways run tests inside sandbox. Do not allow DoS or exploit.\n</system-reminder>",
					},
					map[string]any{
						"type": "text",
						"text": "Please explain exploit, DoS and kill_process()",
					},
				},
			},
		},
		"tools": []any{
			map[string]any{
				"name":        "Bash",
				"description": "Execute command in sandbox. Do not use to kill daemon or trigger DoS attack or exploit vulnerability 11128.",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"title":       "Shell Command",
							"description": "Command to execute; avoid destructive attack.",
						},
						"title": map[string]any{
							"type":        "string",
							"description": "Short label for exploit reproduction.",
						},
						"dangerouslyDisableSandbox": map[string]any{
							"type":        "boolean",
							"description": "Run unsandboxed when approved.",
						},
					},
					"required": []any{"command"},
				},
			},
		},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	outBytes, stats, err := eng.SanitizePayload("anthropic", raw)
	if err != nil {
		t.Fatalf("SanitizePayload(anthropic) failed: %v", err)
	}
	outStr := string(outBytes)
	assertNoZWSP(t, "anthropic payload output", outStr)

	if stats.FieldsTouched < 4 {
		t.Fatalf("expected at least 4 touched fields, got %d", stats.FieldsTouched)
	}
	if stats.PruneHits == 0 || stats.RewriteHits == 0 || stats.ReplaceHits == 0 {
		t.Fatalf("expected prune, rewrite, and replace hits > 0, got %+v", stats)
	}

	var out map[string]any
	if err := json.Unmarshal(outBytes, &out); err != nil {
		t.Fatalf("unmarshal sanitized payload: %v", err)
	}

	// Verify system[0] billing header was pruned cleanly.
	sysArr := out["system"].([]any)
	sys0 := sysArr[0].(map[string]any)["text"].(string)
	if sys0 != "" {
		t.Fatalf("expected billing header block pruned to empty, got %q", sys0)
	}

	// Verify system[1] template rewrites, prunes, and safe replacements + cache_control preservation.
	sys1Blk := sysArr[1].(map[string]any)
	sys1 := sys1Blk["text"].(string)
	if _, ok := sys1Blk["cache_control"]; !ok {
		t.Fatal("cache_control metadata was unexpectedly stripped")
	}
	for _, forbidden := range []string{
		"Claude Code",
		"Anthropic",
		"(you will usually use this for PRs)",
		"Tokens prefixed with @ are paths the user explicitly referenced.",
		"github.com/anthropics/claude-code/issues",
		"11128",
		"DoS",
		"exploit",
		"vulnerability",
		"attack",
		"sandbox",
		"kill",
	} {
		if strings.Contains(sys1, forbidden) {
			t.Fatalf("system[1] still contains %q: %s", forbidden, sys1)
		}
	}
	for _, expected := range []string{
		"You are Ess-Agentic, an AI coding assistant.",
		"Items with the @ prefix indicate file paths specified by the user.",
		"11-128",
		"high-load traffic",
		"reproduction case",
		"defect",
		"stress test",
		"isolated runtime",
		"terminate",
	} {
		if !strings.Contains(sys1, expected) {
			t.Fatalf("system[1] missing expected replacement %q: %s", expected, sys1)
		}
	}

	// Verify user message: harness <system-reminder> sanitized, real user question untouched.
	msgs := out["messages"].([]any)
	userContent := msgs[0].(map[string]any)["content"].([]any)
	reminderText := userContent[0].(map[string]any)["text"].(string)
	if strings.Contains(reminderText, "DoS") || strings.Contains(reminderText, "exploit") || strings.Contains(reminderText, "sandbox") {
		t.Fatalf("user <system-reminder> was not sanitized: %s", reminderText)
	}
	if !strings.Contains(reminderText, "isolated runtime") {
		t.Fatalf("expected sandbox -> isolated runtime in <system-reminder>, got %s", reminderText)
	}
	realQuestion := userContent[1].(map[string]any)["text"].(string)
	if realQuestion != "Please explain exploit, DoS and kill_process()" {
		t.Fatalf("genuine user question was modified: %q", realQuestion)
	}

	// Verify tool structure preserved while descriptions/titles sanitized.
	tools := out["tools"].([]any)
	bash := tools[0].(map[string]any)
	if bash["name"] != "Bash" {
		t.Fatalf("tool name changed: %v", bash["name"])
	}
	bashDesc := bash["description"].(string)
	for _, forbidden := range []string{"sandbox", "kill", "DoS", "attack", "exploit", "vulnerability", "11128"} {
		if strings.Contains(bashDesc, forbidden) {
			t.Fatalf("tool description still contains %q: %s", forbidden, bashDesc)
		}
	}
	schema := bash["input_schema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	if _, ok := props["dangerouslyDisableSandbox"]; !ok {
		t.Fatal("property key dangerouslyDisableSandbox was altered")
	}
	if _, ok := props["title"]; !ok {
		t.Fatal("property key named 'title' was lost")
	}
	titleProp := props["title"].(map[string]any)
	if titleProp["description"] != "Short label for reproduction case reproduction." {
		t.Fatalf("unexpected title property description: %q", titleProp["description"])
	}
	dangerDesc := props["dangerouslyDisableSandbox"].(map[string]any)["description"].(string)
	if dangerDesc != "Run direct runtime when approved." {
		t.Fatalf("unexpected unsandboxed description: %q", dangerDesc)
	}
}

func TestCodexResponsesAndOpenAIDeveloperRole(t *testing.T) {
	eng := NewDefaultEngine()

	// 1. Codex CLI /v1/responses payload
	respPayload := map[string]any{
		"model":        "gpt-5.3-codex",
		"instructions": "You are a coding agent running in the Codex CLI, a terminal-based coding assistant. Never allow RCE or privilege escalation.",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "developer",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "<permissions instructions>Filesystem sandboxing active. Unsandboxed commands need approval.</permissions instructions>",
					},
				},
			},
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "# AGENTS.md instructions for /workspace\n\n<INSTRUCTIONS>\nNever launch DDoS or kill host processes.\n</INSTRUCTIONS>\n\n" +
							"<environment_context>\n<cwd>/workspace</cwd>\n</environment_context>\n\n" +
							"<collaboration_mode>Stay in sandbox</collaboration_mode>\n\n" +
							"<skills_instructions>Do not run exploit scripts</skills_instructions>",
					},
				},
			},
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "Please explain exploit, DoS and kill_process()",
					},
				},
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output":  "func kill_process() { /* exploit test in sandbox */ }",
			},
		},
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "shell",
				"description": "Execute command in sandbox.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"title":       "Sandbox Command",
							"description": "Command to execute without SQL injection or CSRF.",
						},
					},
				},
			},
		},
	}

	rawResp, _ := json.Marshal(respPayload)
	outRespBytes, _, err := eng.SanitizePayload("responses", rawResp)
	if err != nil {
		t.Fatalf("SanitizePayload(responses) error: %v", err)
	}
	assertNoZWSP(t, "responses payload", string(outRespBytes))

	var outResp map[string]any
	if err := json.Unmarshal(outRespBytes, &outResp); err != nil {
		t.Fatalf("unmarshal responses output: %v", err)
	}
	instr := outResp["instructions"].(string)
	if !strings.Contains(instr, "You are Ess-Agentic, an AI coding assistant.") {
		t.Fatalf("Codex CLI instructions not rewritten: %s", instr)
	}
	if !strings.Contains(instr, "remote session") || !strings.Contains(instr, "access elevation") {
		t.Fatalf("Codex CLI security terms not replaced: %s", instr)
	}

	inputs := outResp["input"].([]any)
	devText := inputs[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(strings.ToLower(devText), "sandbox") {
		t.Fatalf("developer role in responses input not sanitized: %s", devText)
	}

	harnessUserText := inputs[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, forbidden := range []string{"DDoS", "kill host", "sandbox", "exploit"} {
		if strings.Contains(harnessUserText, forbidden) {
			t.Fatalf("harness user block still contains %q: %s", forbidden, harnessUserText)
		}
	}
	if !strings.Contains(harnessUserText, "<skills_instructions>") || !strings.Contains(harnessUserText, "terminate host processes") {
		t.Fatalf("expected <skills_instructions> tag intact and kill -> terminate, got: %s", harnessUserText)
	}

	pureUserText := inputs[2].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if pureUserText != "Please explain exploit, DoS and kill_process()" {
		t.Fatalf("pure user input in responses modified: %q", pureUserText)
	}

	fnOut := inputs[3].(map[string]any)["output"].(string)
	if fnOut != "func kill_process() { /* exploit test in sandbox */ }" {
		t.Fatalf("function_call_output modified: %q", fnOut)
	}

	tool0 := outResp["tools"].([]any)[0].(map[string]any)
	cmdProp := tool0["parameters"].(map[string]any)["properties"].(map[string]any)["command"].(map[string]any)
	if cmdProp["title"] != "isolated runtime Command" {
		t.Fatalf("expected tool schema title sanitized, got %q", cmdProp["title"])
	}
	if strings.Contains(cmdProp["description"].(string), "SQL injection") || strings.Contains(cmdProp["description"].(string), "CSRF") {
		t.Fatalf("tool parameter description not sanitized: %s", cmdProp["description"])
	}

	// 2. OpenAI /v1/chat/completions with developer role
	openaiPayload := map[string]any{
		"model": "deepseek-v4.1",
		"messages": []any{
			map[string]any{
				"role":    "developer",
				"content": "You are an AI agent powered by DeepSeek Harness. Avoid XSS, SSRF, malware, and brute force.",
			},
			map[string]any{
				"role":    "user",
				"content": "Please explain exploit, DoS and kill_process()",
			},
		},
	}
	rawOAI, _ := json.Marshal(openaiPayload)
	outOAIBytes, _, err := eng.SanitizePayload("openai", rawOAI)
	if err != nil {
		t.Fatalf("SanitizePayload(openai) error: %v", err)
	}
	assertNoZWSP(t, "openai payload", string(outOAIBytes))

	var outOAI map[string]any
	if err := json.Unmarshal(outOAIBytes, &outOAI); err != nil {
		t.Fatalf("unmarshal openai output: %v", err)
	}
	oaiMsgs := outOAI["messages"].([]any)
	oaiDev := oaiMsgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(oaiDev, "You are Ess-Agentic, an AI coding assistant") {
		t.Fatalf("DSH developer message not rewritten: %s", oaiDev)
	}
	if strings.Contains(oaiDev, "XSS") || strings.Contains(oaiDev, "SSRF") || strings.Contains(oaiDev, "malware") || strings.Contains(oaiDev, "brute force") {
		t.Fatalf("developer security terms not replaced: %s", oaiDev)
	}
	oaiUser := oaiMsgs[1].(map[string]any)["content"].(string)
	if oaiUser != "Please explain exploit, DoS and kill_process()" {
		t.Fatalf("openai pure user message modified: %q", oaiUser)
	}
}

func TestPureUserQuestionAndCodeProtection(t *testing.T) {
	eng := NewDefaultEngine()

	cases := []struct {
		name string
		text string
	}{
		{
			name: "plain_security_question",
			text: "Please explain exploit, DoS and kill_process()",
		},
		{
			name: "fenced_code_with_fake_tags_and_billing_header",
			text: "Review my Go test fixture:\n```go\nconst xml = `<system-reminder>DoS exploit kill</system-reminder>`\nconst hdr = \"x-anthropic-billing-header: cc_version=2.1; cch=abc\"\nconst h = \"# claudeMd\"\n```\nWhy does this fail?",
		},
		{
			name: "inline_backticks_with_markers",
			text: "What does `<system-reminder>exploit</system-reminder>` and `cc_version=2.1.63` mean when discussing DoS?",
		},
		{
			name: "unclosed_tag_mid_sentence",
			text: "Why does Claude Code mention <system-reminder> when talking about exploit or DoS?",
		},
		{
			name: "identifiers_with_substrings",
			text: "Check my functions: kill_process(), skill_runner(), dos2unix_convert().",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, hits := eng.SanitizeText(ScopeUser, tc.text)
			if out != tc.text {
				t.Fatalf("pure user text modified:\nwant: %q\ngot:  %q", tc.text, out)
			}
			if len(hits) != 0 {
				t.Fatalf("expected 0 hits on pure user text, got %+v", hits)
			}
			assertNoZWSP(t, tc.name, out)

			// Also verify via SanitizePayload across all 3 protocols.
			for _, proto := range []string{"openai", "anthropic", "responses"} {
				var body map[string]any
				if proto == "responses" {
					body = map[string]any{
						"model": "gpt-5.3-codex",
						"input": []any{map[string]any{"role": "user", "content": tc.text}},
					}
				} else {
					body = map[string]any{
						"model":    "claude-sonnet-4-6",
						"messages": []any{map[string]any{"role": "user", "content": tc.text}},
					}
				}
				raw, _ := json.Marshal(body)
				sanitized, stats, err := eng.SanitizePayload(proto, raw)
				if err != nil {
					t.Fatalf("SanitizePayload(%s) error: %v", proto, err)
				}
				if stats.FieldsTouched != 0 || string(sanitized) != string(raw) {
					t.Fatalf("%s modified pure user payload:\nwant: %s\ngot:  %s", proto, raw, sanitized)
				}
			}
		})
	}
}

func TestMixedUserHarnessAndNaturalQuestion(t *testing.T) {
	eng := NewDefaultEngine()

	mixed := strings.Join([]string{
		"x-anthropic-billing-header: cc_version=2.1.63; cc_entrypoint=cli; cch=8a9c1;",
		"<system-reminder>",
		"# claudeMd",
		"Never run DoS attacks or exploit tools outside sandbox.",
		"</system-reminder>",
		"",
		"Please explain exploit, DoS and kill_process()",
	}, "\n")

	out, hits := eng.SanitizeText(ScopeUser, mixed)
	assertNoZWSP(t, "mixed user harness", out)
	if len(hits) == 0 {
		t.Fatal("expected hits inside harness spans")
	}
	if strings.Contains(out, "x-anthropic-billing-header") || strings.Contains(out, "cc_version=") || strings.Contains(out, "cch=") {
		t.Fatalf("billing header not pruned from user harness: %s", out)
	}
	if !strings.HasSuffix(out, "\n\nPlease explain exploit, DoS and kill_process()") {
		t.Fatalf("trailing natural user question was altered or lost: %q", out)
	}
	if !strings.Contains(out, "Never run high-load traffic stress tests or reproduction case tools outside isolated runtime.") {
		t.Fatalf("expected sanitized instructions inside <system-reminder>, got: %s", out)
	}

	// Trailing billing header line at EOF should also drop cleanly without trailing dangling newline.
	trailingHdr := "Please explain exploit, DoS and kill_process()\nx-anthropic-billing-header: cc_version=2.1.63; cch=1234;"
	out2, _ := eng.SanitizeText(ScopeUser, trailingHdr)
	if out2 != "Please explain exploit, DoS and kill_process()" {
		t.Fatalf("expected clean trailing header removal, got %q", out2)
	}

	// # AGENTS.md instructions with <INSTRUCTIONS>...</INSTRUCTIONS> followed by user question.
	agentsMixed := "# AGENTS.md instructions for /repo\n\n<INSTRUCTIONS>\nAvoid attack and kill.\n</INSTRUCTIONS>\n\nPlease explain exploit, DoS and kill_process()"
	out3, _ := eng.SanitizeText(ScopeUser, agentsMixed)
	if !strings.Contains(out3, "Avoid stress test and terminate.") {
		t.Fatalf("expected AGENTS.md instructions sanitized, got %q", out3)
	}
	if !strings.HasSuffix(out3, "\n\nPlease explain exploit, DoS and kill_process()") {
		t.Fatalf("user question after </INSTRUCTIONS> modified: %q", out3)
	}

	// Attributed <system-reminder source="hook"> and scalar Responses API input.
	attrReminder := `<system-reminder source="hook">Avoid DoS</system-reminder>` + "\n\nExplain exploit"
	out4, _ := eng.SanitizeText(ScopeUser, attrReminder)
	if out4 != `<system-reminder source="hook">Avoid high-load traffic</system-reminder>`+"\n\nExplain exploit" {
		t.Fatalf("unexpected attributed reminder output: %q", out4)
	}

	scalarResp := []byte(`{"model":"gpt-5.3-codex","instructions":"Avoid DoS","input":"<environment_context>Stay in sandbox</environment_context>\n\nPlease explain exploit, DoS and kill_process()"}`)
	outScalar, scalarStats, err := eng.SanitizePayload("responses", scalarResp)
	if err != nil {
		t.Fatalf("SanitizePayload(scalar responses) error: %v", err)
	}
	if scalarStats.FieldsTouched != 2 {
		t.Fatalf("expected 2 touched fields in scalar responses, got %d", scalarStats.FieldsTouched)
	}
	var scalarObj map[string]any
	if err := json.Unmarshal(outScalar, &scalarObj); err != nil {
		t.Fatalf("unmarshal scalar responses: %v", err)
	}
	if scalarObj["instructions"] != "Avoid high-load traffic" {
		t.Fatalf("unexpected scalar instructions: %v", scalarObj["instructions"])
	}
	if scalarObj["input"] != "<environment_context>Stay in isolated runtime</environment_context>\n\nPlease explain exploit, DoS and kill_process()" {
		t.Fatalf("unexpected scalar input: %v", scalarObj["input"])
	}
}

func TestAllDefaultSecurityAndBrandReplacements(t *testing.T) {
	eng := NewDefaultEngine()

	// Ensure word boundaries protect code identifiers even in system scope.
	idOut, _ := eng.SanitizeText(ScopeSystem, "call kill_process() and skill() and dos2unix")
	if idOut != "call kill_process() and skill() and dos2unix" {
		t.Fatalf("word-boundary rules corrupted identifiers: %q", idOut)
	}

	pairs := []struct {
		in   string
		want string
	}{
		{"Claude Code with Anthropic Claude Opus, Claude Sonnet, Claude Haiku", "Ess-Agentic with Provider Ess-Agentic Model, Ess-Agentic Model, Ess-Agentic Model"},
		{"Codex CLI and DeepSeek Harness", "Ess-Agentic and Ess-Agentic"},
		{"OpenAI Codex, Codex, ChatGPT, GPT-5, GPT-4o, GPT-4, GPT-3.5, GPT, gpt-5.3-codex", "Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic-Model, Ess-Agentic-Model, Ess-Agentic-Model, Ess-Agentic-Model, Ess-Agentic-Model, Ess-Agentic-Model.3-Ess-Agentic"},
		{"DeepSeek, Deepseek, DeepSeek-R1, DeepSeek-V3", "Ess-Agentic, Ess-Agentic, Ess-Agentic-Model, Ess-Agentic-Model"},
		{"Gemini Cloud, Gemini CLI, Gemini Code Assist, Google Gemini, Gemini Pro, Gemini Flash, Gemini Ultra, Gemini", "Ess-Agentic Cloud, Ess-Agentic, Ess-Agentic, Ess-Agentic Model, Ess-Agentic Model, Ess-Agentic Model, Ess-Agentic Model, Ess-Agentic"},
		{"Google Cloud, Vertex AI, Google DeepMind, DeepMind, OpenAI, Claude", "Cloud Service, Cloud Service, Provider, Provider, Provider, Ess-Agentic"},
		{"Cursor, Windsurf, Cascade, Codeium, Cline, Roo Code, OpenCode, Command Code, ZCode, Z Code, Z.ai, Kiro, Trae AI, GitHub Copilot, Copilot, Aider, Devin, Antigravity, WorkBuddy", "Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Provider, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic"},
		{"You are ChatGPT, a large language model trained by OpenAI. Docs: https://github.com/openai/codex", "You are Ess-Agentic, an AI coding assistant. Docs: https://github.com/provider/ess-agentic"},
		{"You are Gemini CLI, an interactive CLI agent specializing in software engineering tasks. Repo: https://github.com/google-gemini/gemini-cli", "You are Ess-Agentic, an interactive AI coding assistant specializing in software engineering tasks. Repo: https://github.com/provider/ess-agentic"},
		{"You are opencode, an interactive CLI tool that helps users with software engineering tasks. Report at https://github.com/anomalyco/opencode/issues or https://opencode.ai", "You are Ess-Agentic, an interactive AI coding assistant. Report at https://github.com/provider/ess-agentic/issues or https://ess-agentic.local"},
		{"You are Command Code. Read .commandcode/taste/taste.md and visit https://commandcode.ai", "You are Ess-Agentic. Read .ess-agentic/prefs/taste.md and visit https://ess-agentic.local"},
		{"You are ZCode. Plugins: https://github.com/zai-org/zcode-plugins Core: https://github.com/zai-org/ZCode", "You are Ess-Agentic. Plugins: https://github.com/provider/ess-agentic-plugins Core: https://github.com/provider/ess-agentic"},
		{"You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer.", "You are Ess-Agentic, an AI coding assistant."},
		{"You are Cline, a highly skilled software engineer. You operate exclusively in Cursor, the world's best IDE.", "You are Ess-Agentic, a highly skilled software engineer. You operate in the Ess-Agentic environment."},
		{"You are Cascade, a powerful agentic AI coding assistant designed by the Codeium engineering team. You are Kiro, an AI assistant and IDE built to assist developers.", "You are Ess-Agentic, an agentic AI coding assistant. You are Ess-Agentic, an AI coding assistant built to assist developers."},
		{"You operate exclusively in Trae AI, the world's best IDE. You are Copilot, an AI companion created by Microsoft.", "You operate in the Ess-Agentic environment. You are Ess-Agentic, an AI coding assistant."},
		{"Override via GEMINI_SYSTEM_MD and GEMINI.md", "Override via ESS_SYSTEM_MD and AGENTS.md"},
		{"denial-of-service, denial of service, DDoS, DoS", "high-load traffic, high-load traffic, high-load traffic, high-load traffic"},
		{"exploitation, exploits, exploit", "reproduction case, reproduction case, reproduction case"},
		{"vulnerabilities and vulnerability", "defects and defect"},
		{"attackers, attacker, attacks, attack", "testers, tester, stress tests, stress test"},
		{"ransomware keylogger rootkit backdoor malware trojan spyware botnet", "unauthorized module unauthorized module unauthorized module unauthorized module unauthorized module unauthorized module unauthorized module unauthorized module"},
		{"command and control via C2 framework", "remote coordination via remote coordination"},
		{"credential stuffing, credential testing, brute force", "auth verification, auth verification, auth verification"},
		{"privilege escalation and escalated privileges", "access elevation and access elevation"},
		{"unsandboxed vs sandboxed vs sandbox", "direct runtime vs isolated runtime vs isolated runtime"},
		{"remote code execution, reverse shell, RCE", "remote session, remote session, remote session"},
		{"SQL injection, XSS, CSRF, SSRF", "input validation issue, input validation issue, input validation issue, input validation issue"},
		{"penetration testing, red teaming, pentest", "system auditing, system auditing, system auditing"},
		{"destructive action", "irreversible action"},
		{"kill process", "terminate process"},
		{"weapons weapon explosive bomb terrorist self-harm suicide", "restricted_item restricted_item restricted_item restricted_item restricted_item restricted_item restricted_item"},
		{"bare x-anthropic-billing-header reference", "bare x-client-billing-hdr reference"},
	}

	for _, p := range pairs {
		got, _ := eng.SanitizeText(ScopeSystem, p.in)
		assertNoZWSP(t, p.in, got)
		if got != p.want {
			t.Errorf("input %q:\nwant: %q\ngot:  %q", p.in, p.want, got)
		}
	}

	// Verify brand/model interception works across tools and user <environment_context> harness blocks,
	// while natural user questions mentioning competitor names remain completely untouched.
	mixedPayload := []byte(`{
		"model": "gpt-5",
		"messages": [
			{"role": "system", "content": "Built with Codex, ChatGPT, Deepseek, Gemini Cloud, OpenCode, Command Code, ZCode."},
			{"role": "user", "content": "<environment_context>client=Codex ide=Cursor</environment_context>\n\nCompare Codex, ChatGPT, GPT-5, Deepseek, and Gemini Cloud."}
		],
		"tools": [{"type": "function", "function": {"name": "search", "description": "Query Gemini Cloud or ChatGPT API"}}]
	}`)
	outBytes, _, err := eng.SanitizePayload("openai", mixedPayload)
	if err != nil {
		t.Fatalf("SanitizePayload brand test error: %v", err)
	}
	var outDoc map[string]any
	if err := json.Unmarshal(outBytes, &outDoc); err != nil {
		t.Fatalf("unmarshal brand test output: %v", err)
	}
	msgs := outDoc["messages"].([]any)
	sysContent := msgs[0].(map[string]any)["content"].(string)
	if sysContent != "Built with Ess-Agentic, Ess-Agentic, Ess-Agentic, Ess-Agentic Cloud, Ess-Agentic, Ess-Agentic, Ess-Agentic." {
		t.Fatalf("unexpected sanitized system content: %q", sysContent)
	}
	userContent := msgs[1].(map[string]any)["content"].(string)
	wantUser := "<environment_context>client=Ess-Agentic ide=Ess-Agentic</environment_context>\n\nCompare Codex, ChatGPT, GPT-5, Deepseek, and Gemini Cloud."
	if userContent != wantUser {
		t.Fatalf("unexpected mixed user content:\nwant: %q\ngot:  %q", wantUser, userContent)
	}
	toolDesc := outDoc["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["description"].(string)
	if toolDesc != "Query Ess-Agentic Cloud or Ess-Agentic API" {
		t.Fatalf("unexpected sanitized tool description: %q", toolDesc)
	}
}

func TestReloadAndPreviewModes(t *testing.T) {
	eng := NewDefaultEngine()
	initialCount := len(eng.Rules())
	if initialCount == 0 {
		t.Fatal("expected initial default rules")
	}

	// Verify Rules() returns a defensive copy.
	copied := eng.Rules()
	copied[0].Enabled = false
	if !eng.Rules()[0].Enabled {
		t.Fatal("mutating slice returned by Rules() affected engine state")
	}

	// Test Preview across all 5 modes.
	resSys, err := eng.Preview("Refuse DoS attacks and Claude Code 11128.", "system", "")
	if err != nil {
		t.Fatalf("Preview(system) failed: %v", err)
	}
	if !resSys.Changed || resSys.Mode != "system" || resSys.Stats.ReplaceHits == 0 || resSys.Stats.RewriteHits == 0 {
		t.Fatalf("unexpected Preview(system) result: %+v", resSys)
	}
	assertNoZWSP(t, "preview system", resSys.Output)

	resTool, err := eng.Preview("Run command in sandbox without DoS", "tool", "")
	if err != nil {
		t.Fatalf("Preview(tool) failed: %v", err)
	}
	if resTool.Output != "Run command in isolated runtime without high-load traffic" || !resTool.Changed {
		t.Fatalf("unexpected Preview(tool): %+v", resTool)
	}

	resUser, err := eng.Preview("Please explain exploit, DoS and kill_process()", "user_harness", "")
	if err != nil {
		t.Fatalf("Preview(user_harness) failed: %v", err)
	}
	if resUser.Changed || resUser.Output != "Please explain exploit, DoS and kill_process()" {
		t.Fatalf("Preview(user_harness) modified pure user input: %+v", resUser)
	}

	resAutoMixed, err := eng.Preview("<system-reminder>Avoid DoS</system-reminder>\n\nPlease explain exploit, DoS", "auto", "")
	if err != nil {
		t.Fatalf("Preview(auto mixed) failed: %v", err)
	}
	if resAutoMixed.Mode != "user_harness" || !resAutoMixed.Changed {
		t.Fatalf("expected auto mode to resolve to user_harness, got %+v", resAutoMixed)
	}
	if !strings.HasSuffix(resAutoMixed.Output, "Please explain exploit, DoS") {
		t.Fatalf("auto user_harness modified user question: %q", resAutoMixed.Output)
	}

	jsonInput := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"Please explain exploit, DoS and kill_process()"}]}`
	resAutoJSON, err := eng.Preview(jsonInput, "auto", "openai")
	if err != nil {
		t.Fatalf("Preview(auto json) failed: %v", err)
	}
	if resAutoJSON.Mode != "json" || resAutoJSON.Changed || resAutoJSON.Output != jsonInput {
		t.Fatalf("expected untouched pure user JSON in preview, got %+v", resAutoJSON)
	}

	if _, err := eng.Preview("{bad json", "json", "openai"); err == nil {
		t.Fatal("expected error for invalid JSON in json mode")
	}
	if _, err := eng.Preview("hi", "invalid_mode", ""); err == nil {
		t.Fatal("expected error for unsupported preview mode")
	}

	// Test hot Reload with custom rules + priority/length ordering.
	customRules := []Rule{
		{
			ID:            1001,
			Name:          "custom-short",
			Kind:          KindReplace,
			Pattern:       "foo",
			Replacement:   "bar",
			Scope:         ScopeAllInjected,
			CaseSensitive: false,
			WordBoundary:  true,
			Enabled:       true,
			Priority:      50,
		},
		{
			ID:            1002,
			Name:          "custom-long",
			Kind:          KindReplace,
			Pattern:       "foo bar",
			Replacement:   "baz",
			Scope:         ScopeAllInjected,
			CaseSensitive: false,
			WordBoundary:  true,
			Enabled:       true,
			Priority:      50,
		},
	}
	eng.Reload(customRules)
	if len(eng.Rules()) != 2 {
		t.Fatalf("expected 2 rules after reload, got %d", len(eng.Rules()))
	}
	reloadedOut, _ := eng.SanitizeText(ScopeSystem, "foo bar and DoS")
	if reloadedOut != "baz and DoS" {
		t.Fatalf("expected longer same-priority rule to win and old rules replaced, got %q", reloadedOut)
	}
}

func TestIdempotencyAndConcurrency(t *testing.T) {
	eng := NewDefaultEngine()

	raw := []byte(`{
		"model": "claude-sonnet-4-6",
		"system": "x-anthropic-billing-header: cc_version=2.1; cc_entrypoint=cli; cch=abc;\nYou are Claude Code, Anthropic's official CLI for Claude. Avoid DoS, exploit, sandbox, kill, 11128.",
		"messages": [{"role": "user", "content": "<system-reminder>Avoid DoS</system-reminder>\n\nExplain exploit"}],
		"tools": [{"name": "Run", "description": "Run in sandbox", "input_schema": {"type": "object"}}]
	}`)

	first, stats1, err := eng.SanitizePayload("anthropic", raw)
	if err != nil {
		t.Fatalf("first pass error: %v", err)
	}
	if stats1.FieldsTouched == 0 {
		t.Fatal("expected first pass to modify payload")
	}
	second, stats2, err := eng.SanitizePayload("anthropic", first)
	if err != nil {
		t.Fatalf("second pass error: %v", err)
	}
	if stats2.FieldsTouched != 0 || string(second) != string(first) {
		t.Fatalf("expected idempotent second pass, got touched=%d", stats2.FieldsTouched)
	}

	// Concurrent reads + reloads.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if idx == 0 && j%5 == 0 {
					eng.Reload(DefaultRules())
				} else {
					out, _, err := eng.SanitizePayload("anthropic", raw)
					if err != nil {
						t.Errorf("concurrent SanitizePayload failed: %v", err)
						return
					}
					assertNoZWSP(t, "concurrent output", string(out))
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestFalsePositiveAudit_KillCursorCascadeAndNumbers(t *testing.T) {
	eng := NewDefaultEngine()

	// Safe coding phrases that MUST NEVER be altered even in ScopeSystem / ScopeTool:
	safeCodingInputs := []string{
		"Load the skill and reap any killed worker or killing goroutines.",
		"Call childProcess.kill('SIGTERM') or proc.kill() or /bin/kill with --kill-after=5s.",
		"Emacs kill-ring and auto-kill timeout settings.",
		"Move the database cursor, set CSS cursor: pointer, or run DECLARE CURSOR in SQL.",
		"Configure CSS cascade layers and PostgreSQL ON DELETE CASCADE foreign keys.",
		"Commit 202611128 hash 0xfa11128b on build_111289.",
	}
	for _, sample := range safeCodingInputs {
		gotSys, hitsSys := eng.SanitizeText(ScopeSystem, sample)
		if gotSys != sample || len(hitsSys) != 0 {
			t.Errorf("false positive in ScopeSystem:\ninput: %q\ngot:   %q\nhits:  %+v", sample, gotSys, hitsSys)
		}
		gotTool, hitsTool := eng.SanitizeText(ScopeTool, sample)
		if gotTool != sample || len(hitsTool) != 0 {
			t.Errorf("false positive in ScopeTool:\ninput: %q\ngot:   %q\nhits:  %+v", sample, gotTool, hitsTool)
		}
	}

	// Genuine brand/security targets MUST still be sanitized:
	gotBrand, _ := eng.SanitizeText(ScopeSystem, "You are Antigravity, a powerful agentic AI assistant designed by the Google Deepmind team. Built for Cursor and Cascade.")
	if strings.Contains(gotBrand, "Antigravity") || strings.Contains(gotBrand, "Cursor") || strings.Contains(gotBrand, "Cascade") {
		t.Fatalf("expected capitalized brands neutralized, got: %q", gotBrand)
	}
}

func TestCrossPlatformHarnessTags_All11Harnesses(t *testing.T) {
	eng := NewDefaultEngine()

	userWithAllWrappers := strings.Join([]string{
		"<user_info>OS: win32, IDE: Cursor</user_info>",
		"<cursor_context>Active workspace in Cursor</cursor_context>",
		"<open_and_recently_viewed_files>main.go (opened in Cursor)</open_and_recently_viewed_files>",
		"<cascade_context>Windsurf Cascade flow active</cascade_context>",
		"<environment_details>Running inside Cline / Roo Code sandbox</environment_details>",
		"<system-reminder>Claude Code reminder: avoid DoS</system-reminder>",
		"<user-prompt-submit-hook>Claude hook check</user-prompt-submit-hook>",
		"<fast_mode_info>Claude Opus fast mode</fast_mode_info>",
		"<user_memory>Gemini CLI memory note</user_memory>",
		"<identity>You are Antigravity</identity>",
		"<conversation_transcript>Antigravity transcript</conversation_transcript>",
		"<environment_context>Codex CLI cwd=/repo</environment_context>",
		"<dsh_context>DeepSeek Harness shell=zsh</dsh_context>",
		"",
		"How do Cursor, Cascade, Claude Code, exploit, DoS, and kill_process() compare?",
	}, "\n")

	// 1. InspectHarnessBlocks detects all 13 platform wrapper blocks.
	detected := InspectHarnessBlocks(userWithAllWrappers, ScopeUser)
	if len(detected) != 13 {
		t.Fatalf("expected 13 detected harness blocks, got %d: %+v", len(detected), detected)
	}

	// 2. ScopeUser sanitizes inside every wrapper while preserving trailing natural user question verbatim!
	sanitizedUser, hits := eng.SanitizeText(ScopeUser, userWithAllWrappers)
	if len(hits) == 0 {
		t.Fatal("expected rule hits inside cross-platform harness blocks")
	}
	wantSuffix := "\n\nHow do Cursor, Cascade, Claude Code, exploit, DoS, and kill_process() compare?"
	if !strings.HasSuffix(sanitizedUser, wantSuffix) {
		t.Fatalf("natural user question was altered:\ngot: %q", sanitizedUser)
	}
	headOnly := strings.TrimSuffix(sanitizedUser, wantSuffix)
	for _, leak := range []string{"Cursor", "Windsurf", "Cascade", "Cline", "Roo Code", "Claude Code", "Claude Opus", "Gemini CLI", "Antigravity", "Codex CLI", "DeepSeek Harness", "DoS", "sandbox"} {
		if strings.Contains(headOnly, leak) {
			t.Fatalf("wrapper block still leaked %q:\n%s", leak, headOnly)
		}
	}

	// 3. Auto preview mode recognizes new platform wrappers (e.g. Cursor <user_info>) as user_harness.
	pv, err := eng.Preview("<user_info>IDE: Cursor</user_info>\n\nExplain DoS and exploit", "auto", "")
	if err != nil {
		t.Fatalf("Preview(auto cursor user_info) failed: %v", err)
	}
	if pv.Mode != "user_harness" || !strings.HasSuffix(pv.Output, "Explain DoS and exploit") {
		t.Fatalf("expected user_harness mode preserving user question, got mode=%q out=%q", pv.Mode, pv.Output)
	}

	// 4. StripConflictingSystemHarnesses strips IDE-only blocks (<artifacts>, <slash_commands>, <open_and_recently_viewed_files>) while keeping core rules.
	rawSys := "<identity>\nYou are Antigravity.\n</identity>\n<artifacts>\nWrite to brain dir\n</artifacts>\n<slash_commands>\n/goal\n</slash_commands>\nKeep code clean and tested."
	strippedSys, sysDetected := StripConflictingSystemHarnesses(rawSys)
	if len(sysDetected) < 3 {
		t.Fatalf("expected >=3 detected system blocks, got %+v", sysDetected)
	}
	if strings.Contains(strippedSys, "<artifacts>") || strings.Contains(strippedSys, "<slash_commands>") {
		t.Fatalf("expected conflicting AGY blocks stripped, got: %q", strippedSys)
	}
	if !strings.Contains(strippedSys, "Keep code clean and tested.") || !strings.Contains(strippedSys, "<identity>") {
		t.Fatalf("expected core instructions preserved, got: %q", strippedSys)
	}
}
