// Package netx 提供全链路强制 HTTP/2 协商（含 Google / 代理隧道 ALPN h2 + PING 保活）、
// 服务端 HTTP/2 (含 H2C) 以及针对代理链路瞬时 EOF / GOAWAY / 连接重置的透明自动重试能力。
package netx

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

const (
	// DefaultH2ReadIdleTimeout 空闲 HTTP/2 连接触发 PING 探活的间隔，防止代理或 Google GFE 静默掐断导致下一次请求报 EOF。
	DefaultH2ReadIdleTimeout = 25 * time.Second
	// DefaultH2PingTimeout HTTP/2 PING 探活超时，超期立刻淘汰半开死连接。
	DefaultH2PingTimeout = 15 * time.Second
	// DefaultDialTimeout 默认 TCP 建连超时。
	DefaultDialTimeout = 20 * time.Second
	// DefaultKeepAlive 默认 TCP KeepAlive 周期。
	DefaultKeepAlive = 30 * time.Second
)

// ConfigureHTTP2Transport 为 *http.Transport 强制开启完整 HTTP/2 能力（包含 HTTPS 直连、HTTP CONNECT 代理隧道、
// SOCKS5/SOCKS5H 代理隧道到 Google / OpenAI / Anthropic 等所有 HTTPS 上游）：
//   - 显式声明 TLS 1.2+ 与 ALPN ["h2", "http/1.1"]，优先走 HTTP/2
//   - 配置 Go 1.24+ Protocols 同时支持 HTTP/2 与 HTTP/1.1 回退
//   - 通过 golang.org/x/net/http2.ConfigureTransports 开启 ReadIdleTimeout + PingTimeout 帧级保活，根治空闲连接 EOF
func ConfigureHTTP2Transport(tr *http.Transport) *http2.Transport {
	if tr == nil {
		return nil
	}
	tr.ForceAttemptHTTP2 = true

	if tr.TLSHandshakeTimeout <= 0 {
		tr.TLSHandshakeTimeout = 15 * time.Second
	}
	if tr.ExpectContinueTimeout <= 0 {
		tr.ExpectContinueTimeout = 1 * time.Second
	}
	if tr.IdleConnTimeout <= 0 {
		tr.IdleConnTimeout = 90 * time.Second
	}

	var tlsCfg *tls.Config
	if tr.TLSClientConfig != nil {
		tlsCfg = tr.TLSClientConfig.Clone()
	} else {
		tlsCfg = &tls.Config{}
	}
	if tlsCfg.MinVersion == 0 || tlsCfg.MinVersion < tls.VersionTLS12 {
		tlsCfg.MinVersion = tls.VersionTLS12
	}
	tlsCfg.NextProtos = ensureH2FirstNextProtos(tlsCfg.NextProtos)
	tr.TLSClientConfig = tlsCfg

	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	tr.Protocols = &protos

	h2Tr, err := http2.ConfigureTransports(tr)
	if err == nil && h2Tr != nil {
		h2Tr.ReadIdleTimeout = DefaultH2ReadIdleTimeout
		h2Tr.PingTimeout = DefaultH2PingTimeout
		h2Tr.StrictMaxConcurrentStreams = true
		h2Tr.AllowHTTP = false
	}
	return h2Tr
}

// NewHTTP2Transport 构造一个默认启用 HTTP/2 + PING 保活 + TCP KeepAlive 的标准 *http.Transport。
func NewHTTP2Transport(proxyURL *url.URL) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   DefaultDialTimeout,
		KeepAlive: DefaultKeepAlive,
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	ConfigureHTTP2Transport(tr)
	return tr
}

// NewHTTP2Client 构造一个默认启用完整 HTTP/2 + PING 保活的 *http.Client（timeout=0 表示流式长连接安全）。
func NewHTTP2Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: NewHTTP2Transport(nil),
		Timeout:   timeout,
	}
}

// EnableServerHTTP2 为 Go *http.Server 启用 HTTP/1.1 + HTTP/2 + 明文 HTTP/2 (H2C) 协议支持，
// 降低网关与容器内 agy-bridge、反向代理之间的长连接开销与队头阻塞。
func EnableServerHTTP2(srv *http.Server) {
	if srv == nil {
		return
	}
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv.Protocols = &protos
}

// EnsureGODEBUGHTTP2 在环境变量切片中确保 Go 子进程（如真实 agy CLI）始终开启 HTTP/2 客户端
// （移除任何外部注入的 http2client=0，并保留其余 GODEBUG 标志）。
func EnsureGODEBUGHTTP2(env []string) []string {
	out := make([]string, 0, len(env)+1)
	var existingVal string
	for _, kv := range env {
		if len(kv) >= 8 && strings.EqualFold(kv[:8], "GODEBUG=") {
			existingVal = kv[8:]
			continue
		}
		out = append(out, kv)
	}

	var kept []string
	if strings.TrimSpace(existingVal) != "" {
		for _, part := range strings.Split(existingVal, ",") {
			p := strings.TrimSpace(part)
			if p == "" || strings.HasPrefix(strings.ToLower(p), "http2client=") {
				continue
			}
			kept = append(kept, p)
		}
	}
	kept = append(kept, "http2client=1")
	out = append(out, "GODEBUG="+strings.Join(kept, ","))
	return out
}

// IsTransientNetworkError 判断网络请求错误是否属于代理/网关常见的瞬时断流（EOF / GOAWAY / 连接重置），
// 这类错误在尚未向客户端发送首字节前可安全清空空闲连接池并立即重建 HTTP/2 连接重试。
func IsTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return IsTransientNetworkErrorMessage(err.Error())
}

// IsTransientNetworkErrorMessage 根据错误文本识别瞬时 EOF / GOAWAY / 代理握手中断。
func IsTransientNetworkErrorMessage(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return false
	}
	markers := []string{
		": eof",
		"unexpected eof",
		"connection reset by peer",
		"wsarecv:",
		"wsasend:",
		"broken pipe",
		"use of closed network connection",
		"http2: client connection lost",
		"http2: server sent goaway",
		"server closed idle connection",
		"stream error:",
		"tls handshake timeout",
		"eligibility check failed",
		"authentication failed or timed out",
		"failed to get profile picture",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return lower == "eof"
}

// DoWithEOFRetry 执行 HTTP 请求；遇到瞬时 EOF / GOAWAY / 连接重置时自动关闭陈旧空闲连接并重试最多 maxRetries 次。
func DoWithEOFRetry(
	ctx context.Context,
	client *http.Client,
	buildReq func() (*http.Request, error),
	maxRetries int,
) (*http.Response, error) {
	if client == nil {
		client = NewHTTP2Client(30 * time.Second)
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			client.CloseIdleConnections()
			backoff := time.Duration(250*attempt) * time.Millisecond
			if backoff > 1500*time.Millisecond {
				backoff = 1500 * time.Millisecond
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !IsTransientNetworkError(err) || attempt >= maxRetries {
			return nil, err
		}
	}
	return nil, lastErr
}

func ensureH2FirstNextProtos(existing []string) []string {
	out := []string{"h2", "http/1.1"}
	for _, p := range existing {
		p = strings.TrimSpace(p)
		if p == "" || p == "h2" || p == "http/1.1" {
			continue
		}
		out = append(out, p)
	}
	return out
}
