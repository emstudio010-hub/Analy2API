package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// BridgeConfig 描述单容器独立真实 agy CLI 客户端桥接服务 (`cmdwb2api agy-bridge`) 的运行配置。
// 每个容器内只运行一个完整的真实 agy CLI 实例，通过本桥接对外暴露标准 OpenAI /v1 与管理 /v0/management 接口。
type BridgeConfig struct {
	ListenAddr       string
	CLIBinary        string
	AuthDir          string
	APIKey           string
	ManagementKey    string
	ProxyURL         string
	SessionRetention string        // "stateless"（默认，每轮请求后立即清理磁盘）| "ephemeral"（短期复用并在驱逐时清理）
	PromptBlendMode  string        // "blend_clean"（默认，净化版 AGY 原生 + 后接下游提示词）| "verbatim_only" | "native_raw"
	NativePrompt     string        // 可选自定义 AGY 原生底本（blend_clean 下会自动删误导段）
	SessionTTL       time.Duration // ephemeral 模式下的会话过期时间（默认 30 分钟）
	MaxSessions      int           // ephemeral 模式下的最大会话数（默认 256）
	Logger           *slog.Logger
}

// NewAgyBridgeHandler 构建单容器真实 agy CLI 桥接 HTTP Handler，供 `cmdwb2api agy-bridge` 与单元测试共用。
func NewAgyBridgeHandler(cfg BridgeConfig) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	authDir := strings.TrimSpace(cfg.AuthDir)
	retention := strings.TrimSpace(cfg.SessionRetention)
	if retention == "" {
		retention = os.Getenv(AgySessionRetentionEnv)
	}
	retention = NormalizeSessionRetention(retention)

	promptBlendMode := strings.TrimSpace(cfg.PromptBlendMode)
	if promptBlendMode == "" {
		promptBlendMode = os.Getenv(AgyPromptBlendModeEnv)
	}
	promptBlendMode = NormalizePromptBlendMode(promptBlendMode)

	nativePrompt := strings.TrimSpace(cfg.NativePrompt)
	if nativePrompt == "" {
		nativePrompt = strings.TrimSpace(os.Getenv(AgyNativePromptEnv))
	}

	spec := NodeSpec{
		Mode:             ModeLocalCLI,
		CLIBinary:        strings.TrimSpace(cfg.CLIBinary),
		AuthDir:          authDir,
		ProxyURL:         strings.TrimSpace(cfg.ProxyURL),
		APIKey:           strings.TrimSpace(cfg.APIKey),
		ManagementKey:    strings.TrimSpace(cfg.ManagementKey),
		SessionRetention: retention,
		PromptBlendMode:  promptBlendMode,
		NativePrompt:     nativePrompt,
	}
	homeDir := ResolveAgyHomeDir(spec)

	var (
		bridgeCleanedCount atomic.Int64
		bridgeFreedBytes   atomic.Int64

		modelsMu       sync.Mutex
		modelsFetchMu  sync.Mutex // 并发防惊群：避免多客户端同时冷启多个 `agy models` 子进程
		cachedModels   []ModelItem
		modelsCachedAt time.Time
	)

	sessStore := NewCLISessionStore(cfg.SessionTTL, cfg.MaxSessions)
	sessStore.SetOnEvict(func(ent CLISessionEntry) {
		removed, freed := CleanupConversationDiskArtifacts(homeDir, ent.AgyConversationID)
		if removed > 0 {
			bridgeCleanedCount.Add(1)
			bridgeFreedBytes.Add(freed)
		}
	})

	// 仅在隔离容器/节点专属 AuthDir 下：
	//  1. 自动写入系统级反代规范 <authDir>/.gemini/GEMINI.md（使真实 agy CLI 从系统级 <user_rules> 屏蔽 IDE 误导段并优先遵循下游提示词）
	//  2. 启动孤儿磁盘清理（启动清一次 + 每 10 分钟巡检，彻底防止异常崩溃残留撑爆磁盘）
	if authDir != "" {
		EnsureIsolatedAgyProxyRules(authDir, promptBlendMode, nativePrompt)
		go func() {
			minAge := 2 * time.Minute
			if retention == SessionRetentionStateless {
				minAge = 30 * time.Second
			}
			if n, freed := SweepOrphanedConversationDiskArtifacts(homeDir, sessStore.ActiveConversationIDs(), minAge); n > 0 {
				bridgeCleanedCount.Add(int64(n))
				bridgeFreedBytes.Add(freed)
				logger.Info("启动时已清理孤儿 agy 会话磁盘文件", "conversations", n, "freed_bytes", freed)
			}
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				sessStore.SweepExpired()
				if n, freed := SweepOrphanedConversationDiskArtifacts(homeDir, sessStore.ActiveConversationIDs(), 5*time.Minute); n > 0 {
					bridgeCleanedCount.Add(int64(n))
					bridgeFreedBytes.Add(freed)
					logger.Info("定时巡检已清理孤儿 agy 会话磁盘文件", "conversations", n, "freed_bytes", freed)
				}
			}
		}()
	}

	checkAuth := func(r *http.Request, expected string, altHeader string) bool {
		if expected == "" {
			return true
		}
		if altHeader != "" && strings.TrimSpace(r.Header.Get(altHeader)) == expected {
			return true
		}
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			return strings.TrimSpace(auth[7:]) == expected
		}
		return auth == expected
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		bin, err := ResolveAgyCLIBinary(spec.CLIBinary)
		globalCleaned, globalFreed := GlobalDiskCleanupStats()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":                "ok",
			"service":               "Analy2API-agy-bridge",
			"driver":                "real-agy-cli",
			"single_client":         true,
			"cli_binary":            bin,
			"cli_ready":             err == nil,
			"session_retention":     retention,
			"prompt_blend_mode":     promptBlendMode,
			"prompt_passthrough":    "verbatim",
			"adaptive_tools":        true,
			"active_sessions":       len(sessStore.List()),
			"cleaned_conversations": globalCleaned,
			"freed_bytes":           globalFreed,
			"time":                  time.Now().UTC().Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, spec.APIKey, "") {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		forceRefresh := r.URL.Query().Get("refresh") == "1"
		modelsMu.Lock()
		var models []ModelItem
		if !forceRefresh && len(cachedModels) > 0 && time.Since(modelsCachedAt) < 5*time.Minute {
			models = cachedModels
		}
		modelsMu.Unlock()

		if len(models) == 0 {
			modelsFetchMu.Lock()
			// 双重检查缓存：等待期间若有并发请求已拉取成功则直接复用
			modelsMu.Lock()
			if !forceRefresh && len(cachedModels) > 0 && time.Since(modelsCachedAt) < 5*time.Minute {
				models = cachedModels
			}
			modelsMu.Unlock()

			if len(models) == 0 {
				fetched, err := ListModelsViaCLI(r.Context(), spec)
				if err != nil || len(fetched) == 0 {
					logger.Warn("agy models 调用回退内置列表", "err", err)
					models = DefaultModels()
				} else {
					models = fetched
					modelsMu.Lock()
					cachedModels = fetched
					modelsCachedAt = time.Now()
					modelsMu.Unlock()
				}
			}
			modelsFetchMu.Unlock()
		}
		data := make([]map[string]any, 0, len(models))
		for _, m := range models {
			data = append(data, map[string]any{
				"id":           m.ID,
				"object":       "model",
				"display_name": m.Name,
				"owned_by":     m.Vendor,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   data,
		})
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":{"message":"method not allowed"}}`, http.StatusMethodNotAllowed)
			return
		}
		if !checkAuth(r, spec.APIKey, "") {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, `{"error":{"message":"read body failed"}}`, http.StatusBadRequest)
			return
		}
		upstreamSID := firstNonEmptyStr(
			r.Header.Get("X-Session-Id"),
			r.Header.Get("X-Conversation-Id"),
			r.Header.Get("Conversation-Id"),
			r.Header.Get("X-Thread-Id"),
		)
		reqSpec := spec
		if hdrRetention := strings.TrimSpace(r.Header.Get("X-Agy-Session-Retention")); hdrRetention != "" {
			reqSpec.SessionRetention = NormalizeSessionRetention(hdrRetention)
		}
		if hdrBlend := strings.TrimSpace(r.Header.Get("X-Agy-Prompt-Blend-Mode")); hdrBlend != "" {
			reqSpec.PromptBlendMode = NormalizePromptBlendMode(hdrBlend)
		}

		resp, _, err := ChatStreamViaCLIWithSession(r.Context(), reqSpec, body, upstreamSID, sessStore)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": err.Error()}})
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Agy-Driver", "real-agy-cli")
		for _, hk := range []string{
			"X-Agy-Retention-Mode",
			"X-Agy-Upstream-Session",
			"X-Agy-Session-Mode",
			"X-Agy-Conversation-Id",
			"X-Agy-Tool-Tier",
			"X-Agy-Tool-Count",
			"X-Agy-Prompt-Passthrough",
			"X-Agy-Prompt-Mode",
		} {
			if v := resp.Header.Get(hk); v != "" {
				w.Header().Set(hk, v)
			}
		}
		flusher, _ := w.(http.Flusher)

		buf := make([]byte, 4096)
		for {
			n, rErr := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if rErr != nil {
				break
			}
		}
	})

	// 查询 / 重置单容器内「上游 session_id -> agy conversation_id (si)」映射与磁盘清理统计
	mux.HandleFunc("/v0/management/sessions", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, spec.ManagementKey, "X-Management-Key") {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			list := sessStore.List()
			globalCleaned, globalFreed := GlobalDiskCleanupStats()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"retention_mode":        retention,
				"sessions":              list,
				"count":                 len(list),
				"cleaned_conversations": globalCleaned,
				"freed_bytes":           globalFreed,
				"bridge_evicted":        bridgeCleanedCount.Load(),
				"bridge_freed_bytes":    bridgeFreedBytes.Load(),
			})
		case http.MethodDelete:
			sid := strings.TrimSpace(r.URL.Query().Get("session_id"))
			purgeDisk := r.URL.Query().Get("purge_disk") == "1" || strings.EqualFold(r.URL.Query().Get("purge_disk"), "true")
			var sweptConvs int
			var sweptBytes int64
			if sid != "" {
				removed := sessStore.Delete(sid)
				if purgeDisk && authDir != "" {
					sweptConvs, sweptBytes = SweepOrphanedConversationDiskArtifacts(homeDir, sessStore.ActiveConversationIDs(), 0)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":          true,
					"removed":     removed,
					"session_id":  sid,
					"swept_convs": sweptConvs,
					"swept_bytes": sweptBytes,
				})
				return
			}
			cleared := sessStore.Clear()
			if purgeDisk && authDir != "" {
				sweptConvs, sweptBytes = SweepOrphanedConversationDiskArtifacts(homeDir, sessStore.ActiveConversationIDs(), 0)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":          true,
				"cleared":     cleared,
				"swept_convs": sweptConvs,
				"swept_bytes": sweptBytes,
			})
		default:
			http.Error(w, `{"error":{"message":"method not allowed"}}`, http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/v0/management/auth-files", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, spec.ManagementKey, "X-Management-Key") {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		dir := authDir
		if dir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dir = filepath.Join(home, ".gemini", "antigravity-cli")
			}
		}
		switch r.Method {
		case http.MethodGet:
			files, err := ListLocalAuthFiles(dir)
			if err != nil {
				files = []AuthFileInfo{}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
		case http.MethodPost:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				name = fmt.Sprintf("agy-%d.json", time.Now().Unix())
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
			if err != nil {
				http.Error(w, `{"error":{"message":"read body failed"}}`, http.StatusBadRequest)
				return
			}
			info, err := SaveLocalAuthFile(dir, name, raw)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "file": info})
		default:
			http.Error(w, `{"error":{"message":"method not allowed"}}`, http.StatusMethodNotAllowed)
		}
	})

	return mux
}

// RunAgyBridgeServer 启动单容器真实 agy CLI 桥接 HTTP 服务，支持 SIGINT / SIGTERM 优雅关闭。
func RunAgyBridgeServer(cfg BridgeConfig) error {
	addr := strings.TrimSpace(cfg.ListenAddr)
	if addr == "" {
		addr = fmt.Sprintf(":%d", DefaultContainerPort)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           NewAgyBridgeHandler(cfg),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	}
}
