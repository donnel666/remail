package icloud

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

func newOnboardingResourceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func createOnboardingResource(t *testing.T, db *gorm.DB, owner uint, importID *uint, status, dispatch string, now time.Time) iCloudResourceModel {
	t.Helper()
	root := iCloudRootModel{Type: "icloud", OwnerUserID: owner, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	resource := iCloudResourceModel{
		ID: root.ID, ResourceType: "icloud", PrimaryEmail: fmt.Sprintf("onboarding-%d@example.com", root.ID),
		AccountRole: "child", Status: iCloudResourcePending, ExpireAt: now.Add(time.Hour),
		WorkflowImportID: importID, WorkflowResourceID: &root.ID, WorkflowTaskKind: "onboarding",
		OnboardingStatus: status, WorkflowStage: "accepted", WorkflowDispatchStatus: dispatch,
		WorkflowGeneration: 1, WorkflowMaxAttempts: iCloudOnboardingMaxAttempts,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	return resource
}

func TestDispatchICloudOnboardingTasksUsesResourceRows(t *testing.T) {
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	db := newOnboardingResourceTestDB(t)
	importID := uint(42)
	createOnboardingResource(t, db, 1, &importID, iCloudOnboardingWaiting, "pending", now)

	redisServer := miniredis.RunT(t)
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = queue.Close() })
	service := NewService(db, queue, nil)
	service.now = func() time.Time { return now }
	if err := service.DispatchICloudOnboardingTasks(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource).Error; err != nil {
		t.Fatal(err)
	}
	if resource.WorkflowDispatchStatus != "queued" {
		t.Fatalf("resource workflow was not queued: %+v", resource)
	}
}

func TestGetAdminICloudOnboardingImportAggregatesResourceRows(t *testing.T) {
	now := time.Date(2026, time.August, 17, 14, 0, 0, 0, time.UTC)
	db := newOnboardingResourceTestDB(t)
	importID := uint(99)
	refreshed := createOnboardingResource(t, db, 7, &importID, iCloudOnboardingCompleted, "succeeded", now.Add(-time.Minute))
	if err := db.Model(&refreshed).Updates(map[string]any{
		"task_kind": "refresh", "onboarding_status": iCloudOnboardingFailed, "dispatch_status": "failed",
		"last_safe_error": "refresh failed",
	}).Error; err != nil {
		t.Fatal(err)
	}
	createOnboardingResource(t, db, 7, &importID, iCloudOnboardingWaiting, "waiting", now)
	createOnboardingResource(t, db, 7, &importID, iCloudOnboardingFailed, "failed", now)

	service := NewService(db, nil, nil)
	view, err := service.GetAdminICloudOnboardingImport(context.Background(), importID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Accepted != 3 || view.Completed != 1 || view.Waiting != 1 || view.Failed != 1 || view.Status != iCloudOnboardingProcessing {
		t.Fatalf("unexpected resource-derived import view: %+v", view)
	}
	if view.Tasks[0].TaskKind != "onboarding" || view.Tasks[0].Status != iCloudOnboardingCompleted || view.Tasks[0].LastSafeError != "" {
		t.Fatalf("refresh rewrote onboarding history: %+v", view.Tasks[0])
	}
}
