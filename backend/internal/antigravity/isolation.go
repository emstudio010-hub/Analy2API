package antigravity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"cmd2api/internal/workbuddy"
)

const (
	// DefaultCLIVersion 是 AntiGravity CLI 官方客户端默认版本号（可通过 ANTIGRAVITY_USER_AGENT_VERSION 环境变量覆盖）。
	DefaultCLIVersion = "2.9.1"

	// AntigravityUserAgentVersionEnv 允许运行时覆盖 AntiGravity CLI User-Agent 版本号。
	AntigravityUserAgentVersionEnv = "ANTIGRAVITY_USER_AGENT_VERSION"

	// IsolationStatusIsolated 表示节点已配置独立代理 IP 且与其他启用节点无冲突，并持有独立设备指纹。
	IsolationStatusIsolated = "isolated"
	// IsolationStatusDuplicateProxy 表示多个节点共用了同一 ProxyURL，违反 1 容器 1 独立 IP 隔离策略。
	IsolationStatusDuplicateProxy = "duplicate_proxy"
	// IsolationStatusNoProxy 表示节点未配置独立代理 IP（将直连宿主机出口 IP），存在多容器共享宿主机 IP 风险。
	IsolationStatusNoProxy = "no_proxy"
)

var cliVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// EffectiveCLIVersion 返回当前生效的 AntiGravity CLI 客户端版本号。
func EffectiveCLIVersion() string {
	if v := strings.TrimSpace(os.Getenv(AntigravityUserAgentVersionEnv)); cliVersionRe.MatchString(v) {
		return v
	}
	return DefaultCLIVersion
}

// BuildCLIUserAgent 构造严格符合官方 AntiGravity CLI 客户端格式的 User-Agent：
// `antigravity/<version> <os>/<arch>`（绝不附加任何自定义后缀或网关标识）。
func BuildCLIUserAgent(version, osName, arch string) string {
	v := strings.TrimSpace(version)
	if !cliVersionRe.MatchString(v) {
		v = EffectiveCLIVersion()
	}
	o := strings.ToLower(strings.TrimSpace(osName))
	if o == "" {
		o = "windows"
	}
	a := strings.ToLower(strings.TrimSpace(arch))
	if a == "" {
		a = "amd64"
	}
	return fmt.Sprintf("antigravity/%s %s/%s", v, o, a)
}

// NodeFingerprint 描述单个 AntiGravity 容器/CLI 节点专属的隔离客户端身份。
// 每个独立客户端持有独立的 FingerprintID、Qimei36、MachineID、容器 Hostname、MAC、ClientID 与标准 AntiGravity CLI User-Agent。
type NodeFingerprint struct {
	FingerprintID string `json:"fingerprint_id"`
	Qimei36       string `json:"qimei36"`
	MachineID     string `json:"machine_id"`
	Hostname      string `json:"hostname"`
	MACAddress    string `json:"mac_address"`
	UserAgent     string `json:"user_agent"`
	ClientID      string `json:"client_id"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	RotatedAt     string `json:"rotated_at"`
}

var isolatedProfiles = []struct {
	OS   string
	Arch string
}{
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "windows", Arch: "amd64"},
}

// GenerateNodeFingerprint 为单个 AntiGravity CLI 节点签发一套全新的独立设备指纹与真实官方格式 User-Agent。
func GenerateNodeFingerprint(seed string) NodeFingerprint {
	qimei, machineID := workbuddy.GenerateDevice()

	var rb [16]byte
	if _, err := rand.Read(rb[:]); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, time.Now().UnixNano())))
		copy(rb[:], sum[:16])
	}

	fpHex := hex.EncodeToString(rb[:8])
	cliHex := hex.EncodeToString(rb[8:14])
	prof := isolatedProfiles[int(rb[15])%len(isolatedProfiles)]

	mac := fmt.Sprintf("02:42:ac:%02x:%02x:%02x", rb[3], rb[4], rb[5])
	hostname := "agy-" + fpHex[:10]
	fpID := "fp_" + fpHex

	return NodeFingerprint{
		FingerprintID: fpID,
		Qimei36:       qimei,
		MachineID:     machineID,
		Hostname:      hostname,
		MACAddress:    mac,
		UserAgent:     BuildCLIUserAgent("", prof.OS, prof.Arch),
		ClientID:      "cli_" + cliHex,
		OS:            prof.OS,
		Arch:          prof.Arch,
		RotatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

// NormalizeNodeFingerprint 补齐节点指纹中的缺失字段，并将旧版非官方格式的 UA 自动归一化为 `antigravity/<ver> <os>/<arch>`。
func NormalizeNodeFingerprint(fp NodeFingerprint, fallbackSeed string) NodeFingerprint {
	if strings.TrimSpace(fp.FingerprintID) == "" ||
		strings.TrimSpace(fp.Hostname) == "" ||
		strings.TrimSpace(fp.MACAddress) == "" ||
		strings.TrimSpace(fp.ClientID) == "" ||
		strings.TrimSpace(fp.UserAgent) == "" ||
		strings.TrimSpace(fp.OS) == "" ||
		strings.TrimSpace(fp.Arch) == "" {
		fresh := GenerateNodeFingerprint(fallbackSeed)
		if strings.TrimSpace(fp.FingerprintID) == "" {
			fp.FingerprintID = fresh.FingerprintID
		}
		if strings.TrimSpace(fp.Hostname) == "" {
			fp.Hostname = fresh.Hostname
		}
		if strings.TrimSpace(fp.MACAddress) == "" {
			fp.MACAddress = fresh.MACAddress
		}
		if strings.TrimSpace(fp.ClientID) == "" {
			fp.ClientID = fresh.ClientID
		}
		if strings.TrimSpace(fp.OS) == "" {
			fp.OS = fresh.OS
		}
		if strings.TrimSpace(fp.Arch) == "" {
			fp.Arch = fresh.Arch
		}
		if strings.TrimSpace(fp.UserAgent) == "" {
			fp.UserAgent = BuildCLIUserAgent("", fp.OS, fp.Arch)
		}
		if strings.TrimSpace(fp.RotatedAt) == "" {
			fp.RotatedAt = fresh.RotatedAt
		}
	}
	// 严禁对外发送旧版 CLIProxyAPI/... 或附带 fp_ 后缀的非真实 AntiGravity CLI User-Agent
	lowerUA := strings.ToLower(strings.TrimSpace(fp.UserAgent))
	if !strings.HasPrefix(lowerUA, "antigravity/") || strings.Contains(lowerUA, "fp_") || strings.Contains(lowerUA, "cliproxy") {
		fp.UserAgent = BuildCLIUserAgent("", fp.OS, fp.Arch)
	}
	fp.Qimei36, fp.MachineID = workbuddy.EnsureDevice(fp.Qimei36, fp.MachineID)
	return fp
}

// ExtractFingerprintFromExtra 从节点的 Extra 扩展字段恢复设备指纹；若缺失则生成新指纹并返回 created=true。
func ExtractFingerprintFromExtra(extra map[string]any, fallbackSeed string) (NodeFingerprint, bool) {
	if extra != nil {
		if raw, ok := extra["fingerprint"]; ok && raw != nil {
			b, err := json.Marshal(raw)
			if err == nil {
				var fp NodeFingerprint
				if json.Unmarshal(b, &fp) == nil && fp.FingerprintID != "" {
					return NormalizeNodeFingerprint(fp, fallbackSeed), false
				}
			}
		}
	}
	return GenerateNodeFingerprint(fallbackSeed), true
}

// ParseStrictIsolationFromExtra 判断节点是否启用「1容器:1IP:1指纹」强隔离（默认开启）。
func ParseStrictIsolationFromExtra(extra map[string]any) bool {
	if extra == nil {
		return true
	}
	v, ok := extra["strict_isolation"]
	if !ok || v == nil {
		return true
	}
	switch tv := v.(type) {
	case bool:
		return tv
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(tv))
		return trimmed != "false" && trimmed != "0" && trimmed != "off"
	default:
		return true
	}
}
