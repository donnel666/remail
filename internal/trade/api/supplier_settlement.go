package api

import (
	"context"
	"errors"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
)

func (a allocationAdapter) AllocationSupplierUserID(ctx context.Context, orderNo string) (uint, error) {
	return a.alloc.AllocationSupplierUserID(ctx, orderNo)
}

func (a billingWalletAdapter) LockSupplierWallets(ctx context.Context, userIDs ...uint) error {
	return a.wallet.LockSupplierWallets(ctx, userIDs...)
}
func (a billingWalletAdapter) FreezeSupplierSettlement(ctx context.Context, req tradeapp.SupplierSettlementRequest) error {
	return a.wallet.FreezeSupplierSettlement(ctx, billingapp.SupplierSettlementRequest{OrderNo: req.OrderNo, SupplierUserID: req.SupplierUserID, Amount: req.Amount, AvailableAt: req.AvailableAt})
}
func (a billingWalletAdapter) RescheduleSupplierSettlement(ctx context.Context, orderNo string, due time.Time) error {
	return a.wallet.RescheduleSupplierSettlement(ctx, orderNo, due)
}
func (a billingWalletAdapter) CreditSupplierSettlement(ctx context.Context, orderNo string, now time.Time) (bool, error) {
	return a.wallet.CreditSupplierSettlement(ctx, orderNo, now)
}
func (a billingWalletAdapter) CancelSupplierSettlement(ctx context.Context, orderNo string) error {
	err := a.wallet.CancelSupplierSettlement(ctx, orderNo)
	if errors.Is(err, billingdomain.ErrSupplierSettlementBalance) {
		return domain.ErrSupplierSettlementBalance
	}
	return err
}

var _ tradeapp.SupplierSettlementPort = billingWalletAdapter{}
