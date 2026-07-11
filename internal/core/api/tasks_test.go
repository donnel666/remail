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
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type coreBackgroundDispatchStub struct {
	admitted bool
	queue    string
	released bool
}

func (s *coreBackgroundDispatchStub) AcquireDispatchBudget(context.Context, string, int, int) (int, func()) {
	return 0, func() {}
}

func (s *coreBackgroundDispatchStub) TryAcquireExecution(_ context.Context, queue string) (bool, func()) {
	s.queue = queue
	return s.admitted, func() { s.released = true }
}

func TestResourceValidationTaskAdmissionDenialReleasesDurableDispatch(t *testing.T) {
	repo := newMockValidationRepo(nil)
	now := time.Now().UTC()
	repo.jobs[1] = &coredomain.ResourceValidation{
		ID:            1,
		Status:        coredomain.ResourceValidationQueued,
		DispatchToken: "dispatch-token",
		DispatchedAt:  &now,
		MaxAttempts:   coredomain.ResourceValidationDefaultMaxAttempts,
	}
	gate := &coreBackgroundDispatchStub{}
	module := &CoreModule{
		ValidationUseCase:  coreapp.NewResourceValidationUseCase(nil, repo, &mockValidationQueue{}, nil),
		BackgroundDispatch: gate,
	}
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, module)
	encoded, err := json.Marshal(coreapp.ResourceValidationTask{
		JobID:         1,
		DispatchToken: "dispatch-token",
	})
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(coreinfra.TypeResourceValidation, encoded))

	require.NoError(t, err)
	require.Equal(t, coreinfra.ResourceValidationQueueName, gate.queue)
	require.Empty(t, repo.jobs[1].DispatchToken)
	require.Nil(t, repo.jobs[1].DispatchedAt)
	require.Equal(t, coredomain.ResourceValidationQueued, repo.jobs[1].Status)
	require.False(t, gate.released)
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
