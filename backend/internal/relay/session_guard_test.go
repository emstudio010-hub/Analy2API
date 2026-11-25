package relay

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cmd2api/internal/config"
	"cmd2api/internal/domain"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"
)

func TestSessionGuardNonQuota429PausesAndDumpsLog(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	logDir := t.TempDir()

	var upstreamHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"RATE_LIMITED","message":"tool signature suspicious rate limit"}}`)
	}))
	defer srv.Close()

	clientDB := newRelayTestDB(t)
	grp, _ := clientDB.Group.Create().SetName("sg-test").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("sg-acc-1").SetPlatform(domain.PlatformWorkBuddy).SetPriority(1).
		SetDomain(srv.URL).SetCredentials(map[string]any{"api_key": "k1", "domain": srv.URL}).
		AddGroupIDs(grp.ID).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("sg-acc-2").SetPlatform(domain.PlatformWorkBuddy).SetPriority(5).
		SetDomain(srv.URL).SetCredentials(map[string]any{"api_key": "k2", "domain": srv.URL}).
		AddGroupIDs(grp.ID).Save(ctx)

	sg := NewSessionGuard(SessionGuardConfig{
		Enabled:             true,
		PauseSeconds:        300,
		LogEnabled:          true,
		LogDir:              logDir,
		MaxLogFiles:         50,
		AbortCurrentRetry:   true,
		FallbackFingerprint: true,
	})

	// 故意让工具混淆关闭，验证日志可一眼分辨出 UNOBFUSCATED 原名工具！
	obfOff := toolobf.NewEngine(toolobf.Config{
		Enabled: false,
		Mode:    toolobf.ModeHash,
		Salt:    "sg-salt",
	})

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbCli := workbuddy.NewClient("", srv.URL, logger)
	relayCli := NewClient(config.CommandCodeConfig{}, logger)
	relayCli.SetWorkBuddyClient(wbCli)

	svc := NewService(relayCli, sched, &lockedSpyRecorder{}, &config.Config{}, logger,
		WithWorkBuddyClient(wbCli),
		WithToolObfuscator(obfOff),
		WithSessionGuard(sg),
	)

	reqPayload := []byte(`{
		"model":"claude-sonnet-4-6",
		"stream":false,
		"session_id":"sess-risk-001",
		"tools":[{"type":"function","function":{"name":"Bash","description":"execute shell","parameters":{"type":"object"}}}],
		"messages":[{"role":"user","content":"list files"}]
	}`)

	// 第 1 次请求：账号 1 返回非额度 429 -> 立即暂停 sess-risk-001 5 分钟、停止尝试账号 2、并将请求体写成 .log
	rr1 := httptest.NewRecorder()
	svc.Relay(ctx, rr1, &RelayRequest{
		Protocol:  ProtocolOpenAI,
		GroupID:   grp.ID,
		RequestID: "req-sg-1",
		Body:      reqPayload,
	})
	if rr1.Code != http.StatusTooManyRequests {
		t.Fatalf("期望返回 429，实际 %d body=%s", rr1.Code, rr1.Body.String())
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("开启 abort_current_retry 后应只打到账号 1 即暂停，实际 upstreamHits=%d", upstreamHits.Load())
	}

	pausedList := sg.PausedSessions()
	if len(pausedList) != 1 || pausedList[0].SessionID != "sess-risk-001" {
		t.Fatalf("sess-risk-001 应被加入暂停调度列表: %+v", pausedList)
	}
	if !pausedList[0].SuspectedUnobfuscated || pausedList[0].UnobfuscatedToolsCount != 1 {
		t.Fatalf("诊断结果应标记 Bash 为未混淆: %+v", pausedList[0])
	}

	// 检查 .log 文件已生成且包含原始请求体与 [UNOBFUSCATED] "Bash" 诊断
	files, err := sg.ListLogFiles(10)
	if err != nil || len(files) != 1 {
		t.Fatalf("期望写入 1 份 .log 审计文件，实际 err=%v files=%+v", err, files)
	}
	logRaw, err := os.ReadFile(filepath.Join(logDir, files[0].Filename))
	if err != nil {
		t.Fatalf("读取 .log 文件失败: %v", err)
	}
	logText := string(logRaw)
	if !strings.Contains(logText, `[UNOBFUSCATED] "Bash"`) || !strings.Contains(logText, "RAW INBOUND REQUEST BODY") || !strings.Contains(logText, `"sess-risk-001"`) {
		t.Fatalf(".log 内容缺少混淆诊断或完整请求体:\n%s", logText)
	}

	// 第 2 次相同 session_id 请求：直接被 SessionGuard 拦截返回 429，绝不再打向上游（upstreamHits 仍为 1！）
	rr2 := httptest.NewRecorder()
	svc.Relay(ctx, rr2, &RelayRequest{
		Protocol:  ProtocolOpenAI,
		GroupID:   grp.ID,
		RequestID: "req-sg-2",
		Body:      reqPayload,
	})
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("暂停期内第 2 次请求应被直接拦截返回 429，实际 %d", rr2.Code)
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("暂停期内不应再次调度任何上游账号，期望 upstreamHits=1 实际=%d", upstreamHits.Load())
	}

	// 第 3 次：换新 session_id，或手动恢复后即可正常放行调度
	if !sg.ResumeSession("sess-risk-001") {
		t.Fatal("ResumeSession 应返回 true")
	}
	rr3 := httptest.NewRecorder()
	svc.Relay(ctx, rr3, &RelayRequest{
		Protocol:  ProtocolOpenAI,
		GroupID:   grp.ID,
		RequestID: "req-sg-3",
		Body:      reqPayload,
	})
	if upstreamHits.Load() != 2 {
		t.Fatalf("手动恢复后请求应允许继续调度上游，期望 upstreamHits=2 实际=%d", upstreamHits.Load())
	}
}

func TestSessionGuardDoesNotPauseOnQuotaExhausted429(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	logDir := t.TempDir()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// 账号 1 返回账号额度不足 (credits/quota exhausted) -> 应正常换号到账号 2，且不暂停 session!
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"QUOTA_EXCEEDED","message":"account quota exceeded / 额度不足"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-ok\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-ok\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	clientDB := newRelayTestDB(t)
	grp, _ := clientDB.Group.Create().SetName("sg-quota").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("sg-q1").SetPlatform(domain.PlatformWorkBuddy).SetPriority(1).
		SetDomain(srv.URL).SetCredentials(map[string]any{"api_key": "k1", "domain": srv.URL}).
		AddGroupIDs(grp.ID).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("sg-q2").SetPlatform(domain.PlatformWorkBuddy).SetPriority(5).
		SetDomain(srv.URL).SetCredentials(map[string]any{"api_key": "k2", "domain": srv.URL}).
		AddGroupIDs(grp.ID).Save(ctx)

	sg := NewSessionGuard(SessionGuardConfig{
		Enabled:             true,
		PauseSeconds:        300,
		LogEnabled:          true,
		LogDir:              logDir,
		AbortCurrentRetry:   true,
		FallbackFingerprint: true,
	})

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbCli := workbuddy.NewClient("", srv.URL, logger)
	relayCli := NewClient(config.CommandCodeConfig{}, logger)
	relayCli.SetWorkBuddyClient(wbCli)

	svc := NewService(relayCli, sched, &lockedSpyRecorder{}, &config.Config{}, logger,
		WithWorkBuddyClient(wbCli),
		WithSessionGuard(sg),
	)

	rr := httptest.NewRecorder()
	svc.Relay(ctx, rr, &RelayRequest{
		Protocol:  ProtocolOpenAI,
		GroupID:   grp.ID,
		RequestID: "req-quota-ok",
		Body: []byte(`{
			"model":"claude-sonnet-4-6",
			"stream":false,
			"session_id":"sess-quota-ok",
			"messages":[{"role":"user","content":"hi"}]
		}`),
	})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "ok") {
		t.Fatalf("账号额度不足 429 应正常切换到账号 2 并返回 200，实际 code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(sg.PausedSessions()) != 0 {
		t.Fatalf("账号额度不足 429 不应暂停会话调度，实际=%+v", sg.PausedSessions())
	}
	if logs, _ := sg.ListLogFiles(10); len(logs) != 0 {
		t.Fatalf("账号额度不足 429 不应写入暂停日志，实际=%+v", logs)
	}
}
