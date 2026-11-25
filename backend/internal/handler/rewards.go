package handler

import (
	"net/http"

	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

// GetRewardsOverview 返回四大平台领奖、额度同步与保活状态汇总（GET /api/rewards/overview）。
func (h *Handler) GetRewardsOverview(c *gin.Context) {
	svc := h.rewardsSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "领奖与保活服务未初始化")
		return
	}

	ctx := c.Request.Context()
	ov, err := svc.Overview(ctx)
	if err != nil {
		h.failInternal(c, "查询领奖与保活概览失败", err)
		return
	}

	accDTOs := make([]accountDTO, 0, len(ov.Accounts))
	for _, acc := range ov.Accounts {
		accDTOs = append(accDTOs, h.toAccountDTO(acc))
	}

	cronItems := make([]cronJobDTO, 0)
	if h.crons != nil {
		if views, cErr := h.crons.List(ctx); cErr == nil && views != nil {
			for _, j := range views {
				cronItems = append(cronItems, h.toCronJobDTO(j))
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"summary":     ov.Summary,
		"by_platform": ov.ByPlatform,
		"platforms":   ov.Platforms,
		"accounts":    accDTOs,
		"cron_jobs":   cronItems,
	})
}

type runRewardsActionRequest struct {
	Platform   string  `json:"platform"`
	Action     string  `json:"action"`
	AccountIDs []int64 `json:"account_ids"`
	GroupID    *int64  `json:"group_id"`
	DoChat     bool    `json:"do_chat"`
}

// RunRewardsAction 触发单平台或全平台签到领奖、额度刷新、OAuth 刷新或探活巡检（POST /api/rewards/run）。
func (h *Handler) RunRewardsAction(c *gin.Context) {
	svc := h.rewardsSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "领奖与保活服务未初始化")
		return
	}

	var req runRewardsActionRequest
	if !bindJSON(c, &req) {
		return
	}

	out, err := svc.RunAction(c.Request.Context(), service.RunRewardsActionInput{
		Platform:   req.Platform,
		Action:     req.Action,
		AccountIDs: req.AccountIDs,
		GroupID:    req.GroupID,
		DoChat:     req.DoChat,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}
