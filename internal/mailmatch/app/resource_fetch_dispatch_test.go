package app

import (
	"context"
	"errors"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type resourceFetchDispatchRepoStub struct {
	ResourceFetchRepository
	pending    []domain.ResourceFetchJob
	processing int
}

func (s *resourceFetchDispatchRepoStub) ListPendingResourceFetches(context.Context, int) ([]domain.ResourceFetchJob, error) {
	return s.pending, nil
}

func (s *resourceFetchDispatchRepoStub) MarkResourceFetchProcessing(context.Context, uint, uint64) (bool, error) {
	s.processing++
	return true, nil
}

type resourceFetchDispatchQueueStub struct{ accepted bool }

func (s resourceFetchDispatchQueueStub) EnqueueResourceFetch(context.Context, ResourceFetchTask) (bool, error) {
	return s.accepted, nil
}

func (resourceFetchDispatchQueueStub) EnqueueFetchDispatcher(context.Context, time.Duration) error {
	return nil
}

func TestResourceFetchMarksProcessingOnlyAfterAcceptedEnqueue(t *testing.T) {
	repo := &resourceFetchDispatchRepoStub{pending: []domain.ResourceFetchJob{{ResourceID: 100, Generation: 4}}}
	uc := NewResourceFetchUseCase(repo, resourceFetchDispatchQueueStub{}, nil, nil, nil)
	result, err := uc.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, result.Queued)
	require.Zero(t, repo.processing)

	uc.queue = resourceFetchDispatchQueueStub{accepted: true}
	result, err = uc.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, result.Queued)
	require.Equal(t, 1, repo.processing)
}

type resourceFetchReleaseRepoStub struct {
	ResourceFetchRepository
}

func (*resourceFetchReleaseRepoStub) ReleaseResourceFetchInfrastructureFailure(context.Context, uint, uint64, string, *governancedomain.SystemLog) (bool, error) {
	return true, nil
}

func TestResourceFetchInfrastructureReleaseDoesNotAlsoRetryAsynqTask(t *testing.T) {
	uc := NewResourceFetchUseCase(&resourceFetchReleaseRepoStub{}, resourceFetchDispatchQueueStub{}, nil, nil, nil)

	err := uc.releaseResourceFetchInfrastructure(context.Background(), 1, 2, errors.New("database timeout"))

	require.NoError(t, err)
}
