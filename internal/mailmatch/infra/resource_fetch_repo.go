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
	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ResourceFetchRepo struct {
	db            *gorm.DB
	credentials   coreapp.MicrosoftCredentialPort
	operationLogs *governanceinfra.OperationLogRepo
	systemLogs    *governanceinfra.SystemLogRepo
}

func NewResourceFetchRepo(db *gorm.DB) *ResourceFetchRepo {
	return &ResourceFetchRepo{
		db: db, operationLogs: governanceinfra.NewOperationLogRepo(db), systemLogs: governanceinfra.NewSystemLogRepo(db),
	}
}

func (r *ResourceFetchRepo) SetMicrosoftCredentialPort(credentials coreapp.MicrosoftCredentialPort) {
	if r != nil {
		r.credentials = credentials
	}
}

func (r *ResourceFetchRepo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *ResourceFetchRepo) withTx(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		tx = tx.WithContext(ctx)
		return fn(platform.WithGormTx(ctx, tx), tx)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx), tx)
	})
}

func (r *ResourceFetchRepo) CreateOrReuseResourceFetch(ctx context.Context, job *domain.ResourceFetchJob, log *governancedomain.OperationLog) (bool, error) {
	if r == nil || r.db == nil || job == nil || job.ResourceID == 0 || job.OperatorUserID == 0 {
		return false, domain.ErrInvalidRequest
	}
	job.IdempotencyKey = strings.TrimSpace(job.IdempotencyKey)
	if job.IdempotencyKey == "" || len(job.IdempotencyKey) > 128 {
		return false, domain.ErrInvalidRequest
	}
	if job.Kind == "" {
		job.Kind = domain.ResourceFetchJobFetch
	}
	if !domain.IsValidResourceFetchJobKind(job.Kind) {
		return false, domain.ErrInvalidRequest
	}
	reused := false
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		scope, err := r.lockResourceFetchScope(txCtx, job.ResourceID)
		if err != nil {
			return err
		}
		if err := validateResourceFetchScope(scope, 0); err != nil {
			return err
		}

		var state FetchStateModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "email_resource_id = ?", job.ResourceID).Error
		operationKind := resourceFetchOperationKind(job.Kind)
		if err == nil && state.IdempotencyKey == job.IdempotencyKey {
			if state.OperationKind != operationKind || state.OperatorUserID == nil || *state.OperatorUserID != job.OperatorUserID {
				return domain.ErrResourceFetchIdempotencyConflict
			}
			*job = resourceFetchStateToDomain(state)
			reused = true
			return r.createResourceFetchOperationLog(txCtx, tx, job, true, log)
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lock resource fetch state: %w", err)
		}

		now := time.Now().UTC()
		generation := uint64(1)
		createdAt := now
		if err == nil {
			generation = state.Generation + 1
			createdAt = state.CreatedAt
		}
		requested := FetchStateModel{
			EmailResourceID: job.ResourceID, Status: string(domain.ResourceFetchJobQueued), Generation: generation,
			Failures: 0, OperationKind: operationKind, OperatorUserID: &job.OperatorUserID,
			ExpectedCredentialRevision: scope.CredentialRevision,
			SinceAt:                    job.SinceAt, UntilAt: job.UntilAt,
			RequestID: strings.TrimSpace(job.RequestID), Path: strings.TrimSpace(job.Path), IdempotencyKey: job.IdempotencyKey,
			RequestedAt: &now, CreatedAt: createdAt, UpdatedAt: now,
		}
		if err == nil {
			result := tx.Model(&FetchStateModel{}).Where("email_resource_id = ?", job.ResourceID).Updates(map[string]any{
				"status": string(domain.ResourceFetchJobQueued), "generation": generation, "failures": 0,
				"operation_kind": operationKind, "order_no": "", "purpose": "order_fetch",
				"operator_user_id": job.OperatorUserID, "expected_credential_revision": scope.CredentialRevision,
				"since_at": job.SinceAt, "until_at": job.UntilAt,
				"fetched_count": 0, "stored_count": 0, "matched_count": 0,
				"request_id": strings.TrimSpace(job.RequestID), "path": strings.TrimSpace(job.Path), "idempotency_key": job.IdempotencyKey,
				"requested_at": now, "started_at": nil, "finished_at": nil, "last_safe_error": "",
			})
			if result.Error != nil {
				return fmt.Errorf("replace resource fetch state: %w", result.Error)
			}
		} else if err := tx.Create(&requested).Error; err != nil {
			return fmt.Errorf("create resource fetch state: %w", err)
		}
		*job = resourceFetchStateToDomain(requested)
		job.Recipient = strings.ToLower(strings.TrimSpace(scope.EmailAddress))
		return r.createResourceFetchOperationLog(txCtx, tx, job, false, log)
	})
	return reused, err
}

func (r *ResourceFetchRepo) createResourceFetchOperationLog(ctx context.Context, tx *gorm.DB, job *domain.ResourceFetchJob, reused bool, log *governancedomain.OperationLog) error {
	if log == nil {
		return nil
	}
	log.SafeSummary = fmt.Sprintf(
		"Microsoft resource %s accepted; task=fetch:%d:%d; reused=%t.",
		resourceFetchOperationLabel(job.Kind), job.ResourceID, job.Generation, reused,
	)
	return r.operationLogs.CreateInTx(ctx, tx, log)
}

func (r *ResourceFetchRepo) FindResourceFetch(ctx context.Context, resourceID uint, generation uint64) (*domain.ResourceFetchJob, error) {
	var state FetchStateModel
	err := r.dbFor(ctx).First(&state, "email_resource_id = ? AND generation = ? AND operation_kind IN ?", resourceID, generation, resourceFetchOperationKinds()).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find resource fetch: %w", err)
	}
	job := resourceFetchStateToDomain(state)
	return &job, nil
}

func (r *ResourceFetchRepo) ListPendingResourceFetches(ctx context.Context, limit int) ([]domain.ResourceFetchJob, error) {
	if limit <= 0 {
		limit = 100
	}
	var states []FetchStateModel
	err := r.dbFor(ctx).
		Where("status = ? AND operation_kind IN ?", string(domain.ResourceFetchJobQueued), resourceFetchOperationKinds()).
		Order("requested_at ASC, email_resource_id ASC").Limit(limit).Find(&states).Error
	if err != nil {
		return nil, fmt.Errorf("list pending resource fetches: %w", err)
	}
	jobs := make([]domain.ResourceFetchJob, len(states))
	for i := range states {
		jobs[i] = resourceFetchStateToDomain(states[i])
	}
	return jobs, nil
}

func (r *ResourceFetchRepo) MarkResourceFetchProcessing(ctx context.Context, resourceID uint, generation uint64) (bool, error) {
	now := time.Now().UTC()
	result := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ? AND operation_kind IN ?", resourceID, generation, string(domain.ResourceFetchJobQueued), resourceFetchOperationKinds()).
		Updates(map[string]any{"status": string(domain.ResourceFetchJobRunning), "started_at": now, "finished_at": nil, "last_safe_error": ""})
	if result.Error != nil {
		return false, fmt.Errorf("mark resource fetch processing: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	var count int64
	err := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ? AND operation_kind IN ?", resourceID, generation, string(domain.ResourceFetchJobRunning), resourceFetchOperationKinds()).
		Count(&count).Error
	return count == 1, err
}

func (r *ResourceFetchRepo) ReleaseResourceFetchInfrastructureFailure(ctx context.Context, resourceID uint, generation uint64, safeError string, log *governancedomain.SystemLog) (bool, error) {
	updated := false
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		result := tx.Model(&FetchStateModel{}).
			Where("email_resource_id = ? AND generation = ? AND status = ? AND operation_kind IN ?", resourceID, generation, string(domain.ResourceFetchJobRunning), resourceFetchOperationKinds()).
			Updates(map[string]any{
				"status": string(domain.ResourceFetchJobQueued), "generation": gorm.Expr("generation + 1"),
				"started_at": nil, "last_safe_error": safeDiagnostic(safeError),
			})
		if result.Error != nil {
			return result.Error
		}
		updated = result.RowsAffected == 1
		if !updated || log == nil {
			return nil
		}
		return r.systemLogs.CreateInTx(txCtx, tx, log)
	})
	return updated, err
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

func (r *ResourceFetchRepo) AssertResourceFetchFence(ctx context.Context, resourceID uint, generation uint64, expectedCredentialRevision uint64) error {
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		if err := r.lockResourceFetchState(tx, resourceID, generation); err != nil {
			return err
		}
		scope, err := r.lockResourceFetchScope(txCtx, resourceID)
		if err != nil {
			return err
		}
		return validateResourceFetchScope(scope, expectedCredentialRevision)
	})
}

func (r *ResourceFetchRepo) CompleteResourceFetch(ctx context.Context, resourceID uint, generation uint64, expectedCredentialRevision uint64, rotatedRefreshToken string, fetched int, stored int, matched int, now time.Time, log *governancedomain.SystemLog) error {
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		if err := r.lockResourceFetchState(tx, resourceID, generation); err != nil {
			return err
		}
		scope, err := r.lockResourceFetchScope(txCtx, resourceID)
		if err != nil {
			return err
		}
		if err := validateResourceFetchScope(scope, expectedCredentialRevision); err != nil {
			return err
		}
		if err := r.applyResourceFetchRefreshToken(txCtx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID: resourceID, ExpectedCredentialRevision: expectedCredentialRevision,
			RefreshToken: strings.TrimSpace(rotatedRefreshToken), Now: now,
		}); err != nil {
			return err
		}
		return r.finishResourceFetchState(txCtx, tx, resourceID, generation, map[string]any{
			"status": string(domain.ResourceFetchJobSucceeded), "failures": 0,
			"fetched_count": max(fetched, 0), "stored_count": max(stored, 0), "matched_count": max(matched, 0),
			"last_safe_error": "", "finished_at": now,
		}, log)
	})
}

func (r *ResourceFetchRepo) CompleteResourceFetchTask(ctx context.Context, resourceID uint, generation uint64, now time.Time, log *governancedomain.SystemLog) error {
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		return r.finishResourceFetchState(txCtx, tx, resourceID, generation, map[string]any{
			"status": string(domain.ResourceFetchJobSucceeded), "failures": 0,
			"fetched_count": 0, "stored_count": 0, "matched_count": 0,
			"last_safe_error": "", "finished_at": now,
		}, log)
	})
}

func (r *ResourceFetchRepo) MarkResourceFetchCanceled(ctx context.Context, resourceID uint, generation uint64, safeError string, now time.Time, log *governancedomain.SystemLog) error {
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		return r.finishResourceFetchState(txCtx, tx, resourceID, generation, map[string]any{
			"status": string(domain.ResourceFetchJobCanceled), "last_safe_error": safeDiagnostic(safeError), "finished_at": now,
		}, log)
	})
}

func (r *ResourceFetchRepo) MarkResourceFetchFailure(ctx context.Context, resourceID uint, generation uint64, safeError string, retryable bool, now time.Time, log *governancedomain.SystemLog) (bool, error) {
	retryScheduled := false
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		var state FetchStateModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("email_resource_id = ? AND generation = ? AND status = ? AND operation_kind IN ?", resourceID, generation, string(domain.ResourceFetchJobRunning), resourceFetchOperationKinds()).
			First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrResourceFetchInvalidClaim
		}
		if err != nil {
			return err
		}
		failures := state.Failures + 1
		retryScheduled = retryable && failures < domain.ResourceFetchDefaultMaxAttempts
		status := string(domain.ResourceFetchJobFailed)
		updates := map[string]any{
			"status": status, "failures": min(failures, domain.ResourceFetchDefaultMaxAttempts),
			"last_safe_error": safeDiagnostic(safeError), "started_at": nil, "finished_at": now,
		}
		if retryScheduled {
			updates["status"] = string(domain.ResourceFetchJobQueued)
			updates["finished_at"] = nil
		}
		result := tx.Model(&FetchStateModel{}).
			Where("email_resource_id = ? AND generation = ? AND status = ?", resourceID, generation, string(domain.ResourceFetchJobRunning)).
			Updates(updates)
		if result.Error != nil {
			return result.Error
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
		return r.systemLogs.CreateInTx(txCtx, tx, log)
	})
	return retryScheduled, err
}

func (r *ResourceFetchRepo) finishResourceFetchState(ctx context.Context, tx *gorm.DB, resourceID uint, generation uint64, updates map[string]any, log *governancedomain.SystemLog) error {
	result := tx.Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ? AND operation_kind IN ?", resourceID, generation, string(domain.ResourceFetchJobRunning), resourceFetchOperationKinds()).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domain.ErrResourceFetchInvalidClaim
	}
	if log == nil {
		return nil
	}
	return r.systemLogs.CreateInTx(ctx, tx, log)
}

func (r *ResourceFetchRepo) lockResourceFetchState(tx *gorm.DB, resourceID uint, generation uint64) error {
	var state FetchStateModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("email_resource_id = ? AND generation = ? AND status = ? AND operation_kind IN ?", resourceID, generation, string(domain.ResourceFetchJobRunning), resourceFetchOperationKinds()).
		First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrResourceFetchInvalidClaim
	}
	return err
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
		ClientID: scope.ClientID, RefreshToken: scope.RefreshToken, CredentialRevision: scope.CredentialRevision,
	}, nil
}

func (r *ResourceFetchRepo) applyResourceFetchRefreshToken(ctx context.Context, update coreapp.MicrosoftFetchRefreshTokenRotation) error {
	if r == nil || r.credentials == nil {
		return fmt.Errorf("rotate resource fetch refresh token: credential port is unavailable")
	}
	return resourceFetchCredentialError("rotate resource fetch refresh token", r.credentials.ApplyMicrosoftFetchRefreshToken(ctx, update))
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

func resourceFetchStateToDomain(state FetchStateModel) domain.ResourceFetchJob {
	kind := domain.ResourceFetchJobFetch
	if state.OperationKind == "resource_history" {
		kind = domain.ResourceFetchJobHistory
	}
	operatorID := uint(0)
	if state.OperatorUserID != nil {
		operatorID = *state.OperatorUserID
	}
	createdAt := state.CreatedAt
	if state.RequestedAt != nil {
		createdAt = *state.RequestedAt
	}
	return domain.ResourceFetchJob{
		ID: state.EmailResourceID, Generation: state.Generation, Kind: kind,
		ResourceID: state.EmailResourceID, OperatorUserID: operatorID,
		ExpectedCredentialRevision: state.ExpectedCredentialRevision,
		Status:                     domain.ResourceFetchJobStatus(state.Status), Attempts: state.Failures,
		MaxAttempts:  domain.ResourceFetchDefaultMaxAttempts,
		FetchedCount: state.FetchedCount, StoredCount: state.StoredCount, MatchedCount: state.MatchedCount,
		SinceAt: state.SinceAt, UntilAt: state.UntilAt,
		LastSafeError: state.LastSafeError, RequestID: state.RequestID, Path: state.Path,
		IdempotencyKey: state.IdempotencyKey, StartedAt: state.StartedAt, FinishedAt: state.FinishedAt,
		CreatedAt: createdAt, UpdatedAt: state.UpdatedAt,
	}
}

func resourceFetchOperationKind(kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "resource_history"
	}
	return "resource_fetch"
}

func resourceFetchOperationKinds() []string { return []string{"resource_fetch", "resource_history"} }

func resourceFetchOperationLabel(kind domain.ResourceFetchJobKind) string {
	if kind == domain.ResourceFetchJobHistory {
		return "project history scan"
	}
	return "mail fetch"
}

var _ app.ResourceFetchRepository = (*ResourceFetchRepo)(nil)
