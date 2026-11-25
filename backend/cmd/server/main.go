// Command server 是 cmd2api 的后端服务入口。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// 内嵌时区数据库，保证 Alpine / scratch 容器内 TZ=Asia/Shanghai 正常生效。
	_ "time/tzdata"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/auth"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/filter"
	"cmd2api/internal/handler"
	"cmd2api/internal/prompt"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/server"
	"cmd2api/internal/service"
	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"
)

var (
	// AppName、Version、GitCommit 与 BuildTime 支持在发行编译阶段通过 -ldflags 注入。
	AppName   = "Analy2API"
	Version   = "1.0.0"
	GitCommit = "dev"
	BuildTime = ""
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v" || os.Args[1] == "version") {
		if BuildTime != "" {
			fmt.Printf("%s v%s (commit=%s, built=%s)\n", AppName, Version, GitCommit, BuildTime)
		} else {
			fmt.Printf("%s v%s (commit=%s)\n", AppName, Version, GitCommit)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "agy-bridge" {
		logger := newLogger()
		slog.SetDefault(logger)
		cfg := antigravity.BridgeConfig{
			ListenAddr:       os.Getenv("AGY_BRIDGE_ADDR"),
			CLIBinary:        os.Getenv("AGY_CLI_BIN"),
			AuthDir:          os.Getenv("AGY_AUTH_DIR"),
			APIKey:           os.Getenv("AGY_API_KEY"),
			ManagementKey:    os.Getenv("AGY_MANAGEMENT_KEY"),
			ProxyURL:         os.Getenv("AGY_PROXY_URL"),
			SessionRetention: os.Getenv("AGY_SESSION_RETENTION"),
			PromptBlendMode:  os.Getenv("AGY_PROMPT_BLEND_MODE"),
			NativePrompt:     os.Getenv("AGY_NATIVE_PROMPT"),
			Logger:           logger,
		}
		logger.Info("启动 Analy2API 单容器真实 agy CLI 桥接服务", "version", Version, "addr", cfg.ListenAddr, "auth_dir", cfg.AuthDir)
		if err := antigravity.RunAgyBridgeServer(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Analy2API agy-bridge 启动失败: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s 启动失败: %v\n", AppName, err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger()
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := db.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("关闭数据库连接失败", "err", err)
		}
	}()
	logger.Info("数据库已就绪", "driver", cfg.Database.Driver)

	// 1. 加载持久化的运行态覆盖配置（三级超时、默认代理、自动收割开关等）。
	runtimeSettingsSvc := service.NewRuntimeSettingsService(client, logger)
	if err := runtimeSettingsSvc.LoadIntoConfig(ctx, cfg); err != nil {
		logger.Warn("加载运行态持久化配置失败，使用环境变量默认值", "err", err)
	}

	logger.Info("Analy2API 正在启动",
		"version", Version,
		"commit", GitCommit,
		"addr", cfg.Server.Addr(),
		"upstream", cfg.CommandCC.BaseURL,
		"first_token_timeout", cfg.CommandCC.FirstTokenTimeout.String(),
		"stream_idle_timeout", cfg.CommandCC.StreamIdleTimeout.String(),
		"nonstream_idle_timeout", cfg.CommandCC.NonStreamIdleTimeout.String(),
	)

	vault, err := crypto.NewVault(cfg.Auth.EncryptionKey)
	if err != nil {
		return fmt.Errorf("初始化凭证加密: %w", err)
	}
	issuer := auth.NewIssuer(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)

	if err := service.BootstrapAdmin(ctx, client, cfg.Bootstrap, logger); err != nil {
		return err
	}
	if _, err := service.BootstrapDefaultGroup(ctx, client, logger); err != nil {
		return err
	}

	// 2. 初始化上游多平台客户端（共享代理池）。
	wbClient := workbuddy.NewClient(cfg.CommandCC.UpstreamProxy, cfg.CommandCC.BaseURL, logger)
	agClient := antigravity.NewClientWithPool(wbClient.ProxyPool(), logger)
	relayClient := relay.NewClient(cfg.CommandCC, logger)
	relayClient.SetWorkBuddyClient(wbClient)
	relayClient.SetAntigravityClient(agClient)

	// 3. 初始化内容风控引擎与前置系统提示词注入引擎。
	filterEngine := filter.NewDefaultEngine()
	filterService := service.NewFilterService(client, filterEngine, logger)
	if err := filterService.EnsureSeeded(ctx); err != nil {
		return fmt.Errorf("初始化内容风控规则库: %w", err)
	}

	promptEngine := prompt.NewDefaultEngine()
	promptService := service.NewPromptService(client, promptEngine, filterEngine, logger)
	if err := promptService.EnsureSeeded(ctx); err != nil {
		return fmt.Errorf("初始化前置系统提示词库: %w", err)
	}

	toolObfEngine := toolobf.NewDefaultEngine()
	toolObfService := service.NewToolObfuscationService(client, toolObfEngine, logger)
	if err := toolObfService.EnsureSeeded(ctx); err != nil {
		return fmt.Errorf("初始化工具名混淆配置: %w", err)
	}

	sessionGuard := relay.NewDefaultSessionGuard()
	sessionGuardService := service.NewSessionGuardService(client, sessionGuard, logger)
	if err := sessionGuardService.EnsureSeeded(ctx); err != nil {
		return fmt.Errorf("初始化非额度429会话保护配置: %w", err)
	}

	// 4. 初始化标准价格覆盖库与精确用量计费器。
	pricingService := service.NewPricingService(client, logger)
	if err := pricingService.LoadFromDB(ctx); err != nil {
		return fmt.Errorf("加载自定义模型定价库: %w", err)
	}
	recorder := service.NewRecorder(client, logger, service.WithPricingService(pricingService))

	// 5. 初始化调度器、账号服务、AntiGravity 容器服务、管理密钥服务、领奖中心与 Cron。
	sched := scheduler.New(client, vault, cfg.Health.FailureThreshold, logger)
	accountService := service.NewAccountService(client, vault, relayClient, sched, logger)
	adminKeyService := service.NewAdminKeyService(client)
	antigravityService := service.NewAntigravityService(
		client,
		vault,
		agClient,
		agClient.Docker(),
		cfg.AntigravityDataDir,
		cfg.AntigravityDefaultImage,
		logger,
	)
	accountService.SetAntigravityService(antigravityService)
	rewardsService := service.NewRewardsService(client, accountService, antigravityService, logger)
	oauthService := service.NewOAuthService(client, accountService, antigravityService, relayClient, logger)

	cronService := service.NewCronService(client, accountService, logger)
	if err := cronService.EnsureSeeded(ctx); err != nil {
		return fmt.Errorf("初始化 Cron 定时任务: %w", err)
	}

	relayService := relay.NewService(
		relayClient,
		sched,
		recorder,
		cfg,
		logger,
		relay.WithFilterEngine(filterEngine),
		relay.WithPromptEngine(promptEngine),
		relay.WithToolObfuscator(toolObfEngine),
		relay.WithSessionGuard(sessionGuard),
		relay.WithWorkBuddyClient(wbClient),
		relay.WithAntigravityClient(agClient),
		relay.WithTokenUpdater(accountService),
	)

	if cfg.Health.Enabled {
		go accountService.RunHealthChecks(ctx, cfg.Health.Interval)
	} else {
		logger.Info("账号健康检查已关闭")
	}

	if cfg.CommandCC.AutoHarvestEnabled {
		go accountService.RunAutoHarvest(ctx, cfg.CommandCC.AutoHarvestInterval, true)
	}

	go cronService.Start(ctx)

	h := handler.New(client, vault, issuer, sched, relayService, accountService, cfg, logger, filterService)
	h.SetCronService(cronService)
	h.SetPricingService(pricingService)
	h.SetPromptService(promptService)
	h.SetAdminKeyService(adminKeyService)
	h.SetAntigravityService(antigravityService)
	h.SetRewardsService(rewardsService)
	h.SetRuntimeSettingsService(runtimeSettingsSvc)
	h.SetToolObfuscationService(toolObfService)
	h.SetSessionGuardService(sessionGuardService)
	h.SetOAuthService(oauthService)

	router := server.NewRouter(server.Options{
		Handler:              h,
		Issuer:               issuer,
		Client:               client,
		BootstrapAdminAPIKey: cfg.Auth.BootstrapAdminAPIKey,
		StaticDir:            staticDir(),
		Mode:                 cfg.Server.Mode,
		Version:              Version,
	})

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr(),
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务已监听", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	case <-ctx.Done():
		logger.Info("收到退出信号，正在优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("优雅关闭超时，强制退出", "err", err)
	}
	logger.Info("已关闭")
	return nil
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}

func staticDir() string {
	if dir := os.Getenv("STATIC_DIR"); dir != "" {
		return dir
	}
	candidates := []string{
		"./web",
		"./frontend/dist",
		"../frontend/dist",
		"../../frontend/dist",
	}
	for _, dir := range candidates {
		if info, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && !info.IsDir() {
			return dir
		}
	}
	return "./web"
}
