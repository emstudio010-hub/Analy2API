package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/group"
	"cmd2api/ent/prompttemplate"
	"cmd2api/ent/systemsetting"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/prompt"
)

const (
	// SettingKeyGlobalPromptEnabled 控制是否启用全局底线系统提示词注入。
	SettingKeyGlobalPromptEnabled = "global_prompt_enabled"
	// SettingKeyGlobalPromptContent 存储全局底线系统提示词正文。
	SettingKeyGlobalPromptContent = "global_prompt_content"
)

var (
	// ErrInvalidPromptTemplate 表示提示词模板参数不合法。
	ErrInvalidPromptTemplate = errors.New("提示词模板名称和内容不能为空")
	// ErrPromptTemplateNotFound 表示指定的提示词模板不存在。
	ErrPromptTemplateNotFound = errors.New("提示词模板不存在")
	// ErrInvalidPromptInjectMode 表示分组提示词注入模式不受支持。
	ErrInvalidPromptInjectMode = errors.New("提示词注入模式无效，仅支持 append_global、override_global、replace_all 或 off")
	// ErrGroupNotFound 表示分组不存在或已软删除。
	ErrGroupNotFound = errors.New("分组不存在")
)

// CreatePromptTemplateInput 是新建提示词模板的入参。
type CreatePromptTemplateInput struct {
	Name        string
	Category    string
	Content     string
	Description string
	Priority    *int
}

// UpdatePromptTemplateInput 是局部更新提示词模板的入参。
type UpdatePromptTemplateInput struct {
	Name        *string
	Category    *string
	Content     *string
	Description *string
	Priority    *int
}

// PromptConfigOutput 对齐前端 PromptConfigResponse 结构。
type PromptConfigOutput struct {
	GlobalEnabled bool                  `json:"global_enabled"`
	GlobalPrompt  string                `json:"global_prompt"`
	Groups        []*ent.Group          `json:"groups"`
	Templates     []*ent.PromptTemplate `json:"templates"`
}

// PromptPreviewOutput 对齐前端 PromptPreviewResponse 结构。
type PromptPreviewOutput struct {
	FinalSystem           string           `json:"final_system"`
	SanitizedClientSystem string           `json:"sanitized_client_system"`
	LayersApplied         []string         `json:"layers_applied"`
	ReplaceClient         bool             `json:"replace_client"`
	InjectMode            string           `json:"inject_mode"`
	FilterHits            []filter.RuleHit `json:"filter_hits"`
}

// PromptService 负责全局提示词配置、内置/自定义模板管理、分组提示词绑定与内存引擎热同步。
type PromptService struct {
	db           *ent.Client
	promptEngine *prompt.Engine
	filterEngine *filter.Engine
	logger       *slog.Logger
}

// NewPromptService 构造 PromptService。
func NewPromptService(
	db *ent.Client,
	promptEngine *prompt.Engine,
	filterEngine *filter.Engine,
	logger *slog.Logger,
) *PromptService {
	if promptEngine == nil {
		promptEngine = prompt.NewDefaultEngine()
	}
	if filterEngine == nil {
		filterEngine = filter.NewDefaultEngine()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PromptService{
		db:           db,
		promptEngine: promptEngine,
		filterEngine: filterEngine,
		logger:       logger,
	}
}

// Engine 返回共享的提示词注入引擎实例。
func (s *PromptService) Engine() *prompt.Engine {
	return s.promptEngine
}

// EnsureSeeded 幂等初始化内置提示词模板与全局提示词默认设置，并热加载到内存引擎。
func (s *PromptService) EnsureSeeded(ctx context.Context) error {
	if err := s.syncBuiltinTemplates(ctx); err != nil {
		return err
	}
	if err := s.ensureSettingDefault(ctx, SettingKeyGlobalPromptEnabled, "true", "是否开启全局底线系统提示词注入"); err != nil {
		return err
	}
	if err := s.ensureSettingDefault(ctx, SettingKeyGlobalPromptContent, prompt.DefaultGlobalPrompt(), "全局底线系统提示词正文"); err != nil {
		return err
	}
	// 平滑迁移旧版自动创建的 antigravity 默认分组：若仍处于旧默认文案且未手写分组提示词，自动对齐为 append_global，避免 WB 开了而 AGY 没开
	_, _ = s.db.Group.Update().
		Where(
			group.DeletedAtIsNil(),
			group.PlatformEQ(domain.PlatformAntigravity),
			group.PromptInjectModeEQ(domain.PromptModeOff),
			group.DescriptionContains("默认关闭额外提示词注入"),
		).
		SetPromptInjectMode(domain.PromptModeAppendGlobal).
		SetDescription("默认 AntiGravity 容器池分组").
		Save(ctx)
	return s.ReloadEngine(ctx)
}

func (s *PromptService) syncBuiltinTemplates(ctx context.Context) error {
	existingRows, err := s.db.PromptTemplate.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("查询提示词模板失败: %w", err)
	}
	byName := make(map[string]*ent.PromptTemplate, len(existingRows))
	for _, r := range existingRows {
		byName[r.Name] = r
	}

	defaults := prompt.DefaultTemplates()
	for _, spec := range defaults {
		category := strings.TrimSpace(spec.Category)
		if category == "" {
			category = "general"
		}
		priority := spec.Priority
		if priority <= 0 {
			priority = 100
		}

		existing, ok := byName[spec.Name]
		if !ok {
			if _, err := s.db.PromptTemplate.Create().
				SetName(spec.Name).
				SetCategory(category).
				SetContent(spec.Content).
				SetDescription(spec.Description).
				SetIsBuiltin(true).
				SetPriority(priority).
				Save(ctx); err != nil {
				return fmt.Errorf("播种内置提示词模板 %q 失败: %w", spec.Name, err)
			}
			continue
		}
		if !existing.IsBuiltin {
			continue
		}
		if existing.Category != category ||
			existing.Content != spec.Content ||
			existing.Description != spec.Description ||
			existing.Priority != priority {
			if _, err := existing.Update().
				SetCategory(category).
				SetContent(spec.Content).
				SetDescription(spec.Description).
				SetPriority(priority).
				Save(ctx); err != nil {
				return fmt.Errorf("更新内置提示词模板 %q 失败: %w", spec.Name, err)
			}
		}
	}
	return nil
}

func (s *PromptService) ensureSettingDefault(ctx context.Context, key, defValue, description string) error {
	exists, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(key)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("查询系统配置 %q 失败: %w", key, err)
	}
	if exists {
		return nil
	}
	_, err = s.db.SystemSetting.Create().
		SetKey(key).
		SetValue(defValue).
		SetDescription(description).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("写入默认系统配置 %q 失败: %w", key, err)
	}
	return nil
}

func (s *PromptService) upsertSetting(ctx context.Context, key, val, description string) error {
	existing, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(key)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			_, cErr := s.db.SystemSetting.Create().
				SetKey(key).
				SetValue(val).
				SetDescription(description).
				Save(ctx)
			return cErr
		}
		return err
	}
	_, err = existing.Update().SetValue(val).Save(ctx)
	return err
}

func (s *PromptService) loadGlobalSettings(ctx context.Context) (prompt.GlobalConfig, error) {
	rows, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyIn(SettingKeyGlobalPromptEnabled, SettingKeyGlobalPromptContent)).
		All(ctx)
	if err != nil {
		return prompt.GlobalConfig{}, fmt.Errorf("读取全局提示词配置失败: %w", err)
	}

	enabled := true
	content := prompt.DefaultGlobalPrompt()
	for _, r := range rows {
		switch r.Key {
		case SettingKeyGlobalPromptEnabled:
			if b, parseErr := strconv.ParseBool(strings.TrimSpace(r.Value)); parseErr == nil {
				enabled = b
			}
		case SettingKeyGlobalPromptContent:
			content = r.Value
		}
	}
	return prompt.GlobalConfig{
		Enabled: enabled,
		Prompt:  content,
	}, nil
}

// ReloadEngine 从数据库重读全局提示词、全部分组策略与模板，原子热刷新到内存引擎。
func (s *PromptService) ReloadEngine(ctx context.Context) error {
	globalCfg, err := s.loadGlobalSettings(ctx)
	if err != nil {
		return err
	}

	templates, err := s.ListTemplates(ctx)
	if err != nil {
		return err
	}
	tplMap := make(map[int64]string, len(templates))
	for _, tpl := range templates {
		tplMap[tpl.ID] = tpl.Content
	}

	groups, err := s.db.Group.Query().
		Where(group.DeletedAtIsNil()).
		Order(ent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("查询分组提示词策略失败: %w", err)
	}

	groupCfgs := make([]prompt.GroupConfig, 0, len(groups))
	for _, g := range groups {
		var tplID *int64
		if g.PromptTemplateID != nil && *g.PromptTemplateID > 0 {
			idCopy := *g.PromptTemplateID
			tplID = &idCopy
		}
		groupCfgs = append(groupCfgs, prompt.GroupConfig{
			GroupID:      g.ID,
			GroupName:    g.Name,
			Platform:     domain.NormalizePlatform(g.Platform),
			Mode:         domain.NormalizePromptInjectMode(g.PromptInjectMode),
			SystemPrompt: g.SystemPrompt,
			TemplateID:   tplID,
		})
	}

	s.promptEngine.Reload(globalCfg, groupCfgs, tplMap)
	return nil
}

// GetConfig 返回全局开关、全局提示词正文、未软删分组列表与全部提示词模板。
func (s *PromptService) GetConfig(ctx context.Context) (*PromptConfigOutput, error) {
	globalCfg, err := s.loadGlobalSettings(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.db.Group.Query().
		Where(group.DeletedAtIsNil()).
		WithAccounts(func(q *ent.AccountQuery) {
			q.Where()
		}).
		WithAPIKeys().
		Order(ent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询分组列表失败: %w", err)
	}
	templates, err := s.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	return &PromptConfigOutput{
		GlobalEnabled: globalCfg.Enabled,
		GlobalPrompt:  globalCfg.Prompt,
		Groups:        groups,
		Templates:     templates,
	}, nil
}

// UpdateGlobal 更新全局提示词开关与正文，写回 system_settings 并热加载引擎。
func (s *PromptService) UpdateGlobal(ctx context.Context, enabled bool, content string) error {
	if err := s.upsertSetting(ctx, SettingKeyGlobalPromptEnabled, strconv.FormatBool(enabled), "是否开启全局底线系统提示词注入"); err != nil {
		return fmt.Errorf("保存全局提示词开关失败: %w", err)
	}
	if err := s.upsertSetting(ctx, SettingKeyGlobalPromptContent, content, "全局底线系统提示词正文"); err != nil {
		return fmt.Errorf("保存全局提示词内容失败: %w", err)
	}
	return s.ReloadEngine(ctx)
}

// ListTemplates 按优先级与 ID 升序返回全部提示词模板。
func (s *PromptService) ListTemplates(ctx context.Context) ([]*ent.PromptTemplate, error) {
	rows, err := s.db.PromptTemplate.Query().
		Order(ent.Asc(prompttemplate.FieldPriority), ent.Asc(prompttemplate.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询提示词模板列表失败: %w", err)
	}
	return rows, nil
}

// CreateTemplate 新建自定义提示词模板并热更新引擎。
func (s *PromptService) CreateTemplate(ctx context.Context, in CreatePromptTemplateInput) (*ent.PromptTemplate, error) {
	name := strings.TrimSpace(in.Name)
	content := strings.TrimSpace(in.Content)
	if name == "" || content == "" {
		return nil, ErrInvalidPromptTemplate
	}
	category := strings.TrimSpace(in.Category)
	if category == "" {
		category = "general"
	}
	priority := 100
	if in.Priority != nil && *in.Priority > 0 {
		priority = *in.Priority
	}

	row, err := s.db.PromptTemplate.Create().
		SetName(name).
		SetCategory(category).
		SetContent(content).
		SetDescription(strings.TrimSpace(in.Description)).
		SetIsBuiltin(false).
		SetPriority(priority).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建提示词模板失败: %w", err)
	}
	if err := s.ReloadEngine(ctx); err != nil {
		return nil, err
	}
	return row, nil
}

// UpdateTemplate 更新指定提示词模板并热更新引擎。
func (s *PromptService) UpdateTemplate(ctx context.Context, id int64, in UpdatePromptTemplateInput) (*ent.PromptTemplate, error) {
	existing, err := s.db.PromptTemplate.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrPromptTemplateNotFound
		}
		return nil, fmt.Errorf("读取提示词模板失败: %w", err)
	}

	name := existing.Name
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	content := existing.Content
	if in.Content != nil {
		content = strings.TrimSpace(*in.Content)
	}
	if name == "" || content == "" {
		return nil, ErrInvalidPromptTemplate
	}

	category := existing.Category
	if in.Category != nil {
		if c := strings.TrimSpace(*in.Category); c != "" {
			category = c
		}
	}
	description := existing.Description
	if in.Description != nil {
		description = strings.TrimSpace(*in.Description)
	}
	priority := existing.Priority
	if in.Priority != nil && *in.Priority > 0 {
		priority = *in.Priority
	}

	updated, err := existing.Update().
		SetName(name).
		SetCategory(category).
		SetContent(content).
		SetDescription(description).
		SetPriority(priority).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("更新提示词模板失败: %w", err)
	}
	if err := s.ReloadEngine(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteTemplate 删除提示词模板，自动解绑引用它的分组并热更新引擎。
func (s *PromptService) DeleteTemplate(ctx context.Context, id int64) error {
	if _, err := s.db.Group.Update().
		Where(group.PromptTemplateIDEQ(id)).
		ClearPromptTemplateID().
		Save(ctx); err != nil {
		return fmt.Errorf("解绑分组模板引用失败: %w", err)
	}
	if err := s.db.PromptTemplate.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrPromptTemplateNotFound
		}
		return fmt.Errorf("删除提示词模板失败: %w", err)
	}
	return s.ReloadEngine(ctx)
}

// ResetTemplates 重置全部内置提示词模板到出厂默认（移除非内置模板并清理失效引用），随后热更新引擎。
func (s *PromptService) ResetTemplates(ctx context.Context) ([]*ent.PromptTemplate, error) {
	customs, err := s.db.PromptTemplate.Query().
		Where(prompttemplate.IsBuiltinEQ(false)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询自定义模板失败: %w", err)
	}
	if len(customs) > 0 {
		ids := make([]int64, 0, len(customs))
		for _, c := range customs {
			ids = append(ids, c.ID)
		}
		if _, err := s.db.Group.Update().
			Where(group.PromptTemplateIDIn(ids...)).
			ClearPromptTemplateID().
			Save(ctx); err != nil {
			return nil, fmt.Errorf("清理自定义模板分组引用失败: %w", err)
		}
		if _, err := s.db.PromptTemplate.Delete().
			Where(prompttemplate.IDIn(ids...)).
			Exec(ctx); err != nil {
			return nil, fmt.Errorf("清理自定义模板失败: %w", err)
		}
	}

	// 强制将全部内置模板恢复为 DefaultTemplates 内容。
	defaults := prompt.DefaultTemplates()
	existingBuiltins, err := s.db.PromptTemplate.Query().
		Where(prompttemplate.IsBuiltinEQ(true)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*ent.PromptTemplate, len(existingBuiltins))
	for _, b := range existingBuiltins {
		byName[b.Name] = b
	}
	for _, spec := range defaults {
		if row, ok := byName[spec.Name]; ok {
			if _, err := row.Update().
				SetCategory(spec.Category).
				SetContent(spec.Content).
				SetDescription(spec.Description).
				SetPriority(spec.Priority).
				Save(ctx); err != nil {
				return nil, err
			}
		} else {
			if _, err := s.db.PromptTemplate.Create().
				SetName(spec.Name).
				SetCategory(spec.Category).
				SetContent(spec.Content).
				SetDescription(spec.Description).
				SetIsBuiltin(true).
				SetPriority(spec.Priority).
				Save(ctx); err != nil {
				return nil, err
			}
		}
	}

	if err := s.ReloadEngine(ctx); err != nil {
		return nil, err
	}
	return s.ListTemplates(ctx)
}

// UpdateGroupPrompt 更新分组的提示词注入模式、分组正文与关联模板 ID，并立即热更新引擎。
func (s *PromptService) UpdateGroupPrompt(
	ctx context.Context,
	groupID int64,
	mode string,
	systemPrompt string,
	templateID *int64,
) (*ent.Group, error) {
	normMode := domain.NormalizePromptInjectMode(mode)
	if !domain.IsValidPromptInjectMode(normMode) {
		return nil, ErrInvalidPromptInjectMode
	}

	grp, err := s.db.Group.Query().
		Where(group.IDEQ(groupID), group.DeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrGroupNotFound
		}
		return nil, fmt.Errorf("查询分组失败: %w", err)
	}

	upd := grp.Update().
		SetPromptInjectMode(normMode).
		SetSystemPrompt(systemPrompt)

	if templateID != nil && *templateID > 0 {
		exists, tErr := s.db.PromptTemplate.Query().
			Where(prompttemplate.IDEQ(*templateID)).
			Exist(ctx)
		if tErr != nil {
			return nil, fmt.Errorf("校验提示词模板失败: %w", tErr)
		}
		if !exists {
			return nil, ErrPromptTemplateNotFound
		}
		upd = upd.SetPromptTemplateID(*templateID)
	} else {
		upd = upd.ClearPromptTemplateID()
	}

	updated, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("保存分组提示词策略失败: %w", err)
	}
	if err := s.ReloadEngine(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

// Preview 先通过内容风控引擎清洗客户端 system 提示词，再通过提示词引擎合成最终注入结果。
func (s *PromptService) Preview(
	ctx context.Context,
	groupID int64,
	clientSystem, model, protocol string,
) (*PromptPreviewOutput, error) {
	_ = ctx
	_ = protocol

	var sanitized string
	var hits []filter.RuleHit
	if s.filterEngine != nil {
		sanitized, hits = s.filterEngine.SanitizeText(filter.ScopeSystem, clientSystem)
	} else {
		sanitized = clientSystem
	}
	if hits == nil {
		hits = []filter.RuleHit{}
	}

	finalSys, replaceClient, layers := s.promptEngine.ResolveSystemPrompt(groupID, sanitized, prompt.Vars{
		Date:  time.Now().UTC().Format("2006-01-02"),
		Model: strings.TrimSpace(model),
	})
	if layers == nil {
		layers = []string{}
	}

	injectMode := domain.PromptModeAppendGlobal
	if grpCfg, ok := s.promptEngine.Group(groupID); ok {
		injectMode = grpCfg.Mode
	}

	return &PromptPreviewOutput{
		FinalSystem:           finalSys,
		SanitizedClientSystem: sanitized,
		LayersApplied:         layers,
		ReplaceClient:         replaceClient,
		InjectMode:            injectMode,
		FilterHits:            hits,
	}, nil
}
