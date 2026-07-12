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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MicrosoftTokenRefreshJobModel struct {
	ID                         uint64     `gorm:"primaryKey;autoIncrement"`
	ResourceID                 uint       `gorm:"not null;column:resource_id"`
	OperatorUserID             uint       `gorm:"not null;column:operator_user_id"`
	ExpectedCredentialRevision uint64     `gorm:"not null;column:expected_credential_revision"`
	Status                     string     `gorm:"type:varchar(32);not null;default:'queued'"`
	Attempts                   int        `gorm:"not null;default:0"`
	MaxAttempts                int        `gorm:"not null;default:3;column:max_attempts"`
	ClaimToken                 string     `gorm:"type:char(36);not null;default:'';column:claim_token"`
	DispatchToken              string     `gorm:"type:char(36);not null;default:'';column:dispatch_token"`
	LastSafeError              string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID                  string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path                       string     `gorm:"type:varchar(255);not null;default:''"`
	DispatchedAt               *time.Time `gorm:"column:dispatched_at"`
	StartedAt                  *time.Time `gorm:"column:started_at"`
	FinishedAt                 *time.Time `gorm:"column:finished_at"`
	CreatedAt                  time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt                  time.Time  `gorm:"not null;autoUpdateTime"`
}

func (MicrosoftTokenRefreshJobModel) TableName() string {
	return "microsoft_token_refresh_jobs"
}

type MicrosoftTokenRefreshRequestModel struct {
	OperatorUserID uint      `gorm:"primaryKey;column:operator_user_id"`
	IdempotencyKey string    `gorm:"primaryKey;type:varchar(128);column:idempotency_key"`
	ResourceID     uint      `gorm:"not null;column:resource_id"`
	JobID          uint64    `gorm:"not null;column:job_id"`
	Reused         bool      `gorm:"not null;default:false"`
	CreatedAt      time.Time `gorm:"not null;autoCreateTime"`
}

func (MicrosoftTokenRefreshRequestModel) TableName() string {
	return "microsoft_token_refresh_requests"
}

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

func (r *MicrosoftTokenRefreshRepo) CreateOrReuse(
	ctx context.Context,
	command mailapp.MicrosoftTokenRefreshCommand,
	operationLog *governancedomain.OperationLog,
) (*mailapp.MicrosoftTokenRefreshJob, bool, error) {
	var accepted *MicrosoftTokenRefreshJobModel
	reused := false
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		replayReceipt := func(receipt MicrosoftTokenRefreshRequestModel) error {
			if receipt.ResourceID != command.ResourceID {
				return mailapp.ErrMicrosoftAdminIdempotencyConflict
			}
			var existing MicrosoftTokenRefreshJobModel
			if err := tx.Where("id = ?", receipt.JobID).Take(&existing).Error; err != nil {
				return tokenRefreshUnavailable("reload idempotent job", err)
			}
			accepted = &existing
			reused = true
			return r.createTokenRefreshOperationLogTx(txCtx, tx, operationLog, accepted.ID, true)
		}

		var receipt MicrosoftTokenRefreshRequestModel
		err := tx.
			Where("operator_user_id = ? AND idempotency_key = ?", command.OperatorUserID, command.IdempotencyKey).
			Take(&receipt).Error
		if err == nil {
			return replayReceipt(receipt)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return tokenRefreshUnavailable("find idempotency receipt", err)
		}

		resource, err := r.lockMicrosoftCredentialScope(txCtx, command.ResourceID)
		if err != nil {
			return err
		}
		receipt = MicrosoftTokenRefreshRequestModel{}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operator_user_id = ? AND idempotency_key = ?", command.OperatorUserID, command.IdempotencyKey).
			Take(&receipt).Error
		if err == nil {
			return replayReceipt(receipt)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return tokenRefreshUnavailable("lock idempotency receipt", err)
		}
		if resource.Status == "deleted" {
			return mailapp.ErrMicrosoftTokenRefreshConflict
		}
		if strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
			return mailapp.ErrMicrosoftTokenCredentialsMissing
		}

		var active MicrosoftTokenRefreshJobModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND status IN ?", command.ResourceID, []string{
				mailapp.MicrosoftTokenRefreshQueued,
				mailapp.MicrosoftTokenRefreshRunning,
			}).
			Order("id DESC").
			Take(&active).Error
		switch {
		case err == nil && active.ExpectedCredentialRevision == resource.CredentialRevision:
			accepted = &active
			reused = true
		case err == nil:
			now := time.Now().UTC()
			if err := tx.Model(&MicrosoftTokenRefreshJobModel{}).
				Where("id = ? AND status IN ?", active.ID, []string{
					mailapp.MicrosoftTokenRefreshQueued,
					mailapp.MicrosoftTokenRefreshRunning,
				}).
				Updates(map[string]any{
					"status":          mailapp.MicrosoftTokenRefreshCanceled,
					"claim_token":     "",
					"dispatch_token":  "",
					"dispatched_at":   nil,
					"last_safe_error": "Resource credentials changed; token refresh was canceled.",
					"finished_at":     now,
					"updated_at":      now,
				}).Error; err != nil {
				return tokenRefreshUnavailable("cancel stale active job", err)
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return tokenRefreshUnavailable("find active job", err)
		}

		if accepted == nil {
			job := &MicrosoftTokenRefreshJobModel{
				ResourceID:                 command.ResourceID,
				OperatorUserID:             command.OperatorUserID,
				ExpectedCredentialRevision: resource.CredentialRevision,
				Status:                     mailapp.MicrosoftTokenRefreshQueued,
				MaxAttempts:                mailapp.MicrosoftTokenRefreshDefaultMaxAttempts,
				RequestID:                  strings.TrimSpace(command.RequestID),
				Path:                       strings.TrimSpace(command.Path),
			}
			if err := tx.Create(job).Error; err != nil {
				return tokenRefreshUnavailable("create job", err)
			}
			accepted = job
		}

		receipt = MicrosoftTokenRefreshRequestModel{
			OperatorUserID: command.OperatorUserID,
			IdempotencyKey: command.IdempotencyKey,
			ResourceID:     command.ResourceID,
			JobID:          accepted.ID,
			Reused:         reused,
		}
		if err := tx.Create(&receipt).Error; err != nil {
			if isDuplicateKeyError(err) {
				return mailapp.ErrMicrosoftAdminIdempotencyConflict
			}
			return tokenRefreshUnavailable("create idempotency receipt", err)
		}
		return r.createTokenRefreshOperationLogTx(txCtx, tx, operationLog, accepted.ID, reused)
	})
	if err != nil {
		return nil, false, err
	}
	job := tokenRefreshJobFromModel(accepted)
	return job, reused, nil
}

func (r *MicrosoftTokenRefreshRepo) ClaimDispatchable(
	ctx context.Context,
	limit int,
	runningStaleBefore, dispatchStaleBefore time.Time,
) ([]mailapp.MicrosoftTokenRefreshJob, error) {
	if limit <= 0 {
		limit = 32
	}
	models := make([]MicrosoftTokenRefreshJobModel, 0, limit)
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		appendLocked := func(query *gorm.DB) error {
			remaining := limit - len(models)
			if remaining <= 0 {
				return nil
			}
			var batch []MicrosoftTokenRefreshJobModel
			if err := query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Limit(remaining).
				Find(&batch).Error; err != nil {
				return err
			}
			models = append(models, batch...)
			return nil
		}
		if err := appendLocked(tx.
			Where("status = ? AND updated_at < ? AND (dispatched_at IS NULL OR dispatched_at < ?)", mailapp.MicrosoftTokenRefreshRunning, runningStaleBefore, dispatchStaleBefore).
			Order("updated_at ASC, id ASC")); err != nil {
			return tokenRefreshUnavailable("lock stale running jobs", err)
		}
		if err := appendLocked(tx.
			Where("status = ? AND attempts < max_attempts AND (dispatched_at IS NULL OR dispatched_at < ?)", mailapp.MicrosoftTokenRefreshQueued, dispatchStaleBefore).
			Order("id ASC")); err != nil {
			return tokenRefreshUnavailable("lock queued jobs", err)
		}
		for i := range models {
			dispatchToken := platform.NewUUIDV4String()
			updated := tx.Model(&MicrosoftTokenRefreshJobModel{}).
				Where("id = ? AND status = ? AND dispatch_token = ?", models[i].ID, models[i].Status, models[i].DispatchToken).
				UpdateColumns(map[string]any{
					"dispatch_token": dispatchToken,
					"dispatched_at":  now,
					"updated_at":     gorm.Expr("updated_at"),
				})
			if updated.Error != nil {
				return tokenRefreshUnavailable("claim dispatch", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return mailapp.ErrMicrosoftTokenRefreshConflict
			}
			models[i].DispatchToken = dispatchToken
			models[i].DispatchedAt = &now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]mailapp.MicrosoftTokenRefreshJob, len(models))
	for i := range models {
		jobs[i] = *tokenRefreshJobFromModel(&models[i])
	}
	return jobs, nil
}

func (r *MicrosoftTokenRefreshRepo) MarkDispatchFailed(ctx context.Context, id uint64, dispatchToken, safeError string) error {
	result := r.db.WithContext(ctx).Model(&MicrosoftTokenRefreshJobModel{}).
		Where("id = ? AND dispatch_token = ? AND status IN ?", id, strings.TrimSpace(dispatchToken), []string{
			mailapp.MicrosoftTokenRefreshQueued,
			mailapp.MicrosoftTokenRefreshRunning,
		}).
		UpdateColumns(map[string]any{
			"dispatch_token":  "",
			"dispatched_at":   nil,
			"last_safe_error": safeMicrosoftTokenMessage(safeError),
			"updated_at":      gorm.Expr("updated_at"),
		})
	if result.Error != nil {
		return tokenRefreshUnavailable("release failed dispatch", result.Error)
	}
	return nil
}

func (r *MicrosoftTokenRefreshRepo) ReleaseDispatch(ctx context.Context, id uint64, dispatchToken string) error {
	return r.MarkDispatchFailed(ctx, id, dispatchToken, "")
}

func (r *MicrosoftTokenRefreshRepo) ClaimExecution(
	ctx context.Context,
	id uint64,
	dispatchToken string,
	runningStaleBefore time.Time,
) (*mailapp.MicrosoftTokenRefreshExecution, bool, error) {
	ref, err := r.findTokenRefreshJobRef(ctx, id)
	if err != nil || ref.ResourceID == 0 {
		return nil, false, err
	}
	var execution *mailapp.MicrosoftTokenRefreshExecution
	claimed := false
	err = r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		resource, err := r.lockMicrosoftCredentialScope(txCtx, ref.ResourceID)
		if err != nil {
			return err
		}
		var job MicrosoftTokenRefreshJobModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return tokenRefreshUnavailable("lock execution job", err)
		}
		if job.DispatchToken != strings.TrimSpace(dispatchToken) {
			return nil
		}
		isQueued := job.Status == mailapp.MicrosoftTokenRefreshQueued
		isStaleRunning := job.Status == mailapp.MicrosoftTokenRefreshRunning && job.UpdatedAt.Before(runningStaleBefore)
		if !isQueued && !isStaleRunning {
			return nil
		}
		now := time.Now().UTC()
		nextAttempts := job.Attempts
		if isStaleRunning {
			nextAttempts++
		}
		if nextAttempts >= normalizeMicrosoftTokenMaxAttempts(job.MaxAttempts) {
			return finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshFailed, nextAttempts, "Microsoft token refresh retry attempts exhausted.", now)
		}
		if job.ExpectedCredentialRevision != resource.CredentialRevision {
			return finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshCanceled, nextAttempts, "Resource credentials changed; token refresh was canceled.", now)
		}
		if resource.Status == "deleted" {
			return finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshFailed, nextAttempts, "Resource not found.", now)
		}
		if strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
			return finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshFailed, nextAttempts, "Microsoft refresh token is not configured.", now)
		}
		claimToken := platform.NewUUIDV4String()
		updated := tx.Model(&MicrosoftTokenRefreshJobModel{}).
			Where("id = ? AND status = ? AND dispatch_token = ?", job.ID, job.Status, job.DispatchToken).
			Updates(map[string]any{
				"status":          mailapp.MicrosoftTokenRefreshRunning,
				"attempts":        nextAttempts,
				"claim_token":     claimToken,
				"dispatch_token":  "",
				"dispatched_at":   nil,
				"last_safe_error": "",
				"started_at":      now,
				"finished_at":     nil,
				"updated_at":      now,
			})
		if updated.Error != nil {
			return tokenRefreshUnavailable("claim execution", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return nil
		}
		job.Status = mailapp.MicrosoftTokenRefreshRunning
		job.Attempts = nextAttempts
		job.ClaimToken = claimToken
		job.DispatchToken = ""
		job.DispatchedAt = nil
		job.StartedAt = &now
		job.UpdatedAt = now
		execution = &mailapp.MicrosoftTokenRefreshExecution{
			Job:          *tokenRefreshJobFromModel(&job),
			EmailAddress: resource.EmailAddress,
			ClientID:     resource.ClientID,
			RefreshToken: resource.RefreshToken,
		}
		claimed = true
		return nil
	})
	return execution, claimed, err
}

func (r *MicrosoftTokenRefreshRepo) MarkRetryableFailure(ctx context.Context, id uint64, claimToken, safeError string) (bool, error) {
	ref, err := r.findTokenRefreshJobRef(ctx, id)
	if err != nil || ref.ResourceID == 0 {
		return true, err
	}
	exhausted := false
	err = r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		resource, err := r.lockMicrosoftCredentialScope(txCtx, ref.ResourceID)
		if err != nil {
			return err
		}
		job, err := lockMicrosoftTokenRunningJobTx(tx, id, claimToken)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if job.ExpectedCredentialRevision != resource.CredentialRevision {
			exhausted = true
			if err := finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshCanceled, job.Attempts, "Resource credentials changed; token refresh was canceled.", now); err != nil {
				return err
			}
			return r.createTokenSystemLogTx(txCtx, tx, job, "warning", "microsoft.token_refresh_stale", "Microsoft token refresh result was discarded.", "credential revision changed")
		}
		nextAttempts := job.Attempts + 1
		if nextAttempts >= normalizeMicrosoftTokenMaxAttempts(job.MaxAttempts) {
			exhausted = true
			if err := r.applyMicrosoftTokenRefreshFailure(txCtx, coreapp.MicrosoftTokenRefreshFailure{
				ResourceID: ref.ResourceID, ExpectedCredentialRevision: job.ExpectedCredentialRevision,
				SafeError: safeMicrosoftTokenMessage(safeError), RequestID: job.RequestID,
			}); err != nil {
				return err
			}
			if err := finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshFailed, nextAttempts, safeError, now); err != nil {
				return err
			}
			return r.createTokenSystemLogTx(txCtx, tx, job, "warning", "microsoft.token_refresh_retry_exhausted", "Microsoft token refresh retry attempts exhausted.", safeError)
		}
		updated := tx.Model(&MicrosoftTokenRefreshJobModel{}).
			Where("id = ? AND status = ? AND claim_token = ?", job.ID, mailapp.MicrosoftTokenRefreshRunning, job.ClaimToken).
			Updates(map[string]any{
				"status":          mailapp.MicrosoftTokenRefreshQueued,
				"attempts":        nextAttempts,
				"claim_token":     "",
				"dispatch_token":  "",
				"dispatched_at":   nil,
				"last_safe_error": safeMicrosoftTokenMessage(safeError),
				"finished_at":     nil,
				"updated_at":      now,
			})
		if updated.Error != nil {
			return tokenRefreshUnavailable("requeue retryable job", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return mailapp.ErrMicrosoftTokenRefreshConflict
		}
		return nil
	})
	return exhausted, err
}

func (r *MicrosoftTokenRefreshRepo) ApplyResult(
	ctx context.Context,
	id uint64,
	claimToken string,
	result mailapp.MicrosoftTokenRefreshProtocolResult,
) error {
	ref, err := r.findTokenRefreshJobRef(ctx, id)
	if err != nil || ref.ResourceID == 0 {
		return err
	}
	stale := false
	err = r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		resource, err := r.lockMicrosoftCredentialScope(txCtx, ref.ResourceID)
		if err != nil {
			return err
		}
		job, err := lockMicrosoftTokenRunningJobTx(tx, id, claimToken)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if job.ExpectedCredentialRevision != resource.CredentialRevision {
			stale = true
			if err := finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshCanceled, job.Attempts, "Resource credentials changed; token refresh was canceled.", now); err != nil {
				return err
			}
			return r.createTokenSystemLogTx(txCtx, tx, job, "warning", "microsoft.token_refresh_stale", "Microsoft token refresh result was discarded.", "credential revision changed")
		}
		if resource.Status == "deleted" {
			stale = true
			if err := finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshFailed, job.Attempts, "Resource not found.", now); err != nil {
				return err
			}
			return r.createTokenSystemLogTx(txCtx, tx, job, "warning", "microsoft.token_refresh_resource_gone", "Microsoft token refresh resource is unavailable.", "resource deleted")
		}

		if result.Valid {
			if err := r.applyMicrosoftTokenRefreshSuccess(txCtx, coreapp.MicrosoftTokenRefreshSuccess{
				ResourceID: ref.ResourceID, ExpectedCredentialRevision: job.ExpectedCredentialRevision,
				ClientID: result.ClientID, RefreshToken: result.RefreshToken,
				RequestID: job.RequestID, Now: now,
			}); err != nil {
				return err
			}
			if err := finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshSucceeded, job.Attempts, "", now); err != nil {
				return err
			}
			return r.createTokenSystemLogTx(txCtx, tx, job, "info", "microsoft.token_refresh_succeeded", "Microsoft refresh-token diagnostic succeeded.", strings.TrimSpace(result.Category))
		}

		safeError := safeMicrosoftTokenMessage(result.SafeMessage)
		if safeError == "" {
			safeError = "Microsoft refresh token is invalid or unavailable."
		}
		if err := r.applyMicrosoftTokenRefreshFailure(txCtx, coreapp.MicrosoftTokenRefreshFailure{
			ResourceID: ref.ResourceID, ExpectedCredentialRevision: job.ExpectedCredentialRevision,
			SafeError: safeError, RequestID: job.RequestID,
		}); err != nil {
			return err
		}
		if err := finishMicrosoftTokenJobTx(tx, job.ID, job.Status, job.ClaimToken, mailapp.MicrosoftTokenRefreshFailed, job.Attempts, safeError, now); err != nil {
			return err
		}
		return r.createTokenSystemLogTx(txCtx, tx, job, "warning", "microsoft.token_refresh_failed", "Microsoft refresh-token diagnostic failed.", strings.TrimSpace(result.Category))
	})
	if err != nil {
		return err
	}
	if stale {
		return mailapp.ErrMicrosoftTokenRefreshStale
	}
	return nil
}

func lockMicrosoftTokenRunningJobTx(tx *gorm.DB, jobID uint64, claimToken string) (*MicrosoftTokenRefreshJobModel, error) {
	var job MicrosoftTokenRefreshJobModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = ? AND claim_token = ?", jobID, mailapp.MicrosoftTokenRefreshRunning, strings.TrimSpace(claimToken)).
		Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, mailapp.ErrMicrosoftTokenRefreshConflict
	}
	if err != nil {
		return nil, tokenRefreshUnavailable("lock running job", err)
	}
	return &job, nil
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

type microsoftTokenRefreshJobRef struct {
	ResourceID uint `gorm:"column:resource_id"`
}

func (r *MicrosoftTokenRefreshRepo) findTokenRefreshJobRef(ctx context.Context, id uint64) (microsoftTokenRefreshJobRef, error) {
	var ref microsoftTokenRefreshJobRef
	err := r.db.WithContext(ctx).Model(&MicrosoftTokenRefreshJobModel{}).
		Select("resource_id").
		Where("id = ?", id).
		Take(&ref).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ref, nil
	}
	if err != nil {
		return ref, tokenRefreshUnavailable("find job resource", err)
	}
	return ref, nil
}

func finishMicrosoftTokenJobTx(tx *gorm.DB, id uint64, fromStatus, claimToken, toStatus string, attempts int, safeError string, now time.Time) error {
	query := tx.Model(&MicrosoftTokenRefreshJobModel{}).Where("id = ? AND status = ?", id, fromStatus)
	if strings.TrimSpace(claimToken) != "" {
		query = query.Where("claim_token = ?", strings.TrimSpace(claimToken))
	}
	updated := query.Updates(map[string]any{
		"status":          toStatus,
		"attempts":        attempts,
		"claim_token":     "",
		"dispatch_token":  "",
		"dispatched_at":   nil,
		"last_safe_error": safeMicrosoftTokenMessage(safeError),
		"finished_at":     now,
		"updated_at":      now,
	})
	if updated.Error != nil {
		return tokenRefreshUnavailable("finish job", updated.Error)
	}
	if updated.RowsAffected != 1 {
		return mailapp.ErrMicrosoftTokenRefreshConflict
	}
	return nil
}

func (r *MicrosoftTokenRefreshRepo) createTokenSystemLogTx(
	ctx context.Context,
	tx *gorm.DB,
	job *MicrosoftTokenRefreshJobModel,
	level, eventType, message, detail string,
) error {
	if job == nil {
		return nil
	}
	return r.systemLogs.CreateInTx(ctx, tx, &governancedomain.SystemLog{
		Level:     level,
		Module:    "mailtransport",
		EventType: eventType,
		RequestID: job.RequestID,
		BizType:   "microsoft_resource",
		BizID:     strconv.FormatUint(uint64(job.ResourceID), 10),
		Message:   message,
		Detail:    safeMicrosoftTokenCategory(detail),
	})
}

func (r *MicrosoftTokenRefreshRepo) createTokenRefreshOperationLogTx(
	ctx context.Context,
	tx *gorm.DB,
	operationLog *governancedomain.OperationLog,
	jobID uint64,
	reused bool,
) error {
	if operationLog == nil {
		return nil
	}
	operationLog.SafeSummary = fmt.Sprintf(
		"Microsoft refresh-token diagnostic accepted; task=token:%d; reused=%t.",
		jobID,
		reused,
	)
	if err := r.operationLogs.CreateInTx(ctx, tx, operationLog); err != nil {
		return tokenRefreshUnavailable("create operation log", err)
	}
	return nil
}

func tokenRefreshJobFromModel(model *MicrosoftTokenRefreshJobModel) *mailapp.MicrosoftTokenRefreshJob {
	if model == nil {
		return nil
	}
	return &mailapp.MicrosoftTokenRefreshJob{
		ID:                         model.ID,
		ResourceID:                 model.ResourceID,
		OperatorUserID:             model.OperatorUserID,
		ExpectedCredentialRevision: model.ExpectedCredentialRevision,
		Status:                     model.Status,
		Attempts:                   model.Attempts,
		MaxAttempts:                normalizeMicrosoftTokenMaxAttempts(model.MaxAttempts),
		ClaimToken:                 model.ClaimToken,
		DispatchToken:              model.DispatchToken,
		LastSafeError:              model.LastSafeError,
		RequestID:                  model.RequestID,
		Path:                       model.Path,
		DispatchedAt:               model.DispatchedAt,
		StartedAt:                  model.StartedAt,
		FinishedAt:                 model.FinishedAt,
		CreatedAt:                  model.CreatedAt,
		UpdatedAt:                  model.UpdatedAt,
	}
}

func normalizeMicrosoftTokenMaxAttempts(value int) int {
	if value <= 0 {
		return mailapp.MicrosoftTokenRefreshDefaultMaxAttempts
	}
	return value
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
	case "request", "auth_timeout", "rate_limited", "oauth_invalid_grant", "oauth_client", "oauth_permission", "mfa", "passkey", "phone", "password", "unknown_mailbox", "locked":
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
