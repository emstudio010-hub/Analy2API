package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cmd2api/internal/netx"
)

const (
	// AgyCLIBinEnv 允许通过环境变量指定真实 agy CLI 二进制路径（默认从 PATH 查找 agy）。
	AgyCLIBinEnv = "AGY_CLI_BIN"

	// AgySessionRetentionEnv 控制 agy CLI 磁盘会话保留策略："stateless"（默认，反代零残留即用即清）或 "ephemeral"（短期复用并在驱逐时清理）。
	AgySessionRetentionEnv = "AGY_SESSION_RETENTION"

	// defaultAgyModel 当调用方未指定模型时使用的真实 agy CLI 默认轻量模型。
	defaultAgyModel = "gemini-3.8-flash-medium"
)

// ResolveAgyCLIBinary 查找本机或容器内安装的真实 AntiGravity CLI (`agy` / `agy.exe`) 路径。
func ResolveAgyCLIBinary(override string) (string, error) {
	if p := strings.TrimSpace(override); p != "" {
		if found, err := exec.LookPath(p); err == nil && found != "" {
			return found, nil
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("未找到真实的 AntiGravity CLI 可执行文件 (%s): executable file not found", p)
	}
	if p := strings.TrimSpace(os.Getenv(AgyCLIBinEnv)); p != "" {
		if found, err := exec.LookPath(p); err == nil && found != "" {
			return found, nil
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("未找到环境变量 AGY_CLI_BIN 指定的 AntiGravity CLI 可执行文件 (%s): executable file not found", p)
	}
	if found, err := exec.LookPath("agy"); err == nil && found != "" {
		return found, nil
	}
	if found, err := exec.LookPath("agy.exe"); err == nil && found != "" {
		return found, nil
	}
	return "", errors.New("未找到真实的 AntiGravity CLI (agy) 可执行文件，请先安装 agy 或设置环境变量 AGY_CLI_BIN")
}

// ResolveSessionRetention 解析当前生效的 agy 会话磁盘策略（spec 优先 -> 环境变量 -> 默认 stateless 零磁盘残留）。
func ResolveSessionRetention(spec NodeSpec) string {
	if strings.TrimSpace(spec.SessionRetention) != "" {
		return NormalizeSessionRetention(spec.SessionRetention)
	}
	if envVal := strings.TrimSpace(os.Getenv(AgySessionRetentionEnv)); envVal != "" {
		return NormalizeSessionRetention(envVal)
	}
	return SessionRetentionStateless
}

// buildIsolatedCLIEnv 为单个 agy CLI 客户端进程构造隔离环境变量：
//   - 若指定 spec.AuthDir，则将 HOME / USERPROFILE / XDG_CONFIG_HOME 指向该单实例专属目录，实现单账号凭证与 GEMINI.md 规则隔离
//   - 若指定非直连 spec.ProxyURL，则注入 HTTP_PROXY / HTTPS_PROXY / ALL_PROXY，实现单客户端独立出口 IP
//   - 若指定 direct，则清除宿主机残留代理变量，防止串流
func buildIsolatedCLIEnv(spec NodeSpec) []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base)+12)
	authDir := strings.TrimSpace(spec.AuthDir)
	proxy := strings.TrimSpace(spec.ProxyURL)
	clearProxy := strings.EqualFold(proxy, "direct")

	for _, kv := range base {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		k := strings.ToUpper(kv[:eq])
		if authDir != "" && (k == "HOME" || k == "USERPROFILE" || k == "XDG_CONFIG_HOME") {
			continue
		}
		if (proxy != "" || clearProxy) && (k == "HTTP_PROXY" || k == "HTTPS_PROXY" || k == "ALL_PROXY") {
			continue
		}
		filtered = append(filtered, kv)
	}

	if authDir != "" {
		if abs, err := filepath.Abs(authDir); err == nil {
			authDir = abs
		}
		_ = os.MkdirAll(authDir, 0o755)
		filtered = append(filtered,
			"HOME="+authDir,
			"USERPROFILE="+authDir,
			"XDG_CONFIG_HOME="+filepath.Join(authDir, ".config"),
		)
	}
	if proxy != "" && !clearProxy {
		filtered = append(filtered,
			"HTTP_PROXY="+proxy,
			"HTTPS_PROXY="+proxy,
			"ALL_PROXY="+proxy,
			"http_proxy="+proxy,
			"https_proxy="+proxy,
			"all_proxy="+proxy,
		)
	}
	if fp := strings.TrimSpace(spec.Fingerprint.FingerprintID); fp != "" {
		filtered = append(filtered, "AGY_FINGERPRINT_ID="+fp)
	}
	// 抑制无头反代子进程触发后台自动更新检查，防止派生孤儿更新器锁住磁盘目录
	filtered = append(filtered,
		"AGY_CLI_DISABLE_AUTO_UPDATE=true",
		"AGY_DISABLE_AUTO_UPDATE=1",
		"DISABLE_AUTOUPDATER=1",
		"NO_UPDATE_NOTIFIER=1",
	)
	// 强制 agy CLI Go 运行时始终启用 HTTP/2 客户端连接 Google（移除宿主机可能残留的 http2client=0）
	return netx.EnsureGODEBUGHTTP2(filtered)
}

// ListModelsViaCLI 调用真实 `agy models` 命令解析当前 CLI 客户端的真实可用模型列表。
func ListModelsViaCLI(ctx context.Context, spec NodeSpec) ([]ModelItem, error) {
	bin, err := ResolveAgyCLIBinary(spec.CLIBinary)
	if err != nil {
		return nil, err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, bin, "models")
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = buildIsolatedCLIEnv(spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("执行真实 agy models 失败: %s", msg)
	}
	return ParseAgyModelsOutput(stdout.String()), nil
}

// ParseAgyModelsOutput 解析真实 `agy models` 输出（每行 `<model_id>\t<Display Name>`）。
func ParseAgyModelsOutput(raw string) []ModelItem {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	seen := make(map[string]struct{}, len(lines))
	out := make([]ModelItem, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "fetching ") || strings.HasPrefix(trimmed, "Usage") {
			continue
		}
		parts := strings.SplitN(trimmed, "\t", 2)
		id := strings.TrimSpace(parts[0])
		if id == "" || strings.Contains(id, " ") {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		displayName := id
		if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
			displayName = strings.TrimSpace(parts[1])
		}
		item, ok := InferModelItem(map[string]any{
			"id":           id,
			"display_name": displayName,
		})
		if ok {
			out = append(out, item)
		}
	}
	return out
}

type agyPromptBuildResult struct {
	Model                string
	Effort               string
	Prompt               string
	PromptBlendMode      string
	InboundInterceptHits []string
	ToolsSpec            AdaptedToolsSpec
	TotalNonSysMsgs      int
}

func extractPayloadPromptModeOverride(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	for _, k := range []string{"agy_prompt_mode", "prompt_mode", "agy_mode"} {
		if v, ok := payload[k].(string); ok && IsValidPromptMode(v) {
			return NormalizePromptMode(v)
		}
	}
	if meta, ok := payload["metadata"].(map[string]any); ok && meta != nil {
		for _, k := range []string{"agy_prompt_mode", "prompt_mode", "agy_mode"} {
			if v, ok := meta[k].(string); ok && IsValidPromptMode(v) {
				return NormalizePromptMode(v)
			}
		}
	}
	return ""
}

// buildAgyCLIPrompt 使用当前生效的提示词管线模式（默认 default_intercept_continue：原生默认 -> 拦截 -> 接续）组装 agy CLI 输入文本。
func buildAgyCLIPrompt(openaiBody []byte, resumeFromIndex int) (agyPromptBuildResult, error) {
	return buildAgyCLIPromptWithOptions(openaiBody, resumeFromIndex, ActivePromptMode(), "")
}

// buildAgyCLIPromptWithOptions 将标准 OpenAI 消息数组、拦截桥、下游系统提示词及自适应分级工具定义组装为无头 agy CLI 输入文本：
//   - 默认 default_intercept_continue：保留真实 agy CLI 内置默认提示词，经入站冲突拦截 + <agy_intercept_bridge> 隔离后，在 <downstream_continuation> 内权威接续下游提示词与 <AGY_TOOL_CALL> 外部工具
//   - 可选支持 blend_sanitized / native_only / downstream_only（通过请求 metadata、NodeSpec、运行态配置或 AGY_PROMPT_MODE 环境变量切换）
//   - 自动根据工具数量与 Schema 体积选择 full / compact / signature / none 自适应层级
//   - 当 resumeFromIndex > 0（ephemeral 模式同一上游 session_id 续接已有 agy si）时，仅发送增量新增轮次
func buildAgyCLIPromptWithOptions(openaiBody []byte, resumeFromIndex int, blendMode, nativePrompt string) (agyPromptBuildResult, error) {
	var payload map[string]any
	if err := json.Unmarshal(openaiBody, &payload); err != nil {
		return agyPromptBuildResult{}, fmt.Errorf("解析请求体失败: %w", err)
	}

	normBlendMode := NormalizePromptMode(blendMode)
	if reqMode := extractPayloadPromptModeOverride(payload); reqMode != "" {
		normBlendMode = reqMode
	}

	res := agyPromptBuildResult{
		PromptBlendMode:      normBlendMode,
		InboundInterceptHits: []string{},
	}
	res.Model, _ = payload["model"].(string)
	res.Model = strings.TrimSpace(res.Model)
	if res.Model == "" {
		res.Model = defaultAgyModel
	}
	if eff, ok := payload["reasoning_effort"].(string); ok {
		eff = strings.ToLower(strings.TrimSpace(eff))
		switch eff {
		case "low", "medium", "high", "xhigh", "max":
			res.Effort = eff
		}
	}

	// 1. 拆分系统提示词与非系统消息
	var sysBlocks []string
	var nonSysMsgs []map[string]any
	toolIDToName := make(map[string]string)

	if msgs, ok := payload["messages"].([]any); ok {
		for _, item := range msgs {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role := strings.ToLower(strings.TrimSpace(fmt.Sprint(m["role"])))
			if role == "system" || role == "developer" {
				if text := extractMessageText(m["content"]); strings.TrimSpace(text) != "" {
					sysBlocks = append(sysBlocks, text)
				}
				continue
			}
			if role == "assistant" {
				if tcs, ok := m["tool_calls"].([]any); ok {
					for _, tcItem := range tcs {
						if tc, ok := tcItem.(map[string]any); ok {
							id, _ := tc["id"].(string)
							fn, _ := tc["function"].(map[string]any)
							name, _ := fn["name"].(string)
							if id != "" && name != "" {
								toolIDToName[id] = name
							}
						}
					}
				}
			}
			nonSysMsgs = append(nonSysMsgs, m)
		}
	}
	res.TotalNonSysMsgs = len(nonSysMsgs)

	startIdx := 0
	isResumeDelta := resumeFromIndex > 0 && resumeFromIndex < res.TotalNonSysMsgs
	if isResumeDelta {
		startIdx = resumeFromIndex
	}

	// 2. 自适应解析下游外部工具（0 工具 / dsh / codex / Claude Code / 100+ MCP 工具）
	var rawTools []any
	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		rawTools = tools
	}
	res.ToolsSpec = AdaptToolsForAgyCLI(rawTools, payload["tool_choice"])

	var b strings.Builder

	// 3. 按选定管线模式组装拦截桥与下游接续包络（仅首轮或无状态反代写入）
	if !isResumeDelta {
		assembledSys, inboundHits := AssembleAgyPromptEnvelope(
			nativePrompt,
			sysBlocks,
			res.ToolsSpec.PromptBlock,
			res.ToolsSpec.HasTools(),
			normBlendMode,
			true,
		)
		res.InboundInterceptHits = inboundHits
		if assembledSys != "" {
			b.WriteString(assembledSys)
			b.WriteString("\n\n")
		}
	}

	// 4. 对话消息历史（历史工具调用标签与当前管线模式严格对齐）
	histToolTag := "tool_call"
	if normBlendMode == PromptModeDefaultInterceptContinue {
		histToolTag = AgyToolCallTag
	}

	var convoBlocks []string
	for _, m := range nonSysMsgs[startIdx:] {
		role := strings.ToLower(strings.TrimSpace(fmt.Sprint(m["role"])))
		text := extractMessageText(m["content"])
		switch role {
		case "user":
			if strings.TrimSpace(text) != "" {
				convoBlocks = append(convoBlocks, "User:\n"+text)
			}
		case "assistant":
			var sub strings.Builder
			if strings.TrimSpace(text) != "" {
				sub.WriteString(text)
			}
			if tcs, ok := m["tool_calls"].([]any); ok && len(tcs) > 0 {
				for _, tcItem := range tcs {
					if tc, ok := tcItem.(map[string]any); ok {
						fn, _ := tc["function"].(map[string]any)
						name, _ := fn["name"].(string)
						args := fmt.Sprint(fn["arguments"])
						if sub.Len() > 0 {
							sub.WriteString("\n")
						}
						sub.WriteString(fmt.Sprintf("<%s>{\"name\":%q,\"arguments\":%s}</%s>", histToolTag, name, normalizeJSONArgsString(args), histToolTag))
					}
				}
			}
			if sub.Len() > 0 {
				convoBlocks = append(convoBlocks, "Assistant:\n"+sub.String())
			}
		case "tool":
			tcID, _ := m["tool_call_id"].(string)
			fnName, _ := m["name"].(string)
			if fnName == "" && tcID != "" {
				fnName = toolIDToName[tcID]
			}
			convoBlocks = append(convoBlocks, fmt.Sprintf("Tool Result (id=%s name=%s):\n%s", tcID, fnName, text))
		}
	}

	if len(convoBlocks) == 1 && strings.HasPrefix(convoBlocks[0], "User:\n") && isResumeDelta {
		b.WriteString(strings.TrimPrefix(convoBlocks[0], "User:\n"))
	} else {
		b.WriteString(strings.Join(convoBlocks, "\n\n"))
	}

	res.Prompt = strings.TrimSpace(b.String())
	if res.Prompt == "" {
		res.Prompt = "Hello"
	}
	return res, nil
}

func normalizeJSONArgsString(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "<nil>" {
		return "{}"
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}
	b, _ := json.Marshal(trimmed)
	return string(b)
}

func extractUpstreamSessionFromBody(openaiBody []byte) string {
	var probe struct {
		SessionID      string `json:"session_id"`
		ConversationID string `json:"conversation_id"`
		User           string `json:"user"`
		Metadata       struct {
			SessionID string `json:"session_id"`
			UserID    string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(openaiBody, &probe) != nil {
		return ""
	}
	return firstNonEmptyStr(
		probe.SessionID,
		probe.ConversationID,
		probe.Metadata.SessionID,
		probe.Metadata.UserID,
		probe.User,
	)
}

// buildAgyStreamInputLine 构造真实 `agy --input-format stream-json` 标准 stdin NDJSON 用户输入行。
func buildAgyStreamInputLine(prompt string) ([]byte, error) {
	payload := map[string]any{
		"event": "user",
		"message": map[string]any{
			"role":    "user",
			"content": prompt,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

var defaultLocalCLISessions = NewCLISessionStore(defaultCLISessionTTL, defaultMaxCLISession)

// ChatStreamViaCLI 直接驱动真实的 AntiGravity CLI (`agy --input-format stream-json --output-format stream-json --disable-slash-commands`)。
func ChatStreamViaCLI(ctx context.Context, spec NodeSpec, openaiBody []byte) (*http.Response, []byte, error) {
	return ChatStreamViaCLIWithSession(ctx, spec, openaiBody, "", defaultLocalCLISessions)
}

// ChatStreamViaCLIWithSession 驱动真实 agy CLI 完成一次反代推理，默认采用 `default_intercept_continue`（默认提示词 -> 拦截 -> 接续）。
func ChatStreamViaCLIWithSession(
	ctx context.Context,
	spec NodeSpec,
	openaiBody []byte,
	upstreamSessionID string,
	sessStore *CLISessionStore,
) (*http.Response, []byte, error) {
	bin, err := ResolveAgyCLIBinary(spec.CLIBinary)
	if err != nil {
		return nil, nil, err
	}

	preparedBody, err := PrepareOpenAIPayloadForCLI(openaiBody)
	if err != nil {
		return nil, nil, err
	}

	retentionMode := ResolveSessionRetention(spec)
	homeDir := ResolveAgyHomeDir(spec)

	resolvedUpstreamSID := strings.TrimSpace(upstreamSessionID)
	if resolvedUpstreamSID == "" {
		resolvedUpstreamSID = extractUpstreamSessionFromBody(preparedBody)
	}
	isEphemeralCaller := false
	if resolvedUpstreamSID == "" {
		isEphemeralCaller = true
		resolvedUpstreamSID = "stateless_" + randomUUIDv4()[:12]
	}

	if sessStore == nil {
		sessStore = defaultLocalCLISessions
	}

	var unlockSession func()
	if retentionMode == SessionRetentionEphemeral && !isEphemeralCaller {
		unlockSession = sessStore.LockSession(resolvedUpstreamSID)
	} else {
		unlockSession = func() {}
	}

	var (
		resumeConvID    string
		resumeFromIndex int
		sessionMode     = SessionRetentionStateless
	)
	if retentionMode == SessionRetentionEphemeral && !isEphemeralCaller {
		sessionMode = "new"
		if existing, ok := sessStore.Lookup(resolvedUpstreamSID); ok && existing.AgyConversationID != "" {
			probeRes, pErr := buildAgyCLIPrompt(preparedBody, 0)
			if pErr == nil && probeRes.TotalNonSysMsgs > existing.LastMessageCount {
				resumeConvID = existing.AgyConversationID
				resumeFromIndex = existing.LastMessageCount
				sessionMode = "resumed"
			} else {
				sessStore.Delete(resolvedUpstreamSID)
			}
		}
	}

	blendMode := ResolvePromptBlendMode(spec)
	nativePrompt := ResolveNativePromptTemplate(spec)
	built, err := buildAgyCLIPromptWithOptions(preparedBody, resumeFromIndex, blendMode, nativePrompt)
	if err != nil {
		unlockSession()
		return nil, preparedBody, err
	}
	// 使用最终解析出的生效模式写入隔离目录 GEMINI.md 拦截规则
	EnsureIsolatedAgyProxyRules(spec.AuthDir, built.PromptBlendMode, nativePrompt)

	stdinPayload, err := buildAgyStreamInputLine(built.Prompt)
	if err != nil {
		unlockSession()
		return nil, preparedBody, fmt.Errorf("编码 agy stream-json 输入失败: %w", err)
	}

	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--disable-slash-commands",
	}
	if resumeConvID != "" {
		args = append(args, "--conversation", resumeConvID)
	}
	if built.Model != "" {
		args = append(args, "--model", built.Model)
	}
	if built.Effort != "" {
		args = append(args, "--effort", built.Effort)
	}

	isolatedEnv := buildIsolatedCLIEnv(spec)
	var wsDir string
	if spec.AuthDir != "" {
		wsDir = filepath.Join(spec.AuthDir, "workspace")
		_ = os.MkdirAll(wsDir, 0o755)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	bodyCloser := &cancelOnCloseReadCloser{
		ReadCloser: pr,
		cancel:     cancelRun,
	}
	reqID := "chatcmpl-agy-" + randomUUIDv4()[:10]
	created := time.Now().Unix()

	const maxTransientRetries = 2
	go func() {
		defer cancelRun()
		defer unlockSession()
		defer pw.Close()

		var finalConvID string
		for attempt := 0; attempt <= maxTransientRetries; attempt++ {
			if runCtx.Err() != nil {
				return
			}
			if attempt > 0 {
				backoff := time.Duration(800*attempt) * time.Millisecond
				select {
				case <-runCtx.Done():
					return
				case <-time.After(backoff):
				}
			}

			cmd := exec.CommandContext(runCtx, bin, args...)
			cmd.WaitDelay = 3 * time.Second
			cmd.Env = isolatedEnv
			cmd.Stdin = bytes.NewReader(stdinPayload)
			if wsDir != "" {
				cmd.Dir = wsDir
			}
			stdoutPipe, pErr := cmd.StdoutPipe()
			if pErr != nil {
				writeAgySSEErrorAndDone(pw, fmt.Sprintf("创建 agy CLI stdout 管道失败: %v", pErr))
				return
			}
			stderrBuf := &limitedBuffer{limit: 64 * 1024}
			cmd.Stderr = stderrBuf

			if sErr := cmd.Start(); sErr != nil {
				writeAgySSEErrorAndDone(pw, fmt.Sprintf("启动真实 agy CLI (%s) 失败: %v", bin, sErr))
				return
			}

			canRetry := attempt < maxTransientRetries
			capturedConvID, emittedAny, deferredErr := streamAgyNDJSONToOpenAISSEAttempt(
				runCtx, stdoutPipe, pw, built.Model, reqID, created, built.ToolsSpec, canRetry,
			)
			waitErr := cmd.Wait()

			if capturedConvID == "" {
				capturedConvID = resumeConvID
			}
			if deferredErr == "" && !emittedAny && waitErr != nil {
				msg := strings.TrimSpace(stderrBuf.buf.String())
				if msg == "" {
					msg = waitErr.Error()
				}
				deferredErr = msg
			}

			if deferredErr != "" && !emittedAny && canRetry && netx.IsTransientNetworkErrorMessage(deferredErr) {
				if capturedConvID != "" && capturedConvID != resumeConvID {
					CleanupConversationDiskArtifacts(homeDir, capturedConvID)
				}
				continue
			}

			if deferredErr != "" && !emittedAny {
				writeAgySSEErrorAndDone(pw, deferredErr)
			}
			finalConvID = capturedConvID
			break
		}

		if retentionMode == SessionRetentionStateless || isEphemeralCaller {
			if finalConvID != "" {
				CleanupConversationDiskArtifacts(homeDir, finalConvID)
			}
			return
		}
		if finalConvID != "" {
			sessStore.Bind(resolvedUpstreamSID, finalConvID, built.Model, built.TotalNonSysMsgs)
		}
	}()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header: http.Header{
			"Content-Type":             []string{"text/event-stream; charset=utf-8"},
			"X-Agy-Driver":             []string{"real-agy-cli"},
			"X-Agy-Retention-Mode":     []string{retentionMode},
			"X-Agy-Upstream-Session":   []string{resolvedUpstreamSID},
			"X-Agy-Session-Mode":       []string{sessionMode},
			"X-Agy-Tool-Tier":          []string{built.ToolsSpec.Tier},
			"X-Agy-Tool-Count":         []string{strconv.Itoa(len(built.ToolsSpec.Tools))},
			"X-Agy-Prompt-Passthrough": []string{"verbatim"},
			"X-Agy-Prompt-Mode":        []string{built.PromptBlendMode},
			"X-Agy-Inbound-Intercepts": []string{strconv.Itoa(len(built.InboundInterceptHits))},
		},
		Body: bodyCloser,
	}
	if resumeConvID != "" {
		resp.Header.Set("X-Agy-Conversation-Id", resumeConvID)
	}
	return resp, preparedBody, nil
}

type agyStreamEvent struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	StepUpdate     *struct {
		ConversationID string    `json:"conversation_id"`
		State          string    `json:"state"`
		StepType       string    `json:"step_type"`
		TextDelta      string    `json:"text_delta"`
		ThinkingDelta  string    `json:"thinking_delta"`
		Error          string    `json:"error"`
		Usage          *agyUsage `json:"usage"`
	} `json:"step_update"`
	Result *struct {
		ConversationID string    `json:"conversation_id"`
		Status         string    `json:"status"`
		Response       string    `json:"response"`
		Error          string    `json:"error"`
		Usage          *agyUsage `json:"usage"`
	} `json:"result"`
}

type agyUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

func writeAgySSEErrorAndDone(dst io.Writer, msg string) {
	errPayload, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": firstNonEmptyStr(msg, "agy CLI returned error status"),
			"type":    "upstream_error",
		},
	})
	_, _ = fmt.Fprintf(dst, "data: %s\n\n", errPayload)
	_, _ = io.WriteString(dst, "data: [DONE]\n\n")
}

// streamAgyNDJSONToOpenAISSE 解析真实 `agy --output-format stream-json` 的每一行 NDJSON，
// 经出站拦截器剥离任何意外泄漏的内部桥接标记后，输出标准 OpenAI `chat.completion.chunk` SSE 事件流。
func streamAgyNDJSONToOpenAISSE(
	ctx context.Context,
	src io.Reader,
	dst io.Writer,
	model string,
	reqID string,
	created int64,
	toolsSpec AdaptedToolsSpec,
) string {
	convID, _, _ := streamAgyNDJSONToOpenAISSEAttempt(ctx, src, dst, model, reqID, created, toolsSpec, false)
	return convID
}

func streamAgyNDJSONToOpenAISSEAttempt(
	ctx context.Context,
	src io.Reader,
	dst io.Writer,
	model string,
	reqID string,
	created int64,
	toolsSpec AdaptedToolsSpec,
	deferTransientError bool,
) (capturedConvID string, emittedAny bool, deferredErr string) {
	hasTools := toolsSpec.HasTools()
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	sentRole := false
	writeChunk := func(delta map[string]any, finishReason any, usage map[string]any) bool {
		if !sentRole && delta != nil {
			delta["role"] = "assistant"
			sentRole = true
		}
		chunk := map[string]any{
			"id":      reqID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         delta,
					"finish_reason": finishReason,
				},
			},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(dst, "data: %s\n\n", b)
		return err == nil
	}

	var (
		fullText       strings.Builder
		outInterceptor OutboundStreamInterceptor
		lastUsage      map[string]any
		hadToolCall    bool
	)

	toOpenAIUsage := func(u *agyUsage) map[string]any {
		if u == nil {
			return nil
		}
		prompt := u.InputTokens
		comp := u.OutputTokens + u.ThinkingTokens
		total := u.TotalTokens
		if total == 0 {
			total = prompt + comp
		}
		return map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": comp,
			"total_tokens":      total,
			"prompt_tokens_details": map[string]any{
				"cached_tokens": u.CacheReadTokens,
			},
		}
	}

	for scanner.Scan() {
		if ctx != nil && ctx.Err() != nil {
			return capturedConvID, emittedAny, ""
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev agyStreamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if capturedConvID == "" && strings.TrimSpace(ev.ConversationID) != "" {
			capturedConvID = strings.TrimSpace(ev.ConversationID)
		}
		switch ev.Event {
		case "step_update":
			if ev.StepUpdate == nil {
				continue
			}
			if capturedConvID == "" && strings.TrimSpace(ev.StepUpdate.ConversationID) != "" {
				capturedConvID = strings.TrimSpace(ev.StepUpdate.ConversationID)
			}
			if ev.StepUpdate.Usage != nil {
				lastUsage = toOpenAIUsage(ev.StepUpdate.Usage)
			}
			if ev.StepUpdate.ThinkingDelta != "" {
				cleanThinking := StripOutboundBridgeMarkers(ev.StepUpdate.ThinkingDelta)
				if cleanThinking != "" {
					emittedAny = true
					if !writeChunk(map[string]any{"reasoning_content": cleanThinking}, nil, nil) {
						return capturedConvID, emittedAny, ""
					}
				}
			}
			if ev.StepUpdate.TextDelta != "" {
				fullText.WriteString(ev.StepUpdate.TextDelta)
				if !hasTools {
					if cleanDelta := outInterceptor.Push(ev.StepUpdate.TextDelta); cleanDelta != "" {
						emittedAny = true
						if !writeChunk(map[string]any{"content": cleanDelta}, nil, nil) {
							return capturedConvID, emittedAny, ""
						}
					}
				}
			}
		case "result":
			if ev.Result == nil {
				continue
			}
			if capturedConvID == "" && strings.TrimSpace(ev.Result.ConversationID) != "" {
				capturedConvID = strings.TrimSpace(ev.Result.ConversationID)
			}
			if ev.Result.Usage != nil {
				lastUsage = toOpenAIUsage(ev.Result.Usage)
			}
			if strings.EqualFold(ev.Result.Status, "ERROR") || ev.Result.Error != "" {
				errMsg := firstNonEmptyStr(ev.Result.Error, "agy CLI returned error status")
				if deferTransientError && !emittedAny && netx.IsTransientNetworkErrorMessage(errMsg) {
					return capturedConvID, false, errMsg
				}
				writeAgySSEErrorAndDone(dst, errMsg)
				return capturedConvID, true, ""
			}
			if fullText.Len() == 0 && ev.Result.Response != "" {
				fullText.WriteString(ev.Result.Response)
				if !hasTools {
					if cleanResp := StripOutboundBridgeMarkers(ev.Result.Response); cleanResp != "" {
						emittedAny = true
						_ = writeChunk(map[string]any{"content": cleanResp}, nil, nil)
					}
				}
			}
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return capturedConvID, emittedAny, ""
		}
		errMsg := fmt.Sprintf("读取 agy CLI 流式输出失败: %v", scanErr)
		if deferTransientError && !emittedAny && netx.IsTransientNetworkErrorMessage(errMsg) {
			return capturedConvID, false, errMsg
		}
		writeAgySSEErrorAndDone(dst, errMsg)
		return capturedConvID, true, ""
	}

	if !hasTools {
		if tail := outInterceptor.Flush(); tail != "" {
			emittedAny = true
			_ = writeChunk(map[string]any{"content": tail}, nil, nil)
		}
	}

	// 若带有外部工具声明：支持 <AGY_TOOL_CALL> 与 <tool_call> 双格式混合正文 + 并行多工具提取，
	// 同步剥离内部桥接标记，并按 DeclaredTools 白名单拦截未声明的宿主内置工具幻觉。
	if hasTools {
		rawText := fullText.String()
		prose, calls := ExtractInterceptionAwareToolCalls(rawText, toolsSpec)
		if prose != "" {
			emittedAny = true
			_ = writeChunk(map[string]any{"content": prose}, nil, nil)
		}
		if len(calls) > 0 {
			hadToolCall = true
			for idx, tc := range calls {
				emittedAny = true
				_ = writeChunk(map[string]any{
					"tool_calls": []map[string]any{
						{
							"index": idx,
							"id":    fmt.Sprintf("call_agy_%s_%d", randomUUIDv4()[:8], idx),
							"type":  "function",
							"function": map[string]any{
								"name":      tc.Name,
								"arguments": tc.Arguments,
							},
						},
					},
				}, nil, nil)
			}
		}
	}

	if emittedAny || lastUsage != nil {
		finish := "stop"
		if hadToolCall {
			finish = "tool_calls"
		}
		_ = writeChunk(map[string]any{}, finish, lastUsage)
		_, _ = io.WriteString(dst, "data: [DONE]\n\n")
		return capturedConvID, true, ""
	}
	if !deferTransientError {
		_, _ = io.WriteString(dst, "data: [DONE]\n\n")
	}
	return capturedConvID, false, ""
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// cancelOnCloseReadCloser 在调用方关闭 HTTP 响应 Body 时立即取消底层子进程 Context，
// 防止客户端断连或空闲超时关闭流后 agy 子进程继续在后台缓冲或持有会话锁。
type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnCloseReadCloser) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	return c.ReadCloser.Close()
}

// limitedBuffer 将子进程 stderr 捕获上限限制在固定字节数内，防止异常刷屏导致内存膨胀。
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.limit <= 0 {
		return len(p), nil
	}
	remain := l.limit - l.buf.Len()
	if remain > 0 {
		if len(p) > remain {
			_, _ = l.buf.Write(p[:remain])
		} else {
			_, _ = l.buf.Write(p)
		}
	}
	return len(p), nil
}
