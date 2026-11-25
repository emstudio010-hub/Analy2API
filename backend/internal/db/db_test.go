package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/adminkey"
	"cmd2api/ent/containernode"
	"cmd2api/ent/filterrule"
	"cmd2api/ent/modelprice"
	"cmd2api/ent/prompttemplate"
	"cmd2api/ent/systemsetting"
	"cmd2api/ent/usagelog"
	"cmd2api/internal/config"
	"cmd2api/internal/domain"
)

func assertPragmaInt(t *testing.T, ctx context.Context, client *ent.Client, pragma string, want int) {
	t.Helper()
	rows, err := client.QueryContext(ctx, "PRAGMA "+pragma)
	if err != nil {
		t.Fatalf("查询 PRAGMA %s 失败: %v", pragma, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("PRAGMA %s 无返回行", pragma)
	}
	var got int
	if err := rows.Scan(&got); err != nil {
		t.Fatalf("扫描 PRAGMA %s 失败: %v", pragma, err)
	}
	if got != want {
		t.Errorf("PRAGMA %s = %d, want %d", pragma, got, want)
	}
}

func assertPragmaString(t *testing.T, ctx context.Context, client *ent.Client, pragma string, want string) {
	t.Helper()
	rows, err := client.QueryContext(ctx, "PRAGMA "+pragma)
	if err != nil {
		t.Fatalf("查询 PRAGMA %s 失败: %v", pragma, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("PRAGMA %s 无返回行", pragma)
	}
	var got string
	if err := rows.Scan(&got); err != nil {
		t.Fatalf("扫描 PRAGMA %s 失败: %v", pragma, err)
	}
	if !strings.EqualFold(got, want) {
		t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
	}
}

func TestOpenSQLiteMemoryAndEntities(t *testing.T) {
	ctx := context.Background()
	client, err := Open(ctx, config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: ":memory:",
	})
	if err != nil {
		t.Fatalf("打开内存 SQLite 并建表失败: %v", err)
	}
	defer client.Close()

	assertPragmaInt(t, ctx, client, "foreign_keys", 1)
	assertPragmaInt(t, ctx, client, "busy_timeout", 5000)

	// 验证默认平台与 Account 新增 WorkBuddy / 独立代理 IP 字段写入与读取。
	now := time.Now().UTC().Truncate(time.Second)
	credits := 1234.5678
	acc, err := client.Account.Create().
		SetName("wb-acc-1").
		SetType(domain.AccountTypeWorkBuddy).
		SetProxyURL("socks5://user:pass@127.0.0.1:1080").
		SetQimei36("0123456789abcdef0123456789abcdef0123").
		SetMachineID("11111111-2222-4333-8444-555555555555").
		SetUID("uid-1001").
		SetNickname("tester").
		SetEmail("tester@example.com").
		SetEnterpriseID("ent-1").
		SetEnterpriseName("Demo Org").
		SetBalanceTotalCredits(credits).
		SetBalancePackages([]map[string]any{
			{"package_code": "DAILY", "remain": 100.5, "total": 100.5},
		}).
		SetLastCheckinDate("2026-11-15").
		SetCheckinStreak(3).
		SetLastHarvestAt(now).
		SetLastHarvestSummary(map[string]any{
			"credit_gained": 200.0,
			"ok":            true,
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 Account 失败: %v", err)
	}
	if acc.Platform != domain.PlatformWorkBuddy {
		t.Errorf("Account 默认平台应为 %q，得到 %q", domain.PlatformWorkBuddy, acc.Platform)
	}
	if acc.Domain != domain.WorkBuddyDefaultDomain {
		t.Errorf("Account 默认 Domain 应为 %q，得到 %q", domain.WorkBuddyDefaultDomain, acc.Domain)
	}

	gotAcc, err := client.Account.Query().Where(account.UIDEQ("uid-1001")).Only(ctx)
	if err != nil {
		t.Fatalf("按 UID 查询 Account 失败: %v", err)
	}
	if gotAcc.ProxyURL != "socks5://user:pass@127.0.0.1:1080" {
		t.Errorf("ProxyURL = %q", gotAcc.ProxyURL)
	}
	if gotAcc.BalanceTotalCredits == nil || *gotAcc.BalanceTotalCredits != credits {
		t.Errorf("BalanceTotalCredits = %v, want %v", gotAcc.BalanceTotalCredits, credits)
	}
	if len(gotAcc.BalancePackages) != 1 {
		t.Errorf("BalancePackages len = %d, want 1", len(gotAcc.BalancePackages))
	}
	if gotAcc.CheckinStreak != 3 || gotAcc.LastCheckinDate != "2026-11-15" {
		t.Errorf("checkin fields mismatch: %+v", gotAcc)
	}

	// 验证 PromptTemplate CRUD。
	tpl, err := client.PromptTemplate.Create().
		SetName("Senior Architect").
		SetCategory("engineering").
		SetContent("Always analyze trade-offs clearly.").
		SetDescription("工程架构师模板").
		SetIsBuiltin(true).
		SetPriority(20).
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 PromptTemplate 失败: %v", err)
	}
	if tpl.Category != "engineering" || tpl.Priority != 20 || !tpl.IsBuiltin {
		t.Errorf("PromptTemplate 字段不符合预期: %+v", tpl)
	}

	// 验证 Group 默认平台、提示词字段默认值与更新，以及 AccountGroup 关联。
	grp, err := client.Group.Create().
		SetName("default-wb").
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 Group 失败: %v", err)
	}
	if grp.Platform != domain.PlatformWorkBuddy {
		t.Errorf("Group 默认平台应为 %q，得到 %q", domain.PlatformWorkBuddy, grp.Platform)
	}
	if grp.PromptInjectMode != domain.PromptModeAppendGlobal || grp.SystemPrompt != "" || grp.PromptTemplateID != nil {
		t.Errorf("Group 默认提示词配置不符合预期: mode=%q prompt=%q tpl=%v", grp.PromptInjectMode, grp.SystemPrompt, grp.PromptTemplateID)
	}
	grp, err = grp.Update().
		SetSystemPrompt("Group custom prompt").
		SetPromptInjectMode(domain.PromptModeOverrideGlobal).
		SetPromptTemplateID(tpl.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("更新 Group 提示词配置失败: %v", err)
	}
	if grp.PromptInjectMode != domain.PromptModeOverrideGlobal || grp.PromptTemplateID == nil || *grp.PromptTemplateID != tpl.ID {
		t.Errorf("更新后 Group 提示词配置不对: %+v", grp)
	}

	if _, err := client.AccountGroup.Create().
		SetAccountID(acc.ID).
		SetGroupID(grp.ID).
		SetPriority(10).
		Save(ctx); err != nil {
		t.Fatalf("写入 AccountGroup 失败: %v", err)
	}

	// 验证 User / APIKey / UsageLog（含 platform, reasoning_tokens, cache_cost）全表连通性。
	u, err := client.User.Create().
		SetEmail("admin@cmdwb2api.local").
		SetPasswordHash("hash").
		SetRole(domain.RoleAdmin).
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 User 失败: %v", err)
	}
	k, err := client.APIKey.Create().
		SetUserID(u.ID).
		SetGroupID(grp.ID).
		SetKey(domain.APIKeyPrefix + "test1234").
		SetName("cli").
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 APIKey 失败: %v", err)
	}
	ulog, err := client.UsageLog.Create().
		SetUserID(u.ID).
		SetAPIKeyID(k.ID).
		SetAccountID(acc.ID).
		SetGroupID(grp.ID).
		SetRequestID("req-1").
		SetPlatform(domain.PlatformAntigravity).
		SetModel("gemini-2.5-pro").
		SetReasoningTokens(512).
		SetCacheCost(0.00125).
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 UsageLog 失败: %v", err)
	}
	gotLogs, err := client.UsageLog.Query().
		Where(usagelog.PlatformEQ(domain.PlatformAntigravity)).
		All(ctx)
	if err != nil || len(gotLogs) != 1 || gotLogs[0].ID != ulog.ID || gotLogs[0].ReasoningTokens != 512 || gotLogs[0].CacheCost != 0.00125 {
		t.Fatalf("按 platform 查询 UsageLog 失败或字段不匹配: err=%v logs=%+v", err, gotLogs)
	}

	// 验证 FilterRule 建表、默认值、写入与查询。
	rule, err := client.FilterRule.Create().
		SetName("strip-billing-header").
		SetKind(domain.FilterKindPrune).
		SetPattern(`(?i)x-anthropic-billing-header:[^\n;]*;?\s*`).
		SetIsRegex(true).
		SetWordBoundary(false).
		SetPriority(10).
		SetIsBuiltin(true).
		SetDescription("剔除客户端计费头").
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 FilterRule 失败: %v", err)
	}
	if rule.Scope != "all_injected" || !rule.Enabled {
		t.Errorf("FilterRule 默认字段不对: scope=%q enabled=%v", rule.Scope, rule.Enabled)
	}

	rules, err := client.FilterRule.Query().
		Where(filterrule.KindEQ(domain.FilterKindPrune), filterrule.EnabledEQ(true)).
		All(ctx)
	if err != nil {
		t.Fatalf("查询 FilterRule 失败: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != rule.ID {
		t.Errorf("查询 FilterRule 结果不对: %+v", rules)
	}

	// 验证 SystemSetting CRUD。
	ss, err := client.SystemSetting.Create().
		SetKey("first_token_timeout").
		SetValue("300s").
		SetDescription("首字超时时间").
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 SystemSetting 失败: %v", err)
	}
	ss, err = client.SystemSetting.UpdateOneID(ss.ID).SetValue("360s").Save(ctx)
	if err != nil || ss.Value != "360s" {
		t.Fatalf("更新 SystemSetting 失败: err=%v val=%q", err, ss.Value)
	}
	if _, err := client.SystemSetting.Query().Where(systemsetting.KeyEQ("first_token_timeout")).Only(ctx); err != nil {
		t.Fatalf("查询 SystemSetting 失败: %v", err)
	}
	if err := client.SystemSetting.DeleteOneID(ss.ID).Exec(ctx); err != nil {
		t.Fatalf("删除 SystemSetting 失败: %v", err)
	}

	// 验证 AdminKey CRUD。
	ak, err := client.AdminKey.Create().
		SetName("ci-runner").
		SetKeyHash("sha256-abcdef0123456789").
		SetKeyPrefix("adm-abcd").
		SetAllowedIps("127.0.0.1,10.0.0.0/8").
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 AdminKey 失败: %v", err)
	}
	if ak.Scopes != "full" || ak.Status != domain.StatusActive {
		t.Errorf("AdminKey 默认值不对: scopes=%q status=%q", ak.Scopes, ak.Status)
	}
	ak, err = ak.Update().SetScopes("readonly").SetLastUsedAt(now).Save(ctx)
	if err != nil || ak.Scopes != "readonly" || ak.LastUsedAt == nil {
		t.Fatalf("更新 AdminKey 失败: err=%v ak=%+v", err, ak)
	}
	if _, err := client.AdminKey.Query().Where(adminkey.KeyHashEQ("sha256-abcdef0123456789")).Only(ctx); err != nil {
		t.Fatalf("按 KeyHash 查询 AdminKey 失败: %v", err)
	}
	if err := client.AdminKey.DeleteOneID(ak.ID).Exec(ctx); err != nil {
		t.Fatalf("删除 AdminKey 失败: %v", err)
	}

	// 验证 ContainerNode CRUD。
	node, err := client.ContainerNode.Create().
		SetName("agy-local-1").
		SetContainerName("cmd2api-agy-1").
		SetAuthDir("/data/antigravity/node-1/auths").
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 ContainerNode 失败: %v", err)
	}
	if node.Mode != domain.ContainerModeLocalDocker ||
		node.DockerImage != "routerforme/cliproxyapi:latest" ||
		node.HostIP != "127.0.0.1" ||
		node.HostPort != 8317 ||
		node.Status != "stopped" {
		t.Errorf("ContainerNode 默认值不对: %+v", node)
	}
	node, err = node.Update().
		SetStatus("running").
		SetAuthFilesCount(4).
		SetLastSyncAt(now).
		SetExtra(map[string]any{"version": "1.2.3"}).
		Save(ctx)
	if err != nil || node.Status != "running" || node.AuthFilesCount != 4 || node.Extra["version"] != "1.2.3" {
		t.Fatalf("更新 ContainerNode 失败: err=%v node=%+v", err, node)
	}
	if _, err := client.ContainerNode.Query().Where(containernode.ModeEQ(domain.ContainerModeLocalDocker)).Only(ctx); err != nil {
		t.Fatalf("查询 ContainerNode 失败: %v", err)
	}
	if err := client.ContainerNode.DeleteOneID(node.ID).Exec(ctx); err != nil {
		t.Fatalf("删除 ContainerNode 失败: %v", err)
	}

	// 验证 ModelPrice CRUD。
	mp, err := client.ModelPrice.Create().
		SetModel("claude-sonnet-4-6").
		SetInputPrice(3.0).
		SetOutputPrice(15.0).
		SetCacheReadPrice(0.3).
		SetCacheWritePrice(3.75).
		SetNotes("Anthropic Sonnet 4.6").
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 ModelPrice 失败: %v", err)
	}
	mp, err = mp.Update().SetOutputPrice(16.0).Save(ctx)
	if err != nil || mp.OutputPrice != 16.0 {
		t.Fatalf("更新 ModelPrice 失败: err=%v mp=%+v", err, mp)
	}
	if _, err := client.ModelPrice.Query().Where(modelprice.ModelEQ("claude-sonnet-4-6")).Only(ctx); err != nil {
		t.Fatalf("查询 ModelPrice 失败: %v", err)
	}
	if err := client.ModelPrice.DeleteOneID(mp.ID).Exec(ctx); err != nil {
		t.Fatalf("删除 ModelPrice 失败: %v", err)
	}

	// 验证 PromptTemplate 更新与删除。
	if _, err := client.PromptTemplate.Query().Where(prompttemplate.CategoryEQ("engineering")).Only(ctx); err != nil {
		t.Fatalf("查询 PromptTemplate 失败: %v", err)
	}
	if err := client.PromptTemplate.DeleteOneID(tpl.ID).Exec(ctx); err != nil {
		t.Fatalf("删除 PromptTemplate 失败: %v", err)
	}
}

func TestOpenSQLiteFileCreatesParentDirAndWAL(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "nested", "sub", "cmdwb2api.db")

	client, err := Open(ctx, config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: dbPath,
	})
	if err != nil {
		t.Fatalf("打开文件 SQLite 失败: %v", err)
	}
	assertPragmaInt(t, ctx, client, "foreign_keys", 1)
	assertPragmaString(t, ctx, client, "journal_mode", "wal")
	assertPragmaInt(t, ctx, client, "busy_timeout", 5000)
	if _, err := os.Stat(dbPath); err != nil {
		_ = client.Close()
		t.Fatalf("SQLite 文件应当已创建 (%s): %v", dbPath, err)
	}

	// 在文件库中写入全部新增实体，确保落盘与重开迁移正常。
	if _, err := client.SystemSetting.Create().SetKey("global_prompt_enabled").SetValue("true").Save(ctx); err != nil {
		_ = client.Close()
		t.Fatalf("文件库写入 SystemSetting 失败: %v", err)
	}
	if _, err := client.PromptTemplate.Create().SetName("Default").SetContent("Be concise.").Save(ctx); err != nil {
		_ = client.Close()
		t.Fatalf("文件库写入 PromptTemplate 失败: %v", err)
	}
	if _, err := client.AdminKey.Create().SetName("ops").SetKeyHash("hash-ops-1").SetKeyPrefix("adm-ops1").Save(ctx); err != nil {
		_ = client.Close()
		t.Fatalf("文件库写入 AdminKey 失败: %v", err)
	}
	if _, err := client.ContainerNode.Create().SetName("remote-1").SetMode(domain.ContainerModeRemoteAPI).Save(ctx); err != nil {
		_ = client.Close()
		t.Fatalf("文件库写入 ContainerNode 失败: %v", err)
	}
	if _, err := client.ModelPrice.Create().SetModel("gemini-2.5-pro").SetInputPrice(1.25).SetOutputPrice(10.0).Save(ctx); err != nil {
		_ = client.Close()
		t.Fatalf("文件库写入 ModelPrice 失败: %v", err)
	}

	// 二次打开同一文件应平滑通过迁移（幂等 Schema.Create）且保留全部数据。
	if err := client.Close(); err != nil {
		t.Fatalf("关闭 SQLite 失败: %v", err)
	}

	client2, err := Open(ctx, config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: dbPath,
	})
	if err != nil {
		t.Fatalf("二次打开已存在 SQLite 文件失败: %v", err)
	}
	defer client2.Close()

	if _, err := client2.FilterRule.Create().
		SetName("rewrite-11128").
		SetKind(domain.FilterKindRewrite).
		SetPattern("11128").
		SetReplacement("11-128").
		Save(ctx); err != nil {
		t.Fatalf("二次打开后写入 FilterRule 失败: %v", err)
	}

	if n, err := client2.SystemSetting.Query().Count(ctx); err != nil || n != 1 {
		t.Errorf("二次打开后 SystemSetting count=%d, err=%v", n, err)
	}
	if n, err := client2.PromptTemplate.Query().Count(ctx); err != nil || n != 1 {
		t.Errorf("二次打开后 PromptTemplate count=%d, err=%v", n, err)
	}
	if n, err := client2.AdminKey.Query().Count(ctx); err != nil || n != 1 {
		t.Errorf("二次打开后 AdminKey count=%d, err=%v", n, err)
	}
	if n, err := client2.ContainerNode.Query().Count(ctx); err != nil || n != 1 {
		t.Errorf("二次打开后 ContainerNode count=%d, err=%v", n, err)
	}
	if n, err := client2.ModelPrice.Query().Count(ctx); err != nil || n != 1 {
		t.Errorf("二次打开后 ModelPrice count=%d, err=%v", n, err)
	}
}
