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
		ProductType:    domain.ProductTypeMicrosoft,
		PayAmount:      "10.00",
		SupplierAmount: "6.00",
	}
	require.NoError(t, applyOrderingDiscount(quote, "0.90"))
	require.Equal(t, "8.00", quote.PayAmount)
	require.Equal(t, "4.80", quote.SupplierAmount)

	groupPromotion := &tradeapp.OrderingQuote{ProductType: domain.ProductTypeMicrosoft, PayAmount: "10.00", SupplierAmount: "6.00"}
	require.NoError(t, applyOrderingDiscount(groupPromotion, "0.70"))
	require.Equal(t, "7.00", groupPromotion.PayAmount)
	require.Equal(t, "4.20", groupPromotion.SupplierAmount)
	require.Error(t, applyOrderingDiscount(groupPromotion, "1.01"))
}

func TestApplyOrderingDiscountKeepsMicrosoftEnterpriseIndependent(t *testing.T) {
	runtimeconfig.Replace([]settingsdomain.Setting{
		{Key: runtimeconfig.MicrosoftPriceMultiplierKey, Value: "0.8"},
		{Key: runtimeconfig.MicrosoftEnterprisePriceMultiplierKey, Value: "0.6"},
	})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	for _, tc := range []struct {
		productType               domain.ProductType
		payAmount, supplierAmount string
	}{
		{domain.ProductTypeMicrosoft, "8.00", "4.80"},
		{domain.ProductTypeMicrosoftEnterprise, "6.00", "3.60"},
	} {
		t.Run(string(tc.productType), func(t *testing.T) {
			quote := &tradeapp.OrderingQuote{ProductType: tc.productType, PayAmount: "10.00", SupplierAmount: "6.00"}
			require.NoError(t, applyOrderingDiscount(quote, "0.90"))
			require.Equal(t, tc.payAmount, quote.PayAmount)
			require.Equal(t, tc.supplierAmount, quote.SupplierAmount)

			groupPromotion := &tradeapp.OrderingQuote{ProductType: tc.productType, PayAmount: "10.00", SupplierAmount: "6.00"}
			require.NoError(t, applyOrderingDiscount(groupPromotion, "0.50"))
			require.Equal(t, "5.00", groupPromotion.PayAmount)
			require.Equal(t, "3.00", groupPromotion.SupplierAmount)
		})
	}
}

func TestOrderingSharedDiscountPreservesPrecisionAndZeroPrice(t *testing.T) {
	runtimeconfig.Replace(nil)
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	quote := &tradeapp.OrderingQuote{ProductType: domain.ProductTypeMicrosoft, PayAmount: "0.012345", SupplierAmount: "0.007777"}
	require.NoError(t, applyOrderingDiscount(quote, "0.8"))
	require.Equal(t, "0.009876", quote.PayAmount)
	require.Equal(t, "0.006222", quote.SupplierAmount)
	require.NoError(t, applyOrderingDiscount(quote, "0"))
	require.Equal(t, "0.00", quote.PayAmount)
	require.Equal(t, "0.00", quote.SupplierAmount)
	quote.SupplierAmount = "-1"
	require.Error(t, applyOrderingDiscount(quote, "0.8"))
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
