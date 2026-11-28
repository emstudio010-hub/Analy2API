// Package scheduler 负责从分组的上游账号池里挑一个账号来承载请求，
// 并维护账号的调度状态（限流、过载、连续失败、自动禁用）。
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/group"
	"cmd2api/ent/predicate"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/crypto"
	"cmd2api/internal/domain"
	"cmd2api/internal/workbuddy"

	"entgo.io/ent/dialect/sql"
)

// ErrNoAccountAvailable 表示分组里当前没有可用账号。
var ErrNoAccountAvailable = errors.New("分组内没有可用账号")

// candidateLimit 是一次调度最多评估多少个候选账号。
//
// 不设上限的话，账号池很大时每个请求都要把整池读出来再逐个试闸门。
// 取前 50 个按优先级排好序的就够——真出现前 50 个全满的情况，
// 说明这个分组本来就该扩容了，让请求快速失败比拖慢更好。
const candidateLimit = 50

// Credentials 是从账号行里解出来的明文凭证与多平台上下文。
type Credentials struct {
	APIKey         string
	AccessToken    string
	RefreshToken   string
	ManagementKey  string
	UID            string
	Nickname       string
	Email          string
	EnterpriseID   string
	EnterpriseName string
	Domain         string
	BaseURL        string
	ProxyURL       string
	Qimei36        string
	MachineID      string
	NodeID         int64
	AuthFile       string
	Fingerprint    antigravity.NodeFingerprint
}

// Lease 是一次账号占用。调用方必须调用 Release，通常写成 defer。
type Lease struct {
	Account     *ent.Account
	Credentials Credentials
	// Platform 是所选账号所属的上游平台，调用方据此选择协议适配器。
	Platform string
	// BaseURL 是上游地址：OpenCode / AntiGravity 为节点或网关地址，WorkBuddy 为规范化域名。
	BaseURL string
	// AccountMode 是 OpenCode 的计费模式（zen/go）；非 OpenCode 账号为空。
	AccountMode    string
	AccessToken    string
	RefreshToken   string
	ManagementKey  string
	UID            string
	Nickname       string
	Email          string
	EnterpriseID   string
	EnterpriseName string
	Domain         string
	ProxyURL       string
	Qimei36        string
	MachineID      string
	NodeID         int64
	AuthFile       string
	Fingerprint    antigravity.NodeFingerprint
	Release        func()
}

// WorkBuddyCreds 构造发送给 WorkBuddy 上游客户端的完整账号凭证，
// 自动补全缺失的设备指纹并规范化域名。
func (l *Lease) WorkBuddyCreds() workbuddy.AccountCreds {
	if l == nil {
		return workbuddy.AccountCreds{}
	}
	var accountID int64
	var name string
	if l.Account != nil {
		accountID = l.Account.ID
		name = l.Account.Name
	}
	qimei, mid := workbuddy.EnsureDevice(
		firstNonEmpty(l.Qimei36, l.Credentials.Qimei36),
		firstNonEmpty(l.MachineID, l.Credentials.MachineID),
	)
	dom := workbuddy.NormalizeDomain(firstNonEmpty(l.Domain, l.Credentials.Domain, l.BaseURL))
	return workbuddy.AccountCreds{
		AccountID:      accountID,
		Name:           name,
		UID:            firstNonEmpty(l.UID, l.Credentials.UID),
		Nickname:       firstNonEmpty(l.Nickname, l.Credentials.Nickname),
		Email:          firstNonEmpty(l.Email, l.Credentials.Email),
		EnterpriseID:   firstNonEmpty(l.EnterpriseID, l.Credentials.EnterpriseID),
		EnterpriseName: firstNonEmpty(l.EnterpriseName, l.Credentials.EnterpriseName),
		Domain:         dom,
		AccessToken:    firstNonEmpty(l.AccessToken, l.Credentials.AccessToken, l.Credentials.APIKey),
		RefreshToken:   firstNonEmpty(l.RefreshToken, l.Credentials.RefreshToken),
		ProxyURL:       firstNonEmpty(l.ProxyURL, l.Credentials.ProxyURL),
		Qimei36:        qimei,
		MachineID:      mid,
	}
}

// AntigravityNodeSpec 构造发送给 AntiGravity 客户端的节点连接规格（附带节点专属隔离指纹）。
func (l *Lease) AntigravityNodeSpec() antigravity.NodeSpec {
	if l == nil {
		return antigravity.NodeSpec{}
	}
	var name string
	if l.Account != nil {
		name = l.Account.Name
	}
	baseURL := firstNonEmpty(l.BaseURL, l.Credentials.BaseURL)
	mode := antigravity.ModeRemoteAPI
	normAccMode := strings.ToLower(strings.TrimSpace(l.AccountMode))
	switch {
	case normAccMode == antigravity.ModeLocalCLI || normAccMode == "agy_cli" || normAccMode == "agy" || normAccMode == "cli" ||
		strings.EqualFold(baseURL, "local_cli") || strings.EqualFold(baseURL, "agy"):
		mode = antigravity.ModeLocalCLI
	case normAccMode == antigravity.ModeDirectCLI || strings.Contains(strings.ToLower(baseURL), "cloudcode-pa.googleapis.com"):
		mode = antigravity.ModeDirectCLI
	case antigravity.IsLoopbackTarget(baseURL):
		mode = antigravity.ModeLocalDocker
	}
	fp := l.Fingerprint
	if fp.FingerprintID == "" {
		fp = l.Credentials.Fingerprint
	}
	var projectID, authDir, cliBin string
	if l.Account != nil {
		if l.Account.Extra != nil {
			if pid, ok := l.Account.Extra["project_id"].(string); ok && strings.TrimSpace(pid) != "" {
				projectID = strings.TrimSpace(pid)
			}
			if ad, ok := l.Account.Extra["auth_dir"].(string); ok && strings.TrimSpace(ad) != "" {
				authDir = strings.TrimSpace(ad)
			}
			if cb, ok := l.Account.Extra["cli_binary"].(string); ok && strings.TrimSpace(cb) != "" {
				cliBin = strings.TrimSpace(cb)
			}
		}
		if projectID == "" {
			projectID = strings.TrimSpace(l.Account.EnterpriseID)
		}
	}
	return antigravity.NodeSpec{
		ID:            l.NodeID,
		Name:          name,
		Mode:          mode,
		HostIP:        baseURL,
		AuthDir:       authDir,
		CLIBinary:     cliBin,
		ManagementKey: firstNonEmpty(l.ManagementKey, l.Credentials.ManagementKey),
		APIKey:        firstNonEmpty(l.Credentials.APIKey, l.AccessToken, l.Credentials.AccessToken),
		ProjectID:     projectID,
		ProxyURL:      firstNonEmpty(l.ProxyURL, l.Credentials.ProxyURL),
		Fingerprint:   fp,
	}
}

type cachedGroupPlatform struct {
	platform  string
	expiresAt time.Time
}

type credCacheKey struct {
	accountID int64
	updatedNs int64
}

// Scheduler 持有调度所需的依赖与热路径内存缓存。
type Scheduler struct {
	client  *ent.Client
	vault   *crypto.Vault
	limiter *limiter
	logger  *slog.Logger

	// failureThreshold 是连续失败多少次后自动禁用账号。
	failureThreshold int

	cacheMu        sync.RWMutex
	groupPlatCache map[int64]cachedGroupPlatform
	credsCache     map[credCacheKey]Credentials
	memLastUsed    map[int64]time.Time
	lastMarkFlush  map[int64]time.Time
}

// New 构造 Scheduler。
func New(client *ent.Client, vault *crypto.Vault, failureThreshold int, logger *slog.Logger) *Scheduler {
	if failureThreshold < 1 {
		if logger != nil {
			logger.Warn("连续失败阈值配置无效，已按 1 处理",
				"configured", failureThreshold, "env", "HEALTH_FAILURE_THRESHOLD")
		}
		failureThreshold = 1
	}
	return &Scheduler{
		client:           client,
		vault:            vault,
		limiter:          newLimiter(),
		logger:           logger,
		failureThreshold: failureThreshold,
		groupPlatCache:   make(map[int64]cachedGroupPlatform),
		credsCache:       make(map[credCacheKey]Credentials),
		memLastUsed:      make(map[int64]time.Time),
		lastMarkFlush:    make(map[int64]time.Time),
	}
}

// Invalidate 清空调度器内存缓存（账号/分组变更时调用）。
func (s *Scheduler) Invalidate() {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	s.groupPlatCache = make(map[int64]cachedGroupPlatform)
	s.credsCache = make(map[credCacheKey]Credentials)
	s.cacheMu.Unlock()
}

// InvalidateGroup 失效指定分组的平台缓存。
func (s *Scheduler) InvalidateGroup(groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}
	s.cacheMu.Lock()
	delete(s.groupPlatCache, groupID)
	s.cacheMu.Unlock()
}

func platformAccountPredicate(platform string) predicate.Account {
	switch domain.NormalizePlatform(platform) {
	case domain.PlatformWorkBuddy:
		// 兼容历史未迁移账号（空平台、legacy aliases 或旧 commandcode），统一视为 workbuddy 分组内可用账号；
		// 仍严格隔离 antigravity 与 opencode 账号。
		return account.PlatformIn(
			"",
			domain.PlatformWorkBuddy,
			domain.PlatformCommandCode,
			"wb",
			"work-buddy",
			"work_buddy",
			"codebuddy",
		)
	case domain.PlatformAntigravity:
		return account.PlatformIn(
			domain.PlatformAntigravity,
			"agy",
			"anti-gravity",
			"anti_gravity",
			"cliproxy",
			"cliproxyapi",
		)
	case domain.PlatformCommandCode:
		return account.PlatformIn(
			domain.PlatformCommandCode,
			"cc",
			"command-code",
			"command_code",
		)
	case domain.PlatformOpenCode:
		return account.PlatformIn(
			domain.PlatformOpenCode,
			"open-code",
			"open_code",
		)
	default:
		return account.PlatformEQ(platform)
	}
}

// GroupPlatform 查询指定分组绑定的规范化平台名称（带 15s 内存缓存）。
func (s *Scheduler) GroupPlatform(ctx context.Context, groupID int64) (string, error) {
	now := time.Now()
	s.cacheMu.RLock()
	if cached, ok := s.groupPlatCache[groupID]; ok && now.Before(cached.expiresAt) {
		s.cacheMu.RUnlock()
		return cached.platform, nil
	}
	s.cacheMu.RUnlock()

	grp, err := s.client.Group.Query().
		Where(group.IDEQ(groupID), group.DeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", fmt.Errorf("分组 %d 不存在或已删除", groupID)
		}
		return "", fmt.Errorf("查询分组: %w", err)
	}
	p := domain.NormalizePlatform(grp.Platform)
	if p == "" {
		p = domain.PlatformWorkBuddy
	}
	s.cacheMu.Lock()
	s.groupPlatCache[groupID] = cachedGroupPlatform{platform: p, expiresAt: now.Add(15 * time.Second)}
	s.cacheMu.Unlock()
	return p, nil
}

// Acquire 为指定分组租一个可用账号。excludeIDs 里的账号会被跳过。
func (s *Scheduler) Acquire(ctx context.Context, groupID int64, excludeIDs ...int64) (*Lease, error) {
	platform, err := s.GroupPlatform(ctx, groupID)
	if err != nil {
		return nil, err
	}
	platformPred := platformAccountPredicate(platform)

	now := time.Now()
	query := s.client.Account.Query().
		Where(
			account.DeletedAtIsNil(),
			account.StatusEQ(domain.StatusActive),
			account.SchedulableEQ(true),
			account.HasGroupsWith(group.IDEQ(groupID)),
			platformPred,
			// 限流未解除的跳过。
			account.Or(
				account.RateLimitResetAtIsNil(),
				account.RateLimitResetAtLT(now),
			),
			// 上游过载期的跳过。
			account.Or(
				account.OverloadUntilIsNil(),
				account.OverloadUntilLT(now),
			),
		)

	if len(excludeIDs) > 0 {
		query = query.Where(account.IDNotIn(excludeIDs...))
	}

	accounts, err := query.
		Order(
			ent.Asc(account.FieldPriority),
			func(sel *sql.Selector) {
				sel.OrderExpr(sql.ExprP(account.FieldLastUsedAt + " asc nulls first"))
			},
		).
		Limit(candidateLimit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询候选账号: %w", err)
	}
	if len(accounts) == 0 {
		return nil, ErrNoAccountAvailable
	}

	var lastErr error
	var eligibleCount int
	preferred, exhausted := splitByWindow(accounts, now)
	s.sortByPriorityAndMemLastUsed(preferred)
	s.sortByPriorityAndMemLastUsed(exhausted)

	if len(exhausted) > 0 && len(preferred) > 0 && s.logger != nil {
		s.logger.Debug("优先使用额度窗口还有余量的账号",
			"group_id", groupID, "可用", len(preferred), "已打满", len(exhausted))
	}

	for _, bucket := range [][]*ent.Account{preferred, exhausted} {
		for _, acc := range bucket {
			creds, err := s.credentials(acc)
			if err != nil {
				lastErr = fmt.Errorf("账号 %d(%s) 的凭证无法解密: %w", acc.ID, acc.Name, err)
				if s.logger != nil {
					s.logger.Error("凭证解密失败", "account_id", acc.ID, "account_name", acc.Name, "err", err)
				}
				continue
			}

			// 仅当账号既没有可自动续期的 OAuth refresh_token、也未绑定本地容器节点时，才把 expires_at 视为硬过期时间拦截；
			// 避免把 OAuth 短效 1 小时 access_token 缓存过期时间误判为账号永久失效导致「分组内没有可用账号」。
			if acc.ExpiresAt != nil && !acc.ExpiresAt.After(now) && strings.TrimSpace(creds.RefreshToken) == "" && creds.NodeID <= 0 {
				continue
			}
			eligibleCount++

			if !s.limiter.tryAcquire(acc.ID, acc.Concurrency) {
				continue
			}

			accountID := acc.ID
			leasePlatform := resolveEffectiveAccountPlatform(acc.Platform, platform)
			accountMode, baseURL := resolveAccountEndpoint(leasePlatform, acc, creds)

			s.cacheMu.Lock()
			s.memLastUsed[accountID] = now
			s.cacheMu.Unlock()

			lease := &Lease{
				Account:        acc,
				Credentials:    creds,
				Platform:       leasePlatform,
				BaseURL:        baseURL,
				AccountMode:    accountMode,
				AccessToken:    creds.AccessToken,
				RefreshToken:   creds.RefreshToken,
				ManagementKey:  creds.ManagementKey,
				UID:            creds.UID,
				Nickname:       creds.Nickname,
				Email:          creds.Email,
				EnterpriseID:   creds.EnterpriseID,
				EnterpriseName: creds.EnterpriseName,
				Domain:         creds.Domain,
				ProxyURL:       creds.ProxyURL,
				Qimei36:        creds.Qimei36,
				MachineID:      creds.MachineID,
				NodeID:         creds.NodeID,
				AuthFile:       creds.AuthFile,
				Fingerprint:    creds.Fingerprint,
				Release:        func() { s.limiter.release(accountID) },
			}
			return lease, nil
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	if eligibleCount == 0 {
		return nil, ErrNoAccountAvailable
	}
	return nil, fmt.Errorf("%w（%s 平台的 %d 个候选账号并发均已达上限）",
		ErrNoAccountAvailable, platform, eligibleCount)
}

func resolveEffectiveAccountPlatform(rawAccPlatform, groupPlatform string) string {
	accNorm := domain.NormalizePlatform(rawAccPlatform)
	if groupPlatform != "" {
		if groupPlatform == domain.PlatformWorkBuddy && (accNorm == "" || accNorm == domain.PlatformCommandCode) {
			return domain.PlatformWorkBuddy
		}
		if accNorm == "" {
			return groupPlatform
		}
	}
	if accNorm == "" {
		return domain.PlatformWorkBuddy
	}
	return accNorm
}

func resolveAccountEndpoint(platform string, acc *ent.Account, creds Credentials) (accountMode string, baseURL string) {
	switch platform {
	case domain.PlatformOpenCode:
		accountMode = stringFromExtra(acc.Extra, "account_mode")
		baseURL = firstNonEmpty(
			creds.BaseURL,
			stringFromExtra(acc.Extra, "base_url"),
			stringFromExtra(acc.Credentials, "base_url"),
		)
		if baseURL == "" {
			baseURL = domain.DefaultOpenCodeBaseURL(accountMode)
		}
		return accountMode, baseURL

	case domain.PlatformAntigravity:
		return stringFromExtra(acc.Extra, "account_mode"), resolveAntigravityBaseURL(acc, creds)

	case domain.PlatformCommandCode:
		baseURL = firstNonEmpty(
			creds.BaseURL,
			stringFromExtra(acc.Credentials, "base_url"),
			stringFromExtra(acc.Extra, "base_url"),
		)
		return "", baseURL

	default:
		return "", creds.Domain
	}
}

func resolveAntigravityBaseURL(acc *ent.Account, creds Credentials) string {
	raw := firstNonEmpty(
		creds.BaseURL,
		stringFromExtra(acc.Credentials, "base_url"),
		stringFromExtra(acc.Extra, "base_url"),
		stringFromExtra(acc.Credentials, "endpoint"),
		stringFromExtra(acc.Extra, "endpoint"),
	)
	if raw == "" {
		dom := strings.TrimSpace(acc.Domain)
		// 忽略 Ent Schema 默认填入的 WorkBuddy 官网域名，避免误把本机 AntiGravity 账号发往 codebuddy.cn
		if dom != "" && !strings.EqualFold(strings.TrimRight(dom, "/"), strings.TrimRight(domain.WorkBuddyDefaultDomain, "/")) {
			raw = dom
		}
	}
	if raw == "" {
		hostIP := firstNonEmpty(stringFromExtra(acc.Extra, "host_ip"), stringFromExtra(acc.Credentials, "host_ip"))
		hostPort := int(firstNonZeroInt64(
			int64FromMap(acc.Extra, "host_port"),
			int64FromMap(acc.Credentials, "host_port"),
		))
		if hostIP != "" || hostPort > 0 {
			return antigravity.BaseURL(antigravity.NodeSpec{
				HostIP:   hostIP,
				HostPort: hostPort,
			})
		}
	}
	if raw == "" {
		return antigravity.BaseURL(antigravity.NodeSpec{})
	}
	return antigravity.BaseURL(antigravity.NodeSpec{HostIP: raw})
}

// stringFromExtra 从 JSON map 里取一个去首尾空白的字符串字段。
func stringFromExtra(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v, _ := extra[key].(string)
	return strings.TrimSpace(v)
}

func int64FromMap(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	val, ok := m[key]
	if !ok || val == nil {
		return 0
	}
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func firstNonZeroInt64(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

// Target 描述一个可用账号的接入信息，不持有并发名额。
type Target struct {
	AccountID      int64
	AccountName    string
	APIKey         string
	AccessToken    string
	RefreshToken   string
	ManagementKey  string
	UID            string
	Nickname       string
	Email          string
	EnterpriseID   string
	EnterpriseName string
	Domain         string
	ProxyURL       string
	Qimei36        string
	MachineID      string
	NodeID         int64
	AuthFile       string
	Fingerprint    antigravity.NodeFingerprint
	Platform       string
	BaseURL        string
	AccountMode    string
}

// WorkBuddyCreds 构造发送给 WorkBuddy 上游客户端的账号凭证。
func (t *Target) WorkBuddyCreds() workbuddy.AccountCreds {
	if t == nil {
		return workbuddy.AccountCreds{}
	}
	qimei, mid := workbuddy.EnsureDevice(t.Qimei36, t.MachineID)
	dom := workbuddy.NormalizeDomain(firstNonEmpty(t.Domain, t.BaseURL))
	return workbuddy.AccountCreds{
		AccountID:      t.AccountID,
		Name:           t.AccountName,
		UID:            t.UID,
		Nickname:       t.Nickname,
		Email:          t.Email,
		EnterpriseID:   t.EnterpriseID,
		EnterpriseName: t.EnterpriseName,
		Domain:         dom,
		AccessToken:    firstNonEmpty(t.AccessToken, t.APIKey),
		RefreshToken:   t.RefreshToken,
		ProxyURL:       t.ProxyURL,
		Qimei36:        qimei,
		MachineID:      mid,
	}
}

// AntigravityNodeSpec 构造发送给 AntiGravity 客户端的节点规格。
func (t *Target) AntigravityNodeSpec() antigravity.NodeSpec {
	if t == nil {
		return antigravity.NodeSpec{}
	}
	mode := antigravity.ModeRemoteAPI
	normAccMode := strings.ToLower(strings.TrimSpace(t.AccountMode))
	switch {
	case normAccMode == antigravity.ModeLocalCLI || normAccMode == "agy_cli" || normAccMode == "agy" || normAccMode == "cli" ||
		strings.EqualFold(t.BaseURL, "local_cli") || strings.EqualFold(t.BaseURL, "agy"):
		mode = antigravity.ModeLocalCLI
	case normAccMode == antigravity.ModeDirectCLI || strings.Contains(strings.ToLower(t.BaseURL), "cloudcode-pa.googleapis.com"):
		mode = antigravity.ModeDirectCLI
	case antigravity.IsLoopbackTarget(t.BaseURL):
		mode = antigravity.ModeLocalDocker
	}
	return antigravity.NodeSpec{
		ID:            t.NodeID,
		Name:          t.AccountName,
		Mode:          mode,
		HostIP:        t.BaseURL,
		ManagementKey: t.ManagementKey,
		APIKey:        firstNonEmpty(t.APIKey, t.AccessToken),
		ProxyURL:      t.ProxyURL,
		Fingerprint:   t.Fingerprint,
	}
}

// AnyTarget 取分组内任一可用账号的接入信息，但**不占用并发名额**。
func (s *Scheduler) AnyTarget(ctx context.Context, groupID int64) (*Target, error) {
	groupPlat, _ := s.GroupPlatform(ctx, groupID)
	target, err := s.firstActiveAccount(ctx, groupID, groupPlat)
	if err != nil {
		return nil, err
	}
	creds, err := s.credentials(target)
	if err != nil {
		return nil, err
	}
	return s.buildTarget(target, creds, groupPlat), nil
}

// AnyTargetByPlatform 取指定平台下任一处于 active 状态的账号（不限分组、不占并发名额），
// 供全平台模型目录聚合拉取等场景使用。
func (s *Scheduler) AnyTargetByPlatform(ctx context.Context, platform string) (*Target, error) {
	norm := domain.NormalizePlatform(platform)
	acc, err := s.client.Account.Query().
		Where(
			account.DeletedAtIsNil(),
			account.StatusEQ(domain.StatusActive),
			platformAccountPredicate(norm),
		).
		Order(ent.Asc(account.FieldPriority)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNoAccountAvailable
		}
		return nil, fmt.Errorf("按平台查询账号: %w", err)
	}
	creds, err := s.credentials(acc)
	if err != nil {
		return nil, err
	}
	return s.buildTarget(acc, creds, norm), nil
}

// TargetByAccountID 按账号 ID 取接入信息，用于单账号探活与余额刷新。
func (s *Scheduler) TargetByAccountID(ctx context.Context, accountID int64) (*Target, error) {
	acc, err := s.client.Account.Query().
		Where(account.IDEQ(accountID), account.DeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	creds, err := s.credentials(acc)
	if err != nil {
		return nil, err
	}
	return s.buildTarget(acc, creds, ""), nil
}

func (s *Scheduler) firstActiveAccount(ctx context.Context, groupID int64, groupPlatform string) (*ent.Account, error) {
	predicates := []predicate.Account{
		account.DeletedAtIsNil(),
		account.StatusEQ(domain.StatusActive),
		account.HasGroupsWith(group.IDEQ(groupID)),
	}
	if groupPlatform != "" {
		predicates = append(predicates, platformAccountPredicate(groupPlatform))
	}
	acc, err := s.client.Account.Query().
		Where(predicates...).
		Order(ent.Asc(account.FieldPriority)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNoAccountAvailable
		}
		return nil, fmt.Errorf("查询账号: %w", err)
	}
	return acc, nil
}

// buildTarget 把账号行与凭证组装成 Target。
func (s *Scheduler) buildTarget(acc *ent.Account, creds Credentials, groupPlatform string) *Target {
	targetPlatform := resolveEffectiveAccountPlatform(acc.Platform, groupPlatform)
	accountMode, baseURL := resolveAccountEndpoint(targetPlatform, acc, creds)

	return &Target{
		AccountID:      acc.ID,
		AccountName:    acc.Name,
		APIKey:         creds.APIKey,
		AccessToken:    creds.AccessToken,
		RefreshToken:   creds.RefreshToken,
		ManagementKey:  creds.ManagementKey,
		UID:            creds.UID,
		Nickname:       creds.Nickname,
		Email:          creds.Email,
		EnterpriseID:   creds.EnterpriseID,
		EnterpriseName: creds.EnterpriseName,
		Domain:         creds.Domain,
		ProxyURL:       creds.ProxyURL,
		Qimei36:        creds.Qimei36,
		MachineID:      creds.MachineID,
		NodeID:         creds.NodeID,
		AuthFile:       creds.AuthFile,
		Fingerprint:    creds.Fingerprint,
		Platform:       targetPlatform,
		BaseURL:        baseURL,
		AccountMode:    accountMode,
	}
}

func (s *Scheduler) sortByPriorityAndMemLastUsed(accs []*ent.Account) {
	if len(accs) <= 1 {
		return
	}
	s.cacheMu.RLock()
	lastMap := s.memLastUsed
	effTime := func(a *ent.Account) time.Time {
		var t time.Time
		if a.LastUsedAt != nil {
			t = *a.LastUsedAt
		}
		if mt, ok := lastMap[a.ID]; ok && mt.After(t) {
			return mt
		}
		return t
	}
	sort.SliceStable(accs, func(i, j int) bool {
		if accs[i].Priority != accs[j].Priority {
			return accs[i].Priority < accs[j].Priority
		}
		ti := effTime(accs[i])
		tj := effTime(accs[j])
		if ti.IsZero() && !tj.IsZero() {
			return true
		}
		if !ti.IsZero() && tj.IsZero() {
			return false
		}
		return ti.Before(tj)
	})
	s.cacheMu.RUnlock()
}

func (s *Scheduler) credentials(acc *ent.Account) (Credentials, error) {
	cacheKey := credCacheKey{accountID: acc.ID, updatedNs: acc.UpdatedAt.UnixNano()}
	s.cacheMu.RLock()
	if cached, ok := s.credsCache[cacheKey]; ok {
		s.cacheMu.RUnlock()
		return cached, nil
	}
	s.cacheMu.RUnlock()

	accessToken, err := s.decryptOrPlain(acc.Credentials, "api_key_encrypted", "api_key", "access_token_encrypted", "access_token")
	if err != nil {
		return Credentials{}, err
	}
	normPlat := domain.NormalizePlatform(acc.Platform)
	if accessToken == "" && normPlat != domain.PlatformAntigravity {
		return Credentials{}, errors.New("账号未配置 api_key")
	}

	refreshToken, err := s.decryptOrPlain(acc.Credentials, "refresh_token_encrypted", "refresh_token")
	if err != nil && s.logger != nil {
		s.logger.Warn("refresh_token 解密失败，将按无 refresh_token 处理", "account_id", acc.ID, "err", err)
	}

	mgmtKey, err := s.decryptOrPlain(acc.Credentials, "management_key_encrypted", "management_key")
	if err != nil && s.logger != nil {
		s.logger.Warn("management_key 解密失败，将按无 management_key 处理", "account_id", acc.ID, "err", err)
	}
	if mgmtKey == "" {
		mgmtKey = stringFromExtra(acc.Extra, "management_key")
	}

	qimei, mid := workbuddy.EnsureDevice(
		firstNonEmpty(acc.Qimei36, stringFromExtra(acc.Credentials, "qimei36"), stringFromExtra(acc.Extra, "qimei36")),
		firstNonEmpty(acc.MachineID, stringFromExtra(acc.Credentials, "machine_id"), stringFromExtra(acc.Extra, "machine_id")),
	)
	dom := workbuddy.NormalizeDomain(firstNonEmpty(
		acc.Domain,
		stringFromExtra(acc.Credentials, "domain"),
		stringFromExtra(acc.Extra, "domain"),
		stringFromExtra(acc.Extra, "base_url"),
	))
	baseURL := firstNonEmpty(
		stringFromExtra(acc.Credentials, "base_url"),
		stringFromExtra(acc.Extra, "base_url"),
	)
	proxyURL := firstNonEmpty(
		acc.ProxyURL,
		stringFromExtra(acc.Credentials, "proxy_url"),
		stringFromExtra(acc.Extra, "proxy_url"),
	)
	if normProxy, err := workbuddy.NormalizeProxyURL(proxyURL); err == nil {
		proxyURL = normProxy
	}

	nodeID := firstNonZeroInt64(
		int64FromMap(acc.Extra, "node_id"),
		int64FromMap(acc.Credentials, "node_id"),
	)
	authFile := firstNonEmpty(
		stringFromExtra(acc.Extra, "auth_file"),
		stringFromExtra(acc.Extra, "filename"),
		stringFromExtra(acc.Credentials, "auth_file"),
		stringFromExtra(acc.Credentials, "filename"),
	)

	var fp antigravity.NodeFingerprint
	if normPlat == domain.PlatformAntigravity {
		fp, _ = antigravity.ExtractFingerprintFromExtra(acc.Extra, fmt.Sprintf("acc-%d", acc.ID))
		if fp.Qimei36 == "" {
			fp.Qimei36 = qimei
		}
		if fp.MachineID == "" {
			fp.MachineID = mid
		}
		fp = antigravity.NormalizeNodeFingerprint(fp, fmt.Sprintf("acc-%d", acc.ID))
	}

	out := Credentials{
		APIKey:         accessToken,
		AccessToken:    accessToken,
		RefreshToken:   refreshToken,
		ManagementKey:  mgmtKey,
		UID:            firstNonEmpty(acc.UID, stringFromExtra(acc.Credentials, "uid"), stringFromExtra(acc.Extra, "uid")),
		Nickname:       firstNonEmpty(acc.Nickname, stringFromExtra(acc.Credentials, "nickname"), stringFromExtra(acc.Extra, "nickname")),
		Email:          firstNonEmpty(acc.Email, stringFromExtra(acc.Credentials, "email"), stringFromExtra(acc.Extra, "email")),
		EnterpriseID:   firstNonEmpty(acc.EnterpriseID, stringFromExtra(acc.Credentials, "enterprise_id"), stringFromExtra(acc.Extra, "enterprise_id")),
		EnterpriseName: firstNonEmpty(acc.EnterpriseName, stringFromExtra(acc.Credentials, "enterprise_name"), stringFromExtra(acc.Extra, "enterprise_name")),
		Domain:         dom,
		BaseURL:        baseURL,
		ProxyURL:       proxyURL,
		Qimei36:        qimei,
		MachineID:      mid,
		NodeID:         nodeID,
		AuthFile:       authFile,
		Fingerprint:    fp,
	}

	if acc.ID > 0 {
		key := credCacheKey{accountID: acc.ID, updatedNs: acc.UpdatedAt.UnixNano()}
		s.cacheMu.Lock()
		if len(s.credsCache) > 2048 {
			s.credsCache = make(map[credCacheKey]Credentials)
		}
		s.credsCache[key] = out
		s.cacheMu.Unlock()
	}
	return out, nil
}

func (s *Scheduler) decryptOrPlain(m map[string]any, keys ...string) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	for i := 0; i < len(keys); i++ {
		k := keys[i]
		val, _ := m[k].(string)
		if val == "" {
			continue
		}
		if len(k) > len("_encrypted") && k[len(k)-len("_encrypted"):] == "_encrypted" {
			if s.vault == nil {
				return "", errors.New("未配置 vault，无法解密凭证")
			}
			plain, err := s.vault.Decrypt(val)
			if err != nil {
				return "", err
			}
			return plain, nil
		}
		return val, nil
	}
	return "", nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- 状态回写 ----

// MarkUsed 记录账号最近被使用，并清零连续失败计数。
// 内存中立即更新轮询时间戳保证负载均衡精确；数据库写操作按每账号 15s 节流异步落盘，消除高并发 SQLite 写锁争用。
func (s *Scheduler) MarkUsed(ctx context.Context, accountID int64) {
	now := time.Now()
	s.cacheMu.Lock()
	s.memLastUsed[accountID] = now
	lastFlush := s.lastMarkFlush[accountID]
	shouldFlush := lastFlush.IsZero() || now.Sub(lastFlush) >= 15*time.Second
	if shouldFlush {
		s.lastMarkFlush[accountID] = now
	}
	s.cacheMu.Unlock()

	if !shouldFlush {
		return
	}
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, err := s.client.Account.Update().
		Where(account.ID(accountID)).
		SetLastUsedAt(now).
		SetConsecutiveFailures(0).
		Save(bgCtx)
	if err != nil && s.logger != nil {
		s.logger.Warn("回写账号使用状态失败", "account_id", accountID, "err", err)
	}
}

// MarkRateLimited 记录一次限流。resetAt 为空时按 5 分钟兜底。
func (s *Scheduler) MarkRateLimited(ctx context.Context, accountID int64, resetAt *time.Time) {
	reset := time.Now().Add(5 * time.Minute)
	if resetAt != nil && resetAt.After(time.Now()) {
		reset = *resetAt
	}
	_, err := s.client.Account.Update().
		Where(account.ID(accountID)).
		SetRateLimitedAt(time.Now()).
		SetRateLimitResetAt(reset).
		Save(ctx)
	if err != nil && s.logger != nil {
		s.logger.Warn("回写限流状态失败", "account_id", accountID, "err", err)
	}
}

// MarkOverloaded 记录一次上游过载（529 之类），在 until 之前不再调度该账号。
func (s *Scheduler) MarkOverloaded(ctx context.Context, accountID int64, until time.Time) {
	_, err := s.client.Account.Update().
		Where(account.ID(accountID)).
		SetOverloadUntil(until).
		Save(ctx)
	if err != nil && s.logger != nil {
		s.logger.Warn("回写过载状态失败", "account_id", accountID, "err", err)
	}
}

// MarkFailure 记录一次失败，连续失败达到阈值时自动禁用账号。
func (s *Scheduler) MarkFailure(ctx context.Context, accountID int64, cause error) bool {
	msg := cause.Error()

	if _, err := s.client.Account.Update().
		Where(account.IDEQ(accountID), account.DeletedAtIsNil()).
		AddConsecutiveFailures(1).
		SetErrorMessage(msg).
		Save(ctx); err != nil {
		if s.logger != nil {
			s.logger.Warn("回写失败计数失败", "account_id", accountID, "err", err)
		}
		return false
	}

	affected, err := s.client.Account.Update().
		Where(
			account.IDEQ(accountID),
			account.DeletedAtIsNil(),
			account.ConsecutiveFailuresGTE(s.failureThreshold),
			account.StatusNEQ(domain.StatusError),
		).
		SetStatus(domain.StatusError).
		SetSchedulable(false).
		Save(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("自动禁用账号失败", "account_id", accountID, "err", err)
		}
		return false
	}
	if affected == 0 {
		return false
	}

	name := ""
	if acc, err := s.client.Account.Get(ctx, accountID); err == nil {
		name = acc.Name
	}
	if s.logger != nil {
		s.logger.Warn("账号连续失败已达阈值，已自动禁用",
			"account_id", accountID, "account_name", name,
			"threshold", s.failureThreshold, "cause", msg)
	}
	return true
}
