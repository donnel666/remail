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
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com",
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
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1, ValidationGeneration: 1,
	})
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com", Cookie: "old-secret",
		SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create old channel: %v", err)
	}
	if err := db.Create(&iCloudAliasModel{
		ResourceID: 1, AnonymousID: "existing-alias", Email: "existing@icloud.com", Status: iCloudResourceNormal,
		ForwardToEmail: "old@relay.example", RecipientMailID: "relay-id", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create existing alias: %v", err)
	}
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"selected_forward_to": "old@relay.example", "alias_count": 1, "last_alias_sync_at": now,
	}).Error; err != nil {
		t.Fatalf("seed old forwarding identity: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	line := "owner@icloud.com----" + testICloudNewCurl + "----" + testICloudOldCurl
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
	if resource.CredentialRevision != 2 ||
		resource.ValidationGeneration != 2 || resource.Status != iCloudResourcePending ||
		resource.NextValidationAt == nil || resource.NextProvisionAt != nil || !resource.ExpireAt.Equal(expireAt) {
		t.Fatalf("unexpected edited resource: %#v", resource)
	}
	var channels []iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", 1).Order("kind").Find(&channels).Error; err != nil {
		t.Fatalf("read channels: %v", err)
	}
	if len(channels) != 2 || channels[0].Kind != iCloudChannelAppleAccount || channels[1].Kind != iCloudChannelWeb ||
		channels[0].FDClientInfo != testICloudFDClientInfo {
		t.Fatalf("unexpected edited channels: %#v", channels)
	}
	for _, channel := range channels {
		if channel.SessionStatus != iCloudSessionUnchecked {
			t.Fatalf("edited channel was not reset: %#v", channel)
		}
	}

	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Updates(map[string]any{"status": iCloudResourceNormal, "next_validation_at": nil}).Error; err != nil {
		t.Fatalf("mark resource healthy: %v", err)
	}
	now = now.Add(time.Minute)
	rotated := strings.Replace(testICloudNewCurl, "myacinfo=secret", "myacinfo=rotated", 1)
	line = "owner@icloud.com----" + rotated
	result, err = service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 2, ImportLine: &line,
		OperatorUserID: 99, IdempotencyKey: "edit-2", RequestID: "request-2", Path: "/v1/admin/icloud/resources/1",
	})
	if err != nil {
		t.Fatalf("edit channel only: %v", err)
	}
	if result.Status != iCloudResourcePending {
		t.Fatalf("cookie edit did not requeue validation: %#v", result)
	}
	var channelOnlyResource iCloudResourceModel
	if err := db.First(&channelOnlyResource, 1).Error; err != nil {
		t.Fatalf("read channel-only edit: %v", err)
	}
	if channelOnlyResource.Status != iCloudResourcePending || channelOnlyResource.ValidationGeneration != 3 ||
		channelOnlyResource.NextValidationAt == nil || channelOnlyResource.NextProvisionAt != nil ||
		channelOnlyResource.SelectedForwardTo != "old@relay.example" || channelOnlyResource.AliasCount != 1 || channelOnlyResource.LastAliasSyncAt == nil {
		t.Fatalf("channel-only edit did not requeue validation: %#v", channelOnlyResource)
	}
	var retiredAlias iCloudAliasModel
	if err := db.Where("resource_id = ? AND anonymous_id = ?", 1, "existing-alias").Take(&retiredAlias).Error; err != nil || retiredAlias.Status != iCloudResourceNormal {
		t.Fatalf("same-account channel edit hid existing alias: alias=%#v err=%v", retiredAlias, err)
	}
	channels = nil
	if err := db.Where("resource_id = ?", 1).Find(&channels).Error; err != nil {
		t.Fatalf("read replacement channel: %v", err)
	}
	if len(channels) != 1 || channels[0].Kind != iCloudChannelAppleAccount || !strings.Contains(channels[0].Cookie, "rotated") {
		t.Fatalf("import line did not atomically replace channels: %#v", channels)
	}

	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"selected_forward_to": "relay@example.com", "alias_count": 1,
		"alias_provision_candidate": "candidate@icloud.com", "alias_provision_reconcile": true,
		"last_alias_sync_at": now,
	}).Error; err != nil {
		t.Fatalf("seed old account inventory: %v", err)
	}
	if err := db.Create(&iCloudAliasModel{
		ResourceID: 1, AnonymousID: "old-alias-id", Email: "old@icloud.com", Status: iCloudResourceNormal,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create old alias: %v", err)
	}
	now = now.Add(time.Minute)
	line = "renamed@icloud.com----" + rotated
	if _, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 3, ImportLine: &line,
		OperatorUserID: 99, IdempotencyKey: "edit-3", RequestID: "request-3", Path: "/v1/admin/icloud/resources/1",
	}); err != nil {
		t.Fatalf("edit Apple account email: %v", err)
	}
	var renamed iCloudResourceModel
	if err := db.First(&renamed, 1).Error; err != nil {
		t.Fatalf("read renamed resource: %v", err)
	}
	if renamed.PrimaryEmail != "renamed@icloud.com" || renamed.SelectedForwardTo != "" || renamed.AliasCount != 0 ||
		renamed.AliasProvisionCandidate != "" || renamed.AliasProvisionReconcile || renamed.LastAliasSyncAt != nil {
		t.Fatalf("email edit retained old account inventory: %#v", renamed)
	}
	var oldAlias iCloudAliasModel
	if err := db.Where("resource_id = ?", 1).First(&oldAlias).Error; err != nil || oldAlias.Status != "missing" {
		t.Fatalf("old alias was not retired: alias=%#v err=%v", oldAlias, err)
	}
}

func TestEditAdminICloudResourceAlwaysReplacesSubmittedCredentials(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-edit-same-credentials")
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com",
		Status: iCloudResourceAbnormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1, ValidationGeneration: 1,
		SelectedForwardTo: "mailbox@relay.example", AliasCount: 1, LastAliasSyncAt: &now,
	})
	if err := db.Create(&iCloudAliasModel{
		ResourceID: 1, AnonymousID: "alias-id", Email: "alias@icloud.com", ForwardToEmail: "mailbox@relay.example",
		Status: iCloudResourceNormal, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create existing alias: %v", err)
	}
	line := "owner@icloud.com----" + testICloudNewCurl
	parsed, failure := parseICloudCurlImportLine(1, line)
	if failure != nil || parsed == nil || len(parsed.Channels) != 1 {
		t.Fatalf("parse fixture credentials: line=%#v failure=%#v", parsed, failure)
	}
	input := parsed.Channels[0]
	manageExpiresAt := now.Add(15 * time.Minute)
	cooldownUntil := now.Add(time.Hour)
	channel := iCloudResourceChannelModel{
		ResourceID: 1, Kind: input.Kind, Host: input.Host, Cookie: input.Cookie,
		Origin: input.Origin, Referer: input.Referer, UserAgent: input.UserAgent,
		FDClientInfo: input.FDClientInfo, Scnt: input.Scnt,
		SessionID: "stale-session", APIKey: "stale-api-key", DataAccessToken: "stale-token",
		ManageExpiresAt: &manageExpiresAt, SessionStatus: iCloudSessionInvalid, SessionFailures: 3,
		CooldownUntil: &cooldownUntil, CooldownStage: 2, NextKeepaliveAt: &cooldownUntil,
		LastCheckedAt: &now, LastValidAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create existing channel: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	result, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, ImportLine: &line,
		OperatorUserID: 99, IdempotencyKey: "edit-same-credentials", RequestID: "request-same", Path: "/v1/admin/icloud/resources/1",
	})
	if err != nil {
		t.Fatalf("replace matching credentials: %v", err)
	}
	if !result.Changed || result.Status != iCloudResourcePending {
		t.Fatalf("matching credential replacement result: %#v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read replaced resource: %v", err)
	}
	if resource.CredentialRevision != 2 || resource.ValidationGeneration != 2 || resource.NextValidationAt == nil {
		t.Fatalf("matching credentials did not queue a fresh validation: %#v", resource)
	}
	var stored iCloudResourceChannelModel
	if err := db.Where("resource_id = ? AND kind = ?", 1, input.Kind).Take(&stored).Error; err != nil {
		t.Fatalf("read replaced channel: %v", err)
	}
	if resource.SelectedForwardTo != "mailbox@relay.example" || resource.AliasCount != 1 || resource.LastAliasSyncAt == nil {
		t.Fatalf("same-account edit reset alias state: %#v", resource)
	}
	if stored.SessionStatus != iCloudSessionUnchecked || stored.SessionFailures != 0 || stored.CooldownUntil != nil ||
		stored.CooldownStage != 0 || stored.NextKeepaliveAt != nil || stored.SessionID != "" || stored.APIKey != "" ||
		stored.DataAccessToken != "" || stored.ManageExpiresAt != nil || stored.LastCheckedAt != nil || stored.LastValidAt != nil {
		t.Fatalf("submitted credentials did not reset channel runtime state: %#v", stored)
	}
	var alias iCloudAliasModel
	if err := db.Where("resource_id = ?", 1).Take(&alias).Error; err != nil || alias.Status != iCloudResourceNormal {
		t.Fatalf("same-account edit hid existing alias: alias=%#v err=%v", alias, err)
	}
}

func TestAdminICloudExpirationStopsProvisioningWithoutChangingHealth(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-expire")
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com",
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
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com",
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

type iCloudAdminCommandAllocation struct {
	ID         uint   `gorm:"primaryKey"`
	ResourceID uint   `gorm:"column:resource_id"`
	Status     string `gorm:"column:status"`
}

func (iCloudAdminCommandAllocation) TableName() string { return "icloud_allocations" }

func newAdminICloudCommandTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}, &iCloudAdminCommandAllocation{},
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
