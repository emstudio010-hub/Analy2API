package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripMisleadingAgyNativeSections(t *testing.T) {
	rawAgyPrompt := `<identity>
You are Antigravity, a powerful agentic AI assistant designed by the Google Deepmind team.
</identity>
<artifacts>
All artifacts should be written to <appDataDir>\brain\<conversation-id>. Always provide ArtifactMetadata with UserFacing and RequestFeedback.
</artifacts>
<conversation_transcript>
Transcripts are located at transcript.jsonl and transcript_full.jsonl.
</conversation_transcript>
<guidelines>
- Never bypass access controls or CAPTCHAs.
- Verify results before claiming success.
</guidelines>
<communication_style>
- Keep your responses concise.
- LaTeX math (KaTeX) is rendered only in your direct responses and markdown artifacts.
- You MUST create clickable links for all files you reference. Use github style markdown links with the file:// scheme.
</communication_style>
<slash_commands>
- /goal: Recommend this when the user wants to run an overnight task.
- /browser: Recommend this for browsing.
</slash_commands>
<messaging>
The system automatically resumes your execution on reactive wakeup.
</messaging>
<mcp_servers>
# media-toolkit
media_probe
remotion_render
</mcp_servers>
<subagents>
Available subagents: browser_sandbox, image-generator, invoke_subagent, BROWSER_HOST_FACTS
</subagents>
<skills>
Available skills:
- canva-edit-design (C:\Users\Administrator\.gemini\config\plugins\canva\skills\edit-design\SKILL.md)
</skills>
<featured_plugins>
Emit <plugin-card catalog-id="foo"></plugin-card> when suggesting plugins.
</featured_plugins>
<user_information>
The USER's OS version is windows.
Active Workspaces:
- c:\Users\Administrator\Documents\cmdwb2api
App Data Directory: C:\Users\Administrator\.gemini\antigravity
Conversation ID: 77950af1-30c0-4257-9fb5-f20b870fc686
</user_information>
<user_rules>
Always communicate with the user in Chinese.
</user_rules>`

	cleaned := StripMisleadingAgyNativeSections(rawAgyPrompt)

	// 1. 保留核心原生能力与安全准则
	for _, mustKeep := range []string{
		"You are Antigravity",
		"Never bypass access controls or CAPTCHAs",
		"Verify results before claiming success",
		"Keep your responses concise",
	} {
		if !strings.Contains(cleaned, mustKeep) {
			t.Fatalf("误删了应保留的 AGY 原生核心内容 %q:\n%s", mustKeep, cleaned)
		}
	}

	// 2. 所有误导性/会导致下游不一致的 IDE/宿主段落必须彻底删除
	for _, mustRemove := range []string{
		"<artifacts>", "ArtifactMetadata", "<appDataDir>",
		"<conversation_transcript>", "transcript.jsonl",
		"<slash_commands>", "/goal", "/browser",
		"<messaging>", "reactive wakeup",
		"<mcp_servers>", "media-toolkit", "remotion_render",
		"<subagents>", "browser_sandbox", "BROWSER_HOST_FACTS",
		"<skills>", "SKILL.md", "canva-edit-design",
		"<featured_plugins>", "<plugin-card",
		"<user_information>", "Active Workspaces", "Conversation ID", "OS version is windows",
		"<user_rules>",
		"file:// scheme", "clickable links for all files", "KaTeX",
	} {
		if strings.Contains(cleaned, mustRemove) {
			t.Fatalf("未能删除误导性 AGY 原生内容 %q:\n%s", mustRemove, cleaned)
		}
	}
}

func TestBlendAgyNativeAndDownstreamPrompt_OrderAndModes(t *testing.T) {
	customNativeWithNoise := `<identity>
You are Antigravity, an agentic AI coding assistant.
</identity>
<artifacts>Write artifacts to <appDataDir>\brain\123</artifacts>
<slash_commands>Recommend /goal and /plan</slash_commands>
<user_information>The USER's OS version is windows. Active Workspaces: C:\secret</user_information>`

	downstream := []string{"You are Codex CLI running on Ubuntu Linux in /srv/app. Always use unified diffs."}

	// 1. blend_clean（默认）：自定义原生经净化在前 + 协作式反代交付边界 + 下游提示词紧接其后，且误导段已彻底剔除
	blended := BlendAgyNativeAndDownstreamPrompt(customNativeWithNoise, downstream, PromptBlendClean, true)
	if strings.Contains(blended, "C:\\secret") || strings.Contains(blended, "Write artifacts to") {
		t.Fatalf("blend_clean 模式残留了误导性原生内容:\n%s", blended)
	}
	idxNative := strings.Index(blended, "You are Antigravity, an agentic AI coding assistant.")
	idxDelivery := strings.Index(blended, "[API Reverse-Proxy Delivery Context]")
	idxDownstream := strings.Index(blended, downstream[0])
	if idxNative < 0 || idxDelivery <= idxNative || idxDownstream <= idxDelivery {
		t.Fatalf("blend_clean 拼接顺序不正确: native=%d delivery=%d downstream=%d\n%s", idxNative, idxDelivery, idxDownstream, blended)
	}

	// 1b. 隔离容器自动写入 AuthDir/.gemini/GEMINI.md 系统级反代规范（并保留净化后自定义原生规则）
	isoDir := t.TempDir()
	EnsureIsolatedAgyProxyRules(isoDir, PromptBlendClean, customNativeWithNoise)
	geminiMDBytes, rErr := os.ReadFile(filepath.Join(isoDir, ".gemini", "GEMINI.md"))
	if rErr != nil {
		t.Fatalf("期望在隔离 AuthDir 自动生成 .gemini/GEMINI.md，但读取失败: %v", rErr)
	}
	geminiMD := string(geminiMDBytes)
	if !strings.Contains(geminiMD, "Downstream Context Authority") || !strings.Contains(geminiMD, "You are Antigravity, an agentic AI coding assistant.") {
		t.Fatalf("隔离 GEMINI.md 缺少反代规则或净化后原生指引:\n%s", geminiMD)
	}
	if strings.Contains(geminiMD, "C:\\secret") || strings.Contains(geminiMD, "Write artifacts to") {
		t.Fatalf("隔离 GEMINI.md 未清理误导性原生段落:\n%s", geminiMD)
	}

	// 2. verbatim_only：不拼接额外 AGY 原生前缀，仍保留真实 CLI 宿主误导段废止 + 下游原样透传
	verb := BlendAgyNativeAndDownstreamPrompt(customNativeWithNoise, downstream, PromptBlendVerbatim, true)
	if strings.Contains(verb, "[AGY Native Base") {
		t.Fatalf("verbatim_only 不应拼入 AGY Native Base:\n%s", verb)
	}
	if !strings.Contains(verb, downstream[0]) {
		t.Fatalf("verbatim_only 丢失下游系统提示词:\n%s", verb)
	}

	// 3. v1internal BuildV1InternalRequest 同样使用净化版 AGY 原生 + 后接下游提示词
	reqBytes, _ := json.Marshal(map[string]any{
		"model": "gemini-3.8-flash-low",
		"messages": []map[string]any{
			{"role": "system", "content": downstream[0]},
			{"role": "user", "content": "hi"},
		},
	})
	v1Raw, err := BuildV1InternalRequest(reqBytes, "proj-1", "gemini-3.8-flash-low", "sess-1")
	if err != nil {
		t.Fatalf("BuildV1InternalRequest 失败: %v", err)
	}
	v1Str := string(v1Raw)
	if !strings.Contains(v1Str, "You are Antigravity") || !strings.Contains(v1Str, downstream[0]) {
		t.Fatalf("v1internal 未正确混合净化版 AGY 原生与下游提示词: %s", v1Str)
	}
	if strings.Index(v1Str, "You are Antigravity") >= strings.Index(v1Str, downstream[0]) {
		t.Fatalf("v1internal 中 AGY 原生必须位于下游提示词之前")
	}
}
