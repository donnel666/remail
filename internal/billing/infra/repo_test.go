package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestLatestLeaderboardRewardsLoadsEmailForDisplayFallback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE leaderboard_settlements (id INTEGER PRIMARY KEY, business_date TEXT, period_start DATETIME, period_end DATETIME, status TEXT, settled_at DATETIME)`,
		`CREATE TABLE leaderboard_rewards (settlement_id INTEGER, user_id INTEGER, rank_no INTEGER, score INTEGER, reward_amount TEXT)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, nickname TEXT, email TEXT)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`INSERT INTO leaderboard_settlements(id, business_date, period_start, period_end, status, settled_at) VALUES (1, '2026-08-30', ?, ?, 'completed', ?)`, now.Add(-24*time.Hour), now, now).Error)
	require.NoError(t, db.Exec(`INSERT INTO users(id, nickname, email) VALUES (7, '', 'fallback@example.com')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO leaderboard_rewards(settlement_id, user_id, rank_no, score, reward_amount) VALUES (1, 7, 1, 9, '12.340000')`).Error)

	settlement, err := (&BillingRepo{db: db}).LatestLeaderboardRewards(context.Background(), 10)

	require.NoError(t, err)
	require.Len(t, settlement.Rewards, 1)
	require.Equal(t, "fallback@example.com", settlement.Rewards[0].Email)
	require.Equal(t, "12.34", settlement.Rewards[0].Amount)
}
