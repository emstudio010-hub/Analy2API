package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

type cronJobDTO struct {
	ID             int64                     `json:"id"`
	Name           string                    `json:"name"`
	Description    string                    `json:"description"`
	CronExpr       string                    `json:"cron_expr"`
	Timezone       string                    `json:"timezone"`
	Action         string                    `json:"action"`
	TargetScope    string                    `json:"target_scope"`
	GroupID        *int64                    `json:"group_id"`
	AccountIDs     []int64                   `json:"account_ids"`
	Options        map[string]any            `json:"options"`
	Enabled        bool                      `json:"enabled"`
	IsBuiltin      bool                      `json:"is_builtin"`
	Running        bool                      `json:"running"`
	NextRunAt      *string                   `json:"next_run_at"`
	NextRuns       []service.CronPreviewTime `json:"next_runs"`
	LastRunAt      *string                   `json:"last_run_at"`
	LastStatus     string                    `json:"last_status"`
	LastDurationMs int                       `json:"last_duration_ms"`
	LastSummary    string                    `json:"last_summary"`
	LastResult     map[string]any            `json:"last_result"`
	RunHistory     []map[string]any          `json:"run_history"`
	CreatedAt      string                    `json:"created_at"`
	UpdatedAt      string                    `json:"updated_at"`
}

func (h *Handler) toCronJobDTO(job *ent.CronJob) cronJobDTO {
	accountIDs := job.AccountIds
	if accountIDs == nil {
		accountIDs = []int64{}
	}
	opts := job.Options
	if opts == nil {
		opts = map[string]any{}
	}
	history := job.RunHistory
	if history == nil {
		history = []map[string]any{}
	}

	var nextRuns []service.CronPreviewTime
	var nextRunAt *string
	running := false
	if h.crons != nil {
		running = h.crons.IsRunning(job.ID)
		nextRuns = h.crons.NextRunsForJob(job, 5)
		if len(nextRuns) > 0 {
			first := nextRuns[0].RFC3339
			nextRunAt = &first
		}
	}
	if nextRuns == nil {
		nextRuns = []service.CronPreviewTime{}
	}

	return cronJobDTO{
		ID:             job.ID,
		Name:           job.Name,
		Description:    job.Description,
		CronExpr:       job.CronExpr,
		Timezone:       job.Timezone,
		Action:         job.Action,
		TargetScope:    job.TargetScope,
		GroupID:        job.GroupID,
		AccountIDs:     accountIDs,
		Options:        opts,
		Enabled:        job.Enabled,
		IsBuiltin:      job.IsBuiltin,
		Running:        running,
		NextRunAt:      nextRunAt,
		NextRuns:       nextRuns,
		LastRunAt:      timePtr(job.LastRunAt),
		LastStatus:     job.LastStatus,
		LastDurationMs: job.LastDurationMs,
		LastSummary:    job.LastSummary,
		LastResult:     job.LastResult,
		RunHistory:     history,
		CreatedAt:      job.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      job.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type createCronJobRequest struct {
	Name        string         `json:"name" binding:"required"`
	Description string         `json:"description"`
	CronExpr    string         `json:"cron_expr" binding:"required"`
	Timezone    string         `json:"timezone"`
	Action      string         `json:"action" binding:"required"`
	TargetScope string         `json:"target_scope"`
	GroupID     *int64         `json:"group_id"`
	AccountIDs  []int64        `json:"account_ids"`
	Options     map[string]any `json:"options"`
	Enabled     *bool          `json:"enabled"`
}

type updateCronJobRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	CronExpr    *string         `json:"cron_expr"`
	Timezone    *string         `json:"timezone"`
	Action      *string         `json:"action"`
	TargetScope *string         `json:"target_scope"`
	GroupID     *int64          `json:"group_id"`
	ClearGroup  bool            `json:"clear_group"`
	AccountIDs  *[]int64        `json:"account_ids"`
	Options     *map[string]any `json:"options"`
	Enabled     *bool           `json:"enabled"`
}

type previewCronRequest struct {
	CronExpr string `json:"cron_expr" binding:"required"`
	Timezone string `json:"timezone"`
	Count    int    `json:"count"`
}

// ListCronJobs 列出所有 Cron 定时任务与概览统计。
func (h *Handler) ListCronJobs(c *gin.Context) {
	if h.crons == nil {
		fail(c, http.StatusServiceUnavailable, "定时任务服务未启用")
		return
	}
	jobs, err := h.crons.List(c.Request.Context())
	if err != nil {
		h.failInternal(c, "查询定时任务失败", err)
		return
	}
	items := make([]cronJobDTO, 0, len(jobs))
	enabledCount := 0
	runningCount := 0
	for _, job := range jobs {
		dto := h.toCronJobDTO(job)
		if dto.Enabled {
			enabledCount++
		}
		if dto.Running {
			runningCount++
		}
		items = append(items, dto)
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
		"stats": gin.H{
			"total":   len(items),
			"enabled": enabledCount,
			"running": runningCount,
		},
	})
}

// CreateCronJob 新增 Cron 定时任务。
func (h *Handler) CreateCronJob(c *gin.Context) {
	if h.crons == nil {
		fail(c, http.StatusServiceUnavailable, "定时任务服务未启用")
		return
	}
	var req createCronJobRequest
	if !bindJSON(c, &req) {
		return
	}
	job, err := h.crons.Create(c.Request.Context(), service.CreateCronJobInput{
		Name:        req.Name,
		Description: req.Description,
		CronExpr:    req.CronExpr,
		Timezone:    req.Timezone,
		Action:      req.Action,
		TargetScope: req.TargetScope,
		GroupID:     req.GroupID,
		AccountIDs:  req.AccountIDs,
		Options:     req.Options,
		Enabled:     req.Enabled,
	})
	if err != nil {
		h.writeCronError(c, err, "创建定时任务失败")
		return
	}
	c.JSON(http.StatusCreated, h.toCronJobDTO(job))
}

// UpdateCronJob 更新 Cron 定时任务或切换启停状态。
func (h *Handler) UpdateCronJob(c *gin.Context) {
	if h.crons == nil {
		fail(c, http.StatusServiceUnavailable, "定时任务服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req updateCronJobRequest
	if !bindJSON(c, &req) {
		return
	}
	job, err := h.crons.Update(c.Request.Context(), id, service.UpdateCronJobInput{
		Name:        req.Name,
		Description: req.Description,
		CronExpr:    req.CronExpr,
		Timezone:    req.Timezone,
		Action:      req.Action,
		TargetScope: req.TargetScope,
		GroupID:     req.GroupID,
		ClearGroup:  req.ClearGroup,
		AccountIDs:  req.AccountIDs,
		Options:     req.Options,
		Enabled:     req.Enabled,
	})
	if err != nil {
		h.writeCronError(c, err, "更新定时任务失败")
		return
	}
	c.JSON(http.StatusOK, h.toCronJobDTO(job))
}

// DeleteCronJob 删除指定定时任务。
func (h *Handler) DeleteCronJob(c *gin.Context) {
	if h.crons == nil {
		fail(c, http.StatusServiceUnavailable, "定时任务服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.crons.Delete(c.Request.Context(), id); err != nil {
		h.writeCronError(c, err, "删除定时任务失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// RunCronJobNow 手动立即执行一次指定 Cron 任务。
func (h *Handler) RunCronJobNow(c *gin.Context) {
	if h.crons == nil {
		fail(c, http.StatusServiceUnavailable, "定时任务服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	runOut, err := h.crons.ExecuteJob(c.Request.Context(), id, "manual")
	if err != nil {
		h.writeCronError(c, err, "立即执行定时任务失败")
		return
	}
	job, getErr := h.crons.Get(c.Request.Context(), id)
	var jobDTO *cronJobDTO
	if getErr == nil {
		d := h.toCronJobDTO(job)
		jobDTO = &d
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":     runOut.Status != service.CronStatusError,
		"result": runOut,
		"job":    jobDTO,
	})
}

// PreviewCronExpr 校验 Cron 语句并返回未来 N 次计划触发时间。
func (h *Handler) PreviewCronExpr(c *gin.Context) {
	if h.crons == nil {
		fail(c, http.StatusServiceUnavailable, "定时任务服务未启用")
		return
	}
	var req previewCronRequest
	if !bindJSON(c, &req) {
		return
	}
	count := req.Count
	if count <= 0 || count > 10 {
		count = 5
	}
	c.JSON(http.StatusOK, h.crons.Preview(req.CronExpr, req.Timezone, count))
}

// ResetCronJobs 恢复默认内置 Cron 任务（补齐缺失的默认任务，不影响自定义任务）。
func (h *Handler) ResetCronJobs(c *gin.Context) {
	if h.crons == nil {
		fail(c, http.StatusServiceUnavailable, "定时任务服务未启用")
		return
	}
	jobs, err := h.crons.ResetBuiltin(c.Request.Context())
	if err != nil {
		h.failInternal(c, "恢复默认定时任务失败", err)
		return
	}
	items := make([]cronJobDTO, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, h.toCronJobDTO(job))
	}
	c.JSON(http.StatusOK, gin.H{
		"items":   items,
		"total":   len(items),
		"message": "已恢复默认内置定时任务",
	})
}

func (h *Handler) writeCronError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, service.ErrCronJobNotFound), isNotFound(err):
		fail(c, http.StatusNotFound, "定时任务不存在")
	case errors.Is(err, service.ErrCronJobAlreadyRunning):
		fail(c, http.StatusConflict, "该定时任务正在执行中，请等待本次运行结束")
	case errors.Is(err, service.ErrInvalidCronJob):
		fail(c, http.StatusBadRequest, err.Error())
	default:
		msg := err.Error()
		if strings.Contains(msg, "不能为空") ||
			strings.Contains(msg, "无效") ||
			strings.Contains(msg, "不支持") ||
			strings.Contains(msg, "必须") ||
			strings.Contains(msg, "不存在") ||
			strings.Contains(msg, "cron") {
			fail(c, http.StatusBadRequest, msg)
			return
		}
		h.failInternal(c, fallback, err)
	}
}
