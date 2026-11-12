package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
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

func TestBackgroundValidationWindowLimitUsesUnusedCapacity(t *testing.T) {
	gate := &coreBackgroundExecutionGateStub{available: 7, limit: 32}

	require.Equal(t, 7, backgroundValidationWindowLimit(gate, resourceValidationDispatchMaximum))
}

func TestMicrosoftEnterpriseDispatcherWorksWithPersonalWindowFull(t *testing.T) {
	resources := newMockResourceRepo()
	ctx := context.Background()
	for _, entry := range []struct {
		email  string
		status coredomain.MicrosoftResourceStatus
	}{
		{"busy@outlook.com", coredomain.MicrosoftStatusValidating},
		{"ready@tenant.onmicrosoft.com", coredomain.MicrosoftStatusPending},
	} {
		require.NoError(t, resources.CreateMicrosoft(ctx, &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}, &coredomain.MicrosoftResource{
			EmailAddress: entry.email, Status: entry.status, CredentialRevision: 1,
		}))
	}
	queue := &mockValidationQueue{}
	module := &CoreModule{
		ValidationUseCase:   coreapp.NewResourceValidationUseCase(resources, newMockValidationRepo(resources), queue, nil),
		BackgroundExecution: &coreBackgroundExecutionGateStub{available: 1, limit: 1},
	}
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, module)
	require.NoError(t, mux.ProcessTask(ctx, asynq.NewTask(coreinfra.TypeResourceValidationDispatcher, nil)))
	require.Empty(t, queue.tasks)
	require.NoError(t, mux.ProcessTask(ctx, asynq.NewTask(coreinfra.TypeResourceValidationDispatcher, []byte(`{"enterprise":true}`))))
	require.Len(t, queue.tasks, 1)
	require.True(t, queue.tasks[0].Enterprise)
	require.Equal(t, "ready@tenant.onmicrosoft.com", resources.microsoft[queue.tasks[0].ResourceID].EmailAddress)
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

func TestResourceValidationDispatcherSkipsWhenCapacityIsZero(t *testing.T) {
	module := &CoreModule{
		ValidationUseCase:   coreapp.NewResourceValidationUseCase(nil, nil, &mockValidationQueue{}, nil),
		BackgroundExecution: &coreBackgroundExecutionGateStub{available: 0},
	}
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, module)

	err := mux.ProcessTask(context.Background(), asynq.NewTask(coreinfra.TypeResourceValidationDispatcher, nil))

	require.NoError(t, err, "a full pool is not a failed task")
}

func TestValidationDispatcherFillsOnlyFreeFixedSlots(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	asynqClient := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, asynqClient.Close())
		require.NoError(t, client.Close())
	})
	gate := platform.NewFixedBackgroundExecutionGate(10, client, "validation-test", platform.QueueBackgroundValidation)
	ctx := context.Background()
	resources := newMockResourceRepo()
	var activeIDs []string
	const prefix = "asynq:{" + platform.QueueBackgroundValidation + "}:"
	for i := range 12 {
		status := coredomain.MicrosoftStatusPending
		if i < 6 {
			status = coredomain.MicrosoftStatusValidating
			info, err := asynqClient.Enqueue(asynq.NewTask(coreinfra.TypeResourceValidation, nil), asynq.Queue(platform.QueueBackgroundValidation))
			require.NoError(t, err)
			require.NoError(t, client.RPopLPush(ctx, prefix+"pending", prefix+"active").Err())
			require.NoError(t, client.HSet(ctx, prefix+"t:"+info.ID, "state", "active").Err())
			activeIDs = append(activeIDs, info.ID)
		}
		require.NoError(t, resources.CreateMicrosoft(ctx, &coredomain.EmailResource{Type: coredomain.ResourceTypeMicrosoft, OwnerUserID: 1}, &coredomain.MicrosoftResource{
			EmailAddress: fmt.Sprintf("free-slot-%d@outlook.com", i), Password: "secret", Status: status, CredentialRevision: 1,
		}))
	}
	queue := coreinfra.NewResourceValidationQueue(asynqClient, client)
	module := &CoreModule{
		ValidationUseCase:   coreapp.NewResourceValidationUseCase(resources, newMockValidationRepo(resources), queue, nil),
		ValidationExecution: gate,
	}
	zero, err := module.ValidationUseCase.DispatchPending(ctx, 0, false)
	require.NoError(t, err)
	require.Zero(t, zero.Accepted)
	require.Zero(t, client.ZCard(ctx, prefix+"scheduled").Val(), "zero free slots must not turn into a default batch")
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, module)
	dispatcher := asynq.NewTask(coreinfra.TypeResourceValidationDispatcher, nil)
	require.NoError(t, mux.ProcessTask(ctx, dispatcher))
	scheduled, err := inspector.ListScheduledTasks(platform.QueueBackgroundValidation)
	require.NoError(t, err)
	require.Len(t, scheduled, 4, "six active tasks leave four slots to fill")
	require.NoError(t, mux.ProcessTask(ctx, dispatcher))
	require.EqualValues(t, 4, client.ZCard(ctx, prefix+"scheduled").Val())
	resources.microsoft[1].Status = coredomain.MicrosoftStatusNormal
	require.NoError(t, client.LRem(ctx, prefix+"active", 0, activeIDs[0]).Err())
	require.NoError(t, client.Del(ctx, prefix+"t:"+activeIDs[0]).Err())
	require.NoError(t, mux.ProcessTask(ctx, dispatcher))
	scheduled, err = inspector.ListScheduledTasks(platform.QueueBackgroundValidation)
	require.NoError(t, err)
	require.Len(t, scheduled, 5, "one completed task must admit exactly one replacement")
}

func TestLegacyValidationDispatcherMovesOutOfBusinessQueue(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	queue := coreinfra.NewResourceValidationQueue(client, nil)
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, &CoreModule{ValidationUseCase: coreapp.NewResourceValidationUseCase(nil, nil, queue, nil)})
	_, err := client.Enqueue(asynq.NewTask(coreinfra.TypeResourceValidationDispatcher, nil), asynq.Queue(platform.QueueBackgroundValidation))
	require.NoError(t, err)
	workers := asynq.NewServer(options, asynq.Config{
		Concurrency: 1, Queues: map[string]int{platform.QueueBackgroundValidation: 1},
		TaskCheckInterval: 10 * time.Millisecond, ShutdownTimeout: time.Second,
	})
	require.NoError(t, workers.Start(mux))
	t.Cleanup(workers.Shutdown)
	require.Eventually(t, func() bool {
		dispatchers, err := inspector.ListScheduledTasks(platform.QueueDefault)
		if err != nil || len(dispatchers) != 2 {
			return false
		}
		info, err := inspector.GetQueueInfo(platform.QueueBackgroundValidation)
		return err == nil && info.Size == 0
	}, 5*time.Second, 10*time.Millisecond)
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

func (r *deadlineBlockingImportRepo) ClaimAdminImportDispatchable(ctx context.Context, _ int, _, _ time.Time, _ bool) ([]coreapp.AdminResourceImportDispatchItem, error) {
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

func TestDomainSaleQualityCheckIntervalUsesRuntimeSetting(t *testing.T) {
	runtimeconfig.Delete(runtimeconfig.DomainSaleQualityCheckIntervalSecondsKey)
	t.Cleanup(func() { runtimeconfig.Delete(runtimeconfig.DomainSaleQualityCheckIntervalSecondsKey) })

	require.Equal(t, 30*time.Second, domainSaleQualityCheckInterval())
	runtimeconfig.Set(runtimeconfig.DomainSaleQualityCheckIntervalSecondsKey, "7")
	require.Equal(t, 7*time.Second, domainSaleQualityCheckInterval())
	runtimeconfig.Set(runtimeconfig.DomainSaleQualityCheckIntervalSecondsKey, "0")
	require.Equal(t, 30*time.Second, domainSaleQualityCheckInterval())
}

func TestDomainSaleQualityEnforcerReloadsIntervalAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	var intervalNanos atomic.Int64
	intervalNanos.Store(int64(20 * time.Millisecond))

	done := startDomainSaleQualityEnforcer(ctx, time.Second, func() time.Duration {
		return time.Duration(intervalNanos.Load())
	}, func(context.Context) (int, error) {
		if calls.Add(1) == 2 {
			intervalNanos.Store(int64(time.Millisecond))
		}
		return 0, nil
	})

	require.Eventually(t, func() bool { return calls.Load() >= 4 }, 100*time.Millisecond, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("domain sale quality enforcer did not stop")
	}
	stoppedAt := calls.Load()
	time.Sleep(5 * time.Millisecond)
	require.Equal(t, stoppedAt, calls.Load())
}

type adminResourceBulkCleanupQueue struct {
	released atomic.Bool
	failed   atomic.Bool
}

func (*adminResourceBulkCleanupQueue) EnqueueAdminResourceBulk(context.Context, coreapp.AdminResourceBulkTask) (bool, error) {
	return false, nil
}

func (*adminResourceBulkCleanupQueue) RefreshAdminResourceBulk(context.Context, coreapp.AdminResourceBulkTask) (bool, error) {
	return false, nil
}

func (*adminResourceBulkCleanupQueue) RecordAdminResourceBulkPage(context.Context, coreapp.AdminResourceBulkTask, coreapp.AdminResourceBulkPageResult) error {
	return nil
}

func (q *adminResourceBulkCleanupQueue) FailAdminResourceBulk(context.Context, coreapp.AdminResourceBulkTask) error {
	q.failed.Store(true)
	return nil
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
	require.True(t, queue.failed.Load())
}
