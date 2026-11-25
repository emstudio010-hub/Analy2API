package relay

import (
	"context"
	"encoding/json"
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
	"cmd2api/internal/scheduler"
	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"
)

func TestToolObfuscationAllPlatformsAndProtocols(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	obfCfg := toolobf.Config{
		Enabled: true,
		Mode:    toolobf.ModeHash,
		Prefix:  "ess_t_",
		Salt:    "relay-e2e-salt-2026",
		Scope:   toolobf.ScopeAll,
		Platforms: []string{
			domain.PlatformWorkBuddy,
			domain.PlatformAntigravity,
			domain.PlatformCommandCode,
			domain.PlatformOpenCode,
		},
		CustomMap: map[string]string{
			"custom_helper": "biz_helper",
		},
		ExcludeTools: []string{"ping_health"},
	}
	obfEng := toolobf.NewEngine(obfCfg)
	sessRef := obfEng.NewSession(domain.PlatformWorkBuddy)
	obfBash := sessRef.Obfuscate("Bash")
	obfEdit := sessRef.Obfuscate("Edit")
	longMCP := "mcp__very_long_github_enterprise_organization_repository_automation__create_pull_request_review_thread"
	obfMCP := sessRef.Obfuscate(longMCP)

	if strings.Contains(obfBash, "Bash") || strings.Contains(obfEdit, "Edit") || len(obfMCP) > 64 {
		t.Fatalf("混淆结果未达标: bash=%q edit=%q mcp=%q(len=%d)", obfBash, obfEdit, obfMCP, len(obfMCP))
	}

	assertOpenAIUpstreamObfuscated := func(t *testing.T, bodyStr string) {
		t.Helper()
		if strings.Contains(bodyStr, `"Bash"`) || strings.Contains(bodyStr, `"Edit"`) || strings.Contains(bodyStr, longMCP) {
			t.Fatalf("上游 OpenAI 形状请求仍包含敏感工具原名: %s", bodyStr)
		}
		for _, want := range []string{`"` + obfBash + `"`, `"` + obfEdit + `"`, `"` + obfMCP + `"`, `"biz_helper"`, `"ping_health"`} {
			if !strings.Contains(bodyStr, want) {
				t.Fatalf("上游请求缺少期望工具映射 %s:\n%s", want, bodyStr)
			}
		}
	}

	var (
		mu              sync.Mutex
		capturedByPlat  = make(map[string][]string)
	)
	recordCapture := func(plat, raw string) {
		mu.Lock()
		capturedByPlat[plat] = append(capturedByPlat[plat], raw)
		mu.Unlock()
	}

	wbAndAgyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v2/chat/completions":
			recordCapture(domain.PlatformWorkBuddy, string(raw))
		case "/v1/chat/completions":
			recordCapture(domain.PlatformAntigravity, string(raw))
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-obf\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_obf_1\",\"type\":\"function\",\"function\":{\"name\":\""+obfBash+"\",\"arguments\":\"\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-obf\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"cmd\\\":\\\"pwd\\\"}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-obf\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":12,\"total_tokens\":62}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer wbAndAgyServer.Close()

	ocServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		recordCapture(domain.PlatformOpenCode, string(raw))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_oc_1\",\"type\":\"function\",\"function\":{\"name\":\""+obfBash+"\",\"arguments\":\"{\\\"cmd\\\":\\\"pwd\\\"}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":10}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer ocServer.Close()

	ccServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/fingerprint/record", "/alpha/lifecycle-events":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/alpha/generate":
			raw, _ := io.ReadAll(r.Body)
			recordCapture(domain.PlatformCommandCode, string(raw))
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = io.WriteString(w, `{"type":"tool-call","toolCallId":"call_cc_1","toolName":"`+obfBash+`","input":{"cmd":"pwd"}}`+"\n")
			_, _ = io.WriteString(w, `{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":30,"outputTokens":8}}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer ccServer.Close()

	clientDB := newRelayTestDB(t)
	wbGrp, _ := clientDB.Group.Create().SetName("wb-obf").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("wb-acc").
		SetPlatform(domain.PlatformWorkBuddy).
		SetDomain(wbAndAgyServer.URL).
		SetCredentials(map[string]any{"api_key": "wb-tok", "domain": wbAndAgyServer.URL}).
		AddGroupIDs(wbGrp.ID).
		Save(ctx)

	agyGrp, _ := clientDB.Group.Create().SetName("agy-obf").SetPlatform(domain.PlatformAntigravity).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("agy-acc").
		SetPlatform(domain.PlatformAntigravity).
		SetCredentials(map[string]any{"api_key": "agy-tok", "base_url": wbAndAgyServer.URL}).
		AddGroupIDs(agyGrp.ID).
		Save(ctx)

	ocGrp, _ := clientDB.Group.Create().SetName("oc-obf").SetPlatform(domain.PlatformOpenCode).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("oc-acc").
		SetPlatform(domain.PlatformOpenCode).
		SetCredentials(map[string]any{"api_key": "oc-tok"}).
		SetExtra(map[string]any{"base_url": ocServer.URL}).
		AddGroupIDs(ocGrp.ID).
		Save(ctx)

	ccGrp, _ := clientDB.Group.Create().SetName("cc-obf").SetPlatform(domain.PlatformCommandCode).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("cc-acc").
		SetPlatform(domain.PlatformCommandCode).
		SetCredentials(map[string]any{"api_key": "user_cc_tok"}).
		AddGroupIDs(ccGrp.ID).
		Save(ctx)

	cfg := &config.Config{
		CommandCC: config.CommandCodeConfig{
			BaseURL:              ccServer.URL,
			FirstTokenTimeout:    5 * time.Second,
			StreamIdleTimeout:    5 * time.Second,
			NonStreamIdleTimeout: 5 * time.Second,
		},
	}
	sched := scheduler.New(clientDB, nil, 3, logger)
	wbCli := workbuddy.NewClient("", wbAndAgyServer.URL, logger)
	agCli := antigravity.NewClientWithPool(wbCli.ProxyPool(), logger)
	relayCli := NewClient(cfg.CommandCC, logger)
	relayCli.SetWorkBuddyClient(wbCli)
	relayCli.SetAntigravityClient(agCli)

	svc := NewService(relayCli, sched, &lockedSpyRecorder{}, cfg, logger,
		WithWorkBuddyClient(wbCli),
		WithAntigravityClient(agCli),
		WithToolObfuscator(obfEng),
	)

	buildOpenAIPayload := func(stream bool) []byte {
		body := map[string]any{
			"model":  "claude-sonnet-4-6",
			"stream": stream,
			"tools": []map[string]any{
				{"type": "function", "function": map[string]any{"name": "Bash", "description": "run", "parameters": map[string]any{"type": "object"}}},
				{"type": "function", "function": map[string]any{"name": "Edit", "description": "edit", "parameters": map[string]any{"type": "object"}}},
				{"type": "function", "function": map[string]any{"name": longMCP, "description": "mcp", "parameters": map[string]any{"type": "object"}}},
				{"type": "function", "function": map[string]any{"name": "custom_helper", "description": "custom", "parameters": map[string]any{"type": "object"}}},
				{"type": "function", "function": map[string]any{"name": "ping_health", "description": "ping", "parameters": map[string]any{"type": "object"}}},
			},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}},
			"messages": []map[string]any{
				{"role": "user", "content": "check"},
				{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "call_prev", "type": "function", "function": map[string]any{"name": "Edit", "arguments": "{}"}},
					},
				},
				{"role": "tool", "tool_call_id": "call_prev", "name": "Edit", "content": "ok"},
				{"role": "user", "content": "now run bash"},
			},
		}
		b, _ := json.Marshal(body)
		return b
	}

	buildAnthropicPayload := func(stream bool) []byte {
		body := map[string]any{
			"model":      "claude-sonnet-4-6",
			"max_tokens": 1024,
			"stream":     stream,
			"tools": []map[string]any{
				{"name": "Bash", "description": "run", "input_schema": map[string]any{"type": "object"}},
				{"name": "Edit", "description": "edit", "input_schema": map[string]any{"type": "object"}},
				{"name": longMCP, "description": "mcp", "input_schema": map[string]any{"type": "object"}},
				{"name": "custom_helper", "description": "custom", "input_schema": map[string]any{"type": "object"}},
				{"name": "ping_health", "description": "ping", "input_schema": map[string]any{"type": "object"}},
			},
			"tool_choice": map[string]any{"type": "tool", "name": "Bash"},
			"messages": []map[string]any{
				{"role": "user", "content": "check"},
				{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "toolu_prev", "name": "Edit", "input": map[string]any{}},
					},
				},
				{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "toolu_prev", "content": "ok"},
						{"type": "text", "text": "run bash"},
					},
				},
			},
		}
		b, _ := json.Marshal(body)
		return b
	}

	buildResponsesPayload := func(stream bool) []byte {
		body := map[string]any{
			"model":  "gpt-5.3-codex",
			"stream": stream,
			"tools": []map[string]any{
				{"type": "function", "name": "Bash", "description": "run", "parameters": map[string]any{"type": "object"}},
				{"type": "function", "name": "Edit", "description": "edit", "parameters": map[string]any{"type": "object"}},
				{"type": "function", "name": longMCP, "description": "mcp", "parameters": map[string]any{"type": "object"}},
				{"type": "function", "name": "custom_helper", "description": "custom", "parameters": map[string]any{"type": "object"}},
				{"type": "function", "name": "ping_health", "description": "ping", "parameters": map[string]any{"type": "object"}},
			},
			"tool_choice": map[string]any{"type": "function", "name": "Bash"},
			"input": []map[string]any{
				{"role": "user", "content": "check"},
				{"type": "function_call", "call_id": "fc_prev", "name": "Edit", "arguments": "{}"},
				{"type": "function_call_output", "call_id": "fc_prev", "output": "ok"},
			},
		}
		b, _ := json.Marshal(body)
		return b
	}

	platforms := []struct {
		name    string
		groupID int64
	}{
		{name: domain.PlatformWorkBuddy, groupID: wbGrp.ID},
		{name: domain.PlatformAntigravity, groupID: agyGrp.ID},
		{name: domain.PlatformOpenCode, groupID: ocGrp.ID},
		{name: domain.PlatformCommandCode, groupID: ccGrp.ID},
	}

	for _, plat := range platforms {
		for _, stream := range []bool{true, false} {
			for _, proto := range []Protocol{ProtocolOpenAI, ProtocolAnthropic, ProtocolResponses} {
				var raw []byte
				switch proto {
				case ProtocolOpenAI:
					raw = buildOpenAIPayload(stream)
				case ProtocolAnthropic:
					raw = buildAnthropicPayload(stream)
				case ProtocolResponses:
					raw = buildResponsesPayload(stream)
				}

				rr := httptest.NewRecorder()
				svc.Relay(ctx, rr, &RelayRequest{
					Protocol:  proto,
					Body:      raw,
					GroupID:   plat.groupID,
					RequestID: plat.name + "-" + string(proto),
				})
				if rr.Code != http.StatusOK {
					t.Fatalf("%s %s stream=%v status=%d body=%s", plat.name, proto, stream, rr.Code, rr.Body.String())
				}
				respText := rr.Body.String()
				if strings.Contains(respText, obfBash) {
					t.Fatalf("%s %s stream=%v 下游响应泄露了混淆后的工具名 %q:\n%s", plat.name, proto, stream, obfBash, respText)
				}
				if !strings.Contains(respText, `"Bash"`) {
					t.Fatalf("%s %s stream=%v 下游响应未还原原工具名 Bash:\n%s", plat.name, proto, stream, respText)
				}
			}
		}
	}

	// 验证所有四大平台上游收到的全部请求均完成了敏感工具名混淆
	for _, plat := range platforms {
		bodies := capturedByPlat[plat.name]
		if len(bodies) != 6 {
			t.Fatalf("平台 %s 期望捕获 6 次上游请求，实际 %d", plat.name, len(bodies))
		}
		for _, b := range bodies {
			assertOpenAIUpstreamObfuscated(t, b)
		}
	}
}

func TestStreamDeltaNormalizerSplitToolNameRestore(t *testing.T) {
	cfg := toolobf.DefaultConfig()
	cfg.Mode = toolobf.ModeHash
	cfg.Prefix = "ess_t_"
	cfg.Salt = "split-salt"
	eng := toolobf.NewEngine(cfg)
	sess := eng.NewSession(domain.PlatformWorkBuddy)
	obfBash := sess.Obfuscate("Bash")
	if len(obfBash) <= 6 {
		t.Fatalf("expected hash name longer than prefix, got %q", obfBash)
	}
	part1 := obfBash[:6] // "ess_t_"
	part2 := obfBash[6:]

	norm := NewStreamDeltaNormalizer("claude-sonnet-4-6", "chatcmpl-split", 1700000000)
	norm.SetToolSession(sess)

	// Chunk 1: 只发送部分前缀 "ess_t_"，不应认为已有可见输出，也不得泄漏残片
	c1, done1, skip1 := norm.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_split_1","type":"function","function":{"name":"` + part1 + `","arguments":""}}]}}]}`)
	if done1 || skip1 || c1 == nil {
		t.Fatalf("chunk1 期望返回有效 NormalizedChunk，得到 done=%v skip=%v chunk=%+v", done1, skip1, c1)
	}
	if c1.HasVisibleContent() || len(c1.ToolCalls) != 0 || strings.Contains(string(c1.CleanJSON), part1) {
		t.Fatalf("chunk1 暂存前缀期间不得输出残片工具名: hasVisible=%v tcs=%+v json=%s", c1.HasVisibleContent(), c1.ToolCalls, string(c1.CleanJSON))
	}

	// Chunk 2: 发送剩余后缀后立即合并并无损还原为 Bash，同时回填 Chunk 1 的 id / type
	c2, _, _ := norm.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"` + part2 + `"}}]}}]}`)
	if len(c2.ToolCalls) != 1 || c2.ToolCalls[0].ID != "call_split_1" || c2.ToolCalls[0].Name != "Bash" {
		t.Fatalf("chunk2 还原失败: %+v json=%s", c2.ToolCalls, string(c2.CleanJSON))
	}
	if strings.Contains(string(c2.CleanJSON), obfBash) || !strings.Contains(string(c2.CleanJSON), `"Bash"`) {
		t.Fatalf("chunk2 CleanJSON 未正确还原: %s", string(c2.CleanJSON))
	}

	// Chunk 3 + Finish，同时交叉验证 Anthropic 与 Responses 编码器均从一开始就拿到完整的 "Bash"
	c3, _, _ := norm.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"ls\"}"}}]}}]}`)
	c4, _, _ := norm.NormalizeLine(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)

	anthEnc := newWBAnthropicStreamEncoder("claude-sonnet-4-6", "msg_split")
	respEnc := NewResponsesStreamEncoder("gpt-5.3-codex", "resp_split", 1700000000)
	var anthOut, respOut strings.Builder
	for _, ch := range []*NormalizedChunk{c1, c2, c3, c4} {
		for _, f := range anthEnc.EncodeChunk(ch) {
			anthOut.WriteString(f)
		}
		for _, f := range respEnc.EncodeChunk(ch) {
			respOut.WriteString(f)
		}
	}
	for _, f := range anthEnc.Finish("tool_calls", &NormalizedUsage{}) {
		anthOut.WriteString(f)
	}
	for _, f := range respEnc.Finish() {
		respOut.WriteString(f)
	}

	if !strings.Contains(anthOut.String(), `"name":"Bash"`) || !strings.Contains(anthOut.String(), `"id":"call_split_1"`) {
		t.Fatalf("Anthropic 分片工具名还原异常:\n%s", anthOut.String())
	}
	if !strings.Contains(respOut.String(), `"name":"Bash"`) || !strings.Contains(respOut.String(), `"call_id":"call_split_1"`) {
		t.Fatalf("Responses 分片工具名还原异常:\n%s", respOut.String())
	}
}

func TestToolObfuscationFailoverRetryNoDoubleObfuscation(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfgObf := toolobf.DefaultConfig()
	cfgObf.Mode = toolobf.ModePrefix
	cfgObf.Prefix = "ess_"
	cfgObf.Salt = "retry-salt"
	eng := toolobf.NewEngine(cfgObf)
	expectedWire := "ess_Bash"

	var calls atomic.Int32
	var mu sync.Mutex
	var bodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-retry\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_r1\",\"type\":\"function\",\"function\":{\"name\":\""+expectedWire+"\",\"arguments\":\"{}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-retry\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	clientDB := newRelayTestDB(t)
	grp, _ := clientDB.Group.Create().SetName("wb-retry-obf").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("wb-1").SetPlatform(domain.PlatformWorkBuddy).SetPriority(1).
		SetDomain(srv.URL).SetCredentials(map[string]any{"api_key": "k1", "domain": srv.URL}).
		AddGroupIDs(grp.ID).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("wb-2").SetPlatform(domain.PlatformWorkBuddy).SetPriority(5).
		SetDomain(srv.URL).SetCredentials(map[string]any{"api_key": "k2", "domain": srv.URL}).
		AddGroupIDs(grp.ID).Save(ctx)

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbCli := workbuddy.NewClient("", srv.URL, logger)
	relayCli := NewClient(config.CommandCodeConfig{}, logger)
	relayCli.SetWorkBuddyClient(wbCli)

	svc := NewService(relayCli, sched, &lockedSpyRecorder{}, &config.Config{}, logger,
		WithWorkBuddyClient(wbCli),
		WithToolObfuscator(eng),
	)

	rr := httptest.NewRecorder()
	svc.Relay(ctx, rr, &RelayRequest{
		Protocol:  ProtocolOpenAI,
		GroupID:   grp.ID,
		RequestID: "retry-obf",
		Body: []byte(`{
			"model":"claude-sonnet-4-6",
			"stream":true,
			"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}}],
			"messages":[{"role":"user","content":"go"}]
		}`),
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover, got %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"Bash"`) || strings.Contains(rr.Body.String(), expectedWire) {
		t.Fatalf("downstream response must restore Bash: %s", rr.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d", len(bodies))
	}
	for i, b := range bodies {
		if strings.Contains(b, "ess_ess_") || !strings.Contains(b, `"`+expectedWire+`"`) {
			t.Fatalf("attempt %d suffered missing or double obfuscation: %s", i+1, b)
		}
	}
}

func TestPerGroupToolObfuscationAndPromptRelay(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var mu sync.Mutex
	captured := make(map[string]string)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		auth := r.Header.Get("Authorization")
		mu.Lock()
		captured[auth] = string(raw)
		mu.Unlock()

		wireTool := "Bash"
		if strings.Contains(string(raw), `"exec_task"`) {
			wireTool = "exec_task"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-pg\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_pg\",\"type\":\"function\",\"function\":{\"name\":\""+wireTool+"\",\"arguments\":\"{}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"cmpl-pg\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	clientDB := newRelayTestDB(t)
	grpAlias, _ := clientDB.Group.Create().SetName("grp-alias").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("wb-alias").SetPlatform(domain.PlatformWorkBuddy).
		SetDomain(srv.URL).SetCredentials(map[string]any{"api_key": "tok-alias", "domain": srv.URL}).
		AddGroupIDs(grpAlias.ID).Save(ctx)

	grpOff, _ := clientDB.Group.Create().SetName("grp-off").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("wb-off").SetPlatform(domain.PlatformWorkBuddy).
		SetDomain(srv.URL).SetCredentials(map[string]any{"api_key": "tok-off", "domain": srv.URL}).
		AddGroupIDs(grpOff.ID).Save(ctx)

	eng := toolobf.NewEngine(toolobf.Config{
		Enabled:   true,
		Mode:      toolobf.ModeHash,
		Prefix:    "ess_t_",
		Salt:      "per-grp-salt",
		Scope:     toolobf.ScopeAll,
		Platforms: []string{domain.PlatformWorkBuddy},
		GroupOverrides: map[int64]toolobf.GroupOverride{
			grpAlias.ID: {Mode: toolobf.ModeAlias},
			grpOff.ID:   {Mode: toolobf.GroupModeOff},
		},
	})

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbCli := workbuddy.NewClient("", srv.URL, logger)
	relayCli := NewClient(config.CommandCodeConfig{}, logger)
	relayCli.SetWorkBuddyClient(wbCli)

	svc := NewService(relayCli, sched, &lockedSpyRecorder{}, &config.Config{}, logger,
		WithWorkBuddyClient(wbCli),
		WithToolObfuscator(eng),
	)

	reqBody := []byte(`{
		"model":"claude-sonnet-4-6",
		"stream":false,
		"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}}],
		"messages":[{"role":"user","content":"go"}]
	}`)

	rrA := httptest.NewRecorder()
	svc.Relay(ctx, rrA, &RelayRequest{Protocol: ProtocolOpenAI, GroupID: grpAlias.ID, RequestID: "pg-a", Body: reqBody})
	if rrA.Code != http.StatusOK || !strings.Contains(rrA.Body.String(), `"Bash"`) {
		t.Fatalf("grpAlias relay unexpected: code=%d body=%s", rrA.Code, rrA.Body.String())
	}

	rrB := httptest.NewRecorder()
	svc.Relay(ctx, rrB, &RelayRequest{Protocol: ProtocolOpenAI, GroupID: grpOff.ID, RequestID: "pg-b", Body: reqBody})
	if rrB.Code != http.StatusOK || !strings.Contains(rrB.Body.String(), `"Bash"`) {
		t.Fatalf("grpOff relay unexpected: code=%d body=%s", rrB.Code, rrB.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(captured["Bearer tok-alias"], `"exec_task"`) || strings.Contains(captured["Bearer tok-alias"], `"Bash"`) {
		t.Fatalf("grpAlias upstream payload must obfuscate Bash -> exec_task: %s", captured["Bearer tok-alias"])
	}
	if !strings.Contains(captured["Bearer tok-off"], `"Bash"`) || strings.Contains(captured["Bearer tok-off"], `"exec_task"`) {
		t.Fatalf("grpOff upstream payload must leave Bash un-obfuscated: %s", captured["Bearer tok-off"])
	}
}

func TestAntigravityDirectCLIV1InternalToolObfuscationAndHeaders(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var (
		gotPath    string
		gotHeaders http.Header
		gotBody    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"exec_task","id":"fc_ag_1","args":{"cmd":"uname -a"}}}]}}]}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":10}}}`+"\n\n")
	}))
	defer srv.Close()

	clientDB := newRelayTestDB(t)
	grpAG, _ := clientDB.Group.Create().SetName("grp-agy-direct").SetPlatform(domain.PlatformAntigravity).Save(ctx)
	_, _ = clientDB.Account.Create().
		SetName("agy-direct-1").
		SetPlatform(domain.PlatformAntigravity).
		SetDomain(srv.URL).
		SetCredentials(map[string]any{"api_key": "ya29.direct_tok", "base_url": srv.URL}).
		SetExtra(map[string]any{"account_mode": "direct_cli", "base_url": srv.URL, "project_id": "proj-direct-99"}).
		AddGroupIDs(grpAG.ID).
		Save(ctx)

	eng := toolobf.NewEngine(toolobf.Config{
		Enabled:   true,
		Mode:      toolobf.ModeAlias,
		Scope:     toolobf.ScopeAll,
		Platforms: []string{domain.PlatformAntigravity},
	})

	sched := scheduler.New(clientDB, nil, 3, logger)
	relayCli := NewClient(config.CommandCodeConfig{}, logger)
	svc := NewService(relayCli, sched, &lockedSpyRecorder{}, &config.Config{}, logger,
		WithToolObfuscator(eng),
	)

	reqBody := []byte(`{
		"model":"claude-sonnet-4-6",
		"stream":false,
		"tools":[{
			"type":"function",
			"function":{
				"name":"Bash",
				"description":"run shell",
				"parameters":{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"$defs":{"Cmd":{"type":["string","null"],"minLength":1}},
					"type":"object",
					"additionalProperties":false,
					"properties":{"cmd":{"$ref":"#/$defs/Cmd"}},
					"required":["cmd","ghost_field"]
				}
			}
		}],
		"messages":[
			{"role":"system","content":"x-anthropic-billing-header: cc=1\nYou are Claude Code, Anthropic's official CLI for Claude. Check OS."},
			{"role":"user","content":"check os"}
		]
	}`)

	rr := httptest.NewRecorder()
	svc.Relay(ctx, rr, &RelayRequest{
		Protocol:  ProtocolOpenAI,
		GroupID:   grpAG.ID,
		RequestID: "ag-direct-1",
		Body:      reqBody,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("antigravity direct relay failed: code=%d body=%s", rr.Code, rr.Body.String())
	}

	// 1. 验证端点与真实 AntiGravity CLI 请求头（绝不泄漏 X-Qimei36 等自定义头）
	if gotPath != "/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("expected v1internal endpoint, got %q", gotPath)
	}
	if ua := gotHeaders.Get("User-Agent"); !strings.HasPrefix(ua, "antigravity/") || strings.Contains(ua, "fp_") {
		t.Fatalf("expected authentic antigravity UA, got %q", ua)
	}
	for _, forbidden := range []string{"X-Qimei36", "X-Machine-Id", "X-Device-Fingerprint", "X-Client-Id"} {
		if gotHeaders.Get(forbidden) != "" {
			t.Fatalf("forbidden header %s leaked on antigravity inference request", forbidden)
		}
	}

	// 2. 验证出站 v1internal 请求体：项目、工具名混淆、JSON Schema 清洗、系统提示词净化
	if !strings.Contains(gotBody, `"project":"proj-direct-99"`) || !strings.Contains(gotBody, `"userAgent":"antigravity"`) {
		t.Fatalf("missing v1internal project/userAgent: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"exec_task"`) || strings.Contains(gotBody, `"name":"Bash"`) {
		t.Fatalf("tool name Bash should be obfuscated to exec_task in v1internal functionDeclarations: %s", gotBody)
	}
	for _, bad := range []string{"x-anthropic-billing-header", "Claude Code", "$defs", "additionalProperties", "ghost_field"} {
		if strings.Contains(gotBody, bad) {
			t.Fatalf("outbound v1internal payload still contains unsanitized %q: %s", bad, gotBody)
		}
	}

	// 3. 验证下游响应中的工具名已从 exec_task 精确还原回 Bash
	if !strings.Contains(rr.Body.String(), `"name":"Bash"`) {
		t.Fatalf("downstream client response must restore tool name to Bash, got: %s", rr.Body.String())
	}
}
