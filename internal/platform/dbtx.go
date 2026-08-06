package platform

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type gormTxContextKey struct{}
type gormRollbackContextKey struct{}

type gormRollbackRegistry struct {
	callbacks []func(context.Context) error
}

// WithGormTx attaches the current GORM transaction to ctx so another module
// can participate in the same short database transaction through a port call.
func WithGormTx(ctx context.Context, tx *gorm.DB) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx == nil {
		return ctx
	}
	return context.WithValue(ctx, gormTxContextKey{}, tx)
}

// GormTxFromContext returns the GORM transaction attached by WithGormTx.
func GormTxFromContext(ctx context.Context) (*gorm.DB, bool) {
	if ctx == nil {
		return nil, false
	}
	tx, ok := ctx.Value(gormTxContextKey{}).(*gorm.DB)
	return tx, ok && tx != nil
}

// WithGormRollback starts a rollback callback scope for one transaction attempt.
func WithGormRollback(ctx context.Context) (context.Context, func(context.Context) error) {
	if ctx == nil {
		ctx = context.Background()
	}
	registry := &gormRollbackRegistry{}
	return context.WithValue(ctx, gormRollbackContextKey{}, registry), func(runCtx context.Context) error {
		callbacks := registry.callbacks
		registry.callbacks = nil
		var err error
		for index := len(callbacks) - 1; index >= 0; index-- {
			err = errors.Join(err, callbacks[index](runCtx))
		}
		return err
	}
}

func HasGormRollback(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(gormRollbackContextKey{}).(*gormRollbackRegistry)
	return ok
}

func RegisterGormRollback(ctx context.Context, callback func(context.Context) error) bool {
	if ctx == nil || callback == nil {
		return false
	}
	registry, ok := ctx.Value(gormRollbackContextKey{}).(*gormRollbackRegistry)
	if !ok {
		return false
	}
	registry.callbacks = append(registry.callbacks, callback)
	return true
}
