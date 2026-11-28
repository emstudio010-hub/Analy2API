package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/accountgroup"
	"cmd2api/ent/apikey"
	"cmd2api/ent/containernode"
	"cmd2api/ent/group"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/crypto"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/workbuddy"
)

var (
	// ErrContainerNodeNotFound 表示指定的容器节点不存在。
	ErrContainerNodeNotFound = errors.New("容器节点不存在")
	// ErrInvalidContainerNodeMode 表示容器节点模式不合法。
	ErrInvalidContainerNodeMode = errors.New("节点模式无效，仅支持 local_docker 或 remote_api")
	// ErrInvalidContainerNodeAction 表示节点运维动作不受支持。
	ErrInvalidContainerNodeAction = errors.New("不支持的节点运维动作，仅支持 start、stop、restart、pull 或 check")
)

// AntigravityEnvInfo 对齐前端 AntigravityEnvResponse 结构。
type AntigravityEnvInfo struct {
	DockerAvailable bool   `json:"docker_available"`
	DockerVersion   string `json:"docker_version"`
	ServerOS        string `json:"server_os,omitempty"`
	Error           string `json:"error,omitempty"`
	DefaultImage    string `json:"default_image"`
	DataRoot        string `json:"data_root"`
}

// ContainerNodeView 对齐前端 ContainerNode 结构（脱敏返回密钥状态，附带 1容器:1IP:1指纹 隔离诊断）。
type ContainerNodeView struct {
	ID                  int64                        `json:"id"`
	Name                string                       `json:"name"`
	Mode                string                       `json:"mode"`
	DockerImage         string                       `json:"docker_image"`
	ContainerName       string                       `json:"container_name"`
	ContainerID         string                       `json:"container_id"`
	HostIP              string                       `json:"host_ip"`
	HostPort            int                          `json:"host_port"`
	BaseURL             string                       `json:"base_url"`
	AuthDir             string                       `json:"auth_dir"`
	HasManagementKey    bool                         `json:"has_management_key"`
	MaskedManagementKey string                       `json:"masked_management_key"`
	HasAPIKey           bool                         `json:"has_api_key"`
	MaskedAPIKey        string                       `json:"masked_api_key"`
	ProxyURL            string                       `json:"proxy_url"`
	StrictIsolation     bool                         `json:"strict_isolation"`
	Fingerprint         antigravity.NodeFingerprint  `json:"fingerprint"`
	IsolationStatus     string                       `json:"isolation_status"`
	IsolationWarnings   []string                     `json:"isolation_warnings,omitempty"`
	LastExitIP          string                       `json:"last_exit_ip,omitempty"`
	LastIPCheckedAt     string                       `json:"last_ip_checked_at,omitempty"`
	Status              string                       `json:"status"`
	AuthFilesCount      int                          `json:"auth_files_count"`
	ModelsCount         int                          `json:"models_count,omitempty"`
	LatencyMs           *int                         `json:"latency_ms,omitempty"`
	Runtime             *antigravity.ContainerStatus `json:"runtime,omitempty"`
	LastSyncAt          *time.Time                   `json:"last_sync_at"`
	LastError           *string                      `json:"last_error"`
	Extra               map[string]any               `json:"extra"`
	CreatedAt           time.Time                    `json:"created_at"`
	UpdatedAt           time.Time                    `json:"updated_at"`
}

// NodeExitIPOutput 描述容器节点代理出口 IP 实时核验与跨容器碰撞结果。
type NodeExitIPOutput struct {
	antigravity.ExitIPProbeResult
	NodeID          int64              `json:"node_id"`
	NodeName        string             `json:"node_name"`
	IsolationStatus string             `json:"isolation_status"`
	SharedWithNodes []string           `json:"shared_with_nodes,omitempty"`
	Node            *ContainerNodeView `json:"node,omitempty"`
}

// CreateContainerNodeInput 是新建本机 Docker 或远程 API 节点的入参。
type CreateContainerNodeInput struct {
	Name            string
	Mode            string
	DockerImage     string
	ContainerName   string
	HostIP          string
	HostPort        int
	AuthDir         string
	ManagementKey   string
	APIKey          string
	ProxyURL        string
	StrictIsolation *bool
	AutoStart       bool
	Extra           map[string]any
}

// UpdateContainerNodeInput 是局部更新容器节点的入参。
type UpdateContainerNodeInput struct {
	Name            *string
	Mode            *string
	DockerImage     *string
	ContainerName   *string
	HostIP          *string
	HostPort        *int
	AuthDir         *string
	ManagementKey   *string
	APIKey          *string
	ProxyURL        *string
	StrictIsolation *bool
	Extra           map[string]any
}

// ContainerNodeActionOutput 对齐前端 ContainerNodeActionResult 结构。
type ContainerNodeActionOutput struct {
	OK             bool               `json:"ok"`
	Action         string             `json:"action"`
	Message        string             `json:"message,omitempty"`
	Error          *string            `json:"error,omitempty"`
	LatencyMs      int                `json:"latency_ms,omitempty"`
	ModelsCount    int                `json:"models_count,omitempty"`
	AuthFilesCount int                `json:"auth_files_count,omitempty"`
	Node           *ContainerNodeView `json:"node,omitempty"`
}

// ContainerNodeLogsOutput 对齐前端 ContainerNodeLogsResponse 结构。
type ContainerNodeLogsOutput struct {
	NodeID        int64  `json:"node_id"`
	ContainerName string `json:"container_name"`
	Tail          int    `json:"tail"`
	Logs          string `json:"logs"`
}

// AntigravityAuthFileView 对齐前端 AntigravityAuthFile 并附加已同步的调度账号 ID。
type AntigravityAuthFileView struct {
	antigravity.AuthFileInfo
	AccountID     *int64 `json:"account_id,omitempty"`
	AccountStatus string `json:"account_status,omitempty"`
}

// UploadAuthFileOutput 对齐前端 UploadAntigravityAuthFileResponse 结构。
type UploadAuthFileOutput struct {
	OK      bool                    `json:"ok"`
	File    AntigravityAuthFileView `json:"file"`
	Item    AntigravityAuthFileView `json:"item"`
	Account *ent.Account            `json:"account,omitempty"`
}

// SyncNodeAccountsOptions 控制节点同步到上游账号池时的可选参数。
type SyncNodeAccountsOptions struct {
	GroupIDs       []int64
	ProxyURL       string
	Concurrency    int
	Priority       int
	RateMultiplier float64
}

// SyncNodeAccountsOutput 对齐前端 SyncAntigravityAccountsResponse 结构。
type SyncNodeAccountsOutput struct {
	OK         bool               `json:"ok"`
	Created    int                `json:"created"`
	Updated    int                `json:"updated"`
	Total      int                `json:"total"`
	FilesCount int                `json:"files_count"`
	GroupID    int64              `json:"group_id,omitempty"`
	Accounts   []*ent.Account     `json:"accounts"`
	Node       *ContainerNodeView `json:"node,omitempty"`
	Errors     []string           `json:"errors,omitempty"`
}

// AntigravityService 负责 AntiGravity / CLIProxyAPI 容器节点编排、OAuth 登录凭证与账号池同步。
type AntigravityService struct {
	db            *ent.Client
	vault         *crypto.Vault
	agClient      *antigravity.Client
	docker        *antigravity.DockerManager
	proxyPool     *ProxyPoolService
	recorder      *Recorder
	dataRoot      string
	defaultImage  string
	logger        *slog.Logger
	portMu        sync.Mutex
	reservedPorts map[int]time.Time
}

// NewAntigravityService 构造 AntigravityService。
func NewAntigravityService(
	db *ent.Client,
	vault *crypto.Vault,
	agClient *antigravity.Client,
	docker *antigravity.DockerManager,
	dataRoot string,
	defaultImage string,
	logger *slog.Logger,
) *AntigravityService {
	if agClient == nil {
		agClient = antigravity.DefaultClient()
	}
	if docker == nil {
		docker = agClient.Docker()
	}
	if strings.TrimSpace(dataRoot) == "" {
		dataRoot = "./data/antigravity"
	}
	if docker != nil {
		docker.SetBuildWorkDir(filepath.Join(dataRoot, ".docker-build"))
	}
	if strings.TrimSpace(defaultImage) == "" {
		defaultImage = antigravity.DefaultDockerImage
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AntigravityService{
		db:            db,
		vault:         vault,
		agClient:      agClient,
		docker:        docker,
		dataRoot:      dataRoot,
		defaultImage:  defaultImage,
		logger:        logger,
		reservedPorts: make(map[int]time.Time),
	}
}

// SetProxyPoolService 注入代理 IP 池服务，用于 OAuth 与批量导入时按策略自动分 IP。
func (s *AntigravityService) SetProxyPoolService(pool *ProxyPoolService) {
	if s != nil {
		s.proxyPool = pool
	}
}

// ResolveAccountProxy 按 ip_mode ("auto_pool" | "manual" | "keep" | "direct") 为单个账号解析或从 IP 池分配出口代理。
func (s *AntigravityService) ResolveAccountProxy(
	ctx context.Context,
	ipMode string,
	manualProxy string,
	existingProxy string,
	inFlight map[string]int,
) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(ipMode))
	rawManual := strings.TrimSpace(manualProxy)
	rawExisting := strings.TrimSpace(existingProxy)

	switch mode {
	case "direct", "none":
		return "", nil
	case "manual":
		if rawManual == "" {
			return "", nil
		}
		norm, err := workbuddy.NormalizeProxyURL(rawManual)
		if err != nil {
			return "", fmt.Errorf("手动填写的代理地址无效: %w", err)
		}
		return norm, nil
	case "keep":
		if rawExisting != "" {
			if norm, err := workbuddy.NormalizeProxyURL(rawExisting); err == nil {
				return norm, nil
			}
		}
		if rawManual != "" {
			norm, err := workbuddy.NormalizeProxyURL(rawManual)
			if err != nil {
				return "", fmt.Errorf("默认代理地址无效: %w", err)
			}
			return norm, nil
		}
		if s != nil && s.proxyPool != nil {
			alloc, err := s.proxyPool.AllocateForPush(ctx, ProxyAllocationRequest{
				Platform:          domain.PlatformAntigravity,
				InFlightBindings:  inFlight,
				AllowEmptyOnError: true,
			})
			if err == nil && alloc.ProxyURL != "" {
				if inFlight != nil {
					inFlight[alloc.ProxyURL]++
				}
				return alloc.ProxyURL, nil
			}
		}
		return "", nil
	case "auto_pool", "pool":
		if s != nil && s.proxyPool != nil {
			alloc, err := s.proxyPool.AllocateForPush(ctx, ProxyAllocationRequest{
				Platform:          domain.PlatformAntigravity,
				RequestMode:       KeyProxyModePool,
				InFlightBindings:  inFlight,
				AllowEmptyOnError: true,
			})
			if err != nil {
				return "", err
			}
			if alloc.ProxyURL != "" && inFlight != nil {
				inFlight[alloc.ProxyURL]++
			}
			return alloc.ProxyURL, nil
		}
		return "", nil
	default:
		if rawManual != "" {
			norm, err := workbuddy.NormalizeProxyURL(rawManual)
			if err != nil {
				return "", fmt.Errorf("代理地址无效: %w", err)
			}
			return norm, nil
		}
		if rawExisting != "" {
			if norm, err := workbuddy.NormalizeProxyURL(rawExisting); err == nil {
				return norm, nil
			}
		}
		if s != nil && s.proxyPool != nil {
			alloc, err := s.proxyPool.AllocateForPush(ctx, ProxyAllocationRequest{
				Platform:          domain.PlatformAntigravity,
				InFlightBindings:  inFlight,
				AllowEmptyOnError: true,
			})
			if err == nil && alloc.ProxyURL != "" {
				if inFlight != nil {
					inFlight[alloc.ProxyURL]++
				}
				return alloc.ProxyURL, nil
			}
		}
		return "", nil
	}
}

// Client 返回底层 AntiGravity HTTP 客户端。
func (s *AntigravityService) Client() *antigravity.Client {
	return s.agClient
}

// Docker 返回底层 Docker 编排管理器。
func (s *AntigravityService) Docker() *antigravity.DockerManager {
	return s.docker
}

// DetectEnv 探测宿主机 Docker 可用性并返回默认镜像与数据根目录。
func (s *AntigravityService) DetectEnv(ctx context.Context) AntigravityEnvInfo {
	st := s.docker.DetectDocker(ctx)
	return AntigravityEnvInfo{
		DockerAvailable: st.Available,
		DockerVersion:   st.Version,
		ServerOS:        st.ServerOS,
		Error:           st.Error,
		DefaultImage:    s.defaultImage,
		DataRoot:        s.dataRoot,
	}
}

// ListNodes 返回全部容器/远程节点视图（本机节点自动附带轻量容器运行态快照与跨容器隔离诊断）。
func (s *AntigravityService) ListNodes(ctx context.Context) ([]ContainerNodeView, error) {
	rows, err := s.db.ContainerNode.Query().
		Order(ent.Asc(containernode.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询容器节点列表失败: %w", err)
	}
	out := make([]ContainerNodeView, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toNodeViewWithPeerSet(ctx, r, rows, true))
	}
	return out, nil
}

// GetNode 查询单个节点详情。
func (s *AntigravityService) GetNode(ctx context.Context, id int64) (*ContainerNodeView, error) {
	row, err := s.getNodeRow(ctx, id)
	if err != nil {
		return nil, err
	}
	v := s.toNodeView(ctx, row, true)
	return &v, nil
}

// CreateNode 创建本机 Docker 容器节点或外部远程节点。
func (s *AntigravityService) CreateNode(ctx context.Context, in CreateContainerNodeInput) (*ContainerNodeView, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("节点名称不能为空")
	}

	mode := domain.NormalizeContainerMode(in.Mode)
	if !domain.IsValidContainerMode(mode) {
		return nil, ErrInvalidContainerNodeMode
	}

	proxyURL := strings.TrimSpace(in.ProxyURL)
	if proxyURL != "" {
		normProxy, pErr := workbuddy.NormalizeProxyURL(proxyURL)
		if pErr != nil {
			return nil, fmt.Errorf("节点代理地址无效: %w", pErr)
		}
		proxyURL = normProxy
	}

	image := strings.TrimSpace(in.DockerImage)
	if image == "" {
		image = s.defaultImage
	}

	hostIP := strings.TrimSpace(in.HostIP)
	if hostIP == "" {
		hostIP = antigravity.DefaultHostIP
	}
	hostPort := in.HostPort
	if mode == domain.ContainerModeLocalDocker {
		if hostPort <= 0 || hostPort > 65535 {
			if allocPort, pErr := s.AllocateFreeHostPort(ctx); pErr == nil {
				hostPort = allocPort
			} else {
				hostPort = antigravity.DefaultHostPort
			}
		} else if hostPort == antigravity.DefaultHostPort && !antigravity.IsLocalTCPPortFree(hostIP, hostPort) {
			// 默认 8317 端口若已被宿主机外部进程或旧容器占用，自动顺延分配空闲端口，杜绝 docker run 端口冲突
			if allocPort, pErr := s.AllocateFreeHostPort(ctx); pErr == nil {
				hostPort = allocPort
			}
		}
	} else if hostPort <= 0 || hostPort > 65535 {
		hostPort = antigravity.DefaultHostPort
	}

	mgmtPlain := strings.TrimSpace(in.ManagementKey)
	apiKeyPlain := strings.TrimSpace(in.APIKey)
	if mode == domain.ContainerModeLocalDocker {
		if mgmtPlain == "" {
			mgmtPlain = generateNodeRandomSecret("agym-")
		}
		if apiKeyPlain == "" {
			apiKeyPlain = generateNodeRandomSecret("sk-agy-")
		}
	}

	mgmtEnc, err := s.encryptSecret(mgmtPlain)
	if err != nil {
		return nil, err
	}
	apiKeyEnc, err := s.encryptSecret(apiKeyPlain)
	if err != nil {
		return nil, err
	}

	extra := map[string]any{}
	for k, v := range in.Extra {
		extra[k] = v
	}
	strictIso := antigravity.ParseStrictIsolationFromExtra(extra)
	if in.StrictIsolation != nil {
		strictIso = *in.StrictIsolation
	}
	extra["strict_isolation"] = strictIso

	if err := s.validateUniqueProxyIfStrict(ctx, 0, proxyURL, strictIso); err != nil {
		return nil, err
	}

	fp, _ := antigravity.ExtractFingerprintFromExtra(extra, name)
	extra["fingerprint"] = fp

	initialStatus := "stopped"
	if mode == domain.ContainerModeRemoteAPI {
		initialStatus = "offline"
	}

	created, err := s.db.ContainerNode.Create().
		SetName(name).
		SetMode(mode).
		SetDockerImage(image).
		SetContainerName(strings.TrimSpace(in.ContainerName)).
		SetHostIP(hostIP).
		SetHostPort(hostPort).
		SetAuthDir(strings.TrimSpace(in.AuthDir)).
		SetManagementKeyEnc(mgmtEnc).
		SetAPIKeyEnc(apiKeyEnc).
		SetProxyURL(proxyURL).
		SetStatus(initialStatus).
		SetExtra(extra).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建容器节点失败: %w", err)
	}

	// 为 local_docker 自动补全基于节点 ID 的容器名与挂载目录并预生成 config.yaml。
	if mode == domain.ContainerModeLocalDocker {
		containerName := strings.TrimSpace(created.ContainerName)
		if containerName == "" {
			containerName = fmt.Sprintf("cmd2api-agy-%d", created.ID)
		}
		authDir := strings.TrimSpace(created.AuthDir)
		if authDir == "" {
			authDir = filepath.Join(s.dataRoot, fmt.Sprintf("node-%d", created.ID), "auths")
		}
		created, err = created.Update().
			SetContainerName(containerName).
			SetAuthDir(authDir).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("初始化本机节点路径失败: %w", err)
		}

		spec := s.nodeSpecFromRow(created, mgmtPlain, apiKeyPlain)
		if _, wErr := antigravity.WriteNodeConfigFile(spec); wErr != nil {
			s.logger.Warn("预生成节点 config.yaml 失败", "node_id", created.ID, "err", wErr)
		}

		if in.AutoStart {
			cid, startErr := s.docker.EnsureAndStartContainer(ctx, spec)
			if startErr != nil {
				errMsg := startErr.Error()
				created, _ = created.Update().
					SetStatus("error").
					SetLastError(errMsg).
					Save(ctx)
			} else {
				created, _ = created.Update().
					SetStatus("running").
					SetContainerID(cid).
					ClearLastError().
					Save(ctx)
			}
		}
	} else {
		// 远程模式创建时顺手做一次轻量探活，失败不阻断节点入库。
		spec := s.nodeSpecFromRow(created, mgmtPlain, apiKeyPlain)
		probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, _, authCount, probeErr := s.agClient.ProbeNode(probeCtx, spec)
		cancel()
		now := time.Now()
		if probeErr == nil {
			created, _ = created.Update().
				SetStatus("online").
				SetAuthFilesCount(authCount).
				SetLastSyncAt(now).
				ClearLastError().
				Save(ctx)
		} else if in.AutoStart {
			errMsg := probeErr.Error()
			created, _ = created.Update().
				SetStatus("offline").
				SetLastError(errMsg).
				Save(ctx)
		}
	}

	view := s.toNodeView(ctx, created, true)
	return &view, nil
}

// UpdateNode 局部更新节点参数（本机节点同步重写 config.yaml）。
func (s *AntigravityService) UpdateNode(ctx context.Context, id int64, in UpdateContainerNodeInput) (*ContainerNodeView, error) {
	row, err := s.getNodeRow(ctx, id)
	if err != nil {
		return nil, err
	}

	upd := row.Update()
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return nil, errors.New("节点名称不能为空")
		}
		upd = upd.SetName(n)
	}
	if in.Mode != nil {
		m := domain.NormalizeContainerMode(*in.Mode)
		if !domain.IsValidContainerMode(m) {
			return nil, ErrInvalidContainerNodeMode
		}
		upd = upd.SetMode(m)
	}
	if in.DockerImage != nil {
		img := strings.TrimSpace(*in.DockerImage)
		if img == "" {
			img = s.defaultImage
		}
		upd = upd.SetDockerImage(img)
	}
	if in.ContainerName != nil {
		upd = upd.SetContainerName(strings.TrimSpace(*in.ContainerName))
	}
	if in.HostIP != nil {
		ip := strings.TrimSpace(*in.HostIP)
		if ip == "" {
			ip = antigravity.DefaultHostIP
		}
		upd = upd.SetHostIP(ip)
	}
	if in.HostPort != nil && *in.HostPort > 0 && *in.HostPort <= 65535 {
		upd = upd.SetHostPort(*in.HostPort)
	}
	if in.AuthDir != nil {
		upd = upd.SetAuthDir(strings.TrimSpace(*in.AuthDir))
	}
	if in.ProxyURL != nil {
		p := preserveRedactedNodeProxy(strings.TrimSpace(*in.ProxyURL), row.ProxyURL)
		if p != "" {
			normP, pErr := workbuddy.NormalizeProxyURL(p)
			if pErr != nil {
				return nil, fmt.Errorf("节点代理地址无效: %w", pErr)
			}
			p = normP
		}
		upd = upd.SetProxyURL(p)
	}
	if in.ManagementKey != nil && strings.TrimSpace(*in.ManagementKey) != "" {
		enc, eErr := s.encryptSecret(strings.TrimSpace(*in.ManagementKey))
		if eErr != nil {
			return nil, eErr
		}
		upd = upd.SetManagementKeyEnc(enc)
	}
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		enc, eErr := s.encryptSecret(strings.TrimSpace(*in.APIKey))
		if eErr != nil {
			return nil, eErr
		}
		upd = upd.SetAPIKeyEnc(enc)
	}
	if in.Extra != nil || in.StrictIsolation != nil {
		merged := map[string]any{}
		for k, v := range row.Extra {
			merged[k] = v
		}
		for k, v := range in.Extra {
			merged[k] = v
		}
		if in.StrictIsolation != nil {
			merged["strict_isolation"] = *in.StrictIsolation
		}
		upd = upd.SetExtra(merged)
	}

	effectiveProxy := row.ProxyURL
	if in.ProxyURL != nil {
		effectiveProxy = preserveRedactedNodeProxy(strings.TrimSpace(*in.ProxyURL), row.ProxyURL)
		if effectiveProxy != "" {
			if np, pErr := workbuddy.NormalizeProxyURL(effectiveProxy); pErr == nil {
				effectiveProxy = np
			}
		}
	}
	strictIso := antigravity.ParseStrictIsolationFromExtra(row.Extra)
	if in.StrictIsolation != nil {
		strictIso = *in.StrictIsolation
	} else if in.Extra != nil {
		if _, has := in.Extra["strict_isolation"]; has {
			strictIso = antigravity.ParseStrictIsolationFromExtra(in.Extra)
		}
	}
	if err := s.validateUniqueProxyIfStrict(ctx, id, effectiveProxy, strictIso); err != nil {
		return nil, err
	}

	updated, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("更新容器节点失败: %w", err)
	}

	if updated.Mode == domain.ContainerModeLocalDocker && strings.TrimSpace(updated.AuthDir) != "" {
		spec := s.nodeSpec(updated)
		_, _ = antigravity.WriteNodeConfigFile(spec)
	}

	v := s.toNodeView(ctx, updated, true)
	return &v, nil
}

// DeleteNode 删除容器节点记录；若 removeContainer 为 true 且是本机容器则一并移除宿主机容器。
func (s *AntigravityService) DeleteNode(ctx context.Context, id int64, removeContainer bool) error {
	row, err := s.getNodeRow(ctx, id)
	if err != nil {
		return err
	}
	if removeContainer && row.Mode == domain.ContainerModeLocalDocker && strings.TrimSpace(row.ContainerName) != "" {
		if rmErr := s.docker.RemoveContainer(ctx, row.ContainerName); rmErr != nil {
			s.logger.Warn("删除节点时清理 Docker 容器失败", "node_id", id, "container", row.ContainerName, "err", rmErr)
		}
	}
	if err := s.db.ContainerNode.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("删除容器节点失败: %w", err)
	}
	return nil
}

// RunNodeAction 对节点执行运维动作：start | stop | restart | pull | check。
func (s *AntigravityService) RunNodeAction(ctx context.Context, id int64, action string) (*ContainerNodeActionOutput, error) {
	row, err := s.getNodeRow(ctx, id)
	if err != nil {
		return nil, err
	}
	spec := s.nodeSpec(row)
	act := strings.ToLower(strings.TrimSpace(action))

	switch act {
	case "start":
		if row.Mode != domain.ContainerModeLocalDocker {
			return nil, errors.New("远程节点不支持本机 Docker 启动操作，请使用连通性检测 (check)")
		}
		// 启动前自愈检查：若当前容器未运行但宿主机目标端口已被其它外部服务占用（例如旧 cli-proxy-api 占用 8317），自动迁移至空闲端口
		st, _ := s.docker.InspectContainer(ctx, spec.EffectiveContainerName())
		if !st.Running && !antigravity.IsLocalTCPPortFree(row.HostIP, row.HostPort) {
			if freePort, pErr := s.AllocateFreeHostPort(ctx); pErr == nil && freePort != row.HostPort {
				s.logger.Info("检测到宿主机端口已被外部进程占用，自动迁移容器端口", "node_id", row.ID, "old_port", row.HostPort, "new_port", freePort)
				if uRow, uErr := row.Update().SetHostPort(freePort).Save(ctx); uErr == nil {
					row = uRow
					spec = s.nodeSpec(row)
					s.syncBoundAccountsEndpoint(ctx, row, spec)
				}
			}
		}
		cid, sErr := s.docker.EnsureAndStartContainer(ctx, spec)
		if sErr != nil {
			errMsg := sErr.Error()
			row, _ = row.Update().SetStatus("error").SetLastError(errMsg).Save(ctx)
			v := s.toNodeView(ctx, row, false)
			return &ContainerNodeActionOutput{OK: false, Action: act, Error: &errMsg, Node: &v}, nil
		}
		row, _ = row.Update().SetStatus("running").SetContainerID(cid).ClearLastError().Save(ctx)
		v := s.toNodeView(ctx, row, true)
		return &ContainerNodeActionOutput{OK: true, Action: act, Message: "容器已启动", Node: &v}, nil

	case "stop":
		if row.Mode != domain.ContainerModeLocalDocker {
			return nil, errors.New("远程节点不支持本机 Docker 停止操作")
		}
		if sErr := s.docker.StopContainer(ctx, spec.EffectiveContainerName()); sErr != nil {
			errMsg := sErr.Error()
			row, _ = row.Update().SetStatus("error").SetLastError(errMsg).Save(ctx)
			v := s.toNodeView(ctx, row, false)
			return &ContainerNodeActionOutput{OK: false, Action: act, Error: &errMsg, Node: &v}, nil
		}
		row, _ = row.Update().SetStatus("stopped").ClearLastError().Save(ctx)
		v := s.toNodeView(ctx, row, true)
		return &ContainerNodeActionOutput{OK: true, Action: act, Message: "容器已停止", Node: &v}, nil

	case "restart":
		if row.Mode != domain.ContainerModeLocalDocker {
			return nil, errors.New("远程节点不支持本机 Docker 重启操作")
		}
		_, _ = antigravity.WriteNodeConfigFile(spec)
		st, _ := s.docker.InspectContainer(ctx, spec.EffectiveContainerName())
		var rErr error
		cid := row.ContainerID
		if !st.Exists {
			cid, rErr = s.docker.EnsureAndStartContainer(ctx, spec)
		} else {
			rErr = s.docker.RestartContainer(ctx, spec.EffectiveContainerName())
			if st.ContainerID != "" {
				cid = st.ContainerID
			}
		}
		if rErr != nil {
			errMsg := rErr.Error()
			row, _ = row.Update().SetStatus("error").SetLastError(errMsg).Save(ctx)
			v := s.toNodeView(ctx, row, false)
			return &ContainerNodeActionOutput{OK: false, Action: act, Error: &errMsg, Node: &v}, nil
		}
		row, _ = row.Update().SetStatus("running").SetContainerID(cid).ClearLastError().Save(ctx)
		v := s.toNodeView(ctx, row, true)
		return &ContainerNodeActionOutput{OK: true, Action: act, Message: "容器已重启", Node: &v}, nil

	case "pull":
		if row.Mode != domain.ContainerModeLocalDocker {
			return nil, errors.New("远程节点不支持本机镜像拉取操作")
		}
		if pErr := s.docker.PullImage(ctx, spec.EffectiveImage()); pErr != nil {
			errMsg := pErr.Error()
			row, _ = row.Update().SetLastError(errMsg).Save(ctx)
			v := s.toNodeView(ctx, row, true)
			return &ContainerNodeActionOutput{OK: false, Action: act, Error: &errMsg, Node: &v}, nil
		}
		row, _ = row.Update().ClearLastError().Save(ctx)
		v := s.toNodeView(ctx, row, true)
		return &ContainerNodeActionOutput{OK: true, Action: act, Message: "镜像拉取完成", Node: &v}, nil

	case "check":
		latency, modelsCount, authCount, cErr := s.agClient.ProbeNode(ctx, spec)
		latMs := int(latency.Milliseconds())
		now := time.Now()
		if cErr != nil {
			errMsg := cErr.Error()
			failStatus := "offline"
			if row.Mode == domain.ContainerModeLocalDocker {
				failStatus = "error"
			}
			row, _ = row.Update().
				SetStatus(failStatus).
				SetAuthFilesCount(authCount).
				SetLastError(errMsg).
				Save(ctx)
			v := s.toNodeView(ctx, row, true)
			v.LatencyMs = &latMs
			return &ContainerNodeActionOutput{
				OK:             false,
				Action:         act,
				Error:          &errMsg,
				LatencyMs:      latMs,
				ModelsCount:    modelsCount,
				AuthFilesCount: authCount,
				Node:           &v,
			}, nil
		}

		okStatus := "online"
		if row.Mode == domain.ContainerModeLocalDocker {
			okStatus = "running"
		}
		row, _ = row.Update().
			SetStatus(okStatus).
			SetAuthFilesCount(authCount).
			SetLastSyncAt(now).
			ClearLastError().
			Save(ctx)
		v := s.toNodeView(ctx, row, true)
		v.ModelsCount = modelsCount
		v.LatencyMs = &latMs
		return &ContainerNodeActionOutput{
			OK:             true,
			Action:         act,
			Message:        fmt.Sprintf("节点在线 (%d 模型 / %d 凭证)", modelsCount, authCount),
			LatencyMs:      latMs,
			ModelsCount:    modelsCount,
			AuthFilesCount: authCount,
			Node:           &v,
		}, nil

	default:
		return nil, ErrInvalidContainerNodeAction
	}
}

// GetNodeLogs 读取本机容器节点的最近日志。
func (s *AntigravityService) GetNodeLogs(ctx context.Context, id int64, tail int) (*ContainerNodeLogsOutput, error) {
	row, err := s.getNodeRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if tail <= 0 {
		tail = 200
	}
	spec := s.nodeSpec(row)
	if row.Mode != domain.ContainerModeLocalDocker {
		return &ContainerNodeLogsOutput{
			NodeID:        row.ID,
			ContainerName: row.Name,
			Tail:          tail,
			Logs:          "远程 API 节点不支持读取宿主机本地 Docker 容器日志。",
		}, nil
	}
	logs, lErr := s.docker.ContainerLogs(ctx, spec.EffectiveContainerName(), tail)
	if lErr != nil {
		return nil, lErr
	}
	return &ContainerNodeLogsOutput{
		NodeID:        row.ID,
		ContainerName: spec.EffectiveContainerName(),
		Tail:          tail,
		Logs:          logs,
	}, nil
}

// ListAuthFiles 列出节点下的 OAuth .json 登录文件，并自动匹配关联已同步的调度账号 ID。
// 对于本机 Docker 节点：若容器当前未处于运行态，直接读取隔离挂载目录，避免误连宿主机同端口外部进程触发 401。
func (s *AntigravityService) ListAuthFiles(ctx context.Context, nodeID int64) ([]AntigravityAuthFileView, error) {
	row, err := s.getNodeRow(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	spec := s.nodeSpec(row)
	var files []antigravity.AuthFileInfo
	if row.Mode == domain.ContainerModeLocalDocker && strings.TrimSpace(spec.AuthDir) != "" {
		st, _ := s.docker.InspectContainer(ctx, spec.EffectiveContainerName())
		if !st.Running {
			files, err = antigravity.ListLocalAuthFiles(spec.AuthDir)
			if err != nil {
				files = []antigravity.AuthFileInfo{}
				err = nil
			}
		} else {
			files, err = s.agClient.ListAuthFiles(ctx, spec)
			if err != nil {
				files, _ = antigravity.ListLocalAuthFiles(spec.AuthDir)
				err = nil
			}
		}
	} else {
		files, err = s.agClient.ListAuthFiles(ctx, spec)
	}
	if err != nil {
		return nil, err
	}

	if row.AuthFilesCount != len(files) {
		_, _ = row.Update().SetAuthFilesCount(len(files)).Save(ctx)
	}

	byAuthFile := s.loadNodeSyncedAccountsMap(ctx, nodeID)
	views := make([]AntigravityAuthFileView, 0, len(files))
	for _, f := range files {
		v := AntigravityAuthFileView{AuthFileInfo: f}
		if acc, ok := byAuthFile[strings.ToLower(f.Filename)]; ok {
			idCopy := acc.ID
			v.AccountID = &idCopy
			v.AccountStatus = acc.Status
		}
		views = append(views, v)
	}
	return views, nil
}

// UploadAuthFile 上传任意受支持格式凭证到节点（自动识别 Google ADC / Gemini CLI / CPA / 分隔符文本 / Base64 并转为 agy 原生格式，可选自动同步到调度账号池）。
func (s *AntigravityService) UploadAuthFile(
	ctx context.Context,
	nodeID int64,
	filename string,
	content []byte,
	autoSync bool,
	groupIDs []int64,
) (*UploadAuthFileOutput, error) {
	row, err := s.getNodeRow(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	spec := s.nodeSpec(row)

	uploadName := strings.TrimSpace(filename)
	if uploadName != "" && !strings.HasSuffix(strings.ToLower(uploadName), ".json") {
		ext := filepath.Ext(uploadName)
		stem := strings.TrimSuffix(uploadName, ext)
		if strings.TrimSpace(stem) != "" {
			uploadName = stem + ".json"
		} else {
			uploadName = ""
		}
	}

	uploadBytes := content
	var normalizedAcc *antigravity.ConvertedAGYAccount

	if cleanProbe, sErr := antigravity.SanitizeAuthFilename(firstNonEmptyStrSvc(uploadName, "auth.json")); sErr == nil {
		if parsedInfo, pErr := antigravity.ParseAuthFileJSON(cleanProbe, content); pErr == nil {
			// 已是合法 JSON 对象：若是 gcloud_adc / gemini_cli / cpa_export，补齐顶层 antigravity 元数据以便容器与账号池识别
			if norm, nErr := antigravity.NormalizeSingleCredential(filename, content, row.ProxyURL); nErr == nil && norm.Valid {
				normalizedAcc = &norm
				if parsedInfo.Email == "" || parsedInfo.AccountType == "authorized_user" ||
					norm.SourceFormat == antigravity.SourceFormatGCloudADC ||
					norm.SourceFormat == antigravity.SourceFormatGeminiCLI ||
					norm.SourceFormat == antigravity.SourceFormatCPAExport {
					uploadBytes = norm.CanonicalAuthJSONBytes()
				}
				if uploadName == "" {
					uploadName = norm.AuthFilename
				}
			}
		} else {
			// 非单一 JSON 对象（例如分隔符文本行、裸 1// refresh_token、CSV、Base64）：自动转换为标准 agy 凭证 JSON
			norm, nErr := antigravity.NormalizeSingleCredential(filename, content, row.ProxyURL)
			if nErr != nil {
				return nil, pErr
			}
			normalizedAcc = &norm
			uploadBytes = norm.CanonicalAuthJSONBytes()
			if uploadName == "" {
				uploadName = norm.AuthFilename
			}
		}
	}
	if uploadName == "" {
		uploadName = fmt.Sprintf("agy-%d.json", time.Now().Unix())
	}

	info, err := s.agClient.UploadAuthFile(ctx, spec, uploadName, uploadBytes)
	if err != nil {
		return nil, err
	}

	// 若节点具备本地隔离目录且转换出了有效 OAuth 令牌，确保完整写入 .gemini/oauth_creds.json 等伴生文件
	if normalizedAcc != nil && strings.TrimSpace(spec.AuthDir) != "" {
		_, _ = antigravity.WriteIsolatedAGYAuthFiles(spec.AuthDir, *normalizedAcc, info.Filename)
	}

	// 刷新节点 auth_files_count
	if allFiles, lErr := s.agClient.ListAuthFiles(ctx, spec); lErr == nil {
		_, _ = row.Update().SetAuthFilesCount(len(allFiles)).Save(ctx)
	}

	view := AntigravityAuthFileView{AuthFileInfo: info}
	var syncedAcc *ent.Account
	if autoSync {
		syncOut, sErr := s.SyncNodeAccounts(ctx, nodeID, groupIDs)
		if sErr != nil {
			return nil, sErr
		}
		for _, a := range syncOut.Accounts {
			if strings.EqualFold(extractExtraString(a.Extra, "auth_file"), info.Filename) {
				if normalizedAcc != nil {
					if enriched, eErr := s.enrichSyncedAccountBundle(ctx, a, *normalizedAcc, spec.AuthDir); eErr == nil && enriched != nil {
						a = enriched
					}
				}
				syncedAcc = a
				idCopy := a.ID
				view.AccountID = &idCopy
				view.AccountStatus = a.Status
				break
			}
		}
	}

	return &UploadAuthFileOutput{
		OK:      true,
		File:    view,
		Item:    view,
		Account: syncedAcc,
	}, nil
}

// DeleteAuthFile 删除节点下的指定 .json 登录凭证文件，并软删关联的调度账号。
func (s *AntigravityService) DeleteAuthFile(ctx context.Context, nodeID int64, filename string) error {
	row, err := s.getNodeRow(ctx, nodeID)
	if err != nil {
		return err
	}
	spec := s.nodeSpec(row)
	if err := s.agClient.DeleteAuthFile(ctx, spec, filename); err != nil {
		return err
	}

	if allFiles, lErr := s.agClient.ListAuthFiles(ctx, spec); lErr == nil {
		_, _ = row.Update().SetAuthFilesCount(len(allFiles)).Save(ctx)
	}

	cleanName, sErr := antigravity.SanitizeAuthFilename(filename)
	if sErr == nil {
		byAuthFile := s.loadNodeSyncedAccountsMap(ctx, nodeID)
		if acc, ok := byAuthFile[strings.ToLower(cleanName)]; ok {
			_, _ = acc.Update().SetDeletedAt(time.Now()).SetSchedulable(false).SetStatus(domain.StatusDisabled).Save(ctx)
		}
	}
	return nil
}

// RequestOAuthURL 向节点管理端请求在线 OAuth 授权地址。
func (s *AntigravityService) RequestOAuthURL(
	ctx context.Context,
	nodeID int64,
	provider, redirectURI string,
) (*antigravity.OAuthURLResult, error) {
	row, err := s.getNodeRow(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	spec := s.nodeSpec(row)
	res, err := s.agClient.RequestOAuthURL(ctx, spec, provider, redirectURI)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// SyncNodeAccounts 将容器节点（及其有效 .json 登录文件或节点推理端点）同步创建/更新到 accounts 表并绑定 antigravity 分组。
func (s *AntigravityService) SyncNodeAccounts(
	ctx context.Context,
	nodeID int64,
	groupIDs []int64,
) (*SyncNodeAccountsOutput, error) {
	return s.SyncNodeAccountsWithOptions(ctx, nodeID, SyncNodeAccountsOptions{
		GroupIDs: groupIDs,
	})
}

// SyncNodeAccountsWithOptions 支持附带并发、优先级、倍率与覆盖代理的完整节点账号池同步。
func (s *AntigravityService) SyncNodeAccountsWithOptions(
	ctx context.Context,
	nodeID int64,
	opts SyncNodeAccountsOptions,
) (*SyncNodeAccountsOutput, error) {
	row, err := s.getNodeRow(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	spec := s.nodeSpec(row)
	baseURL := antigravity.BaseURL(spec)

	targetGroupIDs, err := s.resolveOrEnsureAntigravityGroups(ctx, opts.GroupIDs)
	if err != nil {
		return nil, err
	}

	proxyURL := strings.TrimSpace(opts.ProxyURL)
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(row.ProxyURL)
	} else {
		normP, pErr := workbuddy.NormalizeProxyURL(proxyURL)
		if pErr != nil {
			return nil, fmt.Errorf("同步代理地址无效: %w", pErr)
		}
		proxyURL = normP
	}

	files, listErr := s.agClient.ListAuthFiles(ctx, spec)
	activeFiles := make([]antigravity.AuthFileInfo, 0, len(files))
	for _, f := range files {
		if !f.Disabled {
			activeFiles = append(activeFiles, f)
		}
	}

	// 若节点没有可枚举的 auth_file（例如纯推理端点），且节点本身已就绪，则生成一条代表该节点端点的调度账号。
	if len(activeFiles) == 0 {
		if listErr != nil && strings.TrimSpace(spec.APIKey) == "" {
			return nil, fmt.Errorf("读取节点登录凭证失败: %w", listErr)
		}
		activeFiles = append(activeFiles, antigravity.AuthFileInfo{
			Filename: "",
			Provider: domain.PlatformAntigravity,
		})
	}

	credsMap, err := s.buildAntigravityCredentials(spec, baseURL)
	if err != nil {
		return nil, err
	}

	existingByKey := s.loadNodeSyncedAccountsMap(ctx, nodeID)
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	defaultPriority := opts.Priority
	if defaultPriority <= 0 {
		defaultPriority = 50
	}
	rateMult := opts.RateMultiplier
	if rateMult <= 0 {
		rateMult = 1.0
	}

	out := &SyncNodeAccountsOutput{
		OK:         true,
		FilesCount: len(files),
		Accounts:   make([]*ent.Account, 0, len(activeFiles)),
	}
	if len(targetGroupIDs) > 0 {
		out.GroupID = targetGroupIDs[0]
	}

	for _, f := range activeFiles {
		matchKey := strings.ToLower(strings.TrimSpace(f.Filename))
		if matchKey == "" {
			matchKey = "__endpoint__"
		}

		accName := row.Name
		if f.Filename != "" {
			if f.Email != "" {
				accName = fmt.Sprintf("%s · %s", row.Name, f.Email)
			} else {
				accName = fmt.Sprintf("%s · %s", row.Name, strings.TrimSuffix(f.Filename, ".json"))
			}
		}
		if len(accName) > 100 {
			accName = accName[:100]
		}

		prio := defaultPriority
		if f.Priority > 0 {
			prio = f.Priority
		}

		extra := map[string]any{
			"node_id":     nodeID,
			"auth_file":   f.Filename,
			"base_url":    baseURL,
			"provider":    f.Provider,
			"project_id":  f.ProjectID,
			"fingerprint": spec.Fingerprint,
		}

		var savedAcc *ent.Account
		if existing, exists := existingByKey[matchKey]; exists {
			savedAcc, err = existing.Update().
				SetName(accName).
				SetPlatform(domain.PlatformAntigravity).
				SetType(domain.AccountTypeOAuthJSON).
				SetDomain(baseURL).
				SetEmail(strings.TrimSpace(f.Email)).
				SetProxyURL(proxyURL).
				SetQimei36(spec.Fingerprint.Qimei36).
				SetMachineID(spec.Fingerprint.MachineID).
				SetCredentials(credsMap).
				SetExtra(extra).
				SetStatus(domain.StatusActive).
				SetSchedulable(true).
				ClearErrorMessage().
				Save(ctx)
			if err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("更新账号 %s 失败: %v", accName, err))
				continue
			}
			out.Updated++
		} else {
			qimei, mid := spec.Fingerprint.Qimei36, spec.Fingerprint.MachineID
			if qimei == "" || mid == "" {
				qimei, mid = workbuddy.GenerateDevice()
			}
			savedAcc, err = s.db.Account.Create().
				SetName(accName).
				SetPlatform(domain.PlatformAntigravity).
				SetType(domain.AccountTypeOAuthJSON).
				SetDomain(baseURL).
				SetEmail(strings.TrimSpace(f.Email)).
				SetProxyURL(proxyURL).
				SetQimei36(qimei).
				SetMachineID(mid).
				SetCredentials(credsMap).
				SetExtra(extra).
				SetConcurrency(concurrency).
				SetPriority(prio).
				SetRateMultiplier(rateMult).
				SetStatus(domain.StatusActive).
				SetSchedulable(true).
				Save(ctx)
			if err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("创建账号 %s 失败: %v", accName, err))
				continue
			}
			out.Created++
		}

		if err := s.bindAccountGroups(ctx, savedAcc.ID, targetGroupIDs); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("绑定分组失败 (%s): %v", accName, err))
		}
		reloaded, rErr := s.db.Account.Query().
			Where(account.IDEQ(savedAcc.ID)).
			WithGroups().
			Only(ctx)
		if rErr == nil {
			savedAcc = reloaded
		}
		out.Accounts = append(out.Accounts, savedAcc)
	}

	out.Total = out.Created + out.Updated
	now := time.Now()
	row, _ = row.Update().
		SetAuthFilesCount(len(files)).
		SetLastSyncAt(now).
		Save(ctx)
	v := s.toNodeView(ctx, row, false)
	out.Node = &v
	return out, nil
}

func (s *AntigravityService) resolveOrEnsureAntigravityGroups(ctx context.Context, groupIDs []int64) ([]int64, error) {
	ids := uniqueIDs(groupIDs)
	if len(ids) > 0 {
		rows, err := s.db.Group.Query().
			Where(group.IDIn(ids...), group.DeletedAtIsNil()).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("查询目标分组失败: %w", err)
		}
		if len(rows) != len(ids) {
			return nil, errors.New("存在无效或已删除的分组 ID")
		}
		for _, g := range rows {
			if domain.NormalizePlatform(g.Platform) != domain.PlatformAntigravity {
				return nil, fmt.Errorf("%w（分组「%s」平台为 %s，需选择 antigravity 分组）", ErrPlatformMismatch, g.Name, g.Platform)
			}
		}
		return ids, nil
	}

	// 自动查找已有的 antigravity 分组（优先名为 "antigravity"）。
	existingGroups, err := s.db.Group.Query().
		Where(group.DeletedAtIsNil(), group.PlatformEQ(domain.PlatformAntigravity)).
		Order(ent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询默认 antigravity 分组失败: %w", err)
	}
	for _, g := range existingGroups {
		if strings.EqualFold(g.Name, domain.PlatformAntigravity) {
			return []int64{g.ID}, nil
		}
	}
	if len(existingGroups) > 0 {
		return []int64{existingGroups[0].ID}, nil
	}

	// 自动创建默认分组 "antigravity"（若存在同名已软删旧分组则先归档释放唯一索引）。
	groupName := domain.PlatformAntigravity
	s.archiveSoftDeletedGroupByName(ctx, groupName)
	nameTaken, _ := s.db.Group.Query().Where(group.NameEQ(groupName)).Exist(ctx)
	if nameTaken {
		groupName = fmt.Sprintf("antigravity-%d", time.Now().Unix()%10000)
	}

	createdGrp, err := s.db.Group.Create().
		SetName(groupName).
		SetDescription("默认 AntiGravity 容器池分组").
		SetPlatform(domain.PlatformAntigravity).
		SetPromptInjectMode(domain.PromptModeAppendGlobal).
		SetRateMultiplier(1.0).
		SetStatus(domain.StatusActive).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建默认 antigravity 分组失败: %w", err)
	}
	return []int64{createdGrp.ID}, nil
}

func (s *AntigravityService) bindAccountGroups(ctx context.Context, accountID int64, groupIDs []int64) error {
	for _, gid := range uniqueIDs(groupIDs) {
		exists, err := s.db.AccountGroup.Query().
			Where(accountgroup.AccountIDEQ(accountID), accountgroup.GroupIDEQ(gid)).
			Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := s.db.AccountGroup.Create().
				SetAccountID(accountID).
				SetGroupID(gid).
				Save(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *AntigravityService) buildAntigravityCredentials(spec antigravity.NodeSpec, baseURL string) (map[string]any, error) {
	creds := map[string]any{
		"base_url": baseURL,
	}
	apiKey := strings.TrimSpace(spec.APIKey)
	if apiKey != "" {
		if s.vault != nil {
			enc, err := s.vault.Encrypt(apiKey)
			if err != nil {
				return nil, fmt.Errorf("加密节点 APIKey 失败: %w", err)
			}
			creds["api_key_encrypted"] = enc
		} else {
			creds["api_key"] = apiKey
		}
	}
	mgmtKey := strings.TrimSpace(spec.ManagementKey)
	if mgmtKey != "" {
		if s.vault != nil {
			enc, err := s.vault.Encrypt(mgmtKey)
			if err != nil {
				return nil, fmt.Errorf("加密节点 ManagementKey 失败: %w", err)
			}
			creds["management_key_encrypted"] = enc
		} else {
			creds["management_key"] = mgmtKey
		}
	}
	return creds, nil
}

func (s *AntigravityService) loadNodeSyncedAccountsMap(ctx context.Context, nodeID int64) map[string]*ent.Account {
	rows, err := s.db.Account.Query().
		Where(account.DeletedAtIsNil(), account.PlatformEQ(domain.PlatformAntigravity)).
		All(ctx)
	if err != nil {
		return map[string]*ent.Account{}
	}
	out := make(map[string]*ent.Account)
	for _, r := range rows {
		if extractExtraInt64(r.Extra, "node_id") != nodeID {
			continue
		}
		af := strings.ToLower(strings.TrimSpace(extractExtraString(r.Extra, "auth_file")))
		if af == "" {
			af = "__endpoint__"
		}
		out[af] = r
	}
	return out
}

func (s *AntigravityService) getNodeRow(ctx context.Context, id int64) (*ent.ContainerNode, error) {
	row, err := s.db.ContainerNode.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrContainerNodeNotFound
		}
		return nil, fmt.Errorf("读取容器节点失败: %w", err)
	}
	return row, nil
}

func (s *AntigravityService) encryptSecret(plain string) (string, error) {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return "", nil
	}
	if s.vault == nil {
		return plain, nil
	}
	return s.vault.Encrypt(plain)
}

func (s *AntigravityService) decryptSecret(enc string) string {
	enc = strings.TrimSpace(enc)
	if enc == "" {
		return ""
	}
	if s.vault == nil {
		return enc
	}
	if plain, err := s.vault.Decrypt(enc); err == nil {
		return plain
	}
	return ""
}

func (s *AntigravityService) nodeSpec(row *ent.ContainerNode) antigravity.NodeSpec {
	return s.nodeSpecFromRow(row, s.decryptSecret(row.ManagementKeyEnc), s.decryptSecret(row.APIKeyEnc))
}

func (s *AntigravityService) nodeSpecFromRow(row *ent.ContainerNode, mgmtPlain, apiPlain string) antigravity.NodeSpec {
	fp, _ := antigravity.ExtractFingerprintFromExtra(row.Extra, fmt.Sprintf("node-%d-%s", row.ID, row.Name))
	strictIso := antigravity.ParseStrictIsolationFromExtra(row.Extra)
	return antigravity.NodeSpec{
		ID:              row.ID,
		Name:            row.Name,
		Mode:            row.Mode,
		DockerImage:     row.DockerImage,
		ContainerName:   row.ContainerName,
		HostIP:          row.HostIP,
		HostPort:        row.HostPort,
		AuthDir:         row.AuthDir,
		ManagementKey:   mgmtPlain,
		APIKey:          apiPlain,
		ProxyURL:        row.ProxyURL,
		StrictIsolation: strictIso,
		Fingerprint:     fp,
	}
}

func (s *AntigravityService) toNodeView(ctx context.Context, row *ent.ContainerNode, inspectRuntime bool) ContainerNodeView {
	peers, err := s.db.ContainerNode.Query().All(ctx)
	if err != nil {
		peers = []*ent.ContainerNode{row}
	}
	return s.toNodeViewWithPeerSet(ctx, row, peers, inspectRuntime)
}

func (s *AntigravityService) toNodeViewWithPeerSet(ctx context.Context, row *ent.ContainerNode, peers []*ent.ContainerNode, inspectRuntime bool) ContainerNodeView {
	mgmtPlain := s.decryptSecret(row.ManagementKeyEnc)
	apiPlain := s.decryptSecret(row.APIKeyEnc)
	spec := s.nodeSpecFromRow(row, mgmtPlain, apiPlain)

	status := row.Status
	cid := row.ContainerID
	var runtimePtr *antigravity.ContainerStatus

	if inspectRuntime && row.Mode == domain.ContainerModeLocalDocker && strings.TrimSpace(row.ContainerName) != "" {
		if st, err := s.docker.InspectContainer(ctx, row.ContainerName); err == nil {
			runtimePtr = &st
			if st.ContainerID != "" {
				cid = st.ContainerID
			}
			if st.Running {
				status = "running"
			} else if !st.Exists && status == "running" {
				status = "stopped"
			}
		}
	}

	extra := map[string]any{}
	for k, v := range row.Extra {
		extra[k] = v
	}
	extra["fingerprint"] = spec.Fingerprint
	extra["strict_isolation"] = spec.StrictIsolation

	isoStatus, isoWarnings := evaluateNodeIsolation(row, peers, spec.Fingerprint)

	return ContainerNodeView{
		ID:                  row.ID,
		Name:                row.Name,
		Mode:                row.Mode,
		DockerImage:         row.DockerImage,
		ContainerName:       row.ContainerName,
		ContainerID:         cid,
		HostIP:              row.HostIP,
		HostPort:            row.HostPort,
		BaseURL:             antigravity.BaseURL(spec),
		AuthDir:             row.AuthDir,
		HasManagementKey:    mgmtPlain != "",
		MaskedManagementKey: crypto.MaskKey(mgmtPlain),
		HasAPIKey:           apiPlain != "",
		MaskedAPIKey:        crypto.MaskKey(apiPlain),
		ProxyURL:            workbuddy.RedactProxyURL(row.ProxyURL),
		StrictIsolation:     spec.StrictIsolation,
		Fingerprint:         spec.Fingerprint,
		IsolationStatus:     isoStatus,
		IsolationWarnings:   isoWarnings,
		LastExitIP:          extractExtraString(row.Extra, "last_exit_ip"),
		LastIPCheckedAt:     extractExtraString(row.Extra, "last_ip_checked_at"),
		Status:              status,
		AuthFilesCount:      row.AuthFilesCount,
		Runtime:             runtimePtr,
		LastSyncAt:          row.LastSyncAt,
		LastError:           row.LastError,
		Extra:               extra,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

func (s *AntigravityService) validateUniqueProxyIfStrict(ctx context.Context, excludeNodeID int64, proxyURL string, strictIso bool) error {
	p := strings.TrimSpace(proxyURL)
	if !strictIso || p == "" {
		return nil
	}
	rows, err := s.db.ContainerNode.Query().All(ctx)
	if err != nil {
		return nil
	}
	for _, other := range rows {
		if excludeNodeID > 0 && other.ID == excludeNodeID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(other.ProxyURL), p) {
			return fmt.Errorf("严格 1容器:1IP 隔离已启用：代理 %s 已被节点「%s」(#%d) 占用，请分配独立出口 IP 或关闭 strict_isolation",
				workbuddy.RedactProxyURL(p), other.Name, other.ID)
		}
	}
	return nil
}

func evaluateNodeIsolation(row *ent.ContainerNode, peers []*ent.ContainerNode, fp antigravity.NodeFingerprint) (string, []string) {
	var warnings []string
	myProxy := strings.TrimSpace(row.ProxyURL)
	myExitIP := strings.TrimSpace(extractExtraString(row.Extra, "last_exit_ip"))

	if myProxy == "" {
		warnings = append(warnings, "未配置独立 ProxyURL，当前容器将直连宿主机出口网络")
	}

	var dupProxyNames []string
	var dupIPNames []string
	var dupFPNames []string
	for _, other := range peers {
		if other.ID == row.ID {
			continue
		}
		if myProxy != "" && strings.EqualFold(strings.TrimSpace(other.ProxyURL), myProxy) {
			dupProxyNames = append(dupProxyNames, fmt.Sprintf("%s(#%d)", other.Name, other.ID))
		}
		otherIP := strings.TrimSpace(extractExtraString(other.Extra, "last_exit_ip"))
		if myExitIP != "" && otherIP != "" && myExitIP == otherIP {
			dupIPNames = append(dupIPNames, fmt.Sprintf("%s(#%d)", other.Name, other.ID))
		}
		otherFP, created := antigravity.ExtractFingerprintFromExtra(other.Extra, "")
		if !created && fp.FingerprintID != "" && otherFP.FingerprintID == fp.FingerprintID {
			dupFPNames = append(dupFPNames, fmt.Sprintf("%s(#%d)", other.Name, other.ID))
		}
	}

	if len(dupProxyNames) > 0 {
		warnings = append(warnings, "ProxyURL 与其他容器节点重复: "+strings.Join(dupProxyNames, ", "))
	}
	if len(dupIPNames) > 0 {
		warnings = append(warnings, fmt.Sprintf("探测到的出口 IP (%s) 与节点冲突: %s", myExitIP, strings.Join(dupIPNames, ", ")))
	}
	if len(dupFPNames) > 0 {
		warnings = append(warnings, "设备指纹与节点冲突，建议立即轮换指纹: "+strings.Join(dupFPNames, ", "))
	}

	switch {
	case len(dupProxyNames) > 0 || len(dupIPNames) > 0:
		return antigravity.IsolationStatusDuplicateProxy, warnings
	case myProxy == "":
		return antigravity.IsolationStatusNoProxy, warnings
	default:
		return antigravity.IsolationStatusIsolated, warnings
	}
}

// RotateNodeFingerprint 为指定容器节点重新生成一套全新隔离设备指纹（FingerprintID/Qimei36/MachineID/Hostname/MAC/ClientID），
// 并同步更新该节点关联的所有调度账号与本地 config.yaml。
func (s *AntigravityService) RotateNodeFingerprint(ctx context.Context, nodeID int64) (*ContainerNodeView, error) {
	row, err := s.getNodeRow(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	newFP := antigravity.GenerateNodeFingerprint(fmt.Sprintf("node-%d-%d", row.ID, time.Now().UnixNano()))
	extra := map[string]any{}
	for k, v := range row.Extra {
		extra[k] = v
	}
	extra["fingerprint"] = newFP
	if _, has := extra["strict_isolation"]; !has {
		extra["strict_isolation"] = true
	}

	updated, err := row.Update().SetExtra(extra).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("保存轮换指纹失败: %w", err)
	}

	// 同步刷新绑定该节点的所有调度账号的设备指纹字段
	byAuthFile := s.loadNodeSyncedAccountsMap(ctx, nodeID)
	for _, acc := range byAuthFile {
		accExtra := map[string]any{}
		for k, v := range acc.Extra {
			accExtra[k] = v
		}
		accExtra["fingerprint"] = newFP
		_, _ = acc.Update().
			SetQimei36(newFP.Qimei36).
			SetMachineID(newFP.MachineID).
			SetExtra(accExtra).
			Save(ctx)
	}

	if updated.Mode == domain.ContainerModeLocalDocker && strings.TrimSpace(updated.AuthDir) != "" {
		spec := s.nodeSpec(updated)
		_, _ = antigravity.WriteNodeConfigFile(spec)
	}

	v := s.toNodeView(ctx, updated, true)
	return &v, nil
}

// CheckNodeExitIP 通过节点专属 ProxyURL 探测实际出口 IP，并核验是否与其他容器节点碰撞。
func (s *AntigravityService) CheckNodeExitIP(ctx context.Context, nodeID int64) (*NodeExitIPOutput, error) {
	row, err := s.getNodeRow(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	probe := s.agClient.ProbeExitIP(ctx, row.ProxyURL)

	extra := map[string]any{}
	for k, v := range row.Extra {
		extra[k] = v
	}
	extra["last_ip_checked_at"] = time.Now().UTC().Format(time.RFC3339)
	if probe.OK && probe.ExitIP != "" {
		extra["last_exit_ip"] = probe.ExitIP
	}
	if updated, uErr := row.Update().SetExtra(extra).Save(ctx); uErr == nil {
		row = updated
	}

	peers, _ := s.db.ContainerNode.Query().All(ctx)
	var sharedWith []string
	if probe.OK && probe.ExitIP != "" {
		for _, other := range peers {
			if other.ID == row.ID {
				continue
			}
			otherIP := strings.TrimSpace(extractExtraString(other.Extra, "last_exit_ip"))
			if otherIP == probe.ExitIP {
				sharedWith = append(sharedWith, fmt.Sprintf("%s(#%d)", other.Name, other.ID))
			}
		}
	}

	v := s.toNodeViewWithPeerSet(ctx, row, peers, false)
	return &NodeExitIPOutput{
		ExitIPProbeResult: probe,
		NodeID:            row.ID,
		NodeName:          row.Name,
		IsolationStatus:   v.IsolationStatus,
		SharedWithNodes:   sharedWith,
		Node:              &v,
	}, nil
}

func generateNodeRandomSecret(prefix string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(buf)
}

// ProvisionAccountContainerInput 描述为单个 AntiGravity 账号自动分配独立 agy 容器的入参。
type ProvisionAccountContainerInput struct {
	AccountID       int64
	ProxyURL        string
	DockerImage     string
	StrictIsolation *bool
	AutoStart       bool
	AuthFilename    string
	AuthJSON        []byte
}

// AllocateFreeHostPort 扫描全部已登记容器节点与短期预占表，并发安全地分配未占用的宿主机映射端口（从 18317 起步）。
func (s *AntigravityService) AllocateFreeHostPort(ctx context.Context) (int, error) {
	s.portMu.Lock()
	defer s.portMu.Unlock()

	rows, err := s.db.ContainerNode.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("查询容器端口占用失败: %w", err)
	}
	now := time.Now()
	if s.reservedPorts == nil {
		s.reservedPorts = make(map[int]time.Time)
	}
	for p, exp := range s.reservedPorts {
		if now.After(exp) {
			delete(s.reservedPorts, p)
		}
	}

	used := make(map[int]struct{}, len(rows)+len(s.reservedPorts))
	for _, r := range rows {
		if r.HostPort > 0 {
			used[r.HostPort] = struct{}{}
		}
	}
	for p := range s.reservedPorts {
		used[p] = struct{}{}
	}
	for port := 18317; port < 28317; port++ {
		if _, taken := used[port]; !taken && antigravity.IsLocalTCPPortFree("127.0.0.1", port) {
			s.reservedPorts[port] = now.Add(30 * time.Second)
			return port, nil
		}
	}
	return 0, errors.New("没有可用的 AntiGravity 容器端口（18317-28316 已满）")
}

// NodeHasBoundAccount 检查某个容器节点是否已绑定了独立账号（严格 1 账号 1 agy 容器模式防混用）。
func (s *AntigravityService) NodeHasBoundAccount(ctx context.Context, nodeID int64) bool {
	if nodeID <= 0 {
		return false
	}
	row, err := s.getNodeRow(ctx, nodeID)
	if err == nil && row != nil && extractExtraInt64(row.Extra, "bound_account_id") > 0 {
		return true
	}
	synced := s.loadNodeSyncedAccountsMap(ctx, nodeID)
	return len(synced) > 0
}

// ProvisionAccountContainer 为单个 AntiGravity 账号自动分配专属的独立 agy CLI 容器（1 账号 : 1 容器 : 1 IP : 1 指纹）：
//   - 不依赖宿主机本地安装 agy，在空服务器上自动创建/启动独立 Docker 容器节点（默认 cmdwb2api-agy-cli:latest）
//   - 自动分配未占用的本地端口、独立挂载目录（同时映射到 /app/auths 与 /root/.gemini）与全新隔离设备指纹
//   - 自动回写账号的 base_url / domain / api_key / management_key / node_id / fingerprint 完成 1:1 绑定
func (s *AntigravityService) ProvisionAccountContainer(ctx context.Context, in ProvisionAccountContainerInput) (*ContainerNodeView, *ent.Account, error) {
	if in.AccountID <= 0 {
		return nil, nil, errors.New("account_id 不能为空")
	}
	acc, err := s.db.Account.Query().
		Where(account.IDEQ(in.AccountID), account.DeletedAtIsNil()).
		WithGroups().
		Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("查询 AntiGravity 账号 #%d 失败: %w", in.AccountID, err)
	}
	if domain.NormalizePlatform(acc.Platform) != domain.PlatformAntigravity {
		return nil, nil, fmt.Errorf("账号 #%d 平台为 %s，仅 antigravity 账号支持自动分配独立 agy 容器", acc.ID, acc.Platform)
	}

	// 若账号已绑定专属容器节点，则直接复用（如携带最新凭证/新代理则同步刷新隔离目录与容器配置，必要时自动启动）
	if existingNodeID := extractExtraInt64(acc.Extra, "node_id"); existingNodeID > 0 {
		if row, gErr := s.getNodeRow(ctx, existingNodeID); gErr == nil && row != nil {
			boundID := extractExtraInt64(row.Extra, "bound_account_id")
			if boundID == 0 || boundID == acc.ID {
				nextProxy := strings.TrimSpace(in.ProxyURL)
				if nextProxy != "" && nextProxy != strings.TrimSpace(row.ProxyURL) {
					strictIso := true
					if _, uErr := s.UpdateNode(ctx, row.ID, UpdateContainerNodeInput{
						ProxyURL:        &nextProxy,
						StrictIsolation: &strictIso,
					}); uErr == nil {
						row, _ = s.getNodeRow(ctx, row.ID)
					}
				}
				if len(in.AuthJSON) > 0 && strings.TrimSpace(row.AuthDir) != "" {
					fname := strings.TrimSpace(in.AuthFilename)
					if fname == "" {
						fname = fmt.Sprintf("account-%d.json", acc.ID)
					}
					if normAcc, nErr := antigravity.NormalizeSingleCredential(fname, in.AuthJSON, row.ProxyURL); nErr == nil && normAcc.Valid {
						_, _ = antigravity.WriteIsolatedAGYAuthFiles(row.AuthDir, normAcc, fname)
					} else {
						_, _ = antigravity.SaveLocalAuthFile(row.AuthDir, fname, in.AuthJSON)
					}
					if files, lErr := antigravity.ListLocalAuthFiles(row.AuthDir); lErr == nil {
						row, _ = row.Update().SetAuthFilesCount(len(files)).Save(ctx)
					}
				}
				if in.AutoStart && row.Mode == domain.ContainerModeLocalDocker && row.Status != "running" {
					if env := s.DetectEnv(ctx); env.DockerAvailable {
						_, _ = s.RunNodeAction(ctx, row.ID, "start")
						row, _ = s.getNodeRow(ctx, row.ID)
					}
				}
				v := s.toNodeView(ctx, row, true)
				return &v, acc, nil
			}
		}
	}

	freePort, err := s.AllocateFreeHostPort(ctx)
	if err != nil {
		return nil, nil, err
	}

	proxyURL := strings.TrimSpace(in.ProxyURL)
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(acc.ProxyURL)
	}
	strictIso := true
	if in.StrictIsolation != nil {
		strictIso = *in.StrictIsolation
	} else if proxyURL == "" {
		// 未提供独立代理时仍自动生成独立容器与唯一指纹（避免阻断零配置空服开箱），同时记录隔离告警
		strictIso = false
	}

	autoStart := in.AutoStart
	if autoStart {
		// 仅在宿主机 Docker 守护进程可用时立即拉起容器；无 Docker 守护进程时仍完成节点/目录/配置预分配
		if env := s.DetectEnv(ctx); !env.DockerAvailable {
			autoStart = false
		}
	}

	nodeName := fmt.Sprintf("agy-acc-%d-%s", acc.ID, antigravity.SanitizeContainerName(acc.Name))
	if len(nodeName) > 64 {
		nodeName = nodeName[:64]
	}
	containerName := fmt.Sprintf("cmd2api-agy-acc-%d", acc.ID)
	authDir := filepath.Join(s.dataRoot, fmt.Sprintf("account-%d", acc.ID), "auths")

	// 在启动 Docker 容器之前先把真实 agy CLI 隔离文件写入宿主机 authDir，
	// 防止非 root 服务进程下 Docker 守护进程先行创建 root 属主目录导致权限拒绝，同时确保容器 t=0 即刻可见凭证。
	var savedAuthFilename string
	if len(in.AuthJSON) > 0 {
		fname := strings.TrimSpace(in.AuthFilename)
		if fname == "" {
			fname = fmt.Sprintf("account-%d.json", acc.ID)
		}
		if normAcc, nErr := antigravity.NormalizeSingleCredential(fname, in.AuthJSON, proxyURL); nErr == nil && normAcc.Valid {
			_, _ = antigravity.WriteIsolatedAGYAuthFiles(authDir, normAcc, fname)
			if cleanName, cErr := antigravity.SanitizeAuthFilename(fname); cErr == nil {
				savedAuthFilename = cleanName
			} else {
				savedAuthFilename = normAcc.AuthFilename
			}
		} else if savedInfo, sErr := antigravity.SaveLocalAuthFile(authDir, fname, in.AuthJSON); sErr == nil {
			savedAuthFilename = savedInfo.Filename
		}
	} else if synth := s.synthesizeAGYAccountFromEnt(acc, proxyURL); synth.Valid {
		fname := fmt.Sprintf("account-%d.json", acc.ID)
		_, _ = antigravity.WriteIsolatedAGYAuthFiles(authDir, synth, fname)
		savedAuthFilename = fname
	}

	nodeView, err := s.CreateNode(ctx, CreateContainerNodeInput{
		Name:            nodeName,
		Mode:            domain.ContainerModeLocalDocker,
		DockerImage:     strings.TrimSpace(in.DockerImage),
		ContainerName:   containerName,
		HostIP:          antigravity.DefaultHostIP,
		HostPort:        freePort,
		AuthDir:         authDir,
		ProxyURL:        proxyURL,
		StrictIsolation: &strictIso,
		AutoStart:       autoStart,
		Extra: map[string]any{
			"single_account":   true,
			"bound_account_id": acc.ID,
			"bound_account":    acc.Name,
			"auto_provisioned": true,
		},
	})
	if err != nil {
		return nil, nil, err
	}

	row, err := s.getNodeRow(ctx, nodeView.ID)
	if err != nil {
		return nodeView, acc, nil
	}
	if savedAuthFilename != "" {
		if files, lErr := antigravity.ListLocalAuthFiles(nodeView.AuthDir); lErr == nil {
			row, _ = row.Update().SetAuthFilesCount(len(files)).Save(ctx)
			nodeView.AuthFilesCount = len(files)
		}
	}
	spec := s.nodeSpec(row)
	baseURL := antigravity.BaseURL(spec)

	creds, err := s.buildAntigravityCredentialsWithBase(spec, baseURL, acc.Credentials)
	if err != nil {
		return nil, nil, err
	}
	extra := map[string]any{}
	for k, v := range acc.Extra {
		extra[k] = v
	}
	extra["node_id"] = nodeView.ID
	extra["node_name"] = nodeView.Name
	extra["container_name"] = nodeView.ContainerName
	extra["auth_dir"] = nodeView.AuthDir
	if savedAuthFilename != "" {
		extra["auth_file"] = savedAuthFilename
	}
	extra["dedicated_container"] = true
	extra["single_account"] = true
	extra["base_url"] = baseURL
	extra["fingerprint"] = nodeView.Fingerprint

	updatedAcc, err := acc.Update().
		SetDomain(baseURL).
		SetCredentials(creds).
		SetProxyURL(proxyURL).
		SetQimei36(nodeView.Fingerprint.Qimei36).
		SetMachineID(nodeView.Fingerprint.MachineID).
		SetExtra(extra).
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("绑定账号 #%d 至专属容器 #%d 失败: %w", acc.ID, nodeView.ID, err)
	}

	s.logger.Info("已为 AntiGravity 账号自动分配独立 agy 容器",
		"account_id", acc.ID, "node_id", nodeView.ID,
		"container_name", nodeView.ContainerName, "base_url", baseURL,
		"fingerprint", nodeView.Fingerprint.FingerprintID)

	return nodeView, updatedAcc, nil
}

func extractExtraString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func extractExtraInt64(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// ConvertAccountsInput 描述全格式凭证解析与可选一键入池的入参。
type ConvertAccountsInput struct {
	Files         []antigravity.AdapterInputFile
	AutoImport    bool
	NodeID        int64
	GroupIDs      []int64
	IPMode        string // "auto_pool" | "manual" | "keep" | "direct"
	ProxyURL      string
	AutoProvision *bool
	Deduplicate   *bool
}

// ConvertImportResult 描述全格式转换后自动导入 AntiGravity 账号池的执行结果。
type ConvertImportResult struct {
	Imported int            `json:"imported"`
	Created  int            `json:"created"`
	Updated  int            `json:"updated"`
	Failed   int            `json:"failed"`
	NodeID   int64          `json:"node_id,omitempty"`
	GroupIDs []int64        `json:"group_ids,omitempty"`
	Accounts []*ent.Account `json:"accounts"`
	Errors   []string       `json:"errors,omitempty"`
}

// ConvertAccountsOutput 是 ConvertAndImportAccounts 的聚合输出。
type ConvertAccountsOutput struct {
	*antigravity.ConvertBatchResult
	ImportResult *ConvertImportResult `json:"import_result,omitempty"`
}

// ConvertAndImportAccounts 自动嗅探并转换任意格式账号包为真实 `agy` CLI 规范格式，支持一键导入指定容器节点或独立账号池。
func (s *AntigravityService) ConvertAndImportAccounts(ctx context.Context, in ConvertAccountsInput) (*ConvertAccountsOutput, error) {
	batch, err := antigravity.ConvertCredentialsToAGY(in.Files, antigravity.ConvertOptions{
		DefaultProxy: in.ProxyURL,
		Deduplicate:  in.Deduplicate,
	})
	if err != nil {
		return nil, err
	}

	out := &ConvertAccountsOutput{
		ConvertBatchResult: batch,
	}
	if !in.AutoImport || batch.ValidCount == 0 {
		return out, nil
	}

	targetGroups, err := s.resolveOrEnsureAntigravityGroups(ctx, in.GroupIDs)
	if err != nil {
		return nil, err
	}

	imp := &ConvertImportResult{
		NodeID:   in.NodeID,
		GroupIDs: targetGroups,
		Accounts: []*ent.Account{},
	}

	// 分支 A：导入到显式指定的容器节点并同步进调度池
	if in.NodeID > 0 {
		row, nErr := s.getNodeRow(ctx, in.NodeID)
		if nErr != nil {
			return nil, nErr
		}
		spec := s.nodeSpec(row)
		uploadedByFile := make(map[string]antigravity.ConvertedAGYAccount)
		for _, acc := range batch.Accounts {
			if !acc.Valid {
				continue
			}
			upOut, uErr := s.UploadAuthFile(ctx, in.NodeID, acc.AuthFilename, acc.CanonicalAuthJSONBytes(), false, nil)
			if uErr != nil {
				imp.Failed++
				imp.Errors = append(imp.Errors, fmt.Sprintf("%s: %v", acc.Name, uErr))
				continue
			}
			uploadedByFile[strings.ToLower(upOut.File.Filename)] = acc
		}
		if len(uploadedByFile) > 0 {
			syncOut, sErr := s.SyncNodeAccountsWithOptions(ctx, in.NodeID, SyncNodeAccountsOptions{
				GroupIDs: targetGroups,
				ProxyURL: in.ProxyURL,
			})
			if sErr != nil {
				imp.Errors = append(imp.Errors, sErr.Error())
			} else {
				imp.Created = syncOut.Created
				imp.Updated = syncOut.Updated
				imp.Imported = syncOut.Created + syncOut.Updated
				for _, savedAcc := range syncOut.Accounts {
					af := strings.ToLower(extractExtraString(savedAcc.Extra, "auth_file"))
					if srcConv, ok := uploadedByFile[af]; ok {
						if enriched, eErr := s.enrichSyncedAccountBundle(ctx, savedAcc, srcConv, spec.AuthDir); eErr == nil && enriched != nil {
							savedAcc = enriched
						}
					}
					imp.Accounts = append(imp.Accounts, savedAcc)
				}
				imp.Errors = append(imp.Errors, syncOut.Errors...)
			}
		}
		out.ImportResult = imp
		return out, nil
	}

	// 分支 B：直接入池（支持幂等 Upsert + AuthBundle 加密存储 + 自动分配 1:1 专属隔离目录与容器）
	autoProvision := true
	if in.AutoProvision != nil {
		autoProvision = *in.AutoProvision
	}

	existingRows, _ := s.db.Account.Query().
		Where(account.DeletedAtIsNil(), account.PlatformEQ(domain.PlatformAntigravity)).
		WithGroups().
		All(ctx)

	inFlightProxies := make(map[string]int)
	for _, conv := range batch.Accounts {
		if !conv.Valid {
			continue
		}
		// 按 ip_mode 为每个导入账号分配独立 IP（支持 1 账号 1 IP 池轮询、手动填写、保留原代理或直连）
		resolvedProxy, pErr := s.ResolveAccountProxy(ctx, in.IPMode, in.ProxyURL, conv.RawProxy, inFlightProxies)
		if pErr != nil {
			imp.Failed++
			imp.Errors = append(imp.Errors, fmt.Sprintf("%s: %v", conv.Name, pErr))
			continue
		}
		conv.RawProxy = resolvedProxy
		conv.Proxy = workbuddy.RedactProxyURL(resolvedProxy)

		savedAcc, isCreated, saveErr := s.upsertConvertedAccountToPool(ctx, conv, existingRows, targetGroups, autoProvision)
		if saveErr != nil {
			imp.Failed++
			imp.Errors = append(imp.Errors, fmt.Sprintf("%s: %v", conv.Name, saveErr))
			continue
		}
		if isCreated {
			imp.Created++
			existingRows = append(existingRows, savedAcc)
		} else {
			imp.Updated++
		}
		imp.Imported++
		imp.Accounts = append(imp.Accounts, savedAcc)
	}

	out.ImportResult = imp
	return out, nil
}

// UpsertSingleAGYAccountWithContainer 将单条标准化 AGY 凭证写入账号池并自动分配/启动 1:1 专属独立容器（供 OAuth 回调闭环与直接导入复用）。
func (s *AntigravityService) UpsertSingleAGYAccountWithContainer(
	ctx context.Context,
	conv antigravity.ConvertedAGYAccount,
	groupIDs []int64,
	autoProvision bool,
) (*ent.Account, *ContainerNodeView, error) {
	targetGroups, err := s.resolveOrEnsureAntigravityGroups(ctx, groupIDs)
	if err != nil {
		return nil, nil, err
	}
	existingRows, _ := s.db.Account.Query().
		Where(account.DeletedAtIsNil(), account.PlatformEQ(domain.PlatformAntigravity)).
		WithGroups().
		All(ctx)
	savedAcc, _, err := s.upsertConvertedAccountToPool(ctx, conv, existingRows, targetGroups, autoProvision)
	if err != nil {
		return nil, nil, err
	}
	var nodeView *ContainerNodeView
	if savedAcc != nil {
		if nid := extractExtraInt64(savedAcc.Extra, "node_id"); nid > 0 {
			nodeView, _ = s.GetNode(ctx, nid)
		}
	}
	return savedAcc, nodeView, nil
}

// SwapAccountIPAndRestartContainer 为单个 AntiGravity 账号重新分配代理池 IP 或设置指定代理 IP，并自动同步重启其 1:1 专属容器。
func (s *AntigravityService) SwapAccountIPAndRestartContainer(
	ctx context.Context,
	accountID int64,
	ipMode string,
	manualProxy string,
) (*ent.Account, *ContainerNodeView, error) {
	acc, err := s.db.Account.Query().
		Where(account.IDEQ(accountID), account.DeletedAtIsNil()).
		WithGroups().
		Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("查询账号 #%d 失败: %w", accountID, err)
	}
	if strings.TrimSpace(ipMode) == "" {
		if strings.TrimSpace(manualProxy) != "" {
			ipMode = "manual"
		} else {
			ipMode = "auto_pool"
		}
	}
	nextProxy, err := s.ResolveAccountProxy(ctx, ipMode, manualProxy, "", nil)
	if err != nil {
		return nil, nil, err
	}
	acc, err = acc.Update().SetProxyURL(nextProxy).Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("更新账号代理失败: %w", err)
	}

	nodeID := extractExtraInt64(acc.Extra, "node_id")
	if nodeID > 0 {
		strictIso := nextProxy != ""
		nodeView, uErr := s.UpdateNode(ctx, nodeID, UpdateContainerNodeInput{
			ProxyURL:        &nextProxy,
			StrictIsolation: &strictIso,
		})
		if uErr == nil && nodeView != nil {
			if nodeView.Mode == domain.ContainerModeLocalDocker && s.DetectEnv(ctx).DockerAvailable {
				// 重建容器以使新的 HTTP_PROXY / HTTPS_PROXY 环境变量立即生效
				if row, gErr := s.getNodeRow(ctx, nodeID); gErr == nil {
					spec := s.nodeSpec(row)
					if cid, startErr := s.docker.EnsureAndStartContainer(ctx, spec); startErr == nil {
						row, _ = row.Update().SetStatus("running").SetContainerID(cid).ClearLastError().Save(ctx)
						v := s.toNodeView(ctx, row, true)
						nodeView = &v
					}
				}
			}
			if reloaded, rErr := s.db.Account.Query().Where(account.IDEQ(acc.ID)).WithGroups().Only(ctx); rErr == nil {
				acc = reloaded
			}
			return acc, nodeView, nil
		}
	}

	nodeView, updatedAcc, err := s.ProvisionAccountContainer(ctx, ProvisionAccountContainerInput{
		AccountID: acc.ID,
		ProxyURL:  nextProxy,
		AutoStart: true,
	})
	if err != nil {
		return acc, nil, err
	}
	return updatedAcc, nodeView, nil
}

func (s *AntigravityService) syncBoundAccountsEndpoint(ctx context.Context, row *ent.ContainerNode, spec antigravity.NodeSpec) {
	if row == nil {
		return
	}
	baseURL := antigravity.BaseURL(spec)
	allAgy, err := s.db.Account.Query().
		Where(account.DeletedAtIsNil(), account.PlatformEQ(domain.PlatformAntigravity)).
		All(ctx)
	if err != nil {
		return
	}
	for _, acc := range allAgy {
		if extractExtraInt64(acc.Extra, "node_id") != row.ID {
			continue
		}
		creds, cErr := s.buildAntigravityCredentialsWithBase(spec, baseURL, acc.Credentials)
		if cErr != nil {
			continue
		}
		extra := cloneAnyMap(acc.Extra)
		extra["base_url"] = baseURL
		_, _ = acc.Update().SetDomain(baseURL).SetCredentials(creds).SetExtra(extra).Save(ctx)
	}
}

func (s *AntigravityService) upsertConvertedAccountToPool(
	ctx context.Context,
	conv antigravity.ConvertedAGYAccount,
	existingRows []*ent.Account,
	groupIDs []int64,
	autoProvision bool,
) (*ent.Account, bool, error) {
	matched := s.matchExistingAntigravityAccount(existingRows, conv)

	apiToken := firstNonEmptyStrSvc(conv.AccessToken, conv.RefreshToken, "agy-oauth-json")
	creds, err := s.buildConvertedAuthBundleCreds(apiToken, conv, nil)
	if err != nil {
		return nil, false, err
	}

	// 注意：Google OAuth 的 conv.ExpiresAt 是 1 小时短效 access_token 刷新时间（已保存在 creds.auth_bundle 内），
	// 只要持有长期有效的 RefreshToken，绝不能将其写入 Account.ExpiresAt 硬过期字段，否则 1 小时后调度器会误判账号过期。
	var expPtr *time.Time
	if strings.TrimSpace(conv.RefreshToken) == "" && conv.ExpiresAt != "" {
		if parsed, pErr := time.Parse(time.RFC3339, conv.ExpiresAt); pErr == nil {
			expPtr = &parsed
		}
	}

	var saved *ent.Account
	isCreated := false

	if matched != nil {
		mergedCreds, cErr := s.buildConvertedAuthBundleCreds(apiToken, conv, matched.Credentials)
		if cErr != nil {
			return nil, false, cErr
		}
		extra := cloneAnyMap(matched.Extra)
		extra["source_format"] = conv.SourceFormat
		if conv.ProjectID != "" {
			extra["project_id"] = conv.ProjectID
		}
		if conv.ClientID != "" {
			extra["client_id"] = conv.ClientID
		}
		upd := matched.Update().
			SetCredentials(mergedCreds).
			SetExtra(extra).
			SetStatus(domain.StatusActive).
			SetSchedulable(true).
			ClearErrorMessage()
		if conv.Email != "" {
			upd = upd.SetEmail(conv.Email)
		}
		if conv.RawProxy != "" {
			upd = upd.SetProxyURL(conv.RawProxy)
		}
		if strings.TrimSpace(conv.RefreshToken) != "" {
			upd = upd.ClearExpiresAt()
		} else if expPtr != nil {
			upd = upd.SetExpiresAt(*expPtr)
		}
		saved, err = upd.Save(ctx)
		if err != nil {
			return nil, false, err
		}
	} else {
		isCreated = true
		qimei, mid := workbuddy.GenerateDevice()
		accName := strings.TrimSpace(conv.Name)
		if accName == "" {
			accName = "agy-imported"
		}
		if len(accName) > 100 {
			accName = accName[:100]
		}
		extra := map[string]any{
			"source_format": conv.SourceFormat,
			"auth_file":     conv.AuthFilename,
		}
		if conv.ProjectID != "" {
			extra["project_id"] = conv.ProjectID
		}
		if conv.ClientID != "" {
			extra["client_id"] = conv.ClientID
		}
		prio := conv.Priority
		if prio <= 0 {
			prio = 50
		}
		createB := s.db.Account.Create().
			SetName(accName).
			SetPlatform(domain.PlatformAntigravity).
			SetType(domain.AccountTypeOAuthJSON).
			SetDomain("http://127.0.0.1:8317").
			SetEmail(conv.Email).
			SetProxyURL(conv.RawProxy).
			SetQimei36(qimei).
			SetMachineID(mid).
			SetCredentials(creds).
			SetExtra(extra).
			SetConcurrency(5).
			SetPriority(prio).
			SetRateMultiplier(1.0).
			SetStatus(domain.StatusActive).
			SetSchedulable(true)
		if expPtr != nil {
			createB = createB.SetExpiresAt(*expPtr)
		}
		saved, err = createB.Save(ctx)
		if err != nil {
			return nil, false, err
		}
	}

	if len(groupIDs) > 0 {
		_ = s.bindAccountGroups(ctx, saved.ID, groupIDs)
	}

	if autoProvision {
		_, provisionedAcc, pErr := s.ProvisionAccountContainer(ctx, ProvisionAccountContainerInput{
			AccountID:    saved.ID,
			ProxyURL:     conv.RawProxy,
			AutoStart:    true,
			AuthFilename: conv.AuthFilename,
			AuthJSON:     conv.CanonicalAuthJSONBytes(),
		})
		if pErr == nil && provisionedAcc != nil {
			saved = provisionedAcc
		} else if pErr != nil {
			// 即便端口或容器受限，仍确保在独立目录落盘真实 agy CLI 原生凭证文件
			isoDir := filepath.Join(s.dataRoot, fmt.Sprintf("account-%d", saved.ID), "auths")
			if _, wErr := antigravity.WriteIsolatedAGYAuthFiles(isoDir, conv, conv.AuthFilename); wErr == nil {
				extra := cloneAnyMap(saved.Extra)
				extra["auth_dir"] = isoDir
				extra["auth_file"] = conv.AuthFilename
				if uAcc, uErr := saved.Update().SetExtra(extra).Save(ctx); uErr == nil {
					saved = uAcc
				}
			}
		}
	} else {
		isoDir := filepath.Join(s.dataRoot, fmt.Sprintf("account-%d", saved.ID), "auths")
		if _, wErr := antigravity.WriteIsolatedAGYAuthFiles(isoDir, conv, conv.AuthFilename); wErr == nil {
			extra := cloneAnyMap(saved.Extra)
			extra["auth_dir"] = isoDir
			extra["auth_file"] = conv.AuthFilename
			if uAcc, uErr := saved.Update().SetExtra(extra).Save(ctx); uErr == nil {
				saved = uAcc
			}
		}
	}

	if reloaded, rErr := s.db.Account.Query().Where(account.IDEQ(saved.ID)).WithGroups().Only(ctx); rErr == nil {
		saved = reloaded
	}
	return saved, isCreated, nil
}

func (s *AntigravityService) matchExistingAntigravityAccount(rows []*ent.Account, conv antigravity.ConvertedAGYAccount) *ent.Account {
	if conv.Email != "" {
		for _, r := range rows {
			if strings.EqualFold(strings.TrimSpace(r.Email), conv.Email) {
				return r
			}
		}
	}
	if conv.RefreshToken != "" {
		for _, r := range rows {
			if rt := s.decryptAccountCredField(r.Credentials, "refresh_token_encrypted", "refresh_token"); rt != "" && rt == conv.RefreshToken {
				return r
			}
		}
	}
	if conv.Name != "" && !strings.HasPrefix(conv.Name, "agy-token-") {
		for _, r := range rows {
			if strings.EqualFold(strings.TrimSpace(r.Name), conv.Name) {
				return r
			}
		}
	}
	return nil
}

func (s *AntigravityService) buildConvertedAuthBundleCreds(
	apiToken string,
	conv antigravity.ConvertedAGYAccount,
	base map[string]any,
) (map[string]any, error) {
	creds := cloneAnyMap(base)
	if apiToken != "" {
		enc, err := s.encryptSecret(apiToken)
		if err != nil {
			return nil, err
		}
		if s.vault != nil {
			creds["api_key_encrypted"] = enc
			delete(creds, "api_key")
		} else {
			creds["api_key"] = apiToken
		}
	}
	if conv.RefreshToken != "" {
		enc, err := s.encryptSecret(conv.RefreshToken)
		if err != nil {
			return nil, err
		}
		if s.vault != nil {
			creds["refresh_token_encrypted"] = enc
			delete(creds, "refresh_token")
		} else {
			creds["refresh_token"] = conv.RefreshToken
		}
	}
	if conv.ClientSecret != "" {
		enc, err := s.encryptSecret(conv.ClientSecret)
		if err != nil {
			return nil, err
		}
		if s.vault != nil {
			creds["client_secret_encrypted"] = enc
			delete(creds, "client_secret")
		} else {
			creds["client_secret"] = conv.ClientSecret
		}
	}

	bundleMeta := map[string]any{
		"format":         "agy_cli_v1",
		"source_format":  conv.SourceFormat,
		"client_id":      conv.ClientID,
		"project_id":     conv.ProjectID,
		"scope":          conv.Scope,
		"expiry_date_ms": conv.ExpiryDateMs,
		"expires_at":     conv.ExpiresAt,
		"auth_filename":  conv.AuthFilename,
	}
	creds["auth_bundle"] = bundleMeta
	if conv.ClientID != "" {
		creds["client_id"] = conv.ClientID
	}
	if conv.ProjectID != "" {
		creds["project_id"] = conv.ProjectID
	}
	return creds, nil
}

func (s *AntigravityService) buildAntigravityCredentialsWithBase(spec antigravity.NodeSpec, baseURL string, existing map[string]any) (map[string]any, error) {
	nodeCreds, err := s.buildAntigravityCredentials(spec, baseURL)
	if err != nil {
		return nil, err
	}
	merged := cloneAnyMap(existing)
	// 若原账号已有 OAuth refresh_token / auth_bundle，保留它们，仅写入节点推理与管理端点凭证
	for k, v := range nodeCreds {
		merged[k] = v
	}
	return merged, nil
}

func (s *AntigravityService) enrichSyncedAccountBundle(
	ctx context.Context,
	acc *ent.Account,
	conv antigravity.ConvertedAGYAccount,
	authDir string,
) (*ent.Account, error) {
	if acc == nil {
		return nil, nil
	}
	creds, err := s.buildConvertedAuthBundleCreds("", conv, acc.Credentials)
	if err != nil {
		return acc, err
	}
	extra := cloneAnyMap(acc.Extra)
	extra["source_format"] = conv.SourceFormat
	if strings.TrimSpace(authDir) != "" {
		extra["auth_dir"] = strings.TrimSpace(authDir)
	}
	if conv.ProjectID != "" {
		extra["project_id"] = conv.ProjectID
	}
	upd := acc.Update().SetCredentials(creds).SetExtra(extra)
	if strings.TrimSpace(conv.RefreshToken) != "" {
		upd = upd.ClearExpiresAt()
	} else if conv.ExpiresAt != "" {
		if parsed, pErr := time.Parse(time.RFC3339, conv.ExpiresAt); pErr == nil {
			upd = upd.SetExpiresAt(parsed)
		}
	}
	saved, sErr := upd.Save(ctx)
	if sErr != nil {
		return acc, sErr
	}
	if reloaded, rErr := s.db.Account.Query().Where(account.IDEQ(saved.ID)).WithGroups().Only(ctx); rErr == nil {
		return reloaded, nil
	}
	return saved, nil
}

func (s *AntigravityService) synthesizeAGYAccountFromEnt(acc *ent.Account, proxyURL string) antigravity.ConvertedAGYAccount {
	if acc == nil {
		return antigravity.ConvertedAGYAccount{}
	}
	accessTok := s.decryptAccountCredField(acc.Credentials, "api_key_encrypted", "api_key")
	refreshTok := s.decryptAccountCredField(acc.Credentials, "refresh_token_encrypted", "refresh_token")
	clientSec := s.decryptAccountCredField(acc.Credentials, "client_secret_encrypted", "client_secret")
	if strings.HasPrefix(accessTok, "sk-agy-") || accessTok == "agy-oauth-json" {
		accessTok = ""
	}
	if strings.HasPrefix(accessTok, "1//") && refreshTok == "" {
		refreshTok = accessTok
		accessTok = ""
	}
	if accessTok == "" && refreshTok == "" {
		return antigravity.ConvertedAGYAccount{}
	}
	clientID := extractExtraString(acc.Credentials, "client_id")
	if clientID == "" {
		clientID = extractExtraString(acc.Extra, "client_id")
	}
	projectID := extractExtraString(acc.Credentials, "project_id")
	if projectID == "" {
		projectID = extractExtraString(acc.Extra, "project_id")
	}
	var expStr string
	var expMs int64
	if acc.ExpiresAt != nil {
		expStr = acc.ExpiresAt.UTC().Format(time.RFC3339)
		expMs = acc.ExpiresAt.UnixMilli()
	}
	rawJSON, _ := json.Marshal(map[string]any{
		"type":          "antigravity",
		"email":         acc.Email,
		"name":          acc.Name,
		"access_token":  accessTok,
		"refresh_token": refreshTok,
		"client_id":     clientID,
		"client_secret": clientSec,
		"project_id":    projectID,
		"expires_at":    expStr,
		"expiry_date":   expMs,
	})
	norm, err := antigravity.NormalizeSingleCredential(fmt.Sprintf("account-%d.json", acc.ID), rawJSON, proxyURL)
	if err != nil {
		return antigravity.ConvertedAGYAccount{}
	}
	return norm
}

func (s *AntigravityService) decryptAccountCredField(creds map[string]any, encKey, plainKey string) string {
	if creds == nil {
		return ""
	}
	if enc, ok := creds[encKey].(string); ok && strings.TrimSpace(enc) != "" {
		if dec := s.decryptSecret(enc); dec != "" {
			return dec
		}
	}
	if raw, ok := creds[plainKey].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func (s *AntigravityService) archiveSoftDeletedGroupByName(ctx context.Context, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	softDeleted, err := s.db.Group.Query().
		Where(group.NameEQ(name), group.DeletedAtNotNil()).
		All(ctx)
	if err != nil {
		return
	}
	for _, r := range softDeleted {
		archived := fmt.Sprintf("%s__deleted_%d", r.Name, time.Now().UnixNano())
		_, _ = r.Update().SetName(archived).Save(ctx)
	}
}

func preserveRedactedNodeProxy(newRaw, existingRaw string) string {
	trimmedNew := strings.TrimSpace(newRaw)
	trimmedOld := strings.TrimSpace(existingRaw)
	if trimmedNew == "" {
		return ""
	}
	lowerNew := strings.ToLower(trimmedNew)
	if !strings.Contains(lowerNew, "***") && !strings.Contains(lowerNew, "%2a%2a%2a") {
		return trimmedNew
	}
	if trimmedOld == "" {
		return ""
	}
	uNew, errNew := parseProxyForCompare(trimmedNew)
	uOld, errOld := parseProxyForCompare(trimmedOld)
	if errNew != nil || errOld != nil {
		return trimmedOld
	}
	if strings.EqualFold(uNew.Scheme, uOld.Scheme) && strings.EqualFold(uNew.Host, uOld.Host) {
		newUser := uNew.User.Username()
		oldUser := uOld.User.Username()
		if newUser == "" || newUser == "***" || newUser == oldUser {
			return trimmedOld
		}
		if oldPass, hasPass := uOld.User.Password(); hasPass && oldPass != "" {
			uNew.User = url.UserPassword(newUser, oldPass)
			return uNew.String()
		}
	}
	return trimmedOld
}

func parseProxyForCompare(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return url.Parse(s)
}

func cloneAnyMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src)+4)
	for k, v := range src {
		out[k] = v
	}
	return out
}

func firstNonEmptyStrSvc(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// AntigravityAccountModelsOutput 描述单个 AntiGravity 账号支持的模型、套餐层级与各模型实时额度输出。
type AntigravityAccountModelsOutput struct {
	OK              bool                         `json:"ok"`
	AccountID       int64                        `json:"account_id"`
	Email           string                       `json:"email,omitempty"`
	NodeID          int64                        `json:"node_id,omitempty"`
	ContainerName   string                       `json:"container_name,omitempty"`
	ProxyURL        string                       `json:"proxy_url,omitempty"`
	ProjectID       string                       `json:"project_id,omitempty"`
	TierID          string                       `json:"tier_id,omitempty"`
	TierName        string                       `json:"tier_name,omitempty"`
	Driver          string                       `json:"driver,omitempty"`
	CLIReady        bool                         `json:"cli_ready"`
	AvgRemainingPct *float64                     `json:"avg_remaining_pct,omitempty"`
	EarliestResetAt string                       `json:"earliest_reset_at,omitempty"`
	Models          []antigravity.ModelQuotaItem `json:"models"`
	FetchedAt       string                       `json:"fetched_at"`
	Warning         string                       `json:"warning,omitempty"`
}

// GetAccountModelsAndQuota 查询指定 AntiGravity 账号绑定容器的实时可用模型清单、套餐层级与各模型剩余额度百分比，并顺带写回账号余额快照。
func (s *AntigravityService) GetAccountModelsAndQuota(ctx context.Context, accountID int64) (*AntigravityAccountModelsOutput, error) {
	acc, err := s.db.Account.Query().
		Where(account.IDEQ(accountID), account.DeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询账号 #%d 失败: %w", accountID, err)
	}
	nodeID := extractExtraInt64(acc.Extra, "node_id")
	var row *ent.ContainerNode
	if nodeID > 0 {
		row, _ = s.getNodeRow(ctx, nodeID)
	}
	if row == nil {
		// 扫描 bound_account_id 兜底匹配
		allNodes, _ := s.db.ContainerNode.Query().All(ctx)
		for _, n := range allNodes {
			if extractExtraInt64(n.Extra, "bound_account_id") == acc.ID {
				row = n
				nodeID = n.ID
				break
			}
		}
	}

	var spec antigravity.NodeSpec
	var containerName string
	if row != nil {
		spec = s.nodeSpec(row)
		containerName = spec.EffectiveContainerName()
		// 顺便补齐旧容器挂载目录中的 ADC client_secret，使运行中容器无需重导入即可用上 AGY_ADC_AUTH=1
		if strings.TrimSpace(spec.AuthDir) != "" {
			_, _ = antigravity.EnsureIsolatedADCFile(spec.AuthDir)
		}
	} else {
		spec = antigravity.NodeSpec{
			Name:     acc.Name,
			Mode:     antigravity.ModeDirectCLI,
			ProxyURL: acc.ProxyURL,
			AuthDir:  extractExtraString(acc.Extra, "auth_dir"),
		}
	}

	status, qErr := s.agClient.FetchQuotaStatus(ctx, spec)
	if qErr != nil {
		refreshTok := s.decryptAccountCredField(acc.Credentials, "refresh_token_encrypted", "refresh_token")
		clientID := firstNonEmptyStrSvc(extractExtraString(acc.Credentials, "client_id"), extractExtraString(acc.Extra, "client_id"))
		clientSec := s.decryptAccountCredField(acc.Credentials, "client_secret_encrypted", "client_secret")
		projID := firstNonEmptyStrSvc(extractExtraString(acc.Credentials, "project_id"), extractExtraString(acc.Extra, "project_id"))
		if refreshTok != "" {
			httpCli, cErr := s.agClient.ProxyPool().Client(strings.TrimSpace(acc.ProxyURL))
			if cErr == nil {
				if tokRes, rErr := antigravity.RefreshGoogleOAuthToken(ctx, httpCli, clientID, clientSec, refreshTok, ""); rErr == nil {
					ua := antigravity.BuildCLIUserAgent("", spec.Fingerprint.OS, spec.Fingerprint.Arch)
					status, qErr = antigravity.FetchAntigravityQuotaAndModels(ctx, httpCli, "", tokRes.AccessToken, projID, ua)
				}
			}
		}
	}
	if qErr != nil {
		return nil, qErr
	}

	// 同步将额度快照持久化到 Account 表，使账号列表与 AntiGravity 列表刷新页面后依然直接可见额度
	if status != nil {
		rem := 100.0
		if status.AvgRemainingPct != nil {
			rem = *status.AvgRemainingPct
		}
		pkgs := make([]map[string]any, 0, len(status.Models))
		for _, m := range status.Models {
			mRem := 100.0
			if m.RemainingPct != nil {
				mRem = *m.RemainingPct
			}
			mUsed := 100.0 - mRem
			if mUsed < 0 {
				mUsed = 0
			}
			pkgs = append(pkgs, map[string]any{
				"package_code": m.ID,
				"package_name": m.ID,
				"display_name": m.Name,
				"kind":         "model_quota_pct",
				"total":        100.0,
				"remain":       mRem,
				"used":         mUsed,
				"total_count":  100,
				"expired_at":   m.ResetTime,
				"expired":      m.Exhausted,
			})
		}
		planName := firstNonEmptyStrSvc(status.TierName, status.TierID, "Google AI Code")
		upd := s.db.Account.UpdateOneID(acc.ID).
			SetBalanceFetchedAt(time.Now()).
			ClearBalanceError().
			SetBalanceRemaining(rem).
			SetBalanceTotalCredits(100.0).
			SetBalancePackages(pkgs).
			SetBalancePlanID(firstNonEmptyStrSvc(status.TierID, planName))
		if status.EarliestResetAt != "" {
			if t, tErr := time.Parse(time.RFC3339, status.EarliestResetAt); tErr == nil {
				upd = upd.SetBalancePeriodEnd(t)
			}
		}
		_, _ = upd.Save(ctx)
	}

	return &AntigravityAccountModelsOutput{
		OK:              true,
		AccountID:       acc.ID,
		Email:           firstNonEmptyStrSvc(acc.Email, acc.Name),
		NodeID:          nodeID,
		ContainerName:   containerName,
		ProxyURL:        workbuddy.RedactProxyURL(acc.ProxyURL),
		ProjectID:       status.ProjectID,
		TierID:          status.TierID,
		TierName:        status.TierName,
		Driver:          status.Driver,
		CLIReady:        status.CLIReady,
		AvgRemainingPct: status.AvgRemainingPct,
		EarliestResetAt: status.EarliestResetAt,
		Models:          status.Models,
		FetchedAt:       status.FetchedAt,
		Warning:         status.Warning,
	}, nil
}

// TestAntigravityAccountInput 描述针对单个 AntiGravity 账号/容器发起在线测试调用的入参。
type TestAntigravityAccountInput struct {
	AccountID    int64
	Model        string
	SystemPrompt string
	Prompt       string
	TestToolCall bool
}

// TestAntigravityAccountOutput 描述单容器在线测试调用的详细返回结果。
type TestAntigravityAccountOutput struct {
	OK            bool             `json:"ok"`
	AccountID     int64            `json:"account_id"`
	NodeID        int64            `json:"node_id,omitempty"`
	ContainerName string           `json:"container_name,omitempty"`
	Model         string           `json:"model"`
	Reply         string           `json:"reply"`
	Reasoning     string           `json:"reasoning,omitempty"`
	ToolCalls     []map[string]any `json:"tool_calls,omitempty"`
	Driver        string           `json:"driver,omitempty"`
	PromptMode    string           `json:"prompt_mode,omitempty"`
	LatencyMs     int64            `json:"latency_ms"`
	ProxyURL      string           `json:"proxy_url,omitempty"`
	Error         string           `json:"error,omitempty"`
}

// TestAccountChat 直接穿透至单个 AntiGravity 账号绑定的 1:1 容器执行一次真实对话（或工具调用）验证。
func (s *AntigravityService) TestAccountChat(ctx context.Context, in TestAntigravityAccountInput) (*TestAntigravityAccountOutput, error) {
	acc, err := s.db.Account.Query().
		Where(account.IDEQ(in.AccountID), account.DeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询账号 #%d 失败: %w", in.AccountID, err)
	}
	nodeID := extractExtraInt64(acc.Extra, "node_id")
	var row *ent.ContainerNode
	if nodeID > 0 {
		row, _ = s.getNodeRow(ctx, nodeID)
	}
	if row == nil {
		return nil, fmt.Errorf("账号 #%d 尚未绑定专属容器，请先点击自愈启动或补配容器", acc.ID)
	}
	spec := s.nodeSpec(row)
	if strings.TrimSpace(spec.AuthDir) != "" {
		_, _ = antigravity.EnsureIsolatedADCFile(spec.AuthDir)
	}

	model := strings.TrimSpace(in.Model)
	if model == "" {
		if mList, mErr := s.agClient.Models(ctx, spec); mErr == nil && len(mList) > 0 {
			model = mList[0].ID
			for _, pref := range []string{"gemini-3.8-flash-low", "gemini-4-argon-low", "gemini-3.8-flash-medium", "flash"} {
				for _, m := range mList {
					if strings.Contains(strings.ToLower(m.ID), pref) {
						model = m.ID
						break
					}
				}
				if strings.Contains(strings.ToLower(model), pref) {
					break
				}
			}
		} else {
			model = "gemini-3.8-flash-low"
		}
	}

	promptText := strings.TrimSpace(in.Prompt)
	if promptText == "" {
		if in.TestToolCall {
			promptText = "Call the get_gateway_ping tool with target=\"cmd2api\" right now. Do not answer in prose before calling the tool."
		} else {
			promptText = "请用一句话简短回复：当前节点连通正常，并报出 7 * 8 的结果。"
		}
	}

	msgs := make([]map[string]any, 0, 2)
	if sys := strings.TrimSpace(in.SystemPrompt); sys != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": sys})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": promptText})

	reqMap := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   true,
	}
	if in.TestToolCall {
		reqMap["tools"] = []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "get_gateway_ping",
					"description": "Verify gateway tool calling round-trip",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"target": map[string]any{"type": "string"},
						},
						"required": []string{"target"},
					},
				},
			},
		}
	}

	payload, _ := json.Marshal(reqMap)
	started := time.Now()
	testCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resp, _, chatErr := s.agClient.ChatStreamWithSession(testCtx, spec, payload, "")
	latency := time.Since(started).Milliseconds()
	out := &TestAntigravityAccountOutput{
		AccountID:     acc.ID,
		NodeID:        row.ID,
		ContainerName: spec.EffectiveContainerName(),
		Model:         model,
		LatencyMs:     latency,
		ProxyURL:      workbuddy.RedactProxyURL(row.ProxyURL),
	}
	if chatErr != nil {
		out.OK = false
		out.Error = chatErr.Error()
		s.recordConsoleTestUsage(ctx, acc, model, started, nil, 502, out.Error, nil)
		return out, nil
	}
	defer resp.Body.Close()

	out.Driver = resp.Header.Get("X-Agy-Driver")
	out.PromptMode = firstNonEmptyStrSvc(resp.Header.Get("X-Agy-Prompt-Mode"), resp.Header.Get("X-Agy-Prompt-Blend-Mode"))

	var replyBuf, reasonBuf strings.Builder
	var toolCalls []map[string]any
	var lastUsage map[string]any
	tcByIdx := map[int]map[string]any{}

	rawSSE, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	totalElapsedMs := int(time.Since(started).Milliseconds())
	out.LatencyMs = int64(totalElapsedMs)

	for _, line := range strings.Split(string(rawSSE), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		dataPart := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if dataPart == "" || dataPart == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(dataPart), &chunk) != nil {
			continue
		}
		if errObj, ok := chunk["error"].(map[string]any); ok && errObj != nil {
			errMsg, _ := errObj["message"].(string)
			out.OK = false
			out.Error = firstNonEmptyStrSvc(errMsg, dataPart)
			s.recordConsoleTestUsage(ctx, acc, model, started, nil, 502, out.Error, lastUsage)
			return out, nil
		}
		if uObj, ok := chunk["usage"].(map[string]any); ok && uObj != nil {
			lastUsage = uObj
		}
		choices, _ := chunk["choices"].([]any)
		for _, chItem := range choices {
			ch, _ := chItem.(map[string]any)
			delta, _ := ch["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			if c, ok := delta["content"].(string); ok {
				replyBuf.WriteString(c)
			}
			if rc, ok := delta["reasoning_content"].(string); ok {
				reasonBuf.WriteString(rc)
			}
			if tcs, ok := delta["tool_calls"].([]any); ok {
				for _, tcRaw := range tcs {
					tc, _ := tcRaw.(map[string]any)
					if tc == nil {
						continue
					}
					idx := int(extractExtraInt64(tc, "index"))
					cur, exists := tcByIdx[idx]
					if !exists {
						cur = map[string]any{"id": "", "name": "", "arguments": ""}
						tcByIdx[idx] = cur
					}
					if idStr, ok := tc["id"].(string); ok && idStr != "" {
						cur["id"] = idStr
					}
					if fn, ok := tc["function"].(map[string]any); ok && fn != nil {
						if nStr, ok := fn["name"].(string); ok && nStr != "" {
							cur["name"] = nStr
						}
						if aStr, ok := fn["arguments"].(string); ok && aStr != "" {
							cur["arguments"] = fmt.Sprint(cur["arguments"]) + aStr
						}
					}
				}
			}
		}
	}
	for i := 0; i < len(tcByIdx); i++ {
		if item, ok := tcByIdx[i]; ok {
			toolCalls = append(toolCalls, item)
		}
	}

	out.Reply = strings.TrimSpace(replyBuf.String())
	out.Reasoning = strings.TrimSpace(reasonBuf.String())
	out.ToolCalls = toolCalls
	out.OK = out.Reply != "" || len(out.ToolCalls) > 0 || out.Reasoning != ""
	if !out.OK && out.Error == "" {
		out.Error = "容器已响应但未返回可见文本或工具调用"
	}
	firstMs := int(latency)
	if firstMs <= 0 {
		firstMs = totalElapsedMs
	}
	statusCode := 200
	if !out.OK {
		statusCode = 502
	}
	s.recordConsoleTestUsage(ctx, acc, model, started, &firstMs, statusCode, out.Error, lastUsage)

	if out.OK {
		now := time.Now()
		_, _ = s.db.Account.UpdateOneID(acc.ID).
			SetLastHealthCheckAt(now).
			SetLastHealthCheckOk(true).
			ClearLastHealthCheckError().
			SetLatencyMs(totalElapsedMs).
			SetConsecutiveFailures(0).
			ClearErrorMessage().
			Save(ctx)
	}
	return out, nil
}

// SetRecorder 注入用量记录器，使控制台 AntiGravity 在线测试调用同样写入调用明细日志。
func (s *AntigravityService) SetRecorder(r *Recorder) {
	if s != nil {
		s.recorder = r
	}
}

func (s *AntigravityService) recordConsoleTestUsage(
	ctx context.Context,
	acc *ent.Account,
	model string,
	started time.Time,
	firstTokenMs *int,
	statusCode int,
	errMsg string,
	usageMap map[string]any,
) {
	if s == nil || s.recorder == nil || s.db == nil || acc == nil {
		return
	}
	recCtx := context.WithoutCancel(ctx)
	agLinks, _ := s.db.AccountGroup.Query().
		Where(accountgroup.AccountIDEQ(acc.ID)).
		All(recCtx)
	var groupIDs []int64
	for _, l := range agLinks {
		if l.GroupID > 0 {
			groupIDs = append(groupIDs, l.GroupID)
		}
	}

	var keyRow *ent.APIKey
	if len(groupIDs) > 0 {
		keyRow, _ = s.db.APIKey.Query().
			Where(apikey.DeletedAtIsNil(), apikey.GroupIDIn(groupIDs...)).
			Order(ent.Asc(apikey.FieldID)).
			First(recCtx)
	}
	if keyRow == nil {
		keyRow, _ = s.db.APIKey.Query().
			Where(apikey.DeletedAtIsNil()).
			Order(ent.Asc(apikey.FieldID)).
			First(recCtx)
	}
	if keyRow == nil {
		return
	}

	var resolvedGroupID *int64
	if len(groupIDs) > 0 {
		gid := groupIDs[0]
		resolvedGroupID = &gid
	} else if keyRow.GroupID != nil {
		gid := *keyRow.GroupID
		resolvedGroupID = &gid
	}

	var inputTokens, outputTokens, cachedTokens, cacheCreateTokens, reasoningTokens int
	if usageMap != nil {
		promptTokens := int(extractExtraInt64(usageMap, "prompt_tokens"))
		outputTokens = int(extractExtraInt64(usageMap, "completion_tokens"))
		cachedTokens = int(extractExtraInt64(usageMap, "cached_tokens"))
		cacheCreateTokens = int(extractExtraInt64(usageMap, "cache_creation_input_tokens"))
		if ptd, ok := usageMap["prompt_tokens_details"].(map[string]any); ok && ptd != nil {
			if cachedTokens == 0 {
				cachedTokens = int(extractExtraInt64(ptd, "cached_tokens"))
			}
			if cacheCreateTokens == 0 {
				cacheCreateTokens = int(extractExtraInt64(ptd, "cache_creation_tokens"))
			}
		}
		if ctd, ok := usageMap["completion_tokens_details"].(map[string]any); ok && ctd != nil {
			reasoningTokens = int(extractExtraInt64(ctd, "reasoning_tokens"))
		}
		if promptTokens > 0 {
			inputTokens = promptTokens - cachedTokens
			if inputTokens < 0 {
				inputTokens = 0
			}
		} else {
			inputTokens = int(extractExtraInt64(usageMap, "input_tokens"))
		}
	}

	var rb [4]byte
	_, _ = rand.Read(rb[:])
	reqID := "agy-test-" + hex.EncodeToString(rb[:])
	durMs := int(time.Since(started).Milliseconds())
	if durMs < 0 {
		durMs = 0
	}
	rateMult := acc.RateMultiplier
	if rateMult <= 0 {
		rateMult = 1
	}

	rec := relay.UsageRecord{
		UserID:              keyRow.UserID,
		APIKeyID:            keyRow.ID,
		AccountID:           acc.ID,
		GroupID:             resolvedGroupID,
		Platform:            domain.PlatformAntigravity,
		RequestID:           reqID,
		Model:               model,
		InputTokens:         inputTokens,
		OutputTokens:        outputTokens,
		CacheCreationTokens: cacheCreateTokens,
		CacheReadTokens:     cachedTokens,
		ReasoningTokens:     reasoningTokens,
		RateMultiplier:      rateMult,
		Stream:              true,
		DurationMs:          durMs,
		FirstTokenMs:        firstTokenMs,
		StatusCode:          statusCode,
		ErrorMessage:        strings.TrimSpace(errMsg),
		UserAgent:           "Analy2API-Console-Test/1.0",
		IPAddress:           "127.0.0.1",
	}
	s.recorder.RecordWithMeta(recCtx, rec, domain.PlatformAntigravity, reasoningTokens)
}

// UpgradeManagedContainersOnStartup 在服务端启动后异步检测内置单账号容器镜像与运行中容器：
// 若刚完成版本升级（旧容器缺失最新镜像标签或隔离目录缺失 ADC client_secret），自动补全 ADC 凭证并滚动重建容器，零手工干预。
func (s *AntigravityService) UpgradeManagedContainersOnStartup(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	// 自动修复历史版本将 OAuth 1 小时短效 access_token TTL 误写入 Account.ExpiresAt 导致 1 小时后调度器跳过账号的问题
	if staleAccs, qErr := s.db.Account.Query().
		Where(
			account.DeletedAtIsNil(),
			account.ExpiresAtNotNil(),
			account.PlatformEQ(domain.PlatformAntigravity),
		).
		All(ctx); qErr == nil {
		for _, a := range staleAccs {
			hasRT := s.decryptAccountCredField(a.Credentials, "refresh_token_encrypted", "refresh_token") != ""
			hasNode := extractExtraInt64(a.Extra, "node_id") > 0
			if hasRT || hasNode {
				_, _ = s.db.Account.UpdateOneID(a.ID).ClearExpiresAt().Save(ctx)
			}
		}
	}
	env := s.DetectEnv(ctx)
	if !env.DockerAvailable {
		return
	}
	rows, err := s.db.ContainerNode.Query().All(ctx)
	if err != nil || len(rows) == 0 {
		return
	}

	var localRows []*ent.ContainerNode
	for _, r := range rows {
		if r.Mode == domain.ContainerModeLocalDocker {
			if strings.TrimSpace(r.AuthDir) != "" {
				_, _ = antigravity.EnsureIsolatedADCFile(r.AuthDir)
			}
			localRows = append(localRows, r)
		}
	}
	if len(localRows) == 0 {
		return
	}

	if err := s.docker.EnsureAgyCLIImage(ctx, s.defaultImage); err != nil {
		s.logger.Warn("启动期自检升级单账号 agy 镜像失败", "err", err)
		return
	}

	for _, r := range localRows {
		if ctx.Err() != nil {
			return
		}
		if r.Status != "running" {
			continue
		}
		if _, rErr := s.RunNodeAction(ctx, r.ID, "start"); rErr != nil {
			s.logger.Warn("启动期滚动刷新单账号容器失败", "node_id", r.ID, "err", rErr)
		} else {
			s.logger.Info("启动期已同步刷新单账号 agy 容器", "node_id", r.ID, "container", r.ContainerName)
		}
	}
}
