package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSupplierSettlementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:supplier-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&WalletModel{}, &WalletTransactionModel{}, &SupplierSettlementModel{}, &IdempotencyKeyModel{}))
	require.NoError(t, db.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, debit_tx_id INTEGER, refund_tx_id INTEGER)").Error)
	return db
}

func TestOrderLinkedTransactionsRejectReversalByReference(t *testing.T) {
	db := newSupplierSettlementTestDB(t)
	wallet := defaultWalletModel(7)
	wallet.ConsumerBalance = "100.00"
	require.NoError(t, db.Create(&wallet).Error)
	debit := WalletTransactionModel{TransactionNo: "ORDER-DEBIT", UserID: 7, TransactionType: "debit", BalanceBucket: "consumer", Direction: "out", Amount: "-80.00", BalanceBefore: "100.00", BalanceAfter: "20.00", BizType: "admin_wallet_adjustment", BizID: "legacy-note"}
	refund := WalletTransactionModel{TransactionNo: "ORDER-REFUND", UserID: 7, TransactionType: "refund", BalanceBucket: "consumer", Direction: "in", Amount: "80.00", BalanceBefore: "20.00", BalanceAfter: "100.00", BizType: "wallet_refund", BizID: "legacy-note"}
	require.NoError(t, db.Create(&debit).Error)
	require.NoError(t, db.Create(&refund).Error)
	require.NoError(t, db.Exec("INSERT INTO orders(id, debit_tx_id, refund_tx_id) VALUES (1, ?, ?)", debit.ID, refund.ID).Error)
	repo := NewBillingRepo(db)
	for _, original := range []WalletTransactionModel{debit, refund} {
		read, err := repo.GetAdminTransaction(context.Background(), original.ID)
		require.NoError(t, err)
		require.True(t, read.OrderLinked)
		_, err = repo.ReverseTransaction(context.Background(), billingapp.ReverseTransactionCommand{Original: transactionModelToDomain(original), IdempotencyKey: original.TransactionNo, RequestFingerprint: original.TransactionNo})
		require.ErrorIs(t, err, domain.ErrTransactionNotReversible)
	}
	items, total, _, err := repo.ListAdminTransactions(context.Background(), billingapp.AdminTransactionFilter{}, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	for _, item := range items {
		require.True(t, item.OrderLinked)
	}
	var after WalletModel
	require.NoError(t, db.First(&after, "user_id = 7").Error)
	require.Equal(t, "100.00", normalizeMoneyString(after.ConsumerBalance))

	// A note resembling an order number does not turn an unrelated adjustment into an order payment.
	manual := WalletTransactionModel{TransactionNo: "MANUAL-CREDIT", UserID: 7, TransactionType: "credit", BalanceBucket: "consumer", Direction: "in", Amount: "10.00", BalanceBefore: "90.00", BalanceAfter: "100.00", BizType: "admin_wallet_adjustment", BizID: "order:unlinked-note"}
	require.NoError(t, db.Create(&manual).Error)
	_, err = repo.ReverseTransaction(context.Background(), billingapp.ReverseTransactionCommand{Original: transactionModelToDomain(manual), IdempotencyKey: "manual-reversal", RequestFingerprint: "manual-reversal"})
	require.NoError(t, err)
	require.NoError(t, db.First(&after, "user_id = 7").Error)
	require.Equal(t, "90.00", normalizeMoneyString(after.ConsumerBalance))
}

func assertSupplierWallet(t *testing.T, db *gorm.DB, available, frozen string, transactions int64) {
	t.Helper()
	var wallet WalletModel
	require.NoError(t, db.First(&wallet, "user_id = 7").Error)
	require.Equal(t, available, normalizeMoneyString(wallet.SupplierAvailable))
	require.Equal(t, frozen, normalizeMoneyString(wallet.SupplierFrozen))
	var count int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).Count(&count).Error)
	require.Equal(t, transactions, count)
}

func TestSupplierSettlementLifecycleIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newSupplierSettlementTestDB(t)
	repo := NewBillingRepo(db)
	due := time.Now().UTC().Add(time.Hour)
	req := billingapp.SupplierSettlementRequest{OrderNo: "OR-SUPPLIER", SupplierUserID: 7, Amount: "48.000001", AvailableAt: due}
	require.NoError(t, repo.FreezeSupplierSettlement(ctx, req))
	require.NoError(t, repo.FreezeSupplierSettlement(ctx, req))
	assertSupplierWallet(t, db, "0.00", "48.000001", 1)
	changed := req
	changed.Amount = "48.00"
	require.ErrorIs(t, repo.FreezeSupplierSettlement(ctx, changed), domain.ErrSupplierSettlementConflict)
	credited, err := repo.CreditSupplierSettlement(ctx, req.OrderNo, due.Add(-time.Second))
	require.False(t, credited)
	require.ErrorIs(t, err, domain.ErrSupplierSettlementConflict)
	// Fail the second ledger write: releasing the frozen bucket must also roll back.
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_supplier_credit", func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*WalletTransactionModel); ok && row.BalanceBucket == "supplier_available" {
			_ = tx.AddError(fmt.Errorf("credit failed"))
		}
	}))
	credited, err = repo.CreditSupplierSettlement(ctx, req.OrderNo, due)
	require.False(t, credited)
	require.ErrorContains(t, err, "credit failed")
	assertSupplierWallet(t, db, "0.00", "48.000001", 1)
	require.NoError(t, db.Callback().Create().Remove("test:fail_supplier_credit"))
	credited, err = repo.CreditSupplierSettlement(ctx, req.OrderNo, due)
	require.NoError(t, err)
	require.True(t, credited)
	credited, err = repo.CreditSupplierSettlement(ctx, req.OrderNo, due)
	require.NoError(t, err)
	require.False(t, credited)
	assertSupplierWallet(t, db, "48.000001", "0.00", 3)
	require.NoError(t, repo.CancelSupplierSettlement(ctx, req.OrderNo))
	require.NoError(t, repo.CancelSupplierSettlement(ctx, req.OrderNo))
	assertSupplierWallet(t, db, "0.00", "0.00", 4)
	items, total, facets, err := repo.ListAdminTransactions(ctx, billingapp.AdminTransactionFilter{Category: billingapp.AdminTransactionCategoryAll}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 4)
	require.EqualValues(t, 4, total)
	require.Zero(t, facets.Activity)
	require.ErrorIs(t, repo.FreezeSupplierSettlement(ctx, req), domain.ErrSupplierSettlementConflict)
	var row WalletTransactionModel
	require.NoError(t, db.Where("balance_bucket = 'supplier_available'").First(&row).Error)
	_, err = repo.reverseTransactionInTx(ctx, db, billingapp.ReverseTransactionCommand{Original: transactionModelToDomain(row), IdempotencyKey: "reverse", RequestFingerprint: "reverse"})
	require.ErrorIs(t, err, domain.ErrTransactionNotReversible)
}

func TestSupplierSettlementCancellationAndInsufficientClawback(t *testing.T) {
	for _, credited := range []bool{false, true} {
		t.Run(fmt.Sprint(credited), func(t *testing.T) {
			ctx := context.Background()
			db := newSupplierSettlementTestDB(t)
			repo := NewBillingRepo(db)
			due := time.Now().UTC()
			req := billingapp.SupplierSettlementRequest{OrderNo: "OR-CANCEL", SupplierUserID: 7, Amount: "12.00", AvailableAt: due}
			require.NoError(t, repo.FreezeSupplierSettlement(ctx, req))
			if credited {
				changed, err := repo.CreditSupplierSettlement(ctx, req.OrderNo, due)
				require.NoError(t, err)
				require.True(t, changed)
				require.NoError(t, db.Model(&WalletModel{}).Where("user_id = 7").Update("supplier_available", "0.00").Error)
				require.ErrorIs(t, repo.CancelSupplierSettlement(ctx, req.OrderNo), domain.ErrSupplierSettlementBalance)
				var row SupplierSettlementModel
				require.NoError(t, db.First(&row, "order_no = ?", req.OrderNo).Error)
				require.Equal(t, "credited", row.Status)
				assertSupplierWallet(t, db, "0.00", "0.00", 3)
			} else {
				require.NoError(t, repo.CancelSupplierSettlement(ctx, req.OrderNo))
				require.NoError(t, repo.CancelSupplierSettlement(ctx, req.OrderNo))
				assertSupplierWallet(t, db, "0.00", "0.00", 2)
				changed, err := repo.CreditSupplierSettlement(ctx, req.OrderNo, due)
				require.False(t, changed)
				require.ErrorIs(t, err, domain.ErrSupplierSettlementConflict)
			}
		})
	}
}
