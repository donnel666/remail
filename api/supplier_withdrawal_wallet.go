package api

import (
	"context"
	"errors"
	"fmt"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	aftersaledomain "github.com/donnel666/remail/internal/aftersale/domain"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

var _ aftersaleapp.SupplierWalletPort = supplierWithdrawalWalletAdapter{}

type supplierWithdrawalWalletAdapter struct {
	wallets *billingapp.WalletUseCase
}

func (a supplierWithdrawalWalletAdapter) SupplierAvailable(ctx context.Context, userID uint) (string, error) {
	if a.wallets == nil {
		return "", errors.New("billing wallet is unavailable")
	}
	summary, err := a.wallets.GetWallet(ctx, userID)
	if err != nil {
		return "", err
	}
	if summary == nil {
		return "", errors.New("billing wallet is unavailable")
	}
	return summary.Wallet.SupplierAvailable, nil
}

func (a supplierWithdrawalWalletAdapter) WithdrawSupplier(ctx context.Context, cmd aftersaleapp.SupplierWithdrawalCommand) error {
	if a.wallets == nil {
		return aftersaledomain.ErrSupplierWalletUnavailable
	}
	_, err := a.wallets.WithdrawSupplier(ctx, billingapp.WithdrawSupplierRequest{
		TicketNo:       cmd.TicketNo,
		UserID:         cmd.UserID,
		Amount:         cmd.Amount,
		Note:           cmd.TicketNo,
		IdempotencyKey: "astw-" + cmd.TicketNo,
		RequestID:      cmd.RequestID,
		OperationLog: &governancedomain.OperationLog{
			OperatorUserID: cmd.OperatorUserID,
			OperationType:  "billing.wallet.withdraw",
			ResourceType:   "billing",
			ResourceID:     fmt.Sprint(cmd.UserID),
			Path:           "/v1/admin/tickets/:ticketNo/withdraw",
			Result:         "success",
			SafeSummary:    "Supplier withdrawal confirmed for ticket " + cmd.TicketNo,
			RequestID:      cmd.RequestID,
		},
	})
	switch {
	case errors.Is(err, billingdomain.ErrInsufficientBalance):
		return aftersaledomain.ErrInsufficientSupplierBalance
	case errors.Is(err, billingdomain.ErrIdempotencyConflict):
		return aftersaledomain.ErrTicketStateConflict
	case errors.Is(err, billingdomain.ErrWithdrawalAlreadyRecorded):
		return aftersaledomain.ErrWithdrawalAlreadyRecorded
	default:
		return err
	}
}
