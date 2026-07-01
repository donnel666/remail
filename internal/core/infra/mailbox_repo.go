package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	"gorm.io/gorm"
)

// GeneratedMailboxModel is the GORM model for the generated_mailboxes table.
type GeneratedMailboxModel struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID      uint       `gorm:"not null;column:resource_id"`
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

func (r *GeneratedMailboxRepo) List(ctx context.Context, domainResourceID uint, offset, limit int) ([]domain.GeneratedMailbox, error) {
	var models []GeneratedMailboxModel
	err := r.db.WithContext(ctx).
		Where("resource_id = ?", domainResourceID).
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

func (r *GeneratedMailboxRepo) Count(ctx context.Context, domainResourceID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&GeneratedMailboxModel{}).
		Where("resource_id = ?", domainResourceID).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count generated mailboxes: %w", err)
	}
	return count, nil
}
