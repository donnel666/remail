package infra

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResourceValidationRepoDiscardsStaleMicrosoftResultMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	bindingCommit := &validationBindingCommitSpy{}
	validationRepo.SetMicrosoftValidationBindingCommitPort(bindingCommit)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-stale-result@outlook.com",
		Password:     "initial-password",
		ClientID:     "initial-client",
		RefreshToken: "initial-refresh",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
	job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)
	require.EqualValues(t, 1, job.ExpectedCredentialRevision)

	// Simulate a newer administrator credential replacement committed while the
	// worker was performing its external Microsoft call.
	require.NoError(t, db.Model(&MicrosoftResourceModel{}).
		Where("id = ?", resource.ID).
		Updates(map[string]any{
			"client_id":             "admin-client",
			"refresh_token":         "admin-refresh",
			"credential_revision":   2,
			"credential_updated_at": time.Now().UTC(),
		}).Error)
	require.NoError(t, db.Model(&EmailResourceModel{}).
		Where("id = ?", resource.ID).
		Update("version", 2).Error)

	err := validationRepo.ApplyMicrosoftResult(ctx, job.ID, resource.ID, claimToken, coreapp.MicrosoftValidationResult{
		Valid:          true,
		ClientID:       "stale-worker-client",
		RefreshToken:   "stale-worker-refresh",
		GraphAvailable: true,
		RecoveredBinding: &coreapp.MicrosoftRecoveredBinding{
			Address: "must-not-commit@example.test",
		},
	}, nil)
	require.ErrorIs(t, err, coreapp.ErrValidationResultStale)
	require.Zero(t, bindingCommit.calls, "credential revision fence must run before MailTransport binding persistence")

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, resource.ID).Error)
	require.Equal(t, "admin-client", stored.ClientID)
	require.Equal(t, "admin-refresh", stored.RefreshToken)
	require.EqualValues(t, 2, stored.CredentialRevision)
	require.False(t, stored.GraphAvailable)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version, "discarding a stale result must not create a second visible mutation")

	var storedJob ResourceValidationModel
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	require.Equal(t, string(domain.ResourceValidationFailed), storedJob.Status)
	require.Empty(t, storedJob.ClaimToken)
	require.Contains(t, storedJob.LastSafeError, "stale validation result")
}

func TestResourceValidationRepoAppliesRotatedCredentialsWithRevisionAndVersionMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-rotation@outlook.com",
		Password:     "initial-password",
		ClientID:     "initial-client",
		RefreshToken: "initial-refresh",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
	job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)
	expireAt := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Microsecond)

	require.NoError(t, validationRepo.ApplyMicrosoftResult(ctx, job.ID, resource.ID, claimToken, coreapp.MicrosoftValidationResult{
		Valid:          true,
		ClientID:       "rotated-client",
		RefreshToken:   "rotated-refresh",
		RTExpireAt:     &expireAt,
		GraphAvailable: true,
	}, nil))

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, resource.ID).Error)
	require.Equal(t, "rotated-client", stored.ClientID)
	require.Equal(t, "rotated-refresh", stored.RefreshToken)
	require.EqualValues(t, 2, stored.CredentialRevision)
	require.NotZero(t, stored.CredentialUpdatedAt)
	require.NotNil(t, stored.TokenLastRefreshedAt)
	require.Equal(t, job.RequestID, stored.TokenLastRequestID)
	require.True(t, stored.GraphAvailable)
	require.Equal(t, string(domain.MicrosoftStatusNormal), stored.Status)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version)

	var storedJob ResourceValidationModel
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	require.Equal(t, string(domain.ResourceValidationSucceeded), storedJob.Status)
	require.Empty(t, storedJob.ClaimToken)

	err := validationRepo.ApplyMicrosoftResult(ctx, job.ID, resource.ID, claimToken, coreapp.MicrosoftValidationResult{Valid: true}, nil)
	require.ErrorIs(t, err, domain.ErrInvalidResourceStatus)
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version, "a duplicate delivery must be fenced before changing the resource")
}

func TestResourceValidationRepoPersistsOnlyAuthoritativeCredentialsOnInvalidResultMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	create := func(email string) (*domain.MicrosoftResource, *domain.ResourceValidation, string) {
		root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
		resource := &domain.MicrosoftResource{
			EmailAddress: email,
			Password:     "initial-password",
			ClientID:     "initial-client",
			RefreshToken: "initial-refresh",
			Status:       domain.MicrosoftStatusPending,
		}
		require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
		job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)
		return resource, job, claimToken
	}

	authoritative, authoritativeJob, authoritativeClaim := create("validation-invalid-authoritative@outlook.com")
	require.NoError(t, validationRepo.ApplyMicrosoftResult(ctx, authoritativeJob.ID, authoritative.ID, authoritativeClaim, coreapp.MicrosoftValidationResult{
		Valid:                    false,
		ClientID:                 "rotated-client",
		RefreshToken:             "rotated-refresh",
		CredentialsAuthoritative: true,
		Category:                 "password",
		SafeMessage:              "Microsoft account password is incorrect.",
	}, nil))

	var storedAuthoritative MicrosoftResourceModel
	require.NoError(t, db.First(&storedAuthoritative, authoritative.ID).Error)
	require.Equal(t, "rotated-client", storedAuthoritative.ClientID)
	require.Equal(t, "rotated-refresh", storedAuthoritative.RefreshToken)
	require.EqualValues(t, 2, storedAuthoritative.CredentialRevision)
	require.Equal(t, string(domain.MicrosoftStatusAbnormal), storedAuthoritative.Status)
	var authoritativeRoot EmailResourceModel
	require.NoError(t, db.First(&authoritativeRoot, authoritative.ID).Error)
	require.EqualValues(t, 2, authoritativeRoot.Version, "credential rotation and final invalid status share one root bump")

	untrusted, untrustedJob, untrustedClaim := create("validation-invalid-untrusted@outlook.com")
	require.NoError(t, validationRepo.ApplyMicrosoftResult(ctx, untrustedJob.ID, untrusted.ID, untrustedClaim, coreapp.MicrosoftValidationResult{
		Valid:        false,
		ClientID:     "must-not-save-client",
		RefreshToken: "must-not-save-refresh",
		Category:     "password",
		SafeMessage:  "Microsoft account password is incorrect.",
	}, nil))

	var storedUntrusted MicrosoftResourceModel
	require.NoError(t, db.First(&storedUntrusted, untrusted.ID).Error)
	require.Equal(t, "initial-client", storedUntrusted.ClientID)
	require.Equal(t, "initial-refresh", storedUntrusted.RefreshToken)
	require.EqualValues(t, 1, storedUntrusted.CredentialRevision)
	require.Equal(t, string(domain.MicrosoftStatusAbnormal), storedUntrusted.Status)
	var untrustedRoot EmailResourceModel
	require.NoError(t, db.First(&untrustedRoot, untrusted.ID).Error)
	require.EqualValues(t, 2, untrustedRoot.Version)
}

func TestResourceValidationRepoCommitsRecoveredBindingInsideFencedResultTransactionMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	bindingCommit := &validationBindingCommitSpy{}
	validationRepo.SetMicrosoftValidationBindingCommitPort(bindingCommit)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-binding-commit@outlook.com",
		Password:     "initial-password",
		ClientID:     "initial-client",
		RefreshToken: "initial-refresh",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
	job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)
	snapshotTime := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)

	require.NoError(t, validationRepo.ApplyMicrosoftResult(ctx, job.ID, resource.ID, claimToken, coreapp.MicrosoftValidationResult{
		Valid: true,
		RecoveredBinding: &coreapp.MicrosoftRecoveredBinding{
			Address:                  "recovered-binding@example.test",
			ExpectedBindingID:        77,
			ExpectedBindingAddress:   "old-binding@example.test",
			ExpectedBindingUpdatedAt: snapshotTime,
		},
	}, nil))

	require.Equal(t, 1, bindingCommit.calls)
	require.True(t, bindingCommit.sawCallerTransaction)
	require.Equal(t, coreapp.MicrosoftValidationBindingCommand{
		ResourceID:   resource.ID,
		OwnerUserID:  1,
		AccountEmail: resource.EmailAddress,
		RecoveredBinding: &coreapp.MicrosoftRecoveredBinding{
			Address:                  "recovered-binding@example.test",
			ExpectedBindingID:        77,
			ExpectedBindingAddress:   "old-binding@example.test",
			ExpectedBindingUpdatedAt: snapshotTime,
		},
	}, bindingCommit.command)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version, "binding and validation must share Core's single root version advance")
}

func TestResourceValidationRepoDiscardsRecoveredBindingWhenSnapshotPortReportsStaleMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	bindingCommit := &validationBindingCommitSpy{err: coreapp.ErrValidationResultStale}
	validationRepo.SetMicrosoftValidationBindingCommitPort(bindingCommit)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-binding-stale@outlook.com",
		Password:     "initial-password",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
	job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)

	err := validationRepo.ApplyMicrosoftResult(ctx, job.ID, resource.ID, claimToken, coreapp.MicrosoftValidationResult{
		Valid: true,
		RecoveredBinding: &coreapp.MicrosoftRecoveredBinding{
			Address: "recovered-binding@example.test",
		},
	}, nil)
	require.ErrorIs(t, err, coreapp.ErrValidationResultStale)
	require.Equal(t, 1, bindingCommit.calls)
	require.True(t, bindingCommit.sawCallerTransaction)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 1, storedRoot.Version)

	var storedResource MicrosoftResourceModel
	require.NoError(t, db.First(&storedResource, resource.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusPending), storedResource.Status)

	var storedJob ResourceValidationModel
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	require.Equal(t, string(domain.ResourceValidationFailed), storedJob.Status)
	require.Contains(t, storedJob.LastSafeError, "stale validation result")
}

func TestResourceValidationRepoTerminatesRejectedRecoveredBindingMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	bindingCommit := &validationBindingCommitSpy{err: coreapp.ErrValidationBindingRejected}
	validationRepo.SetMicrosoftValidationBindingCommitPort(bindingCommit)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-binding-rejected@outlook.com",
		Password:     "initial-password",
		ClientID:     "initial-client",
		RefreshToken: "initial-refresh",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
	job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)

	err := validationRepo.ApplyMicrosoftResult(ctx, job.ID, resource.ID, claimToken, coreapp.MicrosoftValidationResult{
		Valid:                    true,
		ClientID:                 "rotated-client",
		RefreshToken:             "rotated-refresh",
		CredentialsAuthoritative: true,
		RecoveredBinding: &coreapp.MicrosoftRecoveredBinding{
			Address: "occupied@binding.example",
		},
	}, nil)
	require.ErrorIs(t, err, coreapp.ErrValidationBindingRejected)
	require.Equal(t, 1, bindingCommit.calls)
	require.True(t, bindingCommit.sawCallerTransaction)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version)

	var storedResource MicrosoftResourceModel
	require.NoError(t, db.First(&storedResource, resource.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusAbnormal), storedResource.Status)
	require.Equal(t, coreapp.MicrosoftValidationBindingRejectedMessage, storedResource.LastSafeError)
	require.Equal(t, "rotated-client", storedResource.ClientID)
	require.Equal(t, "rotated-refresh", storedResource.RefreshToken)
	require.EqualValues(t, 2, storedResource.CredentialRevision)

	var storedJob ResourceValidationModel
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	require.Equal(t, string(domain.ResourceValidationFailed), storedJob.Status)
	require.Empty(t, storedJob.ClaimToken)
	require.Equal(t, coreapp.MicrosoftValidationBindingRejectedMessage, storedJob.LastSafeError)

	var systemLog struct {
		EventType string `gorm:"column:event_type"`
		Detail    string `gorm:"column:detail"`
	}
	require.NoError(t, db.Table("system_logs").
		Select("event_type, detail").
		Where("request_id = ?", job.RequestID).
		Take(&systemLog).Error)
	require.Equal(t, "resource.validation_failed", systemLog.EventType)
	require.Contains(t, systemLog.Detail, coreapp.MicrosoftValidationBindingRejectedMessage)
}

func TestResourceValidationRepoPersistsRetryRotationAndAdvancesJobFenceMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-retry-rotation@outlook.com",
		Password:     "initial-password",
		ClientID:     "initial-client",
		RefreshToken: "initial-refresh",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
	job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)

	require.NoError(t, validationRepo.SaveMicrosoftCredentials(
		ctx,
		job.ID,
		resource.ID,
		claimToken,
		"retry-client",
		"retry-refresh",
	))

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, resource.ID).Error)
	require.EqualValues(t, 2, stored.CredentialRevision)
	require.Equal(t, "retry-client", stored.ClientID)
	require.Equal(t, "retry-refresh", stored.RefreshToken)

	var storedJob ResourceValidationModel
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	require.EqualValues(t, 2, storedJob.ExpectedCredentialRevision)
	require.Equal(t, string(domain.ResourceValidationRunning), storedJob.Status)
	require.Equal(t, claimToken, storedJob.ClaimToken)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version)
}

func TestResourceValidationRepoSaveMicrosoftProgressCommitsRetryableRecoveryAndCredentialsOnceMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	bindingCommit := &validationBindingCommitSpy{changed: true}
	validationRepo.SetMicrosoftValidationBindingCommitPort(bindingCommit)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-retry-progress@outlook.com",
		Password:     "initial-password",
		ClientID:     "initial-client",
		RefreshToken: "initial-refresh",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
	job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)

	require.NoError(t, validationRepo.SaveMicrosoftProgress(ctx, job.ID, resource.ID, claimToken, coreapp.MicrosoftValidationResult{
		Valid:                    false,
		Category:                 "request",
		ClientID:                 "rotated-client",
		RefreshToken:             "rotated-refresh",
		CredentialsAuthoritative: true,
		RecoveredBinding: &coreapp.MicrosoftRecoveredBinding{
			Address: "recovered@binding.test",
		},
	}))

	require.Equal(t, 1, bindingCommit.calls)
	require.True(t, bindingCommit.sawCallerTransaction)
	require.Equal(t, resource.ID, bindingCommit.command.ResourceID)
	require.Equal(t, uint(1), bindingCommit.command.OwnerUserID)
	require.Equal(t, resource.EmailAddress, bindingCommit.command.AccountEmail)
	require.NotNil(t, bindingCommit.command.RecoveredBinding)

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, resource.ID).Error)
	require.Equal(t, "rotated-client", stored.ClientID)
	require.Equal(t, "rotated-refresh", stored.RefreshToken)
	require.EqualValues(t, 2, stored.CredentialRevision)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)

	var storedJob ResourceValidationModel
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	require.Equal(t, string(domain.ResourceValidationRunning), storedJob.Status)
	require.Equal(t, claimToken, storedJob.ClaimToken)
	require.EqualValues(t, 2, storedJob.ExpectedCredentialRevision)

	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version, "binding and credential progress must advance the root only once")

	untrustedRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	untrusted := &domain.MicrosoftResource{
		EmailAddress: "validation-retry-untrusted@outlook.com",
		Password:     "initial-password",
		ClientID:     "initial-client",
		RefreshToken: "initial-refresh",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, untrustedRoot, untrusted))
	untrustedJob, untrustedClaim := createRunningMicrosoftValidation(t, db, validationRepo, untrusted.ID)
	require.NoError(t, validationRepo.SaveMicrosoftProgress(ctx, untrustedJob.ID, untrusted.ID, untrustedClaim, coreapp.MicrosoftValidationResult{
		Valid:        false,
		Category:     "request",
		ClientID:     "must-not-save-client",
		RefreshToken: "must-not-save-refresh",
	}))
	var storedUntrusted MicrosoftResourceModel
	require.NoError(t, db.First(&storedUntrusted, untrusted.ID).Error)
	require.Equal(t, "initial-client", storedUntrusted.ClientID)
	require.Equal(t, "initial-refresh", storedUntrusted.RefreshToken)
	require.EqualValues(t, 1, storedUntrusted.CredentialRevision)
	var storedUntrustedJob ResourceValidationModel
	require.NoError(t, db.First(&storedUntrustedJob, untrustedJob.ID).Error)
	require.EqualValues(t, 1, storedUntrustedJob.ExpectedCredentialRevision)
	var storedUntrustedRoot EmailResourceModel
	require.NoError(t, db.First(&storedUntrustedRoot, untrusted.ID).Error)
	require.EqualValues(t, 1, storedUntrustedRoot.Version)
}

func TestResourceValidationRepoRetryExhaustionMarksResourceAbnormalAndBumpsRootMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-retry-exhaustion@outlook.com",
		Password:     "initial-password",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))
	job, claimToken := createRunningMicrosoftValidation(t, db, validationRepo, resource.ID)
	require.NoError(t, db.Model(&ResourceValidationModel{}).
		Where("id = ?", job.ID).
		Updates(map[string]any{
			"attempts":     2,
			"max_attempts": 3,
		}).Error)

	exhausted, err := validationRepo.MarkRetryableFailure(ctx, job.ID, claimToken, "Microsoft authorization timed out.")
	require.NoError(t, err)
	require.True(t, exhausted)

	var storedResource MicrosoftResourceModel
	require.NoError(t, db.First(&storedResource, resource.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusAbnormal), storedResource.Status)
	require.Equal(t, "Microsoft authorization timed out.", storedResource.LastSafeError)
	var storedRoot EmailResourceModel
	require.NoError(t, db.First(&storedRoot, resource.ID).Error)
	require.EqualValues(t, 2, storedRoot.Version)
	var storedJob ResourceValidationModel
	require.NoError(t, db.First(&storedJob, job.ID).Error)
	require.Equal(t, string(domain.ResourceValidationFailed), storedJob.Status)
	require.Equal(t, 3, storedJob.Attempts)
	require.Empty(t, storedJob.ClaimToken)
}

func TestResourceValidationRepoCreateWithLogJoinsCallerTransactionMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress:  "validation-parent-transaction@outlook.com",
		Password:      "initial-password",
		Status:        domain.MicrosoftStatusAbnormal,
		LastSafeError: "safe previous failure",
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))

	rollbackErr := errors.New("force parent rollback")
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job := &domain.ResourceValidation{
			ResourceID:   resource.ID,
			ResourceType: domain.ResourceTypeMicrosoft,
			OwnerUserID:  1,
			Status:       domain.ResourceValidationQueued,
			MaxAttempts:  domain.ResourceValidationDefaultMaxAttempts,
			RequestID:    "req-parent-transaction",
		}
		created, createErr := validationRepo.CreateWithLog(platform.WithGormTx(ctx, tx), job, nil)
		require.NoError(t, createErr)
		require.True(t, created)
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)

	var jobs int64
	require.NoError(t, db.Model(&ResourceValidationModel{}).
		Where("resource_id = ?", resource.ID).
		Count(&jobs).Error)
	require.Zero(t, jobs)

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, resource.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusAbnormal), stored.Status)
	require.Equal(t, "safe previous failure", stored.LastSafeError)
}

func TestResourceValidationRepoCreateSingleFlightUnderConcurrentRequestsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	resource := &domain.MicrosoftResource{
		EmailAddress: "validation-100-concurrent@outlook.com",
		Password:     "initial-password",
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(ctx, root, resource))

	const workerCount = 10
	start := make(chan struct{})
	jobs := make([]domain.ResourceValidation, workerCount)
	createdByWorker := make([]bool, workerCount)
	errorsByWorker := make([]error, workerCount)
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			job := domain.ResourceValidation{
				ResourceID:   resource.ID,
				ResourceType: domain.ResourceTypeMicrosoft,
				OwnerUserID:  1,
				Status:       domain.ResourceValidationQueued,
				MaxAttempts:  domain.ResourceValidationDefaultMaxAttempts,
				RequestID:    fmt.Sprintf("req-validation-concurrent-%03d", worker),
				Path:         "/v1/admin/resources/:resourceId/validate",
			}
			createdByWorker[worker], errorsByWorker[worker] = validationRepo.CreateWithLog(ctx, &job, nil)
			jobs[worker] = job
		}(index)
	}
	close(start)
	workers.Wait()

	createdCount := 0
	jobIDs := make(map[uint]struct{}, workerCount)
	for index, err := range errorsByWorker {
		require.NoError(t, err, "worker %d", index)
		if createdByWorker[index] {
			createdCount++
		}
		require.NotZero(t, jobs[index].ID)
		jobIDs[jobs[index].ID] = struct{}{}
	}
	require.Equal(t, 1, createdCount)
	require.Len(t, jobIDs, 1)

	var totalJobs, activeJobs int64
	require.NoError(t, db.Model(&ResourceValidationModel{}).
		Where("resource_id = ?", resource.ID).
		Count(&totalJobs).Error)
	require.NoError(t, db.Model(&ResourceValidationModel{}).
		Where("resource_id = ? AND status IN ?", resource.ID, []string{
			string(domain.ResourceValidationQueued),
			string(domain.ResourceValidationRunning),
		}).
		Count(&activeJobs).Error)
	require.EqualValues(t, 1, totalJobs)
	require.EqualValues(t, 1, activeJobs)
}

func TestResourceValidationRepoDomainResultUsesResourceBoundRootSubtypeJobFenceMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	ctx := context.Background()

	insertAdminValidationOwner(t, db)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, name, server_address, status) VALUES (?, ?, ?, ?, ?)",
		700,
		1,
		"domain-validation",
		"mx.domain-validation.test",
		"online",
	).Error)
	createDomain := func(name string) (*domain.EmailResource, *domain.MailDomainResource) {
		root := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
		resource := &domain.MailDomainResource{
			Domain:       name,
			MailServerID: 700,
			Purpose:      domain.PurposeNotSale,
			Status:       domain.DomainStatusAbnormal,
		}
		require.NoError(t, resourceRepo.CreateDomain(ctx, root, resource))
		return root, resource
	}
	rootA, resourceA := createDomain("validation-fence-a.test")
	_, resourceB := createDomain("validation-fence-b.test")
	job, claimToken := createRunningDomainValidation(t, db, validationRepo, rootA.ID)

	err := validationRepo.ApplyDomainResult(ctx, job.ID, resourceB.ID, claimToken, coreapp.DomainValidationResult{Valid: true}, nil)
	require.ErrorIs(t, err, domain.ErrInvalidResourceStatus)

	var untouched DomainResourceModel
	require.NoError(t, db.First(&untouched, resourceB.ID).Error)
	require.Equal(t, string(domain.DomainStatusAbnormal), untouched.Status)
	var runningJob ResourceValidationModel
	require.NoError(t, db.First(&runningJob, job.ID).Error)
	require.Equal(t, string(domain.ResourceValidationRunning), runningJob.Status)
	require.Equal(t, claimToken, runningJob.ClaimToken)

	require.NoError(t, validationRepo.ApplyDomainResult(ctx, job.ID, resourceA.ID, claimToken, coreapp.DomainValidationResult{Valid: true}, nil))
	var validated DomainResourceModel
	require.NoError(t, db.First(&validated, resourceA.ID).Error)
	require.Equal(t, string(domain.DomainStatusNormal), validated.Status)
	var finishedJob ResourceValidationModel
	require.NoError(t, db.First(&finishedJob, job.ID).Error)
	require.Equal(t, string(domain.ResourceValidationSucceeded), finishedJob.Status)
	require.Empty(t, finishedJob.ClaimToken)
}

func insertAdminValidationOwner(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"admin-validation-owner@test.local",
		"hash",
		"supplier",
	).Error)
}

func createRunningMicrosoftValidation(t *testing.T, db *gorm.DB, repo *ResourceValidationRepo, resourceID uint) (*domain.ResourceValidation, string) {
	t.Helper()
	job := &domain.ResourceValidation{
		ResourceID:   resourceID,
		ResourceType: domain.ResourceTypeMicrosoft,
		OwnerUserID:  1,
		Status:       domain.ResourceValidationQueued,
		MaxAttempts:  domain.ResourceValidationDefaultMaxAttempts,
		RequestID:    "req-admin-validation-fence",
		Path:         "/v1/admin/resources/:resourceId/validate",
	}
	created, err := repo.CreateWithLog(context.Background(), job, nil)
	require.NoError(t, err)
	require.True(t, created)
	claimToken := "11111111-1111-4111-8111-111111111111"
	require.NoError(t, db.Model(&ResourceValidationModel{}).
		Where("id = ?", job.ID).
		Updates(map[string]any{
			"status":      string(domain.ResourceValidationRunning),
			"claim_token": claimToken,
		}).Error)
	return job, claimToken
}

func createRunningDomainValidation(t *testing.T, db *gorm.DB, repo *ResourceValidationRepo, resourceID uint) (*domain.ResourceValidation, string) {
	t.Helper()
	job := &domain.ResourceValidation{
		ResourceID:   resourceID,
		ResourceType: domain.ResourceTypeDomain,
		OwnerUserID:  1,
		Status:       domain.ResourceValidationQueued,
		MaxAttempts:  domain.ResourceValidationDefaultMaxAttempts,
		RequestID:    "req-domain-validation-fence",
		Path:         "/v1/admin/resources/:resourceId/validate",
	}
	created, err := repo.CreateWithLog(context.Background(), job, nil)
	require.NoError(t, err)
	require.True(t, created)
	claimToken := "22222222-2222-4222-8222-222222222222"
	require.NoError(t, db.Model(&ResourceValidationModel{}).
		Where("id = ?", job.ID).
		Updates(map[string]any{
			"status":      string(domain.ResourceValidationRunning),
			"claim_token": claimToken,
		}).Error)
	return job, claimToken
}

type validationBindingCommitSpy struct {
	calls                int
	sawCallerTransaction bool
	command              coreapp.MicrosoftValidationBindingCommand
	changed              bool
	err                  error
}

func (s *validationBindingCommitSpy) CommitValidationBinding(ctx context.Context, command coreapp.MicrosoftValidationBindingCommand) (bool, error) {
	s.calls++
	s.command = command
	_, s.sawCallerTransaction = platform.GormTxFromContext(ctx)
	return s.changed, s.err
}
