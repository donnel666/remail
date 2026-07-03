package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
)

// SystemLogModel is the GORM model for governance system logs.
type SystemLogModel struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	Level     string    `gorm:"type:varchar(32);not null"`
	Module    string    `gorm:"type:varchar(100);not null"`
	EventType string    `gorm:"type:varchar(100);not null;column:event_type"`
	RequestID string    `gorm:"type:varchar(64);not null;column:request_id"`
	BizType   string    `gorm:"type:varchar(100);not null;column:biz_type"`
	BizID     string    `gorm:"type:varchar(100);not null;column:biz_id"`
	Message   string    `gorm:"type:varchar(500);not null"`
	Detail    string    `gorm:"type:varchar(1000);not null"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (SystemLogModel) TableName() string {
	return "system_logs"
}

// SystemLogRepo persists safe system diagnostics.
type SystemLogRepo struct {
	db *gorm.DB
}

func NewSystemLogRepo(db *gorm.DB) *SystemLogRepo {
	return &SystemLogRepo{db: db}
}

func (r *SystemLogRepo) Create(ctx context.Context, log *domain.SystemLog) error {
	if err := r.db.WithContext(ctx).Create(systemLogModel(log)).Error; err != nil {
		return fmt.Errorf("create system log: %w", err)
	}
	return nil
}

func systemLogModel(log *domain.SystemLog) *SystemLogModel {
	return &SystemLogModel{
		Level:     log.Level,
		Module:    log.Module,
		EventType: log.EventType,
		RequestID: log.RequestID,
		BizType:   log.BizType,
		BizID:     log.BizID,
		Message:   log.Message,
		Detail:    log.Detail,
	}
}
