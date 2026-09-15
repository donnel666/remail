package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/kitesim"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func setDeviceTestSetting(t *testing.T, key, value string) {
	t.Helper()
	old := runtimeconfig.String(key, "")
	runtimeconfig.Set(key, value)
	t.Cleanup(func() { runtimeconfig.Set(key, old) })
}

type deviceTestPhones struct {
	onboardingProvidedPhone
	reserves int
	checks   int
	messages []kitesim.MessageItem
}

func (p *deviceTestPhones) ReserveSMSChallenge(_ context.Context, id uint, _, _ string, expires time.Time) (kitesim.SMSReservation, error) {
	p.reserves++
	return kitesim.SMSReservation{ID: uint64(id), PhoneID: id, ExpiresAt: expires}, nil
}
func (p *deviceTestPhones) CheckSMSPhoneAvailable(context.Context, uint) error {
	p.checks++
	return errors.New("SMS phone must not be used after device binding")
}
func (p *deviceTestPhones) FetchSMSMessages(context.Context, uint) ([]kitesim.MessageItem, error) {
	return p.messages, nil
}

func TestDeviceBindingDelaySharedPollingAndPersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, db, task, _ := newOnboardingStateTest(t)
	require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
	clock := s.now()
	s.now = func() time.Time { return clock }
	phoneID := uint(7)
	require.NoError(t, db.Model(task).Updates(map[string]any{"kitesim_phone_id": phoneID, "stage": "family_prepare", "dispatch_status": "waiting", "onboarding_status": iCloudOnboardingWaiting}).Error)
	phones := &deviceTestPhones{}
	s.smsPhones = phones
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s.deviceRedis = rdb
	advance := func(d time.Duration) { clock = clock.Add(d); redisServer.FastForward(d) }
	imports := []map[string]any{}
	listCalls := 0
	status := "processing"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/outapi/Account/importData":
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, float64(919), payload["uid"])
			imports = append(imports, payload)
			_, _ = w.Write([]byte(`{"code":0,"msg":"导入成功"}`))
		case "/api/accounts":
			listCalls++
			require.Equal(t, "20", r.URL.Query().Get("limit"))
			items := []map[string]any{}
			for i, item := range imports {
				items = append(items, map[string]any{"id": i + 1, "account": item["account"], "tag": item["tag"], "result_status": status, "bind_uuid": "uuid", "bind_adi": "adi"})
			}
			more := r.URL.Query().Get("cursor") == ""
			cursor := ""
			if more {
				items = items[:1]
				cursor = "page-2"
			} else {
				items = items[1:]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"list": items, "has_more": more, "next_cursor": cursor}})
		case "/api/free/v4/getcode":
			_, _ = w.Write([]byte("AppleID登录验证码:000123"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	s.device.http.Transport = server.Client().Transport
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
	ctx := context.Background()
	first, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", phoneID, task.BoundPhoneNumber, &task.ID, clock)
	require.NoError(t, err)
	require.Equal(t, "pending", first.Status)
	_, err = s.EnsureDeviceBinding(ctx, "second@example.com", "other-password", 8, "14155550002", nil, clock)
	require.NoError(t, err)
	_, err = s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", phoneID, task.BoundPhoneNumber, &task.ID, clock)
	require.NoError(t, err)
	require.NoError(t, s.syncDeviceBindings(ctx))
	require.Empty(t, imports)
	advance(20 * time.Second)
	require.NoError(t, s.syncDeviceBindings(ctx))
	require.Empty(t, imports)
	advance(10 * time.Second)
	require.NoError(t, s.syncDeviceBindings(ctx))
	require.Len(t, imports, 2)
	require.Equal(t, 2, phones.reserves)
	var binding deviceBindingModel
	require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&binding).Error)
	require.Equal(t, "binding", binding.Status)
	require.Equal(t, uint64(1), binding.Generation)
	callback := "/sms/icloud-device/" + *binding.CallbackToken
	router := gin.New()
	RegisterDeviceCallbackRoutes(router, s, rdb)
	phones.messages = []kitesim.MessageItem{{Content: "old code 111111", Time: clock.Add(-time.Second).Format(time.RFC3339)}, {Content: "Apple code 000123", Time: clock.Format(time.RFC3339)}}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, callback, nil))
	require.Equal(t, 200, w.Code)
	require.True(t, strings.HasPrefix(w.Body.String(), "Apple code 000123|"))
	advance(5 * time.Second)
	require.NoError(t, s.syncDeviceBindings(ctx))
	require.Zero(t, listCalls)
	advance(5 * time.Second)
	require.NoError(t, s.syncDeviceBindings(ctx))
	require.Equal(t, 2, listCalls)
	var resource iCloudResourceModel
	require.NoError(t, db.First(&resource, task.ID).Error)
	require.Empty(t, resource.DeviceCodeAPI)
	status = "unknown"
	advance(10 * time.Second)
	require.NoError(t, s.syncDeviceBindings(ctx))
	require.NoError(t, db.First(&resource, task.ID).Error)
	require.Empty(t, resource.DeviceCodeAPI)
	status = "success"
	advance(10 * time.Second)
	require.NoError(t, s.syncDeviceBindings(ctx))
	require.Len(t, imports, 2)
	require.NoError(t, db.First(&resource, task.ID).Error)
	require.Equal(t, "success", resource.DeviceBindStatus)
	require.Contains(t, resource.DeviceCodeAPI, "/api/free/v4/getcode?id=")
	require.Equal(t, "pending", resource.WorkflowDispatchStatus)
	var completed deviceBindingModel
	require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&completed).Error)
	require.Empty(t, completed.Password)
	require.Nil(t, completed.CallbackToken)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, callback, nil))
	require.Equal(t, 404, w.Code)
	code, err := s.FetchDeviceCode(ctx, resource.DeviceCodeAPI)
	require.NoError(t, err)
	require.Equal(t, "000123", code)
	_, err = s.FetchDeviceCode(ctx, "https://untrusted.example/code")
	require.ErrorIs(t, err, errDeviceResponse)
	// A later Cookie workflow can retrieve the existing binding without another import.
	s = NewService(db, nil, nil, rdb)
	s.now = func() time.Time { return clock }
	ready, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", phoneID, task.BoundPhoneNumber, &task.ID, clock)
	require.NoError(t, err)
	require.Equal(t, resource.DeviceCodeAPI, ready.CodeAPI)
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = queue.Close() })
	s.queue = queue
	require.NoError(t, s.ScheduleDeviceBindingSync(ctx))
	require.NoError(t, s.ScheduleDeviceBindingSync(ctx))
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = inspector.Close() })
	info, err := inspector.GetTaskInfo("default", typeICloudDeviceSync)
	require.NoError(t, err)
	require.Equal(t, typeICloudDeviceSync, info.Type)
}

func TestDeviceCodeAuthenticationNeverDispatchesSMS(t *testing.T) {
	for _, purpose := range []string{appleSMSFamilyLogin, appleSMSManageLogin, appleSMSOldCookieLogin, appleSMSICloudCookieLogin} {
		t.Run(purpose, func(t *testing.T) {
			now := time.Now()
			session := &appleOnboardingScriptedSession{cookies: []msacl.SessionCookie{{Name: "myacinfo", Value: "session", Domain: "appleid.apple.com"}}, responses: []appleOnboardingScriptedResponse{{status: 200, body: `{"securityCode":{"valid":true}}`}}}
			client := appleOnboardingTestClient(now, session)
			state := appleOnboardingTestState(t, nil)
			_, err := client.Execute(context.Background(), AppleOnboardingRequest{Operation: appleOnboardingSendSMS, Session: state, UseDeviceCode: true, SMSPurpose: purpose})
			require.NoError(t, err)
			require.Empty(t, session.requests)
			_, err = client.Execute(context.Background(), AppleOnboardingRequest{Operation: appleOnboardingVerifySMS, Session: state, UseDeviceCode: true, SMSPurpose: purpose, Code: "000123"})
			require.NoError(t, err)
			require.Equal(t, []string{"POST https://idmsa.apple.com/appleauth/auth/verify/trusteddevice/securitycode"}, session.requests)
			require.Equal(t, map[string]any{"securityCode": map[string]any{"code": "000123"}}, session.requestBodies[0])
		})
	}
}

func TestDeviceBindingTimeoutRetryAndStaleResults(t *testing.T) {
	s, db, task, apple := newOnboardingStateTest(t)
	require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
	clock := s.now()
	s.now = func() time.Time { return clock }
	require.NoError(t, db.Model(task).Updates(map[string]any{"kitesim_phone_id": 7, "stage": "family_prepare"}).Error)
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
	ctx := context.Background()
	_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, clock)
	require.NoError(t, err)
	var old deviceBindingModel
	require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&old).Error)
	clock = clock.Add(11 * time.Minute)
	require.NoError(t, s.syncDeviceBindings(ctx))
	var expired deviceBindingModel
	require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&expired).Error)
	require.Equal(t, "failed", expired.Status)
	require.Empty(t, expired.Password)
	require.Nil(t, expired.CallbackToken)
	retry, err := s.ensureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, clock, true, nil)
	require.NoError(t, err)
	require.Equal(t, "pending", retry.Status)
	require.NoError(t, s.finishDeviceBinding(ctx, old, "success", "stale", "https://devices.orangeid.top:56133/api/free/v4/getcode?id=1", ""))
	var current deviceBindingModel
	require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&current).Error)
	require.Equal(t, old.Generation+1, current.Generation)
	require.Empty(t, current.CodeAPI)
	require.NotEqual(t, old.CallbackToken, current.CallbackToken)
	// Removing the API key after enrollment started must not re-enable SMS.
	runtimeconfig.Set(runtimeconfig.ICloudDeviceAPIKey, "")
	require.NoError(t, s.ProcessICloudOnboardingTask(ctx, iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}))
	require.Empty(t, apple.operations)
	require.NoError(t, db.First(task, task.ID).Error)
	require.Equal(t, "waiting", task.DispatchStatus)
	require.Equal(t, "pending", task.DeviceBindStatus)
}

func TestDeviceWorkflowCoversMultipleCodesAndCookieRecovery(t *testing.T) {
	for _, tc := range []struct{ kind, purpose, next string }{{"onboarding", appleSMSFamilyLogin, "family_join_intent"}, {"onboarding", appleSMSManageLogin, "manage_profile"}, {"refresh", appleSMSOldCookieLogin, "old_cookie_finish"}, {iCloudCookieRecoveryTaskKind, appleSMSManageLogin, "manage_profile"}} {
		t.Run(tc.kind+tc.purpose, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			phones := &deviceTestPhones{}
			s.smsPhones = phones
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "AppleID登录验证码:123456") }))
			defer server.Close()
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
			s.device.http.Transport = server.Client().Transport
			require.NoError(t, db.Model(task).Updates(map[string]any{"task_kind": tc.kind, "device_code_api": server.URL + "/code", "device_bind_status": "success", "pending_sms_purpose": tc.purpose, "stage": "sms_send", "dispatch_status": "running", "claim_token": "device-test"}).Error)
			require.NoError(t, db.First(task, task.ID).Error)
			for round := 0; round < 2; round++ {
				require.NoError(t, s.sendICloudOnboardingSMS(context.Background(), task, iCloudOnboardingSecret{}))
				require.NoError(t, db.Model(task).Updates(map[string]any{"dispatch_status": "running", "claim_token": "device-test"}).Error)
				require.NoError(t, db.First(task, task.ID).Error)
				require.NoError(t, s.waitICloudOnboardingSMS(context.Background(), task))
				require.NoError(t, db.Model(task).Updates(map[string]any{"dispatch_status": "running", "claim_token": "device-test"}).Error)
				require.NoError(t, db.First(task, task.ID).Error)
				require.NoError(t, s.verifyICloudOnboardingSMS(context.Background(), task, iCloudOnboardingSecret{}))
				require.NoError(t, db.First(task, task.ID).Error)
				require.Equal(t, tc.next, task.Stage)
				require.NoError(t, db.Model(task).Updates(map[string]any{"dispatch_status": "running", "claim_token": "device-test", "stage": "sms_send", "pending_sms_purpose": tc.purpose}).Error)
				require.NoError(t, db.First(task, task.ID).Error)
			}
			require.Zero(t, phones.reserves)
			require.Zero(t, phones.checks)
			ready, err := s.checkICloudOnboardingSMSPhone(context.Background(), task)
			require.NoError(t, err)
			require.True(t, ready)
			require.Zero(t, phones.checks)
		})
	}
	for i := 0; i < 100; i++ {
		delay := iCloudOnboardingStageDelay()
		require.GreaterOrEqual(t, delay, time.Second)
		require.LessOrEqual(t, delay, 30*time.Second)
	}
}

func TestLegacyResourcesKeepSMSWithDevicePlatformConfigured(t *testing.T) {
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "configured-for-new-accounts")
	for _, tc := range []struct{ kind, stage, status string }{
		{"onboarding", "family_prepare", ""},
		{"onboarding", "manage_prepare", ""},
		{"refresh", "old_cookie_prepare", ""},
		{iCloudCookieRecoveryTaskKind, "manage_prepare", ""},
		{"refresh", "old_cookie_prepare", "pending"},
		{iCloudCookieRecoveryTaskKind, "manage_prepare", "failed"},
	} {
		t.Run(tc.kind+"/"+tc.stage+"/"+tc.status, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			require.NoError(t, db.Model(task).Updates(map[string]any{"task_kind": tc.kind, "stage": tc.stage, "device_bind_status": tc.status, "kitesim_phone_id": 7, "family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=test", "dispatch_status": "running", "claim_token": "compatibility"}).Error)
			require.NoError(t, db.First(task, task.ID).Error)
			provider := &onboardingRequestApple{}
			s.onboardingApple = provider
			require.NoError(t, s.processICloudOnboardingStage(context.Background(), task))
			require.NotEmpty(t, provider.request.Operation, "unbound legacy accounts must reach normal Apple authentication")
			require.False(t, provider.request.UseDeviceCode)
		})
	}
}

func TestSavedDeviceAPISelectsDeviceWithoutBindingStatus(t *testing.T) {
	s, db, task, _ := newOnboardingStateTest(t)
	task.DeviceCodeAPI = "https://devices.orangeid.top:56133/api/free/v4/getcode?id=1"
	task.DeviceBindStatus = ""
	phones := &deviceTestPhones{}
	s.smsPhones = phones
	ready, err := s.ensureOnboardingDevice(context.Background(), task, iCloudOnboardingSecret{})
	require.NoError(t, err)
	require.True(t, ready)
	ready, err = s.checkICloudOnboardingSMSPhone(context.Background(), task)
	require.NoError(t, err)
	require.True(t, ready)
	require.Zero(t, phones.checks)
	provider := &onboardingRequestApple{}
	s.onboardingApple = provider
	_, err = s.executeICloudOnboardingApple(context.Background(), task, iCloudOnboardingSecret{}, AppleOnboardingRequest{Operation: appleOnboardingPrepareManage})
	require.NoError(t, err)
	require.True(t, provider.request.UseDeviceCode)
	require.NoError(t, db.Model(task).Update("device_code_api", task.DeviceCodeAPI).Error)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, "/test", nil)
		c.Params = gin.Params{{Key: "resourceId", Value: fmt.Sprint(task.ID)}}
		(&handler{service: s}).deviceBinding(c)
		require.Equal(t, http.StatusOK, w.Code)
		var view DeviceBinding
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
		require.Equal(t, "success", view.Status)
		require.Equal(t, task.DeviceCodeAPI, view.CodeAPI)
	}
}
