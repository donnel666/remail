package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapidomain "github.com/donnel666/remail/internal/openapi/domain"
	openapiinfra "github.com/donnel666/remail/internal/openapi/infra"
	"github.com/donnel666/remail/internal/platform/testmysql"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyPointQuotaPaymentRefundAndConcurrencyMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	ctx := context.Background()
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 3, true)
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Update("code_price", "0.600001").Error)
	creditBuyer(t, db, 2, "10.00")
	keys := openapiapp.NewUseCase(openapiinfra.NewRepo(db))
	t.Cleanup(func() { require.NoError(t, keys.Close(ctx)) })
	limit := int64(1)
	key, err := keys.CreateAPIKey(ctx, openapiapp.CreateAPIKeyRequest{UserID: 2, QuotaLimit: &limit, IdempotencyKey: "point-key"})
	require.NoError(t, err)
	orders := newTradeUseCase(db)
	wallet := billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))

	assertBalances := func(keyID uint, points, balance string) {
		t.Helper()
		var stored openapiinfra.APIKeyModel
		require.NoError(t, db.First(&stored, keyID).Error)
		require.Equal(t, points, stored.QuotaUsed.String())
		balances, err := wallet.ListConsumerBalances(ctx, []uint{2})
		require.NoError(t, err)
		require.Equal(t, balance, balances[2])
	}
	request := tradeapp.CheckoutRequest{
		UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "code", SupplyPolicy: "public_only",
		ClientChannel: tradedomain.ClientChannelAPIKey, APIKeyID: &key.ID, IdempotencyKey: "point-order",
	}
	first, err := orders.Checkout(ctx, request)
	require.NoError(t, err)
	assertBalances(key.ID, "0.600001", "9.399999")
	replayed, err := orders.Checkout(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first.Order.OrderNo, replayed.Order.OrderNo)
	assertBalances(key.ID, "0.600001", "9.399999")

	request.IdempotencyKey = "over-point-limit"
	_, err = orders.Checkout(ctx, request)
	require.ErrorIs(t, err, tradedomain.ErrInsufficientBalance)
	assertBalances(key.ID, "0.600001", "9.399999")
	var debits int64
	require.NoError(t, db.Table("wallet_transactions").Where("user_id = ? AND transaction_type = 'debit'", 2).Count(&debits).Error)
	require.EqualValues(t, 1, debits)

	// Removing the credential must not prevent its paid orders being refunded.
	require.NoError(t, keys.DeleteAPIKey(ctx, 2, key.ID))
	refund := tradeapp.AdminOrderCommandRequest{
		OrderNo: first.Order.OrderNo, Reason: "point quota refund", IdempotencyKey: "point-refund",
	}
	_, err = orders.AdminRefundOrder(ctx, refund)
	require.NoError(t, err)
	_, err = orders.AdminRefundOrder(ctx, refund)
	require.NoError(t, err)
	assertBalances(key.ID, "0", "10.00")

	key, err = keys.CreateAPIKey(ctx, openapiapp.CreateAPIKeyRequest{UserID: 2, QuotaLimit: &limit, IdempotencyKey: "concurrent-point-key"})
	require.NoError(t, err)
	const attempts = 8
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := wallet.DebitConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
				UserID: 2, APIKeyID: &key.ID, Amount: "0.600001", Reason: "concurrent point debit",
				IdempotencyKey: fmt.Sprintf("concurrent-point-debit-%d", i),
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, err, billingdomain.ErrInsufficientBalance)
		}
	}
	require.Equal(t, 1, succeeded)
	assertBalances(key.ID, "0.600001", "9.399999")

	_, err = wallet.DebitConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID: 2, Amount: "9.399999", Reason: "console debit", IdempotencyKey: "empty-wallet",
	})
	require.NoError(t, err)
	_, err = wallet.DebitConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID: 2, APIKeyID: &key.ID, Amount: "0.10", Reason: "insufficient wallet", IdempotencyKey: "no-wallet-balance",
	})
	require.ErrorIs(t, err, billingdomain.ErrInsufficientBalance)
	assertBalances(key.ID, "0.600001", "0.00")

	// Arithmetic must stay decimal even near the ledger's largest values.
	require.NoError(t, db.Table("api_keys").Where("id = ?", key.ID).Updates(map[string]any{
		"quota_limit": int64(999999999999), "quota_used": "999999999998.999998",
	}).Error)
	_, err = wallet.CreditConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID: 2, Amount: "1.00", Reason: "precision credit", IdempotencyKey: "precision-credit",
	})
	require.NoError(t, err)
	_, err = wallet.DebitConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID: 2, APIKeyID: &key.ID, Amount: "0.000001", Reason: "precision debit", IdempotencyKey: "precision-debit",
	})
	require.NoError(t, err)
	assertBalances(key.ID, "999999999998.999999", "0.999999")
}

func TestAPIKeyPointQuotaMigrationMySQL(t *testing.T) {
	server := testmysql.New("remail_key_quota_migration_test")
	ctx := context.Background()
	t.Cleanup(func() { require.NoError(t, server.Close(ctx)) })
	db := server.Database(t, testmysql.MigrationsThrough(t, tradeMigrationsDir(t), 138))
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 2, true)
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Update("code_price", "12345.123456").Error)
	creditBuyer(t, db, 2, "100000.00")
	require.NoError(t, db.Exec(`INSERT INTO api_keys(id, user_id, key_prefix, key_plain, quota_limit, quota_used)
		VALUES (7, 2, 'legacy-key', 'rk-legacy-key', 1000, 200), (8, 2, 'unused-key', 'rk-unused-key', NULL, 50)`).Error)
	orders := newTradeUseCase(db)
	for i := range 2 {
		order, err := orders.Checkout(ctx, tradeapp.CheckoutRequest{
			UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "code", SupplyPolicy: "public_only",
			IdempotencyKey: fmt.Sprintf("legacy-order-%d", i),
		})
		require.NoError(t, err)
		if i == 1 {
			_, err = orders.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{
				OrderNo: order.Order.OrderNo, Reason: "legacy refund", IdempotencyKey: "legacy-refund",
			})
			require.NoError(t, err)
		}
		require.NoError(t, db.Table("orders").Where("order_no = ?", order.Order.OrderNo).
			Updates(map[string]any{"client_channel": "api_key", "api_key_id": 7}).Error)
	}
	keyID := uint(7)
	_, _, err := tradeinfra.NewRepo(db).LoadOrCreatePendingOrder(ctx, tradeapp.CreatePendingOrderCommand{
		OrderNo: "legacy-unpaid", UserID: 2, ProjectID: 10, ProjectProductID: 20,
		ProductType: tradedomain.ProductTypeMicrosoft, ServiceMode: tradedomain.ServiceModeCode,
		SupplyPolicy: tradedomain.SupplyPolicyPublicOnly, PayAmount: "50000.00", CodeWindowMinutes: 10,
		ClientChannel: tradedomain.ClientChannelAPIKey, APIKeyID: &keyID, IdempotencyKey: "legacy-unpaid",
		RequestFingerprint: strings.Repeat("a", 64), Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, db.Table("api_keys").Where("id = ?", 7).Updates(map[string]any{"enabled": false, "deleted_at": time.Now().UTC()}).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	migrationDir := testmysql.MigrationsThrough(t, tradeMigrationsDir(t), 139)
	migrationFile := filepath.Join(migrationDir, "00139_api_key_point_quota.sql")
	original, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	for i, marker := range []string{"UPDATE api_keys AS k", "-- +goose Down"} {
		interrupted := strings.Replace(string(original), marker,
			"SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'injected migration interruption';\n"+marker, 1)
		require.NoError(t, os.WriteFile(migrationFile, []byte(interrupted), 0600))
		require.ErrorContains(t, goose.Up(sqlDB, migrationDir), "injected migration interruption")
		version, err := goose.GetDBVersion(sqlDB)
		require.NoError(t, err)
		require.EqualValues(t, 138, version)
		var interruptedKey openapiinfra.APIKeyModel
		require.NoError(t, db.First(&interruptedKey, 7).Error)
		require.EqualValues(t, 200, interruptedKey.RequestCount)
		require.Equal(t, []string{"0", "12345.123456"}[i], interruptedKey.QuotaUsed.String())
	}
	require.NoError(t, os.WriteFile(migrationFile, original, 0600))
	require.NoError(t, goose.Up(sqlDB, migrationDir))
	require.NoError(t, goose.Up(sqlDB, migrationDir))
	var keys []openapiinfra.APIKeyModel
	require.NoError(t, db.Order("id").Find(&keys).Error)
	require.Len(t, keys, 2)
	require.Equal(t, "12345.123456", keys[0].QuotaUsed.String())
	require.EqualValues(t, 200, keys[0].RequestCount)
	require.EqualValues(t, 1000, *keys[0].QuotaLimit)
	require.NotNil(t, keys[0].DeletedAt)
	require.True(t, keys[1].QuotaUsed.IsZero())
	require.EqualValues(t, 50, keys[1].RequestCount)
}

func TestAPIKeyPointQuotaOverLimitMetadataUpdateMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	ctx := context.Background()
	seedTradeBase(t, db, "microsoft")
	creditBuyer(t, db, 2, "10000.00")
	now := time.Now().UTC().Truncate(time.Second)
	db.NowFunc = func() time.Time { return now }
	keys := openapiapp.NewUseCase(openapiinfra.NewRepo(db))
	t.Cleanup(func() { require.NoError(t, keys.Close(ctx)) })
	limit := int64(1000)
	key, err := keys.CreateAPIKey(ctx, openapiapp.CreateAPIKeyRequest{
		UserID: 2, QuotaLimit: &limit, IdempotencyKey: "over-limit-key",
	})
	require.NoError(t, err)
	require.NoError(t, db.Table("api_keys").Where("id = ?", key.ID).UpdateColumn("quota_used", "5000.00").Error)
	name := "renamed key"
	expireAt := now.Add(24 * time.Hour)
	request := openapiapp.UpdateAPIKeyRequest{
		UserID: 2, KeyID: key.ID, Name: &name, ExpireSet: true, ExpireAt: &expireAt,
		ConcurrencySet: true, ConcurrencyLimit: intPointer(5), QuotaSet: true, QuotaLimit: &limit,
	}
	// Fixed UpdatedAt also exercises MySQL's zero-rows-changed result on replay.
	for range 2 {
		updated, err := keys.UpdateAPIKey(ctx, request)
		require.NoError(t, err)
		require.Equal(t, name, updated.Name)
		require.WithinDuration(t, expireAt, *updated.ExpireAt, 0)
		require.Equal(t, 5, *updated.ConcurrencyLimit)
		require.Equal(t, limit, *updated.QuotaLimit)
		require.Equal(t, "5000", updated.QuotaUsed.String())
	}
	for _, changedLimit := range []int64{999, 1001} {
		request.QuotaLimit = &changedLimit
		_, err := keys.UpdateAPIKey(ctx, request)
		require.ErrorIs(t, err, openapidomain.ErrAPIKeyQuotaExceeded)
	}
	wallet := billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))
	_, err = wallet.DebitConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID: 2, APIKeyID: &key.ID, Amount: "1.00", Reason: "over-limit debit", IdempotencyKey: "over-limit-debit",
	})
	require.ErrorIs(t, err, billingdomain.ErrInsufficientBalance)
}
