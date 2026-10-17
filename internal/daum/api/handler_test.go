package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testRoot struct {
	ID          uint `gorm:"primaryKey"`
	Type        string
	OwnerUserID uint
	Version     uint64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (testRoot) TableName() string { return "email_resources" }

func TestDaumAdminResponsesKeepBothPasswordsAndPickupURLWriteOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:daum-api-secret?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&testRoot{}, &infra.Resource{}, &infra.ExplicitAlias{}, &infra.BindingMailbox{}, &infra.MaintenanceRun{}))
	service := infra.NewService(db)
	id, _, err := service.ImportLine(context.Background(), 7, domain.ImportLine{Email: "account@daum.net", Password: "private-login", AppPassword: "private-app", BindingAddress: "aux@example.test", PickupURL: "https://remail.test/pickup?email=aux%40example.test&token=private-token"})
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/admin/daum/resources", fmt.Sprintf("/v1/admin/daum/resources/%d", id)} {
		router := gin.New()
		RegisterRoutes(router.Group("/v1"), NewModuleWithQueue(service, nil), middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 1, iamdomain.RoleAdmin, "admin@example.test", true
		}), allowDaumPermission{})
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		body := response.Body.String()
		for _, secret := range []string{"private-login", "private-app", "private-token", "https://remail.test/pickup"} {
			require.NotContains(t, body, secret)
		}
		require.Contains(t, body, `"appPasswordConfigured":true`)
		require.Contains(t, body, `"loginPasswordConfigured":true`)
		require.Contains(t, body, `"pickupUrlConfigured":true`)
		require.Contains(t, body, "account@hanmail.net")
		require.Contains(t, body, "account@hanmail2.net")
	}
}
func TestDaumBindingEditsRequireAuxiliaryWritePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var checked []string
	checker := daumPermissionCheckerFunc(func(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
		checked = append(checked, resource+":"+action)
		return resource != "mailtransport:binding", nil
	})
	router := daumPermissionRouter(iamdomain.RoleAdmin, checker)
	for _, field := range []string{"bindingAddress", "pickupUrl"} {
		checked = nil
		body, _ := json.Marshal(map[string]any{"version": 1, field: "sensitive-binding"})
		request := httptest.NewRequest(http.MethodPatch, "/v1/admin/daum/resources/1", strings.NewReader(string(body)))
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
		request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
		request.Header.Set(middleware.CSRFHeaderName, "csrf")
		request.Header.Set("Idempotency-Key", "binding-write")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code)
		require.Contains(t, checked, "mailtransport:binding:write")
	}
}

func TestDaumCredentialsRejectAuxiliaryAndMetadataFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := daumPermissionRouter(iamdomain.RoleAdmin, allowDaumPermission{})
	for _, field := range []string{"bindingAddress", "pickupUrl", "kakaoEmail", "ownerId", "longLived"} {
		t.Run(field, func(t *testing.T) {
			value := any("other@example.test")
			if field == "ownerId" {
				value = 7
			}
			if field == "longLived" {
				value = true
			}
			payload, _ := json.Marshal(map[string]any{"version": 1, "appPassword": "fixture-only", field: value})
			req := httptest.NewRequest(http.MethodPut, "/v1/admin/daum/resources/1/credentials", strings.NewReader(string(payload)))
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
			req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
			req.Header.Set(middleware.CSRFHeaderName, "csrf")
			req.Header.Set("Idempotency-Key", "reject-extra")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}

func TestDaumAuxiliaryOnlyEditDoesNotRequireResourceOperate(t *testing.T) {
	var checked []string
	router := daumPermissionRouter(iamdomain.RoleAdmin, daumPermissionCheckerFunc(func(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
		checked = append(checked, resource+":"+action)
		return resource != "core:resource" || action != "operate", nil
	}))
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/daum/resources/1", strings.NewReader(`{"version":1,"bindingAddress":null}`))
	setDaumTestMutationHeaders(req)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	require.Equal(t, http.StatusServiceUnavailable, response.Code, "request must pass authorization and reach the intentionally unavailable database")
	require.Equal(t, []string{"core:resource:write", "mailtransport:binding:write"}, checked)
}

func TestDaumManualMaintenanceAcceptsEmptyBodyWithoutVersion(t *testing.T) {
	router := daumPermissionRouter(iamdomain.RoleAdmin, allowDaumPermission{})
	for _, action := range []string{"validate", "history"} {
		for _, body := range []string{"", `{}`, `{"version":1}`} {
			req := httptest.NewRequest(http.MethodPost, "/v1/admin/daum/resources/1/"+action, strings.NewReader(body))
			setDaumTestMutationHeaders(req)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
		}
	}
}

func setDaumTestMutationHeaders(req *http.Request) {
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	req.Header.Set(middleware.CSRFHeaderName, "csrf")
	req.Header.Set("Idempotency-Key", "fixture-request")
}

func TestDaumImportReplayDoesNotDependOnPrivateStorage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:daum-api-replay?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&infra.ImportStatus{}))
	service := infra.NewService(db)
	content := []byte("replay@daum.net----fixture")
	id, _, err := service.CreateImportWithOptions(context.Background(), 7, 7, "skip", "fixture-request", "already-stored", "source.txt", "", content, false, "imap")
	require.NoError(t, err)
	module := NewModuleWithQueue(service, nil)
	module.ValidateOwner = func(context.Context, uint) (bool, error) { return true, nil }
	router := gin.New()
	RegisterRoutes(router.Group("/v1"), module, middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 7, iamdomain.RoleAdmin, "admin@example.test", true
	}), allowDaumPermission{})
	for _, conflict := range []bool{false, true} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("ownerId", "7"))
		part, err := writer.CreateFormFile("file", "source.txt")
		require.NoError(t, err)
		if conflict {
			_, err = part.Write([]byte("changed@daum.net----fixture"))
		} else {
			_, err = part.Write(content)
		}
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/daum/resources/imports", &body)
		setDaumTestMutationHeaders(req)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if conflict {
			require.Equal(t, http.StatusConflict, response.Code)
		} else {
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), fmt.Sprintf(`"importId":%d`, id))
			require.Contains(t, response.Body.String(), `"reused":true`)
		}
	}
}

func TestDaumImportSizeUsesMicrosoftSettingAndCeiling(t *testing.T) {
	runtimeconfig.Delete("resource_import_max_bytes")
	t.Cleanup(func() { runtimeconfig.Delete("resource_import_max_bytes") })
	require.Equal(t, 100<<20, maxImportBytesValue())
	runtimeconfig.Set("resource_import_max_bytes", "209715200")
	require.Equal(t, 200<<20, maxImportBytesValue())
	runtimeconfig.Set("resource_import_max_bytes", "1073741824")
	require.Equal(t, 512<<20, maxImportBytesValue())
}
