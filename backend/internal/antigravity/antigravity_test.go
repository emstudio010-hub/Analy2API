package antigravity

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recordedCmd struct {
	Name string
	Args []string
}

type mockRunner struct {
	mu      sync.Mutex
	calls   []recordedCmd
	handler func(ctx context.Context, name string, args ...string) (string, string, error)
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cp := make([]string, len(args))
	copy(cp, args)
	m.mu.Lock()
	m.calls = append(m.calls, recordedCmd{Name: name, Args: cp})
	m.mu.Unlock()
	if m.handler != nil {
		return m.handler(ctx, name, cp...)
	}
	return "", "", nil
}

func (m *mockRunner) snapshotCalls() []recordedCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]recordedCmd, len(m.calls))
	copy(out, m.calls)
	return out
}

func TestDetectDocker(t *testing.T) {
	t.Run("available", func(t *testing.T) {
		mgr := NewDockerManager(CommandRunnerFunc(func(_ context.Context, name string, args ...string) (string, string, error) {
			if name != "docker" || len(args) < 3 || args[0] != "version" {
				t.Fatalf("unexpected command: %s %v", name, args)
			}
			return "27.5.1|linux\n", "", nil
		}))
		st := mgr.DetectDocker(context.Background())
		if !st.Available || st.Version != "27.5.1" || st.ServerOS != "linux" || st.Error != "" {
			t.Fatalf("unexpected status: %+v", st)
		}
	})

	t.Run("not_installed", func(t *testing.T) {
		mgr := NewDockerManager(CommandRunnerFunc(func(_ context.Context, _ string, _ ...string) (string, string, error) {
			return "", "", exec.ErrNotFound
		}))
		st := mgr.DetectDocker(context.Background())
		if st.Available || st.Error == "" {
			t.Fatalf("expected unavailable status with readable error, got: %+v", st)
		}
	})

	t.Run("daemon_down_and_panic_safe", func(t *testing.T) {
		mgr := NewDockerManager(CommandRunnerFunc(func(_ context.Context, _ string, _ ...string) (string, string, error) {
			panic("simulated runner crash")
		}))
		st := mgr.DetectDocker(context.Background())
		if st.Available || !strings.Contains(st.Error, "simulated runner crash") {
			t.Fatalf("expected panic recovery in DetectDocker, got: %+v", st)
		}
	})
}

func TestWriteNodeConfigFile(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "node-1", "auths")

	spec := NodeSpec{
		ID:            1,
		Name:          "local-main",
		Mode:          ModeLocalDocker,
		AuthDir:       authDir, // ConfigDir 留空，测试自动回退 filepath.Dir(AuthDir)
		APIKey:        "sk-agy-infer-key",
		ManagementKey: "mgmt-secret-123",
		ProxyURL:      "http://user:pass@10.0.0.2:7890",
	}

	cfgPath, err := WriteNodeConfigFile(spec)
	if err != nil {
		t.Fatalf("WriteNodeConfigFile failed: %v", err)
	}

	expectedCfgPath, _ := filepath.Abs(filepath.Join(root, "node-1", "config.yaml"))
	if cfgPath != expectedCfgPath {
		t.Fatalf("config path mismatch: got %q, want %q", cfgPath, expectedCfgPath)
	}

	if st, err := os.Stat(authDir); err != nil || !st.IsDir() {
		t.Fatalf("expected authDir %q to be auto-created: %v", authDir, err)
	}

	rawBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.yaml failed: %v", err)
	}
	content := string(rawBytes)
	for _, want := range []string{
		"port: 8317",
		`auth-dir: "/app/auths"`,
		`- "sk-agy-infer-key"`,
		"allow-remote: true",
		`secret-key: "mgmt-secret-123"`,
		`proxy-url: "http://user:pass@10.0.0.2:7890"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config.yaml missing %q\nfull content:\n%s", want, content)
		}
	}

	// 非法代理地址应报错拒绝
	badSpec := spec
	badSpec.ProxyURL = "ftp://bad-proxy:21"
	if _, err := WriteNodeConfigFile(badSpec); err == nil {
		t.Fatalf("expected invalid proxy URL to fail")
	}
}

func TestDockerContainerLifecycleAndSafety(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	cfgDir := filepath.Join(root, "cfg")

	containerExists := false
	runner := &mockRunner{}
	runner.handler = func(_ context.Context, name string, args ...string) (string, string, error) {
		if name != "docker" || len(args) == 0 {
			return "", "", fmt.Errorf("unexpected binary: %s", name)
		}
		switch args[0] {
		case "pull":
			return "Status: Downloaded newer image", "", nil
		case "inspect":
			if !containerExists {
				return "", "Error: No such object: agy-test", errors.New("exit status 1")
			}
			return "abcdef1234567890|running|true|2026-11-18T00:00:00Z|0\n", "", nil
		case "rm":
			containerExists = false
			return "agy-test\n", "", nil
		case "run":
			containerExists = true
			return "abcdef1234567890\n", "", nil
		case "stop", "restart", "start":
			return "agy-test\n", "", nil
		case "logs":
			return "line-stdout-1\n", "line-stderr-2\n", nil
		default:
			return "", "", fmt.Errorf("unhandled subcommand %q", args[0])
		}
	}

	mgr := NewDockerManager(runner)

	if err := mgr.PullImage(context.Background(), ""); err != nil {
		t.Fatalf("PullImage failed: %v", err)
	}

	spec := NodeSpec{
		ID:            7,
		ContainerName: "agy-test",
		DockerImage:   "routerforme/cliproxyapi:latest",
		HostIP:        "127.0.0.1",
		HostPort:      18317,
		AuthDir:       authDir,
		ConfigDir:     cfgDir,
		ManagementKey: "mgmt-k",
		APIKey:        "api-k",
	}

	// 第一次创建（容器尚不存在）
	cid, err := mgr.EnsureAndStartContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("first EnsureAndStartContainer failed: %v", err)
	}
	if cid != "abcdef1234567890" {
		t.Fatalf("unexpected container ID: %q", cid)
	}

	// 第二次启动（同名容器已存在，应先触发 docker rm -f 再 docker run）
	cid2, err := mgr.EnsureAndStartContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("second EnsureAndStartContainer failed: %v", err)
	}
	if cid2 != "abcdef1234567890" {
		t.Fatalf("unexpected container ID on recreate: %q", cid2)
	}

	absAuth, _ := filepath.Abs(authDir)
	absCfg, _ := filepath.Abs(filepath.Join(cfgDir, "config.yaml"))

	calls := runner.snapshotCalls()
	var sawRmBeforeSecondRun bool
	var runArgs []string
	rmCount := 0
	for _, c := range calls {
		if len(c.Args) > 0 && c.Args[0] == "rm" {
			rmCount++
			sawRmBeforeSecondRun = true
		}
		if len(c.Args) > 0 && c.Args[0] == "run" {
			runArgs = c.Args
		}
	}
	if !sawRmBeforeSecondRun || rmCount != 1 {
		t.Fatalf("expected existing container to be removed once before second run, calls=%+v", calls)
	}

	joinedRun := strings.Join(runArgs, " ")
	for _, mustHave := range []string{
		"--name agy-test",
		"--restart unless-stopped",
		"-p 127.0.0.1:18317:8317",
		fmt.Sprintf("-v %s:/app/auths", absAuth),
		fmt.Sprintf("-v %s:/app/config.yaml", absCfg),
		"routerforme/cliproxyapi:latest",
	} {
		if !strings.Contains(joinedRun, mustHave) {
			t.Errorf("docker run args missing %q\ngot: %s", mustHave, joinedRun)
		}
	}

	st, err := mgr.InspectContainer(context.Background(), "agy-test")
	if err != nil || !st.Exists || !st.Running || st.State != "running" {
		t.Fatalf("InspectContainer unexpected result: %+v, err=%v", st, err)
	}

	logs, err := mgr.ContainerLogs(context.Background(), "agy-test", 50)
	if err != nil {
		t.Fatalf("ContainerLogs failed: %v", err)
	}
	if !strings.Contains(logs, "line-stdout-1") || !strings.Contains(logs, "line-stderr-2") {
		t.Fatalf("expected combined stdout+stderr logs, got: %q", logs)
	}

	if err := mgr.RestartContainer(context.Background(), "agy-test"); err != nil {
		t.Fatalf("RestartContainer failed: %v", err)
	}
	if err := mgr.StopContainer(context.Background(), "agy-test"); err != nil {
		t.Fatalf("StopContainer failed: %v", err)
	}
	if err := mgr.RemoveContainer(context.Background(), "agy-test"); err != nil {
		t.Fatalf("RemoveContainer failed: %v", err)
	}

	stAfterRm, err := mgr.InspectContainer(context.Background(), "agy-test")
	if err != nil || stAfterRm.Exists {
		t.Fatalf("expected Exists=false after RemoveContainer, got %+v, err=%v", stAfterRm, err)
	}

	// 参数注入防护校验
	if err := mgr.StopContainer(context.Background(), "--privileged"); err == nil {
		t.Fatalf("expected flag injection container name to fail")
	}
	if err := mgr.PullImage(context.Background(), "--evil-flag"); err == nil {
		t.Fatalf("expected flag injection image name to fail")
	}
}

func TestLocalAuthFileManagementAndPathTraversal(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "auths")

	// 1. 测试路径穿越与后缀校验拦截
	maliciousNames := []string{
		"../escape.json",
		"..\\escape.json",
		"nested/evil.json",
		"nested\\evil.json",
		"C:\\evil.json",
		".hidden.json",
		"-flag.json",
		"not-json.txt",
		".json",
		"",
	}
	for _, bad := range maliciousNames {
		if _, err := SaveLocalAuthFile(authDir, bad, []byte(`{"type":"antigravity"}`)); err == nil {
			t.Errorf("expected SaveLocalAuthFile to reject unsafe filename %q", bad)
		}
		if err := DeleteLocalAuthFile(authDir, bad); err == nil {
			t.Errorf("expected DeleteLocalAuthFile to reject unsafe filename %q", bad)
		}
	}

	// 非法 JSON 拦截
	for _, badBody := range [][]byte{
		[]byte(``),
		[]byte(`not json`),
		[]byte(`["array-not-object"]`),
		[]byte(`{"a":1}{"b":2}`),
	} {
		if _, err := SaveLocalAuthFile(authDir, "valid.json", badBody); err == nil {
			t.Errorf("expected invalid JSON body %q to be rejected", string(badBody))
		}
	}

	// 2. 测试多种 OAuth 凭证归一化（AntiGravity + Codex JWT + Claude 过期时间）
	futureExp := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	agyPayload := fmt.Sprintf(`{
		"type": "antigravity",
		"email": "dev@example.com",
		"project_id": "agy-cloud-01",
		"access_token": "ya29.mock",
		"refresh_token": "1//mock",
		"expiry": %q,
		"priority": 20,
		"excluded-models": ["gemini-2.5-flash", "claude-haiku-4-5"],
		"disabled": false
	}`, futureExp)

	savedAgy, err := SaveLocalAuthFile(authDir, "antigravity-dev@example.com.json", []byte(agyPayload))
	if err != nil {
		t.Fatalf("SaveLocalAuthFile agy failed: %v", err)
	}
	if savedAgy.Provider != "antigravity" || savedAgy.Email != "dev@example.com" ||
		savedAgy.ProjectID != "agy-cloud-01" || savedAgy.Priority != 20 || savedAgy.Expired ||
		len(savedAgy.ExcludedModels) != 2 || savedAgy.Source != SourceLocalDir {
		t.Fatalf("unexpected savedAgy: %+v", savedAgy)
	}

	// 构造含 JWT id_token 的 Codex 凭证（无顶层 email，验证从 JWT 提取）
	jwtClaims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"codex-user@openai.example"}`))
	mockJWT := "eyJhbGciOiJSUzI1NiJ9." + jwtClaims + ".sig"
	pastUnix := time.Now().Add(-1 * time.Hour).Unix()
	codexPayload := fmt.Sprintf(`{
		"provider": "openai",
		"tokens": {
			"access_token": "eyJ-access",
			"refresh_token": "rt-codex",
			"id_token": %q,
			"expires_at": %d
		},
		"priority": 5,
		"disabled": true
	}`, mockJWT, pastUnix)

	savedCodex, err := SaveLocalAuthFile(authDir, "codex-team.json", []byte(codexPayload))
	if err != nil {
		t.Fatalf("SaveLocalAuthFile codex failed: %v", err)
	}
	if savedCodex.Provider != "codex" || savedCodex.Email != "codex-user@openai.example" ||
		!savedCodex.Expired || !savedCodex.Disabled {
		t.Fatalf("unexpected savedCodex: %+v", savedCodex)
	}

	list, err := ListLocalAuthFiles(authDir)
	if err != nil {
		t.Fatalf("ListLocalAuthFiles failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 local auth files, got %d", len(list))
	}
	// Priority=20 的应排在前面
	if list[0].Filename != "antigravity-dev@example.com.json" {
		t.Fatalf("expected higher priority file first, got %s", list[0].Filename)
	}

	if err := DeleteLocalAuthFile(authDir, "codex-team.json"); err != nil {
		t.Fatalf("DeleteLocalAuthFile failed: %v", err)
	}
	afterDel, err := ListLocalAuthFiles(authDir)
	if err != nil || len(afterDel) != 1 {
		t.Fatalf("expected 1 file after delete, got %d (err=%v)", len(afterDel), err)
	}
}

func TestBaseURLAndLoopbackDetection(t *testing.T) {
	cases := []struct {
		spec     NodeSpec
		wantBase string
		loopback bool
	}{
		{NodeSpec{}, "http://127.0.0.1:8317", true},
		{NodeSpec{HostIP: "localhost", HostPort: 9317}, "http://localhost:9317", true},
		{NodeSpec{HostIP: "::1", HostPort: 8317}, "http://[::1]:8317", true},
		{NodeSpec{HostIP: "http://127.0.0.1:18317", HostPort: 8317}, "http://127.0.0.1:18317", true},
		{NodeSpec{HostIP: "https://agy.example.com", HostPort: 0}, "https://agy.example.com", false},
		{NodeSpec{HostIP: "https://agy.example.com", HostPort: 8443}, "https://agy.example.com:8443", false},
		{NodeSpec{HostIP: "http://192.168.10.25", HostPort: 8317}, "http://192.168.10.25:8317", false},
	}

	for _, tc := range cases {
		got := BaseURL(tc.spec)
		if got != tc.wantBase {
			t.Errorf("BaseURL(%+v) = %q, want %q", tc.spec, got, tc.wantBase)
		}
		if lo := IsLoopbackTarget(got); lo != tc.loopback {
			t.Errorf("IsLoopbackTarget(%q) = %v, want %v", got, lo, tc.loopback)
		}
	}
}

func TestClientManagementOAuthModelsAndChatStream(t *testing.T) {
	const (
		mgmtKey = "mgmt-top-secret"
		apiKey  = "sk-node-api-key"
	)

	var (
		mu          sync.Mutex
		remoteFiles = map[string]map[string]any{
			"antigravity-alice@example.com.json": {
				"name":       "antigravity-alice@example.com.json",
				"provider":   "antigravity",
				"email":      "alice@example.com",
				"project_id": "proj-alice",
				"priority":   10,
			},
		}
		putFallbackUsed atomic.Bool
	)

	checkMgmtAuth := func(w http.ResponseWriter, r *http.Request) bool {
		xKey := r.Header.Get("X-Management-Key")
		bearer := r.Header.Get("Authorization")
		if xKey != mgmtKey || bearer != "Bearer "+mgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid management key"}}`))
			return false
		}
		return true
	}

	checkAPIAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return false
		}
		return true
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files":
			if !checkMgmtAuth(w, r) {
				return
			}
			switch r.Method {
			case http.MethodGet:
				mu.Lock()
				list := make([]map[string]any, 0, len(remoteFiles))
				for _, v := range remoteFiles {
					list = append(list, v)
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"files": list})
			case http.MethodPost:
				// 故意对 claude 文件返回 405，验证客户端自动回退 PUT
				name := r.URL.Query().Get("name")
				if strings.HasPrefix(name, "claude-") {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				raw, _ := io.ReadAll(r.Body)
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				m["name"] = name
				mu.Lock()
				remoteFiles[name] = m
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case http.MethodPut:
				putFallbackUsed.Store(true)
				name := r.URL.Query().Get("name")
				raw, _ := io.ReadAll(r.Body)
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				m["name"] = name
				mu.Lock()
				remoteFiles[name] = m
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case http.MethodDelete:
				name := r.URL.Query().Get("name")
				mu.Lock()
				delete(remoteFiles, name)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"deleted"}`))
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}

		case r.URL.Path == "/v8/management/oauth/auth-url":
			if !checkMgmtAuth(w, r) {
				return
			}
			prov := r.URL.Query().Get("provider")
			if prov == "antigravity" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"auth_url":"https://accounts.google.com/o/oauth2/v2/auth?state=agy-123","state":"agy-123"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)

		case r.URL.Path == "/v0/management/oauth/auth-url":
			if !checkMgmtAuth(w, r) {
				return
			}
			prov := r.URL.Query().Get("provider")
			if prov == "codex" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"url":"https://auth.openai.com/authorize?state=codex-456"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)

		case r.URL.Path == "/claude-auth-url":
			if !checkMgmtAuth(w, r) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"url":"https://claude.ai/oauth/authorize?state=claude-789","state":"claude-789"}}`))

		case r.URL.Path == "/v1/models":
			if !checkAPIAuth(w, r) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"object": "list",
				"data": [
					{"id": "gemini-2.5-pro", "owned_by": "google"},
					{"id": "claude-sonnet-4-6", "owned_by": "anthropic"},
					{"id": "gpt-5.3-codex", "owned_by": "openai"},
					{"id": "text-embedding-3-small", "owned_by": "openai"}
				]
			}`))

		case r.URL.Path == "/v1/chat/completions":
			if !checkAPIAuth(w, r) {
				return
			}
			rawReq, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(rawReq, &payload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// 验证客户端强制注入了 stream:true 与 stream_options.include_usage:true
			if streamVal, ok := payload["stream"].(bool); !ok || !streamVal {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"stream must be true"}`))
				return
			}
			so, _ := payload["stream_options"].(map[string]any)
			if incUsage, ok := so["include_usage"].(bool); !ok || !incUsage {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"include_usage must be true"}`))
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())
	localAuthDir := filepath.Join(t.TempDir(), "auths")

	// 预置一份仅在宿主机本地存在的 JSON 文件，验证远程+本地合并
	if _, err := SaveLocalAuthFile(localAuthDir, "gemini-local@example.com.json", []byte(`{"type":"gemini","email":"local@example.com"}`)); err != nil {
		t.Fatalf("seed local auth file failed: %v", err)
	}

	cli := NewClient("", nil)
	spec := NodeSpec{
		ID:            1,
		Mode:          ModeLocalDocker,
		HostIP:        u.Hostname(),
		HostPort:      port,
		AuthDir:       localAuthDir,
		ManagementKey: mgmtKey,
		APIKey:        apiKey,
	}

	ctx := context.Background()

	// 1. ListAuthFiles 合并远程 + 本地
	files, err := cli.ListAuthFiles(ctx, spec)
	if err != nil {
		t.Fatalf("ListAuthFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 merged auth files, got %d: %+v", len(files), files)
	}

	// 2. UploadAuthFile（含 PUT 405 回退验证）
	uploaded, err := cli.UploadAuthFile(ctx, spec, "claude-pro@example.com.json", []byte(`{"type":"claude","email":"pro@example.com","priority":30}`))
	if err != nil {
		t.Fatalf("UploadAuthFile failed: %v", err)
	}
	if !putFallbackUsed.Load() {
		t.Fatalf("expected PUT fallback to be triggered on 405")
	}
	if uploaded.Provider != "claude" || uploaded.Email != "pro@example.com" || uploaded.Source != SourceRemoteAPI {
		t.Fatalf("unexpected uploaded info: %+v", uploaded)
	}
	if _, err := os.Stat(filepath.Join(localAuthDir, "claude-pro@example.com.json")); err != nil {
		t.Fatalf("expected uploaded file to also persist in localAuthDir: %v", err)
	}

	// 3. DeleteAuthFile 同步删除本地与远程
	if err := cli.DeleteAuthFile(ctx, spec, "claude-pro@example.com.json"); err != nil {
		t.Fatalf("DeleteAuthFile failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localAuthDir, "claude-pro@example.com.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected local file to be deleted, stat err=%v", err)
	}

	// 4. RequestOAuthURL 三级回退探测
	oauthAgy, err := cli.RequestOAuthURL(ctx, spec, "antigravity", "")
	if err != nil || oauthAgy.State != "agy-123" || !strings.Contains(oauthAgy.AuthURL, "accounts.google.com") {
		t.Fatalf("RequestOAuthURL antigravity failed: %+v, err=%v", oauthAgy, err)
	}
	oauthCodex, err := cli.RequestOAuthURL(ctx, spec, "codex", "")
	if err != nil || oauthCodex.State != "codex-456" {
		t.Fatalf("RequestOAuthURL codex (/v0 fallback) failed: %+v, err=%v", oauthCodex, err)
	}
	oauthClaude, err := cli.RequestOAuthURL(ctx, spec, "claude", "")
	if err != nil || oauthClaude.State != "claude-789" {
		t.Fatalf("RequestOAuthURL claude (/<provider>-auth-url fallback) failed: %+v, err=%v", oauthClaude, err)
	}

	// 5. Models 拉取与能力推断 + DefaultModels 校验
	models, err := cli.Models(ctx, spec)
	if err != nil {
		t.Fatalf("Models failed: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 chat models (embedding filtered out), got %d: %+v", len(models), models)
	}
	for _, m := range models {
		if !m.SupportsReasoning || !m.SupportsImages || !m.SupportsToolCall {
			t.Errorf("expected flagship model %s to infer reasoning/images/tools=true, got %+v", m.ID, m)
		}
	}
	defaults := DefaultModels()
	if len(defaults) < 10 {
		t.Fatalf("expected comprehensive DefaultModels, got %d", len(defaults))
	}

	// 6. ProbeNode 连通性与计数
	lat, mCount, aCount, err := cli.ProbeNode(ctx, spec)
	if err != nil || mCount != 3 || aCount != 2 || lat < 0 {
		t.Fatalf("ProbeNode unexpected: lat=%v models=%d auths=%d err=%v", lat, mCount, aCount, err)
	}

	// 7. ChatStream 强制流式与 usage 选项并读取完整 SSE
	resp, err := cli.ChatStream(ctx, spec, []byte(`{"model":"gemini-2.5-pro","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}
	defer resp.Body.Close()

	var frames []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "data:") {
			frames = append(frames, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(frames) != 3 || frames[2] != "[DONE]" {
		t.Fatalf("unexpected SSE frames: %+v", frames)
	}

	// 8. 容器未启动时本机模式优雅回退本地目录
	offlineSpec := spec
	offlineSpec.HostPort = 1 // 不可达端口
	fallbackFiles, err := cli.ListAuthFiles(ctx, offlineSpec)
	if err != nil || len(fallbackFiles) != 1 || fallbackFiles[0].Source != SourceLocalDir {
		t.Fatalf("expected offline local fallback to return 1 local file, got %+v, err=%v", fallbackFiles, err)
	}
}

func TestLoopbackBypassAndRemoteProxyRouting(t *testing.T) {
	// 1. 构造本机模拟容器节点（监听 127.0.0.1）
	localContainer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gemini-2.5-flash"}]}`))
	}))
	defer localContainer.Close()

	lu, _ := url.Parse(localContainer.URL)
	localPort, _ := strconv.Atoi(lu.Port())

	// 故意将全局默认代理和节点 ProxyURL 都指向死端口 127.0.0.1:1！
	// 若客户端未对 127.0.0.1 / localhost 执行直连绕过，请求必定因连接死代理失败。
	cli := NewClient("http://127.0.0.1:1", nil)
	loopbackSpec := NodeSpec{
		Mode:     ModeLocalDocker,
		HostIP:   "localhost",
		HostPort: localPort,
		APIKey:   "k",
		ProxyURL: "http://127.0.0.1:1",
	}

	_, bypassed, err := cli.HTTPClientForNode(loopbackSpec)
	if err != nil || !bypassed {
		t.Fatalf("expected HTTPClientForNode to report bypassed=true for localhost, got bypassed=%v err=%v", bypassed, err)
	}

	items, err := cli.Models(context.Background(), loopbackSpec)
	if err != nil || len(items) != 1 || items[0].ID != "gemini-2.5-flash" {
		t.Fatalf("expected loopback request to bypass dead proxy and succeed, got items=%+v err=%v", items, err)
	}

	// 2. 验证非回环远程节点（如 remote-node.example.internal）严格经由节点配置的 HTTP 代理转发
	var proxyHitCount atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHitCount.Add(1)
		if r.Host != "remote-node.example.internal:8317" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"claude-opus-4-6"}]}`))
	}))
	defer proxySrv.Close()

	// 确保代理监听在有效地址
	if _, _, err := net.SplitHostPort(strings.TrimPrefix(proxySrv.URL, "http://")); err != nil {
		t.Fatalf("unexpected proxy URL: %s", proxySrv.URL)
	}

	remoteSpec := NodeSpec{
		Mode:     ModeRemoteAPI,
		HostIP:   "remote-node.example.internal",
		HostPort: 8317,
		APIKey:   "rk",
		ProxyURL: proxySrv.URL,
	}

	_, remoteBypassed, err := cli.HTTPClientForNode(remoteSpec)
	if err != nil || remoteBypassed {
		t.Fatalf("expected non-loopback remote node NOT to bypass proxy, got bypassed=%v err=%v", remoteBypassed, err)
	}

	remoteItems, err := cli.Models(context.Background(), remoteSpec)
	if err != nil || len(remoteItems) != 1 || remoteItems[0].ID != "claude-opus-4-6" {
		t.Fatalf("expected remote node request through proxy to succeed, got items=%+v err=%v", remoteItems, err)
	}
	if proxyHitCount.Load() != 1 {
		t.Fatalf("expected proxy server to be hit once, got %d", proxyHitCount.Load())
	}
}

func TestPackageWrappersEdgeCasesAndModelInference(t *testing.T) {
	// 1. NodeSpec & APIError 辅助方法测试
	spec := NodeSpec{ID: 42, Name: "My Prod Node!", Mode: "remote"}
	if spec.NormalizedMode() != ModeRemoteAPI || spec.IsLocalDocker() {
		t.Fatalf("expected remote_api mode, got %s", spec.NormalizedMode())
	}
	if got := spec.EffectiveContainerName(); got != "cmd2api-agy-42" {
		t.Fatalf("unexpected EffectiveContainerName: %q", got)
	}
	specNoID := NodeSpec{Name: "  @#Node_Alpha-- "}
	if got := specNoID.EffectiveContainerName(); got != "cmd2api-agy-Node_Alpha" {
		t.Fatalf("unexpected sanitized name: %q", got)
	}

	apiErr := &APIError{Status: http.StatusTooManyRequests, Code: "rate_limit", Message: "quota exceeded"}
	if !apiErr.IsRateLimited() || apiErr.IsAuthFailure() || apiErr.IsNotFound() || !strings.Contains(apiErr.Error(), "429") {
		t.Fatalf("unexpected APIError behavior: %+v (%s)", apiErr, apiErr.Error())
	}
	authErr := &APIError{Status: http.StatusUnauthorized, Raw: "unauthorized"}
	if !authErr.IsAuthFailure() {
		t.Fatalf("expected 401 IsAuthFailure=true")
	}
	notFoundErr := &APIError{Status: http.StatusNotFound}
	if !notFoundErr.IsNotFound() {
		t.Fatalf("expected 404 IsNotFound=true")
	}

	// 2. 代理辅助透传函数
	if err := ValidateProxyURL("socks5://user:secret@127.0.0.1:1080"); err != nil {
		t.Fatalf("ValidateProxyURL socks5 failed: %v", err)
	}
	if redacted := RedactProxyURL("http://admin:supersecret@10.0.0.1:8080"); strings.Contains(redacted, "supersecret") {
		t.Fatalf("RedactProxyURL leaked password: %q", redacted)
	}

	// 3. 包级默认 Docker 委托函数与 SetDefaultCommandRunner
	origMgr := DefaultDockerManager()
	defer func() {
		defaultDockerMgrMu.Lock()
		defaultDockerMgr = origMgr
		defaultDockerMgrMu.Unlock()
	}()

	SetDefaultCommandRunner(CommandRunnerFunc(func(_ context.Context, _ string, args ...string) (string, string, error) {
		if len(args) == 0 {
			return "", "", nil
		}
		switch args[0] {
		case "version":
			return "26.1.0|windows\n", "", nil
		case "inspect":
			return "11223344|exited|false|0001-01-01T00:00:00Z|137\n", "", nil
		case "logs":
			return "", "only-stderr-log\n", nil
		case "run":
			return "11223344\n", "", nil
		default:
			return "ok\n", "", nil
		}
	}))

	env := DetectDocker(context.Background())
	if !env.Available || env.ServerOS != "windows" {
		t.Fatalf("package DetectDocker unexpected: %+v", env)
	}
	if err := PullImage(context.Background(), "routerforme/cliproxyapi:latest"); err != nil {
		t.Fatalf("package PullImage failed: %v", err)
	}
	if err := StartContainer(context.Background(), "agy-pkg"); err != nil {
		t.Fatalf("package StartContainer failed: %v", err)
	}
	if err := StopContainer(context.Background(), "agy-pkg"); err != nil {
		t.Fatalf("package StopContainer failed: %v", err)
	}
	if err := RestartContainer(context.Background(), "agy-pkg"); err != nil {
		t.Fatalf("package RestartContainer failed: %v", err)
	}
	if err := RemoveContainer(context.Background(), "agy-pkg"); err != nil {
		t.Fatalf("package RemoveContainer failed: %v", err)
	}
	ins, err := InspectContainer(context.Background(), "agy-pkg")
	if err != nil || !ins.Exists || ins.Running || ins.StartedAt != "" || !strings.Contains(ins.StatusText, "exit 137") {
		t.Fatalf("package InspectContainer unexpected: %+v, err=%v", ins, err)
	}
	logOut, err := ContainerLogs(context.Background(), "agy-pkg", 0)
	if err != nil || !strings.Contains(logOut, "only-stderr-log") {
		t.Fatalf("package ContainerLogs unexpected: %q, err=%v", logOut, err)
	}

	tmpDir := t.TempDir()
	pkgSpec := NodeSpec{
		Name:      "pkg-node",
		ConfigDir: tmpDir, // AuthDir 留空，验证自动创建 ConfigDir/auths
		HostIP:    "::1",
		HostPort:  18318,
		ProxyURL:  "direct",
	}
	if cid, err := EnsureAndStartContainer(context.Background(), pkgSpec); err != nil || cid != "11223344" {
		t.Fatalf("package EnsureAndStartContainer failed: cid=%q err=%v", cid, err)
	}

	// 4. ReadLocalAuthFileRaw 与损坏 JSON 容错
	authsPath := filepath.Join(tmpDir, "auths")
	if _, err := SaveLocalAuthFile(authsPath, "qwen-user.JSON", []byte(`{"type":"qwen","excluded_models":"qwen-turbo, qwen-plus","expired":"2099-01-01 00:00:00"}`)); err != nil {
		t.Fatalf("SaveLocalAuthFile uppercase .JSON failed: %v", err)
	}
	rawRead, err := ReadLocalAuthFileRaw(authsPath, "qwen-user.json")
	if err != nil || !strings.Contains(string(rawRead), "qwen") {
		t.Fatalf("ReadLocalAuthFileRaw failed: %v", err)
	}
	// 写入一个损坏的 .json，验证 ListLocalAuthFiles 不崩溃并标记 Disabled=true
	_ = os.WriteFile(filepath.Join(authsPath, "corrupted.json"), []byte(`{broken`), 0o600)
	listed, err := ListLocalAuthFiles(authsPath)
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListLocalAuthFiles with corrupted file unexpected: %+v, err=%v", listed, err)
	}

	// 5. 多厂商模型推断 + SubmitOAuthCallback + 多种 auth-files 包装格式 + ChatStream 错误映射
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/auth-files":
			if r.Method == http.MethodGet {
				// 测试字典包装格式 {"data": {"files": {"iflow-1.json": {...}}}}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"files":{"iflow-1.json":{"provider":"iflow","email":"u@iflow.example"}}}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"file":{"provider":"iflow"}}`))
		case "/v0/management/oauth-callback":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"saved"}`))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"id":"deepseek-r1"},
				{"id":"qwen3-coder-vl"},
				{"id":"glm-4.6"},
				{"id":"kimi-k2"},
				{"id":"grok-3-thinking"},
				{"id":"gpt-4.1"},
				{"id":"o1-mini"}
			]`))
		case "/v1/chat/completions":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"quota_exhausted","message":"rate limit hit"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	tsu, _ := url.Parse(ts.URL)
	tsPort, _ := strconv.Atoi(tsu.Port())
	remoteOnlySpec := NodeSpec{
		Mode:          ModeRemoteAPI,
		HostIP:        tsu.Hostname(),
		HostPort:      tsPort,
		ManagementKey: "mk",
		APIKey:        "ak",
	}

	cli := NewClientWithPool(nil, nil)
	cbRes, err := cli.SubmitOAuthCallback(context.Background(), remoteOnlySpec, "antigravity", "http://localhost:1455/callback?code=123")
	if err != nil || cbRes["status"] != "saved" {
		t.Fatalf("SubmitOAuthCallback failed: %+v, err=%v", cbRes, err)
	}

	keyedFiles, err := ListAuthFiles(context.Background(), remoteOnlySpec)
	if err != nil || len(keyedFiles) != 1 || keyedFiles[0].Filename != "iflow-1.json" {
		t.Fatalf("keyed map auth-files decode failed: %+v, err=%v", keyedFiles, err)
	}

	inferredModels, err := Models(context.Background(), remoteOnlySpec)
	if err != nil || len(inferredModels) != 7 {
		t.Fatalf("Models array format failed: %+v, err=%v", inferredModels, err)
	}

	_, chatErr := ChatStream(context.Background(), remoteOnlySpec, []byte(`{"model":"deepseek-r1","stream_options":{"foo":"bar"},"messages":[]}`))
	var mapped *APIError
	if !errors.As(chatErr, &mapped) || !mapped.IsRateLimited() || mapped.Code != "quota_exhausted" {
		t.Fatalf("expected structured 429 APIError from ChatStream, got: %v", chatErr)
	}
}

