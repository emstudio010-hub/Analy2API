package service

import (
	"testing"
)

func TestNormalizeAndMaskProxyAddress(t *testing.T) {
	cases := []struct {
		raw       string
		scheme    string
		user      string
		pass      string
		want      string
		wantMask  string
		wantError bool
	}{
		{
			raw:      "1.2.3.4:8080",
			scheme:   "http",
			want:     "http://1.2.3.4:8080",
			wantMask: "http://1.2.3.4:8080",
		},
		{
			raw:      "1.2.3.4:1080",
			scheme:   "socks5",
			user:     "alice",
			pass:     "s3cr3t",
			want:     "socks5://alice:s3cr3t@1.2.3.4:1080",
			wantMask: "socks5://alice:***@1.2.3.4:1080",
		},
		{
			raw:      "5.6.7.8:9000:bob:pass99",
			scheme:   "http",
			want:     "http://bob:pass99@5.6.7.8:9000",
			wantMask: "http://bob:***@5.6.7.8:9000",
		},
		{
			raw:      "socks5h://u1:p1@proxy.example.com:1080",
			scheme:   "http",
			want:     "socks5h://u1:p1@proxy.example.com:1080",
			wantMask: "socks5h://u1:***@proxy.example.com:1080",
		},
		{
			raw:       "ftp://1.2.3.4:21",
			scheme:    "http",
			wantError: true,
		},
		{
			raw:       "not-a-proxy",
			scheme:    "http",
			wantError: true,
		},
	}

	for _, tc := range cases {
		got, err := NormalizeProxyAddress(tc.raw, tc.scheme, tc.user, tc.pass)
		if tc.wantError {
			if err == nil {
				t.Errorf("NormalizeProxyAddress(%q) 期望报错，但返回 %q", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeProxyAddress(%q) 意外失败: %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeProxyAddress(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if masked := MaskProxyURL(got); masked != tc.wantMask {
			t.Errorf("MaskProxyURL(%q) = %q, want %q", got, masked, tc.wantMask)
		}
	}
}

func TestParseFetchedProxies_PlainAndJSON(t *testing.T) {
	// 1. 纯文本（支持 ip:port、ip:port:user:pass 与 #tag）
	txt := "# comment\n10.0.0.1:8080 #us\n10.0.0.2:8081:u2:p2\nsocks5://10.0.0.3:1080,jp\n"
	gotTxt := ParseFetchedProxies([]byte(txt), ProxyFetchConfig{DefaultScheme: "http", DefaultTag: "default"})
	if len(gotTxt) != 3 {
		t.Fatalf("纯文本期望解析 3 条，实际 %d: %+v", len(gotTxt), gotTxt)
	}
	if gotTxt[0].URL != "http://10.0.0.1:8080" || gotTxt[0].Tag != "us" {
		t.Errorf("第 1 条不对: %+v", gotTxt[0])
	}
	if gotTxt[1].URL != "http://u2:p2@10.0.0.2:8081" || gotTxt[1].Tag != "default" {
		t.Errorf("第 2 条不对: %+v", gotTxt[1])
	}
	if gotTxt[2].URL != "socks5://10.0.0.3:1080" || gotTxt[2].Tag != "jp" {
		t.Errorf("第 3 条不对: %+v", gotTxt[2])
	}

	// 2. 代理商常见嵌套 JSON 响应
	jsonBody := `{
		"code": 0,
		"data": {
			"list": [
				{"ip": "20.0.0.1", "port": 8888, "user": "a", "pass": "b", "country": "sg"},
				{"proxy": "socks5://20.0.0.2:1080", "region": "us"}
			]
		}
	}`
	gotJSON := ParseFetchedProxies([]byte(jsonBody), ProxyFetchConfig{
		DefaultScheme: "http",
		JSONPath:      "data.list",
		DefaultTag:    "remote",
	})
	if len(gotJSON) != 2 {
		t.Fatalf("JSON 期望解析 2 条，实际 %d: %+v", len(gotJSON), gotJSON)
	}
	if gotJSON[0].URL != "http://a:b@20.0.0.1:8888" || gotJSON[0].Tag != "sg" {
		t.Errorf("JSON 第 1 条不对: %+v", gotJSON[0])
	}
	if gotJSON[1].URL != "socks5://20.0.0.2:1080" || gotJSON[1].Tag != "us" {
		t.Errorf("JSON 第 2 条不对: %+v", gotJSON[1])
	}
}
