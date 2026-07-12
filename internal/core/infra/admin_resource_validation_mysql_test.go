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
	}, nil)
	require.ErrorIs(t, err, coreapp.ErrValidationResultStale)

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
