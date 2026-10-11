package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/billing/domain"
)

type SupplierSettlementRequest struct {
	OrderNo        string
	SupplierUserID uint
	Amount         string
	AvailableAt    time.Time
}

type SupplierSettlementRepository interface {
	LockSupplierWallets(context.Context, ...uint) error
	FreezeSupplierSettlement(context.Context, SupplierSettlementRequest) error
	RescheduleSupplierSettlement(context.Context, string, time.Time) error
	CreditSupplierSettlement(context.Context, string, time.Time) (bool, error)
	CancelSupplierSettlement(context.Context, string) error
}

func (uc *WalletUseCase) supplierSettlements() (SupplierSettlementRepository, error) {
	repo, ok := uc.repo.(SupplierSettlementRepository)
	if !ok {
		return nil, fmt.Errorf("supplier settlement repository unavailable")
	}
	return repo, nil
}

func (uc *WalletUseCase) LockSupplierWallets(ctx context.Context, userIDs ...uint) error {
	repo, err := uc.supplierSettlements()
	if err != nil {
		return err
	}
	return repo.LockSupplierWallets(ctx, userIDs...)
}

func (uc *WalletUseCase) FreezeSupplierSettlement(ctx context.Context, req SupplierSettlementRequest) error {
	amount, err := domain.NormalizePositiveMoney(req.Amount)
	if err != nil {
		return err
	}
	req.OrderNo = strings.TrimSpace(req.OrderNo)
	if req.OrderNo == "" || req.SupplierUserID == 0 || req.AvailableAt.IsZero() {
		return domain.ErrInvalidFilter
	}
	req.Amount = amount
	repo, err := uc.supplierSettlements()
	if err != nil {
		return err
	}
	return repo.FreezeSupplierSettlement(ctx, req)
}

func (uc *WalletUseCase) RescheduleSupplierSettlement(ctx context.Context, orderNo string, due time.Time) error {
	if strings.TrimSpace(orderNo) == "" || due.IsZero() {
		return domain.ErrInvalidFilter
	}
	repo, err := uc.supplierSettlements()
	if err != nil {
		return err
	}
	return repo.RescheduleSupplierSettlement(ctx, orderNo, due)
}

func (uc *WalletUseCase) CreditSupplierSettlement(ctx context.Context, orderNo string, now time.Time) (bool, error) {
	if strings.TrimSpace(orderNo) == "" || now.IsZero() {
		return false, domain.ErrInvalidFilter
	}
	repo, err := uc.supplierSettlements()
	if err != nil {
		return false, err
	}
	return repo.CreditSupplierSettlement(ctx, orderNo, now)
}

func (uc *WalletUseCase) CancelSupplierSettlement(ctx context.Context, orderNo string) error {
	if strings.TrimSpace(orderNo) == "" {
		return domain.ErrInvalidFilter
	}
	repo, err := uc.supplierSettlements()
	if err != nil {
		return err
	}
	return repo.CancelSupplierSettlement(ctx, orderNo)
}
