package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ResourceValidationModel struct {
	ID                         uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID                 uint       `gorm:"not null;column:resource_id"`
	ResourceType               string     `gorm:"type:varchar(32);not null;column:resource_type"`
	OwnerUserID                uint       `gorm:"not null;column:owner_user_id"`
	ExpectedCredentialRevision uint64     `gorm:"not null;default:0;column:expected_credential_revision"`
	Status                     string     `gorm:"type:varchar(32);not null;default:'queued'"`
	Attempts                   int        `gorm:"not null;default:0"`
	MaxAttempts                int        `gorm:"not null;default:3;column:max_attempts"`
	ClaimToken                 string     `gorm:"type:char(36);not null;default:'';column:claim_token"`
	DispatchToken              string     `gorm:"type:char(36);not null;default:'';column:dispatch_token"`
	DispatchedAt               *time.Time `gorm:"column:dispatched_at"`
	LastSafeError              string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID                  string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path                       string     `gorm:"type:varchar(255);not null;default:''"`
	StartedAt                  *time.Time `gorm:"column:started_at"`
	FinishedAt                 *time.Time `gorm:"column:finished_at"`
	CreatedAt                  time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt                  time.Time  `gorm:"not null;autoUpdateTime"`
}

type ResourceValidationBatchModel struct {
	ID            uint       `gorm:"primaryKey;autoIncrement"`
	OwnerUserID   uint       `gorm:"not null;column:owner_user_id"`
	SelectionJSON []byte     `gorm:"type:json;not null;column:selection_json"`
	AfterID       uint       `gorm:"not null;default:0;column:after_id"`
	ThroughID     uint       `gorm:"not null;default:0;column:through_id"`
	Status        string     `gorm:"type:varchar(32);not null;default:'pending'"`
	Requested     int        `gorm:"not null;default:0"`
	Queued        int        `gorm:"not null;default:0"`
	Created       int        `gorm:"not null;default:0"`
	RequestID     string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path          string     `gorm:"type:varchar(255);not null;default:''"`
	LastSafeError string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	FinishedAt    *time.Time `gorm:"column:finished_at"`
	CreatedAt     time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"not null;autoUpdateTime"`
}

func (ResourceValidationBatchModel) TableName() string {
	return "resource_validation_batches"
}

func (ResourceValidationModel) TableName() string {
	return "resource_validation_jobs"
}

func validationModel(job *domain.ResourceValidation) *ResourceValidationModel {
	return &ResourceValidationModel{
		ID:                         job.ID,
		ResourceID:                 job.ResourceID,
		ResourceType:               string(job.ResourceType),
		OwnerUserID:                job.OwnerUserID,
		ExpectedCredentialRevision: job.ExpectedCredentialRevision,
		Status:                     string(job.Status),
		Attempts:                   job.Attempts,
		MaxAttempts:                normalizeValidationMaxAttempts(job.MaxAttempts),
		ClaimToken:                 job.ClaimToken,
		DispatchToken:              job.DispatchToken,
		DispatchedAt:               job.DispatchedAt,
		LastSafeError:              job.LastSafeError,
		RequestID:                  job.RequestID,
		Path:                       job.Path,
		StartedAt:                  job.StartedAt,
		FinishedAt:                 job.FinishedAt,
		CreatedAt:                  job.CreatedAt,
		UpdatedAt:                  job.UpdatedAt,
	}
}

func (m *ResourceValidationModel) toDomain() *domain.ResourceValidation {
	return &domain.ResourceValidation{
		ID:                         m.ID,
		ResourceID:                 m.ResourceID,
		ResourceType:               domain.ResourceType(m.ResourceType),
		OwnerUserID:                m.OwnerUserID,
		ExpectedCredentialRevision: m.ExpectedCredentialRevision,
		Status:                     domain.ResourceValidationStatus(m.Status),
		Attempts:                   m.Attempts,
		MaxAttempts:                normalizeValidationMaxAttempts(m.MaxAttempts),
		ClaimToken:                 m.ClaimToken,
		DispatchToken:              m.DispatchToken,
		DispatchedAt:               m.DispatchedAt,
		LastSafeError:              m.LastSafeError,
		RequestID:                  m.RequestID,
		Path:                       m.Path,
		StartedAt:                  m.StartedAt,
		FinishedAt:                 m.FinishedAt,
		CreatedAt:                  m.CreatedAt,
		UpdatedAt:                  m.UpdatedAt,
	}
}

type ResourceValidationRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
}

const (
	resourceValidationBatchInsertSize = 1000
	resourceValidationMaxPageSize     = 1000
)

func NewResourceValidationRepo(db *gorm.DB) *ResourceValidationRepo {
	return &ResourceValidationRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
}

func (r *ResourceValidationRepo) CreateWithLog(ctx context.Context, job *domain.ResourceValidation, log *governancedomain.OperationLog) (bool, error) {
	created := false
	create := func(tx *gorm.DB) error {
		if err := prepareResourceForValidationTx(tx, job); err != nil {
			return err
		}
		existing, err := findActiveValidationJobTx(tx, job.ResourceID)
		if err != nil {
			return err
		}
		if existing != nil {
			if job.ResourceType != domain.ResourceTypeMicrosoft ||
				existing.ExpectedCredentialRevision == job.ExpectedCredentialRevision {
				*job = *existing
				return nil
			}
			if err := supersedeValidationJobTx(tx, existing.ID, "Resource credentials changed; validation was superseded."); err != nil {
				return err
			}
		}
		model := validationModel(job)
		if err := tx.Create(model).Error; err != nil {
			if isDuplicateKeyError(err) {
				existing, findErr := findActiveValidationJobTx(tx, job.ResourceID)
				if findErr != nil {
					return findErr
				}
				if existing != nil {
					*job = *existing
					return nil
				}
			}
			return fmt.Errorf("create resource validation job: %w", err)
		}
		job.ID = model.ID
		job.CreatedAt = model.CreatedAt
		job.UpdatedAt = model.UpdatedAt
		created = true
		if log != nil {
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return fmt.Errorf("create operation log: %w", err)
			}
		}
		return nil
	}
	var err error
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		err = create(tx.WithContext(ctx))
	} else {
		err = r.db.WithContext(ctx).Transaction(create)
	}
	return created, err
}

func (r *ResourceValidationRepo) CreateBatchWithLog(ctx context.Context, ownerUserID uint, selection coreapp.ResourceBulkSelection, log *governancedomain.OperationLog, requestID, path string) (*coreapp.ResourceBatchValidationResult, error) {
	switch selection.Mode {
	case coreapp.ResourceBulkSelectionIDs, coreapp.ResourceBulkSelectionFilter:
		return r.CreateDeferredBatchWithLog(ctx, ownerUserID, selection, log, requestID, path)
	default:
		return nil, domain.ErrInvalidResourceType
	}
}

func (r *ResourceValidationRepo) CreateDeferredBatchWithLog(ctx context.Context, ownerUserID uint, selection coreapp.ResourceBulkSelection, log *governancedomain.OperationLog, requestID, path string) (*coreapp.ResourceBatchValidationResult, error) {
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		return nil, fmt.Errorf("marshal resource validation selection: %w", err)
	}
	batch := &ResourceValidationBatchModel{
		OwnerUserID:   ownerUserID,
		SelectionJSON: selectionJSON,
		Status:        "pending",
		RequestID:     strings.TrimSpace(requestID),
		Path:          strings.TrimSpace(path),
	}
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if selection.Mode == coreapp.ResourceBulkSelectionFilter {
			if err := tx.Table("email_resources").
				Select("COALESCE(MAX(id), 0)").
				Where("owner_user_id = ? AND type = ?", ownerUserID, string(selection.Filter.ResourceType)).
				Row().Scan(&batch.ThroughID); err != nil {
				return fmt.Errorf("capture resource validation batch high-water mark: %w", err)
			}
		}
		if err := tx.Create(batch).Error; err != nil {
			return fmt.Errorf("create resource validation batch: %w", err)
		}
		if log != nil {
			log.SafeSummary = "Resource validation batch accepted for durable expansion."
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return fmt.Errorf("create operation log: %w", err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result := &coreapp.ResourceBatchValidationResult{}
	if selection.Mode == coreapp.ResourceBulkSelectionIDs {
		// The IDs are durably accepted here but are intentionally resolved by the
		// dispatcher so the HTTP request performs no resource expansion work.
		result.Requested = len(selection.ResourceIDs)
		result.Queued = len(selection.ResourceIDs)
	}
	return result, nil
}

func (r *ResourceValidationRepo) CreateImportedValidationJobs(ctx context.Context, ownerUserID uint, resourceIDs []uint, requestID string, path string) error {
	if len(resourceIDs) == 0 {
		return nil
	}
	create := func(tx *gorm.DB) error {
		candidates, err := selectValidationCandidatesByIDs(ctx, tx, ownerUserID, resourceIDs)
		if err != nil {
			return err
		}
		return createValidationCandidateJobsTx(
			ctx,
			tx,
			candidates,
			requestID,
			path,
			&coreapp.ResourceBatchValidationResult{},
		)
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return create(tx)
	}
	return r.db.WithContext(ctx).Transaction(create)
}

func createValidationCandidateJobsTx(ctx context.Context, tx *gorm.DB, candidates []validationCandidateRow, requestID, path string, result *coreapp.ResourceBatchValidationResult) error {
	if len(candidates) == 0 {
		return nil
	}
	result.Requested += len(candidates)
	result.Queued += len(candidates)
	if err := resetAbnormalMicrosoftCandidatesTx(ctx, tx, candidates); err != nil {
		return err
	}

	now := time.Now().UTC()
	models := make([]ResourceValidationModel, len(candidates))
	for i, candidate := range candidates {
		models[i] = ResourceValidationModel{
			ResourceID:                 candidate.ID,
			ResourceType:               candidate.ResourceType,
			OwnerUserID:                candidate.OwnerUserID,
			ExpectedCredentialRevision: candidate.CredentialRevision,
			Status:                     string(domain.ResourceValidationQueued),
			MaxAttempts:                domain.ResourceValidationDefaultMaxAttempts,
			RequestID:                  strings.TrimSpace(requestID),
			Path:                       strings.TrimSpace(path),
			CreatedAt:                  now,
			UpdatedAt:                  now,
		}
	}
	for start := 0; start < len(models); start += resourceValidationBatchInsertSize {
		end := start + resourceValidationBatchInsertSize
		if end > len(models) {
			end = len(models)
		}
		inserted := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(models[start:end], resourceValidationBatchInsertSize)
		if inserted.Error != nil {
			return fmt.Errorf("create resource validation batch: %w", inserted.Error)
		}
		result.Created += int(inserted.RowsAffected)
	}
	return nil
}

func (r *ResourceValidationRepo) ResumeValidationBatches(ctx context.Context, candidateLimit int) (int, error) {
	if candidateLimit <= 0 {
		return 0, nil
	}
	processed := 0
	// Empty or already-complete batches do not consume candidate budget. Bound
	// the number inspected so corrupted/stale rows cannot monopolize a dispatch.
	maxBatchScans := candidateLimit + 16
	for scans := 0; processed < candidateLimit && scans < maxBatchScans; scans++ {
		var batch ResourceValidationBatchModel
		err := r.db.WithContext(ctx).
			Where("status = ?", "pending").
			Order("updated_at ASC, id ASC").
			First(&batch).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return processed, nil
		}
		if err != nil {
			return processed, fmt.Errorf("find pending resource validation batch: %w", err)
		}
		pageLimit := candidateLimit - processed
		if pageLimit > resourceValidationMaxPageSize {
			pageLimit = resourceValidationMaxPageSize
		}
		_, pageProcessed, err := r.expandValidationBatchPage(ctx, batch.ID, pageLimit)
		if err != nil {
			return processed, err
		}
		processed += pageProcessed
	}
	return processed, nil
}

func (r *ResourceValidationRepo) expandValidationBatchPage(ctx context.Context, batchID uint, limit int) (bool, int, error) {
	if limit <= 0 {
		return false, 0, nil
	}
	done := false
	processed := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch ResourceValidationBatchModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ?", batchID, "pending").
			First(&batch).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			done = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock resource validation batch: %w", err)
		}
		var selection coreapp.ResourceBulkSelection
		if err := json.Unmarshal(batch.SelectionJSON, &selection); err != nil {
			now := time.Now().UTC()
			done = true
			return tx.Model(&ResourceValidationBatchModel{}).
				Where("id = ? AND status = ?", batch.ID, "pending").
				Updates(map[string]any{
					"status":          "failed",
					"selection_json":  gorm.Expr("JSON_OBJECT()"),
					"last_safe_error": "Resource validation batch filter is invalid.",
					"finished_at":     now,
					"updated_at":      now,
				}).Error
		}
		var candidates []validationCandidateRow
		nextAfterID := batch.AfterID
		pageDone := false
		switch selection.Mode {
		case coreapp.ResourceBulkSelectionFilter:
			candidates, err = selectValidationCandidatesByFilter(
				ctx,
				tx,
				batch.OwnerUserID,
				selection.Filter,
				batch.AfterID,
				batch.ThroughID,
				limit+1,
			)
			pageDone = len(candidates) <= limit
			if len(candidates) > limit {
				candidates = candidates[:limit]
			}
			processed = len(candidates)
			if len(candidates) == 0 {
				pageDone = true
			} else {
				nextAfterID = candidates[len(candidates)-1].ID
			}
		case coreapp.ResourceBulkSelectionIDs:
			pageIDs := validationBatchIDPage(selection.ResourceIDs, batch.AfterID, limit+1)
			pageDone = len(pageIDs) <= limit
			if len(pageIDs) > limit {
				pageIDs = pageIDs[:limit]
			}
			processed = len(pageIDs)
			if len(pageIDs) > 0 {
				candidates, err = selectAvailableValidationCandidatesByIDs(ctx, tx, batch.OwnerUserID, pageIDs)
				nextAfterID = pageIDs[len(pageIDs)-1]
			}
		default:
			now := time.Now().UTC()
			done = true
			return tx.Model(&ResourceValidationBatchModel{}).
				Where("id = ? AND status = ?", batch.ID, "pending").
				Updates(map[string]any{
					"status":          "failed",
					"selection_json":  gorm.Expr("JSON_OBJECT()"),
					"last_safe_error": "Resource validation batch selection is invalid.",
					"finished_at":     now,
					"updated_at":      now,
				}).Error
		}
		if err != nil {
			return err
		}
		if len(candidates) == 0 && pageDone {
			now := time.Now().UTC()
			done = true
			return tx.Model(&ResourceValidationBatchModel{}).
				Where("id = ? AND status = ?", batch.ID, "pending").
				Updates(map[string]any{
					"status":         "succeeded",
					"selection_json": gorm.Expr("JSON_OBJECT()"),
					"finished_at":    now,
					"updated_at":     now,
				}).Error
		}

		pageResult := &coreapp.ResourceBatchValidationResult{}
		if len(candidates) > 0 {
			if err := createValidationCandidateJobsTx(ctx, tx, candidates, batch.RequestID, batch.Path, pageResult); err != nil {
				return err
			}
		}
		if nextAfterID == batch.AfterID {
			return fmt.Errorf("resource validation batch made no progress")
		}
		done = pageDone
		updates := map[string]any{
			"after_id":        nextAfterID,
			"requested":       gorm.Expr("requested + ?", pageResult.Requested),
			"queued":          gorm.Expr("queued + ?", pageResult.Queued),
			"created":         gorm.Expr("created + ?", pageResult.Created),
			"last_safe_error": "",
			"updated_at":      time.Now().UTC(),
		}
		if done {
			now := time.Now().UTC()
			updates["status"] = "succeeded"
			updates["selection_json"] = gorm.Expr("JSON_OBJECT()")
			updates["finished_at"] = now
			updates["updated_at"] = now
		}
		return tx.Model(&ResourceValidationBatchModel{}).
			Where("id = ? AND status = ?", batch.ID, "pending").
			Updates(updates).Error
	})
	return done, processed, err
}

func validationBatchIDPage(values []uint, afterID uint, limit int) []uint {
	ids := append([]uint(nil), values...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	capacity := len(ids)
	if limit > 0 && capacity > limit {
		capacity = limit
	}
	result := make([]uint, 0, capacity)
	var previous uint
	for _, id := range ids {
		if id == 0 || id == previous || id <= afterID {
			continue
		}
		previous = id
		result = append(result, id)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

func (r *ResourceValidationRepo) FindByID(ctx context.Context, id uint) (*domain.ResourceValidation, error) {
	var model ResourceValidationModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find resource validation job: %w", err)
	}
	return model.toDomain(), nil
}

func (r *ResourceValidationRepo) ClaimDispatchable(ctx context.Context, limit int, runningStaleBefore time.Time, queuedDispatchStaleBefore time.Time) ([]domain.ResourceValidation, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []ResourceValidationModel
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockMore := func(query *gorm.DB, description string) error {
			remaining := limit - len(models)
			if remaining <= 0 {
				return nil
			}
			var locked []ResourceValidationModel
			if err := query.
				Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Limit(remaining).
				Find(&locked).Error; err != nil {
				return fmt.Errorf("lock %s: %w", description, err)
			}
			models = append(models, locked...)
			return nil
		}

		// Recover timed-out executions before admitting new jobs so a continuous
		// queued backlog cannot starve durable running work.
		if err := lockMore(
			tx.Where("status = ? AND updated_at < ?", string(domain.ResourceValidationRunning), runningStaleBefore).
				Where("dispatched_at IS NULL").
				Order("updated_at ASC, id ASC"),
			"stale running resource validation jobs",
		); err != nil {
			return err
		}
		if err := lockMore(
			tx.Where("status = ? AND updated_at < ?", string(domain.ResourceValidationRunning), runningStaleBefore).
				Where("dispatched_at < ?", queuedDispatchStaleBefore).
				Order("updated_at ASC, id ASC"),
			"expired stale-running validation dispatches",
		); err != nil {
			return err
		}
		if err := lockMore(
			tx.Where("status = ? AND attempts < max_attempts", string(domain.ResourceValidationQueued)).
				Where("dispatched_at IS NULL").
				Order("id ASC"),
			"queued resource validation jobs",
		); err != nil {
			return err
		}
		if err := lockMore(
			tx.Where("status = ? AND attempts < max_attempts", string(domain.ResourceValidationQueued)).
				Where("dispatched_at < ?", queuedDispatchStaleBefore).
				Order("dispatched_at ASC, id ASC"),
			"expired queued resource validation dispatches",
		); err != nil {
			return err
		}

		for i := range models {
			dispatchToken := platform.NewUUIDV4String()
			result := tx.Model(&ResourceValidationModel{}).
				Where("id = ?", models[i].ID).
				UpdateColumns(map[string]any{
					"dispatch_token": dispatchToken,
					"dispatched_at":  now,
					"updated_at":     gorm.Expr("updated_at"),
				})
			if result.Error != nil {
				return fmt.Errorf("claim resource validation dispatch: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("claim resource validation dispatch: job %d changed concurrently", models[i].ID)
			}
			models[i].DispatchToken = dispatchToken
			models[i].DispatchedAt = &now
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim dispatchable resource validation jobs: %w", err)
	}
	result := make([]domain.ResourceValidation, len(models))
	for i := range models {
		result[i] = *models[i].toDomain()
	}
	return result, nil
}

func (r *ResourceValidationRepo) MarkRunning(ctx context.Context, id uint, dispatchToken string) (string, bool, error) {
	dispatchToken = strings.TrimSpace(dispatchToken)
	if dispatchToken == "" {
		return "", false, nil
	}
	now := time.Now().UTC()
	staleBefore := now.Add(-20 * time.Minute)
	claimToken := platform.NewUUIDV4String()
	claimed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job ResourceValidationModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock resource validation job: %w", err)
		}
		job.MaxAttempts = normalizeValidationMaxAttempts(job.MaxAttempts)
		if job.DispatchToken != dispatchToken {
			return nil
		}
		isQueued := job.Status == string(domain.ResourceValidationQueued)
		isStale := job.Status == string(domain.ResourceValidationRunning) && job.UpdatedAt.Before(staleBefore)
		if !isQueued && !isStale {
			return nil
		}

		nextAttempts := job.Attempts
		if isStale {
			nextAttempts++
		}
		if nextAttempts >= job.MaxAttempts {
			return tx.Model(&ResourceValidationModel{}).
				Where("id = ? AND status IN ?", id, []string{
					string(domain.ResourceValidationQueued),
					string(domain.ResourceValidationRunning),
				}).
				Updates(map[string]any{
					"status":          string(domain.ResourceValidationFailed),
					"attempts":        nextAttempts,
					"claim_token":     "",
					"dispatch_token":  "",
					"dispatched_at":   nil,
					"last_safe_error": "Resource validation retry attempts exhausted.",
					"finished_at":     now,
					"updated_at":      now,
				}).Error
		}

		result := tx.Model(&ResourceValidationModel{}).
			Where("id = ? AND status = ? AND dispatch_token = ?", id, job.Status, dispatchToken).
			Updates(map[string]any{
				"status":          string(domain.ResourceValidationRunning),
				"attempts":        nextAttempts,
				"claim_token":     claimToken,
				"dispatch_token":  "",
				"dispatched_at":   nil,
				"last_safe_error": "",
				"started_at":      now,
				"updated_at":      now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark resource validation running: %w", result.Error)
		}
		claimed = result.RowsAffected > 0
		return nil
	})
	if !claimed {
		claimToken = ""
	}
	return claimToken, claimed, err
}

func (r *ResourceValidationRepo) ReleaseDispatch(ctx context.Context, id uint, dispatchToken string) error {
	dispatchToken = strings.TrimSpace(dispatchToken)
	if id == 0 || dispatchToken == "" {
		return nil
	}
	result := r.db.WithContext(ctx).Model(&ResourceValidationModel{}).
		Where(
			"id = ? AND dispatch_token = ? AND status IN ?",
			id,
			dispatchToken,
			[]string{string(domain.ResourceValidationQueued), string(domain.ResourceValidationRunning)},
		).
		UpdateColumns(map[string]any{
			"dispatch_token": "",
			"dispatched_at":  nil,
			"updated_at":     gorm.Expr("updated_at"),
		})
	if result.Error != nil {
		return fmt.Errorf("release resource validation dispatch: %w", result.Error)
	}
	return nil
}

func (r *ResourceValidationRepo) MarkFailed(ctx context.Context, id uint, claimToken string, safeError string) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := finishValidationJobTx(tx, id, claimToken, string(domain.ResourceValidationFailed), safeValidationMessage(safeError), now); err != nil {
			return err
		}
		return nil
	})
}

func (r *ResourceValidationRepo) MarkRetryableFailure(ctx context.Context, id uint, claimToken string, safeError string) (bool, error) {
	now := time.Now().UTC()
	exhausted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job ResourceValidationModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND claim_token = ?", id, string(domain.ResourceValidationRunning), claimToken).
			First(&job).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrInvalidResourceStatus
			}
			return fmt.Errorf("lock resource validation retry: %w", err)
		}
		job.MaxAttempts = normalizeValidationMaxAttempts(job.MaxAttempts)
		nextAttempts := job.Attempts + 1
		nextStatus := string(domain.ResourceValidationQueued)
		updates := map[string]interface{}{
			"attempts":        nextAttempts,
			"status":          nextStatus,
			"claim_token":     "",
			"last_safe_error": safeValidationMessage(safeError),
			"updated_at":      now,
		}
		if nextAttempts >= job.MaxAttempts {
			exhausted = true
			updates["status"] = string(domain.ResourceValidationFailed)
			updates["finished_at"] = now
		}
		result := tx.Model(&ResourceValidationModel{}).
			Where("id = ? AND status = ? AND claim_token = ?", id, string(domain.ResourceValidationRunning), claimToken).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("mark resource validation retryable failure: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return domain.ErrInvalidResourceStatus
		}
		if exhausted {
			return createSystemLogInTx(ctx, tx, &governancedomain.SystemLog{
				Level:     "warning",
				Module:    "core",
				EventType: "resource.validation_retry_exhausted",
				RequestID: job.RequestID,
				BizType:   "resource",
				BizID:     fmt.Sprintf("%d", job.ResourceID),
				Message:   "Resource validation retry attempts exhausted.",
				Detail:    safeValidationMessage(safeError),
			})
		}
		return nil
	})
	return exhausted, err
}

func (r *ResourceValidationRepo) SaveMicrosoftCredentials(ctx context.Context, jobID uint, resourceID uint, claimToken string, clientID string, refreshToken string) error {
	clientID = strings.TrimSpace(clientID)
	refreshToken = strings.TrimSpace(refreshToken)
	if clientID == "" && refreshToken == "" {
		return nil
	}
	stale := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, ms, job, err := lockMicrosoftValidationStateTx(tx, jobID, resourceID, claimToken)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if !validationJobMatchesCredentialRevision(job, ms) ||
			domain.MicrosoftResourceStatus(ms.Status) == domain.MicrosoftStatusDeleted ||
			domain.MicrosoftResourceStatus(ms.Status) == domain.MicrosoftStatusDisabled {
			stale = true
			return finishStaleMicrosoftValidationTx(tx, job, now)
		}

		updates := map[string]any{}
		if clientID != "" && clientID != ms.ClientID {
			updates["client_id"] = clientID
		}
		if refreshToken != "" && refreshToken != ms.RefreshToken {
			updates["refresh_token"] = refreshToken
		}
		if len(updates) == 0 {
			return nil
		}
		nextRevision := ms.CredentialRevision + 1
		updates["credential_revision"] = nextRevision
		updates["credential_updated_at"] = now
		updates["token_last_refreshed_at"] = now
		updates["token_last_request_id"] = job.RequestID
		updates["updated_at"] = now
		result := tx.Model(&MicrosoftResourceModel{}).
			Where("id = ? AND credential_revision = ? AND status NOT IN ?", resourceID, ms.CredentialRevision, []string{
				string(domain.MicrosoftStatusDeleted),
				string(domain.MicrosoftStatusDisabled),
			}).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("save refreshed microsoft credentials: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return domain.ErrResourceVersionConflict
		}
		if err := tx.Model(&ResourceValidationModel{}).
			Where("id = ? AND status = ? AND claim_token = ?", job.ID, string(domain.ResourceValidationRunning), job.ClaimToken).
			Update("expected_credential_revision", nextRevision).Error; err != nil {
			return fmt.Errorf("advance validation credential revision: %w", err)
		}
		if err := bumpResourceVersionTx(tx, root.ID, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if stale {
		return coreapp.ErrValidationResultStale
	}
	return nil
}

func (r *ResourceValidationRepo) ApplyMicrosoftResult(ctx context.Context, jobID uint, resourceID uint, claimToken string, result coreapp.MicrosoftValidationResult, systemLog *governancedomain.SystemLog) error {
	stale := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		root, ms, job, err := lockMicrosoftValidationStateTx(tx, jobID, resourceID, claimToken)
		if err != nil {
			return err
		}
		if !validationJobMatchesCredentialRevision(job, ms) {
			stale = true
			if err := finishStaleMicrosoftValidationTx(tx, job, now); err != nil {
				return err
			}
			return createSystemLogInTx(ctx, tx, staleValidationSystemLog(systemLog))
		}
		switch domain.MicrosoftResourceStatus(ms.Status) {
		case domain.MicrosoftStatusDeleted:
			stale = true
			return markValidationJobFailedTx(tx, job.ID, job.ClaimToken, "Resource not found.", now)
		case domain.MicrosoftStatusDisabled:
			stale = true
			return markValidationJobFailedTx(tx, job.ID, job.ClaimToken, "Resource status does not allow validation.", now)
		}

		safeMessage := safeValidationMessage(result.SafeMessage)
		nextStatus := string(domain.MicrosoftStatusAbnormal)
		jobStatus := string(domain.ResourceValidationFailed)
		if result.Valid {
			nextStatus = string(domain.MicrosoftStatusNormal)
			jobStatus = string(domain.ResourceValidationSucceeded)
			safeMessage = ""
		}

		updates := map[string]interface{}{
			"status":          nextStatus,
			"quality_score":   validationQualityScore(result.Valid),
			"graph_available": false,
			"last_safe_error": safeMessage,
			"updated_at":      now,
		}
		if result.Valid {
			credentialsChanged := false
			if value := strings.TrimSpace(result.ClientID); value != "" && value != ms.ClientID {
				updates["client_id"] = value
				credentialsChanged = true
			}
			if value := strings.TrimSpace(result.RefreshToken); value != "" && value != ms.RefreshToken {
				updates["refresh_token"] = value
				credentialsChanged = true
			}
			if result.RTExpireAt != nil {
				updates["rt_expire_at"] = result.RTExpireAt
			}
			updates["graph_available"] = result.GraphAvailable
			if credentialsChanged {
				updates["credential_revision"] = ms.CredentialRevision + 1
				updates["credential_updated_at"] = now
				updates["token_last_refreshed_at"] = now
				updates["token_last_request_id"] = job.RequestID
			}
		}
		updated := tx.Model(&MicrosoftResourceModel{}).
			Where("id = ? AND credential_revision = ? AND status <> ?", resourceID, ms.CredentialRevision, string(domain.MicrosoftStatusDeleted)).
			Updates(updates)
		if updated.Error != nil {
			return fmt.Errorf("apply microsoft validation result: %w", updated.Error)
		}
		if updated.RowsAffected == 0 {
			return domain.ErrResourceVersionConflict
		}
		if err := bumpResourceVersionTx(tx, root.ID, now); err != nil {
			return err
		}
		if err := finishValidationJobTx(tx, job.ID, job.ClaimToken, jobStatus, safeMessage, now); err != nil {
			return err
		}
		return createSystemLogInTx(ctx, tx, systemLog)
	})
	if err != nil {
		return err
	}
	if stale {
		return coreapp.ErrValidationResultStale
	}
	return nil
}

func (r *ResourceValidationRepo) ApplyDomainResult(ctx context.Context, jobID uint, resourceID uint, claimToken string, result coreapp.DomainValidationResult, systemLog *governancedomain.SystemLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := lockRunningValidationJob(tx, jobID, claimToken); err != nil {
			return err
		}
		var dr DomainResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dr, resourceID).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return markValidationJobFailedTx(tx, jobID, claimToken, "Resource not found.", now)
			}
			return fmt.Errorf("lock domain resource for validation: %w", err)
		}
		switch domain.MailDomainStatus(dr.Status) {
		case domain.DomainStatusDeleted:
			return markValidationJobFailedTx(tx, jobID, claimToken, "Resource not found.", now)
		case domain.DomainStatusDisabled:
			return markValidationJobFailedTx(tx, jobID, claimToken, "Resource status does not allow validation.", now)
		}

		safeMessage := safeValidationMessage(result.SafeMessage)
		nextStatus := string(domain.DomainStatusAbnormal)
		jobStatus := string(domain.ResourceValidationFailed)
		if result.Valid {
			nextStatus = string(domain.DomainStatusNormal)
			jobStatus = string(domain.ResourceValidationSucceeded)
			safeMessage = ""
		}
		if err := tx.Model(&DomainResourceModel{}).
			Where("id = ? AND status <> ?", resourceID, string(domain.DomainStatusDeleted)).
			Updates(map[string]interface{}{
				"status":          nextStatus,
				"last_safe_error": safeMessage,
				"updated_at":      now,
			}).Error; err != nil {
			return fmt.Errorf("apply domain validation result: %w", err)
		}
		if err := finishValidationJobTx(tx, jobID, claimToken, jobStatus, safeMessage, now); err != nil {
			return err
		}
		return createSystemLogInTx(ctx, tx, systemLog)
	})
}

func (r *ResourceValidationRepo) MarkDispatchFailed(ctx context.Context, id uint, dispatchToken string, safeError string) error {
	dispatchToken = strings.TrimSpace(dispatchToken)
	if id == 0 || dispatchToken == "" {
		return nil
	}
	result := r.db.WithContext(ctx).Model(&ResourceValidationModel{}).
		Where(
			"id = ? AND dispatch_token = ? AND status IN ?",
			id,
			dispatchToken,
			[]string{string(domain.ResourceValidationQueued), string(domain.ResourceValidationRunning)},
		).
		UpdateColumns(map[string]any{
			"dispatch_token":  "",
			"dispatched_at":   nil,
			"last_safe_error": safeValidationMessage(safeError),
			"updated_at":      gorm.Expr("updated_at"),
		})
	if result.Error != nil {
		return fmt.Errorf("mark resource validation dispatch failed: %w", result.Error)
	}
	return nil
}

func lockRunningValidationJob(tx *gorm.DB, id uint, claimToken string) error {
	var job ResourceValidationModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = ? AND claim_token = ?", id, string(domain.ResourceValidationRunning), claimToken).
		First(&job).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrInvalidResourceStatus
		}
		return fmt.Errorf("lock resource validation job: %w", err)
	}
	return nil
}

// lockMicrosoftValidationStateTx follows the global mutation lock order used
// by Core and Alloc: resource root, Microsoft subtype, then durable job. That
// order prevents administrator commands and validation workers from forming a
// root/job lock cycle while still fencing duplicate worker deliveries.
func lockMicrosoftValidationStateTx(tx *gorm.DB, jobID uint, resourceID uint, claimToken string) (*EmailResourceModel, *MicrosoftResourceModel, *ResourceValidationModel, error) {
	var root EmailResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", resourceID, string(domain.ResourceTypeMicrosoft)).
		First(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, domain.ErrResourceNotFound
		}
		return nil, nil, nil, fmt.Errorf("lock microsoft resource root for validation: %w", err)
	}

	var ms MicrosoftResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", resourceID).
		First(&ms).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, domain.ErrResourceNotFound
		}
		return nil, nil, nil, fmt.Errorf("lock microsoft resource for validation: %w", err)
	}

	var job ResourceValidationModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			"id = ? AND resource_id = ? AND resource_type = ? AND status = ? AND claim_token = ?",
			jobID,
			resourceID,
			string(domain.ResourceTypeMicrosoft),
			string(domain.ResourceValidationRunning),
			claimToken,
		).
		First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, domain.ErrInvalidResourceStatus
		}
		return nil, nil, nil, fmt.Errorf("lock microsoft validation job: %w", err)
	}
	return &root, &ms, &job, nil
}

func validationJobMatchesCredentialRevision(job *ResourceValidationModel, ms *MicrosoftResourceModel) bool {
	return job != nil && ms != nil &&
		job.ExpectedCredentialRevision > 0 &&
		job.ExpectedCredentialRevision == ms.CredentialRevision
}

func finishStaleMicrosoftValidationTx(tx *gorm.DB, job *ResourceValidationModel, now time.Time) error {
	if job == nil {
		return domain.ErrInvalidResourceStatus
	}
	return finishValidationJobTx(
		tx,
		job.ID,
		job.ClaimToken,
		string(domain.ResourceValidationFailed),
		"Resource credentials changed; stale validation result was discarded.",
		now,
	)
}

func staleValidationSystemLog(log *governancedomain.SystemLog) *governancedomain.SystemLog {
	if log == nil {
		return nil
	}
	copyLog := *log
	copyLog.Level = "info"
	copyLog.EventType = "resource.validation_stale"
	copyLog.Message = "Stale resource validation result was discarded."
	copyLog.Detail = "Resource credentials changed before the worker committed its result."
	return &copyLog
}

func bumpResourceVersionTx(tx *gorm.DB, resourceID uint, now time.Time) error {
	result := tx.Model(&EmailResourceModel{}).
		Where("id = ?", resourceID).
		Updates(map[string]any{
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("advance resource version: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrResourceNotFound
	}
	return nil
}

func markValidationJobFailedTx(tx *gorm.DB, jobID uint, claimToken string, safeError string, now time.Time) error {
	return finishValidationJobTx(tx, jobID, claimToken, string(domain.ResourceValidationFailed), safeValidationMessage(safeError), now)
}

func finishValidationJobTx(tx *gorm.DB, jobID uint, claimToken string, status string, safeError string, now time.Time) error {
	result := tx.Model(&ResourceValidationModel{}).
		Where("id = ? AND status = ? AND claim_token = ?", jobID, string(domain.ResourceValidationRunning), claimToken).
		Updates(map[string]interface{}{
			"status":          status,
			"claim_token":     "",
			"last_safe_error": safeError,
			"finished_at":     now,
			"updated_at":      now,
		})
	if result.Error != nil {
		return fmt.Errorf("finish resource validation job: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrInvalidResourceStatus
	}
	return nil
}

func supersedeValidationJobTx(tx *gorm.DB, jobID uint, safeError string) error {
	now := time.Now().UTC()
	result := tx.Model(&ResourceValidationModel{}).
		Where("id = ? AND status IN ?", jobID, []string{
			string(domain.ResourceValidationQueued),
			string(domain.ResourceValidationRunning),
		}).
		Updates(map[string]any{
			"status":          string(domain.ResourceValidationFailed),
			"claim_token":     "",
			"dispatch_token":  "",
			"dispatched_at":   nil,
			"last_safe_error": safeValidationMessage(safeError),
			"finished_at":     now,
			"updated_at":      now,
		})
	if result.Error != nil {
		return fmt.Errorf("supersede resource validation job: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrInvalidResourceStatus
	}
	return nil
}

type validationCandidateRow struct {
	ID                 uint
	ResourceType       string `gorm:"column:resource_type"`
	OwnerUserID        uint   `gorm:"column:owner_user_id"`
	CredentialRevision uint64 `gorm:"column:credential_revision"`
	MicrosoftStatus    string `gorm:"column:microsoft_status"`
	DomainStatus       string `gorm:"column:domain_status"`
}

func selectValidationCandidatesByIDs(ctx context.Context, tx *gorm.DB, ownerUserID uint, ids []uint) ([]validationCandidateRow, error) {
	if len(ids) == 0 {
		return nil, domain.ErrResourceNotFound
	}
	var rows []validationCandidateRow
	if err := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, COALESCE(ms.credential_revision, 0) AS credential_revision, COALESCE(ms.status, '') AS microsoft_status, COALESCE(dr.status, '') AS domain_status").
		Joins("LEFT JOIN microsoft_resources AS ms ON ms.id = er.id AND er.type = ?", string(domain.ResourceTypeMicrosoft)).
		Joins("LEFT JOIN domain_resources AS dr ON dr.id = er.id AND er.type = ?", string(domain.ResourceTypeDomain)).
		Where("er.id IN ? AND er.owner_user_id = ?", ids, ownerUserID).
		Order("er.id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select validation resources: %w", err)
	}
	if len(rows) != len(ids) {
		return nil, domain.ErrForbiddenResource
	}
	return validateValidationCandidateRows(rows)
}

// Deferred batches are accepted against a point-in-time selection. Resources
// may be deleted, disabled, or transferred before a later page expands; those
// rows are skipped so one stale ID cannot poison the global dispatcher.
func selectAvailableValidationCandidatesByIDs(ctx context.Context, tx *gorm.DB, ownerUserID uint, ids []uint) ([]validationCandidateRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []validationCandidateRow
	if err := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, COALESCE(ms.credential_revision, 0) AS credential_revision, COALESCE(ms.status, '') AS microsoft_status, COALESCE(dr.status, '') AS domain_status").
		Joins("LEFT JOIN microsoft_resources AS ms ON ms.id = er.id AND er.type = ?", string(domain.ResourceTypeMicrosoft)).
		Joins("LEFT JOIN domain_resources AS dr ON dr.id = er.id AND er.type = ?", string(domain.ResourceTypeDomain)).
		Where("er.id IN ? AND er.owner_user_id = ?", ids, ownerUserID).
		Order("er.id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select deferred validation resources: %w", err)
	}
	result := make([]validationCandidateRow, 0, len(rows))
	for _, row := range rows {
		switch domain.ResourceType(row.ResourceType) {
		case domain.ResourceTypeMicrosoft:
			status := domain.MicrosoftResourceStatus(row.MicrosoftStatus)
			if status == "" || status == domain.MicrosoftStatusDeleted || status == domain.MicrosoftStatusDisabled {
				continue
			}
		case domain.ResourceTypeDomain:
			status := domain.MailDomainStatus(row.DomainStatus)
			if status == "" || status == domain.DomainStatusDeleted || status == domain.DomainStatusDisabled {
				continue
			}
		default:
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

func selectValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter, afterID uint, throughID uint, limit int) ([]validationCandidateRow, error) {
	switch filter.ResourceType {
	case domain.ResourceTypeMicrosoft:
		return selectMicrosoftValidationCandidatesByFilter(ctx, tx, ownerUserID, filter, afterID, throughID, limit)
	case domain.ResourceTypeDomain:
		return selectDomainValidationCandidatesByFilter(ctx, tx, ownerUserID, filter, afterID, throughID, limit)
	default:
		return nil, domain.ErrInvalidResourceType
	}
}

func selectMicrosoftValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter, afterID uint, throughID uint, limit int) ([]validationCandidateRow, error) {
	q := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, ms.credential_revision AS credential_revision, ms.status AS microsoft_status, '' AS domain_status").
		Joins("JOIN microsoft_resources AS ms ON ms.id = er.id").
		Where("er.owner_user_id = ? AND er.type = ?", ownerUserID, string(domain.ResourceTypeMicrosoft)).
		Where("er.id <= ?", throughID).
		Where("ms.status NOT IN ?", []string{string(domain.MicrosoftStatusDeleted), string(domain.MicrosoftStatusDisabled)})

	if afterID > 0 {
		q = q.Where("er.id > ?", afterID)
	}
	if filter.Status != "" {
		q = q.Where("ms.status = ?", filter.Status)
	}
	if filter.ForSale != nil {
		q = q.Where("ms.for_sale = ?", *filter.ForSale)
	}
	if filter.LongLived != nil {
		q = q.Where("ms.long_lived = ?", *filter.LongLived)
	}
	if filter.GraphAvailable != nil {
		q = q.Where("ms.graph_available = ?", *filter.GraphAvailable)
	}
	if filter.CreatedFrom != nil {
		q = q.Where("er.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		q = q.Where("er.created_at <= ?", *filter.CreatedTo)
	}
	if filter.Suffix != "" {
		suffix := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(filter.Suffix)), "@")
		if suffix != "" {
			q = q.Where("ms.email_domain = ?", suffix)
		}
	}
	if filter.Search != "" {
		prefix := strings.ToLower(strings.TrimSpace(filter.Search)) + "%"
		q = q.Where(
			"(ms.email_address LIKE ? OR ms.email_domain LIKE ?)",
			prefix,
			prefix,
		)
	}

	var rows []validationCandidateRow
	q = q.Order("er.id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select microsoft validation resources: %w", err)
	}
	return validateValidationCandidateRows(rows)
}

func selectDomainValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter, afterID uint, throughID uint, limit int) ([]validationCandidateRow, error) {
	q := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, 0 AS credential_revision, '' AS microsoft_status, dr.status AS domain_status").
		Joins("JOIN domain_resources AS dr ON dr.id = er.id").
		Where("er.owner_user_id = ? AND er.type = ?", ownerUserID, string(domain.ResourceTypeDomain)).
		Where("er.id <= ?", throughID).
		Where("dr.owner_user_id = ?", ownerUserID).
		Where("dr.status NOT IN ?", []string{string(domain.DomainStatusDeleted), string(domain.DomainStatusDisabled)})

	if afterID > 0 {
		q = q.Where("er.id > ?", afterID)
	}
	if filter.Status != "" {
		q = q.Where("dr.status = ?", filter.Status)
	}
	if filter.Purpose != "" {
		q = q.Where("dr.purpose = ?", filter.Purpose)
	}
	if filter.CreatedFrom != nil {
		q = q.Where("er.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		q = q.Where("er.created_at <= ?", *filter.CreatedTo)
	}
	if filter.TLD != "" {
		q = q.Where("dr.domain_tld = ?", filter.TLD)
	}
	if filter.Search != "" {
		q = q.Where("dr.domain LIKE ?", strings.ToLower(strings.TrimSpace(filter.Search))+"%")
	}

	var rows []validationCandidateRow
	q = q.Order("er.id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select domain validation resources: %w", err)
	}
	return validateValidationCandidateRows(rows)
}

func validateValidationCandidateRows(rows []validationCandidateRow) ([]validationCandidateRow, error) {
	for _, row := range rows {
		switch domain.ResourceType(row.ResourceType) {
		case domain.ResourceTypeMicrosoft:
			switch domain.MicrosoftResourceStatus(row.MicrosoftStatus) {
			case "":
				return nil, domain.ErrResourceNotFound
			case domain.MicrosoftStatusDeleted:
				return nil, domain.ErrResourceNotFound
			case domain.MicrosoftStatusDisabled:
				return nil, domain.ErrInvalidResourceStatus
			}
		case domain.ResourceTypeDomain:
			switch domain.MailDomainStatus(row.DomainStatus) {
			case "":
				return nil, domain.ErrResourceNotFound
			case domain.DomainStatusDeleted:
				return nil, domain.ErrResourceNotFound
			case domain.DomainStatusDisabled:
				return nil, domain.ErrInvalidResourceStatus
			}
		default:
			return nil, domain.ErrInvalidResourceType
		}
	}
	return rows, nil
}

func resetAbnormalMicrosoftCandidatesTx(ctx context.Context, tx *gorm.DB, candidates []validationCandidateRow) error {
	ids := make([]uint, 0)
	for _, candidate := range candidates {
		if domain.ResourceType(candidate.ResourceType) == domain.ResourceTypeMicrosoft {
			ids = append(ids, candidate.ID)
		}
	}
	now := time.Now().UTC()
	for start := 0; start < len(ids); start += resourceValidationBatchInsertSize {
		end := start + resourceValidationBatchInsertSize
		if end > len(ids) {
			end = len(ids)
		}
		if err := tx.WithContext(ctx).
			Model(&MicrosoftResourceModel{}).
			Where("id IN ? AND status = ?", ids[start:end], string(domain.MicrosoftStatusAbnormal)).
			Updates(map[string]interface{}{
				"status":          string(domain.MicrosoftStatusPending),
				"last_safe_error": "",
				"updated_at":      now,
			}).Error; err != nil {
			return fmt.Errorf("mark microsoft resources pending for validation: %w", err)
		}
	}
	return nil
}

func findActiveValidationJobTx(tx *gorm.DB, resourceID uint) (*domain.ResourceValidation, error) {
	var model ResourceValidationModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ? AND status IN ?", resourceID, []string{
			string(domain.ResourceValidationQueued),
			string(domain.ResourceValidationRunning),
		}).
		Order("id DESC").
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find active resource validation job: %w", err)
	}
	return model.toDomain(), nil
}

func prepareResourceForValidationTx(tx *gorm.DB, job *domain.ResourceValidation) error {
	var root EmailResourceModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ? AND owner_user_id = ?", job.ResourceID, string(job.ResourceType), job.OwnerUserID).
		First(&root).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrResourceNotFound
		}
		return fmt.Errorf("lock resource for validation: %w", err)
	}

	now := time.Now().UTC()
	switch job.ResourceType {
	case domain.ResourceTypeMicrosoft:
		var ms MicrosoftResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ms, job.ResourceID).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock microsoft resource for validation create: %w", err)
		}
		switch domain.MicrosoftResourceStatus(ms.Status) {
		case domain.MicrosoftStatusDeleted:
			return domain.ErrResourceNotFound
		case domain.MicrosoftStatusDisabled:
			return domain.ErrInvalidResourceStatus
		case domain.MicrosoftStatusAbnormal:
			if err := tx.Model(&MicrosoftResourceModel{}).
				Where("id = ? AND status = ?", job.ResourceID, string(domain.MicrosoftStatusAbnormal)).
				Updates(map[string]interface{}{
					"status":          string(domain.MicrosoftStatusPending),
					"last_safe_error": "",
					"updated_at":      now,
				}).Error; err != nil {
				return fmt.Errorf("mark microsoft resource pending for validation: %w", err)
			}
		}
		job.ExpectedCredentialRevision = ms.CredentialRevision
	case domain.ResourceTypeDomain:
		var dr DomainResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dr, job.ResourceID).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock domain resource for validation create: %w", err)
		}
		switch domain.MailDomainStatus(dr.Status) {
		case domain.DomainStatusDeleted:
			return domain.ErrResourceNotFound
		case domain.DomainStatusDisabled:
			return domain.ErrInvalidResourceStatus
		}
	default:
		return domain.ErrInvalidResourceType
	}
	return nil
}

func createSystemLogInTx(ctx context.Context, tx *gorm.DB, log *governancedomain.SystemLog) error {
	if log == nil {
		return nil
	}
	model := &governanceinfra.SystemLogModel{
		Level:     log.Level,
		Module:    log.Module,
		EventType: log.EventType,
		RequestID: log.RequestID,
		BizType:   log.BizType,
		BizID:     log.BizID,
		Message:   safeValidationMessage(log.Message),
		Detail:    safeValidationMessage(log.Detail),
	}
	if err := tx.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("create system log: %w", err)
	}
	return nil
}

func validationQualityScore(valid bool) int {
	if valid {
		return 100
	}
	return 0
}

func safeValidationMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	const maxLen = 500
	if len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}

func normalizeValidationMaxAttempts(value int) int {
	if value <= 0 {
		return domain.ResourceValidationDefaultMaxAttempts
	}
	return value
}
