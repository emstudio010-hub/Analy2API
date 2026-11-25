package prompt

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cmd2api/internal/domain"
)

func TestDefaultTemplatesAndGlobalPrompt(t *testing.T) {
	gp := DefaultGlobalPrompt()
	if !strings.Contains(gp, "Ess-Agentic") {
		t.Fatalf("DefaultGlobalPrompt 应包含 Ess-Agentic，得到: %q", gp)
	}

	tpls := DefaultTemplates()
	if len(tpls) < 4 {
		t.Fatalf("DefaultTemplates 应至少包含 4 套内置模板，得到 %d", len(tpls))
	}

	wantNames := map[string]bool{
		"ess-agentic-core":      false,
		"ess-agentic-architect": false,
		"ess-agentic-concise":   false,
		"ess-agentic-auditor":   false,
	}
	for _, tpl := range tpls {
		if _, exists := wantNames[tpl.Name]; exists {
			wantNames[tpl.Name] = true
		}
		if strings.TrimSpace(tpl.Content) == "" {
			t.Errorf("模板 %q 内容不应为空", tpl.Name)
		}
		if !tpl.IsBuiltin {
			t.Errorf("模板 %q IsBuiltin 应为 true", tpl.Name)
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("缺少要求的内置模板 %q", name)
		}
	}

	// 验证返回防御性拷贝
	tpls[0].Name = "mutated"
	if DefaultTemplates()[0].Name == "mutated" {
		t.Error("DefaultTemplates() 应返回拷贝，避免外部篡改")
	}
}

func TestResolveSystemPrompt_AllFourModesTemplateFallbackAndVars(t *testing.T) {
	tplID := int64(101)
	templates := map[int64]string{
		tplID: "TEMPLATE[date={{date}},model={{model}},platform={{platform}},group={{group}}]",
	}
	global := GlobalConfig{
		Enabled: true,
		Prompt:  "GLOBAL[{{date}}|{{platform}}]",
	}
	groups := []GroupConfig{
		{
			GroupID:      1,
			GroupName:    "agy-prod",
			Platform:     domain.PlatformAntigravity,
			Mode:         domain.PromptModeAppendGlobal,
			SystemPrompt: "GROUP[{{model}}|{{group}}]",
			TemplateID:   &tplID, // SystemPrompt 非空时应优先使用 SystemPrompt
		},
		{
			GroupID:      2,
			GroupName:    "wb-tpl",
			Platform:     domain.PlatformWorkBuddy,
			Mode:         domain.PromptModeAppendGlobal,
			SystemPrompt: "   ", // 空白时自动回退 TemplateID
			TemplateID:   &tplID,
		},
		{
			GroupID:      3,
			GroupName:    "override-grp",
			Platform:     domain.PlatformCommandCode,
			Mode:         domain.PromptModeOverrideGlobal,
			SystemPrompt: "OVERRIDE[{{platform}}]",
		},
		{
			GroupID:      4,
			GroupName:    "replace-grp",
			Platform:     domain.PlatformOpenCode,
			Mode:         domain.PromptModeReplaceAll,
			SystemPrompt: "STRICT_ONLY[{{model}}]",
		},
		{
			GroupID:      5,
			GroupName:    "replace-fallback-global",
			Platform:     domain.PlatformWorkBuddy,
			Mode:         domain.PromptModeReplaceAll,
			SystemPrompt: "",
		},
		{
			GroupID:      6,
			GroupName:    "off-grp",
			Platform:     domain.PlatformWorkBuddy,
			Mode:         domain.PromptModeOff,
			SystemPrompt: "SHOULD_NOT_APPEAR",
		},
	}

	eng := NewEngine(global, groups, templates)

	// 1. append_global（SystemPrompt 非空优先于 TemplateID）
	final, replaceClient, layers := eng.ResolveSystemPrompt(1, "CLIENT_SYS", Vars{
		Date:  "2026-11-18",
		Model: "gemini-2.5-pro",
	})
	want1 := "GLOBAL[2026-11-18|antigravity]\n\nGROUP[gemini-2.5-pro|agy-prod]\n\nCLIENT_SYS"
	if final != want1 || replaceClient || !reflect.DeepEqual(layers, []string{"global", "group", "client"}) {
		t.Errorf("append_global 不符合预期:\nfinal=%q\nwant =%q\nreplace=%v layers=%v", final, want1, replaceClient, layers)
	}

	// 2. append_global + 模板回退 + 默认当天 UTC 日期填充
	todayUTC := time.Now().UTC().Format("2006-01-02")
	final, replaceClient, layers = eng.ResolveSystemPrompt(2, "CLIENT_WB", Vars{Model: "claude-sonnet-4-6"})
	want2 := fmt.Sprintf(
		"GLOBAL[%s|workbuddy]\n\nTEMPLATE[date=%s,model=claude-sonnet-4-6,platform=workbuddy,group=wb-tpl]\n\nCLIENT_WB",
		todayUTC, todayUTC,
	)
	if final != want2 || replaceClient || !reflect.DeepEqual(layers, []string{"global", "template", "client"}) {
		t.Errorf("模板回退不符合预期:\nfinal=%q\nwant =%q\nreplace=%v layers=%v", final, want2, replaceClient, layers)
	}

	// 3. override_global：忽略全局提示词，仅保留 分组 + 客户端
	final, replaceClient, layers = eng.ResolveSystemPrompt(3, "CLIENT_CC", Vars{Date: "2026-11-18"})
	want3 := "OVERRIDE[commandcode]\n\nCLIENT_CC"
	if final != want3 || replaceClient || !reflect.DeepEqual(layers, []string{"group", "client"}) {
		t.Errorf("override_global 不符合预期: final=%q replace=%v layers=%v", final, replaceClient, layers)
	}

	// 4. replace_all：彻底丢弃客户端提示词，使用分组提示词
	final, replaceClient, layers = eng.ResolveSystemPrompt(4, "CLIENT_MUST_BE_STRIPPED", Vars{
		Date:  "2026-11-18",
		Model: "gpt-5",
	})
	if final != "STRICT_ONLY[gpt-5]" || !replaceClient || !reflect.DeepEqual(layers, []string{"group"}) {
		t.Errorf("replace_all 不符合预期: final=%q replace=%v layers=%v", final, replaceClient, layers)
	}

	// 5. replace_all（分组提示词为空时自动回退全局提示词）
	final, replaceClient, layers = eng.ResolveSystemPrompt(5, "CLIENT_MUST_BE_STRIPPED", Vars{Date: "2026-11-18"})
	if final != "GLOBAL[2026-11-18|workbuddy]" || !replaceClient || !reflect.DeepEqual(layers, []string{"global"}) {
		t.Errorf("replace_all 回退全局不符合预期: final=%q replace=%v layers=%v", final, replaceClient, layers)
	}

	// 6. off：不注入任何额外提示词，layers = ["off"]
	final, replaceClient, layers = eng.ResolveSystemPrompt(6, "RAW_CLIENT_ONLY", Vars{Date: "2026-11-18"})
	if final != "RAW_CLIENT_ONLY" || replaceClient || !reflect.DeepEqual(layers, []string{"off"}) {
		t.Errorf("off 模式不符合预期: final=%q replace=%v layers=%v", final, replaceClient, layers)
	}
}

func TestApplyToMessages_ReplaceAllStripsSystemDeveloperAndDedup(t *testing.T) {
	eng := NewEngine(
		GlobalConfig{Enabled: true, Prompt: "GLOBAL_GUARD"},
		[]GroupConfig{
			{GroupID: 10, GroupName: "append", Platform: "antigravity", Mode: domain.PromptModeAppendGlobal, SystemPrompt: "GROUP_APPEND"},
			{GroupID: 20, GroupName: "override", Platform: "antigravity", Mode: domain.PromptModeOverrideGlobal, SystemPrompt: "GROUP_OVERRIDE"},
			{GroupID: 30, GroupName: "replace", Platform: "antigravity", Mode: domain.PromptModeReplaceAll, SystemPrompt: "GROUP_REPLACE_ALL"},
			{GroupID: 40, GroupName: "off", Platform: "antigravity", Mode: domain.PromptModeOff, SystemPrompt: "GROUP_OFF"},
		},
		nil,
	)

	origMessages := []map[string]any{
		{"role": "system", "content": "client system 1"},
		{"role": "developer", "content": "client developer instructions"},
		{"role": "user", "content": "hello world"},
		{"role": "assistant", "content": "hi"},
	}

	// 1. replace_all 必须剥离全部原有 system / developer 消息，仅保留单条前置 system + user/assistant
	replaced := eng.ApplyToMessages(30, origMessages, Vars{Date: "2026-11-18"})
	if len(replaced) != 3 {
		t.Fatalf("replace_all 后消息数应为 3 (1 system + user + assistant)，得到 %d: %+v", len(replaced), replaced)
	}
	if replaced[0]["role"] != "system" || replaced[0]["content"] != "GROUP_REPLACE_ALL" {
		t.Errorf("replace_all 首条消息不对: %+v", replaced[0])
	}
	if replaced[1]["role"] != "user" || replaced[2]["role"] != "assistant" {
		t.Errorf("replace_all 非系统消息顺序不对: %+v", replaced)
	}
	// 确认未破坏入参切片
	if len(origMessages) != 4 {
		t.Error("ApplyToMessages 不应原地篡改入参切片")
	}

	// 2. append_global 前置注入且二次调用幂等防重
	appendedOnce := eng.ApplyToMessages(10, origMessages, Vars{Date: "2026-11-18"})
	if len(appendedOnce) != 5 {
		t.Fatalf("append_global 首次注入消息数应为 5，得到 %d", len(appendedOnce))
	}
	if appendedOnce[0]["role"] != "system" || appendedOnce[0]["content"] != "GLOBAL_GUARD\n\nGROUP_APPEND" {
		t.Errorf("append_global 前置消息不对: %+v", appendedOnce[0])
	}
	appendedTwice := eng.ApplyToMessages(10, appendedOnce, Vars{Date: "2026-11-18"})
	if len(appendedTwice) != len(appendedOnce) {
		t.Errorf("append_global 二次调用应自动防重，得到 len=%d, want %d", len(appendedTwice), len(appendedOnce))
	}

	// 3. override_global 仅注入分组提示词，不含全局提示词
	overridden := eng.ApplyToMessages(20, origMessages, Vars{Date: "2026-11-18"})
	if overridden[0]["content"] != "GROUP_OVERRIDE" {
		t.Errorf("override_global 前置内容应为 GROUP_OVERRIDE，得到 %v", overridden[0]["content"])
	}

	// 4. off 原样保留
	offMsgs := eng.ApplyToMessages(40, origMessages, Vars{Date: "2026-11-18"})
	if !reflect.DeepEqual(offMsgs, origMessages) {
		t.Errorf("off 模式应保持消息不变，得到 %+v", offMsgs)
	}
}

func TestEngineConcurrentReloadAndResolve(t *testing.T) {
	eng := NewDefaultEngine()
	var wg sync.WaitGroup

	for writer := range 4 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range 100 {
				tplID := int64(i + 1)
				eng.Reload(
					GlobalConfig{Enabled: i%2 == 0, Prompt: fmt.Sprintf("G-%d-%d {{model}}", w, i)},
					[]GroupConfig{
						{
							GroupID:    1,
							GroupName:  "concurrent-grp",
							Platform:   domain.PlatformAntigravity,
							Mode:       domain.PromptModeAppendGlobal,
							TemplateID: &tplID,
						},
						{
							GroupID:      2,
							GroupName:    "replace-grp",
							Platform:     domain.PlatformWorkBuddy,
							Mode:         domain.PromptModeReplaceAll,
							SystemPrompt: fmt.Sprintf("R-%d-%d {{date}}", w, i),
						},
					},
					map[int64]string{
						tplID: fmt.Sprintf("TPL-%d {{group}}", i),
					},
				)
			}
		}(writer)
	}

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs := []map[string]any{
				{"role": "system", "content": "sys"},
				{"role": "user", "content": "ping"},
			}
			for i := range 200 {
				gid := int64((i % 2) + 1)
				_, _, _ = eng.ResolveSystemPrompt(gid, "client", Vars{Model: "claude-opus-4-6"})
				_ = eng.ApplyToMessages(gid, msgs, Vars{Model: "claude-opus-4-6"})
			}
		}()
	}

	wg.Wait()
}
