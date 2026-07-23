package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
)

const (
	resourceFetchLookbackWindow       = 90 * 24 * time.Hour
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
	Kind           domain.ResourceFetchJobKind
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
	ResourceID uint   `json:"resourceId"`
	Generation uint64 `json:"generation"`
	RequestID  string `json:"requestId"`
}

type DispatchResourceFetchJobsResult struct {
	Attempted int
	Queued    int
	Failed    int
}

type ResourceFetchRepository interface {
	CreateOrReuseResourceFetch(ctx context.Context, job *domain.ResourceFetchJob, log *governancedomain.OperationLog) (bool, error)
	FindResourceFetch(ctx context.Context, resourceID uint, generation uint64) (*domain.ResourceFetchJob, error)
	ListPendingResourceFetches(ctx context.Context, limit int) ([]domain.ResourceFetchJob, error)
	MarkResourceFetchProcessing(ctx context.Context, resourceID uint, generation uint64) (bool, error)
	ReleaseResourceFetchInfrastructureFailure(ctx context.Context, resourceID uint, generation uint64, safeError string, log *governancedomain.SystemLog) (bool, error)
	LoadResourceFetchScope(ctx context.Context, resourceID uint, expectedCredentialRevision uint64) (*domain.ResourceFetchScope, error)
	AssertResourceFetchFence(ctx context.Context, resourceID uint, generation uint64, expectedCredentialRevision uint64) error
	CompleteResourceFetch(ctx context.Context, resourceID uint, generation uint64, expectedCredentialRevision uint64, rotatedRefreshToken string, fetched int, stored int, matched int, now time.Time, log *governancedomain.SystemLog) error
	CompleteResourceFetchTask(ctx context.Context, resourceID uint, generation uint64, now time.Time, log *governancedomain.SystemLog) error
	MarkResourceFetchCanceled(ctx context.Context, resourceID uint, generation uint64, safeError string, now time.Time, log *governancedomain.SystemLog) error
	MarkResourceFetchFailure(ctx context.Context, resourceID uint, generation uint64, safeError string, retryable bool, now time.Time, log *governancedomain.SystemLog) (bool, error)
}

type ResourceFetchQueue interface {
	EnqueueResourceFetch(ctx context.Context, task ResourceFetchTask) (bool, error)
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
	history    *ProjectHistoryScanUseCase
	systemLogs governanceapp.SystemLogPort
	now        func() time.Time
}

func (uc *ResourceFetchUseCase) SetProjectHistoryScan(history *ProjectHistoryScanUseCase) {
	if uc != nil {
		uc.history = history
	}
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
	if cmd.Kind == "" {
		cmd.Kind = domain.ResourceFetchJobFetch
	}
	if !domain.IsValidResourceFetchJobKind(cmd.Kind) {
		return nil, domain.ErrInvalidRequest
	}
	now := uc.now()
	job := &domain.ResourceFetchJob{
		Kind:           cmd.Kind,
		ResourceID:     cmd.ResourceID,
		OperatorUserID: cmd.OperatorUserID,
		Status:         domain.ResourceFetchJobQueued,
		MaxAttempts:    domain.ResourceFetchDefaultMaxAttempts,
		RequestID:      strings.TrimSpace(cmd.RequestID),
		Path:           strings.TrimSpace(cmd.Path),
		IdempotencyKey: cmd.IdempotencyKey,
	}
	if cmd.Kind == domain.ResourceFetchJobFetch {
		sinceAt := now.Add(-resourceFetchLookbackWindow)
		job.SinceAt = &sinceAt
		job.UntilAt = &now
	}
	log := &governancedomain.OperationLog{
		OperatorUserID: cmd.OperatorUserID,
		OperationType:  resourceFetchOperationType(cmd.Kind),
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", cmd.ResourceID),
		Path:           strings.TrimSpace(cmd.Path),
		Result:         "success",
		SafeSummary:    resourceFetchAcceptedSummary(cmd.Kind),
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
	if uc == nil || uc.repo == nil || task.ResourceID == 0 || task.Generation == 0 {
		return domain.ErrInvalidRequest
	}
	claimed, err := uc.repo.MarkResourceFetchProcessing(ctx, task.ResourceID, task.Generation)
	if err != nil || !claimed {
		return err
	}
	job, err := uc.repo.FindResourceFetch(ctx, task.ResourceID, task.Generation)
	if err != nil {
		return uc.releaseResourceFetchInfrastructure(ctx, task.ResourceID, task.Generation, err)
	}
	if job == nil {
		return nil
	}
	if domain.IsTerminalResourceFetchStatus(job.Status) {
		return nil
	}
	platform.ObserveQueueWait("mailmatch_resource_fetch", job.CreatedAt)

	scope, err := uc.repo.LoadResourceFetchScope(ctx, job.ResourceID, job.ExpectedCredentialRevision)
	if err != nil {
		return uc.finishScopeFailure(ctx, *job, err)
	}
	if job.Kind == domain.ResourceFetchJobHistory {
		return uc.processResourceHistory(ctx, *job)
	}
	if uc.transport == nil {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, errors.New("microsoft mail transport is unavailable"))
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
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
		}
		safe, category, retryable := classifyResourceFetchFailure(err)
		return uc.retryResourceFetch(ctx, *job, safe, category, retryable, err)
	}
	if fetched == nil {
		return uc.retryResourceFetch(ctx, *job, "Microsoft mail service is temporarily unavailable.", "request", true, domain.ErrMailServiceUnavailable)
	}

	if uc.messages == nil {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, errors.New("mailmatch message use case is unavailable"))
	}
	stored, matched, _, err := uc.messages.ingestFetchedMessagesForResourcesWithFence(ctx, fetched.Messages, domain.ResourceTypeMicrosoft, []uint{job.ResourceID}, func(txCtx context.Context) error {
		return uc.repo.AssertResourceFetchFence(
			txCtx,
			job.ResourceID,
			job.Generation,
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
		if safe == "Mail match result notification failed." {
			return uc.retryResourceFetch(ctx, *job, safe, "ingestion", true, err)
		}
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
	}

	now := uc.now()
	if err := uc.repo.CompleteResourceFetch(
		ctx,
		job.ResourceID,
		job.Generation,
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
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
	}
	return nil
}

func (uc *ResourceFetchUseCase) processResourceHistory(ctx context.Context, job domain.ResourceFetchJob) error {
	if uc.history == nil {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, errors.New("project history scan service is unavailable"))
	}
	err := uc.history.scanValidatedMicrosoftHistory(ctx, ValidatedMicrosoftHistoryScanTask{
		ResourceID: job.ResourceID, RequestID: job.RequestID,
	}, job.ExpectedCredentialRevision)
	if err != nil {
		switch {
		case errors.Is(err, coreapp.ErrMicrosoftCredentialChanged):
			return uc.cancelResourceFetch(ctx, job, "Resource credentials changed before project history scan completed.", "credential_changed")
		case errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted), errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound):
			return uc.cancelResourceFetch(ctx, job, "Resource is not available for project history scan.", "resource_unavailable")
		}
		failure := (*MailFetchFailure)(nil)
		if errors.As(err, &failure) && failure != nil {
			safe, category, retryable := classifyResourceFetchFailure(err)
			return uc.retryResourceFetch(ctx, job, safe, category, retryable, err)
		}
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
	}
	now := uc.now()
	err = uc.repo.CompleteResourceFetchTask(
		ctx, job.ResourceID, job.Generation, now,
		resourceFetchSystemLog(job, "info", "resource_history_scan_succeeded", "Microsoft resource project history scan completed.", ""),
	)
	if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
		return nil
	}
	if err != nil {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
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
	jobs, err := uc.repo.ListPendingResourceFetches(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := &DispatchResourceFetchJobsResult{Attempted: len(jobs)}
	var dispatchErrors []error
	for _, job := range jobs {
		accepted, err := uc.queue.EnqueueResourceFetch(ctx, ResourceFetchTask{
			ResourceID: job.ResourceID,
			Generation: job.Generation,
			RequestID:  job.RequestID,
		})
		if err != nil {
			result.Failed++
			dispatchErrors = append(dispatchErrors, fmt.Errorf("enqueue resource fetch %d generation %d: %w", job.ResourceID, job.Generation, err))
			continue
		}
		if !accepted {
			continue
		}
		processing, markErr := uc.repo.MarkResourceFetchProcessing(ctx, job.ResourceID, job.Generation)
		if markErr != nil {
			result.Failed++
			dispatchErrors = append(dispatchErrors, markErr)
			continue
		}
		if processing {
			result.Queued++
		}
	}
	return result, errors.Join(dispatchErrors...)
}

func (uc *ResourceFetchUseCase) ReleaseDispatch(ctx context.Context, task ResourceFetchTask) error {
	if uc == nil || uc.repo == nil || task.ResourceID == 0 || task.Generation == 0 {
		return nil
	}
	released, err := uc.repo.ReleaseResourceFetchInfrastructureFailure(
		ctx, task.ResourceID, task.Generation, "Microsoft resource fetch execution capacity is temporarily unavailable.", nil,
	)
	if released {
		uc.ScheduleDispatcher(context.WithoutCancel(ctx), 0)
	}
	return err
}

func (uc *ResourceFetchUseCase) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if uc == nil || uc.queue == nil {
		return
	}
	_ = uc.queue.EnqueueFetchDispatcher(ctx, delay)
}

func (uc *ResourceFetchUseCase) finishScopeFailure(ctx context.Context, job domain.ResourceFetchJob, err error) error {
	operation := resourceFetchOperationLabel(job.Kind)
	switch {
	case errors.Is(err, domain.ErrResourceFetchCredentialChanged):
		return uc.cancelResourceFetch(ctx, job, "Resource credentials changed before "+operation+" started.", "credential_changed")
	case errors.Is(err, domain.ErrResourceFetchDeleted), errors.Is(err, domain.ErrResourceFetchNotFound):
		return uc.cancelResourceFetch(ctx, job, "Resource is not available for "+operation+".", "resource_unavailable")
	case errors.Is(err, domain.ErrResourceFetchCredentialsMissing):
		return uc.retryResourceFetch(ctx, job, "Microsoft mail fetch credentials are incomplete.", "missing_token", false, err)
	default:
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
	}
}

func (uc *ResourceFetchUseCase) releaseResourceFetchInfrastructure(ctx context.Context, resourceID uint, generation uint64, cause error) error {
	released, err := uc.repo.ReleaseResourceFetchInfrastructureFailure(
		context.WithoutCancel(ctx), resourceID, generation,
		"Microsoft resource fetch infrastructure is temporarily unavailable.", nil,
	)
	if err != nil {
		return errors.Join(cause, err)
	}
	if released {
		uc.ScheduleDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	return nil
}

func (uc *ResourceFetchUseCase) cancelResourceFetch(ctx context.Context, job domain.ResourceFetchJob, safe string, category string) error {
	now := uc.now()
	err := uc.repo.MarkResourceFetchCanceled(
		ctx,
		job.ResourceID,
		job.Generation,
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
		job.ResourceID,
		job.Generation,
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
	// The business state owns retry/exhaustion; Asynq retry count is separate.
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
		"Microsoft resource "+resourceFetchOperationLabel(job.Kind)+" was saved and awaits dispatcher recovery.",
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

func resourceFetchOperationType(kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "mailmatch.admin_resource.history_scan"
	}
	return "mailmatch.admin_resource.fetch"
}

func resourceFetchAcceptedSummary(kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "Microsoft resource project history scan accepted."
	}
	return "Microsoft resource mail fetch accepted."
}

func resourceFetchOperationLabel(kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "project history scan"
	}
	return "mail fetch"
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
