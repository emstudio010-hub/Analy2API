package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"cmd2api/internal/netx"
)

const (
	// AntigravityAICodeBaseURL 是官方 AntiGravity 1.5.x 新一代 AI Code 服务根地址（承载 Claude 5.5、Gemini 4 新体验模型及双窗口配额）。
	AntigravityAICodeBaseURL = "https://aicode.googleapis.com"

	// AntigravityAICodeBaseURLEnv 允许在测试或私有网关场景覆盖 aicode 上游地址。
	AntigravityAICodeBaseURLEnv = "AGY_AICODE_BASE_URL"
)

// AICodeCatalogResult 汇总 `aicode.googleapis.com/v1alpha` 返回的订阅层级、双窗口配额组与可用模型体验目录。
type AICodeCatalogResult struct {
	TierID            string
	TierName          string
	QuotaGroups       []QuotaGroupSummary
	ModelBuckets      map[string]QuotaWindowBucket
	Models            []ModelQuotaItem
	ScopeInsufficient bool
}

func effectiveAICodeBaseURL(override string) string {
	if v := strings.TrimRight(strings.TrimSpace(override), "/"); v != "" {
		return v
	}
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv(AntigravityAICodeBaseURLEnv)), "/"); v != "" {
		return v
	}
	return AntigravityAICodeBaseURL
}

func isScopeInsufficientBody(status int, raw []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	s := strings.ToLower(string(raw))
	return strings.Contains(s, "access_token_scope_insufficient") ||
		strings.Contains(s, "insufficient authentication scopes")
}

// ResolveAICodeExperience 将用户传入的模型别名规范化为 `aicode.googleapis.com/v1alpha` 接受的真实 experience 名称。
func ResolveAICodeExperience(rawModel string) string {
	m := strings.TrimSpace(rawModel)
	lower := strings.ToLower(m)
	// 将 claude-opus-5.5 / claude-sonnet-5.5 点号写法归一化为官方连字符写法
	if strings.HasPrefix(lower, "claude-opus-5.5") {
		lower = "claude-opus-5-5" + lower[len("claude-opus-5.5"):]
		m = lower
	} else if strings.HasPrefix(lower, "claude-sonnet-5.5") {
		lower = "claude-sonnet-5-5" + lower[len("claude-sonnet-5.5"):]
		m = lower
	}

	switch lower {
	case "claude-opus-5-5":
		return "claude-opus-5-5-high"
	case "claude-sonnet-5-5":
		return "claude-sonnet-5-5-high"
	case "gemini-4-neon":
		return "gemini-4-neon-high"
	case "gemini-4-carbon":
		return "gemini-4-carbon-high"
	case "gemini-4-argon":
		return "gemini-4-argon-high"
	case "gemini-4-hydrogen":
		return "gemini-4-hydrogen-high"
	case "gemini-3.8-flash", "gemini-3.8-flash-tiered":
		return "gemini-3.8-flash-medium"
	case "gemini-3-flash-preview":
		return "gemini-3-flash"
	case "gemini-3.1-flash-lite-preview":
		return "gemini-3.1-flash-lite"
	case "gemini-3.1-flash-image-preview":
		return "gemini-3.1-flash-image"
	case "gpt-oss-120b":
		return "gpt-oss-120b-medium"
	default:
		return m
	}
}

// IsAICodeExclusiveModel 判断模型是否仅存在于新一代 `aicode.googleapis.com`（旧版 CCPA `v1internal` 完全不支持或已下线返回升级提示）。
func IsAICodeExclusiveModel(rawModel string) bool {
	l := strings.ToLower(strings.TrimSpace(rawModel))
	if l == "gemini-4-hydrogen-sonar" {
		return false
	}
	return strings.HasPrefix(l, "claude-opus-5") ||
		strings.HasPrefix(l, "claude-sonnet-5") ||
		strings.HasPrefix(l, "gemini-4-carbon") ||
		strings.HasPrefix(l, "gemini-4-neon") ||
		strings.HasPrefix(l, "gemini-4-argon") ||
		strings.HasPrefix(l, "gemini-4-hydrogen") ||
		strings.HasPrefix(l, "gemini-pro-orchestrator")
}

// ShouldPreferAICodeForModel 判断给定模型是否应优先走 `aicode.googleapis.com/v1alpha:streamGenerateContent?alt=sse`。
func ShouldPreferAICodeForModel(rawModel string) bool {
	if IsAICodeExclusiveModel(rawModel) {
		return true
	}
	l := strings.ToLower(strings.TrimSpace(rawModel))
	return strings.HasPrefix(l, "gemini-3.8-flash") ||
		strings.HasPrefix(l, "gemini-3.7-flash") ||
		strings.HasPrefix(l, "gemini-3.6-flash") ||
		strings.HasPrefix(l, "gemini-3.5-flash") ||
		strings.HasPrefix(l, "gemini-3.1-flash") ||
		strings.HasPrefix(l, "gemini-3-flash") ||
		strings.HasPrefix(l, "gpt-oss-120b")
}

// FetchAICodeLicenses 调用 `GET /v1alpha:fetchLicenses` 获取账号在 AI Code 侧的真实付费订阅层级（如 `g1-ultra-tier` / `g1-pro-tier`）。
func FetchAICodeLicenses(
	ctx context.Context,
	httpClient *http.Client,
	aicodeBaseURL string,
	accessToken string,
	userAgent string,
) (tierID string, tierName string, scopeInsufficient bool, err error) {
	if httpClient == nil {
		httpClient = netx.NewHTTP2Client(15 * time.Second)
	}
	base := effectiveAICodeBaseURL(aicodeBaseURL)
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		ua = BuildCLIUserAgent("", "", "")
	}
	resp, reqErr := netx.DoWithEOFRetry(ctx, httpClient, func() (*http.Request, error) {
		req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1alpha:fetchLicenses", nil)
		if rErr != nil {
			return nil, rErr
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
		req.Header.Set("User-Agent", ua)
		return req, nil
	}, 2)
	if reqErr != nil {
		return "", "", false, reqErr
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if resp.StatusCode >= http.StatusBadRequest {
		if isScopeInsufficientBody(resp.StatusCode, raw) {
			return "", "", true, fmt.Errorf("aicode scope insufficient")
		}
		return "", "", false, fmt.Errorf("aicode fetchLicenses HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Licenses []struct {
			UserTier        string `json:"userTier"`
			TierDisplayName string `json:"tierDisplayName"`
		} `json:"licenses"`
	}
	if uErr := json.Unmarshal(raw, &parsed); uErr != nil {
		return "", "", false, uErr
	}
	for _, lic := range parsed.Licenses {
		tid := strings.TrimSpace(lic.UserTier)
		if tid == "" {
			continue
		}
		tname := strings.TrimSpace(lic.TierDisplayName)
		if tname == "" {
			switch strings.ToLower(tid) {
			case "g1-ultra-tier":
				tname = "Google AI Ultra"
			case "g1-pro-tier":
				tname = "Google AI Pro"
			}
		}
		return tid, tname, false, nil
	}
	return "", "", false, nil
}

// FetchAICodeCatalogAndQuota 并发调用 `aicode.googleapis.com/v1alpha` 的 `fetchLicenses`、`fetchQuotaStatus` 与 `fetchConfig`，
// 解析真实可用新模型体验（如 Claude Opus/Sonnet 5.5、Gemini 4 新模型、Gemini 3.8 Flash）及 Gemini / 3P 的 5h 与周配额。
func FetchAICodeCatalogAndQuota(
	ctx context.Context,
	httpClient *http.Client,
	aicodeBaseURL string,
	accessToken string,
	userAgent string,
) (*AICodeCatalogResult, error) {
	tok := strings.TrimSpace(accessToken)
	if tok == "" {
		return nil, errors.New("access_token 不能为空")
	}
	if httpClient == nil {
		httpClient = netx.NewHTTP2Client(20 * time.Second)
	}
	base := effectiveAICodeBaseURL(aicodeBaseURL)
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		ua = BuildCLIUserAgent("", "", "")
	}

	doGet := func(endpoint string) ([]byte, int, error) {
		resp, err := netx.DoWithEOFRetry(ctx, httpClient, func() (*http.Request, error) {
			req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, base+endpoint, nil)
			if rErr != nil {
				return nil, rErr
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("User-Agent", ua)
			return req, nil
		}, 2)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return raw, resp.StatusCode, nil
	}

	var (
		wg                        sync.WaitGroup
		licTierID, licTierName    string
		licScopeInsuf             bool
		quotaRaw, configRaw       []byte
		quotaStatus, configStatus int
		quotaErr, configErr       error
	)

	wg.Add(3)
	go func() {
		defer wg.Done()
		licTierID, licTierName, licScopeInsuf, _ = FetchAICodeLicenses(ctx, httpClient, base, tok, ua)
	}()
	go func() {
		defer wg.Done()
		quotaRaw, quotaStatus, quotaErr = doGet("/v1alpha:fetchQuotaStatus")
	}()
	go func() {
		defer wg.Done()
		configRaw, configStatus, configErr = doGet("/v1alpha:fetchConfig")
	}()
	wg.Wait()

	if licScopeInsuf || isScopeInsufficientBody(quotaStatus, quotaRaw) || isScopeInsufficientBody(configStatus, configRaw) {
		return &AICodeCatalogResult{ScopeInsufficient: true}, nil
	}

	out := &AICodeCatalogResult{
		TierID:       licTierID,
		TierName:     licTierName,
		ModelBuckets: make(map[string]QuotaWindowBucket),
	}

	if quotaErr == nil && quotaStatus == http.StatusOK && len(quotaRaw) > 0 {
		var qParsed struct {
			Buckets []struct {
				RemainingFraction any    `json:"remainingFraction"`
				ResetTime         string `json:"resetTime"`
				IsExhausted       bool   `json:"isExhausted"`
				Disabled          bool   `json:"disabled"`
				Model             struct {
					ModelID     string `json:"modelId"`
					DisplayName string `json:"displayName"`
				} `json:"model"`
			} `json:"buckets"`
			QuotaSummary struct {
				Groups []rawQuotaGroupJSON `json:"groups"`
			} `json:"quotaSummary"`
		}
		if json.Unmarshal(quotaRaw, &qParsed) == nil {
			out.QuotaGroups = parseRawQuotaGroups(qParsed.QuotaSummary.Groups, true)
			for _, b := range qParsed.Buckets {
				mid := strings.ToLower(strings.TrimSpace(b.Model.ModelID))
				if mid == "" {
					continue
				}
				wb := QuotaWindowBucket{
					BucketID:    mid,
					Window:      "5h",
					DisplayName: strings.TrimSpace(b.Model.DisplayName),
					ResetTime:   strings.TrimSpace(b.ResetTime),
					Disabled:    b.Disabled,
					Exhausted:   b.IsExhausted,
				}
				if rfVal, hasRF := parseOptionalFloat64(b.RemainingFraction); hasRF {
					rfVal = clamp01(rfVal)
					pct := roundPct1(rfVal)
					wb.RemainingFraction = &rfVal
					wb.RemainingPct = &pct
					wb.Exhausted = wb.Exhausted || rfVal <= 0
				} else if !b.Disabled && wb.ResetTime != "" {
					// Proto3 JSON 在 remainingFraction == 0 时省略字段
					zero := 0.0
					wb.RemainingFraction = &zero
					wb.RemainingPct = &zero
					wb.Exhausted = true
				}
				out.ModelBuckets[mid] = wb
			}
		}
	}

	if configErr != nil {
		if len(out.QuotaGroups) > 0 {
			return out, nil
		}
		return nil, configErr
	}
	if configStatus >= http.StatusBadRequest {
		if len(out.QuotaGroups) > 0 {
			return out, nil
		}
		return nil, fmt.Errorf("aicode fetchConfig HTTP %d: %s", configStatus, strings.TrimSpace(string(configRaw)))
	}

	var cParsed struct {
		AvailableExperiences []struct {
			Name  string `json:"name"`
			Model struct {
				ModelID           string   `json:"modelId"`
				Name              string   `json:"name"`
				DisplayName       string   `json:"displayName"`
				BaseModelID       string   `json:"baseModelId"`
				InputTokenLimit   int      `json:"inputTokenLimit"`
				OutputTokenLimit  int      `json:"outputTokenLimit"`
				SupportedFeatures []string `json:"supportedFeatures"`
				ModelProvider     string   `json:"modelProvider"`
			} `json:"model"`
			SurfaceConfig struct {
				Disabled bool `json:"disabled"`
			} `json:"surfaceConfig"`
		} `json:"availableExperiences"`
	}
	if err := json.Unmarshal(configRaw, &cParsed); err != nil {
		return out, nil
	}

	seen := make(map[string]struct{}, len(cParsed.AvailableExperiences))
	for _, exp := range cParsed.AvailableExperiences {
		if exp.SurfaceConfig.Disabled {
			continue
		}
		expID := strings.TrimSpace(exp.Name)
		if expID == "" {
			continue
		}
		lowerExp := strings.ToLower(expID)
		if strings.HasPrefix(lowerExp, "models/") ||
			strings.Contains(lowerExp, "proactive-observer") ||
			strings.Contains(lowerExp, "embedding") ||
			strings.Contains(lowerExp, "tab_") ||
			strings.Contains(lowerExp, "autocomplete") ||
			lowerExp == "gemini-3.8-flash-tiered" {
			continue
		}
		if _, dup := seen[lowerExp]; dup {
			continue
		}
		seen[lowerExp] = struct{}{}

		vendor := mapAICodeProvider(exp.Model.ModelProvider, expID)
		hasImg := false
		hasThink := false
		hasTool := false
		for _, feat := range exp.Model.SupportedFeatures {
			switch strings.ToUpper(strings.TrimSpace(feat)) {
			case "IMAGE_INPUT":
				hasImg = true
			case "THINKING", "THINKING_LEVEL":
				hasThink = true
			case "FUNCTION_CALLING":
				hasTool = true
			}
		}

		meta := map[string]any{
			"id":                 expID,
			"display_name":       strings.TrimSpace(exp.Model.DisplayName),
			"vendor":             vendor,
			"input_token_limit":  exp.Model.InputTokenLimit,
			"output_token_limit": exp.Model.OutputTokenLimit,
		}
		if len(exp.Model.SupportedFeatures) > 0 {
			meta["supports_images"] = hasImg
			meta["supports_reasoning"] = hasThink
			meta["supports_tool_call"] = hasTool
		}
		inferred, ok := InferModelItem(meta)
		if !ok {
			continue
		}

		qi := ModelQuotaItem{
			ID:                inferred.ID,
			Name:              inferred.Name,
			Vendor:            inferred.Vendor,
			ContextWindow:     inferred.ContextWindow,
			MaxOutputTokens:   inferred.MaxOutputTokens,
			SupportsImages:    inferred.SupportsImages,
			SupportsReasoning: inferred.SupportsReasoning,
			SupportsToolCall:  inferred.SupportsToolCall,
			Source:            "aicode",
		}

		baseModelKey := strings.ToLower(strings.TrimSpace(firstNonEmptyStr(exp.Model.ModelID, exp.Model.BaseModelID)))
		if mb, hasMB := out.ModelBuckets[baseModelKey]; hasMB && mb.RemainingPct != nil {
			pct := *mb.RemainingPct
			qi.RemainingPct = &pct
			if mb.RemainingFraction != nil {
				rf := *mb.RemainingFraction
				qi.RemainingFraction = &rf
			}
			qi.ResetTime = mb.ResetTime
			qi.FiveHourRemainingPct = &pct
			qi.FiveHourResetTime = mb.ResetTime
			qi.Exhausted = mb.Exhausted
		}

		if grp := matchQuotaGroupForModel(qi.ID, qi.Vendor, out.QuotaGroups); grp != nil {
			if grp.FiveHour != nil && !grp.FiveHour.Disabled && grp.FiveHour.RemainingPct != nil {
				v := *grp.FiveHour.RemainingPct
				qi.FiveHourRemainingPct = &v
				if grp.FiveHour.ResetTime != "" {
					qi.FiveHourResetTime = grp.FiveHour.ResetTime
				}
				if qi.RemainingPct == nil {
					qi.RemainingPct = &v
					qi.ResetTime = grp.FiveHour.ResetTime
				}
			}
			if grp.Weekly != nil && !grp.Weekly.Disabled && grp.Weekly.RemainingPct != nil {
				v := *grp.Weekly.RemainingPct
				qi.WeeklyRemainingPct = &v
				qi.WeeklyResetTime = grp.Weekly.ResetTime
				if qi.RemainingPct == nil {
					qi.RemainingPct = &v
					qi.ResetTime = grp.Weekly.ResetTime
				}
			}
		}

		out.Models = append(out.Models, qi)
	}

	return out, nil
}

func mapAICodeProvider(rawProvider, modelID string) string {
	switch strings.ToUpper(strings.TrimSpace(rawProvider)) {
	case "GOOGLE":
		return "google"
	case "ANTHROPIC":
		return "anthropic"
	case "OPENAI":
		return "openai"
	default:
		return inferModelVendor(modelID, rawProvider)
	}
}

// BuildAICodeStreamRequest 将清洗后的 OpenAI 请求转换为 `POST https://aicode.googleapis.com/v1alpha:streamGenerateContent?alt=sse` 请求体：
//   - 使用 `aicode.experience` 绑定官方体验模型（如 `claude-opus-5-5-high`、`claude-sonnet-5-5-medium`、`gemini-4-neon-high`、`gemini-3.8-flash-low`）
//   - 携带 `entitlement.userTier`（如 `g1-ultra-tier` / `g1-pro-tier` / `free-tier`）
//   - 工具调用配置固定使用 `aicode` 支持的 `functionCallingConfig.mode = "AUTO"`（`aicode` 不接受旧版 CCPA 的 `VALIDATED` 枚举）
func BuildAICodeStreamRequest(openaiBody []byte, userTier, modelOverride, sessionID string) ([]byte, string, error) {
	sanitized, err := SanitizeOpenAIPayload(openaiBody)
	if err != nil {
		return nil, "", err
	}
	var reqMap map[string]any
	if err := json.Unmarshal(sanitized, &reqMap); err != nil {
		return nil, "", err
	}

	rawModel := strings.TrimSpace(modelOverride)
	if rawModel == "" {
		rawModel, _ = reqMap["model"].(string)
		rawModel = strings.TrimSpace(rawModel)
	}
	if rawModel == "" {
		rawModel = "gemini-3.8-flash-medium"
	}
	expName := ResolveAICodeExperience(rawModel)
	allowDummyThought := strings.HasPrefix(strings.ToLower(expName), "gemini-")

	var (
		systemTexts   []string
		contents      []map[string]any
		toolIDToName  = make(map[string]string)
		firstUserText string
	)

	if msgs, ok := reqMap["messages"].([]any); ok {
		for _, item := range msgs {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role := strings.ToLower(strings.TrimSpace(fmt.Sprint(m["role"])))
			switch role {
			case "system", "developer":
				if txt := extractMessageText(m["content"]); strings.TrimSpace(txt) != "" {
					systemTexts = append(systemTexts, txt)
				}
			case "user":
				parts := buildUserPartsFromOpenAI(m["content"])
				if firstUserText == "" {
					for _, p := range parts {
						if t, ok := p["text"].(string); ok && strings.TrimSpace(t) != "" {
							firstUserText = t
							break
						}
					}
				}
				if len(parts) > 0 {
					contents = append(contents, map[string]any{
						"role":  "user",
						"parts": parts,
					})
				}
			case "assistant":
				var parts []map[string]any
				if txt := extractMessageText(m["content"]); strings.TrimSpace(txt) != "" {
					parts = append(parts, map[string]any{"text": txt})
				}
				if tcs, ok := m["tool_calls"].([]any); ok {
					for _, tcItem := range tcs {
						tc, ok := tcItem.(map[string]any)
						if !ok {
							continue
						}
						tcID, _ := tc["id"].(string)
						fn, _ := tc["function"].(map[string]any)
						fnName, _ := fn["name"].(string)
						fnName = SanitizeToolName(fnName)
						if tcID != "" && fnName != "" {
							toolIDToName[tcID] = fnName
						}
						fc := map[string]any{
							"name": fnName,
							"args": parseToolArgsMap(fn["arguments"]),
						}
						if tcID != "" {
							fc["id"] = tcID
						}
						part := map[string]any{"functionCall": fc}
						if allowDummyThought {
							part["thoughtSignature"] = DummyThoughtSignature
						}
						parts = append(parts, part)
					}
				}
				if len(parts) > 0 {
					contents = append(contents, map[string]any{
						"role":  "model",
						"parts": parts,
					})
				}
			case "tool":
				tcID, _ := m["tool_call_id"].(string)
				fnName, _ := m["name"].(string)
				if fnName == "" {
					fnName = toolIDToName[tcID]
				}
				if fnName == "" {
					fnName = tcID
				}
				fnName = SanitizeToolName(fnName)
				resultText := extractMessageText(m["content"])
				if strings.TrimSpace(resultText) == "" {
					resultText = "Command executed successfully."
				}
				fr := map[string]any{
					"name": fnName,
					"response": map[string]any{
						"result": resultText,
					},
				}
				if tcID != "" {
					fr["id"] = tcID
				}
				contents = append(contents, map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": fr},
					},
				})
			}
		}
	}

	var sysParts []map[string]any
	hasAGIdentity := false
	for _, st := range systemTexts {
		if strings.Contains(st, "You are Antigravity") {
			hasAGIdentity = true
			break
		}
	}
	if !hasAGIdentity {
		sysParts = append(sysParts,
			map[string]any{"text": CleanAgyNativePromptCore},
			map[string]any{"text": antigravitySilentBoundary},
		)
		for _, st := range systemTexts {
			sysParts = append(sysParts, map[string]any{"text": st})
		}
		sysParts = append(sysParts, map[string]any{"text": "\n--- [SYSTEM_PROMPT_END] ---"})
	} else {
		for _, st := range systemTexts {
			if cleaned := StripMisleadingAgyNativeSections(st); cleaned != "" {
				sysParts = append(sysParts, map[string]any{"text": cleaned})
			}
		}
	}

	genCfg := map[string]any{}
	if v, ok := reqMap["max_tokens"]; ok && v != nil {
		genCfg["maxOutputTokens"] = v
	} else if v, ok := reqMap["max_completion_tokens"]; ok && v != nil {
		genCfg["maxOutputTokens"] = v
	}
	if v, ok := reqMap["temperature"]; ok && v != nil {
		genCfg["temperature"] = v
	}
	if v, ok := reqMap["top_p"]; ok && v != nil {
		genCfg["topP"] = v
	}
	if stop, ok := reqMap["stop"].([]any); ok && len(stop) > 0 {
		genCfg["stopSequences"] = stop
	} else if stopStr, ok := reqMap["stop"].(string); ok && stopStr != "" {
		genCfg["stopSequences"] = []string{stopStr}
	}

	var funcDecls []map[string]any
	if tools, ok := reqMap["tools"].([]any); ok && len(tools) > 0 {
		for _, item := range tools {
			tm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tm["function"].(map[string]any)
			if !ok || fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			name = SanitizeToolName(name)
			decl := map[string]any{"name": name}
			if desc, ok := fn["description"].(string); ok && strings.TrimSpace(desc) != "" {
				decl["description"] = desc
			}
			if params, ok := fn["parameters"].(map[string]any); ok && params != nil {
				decl["parameters"] = CleanJSONSchema(params)
			} else {
				decl["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			funcDecls = append(funcDecls, decl)
		}
	}

	effTier := strings.TrimSpace(userTier)
	if effTier == "" {
		effTier = "free-tier"
	}

	payload := map[string]any{
		"aicode": map[string]any{
			"experience": expName,
		},
		"entitlement": map[string]any{
			"userTier": effTier,
		},
		"sessionId": normalizeV1InternalSessionID(sessionID, firstUserText),
		"contents":  contents,
	}
	if len(sysParts) > 0 {
		payload["systemInstruction"] = map[string]any{
			"role":  "user",
			"parts": sysParts,
		}
	}
	if len(genCfg) > 0 {
		payload["generationConfig"] = genCfg
	}
	if len(funcDecls) > 0 {
		payload["tools"] = []map[string]any{
			{"functionDeclarations": funcDecls},
		}
		payload["toolConfig"] = map[string]any{
			"functionCallingConfig": map[string]any{
				"mode": "AUTO",
			},
		}
	}
	encoded, err := json.Marshal(payload)
	return encoded, expName, err
}

// StreamAICodeGenerateContent 通过 HTTP/2 调用 `POST https://aicode.googleapis.com/v1alpha:streamGenerateContent?alt=sse`，
// 并将返回的 Gemini/Claude 原生 SSE 流转换为标准 OpenAI `chat.completion.chunk` SSE 流。
func StreamAICodeGenerateContent(
	ctx context.Context,
	httpClient *http.Client,
	aicodeBaseURL string,
	accessToken string,
	userTier string,
	userAgent string,
	openaiBody []byte,
	modelOverride string,
	sessionID string,
) (*http.Response, []byte, error) {
	if httpClient == nil {
		httpClient = netx.NewHTTP2Client(0)
	}
	reqBytes, expName, err := BuildAICodeStreamRequest(openaiBody, userTier, modelOverride, sessionID)
	if err != nil {
		return nil, nil, err
	}
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		ua = BuildCLIUserAgent("", "", "")
	}
	targetURL := effectiveAICodeBaseURL(aicodeBaseURL) + "/v1alpha:streamGenerateContent?alt=sse"

	resp, err := netx.DoWithEOFRetry(ctx, httpClient, func() (*http.Request, error) {
		req, rErr := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(reqBytes))
		if rErr != nil {
			return nil, rErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
		req.Header.Set("User-Agent", ua)
		return req, nil
	}, 2)
	if err != nil {
		return nil, reqBytes, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		rawErr, _ := io.ReadAll(io.LimitReader(resp.Body, maxMgmtResponseBytes))
		_ = resp.Body.Close()
		apiErr := parseUpstreamAPIError(resp.StatusCode, rawErr)
		if resp.StatusCode == http.StatusNotFound && IsAICodeExclusiveModel(expName) {
			apiErr.Message = fmt.Sprintf(
				"当前 Google 账号套餐尚未获授模型 %q（Google 当前对个人 OAuth 开放 Claude 5.5 与 Gemini 3.8 系列，Gemini 4 Carbon/Neon 需 Google 实验白名单或企业 GCP Vertex ADC 项目）",
				expName,
			)
		}
		return nil, reqBytes, apiErr
	}

	outModel := strings.TrimSpace(modelOverride)
	if outModel == "" {
		outModel = expName
	}
	outResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       TranslateV1InternalSSEToOpenAISSE(ctx, resp.Body, outModel, ""),
	}
	outResp.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
	outResp.Header.Set("X-Agy-Driver", "container-http2-aicode")
	return outResp, reqBytes, nil
}
