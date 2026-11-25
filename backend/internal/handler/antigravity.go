package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

// GetAntigravityEnv 返回宿主机 Docker 环境就绪状态、默认镜像与挂载数据根目录（GET /api/antigravity/env）。
func (h *Handler) GetAntigravityEnv(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	c.JSON(http.StatusOK, svc.DetectEnv(c.Request.Context()))
}

// ListAntigravityNodes 返回全部本机 Docker / 外部远程容器节点列表（GET /api/antigravity/nodes）。
func (h *Handler) ListAntigravityNodes(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	items, err := svc.ListNodes(c.Request.Context())
	if err != nil {
		h.failInternal(c, "查询 AntiGravity 容器节点列表失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
	})
}

type containerNodePayloadRequest struct {
	Name            string         `json:"name"`
	Mode            string         `json:"mode"`
	DockerImage     string         `json:"docker_image"`
	ContainerName   string         `json:"container_name"`
	HostIP          string         `json:"host_ip"`
	HostPort        int            `json:"host_port"`
	AuthDir         string         `json:"auth_dir"`
	ManagementKey   string         `json:"management_key"`
	APIKey          string         `json:"api_key"`
	ProxyURL        string         `json:"proxy_url"`
	StrictIsolation *bool          `json:"strict_isolation"`
	AutoStart       bool           `json:"auto_start"`
	Extra           map[string]any `json:"extra"`
}

// CreateAntigravityNode 新建容器或远程节点（POST /api/antigravity/nodes）。
func (h *Handler) CreateAntigravityNode(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}

	var req containerNodePayloadRequest
	if !bindJSON(c, &req) {
		return
	}

	nodeView, err := svc.CreateNode(c.Request.Context(), service.CreateContainerNodeInput{
		Name:            req.Name,
		Mode:            req.Mode,
		DockerImage:     req.DockerImage,
		ContainerName:   req.ContainerName,
		HostIP:          req.HostIP,
		HostPort:        req.HostPort,
		AuthDir:         req.AuthDir,
		ManagementKey:   req.ManagementKey,
		APIKey:          req.APIKey,
		ProxyURL:        req.ProxyURL,
		StrictIsolation: req.StrictIsolation,
		AutoStart:       req.AutoStart,
		Extra:           req.Extra,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusCreated, nodeView)
}

type updateContainerNodeRequest struct {
	Name            *string        `json:"name"`
	Mode            *string        `json:"mode"`
	DockerImage     *string        `json:"docker_image"`
	ContainerName   *string        `json:"container_name"`
	HostIP          *string        `json:"host_ip"`
	HostPort        *int           `json:"host_port"`
	AuthDir         *string        `json:"auth_dir"`
	ManagementKey   *string        `json:"management_key"`
	APIKey          *string        `json:"api_key"`
	ProxyURL        *string        `json:"proxy_url"`
	StrictIsolation *bool          `json:"strict_isolation"`
	Extra           map[string]any `json:"extra"`
}

// UpdateAntigravityNode 更新节点参数（PUT /api/antigravity/nodes/:id）。
func (h *Handler) UpdateAntigravityNode(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req updateContainerNodeRequest
	if !bindJSON(c, &req) {
		return
	}

	updated, err := svc.UpdateNode(c.Request.Context(), id, service.UpdateContainerNodeInput{
		Name:            req.Name,
		Mode:            req.Mode,
		DockerImage:     req.DockerImage,
		ContainerName:   req.ContainerName,
		HostIP:          req.HostIP,
		HostPort:        req.HostPort,
		AuthDir:         req.AuthDir,
		ManagementKey:   req.ManagementKey,
		APIKey:          req.APIKey,
		ProxyURL:        req.ProxyURL,
		StrictIsolation: req.StrictIsolation,
		Extra:           req.Extra,
	})
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, updated)
}

// DeleteAntigravityNode 删除容器节点记录（DELETE /api/antigravity/nodes/:id?remove_container=true）。
func (h *Handler) DeleteAntigravityNode(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	removeContainer, _ := strconv.ParseBool(strings.TrimSpace(c.Query("remove_container")))
	if err := svc.DeleteNode(c.Request.Context(), id, removeContainer); err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		h.failInternal(c, "删除容器节点失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "已删除"})
}

// RunAntigravityNodeAction 执行节点运维动作（POST /api/antigravity/nodes/:id/action）。
func (h *Handler) RunAntigravityNodeAction(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req struct {
		Action string `json:"action"`
	}
	if !bindJSON(c, &req) {
		return
	}

	res, err := svc.RunNodeAction(c.Request.Context(), id, req.Action)
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// GetAntigravityNodeLogs 读取容器实时日志（GET /api/antigravity/nodes/:id/logs?tail=200）。
func (h *Handler) GetAntigravityNodeLogs(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	tail := queryInt(c, "tail", 200)
	res, err := svc.GetNodeLogs(c.Request.Context(), id, tail)
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// ListAntigravityAuthFiles 列出节点下的 OAuth .json 登录凭证（GET /api/antigravity/nodes/:id/auth-files）。
func (h *Handler) ListAntigravityAuthFiles(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	items, err := svc.ListAuthFiles(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
	})
}

type uploadAntigravityAuthFileRequest struct {
	Filename    string          `json:"filename"`
	Content     json.RawMessage `json:"content"`
	RawJSON     string          `json:"raw_json"`
	SyncAccount *bool           `json:"sync_account"`
	AutoSync    *bool           `json:"auto_sync"`
	GroupIDs    []int64         `json:"group_ids"`
}

// UploadAntigravityAuthFile 上传或导入 .json 登录文件（兼容 content 为字符串或 JSON 对象）（POST /api/antigravity/nodes/:id/auth-files）。
func (h *Handler) UploadAntigravityAuthFile(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req uploadAntigravityAuthFileRequest
	if !bindJSON(c, &req) {
		return
	}

	rawBytes, err := decodeFlexibleAuthFileContent(req.Content, req.RawJSON)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	autoSync := false
	if req.SyncAccount != nil {
		autoSync = *req.SyncAccount
	} else if req.AutoSync != nil {
		autoSync = *req.AutoSync
	}

	ctx := c.Request.Context()
	res, err := svc.UploadAuthFile(ctx, id, req.Filename, rawBytes, autoSync, req.GroupIDs)
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	if autoSync {
		if ps := h.promptSvc(); ps != nil {
			_ = ps.ReloadEngine(ctx)
		}
	}

	var accDTO *accountDTO
	if res.Account != nil {
		dto := h.toAccountDTO(res.Account)
		accDTO = &dto
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      res.OK,
		"file":    res.File,
		"item":    res.Item,
		"account": accDTO,
	})
}

func decodeFlexibleAuthFileContent(content json.RawMessage, rawJSON string) ([]byte, error) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if trimmed[0] == '"' {
			var unquoted string
			if err := json.Unmarshal(trimmed, &unquoted); err != nil {
				return nil, errors.New("content 字符串解析失败: " + err.Error())
			}
			return []byte(strings.TrimSpace(unquoted)), nil
		}
		return trimmed, nil
	}
	if strings.TrimSpace(rawJSON) != "" {
		return []byte(strings.TrimSpace(rawJSON)), nil
	}
	return nil, errors.New("登录凭证 JSON 内容不能为空")
}

// DeleteAntigravityAuthFile 删除节点下的指定 .json 登录凭证文件（DELETE /api/antigravity/nodes/:id/auth-files/:filename）。
func (h *Handler) DeleteAntigravityAuthFile(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	filename := strings.TrimSpace(c.Param("filename"))
	if filename == "" {
		fail(c, http.StatusBadRequest, "文件名不能为空")
		return
	}

	if err := svc.DeleteAuthFile(c.Request.Context(), id, filename); err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "已删除"})
}

// RequestAntigravityOAuthURL 请求容器在线或网关 PKCE OAuth 授权地址（POST /api/antigravity/nodes/:id/oauth-url）。
func (h *Handler) RequestAntigravityOAuthURL(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req struct {
		Provider    string  `json:"provider"`
		RedirectURI string  `json:"redirect_uri"`
		GroupIDs    []int64 `json:"group_ids"`
	}
	if c.Request.ContentLength != 0 {
		if !bindJSON(c, &req) {
			return
		}
	}

	ctx := c.Request.Context()
	if os := h.oauthSvc(); os != nil {
		redirectURI := strings.TrimSpace(req.RedirectURI)
		if redirectURI == "" {
			redirectURI = inferExternalOAuthCallbackURL(c)
		}
		sess, err := os.CreateSession(ctx, service.CreateOAuthSessionInput{
			Platform:    "antigravity",
			Provider:    req.Provider,
			NodeID:      id,
			GroupIDs:    req.GroupIDs,
			RedirectURI: redirectURI,
		})
		if err != nil {
			if errors.Is(err, service.ErrContainerNodeNotFound) {
				fail(c, http.StatusNotFound, err.Error())
				return
			}
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"session_id":   sess.SessionID,
			"auth_url":     sess.AuthURL,
			"url":          sess.AuthURL,
			"state":        sess.State,
			"provider":     sess.Provider,
			"mode":         sess.Mode,
			"callback_url": sess.RedirectURI,
			"fingerprint":  sess.Fingerprint,
			"instructions": sess.Instructions,
		})
		return
	}

	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	res, err := svc.RequestOAuthURL(ctx, id, req.Provider, req.RedirectURI)
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"auth_url":     res.AuthURL,
		"url":          res.AuthURL,
		"state":        res.State,
		"provider":     res.Provider,
		"instructions": res.Instructions,
	})
}

// CompleteAntigravityOAuthCallback 提交 AntiGravity 节点的 OAuth 回调 URL/Code/TokenJSON 并自动落盘入池
// （POST /api/antigravity/nodes/:id/oauth-callback）。
func (h *Handler) CompleteAntigravityOAuthCallback(c *gin.Context) {
	os := h.oauthSvc()
	if os == nil {
		fail(c, http.StatusServiceUnavailable, "OAuth 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req completeOAuthCallbackRequest
	if !bindJSON(c, &req) {
		return
	}
	cbURL := strings.TrimSpace(req.CallbackURL)
	if cbURL == "" {
		cbURL = strings.TrimSpace(req.RedirectURL)
	}

	res, err := os.CompleteCallback(c.Request.Context(), service.CompleteOAuthCallbackInput{
		SessionID:   req.SessionID,
		State:       req.State,
		Platform:    "antigravity",
		Provider:    req.Provider,
		NodeID:      id,
		GroupIDs:    req.GroupIDs,
		ProxyURL:    req.ProxyURL,
		CallbackURL: cbURL,
		RedirectURI: req.RedirectURI,
		Code:        req.Code,
		APIKey:      req.APIKey,
		Email:       req.Email,
		ProjectID:   req.ProjectID,
		AccountName: req.AccountName,
		Filename:    req.Filename,
		TokenJSON:   req.TokenJSON,
		RawJSON:     req.RawJSON,
	})
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if h.sched != nil {
		h.sched.Invalidate()
	}
	h.respondOAuthCallbackJSON(c, res)
}

type syncAntigravityAccountsRequest struct {
	GroupIDs       []int64 `json:"group_ids"`
	ProxyURL       string  `json:"proxy_url"`
	Concurrency    int     `json:"concurrency"`
	Priority       int     `json:"priority"`
	RateMultiplier float64 `json:"rate_multiplier"`
}

// SyncAntigravityAccounts 一键将节点凭证同步至 antigravity 调度账号池（POST /api/antigravity/nodes/:id/sync-accounts）。
func (h *Handler) SyncAntigravityAccounts(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req syncAntigravityAccountsRequest
	if c.Request.ContentLength != 0 {
		if !bindJSON(c, &req) {
			return
		}
	}

	ctx := c.Request.Context()
	out, err := svc.SyncNodeAccountsWithOptions(ctx, id, service.SyncNodeAccountsOptions{
		GroupIDs:       req.GroupIDs,
		ProxyURL:       req.ProxyURL,
		Concurrency:    req.Concurrency,
		Priority:       req.Priority,
		RateMultiplier: req.RateMultiplier,
	})
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	if ps := h.promptSvc(); ps != nil {
		_ = ps.ReloadEngine(ctx)
	}

	accDTOs := make([]accountDTO, 0, len(out.Accounts))
	for _, a := range out.Accounts {
		accDTOs = append(accDTOs, h.toAccountDTO(a))
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":          out.OK,
		"created":     out.Created,
		"updated":     out.Updated,
		"total":       out.Total,
		"files_count": out.FilesCount,
		"group_id":    out.GroupID,
		"accounts":    accDTOs,
		"node":        out.Node,
		"errors":      out.Errors,
	})
}

// RotateAntigravityFingerprint 一键轮换容器节点隔离设备指纹并同步关联账号（POST /api/antigravity/nodes/:id/rotate-fingerprint）。
func (h *Handler) RotateAntigravityFingerprint(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	view, err := svc.RotateNodeFingerprint(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		h.failInternal(c, "轮换节点设备指纹失败", err)
		return
	}
	if h.sched != nil {
		h.sched.Invalidate()
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"node":    view,
		"message": "容器设备指纹已轮换，关联调度账号已同步",
	})
}

// CheckAntigravityNodeExitIP 探测容器节点独立代理出口 IP 并检测跨容器 IP 碰撞（POST /api/antigravity/nodes/:id/check-ip）。
func (h *Handler) CheckAntigravityNodeExitIP(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	res, err := svc.CheckNodeExitIP(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// ProvisionAntigravityAccountContainer 为指定 AntiGravity 账号自动分配/拉起专属 1:1 独立 agy 容器
// （POST /api/antigravity/accounts/:id/provision-container）。
func (h *Handler) ProvisionAntigravityAccountContainer(c *gin.Context) {
	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req struct {
		ProxyURL        string `json:"proxy_url"`
		DockerImage     string `json:"docker_image"`
		StrictIsolation *bool  `json:"strict_isolation"`
		AutoStart       *bool  `json:"auto_start"`
	}
	if c.Request.ContentLength != 0 {
		if !bindJSON(c, &req) {
			return
		}
	}
	autoStart := true
	if req.AutoStart != nil {
		autoStart = *req.AutoStart
	}
	nodeView, updatedAcc, err := svc.ProvisionAccountContainer(c.Request.Context(), service.ProvisionAccountContainerInput{
		AccountID:       id,
		ProxyURL:        req.ProxyURL,
		DockerImage:     req.DockerImage,
		StrictIsolation: req.StrictIsolation,
		AutoStart:       autoStart,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if h.sched != nil {
		h.sched.Invalidate()
	}
	var accDTO *accountDTO
	if updatedAcc != nil {
		d := h.toAccountDTO(updatedAcc)
		accDTO = &d
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"node":    nodeView,
		"account": accDTO,
	})
}
