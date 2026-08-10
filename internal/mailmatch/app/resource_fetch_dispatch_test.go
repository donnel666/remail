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
	resourceTaskRepository
	pending    []domain.ResourceFetchJob
	processing int
	limit      int
}

func (s *resourceFetchDispatchRepoStub) ListPendingResourceFetches(_ context.Context, limit int) ([]domain.ResourceFetchJob, error) {
	s.limit = limit
	return s.pending, nil
}

func (s *resourceFetchDispatchRepoStub) MarkResourceFetchProcessing(context.Context, uint, uint64) (bool, error) {
	s.processing++
	return true, nil
}

func (*resourceFetchDispatchRepoStub) AssertResourceFetchFence(context.Context, uint, uint64, uint64) error {
	return nil
}

func (*resourceFetchDispatchRepoStub) CompleteResourceFetch(context.Context, uint, uint64, uint64, string, int, int, int, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*resourceFetchDispatchRepoStub) AssertICloudResourceFetchFence(context.Context, uint, uint64) error {
	return nil
}

func (*resourceFetchDispatchRepoStub) CompleteICloudResourceFetch(context.Context, uint, uint64, int, int, int, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*resourceFetchDispatchRepoStub) CompleteResourceFetchTask(context.Context, uint, uint64, time.Time, *governancedomain.SystemLog) error {
	return nil
}

type resourceFetchDispatchQueueStub struct {
	accepted       bool
	adminTasks     []AdminResourceFetchTask
	historyTasks   []ResourceHistoryTask
	adminWakeups   int
	historyWakeups int
}

func (s *resourceFetchDispatchQueueStub) EnqueueAdminResourceFetch(_ context.Context, task AdminResourceFetchTask) (bool, error) {
	s.adminTasks = append(s.adminTasks, task)
	return s.accepted, nil
}

func (s *resourceFetchDispatchQueueStub) EnqueueResourceHistory(_ context.Context, task ResourceHistoryTask) (bool, error) {
	s.historyTasks = append(s.historyTasks, task)
	return true, nil
}

func (s *resourceFetchDispatchQueueStub) EnqueueAdminResourceFetchDispatcher(context.Context, time.Duration) error {
	s.adminWakeups++
	return nil
}

func (s *resourceFetchDispatchQueueStub) EnqueueResourceHistoryDispatcher(context.Context, time.Duration) error {
	s.historyWakeups++
	return nil
}

func TestResourceFetchMarksProcessingOnlyAfterAcceptedEnqueue(t *testing.T) {
	repo := &resourceFetchDispatchRepoStub{pending: []domain.ResourceFetchJob{{ResourceID: 100, Generation: 4}}}
	queue := &resourceFetchDispatchQueueStub{}
	uc := NewAdminResourceFetchUseCase(repo, queue, nil, nil, nil)
	result, err := uc.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, result.Queued)
	require.Zero(t, repo.processing)
	require.Len(t, queue.adminTasks, 1)
	require.Empty(t, queue.historyTasks)

	queue.accepted = true
	result, err = uc.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, result.Queued)
	require.Equal(t, 1, repo.processing)
}

func TestResourceFetchDispatchersUseIndependentRepositoriesAndQueues(t *testing.T) {
	adminRepo := &resourceFetchDispatchRepoStub{pending: []domain.ResourceFetchJob{{ResourceID: 100, Generation: 1, Kind: domain.ResourceFetchJobFetch}}}
	historyRepo := &resourceFetchDispatchRepoStub{pending: []domain.ResourceFetchJob{{ResourceID: 200, Generation: 2, Kind: domain.ResourceFetchJobHistory}}}
	queue := &resourceFetchDispatchQueueStub{accepted: true}
	admin := NewAdminResourceFetchUseCase(adminRepo, queue, nil, nil, nil)
	history := NewResourceHistoryUseCase(historyRepo, queue, nil, nil)

	adminResult, err := admin.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, adminResult.Attempted)
	require.Equal(t, 1, adminResult.Queued)
	require.Equal(t, []AdminResourceFetchTask{{ResourceID: 100, Generation: 1}}, queue.adminTasks)
	require.Empty(t, queue.historyTasks)

	historyResult, err := history.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, historyResult.Attempted)
	require.Equal(t, 1, historyResult.Queued)
	require.Equal(t, []ResourceHistoryTask{{ResourceID: 200, Generation: 2}}, queue.historyTasks)
}

func TestResourceFetchDefaultDispatchLimitIsTenThousand(t *testing.T) {
	repo := &resourceFetchDispatchRepoStub{}
	uc := NewAdminResourceFetchUseCase(repo, &resourceFetchDispatchQueueStub{}, nil, nil, nil)

	_, err := uc.DispatchPending(context.Background(), 0)

	require.NoError(t, err)
	require.Equal(t, 10000, repo.limit)
}

type resourceFetchProcessRepoStub struct {
	AdminResourceFetchRepository
	job       domain.ResourceFetchJob
	scope     domain.ResourceFetchScope
	completed bool
	fenced    int
	fetched   int
	stored    int
	matched   int
}

func (*resourceFetchProcessRepoStub) MarkResourceFetchProcessing(context.Context, uint, uint64) (bool, error) {
	return true, nil
}

func (s *resourceFetchProcessRepoStub) FindResourceFetch(context.Context, uint, uint64) (*domain.ResourceFetchJob, error) {
	return &s.job, nil
}

func (s *resourceFetchProcessRepoStub) LoadResourceFetchScope(context.Context, uint, uint64, domain.ResourceType) (*domain.ResourceFetchScope, error) {
	return &s.scope, nil
}

func (*resourceFetchProcessRepoStub) MarkResourceFetchFailure(context.Context, uint, uint64, string, bool, time.Time, *governancedomain.SystemLog) (bool, error) {
	return false, nil
}

func (s *resourceFetchProcessRepoStub) CompleteResourceFetchTask(context.Context, uint, uint64, time.Time, *governancedomain.SystemLog) error {
	s.completed = true
	return nil
}

func (s *resourceFetchProcessRepoStub) AssertICloudResourceFetchFence(context.Context, uint, uint64) error {
	s.fenced++
	return nil
}

func (s *resourceFetchProcessRepoStub) CompleteICloudResourceFetch(_ context.Context, _ uint, _ uint64, fetched, stored, matched int, _ time.Time, _ *governancedomain.SystemLog) error {
	s.completed = true
	s.fetched, s.stored, s.matched = fetched, stored, matched
	return nil
}

type resourceFetchTransportStub struct{ request FetchMessagesRequest }

func (s *resourceFetchTransportStub) FetchMicrosoftMessages(_ context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
	s.request = request
	return nil, &MailFetchFailure{SafeMessage: "stop after request capture", Retryable: true}
}

func TestResourceFetchIgnoresLegacyLookbackForUnlimitedAdministratorChannel(t *testing.T) {
	legacySinceAt := time.Now().Add(-90 * 24 * time.Hour)
	untilAt := time.Now()
	repo := &resourceFetchProcessRepoStub{
		job: domain.ResourceFetchJob{
			ID: 1, ResourceID: 100, Generation: 2, ExpectedCredentialRevision: 3,
			SinceAt: &legacySinceAt, UntilAt: &untilAt,
		},
		scope: domain.ResourceFetchScope{
			ResourceID: 100, EmailAddress: "owner@example.test", ClientID: "client-id",
			RefreshToken: "refresh-token", CredentialRevision: 3,
		},
	}
	transport := &resourceFetchTransportStub{}
	uc := NewAdminResourceFetchUseCase(repo, nil, transport, nil, nil)

	require.NoError(t, uc.Process(context.Background(), AdminResourceFetchTask{ResourceID: 100, Generation: 2}))
	require.True(t, transport.request.FullHistory)
	require.True(t, transport.request.SinceAt.IsZero())
	require.Equal(t, untilAt, transport.request.UntilAt)
}

type iCloudPurchaseFetchStub struct {
	orderNo    string
	resourceID uint
	fenceCalls int
}

func (s *iCloudPurchaseFetchStub) FetchICloudMail(_ context.Context, orderNo string) error {
	s.orderNo = orderNo
	return nil
}

func (s *iCloudPurchaseFetchStub) FetchICloudResourceMailWithFence(ctx context.Context, resourceID uint, fence func(context.Context) error) (int, int, int, error) {
	s.resourceID = resourceID
	s.fenceCalls++
	if err := fence(ctx); err != nil {
		return 0, 0, 0, err
	}
	return 3, 2, 1, nil
}

func TestResourceFetchUsesExistingICloudPickupFetcher(t *testing.T) {
	repo := &resourceFetchProcessRepoStub{
		job: domain.ResourceFetchJob{
			ID: 1, ResourceType: domain.ResourceTypeICloud, ResourceID: 100,
			Generation: 2, ExpectedCredentialRevision: 3,
		},
		scope: domain.ResourceFetchScope{
			ResourceID: 100, ResourceType: domain.ResourceTypeICloud,
			OrderNo: "icloud-order", CredentialRevision: 3,
		},
	}
	purchase := &iCloudPurchaseFetchStub{}
	messages := NewUseCase(nil, nil, nil, nil)
	messages.SetICloudPurchaseFetchPort(purchase)
	uc := NewAdminResourceFetchUseCase(repo, nil, nil, messages, nil)

	require.NoError(t, uc.Process(context.Background(), AdminResourceFetchTask{ResourceID: 100, Generation: 2}))
	require.Equal(t, uint(100), purchase.resourceID)
	require.Empty(t, purchase.orderNo)
	require.Equal(t, 1, purchase.fenceCalls)
	require.Equal(t, 1, repo.fenced)
	require.True(t, repo.completed)
	require.Equal(t, 3, repo.fetched)
	require.Equal(t, 2, repo.stored)
	require.Equal(t, 1, repo.matched)
}

type resourceFetchReleaseRepoStub struct {
	AdminResourceFetchRepository
}

func (*resourceFetchReleaseRepoStub) ReleaseResourceFetchInfrastructureFailure(context.Context, uint, uint64, string, *governancedomain.SystemLog) (bool, error) {
	return true, nil
}

func TestResourceFetchInfrastructureReleaseDoesNotAlsoRetryAsynqTask(t *testing.T) {
	uc := NewAdminResourceFetchUseCase(&resourceFetchReleaseRepoStub{}, &resourceFetchDispatchQueueStub{}, nil, nil, nil)

	err := uc.task.releaseResourceFetchInfrastructure(context.Background(), 1, 2, errors.New("database timeout"))

	require.NoError(t, err)
}

type resourceFetchSubmitRepoStub struct {
	AdminResourceFetchRepository
	job domain.ResourceFetchJob
}

func (s *resourceFetchSubmitRepoStub) CreateOrReuseResourceFetch(_ context.Context, job *domain.ResourceFetchJob, _ *governancedomain.OperationLog) (bool, error) {
	s.job = *job
	s.job.Generation = 1
	s.job.CreatedAt = time.Now().UTC()
	s.job.UpdatedAt = s.job.CreatedAt
	*job = s.job
	return false, nil
}

func (*resourceFetchSubmitRepoStub) CompleteResourceFetchTask(context.Context, uint, uint64, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func TestResourceHistorySubmissionUsesIndependentHistoryLifecycle(t *testing.T) {
	repo := &resourceFetchSubmitRepoStub{}
	queue := &resourceFetchDispatchQueueStub{}
	uc := NewResourceHistoryUseCase(repo, queue, nil, nil)

	result, err := uc.Submit(context.Background(), ResourceHistorySubmitCommand{
		ResourceID:     10,
		OperatorUserID: 1,
		IdempotencyKey: "resource-history",
	})

	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobHistory, result.Job.Kind)
	require.Equal(t, domain.ResourceTypeMicrosoft, result.Job.ResourceType)
	require.Zero(t, queue.adminWakeups)
	require.Equal(t, 1, queue.historyWakeups)
}

func TestResourceFetchDefaultsICloudSubmissionToForegroundFetch(t *testing.T) {
	repo := &resourceFetchSubmitRepoStub{}
	queue := &resourceFetchDispatchQueueStub{}
	uc := NewAdminResourceFetchUseCase(repo, queue, nil, nil, nil)

	result, err := uc.Submit(context.Background(), AdminResourceFetchSubmitCommand{
		ResourceType:   domain.ResourceTypeICloud,
		ResourceID:     11,
		OperatorUserID: 1,
		IdempotencyKey: "icloud-fetch",
	})

	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobFetch, result.Job.Kind)
	require.Equal(t, domain.ResourceTypeICloud, result.Job.ResourceType)
	require.Equal(t, 1, queue.adminWakeups)
	require.Zero(t, queue.historyWakeups)
}

func TestResourceFetchKeepsPermanentMicrosoftFailureCategory(t *testing.T) {
	for _, category := range []string{
		"oauth_invalid_grant", "oauth_client", "oauth_permission", "mfa", "passkey", "phone", "password",
		"unknown_mailbox", "locked", "account_abnormal", "graph_unauthorized", "graph_forbidden", "imap_auth_failed", "identity_mismatch",
	} {
		require.Equal(t, category, safeResourceFetchCategory(category), category)
	}
}

func TestResourceFetchKeepsPermanentMicrosoftFailureSafeMessage(t *testing.T) {
	safe, category, retryable := classifyResourceFetchFailure(&MailFetchFailure{
		Category: "oauth_invalid_grant", SafeMessage: "Microsoft refresh token is invalid or expired.",
	})

	require.Equal(t, "Microsoft refresh token is invalid or expired.", safe)
	require.Equal(t, "oauth_invalid_grant", category)
	require.False(t, retryable)
}
