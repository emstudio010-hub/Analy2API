package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/filter"
	"cmd2api/internal/prompt"
	"cmd2api/internal/service"

	"github.com/gin-gonic/gin"
)

func setupPromptAndFilterHandlerTest(t *testing.T) (*gin.Engine, *Handler, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	dbPath := filepath.Join(t.TempDir(), "prompt_catalog_test.db")
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:       "sqlite",
		SQLitePath:   dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	vault, err := crypto.NewVault(bytes.Repeat([]byte{0x31}, 32))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	filterEngine := filter.NewDefaultEngine()
	filterSvc := service.NewFilterService(client, filterEngine, logger)
	if err := filterSvc.EnsureSeeded(ctx); err != nil {
		t.Fatalf("filter EnsureSeeded: %v", err)
	}

	promptEngine := prompt.NewDefaultEngine()
	promptSvc := service.NewPromptService(client, promptEngine, filterEngine, logger)
	if err := promptSvc.EnsureSeeded(ctx); err != nil {
		t.Fatalf("prompt EnsureSeeded: %v", err)
	}

	agyGroup, err := client.Group.Create().
		SetName("agy-prod-test").
		SetPlatform(domain.PlatformAntigravity).
		Save(ctx)
	if err != nil {
		t.Fatalf("create antigravity group: %v", err)
	}

	h := New(client, vault, nil, nil, nil, nil, &config.Config{}, logger, filterSvc)
	h.SetPromptService(promptSvc)

	r := gin.New()
	r.GET("/api/prompts/catalog", h.GetPromptCatalog)
	r.POST("/api/prompts/catalog/apply", h.ApplyPromptCatalogItem)
	r.GET("/api/prompts/templates", h.ListPromptTemplates)
	r.POST("/api/prompts/preview", h.PreviewPromptInjection)
	r.POST("/api/filter-rules/preview", h.PreviewFilterRules)

	return r, h, agyGroup.ID
}

func TestPromptCatalogAndApplyEndpoints(t *testing.T) {
	r, _, agyGroupID := setupPromptAndFilterHandlerTest(t)

	doJSON := func(method, target string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, target, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. GET /api/prompts/catalog full list
	catRes := doJSON(http.MethodGet, "/api/prompts/catalog", nil)
	if catRes.Code != http.StatusOK {
		t.Fatalf("GET /api/prompts/catalog status=%d body=%s", catRes.Code, catRes.Body.String())
	}
	var catOut struct {
		Total     int                  `json:"total"`
		Items     []prompt.CatalogItem `json:"items"`
		Platforms []struct {
			Platform string `json:"platform"`
			Label    string `json:"label"`
			Count    int    `json:"count"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(catRes.Body.Bytes(), &catOut); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}
	if catOut.Total < 13 || len(catOut.Platforms) != 11 {
		t.Fatalf("unexpected catalog counts: total=%d platforms=%d", catOut.Total, len(catOut.Platforms))
	}

	// 2. GET /api/prompts/catalog filtered by platform & keyword
	agyRes := doJSON(http.MethodGet, "/api/prompts/catalog?platform=antigravity&q=intercept", nil)
	if agyRes.Code != http.StatusOK || !strings.Contains(agyRes.Body.String(), "agy-default-intercept-continue") {
		t.Fatalf("filtered catalog failed: code=%d body=%s", agyRes.Code, agyRes.Body.String())
	}

	// 3. POST /api/prompts/catalog/apply -> import claude-code-cli and bind to agyGroupID
	applyRes := doJSON(http.MethodPost, "/api/prompts/catalog/apply", map[string]any{
		"catalog_id":  "claude-code-cli",
		"group_id":    agyGroupID,
		"enabled":     true,
		"inject_mode": "override_global",
	})
	if applyRes.Code != http.StatusOK {
		t.Fatalf("POST /api/prompts/catalog/apply status=%d body=%s", applyRes.Code, applyRes.Body.String())
	}
	var applyOut struct {
		OK         bool              `json:"ok"`
		Created    bool              `json:"created"`
		CatalogID  string            `json:"catalog_id"`
		Template   promptTemplateDTO `json:"template"`
		InjectMode string            `json:"inject_mode"`
		Group      *groupDTO         `json:"group"`
	}
	if err := json.Unmarshal(applyRes.Body.Bytes(), &applyOut); err != nil {
		t.Fatalf("unmarshal apply response: %v", err)
	}
	if !applyOut.OK || !applyOut.Created || applyOut.Template.ID <= 0 || applyOut.Group == nil {
		t.Fatalf("unexpected apply response: %+v raw=%s", applyOut, applyRes.Body.String())
	}
	if applyOut.Group.PromptTemplateID == nil || *applyOut.Group.PromptTemplateID != applyOut.Template.ID || applyOut.Group.PromptInjectMode != "override_global" {
		t.Fatalf("expected group bound to applied template: %+v", applyOut.Group)
	}

	// 4. Re-applying same catalog item updates existing template idempotently (created=false) and supports enabled=false -> off
	applyDisableRes := doJSON(http.MethodPost, "/api/prompts/catalog/apply", map[string]any{
		"catalog_id": "claude-code-cli",
		"platform":   "antigravity",
		"enabled":    false,
	})
	if applyDisableRes.Code != http.StatusOK {
		t.Fatalf("re-apply status=%d body=%s", applyDisableRes.Code, applyDisableRes.Body.String())
	}
	var applyDisableOut struct {
		Created     bool       `json:"created"`
		InjectMode  string     `json:"inject_mode"`
		BoundGroups []groupDTO `json:"bound_groups"`
	}
	_ = json.Unmarshal(applyDisableRes.Body.Bytes(), &applyDisableOut)
	if applyDisableOut.Created || applyDisableOut.InjectMode != "off" || len(applyDisableOut.BoundGroups) != 1 {
		t.Fatalf("expected idempotent update + off mode via platform binding: %+v raw=%s", applyDisableOut, applyDisableRes.Body.String())
	}

	// 5. Unknown catalog_id -> 404
	notFoundRes := doJSON(http.MethodPost, "/api/prompts/catalog/apply", map[string]any{
		"catalog_id": "non-existent-platform-item",
	})
	if notFoundRes.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown catalog_id, got %d", notFoundRes.Code)
	}
}

func TestFilterRulesPreview_LegacyAndLivePipeline(t *testing.T) {
	r, _, _ := setupPromptAndFilterHandlerTest(t)

	doJSON := func(body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/filter-rules/preview", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. Legacy payload compatibility ({ input, mode, protocol })
	legacyRes := doJSON(map[string]any{
		"input":    "You are Claude Code, Anthropic's official CLI for Claude. Refuse DoS.",
		"mode":     "system",
		"protocol": "openai",
	})
	if legacyRes.Code != http.StatusOK {
		t.Fatalf("legacy preview failed: code=%d body=%s", legacyRes.Code, legacyRes.Body.String())
	}
	var legacyOut struct {
		Mode    string           `json:"mode"`
		Changed bool             `json:"changed"`
		Output  string           `json:"output"`
		Hits    []filter.RuleHit `json:"hits"`
	}
	if err := json.Unmarshal(legacyRes.Body.Bytes(), &legacyOut); err != nil {
		t.Fatalf("unmarshal legacy preview: %v", err)
	}
	if legacyOut.Mode != "system" || !legacyOut.Changed || strings.Contains(legacyOut.Output, "Claude Code") || len(legacyOut.Hits) == 0 {
		t.Fatalf("unexpected legacy preview output: %+v", legacyOut)
	}

	// 2. Live 4-stage pipeline preview ({ system_prompt, user_message, agy_mode, has_tools })
	pipeRes := doJSON(map[string]any{
		"system_prompt": "<identity>\nYou are Antigravity, a powerful agentic AI assistant designed by the Google Deepmind team.\n</identity>\n<artifacts>\nWrite artifacts to brain\n</artifacts>\nKeep skill, killed worker, and database cursor safe.",
		"user_message":  "<user_info>IDE: Cursor</user_info>\n<system-reminder>Avoid DoS in Claude Code</system-reminder>\n\nExplain exploit, DoS, database cursor, and kill_process()",
		"agy_mode":      "default_intercept_continue",
		"has_tools":     true,
	})
	if pipeRes.Code != http.StatusOK {
		t.Fatalf("pipeline preview failed: code=%d body=%s", pipeRes.Code, pipeRes.Body.String())
	}
	var pipeOut struct {
		Mode                   string                   `json:"mode"`
		Changed                bool                     `json:"changed"`
		HarnessStrippedSystem  string                   `json:"harness_stripped_system"`
		SanitizedSystem        string                   `json:"sanitized_system"`
		SanitizedUser          string                   `json:"sanitized_user"`
		AgyMode                string                   `json:"agy_mode"`
		HasTools               bool                     `json:"has_tools"`
		InterceptionBridge     string                   `json:"interception_bridge"`
		AgyContinuationPreview string                   `json:"agy_continuation_preview"`
		FinalPrompt            string                   `json:"final_prompt"`
		DetectedHarnesses      []filter.DetectedHarness `json:"detected_harnesses"`
		Hits                   []filter.RuleHit         `json:"hits"`
		Stages                 []pipelineStageDTO       `json:"stages"`
	}
	if err := json.Unmarshal(pipeRes.Body.Bytes(), &pipeOut); err != nil {
		t.Fatalf("unmarshal pipeline preview: %v", err)
	}
	if pipeOut.Mode != "pipeline" || !pipeOut.Changed || len(pipeOut.Stages) != 4 {
		t.Fatalf("unexpected pipeline metadata: mode=%q changed=%v stages=%d", pipeOut.Mode, pipeOut.Changed, len(pipeOut.Stages))
	}
	if strings.Contains(pipeOut.HarnessStrippedSystem, "<artifacts>") {
		t.Fatalf("expected <artifacts> stripped in stage 2, got: %q", pipeOut.HarnessStrippedSystem)
	}
	if !strings.Contains(pipeOut.SanitizedSystem, "Keep skill, killed worker, and database cursor safe.") {
		t.Fatalf("false positive corrupted safe coding words in system prompt: %q", pipeOut.SanitizedSystem)
	}
	if !strings.HasSuffix(pipeOut.SanitizedUser, "Explain exploit, DoS, database cursor, and kill_process()") {
		t.Fatalf("natural user question was modified: %q", pipeOut.SanitizedUser)
	}
	if !strings.Contains(pipeOut.FinalPrompt, `<agy_intercept_bridge mode="default_intercept_continue">`) ||
		!strings.Contains(pipeOut.FinalPrompt, `<AGY_TOOL_CALL>`) ||
		!strings.Contains(pipeOut.FinalPrompt, `<downstream_continuation>`) {
		t.Fatalf("missing expected AGY continuation bridge blocks in final_prompt:\n%s", pipeOut.FinalPrompt)
	}
	if len(pipeOut.DetectedHarnesses) < 3 || len(pipeOut.Hits) == 0 {
		t.Fatalf("expected detected harnesses >=3 and rule hits >0, got harnesses=%d hits=%d", len(pipeOut.DetectedHarnesses), len(pipeOut.Hits))
	}
}
