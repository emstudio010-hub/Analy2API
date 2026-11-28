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
	"cmd2api/internal/antigravity"
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
	Proxy          string          `json:"proxy,omitempty"`
	IP             string          `json:"ip,omitempty"`
	ProxyIP        string          `json:"proxy_ip,omitempty"`
	ProxyMode      string          `json:"proxy_mode,omitempty"`
	ProxyTag       string          `json:"proxy_tag,omitempty"`
	ProxyStrategy  string          `json:"proxy_strategy,omitempty"`
	ProxyFetchURL  string          `json:"proxy_fetch_url,omitempty"`
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

// RemotePushAccountsInput 描述一次远程推号请求（支持单条或批量数组 + 默认覆盖参数 + 代理 IP 分配策略）。
type RemotePushAccountsInput struct {
	AdminKeyID           int64
	DefaultPlatform      string
	DefaultGroupIDs      []int64
	DefaultGroupName     string
	DefaultProxyURL      string
	DefaultProxyList     []string
	DefaultProxyMode     string
	DefaultProxyTag      string
	DefaultProxyStrategy string
	DefaultProxyFetchURL string
	DefaultAccountMode   string
	DefaultUpsert        bool
	DefaultAutoProvision bool
	Items                []RemotePushAccountItem
}

// RemotePushAccountItemResult 描述单条远程推号的执行明细（绝不回显明文密钥与代理密码）。
type RemotePushAccountItemResult struct {
	Index           int     `json:"index"`
	OK              bool    `json:"ok"`
	Action          string  `json:"action"` // "created" | "updated" | "failed"
	AccountID       int64   `json:"account_id,omitempty"`
	Name            string  `json:"name,omitempty"`
	Platform        string  `json:"platform,omitempty"`
	GroupIDs        []int64 `json:"group_ids,omitempty"`
	ContainerNodeID int64   `json:"container_node_id,omitempty"`
	AssignedProxy   string  `json:"assigned_proxy,omitempty"`
	ProxySource     string  `json:"proxy_source,omitempty"`
	ProxyTag        string  `json:"proxy_tag,omitempty"`
	ProxyWarning    string  `json:"proxy_warning,omitempty"`
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

// PushAccounts 执行统一远程补号入池（支持四平台单条/批量、AntiGravity 全格式自动识别转换、幂等 upsert、按组名自动建组、独立容器与凭证落盘）。
func (s *AccountService) PushAccounts(ctx context.Context, in RemotePushAccountsInput) (*RemotePushAccountsResult, error) {
	if len(in.Items) == 0 {
		return nil, errors.New("未提供任何待推送的账号条目")
	}

	expandedItems := expandAntigravityPushItems(in.Items, in.DefaultPlatform)

	res := &RemotePushAccountsResult{
		Total:   len(expandedItems),
		Results: make([]RemotePushAccountItemResult, 0, len(expandedItems)),
	}
	inFlightBindings := make(map[string]int)

	for idx, item := range expandedItems {
		itemRes := s.pushSingleAccount(ctx, idx, item, in, inFlightBindings)
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
	inFlightBindings map[string]int,
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
	itemProxyRaw := firstNonEmptyPush(
		item.ProxyURL,
		item.Proxy,
		item.IP,
		item.ProxyIP,
		mapStringField(authMap, "proxy_url", "proxy"),
	)
	reqProxyMode := firstNonEmptyPush(item.ProxyMode, batch.DefaultProxyMode)
	reqProxyTag := firstNonEmptyPush(item.ProxyTag, batch.DefaultProxyTag)
	reqProxyStrategy := firstNonEmptyPush(item.ProxyStrategy, batch.DefaultProxyStrategy)
	reqProxyFetchURL := firstNonEmptyPush(item.ProxyFetchURL, batch.DefaultProxyFetchURL)

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

	// 2.5 AntiGravity 推送载荷全格式自动转换（支持 Google ADC / Gemini CLI oauth_creds / CPA / email----refresh_token / 1// / Base64 等）
	if platform == domain.PlatformAntigravity {
		var convSource []byte
		switch {
		case len(rawAuthBytes) > 0:
			convSource = rawAuthBytes
		case shouldNormalizeAntigravityString(apiKey):
			convSource = []byte(apiKey)
		case shouldNormalizeAntigravityString(refreshToken):
			convSource = []byte(refreshToken)
		}
		if len(convSource) > 0 {
			if normAcc, convErr := antigravity.NormalizeSingleCredential(item.AuthFilename, convSource, itemProxyRaw); convErr == nil && normAcc.Valid {
				if email == "" && normAcc.Email != "" {
					email = normAcc.Email
				}
				if refreshToken == "" && normAcc.RefreshToken != "" {
					refreshToken = normAcc.RefreshToken
				}
				if normAcc.AccessToken != "" && (apiKey == "" || shouldNormalizeAntigravityString(apiKey)) {
					apiKey = normAcc.AccessToken
				} else if shouldNormalizeAntigravityString(apiKey) && normAcc.AccessToken == "" {
					apiKey = ""
				}
				if projectID == "" && normAcc.ProjectID != "" {
					projectID = normAcc.ProjectID
				}
				if item.ExpiresAt == "" && normAcc.ExpiresAt != "" {
					item.ExpiresAt = normAcc.ExpiresAt
				}
				if itemProxyRaw == "" && normAcc.RawProxy != "" {
					itemProxyRaw = normAcc.RawProxy
				}
				if strings.TrimSpace(item.Name) == "" && normAcc.Name != "" && !strings.HasPrefix(normAcc.Name, "agy-token-") {
					item.Name = normAcc.Name
				}
				if strings.TrimSpace(item.AuthFilename) == "" && normAcc.AuthFilename != "" {
					item.AuthFilename = normAcc.AuthFilename
				}
				rawAuthBytes = normAcc.CanonicalAuthJSONBytes()
			}
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
			alloc, allocErr := s.allocatePushProxy(ctx, ProxyAllocationRequest{
				AdminKeyID:        batch.AdminKeyID,
				Platform:          platform,
				ItemProxyRaw:      itemProxyRaw,
				BatchProxyRaw:     batch.DefaultProxyURL,
				BatchProxyList:    batch.DefaultProxyList,
				BatchIndex:        idx,
				RequestMode:       reqProxyMode,
				RequestTag:        reqProxyTag,
				RequestStrategy:   reqProxyStrategy,
				FetchURLOverride:  reqProxyFetchURL,
				InFlightBindings:  inFlightBindings,
				ExistingProxyURL:  existing.ProxyURL,
				AllowEmptyOnError: true,
			})
			if allocErr != nil {
				return RemotePushAccountItemResult{
					Index:    idx,
					OK:       false,
					Action:   "failed",
					Name:     existing.Name,
					Platform: platform,
					Error:    allocErr.Error(),
				}
			}
			proxyURL := alloc.ProxyURL

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
			if alloc.Source == "direct" || alloc.Source == "key_direct" {
				empty := ""
				updIn.ProxyURL = &empty
			} else if proxyURL != "" && alloc.Source != "existing" {
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
				nodeID = s.ensureAntigravityPushContainer(ctx, updated, updated.ProxyURL, item.AuthFilename, rawAuthBytes, email, apiKey, refreshToken, projectID)
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
				AssignedProxy:   MaskProxyURL(updated.ProxyURL),
				ProxySource:     alloc.Source,
				ProxyTag:        alloc.Tag,
				ProxyWarning:    alloc.Warning,
			}
		}
	}

	// 6. 为新建账号分配代理 IP（请求显式指定 -> 批量 proxy_list -> Key 专属策略 -> 全局 IP 池轮询/最少占用/独占/自动远程拉取）
	alloc, allocErr := s.allocatePushProxy(ctx, ProxyAllocationRequest{
		AdminKeyID:        batch.AdminKeyID,
		Platform:          platform,
		ItemProxyRaw:      itemProxyRaw,
		BatchProxyRaw:     batch.DefaultProxyURL,
		BatchProxyList:    batch.DefaultProxyList,
		BatchIndex:        idx,
		RequestMode:       reqProxyMode,
		RequestTag:        reqProxyTag,
		RequestStrategy:   reqProxyStrategy,
		FetchURLOverride:  reqProxyFetchURL,
		InFlightBindings:  inFlightBindings,
		AllowEmptyOnError: true,
	})
	if allocErr != nil {
		return RemotePushAccountItemResult{
			Index:    idx,
			OK:       false,
			Action:   "failed",
			Name:     name,
			Platform: platform,
			Error:    allocErr.Error(),
		}
	}
	proxyURL := alloc.ProxyURL

	// 7. 新建账号入池
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
		AssignedProxy:   MaskProxyURL(created.ProxyURL),
		ProxySource:     alloc.Source,
		ProxyTag:        alloc.Tag,
		ProxyWarning:    alloc.Warning,
	}
}

func (s *AccountService) allocatePushProxy(ctx context.Context, req ProxyAllocationRequest) (ProxyAllocationResult, error) {
	if s.proxyPool != nil {
		return s.proxyPool.AllocateForPush(ctx, req)
	}
	// 无代理池实例时仍完整支持请求体显式传 IP/代理并规范化
	if strings.EqualFold(strings.TrimSpace(req.RequestMode), "direct") {
		return ProxyAllocationResult{Source: "direct"}, nil
	}
	raw := strings.TrimSpace(req.ItemProxyRaw)
	src := "request"
	if raw == "" && len(req.BatchProxyList) > 0 {
		raw = strings.TrimSpace(req.BatchProxyList[req.BatchIndex%len(req.BatchProxyList)])
		src = "request_list"
	}
	if raw == "" {
		raw = strings.TrimSpace(req.BatchProxyRaw)
		src = "request_default"
	}
	if raw == "" {
		if strings.TrimSpace(req.ExistingProxyURL) != "" {
			return ProxyAllocationResult{ProxyURL: req.ExistingProxyURL, MaskedProxyURL: MaskProxyURL(req.ExistingProxyURL), Source: "existing"}, nil
		}
		return ProxyAllocationResult{Source: "direct"}, nil
	}
	norm, err := NormalizeProxyAddress(raw, "http", "", "")
	if err != nil {
		return ProxyAllocationResult{}, fmt.Errorf("代理地址格式不合法 (%q): %w", raw, err)
	}
	return ProxyAllocationResult{ProxyURL: norm, MaskedProxyURL: MaskProxyURL(norm), Source: src}, nil
}

// ReassignAccountProxyInput 描述远程调整指定已有账号代理 IP 的输入。
type ReassignAccountProxyInput struct {
	AdminKeyID       int64
	AccountID        int64
	AccountName      string
	Email            string
	ProxyRaw         string
	ProxyMode        string // "" | "pool" | "fetch" | "direct"
	ProxyTag         string
	ProxyStrategy    string
	FetchURLOverride string
}

// ReassignAccountProxyResult 描述远程更换已有账号代理 IP 的结果。
type ReassignAccountProxyResult struct {
	OK            bool   `json:"ok"`
	AccountID     int64  `json:"account_id"`
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	AssignedProxy string `json:"assigned_proxy"`
	ProxySource   string `json:"proxy_source"`
	ProxyTag      string `json:"proxy_tag,omitempty"`
}

// ReassignAccountProxy 供远程补号 Key / 管理员随时给池中已有账号更换或轮换出口代理 IP（AntiGravity 同步更新容器出口）。
func (s *AccountService) ReassignAccountProxy(ctx context.Context, in ReassignAccountProxyInput) (*ReassignAccountProxyResult, error) {
	var target *ent.Account
	if in.AccountID > 0 {
		row, err := s.client.Account.Query().
			Where(account.IDEQ(in.AccountID), account.DeletedAtIsNil()).
			Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("未找到账号 #%d", in.AccountID)
		}
		target = row
	} else {
		q := s.client.Account.Query().Where(account.DeletedAtIsNil())
		if email := strings.TrimSpace(in.Email); email != "" {
			row, err := q.Clone().Where(account.EmailEqualFold(email)).First(ctx)
			if err == nil {
				target = row
			}
		}
		if target == nil && strings.TrimSpace(in.AccountName) != "" {
			row, err := q.Clone().Where(account.NameEQ(strings.TrimSpace(in.AccountName))).First(ctx)
			if err == nil {
				target = row
			}
		}
		if target == nil {
			return nil, errors.New("未找到匹配的目标账号（请提供有效的 account_id、account_name 或 email）")
		}
	}

	mode := strings.TrimSpace(in.ProxyMode)
	if mode == "" && strings.TrimSpace(in.ProxyRaw) == "" {
		mode = KeyProxyModePool
	}
	alloc, err := s.allocatePushProxy(ctx, ProxyAllocationRequest{
		AdminKeyID:        in.AdminKeyID,
		Platform:          domain.NormalizePlatform(target.Platform),
		ItemProxyRaw:      in.ProxyRaw,
		RequestMode:       mode,
		RequestTag:        in.ProxyTag,
		RequestStrategy:   in.ProxyStrategy,
		FetchURLOverride:  in.FetchURLOverride,
		AllowEmptyOnError: false,
	})
	if err != nil {
		return nil, err
	}

	nextProxy := alloc.ProxyURL
	updated, err := s.Update(ctx, target.ID, UpdateAccountInput{ProxyURL: &nextProxy})
	if err != nil {
		return nil, fmt.Errorf("更新账号代理失败: %w", err)
	}
	// AntiGravity 账号同步更新其绑定的 1:1 容器节点出口代理
	if domain.NormalizePlatform(updated.Platform) == domain.PlatformAntigravity && s.agSvc != nil {
		if nodeID := extractExtraInt64(updated.Extra, "node_id"); nodeID > 0 {
			_, _ = s.agSvc.UpdateNode(ctx, nodeID, UpdateContainerNodeInput{ProxyURL: &nextProxy})
		}
	}
	return &ReassignAccountProxyResult{
		OK:            true,
		AccountID:     updated.ID,
		Name:          updated.Name,
		Platform:      domain.NormalizePlatform(updated.Platform),
		AssignedProxy: MaskProxyURL(updated.ProxyURL),
		ProxySource:   alloc.Source,
		ProxyTag:      alloc.Tag,
	}, nil
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
	if len(authBytes) > 0 {
		if normAcc, nErr := antigravity.NormalizeSingleCredential(authFilename, authBytes, proxyURL); nErr == nil && normAcc.Valid {
			authBytes = normAcc.CanonicalAuthJSONBytes()
			if strings.TrimSpace(authFilename) == "" {
				authFilename = normAcc.AuthFilename
			}
		}
	} else if refreshToken != "" || accessToken != "" {
		cleanAccess := accessToken
		if cleanAccess == "agy-oauth-json" || cleanAccess == refreshToken {
			cleanAccess = ""
		}
		payload := map[string]any{
			"type":         "antigravity",
			"provider":     "antigravity",
			"access_token": cleanAccess,
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
		// 若存在已软删除的同名历史分组，先归档重命名以释放 groups.name 唯一约束，避免 SQLite/Postgres UNIQUE 冲突
		s.archiveSoftDeletedPushGroup(ctx, groupName)
		builder := s.client.Group.Create().
			SetName(groupName).
			SetDescription("远程补号自动创建的 " + platform + " 分组").
			SetPlatform(platform).
			SetPromptInjectMode(domain.PromptModeAppendGlobal).
			SetRateMultiplier(1.0).
			SetStatus(domain.StatusActive)
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
	s.archiveSoftDeletedPushGroup(ctx, defaultName)
	if exists, _ := s.client.Group.Query().Where(group.NameEQ(defaultName)).Exist(ctx); exists {
		defaultName = fmt.Sprintf("%s-%d", platform, time.Now().Unix()%10000)
	}
	builder := s.client.Group.Create().
		SetName(defaultName).
		SetDescription("Analy2API 远程补号默认 " + platform + " 分组").
		SetPlatform(platform).
		SetPromptInjectMode(domain.PromptModeAppendGlobal).
		SetRateMultiplier(1.0).
		SetStatus(domain.StatusActive)
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
	if projectID != "" || hasAuthJSON || strings.HasPrefix(apiKey, "ya29.") || strings.HasPrefix(refreshToken, "1//") ||
		strings.HasPrefix(apiKey, "1//") || strings.Contains(apiKey, "----1//") || strings.Contains(apiKey, ":1//") || strings.Contains(apiKey, "|1//") {
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

func (s *AccountService) archiveSoftDeletedPushGroup(ctx context.Context, groupName string) {
	if strings.TrimSpace(groupName) == "" {
		return
	}
	deletedRows, err := s.client.Group.Query().
		Where(group.NameEQ(groupName), group.DeletedAtNotNil()).
		All(ctx)
	if err != nil {
		return
	}
	for _, r := range deletedRows {
		archived := fmt.Sprintf("%s__deleted_%d", r.Name, time.Now().UnixNano())
		_, _ = r.Update().SetName(archived).Save(ctx)
	}
}

func shouldNormalizeAntigravityString(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "----") || strings.Contains(trimmed, "|") ||
		strings.HasPrefix(trimmed, "1//") || strings.Contains(trimmed, ":1//") ||
		strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return true
	}
	// Base64 编码的 JSON 凭证探测
	if len(trimmed) >= 24 && !strings.HasPrefix(trimmed, "ya29.") && !strings.Contains(trimmed, " ") {
		if norm, err := antigravity.NormalizeSingleCredential("", []byte(trimmed), ""); err == nil && norm.Valid && norm.SourceFormat == antigravity.SourceFormatBase64Blob {
			return true
		}
	}
	return false
}

// expandAntigravityPushItems 当远程推送的条目中包含 CPA/Sub2API 多账号导出 JSON、JSON 数组或 NDJSON 时，自动展开为多条独立账号项。
func expandAntigravityPushItems(items []RemotePushAccountItem, defaultPlatform string) []RemotePushAccountItem {
	out := make([]RemotePushAccountItem, 0, len(items))
	normDefaultPlat := domain.NormalizePlatform(defaultPlatform)

	for _, item := range items {
		effPlat := domain.NormalizePlatform(firstNonEmptyPush(item.Platform, normDefaultPlat))
		rawBlob := firstValidJSONBytes(item.AuthJSON, item.TokenJSON)
		if len(rawBlob) == 0 && (effPlat == domain.PlatformAntigravity || strings.Contains(item.APIKey, "\n") || strings.HasPrefix(strings.TrimSpace(item.APIKey), "[") || strings.HasPrefix(strings.TrimSpace(item.APIKey), "{")) {
			if shouldExpandMultiAccountString(item.APIKey) {
				rawBlob = []byte(item.APIKey)
			}
		}
		if len(rawBlob) == 0 || (effPlat != "" && effPlat != domain.PlatformAntigravity) {
			out = append(out, item)
			continue
		}

		fFalse := false
		batch, err := antigravity.ConvertCredentialsToAGY([]antigravity.AdapterInputFile{
			{Filename: item.AuthFilename, Content: rawBlob},
		}, antigravity.ConvertOptions{
			DefaultProxy: firstNonEmptyPush(item.ProxyURL, item.Proxy, item.IP, item.ProxyIP),
			Deduplicate:  &fFalse,
		})
		if err != nil || batch.ValidCount <= 1 {
			out = append(out, item)
			continue
		}

		for _, conv := range batch.Accounts {
			if !conv.Valid {
				continue
			}
			cloned := item
			cloned.Platform = domain.PlatformAntigravity
			if cloned.Email == "" {
				cloned.Email = conv.Email
			}
			cloned.Name = conv.Name
			cloned.AccessToken = conv.AccessToken
			cloned.RefreshToken = conv.RefreshToken
			cloned.APIKey = firstNonEmptyPush(conv.AccessToken, conv.RefreshToken)
			if cloned.ProjectID == "" {
				cloned.ProjectID = conv.ProjectID
			}
			if cloned.ExpiresAt == "" {
				cloned.ExpiresAt = conv.ExpiresAt
			}
			if cloned.ProxyURL == "" && cloned.Proxy == "" && conv.RawProxy != "" {
				cloned.ProxyURL = conv.RawProxy
			}
			cloned.AuthFilename = conv.AuthFilename
			cloned.AuthJSON = json.RawMessage(conv.CanonicalAuthJSONBytes())
			cloned.TokenJSON = nil
			out = append(out, cloned)
		}
	}
	return out
}

func shouldExpandMultiAccountString(s string) bool {
	trimmed := strings.TrimSpace(s)
	return strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") || strings.Contains(trimmed, "\n")
}
