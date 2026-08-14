package icloud

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestApplyAdminICloudValidationResetsOnlyHealthScheduling(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-validate")
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	nextProvisionAt := now.Add(time.Minute)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 2,
		ValidationGeneration: 3, NextProvisionAt: &nextProvisionAt,
	})
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com", Cookie: "secret",
		SessionStatus: iCloudSessionInvalid, SessionFailures: 4, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	result, err := service.ApplyAdminICloudCommand(
		context.Background(), AdminICloudValidate, 1, 1, 99,
		"validate-1", "request-1", "/v1/admin/icloud/resources/1/validation",
	)
	if err != nil {
		t.Fatalf("validate command: %v", err)
	}
	if !result.Changed || result.Status != iCloudResourcePending || result.Version != 2 {
		t.Fatalf("unexpected mutation result: %#v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.ValidationGeneration != 4 ||
		resource.NextValidationAt == nil || resource.NextProvisionAt != nil {
		t.Fatalf("unexpected validation state: %#v", resource)
	}
	var channel iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", 1).First(&channel).Error; err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if channel.SessionStatus != iCloudSessionInvalid || channel.SessionFailures != 4 || channel.Cookie != "secret" {
		t.Fatalf("validation changed provisioning channel: %#v", channel)
	}
}

func TestEditAdminICloudResourceUsesCompleteImportLine(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-edit")
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "old-app-password",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1, ValidationGeneration: 1,
	})
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com", Cookie: "old-secret",
		SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create old channel: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	line := "owner@icloud.com----new-app-password----" + testICloudNewCurl + "----" + testICloudOldCurl
	expireAt := now.Add(48 * time.Hour)
	result, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, ImportLine: &line, ExpireAt: &expireAt,
		OperatorUserID: 99, IdempotencyKey: "edit-1", RequestID: "request-1", Path: "/v1/admin/icloud/resources/1",
	})
	if err != nil {
		t.Fatalf("edit credentials: %v", err)
	}
	if !result.Changed || result.Version != 2 || result.Status != iCloudResourcePending {
		t.Fatalf("unexpected edit result: %#v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.IMAPAppPassword != "new-app-password" || resource.CredentialRevision != 2 ||
		resource.ValidationGeneration != 2 || resource.Status != iCloudResourcePending ||
		resource.NextValidationAt == nil || resource.NextProvisionAt != nil || !resource.ExpireAt.Equal(expireAt) {
		t.Fatalf("unexpected edited resource: %#v", resource)
	}
	var channels []iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", 1).Order("kind").Find(&channels).Error; err != nil {
		t.Fatalf("read channels: %v", err)
	}
	if len(channels) != 2 || channels[0].Kind != iCloudChannelAppleAccount || channels[1].Kind != iCloudChannelWeb {
		t.Fatalf("unexpected edited channels: %#v", channels)
	}
	for _, channel := range channels {
		if channel.SessionStatus != iCloudSessionUnchecked {
			t.Fatalf("edited channel was not reset: %#v", channel)
		}
	}

	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Updates(map[string]any{"status": iCloudResourceNormal, "next_validation_at": nil}).Error; err != nil {
		t.Fatalf("mark IMAP healthy: %v", err)
	}
	now = now.Add(time.Minute)
	rotated := strings.Replace(testICloudNewCurl, "myacinfo=secret", "myacinfo=rotated", 1)
	line = "owner@icloud.com----new-app-password----" + rotated
	result, err = service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 2, ImportLine: &line,
		OperatorUserID: 99, IdempotencyKey: "edit-2", RequestID: "request-2", Path: "/v1/admin/icloud/resources/1",
	})
	if err != nil {
		t.Fatalf("edit channel only: %v", err)
	}
	if result.Status != iCloudResourceNormal {
		t.Fatalf("cookie edit changed resource health: %#v", result)
	}
	var channelOnlyResource iCloudResourceModel
	if err := db.First(&channelOnlyResource, 1).Error; err != nil {
		t.Fatalf("read channel-only edit: %v", err)
	}
	if channelOnlyResource.Status != iCloudResourceNormal || channelOnlyResource.ValidationGeneration != 2 ||
		channelOnlyResource.NextValidationAt != nil || channelOnlyResource.NextProvisionAt == nil {
		t.Fatalf("channel-only edit changed IMAP state: %#v", channelOnlyResource)
	}
	channels = nil
	if err := db.Where("resource_id = ?", 1).Find(&channels).Error; err != nil {
		t.Fatalf("read replacement channel: %v", err)
	}
	if len(channels) != 1 || channels[0].Kind != iCloudChannelAppleAccount || !strings.Contains(channels[0].Cookie, "rotated") {
		t.Fatalf("import line did not atomically replace channels: %#v", channels)
	}
}

func TestAdminICloudExpirationStopsProvisioningWithoutChangingHealth(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-expire")
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
		Status: iCloudResourceNormal, ExpireAt: now.Add(-time.Minute), CredentialRevision: 1,
		ValidationGeneration: 1,
	})
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	result, err := service.ApplyAdminICloudCommand(
		context.Background(), AdminICloudAlias, 1, 1, 99,
		"alias-expired-1", "request-1", "/v1/admin/icloud/resources/1/aliases",
	)
	if err != nil {
		t.Fatalf("request alias on expired resource: %v", err)
	}
	if result.Changed || result.Status != iCloudResourceNormal {
		t.Fatalf("unexpected expired alias result: %#v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read expired resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.NextProvisionAt != nil {
		t.Fatalf("expiration changed resource health: %#v", resource)
	}
	past := now.Add(-time.Hour)
	if _, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, ExpireAt: &past, OperatorUserID: 99, IdempotencyKey: "past-expire",
	}); !errors.Is(err, ErrICloudResourceUpdate) {
		t.Fatalf("past expiration error = %v", err)
	}
	if _, err := service.ApplyAdminICloudBatch(
		context.Background(), AdminICloudExpire, AdminICloudResourceSelection{Mode: "ids", ResourceIDs: []uint{1}},
		&past, 99, "past-expire-batch", "request-2", "/v1/admin/icloud/resources/expiration",
	); !errors.Is(err, ErrICloudResourceUpdate) {
		t.Fatalf("past batch expiration error = %v", err)
	}
}

func TestAdminICloudAliasRequiresUsableChannel(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-alias-invalid-channel")
	now := time.Date(2026, 8, 14, 10, 30, 0, 0, time.UTC)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1, ValidationGeneration: 1,
	})
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com", Cookie: "expired",
		SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create invalid channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	result, err := service.ApplyAdminICloudCommand(
		context.Background(), AdminICloudAlias, 1, 1, 99,
		"alias-invalid-channel", "request-1", "/v1/admin/icloud/resources/1/aliases",
	)
	if err != nil {
		t.Fatalf("request alias: %v", err)
	}
	if result.Changed {
		t.Fatalf("invalid channel queued provisioning: %#v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil || resource.NextProvisionAt != nil {
		t.Fatalf("invalid channel left provisioning scheduled: resource=%#v err=%v", resource, err)
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

func newAdminICloudCommandTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudMaintenanceRunModel{},
		&governanceinfra.OperationLogModel{}, &coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func createAdminICloudCommandResource(t *testing.T, db *gorm.DB, now time.Time, resource iCloudResourceModel) {
	t.Helper()
	if err := db.Create(&iCloudRootModel{ID: resource.ID, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	resource.CreatedAt = now
	resource.UpdatedAt = now
	resource.CredentialUpdatedAt = now
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
}
