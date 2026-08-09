package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalGmailValidationBatchUsesRedisCursorAndBumpsRootVersions(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	queueOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	queue := asynq.NewClient(queueOptions)
	inspector := asynq.NewInspector(queueOptions)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, queue.Close())
		require.NoError(t, redisClient.Close())
	})

	db, err := gorm.Open(sqlite.Open("file:gmail-validation-batch?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&resourceRootModel{}, &localResourceModel{}, &gmailMaintenanceRunModel{}, &governanceinfra.OperationLogModel{},
	))
	resourceIDs := make([]uint, localGmailValidationBatchPage+1)
	for i := range resourceIDs {
		root := resourceRootModel{Type: "gmail", OwnerUserID: 7, Version: 1}
		require.NoError(t, db.Create(&root).Error)
		resourceIDs[i] = root.ID
		email := fmt.Sprintf("batch-%03d@gmail.com", i)
		require.NoError(t, db.Create(&localResourceModel{
			ID: root.ID, ResourceType: "gmail", OwnerUserID: 7, Email: email, Identity: email,
			Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
			CredentialRevision: 1, ValidationGeneration: 1, Status: LocalResourceNormal,
		}).Error)
	}

	service := NewService(db, queue)
	service.redis = redisClient
	accepted, err := service.AcceptAdminLocalResourceValidationBatch(
		context.Background(), resourceIDs, 9, "batch-key", "request-batch", "/v1/admin/gmail/resources/validations",
	)
	require.NoError(t, err)
	require.Equal(t, "queued", accepted.Status)
	require.Equal(t, len(resourceIDs), accepted.Requested)

	pending, err := inspector.ListPendingTasks(platform.QueueBackgroundGmailValidation)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var first localGmailValidationBatchTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &first))
	require.NoError(t, service.ProcessLocalResourceValidationBatch(context.Background(), first))

	progress, err := service.GetAdminLocalResourceValidationBatch(context.Background(), accepted.BatchID)
	require.NoError(t, err)
	require.Equal(t, "processing", progress.Status)
	require.Equal(t, localGmailValidationBatchPage, progress.Processed)
	require.Equal(t, localGmailValidationBatchPage, progress.Queued)

	scheduled, err := inspector.ListScheduledTasks(platform.QueueBackgroundGmailValidation)
	require.NoError(t, err)
	require.Len(t, scheduled, 1)
	var last localGmailValidationBatchTask
	require.NoError(t, json.Unmarshal(scheduled[0].Payload, &last))
	require.Equal(t, localGmailValidationBatchPage, last.Cursor)
	require.NoError(t, service.ProcessLocalResourceValidationBatch(context.Background(), last))

	completed, err := service.GetAdminLocalResourceValidationBatch(context.Background(), accepted.BatchID)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Equal(t, len(resourceIDs), completed.Processed)
	require.Equal(t, len(resourceIDs), completed.Queued)
	var pendingCount, versionCount int64
	require.NoError(t, db.Model(&localResourceModel{}).
		Where("status = ? AND validation_generation = ?", LocalResourcePending, 2).Count(&pendingCount).Error)
	require.EqualValues(t, len(resourceIDs), pendingCount)
	require.NoError(t, db.Model(&resourceRootModel{}).Where("version = ?", 2).Count(&versionCount).Error)
	require.EqualValues(t, len(resourceIDs), versionCount)

	replayed, err := service.AcceptAdminLocalResourceValidationBatch(
		context.Background(), resourceIDs, 9, "batch-key", "request-replay", "/v1/admin/gmail/resources/validations",
	)
	require.NoError(t, err)
	require.True(t, replayed.Reused)
	require.Equal(t, accepted.BatchID, replayed.BatchID)
	_, err = service.AcceptAdminLocalResourceValidationBatch(
		context.Background(), resourceIDs[:1], 9, "batch-key", "request-conflict", "/v1/admin/gmail/resources/validations",
	)
	require.ErrorIs(t, err, ErrLocalValidationConflict)
}
