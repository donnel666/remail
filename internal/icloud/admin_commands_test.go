package icloud

import (
	"context"
	"errors"
	"testing"
	"time"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestApplyAdminICloudCommandFencesLifecycleAndProtectsActiveAllocation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-commands?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAdminTestUser{},
		&governanceinfra.OperationLogModel{},
		&coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudAdminTestUser{ID: 7, Email: "owner@example.com", Status: "active", Role: "supplier"}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "main@icloud.com", Status: iCloudResourceNormal,
		SessionStatus: iCloudSessionValid, CredentialRevision: 1, ValidationGeneration: 1,
		ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	service := NewService(db, nil, nil)
	ctx := context.Background()

	disabled, err := service.ApplyAdminICloudCommand(ctx, AdminICloudDisable, 1, 1, 99, "key-1", "request-1", "/disable")
	if err != nil || disabled.Status != iCloudResourceDisabled || disabled.Version != 2 {
		t.Fatalf("disable result = %#v err=%v", disabled, err)
	}
	replayed, err := service.ApplyAdminICloudCommand(ctx, AdminICloudDisable, 1, 1, 99, "key-1", "request-retry", "/disable")
	if err != nil || *replayed != *disabled {
		t.Fatalf("disable replay = %#v err=%v", replayed, err)
	}
	enabled, err := service.ApplyAdminICloudCommand(ctx, AdminICloudEnable, 1, 2, 99, "key-2", "request-2", "/enable")
	if err != nil || enabled.Status != iCloudResourcePending || enabled.ForSale || enabled.Version != 3 {
		t.Fatalf("enable result = %#v err=%v", enabled, err)
	}
	if _, err := service.ApplyAdminICloudCommand(ctx, AdminICloudValidate, 1, 2, 99, "key-stale", "stale", "/validation"); !errors.Is(err, ErrICloudResourceVersion) {
		t.Fatalf("stale command error = %v", err)
	}

	published, err := service.ApplyAdminICloudCommand(ctx, AdminICloudPublish, 1, 3, 99, "key-3", "request-3", "/publish")
	if err != nil || !published.ForSale || published.Version != 4 {
		t.Fatalf("publish result = %#v err=%v", published, err)
	}
	private, err := service.ApplyAdminICloudCommand(ctx, AdminICloudUnpublish, 1, 4, 99, "key-4", "request-4", "/unpublish")
	if err != nil || private.ForSale || private.Version != 5 {
		t.Fatalf("unpublish result = %#v err=%v", private, err)
	}
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Update("status", iCloudResourceDeleted).Error; err != nil {
		t.Fatalf("mark resource deleted for recovery test: %v", err)
	}
	recovered, err := service.ApplyAdminICloudCommand(ctx, AdminICloudRecover, 1, 5, 99, "key-5", "request-5", "/recover")
	if err != nil || recovered.Status != iCloudResourcePending || recovered.ForSale || recovered.Version != 6 {
		t.Fatalf("recover result = %#v err=%v", recovered, err)
	}
	var logs int64
	if err := db.Model(&governanceinfra.OperationLogModel{}).Count(&logs).Error; err != nil {
		t.Fatalf("count operation logs: %v", err)
	}
	if logs != 5 {
		t.Fatalf("operation log count = %d, want 5 successful commands", logs)
	}
}

func TestApplyAdminICloudBatchReportsSkippedReasons(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-batch?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAdminTestUser{},
		&governanceinfra.OperationLogModel{},
		&coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudAdminTestUser{ID: 7, Email: "owner@example.com", Status: "active", Role: "supplier"}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	for id, status := range map[uint]string{1: iCloudResourceNormal, 2: iCloudResourceDeleted} {
		if err := db.Create(&iCloudRootModel{ID: id, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("create root %d: %v", id, err)
		}
		if err := db.Create(&iCloudResourceModel{ID: id, ResourceType: "icloud", PrimaryEmail: "main" + string(rune('0'+id)) + "@icloud.com", Status: status, CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("create resource %d: %v", id, err)
		}
	}
	service := NewService(db, nil, nil)
	result, err := service.ApplyAdminICloudBatch(context.Background(), AdminICloudDisable, AdminICloudResourceSelection{Mode: "ids", ResourceIDs: []uint{2, 1, 1}}, 99, "batch-key-1", "batch-1", "/batch/disable")
	if err != nil {
		t.Fatalf("batch disable: %v", err)
	}
	if result.Requested != 2 || result.Affected != 1 || result.Skipped != 1 || len(result.AffectedResourceIDs) != 1 || len(result.SkippedResourceIDs) != 1 {
		t.Fatalf("unexpected batch result: %#v", result)
	}
	if len(result.ReasonCounts) != 1 || result.ReasonCounts[0].Reason != "not_found" || result.ReasonCounts[0].Count != 1 {
		t.Fatalf("unexpected skip reasons: %#v", result.ReasonCounts)
	}
}

func TestNormalizeAdminICloudSelectionRejectsContradictoryFields(t *testing.T) {
	filter := AdminICloudResourceListFilter{}
	for _, selection := range []AdminICloudResourceSelection{
		{Mode: "ids", ResourceIDs: []uint{1}, Filter: &filter},
		{Mode: "ids", ResourceIDs: []uint{0, 1}},
		{Mode: "filter", ResourceIDs: []uint{1}, Filter: &filter},
	} {
		if _, err := normalizeAdminICloudSelection(selection); !errors.Is(err, ErrICloudResourceSelection) {
			t.Fatalf("selection %#v error = %v", selection, err)
		}
	}
}
