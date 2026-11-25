package handler

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/apikey"
	"cmd2api/ent/usagelog"
	"cmd2api/internal/domain"

	"github.com/gin-gonic/gin"
)

// usageLogDTO 是单条用量记录的对外表示（严格对齐前端 UsageLog 接口）。
type usageLogDTO struct {
	ID        int64  `json:"id"`
	Model     string `json:"model"`
	Platform  string `json:"platform"`
	RequestID string `json:"request_id"`

	UserID      int64  `json:"user_id"`
	APIKeyID    int64  `json:"api_key_id"`
	APIKeyName  string `json:"api_key_name"`
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name"`
	GroupID     *int64 `json:"group_id"`
	GroupName   string `json:"group_name"`

	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	ReasoningTokens     int `json:"reasoning_tokens"`

	TotalCost      float64 `json:"total_cost"`
	InputCost      float64 `json:"input_cost"`
	OutputCost     float64 `json:"output_cost"`
	CacheCost      float64 `json:"cache_cost"`
	CacheHitRate   float64 `json:"cache_hit_rate"`
	TPS            float64 `json:"tps"`
	RateMultiplier float64 `json:"rate_multiplier"`

	Stream       bool    `json:"stream"`
	DurationMs   *int    `json:"duration_ms"`
	FirstTokenMs *int    `json:"first_token_ms"`
	StatusCode   int     `json:"status_code"`
	ErrorMessage *string `json:"error_message"`
	IPAddress    *string `json:"ip_address"`
	UserAgent    *string `json:"user_agent"`

	CreatedAt time.Time `json:"created_at"`
}

// ListUsageLogs 分页返回用量日志（支持按时间窗、平台、分组、模型、密钥、账号、状态码、RequestID 筛选）。
func (h *Handler) ListUsageLogs(c *gin.Context) {
	limit, offset := pageParams(c)
	ctx := c.Request.Context()

	query := h.client.UsageLog.Query()

	start, end, ok := h.parseRange(c)
	if !ok {
		return
	}
	query = query.Where(usagelog.CreatedAtGTE(start), usagelog.CreatedAtLT(end))

	if model := strings.TrimSpace(c.Query("model")); model != "" {
		query = query.Where(usagelog.ModelContainsFold(model))
	}
	if rawPlat := strings.TrimSpace(c.Query("platform")); rawPlat != "" {
		plat := domain.NormalizePlatform(rawPlat)
		query = query.Where(
			usagelog.Or(
				usagelog.PlatformEQ(plat),
				usagelog.And(
					usagelog.PlatformEQ(""),
					usagelog.HasAccountWith(account.PlatformEQ(plat)),
				),
			),
		)
	}
	if groupID := queryInt(c, "group_id", 0); groupID > 0 {
		query = query.Where(usagelog.GroupIDEQ(int64(groupID)))
	}
	if keyID := queryInt(c, "api_key_id", 0); keyID > 0 {
		query = query.Where(usagelog.APIKeyIDEQ(int64(keyID)))
	}
	if accountID := queryInt(c, "account_id", 0); accountID > 0 {
		query = query.Where(usagelog.AccountIDEQ(int64(accountID)))
	}
	if requestID := strings.TrimSpace(c.Query("request_id")); requestID != "" {
		query = query.Where(usagelog.RequestIDEQ(requestID))
	}
	switch c.Query("status") {
	case "error":
		query = query.Where(usagelog.StatusCodeGTE(400))
	case "ok":
		query = query.Where(usagelog.StatusCodeLT(400))
	}

	total, err := query.Clone().Count(ctx)
	if err != nil {
		h.failInternal(c, "统计日志条数失败", err)
		return
	}

	rows, err := query.
		Order(ent.Desc(usagelog.FieldCreatedAt)).
		Limit(limit).
		Offset(offset).
		WithAPIKey().
		WithAccount().
		WithGroup().
		All(ctx)
	if err != nil {
		h.failInternal(c, "查询用量日志失败", err)
		return
	}

	items := make([]usageLogDTO, 0, len(rows))
	for _, row := range rows {
		plat := strings.TrimSpace(row.Platform)
		if plat == "" && row.Edges.Account != nil {
			plat = row.Edges.Account.Platform
		}
		plat = domain.NormalizePlatform(plat)

		pureInputCost := math.Max(0, row.InputCost-row.CacheCost)
		cacheHitRatio := computeCacheHitRatio(int64(row.InputTokens), int64(row.CacheReadTokens), int64(row.CacheCreationTokens))
		tps := computeRowTPS(row.OutputTokens, row.DurationMs, row.FirstTokenMs)

		dto := usageLogDTO{
			ID:                  row.ID,
			Model:               row.Model,
			Platform:            plat,
			RequestID:           row.RequestID,
			UserID:              row.UserID,
			APIKeyID:            row.APIKeyID,
			AccountID:           row.AccountID,
			GroupID:             row.GroupID,
			InputTokens:         row.InputTokens,
			OutputTokens:        row.OutputTokens,
			CacheCreationTokens: row.CacheCreationTokens,
			CacheReadTokens:     row.CacheReadTokens,
			ReasoningTokens:     row.ReasoningTokens,
			TotalCost:           row.TotalCost,
			InputCost:           pureInputCost,
			OutputCost:          row.OutputCost,
			CacheCost:           row.CacheCost,
			CacheHitRate:        cacheHitRatio,
			TPS:                 tps,
			RateMultiplier:      row.RateMultiplier,
			Stream:              row.Stream,
			DurationMs:          row.DurationMs,
			FirstTokenMs:        row.FirstTokenMs,
			StatusCode:          row.StatusCode,
			ErrorMessage:        row.ErrorMessage,
			IPAddress:           row.IPAddress,
			UserAgent:           row.UserAgent,
			CreatedAt:           row.CreatedAt,
		}
		if row.Edges.APIKey != nil {
			dto.APIKeyName = row.Edges.APIKey.Name
		}
		if row.Edges.Account != nil {
			dto.AccountName = row.Edges.Account.Name
		}
		if row.Edges.Group != nil {
			dto.GroupName = row.Edges.Group.Name
		}
		items = append(items, dto)
	}

	c.JSON(http.StatusOK, gin.H{
		"items":     items,
		"total":     total,
		"page":      offset/limit + 1,
		"page_size": limit,
		"start":     start.UTC().Format(time.RFC3339),
		"end":       end.UTC().Format(time.RFC3339),
	})
}

// parseRange 解析时间范围，默认最近 24 小时。
func (h *Handler) parseRange(c *gin.Context) (time.Time, time.Time, bool) {
	now := time.Now()
	end := now
	start := now.Add(-24 * time.Hour)

	if raw := c.Query("end"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "end 需要是 RFC3339 格式")
			return time.Time{}, time.Time{}, false
		}
		end = t
	}
	if raw := c.Query("start"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "start 需要是 RFC3339 格式")
			return time.Time{}, time.Time{}, false
		}
		start = t
	}
	if !start.Before(end) {
		fail(c, http.StatusBadRequest, "start 必须早于 end")
		return time.Time{}, time.Time{}, false
	}
	if end.Sub(start) > 90*24*time.Hour {
		fail(c, http.StatusBadRequest, "时间范围不能超过 90 天")
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// dashboardSummary 对齐前端 DashboardSummary（success_rate 与 cache_hit_rate 均为 0~1 小数比率）。
type dashboardSummary struct {
	Requests            int64   `json:"requests"`
	Failed              int64   `json:"failed"`
	SuccessRate         float64 `json:"success_rate"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CacheHitRate        float64 `json:"cache_hit_rate"`
	P50FirstTokenMs     float64 `json:"p50_first_token_ms"`
	P95FirstTokenMs     float64 `json:"p95_first_token_ms"`
	AvgTPS              float64 `json:"avg_tps"`
	TotalCost           float64 `json:"total_cost"`
	InputCost           float64 `json:"input_cost"`
	OutputCost          float64 `json:"output_cost"`
	CacheCost           float64 `json:"cache_cost"`
	AvgDurationMs       float64 `json:"avg_duration_ms"`
	AvgFirstTokenMs     float64 `json:"avg_first_token_ms"`
}

// seriesPoint 是时间序列上的一个点。
type seriesPoint struct {
	Bucket              time.Time `json:"bucket"`
	Requests            int64     `json:"requests"`
	Failed              int64     `json:"failed"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	ReasoningTokens     int64     `json:"reasoning_tokens"`
	AvgFirstTokenMs     float64   `json:"avg_first_token_ms"`
	AvgTPS              float64   `json:"avg_tps"`
	TotalCost           float64   `json:"total_cost"`
}

// breakdownItem 对齐前端 BreakdownItem 结构。
type breakdownItem struct {
	Key                 string  `json:"key"`
	Label               string  `json:"label"`
	Requests            int64   `json:"requests"`
	Failed              int64   `json:"failed"`
	Tokens              int64   `json:"tokens"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CacheHitRate        float64 `json:"cache_hit_rate"`
	TotalCost           float64 `json:"total_cost"`
	AvgFirstTokenMs     float64 `json:"avg_first_token_ms"`
	P95FirstTokenMs     float64 `json:"p95_first_token_ms"`
	AvgDurationMs       float64 `json:"avg_duration_ms"`
	AvgTPS              float64 `json:"avg_tps"`
	SuccessRate         float64 `json:"success_rate"`
}

type metricAccumulator struct {
	key                 string
	label               string
	requests            int64
	failed              int64
	inputTokens         int64
	outputTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64
	reasoningTokens     int64
	totalCost           float64
	inputCost           float64
	outputCost          float64
	cacheCost           float64
	durationSum         int64
	durationCount       int64
	firstTokenList      []int
	firstTokenSum       int64
	tpsSum              float64
	tpsCount            int64
}

func (a *metricAccumulator) add(row *ent.UsageLog) {
	a.requests++
	if row.StatusCode >= 400 {
		a.failed++
	}
	a.inputTokens += int64(row.InputTokens)
	a.outputTokens += int64(row.OutputTokens)
	a.cacheReadTokens += int64(row.CacheReadTokens)
	a.cacheCreationTokens += int64(row.CacheCreationTokens)
	a.reasoningTokens += int64(row.ReasoningTokens)

	pureInput := math.Max(0, row.InputCost-row.CacheCost)
	a.totalCost += row.TotalCost
	a.inputCost += pureInput
	a.outputCost += row.OutputCost
	a.cacheCost += row.CacheCost

	if row.DurationMs != nil && *row.DurationMs >= 0 {
		a.durationSum += int64(*row.DurationMs)
		a.durationCount++
	}
	if row.FirstTokenMs != nil && *row.FirstTokenMs >= 0 {
		a.firstTokenList = append(a.firstTokenList, *row.FirstTokenMs)
		a.firstTokenSum += int64(*row.FirstTokenMs)
	}
	if tps := computeRowTPS(row.OutputTokens, row.DurationMs, row.FirstTokenMs); tps > 0 {
		a.tpsSum += tps
		a.tpsCount++
	}
}

func (a *metricAccumulator) toBreakdown() breakdownItem {
	var successRate float64
	if a.requests > 0 {
		successRate = float64(a.requests-a.failed) / float64(a.requests)
	}
	var avgDur float64
	if a.durationCount > 0 {
		avgDur = float64(a.durationSum) / float64(a.durationCount)
	}
	var avgFT, p95FT float64
	if len(a.firstTokenList) > 0 {
		avgFT = float64(a.firstTokenSum) / float64(len(a.firstTokenList))
		sorted := append([]int(nil), a.firstTokenList...)
		sort.Ints(sorted)
		p95FT = percentileSortedInts(sorted, 0.95)
	}
	var avgTPS float64
	if a.tpsCount > 0 {
		avgTPS = a.tpsSum / float64(a.tpsCount)
	}
	return breakdownItem{
		Key:                 a.key,
		Label:               a.label,
		Requests:            a.requests,
		Failed:              a.failed,
		Tokens:              a.inputTokens + a.outputTokens + a.cacheReadTokens + a.cacheCreationTokens,
		InputTokens:         a.inputTokens,
		OutputTokens:        a.outputTokens,
		CacheReadTokens:     a.cacheReadTokens,
		CacheCreationTokens: a.cacheCreationTokens,
		ReasoningTokens:     a.reasoningTokens,
		CacheHitRate:        computeCacheHitRatio(a.inputTokens, a.cacheReadTokens, a.cacheCreationTokens),
		TotalCost:           a.totalCost,
		AvgFirstTokenMs:     avgFT,
		P95FirstTokenMs:     p95FT,
		AvgDurationMs:       avgDur,
		AvgTPS:              avgTPS,
		SuccessRate:         successRate,
	}
}

func (a *metricAccumulator) toSummary() dashboardSummary {
	bd := a.toBreakdown()
	var p50FT float64
	if len(a.firstTokenList) > 0 {
		sorted := append([]int(nil), a.firstTokenList...)
		sort.Ints(sorted)
		p50FT = percentileSortedInts(sorted, 0.50)
	}
	return dashboardSummary{
		Requests:            bd.Requests,
		Failed:              bd.Failed,
		SuccessRate:         bd.SuccessRate,
		InputTokens:         bd.InputTokens,
		OutputTokens:        bd.OutputTokens,
		CacheReadTokens:     bd.CacheReadTokens,
		CacheCreationTokens: bd.CacheCreationTokens,
		ReasoningTokens:     bd.ReasoningTokens,
		CacheHitRate:        bd.CacheHitRate,
		P50FirstTokenMs:     p50FT,
		P95FirstTokenMs:     bd.P95FirstTokenMs,
		AvgTPS:              bd.AvgTPS,
		TotalCost:           bd.TotalCost,
		InputCost:           a.inputCost,
		OutputCost:          a.outputCost,
		CacheCost:           a.cacheCost,
		AvgDurationMs:       bd.AvgDurationMs,
		AvgFirstTokenMs:     bd.AvgFirstTokenMs,
	}
}

// Dashboard 返回仪表盘所需的全部深度指标（SQLite 与 PostgreSQL 完全同构计算）。
func (h *Handler) Dashboard(c *gin.Context) {
	start, end, ok := h.parseRange(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	bucket := "hour"
	if end.Sub(start) > 48*time.Hour {
		bucket = "day"
	}

	summary, series, models, accounts, byPlat, byGrp, err := h.computeDashboardAggregates(ctx, start, end, bucket)
	if err != nil {
		h.failInternal(c, "统计监控看板失败", err)
		return
	}

	accountTotal, _ := h.client.Account.Query().Where(account.DeletedAtIsNil()).Count(ctx)
	accountActive, _ := h.client.Account.Query().Where(account.DeletedAtIsNil(), account.StatusEQ(domain.StatusActive)).Count(ctx)
	keyTotal, _ := h.client.APIKey.Query().Where(apikey.DeletedAtIsNil()).Count(ctx)

	nodesTotal, _ := h.client.ContainerNode.Query().Count(ctx)
	nodesRunning := 0
	if nodeRows, nErr := h.client.ContainerNode.Query().All(ctx); nErr == nil {
		for _, n := range nodeRows {
			if n.Status == "running" || n.Status == "online" {
				nodesRunning++
			}
		}
	}

	accountsByPlatform := map[string]int{
		domain.PlatformWorkBuddy:   0,
		domain.PlatformAntigravity: 0,
		domain.PlatformCommandCode: 0,
		domain.PlatformOpenCode:    0,
	}
	if accRows, aErr := h.client.Account.Query().Where(account.DeletedAtIsNil()).All(ctx); aErr == nil {
		for _, acc := range accRows {
			p := domain.NormalizePlatform(acc.Platform)
			accountsByPlatform[p]++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"summary":     summary,
		"series":      series,
		"models":      models,
		"by_model":    models,
		"accounts":    accounts,
		"by_platform": byPlat,
		"by_group":    byGrp,
		"runtime": gin.H{
			"accounts_total":          accountTotal,
			"accounts_active":         accountActive,
			"api_keys_total":          keyTotal,
			"container_nodes_total":   nodesTotal,
			"container_nodes_running": nodesRunning,
			"accounts_by_platform":    accountsByPlatform,
			"bucket":                  bucket,
		},
		"start": start.UTC().Format(time.RFC3339),
		"end":   end.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) computeDashboardAggregates(
	ctx context.Context,
	start, end time.Time,
	bucket string,
) (
	summary dashboardSummary,
	series []seriesPoint,
	models []breakdownItem,
	accounts []breakdownItem,
	byPlatform []breakdownItem,
	byGroup []breakdownItem,
	err error,
) {
	rows, err := h.client.UsageLog.Query().
		Where(usagelog.CreatedAtGTE(start), usagelog.CreatedAtLT(end)).
		WithAccount().
		WithGroup().
		Order(ent.Asc(usagelog.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return summary, nil, nil, nil, nil, nil, err
	}

	var totalAcc metricAccumulator
	type seriesBucketAcc struct {
		pt      seriesPoint
		ftSum   int64
		ftCount int64
		tpsSum  float64
		tpsCnt  int64
	}
	bucketMap := make(map[int64]*seriesBucketAcc)
	modelMap := make(map[string]*metricAccumulator)
	accountMap := make(map[int64]*metricAccumulator)
	platformMap := make(map[string]*metricAccumulator)
	groupMap := make(map[string]*metricAccumulator)

	for _, row := range rows {
		totalAcc.add(row)

		// 1. Series bucket
		ts := row.CreatedAt.UTC()
		var truncated time.Time
		if bucket == "day" {
			truncated = time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
		} else {
			truncated = ts.Truncate(time.Hour)
		}
		bKey := truncated.Unix()
		sb, ok := bucketMap[bKey]
		if !ok {
			sb = &seriesBucketAcc{pt: seriesPoint{Bucket: truncated}}
			bucketMap[bKey] = sb
		}
		sb.pt.Requests++
		if row.StatusCode >= 400 {
			sb.pt.Failed++
		}
		sb.pt.InputTokens += int64(row.InputTokens)
		sb.pt.OutputTokens += int64(row.OutputTokens)
		sb.pt.CacheReadTokens += int64(row.CacheReadTokens)
		sb.pt.CacheCreationTokens += int64(row.CacheCreationTokens)
		sb.pt.ReasoningTokens += int64(row.ReasoningTokens)
		sb.pt.TotalCost += row.TotalCost
		if row.FirstTokenMs != nil && *row.FirstTokenMs >= 0 {
			sb.ftSum += int64(*row.FirstTokenMs)
			sb.ftCount++
		}
		if tps := computeRowTPS(row.OutputTokens, row.DurationMs, row.FirstTokenMs); tps > 0 {
			sb.tpsSum += tps
			sb.tpsCnt++
		}

		// 2. By Model
		mAcc, exists := modelMap[row.Model]
		if !exists {
			mAcc = &metricAccumulator{key: row.Model, label: row.Model}
			modelMap[row.Model] = mAcc
		}
		mAcc.add(row)

		// 3. By Account
		aAcc, exists := accountMap[row.AccountID]
		if !exists {
			accLabel := "[已删除]"
			if row.Edges.Account != nil && row.Edges.Account.Name != "" {
				accLabel = row.Edges.Account.Name
			}
			aAcc = &metricAccumulator{
				key:   strconv.FormatInt(row.AccountID, 10),
				label: accLabel,
			}
			accountMap[row.AccountID] = aAcc
		}
		aAcc.add(row)

		// 4. By Platform
		plat := strings.TrimSpace(row.Platform)
		if plat == "" && row.Edges.Account != nil {
			plat = row.Edges.Account.Platform
		}
		plat = domain.NormalizePlatform(plat)
		pAcc, exists := platformMap[plat]
		if !exists {
			pAcc = &metricAccumulator{
				key:   plat,
				label: platformLabel(plat),
			}
			platformMap[plat] = pAcc
		}
		pAcc.add(row)

		// 5. By Group
		grpKey := "0"
		grpLabel := "未分组"
		if row.GroupID != nil && *row.GroupID > 0 {
			grpKey = strconv.FormatInt(*row.GroupID, 10)
			if row.Edges.Group != nil && row.Edges.Group.Name != "" {
				grpLabel = row.Edges.Group.Name
			} else {
				grpLabel = "分组 #" + grpKey
			}
		}
		gAcc, exists := groupMap[grpKey]
		if !exists {
			gAcc = &metricAccumulator{
				key:   grpKey,
				label: grpLabel,
			}
			groupMap[grpKey] = gAcc
		}
		gAcc.add(row)
	}

	summary = totalAcc.toSummary()

	series = make([]seriesPoint, 0, len(bucketMap))
	for _, sb := range bucketMap {
		if sb.ftCount > 0 {
			sb.pt.AvgFirstTokenMs = float64(sb.ftSum) / float64(sb.ftCount)
		}
		if sb.tpsCnt > 0 {
			sb.pt.AvgTPS = sb.tpsSum / float64(sb.tpsCnt)
		}
		series = append(series, sb.pt)
	}
	sort.Slice(series, func(i, j int) bool {
		return series[i].Bucket.Before(series[j].Bucket)
	})

	models = finalizeBreakdownList(modelMap, 20)
	accounts = finalizeAccountBreakdownList(accountMap, 20)
	byPlatform = finalizeBreakdownList(platformMap, 10)
	byGroup = finalizeBreakdownList(groupMap, 20)

	return summary, series, models, accounts, byPlatform, byGroup, nil
}

func finalizeBreakdownList(m map[string]*metricAccumulator, limit int) []breakdownItem {
	items := make([]breakdownItem, 0, len(m))
	for _, acc := range m {
		items = append(items, acc.toBreakdown())
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Requests != items[j].Requests {
			return items[i].Requests > items[j].Requests
		}
		return items[i].Key < items[j].Key
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func finalizeAccountBreakdownList(m map[int64]*metricAccumulator, limit int) []breakdownItem {
	items := make([]breakdownItem, 0, len(m))
	for _, acc := range m {
		items = append(items, acc.toBreakdown())
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Requests != items[j].Requests {
			return items[i].Requests > items[j].Requests
		}
		return items[i].Key < items[j].Key
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func computeCacheHitRatio(inputTokens, cacheReadTokens, cacheCreationTokens int64) float64 {
	denom := inputTokens + cacheReadTokens + cacheCreationTokens
	if denom <= 0 || cacheReadTokens <= 0 {
		return 0
	}
	r := float64(cacheReadTokens) / float64(denom)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

func computeRowTPS(outputTokens int, durationMs *int, firstTokenMs *int) float64 {
	if outputTokens <= 0 || durationMs == nil || *durationMs <= 0 {
		return 0
	}
	genMs := *durationMs
	if firstTokenMs != nil && *firstTokenMs > 0 && *durationMs > *firstTokenMs {
		genMs = *durationMs - *firstTokenMs
	}
	if genMs <= 0 {
		genMs = *durationMs
	}
	if genMs <= 0 {
		return 0
	}
	return float64(outputTokens) / (float64(genMs) / 1000.0)
}

func percentileSortedInts(sorted []int, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return float64(sorted[0])
	}
	pos := p * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo < 0 {
		lo = 0
	}
	if hi >= n {
		hi = n - 1
	}
	if lo == hi {
		return float64(sorted[lo])
	}
	frac := pos - float64(lo)
	return float64(sorted[lo])*(1-frac) + float64(sorted[hi])*frac
}
