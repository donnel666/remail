package infra

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResourceFetchCreateReuseAndOperationLogMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	const passwordCanary = "password-resource-fetch-log-canary"
	const clientCanary = "client-resource-fetch-log-canary"
	const refreshCanary = "refresh-resource-fetch-log-canary"
	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 100).Updates(map[string]any{
		"password":      passwordCanary,
		"client_id":     clientCanary,
		"refresh_token": refreshCanary,
	}).Error)
	repo := newResourceFetchTestRepo(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	first := resourceFetchTestJob(now, "req-resource-fetch-first")
	reused, err := repo.CreateOrReuseResourceFetch(ctx, &first, resourceFetchTestOperationLog(first.RequestID))
	require.NoError(t, err)
	require.False(t, reused)
	require.NotZero(t, first.ID)
	require.Equal(t, uint64(1), first.ExpectedCredentialRevision)
	require.Equal(t, "main@example.com", first.Recipient)
	require.Equal(t, domain.ResourceFetchJobQueued, first.Status)

	second := resourceFetchTestJob(now.Add(time.Second), "req-resource-fetch-second")
	reused, err = repo.CreateOrReuseResourceFetch(ctx, &second, resourceFetchTestOperationLog(second.RequestID))
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, first.RequestID, second.RequestID, "reused task keeps its original trace metadata")

	var jobCount int64
	require.NoError(t, db.Model(&ResourceFetchJobModel{}).Count(&jobCount).Error)
	require.Equal(t, int64(1), jobCount)
	var receiptCount int64
	require.NoError(t, db.Model(&ResourceFetchRequestModel{}).Count(&receiptCount).Error)
	require.Equal(t, int64(2), receiptCount, "every accepted key, including active reuse, keeps a durable receipt")
	var logs []governanceinfra.OperationLogModel
	require.NoError(t, db.Where("operation_type = ?", "mailmatch.admin_resource.fetch").Order("id ASC").Find(&logs).Error)
	require.Len(t, logs, 2)
	require.Equal(t, fmt.Sprintf("Microsoft resource mail fetch accepted; task=fetch:%d; reused=false.", first.ID), logs[0].SafeSummary)
	require.Equal(t, fmt.Sprintf("Microsoft resource mail fetch accepted; task=fetch:%d; reused=true.", first.ID), logs[1].SafeSummary)
	for _, log := range logs {
		serialized := strings.ToLower(fmt.Sprintf("%+v", log))
		require.NotContains(t, serialized, strings.ToLower(passwordCanary))
		require.NotContains(t, serialized, strings.ToLower(clientCanary))
		require.NotContains(t, serialized, strings.ToLower(refreshCanary))
		require.NotContains(t, serialized, "main@example.com")
	}

	require.NoError(t, db.Model(&ResourceFetchJobModel{}).
		Where("id = ?", first.ID).
		Updates(map[string]any{"status": string(domain.ResourceFetchJobSucceeded), "finished_at": now.Add(time.Minute)}).Error)
	replay := resourceFetchTestJob(now.Add(2*time.Minute), "req-resource-fetch-replay")
	replay.IdempotencyKey = first.IdempotencyKey
	reused, err = repo.CreateOrReuseResourceFetch(ctx, &replay, resourceFetchTestOperationLog(replay.RequestID))
	require.NoError(t, err)
	require.False(t, reused, "idempotency replay preserves the original accepted response")
	require.Equal(t, first.ID, replay.ID)
	require.Equal(t, domain.ResourceFetchJobSucceeded, replay.Status, "terminal replay returns the original task")

	conflict := resourceFetchTestJob(now.Add(3*time.Minute), "req-resource-fetch-conflict")
	conflict.ResourceID = 999
	conflict.IdempotencyKey = first.IdempotencyKey
	_, err = repo.CreateOrReuseResourceFetch(ctx, &conflict, resourceFetchTestOperationLog(conflict.RequestID))
	require.ErrorIs(t, err, domain.ErrResourceFetchIdempotencyConflict)
	require.Equal(t, int64(1), tableCount(t, db, "mailmatch_resource_fetch_jobs"))
	require.Equal(t, int64(3), tableCount(t, db, "operation_logs"), "conflicting replay cannot write a success audit")
}

func TestResourceFetchCreateSingleFlightUnderConcurrentRequestsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	repo := newResourceFetchTestRepo(db)
	now := time.Now().UTC().Truncate(time.Millisecond)

	const workerCount = 10
	start := make(chan struct{})
	jobs := make([]domain.ResourceFetchJob, workerCount)
	reusedByWorker := make([]bool, workerCount)
	errorsByWorker := make([]error, workerCount)
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			requestID := fmt.Sprintf("req-resource-fetch-concurrent-%03d", worker)
			job := resourceFetchTestJob(now, requestID)
			reusedByWorker[worker], errorsByWorker[worker] = repo.CreateOrReuseResourceFetch(
				context.Background(),
				&job,
				resourceFetchTestOperationLog(requestID),
			)
			jobs[worker] = job
		}(index)
	}
	close(start)
	workers.Wait()

	createdCount := 0
	jobIDs := make(map[uint]struct{}, workerCount)
	for index, err := range errorsByWorker {
		require.NoError(t, err, "worker %d", index)
		if !reusedByWorker[index] {
			createdCount++
		}
		require.NotZero(t, jobs[index].ID)
		jobIDs[jobs[index].ID] = struct{}{}
	}
	require.Equal(t, 1, createdCount)
	require.Len(t, jobIDs, 1)
	require.EqualValues(t, 1, tableCount(t, db, "mailmatch_resource_fetch_jobs"))
	require.EqualValues(t, workerCount, tableCount(t, db, "mailmatch_resource_fetch_requests"))
	require.EqualValues(t, workerCount, tableCount(t, db, "operation_logs"))
}

func TestResourceFetchSubmissionStateBoundariesMySQL(t *testing.T) {
	t.Run("disabled remains fetchable for diagnostics", func(t *testing.T) {
		db := newMailmatchMySQLTestDB(t)
		seedMailmatchFetchResource(t, db)
		require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 100).Update("status", "disabled").Error)
		repo := newResourceFetchTestRepo(db)
		job := resourceFetchTestJob(time.Now().UTC(), "req-disabled-fetch")
		reused, err := repo.CreateOrReuseResourceFetch(context.Background(), &job, resourceFetchTestOperationLog(job.RequestID))
		require.NoError(t, err)
		require.False(t, reused)
		require.NotZero(t, job.ID)

		var status string
		require.NoError(t, db.Table("microsoft_resources").Select("status").Where("id = ?", 100).Scan(&status).Error)
		require.Equal(t, "disabled", status, "diagnostic fetch must not enable the resource")
	})

	t.Run("deleted is rejected without task or success log", func(t *testing.T) {
		db := newMailmatchMySQLTestDB(t)
		seedMailmatchFetchResource(t, db)
		require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 100).Update("status", "deleted").Error)
		repo := newResourceFetchTestRepo(db)
		job := resourceFetchTestJob(time.Now().UTC(), "req-deleted-fetch")
		_, err := repo.CreateOrReuseResourceFetch(context.Background(), &job, resourceFetchTestOperationLog(job.RequestID))
		require.ErrorIs(t, err, domain.ErrResourceFetchDeleted)
		require.Equal(t, int64(0), tableCount(t, db, "mailmatch_resource_fetch_jobs"))
		require.Equal(t, int64(0), tableCount(t, db, "operation_logs"))
	})

	t.Run("incomplete credentials are rejected", func(t *testing.T) {
		db := newMailmatchMySQLTestDB(t)
		seedMailmatchFetchResource(t, db)
		require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 100).Update("refresh_token", "").Error)
		repo := newResourceFetchTestRepo(db)
		job := resourceFetchTestJob(time.Now().UTC(), "req-missing-fetch-token")
		_, err := repo.CreateOrReuseResourceFetch(context.Background(), &job, resourceFetchTestOperationLog(job.RequestID))
		require.ErrorIs(t, err, domain.ErrResourceFetchCredentialsMissing)
		require.Equal(t, int64(0), tableCount(t, db, "mailmatch_resource_fetch_jobs"))
		require.Equal(t, int64(0), tableCount(t, db, "operation_logs"))
	})
}

func TestResourceFetchDispatchClaimAndCredentialFenceMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	repo := newResourceFetchTestRepo(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	job := resourceFetchTestJob(now, "req-resource-fetch-fence")
	_, err := repo.CreateOrReuseResourceFetch(ctx, &job, resourceFetchTestOperationLog(job.RequestID))
	require.NoError(t, err)

	dispatchable, err := repo.ClaimDispatchableResourceFetches(ctx, 10, now.Add(-20*time.Minute), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	require.NotEmpty(t, dispatchable[0].DispatchToken)
	dispatchToken := dispatchable[0].DispatchToken

	claimToken, claimed, err := repo.MarkResourceFetchRunning(ctx, job.ID, dispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEmpty(t, claimToken)
	_, claimed, err = repo.MarkResourceFetchRunning(ctx, job.ID, dispatchToken)
	require.NoError(t, err)
	require.False(t, claimed, "a consumed dispatch generation cannot be claimed twice")

	scope, err := repo.LoadResourceFetchScope(ctx, job.ResourceID, job.ExpectedCredentialRevision)
	require.NoError(t, err)
	require.Equal(t, "client", scope.ClientID)
	require.Equal(t, "rt", scope.RefreshToken)

	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 100).Updates(map[string]any{
		"refresh_token":       "replacement-canary-rt",
		"credential_revision": gorm.Expr("credential_revision + 1"),
	}).Error)
	err = repo.AssertResourceFetchFence(ctx, job.ID, claimToken, job.ResourceID, job.ExpectedCredentialRevision)
	require.ErrorIs(t, err, domain.ErrResourceFetchCredentialChanged)
	require.NoError(t, repo.MarkResourceFetchCanceled(
		ctx,
		job.ID,
		claimToken,
		"Resource changed while mail fetch was running.",
		now.Add(time.Minute),
		resourceFetchTestSystemLog(job, "resource_fetch_canceled", "credential_changed"),
	))
	stored, err := repo.FindResourceFetchJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobCanceled, stored.Status)
	require.Empty(t, stored.ClaimToken)

	var logs []governanceinfra.SystemLogModel
	require.NoError(t, db.Where("event_type = ?", "resource_fetch_canceled").Find(&logs).Error)
	require.Len(t, logs, 1)
	require.NotContains(t, strings.ToLower(fmt.Sprintf("%+v", logs[0])), "replacement-canary-rt")
}

func TestResourceFetchRotatedTokenUsesRevisionFenceAndAdvancesRootVersionMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	repo := newResourceFetchTestRepo(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	job := resourceFetchTestJob(now, "req-resource-fetch-rotate")
	_, err := repo.CreateOrReuseResourceFetch(ctx, &job, resourceFetchTestOperationLog(job.RequestID))
	require.NoError(t, err)
	dispatchable, err := repo.ClaimDispatchableResourceFetches(ctx, 1, now.Add(-20*time.Minute), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	claimToken, claimed, err := repo.MarkResourceFetchRunning(ctx, job.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)

	const rotatedToken = "rotated-resource-fetch-canary-token"
	require.NoError(t, repo.CompleteResourceFetch(
		ctx,
		job.ID,
		claimToken,
		job.ResourceID,
		job.ExpectedCredentialRevision,
		rotatedToken,
		2,
		2,
		1,
		now.Add(time.Minute),
		resourceFetchTestSystemLog(job, "resource_fetch_succeeded", ""),
	))
	var credential struct {
		RefreshToken       string `gorm:"column:refresh_token"`
		CredentialRevision uint64 `gorm:"column:credential_revision"`
	}
	require.NoError(t, db.Table("microsoft_resources").
		Select("refresh_token, credential_revision").
		Where("id = ?", job.ResourceID).
		Take(&credential).Error)
	require.Equal(t, rotatedToken, credential.RefreshToken)
	require.Equal(t, uint64(2), credential.CredentialRevision)
	var version uint64
	require.NoError(t, db.Table("email_resources").Select("version").Where("id = ?", job.ResourceID).Scan(&version).Error)
	require.Equal(t, uint64(2), version)

	stored, err := repo.FindResourceFetchJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobSucceeded, stored.Status)
	require.Equal(t, job.ExpectedCredentialRevision, stored.ExpectedCredentialRevision, "submission revision remains immutable")
	require.Equal(t, 2, stored.FetchedCount)
	require.Equal(t, 2, stored.StoredCount)
	require.Equal(t, 1, stored.MatchedCount)

	var operationLogs []governanceinfra.OperationLogModel
	require.NoError(t, db.Find(&operationLogs).Error)
	var systemLogs []governanceinfra.SystemLogModel
	require.NoError(t, db.Find(&systemLogs).Error)
	serialized := strings.ToLower(fmt.Sprintf("%+v %+v", operationLogs, systemLogs))
	require.NotContains(t, serialized, strings.ToLower(rotatedToken))
	require.NotContains(t, serialized, "main@example.com")
}

func TestResourceFetchCompletionRollsBackTokenRotationWhenSystemLogFailsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	repo := newResourceFetchTestRepo(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	job := resourceFetchTestJob(now, "req-resource-fetch-completion-rollback")
	_, err := repo.CreateOrReuseResourceFetch(ctx, &job, resourceFetchTestOperationLog(job.RequestID))
	require.NoError(t, err)
	dispatchable, err := repo.ClaimDispatchableResourceFetches(ctx, 1, now.Add(-20*time.Minute), now.Add(-time.Hour))
	require.NoError(t, err)
	claimToken, claimed, err := repo.MarkResourceFetchRunning(ctx, job.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)

	failingLog := resourceFetchTestSystemLog(job, "resource_fetch_succeeded", "")
	failingLog.Detail = strings.Repeat("x", 1001)
	err = repo.CompleteResourceFetch(
		ctx,
		job.ID,
		claimToken,
		job.ResourceID,
		job.ExpectedCredentialRevision,
		"rotation-must-roll-back-canary",
		1,
		1,
		0,
		now.Add(time.Minute),
		failingLog,
	)
	require.Error(t, err)
	var credential struct {
		RefreshToken       string `gorm:"column:refresh_token"`
		CredentialRevision uint64 `gorm:"column:credential_revision"`
	}
	require.NoError(t, db.Table("microsoft_resources").
		Select("refresh_token, credential_revision").
		Where("id = ?", job.ResourceID).
		Take(&credential).Error)
	require.Equal(t, "rt", credential.RefreshToken)
	require.Equal(t, uint64(1), credential.CredentialRevision)
	var version uint64
	require.NoError(t, db.Table("email_resources").Select("version").Where("id = ?", job.ResourceID).Scan(&version).Error)
	require.Equal(t, uint64(1), version)
	stored, err := repo.FindResourceFetchJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobRunning, stored.Status)
	require.Equal(t, claimToken, stored.ClaimToken)
}

func TestResourceFetchRetryIsDurableAndFencedMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	repo := newResourceFetchTestRepo(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	job := resourceFetchTestJob(now, "req-resource-fetch-retry")
	_, err := repo.CreateOrReuseResourceFetch(ctx, &job, resourceFetchTestOperationLog(job.RequestID))
	require.NoError(t, err)

	dispatchable, err := repo.ClaimDispatchableResourceFetches(ctx, 1, now.Add(-20*time.Minute), now.Add(-time.Hour))
	require.NoError(t, err)
	claimToken, claimed, err := repo.MarkResourceFetchRunning(ctx, job.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)
	retryScheduled, err := repo.MarkResourceFetchFailure(
		ctx,
		job.ID,
		claimToken,
		"Microsoft mail service is temporarily unavailable.",
		true,
		now.Add(time.Second),
		resourceFetchTestSystemLog(job, "resource_fetch_failed", "request"),
	)
	require.NoError(t, err)
	require.True(t, retryScheduled)
	stored, err := repo.FindResourceFetchJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobQueued, stored.Status)
	require.Equal(t, 1, stored.Attempts)
	require.Empty(t, stored.ClaimToken)

	dispatchable, err = repo.ClaimDispatchableResourceFetches(ctx, 1, now.Add(-20*time.Minute), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	secondClaim, claimed, err := repo.MarkResourceFetchRunning(ctx, job.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEqual(t, claimToken, secondClaim)
	_, err = repo.MarkResourceFetchFailure(
		ctx,
		job.ID,
		secondClaim,
		"Microsoft mail fetch credentials are incomplete.",
		false,
		now.Add(2*time.Second),
		resourceFetchTestSystemLog(job, "resource_fetch_failed", "missing_token"),
	)
	require.NoError(t, err)
	stored, err = repo.FindResourceFetchJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobFailed, stored.Status)
	require.Equal(t, 2, stored.Attempts)
	require.NotNil(t, stored.FinishedAt)
}

func TestResourceFetchUseCaseProcessesOutsideTransactionAndIngestsMessagesMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 100).Update("status", "disabled").Error)
	messageRepo := NewRepo(db, nil)
	resourceRepo := newResourceFetchTestRepo(db)
	queue := &resourceFetchQueueStub{}
	transport := &resourceFetchTransportStub{
		result: &mailmatchapp.FetchMessagesResult{
			Messages: []mailmatchapp.FetchedMessage{{
				EmailResourceID:   100,
				ResourceType:      domain.ResourceTypeMicrosoft,
				Recipient:         "main@example.com",
				Recipients:        []string{"main@example.com"},
				Sender:            "noreply@example.net",
				Subject:           "Resource fetch test",
				Body:              "mail-body-resource-fetch-canary",
				BodyPreview:       "mail-body-resource-fetch-canary",
				MessageIDHeader:   "resource-fetch-message@example.net",
				ProviderMessageID: "provider-resource-fetch-1",
				Protocol:          "graph",
				Folder:            "inbox",
				ReceivedAt:        time.Now().UTC().Add(-time.Minute),
			}},
			RefreshToken: "resource-fetch-rotated-rt-canary",
		},
	}
	messageUseCase := mailmatchapp.NewUseCase(messageRepo, queue, transport, nil)
	resourceUseCase := mailmatchapp.NewResourceFetchUseCase(
		resourceRepo,
		queue,
		transport,
		messageUseCase,
		governanceinfra.NewSystemLogRepo(db),
	)

	result, err := resourceUseCase.Submit(context.Background(), mailmatchapp.ResourceFetchSubmitCommand{
		ResourceID:     100,
		OperatorUserID: 1,
		IdempotencyKey: "resource-fetch-usecase-idempotency",
		RequestID:      "req-resource-fetch-usecase",
		Path:           "/v1/admin/resources/:resourceId/messages/fetch",
	})
	require.NoError(t, err)
	require.False(t, result.Reused)
	require.NotZero(t, queue.dispatcherWakeups)
	dispatched, err := resourceUseCase.DispatchPending(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, dispatched.Queued)
	require.Len(t, queue.resourceTasks, 1)

	require.NoError(t, resourceUseCase.Process(context.Background(), queue.resourceTasks[0]))
	require.False(t, transport.calledInsideTransaction, "Graph/IMAP adapter must run outside the database transaction")
	storedJob, err := resourceRepo.FindResourceFetchJob(context.Background(), result.Job.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobSucceeded, storedJob.Status)
	require.Equal(t, 1, storedJob.FetchedCount)
	require.Equal(t, 1, storedJob.StoredCount)
	require.Equal(t, 0, storedJob.MatchedCount)

	var message MessageModel
	require.NoError(t, db.Where("email_resource_id = ?", 100).Take(&message).Error)
	require.Equal(t, "Resource fetch test", message.Subject)
	require.Equal(t, "mail-body-resource-fetch-canary", message.RawBody.String)
	require.Equal(t, "ignored", message.Status)
	var resourceStatus string
	require.NoError(t, db.Table("microsoft_resources").Select("status").Where("id = ?", 100).Scan(&resourceStatus).Error)
	require.Equal(t, "disabled", resourceStatus, "diagnostic fetch must not enable the resource")

	var credential struct {
		RefreshToken       string `gorm:"column:refresh_token"`
		CredentialRevision uint64 `gorm:"column:credential_revision"`
	}
	require.NoError(t, db.Table("microsoft_resources").
		Select("refresh_token, credential_revision").
		Where("id = ?", 100).
		Take(&credential).Error)
	require.Equal(t, "resource-fetch-rotated-rt-canary", credential.RefreshToken)
	require.Equal(t, uint64(2), credential.CredentialRevision)

	var operationLogs []governanceinfra.OperationLogModel
	require.NoError(t, db.Find(&operationLogs).Error)
	var systemLogs []governanceinfra.SystemLogModel
	require.NoError(t, db.Find(&systemLogs).Error)
	serializedLogs := strings.ToLower(fmt.Sprintf("%+v %+v", operationLogs, systemLogs))
	for _, canary := range []string{
		"mail-body-resource-fetch-canary",
		"resource-fetch-rotated-rt-canary",
		"main@example.com",
		"noreply@example.net",
	} {
		require.NotContains(t, serializedLogs, strings.ToLower(canary))
	}
}

func TestResourceFetchUseCaseRejectsStaleCredentialResultsBeforeMessageWriteMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	messageRepo := NewRepo(db, nil)
	resourceRepo := newResourceFetchTestRepo(db)
	queue := &resourceFetchQueueStub{}
	transport := &resourceFetchTransportStub{
		result: &mailmatchapp.FetchMessagesResult{Messages: []mailmatchapp.FetchedMessage{{
			EmailResourceID:   100,
			ResourceType:      domain.ResourceTypeMicrosoft,
			Recipient:         "main@example.com",
			Sender:            "sender@example.net",
			Subject:           "Must not persist",
			Body:              "stale-credential-message-canary",
			MessageIDHeader:   "stale-resource-fetch@example.net",
			ProviderMessageID: "stale-provider-message",
			Protocol:          "graph",
			Folder:            "inbox",
			ReceivedAt:        time.Now().UTC(),
		}}},
		beforeReturn: func() {
			require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 100).Updates(map[string]any{
				"refresh_token":       "replacement-during-fetch-canary",
				"credential_revision": gorm.Expr("credential_revision + 1"),
			}).Error)
		},
	}
	messageUseCase := mailmatchapp.NewUseCase(messageRepo, queue, transport, nil)
	resourceUseCase := mailmatchapp.NewResourceFetchUseCase(
		resourceRepo,
		queue,
		transport,
		messageUseCase,
		governanceinfra.NewSystemLogRepo(db),
	)
	result, err := resourceUseCase.Submit(context.Background(), mailmatchapp.ResourceFetchSubmitCommand{
		ResourceID:     100,
		OperatorUserID: 1,
		IdempotencyKey: "resource-fetch-stale-credential-idem",
		RequestID:      "req-resource-fetch-stale-credential",
		Path:           "/v1/admin/resources/:resourceId/messages/fetch",
	})
	require.NoError(t, err)
	_, err = resourceUseCase.DispatchPending(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, queue.resourceTasks, 1)
	require.NoError(t, resourceUseCase.Process(context.Background(), queue.resourceTasks[0]))

	storedJob, err := resourceRepo.FindResourceFetchJob(context.Background(), result.Job.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobCanceled, storedJob.Status)
	require.Equal(t, int64(0), tableCount(t, db, "mailmatch_messages"), "stale credential results cannot write message facts")
	var logs []governanceinfra.SystemLogModel
	require.NoError(t, db.Where("event_type = ?", "resource_fetch_canceled").Find(&logs).Error)
	require.Len(t, logs, 1)
	serialized := strings.ToLower(fmt.Sprintf("%+v", logs))
	require.NotContains(t, serialized, "replacement-during-fetch-canary")
	require.NotContains(t, serialized, "stale-credential-message-canary")
}

func resourceFetchTestJob(now time.Time, requestID string) domain.ResourceFetchJob {
	sinceAt := now.Add(-time.Hour)
	untilAt := now
	return domain.ResourceFetchJob{
		ResourceID:     100,
		OperatorUserID: 1,
		Status:         domain.ResourceFetchJobQueued,
		MaxAttempts:    domain.ResourceFetchDefaultMaxAttempts,
		SinceAt:        &sinceAt,
		UntilAt:        &untilAt,
		RequestID:      requestID,
		Path:           "/v1/admin/resources/:resourceId/messages/fetch",
		IdempotencyKey: "idem-" + requestID,
	}
}

func newResourceFetchTestRepo(db *gorm.DB) *ResourceFetchRepo {
	repo := NewResourceFetchRepo(db)
	repo.SetMicrosoftCredentialPort(coreapp.NewMicrosoftCredentialService(coreinfra.NewAdminResourceRepo(db)))
	return repo
}

func resourceFetchTestOperationLog(requestID string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "mailmatch.admin_resource.fetch",
		ResourceType:   "microsoft_resource",
		ResourceID:     "100",
		Path:           "/v1/admin/resources/:resourceId/messages/fetch",
		Result:         "success",
		SafeSummary:    "Microsoft resource mail fetch accepted.",
		RequestID:      requestID,
	}
}

func resourceFetchTestSystemLog(job domain.ResourceFetchJob, eventType string, detail string) *governancedomain.SystemLog {
	return &governancedomain.SystemLog{
		Level:     "warning",
		Module:    "mailmatch",
		EventType: eventType,
		RequestID: job.RequestID,
		BizType:   "microsoft_resource",
		BizID:     fmt.Sprintf("%d", job.ResourceID),
		Message:   "Safe resource fetch diagnostic.",
		Detail:    detail,
	}
}

func tableCount(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(table).Count(&count).Error)
	return count
}

type resourceFetchQueueStub struct {
	resourceTasks     []mailmatchapp.ResourceFetchTask
	dispatcherWakeups int
}

func (q *resourceFetchQueueStub) EnqueueFetch(context.Context, mailmatchapp.FetchTask) error {
	return nil
}

func (q *resourceFetchQueueStub) EnqueueResourceFetch(_ context.Context, task mailmatchapp.ResourceFetchTask) error {
	q.resourceTasks = append(q.resourceTasks, task)
	return nil
}

func (q *resourceFetchQueueStub) EnqueueFetchDispatcher(context.Context, time.Duration) error {
	q.dispatcherWakeups++
	return nil
}

type resourceFetchTransportStub struct {
	result                  *mailmatchapp.FetchMessagesResult
	err                     error
	calledInsideTransaction bool
	beforeReturn            func()
}

func (s *resourceFetchTransportStub) FetchMicrosoftMessages(ctx context.Context, _ mailmatchapp.FetchMessagesRequest) (*mailmatchapp.FetchMessagesResult, error) {
	_, s.calledInsideTransaction = platform.GormTxFromContext(ctx)
	if s.beforeReturn != nil {
		s.beforeReturn()
	}
	return s.result, s.err
}
