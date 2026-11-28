package antigravity

import (
	"bufio"
	"bytes"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"cmd2api/internal/netx"
	"cmd2api/internal/workbuddy"
)

// BridgeConfig 描述单容器独立真实 agy CLI 客户端桥接服务 (`analy2api agy-bridge`) 的运行配置。
// 每个容器内只运行一个完整的真实 agy CLI 实例，通过本桥接对外暴露标准 OpenAI /v1 与管理 /v0/management 接口。
type BridgeConfig struct {
	ListenAddr       string
	CLIBinary        string
	AuthDir          string
	APIKey           string
	ManagementKey    string
	ProxyURL         string
	SessionRetention string        // "stateless"（默认，每轮请求后立即清理磁盘）| "ephemeral"（短期复用并在驱逐时清理）
	PromptBlendMode  string        // "default_intercept_continue"（默认，原生默认 -> 拦截 -> 接续）| "blend_sanitized" | "native_only" | "downstream_only"
	NativePrompt     string        // 可选自定义 AGY 原生底本
	SessionTTL       time.Duration // ephemeral 模式下的会话过期时间（默认 30 分钟）
	MaxSessions      int           // ephemeral 模式下的最大会话数（默认 256）
	Logger           *slog.Logger
}

// NewAgyBridgeHandler 构建单容器真实 agy CLI 桥接 HTTP Handler，供 `analy2api agy-bridge` 与单元测试共用。
func NewAgyBridgeHandler(cfg BridgeConfig) http.Handler {
	return NewAgyBridgeHandlerWithContext(nil, cfg)
}

// NewAgyBridgeHandlerWithContext 构建绑定生命周期 Context 的 agy CLI 桥接 Handler（当 ctx 结束时自动停止后台磁盘巡检协程）。
// 当传入 ctx 为 nil（如单测构造临时 Handler）时，仍执行启动期孤儿清理，但不启动无界后台 Ticker 防止泄漏。
func NewAgyBridgeHandlerWithContext(lifecycleCtx context.Context, cfg BridgeConfig) http.Handler {
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
		promptBlendMode = firstNonEmptyStr(os.Getenv(AgyPromptModeEnv), os.Getenv(AgyPromptBlendModeEnv))
	}
	if promptBlendMode != "" {
		promptBlendMode = NormalizePromptMode(promptBlendMode)
	}

	nativePrompt := strings.TrimSpace(cfg.NativePrompt)
	if nativePrompt == "" {
		nativePrompt = strings.TrimSpace(os.Getenv(AgyNativePromptEnv))
	}

	var specMu sync.RWMutex
	baseSpec := NodeSpec{
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
	getBaseSpec := func() NodeSpec {
		specMu.RLock()
		defer specMu.RUnlock()
		return baseSpec
	}

	homeDir := ResolveAgyHomeDir(baseSpec)

	var (
		bridgeCleanedCount atomic.Int64
		bridgeFreedBytes   atomic.Int64

		modelsMu       sync.Mutex
		modelsFetchMu  sync.Mutex // 并发防惊群：避免多客户端同时冷启多个 `agy models` 子进程
		cachedModels   []ModelItem
		modelsCachedAt time.Time

		credMu         sync.Mutex
		cachedTok      string
		cachedTokExp   time.Time
		cachedProjID   string
		bridgePool     = workbuddy.NewProxyPool(strings.TrimSpace(cfg.ProxyURL))
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
	//  1. 自动写入系统级反代拦截规范（.gemini/GEMINI.md、.gemini/config/GEMINI.md 与 workspace/GEMINI.md）
	//  2. 启动孤儿磁盘清理（启动清一次 + 每 10 分钟巡检，彻底防止异常崩溃残留撑爆磁盘）
	if authDir != "" {
		EnsureIsolatedAgyProxyRules(authDir, ResolvePromptBlendMode(baseSpec), nativePrompt)
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
			if lifecycleCtx == nil {
				return
			}
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-lifecycleCtx.Done():
					return
				case <-ticker.C:
					sessStore.SweepExpired()
					if n, freed := SweepOrphanedConversationDiskArtifacts(homeDir, sessStore.ActiveConversationIDs(), 5*time.Minute); n > 0 {
						bridgeCleanedCount.Add(int64(n))
						bridgeFreedBytes.Add(freed)
						logger.Info("定时巡检已清理孤儿 agy 会话磁盘文件", "conversations", n, "freed_bytes", freed)
					}
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
		curSpec := getBaseSpec()
		effMode := ResolvePromptBlendMode(curSpec)
		bin, err := ResolveAgyCLIBinary(curSpec.CLIBinary)
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
			"prompt_mode":           effMode,
			"prompt_blend_mode":     effMode,
			"prompt_passthrough":    "verbatim",
			"adaptive_tools":        true,
			"active_sessions":       len(sessStore.List()),
			"cleaned_conversations": globalCleaned,
			"freed_bytes":           globalFreed,
			"time":                  time.Now().UTC().Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		curSpec := getBaseSpec()
		if !checkAuth(r, curSpec.APIKey, "") {
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
			modelsMu.Lock()
			if !forceRefresh && len(cachedModels) > 0 && time.Since(modelsCachedAt) < 5*time.Minute {
				models = cachedModels
			}
			modelsMu.Unlock()

			if len(models) == 0 {
				var fetched []ModelItem
				var err error
				if _, hasCred := loadFirstValidAccountFromDir(curSpec.AuthDir, curSpec.ProxyURL); hasCred {
					if qStatus, qErr := fetchBridgeQuotaAndModels(r.Context(), curSpec, bridgePool, &credMu, &cachedTok, &cachedTokExp, &cachedProjID); qErr == nil && qStatus != nil && len(qStatus.Models) > 0 {
						for _, qm := range qStatus.Models {
							fetched = append(fetched, ModelItem{
								ID:                qm.ID,
								Name:              qm.Name,
								Vendor:            qm.Vendor,
								ContextWindow:     qm.ContextWindow,
								MaxOutputTokens:   qm.MaxOutputTokens,
								SupportsImages:    qm.SupportsImages,
								SupportsReasoning: qm.SupportsReasoning,
								SupportsToolCall:  qm.SupportsToolCall,
							})
						}
					} else {
						err = qErr
					}
				}
				if len(fetched) == 0 {
					var cliErr error
					fetched, cliErr = ListModelsViaCLI(r.Context(), curSpec)
					if cliErr != nil && err == nil {
						err = cliErr
					}
				}
				if len(fetched) == 0 {
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
		curSpec := getBaseSpec()
		if !checkAuth(r, curSpec.APIKey, "") {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, `{"error":{"message":"read body failed"}}`, http.StatusBadRequest)
			return
		}
		wantNonStream := isExplicitNonStreamingRequest(body)

		upstreamSID := firstNonEmptyStr(
			r.Header.Get("X-Session-Id"),
			r.Header.Get("X-Conversation-Id"),
			r.Header.Get("Conversation-Id"),
			r.Header.Get("X-Thread-Id"),
		)
		reqSpec := curSpec
		if hdrRetention := strings.TrimSpace(r.Header.Get("X-Agy-Session-Retention")); hdrRetention != "" {
			reqSpec.SessionRetention = NormalizeSessionRetention(hdrRetention)
		}
		if hdrMode := firstNonEmptyStr(r.Header.Get("X-Agy-Prompt-Mode"), r.Header.Get("X-Agy-Prompt-Blend-Mode")); hdrMode != "" {
			reqSpec.PromptBlendMode = NormalizePromptMode(hdrMode)
		}

		resp, _, err := ChatStreamViaCLIWithSession(r.Context(), reqSpec, body, upstreamSID, sessStore)
		driverLabel := "real-agy-cli"
		if err == nil && resp != nil && resp.Body != nil {
			// 在真正向客户端写出 HTTP 响应头前，先窥探首帧：
			// 若无头 Linux 容器内 agy CLI 因缺少 GNOME Keyring / D-Bus 会话首帧即报登录或密钥环错误，立即无缝切到容器内 HTTP/2 v1internal 引擎。
			rewoundBody, firstErrMsg := peekFirstAgySSEError(resp.Body)
			if firstErrMsg != "" && shouldFallbackToBridgeV1Internal(errors.New(firstErrMsg), reqSpec) {
				_ = resp.Body.Close()
				resp = nil
				err = errors.New(firstErrMsg)
			} else {
				resp.Body = rewoundBody
			}
		}
		if err != nil && shouldFallbackToBridgeV1Internal(err, reqSpec) {
			if fbResp, fbErr := chatStreamViaBridgeV1Internal(
				r.Context(), reqSpec, body, upstreamSID, bridgePool,
				&credMu, &cachedTok, &cachedTokExp, &cachedProjID, logger,
			); fbErr == nil {
				resp = fbResp
				err = nil
				driverLabel = "container-http2-v1internal"
			} else {
				logger.Warn("单容器 HTTP/2 v1internal 兜底引擎执行失败", "cli_err", err, "fallback_err", fbErr)
				err = fbErr
			}
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": err.Error()}})
			return
		}
		defer resp.Body.Close()

		w.Header().Set("X-Agy-Driver", driverLabel)
		for _, hk := range []string{
			"X-Agy-Retention-Mode",
			"X-Agy-Upstream-Session",
			"X-Agy-Session-Mode",
			"X-Agy-Conversation-Id",
			"X-Agy-Tool-Tier",
			"X-Agy-Tool-Count",
			"X-Agy-Prompt-Passthrough",
			"X-Agy-Prompt-Mode",
			"X-Agy-Inbound-Intercepts",
		} {
			if v := resp.Header.Get(hk); v != "" {
				w.Header().Set(hk, v)
			}
		}

		if wantNonStream {
			jsonBytes, statusCode := aggregateOpenAISSEToChatCompletionJSON(resp.Body)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(statusCode)
			_, _ = w.Write(jsonBytes)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)

		buf := make([]byte, 4096)
		for {
			n, rErr := resp.Body.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
					break
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if rErr != nil {
				break
			}
		}
	})

	// 查询单容器内绑定账号的实时套餐层级、可用模型与各模型剩余配额
	mux.HandleFunc("/v0/management/quota", func(w http.ResponseWriter, r *http.Request) {
		curSpec := getBaseSpec()
		if !checkAuth(r, curSpec.ManagementKey, "X-Management-Key") && !checkAuth(r, curSpec.APIKey, "") {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":{"message":"method not allowed"}}`, http.StatusMethodNotAllowed)
			return
		}
		status, err := fetchBridgeQuotaAndModels(r.Context(), curSpec, bridgePool, &credMu, &cachedTok, &cachedTokExp, &cachedProjID)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})

	// 查询 / 热切换单容器内的 AGY 提示词运行管线模式与预览
	mux.HandleFunc("/v0/management/prompt-modes", func(w http.ResponseWriter, r *http.Request) {
		curSpec := getBaseSpec()
		if !checkAuth(r, curSpec.ManagementKey, "X-Management-Key") {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			qMode := strings.TrimSpace(r.URL.Query().Get("mode"))
			if qMode == "" {
				qMode = ResolvePromptBlendMode(curSpec)
			}
			ds := r.URL.Query().Get("downstream_system")
			hasTools, _ := strconv.ParseBool(r.URL.Query().Get("has_tools"))
			status := BuildPromptModesStatus(qMode, ds, hasTools)
			status.ActiveMode = ResolvePromptBlendMode(curSpec)
			_ = json.NewEncoder(w).Encode(status)

		case http.MethodPut, http.MethodPost:
			var req struct {
				Mode             string `json:"mode"`
				DownstreamSystem string `json:"downstream_system"`
				HasTools         bool   `json:"has_tools"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				http.Error(w, `{"error":{"message":"invalid json"}}`, http.StatusBadRequest)
				return
			}
			if !IsValidPromptMode(req.Mode) {
				http.Error(w, `{"error":{"message":"invalid prompt mode"}}`, http.StatusBadRequest)
				return
			}
			norm := NormalizePromptMode(req.Mode)
			SetRuntimePromptMode(norm)
			specMu.Lock()
			baseSpec.PromptBlendMode = norm
			specMu.Unlock()
			if authDir != "" {
				EnsureIsolatedAgyProxyRules(authDir, norm, nativePrompt)
			}
			status := BuildPromptModesStatus(norm, req.DownstreamSystem, req.HasTools)
			status.ActiveMode = norm
			_ = json.NewEncoder(w).Encode(status)

		default:
			http.Error(w, `{"error":{"message":"method not allowed"}}`, http.StatusMethodNotAllowed)
		}
	})

	// 查询 / 重置单容器内「上游 session_id -> agy conversation_id (si)」映射与磁盘清理统计
	mux.HandleFunc("/v0/management/sessions", func(w http.ResponseWriter, r *http.Request) {
		curSpec := getBaseSpec()
		if !checkAuth(r, curSpec.ManagementKey, "X-Management-Key") {
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
		curSpec := getBaseSpec()
		if !checkAuth(r, curSpec.ManagementKey, "X-Management-Key") {
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

func isExplicitNonStreamingRequest(body []byte) bool {
	var probe struct {
		Stream *bool `json:"stream"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &probe) != nil {
		return false
	}
	return probe.Stream != nil && !*probe.Stream
}

// aggregateOpenAISSEToChatCompletionJSON 将已执行出站拦截的标准 OpenAI SSE 流聚合为非流式 chat.completion JSON 对象，
// 确保直连 agy-bridge 且显式传 stream=false 的客户端同样获得无内部标记泄漏、保留 reasoning 与 tool_calls 的标准响应。
func aggregateOpenAISSEToChatCompletionJSON(sseReader io.Reader) ([]byte, int) {
	scanner := bufio.NewScanner(sseReader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		id           = "chatcmpl-agy-" + randomUUIDv4()[:10]
		model        = defaultAgyModel
		created      = time.Now().Unix()
		contentBuf   strings.Builder
		reasoningBuf strings.Builder
		toolCalls    []map[string]any
		finishReason = "stop"
		usage        map[string]any
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(data), &obj) != nil {
			continue
		}
		if errObj, hasErr := obj["error"].(map[string]any); hasErr && errObj != nil {
			b, _ := json.Marshal(map[string]any{"error": errObj})
			return b, http.StatusBadGateway
		}
		if v, ok := obj["id"].(string); ok && v != "" {
			id = v
		}
		if v, ok := obj["model"].(string); ok && v != "" {
			model = v
		}
		if v, ok := obj["created"].(float64); ok && v > 0 {
			created = int64(v)
		}
		if u, ok := obj["usage"].(map[string]any); ok && u != nil {
			usage = u
		}
		choices, _ := obj["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		ch, _ := choices[0].(map[string]any)
		if ch == nil {
			continue
		}
		if fr, ok := ch["finish_reason"].(string); ok && fr != "" {
			finishReason = fr
		}
		if delta, ok := ch["delta"].(map[string]any); ok && delta != nil {
			if c, ok := delta["content"].(string); ok {
				contentBuf.WriteString(c)
			}
			if rc, ok := delta["reasoning_content"].(string); ok {
				reasoningBuf.WriteString(rc)
			}
			if tcs, ok := delta["tool_calls"].([]any); ok {
				for _, item := range tcs {
					if tcMap, ok := item.(map[string]any); ok && tcMap != nil {
						toolCalls = append(toolCalls, tcMap)
					}
				}
			}
		}
	}

	msg := map[string]any{
		"role":    "assistant",
		"content": StripOutboundBridgeMarkers(contentBuf.String()),
	}
	if len(toolCalls) > 0 && msg["content"] == "" {
		msg["content"] = nil
	}
	if rc := StripOutboundBridgeMarkers(reasoningBuf.String()); rc != "" {
		msg["reasoning_content"] = rc
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		finishReason = "tool_calls"
	}

	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finishReason,
			},
		},
	}
	if usage != nil {
		resp["usage"] = usage
	}
	b, _ := json.Marshal(resp)
	return b, http.StatusOK
}

// RunAgyBridgeServer 启动单容器真实 agy CLI 桥接 HTTP 服务，支持 SIGINT / SIGTERM 优雅关闭。
func RunAgyBridgeServer(cfg BridgeConfig) error {
	addr := strings.TrimSpace(cfg.ListenAddr)
	if addr == "" {
		addr = fmt.Sprintf(":%d", DefaultContainerPort)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              addr,
		Handler:           NewAgyBridgeHandlerWithContext(ctx, cfg),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	netx.EnableServerHTTP2(srv)

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

func shouldFallbackToBridgeV1Internal(cliErr error, spec NodeSpec) bool {
	if cliErr == nil || strings.TrimSpace(spec.AuthDir) == "" {
		return false
	}
	msg := strings.ToLower(cliErr.Error())
	isHeadlessOrAuthFallback := strings.Contains(msg, "未找到真实的 antigravity cli") ||
		strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "cannot find the file") ||
		strings.Contains(msg, "keyring") ||
		strings.Contains(msg, "secret-service") ||
		strings.Contains(msg, "dbus") ||
		strings.Contains(msg, "not logged in") ||
		strings.Contains(msg, "please sign in") ||
		strings.Contains(msg, "authentication") ||
		strings.Contains(msg, "failed to retrieve token") ||
		strings.Contains(msg, "unauthenticated") ||
		strings.Contains(msg, "agent platform api") ||
		strings.Contains(msg, "aiplatform.googleapis.com") ||
		strings.Contains(msg, "invalid model selection") ||
		strings.Contains(msg, "not recognized as a known model") ||
		strings.Contains(msg, "plan model not specified")
	if !isHeadlessOrAuthFallback {
		return false
	}
	_, ok := loadFirstValidAccountFromDir(spec.AuthDir, spec.ProxyURL)
	return ok
}

type compositeReadCloser struct {
	io.Reader
	io.Closer
}

func peekFirstAgySSEError(body io.ReadCloser) (io.ReadCloser, string) {
	br := bufio.NewReaderSize(body, 8192)
	var consumed bytes.Buffer
	for i := 0; i < 4; i++ {
		line, rErr := br.ReadBytes('\n')
		if len(line) > 0 {
			consumed.Write(line)
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if strings.HasPrefix(payload, "{") {
					var errWrap struct {
						Error *struct {
							Message string `json:"message"`
						} `json:"error"`
					}
					if json.Unmarshal([]byte(payload), &errWrap) == nil && errWrap.Error != nil && strings.TrimSpace(errWrap.Error.Message) != "" {
						return &compositeReadCloser{Reader: io.MultiReader(&consumed, br), Closer: body}, strings.TrimSpace(errWrap.Error.Message)
					}
				}
			}
			break
		}
		if rErr != nil {
			break
		}
	}
	return &compositeReadCloser{Reader: io.MultiReader(&consumed, br), Closer: body}, ""
}

func loadFirstValidAccountFromDir(authDir, proxyURL string) (ConvertedAGYAccount, bool) {
	dir := strings.TrimSpace(authDir)
	if dir == "" {
		return ConvertedAGYAccount{}, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ConvertedAGYAccount{}, false
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		raw, rErr := os.ReadFile(filepath.Join(dir, name))
		if rErr != nil || len(raw) == 0 {
			continue
		}
		if conv, cErr := NormalizeSingleCredential(name, raw, proxyURL); cErr == nil && conv.Valid {
			if conv.RefreshToken != "" || conv.AccessToken != "" {
				return conv, true
			}
		}
	}
	// 兜底检查 .gemini/oauth_creds.json
	oauthPath := filepath.Join(dir, ".gemini", "oauth_creds.json")
	if raw, rErr := os.ReadFile(oauthPath); rErr == nil && len(raw) > 0 {
		if conv, cErr := NormalizeSingleCredential("oauth_creds.json", raw, proxyURL); cErr == nil && conv.Valid {
			return conv, true
		}
	}
	return ConvertedAGYAccount{}, false
}

var bridgeTierByAuthDir sync.Map // map[string]string: authDir -> tierID (e.g. "g1-ultra-tier" / "g1-pro-tier")

// chatStreamViaBridgeV1Internal 在单账号独立容器内直接通过 HTTP/2 + 独立代理出口驱动 Google AI Code (`aicode.googleapis.com/v1alpha`)
// 与 Cloud Code Assist (`v1internal`) 流式接口：
//   - 对 Claude 5.5、Gemini 4 新模型及 Gemini 3.8 系列优先走 `aicode.googleapis.com/v1alpha:streamGenerateContent?alt=sse`；
//   - 若旧账号 OAuth 令牌尚未包含 `aicode` scope（返回 403）或共享模型回退，自动无缝降级至 `v1internal:streamGenerateContent?alt=sse`。
func chatStreamViaBridgeV1Internal(
	ctx context.Context,
	spec NodeSpec,
	openaiBody []byte,
	sessionID string,
	pool *workbuddy.ProxyPool,
	credMu *sync.Mutex,
	cachedTok *string,
	cachedTokExp *time.Time,
	cachedProjID *string,
	logger *slog.Logger,
) (*http.Response, error) {
	conv, ok := loadFirstValidAccountFromDir(spec.AuthDir, spec.ProxyURL)
	if !ok {
		return nil, errors.New("容器隔离目录尚未写入有效的 OAuth refresh_token / access_token 凭证")
	}

	proxyAddr := strings.TrimSpace(spec.ProxyURL)
	if proxyAddr == "" {
		proxyAddr = firstNonEmptyStr(os.Getenv("AGY_PROXY_URL"), os.Getenv("HTTPS_PROXY"), os.Getenv("HTTP_PROXY"), os.Getenv("ALL_PROXY"))
	}
	var (
		httpCli *http.Client
		err     error
	)
	if pool != nil {
		httpCli, err = pool.Client(proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("初始化容器出口代理客户端失败: %w", err)
		}
	} else {
		httpCli = netx.NewHTTP2Client(0)
	}

	ua := BuildCLIUserAgent("", spec.Fingerprint.OS, spec.Fingerprint.Arch)

	credMu.Lock()
	accessTok := *cachedTok
	projID := *cachedProjID
	needRefresh := accessTok == "" || (!cachedTokExp.IsZero() && time.Now().Add(2*time.Minute).After(*cachedTokExp))
	if accessTok == "" && conv.AccessToken != "" && !conv.Expired {
		accessTok = conv.AccessToken
		if conv.ExpiryDateMs > 0 {
			exp := time.UnixMilli(conv.ExpiryDateMs)
			if time.Now().Add(2 * time.Minute).Before(exp) {
				*cachedTok = accessTok
				*cachedTokExp = exp
				needRefresh = false
			} else {
				needRefresh = true
			}
		} else {
			needRefresh = conv.RefreshToken != ""
		}
	}
	if projID == "" && conv.ProjectID != "" {
		projID = conv.ProjectID
		*cachedProjID = projID
	}

	upstreamBase := strings.TrimRight(strings.TrimSpace(os.Getenv("AGY_UPSTREAM_BASE_URL")), "/")
	tokenEndpoint := strings.TrimSpace(os.Getenv("AGY_OAUTH_TOKEN_URL"))

	if needRefresh && conv.RefreshToken != "" {
		refCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		tokRes, rErr := RefreshGoogleOAuthToken(refCtx, httpCli, conv.ClientID, conv.ClientSecret, conv.RefreshToken, tokenEndpoint)
		cancel()
		if rErr != nil {
			credMu.Unlock()
			return nil, fmt.Errorf("容器内自动刷新 Google OAuth 令牌失败: %w", rErr)
		}
		accessTok = tokRes.AccessToken
		exp := time.Now().Add(55 * time.Minute)
		if tokRes.ExpiresIn > 0 {
			exp = time.Now().Add(time.Duration(tokRes.ExpiresIn) * time.Second)
		}
		*cachedTok = accessTok
		*cachedTokExp = exp
	}
	if accessTok == "" {
		credMu.Unlock()
		return nil, errors.New("容器内凭证缺少可用 access_token 且无法刷新")
	}

	var cachedTier string
	if v, ok := bridgeTierByAuthDir.Load(strings.TrimSpace(spec.AuthDir)); ok {
		cachedTier, _ = v.(string)
	}
	if projID == "" || cachedTier == "" {
		caCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if ca, caErr := LoadCodeAssist(caCtx, httpCli, upstreamBase, accessTok, ua); caErr == nil && ca != nil {
			if ca.ProjectID != "" && projID == "" {
				projID = ca.ProjectID
				*cachedProjID = projID
			}
			if ca.TierID != "" {
				cachedTier = ca.TierID
				if strings.TrimSpace(spec.AuthDir) != "" {
					bridgeTierByAuthDir.Store(strings.TrimSpace(spec.AuthDir), cachedTier)
				}
			}
		} else if caErr != nil && logger != nil {
			logger.Warn("容器内自动获取 CodeAssist project_id 告警", "err", caErr)
		}
		cancel()
	}
	credMu.Unlock()

	sanitizedBody, err := SanitizeOpenAIPayload(openaiBody)
	if err != nil {
		return nil, err
	}
	var modelProbe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(sanitizedBody, &modelProbe)

	// 新一代 AI Code 引擎优先路由（当未自定义非 Google 测试桩 AGY_UPSTREAM_BASE_URL 时启用）
	canUseAICode := (upstreamBase == "" || strings.Contains(strings.ToLower(upstreamBase), "googleapis.com") || os.Getenv(AntigravityAICodeBaseURLEnv) != "")
	if canUseAICode && ShouldPreferAICodeForModel(modelProbe.Model) {
		aicodeResp, _, aicodeErr := StreamAICodeGenerateContent(
			ctx, httpCli, "", accessTok, cachedTier, ua, sanitizedBody, modelProbe.Model, sessionID,
		)
		if aicodeErr == nil && aicodeResp != nil {
			aicodeResp.Header.Set("X-Agy-Prompt-Mode", ResolvePromptBlendMode(spec))
			return aicodeResp, nil
		}
		if IsAICodeExclusiveModel(modelProbe.Model) {
			var apiErr *APIError
			if errors.As(aicodeErr, &apiErr) && apiErr.Status == http.StatusForbidden {
				return nil, fmt.Errorf("调用新模型 %q 需要 Google AI Code (aicode) 授权权限：请重新使用最新 OAuth 链接授权该账号后再试（原始响应：%s）", modelProbe.Model, apiErr.Message)
			}
			return nil, aicodeErr
		}
		if logger != nil {
			logger.Info("aicode 流式接口回退至 v1internal", "model", modelProbe.Model, "err", aicodeErr)
		}
	}

	v1Body, err := BuildV1InternalRequest(sanitizedBody, projID, modelProbe.Model, sessionID)
	if err != nil {
		return nil, err
	}

	baseForStream := upstreamBase
	if baseForStream == "" {
		baseForStream = AntigravityDailyBaseURL
	}
	targetURL := baseForStream + "/v1internal:streamGenerateContent?alt=sse"
	resp, err := netx.DoWithEOFRetry(ctx, httpCli, func() (*http.Request, error) {
		req, rErr := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(v1Body))
		if rErr != nil {
			return nil, rErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessTok)
		req.Header.Set("User-Agent", ua)
		return req, nil
	}, 2)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		rawErr, _ := io.ReadAll(io.LimitReader(resp.Body, maxMgmtResponseBytes))
		_ = resp.Body.Close()
		return nil, parseUpstreamAPIError(resp.StatusCode, rawErr)
	}

	outResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       TranslateV1InternalSSEToOpenAISSE(ctx, resp.Body, modelProbe.Model, ""),
	}
	outResp.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
	outResp.Header.Set("X-Agy-Prompt-Mode", ResolvePromptBlendMode(spec))
	return outResp, nil
}

func fetchBridgeQuotaAndModels(
	ctx context.Context,
	spec NodeSpec,
	pool *workbuddy.ProxyPool,
	credMu *sync.Mutex,
	cachedTok *string,
	cachedTokExp *time.Time,
	cachedProjID *string,
) (*AccountQuotaStatus, error) {
	cliBin, cliBinErr := ResolveAgyCLIBinary(spec.CLIBinary)
	conv, hasCred := loadFirstValidAccountFromDir(spec.AuthDir, spec.ProxyURL)

	var status *AccountQuotaStatus
	var quotaErr error
	if hasCred {
		proxyAddr := strings.TrimSpace(spec.ProxyURL)
		if proxyAddr == "" {
			proxyAddr = firstNonEmptyStr(os.Getenv("AGY_PROXY_URL"), os.Getenv("HTTPS_PROXY"), os.Getenv("HTTP_PROXY"), os.Getenv("ALL_PROXY"))
		}
		var httpCli *http.Client
		if pool != nil {
			httpCli, _ = pool.Client(proxyAddr)
		}
		if httpCli == nil {
			httpCli = netx.NewHTTP2Client(25 * time.Second)
		}

		credMu.Lock()
		accessTok := *cachedTok
		projID := *cachedProjID
		needRefresh := accessTok == "" || (!cachedTokExp.IsZero() && time.Now().Add(2*time.Minute).After(*cachedTokExp))
		if accessTok == "" && conv.AccessToken != "" && !conv.Expired {
			accessTok = conv.AccessToken
			needRefresh = conv.RefreshToken != "" && conv.ExpiryDateMs <= 0
		}
		if projID == "" && conv.ProjectID != "" {
			projID = conv.ProjectID
			*cachedProjID = projID
		}
		tokenEndpoint := strings.TrimSpace(os.Getenv("AGY_OAUTH_TOKEN_URL"))
		if needRefresh && conv.RefreshToken != "" {
			refCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if tokRes, rErr := RefreshGoogleOAuthToken(refCtx, httpCli, conv.ClientID, conv.ClientSecret, conv.RefreshToken, tokenEndpoint); rErr == nil {
				accessTok = tokRes.AccessToken
				exp := time.Now().Add(55 * time.Minute)
				if tokRes.ExpiresIn > 0 {
					exp = time.Now().Add(time.Duration(tokRes.ExpiresIn) * time.Second)
				}
				*cachedTok = accessTok
				*cachedTokExp = exp
			} else {
				quotaErr = rErr
			}
			cancel()
		}
		credMu.Unlock()

		if accessTok != "" {
			upstreamBase := strings.TrimRight(strings.TrimSpace(os.Getenv("AGY_UPSTREAM_BASE_URL")), "/")
			ua := BuildCLIUserAgent("", spec.Fingerprint.OS, spec.Fingerprint.Arch)
			qCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			status, quotaErr = FetchAntigravityQuotaAndModels(qCtx, httpCli, upstreamBase, accessTok, projID, ua)
			cancel()
			if status != nil {
				if status.ProjectID != "" {
					credMu.Lock()
					*cachedProjID = status.ProjectID
					credMu.Unlock()
				}
				if status.TierID != "" && strings.TrimSpace(spec.AuthDir) != "" {
					bridgeTierByAuthDir.Store(strings.TrimSpace(spec.AuthDir), status.TierID)
				}
			}
		}
	}

	var cliModels []ModelItem
	isEnterpriseGCP := status != nil && status.ProjectID != "" && !strings.EqualFold(status.ProjectID, DefaultAGYProjectID)
	if cliBinErr == nil && (status == nil || len(status.Models) == 0 || isEnterpriseGCP) {
		mCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		cliModels, _ = ListModelsViaCLI(mCtx, spec)
		cancel()
	}
	if !hasCred && len(cliModels) == 0 {
		return nil, errors.New("容器隔离目录尚未写入有效 OAuth 凭证")
	}

	if status == nil {
		if len(cliModels) == 0 && quotaErr != nil {
			return nil, quotaErr
		}
		status = &AccountQuotaStatus{
			OK:        true,
			ProjectID: conv.ProjectID,
			Models:    []ModelQuotaItem{},
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if quotaErr != nil {
			status.Warning = quotaErr.Error()
		}
	}

	status.CLIBinary = cliBin
	status.CLIReady = cliBinErr == nil
	hasAICodeModels := false
	for _, m := range status.Models {
		if m.Source == "aicode" || strings.HasPrefix(strings.ToLower(m.ID), "claude-opus-5") || strings.HasPrefix(strings.ToLower(m.ID), "claude-sonnet-5") {
			hasAICodeModels = true
			break
		}
	}
	switch {
	case status.CLIReady && isEnterpriseGCP:
		status.Driver = "real-agy-cli"
	case hasAICodeModels:
		status.Driver = "agy-container-aicode"
	default:
		status.Driver = "agy-container-v1internal"
	}

	// 仅在真实企业 GCP 项目或 v1internal 未返回目录时合并 CLI 列表，确保 Free/Pro 个人账号只展示该套餐 100% 真实可调用模型
	if len(cliModels) > 0 {
		byID := make(map[string]int, len(status.Models))
		for i, m := range status.Models {
			byID[strings.ToLower(m.ID)] = i
		}
		for _, cm := range cliModels {
			key := strings.ToLower(cm.ID)
			if idx, exists := byID[key]; exists {
				if cm.Name != "" {
					status.Models[idx].Name = cm.Name
				}
				status.Models[idx].Source = "merged"
				continue
			}
			qItem := ModelQuotaItem{
				ID:                cm.ID,
				Name:              cm.Name,
				Vendor:            cm.Vendor,
				ContextWindow:     cm.ContextWindow,
				MaxOutputTokens:   cm.MaxOutputTokens,
				SupportsImages:    cm.SupportsImages,
				SupportsReasoning: cm.SupportsReasoning,
				SupportsToolCall:  cm.SupportsToolCall,
				Source:            "agy_cli",
			}
			// 对同家族推理档位变体（如 gemini-3.8-flash-high 对应 gemini-3.8-flash）自动继承家族配额窗口
			inheritFamilyQuota(&qItem, status.Models)
			status.Models = append(status.Models, qItem)
		}
		sortModelQuotaItems(status.Models)
	}
	return status, nil
}

func inheritFamilyQuota(dst *ModelQuotaItem, pool []ModelQuotaItem) {
	if dst == nil || dst.RemainingPct != nil || len(pool) == 0 {
		return
	}
	stem := strings.ToLower(dst.ID)
	for _, suf := range []string{"-xhigh", "-high", "-medium", "-low", "-thinking"} {
		stem = strings.TrimSuffix(stem, suf)
	}
	for _, cand := range pool {
		if cand.RemainingPct == nil {
			continue
		}
		cl := strings.ToLower(cand.ID)
		if cl == stem || strings.HasPrefix(cl, stem) || strings.HasPrefix(stem, cl) {
			pct := *cand.RemainingPct
			dst.RemainingPct = &pct
			if cand.RemainingFraction != nil {
				rf := *cand.RemainingFraction
				dst.RemainingFraction = &rf
			}
			if cand.FiveHourRemainingPct != nil {
				fh := *cand.FiveHourRemainingPct
				dst.FiveHourRemainingPct = &fh
				dst.FiveHourResetTime = cand.FiveHourResetTime
			}
			if cand.WeeklyRemainingPct != nil {
				wk := *cand.WeeklyRemainingPct
				dst.WeeklyRemainingPct = &wk
				dst.WeeklyResetTime = cand.WeeklyResetTime
			}
			dst.ResetTime = cand.ResetTime
			dst.Exhausted = cand.Exhausted
			return
		}
	}
}
