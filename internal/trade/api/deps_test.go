package api

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMapAllocationInvalidRequest(t *testing.T) {
	require.ErrorIs(t, mapAllocationError(allocdomain.ErrInvalidAllocationRequest), domain.ErrInvalidOrderRequest)
	mapped := mapAllocationError(allocdomain.ErrDefinitiveInventoryExhausted)
	require.ErrorIs(t, mapped, domain.ErrDefinitiveInventoryExhausted)
	require.ErrorIs(t, mapped, domain.ErrInsufficientInventory)
}

func TestApplyOrderingDiscountUsesLowerMultiplierAtLedgerPrecision(t *testing.T) {
	runtimeconfig.Replace([]settingsdomain.Setting{{Key: runtimeconfig.MicrosoftPriceMultiplierKey, Value: "0.80"}})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	quote := &tradeapp.OrderingQuote{
		ProductType: domain.ProductTypeMicrosoft,
		PayAmount:   "10.00",
	}
	require.NoError(t, applyOrderingDiscount(quote, "0.90"))
	require.Equal(t, "8.00", quote.PayAmount)

	groupPromotion := &tradeapp.OrderingQuote{ProductType: domain.ProductTypeMicrosoft, PayAmount: "10.00"}
	require.NoError(t, applyOrderingDiscount(groupPromotion, "0.70"))
	require.Equal(t, "7.00", groupPromotion.PayAmount)
	require.Error(t, applyOrderingDiscount(groupPromotion, "1.01"))
}

func TestProjectProductIDCachesDatabaseMapping(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:trade-product-id-cache?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE project_products (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, type TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products (id, project_id, type) VALUES (20, 10, 'gmail')`).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	adapter := coreOrderingAdapter{db: db, redis: client}

	id, cached, err := adapter.projectProductID(context.Background(), 10, domain.ProductTypeGmail)
	require.NoError(t, err)
	require.Equal(t, uint(20), id)
	require.False(t, cached)
	require.Equal(t, "20", server.HGet(projectProductRedisKey(10), "gmail"))

	require.NoError(t, db.Exec(`DELETE FROM project_products`).Error)
	id, cached, err = adapter.projectProductID(context.Background(), 10, domain.ProductTypeGmail)
	require.NoError(t, err)
	require.Equal(t, uint(20), id)
	require.True(t, cached)
}

func TestProjectProductIDFallsBackWhenRedisIsUnavailable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:trade-product-id-redis-fallback?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE project_products (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, type TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products (id, project_id, type) VALUES (21, 10, 'icloud')`).Error)
	server, err := miniredis.Run()
	require.NoError(t, err)
	addr := server.Addr()
	server.Close()
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	id, cached, err := (coreOrderingAdapter{db: db, redis: client}).projectProductID(
		context.Background(), 10, domain.ProductTypeICloud,
	)
	require.NoError(t, err)
	require.Equal(t, uint(21), id)
	require.False(t, cached)
}

func TestProjectProductIDPropagatesDatabaseErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:trade-product-id-db-error?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	_, _, err = (coreOrderingAdapter{db: db}).projectProductID(
		context.Background(), 10, domain.ProductTypeDomain,
	)
	require.ErrorContains(t, err, "load project product ID")
}
