package api

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type allocationTaskQueueStub struct {
	candidateCalls atomic.Int32
	inventoryCalls atomic.Int32
}

func (*allocationTaskQueueStub) EnqueueCandidateRefresh(context.Context, allocapp.CandidateRefreshTask) (bool, error) {
	return false, nil
}

func (q *allocationTaskQueueStub) EnqueueCandidateRefreshDispatcher(context.Context, time.Duration) error {
	q.candidateCalls.Add(1)
	return nil
}

func (q *allocationTaskQueueStub) EnqueueInventoryRefresh(context.Context) error {
	q.inventoryCalls.Add(1)
	return nil
}

type allocationBackgroundGateStub struct {
	admitted bool
	released atomic.Bool
}

func (g *allocationBackgroundGateStub) TryAcquire() (func(), bool) {
	return func() { g.released.Store(true) }, g.admitted
}

func TestInventoryRefreshAdmissionDenialDefersTask(t *testing.T) {
	gate := &allocationBackgroundGateStub{}
	module := &Module{
		UseCase:             allocapp.NewUseCase(nil),
		BackgroundExecution: gate,
	}
	mux := asynq.NewServeMux()
	cleanup := RegisterAllocationTaskHandlers(mux, module)
	t.Cleanup(func() { cleanup(context.Background()) })

	err := mux.ProcessTask(context.Background(), asynq.NewTask(allocinfra.TypeInventoryRefresh, nil))

	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.False(t, gate.released.Load())
}

func TestAllocationTaskSeedersStopOnCleanup(t *testing.T) {
	queue := &allocationTaskQueueStub{}
	module := &Module{UseCase: allocapp.NewUseCase(nil, queue)}
	cleanup := startAllocationTaskSeeders(module, 2*time.Millisecond, 2*time.Millisecond)
	require.Eventually(t, func() bool {
		return queue.candidateCalls.Load() > 0 && queue.inventoryCalls.Load() > 0
	}, 100*time.Millisecond, time.Millisecond)
	cleanup(context.Background())
	candidateCalls := queue.candidateCalls.Load()
	inventoryCalls := queue.inventoryCalls.Load()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, candidateCalls, queue.candidateCalls.Load())
	require.Equal(t, inventoryCalls, queue.inventoryCalls.Load())
}
