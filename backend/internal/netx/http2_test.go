package netx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigureHTTP2TransportNegotiatesH2(t *testing.T) {
	var seenProto atomic.Value
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenProto.Store(r.Proto)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "h2-ok")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	// 复制测试服务器根证书并挂上我们的 HTTP/2 强制配置
	baseTr := srv.Client().Transport.(*http.Transport).Clone()
	h2Tr := ConfigureHTTP2Transport(baseTr)
	if h2Tr == nil {
		t.Fatal("ConfigureHTTP2Transport 应返回非空 *http2.Transport")
	}
	if h2Tr.ReadIdleTimeout != DefaultH2ReadIdleTimeout {
		t.Fatalf("ReadIdleTimeout = %v, want %v", h2Tr.ReadIdleTimeout, DefaultH2ReadIdleTimeout)
	}
	if !baseTr.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 必须为 true")
	}
	if len(baseTr.TLSClientConfig.NextProtos) == 0 || baseTr.TLSClientConfig.NextProtos[0] != "h2" {
		t.Fatalf("NextProtos 首位必须为 h2，实际 %v", baseTr.TLSClientConfig.NextProtos)
	}

	cli := &http.Client{Transport: baseTr, Timeout: 5 * time.Second}
	resp, err := cli.Get(srv.URL)
	if err != nil {
		t.Fatalf("HTTP/2 GET 失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.ProtoMajor != 2 {
		t.Fatalf("期望协商为 HTTP/2，实际 %s", resp.Proto)
	}
	if got, _ := seenProto.Load().(string); got != "HTTP/2.0" {
		t.Fatalf("服务端观测到的协议应为 HTTP/2.0，实际 %q", got)
	}
}

func TestDoWithEOFRetryRecoversTransientEOF(t *testing.T) {
	var attempts atomic.Int32
	cli := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			n := attempts.Add(1)
			if n < 3 {
				return nil, io.EOF
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Proto:      "HTTP/2.0",
				ProtoMajor: 2,
				Body:       io.NopCloser(strings.NewReader("recovered")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	resp, err := DoWithEOFRetry(context.Background(), cli, func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal", strings.NewReader(`{"ok":true}`))
	}, 2)
	if err != nil {
		t.Fatalf("DoWithEOFRetry 应在第 3 次成功，实际失败: %v", err)
	}
	defer resp.Body.Close()
	if attempts.Load() != 3 {
		t.Fatalf("期望尝试 3 次，实际 %d", attempts.Load())
	}
}

func TestEnsureGODEBUGHTTP2(t *testing.T) {
	env := EnsureGODEBUGHTTP2([]string{"PATH=/usr/bin", "GODEBUG=http2client=0,x509sha1=1"})
	var got string
	for _, kv := range env {
		if strings.HasPrefix(kv, "GODEBUG=") {
			got = kv
		}
	}
	if !strings.Contains(got, "http2client=1") || strings.Contains(got, "http2client=0") || !strings.Contains(got, "x509sha1=1") {
		t.Fatalf("GODEBUG 处理异常: %q", got)
	}
}

func TestIsTransientNetworkError(t *testing.T) {
	if !IsTransientNetworkError(io.EOF) {
		t.Fatal("io.EOF 应判定为瞬时错误")
	}
	if !IsTransientNetworkError(errors.New(`Eligibility check failed: Get "https://www.googleapis.com/oauth2/v2/userinfo": EOF`)) {
		t.Fatal("Google Eligibility EOF 应判定为瞬时错误")
	}
	if IsTransientNetworkError(context.Canceled) {
		t.Fatal("context.Canceled 不应重试")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
