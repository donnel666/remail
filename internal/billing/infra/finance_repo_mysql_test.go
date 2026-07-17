package infra

import (
	"context"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestFinanceSummaryBucketsMatchDBTimezoneMySQL guards the SQL↔Go bucketing
// seam. The ledger SQL groups by DATE_FORMAT(created_at) in the DB session zone
// (loc=Local) while buildFinanceSummary keys its buckets in time.Local. If those
// desync (e.g. Go keying in UTC under a non-UTC deployment) every bucket lookup
// misses and the whole trend + totals silently zero out. Seeds real rows across
// two hours and asserts they land in the right buckets, with a compensating
// reversal row excluded.
func TestFinanceSummaryBucketsMatchDBTimezoneMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "summary@example.com")
	uc := billingapp.NewWalletUseCase(NewBillingRepo(db))

	seedFinanceTxn(t, db, userID, "SUMTX-RECH", "recharge", "in", "0.000000", "50.000000",
		time.Date(2026, 3, 15, 9, 15, 0, 0, time.Local))
	seedFinanceTxn(t, db, userID, "SUMTX-SPEND", "debit", "out", "100.000000", "-100.000000",
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

// seedFinanceTxn inserts one ledger row satisfying the balance check constraint
// (balance_after = balance_before + amount).
func seedFinanceTxn(t *testing.T, db *gorm.DB, userID uint, no, txType, direction, before, amount string, at time.Time) {
	t.Helper()
	after := decimal.RequireFromString(before).Add(decimal.RequireFromString(amount))
	require.NoError(t, db.Create(&WalletTransactionModel{
		TransactionNo: no, UserID: userID, TransactionType: txType,
		BalanceBucket: "consumer", Direction: direction, Amount: amount,
		BalanceBefore: before, BalanceAfter: after.StringFixed(6), BizType: "test", BizID: "test",
		CreatedAt: at,
	}).Error)
}
