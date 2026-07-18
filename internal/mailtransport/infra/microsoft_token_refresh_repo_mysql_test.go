package infra

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftTokenRefreshRequestUsesResourceStateMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9250, "normal", 1)
	createMicrosoftTokenRefreshTestResource(t, db, 9251, "disabled", 1)
	createMicrosoftTokenRefreshTestResource(t, db, 9252, "deleted", 1)
	createMicrosoftTokenRefreshTestResource(t, db, 9253, "normal", 1)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET client_id = '', refresh_token = '' WHERE id = 9253").Error)
	repo := newMicrosoftTokenRefreshTestRepo(db)
	command := microsoftTokenRefreshTestCommand(9250, "token-idempotency-9250")

	state, reused, err := repo.Request(context.Background(), command, microsoftTokenRefreshTestOperationLog(command))
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.False(t, reused)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshPending, state.Status)
	assert.Equal(t, uint64(1), state.Generation)
	assert.Equal(t, uint64(1), state.ExpectedCredentialRevision)

	replayed, reused, err := repo.Request(context.Background(), command, microsoftTokenRefreshTestOperationLog(command))
	require.NoError(t, err)
	require.True(t, reused)
	assert.Equal(t, state.Generation, replayed.Generation)

	conflict := command
	conflict.ResourceID = 9251
	_, _, err = repo.Request(context.Background(), conflict, microsoftTokenRefreshTestOperationLog(conflict))
	require.ErrorIs(t, err, mailapp.ErrMicrosoftAdminIdempotencyConflict)

	retrigger := microsoftTokenRefreshTestCommand(9250, "token-retrigger-9250")
	retriggered, reused, err := repo.Request(context.Background(), retrigger, microsoftTokenRefreshTestOperationLog(retrigger))
	require.NoError(t, err)
	assert.False(t, reused)
	assert.Equal(t, uint64(2), retriggered.Generation)
	assert.Zero(t, retriggered.Failures)

	deleted := microsoftTokenRefreshTestCommand(9252, "deleted-diagnostic")
	_, _, err = repo.Request(context.Background(), deleted, microsoftTokenRefreshTestOperationLog(deleted))
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenRefreshConflict)

	missing := microsoftTokenRefreshTestCommand(9253, "missing-credentials")
	_, _, err = repo.Request(context.Background(), missing, microsoftTokenRefreshTestOperationLog(missing))
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenCredentialsMissing)

	var persisted MicrosoftTokenRefreshStateModel
	require.NoError(t, db.First(&persisted, 9250).Error)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshPending, persisted.Status)
	assert.Equal(t, retrigger.IdempotencyKey, persisted.IdempotencyKey)
	assert.Equal(t, uint64(2), persisted.Generation)

	var auditSummaries []string
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ? AND resource_id = ?", "mailtransport.microsoft_token_refresh.accept", "9250").
		Order("id ASC").
		Pluck("safe_summary", &auditSummaries).Error)
	require.Len(t, auditSummaries, 3)
	for _, summary := range auditSummaries {
		assert.Contains(t, summary, "task=token:9250")
		assertTokenRefreshTextIsSafe(t, summary)
	}
}

func TestMicrosoftTokenRefreshConcurrentReplayDoesNotRetriggerGenerationMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9254, "normal", 1)
	repo := newMicrosoftTokenRefreshTestRepo(db)
	command := microsoftTokenRefreshTestCommand(9254, "concurrent-replay-9254")

	const workers = 8
	start := make(chan struct{})
	states := make([]*mailapp.MicrosoftTokenRefreshState, workers)
	reused := make([]bool, workers)
	errs := make([]error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			states[index], reused[index], errs[index] = repo.Request(context.Background(), command, nil)
		}(i)
	}
	close(start)
	group.Wait()

	created := 0
	for i := range errs {
		require.NoError(t, errs[i], "worker %d", i)
		require.NotNil(t, states[i])
		assert.Equal(t, uint64(1), states[i].Generation)
		if !reused[i] {
			created++
		}
	}
	assert.Equal(t, 1, created)
	persisted := loadMicrosoftTokenRefreshState(t, db, 9254)
	assert.Equal(t, uint64(1), persisted.Generation)
}

func TestMicrosoftTokenRefreshEnqueueActivationAndFailureStateMachineMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9260, "normal", 4)
	repo := newMicrosoftTokenRefreshTestRepo(db)
	command := microsoftTokenRefreshTestCommand(9260, "state-machine-9260")
	state, _, err := repo.Request(context.Background(), command, microsoftTokenRefreshTestOperationLog(command))
	require.NoError(t, err)

	pending, err := repo.ListPending(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, state.Generation, pending[0].Generation)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshPending, loadMicrosoftTokenRefreshState(t, db, 9260).Status,
		"scanning must not activate work before queue acceptance")

	activated, err := repo.MarkProcessing(context.Background(), 9260, state.Generation+1)
	require.NoError(t, err)
	assert.False(t, activated)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshPending, loadMicrosoftTokenRefreshState(t, db, 9260).Status)

	activated, err = repo.MarkProcessing(context.Background(), 9260, state.Generation)
	require.NoError(t, err)
	require.True(t, activated)
	released, err := repo.ReleaseInfrastructureFailure(context.Background(), 9260, state.Generation, "Redis unavailable.")
	require.NoError(t, err)
	require.True(t, released)
	persisted := loadMicrosoftTokenRefreshState(t, db, 9260)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshPending, persisted.Status)
	assert.Equal(t, state.Generation+1, persisted.Generation)
	assert.Zero(t, persisted.Failures, "infrastructure failure must not consume a business attempt")

	for attempt := 1; attempt <= mailapp.MicrosoftTokenRefreshDefaultMaxAttempts; attempt++ {
		persisted = loadMicrosoftTokenRefreshState(t, db, 9260)
		activated, err = repo.MarkProcessing(context.Background(), 9260, persisted.Generation)
		require.NoError(t, err)
		require.True(t, activated)
		task := microsoftTokenRefreshTaskFromState(persisted)
		abnormal, retryErr := repo.RecordRetryableFailure(
			context.Background(), task, "Microsoft mail service is temporarily unavailable.",
		)
		require.NoError(t, retryErr)
		assert.Equal(t, attempt == mailapp.MicrosoftTokenRefreshDefaultMaxAttempts, abnormal)
		persisted = loadMicrosoftTokenRefreshState(t, db, 9260)
		assert.Equal(t, attempt, persisted.Failures)
		if attempt < mailapp.MicrosoftTokenRefreshDefaultMaxAttempts {
			assert.Equal(t, mailapp.MicrosoftTokenRefreshPending, persisted.Status)
		} else {
			assert.Equal(t, mailapp.MicrosoftTokenRefreshAbnormal, persisted.Status)
			require.NotNil(t, persisted.FinishedAt)
		}
	}
}

func TestMicrosoftTokenRefreshApplyRotationAndFenceStaleRevisionMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9270, "disabled", 5)
	createMicrosoftTokenRefreshTestResource(t, db, 9271, "normal", 3)
	repo := newMicrosoftTokenRefreshTestRepo(db)

	command := microsoftTokenRefreshTestCommand(9270, "rotate-token-9270")
	state, _, err := repo.Request(context.Background(), command, microsoftTokenRefreshTestOperationLog(command))
	require.NoError(t, err)
	requireTokenRefreshProcessing(t, repo, state)
	task := microsoftTokenRefreshTaskFromState(*state)
	execution, current, err := repo.LoadExecution(context.Background(), task)
	require.NoError(t, err)
	require.True(t, current)
	require.NotNil(t, execution)
	initialVersion := microsoftTokenRefreshRootVersion(t, db, 9270)

	require.NoError(t, repo.ApplyResult(context.Background(), task, mailapp.MicrosoftTokenRefreshProtocolResult{
		Valid:        true,
		ClientID:     "rotated-client-9270",
		RefreshToken: "rotated-refresh-9270",
		Category:     "success",
	}))

	var rotated struct {
		Status             string `gorm:"column:status"`
		TokenRefreshStatus string `gorm:"column:token_refresh_status"`
		ClientID           string `gorm:"column:client_id"`
		RefreshToken       string `gorm:"column:refresh_token"`
		CredentialRevision uint64 `gorm:"column:credential_revision"`
		TokenRequestID     string `gorm:"column:token_last_request_id"`
	}
	require.NoError(t, db.Raw(`
SELECT status, token_refresh_status, client_id, refresh_token,
       credential_revision, token_last_request_id
FROM microsoft_resources WHERE id = 9270`).Scan(&rotated).Error)
	assert.Equal(t, "disabled", rotated.Status)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshNormal, rotated.TokenRefreshStatus)
	assert.Equal(t, "rotated-client-9270", rotated.ClientID)
	assert.Equal(t, "rotated-refresh-9270", rotated.RefreshToken)
	assert.Equal(t, uint64(6), rotated.CredentialRevision)
	assert.Equal(t, command.RequestID, rotated.TokenRequestID)
	assert.Equal(t, initialVersion+1, microsoftTokenRefreshRootVersion(t, db, 9270))

	staleCommand := microsoftTokenRefreshTestCommand(9271, "stale-token-9271")
	staleState, _, err := repo.Request(context.Background(), staleCommand, microsoftTokenRefreshTestOperationLog(staleCommand))
	require.NoError(t, err)
	requireTokenRefreshProcessing(t, repo, staleState)
	staleTask := microsoftTokenRefreshTaskFromState(*staleState)
	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET refresh_token = 'external-refresh-token', credential_revision = 4
WHERE id = 9271`).Error)
	err = repo.ApplyResult(context.Background(), staleTask, mailapp.MicrosoftTokenRefreshProtocolResult{
		Valid: true, RefreshToken: "stale-worker-refresh-token",
	})
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenRefreshStale)
	stalePersisted := loadMicrosoftTokenRefreshState(t, db, 9271)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshNormal, stalePersisted.Status)
	assert.Greater(t, stalePersisted.Generation, staleTask.Generation)
	var refreshToken string
	require.NoError(t, db.Raw("SELECT refresh_token FROM microsoft_resources WHERE id = 9271").Scan(&refreshToken).Error)
	assert.Equal(t, "external-refresh-token", refreshToken)
}

func TestMicrosoftTokenRefreshAuditFailureRollsBackResourceStateMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftTokenRefreshTestResource(t, db, 9280, "normal", 1)
	repo := newMicrosoftTokenRefreshTestRepo(db)
	repo.operationLogs = failingAdminOperationLogWriter{err: errors.New("forced operation log failure")}
	command := microsoftTokenRefreshTestCommand(9280, "audit-rollback-9280")

	_, _, err := repo.Request(context.Background(), command, microsoftTokenRefreshTestOperationLog(command))
	require.ErrorIs(t, err, mailapp.ErrMicrosoftTokenRefreshUnavailable)
	persisted := loadMicrosoftTokenRefreshState(t, db, 9280)
	assert.Equal(t, mailapp.MicrosoftTokenRefreshNormal, persisted.Status)
	assert.Zero(t, persisted.Generation)
	assert.Empty(t, persisted.IdempotencyKey)
}

func TestMicrosoftTokenRefreshMigrationDropsJobTablesAndIndexesPendingStateMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	var obsoleteTables int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name IN ('microsoft_token_refresh_jobs', 'microsoft_token_refresh_requests')`).Scan(&obsoleteTables).Error)
	assert.Zero(t, obsoleteTables)

	var pendingIndex int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.statistics
WHERE table_schema = DATABASE()
  AND table_name = 'microsoft_resources'
  AND index_name = 'idx_microsoft_token_refresh_pending'`).Scan(&pendingIndex).Error)
	assert.EqualValues(t, 3, pendingIndex, "the pending dispatcher index contains status, requested time, and id")
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

func loadMicrosoftTokenRefreshState(t *testing.T, db *gorm.DB, resourceID uint) mailapp.MicrosoftTokenRefreshState {
	t.Helper()
	var model MicrosoftTokenRefreshStateModel
	require.NoError(t, db.First(&model, resourceID).Error)
	return tokenRefreshStateFromModel(model)
}

func microsoftTokenRefreshTaskFromState(state mailapp.MicrosoftTokenRefreshState) mailapp.MicrosoftTokenRefreshTask {
	return mailapp.MicrosoftTokenRefreshTask{
		ResourceID:                 state.ResourceID,
		Generation:                 state.Generation,
		ExpectedCredentialRevision: state.ExpectedCredentialRevision,
		RequestID:                  state.RequestID,
	}
}

func requireTokenRefreshProcessing(t *testing.T, repo *MicrosoftTokenRefreshRepo, state *mailapp.MicrosoftTokenRefreshState) {
	t.Helper()
	require.NotNil(t, state)
	activated, err := repo.MarkProcessing(context.Background(), state.ResourceID, state.Generation)
	require.NoError(t, err)
	require.True(t, activated)
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
		"objectkey",
	} {
		assert.NotContains(t, lower, forbidden)
	}
}
