package infra

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	lotteryinfra "github.com/donnel666/remail/internal/lottery/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestLotteryDrawReadsAwardsCommittedBeforeUserLockMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	creatorID := createBillingTestUser(t, db, "lottery-snapshot-creator@example.com")
	winnerID := createBillingTestUser(t, db, "lottery-snapshot-winner@example.com")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	lotteries := lotteryinfra.NewRepo(db)
	wallets := billingapp.NewWalletUseCase(NewBillingRepo(db))
	target := 1
	activities := make([]lotterydomain.Lottery, 2)
	for i := range activities {
		key := fmt.Sprintf("score-snapshot-%d", i)
		activities[i] = lotterydomain.Lottery{
			PublicToken: key, CreatedByUserID: creatorID, Title: key,
			TotalAmount: "1000.00", MinPayout: "1.00", MaxPayout: "1000.00",
			TierWeights:       lotterydomain.TierWeights{Lucky: 1},
			ParticipantTarget: &target, ParticipantCount: 1, MaxParticipants: 1,
			Status: lotterydomain.StatusSettling, AlgorithmVersion: "fixed-tier-v3", UnusedAmount: "0.00",
			IdempotencyKey: key, RequestFingerprint: strings.Repeat("a", 64),
		}
		require.NoError(t, lotteries.Create(ctx, &activities[i]))
		require.NoError(t, db.Create(&lotteryinfra.EntryModel{
			LotteryID: activities[i].ID, UserID: winnerID, RegisteredAt: time.Now().UTC(),
		}).Error)
	}

	first := db.WithContext(ctx).Begin()
	require.NoError(t, first.Error)
	defer first.Rollback()
	firstCtx := platform.WithGormTx(ctx, first)
	require.NoError(t, lotteries.LockWinnerUsers(firstCtx, []uint{winnerID}))
	require.NoError(t, lotteries.SavePayouts(firstCtx, activities[0].ID, []lotterydomain.Payout{
		{UserID: winnerID, Tier: lotterydomain.TierLucky, Amount: "1000.00"},
	}))
	settlement, err := wallets.SettleLotteryPool(firstCtx, billingapp.LotterySettlementRequest{
		LotteryID: activities[0].ID, TotalAmount: "1000.00", UnusedAmount: "0.00",
		Awards: []billingapp.LotteryAward{{UserID: winnerID, Amount: "1000.00"}},
	})
	require.NoError(t, err)
	require.NoError(t, lotteries.RecordBillingTransactions(firstCtx, activities[0].ID,
		map[uint]string{winnerID: settlement.Awards[0].Transaction.TransactionNo}, "0.00"))
	require.NoError(t, lotteries.Complete(firstCtx, activities[0].ID, lotterydomain.StatusCompleted, "0.00", time.Now().UTC()))

	err = lotteries.WithDrawTransaction(ctx, activities[1].ID, func(secondCtx context.Context, activity *lotterydomain.Lottery) error {
		// Match drawSettling's reads before the first draw commits and releases its user lock.
		entries, err := lotteries.ListAllEntries(secondCtx, activity.ID)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		existing, err := lotteries.GetPayouts(secondCtx, activity.ID)
		require.NoError(t, err)
		require.Empty(t, existing)
		require.NoError(t, first.Commit().Error)
		require.NoError(t, lotteries.LockWinnerUsers(secondCtx, []uint{winnerID}))

		points, err := wallets.LookupLotteryScoreStats(secondCtx, []uint{winnerID})
		require.NoError(t, err)
		require.Equal(t, "1000", points[winnerID].FreePoints.String())
		history, err := lotteries.LookupWinnerStats(secondCtx, []uint{winnerID})
		require.NoError(t, err)
		require.Equal(t, int64(1), history[winnerID].LuckyCount)
		return nil
	})
	require.NoError(t, err)
}

func TestLookupLotteryScoreStatsPreservesDecimalPrecisionMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	userID := createBillingTestUser(t, db, "lottery-score@example.com")
	rows := []WalletTransactionModel{
		{TransactionType: "recharge", BizType: "recharge_alipay", Amount: "123456789012.123456"},
		{TransactionType: "card_redeem", BizType: "card_key", Amount: "0.000001"},
		{TransactionType: "credit", BizType: "leaderboard_reward", Amount: "0.123456"},
		{TransactionType: "credit", BizType: "invitation_reward", Amount: "0.000001"},
		{TransactionType: "credit", BizType: "referral_transfer", Amount: "0.000002"},
		{TransactionType: "credit", BizType: "lottery_award", Amount: "0.000001"},
		{TransactionType: "credit", BizType: "daily_checkin", Amount: "0.000002"},
		{TransactionType: "credit", BizType: "registration_reward", Amount: "1000", TransactionNo: "mysql-reversed-gift"},
	}
	for i := range rows {
		rows[i].UserID = userID
		if rows[i].TransactionNo == "" {
			rows[i].TransactionNo = fmt.Sprintf("mysql-score-%d", i)
		}
		rows[i].BizID = rows[i].TransactionNo
		rows[i].BalanceBucket, rows[i].Direction = "consumer", "in"
		rows[i].BalanceBefore, rows[i].BalanceAfter = "0", rows[i].Amount
	}
	original := "mysql-reversed-gift"
	rows = append(rows, WalletTransactionModel{
		UserID: userID, TransactionNo: "mysql-score-reversal", TransactionType: "manual_adjustment", BizType: "reversal", BizID: original,
		BalanceBucket: "consumer", Direction: "out", Amount: "-1000", BalanceBefore: "1000", BalanceAfter: "0", ReversalOfNo: &original,
	})
	require.NoError(t, db.Create(&rows).Error)
	stats, err := NewBillingRepo(db).LookupLotteryScoreStats(context.Background(), []uint{userID})
	require.NoError(t, err)
	require.Equal(t, "123456789012.123457", stats[userID].RechargePoints.String())
	require.Equal(t, "0.123459", stats[userID].EarnedPoints.String())
	require.Equal(t, "0.000003", stats[userID].FreePoints.String())
}

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
    lottery_type, starting_amount, total_amount, pool_increment_amount,
    min_payout, max_payout, tier_weights,
    min_account_age_days, draw_at, participant_target, max_participants,
    status, algorithm_version, unused_amount, idempotency_key, request_fingerprint
) VALUES (
    'mysql-system-grant', ?, ?, 'System grant',
    'fixed', 10, 10, 0, 1, 8, JSON_OBJECT('consolation', 80, 'normal', 15, 'lucky', 5),
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
