package handler

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/domain"
	"cmd2api/internal/pricing"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/service"
	"cmd2api/internal/workbuddy"

	"github.com/gin-gonic/gin"
)

type modelPriceDTO struct {
	ID              int64     `json:"id"`
	Model           string    `json:"model"`
	InputPrice      float64   `json:"input_price"`
	OutputPrice     float64   `json:"output_price"`
	CacheReadPrice  float64   `json:"cache_read_price"`
	CacheWritePrice float64   `json:"cache_write_price"`
	Notes           string    `json:"notes"`
	IsBuiltin       bool      `json:"is_builtin"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func toModelPriceDTO(row *ent.ModelPrice) modelPriceDTO {
	return modelPriceDTO{
		ID:              row.ID,
		Model:           row.Model,
		InputPrice:      row.InputPrice,
		OutputPrice:     row.OutputPrice,
		CacheReadPrice:  row.CacheReadPrice,
		CacheWritePrice: row.CacheWritePrice,
		Notes:           row.Notes,
		IsBuiltin:       false,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

type catalogModelItemDTO struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Vendor            string   `json:"vendor"`
	Platforms         []string `json:"platforms"`
	Platform          string   `json:"platform,omitempty"`
	ContextWindow     int      `json:"context_window,omitempty"`
	MaxOutputTokens   int      `json:"max_output_tokens,omitempty"`
	SupportsImages    bool     `json:"supports_images"`
	SupportsReasoning bool     `json:"supports_reasoning"`
	SupportsToolCall  bool     `json:"supports_tool_call"`
	SupportsCache     bool     `json:"supports_cache"`
	InputPrice        float64  `json:"input_price"`
	OutputPrice       float64  `json:"output_price"`
	CacheReadPrice    float64  `json:"cache_read_price"`
	CacheWritePrice   float64  `json:"cache_write_price"`
	IsCustomPrice     bool     `json:"is_custom_price"`
	PriceSource       string   `json:"price_source"`
	AccountCount      int      `json:"account_count"`
}

// GetModelCatalog 聚合四大平台可用模型与标准/自定义定价目录（GET /api/models/catalog）。
func (h *Handler) GetModelCatalog(c *gin.Context) {
	ctx := c.Request.Context()
	refresh, _ := strconv.ParseBool(strings.TrimSpace(c.Query("refresh")))
	rawFilterPlat := strings.TrimSpace(c.Query("platform"))
	filterPlat := ""
	if rawFilterPlat != "" && !strings.EqualFold(rawFilterPlat, "all") {
		filterPlat = domain.NormalizePlatform(rawFilterPlat)
	}

	allPlatforms := []string{
		domain.PlatformWorkBuddy,
		domain.PlatformAntigravity,
		domain.PlatformCommandCode,
		domain.PlatformOpenCode,
	}

	// 统计各平台活跃账号数
	activeAccByPlat := map[string]int{}
	if h.client != nil {
		if accs, err := h.client.Account.Query().
			Where(account.DeletedAtIsNil(), account.StatusEQ(domain.StatusActive)).
			All(ctx); err == nil {
			for _, a := range accs {
				activeAccByPlat[domain.NormalizePlatform(a.Platform)]++
			}
		}
	}

	merged := make(map[string]*catalogModelItemDTO)
	platSetByModel := make(map[string]map[string]bool)
	platErrors := make(map[string]string)

	addModel := func(plat string, m relay.Model) {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			return
		}
		key := strings.ToLower(id)
		item, exists := merged[key]
		if !exists {
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = id
			}
			vendor := strings.TrimSpace(m.Vendor)
			if vendor == "" {
				vendor = pricing.InferVendor(id)
			}
			item = &catalogModelItemDTO{
				ID:                id,
				Name:              name,
				Vendor:            vendor,
				ContextWindow:     m.ContextWindow,
				MaxOutputTokens:   m.MaxOutputTokens,
				SupportsImages:    m.SupportsImages,
				SupportsReasoning: m.SupportsReasoning,
				SupportsToolCall:  m.SupportsToolCall,
			}
			enrichCapabilitiesHeuristic(item)
			merged[key] = item
			platSetByModel[key] = make(map[string]bool)
		} else {
			if item.ContextWindow == 0 && m.ContextWindow > 0 {
				item.ContextWindow = m.ContextWindow
			}
			if item.MaxOutputTokens == 0 && m.MaxOutputTokens > 0 {
				item.MaxOutputTokens = m.MaxOutputTokens
			}
			item.SupportsImages = item.SupportsImages || m.SupportsImages
			item.SupportsReasoning = item.SupportsReasoning || m.SupportsReasoning
			item.SupportsToolCall = item.SupportsToolCall || m.SupportsToolCall
		}
		if plat != "" {
			platSetByModel[key][plat] = true
		}
	}

	// 1. 优先从已配置活跃账号的平台实时拉取真实支持的模型列表，避免塞入无关或虚构的静态模型名。
	liveLoaded := map[string]bool{}
	if h.relay != nil {
		fetchTimeout := 10 * time.Second
		if refresh {
			fetchTimeout = 25 * time.Second
		}
		for _, p := range allPlatforms {
			if filterPlat != "" && p != filterPlat {
				continue
			}
			// 仅对当前已有活跃账号的平台（或用户显式筛选的平台）拉取实时模型
			if activeAccByPlat[p] == 0 && filterPlat == "" {
				continue
			}
			pCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
			liveModels, err := h.relay.PlatformModels(pCtx, p)
			cancel()
			if err != nil {
				if !errors.Is(err, scheduler.ErrNoAccountAvailable) && refresh {
					platErrors[p] = err.Error()
				}
				continue
			}
			if len(liveModels) > 0 {
				liveLoaded[p] = true
				for _, lm := range liveModels {
					addModel(p, lm)
				}
			}
		}
	}

	// 2. 兜底基准：仅当尚无任何实时模型返回（如全新空库离线环境）或显式按某平台查看时，补充真实官方兜底模型。
	totalActiveAccounts := 0
	for _, cnt := range activeAccByPlat {
		totalActiveAccounts += cnt
	}
	if !liveLoaded[domain.PlatformWorkBuddy] && (totalActiveAccounts == 0 || activeAccByPlat[domain.PlatformWorkBuddy] > 0 || filterPlat == domain.PlatformWorkBuddy) {
		for _, wm := range workbuddy.DefaultModels() {
			addModel(domain.PlatformWorkBuddy, relay.Model{
				ID:                wm.ID,
				Name:              wm.Name,
				Vendor:            wm.Vendor,
				ContextWindow:     wm.ContextWindow,
				MaxOutputTokens:   wm.MaxOutputTokens,
				SupportsImages:    wm.SupportsImages,
				SupportsReasoning: wm.SupportsReasoning,
				SupportsToolCall:  wm.SupportsToolCall,
			})
		}
	}
	if !liveLoaded[domain.PlatformAntigravity] && (totalActiveAccounts == 0 || activeAccByPlat[domain.PlatformAntigravity] > 0 || filterPlat == domain.PlatformAntigravity) {
		for _, am := range antigravity.DefaultModels() {
			addModel(domain.PlatformAntigravity, relay.Model{
				ID:                am.ID,
				Name:              am.Name,
				Vendor:            am.Vendor,
				ContextWindow:     am.ContextWindow,
				MaxOutputTokens:   am.MaxOutputTokens,
				SupportsImages:    am.SupportsImages,
				SupportsReasoning: am.SupportsReasoning,
				SupportsToolCall:  am.SupportsToolCall,
			})
		}
	}

	ps := h.pricingSvc()
	var customRows []*ent.ModelPrice
	if ps != nil {
		customRows, _ = ps.ListOverrides(ctx)
		// 确保用户在 model_prices 自定义添加的新模型也出现在目录中
		for _, cr := range customRows {
			key := strings.ToLower(strings.TrimSpace(cr.Model))
			if _, ok := merged[key]; !ok && key != "" {
				addModel("", relay.Model{ID: cr.Model, Name: cr.Model, Vendor: pricing.InferVendor(cr.Model)})
			}
		}
	}

	byPlatformCounts := map[string]int{
		domain.PlatformWorkBuddy:   0,
		domain.PlatformAntigravity: 0,
		domain.PlatformCommandCode: 0,
		domain.PlatformOpenCode:    0,
	}

	items := make([]catalogModelItemDTO, 0, len(merged))
	for key, item := range merged {
		pset := platSetByModel[key]
		plats := make([]string, 0, len(pset))
		accCount := 0
		for _, p := range allPlatforms {
			if pset[p] {
				plats = append(plats, p)
				byPlatformCounts[p]++
				accCount += activeAccByPlat[p]
			}
		}
		if filterPlat != "" && !pset[filterPlat] {
			continue
		}

		item.Platforms = plats
		if len(plats) > 0 {
			item.Platform = plats[0]
		}
		item.AccountCount = accCount

		var spec pricing.PriceSpec
		if ps != nil {
			spec, _ = ps.Resolve(item.ID)
		} else {
			spec, _ = pricing.LookupOfficial(item.ID)
		}
		item.InputPrice = spec.Input
		item.OutputPrice = spec.Output
		item.CacheReadPrice = spec.CacheRead
		item.CacheWritePrice = spec.CacheWrite
		item.SupportsCache = spec.SupportsCache()
		item.PriceSource = spec.Source
		if item.PriceSource == "" {
			item.PriceSource = pricing.SourceUnpriced
		}
		item.IsCustomPrice = item.PriceSource == pricing.SourceCustom
		if item.Vendor == "" && spec.Vendor != "" {
			item.Vendor = spec.Vendor
		}

		items = append(items, *item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Vendor != items[j].Vendor {
			return items[i].Vendor < items[j].Vendor
		}
		return items[i].ID < items[j].ID
	})

	priceDTOs := make([]modelPriceDTO, 0, len(customRows))
	for _, r := range customRows {
		priceDTOs = append(priceDTOs, toModelPriceDTO(r))
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	c.JSON(http.StatusOK, gin.H{
		"items":        items,
		"total":        len(items),
		"by_platform":  byPlatformCounts,
		"refreshed_at": nowStr,
		"cached":       !refresh,
		"errors":       platErrors,
		"prices":       priceDTOs,
	})
}

func seedCommandAndOpenCodeCatalog(addModel func(plat string, m relay.Model)) {
	for _, entry := range pricing.OfficialCatalog() {
		id := entry.Model
		lower := strings.ToLower(id)
		m := relay.Model{
			ID:     id,
			Name:   id,
			Vendor: entry.Spec.Vendor,
		}
		if strings.HasSuffix(lower, "-free") || strings.Contains(lower, "nemotron") || strings.Contains(lower, "big-pickle") {
			addModel(domain.PlatformOpenCode, m)
			continue
		}
		if strings.HasPrefix(lower, "claude-") || strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "gemini-") ||
			strings.HasPrefix(lower, "glm-") || strings.HasPrefix(lower, "kimi-") || strings.HasPrefix(lower, "minimax-") ||
			strings.HasPrefix(lower, "qwen3") {
			addModel(domain.PlatformCommandCode, m)
			addModel(domain.PlatformOpenCode, m)
		} else {
			addModel(domain.PlatformCommandCode, m)
		}
	}
}

func enrichCapabilitiesHeuristic(item *catalogModelItemDTO) {
	lower := strings.ToLower(item.ID)
	if !item.SupportsToolCall {
		item.SupportsToolCall = true
	}
	if !item.SupportsImages {
		if strings.Contains(lower, "claude") || strings.Contains(lower, "gpt-4") || strings.Contains(lower, "gpt-5") ||
			strings.Contains(lower, "gemini") || strings.Contains(lower, "vl") || strings.Contains(lower, "kimi-k2.5") {
			item.SupportsImages = true
		}
	}
	if !item.SupportsReasoning {
		if strings.Contains(lower, "opus") || strings.Contains(lower, "sonnet-4") || strings.Contains(lower, "gpt-5") ||
			strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4") ||
			strings.Contains(lower, "gemini-2.5") || strings.Contains(lower, "gemini-3") ||
			strings.Contains(lower, "reasoner") || strings.Contains(lower, "r1") || strings.Contains(lower, "thinking") ||
			strings.Contains(lower, "glm-5") || strings.Contains(lower, "kimi-k2") || strings.Contains(lower, "qwen3") {
			item.SupportsReasoning = true
		}
	}
}

// ListModelPrices 返回全部自定义模型价格覆盖项（GET /api/models/prices）。
func (h *Handler) ListModelPrices(c *gin.Context) {
	ps := h.pricingSvc()
	if ps == nil {
		fail(c, http.StatusServiceUnavailable, "定价服务未初始化")
		return
	}

	rows, err := ps.ListOverrides(c.Request.Context())
	if err != nil {
		h.failInternal(c, "查询模型价格列表失败", err)
		return
	}

	items := make([]modelPriceDTO, 0, len(rows))
	for _, r := range rows {
		items = append(items, toModelPriceDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
	})
}

type upsertModelPriceRequest struct {
	ID              int64   `json:"id"`
	Model           string  `json:"model"`
	InputPrice      float64 `json:"input_price"`
	OutputPrice     float64 `json:"output_price"`
	CacheReadPrice  float64 `json:"cache_read_price"`
	CacheWritePrice float64 `json:"cache_write_price"`
	Notes           string  `json:"notes"`
}

// UpsertModelPrice 新增或更新自定义模型单价并立即刷新计费热缓存（PUT /api/models/prices）。
func (h *Handler) UpsertModelPrice(c *gin.Context) {
	ps := h.pricingSvc()
	if ps == nil {
		fail(c, http.StatusServiceUnavailable, "定价服务未初始化")
		return
	}

	var req upsertModelPriceRequest
	if !bindJSON(c, &req) {
		return
	}

	saved, err := ps.UpsertOverride(c.Request.Context(), service.UpsertModelPriceInput{
		ID:              req.ID,
		Model:           req.Model,
		InputPrice:      req.InputPrice,
		OutputPrice:     req.OutputPrice,
		CacheReadPrice:  req.CacheReadPrice,
		CacheWritePrice: req.CacheWritePrice,
		Notes:           req.Notes,
	})
	if err != nil {
		if errors.Is(err, service.ErrModelPriceNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, toModelPriceDTO(saved))
}

// DeleteModelPrice 删除自定义模型定价覆盖，自动回退至官方标准牌价（DELETE /api/models/prices/:id）。
func (h *Handler) DeleteModelPrice(c *gin.Context) {
	ps := h.pricingSvc()
	if ps == nil {
		fail(c, http.StatusServiceUnavailable, "定价服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	if err := ps.DeleteOverride(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrModelPriceNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		h.failInternal(c, "删除自定义模型价格失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "已删除自定义定价并恢复官方标准牌价"})
}
