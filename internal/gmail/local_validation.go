package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	typeGmailValidateLocal             = "gmail:validate_local"
	typeGmailValidationDispatcher      = "gmail:resource_validation_dispatcher"
	localGmailValidationTimeout        = 8 * time.Minute
	localGmailValidationTaskTTL        = 10 * time.Minute
	localGmailValidationDispatchUnique = 30 * time.Second
	localGmailValidationSettleAfter    = time.Second
	localGmailValidationBatchMax       = 200
	localGmailValidationMaxFailures    = 3
)

const localGmailValidationIdempotencyTTL = 24 * time.Hour

type localResourceValidationTask struct {
	ResourceID                 uint   `json:"resourceId"`
	OwnerUserID                uint   `json:"ownerUserId"`
	ValidationGeneration       uint64 `json:"validationGeneration"`
	ExpectedCredentialRevision uint64 `json:"expectedCredentialRevision"`
	RequestID                  string `json:"requestId,omitempty"`
}

type localGmailValidationInput struct {
	ResourceID           uint
	ValidationGeneration uint64
	Email                string
	Password             string
	BindingEmail         string
	TwoFactorSecret      string
	AppPassword          string
	RequestID            string
}

type localGmailValidationResult struct {
	TwoFactorSecret          string
	AppPassword              string
	TwoFactorAuthoritative   bool
	AppPasswordAuthoritative bool
	SafeError                string
	Temporary                bool
	ProxyFailure             bool
	Err                      error
}

type localGmailValidationFunc func(context.Context, localGmailValidationInput) localGmailValidationResult

func (s *Service) RequestAdminLocalResourceValidation(
	ctx context.Context,
	resourceID, operatorUserID uint,
	idempotencyKey, requestID, path string,
) (bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	requestID = strings.TrimSpace(requestID)
	if s == nil || s.db == nil || s.redis == nil || resourceID == 0 || operatorUserID == 0 ||
		idempotencyKey == "" || len(idempotencyKey) > 128 {
		return false, ErrInvalidLocalResource
	}
	fingerprint := stableDigest(fmt.Sprintf("validate|%d|%d", operatorUserID, resourceID))
	reused, err := s.claimLocalGmailCommandIdempotency(ctx, operatorUserID, idempotencyKey, fingerprint)
	if err != nil {
		return false, err
	}
	commandHash := stableDigest(fingerprint + "|" + idempotencyKey)
	changed := false
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resourceID, "gmail").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return fmt.Errorf("lock Gmail validation command root: %w", err)
		}
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", resourceID).Take(&resource).Error; err != nil {
			return fmt.Errorf("lock Gmail validation command resource: %w", err)
		}
		if resource.Status == LocalResourceDeleted {
			return ErrLocalResourceMissing
		}
		if resource.ValidationCommandHash == commandHash {
			return nil
		}
		if resource.Status == LocalResourceDisabled {
			return ErrInvalidLocalResource
		}
		now := s.now().UTC()
		result := tx.Model(&localResourceModel{}).Where("id = ?", resourceID).Updates(map[string]any{
			"status": LocalResourcePending, "validation_generation": gorm.Expr("validation_generation + 1"),
			"validation_failures": 0, "validation_request_id": requestID,
			"validation_command_hash": commandHash, "last_safe_error": "", "last_checked_at": nil,
			"updated_at": now,
		})
		if result.Error != nil {
			return fmt.Errorf("mark Gmail validation command pending: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrLocalValidationConflict
		}
		if err := tx.Model(&resourceRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return fmt.Errorf("bump Gmail validation command version: %w", err)
		}
		if s.logs != nil {
			if err := s.logs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
				OperatorUserID: operatorUserID, OperationType: "gmail.resource.validate",
				ResourceType: "gmail_resource", ResourceID: strconv.FormatUint(uint64(resourceID), 10),
				Path: path, Result: "success",
				SafeSummary: "Gmail resource validation marked pending for asynchronous execution.",
				RequestID:   requestID,
			}); err != nil {
				return err
			}
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if changed {
		_ = s.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return reused || !changed, nil
}

func (s *Service) claimLocalGmailCommandIdempotency(
	ctx context.Context,
	operatorUserID uint,
	idempotencyKey, fingerprint string,
) (bool, error) {
	key := fmt.Sprintf("gmail:resource_validation:idempotency:%d:%s", operatorUserID, stableDigest(idempotencyKey))
	const script = `
local current = redis.call('GET', KEYS[1])
if not current then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
if current == ARGV[1] then return 0 end
return -1`
	result, err := s.redis.Eval(ctx, script, []string{key}, fingerprint, localGmailValidationIdempotencyTTL.Milliseconds()).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return false, fmt.Errorf("claim Gmail validation idempotency: %w", ErrLocalValidationDependency)
	}
	if result < 0 {
		return false, ErrLocalValidationConflict
	}
	return result == 0, nil
}

func (s *Service) scheduleLocalResourceValidation(ctx context.Context, resourceID uint) error {
	if resourceID == 0 {
		return nil
	}
	return s.scheduleLocalResourceValidationDispatcher(ctx, 0)
}

func (s *Service) scheduleLocalResourceValidationDispatcher(ctx context.Context, delay time.Duration) error {
	if s == nil || s.queue == nil {
		return ErrLocalValidationDependency
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueDefault),
		asynq.Unique(localGmailValidationDispatchUnique),
		asynq.MaxRetry(0),
		asynq.Timeout(30 * time.Second),
		asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailValidationDispatcher, nil), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) DispatchLocalResourceValidations(ctx context.Context, limit int) error {
	if s == nil || s.queue == nil {
		return ErrLocalValidationDependency
	}
	if limit <= 0 || limit > localGmailValidationBatchMax {
		limit = localGmailValidationBatchMax
	}
	historyErr := s.dispatchIdentifyingLocalGmailHistory(ctx, limit)
	window := min(limit, runtimeconfig.Int("validation_dispatch_maximum", 128, 1))
	window = min(window, runtimeconfig.Int("gmail_validation_concurrency", gmailValidationConcurrency, 1))
	if s.backgroundExecution != nil {
		window = min(window, s.backgroundExecution.Snapshot().Limit)
	}
	var validating int64
	if err := s.dbFor(ctx).Model(&localResourceModel{}).Where("status = ?", LocalResourceValidating).Count(&validating).Error; err != nil {
		return fmt.Errorf("count active local Gmail validations: %w", err)
	}
	capacity := min(limit, max(0, window-int(validating)))
	if capacity <= 0 {
		return historyErr
	}
	var tasks []localResourceValidationTask
	if err := s.dbFor(ctx).Table("gmail_resources AS gr").
		Select(`gr.id AS resource_id, er.owner_user_id,
gr.validation_generation, gr.credential_revision AS expected_credential_revision,
gr.validation_request_id AS request_id`).
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = ?", "gmail").
		Where("gr.status = ? AND gr.validation_generation > 0 AND gr.credential_revision > 0", LocalResourcePending).
		Order("gr.updated_at ASC, gr.id ASC").Limit(capacity).Scan(&tasks).Error; err != nil {
		return fmt.Errorf("list pending local Gmail validations: %w", err)
	}
	var dispatchErr error
	for _, task := range tasks {
		task.RequestID = strings.TrimSpace(task.RequestID)
		if task.RequestID == "" {
			task.RequestID = fmt.Sprintf("gmail-validation-%d-%d", task.ResourceID, task.ValidationGeneration)
		}
		if err := s.enqueueLocalResourceValidation(ctx, task); err != nil {
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf("schedule local Gmail validation %d: %w", task.ResourceID, err))
			continue
		}
		result := s.dbFor(ctx).Model(&localResourceModel{}).
			Where(`id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?
AND EXISTS (SELECT 1 FROM email_resources AS er WHERE er.id = gmail_resources.id AND er.type = 'gmail' AND er.owner_user_id = ?)`,
				task.ResourceID, LocalResourcePending, task.ValidationGeneration,
				task.ExpectedCredentialRevision, task.OwnerUserID).
			Updates(map[string]any{
				"status": LocalResourceValidating, "validation_request_id": task.RequestID, "updated_at": s.now().UTC(),
			})
		if result.Error != nil {
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf("activate local Gmail validation %d: %w", task.ResourceID, result.Error))
		}
	}
	return errors.Join(historyErr, dispatchErr)
}

func (s *Service) dispatchIdentifyingLocalGmailHistory(ctx context.Context, limit int) error {
	var tasks []localGmailHistoryTask
	if err := s.dbFor(ctx).Table("gmail_resources AS gr").
		Select(`gr.id AS resource_id, er.owner_user_id, gr.validation_generation,
gr.credential_revision AS expected_credential_revision, gr.validation_request_id AS request_id`).
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = ?", "gmail").
		Where("gr.status = ? AND gr.validation_generation > 0 AND gr.credential_revision > 0", LocalResourceIdentifying).
		Order("gr.updated_at ASC, gr.id ASC").Limit(limit).Scan(&tasks).Error; err != nil {
		return fmt.Errorf("list identifying local Gmail resources: %w", err)
	}
	var result error
	for _, task := range tasks {
		if err := s.enqueueValidatedLocalGmailHistory(ctx, task); err != nil {
			result = errors.Join(result, fmt.Errorf("schedule local Gmail history %d: %w", task.ResourceID, err))
		}
	}
	return result
}

func (s *Service) enqueueLocalResourceValidation(ctx context.Context, task localResourceValidationTask) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("encode local Gmail validation task: %w", err)
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailValidateLocal, payload),
		asynq.Queue(platform.QueueBackgroundValidation),
		asynq.ProcessIn(localGmailValidationSettleAfter),
		asynq.Unique(localGmailValidationTaskTTL),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(localGmailValidationTimeout+30*time.Second),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func decodeLocalResourceValidationTask(task *asynq.Task) (localResourceValidationTask, error) {
	var payload localResourceValidationTask
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil ||
		payload.ResourceID == 0 || payload.OwnerUserID == 0 || payload.ValidationGeneration == 0 || payload.ExpectedCredentialRevision == 0 {
		return localResourceValidationTask{}, fmt.Errorf("decode local Gmail validation task: %w", asynq.SkipRetry)
	}
	payload.RequestID = strings.TrimSpace(payload.RequestID)
	return payload, nil
}

func (s *Service) ValidateLocalResource(ctx context.Context, resourceID uint) error {
	return s.validateLocalResourceWith(ctx, resourceID, s.validateLocalGmailAccount)
}

func (s *Service) validateLocalResourceWith(
	ctx context.Context,
	resourceID uint,
	validate localGmailValidationFunc,
) error {
	if s == nil || s.db == nil || resourceID == 0 || validate == nil {
		return ErrLocalResourceMissing
	}
	var task localResourceValidationTask
	if err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		resource, err := lockLocalResource(tx, resourceID)
		if err != nil {
			return err
		}
		if resource.Status != LocalResourcePending {
			return nil
		}
		if resource.ValidationGeneration == 0 || resource.CredentialRevision == 0 {
			return ErrLocalValidationConflict
		}
		result := tx.Model(&localResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				resource.ID, LocalResourcePending, resource.ValidationGeneration, resource.CredentialRevision).
			Update("status", LocalResourceValidating)
		if result.Error != nil {
			return fmt.Errorf("claim local Gmail validation: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil
		}
		task = localResourceValidationTask{
			ResourceID: resource.ID, OwnerUserID: resource.OwnerUserID,
			ValidationGeneration:       resource.ValidationGeneration,
			ExpectedCredentialRevision: resource.CredentialRevision,
			RequestID:                  resource.ValidationRequestID,
		}
		return nil
	}); err != nil {
		return err
	}
	if task.ResourceID == 0 {
		return nil
	}
	return s.processLocalResourceValidationWith(ctx, task, validate)
}

func (s *Service) ProcessLocalResourceValidation(ctx context.Context, task localResourceValidationTask) error {
	return s.processLocalResourceValidationWith(ctx, task, s.validateLocalGmailAccount)
}

func (s *Service) processLocalResourceValidationWith(
	ctx context.Context,
	task localResourceValidationTask,
	validate localGmailValidationFunc,
) error {
	if s == nil || s.db == nil || validate == nil || task.ResourceID == 0 || task.OwnerUserID == 0 ||
		task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return ErrLocalValidationConflict
	}
	var resource localResourceModel
	result := s.dbFor(ctx).Table("gmail_resources AS gr").Select("gr.*").
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = ? AND er.owner_user_id = ?", "gmail", task.OwnerUserID).
		Where("gr.id = ?", task.ResourceID).Limit(1).Scan(&resource)
	if result.Error != nil {
		return fmt.Errorf("load local Gmail validation state: %w", result.Error)
	}
	if resource.ID == 0 || resource.OwnerUserID != task.OwnerUserID {
		return nil
	}
	if resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.ExpectedCredentialRevision {
		return nil
	}
	if resource.Status == LocalResourcePending {
		return ErrLocalValidationDependency
	}
	if resource.Status != LocalResourceValidating {
		return nil
	}

	validation := validate(ctx, localGmailValidationInput{
		ResourceID: task.ResourceID, ValidationGeneration: task.ValidationGeneration,
		Email: resource.Email, Password: resource.Password, BindingEmail: resource.BindingEmail, TwoFactorSecret: resource.TwoFactorSecret,
		AppPassword: resource.AppPassword, RequestID: task.RequestID,
	})
	if validation.TwoFactorAuthoritative && !validLocalGmailTOTPSecret(validation.TwoFactorSecret) {
		validation.TwoFactorAuthoritative = false
	}
	if validation.AppPasswordAuthoritative && !validLocalGmailAppPassword(validation.AppPassword) {
		validation.AppPasswordAuthoritative = false
	}
	if validation.Err == nil && !validLocalGmailRotatedCredentials(validation.TwoFactorSecret, validation.AppPassword) {
		validation.SafeError = "Gmail returned incomplete replacement credentials."
		validation.Temporary = true
		validation.Err = ErrLocalValidationDependency
	}
	applyCtx, cancelApply := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	historyTask, retryPending, err := s.applyLocalResourceValidationResult(applyCtx, task, validation)
	cancelApply()
	if err != nil {
		return err
	}
	if historyTask.ResourceID != 0 {
		enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		enqueueErr := s.enqueueValidatedLocalGmailHistory(enqueueCtx, historyTask)
		cancel()
		if enqueueErr != nil {
			_ = s.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), time.Second)
			return fmt.Errorf("create validated Gmail history task: %w", ErrLocalValidationDependency)
		}
	}
	if retryPending {
		_ = s.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func (s *Service) applyLocalResourceValidationResult(
	ctx context.Context,
	task localResourceValidationTask,
	validation localGmailValidationResult,
) (localGmailHistoryTask, bool, error) {
	var historyTask localGmailHistoryTask
	retryPending := false
	err := withLocalGmailValidationTransaction(ctx, s.dbFor(ctx), func(tx *gorm.DB) error {
		historyTask = localGmailHistoryTask{}
		retryPending = false
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "gmail", task.OwnerUserID).
			Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("lock local Gmail validation root: %w", err)
		}
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", task.ResourceID).Take(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("lock local Gmail validation resource: %w", err)
		}
		if resource.OwnerUserID != task.OwnerUserID || resource.Status != LocalResourceValidating ||
			resource.ValidationGeneration != task.ValidationGeneration ||
			resource.CredentialRevision != task.ExpectedCredentialRevision {
			return nil
		}

		checkedAt := s.now().UTC()
		nextStatus := LocalResourceIdentifying
		nextFailures := 0
		safeError := ""
		updates := map[string]any{
			"status": nextStatus, "validation_failures": nextFailures,
			"last_safe_error": safeError, "last_checked_at": checkedAt, "updated_at": checkedAt,
		}
		credentialChanged := false
		if validation.Err == nil || validation.TwoFactorAuthoritative {
			updates["two_factor_secret"] = strings.ToUpper(removeWhitespace(validation.TwoFactorSecret))
			credentialChanged = true
		}
		if validation.Err == nil || validation.AppPasswordAuthoritative {
			updates["app_password"] = removeWhitespace(validation.AppPassword)
			credentialChanged = true
		}
		if credentialChanged {
			updates["credential_revision"] = resource.CredentialRevision + 1
			updates["credential_updated_at"] = checkedAt
		}
		if validation.Err != nil {
			safeError = strings.TrimSpace(validation.SafeError)
			if safeError == "" {
				safeError = "Gmail account validation failed."
			}
			nextFailures = min(resource.ValidationFailures+1, localGmailValidationMaxFailures)
			nextStatus = LocalResourceAbnormal
			if validation.Temporary && nextFailures < localGmailValidationMaxFailures {
				nextStatus = LocalResourcePending
				retryPending = true
				updates["validation_generation"] = resource.ValidationGeneration + 1
			}
			updates["status"] = nextStatus
			updates["validation_failures"] = nextFailures
			updates["last_safe_error"] = safeError
		}
		result := tx.Model(&localResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				task.ResourceID, LocalResourceValidating, task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("apply local Gmail validation result: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			retryPending = false
			return nil
		}
		if validation.Err == nil {
			historyTask = localGmailHistoryTask{
				ResourceID: task.ResourceID, OwnerUserID: task.OwnerUserID,
				ValidationGeneration:       task.ValidationGeneration,
				ExpectedCredentialRevision: resource.CredentialRevision + 1,
				RequestID:                  task.RequestID,
			}
		}
		if err := tx.Model(&resourceRootModel{}).Where("id = ?", root.ID).Updates(map[string]any{
			"version": gorm.Expr("version + 1"), "updated_at": checkedAt,
		}).Error; err != nil {
			return fmt.Errorf("bump local Gmail validation resource version: %w", err)
		}
		if validation.Err != nil && s.systemLogs != nil {
			if err := s.systemLogs.CreateInTx(ctx, tx, &governancedomain.SystemLog{
				Level: "warning", Module: "gmail", EventType: "gmail.resource_validation_failed",
				RequestID: task.RequestID, BizType: "gmail_resource", BizID: strconv.FormatUint(uint64(task.ResourceID), 10),
				Message: "Gmail resource validation failed.", Detail: safeError,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return historyTask, retryPending, err
}

func withLocalGmailValidationTransaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = db.WithContext(ctx).Transaction(fn)
		if err == nil || !isLocalGmailDeadlock(err) || attempt == 2 {
			return err
		}
		timer := time.NewTimer(localGmailDeadlockBackoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func (s *Service) ReleaseLocalResourceValidation(ctx context.Context, task localResourceValidationTask) error {
	if s == nil || s.db == nil || task.ResourceID == 0 || task.OwnerUserID == 0 ||
		task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return nil
	}
	result := s.dbFor(ctx).Model(&localResourceModel{}).
		Where(`id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?
AND EXISTS (SELECT 1 FROM email_resources AS er WHERE er.id = gmail_resources.id AND er.type = 'gmail' AND er.owner_user_id = ?)`,
			task.ResourceID, LocalResourceValidating, task.ValidationGeneration,
			task.ExpectedCredentialRevision, task.OwnerUserID).
		Updates(map[string]any{
			"status":                LocalResourcePending,
			"validation_generation": task.ValidationGeneration + 1,
			"updated_at":            s.now().UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("release local Gmail validation: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		_ = s.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func lockLocalResource(tx *gorm.DB, resourceID uint) (*localResourceModel, error) {
	var root resourceRootModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", resourceID, "gmail").Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLocalResourceMissing
		}
		return nil, fmt.Errorf("lock local Gmail resource root: %w", err)
	}
	var resource localResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", resourceID).Take(&resource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLocalResourceMissing
		}
		return nil, fmt.Errorf("lock local Gmail resource: %w", err)
	}
	return &resource, nil
}
