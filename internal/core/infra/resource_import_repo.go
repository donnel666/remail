package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ResourceImportModel is the GORM model for resource_imports.
type ResourceImportModel struct {
	ID                 uint       `gorm:"primaryKey;autoIncrement"`
	OwnerUserID        uint       `gorm:"not null;column:owner_user_id"`
	OperatorUserID     *uint      `gorm:"column:operator_user_id"`
	ResourceType       string     `gorm:"type:varchar(32);not null;column:resource_type"`
	LongLived          bool       `gorm:"not null;default:false;column:long_lived"`
	ErrorStrategy      string     `gorm:"type:varchar(16);not null;default:'skip';column:error_strategy"`
	SourceObjectKey    string     `gorm:"type:varchar(500);not null;column:source_object_key"`
	FailureObjectKey   string     `gorm:"type:varchar(500);not null;default:'';column:failure_object_key"`
	Status             string     `gorm:"type:varchar(32);not null;default:'processing'"`
	ImportedCount      int        `gorm:"not null;default:0;column:imported_count"`
	AcceptedCount      int        `gorm:"not null;default:0;column:accepted_count"`
	SkippedCount       int        `gorm:"not null;default:0;column:skipped_count"`
	LastSafeError      string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID          string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path               string     `gorm:"type:varchar(255);not null;default:''"`
	IdempotencyKey     string     `gorm:"type:varchar(128);not null;default:'';column:idempotency_key"`
	RequestFingerprint string     `gorm:"type:char(64);not null;default:'';column:request_fingerprint"`
	DispatchStatus     string     `gorm:"type:varchar(32);not null;default:'pending';column:dispatch_status"`
	Generation         uint64     `gorm:"not null;default:1"`
	Attempts           int        `gorm:"not null;default:0"`
	MaxAttempts        int        `gorm:"not null;default:3;column:max_attempts"`
	ClaimToken         string     `gorm:"type:char(36);not null;default:'';column:claim_token"`
	StartedAt          *time.Time `gorm:"column:started_at"`
	FinishedAt         *time.Time `gorm:"column:finished_at"`
	CreatedAt          time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt          time.Time  `gorm:"not null;autoUpdateTime"`
}

func (ResourceImportModel) TableName() string {
	return "resource_imports"
}

type ResourceImportItemModel struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement"`
	ImportID      uint      `gorm:"not null;column:import_id"`
	ResourceID    *uint     `gorm:"column:resource_id"`
	LineNumber    int       `gorm:"not null;column:line_number"`
	Outcome       string    `gorm:"type:varchar(32);not null"`
	Category      string    `gorm:"type:varchar(64);not null;default:''"`
	LastSafeError string    `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	CreatedAt     time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (ResourceImportItemModel) TableName() string { return "resource_import_items" }

func fromResourceImportDomain(item *domain.ResourceImport) *ResourceImportModel {
	dispatchStatus := item.DispatchStatus
	if dispatchStatus == "" {
		dispatchStatus = "pending"
	}
	maxAttempts := item.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	errorStrategy, ok := domain.NormalizeImportErrorStrategy(string(item.ErrorStrategy))
	if !ok {
		errorStrategy = domain.ImportErrorStrategySkip
	}
	generation := item.Generation
	if generation == 0 {
		generation = 1
	}
	return &ResourceImportModel{
		ID:               item.ID,
		OwnerUserID:      item.OwnerUserID,
		ResourceType:     string(item.ResourceType),
		LongLived:        item.LongLived,
		ErrorStrategy:    string(errorStrategy),
		SourceObjectKey:  item.SourceObjectKey,
		FailureObjectKey: item.FailureObjectKey,
		Status:           string(item.Status),
		DispatchStatus:   dispatchStatus,
		MaxAttempts:      maxAttempts,
		Generation:       generation,
		ImportedCount:    item.ImportedCount,
		LastSafeError:    item.LastSafeError,
		RequestID:        item.RequestID,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

func (m *ResourceImportModel) toDomain() *domain.ResourceImport {
	operatorUserID := uint(0)
	if m.OperatorUserID != nil {
		operatorUserID = *m.OperatorUserID
	}
	return &domain.ResourceImport{
		ID:               m.ID,
		OwnerUserID:      m.OwnerUserID,
		OperatorUserID:   operatorUserID,
		ResourceType:     domain.ResourceType(m.ResourceType),
		LongLived:        m.LongLived,
		ErrorStrategy:    domain.ImportErrorStrategy(m.ErrorStrategy),
		SourceObjectKey:  m.SourceObjectKey,
		FailureObjectKey: m.FailureObjectKey,
		Status:           domain.ResourceImportStatus(m.Status),
		ImportedCount:    m.ImportedCount,
		AcceptedCount:    m.AcceptedCount,
		SkippedCount:     m.SkippedCount,
		DispatchStatus:   m.DispatchStatus,
		Attempts:         m.Attempts,
		MaxAttempts:      m.MaxAttempts,
		Generation:       m.Generation,
		ClaimToken:       m.ClaimToken,
		LastSafeError:    m.LastSafeError,
		RequestID:        m.RequestID,
		StartedAt:        m.StartedAt,
		FinishedAt:       m.FinishedAt,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}

// ResourceImportRepo persists resource import metadata.
type ResourceImportRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
}

// NewResourceImportRepo creates a GORM-backed resource import repository.
func NewResourceImportRepo(db *gorm.DB) *ResourceImportRepo {
	return &ResourceImportRepo{db: db, operationLogs: governanceinfra.NewOperationLogRepo(db)}
}

func (r *ResourceImportRepo) Create(ctx context.Context, item *domain.ResourceImport) error {
	model := fromResourceImportDomain(item)
	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("create resource import: %w", err)
	}
	item.ID = model.ID
	item.CreatedAt = model.CreatedAt
	item.UpdatedAt = model.UpdatedAt
	return nil
}

func (r *ResourceImportRepo) FindAdminByIdempotency(ctx context.Context, operatorUserID uint, idempotencyKey string) (*domain.ResourceImport, string, error) {
	var model ResourceImportModel
	err := r.db.WithContext(ctx).
		Where("operator_user_id = ? AND idempotency_key = ?", operatorUserID, idempotencyKey).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("find administrator resource import idempotency key: %w", err)
	}
	return model.toDomain(), model.RequestFingerprint, nil
}

func (r *ResourceImportRepo) SetAdminImportCounts(ctx context.Context, importID uint, claimToken string, accepted, skipped int) error {
	if accepted < 0 || skipped < 0 {
		return domain.ErrInvalidImportFormat
	}
	query := r.db.WithContext(ctx).Model(&ResourceImportModel{}).
		Where("id = ? AND status = ?", importID, string(domain.ResourceImportProcessing))
	if claimToken != "" {
		query = query.Where("dispatch_status = ? AND claim_token = ?", "running", claimToken)
	}
	result := query.
		Updates(map[string]any{
			"accepted_count": gorm.Expr("GREATEST(accepted_count, ?)", accepted),
			"skipped_count":  gorm.Expr("GREATEST(skipped_count, ?)", skipped),
			"updated_at":     time.Now().UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("update administrator resource import counts: %w", result.Error)
	}
	if claimToken != "" && result.RowsAffected == 0 {
		var count int64
		if err := r.db.WithContext(ctx).Model(&ResourceImportModel{}).
			Where("id = ? AND status = ? AND dispatch_status = ? AND claim_token = ?", importID, string(domain.ResourceImportProcessing), "running", claimToken).
			Count(&count).Error; err != nil {
			return fmt.Errorf("verify administrator resource import claim: %w", err)
		}
		if count == 0 {
			return domain.ErrResourceImportInvalidClaim
		}
	}
	return nil
}

func (r *ResourceImportRepo) ListAdminImportProcessedLines(ctx context.Context, importID uint) (map[int]struct{}, error) {
	var rows []struct {
		LineNumber int `gorm:"column:line_number"`
	}
	if err := r.db.WithContext(ctx).Model(&ResourceImportItemModel{}).
		Select("line_number").Where("import_id = ?", importID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list administrator resource import processed lines: %w", err)
	}
	result := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		result[row.LineNumber] = struct{}{}
	}
	return result, nil
}

func (r *ResourceImportRepo) ClaimAdminImportDispatchable(ctx context.Context, limit int, _ time.Time, _ time.Time) ([]coreapp.AdminResourceImportDispatchItem, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	var models []ResourceImportModel
	if err := r.db.WithContext(ctx).
		Where("status = ? AND dispatch_status = ?", string(domain.ResourceImportProcessing), "pending").
		Order("id ASC").Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list pending administrator resource imports: %w", err)
	}
	items := make([]coreapp.AdminResourceImportDispatchItem, len(models))
	for i := range models {
		items[i] = coreapp.AdminResourceImportDispatchItem{
			ImportID: models[i].ID, OwnerUserID: models[i].OwnerUserID,
			LongLived: models[i].LongLived, ErrorStrategy: domain.ImportErrorStrategy(models[i].ErrorStrategy),
			RequestID: models[i].RequestID, Generation: models[i].Generation,
		}
	}
	return items, nil
}

func (r *ResourceImportRepo) MarkAdminImportDispatched(ctx context.Context, importID uint, generation uint64) (bool, error) {
	if importID == 0 || generation == 0 {
		return false, domain.ErrResourceImportInvalidClaim
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&ResourceImportModel{}).
		Where("id = ? AND status = ? AND dispatch_status = ? AND generation = ?", importID, string(domain.ResourceImportProcessing), "pending", generation).
		Updates(map[string]any{"dispatch_status": "queued", "last_safe_error": "", "updated_at": now})
	if result.Error != nil {
		return false, fmt.Errorf("activate administrator resource import dispatch: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *ResourceImportRepo) MarkAdminImportRunning(ctx context.Context, importID uint, generation uint64) (string, bool, error) {
	if importID == 0 || generation == 0 {
		return "", false, domain.ErrResourceImportInvalidClaim
	}
	now := time.Now().UTC()
	claimToken := platform.NewUUIDV7String()
	result := r.db.WithContext(ctx).Model(&ResourceImportModel{}).
		Where("id = ? AND status = ? AND dispatch_status IN ? AND generation = ?", importID, string(domain.ResourceImportProcessing), []string{"pending", "queued"}, generation).
		Updates(map[string]any{
			"dispatch_status": "running", "claim_token": claimToken,
			"started_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return "", false, fmt.Errorf("mark administrator resource import running: %w", result.Error)
	}
	return claimToken, result.RowsAffected == 1, nil
}

func (r *ResourceImportRepo) MarkAdminImportPending(ctx context.Context, importID uint, generation uint64, safeError string) error {
	if importID == 0 || generation == 0 {
		return domain.ErrResourceImportInvalidClaim
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&ResourceImportModel{}).
		Where("id = ? AND status = ? AND dispatch_status IN ? AND generation = ?", importID, string(domain.ResourceImportProcessing), []string{"queued", "running"}, generation).
		Updates(map[string]any{
			"dispatch_status": "pending", "generation": gorm.Expr("generation + 1"),
			"claim_token":     "",
			"last_safe_error": safeError, "updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("return administrator resource import to pending: %w", result.Error)
	}
	return nil
}

func (r *ResourceImportRepo) MarkAdminImportFailed(ctx context.Context, importID uint, claimToken, failureObjectKey, safeError string) error {
	if importID == 0 || claimToken == "" {
		return domain.ErrResourceImportInvalidClaim
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model ResourceImportModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND dispatch_status = ? AND claim_token = ?", importID, string(domain.ResourceImportProcessing), "running", claimToken).
			First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceImportInvalidClaim
			}
			return fmt.Errorf("lock administrator resource import business failure: %w", err)
		}
		nextAttempts := model.Attempts + 1
		updates := map[string]any{
			"dispatch_status": "pending", "generation": model.Generation + 1,
			"attempts": nextAttempts, "claim_token": "", "failure_object_key": failureObjectKey,
			"last_safe_error": safeError, "updated_at": now,
		}
		if nextAttempts >= model.MaxAttempts {
			updates["status"] = string(domain.ResourceImportFailed)
			updates["dispatch_status"] = "failed"
			updates["finished_at"] = now
		}
		return tx.Model(&ResourceImportModel{}).
			Where("id = ? AND claim_token = ?", importID, claimToken).
			Updates(updates).Error
	})
}

func (r *ResourceImportRepo) CreateAdminWithLog(
	ctx context.Context,
	item *domain.ResourceImport,
	metadata coreapp.AdminResourceImportMetadata,
	log *governancedomain.OperationLog,
) (*domain.ResourceImport, bool, error) {
	var stored *domain.ResourceImport
	created := false
	err := withRetryableMySQLTransaction(ctx, r.db, func(tx *gorm.DB) error {
		stored = nil
		created = false
		var existing ResourceImportModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operator_user_id = ? AND idempotency_key = ?", metadata.OperatorUserID, metadata.IdempotencyKey).
			First(&existing).Error
		if err == nil {
			if existing.RequestFingerprint != metadata.RequestFingerprint {
				return domain.ErrResourceIdempotencyConflict
			}
			stored = existing.toDomain()
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lock administrator resource import idempotency key: %w", err)
		}
		operatorID := metadata.OperatorUserID
		model := &ResourceImportModel{
			OwnerUserID: item.OwnerUserID, OperatorUserID: &operatorID,
			ResourceType: string(item.ResourceType), LongLived: metadata.LongLived,
			ErrorStrategy:   string(metadata.ErrorStrategy),
			SourceObjectKey: item.SourceObjectKey, Status: string(item.Status),
			RequestID: metadata.RequestID, Path: metadata.Path,
			IdempotencyKey: metadata.IdempotencyKey, RequestFingerprint: metadata.RequestFingerprint,
			DispatchStatus: "pending", Generation: 1, MaxAttempts: 3,
		}
		if err := tx.Create(model).Error; err != nil {
			if isDuplicateKeyError(err) {
				var raced ResourceImportModel
				if findErr := tx.Where("operator_user_id = ? AND idempotency_key = ?", metadata.OperatorUserID, metadata.IdempotencyKey).
					First(&raced).Error; findErr != nil {
					return fmt.Errorf("reload administrator resource import idempotency key: %w", findErr)
				}
				if raced.RequestFingerprint != metadata.RequestFingerprint {
					return domain.ErrResourceIdempotencyConflict
				}
				stored = raced.toDomain()
				return nil
			}
			return fmt.Errorf("create administrator resource import: %w", err)
		}
		stored = model.toDomain()
		created = true
		if log != nil {
			log.ResourceID = fmt.Sprintf("import:%d", model.ID)
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
	return stored, created, err
}

func (r *ResourceImportRepo) FindByID(ctx context.Context, id uint) (*domain.ResourceImport, error) {
	var model ResourceImportModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find resource import: %w", err)
	}
	return model.toDomain(), nil
}

func (r *ResourceImportRepo) MarkFailed(ctx context.Context, id uint, failureObjectKey string, safeError string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Model(&ResourceImportModel{}).
		Where("id = ? AND status = ?", id, string(domain.ResourceImportProcessing)).
		Updates(map[string]interface{}{
			"status":             string(domain.ResourceImportFailed),
			"failure_object_key": failureObjectKey,
			"last_safe_error":    safeError,
			"dispatch_status":    "failed",
			"finished_at":        now,
			"updated_at":         now,
		}).Error
	if err != nil {
		return fmt.Errorf("mark resource import failed: %w", err)
	}
	return nil
}

func (r *ResourceImportRepo) CreateMicrosoftResourcesAndMarkSucceeded(
	ctx context.Context,
	id uint,
	claimToken string,
	lines []domain.MicrosoftImportLine,
	resources []domain.EmailResource,
	ms []domain.MicrosoftResource,
	skippedItems []coreapp.AdminResourceImportSkippedItem,
	failureObjectKey string,
	safeSummary string,
	afterCreate func(context.Context, []domain.MicrosoftResource, []uint) error,
) ([]uint, error) {
	if len(resources) != len(ms) || len(lines) != len(ms) {
		return nil, fmt.Errorf("create microsoft resources and mark import succeeded: resource count mismatch")
	}

	importedResourceIDs := make([]uint, 0, len(ms))
	alreadyTerminal, err := r.resourceImportAlreadyTerminal(ctx, id)
	if err != nil {
		return nil, err
	}
	if alreadyTerminal {
		return importedResourceIDs, nil
	}

	for start := 0; start < len(ms); start += resourceImportInsertBatchSize {
		end := start + resourceImportInsertBatchSize
		if end > len(ms) {
			end = len(ms)
		}
		chunkResources := resources[start:end]
		chunkMicrosoft := ms[start:end]
		chunkLines := lines[start:end]
		var chunkIDs []uint
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			importModel, err := lockProcessingResourceImportTx(tx, id, claimToken)
			if err != nil {
				return err
			}
			if importModel == nil {
				return nil
			}

			restored, err := createMicrosoftBatchTx(tx, chunkResources, chunkMicrosoft)
			if err != nil {
				return err
			}
			chunkIDs, err = findMicrosoftResourceIDsByImportRowsTx(tx, chunkMicrosoft)
			if err != nil {
				return err
			}
			if afterCreate != nil {
				if err := afterCreate(platform.WithGormTx(ctx, tx), chunkMicrosoft, chunkIDs); err != nil {
					return fmt.Errorf("finalize imported microsoft resources: %w", err)
				}
			}
			items := make([]ResourceImportItemModel, 0, len(chunkLines))
			for i := range chunkLines {
				if chunkLines[i].LineNumber <= 0 || i >= len(chunkIDs) {
					return domain.ErrInvalidImportFormat
				}
				resourceID := chunkIDs[i]
				outcome := "imported"
				if _, ok := restored[microsoftEmailKey(chunkMicrosoft[i].EmailAddress)]; ok {
					outcome = "restored"
				}
				items = append(items, ResourceImportItemModel{
					ImportID: id, ResourceID: &resourceID, LineNumber: chunkLines[i].LineNumber,
					Outcome: outcome,
				})
			}
			if len(items) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&items).Error; err != nil {
					return fmt.Errorf("record imported resource items: %w", err)
				}
			}
			if err := tx.Model(&ResourceImportModel{}).
				Where("id = ? AND status = ?", id, string(domain.ResourceImportProcessing)).
				Updates(map[string]interface{}{
					"imported_count": gorm.Expr("imported_count + ?", len(chunkMicrosoft)),
					"updated_at":     time.Now(),
				}).Error; err != nil {
				return fmt.Errorf("update resource import progress: %w", err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		importedResourceIDs = append(importedResourceIDs, chunkIDs...)
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		importModel, err := lockProcessingResourceImportTx(tx, id, claimToken)
		if err != nil {
			return err
		}
		if importModel == nil {
			return nil
		}

		if len(skippedItems) > 0 {
			models := make([]ResourceImportItemModel, 0, len(skippedItems))
			for _, item := range skippedItems {
				if item.LineNumber <= 0 {
					continue
				}
				models = append(models, ResourceImportItemModel{
					ImportID: id, LineNumber: item.LineNumber, Outcome: "skipped",
					Category: item.Category, LastSafeError: item.SafeError,
				})
			}
			if len(models) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models).Error; err != nil {
					return fmt.Errorf("record skipped resource import items: %w", err)
				}
			}
		}
		var counts struct {
			Imported int `gorm:"column:imported"`
			Skipped  int `gorm:"column:skipped"`
		}
		if err := tx.Model(&ResourceImportItemModel{}).
			Select("COALESCE(SUM(CASE WHEN outcome IN ('imported', 'restored') THEN 1 ELSE 0 END), 0) AS imported, COALESCE(SUM(CASE WHEN outcome = 'skipped' THEN 1 ELSE 0 END), 0) AS skipped").
			Where("import_id = ?", id).Scan(&counts).Error; err != nil {
			return fmt.Errorf("count resource import items: %w", err)
		}

		now := time.Now().UTC()
		updates := map[string]interface{}{
			"status":             string(domain.ResourceImportImported),
			"imported_count":     counts.Imported,
			"accepted_count":     gorm.Expr("GREATEST(accepted_count, ?)", counts.Imported),
			"skipped_count":      counts.Skipped,
			"failure_object_key": failureObjectKey,
			"last_safe_error":    safeSummary,
			"dispatch_status":    "succeeded",
			"finished_at":        now,
			"updated_at":         now,
		}
		if claimToken != "" {
			updates["claim_token"] = ""
		}
		if err := tx.Model(&ResourceImportModel{}).
			Where("id = ? AND status = ?", id, string(domain.ResourceImportProcessing)).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("mark resource import succeeded: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return importedResourceIDs, nil
}

func (r *ResourceImportRepo) resourceImportAlreadyTerminal(ctx context.Context, id uint) (bool, error) {
	var terminal bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var importModel ResourceImportModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&importModel, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock resource import: %w", err)
		}
		switch domain.ResourceImportStatus(importModel.Status) {
		case domain.ResourceImportImported, domain.ResourceImportFailed:
			terminal = true
			return nil
		case domain.ResourceImportProcessing:
			terminal = false
			return nil
		default:
			return domain.ErrInvalidResourceStatus
		}
	})
	if err != nil {
		return false, err
	}
	return terminal, nil
}

func lockProcessingResourceImportTx(tx *gorm.DB, id uint, claimToken string) (*ResourceImportModel, error) {
	var importModel ResourceImportModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&importModel, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrResourceNotFound
		}
		return nil, fmt.Errorf("lock resource import: %w", err)
	}
	switch domain.ResourceImportStatus(importModel.Status) {
	case domain.ResourceImportImported, domain.ResourceImportFailed:
		return nil, nil
	case domain.ResourceImportProcessing:
		if claimToken != "" && (importModel.DispatchStatus != "running" || importModel.ClaimToken != claimToken) {
			return nil, domain.ErrResourceImportInvalidClaim
		}
		return &importModel, nil
	default:
		return nil, domain.ErrInvalidResourceStatus
	}
}

func findMicrosoftResourceIDsByImportRowsTx(tx *gorm.DB, rows []domain.MicrosoftResource) ([]uint, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	emails := make([]string, 0, len(rows))
	for _, row := range rows {
		emails = append(emails, row.EmailAddress)
	}
	models, err := findMicrosoftResourceModelsByEmails(tx, emails, false)
	if err != nil {
		return nil, fmt.Errorf("find imported microsoft resource ids: %w", err)
	}
	if len(models) != len(uniqueMicrosoftEmails(emails)) {
		return nil, domain.ErrResourceNotFound
	}
	idsByEmail := make(map[string]uint, len(models))
	for _, model := range models {
		idsByEmail[microsoftEmailKey(model.EmailAddress)] = model.ID
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		id, ok := idsByEmail[microsoftEmailKey(row.EmailAddress)]
		if !ok {
			return nil, domain.ErrResourceNotFound
		}
		ids = append(ids, id)
	}
	return ids, nil
}
