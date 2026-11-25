package workbuddy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	defaultDeviceAuthTarget  = "/usage"
	defaultDeviceAuthExpires = 300
)

// BuildClientLoginURL 构造一次性 WorkBuddy 官网免密登录落地页 URL。
func BuildClientLoginURL(domain, deviceCode, targetPath string) string {
	base := NormalizeDomain(domain)
	target := strings.TrimSpace(targetPath)
	if target == "" {
		target = defaultDeviceAuthTarget
	}

	q := url.Values{}
	q.Set("code", strings.TrimSpace(deviceCode))
	q.Set("target", target)
	return base + PathClientLogin + "?" + q.Encode()
}

// IssueLoginLink 通过账号专属代理向 WorkBuddy 申请 300s 一次性免密登录链接。
func IssueLoginLink(ctx context.Context, creds AccountCreds, targetPath string) (*DeviceAuthResult, error) {
	return DefaultClient().IssueLoginLink(ctx, creds, targetPath)
}

// IssueLoginLink 调用 POST {Domain}/v2/plugin/device/auth/code（无请求体，必带 X-Refresh-Token 与 X-User-Id）。
func (c *Client) IssueLoginLink(ctx context.Context, creds AccountCreds, targetPath string) (*DeviceAuthResult, error) {
	if strings.TrimSpace(creds.UID) == "" {
		return nil, fmt.Errorf("workbuddy device auth requires non-empty uid")
	}
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return nil, fmt.Errorf("workbuddy device auth requires non-empty refresh_token")
	}

	target := strings.TrimSpace(targetPath)
	if target == "" {
		target = defaultDeviceAuthTarget
	}

	// 严禁传任何请求体（nil body），否则服务端部分网关会拒绝或解析失败；必带 X-Refresh-Token 和 X-User-Id。
	extra := AuthHeaders(creds, true, true)
	rawBytes, root, status, reqID, err := c.doAPI(ctx, creds, http.MethodPost, PathDeviceAuthCode, nil, "application/json", extra)
	if err != nil {
		return nil, err
	}

	data := unwrapDataMap(root)
	code := firstNonEmptyString(data, "deviceCode", "device_code", "code")
	if code == "" {
		code = firstNonEmptyString(root, "deviceCode", "device_code", "code")
	}
	if code == "" {
		return nil, &APIError{
			Status:    status,
			Message:   "device auth response missing deviceCode",
			RequestID: reqID,
			Raw:       truncateRaw(rawBytes),
		}
	}

	expiresIn := firstInt(data, "expiresIn", "expires_in")
	if expiresIn <= 0 {
		expiresIn = firstInt(root, "expiresIn", "expires_in")
	}
	if expiresIn <= 0 {
		expiresIn = defaultDeviceAuthExpires
	}

	resolvedDomain := NormalizeDomain(fallbackString(creds.Domain, c.DefaultDomain()))
	return &DeviceAuthResult{
		DeviceCode: code,
		ExpiresIn:  expiresIn,
		LoginURL:   BuildClientLoginURL(resolvedDomain, code, target),
		Target:     target,
	}, nil
}
