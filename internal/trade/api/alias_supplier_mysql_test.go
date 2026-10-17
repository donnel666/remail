package api

import (
	"context"
	"testing"

	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMicrosoftDaumExplicitAliasIncomeSettlesToPlatformMySQL(t *testing.T) {
	for _, provider := range []string{"microsoft", "daum"} {
		t.Run(provider, func(t *testing.T) {
			db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
			seedTradeBase(t, db, provider)
			require.NoError(t, db.Table("users").Where("id=3").Update("role", "supplier").Error)
			creditBuyer(t, db, 2, "100.00")
			suffixes := []string{"", "outlook.com", "hotmail.com"}
			if provider == "microsoft" {
				seedTradeMicrosoftResources(t, db, 3, 1000, 1, true)
				require.NoError(t, db.Exec(`INSERT INTO explicit_aliases(resource_id,owner_user_id,email,status) VALUES (1000,1,'alias@outlook.com','normal'),(1000,1,'alias@hotmail.com','normal')`).Error)
			} else {
				suffixes = []string{"daum.net", "hanmail.net", "hanmail2.net"}
				require.NoError(t, db.Exec(`INSERT INTO email_resources(id,type,owner_user_id) VALUES(1000,'daum',3)`).Error)
				require.NoError(t, db.Exec(`INSERT INTO daum_resources(id,owner_user_id,email_address,app_password,status,for_sale) VALUES(1000,3,'alias@daum.net','fixture-app','normal',TRUE)`).Error)
				require.NoError(t, db.Exec(`INSERT INTO daum_explicit_aliases(resource_id,email_address,email_domain) VALUES(1000,'alias@hanmail.net','hanmail.net'),(1000,'alias@hanmail2.net','hanmail2.net')`).Error)
			}
			uc := resourceRefundUseCase(db, tradeinfra.NewRepo(db))
			ctx := context.Background()
			orders := make([]*tradeapp.CheckoutResult, 0, 3)
			requests := make([]tradeapp.CheckoutRequest, 0, 3)
			for i, suffix := range suffixes {
				request := tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, EmailSuffix: suffix, ServiceMode: "purchase", SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: provider + "-supplier-" + suffix}
				result, err := uc.Checkout(ctx, request)
				require.NoError(t, err)
				require.NotNil(t, result.Order.SupplierUserID)
				want := uint(1)
				if i == 0 {
					want = 3
				}
				require.Equal(t, want, *result.Order.SupplierUserID)
				require.Equal(t, "1.00", result.Order.SupplierAmount)
				var guardOwner uint
				require.NoError(t, db.Table("allocation_order_guards").Select("owner_user_id").Where("order_no=?", result.Order.OrderNo).Scan(&guardOwner).Error)
				require.Equal(t, want, guardOwner)
				orders = append(orders, result)
				requests = append(requests, request)
			}
			assertIncome := func(id uint, frozen, available string) {
				t.Helper()
				var wallet billinginfra.WalletModel
				require.NoError(t, db.First(&wallet, "user_id=?", id).Error)
				require.Equal(t, frozen, wallet.SupplierFrozen)
				require.Equal(t, available, wallet.SupplierAvailable)
			}
			assertIncome(1, "2.000000", "0.000000")
			assertIncome(3, "1.000000", "0.000000")
			// Ownership changes cannot redirect settlement or an idempotent order replay.
			require.NoError(t, db.Table("email_resources").Where("id=1000").Update("owner_user_id", 1).Error)
			if provider == "daum" {
				require.NoError(t, db.Table("daum_resources").Where("id=1000").Update("owner_user_id", 1).Error)
			}
			for i, request := range requests {
				replay, err := uc.Checkout(ctx, request)
				require.NoError(t, err)
				require.Equal(t, orders[i].Order.SupplierUserID, replay.Order.SupplierUserID)
				require.Equal(t, orders[i].Order.SupplierAmount, replay.Order.SupplierAmount)
			}
			expireSupplierOrder(t, db, orders[0].Order.OrderNo, "purchase")
			expireSupplierOrder(t, db, orders[1].Order.OrderNo, "purchase")
			swept, err := uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			require.Equal(t, 2, swept.SupplierSettled)
			assertIncome(1, "1.000000", "1.000000")
			assertIncome(3, "0.000000", "1.000000")
			// Refunds reverse each original supplier, including already settled income.
			for _, order := range orders[:2] {
				_, err = uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{OrderNo: order.Order.OrderNo, Reason: "alias attribution test", IdempotencyKey: "refund-" + order.Order.OrderNo})
				require.NoError(t, err)
			}
			require.NoError(t, db.Table(provider+"_resources").Where("id=1000").Update("status", "abnormal").Error)
			refund := uc.RefundUnavailableMicrosoftOrders
			if provider == "daum" {
				refund = uc.RefundUnavailableDaumOrders
			}
			count, err := refund(ctx, 1000, "alias-attribution")
			require.NoError(t, err)
			require.Equal(t, 1, count)
			count, err = refund(ctx, 1000, "alias-replay")
			require.NoError(t, err)
			require.Zero(t, count)
			assertIncome(1, "0.000000", "0.000000")
			assertIncome(3, "0.000000", "0.000000")
			var buyer billinginfra.WalletModel
			require.NoError(t, db.First(&buyer, "user_id=2").Error)
			require.Equal(t, "100.000000", buyer.ConsumerBalance)
			var cancelled int64
			require.NoError(t, db.Table("supplier_settlements").Where("status='cancelled'").Count(&cancelled).Error)
			require.EqualValues(t, 3, cancelled)
		})
	}
}
