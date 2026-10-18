package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func purchaseLifecycleFixture(t *testing.T, provider string, count int) (*gorm.DB, *tradeapp.UseCase, []*tradeapp.CheckoutResult) {
	t.Helper()
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, provider)
	creditBuyer(t, db, 2, "100.00")
	require.NoError(t, db.Table("users").Where("id=3").Update("role", "supplier").Error)
	if provider == "microsoft" {
		seedTradeMicrosoftResources(t, db, 3, 1000, count, true)
	} else {
		for i := range count {
			id := 1000 + i
			require.NoError(t, db.Exec(`INSERT INTO email_resources(id,type,owner_user_id) VALUES(?,'daum',3)`, id).Error)
			require.NoError(t, db.Exec(`INSERT INTO daum_resources(id,owner_user_id,email_address,app_password,status,for_sale) VALUES(?,3,?,'fixture','normal',TRUE)`, id, fmt.Sprintf("purchase%d@daum.net", id)).Error)
		}
	}
	uc := resourceRefundUseCase(db, tradeinfra.NewRepo(db))
	orders := make([]*tradeapp.CheckoutResult, 0, count)
	for i := range count {
		order, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "purchase", SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: fmt.Sprintf("purchase-lifecycle-%d", i)})
		require.NoError(t, err)
		require.NotNil(t, order.Order.SupplierUserID)
		require.EqualValues(t, 3, *order.Order.SupplierUserID)
		orders = append(orders, order)
	}
	return db, uc, orders
}

func agePurchaseLifecycle(t *testing.T, db *gorm.DB, orders []*tradeapp.CheckoutResult, receiveUntil time.Time) {
	t.Helper()
	for _, order := range orders {
		started := receiveUntil.Add(-time.Duration(order.Order.ActivationWindowMinutes) * time.Minute)
		require.NoError(t, db.Table("orders").Where("order_no=?", order.Order.OrderNo).Updates(map[string]any{"receive_started_at": started, "receive_until": receiveUntil}).Error)
		require.NoError(t, db.Table("supplier_settlements").Where("order_no=?", order.Order.OrderNo).Update("available_at", started.Add(time.Duration(order.Order.WarrantyMinutes)*time.Minute)).Error)
	}
}

func assertPurchaseWallets(t *testing.T, db *gorm.DB, buyer, frozen, available string) {
	t.Helper()
	var consumer, supplier billinginfra.WalletModel
	require.NoError(t, db.First(&consumer, "user_id=2").Error)
	require.NoError(t, db.First(&supplier, "user_id=3").Error)
	require.Equal(t, buyer, consumer.ConsumerBalance)
	require.Equal(t, frozen, supplier.SupplierFrozen)
	require.Equal(t, available, supplier.SupplierAvailable)
}

type purchaseActivationScanRepo struct {
	*tradeinfra.Repo
	afterScan func([]string) error
}

func (r *purchaseActivationScanRepo) ListExpiredPurchaseActivationOrderNos(ctx context.Context, now time.Time, limit int) ([]string, error) {
	orders, err := r.Repo.ListExpiredPurchaseActivationOrderNos(ctx, now, limit)
	if err == nil && r.afterScan != nil {
		err = r.afterScan(orders)
	}
	return orders, err
}

func TestPurchaseActivationAfterExpiryScanKeepsWarrantyMySQL(t *testing.T) {
	for _, provider := range []string{"daum", "microsoft"} {
		t.Run(provider, func(t *testing.T) {
			db, _, orders := purchaseLifecycleFixture(t, provider, 1)
			ctx := context.Background()
			order := orders[0].Order
			expired := time.Now().UTC().Add(-time.Minute)
			agePurchaseLifecycle(t, db, orders, expired)
			repo := &purchaseActivationScanRepo{Repo: tradeinfra.NewRepo(db)}
			uc := resourceRefundUseCase(db, repo)
			repo.afterScan = func(selected []string) error {
				require.Equal(t, []string{order.OrderNo}, selected)
				return uc.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{OrderNo: order.OrderNo, MatchedAt: expired.Add(-time.Second)})
			}
			sweep, err := uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			require.Zero(t, sweep.Failed)
			require.Zero(t, sweep.PurchaseActivationCompleted)
			require.Zero(t, sweep.SupplierSettled)
			current, err := repo.FindOrder(ctx, order.OrderNo)
			require.NoError(t, err)
			require.NotNil(t, current.ActivatedAt)
			require.NotNil(t, current.AfterSaleUntil)
			require.True(t, current.AfterSaleUntil.After(time.Now().UTC()))
			require.Equal(t, tradedomain.OrderStatusActive, current.Status)
			assertPurchaseWallets(t, db, "98.000000", "1.000000", "0.000000")
			require.NoError(t, db.Table(provider+"_resources").Where("id=1000").Update("status", "abnormal").Error)
			refund := uc.RefundUnavailableDaumOrders
			if provider == "microsoft" {
				refund = uc.RefundUnavailableMicrosoftOrders
			}
			count, err := refund(ctx, 1000, "valid-warranty")
			require.NoError(t, err)
			require.Equal(t, 1, count)
			assertPurchaseWallets(t, db, "100.000000", "0.000000", "0.000000")
		})
	}
}

func TestPurchaseLateActivationRestoresOnlyUnexpiredWarrantyMySQL(t *testing.T) {
	for _, provider := range []string{"daum", "microsoft"} {
		for _, warrantyExpired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/warranty_expired=%t", provider, warrantyExpired), func(t *testing.T) {
				db, uc, orders := purchaseLifecycleFixture(t, provider, 1)
				receiveUntil := time.Now().UTC().Truncate(time.Second).Add(-time.Minute + 750*time.Millisecond)
				if warrantyExpired {
					receiveUntil = receiveUntil.Add(-48 * time.Hour)
				}
				agePurchaseLifecycle(t, db, orders, receiveUntil)
				ctx := context.Background()
				orderNo := orders[0].Order.OrderNo
				sweep, err := uc.ExpireDueOrders(ctx, 200)
				require.NoError(t, err)
				require.Equal(t, 1, sweep.PurchaseActivationCompleted)
				before, err := tradeinfra.NewRepo(db).FindOrder(ctx, orderNo)
				require.NoError(t, err)
				require.Equal(t, tradedomain.OrderStatusCompleted, before.Status)
				var settlement billinginfra.SupplierSettlementModel
				require.NoError(t, db.First(&settlement, "order_no=?", orderNo).Error)
				require.Zero(t, before.ReceiveStartedAt.Nanosecond())
				require.Equal(t, 750*time.Millisecond, time.Duration(settlement.AvailableAt.Nanosecond()))
				require.True(t, before.ReceiveStartedAt.Add(time.Duration(before.WarrantyMinutes)*time.Minute).After(settlement.AvailableAt), "DATETIME(0) rounding must exercise the credited-deadline mismatch")
				notification := tradeapp.MatchCodeResultRequest{OrderNo: orderNo, MatchedAt: receiveUntil.Add(-time.Second)}
				require.NoError(t, uc.NotifyMatchedCode(ctx, notification))
				require.NoError(t, uc.NotifyMatchedCode(ctx, notification))
				current, err := tradeinfra.NewRepo(db).FindOrder(ctx, orderNo)
				require.NoError(t, err)
				require.NotNil(t, current.ActivatedAt)
				require.NotNil(t, current.AfterSaleUntil)
				wantStatus, wantRefunds := tradedomain.OrderStatusActive, 1
				if warrantyExpired {
					wantStatus, wantRefunds = tradedomain.OrderStatusCompleted, 0
					var after billinginfra.SupplierSettlementModel
					require.NoError(t, db.First(&after, "order_no=?", orderNo).Error)
					require.Equal(t, settlement, after, "late mail cannot modify already credited historical income")
					assertPurchaseWallets(t, db, "98.000000", "0.000000", "1.000000")
				} else {
					assertPurchaseWallets(t, db, "98.000000", "1.000000", "0.000000")
				}
				require.Equal(t, wantStatus, current.Status)
				require.Equal(t, !warrantyExpired, current.AfterSaleUntil.After(time.Now().UTC()))
				var events []tradeinfra.OrderEventModel
				require.NoError(t, db.Where("order_no=? AND event_type='order.purchase_activated'", orderNo).Find(&events).Error)
				require.Len(t, events, 1)
				require.Equal(t, string(tradedomain.OrderStatusCompleted), events[0].FromStatus.String)
				require.Equal(t, string(wantStatus), events[0].ToStatus.String)
				var enabled int64
				require.NoError(t, db.Table("order_tokens").Where("order_no=? AND enabled=TRUE", orderNo).Count(&enabled).Error)
				require.EqualValues(t, 1, enabled, "purchase mail access survives completion and late activation")
				require.NoError(t, db.Table(provider+"_resources").Where("id=1000").Update("status", "abnormal").Error)
				refund := uc.RefundUnavailableDaumOrders
				if provider == "microsoft" {
					refund = uc.RefundUnavailableMicrosoftOrders
				}
				count, err := refund(ctx, 1000, "late-activation-warranty")
				require.NoError(t, err)
				require.Equal(t, wantRefunds, count)
				if !warrantyExpired {
					assertPurchaseWallets(t, db, "100.000000", "0.000000", "0.000000")
				}
			})
		}
	}
}

func TestPurchaseRefundBatchLimitCannotReleaseSupplierIncomeMySQL(t *testing.T) {
	for _, provider := range []string{"daum", "microsoft"} {
		for _, limit := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/limit=%d", provider, limit), func(t *testing.T) {
				db, uc, orders := purchaseLifecycleFixture(t, provider, 2)
				ctx := context.Background()
				agePurchaseLifecycle(t, db, orders, time.Now().UTC().Add(-48*time.Hour))
				require.NoError(t, db.Table(provider+"_resources").Where("id IN ?", []uint{1000, 1001}).Update("status", "abnormal").Error)
				repo := tradeinfra.NewRepo(db)
				list := repo.ListUnavailableDaumOrders
				if provider == "microsoft" {
					list = repo.ListUnavailableMicrosoftOrders
				}
				proofs, err := list(ctx, 0, 200)
				require.NoError(t, err)
				require.Len(t, proofs, 2)
				sweep, err := uc.ExpireDueOrders(ctx, limit)
				require.NoError(t, err)
				require.Zero(t, sweep.Failed)
				require.Equal(t, limit, sweep.ResourceUnavailableRefunded)
				require.Zero(t, sweep.PurchaseActivationCompleted)
				require.Zero(t, sweep.SupplierSettled)
				if limit == 1 {
					current, err := repo.FindOrder(ctx, orders[1].Order.OrderNo)
					require.NoError(t, err)
					require.Equal(t, tradedomain.OrderStatusActive, current.Status)
					assertPurchaseWallets(t, db, "98.000000", "1.000000", "0.000000")
				}
				sweep, err = uc.ExpireDueOrders(ctx, limit)
				require.NoError(t, err)
				require.Equal(t, 2-limit, sweep.ResourceUnavailableRefunded)
				require.Zero(t, sweep.SupplierSettled)
				assertPurchaseWallets(t, db, "100.000000", "0.000000", "0.000000")
				for _, order := range orders {
					current, err := repo.FindOrder(ctx, order.Order.OrderNo)
					require.NoError(t, err)
					require.Equal(t, tradedomain.OrderStatusRefunded, current.Status)
					require.NotNil(t, current.RefundTxID)
				}
			})
		}
	}
}

func TestPurchaseFailedRefundCannotCompleteOrSettleBeforeRetryMySQL(t *testing.T) {
	for _, provider := range []string{"daum", "microsoft"} {
		t.Run(provider, func(t *testing.T) {
			db, uc, orders := purchaseLifecycleFixture(t, provider, 1)
			agePurchaseLifecycle(t, db, orders, time.Now().UTC().Add(-48*time.Hour))
			require.NoError(t, db.Table(provider+"_resources").Where("id=1000").Update("status", "abnormal").Error)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:refund_unavailable", func(tx *gorm.DB) {
				if row, ok := tx.Statement.Dest.(*billinginfra.WalletTransactionModel); ok && row.TransactionType == "refund" {
					_ = tx.AddError(errors.New("temporary refund ledger failure"))
				}
			}))
			ctx := context.Background()
			sweep, err := uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			require.Equal(t, 1, sweep.Failed)
			require.Zero(t, sweep.ResourceUnavailableRefunded)
			require.Zero(t, sweep.PurchaseActivationCompleted)
			require.Zero(t, sweep.SupplierSettled)
			current, err := tradeinfra.NewRepo(db).FindOrder(ctx, orders[0].Order.OrderNo)
			require.NoError(t, err)
			require.Equal(t, tradedomain.OrderStatusActive, current.Status)
			require.Nil(t, current.RefundTxID)
			assertPurchaseWallets(t, db, "98.000000", "1.000000", "0.000000")
			require.NoError(t, db.Callback().Create().Remove("test:refund_unavailable"))
			sweep, err = uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			require.Zero(t, sweep.Failed)
			require.Equal(t, 1, sweep.ResourceUnavailableRefunded)
			require.Zero(t, sweep.SupplierSettled)
			assertPurchaseWallets(t, db, "100.000000", "0.000000", "0.000000")
			var refunds int64
			require.NoError(t, db.Table("wallet_transactions").Where("transaction_type='refund' AND user_id=2").Count(&refunds).Error)
			require.EqualValues(t, 1, refunds)
		})
	}
}

func TestPurchaseExpirySkipsBusyResourceFenceMySQL(t *testing.T) {
	for _, provider := range []string{"daum", "microsoft"} {
		t.Run(provider, func(t *testing.T) {
			db, uc, orders := purchaseLifecycleFixture(t, provider, 2)
			agePurchaseLifecycle(t, db, orders, time.Now().UTC().Add(-48*time.Hour))
			repo := tradeinfra.NewRepo(db)
			for _, table := range []string{"email_resources", provider + "_resources", provider + "_allocations"} {
				t.Run(table, func(t *testing.T) {
					hold := db.Begin()
					require.NoError(t, hold.Error)
					t.Cleanup(func() { _ = hold.Rollback().Error })
					condition, value := "id=?", any(uint(1000))
					if table == provider+"_allocations" {
						condition, value = "order_no=?", orders[0].Order.OrderNo
					}
					var row struct{ ID uint }
					require.NoError(t, hold.Table(table).Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where(condition, value).Take(&row).Error)
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					_, changed, err := repo.CompleteExpiredOrder(ctx, orders[0].Order.OrderNo, time.Now().UTC(), "Purchase activation window expired.")
					require.NoError(t, err, "a resource lock must not stall the expiry scan")
					require.False(t, changed)
					current, err := repo.FindOrder(ctx, orders[0].Order.OrderNo)
					require.NoError(t, err)
					require.Equal(t, tradedomain.OrderStatusActive, current.Status)
				})
			}
			sweep, err := uc.ExpireDueOrders(context.Background(), 200)
			require.NoError(t, err)
			require.Zero(t, sweep.Failed)
			require.Equal(t, 2, sweep.PurchaseActivationCompleted)
			require.Equal(t, 2, sweep.SupplierSettled)
			assertPurchaseWallets(t, db, "96.000000", "0.000000", "2.000000")
		})
	}
}

func TestCompletedPurchaseKeepsResourceFenceBeforeOrderLockMySQL(t *testing.T) {
	for _, provider := range []string{"daum", "microsoft"} {
		t.Run(provider, func(t *testing.T) {
			db, uc, orders := purchaseLifecycleFixture(t, provider, 1)
			agePurchaseLifecycle(t, db, orders, time.Now().UTC().Add(-time.Minute))
			ctx := context.Background()
			_, err := uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			orderNo := orders[0].Order.OrderNo
			repo := tradeinfra.NewRepo(db)
			current, err := repo.FindOrder(ctx, orderNo)
			require.NoError(t, err)
			require.Equal(t, tradedomain.OrderStatusCompleted, current.Status)
			wallet := billingWalletAdapter{wallet: billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))}
			require.NoError(t, repo.WithTx(ctx, func(txCtx context.Context) error {
				if err := wallet.LockSupplierWallets(txCtx, 3); err != nil {
					return err
				}
				pending, err := repo.LockPurchaseRefundState(txCtx, orderNo)
				require.NoError(t, err)
				require.False(t, pending)
				for _, table := range []string{"email_resources", provider + "_resources", provider + "_allocations"} {
					condition, value := "id=?", any(uint(1000))
					if table == provider+"_allocations" {
						condition, value = "order_no=?", orderNo
					}
					err := db.Transaction(func(other *gorm.DB) error {
						var probe struct{ ID uint }
						return other.Raw("SELECT id FROM "+table+" WHERE "+condition+" FOR UPDATE NOWAIT", value).Scan(&probe).Error
					})
					var busy *mysql.MySQLError
					require.ErrorAs(t, err, &busy, table)
					require.EqualValues(t, 3572, busy.Number, "the completed order can still reactivate; hold its resource fence")
				}
				// The resource locks are established before the settlement takes the order lock.
				require.NoError(t, db.Transaction(func(other *gorm.DB) error {
					var probe struct{ ID uint }
					return other.Raw("SELECT id FROM orders WHERE order_no=? FOR UPDATE NOWAIT", orderNo).Scan(&probe).Error
				}))
				_, err = repo.LockOrderForUpdate(txCtx, orderNo)
				return err
			}))
		})
	}
}
