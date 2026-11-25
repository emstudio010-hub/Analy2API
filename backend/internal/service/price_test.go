package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"

	"cmd2api/ent/usagelog"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/domain"
	"cmd2api/internal/pricing"
	"cmd2api/internal/relay"
	"cmd2api/internal/workbuddy"
)

func almostEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// 验证四大平台（WorkBuddy / AntiGravity / Command Code / OpenCode）全部模型目录 100% 命中，0 漏网。
func TestAllFourPlatformsModelCatalogsZeroMiss(t *testing.T) {
	// 1. WorkBuddy DefaultModels() 100% 命中且单价 > 0（含曾经踩坑的 claude-sonnet-4.5 / claude-opus-4.5 / claude-3.7-sonnet）
	for _, m := range workbuddy.DefaultModels() {
		spec, ok := priceFor(m.ID)
		if !ok {
			t.Errorf("WorkBuddy DefaultModels 条目 %q 查不到官方单价", m.ID)
			continue
		}
		if spec.Input <= 0 || spec.Output <= 0 {
			t.Errorf("WorkBuddy 付费模型 %q 单价应大于 0，得到 %+v", m.ID, spec)
		}
	}

	// 2. AntiGravity DefaultModels() 100% 命中且单价 > 0
	for _, m := range antigravity.DefaultModels() {
		spec, ok := priceFor(m.ID)
		if !ok {
			t.Errorf("AntiGravity DefaultModels 条目 %q 查不到官方单价", m.ID)
			continue
		}
		if spec.Input <= 0 || spec.Output <= 0 {
			t.Errorf("AntiGravity 旗舰模型 %q 单价应大于 0，得到 %+v", m.ID, spec)
		}
	}

	// 3. Command Code 全量内置目录模型（同步自 relay/client.go hardcodedModels，含 vendor/ 前缀、日期后缀与免费模型）
	commandCodeModels := []string{
		"claude-sonnet-5",
		"claude-sonnet-4-6",
		"claude-fable-5-1",
		"claude-fable-5",
		"claude-opus-5-5",
		"claude-opus-5",
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-haiku-4-5-20251001",
		"gpt-6-astra",
		"gpt-6-sol",
		"gpt-6-luna",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.4",
		"gpt-5.3-codex",
		"gpt-5.4-mini",
		"deepseek/deepseek-v4-pro",
		"deepseek/deepseek-v4-flash",
		"deepseek/deepseek-v4-flash-vision-exp",
		"deepseek/deepseek-v4-flash-fast",
		"deepseek/deepseek-v4.1-flash",
		"moonshotai/Kimi-K3",
		"moonshotai/Kimi-K2.7-Code",
		"moonshotai/Kimi-K2.7-Code-Highspeed",
		"moonshotai/Kimi-K2.6",
		"moonshotai/Kimi-K2.5",
		"z-ai/glm-5.3-flash",
		"z-ai/glm-5.3-flashx",
		"zai-org/GLM-5.3",
		"zai-org/GLM-5.2",
		"zai-org/GLM-5.2-Fast",
		"zai-org/GLM-5.1",
		"zai-org/GLM-5",
		"MiniMaxAI/MiniMax-M3",
		"MiniMaxAI/MiniMax-M2.7",
		"MiniMaxAI/MiniMax-M2.5",
		"xiaomi/mimo-v2.6-pro",
		"xiaomi/mimo-v2.6-pro-ultraspeed",
		"xiaomi/mimo-v2.6-flash",
		"xiaomi/mimo-v2.5-pro",
		"xiaomi/mimo-v2.5",
		"Qwen/Qwen3.8-Omni-Flash",
		"Qwen/Qwen3.8-Max-0902",
		"Qwen/Qwen3.8-Max",
		"Qwen/Qwen3.8-27B",
		"Qwen/Qwen3.8-Flash",
		"Qwen/Qwen3.7-Max",
		"Qwen/Qwen3.7-Plus",
		"Qwen/Qwen3.7-Flash",
		"Qwen/Qwen3.6-Max-Preview",
		"Qwen/Qwen3.6-Plus",
		"meituan/LongCat-2.0",
		"stepfun/Step-5-Preview",
		"stepfun/Step-3.7-Flash",
		"stepfun/Step-3.5-Flash",
		"tencent/hy3-paid",
		"tencent/hy4-preview",
		"google/gemini-3.8-flash",
		"google/gemini-3.7-flash",
		"google/gemini-3.6-flash",
		"google/gemini-3.5-flash",
		"google/gemini-3.5-flash-lite",
		"google/gemini-3.1-flash-lite",
		"sakana/fugu-ultra",
		"nvidia/nemotron-3-ultra-550b-a55b",
		"thinkingmachines/inkling",
		"thinkingmachines/inkling-small",
		"stealth/space-bunny-alpha",
		"stealth/pixel-canary",
		"poolside/laguna-s-2.1-free",
		"inclusionai/ling-3.0-flash-sante:free",
		"meta/muse-spark-1.1",
		"meta/muse-spark-1.2",
		"meta/muse-spark-1.2-contributor",
		"meta/muse-spark-1.3",
		"meta/muse-spark-1.3-contributor",
		"xai/grok-4.5",
		"xai/grok-4.6",
		"xai/grok-4.7",
	}
	for _, id := range commandCodeModels {
		spec, ok := priceFor(id)
		if !ok {
			t.Errorf("Command Code 模型 %q 查不到官方单价", id)
			continue
		}
		if pricing.IsFreeModel(id) {
			if spec.Input != 0 || spec.Output != 0 {
				t.Errorf("Command Code 免费模型 %q 应为 $0，得到 %+v", id, spec)
			}
		} else if spec.Input <= 0 || spec.Output <= 0 {
			t.Errorf("Command Code 付费模型 %q 单价应大于 0，得到 %+v", id, spec)
		}
	}

	// 4. OpenCode Zen & Go 全系列与常见线上变体
	openCodeModels := []string{
		"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7",
		"claude-opus-4-6", "claude-opus-4-5", "claude-sonnet-5", "claude-sonnet-4-6",
		"claude-sonnet-4-5", "claude-haiku-4-5", "claude-fable-5", "claude-fable-5-1",
		"gpt-6-luna", "gpt-6-sol", "gpt-6-astra", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra",
		"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.1-codex", "gpt-5",
		"gemini-3.8-flash", "gemini-3.6-flash", "gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-3.1-pro",
		"grok-4.7", "grok-4.6", "grok-4.5", "grok-4", "grok-3",
		"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4.1-flash", "deepseek-v4.1-flash-fast",
		"kimi-k3", "kimi-k2.7-code", "kimi-k2.6", "kimi-k2.5", "kimi-k2-thinking",
		"glm-5.3", "glm-5.3-flash", "glm-5.3-flashx", "glm-5.2", "glm-5.1", "glm-5",
		"minimax-m3", "minimax-m2.7", "minimax-m2.5",
		"qwen3.8-max", "qwen3.8-flash", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-plus",
		"mimo-v2.6-pro", "mimo-v2.6-flash", "mimo-v2.5-pro", "mimo-v2.5",
	}
	for _, id := range openCodeModels {
		if _, ok := priceFor(id); !ok {
			t.Errorf("OpenCode 模型 %q 查不到官方单价", id)
		}
	}

	// 5. -free 免费后缀命中 SourceOfficial ($0)，完全未知模型返回 false
	for _, freeID := range []string{"deepseek-v4-flash-free", "space-bunny-free"} {
		spec, ok := priceFor(freeID)
		if !ok || spec.Source != pricing.SourceOfficial || spec.Input != 0 || spec.Output != 0 {
			t.Errorf("免费模型 %q 期望 ok=true 且 $0 official，得到 ok=%v spec=%+v", freeID, ok, spec)
		}
	}
	if _, ok := priceFor("some-future-model"); ok {
		t.Error("完全未知模型 some-future-model 不应命中价格表")
	}
}

// 验证纯输入、缓存读、缓存写、倍率与缺失缓存价回退逻辑。
func TestCalculateCostBreakdownCacheReadWriteAndFallback(t *testing.T) {
	// 1. z-ai/glm-5.3-flashx：输入 $0.37/M，输出 $1.25/M，缓存读 $0.075/M
	fullInput, _ := calculateCost(relay.UsageRecord{
		Model:          "z-ai/glm-5.3-flashx",
		InputTokens:    1_000_000,
		RateMultiplier: 1,
	})
	if !almostEq(fullInput.InputCost, 0.37) || !almostEq(fullInput.CacheCost, 0) || !almostEq(fullInput.TotalCost, 0.37) {
		t.Fatalf("纯输入计费不对: %+v", fullInput)
	}

	cachedInput, _ := calculateCost(relay.UsageRecord{
		Model:           "z-ai/glm-5.3-flashx",
		InputTokens:     0,
		CacheReadTokens: 1_000_000,
		RateMultiplier:  1,
	})
	if !almostEq(cachedInput.CacheCost, 0.075) || !almostEq(cachedInput.InputCost, 0) || !almostEq(cachedInput.TotalCost, 0.075) {
		t.Fatalf("100 万缓存读计费应为 CacheCost=$0.075 InputCost=$0，得到 %+v", cachedInput)
	}
	if cachedInput.TotalCost >= fullInput.TotalCost {
		t.Errorf("缓存读没有折扣：全输入 %v，全缓存 %v", fullInput.TotalCost, cachedInput.TotalCost)
	}

	// 2. Anthropic claude-sonnet-4.5（点号写法）：输入 $3，输出 $15，缓存读 $0.30，缓存写 $3.75，倍率 1.5
	anthCost, mult := calculateCost(relay.UsageRecord{
		Model:               "claude-sonnet-4.5",
		InputTokens:         1_000_000,
		CacheReadTokens:     1_000_000,
		CacheCreationTokens: 1_000_000,
		OutputTokens:        1_000_000,
		RateMultiplier:      1.5,
	})
	if !almostEq(mult, 1.5) {
		t.Fatalf("multiplier = %v, want 1.5", mult)
	}
	// base: input=3.00, cache=0.30+3.75=4.05, output=15.00, total=22.05; *1.5 => 4.50, 6.075, 22.50, 33.075
	if !almostEq(anthCost.InputCost, 4.50) ||
		!almostEq(anthCost.CacheCost, 6.075) ||
		!almostEq(anthCost.OutputCost, 22.50) ||
		!almostEq(anthCost.TotalCost, 33.075) {
		t.Fatalf("Anthropic 混合缓存读写+倍率计费不对: %+v", anthCost)
	}

	// 3. 缓存读写单价为 0 时必须回退到输入单价（避免付费模型未填缓存价时被免费白送）
	fallback, _ := calculateCostWithResolver(relay.UsageRecord{
		Model:               "custom-no-cache-tier",
		CacheReadTokens:     1_000_000,
		CacheCreationTokens: 500_000,
		RateMultiplier:      1,
	}, 0, func(string) (pricing.PriceSpec, bool) {
		return pricing.PriceSpec{Input: 10.0, Output: 30.0, CacheRead: 0, CacheWrite: 0, Source: pricing.SourceCustom}, true
	})
	if !almostEq(fallback.CacheCost, 15.0) || !almostEq(fallback.TotalCost, 15.0) {
		t.Errorf("缺缓存单价时应回退按输入价 $10/M 计（1.5M = $15），得到 %+v", fallback)
	}

	// 4. 免费模型即使包含大量缓存与输出 Token 也恒为 $0
	freeCost, _ := calculateCost(relay.UsageRecord{
		Model:           "deepseek-v4-flash-free",
		InputTokens:     2_000_000,
		CacheReadTokens: 5_000_000,
		OutputTokens:    1_000_000,
		RateMultiplier:  2,
	})
	if freeCost.TotalCost != 0 || freeCost.InputCost != 0 || freeCost.CacheCost != 0 || freeCost.OutputCost != 0 {
		t.Errorf("免费模型计费应全为 0，得到 %+v", freeCost)
	}
}

// 验证 PricingService 数据库自定义改价覆盖、热加载、归一化匹配及删除回退。
func TestPricingServiceDBOverridesHotReloadAndFallback(t *testing.T) {
	ctx := context.Background()
	dbClient := newServiceTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ps := NewPricingService(dbClient, logger)
	if err := ps.LoadFromDB(ctx); err != nil {
		t.Fatalf("initial LoadFromDB: %v", err)
	}

	// 默认命中官方牌价 ($3 / $15)
	orig, ok := ps.Resolve("claude-sonnet-4.5")
	if !ok || orig.Source != pricing.SourceOfficial || !almostEq(orig.Input, 3.0) {
		t.Fatalf("expected official $3 price initially, got ok=%v spec=%+v", ok, orig)
	}

	// 参数校验：拒绝空模型名与负数价格
	if _, err := ps.UpsertOverride(ctx, UpsertModelPriceInput{Model: "", InputPrice: 1, OutputPrice: 2}); !errors.Is(err, ErrInvalidModelPrice) {
		t.Fatalf("expected ErrInvalidModelPrice for empty model, got %v", err)
	}
	if _, err := ps.UpsertOverride(ctx, UpsertModelPriceInput{Model: "gpt-5", InputPrice: -0.1, OutputPrice: 2}); !errors.Is(err, ErrInvalidModelPrice) {
		t.Fatalf("expected ErrInvalidModelPrice for negative price, got %v", err)
	}

	// 新增自定义覆盖（用点号写法写入）
	row1, err := ps.UpsertOverride(ctx, UpsertModelPriceInput{
		Model:           "claude-sonnet-4.5",
		InputPrice:      1.80,
		OutputPrice:     9.00,
		CacheReadPrice:  0.18,
		CacheWritePrice: 2.25,
		Notes:           "企业内部折扣价",
	})
	if err != nil {
		t.Fatalf("UpsertOverride create: %v", err)
	}

	// 精确名、横线变体、带 vendor 前缀与日期后缀均应命中自定义覆盖
	for _, variant := range []string{
		"claude-sonnet-4.5",
		"claude-sonnet-4-5",
		"anthropic/claude-sonnet-4-5-20250929",
		"anthropic/claude-sonnet-4.5:thinking",
	} {
		got, found := ps.Resolve(variant)
		if !found || got.Source != pricing.SourceCustom || !almostEq(got.Input, 1.80) || !almostEq(got.Output, 9.00) {
			t.Errorf("Resolve(%q) expected custom 1.80/9.00, got found=%v spec=%+v", variant, found, got)
		}
	}

	// 同模型名不传 ID 二次 Upsert 应原地更新而非报错重复
	row1Updated, err := ps.UpsertOverride(ctx, UpsertModelPriceInput{
		Model:           "CLAUDE-SONNET-4.5",
		InputPrice:      2.10,
		OutputPrice:     10.50,
		CacheReadPrice:  0.21,
		CacheWritePrice: 2.60,
		Notes:           "二次调价",
	})
	if err != nil {
		t.Fatalf("UpsertOverride update by model: %v", err)
	}
	if row1Updated.ID != row1.ID {
		t.Errorf("expected in-place upsert on ID %d, got ID %d", row1.ID, row1Updated.ID)
	}

	// 为未知私有模型配置单价
	rowCustomOnly, err := ps.UpsertOverride(ctx, UpsertModelPriceInput{
		Model:       "internal-coder-v1",
		InputPrice:  0.50,
		OutputPrice: 1.50,
	})
	if err != nil {
		t.Fatalf("UpsertOverride internal-coder-v1: %v", err)
	}

	list, err := ps.ListOverrides(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListOverrides expected 2 rows, got len=%d err=%v", len(list), err)
	}

	// 新建第二个实例验证启动期 LoadFromDB 热加载
	psReloaded := NewPricingService(dbClient, logger)
	if err := psReloaded.LoadFromDB(ctx); err != nil {
		t.Fatalf("psReloaded.LoadFromDB: %v", err)
	}
	if spec, found := psReloaded.Resolve("internal-coder-v1"); !found || spec.Source != pricing.SourceCustom || !almostEq(spec.Input, 0.50) {
		t.Fatalf("reloaded service failed to resolve internal-coder-v1: found=%v spec=%+v", found, spec)
	}

	// 删除覆盖后立即回退至官方标准价（或 unpriced）
	if err := ps.DeleteOverride(ctx, row1.ID); err != nil {
		t.Fatalf("DeleteOverride row1: %v", err)
	}
	reverted, found := ps.Resolve("claude-sonnet-4-5")
	if !found || reverted.Source != pricing.SourceOfficial || !almostEq(reverted.Input, 3.00) {
		t.Fatalf("after DeleteOverride expected fallback to official $3.00, got found=%v spec=%+v", found, reverted)
	}

	if err := ps.DeleteOverride(ctx, rowCustomOnly.ID); err != nil {
		t.Fatalf("DeleteOverride rowCustomOnly: %v", err)
	}
	if spec, found := ps.Resolve("internal-coder-v1"); found || spec.Source != pricing.SourceUnpriced {
		t.Fatalf("after deleting custom-only model expected unpriced, got found=%v spec=%+v", found, spec)
	}

	if err := ps.DeleteOverride(ctx, 999999); !errors.Is(err, ErrModelPriceNotFound) {
		t.Errorf("expected ErrModelPriceNotFound for missing id, got %v", err)
	}
}

// 验证 Recorder 绑定 PricingService 后落库精确写入 cache_cost、兼容 input_cost、自动补齐 platform 与 reasoning_tokens，并累加 APIKey 额度。
func TestRecorderPersistsCacheCostPlatformReasoningAndUpdatesQuota(t *testing.T) {
	ctx := context.Background()
	dbClient := newServiceTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ps := NewPricingService(dbClient, logger)
	recSvc := NewRecorder(dbClient, logger, WithPricingService(ps))

	user, err := dbClient.User.Create().
		SetEmail("recorder-test@cmd2api.local").
		SetPasswordHash("hash").
		SetRole(domain.RoleAdmin).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	grp, err := dbClient.Group.Create().
		SetName("agy-group").
		SetPlatform(domain.PlatformAntigravity).
		Save(ctx)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	acc, err := dbClient.Account.Create().
		SetName("agy-node-acc").
		SetPlatform(domain.PlatformAntigravity).
		SetType(domain.AccountTypeOAuthJSON).
		Save(ctx)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	keyRow, err := dbClient.APIKey.Create().
		SetUserID(user.ID).
		SetGroupID(grp.ID).
		SetKey(domain.APIKeyPrefix + "recorder_test_key").
		SetName("recorder-key").
		SetQuota(100).
		Save(ctx)
	if err != nil {
		t.Fatalf("create apikey: %v", err)
	}

	// 记录一笔 claude-opus-4.6 请求（官方价：Input $5, Output $25, CacheRead $0.50, CacheWrite $6.25）
	grpID := grp.ID
	recSvc.RecordWithMeta(ctx, relay.UsageRecord{
		UserID:              user.ID,
		APIKeyID:            keyRow.ID,
		AccountID:           acc.ID,
		GroupID:             &grpID,
		RequestID:           "req-opus-cache-1",
		Model:               "claude-opus-4-6",
		UpstreamModel:       "anthropic/claude-opus-4-6-thinking",
		InputTokens:         1_000_000, // $5.00
		CacheReadTokens:     1_000_000, // $0.50
		CacheCreationTokens: 1_000_000, // $6.25
		OutputTokens:        1_000_000, // $25.00
		RateMultiplier:      1.0,
		Stream:              true,
		DurationMs:          1850,
		StatusCode:          200,
	}, "", 250_000)

	savedLog, err := dbClient.UsageLog.Query().
		Where(usagelog.RequestIDEQ("req-opus-cache-1")).
		Only(ctx)
	if err != nil {
		t.Fatalf("query saved UsageLog: %v", err)
	}

	// CacheCost = 0.50 + 6.25 = 6.75
	// InputCost (DB 兼容口径) = 纯输入 5.00 + 缓存 6.75 = 11.75
	// OutputCost = 25.00
	// TotalCost = 36.75
	if !almostEq(savedLog.CacheCost, 6.75) {
		t.Errorf("savedLog.CacheCost = %v, want 6.75", savedLog.CacheCost)
	}
	if !almostEq(savedLog.InputCost, 11.75) {
		t.Errorf("savedLog.InputCost = %v, want 11.75 (5.00 + 6.75)", savedLog.InputCost)
	}
	if !almostEq(savedLog.OutputCost, 25.00) {
		t.Errorf("savedLog.OutputCost = %v, want 25.00", savedLog.OutputCost)
	}
	if !almostEq(savedLog.TotalCost, 36.75) {
		t.Errorf("savedLog.TotalCost = %v, want 36.75", savedLog.TotalCost)
	}
	if savedLog.Platform != domain.PlatformAntigravity {
		t.Errorf("savedLog.Platform = %q, want %q (auto-resolved from Account)", savedLog.Platform, domain.PlatformAntigravity)
	}
	if savedLog.ReasoningTokens != 250_000 {
		t.Errorf("savedLog.ReasoningTokens = %d, want 250000", savedLog.ReasoningTokens)
	}
	if savedLog.UpstreamModel == nil || *savedLog.UpstreamModel != "anthropic/claude-opus-4-6-thinking" {
		t.Errorf("savedLog.UpstreamModel = %v, want anthropic/claude-opus-4-6-thinking", savedLog.UpstreamModel)
	}

	updatedKey, err := dbClient.APIKey.Get(ctx, keyRow.ID)
	if err != nil {
		t.Fatalf("get updated apikey: %v", err)
	}
	if !almostEq(updatedKey.QuotaUsed, 36.75) {
		t.Errorf("updatedKey.QuotaUsed = %v, want 36.75", updatedKey.QuotaUsed)
	}

	// 再测试自定义改价覆盖实时生效到 Recorder.Record
	if _, err := ps.UpsertOverride(ctx, UpsertModelPriceInput{
		Model:          "claude-opus-4.6",
		InputPrice:     2.00,
		OutputPrice:    10.00,
		CacheReadPrice: 0.20,
	}); err != nil {
		t.Fatalf("UpsertOverride claude-opus-4.6: %v", err)
	}

	recSvc.Record(ctx, relay.UsageRecord{
		UserID:          user.ID,
		APIKeyID:        keyRow.ID,
		AccountID:       acc.ID,
		GroupID:         &grpID,
		RequestID:       "req-opus-custom-2",
		Model:           "claude-opus-4-6",
		InputTokens:     1_000_000, // $2.00
		CacheReadTokens: 1_000_000, // $0.20
		OutputTokens:    1_000_000, // $10.00
		RateMultiplier:  1.0,
		StatusCode:      200,
	})

	savedLog2, err := dbClient.UsageLog.Query().
		Where(usagelog.RequestIDEQ("req-opus-custom-2")).
		Only(ctx)
	if err != nil {
		t.Fatalf("query savedLog2: %v", err)
	}
	if !almostEq(savedLog2.CacheCost, 0.20) || !almostEq(savedLog2.InputCost, 2.20) || !almostEq(savedLog2.TotalCost, 12.20) {
		t.Errorf("custom override cost mismatch in UsageLog: %+v", savedLog2)
	}
}

// TestKeyPrefixRuleIsPlatformScoped 保留平台前缀校验规则回归测试。
func TestKeyPrefixRuleIsPlatformScoped(t *testing.T) {
	if domain.PlatformCommandCode != "commandcode" {
		t.Fatal("平台常量变了，这个测试的前提失效")
	}
	if domain.PlatformOpenCode == domain.PlatformCommandCode {
		t.Fatal("两个平台常量不该相等")
	}
	if domain.CommandCodeKeyPrefix != "user_" {
		t.Errorf("Command Code 前缀应为 user_，得到 %q", domain.CommandCodeKeyPrefix)
	}
}
