package app

import (
	"context"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

// Repository persists generic system settings.
type Repository interface {
	List(ctx context.Context) ([]domain.Setting, error)
	Get(ctx context.Context, key string) (*domain.Setting, error)
	Upsert(ctx context.Context, key, value string) (*domain.Setting, error)
	Delete(ctx context.Context, key string) error
}
