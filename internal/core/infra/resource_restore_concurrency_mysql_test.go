package infra

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDomainRestoreSerializesWithAdminRecoverMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validation := coreapp.NewResourceValidationUseCase(repo, NewResourceValidationRepo(db), adminCommandValidationQueue{}, nil)
	admin := coreapp.NewAdminDomainCommandService(adminRepo, nil, validation, governanceinfra.NewOperationLogRepo(db))
	admin.SetPorts(adminCommandOwners(), &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)
	require.NoError(t, db.Exec(`
INSERT INTO mail_servers(id, owner_user_id, server_address, mx_record, status)
VALUES
    (200, 1, 'mx.restore-one.test', 'mx.restore-one.test', 'online'),
    (201, 2, 'mx.restore-two.test', 'mx.restore-two.test', 'online')`).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	resource := &domain.MailDomainResource{
		Domain: "lock-order.example.com", MailServerID: 200,
		Purpose: domain.PurposeNotSale, Status: domain.DomainStatusAbnormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), root, resource))
	require.NoError(t, repo.DeletePrivateDomainWithLog(context.Background(), 1, root.ID, governancedomain.OperationLog{
		OperatorUserID: 1, OperationType: "core.domain_resource.delete_private",
		ResourceType: "domain_resource", ResourceID: fmt.Sprintf("%d", root.ID),
		Path: "/v1/resources/:resourceId", Result: "success", RequestID: "delete-domain-before-restore-race",
	}))

	before := resourceRestoreLockMetrics(t, db)
	waitsBefore := resourceRestoreLockWaits(t, db)
	blocker := lockResourceRestoreRoot(t, db, root.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	adminDone := make(chan error, 1)
	go func() {
		_, err := admin.ApplyAction(ctx, coreapp.AdminDomainEditCommand{
			ResourceID: root.ID, Version: root.Version, Action: coreapp.AdminDomainRecover,
			OperatorUserID: 9, IdempotencyKey: "admin-domain-restore-race",
			RequestID: "admin-domain-restore-race", Path: "/v1/admin/domains/:resourceId/recover",
		})
		adminDone <- err
	}()
	waitForResourceRestoreLockWaits(t, db, waitsBefore+1)

	coreRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 2}
	coreResource := &domain.MailDomainResource{
		Domain: resource.Domain, MailServerID: 201,
		Purpose: domain.PurposeNotSale, Status: domain.DomainStatusAbnormal,
	}
	coreDone := make(chan error, 1)
	go func() { coreDone <- repo.CreateDomain(ctx, coreRoot, coreResource) }()
	waitForResourceRestoreLockWaits(t, db, waitsBefore+2)
	require.NoError(t, blocker.Commit().Error)

	require.NoError(t, waitForResourceRestoreResult(t, adminDone))
	require.ErrorIs(t, waitForResourceRestoreResult(t, coreDone), domain.ErrDuplicateDomain)
	waitForResourceRestoreLockWaitsEqual(t, db, waitsBefore)
	after := resourceRestoreLockMetrics(t, db)
	require.Equal(t, before, after)

	var storedRoot EmailResourceModel
	var stored DomainResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, uint(1), storedRoot.OwnerUserID)
	require.EqualValues(t, root.Version+1, storedRoot.Version)
	require.Equal(t, uint(1), stored.OwnerUserID)
	require.Equal(t, uint(200), stored.MailServerID)
	require.Equal(t, string(domain.PurposeNotSale), stored.Purpose)
	require.Equal(t, string(domain.DomainStatusPending), stored.Status)
}

func TestMicrosoftRestoreSerializesWithAdminRecoverMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	adminRepo := NewAdminResourceRepo(db)
	validation := coreapp.NewResourceValidationUseCase(repo, NewResourceValidationRepo(db), adminCommandValidationQueue{}, nil)
	admin := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	admin.SetPorts(adminCommandOwners(), adminCommandBindingQueryPort{}, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	insertAdminCommandUsers(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "lock-order@outlook.com", Password: "old-password",
		Status: domain.MicrosoftStatusPending, ForSale: false,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, resource))
	require.NoError(t, repo.DeletePrivateMicrosoftWithLog(context.Background(), 1, root.ID, governancedomain.OperationLog{
		OperatorUserID: 1, OperationType: "core.microsoft_resource.delete_private",
		ResourceType: "microsoft_resource", ResourceID: fmt.Sprintf("%d", root.ID),
		Path: "/v1/resources/:resourceId", Result: "success", RequestID: "delete-microsoft-before-restore-race",
	}))
	importRepo := NewResourceImportRepo(db)
	importRecord := &domain.ResourceImport{
		OwnerUserID: 2, ResourceType: domain.ResourceTypeMicrosoft,
		SourceObjectKey: "imports/microsoft/source/restore-lock-order.txt",
		Status:          domain.ResourceImportProcessing,
	}
	require.NoError(t, importRepo.Create(context.Background(), importRecord))

	before := resourceRestoreLockMetrics(t, db)
	waitsBefore := resourceRestoreLockWaits(t, db)
	blocker := lockResourceRestoreRoot(t, db, root.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	adminDone := make(chan error, 1)
	go func() {
		_, err := admin.Recover(
			ctx, root.ID, root.Version, 9, "admin-microsoft-restore-race",
			"admin-microsoft-restore-race", "/v1/admin/resources/:resourceId/recover",
		)
		adminDone <- err
	}()
	waitForResourceRestoreLockWaits(t, db, waitsBefore+1)

	coreResources := []domain.EmailResource{{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 2}}
	coreMicrosoft := []domain.MicrosoftResource{{
		EmailAddress: resource.EmailAddress, Password: "new-password",
		Status: domain.MicrosoftStatusPending, ForSale: true,
	}}
	coreDone := make(chan error, 1)
	go func() {
		_, err := importRepo.CreateMicrosoftResourcesAndMarkSucceeded(
			ctx, importRecord.ID, "", microsoftImportLinesForRepoTest(coreMicrosoft),
			coreResources, coreMicrosoft, nil, "", "", nil,
		)
		coreDone <- err
	}()
	waitForResourceRestoreLockWaits(t, db, waitsBefore+2)
	require.NoError(t, blocker.Commit().Error)

	require.NoError(t, waitForResourceRestoreResult(t, adminDone))
	require.ErrorIs(t, waitForResourceRestoreResult(t, coreDone), domain.ErrDuplicateEmail)
	waitForResourceRestoreLockWaitsEqual(t, db, waitsBefore)
	after := resourceRestoreLockMetrics(t, db)
	require.Equal(t, before, after)

	var storedRoot EmailResourceModel
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&storedRoot, root.ID).Error)
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, uint(1), storedRoot.OwnerUserID)
	require.EqualValues(t, root.Version+1, storedRoot.Version)
	require.Equal(t, "old-password", stored.Password)
	require.False(t, stored.ForSale)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)
}

type resourceRestoreInnoDBMetrics struct {
	Deadlocks uint64
	Timeouts  uint64
}

func resourceRestoreLockMetrics(t *testing.T, db *gorm.DB) resourceRestoreInnoDBMetrics {
	t.Helper()
	var rows []struct {
		Name  string
		Count uint64
	}
	require.NoError(t, db.Raw(`
SELECT NAME, COUNT
FROM information_schema.INNODB_METRICS
WHERE NAME IN ('lock_deadlocks', 'lock_timeouts')`).Scan(&rows).Error)
	metrics := resourceRestoreInnoDBMetrics{}
	for _, row := range rows {
		switch row.Name {
		case "lock_deadlocks":
			metrics.Deadlocks = row.Count
		case "lock_timeouts":
			metrics.Timeouts = row.Count
		}
	}
	require.Len(t, rows, 2)
	return metrics
}

func resourceRestoreLockWaits(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	result := db.Raw(`
SELECT COUNT
FROM information_schema.INNODB_METRICS
WHERE NAME = 'lock_row_lock_current_waits'`).Scan(&count)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)
	return count
}

func lockResourceRestoreRoot(t *testing.T, db *gorm.DB, resourceID uint) *gorm.DB {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	var lockedID uint
	require.NoError(t, tx.Raw("SELECT id FROM email_resources WHERE id = ? FOR UPDATE", resourceID).Scan(&lockedID).Error)
	require.Equal(t, resourceID, lockedID)
	return tx
}

func waitForResourceRestoreLockWaits(t *testing.T, db *gorm.DB, minimum int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		return resourceRestoreLockWaits(t, db) >= minimum
	}, 5*time.Second, 10*time.Millisecond)
}

func waitForResourceRestoreLockWaitsEqual(t *testing.T, db *gorm.DB, expected int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		return resourceRestoreLockWaits(t, db) == expected
	}, 5*time.Second, 10*time.Millisecond)
}

func waitForResourceRestoreResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		return errors.New("resource restore race did not finish")
	}
}
