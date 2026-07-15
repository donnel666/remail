package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
)

const (
	resourceFetchLookbackWindow       = 90 * 24 * time.Hour
	resourceFetchRunningStaleAfter    = 20 * time.Minute
	resourceFetchQueuedDispatchLease  = 60 * time.Minute
	resourceFetchDefaultDispatchLimit = 100
)

// MailFetchFailure carries the ACL's safe classification without exposing raw
// upstream content. Existing order-scoped callers may continue treating it as
// a plain error; the administrator resource worker uses Retryable to decide its
// durable transition.
type MailFetchFailure struct {
	Category     string
	SafeMessage  string
	Retryable    bool
	RefreshToken string
	Cause        error
}

func (e *MailFetchFailure) Error() string {
	if e == nil {
		return "mail fetch failed"
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "mail fetch failed"
}

func (e *MailFetchFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ResourceFetchSubmitCommand struct {
	ResourceID     uint
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

type ResourceFetchSubmitResult struct {
	Job    domain.ResourceFetchJob
	Reused bool
}

type ResourceFetchTask struct {
	JobID         uint   `json:"jobId"`
	DispatchToken string `json:"dispatchToken"`
	RequestID     string `json:"requestId"`
}

type DispatchResourceFetchJobsResult struct {
	Attempted int
	Queued    int
	Failed    int
}

type ResourceFetchRepository interface {
	CreateOrReuseResourceFetch(ctx context.Context, job *domain.ResourceFetchJob, log *governancedomain.OperationLog) (bool, error)
	FindResourceFetchJob(ctx context.Context, id uint) (*domain.ResourceFetchJob, error)
	ClaimDispatchableResourceFetches(ctx context.Context, limit int, runningStaleBefore time.Time, queuedDispatchStaleBefore time.Time) ([]domain.ResourceFetchJob, error)
	MarkResourceFetchRunning(ctx context.Context, id uint, dispatchToken string) (string, bool, error)
	ReleaseResourceFetchDispatch(ctx context.Context, id uint, dispatchToken string) error
	MarkResourceFetchDispatchFailed(ctx context.Context, id uint, dispatchToken string, safeError string, log *governancedomain.SystemLog) error
	LoadResourceFetchScope(ctx context.Context, resourceID uint, expectedCredentialRevision uint64) (*domain.ResourceFetchScope, error)
	AssertResourceFetchFence(ctx context.Context, jobID uint, claimToken string, resourceID uint, expectedCredentialRevision uint64) error
	CompleteResourceFetch(ctx context.Context, jobID uint, claimToken string, resourceID uint, expectedCredentialRevision uint64, rotatedRefreshToken string, fetched int, stored int, matched int, now time.Time, log *governancedomain.SystemLog) error
	MarkResourceFetchCanceled(ctx context.Context, jobID uint, claimToken string, safeError string, now time.Time, log *governancedomain.SystemLog) error
	MarkResourceFetchFailure(ctx context.Context, jobID uint, claimToken string, safeError string, retryable bool, now time.Time, log *governancedomain.SystemLog) (bool, error)
}

type ResourceFetchQueue interface {
	EnqueueResourceFetch(ctx context.Context, task ResourceFetchTask) error
	EnqueueFetchDispatcher(ctx context.Context, delay time.Duration) error
}

// ResourceFetchUseCase owns the administrator resource-scoped durable task.
// Message persistence and matching continue through the existing MailMatch
// UseCase; Microsoft/Graph/IMAP work continues through MailTransportFetchPort.
type ResourceFetchUseCase struct {
	repo       ResourceFetchRepository
	queue      ResourceFetchQueue
	transport  MailTransportFetchPort
	messages   *UseCase
	systemLogs governanceapp.SystemLogPort
	now        func() time.Time
}

func NewResourceFetchUseCase(
	repo ResourceFetchRepository,
	queue ResourceFetchQueue,
	transport MailTransportFetchPort,
	messages *UseCase,
	systemLogs governanceapp.SystemLogPort,
) *ResourceFetchUseCase {
	return &ResourceFetchUseCase{
		repo:       repo,
		queue:      queue,
		transport:  transport,
		messages:   messages,
		systemLogs: systemLogs,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

func (uc *ResourceFetchUseCase) Submit(ctx context.Context, cmd ResourceFetchSubmitCommand) (*ResourceFetchSubmitResult, error) {
	if uc == nil || uc.repo == nil || cmd.ResourceID == 0 || cmd.OperatorUserID == 0 {
		return nil, domain.ErrInvalidRequest
	}
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if cmd.IdempotencyKey == "" || len(cmd.IdempotencyKey) > 128 {
		return nil, domain.ErrInvalidRequest
	}
	now := uc.now()
	sinceAt := now.Add(-resourceFetchLookbackWindow)
	job := &domain.ResourceFetchJob{
		ResourceID:     cmd.ResourceID,
		OperatorUserID: cmd.OperatorUserID,
		Status:         domain.ResourceFetchJobQueued,
		MaxAttempts:    domain.ResourceFetchDefaultMaxAttempts,
		SinceAt:        &sinceAt,
		UntilAt:        &now,
		RequestID:      strings.TrimSpace(cmd.RequestID),
		Path:           strings.TrimSpace(cmd.Path),
		IdempotencyKey: cmd.IdempotencyKey,
	}
	log := &governancedomain.OperationLog{
		OperatorUserID: cmd.OperatorUserID,
		OperationType:  "mailmatch.admin_resource.fetch",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", cmd.ResourceID),
		Path:           strings.TrimSpace(cmd.Path),
		Result:         "success",
		SafeSummary:    "Microsoft resource mail fetch accepted.",
		RequestID:      strings.TrimSpace(cmd.RequestID),
	}
	reused, err := uc.repo.CreateOrReuseResourceFetch(ctx, job, log)
	if err != nil {
		return nil, err
	}
	uc.wakeDispatcher(ctx, *job)
	return &ResourceFetchSubmitResult{Job: *job, Reused: reused}, nil
}

func (uc *ResourceFetchUseCase) Process(ctx context.Context, task ResourceFetchTask) error {
	if uc == nil || uc.repo == nil || task.JobID == 0 {
		return domain.ErrInvalidRequest
	}
	if strings.TrimSpace(task.DispatchToken) == "" {
		// Old or forged payloads cannot consume an unfenced durable job. The
		// dispatcher will issue a fresh generation.
		return nil
	}
	job, err := uc.repo.FindResourceFetchJob(ctx, task.JobID)
	if err != nil {
		return err
	}
	if job == nil {
		return domain.ErrResourceFetchJobNotFound
	}
	if domain.IsTerminalResourceFetchStatus(job.Status) {
		return nil
	}
	claimToken, claimed, err := uc.repo.MarkResourceFetchRunning(ctx, task.JobID, task.DispatchToken)
	if err != nil || !claimed {
		return err
	}
	job.ClaimToken = claimToken
	platform.ObserveQueueWait("mailmatch_resource_fetch", job.CreatedAt)

	scope, err := uc.repo.LoadResourceFetchScope(ctx, job.ResourceID, job.ExpectedCredentialRevision)
	if err != nil {
		return uc.finishScopeFailure(ctx, *job, err)
	}
	if uc.transport == nil {
		return uc.retryResourceFetch(ctx, *job, "Microsoft mail service is temporarily unavailable.", "request", true, domain.ErrMailServiceUnavailable)
	}
	fetched, err := uc.transport.FetchMicrosoftMessages(ctx, FetchMessagesRequest{
		Scope: OrderScope{
			OrderNo:           firstNonBlank(job.RequestID, fmt.Sprintf("resource-fetch-%d", job.ID)),
			AllocationType:    domain.ResourceTypeMicrosoft,
			EmailResourceID:   scope.ResourceID,
			Recipient:         scope.EmailAddress,
			MicrosoftEmail:    scope.EmailAddress,
			MicrosoftClientID: scope.ClientID,
			MicrosoftRT:       scope.RefreshToken,
		},
		SinceAt:   dereferenceTime(job.SinceAt, uc.now().Add(-resourceFetchLookbackWindow)),
		UntilAt:   dereferenceTime(job.UntilAt, uc.now()),
		RequestID: job.RequestID,
	})
	if err != nil {
		safe, category, retryable := classifyResourceFetchFailure(err)
		return uc.retryResourceFetch(ctx, *job, safe, category, retryable, err)
	}
	if fetched == nil {
		return uc.retryResourceFetch(ctx, *job, "Microsoft mail service is temporarily unavailable.", "request", true, domain.ErrMailServiceUnavailable)
	}

	if uc.messages == nil {
		return uc.retryResourceFetch(ctx, *job, "Mail message ingestion failed.", "ingestion", true, errors.New("mailmatch message use case is unavailable"))
	}
	stored, matched, _, err := uc.messages.ingestFetchedMessagesWithFence(ctx, fetched.Messages, func(txCtx context.Context) error {
		return uc.repo.AssertResourceFetchFence(
			txCtx,
			job.ID,
			job.ClaimToken,
			job.ResourceID,
			job.ExpectedCredentialRevision,
		)
	})
	if err != nil {
		if errors.Is(err, domain.ErrResourceFetchCredentialChanged) ||
			errors.Is(err, domain.ErrResourceFetchDeleted) ||
			errors.Is(err, domain.ErrResourceFetchNotFound) {
			return uc.cancelResourceFetch(ctx, *job, "Resource changed while mail fetch was running.", "credential_changed")
		}
		safe := "Mail message ingestion failed."
		if stageErr := (*mailIngestError)(nil); errors.As(err, &stageErr) {
			safe = stageErr.safe
		}
		return uc.retryResourceFetch(ctx, *job, safe, "ingestion", true, err)
	}

	now := uc.now()
	if err := uc.repo.CompleteResourceFetch(
		ctx,
		job.ID,
		job.ClaimToken,
		job.ResourceID,
		job.ExpectedCredentialRevision,
		strings.TrimSpace(fetched.RefreshToken),
		len(fetched.Messages),
		stored,
		matched,
		now,
		resourceFetchSystemLog(*job, "info", "resource_fetch_succeeded", "Microsoft resource mail fetch completed.", ""),
	); err != nil {
		if errors.Is(err, domain.ErrResourceFetchCredentialChanged) ||
			errors.Is(err, domain.ErrResourceFetchDeleted) ||
			errors.Is(err, domain.ErrResourceFetchNotFound) {
			return uc.cancelResourceFetch(ctx, *job, "Resource changed while mail fetch was running.", "credential_changed")
		}
		if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
			return nil
		}
		return err
	}
	return nil
}

func (uc *ResourceFetchUseCase) DispatchPending(ctx context.Context, limit int) (*DispatchResourceFetchJobsResult, error) {
	if uc == nil || uc.repo == nil || uc.queue == nil {
		return nil, domain.ErrFetchQueueUnavailable
	}
	if limit <= 0 {
		limit = resourceFetchDefaultDispatchLimit
	}
	now := uc.now()
	jobs, err := uc.repo.ClaimDispatchableResourceFetches(
		ctx,
		limit,
		now.Add(-resourceFetchRunningStaleAfter),
		now.Add(-resourceFetchQueuedDispatchLease),
	)
	if err != nil {
		return nil, err
	}
	result := &DispatchResourceFetchJobsResult{Attempted: len(jobs)}
	var dispatchErrors []error
	for _, job := range jobs {
		err := uc.queue.EnqueueResourceFetch(ctx, ResourceFetchTask{
			JobID:         job.ID,
			DispatchToken: job.DispatchToken,
			RequestID:     job.RequestID,
		})
		if err == nil {
			result.Queued++
			continue
		}
		result.Failed++
		dispatchErrors = append(dispatchErrors, fmt.Errorf("enqueue resource fetch job %d: %w", job.ID, err))
		releaseErr := uc.repo.MarkResourceFetchDispatchFailed(
			ctx,
			job.ID,
			job.DispatchToken,
			"Mail fetch queue is temporarily unavailable; dispatcher will retry.",
			resourceFetchSystemLog(job, "warning", "resource_fetch_dispatch_failed", "Microsoft resource mail fetch dispatch failed.", "queue_unavailable"),
		)
		if releaseErr != nil {
			dispatchErrors = append(dispatchErrors, fmt.Errorf("release resource fetch job %d: %w", job.ID, releaseErr))
		}
	}
	return result, errors.Join(dispatchErrors...)
}

func (uc *ResourceFetchUseCase) ReleaseDispatch(ctx context.Context, task ResourceFetchTask) error {
	if uc == nil || uc.repo == nil || task.JobID == 0 || strings.TrimSpace(task.DispatchToken) == "" {
		return nil
	}
	return uc.repo.ReleaseResourceFetchDispatch(ctx, task.JobID, task.DispatchToken)
}

func (uc *ResourceFetchUseCase) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if uc == nil || uc.queue == nil {
		return
	}
	_ = uc.queue.EnqueueFetchDispatcher(ctx, delay)
}

func (uc *ResourceFetchUseCase) finishScopeFailure(ctx context.Context, job domain.ResourceFetchJob, err error) error {
	switch {
	case errors.Is(err, domain.ErrResourceFetchCredentialChanged):
		return uc.cancelResourceFetch(ctx, job, "Resource credentials changed before mail fetch started.", "credential_changed")
	case errors.Is(err, domain.ErrResourceFetchDeleted), errors.Is(err, domain.ErrResourceFetchNotFound):
		return uc.cancelResourceFetch(ctx, job, "Resource is not available for mail fetch.", "resource_unavailable")
	case errors.Is(err, domain.ErrResourceFetchCredentialsMissing):
		return uc.retryResourceFetch(ctx, job, "Microsoft mail fetch credentials are incomplete.", "missing_token", false, err)
	default:
		return err
	}
}

func (uc *ResourceFetchUseCase) cancelResourceFetch(ctx context.Context, job domain.ResourceFetchJob, safe string, category string) error {
	now := uc.now()
	err := uc.repo.MarkResourceFetchCanceled(
		ctx,
		job.ID,
		job.ClaimToken,
		safe,
		now,
		resourceFetchSystemLog(job, "warning", "resource_fetch_canceled", safe, safeResourceFetchCategory(category)),
	)
	if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
		return nil
	}
	return err
}

func (uc *ResourceFetchUseCase) retryResourceFetch(
	ctx context.Context,
	job domain.ResourceFetchJob,
	safe string,
	category string,
	retryable bool,
	cause error,
) error {
	now := uc.now()
	retryScheduled, err := uc.repo.MarkResourceFetchFailure(
		ctx,
		job.ID,
		job.ClaimToken,
		safe,
		retryable,
		now,
		resourceFetchSystemLog(job, "warning", "resource_fetch_failed", safe, safeResourceFetchCategory(category)),
	)
	if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
		return nil
	}
	if err != nil {
		return err
	}
	if retryScheduled {
		uc.ScheduleDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	// The durable row already owns retry/exhaustion. Returning nil prevents
	// Asynq from replaying a consumed dispatch token.
	_ = cause
	return nil
}

func (uc *ResourceFetchUseCase) wakeDispatcher(ctx context.Context, job domain.ResourceFetchJob) {
	if uc == nil || uc.queue == nil {
		return
	}
	if err := uc.queue.EnqueueFetchDispatcher(ctx, 0); err == nil {
		return
	}
	if uc.systemLogs == nil {
		return
	}
	_ = uc.systemLogs.Create(context.WithoutCancel(ctx), resourceFetchSystemLog(
		job,
		"warning",
		"resource_fetch_dispatch_wakeup_failed",
		"Microsoft resource mail fetch was saved and awaits dispatcher recovery.",
		"queue_unavailable",
	))
}

func classifyResourceFetchFailure(err error) (safe string, category string, retryable bool) {
	failure := (*MailFetchFailure)(nil)
	if errors.As(err, &failure) && failure != nil {
		category = safeResourceFetchCategory(failure.Category)
		retryable = failure.Retryable
		if retryable {
			return "Microsoft mail service is temporarily unavailable.", category, true
		}
		if category == "missing_token" {
			return "Microsoft mail fetch credentials are incomplete.", category, false
		}
		return "Microsoft mail fetch failed.", category, false
	}
	return "Microsoft mail service is temporarily unavailable.", "request", true
}

func resourceFetchSystemLog(job domain.ResourceFetchJob, level string, eventType string, message string, detail string) *governancedomain.SystemLog {
	safeDetail := fmt.Sprintf("task=fetch:%d; attempt=%d", job.ID, job.Attempts+1)
	if detail = strings.TrimSpace(detail); detail != "" {
		safeDetail += "; category=" + detail
	}
	return &governancedomain.SystemLog{
		Level:     level,
		Module:    "mailmatch",
		EventType: eventType,
		RequestID: job.RequestID,
		BizType:   "microsoft_resource",
		BizID:     fmt.Sprintf("%d", job.ResourceID),
		Message:   strings.TrimSpace(message),
		Detail:    safeDetail,
	}
}

func safeResourceFetchCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "request", "auth_timeout", "missing_token", "credential_changed", "resource_unavailable", "ingestion", "queue_unavailable":
		return value
	default:
		return "unknown"
	}
}

func dereferenceTime(value *time.Time, fallback time.Time) time.Time {
	if value == nil {
		return fallback
	}
	return *value
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
