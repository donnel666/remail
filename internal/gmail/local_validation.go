package gmail

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	typeGmailValidateLocal             = "gmail:validate_local"
	typeGmailValidationDispatcher      = "gmail:resource_validation_dispatcher"
	localGmailIMAPAddress              = "imap.gmail.com:993"
	localGmailIMAPServerName           = "imap.gmail.com"
	localGmailValidationTimeout        = 20 * time.Second
	localGmailValidationTaskTTL        = time.Minute
	localGmailValidationDispatchUnique = 30 * time.Second
	localGmailValidationSettleAfter    = time.Second
	localGmailValidationBatchMax       = 200
	localGmailValidationMaxFailures    = 3
)

var errLocalGmailAuthentication = errors.New("gmail: local IMAP authentication failed")

const localGmailValidationIdempotencyTTL = 24 * time.Hour

type localResourceValidationTask struct {
	ResourceID                 uint   `json:"resourceId"`
	OwnerUserID                uint   `json:"ownerUserId"`
	ValidationGeneration       uint64 `json:"validationGeneration"`
	ExpectedCredentialRevision uint64 `json:"expectedCredentialRevision"`
	RequestID                  string `json:"requestId,omitempty"`
}

type localIMAPValidationResult struct {
	SafeError string
	Temporary bool
	Err       error
}

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
	window := min(limit, runtimeconfig.Int("validation_dispatch_maximum", 128, 1))
	if s.backgroundExecution != nil {
		window = min(window, s.backgroundExecution.Snapshot().Limit)
	}
	var validating int64
	if err := s.dbFor(ctx).Model(&localResourceModel{}).Where("status = ?", LocalResourceValidating).Count(&validating).Error; err != nil {
		return fmt.Errorf("count active local Gmail validations: %w", err)
	}
	capacity := min(limit, max(0, window-int(validating)))
	if capacity <= 0 {
		return nil
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
	return dispatchErr
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
		asynq.Timeout(localGmailValidationTimeout+5*time.Second),
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
	return s.validateLocalResourceWith(ctx, resourceID, validateLocalGmailIMAP)
}

func (s *Service) validateLocalResourceWith(
	ctx context.Context,
	resourceID uint,
	validate func(context.Context, string, string) localIMAPValidationResult,
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
	return s.processLocalResourceValidationWith(ctx, task, validateLocalGmailIMAP)
}

func (s *Service) processLocalResourceValidationWith(
	ctx context.Context,
	task localResourceValidationTask,
	validate func(context.Context, string, string) localIMAPValidationResult,
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

	validation := validate(ctx, resource.Email, resource.AppPassword)
	if validation.Err == nil {
		enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		enqueueErr := s.enqueueValidatedLocalGmailHistory(enqueueCtx, localGmailHistoryTask{
			ResourceID: task.ResourceID, OwnerUserID: task.OwnerUserID,
			ValidationGeneration:       task.ValidationGeneration,
			ExpectedCredentialRevision: task.ExpectedCredentialRevision,
			RequestID:                  task.RequestID,
		})
		cancel()
		if enqueueErr != nil {
			return fmt.Errorf("create validated Gmail history task: %w", ErrLocalValidationDependency)
		}
	}
	retryPending, err := s.applyLocalResourceValidationResult(ctx, task, validation)
	if err != nil {
		return err
	}
	if retryPending {
		_ = s.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func (s *Service) applyLocalResourceValidationResult(
	ctx context.Context,
	task localResourceValidationTask,
	validation localIMAPValidationResult,
) (bool, error) {
	retryPending := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
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
		if validation.Err != nil {
			safeError = strings.TrimSpace(validation.SafeError)
			if safeError == "" {
				safeError = "Gmail IMAP validation failed."
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
	return retryPending, err
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

func validateLocalGmailIMAP(ctx context.Context, email, appPassword string) localIMAPValidationResult {
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithTimeout(ctx, localGmailValidationTimeout)
	defer cancel()
	client, closeClient, err := openLocalGmailIMAP(operationCtx, email, appPassword)
	if err != nil {
		if errors.Is(err, errLocalGmailAuthentication) {
			return localIMAPValidationResult{SafeError: "Gmail IMAP authentication failed. Check the app password.", Err: err}
		}
		return temporaryLocalIMAPValidation(err)
	}
	defer closeClient()
	if _, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		var imapErr *imap.Error
		if errors.As(err, &imapErr) && imapErr.Type == imap.StatusResponseTypeNo {
			return localIMAPValidationResult{SafeError: "Gmail inbox is unavailable. Check IMAP access.", Err: err}
		}
		return temporaryLocalIMAPValidation(err)
	}
	_ = client.Logout().Wait()
	return localIMAPValidationResult{}
}

func openLocalGmailIMAP(ctx context.Context, email, appPassword string) (*imapclient.Client, func(), error) {
	dialer := &net.Dialer{Timeout: localGmailValidationTimeout, KeepAlive: 30 * time.Second}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: localGmailIMAPServerName, NextProtos: []string{"imap"}}
	conn, err := (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", localGmailIMAPAddress)
	if err != nil {
		return nil, nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client := imapclient.New(conn, &imapclient.Options{TLSConfig: tlsConfig})
	stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
	closeClient := func() {
		stopClose()
		_ = client.Close()
	}
	if err := client.Login(strings.TrimSpace(email), appPassword).Wait(); err != nil {
		closeClient()
		if isDefinitiveLocalGmailAuthFailure(err) {
			return nil, nil, fmt.Errorf("%w: %v", errLocalGmailAuthentication, err)
		}
		return nil, nil, err
	}
	return client, closeClient, nil
}

func temporaryLocalIMAPValidation(err error) localIMAPValidationResult {
	return localIMAPValidationResult{
		SafeError: "Gmail IMAP is temporarily unavailable.", Temporary: true, Err: err,
	}
}

func isDefinitiveLocalGmailAuthFailure(err error) bool {
	var imapErr *imap.Error
	if !errors.As(err, &imapErr) || imapErr == nil {
		return false
	}
	if imapErr.Code == imap.ResponseCodeAuthenticationFailed || imapErr.Code == imap.ResponseCodeAuthorizationFailed {
		return true
	}
	if imapErr.Type != imap.StatusResponseTypeNo {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(imapErr.Text))
	return strings.Contains(text, "authentication failed") || strings.Contains(text, "login failed") ||
		strings.Contains(text, "invalid credentials") || strings.Contains(text, "app password")
}
