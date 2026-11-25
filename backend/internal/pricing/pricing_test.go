package pricing

import (
	"math"
	"testing"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestOfficialCatalogWellFormed(t *testing.T) {
	catalog := OfficialCatalog()
	if len(catalog) < 80 {
		t.Fatalf("official catalog unexpectedly small: got %d entries", len(catalog))
	}

	seen := make(map[string]struct{}, len(catalog))
	for i, entry := range catalog {
		if entry.Model == "" {
			t.Errorf("entry[%d] has empty model id", i)
		}
		if _, dup := seen[entry.Model]; dup {
			t.Errorf("duplicate model id %q in OfficialCatalog", entry.Model)
		}
		seen[entry.Model] = struct{}{}

		if entry.Spec.Input < 0 || entry.Spec.Output < 0 || entry.Spec.CacheRead < 0 || entry.Spec.CacheWrite < 0 {
			t.Errorf("model %q has negative price: %+v", entry.Model, entry.Spec)
		}
		if entry.Spec.Vendor == "" {
			t.Errorf("model %q missing vendor", entry.Model)
		}
		if entry.Spec.Source != SourceOfficial {
			t.Errorf("model %q expected source=%q, got %q", entry.Model, SourceOfficial, entry.Spec.Source)
		}
	}

	prices := OfficialPrices()
	if len(prices) != len(catalog) {
		t.Errorf("OfficialPrices len=%d, want %d", len(prices), len(catalog))
	}
}

func TestLookupOfficialBidirectionalDotAndHyphen(t *testing.T) {
	cases := []struct {
		dotModel   string
		dashModel  string
		wantInput  float64
		wantOutput float64
		wantRead   float64
		wantWrite  float64
		wantVendor string
	}{
		{"claude-sonnet-4.5", "claude-sonnet-4-5", 3.00, 15.00, 0.30, 3.75, "anthropic"},
		{"claude-sonnet-4.6", "claude-sonnet-4-6", 3.00, 15.00, 0.30, 3.75, "anthropic"},
		{"claude-opus-4.5", "claude-opus-4-5", 5.00, 25.00, 0.50, 6.25, "anthropic"},
		{"claude-opus-4.6", "claude-opus-4-6", 5.00, 25.00, 0.50, 6.25, "anthropic"},
		{"claude-opus-4.7", "claude-opus-4-7", 5.00, 25.00, 0.50, 6.25, "anthropic"},
		{"claude-opus-4.8", "claude-opus-4-8", 5.00, 25.00, 0.50, 6.25, "anthropic"},
		{"claude-opus-5.5", "claude-opus-5-5", 5.00, 25.00, 0.50, 6.25, "anthropic"},
		{"claude-opus-4.1", "claude-opus-4-1", 15.00, 75.00, 1.50, 18.75, "anthropic"},
		{"claude-3.7-sonnet", "claude-3-7-sonnet", 3.00, 15.00, 0.30, 3.75, "anthropic"},
		{"claude-3.5-sonnet", "claude-3-5-sonnet", 3.00, 15.00, 0.30, 3.75, "anthropic"},
		{"claude-haiku-4.5", "claude-haiku-4-5", 1.00, 5.00, 0.10, 1.25, "anthropic"},
		{"claude-3.5-haiku", "claude-3-5-haiku", 0.80, 4.00, 0.08, 1.00, "anthropic"},
		{"claude-fable-5.1", "claude-fable-5-1", 3.00, 15.00, 0.30, 3.75, "anthropic"},
		{"gemini-2.5-pro", "gemini-2-5-pro", 1.25, 10.00, 0.125, 0, "google"},
		{"gemini-3.1-pro", "gemini-3-1-pro", 1.25, 10.00, 0.125, 0, "google"},
		{"gemini-3.5-flash", "gemini-3-5-flash", 0.30, 2.50, 0.03, 0, "google"},
		{"gpt-5.3-codex", "gpt-5-3-codex", 1.25, 10.00, 0.125, 0, "openai"},
		{"deepseek-v4.1-flash", "deepseek-v4-1-flash", 0.15, 0.60, 0.015, 0, "deepseek"},
	}

	for _, tc := range cases {
		for _, m := range []string{tc.dotModel, tc.dashModel} {
			spec, ok := LookupOfficial(m)
			if !ok {
				t.Fatalf("LookupOfficial(%q) expected match, got not found", m)
			}
			if !almostEqual(spec.Input, tc.wantInput) ||
				!almostEqual(spec.Output, tc.wantOutput) ||
				!almostEqual(spec.CacheRead, tc.wantRead) ||
				!almostEqual(spec.CacheWrite, tc.wantWrite) {
				t.Errorf("LookupOfficial(%q) got %+v, want in=%v out=%v cr=%v cw=%v",
					m, spec, tc.wantInput, tc.wantOutput, tc.wantRead, tc.wantWrite)
			}
			if spec.Vendor != tc.wantVendor || spec.Source != SourceOfficial {
				t.Errorf("LookupOfficial(%q) vendor/source=%s/%s, want %s/%s",
					m, spec.Vendor, spec.Source, tc.wantVendor, SourceOfficial)
			}
		}
	}
}

func TestLookupOfficialVendorPrefixesTagsAndDateSuffixes(t *testing.T) {
	cases := []struct {
		model      string
		wantInput  float64
		wantOutput float64
		wantRead   float64
	}{
		{"anthropic/claude-sonnet-4.5", 3.00, 15.00, 0.30},
		{"anthropic/claude-opus-4-6:thinking", 5.00, 25.00, 0.50},
		{"claude-haiku-4-5-20251001", 1.00, 5.00, 0.10},
		{"claude-sonnet-4-20250514", 3.00, 15.00, 0.30},
		{"claude-opus-4-20250514", 15.00, 75.00, 1.50},
		{"claude-opus-4-5-20251101", 5.00, 25.00, 0.50},
		{"claude-sonnet-4-5@20250929", 3.00, 15.00, 0.30},
		{"openai/gpt-5.4", 1.25, 10.00, 0.125},
		{"gpt-4.1-2025-04-14", 2.00, 8.00, 0.50},
		{"gpt-4o-2024-08-06", 2.50, 10.00, 1.25},
		{"o3-2025-04-16", 2.00, 8.00, 0.50},
		{"google/gemini-3.8-flash", 0.30, 2.50, 0.03},
		{"models/gemini-2.5-pro-preview-05-06", 1.25, 10.00, 0.125},
		{"gemini-3.1-pro-high", 1.25, 10.00, 0.125},
		{"gemini-3-pro-low", 1.25, 10.00, 0.125},
		{"deepseek/deepseek-v4-pro", 0.66, 1.98, 0.066},
		{"deepseek/deepseek-r1-0528", 0.55, 2.19, 0.14},
		{"z-ai/glm-5.3-flashx", 0.37, 1.25, 0.075},
		{"zai-org/GLM-5.3", 1.40, 4.40, 0.26},
		{"Qwen/Qwen3.8-Max-0902", 1.60, 6.40, 0.32},
		{"Qwen/Qwen3.8-Max-1118", 1.60, 6.40, 0.32},
		{"moonshotai/Kimi-K3", 2.00, 10.00, 0.20},
		{"MiniMaxAI/MiniMax-M3", 0.30, 1.20, 0.06},
		{"xiaomi/mimo-v2.6-pro", 0.435, 0.87, 0.0036},
		{"stepfun/Step-3.7-Flash", 0.20, 1.15, 0.04},
		{"tencent/hy3-paid", 0.14, 0.58, 0.035},
		{"tencent/hy4-preview", 0.834, 2.501, 0.042},
		{"xai/grok-4.7", 3.00, 15.00, 0.75},
		{"nvidia/nemotron-3-ultra-550b-a55b", 0.60, 2.40, 0.12},
		{"meta/muse-spark-1.3-contributor", 0.10, 0.20, 0.002},
	}

	for _, tc := range cases {
		spec, ok := LookupOfficial(tc.model)
		if !ok {
			t.Errorf("LookupOfficial(%q) failed to match", tc.model)
			continue
		}
		if !almostEqual(spec.Input, tc.wantInput) ||
			!almostEqual(spec.Output, tc.wantOutput) ||
			!almostEqual(spec.CacheRead, tc.wantRead) {
			t.Errorf("LookupOfficial(%q) = %+v, want in=%v out=%v cr=%v",
				tc.model, spec, tc.wantInput, tc.wantOutput, tc.wantRead)
		}
	}
}

func TestLookupOfficialFreeAndUnpricedModels(t *testing.T) {
	freeModels := []string{
		"deepseek-v4-flash-free",
		"space-bunny-free",
		"poolside/laguna-s-2.1-free",
		"inclusionai/ling-3.0-flash-sante:free",
		"google/gemini-2.5-flash:free",
		"deepseek/deepseek-r1:free:thinking",
	}
	for _, m := range freeModels {
		spec, ok := LookupOfficial(m)
		if !ok {
			t.Errorf("LookupOfficial(%q) expected free model match (ok=true), got false", m)
			continue
		}
		if spec.Source != SourceOfficial {
			t.Errorf("LookupOfficial(%q) source=%q, want %q", m, spec.Source, SourceOfficial)
		}
		if spec.Input != 0 || spec.Output != 0 || spec.CacheRead != 0 || spec.CacheWrite != 0 {
			t.Errorf("LookupOfficial(%q) expected $0 free spec, got %+v", m, spec)
		}
	}

	unpricedModels := []string{
		"",
		"   ",
		"unknown",
		"some-future-model",
		"vendor/non-existent-model-20261231",
	}
	for _, m := range unpricedModels {
		spec, ok := LookupOfficial(m)
		if ok {
			t.Errorf("LookupOfficial(%q) expected ok=false, got %+v", m, spec)
		}
		if spec.Source != SourceUnpriced {
			t.Errorf("LookupOfficial(%q) expected source=%q, got %q", m, SourceUnpriced, spec.Source)
		}
	}
}

func TestLookupInMapDoesNotLeakPaidOverrideToFreeTier(t *testing.T) {
	overrides := map[string]PriceSpec{
		"deepseek-v4-flash": {Input: 0.99, Output: 2.99, Source: SourceCustom},
	}
	if _, ok := LookupInMap(overrides, "deepseek/deepseek-v4-flash"); !ok {
		t.Error("expected vendor-prefixed paid model to match custom override")
	}
	if _, ok := LookupInMap(overrides, "deepseek-v4-flash-free"); ok {
		t.Error("free model variant must NOT inherit paid model custom override")
	}
	if _, ok := LookupInMap(overrides, "deepseek/deepseek-v4-flash:free"); ok {
		t.Error(":free tagged model must NOT inherit paid model custom override")
	}

	// Explicit custom override on the free model itself IS allowed.
	overrides["deepseek-v4-flash-free"] = PriceSpec{Input: 0.05, Output: 0.10, Source: SourceCustom}
	if spec, ok := LookupInMap(overrides, "deepseek/deepseek-v4-flash-free"); !ok || !almostEqual(spec.Input, 0.05) {
		t.Errorf("expected explicit override on -free model to match, got ok=%v spec=%+v", ok, spec)
	}
}
