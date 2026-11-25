package domain

import "testing"

func TestNormalizeAndValidatePlatform(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", PlatformWorkBuddy, true},
		{"  ", PlatformWorkBuddy, true},
		{"workbuddy", PlatformWorkBuddy, true},
		{"WorkBuddy", PlatformWorkBuddy, true},
		{"wb", PlatformWorkBuddy, true},
		{"codebuddy", PlatformWorkBuddy, true},
		{"antigravity", PlatformAntigravity, true},
		{"AntiGravity", PlatformAntigravity, true},
		{"agy", PlatformAntigravity, true},
		{"anti-gravity", PlatformAntigravity, true},
		{"anti_gravity", PlatformAntigravity, true},
		{"cliproxy", PlatformAntigravity, true},
		{"cliproxyapi", PlatformAntigravity, true},
		{"commandcode", PlatformCommandCode, true},
		{"cc", PlatformCommandCode, true},
		{"opencode", PlatformOpenCode, true},
		{"unknown", "unknown", false},
	}

	for _, tc := range cases {
		got := NormalizePlatform(tc.in)
		if got != tc.want {
			t.Errorf("NormalizePlatform(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if IsValidPlatform(got) != tc.ok {
			t.Errorf("IsValidPlatform(%q) = %v, want %v", got, IsValidPlatform(got), tc.ok)
		}
	}

	if IsValidPlatform("") || IsValidPlatform("WorkBuddy") || IsValidPlatform("AntiGravity") {
		t.Error("IsValidPlatform 仅应接受规范化后的小写平台常量")
	}
}

func TestAPIKeyAndAdminKeyPrefix(t *testing.T) {
	if APIKeyPrefix != "sk-wb2a-" {
		t.Fatalf("APIKeyPrefix = %q, want sk-wb2a-", APIKeyPrefix)
	}
	if !HasValidAPIKeyPrefix("sk-wb2a-123456") {
		t.Error("应接受 sk-wb2a- 前缀")
	}
	if !HasValidAPIKeyPrefix("sk-c2a-123456") {
		t.Error("应兼容 sk-c2a- 前缀")
	}
	if HasValidAPIKeyPrefix("sk-other-123456") {
		t.Error("不应接受无关前缀")
	}

	if AdminKeyPrefix != "adm-" {
		t.Fatalf("AdminKeyPrefix = %q, want adm-", AdminKeyPrefix)
	}
	if !HasAdminKeyPrefix("adm-abcdef123456") {
		t.Error("应接受 adm- 前缀")
	}
	if HasAdminKeyPrefix("sk-wb2a-abcdef") || HasAdminKeyPrefix("") {
		t.Error("HasAdminKeyPrefix 不应接受非 adm- 前缀")
	}
}

func TestPromptInjectModeNormalizeAndValidate(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", PromptModeAppendGlobal, true},
		{"  ", PromptModeAppendGlobal, true},
		{"append_global", PromptModeAppendGlobal, true},
		{"APPEND-GLOBAL", PromptModeAppendGlobal, true},
		{"override_global", PromptModeOverrideGlobal, true},
		{"override-global", PromptModeOverrideGlobal, true},
		{"replace_all", PromptModeReplaceAll, true},
		{"replace-all", PromptModeReplaceAll, true},
		{"off", PromptModeOff, true},
		{"OFF", PromptModeOff, true},
		{"invalid_mode", "invalid_mode", false},
	}

	for _, tc := range cases {
		got := NormalizePromptInjectMode(tc.in)
		if got != tc.want {
			t.Errorf("NormalizePromptInjectMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if IsValidPromptInjectMode(got) != tc.ok {
			t.Errorf("IsValidPromptInjectMode(%q) = %v, want %v", got, IsValidPromptInjectMode(got), tc.ok)
		}
	}

	if IsValidPromptInjectMode("") || IsValidPromptInjectMode("APPEND_GLOBAL") {
		t.Error("IsValidPromptInjectMode 仅应接受规范化后的小写模式常量")
	}
}

func TestOmniAccountAndContainerConstants(t *testing.T) {
	if AccountTypeWorkBuddy != "workbuddy_oauth" {
		t.Errorf("AccountTypeWorkBuddy = %q", AccountTypeWorkBuddy)
	}
	if AccountTypeOAuthJSON != "oauth_json" {
		t.Errorf("AccountTypeOAuthJSON = %q", AccountTypeOAuthJSON)
	}
	if WorkBuddyDefaultDomain != "https://www.codebuddy.cn" {
		t.Errorf("WorkBuddyDefaultDomain = %q", WorkBuddyDefaultDomain)
	}
	if ContainerModeLocalDocker != "local_docker" || ContainerModeRemoteAPI != "remote_api" {
		t.Errorf("容器节点模式常量不对: %q %q", ContainerModeLocalDocker, ContainerModeRemoteAPI)
	}
	if !IsValidContainerMode(NormalizeContainerMode("")) || NormalizeContainerMode("remote-api") != ContainerModeRemoteAPI {
		t.Errorf("容器节点模式规范化不对")
	}
	if FilterKindPrune != "prune" || FilterKindRewrite != "rewrite" || FilterKindReplace != "replace" {
		t.Errorf("风控规则常量不对: %q %q %q", FilterKindPrune, FilterKindRewrite, FilterKindReplace)
	}
}
