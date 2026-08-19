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

func TestEditAdminICloudResourcePatchesSubmittedChannels(t *testing.T) {
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
	if !result.Changed || result.Version != 2 || result.Status != iCloudResourceNormal {
		t.Fatalf("unexpected edit result: %#v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.CredentialRevision != 2 ||
		resource.ValidationGeneration != 2 || resource.Status != iCloudResourceNormal ||
		resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(now) || resource.NextProvisionAt != nil || !resource.ExpireAt.Equal(expireAt) {
		t.Fatalf("unexpected edited resource: %#v", resource)
	}
	var channels []iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", 1).Order("kind").Find(&channels).Error; err != nil {
		t.Fatalf("read channels: %v", err)
	}
	if len(channels) != 2 || channels[0].Kind != iCloudChannelAppleAccount || channels[1].Kind != iCloudChannelWeb ||
		channels[0].FDClientInfo != testICloudFDClientInfo || channels[0].Scnt != testICloudLongScnt || channels[0].APIKey != "api-key" ||
		channels[0].ManageExpiresAt == nil || !channels[0].ManageExpiresAt.Equal(now.Add(iCloudImportedAppleManageTTL)) {
		t.Fatalf("unexpected edited channels: %#v", channels)
	}
	for _, channel := range channels {
		if channel.SessionStatus != iCloudSessionUnchecked {
			t.Fatalf("edited channel was not reset: %#v", channel)
		}
	}

	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Update("next_validation_at", nil).Error; err != nil {
		t.Fatalf("finish credential check: %v", err)
	}
	now = now.Add(time.Minute)
	if err := db.Model(&iCloudResourceChannelModel{}).
		Where("resource_id = ? AND kind = ?", 1, iCloudChannelAppleAccount).
		Updates(map[string]any{"session_status": iCloudSessionValid, "api_key": "preserved-api-key"}).Error; err != nil {
		t.Fatalf("mark omitted channel valid: %v", err)
	}
	rotated := strings.Replace(testICloudOldCurl, "X-APPLE-WEBAUTH-TOKEN=token", "X-APPLE-WEBAUTH-TOKEN=rotated", 1)
	line = "OWNER@ICLOUD.COM----" + rotated
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
	if channelOnlyResource.Status != iCloudResourceNormal || channelOnlyResource.ValidationGeneration != 3 ||
		channelOnlyResource.NextValidationAt == nil || !channelOnlyResource.NextValidationAt.Equal(now) || channelOnlyResource.NextProvisionAt != nil ||
		channelOnlyResource.SelectedForwardTo != "old@relay.example" || channelOnlyResource.AliasCount != 1 || channelOnlyResource.LastAliasSyncAt == nil {
		t.Fatalf("channel-only edit did not queue a credential check: %#v", channelOnlyResource)
	}
	var retiredAlias iCloudAliasModel
	if err := db.Where("resource_id = ? AND anonymous_id = ?", 1, "existing-alias").Take(&retiredAlias).Error; err != nil || retiredAlias.Status != iCloudResourceNormal {
		t.Fatalf("same-account channel edit hid existing alias: alias=%#v err=%v", retiredAlias, err)
	}
	channels = nil
	if err := db.Where("resource_id = ?", 1).Order("kind").Find(&channels).Error; err != nil {
		t.Fatalf("read replacement channel: %v", err)
	}
	if len(channels) != 2 || channels[0].Kind != iCloudChannelAppleAccount || channels[1].Kind != iCloudChannelWeb {
		t.Fatalf("partial edit did not preserve both channels: %#v", channels)
	}
	if channels[0].Cookie != testICloudNewCookie || channels[0].SessionStatus != iCloudSessionValid || channels[0].APIKey != "preserved-api-key" {
		t.Fatalf("partial edit changed omitted new channel: %#v", channels[0])
	}
	if !strings.Contains(channels[1].Cookie, "X-APPLE-WEBAUTH-TOKEN=rotated") || channels[1].SessionStatus != iCloudSessionUnchecked {
		t.Fatalf("partial edit did not replace submitted old channel: %#v", channels[1])
	}

	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"selected_forward_to": "relay@example.com", "alias_count": 1,
		"alias_provision_candidate": "candidate@icloud.com", "alias_provision_reconcile": true,
		"last_alias_sync_at": now,
	}).Error; err != nil {
		t.Fatalf("seed old account inventory: %v", err)
	}
	now = now.Add(time.Minute)
	rotatedNew := strings.Replace(testICloudNewCurl, "myacinfo=secret", "myacinfo=rotated", 1)
	line = "owner@icloud.com----" + rotatedNew
	if _, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 3, ImportLine: &line,
		OperatorUserID: 99, IdempotencyKey: "edit-3", RequestID: "request-3", Path: "/v1/admin/icloud/resources/1",
	}); err != nil {
		t.Fatalf("edit new channel only: %v", err)
	}
	var newChannelOnlyResource iCloudResourceModel
	if err := db.First(&newChannelOnlyResource, 1).Error; err != nil {
		t.Fatalf("read new-channel-only edit: %v", err)
	}
	if newChannelOnlyResource.AliasProvisionCandidate != "candidate@icloud.com" || !newChannelOnlyResource.AliasProvisionReconcile {
		t.Fatalf("new-channel edit cleared old-channel candidate: %#v", newChannelOnlyResource)
	}
	channels = nil
	if err := db.Where("resource_id = ?", 1).Order("kind").Find(&channels).Error; err != nil {
		t.Fatalf("read new-channel-only credentials: %v", err)
	}
	if len(channels) != 2 || !strings.Contains(channels[0].Cookie, "myacinfo=rotated") ||
		!strings.Contains(channels[1].Cookie, "X-APPLE-WEBAUTH-TOKEN=rotated") {
		t.Fatalf("new-channel edit changed the wrong credentials: %#v", channels)
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
		ResourceID: 1, Version: 4, ImportLine: &line,
		OperatorUserID: 99, IdempotencyKey: "edit-4", RequestID: "request-4", Path: "/v1/admin/icloud/resources/1",
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
	channels = nil
	if err := db.Where("resource_id = ?", 1).Find(&channels).Error; err != nil {
		t.Fatalf("read renamed account channels: %v", err)
	}
	if len(channels) != 1 || channels[0].Kind != iCloudChannelWeb || !strings.Contains(channels[0].Cookie, "X-APPLE-WEBAUTH-TOKEN=rotated") {
		t.Fatalf("email edit retained an omitted old-account channel: %#v", channels)
	}
}

func TestEditAdminICloudResourceRejectsOnboardedIdentityChange(t *testing.T) {
	tests := []struct {
		name      string
		role      string
		withPhone bool
	}{
		{name: "account role", role: "child"},
		{name: "permanent phone binding", role: "unknown", withPhone: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newAdminICloudCommandTestDB(t, "icloud-admin-edit-identity-"+strings.ReplaceAll(test.name, " ", "-"))
			now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
			resource := iCloudResourceModel{
				ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", AccountRole: test.role,
				Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1,
			}
			if test.withPhone {
				phoneID := uint(42)
				resource.KitesimPhoneID = &phoneID
			}
			createAdminICloudCommandResource(t, db, now, resource)

			service := NewService(db, nil, nil)
			service.now = func() time.Time { return now }
			line := "renamed@icloud.com----" + testICloudOldCurl
			_, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
				ResourceID: 1, Version: 1, ImportLine: &line,
				OperatorUserID: 99, IdempotencyKey: "reject-identity-change", RequestID: "reject-identity-change", Path: "/v1/admin/icloud/resources/1",
			})
			if !errors.Is(err, ErrICloudResourceUpdate) {
				t.Fatalf("identity change error = %v", err)
			}
			var stored iCloudResourceModel
			if err := db.First(&stored, 1).Error; err != nil || stored.PrimaryEmail != "owner@icloud.com" {
				t.Fatalf("identity change mutated resource: resource=%#v err=%v", stored, err)
			}
		})
	}
}

func TestEditAdminICloudResourceChangingInviteClearsQuarantine(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-edit-family-invite")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		FamilyInviteURL: "old-token", FamilySyncStatus: iCloudFamilySyncReady,
		FamilySyncErrorCategory: "family_invite_invalid", Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour),
	})
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }

	same := "old-token"
	result, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, FamilyInviteURL: &same,
		OperatorUserID: 99, IdempotencyKey: "same-invite", RequestID: "same-invite", Path: "/v1/admin/icloud/resources/1",
	})
	if err != nil || result.Changed || result.Version != 1 {
		t.Fatalf("same invitation edit: result=%#v err=%v", result, err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil || resource.FamilySyncErrorCategory != "family_invite_invalid" {
		t.Fatalf("same invitation removed quarantine: resource=%#v err=%v", resource, err)
	}

	replacement := "new-token"
	result, err = service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, FamilyInviteURL: &replacement,
		OperatorUserID: 99, IdempotencyKey: "replacement-invite", RequestID: "replacement-invite", Path: "/v1/admin/icloud/resources/1",
	})
	if err != nil || !result.Changed || result.Version != 2 {
		t.Fatalf("replacement invitation edit: result=%#v err=%v", result, err)
	}
	if err := db.First(&resource, 1).Error; err != nil || resource.FamilyInviteURL != replacement || resource.FamilySyncErrorCategory != "" {
		t.Fatalf("replacement invitation did not clear quarantine: resource=%#v err=%v", resource, err)
	}
}

func TestEditAdminICloudResourceRejectsInviteOnChild(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-edit-child-family-invite")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "child@example.com", AccountRole: "child",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour),
	})
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	invite := "new-token"

	_, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, FamilyInviteURL: &invite,
		OperatorUserID: 99, IdempotencyKey: "child-invite", RequestID: "child-invite", Path: "/v1/admin/icloud/resources/1",
	})
	if !errors.Is(err, ErrICloudResourceUpdate) {
		t.Fatalf("child invitation error = %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil || resource.FamilyInviteURL != "" {
		t.Fatalf("child invitation mutated resource: resource=%#v err=%v", resource, err)
	}
}

func TestEditAdminICloudResourceQueuesCredentialCheckWithoutChangingHealth(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, "icloud-admin-edit-silent-channel-check")
	now := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1, ValidationGeneration: 1,
	})
	parsed, failure := parseICloudCurlImportLine(1, "owner@icloud.com----"+testICloudNewCurl+"----"+testICloudOldCurl)
	if failure != nil || parsed == nil {
		t.Fatalf("parse initial credentials: line=%#v failure=%#v", parsed, failure)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return upsertICloudImportChannelsTx(tx, 1, parsed.Channels, true, now)
	}); err != nil {
		t.Fatalf("store initial credentials: %v", err)
	}
	if err := db.Model(&iCloudResourceChannelModel{}).Where("resource_id = ?", 1).
		Update("session_status", iCloudSessionValid).Error; err != nil {
		t.Fatalf("mark initial credentials valid: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	rotated := strings.Replace(testICloudNewCurl, "myacinfo=secret", "myacinfo=rotated", 1)
	line := "owner@icloud.com----" + rotated
	result, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, ImportLine: &line,
		OperatorUserID: 99, IdempotencyKey: "silent-check", RequestID: "request-silent", Path: "/v1/admin/icloud/resources/1",
	})
	if err != nil {
		t.Fatalf("edit new channel: %v", err)
	}
	if result.Status != iCloudResourceNormal {
		t.Fatalf("credential edit changed resource health: %#v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil || resource.NextValidationAt == nil || resource.NextProvisionAt != nil || resource.ValidationGeneration != 2 {
		t.Fatalf("credential check was not scheduled: resource=%#v err=%v", resource, err)
	}
	var channels []iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", 1).Order("kind").Find(&channels).Error; err != nil {
		t.Fatalf("read edited channels: %v", err)
	}
	if len(channels) != 2 || channels[0].SessionStatus != iCloudSessionUnchecked || channels[1].SessionStatus != iCloudSessionValid {
		t.Fatalf("edit did not isolate the submitted channel: %#v", channels)
	}
	cooldown := now.Add(time.Hour)
	if err := db.Model(&iCloudResourceChannelModel{}).
		Where("resource_id = ? AND kind = ?", 1, iCloudChannelWeb).
		Update("cooldown_until", cooldown).Error; err != nil {
		t.Fatalf("defer omitted channel: %v", err)
	}
	setICloudForwardingSuffixes(t, "relay.example")
	service.apple = newICloudValidationAppleClient(t, "silent-check-id", "mailbox@relay.example")
	tasks, err := service.iCloudValidationCandidates(context.Background(), 10)
	if err != nil || len(tasks) != 1 || !tasks[0].PreserveResourceStatus {
		t.Fatalf("list credential checks: tasks=%#v err=%v", tasks, err)
	}
	queued, claimed, err := service.markICloudValidationDispatched(context.Background(), tasks[0])
	if err != nil || !claimed || !queued.PreserveResourceStatus {
		t.Fatalf("claim credential check: task=%#v claimed=%v err=%v", queued, claimed, err)
	}
	if err := service.ProcessICloudValidation(context.Background(), queued); err != nil {
		t.Fatalf("process credential check: %v", err)
	}
	var updatedNew, preservedOld iCloudResourceChannelModel
	if err := db.Where("resource_id = ? AND kind = ?", 1, iCloudChannelAppleAccount).Take(&updatedNew).Error; err != nil {
		t.Fatalf("read verified new channel: %v", err)
	}
	if err := db.Where("resource_id = ? AND kind = ?", 1, iCloudChannelWeb).Take(&preservedOld).Error; err != nil {
		t.Fatalf("read preserved old channel: %v", err)
	}
	if updatedNew.SessionStatus != iCloudSessionValid || updatedNew.LastValidAt == nil || preservedOld.SessionStatus != iCloudSessionValid {
		t.Fatalf("silent check did not verify only the updated channel: new=%#v old=%#v", updatedNew, preservedOld)
	}
	if err := db.First(&resource, 1).Error; err != nil || resource.Status != iCloudResourceNormal {
		t.Fatalf("silent check changed resource health: resource=%#v err=%v", resource, err)
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
		stored.CooldownStage != 0 || stored.NextKeepaliveAt != nil || stored.SessionID != "" || stored.APIKey != input.APIKey ||
		stored.DataAccessToken != "" || stored.ManageExpiresAt == nil ||
		!stored.ManageExpiresAt.Equal(now.Add(iCloudImportedAppleManageTTL)) || stored.LastCheckedAt != nil || stored.LastValidAt != nil {
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
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudAliasModel{}, &iCloudAliasRouteModel{}, &iCloudMaintenanceRunModel{}, &iCloudAdminCommandAllocation{},
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
