// Package service 存放连接 HTTP 层与数据层的业务逻辑。
package service

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/apikey"
	"cmd2api/ent/group"
	"cmd2api/internal/domain"
	"cmd2api/internal/pricing"
	"cmd2api/internal/relay"
)

// RecorderOption 配置 Recorder 的可选依赖。
type RecorderOption func(*Recorder)

// WithPricingService 注入数据库自定义单价覆盖服务。
func WithPricingService(ps *PricingService) RecorderOption {
	return func(r *Recorder) {
		r.pricingSvc = ps
	}
}

// Recorder 把 relay 报上来的用量写成 usage_logs，并累加 API Key 的已用额度。
type Recorder struct {
	client *ent.Client
	logger *slog.Logger

	pricingMu  sync.RWMutex
	pricingSvc *PricingService

	// platformCache 缓存 account_id -> platform 映射，避免当上游记录未显式携带 platform 时重复查表。
	platformCache sync.Map
}

// NewRecorder 构造 Recorder（兼容原有双参签名 NewRecorder(client, logger)，同时支持可选注入 WithPricingService）。
func NewRecorder(client *ent.Client, logger *slog.Logger, opts ...RecorderOption) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Recorder{
		client: client,
		logger: logger,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// SetPricingService 动态绑定或替换自定义价格覆盖服务（并发安全）。
func (r *Recorder) SetPricingService(ps *PricingService) {
	if r == nil {
		return
	}
	r.pricingMu.Lock()
	r.pricingSvc = ps
	r.pricingMu.Unlock()
}

// PricingService 返回当前绑定的 *PricingService（未注入时为 nil）。
func (r *Recorder) PricingService() *PricingService {
	if r == nil {
		return nil
	}
	r.pricingMu.RLock()
	defer r.pricingMu.RUnlock()
	return r.pricingSvc
}

// ResolvePrice 解析模型生效单价：若绑定了 *PricingService 则优先走自定义覆盖，否则直接查官方目录。
func (r *Recorder) ResolvePrice(model string) (pricing.PriceSpec, bool) {
	if ps := r.PricingService(); ps != nil {
		return ps.Resolve(model)
	}
	return pricing.LookupOfficial(model)
}

// CostBreakdown 是一次请求的精确费用拆分（均已乘以分组/账号倍率 multiplier）。
type CostBreakdown struct {
	// InputCost 为未命中缓存的纯输入 Token 费用。
	InputCost float64
	// CacheCost 为缓存读取 + 缓存写入（CacheCreation）费用之和。
	CacheCost float64
	// OutputCost 为输出（含思维链输出）Token 费用。
	OutputCost float64
	// TotalCost 为总费用：(纯输入 + 缓存读写 + 输出) * multiplier。
	TotalCost float64
}

type costBreakdown = CostBreakdown

// Record 实现 relay.UsageRecorder。
//
// 刻意不返回错误：用量记录失败不该影响已经成功返回给用户的请求。
// 但会打 warn 日志——记录一直失败说明系统有问题，得能看见。
func (r *Recorder) Record(ctx context.Context, rec relay.UsageRecord) {
	optPlatform, optReasoning := extractOptionalUsageFields(rec)
	r.RecordWithMeta(ctx, rec, optPlatform, optReasoning)
}

// RecordWithMeta 支持显式补充或覆盖 platform 与 reasoningTokens 落库，
// 同时在 relay.UsageRecord 扩展同名字段前后均可无缝联通。
func (r *Recorder) RecordWithMeta(ctx context.Context, rec relay.UsageRecord, platform string, reasoningTokens int) {
	if r == nil || r.client == nil {
		return
	}

	cost, multiplier := calculateCostWithResolver(rec, reasoningTokens, r.ResolvePrice)

	resolvedPlatform := strings.TrimSpace(platform)
	if resolvedPlatform == "" {
		resolvedPlatform = r.resolveAccountPlatform(ctx, rec.AccountID, rec.GroupID)
	} else {
		resolvedPlatform = domain.NormalizePlatform(resolvedPlatform)
	}

	create := r.client.UsageLog.Create().
		SetUserID(rec.UserID).
		SetAPIKeyID(rec.APIKeyID).
		SetAccountID(rec.AccountID).
		SetRequestID(rec.RequestID).
		SetModel(modelOrUnknown(rec.Model)).
		SetInputTokens(clampNonNegative(rec.InputTokens)).
		SetOutputTokens(clampNonNegative(rec.OutputTokens)).
		SetCacheCreationTokens(clampNonNegative(rec.CacheCreationTokens)).
		SetCacheReadTokens(clampNonNegative(rec.CacheReadTokens)).
		SetReasoningTokens(clampNonNegative(reasoningTokens)).
		// 兼容旧统计口径（input_cost 包含输入侧全部开销）并同时写入独立精确的 cache_cost 列。
		SetCacheCost(cost.CacheCost).
		SetInputCost(cost.InputCost + cost.CacheCost).
		SetOutputCost(cost.OutputCost).
		SetTotalCost(cost.TotalCost).
		SetRateMultiplier(multiplier).
		SetStream(rec.Stream).
		SetDurationMs(rec.DurationMs).
		SetStatusCode(rec.StatusCode)

	if resolvedPlatform != "" {
		create = create.SetPlatform(resolvedPlatform)
	}
	if strings.TrimSpace(rec.UpstreamModel) != "" {
		create = create.SetUpstreamModel(strings.TrimSpace(rec.UpstreamModel))
	}
	if rec.GroupID != nil {
		create = create.SetGroupID(*rec.GroupID)
	}
	if rec.FirstTokenMs != nil {
		create = create.SetFirstTokenMs(*rec.FirstTokenMs)
	}
	if rec.ErrorMessage != "" {
		create = create.SetErrorMessage(rec.ErrorMessage)
	}
	if rec.UserAgent != "" {
		create = create.SetUserAgent(rec.UserAgent)
	}
	if rec.IPAddress != "" {
		create = create.SetIPAddress(rec.IPAddress)
	}

	if _, err := create.Save(ctx); err != nil {
		r.logger.Warn("写入用量日志失败", "err", err, "request_id", rec.RequestID)
		return
	}

	if cost.TotalCost <= 0 || strings.HasPrefix(rec.RequestID, "agy-test-") {
		return
	}
	// 用带条件表达式的原子自增，不做「读出来加完写回去」——
	// 高并发下读-改-写会丢更新，额度就会算少。
	if _, err := r.client.APIKey.Update().
		Where(apikey.ID(rec.APIKeyID)).
		AddQuotaUsed(cost.TotalCost).
		SetLastUsedAt(time.Now()).
		Save(ctx); err != nil {
		r.logger.Warn("累加 API Key 已用额度失败", "err", err, "api_key_id", rec.APIKeyID)
	}
}

// CalculateCost 使用当前 Recorder 绑定的价格源计算单次请求费用拆分。
func (r *Recorder) CalculateCost(rec relay.UsageRecord) (CostBreakdown, float64) {
	_, optReasoning := extractOptionalUsageFields(rec)
	if r == nil {
		return calculateCostWithResolver(rec, optReasoning, pricing.LookupOfficial)
	}
	return calculateCostWithResolver(rec, optReasoning, r.ResolvePrice)
}

// calculateCost 使用官方标准价格库计算单次请求费用拆分（供未注入 PricingService 的场景与单测直接调用）。
func calculateCost(rec relay.UsageRecord) (CostBreakdown, float64) {
	_, optReasoning := extractOptionalUsageFields(rec)
	return calculateCostWithResolver(rec, optReasoning, pricing.LookupOfficial)
}

func calculateCostWithResolver(
	rec relay.UsageRecord,
	reasoningTokens int,
	resolve func(model string) (pricing.PriceSpec, bool),
) (CostBreakdown, float64) {
	multiplier := rec.RateMultiplier
	if multiplier <= 0 {
		multiplier = 1
	}

	spec, ok := resolve(rec.Model)
	if !ok {
		// 未知且未配置自定义价的模型只记 token，不编造金额。
		return CostBreakdown{}, multiplier
	}

	const perMillion = 1_000_000.0

	inputTokens := clampNonNegative(rec.InputTokens)
	cacheReadTokens := clampNonNegative(rec.CacheReadTokens)
	cacheWriteTokens := clampNonNegative(rec.CacheCreationTokens)
	outputTokens := clampNonNegative(rec.OutputTokens)
	if outputTokens == 0 && reasoningTokens > 0 {
		outputTokens = reasoningTokens
	}

	// 价目表里未单独声明缓存读/写价时回退到输入单价（免费模型 Input=0 时回退亦为 0）。
	cacheReadPrice := spec.CacheRead
	if cacheReadPrice <= 0 {
		cacheReadPrice = spec.Input
	}
	cacheWritePrice := spec.CacheWrite
	if cacheWritePrice <= 0 {
		cacheWritePrice = spec.Input
	}

	rawInput := float64(inputTokens) / perMillion * spec.Input
	rawCache := (float64(cacheReadTokens)/perMillion)*cacheReadPrice +
		(float64(cacheWriteTokens)/perMillion)*cacheWritePrice
	rawOutput := float64(outputTokens) / perMillion * spec.Output

	return CostBreakdown{
		InputCost:  rawInput * multiplier,
		CacheCost:  rawCache * multiplier,
		OutputCost: rawOutput * multiplier,
		TotalCost:  (rawInput + rawCache + rawOutput) * multiplier,
	}, multiplier
}

func priceFor(model string) (pricing.PriceSpec, bool) {
	return pricing.LookupOfficial(model)
}

// resolveAccountPlatform 从账号或分组记录补齐所属上游平台，并写入进程内轻量缓存。
func (r *Recorder) resolveAccountPlatform(ctx context.Context, accountID int64, groupID *int64) string {
	if r == nil || r.client == nil {
		return ""
	}
	if accountID > 0 {
		if cached, ok := r.platformCache.Load(accountID); ok {
			if s, ok2 := cached.(string); ok2 && s != "" {
				return s
			}
		}
		if p, err := r.client.Account.Query().
			Where(account.IDEQ(accountID)).
			Select(account.FieldPlatform).
			String(ctx); err == nil && strings.TrimSpace(p) != "" {
			norm := domain.NormalizePlatform(p)
			r.platformCache.Store(accountID, norm)
			return norm
		}
	}
	if groupID != nil && *groupID > 0 {
		if p, err := r.client.Group.Query().
			Where(group.IDEQ(*groupID)).
			Select(group.FieldPlatform).
			String(ctx); err == nil && strings.TrimSpace(p) != "" {
			return domain.NormalizePlatform(p)
		}
	}
	return ""
}

// extractOptionalUsageFields 动态提取 relay.UsageRecord 上的可选扩展字段（Platform / ReasoningTokens），
// 使本包无需改动 relay/ 目录即可在 relay-omni 增加字段前后平滑兼容、一键联通。
func extractOptionalUsageFields(rec relay.UsageRecord) (platform string, reasoningTokens int) {
	rv := reflect.ValueOf(rec)
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return "", 0
	}
	if pf := rv.FieldByName("Platform"); pf.IsValid() && pf.Kind() == reflect.String {
		platform = strings.TrimSpace(pf.String())
	}
	if rf := rv.FieldByName("ReasoningTokens"); rf.IsValid() {
		switch rf.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			reasoningTokens = int(rf.Int())
		}
	}
	return platform, reasoningTokens
}

func clampNonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// modelOrUnknown 保证 model 字段非空——表上有 NotEmpty 约束，
// 空值会让整条用量记录写不进去。
func modelOrUnknown(model string) string {
	if strings.TrimSpace(model) == "" {
		return "unknown"
	}
	return strings.TrimSpace(model)
}

// 确保 Recorder 满足 relay 的接口。
var _ relay.UsageRecorder = (*Recorder)(nil)
