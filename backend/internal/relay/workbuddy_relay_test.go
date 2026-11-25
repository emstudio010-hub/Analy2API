package relay

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cmd2api/ent"
	"cmd2api/internal/config"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/workbuddy"
)

type spyRecorder struct {
	records []UsageRecord
}

func (r *spyRecorder) Record(_ context.Context, rec UsageRecord) {
	r.records = append(r.records, rec)
}

type spyTokenUpdater struct {
	calls        int
	accountID    int64
	accessToken  string
	refreshToken string
}

func (u *spyTokenUpdater) UpdateTokens(_ context.Context, accountID int64, accessToken, refreshToken string) error {
	u.calls++
	u.accountID = accountID
	u.accessToken = accessToken
	u.refreshToken = refreshToken
	return nil
}

func newRelayTestDB(t *testing.T) *ent.Client {
	t.Helper()
	ctx := context.Background()
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: ":memory:",
	})
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestNormalizeCompatAndOrphanToolCalls(t *testing.T) {
	raw := map[string]any{
		"model":  "deepseek-v4.1-flash",
		"stream": false,
		"reasoning": map[string]any{
			"effort": "minimal",
		},
		"tool_choice": map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "read_file",
			},
		},
		"messages": []any{
			map[string]any{"role": "developer", "content": "sys prompt"},
			map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_ok",
						"type": "function",
						"function": map[string]any{
							"name":      "read_file",
							"arguments": `{"path":"a.go"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_ok",
				"content":      "package main",
			},
			map[string]any{
				"role":    "assistant",
				"content": "partial text",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_orphan",
						"type": "function",
						"function": map[string]any{
							"name":      "missing_tool",
							"arguments": "{}",
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_dangling",
				"content":      "should be removed",
			},
			map[string]any{"role": "user", "content": "continue"},
		},
	}

	out, err := NormalizeCompat(raw)
	if err != nil {
		t.Fatalf("NormalizeCompat failed: %v", err)
	}
	if stream, _ := out["stream"].(bool); !stream {
		t.Fatalf("expected stream=true, got %v", out["stream"])
	}
	so, _ := out["stream_options"].(map[string]any)
	if inc, _ := so["include_usage"].(bool); !inc {
		t.Fatalf("expected stream_options.include_usage=true, got %v", out["stream_options"])
	}
	if out["tool_choice"] != "read_file" {
		t.Fatalf("expected flattened tool_choice=read_file, got %v", out["tool_choice"])
	}
	if out["reasoning_effort"] != "low" {
		t.Fatalf("expected reasoning_effort=low, got %v", out["reasoning_effort"])
	}

	msgs, _ := out["messages"].([]map[string]any)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 cleaned messages (developer, assistant ok, tool ok, assistant orphan-stripped, user), got %d: %#v", len(msgs), msgs)
	}
	if msgs[0]["role"] != "system" {
		t.Fatalf("expected developer converted to system, got %v", msgs[0]["role"])
	}
	if _, hasRC := msgs[1]["reasoning_content"]; !hasRC {
		t.Fatalf("expected deepseek assistant message to backfill reasoning_content")
	}
	if _, hasTC := msgs[3]["tool_calls"]; hasTC {
		t.Fatalf("expected orphan tool_calls stripped from assistant message")
	}

	// tool_choice=none strips tools & tool_choice
	noneOut, err := NormalizeCompat(map[string]any{
		"model":       "gpt-5",
		"tool_choice": map[string]any{"type": "none"},
		"tools":       []any{map[string]any{"type": "function"}},
		"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("NormalizeCompat none failed: %v", err)
	}
	if _, ok := noneOut["tools"]; ok {
		t.Fatalf("expected tools removed when tool_choice=none")
	}
	if _, ok := noneOut["tool_choice"]; ok {
		t.Fatalf("expected tool_choice removed when tool_choice=none")
	}
}

func TestStreamDeltaNormalizerAndCommitBuffer(t *testing.T) {
	norm := NewStreamDeltaNormalizer("gpt-5", "chatcmpl-1", 123456)

	// Heartbeat / empty lines skipped
	for _, line := range []string{"", "   ", ": heartbeat", "event: ping"} {
		chunk, done, skip := norm.NormalizeLine(line)
		if !skip || done || chunk != nil {
			t.Fatalf("expected line %q to be skipped cleanly", line)
		}
	}

	// Empty delta placeholders stripped & reasoning promoted
	rawLine := `data: {"id":"orig","model":"upstream","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"think...","content":"","tool_calls":[],"function_call":null,"refusal":"","extra_fields":{}}}]}`
	chunk, done, skip := norm.NormalizeLine(rawLine)
	if skip || done || chunk == nil {
		t.Fatalf("expected valid chunk, got done=%v skip=%v chunk=%v", done, skip, chunk)
	}
	if chunk.ReasoningContent != "think..." {
		t.Fatalf("expected reasoning promoted, got %q", chunk.ReasoningContent)
	}
	var cleaned map[string]any
	if err := json.Unmarshal(chunk.CleanJSON, &cleaned); err != nil {
		t.Fatalf("unmarshal CleanJSON failed: %v", err)
	}
	choices := cleaned["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	for _, forbidden := range []string{"tool_calls", "function_call", "refusal", "extra_fields", "reasoning"} {
		if _, exists := delta[forbidden]; exists {
			t.Fatalf("expected placeholder %q stripped from delta, got %#v", forbidden, delta)
		}
	}
	if delta["reasoning_content"] != "think..." {
		t.Fatalf("expected reasoning_content in delta JSON, got %#v", delta)
	}

	// Role-only frame does not commit buffer; in-stream code 11128 fails before commit
	rec := httptest.NewRecorder()
	buf := NewStreamCommitBuffer(rec, true)
	roleChunk, _, _ := norm.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
	if roleChunk.HasVisibleContent() {
		t.Fatalf("role-only chunk should not count as visible content")
	}
	_ = buf.WriteFrame("data: "+string(roleChunk.CleanJSON)+"\n\n", roleChunk.HasVisibleContent())
	if buf.IsCommitted() {
		t.Fatalf("buffer should remain uncommitted on role-only chunk")
	}

	errChunk, _, _ := norm.NormalizeLine(`data: {"code":11128,"msg":"insufficient quota"}`)
	if errChunk == nil || errChunk.Err == nil {
		t.Fatalf("expected code 11128 in-stream error detected")
	}
	if errChunk.Err.Status != http.StatusTooManyRequests {
		t.Fatalf("expected status 429 for code 11128, got %d", errChunk.Err.Status)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected 0 bytes written before commit, got %q", rec.Body.String())
	}
}

func TestRelayFailoverOnPreCommitStreamErrorAndNoZeroWidth(t *testing.T) {
	ctx := context.Background()
	clientDB := newRelayTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var upstreamCalls atomic.Int32
	var seenPayload strings.Builder

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/chat/completions" {
			bodyBytes, _ := io.ReadAll(r.Body)
			seenPayload.Write(bodyBytes)
			callNum := upstreamCalls.Add(1)
			auth := r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if strings.Contains(auth, "tok-bad") || callNum == 1 {
				_, _ = io.WriteString(w, ": heartbeat\n\n")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
				_, _ = io.WriteString(w, "data: {\"code\":11128,\"msg\":\"quota exhausted on account 1\"}\n\n")
				return
			}
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello \"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"World!\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	grp, err := clientDB.Group.Create().
		SetName("workbuddy").
		SetPlatform(domain.PlatformWorkBuddy).
		Save(ctx)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	_, err = clientDB.Account.Create().
		SetName("acc-1").
		SetPlatform(domain.PlatformWorkBuddy).
		SetDomain(upstream.URL).
		SetQimei36("q1").
		SetMachineID("m1").
		SetPriority(1).
		SetCredentials(map[string]any{
			"api_key": "tok-bad",
			"domain":  upstream.URL,
		}).
		AddGroupIDs(grp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create acc-1: %v", err)
	}
	_, err = clientDB.Account.Create().
		SetName("acc-2").
		SetPlatform(domain.PlatformWorkBuddy).
		SetDomain(upstream.URL).
		SetQimei36("q2").
		SetMachineID("m2").
		SetPriority(5).
		SetCredentials(map[string]any{
			"api_key": "tok-good",
			"domain":  upstream.URL,
		}).
		AddGroupIDs(grp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create acc-2: %v", err)
	}

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbClient := workbuddy.NewClient("", upstream.URL, logger)
	filterEng := filter.NewDefaultEngine()
	recSpy := &spyRecorder{}
	ccClient := NewClient(config.CommandCodeConfig{StreamIdleTimeout: 5 * time.Second}, logger)
	svc := NewService(ccClient, sched, recSpy, &config.Config{
		CommandCC: config.CommandCodeConfig{StreamIdleTimeout: 5 * time.Second},
	}, logger, WithFilterEngine(filterEng), WithWorkBuddyClient(wbClient))

	reqBody := []byte(`{
		"model": "gpt-5",
		"stream": true,
		"messages": [
			{"role": "system", "content": "You are Claude Code by Anthropic."},
			{"role": "user", "content": "What is 2 + 2?"}
		]
	}`)
	rr := httptest.NewRecorder()
	svc.Relay(ctx, rr, &RelayRequest{
		Protocol:       ProtocolOpenAI,
		Body:           reqBody,
		GroupID:        grp.ID,
		RequestID:      "req-failover-1",
		RateMultiplier: 1.0,
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 after failover, got %d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("expected 2 upstream attempts (failover), got %d", upstreamCalls.Load())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "Hello ") || !strings.Contains(out, "World!") || !strings.Contains(out, "[DONE]") {
		t.Fatalf("unexpected SSE output: %s", out)
	}
	if strings.Contains(out, "11128") {
		t.Fatalf("pre-commit error from first account should not leak to client: %s", out)
	}
	if strings.ContainsRune(seenPayload.String(), '\u200b') {
		t.Fatalf("zero-width character must never be inserted into upstream payload")
	}
	if !strings.Contains(seenPayload.String(), "What is 2 + 2?") {
		t.Fatalf("pure user natural question must remain intact, got %s", seenPayload.String())
	}
	if len(recSpy.records) < 2 {
		t.Fatalf("expected usage records for failed + succeeded attempts, got %d", len(recSpy.records))
	}
}

func TestRelayRefreshTokenOn401AndResponsesAPI(t *testing.T) {
	ctx := context.Background()
	clientDB := newRelayTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var chatAttempts atomic.Int32
	var refreshCalls atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case workbuddy.PathTokenRefresh:
			refreshCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"data":{"accessToken":"tok-fresh","refreshToken":"rt-fresh","expiresIn":3600}}`)
			return
		case workbuddy.PathChatCompletions:
			chatAttempts.Add(1)
			auth := r.Header.Get("Authorization")
			if auth == "Bearer tok-stale" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"code":401,"msg":"token expired"}`)
				return
			}
			if auth != "Bearer tok-fresh" {
				t.Errorf("expected refreshed Bearer tok-fresh, got %q", auth)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"plan\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":3,\"total_tokens\":11}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	grp, err := clientDB.Group.Create().
		SetName("workbuddy").
		SetPlatform(domain.PlatformWorkBuddy).
		Save(ctx)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	acc, err := clientDB.Account.Create().
		SetName("acc-refresh").
		SetPlatform(domain.PlatformWorkBuddy).
		SetDomain(upstream.URL).
		SetQimei36("qimei123").
		SetMachineID("mach123").
		SetCredentials(map[string]any{
			"api_key":       "tok-stale",
			"refresh_token": "rt-old",
			"domain":        upstream.URL,
		}).
		AddGroupIDs(grp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbClient := workbuddy.NewClient("", upstream.URL, logger)
	updater := &spyTokenUpdater{}
	recSpy := &spyRecorder{}
	svc := NewService(nil, sched, recSpy, &config.Config{}, logger,
		WithWorkBuddyClient(wbClient),
		WithTokenUpdater(updater),
	)

	// 1. Streaming /v1/responses request triggers 401 refresh -> retry -> valid Responses SSE
	respReq := []byte(`{
		"model": "gpt-5",
		"instructions": "Be concise",
		"input": [
			{"type": "input_text", "text": "Solve 1+1"}
		],
		"stream": true
	}`)
	rr := httptest.NewRecorder()
	svc.Relay(ctx, rr, &RelayRequest{
		Protocol:       ProtocolResponses,
		Body:           respReq,
		GroupID:        grp.ID,
		RequestID:      "resp-stream-1",
		RateMultiplier: 1.0,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 after 401 refresh retry, got %d body=%s", rr.Code, rr.Body.String())
	}
	if refreshCalls.Load() != 1 || chatAttempts.Load() != 2 {
		t.Fatalf("expected 1 refresh + 2 chat attempts, got refresh=%d chat=%d", refreshCalls.Load(), chatAttempts.Load())
	}
	if updater.calls != 1 || updater.accountID != acc.ID || updater.accessToken != "tok-fresh" || updater.refreshToken != "rt-fresh" {
		t.Fatalf("unexpected token updater call: %+v", updater)
	}
	sse := rr.Body.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.reasoning_summary_text.delta",
		"event: response.output_text.delta",
		"event: response.completed",
	} {
		if !strings.Contains(sse, want) {
			t.Fatalf("expected %q in /v1/responses SSE output:\n%s", want, sse)
		}
	}

	// 2. Non-stream /v1/responses returns standard object="response" JSON
	// Update stored key so next Acquire uses tok-fresh cleanly
	if _, err := clientDB.Account.UpdateOneID(acc.ID).SetCredentials(map[string]any{
		"api_key":       "tok-fresh",
		"refresh_token": "rt-fresh",
		"domain":        upstream.URL,
	}).Save(ctx); err != nil {
		t.Fatalf("update account key: %v", err)
	}
	nonStreamBody := []byte(`{
		"model": "gpt-5",
		"input": "hello",
		"stream": false
	}`)
	rr2 := httptest.NewRecorder()
	svc.Relay(ctx, rr2, &RelayRequest{
		Protocol:       ProtocolResponses,
		Body:           nonStreamBody,
		GroupID:        grp.ID,
		RequestID:      "resp-nonstream-2",
		RateMultiplier: 1.0,
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 on non-stream /v1/responses, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	var fullResp map[string]any
	if err := json.Unmarshal(rr2.Body.Bytes(), &fullResp); err != nil {
		t.Fatalf("unmarshal /v1/responses JSON failed: %v", err)
	}
	if fullResp["object"] != "response" || fullResp["status"] != "completed" {
		t.Fatalf("unexpected non-stream response object: %#v", fullResp)
	}

	// 3. Models falls back cleanly even when groupID has no accounts
	models, err := svc.Models(ctx, 999999)
	if err != nil {
		t.Fatalf("Models fallback failed: %v", err)
	}
	if len(models) == 0 {
		t.Fatalf("expected non-empty fallback DefaultModels")
	}
}

func TestGlobalEssAgenticSystemPromptInjection(t *testing.T) {
	ctx := context.Background()
	clientDB := newRelayTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-g1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	grp, err := clientDB.Group.Create().
		SetName("workbuddy-global-sys").
		SetPlatform(domain.PlatformWorkBuddy).
		Save(ctx)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if _, err := clientDB.Account.Create().
		SetName("wb-global").
		SetPlatform(domain.PlatformWorkBuddy).
		SetDomain(upstream.URL).
		SetQimei36("qimei_global").
		SetMachineID("m_global").
		SetPriority(10).
		SetCredentials(map[string]any{
			"api_key": "wb_tok_global",
			"domain":  upstream.URL,
		}).
		AddGroupIDs(grp.ID).
		Save(ctx); err != nil {
		t.Fatalf("create account: %v", err)
	}

	sched := scheduler.New(clientDB, nil, 3, logger)
	wbClient := workbuddy.NewClient("", upstream.URL, logger)
	filterEng := filter.NewDefaultEngine()
	svc := NewService(NewClient(config.CommandCodeConfig{StreamIdleTimeout: 5 * time.Second}, logger), sched, &spyRecorder{}, &config.Config{
		CommandCC: config.CommandCodeConfig{
			StreamIdleTimeout:         5 * time.Second,
			GlobalSystemPromptEnabled: true,
			GlobalSystemPrompt:        config.DefaultGlobalSystemPrompt,
		},
	}, logger, WithFilterEngine(filterEng), WithWorkBuddyClient(wbClient))

	// Even when client sends NO system message, the global Ess-Agentic restriction prompt is injected at index 0.
	rr := httptest.NewRecorder()
	svc.Relay(ctx, rr, &RelayRequest{
		Protocol:  ProtocolOpenAI,
		Body:      []byte(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"Who are you?"}]}`),
		GroupID:   grp.ID,
		RequestID: "req-global-sys-1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	msgs, ok := capturedBody["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("expected 2 messages (global system + user), got %#v", capturedBody["messages"])
	}
	sysMsg := msgs[0].(map[string]any)
	if sysMsg["role"] != "system" || !strings.Contains(sysMsg["content"].(string), "You are Ess-Agentic") {
		t.Fatalf("expected leading Ess-Agentic global system prompt, got %#v", sysMsg)
	}
}
