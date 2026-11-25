package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"

	"cmd2api/ent"
	"cmd2api/ent/modelprice"
	"cmd2api/internal/pricing"
)

var (
	// ErrInvalidModelPrice 表示自定义模型价格参数非法（空模型名、超长或负数/非有限单价）。
	ErrInvalidModelPrice = errors.New("invalid model price")
	// ErrModelPriceNotFound 表示指定的自定义模型价格记录不存在。
	ErrModelPriceNotFound = errors.New("model price not found")
)

// UpsertModelPriceInput 是新增或更新自定义模型单价覆盖项的输入参数（单位：USD / 1M Tokens）。
type UpsertModelPriceInput struct {
	ID              int64   `json:"id,omitempty"`
	Model           string  `json:"model"`
	InputPrice      float64 `json:"input_price"`
	OutputPrice     float64 `json:"output_price"`
	CacheReadPrice  float64 `json:"cache_read_price"`
	CacheWritePrice float64 `json:"cache_write_price"`
	Notes           string  `json:"notes"`
}

// PricingService 管理数据库 model_prices 自定义单价覆盖表与内存热缓存，
// 并在未命中自定义覆盖时自动回退至 pricing.LookupOfficial 权威官方价格目录。
type PricingService struct {
	client *ent.Client
	logger *slog.Logger

	mu                 sync.RWMutex
	overrides          map[string]pricing.PriceSpec
	canonicalOverrides map[string]pricing.PriceSpec
}

// NewPricingService 构造 PricingService。
func NewPricingService(client *ent.Client, logger *slog.Logger) *PricingService {
	if logger == nil {
		logger = slog.Default()
	}
	return &PricingService{
		client:             client,
		logger:             logger,
		overrides:          make(map[string]pricing.PriceSpec),
		canonicalOverrides: make(map[string]pricing.PriceSpec),
	}
}

// LoadFromDB 从数据库 model_prices 表全量读取自定义模型单价并原子替换内存缓存。
func (s *PricingService) LoadFromDB(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}

	rows, err := s.client.ModelPrice.Query().
		Order(ent.Asc(modelprice.FieldID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("load model_prices from db failed: %w", err)
	}

	nextExact := make(map[string]pricing.PriceSpec, len(rows))
	nextCanonical := make(map[string]pricing.PriceSpec, len(rows))
	for _, row := range rows {
		key := strings.ToLower(strings.TrimSpace(row.Model))
		if key == "" {
			continue
		}
		spec := pricing.PriceSpec{
			Input:      row.InputPrice,
			Output:     row.OutputPrice,
			CacheRead:  row.CacheReadPrice,
			CacheWrite: row.CacheWritePrice,
			Vendor:     pricing.InferVendor(row.Model),
			Source:     pricing.SourceCustom,
		}
		nextExact[key] = spec
		nextCanonical[pricing.CanonicalVersionKey(key)] = spec
	}

	s.mu.Lock()
	s.overrides = nextExact
	s.canonicalOverrides = nextCanonical
	s.mu.Unlock()
	return nil
}

// Resolve 解析指定模型的最终生效单价：
// 优先查找内存自定义单价覆盖表（支持精确与归一化匹配），未命中则回退 pricing.LookupOfficial。
func (s *PricingService) Resolve(model string) (pricing.PriceSpec, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" || strings.EqualFold(trimmed, "unknown") {
		return pricing.PriceSpec{Source: pricing.SourceUnpriced}, false
	}

	if s != nil {
		s.mu.RLock()
		exact := s.overrides
		canonical := s.canonicalOverrides
		s.mu.RUnlock()

		if len(exact) > 0 {
			if spec, ok := pricing.LookupPreindexed(exact, canonical, trimmed); ok {
				spec.Source = pricing.SourceCustom
				if spec.Vendor == "" {
					spec.Vendor = pricing.InferVendor(trimmed)
				}
				return spec, true
			}
		}
	}

	return pricing.LookupOfficial(trimmed)
}

// OverridesSnapshot 返回当前内存自定义价格表的只读副本。
func (s *PricingService) OverridesSnapshot() map[string]pricing.PriceSpec {
	if s == nil {
		return map[string]pricing.PriceSpec{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]pricing.PriceSpec, len(s.overrides))
	for k, v := range s.overrides {
		out[k] = v
	}
	return out
}

// ListOverrides 按模型名字母序返回数据库中全部自定义价格覆盖项。
func (s *PricingService) ListOverrides(ctx context.Context) ([]*ent.ModelPrice, error) {
	if s == nil || s.client == nil {
		return []*ent.ModelPrice{}, nil
	}
	rows, err := s.client.ModelPrice.Query().
		Order(ent.Asc(modelprice.FieldModel), ent.Asc(modelprice.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list model_prices failed: %w", err)
	}
	return rows, nil
}

// UpsertOverride 新增或更新一条自定义模型价格覆盖记录，并立即热刷新内存缓存。
func (s *PricingService) UpsertOverride(ctx context.Context, input UpsertModelPriceInput) (*ent.ModelPrice, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("pricing service database client is not initialized")
	}

	modelName := strings.TrimSpace(input.Model)
	notes := strings.TrimSpace(input.Notes)
	if err := validateModelPriceSpec(modelName, notes, input.InputPrice, input.OutputPrice, input.CacheReadPrice, input.CacheWritePrice); err != nil {
		return nil, err
	}

	var saved *ent.ModelPrice
	if input.ID > 0 {
		existing, err := s.client.ModelPrice.Get(ctx, input.ID)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, ErrModelPriceNotFound
			}
			return nil, fmt.Errorf("get model price %d failed: %w", input.ID, err)
		}
		// 若修改了模型名且目标模型名已被另一条记录占用，拒绝重复冲突。
		conflict, err := s.client.ModelPrice.Query().
			Where(modelprice.IDNEQ(existing.ID), modelprice.ModelEqualFold(modelName)).
			Exist(ctx)
		if err != nil {
			return nil, fmt.Errorf("check duplicate model price failed: %w", err)
		}
		if conflict {
			return nil, fmt.Errorf("%w: model %q already has a custom override", ErrInvalidModelPrice, modelName)
		}
		saved, err = existing.Update().
			SetModel(modelName).
			SetInputPrice(input.InputPrice).
			SetOutputPrice(input.OutputPrice).
			SetCacheReadPrice(input.CacheReadPrice).
			SetCacheWritePrice(input.CacheWritePrice).
			SetNotes(notes).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("update model price failed: %w", err)
		}
	} else {
		existing, err := s.client.ModelPrice.Query().
			Where(modelprice.ModelEqualFold(modelName)).
			First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, fmt.Errorf("query existing model price failed: %w", err)
		}
		if existing != nil {
			saved, err = existing.Update().
				SetModel(modelName).
				SetInputPrice(input.InputPrice).
				SetOutputPrice(input.OutputPrice).
				SetCacheReadPrice(input.CacheReadPrice).
				SetCacheWritePrice(input.CacheWritePrice).
				SetNotes(notes).
				Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("update model price by model failed: %w", err)
			}
		} else {
			saved, err = s.client.ModelPrice.Create().
				SetModel(modelName).
				SetInputPrice(input.InputPrice).
				SetOutputPrice(input.OutputPrice).
				SetCacheReadPrice(input.CacheReadPrice).
				SetCacheWritePrice(input.CacheWritePrice).
				SetNotes(notes).
				Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("create model price failed: %w", err)
			}
		}
	}

	if err := s.LoadFromDB(ctx); err != nil {
		return nil, err
	}
	return saved, nil
}

// DeleteOverride 删除指定 ID 的自定义模型价格记录并立即热刷新内存缓存（回退官方牌价）。
func (s *PricingService) DeleteOverride(ctx context.Context, id int64) error {
	if s == nil || s.client == nil {
		return errors.New("pricing service database client is not initialized")
	}
	if id <= 0 {
		return fmt.Errorf("%w: invalid id %d", ErrInvalidModelPrice, id)
	}
	if err := s.client.ModelPrice.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrModelPriceNotFound
		}
		return fmt.Errorf("delete model price %d failed: %w", id, err)
	}
	return s.LoadFromDB(ctx)
}

func validateModelPriceSpec(modelName, notes string, inputPrice, outputPrice, cacheReadPrice, cacheWritePrice float64) error {
	if modelName == "" {
		return fmt.Errorf("%w: model name is required", ErrInvalidModelPrice)
	}
	if len(modelName) > 150 {
		return fmt.Errorf("%w: model name exceeds 150 characters", ErrInvalidModelPrice)
	}
	if len(notes) > 500 {
		return fmt.Errorf("%w: notes exceeds 500 characters", ErrInvalidModelPrice)
	}
	for label, val := range map[string]float64{
		"input_price":       inputPrice,
		"output_price":      outputPrice,
		"cache_read_price":  cacheReadPrice,
		"cache_write_price": cacheWritePrice,
	} {
		if math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
			return fmt.Errorf("%w: %s must be a non-negative finite number", ErrInvalidModelPrice, label)
		}
	}
	return nil
}
