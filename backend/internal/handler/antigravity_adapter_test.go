package handler_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cmd2api/internal/antigravity"
	"cmd2api/internal/auth"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/handler"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/server"
	"cmd2api/internal/service"
)

func setupAntigravityAdapterRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	clientDB, err := db.Open(ctx, config.DatabaseConfig{
		Driver:     "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "handler_agy.db"),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = clientDB.Close() })

	vault, err := crypto.NewVault([]byte(strings.Repeat("h", 32)))
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	const bootKey = "adm-test-bootstrap-key-123456"
	cfg := &config.Config{
		Server:             config.ServerConfig{Mode: "release"},
		AntigravityDataDir: filepath.Join(t.TempDir(), "agy-nodes"),
		Auth: config.AuthConfig{
			JWTSecret:            "jwt-secret-test-123456",
			TokenTTL:             time.Hour,
			BootstrapAdminAPIKey: bootKey,
		},
	}
	issuer := auth.NewIssuer(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)
	sched := scheduler.New(clientDB, vault, 3, logger)
	relayCli := relay.NewClient(cfg.CommandCC, logger)
	accSvc := service.NewAccountService(clientDB, vault, relayCli, sched, logger)
	agSvc := service.NewAntigravityService(clientDB, vault, antigravity.NewClient("", logger), nil, cfg.AntigravityDataDir, "", logger)
	accSvc.SetAntigravityService(agSvc)
	rsSvc := service.NewRuntimeSettingsService(clientDB, logger)

	h := handler.New(clientDB, vault, issuer, sched, nil, accSvc, cfg, logger)
	h.SetAntigravityService(agSvc)
	h.SetRuntimeSettingsService(rsSvc)

	r := server.NewRouter(server.Options{
		Handler:              h,
		Issuer:               issuer,
		Client:               clientDB,
		BootstrapAdminAPIKey: bootKey,
		Mode:                 "release",
	})
	return r, bootKey
}

func TestAntigravityConvertAndPromptModesEndpoints(t *testing.T) {
	r, adminKey := setupAntigravityAdapterRouter(t)

	// 1. 测试 JSON 模式 POST /api/antigravity/accounts/convert (auto_import=true)
	jsonBody := `{
		"raw_text": "alpha@example.com----Pass123----1//0gHandlerAlphaRT\nbeta@example.com:1//0gHandlerBetaRT",
		"filename": "batch.txt",
		"auto_import": true
	}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/antigravity/accounts/convert", strings.NewReader(jsonBody))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer "+adminKey)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("POST /api/antigravity/accounts/convert JSON status=%d body=%s", w1.Code, w1.Body.String())
	}
	var resp1 struct {
		OK              bool   `json:"ok"`
		ValidCount      int    `json:"valid_count"`
		BundleZipBase64 string `json:"bundle_zip_base64"`
		ImportResult    struct {
			Imported int `json:"imported"`
			Created  int `json:"created"`
		} `json:"import_result"`
	}
	if err := json.Unmarshal(w1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("decode convert json response: %v", err)
	}
	if !resp1.OK || resp1.ValidCount != 2 || resp1.ImportResult.Created != 2 || resp1.BundleZipBase64 == "" {
		t.Fatalf("unexpected convert JSON response: %+v", resp1)
	}

	// 2. 测试 Multipart 上传 .zip + .json 文件预览模式
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	zf, _ := zw.Create("adc.json")
	_, _ = zf.Write([]byte(`{"type":"authorized_user","refresh_token":"1//0gMultipartZipADC","quota_project_id":"mp-proj"}`))
	_ = zw.Close()

	var mpBody bytes.Buffer
	mw := multipart.NewWriter(&mpBody)
	fw, _ := mw.CreateFormFile("files[]", "creds.zip")
	_, _ = fw.Write(zipBuf.Bytes())
	_ = mw.WriteField("auto_import", "false")
	_ = mw.Close()

	req2 := httptest.NewRequest(http.MethodPost, "/api/antigravity/accounts/convert", &mpBody)
	req2.Header.Set("Content-Type", mw.FormDataContentType())
	req2.Header.Set("Authorization", "Bearer "+adminKey)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("multipart convert failed: status=%d body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "zip_archive") || !strings.Contains(w2.Body.String(), ".gemini/oauth_creds.json") {
		t.Fatalf("expected zip_archive and .gemini/oauth_creds.json in multipart response: %s", w2.Body.String())
	}

	// 3. 测试 GET / PUT / POST /api/antigravity/prompt-modes
	t.Cleanup(func() { antigravity.SetRuntimePromptMode("") })

	reqGetModes := httptest.NewRequest(http.MethodGet, "/api/antigravity/prompt-modes", nil)
	reqGetModes.Header.Set("Authorization", "Bearer "+adminKey)
	wGet := httptest.NewRecorder()
	r.ServeHTTP(wGet, reqGetModes)
	if wGet.Code != http.StatusOK || !strings.Contains(wGet.Body.String(), "default_intercept_continue") {
		t.Fatalf("GET /api/antigravity/prompt-modes unexpected: code=%d body=%s", wGet.Code, wGet.Body.String())
	}

	reqPutMode := httptest.NewRequest(http.MethodPut, "/api/antigravity/prompt-modes", strings.NewReader(`{"mode":"blend_sanitized","downstream_system":"You are DSH."}`))
	reqPutMode.Header.Set("Content-Type", "application/json")
	reqPutMode.Header.Set("Authorization", "Bearer "+adminKey)
	wPut := httptest.NewRecorder()
	r.ServeHTTP(wPut, reqPutMode)
	if wPut.Code != http.StatusOK || !strings.Contains(wPut.Body.String(), `"active_mode":"blend_sanitized"`) {
		t.Fatalf("PUT /api/antigravity/prompt-modes unexpected: code=%d body=%s", wPut.Code, wPut.Body.String())
	}

	reqPreview := httptest.NewRequest(http.MethodPost, "/api/antigravity/prompt-modes/preview", strings.NewReader(`{"mode":"default_intercept_continue","downstream_system":"x-anthropic-billing-header: v1\nYou are Codex.","user_message":"Fix bug","has_tools":true}`))
	reqPreview.Header.Set("Content-Type", "application/json")
	reqPreview.Header.Set("Authorization", "Bearer "+adminKey)
	wPrev := httptest.NewRecorder()
	r.ServeHTTP(wPrev, reqPreview)
	if wPrev.Code != http.StatusOK {
		t.Fatalf("POST /api/antigravity/prompt-modes/preview status=%d body=%s", wPrev.Code, wPrev.Body.String())
	}
	var prevOut antigravity.InterceptContinuePreview
	if err := json.Unmarshal(wPrev.Body.Bytes(), &prevOut); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if !prevOut.OK || prevOut.ToolTag != "<AGY_TOOL_CALL>" || !strings.Contains(prevOut.ContinueStage, "downstream_continuation") || prevOut.SanitizedDownstream != "You are Codex." {
		t.Fatalf("POST /api/antigravity/prompt-modes/preview unexpected struct: %+v", prevOut)
	}
}
