package antigravity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SanitizeAuthFilename 严格校验并规范化凭证文件名：
//   - 严禁包含 "../"、"..\"、"/"、"\"、":" 或空字节等路径穿越字符
//   - 强制要求以 ".json" 结尾且主文件名非空
func SanitizeAuthFilename(filename string) (string, error) {
	s := strings.TrimSpace(filename)
	if s == "" {
		return "", errors.New("凭证文件名不能为空")
	}
	if strings.ContainsRune(s, 0) {
		return "", errors.New("凭证文件名包含非法空字符")
	}
	if strings.Contains(s, "..") || strings.ContainsAny(s, `/\:`) {
		return "", fmt.Errorf("非法的凭证文件名 %q：禁止路径穿越或目录分隔符", filename)
	}
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "-") {
		return "", fmt.Errorf("非法的凭证文件名 %q：不能以 '.' 或 '-' 开头", filename)
	}
	if len(s) <= len(".json") || !strings.HasSuffix(strings.ToLower(s), ".json") {
		return "", fmt.Errorf("非法的凭证文件名 %q：必须以 .json 作为后缀", filename)
	}
	if strings.ContainsAny(s, `<>|"?*`) {
		return "", fmt.Errorf("非法的凭证文件名 %q：包含操作系统保留字符", filename)
	}
	// 规范化后缀大小写为 .json，保持主文件名不变
	stem := strings.TrimSpace(s[:len(s)-len(".json")])
	if stem == "" {
		return "", fmt.Errorf("非法的凭证文件名 %q：主文件名不能为空", filename)
	}
	return stem + ".json", nil
}

func safeResolveAuthFilePath(authDir, filename string) (string, string, error) {
	if strings.TrimSpace(authDir) == "" {
		return "", "", errors.New("auth_dir 不能为空")
	}
	cleanName, err := SanitizeAuthFilename(filename)
	if err != nil {
		return "", "", err
	}
	absDir, err := filepath.Abs(filepath.Clean(authDir))
	if err != nil {
		return "", "", fmt.Errorf("解析 auth_dir 绝对路径失败: %w", err)
	}
	target := filepath.Join(absDir, cleanName)
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("解析目标文件路径失败: %w", err)
	}
	if filepath.Dir(absTarget) != absDir {
		return "", "", fmt.Errorf("路径安全校验失败：%q 超出凭证目录边界", filename)
	}
	return absTarget, cleanName, nil
}

// ParseAuthFileJSON 解析并校验单份 OAuth 登录 JSON 字节流，提取标准化 AuthFileInfo。
func ParseAuthFileJSON(filename string, rawJSON []byte) (AuthFileInfo, error) {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 {
		return AuthFileInfo{}, errors.New("凭证 JSON 内容不能为空")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return AuthFileInfo{}, fmt.Errorf("凭证文件不是合法的 JSON: %w", err)
	}
	// 确保 JSON 尾部没有多余垃圾数据
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return AuthFileInfo{}, errors.New("凭证文件包含多余的非单一 JSON 对象内容")
	}

	root, ok := parsed.(map[string]any)
	if !ok || root == nil {
		return AuthFileInfo{}, errors.New("凭证 JSON 根节点必须是对象 (JSON Object)")
	}

	info := ParseAuthFileMap(filename, root, SourceLocalDir)
	info.SizeBytes = int64(len(rawJSON))
	return info, nil
}

// ParseAuthFileMap 从反序列化后的 JSON Map 中提取并归一化 AntiGravity / Gemini / Codex / Claude 凭证信息。
func ParseAuthFileMap(filename string, root map[string]any, source string) AuthFileInfo {
	if root == nil {
		root = map[string]any{}
	}

	name := strings.TrimSpace(filename)
	if name == "" {
		name = firstString(root, "name", "filename", "file_name", "id")
	}

	nestedToken := firstMap(root, "token", "tokens", "credentials", "oauth")

	accessToken := firstString(root, "access_token", "accessToken", "token")
	if accessToken == "" && nestedToken != nil {
		accessToken = firstString(nestedToken, "access_token", "accessToken")
	}
	refreshToken := firstString(root, "refresh_token", "refreshToken")
	if refreshToken == "" && nestedToken != nil {
		refreshToken = firstString(nestedToken, "refresh_token", "refreshToken")
	}

	email := firstString(root, "email", "account_email", "user_email", "userEmail")
	if email == "" && nestedToken != nil {
		email = firstString(nestedToken, "email")
	}
	if email == "" {
		if acc := firstString(root, "account", "label"); strings.Contains(acc, "@") {
			email = acc
		}
	}
	if email == "" {
		email = extractEmailFromJWT(firstString(root, "id_token", "idToken"))
	}
	if email == "" && nestedToken != nil {
		email = extractEmailFromJWT(firstString(nestedToken, "id_token", "idToken"))
	}
	if email == "" {
		email = extractEmailFromFilename(name)
	}

	projectID := firstString(root, "project_id", "projectId", "project", "quota_project_id")
	if projectID == "" && nestedToken != nil {
		projectID = firstString(nestedToken, "project_id", "projectId")
	}

	rawProvider := firstString(root, "provider", "vendor", "platform")
	rawType := firstString(root, "type", "auth_type")
	accountType := firstString(root, "account_type", "accountType", "plan_type", "planType", "tier")

	provider := normalizeProviderName(rawProvider)
	if provider == "" {
		provider = normalizeProviderName(rawType)
	}
	if provider == "" {
		provider = inferProviderFromHints(name, accessToken, refreshToken, projectID)
	}

	if accountType == "" {
		switch strings.ToLower(strings.TrimSpace(rawType)) {
		case "authorized_user", "service_account", "api_key", "oauth", "oauth2", "device_code":
			accountType = strings.ToLower(strings.TrimSpace(rawType))
		default:
			if refreshToken != "" || accessToken != "" {
				accountType = "oauth"
			} else if rawType != "" {
				accountType = strings.ToLower(strings.TrimSpace(rawType))
			}
		}
	}

	priority := int(firstInt64(root, "priority", "weight"))

	excluded := extractStringsFlexible(root, "excluded-models", "excluded_models", "excludedModels")

	expiresAtStr, expiredFlag := extractExpiry(root, nestedToken)

	disabled := parseBool(root["disabled"])
	if enabledVal, exists := root["enabled"]; exists && !parseBool(enabledVal) {
		disabled = true
	}
	if st := strings.ToLower(firstString(root, "status", "state")); st == "disabled" || st == "inactive" || st == "revoked" {
		disabled = true
	}

	sizeBytes := firstInt64(root, "size", "size_bytes", "sizeBytes")
	updatedAt := firstString(root, "updated_at", "updatedAt", "modtime", "mod_time", "last_refresh")

	return AuthFileInfo{
		Filename:       name,
		Provider:       provider,
		Email:          email,
		ProjectID:      projectID,
		AccountType:    accountType,
		Priority:       priority,
		ExcludedModels: excluded,
		ExpiresAt:      expiresAtStr,
		Expired:        expiredFlag,
		Disabled:       disabled,
		SizeBytes:      sizeBytes,
		UpdatedAt:      updatedAt,
		Source:         source,
		Raw:            root,
	}
}

// ListLocalAuthFiles 扫描宿主机 authDir 目录下的所有 .json 登录凭证文件并归一化返回。
// 若目录尚不存在，优雅返回空切片与 nil 错误。
func ListLocalAuthFiles(authDir string) ([]AuthFileInfo, error) {
	trimmedDir := strings.TrimSpace(authDir)
	if trimmedDir == "" {
		return []AuthFileInfo{}, nil
	}

	entries, err := os.ReadDir(trimmedDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AuthFileInfo{}, nil
		}
		return nil, fmt.Errorf("读取凭证目录 %q 失败: %w", trimmedDir, err)
	}

	result := make([]AuthFileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		cleanName, err := SanitizeAuthFilename(name)
		if err != nil {
			continue
		}
		fullPath := filepath.Join(trimmedDir, name)
		rawBytes, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			continue
		}
		fInfo, statErr := entry.Info()

		parsed, parseErr := ParseAuthFileJSON(cleanName, rawBytes)
		if parseErr != nil {
			// 单个损坏 JSON 不阻断整目录扫描，标记为 disabled 以便管理员在界面识别清理
			fallback := AuthFileInfo{
				Filename: name,
				Disabled: true,
				Source:   SourceLocalDir,
			}
			if statErr == nil && fInfo != nil {
				fallback.SizeBytes = fInfo.Size()
				fallback.UpdatedAt = fInfo.ModTime().UTC().Format(time.RFC3339)
			}
			result = append(result, fallback)
			continue
		}

		parsed.Filename = name
		parsed.Source = SourceLocalDir
		if statErr == nil && fInfo != nil {
			parsed.SizeBytes = fInfo.Size()
			parsed.UpdatedAt = fInfo.ModTime().UTC().Format(time.RFC3339)
		}
		result = append(result, parsed)
	}

	sortAuthFiles(result)
	return result, nil
}

// SaveLocalAuthFile 校验路径安全与合法 JSON 格式后，将登录凭证原子写入宿主机 authDir。
func SaveLocalAuthFile(authDir, filename string, rawJSON []byte) (AuthFileInfo, error) {
	targetPath, cleanName, err := safeResolveAuthFilePath(authDir, filename)
	if err != nil {
		return AuthFileInfo{}, err
	}

	info, err := ParseAuthFileJSON(cleanName, rawJSON)
	if err != nil {
		return AuthFileInfo{}, err
	}

	dirPath := filepath.Dir(targetPath)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return AuthFileInfo{}, fmt.Errorf("创建凭证目录 %q 失败: %w", dirPath, err)
	}

	// 格式化或保留合法 JSON 内容，通过临时文件重命名保证写入完整性
	tmpPath := fmt.Sprintf("%s.tmp.%d", targetPath, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, rawJSON, 0o600); err != nil {
		return AuthFileInfo{}, fmt.Errorf("写入临时凭证文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		// Windows 上某些卷锁情形回退直接覆盖写
		if writeErr := os.WriteFile(targetPath, rawJSON, 0o600); writeErr != nil {
			return AuthFileInfo{}, fmt.Errorf("保存凭证文件 %q 失败: %w", cleanName, writeErr)
		}
	}

	if st, statErr := os.Stat(targetPath); statErr == nil {
		info.SizeBytes = st.Size()
		info.UpdatedAt = st.ModTime().UTC().Format(time.RFC3339)
	} else {
		info.SizeBytes = int64(len(rawJSON))
		info.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	info.Filename = cleanName
	info.Source = SourceLocalDir
	return info, nil
}

// DeleteLocalAuthFile 严格校验文件名路径安全后删除本地凭证文件。
func DeleteLocalAuthFile(authDir, filename string) error {
	targetPath, cleanName, err := safeResolveAuthFilePath(authDir, filename)
	if err != nil {
		return err
	}
	if err := os.Remove(targetPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("凭证文件 %q 不存在: %w", cleanName, os.ErrNotExist)
		}
		return fmt.Errorf("删除凭证文件 %q 失败: %w", cleanName, err)
	}
	return nil
}

// ReadLocalAuthFileRaw 安全读取宿主机 authDir 下指定 .json 凭证文件的原始字节。
func ReadLocalAuthFileRaw(authDir, filename string) ([]byte, error) {
	targetPath, cleanName, err := safeResolveAuthFilePath(authDir, filename)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("凭证文件 %q 不存在: %w", cleanName, os.ErrNotExist)
		}
		return nil, fmt.Errorf("读取凭证文件 %q 失败: %w", cleanName, err)
	}
	return raw, nil
}

// --- 内部解析与归一化工具 ---

func sortAuthFiles(files []AuthFileInfo) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].Priority != files[j].Priority {
			return files[i].Priority > files[j].Priority
		}
		return strings.ToLower(files[i].Filename) < strings.ToLower(files[j].Filename)
	})
}

func normalizeProviderName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "antigravity", "anti-gravity", "anti_gravity", "agy":
		return "antigravity"
	case "gemini", "gemini-cli", "geminicli", "google", "aistudio", "vertex":
		return "gemini"
	case "codex", "openai", "chatgpt":
		return "codex"
	case "claude", "anthropic", "claude-code", "claudecode":
		return "claude"
	case "qwen", "qwen-code":
		return "qwen"
	case "iflow":
		return "iflow"
	default:
		return ""
	}
}

func inferProviderFromHints(filename, accessToken, refreshToken, projectID string) string {
	lowerName := strings.ToLower(strings.TrimSpace(filename))
	switch {
	case strings.HasPrefix(lowerName, "antigravity"), strings.HasPrefix(lowerName, "agy"):
		return "antigravity"
	case strings.HasPrefix(lowerName, "gemini"):
		return "gemini"
	case strings.HasPrefix(lowerName, "codex"), strings.HasPrefix(lowerName, "openai"):
		return "codex"
	case strings.HasPrefix(lowerName, "claude"), strings.HasPrefix(lowerName, "anthropic"):
		return "claude"
	case strings.HasPrefix(lowerName, "qwen"):
		return "qwen"
	case strings.HasPrefix(lowerName, "iflow"):
		return "iflow"
	}

	if strings.HasPrefix(accessToken, "sk-ant-") || strings.HasPrefix(refreshToken, "sk-ant-") {
		return "claude"
	}
	if strings.HasPrefix(accessToken, "ya29.") || strings.HasPrefix(refreshToken, "1//") || projectID != "" {
		return "antigravity"
	}
	return "antigravity"
}

func extractExpiry(root map[string]any, nested map[string]any) (string, bool) {
	explicitExpired := false
	hasExplicitBool := false
	if v, ok := root["expired"]; ok {
		if b, isBool := v.(bool); isBool {
			explicitExpired = b
			hasExplicitBool = true
		}
	}

	candidates := []any{
		root["expiry"],
		root["expires_at"],
		root["expiresAt"],
		root["expire"],
		root["expiration"],
	}
	// CLIProxyAPI 部分版本将过期时间字符串写在 "expired" 字段中
	if v, ok := root["expired"]; ok {
		if _, isStr := v.(string); isStr {
			candidates = append(candidates, v)
		}
	}
	if nested != nil {
		candidates = append(candidates,
			nested["expiry"],
			nested["expires_at"],
			nested["expiresAt"],
			nested["expire"],
		)
	}

	for _, c := range candidates {
		if c == nil {
			continue
		}
		if t, ok := parseFlexibleTime(c); ok {
			isExp := time.Now().After(t)
			if hasExplicitBool && explicitExpired {
				isExp = true
			}
			return t.UTC().Format(time.RFC3339), isExp
		}
	}

	return "", explicitExpired
}

func parseFlexibleTime(val any) (time.Time, bool) {
	switch v := val.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" || strings.EqualFold(s, "true") || strings.EqualFold(s, "false") {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05Z07:00",
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, s); err == nil && t.Year() > 1971 {
				return t, true
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return unixNumberToTime(n)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return unixNumberToTime(n)
		}
		if f, err := v.Float64(); err == nil {
			return unixNumberToTime(int64(f))
		}
	case float64:
		return unixNumberToTime(int64(v))
	case int64:
		return unixNumberToTime(v)
	case int:
		return unixNumberToTime(int64(v))
	}
	return time.Time{}, false
}

func unixNumberToTime(n int64) (time.Time, bool) {
	if n <= 0 {
		return time.Time{}, false
	}
	// 大于 1e12 视为毫秒级 Unix 时间戳
	if n >= 1_000_000_000_000 {
		return time.UnixMilli(n), true
	}
	if n >= 1_000_000_000 {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

func extractEmailFromJWT(jwtToken string) string {
	parts := strings.Split(strings.TrimSpace(jwtToken), ".")
	if len(parts) != 3 {
		return ""
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return ""
	}
	if em := firstString(claims, "email", "upn", "preferred_username"); strings.Contains(em, "@") {
		return em
	}
	return ""
}

func extractEmailFromFilename(filename string) string {
	stem := strings.TrimSuffix(strings.TrimSpace(filename), ".json")
	stem = strings.TrimSuffix(stem, ".JSON")
	if !strings.Contains(stem, "@") {
		return ""
	}
	for _, prefix := range []string{"antigravity-", "agy-", "gemini-", "gemini-cli-", "codex-", "claude-", "anthropic-", "openai-"} {
		if strings.HasPrefix(strings.ToLower(stem), prefix) {
			candidate := stem[len(prefix):]
			if strings.Contains(candidate, "@") {
				return candidate
			}
		}
	}
	return stem
}

func firstMap(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if sub, ok := m[k].(map[string]any); ok && sub != nil {
			return sub
		}
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		switch v := val.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case json.Number:
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return ""
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
		switch v := val.(type) {
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return n
			}
			if f, err := v.Float64(); err == nil {
				return int64(f)
			}
		case float64:
			return int64(v)
		case int64:
			return v
		case int:
			return int64(v)
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

func parseBool(val any) bool {
	switch v := val.(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n != 0
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return false
}

func extractStringsFlexible(m map[string]any, keys ...string) []string {
	if m == nil {
		return nil
	}
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		switch v := val.(type) {
		case []any:
			out := make([]string, 0, len(v))
			for _, elem := range v {
				if s, ok := elem.(string); ok {
					if trimmed := strings.TrimSpace(s); trimmed != "" {
						out = append(out, trimmed)
					}
				}
			}
			if len(out) > 0 {
				return out
			}
		case []string:
			out := make([]string, 0, len(v))
			for _, elem := range v {
				if trimmed := strings.TrimSpace(elem); trimmed != "" {
					out = append(out, trimmed)
				}
			}
			if len(out) > 0 {
				return out
			}
		case string:
			parts := strings.Split(v, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if trimmed := strings.TrimSpace(p); trimmed != "" {
					out = append(out, trimmed)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}
