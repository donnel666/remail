package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

type microsoftTokenRefreshRepoStub struct {
	state         *MicrosoftTokenRefreshState
	pending       []MicrosoftTokenRefreshState
	events        []string
	processing    bool
	execution     *MicrosoftTokenRefreshExecution
	current       bool
	loadErr       error
	releaseCalls  int
	retryCalls    int
	retryAbnormal bool
	retryErr      error
	applied       []MicrosoftTokenRefreshProtocolResult
	operationLog  *governancedomain.OperationLog
}

func (r *microsoftTokenRefreshRepoStub) Request(_ context.Context, _ MicrosoftTokenRefreshCommand, log *governancedomain.OperationLog) (*MicrosoftTokenRefreshState, bool, error) {
	if log != nil {
		clone := *log
		r.operationLog = &clone
	}
	if r.state == nil {
		now := time.Now().UTC()
		r.state = &MicrosoftTokenRefreshState{
			ResourceID: 42, Generation: 1, ExpectedCredentialRevision: 7,
			Status: MicrosoftTokenRefreshPending, RequestedAt: &now, UpdatedAt: now,
		}
	}
	clone := *r.state
	return &clone, false, nil
}

func (r *microsoftTokenRefreshRepoStub) ListPending(context.Context, int) ([]MicrosoftTokenRefreshState, error) {
	return append([]MicrosoftTokenRefreshState(nil), r.pending...), nil
}

func (r *microsoftTokenRefreshRepoStub) MarkProcessing(context.Context, uint, uint64) (bool, error) {
	r.events = append(r.events, "processing")
	return r.processing, nil
}

func (r *microsoftTokenRefreshRepoStub) ReleaseInfrastructureFailure(context.Context, uint, uint64, string) (bool, error) {
	r.releaseCalls++
	return true, nil
}

func (r *microsoftTokenRefreshRepoStub) LoadExecution(context.Context, MicrosoftTokenRefreshTask) (*MicrosoftTokenRefreshExecution, bool, error) {
	return r.execution, r.current, r.loadErr
}

func (r *microsoftTokenRefreshRepoStub) RecordRetryableFailure(context.Context, MicrosoftTokenRefreshTask, string) (bool, error) {
	r.retryCalls++
	return r.retryAbnormal, r.retryErr
}

func (r *microsoftTokenRefreshRepoStub) ApplyResult(_ context.Context, _ MicrosoftTokenRefreshTask, result MicrosoftTokenRefreshProtocolResult) error {
	r.applied = append(r.applied, result)
	return nil
}

type microsoftTokenRefreshQueueStub struct {
	accepted        bool
	err             error
	events          *[]string
	tasks           []MicrosoftTokenRefreshTask
	dispatcherCalls int
}

func (q *microsoftTokenRefreshQueueStub) EnqueueMicrosoftTokenRefresh(_ context.Context, task MicrosoftTokenRefreshTask) (bool, error) {
	if q.events != nil {
		*q.events = append(*q.events, "enqueue")
	}
	q.tasks = append(q.tasks, task)
	return q.accepted, q.err
}

func (q *microsoftTokenRefreshQueueStub) EnqueueMicrosoftTokenRefreshDispatcher(context.Context, time.Duration) error {
	q.dispatcherCalls++
	return nil
}

type microsoftTokenRefresherStub struct {
	result MicrosoftTokenRefreshProtocolResult
	err    error
}

func (r microsoftTokenRefresherStub) RefreshMicrosoftToken(context.Context, MicrosoftTokenRefreshProtocolRequest) (MicrosoftTokenRefreshProtocolResult, error) {
	return r.result, r.err
}

func TestMicrosoftTokenRefreshDispatchActivatesOnlyAcceptedTask(t *testing.T) {
	tests := []struct {
		name       string
		accepted   bool
		queueErr   error
		wantEvents []string
		wantQueued int
		wantFailed int
	}{
		{name: "accepted", accepted: true, wantEvents: []string{"enqueue", "processing"}, wantQueued: 1},
		{name: "duplicate", wantEvents: []string{"enqueue"}},
		{name: "redis failure", queueErr: errors.New("redis unavailable"), wantEvents: []string{"enqueue"}, wantFailed: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &microsoftTokenRefreshRepoStub{
				pending:    []MicrosoftTokenRefreshState{{ResourceID: 42, Generation: 7, ExpectedCredentialRevision: 3}},
				processing: true,
			}
			queue := &microsoftTokenRefreshQueueStub{accepted: tt.accepted, err: tt.queueErr, events: &repo.events}
			result, err := NewMicrosoftTokenRefreshService(repo, queue, nil).DispatchPending(context.Background(), 10)
			if tt.queueErr != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.True(t, slices.Equal(repo.events, tt.wantEvents), "events=%v", repo.events)
			require.Equal(t, tt.wantQueued, result.Queued)
			require.Equal(t, tt.wantFailed, result.Failed)
		})
	}
}

func TestMicrosoftTokenRefreshInfrastructureFailureReleasesWithoutBusinessFailure(t *testing.T) {
	repo := &microsoftTokenRefreshRepoStub{
		current: true,
		execution: &MicrosoftTokenRefreshExecution{State: MicrosoftTokenRefreshState{
			ResourceID: 42, Generation: 7, ExpectedCredentialRevision: 3,
		}},
	}
	service := NewMicrosoftTokenRefreshService(repo, &microsoftTokenRefreshQueueStub{}, microsoftTokenRefresherStub{err: errors.New("network down")})
	err := service.Process(context.Background(), MicrosoftTokenRefreshTask{ResourceID: 42, Generation: 7, ExpectedCredentialRevision: 3})
	require.Error(t, err)
	require.Equal(t, 1, repo.releaseCalls)
	require.Zero(t, repo.retryCalls)
}

func TestMicrosoftTokenRefreshThirdProtocolFailureIsAbnormal(t *testing.T) {
	repo := &microsoftTokenRefreshRepoStub{
		current:       true,
		retryAbnormal: true,
		execution: &MicrosoftTokenRefreshExecution{State: MicrosoftTokenRefreshState{
			ResourceID: 42, Generation: 7, ExpectedCredentialRevision: 3,
		}},
	}
	queue := &microsoftTokenRefreshQueueStub{}
	service := NewMicrosoftTokenRefreshService(repo, queue, microsoftTokenRefresherStub{result: MicrosoftTokenRefreshProtocolResult{Category: "rate_limited"}})
	require.NoError(t, service.Process(context.Background(), MicrosoftTokenRefreshTask{ResourceID: 42, Generation: 7, ExpectedCredentialRevision: 3}))
	require.Equal(t, 1, repo.retryCalls)
	require.Zero(t, queue.dispatcherCalls, "terminal failure must not wake another retry")
}

func TestMicrosoftTokenRefreshPersistenceFailureReleasesInfrastructureState(t *testing.T) {
	repo := &microsoftTokenRefreshRepoStub{
		current:  true,
		retryErr: ErrMicrosoftTokenRefreshUnavailable,
		execution: &MicrosoftTokenRefreshExecution{State: MicrosoftTokenRefreshState{
			ResourceID: 42, Generation: 7, ExpectedCredentialRevision: 3,
		}},
	}
	service := NewMicrosoftTokenRefreshService(repo, &microsoftTokenRefreshQueueStub{}, microsoftTokenRefresherStub{
		result: MicrosoftTokenRefreshProtocolResult{Category: "rate_limited"},
	})
	err := service.Process(context.Background(), MicrosoftTokenRefreshTask{ResourceID: 42, Generation: 7, ExpectedCredentialRevision: 3})
	require.ErrorIs(t, err, ErrMicrosoftTokenRefreshUnavailable)
	require.Equal(t, 1, repo.releaseCalls)
}

func TestMicrosoftTokenRefreshAcceptUsesResourceAsTaskIdentity(t *testing.T) {
	now := time.Now().UTC()
	repo := &microsoftTokenRefreshRepoStub{state: &MicrosoftTokenRefreshState{
		ResourceID: 42, Generation: 3, ExpectedCredentialRevision: 7,
		Status: MicrosoftTokenRefreshPending, RequestedAt: &now, UpdatedAt: now,
	}}
	queue := &microsoftTokenRefreshQueueStub{}
	accepted, err := NewMicrosoftTokenRefreshService(repo, queue, nil).Accept(context.Background(), MicrosoftTokenRefreshCommand{
		ResourceID: 42, OperatorUserID: 7, IdempotencyKey: "request-42", RequestID: "request-id",
	})
	require.NoError(t, err)
	require.Equal(t, "token:42", accepted.Task.TaskID())
	require.Equal(t, "queued", accepted.Task.Status)
	require.Equal(t, 1, queue.dispatcherCalls)
	require.NotNil(t, repo.operationLog)
}
