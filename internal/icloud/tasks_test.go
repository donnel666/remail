package icloud

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestICloudValidationUsesDedicatedQueue(t *testing.T) {
	for _, test := range []struct {
		name    string
		enqueue func(*Service) (bool, error)
	}{
		{
			name: "dispatcher",
			enqueue: func(service *Service) (bool, error) {
				return true, service.ScheduleICloudValidationDispatcher(context.Background(), 0)
			},
		},
		{
			name: "worker",
			enqueue: func(service *Service) (bool, error) {
				return service.enqueueICloudValidation(context.Background(), iCloudValidationTask{
					ResourceID: 1, OwnerUserID: 2, ValidationGeneration: 3, ExpectedCredentialRevision: 4,
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			redisServer := miniredis.RunT(t)
			redisOpt := asynq.RedisClientOpt{Addr: redisServer.Addr()}
			queue := asynq.NewClient(redisOpt)
			inspector := asynq.NewInspector(redisOpt)
			t.Cleanup(func() {
				_ = inspector.Close()
				_ = queue.Close()
			})

			queued, err := test.enqueue(NewService(nil, queue, nil))
			if err != nil || !queued {
				t.Fatalf("enqueue iCloud %s task: queued=%v err=%v", test.name, queued, err)
			}
			queues, err := inspector.Queues()
			if err != nil {
				t.Fatalf("list queues: %v", err)
			}
			if len(queues) != 1 || queues[0] != platform.QueueBackgroundICloudValidation {
				t.Fatalf("iCloud %s task routed to unexpected queues: %#v", test.name, queues)
			}
		})
	}
}

func TestLegacyICloudValidationTasksMoveToDedicatedQueue(t *testing.T) {
	for _, test := range []struct {
		name     string
		taskType string
		run      func(context.Context, *Service) error
	}{
		{
			name:     "dispatcher",
			taskType: typeICloudValidationDispatcher,
			run: func(ctx context.Context, service *Service) error {
				return handleICloudValidationDispatcher(ctx, service, platform.QueueDefault)
			},
		},
		{
			name:     "worker",
			taskType: typeICloudValidation,
			run: func(ctx context.Context, service *Service) error {
				return handleICloudValidationTask(ctx, service, iCloudValidationTask{
					ResourceID: 1, OwnerUserID: 2, ValidationGeneration: 3, ExpectedCredentialRevision: 4,
				}, platform.QueueBackgroundValidation)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			redisServer := miniredis.RunT(t)
			redisOpt := asynq.RedisClientOpt{Addr: redisServer.Addr()}
			queue := asynq.NewClient(redisOpt)
			inspector := asynq.NewInspector(redisOpt)
			t.Cleanup(func() {
				_ = inspector.Close()
				_ = queue.Close()
			})

			require.NoError(t, test.run(context.Background(), NewService(nil, queue, nil)))
			queues, err := inspector.Queues()
			require.NoError(t, err)
			require.Equal(t, []string{platform.QueueBackgroundICloudValidation}, queues)
			pending, err := inspector.ListPendingTasks(platform.QueueBackgroundICloudValidation)
			require.NoError(t, err)
			scheduled, err := inspector.ListScheduledTasks(platform.QueueBackgroundICloudValidation)
			require.NoError(t, err)
			tasks := append(pending, scheduled...)
			require.Len(t, tasks, 1)
			require.Equal(t, test.taskType, tasks[0].Type)
		})
	}
}

func TestICloudOnboardingDispatcherDelayDoesNotBlockImmediateDispatch(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisOpt := asynq.RedisClientOpt{Addr: redisServer.Addr()}
	queue := asynq.NewClient(redisOpt)
	inspector := asynq.NewInspector(redisOpt)
	t.Cleanup(func() {
		_ = inspector.Close()
		_ = queue.Close()
	})

	service := NewService(nil, queue, nil)
	require.NoError(t, service.ScheduleICloudOnboardingDispatcher(context.Background(), 5*time.Minute))
	require.NoError(t, service.ScheduleICloudOnboardingDispatcher(context.Background(), 0))

	pending, err := inspector.ListPendingTasks(platform.QueueBackgroundICloudValidation)
	require.NoError(t, err)
	scheduled, err := inspector.ListScheduledTasks(platform.QueueBackgroundICloudValidation)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Len(t, scheduled, 1)
	require.Equal(t, typeICloudOnboardingDispatcher, pending[0].Type)
	require.Equal(t, typeICloudOnboardingDispatcher, scheduled[0].Type)
}
