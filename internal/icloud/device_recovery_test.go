package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/kitesim"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeviceBindingResultAndWaitCannotLoseWakeup(t *testing.T) {
	for _, tc := range []struct {
		status string
		before bool
	}{{"success", true}, {"success", false}, {"failed", true}, {"failed", false}} {
		t.Run(fmt.Sprintf("%s_before_wait=%t", tc.status, tc.before), func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
			require.NoError(t, db.Model(task).Updates(map[string]any{"kitesim_phone_id": 7, "stage": "family_prepare", "dispatch_status": "running", "claim_token": "recovery"}).Error)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
			ctx, now := context.Background(), s.now()
			_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, now)
			require.NoError(t, err)
			var binding deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&binding).Error)
			require.NoError(t, db.First(task, task.ID).Error)
			finish := func() {
				api := ""
				if tc.status == "success" {
					api = "https://devices.orangeid.top:56133/api/free/v4/getcode?id=1"
				}
				require.NoError(t, s.finishDeviceBinding(ctx, binding, tc.status, "1", api, ""))
			}
			calls, fired := 0, false
			s.now = func() time.Time {
				calls++
				if tc.before && calls == 2 {
					fired = true
					finish()
				}
				return now
			}
			ready, err := s.ensureOnboardingDevice(ctx, task, iCloudOnboardingSecret{Password: "Secret1!"})
			require.NoError(t, err)
			require.False(t, ready)
			if tc.before {
				require.True(t, fired)
			} else {
				finish()
			}
			var row iCloudResourceModel
			require.NoError(t, db.First(&row, task.ID).Error)
			require.Equal(t, tc.status, row.DeviceBindStatus)
			require.Equal(t, "pending", row.WorkflowDispatchStatus)
			require.NotNil(t, row.WorkflowNextAttemptAt)
			if tc.status == "success" {
				require.True(t, row.WorkflowNextAttemptAt.After(now))
				require.False(t, row.WorkflowNextAttemptAt.After(now.Add(30*time.Second)))
			} else {
				require.True(t, row.WorkflowNextAttemptAt.Equal(now))
				processOnboardingStageForTest(t, s, db, task)
				require.Equal(t, iCloudOnboardingFailed, task.Status)
				require.Equal(t, "device_binding_failed", task.LastErrorCategory)
			}
		})
	}
}

type deviceRecoveryPhones struct {
	deviceTestPhones
	status    string
	markCalls int
	failMark  bool
	canceled  []uint64
}

func (p *deviceRecoveryPhones) ReserveSMSChallenge(ctx context.Context, id uint, purpose, owner string, expires time.Time) (kitesim.SMSReservation, error) {
	reservation, err := p.deviceTestPhones.ReserveSMSChallenge(ctx, id, purpose, owner, expires)
	reservation.Status = firstNonEmpty(p.status, kitesim.SMSChallengeReserved)
	return reservation, err
}

func (p *deviceRecoveryPhones) MarkSMSAttemptSent(context.Context, uint64) error {
	p.markCalls++
	if p.failMark {
		p.failMark = false
		return errors.New("temporary local marker failure")
	}
	if p.status == kitesim.SMSChallengeSent {
		return kitesim.ErrSMSChallengeInactive
	}
	p.status = kitesim.SMSChallengeSent
	return nil
}

func (p *deviceRecoveryPhones) CancelSMSChallenge(_ context.Context, id uint64) error {
	p.canceled = append(p.canceled, id)
	p.status = ""
	return nil
}

func TestDeviceImportRetriesOnlyBeforeSubmission(t *testing.T) {
	for _, failure := range []string{"local_marker", "resource_update", "unknown_remote_reply"} {
		t.Run(failure, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
			require.NoError(t, db.Model(task).Update("kitesim_phone_id", 7).Error)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
			imports := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/outapi/Account/importData" {
					imports++
					if failure == "unknown_remote_reply" {
						w.WriteHeader(500)
						return
					}
					_, _ = w.Write([]byte(`{"code":0}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"list":[],"has_more":false,"next_cursor":"","total":0}}`))
			}))
			t.Cleanup(server.Close)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
			s.device.http.Transport = server.Client().Transport
			phones := &deviceRecoveryPhones{failMark: failure == "local_marker"}
			s.smsPhones = phones
			ctx := context.Background()
			_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, s.now().Add(-time.Minute))
			require.NoError(t, err)
			failUpdate := failure == "resource_update"
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("fail_device_resource_once", func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]any)
				if failUpdate && ok && tx.Statement.Table == "icloud_resources" && updates["device_bind_status"] == "binding" {
					failUpdate = false
					_ = tx.AddError(errors.New("temporary resource update failure"))
				}
			}))
			err = s.syncDeviceBindings(ctx)
			if failure == "resource_update" {
				require.Error(t, err)
				require.False(t, failUpdate)
			} else {
				require.NoError(t, err)
			}
			if failure != "unknown_remote_reply" {
				require.Zero(t, imports)
				var pending deviceBindingModel
				require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&pending).Error)
				require.Equal(t, "pending", pending.Status)
				require.Nil(t, pending.SubmittedAt)
			}
			require.NoError(t, s.syncDeviceBindings(ctx))
			require.Equal(t, 1, imports)
			require.NoError(t, s.syncDeviceBindings(ctx))
			require.Equal(t, 1, imports, "an already submitted import must not be replayed")
			if failure == "resource_update" {
				require.Equal(t, 1, phones.markCalls, "reuse the sent reservation after the local transaction rolls back")
			}
		})
	}
}

func TestDeviceBindingCancellationRemainsRetryableAfterEnable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, succeededWhileDisabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("success_races_disable=%t", succeededWhileDisabled), func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
			require.NoError(t, db.Model(task).Update("kitesim_phone_id", 7).Error)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
			ctx := context.Background()
			_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, s.now())
			require.NoError(t, err)
			var snapshot deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&snapshot).Error)
			require.NoError(t, db.Model(task).Update("status", iCloudResourceDisabled).Error)
			if succeededWhileDisabled {
				require.NoError(t, s.finishDeviceBinding(ctx, snapshot, "success", "1", "https://devices.orangeid.top:56133/api/free/v4/getcode?id=1", ""))
			} else {
				require.NoError(t, s.syncDeviceBindings(ctx))
			}
			var canceled deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&canceled).Error)
			require.Equal(t, "failed", canceled.Status)
			require.Empty(t, canceled.Password)
			require.Nil(t, canceled.CallbackToken)
			require.NoError(t, db.Model(task).Update("status", iCloudResourceNormal).Error)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(method, "/test", nil)
				c.Params = gin.Params{{Key: "resourceId", Value: fmt.Sprint(task.ID)}}
				middleware.SetCurrentUser(c, 1, "admin", "admin@example.com", "session")
				(&handler{service: s}).deviceBinding(c)
				require.Equal(t, http.StatusOK, w.Code)
				var view DeviceBinding
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
				if method == http.MethodGet {
					require.Equal(t, "failed", view.Status)
				} else {
					require.Equal(t, "pending", view.Status)
				}
			}
			var retried deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&retried).Error)
			require.Equal(t, snapshot.Generation+1, retried.Generation)
			require.NotEqual(t, snapshot.CallbackToken, retried.CallbackToken)
		})
	}
}

func TestDeviceInitializationFailurePreservesAcceptedSMS(t *testing.T) {
	for _, purpose := range []string{appleSMSPhoneEnrollment, appleSMSICloudLogin} {
		t.Run(purpose, func(t *testing.T) {
			s, db, task, apple := newOnboardingStateTest(t)
			require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
			require.NoError(t, db.Model(task).Updates(map[string]any{
				"stage": "sms_verify", "kitesim_phone_id": 7, "pending_sms_purpose": purpose,
				"manual_verification_code": "123456", "session_payload": iCloudJSON(`{"flow":"before-verify"}`),
				"family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=test",
			}).Error)
			s.smsPhones = &onboardingSMSSuccessPhone{sentAt: s.now(), expiresAt: s.now().Add(2 * time.Minute)}
			failBinding := true
			bindingErr := errors.New("temporary binding table write failure")
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fail_device_initialization_once", func(tx *gorm.DB) {
				if failBinding && tx.Statement.Table == "icloud_device_bindings" {
					failBinding = false
					_ = tx.AddError(bindingErr)
				}
			}))
			processOnboardingStageForTest(t, s, db, task)
			require.Equal(t, "icloud_finish", task.Stage)
			require.JSONEq(t, `{"flow":"ok"}`, string(task.SessionPayload))
			require.Equal(t, purpose, task.PendingSMSPurpose)
			require.Empty(t, task.ManualVerificationCode)

			ctx := context.Background()
			payload := iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}
			require.ErrorIs(t, s.ProcessICloudOnboardingTask(ctx, payload), bindingErr)
			require.False(t, failBinding)
			require.NoError(t, db.First(task, task.ID).Error)
			require.Equal(t, "icloud_finish", task.Stage)
			require.JSONEq(t, `{"flow":"ok"}`, string(task.SessionPayload))
			require.Equal(t, []string{appleOnboardingVerifySMS + ":" + purpose}, apple.operations, "local initialization must finish before calling Apple again")

			require.NoError(t, s.ReleaseICloudOnboardingTask(ctx, payload, "temporary device initialization failure"))
			require.NoError(t, db.First(task, task.ID).Error)
			processOnboardingStageForTest(t, s, db, task)
			require.Equal(t, "family_prepare", task.Stage)
			require.Empty(t, task.PendingSMSPurpose)
			require.Equal(t, "pending", task.DeviceBindStatus)
			require.Equal(t, []string{appleOnboardingVerifySMS + ":" + purpose, appleOnboardingFinishICloud + ":"}, apple.operations)
			var binding deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&binding).Error)
			require.Equal(t, s.now().Add(deviceBindingDelay), binding.SubmitAt)
		})
	}
}

type phoneHoldRecoveryPhone struct {
	onboardingSMSSuccessPhone
	confirmedAt []time.Time
	owners      []string
}

func (p *phoneHoldRecoveryPhone) GetSMSChallengeByOwner(ctx context.Context, owner string) (kitesim.SMSChallenge, error) {
	p.owners = append(p.owners, owner)
	return p.onboardingSMSSuccessPhone.GetSMSChallengeByOwner(ctx, owner)
}

func (p *phoneHoldRecoveryPhone) ConfirmICloudPhoneBinding(_ context.Context, _ string, _ uint, confirmedAt time.Time) error {
	p.confirmedAt = append(p.confirmedAt, confirmedAt)
	if len(p.confirmedAt) <= 2 {
		return errors.New("temporary Redis failure")
	}
	return nil
}

func TestPhoneHoldRecoveryKeepsFirstVerificationTime(t *testing.T) {
	for _, purpose := range []string{appleSMSPhoneEnrollment, appleSMSICloudLogin} {
		t.Run(purpose, func(t *testing.T) {
			s, db, task, apple := newOnboardingStateTest(t)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "")
			confirmedAt := s.now()
			clock := confirmedAt
			s.now = func() time.Time { return clock }
			phone := &phoneHoldRecoveryPhone{onboardingSMSSuccessPhone: onboardingSMSSuccessPhone{sentAt: confirmedAt, expiresAt: confirmedAt.Add(time.Minute)}}
			s.smsPhones = phone
			require.NoError(t, db.Model(task).Updates(map[string]any{
				"stage": "sms_verify", "stage_attempts": 2, "kitesim_phone_id": 7,
				"pending_sms_purpose": purpose, "manual_verification_code": "123456",
				"family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=test",
			}).Error)
			payload := iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}
			require.ErrorIs(t, s.ProcessICloudOnboardingTask(context.Background(), payload), ErrICloudOnboardingTemporary)
			require.NoError(t, db.First(task, task.ID).Error)
			require.Equal(t, "icloud_finish", task.Stage)
			require.Empty(t, task.ManualVerificationCode)
			require.Equal(t, purpose, task.PendingSMSPurpose)
			require.JSONEq(t, `{"flow":"ok"}`, string(task.SessionPayload))
			clock = clock.Add(time.Hour)
			processOnboardingStageForTest(t, s, db, task)
			require.Equal(t, "icloud_finish", task.Stage)
			require.Equal(t, "pending", task.DispatchStatus)
			require.Zero(t, task.Attempts)
			require.Equal(t, []string{appleOnboardingVerifySMS + ":" + purpose}, apple.operations)
			clock = clock.Add(time.Hour)
			processOnboardingStageForTest(t, s, db, task)
			require.Equal(t, "family_prepare", task.Stage)
			require.Empty(t, task.PendingSMSPurpose)
			require.Equal(t, []time.Time{confirmedAt, confirmedAt, confirmedAt}, phone.confirmedAt)
			for _, owner := range phone.owners {
				require.Equal(t, fmt.Sprintf("icloud-onboarding:%d:%s:2", task.ID, purpose), owner)
			}
			require.Equal(t, []string{appleOnboardingVerifySMS + ":" + purpose, appleOnboardingFinishICloud + ":"}, apple.operations)
		})
	}
}

func TestDeviceImportRejectionAllowsExplicitRetry(t *testing.T) {
	for _, rejection := range []string{"nonzero_code", "unauthorized"} {
		t.Run(rejection, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
			require.NoError(t, db.Model(task).Update("kitesim_phone_id", 7).Error)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
			imports := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/outapi/Account/importData" {
					_, _ = w.Write([]byte(`{"code":0,"data":{"list":[],"has_more":false,"next_cursor":"","total":0}}`))
					return
				}
				imports++
				if imports == 1 {
					if rejection == "unauthorized" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					_, _ = w.Write([]byte(`{"code":1,"msg":"insufficient balance"}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":0}`))
			}))
			t.Cleanup(server.Close)
			setDeviceTestSetting(t, runtimeconfig.ICloudDeviceBaseURLKey, server.URL)
			s.device.http.Transport = server.Client().Transport
			phones := &deviceRecoveryPhones{}
			s.smsPhones = phones
			ctx := context.Background()
			_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, s.now().Add(-time.Minute))
			require.NoError(t, err)
			require.NoError(t, s.syncDeviceBindings(ctx))
			var failed deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&failed).Error)
			require.Equal(t, "failed", failed.Status)
			wantError := errDeviceImportRejected
			if rejection == "unauthorized" {
				wantError = errDeviceUnauthorized
			}
			require.Equal(t, wantError.Error(), failed.LastError)
			require.Empty(t, failed.Password)
			require.Nil(t, failed.CallbackToken)
			require.Equal(t, []uint64{failed.ChallengeID}, phones.canceled)
			require.NoError(t, db.First(task, task.ID).Error)
			require.Equal(t, "failed", task.DeviceBindStatus)
			require.NoError(t, s.syncDeviceBindings(ctx))
			var unchanged deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&unchanged).Error)
			require.Equal(t, failed.LastError, unchanged.LastError)
			require.Equal(t, 1, imports, "a rejection must wait for an explicit retry")

			retried, err := s.ensureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, s.now().Add(-time.Minute), true, nil)
			require.NoError(t, err)
			require.Equal(t, "pending", retried.Status)
			require.NoError(t, s.syncDeviceBindings(ctx))
			require.Equal(t, 2, imports)
			var current deviceBindingModel
			require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&current).Error)
			require.Equal(t, failed.Generation+1, current.Generation)
			require.Equal(t, "binding", current.Status)
			require.Empty(t, current.LastError)
		})
	}
}
