package infra

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestValidationDoesNotBlockOnUnrelatedMailboxOperation(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "busy@daum.net", AppPassword: "test-only"})
	require.NoError(t, err)
	server := miniredis.RunT(t)
	s.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = s.Redis.Close() })
	key := fmt.Sprintf("daum:mailbox:%d", id)
	require.NoError(t, s.Redis.Set(ctx, key, "another-reader", 0).Err())
	calls := 0
	s.Protocol = daumValidationClientStub{validate: func(context.Context, protocol.Credentials) error { calls++; return nil }}
	task := app.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, ValidationGeneration: 1, CredentialRevision: 1}
	require.NoError(t, s.ProcessValidation(ctx, task))
	require.Equal(t, 1, calls)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusIdentifying, row.Status)
	run, err := s.FindMaintenanceRun(ctx, id, 1, maintenanceKindValidation)
	require.NoError(t, err)
	require.Equal(t, 1, run.Attempts)
	require.Equal(t, maintenanceSucceeded, run.Status)
}

func TestHistoryContentionDefersWithoutBudgetOrCheckpointLoss(t *testing.T) {
	for _, category := range []string{"busy", "proxy_unavailable"} {
		t.Run(category, func(t *testing.T) {
			s := newHistoryTestService(t)
			ctx := context.Background()
			resource, task := historyTestResource(t, s, "history-busy@daum.net", domain.StatusIdentifying)
			busy := func(context.Context, uint, uint64, protocol.FetchRequest) (protocol.FetchResult, error) {
				return protocol.FetchResult{}, &protocol.Failure{Category: category, Retryable: true, SafeMessage: "Waiting for mailbox capacity."}
			}
			for i := 0; i < 6; i++ {
				require.NoError(t, s.processHistory(ctx, task, busy))
			}
			row, err := s.GetResource(ctx, resource.ID, nil)
			require.NoError(t, err)
			require.Equal(t, domain.StatusIdentifying, row.Status)
			require.Zero(t, row.ValidationFailures)
			run, err := s.FindMaintenanceRun(ctx, resource.ID, task.ValidationGeneration, maintenanceKindHistory)
			require.NoError(t, err)
			require.Equal(t, maintenanceQueued, run.Status)
			require.Zero(t, run.Attempts)
			require.NoError(t, s.processHistory(ctx, task, func(context.Context, uint, uint64, protocol.FetchRequest) (protocol.FetchResult, error) {
				return protocol.FetchResult{Complete: true}, nil
			}))
			row, err = s.GetResource(ctx, resource.ID, nil)
			require.NoError(t, err)
			require.Equal(t, domain.StatusNormal, row.Status)
		})
	}
}

func TestProjectHistoryContentionDoesNotConsumeFailuresOrSkipResource(t *testing.T) {
	for _, category := range []string{"busy", "proxy_unavailable"} {
		t.Run(category, func(t *testing.T) {
			s := newHistoryTestService(t)
			ctx := context.Background()
			resource, _ := historyTestResource(t, s, "project-busy@daum.net", domain.StatusNormal)
			s.Queue = &daumQueueStub{}
			require.NoError(t, s.ScheduleProjectHistory(ctx, 10, "busy-project"))
			require.NoError(t, s.DB.Model(&ProjectHistoryState{}).Where("project_id = 10").Update("failures", 2).Error)
			task := ProjectHistoryTask{ProjectID: 10, Generation: 1}
			busy := func(context.Context, uint, uint64, protocol.FetchRequest) (protocol.FetchResult, error) {
				return protocol.FetchResult{}, &protocol.Failure{Category: category, Retryable: true, SafeMessage: "Waiting for mailbox capacity."}
			}
			for i := 0; i < 6; i++ {
				require.NoError(t, s.processProjectHistory(ctx, task, busy))
			}
			var state ProjectHistoryState
			require.NoError(t, s.DB.First(&state, 10).Error)
			require.Equal(t, "pending", state.Status)
			require.Equal(t, 2, state.Failures)
			require.Zero(t, state.AfterID)
			require.Zero(t, state.ScannedCount)
			require.Zero(t, state.SkippedCount)
			row, err := s.GetResource(ctx, resource.ID, nil)
			require.NoError(t, err)
			require.Equal(t, domain.StatusNormal, row.Status)
			require.NoError(t, s.processProjectHistory(ctx, task, func(context.Context, uint, uint64, protocol.FetchRequest) (protocol.FetchResult, error) {
				return protocol.FetchResult{Complete: true}, nil
			}))
			require.NoError(t, s.DB.First(&state, 10).Error)
			require.Equal(t, resource.ID, state.AfterID)
			require.Equal(t, 2, state.Failures)
			require.Equal(t, 1, state.ScannedCount)
		})
	}
}

func TestValidationOnlyRecordsConfirmedPermanentFailuresForRefunds(t *testing.T) {
	for _, tc := range []struct {
		category  string
		retryable bool
		want      string
		attempts  int
	}{{"transport", true, "", 3}, {"protocol", false, "", 3}, {"protocol_disabled", false, "", 3}, {"invalid_credentials", false, "invalid_credentials", 1}, {"identity_mismatch", false, "identity_mismatch", 1}, {"account_disabled", false, "account_disabled", 1}} {
		t.Run(tc.category, func(t *testing.T) {
			s, _ := newDaumAsyncTestService(t)
			ctx := context.Background()
			id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "failure@daum.net", AppPassword: "test-only"})
			require.NoError(t, err)
			s.Protocol = daumValidationClientStub{validate: func(context.Context, protocol.Credentials) error {
				return &protocol.Failure{Category: tc.category, Retryable: tc.retryable, SafeMessage: "Safe provider failure."}
			}}
			for i := 0; i < tc.attempts; i++ {
				row, err := s.GetResource(ctx, id, nil)
				require.NoError(t, err)
				require.NoError(t, s.ProcessValidation(ctx, app.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, ValidationGeneration: row.ValidationGeneration, CredentialRevision: row.CredentialRevision}))
			}
			var row Resource
			require.NoError(t, s.DB.First(&row, id).Error)
			require.Equal(t, domain.StatusAbnormal, row.Status)
			require.Equal(t, tc.want, row.FailureCategory)
			next := "next-test-password"
			require.NoError(t, s.ReplaceCredentials(ctx, id, nil, nil, &next, nil))
			require.NoError(t, s.DB.First(&row, id).Error)
			require.Empty(t, row.FailureCategory)
		})
	}
}

type busyDuplicateQueue struct{}

func (busyDuplicateQueue) EnqueueContext(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	return nil, asynq.ErrDuplicateTask
}
func TestValidationDuplicateQueueDoesNotStrandDeferredResource(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "duplicate@daum.net", AppPassword: "test-only"})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		count, err := s.DispatchPendingValidations(ctx, busyDuplicateQueue{}, 10)
		require.NoError(t, err)
		require.Equal(t, 1, count)
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Equal(t, domain.StatusPending, row.Status)
	}
}

func TestValidationCapacityDefersWithoutConsumingAttempt(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	s.Proxies = &daumNetworkProxies{t: t}
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "capacity-wait@daum.net", AppPassword: "test-only"})
	require.NoError(t, err)
	s.Protocol = daumValidationClientStub{validate: func(context.Context, protocol.Credentials) error {
		return &protocol.Failure{Category: "busy", Retryable: true, SafeMessage: "Waiting for mailbox capacity."}
	}}
	task := app.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, ValidationGeneration: 1, CredentialRevision: 1}
	for i := 0; i < 6; i++ {
		require.NoError(t, s.ProcessValidation(ctx, task))
	}
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, row.Status)
	require.Zero(t, row.ValidationFailures)
	require.Empty(t, row.FailureCategory)
	run, err := s.FindMaintenanceRun(ctx, id, 1, maintenanceKindValidation)
	require.NoError(t, err)
	require.Equal(t, maintenanceQueued, run.Status)
	require.Zero(t, run.Attempts)
}
