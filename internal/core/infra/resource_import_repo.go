package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ResourceImportModel is the GORM model for resource_imports.
type ResourceImportModel struct {
	ID               uint      `gorm:"primaryKey;autoIncrement"`
	OwnerUserID      uint      `gorm:"not null;column:owner_user_id"`
	ResourceType     string    `gorm:"type:varchar(32);not null;column:resource_type"`
	SourceObjectKey  string    `gorm:"type:varchar(500);not null;column:source_object_key"`
	FailureObjectKey string    `gorm:"type:varchar(500);not null;default:'';column:failure_object_key"`
	Status           string    `gorm:"type:varchar(32);not null;default:'processing'"`
	ImportedCount    int       `gorm:"not null;default:0;column:imported_count"`
	LastSafeError    string    `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	CreatedAt        time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt        time.Time `gorm:"not null;autoUpdateTime"`
}

func (ResourceImportModel) TableName() string {
	return "resource_imports"
}

func fromResourceImportDomain(item *domain.ResourceImport) *ResourceImportModel {
	return &ResourceImportModel{
		ID:               item.ID,
		OwnerUserID:      item.OwnerUserID,
		ResourceType:     string(item.ResourceType),
		SourceObjectKey:  item.SourceObjectKey,
		FailureObjectKey: item.FailureObjectKey,
		Status:           string(item.Status),
		ImportedCount:    item.ImportedCount,
		LastSafeError:    item.LastSafeError,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

func (m *ResourceImportModel) toDomain() *domain.ResourceImport {
	return &domain.ResourceImport{
		ID:               m.ID,
		OwnerUserID:      m.OwnerUserID,
		ResourceType:     domain.ResourceType(m.ResourceType),
		SourceObjectKey:  m.SourceObjectKey,
		FailureObjectKey: m.FailureObjectKey,
		Status:           domain.ResourceImportStatus(m.Status),
		ImportedCount:    m.ImportedCount,
		LastSafeError:    m.LastSafeError,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}

// ResourceImportRepo persists resource import metadata.
type ResourceImportRepo struct {
	db *gorm.DB
}

// NewResourceImportRepo creates a GORM-backed resource import repository.
func NewResourceImportRepo(db *gorm.DB) *ResourceImportRepo {
	return &ResourceImportRepo{db: db}
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
	err := r.db.WithContext(ctx).
		Model(&ResourceImportModel{}).
		Where("id = ? AND status = ?", id, string(domain.ResourceImportProcessing)).
		Updates(map[string]interface{}{
			"status":             string(domain.ResourceImportFailed),
			"failure_object_key": failureObjectKey,
			"last_safe_error":    safeError,
			"updated_at":         time.Now(),
		}).Error
	if err != nil {
		return fmt.Errorf("mark resource import failed: %w", err)
	}
	return nil
}

func (r *ResourceImportRepo) CreateMicrosoftResourcesAndMarkSucceeded(ctx context.Context, id uint, resources []domain.EmailResource, ms []domain.MicrosoftResource, failureObjectKey string, safeSummary string, afterCreate func(context.Context, []domain.MicrosoftResource, []uint) error) ([]uint, error) {
	if len(resources) != len(ms) {
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
		var chunkIDs []uint
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			importModel, err := lockProcessingResourceImportTx(tx, id)
			if err != nil {
				return err
			}
			if importModel == nil {
				return nil
			}

			if err := createMicrosoftBatchTx(tx, chunkResources, chunkMicrosoft); err != nil {
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
		importModel, err := lockProcessingResourceImportTx(tx, id)
		if err != nil {
			return err
		}
		if importModel == nil {
			return nil
		}

		now := time.Now()
		if err := tx.Model(&ResourceImportModel{}).
			Where("id = ? AND status = ?", id, string(domain.ResourceImportProcessing)).
			Updates(map[string]interface{}{
				"status":             string(domain.ResourceImportImported),
				"imported_count":     len(ms),
				"failure_object_key": failureObjectKey,
				"last_safe_error":    safeSummary,
				"updated_at":         now,
			}).Error; err != nil {
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

func lockProcessingResourceImportTx(tx *gorm.DB, id uint) (*ResourceImportModel, error) {
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
	models, err := findMicrosoftResourceModelsByEmails(tx, emails, false, false)
	if err != nil {
		return nil, fmt.Errorf("find imported microsoft resource ids: %w", err)
	}
	if len(models) != len(uniqueMicrosoftEmails(emails)) {
		return nil, domain.ErrResourceNotFound
	}
	ids := make([]uint, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids, nil
}
