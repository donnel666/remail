package app

import (
	"context"
	"errors"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type projectHistoryJobsStub struct {
	running  ProjectHistoryScanJob
	planned  []ProjectHistoryScanJob
	advanced uint
	matched  bool
	skipped  bool
	failed   uint
	failures int
	retry    bool
	complete bool
}

func (*projectHistoryJobsStub) CreatePlanner(context.Context, uint, string) error { return nil }
func (*projectHistoryJobsStub) EnsureMissingPlanners(context.Context, int) (int, error) {
	return 0, nil
}
func (*projectHistoryJobsStub) ClaimDispatchable(context.Context, int, time.Time, time.Time) ([]ProjectHistoryScanJob, error) {
	return nil, nil
}
func (s *projectHistoryJobsStub) MarkRunning(context.Context, uint, string) (*ProjectHistoryScanJob, bool, error) {
	job := s.running
	return &job, true, nil
}
func (s *projectHistoryJobsStub) PlanShards(_ context.Context, _ uint, _ string, shards []ProjectHistoryScanJob) error {
	s.planned = append(s.planned, shards...)
	return nil
}
func (s *projectHistoryJobsStub) Advance(_ context.Context, _ uint, _ string, resourceID uint, matched, skipped bool, _ string) error {
	s.advanced, s.matched, s.skipped = resourceID, matched, skipped
	return nil
}
func (s *projectHistoryJobsStub) Complete(context.Context, uint, string) error {
	s.complete = true
	return nil
}
func (s *projectHistoryJobsStub) MarkFailure(_ context.Context, _ ProjectHistoryScanJob, resourceID uint, retryable bool, _ string) error {
	s.failures++
	s.failed, s.retry = resourceID, retryable
	return nil
}
func (*projectHistoryJobsStub) ReleaseDispatch(context.Context, uint, string) error { return nil }
func (*projectHistoryJobsStub) MarkDispatchFailed(context.Context, uint, string, string) error {
	return nil
}

type projectHistoryMatchesStub struct {
	scope   *HistoricalProjectScope
	matches []HistoricalProjectMatch
}

func (*projectHistoryMatchesStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *projectHistoryMatchesStub) FindHistoricalProjectScope(context.Context, uint) (*HistoricalProjectScope, error) {
	return s.scope, nil
}
func (s *projectHistoryMatchesStub) FindHistoricalProjectScopeForUpdate(context.Context, uint) (*HistoricalProjectScope, error) {
	return s.scope, nil
}
func (s *projectHistoryMatchesStub) UpsertMicrosoftProjectMatches(_ context.Context, matches []HistoricalProjectMatch) error {
	s.matches = append(s.matches, matches...)
	return nil
}

type projectHistoryQueueStub struct{ dispatches int }

func (*projectHistoryQueueStub) EnqueueProjectHistoryScan(context.Context, ProjectHistoryScanTask) error {
	return nil
}
func (q *projectHistoryQueueStub) EnqueueProjectHistoryDispatcher(context.Context, time.Duration) error {
	q.dispatches++
	return nil
}

type projectHistoryCredentialsStub struct {
	maxID       uint
	resources   []*coreapp.MicrosoftCredentialScope
	rotation    coreapp.MicrosoftFetchRefreshTokenRotation
	rotationErr error
}

func (*projectHistoryCredentialsStub) LockMicrosoftCredentialScope(context.Context, uint) (*coreapp.MicrosoftCredentialScope, error) {
	return nil, nil
}
func (s *projectHistoryCredentialsStub) MaxMicrosoftResourceID(context.Context) (uint, error) {
	return s.maxID, nil
}
func (s *projectHistoryCredentialsStub) FindNextMicrosoftCredentialScope(_ context.Context, afterID, maxID uint) (*coreapp.MicrosoftCredentialScope, error) {
	for _, resource := range s.resources {
		if resource.ResourceID > afterID && resource.ResourceID <= maxID {
			return resource, nil
		}
	}
	return nil, nil
}
func (*projectHistoryCredentialsStub) ApplyMicrosoftTokenRefreshSuccess(context.Context, coreapp.MicrosoftTokenRefreshSuccess) error {
	return nil
}
func (*projectHistoryCredentialsStub) ApplyMicrosoftTokenRefreshFailure(context.Context, coreapp.MicrosoftTokenRefreshFailure) error {
	return nil
}
func (s *projectHistoryCredentialsStub) ApplyMicrosoftFetchRefreshToken(_ context.Context, update coreapp.MicrosoftFetchRefreshTokenRotation) error {
	s.rotation = update
	return s.rotationErr
}

type projectHistoryTransportStub struct {
	request FetchMessagesRequest
	result  *FetchMessagesResult
	err     error
}

func (s *projectHistoryTransportStub) FetchMicrosoftMessages(_ context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
	s.request = request
	if request.OnMessages != nil && s.err == nil {
		request.OnMessages([]FetchedMessage{{
			EmailResourceID: 10, ResourceType: domain.ResourceTypeMicrosoft, Folder: "Inbox",
			Recipients: []string{"main@example.com"}, Sender: "noreply@github.com", ReceivedAt: time.Now().UTC(),
		}})
		request.OnMessages([]FetchedMessage{{
			EmailResourceID: 10, ResourceType: domain.ResourceTypeMicrosoft, Folder: "Junk",
			Recipients: []string{"main@example.com"}, Sender: "noreply@github.com", ReceivedAt: time.Now().UTC(),
		}})
	}
	return s.result, s.err
}

func TestProjectHistoryPlannerCreatesFourDurableShards(t *testing.T) {
	jobs := &projectHistoryJobsStub{running: ProjectHistoryScanJob{ID: 1, ProjectID: 7, Shard: -1, ClaimToken: "claim"}}
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	queue := &projectHistoryQueueStub{}
	credentials := &projectHistoryCredentialsStub{maxID: 10}
	uc := NewProjectHistoryScanUseCase(jobs, matches, queue, nil)
	uc.SetMicrosoftCredentialPort(credentials)

	require.NoError(t, uc.Process(context.Background(), ProjectHistoryScanTask{JobID: 1, DispatchToken: "dispatch"}))
	require.Len(t, jobs.planned, 4)
	require.Equal(t, uint(1), jobs.planned[0].StartResourceID)
	require.Equal(t, uint(3), jobs.planned[0].EndResourceID)
	require.Equal(t, uint(10), jobs.planned[3].EndResourceID)
}

func TestProjectHistoryShardStreamsInboxAndJunkThenAdvances(t *testing.T) {
	jobs := &projectHistoryJobsStub{running: ProjectHistoryScanJob{
		ID: 2, ProjectID: 7, Shard: 0, ClaimToken: "claim", EndResourceID: 100,
	}}
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	queue := &projectHistoryQueueStub{}
	credentials := &projectHistoryCredentialsStub{resources: []*coreapp.MicrosoftCredentialScope{{
		ResourceID: 10, EmailAddress: "main@example.com", ClientID: "client", RefreshToken: "refresh", CredentialRevision: 4,
	}}}
	transport := &projectHistoryTransportStub{result: &FetchMessagesResult{RefreshToken: "rotated"}}
	uc := NewProjectHistoryScanUseCase(jobs, matches, queue, transport)
	uc.SetMicrosoftCredentialPort(credentials)

	require.NoError(t, uc.Process(context.Background(), ProjectHistoryScanTask{JobID: 2, DispatchToken: "dispatch"}))
	require.True(t, transport.request.FullHistory)
	require.NotNil(t, transport.request.OnMessages)
	require.Len(t, matches.matches, 1)
	require.Equal(t, 2, matches.matches[0].EvidenceCount)
	require.Equal(t, uint(10), jobs.advanced)
	require.True(t, jobs.matched)
	require.Equal(t, "rotated", credentials.rotation.RefreshToken)
}

func TestProjectHistoryRetryableFetchFailureDoesNotAdvance(t *testing.T) {
	jobs := &projectHistoryJobsStub{running: ProjectHistoryScanJob{
		ID: 2, ProjectID: 7, Shard: 0, ClaimToken: "claim", EndResourceID: 100,
	}}
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	credentials := &projectHistoryCredentialsStub{resources: []*coreapp.MicrosoftCredentialScope{{
		ResourceID: 10, EmailAddress: "main@example.com", ClientID: "client", RefreshToken: "refresh", CredentialRevision: 4,
	}}}
	transport := &projectHistoryTransportStub{err: &MailFetchFailure{Retryable: true, Cause: errors.New("temporary")}}
	uc := NewProjectHistoryScanUseCase(jobs, matches, &projectHistoryQueueStub{}, transport)
	uc.SetMicrosoftCredentialPort(credentials)

	require.NoError(t, uc.Process(context.Background(), ProjectHistoryScanTask{JobID: 2, DispatchToken: "dispatch"}))
	require.Zero(t, jobs.advanced)
	require.Equal(t, uint(10), jobs.failed)
	require.True(t, jobs.retry)
}

func TestProjectHistoryFetchFailurePersistsRotatedTokenBeforeRetry(t *testing.T) {
	jobs := &projectHistoryJobsStub{running: ProjectHistoryScanJob{
		ID: 2, ProjectID: 7, Shard: 0, ClaimToken: "claim", EndResourceID: 100,
	}}
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	credentials := &projectHistoryCredentialsStub{resources: []*coreapp.MicrosoftCredentialScope{{
		ResourceID: 10, EmailAddress: "main@example.com", ClientID: "client", RefreshToken: "refresh", CredentialRevision: 4,
	}}}
	transport := &projectHistoryTransportStub{err: &MailFetchFailure{
		Retryable: false, RefreshToken: "rotated", Cause: errors.New("forbidden"),
	}}
	uc := NewProjectHistoryScanUseCase(jobs, matches, &projectHistoryQueueStub{}, transport)
	uc.SetMicrosoftCredentialPort(credentials)

	require.NoError(t, uc.Process(context.Background(), ProjectHistoryScanTask{JobID: 2, DispatchToken: "dispatch"}))
	require.Equal(t, "rotated", credentials.rotation.RefreshToken)
	require.Equal(t, uint(10), jobs.failed)
	require.False(t, jobs.retry)
}

func TestProjectHistoryCredentialChangeRetriesWithoutAdvancingCheckpoint(t *testing.T) {
	jobs := &projectHistoryJobsStub{running: ProjectHistoryScanJob{
		ID: 2, ProjectID: 7, Shard: 0, ClaimToken: "claim", EndResourceID: 100,
	}}
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	credentials := &projectHistoryCredentialsStub{
		resources: []*coreapp.MicrosoftCredentialScope{{
			ResourceID: 10, EmailAddress: "main@example.com", ClientID: "client", RefreshToken: "refresh", CredentialRevision: 4,
		}},
		rotationErr: coreapp.ErrMicrosoftCredentialChanged,
	}
	transport := &projectHistoryTransportStub{result: &FetchMessagesResult{RefreshToken: "rotated"}}
	uc := NewProjectHistoryScanUseCase(jobs, matches, &projectHistoryQueueStub{}, transport)
	uc.SetMicrosoftCredentialPort(credentials)

	require.NoError(t, uc.Process(context.Background(), ProjectHistoryScanTask{JobID: 2, DispatchToken: "dispatch"}))
	require.Zero(t, jobs.advanced)
	require.Empty(t, matches.matches)
	require.Equal(t, 1, jobs.failures)
	require.Zero(t, jobs.failed)
	require.True(t, jobs.retry)
}

func projectHistoryScope() *HistoricalProjectScope {
	return &HistoricalProjectScope{
		ProjectID: 7, LooseMatch: true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `noreply@github\.com`, Enabled: true},
		},
	}
}
