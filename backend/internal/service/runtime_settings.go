package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/systemsetting"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/config"
	"cmd2api/internal/workbuddy"
)

const (
	SettingKeyFirstTokenSeconds      = "first_token_seconds"
	SettingKeyStreamIdleSeconds      = "stream_idle_seconds"
	SettingKeyNonStreamIdleSeconds   = "nonstream_idle_seconds"
	SettingKeyUpstreamProxy          = "upstream_proxy"
	SettingKeyEmptySystemPlaceholder = "empty_system_placeholder"
	SettingKeyAutoHarvestEnabled     = "auto_harvest_enabled"
	SettingKeyAutoHarvestInterval    = "auto_harvest_interval"
	SettingKeyAgyPromptMode          = "agy_prompt_mode"
)

var runtimeSettingKeys = []string{
	SettingKeyFirstTokenSeconds,
	SettingKeyStreamIdleSeconds,
	SettingKeyNonStreamIdleSeconds,
	SettingKeyUpstreamProxy,
	SettingKeyEmptySystemPlaceholder,
	SettingKeyAutoHarvestEnabled,
	SettingKeyAutoHarvestInterval,
	SettingKeyAgyPromptMode,
}

// RuntimeSettingsInput 对齐前端 RuntimeSettingsPayload，支持按秒数或时长字符串在线热更新。
type RuntimeSettingsInput struct {
	FirstTokenSeconds      *int
	FirstTokenTimeout      *string
	StreamIdleSeconds      *int
	StreamIdleTimeout      *string
	NonStreamIdleSeconds   *int
	NonStreamIdleTimeout   *string
	UpstreamProxy          *string
	EmptySystemPlaceholder *bool
	AutoHarvestEnabled     *bool
	AutoHarvestInterval    *string
	AgyPromptMode          *string
	Settings               map[string]string
}

// RuntimeSettingsService 管理数据库 system_settings 中的分级超时与运行态热参数。
type RuntimeSettingsService struct {
	mu     sync.Mutex
	db     *ent.Client
	logger *slog.Logger
}

// NewRuntimeSettingsService 构造 RuntimeSettingsService。
func NewRuntimeSettingsService(db *ent.Client, logger *slog.Logger) *RuntimeSettingsService {
	if logger == nil {
		logger = slog.Default()
	}
	return &RuntimeSettingsService{
		db:     db,
		logger: logger,
	}
}

// GetAll 返回 system_settings 中已持久化的运行态覆盖键值表。
func (s *RuntimeSettingsService) GetAll(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyIn(runtimeSettingKeys...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询运行态配置失败: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

// LoadIntoConfig 在进程启动期读取已持久化的运行态配置并覆盖写入内存 *config.Config。
func (s *RuntimeSettingsService) LoadIntoConfig(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	kv, err := s.GetAll(ctx)
	if err != nil {
		return err
	}
	applyRuntimeMapToConfig(cfg, kv)
	return nil
}

// Update 校验并持久化运行态与三级超时配置，同时热更新内存 *config.Config 与 wbClient 全局代理。
func (s *RuntimeSettingsService) Update(
	ctx context.Context,
	cfg *config.Config,
	wbClient *workbuddy.Client,
	in RuntimeSettingsInput,
) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	toSave := make(map[string]string)

	// 1. 首字超时
	if sec, ok, err := resolveTimeoutSeconds(in.FirstTokenSeconds, in.FirstTokenTimeout, in.Settings, "first_token_timeout", SettingKeyFirstTokenSeconds); err != nil {
		return nil, fmt.Errorf("首字等待超时无效: %w", err)
	} else if ok {
		toSave[SettingKeyFirstTokenSeconds] = strconv.Itoa(sec)
	}

	// 2. 流式分片空闲超时
	if sec, ok, err := resolveTimeoutSeconds(in.StreamIdleSeconds, in.StreamIdleTimeout, in.Settings, "stream_idle_timeout", SettingKeyStreamIdleSeconds); err != nil {
		return nil, fmt.Errorf("流式空闲超时无效: %w", err)
	} else if ok {
		toSave[SettingKeyStreamIdleSeconds] = strconv.Itoa(sec)
	}

	// 3. 非流式总超时
	if sec, ok, err := resolveTimeoutSeconds(in.NonStreamIdleSeconds, in.NonStreamIdleTimeout, in.Settings, "nonstream_idle_timeout", SettingKeyNonStreamIdleSeconds); err != nil {
		return nil, fmt.Errorf("非流式超时无效: %w", err)
	} else if ok {
		toSave[SettingKeyNonStreamIdleSeconds] = strconv.Itoa(sec)
	}

	// 4. 全局上游默认代理
	var proxyUpdated bool
	var normalizedProxy string
	if in.UpstreamProxy != nil {
		proxyUpdated = true
		normalizedProxy = strings.TrimSpace(*in.UpstreamProxy)
	} else if raw, exists := in.Settings[SettingKeyUpstreamProxy]; exists {
		proxyUpdated = true
		normalizedProxy = strings.TrimSpace(raw)
	}
	if proxyUpdated {
		if normalizedProxy != "" && !strings.EqualFold(normalizedProxy, "direct") {
			norm, pErr := workbuddy.NormalizeProxyURL(normalizedProxy)
			if pErr != nil {
				return nil, fmt.Errorf("全局默认上游代理无效: %w", pErr)
			}
			normalizedProxy = norm
		}
		toSave[SettingKeyUpstreamProxy] = normalizedProxy
	}

	// 5. 空 System 占位开关
	if in.EmptySystemPlaceholder != nil {
		toSave[SettingKeyEmptySystemPlaceholder] = strconv.FormatBool(*in.EmptySystemPlaceholder)
	} else if raw, exists := in.Settings[SettingKeyEmptySystemPlaceholder]; exists {
		if b, bErr := strconv.ParseBool(strings.TrimSpace(raw)); bErr == nil {
			toSave[SettingKeyEmptySystemPlaceholder] = strconv.FormatBool(b)
		}
	}

	// 6. 自动签到收割开关与周期
	if in.AutoHarvestEnabled != nil {
		toSave[SettingKeyAutoHarvestEnabled] = strconv.FormatBool(*in.AutoHarvestEnabled)
	} else if raw, exists := in.Settings[SettingKeyAutoHarvestEnabled]; exists {
		if b, bErr := strconv.ParseBool(strings.TrimSpace(raw)); bErr == nil {
			toSave[SettingKeyAutoHarvestEnabled] = strconv.FormatBool(b)
		}
	}

	if in.AutoHarvestInterval != nil && strings.TrimSpace(*in.AutoHarvestInterval) != "" {
		dur, dErr := parseFlexibleDuration(*in.AutoHarvestInterval)
		if dErr != nil || dur <= 0 {
			return nil, errors.New("自动签到轮询周期无效")
		}
		toSave[SettingKeyAutoHarvestInterval] = dur.String()
	} else if raw, exists := in.Settings[SettingKeyAutoHarvestInterval]; exists && strings.TrimSpace(raw) != "" {
		if dur, dErr := parseFlexibleDuration(raw); dErr == nil && dur > 0 {
			toSave[SettingKeyAutoHarvestInterval] = dur.String()
		}
	}

	// 7. AntiGravity AGY CLI 提示词运行管线模式热更新
	var rawAgyMode string
	var agyModeProvided bool
	if in.AgyPromptMode != nil {
		rawAgyMode = strings.TrimSpace(*in.AgyPromptMode)
		agyModeProvided = true
	} else if raw, exists := in.Settings[SettingKeyAgyPromptMode]; exists {
		rawAgyMode = strings.TrimSpace(raw)
		agyModeProvided = true
	}
	if agyModeProvided {
		if rawAgyMode != "" && !antigravity.IsValidPromptMode(rawAgyMode) {
			return nil, fmt.Errorf("无效的 AGY 提示词模式 %q（支持 default_intercept_continue | blend_sanitized | native_only | downstream_only）", rawAgyMode)
		}
		normMode := ""
		if rawAgyMode != "" {
			normMode = antigravity.NormalizePromptMode(rawAgyMode)
		}
		toSave[SettingKeyAgyPromptMode] = normMode
		antigravity.SetRuntimePromptMode(normMode)
	}

	// 持久化到 system_settings 表
	for k, v := range toSave {
		if err := s.upsertRuntimeKey(ctx, k, v); err != nil {
			return nil, fmt.Errorf("保存运行态参数 %q 失败: %w", k, err)
		}
	}

	// 热更新内存 Config 与 WorkBuddy 默认代理池
	if cfg != nil {
		applyRuntimeMapToConfig(cfg, toSave)
	}
	if proxyUpdated && wbClient != nil {
		if err := wbClient.SetDefaultProxy(normalizedProxy); err != nil {
			return nil, fmt.Errorf("热更新上游默认代理失败: %w", err)
		}
	}

	return s.GetAll(ctx)
}

func (s *RuntimeSettingsService) upsertRuntimeKey(ctx context.Context, key, val string) error {
	existing, err := s.db.SystemSetting.Query().
		Where(systemsetting.KeyEQ(key)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			_, cErr := s.db.SystemSetting.Create().
				SetKey(key).
				SetValue(val).
				SetDescription("Runtime override: " + key).
				Save(ctx)
			return cErr
		}
		return err
	}
	_, err = existing.Update().SetValue(val).Save(ctx)
	return err
}

func applyRuntimeMapToConfig(cfg *config.Config, kv map[string]string) {
	if cfg == nil || len(kv) == 0 {
		return
	}
	if raw, ok := kv[SettingKeyFirstTokenSeconds]; ok {
		if sec, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && sec > 0 {
			cfg.CommandCC.FirstTokenTimeout = time.Duration(sec) * time.Second
		}
	}
	if raw, ok := kv[SettingKeyStreamIdleSeconds]; ok {
		if sec, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && sec > 0 {
			cfg.CommandCC.StreamIdleTimeout = time.Duration(sec) * time.Second
		}
	}
	if raw, ok := kv[SettingKeyNonStreamIdleSeconds]; ok {
		if sec, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && sec > 0 {
			cfg.CommandCC.NonStreamIdleTimeout = time.Duration(sec) * time.Second
		}
	}
	if raw, ok := kv[SettingKeyUpstreamProxy]; ok {
		cfg.CommandCC.UpstreamProxy = strings.TrimSpace(raw)
	}
	if raw, ok := kv[SettingKeyEmptySystemPlaceholder]; ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			cfg.CommandCC.EmptySystemPlaceholder = b
		}
	}
	if raw, ok := kv[SettingKeyAutoHarvestEnabled]; ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			cfg.CommandCC.AutoHarvestEnabled = b
		}
	}
	if raw, ok := kv[SettingKeyAutoHarvestInterval]; ok {
		if dur, err := parseFlexibleDuration(raw); err == nil && dur > 0 {
			cfg.CommandCC.AutoHarvestInterval = dur
		}
	}
	if raw, ok := kv[SettingKeyAgyPromptMode]; ok && strings.TrimSpace(raw) != "" {
		if antigravity.IsValidPromptMode(raw) {
			antigravity.SetRuntimePromptMode(raw)
		}
	}
}

func resolveTimeoutSeconds(
	secPtr *int,
	durPtr *string,
	settingsMap map[string]string,
	altDurKey, secKey string,
) (int, bool, error) {
	if secPtr != nil {
		if *secPtr <= 0 {
			return 0, false, errors.New("超时秒数必须大于 0")
		}
		return *secPtr, true, nil
	}
	if durPtr != nil && strings.TrimSpace(*durPtr) != "" {
		dur, err := parseFlexibleDuration(*durPtr)
		if err != nil || dur <= 0 {
			return 0, false, errors.New("超时时间格式不合法")
		}
		sec := int(dur.Seconds())
		if sec <= 0 {
			sec = 1
		}
		return sec, true, nil
	}
	for _, k := range []string{secKey, altDurKey} {
		if raw, exists := settingsMap[k]; exists && strings.TrimSpace(raw) != "" {
			dur, err := parseFlexibleDuration(raw)
			if err != nil || dur <= 0 {
				return 0, false, fmt.Errorf("参数 %s 格式无效", k)
			}
			sec := int(dur.Seconds())
			if sec <= 0 {
				sec = 1
			}
			return sec, true, nil
		}
	}
	return 0, false, nil
}

func parseFlexibleDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if sec, err := strconv.Atoi(s); err == nil {
		return time.Duration(sec) * time.Second, nil
	}
	return time.ParseDuration(s)
}
