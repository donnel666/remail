package app

import (
	"context"
	"errors"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type microsoftTokenRefreshRepoStub struct {
	job                *MicrosoftTokenRefreshJob
	reused             bool
	createErr          error
	operationLog       *governancedomain.OperationLog
	dispatchable       []MicrosoftTokenRefreshJob
	markDispatchFailed int
	released           int
	execution          *MicrosoftTokenRefreshExecution
	claimed            bool
	claimErr           error
	retryExhausted     bool
	retryCalls         int
	applyCalls         int
	appliedResult      MicrosoftTokenRefreshProtocolResult
	applyErr           error
}

func (r *microsoftTokenRefreshRepoStub) CreateOrReuse(_ context.Context, _ MicrosoftTokenRefreshCommand, log *governancedomain.OperationLog) (*MicrosoftTokenRefreshJob, bool, error) {
	if log != nil {
		clone := *log
		r.operationLog = &clone
	}
	return r.job, r.reused, r.createErr
}

func (r *microsoftTokenRefreshRepoStub) ClaimDispatchable(context.Context, int, time.Time, time.Time) ([]MicrosoftTokenRefreshJob, error) {
	return append([]MicrosoftTokenRefreshJob(nil), r.dispatchable...), nil
}

func (r *microsoftTokenRefreshRepoStub) MarkDispatchFailed(context.Context, uint64, string, string) error {
	r.markDispatchFailed++
	return nil
}

func (r *microsoftTokenRefreshRepoStub) ReleaseDispatch(context.Context, uint64, string) error {
	r.released++
	return nil
}

func (r *microsoftTokenRefreshRepoStub) ClaimExecution(context.Context, uint64, string, time.Time) (*MicrosoftTokenRefreshExecution, bool, error) {
	return r.execution, r.claimed, r.claimErr
}

func (r *microsoftTokenRefreshRepoStub) MarkRetryableFailure(context.Context, uint64, string, string) (bool, error) {
	r.retryCalls++
	return r.retryExhausted, nil
}

func (r *microsoftTokenRefreshRepoStub) ApplyResult(_ context.Context, _ uint64, _ string, result MicrosoftTokenRefreshProtocolResult) error {
	r.applyCalls++
	r.appliedResult = result
	return r.applyErr
}

type microsoftTokenRefreshQueueStub struct {
	tasks            []MicrosoftTokenRefreshTask
	dispatcherCalls  int
	taskErr          error
	dispatcherErr    error
	dispatcherDelays []time.Duration
}

func (q *microsoftTokenRefreshQueueStub) EnqueueMicrosoftTokenRefresh(_ context.Context, task MicrosoftTokenRefreshTask) error {
	q.tasks = append(q.tasks, task)
	return q.taskErr
}

func (q *microsoftTokenRefreshQueueStub) EnqueueMicrosoftTokenRefreshDispatcher(_ context.Context, delay time.Duration) error {
	q.dispatcherCalls++
	q.dispatcherDelays = append(q.dispatcherDelays, delay)
	return q.dispatcherErr
}

type microsoftTokenRefresherStub struct {
	request MicrosoftTokenRefreshProtocolRequest
	result  MicrosoftTokenRefreshProtocolResult
	err     error
}

func (r *microsoftTokenRefresherStub) RefreshMicrosoftToken(_ context.Context, request MicrosoftTokenRefreshProtocolRequest) (MicrosoftTokenRefreshProtocolResult, error) {
	r.request = request
	return r.result, r.err
}

func TestMicrosoftTokenRefreshAcceptReturnsGovernanceTaskAndSafeAudit(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	repo := &microsoftTokenRefreshRepoStub{job: &MicrosoftTokenRefreshJob{
		ID:                         91,
		ResourceID:                 42,
		ExpectedCredentialRevision: 7,
		Status:                     MicrosoftTokenRefreshQueued,
		MaxAttempts:                3,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}}
	queue := &microsoftTokenRefreshQueueStub{dispatcherErr: errors.New("redis unavailable")}
	service := NewMicrosoftTokenRefreshService(repo, queue, nil)
	service.now = func() time.Time { return now }

	accepted, err := service.Accept(context.Background(), MicrosoftTokenRefreshCommand{
		ResourceID:     42,
		OperatorUserID: 7,
		IdempotencyKey: " token-refresh-key ",
		RequestID:      "request-42",
		Path:           "/v1/admin/resources/:resourceId/token/refresh",
	})
	require.NoError(t, err)
	require.NotNil(t, accepted)
	assert.Equal(t, "token:91", accepted.Task.TaskID())
	assert.Equal(t, uint64(42), accepted.Task.BizID)
	assert.Equal(t, governanceapp.AdminTaskKindToken, accepted.Task.Kind)
	assert.Equal(t, MicrosoftTokenRefreshQueued, accepted.Task.Status)
	require.NotNil(t, accepted.Task.CredentialRevision)
	assert.Equal(t, uint64(7), *accepted.Task.CredentialRevision)
	require.NotNil(t, repo.operationLog)
	assert.Equal(t, "mailtransport.microsoft_token_refresh.accept", repo.operationLog.OperationType)
	assert.Equal(t, "42", repo.operationLog.ResourceID)
	assert.Equal(t, 1, queue.dispatcherCalls)

	_, err = service.Accept(context.Background(), MicrosoftTokenRefreshCommand{
		ResourceID:     42,
		OperatorUserID: 7,
		IdempotencyKey: "",
	})
	require.ErrorIs(t, err, ErrInvalidMicrosoftTokenRefresh)
}

func TestMicrosoftTokenRefreshDispatchPersistsQueueFailureForRecovery(t *testing.T) {
	repo := &microsoftTokenRefreshRepoStub{dispatchable: []MicrosoftTokenRefreshJob{{
		ID:            91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
		RequestID:     "request-42",
	}}}
	queue := &microsoftTokenRefreshQueueStub{taskErr: errors.New("redis unavailable")}
	service := NewMicrosoftTokenRefreshService(repo, queue, nil)

	result, err := service.DispatchPending(context.Background(), 10)
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Attempted)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, 1, repo.markDispatchFailed)
	require.Len(t, queue.tasks, 1)
	assert.Equal(t, uint(42), queue.tasks[0].ResourceID)
}

func TestMicrosoftTokenRefreshProcessAppliesSuccessAndRetriesSafeTransientFailure(t *testing.T) {
	repo := &microsoftTokenRefreshRepoStub{
		claimed: true,
		execution: &MicrosoftTokenRefreshExecution{
			Job:          MicrosoftTokenRefreshJob{ID: 91, ResourceID: 42, ClaimToken: "claim-token", RequestID: "request-42"},
			EmailAddress: "main@example.com",
			ClientID:     "client-id",
			RefreshToken: "refresh-token",
		},
	}
	queue := &microsoftTokenRefreshQueueStub{}
	refresher := &microsoftTokenRefresherStub{result: MicrosoftTokenRefreshProtocolResult{
		Valid:        true,
		ClientID:     "rotated-client-id",
		RefreshToken: "rotated-refresh-token",
	}}
	service := NewMicrosoftTokenRefreshService(repo, queue, refresher)
	require.NoError(t, service.Process(context.Background(), MicrosoftTokenRefreshTask{
		JobID:         91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
	}))
	assert.Equal(t, 1, repo.applyCalls)
	assert.True(t, repo.appliedResult.Valid)
	assert.Equal(t, "rotated-refresh-token", repo.appliedResult.RefreshToken)
	assert.Equal(t, "main@example.com", refresher.request.EmailAddress)

	repo.applyCalls = 0
	refresher.result = MicrosoftTokenRefreshProtocolResult{
		Category:    "rate_limited",
		SafeMessage: "Microsoft mail service is rate limited.",
	}
	require.NoError(t, service.Process(context.Background(), MicrosoftTokenRefreshTask{
		JobID:         91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
	}))
	assert.Equal(t, 1, repo.retryCalls)
	assert.Zero(t, repo.applyCalls)
	assert.Equal(t, 1, queue.dispatcherCalls)

	refresher.result = MicrosoftTokenRefreshProtocolResult{
		Category:    "oauth_invalid_grant",
		SafeMessage: "raw refresh-token-canary from upstream",
	}
	require.NoError(t, service.Process(context.Background(), MicrosoftTokenRefreshTask{
		JobID:         91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
	}))
	assert.Equal(t, 1, repo.applyCalls)
	assert.Equal(t, "Microsoft refresh token is invalid or expired.", repo.appliedResult.SafeMessage)
	assert.NotContains(t, repo.appliedResult.SafeMessage, "canary")
}

func TestMicrosoftTokenRefreshProcessTreatsStaleResultAsTerminalNoop(t *testing.T) {
	repo := &microsoftTokenRefreshRepoStub{
		claimed:  true,
		applyErr: ErrMicrosoftTokenRefreshStale,
		execution: &MicrosoftTokenRefreshExecution{
			Job: MicrosoftTokenRefreshJob{ID: 91, ResourceID: 42, ClaimToken: "claim-token"},
		},
	}
	service := NewMicrosoftTokenRefreshService(repo, nil, &microsoftTokenRefresherStub{result: MicrosoftTokenRefreshProtocolResult{Valid: true}})
	require.NoError(t, service.Process(context.Background(), MicrosoftTokenRefreshTask{
		JobID:         91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
	}))
}
