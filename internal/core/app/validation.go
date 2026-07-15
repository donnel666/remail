package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

// ResourceValidationRepository persists durable validation jobs and applies
// resource state changes after the external ACL has finished.
type ResourceValidationRepository interface {
	CreateWithLog(ctx context.Context, job *domain.ResourceValidation, log *governancedomain.OperationLog) (bool, error)
	CreateBatchWithLog(ctx context.Context, ownerUserID uint, selection ResourceBulkSelection, log *governancedomain.OperationLog, requestID, path string) (*ResourceBatchValidationResult, error)
	CreateDeferredBatchWithLog(ctx context.Context, ownerUserID uint, selection ResourceBulkSelection, log *governancedomain.OperationLog, requestID, path string) (*ResourceBatchValidationResult, error)
	FindByID(ctx context.Context, id uint) (*domain.ResourceValidation, error)
	ClaimDispatchable(ctx context.Context, limit int, runningStaleBefore time.Time, queuedDispatchStaleBefore time.Time) ([]domain.ResourceValidation, error)
	ResumeValidationBatches(ctx context.Context, candidateLimit int) (int, error)
	MarkRunning(ctx context.Context, id uint, dispatchToken string) (claimToken string, claimed bool, err error)
	ReleaseDispatch(ctx context.Context, id uint, dispatchToken string) error
	MarkFailed(ctx context.Context, id uint, claimToken string, safeError string) error
	MarkRetryableFailure(ctx context.Context, id uint, claimToken string, safeError string) (bool, error)
	SaveMicrosoftProgress(ctx context.Context, jobID uint, resourceID uint, claimToken string, result MicrosoftValidationResult) error
	ApplyMicrosoftResult(ctx context.Context, jobID uint, resourceID uint, claimToken string, result MicrosoftValidationResult, systemLog *governancedomain.SystemLog) error
	ApplyDomainResult(ctx context.Context, jobID uint, resourceID uint, claimToken string, result DomainValidationResult, systemLog *governancedomain.SystemLog) error
	MarkDispatchFailed(ctx context.Context, id uint, dispatchToken string, safeError string) error
}

// ResourceValidationQueue enqueues asynchronous resource validation work.
type ResourceValidationQueue interface {
	EnqueueResourceValidation(ctx context.Context, task ResourceValidationTask) error
	EnqueueResourceValidationDispatcher(ctx context.Context, delay time.Duration) error
}

// ResourceValidationPort is implemented by BC-MAILTRANSPORT ACL adapters.
type ResourceValidationPort interface {
	ValidateMicrosoft(ctx context.Context, req MicrosoftValidationRequest) (MicrosoftValidationResult, error)
	ValidateDomain(ctx context.Context, req DomainValidationRequest) (DomainValidationResult, error)
}

// MicrosoftValidationBindingCommitPort is implemented by MailTransport. Core
// invokes it only from fenced validation progress/result transactions, after
// the resource root, Microsoft subtype, and running validation job have all
// been locked and the expected credential revision has been checked.
// Implementations must join the caller-owned transaction carried by ctx.
type MicrosoftValidationBindingCommitPort interface {
	CommitValidationBinding(ctx context.Context, command MicrosoftValidationBindingCommand) (changed bool, err error)
}

// MicrosoftAliasScheduleTriggerPort is implemented by MailTransport. It is
// called only after Core has committed a successful Microsoft validation; it
// owns the separate durable alias schedule and never participates in Core's
// validation transaction.
type MicrosoftAliasScheduleTriggerPort interface {
	EnsureForValidatedMicrosoftResource(ctx context.Context, resourceID uint) error
}

type MicrosoftValidationRequest struct {
	ResourceID   uint
	OwnerUserID  uint
	EmailAddress string
	Password     string
	ClientID     string
	RefreshToken string
	RequestID    string
}

type MicrosoftValidationResult struct {
	Valid        bool
	ClientID     string
	RefreshToken string
	// CredentialsAuthoritative is set only after a refresh-token exchange or
	// password OAuth flow actually succeeded. It lets Core preserve a rotated
	// credential even when a later binding/fetch gate makes the overall
	// validation result invalid, without trusting arbitrary non-empty fields
	// returned by a failed protocol step.
	CredentialsAuthoritative bool
	RTExpireAt               *time.Time
	GraphAvailable           bool
	Category                 string
	SafeMessage              string
	// RecoveredBinding is a complete, uniquely resolved, locally receivable
	// recovery-mailbox fact. It intentionally contains no proof mask, token,
	// code, or other Microsoft protocol material.
	RecoveredBinding   *MicrosoftRecoveredBinding
	BindingObservation *MicrosoftBindingObservation
}

// MicrosoftRecoveredBinding carries the optimistic binding snapshot captured
// before the remote proof lookup. ExpectedBindingID == 0 means no binding row
// existed; otherwise ID, address, and updated_at must all still match when Core
// commits the validation result.
type MicrosoftRecoveredBinding struct {
	Address                  string
	ExpectedBindingID        uint
	ExpectedBindingAddress   string
	ExpectedBindingUpdatedAt time.Time
}

// MicrosoftBindingObservation is the ordinary binding outcome produced by a
// validation login. Unlike RecoveredBinding it carries no recovery proof and
// cannot replace a concurrently changed binding outside Core's job fence.
type MicrosoftBindingObservation struct {
	Address      string
	Status       string
	BoundDisplay string
	SafeMessage  string
}

// MicrosoftValidationBindingCommand is assembled by Core from the fenced
// validation result and the currently locked Microsoft resource. AccountEmail
// therefore never comes from an unfenced external protocol response.
type MicrosoftValidationBindingCommand struct {
	ResourceID         uint
	OwnerUserID        uint
	AccountEmail       string
	RecoveredBinding   *MicrosoftRecoveredBinding
	BindingObservation *MicrosoftBindingObservation
}

type DomainValidationRequest struct {
	ResourceID uint
	Domain     string
	MXRecord   string
	RequestID  string
}

type DomainValidationResult struct {
	Valid       bool
	Category    string
	SafeMessage string
}

type ResourceValidationTask struct {
	JobID         uint   `json:"jobId"`
	DispatchToken string `json:"dispatchToken"`
	RequestID     string `json:"requestId"`
}

type DispatchResourceValidationJobsResult struct {
	Attempted int
	Queued    int
	Failed    int
}

// ResourceBatchValidationResult reports a bulk validation submission.
// For an explicit-ID selection, Requested and Queued count IDs durably
// accepted into the deferred batch; they do not imply that child jobs were
// expanded before the HTTP response. Filter counts remain unknown (zero) until
// the dispatcher expands the batch. Created is an internal expansion count.
type ResourceBatchValidationResult struct {
	Requested int
	Queued    int
	Created   int
}

type ValidationResultView struct {
	ValidationID       uint       `json:"validationId"`
	ResourceID         uint       `json:"resourceId"`
	ResourceType       string     `json:"resourceType"`
	Status             string     `json:"status"`
	CredentialRevision uint64     `json:"credentialRevision,omitempty"`
	Attempts           int        `json:"attempts"`
	MaxAttempts        int        `json:"maxAttempts"`
	LastSafeError      string     `json:"lastSafeError,omitempty"`
	RequestID          string     `json:"requestId"`
	StartedAt          *time.Time `json:"startedAt,omitempty"`
	FinishedAt         *time.Time `json:"finishedAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type ResourceValidationUseCase struct {
	resources    EmailResourceRepository
	validations  ResourceValidationRepository
	queue        ResourceValidationQueue
	validator    ResourceValidationPort
	aliasTrigger MicrosoftAliasScheduleTriggerPort
	now          func() time.Time
}

func (uc *ResourceValidationUseCase) SetMicrosoftAliasScheduleTrigger(trigger MicrosoftAliasScheduleTriggerPort) {
	if uc != nil {
		uc.aliasTrigger = trigger
	}
}

const ResourceValidationMaxExplicitIDs = 10_000

const (
	resourceValidationRunningStaleAfter = 20 * time.Minute
	resourceValidationQueuedLease       = time.Hour
)

var ErrValidationTemporaryUnavailable = errors.New("resource validation temporary unavailable")

// ErrValidationResultStale means the durable job was fenced off because the
// resource credentials or command state changed while the external Microsoft
// call was in flight. The repository has already made the job terminal; the
// worker must not retry the stale payload or overwrite the newer state.
var ErrValidationResultStale = errors.New("resource validation result is stale")

// ErrValidationBindingRejected means the remote validation resolved a binding
// fact that cannot be committed as this resource's active recovery mailbox
// (for example, the address is already assigned or its local binding domain is
// no longer active). This is a terminal validation result, not a stale worker
// fence and not a transient infrastructure failure.
var ErrValidationBindingRejected = errors.New("resource validation binding was rejected")

const MicrosoftValidationBindingRejectedMessage = "Microsoft recovery mailbox is unavailable or already assigned."

func NewResourceValidationUseCase(resources EmailResourceRepository, validations ResourceValidationRepository, queue ResourceValidationQueue, validator ResourceValidationPort) *ResourceValidationUseCase {
	return &ResourceValidationUseCase{
		resources:   resources,
		validations: validations,
		queue:       queue,
		validator:   validator,
		now:         func() time.Time { return time.Now().UTC() },
	}
}

func (uc *ResourceValidationUseCase) Create(ctx context.Context, resourceID uint, userID uint, isAdmin bool, requestID, path string) (*ValidationResultView, error) {
	resource, err := uc.resources.FindByID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		return nil, domain.ErrResourceNotFound
	}
	if !isAdmin && resource.OwnerUserID != userID {
		return nil, domain.ErrForbiddenResource
	}

	switch resource.Type {
	case domain.ResourceTypeMicrosoft:
		ms, err := uc.resources.FindMicrosoftByID(ctx, resourceID)
		if err != nil {
			return nil, err
		}
		if ms == nil || ms.Status == domain.MicrosoftStatusDeleted {
			return nil, domain.ErrResourceNotFound
		}
		if ms.Status == domain.MicrosoftStatusDisabled {
			return nil, domain.ErrInvalidResourceStatus
		}
	case domain.ResourceTypeDomain:
		dr, err := uc.resources.FindDomainByID(ctx, resourceID)
		if err != nil {
			return nil, err
		}
		if dr == nil || dr.Status == domain.DomainStatusDeleted {
			return nil, domain.ErrResourceNotFound
		}
		if dr.Status == domain.DomainStatusDisabled {
			return nil, domain.ErrInvalidResourceStatus
		}
		if !isAdmin && dr.Purpose == domain.PurposeBinding {
			return nil, domain.ErrForbiddenResource
		}
	default:
		return nil, domain.ErrInvalidResourceType
	}

	job := &domain.ResourceValidation{
		ResourceID:   resource.ID,
		ResourceType: resource.Type,
		OwnerUserID:  resource.OwnerUserID,
		Status:       domain.ResourceValidationQueued,
		MaxAttempts:  domain.ResourceValidationDefaultMaxAttempts,
		RequestID:    strings.TrimSpace(requestID),
		Path:         strings.TrimSpace(path),
	}
	log := &governancedomain.OperationLog{
		OperatorUserID: userID,
		OperationType:  "core.resource.validate",
		ResourceType:   "resource",
		ResourceID:     fmt.Sprintf("%d", resource.ID),
		Path:           path,
		Result:         "success",
		SafeSummary:    "Resource validation queued.",
		RequestID:      requestID,
	}
	created, err := uc.validations.CreateWithLog(ctx, job, log)
	if err != nil {
		return nil, err
	}
	if !created {
		return validationView(job), nil
	}

	uc.ScheduleDispatcher(ctx, 0)

	return validationView(job), nil
}

func (uc *ResourceValidationUseCase) CreateBatch(ctx context.Context, selection ResourceBulkSelection, userID uint, isAdmin bool, requestID, path string) (*ResourceBatchValidationResult, error) {
	selection.AllowBinding = isAdmin
	if !selection.AllowBinding {
		selection.Filter.ExcludeBinding = true
	}
	switch selection.Mode {
	case ResourceBulkSelectionIDs:
		if len(selection.ResourceIDs) > ResourceValidationMaxExplicitIDs {
			return nil, domain.ErrResourceSelectionTooLarge
		}
		ids := uniqueResourceIDs(selection.ResourceIDs)
		if len(ids) == 0 {
			return nil, domain.ErrResourceNotFound
		}
		selection.ResourceIDs = ids
	case ResourceBulkSelectionFilter:
		filter, err := normalizeResourceBulkFilter(selection.Filter)
		if err != nil {
			return nil, err
		}
		selection.Filter = filter
	default:
		return nil, domain.ErrInvalidResourceType
	}

	log := &governancedomain.OperationLog{
		OperatorUserID: userID,
		OperationType:  "core.resource.validate_batch",
		ResourceType:   "resource",
		ResourceID:     "batch",
		Path:           path,
		Result:         "success",
		SafeSummary:    "Resource validations queued.",
		RequestID:      requestID,
	}
	result, err := uc.validations.CreateBatchWithLog(ctx, userID, selection, log, requestID, path)
	if err != nil {
		return nil, err
	}
	if result != nil && result.Queued > 0 {
		uc.ScheduleDispatcher(ctx, 0)
	}
	return result, nil
}

func (uc *ResourceValidationUseCase) CreateDeferredBatch(ctx context.Context, selection ResourceBulkSelection, userID uint, requestID, path string) (*ResourceBatchValidationResult, error) {
	switch selection.Mode {
	case ResourceBulkSelectionIDs:
		if len(selection.ResourceIDs) > ResourceValidationMaxExplicitIDs {
			return nil, domain.ErrResourceSelectionTooLarge
		}
		ids := uniqueResourceIDs(selection.ResourceIDs)
		if len(ids) == 0 {
			return nil, domain.ErrResourceNotFound
		}
		selection.ResourceIDs = ids
	case ResourceBulkSelectionFilter:
		filter, err := normalizeResourceBulkFilter(selection.Filter)
		if err != nil {
			return nil, err
		}
		selection.Filter = filter
	default:
		return nil, domain.ErrInvalidResourceType
	}
	log := &governancedomain.OperationLog{
		OperatorUserID: userID,
		OperationType:  "core.resource.validate_batch",
		ResourceType:   "resource",
		ResourceID:     "batch",
		Path:           path,
		Result:         "success",
		SafeSummary:    "Resource validation batch accepted.",
		RequestID:      requestID,
	}
	result, err := uc.validations.CreateDeferredBatchWithLog(ctx, userID, selection, log, requestID, path)
	if err != nil {
		return nil, err
	}
	uc.ScheduleDispatcher(ctx, 0)
	return result, nil
}

func (uc *ResourceValidationUseCase) Get(ctx context.Context, validationID uint, userID uint, isAdmin bool) (*ValidationResultView, error) {
	job, err := uc.validations.FindByID(ctx, validationID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, domain.ErrResourceNotFound
	}
	if !isAdmin && job.OwnerUserID != userID {
		return nil, domain.ErrForbiddenResource
	}
	if !isAdmin && job.ResourceType == domain.ResourceTypeDomain {
		if uc.resources == nil {
			return nil, domain.ErrForbiddenResource
		}
		resource, err := uc.resources.FindDomainByID(ctx, job.ResourceID)
		if err != nil {
			return nil, err
		}
		if resource == nil || resource.Purpose == domain.PurposeBinding {
			return nil, domain.ErrForbiddenResource
		}
	}
	return validationView(job), nil
}

func (uc *ResourceValidationUseCase) Process(ctx context.Context, task ResourceValidationTask) error {
	if task.JobID == 0 {
		return domain.ErrResourceNotFound
	}
	if strings.TrimSpace(task.DispatchToken) == "" {
		// Payloads queued before durable dispatch fencing was introduced are
		// harmless no-ops. The dispatcher will create a fresh fenced task.
		return nil
	}
	job, err := uc.validations.FindByID(ctx, task.JobID)
	if err != nil {
		return err
	}
	if job == nil {
		return domain.ErrResourceNotFound
	}
	if domain.IsTerminalValidationStatus(job.Status) {
		return nil
	}

	claimToken, claimed, err := uc.validations.MarkRunning(ctx, task.JobID, task.DispatchToken)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	job.ClaimToken = claimToken

	var processErr error
	switch job.ResourceType {
	case domain.ResourceTypeMicrosoft:
		processErr = uc.processMicrosoft(ctx, job)
	case domain.ResourceTypeDomain:
		processErr = uc.processDomain(ctx, job)
	default:
		processErr = domain.ErrInvalidResourceType
	}
	if processErr == nil {
		return nil
	}
	if errors.Is(processErr, ErrValidationTemporaryUnavailable) {
		uc.ScheduleDispatcher(ctx, time.Second)
		// The durable job has already returned to queued with its attempt count
		// updated. Retrying this Asynq payload would carry a consumed dispatch
		// token and can only be a no-op; the dispatcher owns the next generation.
		return nil
	}

	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, recoveryErr := uc.validations.MarkRetryableFailure(
		recoveryCtx,
		job.ID,
		job.ClaimToken,
		"Resource validation processing failed temporarily.",
	); recoveryErr == nil || errors.Is(recoveryErr, domain.ErrInvalidResourceStatus) {
		return nil
	}
	return processErr
}

func (uc *ResourceValidationUseCase) DispatchPending(ctx context.Context, limit int) (*DispatchResourceValidationJobsResult, error) {
	if uc == nil || uc.validations == nil || uc.queue == nil {
		return nil, ErrValidationTemporaryUnavailable
	}
	if limit <= 0 {
		limit = 100
	}
	now := uc.now()
	jobs, err := uc.validations.ClaimDispatchable(
		ctx,
		limit,
		now.Add(-resourceValidationRunningStaleAfter),
		now.Add(-resourceValidationQueuedLease),
	)
	if err != nil {
		return nil, err
	}
	var dispatchErrors []error
	if remaining := limit - len(jobs); remaining > 0 {
		if _, err := uc.validations.ResumeValidationBatches(ctx, remaining); err != nil {
			dispatchErrors = append(dispatchErrors, fmt.Errorf("expand resource validation batches: %w", err))
		} else {
			expandedJobs, err := uc.validations.ClaimDispatchable(
				ctx,
				remaining,
				now.Add(-resourceValidationRunningStaleAfter),
				now.Add(-resourceValidationQueuedLease),
			)
			if err != nil {
				dispatchErrors = append(dispatchErrors, fmt.Errorf("claim expanded resource validations: %w", err))
			} else {
				jobs = append(jobs, expandedJobs...)
			}
		}
	}
	result := &DispatchResourceValidationJobsResult{Attempted: len(jobs)}
	for _, job := range jobs {
		err := uc.queue.EnqueueResourceValidation(ctx, ResourceValidationTask{
			JobID:         job.ID,
			DispatchToken: job.DispatchToken,
			RequestID:     job.RequestID,
		})
		if err != nil {
			result.Failed++
			dispatchErrors = append(dispatchErrors, fmt.Errorf("enqueue validation job %d: %w", job.ID, err))
			if releaseErr := uc.validations.MarkDispatchFailed(ctx, job.ID, job.DispatchToken, "Resource validation queue is unavailable; dispatcher will retry."); releaseErr != nil {
				dispatchErrors = append(dispatchErrors, fmt.Errorf("release validation job %d after enqueue failure: %w", job.ID, releaseErr))
			}
			continue
		}
		result.Queued++
	}
	return result, errors.Join(dispatchErrors...)
}

// ReleaseDispatch returns a fenced task to the durable dispatcher without
// changing its validation lifecycle. It is used when runtime admission control
// decides that background work should yield before the job starts.
func (uc *ResourceValidationUseCase) ReleaseDispatch(ctx context.Context, task ResourceValidationTask) error {
	if uc == nil || uc.validations == nil || task.JobID == 0 || strings.TrimSpace(task.DispatchToken) == "" {
		return nil
	}
	return uc.validations.ReleaseDispatch(ctx, task.JobID, task.DispatchToken)
}

func (uc *ResourceValidationUseCase) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if uc == nil || uc.queue == nil {
		return
	}
	_ = uc.queue.EnqueueResourceValidationDispatcher(ctx, delay)
}

func (uc *ResourceValidationUseCase) processMicrosoft(ctx context.Context, job *domain.ResourceValidation) error {
	ms, err := uc.resources.FindMicrosoftByID(ctx, job.ResourceID)
	if err != nil {
		return err
	}
	if ms == nil || ms.Status == domain.MicrosoftStatusDeleted {
		return uc.markValidationFailed(ctx, job.ID, job.ClaimToken, "Resource not found.")
	}
	if ms.Status == domain.MicrosoftStatusDisabled {
		return uc.markValidationFailed(ctx, job.ID, job.ClaimToken, "Resource status does not allow validation.")
	}
	var result MicrosoftValidationResult
	if uc.validator == nil {
		return uc.markValidationRetryableFailure(ctx, job.ID, job.ClaimToken, "Microsoft mail service is temporarily unavailable.")
	}
	result, err = uc.validator.ValidateMicrosoft(ctx, MicrosoftValidationRequest{
		ResourceID:   job.ResourceID,
		OwnerUserID:  job.OwnerUserID,
		EmailAddress: ms.EmailAddress,
		Password:     ms.Password,
		ClientID:     ms.ClientID,
		RefreshToken: ms.RefreshToken,
		RequestID:    job.RequestID,
	})
	if err != nil {
		return uc.markValidationRetryableFailure(ctx, job.ID, job.ClaimToken, "Microsoft mail service is temporarily unavailable.")
	}
	if result.SafeMessage == "" {
		if result.Valid {
			result.SafeMessage = "Microsoft resource validation succeeded."
		} else {
			result.SafeMessage = "Microsoft mail service is temporarily unavailable."
		}
	}
	if isRetryableValidationCategory(result.Category) && !result.Valid {
		if err := uc.validations.SaveMicrosoftProgress(ctx, job.ID, job.ResourceID, job.ClaimToken, result); err != nil {
			if errors.Is(err, ErrValidationResultStale) {
				return nil
			}
			if errors.Is(err, ErrValidationBindingRejected) {
				// The remote proof may have completed before a local uniqueness or
				// active-domain constraint changed. Make that a terminal binding
				// failure instead of leaving a pending resource with a failed job.
				result.Valid = false
				result.Category = "binding"
				result.SafeMessage = MicrosoftValidationBindingRejectedMessage
				result.RecoveredBinding = nil
				result.BindingObservation = nil
				applyErr := uc.validations.ApplyMicrosoftResult(
					ctx,
					job.ID,
					job.ResourceID,
					job.ClaimToken,
					result,
					validationSystemLog(job, false, result.Category, result.SafeMessage),
				)
				if errors.Is(applyErr, ErrValidationResultStale) || errors.Is(applyErr, ErrValidationBindingRejected) {
					return nil
				}
				return applyErr
			}
			return err
		}
		return uc.markValidationRetryableFailure(ctx, job.ID, job.ClaimToken, result.SafeMessage)
	}
	err = uc.validations.ApplyMicrosoftResult(ctx, job.ID, job.ResourceID, job.ClaimToken, result, validationSystemLog(job, result.Valid, result.Category, result.SafeMessage))
	if errors.Is(err, ErrValidationResultStale) || errors.Is(err, ErrValidationBindingRejected) {
		return nil
	}
	if err != nil || !result.Valid || uc.aliasTrigger == nil {
		return err
	}
	// Alias scheduling is its own durable concern. A transient failure here
	// must not turn an already-committed validation success back into a retry;
	// the daily alias scan remains the recovery path.
	if triggerErr := uc.aliasTrigger.EnsureForValidatedMicrosoftResource(ctx, job.ResourceID); triggerErr != nil {
		slog.Warn(
			"microsoft alias schedule trigger deferred after validation",
			"resource_id", job.ResourceID,
			"validation_id", job.ID,
			"request_id", job.RequestID,
		)
	}
	return nil
}

func (uc *ResourceValidationUseCase) processDomain(ctx context.Context, job *domain.ResourceValidation) error {
	dr, err := uc.resources.FindDomainByID(ctx, job.ResourceID)
	if err != nil {
		return err
	}
	if dr == nil || dr.Status == domain.DomainStatusDeleted {
		return uc.markValidationFailed(ctx, job.ID, job.ClaimToken, "Resource not found.")
	}
	if dr.Status == domain.DomainStatusDisabled {
		return uc.markValidationFailed(ctx, job.ID, job.ClaimToken, "Resource status does not allow validation.")
	}
	var result DomainValidationResult
	if uc.validator == nil {
		return uc.markValidationRetryableFailure(ctx, job.ID, job.ClaimToken, "Domain DNS service is temporarily unavailable.")
	}
	result, err = uc.validator.ValidateDomain(ctx, DomainValidationRequest{
		ResourceID: job.ResourceID,
		Domain:     dr.Domain,
		RequestID:  job.RequestID,
	})
	if err != nil {
		return uc.markValidationRetryableFailure(ctx, job.ID, job.ClaimToken, "Domain DNS service is temporarily unavailable.")
	}
	if result.SafeMessage == "" {
		if result.Valid {
			result.SafeMessage = "Domain DNS validation succeeded."
		} else {
			result.SafeMessage = "Domain DNS validation failed."
		}
	}
	if isRetryableValidationCategory(result.Category) && !result.Valid {
		return uc.markValidationRetryableFailure(ctx, job.ID, job.ClaimToken, result.SafeMessage)
	}
	return uc.validations.ApplyDomainResult(ctx, job.ID, job.ResourceID, job.ClaimToken, result, validationSystemLog(job, result.Valid, result.Category, result.SafeMessage))
}

func (uc *ResourceValidationUseCase) markValidationFailed(ctx context.Context, jobID uint, claimToken string, safeError string) error {
	if err := uc.validations.MarkFailed(ctx, jobID, claimToken, safeError); err != nil {
		return err
	}
	return nil
}

func (uc *ResourceValidationUseCase) markValidationRetryableFailure(ctx context.Context, jobID uint, claimToken string, safeError string) error {
	exhausted, err := uc.validations.MarkRetryableFailure(ctx, jobID, claimToken, safeError)
	if err != nil {
		return err
	}
	if exhausted {
		return nil
	}
	return ErrValidationTemporaryUnavailable
}

func isRetryableValidationCategory(category string) bool {
	switch strings.TrimSpace(category) {
	case "request", "auth_timeout", "code_timeout", "code_error":
		return true
	default:
		return false
	}
}

func validationSystemLog(job *domain.ResourceValidation, valid bool, category string, safeMessage string) *governancedomain.SystemLog {
	if valid {
		return nil
	}
	return &governancedomain.SystemLog{
		Level:     "warning",
		Module:    "core",
		EventType: "resource.validation_failed",
		RequestID: job.RequestID,
		BizType:   "resource",
		BizID:     fmt.Sprintf("%d", job.ResourceID),
		Message:   "Resource validation failed.",
		Detail:    safeValidationDetail(category, safeMessage),
	}
}

func safeValidationDetail(category, message string) string {
	category = strings.TrimSpace(category)
	message = strings.TrimSpace(message)
	switch {
	case category != "" && message != "":
		return category + ": " + message
	case message != "":
		return message
	case category != "":
		return category
	default:
		return "Resource validation failed."
	}
}

func validationView(job *domain.ResourceValidation) *ValidationResultView {
	if job == nil {
		return nil
	}
	return &ValidationResultView{
		ValidationID:       job.ID,
		ResourceID:         job.ResourceID,
		ResourceType:       string(job.ResourceType),
		Status:             string(job.Status),
		CredentialRevision: job.ExpectedCredentialRevision,
		Attempts:           job.Attempts,
		MaxAttempts:        job.MaxAttempts,
		LastSafeError:      job.LastSafeError,
		RequestID:          job.RequestID,
		StartedAt:          job.StartedAt,
		FinishedAt:         job.FinishedAt,
		CreatedAt:          job.CreatedAt,
		UpdatedAt:          job.UpdatedAt,
	}
}

func IsNonRetryableValidationError(err error) bool {
	return errors.Is(err, domain.ErrResourceNotFound) ||
		errors.Is(err, domain.ErrForbiddenResource) ||
		errors.Is(err, domain.ErrInvalidResourceType) ||
		errors.Is(err, domain.ErrInvalidResourceStatus)
}
