// Package pricing 提供全平台权威标准模型价格目录与智能模型 ID 归一化匹配引擎。
//
// 本包仅依赖 Go 标准库，保持与数据库/HTTP 层零耦合，所有单价单位均为：美元 / 百万 Token (USD / 1M Tokens)。
package pricing

import (
	"regexp"
	"strings"
)

const (
	// SourceOfficial 表示命中内置全平台权威官方牌价（含 -free / :free 免费模型）。
	SourceOfficial = "official"
	// SourceCustom 表示命中数据库自定义改价覆盖（model_prices 表）。
	SourceCustom = "custom"
	// SourceUnpriced 表示未知且未配置自定义单价的模型（计费按 0 处理，不编造价格）。
	SourceUnpriced = "unpriced"
)

// PriceSpec 描述单个模型的标准单价（单位：美元 / 百万 Token）。
type PriceSpec struct {
	Input      float64 `json:"input_price"`
	Output     float64 `json:"output_price"`
	CacheRead  float64 `json:"cache_read_price"`
	CacheWrite float64 `json:"cache_write_price"`
	Vendor     string  `json:"vendor,omitempty"`
	Source     string  `json:"source"` // "official" | "custom" | "unpriced"
}

// SupportsCache 判断该价格条目是否显式配置了缓存读写单价。
func (p PriceSpec) SupportsCache() bool {
	return p.CacheRead > 0 || p.CacheWrite > 0
}

// CatalogEntry 是官方价格目录中的单条标准模型定义。
type CatalogEntry struct {
	Model string    `json:"model"`
	Spec  PriceSpec `json:"spec"`
}

var (
	// 匹配常见日期快照后缀：
	//   - ISO 日期：-2025-04-14, -2024-08-06
	//   - 8 位日期：-20251001, -20250514
	//   - 月-日快照：-05-06, -03-25
	//   - 4 位日期（MMDD 或 YYMM）：-0902, -0528, -0324, -0905, -2507
	dateSuffixRe = regexp.MustCompile(`(?i)-(?:(?:19|20)\d{2}-\d{2}-\d{2}|(?:19|20)\d{6}|(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])|\d{4})$`)

	// 匹配常见渠道推理档位或快照变体后缀（仅在精确模型名未命中时作为回退剥离）。
	variantSuffixRe = regexp.MustCompile(`(?i)-(?:maxthinking|nothinking|thinking|xhigh|high|medium|low|latest|preview|exp|beta|00[1-9])$`)
)

// officialCatalogEntries 按厂商家族维护四大平台（WorkBuddy / AntiGravity / Command Code / OpenCode）
// 全部真实与平台模型的官方标准牌价（单位：USD / 1M Tokens）。
var officialCatalogEntries = buildOfficialCatalog()

var (
	officialExactMap     map[string]PriceSpec
	officialCanonicalMap map[string]PriceSpec
)

func init() {
	officialExactMap = make(map[string]PriceSpec, len(officialCatalogEntries))
	officialCanonicalMap = make(map[string]PriceSpec, len(officialCatalogEntries))
	for _, entry := range officialCatalogEntries {
		key := strings.ToLower(strings.TrimSpace(entry.Model))
		spec := entry.Spec
		spec.Source = SourceOfficial
		if spec.Vendor == "" {
			spec.Vendor = InferVendor(key)
		}
		officialExactMap[key] = spec
		ck := CanonicalVersionKey(key)
		if _, exists := officialCanonicalMap[ck]; !exists {
			officialCanonicalMap[ck] = spec
		}
	}
}

func buildOfficialCatalog() []CatalogEntry {
	var out []CatalogEntry
	add := func(vendor string, input, output, cacheRead, cacheWrite float64, models ...string) {
		spec := PriceSpec{
			Input:      input,
			Output:     output,
			CacheRead:  cacheRead,
			CacheWrite: cacheWrite,
			Vendor:     vendor,
			Source:     SourceOfficial,
		}
		for _, m := range models {
			out = append(out, CatalogEntry{Model: m, Spec: spec})
		}
	}

	// ---- Anthropic 系 ----
	// 新款 Opus 4.5 / 4.6 / 4.7 / 4.8 / 5 / 5.5 官方 API 标准价：$5 / $25
	add("anthropic", 5.00, 25.00, 0.50, 6.25,
		"claude-opus-4.6",
		"claude-opus-4.5",
		"claude-opus-4.7",
		"claude-opus-4.8",
		"claude-opus-5",
		"claude-opus-5.5",
	)
	// 老款 Opus 4 / 4.1 / 3：$15 / $75
	add("anthropic", 15.00, 75.00, 1.50, 18.75,
		"claude-opus-4",
		"claude-opus-4.1",
		"claude-3-opus",
		"claude-opus-3",
	)
	// Sonnet 全系与 Fable 系：$3 / $15
	add("anthropic", 3.00, 15.00, 0.30, 3.75,
		"claude-sonnet-4.6",
		"claude-sonnet-4.5",
		"claude-sonnet-4",
		"claude-sonnet-5",
		"claude-3.7-sonnet",
		"claude-sonnet-3.7",
		"claude-3.5-sonnet",
		"claude-sonnet-3.5",
		"claude-3-sonnet",
		"claude-fable-5",
		"claude-fable-5.1",
	)
	// Haiku 系
	add("anthropic", 1.00, 5.00, 0.10, 1.25,
		"claude-haiku-4.5",
	)
	add("anthropic", 0.80, 4.00, 0.08, 1.00,
		"claude-3.5-haiku",
		"claude-haiku-3.5",
	)
	add("anthropic", 0.25, 1.25, 0.03, 0.30,
		"claude-3-haiku",
		"claude-haiku-3",
	)

	// ---- OpenAI 系 ----
	add("openai", 1.25, 10.00, 0.125, 0,
		"gpt-5",
		"gpt-5-codex",
		"gpt-5.1",
		"gpt-5.1-codex",
		"gpt-5.1-codex-max",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.3-codex",
		"gpt-5.4",
		"gpt-5.5",
	)
	add("openai", 0.25, 2.00, 0.025, 0,
		"gpt-5-mini",
		"gpt-5.1-codex-mini",
		"gpt-5.4-mini",
	)
	add("openai", 0.05, 0.40, 0.005, 0,
		"gpt-5-nano",
	)
	add("openai", 1.50, 10.00, 0.15, 0,
		"gpt-5.6-luna",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
	)
	add("openai", 2.00, 12.00, 0.20, 0,
		"gpt-6-astra",
		"gpt-6-sol",
		"gpt-6-luna",
	)
	add("openai", 2.00, 8.00, 0.50, 0,
		"gpt-4.1",
		"o3",
	)
	add("openai", 0.40, 1.60, 0.10, 0,
		"gpt-4.1-mini",
	)
	add("openai", 0.10, 0.40, 0.025, 0,
		"gpt-4.1-nano",
	)
	add("openai", 2.50, 10.00, 1.25, 0,
		"gpt-4o",
		"chatgpt-4o-latest",
	)
	add("openai", 0.15, 0.60, 0.075, 0,
		"gpt-4o-mini",
	)
	add("openai", 1.10, 4.40, 0.275, 0,
		"o3-mini",
		"o4-mini",
		"codex-mini-latest",
	)
	add("openai", 15.00, 60.00, 7.50, 0,
		"o1",
	)
	add("openai", 1.10, 4.40, 0.55, 0,
		"o1-mini",
	)
	add("openai", 0.15, 0.60, 0.03, 0,
		"gpt-oss-120b",
	)

	// ---- Google Gemini 系（含 AntiGravity 真实主力模型）----
	add("google", 2.00, 12.00, 0.20, 0,
		"gemini-4-neon",
		"gemini-4-carbon",
		"gemini-pro-orchestrator",
	)
	add("google", 1.25, 10.00, 0.125, 0,
		"gemini-4-hydrogen",
		"gemini-4-hydrogen-sonar",
		"gemini-2.5-pro",
		"gemini-3-pro",
		"gemini-3.1-pro",
		"gemini-3.5-pro",
	)
	add("google", 0.50, 3.00, 0.05, 0,
		"gemini-4-argon",
	)
	add("google", 0.30, 2.50, 0.03, 0,
		"gemini-2.5-flash",
		"gemini-3-flash",
		"gemini-3.1-flash",
		"gemini-3.1-flash-image",
		"gemini-3.5-flash",
		"gemini-3.6-flash",
		"gemini-3.7-flash",
		"gemini-3.7-flash-tiered",
		"gemini-3.8-flash",
		"gemini-3.8-flash-tiered",
	)
	add("google", 0.10, 0.40, 0.01, 0,
		"gemini-2.5-flash-lite",
		"gemini-3-flash-lite",
		"gemini-3.1-flash-lite",
		"gemini-3.5-flash-lite",
	)
	add("google", 0.10, 0.40, 0.025, 0,
		"gemini-2.0-flash",
	)
	add("google", 0.075, 0.30, 0.01875, 0,
		"gemini-2.0-flash-lite",
		"gemini-1.5-flash",
	)
	add("google", 1.25, 5.00, 0.3125, 0,
		"gemini-1.5-pro",
	)

	// ---- DeepSeek 系 ----
	add("deepseek", 0.27, 1.10, 0.07, 0,
		"deepseek-v3",
		"deepseek-v3-0324",
		"deepseek-chat",
	)
	add("deepseek", 0.28, 0.42, 0.028, 0,
		"deepseek-v3.1",
		"deepseek-v3.1-terminus",
		"deepseek-v3.2",
		"deepseek-v3.2-exp",
	)
	add("deepseek", 0.55, 2.19, 0.14, 0,
		"deepseek-r1",
		"deepseek-r1-0528",
		"deepseek-reasoner",
	)
	add("deepseek", 0.66, 1.98, 0.066, 0,
		"deepseek-v4-pro",
		"deepseek-v4",
	)
	add("deepseek", 0.15, 0.60, 0.015, 0,
		"deepseek-v4-flash",
		"deepseek-v4.1-flash",
		"deepseek-v4-flash-vision-exp",
	)
	add("deepseek", 0.20, 0.60, 0.02, 0,
		"deepseek-v4-flash-fast",
		"deepseek-v4.1-flash-fast",
	)

	// ---- 智谱 GLM 系 ----
	add("zhipu", 0.60, 2.20, 0.11, 0,
		"glm-4.5",
		"glm-4.6",
		"glm-4.7",
	)
	add("zhipu", 0.14, 0.86, 0.03, 0,
		"glm-4.5-air",
		"glm-4.5-flash",
	)
	add("zhipu", 1.00, 3.20, 0.20, 0,
		"glm-5",
	)
	add("zhipu", 1.40, 4.40, 0.26, 0,
		"glm-5.1",
		"glm-5.2",
		"glm-5.3",
		"glm-5.4",
	)
	add("zhipu", 3.00, 10.25, 0.50, 0,
		"glm-5.2-fast",
	)
	add("zhipu", 0.15, 0.50, 0.03, 0,
		"glm-5.3-flash",
	)
	add("zhipu", 0.37, 1.25, 0.075, 0,
		"glm-5.3-flashx",
	)

	// ---- 阿里 Qwen 系 ----
	add("qwen", 0.35, 1.50, 0.07, 0,
		"qwen3-coder",
		"qwen-3-coder",
		"qwen3-coder-plus",
		"qwen-coder-plus",
		"qwen3-coder-480b-a35b-instruct",
	)
	add("qwen", 1.60, 6.40, 0.32, 0,
		"qwen3-max",
		"qwen-max",
		"qwen3.7-max",
		"qwen3.8-max",
		"qwen3.8-max-0902",
		"qwen3.6-max-preview",
	)
	add("qwen", 0.40, 1.20, 0.08, 0,
		"qwen3-plus",
		"qwen-plus",
		"qwen3.6-plus",
		"qwen3.7-plus",
		"qwen3.8-27b",
		"qwen3-235b-a22b",
		"qwq-32b",
	)
	add("qwen", 0.10, 0.40, 0.02, 0,
		"qwen3-flash",
		"qwen-flash",
		"qwen-turbo",
		"qwen3-coder-flash",
		"qwen3.7-flash",
		"qwen3.8-flash",
		"qwen3.8-omni-flash",
	)

	// ---- Moonshot Kimi 系 ----
	add("moonshot", 0.60, 2.50, 0.15, 0,
		"kimi-k2",
		"kimi-k2-0905",
		"kimi-k2-0711-preview",
		"kimi-k2-turbo-preview",
		"kimi-k2-thinking",
	)
	add("moonshot", 0.60, 3.00, 0.10, 0,
		"kimi-k2.5",
	)
	add("moonshot", 0.95, 4.00, 0.19, 0,
		"kimi-k2.6",
		"kimi-k2.7-code",
	)
	add("moonshot", 1.90, 8.00, 0.38, 0,
		"kimi-k2.7-code-highspeed",
	)
	add("moonshot", 1.20, 5.00, 0.20, 0,
		"kimi-k2.8",
		"kimi-k2.8-preview",
	)
	add("moonshot", 2.00, 10.00, 0.20, 0,
		"kimi-k3",
		"kimi-k3-1",
		"kimi-k3.1-pro",
	)

	// ---- MiniMax 系 ----
	add("minimax", 0.30, 1.20, 0.06, 0,
		"minimax-m2",
		"minimax-m2.1",
		"minimax-m2.5",
		"minimax-m2.7",
		"minimax-m3",
	)

	// ---- 小米 MiMo 系 ----
	add("xiaomi", 0.14, 0.28, 0.0028, 0,
		"mimo-v2.5",
		"mimo-v2.6-flash",
		"mimo-v2-flash",
	)
	add("xiaomi", 0.435, 0.87, 0.0036, 0,
		"mimo-v2.5-pro",
		"mimo-v2.6-pro",
	)
	add("xiaomi", 0.87, 1.74, 0.0072, 0,
		"mimo-v2.6-pro-ultraspeed",
	)

	// ---- 腾讯混元系 ----
	add("tencent", 0.55, 2.20, 0.11, 0,
		"hunyuan-t1",
		"hunyuan-turbos",
		"hunyuan-pro",
	)
	add("tencent", 0.14, 0.58, 0.035, 0,
		"tencent-hy3",
		"hy3-paid",
		"hy3",
	)
	add("tencent", 0.834, 2.501, 0.042, 0,
		"tencent-hy4-preview",
		"hy4-preview",
		"hy4",
		"hy4std",
	)
	add("tencent", 0.08, 0.32, 0.01, 0,
		"hy4flash",
	)

	// ---- xAI Grok 系 ----
	add("xai", 3.00, 15.00, 0.75, 0,
		"grok-3",
		"grok-4",
		"grok-4.5",
		"grok-4.6",
		"grok-4.7",
	)
	add("xai", 0.30, 0.50, 0.075, 0,
		"grok-3-mini",
	)
	add("xai", 0.20, 1.50, 0.02, 0,
		"grok-code-fast-1",
	)

	// ---- StepFun 阶跃星辰 ----
	add("stepfun", 0.09, 0.30, 0.02, 0,
		"step-3.5-flash",
	)
	add("stepfun", 0.20, 1.15, 0.04, 0,
		"step-3.7-flash",
	)
	add("stepfun", 1.00, 2.70, 0.05, 0,
		"step-5-preview",
	)

	// ---- 其他平台目录模型（Meituan / Mistral / NVIDIA / ThinkingMachines / Sakana / Stealth / Meta）----
	add("meituan", 0.30, 1.20, 0.006, 0,
		"longcat-2.0",
	)
	add("mistral", 1.36, 4.18, 0.14, 0,
		"mistral-large-4",
		"mistral-large",
	)
	add("nvidia", 0.60, 2.40, 0.12, 0,
		"nemotron-3-ultra",
		"nemotron-3-ultra-550b-a55b",
	)
	add("thinkingmachines", 1.00, 4.05, 0.17, 0,
		"inkling",
	)
	add("thinkingmachines", 0.50, 1.20, 0.10, 0,
		"inkling-small",
	)
	add("sakana", 0.50, 2.00, 0.10, 0,
		"fugu-ultra",
	)
	add("stealth", 0.20, 0.80, 0.04, 0,
		"space-bunny-alpha",
		"pixel-canary",
	)
	add("meta", 0.10, 0.20, 0.002, 0,
		"muse-spark-1.1",
		"muse-spark-1.2",
		"muse-spark-1.2-contributor",
		"muse-spark-1.3",
		"muse-spark-1.3-contributor",
	)
	add("meta", 0.20, 0.60, 0.04, 0,
		"llama-4-maverick",
	)
	add("meta", 0.10, 0.30, 0.02, 0,
		"llama-4-scout",
	)
	add("meta", 0.12, 0.30, 0.03, 0,
		"llama-3.3-70b-instruct",
	)

	return out
}

// OfficialCatalog 返回内置官方模型价格目录副本（保持稳定声明顺序）。
func OfficialCatalog() []CatalogEntry {
	out := make([]CatalogEntry, len(officialCatalogEntries))
	copy(out, officialCatalogEntries)
	return out
}

// OfficialPrices 返回内置官方模型价格字典副本（小写模型 ID -> PriceSpec）。
func OfficialPrices() map[string]PriceSpec {
	out := make(map[string]PriceSpec, len(officialExactMap))
	for k, v := range officialExactMap {
		out[k] = v
	}
	return out
}

// LookupOfficial 根据智能模型 ID 归一化规则查找官方标准牌价：
//  1. 大小写不敏感精确匹配；
//  2. 识别 :free / -free 免费后缀（直接返回 0 元且 Source="official"，匹配成功）；
//  3. 剥离 vendor/ 前缀及 :thinking 等标签后再匹配；
//  4. 剥离 8 位/4 位/ISO 日期后缀及常见渠道档位后缀再匹配；
//  5. 双向兼容数字版本号中的点号与横线（如 claude-sonnet-4.5 <-> claude-sonnet-4-5）。
func LookupOfficial(model string) (PriceSpec, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" || strings.EqualFold(trimmed, "unknown") {
		return PriceSpec{Source: SourceUnpriced}, false
	}

	if IsFreeModel(trimmed) {
		return PriceSpec{
			Vendor: InferVendor(trimmed),
			Source: SourceOfficial,
		}, true
	}

	for _, cand := range CandidateKeys(trimmed) {
		if spec, ok := officialExactMap[cand]; ok {
			return spec, true
		}
		if spec, ok := officialCanonicalMap[CanonicalVersionKey(cand)]; ok {
			return spec, true
		}
	}

	return PriceSpec{
		Vendor: InferVendor(trimmed),
		Source: SourceUnpriced,
	}, false
}

// LookupInMap 在自定义价格表（如 PricingService 的内存表）中按同套智能归一化规则查找匹配项。
// 注意：对 -free / :free 结尾的模型仅允许匹配显式针对该免费模型配置的自定义项，绝不剥离 -free 误命中付费模型。
func LookupInMap(table map[string]PriceSpec, model string) (PriceSpec, bool) {
	if len(table) == 0 {
		return PriceSpec{Source: SourceUnpriced}, false
	}
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return PriceSpec{Source: SourceUnpriced}, false
	}

	// 快速构建小写与版本号规范化索引（自定义覆盖表通常只有几条到几十条，开销极小；
	// 高频调用方 PricingService 也可使用预构建双索引）。
	exact := make(map[string]PriceSpec, len(table))
	canonical := make(map[string]PriceSpec, len(table))
	for k, v := range table {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "" {
			continue
		}
		exact[lk] = v
		ck := CanonicalVersionKey(lk)
		if _, exists := canonical[ck]; !exists {
			canonical[ck] = v
		}
	}

	return LookupPreindexed(exact, canonical, trimmed)
}

// LookupPreindexed 在预构建好的 exact（全小写）与 canonical（数字点横归一）双索引中执行智能匹配。
func LookupPreindexed(exact, canonical map[string]PriceSpec, model string) (PriceSpec, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return PriceSpec{Source: SourceUnpriced}, false
	}
	for _, cand := range CandidateKeys(trimmed) {
		if spec, ok := exact[cand]; ok {
			return spec, true
		}
		if spec, ok := canonical[CanonicalVersionKey(cand)]; ok {
			return spec, true
		}
	}
	return PriceSpec{Source: SourceUnpriced}, false
}

// IsFreeModel 判断模型 ID 是否带有 :free 或 -free 免费层级标识。
func IsFreeModel(model string) bool {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return false
	}
	parts := strings.Split(s, ":")
	base := strings.TrimSpace(parts[0])
	for _, tag := range parts[1:] {
		if strings.TrimSpace(tag) == "free" {
			return true
		}
	}
	if atIdx := strings.IndexByte(base, '@'); atIdx > 0 {
		base = base[:atIdx]
	}
	return base == "free" || strings.HasSuffix(base, "-free") || strings.HasSuffix(base, "/free")
}

// CanonicalVersionKey 将模型名中位于两个数字之间的 '.' 统一转换为 '-'，
// 使 claude-sonnet-4.5 与 claude-sonnet-4-5、claude-3.7-sonnet 与 claude-3-7-sonnet 双向等价。
func CanonicalVersionKey(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if strings.IndexByte(s, '.') < 0 {
		return s
	}
	b := []byte(s)
	for i := 1; i < len(b)-1; i++ {
		if b[i] == '.' && isASCIIDigit(b[i-1]) && isASCIIDigit(b[i+1]) {
			b[i] = '-'
		}
	}
	return string(b)
}

// CandidateKeys 按从精确到宽松的优先级生成模型 ID 的候选匹配键序列。
func CandidateKeys(model string) []string {
	raw := strings.ToLower(strings.TrimSpace(model))
	if raw == "" {
		return nil
	}

	seen := make(map[string]struct{}, 12)
	out := make([]string, 0, 12)
	push := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" {
			return
		}
		if _, exists := seen[k]; exists {
			return
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}

	// 若是免费模型，仅生成保留免费标识（:free / -free）的精确与去 vendor/ 候选键，
	// 绝不剥离 :free / -free 退化到付费模型 ID。
	if IsFreeModel(raw) {
		push(raw)
		for cur := raw; ; {
			slashIdx := strings.IndexByte(cur, '/')
			if slashIdx < 0 {
				break
			}
			cur = strings.TrimSpace(cur[slashIdx+1:])
			if cur != "" {
				push(cur)
			}
		}
		return out
	}

	// Tier 1: 原始小写、剥离 :tag / @revision、以及逐级剥离 vendor/ 前缀
	push(raw)

	clean := raw
	if colonIdx := strings.IndexByte(clean, ':'); colonIdx > 0 {
		clean = clean[:colonIdx]
	}
	if atIdx := strings.IndexByte(clean, '@'); atIdx > 0 {
		clean = clean[:atIdx]
	}
	push(clean)

	baseForms := []string{clean}
	for cur := clean; ; {
		slashIdx := strings.IndexByte(cur, '/')
		if slashIdx < 0 {
			break
		}
		cur = strings.TrimSpace(cur[slashIdx+1:])
		if cur != "" {
			push(cur)
			baseForms = append(baseForms, cur)
		}
	}

	// Tier 2: 剥离日期快照后缀（如 -20251001, -2025-04-14, -0902, -0528）
	dateStripped := make([]string, 0, len(baseForms))
	for _, bf := range baseForms {
		if d := stripDateSuffix(bf); d != bf {
			push(d)
			dateStripped = append(dateStripped, d)
		}
	}

	// Tier 3: 剥离常见渠道推理档位/预览后缀（如 -thinking, -high, -low, -preview, -latest）并再次组合日期剥离
	for _, bf := range baseForms {
		if v := stripVariantSuffix(bf); v != bf {
			push(v)
			if vd := stripDateSuffix(v); vd != v {
				push(vd)
			}
		}
	}
	for _, df := range dateStripped {
		if dv := stripVariantSuffix(df); dv != df {
			push(dv)
			if dvd := stripDateSuffix(dv); dvd != dv {
				push(dvd)
			}
		}
	}

	return out
}

func stripDateSuffix(s string) string {
	cur := s
	for i := 0; i < 2; i++ {
		loc := dateSuffixRe.FindStringIndex(cur)
		if loc == nil || loc[0] <= 0 {
			break
		}
		cur = cur[:loc[0]]
	}
	return cur
}

func stripVariantSuffix(s string) string {
	cur := s
	for i := 0; i < 2; i++ {
		loc := variantSuffixRe.FindStringIndex(cur)
		if loc == nil || loc[0] <= 0 {
			break
		}
		cur = cur[:loc[0]]
	}
	return cur
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// InferVendor 根据 vendor/ 前缀或模型家族前缀推断标准厂商标识。
func InferVendor(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	if colonIdx := strings.IndexByte(s, ':'); colonIdx > 0 {
		s = s[:colonIdx]
	}

	if slashIdx := strings.IndexByte(s, '/'); slashIdx > 0 {
		prefix := s[:slashIdx]
		rest := s[slashIdx+1:]
		switch prefix {
		case "anthropic", "claude":
			return "anthropic"
		case "openai":
			return "openai"
		case "google", "vertex", "gemini":
			return "google"
		case "deepseek":
			return "deepseek"
		case "z-ai", "zai-org", "zhipu", "thudm":
			return "zhipu"
		case "qwen", "alibaba":
			return "qwen"
		case "moonshotai", "moonshot":
			return "moonshot"
		case "minimaxai", "minimax":
			return "minimax"
		case "xiaomi":
			return "xiaomi"
		case "tencent":
			return "tencent"
		case "stepfun":
			return "stepfun"
		case "xai", "x-ai":
			return "xai"
		case "meituan":
			return "meituan"
		case "mistral", "mistralai":
			return "mistral"
		case "nvidia":
			return "nvidia"
		case "thinkingmachines":
			return "thinkingmachines"
		case "sakana":
			return "sakana"
		case "stealth", "poolside", "inclusionai":
			return prefix
		case "meta", "meta-llama":
			return "meta"
		default:
			if v := inferVendorFromBareID(rest); v != "" {
				return v
			}
			return prefix
		}
	}

	return inferVendorFromBareID(s)
}

func inferVendorFromBareID(id string) string {
	switch {
	case strings.HasPrefix(id, "claude"):
		return "anthropic"
	case strings.HasPrefix(id, "gpt-"), strings.HasPrefix(id, "chatgpt-"), strings.HasPrefix(id, "codex"),
		id == "o1" || strings.HasPrefix(id, "o1-"),
		id == "o3" || strings.HasPrefix(id, "o3-"),
		id == "o4" || strings.HasPrefix(id, "o4-"):
		return "openai"
	case strings.HasPrefix(id, "gemini"), strings.HasPrefix(id, "gemma"):
		return "google"
	case strings.HasPrefix(id, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(id, "glm"):
		return "zhipu"
	case strings.HasPrefix(id, "qwen"), strings.HasPrefix(id, "qwq"):
		return "qwen"
	case strings.HasPrefix(id, "kimi"), strings.HasPrefix(id, "moonshot"):
		return "moonshot"
	case strings.HasPrefix(id, "minimax"):
		return "minimax"
	case strings.HasPrefix(id, "mimo"):
		return "xiaomi"
	case strings.HasPrefix(id, "hunyuan"), strings.HasPrefix(id, "tencent-hy"), strings.HasPrefix(id, "hy3"), strings.HasPrefix(id, "hy4"):
		return "tencent"
	case strings.HasPrefix(id, "grok"):
		return "xai"
	case strings.HasPrefix(id, "step-"):
		return "stepfun"
	case strings.HasPrefix(id, "longcat"):
		return "meituan"
	case strings.HasPrefix(id, "mistral"):
		return "mistral"
	case strings.HasPrefix(id, "nemotron"):
		return "nvidia"
	case strings.HasPrefix(id, "inkling"):
		return "thinkingmachines"
	case strings.HasPrefix(id, "fugu"):
		return "sakana"
	case strings.HasPrefix(id, "muse-spark"), strings.HasPrefix(id, "llama"):
		return "meta"
	case strings.HasPrefix(id, "space-bunny"), strings.HasPrefix(id, "pixel-canary"):
		return "stealth"
	default:
		return ""
	}
}
