package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/group"
	"cmd2api/internal/domain"
)

// ErrInvalidRewardsAction 表示不支持的领奖/保活动作。
var ErrInvalidRewardsAction = errors.New("不支持的运营保活动作")

// RewardsPlatformSummary 对齐前端 RewardsPlatformSummary 并补充四平台专属聚合指标。
type RewardsPlatformSummary struct {
	Platform          string     `json:"platform"`
	TotalAccounts     int        `json:"total_accounts"`
	ActiveAccounts    int        `json:"active_accounts"`
	ErrorAccounts     int        `json:"error_accounts"`
	CheckedInToday    int        `json:"checked_in_today,omitempty"`
	UncheckedToday    int        `json:"unchecked_today,omitempty"`
	MaxCheckinStreak  int        `json:"max_checkin_streak,omitempty"`
	TotalCredits      float64    `json:"total_credits,omitempty"`
	TotalRemainingUSD float64    `json:"total_remaining_usd,omitempty"`
	NodesTotal        int        `json:"nodes_total,omitempty"`
	NodesOnline       int        `json:"nodes_online,omitempty"`
	AuthFilesTotal    int        `json:"auth_files_total,omitempty"`
	ZenAccounts       int        `json:"zen_accounts,omitempty"`
	GoAccounts        int        `json:"go_accounts,omitempty"`
	SupportsHarvest   bool       `json:"supports_harvest"`
	LastHarvestAt     *time.Time `json:"last_harvest_at,omitempty"`
}

// RewardsOverviewSummary 对齐前端 RewardsOverviewSummary 结构。
type RewardsOverviewSummary struct {
	TotalAccounts      int     `json:"total_accounts"`
	ActiveAccounts     int     `json:"active_accounts"`
	WorkBuddyTotal     int     `json:"workbuddy_total"`
	CheckedInToday     int     `json:"checked_in_today"`
	UncheckedToday     int     `json:"unchecked_today"`
	MaxCheckinStreak   int     `json:"max_checkin_streak"`
	TotalCredits       float64 `json:"total_credits"`
	TodayCreditsGained int     `json:"today_credits_gained"`
	RecentHarvestCount int     `json:"recent_harvest_count"`
}

// RewardsOverviewOutput 对齐前端 RewardsOverviewResponse 结构。
type RewardsOverviewOutput struct {
	Summary    RewardsOverviewSummary            `json:"summary"`
	ByPlatform []RewardsPlatformSummary          `json:"by_platform"`
	Platforms  map[string]RewardsPlatformSummary `json:"platforms"`
	Accounts   []*ent.Account                    `json:"accounts"`
}

// RunRewardsActionInput 对齐前端 RunRewardsActionPayload 结构。
type RunRewardsActionInput struct {
	Platform   string
	Action     string
	AccountIDs []int64
	GroupID    *int64
	DoChat     bool
}

// RewardsRunItem 对齐前端 RewardsRunItem 结构。
type RewardsRunItem struct {
	AccountID    int64             `json:"account_id"`
	ID           int64             `json:"id"`
	AccountName  string            `json:"account_name"`
	Name         string            `json:"name"`
	Platform     string            `json:"platform"`
	OK           bool              `json:"ok"`
	Skipped      bool              `json:"skipped,omitempty"`
	DurationMs   int               `json:"duration_ms"`
	LatencyMs    int               `json:"latency_ms,omitempty"`
	CreditGained int               `json:"credit_gained,omitempty"`
	TotalCredits *float64          `json:"total_credits,omitempty"`
	ExitIP       string            `json:"exit_ip,omitempty"`
	Steps        []HarvestStepInfo `json:"steps,omitempty"`
	Detail       string            `json:"detail,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// RunRewardsActionOutput 对齐前端 RunRewardsActionResponse 与后端内部 {Total, Succeeded, Failed, Results} 约定。
type RunRewardsActionOutput struct {
	OK            bool             `json:"ok"`
	Action        string           `json:"action"`
	Platform      string           `json:"platform,omitempty"`
	Total         int              `json:"total"`
	Succeeded     int              `json:"succeeded"`
	Failed        int              `json:"failed"`
	Skipped       int              `json:"skipped"`
	CreditGained  int              `json:"credit_gained"`
	CreditsGained int              `json:"credits_gained"`
	DurationMs    int              `json:"duration_ms"`
	Summary       string           `json:"summary"`
	Items         []RewardsRunItem `json:"items"`
	Results       []RewardsRunItem `json:"results,omitempty"`
	Accounts      []*ent.Account   `json:"accounts,omitempty"`
}

// RewardsService 聚合四大上游平台的签到收割、额度窗口同步、OAuth 刷新与保活探活能力。
type RewardsService struct {
	db         *ent.Client
	accountSvc *AccountService
	agySvc     *AntigravityService
	logger     *slog.Logger
}

// NewRewardsService 构造 RewardsService。
func NewRewardsService(
	db *ent.Client,
	accountSvc *AccountService,
	agySvc *AntigravityService,
	logger *slog.Logger,
) *RewardsService {
	if logger == nil {
		logger = slog.Default()
	}
	return &RewardsService{
		db:         db,
		accountSvc: accountSvc,
		agySvc:     agySvc,
		logger:     logger,
	}
}

// Overview 聚合返回 WorkBuddy、AntiGravity、Command Code、OpenCode 四大平台的领奖与健康概览。
func (s *RewardsService) Overview(ctx context.Context) (*RewardsOverviewOutput, error) {
	accs, err := s.db.Account.Query().
		Where(account.DeletedAtIsNil()).
		WithGroups().
		Order(ent.Asc(account.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询领奖中心账号池失败: %w", err)
	}

	nodes, err := s.db.ContainerNode.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询 AntiGravity 容器节点失败: %w", err)
	}

	todayLocal := time.Now().Format("2006-01-02")
	todayUTC := time.Now().UTC().Format("2006-01-02")

	platOrder := []string{
		domain.PlatformWorkBuddy,
		domain.PlatformAntigravity,
		domain.PlatformCommandCode,
		domain.PlatformOpenCode,
	}
	platMap := make(map[string]*RewardsPlatformSummary, 4)
	for _, p := range platOrder {
		platMap[p] = &RewardsPlatformSummary{
			Platform:        p,
			SupportsHarvest: p == domain.PlatformWorkBuddy,
		}
	}

	var overviewSummary RewardsOverviewSummary
	overviewSummary.TotalAccounts = len(accs)

	for _, acc := range accs {
		plat := domain.NormalizePlatform(acc.Platform)
		ps, ok := platMap[plat]
		if !ok {
			ps = &RewardsPlatformSummary{Platform: plat}
			platMap[plat] = ps
		}

		ps.TotalAccounts++
		if acc.Status == domain.StatusActive {
			ps.ActiveAccounts++
			overviewSummary.ActiveAccounts++
		} else if acc.Status == domain.StatusError {
			ps.ErrorAccounts++
		}

		switch plat {
		case domain.PlatformWorkBuddy:
			overviewSummary.WorkBuddyTotal++
			if acc.LastCheckinDate == todayLocal || acc.LastCheckinDate == todayUTC {
				ps.CheckedInToday++
				overviewSummary.CheckedInToday++
			}
			if acc.CheckinStreak > ps.MaxCheckinStreak {
				ps.MaxCheckinStreak = acc.CheckinStreak
			}
			if acc.CheckinStreak > overviewSummary.MaxCheckinStreak {
				overviewSummary.MaxCheckinStreak = acc.CheckinStreak
			}
			if acc.BalanceTotalCredits != nil {
				ps.TotalCredits += *acc.BalanceTotalCredits
				overviewSummary.TotalCredits += *acc.BalanceTotalCredits
			}
			if acc.LastHarvestAt != nil {
				if ps.LastHarvestAt == nil || acc.LastHarvestAt.After(*ps.LastHarvestAt) {
					tCopy := *acc.LastHarvestAt
					ps.LastHarvestAt = &tCopy
				}
				if time.Since(*acc.LastHarvestAt) <= 24*time.Hour {
					overviewSummary.RecentHarvestCount++
					if g, ok := extractSummaryInt(acc.LastHarvestSummary, "credit_gained"); ok && g > 0 {
						overviewSummary.TodayCreditsGained += g
					}
				}
			}

		case domain.PlatformCommandCode:
			if acc.BalanceRemaining != nil {
				ps.TotalRemainingUSD += *acc.BalanceRemaining
			}

		case domain.PlatformOpenCode:
			mode := strings.ToLower(strings.TrimSpace(extractExtraString(acc.Extra, "account_mode")))
			if mode == domain.AccountModeGo {
				ps.GoAccounts++
			} else {
				ps.ZenAccounts++
			}
		}
	}

	wbSummary := platMap[domain.PlatformWorkBuddy]
	if wbSummary.ActiveAccounts > wbSummary.CheckedInToday {
		wbSummary.UncheckedToday = wbSummary.ActiveAccounts - wbSummary.CheckedInToday
	}
	overviewSummary.UncheckedToday = wbSummary.UncheckedToday

	agySummary := platMap[domain.PlatformAntigravity]
	agySummary.NodesTotal = len(nodes)
	for _, n := range nodes {
		if n.Status == "running" || n.Status == "online" {
			agySummary.NodesOnline++
		}
		agySummary.AuthFilesTotal += n.AuthFilesCount
	}

	byPlatform := make([]RewardsPlatformSummary, 0, len(platOrder))
	platformsObj := make(map[string]RewardsPlatformSummary, len(platMap))
	for _, p := range platOrder {
		val := *platMap[p]
		byPlatform = append(byPlatform, val)
		platformsObj[p] = val
	}

	return &RewardsOverviewOutput{
		Summary:    overviewSummary,
		ByPlatform: byPlatform,
		Platforms:  platformsObj,
		Accounts:   accs,
	}, nil
}

// RunAction 对指定平台或指定账号集合并发执行签到领奖、额度同步、OAuth 刷新或探活巡检。
func (s *RewardsService) RunAction(ctx context.Context, in RunRewardsActionInput) (*RunRewardsActionOutput, error) {
	started := time.Now()
	normAction, err := normalizeRewardsAction(in.Action)
	if err != nil {
		return nil, err
	}

	rawPlat := strings.ToLower(strings.TrimSpace(in.Platform))
	filterPlatform := ""
	if rawPlat != "" && rawPlat != "all" {
		filterPlatform = domain.NormalizePlatform(rawPlat)
		if !domain.IsValidPlatform(filterPlatform) {
			return nil, ErrInvalidPlatform
		}
	}

	query := s.db.Account.Query().Where(account.DeletedAtIsNil())
	if len(in.AccountIDs) > 0 {
		query = query.Where(account.IDIn(uniqueIDs(in.AccountIDs)...))
	} else {
		query = query.Where(account.StatusNEQ(domain.StatusDisabled))
	}
	if filterPlatform != "" {
		query = query.Where(account.PlatformEQ(filterPlatform))
	}
	if in.GroupID != nil && *in.GroupID > 0 {
		query = query.Where(account.HasGroupsWith(group.IDEQ(*in.GroupID)))
	}

	targets, err := query.Order(ent.Asc(account.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询执行目标账号失败: %w", err)
	}

	// 若动作是针对 AntiGravity 的 refresh_oauth 且账号表尚无同步账号，也对全部容器节点执行一次检测刷新。
	if (filterPlatform == domain.PlatformAntigravity || filterPlatform == "") && normAction == "refresh_oauth" && s.agySvc != nil {
		if nodes, nErr := s.db.ContainerNode.Query().All(ctx); nErr == nil {
			for _, n := range nodes {
				_, _ = s.agySvc.RunNodeAction(ctx, n.ID, "check")
			}
		}
	}

	results := make([]RewardsRunItem, len(targets))
	const concurrency = 4
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, acc := range targets {
		wg.Add(1)
		go func(idx int, target *ent.Account) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[idx] = RewardsRunItem{
					AccountID:   target.ID,
					ID:          target.ID,
					AccountName: target.Name,
					Name:        target.Name,
					Platform:    domain.NormalizePlatform(target.Platform),
					OK:          false,
					Error:       ctx.Err().Error(),
				}
				return
			}
			defer func() { <-sem }()

			results[idx] = s.executeSingleAccountAction(ctx, target, normAction, in.DoChat)
		}(i, acc)
	}
	wg.Wait()

	out := &RunRewardsActionOutput{
		OK:         true,
		Action:     in.Action,
		Platform:   filterPlatform,
		Total:      len(results),
		DurationMs: int(time.Since(started).Milliseconds()),
		Items:      results,
		Results:    results,
	}

	for _, item := range results {
		if item.Skipped {
			out.Skipped++
			continue
		}
		if item.OK {
			out.Succeeded++
		} else {
			out.Failed++
		}
		out.CreditGained += item.CreditGained
	}
	out.CreditsGained = out.CreditGained

	if out.Total > 0 && out.Succeeded == 0 && out.Failed > 0 {
		out.OK = false
	}
	out.Summary = fmt.Sprintf("共 %d 个目标：成功 %d，失败 %d，跳过 %d", out.Total, out.Succeeded, out.Failed, out.Skipped)
	return out, nil
}

func (s *RewardsService) executeSingleAccountAction(
	ctx context.Context,
	acc *ent.Account,
	normAction string,
	doChat bool,
) RewardsRunItem {
	itemStart := time.Now()
	plat := domain.NormalizePlatform(acc.Platform)
	base := RewardsRunItem{
		AccountID:   acc.ID,
		ID:          acc.ID,
		AccountName: acc.Name,
		Name:        acc.Name,
		Platform:    plat,
	}

	if s.accountSvc == nil {
		base.Error = "账号服务未初始化"
		base.DurationMs = int(time.Since(itemStart).Milliseconds())
		return base
	}

	switch normAction {
	case "harvest":
		if plat != domain.PlatformWorkBuddy {
			base.OK = true
			base.Skipped = true
			base.Detail = "仅 WorkBuddy 账号支持签到领奖"
			base.DurationMs = int(time.Since(itemStart).Milliseconds())
			return base
		}
		res, err := s.accountSvc.HarvestAccount(ctx, acc.ID, doChat)
		base.DurationMs = int(time.Since(itemStart).Milliseconds())
		if err != nil {
			base.Error = err.Error()
			return base
		}
		base.OK = res.OK
		base.CreditGained = res.CreditGained
		base.TotalCredits = res.TotalCredits
		base.Steps = res.Steps
		base.Error = res.Error
		if res.OK {
			base.Detail = fmt.Sprintf("收割完成，新增 +%d 积分", res.CreditGained)
		}
		return base

	case "sync_balance":
		res, err := s.accountSvc.RefreshBalance(ctx, acc.ID)
		base.DurationMs = int(time.Since(itemStart).Milliseconds())
		if err != nil {
			base.Error = err.Error()
			return base
		}
		if !res.Supported {
			base.OK = true
			base.Skipped = true
			base.Detail = "该平台不支持独立余额接口查询"
			return base
		}
		if res.Err != nil {
			base.Error = res.Err.Error()
			return base
		}
		base.OK = true
		base.Detail = "额度与窗口已同步"
		return base

	case "refresh_oauth":
		if plat == domain.PlatformWorkBuddy {
			res, err := s.accountSvc.RefreshToken(ctx, acc.ID)
			base.DurationMs = int(time.Since(itemStart).Milliseconds())
			if err != nil {
				base.Error = err.Error()
				return base
			}
			base.OK = res.OK
			base.Error = res.Error
			if res.OK {
				base.Detail = "WorkBuddy OAuth 令牌已刷新"
			}
			return base
		}
		if plat == domain.PlatformAntigravity {
			if nodeID := extractExtraInt64(acc.Extra, "node_id"); nodeID > 0 && s.agySvc != nil {
				actOut, nErr := s.agySvc.RunNodeAction(ctx, nodeID, "check")
				base.DurationMs = int(time.Since(itemStart).Milliseconds())
				if nErr != nil {
					base.Error = nErr.Error()
					return base
				}
				base.OK = actOut.OK
				base.LatencyMs = actOut.LatencyMs
				if actOut.Error != nil {
					base.Error = *actOut.Error
				} else {
					base.Detail = actOut.Message
				}
				return base
			}
		}
		// 其余平台回退执行探活
		fallthrough

	case "probe":
		res, err := s.accountSvc.Check(ctx, acc.ID)
		base.DurationMs = int(time.Since(itemStart).Milliseconds())
		if err != nil {
			base.Error = err.Error()
			return base
		}
		base.LatencyMs = int(res.Latency.Milliseconds())
		if res.Err != nil {
			base.Error = res.ErrorMessage()
			return base
		}
		base.OK = true
		base.Detail = fmt.Sprintf("探活正常 (%dms)", base.LatencyMs)
		return base

	case "check_and_harvest":
		probeRes, err := s.accountSvc.Check(ctx, acc.ID)
		if err != nil {
			base.DurationMs = int(time.Since(itemStart).Milliseconds())
			base.Error = err.Error()
			return base
		}
		base.LatencyMs = int(probeRes.Latency.Milliseconds())
		if probeRes.Err != nil {
			base.DurationMs = int(time.Since(itemStart).Milliseconds())
			base.Error = probeRes.ErrorMessage()
			return base
		}
		if plat == domain.PlatformWorkBuddy {
			hRes, hErr := s.accountSvc.HarvestAccount(ctx, acc.ID, doChat)
			base.DurationMs = int(time.Since(itemStart).Milliseconds())
			if hErr != nil {
				base.Error = hErr.Error()
				return base
			}
			base.OK = hRes.OK
			base.CreditGained = hRes.CreditGained
			base.TotalCredits = hRes.TotalCredits
			base.Steps = hRes.Steps
			base.Error = hRes.Error
			return base
		}
		base.OK = true
		base.DurationMs = int(time.Since(itemStart).Milliseconds())
		base.Detail = "探活完成"
		return base

	case "proxy_test":
		res, err := s.accountSvc.TestAccountProxy(ctx, acc.ID, nil)
		base.DurationMs = int(time.Since(itemStart).Milliseconds())
		if err != nil {
			base.Error = err.Error()
			return base
		}
		base.OK = res.OK
		base.LatencyMs = int(res.LatencyMs)
		base.ExitIP = res.ExitIP
		base.Error = res.Error
		return base

	default:
		base.Error = ErrInvalidRewardsAction.Error()
		base.DurationMs = int(time.Since(itemStart).Milliseconds())
		return base
	}
}

func normalizeRewardsAction(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "harvest", "checkin":
		return "harvest", nil
	case "sync_balance", "balance_refresh", "refresh_balance":
		return "sync_balance", nil
	case "keepalive", "probe", "check", "account_check":
		return "probe", nil
	case "refresh_oauth", "token_refresh", "refresh_token":
		return "refresh_oauth", nil
	case "check_and_harvest":
		return "check_and_harvest", nil
	case "proxy_test":
		return "proxy_test", nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidRewardsAction, raw)
	}
}

func extractSummaryInt(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}
