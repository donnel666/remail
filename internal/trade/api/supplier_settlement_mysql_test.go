package api

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOrderDebitAndRefundCannotBypassSettlementThroughReversalMySQL(t *testing.T) {
	db, uc, _, result := supplierCheckoutFixture(t, "purchase")
	ctx := context.Background()
	repo := billinginfra.NewBillingRepo(db)
	wallet := billingapp.NewWalletUseCase(repo)
	debit, err := repo.GetAdminTransaction(ctx, *result.Order.DebitTxID)
	require.NoError(t, err)
	require.True(t, debit.OrderLinked)
	_, err = wallet.ReverseTransaction(ctx, billingapp.ReverseTransactionRequest{TransactionID: debit.Transaction.ID, IdempotencyKey: "reject-order-debit-reversal"})
	require.ErrorIs(t, err, billingdomain.ErrTransactionNotReversible)
	supplierWalletState(t, db, "48.000000", "0.000000", "920.000000")

	refunded, err := uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{OrderNo: result.Order.OrderNo, Reason: "Normal order refund", IdempotencyKey: "order-refund-after-rejected-reversal"})
	require.NoError(t, err)
	_, err = wallet.ReverseTransaction(ctx, billingapp.ReverseTransactionRequest{TransactionID: *refunded.RefundTxID, IdempotencyKey: "reject-order-refund-reversal"})
	require.ErrorIs(t, err, billingdomain.ErrTransactionNotReversible)
	_, err = uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{OrderNo: result.Order.OrderNo, Reason: "Replay order refund", IdempotencyKey: "order-refund-replay"})
	require.NoError(t, err)
	supplierWalletState(t, db, "0.000000", "0.000000", "1000.000000")
	items, _, _, err := repo.ListAdminTransactions(ctx, billingapp.AdminTransactionFilter{Search: "order:" + result.Order.OrderNo}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 2)
	for _, item := range items {
		require.True(t, item.OrderLinked)
	}
}

func TestSupplierSelfPurchaseCannotCreateWithdrawableIncomeMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, "microsoft")
	require.NoError(t, db.Table("users").Where("id = 2").Update("role", "supplier").Error)
	seedTradeMicrosoftResources(t, db, 2, 1000, 2, true)
	runtimeconfig.Replace([]settingsdomain.Setting{{Key: runtimeconfig.MicrosoftPriceMultiplierKey, Value: "0.8"}})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	creditBuyer(t, db, 2, "100")
	uc := newTradeUseCase(db)
	for _, supplierPrice := range []string{"60", "0"} {
		require.NoError(t, db.Table("project_products").Where("id = 20").Updates(map[string]any{"purchase_price": "100", "purchase_supplier_price": supplierPrice}).Error)
		req := tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "purchase", SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "self-purchase-" + supplierPrice}
		result, err := uc.Checkout(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, "0.00", result.Order.PayAmount)
		require.Equal(t, "0.00", result.Order.SupplierAmount)
		require.Nil(t, result.Order.SupplierUserID)
		replay, err := uc.Checkout(context.Background(), req)
		require.NoError(t, err)
		require.False(t, replay.Created)
		_, err = uc.AdminRefundOrder(context.Background(), tradeapp.AdminOrderCommandRequest{OrderNo: result.Order.OrderNo, Reason: "Refund self purchase", IdempotencyKey: "self-refund-" + supplierPrice})
		require.NoError(t, err)
	}
	var wallet billinginfra.WalletModel
	require.NoError(t, db.First(&wallet, "user_id = 2").Error)
	require.Equal(t, "100.000000", wallet.ConsumerBalance)
	require.Equal(t, "0.000000", wallet.SupplierAvailable)
	require.Equal(t, "0.000000", wallet.SupplierFrozen)
	var receipts int64
	require.NoError(t, db.Model(&billinginfra.SupplierSettlementModel{}).Count(&receipts).Error)
	require.Zero(t, receipts)
}

func TestSupplierSettlementDrainsMoreThanOneBatchMySQL(t *testing.T) {
	for _, mode := range []string{"code", "purchase"} {
		t.Run(mode, func(t *testing.T) {
			db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, db.Callback().Create().Remove("test:allocation_bucket"))
			seedTradeBase(t, db, "microsoft")
			seedTradeMicrosoftResources(t, db, 1, 1000, int(allocapp.MicrosoftBucketCount), true)
			creditBuyer(t, db, 2, "1000")
			uc := newTradeUseCase(db)
			ctx := context.Background()
			for start := 0; start < 201; start += 100 {
				requests := make([]tradeapp.CheckoutRequest, min(100, 201-start))
				for i := range requests {
					requests[i] = tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: mode, SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: fmt.Sprintf("multi-settlement-%03d", start+i)}
				}
				items, err := uc.CheckoutBatch(ctx, requests)
				require.NoError(t, err)
				for _, item := range items {
					require.NoError(t, item.Err)
					require.NotNil(t, item.Result)
				}
			}
			past := time.Now().UTC().Add(-time.Minute)
			updates := map[string]any{"receive_until": past, "after_sale_until": past}
			if mode == "purchase" {
				// Keep the real active state at warranty expiry; settlement must finish the last order too.
				updates["activated_at"] = past.Add(-time.Hour)
			} else {
				updates["status"] = "completed"
			}
			require.NoError(t, db.Table("orders").Where("user_id = 2").Updates(updates).Error)
			require.NoError(t, db.Table("supplier_settlements").Where("supplier_user_id = 1").Update("available_at", past).Error)
			result, err := uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			require.Zero(t, result.Failed)
			require.Equal(t, 201, result.SupplierSettled)
			if mode == "purchase" {
				require.Equal(t, 201, result.PurchaseWarrantyCompleted)
				supplierWalletState(t, db, "0.000000", "201.000000", "598.000000")
			} else {
				supplierWalletState(t, db, "0.000000", "100.500000", "799.000000")
			}
			replay, err := uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			require.Zero(t, replay.SupplierSettled)

		})
	}
}

func supplierCheckoutFixture(t *testing.T, mode string) (*gorm.DB, *tradeapp.UseCase, tradeapp.CheckoutRequest, *tradeapp.CheckoutResult) {
	t.Helper()
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 2, true)
	require.NoError(t, db.Table("project_products").Where("id = 20").Updates(map[string]any{"code_price": "100", "purchase_price": "100", "code_supplier_price": "60", "purchase_supplier_price": "60"}).Error)
	require.NoError(t, db.Table("user_groups").Where("id = 1").Update("price_discount_ratio", "0.9").Error)
	runtimeconfig.Replace([]settingsdomain.Setting{{Key: runtimeconfig.MicrosoftPriceMultiplierKey, Value: "0.8"}})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	creditBuyer(t, db, 2, "1000")
	uc := newTradeUseCase(db)
	req := tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: mode, SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "supplier-checkout"}
	result, err := uc.Checkout(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "80.00", result.Order.PayAmount)
	require.Equal(t, "48.00", result.Order.SupplierAmount)
	require.NotNil(t, result.Order.SupplierUserID)
	require.EqualValues(t, 1, *result.Order.SupplierUserID)
	return db, uc, req, result
}

func supplierWalletState(t *testing.T, db *gorm.DB, frozen, available, buyer string) {
	t.Helper()
	var row billinginfra.WalletModel
	require.NoError(t, db.First(&row, "user_id = 1").Error)
	require.Equal(t, frozen, row.SupplierFrozen)
	require.Equal(t, available, row.SupplierAvailable)
	row = billinginfra.WalletModel{}
	require.NoError(t, db.First(&row, "user_id = 2").Error)
	require.Equal(t, buyer, row.ConsumerBalance)
}

func expireSupplierOrder(t *testing.T, db *gorm.DB, orderNo, mode string) {
	t.Helper()
	past := time.Now().UTC().Add(-time.Minute)
	updates := map[string]any{"status": "completed", "receive_until": past}
	if mode == "purchase" {
		updates["activated_at"], updates["after_sale_until"] = past.Add(-time.Hour), past
	}
	require.NoError(t, db.Table("orders").Where("order_no = ?", orderNo).Updates(updates).Error)
	require.NoError(t, db.Table("supplier_settlements").Where("order_no = ?", orderNo).Update("available_at", past).Error)
}

func TestSupplierCheckoutSnapshotsDiscountAndSettlesOnceMySQL(t *testing.T) {
	db, uc, req, result := supplierCheckoutFixture(t, "purchase")
	ctx := context.Background()
	supplierWalletState(t, db, "48.000000", "0.000000", "920.000000")
	// Changing prices, membership, and resource ownership cannot redirect or reprice this sale.
	require.NoError(t, db.Table("project_products").Where("id = 20").Updates(map[string]any{"purchase_price": "500", "purchase_supplier_price": "400"}).Error)
	require.NoError(t, db.Table("user_groups").Where("id = 1").Update("price_discount_ratio", "0.5").Error)
	require.NoError(t, db.Table("email_resources").Where("id = 1000").Update("owner_user_id", 3).Error)
	runtimeconfig.Set(runtimeconfig.MicrosoftPriceMultiplierKey, "0.1")
	replay, err := uc.Checkout(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "48.00", replay.Order.SupplierAmount)
	require.Equal(t, "80.00", replay.Order.PayAmount)
	require.NoError(t, uc.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{OrderNo: result.Order.OrderNo, MatchedAt: time.Now().UTC()}))
	var receipt billinginfra.SupplierSettlementModel
	require.NoError(t, db.First(&receipt, "order_no = ?", result.Order.OrderNo).Error)
	order, err := uc.GetOrder(ctx, result.Order.OrderNo, 2, false)
	require.NoError(t, err)
	require.WithinDuration(t, *order.Order.AfterSaleUntil, receipt.AvailableAt, time.Millisecond)
	_, err = uc.ExpireDueOrders(ctx, 200)
	require.NoError(t, err)
	supplierWalletState(t, db, "48.000000", "0.000000", "920.000000")
	expireSupplierOrder(t, db, result.Order.OrderNo, "purchase")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := uc.ExpireDueOrders(ctx, 200); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	supplierWalletState(t, db, "0.000000", "48.000000", "920.000000")
	var count int64
	require.NoError(t, db.Table("wallet_transactions").Where("biz_type = 'supplier_settlement' AND biz_id = ?", result.Order.OrderNo).Count(&count).Error)
	require.EqualValues(t, 3, count)
	_, err = uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{OrderNo: result.Order.OrderNo, Reason: "Administrator refund after warranty", IdempotencyKey: "late-refund"})
	require.NoError(t, err)
	supplierWalletState(t, db, "0.000000", "0.000000", "1000.000000")
}

func TestSupplierRefundCancelsFrozenIncomeAndBlocksDoubleCreditMySQL(t *testing.T) {
	db, uc, _, result := supplierCheckoutFixture(t, "purchase")
	ctx := context.Background()
	refund := tradeapp.AdminOrderCommandRequest{OrderNo: result.Order.OrderNo, Reason: "Refund during warranty", IdempotencyKey: "frozen-refund"}
	_, err := uc.AdminRefundOrder(ctx, refund)
	require.NoError(t, err)
	_, err = uc.AdminRefundOrder(ctx, refund)
	require.NoError(t, err)
	_, err = uc.ExpireDueOrders(ctx, 200)
	require.NoError(t, err)
	supplierWalletState(t, db, "0.000000", "0.000000", "1000.000000")
	var receipt billinginfra.SupplierSettlementModel
	require.NoError(t, db.First(&receipt, "order_no = ?", result.Order.OrderNo).Error)
	require.Equal(t, "cancelled", receipt.Status)
}

func TestSupplierRefundRacingSettlementKeepsBothWalletsBalancedMySQL(t *testing.T) {
	db, uc, _, result := supplierCheckoutFixture(t, "purchase")
	expireSupplierOrder(t, db, result.Order.OrderNo, "purchase")
	ctx := context.Background()
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{OrderNo: result.Order.OrderNo, Reason: "Refund at settlement boundary", IdempotencyKey: "refund-race"})
		errs <- err
	}()
	go func() { <-start; _, err := uc.ExpireDueOrders(ctx, 200); errs <- err }()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	supplierWalletState(t, db, "0.000000", "0.000000", "1000.000000")
	var receipt billinginfra.SupplierSettlementModel
	require.NoError(t, db.First(&receipt, "order_no = ?", result.Order.OrderNo).Error)
	require.Equal(t, "cancelled", receipt.Status)
	var refunds int64
	require.NoError(t, db.Table("wallet_transactions").Where("user_id = 2 AND transaction_type = 'refund'").Count(&refunds).Error)
	require.EqualValues(t, 1, refunds)
}

func TestSupplierCodeReadWindowAndTimeoutMySQL(t *testing.T) {
	for _, matched := range []bool{false, true} {
		t.Run(fmt.Sprint(matched), func(t *testing.T) {
			db, uc, _, result := supplierCheckoutFixture(t, "code")
			ctx := context.Background()
			now := time.Now().UTC()
			if matched {
				require.NoError(t, uc.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{OrderNo: result.Order.OrderNo, MatchedAt: now}))
				var receipt billinginfra.SupplierSettlementModel
				require.NoError(t, db.First(&receipt, "order_no = ?", result.Order.OrderNo).Error)
				require.WithinDuration(t, now.Add(time.Hour), receipt.AvailableAt, time.Second)
				_, err := uc.ExpireDueOrders(ctx, 200)
				require.NoError(t, err)
				supplierWalletState(t, db, "48.000000", "0.000000", "920.000000")
				expireSupplierOrder(t, db, result.Order.OrderNo, "code")
			} else {
				require.NoError(t, db.Table("orders").Where("order_no = ?", result.Order.OrderNo).Update("receive_until", now.Add(-time.Minute)).Error)
			}
			_, err := uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			if matched {
				supplierWalletState(t, db, "0.000000", "48.000000", "920.000000")
			} else {
				supplierWalletState(t, db, "0.000000", "0.000000", "1000.000000")
			}
		})
	}
}

func TestSupplierOpenDisputeHoldsIncomeAndWalletTransferUsesReleasedFundsMySQL(t *testing.T) {
	db, uc, _, result := supplierCheckoutFixture(t, "purchase")
	ctx := context.Background()
	expireSupplierOrder(t, db, result.Order.OrderNo, "purchase")
	require.NoError(t, db.Exec(`INSERT INTO aftersale_tickets(ticket_no, ticket_type, title, requester_user_id, order_no) VALUES ('T-SUPPLIER', 'order', 'Warranty dispute', 2, ?)`, result.Order.OrderNo).Error)
	_, err := uc.ExpireDueOrders(ctx, 200)
	require.NoError(t, err)
	supplierWalletState(t, db, "48.000000", "0.000000", "920.000000")
	require.NoError(t, db.Table("aftersale_tickets").Where("ticket_no = 'T-SUPPLIER'").Update("status", "closed").Error)
	_, err = uc.ExpireDueOrders(ctx, 200)
	require.NoError(t, err)
	wallet := billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))
	_, err = wallet.TransferSupplierBalance(ctx, billingapp.TransferSupplierBalanceRequest{UserID: 1, Amount: "48", IdempotencyKey: "spend-settlement"})
	require.NoError(t, err)
	_, err = uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{OrderNo: result.Order.OrderNo, Reason: "Manual refund after funds left wallet", IdempotencyKey: "no-negative-balance"})
	require.ErrorIs(t, err, tradedomain.ErrSupplierSettlementBalance)
	var receipt billinginfra.SupplierSettlementModel
	require.NoError(t, db.First(&receipt, "order_no = ?", result.Order.OrderNo).Error)
	require.Equal(t, "credited", receipt.Status)
	var refunded int64
	require.NoError(t, db.Table("wallet_transactions").Where("transaction_type = 'refund' AND user_id = 2").Count(&refunded).Error)
	require.Zero(t, refunded)
}
