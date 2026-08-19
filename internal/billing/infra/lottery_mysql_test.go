package infra

import (
	"context"
	"sync"
	"testing"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

func TestLotterySystemGrantSettlementMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	require.True(t, db.Migrator().HasTable(&LotterySettlementModel{}))
	require.True(t, db.Migrator().HasColumn("lotteries", "unused_amount"))
	require.True(t, db.Migrator().HasColumn("lotteries", "request_fingerprint"))
	require.False(t, db.Migrator().HasColumn("lotteries", "refund_amount"))

	creatorID := createBillingTestUser(t, db, "lottery-creator@example.com")
	winnerOne := createBillingTestUser(t, db, "lottery-winner-one@example.com")
	winnerTwo := createBillingTestUser(t, db, "lottery-winner-two@example.com")

	result := db.Exec(`
INSERT INTO lotteries(
    public_token, created_by_user_id, funding_user_id, title,
    total_amount, min_payout, max_payout, tier_weights,
    min_account_age_days, draw_at, participant_target, max_participants,
    status, algorithm_version, unused_amount, idempotency_key, request_fingerprint
) VALUES (
    'mysql-system-grant', ?, ?, 'System grant',
    10, 1, 8, JSON_OBJECT('consolation', 80, 'normal', 15, 'lucky', 5),
    0, DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL 1 HOUR), 2, 2,
    'settling', 'bounded-tier-v1', 0, 'mysql-system-grant', REPEAT('a', 64)
)`, creatorID, creatorID)
	require.NoError(t, result.Error)

	var lotteryID uint
	require.NoError(t, db.Raw("SELECT id FROM lotteries WHERE public_token = ?", "mysql-system-grant").Scan(&lotteryID).Error)
	require.NotZero(t, lotteryID)
	command := billingapp.LotterySettlementRequest{
		LotteryID: lotteryID, TotalAmount: "10.00", UnusedAmount: "1.00",
		Awards: []billingapp.LotteryAward{{UserID: winnerOne, Amount: "4.00"}, {UserID: winnerTwo, Amount: "5.00"}},
	}
	command.RequestFingerprint = billingapp.LotterySettlementFingerprint(command)
	repo := NewBillingRepo(db)

	results := make([]*billingapp.LotterySettlementResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = repo.SettleLotteryPool(context.Background(), command)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.Len(t, results[i].Awards, 2)
	}
	require.NotEqual(t, results[0].Replayed, results[1].Replayed)

	var creatorWallets, awardTransactions int64
	require.NoError(t, db.Model(&WalletModel{}).Where("user_id = ?", creatorID).Count(&creatorWallets).Error)
	require.Zero(t, creatorWallets)
	require.NoError(t, db.Model(&WalletTransactionModel{}).Where("biz_type = ?", "lottery_award").Count(&awardTransactions).Error)
	require.EqualValues(t, 2, awardTransactions)

	changed := command
	changed.Awards = []billingapp.LotteryAward{{UserID: winnerOne, Amount: "3.00"}, {UserID: winnerTwo, Amount: "6.00"}}
	changed.RequestFingerprint = billingapp.LotterySettlementFingerprint(changed)
	_, err := repo.SettleLotteryPool(context.Background(), changed)
	require.ErrorIs(t, err, domain.ErrIdempotencyConflict)
}
