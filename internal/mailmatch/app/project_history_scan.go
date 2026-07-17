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

type ValidatedMicrosoftHistoryScanTask struct {
	ResourceID uint   `json:"resourceId"`
	RequestID  string `json:"requestId,omitempty"`
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
	ListHistoricalProjectScopes(ctx context.Context) ([]HistoricalProjectScope, error)
	ListHistoricalProjectScopesForUpdate(ctx context.Context) ([]HistoricalProjectScope, error)
	FindHistoricalProjectScope(ctx context.Context, projectID uint) (*HistoricalProjectScope, error)
	FindHistoricalProjectScopeForUpdate(ctx context.Context, projectID uint) (*HistoricalProjectScope, error)
	ClearLegacyMicrosoftProjectHistory(ctx context.Context, resourceID uint, projectID uint) error
}

type HistoricalMicrosoftUsagePort interface {
	ImportHistoricalMicrosoftUsage(ctx context.Context, matches []HistoricalProjectMatch) error
}

type ProjectHistoryScanQueue interface {
	EnqueueProjectHistoryScan(ctx context.Context, task ProjectHistoryScanTask) error
	EnqueueValidatedMicrosoftHistoryScan(ctx context.Context, task ValidatedMicrosoftHistoryScanTask) error
	EnqueueProjectHistoryDispatcher(ctx context.Context, delay time.Duration) error
}

type ProjectHistoryScanUseCase struct {
	jobs        ProjectHistoryScanRepository
	matches     ProjectHistoryMatchRepository
	queue       ProjectHistoryScanQueue
	transport   MailTransportFetchPort
	credentials coreapp.MicrosoftCredentialPort
	history     HistoricalMicrosoftUsagePort
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

func (uc *ProjectHistoryScanUseCase) SetHistoricalMicrosoftUsagePort(history HistoricalMicrosoftUsagePort) {
	if uc != nil {
		uc.history = history
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

func (uc *ProjectHistoryScanUseCase) ScheduleValidatedMicrosoftHistory(ctx context.Context, resourceID uint, requestID string) error {
	if uc == nil || uc.queue == nil || resourceID == 0 {
		return domain.ErrInvalidRequest
	}
	return uc.queue.EnqueueValidatedMicrosoftHistoryScan(ctx, ValidatedMicrosoftHistoryScanTask{
		ResourceID: resourceID,
		RequestID:  strings.TrimSpace(requestID),
	})
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

func (uc *ProjectHistoryScanUseCase) ProcessValidatedMicrosoftHistory(ctx context.Context, task ValidatedMicrosoftHistoryScanTask) error {
	err := uc.scanValidatedMicrosoftHistory(ctx, task, 0)
	failure := (*MailFetchFailure)(nil)
	if errors.As(err, &failure) && failure != nil && !failure.Retryable {
		return nil
	}
	return err
}

func (uc *ProjectHistoryScanUseCase) scanValidatedMicrosoftHistory(ctx context.Context, task ValidatedMicrosoftHistoryScanTask, expectedCredentialRevision uint64) error {
	if uc == nil || uc.matches == nil || uc.credentials == nil || uc.transport == nil || task.ResourceID == 0 {
		return domain.ErrInvalidRequest
	}
	resource, err := uc.credentials.LockMicrosoftCredentialScope(ctx, task.ResourceID)
	if errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) || errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) {
		return nil
	}
	if err != nil {
		return err
	}
	if resource == nil || strings.EqualFold(resource.Status, "deleted") {
		return nil
	}
	if expectedCredentialRevision > 0 && resource.CredentialRevision != expectedCredentialRevision {
		return coreapp.ErrMicrosoftCredentialChanged
	}
	switch strings.ToLower(strings.TrimSpace(resource.Status)) {
	case "normal":
	case "pending", "validating":
		return domain.ErrInvalidRequest
	default:
		return nil
	}
	if strings.TrimSpace(resource.EmailAddress) == "" || strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
		return domain.ErrInvalidRequest
	}
	scopes, err := uc.matches.ListHistoricalProjectScopes(ctx)
	if err != nil || len(scopes) == 0 {
		return err
	}
	accumulator := historicalMatchesAccumulator{
		resourceID: resource.ResourceID, emailAddress: resource.EmailAddress,
		scopes: scopes, scannedAt: uc.now(),
	}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, projectHistoryMailboxTimeout)
	fetched, fetchErr := uc.transport.FetchMicrosoftMessages(fetchCtx, FetchMessagesRequest{
		Scope: OrderScope{
			OrderNo: "validated-microsoft-history", AllocationType: domain.ResourceTypeMicrosoft,
			EmailResourceID: resource.ResourceID, Recipient: resource.EmailAddress,
			MicrosoftEmail: resource.EmailAddress, MicrosoftClientID: resource.ClientID, MicrosoftRT: resource.RefreshToken,
		},
		RequestID: strings.TrimSpace(task.RequestID), FullHistory: true,
		OnMessages: accumulator.add, OnReset: accumulator.reset,
	})
	cancelFetch()
	if fetched == nil && fetchErr == nil {
		fetchErr = domain.ErrMailServiceUnavailable
	}
	refreshToken := ""
	if fetched != nil {
		refreshToken = strings.TrimSpace(fetched.RefreshToken)
	}
	if fetchErr != nil {
		var failure *MailFetchFailure
		if errors.As(fetchErr, &failure) {
			refreshToken = strings.TrimSpace(failure.RefreshToken)
		}
	}
	err = uc.matches.WithTx(ctx, func(txCtx context.Context) error {
		if fetchErr == nil {
			lockedScopes, err := uc.matches.ListHistoricalProjectScopesForUpdate(txCtx)
			if err != nil {
				return err
			}
			if !sameHistoricalProjectScopes(scopes, lockedScopes) {
				return errProjectHistoryScopeChanged
			}
		}
		if err := uc.credentials.ApplyMicrosoftFetchRefreshToken(txCtx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision,
			RefreshToken: refreshToken, Now: uc.now(),
		}); err != nil {
			return err
		}
		if fetchErr != nil {
			return nil
		}
		matches := accumulator.results()
		if len(matches) > 0 {
			if uc.history == nil {
				return domain.ErrInvalidRequest
			}
			if err := uc.history.ImportHistoricalMicrosoftUsage(txCtx, matches); err != nil {
				return err
			}
		}
		return uc.matches.ClearLegacyMicrosoftProjectHistory(txCtx, resource.ResourceID, 0)
	})
	if errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) || errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if fetchErr != nil {
		return fetchErr
	}
	return nil
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
	accumulator := historicalMatchesAccumulator{
		resourceID: resource.ResourceID, emailAddress: resource.EmailAddress,
		scopes: []HistoricalProjectScope{*scope}, scannedAt: uc.now(),
	}
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
		matches := accumulator.results()
		if len(matches) > 0 {
			if uc.history == nil {
				return domain.ErrInvalidRequest
			}
			if err := uc.history.ImportHistoricalMicrosoftUsage(txCtx, matches); err != nil {
				return err
			}
		}
		if err := uc.matches.ClearLegacyMicrosoftProjectHistory(txCtx, resource.ResourceID, job.ProjectID); err != nil {
			return err
		}
		return uc.jobs.Advance(txCtx, job.ID, job.ClaimToken, resource.ResourceID, len(matches) > 0, false, "")
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

type historicalMatchesAccumulator struct {
	resourceID   uint
	emailAddress string
	scopes       []HistoricalProjectScope
	scannedAt    time.Time
	matches      []HistoricalProjectMatch
	index        map[historicalMatchKey]int
}

type historicalMatchKey struct {
	projectID uint
	mailbox   HistoricalMailboxType
	email     string
}

func (a *historicalMatchesAccumulator) reset() {
	a.matches = nil
	a.index = nil
}

func (a *historicalMatchesAccumulator) add(messages []FetchedMessage) {
	pageMatches := historicalProjectMatches(HistoricalProjectMatchRequest{
		ResourceID: a.resourceID, EmailAddress: a.emailAddress,
		Messages: historicalMessagesFromFetched(messages), ScannedAt: a.scannedAt,
	}, a.scopes, a.scannedAt)
	if len(pageMatches) == 0 {
		return
	}
	if a.index == nil {
		a.index = make(map[historicalMatchKey]int, len(pageMatches))
	}
	for _, page := range pageMatches {
		key := historicalMatchKey{projectID: page.ProjectID, mailbox: page.MailboxType, email: page.MailboxEmail}
		matchIndex, ok := a.index[key]
		if !ok {
			a.index[key] = len(a.matches)
			a.matches = append(a.matches, page)
			continue
		}
		a.matches[matchIndex].EvidenceCount += page.EvidenceCount
		if page.FirstMatchedAt.Before(a.matches[matchIndex].FirstMatchedAt) {
			a.matches[matchIndex].FirstMatchedAt = page.FirstMatchedAt
		}
		if page.LastMatchedAt.After(a.matches[matchIndex].LastMatchedAt) {
			a.matches[matchIndex].LastMatchedAt = page.LastMatchedAt
		}
	}
}

func (a *historicalMatchesAccumulator) results() []HistoricalProjectMatch {
	return append([]HistoricalProjectMatch(nil), a.matches...)
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
	if left == nil || right == nil || left.ProjectID != right.ProjectID || left.ProductID != right.ProductID ||
		left.CodeWindowMinutes != right.CodeWindowMinutes || left.ActivationWindowMinutes != right.ActivationWindowMinutes ||
		left.WarrantyMinutes != right.WarrantyMinutes || left.LooseMatch != right.LooseMatch || len(left.Rules) != len(right.Rules) {
		return false
	}
	for i := range left.Rules {
		if left.Rules[i] != right.Rules[i] {
			return false
		}
	}
	return true
}

func sameHistoricalProjectScopes(left, right []HistoricalProjectScope) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !sameHistoricalProjectScope(&left[i], &right[i]) {
			return false
		}
	}
	return true
}
