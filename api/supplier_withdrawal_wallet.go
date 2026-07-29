package api

import (
	"context"
	"errors"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	billingapp "github.com/donnel666/remail/internal/billing/app"
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
