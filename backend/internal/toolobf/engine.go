// Package toolobf 提供确定性、可逆且 KV-Cache 友好的工具名混淆引擎。
//
// 核心约束：
//  1. 确定性（KV-Cache 友好）：相同的 (salt, mode, prefix, orig_tool_name) 在跨轮次、
//     跨账号重路由时始终生成完全一致的上游工具名，避免命中缓存失效；
//  2. 严格双射（无损还原）：每次请求通过独立 Session 建立 forward / reverse 双射表，
//     在工具碰撞超过 64 字符上限或自定义映射冲突时自动加后缀消歧；
//  3. 并发安全：Engine 内部配置通过 sync.RWMutex 保护支持热重载，请求期仅使用轻量只读快照。
package toolobf

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/domain"
)

const (
	// ModeHash 使用确定性摘要生成短标识符（默认模式：<prefix><sha256[:10]>）。
	ModeHash = "hash"
	// ModeAlias 对常见 Coding Agent 工具使用中性业务语义别名，未知工具自动回退哈希短名。
	ModeAlias = "alias"
	// ModePrefix 为原始工具名添加中性前缀并在超长时确定性截断。
	ModePrefix = "prefix"

	// GroupModeInherit 分组级模式：继承全局工具名混淆策略。
	GroupModeInherit = "inherit"
	// GroupModeOff 分组级模式：在本分组内关闭工具名混淆（覆盖全局开启）。
	GroupModeOff = "off"

	// ScopeAll 混淆请求中的全部工具定义与调用。
	ScopeAll = "all"
	// ScopeSensitiveOnly 仅混淆内置识别的高风险 Coding Agent / MCP 工具名。
	ScopeSensitiveOnly = "sensitive_only"

	// DefaultPrefix 是默认中性工具名前缀。
	DefaultPrefix = "ess_t_"
	// MaxToolNameLen 为 OpenAI / Anthropic / Gemini 上游通行的工具名最大长度限制。
	MaxToolNameLen = 64
)

var (
	validCustomTargetRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`)
	backtickSpanRe      = regexp.MustCompile("`([^`\r\n]{1,128})`")
)

// GroupOverride 定义单个分组对工具名混淆策略的独立覆盖配置。
type GroupOverride struct {
	Mode            string            `json:"mode"`
	Prefix          string            `json:"prefix,omitempty"`
	Scope           string            `json:"scope,omitempty"`
	CustomMap       map[string]string `json:"custom_map,omitempty"`
	ExcludeTools    []string          `json:"exclude_tools,omitempty"`
	RewriteToolRefs *bool             `json:"rewrite_tool_refs,omitempty"`
}

// Config 描述工具名混淆策略快照（含各分组独立覆盖策略）。
type Config struct {
	Enabled         bool                    `json:"enabled"`
	Mode            string                  `json:"mode"`
	Prefix          string                  `json:"prefix"`
	Salt            string                  `json:"salt"`
	Scope           string                  `json:"scope"`
	Platforms       []string                `json:"platforms"`
	CustomMap       map[string]string       `json:"custom_map"`
	ExcludeTools    []string                `json:"exclude_tools"`
	RewriteToolRefs bool                    `json:"rewrite_tool_refs"`
	GroupOverrides  map[int64]GroupOverride `json:"group_overrides,omitempty"`
}

// MappingEntry 记录单次请求中某个工具的混淆结果。
type MappingEntry struct {
	Original   string `json:"original"`
	Obfuscated string `json:"obfuscated"`
	Changed    bool   `json:"changed"`
}

// DefaultConfig 返回开箱即用的默认工具名混淆配置。
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Mode:    ModeHash,
		Prefix:  DefaultPrefix,
		Salt:    "",
		Scope:   ScopeAll,
		Platforms: []string{
			domain.PlatformWorkBuddy,
			domain.PlatformAntigravity,
			domain.PlatformCommandCode,
			domain.PlatformOpenCode,
		},
		CustomMap:       map[string]string{},
		ExcludeTools:    []string{},
		RewriteToolRefs: false,
		GroupOverrides:  map[int64]GroupOverride{},
	}
}

// GenerateSalt 生成 32 位十六进制随机盐值。
func GenerateSalt() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("ess-toolobf-%d", time.Now().UnixNano())))
	return hex.EncodeToString(sum[:16])
}

var builtinAliasesMap = map[string]string{
	"Bash":                         "exec_task",
	"bash":                         "exec_task",
	"Read":                         "fetch_entry",
	"read_file":                    "fetch_entry",
	"read":                         "fetch_entry",
	"Edit":                         "patch_entry",
	"edit":                         "patch_entry",
	"Write":                        "store_entry",
	"write_file":                   "store_entry",
	"write":                        "store_entry",
	"MultiEdit":                    "batch_patch",
	"Grep":                         "query_text",
	"grep":                         "query_text",
	"Glob":                         "match_paths",
	"glob":                         "match_paths",
	"LS":                           "enum_dir",
	"list_dir":                     "enum_dir",
	"TodoWrite":                    "sync_notes",
	"todowrite":                    "sync_notes",
	"TodoRead":                     "load_notes",
	"WebFetch":                     "load_uri",
	"webfetch":                     "load_uri",
	"WebSearch":                    "query_web",
	"websearch":                    "query_web",
	"Task":                         "run_subtask",
	"Agent":                        "run_worker",
	"NotebookEdit":                 "patch_sheet",
	"NotebookRead":                 "fetch_sheet",
	"apply_patch":                   "merge_diff",
	"shell":                         "run_proc",
	"local_shell":                   "run_local_proc",
	"shell_command":                 "run_proc_cmd",
	"exec_command":                  "run_proc_cmd",
	"write_stdin":                   "send_proc_input",
	"str_replace_editor":            "text_patcher",
	"str_replace_based_edit_tool":   "text_patcher",
	"str_replace_based_edit_editor": "text_patcher",
	"bash_20250124":                 "exec_task_v2",
	"bash_20241022":                 "exec_task_v1",
	"text_editor_20250429":          "text_patcher_v2",
	"text_editor_20250124":          "text_patcher_v1",
	"computer":                      "ui_driver",
	"computer_20250124":             "ui_driver_v2",
	"computer_use_preview":          "ui_driver_prev",
	"web_search":                    "query_web",
	"web_search_preview":            "query_web_prev",
	"web_search_preview_2025_03_11": "query_web_prev",
	"web_search_20250305":           "query_web_v2",
	"file_search":                   "query_files",
	"code_interpreter":              "eval_snippet",
	"image_generation":              "render_graphic",
	"update_plan":                   "sync_plan",
	"view_image":                    "inspect_media",
	"dsh_exec":                      "exec_task_cli",
	"dsh_read":                      "fetch_entry_cli",
	"dsh_write":                     "store_entry_cli",
	"dsh_edit":                      "patch_entry_cli",
	"bash_output":                   "proc_output",
	"KillShell":                    "stop_proc",
	"AskUserQuestion":              "prompt_input",
	"ExitPlanMode":                 "commit_plan",
	"EnterPlanMode":                "draft_plan",
	"Skill":                        "invoke_module",
	"ToolSearch":                   "lookup_ops",
	"ListMcpResourcesTool":         "enum_resources",
	"ReadMcpResourceTool":          "fetch_resource",
}

var (
	sensitiveLowerSet  map[string]struct{}
	sortedSensitiveLst []string
)

func init() {
	sensitiveLowerSet = make(map[string]struct{}, len(builtinAliasesMap))
	sortedSensitiveLst = make([]string, 0, len(builtinAliasesMap))
	for k := range builtinAliasesMap {
		sensitiveLowerSet[strings.ToLower(k)] = struct{}{}
		sortedSensitiveLst = append(sortedSensitiveLst, k)
	}
	sort.Strings(sortedSensitiveLst)
}

// BuiltinAliases 返回内置语义别名表的防御性副本。
func BuiltinAliases() map[string]string {
	out := make(map[string]string, len(builtinAliasesMap))
	for k, v := range builtinAliasesMap {
		out[k] = v
	}
	return out
}

// SensitiveTools 返回按字母序排列的内置敏感工具名清单。
func SensitiveTools() []string {
	out := make([]string, len(sortedSensitiveLst))
	copy(out, sortedSensitiveLst)
	return out
}

// IsSensitiveAgentTool 判断工具名是否属于常见 Coding Agent 内置或敏感前缀工具。
func IsSensitiveAgentTool(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if _, ok := sensitiveLowerSet[lower]; ok {
		return true
	}
	for _, pfx := range []string{
		"mcp__", "claude_", "codex_", "opencode_", "computer_",
		"dsh_", "dsh:", "bash_", "text_editor_", "web_search",
		"local_shell", "apply_patch", "file_search", "code_interpreter",
	} {
		if strings.HasPrefix(lower, pfx) {
			return true
		}
	}
	return false
}

// NormalizeConfig 校验并规范化工具名混淆配置。
func NormalizeConfig(cfg Config) (Config, error) {
	out := Config{
		Enabled:         cfg.Enabled,
		Mode:            strings.ToLower(strings.TrimSpace(cfg.Mode)),
		Prefix:          sanitizePrefix(cfg.Prefix),
		Salt:            strings.TrimSpace(cfg.Salt),
		Scope:           strings.ToLower(strings.TrimSpace(cfg.Scope)),
		RewriteToolRefs: cfg.RewriteToolRefs,
	}

	if out.Mode == "" {
		out.Mode = ModeHash
	}
	switch out.Mode {
	case ModeHash, ModeAlias, ModePrefix:
	default:
		return Config{}, fmt.Errorf("无效的工具混淆模式 %q，仅支持 hash、alias 或 prefix", cfg.Mode)
	}

	if out.Scope == "" {
		out.Scope = ScopeAll
	}
	switch out.Scope {
	case ScopeAll, ScopeSensitiveOnly:
	default:
		return Config{}, fmt.Errorf("无效的工具混淆范围 %q，仅支持 all 或 sensitive_only", cfg.Scope)
	}

	// 规范化并去重平台列表
	if cfg.Platforms == nil {
		out.Platforms = append([]string(nil), DefaultConfig().Platforms...)
	} else {
		seenPlat := make(map[string]struct{}, len(cfg.Platforms))
		out.Platforms = make([]string, 0, len(cfg.Platforms))
		for _, p := range cfg.Platforms {
			np := domain.NormalizePlatform(p)
			if np == "" {
				continue
			}
			switch np {
			case domain.PlatformWorkBuddy, domain.PlatformAntigravity, domain.PlatformCommandCode, domain.PlatformOpenCode:
			default:
				return Config{}, fmt.Errorf("不支持的平台标识 %q", p)
			}
			if _, ok := seenPlat[np]; ok {
				continue
			}
			seenPlat[np] = struct{}{}
			out.Platforms = append(out.Platforms, np)
		}
	}

	// 规范化并校验自定义覆盖映射（目标必须合规且不得多源撞名）
	out.CustomMap = make(map[string]string, len(cfg.CustomMap))
	usedTargets := make(map[string]string, len(cfg.CustomMap))
	if len(cfg.CustomMap) > 0 {
		keys := make([]string, 0, len(cfg.CustomMap))
		for k := range cfg.CustomMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, rawKey := range keys {
			k := strings.TrimSpace(rawKey)
			if k == "" {
				continue
			}
			v := strings.TrimSpace(cfg.CustomMap[rawKey])
			if v == "" {
				continue
			}
			if !validCustomTargetRe.MatchString(v) {
				return Config{}, fmt.Errorf("自定义映射 %q -> %q 目标名不合法，需符合 ^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$", k, v)
			}
			if prevSrc, dup := usedTargets[v]; dup && prevSrc != k {
				return Config{}, fmt.Errorf("自定义映射存在目标冲突：工具 %q 与 %q 均映射到 %q", prevSrc, k, v)
			}
			usedTargets[v] = k
			out.CustomMap[k] = v
		}
	}

	// 规范化并去重排除列表
	out.ExcludeTools = make([]string, 0, len(cfg.ExcludeTools))
	seenExcl := make(map[string]struct{}, len(cfg.ExcludeTools))
	for _, t := range cfg.ExcludeTools {
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if _, ok := seenExcl[lower]; ok {
			continue
		}
		seenExcl[lower] = struct{}{}
		out.ExcludeTools = append(out.ExcludeTools, trimmed)
	}

	// 规范化各分组独立覆盖配置
	out.GroupOverrides = make(map[int64]GroupOverride, len(cfg.GroupOverrides))
	if len(cfg.GroupOverrides) > 0 {
		gids := make([]int64, 0, len(cfg.GroupOverrides))
		for gid := range cfg.GroupOverrides {
			if gid > 0 {
				gids = append(gids, gid)
			}
		}
		sort.Slice(gids, func(i, j int) bool { return gids[i] < gids[j] })
		for _, gid := range gids {
			ovNorm, err := NormalizeGroupOverride(cfg.GroupOverrides[gid])
			if err != nil {
				return Config{}, fmt.Errorf("分组 #%d 工具混淆配置无效: %w", gid, err)
			}
			if !ovNorm.IsDefaultInherit() {
				out.GroupOverrides[gid] = ovNorm
			}
		}
	}

	return out, nil
}

// IsDefaultInherit 判断分组覆盖是否完全等价于默认继承（无任何自定义差异）。
func (o GroupOverride) IsDefaultInherit() bool {
	return (o.Mode == "" || o.Mode == GroupModeInherit) &&
		strings.TrimSpace(o.Prefix) == "" &&
		strings.TrimSpace(o.Scope) == "" &&
		len(o.CustomMap) == 0 &&
		len(o.ExcludeTools) == 0 &&
		o.RewriteToolRefs == nil
}

// NormalizeGroupOverride 校验并规范化单分组工具名混淆覆盖策略。
func NormalizeGroupOverride(ov GroupOverride) (GroupOverride, error) {
	out := GroupOverride{
		Mode:  strings.ToLower(strings.TrimSpace(ov.Mode)),
		Scope: strings.ToLower(strings.TrimSpace(ov.Scope)),
	}
	if out.Mode == "" {
		out.Mode = GroupModeInherit
	}
	switch out.Mode {
	case GroupModeInherit, GroupModeOff, ModeHash, ModeAlias, ModePrefix:
	default:
		return GroupOverride{}, fmt.Errorf("无效的分组混淆模式 %q，支持 inherit/off/hash/alias/prefix", ov.Mode)
	}

	if trimmedPfx := strings.TrimSpace(ov.Prefix); trimmedPfx != "" {
		out.Prefix = sanitizePrefix(trimmedPfx)
	}

	if out.Scope != "" {
		switch out.Scope {
		case ScopeAll, ScopeSensitiveOnly:
		default:
			return GroupOverride{}, fmt.Errorf("无效的分组混淆范围 %q，支持 all 或 sensitive_only", ov.Scope)
		}
	}

	if len(ov.CustomMap) > 0 {
		out.CustomMap = make(map[string]string, len(ov.CustomMap))
		usedTargets := make(map[string]string, len(ov.CustomMap))
		keys := make([]string, 0, len(ov.CustomMap))
		for k := range ov.CustomMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, rawKey := range keys {
			k := strings.TrimSpace(rawKey)
			v := strings.TrimSpace(ov.CustomMap[rawKey])
			if k == "" || v == "" {
				continue
			}
			if !validCustomTargetRe.MatchString(v) {
				return GroupOverride{}, fmt.Errorf("自定义映射 %q -> %q 目标名不合法", k, v)
			}
			if prev, dup := usedTargets[v]; dup && prev != k {
				return GroupOverride{}, fmt.Errorf("自定义映射目标冲突：%q 与 %q 均映射到 %q", prev, k, v)
			}
			usedTargets[v] = k
			out.CustomMap[k] = v
		}
	}

	if len(ov.ExcludeTools) > 0 {
		seen := make(map[string]struct{}, len(ov.ExcludeTools))
		out.ExcludeTools = make([]string, 0, len(ov.ExcludeTools))
		for _, t := range ov.ExcludeTools {
			trimmed := strings.TrimSpace(t)
			if trimmed == "" {
				continue
			}
			lower := strings.ToLower(trimmed)
			if _, ok := seen[lower]; ok {
				continue
			}
			seen[lower] = struct{}{}
			out.ExcludeTools = append(out.ExcludeTools, trimmed)
		}
	}

	if ov.RewriteToolRefs != nil {
		b := *ov.RewriteToolRefs
		out.RewriteToolRefs = &b
	}
	return out, nil
}

func cloneGroupOverride(ov GroupOverride) GroupOverride {
	cp := GroupOverride{
		Mode:   ov.Mode,
		Prefix: ov.Prefix,
		Scope:  ov.Scope,
	}
	if len(ov.CustomMap) > 0 {
		cp.CustomMap = make(map[string]string, len(ov.CustomMap))
		for k, v := range ov.CustomMap {
			cp.CustomMap[k] = v
		}
	}
	if len(ov.ExcludeTools) > 0 {
		cp.ExcludeTools = append([]string(nil), ov.ExcludeTools...)
	}
	if ov.RewriteToolRefs != nil {
		b := *ov.RewriteToolRefs
		cp.RewriteToolRefs = &b
	}
	return cp
}

func sanitizePrefix(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultPrefix
	}
	var b strings.Builder
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		isValid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
		if isValid {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	res := b.String()
	if res == "" || strings.Trim(res, "_") == "" {
		return DefaultPrefix
	}
	if res[0] >= '0' && res[0] <= '9' {
		res = "ess_" + res
	}
	if len(res) > 20 {
		res = res[:20]
	}
	return res
}

func cloneConfig(cfg Config) Config {
	out := cfg
	if cfg.Platforms != nil {
		out.Platforms = append([]string(nil), cfg.Platforms...)
	} else {
		out.Platforms = []string{}
	}
	out.CustomMap = make(map[string]string, len(cfg.CustomMap))
	for k, v := range cfg.CustomMap {
		out.CustomMap[k] = v
	}
	if cfg.ExcludeTools != nil {
		out.ExcludeTools = append([]string(nil), cfg.ExcludeTools...)
	} else {
		out.ExcludeTools = []string{}
	}
	if len(cfg.GroupOverrides) > 0 {
		out.GroupOverrides = make(map[int64]GroupOverride, len(cfg.GroupOverrides))
		for gid, ov := range cfg.GroupOverrides {
			out.GroupOverrides[gid] = cloneGroupOverride(ov)
		}
	} else {
		out.GroupOverrides = map[int64]GroupOverride{}
	}
	return out
}

// Engine 管理线程安全的工具名混淆配置快照，并为每次中转请求签发独立 Session。
type Engine struct {
	mu  sync.RWMutex
	cfg Config
}

// NewEngine 使用指定配置构造混淆引擎。
func NewEngine(cfg Config) *Engine {
	e := &Engine{}
	e.Reload(cfg)
	return e
}

// NewDefaultEngine 构造启用默认 Hash 混淆策略的引擎。
func NewDefaultEngine() *Engine {
	cfg := DefaultConfig()
	cfg.Salt = GenerateSalt()
	return NewEngine(cfg)
}

// Config 返回当前生效配置的防御性副本。
func (e *Engine) Config() Config {
	if e == nil {
		return DefaultConfig()
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneConfig(e.cfg)
}

// GroupOverrideFor 返回指定分组的工具名混淆覆盖配置（未配置时返回 inherit）。
func (e *Engine) GroupOverrideFor(groupID int64) (GroupOverride, bool) {
	if e == nil || groupID <= 0 {
		return GroupOverride{Mode: GroupModeInherit}, false
	}
	e.mu.RLock()
	ov, ok := e.cfg.GroupOverrides[groupID]
	e.mu.RUnlock()
	if !ok {
		return GroupOverride{Mode: GroupModeInherit}, false
	}
	return cloneGroupOverride(ov), true
}

// ResolveGroupConfig 计算指定分组生效后的最终配置快照及显式模式标志。
//
// 规则：
//   - groupID <= 0 或未配置覆盖 / mode == inherit：继承全局 Enabled 与 Platforms；若覆盖中有子字段（prefix/scope/custom_map/exclude_tools/rewrite_tool_refs）则合并叠加；
//   - mode == off：在本分组内强制关闭工具名混淆；
//   - mode == hash / alias / prefix：即使全局 Enabled 为 false 或未勾选平台，也在本分组内强制开启指定模式并叠加分组参数。
func (e *Engine) ResolveGroupConfig(groupID int64) (cfg Config, explicitGroupMode bool) {
	if e == nil {
		return DefaultConfig(), false
	}
	e.mu.RLock()
	base := cloneConfig(e.cfg)
	e.mu.RUnlock()

	if groupID <= 0 {
		return base, false
	}
	ov, ok := base.GroupOverrides[groupID]
	if !ok {
		return base, false
	}

	eff := base
	switch ov.Mode {
	case GroupModeOff:
		eff.Enabled = false
		return eff, true
	case ModeHash, ModeAlias, ModePrefix:
		eff.Enabled = true
		eff.Mode = ov.Mode
		explicitGroupMode = true
	}

	if strings.TrimSpace(ov.Prefix) != "" {
		eff.Prefix = ov.Prefix
	}
	if strings.TrimSpace(ov.Scope) != "" {
		eff.Scope = ov.Scope
	}
	if len(ov.CustomMap) > 0 {
		for k, v := range ov.CustomMap {
			eff.CustomMap[k] = v
		}
	}
	if len(ov.ExcludeTools) > 0 {
		seen := make(map[string]struct{}, len(eff.ExcludeTools)+len(ov.ExcludeTools))
		for _, t := range eff.ExcludeTools {
			seen[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
		}
		for _, t := range ov.ExcludeTools {
			lower := strings.ToLower(strings.TrimSpace(t))
			if _, dup := seen[lower]; !dup {
				seen[lower] = struct{}{}
				eff.ExcludeTools = append(eff.ExcludeTools, t)
			}
		}
	}
	if ov.RewriteToolRefs != nil {
		eff.RewriteToolRefs = *ov.RewriteToolRefs
	}
	return eff, explicitGroupMode
}

// Reload 热替换当前生效的工具名混淆配置。
func (e *Engine) Reload(cfg Config) {
	if e == nil {
		return
	}
	norm, err := NormalizeConfig(cfg)
	if err != nil {
		// 配置校验应在写入前完成；此处兜底使用安全默认配置防止零值失效。
		norm, _ = NormalizeConfig(DefaultConfig())
	}
	e.mu.Lock()
	e.cfg = norm
	e.mu.Unlock()
}

// ReloadStrict 校验并热更新配置，非法时返回错误且保留旧配置。
func (e *Engine) ReloadStrict(cfg Config) error {
	if e == nil {
		return errors.New("toolobf engine is nil")
	}
	norm, err := NormalizeConfig(cfg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.cfg = norm
	e.mu.Unlock()
	return nil
}

// EnabledForPlatform 判断指定上游平台是否启用工具名混淆。
func (e *Engine) EnabledForPlatform(platform string) bool {
	return e.EnabledForGroupPlatform(0, platform)
}

// EnabledForGroupPlatform 判断指定（分组ID + 上游平台）是否启用工具名混淆。
func (e *Engine) EnabledForGroupPlatform(groupID int64, platform string) bool {
	if e == nil {
		return false
	}
	cfg, explicit := e.ResolveGroupConfig(groupID)
	if !cfg.Enabled {
		return false
	}
	if explicit {
		return true
	}
	if len(cfg.Platforms) == 0 {
		return false
	}
	target := domain.NormalizePlatform(platform)
	if target == "" {
		// 预览或未指定具体平台时允许生效
		return true
	}
	for _, p := range cfg.Platforms {
		if p == target {
			return true
		}
	}
	return false
}

// NewSession 为单次请求构造双射映射会话；若目标平台未启用则返回 nil。
func (e *Engine) NewSession(platform string) *Session {
	return e.NewSessionForGroup(0, platform)
}

// NewSessionForGroup 按分组覆盖策略与目标平台为单次请求构造双射映射会话。
func (e *Engine) NewSessionForGroup(groupID int64, platform string) *Session {
	if e == nil {
		return nil
	}
	cfg, explicit := e.ResolveGroupConfig(groupID)
	if !cfg.Enabled {
		return nil
	}
	if !explicit {
		if len(cfg.Platforms) == 0 {
			return nil
		}
		if target := domain.NormalizePlatform(platform); target != "" {
			matched := false
			for _, p := range cfg.Platforms {
				if p == target {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}
	}

	excl := make(map[string]struct{}, len(cfg.ExcludeTools)*2)
	for _, t := range cfg.ExcludeTools {
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			continue
		}
		excl[trimmed] = struct{}{}
		excl[strings.ToLower(trimmed)] = struct{}{}
	}

	return &Session{
		cfg:        cfg,
		excludeSet: excl,
		forward:    make(map[string]string, 16),
		reverse:    make(map[string]string, 16),
		reserved:   make(map[string]struct{}, 16),
		order:      make([]string, 0, 16),
	}
}

// Session 保存单次请求周期内的原名↔混淆名严格双射表（支持并发安全读写）。
type Session struct {
	mu         sync.RWMutex
	cfg        Config
	excludeSet map[string]struct{}
	forward    map[string]string
	reverse    map[string]string
	reserved   map[string]struct{}
	order      []string
}

// Config 返回该会话捕获的配置快照。
func (s *Session) Config() Config {
	if s == nil {
		return Config{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.cfg)
}

// RegisterPassthrough 预占透传保留的工具名，确保后续生成的混淆名永不与之冲突（同时保留原出现顺序供 Obfuscate 记录）。
func (s *Session) RegisterPassthrough(name string) {
	if s == nil {
		return
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved[trimmed] = struct{}{}
}

// Obfuscate 将原始工具名确定性映射为上游混淆名；同一原名在会话内多次调用结果恒等。
func (s *Session) Obfuscate(orig string) string {
	if s == nil || orig == "" {
		return orig
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if mapped, ok := s.forward[orig]; ok {
		return mapped
	}

	if s.isPassthroughLocked(orig) {
		s.reserved[orig] = struct{}{}
		s.forward[orig] = orig
		s.order = append(s.order, orig)
		return orig
	}

	cand := s.computeCandidateLocked(orig)
	cand = s.disambiguateLocked(orig, cand)

	if cand == orig {
		s.reserved[orig] = struct{}{}
		s.forward[orig] = orig
		s.order = append(s.order, orig)
		return orig
	}

	s.forward[orig] = cand
	s.reverse[cand] = orig
	s.order = append(s.order, orig)
	return cand
}

func (s *Session) isPassthroughLocked(orig string) bool {
	if _, ok := s.excludeSet[orig]; ok {
		return true
	}
	if _, ok := s.excludeSet[strings.ToLower(orig)]; ok {
		return true
	}
	if s.cfg.Scope == ScopeSensitiveOnly {
		if _, hasCustom := s.cfg.CustomMap[orig]; !hasCustom && !IsSensitiveAgentTool(orig) {
			return true
		}
	}
	return false
}

func (s *Session) computeCandidateLocked(orig string) string {
	if custom, ok := s.cfg.CustomMap[orig]; ok && custom != "" {
		return clampToolLen(custom, s.cfg.Salt, orig)
	}

	switch s.cfg.Mode {
	case ModeAlias:
		if alias, ok := builtinAliasesMap[orig]; ok && alias != "" {
			return clampToolLen(alias, s.cfg.Salt, orig)
		}
		return s.hashCandidateLocked(orig)
	case ModePrefix:
		ident := sanitizeToolIdent(orig)
		full := s.cfg.Prefix + ident
		if len(full) <= MaxToolNameLen {
			return full
		}
		hash8 := shortSHA256(s.cfg.Salt, orig, 8)
		keep := MaxToolNameLen - 1 - len(hash8)
		if keep > 55 {
			keep = 55
		}
		if keep < 1 {
			keep = 1
		}
		if keep > len(full) {
			keep = len(full)
		}
		return full[:keep] + "_" + hash8
	default:
		return s.hashCandidateLocked(orig)
	}
}

func (s *Session) hashCandidateLocked(orig string) string {
	h := shortSHA256(s.cfg.Salt, orig, 10)
	cand := s.cfg.Prefix + h
	if len(cand) > MaxToolNameLen {
		keep := MaxToolNameLen - len(h)
		if keep < 1 {
			return "t_" + h[:minInt(len(h), MaxToolNameLen-2)]
		}
		cand = s.cfg.Prefix[:keep] + h
	}
	return cand
}

func (s *Session) disambiguateLocked(orig, base string) string {
	cand := base
	for idx := 2; ; idx++ {
		_, usedByRev := s.reverse[cand]
		_, usedByRes := s.reserved[cand]
		if !usedByRev && !usedByRes {
			// 同样确保不会意外撞上另一个尚未处理、但已确定会碰撞的原始排除工具
			return cand
		}
		if prevOrig, ok := s.reverse[cand]; ok && prevOrig == orig {
			return cand
		}
		suffix := "_" + strconv.Itoa(idx)
		maxBase := MaxToolNameLen - len(suffix)
		if maxBase < 1 {
			maxBase = 1
		}
		trimmedBase := base
		if len(trimmedBase) > maxBase {
			trimmedBase = trimmedBase[:maxBase]
		}
		cand = trimmedBase + suffix
	}
}

func shortSHA256(salt, orig string, hexLen int) string {
	sum := sha256.Sum256([]byte(salt + ":" + orig))
	full := hex.EncodeToString(sum[:])
	if hexLen <= 0 || hexLen >= len(full) {
		return full
	}
	return full[:hexLen]
}

func clampToolLen(name, salt, orig string) string {
	if len(name) <= MaxToolNameLen {
		return name
	}
	h := shortSHA256(salt, orig, 8)
	keep := MaxToolNameLen - 1 - len(h)
	if keep < 1 {
		keep = 1
	}
	return name[:keep] + "_" + h
}

func sanitizeToolIdent(raw string) string {
	if raw == "" {
		return "tool"
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		isOK := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
		if isOK {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	res := b.String()
	if res == "" {
		return "tool"
	}
	return res
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Restore 将上游响应里的混淆工具名精确还原为下游客户端识别的原始工具名。
func (s *Session) Restore(wire string) string {
	if s == nil || wire == "" {
		return wire
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if orig, ok := s.reverse[wire]; ok {
		return orig
	}
	return wire
}

// IsObfuscatedPrefix 判断 partial 是否可能是某个已映射混淆工具名的严格真前缀，
// 用于流式 SSE 工具名跨分片到达时的防泄漏暂存缓冲。
func (s *Session) IsObfuscatedPrefix(partial string) bool {
	if s == nil || partial == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for obf := range s.reverse {
		if obf != partial && strings.HasPrefix(obf, partial) {
			return true
		}
	}
	return false
}

// Count 返回本会话内实际发生重写（orig != obfuscated）的工具数量。
func (s *Session) Count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.reverse)
}

// Entries 按首次出现顺序返回所有工具映射条目（含透传未改动条目）。
func (s *Session) Entries() []MappingEntry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MappingEntry, 0, len(s.order))
	for _, orig := range s.order {
		obf := s.forward[orig]
		out = append(out, MappingEntry{
			Original:   orig,
			Obfuscated: obf,
			Changed:    obf != orig,
		})
	}
	return out
}

// RewriteBacktickRefs 若启用了 RewriteToolRefs，则仅替换反引号包裹的精确工具名引用（不影响正文中自然语言）。
func (s *Session) RewriteBacktickRefs(text string) string {
	if s == nil || text == "" {
		return text
	}
	s.mu.RLock()
	enabled := s.cfg.RewriteToolRefs
	revLen := len(s.reverse)
	if !enabled || revLen == 0 {
		s.mu.RUnlock()
		return text
	}
	fwdCopy := make(map[string]string, len(s.forward))
	for k, v := range s.forward {
		if k != v {
			fwdCopy[k] = v
		}
	}
	s.mu.RUnlock()

	if !strings.Contains(text, "`") {
		return text
	}

	locs := backtickSpanRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	last := 0
	changed := false
	for _, loc := range locs {
		fullStart, fullEnd := loc[0], loc[1]
		innerStart, innerEnd := loc[2], loc[3]
		// 跳过连续多个反引号（如 ``` 代码块标记或 `` 转义）
		if (fullStart > 0 && text[fullStart-1] == '`') || (fullEnd < len(text) && text[fullEnd] == '`') {
			continue
		}
		inner := text[innerStart:innerEnd]
		obf, ok := fwdCopy[inner]
		if !ok {
			continue
		}
		b.WriteString(text[last:innerStart])
		b.WriteString(obf)
		last = innerEnd
		changed = true
	}
	if !changed {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}
