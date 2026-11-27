package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"cmd2api/ent"
	"cmd2api/ent/account"
	"cmd2api/ent/accountgroup"
	"cmd2api/ent/group"
	"cmd2api/internal/antigravity"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/workbuddy"
)

const (
	defaultOAuthSessionTTL = 30 * time.Minute

	OAuthStatusPending   = "pending"
	OAuthStatusCompleted = "completed"
	OAuthStatusFailed    = "failed"
)

// OAuthSession 描述一次跨平台交互式 OAuth / CLI 授权会话状态。
type OAuthSession struct {
	SessionID      string                      `json:"session_id"`
	Platform       string                      `json:"platform"`
	Provider       string                      `json:"provider"`
	Mode           string                      `json:"mode"` // "container_remote" | "gateway_pkce" | "commandcode_studio" | "workbuddy_link" | "token_import"
	NodeID         int64                       `json:"node_id,omitempty"`
	NodeName       string                      `json:"node_name,omitempty"`
	AccountID      int64                       `json:"account_id,omitempty"`
	GroupIDs       []int64                     `json:"group_ids,omitempty"`
	IPMode         string                      `json:"ip_mode,omitempty"`
	ProxyURL       string                      `json:"proxy_url,omitempty"`
	AuthURL        string                      `json:"auth_url"`
	RedirectURI     string                      `json:"redirect_uri"`
	State           string                      `json:"state"`
	CodeChallenge   string                      `json:"code_challenge,omitempty"`
	CodeVerifier    string                      `json:"-"` // 敏感 PKCE 校验码不对外回显
	Fingerprint     antigravity.NodeFingerprint `json:"fingerprint,omitempty"`
	AccountMode     string                      `json:"account_mode,omitempty"`
	BaseURL         string                      `json:"base_url,omitempty"`
	Instructions    string                      `json:"instructions"`
	Status          string                      `json:"status"`
	Error           string                      `json:"error,omitempty"`
	ResultFilename  string                      `json:"result_filename,omitempty"`
	ResultAccount   *ent.Account                `json:"-"`
	ResultAccountID int64                       `json:"result_account_id,omitempty"`
	CreatedAt       time.Time                   `json:"created_at"`
	ExpiresAt       time.Time                   `json:"expires_at"`
	CompletedAt     *time.Time                  `json:"completed_at,omitempty"`
}

// CreateOAuthSessionInput 创建 OAuth 会话入参。
type CreateOAuthSessionInput struct {
	Platform     string
	Provider     string
	NodeID       int64
	AccountID    int64
	GroupIDs     []int64
	IPMode       string // "auto_pool" | "manual" | "direct"
	ProxyURL     string
	RedirectURI  string
	RedirectMode bool
	AccountName  string
	AccountMode  string
	BaseURL      string
}

// CompleteOAuthCallbackInput 完成 OAuth 回调/凭据导入入参。
type CompleteOAuthCallbackInput struct {
	SessionID    string
	State        string
	Platform     string
	Provider     string
	NodeID       int64
	GroupIDs     []int64
	IPMode       string // "auto_pool" | "manual" | "direct"
	ProxyURL     string
	CallbackURL  string
	RedirectURI  string
	Code         string
	APIKey       string
	RefreshToken string
	UID          string
	Nickname     string
	Domain       string
	Email        string
	ProjectID    string
	AccountName  string
	AccountMode  string
	BaseURL      string
	Filename     string
	TokenJSON    json.RawMessage
	RawJSON      string
}

// OAuthCallbackResult 描述 OAuth 回调闭环完成后的产物与入池账号。
type OAuthCallbackResult struct {
	OK           bool                     `json:"ok"`
	Session      *OAuthSession            `json:"session,omitempty"`
	Platform     string                   `json:"platform"`
	Provider     string                   `json:"provider"`
	NodeID       int64                    `json:"node_id,omitempty"`
	Node         *ContainerNodeView       `json:"node,omitempty"`
	Filename     string                   `json:"filename,omitempty"`
	AuthFile     *AntigravityAuthFileView `json:"auth_file,omitempty"`
	Account      *ent.Account             `json:"account,omitempty"`
	Accounts     []*ent.Account           `json:"accounts,omitempty"`
	RemoteResult map[string]any           `json:"remote_result,omitempty"`
	Message      string                   `json:"message"`
}

// OAuthService 统一编排 AntiGravity / CommandCode / WorkBuddy / OpenCode 全平台 OAuth 授权链接签发、回调兑换、.json 凭证落盘与账号入池。
type OAuthService struct {
	mu          sync.RWMutex
	db          *ent.Client
	accounts    *AccountService
	antigravity *AntigravityService
	relayClient *relay.Client
	logger      *slog.Logger

	sessions   map[string]*OAuthSession // session_id -> session
	byState    map[string]string        // state -> session_id
}

// NewOAuthService 构造全平台统一 OAuth 会话与回调服务。
func NewOAuthService(
	db *ent.Client,
	accounts *AccountService,
	agy *AntigravityService,
	relayClient *relay.Client,
	logger *slog.Logger,
) *OAuthService {
	if relayClient == nil && accounts != nil {
		relayClient = accounts.relayClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OAuthService{
		db:          db,
		accounts:    accounts,
		antigravity: agy,
		relayClient: relayClient,
		logger:      logger,
		sessions:    make(map[string]*OAuthSession),
		byState:     make(map[string]string),
	}
}

// CreateSession 为任意支持平台创建状态化 OAuth 授权会话（支持容器远程委托与网关内置 PKCE 双模）。
func (s *OAuthService) CreateSession(ctx context.Context, in CreateOAuthSessionInput) (*OAuthSession, error) {
	if s == nil {
		return nil, errors.New("OAuth 服务未初始化")
	}

	platform, provider := normalizeOAuthPlatformAndProvider(in.Platform, in.Provider, in.NodeID)
	redirectURI := strings.TrimSpace(in.RedirectURI)
	if redirectURI == "" {
		redirectURI = "http://localhost:8080/api/oauth/callback"
	}

	proxyURL := strings.TrimSpace(in.ProxyURL)
	if proxyURL != "" {
		np, pErr := workbuddy.NormalizeProxyURL(proxyURL)
		if pErr != nil {
			return nil, fmt.Errorf("OAuth 会话代理地址无效: %w", pErr)
		}
		proxyURL = np
	}

	sessID := "oauth_" + randomOAuthHex(12)
	state := "st_" + randomOAuthHex(16)
	verifier := randomOAuthPKCEVerifier()
	challenge := pkceChallengeS256(verifier)
	now := time.Now().UTC()

	sess := &OAuthSession{
		SessionID:     sessID,
		Platform:      platform,
		Provider:      provider,
		NodeID:        in.NodeID,
		AccountID:     in.AccountID,
		GroupIDs:      uniqueIDs(in.GroupIDs),
		IPMode:        strings.ToLower(strings.TrimSpace(in.IPMode)),
		ProxyURL:      proxyURL,
		RedirectURI:   redirectURI,
		State:         state,
		CodeVerifier:  verifier,
		CodeChallenge: challenge,
		AccountMode:   strings.TrimSpace(in.AccountMode),
		BaseURL:       strings.TrimSpace(in.BaseURL),
		Status:        OAuthStatusPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(defaultOAuthSessionTTL),
	}

	switch platform {
	case domain.PlatformAntigravity:
		if err := s.initAntigravitySession(ctx, sess); err != nil {
			return nil, err
		}
	case domain.PlatformCommandCode:
		sess.Mode = "commandcode_studio"
		sess.AuthURL = relay.CommandCodeAuthURL(redirectURI, state, in.RedirectMode)
		sess.Instructions = "请在浏览器打开授权链接完成 Command Code Studio 登录；回调将自动回传至本网关，或复制浏览器最终跳转 URL 粘贴到第 2 步完成入池。"
	case domain.PlatformWorkBuddy:
		if in.AccountID > 0 && s.accounts != nil {
			res, lErr := s.accounts.IssueLoginLink(ctx, in.AccountID, "")
			if lErr == nil && res != nil && strings.TrimSpace(res.LoginURL) != "" {
				sess.Mode = "workbuddy_link"
				sess.AuthURL = res.LoginURL
				sess.Instructions = "已生成 WorkBuddy 300s 免密登录链接；如需录入新账号凭证，可直接粘贴回调 URL、AccessToken 或 OAuth Token JSON 完成回调导入。"
				break
			}
		}
		sess.Mode = "workbuddy_oauth"
		sess.AuthURL = buildWorkBuddyOAuthURL(domain.WorkBuddyDefaultDomain, redirectURI, state, challenge, in.RedirectMode)
		sess.Instructions = "请在浏览器登录 WorkBuddy（CodeBuddy）并授权回传至本网关，或复制回调 URL / AccessToken / OAuth Token JSON 粘贴到第 2 步，系统将自动绑定独立设备指纹与代理并入池。"
	case domain.PlatformOpenCode:
		ocMode := normalizeOpenCodeAccountMode(in.AccountMode, provider)
		ocBase := strings.TrimSpace(in.BaseURL)
		if ocBase == "" {
			ocBase = domain.DefaultOpenCodeBaseURL(ocMode)
		}
		sess.AccountMode = ocMode
		sess.BaseURL = ocBase
		sess.Mode = "opencode_" + ocMode
		sess.AuthURL = buildOpenCodeOAuthURL(ocMode, ocBase, redirectURI, state, challenge, in.RedirectMode)
		sess.Instructions = fmt.Sprintf("请在浏览器打开 OpenCode (%s) 授权/控制台页面获取密钥或授权回调（默认端点 %s），提交回调 URL、API Key 或 JSON 后自动分配指纹与代理入池。",
			strings.ToUpper(ocMode), ocBase)
	default:
		return nil, fmt.Errorf("暂不支持平台 %q 的 OAuth 会话创建", platform)
	}

	s.storeSession(sess)
	return cloneOAuthSession(sess), nil
}

func (s *OAuthService) initAntigravitySession(ctx context.Context, sess *OAuthSession) error {
	if s.antigravity == nil {
		return errors.New("AntiGravity 服务未初始化")
	}

	// Google Desktop OAuth 客户端 (1071006060591-...) 仅接受 http://localhost:<port>/... 回环地址；
	// 严禁把公网域名（如 https://cmd.essapi.uno/api/oauth/callback）传给 Google，否则必触发 Error 400: redirect_uri_mismatch。
	provLower := strings.ToLower(strings.TrimSpace(sess.Provider))
	if provLower == "" || provLower == "antigravity" || provLower == "agy" || provLower == "gemini-cli" || provLower == "gemini" {
		sess.RedirectURI = antigravity.NormalizeGoogleDesktopRedirectURI(sess.RedirectURI)
	}

	var spec antigravity.NodeSpec
	var nodeRunning bool
	if sess.NodeID > 0 {
		row, err := s.antigravity.getNodeRow(ctx, sess.NodeID)
		if err != nil {
			return err
		}
		spec = s.antigravity.nodeSpec(row)
		sess.NodeName = row.Name
		if sess.ProxyURL == "" && sess.IPMode != "direct" && sess.IPMode != "auto_pool" {
			sess.ProxyURL = row.ProxyURL
		}
		sess.Fingerprint = spec.Fingerprint
		if row.Mode == domain.ContainerModeRemoteAPI {
			nodeRunning = true
		} else if s.antigravity.docker != nil {
			st, _ := s.antigravity.docker.InspectContainer(ctx, spec.EffectiveContainerName())
			nodeRunning = st.Running
		}
	} else {
		// Account-First 1:1 模式：无需预先创建空节点，回调成功后自动按账号分配 IP 与 1:1 独立容器
		sess.Fingerprint = antigravity.GenerateNodeFingerprint(sess.Provider)
	}

	// 仅当显式指定了在线节点且为第三方容器 Provider 时才尝试向节点请求 OAuth URL
	if sess.NodeID > 0 && nodeRunning && !s.antigravity.NodeHasBoundAccount(ctx, sess.NodeID) &&
		(provLower == "claude" || provLower == "codex") {
		remoteCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		remoteRes, rErr := s.antigravity.agClient.RequestOAuthURL(remoteCtx, spec, sess.Provider, sess.RedirectURI)
		cancel()
		if rErr == nil && strings.TrimSpace(remoteRes.AuthURL) != "" {
			sess.Mode = "container_remote"
			sess.AuthURL = strings.TrimSpace(remoteRes.AuthURL)
			if strings.TrimSpace(remoteRes.State) != "" {
				sess.State = strings.TrimSpace(remoteRes.State)
			}
			sess.Instructions = firstNonEmptyService(remoteRes.Instructions,
				"请在浏览器打开授权链接登录，完成后复制浏览器地址栏完整回调 URL（含 ?code=...）粘贴到下方完成入池与容器绑定。")
			return nil
		}
	}

	// 网关内置标准 Google Desktop PKCE 授权链接生成器（本地与公网服务器 100% 通用，零 redirect_uri_mismatch）
	sess.Mode = "gateway_pkce"
	sess.AuthURL = buildProviderPKCEOAuthURL(sess.Provider, sess.RedirectURI, sess.State, sess.CodeChallenge)
	sess.Instructions = fmt.Sprintf(
		"1) 点击「新窗口打开授权」完成 Google 登录；2) 登录成功后浏览器会跳转至 %s?code=...（若部署在远程服务器，页面提示「无法访问此网站 / 拒绝连接」属完全正常现象！）；3) 直接复制浏览器地址栏里那整串 http://localhost... 链接粘贴到下方提交，系统将自动换取 Token、分配 IP 并创建 1:1 专属独立容器。",
		sess.RedirectURI,
	)
	return nil
}

// ListSessions 返回当前活跃/最近完成的 OAuth 授权会话列表（自动清理过期会话）。
func (s *OAuthService) ListSessions() []OAuthSession {
	if s == nil {
		return []OAuthSession{}
	}
	now := time.Now().UTC()
	s.mu.Lock()
	out := make([]OAuthSession, 0, len(s.sessions))
	for id, sess := range s.sessions {
		if now.After(sess.ExpiresAt) && sess.Status == OAuthStatusPending {
			delete(s.byState, sess.State)
			delete(s.sessions, id)
			continue
		}
		out = append(out, *cloneOAuthSession(sess))
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// CompleteCallback 完成统一多平台 OAuth 回调：解析回调 URL / code / Token JSON，
// 写盘凭证 JSON 或创建/更新账号，绑定分组并失效调度器缓存。
func (s *OAuthService) CompleteCallback(ctx context.Context, in CompleteOAuthCallbackInput) (*OAuthCallbackResult, error) {
	if s == nil {
		return nil, errors.New("OAuth 服务未初始化")
	}

	// 从 CallbackURL 中尝试提前提取 state 以便匹配挂起的会话
	extractedState := strings.TrimSpace(in.State)
	if extractedState == "" && strings.TrimSpace(in.CallbackURL) != "" {
		if parsed, err := extractOAuthURLParts(in.CallbackURL); err == nil && parsed.State != "" {
			extractedState = parsed.State
		}
	}

	sess := s.lookupSession(in.SessionID, extractedState)
	platform := strings.TrimSpace(in.Platform)
	provider := strings.TrimSpace(in.Provider)
	nodeID := in.NodeID
	groupIDs := uniqueIDs(in.GroupIDs)
	proxyURL := strings.TrimSpace(in.ProxyURL)

	ipMode := strings.ToLower(strings.TrimSpace(in.IPMode))

	if sess != nil {
		if platform == "" {
			platform = sess.Platform
		}
		if provider == "" {
			provider = sess.Provider
		}
		if nodeID <= 0 {
			nodeID = sess.NodeID
		}
		if len(groupIDs) == 0 {
			groupIDs = sess.GroupIDs
		}
		if ipMode == "" {
			ipMode = sess.IPMode
		}
		if proxyURL == "" {
			proxyURL = sess.ProxyURL
		}
	}
	in.IPMode = ipMode
	in.ProxyURL = proxyURL
	platform, provider = normalizeOAuthPlatformAndProvider(platform, provider, nodeID)

	var (
		res *OAuthCallbackResult
		err error
	)
	switch platform {
	case domain.PlatformAntigravity:
		res, err = s.completeAntigravityCallback(ctx, sess, nodeID, provider, groupIDs, in)
	case domain.PlatformCommandCode:
		res, err = s.completeCommandCodeCallback(ctx, sess, groupIDs, proxyURL, in)
	case domain.PlatformWorkBuddy:
		res, err = s.completeWorkBuddyCallback(ctx, sess, groupIDs, proxyURL, in)
	case domain.PlatformOpenCode:
		res, err = s.completeOpenCodeCallback(ctx, sess, groupIDs, proxyURL, in)
	default:
		err = fmt.Errorf("不支持的 OAuth 回调平台 %q", platform)
	}

	now := time.Now().UTC()
	if sess != nil {
		s.mu.Lock()
		if err != nil {
			sess.Status = OAuthStatusFailed
			sess.Error = err.Error()
		} else {
			sess.Status = OAuthStatusCompleted
			sess.Error = ""
			sess.CompletedAt = &now
			if res != nil {
				sess.ResultFilename = res.Filename
				if res.Account != nil {
					sess.ResultAccount = res.Account
					sess.ResultAccountID = res.Account.ID
				}
			}
		}
		resCopy := cloneOAuthSession(sess)
		s.mu.Unlock()
		if res != nil {
			res.Session = resCopy
		}
	}

	if err == nil && s.accounts != nil && s.accounts.sched != nil {
		s.accounts.sched.Invalidate()
	}
	return res, err
}

func (s *OAuthService) completeAntigravityCallback(
	ctx context.Context,
	sess *OAuthSession,
	nodeID int64,
	provider string,
	groupIDs []int64,
	in CompleteOAuthCallbackInput,
) (*OAuthCallbackResult, error) {
	if s.antigravity == nil {
		return nil, errors.New("AntiGravity 服务未初始化")
	}

	// 0. 先按 ip_mode ("auto_pool" | "manual" | "direct") 解析或从代理池自动分配专属出口 IP
	resolvedProxy, pErr := s.antigravity.ResolveAccountProxy(ctx, in.IPMode, in.ProxyURL, "", nil)
	if pErr != nil {
		return nil, pErr
	}

	// 1. 解析回调 URL、Code 或直接传入的 Token JSON（先兑换验证成功，再分配容器，杜绝失败时残留空容器！）
	rawTokenJSON := firstNonEmptyService(strings.TrimSpace(string(in.TokenJSON)), strings.TrimSpace(in.RawJSON))
	if rawTokenJSON == "null" {
		rawTokenJSON = ""
	}

	parsedCallback, _ := extractOAuthURLParts(in.CallbackURL)
	if parsedCallback.Error != "" {
		return nil, fmt.Errorf("OAuth 授权被拒绝 (%s): %s", parsedCallback.Error, parsedCallback.ErrorDescription)
	}

	code := strings.TrimSpace(in.Code)
	if code == "" {
		code = parsedCallback.Code
	}
	state := strings.TrimSpace(in.State)
	if state == "" {
		state = parsedCallback.State
	}
	if state == "" && sess != nil {
		state = sess.State
	}

	// 判断是否使用指定的未绑定现有节点，还是采用「1 账号 = 1 独立容器」自动创建新容器
	useDedicatedAutoContainer := nodeID <= 0
	var existingNodeRow *ent.ContainerNode
	var existingSpec antigravity.NodeSpec
	if !useDedicatedAutoContainer {
		if rRow, rErr := s.antigravity.getNodeRow(ctx, nodeID); rErr == nil && rRow != nil {
			if rRow.Mode == domain.ContainerModeLocalDocker && s.antigravity.NodeHasBoundAccount(ctx, nodeID) {
				useDedicatedAutoContainer = true
			} else {
				existingNodeRow = rRow
				existingSpec = s.antigravity.nodeSpec(rRow)
				if resolvedProxy == "" && in.IPMode != "direct" {
					resolvedProxy = strings.TrimSpace(rRow.ProxyURL)
				}
			}
		} else {
			useDedicatedAutoContainer = true
		}
	}

	// 2. 若显式绑定到了外部远程节点且提供了 callback_url，可尝试转发给远程管理端
	var remoteResult map[string]any
	if !useDedicatedAutoContainer && existingNodeRow != nil && existingNodeRow.Mode == domain.ContainerModeRemoteAPI {
		if cb := strings.TrimSpace(in.CallbackURL); cb != "" {
			cbCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			rOut, rErr := s.antigravity.agClient.SubmitOAuthCallback(cbCtx, existingSpec, provider, cb)
			cancel()
			if rErr == nil {
				remoteResult = rOut
			}
		}
	}

	credMap := map[string]any{}
	if rawTokenJSON != "" {
		// 支持直接粘贴任意多格式单条凭证（ADC / oauth_creds / 1// 裸 Token / JSON）
		if normConv, nErr := antigravity.NormalizeSingleCredential("oauth-paste.json", []byte(rawTokenJSON), resolvedProxy); nErr == nil && normConv.Valid {
			_ = json.Unmarshal(normConv.CanonicalAuthJSONBytes(), &credMap)
		} else if err := json.Unmarshal([]byte(rawTokenJSON), &credMap); err != nil {
			return nil, fmt.Errorf("解析 OAuth Token JSON 失败: %w", err)
		}
	} else if code == "" && remoteResult == nil && strings.TrimSpace(in.APIKey) == "" && strings.TrimSpace(in.RefreshToken) == "" {
		return nil, errors.New("请粘贴浏览器跳转后的完整回调 URL（含 ?code=...）、授权码 code 或 Token 凭证")
	}

	if rt := strings.TrimSpace(in.RefreshToken); rt != "" && extractExtraString(credMap, "refresh_token") == "" {
		credMap["refresh_token"] = rt
	}

	// 2.5 Google OAuth 授权码真实兑换：经分配的独立代理向 Google 换取 access_token + refresh_token + email + project_id
	provLower := strings.ToLower(strings.TrimSpace(provider))
	isGoogleProvider := provLower == "" || provLower == "antigravity" || provLower == "agy" || provLower == "gemini-cli" || provLower == "gemini"
	if code != "" && extractExtraString(credMap, "access_token") == "" && isGoogleProvider {
		exCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		httpCli, _, _ := s.antigravity.agClient.HTTPClientForNode(antigravity.NodeSpec{
			HostIP:   antigravity.GoogleOAuthTokenURL,
			ProxyURL: resolvedProxy,
		})
		redir := extractCallbackBaseRedirectURI(in.CallbackURL)
		if redir == "" {
			redir = strings.TrimSpace(in.RedirectURI)
		}
		if redir == "" && sess != nil {
			redir = sess.RedirectURI
		}
		redir = antigravity.NormalizeGoogleDesktopRedirectURI(redir)

		verifier := ""
		if sess != nil {
			verifier = sess.CodeVerifier
		}
		fpUA := antigravity.BuildCLIUserAgent("", "", "")
		if sess != nil && sess.Fingerprint.UserAgent != "" {
			fpUA = sess.Fingerprint.UserAgent
		}

		tok, tErr := antigravity.ExchangeOAuthCode(exCtx, httpCli, code, verifier, redir, "")
		if tErr == nil && tok != nil {
			credMap["access_token"] = tok.AccessToken
			credMap["client_id"] = antigravity.OfficialOAuthClientID
			if tok.RefreshToken != "" {
				credMap["refresh_token"] = tok.RefreshToken
			}
			if tok.ExpiresIn > 0 {
				credMap["expires_in"] = tok.ExpiresIn
				credMap["expires_at"] = time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second).Format(time.RFC3339)
			}
			if uInfo, uErr := antigravity.FetchGoogleUserInfo(exCtx, httpCli, tok.AccessToken, ""); uErr == nil && uInfo != nil && uInfo.Email != "" {
				if extractExtraString(credMap, "email") == "" {
					credMap["email"] = uInfo.Email
				}
			}
			if ca, caErr := antigravity.LoadCodeAssist(exCtx, httpCli, "", tok.AccessToken, fpUA); caErr == nil && ca != nil {
				if ca.ProjectID != "" && extractExtraString(credMap, "project_id") == "" {
					credMap["project_id"] = ca.ProjectID
				}
				if ca.TierID != "" {
					credMap["tier_id"] = ca.TierID
				}
			}
			cancel()
		} else {
			cancel()
			// 真实 Google 授权码（以 "4/" 开头）兑换失败时明确向用户返回具体原因，绝不静默落盘空凭证！
			if remoteResult == nil && strings.HasPrefix(code, "4/") {
				return nil, fmt.Errorf("Google OAuth 令牌兑换失败（请确认授权码未重复使用且服务器/所选代理可连接 oauth2.googleapis.com）：%w", tErr)
			}
		}
	}

	// 补齐通用元数据字段
	fp := existingSpec.Fingerprint
	if fp.FingerprintID == "" {
		if sess != nil && sess.Fingerprint.FingerprintID != "" {
			fp = sess.Fingerprint
		} else {
			fp = antigravity.GenerateNodeFingerprint(provider)
		}
	}

	if len(credMap) > 0 || code != "" || strings.TrimSpace(in.APIKey) != "" {
		if _, hasType := credMap["type"]; !hasType {
			credMap["type"] = provider
		}
		if _, hasProv := credMap["provider"]; !hasProv {
			credMap["provider"] = provider
		}
		if email := firstNonEmptyService(strings.TrimSpace(in.Email), parsedCallback.Email, extractExtraString(credMap, "email")); email != "" {
			credMap["email"] = email
		}
		if pid := firstNonEmptyService(strings.TrimSpace(in.ProjectID), parsedCallback.ProjectID, extractExtraString(credMap, "project_id")); pid != "" {
			credMap["project_id"] = pid
		}
		if code != "" && extractExtraString(credMap, "authorization_code") == "" {
			credMap["authorization_code"] = code
		}
		if apiKey := strings.TrimSpace(in.APIKey); apiKey != "" && extractExtraString(credMap, "access_token") == "" {
			credMap["access_token"] = apiKey
		}
		if state != "" && extractExtraString(credMap, "state") == "" {
			credMap["state"] = state
		}
		if sess != nil && sess.CodeVerifier != "" && extractExtraString(credMap, "code_verifier") == "" {
			credMap["code_verifier"] = sess.CodeVerifier
		}
		redirectURI := strings.TrimSpace(in.RedirectURI)
		if redirectURI == "" && sess != nil {
			redirectURI = sess.RedirectURI
		}
		if redirectURI != "" {
			credMap["redirect_uri"] = redirectURI
		}
		if resolvedProxy != "" {
			credMap["proxy_url"] = resolvedProxy
		}
		credMap["fingerprint"] = fp
		credMap["source"] = "cmd2api_oauth_hub"
		credMap["authorized_at"] = time.Now().UTC().Format(time.RFC3339)

		jsonBytes, err := json.MarshalIndent(credMap, "", "  ")
		if err != nil {
			return nil, err
		}

		filename := strings.TrimSpace(in.Filename)
		if filename == "" {
			tag := extractExtraString(credMap, "email")
			if tag == "" {
				tag = fp.FingerprintID
			}
			filename = fmt.Sprintf("%s-%s-%d.json", sanitizeOAuthFileSlug(provider), sanitizeOAuthFileSlug(tag), time.Now().Unix()%100000)
		}

		// 分支 A：Account-First「1 账号 = 1 独立容器」自动入池 + 自动创建并启动专属容器
		if useDedicatedAutoContainer {
			if normConv, nErr := antigravity.NormalizeSingleCredential(filename, jsonBytes, resolvedProxy); nErr == nil && normConv.Valid {
				normConv.RawProxy = resolvedProxy
				normConv.Proxy = workbuddy.RedactProxyURL(resolvedProxy)
				savedAcc, nodeView, uErr := s.antigravity.UpsertSingleAGYAccountWithContainer(ctx, normConv, groupIDs, true)
				if uErr != nil {
					return nil, fmt.Errorf("创建 AGY 账号与专属容器失败: %w", uErr)
				}
				var outNodeID int64
				var containerMsg string
				if nodeView != nil {
					outNodeID = nodeView.ID
					containerMsg = fmt.Sprintf("，已自动绑定专属容器 %s（状态: %s）", nodeView.ContainerName, nodeView.Status)
				}
				return &OAuthCallbackResult{
					OK:           true,
					Platform:     domain.PlatformAntigravity,
					Provider:     provider,
					NodeID:       outNodeID,
					Node:         nodeView,
					Filename:     normConv.AuthFilename,
					Account:      savedAcc,
					RemoteResult: remoteResult,
					Message:      fmt.Sprintf("Google OAuth 授权成功：%s 已入池%s", savedAcc.Name, containerMsg),
				}, nil
			}
			// 兜底（例如离线合成测试 code）：先建独立节点再落盘
			freePort, fpErr := s.antigravity.AllocateFreeHostPort(ctx)
			if fpErr != nil {
				return nil, fpErr
			}
			strictIso := resolvedProxy != ""
			autoStart := s.antigravity.DetectEnv(ctx).DockerAvailable
			slug := sanitizeOAuthFileSlug(firstNonEmptyService(extractExtraString(credMap, "email"), in.AccountName, provider))
			newNode, cErr := s.antigravity.CreateNode(ctx, CreateContainerNodeInput{
				Name:            fmt.Sprintf("agy-oauth-%s-%d", slug, freePort),
				Mode:            domain.ContainerModeLocalDocker,
				HostIP:          antigravity.DefaultHostIP,
				HostPort:        freePort,
				ProxyURL:        resolvedProxy,
				StrictIsolation: &strictIso,
				AutoStart:       autoStart,
				Extra: map[string]any{
					"single_account":   true,
					"auto_provisioned": true,
				},
			})
			if cErr != nil {
				return nil, fmt.Errorf("自动分配独立 agy 容器失败: %w", cErr)
			}
			nodeID = newNode.ID
		}

		upOut, err := s.antigravity.UploadAuthFile(ctx, nodeID, filename, jsonBytes, true, groupIDs)
		if err != nil {
			return nil, fmt.Errorf("写入 OAuth .json 凭证并同步账号池失败: %w", err)
		}
		// 若该本机节点尚未启动且 Docker 可用，落盘凭证后顺手启动该专属容器
		nodeView, _ := s.antigravity.GetNode(ctx, nodeID)
		if nodeView != nil && nodeView.Mode == domain.ContainerModeLocalDocker && nodeView.Status != "running" && s.antigravity.DetectEnv(ctx).DockerAvailable {
			if actOut, aErr := s.antigravity.RunNodeAction(ctx, nodeID, "start"); aErr == nil && actOut != nil && actOut.Node != nil {
				nodeView = actOut.Node
			}
		}

		return &OAuthCallbackResult{
			OK:           true,
			Platform:     domain.PlatformAntigravity,
			Provider:     provider,
			NodeID:       nodeID,
			Node:         nodeView,
			Filename:     upOut.File.Filename,
			AuthFile:     &upOut.File,
			Account:      upOut.Account,
			RemoteResult: remoteResult,
			Message:      fmt.Sprintf("OAuth 授权完成：已落盘 %s 并自动入池（容器指纹 %s）", upOut.File.Filename, fp.FingerprintID),
		}, nil
	}

	// 容器远端回调已直接写入远程磁盘的情况：直接触发一次全量同步入池
	syncOut, syncErr := s.antigravity.SyncNodeAccounts(ctx, nodeID, groupIDs)
	if syncErr != nil {
		return nil, syncErr
	}
	var firstAcc *ent.Account
	if len(syncOut.Accounts) > 0 {
		firstAcc = syncOut.Accounts[0]
	}
	nodeView, _ := s.antigravity.GetNode(ctx, nodeID)
	return &OAuthCallbackResult{
		OK:           true,
		Platform:     domain.PlatformAntigravity,
		Provider:     provider,
		NodeID:       nodeID,
		Node:         nodeView,
		Account:      firstAcc,
		Accounts:     syncOut.Accounts,
		RemoteResult: remoteResult,
		Message:      fmt.Sprintf("容器 OAuth 回调成功，已同步 %d 个账号入池", syncOut.Total),
	}, nil
}

func extractCallbackBaseRedirectURI(rawCallback string) string {
	trimmed := strings.TrimSpace(rawCallback)
	if trimmed == "" || (!strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://")) {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (s *OAuthService) completeCommandCodeCallback(
	ctx context.Context,
	sess *OAuthSession,
	groupIDs []int64,
	proxyURL string,
	in CompleteOAuthCallbackInput,
) (*OAuthCallbackResult, error) {
	if s.accounts == nil {
		return nil, errors.New("账号服务未初始化")
	}

	rawInput := firstNonEmptyService(strings.TrimSpace(in.CallbackURL), strings.TrimSpace(in.APIKey), strings.TrimSpace(in.Code))
	parsed, err := relay.ParseCommandCodeCallback(rawInput)
	if err != nil {
		return nil, err
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("Command Code OAuth 拒绝 (%s): %s", parsed.Error, parsed.ErrorDescription)
	}
	apiKey := strings.TrimSpace(parsed.APIKey)
	if apiKey == "" {
		return nil, errors.New("未能从 Command Code 回调中提取有效 apiKey")
	}

	if proxyURL != "" {
		normP, pErr := workbuddy.NormalizeProxyURL(proxyURL)
		if pErr != nil {
			return nil, fmt.Errorf("Command Code 代理无效: %w", pErr)
		}
		proxyURL = normP
	}

	accName := strings.TrimSpace(in.AccountName)
	if s.relayClient != nil {
		wCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		who, wErr := s.relayClient.FetchWhoamiWithProxy(wCtx, apiKey, proxyURL)
		cancel()
		if wErr == nil && accName == "" {
			accName = firstNonEmptyService(strings.TrimSpace(who.UserName), strings.TrimSpace(who.Name))
		}
	}
	if accName == "" {
		accName = "CommandCode · " + time.Now().Format("0102-1504")
	}

	targetGroups, gErr := s.resolveOrEnsurePlatformGroup(ctx, domain.PlatformCommandCode, groupIDs)
	if gErr != nil {
		return nil, gErr
	}

	qimei, mid := workbuddy.GenerateDevice()
	created, err := s.accounts.Create(ctx, CreateAccountInput{
		Name:        accName,
		Platform:    domain.PlatformCommandCode,
		APIKey:      apiKey,
		ProxyURL:    proxyURL,
		Qimei36:     qimei,
		MachineID:   mid,
		GroupIDs:    targetGroups,
		Concurrency: 5,
		Priority:    50,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 Command Code 账号入池失败: %w", err)
	}

	return &OAuthCallbackResult{
		OK:       true,
		Platform: domain.PlatformCommandCode,
		Provider: domain.PlatformCommandCode,
		Account:  created,
		Message:  fmt.Sprintf("Command Code OAuth 入池成功：%s（分组 #%d）", created.Name, targetGroups[0]),
	}, nil
}

func (s *OAuthService) completeWorkBuddyCallback(
	ctx context.Context,
	sess *OAuthSession,
	groupIDs []int64,
	proxyURL string,
	in CompleteOAuthCallbackInput,
) (*OAuthCallbackResult, error) {
	if s.accounts == nil {
		return nil, errors.New("账号服务未初始化")
	}

	var payloadMap map[string]any
	rawJSON := firstNonEmptyService(strings.TrimSpace(string(in.TokenJSON)), strings.TrimSpace(in.RawJSON))
	if rawJSON != "" && rawJSON != "null" {
		if err := json.Unmarshal([]byte(rawJSON), &payloadMap); err != nil {
			return nil, fmt.Errorf("解析 WorkBuddy Token JSON 失败: %w", err)
		}
	}

	parsedCallback, _ := extractOAuthURLParts(in.CallbackURL)
	if parsedCallback.Error != "" {
		return nil, fmt.Errorf("WorkBuddy OAuth 拒绝 (%s): %s", parsedCallback.Error, parsedCallback.ErrorDescription)
	}

	accessToken := firstNonEmptyService(
		strings.TrimSpace(in.APIKey),
		strings.TrimSpace(in.Code),
		parsedCallback.APIKey,
		parsedCallback.AccessToken,
		parsedCallback.Code,
		extractExtraString(payloadMap, "access_token"),
		extractExtraString(payloadMap, "api_key"),
		extractExtraString(payloadMap, "token"),
	)
	if accessToken == "" {
		return nil, errors.New("WorkBuddy 回调缺少 access_token / api_key")
	}

	if proxyURL != "" {
		normP, pErr := workbuddy.NormalizeProxyURL(proxyURL)
		if pErr != nil {
			return nil, fmt.Errorf("WorkBuddy 代理地址无效: %w", pErr)
		}
		proxyURL = normP
	}

	refreshToken := firstNonEmptyService(strings.TrimSpace(in.RefreshToken), parsedCallback.RefreshToken, extractExtraString(payloadMap, "refresh_token"))
	wbDomain := firstNonEmptyService(strings.TrimSpace(in.Domain), extractExtraString(payloadMap, "domain"), domain.WorkBuddyDefaultDomain)
	email := firstNonEmptyService(strings.TrimSpace(in.Email), parsedCallback.Email, extractExtraString(payloadMap, "email"))
	uid := firstNonEmptyService(strings.TrimSpace(in.UID), parsedCallback.UID, extractExtraString(payloadMap, "uid"))
	nick := firstNonEmptyService(strings.TrimSpace(in.Nickname), extractExtraString(payloadMap, "nickname"), extractExtraString(payloadMap, "name"))

	accName := firstNonEmptyService(strings.TrimSpace(in.AccountName), email, nick, "WorkBuddy · "+time.Now().Format("0102-1504"))
	targetGroups, gErr := s.resolveOrEnsurePlatformGroup(ctx, domain.PlatformWorkBuddy, groupIDs)
	if gErr != nil {
		return nil, gErr
	}

	qimei, mid := workbuddy.GenerateDevice()
	created, err := s.accounts.Create(ctx, CreateAccountInput{
		Name:         accName,
		Platform:     domain.PlatformWorkBuddy,
		APIKey:       accessToken,
		RefreshToken: refreshToken,
		UID:          uid,
		Nickname:     nick,
		Email:        email,
		Domain:       wbDomain,
		ProxyURL:     proxyURL,
		Qimei36:      qimei,
		MachineID:    mid,
		GroupIDs:     targetGroups,
		Concurrency:  5,
		Priority:     50,
	})
	if err != nil {
		return nil, fmt.Errorf("WorkBuddy OAuth 账号入池失败: %w", err)
	}

	return &OAuthCallbackResult{
		OK:       true,
		Platform: domain.PlatformWorkBuddy,
		Provider: domain.PlatformWorkBuddy,
		Account:  created,
		Message:  fmt.Sprintf("WorkBuddy 授权入池成功：%s（独立指纹 %s）", created.Name, qimei[:10]),
	}, nil
}

func (s *OAuthService) completeOpenCodeCallback(
	ctx context.Context,
	sess *OAuthSession,
	groupIDs []int64,
	proxyURL string,
	in CompleteOAuthCallbackInput,
) (*OAuthCallbackResult, error) {
	if s.accounts == nil {
		return nil, errors.New("账号服务未初始化")
	}

	var payloadMap map[string]any
	rawJSON := firstNonEmptyService(strings.TrimSpace(string(in.TokenJSON)), strings.TrimSpace(in.RawJSON))
	if rawJSON != "" && rawJSON != "null" {
		if err := json.Unmarshal([]byte(rawJSON), &payloadMap); err != nil {
			return nil, fmt.Errorf("解析 OpenCode OAuth JSON 失败: %w", err)
		}
	}

	parsedCallback, _ := extractOAuthURLParts(in.CallbackURL)
	if parsedCallback.Error != "" {
		return nil, fmt.Errorf("OpenCode OAuth 拒绝 (%s): %s", parsedCallback.Error, parsedCallback.ErrorDescription)
	}

	apiKey := firstNonEmptyService(
		strings.TrimSpace(in.APIKey),
		strings.TrimSpace(in.Code),
		parsedCallback.APIKey,
		parsedCallback.AccessToken,
		parsedCallback.Code,
		extractExtraString(payloadMap, "api_key"),
		extractExtraString(payloadMap, "access_token"),
		extractExtraString(payloadMap, "token"),
		extractExtraString(payloadMap, "key"),
	)
	if apiKey == "" {
		return nil, errors.New("OpenCode OAuth 回调缺少 api_key / access_token")
	}

	if proxyURL != "" {
		normP, pErr := workbuddy.NormalizeProxyURL(proxyURL)
		if pErr != nil {
			return nil, fmt.Errorf("OpenCode 代理地址无效: %w", pErr)
		}
		proxyURL = normP
	}

	sessMode := ""
	sessBase := ""
	sessProv := ""
	if sess != nil {
		sessMode = sess.AccountMode
		sessBase = sess.BaseURL
		sessProv = sess.Provider
	}
	accountMode := normalizeOpenCodeAccountMode(
		firstNonEmptyService(
			strings.TrimSpace(in.AccountMode),
			extractExtraString(payloadMap, "account_mode"),
			extractExtraString(payloadMap, "mode"),
			parsedCallback.Mode,
			sessMode,
		),
		firstNonEmptyService(strings.TrimSpace(in.Provider), sessProv),
	)
	baseURL := firstNonEmptyService(
		strings.TrimSpace(in.BaseURL),
		extractExtraString(payloadMap, "base_url"),
		sessBase,
		domain.DefaultOpenCodeBaseURL(accountMode),
	)
	email := firstNonEmptyService(strings.TrimSpace(in.Email), parsedCallback.Email, extractExtraString(payloadMap, "email"))
	accName := firstNonEmptyService(
		strings.TrimSpace(in.AccountName),
		extractExtraString(payloadMap, "name"),
		email,
		fmt.Sprintf("OpenCode-%s · %s", strings.ToUpper(accountMode), time.Now().Format("0102-1504")),
	)

	targetGroups, gErr := s.resolveOrEnsurePlatformGroup(ctx, domain.PlatformOpenCode, groupIDs)
	if gErr != nil {
		return nil, gErr
	}

	qimei, mid := workbuddy.GenerateDevice()
	created, err := s.accounts.Create(ctx, CreateAccountInput{
		Name:        accName,
		Platform:    domain.PlatformOpenCode,
		AccountMode: accountMode,
		BaseURL:     baseURL,
		APIKey:      apiKey,
		Email:       email,
		ProxyURL:    proxyURL,
		Qimei36:     qimei,
		MachineID:   mid,
		GroupIDs:    targetGroups,
		Concurrency: 5,
		Priority:    50,
	})
	if err != nil {
		return nil, fmt.Errorf("OpenCode OAuth 账号入池失败: %w", err)
	}

	return &OAuthCallbackResult{
		OK:       true,
		Platform: domain.PlatformOpenCode,
		Provider: "opencode-" + accountMode,
		Account:  created,
		Message:  fmt.Sprintf("OpenCode (%s) OAuth 入池成功：%s（分组 #%d）", strings.ToUpper(accountMode), created.Name, targetGroups[0]),
	}, nil
}

func (s *OAuthService) resolveOrEnsurePlatformGroup(ctx context.Context, platform string, groupIDs []int64) ([]int64, error) {
	ids := uniqueIDs(groupIDs)
	if len(ids) > 0 {
		return ids, nil
	}
	rows, err := s.db.Group.Query().
		Where(group.DeletedAtIsNil(), group.PlatformEQ(platform)).
		Order(ent.Asc(group.FieldID)).
		All(ctx)
	if err == nil && len(rows) > 0 {
		return []int64{rows[0].ID}, nil
	}
	name := platform
	if exists, _ := s.db.Group.Query().Where(group.NameEQ(name)).Exist(ctx); exists {
		name = fmt.Sprintf("%s-%d", platform, time.Now().Unix()%10000)
	}
	createBuilder := s.db.Group.Create().
		SetName(name).
		SetDescription(platform + " OAuth 默认调度分组").
		SetPlatform(platform).
		SetRateMultiplier(1.0).
		SetStatus(domain.StatusActive)
	if platform == domain.PlatformAntigravity {
		createBuilder = createBuilder.SetPromptInjectMode(domain.PromptModeOff)
	}
	created, cErr := createBuilder.Save(ctx)
	if cErr != nil {
		return nil, fmt.Errorf("自动创建 %s 默认分组失败: %w", platform, cErr)
	}
	// 确保 accountgroup 表导入使用无冗余问题
	_ = account.FieldID
	_ = accountgroup.FieldGroupID
	return []int64{created.ID}, nil
}

func (s *OAuthService) storeSession(sess *OAuthSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) > 500 {
		now := time.Now().UTC()
		for k, v := range s.sessions {
			if now.After(v.ExpiresAt) {
				delete(s.byState, v.State)
				delete(s.sessions, k)
			}
		}
	}
	s.sessions[sess.SessionID] = sess
	if sess.State != "" {
		s.byState[sess.State] = sess.SessionID
	}
}

func (s *OAuthService) lookupSession(sessionID, state string) *OAuthSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sid := strings.TrimSpace(sessionID); sid != "" {
		if v, ok := s.sessions[sid]; ok {
			return v
		}
	}
	if st := strings.TrimSpace(state); st != "" {
		if sid, ok := s.byState[st]; ok {
			if v, ok2 := s.sessions[sid]; ok2 {
				return v
			}
		}
	}
	return nil
}

func cloneOAuthSession(sess *OAuthSession) *OAuthSession {
	if sess == nil {
		return nil
	}
	cp := *sess
	cp.CodeVerifier = ""
	if len(sess.GroupIDs) > 0 {
		cp.GroupIDs = append([]int64(nil), sess.GroupIDs...)
	}
	return &cp
}

func normalizeOAuthPlatformAndProvider(rawPlat, rawProv string, nodeID int64) (string, string) {
	prov := strings.ToLower(strings.TrimSpace(rawProv))
	plat := strings.ToLower(strings.TrimSpace(rawPlat))
	if plat == "" && nodeID > 0 {
		plat = domain.PlatformAntigravity
	}
	switch prov {
	case "antigravity", "agy", "gemini-cli", "gemini", "claude", "codex":
		if plat == "" {
			plat = domain.PlatformAntigravity
		}
	case "commandcode", "command-code", "command_code", "cc", "cmdc":
		plat = domain.PlatformCommandCode
		prov = domain.PlatformCommandCode
	case "workbuddy", "wb", "codebuddy":
		plat = domain.PlatformWorkBuddy
		prov = domain.PlatformWorkBuddy
	case "opencode", "open-code", "open_code", "oc", "zen", "go":
		plat = domain.PlatformOpenCode
	}
	if plat == "" {
		plat = domain.PlatformAntigravity
	}
	plat = domain.NormalizePlatform(plat)
	if prov == "" {
		prov = plat
	}
	return plat, prov
}

func normalizeOpenCodeAccountMode(rawMode, rawProvider string) string {
	m := strings.ToLower(strings.TrimSpace(rawMode))
	if m == "" {
		m = strings.ToLower(strings.TrimSpace(rawProvider))
	}
	if m == domain.AccountModeGo || strings.HasSuffix(m, "-go") {
		return domain.AccountModeGo
	}
	return domain.AccountModeZen
}

func buildWorkBuddyOAuthURL(baseDomain, redirectURI, state, challenge string, redirectMode bool) string {
	base := workbuddy.NormalizeDomain(baseDomain)
	q := url.Values{}
	q.Set("client_id", "workbuddy-desktop")
	q.Set("response_type", "code")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if redirectMode && strings.TrimSpace(redirectURI) != "" {
		q.Set("redirect_uri", strings.TrimSpace(redirectURI))
	}
	return base + workbuddy.PathClientLogin + "?" + q.Encode()
}

func buildOpenCodeOAuthURL(accountMode, baseURL, redirectURI, state, challenge string, redirectMode bool) string {
	host := "https://opencode.ai"
	if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		host = parsed.Scheme + "://" + parsed.Host
	}
	q := url.Values{}
	q.Set("client_id", "cmd2api-opencode")
	q.Set("response_type", "code")
	q.Set("mode", accountMode)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if redirectMode && strings.TrimSpace(redirectURI) != "" {
		q.Set("redirect_uri", strings.TrimSpace(redirectURI))
	}
	return host + "/auth?" + q.Encode()
}

func buildProviderPKCEOAuthURL(provider, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")

	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		q.Set("scope", "org:create_api_key user:profile user:inference")
		q.Set("client_id", "cmd2api-claude-oauth")
		return "https://claude.ai/oauth/authorize?" + q.Encode()
	case "codex":
		q.Set("scope", "openid profile email offline_access")
		q.Set("client_id", "app_EMoamEEZ73f0CkXaXp7hrann")
		return "https://auth.openai.com/oauth/authorize?" + q.Encode()
	default:
		// antigravity / gemini-cli 使用官方 AntiGravity CLI OAuth 2.0 PKCE 端点与完整 Scopes
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
		q.Set("client_id", antigravity.OfficialOAuthClientID)
		q.Set("scope", antigravity.OfficialOAuthScopes)
		return antigravity.GoogleOAuthAuthorizeURL + "?" + q.Encode()
	}
}

type parsedOAuthCallbackURL struct {
	Code             string
	State            string
	APIKey           string
	AccessToken      string
	RefreshToken     string
	UID              string
	Mode             string
	Email            string
	ProjectID        string
	Error            string
	ErrorDescription string
}

func extractOAuthURLParts(raw string) (parsedOAuthCallbackURL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return parsedOAuthCallbackURL{}, nil
	}
	if idx := strings.IndexAny(trimmed, "\r\n"); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}

	var values url.Values
	if qIdx := strings.IndexByte(trimmed, '?'); qIdx >= 0 {
		qPart := trimmed[qIdx+1:]
		if hIdx := strings.IndexByte(qPart, '#'); hIdx >= 0 {
			qPart = qPart[:hIdx]
		}
		values, _ = url.ParseQuery(qPart)
	}
	if hIdx := strings.IndexByte(trimmed, '#'); hIdx >= 0 {
		frag := trimmed[hIdx+1:]
		if fragVals, err := url.ParseQuery(frag); err == nil {
			if values == nil {
				values = fragVals
			} else {
				for k, v := range fragVals {
					if values.Get(k) == "" && len(v) > 0 {
						values.Set(k, v[0])
					}
				}
			}
		}
	}
	if values == nil {
		return parsedOAuthCallbackURL{}, errors.New("no query or fragment found")
	}
	return parsedOAuthCallbackURL{
		Code:             strings.TrimSpace(values.Get("code")),
		State:            strings.TrimSpace(values.Get("state")),
		APIKey:           firstNonEmptyService(values.Get("apiKey"), values.Get("api_key"), values.Get("token"), values.Get("key")),
		AccessToken:      strings.TrimSpace(values.Get("access_token")),
		RefreshToken:     strings.TrimSpace(values.Get("refresh_token")),
		UID:              strings.TrimSpace(values.Get("uid")),
		Mode:             strings.TrimSpace(values.Get("mode")),
		Email:            strings.TrimSpace(values.Get("email")),
		ProjectID:        strings.TrimSpace(values.Get("project_id")),
		Error:            strings.TrimSpace(values.Get("error")),
		ErrorDescription: strings.TrimSpace(values.Get("error_description")),
	}, nil
}

func randomOAuthHex(byteLen int) string {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(sum[:byteLen])
	}
	return hex.EncodeToString(b)
}

func randomOAuthPKCEVerifier() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sanitizeOAuthFileSlug(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "oauth"
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= 24 {
			break
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "oauth"
	}
	return out
}

func firstNonEmptyService(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
