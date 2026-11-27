package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cmd2api/internal/toolobf"
	"cmd2api/internal/workbuddy"
)

// ToolCallDelta 表示上游 SSE 分片里的单个工具调用增量。
type ToolCallDelta struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

// NormalizedUsage 表示从尾帧提取出的标准化 token 与积分用量。
type NormalizedUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
	Credit           float64
}

// NormalizedChunk 是 StreamDeltaNormalizer 清洗单个上游 SSE 数据帧后的产物。
type NormalizedChunk struct {
	RawPayload       string
	CleanJSON        []byte
	Role             string
	Content          string
	ReasoningContent string
	ToolCalls        []ToolCallDelta
	FinishReason     string
	Usage            *NormalizedUsage
	HasChoices       bool
	Err              *MappedError
}

// HasVisibleContent 判断该分片是否已包含可下发给客户端的实质内容或收尾信号。
// Heartbeat、纯 role 占位或空 delta 均视为尚未产生有效输出，
// 从而允许 StreamCommitBuffer 在提交 200 状态码前继续拦截流内可重试错误。
func (c *NormalizedChunk) HasVisibleContent() bool {
	if c == nil {
		return false
	}
	return c.Content != "" ||
		c.ReasoningContent != "" ||
		len(c.ToolCalls) > 0 ||
		c.FinishReason != ""
}

// StreamDeltaNormalizer 对 WorkBuddy `/v2/chat/completions` 的 SSE 流执行增量清洗：
//   - 跳过 `: heartbeat` 注释行与空行
//   - 剥离 delta 中多余的空占位字段（tool_calls: [] / function_call: null / refusal: "" / extra_fields: null）
//   - 统一 reasoning -> reasoning_content
//   - 识别 200 SSE 流内隐藏的 WorkBuddy 业务错误包（如 code=11128 / error 对象）
//   - 提取尾帧 usage（prompt_tokens / completion_tokens / cached_tokens / reasoning_tokens / credit）
type StreamDeltaNormalizer struct {
	rewriteModel string
	rewriteID    string
	created      int64
	toolSession  *toolobf.Session
	pendingTools map[int]*pendingObfTool
}

type pendingObfTool struct {
	name string
	args string
	id   string
	typ  string
}

// NewStreamDeltaNormalizer 构造流式增量清洗器。
func NewStreamDeltaNormalizer(model, completionID string, created int64) *StreamDeltaNormalizer {
	return &StreamDeltaNormalizer{
		rewriteModel: model,
		rewriteID:    completionID,
		created:      created,
	}
}

// SetToolSession 绑定当次请求的工具名混淆双向会话，用于在下游解析与转发前无损还原工具名。
func (n *StreamDeltaNormalizer) SetToolSession(sess *toolobf.Session) {
	if n == nil {
		return
	}
	n.toolSession = sess
}

// NormalizeLine 解析并清洗单行 SSE 文本。
//
// 返回值：
//   - chunk: 若为有效数据帧或流内错误帧，返回结构化结果
//   - done:  遇到 `data: [DONE]` 时为 true
//   - skip:  空行、`: heartbeat` 注释行或其他非 `data:` 控制行时为 true
func (n *StreamDeltaNormalizer) NormalizeLine(line string) (chunk *NormalizedChunk, done bool, skip bool) {
	line = strings.TrimRight(line, "\r\n")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return nil, false, true
	}

	after, found := strings.CutPrefix(trimmed, "data:")
	if !found {
		// event: / id: / retry: 等非 data 行在标准 OpenAI SSE 中不承载内容，跳过。
		return nil, false, true
	}
	payload := strings.TrimSpace(after)
	if payload == "" {
		return nil, false, true
	}
	if payload == "[DONE]" {
		return nil, true, false
	}

	return n.NormalizePayload(payload), false, false
}

// NormalizePayload 解析并清洗去掉 `data:` 前缀后的单条 JSON 载荷。
func (n *StreamDeltaNormalizer) NormalizePayload(payload string) *NormalizedChunk {
	out := &NormalizedChunk{RawPayload: payload}

	var rawMap map[string]any
	if err := json.Unmarshal([]byte(payload), &rawMap); err != nil {
		// 上游偶发非 JSON 噪音帧，按无可见内容的空帧跳过即可，不中断整条流。
		return out
	}

	// 1. 检测流内隐藏错误体（OpenAI `error` 字段或腾讯/WorkBuddy `code != 0` 包）
	if mappedErr := extractInStreamError(rawMap, payload); mappedErr != nil {
		out.Err = mappedErr
		return out
	}

	// 2. 提取 usage
	if uMap, ok := rawMap["usage"].(map[string]any); ok {
		out.Usage = parseNormalizedUsage(uMap)
	}

	// 3. 清洗 choices[].delta（并还原已混淆的工具名）
	if choicesRaw, ok := rawMap["choices"].([]any); ok && len(choicesRaw) > 0 {
		out.HasChoices = true
		cleanedChoices := make([]any, 0, len(choicesRaw))
		for _, chRaw := range choicesRaw {
			ch, ok := chRaw.(map[string]any)
			if !ok {
				continue
			}
			chCopy := cloneJSONMap(ch)
			fr := stringField(chCopy, "finish_reason")
			if fr != "" && out.FinishReason == "" {
				out.FinishReason = fr
			}

			var cleanedDelta map[string]any
			if delta, ok := chCopy["delta"].(map[string]any); ok {
				cleanedDelta = sanitizeDeltaMap(delta)
				if n.toolSession != nil {
					n.restoreToolCallsInDelta(cleanedDelta)
				}
			}
			if fr != "" && n.toolSession != nil && len(n.pendingTools) > 0 {
				if cleanedDelta == nil {
					cleanedDelta = map[string]any{}
				}
				n.flushPendingToolsIntoDelta(cleanedDelta)
			}
			if cleanedDelta != nil {
				chCopy["delta"] = cleanedDelta

				if role := stringField(cleanedDelta, "role"); role != "" && out.Role == "" {
					out.Role = role
				}
				if content := stringField(cleanedDelta, "content"); content != "" {
					out.Content += content
				}
				if rc := stringField(cleanedDelta, "reasoning_content"); rc != "" {
					out.ReasoningContent += rc
				}
				if tcs := parseToolCallDeltas(cleanedDelta["tool_calls"]); len(tcs) > 0 {
					out.ToolCalls = append(out.ToolCalls, tcs...)
				}
			}
			cleanedChoices = append(cleanedChoices, chCopy)
		}
		rawMap["choices"] = cleanedChoices
	}

	if n.rewriteID != "" {
		rawMap["id"] = n.rewriteID
	}
	if n.rewriteModel != "" {
		rawMap["model"] = n.rewriteModel
	}
	if n.created > 0 {
		rawMap["created"] = n.created
	}
	if _, ok := rawMap["object"]; !ok {
		rawMap["object"] = "chat.completion.chunk"
	}

	if cleaned, err := json.Marshal(rawMap); err == nil {
		out.CleanJSON = cleaned
	} else {
		out.CleanJSON = []byte(payload)
	}
	return out
}

// Stream 逐行读取上游 SSE body（带空闲超时看门狗）并将清洗后的分片回调给 handle。
func (n *StreamDeltaNormalizer) Stream(ctx context.Context, body io.ReadCloser, idle time.Duration, handle func(*NormalizedChunk)) error {
	if idle <= 0 {
		idle = 180 * time.Second
	}
	return n.StreamWithTimeouts(ctx, body, idle, idle, handle)
}

// StreamWithTimeouts 逐行读取上游 SSE body：
//   - 在收到首个可见内容分片前使用 firstTokenTimeout（默认 300s），避免长思考/大上下文预填充被误杀；
//   - 一旦产生首个可见内容分片，自动切换为后续分片空闲超时 idleTimeout（默认 180s）。
func (n *StreamDeltaNormalizer) StreamWithTimeouts(
	ctx context.Context,
	body io.ReadCloser,
	firstTokenTimeout time.Duration,
	idleTimeout time.Duration,
	handle func(*NormalizedChunk),
) error {
	if idleTimeout <= 0 {
		idleTimeout = 180 * time.Second
	}
	if firstTokenTimeout <= 0 {
		firstTokenTimeout = 300 * time.Second
	}

	watchdog := time.AfterFunc(firstTokenTimeout, func() { _ = body.Close() })
	defer watchdog.Stop()

	scanner := bufio.NewScanner(body)
	// 支持超大 tool_calls 参数分片或长上下文块。
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	sawVisible := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sawVisible {
			watchdog.Reset(idleTimeout)
		} else {
			watchdog.Reset(firstTokenTimeout)
		}
		chunk, done, skip := n.NormalizeLine(scanner.Text())
		if done {
			return nil
		}
		if skip || chunk == nil {
			continue
		}
		if !sawVisible && chunk.HasVisibleContent() {
			sawVisible = true
			watchdog.Reset(idleTimeout)
		}
		handle(chunk)
		if chunk.Err != nil {
			return nil
		}
	}
	return scanner.Err()
}

// sanitizeDeltaMap 清理 delta 中的空占位字段，防止客户端思考块断裂或工具识别错乱。
func sanitizeDeltaMap(delta map[string]any) map[string]any {
	out := make(map[string]any, len(delta))
	for k, v := range delta {
		out[k] = v
	}

	// 将上游偶发的 reasoning 统一提升为 reasoning_content
	if rc := stringField(out, "reasoning_content"); rc == "" {
		if r := stringField(out, "reasoning"); r != "" {
			out["reasoning_content"] = r
		}
	}
	delete(out, "reasoning")

	// 剥离空 tool_calls: [] / null
	if tcRaw, exists := out["tool_calls"]; exists {
		if tcRaw == nil {
			delete(out, "tool_calls")
		} else if tcs, ok := tcRaw.([]any); ok && len(tcs) == 0 {
			delete(out, "tool_calls")
		}
	}

	// 剥离空 function_call: null
	if fcRaw, exists := out["function_call"]; exists && fcRaw == nil {
		delete(out, "function_call")
	}

	// 剥离空 refusal: "" / null
	if refRaw, exists := out["refusal"]; exists {
		if refRaw == nil {
			delete(out, "refusal")
		} else if s, ok := refRaw.(string); ok && s == "" {
			delete(out, "refusal")
		}
	}

	// 剥离 extra_fields: null / 空对象
	if efRaw, exists := out["extra_fields"]; exists {
		if efRaw == nil {
			delete(out, "extra_fields")
		} else if m, ok := efRaw.(map[string]any); ok && len(m) == 0 {
			delete(out, "extra_fields")
		}
	}

	return out
}

func parseToolCallDeltas(v any) []ToolCallDelta {
	raw, ok := v.([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]ToolCallDelta, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		idx := 0
		if iv := intField(m, "index"); iv != nil {
			idx = *iv
		}
		tc := ToolCallDelta{
			Index: idx,
			ID:    stringField(m, "id"),
			Type:  stringField(m, "type"),
		}
		if fn, ok := m["function"].(map[string]any); ok {
			tc.Name = stringField(fn, "name")
			tc.Arguments = stringField(fn, "arguments")
		}
		out = append(out, tc)
	}
	return out
}

func parseNormalizedUsage(u map[string]any) *NormalizedUsage {
	nu := &NormalizedUsage{}
	if v := intField(u, "prompt_tokens", "input_tokens"); v != nil {
		nu.PromptTokens = *v
	}
	if v := intField(u, "completion_tokens", "output_tokens"); v != nil {
		nu.CompletionTokens = *v
	}
	if v := intField(u, "total_tokens"); v != nil {
		nu.TotalTokens = *v
	} else {
		nu.TotalTokens = nu.PromptTokens + nu.CompletionTokens
	}
	if details, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if v := intField(details, "cached_tokens"); v != nil {
			nu.CachedTokens = *v
		}
	} else if details, ok := u["input_tokens_details"].(map[string]any); ok {
		if v := intField(details, "cached_tokens"); v != nil {
			nu.CachedTokens = *v
		}
	}
	if details, ok := u["completion_tokens_details"].(map[string]any); ok {
		if v := intField(details, "reasoning_tokens"); v != nil {
			nu.ReasoningTokens = *v
		}
	} else if details, ok := u["output_tokens_details"].(map[string]any); ok {
		if v := intField(details, "reasoning_tokens"); v != nil {
			nu.ReasoningTokens = *v
		}
	}
	if cr := floatField(u, "credit"); cr != nil {
		nu.Credit = *cr
	}
	return nu
}

func extractInStreamError(rawMap map[string]any, payload string) *MappedError {
	if errRaw, exists := rawMap["error"]; exists && errRaw != nil {
		apiErr := &workbuddy.APIError{
			Status: http.StatusBadGateway,
			Raw:    payload,
		}
		switch v := errRaw.(type) {
		case map[string]any:
			apiErr.Message = firstNonEmptyStr(stringField(v, "message"), stringField(v, "msg"), payload)
			if codeInt := parseAnyInt(v["code"]); codeInt != 0 {
				apiErr.Code = codeInt
			}
			if st := parseAnyInt(v["status"]); st >= 400 {
				apiErr.Status = st
			}
		case string:
			apiErr.Message = v
		default:
			apiErr.Message = payload
		}
		return MapWorkBuddyError(apiErr)
	}

	// 兼容 WorkBuddy 流内 {"code": 11128, "msg": "..."} 业务错误（通常不含有效 choices）
	if codeRaw, hasCode := rawMap["code"]; hasCode {
		codeInt := parseAnyInt(codeRaw)
		choices, _ := rawMap["choices"].([]any)
		if codeInt != 0 && len(choices) == 0 {
			msg := firstNonEmptyStr(stringField(rawMap, "msg"), stringField(rawMap, "message"), payload)
			apiErr := &workbuddy.APIError{
				Status:  http.StatusBadGateway,
				Code:    codeInt,
				Message: msg,
				Raw:     payload,
			}
			return MapWorkBuddyError(apiErr)
		}
	}
	return nil
}

func parseAnyInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		return 0
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, s := range vals {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// StreamCommitBuffer 缓冲首个有效数据帧到达前的所有前导数据，延迟提交 HTTP 200 状态码与响应头。
//
// 价值：如果上游虽然返回了 HTTP 200 建立 SSE 流，但在产生任何有效文本/思考/工具调用前
// 就在流内吐出了可重试错误（如 code=11128 额度不足、限流、认证失败或直接空流断开），
// Relay 层可以在“尚未向客户端写出任何字节”的状态下无缝切换到账号池内的下一个账号重试！
type StreamCommitBuffer struct {
	w             http.ResponseWriter
	streaming     bool
	committed     bool
	initialFrames []string
	pendingFrames []string
	writeErr      error
}

// NewStreamCommitBuffer 构造提交缓冲器。initialFrames 仅在确认收到首个有效输出后与 200 响应头一并下发。
func NewStreamCommitBuffer(w http.ResponseWriter, streaming bool, initialFrames ...string) *StreamCommitBuffer {
	buf := &StreamCommitBuffer{
		w:         w,
		streaming: streaming,
	}
	for _, f := range initialFrames {
		if f != "" {
			buf.initialFrames = append(buf.initialFrames, f)
		}
	}
	return buf
}

// IsCommitted 返回响应头与首个有效响应是否已正式提交给下游客户端。
func (b *StreamCommitBuffer) IsCommitted() bool {
	return b.committed
}

// WriteError 返回流写出期间遇到的 I/O 错误（通常是客户端断连）。
func (b *StreamCommitBuffer) WriteError() error {
	return b.writeErr
}

// CommitSSE 正式向客户端提交 SSE 响应头（200 OK）并刷出前导控制帧。
func (b *StreamCommitBuffer) CommitSSE() error {
	if b.committed {
		return nil
	}
	b.committed = true
	b.w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	b.w.Header().Set("Cache-Control", "no-cache")
	b.w.Header().Set("Connection", "keep-alive")
	b.w.Header().Set("X-Accel-Buffering", "no")
	b.w.WriteHeader(http.StatusOK)

	for _, f := range b.initialFrames {
		b.writeRaw(f)
	}
	b.initialFrames = nil
	for _, f := range b.pendingFrames {
		b.writeRaw(f)
	}
	b.pendingFrames = nil
	b.flush()
	return b.writeErr
}

// WriteFrame 写入一帧 SSE 数据。若尚未提交且 commitNow 为 false，则先暂存在缓冲区等待首个有效分片触发提交。
func (b *StreamCommitBuffer) WriteFrame(frame string, commitNow bool) error {
	if frame == "" || b.writeErr != nil {
		return b.writeErr
	}
	if !b.committed {
		if !commitNow {
			b.pendingFrames = append(b.pendingFrames, frame)
			return nil
		}
		if err := b.CommitSSE(); err != nil {
			return err
		}
	}
	b.writeRaw(frame)
	b.flush()
	return b.writeErr
}

func (b *StreamCommitBuffer) writeRaw(frame string) {
	if b.writeErr != nil || frame == "" {
		return
	}
	if _, err := io.WriteString(b.w, frame); err != nil {
		b.writeErr = err
	}
}

func (b *StreamCommitBuffer) flush() {
	if b.writeErr != nil {
		return
	}
	if f, ok := b.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (n *StreamDeltaNormalizer) restoreToolCallsInDelta(delta map[string]any) {
	if n == nil || n.toolSession == nil || delta == nil {
		return
	}
	if fc, ok := delta["function_call"].(map[string]any); ok {
		if name, _ := fc["name"].(string); name != "" {
			fcCopy := cloneJSONMap(fc)
			fcCopy["name"] = n.toolSession.Restore(name)
			delta["function_call"] = fcCopy
		}
	}

	rawTCs, ok := delta["tool_calls"].([]any)
	if !ok || len(rawTCs) == 0 {
		return
	}
	kept := make([]any, 0, len(rawTCs))
	for _, item := range rawTCs {
		m, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		mCopy := cloneJSONMap(m)
		idx := 0
		if iv := intField(mCopy, "index"); iv != nil {
			idx = *iv
		}
		id := stringField(mCopy, "id")
		typ := stringField(mCopy, "type")
		fn, hasFn := mCopy["function"].(map[string]any)
		var fnCopy map[string]any
		var rawName, rawArgs string
		if hasFn {
			fnCopy = cloneJSONMap(fn)
			rawName, _ = fnCopy["name"].(string)
			rawArgs, _ = fnCopy["arguments"].(string)
		}

		prev := n.pendingTools[idx]
		combined := rawName
		combinedArgs := rawArgs
		if prev != nil {
			combined = prev.name + rawName
			combinedArgs = prev.args + rawArgs
		}

		if combined != "" {
			isProperPrefix := n.toolSession.IsObfuscatedPrefix(combined)
			restored := n.toolSession.Restore(combined)

			// 1) 若 combined 仍是某个更长混淆名的严格真前缀：
			//    - 当尚无参数到达（combinedArgs == ""）时，即使 combined 本身也是短混淆名（如 exec_task vs exec_task_2），也必须暂存以等待下一片消歧；
			//    - 当 combined 尚未命中完整混淆名（restored == combined）时，即便同片已携带部分参数也继续暂存，防止半截混淆名泄漏。
			if isProperPrefix && (combinedArgs == "" || restored == combined) {
				if n.pendingTools == nil {
					n.pendingTools = make(map[int]*pendingObfTool)
				}
				heldID := id
				heldTyp := typ
				if prev != nil {
					if heldID == "" {
						heldID = prev.id
					}
					if heldTyp == "" {
						heldTyp = prev.typ
					}
				}
				n.pendingTools[idx] = &pendingObfTool{
					name: combined,
					args: combinedArgs,
					id:   heldID,
					typ:  heldTyp,
				}
				continue
			}

			if prev != nil {
				if id == "" && prev.id != "" {
					mCopy["id"] = prev.id
				}
				if typ == "" && prev.typ != "" {
					mCopy["type"] = prev.typ
				}
				delete(n.pendingTools, idx)
			}
			if fnCopy == nil {
				fnCopy = map[string]any{}
			}
			fnCopy["name"] = restored
			if prev != nil && prev.args != "" {
				fnCopy["arguments"] = combinedArgs
			}
			mCopy["function"] = fnCopy
			kept = append(kept, mCopy)
			continue
		}

		if prev != nil && rawArgs != "" {
			if id == "" && prev.id != "" {
				mCopy["id"] = prev.id
			}
			if typ == "" && prev.typ != "" {
				mCopy["type"] = prev.typ
			}
			if fnCopy == nil {
				fnCopy = map[string]any{}
			}
			fnCopy["name"] = n.toolSession.Restore(prev.name)
			fnCopy["arguments"] = combinedArgs
			mCopy["function"] = fnCopy
			delete(n.pendingTools, idx)
		}
		kept = append(kept, mCopy)
	}

	if len(kept) == 0 {
		delete(delta, "tool_calls")
	} else {
		delta["tool_calls"] = kept
	}
}

func (n *StreamDeltaNormalizer) flushPendingToolsIntoDelta(delta map[string]any) {
	if n == nil || n.toolSession == nil || len(n.pendingTools) == 0 || delta == nil {
		return
	}
	idxs := make([]int, 0, len(n.pendingTools))
	for idx := range n.pendingTools {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)

	var existing []any
	if cur, ok := delta["tool_calls"].([]any); ok {
		existing = cur
	}
	for _, idx := range idxs {
		p := n.pendingTools[idx]
		delete(n.pendingTools, idx)
		if p == nil || p.name == "" {
			continue
		}
		fnMap := map[string]any{
			"name": n.toolSession.Restore(p.name),
		}
		if p.args != "" {
			fnMap["arguments"] = p.args
		}
		entry := map[string]any{
			"index":    idx,
			"function": fnMap,
		}
		if p.id != "" {
			entry["id"] = p.id
		}
		if p.typ != "" {
			entry["type"] = p.typ
		}
		existing = append(existing, entry)
	}
	if len(existing) > 0 {
		delta["tool_calls"] = existing
	}
}
