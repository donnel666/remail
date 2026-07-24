package app

import (
	"context"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

// Repository persists generic system settings.
type Repository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	List(ctx context.Context) ([]domain.Setting, error)
	Get(ctx context.Context, key string) (*domain.Setting, error)
	Upsert(ctx context.Context, key, value string) (*domain.Setting, error)
	BulkUpsert(ctx context.Context, settings []domain.Setting) ([]domain.Setting, error)
	Delete(ctx context.Context, key string) error
}

// RuntimeSettingsPublisher notifies other application replicas to reload the
// persisted runtime settings snapshot from the database.
type RuntimeSettingsPublisher interface {
	Publish(ctx context.Context) error
}
