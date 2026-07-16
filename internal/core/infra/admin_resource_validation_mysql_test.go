package infra

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResourceValidationRedisSchemaMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.False(t, db.Migrator().HasTable("resource_validation_jobs"))
	require.False(t, db.Migrator().HasTable("resource_validation_batches"))
	requireIndexExists(t, db, "microsoft_resources", "idx_microsoft_status")
	requireIndexExists(t, db, "domain_resources", "idx_domain_resources_status")
}

func TestResourceValidationRepoClaimsAndReleasesRedisAssignmentsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	insertAdminValidationOwner(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{EmailAddress: "claim@outlook.com", Password: "secret", Status: domain.MicrosoftStatusPending}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, resource))
	makeValidationAssignmentsReady(t, db)

	tasks, err := validations.ClaimPendingValidations(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, root.ID, tasks[0].ResourceID)
	require.Equal(t, domain.ResourceTypeMicrosoft, tasks[0].ResourceType)

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusValidating), stored.Status)

	require.NoError(t, validations.ReleaseValidation(context.Background(), tasks[0]))
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)
	immediate, err := validations.ClaimPendingValidations(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, immediate, "the current Asynq task must finish before the same Redis task ID can be reassigned")
	makeValidationAssignmentsReady(t, db)
	reassigned, err := validations.ClaimPendingValidations(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, reassigned, 1)
}

func TestResourceValidationRepoCapsRedisAssignmentsAtExecutionWindowMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	insertAdminValidationOwner(t, db)

	roots := make([]*domain.EmailResource, 3)
	for i := range roots {
		roots[i] = &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
		require.NoError(t, repo.CreateMicrosoft(context.Background(), roots[i], &domain.MicrosoftResource{
			EmailAddress: fmt.Sprintf("assignment-cap-%d@outlook.com", i), Password: "secret", Status: domain.MicrosoftStatusPending,
		}))
	}
	require.NoError(t, db.Model(&MicrosoftResourceModel{}).Where("id = ?", roots[0].ID).
		Update("status", string(domain.MicrosoftStatusValidating)).Error)
	makeValidationAssignmentsReady(t, db)

	tasks, err := validations.ClaimPendingValidations(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, tasks, 1, "one existing assignment plus one new task must fill the window")
	tasks, err = validations.ClaimPendingValidations(context.Background(), 2)
	require.NoError(t, err)
	require.Empty(t, tasks, "repeated dispatch must not grow Redis backlog beyond the execution window")
}

func TestResourceValidationRepoPagesRedisBatchAndFreezesHighWaterMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	insertAdminValidationOwner(t, db)

	roots := make([]*domain.EmailResource, 3)
	for i := range roots {
		roots[i] = &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
		require.NoError(t, repo.CreateMicrosoft(context.Background(), roots[i], &domain.MicrosoftResource{
			EmailAddress: fmt.Sprintf("batch-page-%d@outlook.com", i), Password: "secret", Status: domain.MicrosoftStatusAbnormal,
		}))
	}
	validatingRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), validatingRoot, &domain.MicrosoftResource{
		EmailAddress: "batch-page-validating@outlook.com", Password: "secret", Status: domain.MicrosoftStatusValidating,
	}))

	task := coreapp.ResourceValidationBatchTask{
		BatchID: "redis-batch-page", OwnerUserID: 1,
		Selection: coreapp.ResourceBulkSelection{
			Mode: coreapp.ResourceBulkSelectionFilter,
			Filter: coreapp.ResourceBulkFilter{
				ResourceType: domain.ResourceTypeMicrosoft,
				Status:       string(domain.MicrosoftStatusAbnormal),
			},
		},
	}
	page, err := validations.MarkValidationBatchPending(context.Background(), task, 1)
	require.NoError(t, err)
	require.Equal(t, 1, page.Processed)
	require.False(t, page.Done)
	require.NotZero(t, page.ThroughID)

	lateRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), lateRoot, &domain.MicrosoftResource{
		EmailAddress: "batch-page-late@outlook.com", Password: "secret", Status: domain.MicrosoftStatusAbnormal,
	}))

	for !page.Done {
		task.AfterID = page.AfterID
		task.ThroughID = page.ThroughID
		page, err = validations.MarkValidationBatchPending(context.Background(), task, 1)
		require.NoError(t, err)
	}
	for _, root := range roots {
		var stored MicrosoftResourceModel
		require.NoError(t, db.First(&stored, root.ID).Error)
		require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)
	}
	var validatingStored, lateStored MicrosoftResourceModel
	require.NoError(t, db.First(&validatingStored, validatingRoot.ID).Error)
	require.NoError(t, db.First(&lateStored, lateRoot.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusValidating), validatingStored.Status)
	require.Equal(t, string(domain.MicrosoftStatusAbnormal), lateStored.Status)
}

func TestResourceValidationRepoAdminIDsRespectEndpointResourceTypeMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	insertAdminValidationOwner(t, db)

	microsoftRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), microsoftRoot, &domain.MicrosoftResource{
		EmailAddress: "typed-batch@outlook.com", Password: "secret", Status: domain.MicrosoftStatusAbnormal,
	}))
	domainRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, repo.CreateDomain(context.Background(), domainRoot, &domain.MailDomainResource{
		Domain: "typed-batch.example.com", MailServerID: 1, Purpose: domain.PurposeNotSale, Status: domain.DomainStatusAbnormal,
	}))
	ids := []uint{microsoftRoot.ID, domainRoot.ID}

	_, err := validations.MarkValidationBatchPending(context.Background(), coreapp.ResourceValidationBatchTask{
		BatchID: "admin-microsoft-ids", OwnerUserID: 1,
		Selection: coreapp.ResourceBulkSelection{
			Mode: coreapp.ResourceBulkSelectionIDs, ResourceIDs: ids, AdminScope: true,
			Filter: coreapp.ResourceBulkFilter{ResourceType: domain.ResourceTypeMicrosoft},
		},
	}, 1000)
	require.NoError(t, err)
	var microsoft MicrosoftResourceModel
	var domainResource DomainResourceModel
	require.NoError(t, db.First(&microsoft, microsoftRoot.ID).Error)
	require.NoError(t, db.First(&domainResource, domainRoot.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusPending), microsoft.Status)
	require.Equal(t, string(domain.DomainStatusAbnormal), domainResource.Status)

	require.NoError(t, db.Model(&MicrosoftResourceModel{}).Where("id = ?", microsoftRoot.ID).Update("status", string(domain.MicrosoftStatusAbnormal)).Error)
	_, err = validations.MarkValidationBatchPending(context.Background(), coreapp.ResourceValidationBatchTask{
		BatchID: "admin-domain-ids", OwnerUserID: 1,
		Selection: coreapp.ResourceBulkSelection{
			Mode: coreapp.ResourceBulkSelectionIDs, ResourceIDs: ids, AdminScope: true,
			Filter: coreapp.ResourceBulkFilter{ResourceType: domain.ResourceTypeDomain},
		},
	}, 1000)
	require.NoError(t, err)
	require.NoError(t, db.First(&microsoft, microsoftRoot.ID).Error)
	require.NoError(t, db.First(&domainResource, domainRoot.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusAbnormal), microsoft.Status)
	require.Equal(t, string(domain.DomainStatusPending), domainResource.Status)
}

func TestResourceValidationRepoDomainResultSurvivesOwnerTransferMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	insertAdminValidationOwner(t, db)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role, enabled)
VALUES (2, 'validation-new-owner@example.com', 'hash', 'supplier', 1)
ON DUPLICATE KEY UPDATE email = VALUES(email)`).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, repo.CreateDomain(context.Background(), root, &domain.MailDomainResource{
		Domain: "owner-transfer.example.com", MailServerID: 1, Purpose: domain.PurposeNotSale, Status: domain.DomainStatusPending,
	}))
	makeValidationAssignmentsReady(t, db)
	tasks, err := validations.ClaimPendingValidations(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, uint(1), tasks[0].OwnerUserID)
	require.NoError(t, db.Model(&EmailResourceModel{}).Where("id = ?", root.ID).Update("owner_user_id", 2).Error)

	require.NoError(t, validations.ApplyDomainResult(context.Background(), tasks[0], coreapp.DomainValidationResult{Valid: true}, nil))
	var stored DomainResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.DomainStatusNormal), stored.Status)
}

func TestResourceValidationRepoResetAssignmentsReturnsBothTypesPendingMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	insertAdminValidationOwner(t, db)

	microsoftRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), microsoftRoot, &domain.MicrosoftResource{
		EmailAddress: "reset@outlook.com", Password: "secret", Status: domain.MicrosoftStatusPending,
	}))
	domainRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, repo.CreateDomain(context.Background(), domainRoot, &domain.MailDomainResource{
		Domain: "reset.example.com", MailServerID: 1, Purpose: domain.PurposeNotSale, Status: domain.DomainStatusPending,
	}))
	makeValidationAssignmentsReady(t, db)
	_, err := validations.ClaimPendingValidations(context.Background(), 2)
	require.NoError(t, err)
	require.NoError(t, validations.ResetValidationAssignments(context.Background()))

	var microsoft MicrosoftResourceModel
	var domainResource DomainResourceModel
	require.NoError(t, db.First(&microsoft, microsoftRoot.ID).Error)
	require.NoError(t, db.First(&domainResource, domainRoot.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusPending), microsoft.Status)
	require.Equal(t, string(domain.DomainStatusPending), domainResource.Status)
}

func TestResourceValidationRepoAppliesRedisMicrosoftResultWithRevisionFenceMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	insertAdminValidationOwner(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "result@outlook.com", Password: "secret", ClientID: "old-client", RefreshToken: "old-rt", Status: domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, resource))
	makeValidationAssignmentsReady(t, db)
	tasks, err := validations.ClaimPendingValidations(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	require.NoError(t, validations.ApplyMicrosoftResult(context.Background(), tasks[0], coreapp.MicrosoftValidationResult{
		Valid: true, ClientID: "new-client", RefreshToken: "new-rt", GraphAvailable: true,
	}, nil))

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusNormal), stored.Status)
	require.Equal(t, "new-client", stored.ClientID)
	require.Equal(t, "new-rt", stored.RefreshToken)
	require.EqualValues(t, tasks[0].ExpectedCredentialRevision+1, stored.CredentialRevision)
	require.True(t, stored.GraphAvailable)

	stale := tasks[0]
	stale.ExpectedCredentialRevision--
	err = validations.ApplyMicrosoftResult(context.Background(), stale, coreapp.MicrosoftValidationResult{Valid: false}, nil)
	require.ErrorIs(t, err, coreapp.ErrValidationResultStale)
}

func TestResourceValidationRepoBindingFailureDoesNotUndoHealthyResultMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	validations.SetMicrosoftValidationBindingCommitPort(validationBindingCommitSpy{err: errors.New("binding unavailable")})
	insertAdminValidationOwner(t, db)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, &domain.MicrosoftResource{
		EmailAddress: "binding@outlook.com", Password: "secret", Status: domain.MicrosoftStatusPending,
	}))
	makeValidationAssignmentsReady(t, db)
	tasks, err := validations.ClaimPendingValidations(context.Background(), 1)
	require.NoError(t, err)
	require.NoError(t, validations.ApplyMicrosoftResult(context.Background(), tasks[0], coreapp.MicrosoftValidationResult{
		Valid:              true,
		BindingObservation: &coreapp.MicrosoftBindingObservation{Address: "a*****b@example.com"},
	}, nil))

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusNormal), stored.Status)
}

func insertAdminValidationOwner(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role, enabled)
VALUES (1, 'validation-owner@example.com', 'hash', 'admin', 1)
ON DUPLICATE KEY UPDATE email = VALUES(email)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mail_servers(id, owner_user_id, server_address, status)
VALUES (1, 1, '127.0.0.1', 'online')
ON DUPLICATE KEY UPDATE owner_user_id = VALUES(owner_user_id)`).Error)
}

func makeValidationAssignmentsReady(t *testing.T, db *gorm.DB) {
	t.Helper()
	readyAt := time.Now().UTC().Add(-2 * validationAssignmentSettleAfter)
	require.NoError(t, db.Model(&MicrosoftResourceModel{}).
		Where("status = ?", string(domain.MicrosoftStatusPending)).
		Update("updated_at", readyAt).Error)
	require.NoError(t, db.Model(&DomainResourceModel{}).
		Where("status = ?", string(domain.DomainStatusPending)).
		Update("updated_at", readyAt).Error)
}

type validationBindingCommitSpy struct {
	err error
}

func (s validationBindingCommitSpy) CommitValidationBinding(ctx context.Context, _ coreapp.MicrosoftValidationBindingCommand) (bool, error) {
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		return false, errors.New("validation binding commit did not receive transaction")
	}
	return false, s.err
}
