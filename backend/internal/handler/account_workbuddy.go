package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

type scanLocalRequest struct {
	GroupIDs               []int64 `json:"group_ids"`
	AutoAssignDefaultGroup *bool   `json:"auto_assign_default_group"`
}

type proxyTestRequest struct {
	ProxyURL *string `json:"proxy_url"`
}

type loginLinkRequest struct {
	Target string `json:"target"`
}

type harvestRequest struct {
	DoChat *bool `json:"do_chat"`
}

// bindOptionalJSON 在请求体非空时解析 JSON；空请求体（EOF）视为全零值请求。
func bindOptionalJSON(c *gin.Context, dst any) bool {
	if c.Request == nil || c.Request.Body == nil || c.Request.ContentLength == 0 {
		return true
	}
	if err := c.ShouldBindJSON(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		fail(c, http.StatusBadRequest, "请求体格式不正确: "+err.Error())
		return false
	}
	return true
}

// ScanLocalAccounts 一键扫描本机 WorkBuddy / CodeBuddy 客户端账号文件并入库。
func (h *Handler) ScanLocalAccounts(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	var req scanLocalRequest
	if !bindOptionalJSON(c, &req) {
		return
	}
	out, err := h.accounts.ScanLocal(c.Request.Context(), service.ScanLocalInput{
		GroupIDs:               req.GroupIDs,
		AutoAssignDefaultGroup: req.AutoAssignDefaultGroup,
	})
	if err != nil {
		h.writeAccountError(c, err, "扫描本机账号失败")
		return
	}

	dtos := make([]accountDTO, 0, len(out.Accounts))
	for _, acc := range out.Accounts {
		dtos = append(dtos, h.toAccountDTO(acc))
	}
	sources := out.Sources
	if sources == nil {
		sources = []string{}
	}
	errs := out.Errors
	if errs == nil {
		errs = []string{}
	}

	c.JSON(http.StatusOK, gin.H{
		"imported": out.Imported,
		"updated":  out.Updated,
		"sources":  sources,
		"accounts": dtos,
		"errors":   errs,
	})
}

// ExportAccounts 导出账号配置备份 JSON（含 WorkBuddy 凭证、设备指纹与代理）。
func (h *Handler) ExportAccounts(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	out, err := h.accounts.ExportAccounts(c.Request.Context())
	if err != nil {
		h.failInternal(c, "导出账号失败", err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// TestAccountProxy 验证账号独立出口代理的连通性与出口 IP。
func (h *Handler) TestAccountProxy(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req proxyTestRequest
	if !bindOptionalJSON(c, &req) {
		return
	}
	res, err := h.accounts.TestAccountProxy(c.Request.Context(), id, req.ProxyURL)
	if err != nil {
		h.writeAccountError(c, err, "测试代理失败")
		return
	}
	c.JSON(http.StatusOK, res)
}

// RotateAccountDevice 轮换生成一对全新的 Qimei36 与 MachineID 并持久化。
func (h *Handler) RotateAccountDevice(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	out, err := h.accounts.RotateDevice(c.Request.Context(), id)
	if err != nil {
		h.writeAccountError(c, err, "轮换设备指纹失败")
		return
	}
	var dto *accountDTO
	if out.Account != nil {
		converted := h.toAccountDTO(out.Account)
		dto = &converted
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":         out.OK,
		"qimei36":    out.Qimei36,
		"machine_id": out.MachineID,
		"account":    dto,
	})
}

// IssueAccountLoginLink 获取一次性 WorkBuddy 官网免密登录链接（300s 有效）。
func (h *Handler) IssueAccountLoginLink(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req loginLinkRequest
	if !bindOptionalJSON(c, &req) {
		return
	}
	res, err := h.accounts.IssueLoginLink(c.Request.Context(), id, req.Target)
	if err != nil {
		if isNotFound(err) {
			fail(c, http.StatusNotFound, "账号不存在")
			return
		}
		if strings.Contains(err.Error(), "Token") || strings.Contains(err.Error(), "不能为空") {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"device_code": res.DeviceCode,
		"expires_in":  res.ExpiresIn,
		"login_url":   res.LoginURL,
		"target":      res.Target,
	})
}

// RefreshAccountToken 使用账号的 RefreshToken 向 WorkBuddy 换取新 AccessToken。
func (h *Handler) RefreshAccountToken(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	out, err := h.accounts.RefreshToken(c.Request.Context(), id)
	if err != nil {
		h.writeAccountError(c, err, "续期 Token 失败")
		return
	}
	var dto *accountDTO
	if out.Account != nil {
		converted := h.toAccountDTO(out.Account)
		dto = &converted
	}
	resp := gin.H{
		"ok":      out.OK,
		"account": dto,
	}
	if out.Error != "" {
		resp["error"] = out.Error
	}
	c.JSON(http.StatusOK, resp)
}

// HarvestAccount 对单个账号执行完整收割任务（每日签到/每日对话/资料/新手任务）。
func (h *Handler) HarvestAccount(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req harvestRequest
	if !bindOptionalJSON(c, &req) {
		return
	}
	doChat := true
	if req.DoChat != nil {
		doChat = *req.DoChat
	}

	out, err := h.accounts.HarvestAccount(c.Request.Context(), id, doChat)
	if err != nil {
		h.writeAccountError(c, err, "执行收割任务失败")
		return
	}
	steps := out.Steps
	if steps == nil {
		steps = []service.HarvestStepInfo{}
	}
	var dto *accountDTO
	if out.Account != nil {
		converted := h.toAccountDTO(out.Account)
		dto = &converted
	}
	resp := gin.H{
		"ok":            out.OK,
		"credit_gained": out.CreditGained,
		"total_credits": out.TotalCredits,
		"steps":         steps,
		"account":       dto,
	}
	if out.Error != "" {
		resp["error"] = out.Error
	}
	c.JSON(http.StatusOK, resp)
}

// HarvestAllAccounts 对账号池全部有效账号依次执行自动化积分收割。
func (h *Handler) HarvestAllAccounts(c *gin.Context) {
	if h.accounts == nil {
		fail(c, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	var req harvestRequest
	if !bindOptionalJSON(c, &req) {
		return
	}
	doChat := true
	if req.DoChat != nil {
		doChat = *req.DoChat
	}

	out, err := h.accounts.HarvestAllAccounts(c.Request.Context(), doChat)
	if err != nil {
		h.failInternal(c, "批量收割任务失败", err)
		return
	}
	items := out.Items
	if items == nil {
		items = []service.HarvestAllItemOutput{}
	}
	dtos := make([]accountDTO, 0, len(out.Accounts))
	for _, acc := range out.Accounts {
		dtos = append(dtos, h.toAccountDTO(acc))
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":            out.OK,
		"total":         out.Total,
		"succeeded":     out.Succeeded,
		"failed":        out.Failed,
		"credit_gained": out.CreditGained,
		"items":         items,
		"accounts":      dtos,
	})
}
