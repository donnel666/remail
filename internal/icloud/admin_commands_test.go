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

func TestApplyAdminICloudCommandFencesLifecycleAndProtectsActiveAllocation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-commands?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAdminTestUser{},
		&iCloudMaintenanceRunModel{},
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
	var validationRun iCloudMaintenanceRunModel
	if err := db.Where("resource_id = ? AND validation_generation = ?", 1, 2).Take(&validationRun).Error; err != nil {
		t.Fatalf("load validation maintenance run: %v", err)
	}
	if validationRun.Kind != iCloudMaintenanceValidation {
		t.Fatalf("enable queued maintenance kind %q", validationRun.Kind)
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

func TestEditAdminICloudResourceUpdatesSafeFieldsAndWriteOnlyCredentials(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-edit?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAdminTestUser{},
		&iCloudImportAllocationTestModel{}, &governanceinfra.OperationLogModel{},
		&iCloudMaintenanceRunModel{},
		&coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	for _, owner := range []iCloudAdminTestUser{
		{ID: 7, Email: "old-owner@example.com", Status: "active", Role: "supplier"},
		{ID: 8, Email: "new-owner@example.com", Status: "active", Role: "supplier"},
	} {
		if err := db.Create(&owner).Error; err != nil {
			t.Fatalf("create owner: %v", err)
		}
	}
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "old@icloud.com", Host: "p119-maildomainws.icloud.com",
		DSID: "old-dsid", ClientID: "old-client", ClientBuildNumber: "old-build", ClientMasteringNumber: "old-master",
		Cookie:  "X-APPLE-DS-WEB-SESSION-TOKEN=old; X-APPLE-WEBAUTH-USER=old; X-APPLE-WEBAUTH-TOKEN=old",
		ForSale: true, Status: iCloudResourceNormal, SessionStatus: iCloudSessionValid,
		CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(30 * 24 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.SetImportOwnerValidator(func(_ context.Context, ownerID uint) (bool, error) { return ownerID == 8, nil })
	primaryEmail := " corrected@icloud.com "
	ownerID := uint(8)
	result, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, PrimaryEmail: &primaryEmail, OwnerUserID: &ownerID,
		Credentials: &AdminICloudCredentialsInput{
			Host: "p120-maildomainws.icloud.com", DSID: "new-dsid", ClientID: "new-client",
			ClientBuildNumber: "new-build", ClientMasteringNumber: "new-master",
			Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=new; X-APPLE-WEBAUTH-USER=new; X-APPLE-WEBAUTH-TOKEN=new",
		},
		OperatorUserID: 99, IdempotencyKey: "edit-key-1", RequestID: "request-edit-1", Path: "/v1/admin/icloud/resources/:resourceId",
	})
	if err != nil {
		t.Fatalf("edit resource: %v", err)
	}
	if result.Version != 2 || result.Status != iCloudResourcePending || result.ForSale {
		t.Fatalf("unexpected edit result: %#v", result)
	}
	var root iCloudRootModel
	var resource iCloudResourceModel
	if err := db.First(&root, 1).Error; err != nil {
		t.Fatalf("load root: %v", err)
	}
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("load resource: %v", err)
	}
	if root.OwnerUserID != 8 || root.Version != 2 || resource.PrimaryEmail != "corrected@icloud.com" ||
		resource.DSID != "new-dsid" || resource.CredentialRevision != 2 || resource.SessionStatus != iCloudSessionUnchecked ||
		!strings.Contains(resource.Cookie, "X-APPLE-WEBAUTH-TOKEN=new") {
		t.Fatalf("unexpected stored edit: root=%#v resource=%#v", root, resource)
	}
	validationGeneration := resource.ValidationGeneration
	var maintenanceRunsBefore int64
	if err := db.Model(&iCloudMaintenanceRunModel{}).Count(&maintenanceRunsBefore).Error; err != nil {
		t.Fatalf("count maintenance runs before expiration edit: %v", err)
	}
	expireAt := now.Add(45 * 24 * time.Hour)
	result, err = service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 2, ExpireAt: &expireAt, OperatorUserID: 99,
		IdempotencyKey: "edit-expiration", RequestID: "request-expiration", Path: "/v1/admin/icloud/resources/:resourceId",
	})
	if err != nil {
		t.Fatalf("edit resource expiration: %v", err)
	}
	if err := db.First(&root, 1).Error; err != nil {
		t.Fatalf("reload root after expiration edit: %v", err)
	}
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("reload resource after expiration edit: %v", err)
	}
	var maintenanceRunsAfter int64
	if err := db.Model(&iCloudMaintenanceRunModel{}).Count(&maintenanceRunsAfter).Error; err != nil {
		t.Fatalf("count maintenance runs after expiration edit: %v", err)
	}
	if result.Version != 3 || root.Version != 3 || !resource.ExpireAt.Equal(expireAt) ||
		resource.ValidationGeneration != validationGeneration || maintenanceRunsAfter != maintenanceRunsBefore {
		t.Fatalf("expiration edit queued maintenance: result=%#v root=%#v resource=%#v runs=%d->%d", result, root, resource, maintenanceRunsBefore, maintenanceRunsAfter)
	}
	now = expireAt.Add(time.Hour)
	replayed, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 2, ExpireAt: &expireAt, OperatorUserID: 99,
		IdempotencyKey: "edit-expiration", RequestID: "request-expiration-retry", Path: "/v1/admin/icloud/resources/:resourceId",
	})
	if err != nil || *replayed != *result {
		t.Fatalf("replay expired edit: result=%#v err=%v", replayed, err)
	}

	if err := db.Create(&iCloudImportAllocationTestModel{ID: 1, ResourceID: 1, Status: "allocated"}).Error; err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	nextEmail := "blocked@icloud.com"
	if _, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 3, PrimaryEmail: &nextEmail, OperatorUserID: 99,
		IdempotencyKey: "edit-key-2", RequestID: "request-edit-2", Path: "/v1/admin/icloud/resources/:resourceId",
	}); !errors.Is(err, ErrICloudResourceAllocation) {
		t.Fatalf("active-allocation edit error = %v", err)
	}
}

func TestEditAdminICloudResourceFencesIdentityChanges(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-identity-edit?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAdminTestUser{},
		&iCloudImportAllocationTestModel{}, &governanceinfra.OperationLogModel{},
		&iCloudMaintenanceRunModel{},
		&coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	for _, owner := range []iCloudAdminTestUser{
		{ID: 7, Email: "old-owner@example.com", Status: "active", Role: "supplier"},
		{ID: 8, Email: "new-owner@example.com", Status: "active", Role: "supplier"},
	} {
		if err := db.Create(&owner).Error; err != nil {
			t.Fatalf("create owner: %v", err)
		}
	}
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	credentials := AdminICloudCredentialsInput{
		Host: "p119-maildomainws.icloud.com", DSID: "same-dsid", ClientID: "same-client",
		ClientBuildNumber: "same-build", ClientMasteringNumber: "same-master",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=same; X-APPLE-WEBAUTH-USER=same; X-APPLE-WEBAUTH-TOKEN=same",
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "old@icloud.com", Host: credentials.Host,
		DSID: credentials.DSID, ClientID: credentials.ClientID, ClientBuildNumber: credentials.ClientBuildNumber,
		ClientMasteringNumber: credentials.ClientMasteringNumber, Cookie: credentials.Cookie,
		ForSale: true, Status: iCloudResourceNormal, SessionStatus: iCloudSessionValid,
		CredentialRevision: 3, CredentialUpdatedAt: now.Add(-time.Hour), ValidationGeneration: 4,
		ExpireAt: now.Add(30 * 24 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.SetImportOwnerValidator(func(_ context.Context, ownerID uint) (bool, error) {
		return ownerID == 7 || ownerID == 8, nil
	})

	nextEmail := "new@icloud.com"
	if _, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, PrimaryEmail: &nextEmail, OperatorUserID: 99,
		IdempotencyKey: "identity-email-without-credentials", RequestID: "identity-1", Path: "/edit",
	}); !errors.Is(err, ErrICloudResourceUpdate) {
		t.Fatalf("email-only edit error = %v", err)
	}

	result, err := service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 1, PrimaryEmail: &nextEmail, Credentials: &credentials,
		OperatorUserID: 99, IdempotencyKey: "identity-email-with-credentials", RequestID: "identity-2", Path: "/edit",
	})
	if err != nil {
		t.Fatalf("email and credential edit: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("load edited resource: %v", err)
	}
	if result.Version != 2 || result.Status != iCloudResourcePending || result.ForSale ||
		resource.PrimaryEmail != nextEmail || resource.CredentialRevision != 4 ||
		resource.ValidationGeneration != 5 || resource.SessionStatus != iCloudSessionUnchecked ||
		resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(now) {
		t.Fatalf("email identity was not fenced: result=%#v resource=%#v", result, resource)
	}

	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Updates(map[string]any{"status": iCloudResourceValidating, "for_sale": true}).Error; err != nil {
		t.Fatalf("prepare validating owner edit: %v", err)
	}
	ownerID := uint(8)
	result, err = service.EditAdminICloudResource(context.Background(), AdminICloudEditCommand{
		ResourceID: 1, Version: 2, OwnerUserID: &ownerID, OperatorUserID: 99,
		IdempotencyKey: "identity-owner", RequestID: "identity-3", Path: "/edit",
	})
	if err != nil {
		t.Fatalf("owner edit: %v", err)
	}
	var root iCloudRootModel
	if err := db.First(&root, 1).Error; err != nil {
		t.Fatalf("load edited root: %v", err)
	}
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("reload owner-edited resource: %v", err)
	}
	if result.Version != 3 || result.Status != iCloudResourcePending || result.ForSale ||
		root.OwnerUserID != 8 || resource.ValidationGeneration != 6 || resource.NextValidationAt == nil {
		t.Fatalf("owner identity was not immediately fenced: root=%#v result=%#v resource=%#v", root, result, resource)
	}
}

func TestApplyAdminICloudAliasUsesIndependentAdmissionAndAudit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-alias-command?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAdminTestUser{},
		&iCloudMaintenanceRunModel{},
		&governanceinfra.OperationLogModel{}, &coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	probeVerifiedAt := now.Add(-time.Minute)
	if err := db.Create(&iCloudAdminTestUser{ID: 7, Email: "owner@example.com", Status: "active", Role: "supplier"}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	for id, aliases := range map[uint]uint{1: 12, 2: iCloudMaxAliases} {
		if err := db.Create(&iCloudRootModel{ID: id, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("create root %d: %v", id, err)
		}
		if err := db.Create(&iCloudResourceModel{
			ID: id, ResourceType: "icloud", PrimaryEmail: "alias" + string(rune('0'+id)) + "@icloud.com",
			Status: iCloudResourceNormal, SessionStatus: iCloudSessionValid, AliasCount: aliases,
			DeliveryProbeToken: "probe-token", DeliveryProbeAlias: "alias@icloud.com",
			DeliveryProbeStartedAt: &probeVerifiedAt, DeliveryProbeVerifiedAt: &probeVerifiedAt,
			CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("create resource %d: %v", id, err)
		}
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }

	result, err := service.ApplyAdminICloudBatch(context.Background(), AdminICloudAlias,
		AdminICloudResourceSelection{Mode: "ids", ResourceIDs: []uint{1, 2}},
		nil, 99, "alias-batch", "alias-request", "/v1/admin/icloud/resources/batch/alias")
	if err != nil {
		t.Fatalf("apply alias batch: %v", err)
	}
	if result.Affected != 1 || result.Skipped != 1 || len(result.AffectedResourceIDs) != 1 || result.AffectedResourceIDs[0] != 1 ||
		len(result.ReasonCounts) != 1 || result.ReasonCounts[0].Reason != "already_target" {
		t.Fatalf("unexpected alias admission result: %#v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("load queued alias resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.ValidationGeneration != 2 || resource.NextValidationAt == nil ||
		resource.DeliveryProbeVerifiedAt == nil || resource.DeliveryProbeToken != "probe-token" {
		t.Fatalf("alias command changed resource availability: %#v", resource)
	}
	var run iCloudMaintenanceRunModel
	if err := db.Where("resource_id = ? AND validation_generation = ?", 1, 2).Take(&run).Error; err != nil {
		t.Fatalf("load alias maintenance run: %v", err)
	}
	if run.Kind != iCloudMaintenanceAlias || run.Status != iCloudMaintenanceQueued {
		t.Fatalf("alias command queued the wrong maintenance task: %#v", run)
	}
	claimed, ok, err := service.markICloudValidationDispatched(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 2, ExpectedCredentialRevision: 1,
	})
	if err != nil || !ok || claimed.MaintenanceKind != iCloudMaintenanceAlias {
		t.Fatalf("claim alias maintenance: task=%#v claimed=%v err=%v", claimed, ok, err)
	}
	resource = iCloudResourceModel{}
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("load claimed alias resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.ValidationGeneration != 2 ||
		resource.DeliveryProbeVerifiedAt == nil || resource.DeliveryProbeToken != "probe-token" {
		t.Fatalf("alias maintenance claim changed resource availability: %#v", resource)
	}
	var log governanceinfra.OperationLogModel
	if err := db.Order("id DESC").First(&log).Error; err != nil {
		t.Fatalf("load alias audit log: %v", err)
	}
	if log.OperationType != "icloud.admin_resource.alias_batch" {
		t.Fatalf("alias operation log = %q", log.OperationType)
	}

	full, err := service.ApplyAdminICloudCommand(context.Background(), AdminICloudAlias,
		2, 1, 99, "alias-full", "alias-full-request", "/v1/admin/icloud/resources/2/aliases")
	if err != nil {
		t.Fatalf("apply full alias command: %v", err)
	}
	var fullRoot iCloudRootModel
	var fullResource iCloudResourceModel
	if err := db.First(&fullRoot, 2).Error; err != nil {
		t.Fatalf("load full alias root: %v", err)
	}
	if err := db.First(&fullResource, 2).Error; err != nil {
		t.Fatalf("load full alias resource: %v", err)
	}
	if full.Changed || full.Version != 1 || fullRoot.Version != 1 ||
		fullResource.Status != iCloudResourceNormal || fullResource.ValidationGeneration != 1 || fullResource.NextValidationAt != nil {
		t.Fatalf("full alias resource must remain unchanged: result=%#v root=%#v resource=%#v", full, fullRoot, fullResource)
	}
}

func TestApplyAdminICloudBatchReportsSkippedReasons(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-batch?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAdminTestUser{},
		&iCloudMaintenanceRunModel{},
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
	service.now = func() time.Time { return now }
	result, err := service.ApplyAdminICloudBatch(context.Background(), AdminICloudDisable, AdminICloudResourceSelection{Mode: "ids", ResourceIDs: []uint{2, 1, 1}}, nil, 99, "batch-key-1", "batch-1", "/batch/disable")
	if err != nil {
		t.Fatalf("batch disable: %v", err)
	}
	if result.Requested != 2 || result.Affected != 1 || result.Skipped != 1 || len(result.AffectedResourceIDs) != 1 || len(result.SkippedResourceIDs) != 1 {
		t.Fatalf("unexpected batch result: %#v", result)
	}
	if len(result.ReasonCounts) != 1 || result.ReasonCounts[0].Reason != "not_found" || result.ReasonCounts[0].Count != 1 {
		t.Fatalf("unexpected skip reasons: %#v", result.ReasonCounts)
	}
	expireAt := now.Add(48 * time.Hour)
	result, err = service.ApplyAdminICloudBatch(context.Background(), AdminICloudExpire, AdminICloudResourceSelection{Mode: "ids", ResourceIDs: []uint{1, 2}}, &expireAt, 99, "batch-expire-1", "batch-expire-1", "/batch/expiration")
	if err != nil || result.Affected != 1 || result.Skipped != 1 {
		t.Fatalf("batch expiration result=%#v err=%v", result, err)
	}
	var resource iCloudResourceModel
	var root iCloudRootModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("load expiration-updated resource: %v", err)
	}
	if err := db.First(&root, 1).Error; err != nil {
		t.Fatalf("load expiration-updated root: %v", err)
	}
	var maintenanceRuns int64
	if err := db.Model(&iCloudMaintenanceRunModel{}).Count(&maintenanceRuns).Error; err != nil {
		t.Fatalf("count maintenance runs: %v", err)
	}
	if !resource.ExpireAt.Equal(expireAt) || resource.Status != iCloudResourceDisabled || root.Version != 3 || maintenanceRuns != 0 {
		t.Fatalf("expiration update did not advance resource version: root=%#v resource=%#v", root, resource)
	}
	now = expireAt.Add(time.Hour)
	replayed, err := service.ApplyAdminICloudBatch(context.Background(), AdminICloudExpire, AdminICloudResourceSelection{Mode: "ids", ResourceIDs: []uint{1, 2}}, &expireAt, 99, "batch-expire-1", "batch-expire-retry", "/batch/expiration")
	if err != nil || replayed.Affected != result.Affected || replayed.Skipped != result.Skipped {
		t.Fatalf("replay expired batch: result=%#v err=%v", replayed, err)
	}
	past := now
	if _, err := service.ApplyAdminICloudBatch(context.Background(), AdminICloudExpire, AdminICloudResourceSelection{Mode: "ids", ResourceIDs: []uint{1}}, &past, 99, "batch-expire-past", "batch-expire-past", "/batch/expiration"); !errors.Is(err, ErrICloudResourceUpdate) {
		t.Fatalf("past expiration error = %v", err)
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
