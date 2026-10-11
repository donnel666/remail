package icloud

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOwnedICloudRoutesRequireSessionAndCSRF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, db, _, _ := newOnboardingStateTest(t)
	s.SetImportOwnerValidator(func(_ context.Context, id uint) (bool, error) { return id == 9, nil })
	s.deviceRedis = redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = s.deviceRedis.Close() })
	router := gin.New()
	RegisterUserRoutes(router.Group("/v1"), &Module{Service: s}, middleware.SessionFetcherFunc(func(_ context.Context, sid string) (uint, iamdomain.Role, string, bool) {
		return 9, iamdomain.RoleUser, "user@example.com", sid == "session"
	}), nil)
	request := func(method, path, body string, session, csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "request-key")
		if session {
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
		}
		if csrf {
			req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
			req.Header.Set(middleware.CSRFHeaderName, "csrf")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	require.Equal(t, http.StatusUnauthorized, request(http.MethodGet, "/v1/icloud/resources", "", false, false).Code)
	require.Equal(t, http.StatusForbidden, request(http.MethodDelete, "/v1/icloud/resources/1", `{"version":1}`, true, false).Code)
	require.Equal(t, http.StatusForbidden, request(http.MethodPost, "/v1/icloud/resources/1/publish", `{"version":1}`, true, true).Code)
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, "/v1/icloud/resources/1/aliases", "", true, true).Code)
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, "/v1/icloud/resources/imports/1", "", true, true).Code)
	require.Equal(t, http.StatusNotFound, request(http.MethodDelete, "/v1/icloud/resources/1", `{"version":1}`, true, true).Code)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "accounts.txt")
	require.NoError(t, err)
	_, err = file.Write([]byte("userapple@example.com----secret----https://codes.example.com/token"))
	require.NoError(t, err)
	require.NoError(t, form.WriteField("ownerId", "1"))
	require.NoError(t, form.WriteField("expireAt", "2099-01-01T00:00:00Z"))
	require.NoError(t, form.Close())
	req := httptest.NewRequest(http.MethodPost, "/v1/icloud/resources/imports", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Idempotency-Key", "owned-import")
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	req.Header.Set(middleware.CSRFHeaderName, "csrf")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var view OwnedICloudImportView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &view))
	var root iCloudRootModel
	require.NoError(t, db.First(&root, view.Tasks[0].ID).Error)
	require.EqualValues(t, 9, root.OwnerUserID)
	var resource iCloudResourceModel
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, s.now().Add(userICloudResourceLifetime), resource.ExpireAt)
	require.NotContains(t, response.Body.String(), "secret")
	require.NotContains(t, response.Body.String(), "codes.example.com")
}
