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
	"github.com/donnel666/remail/internal/platform"
)

// ResourceValidationRepository owns the resource pending/validating state and
// applies results from temporary Redis tasks.
type ResourceValidationRepository interface {
	MarkResourcePendingWithLog(ctx context.Context, resourceID uint, resourceType domain.ResourceType, ownerUserID uint, log *governancedomain.OperationLog) error
	MarkValidationBatchPending(ctx context.Context, task ResourceValidationBatchTask, limit int) (*ResourceValidationBatchPageResult, error)
	ClaimPendingValidations(ctx context.Context, limit int) ([]ResourceValidationTask, error)
	ReleaseValidation(ctx context.Context, task ResourceValidationTask) error
	ResetValidationAssignments(ctx context.Context) error
	SaveMicrosoftProgress(ctx context.Context, task ResourceValidationTask, result MicrosoftValidationResult) error
	ApplyMicrosoftResult(ctx context.Context, task ResourceValidationTask, result MicrosoftValidationResult, systemLog *governancedomain.SystemLog) error
	ApplyDomainResult(ctx context.Context, task ResourceValidationTask, result DomainValidationResult, systemLog *governancedomain.SystemLog) error
}

// ResourceValidationQueue enqueues asynchronous resource validation work.
type ResourceValidationQueue interface {
	EnqueueResourceValidation(ctx context.Context, task ResourceValidationTask) error
	EnqueueResourceValidationBatch(ctx context.Context, task ResourceValidationBatchTask) error
	EnqueueResourceValidationDispatcher(ctx context.Context, delay time.Duration) error
}

// resourceValidationBatchLeaseQueue is implemented by the Redis queue. The
// lease exists only while one cursor chain is active and fences an expired old
// cursor from deleting or extending a later submission with the same BatchID.
type resourceValidationBatchLeaseQueue interface {
	RefreshResourceValidationBatch(ctx context.Context, task ResourceValidationBatchTask) (bool, error)
	ReleaseResourceValidationBatch(ctx context.Context, task ResourceValidationBatchTask) error
}

// ResourceValidationPort is implemented by BC-MAILTRANSPORT ACL adapters.
type ResourceValidationPort interface {
	ValidateMicrosoft(ctx context.Context, req MicrosoftValidationRequest) (MicrosoftValidationResult, error)
	ValidateDomain(ctx context.Context, req DomainValidationRequest) (DomainValidationResult, error)
}

// MicrosoftValidationBindingCommitPort is implemented by MailTransport. Core
// invokes it only from fenced validation progress/result transactions, after
// the resource root and Microsoft subtype have been locked and the expected
// credential revision has been checked.
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

// MicrosoftHistoryScanTriggerPort is implemented by MailMatch. It is called
// after Core commits a successful Microsoft validation so mailbox history can
// be scanned independently from the validation worker.
type MicrosoftHistoryScanTriggerPort interface {
	ScheduleValidatedMicrosoftHistory(ctx context.Context, resourceID uint, requestID string) error
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
	RecoveredBinding     *MicrosoftRecoveredBinding
	BindingObservation   *MicrosoftBindingObservation
	ReleaseRecoveryLease func(context.Context) error
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
// cannot replace a concurrently changed binding outside Core's result fence.
type MicrosoftBindingObservation struct {
	Address     string
	Status      string
	SafeMessage string
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
	ResourceID                 uint                `json:"resourceId"`
	ResourceType               domain.ResourceType `json:"resourceType"`
	OwnerUserID                uint                `json:"ownerUserId"`
	ExpectedCredentialRevision uint64              `json:"expectedCredentialRevision,omitempty"`
	RequestID                  string              `json:"requestId,omitempty"`
}

// ResourceValidationBatchTask is deliberately Redis-only coordination. A lost
// batch can be submitted again.
type ResourceValidationBatchTask struct {
	BatchID     string                `json:"batchId"`
	ClaimToken  string                `json:"claimToken,omitempty"`
	OwnerUserID uint                  `json:"ownerUserId"`
	Selection   ResourceBulkSelection `json:"selection"`
	AfterID     uint                  `json:"afterId"`
	ThroughID   uint                  `json:"throughId"`
	RequestID   string                `json:"requestId"`
	Path        string                `json:"path"`
}

type ResourceValidationBatchPageResult struct {
	Processed int
	AfterID   uint
	ThroughID uint
	Done      bool
}

type DispatchResourceValidationsResult struct {
	Attempted int
	Queued    int
	Failed    int
}

// ResourceBatchValidationResult reports acceptance into Redis. Explicit IDs
// have an exact bounded count; filter matching is intentionally deferred.
type ResourceBatchValidationResult struct {
	Requested int
	Queued    int
}

type ResourceValidationUseCase struct {
	resources      EmailResourceRepository
	validations    ResourceValidationRepository
	queue          ResourceValidationQueue
	validator      ResourceValidationPort
	aliasTrigger   MicrosoftAliasScheduleTriggerPort
	historyTrigger MicrosoftHistoryScanTriggerPort
}

func (uc *ResourceValidationUseCase) SetMicrosoftAliasScheduleTrigger(trigger MicrosoftAliasScheduleTriggerPort) {
	if uc != nil {
		uc.aliasTrigger = trigger
	}
}

func (uc *ResourceValidationUseCase) SetMicrosoftHistoryScanTrigger(trigger MicrosoftHistoryScanTriggerPort) {
	if uc != nil {
		uc.historyTrigger = trigger
	}
}

const ResourceValidationMaxExplicitIDs = 10_000

const (
	resourceValidationBatchPageSize = 1000
	resourceValidationDispatchDelay = time.Second
)

var ErrValidationTemporaryUnavailable = errors.New("resource validation temporary unavailable")

// ErrValidationResultStale means a Redis task was fenced off because the
// resource credentials or command state changed while the external call was
// in flight. The worker must not retry the stale payload or overwrite newer
// resource state.
var ErrValidationResultStale = errors.New("resource validation result is stale")

// ErrValidationBindingRejected classifies a supplementary Binding fact that
// cannot be committed. Repositories must roll that Binding attempt back to a
// savepoint; it is never a resource-health result.
var ErrValidationBindingRejected = errors.New("resource validation binding was rejected")

func NewResourceValidationUseCase(resources EmailResourceRepository, validations ResourceValidationRepository, queue ResourceValidationQueue, validator ResourceValidationPort) *ResourceValidationUseCase {
	return &ResourceValidationUseCase{
		resources:   resources,
		validations: validations,
		queue:       queue,
		validator:   validator,
	}
}

func (uc *ResourceValidationUseCase) Create(ctx context.Context, resourceID uint, userID uint, isAdmin bool, requestID, path string) (*ResourceBatchValidationResult, error) {
	if uc == nil || uc.resources == nil || uc.validations == nil || uc.queue == nil || resourceID == 0 || userID == 0 {
		return nil, domain.ErrInvalidResourceCommand
	}
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
	log := &governancedomain.OperationLog{
		OperatorUserID: userID,
		OperationType:  "core.resource.validate",
		ResourceType:   "resource",
		ResourceID:     fmt.Sprintf("%d", resource.ID),
		Path:           path,
		Result:         "success",
		SafeSummary:    "Resource validation marked pending for asynchronous execution.",
		RequestID:      requestID,
	}
	if err := uc.validations.MarkResourcePendingWithLog(ctx, resource.ID, resource.Type, resource.OwnerUserID, log); err != nil {
		return nil, err
	}
	uc.ScheduleDispatcher(ctx, 0)
	return &ResourceBatchValidationResult{Requested: 1, Queued: 1}, nil
}

func (uc *ResourceValidationUseCase) CreateBatch(ctx context.Context, selection ResourceBulkSelection, userID uint, isAdmin bool, requestID, path string) (*ResourceBatchValidationResult, error) {
	if uc == nil || uc.queue == nil || userID == 0 || (selection.AdminScope && !isAdmin) {
		return nil, domain.ErrInvalidResourceCommand
	}
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

	batchID := strings.TrimSpace(selection.BatchKey)
	selection.BatchKey = ""
	if batchID == "" {
		batchID = platform.NewUUIDV7String()
	}
	if err := uc.queue.EnqueueResourceValidationBatch(ctx, ResourceValidationBatchTask{
		BatchID: batchID, OwnerUserID: userID, Selection: selection,
		RequestID: strings.TrimSpace(requestID), Path: strings.TrimSpace(path),
	}); err != nil {
		return nil, err
	}
	result := &ResourceBatchValidationResult{}
	if selection.Mode == ResourceBulkSelectionIDs {
		result.Requested = len(selection.ResourceIDs)
		result.Queued = len(selection.ResourceIDs)
	}
	return result, nil
}

func (uc *ResourceValidationUseCase) ProcessBatch(ctx context.Context, task ResourceValidationBatchTask) error {
	if uc == nil || uc.validations == nil || uc.queue == nil || strings.TrimSpace(task.BatchID) == "" || task.OwnerUserID == 0 {
		return domain.ErrInvalidResourceCommand
	}
	if leases, ok := uc.queue.(resourceValidationBatchLeaseQueue); ok {
		owned, err := leases.RefreshResourceValidationBatch(ctx, task)
		if err != nil {
			return err
		}
		if !owned {
			return nil
		}
	}
	page, err := uc.validations.MarkValidationBatchPending(ctx, task, resourceValidationBatchPageSize)
	if err != nil {
		return err
	}
	if page.Processed > 0 {
		uc.ScheduleDispatcher(ctx, 0)
	}
	if page.Done {
		return uc.ReleaseBatch(ctx, task)
	}
	task.AfterID = page.AfterID
	task.ThroughID = page.ThroughID
	return uc.queue.EnqueueResourceValidationBatch(ctx, task)
}

// ReleaseBatch deletes only the live Redis lease owned by this cursor token.
// Completed Asynq tasks already use Retention(0), so no per-resource history is
// retained after execution.
func (uc *ResourceValidationUseCase) ReleaseBatch(ctx context.Context, task ResourceValidationBatchTask) error {
	if uc == nil || uc.queue == nil {
		return nil
	}
	leases, ok := uc.queue.(resourceValidationBatchLeaseQueue)
	if !ok {
		return nil
	}
	return leases.ReleaseResourceValidationBatch(ctx, task)
}

func (uc *ResourceValidationUseCase) Process(ctx context.Context, task ResourceValidationTask, finalAttempt bool) error {
	if uc == nil || uc.resources == nil || uc.validations == nil || task.ResourceID == 0 || task.OwnerUserID == 0 || !domain.IsValidResourceType(task.ResourceType) {
		return domain.ErrInvalidResourceCommand
	}
	var processErr error
	switch task.ResourceType {
	case domain.ResourceTypeMicrosoft:
		processErr = uc.processMicrosoft(ctx, task, finalAttempt)
	case domain.ResourceTypeDomain:
		processErr = uc.processDomain(ctx, task, finalAttempt)
	default:
		processErr = domain.ErrInvalidResourceType
	}
	if processErr == nil || errors.Is(processErr, ErrValidationResultStale) || errors.Is(processErr, domain.ErrInvalidResourceStatus) || errors.Is(processErr, domain.ErrResourceNotFound) {
		return nil
	}
	if !finalAttempt {
		return processErr
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := uc.validations.ReleaseValidation(recoveryCtx, task); err != nil && !errors.Is(err, domain.ErrInvalidResourceStatus) {
		slog.Warn(
			"release exhausted Redis validation assignment failed",
			"resource_id", task.ResourceID,
			"request_id", task.RequestID,
			"error", err,
		)
	}
	return nil
}

func (uc *ResourceValidationUseCase) ResetAssignments(ctx context.Context) error {
	if uc == nil || uc.validations == nil {
		return nil
	}
	if err := uc.validations.ResetValidationAssignments(ctx); err != nil {
		return err
	}
	uc.ScheduleDispatcher(ctx, 0)
	return nil
}

func (uc *ResourceValidationUseCase) DispatchPending(ctx context.Context, limit int) (*DispatchResourceValidationsResult, error) {
	if uc == nil || uc.validations == nil || uc.queue == nil {
		return nil, ErrValidationTemporaryUnavailable
	}
	if limit <= 0 {
		limit = 100
	}
	tasks, err := uc.validations.ClaimPendingValidations(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := &DispatchResourceValidationsResult{Attempted: len(tasks)}
	var dispatchErrors []error
	for _, task := range tasks {
		if err := uc.queue.EnqueueResourceValidation(ctx, task); err != nil {
			result.Failed++
			dispatchErrors = append(dispatchErrors, fmt.Errorf("enqueue validation for resource %d: %w", task.ResourceID, err))
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			releaseErr := uc.validations.ReleaseValidation(recoveryCtx, task)
			cancel()
			if releaseErr != nil {
				dispatchErrors = append(dispatchErrors, fmt.Errorf("release resource %d after enqueue failure: %w", task.ResourceID, releaseErr))
			}
			continue
		}
		result.Queued++
	}
	return result, errors.Join(dispatchErrors...)
}

// ReleaseDispatch returns a Redis-assigned resource to pending when the task
// cannot be admitted before its final Asynq attempt.
func (uc *ResourceValidationUseCase) ReleaseDispatch(ctx context.Context, task ResourceValidationTask) error {
	if uc == nil || uc.validations == nil || task.ResourceID == 0 {
		return nil
	}
	return uc.validations.ReleaseValidation(ctx, task)
}

func (uc *ResourceValidationUseCase) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if uc == nil || uc.queue == nil {
		return
	}
	_ = uc.queue.EnqueueResourceValidationDispatcher(ctx, max(delay, resourceValidationDispatchDelay))
}

func (uc *ResourceValidationUseCase) processMicrosoft(ctx context.Context, task ResourceValidationTask, finalAttempt bool) error {
	ms, err := uc.resources.FindMicrosoftByID(ctx, task.ResourceID)
	if err != nil {
		return err
	}
	if ms == nil || ms.Status == domain.MicrosoftStatusDeleted {
		return domain.ErrResourceNotFound
	}
	if ms.Status != domain.MicrosoftStatusValidating || ms.CredentialRevision != task.ExpectedCredentialRevision {
		return ErrValidationResultStale
	}
	var result MicrosoftValidationResult
	if uc.validator == nil {
		return ErrValidationTemporaryUnavailable
	}
	result, err = uc.validator.ValidateMicrosoft(ctx, MicrosoftValidationRequest{
		ResourceID:   task.ResourceID,
		OwnerUserID:  task.OwnerUserID,
		EmailAddress: ms.EmailAddress,
		Password:     ms.Password,
		ClientID:     ms.ClientID,
		RefreshToken: ms.RefreshToken,
		RequestID:    task.RequestID,
	})
	defer releaseMicrosoftValidationRecoveryLease(ctx, task, result.ReleaseRecoveryLease)
	if err != nil {
		return ErrValidationTemporaryUnavailable
	}
	if result.SafeMessage == "" {
		if result.Valid {
			result.SafeMessage = "Microsoft resource validation succeeded."
		} else {
			result.SafeMessage = "Microsoft mail service is temporarily unavailable."
		}
	}
	if isRetryableValidationCategory(result.Category) && !result.Valid {
		if !finalAttempt {
			if err := uc.validations.SaveMicrosoftProgress(ctx, task, result); err != nil {
				if errors.Is(err, ErrValidationResultStale) {
					return nil
				}
				return err
			}
			return ErrValidationTemporaryUnavailable
		}
		// Retry budget exhausted. Do not defer again: fall through to
		// ApplyMicrosoftResult below so the resource commits to abnormal with a
		// last_safe_error instead of looping pending→validating forever. The
		// admin re-validates to move it back to pending.
	}
	if result.Valid && uc.historyTrigger != nil {
		// The task carries only resource identity, so enqueue it before committing
		// the healthy resource state. Its worker rechecks status and credentials;
		// this closes the otherwise permanent "normal but no history task" gap.
		triggerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		triggerErr := uc.historyTrigger.ScheduleValidatedMicrosoftHistory(triggerCtx, task.ResourceID, task.RequestID)
		cancel()
		if triggerErr != nil {
			slog.Warn(
				"microsoft history scan task could not be created before validation commit",
				"resource_id", task.ResourceID,
				"request_id", task.RequestID,
			)
			return ErrValidationTemporaryUnavailable
		}
	}
	err = uc.validations.ApplyMicrosoftResult(ctx, task, result, validationSystemLog(task, result.Valid, result.Category, result.SafeMessage))
	if errors.Is(err, ErrValidationResultStale) {
		return nil
	}
	if err != nil {
		return err
	}
	if !result.Valid {
		return nil
	}
	// Alias scheduling is its own durable concern. A transient failure here
	// must not turn an already-committed validation success back into a retry;
	// the daily alias scan remains the recovery path.
	if uc.aliasTrigger != nil {
		if triggerErr := uc.aliasTrigger.EnsureForValidatedMicrosoftResource(ctx, task.ResourceID); triggerErr != nil {
			slog.Warn(
				"microsoft alias schedule trigger deferred after validation",
				"resource_id", task.ResourceID,
				"request_id", task.RequestID,
			)
		}
	}
	return nil
}

func releaseMicrosoftValidationRecoveryLease(ctx context.Context, task ResourceValidationTask, release func(context.Context) error) {
	if release == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := release(releaseCtx); err != nil {
		slog.Warn(
			"microsoft binding recovery lease release deferred after validation",
			"resource_id", task.ResourceID,
			"request_id", task.RequestID,
			"error", err,
		)
	}
}

func (uc *ResourceValidationUseCase) processDomain(ctx context.Context, task ResourceValidationTask, finalAttempt bool) error {
	dr, err := uc.resources.FindDomainByID(ctx, task.ResourceID)
	if err != nil {
		return err
	}
	if dr == nil || dr.Status == domain.DomainStatusDeleted {
		return domain.ErrResourceNotFound
	}
	if dr.Status != domain.DomainStatusValidating {
		return ErrValidationResultStale
	}
	var result DomainValidationResult
	if uc.validator == nil {
		return ErrValidationTemporaryUnavailable
	}
	result, err = uc.validator.ValidateDomain(ctx, DomainValidationRequest{
		ResourceID: task.ResourceID,
		Domain:     dr.Domain,
		RequestID:  task.RequestID,
	})
	if err != nil {
		return ErrValidationTemporaryUnavailable
	}
	if result.SafeMessage == "" {
		if result.Valid {
			result.SafeMessage = "Domain DNS validation succeeded."
		} else {
			result.SafeMessage = "Domain DNS validation failed."
		}
	}
	if isRetryableValidationCategory(result.Category) && !result.Valid && !finalAttempt {
		return ErrValidationTemporaryUnavailable
	}
	return uc.validations.ApplyDomainResult(ctx, task, result, validationSystemLog(task, result.Valid, result.Category, result.SafeMessage))
}

func isRetryableValidationCategory(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "oauth_invalid_grant", "refresh_token_expired", "oauth_refresh_token_expired",
		"oauth_client", "oauth_permission", "mfa", "passkey", "phone", "password",
		"unknown_mailbox", "locked", "account_abnormal", "dns":
		return false
	default:
		return true
	}
}

func validationSystemLog(task ResourceValidationTask, valid bool, category string, safeMessage string) *governancedomain.SystemLog {
	if valid {
		return nil
	}
	return &governancedomain.SystemLog{
		Level:     "warning",
		Module:    "core",
		EventType: "resource.validation_failed",
		RequestID: task.RequestID,
		BizType:   "resource",
		BizID:     fmt.Sprintf("%d", task.ResourceID),
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
