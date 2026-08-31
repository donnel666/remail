package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type gmailPermissionChecker struct {
	allowed map[string]bool
	calls   []string
}

func (c *gmailPermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	key := resource + "/" + action
	c.calls = append(c.calls, key)
	return c.allowed[key], nil
}

type gmailAdminTestUser struct {
	ID          uint `gorm:"primaryKey"`
	Email       string
	Nickname    string
	Role        string
	Status      string
	UserGroupID uint
}

func (gmailAdminTestUser) TableName() string { return "users" }

type gmailAdminTestGroup struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

func (gmailAdminTestGroup) TableName() string { return "user_groups" }

func TestLocalResourcesAPIKeepsCredentialsWriteOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:gmail-local-api-safe-list?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &gmailAdminTestUser{}, &gmailAdminTestGroup{}))
	require.NoError(t, db.Create(&gmailAdminTestGroup{ID: 1, Name: "Admins"}).Error)
	require.NoError(t, db.Create(&gmailAdminTestUser{ID: 1, Email: "owner@example.com", Nickname: "Owner", Role: "admin", Status: "active", UserGroupID: 1}).Error)
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1, Version: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID:    root.ID,
		Email: "safe@gmail.com", Identity: "safe@gmail.com", Password: "login-password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop", Status: LocalResourceNormal,
	}).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/gmail/resources?limit=200", nil)

	(&handler{service: NewService(db, nil)}).localResources(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	for _, secret := range []string{"login-password", "JBSWY3DPEHPK3PXP", "abcdefghijklmnop"} {
		require.NotContains(t, recorder.Body.String(), secret)
	}
	require.Contains(t, recorder.Body.String(), `"passwordConfigured":true`)
	var result LocalResourceList
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, adminGmailResourceMaxLimit, result.Limit)
}

func TestAdminGmailCredentialRouteRequiresOperatePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := &gmailPermissionChecker{allowed: map[string]bool{"core:resource/write": true}}
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterRoutes(
		router.Group("/v1"),
		&Module{},
		middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 9, iamdomain.RoleAdmin, "admin@test.local", true
		}),
		checker,
	)

	request := httptest.NewRequest(http.MethodPut, "/v1/admin/gmail/resources/42/credentials", bytes.NewBufferString(`{"version":1,"password":"new-secret"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "replace-gmail-credentials")
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	request.Header.Set(middleware.CSRFHeaderName, "csrf")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	require.Equal(t, []string{"core:resource/operate"}, checker.calls)
}

func TestAdminGmailEditCredentialsRequiresOperatePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := &gmailPermissionChecker{allowed: map[string]bool{"core:resource/write": true}}
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterRoutes(
		router.Group("/v1"),
		&Module{},
		middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 9, iamdomain.RoleAdmin, "admin@test.local", true
		}),
		checker,
	)

	request := httptest.NewRequest(http.MethodPatch, "/v1/admin/gmail/resources/42", bytes.NewBufferString(`{"version":1,"ownerId":7,"email":"safe@gmail.com","appPassword":"abcd efgh ijkl mnop"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "edit-gmail-credentials")
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	request.Header.Set(middleware.CSRFHeaderName, "csrf")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	require.Equal(t, []string{"core:resource/write", "core:resource/operate"}, checker.calls)
}

func TestAdminGmailAliasRouteReturnsObservedVariantsWithReadPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:gmail-admin-alias-route?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &allocationModel{}))
	require.NoError(t, db.Create(&resourceRootModel{ID: 1, Type: "gmail", OwnerUserID: 7, Version: 1}).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: 1, ResourceType: "gmail", OwnerUserID: 7, Email: "route@gmail.com", Identity: "route@gmail.com",
		Status: LocalResourceNormal,
	}).Error)
	resourceID := uint(1)
	require.NoError(t, db.Create(&allocationModel{
		OrderNo: "ALIAS-ROUTE-DOT", ResourceID: &resourceID, Mailbox: GmailMailboxDot,
		Email: "r.oute@gmail.com", Status: AllocationStatusReleased,
	}).Error)
	require.NoError(t, db.Create(&allocationModel{
		OrderNo: "ALIAS-ROUTE-MAIN", ResourceID: &resourceID, Mailbox: GmailMailboxMain,
		Email: "route@gmail.com", Status: AllocationStatusAllocated,
	}).Error)

	checker := &gmailPermissionChecker{allowed: map[string]bool{"core:resource/read": true}}
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterRoutes(
		router.Group("/v1"),
		&Module{Service: NewService(db, nil)},
		middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 9, iamdomain.RoleAdmin, "admin@test.local", true
		}),
		checker,
	)

	request := httptest.NewRequest(http.MethodGet, "/v1/admin/gmail/resources/1/aliases?kind=other&offset=0&limit=20", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result AdminGmailAliasList
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.EqualValues(t, 1, result.Total)
	require.Equal(t, GmailMailboxDot, result.Items[0].Kind)
	require.Equal(t, "r.oute@gmail.com", result.Items[0].EmailAddress)
	require.NotContains(t, response.Body.String(), "schedule")
	require.Equal(t, []string{"core:resource/read"}, checker.calls)
}
