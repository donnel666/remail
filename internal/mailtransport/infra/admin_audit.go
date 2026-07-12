package infra

import (
	"context"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
)

// operationLogTxWriter keeps high-risk administrator commands testable while
// requiring the audit write to participate in the same database transaction.
type operationLogTxWriter interface {
	CreateInTx(ctx context.Context, tx *gorm.DB, log *governancedomain.OperationLog) error
}
