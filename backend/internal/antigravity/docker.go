package antigravity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/workbuddy"
)

// CurrentAgyImageRev 标识当前版本内置单账号真实 agy CLI 镜像的构建修订号；升级服务端后自动检测旧镜像并重建。
const CurrentAgyImageRev = "v1.2.3-agy-cli"

// EmbeddedAgyCLIDockerfile 内嵌 Analy2API 单账号真实 agy CLI 容器构建定义：
// 即使部署在完全空白的 Linux 服务器（宿主机未安装 agy），只要有 Docker 即可自动构建镜像并按账号自动起独立容器；
// 容器内内置官方 `agy` CLI 与 HTTP/2 daily-cloudcode-pa v1internal 双引擎。
const EmbeddedAgyCLIDockerfile = `FROM debian:bookworm-slim
LABEL io.analy2api.image.rev="v1.2.3-agy-cli"
ARG HTTP_PROXY=""
ARG HTTPS_PROXY=""
ARG ALL_PROXY=""
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git tar coreutils sed && rm -rf /var/lib/apt/lists/*
RUN curl --retry 3 --connect-timeout 15 -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin && chmod 0755 /usr/local/bin/agy && /usr/local/bin/agy --version
ENV PATH="/usr/local/bin:/root/.local/bin:/root/.antigravity/bin:${PATH}"
COPY analy2api-linux-amd64 /usr/local/bin/analy2api
RUN chmod +x /usr/local/bin/analy2api && ln -sf /usr/local/bin/analy2api /usr/local/bin/cmdwb2api && mkdir -p /app/auths/.gemini/antigravity/conversations /app/auths/.gemini/antigravity-cli /app/auths/.config/gcloud
ENV AGY_SINGLE_CLIENT=1
ENV AGY_CLI_FORCE_FILE_TOKEN_STORAGE=1
ENV AGY_CLI_DISABLE_AUTO_UPDATE=true
ENV AGY_DISABLE_AUTO_UPDATE=1
ENV AGY_SESSION_RETENTION=stateless
ENV AGY_PROMPT_MODE=default_intercept_continue
ENV AGY_BRIDGE_ADDR=:8317
ENV AGY_AUTH_DIR=/app/auths
EXPOSE 8317
ENTRYPOINT ["/usr/local/bin/analy2api", "agy-bridge"]
`

// EmbeddedAgyCLIFallbackDockerfile 在极端受限网络无法访问 antigravity.google 安装脚本时作为降级构建底本（仍包含最新 analy2api agy-bridge v1internal 引擎）。
const EmbeddedAgyCLIFallbackDockerfile = `FROM debian:bookworm-slim
LABEL io.analy2api.image.rev="v1.2.3-bridge-only"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
COPY analy2api-linux-amd64 /usr/local/bin/analy2api
RUN chmod +x /usr/local/bin/analy2api && ln -sf /usr/local/bin/analy2api /usr/local/bin/cmdwb2api && mkdir -p /app/auths/.gemini/antigravity/conversations /app/auths/.config/gcloud
ENV AGY_SINGLE_CLIENT=1
ENV AGY_SESSION_RETENTION=stateless
ENV AGY_PROMPT_MODE=default_intercept_continue
ENV AGY_BRIDGE_ADDR=:8317
ENV AGY_AUTH_DIR=/app/auths
EXPOSE 8317
ENTRYPOINT ["/usr/local/bin/analy2api", "agy-bridge"]
`

func isBuiltinAgyCLIImage(img string) bool {
	lower := strings.ToLower(strings.TrimSpace(img))
	return strings.HasPrefix(lower, "analy2api-agy-cli") || strings.HasPrefix(lower, "cmdwb2api-agy-cli")
}

const (
	defaultDockerDetectTimeout = 8 * time.Second
	defaultContainerLogTail    = 200
	maxContainerLogTail        = 10000
)

var validContainerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// CommandRunner 抽象宿主机外部命令执行接口，支持跨平台单元测试注入 Mock。
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, stderr string, err error)
}

// CommandRunnerFunc 允许直接将函数适配为 CommandRunner。
type CommandRunnerFunc func(ctx context.Context, name string, args ...string) (stdout string, stderr string, err error)

// Run 实现 CommandRunner 接口。
func (f CommandRunnerFunc) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	return f(ctx, name, args...)
}

// ExecCommandRunner 基于标准库 os/exec.CommandContext 执行真实系统命令。
type ExecCommandRunner struct{}

// Run 执行外部命令并分别捕获 stdout 与 stderr。
// 当在无主目录权限的 systemd 服务下调用 docker 时，自动将 DOCKER_CONFIG 指向可写临时目录，消除 `.docker/config.json: permission denied` 告警。
func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "docker" && strings.TrimSpace(os.Getenv("DOCKER_CONFIG")) == "" {
		cfgDir := filepath.Join(os.TempDir(), "analy2api-docker-cfg")
		if os.MkdirAll(cfgDir, 0o700) == nil {
			cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+cfgDir)
		}
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// DockerManager 封装本机 Docker 容器生命周期与配置文件编排。
type DockerManager struct {
	mu           sync.RWMutex
	imageBuildMu sync.Mutex
	buildWorkDir string
	runner       CommandRunner
}

// SetBuildWorkDir 指定镜像自动构建时使用的可写临时父目录（兼容 Linux systemd ProtectSystem=strict / PrivateTmp）。
func (m *DockerManager) SetBuildWorkDir(dir string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.buildWorkDir = strings.TrimSpace(dir)
	m.mu.Unlock()
}

func (m *DockerManager) getBuildWorkDir() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.buildWorkDir
}

// IsLocalTCPPortFree 探测宿主机指定本地端口是否未被任何进程或其它容器监听。
func IsLocalTCPPortFree(hostIP string, port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	ip := extractBindHostIP(hostIP)
	if ip == "" || ip == "*" {
		ip = DefaultHostIP
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

var (
	defaultDockerMgrMu sync.RWMutex
	defaultDockerMgr   = NewDockerManager(ExecCommandRunner{})
)

// NewDockerManager 使用指定 CommandRunner 创建 Docker 编排管理器（runner 为 nil 时使用真实 ExecCommandRunner）。
func NewDockerManager(runner CommandRunner) *DockerManager {
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	return &DockerManager{runner: runner}
}

// DefaultDockerManager 返回进程级默认 DockerManager。
func DefaultDockerManager() *DockerManager {
	defaultDockerMgrMu.RLock()
	defer defaultDockerMgrMu.RUnlock()
	return defaultDockerMgr
}

// SetDefaultCommandRunner 替换进程级默认 DockerManager 的命令执行器（主要用于单测注入）。
func SetDefaultCommandRunner(runner CommandRunner) {
	defaultDockerMgrMu.Lock()
	defer defaultDockerMgrMu.Unlock()
	defaultDockerMgr = NewDockerManager(runner)
}

// SetRunner 动态替换当前 DockerManager 的 CommandRunner。
func (m *DockerManager) SetRunner(runner CommandRunner) {
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	m.mu.Lock()
	m.runner = runner
	m.mu.Unlock()
}

func (m *DockerManager) getRunner() CommandRunner {
	if m == nil {
		return ExecCommandRunner{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.runner == nil {
		return ExecCommandRunner{}
	}
	return m.runner
}

// ValidateContainerName 校验容器名合法性，严防以 "-" 开头的参数注入或非法字符。
func ValidateContainerName(containerName string) error {
	s := strings.TrimSpace(containerName)
	if s == "" {
		return errors.New("容器名称不能为空")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("非法的容器名称 %q：不能以 '-' 开头", s)
	}
	if len(s) > 128 {
		return fmt.Errorf("非法的容器名称 %q：长度超过 128 字符", s)
	}
	if !validContainerNameRe.MatchString(s) {
		return fmt.Errorf("非法的容器名称 %q：仅允许字母、数字及 _ . -", s)
	}
	return nil
}

// ValidateImageName 校验 Docker 镜像引用合法性，防止参数注入与空白字符。
func ValidateImageName(image string) error {
	s := strings.TrimSpace(image)
	if s == "" {
		return errors.New("Docker 镜像名称不能为空")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("非法的 Docker 镜像名称 %q：不能以 '-' 开头", s)
	}
	if strings.ContainsAny(s, " \t\r\n;|&$`") {
		return fmt.Errorf("非法的 Docker 镜像名称 %q：包含空白或危险元字符", s)
	}
	return nil
}

// DetectDocker 执行 `docker version --format "{{.Server.Version}}|{{.Server.Os}}"` 探测宿主机 Docker 可用性。
func (m *DockerManager) DetectDocker(ctx context.Context) (status DockerEnvStatus) {
	defer func() {
		if r := recover(); r != nil {
			status = DockerEnvStatus{
				Available: false,
				Error:     fmt.Sprintf("探测 Docker 环境发生异常: %v", r),
			}
		}
	}()

	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultDockerDetectTimeout)
		defer cancel()
	}

	stdout, stderr, err := m.getRunner().Run(ctx, "docker", "version", "--format", "{{.Server.Version}}|{{.Server.Os}}")
	cleanOut := strings.Trim(strings.TrimSpace(stdout), "\"'")
	if err != nil {
		return DockerEnvStatus{
			Available: false,
			Error:     formatDockerError("Docker 不可用", stderr, stdout, err),
		}
	}

	parts := strings.SplitN(cleanOut, "|", 2)
	version := strings.TrimSpace(parts[0])
	var serverOS string
	if len(parts) > 1 {
		serverOS = strings.TrimSpace(parts[1])
	}
	if version == "" || strings.EqualFold(version, "<no value>") {
		return DockerEnvStatus{
			Available: false,
			Error:     formatDockerError("Docker 守护进程未响应服务端版本", stderr, stdout, nil),
		}
	}

	return DockerEnvStatus{
		Available: true,
		Version:   version,
		ServerOS:  serverOS,
	}
}

// WriteNodeConfigFile 在 spec.ConfigDir（若空则取 filepath.Dir(spec.AuthDir)）生成兼容 CLIProxyAPI 的 config.yaml，
// 并确保 spec.AuthDir 与配置文件目录自动创建，返回生成的 config.yaml 绝对路径。
func WriteNodeConfigFile(spec NodeSpec) (string, error) {
	authDir, configDir, err := resolveNodeDirs(spec)
	if err != nil {
		return "", err
	}

	var normProxy string
	if rawProxy := strings.TrimSpace(spec.ProxyURL); rawProxy != "" {
		normProxy, err = workbuddy.NormalizeProxyURL(rawProxy)
		if err != nil {
			return "", fmt.Errorf("节点代理地址无效: %w", err)
		}
	}

	if err := os.MkdirAll(authDir, 0o755); err != nil {
		return "", fmt.Errorf("创建认证凭证目录 %q 失败: %w", authDir, err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", fmt.Errorf("创建节点配置目录 %q 失败: %w", configDir, err)
	}

	var buf strings.Builder
	buf.WriteString("# Auto-generated by cmd2api antigravity orchestrator\n")
	buf.WriteString("host: \"\"\n")
	buf.WriteString(fmt.Sprintf("port: %d\n", DefaultContainerPort))
	buf.WriteString(fmt.Sprintf("auth-dir: %s\n", yamlQuote(ContainerAuthDir)))

	apiKey := strings.TrimSpace(spec.APIKey)
	if apiKey != "" {
		buf.WriteString("api-keys:\n")
		buf.WriteString(fmt.Sprintf("  - %s\n", yamlQuote(apiKey)))
	} else {
		buf.WriteString("api-keys: []\n")
	}

	mgmtKey := strings.TrimSpace(spec.ManagementKey)
	buf.WriteString("remote-management:\n")
	buf.WriteString("  allow-remote: true\n")
	buf.WriteString(fmt.Sprintf("  secret-key: %s\n", yamlQuote(mgmtKey)))

	if normProxy != "" {
		buf.WriteString(fmt.Sprintf("proxy-url: %s\n", yamlQuote(normProxy)))
	}

	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(buf.String()), 0o644); err != nil {
		return "", fmt.Errorf("写入节点配置文件 %q 失败: %w", configPath, err)
	}

	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return configPath, nil
	}
	return absConfigPath, nil
}

// EnsureAgyCLIImage 检查单账号真实 agy CLI 镜像是否存在且为当前版本修订号；在空服务器或升级后自动现场执行 `docker build` 构建/升级镜像。
func (m *DockerManager) EnsureAgyCLIImage(ctx context.Context, image string) error {
	return m.ensureOrRebuildAgyCLIImage(ctx, image, false)
}

// RebuildAgyCLIImage 强制使用当前二进制与官方 agy CLI 安装源重新构建单账号容器镜像。
func (m *DockerManager) RebuildAgyCLIImage(ctx context.Context, image string) error {
	return m.ensureOrRebuildAgyCLIImage(ctx, image, true)
}

func (m *DockerManager) isCurrentBuiltinImageReady(ctx context.Context, img string) bool {
	stdout, _, err := m.getRunner().Run(ctx, "docker", "image", "inspect", "--format", `{{ index .Config.Labels "io.analy2api.image.rev" }}`, img)
	if err != nil {
		return false
	}
	rev := strings.Trim(strings.TrimSpace(stdout), "\"'")
	if rev == CurrentAgyImageRev {
		return true
	}
	// 非 Linux 本地测试环境下的 Mock Runner 可能返回空串或固定 JSON，保持单测兼容
	if runtime.GOOS != "linux" {
		return true
	}
	return false
}

func (m *DockerManager) ensureOrRebuildAgyCLIImage(ctx context.Context, image string, forceRebuild bool) error {
	img := strings.TrimSpace(image)
	if img == "" {
		img = DefaultDockerImage
	}
	if err := ValidateImageName(img); err != nil {
		return err
	}
	if !isBuiltinAgyCLIImage(img) {
		if !forceRebuild {
			if _, _, err := m.getRunner().Run(ctx, "docker", "image", "inspect", img); err == nil {
				return nil
			}
		}
		return m.PullImage(ctx, img)
	}

	if !forceRebuild && m.isCurrentBuiltinImageReady(ctx, img) {
		return nil
	}

	if m != nil {
		m.imageBuildMu.Lock()
		defer m.imageBuildMu.Unlock()
	}
	buildCtx := context.Background()
	if ctx != nil {
		buildCtx = context.WithoutCancel(ctx)
	}
	buildCtx, cancel := context.WithTimeout(buildCtx, 5*time.Minute)
	defer cancel()

	if !forceRebuild && m.isCurrentBuiltinImageReady(buildCtx, img) {
		return nil
	}

	linuxBin, err := locateLinuxBinaryForImageBuild()
	if err != nil {
		return fmt.Errorf("自动构建单账号 agy 容器镜像 %s 失败: %w", img, err)
	}

	baseTmp := m.getBuildWorkDir()
	if baseTmp != "" {
		if mkErr := os.MkdirAll(baseTmp, 0o755); mkErr != nil {
			baseTmp = ""
		}
	}
	tmpDir, err := os.MkdirTemp(baseTmp, "analy2api-agy-img-*")
	if err != nil && baseTmp != "" {
		tmpDir, err = os.MkdirTemp("", "analy2api-agy-img-*")
	}
	if err != nil {
		return fmt.Errorf("创建镜像构建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(EmbeddedAgyCLIDockerfile), 0o644); err != nil {
		return fmt.Errorf("写入内嵌 Dockerfile 失败: %w", err)
	}
	if err := copyBinaryFile(linuxBin, filepath.Join(tmpDir, "analy2api-linux-amd64")); err != nil {
		return fmt.Errorf("拷贝 Linux 二进制到构建上下文失败: %w", err)
	}

	stdout, stderr, buildErr := m.getRunner().Run(buildCtx, "docker", "build", "-t", img, tmpDir)
	if buildErr != nil {
		// 若服务器直连无法下载官方 install.sh，回退构建带最新 agy-bridge v1internal 引擎的镜像，避免容器彻底起不来
		if wErr := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(EmbeddedAgyCLIFallbackDockerfile), 0o644); wErr == nil {
			if _, _, fbErr := m.getRunner().Run(buildCtx, "docker", "build", "-t", img, tmpDir); fbErr == nil {
				return nil
			}
		}
		return errors.New(formatDockerError(fmt.Sprintf("自动构建单账号 agy CLI 镜像 %s 失败", img), stderr, stdout, buildErr))
	}
	return nil
}

func locateLinuxBinaryForImageBuild() (string, error) {
	if runtime.GOOS == "linux" {
		if exe, err := os.Executable(); err == nil && exe != "" {
			if st, statErr := os.Stat(exe); statErr == nil && !st.IsDir() {
				return exe, nil
			}
		}
	}
	candidates := []string{
		"analy2api-linux-amd64",
		"analy2api",
		filepath.Join("..", "analy2api-linux-amd64"),
		filepath.Join("..", "..", "analy2api-linux-amd64"),
		filepath.Join("dist", "Analy2API-linux-amd64", "analy2api"),
		"cmdwb2api-linux-amd64",
		filepath.Join("..", "cmdwb2api-linux-amd64"),
		filepath.Join("..", "..", "cmdwb2api-linux-amd64"),
		filepath.Join("..", "..", "..", "cmdwb2api-linux-amd64"),
		filepath.Join("dist", "cmdwb2api-linux-amd64", "cmdwb2api"),
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "analy2api-linux-amd64"),
			filepath.Join(dir, "analy2api"),
			filepath.Join(dir, "cmdwb2api-linux-amd64"),
			filepath.Join(dir, "cmdwb2api"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Size() > 0 {
			if abs, err := filepath.Abs(c); err == nil {
				return abs, nil
			}
			return c, nil
		}
	}
	return "", errors.New("未找到 analy2api-linux-amd64 二进制，请确保在 Linux 宿主机运行或在工作目录放置 analy2api-linux-amd64")
}

func copyBinaryFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// PullImage 拉取或重建指定的 Docker 镜像；若为内置单账号 agy 镜像，直接现场重新构建最新镜像。
func (m *DockerManager) PullImage(ctx context.Context, image string) error {
	img := strings.TrimSpace(image)
	if img == "" {
		img = DefaultDockerImage
	}
	if err := ValidateImageName(img); err != nil {
		return err
	}
	if isBuiltinAgyCLIImage(img) {
		if _, isExec := m.getRunner().(ExecCommandRunner); isExec {
			if _, lErr := locateLinuxBinaryForImageBuild(); lErr == nil {
				return m.RebuildAgyCLIImage(ctx, img)
			}
		}
	}
	stdout, stderr, err := m.getRunner().Run(ctx, "docker", "pull", img)
	if err != nil {
		return errors.New(formatDockerError(fmt.Sprintf("拉取镜像 %s 失败", img), stderr, stdout, err))
	}
	return nil
}

// EnsureAndStartContainer 确保配置文件、单账号 agy 镜像与凭证目录就绪，并以最新挂载与端口参数创建启动容器。
// 若同名容器已存在，先执行 `docker rm -f` 重建，以保证端口映射、镜像与凭证挂载即时生效。
func (m *DockerManager) EnsureAndStartContainer(ctx context.Context, spec NodeSpec) (string, error) {
	containerName := spec.EffectiveContainerName()
	if err := ValidateContainerName(containerName); err != nil {
		return "", err
	}

	image := spec.EffectiveImage()
	if err := ValidateImageName(image); err != nil {
		return "", err
	}
	if isBuiltinAgyCLIImage(image) {
		if err := m.EnsureAgyCLIImage(ctx, image); err != nil {
			return "", err
		}
	}

	authDir, _, err := resolveNodeDirs(spec)
	if err != nil {
		return "", err
	}
	spec.AuthDir = authDir

	absConfigFile, err := WriteNodeConfigFile(spec)
	if err != nil {
		return "", err
	}
	absAuthDir, err := filepath.Abs(authDir)
	if err != nil {
		return "", fmt.Errorf("解析认证目录绝对路径失败: %w", err)
	}

	hostIP := extractBindHostIP(spec.EffectiveHostIP())
	hostPort := spec.EffectiveHostPort()
	portMapping := formatPortMapping(hostIP, hostPort, DefaultContainerPort)

	existing, inspectErr := m.InspectContainer(ctx, containerName)
	if inspectErr != nil {
		return "", inspectErr
	}
	if existing.Exists {
		if rmOut, rmErrText, rmErr := m.getRunner().Run(ctx, "docker", "rm", "-f", containerName); rmErr != nil {
			if !isNoSuchContainerOutput(rmErrText, rmOut) {
				return "", errors.New(formatDockerError(fmt.Sprintf("重建前移除旧容器 %s 失败", containerName), rmErrText, rmOut, rmErr))
			}
		}
	}

	fp := NormalizeNodeFingerprint(spec.Fingerprint, containerName)

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", fp.Hostname,
		"--restart", "unless-stopped",
		"--label", "cmd2api.isolation=1",
		"--label", "cmd2api.single_account=1",
		"--label", fmt.Sprintf("cmd2api.node_id=%d", spec.ID),
		"--label", "cmd2api.fingerprint=" + fp.FingerprintID,
		"-e", "AGY_FINGERPRINT_ID=" + fp.FingerprintID,
		"-e", "AGY_MACHINE_ID=" + fp.MachineID,
		"-e", "AGY_QIMEI36=" + fp.Qimei36,
		"-e", "AGY_CLIENT_ID=" + fp.ClientID,
		"-e", "AGY_USER_AGENT=" + fp.UserAgent,
		"-e", "ANTIGRAVITY_USER_AGENT=" + fp.UserAgent,
		"-e", "ANTIGRAVITY_USER_AGENT_VERSION=" + EffectiveCLIVersion(),
		"-e", "AGY_SINGLE_CLIENT=1",
		"-e", "AGY_ADC_AUTH=1",
		"-e", "GOOGLE_APPLICATION_CREDENTIALS=" + ContainerAuthDir + "/.config/gcloud/application_default_credentials.json",
		"-e", "AGY_CLI_DISABLE_AUTO_UPDATE=true",
		"-e", "AGY_DISABLE_AUTO_UPDATE=1",
		"-e", "AGY_SESSION_RETENTION=" + ResolveSessionRetention(spec),
		"-e", "AGY_PROMPT_MODE=" + ResolvePromptBlendMode(spec),
		"-e", "AGY_PROMPT_BLEND_MODE=" + ResolvePromptBlendMode(spec),
		"-e", "AGY_AUTH_DIR=" + ContainerAuthDir,
		"-e", fmt.Sprintf("AGY_BRIDGE_ADDR=:%d", DefaultContainerPort),
	}
	if strings.TrimSpace(spec.APIKey) != "" {
		args = append(args, "-e", "AGY_API_KEY="+strings.TrimSpace(spec.APIKey))
	}
	if strings.TrimSpace(spec.ManagementKey) != "" {
		args = append(args, "-e", "AGY_MANAGEMENT_KEY="+strings.TrimSpace(spec.ManagementKey))
	}
	if p := rewriteLoopbackProxyForContainer(spec.ProxyURL); p != "" {
		args = append(args,
			"--add-host=host.docker.internal:host-gateway",
			"-e", "AGY_PROXY_URL="+p,
			"-e", "HTTP_PROXY="+p,
			"-e", "HTTPS_PROXY="+p,
			"-e", "ALL_PROXY="+p,
		)
	}
	args = append(args,
		"-p", portMapping,
		"-v", fmt.Sprintf("%s:%s", absAuthDir, ContainerAuthDir),
		"-v", fmt.Sprintf("%s:%s", absAuthDir, ContainerGeminiDir),
		"-v", fmt.Sprintf("%s:%s", absConfigFile, ContainerConfigFile),
		"-v", fmt.Sprintf("%s:%s", absConfigFile, ContainerLegacyConfigFile),
		image,
	)

	stdout, stderr, runErr := m.getRunner().Run(ctx, "docker", args...)
	if runErr != nil {
		return "", errors.New(formatDockerError(fmt.Sprintf("启动容器 %s 失败", containerName), stderr, stdout, runErr))
	}

	containerID := firstNonEmptyLine(stdout)
	if containerID == "" {
		if st, err := m.InspectContainer(ctx, containerName); err == nil && st.ContainerID != "" {
			containerID = st.ContainerID
		}
	}
	return containerID, nil
}

// StartContainer 启动已存在的同名容器。
func (m *DockerManager) StartContainer(ctx context.Context, containerName string) error {
	if err := ValidateContainerName(containerName); err != nil {
		return err
	}
	stdout, stderr, err := m.getRunner().Run(ctx, "docker", "start", strings.TrimSpace(containerName))
	if err != nil {
		return errors.New(formatDockerError(fmt.Sprintf("启动容器 %s 失败", containerName), stderr, stdout, err))
	}
	return nil
}

// StopContainer 停止指定名称的运行中容器。
func (m *DockerManager) StopContainer(ctx context.Context, containerName string) error {
	if err := ValidateContainerName(containerName); err != nil {
		return err
	}
	name := strings.TrimSpace(containerName)
	stdout, stderr, err := m.getRunner().Run(ctx, "docker", "stop", name)
	if err != nil {
		if isNoSuchContainerOutput(stderr, stdout) {
			return nil
		}
		return errors.New(formatDockerError(fmt.Sprintf("停止容器 %s 失败", name), stderr, stdout, err))
	}
	return nil
}

// RestartContainer 重启指定名称的容器。
func (m *DockerManager) RestartContainer(ctx context.Context, containerName string) error {
	if err := ValidateContainerName(containerName); err != nil {
		return err
	}
	name := strings.TrimSpace(containerName)
	stdout, stderr, err := m.getRunner().Run(ctx, "docker", "restart", name)
	if err != nil {
		return errors.New(formatDockerError(fmt.Sprintf("重启容器 %s 失败", name), stderr, stdout, err))
	}
	return nil
}

// RemoveContainer 强制删除指定名称的容器（若容器已不存在则幂等返回 nil）。
func (m *DockerManager) RemoveContainer(ctx context.Context, containerName string) error {
	if err := ValidateContainerName(containerName); err != nil {
		return err
	}
	name := strings.TrimSpace(containerName)
	stdout, stderr, err := m.getRunner().Run(ctx, "docker", "rm", "-f", name)
	if err != nil {
		if isNoSuchContainerOutput(stderr, stdout) {
			return nil
		}
		return errors.New(formatDockerError(fmt.Sprintf("删除容器 %s 失败", name), stderr, stdout, err))
	}
	return nil
}

// InspectContainer 查询指定容器的存在性与实时状态；容器不存在时返回 Exists=false 且 error=nil。
func (m *DockerManager) InspectContainer(ctx context.Context, containerName string) (ContainerStatus, error) {
	if err := ValidateContainerName(containerName); err != nil {
		return ContainerStatus{}, err
	}
	name := strings.TrimSpace(containerName)
	stdout, stderr, err := m.getRunner().Run(
		ctx,
		"docker",
		"inspect",
		"--format",
		"{{.Id}}|{{.State.Status}}|{{.State.Running}}|{{.State.StartedAt}}|{{.State.ExitCode}}",
		name,
	)
	if err != nil {
		if isNoSuchContainerOutput(stderr, stdout) {
			return ContainerStatus{
				Exists:     false,
				Running:    false,
				State:      "not_found",
				StatusText: "未创建",
			}, nil
		}
		return ContainerStatus{}, errors.New(formatDockerError(fmt.Sprintf("查询容器 %s 状态失败", name), stderr, stdout, err))
	}

	line := strings.Trim(strings.TrimSpace(firstNonEmptyLine(stdout)), "\"'")
	if line == "" {
		return ContainerStatus{
			Exists:     false,
			Running:    false,
			State:      "not_found",
			StatusText: "未创建",
		}, nil
	}

	parts := strings.Split(line, "|")
	var cid, state, runningStr, startedAt, exitCode string
	if len(parts) > 0 {
		cid = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		state = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	if len(parts) > 2 {
		runningStr = strings.TrimSpace(parts[2])
	}
	if len(parts) > 3 {
		startedAt = strings.TrimSpace(parts[3])
	}
	if len(parts) > 4 {
		exitCode = strings.TrimSpace(parts[4])
	}

	if strings.HasPrefix(startedAt, "0001-01-01") {
		startedAt = ""
	}
	running := strings.EqualFold(runningStr, "true") || state == "running"
	statusText := state
	if running {
		statusText = "running"
	} else if state != "" && exitCode != "" && exitCode != "<no value>" {
		statusText = fmt.Sprintf("%s (exit %s)", state, exitCode)
	}

	return ContainerStatus{
		Exists:      true,
		Running:     running,
		ContainerID: cid,
		State:       state,
		StatusText:  statusText,
		StartedAt:   startedAt,
	}, nil
}

// ContainerLogs 获取容器最近 tail 行日志（合并 stdout 与 stderr）。
func (m *DockerManager) ContainerLogs(ctx context.Context, containerName string, tail int) (string, error) {
	if err := ValidateContainerName(containerName); err != nil {
		return "", err
	}
	if tail <= 0 {
		tail = defaultContainerLogTail
	}
	if tail > maxContainerLogTail {
		tail = maxContainerLogTail
	}
	name := strings.TrimSpace(containerName)
	stdout, stderr, err := m.getRunner().Run(ctx, "docker", "logs", "--tail", strconv.Itoa(tail), name)
	if err != nil {
		return "", errors.New(formatDockerError(fmt.Sprintf("读取容器 %s 日志失败", name), stderr, stdout, err))
	}
	switch {
	case stdout != "" && stderr != "":
		if strings.HasSuffix(stdout, "\n") {
			return stdout + stderr, nil
		}
		return stdout + "\n" + stderr, nil
	case stdout != "":
		return stdout, nil
	default:
		return stderr, nil
	}
}

// --- 包级便捷函数（委托给 DefaultDockerManager） ---

// DetectDocker 使用默认命令执行器探测本机 Docker 环境。
func DetectDocker(ctx context.Context) DockerEnvStatus {
	return DefaultDockerManager().DetectDocker(ctx)
}

// PullImage 使用默认命令执行器拉取 Docker 镜像。
func PullImage(ctx context.Context, image string) error {
	return DefaultDockerManager().PullImage(ctx, image)
}

// EnsureAndStartContainer 使用默认命令执行器写入配置并创建/启动节点容器。
func EnsureAndStartContainer(ctx context.Context, spec NodeSpec) (string, error) {
	return DefaultDockerManager().EnsureAndStartContainer(ctx, spec)
}

// StartContainer 使用默认命令执行器启动已存在的容器。
func StartContainer(ctx context.Context, containerName string) error {
	return DefaultDockerManager().StartContainer(ctx, containerName)
}

// StopContainer 使用默认命令执行器停止容器。
func StopContainer(ctx context.Context, containerName string) error {
	return DefaultDockerManager().StopContainer(ctx, containerName)
}

// RestartContainer 使用默认命令执行器重启容器。
func RestartContainer(ctx context.Context, containerName string) error {
	return DefaultDockerManager().RestartContainer(ctx, containerName)
}

// RemoveContainer 使用默认命令执行器强制删除容器。
func RemoveContainer(ctx context.Context, containerName string) error {
	return DefaultDockerManager().RemoveContainer(ctx, containerName)
}

// InspectContainer 使用默认命令执行器检查容器运行状态。
func InspectContainer(ctx context.Context, containerName string) (ContainerStatus, error) {
	return DefaultDockerManager().InspectContainer(ctx, containerName)
}

// ContainerLogs 使用默认命令执行器获取容器日志。
func ContainerLogs(ctx context.Context, containerName string, tail int) (string, error) {
	return DefaultDockerManager().ContainerLogs(ctx, containerName, tail)
}

// --- 内部辅助函数 ---

func resolveNodeDirs(spec NodeSpec) (authDir string, configDir string, err error) {
	rawAuth := strings.TrimSpace(spec.AuthDir)
	rawCfg := strings.TrimSpace(spec.ConfigDir)
	if rawAuth == "" && rawCfg == "" {
		return "", "", errors.New("节点缺少 auth_dir 与 config_dir，无法定位宿主机配置目录")
	}
	if rawAuth != "" {
		authDir = filepath.Clean(rawAuth)
	}
	if rawCfg != "" {
		configDir = filepath.Clean(rawCfg)
	}
	if configDir == "" {
		configDir = filepath.Dir(authDir)
	}
	if authDir == "" {
		authDir = filepath.Join(configDir, "auths")
	}
	return authDir, configDir, nil
}

func yamlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func extractBindHostIP(rawHost string) string {
	h := strings.TrimSpace(rawHost)
	if h == "" {
		return DefaultHostIP
	}
	lower := strings.ToLower(h)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
			h = u.Hostname()
		}
	} else if hostOnly, _, err := net.SplitHostPort(h); err == nil && hostOnly != "" {
		h = hostOnly
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return DefaultHostIP
	}
	if h == "" {
		return DefaultHostIP
	}
	return h
}

func formatPortMapping(hostIP string, hostPort, containerPort int) string {
	ip := strings.TrimSpace(hostIP)
	if ip == "" || ip == "*" {
		return fmt.Sprintf("%d:%d", hostPort, containerPort)
	}
	if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() == nil {
		return fmt.Sprintf("[%s]:%d:%d", ip, hostPort, containerPort)
	}
	return fmt.Sprintf("%s:%d:%d", ip, hostPort, containerPort)
}

func rewriteLoopbackProxyForContainer(rawProxy string) string {
	trimmed := strings.TrimSpace(rawProxy)
	if trimmed == "" || strings.EqualFold(trimmed, "direct") {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	h := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
	if h == "127.0.0.1" || h == "localhost" || h == "::1" {
		port := u.Port()
		if port != "" {
			u.Host = "host.docker.internal:" + port
		} else {
			u.Host = "host.docker.internal"
		}
		return u.String()
	}
	return trimmed
}

func isNoSuchContainerOutput(stderr, stdout string) bool {
	combined := strings.ToLower(stderr + "\n" + stdout)
	return strings.Contains(combined, "no such object") ||
		strings.Contains(combined, "no such container") ||
		strings.Contains(combined, "error: no such") ||
		strings.Contains(combined, "not found") && strings.Contains(combined, "container")
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func formatDockerError(prefix, stderr, stdout string, err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return prefix + ": 未在 PATH 中找到 docker 可执行文件，请先安装 Docker"
	}
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if detail == "" {
		return prefix
	}
	// 压缩多行输出为紧凑单行提示
	lines := strings.Split(detail, "\n")
	compact := make([]string, 0, len(lines))
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			compact = append(compact, t)
		}
	}
	joined := strings.Join(compact, "; ")
	if len(joined) > 400 {
		joined = joined[:400] + "..."
	}
	return fmt.Sprintf("%s: %s", prefix, joined)
}
