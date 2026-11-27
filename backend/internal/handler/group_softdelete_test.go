package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cmd2api/internal/auth"
	"cmd2api/internal/config"
	"cmd2api/internal/crypto"
	"cmd2api/internal/db"
	"cmd2api/internal/domain"
	"cmd2api/internal/handler"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/server"
	"cmd2api/internal/service"
)

func TestDeleteGroupFreesUniqueNameForRecreate(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "group_softdelete.db")
	client, err := db.Open(ctx, config.DatabaseConfig{
		Driver:       "sqlite",
		SQLitePath:   dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer client.Close()

	vault, err := crypto.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	issuer := auth.NewIssuer("test-jwt-secret-0123456789abcdef", time.Hour)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Server: config.ServerConfig{Mode: "test"}}

	sched := scheduler.New(client, vault, 5, logger)
	relayCli := relay.NewClient(config.CommandCodeConfig{}, logger)
	accSvc := service.NewAccountService(client, vault, relayCli, sched, logger)
	adminKeySvc := service.NewAdminKeyService(client)

	h := handler.New(client, vault, issuer, sched, nil, accSvc, cfg, logger)
	h.SetAdminKeyService(adminKeySvc)

	r := server.NewRouter(server.Options{
		Handler: h,
		Issuer:  issuer,
		Client:  client,
		Mode:    "test",
		Version: "1.0.0",
	})

	fullKey, _, err := adminKeySvc.Create(ctx, "admin", domain.AdminKeyScopeFull, "", 0)
	if err != nil {
		t.Fatalf("Create admin key: %v", err)
	}

	doReq := func(method, path string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Management-Key", fullKey)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	// 1. 创建名为 "prod-agy" 的分组
	rrCreate1 := doReq(http.MethodPost, "/api/groups", map[string]any{
		"name":     "prod-agy",
		"platform": "antigravity",
	})
	if rrCreate1.Code != http.StatusCreated {
		t.Fatalf("first create group failed: code=%d body=%s", rrCreate1.Code, rrCreate1.Body.String())
	}
	var g1 struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rrCreate1.Body.Bytes(), &g1)

	// 2. 软删除该分组
	rrDel := doReq(http.MethodDelete, "/api/groups/1", nil)
	if g1.ID != 1 {
		rrDel = doReq(http.MethodDelete, "/api/groups/"+jsonNumber(g1.ID), nil)
	}
	if rrDel.Code != http.StatusOK {
		t.Fatalf("delete group failed: code=%d body=%s", rrDel.Code, rrDel.Body.String())
	}

	// 3. 再次用同名 "prod-agy" 重建分组，不应撞 UNIQUE constraint 409/500
	rrCreate2 := doReq(http.MethodPost, "/api/groups", map[string]any{
		"name":     "prod-agy",
		"platform": "antigravity",
	})
	if rrCreate2.Code != http.StatusCreated {
		t.Fatalf("recreate group with same name after soft-delete failed: code=%d body=%s", rrCreate2.Code, rrCreate2.Body.String())
	}
}

func jsonNumber(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}
