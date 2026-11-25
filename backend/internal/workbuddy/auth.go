package workbuddy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NormalizeDomain 规范化 WorkBuddy 域名，自动为裸域名补全 https:// 并去除末尾斜杠。
func NormalizeDomain(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultDomain
	}

	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		s = "https://" + strings.TrimLeft(s, "/")
	}

	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		cleaned := strings.TrimRight(s, "/")
		if cleaned == "" {
			return DefaultDomain
		}
		return cleaned
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	host := strings.ToLower(u.Host)
	path := strings.TrimRight(u.Path, "/")
	return scheme + "://" + host + path
}

// DomainHost 从域名或 URL 中提取 host[:port]，用于 X-Domain 请求头。
func DomainHost(raw string) string {
	norm := NormalizeDomain(raw)
	u, err := url.Parse(norm)
	if err != nil || u.Host == "" {
		return DefaultDomainHost
	}
	return u.Host
}

// RefreshToken 通过账号专属代理与设备指纹刷新 WorkBuddy AccessToken。
func RefreshToken(ctx context.Context, creds AccountCreds) (*TokenRefreshResult, error) {
	return DefaultClient().RefreshToken(ctx, creds)
}

// RefreshToken 调用 POST {Domain}/v2/plugin/auth/token/refresh 刷新账号访问凭据。
func (c *Client) RefreshToken(ctx context.Context, creds AccountCreds) (*TokenRefreshResult, error) {
	rt := strings.TrimSpace(creds.RefreshToken)
	if rt == "" {
		return nil, fmt.Errorf("workbuddy refresh token is empty")
	}

	extra := AuthHeaders(creds, true, true)
	extra.Set("X-Auth-Refresh-Source", "plugin")

	rawBytes, root, status, reqID, err := c.doAPI(ctx, creds, http.MethodPost, PathTokenRefresh, map[string]any{}, "application/json", extra)
	if err != nil {
		return nil, err
	}

	data := unwrapDataMap(root)
	accessToken := firstNonEmptyString(data, "accessToken", "access_token", "token")
	if accessToken == "" {
		accessToken = firstNonEmptyString(root, "accessToken", "access_token", "token")
	}
	if accessToken == "" {
		return nil, &APIError{
			Status:    status,
			Message:   "token refresh response missing accessToken",
			RequestID: reqID,
			Raw:       truncateRaw(rawBytes),
		}
	}

	newRefresh := firstNonEmptyString(data, "refreshToken", "refresh_token")
	if newRefresh == "" {
		newRefresh = firstNonEmptyString(root, "refreshToken", "refresh_token")
	}
	if newRefresh == "" {
		newRefresh = rt
	}

	domainRaw := firstNonEmptyString(data, "domain")
	if domainRaw == "" {
		domainRaw = firstNonEmptyString(root, "domain")
	}
	resolvedDomain := NormalizeDomain(fallbackString(domainRaw, creds.Domain, c.DefaultDomain()))

	nowUnix := time.Now().Unix()
	expiresIn := firstInt64(data, "expiresIn", "expires_in")
	refreshExpiresIn := firstInt64(data, "refreshExpiresIn", "refresh_expires_in")
	expiresAt := normalizeUnixSeconds(firstInt64(data, "expiresAt", "expires_at"))
	refreshExpiresAt := normalizeUnixSeconds(firstInt64(data, "refreshExpiresAt", "refresh_expires_at"))

	if expiresAt == 0 && expiresIn > 0 {
		expiresAt = nowUnix + expiresIn
	}
	if refreshExpiresAt == 0 && refreshExpiresIn > 0 {
		refreshExpiresAt = nowUnix + refreshExpiresIn
	}

	tokenType := firstNonEmptyString(data, "tokenType", "token_type")
	if tokenType == "" {
		tokenType = "Bearer"
	}

	return &TokenRefreshResult{
		AccessToken:      accessToken,
		RefreshToken:     newRefresh,
		Domain:           resolvedDomain,
		ExpiresIn:        expiresIn,
		RefreshExpiresIn: refreshExpiresIn,
		ExpiresAt:        expiresAt,
		RefreshExpiresAt: refreshExpiresAt,
		TokenType:        tokenType,
	}, nil
}

func normalizeUnixSeconds(v int64) int64 {
	if v > 1_000_000_000_000 {
		return v / 1000
	}
	return v
}

func firstInt64(m map[string]any, keys ...string) int64 {
	if m == nil {
		return 0
	}
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		switch t := val.(type) {
		case float64:
			if t != 0 {
				return int64(t)
			}
		case int64:
			if t != 0 {
				return t
			}
		case int:
			if t != 0 {
				return int64(t)
			}
		case string:
			s := strings.TrimSpace(t)
			if s == "" {
				continue
			}
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n
			}
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return int64(f)
			}
			if parsed, err := time.Parse(time.RFC3339, s); err == nil {
				return parsed.Unix()
			}
		}
	}
	return 0
}
