package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/accountgroup"
	"cmd2api/ent/group"
	"cmd2api/internal/crypto"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/workbuddy"
)

// AccountService 负责上游账号的增删改查、WorkBuddy 凭证/设备/代理管理与健康检查。
type AccountService struct {
	client       *ent.Client
	vault        *crypto.Vault
	relayClient  *relay.Client
	sched        *scheduler.Scheduler
	agSvc        *AntigravityService
	proxyPool    *ProxyPoolService
	logger       *slog.Logger
	refreshLocks sync.Map // map[int64]*sync.Mutex
}

func (s *AccountService) accountRefreshMutex(accountID int64) *sync.Mutex {
	val, _ := s.refreshLocks.LoadOrStore(accountID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// SetAntigravityService 注入 AntiGravity 容器编排服务，用于新建 antigravity 账号时自动分配 1:1 专属 agy 容器。
func (s *AccountService) SetAntigravityService(agSvc *AntigravityService) {
	if s != nil {
		s.agSvc = agSvc
	}
}

// SetProxyPoolService 注入代理 IP 池服务，用于远程补号自动轮询分配 IP、远程拉取代理与 Key 级策略执行。
func (s *AccountService) SetProxyPoolService(pp *ProxyPoolService) {
	if s != nil {
		s.proxyPool = pp
	}
}

// ProxyPoolService 返回当前绑定的代理 IP 池服务（未注入时为 nil）。
func (s *AccountService) ProxyPoolService() *ProxyPoolService {
	if s == nil {
		return nil
	}
	return s.proxyPool
}

// NewAccountService 构造 AccountService。
func NewAccountService(
	client *ent.Client,
	vault *crypto.Vault,
	relayClient *relay.Client,
	sched *scheduler.Scheduler,
	logger *slog.Logger,
) *AccountService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AccountService{
		client:      client,
		vault:       vault,
		relayClient: relayClient,
		sched:       sched,
		logger:      logger,
	}
}

// CreateAccountInput 是新建账号的输入。
type CreateAccountInput struct {
	Name  string
	Notes string
	// Platform 留空默认 workbuddy，兼容保留 commandcode 与 opencode。
	Platform       string
	AccountMode    string
	BaseURL        string
	APIKey         string
	RefreshToken   string
	UID            string
	Nickname       string
	Email          string
	EnterpriseID   string
	EnterpriseName string
	Domain         string
	ProxyURL       string
	Qimei36        string
	MachineID      string
	Concurrency    int
	Priority       int
	RateMultiplier float64
	Status         string
	Schedulable    *bool
	GroupIDs       []int64
	ExpiresAt      *time.Time
}

// UpdateAccountInput 是修改账号的输入。指针为 nil（或 api_key 为空）表示保留原值。
type UpdateAccountInput struct {
	Name           *string
	Notes          *string
	APIKey         *string
	RefreshToken   *string
	UID            *string
	Nickname       *string
	Email          *string
	EnterpriseID   *string
	EnterpriseName *string
	Domain         *string
	ProxyURL       *string
	Qimei36        *string
	MachineID      *string
	AccountMode    *string
	BaseURL        *string
	Concurrency    *int
	Priority       *int
	RateMultiplier *float64
	Status         *string
	Schedulable    *bool
	GroupIDs       *[]int64
	ExpiresAt      *time.Time
	ClearExpiry    bool
}

// ErrInvalidAPIKey 表示密钥为空或格式无效。
var ErrInvalidAPIKey = errors.New("API Key / AccessToken 不能为空")

// ErrInvalidPlatform 表示平台取值不受支持。
var ErrInvalidPlatform = errors.New("平台取值无效，只支持 workbuddy、antigravity、commandcode 或 opencode")

// ErrInvalidAccountMode 保留兼容错误变量。
var ErrInvalidAccountMode = errors.New("account_mode 取值无效")

// ErrPlatformMismatch 表示账号平台与分组平台不一致。
var ErrPlatformMismatch = errors.New("账号平台与分组平台不一致：一个分组只能装同平台的账号")

// Create 新建一个上游账号。
func (s *AccountService) Create(ctx context.Context, in CreateAccountInput) (*ent.Account, error) {
	platform := domain.NormalizePlatform(in.Platform)
	if !domain.IsValidPlatform(platform) {
		return nil, ErrInvalidPlatform
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("账号名称不能为空")
	}

	apiKey := strings.TrimSpace(in.APIKey)
	if apiKey == "" {
		return nil, ErrInvalidAPIKey
	}

	proxyURL := strings.TrimSpace(in.ProxyURL)
	if proxyURL != "" {
		normProxy, err := workbuddy.NormalizeProxyURL(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("代理地址无效: %w", err)
		}
		proxyURL = normProxy
	}

	domainVal := domain.WorkBuddyDefaultDomain
	if platform == domain.PlatformAntigravity {
		if strings.TrimSpace(in.Domain) != "" {
			domainVal = strings.TrimSpace(in.Domain)
		} else if strings.TrimSpace(in.BaseURL) != "" {
			domainVal = strings.TrimSpace(in.BaseURL)
		} else {
			domainVal = "http://127.0.0.1:8317"
		}
	} else if strings.TrimSpace(in.Domain) != "" {
		domainVal = workbuddy.NormalizeDomain(in.Domain)
	}
	qimei36, machineID := workbuddy.EnsureDevice(in.Qimei36, in.MachineID)

	extra := map[string]any{}
	if mode := strings.TrimSpace(in.AccountMode); mode != "" {
		extra["account_mode"] = mode
	}
	if baseURL := strings.TrimSpace(in.BaseURL); baseURL != "" {
		extra["base_url"] = baseURL
	}

	if len(in.GroupIDs) > 0 {
		if err := s.assertGroupsMatchPlatform(ctx, in.GroupIDs, platform); err != nil {
			return nil, err
		}
	}

	creds, err := s.buildCredentialsMap(apiKey, strings.TrimSpace(in.RefreshToken), nil)
	if err != nil {
		return nil, err
	}
	if baseURL := strings.TrimSpace(in.BaseURL); baseURL != "" {
		creds["base_url"] = baseURL
	}

	accType := domain.AccountTypeWorkBuddy
	switch platform {
	case domain.PlatformWorkBuddy:
		accType = domain.AccountTypeWorkBuddy
	case domain.PlatformAntigravity:
		accType = domain.AccountTypeOAuthJSON
	default:
		accType = domain.AccountTypeAPIKey
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = domain.StatusActive
	}
	schedulable := status == domain.StatusActive
	if in.Schedulable != nil {
		schedulable = *in.Schedulable
	}

	builder := s.client.Account.Create().
		SetName(name).
		SetPlatform(platform).
		SetType(accType).
		SetCredentials(creds).
		SetExtra(extra).
		SetProxyURL(proxyURL).
		SetQimei36(qimei36).
		SetMachineID(machineID).
		SetUID(strings.TrimSpace(in.UID)).
		SetNickname(strings.TrimSpace(in.Nickname)).
		SetEmail(strings.TrimSpace(in.Email)).
		SetEnterpriseID(strings.TrimSpace(in.EnterpriseID)).
		SetEnterpriseName(strings.TrimSpace(in.EnterpriseName)).
		SetDomain(domainVal).
		SetStatus(status).
		SetSchedulable(schedulable)

	if in.Notes != "" {
		builder = builder.SetNotes(in.Notes)
	}
	if in.Concurrency > 0 {
		builder = builder.SetConcurrency(in.Concurrency)
	}
	if in.Priority > 0 {
		builder = builder.SetPriority(in.Priority)
	}
	if in.RateMultiplier > 0 {
		builder = builder.SetRateMultiplier(in.RateMultiplier)
	}
	if in.ExpiresAt != nil {
		builder = builder.SetExpiresAt(*in.ExpiresAt)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}

	if len(in.GroupIDs) > 0 {
		if err := s.setGroups(ctx, created.ID, in.GroupIDs, platform); err != nil {
			if delErr := s.client.Account.DeleteOneID(created.ID).Exec(ctx); delErr != nil {
				s.logger.Error("回滚账号创建失败", "err", delErr, "account_id", created.ID)
			}
			return nil, err
		}
	}

	created = s.reloadWithGroups(ctx, created)

	// 每个 antigravity 账号自动分配 1 个专属单实例 agy 容器（不依赖宿主机本地安装 agy）；
	// 显式指定了外部 base_url 或 direct_cli / local_cli 模式的账号保持调用方显式配置不变。
	if platform == domain.PlatformAntigravity && s.agSvc != nil &&
		strings.TrimSpace(in.BaseURL) == "" &&
		strings.TrimSpace(in.Domain) == "" &&
		strings.TrimSpace(in.AccountMode) == "" {
		if _, boundAcc, pErr := s.agSvc.ProvisionAccountContainer(ctx, ProvisionAccountContainerInput{
			AccountID: created.ID,
			ProxyURL:  proxyURL,
			AutoStart: true,
		}); pErr != nil {
			s.logger.Warn("为新建 AntiGravity 账号自动分配独立 agy 容器失败", "account_id", created.ID, "err", pErr)
		} else if boundAcc != nil {
			created = s.reloadWithGroups(ctx, boundAcc)
		}
	}

	s.logger.Info("已创建上游账号", "account_id", created.ID, "name", created.Name,
		"platform", platform)
	return created, nil
}

// getActiveAccount 按 ID 取一个**未被软删除**的账号。
//
// 本项目的软删除没有用 ent 的拦截器自动改写查询，而是靠每处查询自己带上
// `deleted_at IS NULL`。代价就是这个：漏掉一处，就会读到一个"已经删掉"的账号。
// 实测踩过——删除接口因此可以重复删除同一个账号、Update 还能改已删除的账号。
//
// 所以规则是：凡是要按 ID 拿账号，一律走这里，不要在别处直接 client.Account.Get。
func (s *AccountService) getActiveAccount(ctx context.Context, id int64) (*ent.Account, error) {
	return s.client.Account.Query().
		Where(account.IDEQ(id), account.DeletedAtIsNil()).
		Only(ctx)
}

// reloadWithGroups 重新读取账号并带上分组边。
//
// 读失败时退回原对象：宁可少一个 group_ids 字段，也不该让一次成功的
// 写操作以错误的形式返回给调用方。
func (s *AccountService) reloadWithGroups(ctx context.Context, acc *ent.Account) *ent.Account {
	reloaded, err := s.client.Account.Query().
		Where(account.IDEQ(acc.ID)).
		WithGroups().
		Only(ctx)
	if err != nil {
		s.logger.Warn("重读账号分组失败", "err", err, "account_id", acc.ID)
		return acc
	}
	return reloaded
}

// Update 修改账号。
func (s *AccountService) Update(ctx context.Context, id int64, in UpdateAccountInput) (*ent.Account, error) {
	current, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	platform := domain.NormalizePlatform(current.Platform)

	builder := s.client.Account.UpdateOneID(id)

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, errors.New("账号名不能为空")
		}
		builder = builder.SetName(name)
	}
	if in.Notes != nil {
		builder = builder.SetNotes(*in.Notes)
	}

	var newAPIKey, newRefreshToken string
	credsChanged := false
	if in.APIKey != nil {
		if apiKey := strings.TrimSpace(*in.APIKey); apiKey != "" {
			newAPIKey = apiKey
			credsChanged = true
		}
	}
	if in.RefreshToken != nil {
		if rt := strings.TrimSpace(*in.RefreshToken); rt != "" {
			newRefreshToken = rt
			credsChanged = true
		}
	}
	if credsChanged {
		credsMap, err := s.buildCredentialsMap(newAPIKey, newRefreshToken, current.Credentials)
		if err != nil {
			return nil, err
		}
		builder = builder.SetCredentials(credsMap)
		if newAPIKey != "" && s.relayClient != nil {
			if prev, ok := s.decryptKey(current); ok {
				s.relayClient.ForgetKey(prev)
			}
		}
	}

	if in.UID != nil {
		builder = builder.SetUID(strings.TrimSpace(*in.UID))
	}
	if in.Nickname != nil {
		builder = builder.SetNickname(strings.TrimSpace(*in.Nickname))
	}
	if in.Email != nil {
		builder = builder.SetEmail(strings.TrimSpace(*in.Email))
	}
	if in.EnterpriseID != nil {
		builder = builder.SetEnterpriseID(strings.TrimSpace(*in.EnterpriseID))
	}
	if in.EnterpriseName != nil {
		builder = builder.SetEnterpriseName(strings.TrimSpace(*in.EnterpriseName))
	}
	if in.Domain != nil {
		dom := strings.TrimSpace(*in.Domain)
		if dom == "" {
			if platform == domain.PlatformAntigravity {
				dom = "http://127.0.0.1:8317"
			} else {
				dom = domain.WorkBuddyDefaultDomain
			}
		} else if platform != domain.PlatformAntigravity {
			dom = workbuddy.NormalizeDomain(dom)
		}
		builder = builder.SetDomain(dom)
	}
	if in.ProxyURL != nil {
		proxy := workbuddy.PreserveRedactedProxyPassword(strings.TrimSpace(*in.ProxyURL), current.ProxyURL)
		if proxy != "" {
			normProxy, pErr := workbuddy.NormalizeProxyURL(proxy)
			if pErr != nil {
				return nil, fmt.Errorf("代理地址无效: %w", pErr)
			}
			proxy = normProxy
		}
		builder = builder.SetProxyURL(proxy)
	}
	if in.Qimei36 != nil || in.MachineID != nil {
		qVal := current.Qimei36
		mVal := current.MachineID
		if in.Qimei36 != nil {
			qVal = *in.Qimei36
		}
		if in.MachineID != nil {
			mVal = *in.MachineID
		}
		qimei36, machineID := workbuddy.EnsureDevice(qVal, mVal)
		builder = builder.SetQimei36(qimei36).SetMachineID(machineID)
	}

	if in.AccountMode != nil || in.BaseURL != nil {
		extra := map[string]any{}
		for k, v := range current.Extra {
			extra[k] = v
		}
		if in.AccountMode != nil {
			if mode := strings.TrimSpace(*in.AccountMode); mode != "" {
				extra["account_mode"] = mode
			} else {
				delete(extra, "account_mode")
			}
		}
		if in.BaseURL != nil {
			if base := strings.TrimSpace(*in.BaseURL); base != "" {
				extra["base_url"] = base
			} else {
				delete(extra, "base_url")
			}
		}
		builder = builder.SetExtra(extra)
		if s.relayClient != nil {
			if prev, ok := s.decryptKey(current); ok {
				s.relayClient.ForgetKey(prev)
			}
		}
	}
	if in.Concurrency != nil {
		if *in.Concurrency <= 0 {
			return nil, errors.New("并发数必须大于 0")
		}
		builder = builder.SetConcurrency(*in.Concurrency)
	}
	if in.Priority != nil {
		builder = builder.SetPriority(*in.Priority)
	}
	if in.RateMultiplier != nil {
		builder = builder.SetRateMultiplier(*in.RateMultiplier)
	}
	if in.Status != nil {
		builder = builder.SetStatus(*in.Status)
		if *in.Status == domain.StatusActive {
			builder = builder.SetSchedulable(true).ClearErrorMessage()
		} else {
			builder = builder.SetSchedulable(false)
		}
		builder = builder.SetConsecutiveFailures(0)
	}
	if in.Schedulable != nil {
		builder = builder.SetSchedulable(*in.Schedulable)
	}
	if in.ClearExpiry {
		builder = builder.ClearExpiresAt()
	} else if in.ExpiresAt != nil {
		builder = builder.SetExpiresAt(*in.ExpiresAt)
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}

	if in.GroupIDs != nil {
		plat := domain.NormalizePlatform(updated.Platform)
		if err := s.setGroups(ctx, id, *in.GroupIDs, plat); err != nil {
			return nil, err
		}
	}
	_ = platform

	return s.reloadWithGroups(ctx, updated), nil
}

// Delete 软删除账号，并清掉它的内存状态。
//
// 必须走 UPDATE 置 deleted_at，不能用 DeleteOneID。
//
// 踩过的坑：软删除原来只做了一半——查询都带了 `deleted_at IS NULL`，
// 删除却还是硬删除。于是只要账号有用量记录，usage_logs.account_id 的
// 外键就会挡住删除，接口直接 500。更糟的是这个 bug 只在"账号已经被用过"
// 之后才出现，全新部署时测不出来。
//
// 软删除同时也保住了历史用量：删掉账号不该让统计里的历史数据凭空消失。
func (s *AccountService) Delete(ctx context.Context, id int64) error {
	acc, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return err
	}
	if err := s.client.Account.UpdateOneID(id).
		SetDeletedAt(time.Now()).
		Exec(ctx); err != nil {
		return err
	}
	// 释放调度器里该账号的并发闸门与上游会话状态，
	// 否则这些 map 会随删号次数一直长。
	if key, ok := s.decryptKey(acc); ok {
		s.relayClient.ForgetKey(key)
	}
	s.logger.Info("已删除上游账号", "account_id", id, "name", acc.Name)
	return nil
}

// assertGroupsMatchPlatform 校验分组都存在且平台与账号一致。
//
// 这是分组绑定平台这个设计的执行点：一个分组只能装同平台的账号。
// 混装会让「请求某个模型」被路由到根本没有该模型的账号池上。
func (s *AccountService) assertGroupsMatchPlatform(ctx context.Context, groupIDs []int64, platform string) error {
	ids := uniqueIDs(groupIDs)
	if len(ids) == 0 {
		return nil
	}
	groups, err := s.client.Group.Query().
		Where(group.IDIn(ids...), group.DeletedAtIsNil()).
		All(ctx)
	if err != nil {
		return fmt.Errorf("校验分组: %w", err)
	}
	if len(groups) != len(ids) {
		return errors.New("包含不存在的分组")
	}
	normAccPlat := domain.NormalizePlatform(platform)
	for _, g := range groups {
		groupPlatform := domain.NormalizePlatform(g.Platform)
		if groupPlatform != normAccPlat {
			return fmt.Errorf("%w（分组「%s」是 %s 平台，账号是 %s 平台）",
				ErrPlatformMismatch, g.Name, groupPlatform, normAccPlat)
		}
	}
	return nil
}

// setGroups 重设账号的分组绑定。
func (s *AccountService) setGroups(ctx context.Context, accountID int64, groupIDs []int64, platform string) error {
	if err := s.assertGroupsMatchPlatform(ctx, groupIDs, platform); err != nil {
		return err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.AccountGroup.Delete().
		Where(accountgroup.AccountIDEQ(accountID)).
		Exec(ctx); err != nil {
		return fmt.Errorf("清除旧分组绑定: %w", err)
	}

	for _, gid := range uniqueIDs(groupIDs) {
		if _, err := tx.AccountGroup.Create().
			SetAccountID(accountID).
			SetGroupID(gid).
			Save(ctx); err != nil {
			return fmt.Errorf("绑定分组 %d: %w", gid, err)
		}
	}

	return tx.Commit()
}

// Check 探活单个账号并回写结果。
func (s *AccountService) Check(ctx context.Context, id int64) (*relay.ProbeResult, error) {
	acc, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return nil, err
	}

	// 走调度器拿接入信息，这样两种平台的探活方式由它统一分派，
	// 不用在这里再写一遍平台判断。
	target, err := s.sched.TargetByAccountID(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "api_key") {
			return nil, errors.New("该账号的密钥无法解密，请重新录入")
		}
		return nil, err
	}

	result := s.relayClient.Probe(ctx, target)
	s.applyProbeResult(ctx, acc, result)

	// 顺手刷一次余额。两个请求打的是同一个上游，探活都已经发了，
	// 顺带把余额带回来等于零额外代价——列表页就不用自己去打上游了。
	s.applyBalanceResult(ctx, acc, s.relayClient.FetchBalance(ctx, target))

	return &result, nil
}

// FetchWhoami 用一把**还没入库**的密钥去上游换账号身份。
//
// 单独开一个方法是因为浏览器授权收尾时密钥还不在账号表里，走不了
// TargetByAccountID 那条正常路径，但「先验一次再建号」这件事必须做——
// 建出一个用不了的账号，比当场告诉用户密钥不行要糟得多。
//
// whoami 不生成 token、不占额度，所以它比探活更适合做这件事。
func (s *AccountService) FetchWhoami(ctx context.Context, apiKey string) (relay.Whoami, error) {
	return s.relayClient.FetchWhoami(ctx, apiKey)
}

// RefreshBalance 只刷新余额，不做探活。
//
// 跟探活分开是有意的：探活会真的发一次生成请求、消耗 token，
// 只想看余额的人不该被迫烧一次额度。
func (s *AccountService) RefreshBalance(ctx context.Context, id int64) (*relay.BalanceResult, error) {
	acc, err := s.getActiveAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	target, err := s.sched.TargetByAccountID(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "api_key") {
			return nil, errors.New("该账号的密钥无法解密，请重新录入")
		}
		return nil, err
	}

	result := s.relayClient.FetchBalance(ctx, target)
	s.applyBalanceResult(ctx, acc, result)
	return &result, nil
}

// RefreshAllBalanceItem 是「一键刷新全部余额」里单个账号的结果。
//
// Supported 为 false 表示该平台查不了余额（目前是 OpenCode），这不是失败。
type RefreshAllBalanceItem struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

// refreshAllBalanceConcurrency 是刷新全部余额时的并发上限。
//
// 上游余额接口是只读的、很轻（两个 GET），账号一多串行会等很久；
// 但也不该一次把上游打满，保守取 4。
const refreshAllBalanceConcurrency = 4

// RefreshAllBalances 刷新所有未删除账号的余额。
//
// 只打上游的只读接口，不消耗生成额度，所以可以做成随便点的按钮。
// 每个账号单独超时：一个卡死的账号只影响它自己，不拖垮整轮刷新。
//
// 账号目标（含解密后的密钥）先在主协程里顺序解析完，再并发发请求——
// 调度器不是为并发读取设计的，把它的调用圈在单协程里最省心，
// 而真正慢的是网络，那部分照样并发。
func (s *AccountService) RefreshAllBalances(ctx context.Context) []RefreshAllBalanceItem {
	accs, err := s.client.Account.Query().
		Where(account.DeletedAtIsNil()).
		Order(ent.Asc(account.FieldID)).
		All(ctx)
	if err != nil {
		s.logger.Warn("刷新全部余额时读取账号列表失败", "err", err)
		return nil
	}

	items := make([]RefreshAllBalanceItem, len(accs))
	targets := make([]*scheduler.Target, len(accs))
	for i, acc := range accs {
		items[i] = RefreshAllBalanceItem{ID: acc.ID, Name: acc.Name}
		t, targetErr := s.sched.TargetByAccountID(ctx, acc.ID)
		if targetErr != nil {
			items[i].Error = "读取账号密钥失败，请重新录入"
			continue
		}
		targets[i] = t
	}

	sem := make(chan struct{}, refreshAllBalanceConcurrency)
	var wg sync.WaitGroup
	for i := range accs {
		if targets[i] == nil {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()

			result := s.relayClient.FetchBalance(cctx, targets[i])
			s.applyBalanceResult(cctx, accs[i], result)

			items[i].Supported = result.Supported
			if result.Err != nil {
				items[i].Error = result.Err.Error()
			}
		}(i)
	}
	wg.Wait()
	return items
}

// applyBalanceResult 把余额结果写回账号。
//
// 失败时刻意**不更新** balance_fetched_at：那个字段的意思是「界面上的数字
// 是什么时候取的」。失败时把它推到当前时间，会让一个三天前的余额看起来像
// 刚刚取的。保持原值，界面就能显示「$8.69（3 天前）」外加一条刷新失败——
// 这是实话，而「刚刚取的 $8.69」不是。
func (s *AccountService) applyBalanceResult(ctx context.Context, acc *ent.Account, result relay.BalanceResult) {
	// 平台不支持查余额：什么都不写。写了 fetched_at 会让界面显示
	// 「刚刚刷新」却没有任何数据，看着像刷新失败。
	if !result.Supported {
		return
	}

	builder := s.client.Account.UpdateOneID(acc.ID)

	if result.Err != nil {
		builder = builder.SetBalanceError(truncateBalanceError(result.Err.Error()))
		if _, err := builder.Save(ctx); err != nil {
			s.logger.Warn("回写余额失败原因出错", "err", err, "account_id", acc.ID)
		}
		return
	}

	b := result.Balance
	builder = builder.
		SetBalanceFetchedAt(time.Now()).
		ClearBalanceError().
		SetBalanceRemaining(b.Remaining)

	if b.TotalCredits > 0 || domain.NormalizePlatform(acc.Platform) == domain.PlatformWorkBuddy {
		builder = builder.SetBalanceTotalCredits(b.TotalCredits)
	}
	if b.Packages != nil {
		builder = builder.SetBalancePackages(b.Packages)
	} else if domain.NormalizePlatform(acc.Platform) == domain.PlatformWorkBuddy {
		builder = builder.SetBalancePackages([]map[string]any{})
	}

	if b.PlanID != "" {
		builder = builder.SetBalancePlanID(b.PlanID)
	} else {
		builder = builder.ClearBalancePlanID()
	}
	if b.PeriodEnd != nil {
		builder = builder.SetBalancePeriodEnd(*b.PeriodEnd)
	} else {
		builder = builder.ClearBalancePeriodEnd()
	}

	builder = applyWindow(builder, b.FiveHour,
		func(u *ent.AccountUpdateOne, used, capacity float64) *ent.AccountUpdateOne {
			return u.SetBalance5hUsed(used).SetBalance5hCap(capacity)
		},
		func(u *ent.AccountUpdateOne) *ent.AccountUpdateOne {
			return u.ClearBalance5hUsed().ClearBalance5hCap()
		},
		func(u *ent.AccountUpdateOne, t time.Time) *ent.AccountUpdateOne {
			return u.SetBalance5hResetAt(t)
		},
		func(u *ent.AccountUpdateOne) *ent.AccountUpdateOne { return u.ClearBalance5hResetAt() },
	)
	builder = applyWindow(builder, b.Weekly,
		func(u *ent.AccountUpdateOne, used, capacity float64) *ent.AccountUpdateOne {
			return u.SetBalanceWeeklyUsed(used).SetBalanceWeeklyCap(capacity)
		},
		func(u *ent.AccountUpdateOne) *ent.AccountUpdateOne {
			return u.ClearBalanceWeeklyUsed().ClearBalanceWeeklyCap()
		},
		func(u *ent.AccountUpdateOne, t time.Time) *ent.AccountUpdateOne {
			return u.SetBalanceWeeklyResetAt(t)
		},
		func(u *ent.AccountUpdateOne) *ent.AccountUpdateOne { return u.ClearBalanceWeeklyResetAt() },
	)

	if _, err := builder.Save(ctx); err != nil {
		s.logger.Warn("回写余额失败", "err", err, "account_id", acc.ID)
	}
}

// applyWindow 按窗口是否存在，选择写入数值还是清空。
//
// 上游可能突然不再返回某个窗口（改了套餐、接口变了），这时必须把旧值清掉。
// 留着上一次的用量会让界面一直显示一个早已过期的窗口，比不显示更糟。
func applyWindow(
	builder *ent.AccountUpdateOne,
	window *relay.BalanceWindow,
	setValues func(*ent.AccountUpdateOne, float64, float64) *ent.AccountUpdateOne,
	clearValues func(*ent.AccountUpdateOne) *ent.AccountUpdateOne,
	setReset func(*ent.AccountUpdateOne, time.Time) *ent.AccountUpdateOne,
	clearReset func(*ent.AccountUpdateOne) *ent.AccountUpdateOne,
) *ent.AccountUpdateOne {
	if window == nil {
		return clearReset(clearValues(builder))
	}
	builder = setValues(builder, window.Used, window.Cap)
	if window.ResetAt != nil {
		return setReset(builder, *window.ResetAt)
	}
	return clearReset(builder)
}

// truncateBalanceError 限制写进 error_message 类的字段长度。
//
// 这些字段要在表格里展示，上游偶尔会回一整页 HTML，原样存下来界面就废了。
// 按 rune 截断而不是按字节——按字节切会把一个多字节字符切成两半，
// 存进 Postgres 的 text 列时直接报编码错。
func truncateBalanceError(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	const max = 300
	runes := []rune(msg)
	if len(runes) <= max {
		return msg
	}
	return string(runes[:max]) + "…"
}

// applyProbeResult 把探活结果写回账号。
func (s *AccountService) applyProbeResult(ctx context.Context, acc *ent.Account, result relay.ProbeResult) {
	now := time.Now()
	builder := s.client.Account.UpdateOneID(acc.ID).
		SetLastHealthCheckAt(now).
		SetLatencyMs(int(result.Latency.Milliseconds()))

	if result.Err == nil {
		// 探活成功就是最有力的恢复信号：把失败计数清零、解除临时状态，
		// 让账号重新参与调度。但只要不是手动禁用的，就自动恢复。
		builder = builder.
			SetLastHealthCheckOk(true).
			ClearLastHealthCheckError().
			SetConsecutiveFailures(0)
		if acc.Status == domain.StatusError {
			builder = builder.SetStatus(domain.StatusActive).SetSchedulable(true).ClearErrorMessage()
			s.logger.Info("账号探活成功，已自动恢复", "account_id", acc.ID, "name", acc.Name)
		}
	} else {
		builder = builder.
			SetLastHealthCheckOk(false).
			SetLastHealthCheckError(result.ErrorMessage()).
			SetConsecutiveFailures(acc.ConsecutiveFailures + 1)
	}

	if _, err := builder.Save(ctx); err != nil {
		s.logger.Warn("回写探活结果失败", "err", err, "account_id", acc.ID)
	}
}

// RunHealthChecks 周期性地探活所有账号。
//
// 探活是串行 + 间隔的：并发探活一堆账号会在上游那边形成一波整齐的请求，
// 既是异常模式，也容易触发限流。
func (s *AccountService) RunHealthChecks(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.logger.Info("账号健康检查已启动", "interval", interval.String())
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("账号健康检查已停止")
			return
		case <-ticker.C:
			s.checkAll(ctx)
		}
	}
}

// checkAll 探活一轮。
func (s *AccountService) checkAll(ctx context.Context) {
	accounts, err := s.client.Account.Query().
		Where(
			account.DeletedAtIsNil(),
			// 手动禁用的账号不探活：管理员停用它是有意的，
			// 自动把它探活回来等于违背管理员的意图。
			account.StatusNEQ(domain.StatusDisabled),
		).
		Order(ent.Asc(account.FieldPriority)).
		All(ctx)
	if err != nil {
		s.logger.Warn("查询待探活账号失败", "err", err)
		return
	}

	var okCount, failCount int
	for _, acc := range accounts {
		if ctx.Err() != nil {
			return
		}
		target, err := s.sched.TargetByAccountID(ctx, acc.ID)
		if err != nil {
			s.logger.Warn("取账号接入信息失败，跳过探活", "account_id", acc.ID, "err", err)
			continue
		}

		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result := s.relayClient.Probe(probeCtx, target)
		// 余额另开一个上下文：探活那个可能已经把时间用光了，
		// 拿一个到期的 deadline 去发请求会立刻超时，余额永远刷不出来。
		balanceCtx, balanceCancel := context.WithTimeout(ctx, 20*time.Second)
		balance := s.relayClient.FetchBalance(balanceCtx, target)
		balanceCancel()
		cancel()

		s.applyProbeResult(ctx, acc, result)
		s.applyBalanceResult(ctx, acc, balance)
		if result.Err == nil {
			okCount++
		} else {
			failCount++
			s.logger.Warn("账号探活失败",
				"account_id", acc.ID, "name", acc.Name, "reason", result.ErrorMessage())
		}
	}
	if len(accounts) > 0 {
		s.logger.Info("账号探活完成", "total", len(accounts), "ok", okCount, "failed", failCount)
	}
}

// decryptKey 解出账号的明文密钥。
func (s *AccountService) decryptKey(acc *ent.Account) (string, bool) {
	if acc == nil || acc.Credentials == nil {
		return "", false
	}
	if enc, ok := acc.Credentials["api_key_encrypted"].(string); ok && enc != "" && s.vault != nil {
		plain, err := s.vault.Decrypt(enc)
		if err != nil {
			return "", false
		}
		return plain, true
	}
	// 兼容手工插入或明文测试账号。
	for _, k := range []string{"api_key", "access_token"} {
		if raw, ok := acc.Credentials[k].(string); ok && raw != "" {
			return raw, true
		}
	}
	return "", false
}

// MaskedKey 返回账号密钥的打码形式，供列表展示。
func (s *AccountService) MaskedKey(acc *ent.Account) string {
	key, ok := s.decryptKey(acc)
	if !ok {
		return ""
	}
	return crypto.MaskKey(key)
}

// uniqueIDs 去重，保持原顺序。
func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
