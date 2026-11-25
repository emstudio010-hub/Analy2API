package relay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/toolobf"
)

const (
	// DefaultSession429PauseSeconds 默认非额度 429 会话暂停时长（5 分钟 = 300 秒）。
	DefaultSession429PauseSeconds = 300
	// DefaultSession429LogDir 默认非额度 429 请求体审计日志保存目录。
	DefaultSession429LogDir = "./data/logs/session_429"
	// DefaultSession429MaxLogFiles 默认保留的 429 审计日志文件上限。
	DefaultSession429MaxLogFiles = 200
)

var defaultQuotaKeywords = []string{
	"USAGE_EXCEEDED",
	"QUOTA_EXCEEDED",
	"INSUFFICIENT_QUOTA",
	"BILLING_HARD_LIMIT",
	"CREDIT_EXHAUSTED",
	"OUT_OF_CREDITS",
	"BALANCE_INSUFFICIENT",
	"PAYMENT_REQUIRED",
	"INSUFFICIENT_BALANCE",
	"11128",
	"额度不足",
	"余额不足",
	"积分不足",
	"套餐额度",
	"额度已用尽",
}

// SessionGuardConfig 控制「非账号额度不足 429 会话暂停调度与请求体 Log 落盘」的全部可调参数。
type SessionGuardConfig struct {
	Enabled             bool     `json:"enabled"`
	PauseSeconds        int      `json:"pause_seconds"`
	LogEnabled          bool     `json:"log_enabled"`
	LogDir              string   `json:"log_dir"`
	MaxLogFiles         int      `json:"max_log_files"`
	AbortCurrentRetry   bool     `json:"abort_current_retry"`
	FallbackFingerprint bool     `json:"fallback_fingerprint"`
	QuotaKeywords       []string `json:"quota_keywords"`
}

// DefaultSessionGuardConfig 返回开箱即用的默认会话暂停保护配置（暂停 300 秒 = 5 分钟，开启完整请求体 Log 落盘）。
func DefaultSessionGuardConfig() SessionGuardConfig {
	kw := make([]string, len(defaultQuotaKeywords))
	copy(kw, defaultQuotaKeywords)
	return SessionGuardConfig{
		Enabled:             true,
		PauseSeconds:        DefaultSession429PauseSeconds,
		LogEnabled:          true,
		LogDir:              DefaultSession429LogDir,
		MaxLogFiles:         DefaultSession429MaxLogFiles,
		AbortCurrentRetry:   true,
		FallbackFingerprint: true,
		QuotaKeywords:       kw,
	}
}

// NormalizeSessionGuardConfig 校验并规范化 SessionGuardConfig。
func NormalizeSessionGuardConfig(cfg SessionGuardConfig) SessionGuardConfig {
	out := cfg
	if out.PauseSeconds <= 0 {
		out.PauseSeconds = DefaultSession429PauseSeconds
	}
	if out.PauseSeconds < 5 {
		out.PauseSeconds = 5
	}
	if out.PauseSeconds > 86400 {
		out.PauseSeconds = 86400
	}
	out.LogDir = strings.TrimSpace(out.LogDir)
	if out.LogDir == "" {
		out.LogDir = DefaultSession429LogDir
	}
	if out.MaxLogFiles < 0 {
		out.MaxLogFiles = DefaultSession429MaxLogFiles
	}
	if out.MaxLogFiles > 5000 {
		out.MaxLogFiles = 5000
	}
	if out.QuotaKeywords == nil {
		out.QuotaKeywords = append([]string(nil), defaultQuotaKeywords...)
	} else {
		seen := make(map[string]struct{}, len(out.QuotaKeywords))
		clean := make([]string, 0, len(out.QuotaKeywords))
		for _, k := range out.QuotaKeywords {
			trimmed := strings.TrimSpace(k)
			if trimmed == "" {
				continue
			}
			upper := strings.ToUpper(trimmed)
			if _, ok := seen[upper]; ok {
				continue
			}
			seen[upper] = struct{}{}
			clean = append(clean, trimmed)
		}
		out.QuotaKeywords = clean
	}
	return out
}

// PausedSessionInfo 描述一个因非额度 429 被暂停调度的会话及其诊断摘要。
type PausedSessionInfo struct {
	SessionID              string    `json:"session_id"`
	PausedAt               time.Time `json:"paused_at"`
	PausedUntil            time.Time `json:"paused_until"`
	RemainingSeconds       int       `json:"remaining_seconds"`
	RequestID              string    `json:"request_id"`
	GroupID                int64     `json:"group_id"`
	AccountID              int64     `json:"account_id"`
	Platform               string    `json:"platform"`
	Protocol               string    `json:"protocol"`
	Model                  string    `json:"model"`
	ReportedStatus         int       `json:"reported_status"`
	ErrorCode              string    `json:"error_code"`
	ErrorMessage           string    `json:"error_message"`
	LogFile                string    `json:"log_file,omitempty"`
	LogPath                string    `json:"log_path,omitempty"`
	ObfuscatedToolsCount   int       `json:"obfuscated_tools_count"`
	UnobfuscatedToolsCount int       `json:"unobfuscated_tools_count"`
	TotalToolsCount        int       `json:"total_tools_count"`
	SuspectedUnobfuscated  bool      `json:"suspected_unobfuscated"`
}

// Session429LogFile 描述磁盘上的单个非额度 429 审计日志文件元数据。
type Session429LogFile struct {
	Filename  string    `json:"filename"`
	SizeBytes int64     `json:"size_bytes"`
	ModTime   time.Time `json:"mod_time"`
}

// PauseAndLogInput 记录一次触发非额度 429 会话暂停所需完整上下文（含原始与出站请求体）。
type PauseAndLogInput struct {
	SessionID        string
	RequestID        string
	GroupID          int64
	AccountID        int64
	Platform         string
	Protocol         string
	Model            string
	MappedErr        *MappedError
	RawInboundBody   []byte
	OutboundBody     []byte
	ToolSession      *toolobf.Session
	ObfGlobalEnabled bool
	ObfEffectiveMode string
}

// SessionGuard 在网关内管理「非额度 429 会话级暂停调度」与「请求体混淆诊断 .log 文件落盘」。
type SessionGuard struct {
	mu     sync.RWMutex
	cfg    SessionGuardConfig
	paused map[string]*PausedSessionInfo
}

// NewSessionGuard 构造会话保护器。
func NewSessionGuard(cfg SessionGuardConfig) *SessionGuard {
	return &SessionGuard{
		cfg:    NormalizeSessionGuardConfig(cfg),
		paused: make(map[string]*PausedSessionInfo),
	}
}

// NewDefaultSessionGuard 使用默认参数（开启、暂停 300 秒、落盘日志）构造会话保护器。
func NewDefaultSessionGuard() *SessionGuard {
	return NewSessionGuard(DefaultSessionGuardConfig())
}

// Config 返回当前配置的防御性副本。
func (g *SessionGuard) Config() SessionGuardConfig {
	if g == nil {
		return DefaultSessionGuardConfig()
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	cp := g.cfg
	cp.QuotaKeywords = append([]string(nil), g.cfg.QuotaKeywords...)
	return cp
}

// Reload 热更新配置（零重启生效）。
func (g *SessionGuard) Reload(cfg SessionGuardConfig) {
	if g == nil {
		return
	}
	norm := NormalizeSessionGuardConfig(cfg)
	g.mu.Lock()
	g.cfg = norm
	g.mu.Unlock()
}

// IsQuotaExhausted 判断 MappedError 是否属于「账号额度/余额/积分不足」。
func (e *MappedError) IsQuotaExhausted(quotaKeywords []string) bool {
	if e == nil {
		return false
	}
	if e.ReportedStatus == http.StatusPaymentRequired {
		return true
	}
	upperCode := strings.ToUpper(strings.TrimSpace(e.Code))
	upperMsg := strings.ToUpper(strings.TrimSpace(e.Message))
	if len(quotaKeywords) == 0 {
		quotaKeywords = defaultQuotaKeywords
	}
	for _, kw := range quotaKeywords {
		trimmed := strings.ToUpper(strings.TrimSpace(kw))
		if trimmed == "" {
			continue
		}
		if (upperCode != "" && strings.Contains(upperCode, trimmed)) ||
			(upperMsg != "" && strings.Contains(upperMsg, trimmed)) {
			return true
		}
	}
	return false
}

// IsNonQuotaRateLimit 判断错误是否为「非账号额度不足的 HTTP 429 限流/风控」。
func (e *MappedError) IsNonQuotaRateLimit(quotaKeywords []string) bool {
	if e == nil {
		return false
	}
	if e.Status != http.StatusTooManyRequests && e.ReportedStatus != http.StatusTooManyRequests {
		return false
	}
	return !e.IsQuotaExhausted(quotaKeywords)
}

// CheckPaused 检查 sessionID 当前是否因非额度 429 处于暂停调度期内。
func (g *SessionGuard) CheckPaused(sessionID string) (PausedSessionInfo, bool) {
	if g == nil {
		return PausedSessionInfo{}, false
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return PausedSessionInfo{}, false
	}

	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.cfg.Enabled {
		return PausedSessionInfo{}, false
	}
	entry, ok := g.paused[sid]
	if !ok {
		return PausedSessionInfo{}, false
	}
	if !now.Before(entry.PausedUntil) {
		delete(g.paused, sid)
		return PausedSessionInfo{}, false
	}

	cp := *entry
	rem := int(entry.PausedUntil.Sub(now).Seconds())
	if rem < 1 {
		rem = 1
	}
	cp.RemainingSeconds = rem
	return cp, true
}

// PauseAndLog 在发生非额度 429 时暂停该会话调度并（若开启 LogEnabled）写入完整请求体诊断 .log 文件。
func (g *SessionGuard) PauseAndLog(in PauseAndLogInput) PausedSessionInfo {
	if g == nil {
		return PausedSessionInfo{}
	}
	cfg := g.Config()
	if !cfg.Enabled {
		return PausedSessionInfo{}
	}
	sid := strings.TrimSpace(in.SessionID)
	if sid == "" {
		return PausedSessionInfo{}
	}

	now := time.Now()
	pauseDur := time.Duration(cfg.PauseSeconds) * time.Second
	if pauseDur <= 0 {
		pauseDur = time.Duration(DefaultSession429PauseSeconds) * time.Second
	}
	pausedUntil := now.Add(pauseDur)

	diag := inspectOutboundPayloadTools(in.RawInboundBody, in.OutboundBody, in.ToolSession)

	var (
		errStatus int
		errCode   string
		errMsg    string
	)
	if in.MappedErr != nil {
		errStatus = in.MappedErr.ReportedStatus
		if errStatus == 0 {
			errStatus = in.MappedErr.Status
		}
		errCode = in.MappedErr.Code
		errMsg = in.MappedErr.Message
	}

	info := PausedSessionInfo{
		SessionID:              sid,
		PausedAt:               now,
		PausedUntil:            pausedUntil,
		RemainingSeconds:       int(pauseDur.Seconds()),
		RequestID:              in.RequestID,
		GroupID:                in.GroupID,
		AccountID:              in.AccountID,
		Platform:               in.Platform,
		Protocol:               in.Protocol,
		Model:                  in.Model,
		ReportedStatus:         errStatus,
		ErrorCode:              errCode,
		ErrorMessage:           errMsg,
		ObfuscatedToolsCount:   diag.obfuscatedCount,
		UnobfuscatedToolsCount: diag.unobfuscatedCount,
		TotalToolsCount:        diag.totalCount,
		SuspectedUnobfuscated:  diag.suspectedUnobfuscated,
	}

	if cfg.LogEnabled {
		if filename, fullPath, writeErr := writeSession429LogFile(cfg, info, in, diag); writeErr == nil {
			info.LogFile = filename
			info.LogPath = fullPath
		}
	}

	g.mu.Lock()
	g.paused[sid] = &info
	g.mu.Unlock()

	return info
}

// ResumeSession 手动解除指定会话的暂停状态。
func (g *SessionGuard) ResumeSession(sessionID string) bool {
	if g == nil {
		return false
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, existed := g.paused[sid]
	delete(g.paused, sid)
	return existed
}

// ClearAllPaused 清空全部处于暂停中的会话。
func (g *SessionGuard) ClearAllPaused() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.paused)
	g.paused = make(map[string]*PausedSessionInfo)
	return n
}

// PausedSessions 返回当前所有仍在暂停有效期内的会话列表（按最近触发时间倒序）。
func (g *SessionGuard) PausedSessions() []PausedSessionInfo {
	if g == nil {
		return []PausedSessionInfo{}
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]PausedSessionInfo, 0, len(g.paused))
	for sid, entry := range g.paused {
		if !now.Before(entry.PausedUntil) {
			delete(g.paused, sid)
			continue
		}
		cp := *entry
		rem := int(entry.PausedUntil.Sub(now).Seconds())
		if rem < 1 {
			rem = 1
		}
		cp.RemainingSeconds = rem
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].PausedAt.After(out[j].PausedAt)
	})
	return out
}

// ListLogFiles 返回 LogDir 下保存的非额度 429 `.log` 审计文件列表（按时间倒序）。
func (g *SessionGuard) ListLogFiles(limit int) ([]Session429LogFile, error) {
	if g == nil {
		return []Session429LogFile{}, nil
	}
	cfg := g.Config()
	if limit <= 0 {
		limit = 50
	}
	entries, err := os.ReadDir(cfg.LogDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Session429LogFile{}, nil
		}
		return nil, err
	}
	files := make([]Session429LogFile, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".log") {
			continue
		}
		info, iErr := ent.Info()
		if iErr != nil {
			continue
		}
		files = append(files, Session429LogFile{
			Filename:  name,
			SizeBytes: info.Size(),
			ModTime:   info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})
	if len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

// ReadLogFile 安全读取指定 .log 审计文件内容（严格拦截路径穿越）。
func (g *SessionGuard) ReadLogFile(filename string) (string, error) {
	if g == nil {
		return "", errors.New("会话保护器未初始化")
	}
	base := filepath.Base(strings.TrimSpace(filename))
	if base == "" || base == "." || base == ".." || !strings.HasSuffix(strings.ToLower(base), ".log") ||
		strings.ContainsAny(filename, `/\`) {
		return "", errors.New("日志文件名不合法")
	}
	cfg := g.Config()
	target := filepath.Join(cfg.LogDir, base)
	raw, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ExtractSessionID 从请求头、JSON body（含 metadata.user_id / session_id / conversation_id / thread_id）
// 中提取会话标识；若缺失且开启 fallbackFingerprint，则基于 APIKeyID/ClientIP/模型/首条消息派生稳定会话 ID。
func ExtractSessionID(req *RelayRequest, normalized *normalizedRequest, fallbackFingerprint bool) string {
	if req != nil {
		for _, h := range req.SessionHints {
			if trimmed := strings.TrimSpace(h); trimmed != "" {
				return trimmed
			}
		}
	}
	if normalized != nil {
		for _, h := range normalized.SessionHints {
			if trimmed := strings.TrimSpace(h); trimmed != "" {
				return trimmed
			}
		}
	}

	if req != nil && len(req.Body) > 0 {
		var probe struct {
			SessionID      string `json:"session_id"`
			ConversationID string `json:"conversation_id"`
			ThreadID       string `json:"thread_id"`
			User           string `json:"user"`
			Metadata       struct {
				SessionID string `json:"session_id"`
				UserID    string `json:"user_id"`
			} `json:"metadata"`
		}
		if json.Unmarshal(req.Body, &probe) == nil {
			for _, cand := range []string{
				probe.SessionID,
				probe.ConversationID,
				probe.ThreadID,
				probe.Metadata.SessionID,
				probe.Metadata.UserID,
				probe.User,
			} {
				if trimmed := strings.TrimSpace(cand); trimmed != "" {
					if idx := strings.Index(trimmed, "_session_"); idx >= 0 {
						after := strings.TrimSpace(trimmed[idx+len("_session_"):])
						if after != "" {
							return after
						}
					}
					return trimmed
				}
			}
		}
	}

	if !fallbackFingerprint || req == nil {
		return ""
	}
	var seedParts []string
	seedParts = append(seedParts, fmt.Sprintf("k:%d|g:%d|ip:%s", req.APIKeyID, req.GroupID, strings.TrimSpace(req.ClientIP)))
	if normalized != nil {
		seedParts = append(seedParts, "m:"+strings.TrimSpace(normalized.Model))
		for _, m := range normalized.Messages {
			if role, _ := m["role"].(string); strings.EqualFold(role, "user") {
				rawMsg, _ := json.Marshal(m["content"])
				seedParts = append(seedParts, string(rawMsg))
				break
			}
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(seedParts, "|")))
	return "auto_sess_" + hex.EncodeToString(sum[:8])
}

type outboundToolsDiagnosis struct {
	outboundToolNames     []string
	entries               []toolobf.MappingEntry
	obfuscatedCount       int
	unobfuscatedCount     int
	totalCount            int
	suspectedUnobfuscated bool
	reportLines           []string
}

func inspectOutboundPayloadTools(rawInbound, outbound []byte, toolSess *toolobf.Session) outboundToolsDiagnosis {
	diag := outboundToolsDiagnosis{}
	if toolSess != nil {
		diag.entries = toolSess.Entries()
	}

	revSet := make(map[string]string, len(diag.entries))
	passthroughSet := make(map[string]struct{}, len(diag.entries))
	for _, e := range diag.entries {
		if e.Changed {
			revSet[e.Obfuscated] = e.Original
		} else {
			passthroughSet[e.Original] = struct{}{}
		}
	}

	toolNames := collectToolNamesFromPayload(outbound)
	if len(toolNames) == 0 && len(rawInbound) > 0 && len(diag.entries) == 0 {
		toolNames = collectToolNamesFromPayload(rawInbound)
	}
	diag.outboundToolNames = toolNames
	diag.totalCount = len(toolNames)

	for _, name := range toolNames {
		if orig, ok := revSet[name]; ok {
			diag.obfuscatedCount++
			diag.reportLines = append(diag.reportLines, fmt.Sprintf("  - [OBFUSCATED]   %q  (原名: %q)", name, orig))
			continue
		}
		if _, ok := passthroughSet[name]; ok {
			diag.reportLines = append(diag.reportLines, fmt.Sprintf("  - [PASSTHROUGH]  %q  (规则排除或非敏感跳过)", name))
			continue
		}
		diag.unobfuscatedCount++
		diag.reportLines = append(diag.reportLines, fmt.Sprintf("  - [UNOBFUSCATED] %q  <-- ⚠️ 原名直出未混淆!", name))
	}
	if diag.unobfuscatedCount > 0 {
		diag.suspectedUnobfuscated = true
	}
	return diag
}

func collectToolNamesFromPayload(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil || root == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(name string) {
		n := strings.TrimSpace(name)
		if n == "" {
			return
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}

	for _, field := range []string{"tools", "functions"} {
		if arr, ok := root[field].([]any); ok {
			for _, it := range arr {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				if fn, ok := m["function"].(map[string]any); ok {
					if name, _ := fn["name"].(string); name != "" {
						add(name)
					}
				}
				if name, _ := m["name"].(string); name != "" {
					add(name)
				}
			}
		}
	}
	// CommandCode envelope 也可能将 tools 放在 messages/input 或顶层
	if len(out) == 0 {
		if msgs, ok := root["messages"].([]any); ok {
			for _, mRaw := range msgs {
				m, ok := mRaw.(map[string]any)
				if !ok {
					continue
				}
				if calls, ok := m["tool_calls"].([]any); ok {
					for _, cRaw := range calls {
						if c, ok := cRaw.(map[string]any); ok {
							if fn, ok := c["function"].(map[string]any); ok {
								if n, _ := fn["name"].(string); n != "" {
									add(n)
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

func writeSession429LogFile(
	cfg SessionGuardConfig,
	info PausedSessionInfo,
	in PauseAndLogInput,
	diag outboundToolsDiagnosis,
) (string, string, error) {
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		return "", "", err
	}

	ts := info.PausedAt.Format("20060102_150405")
	safeSID := sanitizeLogFileToken(info.SessionID, 40)
	safeRID := sanitizeLogFileToken(info.RequestID, 24)
	filename := fmt.Sprintf("429_%s_%s_%s.log", ts, safeSID, safeRID)
	fullPath := filepath.Join(cfg.LogDir, filename)

	var b strings.Builder
	b.WriteString("================================================================================\n")
	b.WriteString("CMD2API NON-QUOTA 429 SESSION PAUSE & REQUEST AUDIT LOG\n")
	b.WriteString("================================================================================\n")
	b.WriteString(fmt.Sprintf("Timestamp          : %s\n", info.PausedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("Session ID         : %s\n", info.SessionID))
	b.WriteString(fmt.Sprintf("Paused Until       : %s (%ds)\n", info.PausedUntil.Format(time.RFC3339), info.RemainingSeconds))
	b.WriteString(fmt.Sprintf("Request ID         : %s\n", info.RequestID))
	b.WriteString(fmt.Sprintf("Group ID / Platform: group=%d  platform=%s  protocol=%s\n", info.GroupID, info.Platform, info.Protocol))
	b.WriteString(fmt.Sprintf("Account ID / Model : account=%d  model=%s\n", info.AccountID, info.Model))
	b.WriteString(fmt.Sprintf("Upstream 429 Detail: status=%d  code=%q  message=%q\n", info.ReportedStatus, info.ErrorCode, info.ErrorMessage))
	b.WriteString("\n--- TOOL OBFUSCATION INSPECTION (分辨是否未混淆) --------------------------------\n")
	b.WriteString(fmt.Sprintf("Global Obf Enabled : %v   Effective Mode: %s   ToolSession Active: %v\n",
		in.ObfGlobalEnabled, nonEmptyStr(in.ObfEffectiveMode, "off/none"), in.ToolSession != nil))
	b.WriteString(fmt.Sprintf("Tools Summary      : total=%d  obfuscated=%d  unobfuscated_raw=%d  suspected_unobfuscated=%v\n",
		diag.totalCount, diag.obfuscatedCount, diag.unobfuscatedCount, diag.suspectedUnobfuscated))
	if len(diag.reportLines) > 0 {
		b.WriteString("Outbound Tools List:\n")
		for _, line := range diag.reportLines {
			b.WriteString(line + "\n")
		}
	}
	if len(diag.entries) > 0 {
		b.WriteString("Session Bijection Mappings (original -> outbound):\n")
		for _, e := range diag.entries {
			b.WriteString(fmt.Sprintf("  - %s -> %s (changed=%v)\n", e.Original, e.Obfuscated, e.Changed))
		}
	}

	b.WriteString("\n=== [1] RAW INBOUND REQUEST BODY (客户端原始请求体) ==============================\n")
	b.WriteString(prettyJSONOrRaw(in.RawInboundBody))
	b.WriteString("\n\n=== [2] OUTBOUND UPSTREAM REQUEST BODY (实际发往上游的请求体) ===================\n")
	if len(in.OutboundBody) > 0 {
		b.WriteString(prettyJSONOrRaw(in.OutboundBody))
	} else {
		b.WriteString("<empty or failed before serialization>\n")
	}
	b.WriteString("\n")

	if err := os.WriteFile(fullPath, []byte(b.String()), 0o644); err != nil {
		return "", "", err
	}

	pruneOldSession429Logs(cfg.LogDir, cfg.MaxLogFiles)
	return filename, fullPath, nil
}

func pruneOldSession429Logs(dir string, maxFiles int) {
	if maxFiles <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileMeta struct {
		name    string
		modTime time.Time
	}
	var list []fileMeta
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".log") {
			continue
		}
		if info, err := ent.Info(); err == nil {
			list = append(list, fileMeta{name: ent.Name(), modTime: info.ModTime()})
		}
	}
	if len(list) <= maxFiles {
		return
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].modTime.After(list[j].modTime)
	})
	for _, old := range list[maxFiles:] {
		_ = os.Remove(filepath.Join(dir, old.name))
	}
}

func prettyJSONOrRaw(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "<empty>"
	}
	var buf bytes.Buffer
	if json.Indent(&buf, trimmed, "", "  ") == nil {
		return buf.String()
	}
	return string(raw)
}

func sanitizeLogFileToken(raw string, maxLen int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "none"
	}
	var b strings.Builder
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
		if maxLen > 0 && b.Len() >= maxLen {
			break
		}
	}
	res := b.String()
	if res == "" {
		return "none"
	}
	return res
}

func nonEmptyStr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
