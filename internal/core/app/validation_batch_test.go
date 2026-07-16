package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type validationBatchRepoStub struct {
	ResourceValidationRepository
	page   ResourceValidationBatchPageResult
	called bool
}

func (s *validationBatchRepoStub) MarkValidationBatchPending(context.Context, ResourceValidationBatchTask, int) (*ResourceValidationBatchPageResult, error) {
	s.called = true
	return &s.page, nil
}

type validationBatchQueueStub struct {
	ResourceValidationQueue
	owned     bool
	refreshed bool
	released  bool
	enqueued  *ResourceValidationBatchTask
}

func (s *validationBatchQueueStub) EnqueueResourceValidationBatch(_ context.Context, task ResourceValidationBatchTask) error {
	s.enqueued = &task
	return nil
}

func (*validationBatchQueueStub) EnqueueResourceValidation(context.Context, ResourceValidationTask) error {
	return nil
}

func (*validationBatchQueueStub) EnqueueResourceValidationDispatcher(context.Context, time.Duration) error {
	return nil
}

func (s *validationBatchQueueStub) RefreshResourceValidationBatch(context.Context, ResourceValidationBatchTask) (bool, error) {
	s.refreshed = true
	return s.owned, nil
}

func (s *validationBatchQueueStub) ReleaseResourceValidationBatch(context.Context, ResourceValidationBatchTask) error {
	s.released = true
	return nil
}

func TestResourceValidationBatchLeaseControlsCursorLifecycle(t *testing.T) {
	t.Run("stale cursor does not touch database", func(t *testing.T) {
		repo := &validationBatchRepoStub{}
		queue := &validationBatchQueueStub{}
		uc := NewResourceValidationUseCase(nil, repo, queue, nil)

		require.NoError(t, uc.ProcessBatch(context.Background(), ResourceValidationBatchTask{BatchID: "batch", ClaimToken: "old", OwnerUserID: 1}))
		require.True(t, queue.refreshed)
		require.False(t, repo.called)
	})

	t.Run("completed cursor releases lease", func(t *testing.T) {
		repo := &validationBatchRepoStub{page: ResourceValidationBatchPageResult{Done: true}}
		queue := &validationBatchQueueStub{owned: true}
		uc := NewResourceValidationUseCase(nil, repo, queue, nil)

		require.NoError(t, uc.ProcessBatch(context.Background(), ResourceValidationBatchTask{BatchID: "batch", ClaimToken: "token", OwnerUserID: 1}))
		require.True(t, repo.called)
		require.True(t, queue.released)
	})

	t.Run("next page keeps the same claim token", func(t *testing.T) {
		repo := &validationBatchRepoStub{page: ResourceValidationBatchPageResult{AfterID: 1000, ThroughID: 5000}}
		queue := &validationBatchQueueStub{owned: true}
		uc := NewResourceValidationUseCase(nil, repo, queue, nil)

		require.NoError(t, uc.ProcessBatch(context.Background(), ResourceValidationBatchTask{BatchID: "batch", ClaimToken: "token", OwnerUserID: 1}))
		require.NotNil(t, queue.enqueued)
		require.Equal(t, "token", queue.enqueued.ClaimToken)
		require.Equal(t, uint(1000), queue.enqueued.AfterID)
	})
}

func TestValidationCategoryRequiresExplicitTerminalEvidence(t *testing.T) {
	for _, category := range []string{"", "unknown", "request", "code_timeout", "protocol_changed", "rate_limited"} {
		require.True(t, isRetryableValidationCategory(category), category)
	}
	for _, category := range []string{"oauth_invalid_grant", "password", "locked", "unknown_mailbox", "dns"} {
		require.False(t, isRetryableValidationCategory(category), category)
	}
}
