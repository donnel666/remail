package api

import (
	"context"
	"sync"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapiinfra "github.com/donnel666/remail/internal/openapi/infra"
	"github.com/donnel666/remail/internal/platform/testmysql"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

func TestDaumThreeAddressesCheckoutHistoryAndRefundMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "daum")
	creditBuyer(t, db, 2, "20.00")
	require.NoError(t, db.Exec(`UPDATE users SET role='supplier' WHERE id=3`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id,type,owner_user_id) VALUES(3000,'daum',3),(3001,'daum',2)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_resources(id,owner_user_id,email_address,app_password,status,for_sale) VALUES(3000,3,'public@daum.net','test-only','normal',TRUE),(3001,2,'private@daum.net','test-only','normal',FALSE)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_explicit_aliases(resource_id,email_address,email_domain) VALUES(3000,'public@hanmail.net','hanmail.net'),(3000,'public@hanmail2.net','hanmail2.net'),(3001,'private@hanmail.net','hanmail.net'),(3001,'private@hanmail2.net','hanmail2.net')`).Error)
	allocRepo := allocinfra.NewRepo(db)
	allocation := allocapp.NewUseCase(allocRepo)
	allocation.SetDaumProtocolReady(true)
	uc := NewModule(db, coreapp.NewProjectUseCase(coreinfra.NewProjectRepo(db)), billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db)), allocation, openapiapp.NewUseCase(openapiinfra.NewRepo(db))).UseCase
	ctx := context.Background()
	totals, err := allocation.GetProductInventoryTotals(ctx, 10, 2)
	require.NoError(t, err)
	require.Len(t, totals.Items, 1)
	require.EqualValues(t, 4, totals.Items[0].TotalAvailable)
	require.EqualValues(t, 3, totals.Items[0].PublicAvailable)
	var orders []*tradeapp.CheckoutResult
	for _, suffix := range []string{"daum.net", "hanmail.net", "hanmail2.net"} {
		policy := "public_only"
		if suffix != "daum.net" {
			policy = "private_first"
		}
		request := tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, EmailSuffix: suffix, ServiceMode: "code", SupplyPolicy: policy, ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "daum-" + suffix}
		order, err := uc.Checkout(ctx, request)
		require.NoError(t, err)
		require.Equal(t, "public@"+suffix, order.Order.DeliveryEmail)
		require.Equal(t, tradedomain.AllocationTypeDaum, *order.Order.AllocationType)
		require.NotEmpty(t, order.ServiceToken)
		require.NotNil(t, order.Order.SupplierUserID)
		owner := uint(1)
		if suffix == "daum.net" {
			owner = 3
		}
		require.Equal(t, owner, *order.Order.SupplierUserID, "main income belongs to the root supplier; explicit alias income belongs to platform user 1")
		require.Equal(t, "0.50", order.Order.SupplierAmount)
		replay, err := uc.Checkout(ctx, request)
		require.NoError(t, err)
		require.Equal(t, order.Order.OrderNo, replay.Order.OrderNo)
		request.IdempotencyKey += "-empty"
		_, err = uc.Checkout(ctx, request)
		require.ErrorIs(t, err, tradedomain.ErrInsufficientInventory)
		orders = append(orders, order)
	}
	// All three simultaneous deliveries retain one root and distinct address facts.
	facts, err := allocation.ListAllocations(ctx, allocapp.AllocationFilter{Type: allocdomain.AllocationTypeDaum, ResourceID: 3000, Limit: 10})
	require.NoError(t, err)
	require.Len(t, facts.Items, 3)
	for _, item := range facts.Items {
		require.EqualValues(t, 3000, item.ResourceID)
	}
	for _, suffix := range []string{"hanmail.net", "hanmail2.net"} {
		_, err := uc.Checkout(ctx, tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, EmailSuffix: suffix, ServiceMode: "purchase", SupplyPolicy: "private_first", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "daum-owned-" + suffix})
		require.ErrorIs(t, err, tradedomain.ErrInsufficientInventory, "owned aliases must not substitute for exhausted public inventory")
	}
	owned, err := uc.Checkout(ctx, tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, EmailSuffix: "daum", ServiceMode: "purchase", SupplyPolicy: "private_first", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "daum-owned"})
	require.NoError(t, err)
	require.Equal(t, "private@daum.net", owned.Order.DeliveryEmail)
	require.Equal(t, "0.00", owned.Order.PayAmount)
	require.NoError(t, db.Exec(`UPDATE daum_resources SET status='abnormal',failure_category='invalid_credentials' WHERE id=3000`).Error)
	count, err := uc.RefundUnavailableDaumOrders(ctx, 3000, "test")
	require.NoError(t, err)
	require.Equal(t, 3, count)
	count, err = uc.RefundUnavailableDaumOrders(ctx, 3000, "replay")
	require.NoError(t, err)
	require.Zero(t, count)
	for _, order := range orders {
		fact, err := allocation.FindAllocationByOrder(ctx, order.Order.OrderNo)
		require.NoError(t, err)
		require.Equal(t, allocdomain.AllocationStatusReleased, fact.Status)
	}
	require.NoError(t, db.Exec(`UPDATE daum_resources SET status='normal' WHERE id=3000`).Error)
	suffixes, err := allocRepo.ListProductSuffixInventory(ctx, allocapp.ProductAllocationConfig{ProjectID: 10, ProductType: "daum"}, 2, allocdomain.SupplyScopePublic)
	require.NoError(t, err)
	require.Zero(t, suffixes["hanmail2.net"], "refunded history cannot become fresh stock")
}

func TestDaumAddressAllocationConcurrentAndHistoryMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "daum")
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id,type,owner_user_id) VALUES(4000,'daum',1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_resources(id,owner_user_id,email_address,app_password,status,for_sale) VALUES(4000,1,'concurrent@daum.net','test-only','normal',TRUE)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_explicit_aliases(resource_id,email_address,email_domain) VALUES(4000,'concurrent@hanmail.net','hanmail.net'),(4000,'concurrent@hanmail2.net','hanmail2.net')`).Error)
	allocation := allocapp.NewUseCase(allocinfra.NewRepo(db))
	allocation.SetDaumProtocolReady(true)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, orderNo := range []string{"DAUM-RACE-1", "DAUM-RACE-2"} {
		wg.Add(1)
		go func(no string) {
			defer wg.Done()
			_, err := allocation.Allocate(context.Background(), allocapp.AllocateCommand{OrderNo: testmysql.AllocationOrderNo(no, 10, "main", 0, allocapp.DaumBucketCount), BuyerUserID: 2, ProjectProductID: 20, EmailSuffix: "hanmail.net", ServiceMode: allocdomain.ServiceModeCode})
			results <- err
		}(orderNo)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, allocdomain.ErrInsufficientInventory)
		}
	}
	require.Equal(t, 1, success)
	now := time.Now().UTC().Add(-time.Hour)
	command := allocapp.HistoricalDaumAllocationCommand{ProjectID: 10, ProductID: 20, ResourceID: 4000, Email: "concurrent@hanmail2.net", CreatedAt: now, ReleasedAt: now}
	historical, err := allocation.ImportHistoricalDaumAllocation(context.Background(), command)
	require.NoError(t, err)
	require.NotNil(t, historical)
	require.True(t, historical.Created)
	require.Equal(t, "alias", historical.Mailbox)
	replay, err := allocation.ImportHistoricalDaumAllocation(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, historical.OrderNo, replay.OrderNo)
	require.False(t, replay.Created)
	_, err = allocation.Allocate(context.Background(), allocapp.AllocateCommand{OrderNo: testmysql.AllocationOrderNo("DAUM-HISTORY", 10, "main", 0, allocapp.DaumBucketCount), BuyerUserID: 2, ProjectProductID: 20, EmailSuffix: "hanmail2.net", ServiceMode: allocdomain.ServiceModeCode})
	require.ErrorIs(t, err, allocdomain.ErrInsufficientInventory)
	main, err := allocation.Allocate(context.Background(), allocapp.AllocateCommand{OrderNo: testmysql.AllocationOrderNo("DAUM-MAIN", 10, "main", 0, allocapp.DaumBucketCount), BuyerUserID: 2, ProjectProductID: 20, EmailSuffix: "daum.net", ServiceMode: allocdomain.ServiceModeCode})
	require.NoError(t, err)
	require.Equal(t, "concurrent@daum.net", main.Email)
}
