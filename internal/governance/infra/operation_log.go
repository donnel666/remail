package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
)

// OperationLogModel is the GORM model for governance operation logs.
type OperationLogModel struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	OperatorUserID uint      `gorm:"column:operator_user_id;not null"`
	OperationType  string    `gorm:"type:varchar(100);not null;column:operation_type"`
	ResourceType   string    `gorm:"type:varchar(100);not null;column:resource_type"`
	ResourceID     string    `gorm:"type:varchar(100);not null;column:resource_id"`
	Path           string    `gorm:"type:varchar(255);not null"`
	Result         string    `gorm:"type:varchar(32);not null"`
	SafeSummary    string    `gorm:"type:varchar(500);not null;column:safe_summary"`
	RequestID      string    `gorm:"type:varchar(64);not null;column:request_id"`
	CreatedAt      time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (OperationLogModel) TableName() string {
	return "operation_logs"
}

// OperationLogRepo persists governance operation logs.
type OperationLogRepo struct {
	db *gorm.DB
}

func NewOperationLogRepo(db *gorm.DB) *OperationLogRepo {
	return &OperationLogRepo{db: db}
}

func (r *OperationLogRepo) Create(ctx context.Context, log *domain.OperationLog) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return r.CreateInTx(ctx, tx, log)
	}
	if err := r.db.WithContext(ctx).Create(operationLogModel(log)).Error; err != nil {
		return fmt.Errorf("create operation log: %w", err)
	}
	return nil
}

func (r *OperationLogRepo) CreateInTx(ctx context.Context, tx *gorm.DB, log *domain.OperationLog) error {
	if err := tx.WithContext(ctx).Create(operationLogModel(log)).Error; err != nil {
		return fmt.Errorf("create operation log: %w", err)
	}
	return nil
}

func operationLogModel(log *domain.OperationLog) *OperationLogModel {
	return &OperationLogModel{
		OperatorUserID: log.OperatorUserID,
		OperationType:  log.OperationType,
		ResourceType:   log.ResourceType,
		ResourceID:     log.ResourceID,
		Path:           log.Path,
		Result:         log.Result,
		SafeSummary:    log.SafeSummary,
		RequestID:      log.RequestID,
	}
}
