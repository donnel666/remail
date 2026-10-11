package infra

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
)

// A dedicated connection owns the scan lock; each order still commits in its own transaction.
func (r *Repo) WithSupplierSettlementScan(ctx context.Context, fn func() error) (runErr error) {
	db, err := r.db.DB()
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	const lockName = "SHA2(CONCAT(DATABASE(), ':supplier-settlement-scan'), 256)"
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK("+lockName+", 0)").Scan(&acquired); err != nil {
		return fmt.Errorf("acquire supplier scan lock: %w", err)
	}
	if !acquired.Valid {
		return fmt.Errorf("acquire supplier scan lock: no result")
	}
	if acquired.Int64 != 1 {
		return nil
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		var released sql.NullInt64
		err := conn.QueryRowContext(cleanupCtx, "SELECT RELEASE_LOCK("+lockName+")").Scan(&released)
		if err == nil && released.Valid && released.Int64 == 1 {
			return
		}
		// Never return a connection that may still own the scan lock to the pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		if err == nil {
			err = fmt.Errorf("supplier scan lock was not owned")
		}
		runErr = errors.Join(runErr, fmt.Errorf("release supplier scan lock: %w", err))
	}()
	return fn()
}

func (r *Repo) ListDueSupplierOrders(ctx context.Context, now time.Time, after *tradeapp.SupplierSettlementOrder, limit int) ([]tradeapp.SupplierSettlementOrder, error) {
	var result []tradeapp.SupplierSettlementOrder
	query := r.dbFor(ctx).Table("supplier_settlements AS s").Select("s.order_no, s.available_at").
		Joins("JOIN orders AS o ON o.order_no = s.order_no").
		Where("s.status = 'frozen' AND s.available_at <= ? AND (o.status IN ('completed', 'closed') OR (o.status = 'active' AND o.service_mode = 'purchase'))", now).
		Where("NOT EXISTS (SELECT 1 FROM aftersale_tickets AS t WHERE t.order_no = s.order_no AND t.status IN ('open', 'processing'))")
	if after != nil {
		query = query.Where("s.available_at > ? OR (s.available_at = ? AND s.order_no > ?)", after.AvailableAt, after.AvailableAt, after.OrderNo)
	}
	err := query.Order("s.available_at ASC, s.order_no ASC").Limit(limit).Scan(&result).Error
	return result, err
}

func (r *Repo) HasOpenOrderTicket(ctx context.Context, orderNo string) (bool, error) {
	var count int64
	err := r.dbFor(ctx).Table("aftersale_tickets").Where("order_no = ? AND status IN ('open', 'processing')", orderNo).Count(&count).Error
	return count > 0, err
}
