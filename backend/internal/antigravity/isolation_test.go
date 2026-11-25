package antigravity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeFingerprintIsolationAndHeaders(t *testing.T) {
	fp1 := GenerateNodeFingerprint("node-1")
	fp2 := GenerateNodeFingerprint("node-2")

	if fp1.FingerprintID == "" || fp1.Qimei36 == "" || fp1.MachineID == "" || fp1.Hostname == "" || fp1.MACAddress == "" || fp1.ClientID == "" {
		t.Fatalf("生成的节点设备指纹不完整: %+v", fp1)
	}
	if fp1.FingerprintID == fp2.FingerprintID || fp1.Qimei36 == fp2.Qimei36 || fp1.MachineID == fp2.MachineID || fp1.Hostname == fp2.Hostname {
		t.Fatalf("不同容器节点不应生成相同设备指纹: fp1=%+v fp2=%+v", fp1, fp2)
	}

	// 验证 Docker 创建命令中注入了 --hostname / 隔离标签 / 环境变量与代理重写
	runner := &mockRunner{}
	runner.handler = func(_ context.Context, name string, args ...string) (string, string, error) {
		switch args[0] {
		case "inspect":
			return "", "Error: No such object", errMockNotFound
		case "run":
			return "cid-iso-001\n", "", nil
		default:
			return "", "", nil
		}
	}

	mgr := NewDockerManager(runner)
	root := t.TempDir()
	spec := NodeSpec{
		ID:              11,
		Name:            "iso-node-1",
		Mode:            ModeLocalDocker,
		ContainerName:   "agy-iso-11",
		HostIP:          "127.0.0.1",
		HostPort:        18321,
		AuthDir:         filepath.Join(root, "auths"),
		ConfigDir:       filepath.Join(root, "cfg"),
		APIKey:          "sk-iso",
		ManagementKey:   "mgmt-iso",
		ProxyURL:        "http://127.0.0.1:7890",
		StrictIsolation: true,
		Fingerprint:     fp1,
	}

	cid, err := mgr.EnsureAndStartContainer(context.Background(), spec)
	if err != nil || cid != "cid-iso-001" {
		t.Fatalf("EnsureAndStartContainer 失败: cid=%q err=%v", cid, err)
	}

	var runArgs string
	for _, c := range runner.snapshotCalls() {
		if len(c.Args) > 0 && c.Args[0] == "run" {
			runArgs = strings.Join(c.Args, " ")
		}
	}
	for _, want := range []string{
		"--hostname " + fp1.Hostname,
		"AGY_FINGERPRINT_ID=" + fp1.FingerprintID,
		"AGY_MACHINE_ID=" + fp1.MachineID,
		"AGY_QIMEI36=" + fp1.Qimei36,
		"AGY_CLIENT_ID=" + fp1.ClientID,
		"ANTIGRAVITY_USER_AGENT=" + fp1.UserAgent,
		"AGY_SINGLE_CLIENT=1",
		"HTTP_PROXY=http://host.docker.internal:7890",
	} {
		if !strings.Contains(runArgs, want) {
			t.Fatalf("docker run 缺少强隔离参数 %q\nactual: %s", want, runArgs)
		}
	}

	// 验证推理出站请求头严格使用真实官方 AntiGravity CLI User-Agent，且绝不泄漏 X-Qimei36 等自定义头
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gemini-2.5-flash"}]}`))
	}))
	defer srv.Close()

	cli := NewClient("", nil)
	spec.HostIP = srv.URL
	if _, err := cli.Models(context.Background(), spec); err != nil {
		t.Fatalf("Models 调用失败: %v", err)
	}
	ua := gotHeaders.Get("User-Agent")
	if !strings.HasPrefix(ua, "antigravity/") || strings.Contains(ua, "fp_") || strings.Contains(ua, "CLIProxyAPI") {
		t.Fatalf("出站推理 User-Agent 必须为真实官方 AntiGravity CLI 格式，实际为 %q", ua)
	}
	for _, forbidden := range []string{"X-Qimei36", "X-Machine-Id", "X-Device-Fingerprint", "X-Client-Id"} {
		if v := gotHeaders.Get(forbidden); v != "" {
			t.Fatalf("推理请求严禁泄漏非官方头 %s=%q", forbidden, v)
		}
	}
}

var errMockNotFound = context.DeadlineExceeded
