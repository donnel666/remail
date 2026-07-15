package api

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type coreBackgroundExecutionGateStub struct {
	admitted bool
	released atomic.Bool
}

func (s *coreBackgroundExecutionGateStub) TryAcquire() (func(), bool) {
	return func() { s.released.Store(true) }, s.admitted
}

func TestResourceValidationAdmissionDenialDefersInAsynqWithoutDatabaseMutation(t *testing.T) {
	repo := newMockValidationRepo(nil)
	now := time.Now().UTC()
	repo.jobs[1] = &coredomain.ResourceValidation{
		ID:            1,
		Status:        coredomain.ResourceValidationQueued,
		DispatchToken: "dispatch-token",
		DispatchedAt:  &now,
		MaxAttempts:   coredomain.ResourceValidationDefaultMaxAttempts,
	}
	gate := &coreBackgroundExecutionGateStub{}
	module := &CoreModule{
		ValidationUseCase:   coreapp.NewResourceValidationUseCase(nil, repo, &mockValidationQueue{}, nil),
		BackgroundExecution: gate,
	}
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, module)
	encoded, err := json.Marshal(coreapp.ResourceValidationTask{
		JobID:         1,
		DispatchToken: "dispatch-token",
	})
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(coreinfra.TypeResourceValidation, encoded))

	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.Equal(t, "dispatch-token", repo.jobs[1].DispatchToken)
	require.NotNil(t, repo.jobs[1].DispatchedAt)
	require.Equal(t, coredomain.ResourceValidationQueued, repo.jobs[1].Status)
	require.False(t, gate.released.Load(), "a denied task never owns a permit")
}

type dispatcherCountingQueue struct {
	calls atomic.Int32
}

func (*dispatcherCountingQueue) EnqueueResourceValidation(context.Context, coreapp.ResourceValidationTask) error {
	return nil
}

func (q *dispatcherCountingQueue) EnqueueResourceValidationDispatcher(context.Context, time.Duration) error {
	q.calls.Add(1)
	return nil
}

func TestResourceValidationDispatcherSeederStopsOnCleanup(t *testing.T) {
	queue := &dispatcherCountingQueue{}
	module := &CoreModule{
		ValidationUseCase: coreapp.NewResourceValidationUseCase(nil, nil, queue, nil),
	}
	cleanup := startResourceValidationDispatcher(context.Background(), module, 2*time.Millisecond)
	require.Eventually(t, func() bool {
		return queue.calls.Load() >= 2
	}, 100*time.Millisecond, time.Millisecond)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cleanup(shutdownCtx)
	stoppedAt := queue.calls.Load()
	time.Sleep(10 * time.Millisecond)

	require.Equal(t, stoppedAt, queue.calls.Load())
}
