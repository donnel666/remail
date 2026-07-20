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
	admitted  bool
	available int
	limit     int
	released  atomic.Bool
}

func (s *coreBackgroundExecutionGateStub) TryAcquire() (func(), bool) {
	return func() { s.released.Store(true) }, s.admitted
}

func (s *coreBackgroundExecutionGateStub) Available() int {
	return s.available
}

func (s *coreBackgroundExecutionGateStub) Snapshot() platform.BackgroundLoadSnapshot {
	return platform.BackgroundLoadSnapshot{Limit: s.limit}
}

func TestBackgroundDispatchLimitUsesOnlyUnusedCapacity(t *testing.T) {
	gate := &coreBackgroundExecutionGateStub{available: 7}

	require.Equal(t, 7, backgroundDispatchLimit(gate, resourceValidationDispatchMaximum))
	gate.available = 0
	require.Zero(t, backgroundDispatchLimit(gate, resourceValidationDispatchMaximum))
}

func TestBackgroundValidationWindowLimitUsesTotalWindow(t *testing.T) {
	gate := &coreBackgroundExecutionGateStub{available: 7, limit: 32}

	require.Equal(t, 32, backgroundValidationWindowLimit(gate, resourceValidationDispatchMaximum))
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
		OwnerUserID: 1, ValidationGeneration: resources.microsoft[root.ID].ValidationGeneration, ExpectedCredentialRevision: 3,
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

func (*dispatcherCountingQueue) EnqueueResourceValidation(context.Context, coreapp.ResourceValidationTask) (bool, error) {
	return true, nil
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
	cleanup := startResourceValidationDispatcher(context.Background(), module, 2*time.Millisecond, time.Second)
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

type deadlineBlockingDispatcherQueue struct {
	calls atomic.Int32
}

func (*deadlineBlockingDispatcherQueue) EnqueueResourceValidation(context.Context, coreapp.ResourceValidationTask) (bool, error) {
	return true, nil
}

func (*deadlineBlockingDispatcherQueue) EnqueueResourceValidationBatch(context.Context, coreapp.ResourceValidationBatchTask) error {
	return nil
}

func (q *deadlineBlockingDispatcherQueue) EnqueueResourceValidationDispatcher(ctx context.Context, _ time.Duration) error {
	q.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

type deadlineBlockingImportRepo struct {
	*mockImportRepo
	calls atomic.Int32
}

func (r *deadlineBlockingImportRepo) ClaimAdminImportDispatchable(ctx context.Context, _ int, _, _ time.Time) ([]coreapp.AdminResourceImportDispatchItem, error) {
	r.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestResourceValidationDispatcherDeadlinesAllowLaterTicksAndShutdown(t *testing.T) {
	validationQueue := &deadlineBlockingDispatcherQueue{}
	importRepo := &deadlineBlockingImportRepo{mockImportRepo: newMockImportRepo(newMockResourceRepo())}
	module := &CoreModule{
		ValidationUseCase: coreapp.NewResourceValidationUseCase(nil, nil, validationQueue, nil),
		ImportUseCase:     coreapp.NewImportUseCase(nil, importRepo, nil, nil, &mockImportQueue{}),
	}

	startedAt := time.Now()
	cleanup := startResourceValidationDispatcher(context.Background(), module, time.Millisecond, 5*time.Millisecond)
	require.Less(t, time.Since(startedAt), 100*time.Millisecond, "the initial validation seed must have a deadline")
	require.Eventually(t, func() bool {
		return validationQueue.calls.Load() >= 3 && importRepo.calls.Load() >= 2
	}, 250*time.Millisecond, time.Millisecond, "a timed-out call must not stop later dispatcher ticks")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	shutdownAt := time.Now()
	cleanup(shutdownCtx)
	require.Less(t, time.Since(shutdownAt), 50*time.Millisecond, "shutdown must cancel the active call")
}

type adminResourceBulkCleanupQueue struct {
	released atomic.Bool
}

func (*adminResourceBulkCleanupQueue) EnqueueAdminResourceBulk(context.Context, coreapp.AdminResourceBulkTask) (bool, error) {
	return false, nil
}

func (*adminResourceBulkCleanupQueue) RefreshAdminResourceBulk(context.Context, coreapp.AdminResourceBulkTask) (bool, error) {
	return false, nil
}

func (q *adminResourceBulkCleanupQueue) ReleaseAdminResourceBulk(context.Context, coreapp.AdminResourceBulkTask) error {
	q.released.Store(true)
	return nil
}

func TestAdminResourceBulkCleanupReleasesExhaustedLease(t *testing.T) {
	queue := &adminResourceBulkCleanupQueue{}
	module := &CoreModule{AdminBulk: coreapp.NewAdminResourceBulkService(nil, queue, nil)}

	cleanupAdminResourceBulk(context.Background(), module, coreapp.AdminResourceBulkTask{
		BatchID: "batch", ClaimToken: "claim", RequestFingerprint: "fingerprint", CommandID: 42,
	})

	require.True(t, queue.released.Load())
}
