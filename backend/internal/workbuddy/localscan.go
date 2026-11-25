package workbuddy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type localDeviceRecord struct {
	Qimei36   string `json:"qimei36"`
	MachineID string `json:"machineId"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// ParseAccountsImport 兼容解析多种历史及现代 WorkBuddy 账号导出格式与文本批量格式。
//
// 支持格式：
//   - agent-switch/accounts.json（含 profile_raw / auth_raw 嵌套、sso.domain 等）
//   - workbuddy-sdk/accounts.json（支持 proxy_url / qimei36 / machine_id / devices 映射）
//   - workbuddy-desktop.info（含 auth / account 对象及 $wbEncrypted 密文包检测）
//   - JSON 单对象、JSON 数组、{"accounts":[...]} / {"data":[...]} / {"items":[...]} 包装结构
//   - 多行文本：token | name,token | uid|access_token|refresh_token|proxy_url
func ParseAccountsImport(raw string) ([]ImportedAccount, error) {
	cleaned := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if cleaned == "" {
		return nil, errors.New("empty accounts import input")
	}

	if strings.HasPrefix(cleaned, "{") || strings.HasPrefix(cleaned, "[") {
		accounts, err := parseAccountsJSON([]byte(cleaned), "import")
		if err != nil {
			return nil, err
		}
		if len(accounts) == 0 {
			return nil, errors.New("no valid accounts found in JSON import payload")
		}
		return accounts, nil
	}

	accounts, err := parseAccountsText(cleaned, "text")
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, errors.New("no valid accounts found in text import payload")
	}
	return accounts, nil
}

// ScanLocalAccounts 扫描本机 WorkBuddy 桌面端与 SDK 账号及设备指纹文件。
func ScanLocalAccounts() (*LocalScanResult, error) {
	homeDir, _ := os.UserHomeDir()
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if localAppData == "" && homeDir != "" {
		localAppData = filepath.Join(homeDir, "AppData", "Local")
	}

	var desktopInfoPath string
	if localAppData != "" {
		desktopInfoPath = filepath.Join(localAppData, "CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info")
	} else if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		desktopInfoPath = filepath.Join(cacheDir, "CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info")
	}

	var agentSwitchPath, sdkAccountsPath, devicesPath string
	if homeDir != "" {
		agentSwitchPath = filepath.Join(homeDir, ".agent-switch", "accounts.json")
		sdkAccountsPath = filepath.Join(homeDir, ".workbuddy-sdk", "accounts.json")
		devicesPath = filepath.Join(homeDir, ".workbuddy-sdk", "devices.json")
	}

	return scanLocalAccountsFromPaths(desktopInfoPath, agentSwitchPath, sdkAccountsPath, devicesPath)
}

func scanLocalAccountsFromPaths(desktopInfoPath, agentSwitchPath, sdkAccountsPath, devicesPath string) (*LocalScanResult, error) {
	result := &LocalScanResult{
		Sources:  []string{},
		Accounts: []ImportedAccount{},
	}

	devices, devErr := loadDevicesMap(devicesPath)
	if devErr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", devicesPath, devErr))
	} else if len(devices) > 0 && devicesPath != "" {
		result.Sources = append(result.Sources, devicesPath)
	}

	sourceSpecs := []struct {
		path   string
		source string
	}{
		{path: desktopInfoPath, source: "workbuddy-desktop"},
		{path: agentSwitchPath, source: "agent-switch"},
		{path: sdkAccountsPath, source: "workbuddy-sdk"},
	}

	seenIndex := make(map[string]int)
	for _, spec := range sourceSpecs {
		if strings.TrimSpace(spec.path) == "" {
			continue
		}
		rawBytes, err := os.ReadFile(spec.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", spec.path, err))
			continue
		}
		result.Sources = append(result.Sources, spec.path)

		trimmed := bytes.TrimSpace(bytes.TrimPrefix(rawBytes, []byte("\ufeff")))
		if len(trimmed) == 0 {
			continue
		}

		parsed, parseErr := parseAccountsJSON(trimmed, spec.source)
		if parseErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", spec.path, parseErr))
			continue
		}

		for _, acc := range parsed {
			applyLocalDeviceRecord(&acc, devices)
			dedupKey := dedupKeyForAccount(acc)
			if idx, exists := seenIndex[dedupKey]; exists {
				mergeImportedAccount(&result.Accounts[idx], acc)
				continue
			}
			seenIndex[dedupKey] = len(result.Accounts)
			result.Accounts = append(result.Accounts, acc)
		}
	}

	return result, nil
}

func loadDevicesMap(path string) (map[string]localDeviceRecord, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\ufeff")))
	if len(trimmed) == 0 {
		return nil, nil
	}
	var out map[string]localDeviceRecord
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func applyLocalDeviceRecord(acc *ImportedAccount, devices map[string]localDeviceRecord) {
	if acc == nil || len(devices) == 0 {
		return
	}
	candidates := make([]string, 0, 7)
	if acc.DeviceName != "" {
		candidates = append(candidates, acc.DeviceName)
	}
	if acc.UID != "" {
		candidates = append(candidates, "acct-"+prefixN(acc.UID, 12), acc.UID)
	}
	if acc.Nickname != "" {
		candidates = append(candidates, "acct-"+prefixN(acc.Nickname, 12), acc.Nickname)
	}
	if acc.Name != "" {
		candidates = append(candidates, "acct-"+prefixN(acc.Name, 12), acc.Name)
	}

	for _, key := range candidates {
		rec, ok := devices[key]
		if !ok {
			continue
		}
		if acc.DeviceName == "" {
			acc.DeviceName = key
		}
		if strings.TrimSpace(acc.Qimei36) == "" && strings.TrimSpace(rec.Qimei36) != "" {
			acc.Qimei36 = strings.TrimSpace(rec.Qimei36)
		}
		if strings.TrimSpace(acc.MachineID) == "" && strings.TrimSpace(rec.MachineID) != "" {
			acc.MachineID = strings.TrimSpace(rec.MachineID)
		}
		return
	}
}

func dedupKeyForAccount(acc ImportedAccount) string {
	if uid := strings.TrimSpace(acc.UID); uid != "" {
		return "uid:" + uid
	}
	return "tok:" + strings.TrimSpace(acc.AccessToken)
}

func mergeImportedAccount(dst *ImportedAccount, src ImportedAccount) {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.UID == "" {
		dst.UID = src.UID
	}
	if dst.Nickname == "" {
		dst.Nickname = src.Nickname
	}
	if dst.Email == "" {
		dst.Email = src.Email
	}
	if dst.EnterpriseID == "" {
		dst.EnterpriseID = src.EnterpriseID
	}
	if dst.EnterpriseName == "" {
		dst.EnterpriseName = src.EnterpriseName
	}
	if dst.Domain == "" {
		dst.Domain = src.Domain
	}
	if dst.AccessToken == "" {
		dst.AccessToken = src.AccessToken
	}
	if dst.RefreshToken == "" {
		dst.RefreshToken = src.RefreshToken
	}
	if dst.ProxyURL == "" {
		dst.ProxyURL = src.ProxyURL
	}
	if dst.Qimei36 == "" {
		dst.Qimei36 = src.Qimei36
	}
	if dst.MachineID == "" {
		dst.MachineID = src.MachineID
	}
	if dst.DeviceName == "" {
		dst.DeviceName = src.DeviceName
	}
}

func parseAccountsJSON(rawBytes []byte, defaultSource string) ([]ImportedAccount, error) {
	var rootAny any
	dec := json.NewDecoder(bytes.NewReader(rawBytes))
	dec.UseNumber()
	if err := dec.Decode(&rootAny); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	var entries []map[string]any
	switch v := rootAny.(type) {
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				entries = append(entries, m)
			}
		}
	case map[string]any:
		entries = extractAccountMapsFromObject(v)
	default:
		return nil, errors.New("unsupported JSON root type")
	}

	if len(entries) == 0 {
		return nil, nil
	}

	accounts := make([]ImportedAccount, 0, len(entries))
	for i, entry := range entries {
		acc, err := importedAccountFromMap(entry, defaultSource, i+1)
		if err != nil {
			return nil, err
		}
		if acc != nil {
			accounts = append(accounts, *acc)
		}
	}
	return accounts, nil
}

func extractAccountMapsFromObject(obj map[string]any) []map[string]any {
	for _, listKey := range []string{"accounts", "items", "list"} {
		if arr, ok := obj[listKey].([]any); ok {
			out := make([]map[string]any, 0, len(arr))
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		}
	}

	if dataArr, ok := obj["data"].([]any); ok {
		out := make([]map[string]any, 0, len(dataArr))
		for _, item := range dataArr {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}

	if dataObj, ok := obj["data"].(map[string]any); ok && dataObj != nil {
		if nested := extractAccountMapsFromObject(dataObj); len(nested) > 0 {
			return nested
		}
	}

	return []map[string]any{obj}
}

func importedAccountFromMap(m map[string]any, defaultSource string, idx int) (*ImportedAccount, error) {
	if m == nil {
		return nil, nil
	}

	authMap := firstNestedMap(m, "auth_raw", "auth")
	acctMap := firstNestedMap(m, "profile_raw", "account", "profile")
	deviceMap := firstNestedMap(m, "device", "devices")
	ssoMap := firstNestedMap(acctMap, "sso")
	if ssoMap == nil {
		ssoMap = firstNestedMap(m, "sso")
	}

	if isEncryptedEnvelope(m["access_token"]) ||
		isEncryptedEnvelope(m["accessToken"]) ||
		isEncryptedEnvelope(authMap["accessToken"]) ||
		isEncryptedEnvelope(authMap["access_token"]) {
		return nil, errors.New("account token is desktop-encrypted ($wbEncrypted) and cannot be imported as plaintext")
	}

	accessToken := fallbackString(
		firstNonEmptyString(m, "access_token", "accessToken", "token"),
		firstNonEmptyString(authMap, "accessToken", "access_token", "token"),
	)
	if accessToken == "" {
		return nil, nil
	}

	refreshToken := fallbackString(
		firstNonEmptyString(m, "refresh_token", "refreshToken"),
		firstNonEmptyString(authMap, "refreshToken", "refresh_token"),
	)

	domainRaw := fallbackString(
		firstNonEmptyString(m, "domain", "base_url", "baseUrl"),
		firstNonEmptyString(authMap, "domain"),
		firstNonEmptyString(ssoMap, "domain"),
		firstNonEmptyString(acctMap, "domain"),
	)
	domain := NormalizeDomain(domainRaw)

	uid := fallbackString(
		firstNonEmptyString(m, "uid", "userId", "user_id"),
		firstNonEmptyString(acctMap, "uid", "userId", "user_id", "id"),
		firstNonEmptyString(authMap, "uid", "userId"),
	)
	nickname := fallbackString(
		firstNonEmptyString(m, "nickname", "nickName"),
		firstNonEmptyString(acctMap, "nickname", "nickName"),
	)
	email := fallbackString(
		firstNonEmptyString(m, "email"),
		firstNonEmptyString(acctMap, "email"),
	)
	phone := fallbackString(
		firstNonEmptyString(m, "phoneNumber", "phone"),
		firstNonEmptyString(acctMap, "phoneNumber", "phone"),
	)
	enterpriseID := fallbackString(
		firstNonEmptyString(m, "enterprise_id", "enterpriseId", "tenantId", "tenant_id"),
		firstNonEmptyString(acctMap, "enterpriseId", "enterprise_id", "tenantId"),
		firstNonEmptyString(authMap, "enterpriseId", "enterprise_id"),
	)
	enterpriseName := fallbackString(
		firstNonEmptyString(m, "enterprise_name", "enterpriseName"),
		firstNonEmptyString(acctMap, "enterpriseName", "enterprise_name"),
	)

	proxyRaw := fallbackString(
		firstNonEmptyString(m, "proxy_url", "proxyUrl", "proxy"),
	)
	proxyURL, err := normalizeImportedProxy(proxyRaw)
	if err != nil {
		return nil, fmt.Errorf("account #%d invalid proxy_url: %w", idx, err)
	}

	qimei36 := fallbackString(
		firstNonEmptyString(m, "qimei36", "qimei"),
		firstNonEmptyString(deviceMap, "qimei36", "qimei"),
	)
	machineID := fallbackString(
		firstNonEmptyString(m, "machine_id", "machineId"),
		firstNonEmptyString(deviceMap, "machine_id", "machineId"),
	)
	deviceName := firstNonEmptyString(m, "device_name", "deviceName")
	if deviceName == "" && uid != "" {
		deviceName = "acct-" + prefixN(uid, 12)
	}

	name := fallbackString(
		firstNonEmptyString(m, "name"),
		nickname,
		email,
		phone,
		uid,
		fmt.Sprintf("account-%d", idx),
	)
	source := fallbackString(firstNonEmptyString(m, "source"), defaultSource)

	return &ImportedAccount{
		Name:           name,
		UID:            uid,
		Nickname:       nickname,
		Email:          email,
		EnterpriseID:   enterpriseID,
		EnterpriseName: enterpriseName,
		Domain:         domain,
		AccessToken:    accessToken,
		RefreshToken:   refreshToken,
		ProxyURL:       proxyURL,
		Qimei36:        qimei36,
		MachineID:      machineID,
		DeviceName:     deviceName,
		Source:         source,
	}, nil
}

func parseAccountsText(raw string, defaultSource string) ([]ImportedAccount, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	accounts := make([]ImportedAccount, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		idx := len(accounts) + 1
		var acc ImportedAccount
		acc.Domain = DefaultDomain
		acc.Source = defaultSource

		switch {
		case strings.Contains(line, "|"):
			parts := strings.Split(line, "|")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			if len(parts) < 2 || parts[1] == "" {
				return nil, fmt.Errorf("invalid pipe-delimited line #%d: missing access_token", idx)
			}
			acc.UID = parts[0]
			acc.AccessToken = parts[1]
			if len(parts) >= 3 {
				acc.RefreshToken = parts[2]
			}
			if len(parts) >= 4 && parts[3] != "" {
				proxyURL, err := normalizeImportedProxy(parts[3])
				if err != nil {
					return nil, fmt.Errorf("line #%d invalid proxy_url: %w", idx, err)
				}
				acc.ProxyURL = proxyURL
			}
			if len(parts) >= 5 && parts[4] != "" {
				acc.Domain = NormalizeDomain(parts[4])
			}
			if acc.UID != "" {
				acc.DeviceName = "acct-" + prefixN(acc.UID, 12)
			}
			acc.Name = fallbackString(acc.UID, fmt.Sprintf("account-%d", idx))

		case strings.Contains(line, ","):
			parts := strings.Split(line, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			if len(parts) < 2 || parts[1] == "" {
				return nil, fmt.Errorf("invalid comma-delimited line #%d: missing access_token", idx)
			}
			acc.Name = fallbackString(parts[0], fmt.Sprintf("account-%d", idx))
			acc.AccessToken = parts[1]
			if len(parts) >= 3 {
				acc.RefreshToken = parts[2]
			}
			if len(parts) >= 4 && parts[3] != "" {
				proxyURL, err := normalizeImportedProxy(parts[3])
				if err != nil {
					return nil, fmt.Errorf("line #%d invalid proxy_url: %w", idx, err)
				}
				acc.ProxyURL = proxyURL
			}

		default:
			acc.AccessToken = line
			acc.Name = fmt.Sprintf("account-%d", idx)
		}

		accounts = append(accounts, acc)
	}

	return accounts, nil
}

func normalizeImportedProxy(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if strings.EqualFold(trimmed, directProxyKeyword) {
		return directProxyKeyword, nil
	}
	return NormalizeProxyURL(trimmed)
}

func firstNestedMap(m map[string]any, keys ...string) map[string]any {
	if m == nil {
		return nil
	}
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		switch t := val.(type) {
		case map[string]any:
			if len(t) > 0 {
				return t
			}
		case string:
			s := strings.TrimSpace(t)
			if strings.HasPrefix(s, "{") {
				var parsed map[string]any
				if json.Unmarshal([]byte(s), &parsed) == nil && len(parsed) > 0 {
					return parsed
				}
			}
		}
	}
	return nil
}

func isEncryptedEnvelope(val any) bool {
	switch t := val.(type) {
	case map[string]any:
		_, ok := t["$wbEncrypted"]
		return ok
	case string:
		s := strings.TrimSpace(t)
		return strings.Contains(s, `"$wbEncrypted"`)
	}
	return false
}

func prefixN(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
