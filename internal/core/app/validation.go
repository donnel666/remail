package app

import (
	"context"
	"errors"
	"fmt"
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
	FindByID(ctx context.Context, id uint) (*domain.ResourceValidation, error)
	ClaimDispatchable(ctx context.Context, limit int, staleBefore time.Time) ([]domain.ResourceValidation, error)
	MarkRunning(ctx context.Context, id uint) (bool, error)
	MarkFailed(ctx context.Context, id uint, safeError string) error
	MarkRetryableFailure(ctx context.Context, id uint, safeError string) (bool, error)
	ApplyMicrosoftResult(ctx context.Context, jobID uint, resourceID uint, result MicrosoftValidationResult, systemLog *governancedomain.SystemLog) error
	ApplyDomainResult(ctx context.Context, jobID uint, resourceID uint, result DomainValidationResult, systemLog *governancedomain.SystemLog) error
	MarkDispatchFailed(ctx context.Context, id uint, safeError string) error
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
	Valid          bool
	ClientID       string
	RefreshToken   string
	RTExpireAt     *time.Time
	GraphAvailable bool
	Category       string
	SafeMessage    string
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
	JobID     uint   `json:"jobId"`
	RequestID string `json:"requestId"`
}

type DispatchResourceValidationJobsResult struct {
	Attempted int
	Queued    int
	Failed    int
}

// ResourceBatchValidationResult reports a bulk validation submission.
// Queued means accepted for validation; active jobs are reused instead of
// creating duplicate validation facts.
type ResourceBatchValidationResult struct {
	Requested int
	Queued    int
	Created   int
}

type ValidationResultView struct {
	ValidationID  uint      `json:"validationId"`
	ResourceID    uint      `json:"resourceId"`
	ResourceType  string    `json:"resourceType"`
	Status        string    `json:"status"`
	LastSafeError string    `json:"lastSafeError,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ResourceValidationUseCase struct {
	resources   EmailResourceRepository
	validations ResourceValidationRepository
	queue       ResourceValidationQueue
	validator   ResourceValidationPort
	now         func() time.Time
}

var ErrValidationTemporaryUnavailable = errors.New("resource validation temporary unavailable")

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

	if err := uc.queue.EnqueueResourceValidation(ctx, ResourceValidationTask{JobID: job.ID, RequestID: requestID}); err != nil {
		job.LastSafeError = "Resource validation queue is unavailable; dispatcher will retry."
		_ = uc.validations.MarkDispatchFailed(ctx, job.ID, job.LastSafeError)
	}

	return validationView(job), nil
}

func (uc *ResourceValidationUseCase) CreateBatch(ctx context.Context, selection ResourceBulkSelection, userID uint, requestID, path string) (*ResourceBatchValidationResult, error) {
	switch selection.Mode {
	case ResourceBulkSelectionIDs:
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
	return validationView(job), nil
}

func (uc *ResourceValidationUseCase) Process(ctx context.Context, task ResourceValidationTask) error {
	if task.JobID == 0 {
		return domain.ErrResourceNotFound
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

	claimed, err := uc.validations.MarkRunning(ctx, task.JobID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	switch job.ResourceType {
	case domain.ResourceTypeMicrosoft:
		return uc.processMicrosoft(ctx, job)
	case domain.ResourceTypeDomain:
		return uc.processDomain(ctx, job)
	default:
		return domain.ErrInvalidResourceType
	}
}

func (uc *ResourceValidationUseCase) DispatchPending(ctx context.Context, limit int) (*DispatchResourceValidationJobsResult, error) {
	if limit <= 0 {
		limit = 100
	}
	jobs, err := uc.validations.ClaimDispatchable(ctx, limit, uc.now().Add(-10*time.Minute))
	if err != nil {
		return nil, err
	}
	result := &DispatchResourceValidationJobsResult{Attempted: len(jobs)}
	for _, job := range jobs {
		err := uc.queue.EnqueueResourceValidation(ctx, ResourceValidationTask{JobID: job.ID, RequestID: job.RequestID})
		if err != nil {
			result.Failed++
			_ = uc.validations.MarkDispatchFailed(ctx, job.ID, "Resource validation queue is unavailable; dispatcher will retry.")
			continue
		}
		result.Queued++
	}
	return result, nil
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
		return uc.markValidationFailed(ctx, job.ID, "Resource not found.")
	}
	if ms.Status == domain.MicrosoftStatusDisabled {
		return uc.markValidationFailed(ctx, job.ID, "Resource status does not allow validation.")
	}
	var result MicrosoftValidationResult
	if uc.validator == nil {
		return uc.markValidationRetryableFailure(ctx, job.ID, "Microsoft mail service is temporarily unavailable.")
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
		return uc.markValidationRetryableFailure(ctx, job.ID, "Microsoft mail service is temporarily unavailable.")
	}
	if result.SafeMessage == "" {
		if result.Valid {
			result.SafeMessage = "Microsoft resource validation succeeded."
		} else {
			result.SafeMessage = "Microsoft mail service is temporarily unavailable."
		}
	}
	if isRetryableValidationCategory(result.Category) && !result.Valid {
		return uc.markValidationRetryableFailure(ctx, job.ID, result.SafeMessage)
	}
	return uc.validations.ApplyMicrosoftResult(ctx, job.ID, job.ResourceID, result, validationSystemLog(job, result.Valid, result.Category, result.SafeMessage))
}

func (uc *ResourceValidationUseCase) processDomain(ctx context.Context, job *domain.ResourceValidation) error {
	dr, err := uc.resources.FindDomainByID(ctx, job.ResourceID)
	if err != nil {
		return err
	}
	if dr == nil || dr.Status == domain.DomainStatusDeleted {
		return uc.markValidationFailed(ctx, job.ID, "Resource not found.")
	}
	if dr.Status == domain.DomainStatusDisabled {
		return uc.markValidationFailed(ctx, job.ID, "Resource status does not allow validation.")
	}
	var result DomainValidationResult
	if uc.validator == nil {
		return uc.markValidationRetryableFailure(ctx, job.ID, "Domain DNS service is temporarily unavailable.")
	}
	result, err = uc.validator.ValidateDomain(ctx, DomainValidationRequest{
		ResourceID: job.ResourceID,
		Domain:     dr.Domain,
		RequestID:  job.RequestID,
	})
	if err != nil {
		return uc.markValidationRetryableFailure(ctx, job.ID, "Domain DNS service is temporarily unavailable.")
	}
	if result.SafeMessage == "" {
		if result.Valid {
			result.SafeMessage = "Domain DNS validation succeeded."
		} else {
			result.SafeMessage = "Domain DNS validation failed."
		}
	}
	if isRetryableValidationCategory(result.Category) && !result.Valid {
		return uc.markValidationRetryableFailure(ctx, job.ID, result.SafeMessage)
	}
	return uc.validations.ApplyDomainResult(ctx, job.ID, job.ResourceID, result, validationSystemLog(job, result.Valid, result.Category, result.SafeMessage))
}

func (uc *ResourceValidationUseCase) markValidationFailed(ctx context.Context, jobID uint, safeError string) error {
	if err := uc.validations.MarkFailed(ctx, jobID, safeError); err != nil {
		return err
	}
	return nil
}

func (uc *ResourceValidationUseCase) markValidationRetryableFailure(ctx context.Context, jobID uint, safeError string) error {
	exhausted, err := uc.validations.MarkRetryableFailure(ctx, jobID, safeError)
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
	case "request", "auth_timeout":
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
		ValidationID:  job.ID,
		ResourceID:    job.ResourceID,
		ResourceType:  string(job.ResourceType),
		Status:        string(job.Status),
		LastSafeError: job.LastSafeError,
		CreatedAt:     job.CreatedAt,
		UpdatedAt:     job.UpdatedAt,
	}
}

func IsNonRetryableValidationError(err error) bool {
	return errors.Is(err, domain.ErrResourceNotFound) ||
		errors.Is(err, domain.ErrForbiddenResource) ||
		errors.Is(err, domain.ErrInvalidResourceType) ||
		errors.Is(err, domain.ErrInvalidResourceStatus)
}
