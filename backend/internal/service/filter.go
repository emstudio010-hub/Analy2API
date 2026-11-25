package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"cmd2api/ent"
	"cmd2api/ent/filterrule"
	"cmd2api/internal/filter"
)

var (
	// ErrInvalidFilterRule 表示过滤规则参数不合法（包括空字段、非法正则或试图注入零宽字符）。
	ErrInvalidFilterRule = errors.New("invalid filter rule")
	// ErrFilterRuleNotFound 表示指定的过滤规则不存在。
	ErrFilterRuleNotFound = errors.New("filter rule not found")
)

// FilterRuleStats 统计启用的三段式规则数量。
type FilterRuleStats struct {
	Enabled int `json:"enabled"`
	Prune   int `json:"prune"`
	Rewrite int `json:"rewrite"`
	Replace int `json:"replace"`
}

// CreateFilterRuleInput 是新增过滤规则的入参。
type CreateFilterRuleInput struct {
	Name          string
	Kind          string
	Pattern       string
	Replacement   string
	IsRegex       bool
	CaseSensitive bool
	WordBoundary  *bool
	Scope         string
	Priority      *int
	Enabled       *bool
	Description   string
}

// UpdateFilterRuleInput 使用指针字段，允许局部更新（例如仅切换 enabled 开关）而不覆盖其余字段。
type UpdateFilterRuleInput struct {
	Name          *string
	Kind          *string
	Pattern       *string
	Replacement   *string
	IsRegex       *bool
	CaseSensitive *bool
	WordBoundary  *bool
	Scope         *string
	Priority      *int
	Enabled       *bool
	Description   *string
}

// FilterPreviewOutput 对齐前端 FilterRulePreviewResult 结构。
type FilterPreviewOutput struct {
	Input   string           `json:"input"`
	Output  string           `json:"output"`
	Mode    string           `json:"mode"`
	Changed bool             `json:"changed"`
	Hits    []filter.RuleHit `json:"hits"`
	Stats   filter.Stats     `json:"stats"`
}

// FilterService 管理数据库 `filter_rules` 表与内存 `*filter.Engine` 的热同步。
type FilterService struct {
	db     *ent.Client
	engine *filter.Engine
	logger *slog.Logger
}

// NewFilterService 构造 FilterService。若传入 engine 为 nil，则自动创建默认引擎。
func NewFilterService(db *ent.Client, engine *filter.Engine, logger *slog.Logger) *FilterService {
	if engine == nil {
		engine = filter.NewDefaultEngine()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &FilterService{
		db:     db,
		engine: engine,
		logger: logger,
	}
}

// Engine 返回共享的内容风控引擎指针，供 relay 层热读取。
func (s *FilterService) Engine() *filter.Engine {
	return s.engine
}

// EnsureSeeded 在 `filter_rules` 表为空时写入默认内置规则（标为 is_builtin=true）；
// 若表非空，则按规则名自动补齐新增的内置规则（保留已有规则与用户自定义配置），并热加载到共享引擎。
func (s *FilterService) EnsureSeeded(ctx context.Context) error {
	count, err := s.db.FilterRule.Query().Count(ctx)
	if err != nil {
		return fmt.Errorf("count filter_rules failed: %w", err)
	}
	if count == 0 {
		if err := s.seedDefaultsLocked(ctx); err != nil {
			return err
		}
		s.logger.Info("seeded default content filter rules", "count", len(filter.DefaultRules()))
	} else {
		added, err := s.syncMissingDefaultsLocked(ctx)
		if err != nil {
			return err
		}
		if added > 0 {
			s.logger.Info("synced missing built-in content filter rules", "added", added)
		}
	}
	return s.ReloadEngine(ctx)
}

func (s *FilterService) seedDefaultsLocked(ctx context.Context) error {
	return s.insertBuiltinRules(ctx, filter.DefaultRules())
}

func (s *FilterService) syncMissingDefaultsLocked(ctx context.Context) (int, error) {
	existingRows, err := s.db.FilterRule.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query existing filter_rules failed: %w", err)
	}
	byName := make(map[string]*ent.FilterRule, len(existingRows))
	for _, row := range existingRows {
		byName[row.Name] = row
	}

	defaults := filter.DefaultRules()
	missing := make([]filter.Rule, 0)
	updated := 0
	for _, r := range defaults {
		existing, ok := byName[r.Name]
		if !ok {
			missing = append(missing, r)
			continue
		}
		if !existing.IsBuiltin {
			continue
		}
		normScope := normalizeFilterScope(r.Scope)
		if existing.Kind != r.Kind ||
			existing.Pattern != r.Pattern ||
			existing.Replacement != r.Replacement ||
			existing.IsRegex != r.IsRegex ||
			existing.CaseSensitive != r.CaseSensitive ||
			existing.WordBoundary != r.WordBoundary ||
			existing.Scope != normScope ||
			existing.Priority != r.Priority ||
			existing.Description != r.Description {
			if _, err := existing.Update().
				SetKind(r.Kind).
				SetPattern(r.Pattern).
				SetReplacement(r.Replacement).
				SetIsRegex(r.IsRegex).
				SetCaseSensitive(r.CaseSensitive).
				SetWordBoundary(r.WordBoundary).
				SetScope(normScope).
				SetPriority(r.Priority).
				SetDescription(r.Description).
				Save(ctx); err != nil {
				return 0, fmt.Errorf("sync built-in filter rule %q failed: %w", r.Name, err)
			}
			updated++
		}
	}
	if len(missing) > 0 {
		if err := s.insertBuiltinRules(ctx, missing); err != nil {
			return 0, err
		}
	}
	return len(missing) + updated, nil
}

func (s *FilterService) insertBuiltinRules(ctx context.Context, rules []filter.Rule) error {
	builders := make([]*ent.FilterRuleCreate, 0, len(rules))
	for _, r := range rules {
		b := s.db.FilterRule.Create().
			SetName(r.Name).
			SetKind(r.Kind).
			SetPattern(r.Pattern).
			SetReplacement(r.Replacement).
			SetIsRegex(r.IsRegex).
			SetCaseSensitive(r.CaseSensitive).
			SetWordBoundary(r.WordBoundary).
			SetScope(normalizeFilterScope(r.Scope)).
			SetPriority(r.Priority).
			SetEnabled(r.Enabled).
			SetIsBuiltin(true).
			SetDescription(r.Description)
		builders = append(builders, b)
	}
	if len(builders) == 0 {
		return nil
	}
	if err := s.db.FilterRule.CreateBulk(builders...).Exec(ctx); err != nil {
		return fmt.Errorf("bulk seed default filter rules failed: %w", err)
	}
	return nil
}

// ReloadEngine 从数据库读取全部规则并原子热更新内存 `*filter.Engine`。
func (s *FilterService) ReloadEngine(ctx context.Context) error {
	rows, err := s.db.FilterRule.Query().
		Order(ent.Asc(filterrule.FieldPriority), ent.Asc(filterrule.FieldID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query filter_rules for reload failed: %w", err)
	}
	rules := make([]filter.Rule, 0, len(rows))
	for _, row := range rows {
		rules = append(rules, entRuleToEngineRule(row))
	}
	s.engine.Reload(rules)
	return nil
}

// List 返回有序过滤规则列表与启用态统计。
func (s *FilterService) List(ctx context.Context) ([]*ent.FilterRule, FilterRuleStats, error) {
	rows, err := s.db.FilterRule.Query().
		Order(ent.Asc(filterrule.FieldPriority), ent.Asc(filterrule.FieldID)).
		All(ctx)
	if err != nil {
		return nil, FilterRuleStats{}, fmt.Errorf("list filter_rules failed: %w", err)
	}
	stats := computeFilterRuleStats(rows)
	return rows, stats, nil
}

// Summary 返回总规则数与启用规则数，供系统设置页展示。
func (s *FilterService) Summary(ctx context.Context) (total int, enabled int, err error) {
	rows, err := s.db.FilterRule.Query().All(ctx)
	if err != nil {
		return 0, 0, err
	}
	total = len(rows)
	for _, r := range rows {
		if r.Enabled {
			enabled++
		}
	}
	return total, enabled, nil
}

// Create 校验并新增一条过滤规则，然后立即热更新引擎。
func (s *FilterService) Create(ctx context.Context, input CreateFilterRuleInput) (*ent.FilterRule, error) {
	name := strings.TrimSpace(input.Name)
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	pattern := input.Pattern
	replacement := input.Replacement
	scope := normalizeFilterScope(input.Scope)
	wordBoundary := true
	if input.WordBoundary != nil {
		wordBoundary = *input.WordBoundary
	} else if input.IsRegex {
		wordBoundary = false
	}
	priority := 100
	if input.Priority != nil && *input.Priority > 0 {
		priority = *input.Priority
	} else if input.Priority != nil && *input.Priority == 0 {
		priority = 100
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	if err := validateFilterRuleSpec(name, kind, pattern, replacement, scope, input.IsRegex, input.CaseSensitive); err != nil {
		return nil, err
	}

	row, err := s.db.FilterRule.Create().
		SetName(name).
		SetKind(kind).
		SetPattern(pattern).
		SetReplacement(replacement).
		SetIsRegex(input.IsRegex).
		SetCaseSensitive(input.CaseSensitive).
		SetWordBoundary(wordBoundary).
		SetScope(scope).
		SetPriority(priority).
		SetEnabled(enabled).
		SetIsBuiltin(false).
		SetDescription(strings.TrimSpace(input.Description)).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create filter rule failed: %w", err)
	}
	if err := s.ReloadEngine(ctx); err != nil {
		return nil, err
	}
	return row, nil
}

// Update 局部或全量更新指定过滤规则并热更新引擎。
func (s *FilterService) Update(ctx context.Context, id int64, input UpdateFilterRuleInput) (*ent.FilterRule, error) {
	existing, err := s.db.FilterRule.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrFilterRuleNotFound
		}
		return nil, fmt.Errorf("get filter rule failed: %w", err)
	}

	name := existing.Name
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	kind := existing.Kind
	if input.Kind != nil {
		kind = strings.ToLower(strings.TrimSpace(*input.Kind))
	}
	pattern := existing.Pattern
	if input.Pattern != nil {
		pattern = *input.Pattern
	}
	replacement := existing.Replacement
	if input.Replacement != nil {
		replacement = *input.Replacement
	}
	isRegex := existing.IsRegex
	if input.IsRegex != nil {
		isRegex = *input.IsRegex
	}
	caseSensitive := existing.CaseSensitive
	if input.CaseSensitive != nil {
		caseSensitive = *input.CaseSensitive
	}
	wordBoundary := existing.WordBoundary
	if input.WordBoundary != nil {
		wordBoundary = *input.WordBoundary
	}
	scope := normalizeFilterScope(existing.Scope)
	if input.Scope != nil {
		scope = normalizeFilterScope(*input.Scope)
	}
	priority := existing.Priority
	if input.Priority != nil && *input.Priority > 0 {
		priority = *input.Priority
	}
	enabled := existing.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	description := existing.Description
	if input.Description != nil {
		description = strings.TrimSpace(*input.Description)
	}

	if err := validateFilterRuleSpec(name, kind, pattern, replacement, scope, isRegex, caseSensitive); err != nil {
		return nil, err
	}

	updated, err := existing.Update().
		SetName(name).
		SetKind(kind).
		SetPattern(pattern).
		SetReplacement(replacement).
		SetIsRegex(isRegex).
		SetCaseSensitive(caseSensitive).
		SetWordBoundary(wordBoundary).
		SetScope(scope).
		SetPriority(priority).
		SetEnabled(enabled).
		SetDescription(description).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update filter rule failed: %w", err)
	}
	if err := s.ReloadEngine(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

// Delete 删除指定过滤规则并立即热更新引擎。
func (s *FilterService) Delete(ctx context.Context, id int64) error {
	if err := s.db.FilterRule.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrFilterRuleNotFound
		}
		return fmt.Errorf("delete filter rule failed: %w", err)
	}
	return s.ReloadEngine(ctx)
}

// ResetDefaults 清空现有过滤规则、重新写入默认内置规则并热更新引擎。
func (s *FilterService) ResetDefaults(ctx context.Context) ([]*ent.FilterRule, error) {
	if _, err := s.db.FilterRule.Delete().Exec(ctx); err != nil {
		return nil, fmt.Errorf("clear filter rules failed: %w", err)
	}
	if err := s.seedDefaultsLocked(ctx); err != nil {
		return nil, err
	}
	if err := s.ReloadEngine(ctx); err != nil {
		return nil, err
	}
	rows, _, err := s.List(ctx)
	return rows, err
}

// Preview 使用当前实时规则引擎在内存中演练脱敏效果，返回前后端一致的命中明细结构。
func (s *FilterService) Preview(input, mode, protocol string) (*FilterPreviewOutput, error) {
	res, err := s.engine.Preview(input, mode, protocol)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFilterRule, err)
	}
	hits := res.Stats.Hits
	if hits == nil {
		hits = []filter.RuleHit{}
	}
	stats := res.Stats
	stats.Hits = hits
	return &FilterPreviewOutput{
		Input:   res.Input,
		Output:  res.Output,
		Mode:    res.Mode,
		Changed: res.Changed,
		Hits:    hits,
		Stats:   stats,
	}, nil
}

func validateFilterRuleSpec(name, kind, pattern, replacement, scope string, isRegex, caseSensitive bool) error {
	if name == "" {
		return fmt.Errorf("%w: rule name is required", ErrInvalidFilterRule)
	}
	switch kind {
	case filter.KindPrune, filter.KindRewrite, filter.KindReplace:
	default:
		return fmt.Errorf("%w: invalid rule kind %q (must be prune/rewrite/replace)", ErrInvalidFilterRule, kind)
	}
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("%w: pattern is required", ErrInvalidFilterRule)
	}
	if strings.ContainsRune(pattern, '\u200b') || strings.ContainsRune(replacement, '\u200b') {
		return fmt.Errorf("%w: zero-width character (\\u200b) is forbidden", ErrInvalidFilterRule)
	}
	if err := validateFilterScope(scope); err != nil {
		return err
	}
	if isRegex {
		expr := pattern
		if !caseSensitive && !strings.HasPrefix(expr, "(?i)") && !strings.HasPrefix(expr, "(?-i)") {
			expr = "(?i)" + expr
		}
		if _, err := regexp.Compile(expr); err != nil {
			return fmt.Errorf("%w: invalid regex pattern: %v", ErrInvalidFilterRule, err)
		}
	}
	return nil
}

func normalizeFilterScope(scope string) string {
	trimmed := strings.TrimSpace(scope)
	if trimmed == "" {
		return filter.ScopeAllInjected
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, raw := range parts {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		switch p {
		case "all", filter.ScopeAllInjected:
			out = append(out, filter.ScopeAllInjected)
		case "user_harness", filter.ScopeUser:
			out = append(out, filter.ScopeUser)
		default:
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return filter.ScopeAllInjected
	}
	return strings.Join(out, ",")
}

func validateFilterScope(scope string) error {
	parts := strings.Split(scope, ",")
	for _, raw := range parts {
		p := strings.ToLower(strings.TrimSpace(raw))
		switch p {
		case filter.ScopeAllInjected, filter.ScopeSystem, filter.ScopeTool, filter.ScopeUser:
		default:
			return fmt.Errorf("%w: unsupported scope %q", ErrInvalidFilterRule, p)
		}
	}
	return nil
}

func entRuleToEngineRule(row *ent.FilterRule) filter.Rule {
	return filter.Rule{
		ID:            row.ID,
		Name:          row.Name,
		Kind:          row.Kind,
		Pattern:       row.Pattern,
		Replacement:   row.Replacement,
		Scope:         row.Scope,
		Description:   row.Description,
		IsRegex:       row.IsRegex,
		CaseSensitive: row.CaseSensitive,
		WordBoundary:  row.WordBoundary,
		Enabled:       row.Enabled,
		IsBuiltin:     row.IsBuiltin,
		Priority:      row.Priority,
	}
}

func computeFilterRuleStats(rows []*ent.FilterRule) FilterRuleStats {
	var stats FilterRuleStats
	for _, r := range rows {
		if r.Enabled {
			stats.Enabled++
		}
		switch r.Kind {
		case filter.KindPrune:
			stats.Prune++
		case filter.KindRewrite:
			stats.Rewrite++
		case filter.KindReplace:
			stats.Replace++
		}
	}
	return stats
}
