package infra

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftTokenRefreshCreateReplayRevisionAndStateRulesMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9250, "normal", 1)
	createMicrosoftTokenRefreshTestResource(t, db, 9251, "disabled", 1)
	createMicrosoftTokenRefreshTestResource(t, db, 9252, "deleted", 1)
	createMicrosoftTokenRefreshTestResource(t, db, 9253, "normal", 1)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET client_id = '', refresh_token = '' WHERE id = 9253").Error)
	repo := newMicrosoftTokenRefreshTestRepo(db)
	command := microsoftTokenRefreshTestCommand(9250, "token-idempotency-9250")

	job, reused, err := repo.CreateOrReuse(context.Background(), command, microsoftTokenRefreshTestOperationLog(command))
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.False(t, reused)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshQueued, job.Status)
	assert.Equal(t, uint64(1), job.ExpectedCredentialRevision)
	firstJobID := job.ID

	replayed, reused, err := repo.CreateOrReuse(context.Background(), command, microsoftTokenRefreshTestOperationLog(command))
	require.NoError(t, err)
	assert.True(t, reused)
	assert.Equal(t, firstJobID, replayed.ID)

	activeReuseCommand := microsoftTokenRefreshTestCommand(9250, "different-key-same-revision")
	active, reused, err := repo.CreateOrReuse(context.Background(), activeReuseCommand, microsoftTokenRefreshTestOperationLog(activeReuseCommand))
	require.NoError(t, err)
	assert.True(t, reused)
	assert.Equal(t, firstJobID, active.ID)

	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET refresh_token = 'refresh-token-v2', credential_revision = 2, credential_updated_at = CURRENT_TIMESTAMP(3)
WHERE id = 9250`).Error)
	revisionCommand := microsoftTokenRefreshTestCommand(9250, "new-revision-key")
	revisionJob, reused, err := repo.CreateOrReuse(context.Background(), revisionCommand, microsoftTokenRefreshTestOperationLog(revisionCommand))
	require.NoError(t, err)
	assert.False(t, reused)
	assert.NotEqual(t, firstJobID, revisionJob.ID)
	assert.Equal(t, uint64(2), revisionJob.ExpectedCredentialRevision)
	var oldStatus string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_token_refresh_jobs WHERE id = ?", firstJobID).Scan(&oldStatus).Error)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshCanceled, oldStatus)

	disabledCommand := microsoftTokenRefreshTestCommand(9251, "disabled-diagnostic")
	disabledJob, reused, err := repo.CreateOrReuse(context.Background(), disabledCommand, microsoftTokenRefreshTestOperationLog(disabledCommand))
	require.NoError(t, err)
	assert.False(t, reused)
	assert.NotZero(t, disabledJob.ID)
	var disabledStatus string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_resources WHERE id = 9251").Scan(&disabledStatus).Error)
	assert.Equal(t, "disabled", disabledStatus)

	deletedCommand := microsoftTokenRefreshTestCommand(9252, "deleted-diagnostic")
	_, _, err = repo.CreateOrReuse(context.Background(), deletedCommand, microsoftTokenRefreshTestOperationLog(deletedCommand))
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenRefreshConflict)
	missingCommand := microsoftTokenRefreshTestCommand(9253, "missing-credentials")
	_, _, err = repo.CreateOrReuse(context.Background(), missingCommand, microsoftTokenRefreshTestOperationLog(missingCommand))
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenCredentialsMissing)

	conflictCommand := command
	conflictCommand.ResourceID = 9251
	_, _, err = repo.CreateOrReuse(context.Background(), conflictCommand, microsoftTokenRefreshTestOperationLog(conflictCommand))
	require.ErrorIs(t, err, mailapp.ErrMicrosoftAdminIdempotencyConflict)

	var receiptCount int64
	require.NoError(t, db.Model(&MicrosoftTokenRefreshRequestModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", command.OperatorUserID, command.IdempotencyKey).
		Count(&receiptCount).Error)
	assert.EqualValues(t, 1, receiptCount)
	var auditSummaries []string
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ? AND resource_id = ?", "mailtransport.microsoft_token_refresh.accept", "9250").
		Order("id ASC").
		Pluck("safe_summary", &auditSummaries).Error)
	require.GreaterOrEqual(t, len(auditSummaries), 3)
	for _, summary := range auditSummaries {
		assert.Contains(t, summary, "task=token:")
		assertTokenRefreshTextIsSafe(t, summary)
	}
}

func TestMicrosoftTokenRefreshReplaysReceiptAfterResourceDeletionMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9255, "normal", 1)
	repo := newMicrosoftTokenRefreshTestRepo(db)
	command := microsoftTokenRefreshTestCommand(9255, "token-replay-after-delete-9255")

	accepted, reused, err := repo.CreateOrReuse(
		context.Background(),
		command,
		microsoftTokenRefreshTestOperationLog(command),
	)
	require.NoError(t, err)
	require.NotNil(t, accepted)
	assert.False(t, reused)

	require.NoError(t, db.Exec("UPDATE microsoft_resources SET status = 'deleted' WHERE id = ?", 9255).Error)
	replayed, reused, err := repo.CreateOrReuse(
		context.Background(),
		command,
		microsoftTokenRefreshTestOperationLog(command),
	)
	require.NoError(t, err)
	require.NotNil(t, replayed)
	assert.True(t, reused)
	assert.Equal(t, accepted.ID, replayed.ID)

	fresh := microsoftTokenRefreshTestCommand(9255, "token-new-request-after-delete-9255")
	_, _, err = repo.CreateOrReuse(
		context.Background(),
		fresh,
		microsoftTokenRefreshTestOperationLog(fresh),
	)
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenRefreshConflict)
}

func TestMicrosoftTokenRefreshCreateSingleFlightUnderConcurrentRequestsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9254, "normal", 1)
	repo := newMicrosoftTokenRefreshTestRepo(db)

	const workerCount = 10
	start := make(chan struct{})
	jobs := make([]*mailapp.MicrosoftTokenRefreshJob, workerCount)
	reusedByWorker := make([]bool, workerCount)
	errorsByWorker := make([]error, workerCount)
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			command := microsoftTokenRefreshTestCommand(9254, "token-concurrent-"+strconv.Itoa(worker))
			command.RequestID = "request-token-concurrent-" + strconv.Itoa(worker)
			jobs[worker], reusedByWorker[worker], errorsByWorker[worker] = repo.CreateOrReuse(
				context.Background(),
				command,
				microsoftTokenRefreshTestOperationLog(command),
			)
		}(index)
	}
	close(start)
	workers.Wait()

	createdCount := 0
	jobIDs := make(map[uint64]struct{}, workerCount)
	for index, err := range errorsByWorker {
		require.NoError(t, err, "worker %d", index)
		if !reusedByWorker[index] {
			createdCount++
		}
		require.NotNil(t, jobs[index])
		jobIDs[jobs[index].ID] = struct{}{}
	}
	require.Equal(t, 1, createdCount)
	require.Len(t, jobIDs, 1)

	var jobCount, receiptCount, logCount int64
	require.NoError(t, db.Model(&MicrosoftTokenRefreshJobModel{}).
		Where("resource_id = ?", 9254).
		Count(&jobCount).Error)
	require.NoError(t, db.Model(&MicrosoftTokenRefreshRequestModel{}).
		Where("operator_user_id = ?", 9254).
		Count(&receiptCount).Error)
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ? AND resource_id = ?", "mailtransport.microsoft_token_refresh.accept", "9254").
		Count(&logCount).Error)
	require.EqualValues(t, 1, jobCount)
	require.EqualValues(t, workerCount, receiptCount)
	require.EqualValues(t, workerCount, logCount)
}

func TestMicrosoftTokenRefreshApplyRotationFencingAndDisabledSemanticsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9260, "disabled", 5)
	repo := newMicrosoftTokenRefreshTestRepo(db)
	command := microsoftTokenRefreshTestCommand(9260, "rotate-token-9260")
	job, _, err := repo.CreateOrReuse(context.Background(), command, microsoftTokenRefreshTestOperationLog(command))
	require.NoError(t, err)
	initialVersion := microsoftTokenRefreshRootVersion(t, db, 9260)

	dispatchable, err := repo.ClaimDispatchable(context.Background(), 10, time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	require.NotEmpty(t, dispatchable[0].DispatchToken)

	staleExecution, claimed, err := repo.ClaimExecution(context.Background(), job.ID, "stale-dispatch-token", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Nil(t, staleExecution)

	execution, claimed, err := repo.ClaimExecution(context.Background(), job.ID, dispatchable[0].DispatchToken, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, execution)
	require.NotEmpty(t, execution.Job.ClaimToken)
	assert.Equal(t, "client-id-9260", execution.ClientID)
	assert.Equal(t, "refresh-token-9260", execution.RefreshToken)

	err = repo.ApplyResult(context.Background(), job.ID, "stale-claim-token", mailapp.MicrosoftTokenRefreshProtocolResult{
		Valid:        true,
		ClientID:     "must-not-apply-client",
		RefreshToken: "must-not-apply-refresh-token",
	})
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenRefreshConflict)

	err = repo.ApplyResult(context.Background(), job.ID, execution.Job.ClaimToken, mailapp.MicrosoftTokenRefreshProtocolResult{
		Valid:        true,
		ClientID:     "rotated-client-9260",
		RefreshToken: "rotated-refresh-9260",
		Category:     "success",
	})
	require.NoError(t, err)

	var state struct {
		Status              string     `gorm:"column:status"`
		ClientID            string     `gorm:"column:client_id"`
		RefreshToken        string     `gorm:"column:refresh_token"`
		CredentialRevision  uint64     `gorm:"column:credential_revision"`
		CredentialUpdatedAt time.Time  `gorm:"column:credential_updated_at"`
		TokenRefreshedAt    *time.Time `gorm:"column:token_last_refreshed_at"`
		TokenRequestID      string     `gorm:"column:token_last_request_id"`
	}
	require.NoError(t, db.Raw(`
SELECT status, client_id, refresh_token, credential_revision,
       credential_updated_at, token_last_refreshed_at, token_last_request_id
FROM microsoft_resources
WHERE id = 9260`).Scan(&state).Error)
	assert.Equal(t, "disabled", state.Status)
	assert.Equal(t, "rotated-client-9260", state.ClientID)
	assert.Equal(t, "rotated-refresh-9260", state.RefreshToken)
	assert.Equal(t, uint64(6), state.CredentialRevision)
	assert.False(t, state.CredentialUpdatedAt.IsZero())
	require.NotNil(t, state.TokenRefreshedAt)
	assert.Equal(t, command.RequestID, state.TokenRequestID)
	assert.Equal(t, initialVersion+1, microsoftTokenRefreshRootVersion(t, db, 9260))

	var persistedJob MicrosoftTokenRefreshJobModel
	require.NoError(t, db.First(&persistedJob, job.ID).Error)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshSucceeded, persistedJob.Status)
	assert.Empty(t, persistedJob.ClaimToken)
	assert.Empty(t, persistedJob.DispatchToken)
	require.NotNil(t, persistedJob.FinishedAt)

	var logs []struct {
		Message string `gorm:"column:message"`
		Detail  string `gorm:"column:detail"`
	}
	require.NoError(t, db.Table("system_logs").
		Where("module = ? AND biz_type = ? AND biz_id = ?", "mailtransport", "microsoft_resource", "9260").
		Find(&logs).Error)
	require.NotEmpty(t, logs)
	for _, log := range logs {
		assertTokenRefreshTextIsSafe(t, log.Message+" "+log.Detail)
	}
}

func TestMicrosoftTokenRefreshRejectsStaleRevisionAndRetriesDurablyMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9270, "normal", 3)
	createMicrosoftTokenRefreshTestResource(t, db, 9271, "normal", 1)
	repo := newMicrosoftTokenRefreshTestRepo(db)

	staleCommand := microsoftTokenRefreshTestCommand(9270, "stale-revision-9270")
	staleJob, _, err := repo.CreateOrReuse(context.Background(), staleCommand, microsoftTokenRefreshTestOperationLog(staleCommand))
	require.NoError(t, err)
	staleExecution := claimMicrosoftTokenRefreshExecution(t, repo, staleJob.ID)
	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET refresh_token = 'external-refresh-token', credential_revision = 4
WHERE id = 9270`).Error)
	err = repo.ApplyResult(context.Background(), staleJob.ID, staleExecution.Job.ClaimToken, mailapp.MicrosoftTokenRefreshProtocolResult{
		Valid:        true,
		RefreshToken: "stale-worker-refresh-token",
	})
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenRefreshStale)
	var staleResource struct {
		RefreshToken       string `gorm:"column:refresh_token"`
		CredentialRevision uint64 `gorm:"column:credential_revision"`
	}
	require.NoError(t, db.Raw(`
SELECT refresh_token, credential_revision
FROM microsoft_resources WHERE id = 9270`).Scan(&staleResource).Error)
	assert.Equal(t, "external-refresh-token", staleResource.RefreshToken)
	assert.Equal(t, uint64(4), staleResource.CredentialRevision)
	var staleJobStatus string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_token_refresh_jobs WHERE id = ?", staleJob.ID).Scan(&staleJobStatus).Error)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshCanceled, staleJobStatus)

	retryCommand := microsoftTokenRefreshTestCommand(9271, "retry-token-9271")
	retryJob, _, err := repo.CreateOrReuse(context.Background(), retryCommand, microsoftTokenRefreshTestOperationLog(retryCommand))
	require.NoError(t, err)
	for attempt := 1; attempt <= mailapp.MicrosoftTokenRefreshDefaultMaxAttempts; attempt++ {
		execution := claimMicrosoftTokenRefreshExecution(t, repo, retryJob.ID)
		exhausted, retryErr := repo.MarkRetryableFailure(
			context.Background(),
			retryJob.ID,
			execution.Job.ClaimToken,
			"Microsoft mail service is temporarily unavailable.",
		)
		require.NoError(t, retryErr)
		assert.Equal(t, attempt == mailapp.MicrosoftTokenRefreshDefaultMaxAttempts, exhausted)
	}
	var retryPersisted MicrosoftTokenRefreshJobModel
	require.NoError(t, db.First(&retryPersisted, retryJob.ID).Error)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshFailed, retryPersisted.Status)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshDefaultMaxAttempts, retryPersisted.Attempts)
	assert.Equal(t, "Microsoft mail service is temporarily unavailable.", retryPersisted.LastSafeError)
}

func TestMicrosoftTokenRefreshAuditFailureRollsBackJobAndReceiptMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9280, "normal", 1)
	repo := newMicrosoftTokenRefreshTestRepo(db)
	command := microsoftTokenRefreshTestCommand(9280, "audit-rollback-9280")
	operationLog := microsoftTokenRefreshTestOperationLog(command)
	repo.operationLogs = failingAdminOperationLogWriter{err: errors.New("forced operation log failure")}

	_, _, err := repo.CreateOrReuse(context.Background(), command, operationLog)
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenRefreshUnavailable)
	var jobCount int64
	require.NoError(t, db.Model(&MicrosoftTokenRefreshJobModel{}).Where("resource_id = ?", 9280).Count(&jobCount).Error)
	assert.Zero(t, jobCount)
	var receiptCount int64
	require.NoError(t, db.Model(&MicrosoftTokenRefreshRequestModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", command.OperatorUserID, command.IdempotencyKey).
		Count(&receiptCount).Error)
	assert.Zero(t, receiptCount)
}

func createMicrosoftTokenRefreshTestResource(t *testing.T, db *gorm.DB, resourceID uint, status string, revision uint64) {
	t.Helper()
	createMicrosoftAliasTestResource(t, db, resourceID, status)
	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET client_id = ?, refresh_token = ?, credential_revision = ?, credential_updated_at = CURRENT_TIMESTAMP(3)
WHERE id = ?`,
		"client-id-"+strconv.FormatUint(uint64(resourceID), 10),
		"refresh-token-"+strconv.FormatUint(uint64(resourceID), 10),
		revision,
		resourceID,
	).Error)
}

func newMicrosoftTokenRefreshTestRepo(db *gorm.DB) *MicrosoftTokenRefreshRepo {
	repo := NewMicrosoftTokenRefreshRepo(db)
	repo.SetMicrosoftCredentialPort(coreapp.NewMicrosoftCredentialService(coreinfra.NewAdminResourceRepo(db)))
	return repo
}

func microsoftTokenRefreshTestCommand(resourceID uint, key string) mailapp.MicrosoftTokenRefreshCommand {
	return mailapp.MicrosoftTokenRefreshCommand{
		ResourceID:     resourceID,
		OperatorUserID: resourceID,
		IdempotencyKey: key,
		RequestID:      "request-" + strconv.FormatUint(uint64(resourceID), 10),
		Path:           "/v1/admin/resources/:resourceId/token/refresh",
	}
}

func microsoftTokenRefreshTestOperationLog(command mailapp.MicrosoftTokenRefreshCommand) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: command.OperatorUserID,
		OperationType:  "mailtransport.microsoft_token_refresh.accept",
		ResourceType:   "microsoft_resource",
		ResourceID:     strconv.FormatUint(uint64(command.ResourceID), 10),
		Path:           command.Path,
		Result:         "success",
		SafeSummary:    "Microsoft refresh-token diagnostic accepted.",
		RequestID:      command.RequestID,
	}
}

func claimMicrosoftTokenRefreshExecution(t *testing.T, repo *MicrosoftTokenRefreshRepo, jobID uint64) *mailapp.MicrosoftTokenRefreshExecution {
	t.Helper()
	now := time.Now().UTC()
	dispatchable, err := repo.ClaimDispatchable(context.Background(), 10, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	var job *mailapp.MicrosoftTokenRefreshJob
	for i := range dispatchable {
		if dispatchable[i].ID == jobID {
			copyJob := dispatchable[i]
			job = &copyJob
			break
		}
	}
	require.NotNil(t, job)
	execution, claimed, err := repo.ClaimExecution(context.Background(), job.ID, job.DispatchToken, now.Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, execution)
	return execution
}

func microsoftTokenRefreshRootVersion(t *testing.T, db *gorm.DB, resourceID uint) uint64 {
	t.Helper()
	var version uint64
	require.NoError(t, db.Raw("SELECT version FROM email_resources WHERE id = ?", resourceID).Scan(&version).Error)
	return version
}

func assertTokenRefreshTextIsSafe(t *testing.T, value string) {
	t.Helper()
	lower := strings.ToLower(value)
	for _, forbidden := range []string{
		"client-id-",
		"refresh-token-",
		"rotated-client",
		"rotated-refresh",
		"external-refresh",
		"stale-worker",
		"claim-token",
		"dispatch-token",
		"objectkey",
	} {
		assert.NotContains(t, lower, forbidden)
	}
}
