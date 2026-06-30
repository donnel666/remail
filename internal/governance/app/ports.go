package app

import (
	"context"

	"github.com/donnel666/remail/internal/governance/domain"
)

// OperationLogPort is used by business contexts to write safe audit records.
type OperationLogPort interface {
	Create(ctx context.Context, log *domain.OperationLog) error
}
