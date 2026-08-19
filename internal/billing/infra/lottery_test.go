package infra

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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
