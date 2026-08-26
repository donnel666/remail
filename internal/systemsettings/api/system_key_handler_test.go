package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	settingsapp "github.com/donnel666/remail/internal/systemsettings/app"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func systemKeyAdminRouter(t *testing.T, checker permissionCheckerFunc) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&settingsinfra.SystemKeyModel{}))
	repo := settingsinfra.NewRepository(db)
	fetcher := middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 1, iamdomain.RoleAdmin, "admin@example.com", true
	})
	router := gin.New()
	RegisterRoutes(router.Group("/v1"), &Module{
		SystemKeys: settingsapp.NewSystemKeyUseCase(repo, &fakeOperationLogs{}),
	}, fetcher, checker)
	return router, db
}

func TestAdminSystemKeyPlaintextIsReturnedOnlyOnCreate(t *testing.T) {
	router, _ := systemKeyAdminRouter(t, permissionCheckerFunc(func(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
		return true, nil
	}))

	createdResponse := httptest.NewRecorder()
	router.ServeHTTP(createdResponse, requestWithSession(http.MethodPost, "/v1/admin/system-keys", `{"name":"SMTP sender","purpose":"smtp_submission"}`))
	require.Equal(t, http.StatusCreated, createdResponse.Code)
	var created systemKeyDTO
	require.NoError(t, json.Unmarshal(createdResponse.Body.Bytes(), &created))
	require.NotEmpty(t, created.KeyPlain)
	require.Equal(t, "smtp_submission", created.Purpose)

	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, requestWithSession(http.MethodGet, "/v1/admin/system-keys", ""))
	require.Equal(t, http.StatusOK, listResponse.Code)
	require.NotContains(t, listResponse.Body.String(), created.KeyPlain)
	var listed struct {
		Items []systemKeyDTO `json:"items"`
	}
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &listed))
	require.Len(t, listed.Items, 1)
	require.Empty(t, listed.Items[0].KeyPlain)
}

func TestAdminSystemKeyCreationRequiresSensitivePermission(t *testing.T) {
	router, db := systemKeyAdminRouter(t, permissionCheckerFunc(func(_ context.Context, _ uint, _ iamdomain.Role, _, action string) (bool, error) {
		return action != "sensitive", nil
	}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, requestWithSession(http.MethodPost, "/v1/admin/system-keys", `{"name":"denied"}`))
	require.Equal(t, http.StatusForbidden, response.Code)
	var count int64
	require.NoError(t, db.Model(&settingsinfra.SystemKeyModel{}).Count(&count).Error)
	require.Zero(t, count)
}
