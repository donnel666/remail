package kitesim

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/businessday"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type smsLinkTestDoer func(*http.Request) (*http.Response, error)

func (f smsLinkTestDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

type smsLinkPermission struct{ deny string }

func (p *smsLinkPermission) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	return resource+"/"+action != p.deny, nil
}

func TestSMSLinkChangesRollBackOnAuditFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exists  bool
		enabled bool
	}{
		{"generate", false, true},
		{"rotate", true, true},
		{"revoke", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, db, clock := newSMSPoolTestService(t)
			var original *string
			if tc.exists {
				hash := strings.Repeat("a", 64)
				original = &hash
			}
			require.NoError(t, db.Model(&phoneModel{}).Where("id = 1").Updates(map[string]any{
				"expire_time": clock.Add(time.Hour).Format(time.RFC3339), "sms_link_token_hash": original,
			}).Error)
			meta := MutationMeta{OperatorUserID: 1, Path: "/test"}
			// The real audit repository fails while its table is unavailable.
			view, err := service.updateSMSLink(context.Background(), 1, tc.enabled, meta)
			require.Error(t, err)
			require.Nil(t, view)
			var phone phoneModel
			require.NoError(t, db.First(&phone, 1).Error)
			require.Equal(t, original, phone.SMSLinkTokenHash, "failed audit must preserve the existing credential")

			require.NoError(t, db.AutoMigrate(&governanceinfra.OperationLogModel{}))
			view, err = service.updateSMSLink(context.Background(), 1, tc.enabled, meta)
			require.NoError(t, err)
			require.Equal(t, tc.enabled, view.Enabled)
			var saved phoneModel
			require.NoError(t, db.First(&saved, 1).Error)
			if tc.enabled {
				require.NotNil(t, saved.SMSLinkTokenHash)
				require.Equal(t, smsLinkHash(strings.TrimPrefix(view.Path, "/sms/")), *saved.SMSLinkTokenHash)
			} else {
				require.Nil(t, saved.SMSLinkTokenHash)
			}
			var count int64
			require.NoError(t, db.Model(&governanceinfra.OperationLogModel{}).Count(&count).Error)
			require.Equal(t, int64(1), count)
		})
	}
}

func TestDeletedSMSLinkStaysRevokedAfterReimport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, clock := newSMSPoolTestService(t)
	require.NoError(t, db.AutoMigrate(&operationModel{}, &syncRunModel{}, &upstreamSettingsModel{}))
	service.logs = testOperationLogs{}
	service.queue = &testSyncQueue{}
	ctx := context.Background()
	meta := MutationMeta{OperatorUserID: 1, Path: "/test"}
	require.NoError(t, db.Model(&phoneModel{}).Where("id IN ?", []uint{1, 2}).Update("expire_time", clock.Add(time.Hour).Format(time.RFC3339)).Error)
	link, err := service.updateSMSLink(ctx, 1, true, meta)
	require.NoError(t, err)
	other, err := service.updateSMSLink(ctx, 2, true, meta)
	require.NoError(t, err)
	_, err = service.DeletePhones(ctx, []uint{1}, nil, meta)
	require.NoError(t, err)
	var deleted phoneModel
	require.NoError(t, db.First(&deleted, 1).Error)
	require.Nil(t, deleted.SMSLinkTokenHash)

	rdbServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: rdbServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := gin.New()
	RegisterPublicRoutes(router, service, rdb)
	assertRevoked := func() {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, link.Path, nil))
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Equal(t, "Invalid URL|", w.Body.String())
	}
	assertRevoked()

	// Also cover deleted rows left by the version that retained credentials.
	require.NoError(t, db.Model(&phoneModel{}).Where("id = 1").Update("sms_link_token_hash", smsLinkHash(strings.TrimPrefix(link.Path, "/sms/"))).Error)
	_, err = service.ImportAccounts(ctx, "owner@example.com----new-password", meta)
	require.NoError(t, err)
	var restored, untouched phoneModel
	require.NoError(t, db.First(&restored, 1).Error)
	require.Nil(t, restored.DeletedAt)
	require.Nil(t, restored.DisabledAt)
	require.Nil(t, restored.SMSLinkTokenHash)
	assertRevoked()
	require.NoError(t, db.First(&untouched, 2).Error)
	require.NotNil(t, untouched.SMSLinkTokenHash)
	require.Equal(t, smsLinkHash(strings.TrimPrefix(other.Path, "/sms/")), *untouched.SMSLinkTokenHash)
	newLink, err := service.updateSMSLink(ctx, 1, true, meta)
	require.NoError(t, err)
	require.NotEqual(t, link.Path, newLink.Path)
	assertRevoked()
}

func TestSMSPickupLinkLifecycleAndRuntimeWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, clock := newSMSPoolTestService(t)
	service.logs = testOperationLogs{}
	setSMSPoolConfig(t, runtimeconfig.KitesimSMSWindowSecondsKey, "120")
	expires := clock.Add(24 * time.Hour)
	require.NoError(t, db.Model(&phoneModel{}).Where("id = 1").Update("expire_time", expires.Format(time.RFC3339)).Error)
	require.NoError(t, db.Model(&accountModel{}).Where("id = 1").Update("token", "private-upstream-token").Error)
	messages := []Message{
		{Content: "old", SendTime: clock.Add(-121 * time.Second).Format(time.RFC3339)},
		{Content: "验证码 123456 | keep this pipe", CreateTime: clock.Add(-2 * time.Second).In(businessday.Shanghai).Format("2006-01-02 15:04:05")},
		{Content: "future", SendTime: clock.Add(time.Second).Format(time.RFC3339)},
		{Content: "older", SendTime: clock.Add(-30 * time.Second).Format(time.RFC3339)},
	}
	var onFetch func()
	var upstreamErr error
	calls := 0
	service.client = &Client{BaseURL: "https://kitesim.test", customHTTP: true, HTTP: smsLinkTestDoer(func(r *http.Request) (*http.Response, error) {
		calls++
		if upstreamErr != nil {
			return nil, upstreamErr
		}
		require.Equal(t, "/userPhonePurchase/seePhoneNubmerSms", r.URL.Path)
		require.Equal(t, "phone-1", r.URL.Query().Get("orderId"))
		require.Equal(t, "14165550001", r.URL.Query().Get("phoneNumber"))
		if onFetch != nil {
			onFetch()
		}
		body, err := json.Marshal(map[string]any{"code": 200, "data": map[string]any{"noteList": messages}})
		require.NoError(t, err)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	rdbServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: rdbServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := gin.New()
	RegisterPublicRoutes(router, service, rdb)
	checker := &smsLinkPermission{}
	fetcher := middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 1, "admin", "admin@example.com", true
	})
	RegisterRoutes(router.Group("/v1"), service, fetcher, checker)
	adminPath := "/v1/admin/kitesim/phones/1/sms-link"
	adminRequest := func(method string, session, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, adminPath, nil)
		if session {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
		}
		if csrf {
			r.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
			r.Header.Set(middleware.CSRFHeaderName, "csrf")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		require.Equal(t, 401, adminRequest(method, false, true).Code)
		for _, permission := range []string{"core:resource/operate", "mailmatch:message/read"} {
			checker.deny = permission
			require.Equal(t, 403, adminRequest(method, true, true).Code, permission)
		}
		checker.deny = ""
	}
	require.Equal(t, 403, adminRequest(http.MethodPost, true, false).Code)
	require.Equal(t, 403, adminRequest(http.MethodDelete, true, false).Code)
	var view SMSLink
	w := adminRequest(http.MethodGet, true, false)
	require.Equal(t, 200, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	require.False(t, view.Enabled)
	require.True(t, view.CanGenerate)
	require.Equal(t, 120, view.WindowSeconds)
	generate := func() string {
		w := adminRequest(http.MethodPost, true, true)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		var link SMSLink
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &link))
		require.True(t, link.Enabled)
		require.Len(t, strings.TrimPrefix(link.Path, "/sms/"), 43)
		return link.Path
	}
	path := generate()
	var phone phoneModel
	require.NoError(t, db.First(&phone, 1).Error)
	require.Equal(t, smsLinkHash(strings.TrimPrefix(path, "/sms/")), *phone.SMSLinkTokenHash)
	require.NotContains(t, adminRequest(http.MethodGet, true, false).Body.String(), strings.TrimPrefix(path, "/sms/"))
	read := func(path string, code int, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, code, w.Code, w.Body.String())
		require.Equal(t, body, w.Body.String())
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		require.Contains(t, w.Header().Get("Content-Type"), "text/plain")
		return w
	}
	date := expires.In(businessday.Shanghai).Format(time.DateOnly)
	read(path+"?phoneId=2&windowSeconds=86400", 200, "验证码 123456 | keep this pipe|"+date)
	require.Equal(t, 1, calls)
	require.Equal(t, "3", read(path, 429, "Too many requests|"+date).Header().Get("Retry-After"))
	require.Equal(t, 1, calls)

	messages = []Message{{Content: "three minutes old", SendTime: clock.Add(-180 * time.Second).Format(time.RFC3339)}}
	rdbServer.FastForward(3 * time.Second)
	read(path+"?windowSeconds=86400", 200, "No message|"+date)
	runtimeconfig.Set(runtimeconfig.KitesimSMSWindowSecondsKey, "300")
	rdbServer.FastForward(3 * time.Second)
	read(path, 200, "three minutes old|"+date)
	runtimeconfig.Set(runtimeconfig.KitesimSMSWindowSecondsKey, "60")
	rdbServer.FastForward(3 * time.Second)
	read(path, 200, "No message|"+date)

	expires = expires.AddDate(0, 1, 0)
	date = expires.In(businessday.Shanghai).Format(time.DateOnly)
	require.NoError(t, db.Model(&phoneModel{}).Where("id = 1").Update("expire_time", expires.Format(time.RFC3339)).Error)
	rdbServer.FastForward(3 * time.Second)
	read(path, 200, "No message|"+date)
	upstreamErr = errors.New("private upstream credential error")
	rdbServer.FastForward(3 * time.Second)
	read(path, 502, "Service unavailable|"+date)
	upstreamErr = nil

	oldPath := path
	path = generate()
	require.NotEqual(t, oldPath, path)
	before := calls
	read(oldPath, 404, "Invalid URL|")
	read("/sms/not-a-token", 404, "Invalid URL|")
	require.Equal(t, before, calls)
	require.Equal(t, 200, adminRequest(http.MethodDelete, true, true).Code)
	read(path, 404, "Invalid URL|")
	require.Equal(t, before, calls)

	path = generate()
	onFetch = func() { *clock = expires }
	rdbServer.FastForward(3 * time.Second)
	read(path, 410, "URL expired|"+date)
	onFetch = nil
	before = calls
	read(path, 410, "URL expired|"+date)
	require.Equal(t, before, calls)
	require.Equal(t, 409, adminRequest(http.MethodPost, true, true).Code)
	*clock = expires.Add(-time.Hour)
	onFetch = func() {
		require.NoError(t, db.Model(&phoneModel{}).Where("id = 1").Update("sms_link_token_hash", nil).Error)
	}
	rdbServer.FastForward(3 * time.Second)
	read(path, 404, "Invalid URL|")
	onFetch = nil
	path = generate()
	for _, column := range []string{"disabled_at", "deleted_at"} {
		require.NoError(t, db.Model(&phoneModel{}).Where("id = 1").Update(column, *clock).Error)
		before = calls
		if column == "disabled_at" {
			read(path, 410, "URL unavailable|"+date)
		} else {
			read(path, 404, "Invalid URL|")
		}
		require.Equal(t, before, calls)
		require.NoError(t, db.Model(&phoneModel{}).Where("id = 1").Update(column, nil).Error)
	}
	require.NoError(t, db.Model(&phoneModel{}).Where("id = 1").Update("expire_time", "unparseable").Error)
	read(path, 503, "Service unavailable|")
	require.Equal(t, 409, adminRequest(http.MethodPost, true, true).Code)
	require.NoError(t, db.Model(&accountModel{}).Where("id = 1").Update("deleted_at", *clock).Error)
	read(path, 404, "Invalid URL|")
}

func TestLatestSMSMessageUsesProviderTimeAtResponse(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		time string
		want bool
	}{
		{"at boundary", now.Add(-120 * time.Second).Format(time.RFC3339Nano), true},
		{"outside boundary", now.Add(-120*time.Second - time.Nanosecond).Format(time.RFC3339Nano), false},
		{"future", now.Add(time.Nanosecond).Format(time.RFC3339Nano), false},
		{"invalid", "provider-specific-time", false},
		{"missing", "", false},
		{"Shanghai", "2026-09-15 07:59:00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []MessageItem{{Content: "code", Time: tc.time}}
			require.Equal(t, tc.want, latestSMSMessage(messages, now, 2*time.Minute) != nil)
		})
	}
	messages := []MessageItem{{Content: "code", Time: now.Add(-119 * time.Second).Format(time.RFC3339)}}
	require.NotNil(t, latestSMSMessage(messages, now, 2*time.Minute))
	require.Nil(t, latestSMSMessage(messages, now.Add(2*time.Second), 2*time.Minute))
	require.Nil(t, latestSMSMessage([]MessageItem{{Content: " \n", Time: now.Format(time.RFC3339)}}, now, 2*time.Minute))
}
