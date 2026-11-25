package antigravity

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultCLISessionTTL = 30 * time.Minute
	defaultMaxCLISession = 256
	maxHistoryFileBytes  = 2 * 1024 * 1024 // 隔离容器内 history.jsonl 超过 2MB 时自动截断，防止磁盘膨胀
)

// safeAgyConvIDRe 严格校验 agy conversation_id (UUID / 字母数字短横线)，杜绝路径穿越误删。
var safeAgyConvIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{7,127}$`)

var (
	globalCleanedConversations atomic.Int64
	globalFreedDiskBytes       atomic.Int64
)

// GlobalDiskCleanupStats 返回本进程累计自动清理的 agy 磁盘会话数量与释放字节数。
func GlobalDiskCleanupStats() (cleanedConversations int64, freedBytes int64) {
	return globalCleanedConversations.Load(), globalFreedDiskBytes.Load()
}

// CLISessionEntry 记录一条「上游 session_id -> 容器内真实 agy CLI 会话 ID (conversation_id / si)」映射。
// 不同上游 session_id 严格隔离并自动开启新的 agy 会话 (si)；同一上游 session_id 可在 ephemeral 模式下续接对应 agy 会话。
type CLISessionEntry struct {
	UpstreamSessionID string    `json:"upstream_session_id"`
	AgyConversationID string    `json:"agy_conversation_id"`
	Model             string    `json:"model"`
	LastMessageCount  int       `json:"last_message_count"`
	TurnCount         int       `json:"turn_count"`
	CreatedAt         time.Time `json:"created_at"`
	LastUsedAt        time.Time `json:"last_used_at"`
}

// CLISessionStore 管理单个 agy CLI 客户端内全部活跃的上游会话映射、每会话串行锁以及驱逐自动磁盘清理回调。
type CLISessionStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	maxSize int
	entries map[string]*CLISessionEntry
	locks   map[string]*sync.Mutex
	onEvict func(CLISessionEntry)
}

// NewCLISessionStore 创建会话映射存储。
func NewCLISessionStore(ttl time.Duration, maxSize int) *CLISessionStore {
	if ttl <= 0 {
		ttl = defaultCLISessionTTL
	}
	if maxSize <= 0 {
		maxSize = defaultMaxCLISession
	}
	return &CLISessionStore{
		ttl:     ttl,
		maxSize: maxSize,
		entries: make(map[string]*CLISessionEntry),
		locks:   make(map[string]*sync.Mutex),
	}
}

// SetOnEvict 注册会话过期、容量淘汰、替换或删除时的自动清理回调（用于即时删除磁盘上的 agy 会话文件）。
func (s *CLISessionStore) SetOnEvict(fn func(CLISessionEntry)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onEvict = fn
	s.mu.Unlock()
}

func (s *CLISessionStore) notifyEvicted(evicted []CLISessionEntry) {
	if s == nil || len(evicted) == 0 {
		return
	}
	s.mu.Lock()
	cb := s.onEvict
	s.mu.Unlock()
	if cb == nil {
		return
	}
	for _, ent := range evicted {
		if strings.TrimSpace(ent.AgyConversationID) != "" {
			cb(ent)
		}
	}
}

// LockSession 获取单个上游 session_id 的专属互斥锁，避免同一会话并发乱序，同时允许不同 session_id 并行。
func (s *CLISessionStore) LockSession(upstreamSessionID string) func() {
	if s == nil {
		return func() {}
	}
	key := strings.TrimSpace(upstreamSessionID)
	if key == "" {
		return func() {}
	}
	s.mu.Lock()
	lk, ok := s.locks[key]
	if !ok {
		lk = &sync.Mutex{}
		s.locks[key] = lk
	}
	s.mu.Unlock()

	lk.Lock()
	return func() {
		lk.Unlock()
	}
}

// Lookup 查询上游 session_id 当前已绑定的 agy conversation_id (si)；若未绑定或已过期则返回 false，并自动触发过期清理。
func (s *CLISessionStore) Lookup(upstreamSessionID string) (CLISessionEntry, bool) {
	if s == nil {
		return CLISessionEntry{}, false
	}
	key := strings.TrimSpace(upstreamSessionID)
	if key == "" {
		return CLISessionEntry{}, false
	}
	now := time.Now().UTC()
	var expired []CLISessionEntry

	s.mu.Lock()
	ent, ok := s.entries[key]
	if !ok || ent == nil {
		s.mu.Unlock()
		return CLISessionEntry{}, false
	}
	if now.Sub(ent.LastUsedAt) > s.ttl {
		expired = append(expired, *ent)
		delete(s.entries, key)
		s.mu.Unlock()
		s.notifyEvicted(expired)
		return CLISessionEntry{}, false
	}
	cp := *ent
	s.mu.Unlock()
	return cp, true
}

// Bind 写入或刷新「上游 session_id -> agy conversation_id (si)」绑定关系；
// 若同 key 切换到新 convID 或触发 LRU/TTL 淘汰，将自动触发旧 convID 磁盘清理。
func (s *CLISessionStore) Bind(upstreamSessionID, agyConversationID, model string, msgCount int) {
	if s == nil {
		return
	}
	key := strings.TrimSpace(upstreamSessionID)
	convID := strings.TrimSpace(agyConversationID)
	if key == "" || convID == "" {
		return
	}
	now := time.Now().UTC()
	var evicted []CLISessionEntry

	s.mu.Lock()
	if existing, ok := s.entries[key]; ok && existing != nil {
		if existing.AgyConversationID != "" && existing.AgyConversationID != convID {
			evicted = append(evicted, *existing)
		}
		existing.AgyConversationID = convID
		if strings.TrimSpace(model) != "" {
			existing.Model = strings.TrimSpace(model)
		}
		existing.LastMessageCount = msgCount
		existing.TurnCount++
		existing.LastUsedAt = now
		s.mu.Unlock()
		s.notifyEvicted(evicted)
		return
	}

	if len(s.entries) >= s.maxSize {
		evicted = s.evictLocked(now)
	}
	s.entries[key] = &CLISessionEntry{
		UpstreamSessionID: key,
		AgyConversationID: convID,
		Model:             strings.TrimSpace(model),
		LastMessageCount:  msgCount,
		TurnCount:         1,
		CreatedAt:         now,
		LastUsedAt:        now,
	}
	s.mu.Unlock()

	s.notifyEvicted(evicted)
}

// Delete 移除单个上游 session_id 的会话绑定并触发对应 agy 磁盘文件清理。
func (s *CLISessionStore) Delete(upstreamSessionID string) bool {
	if s == nil {
		return false
	}
	key := strings.TrimSpace(upstreamSessionID)
	if key == "" {
		return false
	}
	s.mu.Lock()
	ent, ok := s.entries[key]
	if !ok {
		s.mu.Unlock()
		return false
	}
	var evicted []CLISessionEntry
	if ent != nil {
		evicted = append(evicted, *ent)
	}
	delete(s.entries, key)
	s.mu.Unlock()

	s.notifyEvicted(evicted)
	return true
}

// Clear 清空全部会话映射并触发全部关联 agy 磁盘文件清理。
func (s *CLISessionStore) Clear() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	n := len(s.entries)
	evicted := make([]CLISessionEntry, 0, n)
	for _, ent := range s.entries {
		if ent != nil {
			evicted = append(evicted, *ent)
		}
	}
	s.entries = make(map[string]*CLISessionEntry)
	s.locks = make(map[string]*sync.Mutex)
	s.mu.Unlock()

	s.notifyEvicted(evicted)
	return n
}

// SweepExpired 主动扫描并清理全部已过期的会话绑定（同步触发磁盘清理回调）。
func (s *CLISessionStore) SweepExpired() int {
	if s == nil {
		return 0
	}
	now := time.Now().UTC()
	var expired []CLISessionEntry
	s.mu.Lock()
	for k, ent := range s.entries {
		if ent == nil || now.Sub(ent.LastUsedAt) > s.ttl {
			if ent != nil {
				expired = append(expired, *ent)
			}
			delete(s.entries, k)
		}
	}
	s.mu.Unlock()

	s.notifyEvicted(expired)
	return len(expired)
}

// ActiveConversationIDs 返回当前仍在内存活跃表中的所有 agy conversation_id 集合。
func (s *CLISessionStore) ActiveConversationIDs() map[string]struct{} {
	out := make(map[string]struct{})
	if s == nil {
		return out
	}
	s.mu.Lock()
	for _, ent := range s.entries {
		if ent != nil && strings.TrimSpace(ent.AgyConversationID) != "" {
			out[strings.TrimSpace(ent.AgyConversationID)] = struct{}{}
		}
	}
	s.mu.Unlock()
	return out
}

// List 返回当前活跃的全部上游会话 -> agy conversation_id (si) 映射（按最近使用时间倒序）。
func (s *CLISessionStore) List() []CLISessionEntry {
	if s == nil {
		return []CLISessionEntry{}
	}
	now := time.Now().UTC()
	var expired []CLISessionEntry
	s.mu.Lock()
	out := make([]CLISessionEntry, 0, len(s.entries))
	for k, ent := range s.entries {
		if ent == nil || now.Sub(ent.LastUsedAt) > s.ttl {
			if ent != nil {
				expired = append(expired, *ent)
			}
			delete(s.entries, k)
			continue
		}
		out = append(out, *ent)
	}
	s.mu.Unlock()

	s.notifyEvicted(expired)

	sort.Slice(out, func(i, j int) bool {
		return out[i].LastUsedAt.After(out[j].LastUsedAt)
	})
	return out
}

func (s *CLISessionStore) evictLocked(now time.Time) []CLISessionEntry {
	var evicted []CLISessionEntry
	for k, ent := range s.entries {
		if ent == nil || now.Sub(ent.LastUsedAt) > s.ttl {
			if ent != nil {
				evicted = append(evicted, *ent)
			}
			delete(s.entries, k)
		}
	}
	if len(s.entries) < s.maxSize {
		return evicted
	}
	// 淘汰最久未使用的会话
	var oldestKey string
	var oldestTime time.Time
	for k, ent := range s.entries {
		if oldestKey == "" || ent.LastUsedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = ent.LastUsedAt
		}
	}
	if oldestKey != "" {
		if ent := s.entries[oldestKey]; ent != nil {
			evicted = append(evicted, *ent)
		}
		delete(s.entries, oldestKey)
	}
	return evicted
}

// ResolveAgyHomeDir 解析真实 agy CLI 运行所使用的 HOME 根目录：
// 若配置了隔离 AuthDir 则返回其绝对路径；否则回退当前进程用户主目录。
func ResolveAgyHomeDir(spec NodeSpec) string {
	if dir := strings.TrimSpace(spec.AuthDir); dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// IsSafeAgyConversationID 校验 conversation_id 是否为安全纯 ID（禁止路径分隔符、.. 与通配符）。
func IsSafeAgyConversationID(convID string) bool {
	s := strings.TrimSpace(convID)
	if !safeAgyConvIDRe.MatchString(s) {
		return false
	}
	return !strings.Contains(s, "..")
}

// CleanupConversationDiskArtifacts 精准清理单个 agy conversation_id 在磁盘上产生的全部会话持久化产物：
//   - <home>/.gemini/antigravity-cli/conversations/<convID>.db (.db-wal / .db-shm / .db-journal)
//   - <home>/.gemini/antigravity-cli/brain/<convID>/
//   - <home>/.gemini/antigravity-cli/annotations/<convID>*
//   - <home>/.gemini/antigravity-cli/implicit/<convID>*
//   - （同步兼容 .gemini/antigravity/ 同名子目录）
//
// 仅精准删除传入的单个 convID，绝不触碰其它会话或 OAuth 凭证文件。
func CleanupConversationDiskArtifacts(homeDir, convID string) (removedPaths int, freedBytes int64) {
	home := strings.TrimSpace(homeDir)
	id := strings.TrimSpace(convID)
	if home == "" || !IsSafeAgyConversationID(id) {
		return 0, 0
	}

	roots := []string{
		filepath.Join(home, ".gemini", "antigravity-cli"),
		filepath.Join(home, ".gemini", "antigravity"),
	}

	for _, root := range roots {
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}

		// 1. SQLite 主库及 WAL / SHM / Journal 文件
		convDir := filepath.Join(root, "conversations")
		for _, suffix := range []string{".db", ".db-wal", ".db-shm", ".db-journal", ".pb", ".json"} {
			p := filepath.Join(convDir, id+suffix)
			if sz, ok := removePathWithRetry(p); ok {
				removedPaths++
				freedBytes += sz
			}
		}

		// 2. brain/<convID> 完整目录
		if sz, ok := removePathWithRetry(filepath.Join(root, "brain", id)); ok {
			removedPaths++
			freedBytes += sz
		}

		// 3. annotations / implicit 中精确匹配 <convID> 或 <convID>.* 的产物
		for _, sub := range []string{"annotations", "implicit"} {
			subDir := filepath.Join(root, sub)
			entries, err := os.ReadDir(subDir)
			if err != nil {
				continue
			}
			prefix := id + "."
			for _, ent := range entries {
				name := ent.Name()
				if name == id || strings.HasPrefix(name, prefix) {
					if sz, ok := removePathWithRetry(filepath.Join(subDir, name)); ok {
						removedPaths++
						freedBytes += sz
					}
				}
			}
		}
	}

	if removedPaths > 0 {
		globalCleanedConversations.Add(1)
		globalFreedDiskBytes.Add(freedBytes)
	}
	return removedPaths, freedBytes
}

// SweepOrphanedConversationDiskArtifacts 扫描隔离容器专属 AuthDir 下残留的孤儿会话文件（如容器崩溃/OOM 遗留）并清理，
// 同时截断超大的 history.jsonl。
// 安全约束：调用方仅应对隔离容器/节点专属 AuthDir 调用，切勿对个人宿主机通用主目录调用。
func SweepOrphanedConversationDiskArtifacts(isolatedHomeDir string, activeConvIDs map[string]struct{}, minAge time.Duration) (removedConvs int, freedBytes int64) {
	home := strings.TrimSpace(isolatedHomeDir)
	if home == "" {
		return 0, 0
	}
	if minAge < 0 {
		minAge = 0
	}
	cutoff := time.Now().Add(-minAge)
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return 0, 0
	}

	candidates := make(map[string]struct{})

	// 收集 conversations/*.db 中的孤儿 ID
	if entries, err := os.ReadDir(filepath.Join(root, "conversations")); err == nil {
		for _, ent := range entries {
			name := ent.Name()
			if !strings.HasSuffix(name, ".db") {
				continue
			}
			convID := strings.TrimSuffix(name, ".db")
			if !IsSafeAgyConversationID(convID) {
				continue
			}
			if _, active := activeConvIDs[convID]; active {
				continue
			}
			if info, iErr := ent.Info(); iErr == nil && minAge > 0 && info.ModTime().After(cutoff) {
				continue
			}
			candidates[convID] = struct{}{}
		}
	}

	// 收集 brain/<convID> 中的孤儿 ID
	if entries, err := os.ReadDir(filepath.Join(root, "brain")); err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			convID := ent.Name()
			if !IsSafeAgyConversationID(convID) {
				continue
			}
			if _, active := activeConvIDs[convID]; active {
				continue
			}
			if info, iErr := ent.Info(); iErr == nil && minAge > 0 && info.ModTime().After(cutoff) {
				continue
			}
			candidates[convID] = struct{}{}
		}
	}

	for convID := range candidates {
		removed, sz := CleanupConversationDiskArtifacts(home, convID)
		if removed > 0 {
			removedConvs++
			freedBytes += sz
		}
	}

	// 防止隔离容器内 history.jsonl 长期无限增长
	histPath := filepath.Join(root, "history.jsonl")
	if st, err := os.Stat(histPath); err == nil && !st.IsDir() && st.Size() > maxHistoryFileBytes {
		prevSize := st.Size()
		if os.Truncate(histPath, 0) == nil {
			freedBytes += prevSize
			globalFreedDiskBytes.Add(prevSize)
		}
	}

	return removedConvs, freedBytes
}

func removePathWithRetry(path string) (int64, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, false
	}
	size := measurePathBytes(path, info)

	backoffs := []time.Duration{0, 50 * time.Millisecond, 120 * time.Millisecond, 250 * time.Millisecond}
	for _, wait := range backoffs {
		if wait > 0 {
			time.Sleep(wait)
		}
		if err := os.RemoveAll(path); err == nil {
			return size, true
		}
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			return size, true
		}
	}
	return 0, false
}

func measurePathBytes(path string, info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	if !info.IsDir() {
		return info.Size()
	}
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, fErr := d.Info(); fErr == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}
