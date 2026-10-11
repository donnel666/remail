package icloud

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newOwnedICloudBatchTestService(t *testing.T) *Service {
	t.Helper()
	db := newAdminICloudCommandTestDB(t, t.Name())
	require.NoError(t, db.AutoMigrate(&iCloudAdminTestUser{}, &iCloudAdminTestGroup{}, &iCloudAdminTestPhone{}))
	require.NoError(t, db.Create(&iCloudAdminTestUser{ID: 7, Email: "owner@example.test", Role: "supplier", Status: "active"}).Error)
	return NewService(db, nil, nil)
}

func TestOwnedICloudFilterBatchReplayKeepsOriginalResult(t *testing.T) {
	for _, command := range []AdminICloudCommand{AdminICloudDelete, AdminICloudPublish, AdminICloudValidate} {
		t.Run(string(command), func(t *testing.T) {
			s := newOwnedICloudBatchTestService(t)
			now := s.now().UTC()
			createAdminICloudCommandResource(t, s.db, now, iCloudResourceModel{ID: 1, PrimaryEmail: "first@example.test", Status: iCloudResourceNormal, CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour)})
			ctx := context.Background()
			filter := AdminICloudResourceListFilter{Suffix: "example.test"}
			first, err := s.ApplyOwnedICloudBatchByFilter(ctx, 7, command, filter, "same-request", "first", "/test")
			require.NoError(t, err)
			require.Equal(t, []uint{1}, first.AffectedResourceIDs)
			createAdminICloudCommandResource(t, s.db, now, iCloudResourceModel{ID: 2, PrimaryEmail: "new@example.test", Status: iCloudResourceNormal, CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour)})
			replay, err := s.ApplyOwnedICloudBatchByFilter(ctx, 7, command, filter, "same-request", "retry", "/test")
			require.NoError(t, err)
			require.Equal(t, first, replay)
			var fresh iCloudResourceModel
			require.NoError(t, s.db.First(&fresh, 2).Error)
			require.Equal(t, iCloudResourceNormal, fresh.Status)
			require.False(t, fresh.ForSale)
			require.EqualValues(t, 1, fresh.ValidationGeneration)
			var root iCloudRootModel
			require.NoError(t, s.db.First(&root, 2).Error)
			require.EqualValues(t, 1, root.Version)
		})
	}
}

func TestOwnedICloudFilterBatchRecordsEmptyResultAndWholeRequest(t *testing.T) {
	s := newOwnedICloudBatchTestService(t)
	ctx := context.Background()
	filter := AdminICloudResourceListFilter{Suffix: "example.test"}
	first, err := s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudDelete, filter, "empty-request", "first", "/test")
	require.NoError(t, err)
	require.Zero(t, first.Requested)
	now := s.now().UTC()
	createAdminICloudCommandResource(t, s.db, now, iCloudResourceModel{ID: 1, PrimaryEmail: "new@example.test", Status: iCloudResourceNormal, CredentialRevision: 1, ExpireAt: now.Add(time.Hour)})
	replay, err := s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudDelete, filter, "empty-request", "retry", "/test")
	require.NoError(t, err)
	require.Equal(t, first, replay)
	for _, mutate := range []func(*AdminICloudResourceListFilter){
		func(f *AdminICloudResourceListFilter) { f.Suffix = "other.test" },
		func(f *AdminICloudResourceListFilter) { f.Search = "new" },
		func(f *AdminICloudResourceListFilter) { f.Status = iCloudResourceNormal },
		func(f *AdminICloudResourceListFilter) { value := false; f.ForSale = &value },
		func(f *AdminICloudResourceListFilter) { f.CreatedFrom = &now },
		func(f *AdminICloudResourceListFilter) { f.CreatedTo = &now },
	} {
		changed := filter
		mutate(&changed)
		_, err := s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudDelete, changed, "empty-request", "changed", "/test")
		require.ErrorIs(t, err, ErrICloudImportConflict)
	}
	_, err = s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudPublish, filter, "empty-request", "changed-command", "/test")
	require.ErrorIs(t, err, ErrICloudImportConflict)
	_, err = s.ApplyOwnedICloudBatch(ctx, 7, AdminICloudDelete, []OwnedICloudCommandItem{{ID: 1, Version: 1}}, "empty-request", "changed-mode", "/test")
	require.ErrorIs(t, err, ErrICloudImportConflict)
	var resource iCloudResourceModel
	require.NoError(t, s.db.First(&resource, 1).Error)
	require.Equal(t, iCloudResourceNormal, resource.Status)
}

func TestOwnedICloudIDBatchReplayRejectsChangedSelection(t *testing.T) {
	s := newOwnedICloudBatchTestService(t)
	now := s.now().UTC()
	for id := uint(1); id <= 3; id++ {
		createAdminICloudCommandResource(t, s.db, now, iCloudResourceModel{ID: id, Status: iCloudResourceNormal, CredentialRevision: 1, ExpireAt: now.Add(time.Hour)})
	}
	ctx := context.Background()
	items := []OwnedICloudCommandItem{{ID: 2, Version: 1}, {ID: 1, Version: 1}}
	first, err := s.ApplyOwnedICloudBatch(ctx, 7, AdminICloudDelete, items, "selected-request", "first", "/test")
	require.NoError(t, err)
	replay, err := s.ApplyOwnedICloudBatch(ctx, 7, AdminICloudDelete, []OwnedICloudCommandItem{items[1], items[0]}, "selected-request", "reordered", "/test")
	require.NoError(t, err)
	require.Equal(t, first, replay)
	_, err = s.ApplyOwnedICloudBatch(ctx, 7, AdminICloudDelete, []OwnedICloudCommandItem{{ID: 3, Version: 1}}, "selected-request", "changed-selection", "/test")
	require.ErrorIs(t, err, ErrICloudImportConflict)
	_, err = s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudDelete, AdminICloudResourceListFilter{}, "selected-request", "changed-mode", "/test")
	require.ErrorIs(t, err, ErrICloudImportConflict)
	var untouched iCloudResourceModel
	require.NoError(t, s.db.First(&untouched, 3).Error)
	require.Equal(t, iCloudResourceNormal, untouched.Status)
}

func TestOwnedICloudBatchRollsBackBeforeDispatchingValidation(t *testing.T) {
	s := newOwnedICloudBatchTestService(t)
	now := s.now().UTC()
	for id := uint(1); id <= 2; id++ {
		createAdminICloudCommandResource(t, s.db, now, iCloudResourceModel{ID: id, Status: iCloudResourceNormal, CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour)})
	}
	redisServer := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: redisServer.Addr()}
	s.queue = asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() { _ = inspector.Close(); _ = s.queue.Close() })
	failSecond := true
	require.NoError(t, s.db.Callback().Create().Before("gorm:create").Register("owned_batch_fail_second", func(tx *gorm.DB) {
		if log, ok := tx.Statement.Dest.(*governanceinfra.OperationLogModel); ok && log.ResourceID == "2" {
			pending, err := inspector.ListPendingTasks(platform.QueueBackgroundICloudValidation)
			if !errors.Is(err, asynq.ErrQueueNotFound) {
				require.NoError(t, err)
			}
			require.Empty(t, pending, "must not dispatch before the batch commits")
			if failSecond {
				_ = tx.AddError(errors.New("injected audit write failure"))
			}
		}
	}))
	t.Cleanup(func() { _ = s.db.Callback().Create().Remove("owned_batch_fail_second") })
	ctx := context.Background()
	items := []OwnedICloudCommandItem{{ID: 1, Version: 1}, {ID: 2, Version: 1}}
	_, err := s.ApplyOwnedICloudBatch(ctx, 7, AdminICloudValidate, items, "rollback-request", "first", "/test")
	require.Error(t, err)
	var resources []iCloudResourceModel
	require.NoError(t, s.db.Order("id").Find(&resources).Error)
	for _, resource := range resources {
		require.Equal(t, iCloudResourceNormal, resource.Status)
		require.EqualValues(t, 1, resource.ValidationGeneration)
	}
	var receipts int64
	require.NoError(t, s.db.Model(&coreinfra.AdminResourceCommandReceiptModel{}).Count(&receipts).Error)
	require.Zero(t, receipts)
	failSecond = false
	result, err := s.ApplyOwnedICloudBatch(ctx, 7, AdminICloudValidate, items, "rollback-request", "retry", "/test")
	require.NoError(t, err)
	require.Equal(t, []uint{1, 2}, result.AffectedResourceIDs)
	pending, err := inspector.ListPendingTasks(platform.QueueBackgroundICloudValidation)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, typeICloudValidationDispatcher, pending[0].Type)
}
