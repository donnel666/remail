package kitesim

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

func TestWorkersOnlyClaimQueuedStates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}, &syncRunModel{}); err != nil {
		t.Fatal(err)
	}
	account := accountModel{Account: "owner@example.com", SyncStatus: string(SyncTaskSucceeded)}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&upstreamSettingsModel{ID: upstreamSettingsID, AccountID: &account.ID, RefreshStatus: string(SyncTaskSucceeded)}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)

	if _, err := service.markSyncRunning(context.Background(), account.ID); !errors.Is(err, errTaskNotQueued) {
		t.Fatalf("sync claim error = %v", err)
	}
	if _, err := service.markUpstreamRefreshRunning(context.Background()); !errors.Is(err, errTaskNotQueued) {
		t.Fatalf("refresh claim error = %v", err)
	}
}

func TestOldAccountSyncFailureDoesNotOverwriteNewClaim(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}); err != nil {
		t.Fatal(err)
	}
	oldStarted := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	newStarted := oldStarted.Add(time.Millisecond)
	account := accountModel{
		Account: "owner@example.com", Password: "new-password", Token: "new-token",
		SyncStatus: string(SyncTaskRunning), SyncStartedAt: &newStarted,
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.recordSyncTaskFailure(context.Background(), accountSyncClaim{
		AccountID: account.ID, StartedAt: oldStarted,
	}, "stale failure", false)

	if err := db.First(&account, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if SyncTaskStatus(account.SyncStatus) != SyncTaskRunning || account.SyncStartedAt == nil ||
		!account.SyncStartedAt.Equal(newStarted) || account.LastSafeError != "" || account.Token != "new-token" {
		t.Fatalf("new sync claim was overwritten: %+v", account)
	}
}

func TestOldUpstreamRefreshFailureDoesNotOverwriteNewClaim(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}); err != nil {
		t.Fatal(err)
	}
	accountA := testAccount("a@example.com", "password", "")
	accountB := testAccount("b@example.com", "password", "")
	if err := db.Create(&accountA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&accountB).Error; err != nil {
		t.Fatal(err)
	}
	oldStarted := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	newStarted := oldStarted.Add(time.Minute)
	if err := db.Create(&upstreamSettingsModel{
		ID: upstreamSettingsID, AccountID: &accountB.ID, Balance: "20",
		RefreshStatus: string(SyncTaskRunning), RefreshStarted: &newStarted,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.recordUpstreamRefreshFailure(context.Background(), upstreamRefreshClaim{
		AccountID: accountA.ID, StartedAt: oldStarted,
	}, ErrLoginFailed, false)

	var settings upstreamSettingsModel
	if err := db.First(&settings, upstreamSettingsID).Error; err != nil {
		t.Fatal(err)
	}
	if settings.AccountID == nil || *settings.AccountID != accountB.ID ||
		SyncTaskStatus(settings.RefreshStatus) != SyncTaskRunning || settings.RefreshStarted == nil ||
		!settings.RefreshStarted.Equal(newStarted) || settings.Balance != "20" || settings.LastSafeError != "" {
		t.Fatalf("new refresh claim was overwritten: %+v", settings)
	}
}

func TestMoneyOperationUsesDefaultQueueAndOnlyRetriesBeforeClaim(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		_ = inspector.Close()
		_ = client.Close()
	})

	accepted, err := NewSyncQueue(client).EnqueueOperation(context.Background(), 42)
	if err != nil || !accepted {
		t.Fatalf("enqueue operation: accepted=%v err=%v", accepted, err)
	}
	tasks, err := inspector.ListScheduledTasks(platform.QueueDefault)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("scheduled tasks = %d, want 1", len(tasks))
	}
	if tasks[0].Type != typeUpstreamOperation || tasks[0].MaxRetry != platform.BackgroundTaskMaxRetryValue() {
		t.Fatalf("operation task type=%q maxRetry=%d", tasks[0].Type, tasks[0].MaxRetry)
	}
}
