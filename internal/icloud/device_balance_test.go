package icloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/api/middleware"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type deviceBalancePermissions struct{ deny string }

func (p *deviceBalancePermissions) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	return resource+"/"+action != p.deny, nil
}

func TestDevicePlatformBalanceAndCardRecharge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, db, _, _ := newOnboardingStateTest(t)
	red := miniredis.RunT(t)
	s.deviceRedis = redis.NewClient(&redis.Options{Addr: red.Addr()})
	t.Cleanup(func() { _ = s.deviceRedis.Close() })
	balance := json.Number("1000")
	gets, posts := 0, 0
	reject, fail := false, false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/auth/me":
			gets++
			require.Equal(t, http.MethodGet, r.Method)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"id": 919, "username": "private-name", "balance": balance, "status": "normal", "token_version": 1, "last_login_ip": "private-ip"}})
		case "/api/balance/recharge/card":
			posts++
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, map[string]string{"card_key": "CARD-SECRET"}, payload)
			if fail {
				w.WriteHeader(500)
				return
			}
			if reject {
				_, _ = w.Write([]byte(`{"code":1,"message":"invalid card"}`))
				return
			}
			balance = "1500.25"
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"balance": balance}, "message": "充值tai点成功"})
		default:
			t.Errorf("unexpected vendor route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	s.device.http.Transport = server.Client().Transport
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
	permissions := &deviceBalancePermissions{}
	fetcher := middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 1, "admin", "admin@example.com", true
	})
	router := gin.New()
	RegisterRoutes(router.Group("/v1"), &Module{Service: s}, fetcher, permissions)
	request := func(method, body string, session, csrf bool) *httptest.ResponseRecorder {
		path := "/v1/admin/icloud/device-platform/balance"
		if method == http.MethodPost {
			path = "/v1/admin/icloud/device-platform/recharges"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if session {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "sid"})
		}
		if csrf {
			r.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
			r.Header.Set(middleware.CSRFHeaderName, "csrf")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, 401, request(http.MethodGet, "", false, false).Code)
	permissions.deny = "system:settings/read"
	require.Equal(t, 403, request(http.MethodGet, "", true, false).Code)
	permissions.deny = ""
	w := request(http.MethodGet, "", true, false)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"balance":"1000"`)
	require.NotContains(t, w.Body.String(), "private-name")
	require.NotContains(t, w.Body.String(), "private-ip")
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, 1, gets)
	balance = "0"
	require.Contains(t, request(http.MethodGet, "", true, false).Body.String(), `"balance":"0"`)
	permissions.deny = "system:settings/sensitive"
	require.Equal(t, 403, request(http.MethodPost, `{"cardKey":"CARD-SECRET"}`, true, true).Code)
	permissions.deny = ""
	require.Equal(t, 403, request(http.MethodPost, `{"cardKey":"CARD-SECRET"}`, true, false).Code)
	require.Equal(t, 422, request(http.MethodPost, `{"cardKey":" "}`, true, true).Code)
	require.Zero(t, posts)
	w = request(http.MethodPost, `{"cardKey":" CARD-SECRET "}`, true, true)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"balance":"1500.25"`)
	require.Equal(t, 1, posts)
	reject = true
	require.Equal(t, 422, request(http.MethodPost, `{"cardKey":"CARD-SECRET"}`, true, true).Code)
	require.Equal(t, 2, posts)
	reject = false
	fail = true
	w = request(http.MethodPost, `{"cardKey":"CARD-SECRET"}`, true, true)
	require.Equal(t, 503, w.Code)
	require.Contains(t, w.Body.String(), "uncertain")
	require.Equal(t, 3, posts, "ambiguous redemption must not be retried automatically")
	var logs []governanceinfra.OperationLogModel
	require.NoError(t, db.Find(&logs).Error)
	require.Len(t, logs, 3)
	encoded, err := json.Marshal(logs)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "CARD-SECRET")
	require.NotContains(t, string(encoded), "test-key")
	runtimeconfig.Set(runtimeconfig.ICloudDeviceAPIKey, "")
	w = request(http.MethodGet, "", true, false)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"configured":false`)
	require.Contains(t, w.Body.String(), `"balance":null`)
}
