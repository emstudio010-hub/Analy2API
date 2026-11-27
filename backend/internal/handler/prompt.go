package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/apikey"
	"cmd2api/ent/group"
	"cmd2api/ent/prompttemplate"
	"cmd2api/internal/domain"
	"cmd2api/internal/prompt"
	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

type promptTemplateDTO struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Content     string    `json:"content"`
	Description string    `json:"description"`
	IsBuiltin   bool      `json:"is_builtin"`
	Priority    int       `json:"priority"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toPromptTemplateDTO(row *ent.PromptTemplate) promptTemplateDTO {
	return promptTemplateDTO{
		ID:          row.ID,
		Name:        row.Name,
		Category:    row.Category,
		Content:     row.Content,
		Description: row.Description,
		IsBuiltin:   row.IsBuiltin,
		Priority:    row.Priority,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

// GetPromptConfig 返回全局底线提示词配置、全部分组策略与提示词模板库（GET /api/prompts/config）。
func (h *Handler) GetPromptConfig(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}

	ctx := c.Request.Context()
	cfgOut, err := svc.GetConfig(ctx)
	if err != nil {
		h.failInternal(c, "查询提示词配置失败", err)
		return
	}

	accCounts := make(map[int64]int, len(cfgOut.Groups))
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
	keyCounts := make(map[int64]int, len(cfgOut.Groups))
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

	grpDTOs := make([]groupDTO, 0, len(cfgOut.Groups))
	for _, g := range cfgOut.Groups {
		grpDTOs = append(grpDTOs, h.attachToolObfToGroupDTO(toGroupDTO(g, accCounts[g.ID], keyCounts[g.ID])))
	}

	tplDTOs := make([]promptTemplateDTO, 0, len(cfgOut.Templates))
	for _, tpl := range cfgOut.Templates {
		tplDTOs = append(tplDTOs, toPromptTemplateDTO(tpl))
	}

	c.JSON(http.StatusOK, gin.H{
		"global_enabled": cfgOut.GlobalEnabled,
		"global_prompt":  cfgOut.GlobalPrompt,
		"groups":         grpDTOs,
		"templates":      tplDTOs,
	})
}

type updateGlobalPromptRequest struct {
	Enabled       *bool   `json:"enabled"`
	GlobalEnabled *bool   `json:"global_enabled"`
	Prompt        *string `json:"prompt"`
	GlobalPrompt  *string `json:"global_prompt"`
}

// UpdateGlobalPrompt 更新全局底线系统提示词开关与正文并立即热生效（PUT /api/prompts/global）。
func (h *Handler) UpdateGlobalPrompt(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}

	var req updateGlobalPromptRequest
	if !bindJSON(c, &req) {
		return
	}

	cur := svc.Engine().Global()
	enabled := cur.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else if req.GlobalEnabled != nil {
		enabled = *req.GlobalEnabled
	}

	content := cur.Prompt
	if req.Prompt != nil {
		content = *req.Prompt
	} else if req.GlobalPrompt != nil {
		content = *req.GlobalPrompt
	}

	if err := svc.UpdateGlobal(c.Request.Context(), enabled, content); err != nil {
		h.failInternal(c, "保存全局提示词失败", err)
		return
	}

	if h.cfg != nil {
		h.cfg.CommandCC.GlobalSystemPromptEnabled = enabled
		h.cfg.CommandCC.GlobalSystemPrompt = content
	}

	c.JSON(http.StatusOK, gin.H{
		"enabled":        enabled,
		"global_enabled": enabled,
		"prompt":         content,
		"global_prompt":  content,
		"message":        "全局底线提示词已更新并热生效",
	})
}

// ListPromptTemplates 返回全部提示词模板列表（GET /api/prompts/templates）。
func (h *Handler) ListPromptTemplates(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}

	rows, err := svc.ListTemplates(c.Request.Context())
	if err != nil {
		h.failInternal(c, "查询提示词模板失败", err)
		return
	}
	items := make([]promptTemplateDTO, 0, len(rows))
	for _, r := range rows {
		items = append(items, toPromptTemplateDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
	})
}

type promptTemplatePayloadRequest struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Content     string `json:"content"`
	Description string `json:"description"`
	Priority    *int   `json:"priority"`
}

// CreatePromptTemplate 新建提示词模板（POST /api/prompts/templates）。
func (h *Handler) CreatePromptTemplate(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}

	var req promptTemplatePayloadRequest
	if !bindJSON(c, &req) {
		return
	}

	row, err := svc.CreateTemplate(c.Request.Context(), service.CreatePromptTemplateInput{
		Name:        req.Name,
		Category:    req.Category,
		Content:     req.Content,
		Description: req.Description,
		Priority:    req.Priority,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusCreated, toPromptTemplateDTO(row))
}

type updatePromptTemplateRequest struct {
	Name        *string `json:"name"`
	Category    *string `json:"category"`
	Content     *string `json:"content"`
	Description *string `json:"description"`
	Priority    *int    `json:"priority"`
}

// UpdatePromptTemplate 修改提示词模板（PUT /api/prompts/templates/:id）。
func (h *Handler) UpdatePromptTemplate(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req updatePromptTemplateRequest
	if !bindJSON(c, &req) {
		return
	}

	updated, err := svc.UpdateTemplate(c.Request.Context(), id, service.UpdatePromptTemplateInput{
		Name:        req.Name,
		Category:    req.Category,
		Content:     req.Content,
		Description: req.Description,
		Priority:    req.Priority,
	})
	if err != nil {
		if errors.Is(err, service.ErrPromptTemplateNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, toPromptTemplateDTO(updated))
}

// DeletePromptTemplate 删除提示词模板（DELETE /api/prompts/templates/:id）。
func (h *Handler) DeletePromptTemplate(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	if err := svc.DeleteTemplate(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrPromptTemplateNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		h.failInternal(c, "删除提示词模板失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "已删除"})
}

// ResetPromptTemplates 恢复出厂内置提示词模板库（POST /api/prompts/templates/reset）。
func (h *Handler) ResetPromptTemplates(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}

	rows, err := svc.ResetTemplates(c.Request.Context())
	if err != nil {
		h.failInternal(c, "重置提示词模板失败", err)
		return
	}

	items := make([]promptTemplateDTO, 0, len(rows))
	for _, r := range rows {
		items = append(items, toPromptTemplateDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{
		"items":   items,
		"total":   len(items),
		"message": "已恢复默认内置系统提示词模板",
	})
}

type updateGroupPromptRequest struct {
	PromptInjectMode    string             `json:"prompt_inject_mode"`
	SystemPrompt        string             `json:"system_prompt"`
	PromptTemplateID    *int64             `json:"prompt_template_id"`
	ToolObfMode         *string            `json:"tool_obf_mode"`
	ToolObfPrefix       *string            `json:"tool_obf_prefix"`
	ToolObfScope        *string            `json:"tool_obf_scope"`
	ToolObfCustomMap    *map[string]string `json:"tool_obf_custom_map"`
	ToolObfExcludeTools *[]string          `json:"tool_obf_exclude_tools"`
	ToolObfRewriteRefs  *bool              `json:"tool_obf_rewrite_refs"`
}

// UpdateGroupPrompt 单独更新指定分组的前置提示词策略（及可选分组工具混淆策略）并热加载引擎（PUT /api/prompts/groups/:id）。
func (h *Handler) UpdateGroupPrompt(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req updateGroupPromptRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	updated, err := svc.UpdateGroupPrompt(ctx, id, req.PromptInjectMode, req.SystemPrompt, req.PromptTemplateID)
	if err != nil {
		if errors.Is(err, service.ErrGroupNotFound) || errors.Is(err, service.ErrPromptTemplateNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	gReq := groupRequest{
		ToolObfMode:         req.ToolObfMode,
		ToolObfPrefix:       req.ToolObfPrefix,
		ToolObfScope:        req.ToolObfScope,
		ToolObfCustomMap:    req.ToolObfCustomMap,
		ToolObfExcludeTools: req.ToolObfExcludeTools,
		ToolObfRewriteRefs:  req.ToolObfRewriteRefs,
	}
	if err := gReq.applyToolObfOverride(ctx, h, id); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	dto, cErr := h.buildGroupDTOWithCounts(ctx, updated)
	if cErr != nil {
		dto = h.attachToolObfToGroupDTO(toGroupDTO(updated, 0, 0))
	}
	c.JSON(http.StatusOK, dto)
}

type promptPreviewRequest struct {
	GroupID      *int64 `json:"group_id"`
	ClientSystem string `json:"client_system"`
	Model        string `json:"model"`
	Protocol     string `json:"protocol"`
}

// PreviewPromptInjection 在线演练客户端 System 脱敏清洗与多级前置提示词合成结果（POST /api/prompts/preview）。
func (h *Handler) PreviewPromptInjection(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}

	var req promptPreviewRequest
	if !bindJSON(c, &req) {
		return
	}

	var gid int64
	if req.GroupID != nil {
		gid = *req.GroupID
	}

	res, err := svc.Preview(c.Request.Context(), gid, req.ClientSystem, req.Model, req.Protocol)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// GetPromptCatalog 返回主流编程平台系统提示词图鉴库（GET /api/prompts/catalog）。
func (h *Handler) GetPromptCatalog(c *gin.Context) {
	platform := strings.TrimSpace(c.Query("platform"))
	category := strings.TrimSpace(c.Query("category"))
	keyword := strings.TrimSpace(c.Query("keyword"))
	if keyword == "" {
		keyword = strings.TrimSpace(c.Query("q"))
	}
	if keyword == "" {
		keyword = strings.TrimSpace(c.Query("search"))
	}

	items, platforms := prompt.FilterCatalog(prompt.CatalogFilter{
		Platform: platform,
		Category: category,
		Keyword:  keyword,
	})
	c.JSON(http.StatusOK, gin.H{
		"items":     items,
		"total":     len(items),
		"platforms": platforms,
	})
}

type applyPromptCatalogRequest struct {
	CatalogID  string `json:"catalog_id"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	GroupID    *int64 `json:"group_id"`
	Platform   string `json:"platform"`
	InjectMode string `json:"inject_mode"`
	Enabled    *bool  `json:"enabled"`
	Priority   *int   `json:"priority"`
}

// ApplyPromptCatalogItem 从平台提示词图鉴一键创建或更新模板，并支持绑定指定分组或平台分组（POST /api/prompts/catalog/apply）。
func (h *Handler) ApplyPromptCatalogItem(c *gin.Context) {
	svc := h.promptSvc()
	if svc == nil || h.client == nil {
		fail(c, http.StatusServiceUnavailable, "提示词注入服务未初始化")
		return
	}

	var req applyPromptCatalogRequest
	if !bindJSON(c, &req) {
		return
	}

	catalogID := strings.TrimSpace(req.CatalogID)
	if catalogID == "" {
		catalogID = strings.TrimSpace(req.ID)
	}
	if catalogID == "" {
		fail(c, http.StatusBadRequest, "catalog_id 不能为空")
		return
	}

	item, ok := prompt.LookupCatalogItem(catalogID)
	if !ok {
		fail(c, http.StatusNotFound, "未找到指定的平台提示词图鉴条目: "+catalogID)
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	effectiveInjectMode := domain.PromptModeOverrideGlobal
	if item.RecommendedInjectMode != "" && domain.IsValidPromptInjectMode(domain.NormalizePromptInjectMode(item.RecommendedInjectMode)) {
		effectiveInjectMode = domain.NormalizePromptInjectMode(item.RecommendedInjectMode)
	}
	if strings.TrimSpace(req.InjectMode) != "" {
		normMode := domain.NormalizePromptInjectMode(req.InjectMode)
		if !domain.IsValidPromptInjectMode(normMode) {
			fail(c, http.StatusBadRequest, service.ErrInvalidPromptInjectMode.Error())
			return
		}
		effectiveInjectMode = normMode
	}
	if !enabled {
		effectiveInjectMode = domain.PromptModeOff
	}

	tplName := strings.TrimSpace(req.Name)
	if tplName == "" {
		tplName = item.Name
	}
	if len(tplName) > 120 {
		tplName = tplName[:120]
	}

	tplCategory := strings.TrimSpace(req.Category)
	if tplCategory == "" {
		tplCategory = item.Platform
	}
	if len(tplCategory) > 64 {
		tplCategory = tplCategory[:64]
	}

	tplPriority := item.Priority
	if tplPriority <= 0 {
		tplPriority = 50
	}
	if req.Priority != nil && *req.Priority > 0 {
		tplPriority = *req.Priority
	}

	tplDesc := strings.TrimSpace(item.Description)
	if len(tplDesc) > 500 {
		tplDesc = tplDesc[:500]
	}

	ctx := c.Request.Context()
	if req.GroupID != nil && *req.GroupID > 0 {
		grpExists, gErr := h.client.Group.Query().
			Where(group.IDEQ(*req.GroupID), group.DeletedAtIsNil()).
			Exist(ctx)
		if gErr != nil {
			h.failInternal(c, "校验目标分组失败", gErr)
			return
		}
		if !grpExists {
			fail(c, http.StatusNotFound, service.ErrGroupNotFound.Error())
			return
		}
	}

	existingTpl, qErr := h.client.PromptTemplate.Query().
		Where(prompttemplate.NameEQ(tplName)).
		First(ctx)
	var (
		tplRow  *ent.PromptTemplate
		created bool
	)
	if qErr != nil {
		if !ent.IsNotFound(qErr) {
			h.failInternal(c, "查询现有提示词模板失败", qErr)
			return
		}
		var cErr error
		tplRow, cErr = h.client.PromptTemplate.Create().
			SetName(tplName).
			SetCategory(tplCategory).
			SetContent(item.Content).
			SetDescription(tplDesc).
			SetIsBuiltin(false).
			SetPriority(tplPriority).
			Save(ctx)
		if cErr != nil {
			h.failInternal(c, "从图鉴创建提示词模板失败", cErr)
			return
		}
		created = true
	} else {
		var uErr error
		tplRow, uErr = existingTpl.Update().
			SetCategory(tplCategory).
			SetContent(item.Content).
			SetDescription(tplDesc).
			SetPriority(tplPriority).
			Save(ctx)
		if uErr != nil {
			h.failInternal(c, "更新图鉴提示词模板失败", uErr)
			return
		}
		created = false
	}

	boundGroups := make([]groupDTO, 0)
	if req.GroupID != nil && *req.GroupID > 0 {
		updatedGrp, bErr := svc.UpdateGroupPrompt(ctx, *req.GroupID, effectiveInjectMode, "", &tplRow.ID)
		if bErr != nil {
			if errors.Is(bErr, service.ErrGroupNotFound) {
				fail(c, http.StatusNotFound, bErr.Error())
				return
			}
			fail(c, http.StatusBadRequest, bErr.Error())
			return
		}
		dto, dErr := h.buildGroupDTOWithCounts(ctx, updatedGrp)
		if dErr != nil {
			dto = h.attachToolObfToGroupDTO(toGroupDTO(updatedGrp, 0, 0))
		}
		boundGroups = append(boundGroups, dto)
	} else if rawPlat := strings.TrimSpace(req.Platform); rawPlat != "" {
		normPlat := domain.NormalizePlatform(rawPlat)
		if domain.IsValidPlatform(normPlat) {
			platGroups, gErr := h.client.Group.Query().
				Where(group.PlatformEQ(normPlat), group.DeletedAtIsNil()).
				Order(ent.Asc(group.FieldID)).
				All(ctx)
			if gErr == nil {
				for _, pg := range platGroups {
					updatedGrp, bErr := svc.UpdateGroupPrompt(ctx, pg.ID, effectiveInjectMode, "", &tplRow.ID)
					if bErr != nil {
						continue
					}
					dto, dErr := h.buildGroupDTOWithCounts(ctx, updatedGrp)
					if dErr != nil {
						dto = h.attachToolObfToGroupDTO(toGroupDTO(updatedGrp, 0, 0))
					}
					boundGroups = append(boundGroups, dto)
				}
			}
		}
	}

	if err := svc.ReloadEngine(ctx); err != nil {
		h.failInternal(c, "刷新提示词内存引擎失败", err)
		return
	}

	var primaryGroup any
	if len(boundGroups) > 0 {
		primaryGroup = boundGroups[0]
	}

	msg := "已从平台提示词图鉴导入到模板库并热生效"
	if !created {
		msg = "已同步更新图鉴提示词模板并热生效"
	}
	if len(boundGroups) > 0 {
		msg = "已导入平台提示词模板并绑定目标分组生效"
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":           true,
		"created":      created,
		"catalog_id":   item.ID,
		"template":     toPromptTemplateDTO(tplRow),
		"group":        primaryGroup,
		"bound_groups": boundGroups,
		"enabled":      enabled,
		"inject_mode":  effectiveInjectMode,
		"message":      msg,
	})
}
