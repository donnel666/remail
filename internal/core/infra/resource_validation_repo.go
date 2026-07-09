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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ResourceValidationModel struct {
	ID            uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID    uint       `gorm:"not null;column:resource_id"`
	ResourceType  string     `gorm:"type:varchar(32);not null;column:resource_type"`
	OwnerUserID   uint       `gorm:"not null;column:owner_user_id"`
	Status        string     `gorm:"type:varchar(32);not null;default:'queued'"`
	Attempts      int        `gorm:"not null;default:0"`
	MaxAttempts   int        `gorm:"not null;default:3;column:max_attempts"`
	LastSafeError string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID     string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path          string     `gorm:"type:varchar(255);not null;default:''"`
	StartedAt     *time.Time `gorm:"column:started_at"`
	FinishedAt    *time.Time `gorm:"column:finished_at"`
	CreatedAt     time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"not null;autoUpdateTime"`
}

func (ResourceValidationModel) TableName() string {
	return "resource_validation_jobs"
}

func validationModel(job *domain.ResourceValidation) *ResourceValidationModel {
	return &ResourceValidationModel{
		ID:            job.ID,
		ResourceID:    job.ResourceID,
		ResourceType:  string(job.ResourceType),
		OwnerUserID:   job.OwnerUserID,
		Status:        string(job.Status),
		Attempts:      job.Attempts,
		MaxAttempts:   normalizeValidationMaxAttempts(job.MaxAttempts),
		LastSafeError: job.LastSafeError,
		RequestID:     job.RequestID,
		Path:          job.Path,
		StartedAt:     job.StartedAt,
		FinishedAt:    job.FinishedAt,
		CreatedAt:     job.CreatedAt,
		UpdatedAt:     job.UpdatedAt,
	}
}

func (m *ResourceValidationModel) toDomain() *domain.ResourceValidation {
	return &domain.ResourceValidation{
		ID:            m.ID,
		ResourceID:    m.ResourceID,
		ResourceType:  domain.ResourceType(m.ResourceType),
		OwnerUserID:   m.OwnerUserID,
		Status:        domain.ResourceValidationStatus(m.Status),
		Attempts:      m.Attempts,
		MaxAttempts:   normalizeValidationMaxAttempts(m.MaxAttempts),
		LastSafeError: m.LastSafeError,
		RequestID:     m.RequestID,
		Path:          m.Path,
		StartedAt:     m.StartedAt,
		FinishedAt:    m.FinishedAt,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

type ResourceValidationRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
}

const (
	resourceValidationBatchInsertSize   = 1000
	resourceValidationCandidatePageSize = 1000
)

func NewResourceValidationRepo(db *gorm.DB) *ResourceValidationRepo {
	return &ResourceValidationRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
}

func (r *ResourceValidationRepo) CreateWithLog(ctx context.Context, job *domain.ResourceValidation, log *governancedomain.OperationLog) (bool, error) {
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, err := findActiveValidationJobTx(tx, job.ResourceID)
		if err != nil {
			return err
		}
		if existing != nil {
			*job = *existing
			return nil
		}
		if err := prepareResourceForValidationTx(tx, job); err != nil {
			return err
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
	})
	return created, err
}

func (r *ResourceValidationRepo) CreateBatchWithLog(ctx context.Context, ownerUserID uint, selection coreapp.ResourceBulkSelection, log *governancedomain.OperationLog, requestID, path string) (*coreapp.ResourceBatchValidationResult, error) {
	result := &coreapp.ResourceBatchValidationResult{}
	switch selection.Mode {
	case coreapp.ResourceBulkSelectionIDs:
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			candidates, err := selectValidationCandidatesByIDs(ctx, tx, ownerUserID, selection.ResourceIDs)
			if err != nil {
				return err
			}
			if err := createValidationCandidateJobsTx(ctx, tx, candidates, requestID, path, result); err != nil {
				return err
			}
			if log != nil {
				if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
					return fmt.Errorf("create operation log: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	case coreapp.ResourceBulkSelectionFilter:
		afterID := uint(0)
		for {
			candidateCount := 0
			err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				candidates, err := selectValidationCandidatesByFilter(ctx, tx, ownerUserID, selection.Filter, afterID, resourceValidationCandidatePageSize)
				if err != nil {
					return err
				}
				candidateCount = len(candidates)
				if len(candidates) == 0 {
					return nil
				}
				if err := createValidationCandidateJobsTx(ctx, tx, candidates, requestID, path, result); err != nil {
					return err
				}
				afterID = candidates[len(candidates)-1].ID
				return nil
			})
			if err != nil {
				return nil, err
			}
			if candidateCount == 0 {
				break
			}
		}
		if log != nil {
			log.SafeSummary = resourceValidationBulkSummary(log.SafeSummary, result.Requested, result.Created)
			if err := r.operationLogs.Create(ctx, log); err != nil {
				return nil, fmt.Errorf("create operation log: %w", err)
			}
		}
	default:
		return nil, domain.ErrInvalidResourceType
	}
	return result, nil
}

func resourceValidationBulkSummary(summary string, requested int, created int) string {
	trimmed := strings.TrimRight(strings.TrimSpace(summary), ".")
	if trimmed == "" {
		trimmed = "Resource validation batch submitted"
	}
	return fmt.Sprintf("%s requested=%d created=%d.", trimmed, requested, created)
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
			ResourceID:   candidate.ID,
			ResourceType: candidate.ResourceType,
			OwnerUserID:  candidate.OwnerUserID,
			Status:       string(domain.ResourceValidationQueued),
			MaxAttempts:  domain.ResourceValidationDefaultMaxAttempts,
			RequestID:    strings.TrimSpace(requestID),
			Path:         strings.TrimSpace(path),
			CreatedAt:    now,
			UpdatedAt:    now,
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

func (r *ResourceValidationRepo) ClaimDispatchable(ctx context.Context, limit int, staleBefore time.Time) ([]domain.ResourceValidation, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []ResourceValidationModel
	err := r.db.WithContext(ctx).
		Where("(status = ? OR (status = ? AND updated_at < ?)) AND attempts < max_attempts",
			string(domain.ResourceValidationQueued),
			string(domain.ResourceValidationRunning),
			staleBefore,
		).
		Order("id ASC").
		Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("claim dispatchable resource validation jobs: %w", err)
	}
	result := make([]domain.ResourceValidation, len(models))
	for i := range models {
		result[i] = *models[i].toDomain()
	}
	return result, nil
}

func (r *ResourceValidationRepo) MarkRunning(ctx context.Context, id uint) (bool, error) {
	now := time.Now().UTC()
	staleBefore := now.Add(-10 * time.Minute)
	result := r.db.WithContext(ctx).
		Model(&ResourceValidationModel{}).
		Where("id = ? AND attempts < max_attempts AND (status = ? OR (status = ? AND updated_at < ?))",
			id,
			string(domain.ResourceValidationQueued),
			string(domain.ResourceValidationRunning),
			staleBefore,
		).
		Updates(map[string]interface{}{
			"status":          string(domain.ResourceValidationRunning),
			"attempts":        gorm.Expr("CASE WHEN status = ? AND updated_at < ? THEN attempts + 1 ELSE attempts END", string(domain.ResourceValidationRunning), staleBefore),
			"last_safe_error": "",
			"started_at":      now,
			"updated_at":      now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark resource validation running: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *ResourceValidationRepo) MarkFailed(ctx context.Context, id uint, safeError string) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := finishValidationJobTx(tx, id, string(domain.ResourceValidationFailed), safeValidationMessage(safeError), now); err != nil {
			return err
		}
		return nil
	})
}

func (r *ResourceValidationRepo) MarkRetryableFailure(ctx context.Context, id uint, safeError string) (bool, error) {
	now := time.Now().UTC()
	exhausted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job ResourceValidationModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ?", id, string(domain.ResourceValidationRunning)).
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
			"last_safe_error": safeValidationMessage(safeError),
			"updated_at":      now,
		}
		if nextAttempts >= job.MaxAttempts {
			exhausted = true
			updates["status"] = string(domain.ResourceValidationFailed)
			updates["finished_at"] = now
		}
		result := tx.Model(&ResourceValidationModel{}).
			Where("id = ? AND status = ?", id, string(domain.ResourceValidationRunning)).
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

func (r *ResourceValidationRepo) ApplyMicrosoftResult(ctx context.Context, jobID uint, resourceID uint, result coreapp.MicrosoftValidationResult, systemLog *governancedomain.SystemLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := lockRunningValidationJob(tx, jobID); err != nil {
			return err
		}
		var ms MicrosoftResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ms, resourceID).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return markValidationJobFailedTx(tx, jobID, "Resource not found.", now)
			}
			return fmt.Errorf("lock microsoft resource for validation: %w", err)
		}
		switch domain.MicrosoftResourceStatus(ms.Status) {
		case domain.MicrosoftStatusDeleted:
			return markValidationJobFailedTx(tx, jobID, "Resource not found.", now)
		case domain.MicrosoftStatusDisabled:
			return markValidationJobFailedTx(tx, jobID, "Resource status does not allow validation.", now)
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
			if strings.TrimSpace(result.ClientID) != "" {
				updates["client_id"] = strings.TrimSpace(result.ClientID)
			}
			if strings.TrimSpace(result.RefreshToken) != "" {
				updates["refresh_token"] = strings.TrimSpace(result.RefreshToken)
			}
			if result.RTExpireAt != nil {
				updates["rt_expire_at"] = result.RTExpireAt
			}
			updates["graph_available"] = result.GraphAvailable
		}
		if err := tx.Model(&MicrosoftResourceModel{}).
			Where("id = ? AND status <> ?", resourceID, string(domain.MicrosoftStatusDeleted)).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("apply microsoft validation result: %w", err)
		}
		if err := finishValidationJobTx(tx, jobID, jobStatus, safeMessage, now); err != nil {
			return err
		}
		return createSystemLogInTx(ctx, tx, systemLog)
	})
}

func (r *ResourceValidationRepo) ApplyDomainResult(ctx context.Context, jobID uint, resourceID uint, result coreapp.DomainValidationResult, systemLog *governancedomain.SystemLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := lockRunningValidationJob(tx, jobID); err != nil {
			return err
		}
		var dr DomainResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dr, resourceID).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return markValidationJobFailedTx(tx, jobID, "Resource not found.", now)
			}
			return fmt.Errorf("lock domain resource for validation: %w", err)
		}
		switch domain.MailDomainStatus(dr.Status) {
		case domain.DomainStatusDeleted:
			return markValidationJobFailedTx(tx, jobID, "Resource not found.", now)
		case domain.DomainStatusDisabled:
			return markValidationJobFailedTx(tx, jobID, "Resource status does not allow validation.", now)
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
		if err := finishValidationJobTx(tx, jobID, jobStatus, safeMessage, now); err != nil {
			return err
		}
		return createSystemLogInTx(ctx, tx, systemLog)
	})
}

func (r *ResourceValidationRepo) MarkDispatchFailed(ctx context.Context, id uint, safeError string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&ResourceValidationModel{}).
		Where("id = ? AND status IN ?", id, []string{string(domain.ResourceValidationQueued), string(domain.ResourceValidationRunning)}).
		Updates(map[string]interface{}{
			"last_safe_error": safeValidationMessage(safeError),
			"updated_at":      now,
		}).Error
	if err != nil {
		return fmt.Errorf("mark resource validation dispatch failed: %w", err)
	}
	return nil
}

func lockRunningValidationJob(tx *gorm.DB, id uint) error {
	var job ResourceValidationModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = ?", id, string(domain.ResourceValidationRunning)).
		First(&job).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrInvalidResourceStatus
		}
		return fmt.Errorf("lock resource validation job: %w", err)
	}
	return nil
}

func markValidationJobFailedTx(tx *gorm.DB, jobID uint, safeError string, now time.Time) error {
	return finishValidationJobTx(tx, jobID, string(domain.ResourceValidationFailed), safeValidationMessage(safeError), now)
}

func finishValidationJobTx(tx *gorm.DB, jobID uint, status string, safeError string, now time.Time) error {
	result := tx.Model(&ResourceValidationModel{}).
		Where("id = ? AND status = ?", jobID, string(domain.ResourceValidationRunning)).
		Updates(map[string]interface{}{
			"status":          status,
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

type validationCandidateRow struct {
	ID              uint
	ResourceType    string `gorm:"column:resource_type"`
	OwnerUserID     uint   `gorm:"column:owner_user_id"`
	MicrosoftStatus string `gorm:"column:microsoft_status"`
	DomainStatus    string `gorm:"column:domain_status"`
}

func selectValidationCandidatesByIDs(ctx context.Context, tx *gorm.DB, ownerUserID uint, ids []uint) ([]validationCandidateRow, error) {
	if len(ids) == 0 {
		return nil, domain.ErrResourceNotFound
	}
	var rows []validationCandidateRow
	if err := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, COALESCE(ms.status, '') AS microsoft_status, COALESCE(dr.status, '') AS domain_status").
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

func selectValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter, afterID uint, limit int) ([]validationCandidateRow, error) {
	switch filter.ResourceType {
	case domain.ResourceTypeMicrosoft:
		return selectMicrosoftValidationCandidatesByFilter(ctx, tx, ownerUserID, filter, afterID, limit)
	case domain.ResourceTypeDomain:
		return selectDomainValidationCandidatesByFilter(ctx, tx, ownerUserID, filter, afterID, limit)
	default:
		return nil, domain.ErrInvalidResourceType
	}
}

func selectMicrosoftValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter, afterID uint, limit int) ([]validationCandidateRow, error) {
	q := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, ms.status AS microsoft_status, '' AS domain_status").
		Joins("JOIN microsoft_resources AS ms ON ms.id = er.id").
		Where("er.owner_user_id = ? AND er.type = ?", ownerUserID, string(domain.ResourceTypeMicrosoft)).
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
		like := "%" + strings.ToLower(strings.TrimSpace(filter.Search)) + "%"
		normalized := "%" + normalizeEmailTypeSearch(filter.Search) + "%"
		q = q.Where(
			"(LOWER(ms.email_address) LIKE ? OR LOWER(SUBSTRING_INDEX(ms.email_address, '@', -1)) LIKE ? OR LOWER(REPLACE(REPLACE(SUBSTRING_INDEX(ms.email_address, '@', -1), '.', '_'), '-', '_')) LIKE ?)",
			like,
			like,
			normalized,
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

func selectDomainValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter, afterID uint, limit int) ([]validationCandidateRow, error) {
	q := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, '' AS microsoft_status, dr.status AS domain_status").
		Joins("JOIN domain_resources AS dr ON dr.id = er.id").
		Where("er.owner_user_id = ? AND er.type = ?", ownerUserID, string(domain.ResourceTypeDomain)).
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
		q = q.Where("LOWER(dr.domain) LIKE ?", "%"+strings.ToLower(strings.TrimSpace(filter.Search))+"%")
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
