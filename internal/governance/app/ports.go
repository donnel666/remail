package app

import (
	"context"

	"github.com/donnel666/remail/internal/governance/domain"
)

// OperationLogPort is used by business contexts to write safe audit records.
type OperationLogPort interface {
	Create(ctx context.Context, log *domain.OperationLog) error
}

// FilePort stores private files for business contexts without exposing object storage details.
type FilePort interface {
	SavePrivate(ctx context.Context, file domain.PrivateFile) (*domain.StoredPrivateFile, error)
	ReadPrivate(ctx context.Context, objectKey string) (*domain.PrivateFile, error)
}
