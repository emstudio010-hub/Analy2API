package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/filter"
	"cmd2api/internal/service"
	"cmd2api/internal/workbuddy"

	"github.com/gin-gonic/gin"
)

// Settings 返回当前生效的运行配置（含三级超时、全局提示词、管理密钥与 AntiGravity 环境快照）。
func (h *Handler) Settings(c *gin.Context) {
	ctx := c.Request.Context()
	c.JSON(http.StatusOK, h.buildSettingsPayload(ctx))
}

func (h *Handler) buildSettingsPayload(ctx context.Context) gin.H {
	var cc config.CommandCodeConfig
	var authCfg config.AuthConfig
	var healthCfg config.HealthConfig
	var dbCfg config.DatabaseConfig
	defaultImage := "routerforme/cliproxyapi:latest"
	agyDataDir := "./data/antigravity"

	if h.cfg != nil {
		cc = h.cfg.CommandCC
		authCfg = h.cfg.Auth
		healthCfg = h.cfg.Health
		dbCfg = h.cfg.Database
		if h.cfg.AntigravityDefaultImage != "" {
			defaultImage = h.cfg.AntigravityDefaultImage
		} else if cc.AntigravityDefaultImage != "" {
			defaultImage = cc.AntigravityDefaultImage
		}
		if h.cfg.AntigravityDataDir != "" {
			agyDataDir = h.cfg.AntigravityDataDir
		} else if cc.AntigravityDataDir != "" {
			agyDataDir = cc.AntigravityDataDir
		}
	}

	firstToken := cc.FirstTokenTimeout
	if firstToken <= 0 {
		firstToken = 300 * time.Second
	}
	streamIdle := cc.StreamIdleTimeout
	if streamIdle <= 0 {
		streamIdle = 180 * time.Second
	}
	nonStreamIdle := cc.NonStreamIdleTimeout
	if nonStreamIdle <= 0 {
		nonStreamIdle = 600 * time.Second
	}
	if h.relay != nil {
		ft, si, nsi := h.relay.Timeouts()
		if ft > 0 {
			firstToken = ft
		}
		if si > 0 {
			streamIdle = si
		}
		if nsi > 0 {
			nonStreamIdle = nsi
		}
	}

	redactedProxy := workbuddy.RedactProxyURL(cc.UpstreamProxy)
	rulesCount := len(filter.DefaultRules())
	enabledRules := rulesCount
	if h.filters != nil {
		if total, enabled, err := h.filters.Summary(ctx); err == nil {
			rulesCount = total
			enabledRules = enabled
		}
	}

	globalPromptEnabled := cc.GlobalSystemPromptEnabled
	globalPromptText := cc.GlobalSystemPrompt
	if ps := h.promptSvc(); ps != nil && ps.Engine() != nil {
		gSnap := ps.Engine().Global()
		globalPromptEnabled = gSnap.Enabled
		if gSnap.Prompt != "" {
			globalPromptText = gSnap.Prompt
		}
	}

	adminKeysCount := 0
	nodesCount := 0
	if h.client != nil {
		adminKeysCount, _ = h.client.AdminKey.Query().Count(ctx)
		nodesCount, _ = h.client.ContainerNode.Query().Count(ctx)
	}

	var runtimeOverrides map[string]string
	if rs := h.runtimeSettingsSvc(); rs != nil {
		runtimeOverrides, _ = rs.GetAll(ctx)
	}
	if runtimeOverrides == nil {
		runtimeOverrides = map[string]string{}
	}

	return gin.H{
		"upstream": gin.H{
			"base_url":                 cc.BaseURL,
			"project_slug":             cc.ProjectSlug,
			"cli_version":              cc.CLIVersion,
			"fingerprint_salt_set":     cc.FingerprintSalt != "",
			"fingerprint_salt_masked":  maskSecret(cc.FingerprintSalt),
			"empty_system_placeholder": cc.EmptySystemPlaceholder,
			"first_token_timeout":      firstToken.String(),
			"stream_idle_timeout":      streamIdle.String(),
			"nonstream_idle_timeout":   nonStreamIdle.String(),
			"max_body_mb":              cc.MaxBodyMB,
			"model_cache_ttl":          cc.ModelCacheTTL.String(),
			"upstream_proxy":           redactedProxy,
			"upstream_proxy_set":       cc.UpstreamProxy != "",
			"upstream_proxy_masked":    redactedProxy,
			"auto_harvest_enabled":     cc.AutoHarvestEnabled,
			"auto_harvest_interval":    cc.AutoHarvestInterval.String(),
		},
		"timeouts": gin.H{
			"first_token_timeout":    firstToken.String(),
			"first_token_seconds":    int(firstToken.Seconds()),
			"stream_idle_timeout":    streamIdle.String(),
			"stream_idle_seconds":    int(streamIdle.Seconds()),
			"nonstream_idle_timeout": nonStreamIdle.String(),
			"nonstream_idle_seconds": int(nonStreamIdle.Seconds()),
		},
		"auth": gin.H{
			"token_ttl":                authCfg.TokenTTL.String(),
			"bootstrap_admin_key_set":  strings.TrimSpace(authCfg.BootstrapAdminAPIKey) != "",
			"admin_keys_count":         adminKeysCount,
		},
		"health_check": gin.H{
			"enabled":           healthCfg.Enabled,
			"interval":          healthCfg.Interval.String(),
			"failure_threshold": healthCfg.FailureThreshold,
			"timeout":           healthCfg.Timeout.String(),
		},
		"database": gin.H{
			"driver":      dbCfg.Driver,
			"sqlite_path": dbCfg.SQLitePath,
			"data_dir":    dbCfg.DataDir,
		},
		"filter": gin.H{
			"enabled":                      true,
			"zero_width_disabled":          true,
			"rules_count":                  rulesCount,
			"enabled_rules":                enabledRules,
			"global_system_prompt_enabled": globalPromptEnabled,
			"global_system_prompt":         globalPromptText,
		},
		"antigravity": gin.H{
			"default_image": defaultImage,
			"data_dir":      agyDataDir,
			"nodes_count":   nodesCount,
		},
		"tool_obfuscation":  h.buildToolObfuscationSummary(),
		"session_guard":     h.buildSessionGuardSummary(),
		"runtime_overrides": runtimeOverrides,
	}
}

type updateRuntimeSettingsRequest struct {
	FirstTokenSeconds         *int              `json:"first_token_seconds"`
	FirstTokenTimeout         *string           `json:"first_token_timeout"`
	StreamIdleSeconds         *int              `json:"stream_idle_seconds"`
	StreamIdleTimeout         *string           `json:"stream_idle_timeout"`
	NonStreamIdleSeconds      *int              `json:"nonstream_idle_seconds"`
	NonStreamIdleTimeout      *string           `json:"nonstream_idle_timeout"`
	UpstreamProxy             *string           `json:"upstream_proxy"`
	EmptySystemPlaceholder    *bool             `json:"empty_system_placeholder"`
	AutoHarvestEnabled        *bool             `json:"auto_harvest_enabled"`
	AutoHarvestInterval       *string           `json:"auto_harvest_interval"`
	GlobalSystemPromptEnabled *bool             `json:"global_system_prompt_enabled"`
	GlobalSystemPrompt        *string           `json:"global_system_prompt"`
	Settings                  map[string]string `json:"settings"`
}

// UpdateRuntimeSettings 在线持久化并热更新三级超时、全局代理、空 System 占位与自动收割配置（零重启生效）。
func (h *Handler) UpdateRuntimeSettings(c *gin.Context) {
	rs := h.runtimeSettingsSvc()
	if rs == nil {
		fail(c, http.StatusServiceUnavailable, "运行态配置服务未初始化")
		return
	}

	var req updateRuntimeSettingsRequest
	if !bindJSON(c, &req) {
		return
	}

	// 若前端回传了脱敏后的代理字符串（包含 ***），忽略代理字段以免把掩码写入数据库。
	if req.UpstreamProxy != nil && strings.Contains(*req.UpstreamProxy, "***") {
		req.UpstreamProxy = nil
		delete(req.Settings, service.SettingKeyUpstreamProxy)
	}

	ctx := c.Request.Context()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}

	var wbCli *workbuddy.Client
	if h.relay != nil {
		wbCli = h.relay.WorkBuddyClient()
	}

	_, err := rs.Update(ctx, h.cfg, wbCli, service.RuntimeSettingsInput{
		FirstTokenSeconds:      req.FirstTokenSeconds,
		FirstTokenTimeout:      req.FirstTokenTimeout,
		StreamIdleSeconds:      req.StreamIdleSeconds,
		StreamIdleTimeout:      req.StreamIdleTimeout,
		NonStreamIdleSeconds:   req.NonStreamIdleSeconds,
		NonStreamIdleTimeout:   req.NonStreamIdleTimeout,
		UpstreamProxy:          req.UpstreamProxy,
		EmptySystemPlaceholder: req.EmptySystemPlaceholder,
		AutoHarvestEnabled:     req.AutoHarvestEnabled,
		AutoHarvestInterval:    req.AutoHarvestInterval,
		Settings:               req.Settings,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	// 同步热更新 Relay 服务的三级超时、空占位开关与全局代理
	if h.relay != nil {
		cc := h.cfg.CommandCC
		h.relay.UpdateTimeouts(cc.FirstTokenTimeout, cc.StreamIdleTimeout, cc.NonStreamIdleTimeout)
		h.relay.SetEmptySystemPlaceholder(cc.EmptySystemPlaceholder)
		if req.UpstreamProxy != nil {
			_ = h.relay.UpdateDefaultProxy(cc.UpstreamProxy)
		}
	}

	// 若请求同时附带了全局提示词修改，同步更新 PromptService
	if req.GlobalSystemPromptEnabled != nil || req.GlobalSystemPrompt != nil {
		if ps := h.promptSvc(); ps != nil {
			cur := ps.Engine().Global()
			enabled := cur.Enabled
			if req.GlobalSystemPromptEnabled != nil {
				enabled = *req.GlobalSystemPromptEnabled
			}
			promptText := cur.Prompt
			if req.GlobalSystemPrompt != nil {
				promptText = *req.GlobalSystemPrompt
			}
			_ = ps.UpdateGlobal(ctx, enabled, promptText)
			h.cfg.CommandCC.GlobalSystemPromptEnabled = enabled
			h.cfg.CommandCC.GlobalSystemPrompt = promptText
		}
	}

	c.JSON(http.StatusOK, h.buildSettingsPayload(ctx))
}

// AdminListModels 返回上游可用模型列表（管理后台用，支持 ?platform= 与 ?group_id= 过滤）。
func (h *Handler) AdminListModels(c *gin.Context) {
	if h.relay == nil {
		fail(c, http.StatusServiceUnavailable, "中转服务未启用")
		return
	}
	ctx := c.Request.Context()
	plat := strings.TrimSpace(c.Query("platform"))
	if rawGID := strings.TrimSpace(c.Query("group_id")); rawGID != "" {
		if gid, err := strconv.ParseInt(rawGID, 10, 64); err == nil && gid > 0 {
			models, mErr := h.relay.Models(ctx, gid)
			if mErr == nil {
				c.JSON(http.StatusOK, gin.H{"items": models, "total": len(models)})
				return
			}
		}
	}

	models, err := h.relay.PlatformModels(ctx, plat)
	if (err != nil || len(models) == 0) && plat == "" {
		groupID, gErr := h.firstActiveGroupID(ctx)
		if gErr == nil && groupID > 0 {
			models, err = h.relay.Models(ctx, groupID)
		}
	}
	if err != nil && len(models) == 0 {
		h.logger.Warn("拉取模型列表失败", "err", err, "platform", plat)
		fail(c, http.StatusServiceUnavailable, "暂时无法获取模型列表，请确认账号可用")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": models, "total": len(models)})
}

// maskSecret 把密钥打码，只保留首尾字符。
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return "****"
	}
	return crypto.MaskKey(s)
}
