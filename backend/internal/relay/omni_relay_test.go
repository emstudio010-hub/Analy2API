package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/config"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/prompt"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/workbuddy"
)

type lockedSpyRecorder struct {
	mu      sync.Mutex
	records []UsageRecord
}

func (r *lockedSpyRecorder) Record(_ context.Context, rec UsageRecord) {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
}

func (r *lockedSpyRecorder) last() UsageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.records) == 0 {
		return UsageRecord{}
	}
	return r.records[len(r.records)-1]
}

func TestAntigravityRelayAllThreeProtocolsAndUsageSemantics(t *testing.T) {
	ctx := context.Background()
	clientDB := newRelayTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var agyCalls atomic.Int32
	agyNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gemini-2.5-flash"},{"id":"claude-sonnet-4-6"}]}`)
			return
		case "/v1/chat/completions":
			agyCalls.Add(1)
			if r.Header.Get("Authorization") == "Bearer sk-agy-bad" {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"code":"RATE_LIMITED","message":"node quota busy"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-agy\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking...\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-agy\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"AGY Answer\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-agy\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40,\"total_tokens\":140,\"prompt_tokens_details\":{\"cached_tokens\":60},\"completion_tokens_details\":{\"reasoning_tokens\":25}}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer agyNode.Close()

	grp, err := clientDB.Group.Create().
		SetName("agy-group").
		SetPlatform(domain.PlatformAntigravity).
		Save(ctx)
	if err != nil {
		t.Fatalf("create agy group: %v", err)
	}

	// 账号 1（高优先级）返回 429，验证无缝自动切换到账号 2
	_, _ = clientDB.Account.Create().
		SetName("agy-acc-rate-limited").
		SetPlatform(domain.PlatformAntigravity).
		SetPriority(1).
		SetCredentials(map[string]any{
			"api_key":  "sk-agy-bad",
			"base_url": agyNode.URL,
		}).
		AddGroupIDs(grp.ID).
		Save(ctx)

	goodAcc, err := clientDB.Account.Create().
		SetName("agy-acc-good").
		SetPlatform(domain.PlatformAntigravity).
		SetPriority(5).
		SetProxyURL("http://127.0.0.1:1"). // 本机回环节点应自动绕过死代理直连成功
		SetCredentials(map[string]any{
			"api_key":  "sk-agy-good",
			"base_url": agyNode.URL,
		}).
		SetExtra(map[string]any{
			"node_id":   int64(10),
			"auth_file": "antigravity-good.json",
		}).
		AddGroupIDs(grp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create good agy account: %v", err)
	}

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbCli := workbuddy.NewClient("", "", logger)
	agCli := antigravity.NewClientWithPool(wbCli.ProxyPool(), logger)
	relayCli := NewClient(config.CommandCodeConfig{}, logger)
	relayCli.SetAntigravityClient(agCli)
	recSpy := &lockedSpyRecorder{}

	svc := NewService(relayCli, sched, recSpy, &config.Config{}, logger,
		WithWorkBuddyClient(wbCli),
		WithAntigravityClient(agCli),
	)

	// 1. ProtocolOpenAI 流式 + 429 自动换号 + 缓存去重计费语义校验
	rrOpenAI := httptest.NewRecorder()
	svc.Relay(ctx, rrOpenAI, &RelayRequest{
		Protocol:       ProtocolOpenAI,
		Body:           []byte(`{"model":"gemini-2.5-pro","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		GroupID:        grp.ID,
		RequestID:      "agy-openai-1",
		RateMultiplier: 1.0,
	})
	if rrOpenAI.Code != http.StatusOK || !strings.Contains(rrOpenAI.Body.String(), "AGY Answer") {
		t.Fatalf("OpenAI protocol on AntiGravity failed: code=%d body=%s", rrOpenAI.Code, rrOpenAI.Body.String())
	}
	rec := recSpy.last()
	if rec.Platform != domain.PlatformAntigravity {
		t.Fatalf("expected UsageRecord.Platform=%q, got %q", domain.PlatformAntigravity, rec.Platform)
	}
	// prompt_tokens=100, cached_tokens=60 => InputTokens 必须为 40，CacheReadTokens=60，ReasoningTokens=25
	if rec.InputTokens != 40 || rec.CacheReadTokens != 60 || rec.OutputTokens != 40 || rec.ReasoningTokens != 25 {
		t.Fatalf("unexpected cached/reasoning token accounting: %+v", rec)
	}

	// 2. ProtocolAnthropic (/v1/messages) 流式翻译
	rrAnth := httptest.NewRecorder()
	svc.Relay(ctx, rrAnth, &RelayRequest{
		Protocol:       ProtocolAnthropic,
		Body:           []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		GroupID:        grp.ID,
		RequestID:      "agy-anth-2",
		RateMultiplier: 1.0,
	})
	if rrAnth.Code != http.StatusOK || !strings.Contains(rrAnth.Body.String(), "event: message_start") || !strings.Contains(rrAnth.Body.String(), "thinking_delta") {
		t.Fatalf("Anthropic protocol on AntiGravity failed: code=%d body=%s", rrAnth.Code, rrAnth.Body.String())
	}

	// 3. ProtocolResponses (/v1/responses) 非流式翻译
	rrResp := httptest.NewRecorder()
	svc.Relay(ctx, rrResp, &RelayRequest{
		Protocol:       ProtocolResponses,
		Body:           []byte(`{"model":"gpt-5.3-codex","input":"ping","stream":false}`),
		GroupID:        grp.ID,
		RequestID:      "agy-resp-3",
		RateMultiplier: 1.0,
	})
	if rrResp.Code != http.StatusOK || !strings.Contains(rrResp.Body.String(), `"object":"response"`) {
		t.Fatalf("Responses protocol on AntiGravity failed: code=%d body=%s", rrResp.Code, rrResp.Body.String())
	}

	// 4. Probe 与 PlatformModels 验证
	tgt, err := sched.TargetByAccountID(ctx, goodAcc.ID)
	if err != nil {
		t.Fatalf("TargetByAccountID failed: %v", err)
	}
	probeRes := relayCli.Probe(ctx, tgt)
	if probeRes.Err != nil {
		t.Fatalf("Probe antigravity failed: %+v", probeRes.Err)
	}

	allModels, err := svc.PlatformModels(ctx, "all")
	if err != nil || len(allModels) < 15 {
		t.Fatalf("PlatformModels(all) failed: count=%d err=%v", len(allModels), err)
	}
}

func TestCommandCodeAndOpenCodeResponsesProtocolAndFailover(t *testing.T) {
	ctx := context.Background()
	clientDB := newRelayTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// 模拟 CommandCode NDJSON 上游（第 1 个账号流内返回 429 error，第 2 个账号正常输出思考+文本）
	ccUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/fingerprint/record", "/alpha/lifecycle-events":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/alpha/generate":
			w.Header().Set("Content-Type", "application/x-ndjson")
			if r.Header.Get("Authorization") == "Bearer user_cc_fail" {
				_, _ = io.WriteString(w, `{"type":"error","error":{"message":"rate limited","code":"RATE_LIMITED","statusCode":429}}`+"\n")
				return
			}
			_, _ = io.WriteString(w, `{"type":"reasoning-delta","text":"cc-think"}`+"\n")
			_, _ = io.WriteString(w, `{"type":"text-delta","text":"cc-hello"}`+"\n")
			_, _ = io.WriteString(w, `{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":50,"outputTokens":10,"cachedInputTokens":35,"reasoningTokens":4}}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer ccUpstream.Close()

	// 模拟 OpenCode SSE 上游
	ocUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"oc-think\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"oc-world\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":70},\"completion_tokens_details\":{\"reasoning_tokens\":12}}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer ocUpstream.Close()

	ccGrp, _ := clientDB.Group.Create().SetName("cc-grp").SetPlatform(domain.PlatformCommandCode).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("cc-1").
		SetPlatform(domain.PlatformCommandCode).
		SetPriority(1).
		SetCredentials(map[string]any{"api_key": "user_cc_fail"}).
		AddGroupIDs(ccGrp.ID).
		Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("cc-2").
		SetPlatform(domain.PlatformCommandCode).
		SetPriority(5).
		SetCredentials(map[string]any{"api_key": "user_cc_ok"}).
		AddGroupIDs(ccGrp.ID).
		Save(ctx)

	ocGrp, _ := clientDB.Group.Create().SetName("oc-grp").SetPlatform(domain.PlatformOpenCode).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("oc-1").
		SetPlatform(domain.PlatformOpenCode).
		SetCredentials(map[string]any{"api_key": "sk-oc-ok"}).
		SetExtra(map[string]any{"base_url": ocUpstream.URL}).
		AddGroupIDs(ocGrp.ID).
		Save(ctx)

	cfg := &config.Config{
		CommandCC: config.CommandCodeConfig{
			BaseURL:              ccUpstream.URL,
			FirstTokenTimeout:    5 * time.Second,
			StreamIdleTimeout:    5 * time.Second,
			NonStreamIdleTimeout: 5 * time.Second,
		},
	}
	sched := scheduler.New(clientDB, nil, 3, logger)
	recSpy := &lockedSpyRecorder{}
	svc := NewService(NewClient(cfg.CommandCC, logger), sched, recSpy, cfg, logger)

	// 1. CommandCode -> /v1/responses 流式（含首帧前流内 429 自动换号重试）
	rrCCStream := httptest.NewRecorder()
	svc.Relay(ctx, rrCCStream, &RelayRequest{
		Protocol:  ProtocolResponses,
		Body:      []byte(`{"model":"gpt-5.3-codex","input":"hi","stream":true}`),
		GroupID:   ccGrp.ID,
		RequestID: "cc-resp-stream",
	})
	if rrCCStream.Code != http.StatusOK {
		t.Fatalf("CommandCode /v1/responses stream failed: %d %s", rrCCStream.Code, rrCCStream.Body.String())
	}
	for _, want := range []string{"event: response.created", "cc-think", "cc-hello", "event: response.completed"} {
		if !strings.Contains(rrCCStream.Body.String(), want) {
			t.Fatalf("CommandCode /v1/responses SSE missing %q:\n%s", want, rrCCStream.Body.String())
		}
	}
	ccRec := recSpy.last()
	if ccRec.Platform != domain.PlatformCommandCode || ccRec.InputTokens != 15 || ccRec.CacheReadTokens != 35 || ccRec.ReasoningTokens != 4 {
		t.Fatalf("unexpected CommandCode usage record: %+v", ccRec)
	}

	// 2. OpenCode -> /v1/responses 非流式 JSON + 缓存去重计费校验
	rrOCFull := httptest.NewRecorder()
	svc.Relay(ctx, rrOCFull, &RelayRequest{
		Protocol:  ProtocolResponses,
		Body:      []byte(`{"model":"gpt-5","input":"hi","stream":false}`),
		GroupID:   ocGrp.ID,
		RequestID: "oc-resp-nonstream",
	})
	if rrOCFull.Code != http.StatusOK {
		t.Fatalf("OpenCode /v1/responses non-stream failed: %d %s", rrOCFull.Code, rrOCFull.Body.String())
	}
	var respObj map[string]any
	if err := json.Unmarshal(rrOCFull.Body.Bytes(), &respObj); err != nil {
		t.Fatalf("unmarshal OpenCode response failed: %v", err)
	}
	if respObj["object"] != "response" || respObj["status"] != "completed" {
		t.Fatalf("unexpected OpenCode response JSON: %#v", respObj)
	}
	ocRec := recSpy.last()
	if ocRec.Platform != domain.PlatformOpenCode || ocRec.InputTokens != 30 || ocRec.CacheReadTokens != 70 || ocRec.ReasoningTokens != 12 {
		t.Fatalf("unexpected OpenCode usage record: %+v", ocRec)
	}
}

func TestPromptEngineOrderingAndModesInRelay(t *testing.T) {
	ctx := context.Background()
	clientDB := newRelayTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var mu sync.Mutex
	var capturedMessages []map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		capturedMessages = body.Messages
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	grpAppend, _ := clientDB.Group.Create().SetName("grp-append").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)
	grpReplace, _ := clientDB.Group.Create().SetName("grp-replace").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)

	for _, gid := range []int64{grpAppend.ID, grpReplace.ID} {
		_, _ = clientDB.Account.Create().
			SetName(fmt.Sprintf("wb-%d", gid)).
			SetPlatform(domain.PlatformWorkBuddy).
			SetDomain(upstream.URL).
			SetCredentials(map[string]any{"api_key": "tok", "domain": upstream.URL}).
			AddGroupIDs(gid).
			Save(ctx)
	}

	pe := prompt.NewEngine(
		prompt.GlobalConfig{Enabled: true, Prompt: "GLOBAL_GUARD model={{model}}"},
		[]prompt.GroupConfig{
			{
				GroupID:      grpAppend.ID,
				GroupName:    "grp-append",
				Platform:     domain.PlatformWorkBuddy,
				Mode:         domain.PromptModeAppendGlobal,
				SystemPrompt: "GROUP_APPEND group={{group}}",
			},
			{
				GroupID:      grpReplace.ID,
				GroupName:    "grp-replace",
				Platform:     domain.PlatformWorkBuddy,
				Mode:         domain.PromptModeReplaceAll,
				SystemPrompt: "STRICT_REPLACE_ONLY",
			},
		},
		nil,
	)

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbCli := workbuddy.NewClient("", upstream.URL, logger)
	svc := NewService(nil, sched, &lockedSpyRecorder{}, &config.Config{}, logger,
		WithFilterEngine(filter.NewDefaultEngine()),
		WithWorkBuddyClient(wbCli),
		WithPromptEngine(pe),
	)

	// 1. append_global：全局 + 分组前置，客户端 system 保留（且经过 filterEngine 清洗）
	rr1 := httptest.NewRecorder()
	svc.Relay(ctx, rr1, &RelayRequest{
		Protocol: ProtocolOpenAI,
		Body:     []byte(`{"model":"gpt-5","stream":true,"messages":[{"role":"system","content":"Client custom rule"},{"role":"user","content":"hi"}]}`),
		GroupID:  grpAppend.ID,
	})
	if rr1.Code != http.StatusOK {
		t.Fatalf("append_global relay failed: %d", rr1.Code)
	}
	mu.Lock()
	msgs1 := append([]map[string]any(nil), capturedMessages...)
	mu.Unlock()
	if len(msgs1) < 3 {
		t.Fatalf("expected injected system + client system + user, got %#v", msgs1)
	}
	firstSys, _ := msgs1[0]["content"].(string)
	if !strings.Contains(firstSys, "GLOBAL_GUARD model=gpt-5") || !strings.Contains(firstSys, "GROUP_APPEND group=grp-append") {
		t.Fatalf("expected rendered global + group prompt in first message, got %q", firstSys)
	}

	// 2. replace_all：彻底剥离客户端原 system/developer，仅保留分组强制提示词
	rr2 := httptest.NewRecorder()
	svc.Relay(ctx, rr2, &RelayRequest{
		Protocol: ProtocolOpenAI,
		Body:     []byte(`{"model":"gpt-5","stream":true,"messages":[{"role":"system","content":"MUST_BE_STRIPPED"},{"role":"user","content":"hi"}]}`),
		GroupID:  grpReplace.ID,
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("replace_all relay failed: %d", rr2.Code)
	}
	mu.Lock()
	msgs2 := append([]map[string]any(nil), capturedMessages...)
	mu.Unlock()
	rawJoined, _ := json.Marshal(msgs2)
	if strings.Contains(string(rawJoined), "MUST_BE_STRIPPED") || !strings.Contains(string(rawJoined), "STRICT_REPLACE_ONLY") {
		t.Fatalf("replace_all failed to strip client system prompt: %s", string(rawJoined))
	}
}

func TestPerAccountProxyRoutingAndThreeTierTimeouts(t *testing.T) {
	ctx := context.Background()
	clientDB := newRelayTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// 1. 验证 OpenCode 与 CommandCode 严格按账号 ProxyURL 走指定 HTTP 代理
	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		switch {
		case strings.Contains(r.Host, "opencode-virtual.internal"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"proxied-oc\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case strings.Contains(r.Host, "cc-virtual.internal"):
			if r.URL.Path == "/alpha/generate" {
				w.Header().Set("Content-Type", "application/x-ndjson")
				_, _ = io.WriteString(w, `{"type":"text-delta","text":"proxied-cc"}`+"\n")
				_, _ = io.WriteString(w, `{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":5,"outputTokens":2}}`+"\n")
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer proxySrv.Close()

	ocGrp, _ := clientDB.Group.Create().SetName("oc-proxy").SetPlatform(domain.PlatformOpenCode).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("oc-proxied").
		SetPlatform(domain.PlatformOpenCode).
		SetProxyURL(proxySrv.URL).
		SetCredentials(map[string]any{"api_key": "sk-oc"}).
		SetExtra(map[string]any{"base_url": "http://opencode-virtual.internal/v1"}).
		AddGroupIDs(ocGrp.ID).
		Save(ctx)

	ccGrp, _ := clientDB.Group.Create().SetName("cc-proxy").SetPlatform(domain.PlatformCommandCode).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("cc-proxied").
		SetPlatform(domain.PlatformCommandCode).
		SetProxyURL(proxySrv.URL).
		SetCredentials(map[string]any{"api_key": "user_cc_proxied"}).
		AddGroupIDs(ccGrp.ID).
		Save(ctx)

	cfg := &config.Config{
		CommandCC: config.CommandCodeConfig{
			BaseURL: "http://cc-virtual.internal",
		},
	}
	sched := scheduler.New(clientDB, nil, 3, logger)
	svc := NewService(NewClient(cfg.CommandCC, logger), sched, &lockedSpyRecorder{}, cfg, logger)

	rrOC := httptest.NewRecorder()
	svc.Relay(ctx, rrOC, &RelayRequest{
		Protocol: ProtocolOpenAI,
		Body:     []byte(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		GroupID:  ocGrp.ID,
	})
	if rrOC.Code != http.StatusOK || !strings.Contains(rrOC.Body.String(), "proxied-oc") {
		t.Fatalf("expected OpenCode request via account ProxyURL to succeed: code=%d body=%s", rrOC.Code, rrOC.Body.String())
	}

	rrCC := httptest.NewRecorder()
	svc.Relay(ctx, rrCC, &RelayRequest{
		Protocol: ProtocolOpenAI,
		Body:     []byte(`{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		GroupID:  ccGrp.ID,
	})
	if rrCC.Code != http.StatusOK || !strings.Contains(rrCC.Body.String(), "proxied-cc") {
		t.Fatalf("expected CommandCode request via account ProxyURL to succeed: code=%d body=%s", rrCC.Code, rrCC.Body.String())
	}
	if proxyHits.Load() < 2 {
		t.Fatalf("expected proxy server to receive OpenCode + CommandCode traffic, hits=%d", proxyHits.Load())
	}

	// 2. 验证三级超时：首字前等待 120ms，FirstTokenTimeout=400ms > 120ms > StreamIdleTimeout=50ms，
	// 推理思考期间不会被较短的 StreamIdleTimeout 提前掐断！
	slowThinkUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		time.Sleep(120 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"thought-complete\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer slowThinkUpstream.Close()

	agyGrp, _ := clientDB.Group.Create().SetName("agy-slow-think").SetPlatform(domain.PlatformAntigravity).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("agy-thinker").
		SetPlatform(domain.PlatformAntigravity).
		SetCredentials(map[string]any{"api_key": "k", "base_url": slowThinkUpstream.URL}).
		AddGroupIDs(agyGrp.ID).
		Save(ctx)

	svc.UpdateTimeouts(400*time.Millisecond, 50*time.Millisecond, 500*time.Millisecond)
	rrSlow := httptest.NewRecorder()
	svc.Relay(ctx, rrSlow, &RelayRequest{
		Protocol: ProtocolOpenAI,
		Body:     []byte(`{"model":"gemini-3.1-pro","stream":true,"messages":[{"role":"user","content":"solve hard math"}]}`),
		GroupID:  agyGrp.ID,
	})
	if rrSlow.Code != http.StatusOK || !strings.Contains(rrSlow.Body.String(), "thought-complete") {
		t.Fatalf("expected FirstTokenTimeout to protect slow pre-first-token thinking, got code=%d body=%s", rrSlow.Code, rrSlow.Body.String())
	}
}
