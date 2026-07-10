package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var billingAPIMySQLTestServer = testmysql.New("remail_billing_api_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = billingAPIMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newBillingAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return billingAPIMySQLTestServer.Database(t, billingAPIMigrationsDir(t))
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

func (f fakeSessionFetcher) FetchSession(_ context.Context, sessionID string) (uint, iamdomain.Role, string, bool) {
	session, ok := f.sessions[sessionID]
	return session.userID, session.role, session.email, ok
}

type fakePermissionChecker struct {
	allowed bool
}

func (f fakePermissionChecker) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return f.allowed, nil
}

func newBillingAPIRouter(db *gorm.DB, sessions map[string]sessionFixture, allowPermission bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterBillingRoutes(router.Group("/v1"), NewBillingModule(db), fakeSessionFetcher{sessions: sessions}, fakePermissionChecker{allowed: allowPermission})
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
