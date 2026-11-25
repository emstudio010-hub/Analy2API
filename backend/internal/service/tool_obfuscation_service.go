package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"cmd2api/ent"
	"cmd2api/ent/group"
	"cmd2api/ent/systemsetting"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/toolobf"
)

const (
	// SettingKeyToolObfuscationConfig 存储 toolobf.Config 的 JSON 序列化配置。
	SettingKeyToolObfuscationConfig = "tool_obfuscation_config"
)

// GroupToolObfuscationSummary 在工具混淆视图中附带各分组的生效策略快照，方便矩阵视图直接渲染。
type GroupToolObfuscationSummary struct {
	GroupID       int64                 `json:"group_id"`
	GroupName     string                `json:"group_name"`
	Platform      string                `json:"platform"`
	Override      toolobf.GroupOverride `json:"override"`
	EffectiveMode string                `json:"effective_mode"`
	EffectiveOn   bool                  `json:"effective_enabled"`
}

// ToolObfuscationStats 汇总当前配置规模指标，供控制台卡片与 /api/settings 使用。
type ToolObfuscationStats struct {
	PlatformsCount      int `json:"platforms_count"`
	CustomMapCount      int `json:"custom_map_count"`
	ExcludeCount        int `json:"exclude_count"`
	BuiltinAliasesCount int `json:"builtin_aliases_count"`
	SensitiveToolsCount int `json:"sensitive_tools_count"`
	GroupOverridesCount int `json:"group_overrides_count"`
}

// ToolObfuscationView 是控制台读写接口返回的标准视图。
type ToolObfuscationView struct {
	Config         toolobf.Config                `json:"config"`
	BuiltinAliases map[string]string             `json:"builtin_aliases"`
	SensitiveTools []string                      `json:"sensitive_tools"`
	Groups         []GroupToolObfuscationSummary `json:"groups"`
	Stats          ToolObfuscationStats          `json:"stats"`
}

// PreviewToolObfuscationInput 是工具名混淆预览演练入参。
type PreviewToolObfuscationInput struct {
	GroupID   int64           `json:"group_id"`
	ToolNames []string        `json:"tool_names"`
	Payload   string          `json:"payload"`
	Protocol  string          `json:"protocol"`
	Platform  string          `json:"platform"`
	Config    *toolobf.Config `json:"config,omitempty"`
}

// PreviewMappingEntry 描述单条工具名的“原名 -> 混淆名 -> 还原名”双向演练校验结果。
type PreviewMappingEntry struct {
	Original   string `json:"original"`
	Obfuscated string `json:"obfuscated"`
	Restored   string `json:"restored"`
	Changed    bool   `json:"changed"`
}

// PreviewToolObfuscationOutput 是双向预览结果。
type PreviewToolObfuscationOutput struct {
	Mappings          []PreviewMappingEntry `json:"mappings"`
	ObfuscatedPayload string                `json:"obfuscated_payload,omitempty"`
	RestoredVerified  bool                  `json:"restored_verified"`
	ChangedCount      int                   `json:"changed_count"`
	TotalCount        int                   `json:"total_count"`
	Mode              string                `json:"mode"`
	Scope             string                `json:"scope"`
}

var defaultPreviewTools = []string{
	"Bash",
	"Read",
	"Edit",
	"Grep",
	"Glob",
	"Write",
	"mcp__github__create_issue",
	"apply_patch",
	"weather_lookup",
}

// ToolObfuscationService 管理工具名混淆配置的持久化、热同步与双向演练预览。
type ToolObfuscationService struct {
	mu     sync.Mutex
	db     *ent.Client
	engine *toolobf.Engine
	logger *slog.Logger
}

// NewToolObfuscationService 构造 ToolObfuscationService。
func NewToolObfuscationService(db *ent.Client, engine *toolobf.Engine, logger *slog.Logger) *ToolObfuscationService {
	if engine == nil {
		engine = toolobf.NewDefaultEngine()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ToolObfuscationService{
		db:     db,
		engine: engine,
		logger: logger,
	}
}

// Engine 返回绑定的内存混淆引擎实例。
func (s *ToolObfuscationService) Engine() *toolobf.Engine {
	if s == nil {
		return nil
	}
	return s.engine
}

// EnsureSeeded 从数据库读取已持久化的配置；若缺失则生成随机盐值并写入默认配置，随后热同步引擎。
func (s *ToolObfuscationService) EnsureSeeded(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}

	row, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(SettingKeyToolObfuscationConfig)).
		Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("查询工具名混淆配置失败: %w", err)
		}
		def := toolobf.DefaultConfig()
		def.Salt = toolobf.GenerateSalt()
		if err := s.persistLocked(ctx, def); err != nil {
			return fmt.Errorf("初始化默认工具名混淆配置失败: %w", err)
		}
		s.engine.Reload(def)
		if s.logger != nil {
			s.logger.Info("已初始化默认工具名混淆策略", "mode", def.Mode, "prefix", def.Prefix, "scope", def.Scope)
		}
		return nil
	}

	var loaded toolobf.Config
	needPersist := false
	if uErr := json.Unmarshal([]byte(row.Value), &loaded); uErr != nil {
		loaded = toolobf.DefaultConfig()
		loaded.Salt = toolobf.GenerateSalt()
		needPersist = true
	} else {
		norm, nErr := toolobf.NormalizeConfig(loaded)
		if nErr != nil {
			loaded = toolobf.DefaultConfig()
			loaded.Salt = toolobf.GenerateSalt()
			needPersist = true
		} else {
			loaded = norm
			if loaded.Salt == "" {
				loaded.Salt = toolobf.GenerateSalt()
				needPersist = true
			}
		}
	}

	if needPersist {
		if pErr := s.persistLocked(ctx, loaded); pErr != nil {
			return fmt.Errorf("修复持久化工具名混淆配置失败: %w", pErr)
		}
	}
	s.engine.Reload(loaded)
	return nil
}

// GetView 返回当前生效配置、内置词表及统计指标。
func (s *ToolObfuscationService) GetView(ctx context.Context) (ToolObfuscationView, error) {
	if s == nil {
		return ToolObfuscationView{}, errors.New("工具名混淆服务未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.engine.Config()
	if s.db != nil {
		row, err := s.db.SystemSetting.Query().
			Where(systemsetting.KeyEQ(SettingKeyToolObfuscationConfig)).
			Only(ctx)
		if err == nil {
			var parsed toolobf.Config
			if json.Unmarshal([]byte(row.Value), &parsed) == nil {
				if norm, nErr := toolobf.NormalizeConfig(parsed); nErr == nil {
					if norm.Salt == "" && cfg.Salt != "" {
						norm.Salt = cfg.Salt
					}
					cfg = norm
					s.engine.Reload(norm)
				}
			}
		}
	}
	return s.buildViewLocked(ctx, cfg), nil
}

// UpdateConfig 校验、持久化并立即热更新内存引擎配置（保留现有 salt，且当入参 group_overrides 为 nil 时保留各分组独立配置）。
func (s *ToolObfuscationService) UpdateConfig(ctx context.Context, cfg toolobf.Config) (ToolObfuscationView, error) {
	if s == nil {
		return ToolObfuscationView{}, errors.New("工具名混淆服务未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	curCfg := s.engine.Config()
	if strings.TrimSpace(cfg.Salt) == "" {
		cfg.Salt = curCfg.Salt
	}
	if strings.TrimSpace(cfg.Salt) == "" {
		cfg.Salt = toolobf.GenerateSalt()
	}
	if cfg.GroupOverrides == nil && len(curCfg.GroupOverrides) > 0 {
		cfg.GroupOverrides = curCfg.GroupOverrides
	}

	norm, err := toolobf.NormalizeConfig(cfg)
	if err != nil {
		return ToolObfuscationView{}, err
	}

	if s.db != nil {
		if err := s.persistLocked(ctx, norm); err != nil {
			return ToolObfuscationView{}, fmt.Errorf("保存工具名混淆配置失败: %w", err)
		}
	}
	if err := s.engine.ReloadStrict(norm); err != nil {
		return ToolObfuscationView{}, err
	}
	if s.logger != nil {
		s.logger.Info("已更新工具名混淆配置", "enabled", norm.Enabled, "mode", norm.Mode, "scope", norm.Scope, "platforms", norm.Platforms)
	}
	return s.buildViewLocked(ctx, norm), nil
}

// UpdateGroupOverride 更新单个分组的工具名混淆覆盖策略，立即持久化并热生效。
func (s *ToolObfuscationService) UpdateGroupOverride(ctx context.Context, groupID int64, override toolobf.GroupOverride) (ToolObfuscationView, error) {
	if s == nil {
		return ToolObfuscationView{}, errors.New("工具名混淆服务未初始化")
	}
	if groupID <= 0 {
		return ToolObfuscationView{}, errors.New("分组 ID 无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.engine.Config()
	if cfg.GroupOverrides == nil {
		cfg.GroupOverrides = make(map[int64]toolobf.GroupOverride)
	}
	ovNorm, err := toolobf.NormalizeGroupOverride(override)
	if err != nil {
		return ToolObfuscationView{}, err
	}
	if ovNorm.IsDefaultInherit() {
		delete(cfg.GroupOverrides, groupID)
	} else {
		cfg.GroupOverrides[groupID] = ovNorm
	}

	norm, err := toolobf.NormalizeConfig(cfg)
	if err != nil {
		return ToolObfuscationView{}, err
	}
	if s.db != nil {
		if err := s.persistLocked(ctx, norm); err != nil {
			return ToolObfuscationView{}, fmt.Errorf("保存分组工具名混淆策略失败: %w", err)
		}
	}
	if err := s.engine.ReloadStrict(norm); err != nil {
		return ToolObfuscationView{}, err
	}
	if s.logger != nil {
		s.logger.Info("已更新分组工具名混淆覆盖策略", "group_id", groupID, "mode", ovNorm.Mode)
	}
	return s.buildViewLocked(ctx, norm), nil
}

// GetGroupOverride 返回指定分组当前的覆盖配置与是否生效状态。
func (s *ToolObfuscationService) GetGroupOverride(groupID int64, platform string) (toolobf.GroupOverride, string, bool) {
	if s == nil || s.engine == nil {
		return toolobf.GroupOverride{Mode: toolobf.GroupModeInherit}, toolobf.ModeHash, false
	}
	ov, _ := s.engine.GroupOverrideFor(groupID)
	effCfg, _ := s.engine.ResolveGroupConfig(groupID)
	return ov, effCfg.Mode, s.engine.EnabledForGroupPlatform(groupID, platform)
}

// RegenerateSalt 轮换随机盐值并立即持久化热生效（变更后 hash/超长截断指纹随之轮换）。
func (s *ToolObfuscationService) RegenerateSalt(ctx context.Context) (ToolObfuscationView, error) {
	if s == nil {
		return ToolObfuscationView{}, errors.New("工具名混淆服务未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.engine.Config()
	cfg.Salt = toolobf.GenerateSalt()
	norm, err := toolobf.NormalizeConfig(cfg)
	if err != nil {
		return ToolObfuscationView{}, err
	}
	if s.db != nil {
		if err := s.persistLocked(ctx, norm); err != nil {
			return ToolObfuscationView{}, fmt.Errorf("保存轮换盐值失败: %w", err)
		}
	}
	s.engine.Reload(norm)
	if s.logger != nil {
		s.logger.Info("已轮换工具名混淆盐值")
	}
	return s.buildViewLocked(ctx, norm), nil
}

// Preview 使用给定草稿配置（或当前生效配置及分组覆盖）演练双向“混淆 -> 无损还原”全流程。
func (s *ToolObfuscationService) Preview(_ context.Context, in PreviewToolObfuscationInput) (PreviewToolObfuscationOutput, error) {
	if s == nil {
		return PreviewToolObfuscationOutput{}, errors.New("工具名混淆服务未初始化")
	}

	evalCfg := s.engine.Config()
	if in.Config != nil {
		draft := *in.Config
		if strings.TrimSpace(draft.Salt) == "" {
			draft.Salt = evalCfg.Salt
		}
		if strings.TrimSpace(draft.Salt) == "" {
			draft.Salt = toolobf.GenerateSalt()
		}
		if draft.GroupOverrides == nil && len(evalCfg.GroupOverrides) > 0 {
			draft.GroupOverrides = evalCfg.GroupOverrides
		}
		norm, err := toolobf.NormalizeConfig(draft)
		if err != nil {
			return PreviewToolObfuscationOutput{}, err
		}
		evalCfg = norm
	}

	evalEngine := toolobf.NewEngine(evalCfg)
	effGroupCfg, _ := evalEngine.ResolveGroupConfig(in.GroupID)
	rawPayload := strings.TrimSpace(in.Payload)

	if rawPayload != "" {
		obfBytes, entries, err := relay.ObfuscatePayloadForPreview(evalEngine, in.GroupID, in.Protocol, []byte(rawPayload))
		if err != nil {
			return PreviewToolObfuscationOutput{}, err
		}
		return buildPreviewOutput(effGroupCfg, evalEngine, in.GroupID, in.Platform, entries, string(obfBytes)), nil
	}

	names := in.ToolNames
	if len(names) == 0 {
		names = defaultPreviewTools
	}

	sess := evalEngine.NewSessionForGroup(in.GroupID, in.Platform)
	cleaned := make([]string, 0, len(names))
	seenName := make(map[string]struct{}, len(names))
	for _, raw := range names {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if _, dup := seenName[t]; dup {
			continue
		}
		seenName[t] = struct{}{}
		cleaned = append(cleaned, t)
	}

	var entries []toolobf.MappingEntry
	if sess == nil {
		entries = make([]toolobf.MappingEntry, 0, len(cleaned))
		for _, n := range cleaned {
			entries = append(entries, toolobf.MappingEntry{
				Original:   n,
				Obfuscated: n,
				Changed:    false,
			})
		}
	} else {
		sessCfg := sess.Config()
		exclLower := make(map[string]struct{}, len(sessCfg.ExcludeTools))
		for _, t := range sessCfg.ExcludeTools {
			exclLower[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
		}
		for _, n := range cleaned {
			if _, ok := exclLower[strings.ToLower(n)]; ok {
				sess.RegisterPassthrough(n)
			} else if sessCfg.Scope == toolobf.ScopeSensitiveOnly {
				if _, custom := sessCfg.CustomMap[n]; !custom && !toolobf.IsSensitiveAgentTool(n) {
					sess.RegisterPassthrough(n)
				}
			}
		}
		for _, n := range cleaned {
			sess.Obfuscate(n)
		}
		entries = sess.Entries()
	}

	return buildPreviewOutput(effGroupCfg, evalEngine, in.GroupID, in.Platform, entries, ""), nil
}

func buildPreviewOutput(
	cfg toolobf.Config,
	eng *toolobf.Engine,
	groupID int64,
	platform string,
	entries []toolobf.MappingEntry,
	obfPayload string,
) PreviewToolObfuscationOutput {
	verifySess := eng.NewSessionForGroup(groupID, platform)
	if verifySess != nil {
		for _, e := range entries {
			if !e.Changed {
				verifySess.RegisterPassthrough(e.Original)
			}
		}
		for _, e := range entries {
			if e.Changed {
				verifySess.Obfuscate(e.Original)
			}
		}
	}

	mappings := make([]PreviewMappingEntry, 0, len(entries))
	seenObf := make(map[string]string, len(entries))
	restoredVerified := true
	changedCount := 0

	for _, e := range entries {
		restored := e.Obfuscated
		if verifySess != nil {
			restored = verifySess.Restore(e.Obfuscated)
		} else {
			restored = e.Original
		}
		if e.Changed {
			changedCount++
		}
		if restored != e.Original {
			restoredVerified = false
		}
		if prevOrig, dup := seenObf[e.Obfuscated]; dup && prevOrig != e.Original {
			restoredVerified = false
		}
		seenObf[e.Obfuscated] = e.Original

		mappings = append(mappings, PreviewMappingEntry{
			Original:   e.Original,
			Obfuscated: e.Obfuscated,
			Restored:   restored,
			Changed:    e.Changed,
		})
	}

	return PreviewToolObfuscationOutput{
		Mappings:          mappings,
		ObfuscatedPayload: obfPayload,
		RestoredVerified:  restoredVerified,
		ChangedCount:      changedCount,
		TotalCount:        len(mappings),
		Mode:              cfg.Mode,
		Scope:             cfg.Scope,
	}
}

func (s *ToolObfuscationService) persistLocked(ctx context.Context, cfg toolobf.Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	existing, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(SettingKeyToolObfuscationConfig)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			_, cErr := s.db.SystemSetting.Create().
				SetKey(SettingKeyToolObfuscationConfig).
				SetValue(string(raw)).
				SetDescription("Tool name obfuscation configuration").
				Save(ctx)
			return cErr
		}
		return err
	}
	_, err = existing.Update().SetValue(string(raw)).Save(ctx)
	return err
}

func (s *ToolObfuscationService) buildViewLocked(ctx context.Context, cfg toolobf.Config) ToolObfuscationView {
	aliases := toolobf.BuiltinAliases()
	sensitive := toolobf.SensitiveTools()
	var groupsSummary []GroupToolObfuscationSummary
	if s.db != nil {
		rows, err := s.db.Group.Query().
			Where(group.DeletedAtIsNil()).
			Order(ent.Asc(group.FieldID)).
			All(ctx)
		if err == nil {
			tmpEng := toolobf.NewEngine(cfg)
			groupsSummary = make([]GroupToolObfuscationSummary, 0, len(rows))
			for _, g := range rows {
				plat := domain.NormalizePlatform(g.Platform)
				ov, _ := tmpEng.GroupOverrideFor(g.ID)
				effCfg, _ := tmpEng.ResolveGroupConfig(g.ID)
				groupsSummary = append(groupsSummary, GroupToolObfuscationSummary{
					GroupID:       g.ID,
					GroupName:     g.Name,
					Platform:      plat,
					Override:      ov,
					EffectiveMode: effCfg.Mode,
					EffectiveOn:   tmpEng.EnabledForGroupPlatform(g.ID, plat),
				})
			}
		}
	}
	if groupsSummary == nil {
		groupsSummary = []GroupToolObfuscationSummary{}
	}
	return ToolObfuscationView{
		Config:         cfg,
		BuiltinAliases: aliases,
		SensitiveTools: sensitive,
		Groups:         groupsSummary,
		Stats: ToolObfuscationStats{
			PlatformsCount:      len(cfg.Platforms),
			CustomMapCount:      len(cfg.CustomMap),
			ExcludeCount:        len(cfg.ExcludeTools),
			BuiltinAliasesCount: len(aliases),
			SensitiveToolsCount: len(sensitive),
			GroupOverridesCount: len(cfg.GroupOverrides),
		},
	}
}
