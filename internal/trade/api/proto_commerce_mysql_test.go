package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	"github.com/donnel666/remail/internal/platform"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/stretchr/testify/require"
)

func TestProtoRecoveryAndAllocationIsolationMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	require.NoError(t, db.Exec(`INSERT INTO project_products(
		id, project_id, type, status, code_enabled, purchase_enabled, code_price, purchase_price,
		code_supplier_price, purchase_supplier_price, code_window_minutes, activation_window_minutes,
		warranty_minutes, main_weight, dot_weight, plus_weight)
		SELECT 21, project_id, 'proto', status, code_enabled, purchase_enabled, code_price, purchase_price,
		code_supplier_price, purchase_supplier_price, code_window_minutes, activation_window_minutes,
		warranty_minutes, 1, 0, 0 FROM project_products WHERE id = 20`).Error)
	ctx := context.Background()
	allocRepo := allocinfra.NewRepo(db)

	t.Run("paused-paid-orders-do-not-fill-recovery-page", func(t *testing.T) {
		old := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
		var roots, resources []map[string]any
		var guards []allocinfra.OrderGuardModel
		var allocations []allocinfra.ProtoAllocationModel
		var debits []billinginfra.WalletTransactionModel
		var orders []tradeinfra.OrderModel
		for i := 0; i < 200; i++ {
			id := uint(2000 + i)
			orderNo := fmt.Sprintf("PROTO-PAUSED-%03d", i)
			email := fmt.Sprintf("paused%d@example.com", i)
			roots = append(roots, map[string]any{"id": id, "type": "proto", "owner_user_id": 1})
			resources = append(resources, map[string]any{"id": id, "resource_type": "proto", "owner_user_id": 1,
				"email_address": email, "password": "secret", "status": "normal", "for_sale": true})
			guards = append(guards, allocinfra.OrderGuardModel{OrderNo: orderNo, Type: "proto", CreatedAt: old})
			allocations = append(allocations, allocinfra.ProtoAllocationModel{
				OrderNo: orderNo, GuardType: "proto", ProjectID: 10, ProductID: 21, ResourceID: id, OwnerUserID: 1,
				SupplyScope: "public", Mailbox: "main", ServiceMode: "code", Email: email,
				Status: "allocated", CostPointsSnapshot: "0.50", CreatedAt: old,
			})
			debits = append(debits, billinginfra.WalletTransactionModel{
				ID: id, TransactionNo: "DEBIT-" + orderNo, UserID: 2, TransactionType: "debit",
				BalanceBucket: "consumer", Direction: "out", Amount: "-1",
				BalanceBefore: strconv.Itoa(200 - i), BalanceAfter: strconv.Itoa(199 - i),
				BizType: "order", BizID: orderNo, IdempotencyKey: "debit:" + orderNo, CreatedAt: old,
			})
			orders = append(orders, tradeinfra.OrderModel{
				OrderNo: orderNo, UserID: 2, ProjectID: 10, ProjectProductID: 21, ProductType: "proto",
				ServiceMode: "code", SupplyPolicy: "public_only", Status: "paid", PayAmount: "1", RefundAmount: "0",
				CodeWindowMinutes: 10, ActivationWindowMinutes: 60, WarrantyMinutes: 1440, DebitTxID: &id,
				ClientChannel: "console", IdempotencyKey: orderNo, RequestFingerprint: strings.Repeat("p", 64),
				ServiceCleanupStatus: "none", CreatedAt: old, UpdatedAt: old, Version: 1,
			})
		}
		require.NoError(t, db.Table("email_resources").Create(&roots).Error)
		require.NoError(t, db.Table("proto_resources").Create(&resources).Error)
		require.NoError(t, db.Create(&guards).Error)
		require.NoError(t, db.Create(&allocations).Error)
		require.NoError(t, db.Create(&debits).Error)
		require.NoError(t, db.Create(&orders).Error)

		repo := tradeinfra.NewRepo(db)
		order, _, err := repo.LoadOrCreatePendingOrder(ctx, tradeapp.CreatePendingOrderCommand{
			OrderNo: "MICROSOFT-AFTER-PAUSED-PROTO", UserID: 2, ProjectID: 10, ProjectProductID: 20,
			ProductType: tradedomain.ProductTypeMicrosoft, ServiceMode: tradedomain.ServiceModeCode,
			SupplyPolicy: tradedomain.SupplyPolicyPublicOnly, PayAmount: "1.00",
			CodeWindowMinutes: 10, ActivationWindowMinutes: 60, WarrantyMinutes: 1440,
			ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "legacy-after-paused-proto",
			RequestFingerprint: strings.Repeat("m", 64), Now: time.Now().UTC(),
		})
		require.NoError(t, err)
		_, err = allocapp.NewUseCase(allocRepo).Allocate(ctx, allocapp.AllocateCommand{
			OrderNo: order.OrderNo, BuyerUserID: 2, ProjectProductID: 20, SupplyScope: allocdomain.SupplyScopePublic,
		})
		require.NoError(t, err)
		require.NoError(t, db.Table("microsoft_allocations").Where("order_no = ?", order.OrderNo).Update("created_at", old).Error)

		before := time.Now().UTC().Add(-15 * time.Minute)
		unfiltered, err := repo.ListCheckoutAllocationRecoveries(ctx, before, 200)
		require.NoError(t, err)
		require.Len(t, unfiltered, 200)
		for _, recovery := range unfiltered {
			require.Equal(t, tradedomain.ProductTypeProto, recovery.ProductType)
		}
		filtered, err := repo.ListCheckoutAllocationRecoveriesExcludingProtoPaid(ctx, before, 200)
		require.NoError(t, err)
		require.Equal(t, []tradeapp.CheckoutAllocationRecovery{{OrderNo: order.OrderNo,
			Status: tradedomain.OrderStatusPendingPayment, ProductType: tradedomain.ProductTypeMicrosoft}}, filtered)
		result, err := newTradeUseCase(db).ExpireDueOrders(ctx, 200)
		require.NoError(t, err)
		require.Zero(t, result.Failed)
		require.Equal(t, 1, result.CheckoutRecovered)
		var released int64
		require.NoError(t, db.Table("microsoft_allocations").Where("order_no = ? AND status = 'released'", order.OrderNo).Count(&released).Error)
		require.EqualValues(t, 1, released)
		var paused int64
		require.NoError(t, db.Table("orders").Where("product_type = 'proto' AND status = 'paid'").Count(&paused).Error)
		require.EqualValues(t, 200, paused)
	})

	t.Run("different-resources-do-not-lock-their-shared-supplier", func(t *testing.T) {
		require.NoError(t, db.Exec(`INSERT INTO email_resources(id, type, owner_user_id)
			VALUES (3000, 'proto', 1), (3001, 'proto', 1)`).Error)
		require.NoError(t, db.Exec(`INSERT INTO proto_resources(id, resource_type, owner_user_id, email_address, password, status, for_sale)
			VALUES (3000, 'proto', 1, 'lock-one@example.com', 'secret', 'normal', TRUE),
			       (3001, 'proto', 1, 'lock-two@example.com', 'secret', 'normal', TRUE)`).Error)
		first := db.Begin()
		require.NoError(t, first.Error)
		defer first.Rollback()
		firstCtx := platform.WithGormTx(ctx, first)
		locked, err := allocRepo.LockResourceRoot(firstCtx, 3000, allocdomain.AllocationTypeProto)
		require.NoError(t, err)
		require.True(t, locked)
		one, err := allocRepo.LockProtoCandidate(firstCtx, 3000, 10, 2, allocdomain.SupplyScopePublic)
		require.NoError(t, err)
		require.NotNil(t, one)

		second := db.Begin()
		require.NoError(t, second.Error)
		defer second.Rollback()
		secondCtx := platform.WithGormTx(ctx, second)
		locked, err = allocRepo.LockResourceRoot(secondCtx, 3001, allocdomain.AllocationTypeProto)
		require.NoError(t, err)
		require.True(t, locked)
		two, err := allocRepo.LockProtoCandidate(secondCtx, 3001, 10, 2, allocdomain.SupplyScopePublic)
		require.NoError(t, err)
		require.NotNil(t, two, "another resource's lock must not hide this supplier's inventory")
		busy, err := allocRepo.LockProtoCandidate(secondCtx, 3000, 10, 2, allocdomain.SupplyScopePublic)
		require.NoError(t, err)
		require.Nil(t, busy, "the selected Proto child must remain locked")

		supplier := db.Begin()
		require.NoError(t, supplier.Error)
		defer supplier.Rollback()
		var supplierID uint
		require.NoError(t, supplier.Raw("SELECT id FROM users WHERE id = 1 FOR UPDATE NOWAIT").Scan(&supplierID).Error)
		require.Equal(t, uint(1), supplierID, "Proto candidate locks must not block IAM or Gmail supplier checks")
	})
}
