package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/group"
	"cmd2api/internal/domain"
	"cmd2api/internal/workbuddy"
)

// RemotePushAccountItem 描述远程推送补号接口接收的单条账号载荷（全平台统一，字段高度容错）。
type RemotePushAccountItem struct {
	Platform       string          `json:"platform,omitempty"`
	Name           string          `json:"name,omitempty"`
	Notes          string          `json:"notes,omitempty"`
	APIKey         string          `json:"api_key,omitempty"`
	AccessToken    string          `json:"access_token,omitempty"`
	Token          string          `json:"token,omitempty"`
	RefreshToken   string          `json:"refresh_token,omitempty"`
	Email          string          `json:"email,omitempty"`
	UID            string          `json:"uid,omitempty"`
	Nickname       string          `json:"nickname,omitempty"`
	Domain         string          `json:"domain,omitempty"`
	BaseURL        string          `json:"base_url,omitempty"`
	AccountMode    string          `json:"account_mode,omitempty"`
	ProxyURL       string          `json:"proxy_url,omitempty"`
	Qimei36        string          `json:"qimei36,omitempty"`
	MachineID      string          `json:"machine_id,omitempty"`
	ProjectID      string          `json:"project_id,omitempty"`
	GroupID        int64           `json:"group_id,omitempty"`
	GroupIDs       []int64         `json:"group_ids,omitempty"`
	GroupName      string          `json:"group_name,omitempty"`
	Concurrency    int             `json:"concurrency,omitempty"`
	Priority       int             `json:"priority,omitempty"`
	RateMultiplier float64         `json:"rate_multiplier,omitempty"`
	ExpiresAt      string          `json:"expires_at,omitempty"`
	AuthFilename   string          `json:"auth_filename,omitempty"`
	AuthJSON       json.RawMessage `json:"auth_json,omitempty"`
	TokenJSON      json.RawMessage `json:"token_json,omitempty"`
	AutoProvision  *bool           `json:"auto_provision,omitempty"`
	Upsert         *bool           `json:"upsert,omitempty"`
}

// RemotePushAccountsInput 描述一次远程推号请求（支持单条或批量数组 + 默认覆盖参数）。
type RemotePushAccountsInput struct {
	DefaultPlatform      string
	DefaultGroupIDs      []int64
	DefaultGroupName     string
	DefaultProxyURL      string
	DefaultAccountMode   string
	DefaultUpsert        bool
	DefaultAutoProvision bool
	Items                []RemotePushAccountItem
}

// RemotePushAccountItemResult 描述单条远程推号的执行明细（绝不回显明文密钥）。
type RemotePushAccountItemResult struct {
	Index           int     `json:"index"`
	OK              bool    `json:"ok"`
	Action          string  `json:"action"` // "created" | "updated" | "failed"
	AccountID       int64   `json:"account_id,omitempty"`
	Name            string  `json:"name,omitempty"`
	Platform        string  `json:"platform,omitempty"`
	GroupIDs        []int64 `json:"group_ids,omitempty"`
	ContainerNodeID int64   `json:"container_node_id,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// RemotePushAccountsResult 是远程补号接口的汇总响应。
type RemotePushAccountsResult struct {
	OK      bool                          `json:"ok"`
	Total   int                           `json:"total"`
	Created int                           `json:"created"`
	Updated int                           `json:"updated"`
	Failed  int                           `json:"failed"`
	Results []RemotePushAccountItemResult `json:"results"`
}

// RemotePushGroupBrief 返回可供远程补号端参考的活跃分组摘要。
type RemotePushGroupBrief struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

// RemotePushInfo 返回远程补号接口探测信息。
type RemotePushInfo struct {
	OK                 bool                   `json:"ok"`
	Service            string                 `json:"service"`
	SupportedPlatforms []string               `json:"supported_platforms"`
	Groups             []RemotePushGroupBrief `json:"groups"`
}

// GetRemotePushInfo 返回当前活跃分组与支持平台清单，供远程补号脚本探活与按名推号使用。
func (s *AccountService) GetRemotePushInfo(ctx context.Context) (*RemotePushInfo, error) {
	rows, err := s.client.Group.Query().
		Where(group.DeletedAtIsNil(), group.StatusEQ(domain.StatusActive)).
		Order(ent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询可用分组失败: %w", err)
	}
	groups := make([]RemotePushGroupBrief, 0, len(rows))
	for _, g := range rows {
		groups = append(groups, RemotePushGroupBrief{
			ID:       g.ID,
			Name:     g.Name,
			Platform: domain.NormalizePlatform(g.Platform),
		})
	}
	return &RemotePushInfo{
		OK:      true,
		Service: "Analy2API",
		SupportedPlatforms: []string{
			domain.PlatformAntigravity,
			domain.PlatformWorkBuddy,
			domain.PlatformCommandCode,
			domain.PlatformOpenCode,
		},
		Groups: groups,
	}, nil
}

// PushAccounts 执行统一远程补号入池（支持四平台单条/批量、幂等 upsert、按组名自动建组、AntiGravity 自动分配独立容器与凭证落盘）。
func (s *AccountService) PushAccounts(ctx context.Context, in RemotePushAccountsInput) (*RemotePushAccountsResult, error) {
	if len(in.Items) == 0 {
		return nil, errors.New("未提供任何待推送的账号条目")
	}

	res := &RemotePushAccountsResult{
		Total:   len(in.Items),
		Results: make([]RemotePushAccountItemResult, 0, len(in.Items)),
	}

	for idx, item := range in.Items {
		itemRes := s.pushSingleAccount(ctx, idx, item, in)
		res.Results = append(res.Results, itemRes)
		switch itemRes.Action {
		case "created":
			res.Created++
		case "updated":
			res.Updated++
		default:
			res.Failed++
		}
	}
	res.OK = res.Failed == 0
	return res, nil
}

func (s *AccountService) pushSingleAccount(
	ctx context.Context,
	idx int,
	item RemotePushAccountItem,
	batch RemotePushAccountsInput,
) RemotePushAccountItemResult {
	// 1. 解析附带的 auth_json / token_json（便于直接推送整份 Google/AGY/WorkBuddy OAuth JSON）
	rawAuthBytes := firstValidJSONBytes(item.AuthJSON, item.TokenJSON)
	var authMap map[string]any
	if len(rawAuthBytes) > 0 {
		_ = json.Unmarshal(rawAuthBytes, &authMap)
	}

	apiKey := firstNonEmptyPush(
		item.APIKey,
		item.AccessToken,
		item.Token,
		mapStringField(authMap, "access_token", "api_key", "token"),
	)
	refreshToken := firstNonEmptyPush(
		item.RefreshToken,
		mapStringField(authMap, "refresh_token"),
	)
	email := firstNonEmptyPush(
		item.Email,
		mapStringField(authMap, "email", "account_email"),
	)
	uid := firstNonEmptyPush(
		item.UID,
		mapStringField(authMap, "uid", "user_id", "sub"),
	)
	nickname := firstNonEmptyPush(
		item.Nickname,
		mapStringField(authMap, "nickname", "userName"),
	)
	projectID := firstNonEmptyPush(
		item.ProjectID,
		mapStringField(authMap, "project_id", "projectId"),
	)
	baseURL := firstNonEmptyPush(
		item.BaseURL,
		mapStringField(authMap, "base_url"),
	)
	domainVal := firstNonEmptyPush(
		item.Domain,
		mapStringField(authMap, "domain"),
	)
	accountMode := firstNonEmptyPush(
		item.AccountMode,
		batch.DefaultAccountMode,
		mapStringField(authMap, "account_mode", "mode"),
	)
	proxyURL := firstNonEmptyPush(item.ProxyURL, batch.DefaultProxyURL)

	// 2. 自动识别或归一化目标平台
	rawPlatform := firstNonEmptyPush(item.Platform, batch.DefaultPlatform, mapStringField(authMap, "platform", "provider"))
	platform := detectPushPlatform(rawPlatform, apiKey, refreshToken, uid, projectID, accountMode, baseURL, domainVal, len(rawAuthBytes) > 0)
	if !domain.IsValidPlatform(platform) {
		return RemotePushAccountItemResult{
			Index:  idx,
			OK:     false,
			Action: "failed",
			Error:  fmt.Sprintf("不支持的平台 %q", rawPlatform),
		}
	}

	// AntiGravity 账号若只推送了 refresh_token / auth_json（access_token 留空），允许占位以便入池并挂载到独立 agy 容器
	if apiKey == "" && platform == domain.PlatformAntigravity && (refreshToken != "" || len(rawAuthBytes) > 0) {
		apiKey = firstNonEmptyPush(refreshToken, "agy-oauth-json")
	}
	if apiKey == "" {
		return RemotePushAccountItemResult{
			Index:    idx,
			OK:       false,
			Action:   "failed",
			Platform: platform,
			Error:    "缺少 api_key / access_token / token / auth_json 凭证",
		}
	}

	if platform == domain.PlatformOpenCode {
		if strings.EqualFold(accountMode, domain.AccountModeGo) {
			accountMode = domain.AccountModeGo
		} else {
			accountMode = domain.AccountModeZen
		}
		if baseURL == "" {
			baseURL = domain.DefaultOpenCodeBaseURL(accountMode)
		}
	}

	// 3. 自动生成可读账号名称
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = firstNonEmptyPush(
			email,
			nickname,
			uid,
			fmt.Sprintf("%s-remote-%d-%02d", platform, time.Now().Unix()%100000, idx+1),
		)
	}

	// 4. 解析目标分组（支持 group_ids / group_id / group_name / 自动默认平台分组）
	targetGroupIDs, gErr := s.resolvePushTargetGroups(ctx, platform, item, batch)
	if gErr != nil {
		return RemotePushAccountItemResult{
			Index:    idx,
			OK:       false,
			Action:   "failed",
			Name:     name,
			Platform: platform,
			Error:    gErr.Error(),
		}
	}

	var expiresAt *time.Time
	if expStr := firstNonEmptyPush(item.ExpiresAt, mapStringField(authMap, "expires_at", "expiry")); expStr != "" {
		if parsed, err := time.Parse(time.RFC3339, expStr); err == nil {
			expiresAt = &parsed
		}
	}

	upsert := batch.DefaultUpsert
	if item.Upsert != nil {
		upsert = *item.Upsert
	}
	autoProvision := batch.DefaultAutoProvision
	if item.AutoProvision != nil {
		autoProvision = *item.AutoProvision
	}

	// 5. 幂等查重（优先按 email -> uid -> name -> 相同凭证匹配），命中且 upsert=true 时直接刷新凭证并重新激活
	if upsert {
		if existing := s.findExistingAccountForPush(ctx, platform, email, uid, strings.TrimSpace(item.Name), apiKey); existing != nil {
			activeStatus := domain.StatusActive
			schedTrue := true
			updIn := UpdateAccountInput{
				APIKey:      &apiKey,
				Status:      &activeStatus,
				Schedulable: &schedTrue,
			}
			if refreshToken != "" {
				updIn.RefreshToken = &refreshToken
			}
			if email != "" {
				updIn.Email = &email
			}
			if uid != "" {
				updIn.UID = &uid
			}
			if nickname != "" {
				updIn.Nickname = &nickname
			}
			if proxyURL != "" {
				updIn.ProxyURL = &proxyURL
			}
			if baseURL != "" {
				updIn.BaseURL = &baseURL
			}
			if accountMode != "" {
				updIn.AccountMode = &accountMode
			}
			if strings.TrimSpace(item.Notes) != "" {
				n := strings.TrimSpace(item.Notes)
				updIn.Notes = &n
			}
			if expiresAt != nil {
				updIn.ExpiresAt = expiresAt
			}
			if len(targetGroupIDs) > 0 {
				merged := mergeUniqueGroupIDs(existing.Edges.Groups, targetGroupIDs)
				updIn.GroupIDs = &merged
			}
			updated, uErr := s.Update(ctx, existing.ID, updIn)
			if uErr != nil {
				return RemotePushAccountItemResult{
					Index:    idx,
					OK:       false,
					Action:   "failed",
					Name:     existing.Name,
					Platform: platform,
					Error:    fmt.Sprintf("刷新已有账号 #%d 失败: %v", existing.ID, uErr),
				}
			}

			var nodeID int64
			if platform == domain.PlatformAntigravity && s.agSvc != nil && (autoProvision || len(rawAuthBytes) > 0) {
				nodeID = s.ensureAntigravityPushContainer(ctx, updated, proxyURL, item.AuthFilename, rawAuthBytes, email, apiKey, refreshToken, projectID)
			}
			return RemotePushAccountItemResult{
				Index:           idx,
				OK:              true,
				Action:          "updated",
				AccountID:       updated.ID,
				Name:            updated.Name,
				Platform:        platform,
				GroupIDs:        extractAccountGroupIDs(updated),
				ContainerNodeID: nodeID,
			}
		}
	}

	// 6. 新建账号入池
	qimei, mid := workbuddy.EnsureDevice(item.Qimei36, item.MachineID)
	concurrency := item.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	priority := item.Priority
	if priority <= 0 {
		priority = 50
	}
	schedTrue := true

	created, err := s.Create(ctx, CreateAccountInput{
		Name:           name,
		Notes:          strings.TrimSpace(item.Notes),
		Platform:       platform,
		AccountMode:    accountMode,
		BaseURL:        baseURL,
		APIKey:         apiKey,
		RefreshToken:   refreshToken,
		UID:            uid,
		Nickname:       nickname,
		Email:          email,
		Domain:         domainVal,
		ProxyURL:       proxyURL,
		Qimei36:        qimei,
		MachineID:      mid,
		Concurrency:    concurrency,
		Priority:       priority,
		RateMultiplier: item.RateMultiplier,
		Status:         domain.StatusActive,
		Schedulable:    &schedTrue,
		GroupIDs:       targetGroupIDs,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		return RemotePushAccountItemResult{
			Index:    idx,
			OK:       false,
			Action:   "failed",
			Name:     name,
			Platform: platform,
			Error:    err.Error(),
		}
	}

	var nodeID int64
	if platform == domain.PlatformAntigravity {
		nodeID = extractExtraInt64(created.Extra, "node_id")
		if s.agSvc != nil && (autoProvision || len(rawAuthBytes) > 0) {
			if provisionedID := s.ensureAntigravityPushContainer(ctx, created, proxyURL, item.AuthFilename, rawAuthBytes, email, apiKey, refreshToken, projectID); provisionedID > 0 {
				nodeID = provisionedID
			}
		}
	}

	return RemotePushAccountItemResult{
		Index:           idx,
		OK:              true,
		Action:          "created",
		AccountID:       created.ID,
		Name:            created.Name,
		Platform:        platform,
		GroupIDs:        extractAccountGroupIDs(created),
		ContainerNodeID: nodeID,
	}
}

func (s *AccountService) ensureAntigravityPushContainer(
	ctx context.Context,
	acc *ent.Account,
	proxyURL, authFilename string,
	rawAuthBytes []byte,
	email, accessToken, refreshToken, projectID string,
) int64 {
	if s.agSvc == nil || acc == nil {
		return 0
	}
	authBytes := rawAuthBytes
	if len(authBytes) == 0 && (refreshToken != "" || accessToken != "") {
		payload := map[string]any{
			"type":         "antigravity",
			"provider":     "antigravity",
			"access_token": accessToken,
		}
		if refreshToken != "" {
			payload["refresh_token"] = refreshToken
		}
		if email != "" {
			payload["email"] = email
		}
		if projectID != "" {
			payload["project_id"] = projectID
		}
		authBytes, _ = json.MarshalIndent(payload, "", "  ")
	}
	filename := strings.TrimSpace(authFilename)
	if filename == "" {
		filename = fmt.Sprintf("agy-acc-%d.json", acc.ID)
	}
	nodeView, _, err := s.agSvc.ProvisionAccountContainer(ctx, ProvisionAccountContainerInput{
		AccountID:    acc.ID,
		ProxyURL:     proxyURL,
		AutoStart:    true,
		AuthFilename: filename,
		AuthJSON:     authBytes,
	})
	if err != nil {
		s.logger.Warn("远程推号自动分配 AntiGravity 独立容器失败（账号已入池）", "account_id", acc.ID, "err", err)
		return extractExtraInt64(acc.Extra, "node_id")
	}
	if nodeView != nil {
		return nodeView.ID
	}
	return 0
}

func (s *AccountService) resolvePushTargetGroups(
	ctx context.Context,
	platform string,
	item RemotePushAccountItem,
	batch RemotePushAccountsInput,
) ([]int64, error) {
	var explicitIDs []int64
	if len(item.GroupIDs) > 0 {
		explicitIDs = append(explicitIDs, item.GroupIDs...)
	}
	if item.GroupID > 0 {
		explicitIDs = append(explicitIDs, item.GroupID)
	}
	if len(explicitIDs) == 0 && len(batch.DefaultGroupIDs) > 0 {
		explicitIDs = append(explicitIDs, batch.DefaultGroupIDs...)
	}
	if len(explicitIDs) > 0 {
		ids := uniqueIDs(explicitIDs)
		if err := s.assertGroupsMatchPlatform(ctx, ids, platform); err != nil {
			return nil, err
		}
		return ids, nil
	}

	groupName := firstNonEmptyPush(item.GroupName, batch.DefaultGroupName)
	if groupName != "" {
		existing, err := s.client.Group.Query().
			Where(group.DeletedAtIsNil(), group.NameEQ(groupName)).
			Only(ctx)
		if err == nil && existing != nil {
			if domain.NormalizePlatform(existing.Platform) != platform {
				return nil, fmt.Errorf("%w（分组「%s」平台为 %s，但推送账号平台为 %s）", ErrPlatformMismatch, existing.Name, existing.Platform, platform)
			}
			return []int64{existing.ID}, nil
		}
		builder := s.client.Group.Create().
			SetName(groupName).
			SetDescription("远程补号自动创建的 " + platform + " 分组").
			SetPlatform(platform).
			SetRateMultiplier(1.0).
			SetStatus(domain.StatusActive)
		if platform == domain.PlatformAntigravity {
			builder = builder.SetPromptInjectMode(domain.PromptModeOff)
		}
		created, cErr := builder.Save(ctx)
		if cErr != nil {
			return nil, fmt.Errorf("按名称自动创建分组 %q 失败: %w", groupName, cErr)
		}
		return []int64{created.ID}, nil
	}

	// 未指定分组：自动绑定到该平台首个活跃分组；若尚无该平台分组则自动创建默认分组
	rows, err := s.client.Group.Query().
		Where(group.DeletedAtIsNil(), group.PlatformEQ(platform), group.StatusEQ(domain.StatusActive)).
		Order(ent.Asc(group.FieldID)).
		All(ctx)
	if err == nil && len(rows) > 0 {
		return []int64{rows[0].ID}, nil
	}

	defaultName := platform
	if exists, _ := s.client.Group.Query().Where(group.NameEQ(defaultName)).Exist(ctx); exists {
		defaultName = fmt.Sprintf("%s-%d", platform, time.Now().Unix()%10000)
	}
	builder := s.client.Group.Create().
		SetName(defaultName).
		SetDescription("Analy2API 远程补号默认 " + platform + " 分组").
		SetPlatform(platform).
		SetRateMultiplier(1.0).
		SetStatus(domain.StatusActive)
	if platform == domain.PlatformAntigravity {
		builder = builder.SetPromptInjectMode(domain.PromptModeOff)
	}
	created, cErr := builder.Save(ctx)
	if cErr != nil {
		return nil, fmt.Errorf("自动创建平台默认分组 %q 失败: %w", defaultName, cErr)
	}
	return []int64{created.ID}, nil
}

func (s *AccountService) findExistingAccountForPush(
	ctx context.Context,
	platform, email, uid, explicitName, apiKey string,
) *ent.Account {
	rows, err := s.client.Account.Query().
		Where(account.DeletedAtIsNil(), account.PlatformEQ(platform)).
		WithGroups().
		All(ctx)
	if err != nil || len(rows) == 0 {
		return nil
	}

	if email != "" {
		for _, r := range rows {
			if strings.EqualFold(strings.TrimSpace(r.Email), email) {
				return r
			}
		}
	}
	if uid != "" {
		for _, r := range rows {
			if strings.TrimSpace(r.UID) == uid {
				return r
			}
		}
	}
	if explicitName != "" {
		for _, r := range rows {
			if strings.EqualFold(strings.TrimSpace(r.Name), explicitName) {
				return r
			}
		}
	}
	if apiKey != "" && apiKey != "agy-oauth-json" && s.vault != nil {
		for _, r := range rows {
			if enc, ok := r.Credentials["api_key_encrypted"].(string); ok && enc != "" {
				if plain, dErr := s.vault.Decrypt(enc); dErr == nil && plain == apiKey {
					return r
				}
			}
		}
	}
	return nil
}

func detectPushPlatform(rawPlatform, apiKey, refreshToken, uid, projectID, accountMode, baseURL, domainVal string, hasAuthJSON bool) string {
	if strings.TrimSpace(rawPlatform) != "" {
		return domain.NormalizePlatform(rawPlatform)
	}
	if strings.HasPrefix(apiKey, domain.CommandCodeKeyPrefix) {
		return domain.PlatformCommandCode
	}
	if strings.Contains(strings.ToLower(baseURL), "opencode") ||
		strings.EqualFold(accountMode, domain.AccountModeZen) ||
		strings.EqualFold(accountMode, domain.AccountModeGo) {
		return domain.PlatformOpenCode
	}
	if projectID != "" || hasAuthJSON || strings.HasPrefix(apiKey, "ya29.") || strings.HasPrefix(refreshToken, "1//") {
		return domain.PlatformAntigravity
	}
	if uid != "" || strings.Contains(strings.ToLower(domainVal), "codebuddy") || strings.Contains(strings.ToLower(domainVal), "workbuddy") {
		return domain.PlatformWorkBuddy
	}
	return domain.PlatformWorkBuddy
}

func firstNonEmptyPush(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func firstValidJSONBytes(candidates ...json.RawMessage) []byte {
	for _, raw := range candidates {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" || trimmed == "null" || trimmed == `""` {
			continue
		}
		if strings.HasPrefix(trimmed, `"`) {
			var inner string
			if json.Unmarshal([]byte(trimmed), &inner) == nil && json.Valid([]byte(inner)) {
				return []byte(strings.TrimSpace(inner))
			}
		}
		if json.Valid([]byte(trimmed)) {
			return []byte(trimmed)
		}
	}
	return nil
}

func mapStringField(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func mergeUniqueGroupIDs(existing []*ent.Group, add []int64) []int64 {
	all := make([]int64, 0, len(existing)+len(add))
	for _, g := range existing {
		if g != nil && g.ID > 0 {
			all = append(all, g.ID)
		}
	}
	all = append(all, add...)
	return uniqueIDs(all)
}

func extractAccountGroupIDs(acc *ent.Account) []int64 {
	if acc == nil || len(acc.Edges.Groups) == 0 {
		return []int64{}
	}
	out := make([]int64, 0, len(acc.Edges.Groups))
	for _, g := range acc.Edges.Groups {
		if g != nil && g.ID > 0 {
			out = append(out, g.ID)
		}
	}
	return out
}
