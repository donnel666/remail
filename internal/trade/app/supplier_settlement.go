package app

import (
	"context"
	"fmt"
	"time"

	moneyfmt "github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/trade/domain"
)

type SupplierSettlementRequest struct {
	OrderNo        string
	SupplierUserID uint
	Amount         string
	AvailableAt    time.Time
}

type SupplierSettlementPort interface {
	LockSupplierWallets(context.Context, ...uint) error
	FreezeSupplierSettlement(context.Context, SupplierSettlementRequest) error
	RescheduleSupplierSettlement(context.Context, string, time.Time) error
	CreditSupplierSettlement(context.Context, string, time.Time) (bool, error)
	CancelSupplierSettlement(context.Context, string) error
}

type supplierSettlementRepository interface {
	WithSupplierSettlementScan(context.Context, func() error) error
	ListDueSupplierOrders(context.Context, time.Time, *SupplierSettlementOrder, int) ([]SupplierSettlementOrder, error)
	HasOpenOrderTicket(context.Context, string) (bool, error)
}

type SupplierSettlementOrder struct {
	OrderNo     string
	AvailableAt time.Time
}

const supplierSettlementScanBudget = 20 * time.Second
const supplierSettlementOrderBudget = time.Second

func (uc *UseCase) LockOrderForTicket(ctx context.Context, orderNo string, userID uint) error {
	order, err := uc.repo.LockOrderForUpdate(ctx, orderNo)
	if err != nil {
		return err
	}
	if order.UserID != userID {
		return domain.ErrOrderForbidden
	}
	return nil
}

func (uc *UseCase) lockOrderWallets(ctx context.Context, order domain.Order) error {
	if order.SupplierUserID == nil {
		return uc.wallet.LockConsumer(ctx, order.UserID)
	}
	if uc.settlements == nil {
		return fmt.Errorf("supplier settlement unavailable")
	}
	return uc.settlements.LockSupplierWallets(ctx, order.UserID, *order.SupplierUserID)
}

func supplierSettlementDue(order domain.Order) (time.Time, error) {
	if order.ServiceMode == domain.ServiceModePurchase && order.AfterSaleUntil != nil {
		return order.AfterSaleUntil.UTC(), nil
	}
	if order.ReceiveUntil == nil {
		return time.Time{}, domain.ErrInvalidOrderRequest
	}
	due := order.ReceiveUntil.UTC()
	// Late matching can activate a purchase after its receive window; retain the maximum warranty until then.
	if order.ServiceMode == domain.ServiceModePurchase {
		due = purchaseWarrantyUntil(order, order.WarrantyMinutes, due)
	}
	return due, nil
}

func (uc *UseCase) activateAndFreezeSupplier(ctx context.Context, order domain.Order, cmd MarkActiveCommand) (*domain.Order, error) {
	if order.SupplierUserID == nil {
		return uc.repo.MarkActive(ctx, cmd)
	}
	if *order.SupplierUserID == order.UserID {
		return nil, domain.ErrInvalidOrderRequest
	}
	order.ReceiveStartedAt = &cmd.ReceiveStartedAt
	order.ReceiveUntil = &cmd.ReceiveUntil
	order.AfterSaleUntil = cmd.AfterSaleUntil
	due, err := supplierSettlementDue(order)
	if err != nil {
		return nil, err
	}
	var active *domain.Order
	err = uc.repo.WithTx(ctx, func(ctx context.Context) error {
		// All money changes take wallet -> order -> settlement; matching needs no wallet lock.
		if err := uc.settlements.LockSupplierWallets(ctx, *order.SupplierUserID); err != nil {
			return err
		}
		var err error
		active, err = uc.repo.MarkActive(ctx, cmd)
		if err != nil {
			return err
		}
		return uc.settlements.FreezeSupplierSettlement(ctx, SupplierSettlementRequest{OrderNo: order.OrderNo, SupplierUserID: *order.SupplierUserID, Amount: order.SupplierAmount, AvailableAt: due})
	})
	return active, err
}

func (uc *UseCase) rescheduleSupplier(ctx context.Context, order *domain.Order, changed bool) error {
	if !changed || order == nil || order.SupplierUserID == nil {
		return nil
	}
	due, err := supplierSettlementDue(*order)
	if err != nil {
		return err
	}
	return uc.settlements.RescheduleSupplierSettlement(ctx, order.OrderNo, due)
}

func (uc *UseCase) cancelSupplier(ctx context.Context, order domain.Order) error {
	if order.SupplierUserID == nil {
		return nil
	}
	return uc.settlements.CancelSupplierSettlement(ctx, order.OrderNo)
}

func (uc *UseCase) settleDueSuppliers(ctx context.Context, now time.Time, limit int, result *ExpireOrdersResult) error {
	if uc.settlements == nil {
		return nil
	}
	repo, ok := uc.repo.(supplierSettlementRepository)
	if !ok {
		return fmt.Errorf("supplier settlement order queries unavailable")
	}
	if !uc.supplierScanMu.TryLock() {
		return nil
	}
	defer uc.supplierScanMu.Unlock()
	scanCtx, cancel := context.WithTimeout(ctx, supplierSettlementScanBudget)
	defer cancel()
	return repo.WithSupplierSettlementScan(scanCtx, func() error {
		return uc.scanDueSuppliers(scanCtx, repo, now, limit, result)
	})
}

func (uc *UseCase) scanDueSuppliers(ctx context.Context, repo supplierSettlementRepository, now time.Time, limit int, result *ExpireOrdersResult) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		orders, err := repo.ListDueSupplierOrders(ctx, now, uc.supplierScanAfter, limit)
		if err != nil {
			return err
		}
		for _, order := range orders {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Retain progress across budgets, including a blocked or failed order.
			uc.supplierScanAfter = &order
			orderCtx, cancel := context.WithTimeout(ctx, supplierSettlementOrderBudget)
			err := uc.settleSupplierOrder(orderCtx, repo, order.OrderNo, now, result)
			cancel()
			if err != nil {
				result.Failed++
				if ctx.Err() != nil {
					return ctx.Err()
				}
				uc.writeSystemLog(ctx, "error", "trade.supplier_settlement_failed", "", order.OrderNo, err.Error())
				continue
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(orders) < limit {
			uc.supplierScanAfter = nil
			return nil
		}
	}
}

func (uc *UseCase) settleSupplierOrder(ctx context.Context, repo supplierSettlementRepository, orderNo string, now time.Time, result *ExpireOrdersResult) error {
	order, err := uc.repo.FindOrder(ctx, orderNo)
	if err != nil {
		return err
	}
	if order.SupplierUserID == nil {
		return nil
	}
	credited := false
	completed := false
	err = uc.repo.WithTx(ctx, func(ctx context.Context) error {
		credited, completed = false, false
		if err := uc.settlements.LockSupplierWallets(ctx, *order.SupplierUserID); err != nil {
			return err
		}
		locked, err := uc.repo.LockOrderForUpdate(ctx, orderNo)
		if err != nil {
			return err
		}
		if locked.SupplierUserID == nil || (locked.Status != domain.OrderStatusCompleted && locked.Status != domain.OrderStatusClosed &&
			(locked.Status != domain.OrderStatusActive || locked.ServiceMode != domain.ServiceModePurchase)) {
			return nil
		}
		if *locked.SupplierUserID == locked.UserID {
			return domain.ErrInvalidOrderRequest
		}
		due, err := supplierSettlementDue(*locked)
		if err != nil {
			return err
		}
		if now.Before(due) {
			return nil
		}
		held, err := repo.HasOpenOrderTicket(ctx, orderNo)
		if err != nil || held {
			return err
		}
		if locked.Status == domain.OrderStatusActive {
			// Complete due purchases here too, so the lifecycle batch cannot cap payouts.
			_, completed, err = uc.repo.CompleteExpiredOrder(ctx, orderNo, "Purchase settlement window expired.")
			if err != nil {
				return err
			}
			if !completed {
				return domain.ErrOrderStateConflict
			}
		}
		order = locked
		credited, err = uc.settlements.CreditSupplierSettlement(ctx, orderNo, now)
		return err
	})
	if err == nil {
		if credited {
			result.SupplierSettled++
		}
		if completed {
			if order.ActivatedAt == nil {
				result.PurchaseActivationCompleted++
			} else {
				result.PurchaseWarrantyCompleted++
			}
		}
	}
	return err
}

func (uc *UseCase) checkoutAmounts(ctx context.Context, order domain.Order, allocation AllocationResult, payAmount string) (string, string, *uint, error) {
	if order.SupplierAmount == "" {
		return payAmount, "", nil, nil
	}
	if allocation.SupplyScope == SupplyScopeOwned {
		return "0.00", "0.00", nil, nil
	}
	amount, err := moneyfmt.Parse(order.SupplierAmount)
	if err != nil || amount.IsNegative() {
		return "", "", nil, domain.ErrInvalidOrderRequest
	}
	if amount.IsZero() && payAmount == "0.00" {
		return "0.00", "0.00", nil, nil
	}
	port, ok := uc.allocation.(interface {
		AllocationSupplierUserID(context.Context, string) (uint, error)
	})
	if !ok {
		return "", "", nil, fmt.Errorf("supplier settlement unavailable")
	}
	owner, err := port.AllocationSupplierUserID(ctx, order.OrderNo)
	if err != nil {
		return "", "", nil, err
	}
	if owner == 0 {
		return "", "", nil, domain.ErrInvalidOrderRequest
	}
	if owner == order.UserID {
		return "0.00", "0.00", nil, nil
	}
	if amount.IsZero() {
		return payAmount, "0.00", nil, nil
	}
	if uc.settlements == nil {
		return "", "", nil, fmt.Errorf("supplier settlement unavailable")
	}
	return payAmount, moneyfmt.Format(amount), &owner, nil
}
