package toolobf

import (
	"regexp"
	"strings"
	"testing"

	"cmd2api/internal/domain"
)

func TestModesDeterminismAndBijection(t *testing.T) {
	identRe := regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`)

	// 1. Hash 模式：确定性 + 随盐值变化 + 64 字符超长 MCP 工具安全压缩
	cfg1 := DefaultConfig()
	cfg1.Mode = ModeHash
	cfg1.Salt = "salt-alpha"
	eng1 := NewEngine(cfg1)

	longMCP := "mcp__very_long_github_enterprise_server_namespace__create_or_update_pull_request_review_comment_thread"
	tools := []string{"Bash", "Read", "Edit", longMCP, "custom_tool"}

	s1a := eng1.NewSession(domain.PlatformWorkBuddy)
	s1b := eng1.NewSession(domain.PlatformAntigravity)
	for _, orig := range tools {
		o1 := s1a.Obfuscate(orig)
		o2 := s1b.Obfuscate(orig)
		if o1 != o2 {
			t.Fatalf("相同盐值与模式下工具 %q 混淆结果不一致：%q vs %q", orig, o1, o2)
		}
		if o1 == orig {
			t.Fatalf("工具 %q 未被混淆: %q", orig, o1)
		}
		if len(o1) > MaxToolNameLen || !identRe.MatchString(o1) {
			t.Fatalf("工具 %q 混淆结果 %q 不符合长度<=64或命名规范", orig, o1)
		}
		if got := s1a.Restore(o1); got != orig {
			t.Fatalf("还原失败：Restore(%q) = %q，期望 %q", o1, got, orig)
		}
	}

	// 改盐后应产生不同哈希后缀
	cfg2 := cfg1
	cfg2.Salt = "salt-beta"
	eng2 := NewEngine(cfg2)
	s2 := eng2.NewSession(domain.PlatformWorkBuddy)
	if s2.Obfuscate("Bash") == s1a.Obfuscate("Bash") {
		t.Fatal("更换盐值后 Hash 模式应输出不同的混淆工具名")
	}

	// 2. Alias 模式：命中中性别名（如 Bash -> exec_task）及自动消歧（Bash + bash）
	aliasCfg := DefaultConfig()
	aliasCfg.Mode = ModeAlias
	aliasCfg.Salt = "salt-alias"
	aliasEng := NewEngine(aliasCfg)
	sa := aliasEng.NewSession(domain.PlatformOpenCode)
	obBash := sa.Obfuscate("Bash")
	obLowerBash := sa.Obfuscate("bash")
	if obBash != "exec_task" {
		t.Fatalf("Alias 模式 Bash 应映射为 exec_task，实际 %q", obBash)
	}
	if obLowerBash == obBash {
		t.Fatalf("Bash 与 bash 同别名时必须自动消歧，实际都为 %q", obLowerBash)
	}
	if sa.Restore(obBash) != "Bash" || sa.Restore(obLowerBash) != "bash" {
		t.Fatalf("双射还原失败：obBash=%q->%q obLower=%q->%q", obBash, sa.Restore(obBash), obLowerBash, sa.Restore(obLowerBash))
	}
	if obUnknown := sa.Obfuscate("my_business_calc"); !strings.HasPrefix(obUnknown, DefaultPrefix) {
		t.Fatalf("未知工具在 Alias 模式下应回退 prefix+hash，实际 %q", obUnknown)
	}

	// 3. Prefix 模式：保留中性前缀，超长截断+摘要且可逆
	pfxCfg := DefaultConfig()
	pfxCfg.Mode = ModePrefix
	pfxCfg.Prefix = "op_"
	pfxCfg.Salt = "salt-pfx"
	pfxEng := NewEngine(pfxCfg)
	sp := pfxEng.NewSession(domain.PlatformCommandCode)
	if got := sp.Obfuscate("Bash"); got != "op_Bash" {
		t.Fatalf("Prefix 模式 Bash 期望 op_Bash，实际 %q", got)
	}
	obLong := sp.Obfuscate(longMCP)
	if len(obLong) > MaxToolNameLen || !strings.HasPrefix(obLong, "op_") {
		t.Fatalf("超长 MCP 工具 Prefix 模式不符合约束：len=%d val=%q", len(obLong), obLong)
	}
	if sp.Restore(obLong) != longMCP {
		t.Fatalf("超长工具还原失败：got=%q want=%q", sp.Restore(obLong), longMCP)
	}
}

func TestExcludeScopeCustomMapPlatformAndPrefixBuffer(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Mode:    ModeAlias,
		Prefix:  DefaultPrefix,
		Salt:    "s1",
		Scope:   ScopeSensitiveOnly,
		Platforms: []string{
			domain.PlatformWorkBuddy,
			domain.PlatformAntigravity,
		},
		CustomMap: map[string]string{
			"Bash":         "exec_task", // 覆盖或匹配内置
			"custom_query": "biz_lookup",
		},
		ExcludeTools:    []string{"Read"},
		RewriteToolRefs: true,
	}

	eng := NewEngine(cfg)
	if !eng.EnabledForPlatform("WorkBuddy") || eng.EnabledForPlatform(domain.PlatformOpenCode) {
		t.Fatalf("平台过滤判定异常：wb=%v oc=%v", eng.EnabledForPlatform("WorkBuddy"), eng.EnabledForPlatform(domain.PlatformOpenCode))
	}
	if eng.NewSession(domain.PlatformOpenCode) != nil {
		t.Fatal("未启用平台不应创建 Session")
	}

	sess := eng.NewSession(domain.PlatformWorkBuddy)
	sess.RegisterPassthrough("Read")

	if got := sess.Obfuscate("Read"); got != "Read" {
		t.Fatalf("ExcludeTools 中的 Read 应原样透传，实际 %q", got)
	}
	if got := sess.Obfuscate("domain_weather"); got != "domain_weather" {
		t.Fatalf("sensitive_only 模式下非敏感工具应原样透传，实际 %q", got)
	}
	if got := sess.Obfuscate("Bash"); got != "exec_task" {
		t.Fatalf("CustomMap 应生效，实际 %q", got)
	}
	if got := sess.Obfuscate("custom_query"); got != "biz_lookup" {
		t.Fatalf("CustomMap custom_query 应命中 biz_lookup，实际 %q", got)
	}
	if got := sess.Obfuscate("mcp__fs__list"); got == "mcp__fs__list" {
		t.Fatal("mcp__ 前缀工具在 sensitive_only 模式下必须混淆")
	}
	entries := sess.Entries()
	if len(entries) < 2 || entries[0].Original != "Read" || entries[1].Original != "domain_weather" {
		t.Fatalf("RegisterPassthrough 后应保持首次 Obfuscate 的原始顺序，得到 %+v", entries)
	}

	// Passthrough 冲突规避：若后续新工具原本拟生成名字刚好等于已被保留的 passthrough 名
	if sess.IsObfuscatedPrefix("Re") {
		t.Fatal("未发生混淆的透传工具 Read 不应触发 IsObfuscatedPrefix")
	}
	if !sess.IsObfuscatedPrefix("exec_") {
		t.Fatal("exec_ 应被识别为混淆工具 exec_task 的严格真前缀")
	}
	if sess.IsObfuscatedPrefix("exec_task") {
		t.Fatal("完整匹配名本身不应视为真前缀")
	}

	// 反引号工具引用安全替换（绝不替换普通英文散文里的 bash/Read，不替换代码块标记 ```）
	desc := "Use `Bash` or `custom_query`, never change Read or plain Bash prose, keep ```bash\ncode``` intact."
	rewritten := sess.RewriteBacktickRefs(desc)
	wantDesc := "Use `exec_task` or `biz_lookup`, never change Read or plain Bash prose, keep ```bash\ncode``` intact."
	if rewritten != wantDesc {
		t.Fatalf("RewriteBacktickRefs 结果异常:\ngot:  %q\nwant: %q", rewritten, wantDesc)
	}
}

func TestNormalizeConfigValidation(t *testing.T) {
	// Duplicate custom target should fail
	_, err := NormalizeConfig(Config{
		Enabled:   true,
		Mode:      ModeHash,
		Scope:     ScopeAll,
		Platforms: []string{domain.PlatformWorkBuddy},
		CustomMap: map[string]string{
			"Bash": "same_name",
			"Read": "same_name",
		},
	})
	if err == nil {
		t.Fatal("两个不同源工具映射到相同自定义目标名时应报错")
	}

	// Invalid custom target name should fail
	_, err = NormalizeConfig(Config{
		Enabled:   true,
		Mode:      ModeHash,
		Scope:     ScopeAll,
		Platforms: []string{domain.PlatformWorkBuddy},
		CustomMap: map[string]string{
			"Bash": "123 invalid space!",
		},
	})
	if err == nil {
		t.Fatal("非法自定义工具目标名应被校验拒绝")
	}

	// Prefix starting with digit gets safely prefixed
	norm, err := NormalizeConfig(Config{
		Enabled:   true,
		Mode:      ModePrefix,
		Prefix:    "9tool ",
		Platforms: []string{"WorkBuddy", "workbuddy"},
	})
	if err != nil {
		t.Fatalf("合法规范化意外失败: %v", err)
	}
	if !strings.HasPrefix(norm.Prefix, "ess_9tool") {
		t.Fatalf("数字开头前缀规范化异常: %q", norm.Prefix)
	}
	if len(norm.Platforms) != 1 || norm.Platforms[0] != domain.PlatformWorkBuddy {
		t.Fatalf("平台规范化去重异常: %+v", norm.Platforms)
	}
}

func TestPerGroupOverrides(t *testing.T) {
	rwFalse := false
	rwTrue := true

	// 全局关闭，但分组 10 显式开启 alias 且带自定义后缀、自定义映射；分组 20 强制关闭；分组 30 继承
	cfg := Config{
		Enabled:         false,
		Mode:            ModeHash,
		Prefix:          "glb_",
		Salt:            "group-salt",
		Scope:           ScopeAll,
		Platforms:       []string{domain.PlatformWorkBuddy},
		CustomMap:       map[string]string{"Read": "glb_read"},
		ExcludeTools:    []string{"ping"},
		RewriteToolRefs: true,
		GroupOverrides: map[int64]GroupOverride{
			10: {
				Mode:            ModeAlias,
				Prefix:          "g10_",
				Scope:           ScopeSensitiveOnly,
				CustomMap:       map[string]string{"custom_tool": "g10_biz"},
				ExcludeTools:    []string{"Glob"},
				RewriteToolRefs: &rwFalse,
			},
			20: {
				Mode: GroupModeOff,
			},
			30: {
				Mode:            GroupModeInherit,
				Prefix:          "g30_",
				RewriteToolRefs: &rwTrue,
			},
		},
	}

	eng := NewEngine(cfg)

	// 全局关闭时，分组 30（inherit）不应开启
	if eng.EnabledForGroupPlatform(30, domain.PlatformWorkBuddy) {
		t.Fatal("全局关闭且分组 30 为 inherit 时不应开启混淆")
	}
	if eng.NewSessionForGroup(30, domain.PlatformWorkBuddy) != nil {
		t.Fatal("分组 30 inherit 全局关闭时不应返回 session")
	}

	// 分组 10 显式开启 alias（即使全局关闭、且平台甚至可以是 opencode）
	if !eng.EnabledForGroupPlatform(10, domain.PlatformOpenCode) {
		t.Fatal("分组 10 显式指定 alias 模式时必须生效")
	}
	s10 := eng.NewSessionForGroup(10, domain.PlatformOpenCode)
	if s10 == nil {
		t.Fatal("分组 10 session 不得为 nil")
	}
	if s10.Config().RewriteToolRefs {
		t.Fatal("分组 10 覆盖了 RewriteToolRefs=false，生效配置应为 false")
	}
	if got := s10.Obfuscate("Bash"); got != "exec_task" {
		t.Fatalf("分组 10 alias 模式 Bash 期望 exec_task，实际 %q", got)
	}
	if got := s10.Obfuscate("Glob"); got != "Glob" {
		t.Fatalf("分组 10 专属排除工具 Glob 应原样透传，实际 %q", got)
	}
	if got := s10.Obfuscate("custom_tool"); got != "g10_biz" {
		t.Fatalf("分组 10 自定义映射应生效，实际 %q", got)
	}
	if got := s10.Obfuscate("weather_forecast"); got != "weather_forecast" {
		t.Fatalf("分组 10 sensitive_only 模式下非敏感工具应原样透传，实际 %q", got)
	}

	// 全局开启时，分组 20 (off) 仍关闭，分组 30 (inherit + g30_) 生效并使用 g30_ 前缀
	cfgOn := cfg
	cfgOn.Enabled = true
	eng.Reload(cfgOn)

	if eng.EnabledForGroupPlatform(20, domain.PlatformWorkBuddy) {
		t.Fatal("分组 20 显式设为 off 时即使全局开启也不得启用混淆")
	}
	if eng.NewSessionForGroup(20, domain.PlatformWorkBuddy) != nil {
		t.Fatal("分组 20 off 时应返回 nil session")
	}
	s30 := eng.NewSessionForGroup(30, domain.PlatformWorkBuddy)
	if s30 == nil {
		t.Fatal("分组 30 inherit 在全局开启时应返回 session")
	}
	if got := s30.Obfuscate("Bash"); !strings.HasPrefix(got, "g30_") {
		t.Fatalf("分组 30 覆盖前缀 g30_ 后 hash 应以 g30_ 开头，实际 %q", got)
	}
}
