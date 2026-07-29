package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
)

var balanceWarningThresholds = [...]string{"3000.00", "2000.00", "1000.00", "500.00"}

type BalanceWarningClaim struct {
	UserID        uint
	Balance       string
	Cycle         uint64
	PreviousLevel int
	Level         int
}

func (uc *WalletUseCase) SetMailDelivery(delivery mailapp.DeliveryPort) {
	uc.delivery = delivery
}

func (uc *WalletUseCase) DispatchBalanceWarnings(ctx context.Context, limit int) error {
	if uc == nil || uc.delivery == nil || uc.users == nil {
		return nil
	}
	claims, err := uc.repo.ClaimBalanceWarnings(ctx, limit)
	if err != nil || len(claims) == 0 {
		return err
	}
	ids := make([]uint, len(claims))
	for i := range claims {
		ids[i] = claims[i].UserID
	}
	users, err := uc.users.LookupUsers(ctx, ids)
	if err != nil {
		for _, claim := range claims {
			_ = uc.repo.ReleaseBalanceWarning(ctx, claim)
		}
		return err
	}
	var sendErrors []error
	for _, claim := range claims {
		user, exists := users[claim.UserID]
		status := strings.ToLower(strings.TrimSpace(user.Status))
		if !exists || strings.TrimSpace(user.Email) == "" || (status != "" && status != "active") {
			continue
		}
		failed := false
		for level := claim.PreviousLevel + 1; level <= claim.Level; level++ {
			if err := uc.delivery.Send(ctx, mailapp.BalanceWarningMessage(user.Email, claim.Balance, balanceWarningThresholds[level-1], claim.Cycle)); err != nil {
				sendErrors = append(sendErrors, fmt.Errorf("send balance warning user %d: %w", claim.UserID, err))
				failed = true
				break
			}
		}
		if failed {
			_ = uc.repo.ReleaseBalanceWarning(ctx, claim)
		}
	}
	return errors.Join(sendErrors...)
}

func sendRechargeCreditedNotification(
	ctx context.Context,
	delivery mailapp.DeliveryPort,
	users UserDirectory,
	userID uint,
	reference, amount, balance string,
) error {
	if delivery == nil || users == nil || userID == 0 {
		return nil
	}
	directory, err := users.LookupUsers(ctx, []uint{userID})
	if err != nil {
		return err
	}
	user, exists := directory[userID]
	if !exists || strings.TrimSpace(user.Email) == "" {
		return nil
	}
	return delivery.Send(ctx, mailapp.RechargeCreditedMessage(user.Email, reference, amount, balance))
}
