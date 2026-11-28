package antigravity

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"cmd2api/internal/workbuddy"
)

// 支持的 10 种主流多账号/单账号凭证源格式常量。
const (
	SourceFormatAgyNative     = "agy_native"
	SourceFormatGCloudADC     = "gcloud_adc"
	SourceFormatGeminiCLI     = "gemini_cli"
	SourceFormatCPAExport     = "cpa_export"
	SourceFormatJSONArray     = "json_array"
	SourceFormatNDJSON        = "ndjson"
	SourceFormatCSVTSV        = "csv_tsv"
	SourceFormatDelimitedText = "delimited_text"
	SourceFormatBase64Blob    = "base64_blob"
	SourceFormatZipArchive    = "zip_archive"
)

// ZIP 归档递归安全解压硬上限（防 Zip-Slip 路径穿越与 Zip-Bomb 解压炸弹）。
const (
	MaxZipCompressedBytes    = 32 * 1024 * 1024 // 单个 .zip 最大 32MB
	MaxZipSingleFileBytes    = 8 * 1024 * 1024  // 单个内部文件最大 8MB
	MaxZipTotalUnpackedBytes = 64 * 1024 * 1024 // 整包累计解压上限 64MB
	MaxZipEntryCount         = 512              // 最多条目数
	MaxZipNestedDepth        = 2                // 最大嵌套 .zip 递归层数

	defaultAGYOAuthScope = "https://www.googleapis.com/auth/userinfo.email openid https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.profile"
)

var (
	// ErrZipSlipDetected 表示 ZIP 归档中存在路径穿越或非法绝对路径条目。
	ErrZipSlipDetected = errors.New("zip 安全校验失败：检测到路径穿越或非法条目路径")
	// ErrArchiveTooLarge 表示 ZIP 归档或解压数据超过安全体积上限。
	ErrArchiveTooLarge = errors.New("zip 归档或解压体积超过安全限制")
)

// AdapterInputFile 表示待嗅探与转换的单个输入文件或文本载荷。
type AdapterInputFile struct {
	Filename string
	Content  []byte
}

// ConvertOptions 控制多格式凭证解析、去重与默认填充行为。
type ConvertOptions struct {
	DefaultProxy string
	Deduplicate  *bool // nil 默认为 true
}

// ConvertedAGYAccount 是归一化后的 AntiGravity (`agy` CLI) 标准账号结构，对齐前后端契约。
type ConvertedAGYAccount struct {
	Index              int               `json:"index"`
	SourceFormat       string            `json:"source_format"`
	InnerFormat        string            `json:"inner_format,omitempty"`
	SourceFile         string            `json:"source_file,omitempty"`
	Email              string            `json:"email"`
	Name               string            `json:"name"`
	RefreshToken       string            `json:"-"`
	AccessToken        string            `json:"-"`
	IDToken            string            `json:"-"`
	ClientSecret       string            `json:"-"`
	ClientID           string            `json:"client_id,omitempty"`
	HasClientSecret    bool              `json:"has_client_secret"`
	ProjectID          string            `json:"project_id,omitempty"`
	Scope              string            `json:"scope,omitempty"`
	MaskedRefreshToken string            `json:"masked_refresh_token,omitempty"`
	MaskedAccessToken  string            `json:"masked_access_token,omitempty"`
	HasRefreshToken    bool              `json:"has_refresh_token"`
	HasAccessToken     bool              `json:"has_access_token"`
	ExpiresAt          string            `json:"expires_at,omitempty"`
	ExpiryDateMs       int64             `json:"expiry_date_ms,omitempty"`
	Expired            bool              `json:"expired"`
	Proxy              string            `json:"proxy,omitempty"`
	RawProxy           string            `json:"-"`
	Priority           int               `json:"priority,omitempty"`
	Valid              bool              `json:"valid"`
	Duplicate          bool              `json:"duplicate,omitempty"`
	Error              string            `json:"error,omitempty"`
	Warnings           []string          `json:"warnings"`
	AuthFilename       string            `json:"auth_filename,omitempty"`
	AGYCLIFiles        map[string]string `json:"agy_cli_files,omitempty"`
}

// ConvertSummary 汇总本次转换的统计指标。
type ConvertSummary struct {
	Total           int      `json:"total"`
	ValidCount      int      `json:"valid_count"`
	InvalidCount    int      `json:"invalid_count"`
	DuplicateCount  int      `json:"duplicate_count"`
	DetectedFormats []string `json:"detected_formats"`
}

// ConvertBatchResult 是全格式转换引擎的完整输出（含预览列表、标准 JSON Bundle 与可下载 ZIP Base64）。
type ConvertBatchResult struct {
	OK              bool                  `json:"ok"`
	Total           int                   `json:"total"`
	ValidCount      int                   `json:"valid_count"`
	InvalidCount    int                   `json:"invalid_count"`
	DuplicateCount  int                   `json:"duplicate_count"`
	DetectedFormats []string              `json:"detected_formats"`
	Summary         ConvertSummary        `json:"summary"`
	Accounts        []ConvertedAGYAccount `json:"accounts"`
	BundleFilename  string                `json:"bundle_filename"`
	BundleZipBase64 string                `json:"bundle_zip_base64,omitempty"`
	BundleJSON      map[string]any        `json:"bundle_json,omitempty"`
}

var (
	emailQuickRe      = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)
	authSlugCleanRe   = regexp.MustCompile(`[^a-zA-Z0-9._@\-]+`)
	base64CharsetRe   = regexp.MustCompile(`^[A-Za-z0-9+/=_\-]+$`)
	nonCredGeminiJSON = map[string]struct{}{
		"google_accounts.json": {},
		"settings.json":        {},
		"state.json":           {},
		"projects.json":        {},
		"trustedfolders.json":  {},
		"manifest.json":        {},
		"ultra-desktop.json":   {},
		"ultra-locale.json":    {},
		"ultra-prompts.json":   {},
		"ultra-providers.json": {},
		"ultra-runtime.json":   {},
		"ultra-tools.json":     {},
	}
)

// IsCompanionNonCredJSON 判断文件名是否为 `.gemini` 目录下的非凭证配置/伴生元数据文件。
func IsCompanionNonCredJSON(filename string) bool {
	base := strings.ToLower(strings.TrimSpace(filepath.Base(filename)))
	_, ok := nonCredGeminiJSON[base]
	return ok
}

// ValidateZipEntryPath 严格拦截包含 `..`、绝对路径、盘符或空字节的 Zip-Slip 恶意条目。
func ValidateZipEntryPath(entryName string) error {
	if strings.TrimSpace(entryName) == "" {
		return fmt.Errorf("%w: 空文件名", ErrZipSlipDetected)
	}
	if strings.ContainsRune(entryName, 0) {
		return fmt.Errorf("%w: 包含空字节 %q", ErrZipSlipDetected, entryName)
	}
	if strings.HasPrefix(entryName, "/") || strings.HasPrefix(entryName, `\`) {
		return fmt.Errorf("%w: 禁止绝对路径 %q", ErrZipSlipDetected, entryName)
	}
	if strings.Contains(entryName, ":") {
		return fmt.Errorf("%w: 禁止包含盘符或冒号 %q", ErrZipSlipDetected, entryName)
	}
	normalized := strings.ReplaceAll(entryName, `\`, "/")
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: 检测到上级目录穿越段 %q", ErrZipSlipDetected, entryName)
		}
	}
	cleaned := path.Clean(normalized)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return fmt.Errorf("%w: 规范化后路径越界 %q", ErrZipSlipDetected, entryName)
	}
	return nil
}

// ConvertCredentialsToAGY 接收一个或多个任意格式的凭证文件/原始文本，自动嗅探格式并转换为原生 `agy` CLI 账号包。
func ConvertCredentialsToAGY(inputs []AdapterInputFile, opts ConvertOptions) (*ConvertBatchResult, error) {
	if len(inputs) == 0 {
		return nil, errors.New("未提供任何待转换的凭证文件或文本")
	}

	defaultProxy := strings.TrimSpace(opts.DefaultProxy)
	if defaultProxy != "" && !strings.EqualFold(defaultProxy, "direct") {
		normP, err := workbuddy.NormalizeProxyURL(defaultProxy)
		if err != nil {
			return nil, fmt.Errorf("默认代理地址格式无效: %w", err)
		}
		defaultProxy = normP
	}

	dedupEnabled := true
	if opts.Deduplicate != nil {
		dedupEnabled = *opts.Deduplicate
	}

	// 1. 预扫描同级伴生 google_accounts.json，以便与同目录 oauth_creds.json 自动配对补齐邮箱
	companionEmails := collectCompanionGoogleAccounts(inputs)

	var rawAccounts []ConvertedAGYAccount
	for _, in := range inputs {
		fname := strings.TrimSpace(in.Filename)
		baseLower := strings.ToLower(filepath.Base(strings.ReplaceAll(fname, `\`, "/")))
		if baseLower == "google_accounts.json" && len(inputs) > 1 {
			continue
		}
		if IsCompanionNonCredJSON(baseLower) && baseLower != "google_accounts.json" && len(inputs) > 1 {
			continue
		}

		var unpackBudget int64 = MaxZipTotalUnpackedBytes
		parsed, err := parseSingleInput(in.Filename, in.Content, companionEmails, 0, &unpackBudget)
		if err != nil {
			if errors.Is(err, ErrZipSlipDetected) || errors.Is(err, ErrArchiveTooLarge) {
				return nil, err
			}
			rawAccounts = append(rawAccounts, ConvertedAGYAccount{
				SourceFormat: inferFallbackFormatFromName(fname),
				SourceFile:   fname,
				Valid:        false,
				Error:        err.Error(),
				Warnings:     []string{},
			})
			continue
		}
		rawAccounts = append(rawAccounts, parsed...)
	}

	if len(rawAccounts) == 0 {
		return nil, errors.New("未从输入内容中识别出任何有效账号记录")
	}

	// 2. 归一化、补全默认代理、去重与合并互补字段、生成 agy CLI 标准文件集
	dedupIndexByKey := make(map[string]int, len(rawAccounts))
	usedFilenames := make(map[string]int, len(rawAccounts))
	finalAccounts := make([]ConvertedAGYAccount, 0, len(rawAccounts))
	formatSet := make(map[string]struct{})

	var validCount, invalidCount, duplicateCount int

	for _, item := range rawAccounts {
		finalizeAccountFields(&item, defaultProxy)
		if item.SourceFormat != "" {
			formatSet[item.SourceFormat] = struct{}{}
		}
		if item.InnerFormat != "" {
			formatSet[item.InnerFormat] = struct{}{}
		}

		if !item.Valid {
			item.Index = len(finalAccounts)
			finalAccounts = append(finalAccounts, item)
			invalidCount++
			continue
		}

		dedupKey := accountDedupKey(item)
		if dedupKey != "" {
			if prevIdx, exists := dedupIndexByKey[dedupKey]; exists {
				// 将后出现条目的互补非空字段（如 email / project_id / client_id / proxy / access_token）融合进主记录
				mergeComplementaryAccountFields(&finalAccounts[prevIdx], item)
				rebuildAccountAGYFiles(&finalAccounts[prevIdx], usedFilenames, false)
				duplicateCount++
				if dedupEnabled {
					continue
				}
				item.Duplicate = true
				item.Warnings = appendUniqueWarning(item.Warnings, "检测到与前序条目相同的凭证，已标记为重复项")
			} else {
				dedupIndexByKey[dedupKey] = len(finalAccounts)
			}
		}

		rebuildAccountAGYFiles(&item, usedFilenames, true)
		item.Index = len(finalAccounts)
		finalAccounts = append(finalAccounts, item)
		validCount++
	}

	formats := make([]string, 0, len(formatSet))
	for f := range formatSet {
		formats = append(formats, f)
	}
	sort.Strings(formats)

	totalReported := len(finalAccounts) + duplicateCount
	if !dedupEnabled {
		totalReported = len(finalAccounts)
	}

	summary := ConvertSummary{
		Total:           totalReported,
		ValidCount:      validCount,
		InvalidCount:    invalidCount,
		DuplicateCount:  duplicateCount,
		DetectedFormats: formats,
	}

	res := &ConvertBatchResult{
		OK:              validCount > 0,
		Total:           summary.Total,
		ValidCount:      validCount,
		InvalidCount:    invalidCount,
		DuplicateCount:  duplicateCount,
		DetectedFormats: formats,
		Summary:         summary,
		Accounts:        finalAccounts,
		BundleFilename:  "agy-cli-credentials-bundle.zip",
	}

	if validCount > 0 {
		res.BundleJSON = buildAGYBundleJSON(finalAccounts)
		if zipB64, err := buildAGYBundleZipBase64(finalAccounts, res.BundleJSON); err == nil {
			res.BundleZipBase64 = zipB64
		}
	}

	return res, nil
}

// NormalizeSingleCredential 归一化单份任意格式字节流（供 UploadAuthFile 与 RemotePushAccounts 直接复用）。
func NormalizeSingleCredential(filename string, raw []byte, defaultProxy string) (ConvertedAGYAccount, error) {
	fFalse := false
	batch, err := ConvertCredentialsToAGY([]AdapterInputFile{
		{Filename: filename, Content: raw},
	}, ConvertOptions{
		DefaultProxy: defaultProxy,
		Deduplicate:  &fFalse,
	})
	if err != nil {
		return ConvertedAGYAccount{}, err
	}
	for _, acc := range batch.Accounts {
		if acc.Valid {
			return acc, nil
		}
	}
	if len(batch.Accounts) > 0 && batch.Accounts[0].Error != "" {
		return ConvertedAGYAccount{}, errors.New(batch.Accounts[0].Error)
	}
	return ConvertedAGYAccount{}, errors.New("未识别到有效的 OAuth / API 凭证")
}

// CanonicalAuthJSONBytes 返回指定账号用于 Analy2API / CLIProxyAPI 持久化的标准 auth.json 字节流。
func (a ConvertedAGYAccount) CanonicalAuthJSONBytes() []byte {
	if a.AGYCLIFiles != nil {
		if content, ok := a.AGYCLIFiles["auth.json"]; ok && strings.TrimSpace(content) != "" {
			return []byte(content)
		}
	}
	m := buildCanonicalAuthMap(a)
	b, _ := json.MarshalIndent(m, "", "  ")
	return b
}

// WriteIsolatedAGYAuthFiles 将归一化后的账号写成真实 `agy` CLI 开箱可识别的隔离目录文件结构：
//   - `<authDir>/.gemini/oauth_creds.json`
//   - `<authDir>/.gemini/google_accounts.json`
//   - `<authDir>/.config/gcloud/application_default_credentials.json`（当存在 refresh_token 时）
//   - `<authDir>/<primaryAuthFilename>`（为空时写 `auth.json`）
//
// 安全护栏：绝不覆盖宿主机操作系统当前登录用户的真实个人主目录（`~` 或 `~/.gemini`）。
func WriteIsolatedAGYAuthFiles(authDir string, acc ConvertedAGYAccount, primaryAuthFilename string) ([]string, error) {
	trimmedDir := strings.TrimSpace(authDir)
	if trimmedDir == "" {
		return nil, errors.New("auth_dir 不能为空")
	}
	absDir, err := filepath.Abs(filepath.Clean(trimmedDir))
	if err != nil {
		return nil, fmt.Errorf("解析 auth_dir 路径失败: %w", err)
	}
	if isHostPersonalGeminiOrHomeDir(absDir) {
		return nil, errors.New("安全护栏拒绝写入宿主机真实个人主目录 ~/.gemini")
	}

	if len(acc.AGYCLIFiles) == 0 {
		used := map[string]int{}
		rebuildAccountAGYFiles(&acc, used, true)
	}

	primaryName := strings.TrimSpace(primaryAuthFilename)
	if primaryName == "" {
		primaryName = acc.AuthFilename
	}
	if primaryName == "" {
		primaryName = "auth.json"
	}
	cleanPrimary, err := SanitizeAuthFilename(primaryName)
	if err != nil {
		cleanPrimary = "auth.json"
	}

	filesToWrite := make(map[string]string, len(acc.AGYCLIFiles)+1)
	for relPath, content := range acc.AGYCLIFiles {
		if relPath == "auth.json" {
			filesToWrite[cleanPrimary] = content
			continue
		}
		filesToWrite[relPath] = content
	}
	if _, hasPrimary := filesToWrite[cleanPrimary]; !hasPrimary {
		filesToWrite[cleanPrimary] = string(acc.CanonicalAuthJSONBytes())
	}

	written := make([]string, 0, len(filesToWrite))
	keys := make([]string, 0, len(filesToWrite))
	for k := range filesToWrite {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, rel := range keys {
		if vErr := ValidateZipEntryPath(rel); vErr != nil {
			return written, vErr
		}
		target := filepath.Join(absDir, filepath.FromSlash(rel))
		absTarget, aErr := filepath.Abs(target)
		if aErr != nil {
			return written, aErr
		}
		if absTarget != absDir && !strings.HasPrefix(strings.ToLower(absTarget), strings.ToLower(absDir)+string(filepath.Separator)) {
			return written, fmt.Errorf("目标路径越界: %s", rel)
		}
		if err := os.MkdirAll(filepath.Dir(absTarget), 0o755); err != nil {
			return written, fmt.Errorf("创建凭证子目录失败: %w", err)
		}
		if err := atomicWriteFile(absTarget, []byte(filesToWrite[rel]), 0o600); err != nil {
			return written, fmt.Errorf("写入凭证文件 %s 失败: %w", rel, err)
		}
		written = append(written, rel)
	}
	// 预创建 agy CLI 启动时检查的会话目录，避免首轮日志打印目录不存在告警
	_ = os.MkdirAll(filepath.Join(absDir, ".gemini", "antigravity", "conversations"), 0o755)
	return written, nil
}

func isHostPersonalGeminiOrHomeDir(absDir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	absHome, err := filepath.Abs(filepath.Clean(home))
	if err != nil {
		return false
	}
	cleanTarget := filepath.Clean(absDir)
	if strings.EqualFold(cleanTarget, absHome) {
		return true
	}
	realGemini := filepath.Join(absHome, ".gemini")
	if strings.EqualFold(cleanTarget, realGemini) {
		return true
	}
	realAgyCLI := filepath.Join(realGemini, "antigravity-cli")
	if strings.EqualFold(cleanTarget, realAgyCLI) {
		return true
	}
	return false
}

func atomicWriteFile(targetPath string, data []byte, perm os.FileMode) error {
	tmpPath := fmt.Sprintf("%s.tmp.%d", targetPath, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return os.WriteFile(targetPath, data, perm)
	}
	return nil
}

// --- 核心多格式自动嗅探与解析流水线 ---

func collectCompanionGoogleAccounts(files []AdapterInputFile) map[string]string {
	out := make(map[string]string)
	for _, f := range files {
		norm := strings.ReplaceAll(strings.TrimSpace(f.Filename), `\`, "/")
		base := strings.ToLower(path.Base(norm))
		if base != "google_accounts.json" {
			continue
		}
		email := parseActiveEmailFromGoogleAccountsJSON(f.Content)
		if email == "" {
			continue
		}
		dir := strings.Trim(path.Dir(norm), "./")
		out[dir] = email
		if strings.HasSuffix(strings.ToLower(dir), ".gemini") {
			parent := strings.Trim(path.Dir(dir), "./")
			if parent == "." {
				parent = ""
			}
			if _, exists := out[parent]; !exists {
				out[parent] = email
			}
		}
		if _, exists := out[""]; !exists {
			out[""] = email
		}
	}
	return out
}

func parseActiveEmailFromGoogleAccountsJSON(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(bytes.TrimSpace(raw), &m) != nil || m == nil {
		return ""
	}
	if active := firstString(m, "active", "email", "current"); strings.Contains(active, "@") {
		return active
	}
	if oldArr, ok := m["old"].([]any); ok {
		for _, item := range oldArr {
			if s, ok := item.(string); ok && strings.Contains(s, "@") {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func parseSingleInput(
	filename string,
	content []byte,
	companionEmails map[string]string,
	zipDepth int,
	unpackBudget *int64,
) ([]ConvertedAGYAccount, error) {
	fname := strings.TrimSpace(filename)
	lowerName := strings.ToLower(fname)

	// 1. 检测是否为 .zip 压缩包（PK 魔数或 .zip 后缀）
	if isZipPayload(lowerName, content) {
		return parseZipArchiveInput(fname, content, zipDepth, unpackBudget)
	}

	cleaned := stripUTF8BOM(content)
	trimmed := bytes.TrimSpace(cleaned)
	if len(trimmed) == 0 {
		return nil, errors.New("文件内容为空")
	}

	// 2. 检测整段 Base64 编码的 ZIP 或 JSON/文本载荷
	if decoded, ok := tryDecodeWholeBase64Payload(trimmed); ok {
		if isZipPayload("", decoded) {
			accounts, err := parseZipArchiveInput(fname, decoded, zipDepth, unpackBudget)
			if err != nil {
				return nil, err
			}
			for i := range accounts {
				if accounts[i].InnerFormat == "" {
					accounts[i].InnerFormat = accounts[i].SourceFormat
				}
				accounts[i].SourceFormat = SourceFormatBase64Blob
			}
			return accounts, nil
		}
		innerAccounts, err := parseDecodedTextOrJSON(fname, decoded, companionEmails)
		if err == nil && len(innerAccounts) > 0 {
			for i := range innerAccounts {
				if innerAccounts[i].InnerFormat == "" {
					innerAccounts[i].InnerFormat = innerAccounts[i].SourceFormat
				}
				innerAccounts[i].SourceFormat = SourceFormatBase64Blob
			}
			return innerAccounts, nil
		}
	}

	return parseDecodedTextOrJSON(fname, trimmed, companionEmails)
}

func parseDecodedTextOrJSON(
	filename string,
	trimmed []byte,
	companionEmails map[string]string,
) ([]ConvertedAGYAccount, error) {
	lowerName := strings.ToLower(filename)
	companionEmail := lookupCompanionEmail(filename, companionEmails)

	// 1. 以 `[` 开头：优先按 JSON Array 解析
	if trimmed[0] == '[' {
		accounts, err := parseJSONArrayInput(filename, trimmed, companionEmail)
		if err == nil {
			return accounts, nil
		}
	}

	// 2. 以 `{` 开头：尝试单 JSON 对象（含 CPA/Sub2API/OneAPI 包装字典）或 NDJSON / 拼接多 JSON 对象
	if trimmed[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		var firstObj any
		if err := dec.Decode(&firstObj); err == nil {
			var secondObj any
			if err2 := dec.Decode(&secondObj); errors.Is(err2, io.EOF) {
				if rootMap, ok := firstObj.(map[string]any); ok {
					return parseSingleOrWrappedJSONMap(filename, rootMap, companionEmail)
				}
			} else {
				// 存在多个连续 JSON 对象（NDJSON 或手贴多个 JSON 文件）
				return parseNDJSONOrConcatObjects(filename, string(trimmed), companionEmail)
			}
		}
	}

	text := strings.ReplaceAll(string(trimmed), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// 3. 明确为 .jsonl / .ndjson 后缀或所有非空行均为 `{...}` JSON 行
	if strings.HasSuffix(lowerName, ".jsonl") || strings.HasSuffix(lowerName, ".ndjson") || looksLikeNDJSONLines(text) {
		return parseNDJSONOrConcatObjects(filename, text, companionEmail)
	}

	// 4. 检测 CSV / TSV（带表头或无表头逗号/制表符分隔）
	if strings.HasSuffix(lowerName, ".csv") || strings.HasSuffix(lowerName, ".tsv") || looksLikeCSVOrTSV(text) {
		return parseCSVOrTSVInput(filename, text)
	}

	// 5. 逐行解析（兼容逐行 Base64、分隔符 `----` / `|` / `:`、裸 `1//...` RefreshToken / `ya29.` AccessToken）
	return parseLineDelimitedOrBase64Lines(filename, text, companionEmail)
}

func isZipPayload(lowerFilename string, raw []byte) bool {
	if len(raw) >= 4 && (bytes.HasPrefix(raw, []byte("PK\x03\x04")) || bytes.HasPrefix(raw, []byte("PK\x05\x06")) || bytes.HasPrefix(raw, []byte("PK\x07\x08"))) {
		return true
	}
	return strings.HasSuffix(lowerFilename, ".zip")
}

func parseZipArchiveInput(
	archiveName string,
	raw []byte,
	depth int,
	unpackBudget *int64,
) ([]ConvertedAGYAccount, error) {
	if depth > MaxZipNestedDepth {
		return nil, fmt.Errorf("嵌套 zip 层级超过安全上限 (%d)", MaxZipNestedDepth)
	}
	if len(raw) > MaxZipCompressedBytes {
		return nil, fmt.Errorf("%w: %s (%d bytes > %d bytes)", ErrArchiveTooLarge, archiveName, len(raw), MaxZipCompressedBytes)
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("解析 zip 归档 %q 失败: %w", archiveName, err)
	}
	if len(zr.File) > MaxZipEntryCount {
		return nil, fmt.Errorf("%w: zip 条目数 %d 超过上限 %d", ErrArchiveTooLarge, len(zr.File), MaxZipEntryCount)
	}

	// 第一遍：强制对所有条目执行 Zip-Slip 路径安全检查（即便是非解析后缀也不放过恶意穿越条目）
	for _, zf := range zr.File {
		cleanEntry := strings.TrimSuffix(zf.Name, "/")
		if cleanEntry == "" {
			continue
		}
		if err := ValidateZipEntryPath(zf.Name); err != nil {
			return nil, err
		}
	}

	// 第二遍：安全读取支持的凭证条目到内存
	var extracted []AdapterInputFile
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		normName := strings.ReplaceAll(zf.Name, `\`, "/")
		if shouldSkipBenignArchiveMetadata(normName) {
			continue
		}
		if !isSupportedArchiveEntry(normName) {
			continue
		}
		if zf.UncompressedSize64 > MaxZipSingleFileBytes {
			return nil, fmt.Errorf("%w: 条目 %q 解压大小 %d 超过单文件上限", ErrArchiveTooLarge, normName, zf.UncompressedSize64)
		}

		rc, openErr := zf.Open()
		if openErr != nil {
			return nil, fmt.Errorf("读取 zip 条目 %q 失败: %w", normName, openErr)
		}
		// 使用 LimitReader +1 字节精确识别虚报 Header 的 Zip-Bomb
		data, readErr := io.ReadAll(io.LimitReader(rc, MaxZipSingleFileBytes+1))
		_ = rc.Close()
		if readErr != nil {
			return nil, fmt.Errorf("解压 zip 条目 %q 失败: %w", normName, readErr)
		}
		if int64(len(data)) > MaxZipSingleFileBytes {
			return nil, fmt.Errorf("%w: 条目 %q 实际解压体积超出单文件限制", ErrArchiveTooLarge, normName)
		}
		*unpackBudget -= int64(len(data))
		if *unpackBudget < 0 {
			return nil, fmt.Errorf("%w: 累计解压字节数超过上限 %dMB", ErrArchiveTooLarge, MaxZipTotalUnpackedBytes/(1024*1024))
		}

		displaySource := normName
		if archiveName != "" {
			displaySource = archiveName + ":" + normName
		}
		extracted = append(extracted, AdapterInputFile{
			Filename: normName,
			Content:  data,
		})
		_ = displaySource
	}

	if len(extracted) == 0 {
		return nil, fmt.Errorf("zip 归档 %q 中未找到支持的凭证文件 (.json/.jsonl/.csv/.tsv/.txt)", archiveName)
	}
	sort.Slice(extracted, func(i, j int) bool {
		return extracted[i].Filename < extracted[j].Filename
	})

	companions := collectCompanionGoogleAccounts(extracted)
	var out []ConvertedAGYAccount
	for _, ef := range extracted {
		baseLower := strings.ToLower(path.Base(ef.Filename))
		if baseLower == "google_accounts.json" && len(extracted) > 1 {
			continue
		}
		if IsCompanionNonCredJSON(baseLower) && baseLower != "google_accounts.json" {
			continue
		}
		sub, subErr := parseSingleInput(ef.Filename, ef.Content, companions, depth+1, unpackBudget)
		if subErr != nil {
			if errors.Is(subErr, ErrZipSlipDetected) || errors.Is(subErr, ErrArchiveTooLarge) {
				return nil, subErr
			}
			srcLabel := ef.Filename
			if archiveName != "" {
				srcLabel = archiveName + ":" + ef.Filename
			}
			out = append(out, ConvertedAGYAccount{
				SourceFormat: SourceFormatZipArchive,
				InnerFormat:  inferFallbackFormatFromName(ef.Filename),
				SourceFile:   srcLabel,
				Valid:        false,
				Error:        subErr.Error(),
				Warnings:     []string{},
			})
			continue
		}
		for i := range sub {
			if sub[i].InnerFormat == "" && sub[i].SourceFormat != SourceFormatZipArchive {
				sub[i].InnerFormat = sub[i].SourceFormat
			}
			sub[i].SourceFormat = SourceFormatZipArchive
			if archiveName != "" && !strings.HasPrefix(sub[i].SourceFile, archiveName+":") {
				sub[i].SourceFile = archiveName + ":" + ef.Filename
			}
		}
		out = append(out, sub...)
	}
	return out, nil
}

func shouldSkipBenignArchiveMetadata(normName string) bool {
	lower := strings.ToLower(normName)
	base := path.Base(lower)
	if strings.HasPrefix(lower, "__macosx/") || strings.Contains(lower, "/__macosx/") {
		return true
	}
	if base == ".ds_store" || base == "thumbs.db" || base == "desktop.ini" || base == "gemini.md" {
		return true
	}
	return false
}

func isSupportedArchiveEntry(normName string) bool {
	ext := strings.ToLower(path.Ext(normName))
	switch ext {
	case ".json", ".jsonl", ".ndjson", ".csv", ".tsv", ".txt", ".dat", ".conf", ".creds", ".zip":
		return true
	case "":
		base := strings.ToLower(path.Base(normName))
		return strings.Contains(base, "cred") || strings.Contains(base, "auth") || strings.Contains(base, "token") || strings.Contains(base, "account")
	default:
		return false
	}
}

func lookupCompanionEmail(filename string, companions map[string]string) string {
	if len(companions) == 0 {
		return ""
	}
	norm := strings.ReplaceAll(strings.TrimSpace(filename), `\`, "/")
	dir := strings.Trim(path.Dir(norm), "./")
	if dir == "." {
		dir = ""
	}
	if em := companions[dir]; em != "" {
		return em
	}
	if em := companions[path.Join(dir, ".gemini")]; em != "" {
		return em
	}
	if len(companions) == 1 {
		for _, em := range companions {
			return em
		}
	}
	return ""
}

func parseJSONArrayInput(filename string, raw []byte, companionEmail string) ([]ConvertedAGYAccount, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var arr []any
	if err := dec.Decode(&arr); err != nil {
		return nil, fmt.Errorf("解析 JSON 数组失败: %w", err)
	}
	if len(arr) == 0 {
		return nil, errors.New("JSON 账号数组为空")
	}
	out := make([]ConvertedAGYAccount, 0, len(arr))
	for idx, elem := range arr {
		switch v := elem.(type) {
		case map[string]any:
			acc := extractAccountFromObjectMap(filename, v, companionEmail)
			acc.InnerFormat = acc.SourceFormat
			acc.SourceFormat = SourceFormatJSONArray
			out = append(out, acc)
		case string:
			sub, sErr := parseLineDelimitedOrBase64Lines(filename, v, companionEmail)
			if sErr != nil || len(sub) == 0 {
				out = append(out, ConvertedAGYAccount{
					SourceFormat: SourceFormatJSONArray,
					SourceFile:   filename,
					Valid:        false,
					Error:        fmt.Sprintf("数组第 %d 项未能解析为有效账号", idx+1),
					Warnings:     []string{},
				})
				continue
			}
			for i := range sub {
				sub[i].InnerFormat = sub[i].SourceFormat
				sub[i].SourceFormat = SourceFormatJSONArray
			}
			out = append(out, sub...)
		default:
			out = append(out, ConvertedAGYAccount{
				SourceFormat: SourceFormatJSONArray,
				SourceFile:   filename,
				Valid:        false,
				Error:        fmt.Sprintf("数组第 %d 项必须是对象或凭证字符串", idx+1),
				Warnings:     []string{},
			})
		}
	}
	return out, nil
}

func parseSingleOrWrappedJSONMap(filename string, root map[string]any, companionEmail string) ([]ConvertedAGYAccount, error) {
	// 1. 检查是否为 CPA / CLIProxyAPI / Sub2API / OneAPI / Analy2API 批量导出字典结构
	if entries, isWrapper := extractWrappedAccountMaps(root); isWrapper {
		if len(entries) == 0 {
			return nil, errors.New("导出 JSON 的账号列表为空")
		}
		out := make([]ConvertedAGYAccount, 0, len(entries))
		for _, m := range entries {
			acc := extractAccountFromObjectMap(filename, m, companionEmail)
			if acc.SourceFormat != SourceFormatCPAExport {
				acc.InnerFormat = acc.SourceFormat
			}
			acc.SourceFormat = SourceFormatCPAExport
			out = append(out, acc)
		}
		return out, nil
	}

	// 2. 若单独上传了 google_accounts.json 但不含任何 token，给出明确错误提示
	if strings.EqualFold(path.Base(strings.ReplaceAll(filename, `\`, "/")), "google_accounts.json") &&
		firstString(root, "refresh_token", "access_token") == "" {
		return []ConvertedAGYAccount{{
			SourceFormat: SourceFormatGeminiCLI,
			SourceFile:   filename,
			Email:        parseActiveEmailFromGoogleAccountsJSONMust(root),
			Valid:        false,
			Error:        "google_accounts.json 仅包含邮箱索引，请同时上传对应的 oauth_creds.json",
			Warnings:     []string{},
		}}, nil
	}

	acc := extractAccountFromObjectMap(filename, root, companionEmail)
	return []ConvertedAGYAccount{acc}, nil
}

func parseActiveEmailFromGoogleAccountsJSONMust(root map[string]any) string {
	return firstString(root, "active", "email")
}

func extractWrappedAccountMaps(root map[string]any) ([]map[string]any, bool) {
	for _, key := range []string{"accounts", "items", "auth_files", "authFiles", "files", "list", "channels", "users"} {
		val, exists := root[key]
		if !exists || val == nil {
			continue
		}
		switch typed := val.(type) {
		case []any:
			return anySliceToObjectMaps(typed), true
		case map[string]any:
			if nested, ok := extractWrappedAccountMaps(typed); ok {
				return nested, true
			}
			if keyed := keyedObjectMapToSlice(typed); len(keyed) > 0 {
				return keyed, true
			}
		}
	}
	if dataVal, exists := root["data"]; exists && dataVal != nil {
		switch typed := dataVal.(type) {
		case []any:
			return anySliceToObjectMaps(typed), true
		case map[string]any:
			if nested, ok := extractWrappedAccountMaps(typed); ok {
				return nested, true
			}
			// {"data": {"access_token": "...", "refresh_token": "..."}} 单账号信封
			if hasDirectCredFields(typed) {
				return []map[string]any{typed}, true
			}
		}
	}
	return nil, false
}

func anySliceToObjectMaps(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, elem := range arr {
		switch v := elem.(type) {
		case map[string]any:
			out = append(out, v)
		case string:
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(v)), &m) == nil && m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

func keyedObjectMapToSlice(m map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(m))
	for k, v := range m {
		sub, ok := v.(map[string]any)
		if !ok || sub == nil || !hasDirectCredFields(sub) {
			continue
		}
		cp := make(map[string]any, len(sub)+1)
		for sk, sv := range sub {
			cp[sk] = sv
		}
		if firstString(cp, "name", "filename", "email") == "" {
			cp["name"] = k
		}
		out = append(out, cp)
	}
	return out
}

func hasDirectCredFields(m map[string]any) bool {
	if firstString(m, "refresh_token", "refreshToken", "access_token", "accessToken", "api_key", "apiKey", "token", "key") != "" {
		return true
	}
	for _, nk := range []string{"credentials", "token", "tokens", "oauth", "auth_json", "token_json"} {
		if _, ok := m[nk]; ok {
			return true
		}
	}
	return false
}

func extractAccountFromObjectMap(filename string, root map[string]any, companionEmail string) ConvertedAGYAccount {
	if root == nil {
		root = map[string]any{}
	}

	nestedCreds := extractNestedMapOrJSONString(root, "credentials", "token", "tokens", "oauth", "auth_json", "token_json", "auth", "secret", "config", "other")
	extraMap := extractNestedMapOrJSONString(root, "extra", "metadata", "meta", "user_info", "profile")

	detectedFormat := classifySingleJSONObjectFormat(filename, root, nestedCreds)

	refreshToken := firstString(root, "refresh_token", "refreshToken", "rt")
	if refreshToken == "" && nestedCreds != nil {
		refreshToken = firstString(nestedCreds, "refresh_token", "refreshToken", "rt")
	}

	accessToken := firstString(root, "access_token", "accessToken", "api_key", "apiKey")
	if accessToken == "" {
		if tokStr := firstString(root, "token", "key"); tokStr != "" {
			// OneAPI / Sub2API 有时将 JSON 字符串或 1// RefreshToken 放在 key 字段中
			if strings.HasPrefix(tokStr, "{") {
				var keyMap map[string]any
				if json.Unmarshal([]byte(tokStr), &keyMap) == nil && keyMap != nil {
					if nestedCreds == nil {
						nestedCreds = keyMap
					}
					if refreshToken == "" {
						refreshToken = firstString(keyMap, "refresh_token", "refreshToken")
					}
					accessToken = firstString(keyMap, "access_token", "accessToken", "api_key")
				}
			} else if strings.HasPrefix(tokStr, "1//") && refreshToken == "" {
				refreshToken = tokStr
			} else {
				accessToken = tokStr
			}
		}
	}
	if accessToken == "" && nestedCreds != nil {
		accessToken = firstString(nestedCreds, "access_token", "accessToken", "api_key", "apiKey", "token")
		if strings.HasPrefix(accessToken, "1//") && refreshToken == "" {
			refreshToken = accessToken
			accessToken = ""
		}
	}

	idToken := firstString(root, "id_token", "idToken")
	if idToken == "" && nestedCreds != nil {
		idToken = firstString(nestedCreds, "id_token", "idToken")
	}

	clientID := firstString(root, "client_id", "clientId")
	if clientID == "" && nestedCreds != nil {
		clientID = firstString(nestedCreds, "client_id", "clientId")
	}
	if clientID == "" && extraMap != nil {
		clientID = firstString(extraMap, "client_id", "clientId")
	}

	clientSecret := firstString(root, "client_secret", "clientSecret")
	if clientSecret == "" && nestedCreds != nil {
		clientSecret = firstString(nestedCreds, "client_secret", "clientSecret")
	}
	if clientSecret == "" && extraMap != nil {
		clientSecret = firstString(extraMap, "client_secret", "clientSecret")
	}

	projectID := firstString(root, "project_id", "projectId", "quota_project_id", "quotaProjectId", "project", "enterprise_id")
	if projectID == "" && nestedCreds != nil {
		projectID = firstString(nestedCreds, "project_id", "projectId", "quota_project_id", "quotaProjectId", "project")
	}
	if projectID == "" && extraMap != nil {
		projectID = firstString(extraMap, "project_id", "projectId", "quota_project_id")
	}

	scope := firstString(root, "scope", "scopes")
	if scope == "" && nestedCreds != nil {
		scope = firstString(nestedCreds, "scope", "scopes")
	}

	email := firstString(root, "email", "account_email", "user_email", "userEmail", "active_email")
	if email == "" && nestedCreds != nil {
		email = firstString(nestedCreds, "email", "account_email", "user_email")
	}
	if email == "" && extraMap != nil {
		email = firstString(extraMap, "email", "account_email")
	}
	if email == "" {
		if candidate := firstString(root, "account", "label", "username", "name"); strings.Contains(candidate, "@") {
			email = candidate
		}
	}
	if email == "" && idToken != "" {
		email = extractEmailFromJWT(idToken)
	}
	if email == "" && companionEmail != "" {
		email = companionEmail
	}
	if email == "" {
		email = extractEmailFromFilename(filepath.Base(strings.ReplaceAll(filename, `\`, "/")))
	}

	name := firstString(root, "name", "nickname", "label", "alias")
	if name == "" && extraMap != nil {
		name = firstString(extraMap, "name", "nickname", "label")
	}

	rawProxy := firstString(root, "proxy_url", "proxyUrl", "proxy", "http_proxy", "socks_proxy")
	if rawProxy == "" && nestedCreds != nil {
		rawProxy = firstString(nestedCreds, "proxy_url", "proxyUrl", "proxy")
	}
	if rawProxy == "" && extraMap != nil {
		rawProxy = firstString(extraMap, "proxy_url", "proxy")
	}

	expiresAt, expiryMs, expired := extractAdapterExpiry(root, nestedCreds)
	priority := int(firstInt64(root, "priority", "weight"))

	return ConvertedAGYAccount{
		SourceFormat: detectedFormat,
		SourceFile:   filename,
		Email:        strings.TrimSpace(email),
		Name:         strings.TrimSpace(name),
		RefreshToken: strings.TrimSpace(refreshToken),
		AccessToken:  strings.TrimSpace(accessToken),
		IDToken:      strings.TrimSpace(idToken),
		ClientID:     strings.TrimSpace(clientID),
		ClientSecret: strings.TrimSpace(clientSecret),
		ProjectID:    strings.TrimSpace(projectID),
		Scope:        strings.TrimSpace(scope),
		ExpiresAt:    expiresAt,
		ExpiryDateMs: expiryMs,
		Expired:      expired,
		RawProxy:     strings.TrimSpace(rawProxy),
		Priority:     priority,
		Warnings:     []string{},
	}
}

func classifySingleJSONObjectFormat(filename string, root map[string]any, nestedCreds map[string]any) string {
	lowerBase := strings.ToLower(path.Base(strings.ReplaceAll(filename, `\`, "/")))
	rawType := strings.ToLower(firstString(root, "type", "auth_type"))
	rawProvider := strings.ToLower(firstString(root, "provider", "vendor", "platform", "source"))

	if rawType == "authorized_user" ||
		strings.Contains(lowerBase, "application_default_credentials") ||
		(firstString(root, "quota_project_id", "quotaProjectId") != "" && firstString(root, "client_secret", "clientSecret") != "") {
		return SourceFormatGCloudADC
	}

	if strings.Contains(lowerBase, "oauth_creds") || rawProvider == "gemini-cli" || rawProvider == "gemini_cli" {
		return SourceFormatGeminiCLI
	}

	if nestedCreds != nil ||
		rawProvider == "cliproxyapi" || rawProvider == "cpa" || rawProvider == "sub2api" || rawProvider == "oneapi" || rawProvider == "newapi" ||
		root["credentials"] != nil || root["auth_json"] != nil || root["token_json"] != nil {
		return SourceFormatCPAExport
	}

	// Gemini CLI oauth_creds.json 典型特征：含 expiry_date + token_type/scope 且无 type/provider/project_id
	if _, hasExpDate := root["expiry_date"]; hasExpDate && rawType == "" && rawProvider == "" && firstString(root, "project_id", "projectId") == "" {
		if firstString(root, "token_type", "scope") != "" {
			return SourceFormatGeminiCLI
		}
	}

	return SourceFormatAgyNative
}

func extractNestedMapOrJSONString(m map[string]any, keys ...string) map[string]any {
	if m == nil {
		return nil
	}
	for _, k := range keys {
		val, ok := m[k]
		if !ok || val == nil {
			continue
		}
		switch typed := val.(type) {
		case map[string]any:
			if len(typed) > 0 {
				return typed
			}
		case string:
			trimmed := strings.TrimSpace(typed)
			if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
				var parsed map[string]any
				dec := json.NewDecoder(strings.NewReader(trimmed))
				dec.UseNumber()
				if dec.Decode(&parsed) == nil && len(parsed) > 0 {
					return parsed
				}
			}
		}
	}
	return nil
}

func extractAdapterExpiry(root map[string]any, nested map[string]any) (string, int64, bool) {
	candidates := []any{
		root["expiry_date"],
		root["expiryDate"],
		root["expires_at"],
		root["expiresAt"],
		root["expiry"],
		root["expire"],
		root["expiration"],
	}
	if v, ok := root["expired"]; ok {
		if _, isStr := v.(string); isStr {
			candidates = append(candidates, v)
		}
	}
	if nested != nil {
		candidates = append(candidates,
			nested["expiry_date"],
			nested["expiryDate"],
			nested["expires_at"],
			nested["expiresAt"],
			nested["expiry"],
		)
	}

	for _, c := range candidates {
		if c == nil {
			continue
		}
		if t, ok := parseFlexibleTime(c); ok {
			return t.UTC().Format(time.RFC3339), t.UnixMilli(), time.Now().After(t)
		}
	}

	// 支持仅提供 expires_in 秒数的令牌
	expIn := firstInt64(root, "expires_in", "expiresIn")
	if expIn <= 0 && nested != nil {
		expIn = firstInt64(nested, "expires_in", "expiresIn")
	}
	if expIn > 0 && expIn < 365*24*3600 {
		t := time.Now().UTC().Add(time.Duration(expIn) * time.Second)
		return t.Format(time.RFC3339), t.UnixMilli(), false
	}
	return "", 0, false
}

func looksLikeNDJSONLines(text string) bool {
	nonEmpty := 0
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			continue
		}
		if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
			return false
		}
		nonEmpty++
	}
	return nonEmpty > 0
}

func parseNDJSONOrConcatObjects(filename, text, companionEmail string) ([]ConvertedAGYAccount, error) {
	lines := strings.Split(text, "\n")
	var out []ConvertedAGYAccount

	allLineJSON := true
	nonBlank := 0
	for _, rawLine := range lines {
		s := strings.TrimSpace(rawLine)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			continue
		}
		nonBlank++
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil || m == nil {
			allLineJSON = false
			break
		}
	}

	if allLineJSON && nonBlank > 0 {
		lineNo := 0
		for _, rawLine := range lines {
			lineNo++
			s := strings.TrimSpace(rawLine)
			if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
				continue
			}
			var m map[string]any
			dec := json.NewDecoder(strings.NewReader(s))
			dec.UseNumber()
			if err := dec.Decode(&m); err != nil {
				out = append(out, ConvertedAGYAccount{
					SourceFormat: SourceFormatNDJSON,
					SourceFile:   filename,
					Valid:        false,
					Error:        fmt.Sprintf("第 %d 行 JSON 解析失败: %v", lineNo, err),
					Warnings:     []string{},
				})
				continue
			}
			acc := extractAccountFromObjectMap(filename, m, companionEmail)
			acc.InnerFormat = acc.SourceFormat
			acc.SourceFormat = SourceFormatNDJSON
			out = append(out, acc)
		}
		return out, nil
	}

	// 回退按顶层 `{...}` 配对切分（处理多行格式化 JSON 首尾相接）
	blocks := splitTopLevelJSONObjects(text)
	if len(blocks) == 0 {
		return nil, errors.New("未识别到合法的 NDJSON 或 JSON 对象序列")
	}
	for idx, blk := range blocks {
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(blk))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			out = append(out, ConvertedAGYAccount{
				SourceFormat: SourceFormatNDJSON,
				SourceFile:   filename,
				Valid:        false,
				Error:        fmt.Sprintf("JSON 块 #%d 解析失败: %v", idx+1, err),
				Warnings:     []string{},
			})
			continue
		}
		sub, err := parseSingleOrWrappedJSONMap(filename, m, companionEmail)
		if err != nil {
			continue
		}
		for i := range sub {
			sub[i].InnerFormat = sub[i].SourceFormat
			sub[i].SourceFormat = SourceFormatNDJSON
		}
		out = append(out, sub...)
	}
	return out, nil
}

func splitTopLevelJSONObjects(raw string) []string {
	var out []string
	depth, start := 0, -1
	inString, escaped := false, false
	for i, r := range raw {
		if inString {
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					out = append(out, raw[start:i+1])
					start = -1
				}
			}
		}
	}
	return out
}

// --- CSV / TSV 智能表头与无表头位置推断解析 ---

func looksLikeCSVOrTSV(text string) bool {
	var firstLine string
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		firstLine = s
		break
	}
	if firstLine == "" || strings.HasPrefix(firstLine, "{") || strings.HasPrefix(firstLine, "[") {
		return false
	}
	if strings.Contains(firstLine, "----") || strings.Contains(firstLine, "|") {
		return false
	}
	if strings.Contains(firstLine, "\t") {
		return true
	}
	if !strings.Contains(firstLine, ",") {
		return false
	}
	r := csv.NewReader(strings.NewReader(firstLine))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	cols, err := r.Read()
	if err != nil || len(cols) < 2 {
		return false
	}
	if isCSVHeaderRow(cols) {
		return true
	}
	// 无表头 CSV：存在逗号且包含典型 token (`1//`, `ya29.`) 或邮箱或 >=3 列标准导出
	if len(cols) >= 3 {
		return true
	}
	c0 := strings.TrimSpace(cols[0])
	c1 := strings.TrimSpace(cols[1])
	return emailQuickRe.MatchString(c0) || looksLikeOAuthTokenValue(c0) || looksLikeOAuthTokenValue(c1)
}

func isCSVHeaderRow(cols []string) bool {
	matched := 0
	for _, c := range cols {
		norm := normalizeCSVColName(c)
		switch norm {
		case "email", "mail", "account_email", "user_email", "refresh_token", "refreshtoken", "refresh", "rt",
			"access_token", "accesstoken", "token", "api_key", "apikey", "client_id", "clientid",
			"client_secret", "clientsecret", "project_id", "projectid", "quota_project_id", "proxy", "proxy_url", "name", "label":
			matched++
		}
		// 若某列本身就是真实邮箱或 1// 令牌，则绝非表头
		if emailQuickRe.MatchString(strings.TrimSpace(c)) || strings.HasPrefix(strings.TrimSpace(c), "1//") || strings.HasPrefix(strings.TrimSpace(c), "ya29.") {
			return false
		}
	}
	return matched >= 1 && len(cols) >= 2
}

func normalizeCSVColName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(strings.Trim(raw, `"'`)))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

func parseCSVOrTSVInput(filename, text string) ([]ConvertedAGYAccount, error) {
	sep := ','
	if strings.HasSuffix(strings.ToLower(filename), ".tsv") || (strings.Contains(text, "\t") && !strings.Contains(firstNonCommentLine(text), ",")) {
		sep = '\t'
	}

	var cleanLines []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cleanLines = append(cleanLines, line)
	}
	if len(cleanLines) == 0 {
		return nil, errors.New("CSV/TSV 内容为空")
	}

	reader := csv.NewReader(strings.NewReader(strings.Join(cleanLines, "\n")))
	reader.Comma = sep
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析 CSV/TSV 失败: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("CSV/TSV 未包含任何记录")
	}

	hasHeader := isCSVHeaderRow(records[0])
	var headerMap map[string]int
	startRow := 0
	if hasHeader {
		headerMap = make(map[string]int, len(records[0]))
		for idx, h := range records[0] {
			headerMap[normalizeCSVColName(h)] = idx
		}
		startRow = 1
	}

	if startRow >= len(records) {
		return nil, errors.New("CSV/TSV 仅包含表头，无数据行")
	}

	out := make([]ConvertedAGYAccount, 0, len(records)-startRow)
	for rIdx := startRow; rIdx < len(records); rIdx++ {
		row := records[rIdx]
		if isBlankRow(row) {
			continue
		}
		var acc ConvertedAGYAccount
		if hasHeader {
			acc = mapHeaderedCSVRow(filename, row, headerMap)
		} else {
			acc = mapHeaderlessCSVRow(filename, row)
		}
		acc.SourceFormat = SourceFormatCSVTSV
		out = append(out, acc)
	}
	return out, nil
}

func isBlankRow(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func mapHeaderedCSVRow(filename string, row []string, hdr map[string]int) ConvertedAGYAccount {
	col := func(names ...string) string {
		for _, n := range names {
			if idx, ok := hdr[n]; ok && idx >= 0 && idx < len(row) {
				if v := strings.TrimSpace(row[idx]); v != "" {
					return v
				}
			}
		}
		return ""
	}

	acc := ConvertedAGYAccount{
		SourceFormat: SourceFormatCSVTSV,
		SourceFile:   filename,
		Email:        col("email", "mail", "account_email", "user_email", "account", "user"),
		Name:         col("name", "label", "alias", "nickname"),
		RefreshToken: col("refresh_token", "refreshtoken", "refresh", "rt"),
		AccessToken:  col("access_token", "accesstoken", "api_key", "apikey", "token", "key"),
		ClientID:     col("client_id", "clientid"),
		ClientSecret: col("client_secret", "clientsecret"),
		ProjectID:    col("project_id", "projectid", "quota_project_id", "project"),
		RawProxy:     col("proxy", "proxy_url", "proxyurl", "ip", "http_proxy"),
		Warnings:     []string{},
	}
	if acc.RefreshToken == "" && strings.HasPrefix(acc.AccessToken, "1//") {
		acc.RefreshToken = acc.AccessToken
		acc.AccessToken = ""
	}
	if expRaw := col("expires_at", "expiresat", "expiry", "expiry_date"); expRaw != "" {
		if t, ok := parseFlexibleTime(expRaw); ok {
			acc.ExpiresAt = t.UTC().Format(time.RFC3339)
			acc.ExpiryDateMs = t.UnixMilli()
			acc.Expired = time.Now().After(t)
		}
	}
	return acc
}

// mapHeaderlessCSVRow 按约定顺序 `email,refresh_token,client_id,client_secret,project_id,proxy` 并结合值特征智能校准。
func mapHeaderlessCSVRow(filename string, row []string) ConvertedAGYAccount {
	acc := ConvertedAGYAccount{
		SourceFormat: SourceFormatCSVTSV,
		SourceFile:   filename,
		Warnings:     []string{},
	}
	clean := make([]string, len(row))
	for i, v := range row {
		clean[i] = strings.TrimSpace(v)
	}

	// 标准 6 列映射兜底 + 智能特征识别（支持任意列为 proxy 或无邮箱别名列）
	used := make([]bool, len(clean))

	// 先识别强特征字段（email / 1// refresh_token / ya29. access_token / client_id / GOCSPX- secret / proxy）
	for i, v := range clean {
		if v == "" {
			used[i] = true
			continue
		}
		switch {
		case acc.Email == "" && emailQuickRe.MatchString(v):
			acc.Email = v
			used[i] = true
		case acc.RefreshToken == "" && strings.HasPrefix(v, "1//"):
			acc.RefreshToken = v
			used[i] = true
		case acc.AccessToken == "" && strings.HasPrefix(v, "ya29."):
			acc.AccessToken = v
			used[i] = true
		case acc.ClientID == "" && strings.HasSuffix(strings.ToLower(v), ".apps.googleusercontent.com"):
			acc.ClientID = v
			used[i] = true
		case acc.ClientSecret == "" && strings.HasPrefix(v, "GOCSPX-"):
			acc.ClientSecret = v
			used[i] = true
		case acc.RawProxy == "" && looksLikeProxyField(v):
			acc.RawProxy = v
			used[i] = true
		}
	}

	// 再按标准列位置 `email(0), refresh_token(1), client_id(2), client_secret(3), project_id(4), proxy(5)` 填充未带前缀的值
	for i, v := range clean {
		if used[i] || v == "" {
			continue
		}
		switch i {
		case 0:
			if acc.Email == "" && strings.Contains(v, "@") {
				acc.Email = v
			} else if acc.Name == "" && !looksLikeOAuthTokenValue(v) {
				acc.Name = v
			} else if acc.RefreshToken == "" {
				acc.RefreshToken = v
			}
		case 1:
			if acc.RefreshToken == "" {
				acc.RefreshToken = v
			} else if acc.AccessToken == "" {
				acc.AccessToken = v
			}
		case 2:
			if acc.ClientID == "" {
				acc.ClientID = v
			}
		case 3:
			if acc.ClientSecret == "" {
				acc.ClientSecret = v
			}
		case 4:
			if acc.ProjectID == "" {
				acc.ProjectID = v
			}
		case 5:
			if acc.RawProxy == "" {
				acc.RawProxy = v
			}
		}
	}
	return acc
}

// --- 分隔符文本行 (`----` / `:` / `|` / 裸 `1//...`) 与逐行 Base64 解析 ---

func parseLineDelimitedOrBase64Lines(filename, text, companionEmail string) ([]ConvertedAGYAccount, error) {
	lines := strings.Split(text, "\n")
	var out []ConvertedAGYAccount
	lineNo := 0
	for _, rawLine := range lines {
		lineNo++
		s := strings.TrimSpace(rawLine)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			continue
		}

		// 1. 单行 JSON 对象兼容
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			var m map[string]any
			dec := json.NewDecoder(strings.NewReader(s))
			dec.UseNumber()
			if dec.Decode(&m) == nil && m != nil {
				acc := extractAccountFromObjectMap(filename, m, companionEmail)
				acc.InnerFormat = acc.SourceFormat
				acc.SourceFormat = SourceFormatNDJSON
				out = append(out, acc)
				continue
			}
		}

		// 2. 单行 Base64 编码检测（不以 `1//` 或 `ya29.` 开头、不含 `----`/`:`/`|`/`@`）
		if !strings.HasPrefix(s, "1//") && !strings.HasPrefix(s, "ya29.") &&
			!strings.Contains(s, "----") && !strings.Contains(s, "|") && !strings.Contains(s, "@") {
			if decoded, ok := tryDecodeSingleLineBase64(s); ok {
				sub, sErr := parseDecodedTextOrJSON(filename, decoded, map[string]string{"": companionEmail})
				if sErr == nil && len(sub) > 0 {
					for i := range sub {
						if sub[i].InnerFormat == "" {
							sub[i].InnerFormat = sub[i].SourceFormat
						}
						sub[i].SourceFormat = SourceFormatBase64Blob
					}
					out = append(out, sub...)
					continue
				}
			}
		}

		// 3. 分隔文本行或裸 Token 行
		acc := parseDelimitedLine(filename, s, lineNo)
		out = append(out, acc)
	}

	if len(out) == 0 {
		return nil, errors.New("未包含任何可解析的账号行")
	}
	return out, nil
}

func parseDelimitedLine(filename, line string, lineNo int) ConvertedAGYAccount {
	acc := ConvertedAGYAccount{
		SourceFormat: SourceFormatDelimitedText,
		SourceFile:   filename,
		Warnings:     []string{},
	}

	// Case A: `----` 多段号商格式
	// 支持：
	//   - `email----password----refresh_token`
	//   - `email----refresh_token`
	//   - `email----password----client_id----client_secret----refresh_token----proxy`
	if strings.Contains(line, "----") {
		parts := strings.Split(line, "----")
		classifyDelimitedSegments(&acc, parts, true)
		return acc
	}

	// Case B: `|` 竖线分隔 (`label|refresh_token` 或 `email|refresh_token|proxy`)
	if strings.Contains(line, "|") {
		parts := strings.Split(line, "|")
		classifyDelimitedSegments(&acc, parts, false)
		return acc
	}

	// Case C: 裸 `1//...` OAuth RefreshToken 或裸 `ya29....` AccessToken
	if strings.HasPrefix(line, "1//") && !strings.ContainsAny(line, " \t") {
		acc.RefreshToken = line
		return acc
	}
	if strings.HasPrefix(line, "ya29.") && !strings.ContainsAny(line, " \t") {
		acc.AccessToken = line
		return acc
	}

	// Case D: `email:refresh_token` 或 `label:1//...`（注意避开裸 `http://...` 代理串）
	if colonIdx := strings.IndexByte(line, ':'); colonIdx > 0 && !looksLikeProxyField(line) {
		left := strings.TrimSpace(line[:colonIdx])
		right := strings.TrimSpace(line[colonIdx+1:])
		if right != "" {
			// 支持 `email:password:1//...` 三段式冒号分隔
			if strings.Contains(right, ":1//") {
				idx := strings.Index(right, ":1//")
				if emailQuickRe.MatchString(left) {
					acc.Email = left
				} else {
					acc.Name = left
				}
				acc.RefreshToken = strings.TrimSpace(right[idx+1:])
				return acc
			}
			if emailQuickRe.MatchString(left) {
				acc.Email = left
			} else {
				acc.Name = left
			}
			if strings.HasPrefix(right, "ya29.") {
				acc.AccessToken = right
			} else {
				acc.RefreshToken = right
			}
			return acc
		}
	}

	// Case E: 无分隔符裸令牌（要求具备明确 OAuth/API 令牌特征，避免将普通英文短词误判为密钥）
	if looksLikeBareCredentialToken(line) && !strings.ContainsAny(line, " \t") {
		acc.RefreshToken = line
		acc.Warnings = appendUniqueWarning(acc.Warnings, fmt.Sprintf("第 %d 行使用非标准前缀裸令牌，已按 refresh_token 处理", lineNo))
		return acc
	}

	acc.Valid = false
	acc.Error = fmt.Sprintf("第 %d 行无法识别凭证格式", lineNo)
	return acc
}

func looksLikeBareCredentialToken(s string) bool {
	v := strings.TrimSpace(s)
	for _, pfx := range []string{"1//", "0//", "ya29.", "rt-", "rt_", "eyJ", "oauth_", "tok_"} {
		if strings.HasPrefix(v, pfx) && len(v) >= 10 {
			return true
		}
	}
	if len(v) < 28 {
		return false
	}
	if strings.ContainsAny(v, `[]{}()"',;`) {
		return false
	}
	hasDigit := false
	for _, r := range v {
		if r >= '0' && r <= '9' {
			hasDigit = true
			break
		}
	}
	return hasDigit
}

// classifyDelimitedSegments 智能分类 `----` 或 `|` 切分出的各段：
// 当 allowPasswordSkip=true 且存在 `>=3` 段（如 `email----password----1//...`）时，自动跳过中间账号密码段，精准提取 OAuth RefreshToken。
func classifyDelimitedSegments(acc *ConvertedAGYAccount, rawParts []string, allowPasswordSkip bool) {
	parts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		parts = append(parts, strings.TrimSpace(p))
	}
	used := make([]bool, len(parts))

	// 第一轮：精确提取强特征段（1//、ya29.、email、Google ClientID、GOCSPX ClientSecret、ProxyURL）
	for i, p := range parts {
		if p == "" {
			used[i] = true
			continue
		}
		switch {
		case acc.Email == "" && emailQuickRe.MatchString(p):
			acc.Email = p
			used[i] = true
		case acc.RefreshToken == "" && strings.HasPrefix(p, "1//"):
			acc.RefreshToken = p
			used[i] = true
		case acc.AccessToken == "" && strings.HasPrefix(p, "ya29."):
			acc.AccessToken = p
			used[i] = true
		case acc.ClientID == "" && strings.HasSuffix(strings.ToLower(p), ".apps.googleusercontent.com"):
			acc.ClientID = p
			used[i] = true
		case acc.ClientSecret == "" && strings.HasPrefix(p, "GOCSPX-"):
			acc.ClientSecret = p
			used[i] = true
		case acc.RawProxy == "" && looksLikeProxyField(p):
			acc.RawProxy = p
			used[i] = true
		}
	}

	// 第二轮：处理非 `1//` 开头但合法的自定义 refresh_token 或首段别名 label
	if acc.Email == "" && acc.Name == "" && len(parts) >= 2 && !used[0] && parts[0] != "" {
		acc.Name = parts[0]
		used[0] = true
	}

	if acc.RefreshToken == "" && acc.AccessToken == "" {
		if allowPasswordSkip && len(parts) >= 3 {
			// `email----password----refresh_token`：优先取最后一段非空未使用段作为 refresh_token，跳过 parts[1] 登录密码
			for i := len(parts) - 1; i >= 1; i-- {
				if !used[i] && parts[i] != "" {
					acc.RefreshToken = parts[i]
					used[i] = true
					break
				}
			}
		} else {
			for i := 0; i < len(parts); i++ {
				if !used[i] && parts[i] != "" {
					acc.RefreshToken = parts[i]
					used[i] = true
					break
				}
			}
		}
	}
}

func looksLikeProxyField(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "socks5://") ||
		strings.HasPrefix(lower, "socks5h://")
}

func looksLikeOAuthTokenValue(s string) bool {
	v := strings.TrimSpace(s)
	return strings.HasPrefix(v, "1//") || strings.HasPrefix(v, "ya29.") || strings.HasPrefix(v, "rt-") || strings.HasPrefix(v, "eyJ")
}

func firstNonCommentLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if s != "" && !strings.HasPrefix(s, "#") && !strings.HasPrefix(s, "//") {
			return s
		}
	}
	return ""
}

// --- Base64 嗅探与解码 ---

func tryDecodeWholeBase64Payload(trimmed []byte) ([]byte, bool) {
	if len(trimmed) < 16 {
		return nil, false
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return nil, false
	}
	// 若本身已含明文分隔符或邮箱、明文键值，不是整段 Base64
	if bytes.Contains(trimmed, []byte("----")) || bytes.Contains(trimmed, []byte("@")) ||
		bytes.Contains(trimmed, []byte(",")) || bytes.HasPrefix(trimmed, []byte("1//")) ||
		bytes.HasPrefix(trimmed, []byte("ya29.")) {
		return nil, false
	}

	// 若包含多行：若每一行独立可解为 JSON 对象，则交由逐行 Base64 分支处理，不合并整段
	if bytes.ContainsRune(trimmed, '\n') {
		lines := strings.Split(strings.ReplaceAll(string(trimmed), "\r\n", "\n"), "\n")
		indepDecoded := 0
		nonEmpty := 0
		for _, ln := range lines {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			nonEmpty++
			if dec, ok := tryDecodeSingleLineBase64(ln); ok && len(dec) > 0 && (dec[0] == '{' || bytes.Contains(dec, []byte("1//")) || bytes.Contains(dec, []byte("----"))) {
				indepDecoded++
			}
		}
		if nonEmpty > 1 && indepDecoded == nonEmpty {
			return nil, false
		}
	}

	compact := removeASCIIWhitespace(trimmed)
	decoded, ok := decodeBase64Flexible(compact)
	if !ok || len(decoded) == 0 {
		return nil, false
	}
	if isZipPayload("", decoded) {
		return decoded, true
	}
	decTrim := bytes.TrimSpace(stripUTF8BOM(decoded))
	if len(decTrim) == 0 {
		return nil, false
	}
	if (decTrim[0] == '{' || decTrim[0] == '[') && isPrintableUTF8(decTrim) {
		return decTrim, true
	}
	if isPrintableUTF8(decTrim) && (bytes.Contains(decTrim, []byte("1//")) || bytes.Contains(decTrim, []byte("ya29.")) || bytes.Contains(decTrim, []byte("----")) || bytes.Contains(decTrim, []byte("refresh_token"))) {
		return decTrim, true
	}
	return nil, false
}

func tryDecodeSingleLineBase64(line string) ([]byte, bool) {
	s := strings.TrimSpace(line)
	if len(s) < 16 || !base64CharsetRe.MatchString(s) {
		return nil, false
	}
	decoded, ok := decodeBase64Flexible(s)
	if !ok || len(decoded) == 0 {
		return nil, false
	}
	decTrim := bytes.TrimSpace(stripUTF8BOM(decoded))
	if !isPrintableUTF8(decTrim) {
		return nil, false
	}
	if decTrim[0] == '{' || decTrim[0] == '[' ||
		bytes.Contains(decTrim, []byte("1//")) ||
		bytes.Contains(decTrim, []byte("ya29.")) ||
		bytes.Contains(decTrim, []byte("----")) ||
		bytes.Contains(decTrim, []byte("@")) {
		return decTrim, true
	}
	return nil, false
}

func decodeBase64Flexible(s string) ([]byte, bool) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if b, err := enc.DecodeString(s); err == nil && len(b) > 0 {
			return b, true
		}
	}
	return nil, false
}

func removeASCIIWhitespace(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			continue
		}
		sb.WriteByte(c)
	}
	return sb.String()
}

func isPrintableUTF8(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return true
}

func stripUTF8BOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

// --- 归一化、校验、去重与真实 `agy` CLI 凭证文件生成器 ---

func finalizeAccountFields(acc *ConvertedAGYAccount, defaultProxy string) {
	if acc.Warnings == nil {
		acc.Warnings = []string{}
	}
	if acc.Error != "" && !acc.Valid && acc.RefreshToken == "" && acc.AccessToken == "" {
		return
	}

	acc.Email = strings.ToLower(strings.TrimSpace(acc.Email))
	acc.RefreshToken = strings.TrimSpace(acc.RefreshToken)
	acc.AccessToken = strings.TrimSpace(acc.AccessToken)
	acc.ClientID = strings.TrimSpace(acc.ClientID)
	acc.ClientSecret = strings.TrimSpace(acc.ClientSecret)
	acc.ProjectID = strings.TrimSpace(acc.ProjectID)
	acc.Scope = strings.TrimSpace(acc.Scope)

	if acc.RefreshToken == "" && acc.AccessToken == "" {
		acc.Valid = false
		if acc.Error == "" {
			acc.Error = "缺少 refresh_token 与 access_token，无法生成可用的 agy 凭证"
		}
		return
	}

	acc.Valid = true
	acc.HasRefreshToken = acc.RefreshToken != ""
	acc.HasAccessToken = acc.AccessToken != ""
	acc.HasClientSecret = acc.ClientSecret != ""
	acc.MaskedRefreshToken = maskTokenPreview(acc.RefreshToken)
	acc.MaskedAccessToken = maskTokenPreview(acc.AccessToken)

	if acc.ClientID == "" {
		acc.ClientID = OfficialOAuthClientID
	}
	if acc.Scope == "" {
		acc.Scope = defaultAGYOAuthScope
	}

	if acc.RefreshToken == "" {
		acc.Warnings = appendUniqueWarning(acc.Warnings, "仅包含临时 access_token（缺少 refresh_token），令牌过期后将无法自动刷新")
	}
	if acc.Email == "" {
		acc.Warnings = appendUniqueWarning(acc.Warnings, "源凭证未附带邮箱，将在首次刷新或可通过 google_accounts.json 补全")
	}

	// 校验与规范化代理地址
	rawP := strings.TrimSpace(acc.RawProxy)
	if rawP == "" {
		rawP = defaultProxy
	}
	if rawP != "" && !strings.EqualFold(rawP, "direct") {
		if normP, err := workbuddy.NormalizeProxyURL(rawP); err == nil {
			acc.RawProxy = normP
			acc.Proxy = workbuddy.RedactProxyURL(normP)
		} else {
			acc.Warnings = appendUniqueWarning(acc.Warnings, fmt.Sprintf("忽略不合法的代理字段 %q: %v", rawP, err))
			acc.RawProxy = defaultProxy
			if defaultProxy != "" {
				acc.Proxy = workbuddy.RedactProxyURL(defaultProxy)
			}
		}
	}

	if acc.Name == "" {
		switch {
		case acc.Email != "":
			acc.Name = acc.Email
		case acc.ProjectID != "":
			acc.Name = "agy-" + acc.ProjectID
		default:
			shortHash := shortCredDigest(acc.RefreshToken + "|" + acc.AccessToken)
			acc.Name = "agy-token-" + shortHash
		}
	}
}

func accountDedupKey(acc ConvertedAGYAccount) string {
	if acc.RefreshToken != "" {
		return "rt:" + acc.RefreshToken
	}
	if acc.Email != "" {
		return "em:" + strings.ToLower(acc.Email) + "|" + strings.ToLower(acc.ProjectID)
	}
	if acc.AccessToken != "" {
		return "at:" + acc.AccessToken
	}
	return ""
}

func mergeComplementaryAccountFields(dst *ConvertedAGYAccount, src ConvertedAGYAccount) {
	if dst.Email == "" && src.Email != "" {
		dst.Email = src.Email
		if strings.HasPrefix(dst.Name, "agy-token-") || strings.HasPrefix(dst.Name, "agy-") {
			dst.Name = src.Email
		}
	}
	if (dst.Name == "" || strings.HasPrefix(dst.Name, "agy-token-")) && src.Name != "" && !strings.HasPrefix(src.Name, "agy-token-") {
		dst.Name = src.Name
	}
	if dst.AccessToken == "" && src.AccessToken != "" {
		dst.AccessToken = src.AccessToken
		dst.HasAccessToken = true
		dst.MaskedAccessToken = maskTokenPreview(src.AccessToken)
	}
	if dst.RefreshToken == "" && src.RefreshToken != "" {
		dst.RefreshToken = src.RefreshToken
		dst.HasRefreshToken = true
		dst.MaskedRefreshToken = maskTokenPreview(src.RefreshToken)
	}
	if dst.IDToken == "" && src.IDToken != "" {
		dst.IDToken = src.IDToken
	}
	if (dst.ClientID == "" || dst.ClientID == OfficialOAuthClientID) && src.ClientID != "" && src.ClientID != OfficialOAuthClientID {
		dst.ClientID = src.ClientID
	}
	if dst.ClientSecret == "" && src.ClientSecret != "" {
		dst.ClientSecret = src.ClientSecret
		dst.HasClientSecret = true
	}
	if dst.ProjectID == "" && src.ProjectID != "" {
		dst.ProjectID = src.ProjectID
	}
	if dst.ExpiresAt == "" && src.ExpiresAt != "" {
		dst.ExpiresAt = src.ExpiresAt
		dst.ExpiryDateMs = src.ExpiryDateMs
		dst.Expired = src.Expired
	}
	if dst.RawProxy == "" && src.RawProxy != "" {
		dst.RawProxy = src.RawProxy
		dst.Proxy = src.Proxy
	}
	// 若已补齐邮箱，清理此前的缺失邮箱告警
	if dst.Email != "" && len(dst.Warnings) > 0 {
		filtered := make([]string, 0, len(dst.Warnings))
		for _, w := range dst.Warnings {
			if strings.Contains(w, "未附带邮箱") {
				continue
			}
			filtered = append(filtered, w)
		}
		dst.Warnings = filtered
	}
}

func rebuildAccountAGYFiles(acc *ConvertedAGYAccount, usedFilenames map[string]int, allocateFilename bool) {
	if allocateFilename || acc.AuthFilename == "" {
		baseSlug := sanitizeAuthSlug(firstNonEmptyAdapterStr(acc.Email, acc.Name, acc.ProjectID))
		if baseSlug == "" {
			baseSlug = shortCredDigest(acc.RefreshToken + "|" + acc.AccessToken)
		}
		candidate := "agy-" + baseSlug + ".json"
		if clean, err := SanitizeAuthFilename(candidate); err == nil {
			candidate = clean
		} else {
			candidate = "agy-" + shortCredDigest(baseSlug) + ".json"
		}
		lowerCand := strings.ToLower(candidate)
		if count := usedFilenames[lowerCand]; count > 0 {
			stem := strings.TrimSuffix(candidate, ".json")
			candidate = fmt.Sprintf("%s-%d.json", stem, count+1)
		}
		usedFilenames[lowerCand]++
		acc.AuthFilename = candidate
	}

	expiryMs := acc.ExpiryDateMs
	// 当仅持有 refresh_token 而无当前有效 access_token 时，expiry_date 置 0 使真实 agy CLI 首轮自动触发 OAuth 续期
	if acc.AccessToken == "" && acc.RefreshToken != "" {
		expiryMs = 0
	}

	// 1. `.gemini/oauth_creds.json` — 真实 agy.exe / Gemini CLI 原生读取的主凭证文件
	oauthCreds := map[string]any{
		"access_token":  acc.AccessToken,
		"refresh_token": acc.RefreshToken,
		"token_type":    "Bearer",
		"expiry_date":   expiryMs,
		"scope":         firstNonEmptyAdapterStr(acc.Scope, defaultAGYOAuthScope),
	}
	if acc.IDToken != "" {
		oauthCreds["id_token"] = acc.IDToken
	}
	if acc.ClientID != "" && acc.ClientID != OfficialOAuthClientID {
		oauthCreds["client_id"] = acc.ClientID
	}
	if acc.ClientSecret != "" {
		oauthCreds["client_secret"] = acc.ClientSecret
	}

	// 2. `.gemini/google_accounts.json` — 真实 agy.exe 活跃 Google 账号索引文件
	googleAccounts := map[string]any{
		"active": acc.Email,
		"old":    []string{},
	}

	// 3. `auth.json` — Analy2API / CLIProxyAPI 标准统一凭证文件
	canonicalAuth := buildCanonicalAuthMap(*acc)

	files := map[string]string{
		".gemini/oauth_creds.json":     mustMarshalPrettyJSON(oauthCreds),
		".gemini/google_accounts.json": mustMarshalPrettyJSON(googleAccounts),
		"auth.json":                    mustMarshalPrettyJSON(canonicalAuth),
	}

	// 4. `.config/gcloud/application_default_credentials.json` — 标准 Google ADC 凭证（供无头容器 AGY_ADC_AUTH=1 原生登录直读）
	if acc.RefreshToken != "" {
		effCID := firstNonEmptyAdapterStr(acc.ClientID, OfficialOAuthClientID)
		effCSec := strings.TrimSpace(acc.ClientSecret)
		if effCSec == "" && effCID == OfficialOAuthClientID {
			effCSec = EffectiveOAuthClientSecret()
		}
		adc := map[string]any{
			"type":          "authorized_user",
			"client_id":     effCID,
			"client_secret": effCSec,
			"refresh_token": acc.RefreshToken,
		}
		if acc.ProjectID != "" {
			adc["quota_project_id"] = acc.ProjectID
		}
		files[".config/gcloud/application_default_credentials.json"] = mustMarshalPrettyJSON(adc)
	}

	acc.AGYCLIFiles = files
}

func buildCanonicalAuthMap(acc ConvertedAGYAccount) map[string]any {
	m := map[string]any{
		"type":          "antigravity",
		"provider":      "antigravity",
		"account_type":  "oauth",
		"token_type":    "Bearer",
		"access_token":  acc.AccessToken,
		"refresh_token": acc.RefreshToken,
		"expiry_date":   acc.ExpiryDateMs,
		"scope":         firstNonEmptyAdapterStr(acc.Scope, defaultAGYOAuthScope),
		"source_format": acc.SourceFormat,
	}
	if acc.Email != "" {
		m["email"] = acc.Email
	}
	if acc.Name != "" {
		m["name"] = acc.Name
	}
	if acc.ClientID != "" {
		m["client_id"] = acc.ClientID
	}
	if acc.ClientSecret != "" {
		m["client_secret"] = acc.ClientSecret
	}
	if acc.ProjectID != "" {
		m["project_id"] = acc.ProjectID
	}
	if acc.IDToken != "" {
		m["id_token"] = acc.IDToken
	}
	if acc.ExpiresAt != "" {
		m["expires_at"] = acc.ExpiresAt
		m["expiry"] = acc.ExpiresAt
	}
	if acc.RawProxy != "" {
		m["proxy_url"] = acc.RawProxy
	}
	if acc.Priority > 0 {
		m["priority"] = acc.Priority
	}
	return m
}

func buildAGYBundleJSON(accounts []ConvertedAGYAccount) map[string]any {
	exported := make([]map[string]any, 0, len(accounts))
	for _, acc := range accounts {
		if !acc.Valid {
			continue
		}
		item := buildCanonicalAuthMap(acc)
		item["auth_filename"] = acc.AuthFilename
		item["agy_cli_files"] = acc.AGYCLIFiles
		exported = append(exported, item)
	}
	return map[string]any{
		"format":       "analy2api_agy_bundle_v1",
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"count":        len(exported),
		"accounts":     exported,
	}
}

func buildAGYBundleZipBase64(accounts []ConvertedAGYAccount, bundleJSON map[string]any) (string, error) {
	var valid []ConvertedAGYAccount
	for _, acc := range accounts {
		if acc.Valid {
			valid = append(valid, acc)
		}
	}
	if len(valid) == 0 {
		return "", nil
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	writeEntry := func(entryPath string, data []byte) error {
		hdr := &zip.FileHeader{
			Name:   entryPath,
			Method: zip.Deflate,
		}
		hdr.SetModTime(time.Now().UTC())
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}

	// 单账号时直接在根目录输出 `.gemini/oauth_creds.json` 等，解压到隔离 HOME 即插即用
	if len(valid) == 1 {
		only := valid[0]
		keys := sortedMapKeys(only.AGYCLIFiles)
		for _, k := range keys {
			if err := writeEntry(k, []byte(only.AGYCLIFiles[k])); err != nil {
				return "", err
			}
		}
	}

	// 始终输出按账号隔离目录 `accounts/<01-slug>/...` 与批量 `auths/<auth_filename>`
	for idx, acc := range valid {
		folderSlug := sanitizeAuthSlug(firstNonEmptyAdapterStr(acc.Email, acc.Name))
		if folderSlug == "" {
			folderSlug = fmt.Sprintf("account-%02d", idx+1)
		}
		prefix := fmt.Sprintf("accounts/%02d-%s/", idx+1, folderSlug)
		for _, k := range sortedMapKeys(acc.AGYCLIFiles) {
			if err := writeEntry(prefix+k, []byte(acc.AGYCLIFiles[k])); err != nil {
				return "", err
			}
		}
		if err := writeEntry("auths/"+acc.AuthFilename, acc.CanonicalAuthJSONBytes()); err != nil {
			return "", err
		}
	}

	if bundleJSON != nil {
		if err := writeEntry("bundle.json", []byte(mustMarshalPrettyJSON(bundleJSON))); err != nil {
			return "", err
		}
	}

	if err := zw.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mustMarshalPrettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b) + "\n"
}

func maskTokenPreview(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 10 {
		return s[:2] + "****"
	}
	return s[:6] + "..." + s[len(s)-4:]
}

func sanitizeAuthSlug(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = authSlugCleanRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, ".-_")
	if len(s) > 64 {
		s = strings.Trim(s[:64], ".-_")
	}
	return s
}

func shortCredDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:4])
}

func appendUniqueWarning(list []string, msg string) []string {
	for _, existing := range list {
		if existing == msg {
			return list
		}
	}
	return append(list, msg)
}

func firstNonEmptyAdapterStr(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func inferFallbackFormatFromName(filename string) string {
	lower := strings.ToLower(strings.TrimSpace(filename))
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return SourceFormatZipArchive
	case strings.HasSuffix(lower, ".jsonl"), strings.HasSuffix(lower, ".ndjson"):
		return SourceFormatNDJSON
	case strings.HasSuffix(lower, ".csv"), strings.HasSuffix(lower, ".tsv"):
		return SourceFormatCSVTSV
	case strings.Contains(lower, "application_default_credentials"):
		return SourceFormatGCloudADC
	case strings.Contains(lower, "oauth_creds"):
		return SourceFormatGeminiCLI
	case strings.HasSuffix(lower, ".json"):
		return SourceFormatAgyNative
	default:
		return SourceFormatDelimitedText
	}
}
