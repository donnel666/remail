package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/kitesim"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setDeviceTestSetting(t *testing.T, key, value string) {
	t.Helper()
	old := runtimeconfig.String(key, "")
	runtimeconfig.Set(key, value)
	t.Cleanup(func() { runtimeconfig.Set(key, old) })
}

type manualDeviceApple struct{ requests []AppleOnboardingRequest }

func (p *manualDeviceApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.requests = append(p.requests, request)
	response := AppleOnboardingResponse{Next: "ready", Session: json.RawMessage(`{"version":1}`)}
	switch request.Operation {
	case appleOnboardingPrepareFamily:
		response.Next = appleSMSFamilyLogin
	case appleOnboardingPrepareManage:
		response.Next = appleSMSManageLogin
	case appleOnboardingVerifySMS:
		if request.Code != "123456" {
			return AppleOnboardingResponse{}, errors.New("unexpected device code")
		}
	case appleOnboardingExport:
		response.NewChannel = &AppleOnboardingChannel{Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "myacinfo=device-cookie"}
	}
	return response, nil
}

func TestManualDeviceURLOnboardingWithoutPhone(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%t", compact), func(t *testing.T) {
			testManualDeviceURLOnboardingWithoutPhone(t, compact)
		})
	}
}

func testManualDeviceURLOnboardingWithoutPhone(t *testing.T, compact bool) {
	t.Helper()
	ctx := context.Background()
	service, db, _, _ := newOnboardingStateTest(t)
	require.NoError(t, db.AutoMigrate(&iCloudResourceChannelModel{}, &iCloudImportPreparationModel{}))
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	service.smsPhones = nil
	apple := &manualDeviceApple{}
	service.onboardingApple = apple
	service.device.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/code", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		require.Empty(t, r.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("AppleID 登录验证码:123456"))}, nil
	})
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "")
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, "https://devices.example")
	codeURL := "https://devices.example/code?id=1&token=secret-device-token"
	prefix := "美国区----否----device@example.com----Secret1!----q1(a1)----q2(a2)----q3(a3)----2000-11-02----"
	if compact {
		prefix = "device@example.com----Secret1!----"
		for _, invalid := range []string{"", "14155550001", "not-a-url"} {
			_, err := parseICloudOnboardingLine(1, prefix+invalid)
			require.ErrorIs(t, err, ErrICloudOnboardingInvalid)
		}
	} else {
		_, err := parseICloudOnboardingLine(1, strings.Replace(prefix+codeURL, "2000-11-02", "0001-01-01", 1))
		require.ErrorIs(t, err, ErrICloudOnboardingInvalid, "an explicit zero date must not become a missing birthday")
	}
	for _, invalid := range []string{
		strings.Replace(codeURL, "https:", "http:", 1), "https://other.example/code",
		codeURL + "#fragment", strings.Replace(codeURL, "https://", "https://user:password@", 1),
		strings.Replace(codeURL, "/code", "/co\rde", 1), codeURL + strings.Repeat("x", 2048),
	} {
		_, err := parseICloudOnboardingLine(1, prefix+invalid+"----invite")
		require.ErrorIs(t, err, ErrICloudOnboardingInvalid)
	}
	view, reused, err := service.AcceptAdminICloudOnboardingImport(ctx, 1, 1, []byte(prefix+codeURL+"----invite"), service.now().Add(time.Hour), "device-import", "request", "/test")
	require.NoError(t, err)
	require.False(t, reused)
	task := &iCloudOnboardingTaskModel{}
	require.NoError(t, db.First(task, view.Tasks[0].ID).Error)
	require.Equal(t, "family_prepare", task.Stage)
	require.Equal(t, codeURL, task.DeviceCodeAPI)
	require.Equal(t, "success", task.DeviceBindStatus)
	if compact {
		var missingBirthday int64
		require.NoError(t, db.Model(&iCloudResourceCredentialModel{}).Where("resource_id = ? AND birthday IS NULL", task.ID).Count(&missingBirthday).Error)
		require.EqualValues(t, 1, missingBirthday)
	}
	require.Empty(t, task.BoundPhoneNumber)
	require.Nil(t, task.KitesimPhoneID)
	payload, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "secret-device-token")
	for _, stage := range []string{"sms_send", "sms_wait", "sms_verify", "family_join_intent", "family_join_apply", iCloudOnboardingStageFamilySharing} {
		processOnboardingStageForTest(t, service, db, task)
		require.Equal(t, stage, task.Stage)
	}
	require.NoError(t, service.ConfirmICloudOnboardingFamilyReset(ctx, task.ID, 1, "confirm", "/test"))
	require.NoError(t, db.First(task, task.ID).Error)
	for _, stage := range []string{"sms_send", "sms_wait", "sms_verify", "manage_profile", "forwarding_prepare"} {
		processOnboardingStageForTest(t, service, db, task)
		require.Equal(t, stage, task.Stage)
	}
	now, operator := service.now(), uint(1)
	preparation := iCloudImportPreparationModel{OperatorUserID: &operator, ForwardToEmail: "relay@example.com", VerificationCode: "654321", VerifiedAt: &now, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&preparation).Error)
	require.NoError(t, db.Model(task).Updates(map[string]any{"stage": "resource_import", "selected_forward_to": preparation.ForwardToEmail, "forward_preparation_id": preparation.ID}).Error)
	require.NoError(t, db.First(task, task.ID).Error)
	processOnboardingStageForTest(t, service, db, task)
	require.Equal(t, iCloudOnboardingCompleted, task.Status)
	require.Equal(t, codeURL, task.DeviceCodeAPI)
	if compact {
		var credential iCloudResourceCredentialModel
		require.NoError(t, db.First(&credential, task.ID).Error)
		require.True(t, credential.Birthday.IsZero())
		_, err := credential.onboardingSecret("")
		require.ErrorIs(t, err, ErrICloudOnboardingInvalid, "SMS accounts must still require their original metadata")
	}
	require.NoError(t, db.Model(&iCloudResourceChannelModel{}).Where("resource_id = ?", task.ID).Update("session_status", iCloudSessionInvalid).Error)
	require.NoError(t, service.EnsureICloudCookieRefresh(ctx, task.ID))
	require.NoError(t, db.First(task, task.ID).Error)
	for _, stage := range []string{"sms_send", "sms_wait", "sms_verify", "manage_profile", "resource_refresh", "completed"} {
		processOnboardingStageForTest(t, service, db, task)
		require.Equal(t, stage, task.Stage)
	}
	if compact {
		require.NoError(t, db.Model(&iCloudResourceChannelModel{}).Where("resource_id = ?", task.ID).Update("session_status", iCloudSessionInvalid).Error)
		created := false
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			var err error
			created, err = service.ensureICloudCookieRecoveryTx(ctx, tx, task.ID)
			return err
		}))
		require.True(t, created)
		require.NoError(t, db.First(task, task.ID).Error)
		require.Equal(t, iCloudCookieRecoveryTaskKind, task.TaskKind)
	}
	for _, request := range apple.requests {
		require.True(t, request.UseDeviceCode)
		require.True(t, request.SkipPhoneEnrollment)
		require.Empty(t, request.PhoneNumber)
		require.NotEqual(t, appleOnboardingPrepareICloud, request.Operation)
		require.NotEqual(t, appleOnboardingSendSMS, request.Operation)
	}
	view, reused, err = service.AcceptAdminICloudOnboardingImport(ctx, 1, 1, []byte(prefix+codeURL+"----invite"), now.Add(time.Hour), "device-import", "request", "/test")
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, task.ID, view.Tasks[0].ID)
	view, _, err = service.AcceptAdminICloudOnboardingImport(ctx, 1, 1, []byte(strings.Replace(prefix, "device@example.com", "primary-device@example.com", 1)+codeURL), now.Add(time.Hour), "primary-device-import", "request", "/test")
	require.NoError(t, err)
	require.Equal(t, "manage_prepare", view.Tasks[0].Stage)
	primaryID := view.Tasks[0].ID
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = ?", primaryID).Updates(map[string]any{
		"device_code_api": "", "device_bind_status": "failed", "bound_phone_number": "14155550001", "bound_phone_source": "manual",
		"onboarding_status": iCloudOnboardingFailed, "dispatch_status": "failed",
	}).Error)
	view, _, err = service.AcceptAdminICloudOnboardingImport(ctx, 1, 1, []byte(strings.Replace(prefix, "device@example.com", "primary-device@example.com", 1)+codeURL), now.Add(time.Hour), "retry-device-import", "request", "/test")
	require.NoError(t, err)
	require.Equal(t, primaryID, view.Tasks[0].ID)
	task = &iCloudOnboardingTaskModel{}
	require.NoError(t, db.First(task, primaryID).Error)
	processOnboardingStageForTest(t, service, db, task)
	require.Equal(t, "sms_send", task.Stage)
	require.Nil(t, task.KitesimPhoneID)
	require.NoError(t, db.Model(task).Updates(map[string]any{"stage": "resource_import", "icloud_opened": true}).Error)
	require.NoError(t, db.First(task, primaryID).Error)
	processOnboardingStageForTest(t, service, db, task)
	require.Equal(t, "icloud_cookie_prepare", task.Stage)
	require.Equal(t, "old_cookie_missing", task.LastErrorCategory)
}

func TestDeviceURLImportPreservesPhoneOnboarding(t *testing.T) {
	service, db, _, apple := newOnboardingStateTest(t)
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "configured-for-new-accounts")
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, "https://devices.example")
	codeURL := "https://devices.example/code?id=1"
	entries := []struct {
		email, contact, invite, phone, stage, role string
		compact                                    bool
	}{
		{"phone-child@example.com", "+1 (415) 555-0001", "----invite", "14155550001", "accepted", "child", false},
		{"device-child@example.com", codeURL, "----invite", "", "family_prepare", "child", false},
		{"phone-primary@example.com", "14155550002", "", "14155550002", "accepted", "primary", false},
		{"device-primary@example.com", codeURL, "", "", "manage_prepare", "primary", false},
		{"compact-child@example.com", codeURL, "----invite", "", "family_prepare", "child", true},
		{"compact-primary@example.com", codeURL, "", "", "manage_prepare", "primary", true},
	}
	lines := make([]string, len(entries))
	for i, entry := range entries {
		lines[i] = "美国区----否----" + entry.email + "----Secret1!----q1(a1)----q2(a2)----q3(a3)----2000-11-02----" + entry.contact + entry.invite
		if entry.compact {
			lines[i] = entry.email + "----Secret1!----" + entry.contact + entry.invite
		}
	}
	now := service.now()
	view, _, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, []byte(strings.Join(lines, "\n")), now.Add(time.Hour), "mixed-import", "request", "/test")
	require.NoError(t, err)
	require.Len(t, view.Tasks, len(entries))
	for i, entry := range entries {
		var task iCloudOnboardingTaskModel
		require.NoError(t, db.First(&task, view.Tasks[i].ID).Error)
		require.Equal(t, entry.phone, task.BoundPhoneNumber)
		require.Equal(t, entry.stage, task.Stage)
		require.Equal(t, entry.role, task.AccountRole)
		if entry.phone != "" {
			require.Empty(t, task.DeviceCodeAPI)
			require.Empty(t, task.DeviceBindStatus)
		} else {
			require.Equal(t, codeURL, task.DeviceCodeAPI)
		}
	}
	phoneTask := &iCloudOnboardingTaskModel{}
	require.NoError(t, db.First(phoneTask, view.Tasks[0].ID).Error)
	processOnboardingStageForTest(t, service, db, phoneTask)
	require.Equal(t, "sms_send", phoneTask.Stage)
	require.Equal(t, appleSMSPhoneEnrollment, phoneTask.PendingSMSPurpose)
	require.NotNil(t, phoneTask.KitesimPhoneID)
	require.Equal(t, []string{appleOnboardingPrepareICloud + ":"}, apple.operations)
	require.NoError(t, db.Model(phoneTask).Updates(map[string]any{
		"onboarding_status": iCloudOnboardingFailed, "dispatch_status": "failed",
		"device_code_api": codeURL, "device_bind_status": "success", "device_account_id": "original-device",
	}).Error)
	retry, _, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, []byte(lines[0]), now.Add(time.Hour), "phone-retry", "request", "/test")
	require.NoError(t, err)
	require.Equal(t, phoneTask.ID, retry.Tasks[0].ID)
	require.NoError(t, db.First(phoneTask, phoneTask.ID).Error)
	require.Equal(t, "accepted", phoneTask.Stage)
	require.Equal(t, "14155550001", phoneTask.BoundPhoneNumber)
	require.NotNil(t, phoneTask.KitesimPhoneID)
	require.Equal(t, codeURL, phoneTask.DeviceCodeAPI)
	require.Equal(t, "success", phoneTask.DeviceBindStatus)
	require.Equal(t, "original-device", phoneTask.DeviceAccountID)
	var oldCredential iCloudResourceCredentialModel
	require.NoError(t, db.First(&oldCredential, phoneTask.ID).Error)
	require.NoError(t, db.Model(phoneTask).Updates(map[string]any{
		"onboarding_status": iCloudOnboardingFailed, "dispatch_status": "failed", "icloud_opened": true,
	}).Error)
	retry, _, err = service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, []byte("phone-child@example.com----NewPassword!----"+codeURL+"----invite"), now.Add(time.Hour), "compact-retry", "request", "/test")
	require.NoError(t, err)
	require.Equal(t, phoneTask.ID, retry.Tasks[0].ID)
	require.NoError(t, db.First(phoneTask, phoneTask.ID).Error)
	require.Equal(t, "family_prepare", phoneTask.Stage)
	require.Equal(t, "美国区", phoneTask.Region)
	require.Equal(t, "US", phoneTask.CountryCode)
	require.True(t, phoneTask.ICloudOpened)
	var newCredential iCloudResourceCredentialModel
	require.NoError(t, db.First(&newCredential, phoneTask.ID).Error)
	require.Equal(t, "NewPassword!", newCredential.ApplePassword)
	require.Equal(t, oldCredential.Birthday, newCredential.Birthday)
	require.JSONEq(t, string(oldCredential.SecurityAnswers), string(newCredential.SecurityAnswers))
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
	pending, err := inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 1, "multiple callers share one pending device poll")
	require.Equal(t, typeICloudDeviceSync, pending[0].Type)
	require.NotEqual(t, typeICloudDeviceSync, pending[0].ID)
	previousID := pending[0].ID
	require.NoError(t, inspector.ArchiveTask(platform.QueueDefault, previousID))
	advance(iCloudDispatcherTaskTimeout + time.Second)
	require.NoError(t, s.ScheduleDeviceBindingSync(ctx))
	pending, err = inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 1, "an archived poll cannot permanently block scheduling")
	require.NotEqual(t, previousID, pending[0].ID)
	archived, err := inspector.GetTaskInfo(platform.QueueDefault, previousID)
	require.NoError(t, err)
	require.Equal(t, asynq.TaskStateArchived, archived.State)
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

func TestDeviceCodeUsesSMSCodeExtraction(t *testing.T) {
	for _, tc := range []struct{ body, code string }{
		{"AppleID 登录验证码:000123", "000123"},
		{"AppleID登录验证码:123456", "123456"},
		{"Your verification code is 234567.", "234567"},
		{"345678", "345678"},
		{"AppleID 登录验证码:1234567", ""},
		{"No message", ""},
	} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, tc.body) }))
			t.Cleanup(server.Close)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
			s := &Service{device: newDeviceClient()}
			s.device.http.Transport = server.Client().Transport
			code, err := s.FetchDeviceCode(context.Background(), server.URL)
			if tc.code == "" {
				require.ErrorIs(t, err, errDeviceNoCode)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.code, code)
		})
	}
}

func TestDeviceOnboardingStillWaitsForManualFamilySharing(t *testing.T) {
	s, db, task, apple := newOnboardingStateTest(t)
	require.NoError(t, db.Model(task).Updates(map[string]any{
		"device_code_api": "https://devices.orangeid.top:56133/code", "device_bind_status": "success",
		"stage": "family_join_apply", "family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=test",
	}).Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, iCloudOnboardingStageFamilySharing, task.Stage)
	require.Equal(t, "waiting", task.DispatchStatus)
	require.Nil(t, task.NextAttemptAt)
	require.Equal(t, []string{appleOnboardingJoinFamily + ":"}, apple.operations)
	// A queued wakeup must not advance an unconfirmed manual checkpoint.
	require.NoError(t, db.Model(task).Update("dispatch_status", "pending").Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, iCloudOnboardingStageFamilySharing, task.Stage)
	require.Equal(t, "waiting", task.DispatchStatus)
	require.Len(t, apple.operations, 1)
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

func TestDeviceBindingFailureMarksOnboardingFailed(t *testing.T) {
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
	for _, role := range []string{"primary", "child"} {
		for _, beforeWait := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/failure_before_wait=%t", role, beforeWait), func(t *testing.T) {
				s, db, task, apple := newOnboardingStateTest(t)
				require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
				stage := "family_prepare"
				if role == "primary" {
					stage = "manage_prepare"
				}
				require.NoError(t, db.Model(task).Updates(map[string]any{
					"account_role": role, "stage": stage, "kitesim_phone_id": 7,
				}).Error)
				ctx := context.Background()
				_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, s.now())
				require.NoError(t, err)
				var binding deviceBindingModel
				require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&binding).Error)
				if !beforeWait {
					processOnboardingStageForTest(t, s, db, task)
					require.Equal(t, "waiting", task.DispatchStatus)
				}
				require.NoError(t, s.finishDeviceBinding(ctx, binding, "failed", "1", "", "Device binding failed."))
				processOnboardingStageForTest(t, s, db, task)
				require.Equal(t, iCloudOnboardingFailed, task.Status)
				require.Equal(t, "failed", task.DispatchStatus)
				require.Equal(t, "device_binding_failed", task.LastErrorCategory)
				require.Equal(t, "Device binding failed.", task.LastSafeError)
				require.NotNil(t, task.FinishedAt)
				require.Nil(t, task.NextAttemptAt)
				require.Empty(t, apple.operations)
				var resource iCloudResourceModel
				require.NoError(t, db.First(&resource, task.ID).Error)
				require.Equal(t, iCloudResourceAbnormal, resource.Status)
				batch, err := s.GetAdminICloudOnboardingImport(ctx, *task.ImportID)
				require.NoError(t, err)
				require.Equal(t, 1, batch.Failed)
				require.Zero(t, batch.Waiting)
			})
		}
	}
}

func TestDeviceBindingUnknownOrInvalidResultFails(t *testing.T) {
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
	for _, status := range []string{"unknown", "", "success"} {
		t.Run("status="+status, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
			require.NoError(t, db.Model(task).Updates(map[string]any{"kitesim_phone_id": 7, "stage": "family_prepare"}).Error)
			ctx := context.Background()
			_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, s.now())
			require.NoError(t, err)
			var binding deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&binding).Error)
			require.NoError(t, db.Model(&binding).Update("status", "binding").Error)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/accounts", r.URL.Path)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
					"list": []map[string]any{{"id": 1, "account": task.PrimaryEmail, "tag": deviceBindingTag(binding), "result_status": status}},
				}})
			}))
			t.Cleanup(server.Close)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
			s.device.http.Transport = server.Client().Transport
			require.NoError(t, s.syncDeviceBindings(ctx))
			processOnboardingStageForTest(t, s, db, task)
			require.Equal(t, "failed", task.DeviceBindStatus)
			require.Equal(t, iCloudOnboardingFailed, task.Status)
			require.Equal(t, "device_binding_failed", task.LastErrorCategory)
			require.NotEmpty(t, task.LastSafeError)
		})
	}
}

func TestDeviceWorkflowCoversMultipleCodesAndCookieRecovery(t *testing.T) {
	for _, tc := range []struct{ kind, purpose, next string }{{"onboarding", appleSMSFamilyLogin, "family_join_intent"}, {"onboarding", appleSMSManageLogin, "manage_profile"}, {"refresh", appleSMSOldCookieLogin, "old_cookie_finish"}, {iCloudCookieRecoveryTaskKind, appleSMSManageLogin, "manage_profile"}} {
		t.Run(tc.kind+tc.purpose, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			phones := &deviceTestPhones{}
			s.smsPhones = phones
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "AppleID 登录验证码:123456") }))
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
