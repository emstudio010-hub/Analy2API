package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/systemsetting"
	"cmd2api/internal/domain"
	"cmd2api/internal/netx"
	"cmd2api/internal/workbuddy"
)

const (
	// SettingKeyProxyPoolConfig 存储代理 IP 池、远程拉取源与各管理 Key 代理策略的 JSON 配置。
	SettingKeyProxyPoolConfig = "proxy_pool_config"

	ProxyStrategyRoundRobin   = "round_robin"
	ProxyStrategyLeastUsed    = "least_used"
	ProxyStrategyStickyUnique = "sticky_unique"
	ProxyStrategyRandom       = "random"

	KeyProxyModeInherit = "inherit"
	KeyProxyModePool    = "pool"
	KeyProxyModeFixed   = "fixed"
	KeyProxyModeFetch   = "fetch"
	KeyProxyModeDirect  = "direct"

	ProxySourceManual      = "manual"
	ProxySourceRemoteFetch = "remote_fetch"
)

// ProxyPoolEntry 表示代理 IP 池中的单条出口代理记录。
type ProxyPoolEntry struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	MaskedURL     string `json:"masked_url,omitempty"`
	Tag           string `json:"tag,omitempty"`
	Note          string `json:"note,omitempty"`
	Enabled       bool   `json:"enabled"`
	Source        string `json:"source,omitempty"` // "manual" | "remote_fetch"
	AddedAt       string `json:"added_at,omitempty"`
	LastUsedAt    string `json:"last_used_at,omitempty"`
	LastCheckOK   *bool  `json:"last_check_ok,omitempty"`
	LastExitIP    string `json:"last_exit_ip,omitempty"`
	LastLatencyMS int64  `json:"last_latency_ms,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	BoundAccounts int    `json:"bound_accounts"`
}

// ProxyFetchConfig 描述从外部代理商 API 拉取代理 IP 的配置。
type ProxyFetchConfig struct {
	Enabled          bool              `json:"enabled"`
	URL              string            `json:"url"`
	Method           string            `json:"method"` // GET | POST
	Headers          map[string]string `json:"headers,omitempty"`
	Body             string            `json:"body,omitempty"`
	DefaultScheme    string            `json:"default_scheme"` // http | https | socks5 | socks5h
	Username         string            `json:"username,omitempty"`
	Password         string            `json:"password,omitempty"`
	DefaultTag       string            `json:"default_tag,omitempty"`
	JSONPath         string            `json:"json_path,omitempty"`
	AutoFetchOnEmpty bool              `json:"auto_fetch_on_empty"`
	ReplaceOnFetch   bool              `json:"replace_on_fetch"`
	IntervalMinutes  int               `json:"interval_minutes"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
	LastFetchedAt    string            `json:"last_fetched_at,omitempty"`
	LastFetchCount   int               `json:"last_fetch_count,omitempty"`
	LastFetchError   string            `json:"last_fetch_error,omitempty"`
}

// AdminKeyProxyPolicy 描述单把远程补号/管理密钥的专属 IP 分配策略。
type AdminKeyProxyPolicy struct {
	Mode                string `json:"mode"`                           // inherit | pool | fixed | fetch | direct
	FixedProxyURL       string `json:"fixed_proxy_url,omitempty"`      // 固定代理地址（mode=fixed）
	FixedProxyMasked    string `json:"fixed_proxy_masked,omitempty"`   // 响应脱敏展示
	PoolTag             string `json:"pool_tag,omitempty"`             // 指定只从该标签子池分配
	Strategy            string `json:"strategy,omitempty"`             // "" / inherit / round_robin / least_used / sticky_unique / random
	FetchURLOverride    string `json:"fetch_url_override,omitempty"`   // 可选：覆盖全局远程拉取 API 地址
	AllowOverrideInPush *bool  `json:"allow_override_in_push,omitempty"`
}

// ProxyPoolConfig 描述全局代理 IP 池持久化配置。
type ProxyPoolConfig struct {
	Enabled          bool                          `json:"enabled"`
	Strategy         string                        `json:"strategy"`
	MaxAccountsPerIP int                           `json:"max_accounts_per_ip"`
	ApplyPlatforms   []string                      `json:"apply_platforms,omitempty"`
	Entries          []ProxyPoolEntry              `json:"entries"`
	Fetch            ProxyFetchConfig              `json:"fetch"`
	KeyPolicies      map[int64]AdminKeyProxyPolicy `json:"key_policies,omitempty"`
	Cursor           int                           `json:"cursor"`
}

// ProxyPoolStats 汇总代理池实时运行指标。
type ProxyPoolStats struct {
	TotalEntries   int      `json:"total_entries"`
	EnabledEntries int      `json:"enabled_entries"`
	ManualEntries  int      `json:"manual_entries"`
	FetchedEntries int      `json:"fetched_entries"`
	BoundTotal     int      `json:"bound_total"`
	Tags           []string `json:"tags"`
}

// ProxyPoolView 是返回给前端控制台与运维接口的完整视图。
type ProxyPoolView struct {
	Config ProxyPoolConfig `json:"config"`
	Stats  ProxyPoolStats  `json:"stats"`
}

// ProxyFetchResult 描述一次远程拉取代理 IP 的执行结果。
type ProxyFetchResult struct {
	OK            bool     `json:"ok"`
	FetchedURL    string   `json:"fetched_url"`
	ParsedCount   int      `json:"parsed_count"`
	AddedCount    int      `json:"added_count"`
	UpdatedCount  int      `json:"updated_count"`
	TotalPoolSize int      `json:"total_pool_size"`
	DryRun        bool     `json:"dry_run"`
	SampleProxies []string `json:"sample_proxies"`
	Error         string   `json:"error,omitempty"`
}

// ProxyAllocationRequest 描述为单条远程补号分配代理时的上下文输入。
type ProxyAllocationRequest struct {
	AdminKeyID        int64
	Platform          string
	ItemProxyRaw      string
	BatchProxyRaw     string
	BatchProxyList    []string
	BatchIndex        int
	RequestMode       string // "" | "auto" | "pool" | "fetch" | "direct"
	RequestTag        string
	RequestStrategy   string
	FetchURLOverride  string
	InFlightBindings  map[string]int // 同批次内已分配计数，避免并发批量全部落到同一 IP
	ExistingProxyURL  string         // 幂等更新场景下已有账号的代理
	AllowEmptyOnError bool           // true 时池空/拉取失败返回空串而非阻断补号
}

// ProxyAllocationResult 描述代理分配最终结果与来源说明。
type ProxyAllocationResult struct {
	ProxyURL       string `json:"proxy_url,omitempty"`
	MaskedProxyURL string `json:"masked_proxy_url,omitempty"`
	Source         string `json:"source"` // request | request_list | request_default | key_fixed | key_pool_* | pool_* | remote_fetch | existing | direct
	Tag            string `json:"tag,omitempty"`
	EntryID        string `json:"entry_id,omitempty"`
	Warning        string `json:"warning,omitempty"`
}

// ProxyProbeResult 描述单条代理连通性探测结果。
type ProxyProbeResult struct {
	ID        string `json:"id,omitempty"`
	MaskedURL string `json:"masked_url"`
	OK        bool   `json:"ok"`
	ExitIP    string `json:"exit_ip,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// ProxyPoolService 管理代理 IP 池配置持久化、远程拉取、自动轮询分配、Key 策略与连通性探测。
type ProxyPoolService struct {
	mu      sync.Mutex
	db      *ent.Client
	cfg     ProxyPoolConfig
	loaded  bool
	logger  *slog.Logger
	httpCli *http.Client
}

// DefaultProxyPoolConfig 返回开箱即用的代理 IP 池默认配置。
func DefaultProxyPoolConfig() ProxyPoolConfig {
	return ProxyPoolConfig{
		Enabled:          false,
		Strategy:         ProxyStrategyRoundRobin,
		MaxAccountsPerIP: 0,
		ApplyPlatforms: []string{
			domain.PlatformAntigravity,
			domain.PlatformWorkBuddy,
			domain.PlatformCommandCode,
			domain.PlatformOpenCode,
		},
		Entries: []ProxyPoolEntry{},
		Fetch: ProxyFetchConfig{
			Enabled:          false,
			Method:           http.MethodGet,
			DefaultScheme:    "http",
			DefaultTag:       "remote",
			AutoFetchOnEmpty: true,
			ReplaceOnFetch:   false,
			IntervalMinutes:  0,
			TimeoutSeconds:   15,
		},
		KeyPolicies: map[int64]AdminKeyProxyPolicy{},
		Cursor:      0,
	}
}

// NewProxyPoolService 构造 ProxyPoolService。
func NewProxyPoolService(db *ent.Client, logger *slog.Logger) *ProxyPoolService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProxyPoolService{
		db:     db,
		cfg:    DefaultProxyPoolConfig(),
		logger: logger,
	}
}

// EnsureSeeded 从数据库加载代理池配置；首次启动时写入默认配置。
func (s *ProxyPoolService) EnsureSeeded(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureLoadedLocked(ctx)
}

// Start 启动后台定时远程拉取循环（仅在配置启用且 IntervalMinutes > 0 时触发）。
func (s *ProxyPoolService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.maybeRunScheduledFetch(ctx)
		}
	}
}

func (s *ProxyPoolService) maybeRunScheduledFetch(ctx context.Context) {
	s.mu.Lock()
	_ = s.ensureLoadedLocked(ctx)
	fetchCfg := s.cfg.Fetch
	s.mu.Unlock()

	if !fetchCfg.Enabled || strings.TrimSpace(fetchCfg.URL) == "" || fetchCfg.IntervalMinutes <= 0 {
		return
	}
	if fetchCfg.LastFetchedAt != "" {
		if last, err := time.Parse(time.RFC3339, fetchCfg.LastFetchedAt); err == nil {
			if time.Since(last) < time.Duration(fetchCfg.IntervalMinutes)*time.Minute {
				return
			}
		}
	}
	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(clampInt(fetchCfg.TimeoutSeconds, 5, 60))*time.Second)
	defer cancel()
	res, err := s.FetchFromRemote(fetchCtx, "", false)
	if err != nil {
		s.logger.Warn("定时拉取代理 IP 池失败", "err", err)
		return
	}
	s.logger.Info("定时拉取代理 IP 池完成", "parsed", res.ParsedCount, "added", res.AddedCount, "total", res.TotalPoolSize)
}

func (s *ProxyPoolService) ensureLoadedLocked(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	if s.db == nil {
		s.cfg = normalizeProxyPoolConfig(s.cfg)
		s.loaded = true
		return nil
	}
	row, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(SettingKeyProxyPoolConfig)).
		Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("查询代理 IP 池配置失败: %w", err)
		}
		s.cfg = DefaultProxyPoolConfig()
		_ = s.persistLocked(ctx)
		s.loaded = true
		return nil
	}
	var loaded ProxyPoolConfig
	if uErr := json.Unmarshal([]byte(row.Value), &loaded); uErr != nil {
		s.cfg = DefaultProxyPoolConfig()
		_ = s.persistLocked(ctx)
	} else {
		s.cfg = normalizeProxyPoolConfig(loaded)
	}
	s.loaded = true
	return nil
}

func (s *ProxyPoolService) persistLocked(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	toSave := cloneProxyPoolConfig(s.cfg)
	// 不持久化派生字段，避免下次读取产生脏统计
	for i := range toSave.Entries {
		toSave.Entries[i].MaskedURL = ""
		toSave.Entries[i].BoundAccounts = 0
	}
	for k, kp := range toSave.KeyPolicies {
		kp.FixedProxyMasked = ""
		toSave.KeyPolicies[k] = kp
	}
	raw, err := json.Marshal(toSave)
	if err != nil {
		return fmt.Errorf("序列化代理 IP 池配置失败: %w", err)
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	existing, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(SettingKeyProxyPoolConfig)).
		Only(bg)
	if err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		_, err = s.db.SystemSetting.Create().
			SetKey(SettingKeyProxyPoolConfig).
			SetValue(string(raw)).
			Save(bg)
		return err
	}
	_, err = existing.Update().
		SetValue(string(raw)).
		Save(bg)
	return err
}

// GetView 返回完整的代理 IP 池视图（附带实时账号绑定数与脱敏 URL）。
func (s *ProxyPoolService) GetView(ctx context.Context) (*ProxyPoolView, error) {
	if s == nil {
		def := DefaultProxyPoolConfig()
		return &ProxyPoolView{Config: def}, nil
	}
	s.mu.Lock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	snap := cloneProxyPoolConfig(s.cfg)
	s.mu.Unlock()

	return s.decorateView(ctx, snap), nil
}

// UpdateConfig 更新全局代理 IP 池配置并持久化；若传入 rawProxies 则顺带解析并合并进池。
func (s *ProxyPoolService) UpdateConfig(ctx context.Context, next ProxyPoolConfig, rawProxies, rawTag string) (*ProxyPoolView, error) {
	s.mu.Lock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	// 保留已有的 KeyPolicies（前端保存全局 IP 池时 key_policies 为空 map 或 nil，不应清空已绑定的 Key 策略）；
	// 若显式传入条目且带 *** 脱敏密码，自动合并并还原原密码。
	if len(next.KeyPolicies) == 0 && len(s.cfg.KeyPolicies) > 0 {
		next.KeyPolicies = cloneKeyPolicies(s.cfg.KeyPolicies)
	} else if len(next.KeyPolicies) > 0 && len(s.cfg.KeyPolicies) > 0 {
		mergedKP := cloneKeyPolicies(s.cfg.KeyPolicies)
		for id, kp := range next.KeyPolicies {
			if oldKP, ok := s.cfg.KeyPolicies[id]; ok && (strings.Contains(kp.FixedProxyURL, "***") || strings.Contains(strings.ToUpper(kp.FixedProxyURL), "%2A%2A%2A")) {
				kp.FixedProxyURL = workbuddy.PreserveRedactedProxyPassword(kp.FixedProxyURL, oldKP.FixedProxyURL)
			}
			mergedKP[id] = kp
		}
		next.KeyPolicies = mergedKP
	}
	// 若前端回传了脱敏后的远程拉取默认密码 "***"，保留原密码
	if next.Fetch.Password == "***" && s.cfg.Fetch.Password != "" {
		next.Fetch.Password = s.cfg.Fetch.Password
	}
	// 若前端回传了脱敏后的池条目 URL（如 http://user:***@ip:port），按 ID 或脱敏值匹配保留真实密码
	if len(next.Entries) > 0 && len(s.cfg.Entries) > 0 {
		oldByID := make(map[string]string, len(s.cfg.Entries))
		oldByMasked := make(map[string]string, len(s.cfg.Entries))
		for _, oldEntry := range s.cfg.Entries {
			if oldEntry.ID != "" {
				oldByID[oldEntry.ID] = oldEntry.URL
			}
			oldByMasked[MaskProxyURL(oldEntry.URL)] = oldEntry.URL
		}
		for i := range next.Entries {
			u := strings.TrimSpace(next.Entries[i].URL)
			if !strings.Contains(u, "***") {
				continue
			}
			if oldURL, ok := oldByID[next.Entries[i].ID]; ok {
				next.Entries[i].URL = workbuddy.PreserveRedactedProxyPassword(u, oldURL)
			} else if oldURL, ok := oldByMasked[u]; ok {
				next.Entries[i].URL = workbuddy.PreserveRedactedProxyPassword(u, oldURL)
			}
		}
	}
	// 保留最近一次拉取状态统计
	if next.Fetch.LastFetchedAt == "" {
		next.Fetch.LastFetchedAt = s.cfg.Fetch.LastFetchedAt
		next.Fetch.LastFetchCount = s.cfg.Fetch.LastFetchCount
		next.Fetch.LastFetchError = s.cfg.Fetch.LastFetchError
	}
	// 保留轮询游标
	if next.Cursor == 0 && s.cfg.Cursor > 0 {
		next.Cursor = s.cfg.Cursor
	}

	normalized := normalizeProxyPoolConfig(next)
	if strings.TrimSpace(rawProxies) != "" {
		parsed := ParseProxyLines(rawProxies, normalized.Fetch.DefaultScheme, normalized.Fetch.Username, normalized.Fetch.Password, rawTag)
		mergeEntriesIntoConfig(&normalized, parsed, ProxySourceManual, false)
	}
	s.cfg = normalized
	if err := s.persistLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("保存代理 IP 池配置失败: %w", err)
	}
	snap := cloneProxyPoolConfig(s.cfg)
	s.mu.Unlock()

	s.logger.Info("已更新代理 IP 池配置",
		"enabled", snap.Enabled,
		"strategy", snap.Strategy,
		"entries", len(snap.Entries),
		"fetch_enabled", snap.Fetch.Enabled,
	)
	return s.decorateView(ctx, snap), nil
}

// AddEntries 解析并批量导入手动代理条目（去重合并）。
func (s *ProxyPoolService) AddEntries(ctx context.Context, rawText string, urls []string, tag, note string) (*ProxyPoolView, int, error) {
	s.mu.Lock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, 0, err
	}
	defScheme := s.cfg.Fetch.DefaultScheme
	defUser := s.cfg.Fetch.Username
	defPass := s.cfg.Fetch.Password

	var incoming []ProxyPoolEntry
	if strings.TrimSpace(rawText) != "" {
		incoming = append(incoming, ParseProxyLines(rawText, defScheme, defUser, defPass, tag)...)
	}
	for _, raw := range urls {
		incoming = append(incoming, ParseProxyLines(raw, defScheme, defUser, defPass, tag)...)
	}
	if len(incoming) == 0 {
		s.mu.Unlock()
		return nil, 0, errors.New("未解析到任何有效代理地址（支持 http/https/socks5 URL、ip:port、ip:port:user:pass）")
	}
	if strings.TrimSpace(note) != "" {
		for i := range incoming {
			if incoming[i].Note == "" {
				incoming[i].Note = strings.TrimSpace(note)
			}
		}
	}
	added, _ := mergeEntriesIntoConfig(&s.cfg, incoming, ProxySourceManual, false)
	if err := s.persistLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, 0, err
	}
	snap := cloneProxyPoolConfig(s.cfg)
	s.mu.Unlock()
	return s.decorateView(ctx, snap), added, nil
}

// DeleteEntry 按 ID 删除代理池中的单条代理。
func (s *ProxyPoolService) DeleteEntry(ctx context.Context, id string) (*ProxyPoolView, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("代理条目 ID 不能为空")
	}
	s.mu.Lock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	next := make([]ProxyPoolEntry, 0, len(s.cfg.Entries))
	removed := false
	for _, e := range s.cfg.Entries {
		if e.ID == id {
			removed = true
			continue
		}
		next = append(next, e)
	}
	if !removed {
		s.mu.Unlock()
		return nil, fmt.Errorf("代理条目 %q 不存在", id)
	}
	s.cfg.Entries = next
	if err := s.persistLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	snap := cloneProxyPoolConfig(s.cfg)
	s.mu.Unlock()
	return s.decorateView(ctx, snap), nil
}

// GetKeyPolicy 读取指定管理密钥的代理分配策略。
func (s *ProxyPoolService) GetKeyPolicy(ctx context.Context, adminKeyID int64) AdminKeyProxyPolicy {
	def := defaultAdminKeyProxyPolicy()
	if s == nil || adminKeyID <= 0 {
		return def
	}
	s.mu.Lock()
	_ = s.ensureLoadedLocked(ctx)
	kp, ok := s.cfg.KeyPolicies[adminKeyID]
	s.mu.Unlock()
	if !ok {
		return def
	}
	kp = normalizeAdminKeyProxyPolicy(kp)
	if kp.FixedProxyURL != "" {
		kp.FixedProxyMasked = MaskProxyURL(kp.FixedProxyURL)
		kp.FixedProxyURL = kp.FixedProxyMasked
	}
	return kp
}

// GetAllKeyPolicies 返回全部已配置的 Key 代理策略快照（带脱敏 URL）。
func (s *ProxyPoolService) GetAllKeyPolicies(ctx context.Context) map[int64]AdminKeyProxyPolicy {
	out := make(map[int64]AdminKeyProxyPolicy)
	if s == nil {
		return out
	}
	s.mu.Lock()
	_ = s.ensureLoadedLocked(ctx)
	for id, kp := range s.cfg.KeyPolicies {
		norm := normalizeAdminKeyProxyPolicy(kp)
		if norm.FixedProxyURL != "" {
			norm.FixedProxyMasked = MaskProxyURL(norm.FixedProxyURL)
			norm.FixedProxyURL = norm.FixedProxyMasked
		}
		out[id] = norm
	}
	s.mu.Unlock()
	return out
}

// SetKeyPolicy 设置并持久化指定管理密钥的代理分配策略。
func (s *ProxyPoolService) SetKeyPolicy(ctx context.Context, adminKeyID int64, policy AdminKeyProxyPolicy) (AdminKeyProxyPolicy, error) {
	if s == nil {
		return defaultAdminKeyProxyPolicy(), errors.New("代理池服务未初始化")
	}
	if adminKeyID <= 0 {
		return defaultAdminKeyProxyPolicy(), errors.New("无效的管理密钥 ID")
	}

	s.mu.Lock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		s.mu.Unlock()
		return defaultAdminKeyProxyPolicy(), err
	}
	norm := normalizeAdminKeyProxyPolicy(policy)
	if existing, ok := s.cfg.KeyPolicies[adminKeyID]; ok && (strings.Contains(norm.FixedProxyURL, "***") || strings.Contains(strings.ToUpper(norm.FixedProxyURL), "%2A%2A%2A")) {
		norm.FixedProxyURL = workbuddy.PreserveRedactedProxyPassword(norm.FixedProxyURL, existing.FixedProxyURL)
	}
	if norm.FixedProxyURL != "" {
		normalized, err := NormalizeProxyAddress(norm.FixedProxyURL, "http", "", "")
		if err != nil {
			s.mu.Unlock()
			return norm, fmt.Errorf("固定代理地址格式不合法: %w", err)
		}
		norm.FixedProxyURL = normalized
		norm.FixedProxyMasked = MaskProxyURL(normalized)
	}
	if norm.Mode == KeyProxyModeFixed && norm.FixedProxyURL == "" {
		s.mu.Unlock()
		return norm, errors.New("选择固定代理模式 (fixed) 时必须填写 fixed_proxy_url")
	}
	if s.cfg.KeyPolicies == nil {
		s.cfg.KeyPolicies = make(map[int64]AdminKeyProxyPolicy)
	}
	s.cfg.KeyPolicies[adminKeyID] = norm
	if err := s.persistLocked(ctx); err != nil {
		s.mu.Unlock()
		return norm, fmt.Errorf("保存管理密钥代理策略失败: %w", err)
	}
	s.mu.Unlock()

	ret := norm
	if ret.FixedProxyMasked != "" {
		ret.FixedProxyURL = ret.FixedProxyMasked
	}
	return ret, nil
}

// DeleteKeyPolicy 清理已吊销管理密钥对应的策略记录。
func (s *ProxyPoolService) DeleteKeyPolicy(ctx context.Context, adminKeyID int64) {
	if s == nil || adminKeyID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		return
	}
	if _, ok := s.cfg.KeyPolicies[adminKeyID]; ok {
		delete(s.cfg.KeyPolicies, adminKeyID)
		_ = s.persistLocked(ctx)
	}
}

// FetchFromRemote 从远程代理商 API 拉取最新代理 IP（支持覆盖 URL、dryRun 预览或落池去重合并）。
func (s *ProxyPoolService) FetchFromRemote(ctx context.Context, urlOverride string, dryRun bool) (*ProxyFetchResult, error) {
	if s == nil {
		return nil, errors.New("代理池服务未初始化")
	}
	s.mu.Lock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	fetchCfg := s.cfg.Fetch
	s.mu.Unlock()

	targetURL := strings.TrimSpace(urlOverride)
	if targetURL == "" {
		targetURL = strings.TrimSpace(fetchCfg.URL)
	}
	if targetURL == "" {
		return nil, errors.New("尚未配置远程代理拉取 API 地址 (fetch.url)")
	}
	if _, err := url.ParseRequestURI(targetURL); err != nil {
		return nil, fmt.Errorf("远程拉取地址不合法: %w", err)
	}

	method := strings.ToUpper(strings.TrimSpace(fetchCfg.Method))
	if method != http.MethodPost {
		method = http.MethodGet
	}
	timeout := time.Duration(clampInt(fetchCfg.TimeoutSeconds, 3, 60)) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cli := s.httpCli
	if cli == nil {
		cli = netx.NewHTTP2Client(timeout)
	}
	resp, err := netx.DoWithEOFRetry(reqCtx, cli, func() (*http.Request, error) {
		var bodyRdr io.Reader
		if method == http.MethodPost && fetchCfg.Body != "" {
			bodyRdr = strings.NewReader(fetchCfg.Body)
		}
		req, rErr := http.NewRequestWithContext(reqCtx, method, targetURL, bodyRdr)
		if rErr != nil {
			return nil, fmt.Errorf("构造远程拉取请求失败: %w", rErr)
		}
		req.Header.Set("User-Agent", "Analy2API-ProxyPool/1.0")
		for hk, hv := range fetchCfg.Headers {
			if strings.TrimSpace(hk) != "" {
				req.Header.Set(strings.TrimSpace(hk), strings.TrimSpace(hv))
			}
		}
		return req, nil
	}, 2)
	if err != nil {
		s.recordFetchStatus(ctx, 0, err.Error())
		return &ProxyFetchResult{OK: false, FetchedURL: targetURL, DryRun: dryRun, Error: err.Error()}, fmt.Errorf("请求远程代理接口失败: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		s.recordFetchStatus(ctx, 0, err.Error())
		return &ProxyFetchResult{OK: false, FetchedURL: targetURL, DryRun: dryRun, Error: err.Error()}, fmt.Errorf("读取远程代理响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := fmt.Sprintf("远程代理接口返回 HTTP %d: %s", resp.StatusCode, truncatePushSnippet(string(rawBody), 240))
		s.recordFetchStatus(ctx, 0, msg)
		return &ProxyFetchResult{OK: false, FetchedURL: targetURL, DryRun: dryRun, Error: msg}, errors.New(msg)
	}

	parsed := ParseFetchedProxies(rawBody, fetchCfg)
	if len(parsed) == 0 {
		msg := fmt.Sprintf("未能从远程响应中解析出有效代理 IP（片段：%s）", truncatePushSnippet(string(rawBody), 200))
		s.recordFetchStatus(ctx, 0, msg)
		return &ProxyFetchResult{OK: false, FetchedURL: targetURL, DryRun: dryRun, Error: msg}, errors.New(msg)
	}

	samples := make([]string, 0, minInt(len(parsed), 10))
	for i := 0; i < len(parsed) && i < 10; i++ {
		samples = append(samples, MaskProxyURL(parsed[i].URL))
	}

	if dryRun {
		s.mu.Lock()
		totalSize := len(s.cfg.Entries)
		s.mu.Unlock()
		return &ProxyFetchResult{
			OK:            true,
			FetchedURL:    targetURL,
			ParsedCount:   len(parsed),
			AddedCount:    len(parsed),
			TotalPoolSize: totalSize,
			DryRun:        true,
			SampleProxies: samples,
		}, nil
	}

	s.mu.Lock()
	_ = s.ensureLoadedLocked(ctx)
	added, updated := mergeEntriesIntoConfig(&s.cfg, parsed, ProxySourceRemoteFetch, s.cfg.Fetch.ReplaceOnFetch)
	s.cfg.Fetch.LastFetchedAt = time.Now().UTC().Format(time.RFC3339)
	s.cfg.Fetch.LastFetchCount = len(parsed)
	s.cfg.Fetch.LastFetchError = ""
	totalSize := len(s.cfg.Entries)
	_ = s.persistLocked(ctx)
	s.mu.Unlock()

	return &ProxyFetchResult{
		OK:            true,
		FetchedURL:    targetURL,
		ParsedCount:   len(parsed),
		AddedCount:    added,
		UpdatedCount:  updated,
		TotalPoolSize: totalSize,
		DryRun:        false,
		SampleProxies: samples,
	}, nil
}

func (s *ProxyPoolService) recordFetchStatus(ctx context.Context, count int, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		return
	}
	s.cfg.Fetch.LastFetchedAt = time.Now().UTC().Format(time.RFC3339)
	s.cfg.Fetch.LastFetchCount = count
	s.cfg.Fetch.LastFetchError = strings.TrimSpace(errMsg)
	_ = s.persistLocked(ctx)
}

// AllocateForPush 按「请求显式指定 -> Key 专属策略 -> 全局 IP 池轮询/最少占用/独占/自动远程拉取」的优先级为账号分配代理 IP。
func (s *ProxyPoolService) AllocateForPush(ctx context.Context, req ProxyAllocationRequest) (ProxyAllocationResult, error) {
	// 0. 读取 Key 策略与全局默认协议
	var keyPolicy AdminKeyProxyPolicy
	hasKeyPolicy := false
	defScheme := "http"
	defUser := ""
	defPass := ""

	if s != nil {
		s.mu.Lock()
		_ = s.ensureLoadedLocked(ctx)
		defScheme = nonEmptyStr(s.cfg.Fetch.DefaultScheme, "http")
		defUser = s.cfg.Fetch.Username
		defPass = s.cfg.Fetch.Password
		if req.AdminKeyID > 0 {
			if kp, ok := s.cfg.KeyPolicies[req.AdminKeyID]; ok {
				keyPolicy = normalizeAdminKeyProxyPolicy(kp)
				hasKeyPolicy = true
			}
		}
		s.mu.Unlock()
	}

	allowReqOverride := true
	if hasKeyPolicy && keyPolicy.AllowOverrideInPush != nil {
		allowReqOverride = *keyPolicy.AllowOverrideInPush
	}

	reqMode := strings.ToLower(strings.TrimSpace(req.RequestMode))

	// 1. 请求显式传参优先级最高（前提：该 Key 允许请求体覆盖）
	if allowReqOverride {
		if reqMode == "direct" || reqMode == "none" {
			return ProxyAllocationResult{Source: "direct"}, nil
		}
		if raw := strings.TrimSpace(req.ItemProxyRaw); raw != "" {
			norm, err := NormalizeProxyAddress(raw, defScheme, defUser, defPass)
			if err != nil {
				return ProxyAllocationResult{}, fmt.Errorf("账号代理地址不合法 (%q): %w", raw, err)
			}
			return ProxyAllocationResult{ProxyURL: norm, MaskedProxyURL: MaskProxyURL(norm), Source: "request"}, nil
		}
		if len(req.BatchProxyList) > 0 {
			pick := strings.TrimSpace(req.BatchProxyList[req.BatchIndex%len(req.BatchProxyList)])
			if pick != "" {
				norm, err := NormalizeProxyAddress(pick, defScheme, defUser, defPass)
				if err != nil {
					return ProxyAllocationResult{}, fmt.Errorf("批量代理列表 proxy_list[%d] 不合法 (%q): %w", req.BatchIndex%len(req.BatchProxyList), pick, err)
				}
				return ProxyAllocationResult{ProxyURL: norm, MaskedProxyURL: MaskProxyURL(norm), Source: "request_list"}, nil
			}
		}
		if raw := strings.TrimSpace(req.BatchProxyRaw); raw != "" {
			norm, err := NormalizeProxyAddress(raw, defScheme, defUser, defPass)
			if err != nil {
				return ProxyAllocationResult{}, fmt.Errorf("批量默认代理地址不合法 (%q): %w", raw, err)
			}
			return ProxyAllocationResult{ProxyURL: norm, MaskedProxyURL: MaskProxyURL(norm), Source: "request_default"}, nil
		}
	}

	// 如果是幂等更新已有账号，且请求与 Key 均未要求强制切换新 IP，优先保留原代理
	if strings.TrimSpace(req.ExistingProxyURL) != "" && reqMode == "" && (!hasKeyPolicy || keyPolicy.Mode == KeyProxyModeInherit) {
		return ProxyAllocationResult{
			ProxyURL:       req.ExistingProxyURL,
			MaskedProxyURL: MaskProxyURL(req.ExistingProxyURL),
			Source:         "existing",
		}, nil
	}

	// 2. 合并请求级模式与 Key 级策略
	effectiveMode := reqMode
	effectiveTag := strings.TrimSpace(req.RequestTag)
	effectiveStrategy := strings.ToLower(strings.TrimSpace(req.RequestStrategy))
	effectiveFetchURL := strings.TrimSpace(req.FetchURLOverride)

	if hasKeyPolicy {
		if effectiveMode == "" || !allowReqOverride {
			if keyPolicy.Mode != "" && keyPolicy.Mode != KeyProxyModeInherit {
				effectiveMode = keyPolicy.Mode
			}
		}
		if effectiveTag == "" || !allowReqOverride {
			effectiveTag = strings.TrimSpace(keyPolicy.PoolTag)
		}
		if effectiveStrategy == "" || effectiveStrategy == "inherit" || !allowReqOverride {
			if keyPolicy.Strategy != "" && keyPolicy.Strategy != "inherit" {
				effectiveStrategy = keyPolicy.Strategy
			}
		}
		if effectiveFetchURL == "" || !allowReqOverride {
			effectiveFetchURL = strings.TrimSpace(keyPolicy.FetchURLOverride)
		}
	}

	if effectiveMode == KeyProxyModeDirect {
		return ProxyAllocationResult{Source: "key_direct"}, nil
	}
	if effectiveMode == KeyProxyModeFixed {
		fixedRaw := ""
		if hasKeyPolicy {
			fixedRaw = strings.TrimSpace(keyPolicy.FixedProxyURL)
		}
		if fixedRaw != "" {
			norm, err := NormalizeProxyAddress(fixedRaw, defScheme, defUser, defPass)
			if err != nil {
				return ProxyAllocationResult{}, fmt.Errorf("Key 固定代理地址不合法: %w", err)
			}
			return ProxyAllocationResult{ProxyURL: norm, MaskedProxyURL: MaskProxyURL(norm), Source: "key_fixed"}, nil
		}
	}

	if s == nil {
		return ProxyAllocationResult{Source: "direct"}, nil
	}

	// 3. 实时远程拉取模式（mode=fetch）
	if effectiveMode == KeyProxyModeFetch {
		res, fetchErr := s.FetchFromRemote(ctx, effectiveFetchURL, false)
		if fetchErr != nil {
			if req.AllowEmptyOnError {
				return ProxyAllocationResult{Source: "direct", Warning: "实时拉取代理失败，已回退直连: " + fetchErr.Error()}, nil
			}
			return ProxyAllocationResult{}, fmt.Errorf("实时拉取远程代理失败: %w", fetchErr)
		}
		_ = res
		alloc, allocErr := s.pickFromPool(ctx, true, req.Platform, effectiveTag, effectiveStrategy, effectiveFetchURL, req.InFlightBindings, false)
		if allocErr != nil {
			if req.AllowEmptyOnError {
				return ProxyAllocationResult{Source: "direct", Warning: allocErr.Error()}, nil
			}
			return ProxyAllocationResult{}, allocErr
		}
		alloc.Source = "remote_fetch"
		return alloc, nil
	}

	// 4. IP 池分配（forcePool 当显式指定 mode=pool 或 keyPolicy.Mode=pool 时即使全局开关关闭也可生效）
	forcePool := effectiveMode == KeyProxyModePool
	alloc, err := s.pickFromPool(ctx, forcePool, req.Platform, effectiveTag, effectiveStrategy, effectiveFetchURL, req.InFlightBindings, true)
	if err != nil {
		if req.AllowEmptyOnError {
			return ProxyAllocationResult{Source: "direct", Warning: err.Error()}, nil
		}
		return ProxyAllocationResult{}, err
	}
	if hasKeyPolicy && keyPolicy.Mode == KeyProxyModePool && strings.HasPrefix(alloc.Source, "pool_") {
		alloc.Source = "key_" + alloc.Source
	}
	return alloc, nil
}

func (s *ProxyPoolService) pickFromPool(
	ctx context.Context,
	forcePool bool,
	platform, tag, strategyOverride, fetchURLOverride string,
	inFlight map[string]int,
	allowAutoFetch bool,
) (ProxyAllocationResult, error) {
	// 查询当前数据库中每个代理 URL 已绑定的活跃账号数
	boundCounts := s.countBoundAccountsByProxy(ctx)
	for u, c := range inFlight {
		boundCounts[u] += c
	}

	s.mu.Lock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		s.mu.Unlock()
		return ProxyAllocationResult{}, err
	}

	if !forcePool {
		if !s.cfg.Enabled {
			s.mu.Unlock()
			return ProxyAllocationResult{Source: "direct"}, nil
		}
		if platform != "" && len(s.cfg.ApplyPlatforms) > 0 && !containsStringFold(s.cfg.ApplyPlatforms, platform) {
			s.mu.Unlock()
			return ProxyAllocationResult{Source: "direct"}, nil
		}
	}

	strategy := normalizeProxyStrategy(strategyOverride)
	if strings.TrimSpace(strategyOverride) == "" || strings.EqualFold(strings.TrimSpace(strategyOverride), "inherit") {
		strategy = normalizeProxyStrategy(s.cfg.Strategy)
	}
	maxPerIP := s.cfg.MaxAccountsPerIP
	autoFetchEnabled := allowAutoFetch && s.cfg.Fetch.AutoFetchOnEmpty && (s.cfg.Fetch.Enabled || strings.TrimSpace(fetchURLOverride) != "") && (strings.TrimSpace(fetchURLOverride) != "" || strings.TrimSpace(s.cfg.Fetch.URL) != "")

	prevCursor := s.cfg.Cursor
	chosenIdx, needFetch := selectEntryIndexLocked(s.cfg.Entries, &s.cfg.Cursor, tag, strategy, maxPerIP, boundCounts)
	if chosenIdx < 0 || (needFetch && autoFetchEnabled) {
		if autoFetchEnabled {
			s.cfg.Cursor = prevCursor
			s.mu.Unlock()
			if _, fErr := s.FetchFromRemote(ctx, fetchURLOverride, false); fErr == nil {
				// 拉取完成后重新选池（不再二次递归拉取）
				res, retryErr := s.pickFromPool(ctx, forcePool, platform, tag, strategyOverride, fetchURLOverride, inFlight, false)
				if retryErr == nil && res.ProxyURL != "" {
					res.Source = "pool_remote_fetch"
					return res, nil
				}
			}
			// 若自动拉取失败但此前仍有可用回退项（例如 sticky_unique 允许回退），再次尝试不触发拉取
			return s.pickFromPool(ctx, forcePool, platform, tag, strategyOverride, fetchURLOverride, inFlight, false)
		}
		if chosenIdx < 0 {
			s.mu.Unlock()
			if forcePool {
				return ProxyAllocationResult{}, errors.New("代理 IP 池暂无满足条件的可用代理（请添加代理、调整每 IP 上限或配置远程拉取源）")
			}
			return ProxyAllocationResult{Source: "direct", Warning: "代理 IP 池暂无可用 IP，已回退直连"}, nil
		}
	}

	entry := &s.cfg.Entries[chosenIdx]
	entry.LastUsedAt = time.Now().UTC().Format(time.RFC3339)
	chosen := *entry
	_ = s.persistLocked(ctx)
	s.mu.Unlock()

	if inFlight != nil {
		inFlight[chosen.URL]++
	}
	return ProxyAllocationResult{
		ProxyURL:       chosen.URL,
		MaskedProxyURL: MaskProxyURL(chosen.URL),
		Source:         "pool_" + strategy,
		Tag:            chosen.Tag,
		EntryID:        chosen.ID,
	}, nil
}

// selectEntryIndexLocked 在持锁状态下根据策略挑选池内条目索引。
// 当策略为 sticky_unique 且无 0 占用独占 IP 时，needFetch 返回 true 以便优先触发自动拉取。
func selectEntryIndexLocked(
	entries []ProxyPoolEntry,
	cursor *int,
	tag, strategy string,
	maxPerIP int,
	boundCounts map[string]int,
) (idx int, needFetch bool) {
	tagLower := strings.ToLower(strings.TrimSpace(tag))
	candidates := make([]int, 0, len(entries))
	for i, e := range entries {
		if !e.Enabled || strings.TrimSpace(e.URL) == "" {
			continue
		}
		if tagLower != "" && strings.ToLower(strings.TrimSpace(e.Tag)) != tagLower {
			continue
		}
		if maxPerIP > 0 && boundCounts[e.URL] >= maxPerIP {
			continue
		}
		candidates = append(candidates, i)
	}
	if len(candidates) == 0 {
		return -1, true
	}

	switch strategy {
	case ProxyStrategyLeastUsed:
		best := candidates[0]
		bestCount := boundCounts[entries[best].URL]
		for _, ci := range candidates[1:] {
			c := boundCounts[entries[ci].URL]
			if c < bestCount {
				best = ci
				bestCount = c
			}
		}
		return best, false

	case ProxyStrategyStickyUnique:
		for _, ci := range candidates {
			if boundCounts[entries[ci].URL] == 0 {
				return ci, false
			}
		}
		// 没有未绑定的独占 IP：返回最少占用的索引作为保底，同时标记 needFetch=true 优先尝试拉新 IP
		best := candidates[0]
		bestCount := boundCounts[entries[best].URL]
		for _, ci := range candidates[1:] {
			c := boundCounts[entries[ci].URL]
			if c < bestCount {
				best = ci
				bestCount = c
			}
		}
		return best, true

	case ProxyStrategyRandom:
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
		if err != nil {
			return candidates[0], false
		}
		return candidates[int(n.Int64())], false

	default: // round_robin
		pos := *cursor
		if pos < 0 {
			pos = 0
		}
		pick := candidates[pos%len(candidates)]
		*cursor = (pos + 1) % len(candidates)
		return pick, false
	}
}

// ProbeEntries 探测单条（按 id 或 rawProxyURL）或批量池内代理的连通性与真实出口 IP，并更新池状态。
func (s *ProxyPoolService) ProbeEntries(ctx context.Context, entryID, rawProxyURL string, limit int) ([]ProxyProbeResult, error) {
	if s == nil {
		return nil, errors.New("代理池服务未初始化")
	}
	if strings.TrimSpace(rawProxyURL) != "" {
		norm, err := NormalizeProxyAddress(rawProxyURL, "http", "", "")
		if err != nil {
			return nil, fmt.Errorf("代理地址格式不合法: %w", err)
		}
		pr := probeSingleProxyURL(ctx, "", norm)
		return []ProxyProbeResult{pr}, nil
	}

	s.mu.Lock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	var targets []ProxyPoolEntry
	for _, e := range s.cfg.Entries {
		if strings.TrimSpace(entryID) != "" {
			if e.ID == strings.TrimSpace(entryID) {
				targets = append(targets, e)
				break
			}
			continue
		}
		if e.Enabled {
			targets = append(targets, e)
		}
	}
	s.mu.Unlock()

	if len(targets) == 0 {
		return []ProxyProbeResult{}, nil
	}
	if limit <= 0 || limit > 30 {
		limit = 20
	}
	if len(targets) > limit {
		targets = targets[:limit]
	}

	results := make([]ProxyProbeResult, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for i, target := range targets {
		wg.Add(1)
		go func(idx int, entry ProxyPoolEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = probeSingleProxyURL(ctx, entry.ID, entry.URL)
		}(i, target)
	}
	wg.Wait()

	// 回写探测结果到持久化池
	byID := make(map[string]ProxyProbeResult, len(results))
	for _, r := range results {
		if r.ID != "" {
			byID[r.ID] = r
		}
	}
	s.mu.Lock()
	for i := range s.cfg.Entries {
		if r, ok := byID[s.cfg.Entries[i].ID]; ok {
			okVal := r.OK
			s.cfg.Entries[i].LastCheckOK = &okVal
			s.cfg.Entries[i].LastExitIP = r.ExitIP
			s.cfg.Entries[i].LastLatencyMS = r.LatencyMS
			s.cfg.Entries[i].LastError = r.Error
		}
	}
	_ = s.persistLocked(ctx)
	s.mu.Unlock()

	return results, nil
}

func probeSingleProxyURL(ctx context.Context, id, proxyURL string) ProxyProbeResult {
	started := time.Now()
	out := ProxyProbeResult{
		ID:        id,
		MaskedURL: MaskProxyURL(proxyURL),
	}
	probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	res := workbuddy.TestProxy(probeCtx, proxyURL)
	if res == nil {
		out.LatencyMS = time.Since(started).Milliseconds()
		out.Error = "代理连通性探测失败"
		return out
	}
	out.OK = res.OK
	out.ExitIP = strings.TrimSpace(res.ExitIP)
	out.LatencyMS = res.LatencyMs
	if out.LatencyMS <= 0 {
		out.LatencyMS = time.Since(started).Milliseconds()
	}
	out.Error = strings.TrimSpace(res.Error)
	return out
}

func (s *ProxyPoolService) countBoundAccountsByProxy(ctx context.Context) map[string]int {
	counts := make(map[string]int)
	if s == nil || s.db == nil {
		return counts
	}
	rows, err := s.db.Account.Query().
		Where(account.DeletedAtIsNil()).
		Select(account.FieldProxyURL).
		All(ctx)
	if err != nil {
		return counts
	}
	for _, r := range rows {
		raw := strings.TrimSpace(r.ProxyURL)
		if raw == "" {
			continue
		}
		if norm, err := NormalizeProxyAddress(raw, "http", "", ""); err == nil {
			counts[norm]++
		} else {
			counts[raw]++
		}
	}
	return counts
}

func (s *ProxyPoolService) decorateView(ctx context.Context, snap ProxyPoolConfig) *ProxyPoolView {
	bound := s.countBoundAccountsByProxy(ctx)
	tagSet := make(map[string]struct{})
	stats := ProxyPoolStats{TotalEntries: len(snap.Entries)}
	for i := range snap.Entries {
		snap.Entries[i].MaskedURL = MaskProxyURL(snap.Entries[i].URL)
		c := bound[snap.Entries[i].URL]
		snap.Entries[i].URL = snap.Entries[i].MaskedURL
		snap.Entries[i].BoundAccounts = c
		stats.BoundTotal += c
		if snap.Entries[i].Enabled {
			stats.EnabledEntries++
		}
		if snap.Entries[i].Source == ProxySourceRemoteFetch {
			stats.FetchedEntries++
		} else {
			stats.ManualEntries++
		}
		if t := strings.TrimSpace(snap.Entries[i].Tag); t != "" {
			tagSet[t] = struct{}{}
		}
	}
	if snap.Fetch.Password != "" {
		snap.Fetch.Password = "***"
	}
	for k, kp := range snap.KeyPolicies {
		if kp.FixedProxyURL != "" {
			kp.FixedProxyMasked = MaskProxyURL(kp.FixedProxyURL)
			kp.FixedProxyURL = kp.FixedProxyMasked
			snap.KeyPolicies[k] = kp
		}
	}
	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	stats.Tags = tags
	return &ProxyPoolView{Config: snap, Stats: stats}
}

// NormalizeProxyAddress 将各种代理写法规范化为标准代理 URL：
//   - http(s)://[user:pass@]host:port
//   - socks5(h)://[user:pass@]host:port
//   - user:pass@host:port
//   - host:port:user:pass（常见代理商提取格式）
//   - host:port
func NormalizeProxyAddress(raw, defaultScheme, defaultUser, defaultPass string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("代理地址为空")
	}
	scheme := strings.ToLower(strings.TrimSpace(defaultScheme))
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		scheme = "http"
	}

	if !strings.Contains(s, "://") {
		if strings.Contains(s, "@") {
			s = scheme + "://" + s
		} else {
			parts := strings.Split(s, ":")
			switch len(parts) {
			case 2:
				// host:port
				host := strings.TrimSpace(parts[0])
				port := strings.TrimSpace(parts[1])
				if host == "" || !isValidPortString(port) {
					return "", fmt.Errorf("无效的主机或端口 %q", raw)
				}
				if defaultUser != "" {
					u := &url.URL{
						Scheme: scheme,
						User:   url.UserPassword(defaultUser, defaultPass),
						Host:   net.JoinHostPort(host, port),
					}
					return u.String(), nil
				}
				s = scheme + "://" + net.JoinHostPort(host, port)
			case 4:
				// host:port:user:pass
				host := strings.TrimSpace(parts[0])
				port := strings.TrimSpace(parts[1])
				user := strings.TrimSpace(parts[2])
				pass := strings.TrimSpace(parts[3])
				if host == "" || !isValidPortString(port) {
					return "", fmt.Errorf("无效的 host:port:user:pass 格式 %q", raw)
				}
				u := &url.URL{
					Scheme: scheme,
					User:   url.UserPassword(user, pass),
					Host:   net.JoinHostPort(host, port),
				}
				return u.String(), nil
			default:
				// 尝试 IPv6 [::1]:8080 形式
				if strings.HasPrefix(s, "[") {
					s = scheme + "://" + s
				} else {
					return "", fmt.Errorf("无法识别的代理格式 %q", raw)
				}
			}
		}
	}

	parsed, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
		parsed.Scheme = strings.ToLower(parsed.Scheme)
	default:
		return "", fmt.Errorf("不支持的代理协议 %q（仅支持 http/https/socks5/socks5h）", parsed.Scheme)
	}
	if parsed.Hostname() == "" || parsed.Port() == "" || !isValidPortString(parsed.Port()) {
		return "", fmt.Errorf("代理地址缺少有效主机或端口 %q", raw)
	}
	if parsed.User == nil && defaultUser != "" {
		parsed.User = url.UserPassword(defaultUser, defaultPass)
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// MaskProxyURL 隐藏代理 URL 中的认证密码（若存在），确保响应体与日志安全不泄露代理密码。
func MaskProxyURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	parsed, err := url.Parse(s)
	if err != nil || parsed.User == nil {
		return s
	}
	user := parsed.User.Username()
	if _, hasPass := parsed.User.Password(); hasPass {
		if user != "" {
			return fmt.Sprintf("%s://%s:***@%s", parsed.Scheme, user, parsed.Host)
		}
		return fmt.Sprintf("%s://***@%s", parsed.Scheme, parsed.Host)
	}
	return parsed.String()
}

// ParseProxyLines 从多行/逗号分隔文本解析代理条目（支持行尾 `#tag` 标注地区/分组标签）。
func ParseProxyLines(raw, defaultScheme, defaultUser, defaultPass, defaultTag string) []ProxyPoolEntry {
	var out []ProxyPoolEntry
	seen := make(map[string]struct{})
	now := time.Now().UTC().Format(time.RFC3339)

	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		tag := strings.TrimSpace(defaultTag)
		// 支持行尾 #tag 标注，例如: http://1.2.3.4:8080 #us
		if idx := strings.Index(line, " #"); idx > 0 {
			inlineTag := strings.TrimSpace(line[idx+2:])
			if inlineTag != "" {
				tag = inlineTag
			}
			line = strings.TrimSpace(line[:idx])
		}
		// 也支持按逗号拆多条（仅当不含空格且拆分后都是有效地址）
		parts := []string{line}
		if strings.Contains(line, ",") {
			cand := strings.Split(line, ",")
			// 判断是 "proxy,tag" 还是 "proxy1,proxy2"
			if len(cand) == 2 {
				if _, err2 := NormalizeProxyAddress(cand[1], defaultScheme, defaultUser, defaultPass); err2 != nil {
					parts = []string{strings.TrimSpace(cand[0])}
					if t := strings.TrimSpace(cand[1]); t != "" {
						tag = t
					}
				} else {
					parts = cand
				}
			} else {
				parts = cand
			}
		}
		for _, p := range parts {
			norm, err := NormalizeProxyAddress(p, defaultScheme, defaultUser, defaultPass)
			if err != nil {
				continue
			}
			if _, dup := seen[norm]; dup {
				continue
			}
			seen[norm] = struct{}{}
			out = append(out, ProxyPoolEntry{
				ID:      proxyEntryID(norm),
				URL:     norm,
				Tag:     tag,
				Enabled: true,
				Source:  ProxySourceManual,
				AddedAt: now,
			})
		}
	}
	return out
}

// ParseFetchedProxies 智能解析远程代理商接口返回（兼容纯文本列表、JSON 字符串数组、JSON 对象数组与指定 json_path）。
func ParseFetchedProxies(raw []byte, cfg ProxyFetchConfig) []ProxyPoolEntry {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	scheme := nonEmptyStr(cfg.DefaultScheme, "http")
	tag := strings.TrimSpace(cfg.DefaultTag)

	// 优先尝试 JSON 解析
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var root any
		if err := json.Unmarshal([]byte(trimmed), &root); err == nil {
			target := root
			if strings.TrimSpace(cfg.JSONPath) != "" {
				if nav, ok := navigateJSONPath(root, cfg.JSONPath); ok {
					target = nav
				}
			}
			var rawEntries []ProxyPoolEntry
			seen := make(map[string]struct{})
			extractProxiesFromJSON(target, scheme, cfg.Username, cfg.Password, tag, seen, &rawEntries)
			if len(rawEntries) > 0 {
				return rawEntries
			}
		}
	}

	// 回退纯文本按行解析
	entries := ParseProxyLines(trimmed, scheme, cfg.Username, cfg.Password, tag)
	for i := range entries {
		entries[i].Source = ProxySourceRemoteFetch
	}
	return entries
}

func navigateJSONPath(root any, path string) (any, bool) {
	cur := root
	for _, seg := range strings.Split(strings.TrimSpace(path), ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, exists := m[seg]
		if !exists {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func extractProxiesFromJSON(
	node any,
	defaultScheme, defaultUser, defaultPass, defaultTag string,
	seen map[string]struct{},
	out *[]ProxyPoolEntry,
) {
	now := time.Now().UTC().Format(time.RFC3339)
	addNorm := func(raw, scheme, user, pass, tag string) {
		norm, err := NormalizeProxyAddress(raw, nonEmptyStr(scheme, defaultScheme), nonEmptyStr(user, defaultUser), nonEmptyStr(pass, defaultPass))
		if err != nil {
			return
		}
		if _, dup := seen[norm]; dup {
			return
		}
		seen[norm] = struct{}{}
		*out = append(*out, ProxyPoolEntry{
			ID:      proxyEntryID(norm),
			URL:     norm,
			Tag:     nonEmptyStr(tag, defaultTag),
			Enabled: true,
			Source:  ProxySourceRemoteFetch,
			AddedAt: now,
		})
	}

	switch v := node.(type) {
	case string:
		for _, e := range ParseProxyLines(v, defaultScheme, defaultUser, defaultPass, defaultTag) {
			addNorm(e.URL, defaultScheme, defaultUser, defaultPass, nonEmptyStr(e.Tag, defaultTag))
		}
	case []any:
		for _, item := range v {
			extractProxiesFromJSON(item, defaultScheme, defaultUser, defaultPass, defaultTag, seen, out)
		}
	case map[string]any:
		// 1. 直接匹配常见单条代理对象结构: {ip, port, user, pass, protocol/scheme, tag/region/country}
		host := mapStringField(v, "ip", "host", "server", "addr", "address")
		port := mapAnyPortField(v, "port", "proxy_port")
		scheme := mapStringField(v, "scheme", "protocol", "type")
		user := mapStringField(v, "username", "user", "account")
		pass := mapStringField(v, "password", "pass", "pwd")
		tag := mapStringField(v, "tag", "region", "country", "area", "city")

		if direct := mapStringField(v, "proxy_url", "proxy", "url"); direct != "" {
			addNorm(direct, scheme, user, pass, tag)
		} else if host != "" && port != "" {
			addNorm(net.JoinHostPort(host, port), scheme, user, pass, tag)
		}

		// 2. 递归扫描常见容器字段（如 data / list / proxies / items / result）
		for _, k := range []string{"data", "list", "proxies", "items", "result", "results", "ips", "nodes"} {
			if sub, ok := v[k]; ok && sub != nil {
				extractProxiesFromJSON(sub, defaultScheme, defaultUser, defaultPass, defaultTag, seen, out)
			}
		}
	}
}

func mapAnyPortField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		switch t := val.(type) {
		case string:
			if isValidPortString(strings.TrimSpace(t)) {
				return strings.TrimSpace(t)
			}
		case float64:
			p := int(t)
			if p > 0 && p <= 65535 {
				return strconv.Itoa(p)
			}
		case int:
			if t > 0 && t <= 65535 {
				return strconv.Itoa(t)
			}
		}
	}
	return ""
}

func mergeEntriesIntoConfig(cfg *ProxyPoolConfig, incoming []ProxyPoolEntry, source string, replaceSource bool) (added int, updated int) {
	now := time.Now().UTC().Format(time.RFC3339)
	existing := make([]ProxyPoolEntry, 0, len(cfg.Entries))
	byURL := make(map[string]int, len(cfg.Entries))

	for _, e := range cfg.Entries {
		if replaceSource && e.Source == source {
			continue
		}
		byURL[e.URL] = len(existing)
		existing = append(existing, e)
	}

	for _, item := range incoming {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		item.Source = source
		if item.ID == "" {
			item.ID = proxyEntryID(item.URL)
		}
		if item.AddedAt == "" {
			item.AddedAt = now
		}
		if pos, ok := byURL[item.URL]; ok {
			if item.Tag != "" {
				existing[pos].Tag = item.Tag
			}
			if item.Note != "" {
				existing[pos].Note = item.Note
			}
			existing[pos].Enabled = true
			updated++
		} else {
			byURL[item.URL] = len(existing)
			existing = append(existing, item)
			added++
		}
	}
	cfg.Entries = existing
	return added, updated
}

func normalizeProxyPoolConfig(in ProxyPoolConfig) ProxyPoolConfig {
	out := in
	out.Strategy = normalizeProxyStrategy(in.Strategy)
	if out.MaxAccountsPerIP < 0 {
		out.MaxAccountsPerIP = 0
	}
	out.Fetch.Method = strings.ToUpper(strings.TrimSpace(in.Fetch.Method))
	if out.Fetch.Method != http.MethodPost {
		out.Fetch.Method = http.MethodGet
	}
	out.Fetch.DefaultScheme = strings.ToLower(strings.TrimSpace(in.Fetch.DefaultScheme))
	switch out.Fetch.DefaultScheme {
	case "http", "https", "socks5", "socks5h":
	default:
		out.Fetch.DefaultScheme = "http"
	}
	out.Fetch.URL = strings.TrimSpace(in.Fetch.URL)
	out.Fetch.DefaultTag = strings.TrimSpace(in.Fetch.DefaultTag)
	out.Fetch.JSONPath = strings.TrimSpace(in.Fetch.JSONPath)
	if out.Fetch.IntervalMinutes < 0 {
		out.Fetch.IntervalMinutes = 0
	}
	out.Fetch.TimeoutSeconds = clampInt(out.Fetch.TimeoutSeconds, 3, 60)

	// 规范化并去重 Entries
	cleaned := make([]ProxyPoolEntry, 0, len(in.Entries))
	seen := make(map[string]struct{}, len(in.Entries))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range in.Entries {
		norm, err := NormalizeProxyAddress(e.URL, out.Fetch.DefaultScheme, out.Fetch.Username, out.Fetch.Password)
		if err != nil {
			continue
		}
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		e.URL = norm
		e.ID = proxyEntryID(norm)
		e.Tag = strings.TrimSpace(e.Tag)
		e.Note = strings.TrimSpace(e.Note)
		if e.Source != ProxySourceRemoteFetch {
			e.Source = ProxySourceManual
		}
		if e.AddedAt == "" {
			e.AddedAt = now
		}
		cleaned = append(cleaned, e)
	}
	out.Entries = cleaned

	if out.KeyPolicies == nil {
		out.KeyPolicies = make(map[int64]AdminKeyProxyPolicy)
	} else {
		normKP := make(map[int64]AdminKeyProxyPolicy, len(out.KeyPolicies))
		for id, kp := range out.KeyPolicies {
			if id > 0 {
				normKP[id] = normalizeAdminKeyProxyPolicy(kp)
			}
		}
		out.KeyPolicies = normKP
	}
	return out
}

func defaultAdminKeyProxyPolicy() AdminKeyProxyPolicy {
	allow := true
	return AdminKeyProxyPolicy{
		Mode:                KeyProxyModeInherit,
		Strategy:            "inherit",
		AllowOverrideInPush: &allow,
	}
}

func normalizeAdminKeyProxyPolicy(in AdminKeyProxyPolicy) AdminKeyProxyPolicy {
	out := in
	switch strings.ToLower(strings.TrimSpace(in.Mode)) {
	case KeyProxyModePool, KeyProxyModeFixed, KeyProxyModeFetch, KeyProxyModeDirect:
		out.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
	default:
		out.Mode = KeyProxyModeInherit
	}
	out.FixedProxyURL = strings.TrimSpace(in.FixedProxyURL)
	out.PoolTag = strings.TrimSpace(in.PoolTag)
	out.FetchURLOverride = strings.TrimSpace(in.FetchURLOverride)
	st := strings.ToLower(strings.TrimSpace(in.Strategy))
	switch st {
	case ProxyStrategyRoundRobin, ProxyStrategyLeastUsed, ProxyStrategyStickyUnique, ProxyStrategyRandom:
		out.Strategy = st
	default:
		out.Strategy = "inherit"
	}
	if out.AllowOverrideInPush == nil {
		allow := true
		out.AllowOverrideInPush = &allow
	}
	return out
}

func normalizeProxyStrategy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ProxyStrategyLeastUsed, "least":
		return ProxyStrategyLeastUsed
	case ProxyStrategyStickyUnique, "unique", "exclusive":
		return ProxyStrategyStickyUnique
	case ProxyStrategyRandom, "rand":
		return ProxyStrategyRandom
	default:
		return ProxyStrategyRoundRobin
	}
}

func proxyEntryID(normURL string) string {
	sum := sha256.Sum256([]byte(normURL))
	return "px_" + hex.EncodeToString(sum[:6])
}

func isValidPortString(port string) bool {
	p, err := strconv.Atoi(port)
	return err == nil && p > 0 && p <= 65535
}

func cloneProxyPoolConfig(in ProxyPoolConfig) ProxyPoolConfig {
	out := in
	if in.ApplyPlatforms != nil {
		out.ApplyPlatforms = append([]string(nil), in.ApplyPlatforms...)
	}
	out.Entries = make([]ProxyPoolEntry, len(in.Entries))
	copy(out.Entries, in.Entries)
	if in.Fetch.Headers != nil {
		out.Fetch.Headers = make(map[string]string, len(in.Fetch.Headers))
		for k, v := range in.Fetch.Headers {
			out.Fetch.Headers[k] = v
		}
	}
	out.KeyPolicies = cloneKeyPolicies(in.KeyPolicies)
	return out
}

func cloneKeyPolicies(in map[int64]AdminKeyProxyPolicy) map[int64]AdminKeyProxyPolicy {
	out := make(map[int64]AdminKeyProxyPolicy, len(in))
	for k, v := range in {
		cp := v
		if v.AllowOverrideInPush != nil {
			b := *v.AllowOverrideInPush
			cp.AllowOverrideInPush = &b
		}
		out[k] = cp
	}
	return out
}

func containsStringFold(list []string, target string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func clampInt(val, minV, maxV int) int {
	if val < minV {
		return minV
	}
	if val > maxV {
		return maxV
	}
	return val
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func nonEmptyStr(val, fallback string) string {
	if strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return fallback
}

func truncatePushSnippet(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
