package relay

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ResponsesStreamEncoder 将标准化上游增量分片编码为 OpenAI Responses API (/v1/responses) SSE 事件流。
//
// 兼容 Codex CLI 与 OpenAI Responses SDK：
//   - 前导事件：response.created -> response.in_progress
//   - 思考输出：output_item.added(reasoning) -> reasoning_summary_part.added ->
//     reasoning_summary_text.delta -> reasoning_summary_text.done ->
//     reasoning_summary_part.done -> output_item.done
//   - 文本输出：output_item.added(message) -> content_part.added(output_text) ->
//     output_text.delta -> output_text.done -> content_part.done -> output_item.done
//   - 工具调用：output_item.added(function_call) -> function_call_arguments.delta ->
//     function_call_arguments.done -> output_item.done
//   - 完成事件：response.completed / response.incomplete
type ResponsesStreamEncoder struct {
	responseID string
	model      string
	createdAt  int64

	seq             int
	startedEmitted  bool
	nextOutputIndex int

	reasoningOpen    bool
	reasoningClosed  bool
	reasoningIndex   int
	reasoningItemID  string
	reasoningBuf     strings.Builder
	reasoningSummary bool

	messageOpen   bool
	messageClosed bool
	messageIndex  int
	messageItemID string
	messageBuf    strings.Builder

	activeToolCalls map[int]*respToolCallState
	toolOrder       []int
	customTools     map[string]bool

	completedItems []map[string]any
	finishReason   string
	sawFinish      bool
	usage          NormalizedUsage
}

type respToolCallState struct {
	outputIndex int
	itemID      string
	callID      string
	name        string
	isCustom    bool
	added       bool
	args        strings.Builder
	closed      bool
}

// NewResponsesStreamEncoder 构造一次 Responses API 流式编码器。
func NewResponsesStreamEncoder(model, requestID string, createdAt int64) *ResponsesStreamEncoder {
	return &ResponsesStreamEncoder{
		responseID:      "resp_" + requestID,
		model:           model,
		createdAt:       createdAt,
		reasoningItemID: "rs_" + requestID,
		messageItemID:   "msg_" + requestID,
		activeToolCalls: make(map[int]*respToolCallState),
		finishReason:    "stop",
	}
}

// SetCustomTools 声明入站 Responses 请求中注册为 type="custom" 的工具名集合（如 Codex apply_patch），
// 使其输出为原生 custom_tool_call 事件。
func (e *ResponsesStreamEncoder) SetCustomTools(names map[string]bool) {
	if e == nil || len(names) == 0 {
		return
	}
	e.customTools = make(map[string]bool, len(names))
	for k, v := range names {
		if v && strings.TrimSpace(k) != "" {
			e.customTools[strings.ToLower(strings.TrimSpace(k))] = true
		}
	}
}

func (e *ResponsesStreamEncoder) isCustomTool(name string) bool {
	if e == nil || len(e.customTools) == 0 {
		return false
	}
	return e.customTools[strings.ToLower(strings.TrimSpace(name))]
}

func (e *ResponsesStreamEncoder) isCustomToolPrefix(partial string) bool {
	if e == nil || len(e.customTools) == 0 {
		return false
	}
	p := strings.ToLower(strings.TrimSpace(partial))
	if p == "" {
		return false
	}
	for full := range e.customTools {
		if full != p && strings.HasPrefix(full, p) {
			return true
		}
	}
	return false
}

func (e *ResponsesStreamEncoder) nextSeq() int {
	s := e.seq
	e.seq++
	return s
}

func (e *ResponsesStreamEncoder) sseFrame(eventType string, payload map[string]any) string {
	payload["type"] = eventType
	payload["sequence_number"] = e.nextSeq()
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return "event: " + eventType + "\ndata: " + string(b) + "\n\n"
}

func (e *ResponsesStreamEncoder) baseResponseObj(status string, output []map[string]any) map[string]any {
	if output == nil {
		output = []map[string]any{}
	}
	resp := map[string]any{
		"id":         e.responseID,
		"object":     "response",
		"created_at": e.createdAt,
		"status":     status,
		"model":      e.model,
		"output":     output,
		"error":      nil,
	}
	if status == "completed" || status == "incomplete" {
		resp["usage"] = buildResponsesUsageMap(&e.usage)
		if status == "incomplete" {
			resp["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
		} else {
			resp["incomplete_details"] = nil
		}
	}
	return resp
}

// EnsureStartedFrames 返回首轮响应必需的 response.created 与 response.in_progress 帧（仅生成一次）。
func (e *ResponsesStreamEncoder) EnsureStartedFrames() []string {
	if e.startedEmitted {
		return nil
	}
	e.startedEmitted = true
	return []string{
		e.sseFrame("response.created", map[string]any{
			"response": e.baseResponseObj("in_progress", nil),
		}),
		e.sseFrame("response.in_progress", map[string]any{
			"response": e.baseResponseObj("in_progress", nil),
		}),
	}
}

// EncodeChunk 消费一条清洗后的上游 SSE 分片，返回需要下发给客户端的 Responses SSE 帧。
func (e *ResponsesStreamEncoder) EncodeChunk(chunk *NormalizedChunk) []string {
	if chunk == nil {
		return nil
	}
	if chunk.Usage != nil {
		e.usage = *chunk.Usage
	}
	if chunk.FinishReason != "" {
		e.sawFinish = true
		e.finishReason = mapFinishReason(chunk.FinishReason)
	}

	var out []string
	if chunk.HasVisibleContent() && !e.startedEmitted {
		out = append(out, e.EnsureStartedFrames()...)
	}

	if chunk.ReasoningContent != "" {
		out = append(out, e.encodeReasoningDelta(chunk.ReasoningContent)...)
	}
	if chunk.Content != "" {
		out = append(out, e.encodeTextDelta(chunk.Content)...)
	}
	if len(chunk.ToolCalls) > 0 {
		for _, tc := range chunk.ToolCalls {
			out = append(out, e.encodeToolCallDelta(tc)...)
		}
	}
	return out
}

func (e *ResponsesStreamEncoder) encodeReasoningDelta(delta string) []string {
	var out []string
	if !e.reasoningOpen && !e.reasoningClosed {
		e.reasoningOpen = true
		e.reasoningIndex = e.nextOutputIndex
		e.nextOutputIndex++
		out = append(out, e.sseFrame("response.output_item.added", map[string]any{
			"output_index": e.reasoningIndex,
			"item": map[string]any{
				"id":      e.reasoningItemID,
				"type":    "reasoning",
				"status":  "in_progress",
				"summary": []any{},
			},
		}))
		e.reasoningSummary = true
		out = append(out, e.sseFrame("response.reasoning_summary_part.added", map[string]any{
			"item_id":       e.reasoningItemID,
			"output_index":  e.reasoningIndex,
			"summary_index": 0,
			"part": map[string]any{
				"type": "summary_text",
				"text": "",
			},
		}))
	}
	if e.reasoningOpen {
		e.reasoningBuf.WriteString(delta)
		out = append(out, e.sseFrame("response.reasoning_summary_text.delta", map[string]any{
			"item_id":       e.reasoningItemID,
			"output_index":  e.reasoningIndex,
			"summary_index": 0,
			"delta":         delta,
		}))
	}
	return out
}

func (e *ResponsesStreamEncoder) closeReasoningItem() []string {
	if !e.reasoningOpen {
		return nil
	}
	e.reasoningOpen = false
	e.reasoningClosed = true
	full := e.reasoningBuf.String()
	part := map[string]any{
		"type": "summary_text",
		"text": full,
	}
	completed := map[string]any{
		"id":      e.reasoningItemID,
		"type":    "reasoning",
		"status":  "completed",
		"summary": []any{part},
	}
	e.completedItems = append(e.completedItems, completed)

	return []string{
		e.sseFrame("response.reasoning_summary_text.done", map[string]any{
			"item_id":       e.reasoningItemID,
			"output_index":  e.reasoningIndex,
			"summary_index": 0,
			"text":          full,
		}),
		e.sseFrame("response.reasoning_summary_part.done", map[string]any{
			"item_id":       e.reasoningItemID,
			"output_index":  e.reasoningIndex,
			"summary_index": 0,
			"part":          part,
		}),
		e.sseFrame("response.output_item.done", map[string]any{
			"output_index": e.reasoningIndex,
			"item":         completed,
		}),
	}
}

func (e *ResponsesStreamEncoder) encodeTextDelta(delta string) []string {
	var out []string
	out = append(out, e.closeReasoningItem()...)

	if !e.messageOpen && !e.messageClosed {
		e.messageOpen = true
		e.messageIndex = e.nextOutputIndex
		e.nextOutputIndex++
		out = append(out, e.sseFrame("response.output_item.added", map[string]any{
			"output_index": e.messageIndex,
			"item": map[string]any{
				"id":      e.messageItemID,
				"type":    "message",
				"status":  "in_progress",
				"role":    "assistant",
				"content": []any{},
			},
		}))
		out = append(out, e.sseFrame("response.content_part.added", map[string]any{
			"item_id":       e.messageItemID,
			"output_index":  e.messageIndex,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		}))
	}
	if e.messageOpen {
		e.messageBuf.WriteString(delta)
		out = append(out, e.sseFrame("response.output_text.delta", map[string]any{
			"item_id":       e.messageItemID,
			"output_index":  e.messageIndex,
			"content_index": 0,
			"delta":         delta,
			"logprobs":      []any{},
		}))
	}
	return out
}

func (e *ResponsesStreamEncoder) closeMessageItem() []string {
	if !e.messageOpen {
		return nil
	}
	e.messageOpen = false
	e.messageClosed = true
	full := e.messageBuf.String()
	part := map[string]any{
		"type":        "output_text",
		"text":        full,
		"annotations": []any{},
	}
	completed := map[string]any{
		"id":      e.messageItemID,
		"type":    "message",
		"status":  "completed",
		"role":    "assistant",
		"content": []any{part},
	}
	e.completedItems = append(e.completedItems, completed)

	return []string{
		e.sseFrame("response.output_text.done", map[string]any{
			"item_id":       e.messageItemID,
			"output_index":  e.messageIndex,
			"content_index": 0,
			"text":          full,
			"logprobs":      []any{},
		}),
		e.sseFrame("response.content_part.done", map[string]any{
			"item_id":       e.messageItemID,
			"output_index":  e.messageIndex,
			"content_index": 0,
			"part":          part,
		}),
		e.sseFrame("response.output_item.done", map[string]any{
			"output_index": e.messageIndex,
			"item":         completed,
		}),
	}
}

func (e *ResponsesStreamEncoder) emitToolCallAdded(st *respToolCallState) []string {
	if st == nil || st.added {
		return nil
	}
	st.added = true
	if st.name == "" {
		st.name = "tool"
	}
	st.isCustom = e.isCustomTool(st.name)
	if st.isCustom {
		st.itemID = "ctc_" + strings.TrimPrefix(st.callID, "ctc_")
		return []string{
			e.sseFrame("response.output_item.added", map[string]any{
				"output_index": st.outputIndex,
				"item": map[string]any{
					"id":      st.itemID,
					"type":    "custom_tool_call",
					"status":  "in_progress",
					"call_id": st.callID,
					"name":    st.name,
					"input":   "",
				},
			}),
		}
	}
	st.itemID = "fc_" + strings.TrimPrefix(st.callID, "fc_")
	out := []string{
		e.sseFrame("response.output_item.added", map[string]any{
			"output_index": st.outputIndex,
			"item": map[string]any{
				"id":        st.itemID,
				"type":      "function_call",
				"status":    "in_progress",
				"call_id":   st.callID,
				"name":      st.name,
				"arguments": "",
			},
		}),
	}
	if st.args.Len() > 0 {
		out = append(out, e.sseFrame("response.function_call_arguments.delta", map[string]any{
			"item_id":      st.itemID,
			"output_index": st.outputIndex,
			"delta":        st.args.String(),
		}))
	}
	return out
}

func (e *ResponsesStreamEncoder) encodeToolCallDelta(tc ToolCallDelta) []string {
	var out []string
	out = append(out, e.closeReasoningItem()...)
	out = append(out, e.closeMessageItem()...)

	st, exists := e.activeToolCalls[tc.Index]
	if !exists {
		outIdx := e.nextOutputIndex
		e.nextOutputIndex++
		callID := tc.ID
		if callID == "" {
			callID = "call_" + strconv.FormatInt(e.createdAt, 36) + "_" + strconv.Itoa(tc.Index)
		}
		st = &respToolCallState{
			outputIndex: outIdx,
			itemID:      "fc_" + strings.TrimPrefix(callID, "fc_"),
			callID:      callID,
			name:        tc.Name,
		}
		e.activeToolCalls[tc.Index] = st
		e.toolOrder = append(e.toolOrder, tc.Index)
	} else {
		if tc.ID != "" && st.callID == "" {
			st.callID = tc.ID
		}
		if tc.Name != "" {
			st.name += tc.Name
		}
	}

	if !st.added {
		if tc.Arguments != "" {
			st.args.WriteString(tc.Arguments)
		}
		if st.name != "" && (!e.isCustomToolPrefix(st.name) || st.args.Len() > 0) {
			out = append(out, e.emitToolCallAdded(st)...)
		}
		return out
	}

	if tc.Arguments != "" {
		st.args.WriteString(tc.Arguments)
		if !st.isCustom {
			out = append(out, e.sseFrame("response.function_call_arguments.delta", map[string]any{
				"item_id":      st.itemID,
				"output_index": st.outputIndex,
				"delta":        tc.Arguments,
			}))
		}
	}
	return out
}

func (e *ResponsesStreamEncoder) closeToolCall(st *respToolCallState) []string {
	if st == nil || st.closed {
		return nil
	}
	var prefix []string
	if !st.added {
		prefix = e.emitToolCallAdded(st)
	}
	st.closed = true
	args := st.args.String()
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	if st.isCustom {
		rawInput := extractCustomToolInputText(args)
		completed := map[string]any{
			"id":      st.itemID,
			"type":    "custom_tool_call",
			"status":  "completed",
			"call_id": st.callID,
			"name":    st.name,
			"input":   rawInput,
		}
		e.completedItems = append(e.completedItems, completed)
		frames := append([]string(nil), prefix...)
		if rawInput != "" {
			frames = append(frames, e.sseFrame("response.custom_tool_call_input.delta", map[string]any{
				"item_id":      st.itemID,
				"output_index": st.outputIndex,
				"delta":        rawInput,
			}))
		}
		frames = append(frames,
			e.sseFrame("response.custom_tool_call_input.done", map[string]any{
				"item_id":      st.itemID,
				"output_index": st.outputIndex,
				"input":        rawInput,
			}),
			e.sseFrame("response.output_item.done", map[string]any{
				"output_index": st.outputIndex,
				"item":         completed,
			}),
		)
		return frames
	}

	completed := map[string]any{
		"id":        st.itemID,
		"type":      "function_call",
		"status":    "completed",
		"call_id":   st.callID,
		"name":      st.name,
		"arguments": args,
	}
	e.completedItems = append(e.completedItems, completed)

	return append(prefix,
		e.sseFrame("response.function_call_arguments.done", map[string]any{
			"item_id":      st.itemID,
			"output_index": st.outputIndex,
			"name":         st.name,
			"arguments":    args,
		}),
		e.sseFrame("response.output_item.done", map[string]any{
			"output_index": st.outputIndex,
			"item":         completed,
		}),
	)
}

func extractCustomToolInputText(argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil && obj != nil {
		if s, ok := obj["input"].(string); ok {
			return s
		}
		if s, ok := obj["patch"].(string); ok {
			return s
		}
		if s, ok := obj["text"].(string); ok {
			return s
		}
	}
	var plainStr string
	if err := json.Unmarshal([]byte(trimmed), &plainStr); err == nil {
		return plainStr
	}
	return trimmed
}

// Finish 关闭所有尚未结束的输出项，并生成终态 response.completed / response.incomplete 事件。
func (e *ResponsesStreamEncoder) Finish() []string {
	var out []string
	if !e.startedEmitted {
		out = append(out, e.EnsureStartedFrames()...)
	}
	out = append(out, e.closeReasoningItem()...)
	out = append(out, e.closeMessageItem()...)
	for _, idx := range e.toolOrder {
		out = append(out, e.closeToolCall(e.activeToolCalls[idx])...)
	}

	eventName := "response.completed"
	status := "completed"
	if e.finishReason == "length" {
		eventName = "response.incomplete"
		status = "incomplete"
	}
	out = append(out, e.sseFrame(eventName, map[string]any{
		"response": e.baseResponseObj(status, e.completedItems),
	}))
	return out
}

// Usage 返回编码器累计的标准化用量。
func (e *ResponsesStreamEncoder) Usage() NormalizedUsage {
	if e == nil {
		return NormalizedUsage{}
	}
	return e.usage
}

// SawFinish 返回是否已收到上游完成原因信号。
func (e *ResponsesStreamEncoder) SawFinish() bool {
	return e != nil && (e.sawFinish || e.usage.PromptTokens > 0 || e.usage.CompletionTokens > 0)
}

func buildResponsesUsageMap(u *NormalizedUsage) map[string]any {
	if u == nil {
		u = &NormalizedUsage{}
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	return map[string]any{
		"input_tokens": u.PromptTokens,
		"input_tokens_details": map[string]any{
			"cached_tokens": u.CachedTokens,
		},
		"output_tokens": u.CompletionTokens,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": u.ReasoningTokens,
		},
		"total_tokens": total,
	}
}

// BuildResponsesFullResponse 组装非流式 /v1/responses 标准 JSON 响应体。
func BuildResponsesFullResponse(
	model, requestID string,
	createdAt int64,
	text, reasoning string,
	toolCalls []openAIToolCall,
	finishReason string,
	usage *NormalizedUsage,
) map[string]any {
	return BuildResponsesFullResponseWithCustomTools(model, requestID, createdAt, text, reasoning, toolCalls, finishReason, usage, nil)
}

// BuildResponsesFullResponseWithCustomTools 组装非流式 /v1/responses 响应体（支持 Codex custom_tool_call 还原）。
func BuildResponsesFullResponseWithCustomTools(
	model, requestID string,
	createdAt int64,
	text, reasoning string,
	toolCalls []openAIToolCall,
	finishReason string,
	usage *NormalizedUsage,
	customTools map[string]bool,
) map[string]any {
	customLower := make(map[string]bool, len(customTools))
	for k, v := range customTools {
		if v && strings.TrimSpace(k) != "" {
			customLower[strings.ToLower(strings.TrimSpace(k))] = true
		}
	}
	output := make([]map[string]any, 0, len(toolCalls)+2)
	if reasoning != "" {
		output = append(output, map[string]any{
			"id":     "rs_" + requestID,
			"type":   "reasoning",
			"status": "completed",
			"summary": []any{map[string]any{
				"type": "summary_text",
				"text": reasoning,
			}},
		})
	}
	if text != "" || len(toolCalls) == 0 {
		output = append(output, map[string]any{
			"id":     "msg_" + requestID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		})
	}
	for _, call := range toolCalls {
		callID := call.ID
		if callID == "" {
			callID = "call_" + requestID
		}
		args := call.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		if customLower[strings.ToLower(strings.TrimSpace(call.Function.Name))] {
			output = append(output, map[string]any{
				"id":      "ctc_" + strings.TrimPrefix(callID, "ctc_"),
				"type":    "custom_tool_call",
				"status":  "completed",
				"call_id": callID,
				"name":    call.Function.Name,
				"input":   extractCustomToolInputText(args),
			})
			continue
		}
		output = append(output, map[string]any{
			"id":        "fc_" + strings.TrimPrefix(callID, "fc_"),
			"type":      "function_call",
			"status":    "completed",
			"call_id":   callID,
			"name":      call.Function.Name,
			"arguments": args,
		})
	}

	status := "completed"
	var incompleteDetails any
	if mapFinishReason(finishReason) == "length" {
		status = "incomplete"
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	}

	return map[string]any{
		"id":                 "resp_" + requestID,
		"object":             "response",
		"created_at":         createdAt,
		"status":             status,
		"model":              model,
		"output":             output,
		"usage":              buildResponsesUsageMap(usage),
		"error":              nil,
		"incomplete_details": incompleteDetails,
	}
}

func (s *Service) writeResponsesFullResponse(
	w http.ResponseWriter,
	model, requestID string,
	createdAt int64,
	text, reasoning string,
	toolCalls []openAIToolCall,
	finishReason string,
	usage *NormalizedUsage,
	customTools map[string]bool,
) {
	body := BuildResponsesFullResponseWithCustomTools(model, requestID, createdAt, text, reasoning, toolCalls, finishReason, usage, customTools)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}
