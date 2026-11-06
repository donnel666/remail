package api

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type dashboardBackgroundGateStub struct {
	calls int
}

func TestRankingRefreshIntervalUsesRuntimeSetting(t *testing.T) {
	runtimeconfig.Set("ranking_refresh_interval_minutes", "2")
	t.Cleanup(func() { runtimeconfig.Delete("ranking_refresh_interval_minutes") })

	require.Equal(t, 2*time.Minute, rankingRefreshIntervalValue())
}

func (s *dashboardBackgroundGateStub) TryAcquire() (func(), bool) {
	s.calls++
	return func() {}, false
}

func TestDashboardRefreshesUseDedicatedQueueAndDeduplicate(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	enqueueRankingRefresh(context.Background(), client)
	enqueueRankingRefresh(context.Background(), client)
	enqueueAdminDashboardRefresh(context.Background(), client)
	enqueueAdminDashboardRefresh(context.Background(), client)

	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })
	tasks, err := inspector.ListPendingTasks(platform.QueueBackgroundDashboard)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	timeouts := map[string]time.Duration{}
	for _, task := range tasks {
		timeouts[task.Type] = task.Timeout
	}
	require.Equal(t, rankingRefreshTaskTimeout, timeouts[typeRankingRefresh])
	require.Equal(t, adminDashboardRefreshTaskTimeout, timeouts[typeAdminDashboardRefresh])
}

func TestDashboardRefreshDefersWhenAdaptiveBackgroundWindowIsFull(t *testing.T) {
	gate := &dashboardBackgroundGateStub{}
	module := NewModule(nil, nil, nil)
	module.SetDashboardRefreshGate(gate)
	mux := asynq.NewServeMux()
	cleanup := RegisterTaskHandlers(mux, module)
	t.Cleanup(func() { cleanup(context.Background()) })

	err := mux.ProcessTask(context.Background(), asynq.NewTask(typeRankingRefresh, nil))
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.Equal(t, 1, gate.calls)
}

func TestDashboardRefreshUsesDedicatedSlot(t *testing.T) {
	module := NewModule(nil, nil, nil)
	firstRelease, firstAdmitted := acquireDashboardBackground(context.Background(), module)
	require.True(t, firstAdmitted)
	defer firstRelease()
	_, secondAdmitted := acquireDashboardBackground(context.Background(), module)
	require.False(t, secondAdmitted)
}
