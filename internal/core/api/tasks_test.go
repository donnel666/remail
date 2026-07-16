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
	resources := newMockResourceRepo()
	root := &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resources.CreateMicrosoft(context.Background(), root, &coredomain.MicrosoftResource{
		EmailAddress: "load@example.com", Password: "secret", Status: coredomain.MicrosoftStatusValidating, CredentialRevision: 3,
	}))
	repo := newMockValidationRepo(resources)
	gate := &coreBackgroundExecutionGateStub{}
	module := &CoreModule{
		ValidationUseCase:   coreapp.NewResourceValidationUseCase(nil, repo, &mockValidationQueue{}, nil),
		BackgroundExecution: gate,
	}
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, module)
	encoded, err := json.Marshal(coreapp.ResourceValidationTask{
		ResourceID: root.ID, ResourceType: coredomain.ResourceTypeMicrosoft,
		OwnerUserID: 1, ExpectedCredentialRevision: 3,
	})
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(coreinfra.TypeResourceValidation, encoded))

	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.Equal(t, coredomain.MicrosoftStatusValidating, resources.microsoft[root.ID].Status)
	require.False(t, gate.released.Load(), "a denied task never owns a permit")
}

type dispatcherCountingQueue struct {
	calls atomic.Int32
}

func (*dispatcherCountingQueue) EnqueueResourceValidation(context.Context, coreapp.ResourceValidationTask) error {
	return nil
}

func (*dispatcherCountingQueue) EnqueueResourceValidationBatch(context.Context, coreapp.ResourceValidationBatchTask) error {
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
