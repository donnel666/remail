package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GeneratedMailboxModel is the GORM model for the generated_mailboxes table.
type GeneratedMailboxModel struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID      uint       `gorm:"not null;column:resource_id"`
	OwnerUserID     uint       `gorm:"not null;column:owner_user_id"`
	Email           string     `gorm:"type:varchar(255);not null"`
	Status          string     `gorm:"type:varchar(32);not null;default:'normal'"`
	LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime"`
}

func (GeneratedMailboxModel) TableName() string {
	return "generated_mailboxes"
}

func (m *GeneratedMailboxModel) toDomain() *domain.GeneratedMailbox {
	return &domain.GeneratedMailbox{
		ID:              m.ID,
		ResourceID:      m.ResourceID,
		OwnerUserID:     m.OwnerUserID,
		Email:           m.Email,
		Status:          domain.GeneratedMailboxStatus(m.Status),
		LastAllocatedAt: m.LastAllocatedAt,
		CreatedAt:       m.CreatedAt,
	}
}

// GeneratedMailboxRepo implements app.GeneratedMailboxRepository.
type GeneratedMailboxRepo struct {
	db *gorm.DB
}

// NewGeneratedMailboxRepo creates a new GORM-backed generated mailbox repository.
func NewGeneratedMailboxRepo(db *gorm.DB) *GeneratedMailboxRepo {
	return &GeneratedMailboxRepo{db: db}
}

func (r *GeneratedMailboxRepo) List(ctx context.Context, domainResourceID uint, ownerUserID uint, offset, limit int) ([]domain.GeneratedMailbox, error) {
	var models []GeneratedMailboxModel
	err := r.db.WithContext(ctx).
		Where("resource_id = ? AND owner_user_id = ? AND status <> ?", domainResourceID, ownerUserID, generatedMailboxRetiredStatus).
		Order("created_at DESC").
		Offset(offset).Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("list generated mailboxes: %w", err)
	}
	result := make([]domain.GeneratedMailbox, len(models))
	for i, m := range models {
		result[i] = *m.toDomain()
	}
	return result, nil
}

func (r *GeneratedMailboxRepo) Count(ctx context.Context, domainResourceID uint, ownerUserID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&GeneratedMailboxModel{}).
		Where("resource_id = ? AND owner_user_id = ? AND status <> ?", domainResourceID, ownerUserID, generatedMailboxRetiredStatus).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count generated mailboxes: %w", err)
	}
	return count, nil
}

func (r *GeneratedMailboxRepo) DisableWithLog(ctx context.Context, mailboxID uint, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model GeneratedMailboxModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, mailboxID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock generated mailbox: %w", err)
		}
		if domain.GeneratedMailboxStatus(model.Status) == domain.GeneratedMailboxDisabled {
			return nil
		}
		if domain.GeneratedMailboxStatus(model.Status) != domain.GeneratedMailboxNormal {
			return domain.ErrInvalidResourceStatus
		}
		if err := tx.Model(&model).Update("status", string(domain.GeneratedMailboxDisabled)).Error; err != nil {
			return fmt.Errorf("disable generated mailbox: %w", err)
		}
		if err := governanceinfra.NewOperationLogRepo(r.db).CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		return nil
	})
}
