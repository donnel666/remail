package api

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

func TestICloudConcurrentCheckoutAndPaymentMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "icloud")
	seedTradeDomainResources(t, db, 1, 2000, 1, "binding")
	previous := runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, "")
	runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, "trade2000.example.com")
	t.Cleanup(func() { runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, previous) })
	creditBuyer(t, db, 2, "1000.00")
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id)
WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < 511)
SELECT 10000 + n, 'icloud', 1 FROM seq`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_resources(id, primary_email, expire_at, for_sale, status, alias_count)
SELECT id, CONCAT('account-', id, '@icloud.com'), UTC_TIMESTAMP() - INTERVAL 1 DAY, TRUE, 'normal', 8
FROM email_resources WHERE type = 'icloud'`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_aliases(resource_id, anonymous_id, email, forward_to_email, status)
WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < 7)
SELECT ir.id, seq.n, CONCAT('alias-', ir.id, '-', seq.n, '@icloud.com'), 'inbox@trade2000.example.com', 'normal'
FROM icloud_resources ir CROSS JOIN seq`).Error)

	const requests = 100
	useCase := newTradeUseCase(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	checkout := func(i int) (*tradeapp.CheckoutResult, error) {
		return useCase.Checkout(ctx, tradeapp.CheckoutRequest{
			UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "purchase", SupplyPolicy: "private_first",
			ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: fmt.Sprintf("icloud-checkout-%d", i),
			RequestID: fmt.Sprintf("icloud-checkout-%d", i),
		})
	}
	type outcome struct {
		result *tradeapp.CheckoutResult
		err    error
	}
	results := make(chan outcome, requests)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := checkout(i)
			results <- outcome{result, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	seen := make(map[string]bool, requests)
	for item := range results {
		require.NoError(t, item.err)
		require.NotNil(t, item.result)
		require.Equal(t, tradedomain.OrderStatusActive, item.result.Order.Status)
		require.Equal(t, "2.00", item.result.Order.PayAmount)
		require.NotEmpty(t, item.result.ServiceToken)
		require.NotZero(t, item.result.AllocationID)
		require.False(t, seen[item.result.Order.DeliveryEmail], "duplicate delivery email")
		seen[item.result.Order.DeliveryEmail] = true
	}
	require.Len(t, seen, requests)
	replay, err := checkout(0)
	require.NoError(t, err)
	require.False(t, replay.Created)
	for _, table := range []string{"orders", "icloud_allocations", "order_tokens"} {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		require.EqualValues(t, requests, count, table)
	}
	var debits int64
	require.NoError(t, db.Table("wallet_transactions").Where("user_id = 2 AND transaction_type = 'debit'").Count(&debits).Error)
	require.EqualValues(t, requests, debits)
}
