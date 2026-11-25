package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setEnv 设置测试用的环境变量，自动在测试结束恢复。
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// clearRelatedEnv 清空可能影响 Load 的环境变量，保证测试与宿主机环境隔离。
func clearRelatedEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"DB_DRIVER", "DB_SQLITE_PATH", "SQLITE_PATH", "DATA_DIR",
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE",
		"JWT_SECRET", "ENCRYPTION_KEY", "ADMIN_API_KEY",
		"BOOTSTRAP_ADMIN_EMAIL", "BOOTSTRAP_ADMIN_PASSWORD",
		"WB_API_BASE", "CC_API_BASE", "WB_UPSTREAM_PROXY", "CC_UPSTREAM_PROXY",
		"CC_FIRST_TOKEN_TIMEOUT", "CC_STREAM_IDLE", "CC_NONSTREAM_IDLE",
		"AGY_DOCKER_IMAGE", "AGY_DATA_DIR",
		"WB_AUTO_HARVEST_ENABLED", "WB_AUTO_HARVEST_INTERVAL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

// validEnv 是一份能通过校验的显式 Postgres 配置。
func validEnv() map[string]string {
	return map[string]string{
		"DB_PASSWORD":              "dbpass",
		"JWT_SECRET":               "0123456789abcdefghij",
		"ENCRYPTION_KEY":           strings.Repeat("ab", 32), // 64 位十六进制
		"BOOTSTRAP_ADMIN_PASSWORD": "adminpass123",
	}
}

func TestLoadValid(t *testing.T) {
	clearRelatedEnv(t)
	setEnv(t, validEnv())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	if cfg.Database.Driver != "postgres" {
		t.Errorf("设置 DB_PASSWORD 且未设置 DB_DRIVER 时应启用 postgres，得到 %q", cfg.Database.Driver)
	}
	if len(cfg.Auth.EncryptionKey) != 32 {
		t.Errorf("ENCRYPTION_KEY 应解出 32 字节，得到 %d", len(cfg.Auth.EncryptionKey))
	}
	if cfg.CommandCC.BaseURL != "https://www.codebuddy.cn" {
		t.Errorf("上游默认地址不对: %s", cfg.CommandCC.BaseURL)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("默认端口应为 8080，得到 %d", cfg.Server.Port)
	}
	if !cfg.CommandCC.EmptySystemPlaceholder {
		t.Error("CC_EMPTY_SYSTEM_PLACEHOLDER 应默认为 true")
	}
	if !cfg.CommandCC.AutoHarvestEnabled {
		t.Error("WB_AUTO_HARVEST_ENABLED 应默认为 true")
	}
	if cfg.CommandCC.AutoHarvestInterval != 6*time.Hour {
		t.Errorf("WB_AUTO_HARVEST_INTERVAL 应默认为 6h，得到 %v", cfg.CommandCC.AutoHarvestInterval)
	}
}

func TestLoadZeroConfigSQLiteAndSecretsPersistence(t *testing.T) {
	clearRelatedEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("DATA_DIR", tmpDir)

	cfg1, err := Load()
	if err != nil {
		t.Fatalf("零配置开箱启动不应失败: %v", err)
	}
	if cfg1.Database.Driver != "sqlite" {
		t.Fatalf("默认驱动应为 sqlite，得到 %q", cfg1.Database.Driver)
	}
	if cfg1.Database.DataDir != tmpDir {
		t.Errorf("DataDir = %q, want %q", cfg1.Database.DataDir, tmpDir)
	}
	wantDB := filepath.Join(tmpDir, "analy2api.db")
	if cfg1.Database.SQLitePath != wantDB {
		t.Errorf("SQLitePath = %q, want %q", cfg1.Database.SQLitePath, wantDB)
	}
	if cfg1.Bootstrap.Email != "admin@analy2api.local" {
		t.Errorf("默认管理员邮箱不对: %q", cfg1.Bootstrap.Email)
	}
	if cfg1.Bootstrap.Password != "admin123456" {
		t.Errorf("默认管理员密码不对: %q", cfg1.Bootstrap.Password)
	}
	if len(cfg1.Auth.JWTSecret) < 16 {
		t.Errorf("自动生成 JWT_SECRET 长度不足: %d", len(cfg1.Auth.JWTSecret))
	}
	if len(cfg1.Auth.EncryptionKey) != 32 {
		t.Errorf("自动生成 ENCRYPTION_KEY 应为 32 字节: %d", len(cfg1.Auth.EncryptionKey))
	}

	secretsPath := filepath.Join(tmpDir, ".secrets.json")
	if _, err := os.Stat(secretsPath); err != nil {
		t.Fatalf("应自动持久化生成 %s: %v", secretsPath, err)
	}

	// 再次加载应复用同一个持久化密钥。
	cfg2, err := Load()
	if err != nil {
		t.Fatalf("二次加载不应失败: %v", err)
	}
	if cfg2.Auth.JWTSecret != cfg1.Auth.JWTSecret {
		t.Errorf("二次加载 JWT_SECRET 不一致")
	}
	if string(cfg2.Auth.EncryptionKey) != string(cfg1.Auth.EncryptionKey) {
		t.Errorf("二次加载 ENCRYPTION_KEY 不一致")
	}
}

func TestLoadDefaultSQLitePathWhenDataDirUnset(t *testing.T) {
	clearRelatedEnv(t)
	// 提供明确密钥，避免在源码目录生成 ./data/.secrets.json。
	setEnv(t, map[string]string{
		"JWT_SECRET":     "0123456789abcdefghij",
		"ENCRYPTION_KEY": strings.Repeat("ab", 32),
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("默认 Driver 应为 sqlite，得到 %q", cfg.Database.Driver)
	}
	if cfg.Database.DataDir != "./data" {
		t.Errorf("默认 DataDir 应为 ./data，得到 %q", cfg.Database.DataDir)
	}
	if cfg.Database.SQLitePath != "./data/analy2api.db" && cfg.Database.SQLitePath != filepath.Join("data", "cmdwb2api.db") {
		t.Errorf("默认 SQLitePath 应为 ./data/analy2api.db（或兼容既有 cmdwb2api.db），得到 %q", cfg.Database.SQLitePath)
	}
}

func TestDriverSelection(t *testing.T) {
	clearRelatedEnv(t)
	setEnv(t, map[string]string{
		"DB_DRIVER":      "sqlite",
		"DB_PASSWORD":    "ignored-in-sqlite-mode",
		"JWT_SECRET":     "0123456789abcdefghij",
		"ENCRYPTION_KEY": strings.Repeat("ab", 32),
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("显式 DB_DRIVER=sqlite 不应失败: %v", err)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("Driver = %q, want sqlite", cfg.Database.Driver)
	}
}

// TestEncryptionKeyParsing 覆盖十六进制与裸字节两种写法。
func TestEncryptionKeyParsing(t *testing.T) {
	clearRelatedEnv(t)
	hexEnv := validEnv()
	hexEnv["ENCRYPTION_KEY"] = strings.Repeat("00", 32)
	setEnv(t, hexEnv)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("64 位十六进制应被接受: %v", err)
	}
	if cfg.Auth.EncryptionKey[0] != 0 {
		t.Error("十六进制解析结果不对")
	}

	// 32 个字符的裸串也接受。
	rawEnv := validEnv()
	rawEnv["ENCRYPTION_KEY"] = strings.Repeat("k", 32)
	setEnv(t, rawEnv)
	if _, err := Load(); err != nil {
		t.Fatalf("32 字节裸串应被接受: %v", err)
	}
}

// TestLoadRejectsBadConfig 逐项验证显式传入错误配置都被拦下，
// 且错误信息里要指出是哪个变量有问题。
func TestLoadRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]string)
		wantVar string
	}{
		{
			name:    "加密密钥长度不对",
			mutate:  func(m map[string]string) { m["ENCRYPTION_KEY"] = "tooshort" },
			wantVar: "ENCRYPTION_KEY",
		},
		{
			name:    "加密密钥十六进制非法",
			mutate:  func(m map[string]string) { m["ENCRYPTION_KEY"] = strings.Repeat("zz", 32) },
			wantVar: "ENCRYPTION_KEY",
		},
		{
			name:    "JWT 密钥太短",
			mutate:  func(m map[string]string) { m["JWT_SECRET"] = "short" },
			wantVar: "JWT_SECRET",
		},
		{
			name: "显式 postgres 缺少数据库口令",
			mutate: func(m map[string]string) {
				m["DB_DRIVER"] = "postgres"
				delete(m, "DB_PASSWORD")
			},
			wantVar: "DB_PASSWORD",
		},
		{
			name:    "非法数据库驱动",
			mutate:  func(m map[string]string) { m["DB_DRIVER"] = "mysql" },
			wantVar: "DB_DRIVER",
		},
		{
			name:    "管理员初始密码太短",
			mutate:  func(m map[string]string) { m["BOOTSTRAP_ADMIN_PASSWORD"] = "short" },
			wantVar: "BOOTSTRAP_ADMIN_PASSWORD",
		},
		{
			name:    "不支持的代理协议",
			mutate:  func(m map[string]string) { m["WB_UPSTREAM_PROXY"] = "ftp://127.0.0.1:21" },
			wantVar: "WB_UPSTREAM_PROXY",
		},
		{
			name:    "代理缺少主机名",
			mutate:  func(m map[string]string) { m["CC_UPSTREAM_PROXY"] = "socks5://" },
			wantVar: "CC_UPSTREAM_PROXY",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearRelatedEnv(t)
			envMap := validEnv()
			tc.mutate(envMap)
			setEnv(t, envMap)

			_, err := Load()
			if err == nil {
				t.Fatal("应当报错但没有")
			}
			if !strings.Contains(err.Error(), tc.wantVar) {
				t.Errorf("错误信息里应提到 %s，实际是: %v", tc.wantVar, err)
			}
		})
	}
}

func TestUpstreamProxyPrecedenceAndSchemes(t *testing.T) {
	for _, proxyURL := range []string{
		"http://127.0.0.1:7890",
		"https://user:pass@proxy.example.com:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://user:pass@127.0.0.1:1081",
		"direct",
	} {
		clearRelatedEnv(t)
		envMap := validEnv()
		envMap["CC_UPSTREAM_PROXY"] = "http://fallback:8080"
		envMap["WB_UPSTREAM_PROXY"] = proxyURL
		setEnv(t, envMap)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("代理 %q 应当合法: %v", proxyURL, err)
		}
		if cfg.CommandCC.UpstreamProxy != proxyURL {
			t.Errorf("WB_UPSTREAM_PROXY 应优先于 CC_UPSTREAM_PROXY，得到 %q, want %q", cfg.CommandCC.UpstreamProxy, proxyURL)
		}
	}

	clearRelatedEnv(t)
	envMap := validEnv()
	envMap["CC_UPSTREAM_PROXY"] = "socks5://127.0.0.1:1080"
	setEnv(t, envMap)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("回退 CC_UPSTREAM_PROXY 不应失败: %v", err)
	}
	if cfg.CommandCC.UpstreamProxy != "socks5://127.0.0.1:1080" {
		t.Errorf("应回退 CC_UPSTREAM_PROXY，得到 %q", cfg.CommandCC.UpstreamProxy)
	}
}

// TestDSN 验证连接串拼接。
func TestDSN(t *testing.T) {
	db := DatabaseConfig{
		Host: "postgres", Port: 5432, User: "u",
		Password: "p", Name: "n", SSLMode: "disable",
	}
	want := "host=postgres port=5432 user=u password=p dbname=n sslmode=disable"
	if got := db.DSN(); got != want {
		t.Errorf("DSN 拼接不对:\n得到 %q\n期望 %q", got, want)
	}
}

// TestServerAddr 验证监听地址。
func TestServerAddr(t *testing.T) {
	s := ServerConfig{Host: "0.0.0.0", Port: 8080}
	if got := s.Addr(); got != "0.0.0.0:8080" {
		t.Errorf("Addr() = %q", got)
	}
}

// TestEnvDurationFallback 验证超时类的环境变量解析。
func TestEnvDurationFallback(t *testing.T) {
	clearRelatedEnv(t)
	envMap := validEnv()
	envMap["CC_FIRST_TOKEN_TIMEOUT"] = "invalid"
	envMap["CC_STREAM_IDLE"] = "not-a-duration"
	envMap["CC_NONSTREAM_IDLE"] = "-5s"
	envMap["WB_AUTO_HARVEST_INTERVAL"] = "bad"
	setEnv(t, envMap)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("超时格式错误不应导致启动失败: %v", err)
	}
	if cfg.CommandCC.FirstTokenTimeout != 300*time.Second {
		t.Errorf("FirstTokenTimeout 应退回默认 300s，得到 %v", cfg.CommandCC.FirstTokenTimeout)
	}
	if cfg.CommandCC.StreamIdleTimeout != 180*time.Second {
		t.Errorf("StreamIdleTimeout 应退回默认 180s，得到 %v", cfg.CommandCC.StreamIdleTimeout)
	}
	if cfg.CommandCC.NonStreamIdleTimeout != 600*time.Second {
		t.Errorf("NonStreamIdleTimeout 应退回默认 600s，得到 %v", cfg.CommandCC.NonStreamIdleTimeout)
	}
	if cfg.CommandCC.AutoHarvestInterval != 6*time.Hour {
		t.Errorf("应退回默认 6h，得到 %v", cfg.CommandCC.AutoHarvestInterval)
	}

	envMap["CC_FIRST_TOKEN_TIMEOUT"] = "7m"
	envMap["CC_STREAM_IDLE"] = "45s"
	envMap["CC_NONSTREAM_IDLE"] = "12m"
	envMap["WB_AUTO_HARVEST_INTERVAL"] = "2h"
	envMap["WB_AUTO_HARVEST_ENABLED"] = "false"
	setEnv(t, envMap)
	cfg, _ = Load()
	if cfg.CommandCC.FirstTokenTimeout != 7*time.Minute {
		t.Errorf("FirstTokenTimeout 应解析为 7m，得到 %v", cfg.CommandCC.FirstTokenTimeout)
	}
	if cfg.CommandCC.StreamIdleTimeout != 45*time.Second {
		t.Errorf("StreamIdleTimeout 应解析为 45s，得到 %v", cfg.CommandCC.StreamIdleTimeout)
	}
	if cfg.CommandCC.NonStreamIdleTimeout != 12*time.Minute {
		t.Errorf("NonStreamIdleTimeout 应解析为 12m，得到 %v", cfg.CommandCC.NonStreamIdleTimeout)
	}
	if cfg.CommandCC.AutoHarvestInterval != 2*time.Hour {
		t.Errorf("应解析为 2h，得到 %v", cfg.CommandCC.AutoHarvestInterval)
	}
	if cfg.CommandCC.AutoHarvestEnabled {
		t.Error("WB_AUTO_HARVEST_ENABLED 应解析为 false")
	}
}

func TestTieredTimeoutsAdminKeyAndAntigravityDefaults(t *testing.T) {
	clearRelatedEnv(t)
	tmpDir := t.TempDir()
	envMap := validEnv()
	envMap["DATA_DIR"] = tmpDir
	setEnv(t, envMap)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 失败: %v", err)
	}
	if cfg.CommandCC.FirstTokenTimeout != 300*time.Second {
		t.Errorf("默认 FirstTokenTimeout = %v, want 5m (300s)", cfg.CommandCC.FirstTokenTimeout)
	}
	if cfg.CommandCC.StreamIdleTimeout != 180*time.Second {
		t.Errorf("默认 StreamIdleTimeout = %v, want 3m (180s)", cfg.CommandCC.StreamIdleTimeout)
	}
	if cfg.CommandCC.NonStreamIdleTimeout != 600*time.Second {
		t.Errorf("默认 NonStreamIdleTimeout = %v, want 10m (600s)", cfg.CommandCC.NonStreamIdleTimeout)
	}
	if cfg.Auth.BootstrapAdminAPIKey != "" {
		t.Errorf("默认 BootstrapAdminAPIKey 应为空，得到 %q", cfg.Auth.BootstrapAdminAPIKey)
	}
	if cfg.AntigravityDefaultImage != "analy2api-agy-cli:latest" || cfg.CommandCC.AntigravityDefaultImage != "analy2api-agy-cli:latest" {
		t.Errorf("默认 AntigravityDefaultImage 不对: %q / %q", cfg.AntigravityDefaultImage, cfg.CommandCC.AntigravityDefaultImage)
	}
	wantAgyDir := filepath.Join(tmpDir, "antigravity")
	if cfg.AntigravityDataDir != wantAgyDir || cfg.CommandCC.AntigravityDataDir != wantAgyDir {
		t.Errorf("默认 AntigravityDataDir = %q, want %q", cfg.AntigravityDataDir, wantAgyDir)
	}

	// 验证显式环境变量覆盖
	customAgyDir := filepath.Join(tmpDir, "custom-agy")
	envMap["ADMIN_API_KEY"] = "adm-bootstrap-secret-123"
	envMap["AGY_DOCKER_IMAGE"] = "custom/cliproxyapi:v2"
	envMap["AGY_DATA_DIR"] = customAgyDir
	setEnv(t, envMap)

	cfg2, err := Load()
	if err != nil {
		t.Fatalf("二次 Load() 失败: %v", err)
	}
	if cfg2.Auth.BootstrapAdminAPIKey != "adm-bootstrap-secret-123" {
		t.Errorf("BootstrapAdminAPIKey = %q", cfg2.Auth.BootstrapAdminAPIKey)
	}
	if cfg2.AntigravityDefaultImage != "custom/cliproxyapi:v2" {
		t.Errorf("AntigravityDefaultImage = %q", cfg2.AntigravityDefaultImage)
	}
	if cfg2.AntigravityDataDir != customAgyDir {
		t.Errorf("AntigravityDataDir = %q, want %q", cfg2.AntigravityDataDir, customAgyDir)
	}
}
