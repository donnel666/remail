package infra

import (
	"context"
	"errors"
	"fmt"
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
	db                     *gorm.DB
	operationLogs          *governanceinfra.OperationLogRepo
	microsoftBindingCommit coreapp.MicrosoftValidationBindingCommitPort
}

const (
	resourceValidationInsertSize = 1000
)

func NewResourceValidationRepo(db *gorm.DB) *ResourceValidationRepo {
	return &ResourceValidationRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
}

// SetMicrosoftValidationBindingCommitPort installs the MailTransport-owned
// writer used to commit recovery-mailbox facts. The writer is called only from
// caller-owned, fenced validation progress/result transactions.
func (r *ResourceValidationRepo) SetMicrosoftValidationBindingCommitPort(port coreapp.MicrosoftValidationBindingCommitPort) {
	if r != nil {
		r.microsoftBindingCommit = port
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
	if !selection.AllowBinding {
		selection.Filter.ExcludeBinding = true
	}
	result := &coreapp.ResourceBatchValidationResult{}
	persist := func(tx *gorm.DB) error {
		var candidates []validationCandidateRow
		var err error
		switch selection.Mode {
		case coreapp.ResourceBulkSelectionIDs:
			candidates, err = selectAvailableValidationCandidatesByIDs(ctx, tx, ownerUserID, selection.ResourceIDs, selection.AllowBinding)
		case coreapp.ResourceBulkSelectionFilter:
			candidates, err = selectValidationCandidatesByFilter(ctx, tx, ownerUserID, selection.Filter)
		default:
			return domain.ErrInvalidResourceType
		}
		if err != nil {
			return err
		}

		result.Requested = len(candidates)
		pendingCandidates := make([]validationCandidateRow, 0, len(candidates))
		immediateJobs := make([]validationCandidateRow, 0, len(candidates))
		for _, candidate := range candidates {
			if domain.ResourceType(candidate.ResourceType) != domain.ResourceTypeMicrosoft {
				immediateJobs = append(immediateJobs, candidate)
				continue
			}
			switch domain.MicrosoftResourceStatus(candidate.MicrosoftStatus) {
			case domain.MicrosoftStatusAbnormal:
				pendingCandidates = append(pendingCandidates, candidate)
			case domain.MicrosoftStatusPending:
				// Pending is already the durable validation request. The dispatcher
				// will create a job if no active one exists.
			default:
				// Normal resources can still be explicitly revalidated without
				// changing their externally visible status first.
				immediateJobs = append(immediateJobs, candidate)
			}
		}
		updated, err := markAbnormalMicrosoftCandidatesPendingTx(ctx, tx, pendingCandidates)
		if err != nil {
			return err
		}
		result.Queued = updated
		if len(immediateJobs) > 0 {
			jobResult := &coreapp.ResourceBatchValidationResult{}
			if err := createValidationCandidateJobsTx(ctx, tx, immediateJobs, requestID, path, jobResult); err != nil {
				return err
			}
			result.Queued += jobResult.Queued
			result.Created += jobResult.Created
		}
		if log != nil {
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return fmt.Errorf("create operation log: %w", err)
			}
		}
		return nil
	}
	var err error
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		err = persist(tx.WithContext(ctx))
	} else {
		err = r.db.WithContext(ctx).Transaction(persist)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *ResourceValidationRepo) CreateImportedValidationBatch(ctx context.Context, ownerUserID uint, resourceIDs []uint, requestID string, path string) error {
	if len(resourceIDs) == 0 {
		return nil
	}
	_, err := r.CreateBatchWithLog(ctx, ownerUserID, coreapp.ResourceBulkSelection{
		Mode:        coreapp.ResourceBulkSelectionIDs,
		ResourceIDs: append([]uint(nil), resourceIDs...),
	}, nil, requestID, path)
	return err
}

func createValidationCandidateJobsTx(ctx context.Context, tx *gorm.DB, candidates []validationCandidateRow, requestID, path string, result *coreapp.ResourceBatchValidationResult) error {
	if len(candidates) == 0 {
		return nil
	}
	result.Requested += len(candidates)
	result.Queued += len(candidates)

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
	for start := 0; start < len(models); start += resourceValidationInsertSize {
		end := start + resourceValidationInsertSize
		if end > len(models) {
			end = len(models)
		}
		inserted := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(models[start:end], resourceValidationInsertSize)
		if inserted.Error != nil {
			return fmt.Errorf("create resource validation jobs: %w", inserted.Error)
		}
		result.Created += int(inserted.RowsAffected)
	}
	return nil
}

func (r *ResourceValidationRepo) CreatePendingValidationJobs(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	created := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []validationCandidateRow
		if err := tx.WithContext(ctx).
			Table("email_resources AS er").
			Select("er.id, er.type AS resource_type, er.owner_user_id, ms.credential_revision, ms.status AS microsoft_status, '' AS domain_status").
			Joins("JOIN microsoft_resources AS ms ON ms.id = er.id").
			Where("er.type = ? AND ms.status = ?", string(domain.ResourceTypeMicrosoft), string(domain.MicrosoftStatusPending)).
			Where("NOT EXISTS (SELECT 1 FROM resource_validation_jobs AS rvj WHERE rvj.resource_id = er.id AND rvj.status IN ?)", []string{
				string(domain.ResourceValidationQueued),
				string(domain.ResourceValidationRunning),
			}).
			Order("er.id ASC").
			Limit(limit).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Find(&candidates).Error; err != nil {
			return fmt.Errorf("select pending resource validations: %w", err)
		}
		result := &coreapp.ResourceBatchValidationResult{}
		if err := createValidationCandidateJobsTx(ctx, tx, candidates, "", "", result); err != nil {
			return err
		}
		created = result.Created
		return nil
	})
	return created, err
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
		var hint ResourceValidationModel
		if err := tx.Select("id, resource_id, resource_type").Where("id = ?", id).Take(&hint).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrInvalidResourceStatus
			}
			return fmt.Errorf("load resource validation retry lock hint: %w", err)
		}

		var root EmailResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ?", hint.ResourceID, hint.ResourceType).
			First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock resource root for validation retry: %w", err)
		}
		switch hint.ResourceType {
		case string(domain.ResourceTypeMicrosoft):
			var resource MicrosoftResourceModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, hint.ResourceID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domain.ErrResourceNotFound
				}
				return fmt.Errorf("lock microsoft resource for validation retry: %w", err)
			}
		case string(domain.ResourceTypeDomain):
			var resource DomainResourceModel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, hint.ResourceID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domain.ErrResourceNotFound
				}
				return fmt.Errorf("lock domain resource for validation retry: %w", err)
			}
		default:
			return domain.ErrInvalidResourceType
		}

		var job ResourceValidationModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(
				"id = ? AND resource_id = ? AND resource_type = ? AND status = ? AND claim_token = ?",
				id,
				hint.ResourceID,
				hint.ResourceType,
				string(domain.ResourceValidationRunning),
				claimToken,
			).
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
			// Retryable validation attempts are exhausted. Previously only the job
			// was marked failed, leaving the resource stuck in `pending` forever
			// (never abnormal, endlessly re-validated). Mark the resource abnormal
			// in the same transaction so it reaches a terminal state. Branch by
			// resource type so both Microsoft and domain resources are covered.
			resourceChanged := false
			switch job.ResourceType {
			case string(domain.ResourceTypeMicrosoft):
				updated := tx.Model(&MicrosoftResourceModel{}).
					Where("id = ? AND status NOT IN ?", job.ResourceID,
						[]string{string(domain.MicrosoftStatusDeleted), string(domain.MicrosoftStatusDisabled)}).
					Updates(map[string]interface{}{
						"status":          string(domain.MicrosoftStatusAbnormal),
						"quality_score":   validationQualityScore(false),
						"last_safe_error": safeValidationMessage(safeError),
						"updated_at":      now,
					})
				if updated.Error != nil {
					return fmt.Errorf("mark microsoft resource abnormal on validation exhaustion: %w", updated.Error)
				}
				resourceChanged = updated.RowsAffected > 0
			case string(domain.ResourceTypeDomain):
				updated := tx.Model(&DomainResourceModel{}).
					Where("id = ? AND status NOT IN ?", job.ResourceID,
						[]string{string(domain.DomainStatusDeleted), string(domain.DomainStatusDisabled)}).
					Updates(map[string]interface{}{
						"status":          string(domain.DomainStatusAbnormal),
						"last_safe_error": safeValidationMessage(safeError),
						"updated_at":      now,
					})
				if updated.Error != nil {
					return fmt.Errorf("mark domain resource abnormal on validation exhaustion: %w", updated.Error)
				}
				resourceChanged = updated.RowsAffected > 0
			}
			if resourceChanged {
				if err := bumpResourceVersionTx(tx, root.ID, now); err != nil {
					return err
				}
			}
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

func (r *ResourceValidationRepo) SaveMicrosoftProgress(ctx context.Context, jobID uint, resourceID uint, claimToken string, result coreapp.MicrosoftValidationResult) error {
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

		bindingChanged, err := r.commitMicrosoftValidationBindingTx(ctx, tx, root, ms, result)
		if errors.Is(err, coreapp.ErrValidationResultStale) {
			stale = true
			return finishStaleMicrosoftValidationTx(tx, job, now)
		}
		if err != nil {
			return err
		}

		updates := map[string]any{}
		clientID := ""
		refreshToken := ""
		if result.CredentialsAuthoritative {
			clientID = strings.TrimSpace(result.ClientID)
			refreshToken = strings.TrimSpace(result.RefreshToken)
		}
		if clientID != "" && clientID != ms.ClientID {
			updates["client_id"] = clientID
		}
		if refreshToken != "" && refreshToken != ms.RefreshToken {
			updates["refresh_token"] = refreshToken
		}
		credentialsChanged := len(updates) > 0
		if credentialsChanged {
			nextRevision := ms.CredentialRevision + 1
			updates["credential_revision"] = nextRevision
			updates["credential_updated_at"] = now
			updates["token_last_refreshed_at"] = now
			updates["token_last_request_id"] = job.RequestID
			updates["updated_at"] = now
			updated := tx.Model(&MicrosoftResourceModel{}).
				Where("id = ? AND credential_revision = ? AND status NOT IN ?", resourceID, ms.CredentialRevision, []string{
					string(domain.MicrosoftStatusDeleted),
					string(domain.MicrosoftStatusDisabled),
				}).
				Updates(updates)
			if updated.Error != nil {
				return fmt.Errorf("save microsoft validation progress credentials: %w", updated.Error)
			}
			if updated.RowsAffected == 0 {
				return domain.ErrResourceVersionConflict
			}
			jobUpdate := tx.Model(&ResourceValidationModel{}).
				Where("id = ? AND status = ? AND claim_token = ?", job.ID, string(domain.ResourceValidationRunning), job.ClaimToken).
				Update("expected_credential_revision", nextRevision)
			if jobUpdate.Error != nil {
				return fmt.Errorf("advance validation credential revision: %w", jobUpdate.Error)
			}
			if jobUpdate.RowsAffected == 0 {
				return domain.ErrInvalidResourceStatus
			}
		}
		if bindingChanged || credentialsChanged {
			if err := bumpResourceVersionTx(tx, root.ID, now); err != nil {
				return err
			}
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

// SaveMicrosoftCredentials is kept as a narrow compatibility wrapper for
// callers that only persist token rotation. New validation code must use
// SaveMicrosoftProgress so binding facts and credentials share one fence and
// one root-version advance.
func (r *ResourceValidationRepo) SaveMicrosoftCredentials(ctx context.Context, jobID uint, resourceID uint, claimToken string, clientID string, refreshToken string) error {
	if strings.TrimSpace(clientID) == "" && strings.TrimSpace(refreshToken) == "" {
		return nil
	}
	return r.SaveMicrosoftProgress(ctx, jobID, resourceID, claimToken, coreapp.MicrosoftValidationResult{
		ClientID:                 clientID,
		RefreshToken:             refreshToken,
		CredentialsAuthoritative: true,
	})
}

func (r *ResourceValidationRepo) commitMicrosoftValidationBindingTx(
	ctx context.Context,
	tx *gorm.DB,
	root *EmailResourceModel,
	ms *MicrosoftResourceModel,
	result coreapp.MicrosoftValidationResult,
) (bool, error) {
	if result.RecoveredBinding == nil && result.BindingObservation == nil {
		return false, nil
	}
	if r.microsoftBindingCommit == nil || root == nil || ms == nil {
		return false, domain.ErrResourceDependency
	}
	changed, err := r.microsoftBindingCommit.CommitValidationBinding(
		platform.WithGormTx(ctx, tx.WithContext(ctx)),
		coreapp.MicrosoftValidationBindingCommand{
			ResourceID:         root.ID,
			OwnerUserID:        root.OwnerUserID,
			AccountEmail:       ms.EmailAddress,
			RecoveredBinding:   result.RecoveredBinding,
			BindingObservation: result.BindingObservation,
		},
	)
	if err != nil {
		return false, fmt.Errorf("commit microsoft validation binding: %w", err)
	}
	return changed, nil
}

func (r *ResourceValidationRepo) ApplyMicrosoftResult(ctx context.Context, jobID uint, resourceID uint, claimToken string, result coreapp.MicrosoftValidationResult, systemLog *governancedomain.SystemLog) error {
	stale := false
	bindingRejected := false
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

		_, commitErr := r.commitMicrosoftValidationBindingTx(ctx, tx, root, ms, result)
		if errors.Is(commitErr, coreapp.ErrValidationBindingRejected) {
			bindingRejected = true
			result.Valid = false
			result.Category = "binding"
			result.SafeMessage = coreapp.MicrosoftValidationBindingRejectedMessage
			systemLog = rejectedMicrosoftBindingValidationSystemLog(job)
		} else if errors.Is(commitErr, coreapp.ErrValidationResultStale) {
			stale = true
			if err := finishStaleMicrosoftValidationTx(tx, job, now); err != nil {
				return err
			}
			return createSystemLogInTx(ctx, tx, staleValidationSystemLog(systemLog))
		}
		if commitErr != nil && !bindingRejected {
			return commitErr
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
		credentialsChanged := false
		if result.Valid || result.CredentialsAuthoritative {
			if value := strings.TrimSpace(result.ClientID); value != "" && value != ms.ClientID {
				updates["client_id"] = value
				credentialsChanged = true
			}
			if value := strings.TrimSpace(result.RefreshToken); value != "" && value != ms.RefreshToken {
				updates["refresh_token"] = value
				credentialsChanged = true
			}
		}
		if result.Valid {
			if result.RTExpireAt != nil {
				updates["rt_expire_at"] = result.RTExpireAt
			}
			updates["graph_available"] = result.GraphAvailable
		}
		if credentialsChanged {
			updates["credential_revision"] = ms.CredentialRevision + 1
			updates["credential_updated_at"] = now
			updates["token_last_refreshed_at"] = now
			updates["token_last_request_id"] = job.RequestID
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
	if bindingRejected {
		return coreapp.ErrValidationBindingRejected
	}
	return nil
}

func (r *ResourceValidationRepo) ApplyDomainResult(ctx context.Context, jobID uint, resourceID uint, claimToken string, result coreapp.DomainValidationResult, systemLog *governancedomain.SystemLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		_, dr, job, err := lockDomainValidationStateTx(tx, jobID, resourceID, claimToken)
		if err != nil {
			return err
		}
		switch domain.MailDomainStatus(dr.Status) {
		case domain.DomainStatusDeleted:
			return markValidationJobFailedTx(tx, job.ID, job.ClaimToken, "Resource not found.", now)
		case domain.DomainStatusDisabled:
			return markValidationJobFailedTx(tx, job.ID, job.ClaimToken, "Resource status does not allow validation.", now)
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
		if err := finishValidationJobTx(tx, job.ID, job.ClaimToken, jobStatus, safeMessage, now); err != nil {
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

// lockDomainValidationStateTx follows the same global mutation order as
// validation creation and Microsoft result commits: root, subtype, then job.
// Besides avoiding a subtype/job deadlock, binding the job to the supplied
// resource prevents one worker claim from applying a result to another domain.
func lockDomainValidationStateTx(tx *gorm.DB, jobID uint, resourceID uint, claimToken string) (*EmailResourceModel, *DomainResourceModel, *ResourceValidationModel, error) {
	var root EmailResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", resourceID, string(domain.ResourceTypeDomain)).
		First(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, domain.ErrResourceNotFound
		}
		return nil, nil, nil, fmt.Errorf("lock domain resource root for validation: %w", err)
	}

	var dr DomainResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", resourceID).
		First(&dr).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, domain.ErrResourceNotFound
		}
		return nil, nil, nil, fmt.Errorf("lock domain resource for validation: %w", err)
	}

	var job ResourceValidationModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			"id = ? AND resource_id = ? AND resource_type = ? AND status = ? AND claim_token = ?",
			jobID,
			resourceID,
			string(domain.ResourceTypeDomain),
			string(domain.ResourceValidationRunning),
			claimToken,
		).
		First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, domain.ErrInvalidResourceStatus
		}
		return nil, nil, nil, fmt.Errorf("lock domain validation job: %w", err)
	}
	return &root, &dr, &job, nil
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

func rejectedMicrosoftBindingValidationSystemLog(job *ResourceValidationModel) *governancedomain.SystemLog {
	if job == nil {
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
		Detail:    "binding: " + coreapp.MicrosoftValidationBindingRejectedMessage,
	}
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

// Explicit selections skip resources that are unavailable or outside the
// caller's ownership instead of letting one stale ID poison the whole batch.
func selectAvailableValidationCandidatesByIDs(ctx context.Context, tx *gorm.DB, ownerUserID uint, ids []uint, allowBinding bool) ([]validationCandidateRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []validationCandidateRow
	query := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, COALESCE(ms.credential_revision, 0) AS credential_revision, COALESCE(ms.status, '') AS microsoft_status, COALESCE(dr.status, '') AS domain_status").
		Joins("LEFT JOIN microsoft_resources AS ms ON ms.id = er.id AND er.type = ?", string(domain.ResourceTypeMicrosoft)).
		Joins("LEFT JOIN domain_resources AS dr ON dr.id = er.id AND er.type = ?", string(domain.ResourceTypeDomain)).
		Where("er.id IN ? AND er.owner_user_id = ?", ids, ownerUserID).
		Order("er.id ASC")
	if !allowBinding {
		query = query.Where("er.type <> ? OR dr.purpose <> ?", string(domain.ResourceTypeDomain), string(domain.PurposeBinding))
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select validation resources: %w", err)
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

func selectValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) ([]validationCandidateRow, error) {
	switch filter.ResourceType {
	case domain.ResourceTypeMicrosoft:
		return selectMicrosoftValidationCandidatesByFilter(ctx, tx, ownerUserID, filter)
	case domain.ResourceTypeDomain:
		return selectDomainValidationCandidatesByFilter(ctx, tx, ownerUserID, filter)
	default:
		return nil, domain.ErrInvalidResourceType
	}
}

func selectMicrosoftValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) ([]validationCandidateRow, error) {
	q := microsoftValidationCandidateQuery(ctx, tx, ownerUserID, filter).
		Select("er.id, er.type AS resource_type, er.owner_user_id, ms.credential_revision AS credential_revision, ms.status AS microsoft_status, '' AS domain_status")

	var rows []validationCandidateRow
	q = q.Order("er.id ASC")
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select microsoft validation resources: %w", err)
	}
	return validateValidationCandidateRows(rows)
}

func microsoftValidationCandidateQuery(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) *gorm.DB {
	q := tx.WithContext(ctx).
		Table("email_resources AS er").
		Joins("JOIN microsoft_resources AS ms ON ms.id = er.id").
		Where("er.owner_user_id = ? AND er.type = ?", ownerUserID, string(domain.ResourceTypeMicrosoft)).
		Where("ms.status NOT IN ?", []string{string(domain.MicrosoftStatusDeleted), string(domain.MicrosoftStatusDisabled)})

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
	return q
}

func selectDomainValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) ([]validationCandidateRow, error) {
	q := domainValidationCandidateQuery(ctx, tx, ownerUserID, filter).
		Select("er.id, er.type AS resource_type, er.owner_user_id, 0 AS credential_revision, '' AS microsoft_status, dr.status AS domain_status")

	var rows []validationCandidateRow
	q = q.Order("er.id ASC")
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select domain validation resources: %w", err)
	}
	return validateValidationCandidateRows(rows)
}

func domainValidationCandidateQuery(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) *gorm.DB {
	q := tx.WithContext(ctx).
		Table("email_resources AS er").
		Joins("JOIN domain_resources AS dr ON dr.id = er.id").
		Where("er.owner_user_id = ? AND er.type = ?", ownerUserID, string(domain.ResourceTypeDomain)).
		Where("dr.owner_user_id = ?", ownerUserID).
		Where("dr.status NOT IN ?", []string{string(domain.DomainStatusDeleted), string(domain.DomainStatusDisabled)})
	if filter.ExcludeBinding {
		q = q.Where("dr.purpose <> ?", string(domain.PurposeBinding))
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
	return q
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

func markAbnormalMicrosoftCandidatesPendingTx(ctx context.Context, tx *gorm.DB, candidates []validationCandidateRow) (int, error) {
	ids := make([]uint, 0)
	for _, candidate := range candidates {
		if domain.ResourceType(candidate.ResourceType) == domain.ResourceTypeMicrosoft &&
			domain.MicrosoftResourceStatus(candidate.MicrosoftStatus) == domain.MicrosoftStatusAbnormal {
			ids = append(ids, candidate.ID)
		}
	}
	updated := 0
	now := time.Now().UTC()
	for start := 0; start < len(ids); start += resourceValidationInsertSize {
		end := start + resourceValidationInsertSize
		if end > len(ids) {
			end = len(ids)
		}
		result := tx.WithContext(ctx).
			Model(&MicrosoftResourceModel{}).
			Where("id IN ? AND status = ?", ids[start:end], string(domain.MicrosoftStatusAbnormal)).
			Updates(map[string]interface{}{
				"status":          string(domain.MicrosoftStatusPending),
				"last_safe_error": "",
				"updated_at":      now,
			})
		if result.Error != nil {
			return updated, fmt.Errorf("mark microsoft resources pending for validation: %w", result.Error)
		}
		updated += int(result.RowsAffected)
	}
	return updated, nil
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
