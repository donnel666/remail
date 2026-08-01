package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/gin-gonic/gin"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var (
	billingAPIMySQLTestServer       = testmysql.New("remail_billing_api_test")
	billingAPILegacyMySQLTestServer = testmysql.New("remail_billing_api_legacy_test")
)

func TestMain(m *testing.M) {
	code := m.Run()
	_ = billingAPIMySQLTestServer.Close(context.Background())
	_ = billingAPILegacyMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newBillingAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return billingAPIMySQLTestServer.Database(t, billingAPIMigrationsDir(t))
}

func newBillingAPILegacyMigrationTestDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	migrationsDir := testmysql.MigrationsThrough(t, billingAPIMigrationsDir(t), 65)
	return billingAPILegacyMySQLTestServer.Database(t, migrationsDir), migrationsDir
}

func billingAPIMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestBillingRoutesAuthAndScope(t *testing.T) {
	db := newBillingAPITestDB(t)
	userID := createBillingAPIUser(t, db, "wallet-user@example.com", iamdomain.RoleUser)
	router := newBillingAPIRouter(db, map[string]sessionFixture{
		"user-session": {userID: userID, role: iamdomain.RoleUser, email: "wallet-user@example.com"},
	}, false)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/wallet", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)

	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/wallet/transactions?scope=all", nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "user-session"})
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "Permission denied.")
}

func TestBillingWalletTransactionsFilterTypeBeforePagination(t *testing.T) {
	db := newBillingAPITestDB(t)
	userID := createBillingAPIUser(t, db, "wallet-transactions@example.com", iamdomain.RoleUser)
	records := []billinginfra.WalletTransactionModel{
		{
			TransactionNo: "TX-REDEEM-OLD", UserID: userID,
			TransactionType: string(billingdomain.TransactionTypeCardRedeem), BalanceBucket: "consumer", Direction: "in",
			Amount: "10.000000", BalanceBefore: "0.000000", BalanceAfter: "10.000000", BizType: "card_key", BizID: "CARD-OLD",
		},
		{
			TransactionNo: "TX-DEBIT", UserID: userID,
			TransactionType: string(billingdomain.TransactionTypeDebit), BalanceBucket: "consumer", Direction: "out",
			Amount: "-2.000000", BalanceBefore: "10.000000", BalanceAfter: "8.000000", BizType: "order", BizID: "ORDER-1",
		},
		{
			TransactionNo: "TX-REDEEM-NEW", UserID: userID,
			TransactionType: string(billingdomain.TransactionTypeCardRedeem), BalanceBucket: "consumer", Direction: "in",
			Amount: "25.000000", BalanceBefore: "8.000000", BalanceAfter: "33.000000", BizType: "card_key", BizID: "CARD-NEW",
		},
	}
	require.NoError(t, db.Create(&records).Error)
	router := newBillingAPIRouter(db, map[string]sessionFixture{
		"user-session": {userID: userID, role: iamdomain.RoleUser, email: "wallet-transactions@example.com"},
	}, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/wallet/transactions?type=card_redeem&limit=1", nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "user-session"})
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var firstPage TransactionListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &firstPage))
	require.Len(t, firstPage.Items, 1)
	require.Equal(t, "TX-REDEEM-NEW", firstPage.Items[0].TransactionNo)
	require.True(t, firstPage.HasNext)
	require.NotNil(t, firstPage.NextAfterID)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/wallet/transactions?type=card_redeem&limit=1&afterId="+strconv.FormatUint(uint64(*firstPage.NextAfterID), 10), nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "user-session"})
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var secondPage TransactionListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &secondPage))
	require.Len(t, secondPage.Items, 1)
	require.Equal(t, "TX-REDEEM-OLD", secondPage.Items[0].TransactionNo)
	require.False(t, secondPage.HasNext)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/wallet/transactions?type=unknown", nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "user-session"})
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

func TestBillingAdminMutationPermissionsMatchFrontendContract(t *testing.T) {
	checker := &recordingBillingPermissionChecker{}
	router := newBillingAPIRouterWithChecker(nil, map[string]sessionFixture{
		"admin-session": {userID: 1, role: iamdomain.RoleAdmin, email: "admin@example.com"},
	}, checker)

	w := httptest.NewRecorder()
	req := authenticatedJSONRequest(http.MethodPost, "/v1/admin/wallets/2/credit", "admin-session", `{}`)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "billing:wallet:operate", checker.lastPermission())

	w = httptest.NewRecorder()
	req = authenticatedJSONRequest(http.MethodPatch, "/v1/admin/cards/test-card", "admin-session", `{}`)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "billing:card:write", checker.lastPermission())
}

func TestBillingCardRedeemRequiresIdempotencyAndUsesSafeError(t *testing.T) {
	db := newBillingAPITestDB(t)
	userID := createBillingAPIUser(t, db, "redeem-user@example.com", iamdomain.RoleUser)
	router := newBillingAPIRouter(db, map[string]sessionFixture{
		"user-session": {userID: userID, role: iamdomain.RoleUser, email: "redeem-user@example.com"},
	}, false)

	w := httptest.NewRecorder()
	req := authenticatedJSONRequest(http.MethodPost, "/v1/cards/redeem", "user-session", `{"cardKey":"missing-card"}`)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "Idempotency-Key is required.")

	w = httptest.NewRecorder()
	req = authenticatedJSONRequest(http.MethodPost, "/v1/cards/redeem", "user-session", `{"cardKey":"missing-card"}`)
	req.Header.Set("Idempotency-Key", "idem-missing-card")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Contains(t, w.Body.String(), "Card key cannot be redeemed.")
	require.NotContains(t, w.Body.String(), "not found")
}

func TestBillingWalletReferralsRoute(t *testing.T) {
	db := newBillingAPITestDB(t)
	userID := createBillingAPIUser(t, db, "referral-route@example.com", iamdomain.RoleUser)
	router := newBillingAPIRouter(db, map[string]sessionFixture{
		"user-session": {userID: userID, role: iamdomain.RoleUser, email: "referral-route@example.com"},
	}, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/wallet/referrals", nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "user-session"})
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `{"inviteCount":0,"pendingRewards":"0.00","totalEarned":"0.00"}`, w.Body.String())

	w = httptest.NewRecorder()
	req = authenticatedJSONRequest(http.MethodPost, "/v1/wallet/referrals/transfer", "user-session", ``)
	req.Header.Set("Idempotency-Key", "idem-empty-referral-transfer")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "No referral rewards available.")
}

func TestBillingSupplierTransferIsAuthorizedAtomicAndIdempotent(t *testing.T) {
	db, migrationsDir := newBillingAPILegacyMigrationTestDB(t)
	supplierID := createBillingAPIUser(t, db, "supplier-transfer@example.com", iamdomain.RoleSupplier)
	userID := createBillingAPIUser(t, db, "user-transfer@example.com", iamdomain.RoleUser)
	adminID := createBillingAPIUser(t, db, "admin-transfer@example.com", iamdomain.RoleAdmin)
	repo := billinginfra.NewBillingRepo(db)
	_, err := repo.GetOrCreateWalletSummary(context.Background(), supplierID)
	require.NoError(t, err)
	require.NoError(t, db.Model(&billinginfra.WalletModel{}).Where("user_id = ?", supplierID).Updates(map[string]any{
		"consumer_balance":   "3.000000",
		"supplier_available": "12.000000",
	}).Error)
	router := newBillingAPIRouter(db, map[string]sessionFixture{
		"supplier-session": {userID: supplierID, role: iamdomain.RoleSupplier, email: "supplier-transfer@example.com"},
		"user-session":     {userID: userID, role: iamdomain.RoleUser, email: "user-transfer@example.com"},
		"admin-session":    {userID: adminID, role: iamdomain.RoleAdmin, email: "admin-transfer@example.com"},
	}, true)

	w := httptest.NewRecorder()
	req := authenticatedJSONRequest(http.MethodPost, "/v1/wallet/supplier-transfers", "user-session", `{"amount":"1.00"}`)
	req.Header.Set("Idempotency-Key", "user-cannot-transfer")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	transfer := func(amount, key string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		request := authenticatedJSONRequest(http.MethodPost, "/v1/wallet/supplier-transfers", "supplier-session", `{"amount":"`+amount+`"}`)
		request.Header.Set("Idempotency-Key", key)
		router.ServeHTTP(response, request)
		return response
	}

	w = transfer("1.00", strings.Repeat("x", 129))
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "Invalid Idempotency-Key.")

	w = transfer("4.25", "supplier-transfer-fractional")
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "Invalid amount.")

	w = transfer("4", "supplier-transfer-1")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	firstResponse := w.Body.String()
	var wallet WalletResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wallet))
	require.Equal(t, "7.00", wallet.ConsumerBalance)
	require.Equal(t, "8.00", wallet.SupplierAvailable)

	var transactions []billinginfra.WalletTransactionModel
	require.NoError(t, db.Where("user_id = ? AND biz_type = ?", supplierID, "supplier_transfer").Order("id").Find(&transactions).Error)
	require.Len(t, transactions, 2)
	require.Equal(t, "supplier_available", transactions[0].BalanceBucket)
	require.Equal(t, "out", transactions[0].Direction)
	require.Equal(t, "-4.000000", transactions[0].Amount)
	require.Equal(t, "consumer", transactions[1].BalanceBucket)
	require.Equal(t, "in", transactions[1].Direction)
	require.Equal(t, "4.000000", transactions[1].Amount)

	for _, transaction := range transactions {
		reverse := httptest.NewRecorder()
		request := authenticatedJSONRequest(http.MethodPost, "/v1/admin/transactions/"+strconv.FormatUint(uint64(transaction.ID), 10)+"/reverse", "admin-session", ``)
		request.Header.Set("Idempotency-Key", "reverse-transfer-"+strconv.FormatUint(uint64(transaction.ID), 10))
		router.ServeHTTP(reverse, request)
		require.Equal(t, http.StatusUnprocessableEntity, reverse.Code, reverse.Body.String())
		require.Contains(t, reverse.Body.String(), "Transaction cannot be reversed.")
	}

	w = transfer("1.00", "supplier-transfer-2")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	w = transfer("4", "supplier-transfer-1")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, firstResponse, w.Body.String(), "an idempotent replay must return the original response")
	transactions = nil
	require.NoError(t, db.Where("user_id = ? AND biz_type = ?", supplierID, "supplier_transfer").Find(&transactions).Error)
	require.Len(t, transactions, 4, "an idempotent replay must not write another ledger pair")

	w = transfer("8.00", "supplier-transfer-insufficient")
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "Insufficient balance.")
	var stored billinginfra.WalletModel
	require.NoError(t, db.First(&stored, "user_id = ?", supplierID).Error)
	require.Equal(t, "8.000000", stored.ConsumerBalance)
	require.Equal(t, "7.000000", stored.SupplierAvailable)
	require.Zero(t, stored.BalanceWarningLevel)
	require.Equal(t, uint64(2), stored.BalanceWarningCycle)

	require.NoError(t, db.Model(&billinginfra.WalletModel{}).Where("user_id = ?", supplierID).
		Update("consumer_balance", "999999999999.999999").Error)
	w = transfer("1", "supplier-transfer-overflow")
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.NoError(t, db.First(&stored, "user_id = ?", supplierID).Error)
	require.Equal(t, "999999999999.999999", stored.ConsumerBalance)
	require.Equal(t, "7.000000", stored.SupplierAvailable, "the supplier debit must roll back if the consumer credit fails")
	var transactionCount int64
	require.NoError(t, db.Model(&billinginfra.WalletTransactionModel{}).
		Where("user_id = ? AND biz_type = ?", supplierID, "supplier_transfer").Count(&transactionCount).Error)
	require.EqualValues(t, 4, transactionCount)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.DownTo(sqlDB, migrationsDir, 62), "immutable outbound transfer rows must not block rollback")
	require.NoError(t, goose.UpTo(sqlDB, migrationsDir, 63))
}

func TestBillingAdminWalletCreditWritesOperationLog(t *testing.T) {
	db := newBillingAPITestDB(t)
	targetUserID := createBillingAPIUser(t, db, "target@example.com", iamdomain.RoleUser)
	adminUserID := createBillingAPIUser(t, db, "admin@example.com", iamdomain.RoleAdmin)
	router := newBillingAPIRouter(db, map[string]sessionFixture{
		"admin-session": {userID: adminUserID, role: iamdomain.RoleAdmin, email: "admin@example.com"},
	}, true)

	w := httptest.NewRecorder()
	req := authenticatedJSONRequest(http.MethodPost, "/v1/admin/wallets/"+strconv.Itoa(int(targetUserID))+"/credit", "admin-session", `{"amount":"5.00","reason":"manual credit"}`)
	req.Header.Set("Idempotency-Key", "idem-admin-credit")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var wallet billinginfra.WalletModel
	require.NoError(t, db.First(&wallet, "user_id = ?", targetUserID).Error)
	require.Equal(t, "5.000000", wallet.ConsumerBalance)

	var logs int64
	require.NoError(t, db.Model(&governanceinfra.OperationLogModel{}).
		Where("operator_user_id = ? AND operation_type = ? AND resource_type = ? AND resource_id = ? AND result = ?", adminUserID, "billing.wallet.credit", "billing", strconv.Itoa(int(targetUserID)), "success").
		Count(&logs).Error)
	require.EqualValues(t, 1, logs)
}

func TestBillingAdminCardCreateRequiresIdempotencyAndReplays(t *testing.T) {
	db := newBillingAPITestDB(t)
	adminUserID := createBillingAPIUser(t, db, "card-admin@example.com", iamdomain.RoleAdmin)
	router := newBillingAPIRouter(db, map[string]sessionFixture{
		"admin-session": {userID: adminUserID, role: iamdomain.RoleAdmin, email: "card-admin@example.com"},
	}, true)

	body := `{"amount":"6.00","count":2}`
	w := httptest.NewRecorder()
	req := authenticatedJSONRequest(http.MethodPost, "/v1/admin/cards", "admin-session", body)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "Idempotency-Key is required.")

	w = httptest.NewRecorder()
	req = authenticatedJSONRequest(http.MethodPost, "/v1/admin/cards", "admin-session", body)
	req.Header.Set("Idempotency-Key", "idem-admin-create-cards")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	firstBody := w.Body.String()

	w = httptest.NewRecorder()
	req = authenticatedJSONRequest(http.MethodPost, "/v1/admin/cards", "admin-session", body)
	req.Header.Set("Idempotency-Key", "idem-admin-create-cards")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.JSONEq(t, firstBody, w.Body.String())

	var cardCount int64
	require.NoError(t, db.Model(&billinginfra.CardKeyModel{}).Where("created_by_user_id = ?", adminUserID).Count(&cardCount).Error)
	require.EqualValues(t, 2, cardCount)
}

func TestBillingAdminCardUpdateNotFoundUsesAdminError(t *testing.T) {
	db := newBillingAPITestDB(t)
	adminUserID := createBillingAPIUser(t, db, "missing-card-admin@example.com", iamdomain.RoleAdmin)
	router := newBillingAPIRouter(db, map[string]sessionFixture{
		"admin-session": {userID: adminUserID, role: iamdomain.RoleAdmin, email: "missing-card-admin@example.com"},
	}, true)

	w := httptest.NewRecorder()
	req := authenticatedJSONRequest(http.MethodPatch, "/v1/admin/cards/missing-card", "admin-session", `{"status":"disabled"}`)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "Card key not found.")
	require.NotContains(t, w.Body.String(), "cannot be redeemed")
}

type sessionFixture struct {
	userID uint
	role   iamdomain.Role
	email  string
}

type fakeSessionFetcher struct {
	sessions map[string]sessionFixture
}

func (f fakeSessionFetcher) FetchSession(_ context.Context, sessionID string) (uint, iamdomain.Role, string, bool, error) {
	session, ok := f.sessions[sessionID]
	return session.userID, session.role, session.email, ok, nil
}

type fakePermissionChecker struct {
	allowed bool
}

func (f fakePermissionChecker) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return f.allowed, nil
}

type recordingBillingPermissionChecker struct {
	calls []string
}

func (c *recordingBillingPermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	c.calls = append(c.calls, resource+":"+action)
	return false, nil
}

func (c *recordingBillingPermissionChecker) lastPermission() string {
	if len(c.calls) == 0 {
		return ""
	}
	return c.calls[len(c.calls)-1]
}

func newBillingAPIRouter(db *gorm.DB, sessions map[string]sessionFixture, allowPermission bool) *gin.Engine {
	return newBillingAPIRouterWithChecker(db, sessions, fakePermissionChecker{allowed: allowPermission})
}

func newBillingAPIRouterWithChecker(db *gorm.DB, sessions map[string]sessionFixture, checker middleware.PermissionChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterBillingRoutes(router.Group("/v1"), NewBillingModule(db), fakeSessionFetcher{sessions: sessions}, checker, nil)
	return router
}

func authenticatedJSONRequest(method, target, sessionID, body string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(middleware.CSRFHeaderName, "csrf")
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: sessionID})
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	return req
}

func createBillingAPIUser(t *testing.T, db *gorm.DB, email string, role iamdomain.Role) uint {
	t.Helper()
	type userModel struct {
		ID           uint   `gorm:"primaryKey"`
		Email        string `gorm:"column:email"`
		PasswordHash string `gorm:"column:password_hash"`
		Nickname     string `gorm:"column:nickname"`
		Role         string `gorm:"column:role"`
	}
	user := userModel{
		Email:        email,
		PasswordHash: "hash",
		Nickname:     "Billing API Test",
		Role:         role.String(),
	}
	require.NoError(t, db.Table("users").Create(&user).Error)
	return user.ID
}
