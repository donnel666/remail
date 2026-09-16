package icloud

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
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const familyDetailResponse = `{"currentDsid":"child","currentUserAppleId":"child@example.com","family":{"familyId":"family-1","organizerDsid":"owner"},"familyMembers":[{"dsid":"owner","appleId":"OWNER@example.com"},{"dsid":"child","appleId":"child@example.com"},{"dsid":"missing","appleId":"missing@example.com"},{"dsid":"deleted","appleId":"deleted@example.com"}],"isLinkedToFamily":true,"isMemberOfFamily":true}`

func newFamilyDetailsTest(t *testing.T) (*Service, *gorm.DB, *miniredis.Miniredis) {
	t.Helper()
	s, db, task, _ := newOnboardingStateTest(t)
	require.NoError(t, db.AutoMigrate(&iCloudResourceChannelModel{}))
	require.NoError(t, db.Model(task).Updates(map[string]any{"status": iCloudResourceNormal, "onboarding_status": iCloudOnboardingCompleted, "dispatch_status": "succeeded", "alias_count": 750}).Error)
	red := miniredis.RunT(t)
	s.deviceRedis = redis.NewClient(&redis.Options{Addr: red.Addr()})
	s.queue = asynq.NewClient(asynq.RedisClientOpt{Addr: red.Addr()})
	t.Cleanup(func() { _ = s.deviceRedis.Close(); _ = s.queue.Close() })
	return s, db, red
}

func runFamilyRefreshForTest(t *testing.T, s *Service) {
	t.Helper()
	token, err := s.deviceRedis.Get(context.Background(), familyRedisKey(1, "job")).Result()
	require.NoError(t, err)
	require.NoError(t, s.processFamilyRefresh(context.Background(), iCloudFamilyRefreshTask{ResourceID: 1, Token: token}))
}

func TestFamilyDetailsUseRedisAndLiveLocalCounts(t *testing.T) {
	s, db, red := newFamilyDetailsTest(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&iCloudResourceChannelModel{ResourceID: 1, Kind: iCloudChannelAppleAccount, Cookie: "myacinfo=existing", SessionStatus: iCloudSessionValid}).Error)
	require.NoError(t, db.Create(&iCloudResourceModel{ID: 2, PrimaryEmail: "owner@example.com", Status: iCloudResourceNormal}).Error)
	require.NoError(t, db.Create(&iCloudResourceModel{ID: 3, PrimaryEmail: "deleted@example.com", Status: iCloudResourceDeleted, AliasCount: 750}).Error)
	failed, requests := false, 0
	s.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		require.Equal(t, "myacinfo=existing", r.Header.Get("Cookie"))
		status := http.StatusOK
		if failed {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(familyDetailResponse)), Header: make(http.Header)}, nil
	})})
	view, err := s.RefreshAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "queued", view.State)
	_, err = s.RefreshAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: red.Addr()})
	defer inspector.Close()
	queued, err := inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	runFamilyRefreshForTest(t, s)
	view, err = s.GetAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "ready", view.State)
	require.Len(t, view.Members, 4)
	require.True(t, view.Members[0].Organizer)
	require.True(t, view.Members[1].Current)
	require.EqualValues(t, 0, *view.Members[0].AliasCount)
	require.EqualValues(t, 750, *view.Members[1].AliasCount)
	require.Nil(t, view.Members[2].AliasCount)
	require.Nil(t, view.Members[3].AliasCount)
	require.Equal(t, 2, view.ImportedCount)
	require.Equal(t, 1, view.FullCount)
	_, err = s.RefreshAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, requests)
	require.Empty(t, s.onboardingApple.(*onboardingFakeApple).operations)
	var resource iCloudResourceModel
	require.NoError(t, db.First(&resource, 1).Error)
	require.Empty(t, resource.FamilyID)
	require.Nil(t, resource.FamilySyncedAt)
	require.NoError(t, db.Model(&resource).Update("alias_count", 749).Error)
	view, err = s.GetAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 749, *view.Members[1].AliasCount)
	require.Zero(t, view.FullCount)
	previousSync := *view.SyncedAt
	later := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return later }
	failed = true
	_, err = s.RefreshAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	runFamilyRefreshForTest(t, s)
	view, err = s.GetAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "failed", view.State)
	require.Len(t, view.Members, 4)
	require.Equal(t, previousSync, *view.SyncedAt)
	require.NotEmpty(t, view.LastError)
}

type familyDeviceTestApple struct {
	t          *testing.T
	db         *gorm.DB
	stale      bool
	operations []string
}

func (a *familyDeviceTestApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	a.operations = append(a.operations, request.Operation)
	require.True(a.t, request.UseDeviceCode)
	require.True(a.t, request.SkipPrivateAlias)
	response := AppleOnboardingResponse{Session: json.RawMessage(`{"session":"family"}`), Next: "ready"}
	switch request.Operation {
	case appleOnboardingPrepareManage:
		response.Next = appleSMSManageLogin
	case appleOnboardingVerifySMS:
		require.Equal(a.t, "123456", request.Code)
	case appleOnboardingFetchManage:
	case appleOnboardingExportSession:
		response.NewChannel = &AppleOnboardingChannel{Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "myacinfo=recovered", APIKey: "api-key"}
		if a.stale {
			require.NoError(a.t, a.db.Model(&iCloudResourceModel{}).Where("id = 1").Update("credential_revision", 2).Error)
		}
	default:
		a.t.Fatalf("family refresh performed an unrelated Apple operation: %s", request.Operation)
	}
	return response, nil
}

func TestFamilyDeviceLoginWorksAt750AndFencesStaleCookies(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "full resource", true: "credentials changed"}[stale], func(t *testing.T) {
			s, db, _ := newFamilyDetailsTest(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "AppleID 登录验证码:123456")
			}))
			defer server.Close()
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
			s.device.http.Transport = server.Client().Transport
			require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = 1").Update("device_code_api", server.URL+"/api/free/v4/getcode?id=1").Error)
			require.NoError(t, db.Create(&iCloudResourceChannelModel{ResourceID: 1, Kind: iCloudChannelAppleAccount, Cookie: "myacinfo=old", SessionStatus: iCloudSessionInvalid}).Error)
			apple := &familyDeviceTestApple{t: t, db: db, stale: stale}
			s.onboardingApple = apple
			s.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Cookie") == "myacinfo=old" {
					return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
				}
				require.Equal(t, "myacinfo=recovered", r.Header.Get("Cookie"))
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(familyDetailResponse))}, nil
			})})
			view, err := s.RefreshAdminICloudFamily(context.Background(), 1)
			require.NoError(t, err)
			require.True(t, view.CanRefresh)
			runFamilyRefreshForTest(t, s)
			view, err = s.GetAdminICloudFamily(context.Background(), 1)
			require.NoError(t, err)
			var channel iCloudResourceChannelModel
			require.NoError(t, db.Where("resource_id = 1").Take(&channel).Error)
			if stale {
				require.Equal(t, "failed", view.State)
				require.Contains(t, view.LastError, "changed")
				require.Equal(t, "myacinfo=old", channel.Cookie)
			} else {
				require.Equal(t, "ready", view.State)
				require.Equal(t, "myacinfo=recovered", channel.Cookie)
			}
			require.Equal(t, []string{appleOnboardingPrepareManage, appleOnboardingVerifySMS, appleOnboardingFetchManage, appleOnboardingExportSession}, apple.operations)
			active, err := s.familyLoginActive(context.Background(), 1)
			require.NoError(t, err)
			require.False(t, active)
		})
	}
}

func TestFamilyRefreshRequiresCookieOrDeviceAndSharesLoginLease(t *testing.T) {
	s, db, _ := newFamilyDetailsTest(t)
	ctx := context.Background()
	view, err := s.RefreshAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	require.False(t, view.CanRefresh)
	require.NotEmpty(t, view.UnavailableReason)
	require.EqualValues(t, 0, s.deviceRedis.Exists(ctx, familyRedisKey(1, "job")).Val())
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = 1").Updates(map[string]any{"alias_count": 10, "device_code_api": "https://devices.orangeid.top:56133/api/free/v4/getcode?id=1"}).Error)
	require.NoError(t, s.deviceRedis.Set(ctx, familyRedisKey(1, "login"), "family-owner", time.Minute).Err())
	created, err := s.ensureICloudCookieRecoveryTx(ctx, db, 1)
	require.NoError(t, err)
	require.False(t, created)
	require.NoError(t, s.deviceRedis.Del(ctx, familyRedisKey(1, "login")).Err())
	created, err = s.ensureICloudCookieRecoveryTx(ctx, db, 1)
	require.NoError(t, err)
	require.True(t, created)
	var resource iCloudResourceModel
	require.NoError(t, db.First(&resource, 1).Error)
	require.True(t, resource.WorkflowNextAttemptAt.After(s.now()))
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err = s.refreshFamilySnapshot(waitCtx, iCloudFamilyRefreshTask{ResourceID: 1, Token: "family-request"}, resource, func(state string) error {
		if state == "waiting" {
			cancel()
		}
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, db.First(&resource, 1).Error)
	require.False(t, resource.WorkflowNextAttemptAt.After(s.now()))
	require.Empty(t, s.onboardingApple.(*onboardingFakeApple).operations)
}

func TestFamilyRefreshPermissionsAndStaleLease(t *testing.T) {
	s, _, _ := newFamilyDetailsTest(t)
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		method, path  string
		read, operate bool
		want          int
	}{
		{http.MethodGet, "/v1/admin/icloud/resources/1/family", false, false, http.StatusForbidden},
		{http.MethodGet, "/v1/admin/icloud/resources/1/family", true, false, http.StatusOK},
		{http.MethodPost, "/v1/admin/icloud/resources/1/family/refresh", true, false, http.StatusForbidden},
		{http.MethodPost, "/v1/admin/icloud/resources/1/family/refresh", true, true, http.StatusAccepted},
	} {
		router := gin.New()
		RegisterRoutes(router.Group("/v1"), &Module{Service: s}, middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 1, iamdomain.RoleAdmin, "admin@example.com", true
		}), &recordingICloudPermissionChecker{allowed: map[string]bool{"core:resource/read": test.read, "core:resource/operate": test.operate}})
		request := httptest.NewRequest(test.method, test.path, nil)
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
		request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
		request.Header.Set(middleware.CSRFHeaderName, "csrf")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, test.want, response.Code)
		require.NotContains(t, response.Body.String(), "Secret1!")
	}
	ctx := context.Background()
	require.NoError(t, s.deviceRedis.Set(ctx, familyRedisKey(1, "job"), "new-owner", time.Minute).Err())
	err := s.writeFamilyCache(ctx, iCloudFamilyRefreshTask{ResourceID: 1, Token: "old-owner"}, iCloudFamilyCache{State: "ready"}, true)
	require.ErrorIs(t, err, errICloudRefreshStale)
	require.Equal(t, "new-owner", s.deviceRedis.Get(ctx, familyRedisKey(1, "job")).Val())
}

type delayedFamilyDeviceApple struct {
	*familyDeviceTestApple
	redis *miniredis.Miniredis
}

func (p delayedFamilyDeviceApple) Execute(ctx context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if request.Operation == appleOnboardingExportSession {
		p.redis.FastForward(2 * time.Minute)
	}
	return p.familyDeviceTestApple.Execute(ctx, request)
}

func TestFamilyRefreshRenewsLeaseAfterQueueDelay(t *testing.T) {
	s, db, red := newFamilyDetailsTest(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "AppleID 登录验证码:123456")
	}))
	defer server.Close()
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
	s.device.http.Transport = server.Client().Transport
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = 1").Update("device_code_api", server.URL+"/api/free/v4/getcode?id=1").Error)
	s.onboardingApple = delayedFamilyDeviceApple{familyDeviceTestApple: &familyDeviceTestApple{t: t, db: db}, redis: red}
	s.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(familyDetailResponse))}, nil
	})})
	ctx := context.Background()
	_, err := s.RefreshAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	red.FastForward(9 * time.Minute)
	runFamilyRefreshForTest(t, s)
	view, err := s.GetAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "ready", view.State)
	require.Len(t, view.Members, 4)
	require.Empty(t, view.LastError)
	require.False(t, red.Exists(familyRedisKey(1, "job")))

	// An older queued job must not renew or finish a later request's lease.
	require.NoError(t, s.deviceRedis.Set(ctx, familyRedisKey(1, "job"), "new-owner", time.Minute).Err())
	require.NoError(t, s.processFamilyRefresh(ctx, iCloudFamilyRefreshTask{ResourceID: 1, Token: "old-owner"}))
	require.Equal(t, time.Minute, red.TTL(familyRedisKey(1, "job")))
	require.Equal(t, "new-owner", s.deviceRedis.Get(ctx, familyRedisKey(1, "job")).Val())
}

func TestFamilyRefreshEarlyFailurePreservesSnapshotAndAllowsRetry(t *testing.T) {
	s, db, red := newFamilyDetailsTest(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&iCloudResourceChannelModel{ResourceID: 1, Kind: iCloudChannelAppleAccount, Cookie: "myacinfo=existing"}).Error)
	syncedAt := s.now().Add(-time.Hour)
	cache := iCloudFamilyCache{Email: "child@example.com", State: "ready", SyncedAt: &syncedAt, Snapshot: &iCloudFamilySnapshot{FamilyID: "old-family", Members: []iCloudFamilyMember{{Email: "child@example.com"}}}}
	data, err := json.Marshal(cache)
	require.NoError(t, err)
	require.NoError(t, s.deviceRedis.Set(ctx, familyRedisKey(1, "view"), data, familySnapshotTTL).Err())
	_, err = s.RefreshAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	token := s.deviceRedis.Get(ctx, familyRedisKey(1, "job")).Val()
	fail := true
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("family_resource_read_failure", func(tx *gorm.DB) {
		if fail && tx.Statement.Table == "icloud_resources" {
			fail = false
			_ = tx.AddError(errors.New("temporary database read failure"))
		}
	}))
	require.Error(t, s.processFamilyRefresh(ctx, iCloudFamilyRefreshTask{ResourceID: 1, Token: token}))
	view, err := s.GetAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "failed", view.State)
	require.NotEmpty(t, view.LastError)
	require.Equal(t, "old-family", view.FamilyID)
	require.Equal(t, syncedAt, *view.SyncedAt)
	require.Len(t, view.Members, 1)
	require.False(t, red.Exists(familyRedisKey(1, "job")))
	_, err = s.RefreshAdminICloudFamily(ctx, 1)
	require.NoError(t, err)
	newToken := s.deviceRedis.Get(ctx, familyRedisKey(1, "job")).Val()
	require.NotEmpty(t, newToken)
	require.NotEqual(t, token, newToken)
	// A delayed cleanup or retry cannot clear the replacement job.
	require.ErrorIs(t, s.failFamilyRefresh(ctx, iCloudFamilyRefreshTask{ResourceID: 1, Token: token}, ErrICloudValidationTemp), errICloudRefreshStale)
	require.Equal(t, newToken, s.deviceRedis.Get(ctx, familyRedisKey(1, "job")).Val())
}
