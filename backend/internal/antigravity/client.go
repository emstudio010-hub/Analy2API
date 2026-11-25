package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/workbuddy"
)

const (
	defaultMgmtTimeout      = 25 * time.Second
	defaultProbeTimeout     = 15 * time.Second
	maxMgmtResponseBytes    = 8 * 1024 * 1024
	antigravityClientUA     = "cmd2api-antigravity/1.0"
	defaultOAuthInstruction = "请在浏览器中打开授权链接完成账号登录；授权完成后节点会自动生成并加载 OAuth JSON 凭证。"
)

// Client 是统一的 AntiGravity / CLIProxyAPI 远程管理、OAuth 授权与 /v1 流式推理客户端。
type Client struct {
	pool   *workbuddy.ProxyPool
	docker *DockerManager
	logger *slog.Logger
}

var (
	defaultClientOnce sync.Once
	defaultClientInst *Client
)

// DefaultClient 返回进程级默认 AntiGravity 客户端实例。
func DefaultClient() *Client {
	defaultClientOnce.Do(func() {
		defaultClientInst = NewClient("", nil)
	})
	return defaultClientInst
}

// NewClient 创建一个持有独立 workbuddy.ProxyPool 的 AntiGravity 客户端。
func NewClient(defaultProxyURL string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		pool:   workbuddy.NewProxyPool(defaultProxyURL),
		docker: DefaultDockerManager(),
		logger: logger,
	}
}

// NewClientWithPool 使用外部已有的 *workbuddy.ProxyPool 创建 AntiGravity 客户端。
func NewClientWithPool(pool *workbuddy.ProxyPool, logger *slog.Logger) *Client {
	if pool == nil {
		pool = workbuddy.NewProxyPool("")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		pool:   pool,
		docker: DefaultDockerManager(),
		logger: logger,
	}
}

// ProxyPool 返回底层共享代理连接池。
func (c *Client) ProxyPool() *workbuddy.ProxyPool {
	return c.pool
}

// Docker 返回关联的 DockerManager。
func (c *Client) Docker() *DockerManager {
	if c.docker != nil {
		return c.docker
	}
	return DefaultDockerManager()
}

// SetDockerManager 注入自定义 DockerManager（便于单元测试）。
func (c *Client) SetDockerManager(mgr *DockerManager) {
	if mgr != nil {
		c.docker = mgr
	}
}

// SetDefaultProxy 更新客户端兜底外部代理地址。
func (c *Client) SetDefaultProxy(proxyURL string) error {
	return c.pool.SetDefaultProxy(proxyURL)
}

// BaseURL 根据 NodeSpec 拼接标准基础地址 `http://<host>:<port>`，
// 完整兼容 HostIP 已带 `http://` / `https://`、IPv6 或显式端口的情况。
func BaseURL(spec NodeSpec) string {
	rawHost := strings.TrimSpace(spec.HostIP)
	port := spec.HostPort
	hasExplicitPortArg := port > 0 && port <= 65535
	if !hasExplicitPortArg {
		port = DefaultHostPort
	}

	if rawHost == "" {
		return fmt.Sprintf("http://%s:%d", DefaultHostIP, port)
	}

	lower := strings.ToLower(rawHost)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(rawHost)
		if err == nil && u.Hostname() != "" {
			u.Path = strings.TrimRight(u.Path, "/")
			u.RawQuery = ""
			u.Fragment = ""
			if u.Port() == "" {
				// 若 HostIP 为 https://domain 且未显式指定非标 HostPort，则保留标准端口；
				// 否则拼接 HostPort（默认 8317）。
				if hasExplicitPortArg {
					isStandard := (u.Scheme == "https" && port == 443) || (u.Scheme == "http" && port == 80)
					if !isStandard {
						u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(port))
					}
				} else if u.Scheme == "http" && IsLoopbackTarget(u.Hostname()) {
					u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(DefaultHostPort))
				}
			}
			return strings.TrimRight(u.String(), "/")
		}
	}

	// 未带协议前缀：检查是否已自带 host:port（排除裸 IPv6 地址如 ::1）
	cleanHost := strings.Trim(rawHost, "[]")
	if strings.Count(rawHost, ":") == 1 || strings.HasPrefix(rawHost, "[") {
		if h, p, splitErr := net.SplitHostPort(rawHost); splitErr == nil && h != "" && p != "" {
			return "http://" + net.JoinHostPort(strings.Trim(h, "[]"), p)
		}
	}

	return "http://" + net.JoinHostPort(cleanHost, strconv.Itoa(port))
}

// BaseURL 方法版，方便通过 *Client 直接调用。
func (c *Client) BaseURL(spec NodeSpec) string {
	return BaseURL(spec)
}

// IsLoopbackTarget 判断目标 URL 或主机是否为本机回环地址（127.0.0.0/8、localhost、::1、0.0.0.0 等）。
// 访问本机 Docker 容器时必须强制直连，绕过节点或全局外部代理，否则外部代理无法路由回宿主机回环口。
func IsLoopbackTarget(rawURLOrHost string) bool {
	s := strings.TrimSpace(rawURLOrHost)
	if s == "" {
		return true
	}

	host := s
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
	} else if h, _, err := net.SplitHostPort(s); err == nil && h != "" {
		host = h
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	lowerHost := strings.ToLower(host)

	switch lowerHost {
	case "", "localhost", "0.0.0.0", "::", "host.docker.internal":
		return true
	}
	if strings.HasSuffix(lowerHost, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// HTTPClientForNode 返回访问该节点的 *http.Client（无全局 Client.Timeout，流式安全）及是否触发了本机回环直连绕过。
func (c *Client) HTTPClientForNode(spec NodeSpec) (*http.Client, bool, error) {
	base := BaseURL(spec)
	if IsLoopbackTarget(base) {
		cli, err := c.pool.Client("direct")
		return cli, true, err
	}
	cli, err := c.pool.Client(strings.TrimSpace(spec.ProxyURL))
	return cli, false, err
}

func (c *Client) sendNodeRequest(ctx context.Context, spec NodeSpec, method, pathAndQuery string, body []byte, hdrs http.Header) (*http.Response, error) {
	base := BaseURL(spec)
	if !strings.HasPrefix(pathAndQuery, "/") {
		pathAndQuery = "/" + pathAndQuery
	}
	targetURL := base + pathAndQuery

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, fmt.Errorf("构造节点请求 %s %s 失败: %w", method, targetURL, err)
	}
	if hdrs != nil {
		req.Header = hdrs.Clone()
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", antigravityClientUA)
	}

	httpClient, _, err := c.HTTPClientForNode(spec)
	if err != nil {
		return nil, fmt.Errorf("选择节点代理客户端失败: %w", err)
	}
	return httpClient.Do(req)
}

func resolveCLIUserAgent(spec NodeSpec) string {
	ua := strings.TrimSpace(spec.Fingerprint.UserAgent)
	lower := strings.ToLower(ua)
	if ua != "" && strings.HasPrefix(lower, "antigravity/") && !strings.Contains(lower, "fp_") && !strings.Contains(lower, "cliproxy") {
		return ua
	}
	return BuildCLIUserAgent("", spec.Fingerprint.OS, spec.Fingerprint.Arch)
}

func applyManagementHeaders(h http.Header, spec NodeSpec) {
	h.Set("User-Agent", resolveCLIUserAgent(spec))
	if fid := strings.TrimSpace(spec.Fingerprint.FingerprintID); fid != "" {
		h.Set("X-Device-Fingerprint", fid)
	}
	if cid := strings.TrimSpace(spec.Fingerprint.ClientID); cid != "" {
		h.Set("X-Client-Id", cid)
	}
}

func managementHeaders(spec NodeSpec) http.Header {
	h := make(http.Header)
	h.Set("Accept", "application/json")
	applyManagementHeaders(h, spec)
	if key := strings.TrimSpace(spec.ManagementKey); key != "" {
		h.Set("X-Management-Key", key)
		h.Set("Authorization", "Bearer "+key)
	}
	return h
}

// inferenceHeaders 严格只设置官方 AntiGravity CLI 允许的干净推理请求头：
// User-Agent (`antigravity/<ver> <os>/<arch>`)、Authorization 与可选 Accept；
// 严禁向推理接口泄漏 X-Qimei36 / X-Machine-Id / X-Device-Fingerprint / X-Client-Id 等非官方自定义头。
func inferenceHeaders(spec NodeSpec, accept string) http.Header {
	h := make(http.Header)
	if accept != "" {
		h.Set("Accept", accept)
	}
	h.Set("User-Agent", resolveCLIUserAgent(spec))
	if key := strings.TrimSpace(spec.APIKey); key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	return h
}

// IsLocalCLITarget 判断该节点/账号是否直接驱动真实的 `agy` CLI 可执行文件（零协议逆向）。
func IsLocalCLITarget(spec NodeSpec) bool {
	if spec.NormalizedMode() == ModeLocalCLI {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(spec.HostIP)) {
	case "local_cli", "agy", "agy-cli", "agy_cli":
		return true
	default:
		return false
	}
}

// IsNativeV1InternalTarget 判断该节点/账号是否应作为原生独立 AntiGravity CLI 客户端直连 `/v1internal:streamGenerateContent?alt=sse`。
func IsNativeV1InternalTarget(spec NodeSpec) bool {
	if spec.NormalizedMode() == ModeDirectCLI {
		return true
	}
	lowerHost := strings.ToLower(strings.TrimSpace(spec.HostIP))
	return strings.Contains(lowerHost, "cloudcode-pa.googleapis.com") ||
		strings.Contains(lowerHost, "/v1internal")
}

func (c *Client) doJSONRequest(ctx context.Context, spec NodeSpec, method, pathAndQuery string, body []byte, hdrs http.Header) ([]byte, int, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultMgmtTimeout)
		defer cancel()
	}
	resp, err := c.sendNodeRequest(ctx, spec, method, pathAndQuery, body, hdrs)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxMgmtResponseBytes))
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取节点响应失败: %w", readErr)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return raw, resp.StatusCode, parseUpstreamAPIError(resp.StatusCode, raw)
	}
	return raw, resp.StatusCode, nil
}

// ListAuthFiles 获取节点登录凭证列表：
//   - 优先调用远程 `GET /v0/management/auth-files`（兼容数组或 files/auth_files/data 等包装结构）
//   - 若存在本地 spec.AuthDir，自动合并补全本地元数据与尚未热加载的文件
//   - 若本机容器未启动或接口返回 404 且 spec.AuthDir 存在，优雅回退 ListLocalAuthFiles
func (c *Client) ListAuthFiles(ctx context.Context, spec NodeSpec) ([]AuthFileInfo, error) {
	remoteFiles, remoteErr := c.fetchRemoteAuthFiles(ctx, spec)
	hasLocalDir := strings.TrimSpace(spec.AuthDir) != ""

	if remoteErr != nil {
		var apiErr *APIError
		if errors.As(remoteErr, &apiErr) && apiErr.IsAuthFailure() {
			return nil, remoteErr
		}
		if hasLocalDir && localDirReadable(spec.AuthDir) {
			return ListLocalAuthFiles(spec.AuthDir)
		}
		if spec.IsLocalDocker() && hasLocalDir {
			// 本机模式下即便目录尚未初始化，容器未启动时也返回空列表而非报错
			return ListLocalAuthFiles(spec.AuthDir)
		}
		return nil, remoteErr
	}

	if hasLocalDir && localDirReadable(spec.AuthDir) {
		if localFiles, err := ListLocalAuthFiles(spec.AuthDir); err == nil && len(localFiles) > 0 {
			remoteFiles = mergeAuthFileLists(remoteFiles, localFiles)
		}
	}

	sortAuthFiles(remoteFiles)
	return remoteFiles, nil
}

func (c *Client) fetchRemoteAuthFiles(ctx context.Context, spec NodeSpec) ([]AuthFileInfo, error) {
	raw, _, err := c.doJSONRequest(ctx, spec, http.MethodGet, "/v0/management/auth-files", nil, managementHeaders(spec))
	if err != nil {
		return nil, err
	}
	return decodeRemoteAuthFilesResponse(raw)
}

func decodeRemoteAuthFilesResponse(raw []byte) ([]AuthFileInfo, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []AuthFileInfo{}, nil
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("解析管理接口 auth-files JSON 失败: %w", err)
	}

	items := extractAuthFileRawEntries(parsed)
	out := make([]AuthFileInfo, 0, len(items))
	for _, m := range items {
		filename := firstString(m, "name", "filename", "file_name", "id")
		info := ParseAuthFileMap(filename, m, SourceRemoteAPI)
		info.Source = SourceRemoteAPI
		out = append(out, info)
	}
	return out, nil
}

func extractAuthFileRawEntries(parsed any) []map[string]any {
	switch root := parsed.(type) {
	case []any:
		return sliceAnyToMaps(root)
	case map[string]any:
		for _, key := range []string{"files", "auth_files", "authFiles", "items", "list", "accounts", "data"} {
			val, exists := root[key]
			if !exists || val == nil {
				continue
			}
			switch sub := val.(type) {
			case []any:
				return sliceAnyToMaps(sub)
			case map[string]any:
				// 兼容 {"data": {"files": [...]}} 或按文件名做 key 的字典 {"files": {"a.json": {...}}}
				if nested := extractAuthFileRawEntries(sub); len(nested) > 0 {
					return nested
				}
				return keyedMapToAuthMaps(sub)
			}
		}
	}
	return nil
}

func sliceAnyToMaps(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok && m != nil {
			out = append(out, m)
		}
	}
	return out
}

func keyedMapToAuthMaps(m map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(m))
	for k, v := range m {
		sub, ok := v.(map[string]any)
		if !ok || sub == nil {
			continue
		}
		cloned := make(map[string]any, len(sub)+1)
		for sk, sv := range sub {
			cloned[sk] = sv
		}
		if firstString(cloned, "name", "filename", "file_name") == "" {
			cloned["filename"] = k
		}
		out = append(out, cloned)
	}
	return out
}

func mergeAuthFileLists(remoteFiles, localFiles []AuthFileInfo) []AuthFileInfo {
	localByName := make(map[string]AuthFileInfo, len(localFiles))
	for _, lf := range localFiles {
		localByName[strings.ToLower(lf.Filename)] = lf
	}

	seen := make(map[string]struct{}, len(remoteFiles)+len(localFiles))
	merged := make([]AuthFileInfo, 0, len(remoteFiles)+len(localFiles))

	for _, rf := range remoteFiles {
		key := strings.ToLower(rf.Filename)
		seen[key] = struct{}{}
		if lf, ok := localByName[key]; ok {
			if rf.Provider == "" {
				rf.Provider = lf.Provider
			}
			if rf.Email == "" {
				rf.Email = lf.Email
			}
			if rf.ProjectID == "" {
				rf.ProjectID = lf.ProjectID
			}
			if rf.AccountType == "" {
				rf.AccountType = lf.AccountType
			}
			if rf.Priority == 0 && lf.Priority != 0 {
				rf.Priority = lf.Priority
			}
			if len(rf.ExcludedModels) == 0 && len(lf.ExcludedModels) > 0 {
				rf.ExcludedModels = lf.ExcludedModels
			}
			if rf.ExpiresAt == "" {
				rf.ExpiresAt = lf.ExpiresAt
				rf.Expired = lf.Expired
			}
			if rf.SizeBytes == 0 {
				rf.SizeBytes = lf.SizeBytes
			}
			if rf.UpdatedAt == "" {
				rf.UpdatedAt = lf.UpdatedAt
			}
		}
		rf.Source = SourceRemoteAPI
		merged = append(merged, rf)
	}

	for _, lf := range localFiles {
		key := strings.ToLower(lf.Filename)
		if _, exists := seen[key]; exists {
			continue
		}
		lf.Source = SourceLocalDir
		merged = append(merged, lf)
	}

	return merged
}

// UploadAuthFile 上传或写入单份登录 JSON 凭证：
//   - 若 spec.AuthDir 非空，先安全写入宿主机本地挂载目录，再尝试通过 POST/PUT `/v0/management/auth-files` 通知运行中的容器热加载；
//   - 若为远程节点（无本地 AuthDir），直接通过管理接口上传。
func (c *Client) UploadAuthFile(ctx context.Context, spec NodeSpec, filename string, rawJSON []byte) (AuthFileInfo, error) {
	cleanName, err := SanitizeAuthFilename(filename)
	if err != nil {
		return AuthFileInfo{}, err
	}
	parsedInfo, err := ParseAuthFileJSON(cleanName, rawJSON)
	if err != nil {
		return AuthFileInfo{}, err
	}

	hasLocalDir := strings.TrimSpace(spec.AuthDir) != ""
	var localInfo AuthFileInfo
	if hasLocalDir {
		localInfo, err = SaveLocalAuthFile(spec.AuthDir, cleanName, rawJSON)
		if err != nil {
			return AuthFileInfo{}, err
		}
	}

	remoteInfo, remoteErr := c.uploadRemoteAuthFile(ctx, spec, cleanName, rawJSON)
	if remoteErr == nil {
		if hasLocalDir {
			merged := mergeAuthFileLists([]AuthFileInfo{remoteInfo}, []AuthFileInfo{localInfo})
			if len(merged) > 0 {
				return merged[0], nil
			}
		}
		return remoteInfo, nil
	}

	if hasLocalDir && spec.IsLocalDocker() {
		var apiErr *APIError
		if errors.As(remoteErr, &apiErr) && apiErr.IsAuthFailure() {
			return AuthFileInfo{}, remoteErr
		}
		// 本机容器尚未启动或管理热加载端点未开启时，本地卷文件已持久化成功
		c.logger.Debug("本机容器未响应凭证热加载通知，已保留本地挂载目录写入结果", "filename", cleanName, "err", remoteErr)
		return localInfo, nil
	}

	return parsedInfo, remoteErr
}

func (c *Client) uploadRemoteAuthFile(ctx context.Context, spec NodeSpec, cleanName string, rawJSON []byte) (AuthFileInfo, error) {
	q := url.Values{}
	q.Set("name", cleanName)
	q.Set("filename", cleanName)
	endpoint := "/v0/management/auth-files?" + q.Encode()

	hdrs := managementHeaders(spec)
	hdrs.Set("Content-Type", "application/json")

	rawResp, status, err := c.doJSONRequest(ctx, spec, http.MethodPost, endpoint, rawJSON, hdrs)
	if err != nil && (status == http.StatusMethodNotAllowed || status == http.StatusNotFound) {
		rawResp, _, err = c.doJSONRequest(ctx, spec, http.MethodPut, endpoint, rawJSON, hdrs)
	}
	if err != nil {
		return AuthFileInfo{}, err
	}

	info, _ := ParseAuthFileJSON(cleanName, rawJSON)
	info.Filename = cleanName
	info.Source = SourceRemoteAPI
	info.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	// 若管理接口返回了更新后的元数据则合并补充
	if len(bytes.TrimSpace(rawResp)) > 0 {
		var respMap map[string]any
		dec := json.NewDecoder(bytes.NewReader(rawResp))
		dec.UseNumber()
		if dec.Decode(&respMap) == nil && respMap != nil {
			unwrapped := unwrapMapData(respMap)
			if fileObj, ok := unwrapped["file"].(map[string]any); ok && fileObj != nil {
				unwrapped = fileObj
			}
			remoteMeta := ParseAuthFileMap(cleanName, unwrapped, SourceRemoteAPI)
			merged := mergeAuthFileLists([]AuthFileInfo{remoteMeta}, []AuthFileInfo{info})
			if len(merged) > 0 {
				return merged[0], nil
			}
		}
	}
	return info, nil
}

// DeleteAuthFile 同步删除远程管理接口与宿主机本地 authDir 中的凭证文件。
func (c *Client) DeleteAuthFile(ctx context.Context, spec NodeSpec, filename string) error {
	cleanName, err := SanitizeAuthFilename(filename)
	if err != nil {
		return err
	}

	hasLocalDir := strings.TrimSpace(spec.AuthDir) != ""
	localDeleted := false
	if hasLocalDir {
		localErr := DeleteLocalAuthFile(spec.AuthDir, cleanName)
		if localErr == nil {
			localDeleted = true
		} else if !errors.Is(localErr, os.ErrNotExist) {
			return localErr
		}
	}

	remoteErr := c.deleteRemoteAuthFile(ctx, spec, cleanName)
	if remoteErr == nil {
		return nil
	}

	var apiErr *APIError
	if errors.As(remoteErr, &apiErr) && apiErr.IsAuthFailure() {
		return remoteErr
	}
	if hasLocalDir && spec.IsLocalDocker() && localDeleted {
		// 本机容器停止或远程已不存在该文件时，本地卷已成功移除
		return nil
	}
	return remoteErr
}

func (c *Client) deleteRemoteAuthFile(ctx context.Context, spec NodeSpec, cleanName string) error {
	q := url.Values{}
	q.Set("name", cleanName)
	q.Set("filename", cleanName)
	primaryPath := "/v0/management/auth-files?" + q.Encode()

	_, status, err := c.doJSONRequest(ctx, spec, http.MethodDelete, primaryPath, nil, managementHeaders(spec))
	if err == nil {
		return nil
	}
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		altPath := "/v0/management/auth-files/" + url.PathEscape(cleanName)
		if _, _, altErr := c.doJSONRequest(ctx, spec, http.MethodDelete, altPath, nil, managementHeaders(spec)); altErr == nil {
			return nil
		}
	}
	return err
}

// RequestOAuthURL 依次兼容探测 CLIProxyAPI 的多版本 OAuth 授权地址端点：
//  1. `/v8/management/oauth/auth-url?provider=<provider>`
//  2. `/v0/management/oauth/auth-url?provider=<provider>`
//  3. `/<provider>-auth-url`
//  4. `/v0/management/<provider>-auth-url`
func (c *Client) RequestOAuthURL(ctx context.Context, spec NodeSpec, provider, redirectURI string) (OAuthURLResult, error) {
	prov := normalizeProviderName(provider)
	if prov == "" {
		prov = strings.ToLower(strings.TrimSpace(provider))
	}
	if prov == "" {
		prov = DefaultOAuthProvider
	}
	redir := strings.TrimSpace(redirectURI)

	buildUnifiedQuery := func() string {
		q := url.Values{}
		q.Set("provider", prov)
		q.Set("is_webui", "true")
		if redir != "" {
			q.Set("redirect_uri", redir)
		}
		return q.Encode()
	}
	buildLegacyQuery := func() string {
		q := url.Values{}
		q.Set("is_webui", "true")
		if redir != "" {
			q.Set("redirect_uri", redir)
		}
		return q.Encode()
	}

	candidates := []string{
		"/v8/management/oauth/auth-url?" + buildUnifiedQuery(),
		"/v0/management/oauth/auth-url?" + buildUnifiedQuery(),
		fmt.Sprintf("/%s-auth-url?%s", url.PathEscape(prov), buildLegacyQuery()),
		fmt.Sprintf("/v0/management/%s-auth-url?%s", url.PathEscape(prov), buildLegacyQuery()),
	}

	hdrs := managementHeaders(spec)
	var lastErr error

	for _, endpoint := range candidates {
		raw, status, err := c.doJSONRequest(ctx, spec, http.MethodGet, endpoint, nil, hdrs)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				if apiErr.IsAuthFailure() {
					return OAuthURLResult{}, err
				}
				if status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented {
					lastErr = err
					continue
				}
			}
			// 网络不可达或上下文取消时直接返回，无需继续探测其余路径
			return OAuthURLResult{}, err
		}

		res, parseErr := parseOAuthURLResponse(raw, prov)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		return res, nil
	}

	if lastErr != nil {
		return OAuthURLResult{}, fmt.Errorf("获取 %s OAuth 授权链接失败: %w", prov, lastErr)
	}
	return OAuthURLResult{}, fmt.Errorf("节点不支持 %s OAuth 授权链接生成", prov)
}

func parseOAuthURLResponse(raw []byte, defaultProvider string) (OAuthURLResult, error) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return OAuthURLResult{}, fmt.Errorf("解析 OAuth 响应 JSON 失败: %w", err)
	}
	data := unwrapMapData(root)

	authURL := firstString(data, "auth_url", "url", "authorization_url", "login_url", "authUrl")
	if authURL == "" {
		authURL = firstString(root, "auth_url", "url", "authorization_url", "login_url", "authUrl")
	}
	if authURL == "" {
		return OAuthURLResult{}, errors.New("节点返回的 OAuth 授权地址为空")
	}

	state := firstString(data, "state", "oauth_state", "session_id")
	if state == "" {
		state = firstString(root, "state", "oauth_state", "session_id")
	}
	if state == "" {
		if u, err := url.Parse(authURL); err == nil {
			state = strings.TrimSpace(u.Query().Get("state"))
		}
	}

	prov := firstString(data, "provider")
	if prov == "" {
		prov = defaultProvider
	}
	instructions := firstString(data, "instructions", "message", "tip")
	if instructions == "" || strings.EqualFold(instructions, "ok") || strings.EqualFold(instructions, "success") {
		instructions = defaultOAuthInstruction
	}

	return OAuthURLResult{
		AuthURL:      authURL,
		State:        state,
		Provider:     prov,
		Instructions: instructions,
	}, nil
}

// SubmitOAuthCallback 提交手动复制的浏览器回调 URL 或授权码以完成容器侧 OAuth 令牌落盘。
func (c *Client) SubmitOAuthCallback(ctx context.Context, spec NodeSpec, provider, redirectURL string) (map[string]any, error) {
	prov := normalizeProviderName(provider)
	if prov == "" {
		prov = DefaultOAuthProvider
	}
	payload, err := json.Marshal(map[string]string{
		"provider":     prov,
		"redirect_url": strings.TrimSpace(redirectURL),
	})
	if err != nil {
		return nil, err
	}

	hdrs := managementHeaders(spec)
	hdrs.Set("Content-Type", "application/json")

	candidates := []string{
		"/v8/management/oauth/callback",
		"/v0/management/oauth-callback",
		"/v0/management/oauth/callback",
	}
	var lastErr error
	for _, ep := range candidates {
		raw, status, reqErr := c.doJSONRequest(ctx, spec, http.MethodPost, ep, payload, hdrs)
		if reqErr != nil {
			if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
				lastErr = reqErr
				continue
			}
			return nil, reqErr
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if out == nil {
			out = map[string]any{"status": "ok"}
		}
		return out, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("提交 OAuth 回调失败")
}

// ProbeNode 检测节点连通性、可用模型数量与登录凭证数量。
func (c *Client) ProbeNode(ctx context.Context, spec NodeSpec) (latency time.Duration, modelsCount int, authCount int, err error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultProbeTimeout)
		defer cancel()
	}

	started := time.Now()
	models, modelsErr := c.Models(ctx, spec)
	latency = time.Since(started)

	remoteAuth, remoteAuthErr := c.fetchRemoteAuthFiles(ctx, spec)
	if remoteAuthErr == nil {
		if strings.TrimSpace(spec.AuthDir) != "" && localDirReadable(spec.AuthDir) {
			if lf, lErr := ListLocalAuthFiles(spec.AuthDir); lErr == nil && len(lf) > 0 {
				remoteAuth = mergeAuthFileLists(remoteAuth, lf)
			}
		}
		authCount = len(remoteAuth)
	} else if strings.TrimSpace(spec.AuthDir) != "" && localDirReadable(spec.AuthDir) {
		if lf, lErr := ListLocalAuthFiles(spec.AuthDir); lErr == nil {
			authCount = len(lf)
		}
	}

	if modelsErr == nil {
		modelsCount = len(models)
		if remoteAuthErr != nil && strings.TrimSpace(spec.ManagementKey) != "" {
			var apiErr *APIError
			if errors.As(remoteAuthErr, &apiErr) && apiErr.IsAuthFailure() {
				return latency, modelsCount, authCount, remoteAuthErr
			}
		}
		return latency, modelsCount, authCount, nil
	}

	// 若尚未配置推理 APIKey，但管理接口已正常连通返回 200，则视为节点在线
	if strings.TrimSpace(spec.APIKey) == "" && remoteAuthErr == nil {
		return latency, 0, authCount, nil
	}

	return latency, 0, authCount, modelsErr
}

// Models 拉取节点可用模型目录：当节点为真实 agy CLI 模式时直接调用 `agy models`，
// 否则调用节点的 `GET /v1/models`。
func (c *Client) Models(ctx context.Context, spec NodeSpec) ([]ModelItem, error) {
	if IsLocalCLITarget(spec) {
		return ListModelsViaCLI(ctx, spec)
	}
	raw, _, err := c.doJSONRequest(ctx, spec, http.MethodGet, "/v1/models", nil, inferenceHeaders(spec, "application/json"))
	if err != nil {
		return nil, err
	}
	return parseModelsResponse(raw)
}

// ChatStream 向 AntiGravity 发起流式推理请求（支持真实 agy CLI 直驱、单容器 agy-bridge /v1/chat/completions 及原生 v1internal）。
func (c *Client) ChatStream(ctx context.Context, spec NodeSpec, openaiBody []byte) (*http.Response, error) {
	resp, _, err := c.ChatStreamWithSession(ctx, spec, openaiBody, "")
	return resp, err
}

// ChatStreamWithSession 发起带会话 ID 的 AntiGravity 流式推理请求，同时返回实际发往上游的原始字节流供 429 审计日志落盘。
func (c *Client) ChatStreamWithSession(ctx context.Context, spec NodeSpec, openaiBody []byte, sessionID string) (*http.Response, []byte, error) {
	if IsLocalCLITarget(spec) {
		return ChatStreamViaCLIWithSession(ctx, spec, openaiBody, sessionID, defaultLocalCLISessions)
	}

	if IsNativeV1InternalTarget(spec) {
		sanitizedBody, err := SanitizeOpenAIPayload(openaiBody)
		if err != nil {
			return nil, nil, err
		}
		var probe struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(sanitizedBody, &probe)

		v1Body, err := BuildV1InternalRequest(sanitizedBody, spec.ProjectID, probe.Model, sessionID)
		if err != nil {
			return nil, nil, err
		}

		// 与官方 AntiGravity CLI 保持严格一致：仅发送 Content-Type / Authorization / User-Agent 三个头
		hdrs := inferenceHeaders(spec, "")
		hdrs.Set("Content-Type", "application/json")

		resp, err := c.sendNodeRequest(ctx, spec, http.MethodPost, "/v1internal:streamGenerateContent?alt=sse", v1Body, hdrs)
		if err != nil {
			return nil, v1Body, err
		}
		if resp.StatusCode >= http.StatusBadRequest {
			rawErr, _ := io.ReadAll(io.LimitReader(resp.Body, maxMgmtResponseBytes))
			_ = resp.Body.Close()
			return nil, v1Body, parseUpstreamAPIError(resp.StatusCode, rawErr)
		}
		resp.Body = TranslateV1InternalSSEToOpenAISSE(ctx, resp.Body, probe.Model, "")
		return resp, v1Body, nil
	}

	// 单容器 agy-bridge / 远程 HTTP 反代：严格原样透传下游提示词（dsh / codex / Claude Code 等），不篡改 system 文本
	preparedBody, err := PrepareOpenAIPayloadForCLI(openaiBody)
	if err != nil {
		return nil, nil, err
	}

	patchedBody, err := ensureStreamingPayload(preparedBody)
	if err != nil {
		return nil, preparedBody, err
	}

	hdrs := inferenceHeaders(spec, "text/event-stream")
	hdrs.Set("Content-Type", "application/json")
	if sid := strings.TrimSpace(sessionID); sid != "" {
		// 透传上游 session_id 至单账号独立 agy 容器桥接层
		hdrs.Set("X-Session-Id", sid)
	}
	if strings.TrimSpace(spec.SessionRetention) != "" {
		hdrs.Set("X-Agy-Session-Retention", NormalizeSessionRetention(spec.SessionRetention))
	}
	if strings.TrimSpace(spec.PromptBlendMode) != "" {
		hdrs.Set("X-Agy-Prompt-Blend-Mode", NormalizePromptBlendMode(spec.PromptBlendMode))
	}

	resp, err := c.sendNodeRequest(ctx, spec, http.MethodPost, "/v1/chat/completions", patchedBody, hdrs)
	if err != nil {
		return nil, patchedBody, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		rawErr, _ := io.ReadAll(io.LimitReader(resp.Body, maxMgmtResponseBytes))
		_ = resp.Body.Close()
		return nil, patchedBody, parseUpstreamAPIError(resp.StatusCode, rawErr)
	}
	return resp, patchedBody, nil
}

func ensureStreamingPayload(openaiBody []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(openaiBody)
	if len(trimmed) == 0 {
		return nil, errors.New("chat completions 请求体不能为空")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("chat completions 请求体不是合法 JSON: %w", err)
	}

	payload, ok := parsed.(map[string]any)
	if !ok || payload == nil {
		return nil, errors.New("chat completions 请求体必须是 JSON 对象")
	}

	payload["stream"] = true
	if existingOpt, ok := payload["stream_options"].(map[string]any); ok && existingOpt != nil {
		existingOpt["include_usage"] = true
	} else {
		payload["stream_options"] = map[string]any{
			"include_usage": true,
		}
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化流式请求体失败: %w", err)
	}
	return encoded, nil
}

// DefaultModels 返回 AntiGravity / CLIProxyAPI 权威旗舰兜底模型目录。
func DefaultModels() []ModelItem {
	base := []ModelItem{
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Vendor: "google", ContextWindow: 1048576, MaxOutputTokens: 65536, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Vendor: "google", ContextWindow: 1048576, MaxOutputTokens: 65536, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "gemini-3.1-pro", Name: "Gemini 3.1 Pro", Vendor: "google", ContextWindow: 1048576, MaxOutputTokens: 65536, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash", Vendor: "google", ContextWindow: 1048576, MaxOutputTokens: 65536, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", Vendor: "anthropic", ContextWindow: 200000, MaxOutputTokens: 64000, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "claude-opus-4-6", Name: "Claude Opus 4.6", Vendor: "anthropic", ContextWindow: 200000, MaxOutputTokens: 64000, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", Vendor: "anthropic", ContextWindow: 200000, MaxOutputTokens: 64000, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5", Vendor: "anthropic", ContextWindow: 200000, MaxOutputTokens: 64000, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "gpt-5", Name: "GPT-5", Vendor: "openai", ContextWindow: 400000, MaxOutputTokens: 32768, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", Vendor: "openai", ContextWindow: 400000, MaxOutputTokens: 65536, SupportsImages: true, SupportsReasoning: true, SupportsToolCall: true},
	}
	out := make([]ModelItem, len(base))
	copy(out, base)
	return out
}

// InferModelItem 从 OpenAI `/v1/models` 单条模型对象中解析并智能推断 Vendor、上下文窗口与能力标志。
func InferModelItem(m map[string]any) (ModelItem, bool) {
	id := firstString(m, "id", "model", "name")
	if id == "" {
		return ModelItem{}, false
	}

	lowerID := strings.ToLower(id)
	for _, skip := range []string{"text-embedding", "embedding-", "whisper-", "tts-", "dall-e", "moderation"} {
		if strings.Contains(lowerID, skip) {
			return ModelItem{}, false
		}
	}

	name := firstString(m, "display_name", "displayName", "name")
	if name == "" || name == id {
		name = prettifyModelName(id)
	}

	rawVendor := firstString(m, "vendor", "provider", "owned_by", "ownedBy")
	vendor := inferModelVendor(id, rawVendor)

	ctxWin := int(firstInt64(m, "context_window", "contextWindow", "max_input_tokens", "input_token_limit"))
	maxOut := int(firstInt64(m, "max_output_tokens", "maxOutputTokens", "output_token_limit"))
	defCtx, defOut := defaultModelLimits(lowerID, vendor)
	if ctxWin <= 0 {
		ctxWin = defCtx
	}
	if maxOut <= 0 {
		maxOut = defOut
	}

	supportsImages := inferSupportsImages(lowerID, vendor, m)
	supportsReasoning := inferSupportsReasoning(lowerID, vendor, m)
	supportsToolCall := inferSupportsToolCall(lowerID, vendor, m)

	return ModelItem{
		ID:                id,
		Name:              name,
		Vendor:            vendor,
		ContextWindow:     ctxWin,
		MaxOutputTokens:   maxOut,
		SupportsImages:    supportsImages,
		SupportsReasoning: supportsReasoning,
		SupportsToolCall:  supportsToolCall,
	}, true
}

func parseModelsResponse(raw []byte) ([]ModelItem, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []ModelItem{}, nil
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("解析 /v1/models 响应 JSON 失败: %w", err)
	}

	var entries []map[string]any
	switch root := parsed.(type) {
	case []any:
		entries = sliceAnyToMaps(root)
	case map[string]any:
		for _, k := range []string{"data", "models", "items", "list"} {
			if arr, ok := root[k].([]any); ok {
				entries = sliceAnyToMaps(arr)
				break
			}
		}
	}

	seen := make(map[string]struct{}, len(entries))
	out := make([]ModelItem, 0, len(entries))
	for _, entry := range entries {
		item, ok := InferModelItem(entry)
		if !ok {
			continue
		}
		if _, dup := seen[item.ID]; dup {
			continue
		}
		seen[item.ID] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

func inferModelVendor(modelID, rawVendor string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.HasPrefix(id, "gemini"), strings.HasPrefix(id, "gemma"), strings.HasPrefix(id, "learnlm"):
		return "google"
	case strings.HasPrefix(id, "claude"):
		return "anthropic"
	case strings.HasPrefix(id, "gpt-"), strings.HasPrefix(id, "chatgpt-"), strings.HasPrefix(id, "codex"),
		strings.HasPrefix(id, "o1"), strings.HasPrefix(id, "o3"), strings.HasPrefix(id, "o4"):
		return "openai"
	case strings.HasPrefix(id, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(id, "qwen"), strings.HasPrefix(id, "qwq"):
		return "qwen"
	case strings.HasPrefix(id, "glm"):
		return "zhipu"
	case strings.HasPrefix(id, "kimi"), strings.HasPrefix(id, "moonshot"):
		return "moonshot"
	case strings.HasPrefix(id, "grok"):
		return "xai"
	}

	rv := strings.ToLower(strings.TrimSpace(rawVendor))
	switch rv {
	case "", "system", "custom", "cliproxyapi", "antigravity", "local":
		return "google"
	case "vertex", "aistudio", "gemini", "google":
		return "google"
	case "anthropic", "claude":
		return "anthropic"
	case "openai", "codex":
		return "openai"
	default:
		return rv
	}
}

func defaultModelLimits(lowerID, vendor string) (ctxWin int, maxOut int) {
	switch vendor {
	case "google":
		if strings.HasPrefix(lowerID, "gemini-2") || strings.HasPrefix(lowerID, "gemini-3") || strings.Contains(lowerID, "1.5-pro") {
			return 1048576, 65536
		}
		return 1048576, 32768
	case "anthropic":
		if strings.Contains(lowerID, "-4") || strings.Contains(lowerID, "3-7") || strings.Contains(lowerID, "3.7") {
			return 200000, 64000
		}
		return 200000, 8192
	case "openai":
		if strings.HasPrefix(lowerID, "gpt-5") || strings.Contains(lowerID, "codex") {
			return 400000, 65536
		}
		if strings.HasPrefix(lowerID, "gpt-4.1") {
			return 1047576, 32768
		}
		return 200000, 32768
	default:
		return 131072, 16384
	}
}

func inferSupportsImages(lowerID, vendor string, m map[string]any) bool {
	if v, ok := m["supports_images"]; ok {
		return parseBool(v)
	}
	if v, ok := m["supportsImages"]; ok {
		return parseBool(v)
	}
	switch vendor {
	case "google":
		return strings.HasPrefix(lowerID, "gemini")
	case "anthropic":
		return strings.HasPrefix(lowerID, "claude-3") || strings.Contains(lowerID, "claude-")
	case "openai":
		if strings.HasPrefix(lowerID, "o1-mini") || strings.HasPrefix(lowerID, "o3-mini") {
			return false
		}
		return strings.HasPrefix(lowerID, "gpt-4o") || strings.HasPrefix(lowerID, "gpt-4.1") ||
			strings.HasPrefix(lowerID, "gpt-5") || strings.Contains(lowerID, "codex") ||
			strings.HasPrefix(lowerID, "o1") || strings.HasPrefix(lowerID, "o3") || strings.HasPrefix(lowerID, "o4")
	default:
		return strings.Contains(lowerID, "-vl") || strings.Contains(lowerID, "vision")
	}
}

func inferSupportsReasoning(lowerID, vendor string, m map[string]any) bool {
	if v, ok := m["supports_reasoning"]; ok {
		return parseBool(v)
	}
	if v, ok := m["supportsReasoning"]; ok {
		return parseBool(v)
	}
	if strings.Contains(lowerID, "thinking") || strings.Contains(lowerID, "reasoner") {
		return true
	}
	switch vendor {
	case "google":
		return strings.HasPrefix(lowerID, "gemini-2.5") || strings.HasPrefix(lowerID, "gemini-3")
	case "anthropic":
		return strings.Contains(lowerID, "-4") || strings.Contains(lowerID, "3-7") || strings.Contains(lowerID, "3.7")
	case "openai":
		return strings.HasPrefix(lowerID, "gpt-5") || strings.Contains(lowerID, "codex") ||
			strings.HasPrefix(lowerID, "o1") || strings.HasPrefix(lowerID, "o3") || strings.HasPrefix(lowerID, "o4")
	case "deepseek":
		return strings.Contains(lowerID, "r1") || strings.Contains(lowerID, "v4")
	case "qwen":
		return strings.HasPrefix(lowerID, "qwq") || strings.HasPrefix(lowerID, "qwen3")
	default:
		return false
	}
}

func inferSupportsToolCall(lowerID, _ string, m map[string]any) bool {
	if v, ok := m["supports_tool_call"]; ok {
		return parseBool(v)
	}
	if v, ok := m["supportsToolCall"]; ok {
		return parseBool(v)
	}
	if strings.HasPrefix(lowerID, "o1-mini") || strings.HasPrefix(lowerID, "o1-preview") {
		return false
	}
	return true
}

func prettifyModelName(id string) string {
	for _, preset := range DefaultModels() {
		if strings.EqualFold(preset.ID, id) {
			return preset.Name
		}
	}
	parts := strings.Split(id, "-")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		lp := strings.ToLower(p)
		switch lp {
		case "gpt":
			out = append(out, "GPT")
		case "ai":
			out = append(out, "AI")
		case "vl":
			out = append(out, "VL")
		default:
			out = append(out, strings.ToUpper(p[:1])+p[1:])
		}
	}
	if len(out) == 0 {
		return id
	}
	return strings.Join(out, " ")
}

func unwrapMapData(root map[string]any) map[string]any {
	if root == nil {
		return map[string]any{}
	}
	if sub, ok := root["data"].(map[string]any); ok && sub != nil {
		return sub
	}
	return root
}

func localDirReadable(dir string) bool {
	st, err := os.Stat(strings.TrimSpace(dir))
	return err == nil && st.IsDir()
}

func parseUpstreamAPIError(status int, raw []byte) *APIError {
	apiErr := &APIError{
		Status: status,
		Raw:    truncateErrorText(raw),
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 {
		var root map[string]any
		if json.Unmarshal(trimmed, &root) == nil && root != nil {
			apiErr.Message = firstString(root, "message", "error_description", "msg", "detail")
			apiErr.Code = firstString(root, "code", "type")
			if errObj, ok := root["error"].(map[string]any); ok && errObj != nil {
				if apiErr.Message == "" {
					apiErr.Message = firstString(errObj, "message", "msg", "detail")
				}
				if apiErr.Code == "" {
					apiErr.Code = firstString(errObj, "code", "type")
				}
			} else if errStr, ok := root["error"].(string); ok && apiErr.Message == "" {
				apiErr.Message = strings.TrimSpace(errStr)
			}
		}
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(status)
	}
	return apiErr
}

func truncateErrorText(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 512 {
		return s[:512] + "..."
	}
	return s
}

// --- 包级便捷函数（委托给 DefaultClient） ---

// ListAuthFiles 使用默认客户端列出节点登录凭证文件。
func ListAuthFiles(ctx context.Context, spec NodeSpec) ([]AuthFileInfo, error) {
	return DefaultClient().ListAuthFiles(ctx, spec)
}

// UploadAuthFile 使用默认客户端上传或保存登录凭证文件。
func UploadAuthFile(ctx context.Context, spec NodeSpec, filename string, rawJSON []byte) (AuthFileInfo, error) {
	return DefaultClient().UploadAuthFile(ctx, spec, filename, rawJSON)
}

// DeleteAuthFile 使用默认客户端删除登录凭证文件。
func DeleteAuthFile(ctx context.Context, spec NodeSpec, filename string) error {
	return DefaultClient().DeleteAuthFile(ctx, spec, filename)
}

// RequestOAuthURL 使用默认客户端获取节点 OAuth 登录授权链接。
func RequestOAuthURL(ctx context.Context, spec NodeSpec, provider, redirectURI string) (OAuthURLResult, error) {
	return DefaultClient().RequestOAuthURL(ctx, spec, provider, redirectURI)
}

// ExitIPProbeResult 描述容器/代理出口 IP 实时探测结果。
type ExitIPProbeResult struct {
	OK        bool   `json:"ok"`
	ExitIP    string `json:"exit_ip,omitempty"`
	ProxyUsed string `json:"proxy_used,omitempty"`
	Direct    bool   `json:"direct"`
	LatencyMs int    `json:"latency_ms"`
	Endpoint  string `json:"endpoint,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ProbeExitIP 通过节点绑定的独立 ProxyURL（或直连）探测公网出口 IP，用于验证 1 容器 1 独立 IP 隔离性。
func (c *Client) ProbeExitIP(ctx context.Context, proxyURL string) ExitIPProbeResult {
	p := strings.TrimSpace(proxyURL)
	cli, err := c.pool.Client(p)
	if err != nil {
		return ExitIPProbeResult{OK: false, Direct: p == "", Error: "初始化代理客户端失败: " + err.Error()}
	}
	started := time.Now()
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}

	endpoints := []struct {
		url    string
		isJSON bool
	}{
		{"https://api.ipify.org?format=json", true},
		{"https://ifconfig.me/ip", false},
		{"https://ipinfo.io/ip", false},
	}

	var lastErr error
	for _, ep := range endpoints {
		req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, ep.url, nil)
		if rErr != nil {
			lastErr = rErr
			continue
		}
		req.Header.Set("User-Agent", antigravityClientUA)
		resp, dErr := cli.Do(req)
		if dErr != nil {
			lastErr = dErr
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		var ipStr string
		if ep.isJSON {
			var parsed struct {
				IP string `json:"ip"`
			}
			if json.Unmarshal(raw, &parsed) == nil {
				ipStr = strings.TrimSpace(parsed.IP)
			}
		} else {
			ipStr = strings.TrimSpace(string(raw))
		}
		if net.ParseIP(ipStr) != nil {
			return ExitIPProbeResult{
				OK:        true,
				ExitIP:    ipStr,
				ProxyUsed: workbuddy.RedactProxyURL(p),
				Direct:    p == "",
				LatencyMs: int(time.Since(started).Milliseconds()),
				Endpoint:  ep.url,
			}
		}
		lastErr = fmt.Errorf("无效出口 IP 响应: %q", strings.TrimSpace(string(raw)))
	}

	errMsg := "探测出口 IP 失败"
	if lastErr != nil {
		errMsg = errMsg + ": " + lastErr.Error()
	}
	return ExitIPProbeResult{
		OK:        false,
		ProxyUsed: workbuddy.RedactProxyURL(p),
		Direct:    p == "",
		LatencyMs: int(time.Since(started).Milliseconds()),
		Error:     errMsg,
	}
}

// ProbeNode 使用默认客户端探测节点延迟、模型数与凭证数。
func ProbeNode(ctx context.Context, spec NodeSpec) (time.Duration, int, int, error) {
	return DefaultClient().ProbeNode(ctx, spec)
}

// Models 使用默认客户端获取节点模型目录。
func Models(ctx context.Context, spec NodeSpec) ([]ModelItem, error) {
	return DefaultClient().Models(ctx, spec)
}

// ChatStream 使用默认客户端向节点发起流式对话请求。
func ChatStream(ctx context.Context, spec NodeSpec, openaiBody []byte) (*http.Response, error) {
	return DefaultClient().ChatStream(ctx, spec, openaiBody)
}
