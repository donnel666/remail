package api

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestSupplierSettlementDoesNotLockBuyerWalletMySQL(t *testing.T) {
	db, uc, _, order := supplierCheckoutFixture(t, "purchase")
	expireSupplierOrder(t, db, order.Order.OrderNo, "purchase")
	hold := db.Begin()
	require.NoError(t, hold.Error)
	t.Cleanup(func() { _ = hold.Rollback().Error })
	var wallet billinginfra.WalletModel
	require.NoError(t, hold.Clauses(clause.Locking{Strength: "UPDATE"}).First(&wallet, "user_id = 2").Error)
	result, err := uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Zero(t, result.Failed)
	require.Equal(t, 1, result.SupplierSettled)
	supplierWalletState(t, db, "0.000000", "48.000000", "920.000000")
}

func TestSupplierSettlementBlockedOrderDoesNotBlockOtherSupplierMySQL(t *testing.T) {
	for _, lockOrder := range []bool{false, true} {
		name := "supplier_wallet"
		if lockOrder {
			name = "order"
		}
		t.Run(name, func(t *testing.T) {
			db, uc, req, first := supplierCheckoutFixture(t, "purchase")
			require.NoError(t, db.Exec("INSERT INTO users(id,email,password_hash,nickname,status,role) VALUES (4,'supplier4@test.local','hash','s4','active','supplier')").Error)
			require.NoError(t, db.Table("email_resources").Where("id = 1001").Update("owner_user_id", 4).Error)
			creditBuyer(t, db, 3, "1000")
			req.UserID, req.IdempotencyKey = 3, "other-supplier"
			second, err := uc.Checkout(context.Background(), req)
			require.NoError(t, err)
			require.EqualValues(t, 4, *second.Order.SupplierUserID)
			expireSupplierOrder(t, db, first.Order.OrderNo, "purchase")
			expireSupplierOrder(t, db, second.Order.OrderNo, "purchase")
			require.NoError(t, db.Table("supplier_settlements").Where("order_no = ?", first.Order.OrderNo).Update("available_at", time.Now().UTC().Add(-2*time.Minute)).Error)
			if lockOrder {
				// Exercise the earlier purchase lifecycle phase as well as the settlement phase.
				require.NoError(t, db.Table("orders").Where("order_no = ?", first.Order.OrderNo).Update("status", "active").Error)
			}
			hold := db.Begin()
			require.NoError(t, hold.Error)
			t.Cleanup(func() { _ = hold.Rollback().Error })
			var id uint
			if lockOrder {
				require.NoError(t, hold.Raw("SELECT id FROM orders WHERE order_no = ? FOR UPDATE", first.Order.OrderNo).Scan(&id).Error)
			} else {
				require.NoError(t, hold.Raw("SELECT user_id FROM wallets WHERE user_id = 1 FOR UPDATE").Scan(&id).Error)
			}
			start := time.Now()
			result, err := uc.ExpireDueOrders(context.Background(), 200)
			require.NoError(t, err)
			require.Less(t, time.Since(start), 5*time.Second)
			require.Equal(t, 1, result.Failed)
			require.Equal(t, 1, result.SupplierSettled)
			var firstReceipt, secondReceipt billinginfra.SupplierSettlementModel
			require.NoError(t, db.First(&firstReceipt, "order_no = ?", first.Order.OrderNo).Error)
			require.NoError(t, db.First(&secondReceipt, "order_no = ?", second.Order.OrderNo).Error)
			require.Equal(t, "frozen", firstReceipt.Status)
			require.Equal(t, "credited", secondReceipt.Status)
			t.Logf("blocked %s: elapsed=%s settled=%d failed=%d", name, time.Since(start), result.SupplierSettled, result.Failed)
			require.NoError(t, hold.Rollback().Error)
			retry, err := uc.ExpireDueOrders(context.Background(), 200)
			require.NoError(t, err)
			require.Zero(t, retry.Failed)
			require.Equal(t, 1, retry.SupplierSettled)
		})
	}
}

func TestSupplierMatchDoesNotLockWalletsMySQL(t *testing.T) {
	for _, mode := range []string{"code", "purchase"} {
		t.Run(mode, func(t *testing.T) {
			db, uc, _, result := supplierCheckoutFixture(t, mode)
			hold := db.Begin()
			require.NoError(t, hold.Error)
			t.Cleanup(func() { _ = hold.Rollback().Error })
			var wallets []billinginfra.WalletModel
			require.NoError(t, hold.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id IN (1, 2)").Order("user_id").Find(&wallets).Error)
			require.Len(t, wallets, 2)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			require.NoError(t, uc.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{OrderNo: result.Order.OrderNo, MatchedAt: time.Now().UTC()}))
			order, err := uc.GetOrder(context.Background(), result.Order.OrderNo, 2, false)
			require.NoError(t, err)
			require.NotNil(t, order.Order.AfterSaleUntil)
			var receipt billinginfra.SupplierSettlementModel
			require.NoError(t, db.First(&receipt, "order_no = ?", result.Order.OrderNo).Error)
			require.WithinDuration(t, *order.Order.AfterSaleUntil, receipt.AvailableAt, time.Millisecond)
			supplierWalletState(t, db, "48.000000", "0.000000", "920.000000")
		})
	}
}

type pausedSupplierScanWallet struct {
	billingWalletAdapter
	started, resume chan struct{}
	once            sync.Once
}

func (w *pausedSupplierScanWallet) LockSupplierWallets(ctx context.Context, ids ...uint) error {
	w.once.Do(func() { close(w.started) })
	select {
	case <-w.resume:
	case <-ctx.Done():
		return ctx.Err()
	}
	return w.billingWalletAdapter.LockSupplierWallets(ctx, ids...)
}

func TestSupplierSettlementScannersCoordinateAcrossInstancesMySQL(t *testing.T) {
	db, second, _, order := supplierCheckoutFixture(t, "purchase")
	expireSupplierOrder(t, db, order.Order.OrderNo, "purchase")
	wallet := &pausedSupplierScanWallet{
		billingWalletAdapter: billingWalletAdapter{wallet: billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))},
		started:              make(chan struct{}), resume: make(chan struct{}),
	}
	first := tradeapp.NewUseCase(tradeinfra.NewRepo(db), nil, wallet, nil, nil)
	type outcome struct {
		result *tradeapp.ExpireOrdersResult
		err    error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { result, err := first.ExpireDueOrders(ctx, 200); done <- outcome{result, err} }()
	select {
	case <-wallet.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	other, otherErr := second.ExpireDueOrders(ctx, 200)
	close(wallet.resume)
	result := <-done
	require.NoError(t, otherErr)
	require.NoError(t, result.err)
	require.Zero(t, other.SupplierSettled)
	require.Equal(t, 1, result.result.SupplierSettled)
	require.Zero(t, result.result.Failed)
	supplierWalletState(t, db, "0.000000", "48.000000", "920.000000")

	// Cancellation must release the connection-owned lock for the next instance.
	repo := tradeinfra.NewRepo(db)
	lockCtx, cancelLock := context.WithCancel(context.Background())
	err := repo.WithSupplierSettlementScan(lockCtx, func() error {
		cancelLock()
		return lockCtx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	called := false
	require.NoError(t, tradeinfra.NewRepo(db).WithSupplierSettlementScan(context.Background(), func() error { called = true; return nil }))
	require.True(t, called)
}

func TestSupplierMixedCheckoutMatchRefundAndSettlementMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, db.Callback().Create().Remove("test:allocation_bucket"))
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, int(allocapp.MicrosoftBucketCount), true)
	creditBuyer(t, db, 2, "1000")
	uc := newTradeUseCase(db)
	ctx := context.Background()
	requests := make([]tradeapp.CheckoutRequest, 48)
	for i := range requests {
		requests[i] = tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "purchase", SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: fmt.Sprintf("mixed-old-%03d", i)}
	}
	orders, err := uc.CheckoutBatch(ctx, requests)
	require.NoError(t, err)
	for _, order := range orders {
		require.NoError(t, order.Err)
	}
	for _, order := range orders[:32] {
		expireSupplierOrder(t, db, order.Result.Order.OrderNo, "purchase")
	}
	start := make(chan struct{})
	errs := make(chan error, 52)
	var wg sync.WaitGroup
	launch := func(fn func() error) {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- fn() }()
	}
	for range 4 {
		scanner := newTradeUseCase(db)
		launch(func() error { _, err := scanner.ExpireDueOrders(ctx, 200); return err })
	}
	for i := range 16 {
		orderNo := orders[i].Result.Order.OrderNo
		launch(func() error {
			_, err := uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{OrderNo: orderNo, Reason: "Concurrent refund", IdempotencyKey: "mixed-refund-" + orderNo})
			return err
		})
		notifyNo := orders[i+32].Result.Order.OrderNo
		launch(func() error {
			return uc.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{OrderNo: notifyNo, MatchedAt: time.Now().UTC()})
		})
		request := requests[i]
		request.IdempotencyKey = fmt.Sprintf("mixed-new-%03d", i)
		launch(func() error { _, err := uc.Checkout(ctx, request); return err })
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	_, err = uc.ExpireDueOrders(ctx, 200)
	require.NoError(t, err)
	supplierWalletState(t, db, "32.000000", "16.000000", "904.000000")
	var invalid int64
	require.NoError(t, db.Table("wallet_transactions").Where("amount <> balance_after - balance_before").Count(&invalid).Error)
	require.Zero(t, invalid)
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM (
    SELECT user_id, balance_bucket, SUM(amount) AS ledger_total
    FROM wallet_transactions GROUP BY user_id, balance_bucket
) t JOIN wallets w ON w.user_id = t.user_id
WHERE t.ledger_total <> CASE t.balance_bucket
    WHEN 'consumer' THEN w.consumer_balance WHEN 'supplier_frozen' THEN w.supplier_frozen ELSE w.supplier_available END`).Scan(&invalid).Error)
	require.Zero(t, invalid)
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM (
    SELECT idempotency_key FROM wallet_transactions WHERE biz_type LIKE 'supplier_settlement%'
    GROUP BY idempotency_key HAVING COUNT(*) > 1
) t`).Scan(&invalid).Error)
	require.Zero(t, invalid)
}

func TestSupplierCheckoutFreezeFailureRollsBackActivationMySQL(t *testing.T) {
	db, uc, request, _ := supplierCheckoutFixture(t, "purchase")
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_supplier_freeze", func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*billinginfra.WalletTransactionModel); ok && row.BalanceBucket == "supplier_frozen" {
			_ = tx.AddError(fmt.Errorf("supplier freeze unavailable"))
		}
	}))
	request.IdempotencyKey = "supplier-freeze-failure"
	_, err := uc.Checkout(context.Background(), request)
	require.ErrorContains(t, err, "supplier freeze unavailable")
	var order tradeinfra.OrderModel
	require.NoError(t, db.First(&order, "idempotency_key = ?", request.IdempotencyKey).Error)
	require.Equal(t, "failed", order.Status)
	require.NotNil(t, order.DebitTxID)
	require.NotNil(t, order.RefundTxID)
	var count int64
	require.NoError(t, db.Table("supplier_settlements").Where("order_no = ?", order.OrderNo).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Table("order_events").Where("order_no = ? AND event_type = 'order.active'", order.OrderNo).Count(&count).Error)
	require.Zero(t, count)
	supplierWalletState(t, db, "48.000000", "0.000000", "920.000000")
}

type purchaseSettlementOnlyRepo struct{ *tradeinfra.Repo }

func (r purchaseSettlementOnlyRepo) ListExpiredPurchaseActivationOrderNos(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}
func (r purchaseSettlementOnlyRepo) ListExpiredPurchaseWarrantyOrderNos(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}

func TestSupplierPurchaseCompletionRollsBackWhenCreditFailsMySQL(t *testing.T) {
	db, _, _, checkout := supplierCheckoutFixture(t, "purchase")
	expireSupplierOrder(t, db, checkout.Order.OrderNo, "purchase")
	require.NoError(t, db.Table("orders").Where("order_no = ?", checkout.Order.OrderNo).Update("status", "active").Error)
	// Force the settlement path to finish this purchase, as it does beyond the lifecycle batch.
	uc := tradeapp.NewUseCase(purchaseSettlementOnlyRepo{tradeinfra.NewRepo(db)}, nil,
		billingWalletAdapter{wallet: billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))}, nil, nil)
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_supplier_credit", func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*billinginfra.WalletTransactionModel); ok && row.BalanceBucket == "supplier_available" {
			_ = tx.AddError(fmt.Errorf("supplier credit unavailable"))
		}
	}))
	result, err := uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Zero(t, result.SupplierSettled)
	require.Zero(t, result.PurchaseWarrantyCompleted)
	var order tradeinfra.OrderModel
	require.NoError(t, db.First(&order, "order_no = ?", checkout.Order.OrderNo).Error)
	require.Equal(t, "active", order.Status)
	var receipt billinginfra.SupplierSettlementModel
	require.NoError(t, db.First(&receipt, "order_no = ?", checkout.Order.OrderNo).Error)
	require.Equal(t, "frozen", receipt.Status)
	var count int64
	require.NoError(t, db.Table("order_events").Where("order_no = ? AND event_type = 'order.completed'", checkout.Order.OrderNo).Count(&count).Error)
	require.Zero(t, count)
	supplierWalletState(t, db, "48.000000", "0.000000", "920.000000")
	require.NoError(t, db.Callback().Create().Remove("test:fail_supplier_credit"))
	result, err = uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Zero(t, result.Failed)
	require.Equal(t, 1, result.SupplierSettled)
	require.Equal(t, 1, result.PurchaseWarrantyCompleted)
	supplierWalletState(t, db, "0.000000", "48.000000", "920.000000")
}
