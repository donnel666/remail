package infra

import (
	"context"
	"errors"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSetCardsExpireAtRollsBackFailedChunk(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	runtimeconfig.Set("card_bulk_chunk_size", "1")
	t.Cleanup(func() { runtimeconfig.Delete("card_bulk_chunk_size") })
	require.NoError(t, db.Create(&[]CardKeyModel{
		{Key: "EXP-TX-A", Amount: "1.000000", Status: "enabled", MaxRedemptions: 1},
		{Key: "EXP-TX-B", Amount: "1.000000", Status: "enabled", MaxRedemptions: 1},
	}).Error)

	updates := 0
	callbackName := "test:fail_second_card_expiration_chunk"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates++
		if updates == 2 {
			_ = tx.AddError(errors.New("forced second chunk failure"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(callbackName)) })

	affected, err := NewBillingRepo(db).SetCardsExpireAt(
		context.Background(),
		[]string{"EXP-TX-A", "EXP-TX-B"},
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	)
	require.ErrorContains(t, err, "forced second chunk failure")
	require.Zero(t, affected)

	var cards []CardKeyModel
	require.NoError(t, db.Where("card_key IN ?", []string{"EXP-TX-A", "EXP-TX-B"}).Find(&cards).Error)
	require.Len(t, cards, 2)
	for _, card := range cards {
		require.Nil(t, card.ExpireAt)
	}
}

// TestFinanceSummaryBucketsMatchDBTimezoneMySQL guards the SQL↔Go bucketing
// seam. The ledger SQL groups by DATE_FORMAT(created_at) in Asia/Shanghai while
// buildFinanceSummary keys its buckets in time.Local, which CI pins to Shanghai.
// If those
// desync (e.g. Go keying in UTC under a non-UTC deployment) every bucket lookup
// misses and the whole trend + totals silently zero out. Seeds real rows across
// two hours and asserts they land in the right buckets, with a compensating
// reversal row excluded.
func TestFinanceSummaryBucketsMatchDBTimezoneMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "summary@example.com")
	uc := billingapp.NewWalletUseCase(NewBillingRepo(db))

	seedFinanceTxn(t, db, userID, "SUMTX-RECH", "recharge", "in", "test", "0.000000", "50.000000",
		time.Date(2026, 3, 15, 9, 15, 0, 0, time.Local))
	seedFinanceTxn(t, db, userID, "SUMTX-SPEND", "debit", "out", "test", "100.000000", "-100.000000",
		time.Date(2026, 3, 15, 10, 30, 0, 0, time.Local))
	// A compensating reversal entry in range must be excluded from aggregates.
	reversed := "SUMTX-SPEND"
	require.NoError(t, db.Create(&WalletTransactionModel{
		TransactionNo: "SUMTX-REV", UserID: userID, TransactionType: "manual_adjustment",
		BalanceBucket: "consumer", Direction: "in", Amount: "100.000000",
		BalanceBefore: "0.000000", BalanceAfter: "100.000000", BizType: "reversal", BizID: "SUMTX-SPEND",
		ReversalOfNo: &reversed, CreatedAt: time.Date(2026, 3, 15, 10, 40, 0, 0, time.Local),
	}).Error)

	from := time.Date(2026, 3, 15, 9, 0, 0, 0, time.Local)
	to := time.Date(2026, 3, 15, 11, 0, 0, 0, time.Local)
	res, err := uc.FinanceSummary(ctx, &from, &to)
	require.NoError(t, err)

	require.Equal(t, "50.00", res.RechargeAmount, "recharge must land in its bucket (SQL/Go tz aligned)")
	require.Equal(t, "100.00", res.SpendAmount, "spend must land in its bucket, reversal excluded")
	require.Len(t, res.Trend, 3, "same-day range buckets hourly 09:00..11:00")
	byLabel := map[string]billingapp.TrendPoint{}
	for _, p := range res.Trend {
		byLabel[p.Label] = p
	}
	require.Equal(t, 50.0, byLabel["09:00"].Recharge)
	require.Equal(t, 100.0, byLabel["10:00"].Spend)
	require.Equal(t, 0.0, byLabel["11:00"].Spend, "empty bucket is zero")
}

func TestAdminTransactionBusinessCategoriesMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "transaction-facets@example.com")
	at := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)

	seedFinanceTxn(t, db, userID, "CAT-ALIPAY", "recharge", "in", "recharge", "0.000000", "10.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-CARD", "card_redeem", "in", "card_key", "10.000000", "5.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-SPEND", "debit", "out", "order", "15.000000", "-2.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-REFUND", "refund", "in", "wallet_refund", "13.000000", "2.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-REFERRAL", "credit", "in", "referral_transfer", "15.000000", "3.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-CHECKIN", "credit", "in", "daily_checkin", "18.000000", "1.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-LEADERBOARD", "credit", "in", "leaderboard_reward", "19.000000", "2.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-REGISTRATION", "credit", "in", "registration_reward", "21.000000", "3.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-INVITATION", "credit", "in", "invitation_reward", "24.000000", "3.000000", at)
	seedFinanceTxn(t, db, userID, "CAT-REGISTRATION-OLD", "credit", "in", "registration_reward", "24.000000", "3.000000", at.Add(-2*time.Hour))
	seedFinanceTxn(t, db, userID, "CAT-ADMIN", "credit", "in", "admin_wallet_adjustment", "27.000000", "4.000000", at)

	repo := NewBillingRepo(db)
	expectedFacets := billingapp.AdminTransactionFacets{
		All: 10, Recharge: 2, Spend: 1, Refund: 1, ReferralCashback: 2, Activity: 4,
	}
	tests := []struct {
		category billingapp.AdminTransactionCategory
		nos      []string
	}{
		{billingapp.AdminTransactionCategoryAll, []string{"CAT-ALIPAY", "CAT-CARD", "CAT-SPEND", "CAT-REFUND", "CAT-REFERRAL", "CAT-CHECKIN", "CAT-LEADERBOARD", "CAT-REGISTRATION", "CAT-INVITATION", "CAT-REGISTRATION-OLD"}},
		{billingapp.AdminTransactionCategoryRecharge, []string{"CAT-ALIPAY", "CAT-CARD"}},
		{billingapp.AdminTransactionCategorySpend, []string{"CAT-SPEND"}},
		{billingapp.AdminTransactionCategoryRefund, []string{"CAT-REFUND"}},
		{billingapp.AdminTransactionCategoryReferralCashback, []string{"CAT-REFERRAL", "CAT-INVITATION"}},
		{billingapp.AdminTransactionCategoryActivity, []string{"CAT-CHECKIN", "CAT-LEADERBOARD", "CAT-REGISTRATION", "CAT-REGISTRATION-OLD"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.category), func(t *testing.T) {
			items, total, facets, err := repo.ListAdminTransactions(ctx, billingapp.AdminTransactionFilter{Category: tt.category}, 0, 20)
			require.NoError(t, err)
			require.EqualValues(t, len(tt.nos), total)
			nos := make([]string, len(items))
			for i := range items {
				nos[i] = items[i].Transaction.TransactionNo
			}
			require.ElementsMatch(t, tt.nos, nos)
			require.Equal(t, expectedFacets, facets)
		})
	}

	from, to := at.Add(-time.Minute), at.Add(time.Minute)
	items, total, facets, err := repo.ListAdminTransactions(ctx, billingapp.AdminTransactionFilter{
		Category:    billingapp.AdminTransactionCategoryActivity,
		Search:      "CAT-REGISTRATION",
		CreatedFrom: &from,
		CreatedTo:   &to,
	}, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, "CAT-REGISTRATION", items[0].Transaction.TransactionNo)
	require.Equal(t, billingapp.AdminTransactionFacets{All: 1, Activity: 1}, facets)
}

// seedFinanceTxn inserts one ledger row satisfying the balance check constraint
// (balance_after = balance_before + amount).
func seedFinanceTxn(t *testing.T, db *gorm.DB, userID uint, no, txType, direction, bizType, before, amount string, at time.Time) {
	t.Helper()
	after := decimal.RequireFromString(before).Add(decimal.RequireFromString(amount))
	require.NoError(t, db.Create(&WalletTransactionModel{
		TransactionNo: no, UserID: userID, TransactionType: txType,
		BalanceBucket: "consumer", Direction: direction, Amount: amount,
		BalanceBefore: before, BalanceAfter: after.StringFixed(6), BizType: bizType, BizID: "test",
		CreatedAt: at,
	}).Error)
}
