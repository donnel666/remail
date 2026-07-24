package infra

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MicrosoftTokenRefreshStateModel struct {
	ResourceID                 uint       `gorm:"primaryKey;column:id"`
	Status                     string     `gorm:"column:token_refresh_status"`
	Generation                 uint64     `gorm:"column:token_refresh_generation"`
	Failures                   int        `gorm:"column:token_refresh_failures"`
	ExpectedCredentialRevision uint64     `gorm:"column:token_refresh_expected_credential_revision"`
	OperatorUserID             *uint      `gorm:"column:token_refresh_operator_user_id"`
	IdempotencyKey             string     `gorm:"column:token_refresh_idempotency_key"`
	RequestID                  string     `gorm:"column:token_refresh_request_id"`
	Path                       string     `gorm:"column:token_refresh_path"`
	LastSafeError              string     `gorm:"column:token_refresh_last_safe_error"`
	RequestedAt                *time.Time `gorm:"column:token_refresh_requested_at"`
	StartedAt                  *time.Time `gorm:"column:token_refresh_started_at"`
	FinishedAt                 *time.Time `gorm:"column:token_refresh_finished_at"`
	UpdatedAt                  time.Time  `gorm:"column:updated_at"`
}

func (MicrosoftTokenRefreshStateModel) TableName() string { return "microsoft_resources" }

const microsoftTokenRefreshStateColumns = `
id,
token_refresh_status,
token_refresh_generation,
token_refresh_failures,
token_refresh_expected_credential_revision,
token_refresh_operator_user_id,
token_refresh_idempotency_key,
token_refresh_request_id,
token_refresh_path,
token_refresh_last_safe_error,
token_refresh_requested_at,
token_refresh_started_at,
token_refresh_finished_at,
updated_at`

type MicrosoftTokenRefreshRepo struct {
	db            *gorm.DB
	credentials   coreapp.MicrosoftCredentialPort
	operationLogs operationLogTxWriter
	systemLogs    *governanceinfra.SystemLogRepo
}

func NewMicrosoftTokenRefreshRepo(db *gorm.DB) *MicrosoftTokenRefreshRepo {
	return &MicrosoftTokenRefreshRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
		systemLogs:    governanceinfra.NewSystemLogRepo(db),
	}
}

func (r *MicrosoftTokenRefreshRepo) SetMicrosoftCredentialPort(credentials coreapp.MicrosoftCredentialPort) {
	if r != nil {
		r.credentials = credentials
	}
}

func (r *MicrosoftTokenRefreshRepo) withTx(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if r == nil || r.db == nil || fn == nil {
		return mailapp.ErrMicrosoftTokenRefreshUnavailable
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		tx = tx.WithContext(ctx)
		return fn(platform.WithGormTx(ctx, tx), tx)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tx = tx.WithContext(ctx)
		return fn(platform.WithGormTx(ctx, tx), tx)
	})
}

func (r *MicrosoftTokenRefreshRepo) Request(
	ctx context.Context,
	command mailapp.MicrosoftTokenRefreshCommand,
	operationLog *governancedomain.OperationLog,
) (*mailapp.MicrosoftTokenRefreshState, bool, error) {
	var accepted mailapp.MicrosoftTokenRefreshState
	reused := false
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		scope := fmt.Sprintf("%d:%s", command.OperatorUserID, strings.TrimSpace(command.IdempotencyKey))
		var replay MicrosoftTokenRefreshStateModel
		err := tx.Select(microsoftTokenRefreshStateColumns).
			Where("token_refresh_idempotency_scope = ?", scope).
			Take(&replay).Error
		if err == nil {
			if replay.ResourceID != command.ResourceID {
				return mailapp.ErrMicrosoftAdminIdempotencyConflict
			}
			accepted = tokenRefreshStateFromModel(replay)
			reused = true
			return r.createTokenRefreshOperationLogTx(txCtx, tx, operationLog, replay.ResourceID, true)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return tokenRefreshUnavailable("find idempotent resource state", err)
		}

		resource, err := r.lockMicrosoftCredentialScope(txCtx, command.ResourceID)
		if err != nil {
			return err
		}
		if resource.Status == "deleted" {
			return mailapp.ErrMicrosoftTokenRefreshConflict
		}
		if strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
			return mailapp.ErrMicrosoftTokenCredentialsMissing
		}
		var state MicrosoftTokenRefreshStateModel
		if err := tx.Select(microsoftTokenRefreshStateColumns).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&state, command.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return mailapp.ErrMicrosoftTokenRefreshNotFound
			}
			return tokenRefreshUnavailable("lock resource state", err)
		}
		if state.OperatorUserID != nil &&
			*state.OperatorUserID == command.OperatorUserID &&
			state.IdempotencyKey == strings.TrimSpace(command.IdempotencyKey) {
			accepted = tokenRefreshStateFromModel(state)
			reused = true
			return r.createTokenRefreshOperationLogTx(txCtx, tx, operationLog, state.ResourceID, true)
		}
		now := time.Now().UTC()
		state.Status = mailapp.MicrosoftTokenRefreshPending
		state.Generation++
		state.Failures = 0
		state.ExpectedCredentialRevision = resource.CredentialRevision
		state.OperatorUserID = &command.OperatorUserID
		state.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
		state.RequestID = strings.TrimSpace(command.RequestID)
		state.Path = strings.TrimSpace(command.Path)
		state.LastSafeError = ""
		state.RequestedAt = &now
		state.StartedAt = nil
		state.FinishedAt = nil
		state.UpdatedAt = now
		updated := tx.Model(&MicrosoftTokenRefreshStateModel{}).
			Where("id = ?", command.ResourceID).
			Updates(map[string]any{
				"token_refresh_status":                       state.Status,
				"token_refresh_generation":                   state.Generation,
				"token_refresh_failures":                     0,
				"token_refresh_expected_credential_revision": state.ExpectedCredentialRevision,
				"token_refresh_operator_user_id":             command.OperatorUserID,
				"token_refresh_idempotency_key":              state.IdempotencyKey,
				"token_refresh_request_id":                   state.RequestID,
				"token_refresh_path":                         state.Path,
				"token_refresh_last_safe_error":              "",
				"token_refresh_requested_at":                 now,
				"token_refresh_started_at":                   nil,
				"token_refresh_finished_at":                  nil,
				"updated_at":                                 now,
			})
		if updated.Error != nil {
			if isDuplicateKeyError(updated.Error) {
				return mailapp.ErrMicrosoftAdminIdempotencyConflict
			}
			return tokenRefreshUnavailable("request resource refresh", updated.Error)
		}
		accepted = tokenRefreshStateFromModel(state)
		return r.createTokenRefreshOperationLogTx(txCtx, tx, operationLog, command.ResourceID, false)
	})
	if err != nil {
		return nil, false, err
	}
	return &accepted, reused, nil
}

func (r *MicrosoftTokenRefreshRepo) ListPending(ctx context.Context, limit int) ([]mailapp.MicrosoftTokenRefreshState, error) {
	if limit <= 0 || limit > 100 {
		limit = 32
	}
	var models []MicrosoftTokenRefreshStateModel
	if err := r.db.WithContext(ctx).
		Select(microsoftTokenRefreshStateColumns).
		Where("token_refresh_status = ?", mailapp.MicrosoftTokenRefreshPending).
		Order("token_refresh_requested_at ASC, id ASC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, tokenRefreshUnavailable("list pending resource refreshes", err)
	}
	states := make([]mailapp.MicrosoftTokenRefreshState, len(models))
	for i := range models {
		states[i] = tokenRefreshStateFromModel(models[i])
	}
	return states, nil
}

func (r *MicrosoftTokenRefreshRepo) MarkProcessing(ctx context.Context, resourceID uint, generation uint64) (bool, error) {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&MicrosoftTokenRefreshStateModel{}).
		Where("id = ? AND token_refresh_generation = ? AND token_refresh_status = ?",
			resourceID, generation, mailapp.MicrosoftTokenRefreshPending).
		Updates(map[string]any{
			"token_refresh_status":          mailapp.MicrosoftTokenRefreshProcessing,
			"token_refresh_last_safe_error": "",
			"token_refresh_started_at":      now,
			"token_refresh_finished_at":     nil,
			"updated_at":                    now,
		})
	if result.Error != nil {
		return false, tokenRefreshUnavailable("activate resource refresh", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *MicrosoftTokenRefreshRepo) ReleaseInfrastructureFailure(ctx context.Context, resourceID uint, generation uint64, safeError string) (bool, error) {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&MicrosoftTokenRefreshStateModel{}).
		Where("id = ? AND token_refresh_generation = ? AND token_refresh_status = ?",
			resourceID, generation, mailapp.MicrosoftTokenRefreshProcessing).
		Updates(map[string]any{
			"token_refresh_status":          mailapp.MicrosoftTokenRefreshPending,
			"token_refresh_generation":      gorm.Expr("token_refresh_generation + 1"),
			"token_refresh_last_safe_error": safeMicrosoftTokenMessage(safeError),
			"token_refresh_started_at":      nil,
			"token_refresh_finished_at":     nil,
			"updated_at":                    now,
		})
	if result.Error != nil {
		return false, tokenRefreshUnavailable("release resource refresh infrastructure failure", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *MicrosoftTokenRefreshRepo) LoadExecution(ctx context.Context, task mailapp.MicrosoftTokenRefreshTask) (*mailapp.MicrosoftTokenRefreshExecution, bool, error) {
	var execution *mailapp.MicrosoftTokenRefreshExecution
	current := false
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		resource, err := r.lockMicrosoftCredentialScope(txCtx, task.ResourceID)
		if err != nil {
			return err
		}
		var state MicrosoftTokenRefreshStateModel
		if err := tx.Select(microsoftTokenRefreshStateColumns).First(&state, task.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return mailapp.ErrMicrosoftTokenRefreshNotFound
			}
			return tokenRefreshUnavailable("load resource refresh state", err)
		}
		if state.Generation != task.Generation || state.Status != mailapp.MicrosoftTokenRefreshProcessing ||
			state.ExpectedCredentialRevision != task.ExpectedCredentialRevision {
			return nil
		}
		if resource.CredentialRevision != state.ExpectedCredentialRevision {
			_, err := r.finishStaleStateTx(tx, state, "Resource credentials changed; token refresh was canceled.")
			return err
		}
		if resource.Status == "deleted" {
			_, err := r.finishStaleStateTx(tx, state, "Resource not found.")
			return err
		}
		if strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
			return mailapp.ErrMicrosoftTokenCredentialsMissing
		}
		current = true
		domainState := tokenRefreshStateFromModel(state)
		execution = &mailapp.MicrosoftTokenRefreshExecution{
			State:        domainState,
			EmailAddress: resource.EmailAddress,
			ClientID:     resource.ClientID,
			RefreshToken: resource.RefreshToken,
		}
		return nil
	})
	return execution, current, err
}

func (r *MicrosoftTokenRefreshRepo) RecordRetryableFailure(ctx context.Context, task mailapp.MicrosoftTokenRefreshTask, safeError string) (bool, error) {
	abnormal := true
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		resource, err := r.lockMicrosoftCredentialScope(txCtx, task.ResourceID)
		if err != nil {
			return err
		}
		state, current, err := lockCurrentTokenRefreshState(tx, task)
		if err != nil || !current {
			return err
		}
		if resource.CredentialRevision != state.ExpectedCredentialRevision {
			_, err := r.finishStaleStateTx(tx, *state, "Resource credentials changed; token refresh was canceled.")
			return err
		}
		failures := state.Failures + 1
		abnormal = failures >= runtimeconfig.Int("token_refresh_max_attempts", mailapp.MicrosoftTokenRefreshDefaultMaxAttempts, 1)
		status := mailapp.MicrosoftTokenRefreshPending
		var finishedAt any
		if abnormal {
			status = mailapp.MicrosoftTokenRefreshAbnormal
			finishedAt = time.Now().UTC()
			if err := r.applyMicrosoftTokenRefreshFailure(txCtx, coreapp.MicrosoftTokenRefreshFailure{
				ResourceID: task.ResourceID, ExpectedCredentialRevision: state.ExpectedCredentialRevision,
				SafeError: safeMicrosoftTokenMessage(safeError), RequestID: state.RequestID,
			}); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		updated := tx.Model(&MicrosoftTokenRefreshStateModel{}).
			Where("id = ? AND token_refresh_generation = ? AND token_refresh_status = ?",
				task.ResourceID, task.Generation, mailapp.MicrosoftTokenRefreshProcessing).
			Updates(map[string]any{
				"token_refresh_status":          status,
				"token_refresh_generation":      gorm.Expr("token_refresh_generation + 1"),
				"token_refresh_failures":        failures,
				"token_refresh_last_safe_error": safeMicrosoftTokenMessage(safeError),
				"token_refresh_started_at":      nil,
				"token_refresh_finished_at":     finishedAt,
				"updated_at":                    now,
			})
		if updated.Error != nil {
			return tokenRefreshUnavailable("record resource refresh failure", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return mailapp.ErrMicrosoftTokenRefreshStale
		}
		if abnormal {
			return r.createTokenSystemLogTx(txCtx, tx, state, "warning", "microsoft.token_refresh_retry_exhausted", "Microsoft token refresh retry attempts exhausted.", safeError)
		}
		return nil
	})
	if errors.Is(err, mailapp.ErrMicrosoftTokenRefreshStale) {
		return true, nil
	}
	return abnormal, err
}

func (r *MicrosoftTokenRefreshRepo) ApplyResult(ctx context.Context, task mailapp.MicrosoftTokenRefreshTask, result mailapp.MicrosoftTokenRefreshProtocolResult) error {
	stale := false
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		resource, err := r.lockMicrosoftCredentialScope(txCtx, task.ResourceID)
		if err != nil {
			return err
		}
		state, current, err := lockCurrentTokenRefreshState(tx, task)
		if err != nil || !current {
			stale = !current
			return err
		}
		if resource.CredentialRevision != state.ExpectedCredentialRevision {
			stale = true
			_, err := r.finishStaleStateTx(tx, *state, "Resource credentials changed; token refresh was canceled.")
			return err
		}
		now := time.Now().UTC()
		status := mailapp.MicrosoftTokenRefreshAbnormal
		safeError := safeMicrosoftTokenMessage(result.SafeMessage)
		level := "warning"
		eventType := "microsoft.token_refresh_failed"
		message := "Microsoft refresh-token diagnostic failed."
		if result.Valid {
			if err := r.applyMicrosoftTokenRefreshSuccess(txCtx, coreapp.MicrosoftTokenRefreshSuccess{
				ResourceID: task.ResourceID, ExpectedCredentialRevision: state.ExpectedCredentialRevision,
				ClientID: result.ClientID, RefreshToken: result.RefreshToken,
				RequestID: state.RequestID, Now: now,
			}); err != nil {
				return err
			}
			status = mailapp.MicrosoftTokenRefreshNormal
			safeError = ""
			level = "info"
			eventType = "microsoft.token_refresh_succeeded"
			message = "Microsoft refresh-token diagnostic succeeded."
		} else {
			if safeError == "" {
				safeError = "Microsoft refresh token is invalid or unavailable."
			}
			if err := r.applyMicrosoftTokenRefreshFailure(txCtx, coreapp.MicrosoftTokenRefreshFailure{
				ResourceID: task.ResourceID, ExpectedCredentialRevision: state.ExpectedCredentialRevision,
				SafeError: safeError, RequestID: state.RequestID,
			}); err != nil {
				return err
			}
		}
		updated := tx.Model(&MicrosoftTokenRefreshStateModel{}).
			Where("id = ? AND token_refresh_generation = ? AND token_refresh_status = ?",
				task.ResourceID, task.Generation, mailapp.MicrosoftTokenRefreshProcessing).
			Updates(map[string]any{
				"token_refresh_status":          status,
				"token_refresh_last_safe_error": safeError,
				"token_refresh_finished_at":     now,
				"updated_at":                    now,
			})
		if updated.Error != nil {
			return tokenRefreshUnavailable("finish resource refresh", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return mailapp.ErrMicrosoftTokenRefreshStale
		}
		return r.createTokenSystemLogTx(txCtx, tx, state, level, eventType, message, strings.TrimSpace(result.Category))
	})
	if err != nil {
		return err
	}
	if stale {
		return mailapp.ErrMicrosoftTokenRefreshStale
	}
	return nil
}

func lockCurrentTokenRefreshState(tx *gorm.DB, task mailapp.MicrosoftTokenRefreshTask) (*MicrosoftTokenRefreshStateModel, bool, error) {
	var state MicrosoftTokenRefreshStateModel
	err := tx.Select(microsoftTokenRefreshStateColumns).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&state, task.ResourceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, mailapp.ErrMicrosoftTokenRefreshNotFound
	}
	if err != nil {
		return nil, false, tokenRefreshUnavailable("lock resource refresh state", err)
	}
	current := state.Generation == task.Generation &&
		state.Status == mailapp.MicrosoftTokenRefreshProcessing &&
		state.ExpectedCredentialRevision == task.ExpectedCredentialRevision
	return &state, current, nil
}

func (r *MicrosoftTokenRefreshRepo) finishStaleStateTx(tx *gorm.DB, state MicrosoftTokenRefreshStateModel, safeError string) (bool, error) {
	result := tx.Model(&MicrosoftTokenRefreshStateModel{}).
		Where("id = ? AND token_refresh_generation = ? AND token_refresh_status = ?",
			state.ResourceID, state.Generation, mailapp.MicrosoftTokenRefreshProcessing).
		Updates(map[string]any{
			"token_refresh_status":          mailapp.MicrosoftTokenRefreshNormal,
			"token_refresh_generation":      gorm.Expr("token_refresh_generation + 1"),
			"token_refresh_last_safe_error": safeMicrosoftTokenMessage(safeError),
			"token_refresh_finished_at":     time.Now().UTC(),
			"updated_at":                    time.Now().UTC(),
		})
	if result.Error != nil {
		return false, tokenRefreshUnavailable("discard stale resource refresh", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *MicrosoftTokenRefreshRepo) lockMicrosoftCredentialScope(ctx context.Context, resourceID uint) (*coreapp.MicrosoftCredentialScope, error) {
	if r == nil || r.credentials == nil {
		return nil, mailapp.ErrMicrosoftTokenRefreshUnavailable
	}
	scope, err := r.credentials.LockMicrosoftCredentialScope(ctx, resourceID)
	if err != nil {
		return nil, microsoftTokenCredentialError("lock credential scope", err)
	}
	return scope, nil
}

func (r *MicrosoftTokenRefreshRepo) applyMicrosoftTokenRefreshSuccess(ctx context.Context, update coreapp.MicrosoftTokenRefreshSuccess) error {
	if r == nil || r.credentials == nil {
		return mailapp.ErrMicrosoftTokenRefreshUnavailable
	}
	return microsoftTokenCredentialError("apply refreshed credentials", r.credentials.ApplyMicrosoftTokenRefreshSuccess(ctx, update))
}

func (r *MicrosoftTokenRefreshRepo) applyMicrosoftTokenRefreshFailure(ctx context.Context, update coreapp.MicrosoftTokenRefreshFailure) error {
	if r == nil || r.credentials == nil {
		return mailapp.ErrMicrosoftTokenRefreshUnavailable
	}
	return microsoftTokenCredentialError("update token diagnostic", r.credentials.ApplyMicrosoftTokenRefreshFailure(ctx, update))
}

func microsoftTokenCredentialError(operation string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound):
		return mailapp.ErrMicrosoftTokenRefreshNotFound
	case errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted), errors.Is(err, coreapp.ErrMicrosoftCredentialChanged):
		return mailapp.ErrMicrosoftTokenRefreshStale
	default:
		return tokenRefreshUnavailable(operation, err)
	}
}

func (r *MicrosoftTokenRefreshRepo) createTokenSystemLogTx(
	ctx context.Context,
	tx *gorm.DB,
	state *MicrosoftTokenRefreshStateModel,
	level, eventType, message, detail string,
) error {
	if state == nil || r.systemLogs == nil {
		return nil
	}
	return r.systemLogs.CreateInTx(ctx, tx, &governancedomain.SystemLog{
		Level: level, Module: "mailtransport", EventType: eventType,
		RequestID: state.RequestID, BizType: "microsoft_resource",
		BizID:   strconv.FormatUint(uint64(state.ResourceID), 10),
		Message: message, Detail: safeMicrosoftTokenCategory(detail),
	})
}

func (r *MicrosoftTokenRefreshRepo) createTokenRefreshOperationLogTx(
	ctx context.Context,
	tx *gorm.DB,
	operationLog *governancedomain.OperationLog,
	resourceID uint,
	reused bool,
) error {
	if operationLog == nil || r.operationLogs == nil {
		return nil
	}
	operationLog.SafeSummary = fmt.Sprintf(
		"Microsoft refresh-token diagnostic accepted; task=token:%d; reused=%t.",
		resourceID,
		reused,
	)
	if err := r.operationLogs.CreateInTx(ctx, tx, operationLog); err != nil {
		return tokenRefreshUnavailable("create operation log", err)
	}
	return nil
}

func tokenRefreshStateFromModel(model MicrosoftTokenRefreshStateModel) mailapp.MicrosoftTokenRefreshState {
	operatorUserID := uint(0)
	if model.OperatorUserID != nil {
		operatorUserID = *model.OperatorUserID
	}
	return mailapp.MicrosoftTokenRefreshState{
		ResourceID: model.ResourceID, Generation: model.Generation,
		OperatorUserID: operatorUserID, ExpectedCredentialRevision: model.ExpectedCredentialRevision,
		Status: model.Status, Failures: model.Failures, LastSafeError: model.LastSafeError,
		IdempotencyKey: model.IdempotencyKey, RequestID: model.RequestID, Path: model.Path,
		RequestedAt: model.RequestedAt, StartedAt: model.StartedAt, FinishedAt: model.FinishedAt,
		UpdatedAt: model.UpdatedAt,
	}
}

func safeMicrosoftTokenMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func safeMicrosoftTokenCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "request", "auth_timeout", "rate_limited", "oauth_invalid_grant", "oauth_client", "oauth_permission", "mfa", "passkey", "phone", "password", "unknown_mailbox", "locked", "success":
		return value
	case "credential revision changed", "resource deleted":
		return value
	default:
		return ""
	}
}

func tokenRefreshUnavailable(operation string, err error) error {
	if err == nil {
		return mailapp.ErrMicrosoftTokenRefreshUnavailable
	}
	return fmt.Errorf("%w: %s: %w", mailapp.ErrMicrosoftTokenRefreshUnavailable, operation, err)
}
