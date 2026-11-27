package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/internal/crypto"
	"cmd2api/internal/domain"
	"cmd2api/internal/workbuddy"
)

// ScanLocalInput 控制本机账号扫描与自动落库行为。
type ScanLocalInput struct {
	GroupIDs               []int64
	AutoAssignDefaultGroup *bool
}

// ScanLocalOutput 对齐前端 ScanLocalResult 结构。
type ScanLocalOutput struct {
	Imported int
	Updated  int
	Sources  []string
	Accounts []*ent.Account
	Errors   []string
}

// BatchImportInput 支持通过文本、JSON、agent-switch 或 workbuddy-sdk 格式批量导入/更新账号。
type BatchImportInput struct {
	Keys        string
	Platform    string
	Domain      string
	ProxyURL    string
	AccountMode string
	BaseURL     string
	GroupIDs    []int64
	Concurrency int
	Priority    int
	Upsert      bool
}

// BatchImportFailure 表示批量导入中的单条失败详情。
type BatchImportFailure struct {
	Line  int    `json:"line"`
	Key   string `json:"key"`
	Error string `json:"error"`
}

// BatchImportOutput 对齐前端 BatchImportResult 结构。
type BatchImportOutput struct {
	Created    int                  `json:"created"`
	Updated    int                  `json:"updated"`
	CreatedIDs []int64              `json:"created_ids"`
	Failed     int                  `json:"failed"`
	Failures   []BatchImportFailure `json:"failures"`
}

// ExportAccountsOutput 对齐前端 ExportAccountsResult 结构。
type ExportAccountsOutput struct {
	ExportedAt string           `json:"exported_at"`
	Count      int              `json:"count"`
	Accounts   []map[string]any `json:"accounts"`
}

// RotateDeviceOutput 对齐前端 RotateDeviceResult 结构。
type RotateDeviceOutput struct {
	OK        bool
	Qimei36   string
	MachineID string
	Account   *ent.Account
}

// TokenRefreshOutput 对齐前端 TokenRefreshResult 结构。
type TokenRefreshOutput struct {
	OK      bool
	Account *ent.Account
	Error   string
}

// HarvestStepInfo 对齐前端 HarvestStep 结构。
type HarvestStepInfo struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Credit int    `json:"credit"`
	Detail string `json:"detail"`
}

// HarvestOutput 表示单账号执行收割后的完整结果。
type HarvestOutput struct {
	OK           bool
	CreditGained int
	TotalCredits *float64
	Steps        []HarvestStepInfo
	Account      *ent.Account
	Error        string
}

// HarvestAllItemOutput 表示批量收割中的单账号摘要。
type HarvestAllItemOutput struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	OK           bool              `json:"ok"`
	CreditGained int               `json:"credit_gained"`
	Steps        []HarvestStepInfo `json:"steps"`
	Error        string            `json:"error,omitempty"`
}

// HarvestAllOutput 表示全池批量收割汇总。
type HarvestAllOutput struct {
	OK           bool
	Total        int
	Succeeded    int
	Failed       int
	CreditGained int
	Items        []HarvestAllItemOutput
	Accounts     []*ent.Account
}

func (s *AccountService) wbClient() *workbuddy.Client {
	if s.relayClient != nil && s.relayClient.WorkBuddy() != nil {
		return s.relayClient.WorkBuddy()
	}
	return workbuddy.DefaultClient()
}

func (s *AccountService) buildCredentialsMap(accessToken, refreshToken string, base map[string]any) (map[string]any, error) {
	creds := make(map[string]any, len(base)+4)
	for k, v := range base {
		creds[k] = v
	}
	if accessToken != "" {
		if s.vault != nil {
			enc, err := s.vault.Encrypt(accessToken)
			if err != nil {
				return nil, fmt.Errorf("加密 AccessToken: %w", err)
			}
			creds["api_key_encrypted"] = enc
			delete(creds, "api_key")
			delete(creds, "access_token")
		} else {
			creds["api_key"] = accessToken
		}
	}
	if refreshToken != "" {
		if s.vault != nil {
			enc, err := s.vault.Encrypt(refreshToken)
			if err != nil {
				return nil, fmt.Errorf("加密 RefreshToken: %w", err)
			}
			creds["refresh_token_encrypted"] = enc
			delete(creds, "refresh_token")
		} else {
			creds["refresh_token"] = refreshToken
		}
	}
	return creds, nil
}

// DecryptRefreshToken 提取账号绑定的 RefreshToken（优先密文解密，回退明文兼容）。
func (s *AccountService) DecryptRefreshToken(acc *ent.Account) string {
	if acc == nil || acc.Credentials == nil {
		return ""
	}
	if enc, ok := acc.Credentials["refresh_token_encrypted"].(string); ok && enc != "" && s.vault != nil {
		if plain, err := s.vault.Decrypt(enc); err == nil {
			return plain
		}
	}
	if raw, ok := acc.Credentials["refresh_token"].(string); ok && raw != "" {
		return raw
	}
	return ""
}

// WorkBuddyCreds 从 Account 实体构造 WorkBuddy 上游调用凭证与固定设备/代理上下文。
func (s *AccountService) WorkBuddyCreds(acc *ent.Account) workbuddy.AccountCreds {
	if acc == nil {
		return workbuddy.AccountCreds{}
	}
	accessToken, _ := s.decryptKey(acc)
	refreshToken := s.DecryptRefreshToken(acc)
	domainVal := strings.TrimSpace(acc.Domain)
	if domainVal == "" && acc.Credentials != nil {
		domainVal, _ = acc.Credentials["domain"].(string)
	}
	if domainVal != "" {
		domainVal = workbuddy.NormalizeDomain(domainVal)
	} else {
		domainVal = domain.WorkBuddyDefaultDomain
	}
	qimei36, machineID := workbuddy.EnsureDevice(acc.Qimei36, acc.MachineID)
	return workbuddy.AccountCreds{
		AccessToken:    accessToken,
		RefreshToken:   refreshToken,
		UID:            strings.TrimSpace(acc.UID),
		Nickname:       strings.TrimSpace(acc.Nickname),
		Email:          strings.TrimSpace(acc.Email),
		EnterpriseID:   strings.TrimSpace(acc.EnterpriseID),
		EnterpriseName: strings.TrimSpace(acc.EnterpriseName),
		Domain:         domainVal,
		ProxyURL:       strings.TrimSpace(acc.ProxyURL),
		Qimei36:        qimei36,
		MachineID:      machineID,
	}
}

// UpdateTokens 实现 relay.TokenUpdater 接口：聊天途中遇 401 续期后将最新凭证加密落库。
func (s *AccountService) UpdateTokens(ctx context.Context, accountID int64, accessToken, refreshToken string) error {
	acc, err := s.getActiveAccount(ctx, accountID)
	if err != nil {
		return err
	}
	prevKey, _ := s.decryptKey(acc)
	creds, err := s.buildCredentialsMap(strings.TrimSpace(accessToken), strings.TrimSpace(refreshToken), acc.Credentials)
	if err != nil {
		return err
	}
	if _, err := s.client.Account.UpdateOneID(accountID).SetCredentials(creds).Save(ctx); err != nil {
		return fmt.Errorf("保存续期凭证失败: %w", err)
	}
	if prevKey != "" && s.relayClient != nil {
		s.relayClient.ForgetKey(prevKey)
	}
	return nil
}

// ScanLocal 扫描本机 WorkBuddy 桌面端 / ~/.agent-switch / ~/.workbuddy-sdk 账号并自动去重导入。
func (s *AccountService) ScanLocal(ctx context.Context, in ScanLocalInput) (*ScanLocalOutput, error) {
	scanRes, err := workbuddy.ScanLocalAccounts()
	if err != nil {
		return nil, fmt.Errorf("扫描本机账号失败: %w", err)
	}

	groupIDs := uniqueIDs(in.GroupIDs)
	if len(groupIDs) > 0 {
		if err := s.assertGroupsMatchPlatform(ctx, groupIDs, domain.PlatformWorkBuddy); err != nil {
			return nil, err
		}
	} else {
		autoAssign := true
		if in.AutoAssignDefaultGroup != nil {
			autoAssign = *in.AutoAssignDefaultGroup
		}
		if autoAssign {
			grp, grpErr := BootstrapDefaultGroup(ctx, s.client, s.logger)
			if grpErr != nil {
				return nil, grpErr
			}
			groupIDs = []int64{grp.ID}
		}
	}

	existing, err := s.client.Account.Query().
		Where(account.DeletedAtIsNil()).
		WithGroups().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询现有账号失败: %w", err)
	}

	out := &ScanLocalOutput{
		Sources:  append([]string{}, scanRes.Sources...),
		Accounts: make([]*ent.Account, 0, len(scanRes.Accounts)),
		Errors:   append([]string{}, scanRes.Errors...),
	}
	if out.Sources == nil {
		out.Sources = []string{}
	}
	if out.Errors == nil {
		out.Errors = []string{}
	}

	for _, item := range scanRes.Accounts {
		if strings.TrimSpace(item.AccessToken) == "" {
			continue
		}
		matched := s.findExistingAccountMatch(existing, item.UID, item.Email, item.AccessToken)
		if matched != nil {
			acc, upErr := s.applyImportedUpdate(ctx, matched, item, "", groupIDs)
			if upErr != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", item.Name, upErr))
				continue
			}
			out.Updated++
			out.Accounts = append(out.Accounts, acc)
			continue
		}

		accName := strings.TrimSpace(item.Name)
		if accName == "" {
			accName = firstNonBlank(item.Email, item.Nickname, item.UID, "workbuddy-local")
		}
		created, cErr := s.Create(ctx, CreateAccountInput{
			Name:           accName,
			Platform:       domain.PlatformWorkBuddy,
			APIKey:         item.AccessToken,
			RefreshToken:   item.RefreshToken,
			UID:            item.UID,
			Nickname:       item.Nickname,
			Email:          item.Email,
			EnterpriseID:   item.EnterpriseID,
			EnterpriseName: item.EnterpriseName,
			Domain:         item.Domain,
			ProxyURL:       item.ProxyURL,
			Qimei36:        item.Qimei36,
			MachineID:      item.MachineID,
			Concurrency:    3,
			Priority:       50,
			GroupIDs:       groupIDs,
		})
		if cErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", accName, cErr))
			continue
		}
		existing = append(existing, created)
		out.Imported++
		out.Accounts = append(out.Accounts, created)
	}

	return out, nil
}

// BatchImport 批量导入多种格式（JSON / agent-switch / workbuddy-sdk / 多行文本）的 WorkBuddy 或兼容账号。
func (s *AccountService) BatchImport(ctx context.Context, in BatchImportInput) (*BatchImportOutput, error) {
	platform := domain.NormalizePlatform(in.Platform)
	if !domain.IsValidPlatform(platform) {
		return nil, ErrInvalidPlatform
	}

	presetProxy := strings.TrimSpace(in.ProxyURL)
	if presetProxy != "" {
		norm, err := workbuddy.NormalizeProxyURL(presetProxy)
		if err != nil {
			return nil, fmt.Errorf("全局代理地址无效: %w", err)
		}
		presetProxy = norm
	}

	groupIDs := uniqueIDs(in.GroupIDs)
	if len(groupIDs) > 0 {
		if err := s.assertGroupsMatchPlatform(ctx, groupIDs, platform); err != nil {
			return nil, err
		}
	}

	items, err := workbuddy.ParseAccountsImport(in.Keys)
	if err != nil {
		return nil, fmt.Errorf("解析批量导入内容失败: %w", err)
	}

	var existing []*ent.Account
	if in.Upsert {
		existing, err = s.client.Account.Query().
			Where(account.DeletedAtIsNil(), account.PlatformEQ(platform)).
			WithGroups().
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("查询现有账号失败: %w", err)
		}
	}

	concurrency := in.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	priority := in.Priority
	if priority <= 0 {
		priority = 50
	}

	out := &BatchImportOutput{
		CreatedIDs: []int64{},
		Failures:   []BatchImportFailure{},
	}

	for idx, item := range items {
		lineNum := idx + 1
		masked := crypto.MaskKey(item.AccessToken)
		itemProxy := strings.TrimSpace(item.ProxyURL)
		if itemProxy == "" {
			itemProxy = presetProxy
		} else {
			norm, pErr := workbuddy.NormalizeProxyURL(itemProxy)
			if pErr != nil {
				out.Failed++
				out.Failures = append(out.Failures, BatchImportFailure{
					Line:  lineNum,
					Key:   masked,
					Error: fmt.Sprintf("代理格式错误: %v", pErr),
				})
				continue
			}
			itemProxy = norm
		}

		itemDomain := strings.TrimSpace(item.Domain)
		if strings.TrimSpace(in.Domain) != "" {
			itemDomain = strings.TrimSpace(in.Domain)
		}
		if itemDomain != "" {
			itemDomain = workbuddy.NormalizeDomain(itemDomain)
		} else {
			itemDomain = domain.WorkBuddyDefaultDomain
		}

		if in.Upsert {
			if matched := s.findExistingAccountMatch(existing, item.UID, "", item.AccessToken); matched != nil {
				itemCopy := item
				itemCopy.Domain = itemDomain
				itemCopy.ProxyURL = itemProxy
				updated, upErr := s.applyImportedUpdate(ctx, matched, itemCopy, itemProxy, groupIDs)
				if upErr != nil {
					out.Failed++
					out.Failures = append(out.Failures, BatchImportFailure{
						Line:  lineNum,
						Key:   masked,
						Error: upErr.Error(),
					})
					continue
				}
				*matched = *updated
				out.Updated++
				continue
			}
		}

		accName := strings.TrimSpace(item.Name)
		if accName == "" {
			prefix := "wb"
			if platform != domain.PlatformWorkBuddy {
				prefix = platform
			}
			accName = firstNonBlank(item.Email, item.Nickname, item.UID, fmt.Sprintf("%s-%d", prefix, lineNum))
		}
		created, cErr := s.Create(ctx, CreateAccountInput{
			Name:           accName,
			Platform:       platform,
			AccountMode:    in.AccountMode,
			BaseURL:        in.BaseURL,
			APIKey:         item.AccessToken,
			RefreshToken:   item.RefreshToken,
			UID:            item.UID,
			Nickname:       item.Nickname,
			Email:          item.Email,
			EnterpriseID:   item.EnterpriseID,
			EnterpriseName: item.EnterpriseName,
			Domain:         itemDomain,
			ProxyURL:       itemProxy,
			Qimei36:        item.Qimei36,
			MachineID:      item.MachineID,
			Concurrency:    concurrency,
			Priority:       priority,
			GroupIDs:       groupIDs,
		})
		if cErr != nil {
			out.Failed++
			out.Failures = append(out.Failures, BatchImportFailure{
				Line:  lineNum,
				Key:   masked,
				Error: cErr.Error(),
			})
			continue
		}
		out.Created++
		out.CreatedIDs = append(out.CreatedIDs, created.ID)
		if in.Upsert {
			existing = append(existing, created)
		}
	}

	return out, nil
}

func (s *AccountService) findExistingAccountMatch(existing []*ent.Account, uid, email, accessToken string) *ent.Account {
	uid = strings.TrimSpace(uid)
	email = strings.TrimSpace(email)
	accessToken = strings.TrimSpace(accessToken)
	for _, acc := range existing {
		if acc == nil {
			continue
		}
		if uid != "" && strings.EqualFold(strings.TrimSpace(acc.UID), uid) {
			return acc
		}
		if accessToken != "" {
			if k, ok := s.decryptKey(acc); ok && k == accessToken {
				return acc
			}
		}
		if email != "" && strings.EqualFold(strings.TrimSpace(acc.Email), email) {
			return acc
		}
	}
	return nil
}

func (s *AccountService) applyImportedUpdate(
	ctx context.Context,
	acc *ent.Account,
	item workbuddy.ImportedAccount,
	presetProxy string,
	groupIDs []int64,
) (*ent.Account, error) {
	in := UpdateAccountInput{}
	if strings.TrimSpace(item.AccessToken) != "" {
		tok := strings.TrimSpace(item.AccessToken)
		in.APIKey = &tok
	}
	if strings.TrimSpace(item.RefreshToken) != "" {
		rt := strings.TrimSpace(item.RefreshToken)
		in.RefreshToken = &rt
	}
	if strings.TrimSpace(item.UID) != "" {
		v := strings.TrimSpace(item.UID)
		in.UID = &v
	}
	if strings.TrimSpace(item.Nickname) != "" {
		v := strings.TrimSpace(item.Nickname)
		in.Nickname = &v
	}
	if strings.TrimSpace(item.Email) != "" {
		v := strings.TrimSpace(item.Email)
		in.Email = &v
	}
	if strings.TrimSpace(item.EnterpriseID) != "" {
		v := strings.TrimSpace(item.EnterpriseID)
		in.EnterpriseID = &v
	}
	if strings.TrimSpace(item.EnterpriseName) != "" {
		v := strings.TrimSpace(item.EnterpriseName)
		in.EnterpriseName = &v
	}
	if strings.TrimSpace(item.Domain) != "" {
		v := workbuddy.NormalizeDomain(item.Domain)
		in.Domain = &v
	}
	if p := firstNonBlank(strings.TrimSpace(item.ProxyURL), strings.TrimSpace(presetProxy)); p != "" {
		in.ProxyURL = &p
	}
	if workbuddy.IsValidQimei36(strings.TrimSpace(item.Qimei36)) {
		v := strings.TrimSpace(item.Qimei36)
		in.Qimei36 = &v
	}
	if workbuddy.IsValidMachineID(strings.TrimSpace(item.MachineID)) {
		v := strings.TrimSpace(item.MachineID)
		in.MachineID = &v
	}
	if len(groupIDs) > 0 && len(acc.Edges.Groups) == 0 {
		gCopy := append([]int64(nil), groupIDs...)
		in.GroupIDs = &gCopy
	}
	return s.Update(ctx, acc.ID, in)
}

// ExportAccounts 导出全部未删除账号（含明文凭证、代理与设备指纹，方便迁移或二次导入）。
func (s *AccountService) ExportAccounts(ctx context.Context) (*ExportAccountsOutput, error) {
	rows, err := s.client.Account.Query().
		Where(account.DeletedAtIsNil()).
		WithGroups().
		Order(ent.Asc(account.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("导出账号失败: %w", err)
	}

	items := make([]map[string]any, 0, len(rows))
	for _, acc := range rows {
		accessToken, _ := s.decryptKey(acc)
		refreshToken := s.DecryptRefreshToken(acc)
		groupIDs := make([]int64, 0, len(acc.Edges.Groups))
		for _, g := range acc.Edges.Groups {
			if g.DeletedAt == nil {
				groupIDs = append(groupIDs, g.ID)
			}
		}
		qimei, machine := workbuddy.EnsureDevice(acc.Qimei36, acc.MachineID)
		dom := acc.Domain
		if strings.TrimSpace(dom) == "" {
			dom = domain.WorkBuddyDefaultDomain
		}
		items = append(items, map[string]any{
			"id":              acc.ID,
			"name":            acc.Name,
			"platform":        domain.NormalizePlatform(acc.Platform),
			"uid":             acc.UID,
			"nickname":        acc.Nickname,
			"email":           acc.Email,
			"enterprise_id":   acc.EnterpriseID,
			"enterprise_name": acc.EnterpriseName,
			"domain":          dom,
			"proxy_url":       acc.ProxyURL,
			"qimei36":         qimei,
			"machine_id":      machine,
			"access_token":    accessToken,
			"refresh_token":   refreshToken,
			"concurrency":     acc.Concurrency,
			"priority":        acc.Priority,
			"rate_multiplier": acc.RateMultiplier,
			"status":          acc.Status,
			"group_ids":       groupIDs,
		})
	}

	return &ExportAccountsOutput{
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Count:      len(items),
		Accounts:   items,
	}, nil
}

// TestAccountProxy 测试账号独立代理连通性与出口 IP（支持传入临时 proxy_url 预先测试）。
func (s *AccountService) TestAccountProxy(ctx context.Context, id int64, overrideProxyURL *string) (*workbuddy.ProxyTestResult, error) {
	acc, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	proxyURL := strings.TrimSpace(acc.ProxyURL)
	if overrideProxyURL != nil {
		proxyURL = workbuddy.PreserveRedactedProxyPassword(strings.TrimSpace(*overrideProxyURL), acc.ProxyURL)
	}
	if proxyURL != "" {
		norm, pErr := workbuddy.NormalizeProxyURL(proxyURL)
		if pErr != nil {
			return &workbuddy.ProxyTestResult{
				OK:       false,
				ProxyURL: proxyURL,
				Error:    fmt.Sprintf("代理格式无效: %v", pErr),
			}, nil
		}
		proxyURL = norm
	}
	return s.wbClient().ProxyPool().TestProxy(ctx, proxyURL), nil
}

// RotateDevice 为账号重新签发一对新的 qimei36 + machineId 并持久化。
func (s *AccountService) RotateDevice(ctx context.Context, id int64) (*RotateDeviceOutput, error) {
	if _, err := s.getActiveAccount(ctx, id); err != nil {
		return nil, err
	}
	qimei36, machineID := workbuddy.GenerateDevice()
	updated, err := s.client.Account.UpdateOneID(id).
		SetQimei36(qimei36).
		SetMachineID(machineID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("轮换设备指纹失败: %w", err)
	}
	updated = s.reloadWithGroups(ctx, updated)
	return &RotateDeviceOutput{
		OK:        true,
		Qimei36:   qimei36,
		MachineID: machineID,
		Account:   updated,
	}, nil
}

// IssueLoginLink 通过账号专属代理为账号签发 300s 一次性免密官网登录链接。
func (s *AccountService) IssueLoginLink(ctx context.Context, id int64, target string) (*workbuddy.DeviceAuthResult, error) {
	acc, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	creds := s.WorkBuddyCreds(acc)
	return s.wbClient().IssueLoginLink(ctx, creds, target)
}

// RefreshToken 主动刷新单个 WorkBuddy 账号的 AccessToken / RefreshToken 并写回数据库（按账号加锁防并发续期冲撞）。
func (s *AccountService) RefreshToken(ctx context.Context, id int64) (*TokenRefreshOutput, error) {
	mu := s.accountRefreshMutex(id)
	mu.Lock()
	defer mu.Unlock()

	acc, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	creds := s.WorkBuddyCreds(acc)
	res, err := s.wbClient().RefreshToken(ctx, creds)
	if err != nil {
		return &TokenRefreshOutput{
			OK:    false,
			Error: err.Error(),
		}, nil
	}

	if err := s.UpdateTokens(ctx, id, res.AccessToken, res.RefreshToken); err != nil {
		return &TokenRefreshOutput{
			OK:    false,
			Error: err.Error(),
		}, nil
	}
	if strings.TrimSpace(res.Domain) != "" && res.Domain != acc.Domain {
		_, _ = s.client.Account.UpdateOneID(id).SetDomain(workbuddy.NormalizeDomain(res.Domain)).Save(ctx)
	}

	reloaded, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return &TokenRefreshOutput{OK: true}, nil
	}
	reloaded = s.reloadWithGroups(ctx, reloaded)
	return &TokenRefreshOutput{
		OK:      true,
		Account: reloaded,
	}, nil
}

// HarvestAccount 对单个账号执行完整签到+激励任务收割，更新积分与签到状态快照。
func (s *AccountService) HarvestAccount(ctx context.Context, id int64, doChat bool) (*HarvestOutput, error) {
	acc, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	creds := s.WorkBuddyCreds(acc)
	hr, hErr := s.wbClient().Harvest(ctx, creds, doChat)
	s.applyHarvestResult(ctx, acc, hr, hErr)

	// 收割完成后尽最大努力刷新一次积分余额快照。
	_, _ = s.RefreshBalance(ctx, id)

	reloaded, rErr := s.getActiveAccount(ctx, id)
	if rErr == nil {
		acc = s.reloadWithGroups(ctx, reloaded)
	} else {
		acc = s.reloadWithGroups(ctx, acc)
	}

	out := &HarvestOutput{
		Account:      acc,
		TotalCredits: acc.BalanceTotalCredits,
		Steps:        []HarvestStepInfo{},
	}
	if hr != nil {
		out.OK = hr.OK && hErr == nil
		out.CreditGained = hr.CreditGained
		out.Steps = convertHarvestSteps(hr.Steps)
		out.Error = hr.Error
	}
	if hErr != nil && out.Error == "" {
		out.OK = false
		out.Error = hErr.Error()
	}
	return out, nil
}

func (s *AccountService) applyHarvestResult(ctx context.Context, acc *ent.Account, hr *workbuddy.HarvestResult, hErr error) {
	now := time.Now()
	summary := map[string]any{}
	stepsInfo := []HarvestStepInfo{}
	creditGained := 0
	errText := ""

	if hr != nil {
		creditGained = hr.CreditGained
		stepsInfo = convertHarvestSteps(hr.Steps)
		errText = strings.TrimSpace(hr.Error)
	}
	if hErr != nil && errText == "" {
		errText = hErr.Error()
	}

	summary["credit_gained"] = creditGained
	summary["steps"] = stepsInfo
	if errText != "" {
		summary["error"] = errText
	}

	builder := s.client.Account.UpdateOneID(acc.ID).
		SetLastHarvestAt(now).
		SetLastHarvestSummary(summary)
	if hr != nil {
		if strings.TrimSpace(hr.CheckedInDate) != "" {
			builder = builder.SetLastCheckinDate(strings.TrimSpace(hr.CheckedInDate))
		}
		if hr.Streak > 0 {
			builder = builder.SetCheckinStreak(hr.Streak)
		}
	}
	if _, err := builder.Save(ctx); err != nil {
		s.logger.Warn("保存账号收割结果失败", "err", err, "account_id", acc.ID)
	}
}

const harvestAllConcurrency = 4

// HarvestAllAccounts 对全部未停用的 WorkBuddy 账号并发执行一次全自动签到与积分收割。
func (s *AccountService) HarvestAllAccounts(ctx context.Context, doChat bool) (*HarvestAllOutput, error) {
	rows, err := s.client.Account.Query().
		Where(
			account.DeletedAtIsNil(),
			account.StatusNEQ(domain.StatusDisabled),
		).
		Order(ent.Asc(account.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询收割账号列表失败: %w", err)
	}

	targets := make([]*ent.Account, 0, len(rows))
	for _, r := range rows {
		if domain.NormalizePlatform(r.Platform) == domain.PlatformWorkBuddy {
			targets = append(targets, r)
		}
	}

	items := make([]HarvestAllItemOutput, len(targets))
	updatedAccounts := make([]*ent.Account, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, harvestAllConcurrency)

	for i, acc := range targets {
		wg.Add(1)
		go func(idx int, target *ent.Account) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				items[idx] = HarvestAllItemOutput{
					ID:    target.ID,
					Name:  target.Name,
					OK:    false,
					Error: ctx.Err().Error(),
				}
				return
			}
			defer func() { <-sem }()

			res, hErr := s.HarvestAccount(ctx, target.ID, doChat)
			if hErr != nil {
				items[idx] = HarvestAllItemOutput{
					ID:    target.ID,
					Name:  target.Name,
					OK:    false,
					Error: hErr.Error(),
				}
				return
			}
			items[idx] = HarvestAllItemOutput{
				ID:           target.ID,
				Name:         target.Name,
				OK:           res.OK,
				CreditGained: res.CreditGained,
				Steps:        res.Steps,
				Error:        res.Error,
			}
			updatedAccounts[idx] = res.Account
		}(i, acc)
	}
	wg.Wait()

	out := &HarvestAllOutput{
		OK:       true,
		Total:    len(targets),
		Items:    items,
		Accounts: make([]*ent.Account, 0, len(targets)),
	}
	for i, item := range items {
		if item.OK {
			out.Succeeded++
		} else {
			out.Failed++
		}
		out.CreditGained += item.CreditGained
		if updatedAccounts[i] != nil {
			out.Accounts = append(out.Accounts, updatedAccounts[i])
		}
	}
	if out.Failed > 0 && out.Succeeded == 0 && out.Total > 0 {
		out.OK = false
	}
	return out, nil
}

// RunAutoHarvest 启动定时自动签到与积分收割循环，随 ctx 取消退出。
func (s *AccountService) RunAutoHarvest(ctx context.Context, interval time.Duration, doChat bool) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			res, err := s.HarvestAllAccounts(runCtx, doChat)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Warn("定时自动收割执行失败", "err", err)
			} else if res != nil && res.Total > 0 {
				s.logger.Info("定时自动收割完成", "total", res.Total, "succeeded", res.Succeeded, "failed", res.Failed, "credit_gained", res.CreditGained)
			}
		}
	}
}

func convertHarvestSteps(in []workbuddy.HarvestStep) []HarvestStepInfo {
	out := make([]HarvestStepInfo, 0, len(in))
	for _, st := range in {
		kind := strings.TrimSpace(st.Kind)
		if kind == "" {
			switch {
			case st.Skipped:
				kind = "skip"
			case st.OK:
				kind = "ok"
			default:
				kind = "fail"
			}
		}
		out = append(out, HarvestStepInfo{
			Name:   st.Name,
			Kind:   kind,
			Credit: st.Credit,
			Detail: st.Detail,
		})
	}
	return out
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
