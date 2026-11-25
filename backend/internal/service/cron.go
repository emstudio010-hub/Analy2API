package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/cronjob"
	"cmd2api/ent/group"
	"cmd2api/internal/cron"
	"cmd2api/internal/domain"
)

const (
	CronActionHarvest         = "harvest"
	CronActionAccountCheck    = "account_check"
	CronActionProxyTest       = "proxy_test"
	CronActionBalanceRefresh  = "balance_refresh"
	CronActionTokenRefresh    = "token_refresh"
	CronActionCheckAndHarvest = "check_and_harvest"

	CronScopeAll      = "all"
	CronScopeGroup    = "group"
	CronScopeAccounts = "accounts"

	CronStatusRunning = "running"
	CronStatusSuccess = "success"
	CronStatusPartial = "partial"
	CronStatusError   = "error"

	defaultCronTimezone = "Asia/Shanghai"
	maxCronRunHistory   = 20
)

var (
	ErrInvalidCronJob        = errors.New("invalid cron job")
	ErrCronJobNotFound       = errors.New("cron job not found")
	ErrCronJobAlreadyRunning = errors.New("cron job is already running")
)

// CronService 管理基于 Cron 表达式的定时自动测试、定时领奖励与账号巡检任务。
type CronService struct {
	db       *ent.Client
	accounts *AccountService
	logger   *slog.Logger

	mu        sync.Mutex
	schedules map[int64]*cron.Schedule
	locations map[int64]*time.Location
	nextRun   map[int64]time.Time
	running   map[int64]bool
	jobWg     sync.WaitGroup
}

// CreateCronJobInput 创建 Cron 任务的入参。
type CreateCronJobInput struct {
	Name        string
	Description string
	CronExpr    string
	Timezone    string
	Action      string
	TargetScope string
	GroupID     *int64
	AccountIDs  []int64
	Options     map[string]any
	Enabled     *bool
}

// UpdateCronJobInput 局部更新 Cron 任务的入参。
type UpdateCronJobInput struct {
	Name        *string
	Description *string
	CronExpr    *string
	Timezone    *string
	Action      *string
	TargetScope *string
	GroupID     *int64
	ClearGroup  bool
	AccountIDs  *[]int64
	Options     *map[string]any
	Enabled     *bool
}

// CronPreviewTime 表示一个预计算的未来触发时间点。
type CronPreviewTime struct {
	RFC3339 string `json:"rfc3339"`
	Local   string `json:"local"`
}

// CronPreviewOutput 是 Cron 表达式校验与预览的结果。
type CronPreviewOutput struct {
	Valid    bool              `json:"valid"`
	CronExpr string            `json:"cron_expr"`
	Timezone string            `json:"timezone"`
	Error    string            `json:"error,omitempty"`
	NextRuns []CronPreviewTime `json:"next_runs"`
}

// CronAccountResult 记录一次定时任务中单个账号的执行结果。
type CronAccountResult struct {
	AccountID    int64             `json:"account_id"`
	AccountName  string            `json:"account_name"`
	Platform     string            `json:"platform"`
	OK           bool              `json:"ok"`
	Skipped      bool              `json:"skipped,omitempty"`
	DurationMs   int64             `json:"duration_ms"`
	LatencyMs    int64             `json:"latency_ms,omitempty"`
	CreditGained int               `json:"credit_gained,omitempty"`
	TotalCredits *float64          `json:"total_credits,omitempty"`
	ExitIP       string            `json:"exit_ip,omitempty"`
	ProxyURL     string            `json:"proxy_url,omitempty"`
	Steps        []HarvestStepInfo `json:"steps,omitempty"`
	Detail       string            `json:"detail,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// CronRunOutput 表示一次 Cron 任务执行的汇总输出。
type CronRunOutput struct {
	JobID        int64               `json:"job_id"`
	JobName      string              `json:"job_name"`
	Action       string              `json:"action"`
	Trigger      string              `json:"trigger"`
	Status       string              `json:"status"`
	StartedAt    string              `json:"started_at"`
	FinishedAt   string              `json:"finished_at"`
	DurationMs   int                 `json:"duration_ms"`
	Total        int                 `json:"total"`
	Succeeded    int                 `json:"succeeded"`
	Failed       int                 `json:"failed"`
	Skipped      int                 `json:"skipped"`
	CreditGained int                 `json:"credits_gained"`
	Summary      string              `json:"summary"`
	Items        []CronAccountResult `json:"items"`
}

// NewCronService 构造 CronService。
func NewCronService(db *ent.Client, accounts *AccountService, logger *slog.Logger) *CronService {
	if logger == nil {
		logger = slog.Default()
	}
	return &CronService{
		db:        db,
		accounts:  accounts,
		logger:    logger,
		schedules: make(map[int64]*cron.Schedule),
		locations: make(map[int64]*time.Location),
		nextRun:   make(map[int64]time.Time),
		running:   make(map[int64]bool),
	}
}

type builtinCronSeed struct {
	Name        string
	Description string
	CronExpr    string
	Action      string
	Options     map[string]any
	Enabled     bool
}

func defaultBuiltinCronSeeds() []builtinCronSeed {
	return []builtinCronSeed{
		{
			Name:        "每日早晚自动签到与领奖励",
			Description: "每天 08:00 与 20:00 自动执行签到、补偿礼、连签礼、盲盒及已完成任务领取",
			CronExpr:    "0 8,20 * * *",
			Action:      CronActionHarvest,
			Options:     map[string]any{"do_chat": false},
			Enabled:     true,
		},
		{
			Name:        "定时账号自动测试探活",
			Description: "每 30 分钟对账号池自动发起一次对话探活测试，并自动恢复可响应的异常账号",
			CronExpr:    "*/30 * * * *",
			Action:      CronActionAccountCheck,
			Options:     map[string]any{"auto_recover": true, "concurrency": 2},
			Enabled:     true,
		},
		{
			Name:        "定时独立代理 IP 巡检",
			Description: "每 2 小时检测各账号独立代理 IP 连通性与真实出口 IP 延迟",
			CronExpr:    "0 */2 * * *",
			Action:      CronActionProxyTest,
			Options:     map[string]any{"concurrency": 4},
			Enabled:     true,
		},
		{
			Name:        "定时积分与资源包刷新",
			Description: "每 4 小时（第 15 分钟）刷新所有账号最新可用积分与资源包到期情况",
			CronExpr:    "15 */4 * * *",
			Action:      CronActionBalanceRefresh,
			Options:     map[string]any{"concurrency": 4},
			Enabled:     true,
		},
	}
}

// EnsureSeeded 在 cron_jobs 表为空时写入默认内置 Cron 任务（幂等）。
func (s *CronService) EnsureSeeded(ctx context.Context) error {
	count, err := s.db.CronJob.Query().Count(ctx)
	if err != nil {
		return fmt.Errorf("检查定时任务表失败: %w", err)
	}
	if count > 0 {
		return s.ReloadSchedules(ctx)
	}
	return s.seedMissingBuiltin(ctx)
}

func (s *CronService) seedMissingBuiltin(ctx context.Context) error {
	for _, seed := range defaultBuiltinCronSeeds() {
		exists, err := s.db.CronJob.Query().
			Where(
				cronjob.IsBuiltinEQ(true),
				cronjob.ActionEQ(seed.Action),
				cronjob.CronExprEQ(seed.CronExpr),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("查询内置定时任务失败: %w", err)
		}
		if exists {
			continue
		}
		if _, err := s.db.CronJob.Create().
			SetName(seed.Name).
			SetDescription(seed.Description).
			SetCronExpr(seed.CronExpr).
			SetTimezone(defaultCronTimezone).
			SetAction(seed.Action).
			SetTargetScope(CronScopeAll).
			SetAccountIds([]int64{}).
			SetOptions(seed.Options).
			SetEnabled(seed.Enabled).
			SetIsBuiltin(true).
			SetRunHistory([]map[string]any{}).
			Save(ctx); err != nil {
			return fmt.Errorf("初始化内置定时任务 %q 失败: %w", seed.Name, err)
		}
	}
	return s.ReloadSchedules(ctx)
}

// ResetBuiltin 恢复/补齐默认内置 Cron 任务（不会删除用户自定义任务）。
func (s *CronService) ResetBuiltin(ctx context.Context) ([]*ent.CronJob, error) {
	if err := s.seedMissingBuiltin(ctx); err != nil {
		return nil, err
	}
	return s.List(ctx)
}

// ReloadSchedules 重新从数据库加载所有启用任务的 Cron 调度表。
func (s *CronService) ReloadSchedules(ctx context.Context) error {
	jobs, err := s.db.CronJob.Query().
		Order(ent.Asc(cronjob.FieldID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("加载定时任务列表失败: %w", err)
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	newSched := make(map[int64]*cron.Schedule, len(jobs))
	newLoc := make(map[int64]*time.Location, len(jobs))
	newNext := make(map[int64]time.Time, len(jobs))

	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		sched, err := cron.Parse(job.CronExpr)
		if err != nil {
			s.logger.Warn("跳过无效的 Cron 任务表达式", "job_id", job.ID, "name", job.Name, "cron_expr", job.CronExpr, "err", err)
			continue
		}
		loc, _ := resolveTimezone(job.Timezone)
		newSched[job.ID] = sched
		newLoc[job.ID] = loc
		if prevNext, ok := s.nextRun[job.ID]; ok && !prevNext.IsZero() && prevNext.After(now) {
			newNext[job.ID] = prevNext
		} else {
			newNext[job.ID] = sched.Next(now.In(loc))
		}
	}

	s.schedules = newSched
	s.locations = newLoc
	s.nextRun = newNext
	return nil
}

// Start 启动后台 Cron 调度循环，随 ctx 取消优雅停止。
func (s *CronService) Start(ctx context.Context) {
	if err := s.ReloadSchedules(ctx); err != nil {
		s.logger.Warn("初始化 Cron 调度表失败", "err", err)
	}
	s.logger.Info("Cron 定时任务调度器已启动")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Cron 调度器正在等待在途任务收尾")
			s.jobWg.Wait()
			s.logger.Info("Cron 定时任务调度器已停止")
			return
		case now := <-ticker.C:
			dueIDs := s.popDueJobs(now)
			for _, id := range dueIDs {
				s.jobWg.Add(1)
				go func(jobID int64) {
					defer s.jobWg.Done()
					runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
					defer cancel()
					if _, err := s.ExecuteJob(runCtx, jobID, "cron"); err != nil &&
						!errors.Is(err, ErrCronJobAlreadyRunning) &&
						!errors.Is(err, context.Canceled) {
						s.logger.Warn("Cron 定时任务执行异常", "job_id", jobID, "err", err)
					}
				}(id)
			}
		}
	}
}

func (s *CronService) popDueJobs(now time.Time) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	var due []int64
	for id, next := range s.nextRun {
		if next.IsZero() || now.Before(next) {
			continue
		}
		due = append(due, id)
		sched := s.schedules[id]
		loc := s.locations[id]
		if loc == nil {
			loc = time.Local
		}
		if sched != nil {
			s.nextRun[id] = sched.Next(now.In(loc))
		} else {
			delete(s.nextRun, id)
		}
	}
	return due
}

// IsRunning 返回任务当前是否正在执行中。
func (s *CronService) IsRunning(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}

// RunningCount 返回当前正在执行中的任务数量。
func (s *CronService) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// Preview 校验 Cron 表达式并计算未来 count 次触发时间。
func (s *CronService) Preview(cronExpr, tzName string, count int) CronPreviewOutput {
	if count <= 0 {
		count = 5
	}
	normExpr := strings.TrimSpace(cronExpr)
	loc, normTZ := resolveTimezone(tzName)
	out := CronPreviewOutput{
		CronExpr: normExpr,
		Timezone: normTZ,
		NextRuns: []CronPreviewTime{},
	}

	sched, err := cron.Parse(normExpr)
	if err != nil {
		out.Valid = false
		out.Error = err.Error()
		return out
	}

	now := time.Now().In(loc)
	nextList := sched.NextN(now, count)
	out.Valid = true
	for _, t := range nextList {
		out.NextRuns = append(out.NextRuns, CronPreviewTime{
			RFC3339: t.UTC().Format(time.RFC3339),
			Local:   t.In(loc).Format("2006-01-02 15:04:05"),
		})
	}
	return out
}

// NextRunsForJob 计算某个任务接下来 count 次调度时间。
func (s *CronService) NextRunsForJob(job *ent.CronJob, count int) []CronPreviewTime {
	if job == nil || !job.Enabled {
		return []CronPreviewTime{}
	}
	preview := s.Preview(job.CronExpr, job.Timezone, count)
	if !preview.Valid {
		return []CronPreviewTime{}
	}
	return preview.NextRuns
}

// List 查询全部定时任务（按 ID 升序）。
func (s *CronService) List(ctx context.Context) ([]*ent.CronJob, error) {
	return s.db.CronJob.Query().
		Order(ent.Asc(cronjob.FieldID)).
		All(ctx)
}

// Get 获取单个定时任务。
func (s *CronService) Get(ctx context.Context, id int64) (*ent.CronJob, error) {
	job, err := s.db.CronJob.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrCronJobNotFound
		}
		return nil, err
	}
	return job, nil
}

// Create 新增 Cron 任务并刷新内存调度缓存。
func (s *CronService) Create(ctx context.Context, in CreateCronJobInput) (*ent.CronJob, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: 任务名称不能为空", ErrInvalidCronJob)
	}
	cronExpr := strings.TrimSpace(in.CronExpr)
	if _, err := cron.Parse(cronExpr); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCronJob, err)
	}
	tzName, err := validateTimezone(in.Timezone)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCronJob, err)
	}
	action, err := normalizeCronAction(in.Action)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCronJob, err)
	}
	scope, groupID, accountIDs, err := normalizeCronTarget(ctx, s.db, in.TargetScope, in.GroupID, in.AccountIDs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCronJob, err)
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	opts := cloneMap(in.Options)

	builder := s.db.CronJob.Create().
		SetName(name).
		SetDescription(strings.TrimSpace(in.Description)).
		SetCronExpr(cronExpr).
		SetTimezone(tzName).
		SetAction(action).
		SetTargetScope(scope).
		SetAccountIds(accountIDs).
		SetOptions(opts).
		SetEnabled(enabled).
		SetIsBuiltin(false).
		SetRunHistory([]map[string]any{})
	if groupID != nil {
		builder = builder.SetGroupID(*groupID)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建定时任务失败: %w", err)
	}
	s.refreshSingleJobSchedule(created)
	return created, nil
}

// Update 更新 Cron 任务配置并刷新内存调度缓存。
func (s *CronService) Update(ctx context.Context, id int64, in UpdateCronJobInput) (*ent.CronJob, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	name := existing.Name
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: 任务名称不能为空", ErrInvalidCronJob)
		}
	}
	desc := existing.Description
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	cronExpr := existing.CronExpr
	if in.CronExpr != nil {
		cronExpr = strings.TrimSpace(*in.CronExpr)
		if _, err := cron.Parse(cronExpr); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCronJob, err)
		}
	}
	tzName := existing.Timezone
	if in.Timezone != nil {
		tzName, err = validateTimezone(*in.Timezone)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCronJob, err)
		}
	}
	action := existing.Action
	if in.Action != nil {
		action, err = normalizeCronAction(*in.Action)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCronJob, err)
		}
	}

	rawScope := existing.TargetScope
	if in.TargetScope != nil {
		rawScope = *in.TargetScope
	}
	rawGroupID := existing.GroupID
	if in.ClearGroup {
		rawGroupID = nil
	} else if in.GroupID != nil {
		rawGroupID = in.GroupID
	}
	rawAccountIDs := existing.AccountIds
	if in.AccountIDs != nil {
		rawAccountIDs = *in.AccountIDs
	}
	scope, groupID, accountIDs, err := normalizeCronTarget(ctx, s.db, rawScope, rawGroupID, rawAccountIDs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCronJob, err)
	}

	opts := existing.Options
	if in.Options != nil {
		opts = cloneMap(*in.Options)
	}
	enabled := existing.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	builder := s.db.CronJob.UpdateOneID(id).
		SetName(name).
		SetDescription(desc).
		SetCronExpr(cronExpr).
		SetTimezone(tzName).
		SetAction(action).
		SetTargetScope(scope).
		SetAccountIds(accountIDs).
		SetOptions(opts).
		SetEnabled(enabled)
	if groupID != nil {
		builder = builder.SetGroupID(*groupID)
	} else {
		builder = builder.ClearGroupID()
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("更新定时任务失败: %w", err)
	}
	s.refreshSingleJobSchedule(updated)
	return updated, nil
}

// Delete 删除定时任务。
func (s *CronService) Delete(ctx context.Context, id int64) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if s.IsRunning(id) {
		return ErrCronJobAlreadyRunning
	}
	if err := s.db.CronJob.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("删除定时任务失败: %w", err)
	}
	s.mu.Lock()
	delete(s.schedules, id)
	delete(s.locations, id)
	delete(s.nextRun, id)
	s.mu.Unlock()
	return nil
}

func (s *CronService) refreshSingleJobSchedule(job *ent.CronJob) {
	if job == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if !job.Enabled {
		delete(s.schedules, job.ID)
		delete(s.locations, job.ID)
		delete(s.nextRun, job.ID)
		return
	}
	sched, err := cron.Parse(job.CronExpr)
	if err != nil {
		delete(s.schedules, job.ID)
		delete(s.locations, job.ID)
		delete(s.nextRun, job.ID)
		return
	}
	loc, _ := resolveTimezone(job.Timezone)
	s.schedules[job.ID] = sched
	s.locations[job.ID] = loc
	s.nextRun[job.ID] = sched.Next(time.Now().In(loc))
}

func (s *CronService) tryAcquireJob(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *CronService) releaseJob(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}

// ExecuteJob 立即执行指定定时任务（trigger 为 "cron" 或 "manual"），并写回运行结果与历史。
func (s *CronService) ExecuteJob(ctx context.Context, id int64, trigger string) (*CronRunOutput, error) {
	if s.accounts == nil {
		return nil, errors.New("账号服务未就绪")
	}
	if !s.tryAcquireJob(id) {
		return nil, ErrCronJobAlreadyRunning
	}
	defer s.releaseJob(id)

	job, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(trigger) == "" {
		trigger = "manual"
	}

	startedAt := time.Now()
	_, _ = s.db.CronJob.UpdateOneID(id).
		SetLastStatus(CronStatusRunning).
		Save(ctx)

	targets, err := s.resolveTargetAccounts(ctx, job)
	if err != nil {
		finishedAt := time.Now()
		errMsg := fmt.Sprintf("筛选目标账号失败: %v", err)
		s.persistRunFailure(context.Background(), job, trigger, startedAt, finishedAt, errMsg)
		return nil, err
	}

	items := s.runForTargets(ctx, job, targets)
	finishedAt := time.Now()
	out := summarizeCronRun(job, trigger, startedAt, finishedAt, items)

	if err := s.persistRunOutput(context.Background(), job, startedAt, out); err != nil {
		s.logger.Warn("写回 Cron 执行结果失败", "job_id", job.ID, "err", err)
	}

	return out, nil
}

func (s *CronService) resolveTargetAccounts(ctx context.Context, job *ent.CronJob) ([]*ent.Account, error) {
	q := s.db.Account.Query().
		Where(account.DeletedAtIsNil())

	switch job.TargetScope {
	case CronScopeGroup:
		if job.GroupID == nil || *job.GroupID <= 0 {
			return nil, errors.New("任务目标范围为 group，但未指定分组")
		}
		q = q.Where(account.HasGroupsWith(group.ID(*job.GroupID), group.DeletedAtIsNil()))
	case CronScopeAccounts:
		if len(job.AccountIds) == 0 {
			return []*ent.Account{}, nil
		}
		q = q.Where(account.IDIn(job.AccountIds...))
	}

	// 状态过滤：手动停用 (disabled) 的账号默认不参与自动化任务，
	// 但允许在 account_check / check_and_harvest 开启 auto_recover 时检测 error 账号以便自动恢复。
	switch job.Action {
	case CronActionAccountCheck, CronActionCheckAndHarvest:
		autoRecover := optionBool(job.Options, "auto_recover", true)
		if autoRecover {
			q = q.Where(account.StatusNEQ(domain.StatusDisabled))
		} else {
			q = q.Where(account.StatusEQ(domain.StatusActive))
		}
	default:
		q = q.Where(account.StatusNEQ(domain.StatusDisabled))
	}

	rows, err := q.Order(ent.Asc(account.FieldPriority), ent.Asc(account.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}

	// harvest / refresh_token / check_and_harvest 等特定 WorkBuddy 动作优先筛选 WorkBuddy 账号
	// （但 account_check / proxy_test / balance_refresh 对全部平台均有效）。
	if job.Action == CronActionHarvest || job.Action == CronActionTokenRefresh {
		filtered := make([]*ent.Account, 0, len(rows))
		for _, r := range rows {
			if domain.NormalizePlatform(r.Platform) == domain.PlatformWorkBuddy {
				filtered = append(filtered, r)
			}
		}
		return filtered, nil
	}
	return rows, nil
}

func (s *CronService) runForTargets(ctx context.Context, job *ent.CronJob, targets []*ent.Account) []CronAccountResult {
	if len(targets) == 0 {
		return []CronAccountResult{}
	}

	defaultConc := 4
	if job.Action == CronActionAccountCheck || job.Action == CronActionCheckAndHarvest {
		defaultConc = 2
	}
	conc := optionInt(job.Options, "concurrency", defaultConc)
	if conc <= 0 {
		conc = 1
	}
	if conc > 8 {
		conc = 8
	}

	results := make([]CronAccountResult, len(targets))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	for i, acc := range targets {
		wg.Add(1)
		go func(idx int, target *ent.Account) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[idx] = CronAccountResult{
					AccountID:   target.ID,
					AccountName: target.Name,
					Platform:    domain.NormalizePlatform(target.Platform),
					OK:          false,
					Error:       ctx.Err().Error(),
				}
				return
			}
			defer func() { <-sem }()

			results[idx] = s.executeSingleAccount(ctx, job, target)
		}(i, acc)
	}
	wg.Wait()
	return results
}

func (s *CronService) executeSingleAccount(ctx context.Context, job *ent.CronJob, acc *ent.Account) CronAccountResult {
	start := time.Now()
	res := CronAccountResult{
		AccountID:   acc.ID,
		AccountName: acc.Name,
		Platform:    domain.NormalizePlatform(acc.Platform),
	}

	perTimeout := 90 * time.Second
	if job.Action == CronActionProxyTest || job.Action == CronActionBalanceRefresh {
		perTimeout = 35 * time.Second
	}
	actCtx, cancel := context.WithTimeout(ctx, perTimeout)
	defer cancel()

	switch job.Action {
	case CronActionHarvest:
		doChat := optionBool(job.Options, "do_chat", false)
		out, err := s.accounts.HarvestAccount(actCtx, acc.ID, doChat)
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.OK = false
			res.Error = err.Error()
			return res
		}
		res.OK = out.OK
		res.CreditGained = out.CreditGained
		res.TotalCredits = out.TotalCredits
		res.Steps = out.Steps
		res.Error = out.Error
		if out.OK {
			res.Detail = fmt.Sprintf("收割成功 (+%d 积分)", out.CreditGained)
		}
		return res

	case CronActionAccountCheck:
		probe, err := s.accounts.Check(actCtx, acc.ID)
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.OK = false
			res.Error = err.Error()
			return res
		}
		res.LatencyMs = probe.Latency.Milliseconds()
		res.OK = probe.Err == nil
		if probe.Err != nil {
			res.Error = probe.ErrorMessage()
		} else {
			res.Detail = fmt.Sprintf("探活正常 (%dms)", res.LatencyMs)
			if refreshedAcc, gErr := s.db.Account.Get(actCtx, acc.ID); gErr == nil {
				res.TotalCredits = refreshedAcc.BalanceTotalCredits
			}
		}
		return res

	case CronActionProxyTest:
		pt, err := s.accounts.TestAccountProxy(actCtx, acc.ID, nil)
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.OK = false
			res.Error = err.Error()
			return res
		}
		res.OK = pt.OK
		res.LatencyMs = pt.LatencyMs
		res.ExitIP = pt.ExitIP
		res.ProxyURL = pt.ProxyURL
		res.Error = pt.Error
		if pt.OK {
			if pt.ExitIP != "" {
				res.Detail = fmt.Sprintf("出口 IP %s (%dms)", pt.ExitIP, pt.LatencyMs)
			} else {
				res.Detail = fmt.Sprintf("连通正常 (%dms)", pt.LatencyMs)
			}
		}
		return res

	case CronActionBalanceRefresh:
		bal, err := s.accounts.RefreshBalance(actCtx, acc.ID)
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.OK = false
			res.Error = err.Error()
			return res
		}
		if !bal.Supported {
			res.OK = true
			res.Skipped = true
			res.Detail = "该平台暂不支持余额查询"
			return res
		}
		if bal.Err != nil {
			res.OK = false
			res.Error = bal.Err.Error()
			return res
		}
		credits := bal.Balance.TotalCredits
		res.OK = true
		res.TotalCredits = &credits
		res.Detail = fmt.Sprintf("当前积分 %.2f", credits)
		return res

	case CronActionTokenRefresh:
		if domain.NormalizePlatform(acc.Platform) != domain.PlatformWorkBuddy {
			res.OK = true
			res.Skipped = true
			res.Detail = "非 WorkBuddy 账号，跳过 Token 续期"
			res.DurationMs = time.Since(start).Milliseconds()
			return res
		}
		tr, err := s.accounts.RefreshToken(actCtx, acc.ID)
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.OK = false
			res.Error = err.Error()
			return res
		}
		res.OK = tr.OK
		res.Error = tr.Error
		if tr.OK {
			res.Detail = "Token 续期成功"
		}
		return res

	case CronActionCheckAndHarvest:
		probe, err := s.accounts.Check(actCtx, acc.ID)
		if err != nil {
			res.DurationMs = time.Since(start).Milliseconds()
			res.OK = false
			res.Error = "探活失败: " + err.Error()
			return res
		}
		res.LatencyMs = probe.Latency.Milliseconds()
		if probe.Err != nil && !optionBool(job.Options, "harvest_on_check_fail", false) {
			res.DurationMs = time.Since(start).Milliseconds()
			res.OK = false
			res.Error = "探活失败: " + probe.ErrorMessage()
			return res
		}
		if domain.NormalizePlatform(acc.Platform) != domain.PlatformWorkBuddy {
			res.DurationMs = time.Since(start).Milliseconds()
			res.OK = probe.Err == nil
			res.Detail = fmt.Sprintf("探活正常 (%dms)，非 WorkBuddy 账号跳过领奖", res.LatencyMs)
			return res
		}
		doChat := optionBool(job.Options, "do_chat", false)
		hr, hErr := s.accounts.HarvestAccount(actCtx, acc.ID, doChat)
		res.DurationMs = time.Since(start).Milliseconds()
		if hErr != nil {
			res.OK = false
			res.Error = "领奖失败: " + hErr.Error()
			return res
		}
		res.OK = hr.OK
		res.CreditGained = hr.CreditGained
		res.TotalCredits = hr.TotalCredits
		res.Steps = hr.Steps
		res.Error = hr.Error
		if hr.OK {
			res.Detail = fmt.Sprintf("探活 %dms · 领奖 +%d 积分", res.LatencyMs, hr.CreditGained)
		}
		return res

	default:
		res.DurationMs = time.Since(start).Milliseconds()
		res.OK = false
		res.Error = fmt.Sprintf("未知定时动作: %s", job.Action)
		return res
	}
}

func summarizeCronRun(job *ent.CronJob, trigger string, startedAt, finishedAt time.Time, items []CronAccountResult) *CronRunOutput {
	out := &CronRunOutput{
		JobID:      job.ID,
		JobName:    job.Name,
		Action:     job.Action,
		Trigger:    trigger,
		StartedAt:  startedAt.UTC().Format(time.RFC3339),
		FinishedAt: finishedAt.UTC().Format(time.RFC3339),
		DurationMs: int(finishedAt.Sub(startedAt).Milliseconds()),
		Total:      len(items),
		Items:      items,
	}

	var latencySum int64
	var latencyCount int64
	for _, it := range items {
		switch {
		case it.Skipped:
			out.Skipped++
		case it.OK:
			out.Succeeded++
		default:
			out.Failed++
		}
		out.CreditGained += it.CreditGained
		if it.OK && it.LatencyMs > 0 {
			latencySum += it.LatencyMs
			latencyCount++
		}
	}

	switch {
	case out.Total == 0:
		out.Status = CronStatusSuccess
		out.Summary = "未匹配到符合条件的目标账号，已跳过"
	case out.Failed == 0:
		out.Status = CronStatusSuccess
	case out.Succeeded > 0:
		out.Status = CronStatusPartial
	default:
		out.Status = CronStatusError
	}

	if out.Total > 0 {
		switch job.Action {
		case CronActionHarvest:
			out.Summary = fmt.Sprintf("领奖收割 %d 个账号：成功 %d，失败 %d，共获得 +%d 积分",
				out.Total, out.Succeeded, out.Failed, out.CreditGained)
		case CronActionCheckAndHarvest:
			out.Summary = fmt.Sprintf("测试+领奖 %d 个账号：成功 %d，失败 %d，共获得 +%d 积分",
				out.Total, out.Succeeded, out.Failed, out.CreditGained)
		case CronActionAccountCheck:
			avgMs := int64(0)
			if latencyCount > 0 {
				avgMs = latencySum / latencyCount
			}
			out.Summary = fmt.Sprintf("自动测试 %d 个账号：正常 %d，异常 %d，平均延迟 %dms",
				out.Total, out.Succeeded, out.Failed, avgMs)
		case CronActionProxyTest:
			out.Summary = fmt.Sprintf("代理 IP 巡检 %d 个账号：连通 %d，失败 %d",
				out.Total, out.Succeeded, out.Failed)
		case CronActionBalanceRefresh:
			out.Summary = fmt.Sprintf("刷新积分 %d 个账号：成功 %d，失败 %d",
				out.Total, out.Succeeded, out.Failed)
		case CronActionTokenRefresh:
			out.Summary = fmt.Sprintf("续期 Token %d 个账号：成功 %d，失败 %d",
				out.Total, out.Succeeded, out.Failed)
		default:
			out.Summary = fmt.Sprintf("处理 %d 个账号：成功 %d，失败 %d",
				out.Total, out.Succeeded, out.Failed)
		}
	}

	return out
}

func (s *CronService) persistRunFailure(ctx context.Context, job *ent.CronJob, trigger string, startedAt, finishedAt time.Time, errMsg string) {
	out := &CronRunOutput{
		JobID:      job.ID,
		JobName:    job.Name,
		Action:     job.Action,
		Trigger:    trigger,
		Status:     CronStatusError,
		StartedAt:  startedAt.UTC().Format(time.RFC3339),
		FinishedAt: finishedAt.UTC().Format(time.RFC3339),
		DurationMs: int(finishedAt.Sub(startedAt).Milliseconds()),
		Summary:    errMsg,
		Items:      []CronAccountResult{},
	}
	_ = s.persistRunOutput(ctx, job, startedAt, out)
}

func (s *CronService) persistRunOutput(ctx context.Context, job *ent.CronJob, startedAt time.Time, out *CronRunOutput) error {
	lastResult := map[string]any{
		"trigger":        out.Trigger,
		"status":         out.Status,
		"started_at":     out.StartedAt,
		"finished_at":    out.FinishedAt,
		"duration_ms":    out.DurationMs,
		"total":          out.Total,
		"succeeded":      out.Succeeded,
		"failed":         out.Failed,
		"skipped":        out.Skipped,
		"credits_gained": out.CreditGained,
		"summary":        out.Summary,
		"items":          out.Items,
	}

	historyEntry := map[string]any{
		"trigger":        out.Trigger,
		"status":         out.Status,
		"started_at":     out.StartedAt,
		"finished_at":    out.FinishedAt,
		"duration_ms":    out.DurationMs,
		"total":          out.Total,
		"succeeded":      out.Succeeded,
		"failed":         out.Failed,
		"skipped":        out.Skipped,
		"credits_gained": out.CreditGained,
		"summary":        out.Summary,
	}

	history := make([]map[string]any, 0, len(job.RunHistory)+1)
	history = append(history, historyEntry)
	for _, old := range job.RunHistory {
		if len(history) >= maxCronRunHistory {
			break
		}
		history = append(history, old)
	}

	_, err := s.db.CronJob.UpdateOneID(job.ID).
		SetLastRunAt(startedAt).
		SetLastStatus(out.Status).
		SetLastDurationMs(out.DurationMs).
		SetLastSummary(truncateRunes(out.Summary, 950)).
		SetLastResult(lastResult).
		SetRunHistory(history).
		Save(ctx)
	return err
}

func normalizeCronAction(raw string) (string, error) {
	act := strings.TrimSpace(strings.ToLower(raw))
	switch act {
	case CronActionHarvest,
		CronActionAccountCheck,
		CronActionProxyTest,
		CronActionBalanceRefresh,
		CronActionTokenRefresh,
		CronActionCheckAndHarvest:
		return act, nil
	default:
		return "", fmt.Errorf("不支持的任务类型 %q", raw)
	}
}

func normalizeCronTarget(ctx context.Context, db *ent.Client, rawScope string, groupID *int64, accountIDs []int64) (string, *int64, []int64, error) {
	scope := strings.TrimSpace(strings.ToLower(rawScope))
	if scope == "" {
		scope = CronScopeAll
	}
	switch scope {
	case CronScopeAll:
		return CronScopeAll, nil, []int64{}, nil
	case CronScopeGroup:
		if groupID == nil || *groupID <= 0 {
			return "", nil, nil, errors.New("目标范围为指定分组时必须选择分组")
		}
		exists, err := db.Group.Query().
			Where(group.ID(*groupID), group.DeletedAtIsNil()).
			Exist(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		if !exists {
			return "", nil, nil, fmt.Errorf("分组 %d 不存在", *groupID)
		}
		gid := *groupID
		return CronScopeGroup, &gid, []int64{}, nil
	case CronScopeAccounts:
		cleanIDs := make([]int64, 0, len(accountIDs))
		for _, id := range accountIDs {
			if id > 0 && !slices.Contains(cleanIDs, id) {
				cleanIDs = append(cleanIDs, id)
			}
		}
		if len(cleanIDs) == 0 {
			return "", nil, nil, errors.New("目标范围为指定账号时至少需要选择 1 个账号")
		}
		return CronScopeAccounts, nil, cleanIDs, nil
	default:
		return "", nil, nil, fmt.Errorf("不支持的目标范围 %q", rawScope)
	}
}

func validateTimezone(raw string) (string, error) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		return defaultCronTimezone, nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", fmt.Errorf("无效时区 %q: %w", tz, err)
	}
	return tz, nil
}

func resolveTimezone(raw string) (*time.Location, string) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		tz = defaultCronTimezone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		if tz == defaultCronTimezone {
			return time.FixedZone("CST", 8*3600), defaultCronTimezone
		}
		return time.Local, time.Local.String()
	}
	return loc, tz
}

func optionBool(opts map[string]any, key string, def bool) bool {
	if opts == nil {
		return def
	}
	v, ok := opts[key]
	if !ok || v == nil {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func optionInt(opts map[string]any, key string, def int) int {
	if opts == nil {
		return def
	}
	v, ok := opts[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return def
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
