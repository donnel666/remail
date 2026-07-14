package api

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var validationBindingMySQL = testmysql.New("remail_validation_binding_api_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = validationBindingMySQL.Close(context.Background())
	os.Exit(code)
}

func TestValidationBindingCommitPortSharesCoreResultTransactionMySQL(t *testing.T) {
	db := validationBindingMySQL.Database(t, validationBindingMigrationsDir(t))
	createValidationBindingFixture(t, db, 101, "success@outlook.com")
	createValidationBindingFixture(t, db, 102, "rollback@outlook.com")
	createValidationBindingDomainFixture(t, db, 201, 202, "binding-commit.test")

	validationRepo := coreinfra.NewResourceValidationRepo(db)
	validationRepo.SetMicrosoftValidationBindingCommitPort(
		NewMicrosoftValidationBindingCommitAdapter(mailinfra.NewMicrosoftBindingRepo(db)),
	)
	successJob, successClaim := createRunningValidationBindingJob(t, db, validationRepo, 101)

	require.NoError(t, validationRepo.ApplyMicrosoftResult(context.Background(), successJob.ID, 101, successClaim, coreapp.MicrosoftValidationResult{
		Valid: true,
		RecoveredBinding: &coreapp.MicrosoftRecoveredBinding{
			Address: "recovered@binding-commit.test",
		},
	}, nil))

	var successBinding mailinfra.MicrosoftBindingMailboxModel
	require.NoError(t, db.Where("resource_id = ?", 101).First(&successBinding).Error)
	require.Equal(t, "recovered@binding-commit.test", successBinding.BindingAddress)
	require.Equal(t, string(maildomain.MicrosoftBindingVerified), successBinding.Status)
	require.EqualValues(t, 2, validationBindingRootVersion(t, db, 101))
	var successStored coreinfra.ResourceValidationModel
	require.NoError(t, db.First(&successStored, successJob.ID).Error)
	require.Equal(t, string(domain.ResourceValidationSucceeded), successStored.Status)

	rollbackJob, rollbackClaim := createRunningValidationBindingJob(t, db, validationRepo, 102)
	err := validationRepo.ApplyMicrosoftResult(context.Background(), rollbackJob.ID, 102, rollbackClaim, coreapp.MicrosoftValidationResult{
		Valid: true,
		RecoveredBinding: &coreapp.MicrosoftRecoveredBinding{
			Address: "must-rollback@binding-commit.test",
		},
	}, &governancedomain.SystemLog{
		Level: "warning", Module: strings.Repeat("x", 101), EventType: "forced.validation.failure",
		Message: "Force the final Core transaction step to fail.",
	})
	require.Error(t, err)

	var rollbackBindings int64
	require.NoError(t, db.Model(&mailinfra.MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 102).Count(&rollbackBindings).Error)
	require.Zero(t, rollbackBindings, "binding write must roll back when Core cannot finish its result")
	require.EqualValues(t, 1, validationBindingRootVersion(t, db, 102))
	var rollbackStored coreinfra.ResourceValidationModel
	require.NoError(t, db.First(&rollbackStored, rollbackJob.ID).Error)
	require.Equal(t, string(domain.ResourceValidationRunning), rollbackStored.Status)
	require.Equal(t, rollbackClaim, rollbackStored.ClaimToken)
}

func TestAdminMicrosoftEmailIdentityChangeResetsBindingAndOldCredentialsMySQL(t *testing.T) {
	db := validationBindingMySQL.Database(t, validationBindingMigrationsDir(t))
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (999, 'identity-admin@test.local', 'hash', 'super_admin')",
	).Error)
	createAdminIdentityBindingFixture(t, db, 103, "old-email-only@outlook.com", "email-only@binding.test")
	createAdminIdentityBindingFixture(t, db, 104, "old-new-credentials@outlook.com", "new-credentials@binding.test")
	createAdminIdentityBindingFixture(t, db, 105, "owner-only@outlook.com", "owner-only@binding.test")
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (106, 'identity-new-owner@test.local', 'hash', 'supplier')",
	).Error)

	resourceRepo := coreinfra.NewResourceRepo(db)
	validationRepo := coreinfra.NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminIdentityValidationQueue{}, nil)
	service := coreapp.NewAdminResourceCommandService(
		coreinfra.NewAdminResourceRepo(db),
		validation,
		governanceinfra.NewOperationLogRepo(db),
	)
	bindingRepo := mailinfra.NewMicrosoftBindingRepo(db)
	service.SetPorts(
		adminIdentityOwnerPort{owners: map[uint]coreapp.AdminOwnerSummary{
			106: {ID: 106, Email: "identity-new-owner@test.local", Role: "supplier", Enabled: true},
		}},
		NewMicrosoftBindingQueryAdapter(bindingRepo),
		NewMicrosoftBindingAdminAdapter(bindingRepo),
		adminIdentityAllocationGuard{},
	)

	emailOnly := "new-email-only@outlook.com"
	result, err := service.Edit(context.Background(), coreapp.AdminMicrosoftEditCommand{
		ResourceID: 103, Version: validationBindingRootVersion(t, db, 103), EmailAddress: &emailOnly,
		OperatorUserID: 999, IdempotencyKey: "admin-identity-email-only",
		RequestID: "req-admin-identity-email-only", Path: "/v1/admin/resources/:resourceId",
	})
	require.NoError(t, err)
	require.NotNil(t, result.ValidationTask)
	require.EqualValues(t, 2, result.ValidationTask.CredentialRevision)

	var emailOnlyResource coreinfra.MicrosoftResourceModel
	require.NoError(t, db.First(&emailOnlyResource, 103).Error)
	require.Equal(t, emailOnly, emailOnlyResource.EmailAddress)
	require.Empty(t, emailOnlyResource.Password, "an email-only PATCH must not try the previous account password")
	require.Empty(t, emailOnlyResource.ClientID, "the previous account client ID must be unreachable")
	require.Empty(t, emailOnlyResource.RefreshToken, "the previous account refresh token must be unreachable")
	require.EqualValues(t, 2, emailOnlyResource.CredentialRevision)
	require.Equal(t, string(domain.MicrosoftStatusPending), emailOnlyResource.Status)
	assertAdminIdentityBindingReset(t, db, 103, emailOnly, "email-only@binding.test")
	assertAdminIdentityValidationJob(t, db, 103, 2)

	emailWithCredentials := "new-with-credentials@outlook.com"
	result, err = service.Edit(context.Background(), coreapp.AdminMicrosoftEditCommand{
		ResourceID: 104, Version: validationBindingRootVersion(t, db, 104), EmailAddress: &emailWithCredentials,
		Credentials: &coreapp.AdminMicrosoftCredentials{
			Password: "new-account-password", ClientID: "new-account-client", RefreshToken: "new-account-refresh",
		},
		OperatorUserID: 999, IdempotencyKey: "admin-identity-new-credentials",
		RequestID: "req-admin-identity-new-credentials", Path: "/v1/admin/resources/:resourceId",
	})
	require.NoError(t, err)
	require.NotNil(t, result.ValidationTask)
	require.EqualValues(t, 2, result.ValidationTask.CredentialRevision)

	var credentialResource coreinfra.MicrosoftResourceModel
	require.NoError(t, db.First(&credentialResource, 104).Error)
	require.Equal(t, emailWithCredentials, credentialResource.EmailAddress)
	require.Equal(t, "new-account-password", credentialResource.Password)
	require.Equal(t, "new-account-client", credentialResource.ClientID)
	require.Equal(t, "new-account-refresh", credentialResource.RefreshToken)
	require.EqualValues(t, 2, credentialResource.CredentialRevision)
	require.Equal(t, string(domain.MicrosoftStatusPending), credentialResource.Status)
	assertAdminIdentityBindingReset(t, db, 104, emailWithCredentials, "new-credentials@binding.test")
	assertAdminIdentityValidationJob(t, db, 104, 2)

	newOwnerID := uint(106)
	result, err = service.Edit(context.Background(), coreapp.AdminMicrosoftEditCommand{
		ResourceID: 105, Version: validationBindingRootVersion(t, db, 105), OwnerUserID: &newOwnerID,
		OperatorUserID: 999, IdempotencyKey: "admin-identity-owner-only",
		RequestID: "req-admin-identity-owner-only", Path: "/v1/admin/resources/:resourceId",
	})
	require.NoError(t, err)
	require.NotNil(t, result.ValidationTask)
	require.EqualValues(t, 2, result.ValidationTask.CredentialRevision)

	var ownerOnlyResource coreinfra.MicrosoftResourceModel
	require.NoError(t, db.First(&ownerOnlyResource, 105).Error)
	require.Equal(t, "owner-only@outlook.com", ownerOnlyResource.EmailAddress)
	require.Equal(t, "old-account-password", ownerOnlyResource.Password)
	require.Equal(t, "old-account-client", ownerOnlyResource.ClientID)
	require.Equal(t, "old-account-refresh", ownerOnlyResource.RefreshToken)
	require.EqualValues(t, 2, ownerOnlyResource.CredentialRevision)
	var ownerOnlyBinding mailinfra.MicrosoftBindingMailboxModel
	require.NoError(t, db.Where("resource_id = ?", 105).First(&ownerOnlyBinding).Error)
	require.Equal(t, newOwnerID, ownerOnlyBinding.OwnerUserID)
	require.Equal(t, "owner-only@outlook.com", ownerOnlyBinding.AccountEmail)
	require.Equal(t, "owner-only@binding.test", ownerOnlyBinding.BindingAddress)
	require.Equal(t, string(maildomain.MicrosoftBindingVerified), ownerOnlyBinding.Status)
	require.Equal(t, "old-code-message", ownerOnlyBinding.CodeMessageID)
	require.NotNil(t, ownerOnlyBinding.VerifiedAt)
	assertAdminIdentityValidationJob(t, db, 105, 2)
}

func validationBindingMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func createValidationBindingFixture(t *testing.T, db *gorm.DB, resourceID uint, email string) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, 'hash', 'supplier')",
		resourceID,
		email+".owner",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'microsoft', ?)",
		resourceID,
		resourceID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO microsoft_resources(id, email_address, email_domain, password, status) VALUES (?, ?, 'outlook.com', 'secret', 'pending')",
		resourceID,
		email,
	).Error)
}

func createValidationBindingDomainFixture(t *testing.T, db *gorm.DB, ownerID, resourceID uint, domainName string) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, 'hash', 'supplier')",
		ownerID,
		"binding-domain-owner@test.local",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, name, server_address, status) VALUES (?, ?, 'binding-test', ?, 'online')",
		ownerID,
		ownerID,
		"mx."+domainName,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'domain', ?)",
		resourceID,
		ownerID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO domain_resources(id, resource_type, owner_user_id, domain, mail_server_id, purpose, status) VALUES (?, 'domain', ?, ?, ?, 'binding', 'normal')",
		resourceID,
		ownerID,
		domainName,
		ownerID,
	).Error)
}

func createRunningValidationBindingJob(t *testing.T, db *gorm.DB, repo *coreinfra.ResourceValidationRepo, resourceID uint) (*domain.ResourceValidation, string) {
	t.Helper()
	job := &domain.ResourceValidation{
		ResourceID: resourceID, ResourceType: domain.ResourceTypeMicrosoft, OwnerUserID: resourceID,
		Status: domain.ResourceValidationQueued, MaxAttempts: domain.ResourceValidationDefaultMaxAttempts,
		RequestID: "req-binding-commit",
	}
	created, err := repo.CreateWithLog(context.Background(), job, nil)
	require.NoError(t, err)
	require.True(t, created)
	claim := "11111111-1111-4111-8111-111111111111"
	require.NoError(t, db.Model(&coreinfra.ResourceValidationModel{}).Where("id = ?", job.ID).Updates(map[string]any{
		"status":      string(domain.ResourceValidationRunning),
		"claim_token": claim,
	}).Error)
	return job, claim
}

func validationBindingRootVersion(t *testing.T, db *gorm.DB, resourceID uint) uint64 {
	t.Helper()
	var version uint64
	require.NoError(t, db.Table("email_resources").Where("id = ?", resourceID).Pluck("version", &version).Error)
	return version
}

type adminIdentityValidationQueue struct{}

func (adminIdentityValidationQueue) EnqueueResourceValidation(context.Context, coreapp.ResourceValidationTask) error {
	return nil
}

func (adminIdentityValidationQueue) EnqueueResourceValidationDispatcher(context.Context, time.Duration) error {
	return nil
}

type adminIdentityAllocationGuard struct{}

func (adminIdentityAllocationGuard) AssertNoActiveAllocations(context.Context, []uint) error {
	return nil
}

type adminIdentityOwnerPort struct {
	owners map[uint]coreapp.AdminOwnerSummary
}

func (p adminIdentityOwnerPort) GetByIDs(_ context.Context, ids []uint) (map[uint]coreapp.AdminOwnerSummary, error) {
	result := make(map[uint]coreapp.AdminOwnerSummary, len(ids))
	for _, id := range ids {
		if owner, ok := p.owners[id]; ok {
			result[id] = owner
		}
	}
	return result, nil
}

func (p adminIdentityOwnerPort) SearchAdminOwners(_ context.Context, _ string, limit int) ([]coreapp.AdminOwnerSummary, error) {
	result := make([]coreapp.AdminOwnerSummary, 0, len(p.owners))
	for _, owner := range p.owners {
		result = append(result, owner)
		if limit > 0 && len(result) == limit {
			break
		}
	}
	return result, nil
}

func (p adminIdentityOwnerPort) ValidateTargetOwner(_ context.Context, id uint) (*coreapp.AdminOwnerSummary, error) {
	owner, ok := p.owners[id]
	if !ok {
		return nil, nil
	}
	return &owner, nil
}

func createAdminIdentityBindingFixture(t *testing.T, db *gorm.DB, resourceID uint, accountEmail, bindingAddress string) {
	t.Helper()
	createValidationBindingFixture(t, db, resourceID, accountEmail)
	require.NoError(t, db.Model(&coreinfra.MicrosoftResourceModel{}).
		Where("id = ?", resourceID).
		Updates(map[string]any{
			"password":        "old-account-password",
			"client_id":       "old-account-client",
			"refresh_token":   "old-account-refresh",
			"status":          string(domain.MicrosoftStatusNormal),
			"graph_available": true,
			"quality_score":   100,
		}).Error)
	staleAt := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, db.Create(&mailinfra.MicrosoftBindingMailboxModel{
		ResourceID: resourceID, ResourceType: "microsoft", OwnerUserID: resourceID,
		AccountEmail: accountEmail, BindingAddress: bindingAddress, Purpose: "validation",
		Status: string(maildomain.MicrosoftBindingVerified), CodeMessageID: "old-code-message",
		BoundDisplay: "ol***@external.test", Category: "old-category", LastSafeError: "old safe error",
		SelectedAt: &staleAt, CodeSentAt: &staleAt, VerifiedAt: &staleAt, ExpiresAt: ptrTime(staleAt.Add(time.Hour)),
	}).Error)
}

func assertAdminIdentityBindingReset(t *testing.T, db *gorm.DB, resourceID uint, accountEmail, bindingAddress string) {
	t.Helper()
	var binding mailinfra.MicrosoftBindingMailboxModel
	require.NoError(t, db.Where("resource_id = ?", resourceID).First(&binding).Error)
	require.Equal(t, accountEmail, binding.AccountEmail)
	require.Equal(t, bindingAddress, binding.BindingAddress)
	require.Equal(t, "validation", binding.Purpose)
	require.Equal(t, string(maildomain.MicrosoftBindingPending), binding.Status)
	require.Empty(t, binding.CodeMessageID)
	require.Empty(t, binding.BoundDisplay)
	require.Empty(t, binding.Category)
	require.Empty(t, binding.LastSafeError)
	require.Nil(t, binding.CodeSentAt)
	require.Nil(t, binding.VerifiedAt)
	require.Nil(t, binding.ExpiresAt)
}

func assertAdminIdentityValidationJob(t *testing.T, db *gorm.DB, resourceID uint, expectedRevision uint64) {
	t.Helper()
	var job coreinfra.ResourceValidationModel
	require.NoError(t, db.Where("resource_id = ?", resourceID).First(&job).Error)
	require.Equal(t, string(domain.ResourceValidationQueued), job.Status)
	require.Equal(t, expectedRevision, job.ExpectedCredentialRevision)
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
