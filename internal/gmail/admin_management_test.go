package gmail

import (
	"context"
	"strings"
	"testing"
	"time"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdminGmailManagementCoversBatchEditDeleteRecoverAndMaintenance(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-admin-management?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&resourceRootModel{}, &localResourceModel{}, &gmailMaintenanceRunModel{}, &allocationModel{},
		&gmailAdminTestUser{}, &gmailAdminTestGroup{},
		&governanceinfra.OperationLogModel{}, &coreinfra.AdminResourceCommandReceiptModel{},
	))
	require.NoError(t, db.Create(&gmailAdminTestGroup{ID: 1, Name: "Suppliers"}).Error)
	for _, owner := range []gmailAdminTestUser{
		{ID: 7, Email: "old@example.com", Nickname: "Old", Role: "supplier", Status: "active", UserGroupID: 1},
		{ID: 8, Email: "new@example.com", Nickname: "New", Role: "supplier", Status: "active", UserGroupID: 1},
	} {
		require.NoError(t, db.Create(&owner).Error)
	}
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	createResource := func(id uint, email, status string) {
		t.Helper()
		require.NoError(t, db.Create(&resourceRootModel{
			ID: id, Type: "gmail", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now,
		}).Error)
		require.NoError(t, db.Create(&localResourceModel{
			ID: id, ResourceType: "gmail", OwnerUserID: 7, Email: email, Identity: email,
			Password: "old-password", TwoFactorSecret: "JBSWY3DPEHPK3PXP",
			AppPassword: "abcdefghijklmnop", CredentialRevision: 1,
			CredentialUpdatedAt: now, ProviderCursor: 101, ProviderSpamCursor: 202,
			ValidationGeneration: 1, Status: status,
			CreatedAt: now, UpdatedAt: now,
		}).Error)
	}
	createResource(1, "delete@gmail.com", LocalResourceNormal)
	createResource(2, "busy@gmail.com", LocalResourceNormal)
	createResource(3, "recover@gmail.com", LocalResourceDeleted)
	createResource(4, "edit@gmail.com", LocalResourceNormal)
	createResource(5, "allocated@gmail.com", LocalResourceNormal)
	deleteRun := gmailMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 1, Kind: gmailMaintenanceHistory,
		Status: gmailMaintenanceRunning, Attempts: 1, MaxAttempts: localGmailValidationMaxFailures,
		CredentialRevision: 1, QueuedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&deleteRun).Error)
	busyResourceID := uint(2)
	require.NoError(t, db.Create(&allocationModel{
		ID: 1, ResourceID: &busyResourceID, Status: AllocationStatusAllocated,
		OrderNo: "order-busy", Source: SourceLocal, CreatedAt: now,
	}).Error)
	allocatedResourceID := uint(5)
	require.NoError(t, db.Create(&allocationModel{
		ID: 2, ResourceID: &allocatedResourceID, Status: AllocationStatusAllocated,
		OrderNo: "order-credential-repair", Source: SourceLocal, Mailbox: GmailMailboxPlus,
		Email: "allocated+special@gmail.com", CreatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&allocationModel{
		ID: 3, ResourceID: &allocatedResourceID, Status: AllocationStatusReleased,
		OrderNo: "order-dot-history", Source: SourceLocal, Mailbox: GmailMailboxDot,
		Email: "allo.cated@gmail.com", CreatedAt: now,
	}).Error)

	service := NewService(db, nil)
	service.now = func() time.Time { return now.Add(time.Hour) }
	ctx := context.Background()
	for _, alias := range []string{"allocated+special@gmail.com", "allo.cated@gmail.com"} {
		result, err := service.ListLocalResources(ctx, LocalResourceListFilter{Search: alias})
		require.NoError(t, err)
		require.EqualValues(t, 1, result.Total, alias)
		require.Equal(t, "allocated@gmail.com", result.Items[0].Email, alias)
	}
	_, err = service.UpdateAdminLocalResource(ctx, AdminLocalResourceEditCommand{
		ResourceID: 4, Version: 1, OperatorID: 99, OwnerUserID: 7, Email: "edit@gmail.com",
		Credentials:           &AdminLocalResourceCredentialsInput{TwoFactorSecret: strings.Repeat("A", 520)},
		CredentialReplacement: true, IdempotencyKey: "oversized-2fa",
	})
	require.ErrorIs(t, err, ErrInvalidLocalResource)

	deleted, err := service.ApplyAdminLocalResourceBatch(ctx, AdminLocalResourceDelete,
		AdminLocalResourceSelection{Mode: "ids", ResourceIDs: []uint{2, 1, 1}},
		99, "delete-batch", "request-delete", "/batch/delete")
	require.NoError(t, err)
	require.Equal(t, 2, deleted.Requested)
	require.Equal(t, 1, deleted.Affected)
	require.Equal(t, 1, deleted.Skipped)
	require.Equal(t, "active_allocation", deleted.ReasonCounts[0].Reason)
	require.NoError(t, db.First(&deleteRun, deleteRun.ID).Error)
	require.Equal(t, gmailMaintenanceCanceled, deleteRun.Status)
	require.NotNil(t, deleteRun.FinishedAt)

	recovered, err := service.ApplyAdminLocalResourceCommand(ctx, AdminLocalResourceRecover,
		3, 1, 99, "recover-one", "request-recover", "/recover")
	require.NoError(t, err)
	require.Equal(t, LocalResourcePending, recovered.Status)
	require.EqualValues(t, 2, recovered.Version)

	updated, err := service.UpdateAdminLocalResource(ctx, AdminLocalResourceEditCommand{
		ResourceID: 4, Version: 1, OperatorID: 99, OwnerUserID: 8,
		Email: "corrected@gmail.com", BindingEmail: "recovery@example.com",
		Credentials: &AdminLocalResourceCredentialsInput{
			Password: "new-secret-password", TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",
			AppPassword: "ponmlkjihgfedcba",
		},
		CredentialReplacement: true,
		IdempotencyKey:        "edit-one", RequestID: "request-edit", Path: "/resources/:resourceId",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Version)
	require.Equal(t, LocalResourcePending, updated.Status)

	var root resourceRootModel
	var resource localResourceModel
	require.NoError(t, db.First(&root, 4).Error)
	require.NoError(t, db.First(&resource, 4).Error)
	require.EqualValues(t, 8, root.OwnerUserID)
	require.Equal(t, "corrected@gmail.com", resource.Email)
	require.Equal(t, "recovery@example.com", resource.BindingEmail)
	require.EqualValues(t, 2, resource.CredentialRevision)
	require.EqualValues(t, 2, resource.ValidationGeneration)
	require.Zero(t, resource.ProviderCursor)
	require.Zero(t, resource.ProviderSpamCursor)

	credentials, err := service.UpdateAdminLocalResource(ctx, AdminLocalResourceEditCommand{
		ResourceID: 5, Version: 1, OperatorID: 99, OwnerUserID: 7,
		Email:                 "allocated@gmail.com",
		Credentials:           &AdminLocalResourceCredentialsInput{Password: "old-password"},
		CredentialReplacement: true,
		IdempotencyKey:        "credentials-allocated", RequestID: "request-credentials", Path: "/resources/:resourceId/credentials",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, credentials.Version)
	require.Equal(t, LocalResourcePending, credentials.Status)
	resource = localResourceModel{}
	require.NoError(t, db.First(&resource, 5).Error)
	require.EqualValues(t, 2, resource.CredentialRevision)
	require.EqualValues(t, 2, resource.ValidationGeneration)
	require.Equal(t, "JBSWY3DPEHPK3PXP", resource.TwoFactorSecret)
	require.Equal(t, "abcdefghijklmnop", resource.AppPassword)

	credentials, err = service.UpdateAdminLocalResource(ctx, AdminLocalResourceEditCommand{
		ResourceID: 5, Version: 2, OperatorID: 99, OwnerUserID: 7,
		Email:                 "allocated@gmail.com",
		Credentials:           &AdminLocalResourceCredentialsInput{AppPassword: "ponm lkji hgfe dcba"},
		CredentialReplacement: true,
		IdempotencyKey:        "app-password-only", RequestID: "request-app-password", Path: "/resources/:resourceId",
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, credentials.Version)
	resource = localResourceModel{}
	require.NoError(t, db.First(&resource, 5).Error)
	require.Equal(t, "old-password", resource.Password)
	require.Equal(t, "ponmlkjihgfedcba", resource.AppPassword)
	require.EqualValues(t, 3, resource.CredentialRevision)

	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", 4).
		Update("status", LocalResourceNormal).Error)
	history, err := service.ApplyAdminLocalResourceBatch(ctx, AdminLocalResourceHistory,
		AdminLocalResourceSelection{Mode: "ids", ResourceIDs: []uint{4}},
		99, "history-batch", "request-history", "/batch/history")
	require.NoError(t, err)
	require.Equal(t, 1, history.Affected)
	resource = localResourceModel{}
	require.NoError(t, db.First(&resource, 4).Error)
	require.Equal(t, LocalResourceIdentifying, resource.Status)
	var historyRun gmailMaintenanceRunModel
	require.NoError(t, db.Where("resource_id = ? AND kind = ?", 4, gmailMaintenanceHistory).
		Order("id DESC").Take(&historyRun).Error)
	require.Equal(t, gmailMaintenanceQueued, historyRun.Status)
	require.NoError(t, db.First(&root, 4).Error)
	disabled, err := service.ApplyAdminLocalResourceCommand(ctx, AdminLocalResourceDisable,
		4, root.Version, 99, "disable-history", "request-disable-history", "/disable")
	require.NoError(t, err)
	require.Equal(t, LocalResourceDisabled, disabled.Status)
	require.NoError(t, db.First(&historyRun, historyRun.ID).Error)
	require.Equal(t, gmailMaintenanceCanceled, historyRun.Status)
	require.NotNil(t, historyRun.FinishedAt)

	var logs []governanceinfra.OperationLogModel
	require.NoError(t, db.Find(&logs).Error)
	for _, log := range logs {
		safe := strings.Join([]string{log.OperationType, log.ResourceID, log.SafeSummary, log.RequestID}, " ")
		for _, secret := range []string{"new-secret-password", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "ponmlkjihgfedcba"} {
			require.NotContains(t, safe, secret)
		}
	}
}

func TestListAdminGmailAliasesReturnsDistinctObservedVariants(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-admin-alias-list?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &allocationModel{}))
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&resourceRootModel{ID: 1, Type: "gmail", OwnerUserID: 7, Version: 1}).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: 1, ResourceType: "gmail", OwnerUserID: 7, Email: "alias-list@gmail.com", Identity: "aliaslist@gmail.com",
		Status: LocalResourceNormal,
	}).Error)
	resourceID := uint(1)
	for _, allocation := range []allocationModel{
		{OrderNo: "ALIAS-DOT-OLD", ResourceID: &resourceID, Mailbox: GmailMailboxDot, Email: "a.lias-list@gmail.com", Status: AllocationStatusReleased, CreatedAt: now.Add(-2 * time.Hour)},
		{OrderNo: "ALIAS-DOT-DUP", ResourceID: &resourceID, Mailbox: GmailMailboxDot, Email: "A.LIAS-LIST@GMAIL.COM", Status: AllocationStatusAllocated, CreatedAt: now.Add(-time.Hour)},
		{OrderNo: "ALIAS-PLUS", ResourceID: &resourceID, Mailbox: GmailMailboxPlus, Email: "alias-list+tag@gmail.com", Status: AllocationStatusReleased, CreatedAt: now.Add(-30 * time.Minute)},
		{OrderNo: "ALIAS-MAIN", ResourceID: &resourceID, Mailbox: GmailMailboxMain, Email: "alias-list@gmail.com", Status: AllocationStatusAllocated, CreatedAt: now},
	} {
		require.NoError(t, db.Create(&allocation).Error)
	}

	result, err := NewService(db, nil).ListAdminGmailAliases(context.Background(), resourceID, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, result.Total)
	require.Equal(t, []string{"alias-list+tag@gmail.com", "a.lias-list@gmail.com"}, []string{
		result.Items[0].EmailAddress, result.Items[1].EmailAddress,
	})
	require.Equal(t, []string{"plus", "dot"}, []string{result.Items[0].Kind, result.Items[1].Kind})
}
