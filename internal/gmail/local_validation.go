package gmail

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	typeGmailValidateLocal       = "gmail:validate_local"
	localGmailIMAPAddress        = "imap.gmail.com:993"
	localGmailIMAPServerName     = "imap.gmail.com"
	localGmailValidationTimeout  = 20 * time.Second
	localGmailValidationLease    = time.Minute
	localGmailValidationTaskTTL  = time.Minute
	localGmailValidationBatchMax = 200
)

var errLocalGmailAuthentication = errors.New("gmail: local IMAP authentication failed")

type localResourceValidationTask struct {
	ResourceID uint `json:"resourceId"`
}

type localIMAPValidationResult struct {
	SafeError string
	Temporary bool
	Err       error
}

func (s *Service) scheduleLocalResourceValidation(ctx context.Context, resourceID uint) error {
	if s == nil || s.queue == nil || resourceID == 0 {
		return nil
	}
	payload, err := json.Marshal(localResourceValidationTask{ResourceID: resourceID})
	if err != nil {
		return fmt.Errorf("encode local Gmail validation task: %w", err)
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailValidateLocal, payload),
		asynq.Queue(platform.QueueBackgroundValidation),
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

func (s *Service) DispatchLocalResourceValidations(ctx context.Context, limit int) error {
	if s == nil || s.queue == nil {
		return nil
	}
	if limit <= 0 || limit > localGmailValidationBatchMax {
		limit = localGmailValidationBatchMax
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	if err := s.dbFor(ctx).Model(&localResourceModel{}).
		Where("status = ? AND updated_at < ?", LocalResourceValidating, now.Add(-localGmailValidationLease)).
		Update("status", LocalResourcePending).Error; err != nil {
		return fmt.Errorf("recover stale local Gmail validations: %w", err)
	}
	var resourceIDs []uint
	if err := s.dbFor(ctx).Model(&localResourceModel{}).
		Where("status = ?", LocalResourcePending).
		Order("id ASC").Limit(limit).Pluck("id", &resourceIDs).Error; err != nil {
		return fmt.Errorf("list pending local Gmail validations: %w", err)
	}
	for _, resourceID := range resourceIDs {
		if err := s.scheduleLocalResourceValidation(ctx, resourceID); err != nil {
			return fmt.Errorf("schedule local Gmail validation %d: %w", resourceID, err)
		}
	}
	return nil
}

func decodeLocalResourceValidationTask(task *asynq.Task) (uint, error) {
	var payload localResourceValidationTask
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.ResourceID == 0 {
		return 0, fmt.Errorf("decode local Gmail validation task: %w", asynq.SkipRetry)
	}
	return payload.ResourceID, nil
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
	var email, appPassword string
	claimed := false
	if err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		resource, err := lockLocalResource(tx, resourceID)
		if err != nil {
			return err
		}
		if resource.Status != LocalResourcePending {
			return nil
		}
		result := tx.Model(&localResourceModel{}).
			Where("id = ? AND status = ?", resource.ID, LocalResourcePending).
			Update("status", LocalResourceValidating)
		if result.Error != nil {
			return fmt.Errorf("claim local Gmail validation: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil
		}
		email, appPassword, claimed = resource.Email, resource.AppPassword, true
		return nil
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	validation := validate(ctx, email, appPassword)
	nextStatus, safeError := localResourceRollbackNormal, ""
	shouldRetry := false
	if validation.Err != nil {
		nextStatus, safeError = LocalResourceAbnormal, strings.TrimSpace(validation.SafeError)
		if safeError == "" {
			safeError = "Gmail IMAP validation failed."
		}
		if validation.Temporary && platform.BackgroundTaskHasRetryHeadroom(ctx) {
			nextStatus, shouldRetry = LocalResourcePending, true
		}
	}

	finalized := false
	checkedAt := time.Now().UTC()
	if s.now != nil {
		checkedAt = s.now().UTC()
	}
	if err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		resource, err := lockLocalResource(tx, resourceID)
		if err != nil {
			return err
		}
		if resource.Status != LocalResourceValidating {
			return nil
		}
		result := tx.Model(&localResourceModel{}).
			Where("id = ? AND status = ?", resource.ID, LocalResourceValidating).
			Updates(map[string]any{
				"status": nextStatus, "last_safe_error": safeError, "last_checked_at": checkedAt,
			})
		if result.Error != nil {
			return fmt.Errorf("finish local Gmail validation: %w", result.Error)
		}
		finalized = result.RowsAffected == 1
		return nil
	}); err != nil {
		return err
	}
	if finalized && shouldRetry {
		return fmt.Errorf("validate local Gmail IMAP: %w", validation.Err)
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
