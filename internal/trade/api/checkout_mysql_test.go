package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapidomain "github.com/donnel666/remail/internal/openapi/domain"
	openapiinfra "github.com/donnel666/remail/internal/openapi/infra"
	"github.com/donnel666/remail/internal/platform/testmysql"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/gin-gonic/gin"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var tradeAPIMySQLTestServer = testmysql.New("remail_trade_api_test")

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	code := m.Run()
	_ = tradeAPIMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newTradeMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return tradeAPIMySQLTestServer.Database(t, tradeMigrationsDir(t))
}

func tradeInnoDBMetricCount(t *testing.T, db *gorm.DB, name string) uint64 {
	t.Helper()
	var count uint64
	require.NoError(t, db.Raw(`SELECT COUNT FROM information_schema.innodb_metrics WHERE NAME = ?`, name).Scan(&count).Error)
	return count
}

func tradeCheckoutFactCounts(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, 5)
	for _, table := range []string{"orders", "wallet_transactions", "microsoft_allocations", "order_tokens", "order_events"} {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		counts[table] = count
	}
	return counts
}

func tradeMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestCheckoutSuccessAndIdempotentReplayMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 2, true)
	creditBuyer(t, db, 2, "10.00")

	uc := newTradeUseCase(db)
	first, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "purchase",
		SupplyPolicy:   "private_first",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-success",
		RequestID:      "req-trade-success",
	})
	require.NoError(t, err)
	require.True(t, first.Created)
	require.Equal(t, tradedomain.OrderStatusActive, first.Order.Status)
	require.NotEmpty(t, first.Order.DeliveryEmail)
	require.NotEmpty(t, first.ServiceToken)
	require.NotNil(t, first.Order.DebitTxID)
	require.NotNil(t, first.Order.MicrosoftAllocID)
	require.NotNil(t, first.Order.ReceiveStartedAt)
	require.NotNil(t, first.Order.ReceiveUntil)
	require.Nil(t, first.Order.ActivatedAt)
	require.Nil(t, first.Order.AfterSaleUntil)
	require.InDelta(t, int64((60 * time.Minute).Seconds()), int64(first.Order.ReceiveUntil.Sub(*first.Order.ReceiveStartedAt).Seconds()), 1)

	replay, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "purchase",
		SupplyPolicy:   "private_first",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-success",
		RequestID:      "req-trade-success",
	})
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, first.Order.OrderNo, replay.Order.OrderNo)
	require.Equal(t, first.ServiceToken, replay.ServiceToken)

	var txCount int64
	require.NoError(t, db.Table("wallet_transactions").
		Where("user_id = ? AND transaction_type = ? AND biz_id = ?", 2, "debit", "order:"+first.Order.OrderNo).
		Count(&txCount).Error)
	require.EqualValues(t, 1, txCount)
	var debitAmount string
	require.NoError(t, db.Table("wallet_transactions").
		Select("amount").
		Where("id = ?", *first.Order.DebitTxID).
		Scan(&debitAmount).Error)
	require.Equal(t, "-2.000000", debitAmount)
	var allocationCount int64
	require.NoError(t, db.Table("microsoft_allocations").Where("order_no = ?", first.Order.OrderNo).Count(&allocationCount).Error)
	require.EqualValues(t, 1, allocationCount)
	var purchaseToken struct {
		ExpireAt *time.Time
	}
	require.NoError(t, db.Table("order_tokens").Select("expire_at").Where("order_no = ?", first.Order.OrderNo).Take(&purchaseToken).Error)
	require.Nil(t, purchaseToken.ExpireAt)

	matchedAt := first.Order.ReceiveStartedAt.Add(10 * time.Minute)
	require.NoError(t, uc.NotifyMatchedCode(context.Background(), tradeapp.MatchCodeResultRequest{
		OrderNo:   first.Order.OrderNo,
		MatchedAt: matchedAt,
	}))
	activated, err := uc.GetOrder(context.Background(), first.Order.OrderNo, 2, false)
	require.NoError(t, err)
	require.Equal(t, "Trade Project", activated.ProjectName)
	require.Equal(t, "/v1/projects/logos/trade-project", activated.ProjectLogoURL)
	require.Equal(t, tradedomain.OrderStatusActive, activated.Order.Status)
	require.NotNil(t, activated.Order.ActivatedAt)
	require.InDelta(t, int64(0), int64(activated.Order.ActivatedAt.Sub(matchedAt).Seconds()), 1)
	require.NotNil(t, activated.Order.AfterSaleUntil)
	require.InDelta(t, int64((1440 * time.Minute).Seconds()), int64(activated.Order.AfterSaleUntil.Sub(*first.Order.ReceiveStartedAt).Seconds()), 1)
}

func TestCheckoutZeroPricedPublicPurchaseMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Update("purchase_price", "0.000000").Error)
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)

	result, err := newTradeUseCase(db).Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "purchase",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-zero-priced-public-purchase",
		RequestID:      "req-zero-priced-public-purchase",
	})
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, result.Order.Status)
	require.Equal(t, "0.00", result.Order.PayAmount)
	require.NotNil(t, result.Order.DebitTxID)

	var debitAmount string
	require.NoError(t, db.Table("wallet_transactions").
		Select("amount").
		Where("id = ?", *result.Order.DebitTxID).
		Scan(&debitAmount).Error)
	require.Equal(t, "0.000000", debitAmount)
}

func TestImportHistoricalMicrosoftUsageUsesExistingOrderAllocationAndWalletFactsMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	first := time.Now().UTC().Add(-24 * time.Hour)
	last := time.Now().UTC()
	matches := []tradeapp.HistoricalMicrosoftUsage{
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "main", Email: "ms1000@example.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 3},
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "dot", Email: "ms.1000@example.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "plus", Email: "ms1000+used@example.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "alias", Email: "legacy-alias@outlook.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
	}
	uc := newTradeUseCase(db)
	for range 2 {
		require.NoError(t, uc.ImportHistoricalMicrosoftUsage(context.Background(), matches))
	}
	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 1000).Updates(map[string]any{
		"email_address": "corrected@example.com",
		"email_domain":  "example.com",
	}).Error)
	matches[0].Email = "corrected@example.com"
	require.NoError(t, uc.ImportHistoricalMicrosoftUsage(context.Background(), matches))

	var historicalOrders int64
	require.NoError(t, db.Table("orders").Where("order_no LIKE 'HIST-%'").Count(&historicalOrders).Error)
	require.Equal(t, int64(4), historicalOrders)
	var historicalRows []struct {
		UserID         uint       `gorm:"column:user_id"`
		Status         string     `gorm:"column:status"`
		ServiceMode    string     `gorm:"column:service_mode"`
		PayAmount      string     `gorm:"column:pay_amount"`
		AfterSaleUntil *time.Time `gorm:"column:after_sale_until"`
	}
	require.NoError(t, db.Table("orders").
		Select("user_id, status, service_mode, pay_amount, after_sale_until").
		Where("order_no LIKE 'HIST-%'").Find(&historicalRows).Error)
	for _, row := range historicalRows {
		require.Equal(t, uint(1), row.UserID)
		require.Equal(t, "completed", row.Status)
		require.Equal(t, "purchase", row.ServiceMode)
		require.Equal(t, "0.000000", row.PayAmount)
		require.NotNil(t, row.AfterSaleUntil)
		require.True(t, row.AfterSaleUntil.Before(time.Now().UTC()))
	}
	var historicalAllocations int64
	require.NoError(t, db.Table("microsoft_allocations").Where("order_no LIKE 'HIST-%' AND status = 'released'").Count(&historicalAllocations).Error)
	require.Equal(t, int64(4), historicalAllocations)
	for _, table := range []string{"explicit_aliases", "dot_aliases", "plus_aliases"} {
		var count int64
		require.NoError(t, db.Table(table).Where("resource_id = ?", 1000).Count(&count).Error)
		require.Equal(t, int64(1), count, table)
	}
	var debitCount int64
	require.NoError(t, db.Table("wallet_transactions").Where("user_id = ? AND transaction_type = 'debit' AND amount = 0", 1).Count(&debitCount).Error)
	require.Equal(t, int64(4), debitCount)
	var tokenCount int64
	require.NoError(t, db.Table("order_tokens").Where("order_no LIKE 'HIST-%'").Count(&tokenCount).Error)
	require.Zero(t, tokenCount)

	rollbackMatches := []tradeapp.HistoricalMicrosoftUsage{
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "alias", Email: "rolled-back@outlook.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "main", Email: "corrected@example.com", FirstMatchedAt: first, LastMatchedAt: last},
	}
	require.Error(t, uc.ImportHistoricalMicrosoftUsage(context.Background(), rollbackMatches))
	var rolledBackAliases int64
	require.NoError(t, db.Table("explicit_aliases").Where("resource_id = ? AND email = ?", 1000, "rolled-back@outlook.com").Count(&rolledBackAliases).Error)
	require.Zero(t, rolledBackAliases)
}

func TestCreateHistoricalOrderConflictStopsOuterTransactionMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	createdAt := time.Now().UTC().Add(-24 * time.Hour)
	expiredAt := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, newTradeUseCase(db).ImportHistoricalMicrosoftUsage(context.Background(), []tradeapp.HistoricalMicrosoftUsage{{
		ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "main", Email: "ms1000@example.com",
		FirstMatchedAt: createdAt, LastMatchedAt: expiredAt, EvidenceCount: 1,
	}}))

	var existing struct {
		OrderNo             string `gorm:"column:order_no"`
		DebitTxID           uint   `gorm:"column:debit_tx_id"`
		MicrosoftAllocation uint   `gorm:"column:microsoft_alloc_id"`
	}
	require.NoError(t, db.Table("orders").
		Select("order_no, debit_tx_id, microsoft_alloc_id").
		Where("order_no LIKE 'HIST-%'").Take(&existing).Error)

	repo := tradeinfra.NewRepo(db)
	continued := false
	err := repo.WithTx(context.Background(), func(txCtx context.Context) error {
		if err := repo.CreateHistoricalOrder(txCtx, tradeapp.CreateHistoricalOrderCommand{
			OrderNo: existing.OrderNo, UserID: 3, ProjectID: 10, ProjectProductID: 20,
			DebitTxID: existing.DebitTxID, MicrosoftAllocationID: existing.MicrosoftAllocation,
			DeliveryEmail: "ms1000@example.com", CreatedAt: createdAt, ExpiredAt: expiredAt, Now: time.Now().UTC(),
		}); err != nil {
			return err
		}
		continued = true
		return nil
	})

	require.ErrorIs(t, err, tradedomain.ErrIdempotencyConflict)
	require.False(t, continued)
}

func TestImportHistoricalMicrosoftUsageOnlyBackfillsMissingAllocationRelationsMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	first := time.Now().UTC().Add(-24 * time.Hour)
	last := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (1000, 1, 'existing-alias@example.com', 'normal')`).Error)
	var existingAliasID uint
	require.NoError(t, db.Table("explicit_aliases").Select("id").Where("email = ?", "existing-alias@example.com").Scan(&existingAliasID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type)
VALUES
    ('existing-main-allocation', 'microsoft'),
    ('existing-alias-allocation', 'microsoft')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox,
    explicit_alias_id, email, status, created_at, released_at
) VALUES
    (?, ?, ?, ?, 'public', 'main', NULL, ?, 'released', ?, ?),
    (?, ?, ?, ?, 'public', 'alias', ?, ?, 'released', ?, ?)`,
		"existing-main-allocation", 10, 20, 1000, "ms1000@example.com", first, last,
		"existing-alias-allocation", 10, 20, 1000, existingAliasID, "existing-alias@example.com", first, last,
	).Error)

	uc := newTradeUseCase(db)
	require.NoError(t, uc.ImportHistoricalMicrosoftUsage(context.Background(), []tradeapp.HistoricalMicrosoftUsage{
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "main", Email: "ms1000@example.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "alias", Email: "existing-alias@example.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
	}))
	var historicalOrders int64
	require.NoError(t, db.Table("orders").Where("order_no LIKE 'HIST-%'").Count(&historicalOrders).Error)
	require.Zero(t, historicalOrders)

	err := uc.ImportHistoricalMicrosoftUsage(context.Background(), []tradeapp.HistoricalMicrosoftUsage{
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "main", Email: "ms1000@example.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "alias", Email: "existing-alias@example.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
		{ResourceID: 1000, ProjectID: 10, ProductID: 20, Mailbox: "alias", Email: "missing-alias@outlook.com", FirstMatchedAt: first, LastMatchedAt: last, EvidenceCount: 1},
	})
	require.NoError(t, err)

	require.NoError(t, db.Table("orders").Where("order_no LIKE 'HIST-%'").Count(&historicalOrders).Error)
	require.Equal(t, int64(1), historicalOrders)
	var historicalAllocations []struct {
		Mailbox string `gorm:"column:mailbox"`
	}
	require.NoError(t, db.Table("microsoft_allocations").
		Select("mailbox").Where("order_no LIKE 'HIST-%'").Find(&historicalAllocations).Error)
	require.Equal(t, []struct {
		Mailbox string `gorm:"column:mailbox"`
	}{{Mailbox: "alias"}}, historicalAllocations)
}

func TestCheckoutPurchaseOrderContinuesAfterProductDelistedMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 2, true)
	creditBuyer(t, db, 2, "10.00")

	uc := newTradeUseCase(db)
	request := tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "purchase",
		SupplyPolicy:   "private_first",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-delisted-product-existing",
		RequestID:      "req-delisted-product-existing",
	}
	first, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, first.Order.Status)
	require.Equal(t, 60, first.Order.ActivationWindowMinutes)
	require.Equal(t, 1440, first.Order.WarrantyMinutes)

	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Updates(map[string]any{
		"status":                    "disabled",
		"activation_window_minutes": 5,
		"warranty_minutes":          5,
	}).Error)

	newRequest := request
	newRequest.IdempotencyKey = "order-delisted-product-new"
	_, err = uc.Checkout(context.Background(), newRequest)
	require.ErrorIs(t, err, tradedomain.ErrProjectUnavailable)

	replay, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, first.Order.OrderNo, replay.Order.OrderNo)

	matchedAt := first.Order.ReceiveStartedAt.Add(10 * time.Minute)
	require.NoError(t, uc.NotifyMatchedCode(context.Background(), tradeapp.MatchCodeResultRequest{
		OrderNo:   first.Order.OrderNo,
		MatchedAt: matchedAt,
	}))
	activated, err := uc.GetOrder(context.Background(), first.Order.OrderNo, 2, false)
	require.NoError(t, err)
	require.NotNil(t, activated.Order.ActivatedAt)
	require.NotNil(t, activated.Order.AfterSaleUntil)
	require.InDelta(t, int64((1440 * time.Minute).Seconds()), int64(activated.Order.AfterSaleUntil.Sub(*first.Order.ReceiveStartedAt).Seconds()), 1)
}

func TestOrderServiceSnapshotMigrationBackfillsDelistedProductMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, tradeMigrationsDir(t), 12))

	seedTradeBaseLegacyEnabled(t, db, "microsoft")
	require.NoError(t, db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type,
    service_mode, supply_policy, status, failure_code, pay_amount,
    refund_amount, client_channel, idempotency_key, request_fingerprint,
    service_cleanup_status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-delisted-product-order",
		2,
		10,
		20,
		"microsoft",
		"purchase",
		"public_only",
		"pending_payment",
		"",
		"2.000000",
		"0.000000",
		"console",
		"legacy-delisted-product-key",
		strings.Repeat("a", 64),
		"none",
	).Error)
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Update("status", "disabled").Error)

	require.NoError(t, goose.UpTo(sqlDB, tradeMigrationsDir(t), 13))
	var snapshot struct {
		CodeWindowMinutes       int
		ActivationWindowMinutes int
		WarrantyMinutes         int
	}
	require.NoError(t, db.Table("orders").
		Select("code_window_minutes, activation_window_minutes, warranty_minutes").
		Where("order_no = ?", "legacy-delisted-product-order").
		Take(&snapshot).Error)
	require.Equal(t, 10, snapshot.CodeWindowMinutes)
	require.Equal(t, 60, snapshot.ActivationWindowMinutes)
	require.Equal(t, 1440, snapshot.WarrantyMinutes)
}

func TestCheckoutOwnedMicrosoftStockCreatesZeroDebitMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 2, 1000, 1, false)

	uc := newTradeUseCase(db)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "purchase",
		SupplyPolicy:   "private_first",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-owned-microsoft",
		RequestID:      "req-owned-microsoft",
	})
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, result.Order.Status)
	require.Equal(t, "0.00", result.Order.PayAmount)
	require.NotNil(t, result.Order.DebitTxID)
	require.NotNil(t, result.Order.MicrosoftAllocID)
	require.Equal(t, "ms1000@example.com", result.Order.DeliveryEmail)

	var tx struct {
		Amount        string
		BalanceBefore string
		BalanceAfter  string
	}
	require.NoError(t, db.Table("wallet_transactions").
		Select("amount, balance_before, balance_after").
		Where("id = ?", *result.Order.DebitTxID).
		Take(&tx).Error)
	require.Equal(t, "0.000000", tx.Amount)
	require.Equal(t, "0.000000", tx.BalanceBefore)
	require.Equal(t, "0.000000", tx.BalanceAfter)
	var supplyScope string
	require.NoError(t, db.Table("microsoft_allocations").
		Select("supply_scope").
		Where("id = ?", *result.Order.MicrosoftAllocID).
		Scan(&supplyScope).Error)
	require.Equal(t, "owned", supplyScope)
}

func TestCheckoutOwnedDomainStockCreatesZeroDebitMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "domain")
	seedTradeDomainResources(t, db, 2, 2000, 1, "not_sale")

	uc := newTradeUseCase(db)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "purchase",
		SupplyPolicy:   "private_first",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-owned-domain",
		RequestID:      "req-owned-domain",
	})
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, result.Order.Status)
	require.Equal(t, "0.00", result.Order.PayAmount)
	require.NotNil(t, result.Order.DebitTxID)
	require.NotNil(t, result.Order.DomainAllocID)
	require.Contains(t, result.Order.DeliveryEmail, "@trade2000.example.com")

	var tx struct {
		Amount        string
		BalanceBefore string
		BalanceAfter  string
	}
	require.NoError(t, db.Table("wallet_transactions").
		Select("amount, balance_before, balance_after").
		Where("id = ?", *result.Order.DebitTxID).
		Take(&tx).Error)
	require.Equal(t, "0.000000", tx.Amount)
	require.Equal(t, "0.000000", tx.BalanceBefore)
	require.Equal(t, "0.000000", tx.BalanceAfter)
	var supplyScope string
	require.NoError(t, db.Table("domain_allocations").
		Select("supply_scope").
		Where("id = ?", *result.Order.DomainAllocID).
		Scan(&supplyScope).Error)
	require.Equal(t, "owned", supplyScope)
}

func TestCheckoutAllocationFailureDoesNotDebitMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	creditBuyer(t, db, 2, "10.00")

	uc := newTradeUseCase(db)
	_, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-refund",
		RequestID:      "req-trade-refund",
	})
	require.ErrorIs(t, err, tradedomain.ErrInsufficientInventory)
	_, replayErr := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-refund",
		RequestID:      "req-trade-refund-replay",
	})
	require.ErrorIs(t, replayErr, tradedomain.ErrInsufficientInventory)

	var order struct {
		OrderNo      string
		Status       string
		FailureCode  string
		DebitTxID    *uint
		RefundTxID   *uint
		RefundAmount string
	}
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "order-idem-refund").Take(&order).Error)
	require.Equal(t, string(tradedomain.OrderStatusFailed), order.Status)
	require.Equal(t, string(tradedomain.OrderFailureInsufficientInventory), order.FailureCode)
	require.Nil(t, order.DebitTxID)
	require.Nil(t, order.RefundTxID)
	require.Equal(t, "0.000000", order.RefundAmount)
	var guardCount int64
	require.NoError(t, db.Table("allocation_order_guards").Where("order_no = ?", order.OrderNo).Count(&guardCount).Error)
	require.EqualValues(t, 0, guardCount)

	summary, err := billinginfra.NewBillingRepo(db).GetOrCreateWalletSummary(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, "10.00", summary.Wallet.ConsumerBalance)
}

func TestCheckoutMarkFailedErrorRollsBackPendingOrderMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	creditBuyer(t, db, 2, "10.00")
	baseRepo := tradeinfra.NewRepo(db)
	projects := coreapp.NewProjectUseCase(coreinfra.NewProjectRepo(db))
	wallet := billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))
	alloc := allocapp.NewUseCase(allocinfra.NewRepo(db))
	tokens := openapiapp.NewUseCase(openapiinfra.NewRepo(db))
	uc := tradeapp.NewUseCase(
		&markFailedErrorRepo{Repository: baseRepo},
		coreOrderingAdapter{projects: projects},
		billingWalletAdapter{wallet: wallet},
		allocationAdapter{alloc: alloc},
		orderTokenAdapter{tokens: tokens},
	)
	_, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-mark-failed-rollback",
		RequestID:      "req-mark-failed-rollback",
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, tradedomain.ErrInsufficientInventory)

	var orderCount int64
	require.NoError(t, db.Table("orders").
		Where("idempotency_key = ?", "order-idem-mark-failed-rollback").
		Count(&orderCount).Error)
	require.Zero(t, orderCount)
}

type markFailedErrorRepo struct {
	tradeapp.Repository
}

func (r *markFailedErrorRepo) MarkFailed(context.Context, tradeapp.MarkFailedCommand) (*tradedomain.Order, error) {
	return nil, fmt.Errorf("forced mark failed error")
}

func TestCheckoutInsufficientBalanceReleasesAllocationMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)

	uc := newTradeUseCase(db)
	_, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-insufficient-balance",
		RequestID:      "req-insufficient-balance",
	})
	require.ErrorIs(t, err, tradedomain.ErrInsufficientBalance)
	_, replayErr := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-insufficient-balance",
		RequestID:      "req-insufficient-balance-replay",
	})
	require.ErrorIs(t, replayErr, tradedomain.ErrInsufficientBalance)

	var order struct {
		OrderNo      string
		Status       string
		FailureCode  string
		DebitTxID    *uint
		RefundTxID   *uint
		RefundAmount string
	}
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "order-idem-insufficient-balance").Take(&order).Error)
	require.Equal(t, string(tradedomain.OrderStatusFailed), order.Status)
	require.Equal(t, string(tradedomain.OrderFailureInsufficientBalance), order.FailureCode)
	require.Nil(t, order.DebitTxID)
	require.Nil(t, order.RefundTxID)
	require.Equal(t, "0.000000", order.RefundAmount)

	var allocation struct {
		Status string
	}
	require.NoError(t, db.Table("microsoft_allocations").Where("order_no = ?", order.OrderNo).Take(&allocation).Error)
	require.Equal(t, "released", allocation.Status)
}

func TestExpireDueOrdersRefundsExpiredCodeAndCleansServiceMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	require.NoError(t, db.Table("project_products").
		Where("id = ?", 20).
		Updates(map[string]any{
			"code_price":          "0.008",
			"code_supplier_price": "0.005",
		}).Error)
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "1.00")

	uc := newTradeUseCase(db)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-expired-code",
		RequestID:      "req-expired-code",
	})
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, result.Order.Status)
	require.Equal(t, "0.008", result.Order.PayAmount)
	past := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Table("orders").
		Where("order_no = ?", result.Order.OrderNo).
		Updates(map[string]any{"receive_until": past, "after_sale_until": past}).Error)

	expired, err := uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Equal(t, 1, expired.CodeTimedOut)
	require.Equal(t, 0, expired.DeliveryReconciled)
	require.Equal(t, 0, expired.Failed)

	var order struct {
		Status               string
		RefundTxID           *uint
		RefundAmount         string
		ServiceCleanupStatus string
	}
	require.NoError(t, db.Table("orders").
		Select("status, refund_tx_id, refund_amount, service_cleanup_status").
		Where("order_no = ?", result.Order.OrderNo).
		Take(&order).Error)
	require.Equal(t, string(tradedomain.OrderStatusRefunded), order.Status)
	require.NotNil(t, order.RefundTxID)
	require.Equal(t, "0.008000", order.RefundAmount)
	require.Equal(t, "succeeded", order.ServiceCleanupStatus)

	var allocationStatus string
	require.NoError(t, db.Table("microsoft_allocations").
		Select("status").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&allocationStatus).Error)
	require.Equal(t, "released", allocationStatus)

	var tokenEnabled bool
	require.NoError(t, db.Table("order_tokens").
		Select("enabled").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&tokenEnabled).Error)
	require.False(t, tokenEnabled)

	var refundCount int64
	require.NoError(t, db.Table("wallet_transactions").
		Where("user_id = ? AND transaction_type = ? AND biz_id = ?", 2, "refund", "order:"+result.Order.OrderNo).
		Count(&refundCount).Error)
	require.EqualValues(t, 1, refundCount)

	summary, err := billinginfra.NewBillingRepo(db).GetOrCreateWalletSummary(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, "1.00", summary.Wallet.ConsumerBalance)

	replayed, err := uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Equal(t, 0, replayed.CodeTimedOut)
	require.NoError(t, db.Table("wallet_transactions").
		Where("user_id = ? AND transaction_type = ? AND biz_id = ?", 2, "refund", "order:"+result.Order.OrderNo).
		Count(&refundCount).Error)
	require.EqualValues(t, 1, refundCount)
}

func TestExpireDueOrdersCompletesExpiredCodeWithDeliveryWithoutRefundMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")

	uc := newTradeUseCase(db)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-expired-code-delivery",
		RequestID:      "req-expired-code-delivery",
	})
	require.NoError(t, err)
	past := time.Now().UTC().Add(-time.Minute)
	receivedAt := time.Now().UTC()
	require.NoError(t, db.Table("orders").
		Where("order_no = ?", result.Order.OrderNo).
		Updates(map[string]any{"receive_until": past, "after_sale_until": past}).Error)
	var resourceID uint
	require.NoError(t, db.Table("microsoft_allocations").
		Select("resource_id").
		Where("id = ?", result.Order.MicrosoftAllocID).
		Scan(&resourceID).Error)
	require.NotZero(t, resourceID)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, recipient, sender, subject, raw_body,
    verification_code, dedupe_key, status, received_at
) VALUES (?, 'microsoft', ?, 'noreply@example.com', 'Code', 'Your code is 123456',
          '', REPEAT('d', 64), 'received', ?)`,
		resourceID,
		result.Order.DeliveryEmail,
		receivedAt,
	).Error)
	var messageID uint
	require.NoError(t, db.Table("mailmatch_messages").
		Select("id").
		Where("email_resource_id = ? AND dedupe_key = REPEAT('d', 64)", resourceID).
		Scan(&messageID).Error)
	require.NotZero(t, messageID)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_message_projections(
    message_id, matched_order_id, status, verification_code,
    match_diagnostic, message_received_at
) VALUES (?, ?, 'matched', '123456', 'projection-only delivery', ?)`,
		messageID,
		result.Order.ID,
		receivedAt,
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_order_delivery_heads(order_id, message_id, message_received_at)
VALUES (?, ?, ?)`, result.Order.ID, messageID, receivedAt).Error)

	detail, err := uc.GetOrder(context.Background(), result.Order.OrderNo, result.Order.UserID, false)
	require.NoError(t, err)
	require.True(t, detail.HasDelivery)
	require.Equal(t, "123456", detail.VerificationCode)
	require.NotNil(t, detail.LastMailReceivedAt)
	require.WithinDuration(t, receivedAt, *detail.LastMailReceivedAt, time.Second)

	listed, err := uc.ListOrders(context.Background(), tradeapp.OrderListFilter{UserID: result.Order.UserID}, 0, 0, 20)
	require.NoError(t, err)
	require.Nil(t, listed.NextAfterID)
	require.EqualValues(t, 1, listed.Total)
	require.Len(t, listed.Items, 1)
	require.True(t, listed.Items[0].HasDelivery)
	require.Equal(t, "123456", listed.Items[0].VerificationCode)

	expired, err := uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Equal(t, 0, expired.CodeTimedOut)
	require.Equal(t, 1, expired.DeliveryReconciled)
	require.Equal(t, 0, expired.Failed)

	var order struct {
		Status       string
		RefundTxID   *uint
		AfterSale    *time.Time `gorm:"column:after_sale_until"`
		RefundAmount string
	}
	require.NoError(t, db.Table("orders").
		Select("status, refund_tx_id, refund_amount, after_sale_until").
		Where("order_no = ?", result.Order.OrderNo).
		Take(&order).Error)
	require.Equal(t, string(tradedomain.OrderStatusCompleted), order.Status)
	require.Nil(t, order.RefundTxID)
	require.Equal(t, "0.000000", order.RefundAmount)
	require.NotNil(t, order.AfterSale)
	require.InDelta(t, int64(time.Hour.Seconds()), int64(order.AfterSale.Sub(receivedAt).Seconds()), 1)

	var tokenExpireAt *time.Time
	require.NoError(t, db.Table("order_tokens").
		Select("expire_at").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&tokenExpireAt).Error)
	require.NotNil(t, tokenExpireAt)
	require.InDelta(t, int64(time.Hour.Seconds()), int64(tokenExpireAt.Sub(receivedAt).Seconds()), 1)

	var refundCount int64
	require.NoError(t, db.Table("wallet_transactions").
		Where("user_id = ? AND transaction_type = ? AND biz_id = ?", 2, "refund", "order:"+result.Order.OrderNo).
		Count(&refundCount).Error)
	require.EqualValues(t, 0, refundCount)
}

func TestExpireDueOrdersCompletesExpiredPurchaseWithoutRefundOrReleaseMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")

	uc := newTradeUseCase(db)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "purchase",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-expired-purchase",
		RequestID:      "req-expired-purchase",
	})
	require.NoError(t, err)
	past := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Table("orders").
		Where("order_no = ?", result.Order.OrderNo).
		Updates(map[string]any{"receive_until": past}).Error)

	expired, err := uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Equal(t, 1, expired.PurchaseActivationCompleted)
	require.Equal(t, 0, expired.Failed)

	var order struct {
		Status               string
		RefundTxID           *uint
		ServiceCleanupStatus string
	}
	require.NoError(t, db.Table("orders").
		Select("status, refund_tx_id, service_cleanup_status").
		Where("order_no = ?", result.Order.OrderNo).
		Take(&order).Error)
	require.Equal(t, string(tradedomain.OrderStatusCompleted), order.Status)
	require.Nil(t, order.RefundTxID)
	require.Equal(t, "none", order.ServiceCleanupStatus)

	var allocationStatus string
	require.NoError(t, db.Table("microsoft_allocations").
		Select("status").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&allocationStatus).Error)
	require.Equal(t, "allocated", allocationStatus)

	var tokenEnabled bool
	require.NoError(t, db.Table("order_tokens").
		Select("enabled").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&tokenEnabled).Error)
	require.True(t, tokenEnabled)
}

func TestAdminOrderRefundRouteWritesOperationLogMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")

	uc := newTradeUseCase(db)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "purchase",
		SupplyPolicy:   "public_only",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-admin-refund",
		RequestID:      "req-admin-refund-order",
	})
	require.NoError(t, err)

	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterRoutes(router.Group("/v1"), newTradeModule(db), fixedTradeSessionFetcher{}, allowTradePermissionChecker{})

	body := strings.NewReader(`{"reason":"manual support refund"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/orders/"+result.Order.OrderNo+"/refund", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "admin-refund-idem")
	req.Header.Set(middleware.CSRFHeaderName, "csrf")
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "sid-admin"})
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var logCount int64
	require.NoError(t, db.Table("operation_logs").
		Where("operator_user_id = ? AND operation_type = ? AND resource_id = ? AND result = ?", 1, "trade.order.refund", result.Order.OrderNo, "success").
		Count(&logCount).Error)
	require.EqualValues(t, 1, logCount)
	var allocationStatus string
	require.NoError(t, db.Table("microsoft_allocations").
		Select("status").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&allocationStatus).Error)
	require.Equal(t, "released", allocationStatus)
	var tokenEnabled bool
	require.NoError(t, db.Table("order_tokens").
		Select("enabled").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&tokenEnabled).Error)
	require.False(t, tokenEnabled)

	require.NoError(t, db.Table("microsoft_allocations").
		Where("order_no = ?", result.Order.OrderNo).
		Update("status", "allocated").Error)
	require.NoError(t, db.Table("order_tokens").
		Where("order_no = ?", result.Order.OrderNo).
		Updates(map[string]any{
			"enabled":         true,
			"disabled_at":     nil,
			"disabled_reason": "",
		}).Error)
	require.NoError(t, db.Table("orders").
		Where("order_no = ?", result.Order.OrderNo).
		Update("service_cleanup_status", "partial_failure").Error)

	retried, err := uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Equal(t, 1, retried.CleanupRetried)
	require.Equal(t, 0, retried.Failed)
	require.NoError(t, db.Table("microsoft_allocations").
		Select("status").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&allocationStatus).Error)
	require.Equal(t, "released", allocationStatus)
	require.NoError(t, db.Table("order_tokens").
		Select("enabled").
		Where("order_no = ?", result.Order.OrderNo).
		Scan(&tokenEnabled).Error)
	require.False(t, tokenEnabled)
}

func TestOrderRouteAcceptsAPIKeyWithoutCSRFMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")

	openapiMod := openapiapi.NewModule(db)
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:         2,
		Name:           "sdk",
		IdempotencyKey: "apikey-idem-trade",
		RequestID:      "req-apikey-trade",
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(key.KeyPlain, "rk-"))

	router := gin.New()
	router.Use(middleware.RequestID())
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)

	body, err := json.Marshal(CreateOrderRequest{ProjectID: 10, ProductID: 20})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/open/orders?serviceMode=code&supply=public_only", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key.KeyPlain)
	req.Header.Set("Idempotency-Key", "route-order-idem")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp OrderResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "active", resp.Status)
	require.NotEmpty(t, resp.ServiceToken)
	require.NotEmpty(t, resp.DeliveryEmail)
	require.NotNil(t, resp.ReceiveStartedAt)
	require.NotNil(t, resp.ReceiveUntil)
	require.Nil(t, resp.ActivatedAt)
	require.NotNil(t, resp.AfterSaleUntil)
	var codeToken struct {
		ExpireAt *time.Time
	}
	require.NoError(t, db.Table("order_tokens").Select("expire_at").Where("order_no = ?", resp.OrderNo).Take(&codeToken).Error)
	require.NotNil(t, codeToken.ExpireAt)

	listReq := httptest.NewRequest(http.MethodGet, "/v1/open/orders?scope=mine", nil)
	listReq.Header.Set("Authorization", "Bearer "+key.KeyPlain)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listResp OrderListResponse
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listResp))
	require.Len(t, listResp.Items, 1)
	require.Empty(t, listResp.Items[0].ServiceToken)
}

func TestOrderRouteCreatesIndependentBatchWithStableIdempotencyMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 2, true)
	creditBuyer(t, db, 2, "10.00")

	openapiMod := openapiapi.NewModule(db)
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:         2,
		Name:           "batch-sdk",
		IdempotencyKey: "apikey-idem-trade-batch",
		RequestID:      "req-apikey-trade-batch",
	})
	require.NoError(t, err)

	router := gin.New()
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)

	quantity := 2
	body, err := json.Marshal(CreateOrderBatchRequest{
		CreateOrderRequest: CreateOrderRequest{ProjectID: 10, ProductID: 20},
		Quantity:           quantity,
	})
	require.NoError(t, err)
	request := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/open/orders/batch?serviceMode=code&supply=public_only", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key.KeyPlain)
		req.Header.Set("Idempotency-Key", "route-order-batch")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	created := request(body)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var orders CreateOrderBatchResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &orders))
	require.Len(t, orders, quantity)
	require.Equal(t, "succeeded", orders[0].Status)
	require.Equal(t, "succeeded", orders[1].Status)
	require.NotEqual(t, orders[0].Order.OrderNo, orders[1].Order.OrderNo)

	replayed := request(body)
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	var replayedOrders CreateOrderBatchResponse
	require.NoError(t, json.Unmarshal(replayed.Body.Bytes(), &replayedOrders))
	require.Equal(t,
		[]string{orders[0].Order.OrderNo, orders[1].Order.OrderNo},
		[]string{replayedOrders[0].Order.OrderNo, replayedOrders[1].Order.OrderNo},
	)

	changedBody, err := json.Marshal(CreateOrderBatchRequest{
		CreateOrderRequest: CreateOrderRequest{ProjectID: 10, ProductID: 20},
		Quantity:           3,
	})
	require.NoError(t, err)
	conflict := request(changedBody)
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())

	var orderCount int64
	require.NoError(t, db.Table("orders").Where("user_id = ?", 2).Count(&orderCount).Error)
	require.EqualValues(t, quantity, orderCount)
}

func TestOrderRouteHundredItemBatchReturnsWithinTenSecondsMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Update("code_price", "0.000000").Error)
	seedTradeMicrosoftResources(t, db, 1, 1000, 100, true)

	openapiMod := openapiapi.NewModule(db)
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID: 2, Name: "hundred-batch-sdk", IdempotencyKey: "apikey-idem-hundred-batch", RequestID: "req-hundred-batch",
	})
	require.NoError(t, err)
	router := gin.New()
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)
	body, err := json.Marshal(CreateOrderBatchRequest{
		CreateOrderRequest: CreateOrderRequest{ProjectID: 10, ProductID: 20}, Quantity: 100,
	})
	require.NoError(t, err)
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/open/orders/batch?serviceMode=code&supply=public_only", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key.KeyPlain)
		req.Header.Set("Idempotency-Key", "route-order-hundred-batch")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	deadlocksBefore := tradeInnoDBMetricCount(t, db, "lock_deadlocks")
	timeoutsBefore := tradeInnoDBMetricCount(t, db, "lock_timeouts")

	started := time.Now()
	rec := request()
	elapsed := time.Since(started)
	t.Logf("100-item checkout request completed in %s", elapsed)

	require.Less(t, elapsed, 10*time.Second)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var items CreateOrderBatchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
	require.Len(t, items, 100)
	facts := tradeCheckoutFactCounts(t, db)
	replayed := request()
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	require.Equal(t, facts, tradeCheckoutFactCounts(t, db), "idempotent replay must not create any new checkout fact")
	require.Equal(t, deadlocksBefore, tradeInnoDBMetricCount(t, db, "lock_deadlocks"))
	require.Equal(t, timeoutsBefore, tradeInnoDBMetricCount(t, db, "lock_timeouts"))
}

func TestOrderRouteConcurrentHundredItemBatchesForDifferentUsersHaveNoDeadlocksMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Update("code_price", "0.000000").Error)
	seedTradeMicrosoftResources(t, db, 1, 1000, 100, true)
	seedTradeMicrosoftResources(t, db, 1, 1100, 100, true)

	openapiMod := openapiapi.NewModule(db)
	keys := make(map[uint]string, 2)
	for _, userID := range []uint{2, 3} {
		key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
			UserID: userID, Name: fmt.Sprintf("hundred-batch-%d", userID),
			IdempotencyKey: fmt.Sprintf("apikey-idem-hundred-batch-%d", userID), RequestID: fmt.Sprintf("req-hundred-batch-%d", userID),
		})
		require.NoError(t, err)
		keys[userID] = key.KeyPlain
	}
	router := gin.New()
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)
	body, err := json.Marshal(CreateOrderBatchRequest{
		CreateOrderRequest: CreateOrderRequest{ProjectID: 10, ProductID: 20}, Quantity: 100,
	})
	require.NoError(t, err)
	deadlocksBefore := tradeInnoDBMetricCount(t, db, "lock_deadlocks")
	timeoutsBefore := tradeInnoDBMetricCount(t, db, "lock_timeouts")

	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, len(keys))
	var wg sync.WaitGroup
	for userID, key := range keys {
		wg.Add(1)
		go func(userID uint, key string) {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/v1/open/orders/batch?serviceMode=code&supply=public_only", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Idempotency-Key", fmt.Sprintf("route-order-hundred-batch-%d", userID))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			results <- rec
		}(userID, key)
	}
	close(start)
	wg.Wait()
	close(results)
	for rec := range results {
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var items CreateOrderBatchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
		require.Len(t, items, 100)
	}
	var orderCount, allocationCount int64
	require.NoError(t, db.Table("orders").Count(&orderCount).Error)
	require.NoError(t, db.Table("microsoft_allocations").Count(&allocationCount).Error)
	require.EqualValues(t, 200, orderCount)
	require.EqualValues(t, 200, allocationCount)
	require.Equal(t, deadlocksBefore, tradeInnoDBMetricCount(t, db, "lock_deadlocks"))
	require.Equal(t, timeoutsBefore, tradeInnoDBMetricCount(t, db, "lock_timeouts"))
}

func TestOrderRouteBatchPartialFailureConvergesOnRetryMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")

	openapiMod := openapiapi.NewModule(db)
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:         2,
		Name:           "partial-batch-sdk",
		IdempotencyKey: "apikey-idem-trade-partial-batch",
		RequestID:      "req-apikey-trade-partial-batch",
	})
	require.NoError(t, err)

	router := gin.New()
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)
	body, err := json.Marshal(CreateOrderBatchRequest{
		CreateOrderRequest: CreateOrderRequest{ProjectID: 10, ProductID: 20},
		Quantity:           3,
	})
	require.NoError(t, err)
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/open/orders/batch?serviceMode=code&supply=public_only", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key.KeyPlain)
		req.Header.Set("Idempotency-Key", "route-order-partial-batch")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	first := request()
	require.Equal(t, http.StatusMultiStatus, first.Code, first.Body.String())
	var firstResults CreateOrderBatchResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstResults))
	require.Len(t, firstResults, 3)
	require.Equal(t, "succeeded", firstResults[0].Status)
	for i := 1; i < len(firstResults); i++ {
		require.Equal(t, "failed", firstResults[i].Status)
		require.NotNil(t, firstResults[i].Error)
		require.Equal(t, "insufficient_inventory", firstResults[i].Error.Code)
	}

	replay := request()
	require.Equal(t, http.StatusMultiStatus, replay.Code, replay.Body.String())
	var replayResults CreateOrderBatchResponse
	require.NoError(t, json.Unmarshal(replay.Body.Bytes(), &replayResults))
	require.Len(t, replayResults, 3)
	for i := range firstResults {
		require.Equal(t, firstResults[i].Index, replayResults[i].Index)
		require.Equal(t, firstResults[i].Status, replayResults[i].Status)
		require.Equal(t, firstResults[i].Order.OrderNo, replayResults[i].Order.OrderNo)
		if firstResults[i].Error == nil {
			require.Nil(t, replayResults[i].Error)
		} else {
			require.Equal(t, firstResults[i].Error.Code, replayResults[i].Error.Code)
		}
	}

	var orderCount int64
	require.NoError(t, db.Table("orders").Where("user_id = ?", 2).Count(&orderCount).Error)
	require.EqualValues(t, 3, orderCount)
	var activeCount int64
	require.NoError(t, db.Table("orders").Where("user_id = ? AND status = ?", 2, "active").Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount)
	var failedCount int64
	require.NoError(t, db.Table("orders").Where("user_id = ? AND status = ?", 2, "failed").Count(&failedCount).Error)
	require.EqualValues(t, 2, failedCount)
	var debitCount int64
	require.NoError(t, db.Table("wallet_transactions").Where("user_id = ? AND transaction_type = ?", 2, "debit").Count(&debitCount).Error)
	require.EqualValues(t, 1, debitCount)
	var allocationCount int64
	require.NoError(t, db.Table("microsoft_allocations").Where("status = ?", "allocated").Count(&allocationCount).Error)
	require.EqualValues(t, 1, allocationCount)
}

func TestOrderRouteConcurrentBatchesThrottleSameUserWithoutDeadlockMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Update("code_price", "0.000000").Error)
	seedTradeMicrosoftResources(t, db, 1, 1000, 100, true)

	openapiMod := openapiapi.NewModule(db)
	rateLimit := 1000
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:             2,
		Name:               "concurrent-batch-sdk",
		RateLimitPerMinute: &rateLimit,
		ConcurrencyLimit:   intPointer(10),
		IdempotencyKey:     "apikey-idem-concurrent-batch",
		RequestID:          "req-apikey-concurrent-batch",
	})
	require.NoError(t, err)

	router := gin.New()
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)
	body, err := json.Marshal(CreateOrderBatchRequest{
		CreateOrderRequest: CreateOrderRequest{ProjectID: 10, ProductID: 20},
		Quantity:           25,
	})
	require.NoError(t, err)

	const batches = 4
	results := make(chan *httptest.ResponseRecorder, batches)
	var wg sync.WaitGroup
	for i := 0; i < batches; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/open/orders/batch?serviceMode=code&supply=private_first", bytes.NewReader(body))
			req.Header.Set("X-API-Key", key.KeyPlain)
			req.Header.Set("Idempotency-Key", fmt.Sprintf("route-order-concurrent-batch-%d", i))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			results <- rec
		}(i)
	}
	wg.Wait()
	close(results)
	createdBatches := 0
	throttledBatches := 0
	for result := range results {
		switch result.Code {
		case http.StatusCreated:
			createdBatches++
		case http.StatusTooManyRequests:
			throttledBatches++
		default:
			require.Failf(t, "unexpected batch response", "status=%d body=%s", result.Code, result.Body.String())
		}
	}
	require.Positive(t, createdBatches)
	require.Positive(t, throttledBatches)

	var orderCount int64
	require.NoError(t, db.Table("orders").Where("user_id = ?", 2).Count(&orderCount).Error)
	require.EqualValues(t, createdBatches*25, orderCount)
}

func TestDeletedAPIKeyIsHiddenAndCannotAuthenticateButKeepsOrderFactsMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")

	openapiMod := openapiapi.NewModule(db)
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:           2,
		Name:             "delete-keeps-facts",
		ConcurrencyLimit: intPointer(5),
		IdempotencyKey:   "apikey-idem-delete-keeps-facts",
		RequestID:        "req-apikey-delete-keeps-facts",
	})
	require.NoError(t, err)

	router := gin.New()
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)

	body, err := json.Marshal(CreateOrderRequest{ProjectID: 10, ProductID: 20})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/open/orders?serviceMode=code&supply=public_only", bytes.NewReader(body))
	req.Header.Set("X-API-Key", key.KeyPlain)
	req.Header.Set("Idempotency-Key", "route-order-idem-delete-keeps-facts")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	require.NoError(t, openapiMod.UseCase.DeleteAPIKey(context.Background(), 2, key.ID))

	keys, total, err := openapiMod.UseCase.ListAPIKeys(context.Background(), 2, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 0, total)
	require.Empty(t, keys)

	_, err = openapiMod.UseCase.GetAPIKey(context.Background(), 2, key.ID)
	require.ErrorIs(t, err, openapidomain.ErrAPIKeyNotFound)

	_, err = openapiMod.UseCase.BeginAPIKeyRequest(context.Background(), key.KeyPlain)
	require.ErrorIs(t, err, openapidomain.ErrAPIKeyNotFound)

	var orderCount int64
	require.NoError(t, db.Table("orders").Where("api_key_id = ?", key.ID).Count(&orderCount).Error)
	require.EqualValues(t, 1, orderCount)

	var stored struct {
		DeletedAt *time.Time
		Enabled   bool
	}
	require.NoError(t, db.Table("api_keys").Select("deleted_at, enabled").Where("id = ?", key.ID).Take(&stored).Error)
	require.NotNil(t, stored.DeletedAt)
	require.False(t, stored.Enabled)
}

func TestCheckoutEmailSuffixFiltersAllocationSourceMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResource(t, db, 1, 1000, "first@blocked.test", "blocked.test", 100, true)
	seedTradeMicrosoftResource(t, db, 1, 1001, "first@example.com", "example.com", 99, true)
	creditBuyer(t, db, 2, "10.00")

	uc := newTradeUseCase(db)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		EmailSuffix:    "@example.com",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-suffix",
		RequestID:      "req-trade-suffix",
	})
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, result.Order.Status)
	require.Equal(t, "first@example.com", result.Order.DeliveryEmail)
	require.NotNil(t, result.Order.MicrosoftAllocID)
}

func TestCheckoutEmailSuffixMismatchDoesNotDebitMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResource(t, db, 1, 1000, "first@example.com", "example.com", 100, true)
	creditBuyer(t, db, 2, "10.00")

	uc := newTradeUseCase(db)
	_, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{
		UserID:         2,
		ProjectID:      10,
		ProductID:      20,
		ServiceMode:    "code",
		SupplyPolicy:   "public_only",
		EmailSuffix:    "missing.test",
		ClientChannel:  tradedomain.ClientChannelConsole,
		IdempotencyKey: "order-idem-suffix-missing",
		RequestID:      "req-trade-suffix-missing",
	})
	require.ErrorIs(t, err, tradedomain.ErrInsufficientInventory)

	var order struct {
		OrderNo      string
		Status       string
		DebitTxID    *uint
		RefundTxID   *uint
		RefundAmount string
	}
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "order-idem-suffix-missing").Take(&order).Error)
	require.Equal(t, string(tradedomain.OrderStatusFailed), order.Status)
	require.Nil(t, order.DebitTxID)
	require.Nil(t, order.RefundTxID)
	require.Equal(t, "0.000000", order.RefundAmount)
	var txCount int64
	require.NoError(t, db.Table("wallet_transactions").
		Where("biz_id = ?", "order:"+order.OrderNo).
		Count(&txCount).Error)
	require.EqualValues(t, 0, txCount)
}

func TestFailedOrderDetailDoesNotExposeServiceTokenMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, failure_code, pay_amount, refund_amount, delivery_email,
    client_channel, idempotency_key, request_fingerprint, service_cleanup_status
) VALUES (
    'OR_FAILED_TOKEN', 2, 10, 20, 'microsoft', 'code',
    'public_only', 'failed', 'unknown', 1.00, 0.00, '',
    'console', 'failed-token-idem', REPEAT('a', 64), 'none'
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO order_tokens(token_prefix, token_plain, order_no, enabled, expire_at, created_at, updated_at)
VALUES ('st_failed_tok', 'st_failed_token_plain', 'OR_FAILED_TOKEN', TRUE, ?, ?, ?)`,
		now.Add(time.Hour), now, now).Error)

	result, err := newTradeUseCase(db).GetOrder(context.Background(), "OR_FAILED_TOKEN", 2, false)
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusFailed, result.Order.Status)
	require.Empty(t, result.ServiceToken)
}

func TestAPIKeyRequestLimitsMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role) VALUES
    (2, 'buyer@test.local', 'hash', 'buyer', 'active', 'user')`).Error)

	openapiMod := openapiapi.NewModule(db)
	rateLimit := 1
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:             2,
		Name:               "limited",
		RateLimitPerMinute: &rateLimit,
		ConcurrencyLimit:   intPointer(1),
		IdempotencyKey:     "apikey-idem-limited",
		RequestID:          "req-apikey-limited",
	})
	require.NoError(t, err)

	first, err := openapiMod.UseCase.BeginAPIKeyRequest(context.Background(), key.KeyPlain)
	require.NoError(t, err)
	require.Equal(t, key.ID, first.APIKeyID)
	_, err = openapiMod.UseCase.BeginAPIKeyRequest(context.Background(), key.KeyPlain)
	require.ErrorIs(t, err, openapidomain.ErrAPIKeyConcurrencyLimit)
	require.NoError(t, openapiMod.UseCase.FinishAPIKeyRequest(context.Background(), first.APIKeyID))

	_, err = openapiMod.UseCase.BeginAPIKeyRequest(context.Background(), key.KeyPlain)
	require.ErrorIs(t, err, openapidomain.ErrAPIKeyRateLimited)

}

func TestAPIKeyDefaultConcurrencyIsNullMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role) VALUES
    (2, 'default-concurrency@test.local', 'hash', 'default-concurrency', 'active', 'user')`).Error)

	openapiMod := openapiapi.NewModule(db)
	defaultKey, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:         2,
		Name:           "default-concurrency",
		IdempotencyKey: "apikey-idem-default-concurrency",
	})
	require.NoError(t, err)
	require.Nil(t, defaultKey.ConcurrencyLimit)
	replayedDefaultKey, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:         2,
		Name:           "default-concurrency",
		IdempotencyKey: "apikey-idem-default-concurrency",
	})
	require.NoError(t, err)
	require.Equal(t, defaultKey.ID, replayedDefaultKey.ID)
	var storedDefault struct{ ConcurrencyLimit *int }
	require.NoError(t, db.Table("api_keys").Select("concurrency_limit").Where("id = ?", defaultKey.ID).Take(&storedDefault).Error)
	require.Nil(t, storedDefault.ConcurrencyLimit)
}

func TestAPIKeyQuotaAndNullableLimitsMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role) VALUES
    (2, 'quota-user@test.local', 'hash', 'quota-user', 'active', 'user')`).Error)

	openapiMod := openapiapi.NewModule(db)
	rateLimit := 10
	quotaLimit := int64(2)
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:             2,
		Name:               "quota-limited",
		RateLimitPerMinute: &rateLimit,
		ConcurrencyLimit:   intPointer(5),
		QuotaLimit:         &quotaLimit,
		IdempotencyKey:     "apikey-idem-quota-limited",
		RequestID:          "req-apikey-quota-limited",
	})
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		acquired, err := openapiMod.UseCase.BeginAPIKeyRequest(context.Background(), key.KeyPlain)
		require.NoError(t, err)
		require.NoError(t, openapiMod.UseCase.FinishAPIKeyRequest(context.Background(), acquired.APIKeyID))
	}
	_, err = openapiMod.UseCase.BeginAPIKeyRequest(context.Background(), key.KeyPlain)
	require.ErrorIs(t, err, openapidomain.ErrAPIKeyQuotaExceeded)

	updated, err := openapiMod.UseCase.UpdateAPIKey(context.Background(), openapiapp.UpdateAPIKeyRequest{
		UserID:             2,
		KeyID:              key.ID,
		RateLimitSet:       true,
		RateLimitPerMinute: nil,
		ConcurrencySet:     true,
		ConcurrencyLimit:   nil,
		QuotaSet:           true,
		QuotaLimit:         nil,
	})
	require.NoError(t, err)
	require.Nil(t, updated.RateLimitPerMinute)
	require.Nil(t, updated.ConcurrencyLimit)
	require.Nil(t, updated.QuotaLimit)

	var nullable struct {
		RateLimitPerMinute *int
		ConcurrencyLimit   *int
		QuotaLimit         *int64
	}
	require.NoError(t, db.Table("api_keys").
		Select("rate_limit_per_minute, concurrency_limit, quota_limit").
		Where("id = ?", key.ID).
		Take(&nullable).Error)
	require.Nil(t, nullable.RateLimitPerMinute)
	require.Nil(t, nullable.ConcurrencyLimit)
	require.Nil(t, nullable.QuotaLimit)

	acquired, err := openapiMod.UseCase.BeginAPIKeyRequest(context.Background(), key.KeyPlain)
	require.NoError(t, err)
	require.NoError(t, openapiMod.UseCase.FinishAPIKeyRequest(context.Background(), acquired.APIKeyID))

	oneShotQuota := int64(1)
	concurrentKey, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:           2,
		Name:             "quota-concurrent",
		ConcurrencyLimit: intPointer(20),
		QuotaLimit:       &oneShotQuota,
		IdempotencyKey:   "apikey-idem-quota-concurrent",
		RequestID:        "req-apikey-quota-concurrent",
	})
	require.NoError(t, err)

	const attempts = 8
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acquired, err := openapiMod.UseCase.BeginAPIKeyRequest(context.Background(), concurrentKey.KeyPlain)
			if err == nil {
				_ = openapiMod.UseCase.FinishAPIKeyRequest(context.Background(), acquired.APIKeyID)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	successes := 0
	quotaExceeded := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, err, openapidomain.ErrAPIKeyQuotaExceeded)
		quotaExceeded++
	}
	require.Equal(t, 1, successes)
	require.Equal(t, attempts-1, quotaExceeded)

	require.NoError(t, openapiMod.UseCase.FlushRuntime(context.Background()))
	var quotaUsed int64
	require.NoError(t, db.Table("api_keys").Select("quota_used").Where("id = ?", concurrentKey.ID).Scan(&quotaUsed).Error)
	require.EqualValues(t, 1, quotaUsed)

	usage, err := openapiMod.UseCase.GetAPIKeyUsage(context.Background(), 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, usage.KeyCount)
	require.EqualValues(t, 4, usage.RequestCount)
}

func TestInactiveAPIKeyOwnerCannotOrderMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")

	openapiMod := openapiapi.NewModule(db)
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:         2,
		Name:           "disabled-owner",
		IdempotencyKey: "apikey-idem-disabled-owner",
		RequestID:      "req-apikey-disabled-owner",
	})
	require.NoError(t, err)
	require.NoError(t, db.Table("users").Where("id = ?", 2).Update("status", "disabled").Error)

	router := gin.New()
	router.Use(middleware.RequestID())
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)

	body, err := json.Marshal(CreateOrderRequest{ProjectID: 10, ProductID: 20})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/open/orders?serviceMode=code&supply=public_only", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key.KeyPlain)
	req.Header.Set("Idempotency-Key", "route-order-disabled-owner")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	var orderCount int64
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "route-order-disabled-owner").Count(&orderCount).Error)
	require.EqualValues(t, 0, orderCount)

	require.NoError(t, db.Table("users").Where("id = ?", 2).Update("status", "deleted").Error)
	req = httptest.NewRequest(http.MethodPost, "/v1/open/orders?serviceMode=code&supply=public_only", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key.KeyPlain)
	req.Header.Set("Idempotency-Key", "route-order-deleted-owner")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "route-order-deleted-owner").Count(&orderCount).Error)
	require.EqualValues(t, 0, orderCount)
}

func TestOrderAPIKeyOwnerConstraintMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")

	var apiKeyID uint
	require.NoError(t, db.Exec(`
INSERT INTO api_keys(user_id, name, key_prefix, key_plain, rate_limit_per_minute, concurrency_limit)
VALUES (3, 'other-owner', 'rk-other-owner', 'rk-other-owner-plain', 60, 5)`).Error)
	require.NoError(t, db.Table("api_keys").Select("id").Where("key_plain = ?", "rk-other-owner-plain").Scan(&apiKeyID).Error)
	require.NotZero(t, apiKeyID)

	err := db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, pay_amount, refund_amount, delivery_email,
    client_channel, api_key_id, idempotency_key, request_fingerprint, service_cleanup_status
) VALUES (
    'OR_APIKEY_OWNER_MISMATCH', 2, 10, 20, 'microsoft', 'code',
    'public_only', 'pending_payment', 1.00, 0.00, '',
    'api_key', ?, 'owner-mismatch-idem', REPEAT('b', 64), 'none'
)`, apiKeyID).Error
	require.Error(t, err)

	var orderCount int64
	require.NoError(t, db.Table("orders").Where("order_no = ?", "OR_APIKEY_OWNER_MISMATCH").Count(&orderCount).Error)
	require.EqualValues(t, 0, orderCount)
}

func TestConcurrentAPIKeyOrderReplayDoesNotDuplicateFactsMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")

	openapiMod := openapiapi.NewModule(db)
	rateLimit := 1000
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:             2,
		Name:               "sdk-concurrent",
		RateLimitPerMinute: &rateLimit,
		ConcurrencyLimit:   intPointer(50),
		IdempotencyKey:     "apikey-idem-concurrent",
		RequestID:          "req-apikey-concurrent",
	})
	require.NoError(t, err)

	router := gin.New()
	registerOpenOrderRoute(router, newTradeModule(db), openapiMod)

	const requests = 8
	body, err := json.Marshal(CreateOrderRequest{ProjectID: 10, ProductID: 20})
	require.NoError(t, err)
	results := make(chan int, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/open/orders?serviceMode=code&supply=public_only", bytes.NewReader(body))
			req.Header.Set("X-API-Key", key.KeyPlain)
			req.Header.Set("Idempotency-Key", "route-order-idem-concurrent")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			results <- rec.Code
		}()
	}
	wg.Wait()
	close(results)
	for code := range results {
		require.Contains(t, []int{http.StatusCreated, http.StatusOK}, code)
	}

	var orderRow struct {
		OrderNo string
	}
	require.NoError(t, db.Table("orders").Select("order_no").Where("idempotency_key = ?", "route-order-idem-concurrent").Take(&orderRow).Error)
	var orderCount int64
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "route-order-idem-concurrent").Count(&orderCount).Error)
	require.EqualValues(t, 1, orderCount)
	var debitCount int64
	require.NoError(t, db.Table("wallet_transactions").
		Where("user_id = ? AND transaction_type = ? AND biz_id = ?", 2, "debit", "order:"+orderRow.OrderNo).
		Count(&debitCount).Error)
	require.EqualValues(t, 1, debitCount)
	var allocationCount int64
	require.NoError(t, db.Table("microsoft_allocations").Where("order_no = ?", orderRow.OrderNo).Count(&allocationCount).Error)
	require.EqualValues(t, 1, allocationCount)
}

func TestConcurrentCheckoutReplayAndRefundPathsDoNotDuplicateFactsMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 2, true)
	creditBuyer(t, db, 2, "20.00")

	uc := newTradeUseCase(db)
	requests := []tradeapp.CheckoutRequest{
		{
			UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "code", SupplyPolicy: "public_only",
			ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "order-idem-concurrent-refund",
			RequestID: "req-concurrent-refund",
		},
		{
			UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "code", SupplyPolicy: "public_only",
			ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "order-idem-concurrent-retry-refund",
			RequestID: "req-concurrent-retry-refund",
		},
	}
	created := make([]*tradeapp.CheckoutResult, len(requests))
	for i := range requests {
		var err error
		created[i], err = uc.Checkout(context.Background(), requests[i])
		require.NoError(t, err)
		require.Equal(t, tradedomain.OrderStatusActive, created[i].Order.Status)
	}
	require.NoError(t, db.Table("orders").
		Where("order_no = ?", created[1].Order.OrderNo).
		Updates(map[string]any{
			"status":                 string(tradedomain.OrderStatusFailed),
			"failure_code":           string(tradedomain.OrderFailureServiceToken),
			"service_cleanup_status": "partial_failure",
		}).Error)

	factsBefore := tradeCheckoutFactCounts(t, db)
	deadlocksBefore := tradeInnoDBMetricCount(t, db, "lock_deadlocks")
	timeoutsBefore := tradeInnoDBMetricCount(t, db, "lock_timeouts")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := make(chan struct{})
	activeReplayErr := make(chan error, 1)
	failedReplayErr := make(chan error, 1)
	refundErrs := make(chan error, 2)
	retryRefundErrs := make(chan error, 2)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := uc.Checkout(ctx, requests[0])
		activeReplayErr <- err
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := uc.Checkout(ctx, requests[1])
		failedReplayErr <- err
	}()
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := uc.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{
				OrderNo: created[0].Order.OrderNo, Reason: "Concurrent refund gate.",
				IdempotencyKey: "concurrent-refund-idem", RequestID: "req-concurrent-refund-gate",
			})
			refundErrs <- err
		}()
	}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := uc.AdminRetryOrderRefund(ctx, tradeapp.AdminOrderCommandRequest{
				OrderNo: created[1].Order.OrderNo, Reason: "Concurrent retry refund gate.",
				IdempotencyKey: "concurrent-retry-refund-idem", RequestID: "req-concurrent-retry-refund-gate",
			})
			retryRefundErrs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(refundErrs)
	close(retryRefundErrs)

	require.NoError(t, <-activeReplayErr)
	require.ErrorIs(t, <-failedReplayErr, tradedomain.ErrInvalidOrderRequest)
	for err := range refundErrs {
		require.NoError(t, err)
	}
	retrySucceeded, retryConflicted := 0, 0
	for err := range retryRefundErrs {
		switch {
		case err == nil:
			retrySucceeded++
		case errors.Is(err, tradedomain.ErrOrderStateConflict):
			retryConflicted++
		default:
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, retrySucceeded)
	require.Equal(t, 1, retryConflicted)

	factsAfter := tradeCheckoutFactCounts(t, db)
	require.Equal(t, factsBefore["orders"], factsAfter["orders"])
	require.Equal(t, factsBefore["microsoft_allocations"], factsAfter["microsoft_allocations"])
	require.Equal(t, factsBefore["order_tokens"], factsAfter["order_tokens"])
	require.Equal(t, factsBefore["wallet_transactions"]+2, factsAfter["wallet_transactions"])
	require.Equal(t, factsBefore["order_events"]+2, factsAfter["order_events"])

	for i, order := range created {
		var stored struct {
			Status     string
			DebitTxID  *uint
			RefundTxID *uint
		}
		require.NoError(t, db.Table("orders").
			Select("status, debit_tx_id, refund_tx_id").
			Where("order_no = ?", order.Order.OrderNo).
			Take(&stored).Error)
		require.NotNil(t, stored.DebitTxID)
		require.NotNil(t, stored.RefundTxID)
		if i == 0 {
			require.Equal(t, string(tradedomain.OrderStatusRefunded), stored.Status)
		} else {
			require.Equal(t, string(tradedomain.OrderStatusFailed), stored.Status)
		}

		for _, transactionType := range []string{"debit", "refund"} {
			var count int64
			require.NoError(t, db.Table("wallet_transactions").
				Where("user_id = ? AND transaction_type = ? AND biz_id = ?", 2, transactionType, "order:"+order.Order.OrderNo).
				Count(&count).Error)
			require.EqualValues(t, 1, count)
		}
		var allocationCount, enabledTokenCount, tokenCount int64
		require.NoError(t, db.Table("microsoft_allocations").
			Where("order_no = ?", order.Order.OrderNo).
			Count(&allocationCount).Error)
		require.EqualValues(t, 1, allocationCount)
		require.NoError(t, db.Table("microsoft_allocations").
			Where("order_no = ? AND status = ?", order.Order.OrderNo, "released").
			Count(&allocationCount).Error)
		require.EqualValues(t, 1, allocationCount)
		require.NoError(t, db.Table("order_tokens").Where("order_no = ?", order.Order.OrderNo).Count(&tokenCount).Error)
		require.EqualValues(t, 1, tokenCount)
		require.NoError(t, db.Table("order_tokens").
			Where("order_no = ? AND enabled = ?", order.Order.OrderNo, true).
			Count(&enabledTokenCount).Error)
		require.Zero(t, enabledTokenCount)
	}
	for _, event := range []struct {
		orderNo  string
		typeName string
	}{
		{orderNo: created[0].Order.OrderNo, typeName: "order.refunded"},
		{orderNo: created[1].Order.OrderNo, typeName: "order.refund_retried"},
	} {
		var count int64
		require.NoError(t, db.Table("order_events").
			Where("order_no = ? AND event_type = ?", event.orderNo, event.typeName).
			Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
	require.Equal(t, deadlocksBefore, tradeInnoDBMetricCount(t, db, "lock_deadlocks"))
	require.Equal(t, timeoutsBefore, tradeInnoDBMetricCount(t, db, "lock_timeouts"))
}

func newTradeUseCase(db *gorm.DB) *tradeapp.UseCase {
	return newTradeModule(db).UseCase
}

func registerOpenOrderRoute(router *gin.Engine, mod *Module, openapiMod *openapiapi.Module) {
	open := router.Group("/v1/open")
	open.Use(openapiapi.LoadAPIKey(openapiMod.UseCase))
	open.Use(openapiapi.KeyRequired())
	h := NewHandler(mod)
	open.POST("/orders", h.PostOrder)
	open.POST("/orders/batch", h.PostOrderBatch)
	open.GET("/orders", h.GetOrders)
	open.GET("/orders/:orderNo", h.GetOrder)
}

type fixedTradeSessionFetcher struct{}

func (fixedTradeSessionFetcher) FetchSession(context.Context, string) (uint, iamdomain.Role, string, bool) {
	return 1, iamdomain.RoleAdmin, "admin@test.local", true
}

type allowTradePermissionChecker struct{}

func (allowTradePermissionChecker) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return true, nil
}

func newTradeModule(db *gorm.DB) *Module {
	allocation := allocapp.NewUseCase(allocinfra.NewRepo(db))
	allocation.SetHistoricalMicrosoftAliasPort(mailinfra.NewMicrosoftAliasStore(db))
	return NewModule(
		db,
		coreapp.NewProjectUseCase(coreinfra.NewProjectRepo(db)),
		billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db)),
		allocation,
		openapiapp.NewUseCase(openapiinfra.NewRepo(db)),
	)
}

func creditBuyer(t *testing.T, db *gorm.DB, userID uint, amount string) {
	t.Helper()
	_, err := billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db)).CreditConsumer(context.Background(), billingapp.AdjustConsumerBalanceRequest{
		UserID:         userID,
		Amount:         amount,
		Reason:         fmt.Sprintf("seed-wallet-%d", userID),
		IdempotencyKey: fmt.Sprintf("seed-wallet-%d", userID),
		RequestID:      "req-seed-wallet",
	})
	require.NoError(t, err)
}

func intPointer(value int) *int { return &value }

func seedTradeBase(t *testing.T, db *gorm.DB, productType string) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role) VALUES
    (1, 'super-admin@test.local', 'hash', 'super-admin', 'active', 'super_admin'),
    (2, 'buyer@test.local', 'hash', 'buyer', 'active', 'user'),
    (3, 'regular@test.local', 'hash', 'regular', 'active', 'user')`).Error)
	seedTradeBaseFacts(t, db, productType)
}

func seedTradeBaseLegacyEnabled(t *testing.T, db *gorm.DB, productType string) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, enabled, role) VALUES
    (1, 'super-admin@test.local', 'hash', 'super-admin', TRUE, 'super_admin'),
    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 'user'),
    (3, 'regular@test.local', 'hash', 'regular', TRUE, 'user')`).Error)
	seedTradeBaseFacts(t, db, productType)
}

func seedTradeBaseFacts(t *testing.T, db *gorm.DB, productType string) {
	t.Helper()
	mainWeight := 0
	dotWeight := 0
	plusWeight := 0
	if productType == "microsoft" {
		mainWeight = 1
	}
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, logo_url, status, access_type, loose_match)
VALUES (10, 'Trade Project', 'trade', '/v1/projects/logos/trade-project', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (20, 10, ?, 'enabled', TRUE, TRUE, 1.00, 2.00, 0.50, 1.00, 10, 60, 1440, ?, ?, ?)`,
		productType,
		mainWeight,
		dotWeight,
		plusWeight,
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES
    (10, 'sender', '.*', TRUE),
    (10, 'recipient', 'exact', TRUE)`).Error)
}

func seedTradeMicrosoftResources(t *testing.T, db *gorm.DB, ownerID, startID, count int, forSale bool) {
	t.Helper()
	for i := 0; i < count; i++ {
		id := startID + i
		email := fmt.Sprintf("ms%d@example.com", id)
		seedTradeMicrosoftResource(t, db, ownerID, id, email, "example.com", 100-i, forSale)
	}
}

func seedTradeMicrosoftResource(t *testing.T, db *gorm.DB, ownerID int, id int, email string, domain string, quality int, forSale bool) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'microsoft', ?)",
		id,
		ownerID,
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, email_address, email_domain, password, for_sale, status, quality_score, alloc_bucket)
VALUES (?, ?, ?, 'secret', ?, 'normal', ?, MOD(?, 64))`,
		id,
		email,
		domain,
		forSale,
		quality,
		id,
	).Error)
}

func seedTradeDomainResources(t *testing.T, db *gorm.DB, ownerID, startID, count int, purpose string) {
	t.Helper()
	mailServerID := 900 + ownerID
	require.NoError(t, db.Exec(`
INSERT INTO mail_servers(id, owner_user_id, name, server_address, mx_record, status)
VALUES (?, ?, 'default', 'mx.aishop6.com', 'mx.aishop6.com', 'online')
ON DUPLICATE KEY UPDATE status = VALUES(status)`, mailServerID, ownerID).Error)
	for i := 0; i < count; i++ {
		id := startID + i
		domainName := fmt.Sprintf("trade%d.example.com", id)
		require.NoError(t, db.Exec(
			"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'domain', ?)",
			id,
			ownerID,
		).Error)
		require.NoError(t, db.Exec(`
INSERT INTO domain_resources(id, resource_type, owner_user_id, domain, domain_tld, mail_server_id, purpose, status, alloc_bucket)
VALUES (?, 'domain', ?, ?, 'example.com', ?, ?, 'normal', MOD(?, 64))`,
			id,
			ownerID,
			domainName,
			mailServerID,
			purpose,
			id,
		).Error)
	}
}
