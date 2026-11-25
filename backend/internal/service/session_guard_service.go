package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"cmd2api/ent"
	"cmd2api/ent/systemsetting"
	"cmd2api/internal/relay"
)

const (
	// SettingKeySessionGuardConfig 存储 relay.SessionGuardConfig 的 JSON 配置。
	SettingKeySessionGuardConfig = "session_guard_config"
)

// SessionGuardView 汇总会话暂停保护配置、当前暂停中的会话及最近的 429 请求体日志列表。
type SessionGuardView struct {
	Config         relay.SessionGuardConfig  `json:"config"`
	PausedSessions []relay.PausedSessionInfo `json:"paused_sessions"`
	LogFiles       []relay.Session429LogFile `json:"log_files"`
	PausedCount    int                       `json:"paused_count"`
	LogFilesCount  int                       `json:"log_files_count"`
}

// SessionGuardService 管理非额度 429 会话暂停配置持久化、热生效及审计日志查询。
type SessionGuardService struct {
	mu     sync.Mutex
	db     *ent.Client
	guard  *relay.SessionGuard
	logger *slog.Logger
}

// NewSessionGuardService 构造 SessionGuardService。
func NewSessionGuardService(db *ent.Client, guard *relay.SessionGuard, logger *slog.Logger) *SessionGuardService {
	if guard == nil {
		guard = relay.NewDefaultSessionGuard()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionGuardService{
		db:     db,
		guard:  guard,
		logger: logger,
	}
}

// Guard 返回关联的内存 SessionGuard 实例。
func (s *SessionGuardService) Guard() *relay.SessionGuard {
	if s == nil {
		return nil
	}
	return s.guard
}

// EnsureSeeded 从数据库读取已保存的会话保护配置；缺失时持久化默认配置并热同步。
func (s *SessionGuardService) EnsureSeeded(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}
	row, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(SettingKeySessionGuardConfig)).
		Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("查询非额度429会话保护配置失败: %w", err)
		}
		def := relay.DefaultSessionGuardConfig()
		if err := s.persistLocked(ctx, def); err != nil {
			return fmt.Errorf("初始化默认会话保护配置失败: %w", err)
		}
		s.guard.Reload(def)
		return nil
	}

	var loaded relay.SessionGuardConfig
	if uErr := json.Unmarshal([]byte(row.Value), &loaded); uErr != nil {
		loaded = relay.DefaultSessionGuardConfig()
		_ = s.persistLocked(ctx, loaded)
	} else {
		loaded = relay.NormalizeSessionGuardConfig(loaded)
	}
	s.guard.Reload(loaded)
	return nil
}

// GetView 返回当前配置、实时暂停中的会话与 429 请求体日志目录明细。
func (s *SessionGuardService) GetView(_ context.Context) SessionGuardView {
	if s == nil || s.guard == nil {
		def := relay.DefaultSessionGuardConfig()
		return SessionGuardView{
			Config:         def,
			PausedSessions: []relay.PausedSessionInfo{},
			LogFiles:       []relay.Session429LogFile{},
		}
	}
	paused := s.guard.PausedSessions()
	logs, _ := s.guard.ListLogFiles(80)
	if logs == nil {
		logs = []relay.Session429LogFile{}
	}
	return SessionGuardView{
		Config:         s.guard.Config(),
		PausedSessions: paused,
		LogFiles:       logs,
		PausedCount:    len(paused),
		LogFilesCount:  len(logs),
	}
}

// UpdateConfig 持久化并零重启热更新非额度 429 会话保护全部参数。
func (s *SessionGuardService) UpdateConfig(ctx context.Context, cfg relay.SessionGuardConfig) (SessionGuardView, error) {
	if s == nil {
		return SessionGuardView{}, errors.New("会话保护服务未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	norm := relay.NormalizeSessionGuardConfig(cfg)
	if s.db != nil {
		if err := s.persistLocked(ctx, norm); err != nil {
			return SessionGuardView{}, fmt.Errorf("保存会话保护配置失败: %w", err)
		}
	}
	s.guard.Reload(norm)
	if s.logger != nil {
		s.logger.Info("已更新非额度429会话暂停保护配置",
			"enabled", norm.Enabled,
			"pause_seconds", norm.PauseSeconds,
			"log_enabled", norm.LogEnabled,
			"log_dir", norm.LogDir,
		)
	}
	return s.GetView(ctx), nil
}

func (s *SessionGuardService) persistLocked(ctx context.Context, cfg relay.SessionGuardConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	existing, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(SettingKeySessionGuardConfig)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			_, cErr := s.db.SystemSetting.Create().
				SetKey(SettingKeySessionGuardConfig).
				SetValue(string(raw)).
				SetDescription("Non-quota 429 session pause guard and request body log configuration").
				Save(ctx)
			return cErr
		}
		return err
	}
	_, err = existing.Update().SetValue(string(raw)).Save(ctx)
	return err
}
