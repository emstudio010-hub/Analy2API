package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/apikey"
	"cmd2api/ent/group"
	"cmd2api/internal/domain"
	"cmd2api/internal/middleware"
	"cmd2api/internal/toolobf"

	"github.com/gin-gonic/gin"
)

// groupDTO 是分组的对外表示（含前置提示词策略与分组工具名混淆策略）。
type groupDTO struct {
	ID                  int64             `json:"id"`
	Name                string            `json:"name"`
	Description         string            `json:"description"`
	Platform            string            `json:"platform"`
	RateMultiplier      float64           `json:"rate_multiplier"`
	Status              string            `json:"status"`
	SystemPrompt        string            `json:"system_prompt"`
	PromptInjectMode    string            `json:"prompt_inject_mode"`
	PromptTemplateID    *int64            `json:"prompt_template_id"`
	ToolObfMode         string            `json:"tool_obf_mode"`
	ToolObfPrefix       string            `json:"tool_obf_prefix,omitempty"`
	ToolObfScope        string            `json:"tool_obf_scope,omitempty"`
	ToolObfCustomMap    map[string]string `json:"tool_obf_custom_map,omitempty"`
	ToolObfExcludeTools []string          `json:"tool_obf_exclude_tools,omitempty"`
	ToolObfRewriteRefs  *bool             `json:"tool_obf_rewrite_refs,omitempty"`
	ToolObfEffectiveOn  bool              `json:"tool_obf_effective_on"`
	AccountCount        int               `json:"account_count"`
	APIKeyCount         int               `json:"api_key_count"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

func toGroupDTO(g *ent.Group, accountCount, keyCount int) groupDTO {
	platform := domain.NormalizePlatform(g.Platform)
	injectMode := domain.NormalizePromptInjectMode(g.PromptInjectMode)
	if !domain.IsValidPromptInjectMode(injectMode) {
		injectMode = domain.PromptModeAppendGlobal
	}
	return groupDTO{
		ID:               g.ID,
		Name:             g.Name,
		Description:      g.Description,
		Platform:         platform,
		RateMultiplier:   g.RateMultiplier,
		Status:           g.Status,
		SystemPrompt:     g.SystemPrompt,
		PromptInjectMode: injectMode,
		PromptTemplateID: g.PromptTemplateID,
		ToolObfMode:      toolobf.GroupModeInherit,
		AccountCount:     accountCount,
		APIKeyCount:      keyCount,
		CreatedAt:        g.CreatedAt,
		UpdatedAt:        g.UpdatedAt,
	}
}

func (h *Handler) attachToolObfToGroupDTO(dto groupDTO) groupDTO {
	svc := h.toolObfSvc()
	if svc == nil {
		return dto
	}
	ov, _, effOn := svc.GetGroupOverride(dto.ID, dto.Platform)
	if ov.Mode == "" {
		ov.Mode = toolobf.GroupModeInherit
	}
	dto.ToolObfMode = ov.Mode
	dto.ToolObfPrefix = ov.Prefix
	dto.ToolObfScope = ov.Scope
	dto.ToolObfCustomMap = ov.CustomMap
	dto.ToolObfExcludeTools = ov.ExcludeTools
	dto.ToolObfRewriteRefs = ov.RewriteToolRefs
	dto.ToolObfEffectiveOn = effOn
	return dto
}

func (h *Handler) buildGroupDTOWithCounts(ctx context.Context, g *ent.Group) (groupDTO, error) {
	accountCount, err := g.QueryAccounts().Where(account.DeletedAtIsNil()).Count(ctx)
	if err != nil {
		return groupDTO{}, err
	}
	keyCount, err := g.QueryAPIKeys().Where(apikey.DeletedAtIsNil()).Count(ctx)
	if err != nil {
		return groupDTO{}, err
	}
	return h.attachToolObfToGroupDTO(toGroupDTO(g, accountCount, keyCount)), nil
}

// ListGroups 返回所有分组，批量统计账号数与密钥数并附带提示词与工具混淆策略字段。
func (h *Handler) ListGroups(c *gin.Context) {
	ctx := c.Request.Context()

	groups, err := h.client.Group.Query().
		Where(group.DeletedAtIsNil()).
		Order(ent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		h.failInternal(c, "查询分组列表失败", err)
		return
	}

	// 批量聚合账号与 API Key 数量，消除逐组 N+1 查询开销。
	accCounts := make(map[int64]int, len(groups))
	if allAccs, qErr := h.client.Account.Query().
		Where(account.DeletedAtIsNil()).
		WithGroups(func(gq *ent.GroupQuery) {
			gq.Where(group.DeletedAtIsNil()).Select(group.FieldID)
		}).
		All(ctx); qErr == nil {
		for _, a := range allAccs {
			for _, g := range a.Edges.Groups {
				accCounts[g.ID]++
			}
		}
	}

	keyCounts := make(map[int64]int, len(groups))
	if allKeys, qErr := h.client.APIKey.Query().
		Where(apikey.DeletedAtIsNil(), apikey.GroupIDNotNil()).
		Select(apikey.FieldGroupID).
		All(ctx); qErr == nil {
		for _, k := range allKeys {
			if k.GroupID != nil {
				keyCounts[*k.GroupID]++
			}
		}
	}

	items := make([]groupDTO, 0, len(groups))
	for _, g := range groups {
		items = append(items, h.attachToolObfToGroupDTO(toGroupDTO(g, accCounts[g.ID], keyCounts[g.ID])))
	}

	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

type groupRequest struct {
	Name                string             `json:"name" binding:"required"`
	Description         string             `json:"description"`
	Platform            string             `json:"platform"`
	RateMultiplier      float64            `json:"rate_multiplier"`
	Status              string             `json:"status"`
	SystemPrompt        *string            `json:"system_prompt"`
	PromptInjectMode    *string            `json:"prompt_inject_mode"`
	PromptTemplateID    *int64             `json:"prompt_template_id"`
	ToolObfMode         *string            `json:"tool_obf_mode"`
	ToolObfPrefix       *string            `json:"tool_obf_prefix"`
	ToolObfScope        *string            `json:"tool_obf_scope"`
	ToolObfCustomMap    *map[string]string `json:"tool_obf_custom_map"`
	ToolObfExcludeTools *[]string          `json:"tool_obf_exclude_tools"`
	ToolObfRewriteRefs  *bool              `json:"tool_obf_rewrite_refs"`
}

func (req *groupRequest) applyToolObfOverride(ctx context.Context, h *Handler, groupID int64) error {
	if req.ToolObfMode == nil && req.ToolObfPrefix == nil && req.ToolObfScope == nil &&
		req.ToolObfCustomMap == nil && req.ToolObfExcludeTools == nil && req.ToolObfRewriteRefs == nil {
		return nil
	}
	svc := h.toolObfSvc()
	if svc == nil {
		return nil
	}
	cur, _, _ := svc.GetGroupOverride(groupID, "")
	if req.ToolObfMode != nil {
		cur.Mode = *req.ToolObfMode
	}
	if req.ToolObfPrefix != nil {
		cur.Prefix = *req.ToolObfPrefix
	}
	if req.ToolObfScope != nil {
		cur.Scope = *req.ToolObfScope
	}
	if req.ToolObfCustomMap != nil {
		cur.CustomMap = *req.ToolObfCustomMap
	}
	if req.ToolObfExcludeTools != nil {
		cur.ExcludeTools = *req.ToolObfExcludeTools
	}
	if req.ToolObfRewriteRefs != nil {
		b := *req.ToolObfRewriteRefs
		cur.RewriteToolRefs = &b
	}
	_, err := svc.UpdateGroupOverride(ctx, groupID, cur)
	return err
}

// CreateGroup 新建分组（支持 workbuddy / antigravity / commandcode / opencode 四大平台及提示词策略）。
func (h *Handler) CreateGroup(c *gin.Context) {
	var req groupRequest
	if !bindJSON(c, &req) {
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "分组名不能为空")
		return
	}

	platform := domain.NormalizePlatform(req.Platform)
	if !domain.IsValidPlatform(platform) {
		fail(c, http.StatusBadRequest, "平台取值无效，只支持 workbuddy、antigravity、commandcode 或 opencode")
		return
	}

	injectMode := domain.PromptModeAppendGlobal
	if req.PromptInjectMode != nil {
		injectMode = domain.NormalizePromptInjectMode(*req.PromptInjectMode)
		if !domain.IsValidPromptInjectMode(injectMode) {
			fail(c, http.StatusBadRequest, "提示词注入模式无效，仅支持 append_global、override_global、replace_all 或 off")
			return
		}
	}

	multiplier := req.RateMultiplier
	if multiplier <= 0 {
		multiplier = 1
	}
	status := req.Status
	if status == "" {
		status = domain.StatusActive
	}

	ctx := c.Request.Context()
	builder := h.client.Group.Create().
		SetName(name).
		SetDescription(req.Description).
		SetPlatform(platform).
		SetRateMultiplier(multiplier).
		SetStatus(status).
		SetPromptInjectMode(injectMode)

	if req.SystemPrompt != nil {
		builder = builder.SetSystemPrompt(*req.SystemPrompt)
	}
	if req.PromptTemplateID != nil && *req.PromptTemplateID > 0 {
		builder = builder.SetPromptTemplateID(*req.PromptTemplateID)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		if isConstraint(err) {
			fail(c, http.StatusConflict, "分组名已存在")
			return
		}
		h.failInternal(c, "创建分组失败", err)
		return
	}

	if err := req.applyToolObfOverride(ctx, h, created.ID); err != nil {
		h.logger.Warn("保存新建分组工具混淆覆盖失败", "group_id", created.ID, "err", err)
	}

	if ps := h.promptSvc(); ps != nil {
		_ = ps.ReloadEngine(ctx)
	}

	c.JSON(http.StatusCreated, h.attachToolObfToGroupDTO(toGroupDTO(created, 0, 0)))
}

// UpdateGroup 修改分组。
func (h *Handler) UpdateGroup(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req groupRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()

	// 平台创建后不可更改：改了意味着组内已有的账号全部平台不匹配。
	if req.Platform != "" {
		current, err := h.client.Group.Query().
			Where(group.IDEQ(id), group.DeletedAtIsNil()).
			Only(ctx)
		if err != nil {
			if isNotFound(err) {
				fail(c, http.StatusNotFound, "分组不存在")
				return
			}
			h.failInternal(c, "查询分组失败", err)
			return
		}
		currentPlatform := domain.NormalizePlatform(current.Platform)
		reqPlatform := domain.NormalizePlatform(req.Platform)
		if !domain.IsValidPlatform(reqPlatform) {
			fail(c, http.StatusBadRequest, "平台取值无效，只支持 workbuddy、antigravity、commandcode 或 opencode")
			return
		}
		if reqPlatform != currentPlatform {
			fail(c, http.StatusBadRequest,
				"分组平台创建后不可更改（当前 "+currentPlatform+"）。如需换平台，请新建一个分组并把账号迁过去。")
			return
		}
	}

	builder := h.client.Group.UpdateOneID(id)

	if name := strings.TrimSpace(req.Name); name != "" {
		builder = builder.SetName(name)
	}
	builder = builder.SetDescription(req.Description)
	if req.RateMultiplier > 0 {
		builder = builder.SetRateMultiplier(req.RateMultiplier)
	}
	if req.Status != "" {
		builder = builder.SetStatus(req.Status)
	}
	if req.SystemPrompt != nil {
		builder = builder.SetSystemPrompt(*req.SystemPrompt)
	}
	if req.PromptInjectMode != nil {
		mode := domain.NormalizePromptInjectMode(*req.PromptInjectMode)
		if !domain.IsValidPromptInjectMode(mode) {
			fail(c, http.StatusBadRequest, "提示词注入模式无效，仅支持 append_global、override_global、replace_all 或 off")
			return
		}
		builder = builder.SetPromptInjectMode(mode)
	}
	if req.PromptTemplateID != nil {
		if *req.PromptTemplateID > 0 {
			builder = builder.SetPromptTemplateID(*req.PromptTemplateID)
		} else {
			builder = builder.ClearPromptTemplateID()
		}
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		if isNotFound(err) {
			fail(c, http.StatusNotFound, "分组不存在")
			return
		}
		if isConstraint(err) {
			fail(c, http.StatusConflict, "分组名已存在")
			return
		}
		h.failInternal(c, "更新分组失败", err)
		return
	}

	if err := req.applyToolObfOverride(ctx, h, updated.ID); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	if ps := h.promptSvc(); ps != nil {
		_ = ps.ReloadEngine(ctx)
	}
	middleware.InvalidateAPIKeyCache(h.client)
	if h.sched != nil {
		h.sched.InvalidateGroup(updated.ID)
	}

	dto, _ := h.buildGroupDTOWithCounts(ctx, updated)
	if dto.ID == 0 {
		dto = h.attachToolObfToGroupDTO(toGroupDTO(updated, 0, 0))
	}
	c.JSON(http.StatusOK, dto)
}

// DeleteGroup 删除分组。
func (h *Handler) DeleteGroup(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	keyCount, err := h.client.APIKey.Query().
		Where(apikey.GroupIDEQ(id), apikey.DeletedAtIsNil()).
		Count(ctx)
	if err != nil {
		h.failInternal(c, "统计分组密钥数失败", err)
		return
	}
	if keyCount > 0 {
		fail(c, http.StatusConflict, "该分组下还有 API Key，请先迁移或删除这些 Key")
		return
	}

	if err := h.client.Group.UpdateOneID(id).
		SetDeletedAt(time.Now()).
		Exec(ctx); err != nil {
		if isNotFound(err) {
			fail(c, http.StatusNotFound, "分组不存在")
			return
		}
		h.failInternal(c, "删除分组失败", err)
		return
	}

	if ps := h.promptSvc(); ps != nil {
		_ = ps.ReloadEngine(ctx)
	}
	middleware.InvalidateAPIKeyCache(h.client)
	if h.sched != nil {
		h.sched.InvalidateGroup(id)
	}

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}
