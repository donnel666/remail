package icloud

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestApplyICloudIMAPValidationResultOwnsOnlyResourceHealth(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	retryAt := now.Add(iCloudValidationRetryInterval)
	tests := []struct {
		name              string
		result            iCloudIMAPValidationResult
		expireAt          time.Time
		wantStatus        string
		wantProvision     bool
		wantValidationAt  bool
		wantFailures      uint8
		wantLastValidTime bool
	}{
		{name: "healthy account provisions", result: iCloudIMAPValidationResult{Status: iCloudResourceNormal}, expireAt: now.Add(time.Hour), wantStatus: iCloudResourceNormal, wantProvision: true, wantLastValidTime: true},
		{name: "expired account still healthy", result: iCloudIMAPValidationResult{Status: iCloudResourceNormal}, expireAt: now.Add(-time.Hour), wantStatus: iCloudResourceNormal, wantLastValidTime: true},
		{name: "authentication failure", result: iCloudIMAPValidationResult{Status: iCloudResourceAbnormal, Message: "bad app password", NextAt: &retryAt}, expireAt: now.Add(time.Hour), wantStatus: iCloudResourceAbnormal, wantValidationAt: true, wantFailures: 2},
		{name: "temporary failure", result: iCloudIMAPValidationResult{Status: iCloudResourcePending, Message: "temporary", NextAt: &retryAt}, expireAt: now.Add(time.Hour), wantStatus: iCloudResourcePending, wantValidationAt: true, wantFailures: 2},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:icloud-imap-validation-%d?mode=memory&cache=shared", index)), &gorm.Config{})
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudMaintenanceRunModel{}); err != nil {
				t.Fatalf("migrate database: %v", err)
			}
			if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				t.Fatalf("create root: %v", err)
			}
			initialProvisionAt := now.Add(-time.Minute)
			if err := db.Create(&iCloudResourceModel{
				ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
				Status: iCloudResourceValidating, ExpireAt: test.expireAt, AliasCount: 10,
				CredentialRevision: 4, CredentialUpdatedAt: now, ValidationGeneration: 5, ValidationFailures: 1,
				NextProvisionAt: &initialProvisionAt, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("create resource: %v", err)
			}
			if err := db.Create(&iCloudResourceChannelModel{
				ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com", Cookie: "secret",
				SessionStatus: iCloudSessionInvalid, SessionFailures: 3, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("create channel: %v", err)
			}

			service := NewService(db, nil, nil)
			service.now = func() time.Time { return now }
			if err := service.applyICloudIMAPValidationResult(context.Background(), iCloudValidationTask{
				ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4,
			}, test.result); err != nil {
				t.Fatalf("apply validation result: %v", err)
			}

			var resource iCloudResourceModel
			if err := db.First(&resource, 1).Error; err != nil {
				t.Fatalf("read resource: %v", err)
			}
			if resource.Status != test.wantStatus || (resource.NextProvisionAt != nil) != test.wantProvision ||
				(resource.NextValidationAt != nil) != test.wantValidationAt || resource.ValidationFailures != test.wantFailures ||
				(resource.LastValidAt != nil) != test.wantLastValidTime {
				t.Fatalf("unexpected resource state: %#v", resource)
			}
			var channel iCloudResourceChannelModel
			if err := db.Where("resource_id = ?", 1).First(&channel).Error; err != nil {
				t.Fatalf("read channel: %v", err)
			}
			if channel.SessionStatus != iCloudSessionInvalid || channel.SessionFailures != 3 || channel.Cookie != "secret" {
				t.Fatalf("validation changed provisioning session: %#v", channel)
			}
		})
	}
}

func TestICloudValidationRetryReclaimsFinishedRun(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	db, err := gorm.Open(sqlite.Open("file:icloud-validation-retry?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
		Status: iCloudResourcePending, ExpireAt: now.Add(time.Hour), CredentialRevision: 4,
		ValidationGeneration: 5, NextValidationAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	finishedAt := now.Add(-time.Minute)
	if err := db.Create(&iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 5, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceFailed, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: 4, QueuedAt: now.Add(-time.Minute), FinishedAt: &finishedAt,
		LastSafeError: "temporary", CreatedAt: now.Add(-time.Minute), UpdatedAt: finishedAt,
	}).Error; err != nil {
		t.Fatalf("create maintenance run: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	task, claimed, err := service.markICloudValidationDispatched(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4,
	})
	if err != nil || !claimed || task.MaintenanceRunID == 0 {
		t.Fatalf("retry claim = %#v, %t, %v", task, claimed, err)
	}
	var run iCloudMaintenanceRunModel
	if err := db.First(&run, task.MaintenanceRunID).Error; err != nil {
		t.Fatalf("read maintenance run: %v", err)
	}
	if run.Status != iCloudMaintenanceRunning || run.Attempts != 2 || run.FinishedAt != nil || run.LastSafeError != "" {
		t.Fatalf("unexpected maintenance run: %#v", run)
	}
}
