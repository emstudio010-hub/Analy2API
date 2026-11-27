package workbuddy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/netx"

	"golang.org/x/net/proxy"
)

const (
	directProxyKeyword = "direct"
	proxyDialTimeout   = 15 * time.Second
	proxyTestTimeout   = 15 * time.Second
	ipEchoTimeout      = 5 * time.Second
)

var defaultIPEchoURLs = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://ipinfo.io/ip",
}

type pooledClient struct {
	transport *http.Transport
	client    *http.Client
}

// ProxyPool 按规范化代理 URL 线程安全缓存独立的 *http.Transport 与 *http.Client。
//
// 所有连接均显式禁用 http.ProxyFromEnvironment，彻底防止宿主环境变量（HTTP_PROXY 等）
// 污染单个账号的独立出口 IP。
type ProxyPool struct {
	mu           sync.RWMutex
	defaultProxy string
	entries      map[string]*pooledClient
}

// NewProxyPool 创建一个独立代理连接池，可传入全局默认代理地址（为空或 "direct" 表示直连）。
func NewProxyPool(defaultProxyURL string) *ProxyPool {
	norm, err := NormalizeProxyURL(defaultProxyURL)
	if err != nil {
		norm = ""
	}
	return &ProxyPool{
		defaultProxy: norm,
		entries:      make(map[string]*pooledClient),
	}
}

// SetDefaultProxy 动态更新全局默认代理地址。
func (p *ProxyPool) SetDefaultProxy(defaultProxyURL string) error {
	norm, err := NormalizeProxyURL(defaultProxyURL)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.defaultProxy = norm
	p.mu.Unlock()
	return nil
}

// DefaultProxy 返回当前规范化后的默认代理 URL。
func (p *ProxyPool) DefaultProxy() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.defaultProxy
}

// ResolveProxy 解析最终生效的代理 URL。
//
// 优先级：
//  1. accountProxyURL 为 "direct"（忽略大小写）-> 返回 ""（强制直连，覆盖全局默认代理）
//  2. accountProxyURL 非空 -> 100% 优先使用账号独立代理
//  3. accountProxyURL 为空 -> 回退 defaultProxyURL（若为 "direct" 或空则直连）
func ResolveProxy(accountProxyURL, defaultProxyURL string) string {
	acc := strings.TrimSpace(accountProxyURL)
	if strings.EqualFold(acc, directProxyKeyword) {
		return ""
	}
	if acc != "" {
		if norm, err := NormalizeProxyURL(acc); err == nil {
			return norm
		}
		return acc
	}
	def := strings.TrimSpace(defaultProxyURL)
	if def == "" || strings.EqualFold(def, directProxyKeyword) {
		return ""
	}
	if norm, err := NormalizeProxyURL(def); err == nil {
		return norm
	}
	return def
}

// ValidateProxyURL 校验代理 URL 是否合法（空或 "direct"、http/https/socks5/socks5h）。
func ValidateProxyURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, directProxyKeyword) {
		return nil
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return errors.New("代理地址不能包含空白或换行字符")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("代理地址无法解析: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("不支持的代理协议 %q（仅支持 http / https / socks5 / socks5h / direct）", u.Scheme)
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return errors.New("代理地址缺少主机名")
	}
	if portStr := u.Port(); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("代理端口无效: %q", portStr)
		}
	}
	return nil
}

// NormalizeProxyURL 规范化代理 URL；空串或 "direct" 统一返回 ""。
func NormalizeProxyURL(raw string) (string, error) {
	if err := ValidateProxyURL(raw); err != nil {
		return "", err
	}
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, directProxyKeyword) {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String(), nil
}

// RedactProxyURL 隐藏代理地址中的口令信息以便安全日志输出与展示。
func RedactProxyURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.EqualFold(s, directProxyKeyword) {
		return directProxyKeyword
	}
	u, err := url.Parse(s)
	if err != nil {
		return "(无效代理)"
	}
	if u.User != nil {
		username := u.User.Username()
		if _, hasPass := u.User.Password(); hasPass && username != "" {
			u.User = url.UserPassword(username, "REDACTED")
			return strings.ReplaceAll(u.String(), ":REDACTED@", ":***@")
		}
		u.User = url.User("REDACTED")
		return strings.ReplaceAll(u.String(), "REDACTED@", "***@")
	}
	return u.String()
}

// PreserveRedactedProxyPassword 在前端回传脱敏代理地址（如 http://user:***@ip:port）时，
// 若协议、主机端口与用户名跟数据库原代理一致，自动还原并保留原真实密码，防止编辑操作把密码覆盖成字面量 "***"。
func PreserveRedactedProxyPassword(newRaw, existingRaw string) string {
	n := strings.TrimSpace(newRaw)
	if n == "" || (!strings.Contains(n, "***") && !strings.Contains(strings.ToUpper(n), "%2A%2A%2A")) {
		return n
	}
	e := strings.TrimSpace(existingRaw)
	if e == "" {
		return n
	}
	uNew, errNew := url.Parse(n)
	uOld, errOld := url.Parse(e)
	if errNew != nil || errOld != nil || uNew.User == nil || uOld.User == nil {
		return n
	}
	if !strings.EqualFold(uNew.Scheme, uOld.Scheme) || !strings.EqualFold(uNew.Host, uOld.Host) {
		return n
	}
	newUser := uNew.User.Username()
	newPass, hasNewPass := uNew.User.Password()
	oldUser := uOld.User.Username()
	oldPass, hasOldPass := uOld.User.Password()

	if hasNewPass && newPass == "***" && newUser == oldUser && hasOldPass {
		uNew.User = url.UserPassword(oldUser, oldPass)
		return uNew.String()
	}
	if !hasNewPass && newUser == "***" {
		if hasOldPass {
			uNew.User = url.UserPassword(oldUser, oldPass)
		} else {
			uNew.User = url.User(oldUser)
		}
		return uNew.String()
	}
	return n
}

// Client 返回对应账号代理 URL 的缓存 *http.Client（不设 Client.Timeout，避免流式响应被掐断）。
func (p *ProxyPool) Client(accountProxyURL string) (*http.Client, error) {
	_, client, _, err := p.resolveEntry(accountProxyURL, false)
	return client, err
}

// Transport 返回对应账号代理 URL 的缓存 *http.Transport 与规范化后的代理键。
func (p *ProxyPool) Transport(accountProxyURL string) (*http.Transport, string, error) {
	tr, _, key, err := p.resolveEntry(accountProxyURL, false)
	return tr, key, err
}

// ClientForAccount 根据账号凭据中的 ProxyURL 返回其隔离的 *http.Client。
func (p *ProxyPool) ClientForAccount(creds AccountCreds) (*http.Client, error) {
	return p.Client(creds.ProxyURL)
}

// CloseIdleConnections 关闭池内所有 Transport 的空闲连接。
func (p *ProxyPool) CloseIdleConnections() {
	p.mu.RLock()
	entries := make([]*pooledClient, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e)
	}
	p.mu.RUnlock()
	for _, e := range entries {
		e.transport.CloseIdleConnections()
	}
}

func (p *ProxyPool) resolveEntry(accountProxyURL string, bypassDefault bool) (*http.Transport, *http.Client, string, error) {
	raw := strings.TrimSpace(accountProxyURL)
	if raw != "" {
		if err := ValidateProxyURL(raw); err != nil {
			return nil, nil, "", err
		}
	}
	def := ""
	if !bypassDefault {
		def = p.DefaultProxy()
	}
	key := ResolveProxy(raw, def)
	if key != "" {
		norm, err := NormalizeProxyURL(key)
		if err != nil {
			return nil, nil, "", err
		}
		key = norm
	}

	p.mu.RLock()
	if entry, ok := p.entries[key]; ok {
		p.mu.RUnlock()
		return entry.transport, entry.client, key, nil
	}
	p.mu.RUnlock()

	entry, err := buildPooledClient(key)
	if err != nil {
		return nil, nil, "", err
	}

	p.mu.Lock()
	if existing, ok := p.entries[key]; ok {
		p.mu.Unlock()
		entry.transport.CloseIdleConnections()
		return existing.transport, existing.client, key, nil
	}
	p.entries[key] = entry
	p.mu.Unlock()
	return entry.transport, entry.client, key, nil
}

func buildPooledClient(normalizedProxy string) (*pooledClient, error) {
	baseDialer := &net.Dialer{
		Timeout:   proxyDialTimeout,
		KeepAlive: 30 * time.Second,
	}
	// 刻意将 Proxy 设为 nil，绝不使用 http.ProxyFromEnvironment，
	// 防止进程环境变量（HTTP_PROXY/HTTPS_PROXY）导致多账号出口 IP 串流污染。
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           baseDialer.DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	if normalizedProxy != "" {
		u, err := url.Parse(normalizedProxy)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			tr.Proxy = http.ProxyURL(u)
		case "socks5", "socks5h":
			dialer, err := proxy.FromURL(u, baseDialer)
			if err != nil {
				return nil, fmt.Errorf("构造 SOCKS5 代理拨号器失败: %w", err)
			}
			if cd, ok := dialer.(proxy.ContextDialer); ok {
				tr.DialContext = cd.DialContext
			} else {
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					type dialRes struct {
						c   net.Conn
						err error
					}
					ch := make(chan dialRes, 1)
					go func() {
						c, err := dialer.Dial(network, addr)
						ch <- dialRes{c, err}
					}()
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case r := <-ch:
						return r.c, r.err
					}
				}
			}
		default:
			return nil, fmt.Errorf("不支持的代理协议: %s", u.Scheme)
		}
	}

	// 强制启用完整 HTTP/2 协商（ALPN h2 + ReadIdleTimeout/PingTimeout 帧级保活），
	// 覆盖直连、HTTP/HTTPS 代理隧道与 SOCKS5/SOCKS5H 隧道访问 Google / WorkBuddy 等全部 HTTPS 上游，
	// 防止空闲连接被代理静默断开引发 EOF。
	netx.ConfigureHTTP2Transport(tr)

	// 不设 http.Client.Timeout，流式长回答不会在途中被整体超时强行掐断。
	client := &http.Client{
		Transport: tr,
	}
	return &pooledClient{transport: tr, client: client}, nil
}

// TestProxy 通过指定代理探测 https://www.codebuddy.cn 连通性与出口 IP。
func TestProxy(ctx context.Context, proxyURL string) *ProxyTestResult {
	pool := NewProxyPool("")
	defer pool.CloseIdleConnections()
	return pool.TestProxy(ctx, proxyURL)
}

// TestProxy 使用临时隔离连接探测指定代理的连通性与出口 IP。
func (p *ProxyPool) TestProxy(ctx context.Context, proxyURL string) *ProxyTestResult {
	return p.testProxyWithTargets(ctx, proxyURL, DefaultDomain, defaultIPEchoURLs, nil)
}

func (p *ProxyPool) testProxyWithTargets(ctx context.Context, proxyURL, targetURL string, ipEchoURLs []string, tlsCfg *tls.Config) *ProxyTestResult {
	res := &ProxyTestResult{
		ProxyURL: RedactProxyURL(proxyURL),
	}
	if err := ValidateProxyURL(proxyURL); err != nil {
		res.Error = err.Error()
		return res
	}
	norm, err := NormalizeProxyURL(proxyURL)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	// 探测明确指定的代理时，直接按该 URL 构造独立 Client，避免受 defaultProxy 干扰。
	entry, err := buildPooledClient(norm)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if tlsCfg != nil {
		entry.transport.TLSClientConfig = tlsCfg
	}
	defer entry.transport.CloseIdleConnections()

	probeCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(ctx, proxyTestTimeout)
		defer cancel()
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		res.Error = fmt.Sprintf("构造探测请求失败: %v", err)
		return res
	}
	req.Header.Set("User-Agent", UserAgentDesktop)

	resp, err := entry.client.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = fmt.Sprintf("代理连接失败: %v", err)
		return res
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()

	res.Status = resp.StatusCode
	if resp.StatusCode >= 500 {
		res.Error = fmt.Sprintf("目标站点返回异常状态 HTTP %d", resp.StatusCode)
		return res
	}
	res.OK = true

	// 尽力查询出口 IP；若回显端点不可达不影响代理本身连通性判定。
	for _, echoURL := range ipEchoURLs {
		if strings.TrimSpace(echoURL) == "" {
			continue
		}
		ip := queryExitIP(ctx, entry.client, echoURL)
		if ip != "" {
			res.ExitIP = ip
			break
		}
	}
	return res
}

func queryExitIP(ctx context.Context, client *http.Client, echoURL string) string {
	ipCtx, cancel := context.WithTimeout(ctx, ipEchoTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ipCtx, http.MethodGet, echoURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", UserAgentDesktop)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if ip := net.ParseIP(text); ip != nil {
		return ip.String()
	}
	var obj struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if ip := net.ParseIP(strings.TrimSpace(obj.IP)); ip != nil {
			return ip.String()
		}
	}
	return ""
}
