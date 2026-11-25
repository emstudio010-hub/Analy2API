package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/filterrule"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/workbuddy"
)

func newServiceTestDB(t *testing.T) *ent.Client {
	t.Helper()
	client, err := db.Open(context.Background(), config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: ":memory:",
	})
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func newServiceTestVault(t *testing.T) *crypto.Vault {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	v, err := crypto.NewVault(key)
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	return v
}

func TestFilterServiceLifecycleAndValidation(t *testing.T) {
	ctx := context.Background()
	dbClient := newServiceTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := filter.NewDefaultEngine()
	fs := NewFilterService(dbClient, eng, logger)

	if err := fs.EnsureSeeded(ctx); err != nil {
		t.Fatalf("EnsureSeeded: %v", err)
	}
	rows, stats, err := fs.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != len(filter.DefaultRules()) || stats.Enabled == 0 || !rows[0].IsBuiltin {
		t.Fatalf("unexpected seeded rules: count=%d stats=%+v", len(rows), stats)
	}

	// Idempotent second EnsureSeeded should not duplicate
	if err := fs.EnsureSeeded(ctx); err != nil {
		t.Fatalf("second EnsureSeeded: %v", err)
	}
	rows2, _, _ := fs.List(ctx)
	if len(rows2) != len(rows) {
		t.Fatalf("expected idempotent EnsureSeeded, got %d vs %d", len(rows2), len(rows))
	}

	// Reject zero-width char & invalid regex
	if _, err := fs.Create(ctx, CreateFilterRuleInput{
		Name:        "bad-zwsp",
		Kind:        filter.KindReplace,
		Pattern:     "Claude",
		Replacement: "C\u200blaude",
	}); !errors.Is(err, ErrInvalidFilterRule) {
		t.Fatalf("expected ErrInvalidFilterRule for zwsp, got %v", err)
	}
	if _, err := fs.Create(ctx, CreateFilterRuleInput{
		Name:    "bad-regex",
		Kind:    filter.KindPrune,
		Pattern: "[unclosed",
		IsRegex: true,
	}); !errors.Is(err, ErrInvalidFilterRule) {
		t.Fatalf("expected ErrInvalidFilterRule for invalid regex, got %v", err)
	}

	// Create custom rule & verify live hot-reload in engine
	created, err := fs.Create(ctx, CreateFilterRuleInput{
		Name:        "custom-brand",
		Kind:        filter.KindReplace,
		Pattern:     "SecretBrandX",
		Replacement: "SafeTool",
		Scope:       filter.ScopeSystem,
	})
	if err != nil {
		t.Fatalf("Create custom rule: %v", err)
	}
	preview, err := fs.Preview("You use SecretBrandX.", "system", "openai")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.Changed || !strings.Contains(preview.Output, "SafeTool") || len(preview.Hits) == 0 {
		t.Fatalf("expected live engine to match custom rule, got %+v", preview)
	}

	// Partial update (toggle enabled=false only) must NOT wipe existing fields
	disabled := false
	updated, err := fs.Update(ctx, created.ID, UpdateFilterRuleInput{
		Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("Update toggle: %v", err)
	}
	if updated.Enabled || updated.Pattern != "SecretBrandX" || updated.Replacement != "SafeTool" {
		t.Fatalf("partial update corrupted rule: %+v", updated)
	}

	// Delete & ResetDefaults
	if err := fs.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	resetRows, err := fs.ResetDefaults(ctx)
	if err != nil {
		t.Fatalf("ResetDefaults: %v", err)
	}
	if len(resetRows) != len(filter.DefaultRules()) {
		t.Fatalf("expected %d rows after ResetDefaults, got %d", len(filter.DefaultRules()), len(resetRows))
	}

	// Simulate an existing DB from an older build missing newly added built-in rules,
	// plus an outdated built-in rule (old "WorkBuddy" replacement) and a custom user rule
	// that must remain intact during EnsureSeeded sync.
	if _, err := fs.Create(ctx, CreateFilterRuleInput{
		Name:        "user-keep-rule",
		Kind:        filter.KindReplace,
		Pattern:     "KeepMeBrand",
		Replacement: "SafeKeep",
		Scope:       filter.ScopeSystem,
	}); err != nil {
		t.Fatalf("Create user-keep-rule: %v", err)
	}
	if _, err := dbClient.FilterRule.Update().
		Where(filterrule.NameEQ("replace-brand-claude-code")).
		SetReplacement("WorkBuddy").
		SetEnabled(false).
		Save(ctx); err != nil {
		t.Fatalf("downgrade simulated old built-in rule: %v", err)
	}
	deletedMissing, err := dbClient.FilterRule.Delete().
		Where(filterrule.NameIn("replace-brand-chatgpt", "replace-brand-gemini-cloud", "replace-brand-deepseek")).
		Exec(ctx)
	if err != nil {
		t.Fatalf("delete simulated missing rules: %v", err)
	}
	if deletedMissing != 3 {
		t.Fatalf("expected to delete 3 simulated missing rules, deleted %d", deletedMissing)
	}
	if err := fs.EnsureSeeded(ctx); err != nil {
		t.Fatalf("backfill EnsureSeeded: %v", err)
	}
	afterBackfill, _, err := fs.List(ctx)
	if err != nil {
		t.Fatalf("List after backfill: %v", err)
	}
	if len(afterBackfill) != len(filter.DefaultRules())+1 {
		t.Fatalf("expected %d rules after backfill (defaults + custom), got %d", len(filter.DefaultRules())+1, len(afterBackfill))
	}
	upgradedRule, err := dbClient.FilterRule.Query().
		Where(filterrule.NameEQ("replace-brand-claude-code")).
		Only(ctx)
	if err != nil {
		t.Fatalf("query upgraded built-in rule: %v", err)
	}
	if upgradedRule.Replacement != "Ess-Agentic" || upgradedRule.Enabled {
		t.Fatalf("expected upgraded built-in replacement=Ess-Agentic while preserving enabled=false, got replacement=%q enabled=%v", upgradedRule.Replacement, upgradedRule.Enabled)
	}
	backfillPreview, err := fs.Preview("ChatGPT uses DeepSeek and Gemini Cloud.", "system", "openai")
	if err != nil {
		t.Fatalf("Preview after backfill: %v", err)
	}
	if backfillPreview.Output != "Ess-Agentic uses Ess-Agentic and Ess-Agentic Cloud." {
		t.Fatalf("unexpected backfill preview output: %q", backfillPreview.Output)
	}
}

func TestAccountServiceWorkBuddyCRUDImportAndActions(t *testing.T) {
	ctx := context.Background()
	dbClient := newServiceTestDB(t)
	vault := newServiceTestVault(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var growthAcceptCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case workbuddy.PathDeviceAuthCode:
			_, _ = io.WriteString(w, `{"code":0,"data":{"deviceCode":"dev_code_99","expiresIn":300}}`)
		case workbuddy.PathTokenRefresh:
			_, _ = io.WriteString(w, `{"code":0,"data":{"accessToken":"wb_tok_new","refreshToken":"wb_rt_new","expiresIn":7200}}`)
		case workbuddy.PathGrowthTasks:
			_, _ = io.WriteString(w, `{"code":0,"data":{"tasks":[{"code":"daily_check","status":"pending","credit":10}]}}`)
		case workbuddy.PathGrowthTasksAccept:
			growthAcceptCount.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
		case workbuddy.PathDailyCheckin:
			_, _ = io.WriteString(w, `{"code":0,"data":{"credit":15,"streak":3,"date":"2026-11-15"}}`)
		case workbuddy.PathBillingSummary:
			_, _ = io.WriteString(w, `{"code":0,"data":{"TotalCredits":120.5,"SubscriptionPackageName":"Pro","Packages":[{"PackageCode":"PKG1","PackageName":"Monthly","Kind":"plan_base","Remain":120.5,"Total":200}]}}`)
		default:
			_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
		}
	}))
	defer upstream.Close()

	wbClient := workbuddy.NewClient("", upstream.URL, logger)
	relayClient := relay.NewClient(config.CommandCodeConfig{}, logger)
	relayClient.SetWorkBuddyClient(wbClient)
	sched := scheduler.New(dbClient, vault, 3, logger)
	svc := NewAccountService(dbClient, vault, relayClient, sched, logger)

	// Default group bootstrap
	defGrp, err := BootstrapDefaultGroup(ctx, dbClient, logger)
	if err != nil {
		t.Fatalf("BootstrapDefaultGroup: %v", err)
	}
	if defGrp.Platform != domain.PlatformWorkBuddy {
		t.Fatalf("expected default group platform workbuddy, got %q", defGrp.Platform)
	}

	// Reject invalid proxy URL
	if _, err := svc.Create(ctx, CreateAccountInput{
		Name:     "bad-proxy",
		APIKey:   "wb_access_1",
		ProxyURL: "ftp://badproxy:21",
	}); err == nil {
		t.Fatalf("expected invalid proxy url error")
	}

	// Create WorkBuddy account without user_ prefix & blank platform -> auto pairs device
	acc, err := svc.Create(ctx, CreateAccountInput{
		Name:         "wb-one",
		APIKey:       "arbitrary_jwt_token_123",
		RefreshToken: "rt_123",
		UID:          "uid-1001",
		Email:        "user@example.com",
		Domain:       upstream.URL,
		GroupIDs:     []int64{defGrp.ID},
	})
	if err != nil {
		t.Fatalf("Create WorkBuddy account failed: %v", err)
	}
	if acc.Platform != domain.PlatformWorkBuddy || !workbuddy.IsValidQimei36(acc.Qimei36) || !workbuddy.IsValidMachineID(acc.MachineID) {
		t.Fatalf("unexpected created WorkBuddy account metadata: %+v", acc)
	}

	// Update with blank api_key preserves original key
	emptyKey := ""
	newName := "wb-one-renamed"
	updated, err := svc.Update(ctx, acc.ID, UpdateAccountInput{
		Name:   &newName,
		APIKey: &emptyKey,
	})
	if err != nil {
		t.Fatalf("Update with blank api_key failed: %v", err)
	}
	if updated.Name != "wb-one-renamed" {
		t.Fatalf("expected renamed account, got %q", updated.Name)
	}
	if keyPlain, ok := svc.decryptKey(updated); !ok || keyPlain != "arbitrary_jwt_token_123" {
		t.Fatalf("expected original key preserved when updating with blank api_key, got ok=%v key=%q", ok, keyPlain)
	}

	// IssueLoginLink
	linkRes, err := svc.IssueLoginLink(ctx, acc.ID, "/usage")
	if err != nil {
		t.Fatalf("IssueLoginLink failed: %v", err)
	}
	if linkRes.DeviceCode != "dev_code_99" || !strings.Contains(linkRes.LoginURL, "dev_code_99") {
		t.Fatalf("unexpected login link result: %+v", linkRes)
	}

	// RefreshToken persists encrypted new tokens
	refOut, err := svc.RefreshToken(ctx, acc.ID)
	if err != nil || !refOut.OK || refOut.Account == nil {
		t.Fatalf("RefreshToken failed: out=%+v err=%v", refOut, err)
	}
	if k, _ := svc.decryptKey(refOut.Account); k != "wb_tok_new" {
		t.Fatalf("expected refreshed access token stored, got %q", k)
	}
	if rt := svc.DecryptRefreshToken(refOut.Account); rt != "wb_rt_new" {
		t.Fatalf("expected refreshed refresh token stored, got %q", rt)
	}

	// RotateDevice generates fresh pair
	oldQ := refOut.Account.Qimei36
	rot, err := svc.RotateDevice(ctx, acc.ID)
	if err != nil || !rot.OK || rot.Qimei36 == oldQ {
		t.Fatalf("RotateDevice failed: rot=%+v err=%v", rot, err)
	}

	// HarvestAccount updates checkin state + balance credits & packages
	hOut, err := svc.HarvestAccount(ctx, acc.ID, false)
	if err != nil || hOut == nil || !hOut.OK {
		t.Fatalf("HarvestAccount failed: out=%+v err=%v", hOut, err)
	}
	if growthAcceptCount.Load() == 0 {
		t.Fatalf("expected harvest to hit growth endpoints")
	}
	if hOut.Account.LastCheckinDate == "" || hOut.Account.CheckinStreak < 3 {
		t.Fatalf("expected checkin metadata persisted, got date=%q streak=%d", hOut.Account.LastCheckinDate, hOut.Account.CheckinStreak)
	}
	if hOut.Account.BalanceTotalCredits == nil || *hOut.Account.BalanceTotalCredits != 120.5 || len(hOut.Account.BalancePackages) != 1 {
		t.Fatalf("expected balance_total_credits=120.5 and 1 package, got credits=%v pkgs=%d", hOut.Account.BalanceTotalCredits, len(hOut.Account.BalancePackages))
	}

	// BatchImport with upsert updates matching uid & creates new entry
	importJSON := `[
		{"name":"wb-one-upserted","uid":"uid-1001","access_token":"wb_tok_upserted","refresh_token":"rt_upserted"},
		{"name":"wb-two","uid":"uid-2002","access_token":"wb_tok_two"}
	]`
	batchRes, err := svc.BatchImport(ctx, BatchImportInput{
		Keys:     importJSON,
		Domain:   upstream.URL,
		GroupIDs: []int64{defGrp.ID},
		Upsert:   true,
	})
	if err != nil {
		t.Fatalf("BatchImport failed: %v", err)
	}
	if batchRes.Updated != 1 || batchRes.Created != 1 || batchRes.Failed != 0 {
		t.Fatalf("unexpected batch import stats: %+v", batchRes)
	}

	// ExportAccounts roundtrip
	exp, err := svc.ExportAccounts(ctx)
	if err != nil || exp.Count != 2 {
		t.Fatalf("ExportAccounts failed: exp=%+v err=%v", exp, err)
	}
	rawExport, _ := json.Marshal(exp)
	if !strings.Contains(string(rawExport), "wb_tok_upserted") || !strings.Contains(string(rawExport), "wb_tok_two") {
		t.Fatalf("export missing decrypted tokens: %s", rawExport)
	}

	// Test direct RunAutoHarvest context cancel safety
	ctxAuto, cancelAuto := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() {
		svc.RunAutoHarvest(ctxAuto, 50*time.Millisecond, false)
		close(doneCh)
	}()
	cancelAuto()
	<-doneCh
}
