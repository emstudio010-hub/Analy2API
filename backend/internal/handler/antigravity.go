package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/config"
	"cmd2api/internal/service"
	"cmd2api/internal/workbuddy"

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

type convertAntigravityFilePayload struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

type convertAntigravityAccountsRequest struct {
	RawText       string                          `json:"raw_text"`
	Filename      string                          `json:"filename"`
	Files         []convertAntigravityFilePayload `json:"files"`
	AutoImport    bool                            `json:"auto_import"`
	NodeID        int64                           `json:"node_id"`
	GroupIDs      []int64                         `json:"group_ids"`
	ProxyURL      string                          `json:"proxy_url"`
	AutoProvision *bool                           `json:"auto_provision"`
	Deduplicate   *bool                           `json:"deduplicate"`
}

// ConvertAntigravityAccounts 全格式多账号自动识别并转为真实 `agy` CLI 原生格式，支持一键导入号池/容器（POST /api/antigravity/accounts/convert）。
// 同时接受 `multipart/form-data`（`files` / `files[]`）与 `application/json`。
func (h *Handler) ConvertAntigravityAccounts(c *gin.Context) {
	in, err := parseConvertAntigravityRequest(c)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	ctx := c.Request.Context()
	if !in.AutoImport {
		// 纯解析预览与下载凭证包不强依赖数据库节点（即使无 DB 也可离线测试转换）
		batch, convErr := antigravity.ConvertCredentialsToAGY(in.Files, antigravity.ConvertOptions{
			DefaultProxy: in.ProxyURL,
			Deduplicate:  in.Deduplicate,
		})
		if convErr != nil {
			fail(c, http.StatusBadRequest, convErr.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"ok":                batch.OK,
			"total":             batch.Total,
			"valid_count":       batch.ValidCount,
			"invalid_count":     batch.InvalidCount,
			"duplicate_count":   batch.DuplicateCount,
			"detected_formats":  batch.DetectedFormats,
			"summary":           batch.Summary,
			"accounts":          batch.Accounts,
			"items":             batch.Accounts,
			"bundle_filename":   batch.BundleFilename,
			"bundle_zip_base64": batch.BundleZipBase64,
			"bundle_json":       batch.BundleJSON,
		})
		return
	}

	svc := h.antigravitySvc()
	if svc == nil {
		fail(c, http.StatusServiceUnavailable, "AntiGravity 服务未初始化，无法执行自动入池")
		return
	}

	out, err := svc.ConvertAndImportAccounts(ctx, in)
	if err != nil {
		if errors.Is(err, service.ErrContainerNodeNotFound) {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	if out.ImportResult != nil && out.ImportResult.Imported > 0 {
		if h.sched != nil {
			h.sched.Invalidate()
		}
		if ps := h.promptSvc(); ps != nil {
			_ = ps.ReloadEngine(ctx)
		}
	}

	var importDTO gin.H
	if out.ImportResult != nil {
		dtos := make([]accountDTO, 0, len(out.ImportResult.Accounts))
		for _, a := range out.ImportResult.Accounts {
			dtos = append(dtos, h.toAccountDTO(a))
		}
		importDTO = gin.H{
			"imported":  out.ImportResult.Imported,
			"created":   out.ImportResult.Created,
			"updated":   out.ImportResult.Updated,
			"failed":    out.ImportResult.Failed,
			"node_id":   out.ImportResult.NodeID,
			"group_ids": out.ImportResult.GroupIDs,
			"accounts":  dtos,
			"errors":    out.ImportResult.Errors,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":                out.OK,
		"total":             out.Total,
		"valid_count":       out.ValidCount,
		"invalid_count":     out.InvalidCount,
		"duplicate_count":   out.DuplicateCount,
		"detected_formats":  out.DetectedFormats,
		"summary":           out.Summary,
		"accounts":          out.Accounts,
		"items":             out.Accounts,
		"bundle_filename":   out.BundleFilename,
		"bundle_zip_base64": out.BundleZipBase64,
		"bundle_json":       out.BundleJSON,
		"import_result":     importDTO,
	})
}

func parseConvertAntigravityRequest(c *gin.Context) (service.ConvertAccountsInput, error) {
	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(40 << 20); err != nil {
			return service.ConvertAccountsInput{}, fmt.Errorf("解析 multipart 表单失败: %w", err)
		}
		form := c.Request.MultipartForm
		var files []antigravity.AdapterInputFile
		if form != nil {
			var fileHeaders []*multipart.FileHeader
			fileHeaders = append(fileHeaders, form.File["files"]...)
			fileHeaders = append(fileHeaders, form.File["files[]"]...)
			fileHeaders = append(fileHeaders, form.File["file"]...)
			for _, fh := range fileHeaders {
				f, openErr := fh.Open()
				if openErr != nil {
					return service.ConvertAccountsInput{}, fmt.Errorf("读取上传文件 %q 失败: %w", fh.Filename, openErr)
				}
				raw, readErr := io.ReadAll(io.LimitReader(f, antigravity.MaxZipCompressedBytes+1))
				_ = f.Close()
				if readErr != nil {
					return service.ConvertAccountsInput{}, fmt.Errorf("读取上传文件 %q 内容失败: %w", fh.Filename, readErr)
				}
				if len(raw) > antigravity.MaxZipCompressedBytes {
					return service.ConvertAccountsInput{}, fmt.Errorf("上传文件 %q 超过 32MB 限制", fh.Filename)
				}
				files = append(files, antigravity.AdapterInputFile{
					Filename: fh.Filename,
					Content:  raw,
				})
			}
		}
		rawText := strings.TrimSpace(c.PostForm("raw_text"))
		if rawText != "" {
			hintName := strings.TrimSpace(c.PostForm("filename"))
			if hintName == "" {
				hintName = "pasted-input.txt"
			}
			files = append(files, antigravity.AdapterInputFile{
				Filename: hintName,
				Content:  []byte(rawText),
			})
		}
		if len(files) == 0 {
			return service.ConvertAccountsInput{}, errors.New("请至少上传一个凭证文件或粘贴凭证文本")
		}

		autoImport, _ := strconv.ParseBool(strings.TrimSpace(c.PostForm("auto_import")))
		nodeID, _ := strconv.ParseInt(strings.TrimSpace(c.PostForm("node_id")), 10, 64)
		groupIDs := parseFlexibleGroupIDsForm(c.PostFormArray("group_ids"), c.PostFormArray("group_ids[]"))
		proxyURL := strings.TrimSpace(c.PostForm("proxy_url"))

		var autoProvPtr *bool
		if rawAP := strings.TrimSpace(c.PostForm("auto_provision")); rawAP != "" {
			b, bErr := strconv.ParseBool(rawAP)
			if bErr == nil {
				autoProvPtr = &b
			}
		}
		var dedupPtr *bool
		if rawDedup := strings.TrimSpace(c.PostForm("deduplicate")); rawDedup != "" {
			b, bErr := strconv.ParseBool(rawDedup)
			if bErr == nil {
				dedupPtr = &b
			}
		}

		return service.ConvertAccountsInput{
			Files:         files,
			AutoImport:    autoImport,
			NodeID:        nodeID,
			GroupIDs:      groupIDs,
			ProxyURL:      proxyURL,
			AutoProvision: autoProvPtr,
			Deduplicate:   dedupPtr,
		}, nil
	}

	var req convertAntigravityAccountsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return service.ConvertAccountsInput{}, fmt.Errorf("请求体格式错误: %w", err)
	}

	var files []antigravity.AdapterInputFile
	for _, f := range req.Files {
		if strings.TrimSpace(f.Content) == "" {
			continue
		}
		// 支持前端将二进制文件（如 .zip）以 data URL 或 Base64 字符串放入 files[].content
		rawBytes := decodePossiblyDataURLContent(f.Filename, f.Content)
		files = append(files, antigravity.AdapterInputFile{
			Filename: f.Filename,
			Content:  rawBytes,
		})
	}
	if strings.TrimSpace(req.RawText) != "" {
		fname := strings.TrimSpace(req.Filename)
		if fname == "" {
			fname = "raw_text.txt"
		}
		files = append(files, antigravity.AdapterInputFile{
			Filename: fname,
			Content:  decodePossiblyDataURLContent(fname, req.RawText),
		})
	}
	if len(files) == 0 {
		return service.ConvertAccountsInput{}, errors.New("raw_text 与 files 不能为空")
	}

	return service.ConvertAccountsInput{
		Files:         files,
		AutoImport:    req.AutoImport,
		NodeID:        req.NodeID,
		GroupIDs:      req.GroupIDs,
		ProxyURL:      strings.TrimSpace(req.ProxyURL),
		AutoProvision: req.AutoProvision,
		Deduplicate:   req.Deduplicate,
	}, nil
}

func decodePossiblyDataURLContent(filename, content string) []byte {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		if comma := strings.IndexByte(trimmed, ','); comma > 0 {
			b64Part := strings.TrimSpace(trimmed[comma+1:])
			if decoded, ok := tryStdBase64(b64Part); ok {
				return decoded
			}
		}
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(filename)), ".zip") && !strings.HasPrefix(trimmed, "PK") {
		if decoded, ok := tryStdBase64(trimmed); ok {
			return decoded
		}
	}
	return []byte(content)
}

func tryStdBase64(s string) ([]byte, bool) {
	var clean strings.Builder
	clean.Grow(len(s))
	for _, r := range s {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			continue
		}
		clean.WriteRune(r)
	}
	raw, err := decodeBase64Any(clean.String())
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

func decodeBase64Any(s string) ([]byte, error) {
	var raw []byte
	err := json.Unmarshal([]byte(`"`+s+`"`), &raw)
	if err == nil && len(raw) > 0 {
		return raw, nil
	}
	return nil, errors.New("not base64")
}

func parseFlexibleGroupIDsForm(slices ...[]string) []int64 {
	var out []int64
	for _, arr := range slices {
		for _, item := range arr {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "[") {
				var ids []int64
				if json.Unmarshal([]byte(trimmed), &ids) == nil {
					out = append(out, ids...)
					continue
				}
			}
			for _, part := range strings.Split(trimmed, ",") {
				if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// GetAntigravityPromptModes 返回 AGY CLI 四种提示词运行管线模式及三段式实时预览（GET /api/antigravity/prompt-modes）。
func (h *Handler) GetAntigravityPromptModes(c *gin.Context) {
	previewMode := strings.TrimSpace(c.Query("mode"))
	downstreamSys := c.Query("downstream_system")
	hasTools := true
	if rawHT := strings.TrimSpace(c.Query("has_tools")); rawHT != "" {
		if parsed, err := strconv.ParseBool(rawHT); err == nil {
			hasTools = parsed
		}
	}
	status := antigravity.BuildPromptModesStatus(previewMode, downstreamSys, hasTools)
	c.JSON(http.StatusOK, status)
}

type updateAntigravityPromptModeRequest struct {
	Mode             string `json:"mode"`
	DownstreamSystem string `json:"downstream_system"`
	HasTools         *bool  `json:"has_tools"`
}

// UpdateAntigravityPromptModes 切换当前全局 AGY CLI 提示词运行管线模式并持久化热生效（PUT /api/antigravity/prompt-modes）。
func (h *Handler) UpdateAntigravityPromptModes(c *gin.Context) {
	var req updateAntigravityPromptModeRequest
	if !bindJSON(c, &req) {
		return
	}
	rawMode := strings.TrimSpace(req.Mode)
	if rawMode != "" && !antigravity.IsValidPromptMode(rawMode) {
		fail(c, http.StatusBadRequest, fmt.Sprintf("不支持的 AGY 提示词模式 %q（可选 default_intercept_continue | blend_sanitized | native_only | downstream_only）", rawMode))
		return
	}

	normMode := ""
	if rawMode != "" {
		normMode = antigravity.NormalizePromptMode(rawMode)
	}

	if rs := h.runtimeSettingsSvc(); rs != nil {
		if h.cfg == nil {
			h.cfg = &config.Config{}
		}
		var wbCli *workbuddy.Client
		if h.relay != nil {
			wbCli = h.relay.WorkBuddyClient()
		}
		if _, err := rs.Update(c.Request.Context(), h.cfg, wbCli, service.RuntimeSettingsInput{
			AgyPromptMode: &normMode,
		}); err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		antigravity.SetRuntimePromptMode(normMode)
	}

	hasTools := true
	if req.HasTools != nil {
		hasTools = *req.HasTools
	}
	status := antigravity.BuildPromptModesStatus(normMode, req.DownstreamSystem, hasTools)
	c.JSON(http.StatusOK, status)
}

type previewAntigravityPromptModeRequest struct {
	Mode             string `json:"mode"`
	DownstreamSystem string `json:"downstream_system"`
	UserMessage      string `json:"user_message"`
	HasTools         *bool  `json:"has_tools"`
}

// PreviewAntigravityPromptModes 实时预览「AGY 原生默认 -> 拦截 -> 下游接续」三阶段管线产物（POST /api/antigravity/prompt-modes/preview）。
func (h *Handler) PreviewAntigravityPromptModes(c *gin.Context) {
	var req previewAntigravityPromptModeRequest
	if !bindJSON(c, &req) {
		return
	}
	targetMode := strings.TrimSpace(req.Mode)
	if targetMode == "" {
		targetMode = antigravity.ActivePromptMode()
	} else if !antigravity.IsValidPromptMode(targetMode) {
		fail(c, http.StatusBadRequest, fmt.Sprintf("不支持的 AGY 提示词模式 %q", targetMode))
		return
	}

	hasTools := true
	if req.HasTools != nil {
		hasTools = *req.HasTools
	}
	preview := antigravity.PreviewPromptModeWithUser(targetMode, req.DownstreamSystem, req.UserMessage, hasTools)
	c.JSON(http.StatusOK, preview)
}
