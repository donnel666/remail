package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

type lotterySettlementRepoStub struct {
	WalletRepository
	called bool
	got    LotterySettlementCommand
}

func (s *lotterySettlementRepoStub) SettleLotteryPool(_ context.Context, req LotterySettlementCommand) (*LotterySettlementResult, error) {
	s.called = true
	s.got = req
	return &LotterySettlementResult{}, nil
}

func TestSettleLotteryPoolValidatesTotalAndNormalizesOrder(t *testing.T) {
	repo := &lotterySettlementRepoStub{}
	uc := NewWalletUseCase(repo)
	_, err := uc.SettleLotteryPool(context.Background(), LotterySettlementRequest{
		LotteryID: 9, TotalAmount: "10",
		Awards:       []LotteryAward{{UserID: 8, Amount: "3"}, {UserID: 4, Amount: "6.00"}},
		UnusedAmount: "1",
	})
	require.NoError(t, err)
	require.True(t, repo.called)
	require.Equal(t, []LotteryAward{{UserID: 4, Amount: "6.00"}, {UserID: 8, Amount: "3.00"}}, repo.got.Awards)
	require.Equal(t, "10.00", repo.got.TotalAmount)
	require.Equal(t, "1.00", repo.got.UnusedAmount)
	require.NotEmpty(t, repo.got.RequestFingerprint)

	repo.called = false
	_, err = uc.SettleLotteryPool(context.Background(), LotterySettlementRequest{
		LotteryID: 9, TotalAmount: "10.00",
		Awards:       []LotteryAward{{UserID: 4, Amount: "8.00"}},
		UnusedAmount: "1.00",
	})
	require.ErrorIs(t, err, domain.ErrInvalidAmount)
	require.False(t, repo.called)
}

func TestLotterySettlementFingerprintIgnoresAwardInputOrder(t *testing.T) {
	left := LotterySettlementFingerprint(LotterySettlementRequest{
		LotteryID: 4, TotalAmount: "9.00", UnusedAmount: "1.00",
		Awards: []LotteryAward{{UserID: 8, Amount: "3.00"}, {UserID: 2, Amount: "5.00"}},
	})
	right := LotterySettlementFingerprint(LotterySettlementRequest{
		LotteryID: 4, TotalAmount: "9.00", UnusedAmount: "1.00",
		Awards: []LotteryAward{{UserID: 2, Amount: "5.00"}, {UserID: 8, Amount: "3.00"}},
	})
	require.Equal(t, left, right)
}
