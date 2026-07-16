package infra

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type failingAdminOperationLogWriter struct {
	err error
}

type uncertainAdminAliasCreator struct{}

func (uncertainAdminAliasCreator) PrepareMicrosoftAliasBinding(_ context.Context, request mailapp.MicrosoftAliasCreationRequest) (mailapp.MicrosoftAliasBindingPreparationResult, error) {
	return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: request.BindingAddress}, nil
}

func (uncertainAdminAliasCreator) GenerateMicrosoftAliasCandidates(int) ([]string, error) {
	return []string{"uncertain-admin-task@outlook.com"}, nil
}

func (uncertainAdminAliasCreator) CreateMicrosoftAliases(
	_ context.Context,
	request mailapp.MicrosoftAliasCreationRequest,
) (mailapp.MicrosoftAliasCreationResult, error) {
	return mailapp.MicrosoftAliasCreationResult{
		Attempted:   append([]string(nil), request.Candidates...),
		Uncertain:   append([]string(nil), request.Candidates...),
		Category:    "request",
		SafeMessage: "Microsoft alias result is uncertain.",
	}, nil
}

func (w failingAdminOperationLogWriter) CreateInTx(context.Context, *gorm.DB, *governancedomain.OperationLog) error {
	return w.err
}

func TestMicrosoftAliasAdminScheduleReadsUsageAndCommandAdvancesWithoutBypassingQuotaMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9220, "normal")
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 9220,
		Status:     "pending",
		NextRunAt:  future,
		CreatedAt:  now.Add(-24 * time.Hour),
		UpdatedAt:  now.Add(-24 * time.Hour),
	}).Error)
	require.NoError(t, db.Create(&MicrosoftExplicitAliasModel{
		ResourceID:  9220,
		OwnerUserID: 9220,
		Email:       "existing9220@outlook.com",
		Status:      "normal",
		CreatedAt:   now.Add(-2 * time.Hour),
		UpdatedAt:   now.Add(-2 * time.Hour),
	}).Error)
	require.NoError(t, db.Create(&MicrosoftAliasAttemptModel{
		ResourceID:     9220,
		Candidate:      "uncertain9220@outlook.com",
		Status:         mailapp.MicrosoftAliasAttemptUncertain,
		QuotaAt:        now.Add(-time.Hour),
		WasAttempted:   true,
		UncertainSince: pointerAliasAdminTime(now.Add(-time.Hour)),
		CreatedAt:      now.Add(-time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
	}).Error)

	store := NewMicrosoftAliasStore(db)
	yearStart, yearEnd, weekStart, weekEnd := aliasAdminQuotaWindows(now)
	schedule, err := store.GetAdminSchedule(context.Background(), 9220, yearStart, yearEnd, weekStart, weekEnd)
	require.NoError(t, err)
	assert.Equal(t, 2, schedule.WeekCreated)
	assert.Equal(t, 2, schedule.YearCreated)
	assert.Equal(t, future, *schedule.NextRunAt)

	command := aliasExpediteTestCommand(9220, "alias-schedule-advance-9220")
	result, receiptReused, err := store.AcceptAdminAliasExpedite(
		context.Background(),
		command,
		now,
		aliasExpediteTestOperationLog(command),
	)
	require.NoError(t, err)
	assert.False(t, receiptReused)
	assert.False(t, result.Reused)
	assert.Equal(t, "queued", result.Status)
	assert.True(t, result.WakeDispatcher)
	assert.Equal(t, now, *result.NextRunAt)

	var persisted MicrosoftAliasScheduleModel
	require.NoError(t, db.Where("resource_id = ?", 9220).First(&persisted).Error)
	assert.Equal(t, "pending", persisted.Status)
	assert.True(t, persisted.NextRunAt.Equal(now))
	var attemptCount int64
	require.NoError(t, db.Model(&MicrosoftAliasAttemptModel{}).Where("resource_id = ?", 9220).Count(&attemptCount).Error)
	assert.EqualValues(t, 1, attemptCount)
	var aliasCount int64
	require.NoError(t, db.Model(&MicrosoftExplicitAliasModel{}).Where("resource_id = ?", 9220).Count(&aliasCount).Error)
	assert.EqualValues(t, 1, aliasCount)

	reuseCommand := aliasExpediteTestCommand(9220, "alias-schedule-reuse-9220")
	reused, receiptReused, err := store.AcceptAdminAliasExpedite(
		context.Background(),
		reuseCommand,
		now.Add(time.Second),
		aliasExpediteTestOperationLog(reuseCommand),
	)
	require.NoError(t, err)
	assert.False(t, receiptReused)
	assert.True(t, reused.Reused)
	assert.True(t, reused.WakeDispatcher)
}

func TestMicrosoftAliasAdminUncertainWorkerResultStaysUncertainInTaskViewsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9234, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 9234)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 9234,
		Status:     "pending",
		NextRunAt:  now.Add(48 * time.Hour),
		CreatedAt:  now.Add(-time.Hour),
		UpdatedAt:  now.Add(-time.Hour),
	}).Error)

	store := NewMicrosoftAliasStore(db)
	service := mailapp.NewMicrosoftAliasService(store, nil, uncertainAdminAliasCreator{})
	command := aliasExpediteTestCommand(9234, "alias-uncertain-task-view-9234")
	accepted, err := service.AcceptAdminExpedite(context.Background(), command)
	require.NoError(t, err)
	require.NotNil(t, accepted)
	require.Equal(t, "alias_schedule:9234", accepted.Task.TaskID())
	require.Equal(t, governanceapp.AdminTaskStatusQueued, accepted.Task.Status)

	dispatchAt := time.Now().UTC().Add(time.Second)
	tasks, err := store.FindDispatchable(
		context.Background(),
		1,
		dispatchAt,
		dispatchAt.Add(-4*time.Hour),
		dispatchAt.Add(-30*time.Minute),
	)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NoError(t, service.Process(context.Background(), tasks[0]))

	var schedule MicrosoftAliasScheduleModel
	require.NoError(t, db.Where("resource_id = ?", 9234).Take(&schedule).Error)
	require.Equal(t, "pending", schedule.Status)
	require.True(t, schedule.NextRunAt.After(now))
	var attempt MicrosoftAliasAttemptModel
	require.NoError(t, db.Where("resource_id = ?", 9234).Take(&attempt).Error)
	require.Equal(t, mailapp.MicrosoftAliasAttemptUncertain, attempt.Status)

	tasksRepo := governanceinfra.NewAdminTaskViewRepo(db)
	views, _, _, err := tasksRepo.ListForMicrosoftResource(context.Background(), governanceapp.AdminTaskListFilter{
		BizType: governanceapp.AdminTaskBizMicrosoftResource,
		BizID:   9234,
		Limit:   20,
	})
	require.NoError(t, err)
	var scheduleView *governanceapp.AdminTaskView
	for i := range views {
		if views[i].TaskID() == "alias_schedule:9234" {
			scheduleView = &views[i]
			break
		}
	}
	require.NotNil(t, scheduleView)
	require.Equal(t, governanceapp.AdminTaskStatusUncertain, scheduleView.Status)
	require.Nil(t, scheduleView.FinishedAt)

	replayedView, err := tasksRepo.FindByRef(context.Background(), governanceapp.AdminTaskRef{
		Source: governanceapp.AdminTaskSourceAliasSchedule,
		ID:     9234,
	})
	require.NoError(t, err)
	require.Equal(t, scheduleView.Status, replayedView.Status)
	require.Equal(t, governanceapp.AdminTaskStatusUncertain, replayedView.Status)
}

func TestMicrosoftAliasAdminCommandReusesActiveFencingAndHonorsCallerTransactionMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9221, "normal")
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	claimToken := "0123456789abcdef0123456789abcdef"
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 9221,
		Status:     "queued",
		NextRunAt:  future,
		ClaimToken: claimToken,
		CreatedAt:  now.Add(-time.Hour),
		UpdatedAt:  now.Add(-time.Hour),
	}).Error)
	store := NewMicrosoftAliasStore(db)

	command := aliasExpediteTestCommand(9221, "alias-active-reuse-9221")
	result, receiptReused, err := store.AcceptAdminAliasExpedite(
		context.Background(),
		command,
		now,
		aliasExpediteTestOperationLog(command),
	)
	require.NoError(t, err)
	assert.False(t, receiptReused)
	assert.True(t, result.Reused)
	assert.Equal(t, "queued", result.Status)
	assert.False(t, result.WakeDispatcher)
	persisted := loadAliasAdminSchedule(t, db, 9221)
	assert.Equal(t, claimToken, persisted.ClaimToken)
	assert.True(t, persisted.NextRunAt.Equal(future))

	require.NoError(t, db.Model(&MicrosoftAliasScheduleModel{}).
		Where("resource_id = ?", 9221).
		Updates(map[string]any{"status": "pending", "claim_token": "", "next_run_at": future}).Error)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	transactionCommand := aliasExpediteTestCommand(9221, "alias-transaction-rollback-9221")
	_, receiptReused, err = store.AcceptAdminAliasExpedite(
		platform.WithGormTx(context.Background(), tx),
		transactionCommand,
		now,
		aliasExpediteTestOperationLog(transactionCommand),
	)
	require.NoError(t, err)
	assert.False(t, receiptReused)
	require.NoError(t, tx.Rollback().Error)
	persisted = loadAliasAdminSchedule(t, db, 9221)
	assert.True(t, persisted.NextRunAt.Equal(future))
	var receiptCount int64
	require.NoError(t, db.Model(&MicrosoftAliasExpediteRequestModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", 9221, transactionCommand.IdempotencyKey).
		Count(&receiptCount).Error)
	assert.Zero(t, receiptCount)
}

func TestMicrosoftAliasAdminCommandOnlyWakesPausedScheduleAfterResourceChangeMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9222, "normal")
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	signature := currentAliasAdminResourceSignature(t, db, 9222)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID:               9222,
		Status:                   "paused",
		NextRunAt:                now.Add(24 * time.Hour),
		BlockedResourceSignature: signature,
		LastSafeError:            "Microsoft account password is incorrect.",
		CreatedAt:                now.Add(-time.Hour),
		UpdatedAt:                now.Add(-time.Hour),
	}).Error)
	store := NewMicrosoftAliasStore(db)

	blockedCommand := aliasExpediteTestCommand(9222, "alias-paused-blocked-9222")
	_, _, err := store.AcceptAdminAliasExpedite(
		context.Background(),
		blockedCommand,
		now,
		aliasExpediteTestOperationLog(blockedCommand),
	)
	require.ErrorIs(t, err, mailapp.ErrMicrosoftAliasSchedulePaused)
	persisted := loadAliasAdminSchedule(t, db, 9222)
	assert.Equal(t, "paused", persisted.Status)

	require.NoError(t, db.Exec("UPDATE microsoft_resources SET password = 'changed-secret' WHERE id = ?", 9222).Error)
	wakeCommand := aliasExpediteTestCommand(9222, "alias-paused-wake-9222")
	result, receiptReused, err := store.AcceptAdminAliasExpedite(
		context.Background(),
		wakeCommand,
		now.Add(time.Minute),
		aliasExpediteTestOperationLog(wakeCommand),
	)
	require.NoError(t, err)
	assert.False(t, receiptReused)
	assert.False(t, result.Reused)
	assert.True(t, result.WakeDispatcher)
	persisted = loadAliasAdminSchedule(t, db, 9222)
	assert.Equal(t, "pending", persisted.Status)
	assert.Empty(t, persisted.BlockedResourceSignature)
}

func TestMicrosoftAliasAdminCommandRejectsNonNormalResourceMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9223, "disabled")
	store := NewMicrosoftAliasStore(db)

	command := aliasExpediteTestCommand(9223, "alias-disabled-resource-9223")
	_, _, err := store.AcceptAdminAliasExpedite(
		context.Background(),
		command,
		time.Now().UTC(),
		aliasExpediteTestOperationLog(command),
	)
	require.ErrorIs(t, err, mailapp.ErrMicrosoftAliasResourceConflict)
	var scheduleCount int64
	require.NoError(t, db.Model(&MicrosoftAliasScheduleModel{}).Where("resource_id = ?", 9223).Count(&scheduleCount).Error)
	assert.Zero(t, scheduleCount)
}

func TestMicrosoftAliasAdminCommandRequiresExistingCanonicalScheduleMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9224, "normal")
	store := NewMicrosoftAliasStore(db)

	command := aliasExpediteTestCommand(9224, "alias-missing-schedule-9224")
	_, _, err := store.AcceptAdminAliasExpedite(
		context.Background(),
		command,
		time.Now().UTC(),
		aliasExpediteTestOperationLog(command),
	)
	require.ErrorIs(t, err, mailapp.ErrMicrosoftAliasScheduleNotFound)
	var scheduleCount int64
	require.NoError(t, db.Model(&MicrosoftAliasScheduleModel{}).Where("resource_id = ?", 9224).Count(&scheduleCount).Error)
	assert.Zero(t, scheduleCount)
}

func TestMicrosoftAliasAdminCommandIsIdempotentAuditedAndDoesNotRepeatExpediteMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9230, "normal")
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	replayFuture := now.Add(96 * time.Hour)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 9230,
		Status:     "pending",
		NextRunAt:  future,
		CreatedAt:  now.Add(-time.Hour),
		UpdatedAt:  now.Add(-time.Hour),
	}).Error)
	store := NewMicrosoftAliasStore(db)
	command := mailapp.MicrosoftAliasExpediteCommand{
		ResourceID:     9230,
		OperatorUserID: 9230,
		IdempotencyKey: "alias-idempotency-9230",
		RequestID:      "request-9230",
		Path:           "/v1/admin/resources/:resourceId/aliases",
	}
	operationLog := aliasExpediteTestOperationLog(command)

	result, receiptReused, err := store.AcceptAdminAliasExpedite(context.Background(), command, now, operationLog)
	require.NoError(t, err)
	assert.False(t, receiptReused)
	assert.False(t, result.Reused)
	assert.True(t, result.WakeDispatcher)
	assert.True(t, loadAliasAdminSchedule(t, db, 9230).NextRunAt.Equal(now))

	// Simulate the canonical worker completing and scheduling the next normal
	// replenishment window. Replaying the administrator key must not advance it
	// again or create a second task fact.
	require.NoError(t, db.Model(&MicrosoftAliasScheduleModel{}).
		Where("resource_id = ?", 9230).
		Updates(map[string]any{
			"status":      "pending",
			"next_run_at": replayFuture,
			"updated_at":  now.Add(time.Hour),
		}).Error)
	replayed, receiptReused, err := store.AcceptAdminAliasExpedite(
		context.Background(),
		command,
		now.Add(2*time.Hour),
		aliasExpediteTestOperationLog(command),
	)
	require.NoError(t, err)
	assert.True(t, receiptReused)
	assert.True(t, replayed.Reused)
	assert.False(t, replayed.WakeDispatcher)
	assert.True(t, loadAliasAdminSchedule(t, db, 9230).NextRunAt.Equal(replayFuture))

	var receiptCount int64
	require.NoError(t, db.Model(&MicrosoftAliasExpediteRequestModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", 9230, command.IdempotencyKey).
		Count(&receiptCount).Error)
	assert.EqualValues(t, 1, receiptCount)
	var summaries []string
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ? AND resource_id = ?", "mailtransport.microsoft_alias.expedite", "9230").
		Order("id ASC").
		Pluck("safe_summary", &summaries).Error)
	require.Len(t, summaries, 2)
	for _, summary := range summaries {
		assert.Contains(t, summary, "alias_schedule:9230")
		assert.NotContains(t, strings.ToLower(summary), "secret")
		assert.NotContains(t, strings.ToLower(summary), "candidate")
		assert.NotContains(t, strings.ToLower(summary), "claim")
		assert.NotContains(t, strings.ToLower(summary), "dispatch")
	}
}

func TestMicrosoftAliasAdminCommandSerializesConcurrentIdempotentRequestsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9233, "normal")
	now := time.Date(2026, time.July, 12, 13, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 9233,
		Status:     "pending",
		NextRunAt:  future,
		CreatedAt:  now.Add(-time.Hour),
		UpdatedAt:  now.Add(-time.Hour),
	}).Error)
	store := NewMicrosoftAliasStore(db)

	const workerCount = 10
	start := make(chan struct{})
	results := make([]*mailapp.MicrosoftAliasExpediteResult, workerCount)
	receiptReusedByWorker := make([]bool, workerCount)
	errorsByWorker := make([]error, workerCount)
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			command := mailapp.MicrosoftAliasExpediteCommand{
				ResourceID:     9233,
				OperatorUserID: 9233,
				IdempotencyKey: "alias-concurrent-shared-key",
				RequestID:      "request-alias-concurrent-" + strconv.Itoa(worker),
				Path:           "/v1/admin/resources/:resourceId/aliases",
			}
			results[worker], receiptReusedByWorker[worker], errorsByWorker[worker] = store.AcceptAdminAliasExpedite(
				context.Background(),
				command,
				now,
				aliasExpediteTestOperationLog(command),
			)
		}(index)
	}
	close(start)
	workers.Wait()

	createdReceiptResponses := 0
	for index, err := range errorsByWorker {
		require.NoError(t, err, "worker %d", index)
		if !receiptReusedByWorker[index] {
			createdReceiptResponses++
		}
		require.NotNil(t, results[index])
	}
	require.Equal(t, 1, createdReceiptResponses)

	persisted := loadAliasAdminSchedule(t, db, 9233)
	require.True(t, persisted.NextRunAt.Equal(now))
	var receiptCount, logCount int64
	require.NoError(t, db.Model(&MicrosoftAliasExpediteRequestModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", 9233, "alias-concurrent-shared-key").
		Count(&receiptCount).Error)
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ? AND resource_id = ?", "mailtransport.microsoft_alias.expedite", "9233").
		Count(&logCount).Error)
	require.EqualValues(t, 1, receiptCount)
	require.EqualValues(t, workerCount, logCount)
}

func TestMicrosoftAliasAdminCommandRejectsFingerprintConflictAndRollsBackAuditFailureMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9231, "normal")
	createMicrosoftAliasTestResource(t, db, 9232, "normal")
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	firstFuture := now.Add(24 * time.Hour)
	secondFuture := now.Add(48 * time.Hour)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{ResourceID: 9231, Status: "pending", NextRunAt: firstFuture}).Error)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{ResourceID: 9232, Status: "pending", NextRunAt: secondFuture}).Error)
	store := NewMicrosoftAliasStore(db)
	first := mailapp.MicrosoftAliasExpediteCommand{
		ResourceID:     9231,
		OperatorUserID: 9231,
		IdempotencyKey: "shared-alias-key",
		RequestID:      "request-first",
		Path:           "/v1/admin/resources/:resourceId/aliases",
	}
	_, _, err := store.AcceptAdminAliasExpedite(context.Background(), first, now, aliasExpediteTestOperationLog(first))
	require.NoError(t, err)

	conflict := first
	conflict.ResourceID = 9232
	conflict.RequestID = "request-conflict"
	_, _, err = store.AcceptAdminAliasExpedite(context.Background(), conflict, now.Add(time.Minute), aliasExpediteTestOperationLog(conflict))
	require.ErrorIs(t, err, mailapp.ErrMicrosoftAliasIdempotencyConflict)
	assert.True(t, loadAliasAdminSchedule(t, db, 9232).NextRunAt.Equal(secondFuture))

	rollbackCommand := mailapp.MicrosoftAliasExpediteCommand{
		ResourceID:     9232,
		OperatorUserID: 9232,
		IdempotencyKey: "audit-rollback-key",
		RequestID:      "request-audit-failure",
		Path:           "/v1/admin/resources/:resourceId/aliases",
	}
	store.operationLogs = failingAdminOperationLogWriter{err: errors.New("forced operation log failure")}
	_, _, err = store.AcceptAdminAliasExpedite(
		context.Background(),
		rollbackCommand,
		now.Add(2*time.Minute),
		aliasExpediteTestOperationLog(rollbackCommand),
	)
	require.ErrorIs(t, err, mailapp.ErrMicrosoftAliasAdminUnavailable)
	assert.True(t, loadAliasAdminSchedule(t, db, 9232).NextRunAt.Equal(secondFuture))
	var rollbackReceiptCount int64
	require.NoError(t, db.Model(&MicrosoftAliasExpediteRequestModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", rollbackCommand.OperatorUserID, rollbackCommand.IdempotencyKey).
		Count(&rollbackReceiptCount).Error)
	assert.Zero(t, rollbackReceiptCount)
}

func aliasExpediteTestOperationLog(command mailapp.MicrosoftAliasExpediteCommand) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: command.OperatorUserID,
		OperationType:  "mailtransport.microsoft_alias.expedite",
		ResourceType:   "microsoft_resource",
		ResourceID:     strconv.FormatUint(uint64(command.ResourceID), 10),
		Path:           command.Path,
		Result:         "success",
		SafeSummary:    "Microsoft explicit-alias schedule expedite accepted.",
		RequestID:      command.RequestID,
	}
}

func aliasExpediteTestCommand(resourceID uint, idempotencyKey string) mailapp.MicrosoftAliasExpediteCommand {
	return mailapp.MicrosoftAliasExpediteCommand{
		ResourceID:     resourceID,
		OperatorUserID: resourceID,
		IdempotencyKey: idempotencyKey,
		RequestID:      "request-" + idempotencyKey,
		Path:           "/v1/admin/resources/:resourceId/aliases",
	}
}

func aliasAdminQuotaWindows(now time.Time) (time.Time, time.Time, time.Time, time.Time) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := now.In(location)
	yearStart := time.Date(local.Year(), time.January, 1, 0, 0, 0, 0, location)
	daysSinceMonday := (int(local.Weekday()) + 6) % 7
	weekStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).AddDate(0, 0, -daysSinceMonday)
	return yearStart.UTC(), yearStart.AddDate(1, 0, 0).UTC(), weekStart.UTC(), weekStart.AddDate(0, 0, 7).UTC()
}

func pointerAliasAdminTime(value time.Time) *time.Time {
	return &value
}

func loadAliasAdminSchedule(t *testing.T, db *gorm.DB, resourceID uint) MicrosoftAliasScheduleModel {
	t.Helper()
	var schedule MicrosoftAliasScheduleModel
	require.NoError(t, db.Where("resource_id = ?", resourceID).First(&schedule).Error)
	return schedule
}

func currentAliasAdminResourceSignature(t *testing.T, db *gorm.DB, resourceID uint) string {
	t.Helper()
	var row struct {
		Signature string `gorm:"column:resource_signature"`
	}
	require.NoError(t, db.Raw(`
SELECT SHA2(CONCAT_WS(
    CHAR(0),
    mr.status,
    mr.email_address,
    mr.password,
    mr.client_id,
    mr.refresh_token,
	COALESCE(binding.account_email, ''),
	COALESCE(binding.binding_address, ''),
	COALESCE(binding.status, '')
), 256) AS resource_signature
FROM microsoft_resources AS mr
LEFT JOIN microsoft_binding_mailboxes AS binding
  ON binding.resource_id = mr.id
 AND binding.status <> 'expired'
WHERE mr.id = ?`, resourceID).Scan(&row).Error)
	require.NotEmpty(t, row.Signature)
	return row.Signature
}
