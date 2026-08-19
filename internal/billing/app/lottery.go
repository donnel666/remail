package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/donnel666/remail/internal/billing/domain"
)

// LotteryAward is the only reward data Billing accepts from the lottery
// bounded context. Wallet balances and ledger rows remain Billing-owned.
type LotteryAward struct {
	UserID uint
	Amount string
}

type LotterySettlementRequest struct {
	LotteryID          uint
	TotalAmount        string
	Awards             []LotteryAward
	UnusedAmount       string
	RequestFingerprint string
	RequestID          string
}

type LotteryAwardResult struct {
	UserID      uint
	Amount      string
	Transaction domain.Transaction
}

type LotterySettlementResult struct {
	Awards   []LotteryAwardResult
	Replayed bool
}

type LotterySettlementRepository interface {
	SettleLotteryPool(ctx context.Context, req LotterySettlementCommand) (*LotterySettlementResult, error)
}

type LotterySettlementCommand = LotterySettlementRequest

func (uc *WalletUseCase) SettleLotteryPool(ctx context.Context, req LotterySettlementRequest) (*LotterySettlementResult, error) {
	if req.LotteryID == 0 {
		return nil, domain.ErrInvalidFilter
	}
	total, err := domain.ParseMoney(req.TotalAmount)
	if err != nil || total.IsNegative() {
		return nil, domain.ErrInvalidAmount
	}
	unused, err := domain.ParseMoney(req.UnusedAmount)
	if err != nil || unused.IsNegative() {
		return nil, domain.ErrInvalidAmount
	}
	awards := append([]LotteryAward(nil), req.Awards...)
	sort.Slice(awards, func(i, j int) bool { return awards[i].UserID < awards[j].UserID })
	seen := make(map[uint]struct{}, len(awards))
	sum := unused
	for i := range awards {
		if awards[i].UserID == 0 {
			return nil, domain.ErrInvalidFilter
		}
		if _, exists := seen[awards[i].UserID]; exists {
			return nil, domain.ErrInvalidFilter
		}
		seen[awards[i].UserID] = struct{}{}
		amount, parseErr := domain.ParseMoney(awards[i].Amount)
		if parseErr != nil || !amount.IsPositive() {
			return nil, domain.ErrInvalidAmount
		}
		awards[i].Amount = domain.MoneyString(amount)
		sum = sum.Add(amount)
	}
	if !sum.Equal(total) {
		return nil, domain.ErrInvalidAmount
	}
	fingerprintValue := LotterySettlementFingerprint(LotterySettlementRequest{
		LotteryID:   req.LotteryID,
		TotalAmount: domain.MoneyString(total), Awards: awards,
		UnusedAmount: domain.MoneyString(unused),
	})
	repo, ok := uc.repo.(LotterySettlementRepository)
	if !ok {
		return nil, fmt.Errorf("lottery settlement unavailable")
	}
	result, err := repo.SettleLotteryPool(ctx, LotterySettlementCommand{
		LotteryID:   req.LotteryID,
		TotalAmount: domain.MoneyString(total), Awards: awards,
		UnusedAmount:       domain.MoneyString(unused),
		RequestFingerprint: fingerprintValue,
		RequestID:          strings.TrimSpace(req.RequestID),
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// lotterySettlementFingerprint is shared with the infra implementation so a
// retry with a different award list is rejected by Billing's idempotency guard.
func LotterySettlementFingerprint(req LotterySettlementRequest) string {
	awards := append([]LotteryAward(nil), req.Awards...)
	sort.Slice(awards, func(i, j int) bool { return awards[i].UserID < awards[j].UserID })
	parts := []any{"lottery.settle", req.LotteryID, req.TotalAmount, req.UnusedAmount}
	for _, award := range awards {
		parts = append(parts, award.UserID, award.Amount)
	}
	return fingerprint(parts...)
}
