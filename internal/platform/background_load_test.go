package platform

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type queueInfoReaderStub map[string]*asynq.QueueInfo

func (s queueInfoReaderStub) GetQueueInfo(queue string) (*asynq.QueueInfo, error) {
	info, ok := s[queue]
	if !ok {
		return nil, fmt.Errorf("test inspector: %w", asynq.ErrQueueNotFound)
	}
	return info, nil
}

type queueInfoReaderErrorStub struct{}

func (queueInfoReaderErrorStub) GetQueueInfo(string) (*asynq.QueueInfo, error) {
	return nil, fmt.Errorf("inspector unavailable")
}

func TestBackgroundLoadControllerFillsIdleQueue(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"background_validation": {Pending: 10, Active: 2},
	}, nil, 128)

	require.Equal(t, 52, controller.DispatchLimit("background_validation", 8, 100))
}

func TestBackgroundLoadControllerBorrowsIdleShareButKeepsWeightsWhenBothQueuesHaveWork(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"background_validation": {Pending: 10},
		"background_alias":      {Pending: 2},
	}, nil, 128)

	require.Equal(t, 38, controller.DispatchLimit("background_validation", 8, 100))
	require.Equal(t, 14, controller.DispatchLimit("background_alias", 4, 100))
}

func TestBackgroundLoadControllerYieldsToForegroundBacklog(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"default":               {Pending: 128, Active: 20},
		"background_validation": {Pending: 3, Active: 2},
	}, nil, 128)

	require.Zero(t, controller.DispatchLimit("background_validation", 8, 100))
}

func TestBackgroundLoadControllerStopsWhenBusyBudgetIsFull(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"mailtransport":    {Pending: 256, Active: 64},
		"background_alias": {Pending: 8, Active: 1},
	}, nil, 128)

	require.Zero(t, controller.DispatchLimit("background_alias", 8, 100))
}

func TestBackgroundLoadControllerBusyCapIsNotRaisedByDispatcherMinimum(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"default": {Active: 32},
	}, nil, 128)

	require.Equal(t, backgroundBusyDispatchCap, controller.DispatchLimit("background_validation", 8, 100))
}

func TestBackgroundLoadControllerLetsEmptyQueueProbeAfterOtherQueueBorrowedCapacity(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"background_validation": {Pending: backgroundIdleDispatchCap},
	}, nil, 128)

	require.Equal(t, 4, controller.DispatchLimit("background_alias", 4, 100))
}

func TestBackgroundLoadControllerProbeNeverExceedsBusyCap(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"default":               {Active: 32},
		"background_validation": {Pending: backgroundBusyDispatchCap},
	}, nil, 128)

	require.Equal(t, backgroundBusyDispatchCap, controller.DispatchLimit("background_alias", 4, 100))
}

func TestBackgroundLoadControllerYieldsToForegroundHTTPRequests(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"background_alias": {Pending: 5},
	}, nil, 128)
	done := controller.BeginForegroundRequest()

	require.Equal(t, 11, controller.DispatchLimit("background_alias", 8, 100))

	done()
	require.Equal(t, 59, controller.DispatchLimit("background_alias", 8, 100))
}

func TestBackgroundLoadControllerDispatchBudgetDoesNotCountCurrentDispatcher(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{
		"default": {Active: 1},
	}, nil, 128)

	// Direct load inspection still sees the active foreground task.
	require.Equal(t, backgroundModerateDispatchCap, controller.DispatchLimit("background_alias", 4, 100))

	budget, release := controller.AcquireDispatchBudget(context.Background(), "background_alias", 4, 100)
	defer release()
	require.Equal(t, backgroundIdleDispatchCap, budget)
}

func TestBackgroundLoadControllerExecutionAdmissionCaps(t *testing.T) {
	tests := []struct {
		name       string
		queues     queueInfoReaderStub
		concurrent int
		want       int
	}{
		{name: "idle", queues: queueInfoReaderStub{}, concurrent: 128, want: backgroundIdleExecutionCap},
		{name: "moderate", queues: queueInfoReaderStub{"default": {Active: 1}}, concurrent: 128, want: backgroundModerateExecutionCap},
		{name: "busy", queues: queueInfoReaderStub{"default": {Active: 32}}, concurrent: 128, want: backgroundBusyExecutionCap},
		{name: "critical", queues: queueInfoReaderStub{"default": {Active: 64}}, concurrent: 128, want: backgroundCriticalExecutionCap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := NewBackgroundLoadController(nil, tt.queues, nil, tt.concurrent)
			releases := make([]func(), 0, tt.want)
			for range tt.want {
				admitted, release := controller.TryAcquireExecution(context.Background(), "background_alias")
				require.True(t, admitted)
				releases = append(releases, release)
			}
			admitted, _ := controller.TryAcquireExecution(context.Background(), "background_validation")
			require.False(t, admitted)

			if len(releases) > 0 {
				releases[0]()
				releases = releases[1:]
				admitted, release := controller.TryAcquireExecution(context.Background(), "background_validation")
				require.True(t, admitted)
				release()
			}
			for _, release := range releases {
				release()
			}
		})
	}
}

func TestBackgroundLoadControllerMissingQueuesAreTreatedAsEmpty(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{}, nil, 128)

	require.Equal(t, backgroundIdleDispatchCap, controller.DispatchLimit("background_alias", 4, 100))
	for range backgroundIdleExecutionCap {
		admitted, _ := controller.TryAcquireExecution(context.Background(), "background_alias")
		require.True(t, admitted)
	}
}

func TestBackgroundLoadControllerInspectorFailureIsConservative(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderErrorStub{}, nil, 128)

	require.Zero(t, controller.DispatchLimit("background_alias", 4, 100))
	for range backgroundBusyExecutionCap {
		admitted, _ := controller.TryAcquireExecution(context.Background(), "background_alias")
		require.True(t, admitted)
	}
	admitted, _ := controller.TryAcquireExecution(context.Background(), "background_alias")
	require.False(t, admitted)
}

func TestBackgroundLoadControllerSerializesDispatchBudgets(t *testing.T) {
	controller := NewBackgroundLoadController(nil, queueInfoReaderStub{}, nil, 128)
	_, releaseFirst := controller.AcquireDispatchBudget(context.Background(), "background_validation", 8, 100)
	acquiredSecond := make(chan func(), 1)
	go func() {
		_, release := controller.AcquireDispatchBudget(context.Background(), "background_alias", 8, 100)
		acquiredSecond <- release
	}()

	select {
	case <-acquiredSecond:
		t.Fatal("second dispatcher acquired budget before first released it")
	case <-time.After(20 * time.Millisecond):
	}

	releaseFirst()
	select {
	case releaseSecond := <-acquiredSecond:
		releaseSecond()
	case <-time.After(time.Second):
		t.Fatal("second dispatcher did not acquire budget after release")
	}
}
