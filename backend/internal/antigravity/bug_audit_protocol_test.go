package antigravity

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type trackingBlockingReader struct {
	closedCh chan struct{}
	closed   atomic.Bool
}

func newTrackingBlockingReader() *trackingBlockingReader {
	return &trackingBlockingReader{closedCh: make(chan struct{})}
}

func (r *trackingBlockingReader) Read(_ []byte) (int, error) {
	<-r.closedCh
	return 0, io.ErrClosedPipe
}

func (r *trackingBlockingReader) Close() error {
	if r.closed.CompareAndSwap(false, true) {
		close(r.closedCh)
	}
	return nil
}

func TestTranslateV1InternalSSE_InStreamErrorLargePayloadAndCancel(t *testing.T) {
	t.Run("in_stream_error_not_swallowed_as_stop", func(t *testing.T) {
		src := io.NopCloser(strings.NewReader(
			`data: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded for model"}}` + "\n\n",
		))
		out := TranslateV1InternalSSEToOpenAISSE(context.Background(), src, "claude-sonnet-4-6", "cmpl-err")
		defer out.Close()

		raw, err := io.ReadAll(out)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		text := string(raw)
		if !strings.Contains(text, `"code":429`) || !strings.Contains(text, "Quota exceeded") {
			t.Fatalf("expected upstream 429 error frame preserved, got:\n%s", text)
		}
		if strings.Contains(text, `"finish_reason":"stop"`) || strings.Contains(text, "[DONE]") {
			t.Fatalf("in-stream error must not fabricate finish_reason=stop or [DONE]:\n%s", text)
		}
	})

	t.Run("large_tool_call_above_64kb_scanner_default", func(t *testing.T) {
		bigContent := strings.Repeat("A", 150*1024) // 150KB > 64KB default bufio.Scanner limit
		sse := fmt.Sprintf(
			"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"write_file\",\"id\":\"fc_big\",\"args\":{\"content\":%q}}}]},\"finishReason\":\"STOP\"}]}}\n\n",
			bigContent,
		)
		out := TranslateV1InternalSSEToOpenAISSE(context.Background(), io.NopCloser(strings.NewReader(sse)), "claude-sonnet-4-6", "cmpl-big")
		defer out.Close()

		raw, err := io.ReadAll(out)
		if err != nil {
			t.Fatalf("ReadAll large SSE failed: %v", err)
		}
		text := string(raw)
		if !strings.Contains(text, `"name":"write_file"`) || !strings.Contains(text, bigContent[:1024]) || !strings.Contains(text, `"finish_reason":"tool_calls"`) {
			t.Fatalf("large >64KB tool call payload failed to translate cleanly (len=%d)", len(text))
		}
	})

	t.Run("context_cancel_closes_blocked_upstream_reader", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		src := newTrackingBlockingReader()
		out := TranslateV1InternalSSEToOpenAISSE(ctx, src, "claude-sonnet-4-6", "cmpl-cancel")

		done := make(chan struct{})
		go func() {
			_, _ = io.ReadAll(out)
			close(done)
		}()

		time.Sleep(20 * time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("TranslateV1InternalSSEToOpenAISSE leaked goroutine / hung after context cancellation")
		}
		if !src.closed.Load() {
			t.Fatal("expected underlying src ReadCloser to be closed upon context cancellation")
		}
	})
}
