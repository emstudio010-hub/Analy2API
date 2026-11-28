package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/group"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/service"
	"cmd2api/internal/workbuddy"

	"github.com/gin-gonic/gin"
)

// accountDTO 是账号的对外表示。
//
// 绝不含明文密钥——列表页只需要让管理员认出「是哪一把」，用 mask 后的形式。
type accountDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Notes    string `json:"notes"`
	Platform string `json:"platform"`
	Type     string `json:"type"`
	Status   string `json:"status"`

	// AccountMode / BaseURL 只有 OpenCode 账号有值。
	AccountMode string `json:"account_mode"`
	BaseURL     string `json:"base_url"`

	// WorkBuddy 账号专属凭证上下文与风控指纹（脱敏展示代理口令）。
	ProxyURL           string         `json:"proxy_url"`
	Qimei36            string         `json:"qimei36"`
	MachineID          string         `json:"machine_id"`
	UID                string         `json:"uid"`
	Nickname           string         `json:"nickname"`
	Email              string         `json:"email"`
	EnterpriseID       string         `json:"enterprise_id"`
	EnterpriseName     string         `json:"enterprise_name"`
	Domain             string         `json:"domain"`
	LastCheckinDate    string         `json:"last_checkin_date"`
	CheckinStreak      int            `json:"checkin_streak"`
	LastHarvestAt      *string        `json:"last_harvest_at"`
	LastHarvestSummary map[string]any `json:"last_harvest_summary"`

	ErrorMessage *string `json:"error_message"`
	MaskedKey    string  `json:"masked_key"`

	Concurrency    int     `json:"concurrency"`
	Priority       int     `json:"priority"`
	RateMultiplier float64 `json:"rate_multiplier"`
	Schedulable    bool    `json:"schedulable"`

	ConsecutiveFailures  int     `json:"consecutive_failures"`
	LastUsedAt           *string `json:"last_used_at"`
	ExpiresAt            *string `json:"expires_at"`
	LastHealthCheckAt    *string `json:"last_health_check_at"`
	LastHealthCheckOk    bool    `json:"last_health_check_ok"`
	LastHealthCheckError *string `json:"last_health_check_error"`
	LatencyMs            *int    `json:"latency_ms"`

	GroupIDs []int64        `json:"group_ids"`
	NodeID   int64          `json:"node_id,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`

	Balance accountBalanceDTO `json:"balance"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// accountBalanceDTO 是账号余额快照的对外表示。
//
// 全部字段都可能为空：账号可能从没刷过余额，平台也可能根本不支持查。
type accountBalanceDTO struct {
	// Supported 为 false 表示这个上游平台查不了余额（目前是 OpenCode）。
	// 与「查过但失败」是两回事，界面上的文案也不同。
	Supported bool `json:"supported"`
	// FetchedAt 是最近一次**成功**取到余额的时间。刷新失败不会推进它，
	// 所以它始终代表「界面上的数字是什么时候取的」。
	FetchedAt *string `json:"fetched_at"`
	// Error 是最近一次刷新失败的原因；成功时为 null。
	Error *string `json:"error"`
	// Remaining 是当前还能花的总额度（美元或积分兜底）。
	Remaining *float64 `json:"remaining"`
	// Total 是套餐的月度总额，查不到该套餐时为 null。
	Total *float64 `json:"total"`
	// TotalCredits / Packages 为 WorkBuddy 积分与资源包明细。
	TotalCredits *float64         `json:"total_credits"`
	Packages     []map[string]any `json:"packages"`
	// PlanID / PlanName 是套餐标识与展示名。
	PlanID   string `json:"plan_id"`
	PlanName string `json:"plan_name"`
	// PeriodEnd 是当前计费周期的结束时间。
	PeriodEnd *string `json:"period_end"`

	FiveHour *balanceWindowDTO `json:"five_hour"`
	Weekly   *balanceWindowDTO `json:"weekly"`
}

// balanceWindowDTO 是一个滚动额度窗口。
type balanceWindowDTO struct {
	Used     float64 `json:"used"`
	Cap      float64 `json:"cap"`
	Exceeded bool    `json:"exceeded"`
	ResetAt  *string `json:"reset_at"`
}

func (h *Handler) toAccountDTO(acc *ent.Account) accountDTO {
	platform := domain.NormalizePlatform(acc.Platform)
	qimei36, machineID := workbuddy.EnsureDevice(acc.Qimei36, acc.MachineID)
	dom := strings.TrimSpace(acc.Domain)
	if dom == "" {
		dom = domain.WorkBuddyDefaultDomain
	}
	dto := accountDTO{
		ID:                   acc.ID,
		Name:                 acc.Name,
		Platform:             platform,
		Type:                 acc.Type,
		Status:               acc.Status,
		ProxyURL:             workbuddy.RedactProxyURL(acc.ProxyURL),
		Qimei36:              qimei36,
		MachineID:            machineID,
		UID:                  acc.UID,
		Nickname:             acc.Nickname,
		Email:                acc.Email,
		EnterpriseID:         acc.EnterpriseID,
		EnterpriseName:       acc.EnterpriseName,
		Domain:               dom,
		LastCheckinDate:      acc.LastCheckinDate,
		CheckinStreak:        acc.CheckinStreak,
		LastHarvestAt:        timePtr(acc.LastHarvestAt),
		LastHarvestSummary:   acc.LastHarvestSummary,
		ErrorMessage:         acc.ErrorMessage,
		Concurrency:          acc.Concurrency,
		Priority:             acc.Priority,
		RateMultiplier:       acc.RateMultiplier,
		Schedulable:          acc.Schedulable,
		ConsecutiveFailures:  acc.ConsecutiveFailures,
		LastUsedAt:           timePtr(acc.LastUsedAt),
		ExpiresAt:            timePtr(acc.ExpiresAt),
		LastHealthCheckAt:    timePtr(acc.LastHealthCheckAt),
		LastHealthCheckOk:    acc.LastHealthCheckOk,
		LastHealthCheckError: acc.LastHealthCheckError,
		LatencyMs:            acc.LatencyMs,
		GroupIDs:             []int64{},
		Balance:              toBalanceDTO(acc),
		CreatedAt:            acc.CreatedAt,
		UpdatedAt:            acc.UpdatedAt,
	}
	if acc.Notes != nil {
		dto.Notes = *acc.Notes
	}
	if h.accounts != nil {
		dto.MaskedKey = h.accounts.MaskedKey(acc)
	}
	// 从加载好的边里取分组 ID。调用方负责 WithGroups()；
	// 没加载时这里是空数组，而不是接口漏字段。
	for _, g := range acc.Edges.Groups {
		dto.GroupIDs = append(dto.GroupIDs, g.ID)
	}
	// account_mode / base_url 存在 extra JSONB 里，取出来平铺到顶层，
	// 前端不必知道它是怎么存的。
	if acc.Extra != nil {
		dto.AccountMode, _ = acc.Extra["account_mode"].(string)
		dto.BaseURL, _ = acc.Extra["base_url"].(string)
		dto.Extra = acc.Extra
		switch v := acc.Extra["node_id"].(type) {
		case int64:
			dto.NodeID = v
		case int:
			dto.NodeID = int64(v)
		case float64:
			dto.NodeID = int64(v)
		}
	}
	if dto.BaseURL == "" {
		switch dto.Platform {
		case domain.PlatformOpenCode:
			dto.BaseURL = domain.DefaultOpenCodeBaseURL(dto.AccountMode)
		case domain.PlatformAntigravity:
			if dom != "" && dom != domain.WorkBuddyDefaultDomain {
				dto.BaseURL = dom
			}
		}
	}
	return dto
}

// toBalanceDTO 把账号上的余额快照整理成嵌套对象。
//
// 库里存的是扁平列（balance_5h_used 之类），这里收成嵌套结构再给前端，
// 免得前端去记一堆前缀。收拢放在这一层，将来改存储结构也不用动界面。
func toBalanceDTO(acc *ent.Account) accountBalanceDTO {
	platform := domain.NormalizePlatform(acc.Platform)
	totalCredits := acc.BalanceTotalCredits
	if totalCredits == nil && acc.BalanceRemaining != nil {
		v := *acc.BalanceRemaining
		totalCredits = &v
	}
	packages := make([]map[string]any, 0, len(acc.BalancePackages))
	for _, pkg := range acc.BalancePackages {
		packages = append(packages, pkg)
	}
	dto := accountBalanceDTO{
		// WorkBuddy、AntiGravity 与 Command Code 均支持余额/配额快照查询。
		Supported: platform == domain.PlatformWorkBuddy || platform == domain.PlatformAntigravity || platform == domain.PlatformCommandCode,
		FetchedAt:    timePtr(acc.BalanceFetchedAt),
		Error:        acc.BalanceError,
		Remaining:    acc.BalanceRemaining,
		TotalCredits: totalCredits,
		Packages:     packages,
		PeriodEnd:    timePtr(acc.BalancePeriodEnd),
		FiveHour:     toBalanceWindowDTO(acc.Balance5hUsed, acc.Balance5hCap, acc.Balance5hResetAt),
		Weekly:       toBalanceWindowDTO(acc.BalanceWeeklyUsed, acc.BalanceWeeklyCap, acc.BalanceWeeklyResetAt),
	}
	if acc.BalancePlanID != nil {
		dto.PlanID = *acc.BalancePlanID
		dto.PlanName = relay.PlanName(dto.PlanID)
		// 套餐总额来自内置表，认不出来就留空。界面上少一行百分比，
		// 好过拿一个猜的分母去算。
		if total, ok := relay.PlanCredits(dto.PlanID); ok {
			dto.Total = &total
		}
	}
	return dto
}

// toBalanceWindowDTO 组装一个额度窗口。
//
// 用量和上限缺任何一个都返回 nil：只有一个数字的窗口没法解读，
// 界面显示「$0.02 / —」还不如不显示。
func toBalanceWindowDTO(used, capacity *float64, resetAt *time.Time) *balanceWindowDTO {
	if used == nil || capacity == nil {
		return nil
	}
	return &balanceWindowDTO{
		Used: *used,
		Cap:  *capacity,
		// exceeded 不单独存列：它就是「用量到了上限」，存下来反而多一个
		// 可能和另两个字段对不上的状态。cap 为 0 时不算超——那是「没有上限」
		// 而不是「上限为零」，后者会让每个 0/0 的窗口都标红。
		Exceeded: *capacity > 0 && *used >= *capacity,
		ResetAt:  timePtr(resetAt),
	}
}

// ListAccounts 分页返回账号列表。
func (h *Handler) ListAccounts(c *gin.Context) {
	limit, offset := pageParams(c)
	ctx := c.Request.Context()

	query := h.client.Account.Query().Where(account.DeletedAtIsNil())

	if status := c.Query("status"); status != "" {
		query = query.Where(account.StatusEQ(status))
	}
	if platform := strings.TrimSpace(c.Query("platform")); platform != "" {
		normalized := domain.NormalizePlatform(platform)
		if normalized == domain.PlatformWorkBuddy {
			query = query.Where(account.Or(
				account.PlatformEQ(normalized),
				account.PlatformEQ(""),
			))
		} else {
			query = query.Where(account.PlatformEQ(normalized))
		}
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		query = query.Where(account.Or(
			account.NameContainsFold(keyword),
			account.NicknameContainsFold(keyword),
			account.UIDContainsFold(keyword),
			account.EmailContainsFold(keyword),
			account.ProxyURLContainsFold(keyword),
		))
	}
	if groupID := queryInt(c, "group_id", 0); groupID > 0 {
		query = query.Where(account.HasGroupsWith(group.IDEQ(int64(groupID))))
	}

	total, err := query.Clone().Count(ctx)
	if err != nil {
		h.failInternal(c, "统计账号数量失败", err)
		return
	}

	rows, err := query.
		WithGroups().
		Order(ent.Asc(account.FieldPriority), ent.Desc(account.FieldID)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		h.failInternal(c, "查询账号列表失败", err)
		return
	}

	items := make([]accountDTO, 0, len(rows))
	for _, acc := range rows {
		items = append(items, h.toAccountDTO(acc))
	}

	c.JSON(http.StatusOK, gin.H{
		"items":     items,
		"total":     total,
		"page_size": limit,
		"page":      offset/limit + 1,
	})
}

// GetAccount 返回单个账号详情。
func (h *Handler) GetAccount(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	acc, err := h.client.Account.Query().
		Where(account.IDEQ(id), account.DeletedAtIsNil()).
		WithGroups().
		Only(c.Request.Context())
	if err != nil {
		if isNotFound(err) {
			fail(c, http.StatusNotFound, "账号不存在")
			return
		}
		h.failInternal(c, "查询账号失败", err)
		return
	}

	c.JSON(http.StatusOK, h.toAccountDTO(acc))
}

type createAccountRequest struct {
	Name string `json:"name" binding:"required"`
	// Platform 留空默认 workbuddy，兼容保留 commandcode 与 opencode。
	Platform       string  `json:"platform"`
	AccountMode    string  `json:"account_mode"`
	BaseURL        string  `json:"base_url"`
	Notes          string  `json:"notes"`
	APIKey         string  `json:"api_key" binding:"required"`
	RefreshToken   string  `json:"refresh_token"`
	UID            string  `json:"uid"`
	Nickname       string  `json:"nickname"`
	Email          string  `json:"email"`
	EnterpriseID   string  `json:"enterprise_id"`
	EnterpriseName string  `json:"enterprise_name"`
	Domain         string  `json:"domain"`
	ProxyURL       string  `json:"proxy_url"`
	Qimei36        string  `json:"qimei36"`
	MachineID      string  `json:"machine_id"`
	Status         string  `json:"status"`
	Schedulable    *bool   `json:"schedulable"`
	Concurrency    int     `json:"concurrency"`
	Priority       int     `json:"priority"`
	RateMultiplier float64 `json:"rate_multiplier"`
	GroupIDs       []int64 `json:"group_ids"`
	ExpiresAt      *string `json:"expires_at"`
}

// CreateAccount 新建账号。
func (h *Handler) CreateAccount(c *gin.Context) {
	var req createAccountRequest
	if !bindJSON(c, &req) {
		return
	}
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}

	expiresAt, ok := parseOptionalTime(c, req.ExpiresAt)
	if !ok {
		return
	}

	acc, err := h.accounts.Create(c.Request.Context(), service.CreateAccountInput{
		Name:           req.Name,
		Platform:       req.Platform,
		AccountMode:    req.AccountMode,
		BaseURL:        req.BaseURL,
		Notes:          req.Notes,
		APIKey:         req.APIKey,
		RefreshToken:   req.RefreshToken,
		UID:            req.UID,
		Nickname:       req.Nickname,
		Email:          req.Email,
		EnterpriseID:   req.EnterpriseID,
		EnterpriseName: req.EnterpriseName,
		Domain:         req.Domain,
		ProxyURL:       req.ProxyURL,
		Qimei36:        req.Qimei36,
		MachineID:      req.MachineID,
		Status:         req.Status,
		Schedulable:    req.Schedulable,
		Concurrency:    req.Concurrency,
		Priority:       req.Priority,
		RateMultiplier: req.RateMultiplier,
		GroupIDs:       req.GroupIDs,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		h.writeAccountError(c, err, "创建账号失败")
		return
	}
	c.JSON(http.StatusCreated, h.toAccountDTO(acc))
}

type updateAccountRequest struct {
	Name *string `json:"name"`
	// Platform 刻意不接受：它决定凭证格式与上游协议，改了等于换账号。
	Notes          *string  `json:"notes"`
	AccountMode    *string  `json:"account_mode"`
	BaseURL        *string  `json:"base_url"`
	APIKey         *string  `json:"api_key"`
	RefreshToken   *string  `json:"refresh_token"`
	UID            *string  `json:"uid"`
	Nickname       *string  `json:"nickname"`
	Email          *string  `json:"email"`
	EnterpriseID   *string  `json:"enterprise_id"`
	EnterpriseName *string  `json:"enterprise_name"`
	Domain         *string  `json:"domain"`
	ProxyURL       *string  `json:"proxy_url"`
	Qimei36        *string  `json:"qimei36"`
	MachineID      *string  `json:"machine_id"`
	Concurrency    *int     `json:"concurrency"`
	Priority       *int     `json:"priority"`
	RateMultiplier *float64 `json:"rate_multiplier"`
	Status         *string  `json:"status"`
	Schedulable    *bool    `json:"schedulable"`
	GroupIDs       *[]int64 `json:"group_ids"`
	ExpiresAt      *string  `json:"expires_at"`
}

// UpdateAccount 修改账号。
func (h *Handler) UpdateAccount(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req updateAccountRequest
	if !bindJSON(c, &req) {
		return
	}
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}

	expiresAt, ok := parseOptionalTime(c, req.ExpiresAt)
	if !ok {
		return
	}

	acc, err := h.accounts.Update(c.Request.Context(), id, service.UpdateAccountInput{
		Name:           req.Name,
		Notes:          req.Notes,
		APIKey:         req.APIKey,
		RefreshToken:   req.RefreshToken,
		UID:            req.UID,
		Nickname:       req.Nickname,
		Email:          req.Email,
		EnterpriseID:   req.EnterpriseID,
		EnterpriseName: req.EnterpriseName,
		Domain:         req.Domain,
		ProxyURL:       req.ProxyURL,
		Qimei36:        req.Qimei36,
		MachineID:      req.MachineID,
		AccountMode:    req.AccountMode,
		BaseURL:        req.BaseURL,
		Concurrency:    req.Concurrency,
		Priority:       req.Priority,
		RateMultiplier: req.RateMultiplier,
		Status:         req.Status,
		Schedulable:    req.Schedulable,
		GroupIDs:       req.GroupIDs,
		ExpiresAt:      expiresAt,
		// 传了空字符串视为「清除过期时间」，这是唯一能让字段从有值变空的途径。
		ClearExpiry: req.ExpiresAt != nil && *req.ExpiresAt == "",
	})
	if err != nil {
		h.writeAccountError(c, err, "更新账号失败")
		return
	}
	c.JSON(http.StatusOK, h.toAccountDTO(acc))
}

// DeleteAccount 删除账号。
func (h *Handler) DeleteAccount(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	if err := h.accounts.Delete(c.Request.Context(), id); err != nil {
		if isNotFound(err) {
			fail(c, http.StatusNotFound, "账号不存在")
			return
		}
		h.failInternal(c, "删除账号失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// CheckAccount 探活单个账号。
//
// 这个接口会真的向上游发一次极小请求（消耗几十个 token），
// 但它是唯一能确认「账号现在到底能不能干活」的方式。
func (h *Handler) CheckAccount(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}

	result, err := h.accounts.Check(c.Request.Context(), id)
	if err != nil {
		if isNotFound(err) {
			fail(c, http.StatusNotFound, "账号不存在")
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	// 探活失败不算 HTTP 错误：请求本身成功了，只是探测结论是「账号不可用」。
	c.JSON(http.StatusOK, gin.H{
		"ok":         result.Err == nil,
		"latency_ms": result.Latency.Milliseconds(),
		"error":      result.ErrorMessage(),
	})
}

// RefreshAccountBalance 刷新账号余额，不做探活。
//
// 和「测试连接」分开是有意的：那个会真的发一次生成请求、消耗 token，
// 这个只打两个只读接口，不花额度，所以可以做成随便点的按钮。
func (h *Handler) RefreshAccountBalance(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}

	result, err := h.accounts.RefreshBalance(c.Request.Context(), id)
	if err != nil {
		if isNotFound(err) {
			fail(c, http.StatusNotFound, "账号不存在")
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if !result.Supported {
		fail(c, http.StatusBadRequest, "该平台暂不支持查询余额")
		return
	}
	if result.Err != nil {
		// 刷新失败不算 HTTP 错误：请求本身成功了，只是没拿到数据。
		// 用 200 + ok:false，跟探活接口保持一致。
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": result.Err.Error()})
		return
	}

	// 重新读一次再返回，让界面能就地更新那一行，不用再拉一遍整个列表。
	acc, err := h.client.Account.Query().
		Where(account.IDEQ(id), account.DeletedAtIsNil()).
		WithGroups().
		Only(c.Request.Context())
	if err != nil {
		h.failInternal(c, "读取账号失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "account": h.toAccountDTO(acc)})
}

// RefreshAllAccountBalances 一键刷新**所有**账号的余额。
//
// 和单账号刷新一样只打上游的只读接口，不消耗生成额度，所以可以随便点。
// 逐个失败不影响其余账号：每个账号的结果单独返回，界面按账号报错。
func (h *Handler) RefreshAllAccountBalances(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}

	items := h.accounts.RefreshAllBalances(c.Request.Context())

	var refreshed, unsupported, failed int
	for _, it := range items {
		switch {
		case it.Error != "":
			failed++
		case !it.Supported:
			unsupported++
		default:
			refreshed++
		}
	}

	// 回带刷新后的整张账号表，界面直接替换即可，不用再拉一遍列表。
	accs, err := h.client.Account.Query().
		Where(account.DeletedAtIsNil()).
		WithGroups().
		Order(ent.Asc(account.FieldID)).
		All(c.Request.Context())
	if err != nil {
		h.failInternal(c, "读取账号列表失败", err)
		return
	}
	dtos := make([]accountDTO, 0, len(accs))
	for _, acc := range accs {
		dtos = append(dtos, h.toAccountDTO(acc))
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"total":       len(items),
		"refreshed":   refreshed,
		"unsupported": unsupported,
		"failed":      failed,
		"items":       items,
		"accounts":    dtos,
	})
}

type batchImportRequest struct {
	Keys        string  `json:"keys" binding:"required"`
	Platform    string  `json:"platform"`
	Domain      string  `json:"domain"`
	ProxyURL    string  `json:"proxy_url"`
	AccountMode string  `json:"account_mode"`
	BaseURL     string  `json:"base_url"`
	GroupIDs    []int64 `json:"group_ids"`
	Concurrency int     `json:"concurrency"`
	Priority    int     `json:"priority"`
	Upsert      *bool   `json:"upsert"`
}

// BatchImportAccounts 批量导入/更新账号（支持文本、JSON、agent-switch、workbuddy-sdk 格式）。
func (h *Handler) BatchImportAccounts(c *gin.Context) {
	var req batchImportRequest
	if !bindJSON(c, &req) {
		return
	}
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}

	upsert := true
	if req.Upsert != nil {
		upsert = *req.Upsert
	}

	out, err := h.accounts.BatchImport(c.Request.Context(), service.BatchImportInput{
		Keys:        req.Keys,
		Platform:    req.Platform,
		Domain:      req.Domain,
		ProxyURL:    req.ProxyURL,
		AccountMode: req.AccountMode,
		BaseURL:     req.BaseURL,
		GroupIDs:    req.GroupIDs,
		Concurrency: req.Concurrency,
		Priority:    req.Priority,
		Upsert:      upsert,
	})
	if err != nil {
		h.writeAccountError(c, err, "批量导入账号失败")
		return
	}
	c.JSON(http.StatusOK, out)
}

// accountKeyLine 是批量导入里解析出的一条。
type accountKeyLine struct {
	Name string
	Key  string
}

// ShortKey 返回用于回显的脱敏形式。
func (l accountKeyLine) ShortKey() string {
	if len(l.Key) <= 8 {
		return "****"
	}
	return l.Key[:8] + "…"
}

// parseKeyLines 解析批量导入的文本。
//
// 认两种输入：
//
//  1. 密钥列表：每行一条，支持 "名称,密钥" 和只有密钥两种写法，空行与 # 注释忽略。
//  2. Command Code CLI 的授权文件（~/.commandcode/auth.json）内容：整个文件
//     贴进来即可，apiKey 会被取出来，userName / keyName 拼成账号名。
//
// 解析只在这一处做。前端只负责认出用户贴的是哪一种、给个提示，不自己拆一遍
// ——两份实现迟早会在某些文件上拆出不一样的结果，而那种偏差很难被发现。
func parseKeyLines(raw string) []accountKeyLine {
	if looksLikeAuthFile(raw) {
		return parseAuthFile(raw)
	}

	var out []accountKeyLine
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.LastIndex(line, ","); idx >= 0 {
			name := strings.TrimSpace(line[:idx])
			key := strings.TrimSpace(line[idx+1:])
			if key != "" {
				out = append(out, accountKeyLine{Name: name, Key: key})
				continue
			}
		}
		out = append(out, accountKeyLine{Key: line})
	}
	return out
}

// authFileEntry 是 ~/.commandcode/auth.json 里我们关心的字段。
//
// CLI 登录成功后写下的就是这个结构。userId、authenticatedAt 那些用不上：
// 前者是上游的内部 id，后者是客户端本地的时间戳，对我们都没有意义。
type authFileEntry struct {
	APIKey   string `json:"apiKey"`
	UserName string `json:"userName"`
	KeyName  string `json:"keyName"`
}

// looksLikeAuthFile 只看第一个非空白字符。
//
// 密钥一定是 user_ 开头，不会撞上花括号，所以这个判断是安全的。
func looksLikeAuthFile(raw string) bool {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

// parseAuthFile 从授权文件内容里逐条取出密钥。
//
// 读不出密钥的条目会作为空密钥返回，交给下游按「密钥不能为空」报错——
// 这样失败原因能和其它情况走同一条回显路径，操作员看到的是「第 N 条为什么不行」，
// 而不是「整个文件解析失败」。
func parseAuthFile(raw string) []accountKeyLine {
	blocks := splitJSONObjects(raw)
	out := make([]accountKeyLine, 0, len(blocks))
	for _, block := range blocks {
		var entry authFileEntry
		// 单条解析失败就当成一条空密钥，让它在失败列表里单独出现。
		// 整段放弃的话，一个坏条目会连累后面所有好账号。
		if err := json.Unmarshal([]byte(block), &entry); err != nil {
			out = append(out, accountKeyLine{})
			continue
		}
		out = append(out, accountKeyLine{
			Name: joinNonEmpty(" · ", entry.UserName, entry.KeyName),
			Key:  strings.TrimSpace(entry.APIKey),
		})
	}
	return out
}

// splitJSONObjects 把文本里所有顶层 JSON 对象切出来。
//
// 不对整段做 json.Unmarshal，是因为「多个账号」的贴法不止一种：可以是数组，
// 也可以是把几个 auth.json 的内容首尾相接。后者不是合法 JSON，但人手粘贴
// 时就是这么干的。所以按花括号配对来切，字符串内部的括号要跳过——
// 密钥或用户名里出现花括号虽然少见，但一旦出现就会把配对彻底带偏。
func splitJSONObjects(raw string) []string {
	var out []string
	depth, start := 0, -1
	inString, escaped := false, false

	for i, r := range raw {
		if inString {
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					// 花括号是单字节，i 就是它的字节下标。
					out = append(out, raw[start:i+1])
					start = -1
				}
			}
		}
	}
	return out
}

// joinNonEmpty 把非空片段用 sep 连起来。
//
// sep 必须在最前面：可变参数只能放末尾，所以它没法像 strings.Join 那样收尾。
// 调用时别把它和片段写反了——写反了不会报错，只会安静地拼出一串反过来的东西。
func joinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, sep)
}

// parseOptionalTime 解析可选的 RFC3339 时间字符串。
func parseOptionalTime(c *gin.Context, raw *string) (*time.Time, bool) {
	if raw == nil || *raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		fail(c, http.StatusBadRequest, "expires_at 需要是 RFC3339 格式，例如 2026-12-31T00:00:00Z")
		return nil, false
	}
	return &t, true
}

// writeAccountError 把账号服务的错误翻成合适的 HTTP 状态码。
func (h *Handler) writeAccountError(c *gin.Context, err error, fallback string) {
	switch {
	case err == nil:
		return
	case isNotFound(err):
		fail(c, http.StatusNotFound, "账号不存在")
	case isConstraint(err):
		fail(c, http.StatusConflict, "账号名已存在")
	case err == service.ErrInvalidAPIKey:
		fail(c, http.StatusBadRequest, err.Error())
	case err == service.ErrInvalidPlatform,
		err == service.ErrInvalidAccountMode,
		errors.Is(err, service.ErrPlatformMismatch):
		// 平台/分组不匹配属于配置错误，回显具体原因比笼统的 500 有用得多。
		fail(c, http.StatusBadRequest, err.Error())
	default:
		// 业务校验类错误（并发数必须大于 0 等）直接回显；其余按内部错误处理。
		msg := err.Error()
		if strings.Contains(msg, "不能为空") || strings.Contains(msg, "必须大于") ||
			strings.Contains(msg, "不存在的分组") || strings.Contains(msg, "proxy") ||
			strings.Contains(msg, "代理") || strings.Contains(msg, "无效") ||
			strings.Contains(msg, "格式") {
			fail(c, http.StatusBadRequest, msg)
			return
		}
		h.failInternal(c, fallback, err)
	}
}
