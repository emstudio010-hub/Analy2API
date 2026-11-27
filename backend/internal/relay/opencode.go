package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenCode 上游是 OpenAI 兼容协议：请求基本直通，不需要信封、指纹或设备伪装。

// openCodeGenerate 向 OpenCode 上游发起一次 chat completion 请求。
func (c *Client) openCodeGenerate(ctx context.Context, baseURL, apiKey string, body []byte) (*http.Response, error) {
	return c.openCodeGenerateWithProxy(ctx, baseURL, apiKey, "", body)
}

// openCodeGenerateWithProxy 按账号独立代理向 OpenCode 上游发起一次 chat completion 请求。
func (c *Client) openCodeGenerateWithProxy(ctx context.Context, baseURL, apiKey, accountProxyURL string, body []byte) (*http.Response, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "cmd2api")

	httpCli, err := c.httpClientForProxy(accountProxyURL)
	if err != nil {
		return nil, fmt.Errorf("选择 OpenCode 账号代理客户端: %w", err)
	}
	return httpCli.Do(req)
}

// probeModelPreference 是挑探活模型时的偏好关键词，越靠前越优先。
var probeModelPreference = []string{"flash", "mini", "lite", "haiku", "small", "m2.5", "m2.7"}

// pickProbeModel 从该部署实际支持的模型列表里挑一个探活用模型。
func (c *Client) pickProbeModel(ctx context.Context, baseURL, apiKey string) string {
	return c.pickProbeModelWithProxy(ctx, baseURL, apiKey, "")
}

func (c *Client) pickProbeModelWithProxy(ctx context.Context, baseURL, apiKey, accountProxyURL string) string {
	models := c.openCodeModelsWithProxy(ctx, baseURL, apiKey, accountProxyURL)
	if len(models) == 0 {
		return ""
	}
	for _, want := range probeModelPreference {
		for _, m := range models {
			if strings.Contains(strings.ToLower(m.ID), want) {
				return m.ID
			}
		}
	}
	return models[0].ID
}

// openCodeProbe 探活一个 OpenCode 账号。
func (c *Client) openCodeProbe(ctx context.Context, baseURL, apiKey string) ProbeResult {
	return c.openCodeProbeWithProxy(ctx, baseURL, apiKey, "")
}

func (c *Client) openCodeProbeWithProxy(ctx context.Context, baseURL, apiKey, accountProxyURL string) ProbeResult {
	started := time.Now()

	model := c.pickProbeModelWithProxy(ctx, baseURL, apiKey, accountProxyURL)
	if model == "" {
		return ProbeResult{Latency: time.Since(started), Err: &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error",
			Message: "拿不到 OpenCode 的模型列表，无法完成探活（检查网络或代理配置）",
		}}
	}

	payload := map[string]any{
		"model":      model,
		"messages":   []map[string]any{{"role": "user", "content": probePrompt}},
		"max_tokens": 1,
		"stream":     false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ProbeResult{Latency: time.Since(started), Err: &MappedError{
			Status: http.StatusInternalServerError, Type: "api_error",
			Message: "构造探活请求失败: " + err.Error(),
		}}
	}

	resp, err := c.openCodeGenerateWithProxy(ctx, baseURL, apiKey, accountProxyURL, body)
	if err != nil {
		if ctx.Err() != nil {
			return ProbeResult{Latency: time.Since(started), Err: &MappedError{
				Status: http.StatusGatewayTimeout, Type: "upstream_error",
				Message: "探活超时",
			}}
		}
		return ProbeResult{Latency: time.Since(started), Err: &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error",
			Message: "无法连接 OpenCode 上游: " + err.Error(),
		}}
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode != http.StatusOK {
		mapped := mapOpenCodeError(resp.StatusCode, raw)
		mapped.Message = fmt.Sprintf("%s（探活模型：%s）", mapped.Message, model)
		return ProbeResult{Latency: time.Since(started), Err: mapped}
	}

	var parsed struct {
		Choices []json.RawMessage `json:"choices"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ProbeResult{Latency: time.Since(started), Err: &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error",
			Message: "上游返回的不是合法 JSON",
		}}
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return ProbeResult{Latency: time.Since(started), Err: &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error",
			Message: parsed.Error.Message,
		}}
	}
	if len(parsed.Choices) == 0 {
		return ProbeResult{Latency: time.Since(started), Err: &MappedError{
			Status: http.StatusBadGateway, Type: "upstream_error",
			Message: "上游返回成功状态但没有 choices",
		}}
	}

	return ProbeResult{Latency: time.Since(started)}
}

// openCodeModels 拉取 OpenCode 的模型列表。
func (c *Client) openCodeModels(ctx context.Context, baseURL, apiKey string) []Model {
	return c.openCodeModelsWithProxy(ctx, baseURL, apiKey, "")
}

func (c *Client) openCodeModelsWithProxy(ctx context.Context, baseURL, apiKey, accountProxyURL string) []Model {
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		c.logger.Warn("构造 OpenCode 模型列表请求失败", "err", err)
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("User-Agent", "cmd2api")

	httpCli, err := c.httpClientForProxy(accountProxyURL)
	if err != nil {
		c.logger.Warn("选择 OpenCode 代理失败", "err", err)
		return nil
	}

	resp, err := httpCli.Do(req)
	if err != nil {
		c.logger.Warn("拉取 OpenCode 模型列表失败", "err", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logger.Warn("拉取 OpenCode 模型列表返回非 200", "status", resp.StatusCode)
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.logger.Warn("读取 OpenCode 模型列表失败", "err", err)
		return nil
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		c.logger.Warn("OpenCode 模型列表格式不符合预期", "err", err)
		return nil
	}

	out := make([]Model, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID != "" {
			out = append(out, Model{ID: m.ID, Name: m.ID})
		}
	}
	return out
}

// mapOpenCodeError 把 OpenAI 兼容上游的错误映射成统一结构。
func mapOpenCodeError(status int, body []byte) *MappedError {
	mapped := MapHTTPError(status, body)
	mapped.Message = strings.TrimSpace(mapped.Message)
	if mapped.Message == "" {
		mapped.Message = "OpenCode 上游返回错误"
	}
	return mapped
}

// readOpenAISSE 逐行读 OpenAI SSE 流，把每个 data 载荷交给 handle。
func (c *Client) readOpenAISSE(ctx context.Context, body io.ReadCloser, idle time.Duration, handle func(payload string)) error {
	if idle <= 0 {
		idle = 180 * time.Second
	}
	return c.readOpenAISSEWithTimeouts(ctx, body, idle, idle, nil, handle)
}

// readOpenAISSEWithTimeouts 支持首帧等待超时（firstTokenTimeout）与出字后分片空闲超时（idleTimeout）动态切换。
func (c *Client) readOpenAISSEWithTimeouts(
	ctx context.Context,
	body io.ReadCloser,
	firstTokenTimeout time.Duration,
	idleTimeout time.Duration,
	hasVisible func() bool,
	handle func(payload string),
) error {
	if idleTimeout <= 0 {
		idleTimeout = 180 * time.Second
	}
	if firstTokenTimeout <= 0 {
		firstTokenTimeout = 300 * time.Second
	}

	watchdog := time.AfterFunc(firstTokenTimeout, func() { _ = body.Close() })
	defer watchdog.Stop()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if hasVisible != nil && hasVisible() {
			watchdog.Reset(idleTimeout)
		} else {
			watchdog.Reset(firstTokenTimeout)
		}
		payload, ok := SplitSSELine(scanner.Text())
		if !ok {
			continue
		}
		handle(payload)
		if hasVisible != nil && hasVisible() {
			watchdog.Reset(idleTimeout)
		}
	}
	return scanner.Err()
}
