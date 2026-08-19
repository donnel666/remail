package app

import (
	"testing"
	"time"

	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	"github.com/donnel666/remail/internal/money"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestAllocatePaysEveryEntryWithinBoundsAndPreservesPool(t *testing.T) {
	entries := make([]lotterydomain.Entry, 100)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 7, UserID: uint(i + 1)}
	}
	payouts, unused, err := Allocate(entries, "1000.00", "1.00", "20.00", lotterydomain.TierWeights{
		Consolation: 80,
		Normal:      15,
		Lucky:       5,
	})
	require.NoError(t, err)
	require.Len(t, payouts, len(entries))

	total := decimal.Zero
	tiers := map[lotterydomain.Tier]int{}
	distinct := map[string]struct{}{}
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		require.True(t, amount.GreaterThanOrEqual(decimal.NewFromInt(1)))
		require.True(t, amount.LessThanOrEqual(decimal.NewFromInt(20)))
		total = total.Add(amount)
		tiers[payout.Tier]++
		distinct[payout.Amount] = struct{}{}
	}
	unusedAmount, err := money.Parse(unused)
	require.NoError(t, err)
	require.True(t, total.Add(unusedAmount).Equal(decimal.NewFromInt(1000)))
	require.Equal(t, 80, tiers[lotterydomain.TierConsolation])
	require.Equal(t, 15, tiers[lotterydomain.TierNormal])
	require.Equal(t, 5, tiers[lotterydomain.TierLucky])
	require.Greater(t, len(distinct), 1)
}

func TestAllocateLeavesUnusedAmountAbovePerPersonMaximum(t *testing.T) {
	entries := []lotterydomain.Entry{{LotteryID: 8, UserID: 1}, {LotteryID: 8, UserID: 2}}
	payouts, unused, err := Allocate(entries, "100.00", "1.00", "10.00", lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5})
	require.NoError(t, err)
	require.Len(t, payouts, 2)
	require.Equal(t, "81.80", unused)
	require.NotEqual(t, payouts[0].Amount, payouts[1].Amount)
}

func TestAllocateCapsOneAccountAtThreeTimesNominalAverage(t *testing.T) {
	entries := make([]lotterydomain.Entry, 10)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 9, UserID: uint(i + 1)}
	}
	payouts, _, err := Allocate(entries, "100.00", "1.00", "100.00", lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5})
	require.NoError(t, err)
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		require.True(t, amount.LessThanOrEqual(decimal.NewFromInt(30)), payout.Amount)
	}
}

func TestValidateRulesRequiresBothEarlyDrawConditions(t *testing.T) {
	drawAt := time.Now().Add(time.Hour)
	target := 10
	base := CreateRequest{
		TotalAmount: "100.00", MinPayout: "1.00", MaxPayout: "20.00",
		TierWeights: lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		DrawAt:      &drawAt, ParticipantTarget: &target,
	}
	_, _, _, capacity, err := validateRules(base)
	require.NoError(t, err)
	require.Equal(t, 10, capacity)

	withoutTime := base
	withoutTime.DrawAt = nil
	_, _, _, _, err = validateRules(withoutTime)
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)

	withoutTarget := base
	withoutTarget.ParticipantTarget = nil
	_, _, _, _, err = validateRules(withoutTarget)
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)
}

func TestRemainingUnusedRebuildsSettlementAfterPayoutsWereSaved(t *testing.T) {
	unused, err := remainingUnused("10.00", []lotterydomain.Payout{{Amount: "2.25"}, {Amount: "3.75"}})
	require.NoError(t, err)
	require.Equal(t, "4.00", unused)
}

func TestDistributeBonusUsesOneRoundSnapshot(t *testing.T) {
	amounts := []int64{1, 1}
	require.NoError(t, distributeBonus(amounts, []int64{1, 1}, 98, 100))
	require.Equal(t, []int64{50, 50}, amounts)
}

func TestValidateRulesRejectsFlatTargetDraw(t *testing.T) {
	drawAt := time.Now().Add(time.Hour)
	target := 100
	base := CreateRequest{
		TotalAmount: "100.00", MinPayout: "1.00", MaxPayout: "20.00",
		TierWeights: lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		DrawAt:      &drawAt, ParticipantTarget: &target,
	}
	_, _, _, _, err := validateRules(base)
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)

	base.TotalAmount = "300.00"
	_, _, _, _, err = validateRules(base)
	require.NoError(t, err)
}
