package workbuddy

import (
	"encoding/json"
	"net/http"
	"strings"
)

// DesktopHeaders 构造对齐 WorkBuddy 5.9.3 桌面端的基础请求头。
func DesktopHeaders(creds AccountCreds, accept string, extra http.Header) http.Header {
	if strings.TrimSpace(accept) == "" {
		accept = "application/json"
	}

	h := http.Header{}
	if token := strings.TrimSpace(creds.AccessToken); token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", accept)
	h.Set("User-Agent", UserAgentDesktop)
	h.Set("x-ide-type", IDEType)
	h.Set("x-ide-name", IDEName)
	h.Set("x-ide-version", IDEVersion)
	h.Set("x-product", ProductName)
	if uid := strings.TrimSpace(creds.UID); uid != "" {
		h.Set("x-user-id", uid)
	}
	if q := strings.TrimSpace(creds.Qimei36); q != "" {
		payload, _ := json.Marshal(struct {
			Qimei36 string `json:"qimei36"`
		}{Qimei36: q})
		h.Set("X-Device-Id-List", string(payload))
	}

	for k, vals := range extra {
		for _, v := range vals {
			h.Set(k, v)
		}
	}
	return h
}

// ChatHeaders 构造对话请求头（在桌面端头基础上附加 x-agent-* 与会话追踪头）。
func ChatHeaders(creds AccountCreds) http.Header {
	h := DesktopHeaders(creds, "text/event-stream", nil)
	h.Set("x-agent-intent", "craft")
	h.Set("x-agent-purpose", "conversation")
	h.Set("x-agent-type", "main")
	h.Set("x-agent-runtime", "other")
	h.Set("x-conversation-id", generateUUIDv4())
	h.Set("x-conversation-message-id", generateUUIDv4())
	h.Set("x-conversation-request-id", generateUUIDv4())
	rid := generateUUIDv4()
	h.Set("x-request-id", rid)
	h.Set("x-root-request-id", rid)
	h.Set("x-codebuddy-request", "1")
	return h
}

// AuthHeaders 构造对齐桌面端 buildAuthHeaders(includeAuth, includeDept, includeRefresh) 的鉴权附加头。
func AuthHeaders(creds AccountCreds, withRefresh, withDomain bool) http.Header {
	h := http.Header{}
	if uid := strings.TrimSpace(creds.UID); uid != "" {
		h.Set("X-User-Id", uid)
	} else {
		h.Set("X-User-Id", "")
	}
	if eid := strings.TrimSpace(creds.EnterpriseID); eid != "" {
		h.Set("X-Enterprise-Id", eid)
		h.Set("X-Tenant-Id", eid)
	}
	if withDomain {
		h.Set("X-Domain", DomainHost(creds.Domain))
	}
	if withRefresh {
		if rt := strings.TrimSpace(creds.RefreshToken); rt != "" {
			h.Set("X-Refresh-Token", rt)
		}
	}
	return h
}
