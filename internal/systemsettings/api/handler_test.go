package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	settingsapp "github.com/donnel666/remail/internal/systemsettings/app"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeRepository struct {
	items map[string]settingsdomain.Setting
}

func (r *fakeRepository) List(context.Context) ([]settingsdomain.Setting, error) {
	items := make([]settingsdomain.Setting, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, item)
	}
	return items, nil
}

func (r *fakeRepository) Get(_ context.Context, key string) (*settingsdomain.Setting, error) {
	item, ok := r.items[key]
	if !ok {
		return nil, settingsdomain.ErrSettingNotFound
	}
	copy := item
	return &copy, nil
}

func (r *fakeRepository) Upsert(_ context.Context, key, value string) (*settingsdomain.Setting, error) {
	item := r.items[key]
	item.Key, item.Value = key, value
	r.items[key] = item
	return &item, nil
}

func (r *fakeRepository) Delete(_ context.Context, key string) error {
	if _, ok := r.items[key]; !ok {
		return settingsdomain.ErrSettingNotFound
	}
	delete(r.items, key)
	return nil
}

func testRouter(repo settingsapp.Repository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	fetcher := middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 1, iamdomain.RoleAdmin, "admin@example.com", true
	})
	checker := permissionCheckerFunc(func(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
		return true, nil
	})
	RegisterRoutes(r.Group("/v1"), &Module{Settings: settingsapp.NewSystemSettingsUseCase(repo)}, fetcher, checker)
	return r
}

type permissionCheckerFunc func(context.Context, uint, iamdomain.Role, string, string) (bool, error)

func (f permissionCheckerFunc) Check(ctx context.Context, id uint, role iamdomain.Role, resource, action string) (bool, error) {
	return f(ctx, id, role, resource, action)
}

func requestWithSession(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	if method != http.MethodGet {
		req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
		req.Header.Set(middleware.CSRFHeaderName, "csrf")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestAdminSettingsCRUD(t *testing.T) {
	repo := &fakeRepository{items: map[string]settingsdomain.Setting{}}
	r := testRouter(repo)

	put := httptest.NewRecorder()
	r.ServeHTTP(put, requestWithSession(http.MethodPut, "/v1/admin/settings/site.title", `{"value":"ReMail"}`))
	require.Equal(t, http.StatusOK, put.Code)

	list := httptest.NewRecorder()
	r.ServeHTTP(list, requestWithSession(http.MethodGet, "/v1/admin/settings", ""))
	require.Equal(t, http.StatusOK, list.Code)
	var payload struct {
		Options []settingDTO `json:"options"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &payload))
	require.Len(t, payload.Options, 1)
	require.Equal(t, "site.title", payload.Options[0].Key)

	del := httptest.NewRecorder()
	r.ServeHTTP(del, requestWithSession(http.MethodDelete, "/v1/admin/settings/site.title", ""))
	require.Equal(t, http.StatusNoContent, del.Code)

	missing := httptest.NewRecorder()
	r.ServeHTTP(missing, requestWithSession(http.MethodGet, "/v1/admin/settings/site.title", ""))
	require.Equal(t, http.StatusNotFound, missing.Code)
}
