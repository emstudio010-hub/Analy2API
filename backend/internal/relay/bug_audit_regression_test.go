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

	"cmd2api/internal/config"
	"cmd2api/internal/domain"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"
)

type atomicTokenUpdater struct {
	mu           sync.Mutex
	calls        int
	accountID    int64
	accessToken  string
	refreshToken string
}

func (u *atomicTokenUpdater) UpdateTokens(_ context.Context, accountID int64, accessToken, refreshToken string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	u.accountID = accountID
	u.accessToken = accessToken
	u.refreshToken = refreshToken
	return nil
}

// TestRefreshTokenSingleflightConcurrency 验证当大量并发请求同时命中过期 AccessToken (401) 时，
// 仅发起唯一一次 RefreshToken（防止单次旋转 refresh_token 被并发请求作废），其余请求复用最新凭证全部成功。
func TestRefreshTokenSingleflightConcurrency(t *testing.T) {
	ctx := context.Background()
	clientDB := newRelayTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var refreshCalls atomic.Int32
	var usedRefresh sync.Mutex
	refreshConsumed := false

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case workbuddy.PathTokenRefresh:
			refreshCalls.Add(1)
			time.Sleep(35 * time.Millisecond) // 放大并发窗口，确保所有协程同时进入续期屏障
			usedRefresh.Lock()
			alreadyUsed := refreshConsumed
			refreshConsumed = true
			usedRefresh.Unlock()
			if alreadyUsed {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"code":401,"msg":"refresh_token rotated and invalidated"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"data":{"accessToken":"tok-rotated-ok","refreshToken":"rt-rotated-2","expiresIn":3600}}`)
			return

		case workbuddy.PathChatCompletions:
			auth := r.Header.Get("Authorization")
			if auth == "Bearer tok-expired-0" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"code":401,"msg":"access token expired"}`)
				return
			}
			if auth != "Bearer tok-rotated-ok" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"code":401,"msg":"unexpected token"}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	grp, err := clientDB.Group.Create().SetName("wb-concurrent-refresh").SetPlatform(domain.PlatformWorkBuddy).Save(ctx)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	const workers = 10
	acc, err := clientDB.Account.Create().
		SetName("wb-single-rt").
		SetPlatform(domain.PlatformWorkBuddy).
		SetConcurrency(workers + 2).
		SetDomain(upstream.URL).
		SetQimei36("qimei_conc").
		SetMachineID("mach_conc").
		SetCredentials(map[string]any{
			"api_key":       "tok-expired-0",
			"refresh_token": "rt-single-use-1",
			"domain":        upstream.URL,
		}).
		AddGroupIDs(grp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	sched := scheduler.New(clientDB, nil, workers+2, logger)
	wbCli := workbuddy.NewClient("", upstream.URL, logger)
	updater := &atomicTokenUpdater{}
	svc := NewService(nil, sched, &lockedSpyRecorder{}, &config.Config{}, logger,
		WithWorkBuddyClient(wbCli),
		WithTokenUpdater(updater),
	)

	var wg sync.WaitGroup
	codes := make([]int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			svc.Relay(ctx, rr, &RelayRequest{
				Protocol:  ProtocolOpenAI,
				GroupID:   grp.ID,
				RequestID: fmt.Sprintf("conc-ref-%d", idx),
				Body:      []byte(`{"model":"gpt-5","stream":false,"messages":[{"role":"user","content":"ping"}]}`),
			})
			codes[idx] = rr.Code
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("worker %d expected 200 OK, got %d", i, code)
		}
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 upstream RefreshToken call under 10x stampede, got %d", got)
	}
	updater.mu.Lock()
	defer updater.mu.Unlock()
	if updater.calls != 1 || updater.accountID != acc.ID || updater.accessToken != "tok-rotated-ok" {
		t.Fatalf("unexpected tokenUpdater state: %+v", updater)
	}
}

// TestWBAnthropicStreamEncoder_DeferredNameAndParallelToolCalls 验证：
// 1. 首片仅含 id 而第二片才送达工具名时，content_block_start 不会发出空 name:""；
// 2. 同一分片同时声明多个并行工具调用头、后续分别流式传输各自参数时，不会在已关闭的 content_block 上发送非法 delta。
func TestWBAnthropicStreamEncoder_DeferredNameAndParallelToolCalls(t *testing.T) {
	t.Run("deferred_tool_name", func(t *testing.T) {
		enc := newWBAnthropicStreamEncoder("claude-sonnet-4-6", "msg_def")
		var frames []string
		frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
			ToolCalls: []ToolCallDelta{{Index: 0, ID: "call_def_1", Name: "", Arguments: ""}},
		})...)
		frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
			ToolCalls: []ToolCallDelta{{Index: 0, Name: "Read", Arguments: `{"path":"a.go"}`}},
		})...)
		frames = append(frames, enc.Finish("tool_calls", &NormalizedUsage{PromptTokens: 10, CompletionTokens: 5})...)

		joined := strings.Join(frames, "")
		if strings.Contains(joined, `"name":""`) {
			t.Fatalf("Anthropic SSE must never emit empty tool_use name:\n%s", joined)
		}
		if !strings.Contains(joined, `"id":"call_def_1"`) || !strings.Contains(joined, `"name":"Read"`) {
			t.Fatalf("Anthropic SSE missing deferred id/name:\n%s", joined)
		}
	})

	t.Run("parallel_tool_headers_then_args", func(t *testing.T) {
		enc := newWBAnthropicStreamEncoder("claude-sonnet-4-6", "msg_par")
		var frames []string
		// Chunk 1: 同时声明 index 0 与 index 1 的头部（arguments 为空）
		frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
			ToolCalls: []ToolCallDelta{
				{Index: 0, ID: "call_p0", Name: "Read", Arguments: ""},
				{Index: 1, ID: "call_p1", Name: "Grep", Arguments: ""},
			},
		})...)
		// Chunk 2: 发送 index 0 的参数
		frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
			ToolCalls: []ToolCallDelta{
				{Index: 0, Arguments: `{"file":"x.go"}`},
			},
		})...)
		// Chunk 3: 发送 index 1 的参数
		frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
			ToolCalls: []ToolCallDelta{
				{Index: 1, Arguments: `{"pattern":"main"}`},
			},
		})...)
		frames = append(frames, enc.Finish("tool_calls", &NormalizedUsage{PromptTokens: 20, CompletionTokens: 10})...)

		// 校验协议状态机：任何 content_block_stop(idx) 之后绝不能再出现 content_block_delta(idx)
		stopped := map[int]bool{}
		started := map[int]string{}
		for _, frame := range frames {
			for _, line := range strings.Split(frame, "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var ev map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
					continue
				}
				typ, _ := ev["type"].(string)
				idxFloat, hasIdx := ev["index"].(float64)
				idx := int(idxFloat)
				switch typ {
				case "content_block_start":
					cb, _ := ev["content_block"].(map[string]any)
					name, _ := cb["name"].(string)
					started[idx] = name
				case "content_block_delta":
					if hasIdx && stopped[idx] {
						t.Fatalf("protocol violation: content_block_delta emitted for already-stopped block %d:\n%s", idx, strings.Join(frames, ""))
					}
				case "content_block_stop":
					if hasIdx {
						stopped[idx] = true
					}
				}
			}
		}
		if started[0] != "Read" || started[1] != "Grep" || !stopped[0] || !stopped[1] {
			t.Fatalf("expected both parallel blocks started and cleanly stopped, got started=%+v stopped=%+v", started, stopped)
		}
	})
}

// TestResponsesStreamEncoder_DeferredCustomToolName 验证 Codex custom 工具（如 apply_patch）
// 在首帧仅携带 call_id、次帧才携带工具名时，仍准确输出 custom_tool_call 而非空名 function_call。
func TestResponsesStreamEncoder_DeferredCustomToolName(t *testing.T) {
	enc := NewResponsesStreamEncoder("gpt-5.3-codex", "req_def_patch", 1700000000)
	enc.SetCustomTools(map[string]bool{"apply_patch": true})

	var frames []string
	frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
		ToolCalls: []ToolCallDelta{{Index: 0, ID: "call_patch_9", Name: "", Arguments: ""}},
	})...)
	frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
		ToolCalls: []ToolCallDelta{{Index: 0, Name: "apply_", Arguments: ""}},
	})...)
	frames = append(frames, enc.EncodeChunk(&NormalizedChunk{
		ToolCalls: []ToolCallDelta{{Index: 0, Name: "patch", Arguments: `{"input":"*** Begin Patch\n*** End Patch"}`}},
	})...)
	frames = append(frames, enc.Finish()...)

	joined := strings.Join(frames, "")
	if strings.Contains(joined, `"type":"function_call"`) || strings.Contains(joined, `"name":""`) {
		t.Fatalf("deferred apply_patch must not emit function_call or empty name:\n%s", joined)
	}
	for _, want := range []string{
		`"type":"custom_tool_call"`,
		`"name":"apply_patch"`,
		`response.custom_tool_call_input.done`,
		`"input":"*** Begin Patch\n*** End Patch"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Responses SSE missing %q:\n%s", want, joined)
		}
	}
}

// TestStreamDeltaNormalizer_PrefixCollisionAndEarlyArgs 验证混淆名存在前缀包含关系（exec_task vs exec_task_2）
// 以及半截混淆名与早期参数同片到达时的双射无损还原。
func TestStreamDeltaNormalizer_PrefixCollisionAndEarlyArgs(t *testing.T) {
	eng := toolobf.NewEngine(toolobf.Config{
		Enabled:   true,
		Mode:      toolobf.ModeAlias,
		Prefix:    "ess_t_",
		Salt:      "prefix-collision-salt",
		Scope:     toolobf.ScopeAll,
		Platforms: []string{domain.PlatformWorkBuddy},
		CustomMap: map[string]string{
			"Bash":   "exec_task",
			"Shell2": "exec_task_2",
		},
	})
	sess := eng.NewSession(domain.PlatformWorkBuddy)
	if got1, got2 := sess.Obfuscate("Bash"), sess.Obfuscate("Shell2"); got1 != "exec_task" || got2 != "exec_task_2" {
		t.Fatalf("unexpected setup mapping: %q, %q", got1, got2)
	}

	// Case A: 分两片到达 "exec_task" + "_2" -> 必须还原为 "Shell2"，不能在首片提前误还原成 "Bash"
	normA := NewStreamDeltaNormalizer("gpt-5", "cmpl-a", 1700000000)
	normA.SetToolSession(sess)
	c1, _, _ := normA.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c_a","type":"function","function":{"name":"exec_task","arguments":""}}]}}]}`)
	if len(c1.ToolCalls) != 0 {
		t.Fatalf("expected ambiguous prefix exec_task to be held until next chunk, got %+v", c1.ToolCalls)
	}
	c2, _, _ := normA.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"_2","arguments":"{\"cmd\":\"id\"}"}}]}}]}`)
	if len(c2.ToolCalls) != 1 || c2.ToolCalls[0].Name != "Shell2" || c2.ToolCalls[0].ID != "c_a" || c2.ToolCalls[0].Arguments != `{"cmd":"id"}` {
		t.Fatalf("expected Shell2 restored cleanly, got %+v", c2.ToolCalls)
	}

	// Case B: 首片到达 "exec_task"，次片直接发 arguments -> 立即消歧还原为 "Bash"
	normB := NewStreamDeltaNormalizer("gpt-5", "cmpl-b", 1700000000)
	normB.SetToolSession(sess)
	_, _, _ = normB.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c_b","type":"function","function":{"name":"exec_task","arguments":""}}]}}]}`)
	cb2, _, _ := normB.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"ls\"}"}}]}}]}`)
	if len(cb2.ToolCalls) != 1 || cb2.ToolCalls[0].Name != "Bash" || cb2.ToolCalls[0].ID != "c_b" || cb2.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("expected Bash disambiguated on args arrival, got %+v", cb2.ToolCalls)
	}

	// Case C: 半截未完成混淆名同片附带早期 arguments -> 合并完整后一并输出还原名与完整 arguments
	normC := NewStreamDeltaNormalizer("gpt-5", "cmpl-c", 1700000000)
	normC.SetToolSession(sess)
	cc1, _, _ := normC.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c_c","type":"function","function":{"name":"exec_","arguments":"{\"cmd\":"}}]}}]}`)
	if len(cc1.ToolCalls) != 0 {
		t.Fatalf("incomplete obfuscated name with early args must remain buffered, got %+v", cc1.ToolCalls)
	}
	cc2, _, _ := normC.NormalizeLine(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"task_2","arguments":"\"whoami\"}"}}]}}]}`)
	if len(cc2.ToolCalls) != 1 || cc2.ToolCalls[0].Name != "Shell2" || cc2.ToolCalls[0].Arguments != `{"cmd":"whoami"}` {
		t.Fatalf("expected Shell2 + combined arguments, got %+v", cc2.ToolCalls)
	}
}
