package icloud

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestOnboardingDispatcherFinalizesInvalidWaitingTasks(t *testing.T) {
	for _, tc := range []struct{ status, dispatch, device, category string }{
		{"waiting", "waiting", "failed", "device_binding_failed"},
		{"waiting", "waiting", "", "phone_blacklisted"},
		{"unknown", "pending", "", "invalid_workflow_state"},
		{"processing", "unknown", "", "invalid_workflow_state"},
	} {
		t.Run(fmt.Sprintf("%s/%s/%s", tc.status, tc.dispatch, tc.category), func(t *testing.T) {
			s, db, manual, apple := newOnboardingStateTest(t)
			require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
			require.NoError(t, db.Model(manual).Updates(map[string]any{
				"stage": iCloudOnboardingStageFamilySharing, "onboarding_status": iCloudOnboardingWaiting, "dispatch_status": "waiting",
			}).Error)
			s.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
			content := []byte("美国区----否----failed@example.com----Secret1!----q1(a1)----q2(a2)----q3(a3)----2000-11-02----14155550002")
			batch, _, err := s.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, content, s.now().Add(time.Hour), "repair", "review", "/test")
			require.NoError(t, err)
			var task iCloudOnboardingTaskModel
			require.NoError(t, db.First(&task, batch.Tasks[0].ID).Error)
			category := ""
			if tc.category == "phone_blacklisted" {
				category = tc.category
			}
			require.NoError(t, db.Model(&task).Updates(map[string]any{
				"stage": "manage_prepare", "onboarding_status": tc.status, "dispatch_status": tc.dispatch,
				"device_bind_status": tc.device, "last_error_category": category, "kitesim_phone_id": 7,
			}).Error)
			if tc.device == "failed" {
				require.NoError(t, db.Create(&deviceBindingModel{Email: task.PrimaryEmail, ResourceID: &task.ID, PhoneID: 7, Status: "failed", LastError: "Previously failed binding."}).Error)
			}
			root := iCloudRootModel{Type: "icloud", OwnerUserID: 1, Version: 1, CreatedAt: s.now(), UpdatedAt: s.now()}
			require.NoError(t, db.Create(&root).Error)
			require.NoError(t, db.Create(&iCloudResourceModel{ID: root.ID, PrimaryEmail: "legacy@example.com", AccountRole: "unknown", Status: iCloudResourceNormal, ForSale: true}).Error)
			if tc.status == "unknown" {
				require.NoError(t, db.Model(&task).Update("resource_id", root.ID).Error)
			}
			redisServer := miniredis.RunT(t)
			s.queue = asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
			t.Cleanup(func() { _ = s.queue.Close() })
			// A manual wait preceding the failed task must not starve a one-item batch.
			for range 2 {
				require.NoError(t, s.DispatchICloudOnboardingTasks(context.Background(), 1))
			}
			require.NoError(t, db.First(&task, task.ID).Error)
			require.Equal(t, iCloudOnboardingFailed, task.Status)
			require.Equal(t, "failed", task.DispatchStatus)
			require.Equal(t, tc.category, task.LastErrorCategory)
			require.NotNil(t, task.FinishedAt)
			require.Equal(t, 1, task.Attempts)
			var failedResource iCloudResourceModel
			require.NoError(t, db.First(&failedResource, task.ID).Error)
			require.Equal(t, iCloudResourceAbnormal, failedResource.Status)
			var reservations int64
			require.NoError(t, db.Model(&iCloudAppleIDReservationModel{}).Where("email_key = ?", task.PrimaryEmail).Count(&reservations).Error)
			require.Zero(t, reservations)
			require.NoError(t, db.First(manual, manual.ID).Error)
			require.Equal(t, iCloudOnboardingWaiting, manual.Status)
			require.Equal(t, "waiting", manual.DispatchStatus)
			var legacy iCloudResourceModel
			require.NoError(t, db.First(&legacy, root.ID).Error)
			require.Equal(t, iCloudResourceNormal, legacy.Status)
			require.True(t, legacy.ForSale)
			require.Empty(t, apple.operations)
		})
	}
}

func TestFailedBatchMemberCanBeReimported(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_failure=%t", legacy), func(t *testing.T) {
			s, db, _, _ := newOnboardingStateTest(t)
			s.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
			first := "美国区----否----first-batch@example.com----Secret1!----q1(a1)----q2(a2)----q3(a3)----2000-11-02----14155550001----invite"
			second := "美国区----否----second-batch@example.com----Secret1!----q1(a1)----q2(a2)----q3(a3)----2000-11-02----14155550002----invite"
			ctx := context.Background()
			batch, _, err := s.AcceptAdminICloudOnboardingImport(ctx, 1, 1, []byte(strings.Join([]string{first, second}, "\n")), s.now().Add(time.Hour), "initial-batch", "review", "/test")
			require.NoError(t, err)
			var task iCloudOnboardingTaskModel
			require.NoError(t, db.First(&task, batch.Tasks[1].ID).Error)
			require.NotEqual(t, task.ID, *task.ImportID)
			require.NoError(t, db.Model(&task).Updates(map[string]any{
				"stage": "manage_profile", "dispatch_status": "running", "claim_token": "review", "family_reservation_confirmed": true,
			}).Error)
			require.NoError(t, db.First(&task, task.ID).Error)
			if legacy {
				require.NoError(t, db.Model(&task).Updates(map[string]any{
					"onboarding_status": iCloudOnboardingFailed, "dispatch_status": "failed", "claim_token": "",
					"status": iCloudResourceAbnormal, "for_sale": false,
				}).Error)
			} else {
				require.NoError(t, s.failICloudOnboardingTask(ctx, &task, "provider_rejected", "Apple rejected the request."))
			}
			reimport, _, err := s.AcceptAdminICloudOnboardingImport(ctx, 1, 1, []byte(second), s.now().Add(time.Hour), "reimport", "review", "/test")
			require.NoError(t, err)
			require.Equal(t, task.ID, reimport.Tasks[0].ID)
			var reservation iCloudAppleIDReservationModel
			require.NoError(t, db.Where("email_key = ?", task.PrimaryEmail).Take(&reservation).Error)
			require.Equal(t, reimport.ImportID, reservation.OwnerID)
			var sibling iCloudAppleIDReservationModel
			require.NoError(t, db.Where("email_key = ?", "first-batch@example.com").Take(&sibling).Error)
			require.Equal(t, batch.ImportID, sibling.OwnerID)
		})
	}
}

func TestDeviceRetrySupersedesOldOnboardingFailure(t *testing.T) {
	s, db, task, apple := newOnboardingStateTest(t)
	require.NoError(t, db.AutoMigrate(&deviceBindingModel{}))
	setDeviceTestSetting(t, runtimeconfig.ICloudDeviceAPIKey, "test-key")
	require.NoError(t, db.Model(task).Updates(map[string]any{
		"kitesim_phone_id": 7, "stage": "family_prepare", "family_invite_url": "invite", "dispatch_status": "running", "claim_token": "review",
	}).Error)
	ctx, now := context.Background(), s.now()
	_, err := s.EnsureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, now)
	require.NoError(t, err)
	var old deviceBindingModel
	require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&old).Error)
	require.NoError(t, s.finishDeviceBinding(ctx, old, "failed", "1", "", "Old binding failed."))
	require.NoError(t, db.First(task, task.ID).Error)
	require.NoError(t, s.ensureICloudOnboardingAppleIDReservation(ctx, task))
	calls, retried := 0, false
	s.now = func() time.Time {
		calls++
		if calls == 2 {
			retried = true
			result, err := s.ensureDeviceBinding(ctx, task.PrimaryEmail, "Secret1!", 7, task.BoundPhoneNumber, &task.ID, now, true, nil)
			require.NoError(t, err)
			require.Equal(t, "pending", result.Status)
		}
		return now
	}
	_, err = s.ensureOnboardingDevice(ctx, task, iCloudOnboardingSecret{Password: "Secret1!"})
	require.NoError(t, err)
	require.True(t, retried)
	require.NoError(t, db.First(task, task.ID).Error)
	require.Equal(t, uint64(2), task.Generation)
	require.Equal(t, iCloudOnboardingProcessing, task.Status)
	require.Equal(t, "pending", task.DispatchStatus)
	require.NotEmpty(t, task.SecretPayload)
	var current deviceBindingModel
	require.NoError(t, db.Where("email = ?", task.PrimaryEmail).Take(&current).Error)
	require.Equal(t, old.Generation+1, current.Generation)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "waiting", task.DispatchStatus)
	require.NoError(t, s.finishDeviceBinding(ctx, current, "success", "2", "https://devices.orangeid.top:56133/code", ""))
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "family_join_intent", task.Stage)
	require.Equal(t, []string{appleOnboardingPrepareFamily + ":"}, apple.operations)
}
