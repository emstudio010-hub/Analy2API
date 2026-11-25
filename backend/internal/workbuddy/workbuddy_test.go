package workbuddy

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startTestHTTPProxy 启动同时支持 HTTP 转发与 HTTPS CONNECT 隧道的测试代理服务器，并统计命中次数与鉴权头。
func startTestHTTPProxy(t *testing.T, expectedBasicAuth string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if expectedBasicAuth != "" {
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte(expectedBasicAuth))
			if r.Header.Get("Proxy-Authorization") != want {
				w.Header().Set("Proxy-Authenticate", `Basic realm="test-proxy"`)
				http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
				return
			}
		}
		hits.Add(1)

		if r.Method == http.MethodConnect {
			targetConn, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				_ = targetConn.Close()
				http.Error(w, "hijack unsupported", http.StatusInternalServerError)
				return
			}
			clientConn, _, err := hj.Hijack()
			if err != nil {
				_ = targetConn.Close()
				return
			}
			_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			go func() {
				defer targetConn.Close()
				defer clientConn.Close()
				_, _ = io.Copy(targetConn, clientConn)
			}()
			go func() {
				defer clientConn.Close()
				defer targetConn.Close()
				_, _ = io.Copy(clientConn, targetConn)
			}()
			return
		}

		outReq := r.Clone(r.Context())
		outReq.RequestURI = ""
		outReq.Header.Del("Proxy-Connection")
		outReq.Header.Del("Proxy-Authorization")
		resp, err := http.DefaultTransport.RoundTrip(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// startTestSOCKS5Proxy 启动符合 RFC 1928 的最小 SOCKS5 测试代理，支持无认证或用户名密码认证，支持域名（socks5h）与 IPv4。
func startTestSOCKS5Proxy(t *testing.T, user, pass string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	var hits atomic.Int32
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSOCKS5Conn(conn, user, pass, &hits)
		}
	}()

	if user != "" {
		return fmt.Sprintf("socks5h://%s:%s@%s", url.QueryEscape(user), url.QueryEscape(pass), ln.Addr().String()), &hits
	}
	return "socks5h://" + ln.Addr().String(), &hits
}

func handleSOCKS5Conn(conn net.Conn, user, pass string, hits *atomic.Int32) {
	defer conn.Close()
	buf := make([]byte, 260)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	}
	nMethods := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:nMethods]); err != nil {
		return
	}

	if user != "" {
		_, _ = conn.Write([]byte{0x05, 0x02})
		if _, err := io.ReadFull(conn, buf[:2]); err != nil || buf[0] != 0x01 {
			return
		}
		uLen := int(buf[1])
		if _, err := io.ReadFull(conn, buf[:uLen]); err != nil {
			return
		}
		gotUser := string(buf[:uLen])
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		pLen := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:pLen]); err != nil {
			return
		}
		gotPass := string(buf[:pLen])
		if gotUser != user || gotPass != pass {
			_, _ = conn.Write([]byte{0x01, 0x01})
			return
		}
		_, _ = conn.Write([]byte{0x01, 0x00})
	} else {
		_, _ = conn.Write([]byte{0x05, 0x00})
	}

	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 {
		return
	}
	var host string
	switch buf[3] {
	case 0x01:
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03:
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		dLen := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:dLen]); err != nil {
			return
		}
		host = string(buf[:dLen])
	case 0x04:
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		return
	}
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	port := int(buf[0])<<8 | int(buf[1])
	hits.Add(1)

	target, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 5*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(target, conn)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, target)
	}()
	wg.Wait()
}

func TestPerAccountProxyIsolationAndDeviceFingerprint(t *testing.T) {
	// 故意污染宿主环境变量 HTTP_PROXY/HTTPS_PROXY，验证 ProxyPool 彻底禁用 ProxyFromEnvironment 不会被串流影响。
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	proxyA, hitsA := startTestHTTPProxy(t, "u1:p1")
	proxyB, hitsB := startTestHTTPProxy(t, "")
	socksProxy, hitsSocks := startTestSOCKS5Proxy(t, "socksuser", "sockspass")

	var mu sync.Mutex
	seenDevices := map[string]string{}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathBillingSummary {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("User-Agent") != UserAgentDesktop {
			t.Errorf("unexpected User-Agent: %q", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("x-ide-version") != IDEVersion || r.Header.Get("x-ide-type") != IDEType {
			t.Errorf("missing WorkBuddy 5.9.3 IDE headers: %v", r.Header)
		}

		uid := r.Header.Get("x-user-id")
		devHdr := r.Header.Get("X-Device-Id-List")
		var devObj struct {
			Qimei36 string `json:"qimei36"`
		}
		if err := json.Unmarshal([]byte(devHdr), &devObj); err != nil {
			t.Errorf("invalid X-Device-Id-List %q: %v", devHdr, err)
		}
		mu.Lock()
		seenDevices[uid] = devObj.Qimei36
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"TotalCredits":"123.45"}}`))
	}))
	defer upstream.Close()

	client := NewClient("", upstream.URL, nil)

	proxyAURL, _ := url.Parse(proxyA.URL)
	proxyAWithAuth := fmt.Sprintf("http://u1:p1@%s", proxyAURL.Host)

	qA, mA := GenerateDevice()
	qB, mB := GenerateDevice()
	qC, mC := GenerateDevice()

	credsA := AccountCreds{
		UID:         "uid-A",
		Domain:      upstream.URL,
		AccessToken: "tok-A",
		ProxyURL:    proxyAWithAuth,
		Qimei36:     qA,
		MachineID:   mA,
	}
	credsB := AccountCreds{
		UID:         "uid-B",
		Domain:      upstream.URL,
		AccessToken: "tok-B",
		ProxyURL:    proxyB.URL,
		Qimei36:     qB,
		MachineID:   mB,
	}
	credsC := AccountCreds{
		UID:         "uid-C",
		Domain:      upstream.URL,
		AccessToken: "tok-C",
		ProxyURL:    socksProxy,
		Qimei36:     qC,
		MachineID:   mC,
	}

	if _, err := client.FetchBillingSummary(context.Background(), credsA); err != nil {
		t.Fatalf("account A billing failed: %v", err)
	}
	if _, err := client.FetchBillingSummary(context.Background(), credsB); err != nil {
		t.Fatalf("account B billing failed: %v", err)
	}
	if _, err := client.FetchBillingSummary(context.Background(), credsC); err != nil {
		t.Fatalf("account C socks5 billing failed: %v", err)
	}

	if hitsA.Load() != 1 {
		t.Fatalf("expected proxyA hits=1, got %d", hitsA.Load())
	}
	if hitsB.Load() != 1 {
		t.Fatalf("expected proxyB hits=1, got %d", hitsB.Load())
	}
	if hitsSocks.Load() != 1 {
		t.Fatalf("expected socks5 proxy hits=1, got %d", hitsSocks.Load())
	}

	if seenDevices["uid-A"] != qA || seenDevices["uid-B"] != qB || seenDevices["uid-C"] != qC {
		t.Fatalf("device fingerprint mismatch: seen=%v want A=%s B=%s C=%s", seenDevices, qA, qB, qC)
	}
}

func TestProxyHelpersAndTestProxy(t *testing.T) {
	if err := ValidateProxyURL("http://user:pass@127.0.0.1:8080"); err != nil {
		t.Fatalf("unexpected validate error: %v", err)
	}
	if err := ValidateProxyURL("socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("unexpected socks5 validate error: %v", err)
	}
	if err := ValidateProxyURL("direct"); err != nil {
		t.Fatalf("unexpected direct validate error: %v", err)
	}
	if err := ValidateProxyURL("ftp://127.0.0.1:21"); err == nil {
		t.Fatalf("expected error for unsupported proxy scheme ftp")
	}

	if got := RedactProxyURL("http://alice:s3cr3t@proxy.example.com:8080"); !strings.Contains(got, "alice:***@") || strings.Contains(got, "s3cr3t") {
		t.Fatalf("password not redacted: %q", got)
	}
	if got := ResolveProxy("direct", "http://127.0.0.1:9999"); got != "" {
		t.Fatalf("expected direct to override default proxy, got %q", got)
	}
	if got := ResolveProxy("", "http://127.0.0.1:9999/"); got != "http://127.0.0.1:9999" {
		t.Fatalf("expected default fallback normalized, got %q", got)
	}
	if got := ResolveProxy("http://127.0.0.1:7777", "http://127.0.0.1:9999"); got != "http://127.0.0.1:7777" {
		t.Fatalf("expected account proxy to override default, got %q", got)
	}

	// TestProxy 通过受控本地 HTTPS TLS 目标与出口 IP 服务验证
	proxySrv, hits := startTestHTTPProxy(t, "")
	targetTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetTLS.Close()

	ipEcho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.42\n"))
	}))
	defer ipEcho.Close()

	pool := NewProxyPool("")
	testTLSCfg := targetTLS.Client().Transport.(*http.Transport).TLSClientConfig
	res := pool.testProxyWithTargets(context.Background(), proxySrv.URL, targetTLS.URL, []string{ipEcho.URL}, testTLSCfg)
	if !res.OK || res.ExitIP != "203.0.113.42" || res.Status != http.StatusOK {
		t.Fatalf("unexpected TestProxy result: %+v", res)
	}
	if hits.Load() < 1 {
		t.Fatalf("expected proxy hits >= 1, got %d", hits.Load())
	}
}

func TestBillingSummaryPascalCasePrecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code": 0,
			"data": {
				"TotalCredits": "2799.12345678",
				"SubscriptionPackageName": "Pro Monthly",
				"IsPaidUser": true,
				"CreditGroups": [
					{
						"Kind": "plan_base",
						"Name": "基础额度",
						"CycleTotalCapacity": "3000.00",
						"CycleRemainCapacity": "2000.50",
						"CycleRemainCapacityPrecise": "2000.50000123",
						"CycleUsedCapacity": "999.50"
					},
					{
						"Kind": "addon",
						"Name": "任务加赠",
						"CycleTotalCapacity": 800,
						"CycleRemainCapacity": 798.62345555
					}
				],
				"Packages": [
					{
						"PackageCode": "TCACA_code_008_checkin",
						"PackageName": "签到激励",
						"TotalCapacity": "100.5",
						"RemainCapacityPrecise": "88.7654321",
						"RemainCapacity": "88.77",
						"TotalCount": 1,
						"ExpiredAt": "2099-12-31T23:59:59Z"
					}
				]
			}
		}`))
	}))
	defer srv.Close()

	c := NewClient("", srv.URL, nil)
	sum, err := c.FetchBillingSummary(context.Background(), AccountCreds{
		UID:         "u-bill",
		Domain:      srv.URL,
		AccessToken: "tok",
	})
	if err != nil {
		t.Fatalf("FetchBillingSummary error: %v", err)
	}

	if math.Abs(sum.TotalCredits-2799.12345678) > 1e-8 {
		t.Fatalf("expected high-precision TotalCredits 2799.12345678, got %.10f", sum.TotalCredits)
	}
	if !sum.IsPaidUser || sum.SubscriptionPackageName != "Pro Monthly" {
		t.Fatalf("unexpected paid/package metadata: %+v", sum)
	}
	if len(sum.CreditGroups) != 2 {
		t.Fatalf("expected 2 CreditGroups, got %d", len(sum.CreditGroups))
	}
	if math.Abs(sum.CreditGroups[0].CycleRemainCapacity-2000.50000123) > 1e-8 {
		t.Fatalf("expected Precise group capacity 2000.50000123, got %.10f", sum.CreditGroups[0].CycleRemainCapacity)
	}
	if len(sum.Packages) != 1 || math.Abs(sum.Packages[0].Remain-88.7654321) > 1e-7 {
		t.Fatalf("unexpected Packages precision: %+v", sum.Packages)
	}
	if sum.Packages[0].ExpiredAt != nil {
		t.Fatalf("expected far-future sentinel expiry to normalize to nil, got %v", *sum.Packages[0].ExpiredAt)
	}
}

func TestDeviceAuthEmptyBodyAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathDeviceAuthCode || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Fatalf("expected empty body for device auth, got %d bytes: %q", len(body), string(body))
		}
		if r.Header.Get("X-Refresh-Token") != "rt-secret" {
			t.Fatalf("missing or wrong X-Refresh-Token: %q", r.Header.Get("X-Refresh-Token"))
		}
		if r.Header.Get("X-User-Id") != "uid-999" {
			t.Fatalf("missing or wrong X-User-Id: %q", r.Header.Get("X-User-Id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"deviceCode":"DEV-XYZ-300","expiresIn":300}}`))
	}))
	defer srv.Close()

	c := NewClient("", "www.codebuddy.cn", nil)
	creds := AccountCreds{
		UID:          "uid-999",
		Domain:       srv.URL,
		AccessToken:  "at-123",
		RefreshToken: "rt-secret",
	}

	res, err := c.IssueLoginLink(context.Background(), creds, "/usage")
	if err != nil {
		t.Fatalf("IssueLoginLink failed: %v", err)
	}
	if res.DeviceCode != "DEV-XYZ-300" || res.ExpiresIn != 300 {
		t.Fatalf("unexpected DeviceAuthResult: %+v", res)
	}
	wantPrefix := srv.URL + "/console/client-login?"
	if !strings.HasPrefix(res.LoginURL, wantPrefix) || !strings.Contains(res.LoginURL, "code=DEV-XYZ-300") {
		t.Fatalf("unexpected LoginURL: %q", res.LoginURL)
	}

	// 验证缺失 RefreshToken 时提前拦报错
	if _, err := c.IssueLoginLink(context.Background(), AccountCreds{UID: "u"}, "/usage"); err == nil {
		t.Fatalf("expected error when refresh_token is empty")
	}
}

func TestHarvestPipelineAndSkillShareFourEvents(t *testing.T) {
	proxySrv, proxyHits := startTestHTTPProxy(t, "")
	qimei, machine := GenerateDevice()
	const uid = "uid-harvest-1"

	var mu sync.Mutex
	var skillShareDone bool
	var capturedEvents []map[string]any
	var visitedPaths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		visitedPaths = append(visitedPaths, r.Method+" "+r.URL.Path)
		done := skillShareDone
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case PathGrowthTasks:
			if done {
				_, _ = w.Write([]byte(`{"code":0,"data":{"tasks":[{"code":"skill_share","status":"wait_claim","credit":150,"accepted":true,"completed":true},{"code":"RichMeow_Chat","status":"completed","credit":50,"accepted":true,"completed":true}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"tasks":[{"code":"skill_share","status":"unaccepted","credit":150},{"code":"RichMeow_Chat","status":"in_progress","credit":50,"accepted":true}]}}`))

		case PathGrowthTasksAccept:
			_, _ = w.Write([]byte(`{"code":0,"data":{"accepted":["skill_share"]}}`))

		case PathDailyCheckin:
			_, _ = w.Write([]byte(`{"code":0,"data":{"credit":30,"streak":3,"checkedInDate":"2026-11-15"}}`))

		case PathGiftStatus:
			_, _ = w.Write([]byte(`{"code":0,"data":{"canClaim":true,"claimed":false}}`))

		case PathClaimGift:
			_, _ = w.Write([]byte(`{"code":0,"data":{"credit":100}}`))

		case PathCompStatus:
			_, _ = w.Write([]byte(`{"code":0,"data":{"hasCompensation":false,"claimed":true}}`))

		case PathReport:
			raw, _ := io.ReadAll(r.Body)
			var events []map[string]any
			if err := json.Unmarshal(raw, &events); err != nil {
				t.Fatalf("failed to unmarshal /v2/report body: %v", err)
			}
			mu.Lock()
			capturedEvents = events
			skillShareDone = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))

		case PathGrowthBuddyQuota:
			_, _ = w.Write([]byte(`{"code":0,"data":{"remainQuota":1}}`))

		case PathGrowthBuddyOpen:
			_, _ = w.Write([]byte(`{"code":0,"data":{"credit":80}}`))

		case PathGrowthStreak:
			_, _ = w.Write([]byte(`{"code":0,"data":{"streak":3,"milestones":[{"id":"m3","days":3,"reached":true,"claimed":false}]}}`))

		case PathGrowthRedeem:
			_, _ = w.Write([]byte(`{"code":0,"data":{"credit":200}}`))

		case PathGrowthLottery:
			_, _ = w.Write([]byte(`{"code":0,"data":{"remainChances":1}}`))

		case PathGrowthLotteryDraw:
			_, _ = w.Write([]byte(`{"code":0,"data":{"credit":60}}`))

		case PathChatCompletions:
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))

		case "/activity/growth/tasks/skill_share/claim":
			_, _ = w.Write([]byte(`{"code":0,"data":{"credit":150}}`))

		case "/activity/growth/tasks/RichMeow_Chat/claim":
			_, _ = w.Write([]byte(`{"code":0,"data":{"credit":50}}`))

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient("", srv.URL, nil)
	c.setHarvestPollDelay(0)

	creds := AccountCreds{
		UID:         uid,
		Domain:      srv.URL,
		AccessToken: "tok-harvest",
		ProxyURL:    proxySrv.URL,
		Qimei36:     qimei,
		MachineID:   machine,
	}

	res, err := c.Harvest(context.Background(), creds, true)
	if err != nil {
		t.Fatalf("Harvest failed: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected Harvest OK=true, got %+v", res)
	}
	// 30 (checkin) + 100 (gift) + 80 (blindbox) + 200 (streak) + 60 (lottery) + 150 (skill_share) + 50 (RichMeow_Chat) = 670
	if res.CreditGained != 670 {
		t.Fatalf("expected total CreditGained=670, got %d; steps=%+v", res.CreditGained, res.Steps)
	}
	if res.Streak != 3 || res.CheckedInDate != "2026-11-15" {
		t.Fatalf("unexpected streak/date: %+v", res)
	}
	if proxyHits.Load() == 0 {
		t.Fatalf("expected all harvest traffic to route through account proxy")
	}

	wantEvents := []string{
		"skill_share_entry_click",
		"skill_share_channel_action",
		"skill_share_deeplink_result",
		"skill_share_conversion",
	}
	if len(capturedEvents) != len(wantEvents) {
		t.Fatalf("expected 4 skill_share events, got %d", len(capturedEvents))
	}
	for i, wantName := range wantEvents {
		ev := capturedEvents[i]
		if ev["event"] != wantName {
			t.Errorf("event[%d].event = %v, want %s", i, ev["event"], wantName)
		}
		if ev["qimei36"] != qimei {
			t.Errorf("event[%d].qimei36 = %v, want %s", i, ev["qimei36"], qimei)
		}
		if ev["machineId"] != machine {
			t.Errorf("event[%d].machineId = %v, want %s", i, ev["machineId"], machine)
		}
		if ev["userId"] != uid {
			t.Errorf("event[%d].userId = %v, want %s", i, ev["userId"], uid)
		}
	}
}

func TestHarvestDailyCheckin10001Skipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case PathDailyCheckin:
			_, _ = w.Write([]byte(`{"code":10001,"message":"already checked in today"}`))
		case PathGrowthTasks:
			_, _ = w.Write([]byte(`{"code":0,"data":{"tasks":[{"code":"skill_share","status":"claimed","claimed":true}]}}`))
		case PathGiftStatus:
			_, _ = w.Write([]byte(`{"code":0,"data":{"claimed":true}}`))
		case PathCompStatus:
			_, _ = w.Write([]byte(`{"code":0,"data":{"hasCompensation":false}}`))
		case PathGrowthBuddyQuota:
			_, _ = w.Write([]byte(`{"code":0,"data":{"remainQuota":0}}`))
		case PathGrowthStreak:
			_, _ = w.Write([]byte(`{"code":0,"data":{"streak":5,"milestones":[]}}`))
		case PathGrowthLottery:
			_, _ = w.Write([]byte(`{"code":0,"data":{"remainChances":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient("", srv.URL, nil)
	res, err := c.Harvest(context.Background(), AccountCreds{UID: "u-skip", AccessToken: "tok"}, false)
	if err != nil {
		t.Fatalf("Harvest failed: %v", err)
	}
	var foundCheckin bool
	for _, s := range res.Steps {
		if s.Name == "daily_checkin" {
			foundCheckin = true
			if !s.Skipped || s.Kind != "skip" || !s.OK {
				t.Fatalf("expected daily_checkin skipped on 10001, got %+v", s)
			}
		}
	}
	if !foundCheckin {
		t.Fatalf("daily_checkin step missing")
	}
}

func TestModelsFilteringAndFallbackAndChatPing(t *testing.T) {
	var failModels atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/enterprises/personal/models" {
			if failModels.Load() {
				http.Error(w, "upstream down", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"code": 0,
				"data": {
					"agents": [{"name": "cli", "models": ["glm-4.6", "deepseek-v4.1-flash"]}],
					"models": [
						{"id": "deepseek-v4.1-flash", "name": "DeepSeek V4.1 Flash", "maxOutputTokens": 16384},
						{"id": "glm-4.6", "name": "GLM 4.6", "maxOutputTokens": 16384},
						{"id": "nes-next-edit", "name": "NES", "maxOutputTokens": 4096},
						{"id": "completion-fast", "name": "Comp", "maxOutputTokens": 4096},
						{"id": "tiny-model", "name": "Tiny", "maxOutputTokens": 128},
						{"id": "hunyuan-image-v2", "name": "Img", "maxOutputTokens": 4096}
					]
				}
			}`))
			return
		}
		if r.URL.Path == PathChatCompletions {
			raw, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			if payload["stream"] != true {
				t.Fatalf("expected stream=true forced on ChatStream payload")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(": heartbeat\n: heartbeat\n\ndata: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n\n"))
			return
		}
		if r.URL.Path == PathTokenRefresh {
			if r.Header.Get("X-Refresh-Token") != "rt-old" || r.Header.Get("X-Auth-Refresh-Source") != "plugin" {
				t.Fatalf("missing refresh headers: %v", r.Header)
			}
			if r.Header.Get("X-Domain") == "" {
				t.Fatalf("missing X-Domain header")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"at-new","expiresIn":7200}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient("", srv.URL, nil)
	creds := AccountCreds{UID: "u1", AccessToken: "at-old", RefreshToken: "rt-old"}

	models, err := c.Models(context.Background(), creds)
	if err != nil {
		t.Fatalf("Models failed: %v", err)
	}
	if len(models) != 2 || models[0].ID != "glm-4.6" || models[1].ID != "deepseek-v4.1-flash" {
		t.Fatalf("unexpected filtered/ordered models: %+v", models)
	}

	failModels.Store(true)
	fallback, err := c.Models(context.Background(), creds)
	if err != nil || len(fallback) < 5 {
		t.Fatalf("expected DefaultModels fallback, got len=%d err=%v", len(fallback), err)
	}

	// Ping skipping ": heartbeat"
	lat, err := c.Ping(context.Background(), creds, "")
	if err != nil || lat < 0 {
		t.Fatalf("Ping failed: err=%v lat=%v", err, lat)
	}

	// ChatStream forcing stream:true even if caller omitted stream
	resp, err := c.ChatStream(context.Background(), creds, []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var lines int
	for sc.Scan() {
		lines++
	}
	if lines == 0 {
		t.Fatalf("expected non-empty stream lines")
	}

	// RefreshToken keeping old RefreshToken when upstream omits refreshToken
	tr, err := c.RefreshToken(context.Background(), creds)
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if tr.AccessToken != "at-new" || tr.RefreshToken != "rt-old" {
		t.Fatalf("unexpected RefreshToken result: %+v", tr)
	}
}

func TestParseAccountsImportAndLocalScanAndDomainNorm(t *testing.T) {
	if got := NormalizeDomain("www.codebuddy.cn/"); got != "https://www.codebuddy.cn" {
		t.Fatalf("bare domain norm failed: got %q", got)
	}
	if got := NormalizeDomain(""); got != DefaultDomain {
		t.Fatalf("empty domain norm failed: got %q", got)
	}
	if got := DomainHost("https://ent.codebuddy.cn/"); got != "ent.codebuddy.cn" {
		t.Fatalf("DomainHost failed: got %q", got)
	}

	// 1. agent-switch/accounts.json 格式（含 profile_raw / auth_raw 嵌套、裸域名）
	agentSwitchJSON := `[
		{
			"uid": "uid-as-1",
			"nickname": "alice",
			"access_token": "tok-as-1",
			"refresh_token": "rt-as-1",
			"domain": "www.codebuddy.cn",
			"proxy_url": "socks5://127.0.0.1:1080",
			"profile_raw": {
				"enterpriseId": "ent-100",
				"enterpriseName": "Acme Corp",
				"sso": {"domain": "www.codebuddy.cn"}
			},
			"auth_raw": {
				"accessToken": "tok-as-1",
				"refreshToken": "rt-as-1"
			}
		}
	]`
	parsed, err := ParseAccountsImport(agentSwitchJSON)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("agent-switch parse failed: len=%d err=%v", len(parsed), err)
	}
	if parsed[0].Domain != "https://www.codebuddy.cn" || parsed[0].EnterpriseID != "ent-100" || parsed[0].ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("unexpected agent-switch result: %+v", parsed[0])
	}

	// 2. workbuddy-desktop.info 明文单对象
	desktopPlain := `{"auth":{"accessToken":"tok-dt","refreshToken":"rt-dt","domain":"workbuddy.codebuddy.cn/"},"account":{"uid":"uid-dt-1","nickname":"bob","enterpriseId":"ent-2"}}`
	parsed, err = ParseAccountsImport(desktopPlain)
	if err != nil || len(parsed) != 1 || parsed[0].UID != "uid-dt-1" || parsed[0].Domain != "https://workbuddy.codebuddy.cn" {
		t.Fatalf("desktop plain parse failed: %+v err=%v", parsed, err)
	}

	// 3. workbuddy-desktop.info $wbEncrypted 密文报错检测
	desktopEnc := `{"auth":{"accessToken":{"$wbEncrypted":1,"envelope":"abc"}},"account":{"uid":"uid-enc"}}`
	if _, err := ParseAccountsImport(desktopEnc); err == nil || !strings.Contains(err.Error(), "$wbEncrypted") {
		t.Fatalf("expected $wbEncrypted clear error, got %v", err)
	}

	// 4. workbuddy-sdk {"accounts":[...]} 含设备指纹
	sdkJSON := `{"accounts":[{"name":"sdk-user","uid":"uid-sdk-1","access_token":"tok-sdk","qimei36":"0123456789abcdef0123456789abcdef0123","machine_id":"11111111-2222-4333-8444-555555555555","proxy_url":"direct"}]}`
	parsed, err = ParseAccountsImport(sdkJSON)
	if err != nil || len(parsed) != 1 || parsed[0].ProxyURL != "direct" || parsed[0].Qimei36 == "" || parsed[0].MachineID == "" {
		t.Fatalf("sdk json parse failed: %+v err=%v", parsed, err)
	}

	// 5. 多行文本格式（token / name,token / uid|access_token|refresh_token|proxy_url）
	multiText := `
# comment line
tok-only-1
nick2,tok-comma-2
uid-pipe-3|tok-pipe-3|rt-pipe-3|http://127.0.0.1:8888
`
	parsed, err = ParseAccountsImport(multiText)
	if err != nil || len(parsed) != 3 {
		t.Fatalf("multi-line text parse failed: len=%d err=%v", len(parsed), err)
	}
	if parsed[0].AccessToken != "tok-only-1" || parsed[0].Domain != DefaultDomain {
		t.Fatalf("line 1 mismatch: %+v", parsed[0])
	}
	if parsed[1].Name != "nick2" || parsed[1].AccessToken != "tok-comma-2" {
		t.Fatalf("line 2 mismatch: %+v", parsed[1])
	}
	if parsed[2].UID != "uid-pipe-3" || parsed[2].RefreshToken != "rt-pipe-3" || parsed[2].ProxyURL != "http://127.0.0.1:8888" {
		t.Fatalf("line 3 mismatch: %+v", parsed[2])
	}

	// 6. scanLocalAccountsFromPaths 保留 devices.json 指纹并去重、容错加密桌面文件
	tmpDir := t.TempDir()
	desktopPath := filepath.Join(tmpDir, "workbuddy-desktop.info")
	agentSwitchPath := filepath.Join(tmpDir, "agent-switch.json")
	sdkPath := filepath.Join(tmpDir, "sdk-accounts.json")
	devicesPath := filepath.Join(tmpDir, "devices.json")

	_ = os.WriteFile(desktopPath, []byte(desktopEnc), 0o600)
	_ = os.WriteFile(agentSwitchPath, []byte(agentSwitchJSON), 0o600)
	_ = os.WriteFile(sdkPath, []byte(`[{"uid":"uid-as-1","access_token":"tok-as-1","email":"alice@example.com"},{"uid":"uid-2","access_token":"tok-2"}]`), 0o600)
	_ = os.WriteFile(devicesPath, []byte(`{"acct-uid-as-1":{"qimei36":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","machineId":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}}`), 0o600)

	scanRes, err := scanLocalAccountsFromPaths(desktopPath, agentSwitchPath, sdkPath, devicesPath)
	if err != nil {
		t.Fatalf("scanLocalAccountsFromPaths failed: %v", err)
	}
	if len(scanRes.Errors) != 1 || !strings.Contains(scanRes.Errors[0], "$wbEncrypted") {
		t.Fatalf("expected 1 $wbEncrypted error recorded, got %v", scanRes.Errors)
	}
	if len(scanRes.Accounts) != 2 {
		t.Fatalf("expected 2 deduplicated accounts, got %d: %+v", len(scanRes.Accounts), scanRes.Accounts)
	}
	if scanRes.Accounts[0].Qimei36 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || scanRes.Accounts[0].Email != "alice@example.com" {
		t.Fatalf("expected merged email and preserved devices.json qimei36, got %+v", scanRes.Accounts[0])
	}
}
