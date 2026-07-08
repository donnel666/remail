package api

import (
	"bytes"
	"context"
	"encoding/json"
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
	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapidomain "github.com/donnel666/remail/internal/openapi/domain"
	openapiinfra "github.com/donnel666/remail/internal/openapi/infra"
	"github.com/donnel666/remail/internal/platform/testmysql"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/gin-gonic/gin"
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
	require.NotNil(t, first.Order.AfterSaleUntil)
	require.InDelta(t, int64((60 * time.Minute).Seconds()), int64(first.Order.ReceiveUntil.Sub(*first.Order.ReceiveStartedAt).Seconds()), 1)
	require.InDelta(t, int64((1440 * time.Minute).Seconds()), int64(first.Order.AfterSaleUntil.Sub(*first.Order.ReceiveUntil).Seconds()), 1)

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
	var allocationCount int64
	require.NoError(t, db.Table("microsoft_allocations").Where("order_no = ?", first.Order.OrderNo).Count(&allocationCount).Error)
	require.EqualValues(t, 1, allocationCount)
	var purchaseToken struct {
		ExpireAt *time.Time
	}
	require.NoError(t, db.Table("order_tokens").Select("expire_at").Where("order_no = ?", first.Order.OrderNo).Take(&purchaseToken).Error)
	require.Nil(t, purchaseToken.ExpireAt)
}

func TestCheckoutAllocationFailureRefundsDebitMySQL(t *testing.T) {
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

	var order struct {
		OrderNo      string
		Status       string
		DebitTxID    *uint
		RefundTxID   *uint
		RefundAmount string
	}
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "order-idem-refund").Take(&order).Error)
	require.Equal(t, string(tradedomain.OrderStatusFailed), order.Status)
	require.NotNil(t, order.DebitTxID)
	require.NotNil(t, order.RefundTxID)
	require.Equal(t, "1.00", order.RefundAmount)
	var guardCount int64
	require.NoError(t, db.Table("allocation_order_guards").Where("order_no = ?", order.OrderNo).Count(&guardCount).Error)
	require.EqualValues(t, 0, guardCount)

	summary, err := billinginfra.NewBillingRepo(db).GetOrCreateWalletSummary(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, "10.00", summary.Wallet.ConsumerBalance)
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
	require.True(t, strings.HasPrefix(key.KeyPlain, "ak_"))

	router := gin.New()
	router.Use(middleware.RequestID())
	v1 := router.Group("/v1")
	RegisterRoutes(v1, newTradeModule(db), middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.RoleLevel, string, bool) {
		return 0, 0, "", false
	}), openapiMod)

	body, err := json.Marshal(CreateOrderRequest{ProjectID: 10, ProductID: 20})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/orders?serviceMode=code&supply=public_only", bytes.NewReader(body))
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

	listReq := httptest.NewRequest(http.MethodGet, "/v1/orders?scope=mine", nil)
	listReq.Header.Set("Authorization", "Bearer "+key.KeyPlain)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listResp OrderListResponse
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listResp))
	require.Len(t, listResp.Items, 1)
	require.Empty(t, listResp.Items[0].ServiceToken)

	eventsReq := httptest.NewRequest(http.MethodGet, "/v1/orders/"+resp.OrderNo+"/events", nil)
	eventsReq.Header.Set("Authorization", "Bearer "+key.KeyPlain)
	eventsRec := httptest.NewRecorder()
	router.ServeHTTP(eventsRec, eventsReq)
	require.Equal(t, http.StatusForbidden, eventsRec.Code, eventsRec.Body.String())

	archiveReq := httptest.NewRequest(http.MethodPost, "/v1/orders/"+resp.OrderNo+"/archive", nil)
	archiveReq.Header.Set("Authorization", "Bearer "+key.KeyPlain)
	archiveRec := httptest.NewRecorder()
	router.ServeHTTP(archiveRec, archiveReq)
	require.Equal(t, http.StatusForbidden, archiveRec.Code, archiveRec.Body.String())

	var apiLogs []struct {
		Path           string
		Method         string
		IdempotencyKey string
		HTTPStatus     int
		RequestID      string
	}
	require.NoError(t, db.Table("api_logs").
		Select("path, method, idempotency_key, http_status, request_id").
		Where("principal_type = ? AND principal_id = ? AND user_id = ?", "api_key", key.ID, 2).
		Order("id ASC").
		Scan(&apiLogs).Error)
	require.Len(t, apiLogs, 4)
	require.Equal(t, "/v1/orders", apiLogs[0].Path)
	require.Equal(t, http.MethodPost, apiLogs[0].Method)
	require.Equal(t, "route-order-idem", apiLogs[0].IdempotencyKey)
	require.Equal(t, http.StatusCreated, apiLogs[0].HTTPStatus)
	require.NotEmpty(t, apiLogs[0].RequestID)
	require.Equal(t, "/v1/orders", apiLogs[1].Path)
	require.Equal(t, http.MethodGet, apiLogs[1].Method)
	require.Equal(t, http.StatusOK, apiLogs[1].HTTPStatus)
	require.Equal(t, "/v1/orders/:orderNo/events", apiLogs[2].Path)
	require.Equal(t, http.StatusForbidden, apiLogs[2].HTTPStatus)
	require.Equal(t, "/v1/orders/:orderNo/archive", apiLogs[3].Path)
	require.Equal(t, http.StatusForbidden, apiLogs[3].HTTPStatus)
}

func TestCheckoutEmailSuffixFiltersAllocationSourceMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResource(t, db, 1, 1000, "first@blocked.test", "blocked.test", 999, true)
	seedTradeMicrosoftResource(t, db, 1, 1001, "first@example.com", "example.com", 100, true)
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

func TestCheckoutEmailSuffixMismatchRefundsDebitMySQL(t *testing.T) {
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
		Status       string
		DebitTxID    *uint
		RefundTxID   *uint
		RefundAmount string
	}
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "order-idem-suffix-missing").Take(&order).Error)
	require.Equal(t, string(tradedomain.OrderStatusFailed), order.Status)
	require.NotNil(t, order.DebitTxID)
	require.NotNil(t, order.RefundTxID)
	require.Equal(t, "1.00", order.RefundAmount)
}

func TestFailedOrderDetailDoesNotExposeServiceTokenMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, pay_amount, refund_amount, delivery_email,
    client_channel, idempotency_key, request_fingerprint, service_cleanup_status
) VALUES (
    'OR_FAILED_TOKEN', 2, 10, 20, 'microsoft', 'code',
    'public_only', 'failed', 1.00, 0.00, '',
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
INSERT INTO users(id, email, password_hash, nickname, enabled, role_level) VALUES
    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 10)`).Error)

	openapiMod := openapiapi.NewModule(db)
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:             2,
		Name:               "limited",
		RateLimitPerMinute: 1,
		ConcurrencyLimit:   1,
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

	var activeRequests int
	require.NoError(t, db.Table("api_keys").Select("active_requests").Where("id = ?", key.ID).Scan(&activeRequests).Error)
	require.Equal(t, 0, activeRequests)
}

func TestDisabledAPIKeyOwnerCannotOrderMySQL(t *testing.T) {
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
	require.NoError(t, db.Table("users").Where("id = ?", 2).Update("enabled", false).Error)

	router := gin.New()
	router.Use(middleware.RequestID())
	v1 := router.Group("/v1")
	RegisterRoutes(v1, newTradeModule(db), middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.RoleLevel, string, bool) {
		return 0, 0, "", false
	}), openapiMod)

	body, err := json.Marshal(CreateOrderRequest{ProjectID: 10, ProductID: 20})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/orders?serviceMode=code&supply=public_only", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key.KeyPlain)
	req.Header.Set("Idempotency-Key", "route-order-disabled-owner")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	var orderCount int64
	require.NoError(t, db.Table("orders").Where("idempotency_key = ?", "route-order-disabled-owner").Count(&orderCount).Error)
	require.EqualValues(t, 0, orderCount)
	var activeRequests int
	require.NoError(t, db.Table("api_keys").Select("active_requests").Where("id = ?", key.ID).Scan(&activeRequests).Error)
	require.Equal(t, 0, activeRequests)
}

func TestOrderAPIKeyOwnerConstraintMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")

	var apiKeyID uint
	require.NoError(t, db.Exec(`
INSERT INTO api_keys(user_id, name, key_prefix, key_plain, rate_limit_per_minute, concurrency_limit)
VALUES (3, 'other-owner', 'ak_other_owner', 'ak_other_owner_plain', 60, 5)`).Error)
	require.NoError(t, db.Table("api_keys").Select("id").Where("key_plain = ?", "ak_other_owner_plain").Scan(&apiKeyID).Error)
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
	key, err := openapiMod.UseCase.CreateAPIKey(context.Background(), openapiapp.CreateAPIKeyRequest{
		UserID:             2,
		Name:               "sdk-concurrent",
		RateLimitPerMinute: 1000,
		ConcurrencyLimit:   50,
		IdempotencyKey:     "apikey-idem-concurrent",
		RequestID:          "req-apikey-concurrent",
	})
	require.NoError(t, err)

	router := gin.New()
	v1 := router.Group("/v1")
	RegisterRoutes(v1, newTradeModule(db), middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.RoleLevel, string, bool) {
		return 0, 0, "", false
	}), openapiMod)

	const requests = 8
	body, err := json.Marshal(CreateOrderRequest{ProjectID: 10, ProductID: 20})
	require.NoError(t, err)
	results := make(chan int, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/orders?serviceMode=code&supply=public_only", bytes.NewReader(body))
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
	var activeRequests int
	require.NoError(t, db.Table("api_keys").Select("active_requests").Where("id = ?", key.ID).Scan(&activeRequests).Error)
	require.Equal(t, 0, activeRequests)
}

func newTradeUseCase(db *gorm.DB) *tradeapp.UseCase {
	return newTradeModule(db).UseCase
}

func newTradeModule(db *gorm.DB) *Module {
	return NewModule(
		db,
		coreapp.NewProjectUseCase(coreinfra.NewProjectRepo(db)),
		billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db)),
		allocapp.NewUseCase(allocinfra.NewRepo(db)),
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

func seedTradeBase(t *testing.T, db *gorm.DB, productType string) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, enabled, role_level) VALUES
    (1, 'supplier@test.local', 'hash', 'supplier', TRUE, 20),
    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 10),
    (3, 'regular@test.local', 'hash', 'regular', TRUE, 10)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type, loose_match)
VALUES (10, 'Trade Project', 'trade', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (20, 10, ?, 'enabled', TRUE, TRUE, 1.00, 2.00, 0.50, 1.00, 10, 60, 1440, 1, 0, 0)`, productType).Error)
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
