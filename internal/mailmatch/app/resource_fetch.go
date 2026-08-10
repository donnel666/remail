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

const ResourceFetchDefaultDispatchLimit = 10000

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

var ErrPermanentMicrosoftFetchFailureHandling = errors.New("permanent microsoft fetch failure handling failed")

func (e *MailFetchFailure) Error() string {
	if e == nil {
		return "mail fetch failed"
	}
	if safeMessage := strings.TrimSpace(e.SafeMessage); safeMessage != "" {
		return safeMessage
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

type AdminResourceFetchSubmitCommand struct {
	ResourceType   domain.ResourceType
	ResourceID     uint
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

type ResourceHistorySubmitCommand struct {
	ResourceID     uint
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

type resourceTaskSubmitCommand struct {
	Kind           domain.ResourceFetchJobKind
	ResourceType   domain.ResourceType
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

type AdminResourceFetchTask struct {
	ResourceID uint   `json:"resourceId"`
	Generation uint64 `json:"generation"`
	RequestID  string `json:"requestId"`
}

type ResourceHistoryTask struct {
	ResourceID uint   `json:"resourceId"`
	Generation uint64 `json:"generation"`
	RequestID  string `json:"requestId"`
}

type DispatchResourceFetchJobsResult struct {
	Attempted int
	Queued    int
	Failed    int
}

type resourceTaskRepository interface {
	CreateOrReuseResourceFetch(ctx context.Context, job *domain.ResourceFetchJob, log *governancedomain.OperationLog) (bool, error)
	FindResourceFetch(ctx context.Context, resourceID uint, generation uint64) (*domain.ResourceFetchJob, error)
	ListPendingResourceFetches(ctx context.Context, limit int) ([]domain.ResourceFetchJob, error)
	MarkResourceFetchProcessing(ctx context.Context, resourceID uint, generation uint64) (bool, error)
	ReleaseResourceFetchInfrastructureFailure(ctx context.Context, resourceID uint, generation uint64, safeError string, log *governancedomain.SystemLog) (bool, error)
	LoadResourceFetchScope(ctx context.Context, resourceID uint, expectedCredentialRevision uint64, resourceType domain.ResourceType) (*domain.ResourceFetchScope, error)
	MarkResourceFetchCanceled(ctx context.Context, resourceID uint, generation uint64, safeError string, now time.Time, log *governancedomain.SystemLog) error
	MarkResourceFetchFailure(ctx context.Context, resourceID uint, generation uint64, safeError string, retryable bool, now time.Time, log *governancedomain.SystemLog) (bool, error)
}

type AdminResourceFetchRepository interface {
	resourceTaskRepository
	AssertResourceFetchFence(ctx context.Context, resourceID uint, generation uint64, expectedCredentialRevision uint64) error
	CompleteResourceFetch(ctx context.Context, resourceID uint, generation uint64, expectedCredentialRevision uint64, rotatedRefreshToken string, fetched int, stored int, matched int, now time.Time, log *governancedomain.SystemLog) error
	AssertICloudResourceFetchFence(ctx context.Context, resourceID uint, generation uint64) error
	CompleteICloudResourceFetch(ctx context.Context, resourceID uint, generation uint64, fetched int, stored int, matched int, now time.Time, log *governancedomain.SystemLog) error
}

type ResourceHistoryRepository interface {
	resourceTaskRepository
	CompleteResourceFetchTask(ctx context.Context, resourceID uint, generation uint64, now time.Time, log *governancedomain.SystemLog) error
}

type AdminResourceFetchQueue interface {
	EnqueueAdminResourceFetch(ctx context.Context, task AdminResourceFetchTask) (bool, error)
	EnqueueAdminResourceFetchDispatcher(ctx context.Context, delay time.Duration) error
}

type ResourceHistoryQueue interface {
	EnqueueResourceHistory(ctx context.Context, task ResourceHistoryTask) (bool, error)
	EnqueueResourceHistoryDispatcher(ctx context.Context, delay time.Duration) error
}

type resourceTaskUseCase struct {
	repo         resourceTaskRepository
	adminRepo    AdminResourceFetchRepository
	historyRepo  ResourceHistoryRepository
	adminQueue   AdminResourceFetchQueue
	historyQueue ResourceHistoryQueue
	kind         domain.ResourceFetchJobKind
	transport    MailTransportFetchPort
	messages     *UseCase
	history      *ProjectHistoryScanUseCase
	systemLogs   governanceapp.SystemLogPort
	now          func() time.Time
}

type AdminResourceFetchUseCase struct{ task *resourceTaskUseCase }

type ResourceHistoryUseCase struct{ task *resourceTaskUseCase }

func NewAdminResourceFetchUseCase(
	repo AdminResourceFetchRepository,
	queue AdminResourceFetchQueue,
	transport MailTransportFetchPort,
	messages *UseCase,
	systemLogs governanceapp.SystemLogPort,
) *AdminResourceFetchUseCase {
	return &AdminResourceFetchUseCase{task: &resourceTaskUseCase{
		repo:       repo,
		adminRepo:  repo,
		adminQueue: queue,
		kind:       domain.ResourceFetchJobFetch,
		transport:  transport,
		messages:   messages,
		systemLogs: systemLogs,
		now:        func() time.Time { return time.Now().UTC() },
	}}
}

func NewResourceHistoryUseCase(
	repo ResourceHistoryRepository,
	queue ResourceHistoryQueue,
	history *ProjectHistoryScanUseCase,
	systemLogs governanceapp.SystemLogPort,
) *ResourceHistoryUseCase {
	return &ResourceHistoryUseCase{task: &resourceTaskUseCase{
		repo: repo, historyRepo: repo, historyQueue: queue, kind: domain.ResourceFetchJobHistory,
		history: history, systemLogs: systemLogs, now: func() time.Time { return time.Now().UTC() },
	}}
}

func (uc *AdminResourceFetchUseCase) Submit(ctx context.Context, cmd AdminResourceFetchSubmitCommand) (*ResourceFetchSubmitResult, error) {
	if uc == nil || uc.task == nil {
		return nil, domain.ErrInvalidRequest
	}
	return uc.task.submit(ctx, resourceTaskSubmitCommand{
		Kind: domain.ResourceFetchJobFetch, ResourceType: cmd.ResourceType, ResourceID: cmd.ResourceID,
		OperatorUserID: cmd.OperatorUserID, IdempotencyKey: cmd.IdempotencyKey, RequestID: cmd.RequestID, Path: cmd.Path,
	})
}

func (uc *ResourceHistoryUseCase) Submit(ctx context.Context, cmd ResourceHistorySubmitCommand) (*ResourceFetchSubmitResult, error) {
	if uc == nil || uc.task == nil {
		return nil, domain.ErrInvalidRequest
	}
	return uc.task.submit(ctx, resourceTaskSubmitCommand{
		Kind: domain.ResourceFetchJobHistory, ResourceType: domain.ResourceTypeMicrosoft, ResourceID: cmd.ResourceID,
		OperatorUserID: cmd.OperatorUserID, IdempotencyKey: cmd.IdempotencyKey, RequestID: cmd.RequestID, Path: cmd.Path,
	})
}

func (uc *resourceTaskUseCase) submit(ctx context.Context, cmd resourceTaskSubmitCommand) (*ResourceFetchSubmitResult, error) {
	if uc == nil || uc.repo == nil || cmd.ResourceID == 0 || cmd.OperatorUserID == 0 {
		return nil, domain.ErrInvalidRequest
	}
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if cmd.IdempotencyKey == "" || len(cmd.IdempotencyKey) > 128 {
		return nil, domain.ErrInvalidRequest
	}
	if !domain.IsValidResourceFetchJobKind(cmd.Kind) || cmd.Kind != uc.kind {
		return nil, domain.ErrInvalidRequest
	}
	if cmd.ResourceType == "" {
		cmd.ResourceType = domain.ResourceTypeMicrosoft
	}
	if (cmd.ResourceType != domain.ResourceTypeMicrosoft && cmd.ResourceType != domain.ResourceTypeICloud) ||
		(cmd.ResourceType == domain.ResourceTypeICloud && cmd.Kind != domain.ResourceFetchJobFetch) {
		return nil, domain.ErrInvalidRequest
	}
	now := uc.now()
	job := &domain.ResourceFetchJob{
		Kind:           cmd.Kind,
		ResourceType:   cmd.ResourceType,
		ResourceID:     cmd.ResourceID,
		OperatorUserID: cmd.OperatorUserID,
		Status:         domain.ResourceFetchJobQueued,
		MaxAttempts:    domain.ResourceFetchDefaultMaxAttempts,
		RequestID:      strings.TrimSpace(cmd.RequestID),
		Path:           strings.TrimSpace(cmd.Path),
		IdempotencyKey: cmd.IdempotencyKey,
	}
	if cmd.Kind == domain.ResourceFetchJobFetch {
		job.UntilAt = &now
	}
	log := &governancedomain.OperationLog{
		OperatorUserID: cmd.OperatorUserID,
		OperationType:  resourceFetchOperationType(cmd.ResourceType, cmd.Kind),
		ResourceType:   resourceFetchBizType(cmd.ResourceType),
		ResourceID:     fmt.Sprintf("%d", cmd.ResourceID),
		Path:           strings.TrimSpace(cmd.Path),
		Result:         "success",
		SafeSummary:    resourceFetchAcceptedSummary(cmd.ResourceType, cmd.Kind),
		RequestID:      strings.TrimSpace(cmd.RequestID),
	}
	reused, err := uc.repo.CreateOrReuseResourceFetch(ctx, job, log)
	if err != nil {
		return nil, err
	}
	uc.wakeDispatcher(ctx, *job)
	return &ResourceFetchSubmitResult{Job: *job, Reused: reused}, nil
}

func (uc *AdminResourceFetchUseCase) Process(ctx context.Context, task AdminResourceFetchTask) error {
	if uc == nil || uc.task == nil {
		return domain.ErrInvalidRequest
	}
	return uc.task.process(ctx, task.ResourceID, task.Generation)
}

func (uc *ResourceHistoryUseCase) Process(ctx context.Context, task ResourceHistoryTask) error {
	if uc == nil || uc.task == nil {
		return domain.ErrInvalidRequest
	}
	return uc.task.process(ctx, task.ResourceID, task.Generation)
}

func (uc *resourceTaskUseCase) process(ctx context.Context, resourceID uint, generation uint64) error {
	if uc == nil || uc.repo == nil || resourceID == 0 || generation == 0 {
		return domain.ErrInvalidRequest
	}
	claimed, err := uc.repo.MarkResourceFetchProcessing(ctx, resourceID, generation)
	if err != nil || !claimed {
		return err
	}
	job, err := uc.repo.FindResourceFetch(ctx, resourceID, generation)
	if err != nil {
		return uc.releaseResourceFetchInfrastructure(ctx, resourceID, generation, err)
	}
	if job == nil {
		return nil
	}
	if job.Kind == "" {
		job.Kind = uc.kind
	}
	if uc.kind != job.Kind {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, domain.ErrInvalidRequest)
	}
	if domain.IsTerminalResourceFetchStatus(job.Status) {
		return nil
	}
	platform.ObserveQueueWait(resourceFetchTaskMetric(uc.kind), job.CreatedAt)

	scope, err := uc.repo.LoadResourceFetchScope(ctx, job.ResourceID, job.ExpectedCredentialRevision, job.ResourceType)
	if err != nil {
		return uc.finishScopeFailure(ctx, *job, err)
	}
	if job.Kind == domain.ResourceFetchJobHistory {
		return uc.processResourceHistory(ctx, *job)
	}
	if job.ResourceType == domain.ResourceTypeICloud {
		return uc.processICloudResourceFetch(ctx, *job)
	}
	if uc.transport == nil {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, errors.New("microsoft mail transport is unavailable"))
	}
	fetched, err := uc.transport.FetchMicrosoftMessages(ctx, FetchMessagesRequest{
		Scope: OrderScope{
			OrderNo:            firstNonBlank(job.RequestID, fmt.Sprintf("resource-fetch-%d", job.ID)),
			AllocationType:     domain.ResourceTypeMicrosoft,
			EmailResourceID:    scope.ResourceID,
			Recipient:          scope.EmailAddress,
			MicrosoftEmail:     scope.EmailAddress,
			MicrosoftClientID:  scope.ClientID,
			MicrosoftRT:        scope.RefreshToken,
			CredentialRevision: job.ExpectedCredentialRevision,
		},
		SinceAt:     time.Time{},
		UntilAt:     dereferenceTime(job.UntilAt, uc.now()),
		RequestID:   job.RequestID,
		FullHistory: true,
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
		return uc.adminRepo.AssertResourceFetchFence(
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
	if err := uc.adminRepo.CompleteResourceFetch(
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

func (uc *resourceTaskUseCase) processICloudResourceFetch(ctx context.Context, job domain.ResourceFetchJob) error {
	if uc.messages == nil || uc.messages.iCloudPurchase == nil {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, errors.New("iCloud mail fetch service is unavailable"))
	}
	fetcher, hasFencedFetcher := uc.messages.iCloudPurchase.(ICloudResourceFetchPort)
	if !hasFencedFetcher || uc.adminRepo == nil {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, errors.New("fenced iCloud mail fetch infrastructure is unavailable"))
	}
	fetched, stored, matched, err := fetcher.FetchICloudResourceMailWithFence(ctx, job.ResourceID, func(txCtx context.Context) error {
		return uc.adminRepo.AssertICloudResourceFetchFence(txCtx, job.ResourceID, job.Generation)
	})
	if err != nil {
		if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
			return nil
		}
		if errors.Is(err, domain.ErrResourceFetchDeleted) || errors.Is(err, domain.ErrResourceFetchNotFound) {
			return uc.cancelResourceFetch(ctx, job, "iCloud resource changed while mail fetch was running.", "resource_unavailable")
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
		}
		return uc.retryResourceFetch(ctx, job, "iCloud mail service is temporarily unavailable.", "request", true, err)
	}
	now := uc.now()
	err = uc.adminRepo.CompleteICloudResourceFetch(
		ctx, job.ResourceID, job.Generation, fetched, stored, matched, now,
		resourceFetchSystemLog(job, "info", "resource_fetch_succeeded", "iCloud resource mail fetch completed.", ""),
	)
	if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
		return nil
	}
	if err != nil {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
	}
	return nil
}

func (uc *resourceTaskUseCase) processResourceHistory(ctx context.Context, job domain.ResourceFetchJob) error {
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
	err = uc.historyRepo.CompleteResourceFetchTask(
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

func (uc *AdminResourceFetchUseCase) DispatchPending(ctx context.Context, limit int) (*DispatchResourceFetchJobsResult, error) {
	if uc == nil || uc.task == nil {
		return nil, domain.ErrFetchQueueUnavailable
	}
	return uc.task.dispatchPending(ctx, limit)
}

func (uc *ResourceHistoryUseCase) DispatchPending(ctx context.Context, limit int) (*DispatchResourceFetchJobsResult, error) {
	if uc == nil || uc.task == nil {
		return nil, domain.ErrFetchQueueUnavailable
	}
	return uc.task.dispatchPending(ctx, limit)
}

func (uc *resourceTaskUseCase) dispatchPending(ctx context.Context, limit int) (*DispatchResourceFetchJobsResult, error) {
	if uc == nil || uc.repo == nil || (uc.adminQueue == nil && uc.historyQueue == nil) {
		return nil, domain.ErrFetchQueueUnavailable
	}
	if limit <= 0 {
		limit = ResourceFetchDefaultDispatchLimit
	}
	jobs, err := uc.repo.ListPendingResourceFetches(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := &DispatchResourceFetchJobsResult{Attempted: len(jobs)}
	var dispatchErrors []error
	for _, job := range jobs {
		accepted, err := uc.enqueueTask(ctx, job)
		if err != nil {
			result.Failed++
			dispatchErrors = append(dispatchErrors, fmt.Errorf("enqueue %s %d generation %d: %w", resourceFetchOperationLabel(uc.kind), job.ResourceID, job.Generation, err))
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

func (uc *resourceTaskUseCase) enqueueTask(ctx context.Context, job domain.ResourceFetchJob) (bool, error) {
	if uc.kind == domain.ResourceFetchJobHistory {
		return uc.historyQueue.EnqueueResourceHistory(ctx, ResourceHistoryTask{
			ResourceID: job.ResourceID, Generation: job.Generation, RequestID: job.RequestID,
		})
	}
	return uc.adminQueue.EnqueueAdminResourceFetch(ctx, AdminResourceFetchTask{
		ResourceID: job.ResourceID, Generation: job.Generation, RequestID: job.RequestID,
	})
}

func (uc *ResourceHistoryUseCase) ReleaseDispatch(ctx context.Context, task ResourceHistoryTask) error {
	if uc == nil || uc.task == nil {
		return nil
	}
	return uc.task.releaseDispatch(ctx, task.ResourceID, task.Generation)
}

func (uc *resourceTaskUseCase) releaseDispatch(ctx context.Context, resourceID uint, generation uint64) error {
	if uc == nil || uc.repo == nil || resourceID == 0 || generation == 0 {
		return nil
	}
	released, err := uc.repo.ReleaseResourceFetchInfrastructureFailure(
		ctx, resourceID, generation, "Resource fetch execution capacity is temporarily unavailable.", nil,
	)
	if released {
		uc.scheduleDispatcher(context.WithoutCancel(ctx), 0)
	}
	return err
}

func (uc *AdminResourceFetchUseCase) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if uc != nil && uc.task != nil {
		uc.task.scheduleDispatcher(ctx, delay)
	}
}

func (uc *ResourceHistoryUseCase) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if uc != nil && uc.task != nil {
		uc.task.scheduleDispatcher(ctx, delay)
	}
}

func (uc *resourceTaskUseCase) scheduleDispatcher(ctx context.Context, delay time.Duration) {
	_ = uc.enqueueDispatcher(ctx, delay)
}

func (uc *resourceTaskUseCase) enqueueDispatcher(ctx context.Context, delay time.Duration) error {
	if uc == nil {
		return domain.ErrFetchQueueUnavailable
	}
	if uc.kind == domain.ResourceFetchJobHistory {
		if uc.historyQueue == nil {
			return domain.ErrFetchQueueUnavailable
		}
		return uc.historyQueue.EnqueueResourceHistoryDispatcher(ctx, delay)
	}
	if uc.adminQueue == nil {
		return domain.ErrFetchQueueUnavailable
	}
	return uc.adminQueue.EnqueueAdminResourceFetchDispatcher(ctx, delay)
}

func (uc *resourceTaskUseCase) finishScopeFailure(ctx context.Context, job domain.ResourceFetchJob, err error) error {
	operation := resourceFetchOperationLabel(job.Kind)
	switch {
	case errors.Is(err, domain.ErrResourceFetchCredentialChanged):
		return uc.cancelResourceFetch(ctx, job, "Resource credentials changed before "+operation+" started.", "credential_changed")
	case errors.Is(err, domain.ErrResourceFetchDeleted), errors.Is(err, domain.ErrResourceFetchNotFound), errors.Is(err, domain.ErrResourceFetchJobConflict):
		return uc.cancelResourceFetch(ctx, job, "Resource is not available for "+operation+".", "resource_unavailable")
	case errors.Is(err, domain.ErrResourceFetchCredentialsMissing):
		return uc.retryResourceFetch(ctx, job, "Microsoft mail fetch credentials are incomplete.", "missing_token", false, err)
	default:
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
	}
}

func (uc *resourceTaskUseCase) releaseResourceFetchInfrastructure(ctx context.Context, resourceID uint, generation uint64, cause error) error {
	released, err := uc.repo.ReleaseResourceFetchInfrastructureFailure(
		context.WithoutCancel(ctx), resourceID, generation,
		"Resource fetch infrastructure is temporarily unavailable.", nil,
	)
	if err != nil {
		return errors.Join(cause, err)
	}
	if released {
		uc.scheduleDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	return nil
}

func (uc *resourceTaskUseCase) cancelResourceFetch(ctx context.Context, job domain.ResourceFetchJob, safe string, category string) error {
	now := uc.now()
	err := uc.repo.MarkResourceFetchCanceled(
		ctx,
		job.ResourceID,
		job.Generation,
		safe,
		now,
		resourceFetchSystemLog(job, "warning", resourceFetchEventType(job.Kind, "canceled"), safe, safeResourceFetchCategory(category)),
	)
	if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
		return nil
	}
	return err
}

func (uc *resourceTaskUseCase) retryResourceFetch(
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
		resourceFetchSystemLog(job, "warning", resourceFetchEventType(job.Kind, "failed"), safe, safeResourceFetchCategory(category)),
	)
	if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
		return nil
	}
	if err != nil {
		return err
	}
	if retryScheduled {
		uc.scheduleDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	// The business state owns retry/exhaustion; Asynq retry count is separate.
	_ = cause
	return nil
}

func (uc *resourceTaskUseCase) wakeDispatcher(ctx context.Context, job domain.ResourceFetchJob) {
	if uc == nil {
		return
	}
	err := uc.enqueueDispatcher(ctx, 0)
	if err == nil {
		return
	}
	if uc.systemLogs == nil {
		return
	}
	_ = uc.systemLogs.Create(context.WithoutCancel(ctx), resourceFetchSystemLog(
		job,
		"warning",
		resourceFetchEventType(job.Kind, "dispatch_wakeup_failed"),
		resourceFetchResourceLabel(job.ResourceType)+" resource "+resourceFetchOperationLabel(job.Kind)+" was saved and awaits dispatcher recovery.",
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
		safe = strings.TrimSpace(failure.SafeMessage)
		if safe == "" {
			safe = "Microsoft mail fetch failed."
		}
		return safe, category, false
	}
	return "Microsoft mail service is temporarily unavailable.", "request", true
}

func resourceFetchSystemLog(job domain.ResourceFetchJob, level string, eventType string, message string, detail string) *governancedomain.SystemLog {
	safeDetail := fmt.Sprintf("task=%s:%d; attempt=%d", resourceFetchTaskSource(job.Kind), job.ID, job.Attempts+1)
	if detail = strings.TrimSpace(detail); detail != "" {
		safeDetail += "; category=" + detail
	}
	return &governancedomain.SystemLog{
		Level:     level,
		Module:    "mailmatch",
		EventType: eventType,
		RequestID: job.RequestID,
		BizType:   resourceFetchBizType(job.ResourceType),
		BizID:     fmt.Sprintf("%d", job.ResourceID),
		Message:   strings.TrimSpace(message),
		Detail:    safeDetail,
	}
}

func resourceFetchTaskMetric(kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "mailmatch_resource_history"
	}
	return "mailmatch_admin_resource_fetch"
}

func resourceFetchTaskSource(kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "resource_history"
	}
	return "fetch"
}

func resourceFetchEventType(kind domain.ResourceFetchJobKind, event string) string {
	if kind == domain.ResourceFetchJobHistory {
		return "resource_history_" + event
	}
	return "resource_fetch_" + event
}

func resourceFetchOperationType(resourceType domain.ResourceType, kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "mailmatch.admin_resource.history_scan"
	}
	if resourceType == domain.ResourceTypeICloud {
		return "mailmatch.admin_icloud_resource.fetch"
	}
	return "mailmatch.admin_resource.fetch"
}

func resourceFetchAcceptedSummary(resourceType domain.ResourceType, kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "Microsoft resource project history scan accepted."
	}
	return resourceFetchResourceLabel(resourceType) + " resource mail fetch accepted."
}

func resourceFetchOperationLabel(kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "project history scan"
	}
	return "mail fetch"
}

func resourceFetchBizType(resourceType domain.ResourceType) string {
	if resourceType == domain.ResourceTypeICloud {
		return governanceapp.AdminTaskBizICloudResource
	}
	return governanceapp.AdminTaskBizMicrosoftResource
}

func resourceFetchResourceLabel(resourceType domain.ResourceType) string {
	if resourceType == domain.ResourceTypeICloud {
		return "iCloud"
	}
	return "Microsoft"
}

func safeResourceFetchCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "request", "auth_timeout", "rate_limited", "missing_token", "credential_changed", "resource_unavailable", "ingestion", "queue_unavailable",
		"oauth_invalid_grant", "refresh_token_expired", "oauth_refresh_token_expired", "oauth_client", "oauth_permission",
		"mfa", "passkey", "phone", "password", "unknown_mailbox", "locked", "account_abnormal",
		"graph_unauthorized", "graph_forbidden", "imap_auth_failed", "identity_mismatch":
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
