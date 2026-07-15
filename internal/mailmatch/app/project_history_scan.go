package app

import (
	"context"
	"errors"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
)

const (
	projectHistoryPlannerShard      = -1
	projectHistoryScanShards        = 4 // ponytail: four global worker slots; raise only after measuring upstream capacity.
	projectHistoryMaxAttempts       = 3
	projectHistoryDispatchLimit     = 16
	projectHistoryMailboxTimeout    = 15 * time.Minute
	projectHistoryRunningStaleAfter = 25 * time.Minute
	projectHistoryDispatchLease     = time.Hour
)

var errProjectHistoryScopeChanged = errors.New("mailmatch: project history scope changed")

type ProjectHistoryScanJob struct {
	ID                   uint
	ProjectID            uint
	Shard                int
	Status               string
	StartResourceID      uint
	CheckpointResourceID uint
	EndResourceID        uint
	Attempts             int
	MaxAttempts          int
	ScannedCount         int
	MatchedCount         int
	SkippedCount         int
	ClaimToken           string
	DispatchToken        string
	RequestID            string
	DispatchedAt         *time.Time
	UpdatedAt            time.Time
}

type ProjectHistoryScanTask struct {
	JobID         uint   `json:"jobId"`
	DispatchToken string `json:"dispatchToken"`
}

type ProjectHistoryScanRepository interface {
	CreatePlanner(ctx context.Context, projectID uint, requestID string) error
	EnsureMissingPlanners(ctx context.Context, limit int) (int, error)
	ClaimDispatchable(ctx context.Context, limit int, runningStaleBefore, dispatchStaleBefore time.Time) ([]ProjectHistoryScanJob, error)
	MarkRunning(ctx context.Context, jobID uint, dispatchToken string) (*ProjectHistoryScanJob, bool, error)
	PlanShards(ctx context.Context, plannerID uint, claimToken string, shards []ProjectHistoryScanJob) error
	Advance(ctx context.Context, jobID uint, claimToken string, resourceID uint, matched bool, skipped bool, safeError string) error
	Complete(ctx context.Context, jobID uint, claimToken string) error
	MarkFailure(ctx context.Context, job ProjectHistoryScanJob, resourceID uint, retryable bool, safeError string) error
	ReleaseDispatch(ctx context.Context, jobID uint, dispatchToken string) error
	MarkDispatchFailed(ctx context.Context, jobID uint, dispatchToken string, safeError string) error
}

type ProjectHistoryMatchRepository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	FindHistoricalProjectScope(ctx context.Context, projectID uint) (*HistoricalProjectScope, error)
	FindHistoricalProjectScopeForUpdate(ctx context.Context, projectID uint) (*HistoricalProjectScope, error)
	UpsertMicrosoftProjectMatches(ctx context.Context, matches []HistoricalProjectMatch) error
}

type ProjectHistoryScanQueue interface {
	EnqueueProjectHistoryScan(ctx context.Context, task ProjectHistoryScanTask) error
	EnqueueProjectHistoryDispatcher(ctx context.Context, delay time.Duration) error
}

type ProjectHistoryScanUseCase struct {
	jobs        ProjectHistoryScanRepository
	matches     ProjectHistoryMatchRepository
	queue       ProjectHistoryScanQueue
	transport   MailTransportFetchPort
	credentials coreapp.MicrosoftCredentialPort
	now         func() time.Time
}

func NewProjectHistoryScanUseCase(jobs ProjectHistoryScanRepository, matches ProjectHistoryMatchRepository, queue ProjectHistoryScanQueue, transport MailTransportFetchPort) *ProjectHistoryScanUseCase {
	return &ProjectHistoryScanUseCase{
		jobs: jobs, matches: matches, queue: queue, transport: transport,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (uc *ProjectHistoryScanUseCase) SetMicrosoftCredentialPort(credentials coreapp.MicrosoftCredentialPort) {
	if uc != nil {
		uc.credentials = credentials
	}
}

func (uc *ProjectHistoryScanUseCase) Schedule(ctx context.Context, projectID uint, requestID string) error {
	if uc == nil || uc.jobs == nil || projectID == 0 {
		return domain.ErrInvalidRequest
	}
	if err := uc.jobs.CreatePlanner(ctx, projectID, strings.TrimSpace(requestID)); err != nil {
		return err
	}
	uc.ScheduleDispatcher(ctx, 0)
	return nil
}

func (uc *ProjectHistoryScanUseCase) DispatchPending(ctx context.Context, limit int) error {
	if uc == nil || uc.jobs == nil || uc.queue == nil {
		return domain.ErrFetchQueueUnavailable
	}
	if limit <= 0 {
		limit = projectHistoryDispatchLimit
	}
	_, ensureErr := uc.jobs.EnsureMissingPlanners(ctx, limit)
	now := uc.now()
	jobs, err := uc.jobs.ClaimDispatchable(ctx, limit, now.Add(-projectHistoryRunningStaleAfter), now.Add(-projectHistoryDispatchLease))
	if err != nil {
		return errors.Join(ensureErr, err)
	}
	var result error
	for _, job := range jobs {
		if err := uc.queue.EnqueueProjectHistoryScan(ctx, ProjectHistoryScanTask{JobID: job.ID, DispatchToken: job.DispatchToken}); err != nil {
			result = errors.Join(result, err, uc.jobs.MarkDispatchFailed(ctx, job.ID, job.DispatchToken, "Project history queue is temporarily unavailable."))
		}
	}
	return errors.Join(ensureErr, result)
}

func (uc *ProjectHistoryScanUseCase) Process(ctx context.Context, task ProjectHistoryScanTask) error {
	if uc == nil || uc.jobs == nil || uc.matches == nil || uc.queue == nil || uc.credentials == nil || task.JobID == 0 || strings.TrimSpace(task.DispatchToken) == "" {
		return domain.ErrInvalidRequest
	}
	job, claimed, err := uc.jobs.MarkRunning(ctx, task.JobID, task.DispatchToken)
	if err != nil || !claimed {
		return err
	}
	if job.Shard == projectHistoryPlannerShard {
		return uc.processPlanner(ctx, *job)
	}
	return uc.processShard(ctx, *job)
}

func (uc *ProjectHistoryScanUseCase) processPlanner(ctx context.Context, job ProjectHistoryScanJob) error {
	scope, err := uc.matches.FindHistoricalProjectScope(ctx, job.ProjectID)
	if err != nil {
		return uc.retry(ctx, job, 0, true, "Project history planning failed.")
	}
	if scope == nil {
		return uc.complete(ctx, job)
	}
	maxID, err := uc.credentials.MaxMicrosoftResourceID(ctx)
	if err != nil {
		return uc.retry(ctx, job, 0, true, "Project history resource snapshot failed.")
	}
	if maxID == 0 {
		return uc.complete(ctx, job)
	}
	span := (maxID + projectHistoryScanShards - 1) / projectHistoryScanShards
	shards := make([]ProjectHistoryScanJob, 0, projectHistoryScanShards)
	for start := uint(1); start <= maxID; start += span {
		end := start + span - 1
		if end > maxID || end < start {
			end = maxID
		}
		shards = append(shards, ProjectHistoryScanJob{
			ProjectID: job.ProjectID, Shard: len(shards), Status: "queued",
			StartResourceID: start, CheckpointResourceID: start - 1, EndResourceID: end,
			MaxAttempts: projectHistoryMaxAttempts, RequestID: job.RequestID,
		})
		if end == maxID {
			break
		}
	}
	if err := uc.jobs.PlanShards(ctx, job.ID, job.ClaimToken, shards); err != nil {
		return uc.retry(ctx, job, 0, true, "Project history planning failed.")
	}
	uc.ScheduleDispatcher(ctx, 0)
	return nil
}

func (uc *ProjectHistoryScanUseCase) processShard(ctx context.Context, job ProjectHistoryScanJob) error {
	resource, err := uc.credentials.FindNextMicrosoftCredentialScope(ctx, job.CheckpointResourceID, job.EndResourceID)
	if err != nil {
		return uc.retry(ctx, job, 0, true, "Project history resource lookup failed.")
	}
	if resource == nil {
		return uc.complete(ctx, job)
	}
	if strings.TrimSpace(resource.EmailAddress) == "" || strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
		return uc.skipIncompleteCredential(ctx, job, *resource)
	}
	scope, err := uc.matches.FindHistoricalProjectScope(ctx, job.ProjectID)
	if err != nil {
		return uc.retry(ctx, job, resource.ResourceID, true, "Project history rules could not be loaded.")
	}
	if scope == nil {
		return uc.complete(ctx, job)
	}
	accumulator := historicalMatchAccumulator{resourceID: resource.ResourceID, emailAddress: resource.EmailAddress, scope: *scope, scannedAt: uc.now()}
	if uc.transport == nil {
		return uc.retry(ctx, job, resource.ResourceID, true, "Microsoft mail service is temporarily unavailable.")
	}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, projectHistoryMailboxTimeout)
	fetched, err := uc.transport.FetchMicrosoftMessages(fetchCtx, FetchMessagesRequest{
		Scope: OrderScope{
			OrderNo: "project-history", AllocationType: domain.ResourceTypeMicrosoft,
			EmailResourceID: resource.ResourceID, Recipient: resource.EmailAddress,
			MicrosoftEmail: resource.EmailAddress, MicrosoftClientID: resource.ClientID, MicrosoftRT: resource.RefreshToken,
		},
		RequestID: job.RequestID, FullHistory: true, OnMessages: accumulator.add, OnReset: accumulator.reset,
	})
	cancelFetch()
	if err != nil {
		retryable := true
		refreshToken := ""
		var failure *MailFetchFailure
		if errors.As(err, &failure) {
			retryable = failure.Retryable
			refreshToken = failure.RefreshToken
		}
		return uc.recordFetchFailure(ctx, job, *resource, retryable, refreshToken)
	}
	if fetched == nil {
		return uc.retry(ctx, job, resource.ResourceID, true, "Microsoft mailbox history fetch failed.")
	}
	err = uc.matches.WithTx(ctx, func(txCtx context.Context) error {
		lockedScope, err := uc.matches.FindHistoricalProjectScopeForUpdate(txCtx, job.ProjectID)
		if err != nil {
			return err
		}
		if lockedScope == nil {
			return uc.jobs.Complete(txCtx, job.ID, job.ClaimToken)
		}
		if !sameHistoricalProjectScope(scope, lockedScope) {
			return errProjectHistoryScopeChanged
		}
		if err := uc.credentials.ApplyMicrosoftFetchRefreshToken(txCtx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision,
			RefreshToken: strings.TrimSpace(fetched.RefreshToken), Now: uc.now(),
		}); err != nil {
			return err
		}
		if accumulator.match != nil {
			if err := uc.matches.UpsertMicrosoftProjectMatches(txCtx, []HistoricalProjectMatch{*accumulator.match}); err != nil {
				return err
			}
		}
		return uc.jobs.Advance(txCtx, job.ID, job.ClaimToken, resource.ResourceID, accumulator.match != nil, false, "")
	})
	if err != nil {
		if errors.Is(err, errProjectHistoryScopeChanged) || errors.Is(err, coreapp.ErrMicrosoftCredentialChanged) {
			return uc.retry(ctx, job, 0, true, "Project history scope changed while mail was being fetched.")
		}
		if errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) || errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) {
			return uc.retry(ctx, job, resource.ResourceID, false, "Microsoft resource is no longer available.")
		}
		return uc.retry(ctx, job, resource.ResourceID, true, "Project history match commit failed.")
	}
	uc.ScheduleDispatcher(ctx, 0)
	return nil
}

func (uc *ProjectHistoryScanUseCase) skipIncompleteCredential(ctx context.Context, job ProjectHistoryScanJob, resource coreapp.MicrosoftCredentialScope) error {
	err := uc.matches.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.credentials.ApplyMicrosoftFetchRefreshToken(txCtx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision, Now: uc.now(),
		}); err != nil {
			return err
		}
		return uc.jobs.Advance(txCtx, job.ID, job.ClaimToken, resource.ResourceID, false, true, "Microsoft credentials are incomplete.")
	})
	if errors.Is(err, coreapp.ErrMicrosoftCredentialChanged) {
		return uc.retry(ctx, job, 0, true, "Microsoft credentials changed before history scan.")
	}
	if errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) || errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) {
		return uc.retry(ctx, job, resource.ResourceID, false, "Microsoft resource is no longer available.")
	}
	if err != nil {
		return err
	}
	uc.ScheduleDispatcher(ctx, 0)
	return nil
}

func (uc *ProjectHistoryScanUseCase) recordFetchFailure(ctx context.Context, job ProjectHistoryScanJob, resource coreapp.MicrosoftCredentialScope, retryable bool, refreshToken string) error {
	err := uc.matches.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.credentials.ApplyMicrosoftFetchRefreshToken(txCtx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision,
			RefreshToken: strings.TrimSpace(refreshToken), Now: uc.now(),
		}); err != nil {
			return err
		}
		return uc.jobs.MarkFailure(txCtx, job, resource.ResourceID, retryable, "Microsoft mailbox history fetch failed.")
	})
	if errors.Is(err, coreapp.ErrMicrosoftCredentialChanged) {
		return uc.retry(ctx, job, 0, true, "Microsoft credentials changed while history was being fetched.")
	}
	if errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) || errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) {
		return uc.retry(ctx, job, resource.ResourceID, false, "Microsoft resource is no longer available.")
	}
	if err != nil {
		return err
	}
	uc.ScheduleDispatcher(ctx, time.Second)
	return nil
}

func (uc *ProjectHistoryScanUseCase) retry(ctx context.Context, job ProjectHistoryScanJob, resourceID uint, retryable bool, safeError string) error {
	if err := uc.jobs.MarkFailure(ctx, job, resourceID, retryable, safeError); err != nil {
		return err
	}
	uc.ScheduleDispatcher(ctx, time.Second)
	return nil
}

func (uc *ProjectHistoryScanUseCase) complete(ctx context.Context, job ProjectHistoryScanJob) error {
	if err := uc.jobs.Complete(ctx, job.ID, job.ClaimToken); err != nil {
		return err
	}
	uc.ScheduleDispatcher(ctx, 0)
	return nil
}

func (uc *ProjectHistoryScanUseCase) ReleaseDispatch(ctx context.Context, task ProjectHistoryScanTask) error {
	if uc == nil || uc.jobs == nil {
		return nil
	}
	return uc.jobs.ReleaseDispatch(ctx, task.JobID, task.DispatchToken)
}

func (uc *ProjectHistoryScanUseCase) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if uc != nil && uc.queue != nil {
		_ = uc.queue.EnqueueProjectHistoryDispatcher(ctx, delay)
	}
}

type historicalMatchAccumulator struct {
	resourceID   uint
	emailAddress string
	scope        HistoricalProjectScope
	scannedAt    time.Time
	match        *HistoricalProjectMatch
}

func (a *historicalMatchAccumulator) reset() {
	a.match = nil
}

func (a *historicalMatchAccumulator) add(messages []FetchedMessage) {
	pageMatches := historicalProjectMatches(HistoricalProjectMatchRequest{
		ResourceID: a.resourceID, EmailAddress: a.emailAddress,
		Messages: historicalMessagesFromFetched(messages), ScannedAt: a.scannedAt,
	}, []HistoricalProjectScope{a.scope}, a.scannedAt)
	if len(pageMatches) == 0 {
		return
	}
	page := pageMatches[0]
	if a.match == nil {
		a.match = &page
		return
	}
	a.match.EvidenceCount += page.EvidenceCount
	if page.FirstMatchedAt.Before(a.match.FirstMatchedAt) {
		a.match.FirstMatchedAt = page.FirstMatchedAt
	}
	if page.LastMatchedAt.After(a.match.LastMatchedAt) {
		a.match.LastMatchedAt = page.LastMatchedAt
	}
}

func historicalMessagesFromFetched(messages []FetchedMessage) []HistoricalProjectMessage {
	result := make([]HistoricalProjectMessage, len(messages))
	for i := range messages {
		result[i] = HistoricalProjectMessage{
			Recipients: messages[i].Recipients, Sender: messages[i].Sender, Subject: messages[i].Subject,
			Body: messages[i].Body, BodyPreview: messages[i].BodyPreview,
			MessageIDHeader: messages[i].MessageIDHeader, ProviderMessageID: messages[i].ProviderMessageID,
			Protocol: messages[i].Protocol, Folder: messages[i].Folder, ReceivedAt: messages[i].ReceivedAt,
		}
	}
	return result
}

func sameHistoricalProjectScope(left, right *HistoricalProjectScope) bool {
	if left == nil || right == nil || left.ProjectID != right.ProjectID || left.LooseMatch != right.LooseMatch || len(left.Rules) != len(right.Rules) {
		return false
	}
	for i := range left.Rules {
		if left.Rules[i] != right.Rules[i] {
			return false
		}
	}
	return true
}
