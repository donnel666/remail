package gmail

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestGmailValidationAndIdentificationUseDedicatedQueues(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	queue := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, queue.Close())
	})
	service := NewService(nil, queue)
	ctx := context.Background()

	require.NoError(t, service.scheduleAdminLocalResourceMaintenance(ctx, AdminLocalResourceValidate))
	require.NoError(t, service.enqueueLocalGmailValidationBatch(ctx, localGmailValidationBatchTask{
		BatchID: "validation-batch", ClaimToken: "claim",
	}, 0))
	require.NoError(t, service.enqueueLocalResourceValidation(ctx, localResourceValidationTask{
		ResourceID: 1, OwnerUserID: 2, ValidationGeneration: 3, ExpectedCredentialRevision: 4,
	}))

	require.NoError(t, service.scheduleAdminLocalResourceMaintenance(ctx, AdminLocalResourceHistory))
	require.NoError(t, service.enqueueValidatedLocalGmailHistory(ctx, localGmailHistoryTask{
		ResourceID: 1, OwnerUserID: 2, ValidationGeneration: 3, ExpectedCredentialRevision: 4,
	}))
	accepted, err := service.enqueueLocalGmailProjectHistory(ctx, localGmailProjectHistoryTask{
		ProjectID: 5, Generation: 6,
	})
	require.NoError(t, err)
	require.True(t, accepted)

	queues, err := inspector.Queues()
	require.NoError(t, err)
	require.Contains(t, queues, platform.QueueBackgroundGmailValidation)
	require.Contains(t, queues, platform.QueueBackgroundGmailIdentification)
	for _, shared := range []string{
		platform.QueueDefault,
		platform.QueueResource,
		platform.QueueBackgroundValidation,
		platform.QueueBackgroundProjectHistory,
	} {
		require.NotContains(t, queues, shared)
	}

	validationPending, err := inspector.ListPendingTasks(platform.QueueBackgroundGmailValidation)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{typeGmailValidationDispatcher, typeGmailValidationBatch}, taskTypes(validationPending))
	validationScheduled, err := inspector.ListScheduledTasks(platform.QueueBackgroundGmailValidation)
	require.NoError(t, err)
	require.Equal(t, []string{typeGmailValidateLocal}, taskTypes(validationScheduled))

	identificationPending, err := inspector.ListPendingTasks(platform.QueueBackgroundGmailIdentification)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{typeGmailProjectHistoryDispatcher, typeGmailProjectHistoryScan}, taskTypes(identificationPending))
	identificationScheduled, err := inspector.ListScheduledTasks(platform.QueueBackgroundGmailIdentification)
	require.NoError(t, err)
	require.Equal(t, []string{typeGmailValidatedHistoryScan}, taskTypes(identificationScheduled))
}

func taskTypes(tasks []*asynq.TaskInfo) []string {
	result := make([]string, len(tasks))
	for index, task := range tasks {
		result[index] = task.Type
	}
	return result
}
