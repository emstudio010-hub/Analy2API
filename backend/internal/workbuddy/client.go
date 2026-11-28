package workbuddy

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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPITimeout       = 30 * time.Second
	defaultPingTimeout      = 25 * time.Second
	maxAPIResponseBodyBytes = 8 * 1024 * 1024
)

// Client 是 WorkBuddy 5.9.3 协议上游客户端，所有方法均按账号独立绑定代理 IP 与成对设备指纹。
type Client struct {
	mu                sync.RWMutex
	pool              *ProxyPool
	defaultDomain     string
	logger            *slog.Logger
	harvestPollDelay  time.Duration
}

var (
	defaultClientOnce sync.Once
	defaultClientInst *Client
)

// DefaultClient 返回进程级共享默认 WorkBuddy 客户端实例。
func DefaultClient() *Client {
	defaultClientOnce.Do(func() {
		defaultClientInst = NewClient("", DefaultDomain, nil)
	})
	return defaultClientInst
}

// NewClient 创建一个持有独立 ProxyPool 的 WorkBuddy 客户端实例。
func NewClient(defaultProxyURL, defaultDomain string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		pool:             NewProxyPool(defaultProxyURL),
		defaultDomain:    NormalizeDomain(defaultDomain),
		logger:           logger,
		harvestPollDelay: 2 * time.Second,
	}
}

// ProxyPool 返回客户端持有的账号代理池。
func (c *Client) ProxyPool() *ProxyPool {
	return c.pool
}

// SetDefaultProxy 更新客户端默认兜底代理（不覆盖账号自带 proxy_url）。
func (c *Client) SetDefaultProxy(proxyURL string) error {
	return c.pool.SetDefaultProxy(proxyURL)
}

// DefaultDomain 返回当前默认基础域名。
func (c *Client) DefaultDomain() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.defaultDomain == "" {
		return DefaultDomain
	}
	return c.defaultDomain
}

// SetDefaultDomain 更新客户端默认基础域名。
func (c *Client) SetDefaultDomain(domain string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defaultDomain = NormalizeDomain(domain)
}

func (c *Client) setHarvestPollDelay(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.harvestPollDelay = d
}

func (c *Client) getHarvestPollDelay() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.harvestPollDelay
}

// prepareCreds 规范化账号域名并确保成对设备指纹 qimei36 + machineId 已就绪。
func (c *Client) prepareCreds(creds AccountCreds) AccountCreds {
	creds.Domain = NormalizeDomain(fallbackString(creds.Domain, c.DefaultDomain()))
	creds.Qimei36, creds.MachineID = EnsureDevice(creds.Qimei36, creds.MachineID)
	return creds
}

func (c *Client) sendRequest(ctx context.Context, creds AccountCreds, method, path string, body any, headers http.Header) (*http.Response, error) {
	creds = c.prepareCreds(creds)

	targetURL := path
	lowerPath := strings.ToLower(strings.TrimSpace(path))
	if !strings.HasPrefix(lowerPath, "http://") && !strings.HasPrefix(lowerPath, "https://") {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		targetURL = creds.Domain + path
	}

	var bodyReader io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			bodyReader = bytes.NewReader(b)
		case string:
			bodyReader = strings.NewReader(b)
		default:
			raw, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("marshal request body: %w", err)
			}
			bodyReader = bytes.NewReader(raw)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, targetURL, err)
	}
	if headers != nil {
		req.Header = headers.Clone()
	}

	httpClient, err := c.pool.ClientForAccount(creds)
	if err != nil {
		return nil, fmt.Errorf("select account proxy client: %w", err)
	}

	return httpClient.Do(req)
}

func (c *Client) doAPI(ctx context.Context, creds AccountCreds, method, path string, body any, accept string, extra http.Header) ([]byte, map[string]any, int, string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultAPITimeout)
		defer cancel()
	}

	creds = c.prepareCreds(creds)
	hdrs := DesktopHeaders(creds, accept, extra)
	if body == nil && method == http.MethodPost {
		// DeviceAuth 等接口严禁传 {} body，显式保持 nil body；部分服务端需要 Content-Length: 0。
	}

	resp, err := c.sendRequest(ctx, creds, method, path, body, hdrs)
	if err != nil {
		return nil, nil, 0, "", err
	}
	defer resp.Body.Close()

	rawBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBodyBytes))
	if readErr != nil {
		return nil, nil, resp.StatusCode, "", fmt.Errorf("read response body: %w", readErr)
	}

	reqID := firstHeader(resp.Header, "x-request-id", "X-Request-Id")
	var root map[string]any
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(rawBytes))
		dec.UseNumber()
		var parsed any
		if decErr := dec.Decode(&parsed); decErr == nil {
			if m, ok := parsed.(map[string]any); ok {
				root = m
			}
		}
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return rawBytes, root, resp.StatusCode, reqID, buildAPIError(resp.StatusCode, reqID, rawBytes, root)
	}

	if root != nil {
		if code, hasCode := extractAPICode(root); hasCode && code != CodeOK {
			apiErr := buildAPIError(resp.StatusCode, reqID, rawBytes, root)
			apiErr.Code = code
			return rawBytes, root, resp.StatusCode, reqID, apiErr
		}
	}

	return rawBytes, root, resp.StatusCode, reqID, nil
}

// ChatStream 发起 WorkBuddy 流式对话请求（POST {Domain}/v2/chat/completions），返回未关闭的 SSE 响应流。
func (c *Client) ChatStream(ctx context.Context, creds AccountCreds, openaiPayload []byte) (*http.Response, error) {
	creds = c.prepareCreds(creds)
	if strings.TrimSpace(creds.AccessToken) == "" {
		return nil, fmt.Errorf("workbuddy access token is empty")
	}

	var payloadMap map[string]any
	if err := json.Unmarshal(openaiPayload, &payloadMap); err != nil {
		return nil, fmt.Errorf("invalid chat payload json: %w", err)
	}

	bodyToSend := openaiPayload
	if streamFlag, ok := payloadMap["stream"].(bool); !ok || !streamFlag {
		payloadMap["stream"] = true
		patched, err := json.Marshal(payloadMap)
		if err != nil {
			return nil, fmt.Errorf("marshal patched chat payload: %w", err)
		}
		bodyToSend = patched
	}

	hdrs := ChatHeaders(creds)
	resp, err := c.sendRequest(ctx, creds, http.MethodPost, PathChatCompletions, bodyToSend, hdrs)
	if err != nil {
		return nil, err
	}

	reqID := firstHeader(resp.Header, "x-request-id", "X-Request-Id")
	if resp.StatusCode >= http.StatusBadRequest {
		rawBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBodyBytes))
		_ = resp.Body.Close()
		var root map[string]any
		_ = json.Unmarshal(rawBytes, &root)
		return nil, buildAPIError(resp.StatusCode, reqID, rawBytes, root)
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/json") {
		rawBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBodyBytes))
		_ = resp.Body.Close()
		var root map[string]any
		_ = json.Unmarshal(rawBytes, &root)
		if root != nil {
			if code, hasCode := extractAPICode(root); hasCode && code != CodeOK {
				apiErr := buildAPIError(resp.StatusCode, reqID, rawBytes, root)
				apiErr.Code = code
				return nil, apiErr
			}
			if _, hasErr := root["error"]; hasErr {
				return nil, buildAPIError(resp.StatusCode, reqID, rawBytes, root)
			}
		}
		return nil, &APIError{
			Status:    resp.StatusCode,
			Message:   "unexpected non-stream json response from chat endpoint",
			RequestID: reqID,
			Raw:       truncateRaw(rawBytes),
		}
	}

	return resp, nil
}

// Ping 通过最小 stream:true, max_tokens:1 对话探测账号与代理连通性，自动跳过 : heartbeat SSE 注释帧。
func (c *Client) Ping(ctx context.Context, creds AccountCreds, model string) (time.Duration, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultPingTimeout)
		defer cancel()
	}

	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultFastModel
	}

	payload, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
		"stream":     true,
		"max_tokens": 1,
	})
	if err != nil {
		return 0, err
	}

	start := time.Now()
	resp, err := c.ChatStream(ctx, creds, payload)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	reqID := firstHeader(resp.Header, "x-request-id", "X-Request-Id")
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			// WorkBuddy 首帧常发送 ": heartbeat" SSE 保活注释，必须跳过继续等待有效事件。
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return time.Since(start), nil
		}

		var frame map[string]any
		if json.Unmarshal([]byte(data), &frame) == nil {
			if code, hasCode := extractAPICode(frame); hasCode && code != CodeOK {
				apiErr := buildAPIError(resp.StatusCode, reqID, []byte(data), frame)
				apiErr.Code = code
				return 0, apiErr
			}
			if _, hasErr := frame["error"]; hasErr {
				return 0, buildAPIError(resp.StatusCode, reqID, []byte(data), frame)
			}
		}
		return time.Since(start), nil
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read ping stream: %w", err)
	}
	return 0, fmt.Errorf("ping stream closed before receiving any data frame")
}

// Models 获取并过滤当前账号可用对话模型目录；上游不可用或返回为空时返回权威兜底模型列表。
func (c *Client) Models(ctx context.Context, creds AccountCreds) ([]ModelInfo, error) {
	creds = c.prepareCreds(creds)
	eid := strings.TrimSpace(creds.EnterpriseID)
	if eid == "" {
		eid = "personal"
	}

	path := fmt.Sprintf("/v2/enterprises/%s/models", url.PathEscape(eid))
	extra := AuthHeaders(creds, false, true)

	_, root, _, _, err := c.doAPI(ctx, creds, http.MethodGet, path, nil, "application/json", extra)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.logger.Warn("workbuddy models fetch failed, falling back to default catalog", "error", err)
		return DefaultModels(), nil
	}

	parsed := parseModelsCatalog(root)
	if len(parsed) == 0 {
		return DefaultModels(), nil
	}
	return parsed, nil
}

// IsNonChatModel 过滤代码补全/NES/生图/超小输出上限等非对话模型。
func IsNonChatModel(modelID string, maxOutputTokens int, tags []string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return true
	}
	for _, prefix := range []string{"nes-", "completion-", "codewise-", "hunyuan-image-"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	for _, substr := range []string{"text-to-image", "image-to-image"} {
		if strings.Contains(id, substr) {
			return true
		}
	}
	if maxOutputTokens > 0 && maxOutputTokens <= 256 {
		return true
	}
	for _, tag := range tags {
		t := strings.ToLower(strings.TrimSpace(tag))
		switch t {
		case "text-to-image", "image-to-image", "image-generation", "embedding", "rerank", "tts", "asr", "nes", "completion", "non-chat":
			return true
		}
	}
	return false
}

// DefaultModels 返回 WorkBuddy 真实线上可用对话模型基准目录（移除虚构模型名）。
func DefaultModels() []ModelInfo {
	base := []ModelInfo{
		{ID: "hy4std", Name: "Hy4", Vendor: "tencent", ContextWindow: 960000, MaxOutputTokens: 64000, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "hy4flash", Name: "Hy4 flash", Vendor: "tencent", ContextWindow: 960000, MaxOutputTokens: 64000, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "deepseek-v4.1-flash", Name: "DeepSeek-V4.1-Flash", Vendor: "deepseek", ContextWindow: 1000000, MaxOutputTokens: 128000, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "glm-5.4", Name: "GLM-5.4", Vendor: "zhipu", ContextWindow: 1000000, MaxOutputTokens: 32768, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "glm-5.3", Name: "GLM-5.3", Vendor: "zhipu", ContextWindow: 1000000, MaxOutputTokens: 64000, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "glm-5.3-flash", Name: "GLM-5.3-Flash", Vendor: "zhipu", ContextWindow: 1000000, MaxOutputTokens: 131072, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "glm-5.2", Name: "GLM-5.2", Vendor: "zhipu", ContextWindow: 1000000, MaxOutputTokens: 64000, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "kimi-k3.1-pro", Name: "Kimi-K3.1-Pro", Vendor: "moonshot", ContextWindow: 1000000, MaxOutputTokens: 32000, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "kimi-k3-1", Name: "Kimi-K3", Vendor: "moonshot", ContextWindow: 1000000, MaxOutputTokens: 32000, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "kimi-k2.8-preview", Name: "Kimi-K2.8-Preview", Vendor: "moonshot", ContextWindow: 1000000, MaxOutputTokens: 64000, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "claude-sonnet-4.5", Name: "Claude Sonnet 4.5", Vendor: "anthropic", ContextWindow: 200000, MaxOutputTokens: 16384, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "claude-opus-4.5", Name: "Claude Opus 4.5", Vendor: "anthropic", ContextWindow: 200000, MaxOutputTokens: 16384, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "claude-3.7-sonnet", Name: "Claude 3.7 Sonnet", Vendor: "anthropic", ContextWindow: 200000, MaxOutputTokens: 16384, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Vendor: "google", ContextWindow: 1000000, MaxOutputTokens: 16384, SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true},
	}
	out := make([]ModelInfo, len(base))
	copy(out, base)
	return out
}

func parseModelsCatalog(root map[string]any) []ModelInfo {
	if root == nil {
		return nil
	}
	data := unwrapDataMap(root)

	rawModels := extractSlice(data, "models")
	if len(rawModels) == 0 {
		rawModels = extractSlice(data, "list")
	}
	if len(rawModels) == 0 {
		rawModels = extractSlice(root, "models")
	}

	byID := make(map[string]ModelInfo, len(rawModels))
	order := make([]string, 0, len(rawModels))

	for _, item := range rawModels {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if parseBoolValue(m["disabled"]) {
			continue
		}
		if enabledVal, exists := m["enabled"]; exists && !parseBoolValue(enabledVal) {
			continue
		}

		id := firstNonEmptyString(m, "id", "modelId", "model", "name")
		if id == "" {
			continue
		}
		name := firstNonEmptyString(m, "name", "displayName", "id")
		if name == "" {
			name = id
		}

		tags := extractStringSlice(m["tags"])
		maxOutput := firstInt(m, "maxOutputTokens", "max_output_tokens")
		if IsNonChatModel(id, maxOutput, tags) {
			continue
		}

		ctxWin := firstInt(m, "contextWindow", "maxContextTokens", "context_window", "maxInputTokens", "max_input_tokens")
		maxAllowed := firstInt(m, "maxAllowedSize", "max_allowed_size")

		caps, _ := m["capabilities"].(map[string]any)
		supportsImg := parseBoolValue(m["supportsImages"]) || parseBoolValue(m["supportImage"]) || parseBoolValue(m["supports_images"])
		supportsTool := parseBoolValue(m["supportsToolCall"]) || parseBoolValue(m["supportFunctionCall"]) || parseBoolValue(m["supports_tool_call"])
		if caps != nil {
			supportsImg = supportsImg || parseBoolValue(caps["image"]) || parseBoolValue(caps["vision"]) || parseBoolValue(caps["supportsImages"])
			supportsTool = supportsTool || parseBoolValue(caps["toolCall"]) || parseBoolValue(caps["functionCall"]) || parseBoolValue(caps["tools"])
		}

		supportsReasoning := parseBoolValue(m["supportsReasoning"]) || parseBoolValue(m["supports_reasoning"])
		onlyReasoning := parseBoolValue(m["onlyReasoning"]) || parseBoolValue(m["only_reasoning"])
		var canDisable *bool
		if v, exists := m["canDisableThinking"]; exists {
			b := parseBoolValue(v)
			canDisable = &b
		}
		efforts := extractStringSlice(m["supportedEfforts"])
		defaultEffort := firstNonEmptyString(m, "defaultEffort")

		if rMap, ok := m["reasoning"].(map[string]any); ok {
			if parseBoolValue(rMap["supported"]) || parseBoolValue(rMap["enabled"]) {
				supportsReasoning = true
			}
			if parseBoolValue(rMap["onlyReasoning"]) {
				onlyReasoning = true
			}
			if v, exists := rMap["canDisableThinking"]; exists {
				b := parseBoolValue(v)
				canDisable = &b
			}
			if len(efforts) == 0 {
				efforts = extractStringSlice(rMap["supportedEfforts"])
			}
			if defaultEffort == "" {
				defaultEffort = firstNonEmptyString(rMap, "defaultEffort")
			}
		}
		if onlyReasoning {
			supportsReasoning = true
		}

		var multPtr *float64
		if mult, ok := parseOptionalFloat(m, "creditMultiplier", "credit_multiplier"); ok {
			multPtr = &mult
		}

		info := ModelInfo{
			ID:                 id,
			Name:               name,
			Vendor:             firstNonEmptyString(m, "vendor", "provider", "ownedBy"),
			Description:        firstNonEmptyString(m, "description"),
			Credits:            firstNonEmptyString(m, "credits"),
			CreditMultiplier:   multPtr,
			ContextWindow:      ctxWin,
			MaxOutputTokens:    maxOutput,
			MaxAllowedSize:     maxAllowed,
			SupportsImages:     supportsImg,
			SupportsReasoning:  supportsReasoning,
			SupportsToolCall:   supportsTool,
			OnlyReasoning:      onlyReasoning,
			CanDisableThinking: canDisable,
			SupportedEfforts:   efforts,
			DefaultEffort:      defaultEffort,
			Tags:               tags,
			IconURL:            firstNonEmptyString(m, "iconUrl", "icon_url", "icon"),
		}
		if _, exists := byID[id]; !exists {
			order = append(order, id)
		}
		byID[id] = info
	}

	if len(byID) == 0 {
		return nil
	}

	// 若上游返回 agents[name=="cli"].models 列表，优先按 CLI 允许顺序排序过滤。
	cliModels := extractCLIAgentModels(data)
	if len(cliModels) > 0 {
		filtered := make([]ModelInfo, 0, len(cliModels))
		seen := make(map[string]struct{}, len(cliModels))
		for _, id := range cliModels {
			info, exists := byID[id]
			if !exists {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			filtered = append(filtered, info)
		}
		if len(filtered) > 0 {
			return filtered
		}
	}

	out := make([]ModelInfo, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func extractCLIAgentModels(data map[string]any) []string {
	agents := extractSlice(data, "agents")
	for _, item := range agents {
		agentMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(firstNonEmptyString(agentMap, "name", "id"), "cli") {
			return extractStringSlice(agentMap["models"])
		}
	}
	return nil
}

func unwrapDataMap(root map[string]any) map[string]any {
	if root == nil {
		return map[string]any{}
	}
	if d, ok := root["data"].(map[string]any); ok && d != nil {
		return d
	}
	return root
}

func firstNonEmptyString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		switch t := val.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case json.Number:
			if s := strings.TrimSpace(t.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func fallbackString(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func extractSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	if s, ok := m[key].([]any); ok {
		return s
	}
	return nil
}

func extractStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		switch t := item.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				out = append(out, s)
			}
		case map[string]any:
			if s := firstNonEmptyString(t, "id", "modelId", "name"); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func firstInt(m map[string]any, keys ...string) int {
	return int(firstInt64Any(m, keys...))
}

func firstInt64Any(m map[string]any, keys ...string) int64 {
	if m == nil {
		return 0
	}
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		if n, ok := toInt64(val); ok {
			return n
		}
	}
	return 0
}

func toInt64(val any) (int64, bool) {
	switch t := val.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, true
		}
		if f, err := t.Float64(); err == nil {
			return int64(f), true
		}
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), true
		}
	}
	return 0, false
}

func parseOptionalFloat(m map[string]any, keys ...string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		if f, ok := toFloat64(val); ok {
			return f, true
		}
	}
	return 0, false
}

func toFloat64(val any) (float64, bool) {
	switch t := val.(type) {
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, true
		}
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func parseBoolValue(val any) bool {
	switch t := val.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return t != 0
	case int:
		return t != 0
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n != 0
		}
	}
	return false
}

func extractAPICode(root map[string]any) (int, bool) {
	if root == nil {
		return 0, false
	}
	if v, ok := root["code"]; ok && v != nil {
		if n, ok2 := toInt64(v); ok2 {
			return int(n), true
		}
	}
	if errMap, ok := root["error"].(map[string]any); ok && errMap != nil {
		if v, exists := errMap["code"]; exists && v != nil {
			if n, ok2 := toInt64(v); ok2 {
				return int(n), true
			}
		}
	}
	return 0, false
}

func buildAPIError(status int, reqID string, rawBytes []byte, root map[string]any) *APIError {
	var code int
	var msg string
	if root != nil {
		if c, ok := extractAPICode(root); ok {
			code = c
		}
		msg = firstNonEmptyString(root, "message", "msg", "detail", "error_description")
		if msg == "" {
			if errMap, ok := root["error"].(map[string]any); ok && errMap != nil {
				msg = firstNonEmptyString(errMap, "message", "msg", "detail")
			} else if errStr, ok := root["error"].(string); ok {
				msg = strings.TrimSpace(errStr)
			}
		}
	}
	return &APIError{
		Status:    status,
		Code:      code,
		Message:   msg,
		RequestID: reqID,
		Raw:       truncateRaw(rawBytes),
	}
}

func firstHeader(h http.Header, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func truncateRaw(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 1024 {
		return s[:1024] + "..."
	}
	return s
}

func unwrapNestedError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}
