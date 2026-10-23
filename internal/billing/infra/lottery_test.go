package infra

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLookupLotteryScoreStatsClassifiesCreditsAndExcludesReversals(t *testing.T) {
	db := newLotterySettlementTestDB(t)
	rows := []WalletTransactionModel{
		{UserID: 7, TransactionType: "recharge", BizType: "recharge_alipay", Amount: "1000.125"},
		{UserID: 7, TransactionType: "card_redeem", BizType: "card_key", Amount: "500.25"},
		{UserID: 7, TransactionType: "credit", BizType: "lottery_award", Amount: "1000"},
		{UserID: 7, TransactionType: "credit", BizType: "daily_checkin", Amount: "50.125"},
		{UserID: 7, TransactionType: "credit", BizType: "registration_reward", Amount: "25.5"},
		{UserID: 7, TransactionType: "credit", BizType: "admin_wallet_adjustment", Amount: "75.25"},
		{UserID: 7, TransactionType: "credit", BizType: "leaderboard_reward", Amount: "2000"},
		{UserID: 7, TransactionType: "credit", BizType: "invitation_reward", Amount: "300"},
		{UserID: 7, TransactionType: "credit", BizType: "referral_transfer", Amount: "400"},
		{UserID: 7, TransactionType: "refund", BizType: "order_refund", Amount: "500"},
		{UserID: 7, TransactionType: "transfer", BizType: "supplier_transfer", Amount: "700"},
		{UserID: 7, TransactionType: "credit", BizType: "supplier_settlement", Amount: "900", BalanceBucket: "supplier_available"},
		{UserID: 7, TransactionType: "credit", BizType: "daily_checkin", Amount: "-100", Direction: "out"},
		{UserID: 7, TransactionType: "credit", BizType: "daily_checkin", Amount: "-100"},
		{UserID: 7, TransactionType: "recharge", Amount: "10000", TransactionNo: "reversed-recharge"},
		{UserID: 7, TransactionType: "credit", BizType: "daily_checkin", Amount: "20000", TransactionNo: "reversed-free"},
		{UserID: 7, TransactionType: "credit", BizType: "leaderboard_reward", Amount: "30000", TransactionNo: "reversed-earned"},
		{UserID: 8, TransactionType: "credit", BizType: "registration_reward", Amount: "20"},
		{UserID: 99, TransactionType: "recharge", Amount: "1000"},
	}
	for _, original := range []string{"reversed-recharge", "reversed-free", "reversed-earned"} {
		rows = append(rows, WalletTransactionModel{UserID: 7, TransactionType: "manual_adjustment", BizType: "reversal", Amount: "-1", Direction: "out", ReversalOfNo: &original})
	}
	for i := range rows {
		if rows[i].TransactionNo == "" {
			rows[i].TransactionNo = fmt.Sprintf("score-%d", i)
		}
		if rows[i].BalanceBucket == "" {
			rows[i].BalanceBucket = "consumer"
		}
		if rows[i].Direction == "" {
			rows[i].Direction = "in"
		}
		rows[i].BalanceBefore, rows[i].BalanceAfter = "0", "0"
	}
	require.NoError(t, db.Create(&rows).Error)
	wallets := billingapp.NewWalletUseCase(NewBillingRepo(db))
	stats, err := wallets.LookupLotteryScoreStats(context.Background(), []uint{7, 7, 8, 9})
	require.NoError(t, err)
	require.Len(t, stats, 2)
	require.Equal(t, "1500.375", stats[7].RechargePoints.String())
	require.Equal(t, "2700", stats[7].EarnedPoints.String())
	require.Equal(t, "1150.875", stats[7].FreePoints.String())
	require.Equal(t, "20", stats[8].FreePoints.String())
	require.True(t, stats[9].RechargePoints.IsZero())
	empty, err := wallets.LookupLotteryScoreStats(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestLookupLotteryScoreStatsUsesDrawTransaction(t *testing.T) {
	db := newLotterySettlementTestDB(t)
	repo := NewBillingRepo(db)
	rollback := errors.New("rollback score snapshot")
	err := db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Create(&WalletTransactionModel{
			TransactionNo: "transaction-score", UserID: 7, TransactionType: "credit", BizType: "lottery_award",
			BalanceBucket: "consumer", Direction: "in", Amount: "1000", BalanceBefore: "0", BalanceAfter: "1000",
		}).Error)
		stats, err := repo.LookupLotteryScoreStats(platform.WithGormTx(context.Background(), tx), []uint{7})
		require.NoError(t, err)
		require.Equal(t, "1000", stats[7].FreePoints.String())
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	stats, err := repo.LookupLotteryScoreStats(context.Background(), []uint{7})
	require.NoError(t, err)
	require.Empty(t, stats)
}

func newLotterySettlementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:lottery-settlement-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&WalletModel{}, &WalletTransactionModel{}, &LotterySettlementModel{}))
	return db
}

func settlementCommand() billingapp.LotterySettlementCommand {
	req := billingapp.LotterySettlementRequest{
		LotteryID: 17, TotalAmount: "10.00", UnusedAmount: "1.00",
		Awards: []billingapp.LotteryAward{{UserID: 2, Amount: "4.00"}, {UserID: 3, Amount: "5.00"}},
	}
	req.RequestFingerprint = billingapp.LotterySettlementFingerprint(req)
	return req
}

func TestSettleLotteryPoolIsAtomicAndIdempotent(t *testing.T) {
	db := newLotterySettlementTestDB(t)
	repo := NewBillingRepo(db)

	result, err := repo.SettleLotteryPool(context.Background(), settlementCommand())
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Len(t, result.Awards, 2)

	var wallets []WalletModel
	require.NoError(t, db.Order("user_id").Find(&wallets).Error)
	require.Len(t, wallets, 2)
	for index, expected := range []string{"4.00", "5.00"} {
		actualAmount, parseErr := domain.ParseMoney(wallets[index].ConsumerBalance)
		require.NoError(t, parseErr)
		expectedAmount, parseErr := domain.ParseMoney(expected)
		require.NoError(t, parseErr)
		require.True(t, actualAmount.Equal(expectedAmount))
	}
	var transactionCount int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).Count(&transactionCount).Error)
	require.EqualValues(t, 2, transactionCount)

	second := settlementCommand()
	replayed, err := repo.SettleLotteryPool(context.Background(), second)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.NoError(t, db.Model(&WalletTransactionModel{}).Count(&transactionCount).Error)
	require.EqualValues(t, 2, transactionCount)
}

func TestSettleLotteryPoolRequiresFingerprint(t *testing.T) {
	db := newLotterySettlementTestDB(t)
	command := settlementCommand()
	command.RequestFingerprint = ""

	_, err := NewBillingRepo(db).SettleLotteryPool(context.Background(), command)
	require.ErrorIs(t, err, domain.ErrIdempotencyRequired)

	var settlementCount int64
	require.NoError(t, db.Model(&LotterySettlementModel{}).Count(&settlementCount).Error)
	require.Zero(t, settlementCount)
}

func TestSettleLotteryPoolRollsBackWholeBatch(t *testing.T) {
	db := newLotterySettlementTestDB(t)
	var created atomic.Int32
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fail-second-lottery-transaction", func(tx *gorm.DB) {
		if tx.Statement.Table == "wallet_transactions" && created.Add(1) == 2 {
			_ = tx.AddError(errors.New("forced transaction failure"))
		}
	}))

	_, err := NewBillingRepo(db).SettleLotteryPool(context.Background(), settlementCommand())
	require.ErrorContains(t, err, "forced transaction failure")
	var transactionCount, settlementCount int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).Count(&transactionCount).Error)
	require.NoError(t, db.Model(&LotterySettlementModel{}).Count(&settlementCount).Error)
	require.Zero(t, transactionCount)
	require.Zero(t, settlementCount)
	var wallets []WalletModel
	require.NoError(t, db.Find(&wallets).Error)
	for _, wallet := range wallets {
		require.Equal(t, "0", wallet.ConsumerBalance)
	}
}

func TestSettleLotteryPoolRejectsDifferentAwardsForSameLottery(t *testing.T) {
	db := newLotterySettlementTestDB(t)
	repo := NewBillingRepo(db)
	first := settlementCommand()
	_, err := repo.SettleLotteryPool(context.Background(), first)
	require.NoError(t, err)

	second := settlementCommand()
	second.Awards = []billingapp.LotteryAward{{UserID: 2, Amount: "3.00"}, {UserID: 3, Amount: "6.00"}}
	second.RequestFingerprint = billingapp.LotterySettlementFingerprint(second)
	_, err = repo.SettleLotteryPool(context.Background(), second)
	require.ErrorIs(t, err, domain.ErrIdempotencyConflict)

	var transactionCount int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).Count(&transactionCount).Error)
	require.EqualValues(t, 2, transactionCount)
}
