package icloud

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

func TestDispatchICloudOnboardingTasksReconcilesStaleImports(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-onboarding-reconcile?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&iCloudOnboardingImportModel{}, &iCloudOnboardingTaskModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	terminal := iCloudOnboardingImportModel{Status: iCloudOnboardingProcessing, AcceptedCount: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	waiting := iCloudOnboardingImportModel{Status: iCloudOnboardingProcessing, AcceptedCount: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	if err := db.Create(&terminal).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&waiting).Error; err != nil {
		t.Fatal(err)
	}
	tasks := []iCloudOnboardingTaskModel{
		{ImportID: &terminal.ID, Status: iCloudOnboardingCompleted, DispatchStatus: "succeeded", CreatedAt: now, UpdatedAt: now},
		{ImportID: &terminal.ID, Status: iCloudOnboardingFailed, DispatchStatus: "failed", CreatedAt: now, UpdatedAt: now},
		{ImportID: &waiting.ID, Status: iCloudOnboardingWaiting, DispatchStatus: "waiting", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}

	redisServer := miniredis.RunT(t)
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = queue.Close() })
	service := NewService(db, queue, nil)
	service.now = func() time.Time { return now }
	if err := service.DispatchICloudOnboardingTasks(context.Background(), 100); err != nil {
		t.Fatal(err)
	}

	if err := db.First(&terminal, terminal.ID).Error; err != nil {
		t.Fatal(err)
	}
	if terminal.Status != "partial" || terminal.CompletedCount != 1 || terminal.FailedCount != 1 {
		t.Fatalf("terminal import was not reconciled: %+v", terminal)
	}
	if err := db.First(&waiting, waiting.ID).Error; err != nil {
		t.Fatal(err)
	}
	if waiting.Status != iCloudOnboardingProcessing || waiting.WaitingCount != 1 {
		t.Fatalf("waiting import summary was not reconciled: %+v", waiting)
	}
}

func TestGetAdminICloudOnboardingImportDoesNotTouchCurrentSummary(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-onboarding-current-summary?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&iCloudOnboardingImportModel{}, &iCloudOnboardingTaskModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 17, 13, 0, 0, 0, time.UTC)
	batch := iCloudOnboardingImportModel{
		Status: iCloudOnboardingProcessing, AcceptedCount: 1, WaitingCount: 1,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	task := iCloudOnboardingTaskModel{
		ImportID: &batch.ID, Status: iCloudOnboardingWaiting, DispatchStatus: "waiting",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	view, err := service.GetAdminICloudOnboardingImport(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.UpdatedAt.Equal(batch.UpdatedAt) {
		t.Fatalf("unchanged summary updated_at changed: got %s want %s", view.UpdatedAt, batch.UpdatedAt)
	}
}

func TestGetAdminICloudOnboardingImportReconcilesStaleSummary(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-onboarding-stale-summary?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&iCloudOnboardingImportModel{}, &iCloudOnboardingTaskModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 17, 14, 0, 0, 0, time.UTC)
	batch := iCloudOnboardingImportModel{
		Status: iCloudOnboardingProcessing, AcceptedCount: 1,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	task := iCloudOnboardingTaskModel{
		ImportID: &batch.ID, Status: iCloudOnboardingCompleted, DispatchStatus: "succeeded",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	view, err := service.GetAdminICloudOnboardingImport(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != iCloudOnboardingCompleted || view.Completed != 1 || !view.UpdatedAt.Equal(now) {
		t.Fatalf("stale summary was not reconciled: %+v", view)
	}
}
