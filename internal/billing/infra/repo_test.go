package infra

import (
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestBalanceWarningLevel(t *testing.T) {
	for _, test := range []struct {
		balance string
		level   int
	}{
		{"3000.01", 0}, {"3000.00", 1}, {"2000.00", 2}, {"1000.00", 3}, {"500.00", 4},
	} {
		level, err := balanceWarningLevel(test.balance)
		require.NoError(t, err)
		require.Equal(t, test.level, level, test.balance)
	}
}

func TestMapConsumerBalancesReturnsInvalidBalanceError(t *testing.T) {
	balances, err := mapConsumerBalances([]WalletModel{{UserID: 7, ConsumerBalance: "invalid"}})

	require.Nil(t, balances)
	require.ErrorIs(t, err, domain.ErrInvalidAmount)
	require.ErrorContains(t, err, "user 7")
}

func TestCalculateReferralRewardAppliesSingleAndCumulativeCaps(t *testing.T) {
	reward := calculateReferralReward(
		decimal.NewFromInt(100),
		decimal.NewFromInt(45),
		decimal.RequireFromString("0.8"),
		decimal.NewFromInt(60),
		decimal.NewFromInt(50),
	)

	require.True(t, reward.Equal(decimal.NewFromInt(5)))
}
