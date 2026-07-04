package platform

import (
	"context"

	"gorm.io/gorm"
)

type gormTxContextKey struct{}

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
