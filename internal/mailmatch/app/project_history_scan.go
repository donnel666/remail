package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
)

const (
	projectHistoryDispatchLimit     = 16
	maxProjectHistoryDispatchLimit  = 100
	projectHistoryMailboxTimeout    = 15 * time.Minute
	maxProjectHistoryMailboxTimeout = 2 * time.Hour
)

var errProjectHistoryScopeChanged = errors.New("mailmatch: project history scope changed")
var errProjectHistoryInfrastructure = errors.New("mailmatch: project history infrastructure failure")

type ProjectHistoryScanState struct {
	ProjectID   uint
	Status      string
	Generation  uint64
	Failures    int
	RequestID   string
	RequestedAt time.Time
}

type ProjectHistoryScanTask struct {
	ProjectID       uint   `json:"projectId"`
	Generation      uint64 `json:"generation"`
	RequestID       string `json:"requestId,omitempty"`
	AfterResourceID uint   `json:"afterResourceId,omitempty"`
	MaxResourceID   uint   `json:"maxResourceId,omitempty"`
	ScannedCount    int    `json:"scannedCount,omitempty"`
	MatchedCount    int    `json:"matchedCount,omitempty"`
	SkippedCount    int    `json:"skippedCount,omitempty"`
}

type ValidatedMicrosoftHistoryScanTask struct {
	ResourceID uint   `json:"resourceId"`
	RequestID  string `json:"requestId,omitempty"`
}

type ProjectHistoryScanRepository interface {
	RequestProjectHistoryScan(ctx context.Context, projectID uint, requestID string) (*ProjectHistoryScanState, error)
	ListPendingProjectHistoryScans(ctx context.Context, limit int) ([]ProjectHistoryScanState, error)
	MarkProjectHistoryProcessing(ctx context.Context, projectID uint, generation uint64) (bool, error)
	AssertProjectHistoryFence(ctx context.Context, projectID uint, generation uint64) error
	CompleteProjectHistoryScan(ctx context.Context, projectID uint, generation uint64, scanned int, matched int, skipped int) (bool, error)
	ReleaseProjectHistoryInfrastructureFailure(ctx context.Context, projectID uint, generation uint64, safeError string) (bool, error)
	RecordProjectHistoryFailure(ctx context.Context, projectID uint, generation uint64, safeError string) (recorded bool, abnormal bool, err error)
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
	EnqueueProjectHistoryScan(ctx context.Context, task ProjectHistoryScanTask) (bool, error)
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
	if _, err := uc.jobs.RequestProjectHistoryScan(ctx, projectID, strings.TrimSpace(requestID)); err != nil {
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
	limit = min(limit, maxProjectHistoryDispatchLimit)
	states, err := uc.jobs.ListPendingProjectHistoryScans(ctx, limit)
	if err != nil {
		return err
	}
	var result error
	for _, state := range states {
		accepted, enqueueErr := uc.queue.EnqueueProjectHistoryScan(ctx, ProjectHistoryScanTask{
			ProjectID: state.ProjectID, Generation: state.Generation, RequestID: state.RequestID,
		})
		if enqueueErr != nil {
			result = errors.Join(result, enqueueErr)
			continue
		}
		if !accepted {
			continue
		}
		if _, markErr := uc.jobs.MarkProjectHistoryProcessing(ctx, state.ProjectID, state.Generation); markErr != nil {
			result = errors.Join(result, markErr)
		}
	}
	return result
}

func (uc *ProjectHistoryScanUseCase) Process(ctx context.Context, task ProjectHistoryScanTask) error {
	if uc == nil || uc.jobs == nil || uc.matches == nil || uc.queue == nil || uc.credentials == nil || task.ProjectID == 0 || task.Generation == 0 {
		return domain.ErrInvalidRequest
	}
	current, err := uc.jobs.MarkProjectHistoryProcessing(ctx, task.ProjectID, task.Generation)
	if err != nil || !current {
		return err
	}
	done, next, err := uc.scanProjectHistoryPage(ctx, task)
	if err == nil && done {
		_, err = uc.jobs.CompleteProjectHistoryScan(
			ctx, task.ProjectID, task.Generation, next.ScannedCount, next.MatchedCount, next.SkippedCount,
		)
		return err
	}
	if err == nil {
		_, enqueueErr := uc.queue.EnqueueProjectHistoryScan(ctx, next)
		if enqueueErr == nil {
			return nil
		}
		err = fmt.Errorf("%w: enqueue project history continuation: %v", errProjectHistoryInfrastructure, enqueueErr)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errProjectHistoryInfrastructure) {
		released, releaseErr := uc.jobs.ReleaseProjectHistoryInfrastructureFailure(
			context.WithoutCancel(ctx), task.ProjectID, task.Generation, "Project history infrastructure is temporarily unavailable.",
		)
		if released {
			uc.ScheduleDispatcher(context.WithoutCancel(ctx), 0)
		}
		return errors.Join(err, releaseErr)
	}
	recorded, abnormal, recordErr := uc.jobs.RecordProjectHistoryFailure(
		context.WithoutCancel(ctx), task.ProjectID, task.Generation, safeProjectHistoryFailure(err),
	)
	if recordErr != nil {
		return errors.Join(err, recordErr)
	}
	if recorded && !abnormal {
		uc.ScheduleDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	return nil
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
	case "identifying", "normal":
	case "pending", "validating":
		// Validation deliberately creates this durable task before committing
		// identifying. Retry as a non-failure deferral instead of treating the
		// expected commit race as an invalid request.
		return platform.ErrBackgroundExecutionDeferred
	default:
		return nil
	}
	if strings.TrimSpace(resource.EmailAddress) == "" || strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
		return domain.ErrInvalidRequest
	}
	scopes, err := uc.matches.ListHistoricalProjectScopes(ctx)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return uc.matches.WithTx(ctx, func(txCtx context.Context) error {
			if err := uc.matches.ClearLegacyMicrosoftProjectHistory(txCtx, resource.ResourceID, 0); err != nil {
				return err
			}
			return uc.credentials.ApplyMicrosoftHistoryScanResult(txCtx, coreapp.MicrosoftHistoryScanResult{
				ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision,
				Completed: true, Now: uc.now(),
			})
		})
	}
	accumulator := historicalMatchesAccumulator{
		resourceID: resource.ResourceID, emailAddress: resource.EmailAddress,
		scopes: scopes, scannedAt: uc.now(),
	}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, boundedRuntimeDuration("imap_full_history_timeout_minutes", projectHistoryMailboxTimeout, time.Minute, maxProjectHistoryMailboxTimeout))
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
		if fetchErr != nil {
			return uc.credentials.ApplyMicrosoftHistoryScanResult(txCtx, coreapp.MicrosoftHistoryScanResult{
				ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision,
				RefreshToken: refreshToken, Now: uc.now(),
			})
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
		if err := uc.matches.ClearLegacyMicrosoftProjectHistory(txCtx, resource.ResourceID, 0); err != nil {
			return err
		}
		return uc.credentials.ApplyMicrosoftHistoryScanResult(txCtx, coreapp.MicrosoftHistoryScanResult{
			ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision,
			RefreshToken: refreshToken, Completed: true, Now: uc.now(),
		})
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

func (uc *ProjectHistoryScanUseCase) scanProjectHistoryPage(ctx context.Context, task ProjectHistoryScanTask) (done bool, next ProjectHistoryScanTask, resultErr error) {
	next = task
	scope, err := uc.matches.FindHistoricalProjectScope(ctx, task.ProjectID)
	if err != nil {
		return false, next, fmt.Errorf("%w: load project history scope: %v", errProjectHistoryInfrastructure, err)
	}
	if scope == nil {
		return true, next, nil
	}
	if next.MaxResourceID == 0 {
		next.MaxResourceID, err = uc.credentials.MaxMicrosoftResourceID(ctx)
		if err != nil {
			return false, next, fmt.Errorf("%w: read microsoft resource high-water mark: %v", errProjectHistoryInfrastructure, err)
		}
	}
	resource, err := uc.credentials.FindNextMicrosoftCredentialScope(ctx, next.AfterResourceID, next.MaxResourceID)
	if err != nil {
		return false, next, fmt.Errorf("%w: find next microsoft resource: %v", errProjectHistoryInfrastructure, err)
	}
	if resource == nil {
		return true, next, nil
	}
	next.AfterResourceID = resource.ResourceID
	next.ScannedCount++
	if strings.TrimSpace(resource.EmailAddress) == "" || strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
		next.SkippedCount++
		return false, next, nil
	}
	resourceMatched, resourceSkipped, err := uc.scanProjectHistoryResource(ctx, task, *scope, *resource)
	if resourceMatched {
		next.MatchedCount++
	}
	if resourceSkipped {
		next.SkippedCount++
	}
	return false, next, err
}

func (uc *ProjectHistoryScanUseCase) scanProjectHistoryResource(
	ctx context.Context,
	task ProjectHistoryScanTask,
	scope HistoricalProjectScope,
	resource coreapp.MicrosoftCredentialScope,
) (matched bool, skipped bool, resultErr error) {
	if uc.transport == nil {
		return false, false, &MailFetchFailure{SafeMessage: "Microsoft mail service is temporarily unavailable.", Retryable: true, Cause: domain.ErrMailServiceUnavailable}
	}
	accumulator := historicalMatchesAccumulator{
		resourceID: resource.ResourceID, emailAddress: resource.EmailAddress,
		scopes: []HistoricalProjectScope{scope}, scannedAt: uc.now(),
	}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, boundedRuntimeDuration("imap_full_history_timeout_minutes", projectHistoryMailboxTimeout, time.Minute, maxProjectHistoryMailboxTimeout))
	fetched, fetchErr := uc.transport.FetchMicrosoftMessages(fetchCtx, FetchMessagesRequest{
		Scope: OrderScope{
			OrderNo: "project-history", AllocationType: domain.ResourceTypeMicrosoft,
			EmailResourceID: resource.ResourceID, Recipient: resource.EmailAddress,
			MicrosoftEmail: resource.EmailAddress, MicrosoftClientID: resource.ClientID, MicrosoftRT: resource.RefreshToken,
		},
		RequestID: task.RequestID, FullHistory: true, OnMessages: accumulator.add, OnReset: accumulator.reset,
	})
	cancelFetch()
	if fetched == nil && fetchErr == nil {
		fetchErr = &MailFetchFailure{SafeMessage: "Microsoft mailbox history fetch failed.", Retryable: true, Cause: domain.ErrMailServiceUnavailable}
	}
	refreshToken := ""
	if fetched != nil {
		refreshToken = strings.TrimSpace(fetched.RefreshToken)
	}
	failure := (*MailFetchFailure)(nil)
	if errors.As(fetchErr, &failure) && failure != nil {
		refreshToken = strings.TrimSpace(failure.RefreshToken)
	}
	if fetchErr != nil {
		err := uc.applyProjectHistoryRefreshToken(ctx, resource, refreshToken)
		if errors.Is(err, coreapp.ErrMicrosoftCredentialChanged) || errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) || errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) {
			return false, true, nil
		}
		if err != nil {
			return false, false, fmt.Errorf("%w: persist microsoft refresh token after fetch failure: %v", errProjectHistoryInfrastructure, err)
		}
		if failure != nil && !failure.Retryable {
			return false, true, nil
		}
		if failure != nil {
			return false, false, failure
		}
		return false, false, &MailFetchFailure{SafeMessage: "Microsoft mailbox history fetch failed.", Retryable: true, Cause: fetchErr}
	}

	matches := accumulator.results()
	err := uc.matches.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.jobs.AssertProjectHistoryFence(txCtx, task.ProjectID, task.Generation); err != nil {
			return err
		}
		lockedScope, err := uc.matches.FindHistoricalProjectScopeForUpdate(txCtx, task.ProjectID)
		if err != nil {
			return err
		}
		if lockedScope == nil || !sameHistoricalProjectScope(&scope, lockedScope) {
			return errProjectHistoryScopeChanged
		}
		if len(matches) > 0 {
			if uc.history == nil {
				return errors.New("historical microsoft usage service is unavailable")
			}
			if err := uc.history.ImportHistoricalMicrosoftUsage(txCtx, matches); err != nil {
				return err
			}
		}
		if err := uc.credentials.ApplyMicrosoftFetchRefreshToken(txCtx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision,
			RefreshToken: refreshToken, Now: uc.now(),
		}); err != nil {
			return err
		}
		return uc.matches.ClearLegacyMicrosoftProjectHistory(txCtx, resource.ResourceID, task.ProjectID)
	})
	if errors.Is(err, errProjectHistoryScopeChanged) {
		return false, false, err
	}
	if errors.Is(err, coreapp.ErrMicrosoftCredentialChanged) || errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) || errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("%w: commit project history match: %v", errProjectHistoryInfrastructure, err)
	}
	return len(matches) > 0, false, nil
}

func (uc *ProjectHistoryScanUseCase) applyProjectHistoryRefreshToken(ctx context.Context, resource coreapp.MicrosoftCredentialScope, refreshToken string) error {
	return uc.credentials.ApplyMicrosoftFetchRefreshToken(ctx, coreapp.MicrosoftFetchRefreshTokenRotation{
		ResourceID: resource.ResourceID, ExpectedCredentialRevision: resource.CredentialRevision,
		RefreshToken: strings.TrimSpace(refreshToken), Now: uc.now(),
	})
}

func safeProjectHistoryFailure(err error) string {
	var failure *MailFetchFailure
	if errors.As(err, &failure) && failure != nil && strings.TrimSpace(failure.SafeMessage) != "" {
		return failure.SafeMessage
	}
	if errors.Is(err, errProjectHistoryScopeChanged) {
		return "Project history scope changed while mail was being fetched."
	}
	return "Project history scan failed."
}

func (uc *ProjectHistoryScanUseCase) ReleaseDispatch(ctx context.Context, task ProjectHistoryScanTask) error {
	if uc == nil || uc.jobs == nil || task.ProjectID == 0 || task.Generation == 0 {
		return nil
	}
	released, err := uc.jobs.ReleaseProjectHistoryInfrastructureFailure(
		ctx, task.ProjectID, task.Generation, "Project history execution capacity is temporarily unavailable.",
	)
	if released {
		uc.ScheduleDispatcher(context.WithoutCancel(ctx), 0)
	}
	return err
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
