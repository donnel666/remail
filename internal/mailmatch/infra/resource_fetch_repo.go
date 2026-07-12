package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ResourceFetchJobModel struct {
	ID                         uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID                 uint       `gorm:"not null;column:resource_id"`
	OperatorUserID             uint       `gorm:"not null;column:operator_user_id"`
	ExpectedCredentialRevision uint64     `gorm:"not null;column:expected_credential_revision"`
	Recipient                  string     `gorm:"type:varchar(255);not null"`
	Status                     string     `gorm:"type:varchar(32);not null;default:'queued'"`
	Attempts                   int        `gorm:"not null;default:0"`
	MaxAttempts                int        `gorm:"not null;default:3;column:max_attempts"`
	FetchedCount               int        `gorm:"not null;default:0;column:fetched_count"`
	StoredCount                int        `gorm:"not null;default:0;column:stored_count"`
	MatchedCount               int        `gorm:"not null;default:0;column:matched_count"`
	SinceAt                    *time.Time `gorm:"column:since_at"`
	UntilAt                    *time.Time `gorm:"column:until_at"`
	ClaimToken                 string     `gorm:"type:char(36);not null;default:'';column:claim_token"`
	DispatchToken              string     `gorm:"type:char(36);not null;default:'';column:dispatch_token"`
	LastSafeError              string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID                  string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path                       string     `gorm:"type:varchar(255);not null;default:''"`
	DispatchedAt               *time.Time `gorm:"column:dispatched_at"`
	StartedAt                  *time.Time `gorm:"column:started_at"`
	FinishedAt                 *time.Time `gorm:"column:finished_at"`
	CreatedAt                  time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt                  time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (ResourceFetchJobModel) TableName() string { return "mailmatch_resource_fetch_jobs" }

type ResourceFetchRequestModel struct {
	OperatorUserID uint      `gorm:"primaryKey;column:operator_user_id"`
	IdempotencyKey string    `gorm:"primaryKey;type:varchar(128);column:idempotency_key"`
	ResourceID     uint      `gorm:"not null;column:resource_id"`
	JobID          uint      `gorm:"not null;column:job_id"`
	Reused         bool      `gorm:"not null;default:false"`
	CreatedAt      time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (ResourceFetchRequestModel) TableName() string { return "mailmatch_resource_fetch_requests" }

var errResourceFetchRequestRace = errors.New("mailmatch: resource fetch idempotency receipt raced")

type ResourceFetchRepo struct {
	db            *gorm.DB
	credentials   coreapp.MicrosoftCredentialPort
	operationLogs *governanceinfra.OperationLogRepo
	systemLogs    *governanceinfra.SystemLogRepo
}

func NewResourceFetchRepo(db *gorm.DB) *ResourceFetchRepo {
	return &ResourceFetchRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
		systemLogs:    governanceinfra.NewSystemLogRepo(db),
	}
}

func (r *ResourceFetchRepo) SetMicrosoftCredentialPort(credentials coreapp.MicrosoftCredentialPort) {
	if r != nil {
		r.credentials = credentials
	}
}

func (r *ResourceFetchRepo) withTx(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		tx = tx.WithContext(ctx)
		return fn(platform.WithGormTx(ctx, tx), tx)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tx = tx.WithContext(ctx)
		return fn(platform.WithGormTx(ctx, tx), tx)
	})
}

func (r *ResourceFetchRepo) CreateOrReuseResourceFetch(ctx context.Context, job *domain.ResourceFetchJob, log *governancedomain.OperationLog) (bool, error) {
	idempotencyKey := ""
	if job != nil {
		idempotencyKey = strings.TrimSpace(job.IdempotencyKey)
	}
	if r == nil || r.db == nil || job == nil || job.ResourceID == 0 || job.OperatorUserID == 0 || idempotencyKey == "" || len(idempotencyKey) > 128 {
		return false, domain.ErrInvalidRequest
	}
	requestedResourceID := job.ResourceID
	operatorUserID := job.OperatorUserID
	reused := false
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		receipt, err := findResourceFetchRequest(tx, operatorUserID, idempotencyKey)
		if err != nil {
			return err
		}
		if receipt != nil {
			if receipt.ResourceID != requestedResourceID {
				return domain.ErrResourceFetchIdempotencyConflict
			}
			var existing ResourceFetchJobModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, receipt.JobID).Error; err != nil {
				return fmt.Errorf("find idempotent resource fetch job: %w", err)
			}
			*job = resourceFetchJobModelToDomain(existing)
			reused = receipt.Reused
			return r.createResourceFetchOperationLog(txCtx, tx, job, reused, log)
		}

		scope, err := r.lockResourceFetchScope(txCtx, job.ResourceID)
		if err != nil {
			return err
		}
		if err := validateResourceFetchScope(scope, 0); err != nil {
			return err
		}

		var active ResourceFetchJobModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND status IN ?", job.ResourceID, activeResourceFetchStatuses()).
			Order("id ASC").
			First(&active).Error
		createNew := false
		switch {
		case err == nil && active.ExpectedCredentialRevision == scope.CredentialRevision:
			*job = resourceFetchJobModelToDomain(active)
			reused = true
		case err == nil:
			now := time.Now().UTC()
			result := tx.Model(&ResourceFetchJobModel{}).
				Where("id = ? AND status IN ?", active.ID, activeResourceFetchStatuses()).
				Updates(map[string]any{
					"status":          string(domain.ResourceFetchJobCanceled),
					"claim_token":     "",
					"dispatch_token":  "",
					"dispatched_at":   nil,
					"last_safe_error": "Resource credentials changed; mail fetch was canceled.",
					"finished_at":     now,
					"updated_at":      now,
				})
			if result.Error != nil {
				return fmt.Errorf("cancel stale active resource fetch: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return domain.ErrResourceFetchJobConflict
			}
			if err := r.systemLogs.CreateInTx(txCtx, tx, &governancedomain.SystemLog{
				Level:     "warning",
				Module:    "mailmatch",
				EventType: "resource_fetch_canceled",
				RequestID: active.RequestID,
				BizType:   "microsoft_resource",
				BizID:     fmt.Sprintf("%d", active.ResourceID),
				Message:   "Microsoft resource mail fetch was canceled after credentials changed.",
				Detail:    fmt.Sprintf("task=fetch:%d; category=credential_changed", active.ID),
			}); err != nil {
				return err
			}
			createNew = true
		case errors.Is(err, gorm.ErrRecordNotFound):
			createNew = true
		default:
			return fmt.Errorf("find active resource fetch job: %w", err)
		}
		if createNew {
			model := resourceFetchJobModelFromDomain(*job)
			model.ExpectedCredentialRevision = scope.CredentialRevision
			model.Recipient = strings.ToLower(strings.TrimSpace(scope.EmailAddress))
			model.Status = string(domain.ResourceFetchJobQueued)
			model.MaxAttempts = normalizeResourceFetchMaxAttempts(model.MaxAttempts)
			if err := tx.Create(&model).Error; err != nil {
				if isDuplicateKeyError(err) {
					return domain.ErrResourceFetchJobConflict
				}
				return fmt.Errorf("create resource fetch job: %w", err)
			}
			*job = resourceFetchJobModelToDomain(model)
		}

		receipt = &ResourceFetchRequestModel{
			OperatorUserID: operatorUserID,
			IdempotencyKey: idempotencyKey,
			ResourceID:     requestedResourceID,
			JobID:          job.ID,
			Reused:         reused,
		}
		if err := tx.Create(receipt).Error; err != nil {
			if isDuplicateKeyError(err) {
				return errResourceFetchRequestRace
			}
			return fmt.Errorf("create resource fetch idempotency receipt: %w", err)
		}
		return r.createResourceFetchOperationLog(txCtx, tx, job, reused, log)
	})
	if errors.Is(err, errResourceFetchRequestRace) {
		// The competing transaction committed the authoritative receipt. Retry
		// once through the idempotent read branch; the rolled-back attempt left
		// neither a task nor a success audit behind.
		job.ResourceID = requestedResourceID
		job.OperatorUserID = operatorUserID
		job.IdempotencyKey = idempotencyKey
		return r.CreateOrReuseResourceFetch(ctx, job, log)
	}
	if err != nil {
		return false, err
	}
	job.IdempotencyKey = idempotencyKey
	return reused, nil
}

func (r *ResourceFetchRepo) createResourceFetchOperationLog(
	ctx context.Context,
	tx *gorm.DB,
	job *domain.ResourceFetchJob,
	reused bool,
	log *governancedomain.OperationLog,
) error {
	if log == nil {
		return nil
	}
	log.SafeSummary = fmt.Sprintf(
		"Microsoft resource mail fetch accepted; task=fetch:%d; reused=%t.",
		job.ID,
		reused,
	)
	return r.operationLogs.CreateInTx(ctx, tx, log)
}

func findResourceFetchRequest(tx *gorm.DB, operatorUserID uint, idempotencyKey string) (*ResourceFetchRequestModel, error) {
	var model ResourceFetchRequestModel
	err := tx.Where("operator_user_id = ? AND idempotency_key = ?", operatorUserID, idempotencyKey).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find resource fetch idempotency receipt: %w", err)
	}
	return &model, nil
}

func (r *ResourceFetchRepo) FindResourceFetchJob(ctx context.Context, id uint) (*domain.ResourceFetchJob, error) {
	var model ResourceFetchJobModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find resource fetch job: %w", err)
	}
	item := resourceFetchJobModelToDomain(model)
	return &item, nil
}

func (r *ResourceFetchRepo) ClaimDispatchableResourceFetches(
	ctx context.Context,
	limit int,
	runningStaleBefore time.Time,
	queuedDispatchStaleBefore time.Time,
) ([]domain.ResourceFetchJob, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []ResourceFetchJobModel
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockMore := func(query *gorm.DB, description string) error {
			remaining := limit - len(models)
			if remaining <= 0 {
				return nil
			}
			var locked []ResourceFetchJobModel
			if err := query.
				Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Limit(remaining).
				Find(&locked).Error; err != nil {
				return fmt.Errorf("lock %s: %w", description, err)
			}
			models = append(models, locked...)
			return nil
		}

		if err := lockMore(
			tx.Where("status = ? AND updated_at < ?", string(domain.ResourceFetchJobRunning), runningStaleBefore).
				Where("dispatched_at IS NULL").
				Order("updated_at ASC, id ASC"),
			"stale running resource fetch jobs",
		); err != nil {
			return err
		}
		if err := lockMore(
			tx.Where("status = ? AND updated_at < ?", string(domain.ResourceFetchJobRunning), runningStaleBefore).
				Where("dispatched_at < ?", queuedDispatchStaleBefore).
				Order("updated_at ASC, id ASC"),
			"expired stale-running resource fetch dispatches",
		); err != nil {
			return err
		}
		if err := lockMore(
			tx.Where("status = ? AND attempts < max_attempts", string(domain.ResourceFetchJobQueued)).
				Where("dispatched_at IS NULL").
				Order("id ASC"),
			"queued resource fetch jobs",
		); err != nil {
			return err
		}
		if err := lockMore(
			tx.Where("status = ? AND attempts < max_attempts", string(domain.ResourceFetchJobQueued)).
				Where("dispatched_at < ?", queuedDispatchStaleBefore).
				Order("dispatched_at ASC, id ASC"),
			"expired queued resource fetch dispatches",
		); err != nil {
			return err
		}

		for i := range models {
			dispatchToken := platform.NewUUIDV4String()
			result := tx.Model(&ResourceFetchJobModel{}).
				Where("id = ? AND status = ?", models[i].ID, models[i].Status).
				UpdateColumns(map[string]any{
					"dispatch_token": dispatchToken,
					"dispatched_at":  now,
					"updated_at":     gorm.Expr("updated_at"),
				})
			if result.Error != nil {
				return fmt.Errorf("claim resource fetch dispatch: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return domain.ErrResourceFetchJobConflict
			}
			models[i].DispatchToken = dispatchToken
			models[i].DispatchedAt = &now
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim dispatchable resource fetch jobs: %w", err)
	}
	items := make([]domain.ResourceFetchJob, len(models))
	for i := range models {
		items[i] = resourceFetchJobModelToDomain(models[i])
	}
	return items, nil
}

func (r *ResourceFetchRepo) MarkResourceFetchRunning(ctx context.Context, id uint, dispatchToken string) (string, bool, error) {
	dispatchToken = strings.TrimSpace(dispatchToken)
	if id == 0 || dispatchToken == "" {
		return "", false, nil
	}
	now := time.Now().UTC()
	staleBefore := now.Add(-20 * time.Minute)
	claimToken := platform.NewUUIDV4String()
	claimed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job ResourceFetchJobModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock resource fetch job: %w", err)
		}
		if job.DispatchToken != dispatchToken {
			return nil
		}
		isQueued := job.Status == string(domain.ResourceFetchJobQueued)
		isStale := job.Status == string(domain.ResourceFetchJobRunning) && job.UpdatedAt.Before(staleBefore)
		if !isQueued && !isStale {
			return nil
		}

		job.MaxAttempts = normalizeResourceFetchMaxAttempts(job.MaxAttempts)
		nextAttempts := job.Attempts
		if isStale {
			nextAttempts++
		}
		if nextAttempts >= job.MaxAttempts {
			result := tx.Model(&ResourceFetchJobModel{}).
				Where("id = ? AND status = ? AND dispatch_token = ?", id, job.Status, dispatchToken).
				Updates(map[string]any{
					"status":          string(domain.ResourceFetchJobFailed),
					"attempts":        nextAttempts,
					"claim_token":     "",
					"dispatch_token":  "",
					"dispatched_at":   nil,
					"last_safe_error": "Mail fetch retry attempts exhausted.",
					"finished_at":     now,
					"updated_at":      now,
				})
			if result.Error != nil {
				return fmt.Errorf("exhaust stale resource fetch job: %w", result.Error)
			}
			if result.RowsAffected == 1 {
				return r.systemLogs.CreateInTx(ctx, tx, &governancedomain.SystemLog{
					Level:     "warning",
					Module:    "mailmatch",
					EventType: "resource_fetch_retry_exhausted",
					RequestID: job.RequestID,
					BizType:   "microsoft_resource",
					BizID:     fmt.Sprintf("%d", job.ResourceID),
					Message:   "Microsoft resource mail fetch retry attempts exhausted.",
					Detail:    "stale_execution",
				})
			}
			return nil
		}

		result := tx.Model(&ResourceFetchJobModel{}).
			Where("id = ? AND status = ? AND dispatch_token = ?", id, job.Status, dispatchToken).
			Updates(map[string]any{
				"status":          string(domain.ResourceFetchJobRunning),
				"attempts":        nextAttempts,
				"claim_token":     claimToken,
				"dispatch_token":  "",
				"dispatched_at":   nil,
				"last_safe_error": "",
				"started_at":      now,
				"finished_at":     nil,
				"updated_at":      now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark resource fetch running: %w", result.Error)
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	if !claimed {
		claimToken = ""
	}
	return claimToken, claimed, err
}

func (r *ResourceFetchRepo) ReleaseResourceFetchDispatch(ctx context.Context, id uint, dispatchToken string) error {
	dispatchToken = strings.TrimSpace(dispatchToken)
	if id == 0 || dispatchToken == "" {
		return nil
	}
	result := r.db.WithContext(ctx).Model(&ResourceFetchJobModel{}).
		Where("id = ? AND dispatch_token = ? AND status IN ?", id, dispatchToken, activeResourceFetchStatuses()).
		UpdateColumns(map[string]any{
			"dispatch_token": "",
			"dispatched_at":  nil,
			"updated_at":     gorm.Expr("updated_at"),
		})
	if result.Error != nil {
		return fmt.Errorf("release resource fetch dispatch: %w", result.Error)
	}
	return nil
}

func (r *ResourceFetchRepo) MarkResourceFetchDispatchFailed(
	ctx context.Context,
	id uint,
	dispatchToken string,
	safeError string,
	log *governancedomain.SystemLog,
) error {
	dispatchToken = strings.TrimSpace(dispatchToken)
	if id == 0 || dispatchToken == "" {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&ResourceFetchJobModel{}).
			Where("id = ? AND dispatch_token = ? AND status IN ?", id, dispatchToken, activeResourceFetchStatuses()).
			UpdateColumns(map[string]any{
				"dispatch_token":  "",
				"dispatched_at":   nil,
				"last_safe_error": safeDiagnostic(safeError),
				"updated_at":      gorm.Expr("updated_at"),
			})
		if result.Error != nil {
			return fmt.Errorf("mark resource fetch dispatch failed: %w", result.Error)
		}
		if result.RowsAffected == 0 || log == nil {
			return nil
		}
		return r.systemLogs.CreateInTx(ctx, tx, log)
	})
}

func (r *ResourceFetchRepo) LoadResourceFetchScope(ctx context.Context, resourceID uint, expectedCredentialRevision uint64) (*domain.ResourceFetchScope, error) {
	scope, err := r.lockResourceFetchScope(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if err := validateResourceFetchScope(scope, expectedCredentialRevision); err != nil {
		return nil, err
	}
	return scope, nil
}

func (r *ResourceFetchRepo) AssertResourceFetchFence(
	ctx context.Context,
	jobID uint,
	claimToken string,
	resourceID uint,
	expectedCredentialRevision uint64,
) error {
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		scope, err := r.lockResourceFetchScope(txCtx, resourceID)
		if err != nil {
			return err
		}
		if err := validateResourceFetchScope(scope, expectedCredentialRevision); err != nil {
			return err
		}
		return lockRunningResourceFetchJob(tx, jobID, claimToken)
	})
}

func (r *ResourceFetchRepo) CompleteResourceFetch(
	ctx context.Context,
	jobID uint,
	claimToken string,
	resourceID uint,
	expectedCredentialRevision uint64,
	rotatedRefreshToken string,
	fetched int,
	stored int,
	matched int,
	now time.Time,
	log *governancedomain.SystemLog,
) error {
	rotatedRefreshToken = strings.TrimSpace(rotatedRefreshToken)
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		scope, err := r.lockResourceFetchScope(txCtx, resourceID)
		if err != nil {
			return err
		}
		if err := validateResourceFetchScope(scope, expectedCredentialRevision); err != nil {
			return err
		}
		if err := lockRunningResourceFetchJob(tx, jobID, claimToken); err != nil {
			return err
		}
		if err := r.applyResourceFetchRefreshToken(txCtx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID: resourceID, ExpectedCredentialRevision: expectedCredentialRevision,
			RefreshToken: rotatedRefreshToken, Now: now,
		}); err != nil {
			return err
		}
		result := tx.Model(&ResourceFetchJobModel{}).
			Where("id = ? AND status = ? AND claim_token = ?", jobID, string(domain.ResourceFetchJobRunning), strings.TrimSpace(claimToken)).
			Updates(map[string]any{
				"status":          string(domain.ResourceFetchJobSucceeded),
				"fetched_count":   maxInt(fetched, 0),
				"stored_count":    maxInt(stored, 0),
				"matched_count":   maxInt(matched, 0),
				"claim_token":     "",
				"dispatch_token":  "",
				"dispatched_at":   nil,
				"last_safe_error": "",
				"finished_at":     now,
				"updated_at":      now,
			})
		if result.Error != nil {
			return fmt.Errorf("complete resource fetch job: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrResourceFetchInvalidClaim
		}
		if log == nil {
			return nil
		}
		return r.systemLogs.CreateInTx(txCtx, tx, log)
	})
}

func (r *ResourceFetchRepo) MarkResourceFetchCanceled(
	ctx context.Context,
	jobID uint,
	claimToken string,
	safeError string,
	now time.Time,
	log *governancedomain.SystemLog,
) error {
	return r.finishResourceFetchJob(ctx, jobID, claimToken, map[string]any{
		"status":          string(domain.ResourceFetchJobCanceled),
		"last_safe_error": safeDiagnostic(safeError),
		"finished_at":     now,
		"updated_at":      now,
	}, log)
}

func (r *ResourceFetchRepo) MarkResourceFetchFailure(
	ctx context.Context,
	jobID uint,
	claimToken string,
	safeError string,
	retryable bool,
	now time.Time,
	log *governancedomain.SystemLog,
) (bool, error) {
	retryScheduled := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job ResourceFetchJobModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND claim_token = ?", jobID, string(domain.ResourceFetchJobRunning), strings.TrimSpace(claimToken)).
			First(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrResourceFetchInvalidClaim
		}
		if err != nil {
			return fmt.Errorf("lock resource fetch failure: %w", err)
		}
		job.MaxAttempts = normalizeResourceFetchMaxAttempts(job.MaxAttempts)
		nextAttempts := job.Attempts + 1
		retryScheduled = retryable && nextAttempts < job.MaxAttempts
		status := domain.ResourceFetchJobFailed
		updates := map[string]any{
			"status":          string(status),
			"attempts":        nextAttempts,
			"claim_token":     "",
			"dispatch_token":  "",
			"dispatched_at":   nil,
			"last_safe_error": safeDiagnostic(safeError),
			"finished_at":     now,
			"updated_at":      now,
		}
		if retryScheduled {
			updates["status"] = string(domain.ResourceFetchJobQueued)
			updates["finished_at"] = nil
		}
		result := tx.Model(&ResourceFetchJobModel{}).
			Where("id = ? AND status = ? AND claim_token = ?", jobID, string(domain.ResourceFetchJobRunning), strings.TrimSpace(claimToken)).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("mark resource fetch failure: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrResourceFetchInvalidClaim
		}
		if log == nil {
			return nil
		}
		if retryScheduled {
			log.EventType = "resource_fetch_retry_scheduled"
			log.Message = "Microsoft resource mail fetch retry scheduled."
		}
		return r.systemLogs.CreateInTx(ctx, tx, log)
	})
	return retryScheduled, err
}

func (r *ResourceFetchRepo) finishResourceFetchJob(
	ctx context.Context,
	jobID uint,
	claimToken string,
	updates map[string]any,
	log *governancedomain.SystemLog,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates["claim_token"] = ""
		updates["dispatch_token"] = ""
		updates["dispatched_at"] = nil
		result := tx.Model(&ResourceFetchJobModel{}).
			Where("id = ? AND status = ? AND claim_token = ?", jobID, string(domain.ResourceFetchJobRunning), strings.TrimSpace(claimToken)).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("finish resource fetch job: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrResourceFetchInvalidClaim
		}
		if log == nil {
			return nil
		}
		return r.systemLogs.CreateInTx(ctx, tx, log)
	})
}

func (r *ResourceFetchRepo) lockResourceFetchScope(ctx context.Context, resourceID uint) (*domain.ResourceFetchScope, error) {
	if r == nil || r.credentials == nil {
		return nil, fmt.Errorf("load resource fetch credential scope: credential port is unavailable")
	}
	scope, err := r.credentials.LockMicrosoftCredentialScope(ctx, resourceID)
	if err != nil {
		return nil, resourceFetchCredentialError("load resource fetch credential scope", err)
	}
	return &domain.ResourceFetchScope{
		ResourceID: scope.ResourceID, Status: scope.Status, EmailAddress: scope.EmailAddress,
		ClientID: scope.ClientID, RefreshToken: scope.RefreshToken,
		CredentialRevision: scope.CredentialRevision,
	}, nil
}

func (r *ResourceFetchRepo) applyResourceFetchRefreshToken(ctx context.Context, update coreapp.MicrosoftFetchRefreshTokenRotation) error {
	if r == nil || r.credentials == nil {
		return fmt.Errorf("rotate resource fetch refresh token: credential port is unavailable")
	}
	return resourceFetchCredentialError(
		"rotate resource fetch refresh token",
		r.credentials.ApplyMicrosoftFetchRefreshToken(ctx, update),
	)
}

func resourceFetchCredentialError(operation string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound):
		return domain.ErrResourceFetchNotFound
	case errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted):
		return domain.ErrResourceFetchDeleted
	case errors.Is(err, coreapp.ErrMicrosoftCredentialChanged):
		return domain.ErrResourceFetchCredentialChanged
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func validateResourceFetchScope(row *domain.ResourceFetchScope, expectedCredentialRevision uint64) error {
	if row == nil || row.ResourceID == 0 {
		return domain.ErrResourceFetchNotFound
	}
	if strings.EqualFold(strings.TrimSpace(row.Status), "deleted") {
		return domain.ErrResourceFetchDeleted
	}
	if expectedCredentialRevision > 0 && row.CredentialRevision != expectedCredentialRevision {
		return domain.ErrResourceFetchCredentialChanged
	}
	if strings.TrimSpace(row.EmailAddress) == "" || strings.TrimSpace(row.ClientID) == "" || strings.TrimSpace(row.RefreshToken) == "" {
		return domain.ErrResourceFetchCredentialsMissing
	}
	return nil
}

func lockRunningResourceFetchJob(tx *gorm.DB, jobID uint, claimToken string) error {
	var job ResourceFetchJobModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = ? AND claim_token = ?", jobID, string(domain.ResourceFetchJobRunning), strings.TrimSpace(claimToken)).
		First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrResourceFetchInvalidClaim
	}
	if err != nil {
		return fmt.Errorf("lock running resource fetch job: %w", err)
	}
	return nil
}

func activeResourceFetchStatuses() []string {
	return []string{string(domain.ResourceFetchJobQueued), string(domain.ResourceFetchJobRunning)}
}

func normalizeResourceFetchMaxAttempts(value int) int {
	if value <= 0 {
		return domain.ResourceFetchDefaultMaxAttempts
	}
	return value
}

func resourceFetchJobModelToDomain(model ResourceFetchJobModel) domain.ResourceFetchJob {
	return domain.ResourceFetchJob{
		ID:                         model.ID,
		ResourceID:                 model.ResourceID,
		OperatorUserID:             model.OperatorUserID,
		ExpectedCredentialRevision: model.ExpectedCredentialRevision,
		Recipient:                  model.Recipient,
		Status:                     domain.ResourceFetchJobStatus(model.Status),
		Attempts:                   model.Attempts,
		MaxAttempts:                model.MaxAttempts,
		FetchedCount:               model.FetchedCount,
		StoredCount:                model.StoredCount,
		MatchedCount:               model.MatchedCount,
		SinceAt:                    model.SinceAt,
		UntilAt:                    model.UntilAt,
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

func resourceFetchJobModelFromDomain(item domain.ResourceFetchJob) ResourceFetchJobModel {
	return ResourceFetchJobModel{
		ID:                         item.ID,
		ResourceID:                 item.ResourceID,
		OperatorUserID:             item.OperatorUserID,
		ExpectedCredentialRevision: item.ExpectedCredentialRevision,
		Recipient:                  strings.ToLower(strings.TrimSpace(item.Recipient)),
		Status:                     string(item.Status),
		Attempts:                   item.Attempts,
		MaxAttempts:                normalizeResourceFetchMaxAttempts(item.MaxAttempts),
		FetchedCount:               item.FetchedCount,
		StoredCount:                item.StoredCount,
		MatchedCount:               item.MatchedCount,
		SinceAt:                    item.SinceAt,
		UntilAt:                    item.UntilAt,
		ClaimToken:                 strings.TrimSpace(item.ClaimToken),
		DispatchToken:              strings.TrimSpace(item.DispatchToken),
		LastSafeError:              safeDiagnostic(item.LastSafeError),
		RequestID:                  truncate(item.RequestID, 64),
		Path:                       truncate(item.Path, 255),
		DispatchedAt:               item.DispatchedAt,
		StartedAt:                  item.StartedAt,
		FinishedAt:                 item.FinishedAt,
	}
}

func maxInt(value int, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}
