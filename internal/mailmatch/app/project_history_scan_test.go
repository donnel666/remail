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
	pending          []ProjectHistoryScanState
	processing       int
	completed        int
	completedScanned int
	completedMatched int
	completedSkipped int
	released         int
	failures         int
	abnormal         bool
}

func (s *projectHistoryJobsStub) RequestProjectHistoryScan(_ context.Context, projectID uint, requestID string) (*ProjectHistoryScanState, error) {
	state := &ProjectHistoryScanState{ProjectID: projectID, Status: "pending", Generation: 1, RequestID: requestID}
	return state, nil
}
func (s *projectHistoryJobsStub) ListPendingProjectHistoryScans(context.Context, int) ([]ProjectHistoryScanState, error) {
	return append([]ProjectHistoryScanState(nil), s.pending...), nil
}
func (s *projectHistoryJobsStub) MarkProjectHistoryProcessing(context.Context, uint, uint64) (bool, error) {
	s.processing++
	return true, nil
}
func (*projectHistoryJobsStub) AssertProjectHistoryFence(context.Context, uint, uint64) error {
	return nil
}
func (s *projectHistoryJobsStub) CompleteProjectHistoryScan(_ context.Context, _ uint, _ uint64, scanned, matched, skipped int) (bool, error) {
	s.completed++
	s.completedScanned, s.completedMatched, s.completedSkipped = scanned, matched, skipped
	return true, nil
}
func (s *projectHistoryJobsStub) ReleaseProjectHistoryInfrastructureFailure(context.Context, uint, uint64, string) (bool, error) {
	s.released++
	return true, nil
}
func (s *projectHistoryJobsStub) RecordProjectHistoryFailure(context.Context, uint, uint64, string) (bool, bool, error) {
	s.failures++
	return true, s.abnormal, nil
}

type projectHistoryMatchesStub struct {
	scope        *HistoricalProjectScope
	scopes       []HistoricalProjectScope
	lockedScopes []HistoricalProjectScope
	cleared      uint
	clearProject uint
}

func (*projectHistoryMatchesStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *projectHistoryMatchesStub) ListHistoricalProjectScopes(context.Context) ([]HistoricalProjectScope, error) {
	if s.scopes != nil {
		return s.scopes, nil
	}
	if s.scope == nil {
		return nil, nil
	}
	return []HistoricalProjectScope{*s.scope}, nil
}
func (s *projectHistoryMatchesStub) ListHistoricalProjectScopesForUpdate(context.Context) ([]HistoricalProjectScope, error) {
	if s.lockedScopes != nil {
		return s.lockedScopes, nil
	}
	return s.ListHistoricalProjectScopes(context.Background())
}
func (s *projectHistoryMatchesStub) FindHistoricalProjectScope(context.Context, uint) (*HistoricalProjectScope, error) {
	return s.scope, nil
}
func (s *projectHistoryMatchesStub) FindHistoricalProjectScopeForUpdate(context.Context, uint) (*HistoricalProjectScope, error) {
	return s.scope, nil
}
func (s *projectHistoryMatchesStub) ClearLegacyMicrosoftProjectHistory(_ context.Context, resourceID uint, projectID uint) error {
	s.cleared, s.clearProject = resourceID, projectID
	return nil
}

type projectHistoryUsageStub struct{ matches []HistoricalProjectMatch }

func (s *projectHistoryUsageStub) ImportHistoricalMicrosoftUsage(_ context.Context, matches []HistoricalProjectMatch) error {
	s.matches = append([]HistoricalProjectMatch(nil), matches...)
	return nil
}

type projectHistoryQueueStub struct {
	accepted   bool
	dispatches int
	queued     []ProjectHistoryScanTask
	validated  []ValidatedMicrosoftHistoryScanTask
}

func (q *projectHistoryQueueStub) EnqueueProjectHistoryScan(_ context.Context, task ProjectHistoryScanTask) (bool, error) {
	q.queued = append(q.queued, task)
	return q.accepted, nil
}
func (q *projectHistoryQueueStub) EnqueueValidatedMicrosoftHistoryScan(_ context.Context, task ValidatedMicrosoftHistoryScanTask) error {
	q.validated = append(q.validated, task)
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
	history     coreapp.MicrosoftHistoryScanResult
	rotationErr error
}

func (s *projectHistoryCredentialsStub) LockMicrosoftCredentialScope(_ context.Context, resourceID uint) (*coreapp.MicrosoftCredentialScope, error) {
	for _, resource := range s.resources {
		if resource.ResourceID == resourceID {
			return resource, nil
		}
	}
	return nil, coreapp.ErrMicrosoftCredentialNotFound
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
func (s *projectHistoryCredentialsStub) ApplyMicrosoftHistoryScanResult(_ context.Context, result coreapp.MicrosoftHistoryScanResult) error {
	s.history = result
	if s.rotationErr != nil {
		return s.rotationErr
	}
	for _, resource := range s.resources {
		if resource.ResourceID != result.ResourceID || resource.CredentialRevision != result.ExpectedCredentialRevision {
			continue
		}
		if result.RefreshToken != "" && result.RefreshToken != resource.RefreshToken {
			resource.RefreshToken = result.RefreshToken
			resource.CredentialRevision++
		}
		if result.Completed && resource.Status == "identifying" {
			resource.Status = "normal"
		}
		return nil
	}
	return coreapp.ErrMicrosoftCredentialNotFound
}

type projectHistoryTransportStub struct {
	request FetchMessagesRequest
	result  *FetchMessagesResult
	err     error
	pages   [][]FetchedMessage
}

func (s *projectHistoryTransportStub) FetchMicrosoftMessages(_ context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
	s.request = request
	if request.OnMessages != nil && s.err == nil {
		for _, page := range s.pages {
			request.OnMessages(page)
		}
	}
	return s.result, s.err
}

func TestProjectHistoryDispatchMarksProcessingOnlyAfterAcceptedEnqueue(t *testing.T) {
	jobs := &projectHistoryJobsStub{pending: []ProjectHistoryScanState{{ProjectID: 7, Generation: 3, Status: "pending"}}}
	queue := &projectHistoryQueueStub{}
	uc := NewProjectHistoryScanUseCase(jobs, nil, queue, nil)
	require.NoError(t, uc.DispatchPending(context.Background(), 10))
	require.Zero(t, jobs.processing)

	queue.accepted = true
	require.NoError(t, uc.DispatchPending(context.Background(), 10))
	require.Equal(t, 1, jobs.processing)
}

func TestProjectHistoryProcessCompletesCurrentGeneration(t *testing.T) {
	now := time.Now().UTC()
	jobs := &projectHistoryJobsStub{}
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	credentials := &projectHistoryCredentialsStub{maxID: 10, resources: []*coreapp.MicrosoftCredentialScope{
		{ResourceID: 5},
		{ResourceID: 10, EmailAddress: "main@example.com", ClientID: "client", RefreshToken: "refresh", CredentialRevision: 4},
	}}
	transport := &projectHistoryTransportStub{result: &FetchMessagesResult{RefreshToken: "rotated"}, pages: [][]FetchedMessage{{{
		EmailResourceID: 10, ResourceType: domain.ResourceTypeMicrosoft, Folder: "Inbox",
		Recipients: []string{"main@example.com"}, Sender: "noreply@github.com", ReceivedAt: now,
	}}}}
	history := &projectHistoryUsageStub{}
	queue := &projectHistoryQueueStub{accepted: true}
	uc := NewProjectHistoryScanUseCase(jobs, matches, queue, transport)
	uc.SetMicrosoftCredentialPort(credentials)
	uc.SetHistoricalMicrosoftUsagePort(history)

	task := ProjectHistoryScanTask{ProjectID: 7, Generation: 2, RequestID: "request-2"}
	for jobs.completed == 0 {
		require.NoError(t, uc.Process(context.Background(), task))
		if jobs.completed == 0 {
			require.NotEmpty(t, queue.queued)
			task = queue.queued[len(queue.queued)-1]
		}
	}
	require.Equal(t, 1, jobs.completed)
	require.Equal(t, 2, jobs.completedScanned)
	require.Equal(t, 1, jobs.completedMatched)
	require.Equal(t, 1, jobs.completedSkipped)
	require.Equal(t, "rotated", credentials.rotation.RefreshToken)
}

func TestProjectHistoryRetryableBusinessFailureIsRecorded(t *testing.T) {
	jobs := &projectHistoryJobsStub{}
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	credentials := &projectHistoryCredentialsStub{maxID: 10, resources: []*coreapp.MicrosoftCredentialScope{{
		ResourceID: 10, EmailAddress: "main@example.com", ClientID: "client", RefreshToken: "refresh", CredentialRevision: 4,
	}}}
	transport := &projectHistoryTransportStub{err: &MailFetchFailure{SafeMessage: "Temporary mailbox failure.", Retryable: true, Cause: errors.New("temporary")}}
	uc := NewProjectHistoryScanUseCase(jobs, matches, &projectHistoryQueueStub{}, transport)
	uc.SetMicrosoftCredentialPort(credentials)

	require.NoError(t, uc.Process(context.Background(), ProjectHistoryScanTask{ProjectID: 7, Generation: 2}))
	require.Equal(t, 1, jobs.failures)
	require.Zero(t, jobs.completed)
	require.Zero(t, jobs.released)
}

func TestValidatedMicrosoftHistoryScheduleCreatesResourceTask(t *testing.T) {
	queue := &projectHistoryQueueStub{}
	uc := NewProjectHistoryScanUseCase(nil, nil, queue, nil)
	require.NoError(t, uc.ScheduleValidatedMicrosoftHistory(context.Background(), 10, " request-1 "))
	require.Equal(t, []ValidatedMicrosoftHistoryScanTask{{ResourceID: 10, RequestID: "request-1"}}, queue.validated)
}

func TestValidatedMicrosoftHistoryPromotesIdentifyingResourceAfterImport(t *testing.T) {
	now := time.Now().UTC()
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	resource := &coreapp.MicrosoftCredentialScope{
		ResourceID: 10, Status: "identifying", EmailAddress: "main@example.com",
		ClientID: "client", RefreshToken: "refresh", CredentialRevision: 4,
	}
	credentials := &projectHistoryCredentialsStub{resources: []*coreapp.MicrosoftCredentialScope{resource}}
	transport := &projectHistoryTransportStub{result: &FetchMessagesResult{RefreshToken: "rotated"}, pages: [][]FetchedMessage{{{
		EmailResourceID: 10, ResourceType: domain.ResourceTypeMicrosoft, Folder: "Inbox",
		Recipients: []string{"main@example.com"}, Sender: "noreply@github.com", ReceivedAt: now,
	}}}}
	history := &projectHistoryUsageStub{}
	uc := NewProjectHistoryScanUseCase(nil, matches, &projectHistoryQueueStub{}, transport)
	uc.SetMicrosoftCredentialPort(credentials)
	uc.SetHistoricalMicrosoftUsagePort(history)

	require.NoError(t, uc.ProcessValidatedMicrosoftHistory(context.Background(), ValidatedMicrosoftHistoryScanTask{ResourceID: 10}))
	require.Equal(t, "normal", resource.Status)
	require.True(t, credentials.history.Completed)
	require.Equal(t, "rotated", resource.RefreshToken)
	require.Len(t, history.matches, 1)
}

func TestValidatedMicrosoftHistoryFailureLeavesResourceIdentifying(t *testing.T) {
	matches := &projectHistoryMatchesStub{scope: projectHistoryScope()}
	resource := &coreapp.MicrosoftCredentialScope{
		ResourceID: 10, Status: "identifying", EmailAddress: "main@example.com",
		ClientID: "client", RefreshToken: "refresh", CredentialRevision: 4,
	}
	credentials := &projectHistoryCredentialsStub{resources: []*coreapp.MicrosoftCredentialScope{resource}}
	transport := &projectHistoryTransportStub{err: &MailFetchFailure{
		SafeMessage: "Mailbox rejected the history scan.", Retryable: false, Cause: errors.New("rejected"),
	}}
	uc := NewProjectHistoryScanUseCase(nil, matches, &projectHistoryQueueStub{}, transport)
	uc.SetMicrosoftCredentialPort(credentials)

	require.NoError(t, uc.ProcessValidatedMicrosoftHistory(context.Background(), ValidatedMicrosoftHistoryScanTask{ResourceID: 10}))
	require.Equal(t, "identifying", resource.Status)
	require.False(t, credentials.history.Completed)
}

func projectHistoryScope() *HistoricalProjectScope {
	return &HistoricalProjectScope{
		ProjectID: 7, ProductID: 20, LooseMatch: true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `noreply@github\.com`, Enabled: true},
		},
	}
}
