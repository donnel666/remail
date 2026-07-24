package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	settingsapp "github.com/donnel666/remail/internal/systemsettings/app"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeRepository struct {
	items map[string]settingsdomain.Setting
}

func (r *fakeRepository) WithTx(ctx context.Context, fn func(context.Context) error) error {
	snapshot := make(map[string]settingsdomain.Setting, len(r.items))
	for key, item := range r.items {
		snapshot[key] = item
	}
	if err := fn(ctx); err != nil {
		r.items = snapshot
		return err
	}
	return nil
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
	cloned := item
	return &cloned, nil
}

func (r *fakeRepository) Upsert(_ context.Context, key, value string) (*settingsdomain.Setting, error) {
	item := r.items[key]
	item.Key, item.Value = key, value
	r.items[key] = item
	return &item, nil
}

func (r *fakeRepository) BulkUpsert(ctx context.Context, settings []settingsdomain.Setting) ([]settingsdomain.Setting, error) {
	result := make([]settingsdomain.Setting, len(settings))
	for i, setting := range settings {
		saved, err := r.Upsert(ctx, setting.Key, setting.Value)
		if err != nil {
			return nil, err
		}
		result[i] = *saved
	}
	return result, nil
}

func (r *fakeRepository) Delete(_ context.Context, key string) error {
	if _, ok := r.items[key]; !ok {
		return settingsdomain.ErrSettingNotFound
	}
	delete(r.items, key)
	return nil
}

type fakeOperationLogs struct {
	items []*governancedomain.OperationLog
}

func (l *fakeOperationLogs) Create(_ context.Context, log *governancedomain.OperationLog) error {
	cloned := *log
	l.items = append(l.items, &cloned)
	return nil
}

func testRouter(repo settingsapp.Repository, checkers ...permissionCheckerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	fetcher := middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 1, iamdomain.RoleAdmin, "admin@example.com", true
	})
	checker := permissionCheckerFunc(func(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
		return true, nil
	})
	if len(checkers) > 0 {
		checker = checkers[0]
	}
	RegisterRoutes(r.Group("/v1"), &Module{
		Settings: settingsapp.NewSystemSettingsUseCase(repo, &fakeOperationLogs{}),
	}, fetcher, checker)
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
	require.Equal(t, "no-store", put.Header().Get("Cache-Control"))

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

func TestSensitiveSettingsRequireSensitivePermission(t *testing.T) {
	repo := &fakeRepository{items: map[string]settingsdomain.Setting{
		"site_title":           {Key: "site_title", Value: "ReMail"},
		"github_client_secret": {Key: "github_client_secret", Value: "secret"},
		"epay_merchant_key":    {Key: "epay_merchant_key", Value: "merchant-secret"},
	}}
	checker := permissionCheckerFunc(func(_ context.Context, _ uint, _ iamdomain.Role, _, action string) (bool, error) {
		return action != "sensitive", nil
	})
	r := testRouter(repo, checker)

	list := httptest.NewRecorder()
	r.ServeHTTP(list, requestWithSession(http.MethodGet, "/v1/admin/settings", ""))
	require.Equal(t, http.StatusOK, list.Code)
	require.Equal(t, "no-store", list.Header().Get("Cache-Control"))
	var payload struct {
		Options []settingDTO `json:"options"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &payload))
	require.Equal(t, []settingDTO{{Key: "site_title", Value: "ReMail", CreatedAt: "0001-01-01T00:00:00.000Z", UpdatedAt: "0001-01-01T00:00:00.000Z"}}, payload.Options)

	for _, request := range []*http.Request{
		requestWithSession(http.MethodGet, "/v1/admin/settings/github_client_secret", ""),
		requestWithSession(http.MethodGet, "/v1/admin/settings/%20github_client_secret%20", ""),
		requestWithSession(http.MethodPut, "/v1/admin/settings/github_client_secret", `{"value":"replacement"}`),
		requestWithSession(http.MethodDelete, "/v1/admin/settings/epay_merchant_key", ""),
	} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code)
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	}
	require.Equal(t, "secret", repo.items["github_client_secret"].Value)
	require.Equal(t, "merchant-secret", repo.items["epay_merchant_key"].Value)
}

func TestBulkSettingsPermissionAndBlankSecretSafety(t *testing.T) {
	repo := &fakeRepository{items: map[string]settingsdomain.Setting{
		"site_title":           {Key: "site_title", Value: "old"},
		"github_client_secret": {Key: "github_client_secret", Value: "secret"},
	}}
	checker := permissionCheckerFunc(func(_ context.Context, _ uint, _ iamdomain.Role, _, action string) (bool, error) {
		return action != "sensitive", nil
	})
	r := testRouter(repo, checker)

	denied := httptest.NewRecorder()
	r.ServeHTTP(denied, requestWithSession(http.MethodPut, "/v1/admin/settings", `{"settings":[{"key":"site_title","value":"new"},{"key":" github_client_secret ","value":"replacement"}]}`))
	require.Equal(t, http.StatusForbidden, denied.Code)
	require.Equal(t, "old", repo.items["site_title"].Value)
	require.Equal(t, "secret", repo.items["github_client_secret"].Value)

	blankDenied := httptest.NewRecorder()
	r.ServeHTTP(blankDenied, requestWithSession(http.MethodPut, "/v1/admin/settings", `{"settings":[{"key":"site_title","value":"new"},{"key":"github_client_secret","value":""}]}`))
	require.Equal(t, http.StatusForbidden, blankDenied.Code)
	require.Equal(t, "old", repo.items["site_title"].Value)
	require.Equal(t, "secret", repo.items["github_client_secret"].Value)

	privileged := testRouter(repo)
	cleared := httptest.NewRecorder()
	privileged.ServeHTTP(cleared, requestWithSession(http.MethodPut, "/v1/admin/settings", `{"settings":[{"key":"site_title","value":"new"},{"key":"github_client_secret","value":""}]}`))
	require.Equal(t, http.StatusOK, cleared.Code)
	require.Equal(t, "new", repo.items["site_title"].Value)
	require.Equal(t, "", repo.items["github_client_secret"].Value)
}
