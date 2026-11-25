package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cmd2api/internal/config"
	"cmd2api/internal/domain"
	"cmd2api/internal/relay"
	"cmd2api/internal/scheduler"
	"cmd2api/internal/workbuddy"
)

func TestCronServiceLifecycleAndExecution(t *testing.T) {
	ctx := context.Background()
	dbClient := newServiceTestDB(t)
	vault := newServiceTestVault(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var chatCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case workbuddy.PathChatCompletions:
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case workbuddy.PathBillingSummary:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"data":{"TotalCredits":150,"SubscriptionPackageName":"Pro","Packages":[{"PackageCode":"FREE","PackageName":"体验包","Kind":"plan_base","Remain":150,"Total":200}]}}`)
		case workbuddy.PathDailyCheckin:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"data":{"credit":15,"streak":3,"date":"2026-11-15"}}`)
		case workbuddy.PathGrowthTasks:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"data":{"tasks":[{"code":"daily_check","status":"pending","credit":10}]}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
		}
	}))
	defer srv.Close()

	wbClient := workbuddy.NewClient("", srv.URL, logger)
	relayClient := relay.NewClient(config.CommandCodeConfig{
		BaseURL:           srv.URL,
		StreamIdleTimeout: 15 * time.Second,
	}, logger)
	relayClient.SetWorkBuddyClient(wbClient)

	sched := scheduler.New(dbClient, vault, 3, logger)
	accSvc := NewAccountService(dbClient, vault, relayClient, sched, logger)
	cronSvc := NewCronService(dbClient, accSvc, logger)

	// 1. Seed 默认内置任务（幂等）
	if err := cronSvc.EnsureSeeded(ctx); err != nil {
		t.Fatalf("EnsureSeeded failed: %v", err)
	}
	if err := cronSvc.EnsureSeeded(ctx); err != nil {
		t.Fatalf("second EnsureSeeded failed: %v", err)
	}
	jobs, err := cronSvc.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(jobs) != 4 {
		t.Fatalf("expected 4 default cron jobs, got %d", len(jobs))
	}

	// 2. 创建测试账号（1 个 WorkBuddy 账号 + 1 个手动禁用账号）
	activeAcc, err := accSvc.Create(ctx, CreateAccountInput{
		Name:        "wb-cron-1",
		Platform:    domain.PlatformWorkBuddy,
		Domain:      srv.URL,
		APIKey:      "wb_token_cron_test_1",
		UID:         "uid-1",
		Concurrency: 2,
		Priority:    100,
	})
	if err != nil {
		t.Fatalf("create active account: %v", err)
	}
	disabledStatus := domain.StatusDisabled
	_, err = accSvc.Create(ctx, CreateAccountInput{
		Name:        "wb-cron-disabled",
		Platform:    domain.PlatformWorkBuddy,
		Domain:      srv.URL,
		APIKey:      "wb_token_cron_test_2",
		Status:      disabledStatus,
		Concurrency: 2,
		Priority:    100,
	})
	if err != nil {
		t.Fatalf("create disabled account: %v", err)
	}

	// 3. 预览合法与非法 Cron 表达式
	goodPreview := cronSvc.Preview("0 8,20 * * *", "Asia/Shanghai", 3)
	if !goodPreview.Valid || len(goodPreview.NextRuns) != 3 {
		t.Fatalf("expected valid preview with 3 runs, got %+v", goodPreview)
	}
	badPreview := cronSvc.Preview("invalid cron", "Asia/Shanghai", 3)
	if badPreview.Valid || badPreview.Error == "" {
		t.Fatalf("expected invalid preview error, got %+v", badPreview)
	}

	// 4. 测试创建自定义 Cron 任务并手动触发「定时自动测试探活」
	customCheckJob, err := cronSvc.Create(ctx, CreateCronJobInput{
		Name:        "测试单账号探活",
		Description: "单元测试",
		CronExpr:    "*/15 * * * *",
		Timezone:    "Asia/Shanghai",
		Action:      CronActionAccountCheck,
		TargetScope: CronScopeAccounts,
		AccountIDs:  []int64{activeAcc.ID},
		Options:     map[string]any{"auto_recover": true, "concurrency": 1},
	})
	if err != nil {
		t.Fatalf("Create custom check job failed: %v", err)
	}

	checkOut, err := cronSvc.ExecuteJob(ctx, customCheckJob.ID, "manual")
	if err != nil {
		t.Fatalf("ExecuteJob account_check failed: %v", err)
	}
	if checkOut.Status != CronStatusSuccess || checkOut.Total != 1 || checkOut.Succeeded != 1 {
		b, _ := json.Marshal(checkOut)
		t.Fatalf("unexpected account_check result: %s", string(b))
	}
	if chatCalls.Load() == 0 {
		t.Fatalf("expected probe request to upstream chat completions")
	}

	// 5. 手动触发「定时领奖励」内置任务（只针对未禁用的 WorkBuddy 账号，跳过 disabled）
	var harvestJobID int64
	for _, j := range jobs {
		if j.Action == CronActionHarvest {
			harvestJobID = j.ID
			break
		}
	}
	if harvestJobID == 0 {
		t.Fatalf("harvest builtin job not found")
	}
	harvestOut, err := cronSvc.ExecuteJob(ctx, harvestJobID, "manual")
	if err != nil {
		t.Fatalf("ExecuteJob harvest failed: %v", err)
	}
	if harvestOut.Total != 1 || !harvestOut.Items[0].OK {
		b, _ := json.Marshal(harvestOut)
		t.Fatalf("expected 1 active account harvested successfully, got %s", string(b))
	}

	updatedHarvestJob, err := cronSvc.Get(ctx, harvestJobID)
	if err != nil {
		t.Fatalf("Get updated harvest job failed: %v", err)
	}
	if updatedHarvestJob.LastRunAt == nil || len(updatedHarvestJob.RunHistory) != 1 {
		t.Fatalf("expected persisted run history and last_run_at, got run_at=%v history=%d",
			updatedHarvestJob.LastRunAt, len(updatedHarvestJob.RunHistory))
	}

	// 6. 更新开关并删除自定义任务
	falseVal := false
	toggled, err := cronSvc.Update(ctx, customCheckJob.ID, UpdateCronJobInput{Enabled: &falseVal})
	if err != nil || toggled.Enabled {
		t.Fatalf("toggle disabled failed: err=%v enabled=%v", err, toggled != nil && toggled.Enabled)
	}
	if err := cronSvc.Delete(ctx, customCheckJob.ID); err != nil {
		t.Fatalf("Delete custom check job failed: %v", err)
	}
}
