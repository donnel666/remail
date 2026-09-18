package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWalletKeepsBalancesAndSpendLiveWhileSupplierStatisticsAreCached(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&WalletModel{}))
	wallet := defaultWalletModel(7)
	wallet.ConsumerBalance, wallet.TotalSpend, wallet.SpendCount = "12.00", "30.00", 9
	require.NoError(t, db.Create(&wallet).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo := NewBillingRepo(db)
	repo.SetStatisticsCache(client)
	ctx := context.Background()
	key := fmt.Sprintf("billing:supplier-statistics:v1:%d", wallet.UserID)
	repo.fulfillmentStats.Seed(ctx, key, supplierFulfillmentMetrics{AllocationCount: 100, FulfillmentSuccessRate: 95})
	require.NoError(t, client.Set(ctx, key+":refresh", "1", time.Hour).Err())
	// No allocation tables exist: synchronous supplier statistics would fail.
	summary, err := repo.GetOrCreateWalletSummary(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, "12.00", summary.Wallet.ConsumerBalance)
	require.Equal(t, "30.00", summary.HistoricalSpend)
	require.EqualValues(t, 9, summary.OrderCount)
	require.EqualValues(t, 100, summary.SupplierAllocationCount)
	require.Equal(t, 95.0, summary.SupplierFulfillmentSuccessRate)
	require.NoError(t, db.Model(&WalletModel{}).Where("user_id = ?", 7).Updates(map[string]any{
		"consumer_balance": "11.00", "total_spend": "31.00", "spend_count": 10,
	}).Error)
	summary, err = repo.GetOrCreateWalletSummary(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, "11.00", summary.Wallet.ConsumerBalance)
	require.Equal(t, "31.00", summary.HistoricalSpend)
	require.EqualValues(t, 10, summary.OrderCount)
	require.EqualValues(t, 100, summary.SupplierAllocationCount)
}
