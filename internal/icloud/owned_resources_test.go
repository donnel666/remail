package icloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestOwnedICloudImportOnlyRunsPhaseThree(t *testing.T) {
	s, db, _, apple := newOnboardingStateTest(t)
	s.SetImportOwnerValidator(func(_ context.Context, id uint) (bool, error) { return id == 7, nil })
	s.smsPhones = nil // Console imports must never require the platform phone pool.
	now := s.now()
	content := []byte("\uFEFFconsole@example.com----Secret!----https://codes.example.com/token")
	view, reused, err := s.AcceptOwnedICloudImport(context.Background(), 7, content, "console-import", "req", "/imports")
	require.NoError(t, err)
	require.False(t, reused)
	require.Len(t, view.Tasks, 1)
	var task iCloudOnboardingTaskModel
	require.NoError(t, db.First(&task, view.Tasks[0].ID).Error)
	require.Equal(t, "manage_prepare", task.Stage)
	require.Equal(t, iCloudUserImportTaskKind, task.TaskKind)
	require.False(t, task.ICloudOpened)
	require.Equal(t, now.Add(30*24*time.Hour), task.ExpireAt)
	var root iCloudRootModel
	require.NoError(t, db.First(&root, task.ID).Error)
	require.EqualValues(t, 7, root.OwnerUserID)

	s.now = func() time.Time { return now.Add(time.Minute) }
	replay, reused, err := s.AcceptOwnedICloudImport(context.Background(), 7, content, "console-import", "req2", "/imports")
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, view.ImportID, replay.ImportID)
	require.NoError(t, db.First(&task, task.ID).Error)
	require.Equal(t, now.Add(30*24*time.Hour), task.ExpireAt)
	_, _, err = s.AcceptOwnedICloudImport(context.Background(), 7, []byte("other@example.com----Secret!----https://codes.example.com/token"), "console-import", "req", "/imports")
	require.ErrorIs(t, err, ErrICloudOnboardingConflict)

	require.NoError(t, db.AutoMigrate(&iCloudResourceChannelModel{}, &iCloudImportPreparationModel{}))
	ownerID := uint(7)
	preparation := iCloudImportPreparationModel{OperatorUserID: &ownerID, ForwardToEmail: "relay@example.com", VerificationCode: "654321", VerifiedAt: &now, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&preparation).Error)
	require.NoError(t, db.Model(&task).Updates(map[string]any{"forward_preparation_id": preparation.ID, "selected_forward_to": preparation.ForwardToEmail}).Error)
	for steps := 0; steps < 12 && task.Status != iCloudOnboardingCompleted; steps++ {
		require.Equal(t, 3, iCloudOnboardingTaskPhase(&task), "unexpected stage: %s", task.Stage)
		if task.NextAttemptAt != nil {
			next := task.NextAttemptAt.Add(time.Second)
			s.now = func() time.Time { return next }
		}
		if task.Stage == "resource_import" {
			s.onboardingApple = &onboardingExportApple{}
		}
		processOnboardingStageForTest(t, s, db, &task)
	}
	require.Equal(t, iCloudOnboardingCompleted, task.Status)
	require.Equal(t, []string{appleOnboardingPrepareManage + ":", appleOnboardingFetchManage + ":", appleOnboardingAddForward + ":", appleOnboardingVerifyForward + ":"}, apple.operations)
	var resource iCloudResourceModel
	require.NoError(t, db.First(&resource, task.ID).Error)
	require.Equal(t, iCloudUserImportTaskKind, resource.WorkflowTaskKind)
	require.False(t, resource.ForSale)
	require.Equal(t, preparation.ForwardToEmail, resource.SelectedForwardTo)
	require.NotNil(t, resource.NextValidationAt)
	var channels []iCloudResourceChannelModel
	require.NoError(t, db.Where("resource_id = ?", task.ID).Find(&channels).Error)
	require.Len(t, channels, 1)
	require.Equal(t, iCloudChannelAppleAccount, channels[0].Kind)
}

func TestOwnedICloudImportUsesItsOwnTaskType(t *testing.T) {
	s, db, adminTask, _ := newOnboardingStateTest(t)
	s.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	options := asynq.RedisClientOpt{Addr: miniredis.RunT(t).Addr()}
	s.queue = asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() { _ = inspector.Close(); _ = s.queue.Close() })
	view, _, err := s.AcceptOwnedICloudImport(context.Background(), 7,
		[]byte("user@example.com----secret----https://codes.example.com/code"), "task-type", "req", "/imports")
	require.NoError(t, err)
	require.NoError(t, s.DispatchICloudOnboardingTasks(context.Background(), 100))
	tasks, err := inspector.ListScheduledTasks(platform.QueueBackgroundICloudValidation)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	byResource := make(map[uint]string)
	for _, task := range tasks {
		var payload iCloudOnboardingTask
		require.NoError(t, json.Unmarshal(task.Payload, &payload))
		byResource[payload.TaskID] = task.Type
	}
	require.Equal(t, typeICloudUserImport, byResource[view.Tasks[0].ID])
	require.Equal(t, typeICloudOnboarding, byResource[adminTask.ID])
	var userTask iCloudOnboardingTaskModel
	require.NoError(t, db.First(&userTask, view.Tasks[0].ID).Error)
	require.Equal(t, iCloudUserImportTaskKind, userTask.TaskKind)
	mux := asynq.NewServeMux()
	stop := RegisterTaskHandlers(mux, s)
	t.Cleanup(func() { stop(context.Background()) })
	payload, err := json.Marshal(iCloudOnboardingTask{TaskID: userTask.ID, Generation: userTask.Generation})
	require.NoError(t, err)
	require.NoError(t, mux.ProcessTask(context.Background(), asynq.NewTask(typeICloudUserImport, payload)))
	require.NoError(t, db.First(&userTask, userTask.ID).Error)
	require.Equal(t, "manage_profile", userTask.Stage)
}

func TestOwnedICloudDeviceLoginStaysInPhaseThree(t *testing.T) {
	s, db, task, _ := newOnboardingStateTest(t)
	s.smsPhones = nil
	apple := &manualDeviceApple{}
	s.onboardingApple = apple
	s.device.publicHTTP.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		require.Empty(t, request.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("123456"))}, nil
	})
	require.NoError(t, db.Model(task).Updates(map[string]any{
		"task_kind": iCloudUserImportTaskKind, "stage": "manage_prepare", "device_code_api": "https://codes.example.com/code",
	}).Error)
	for _, next := range []string{"sms_send", "sms_wait", "sms_verify", "manage_profile", "forwarding_prepare"} {
		processOnboardingStageForTest(t, s, db, task)
		require.Equal(t, next, task.Stage)
		require.Equal(t, iCloudUserImportTaskKind, task.TaskKind)
		require.Equal(t, 3, iCloudOnboardingTaskPhase(task))
	}
	require.Len(t, apple.requests, 3)
	require.Equal(t, appleOnboardingPrepareManage, apple.requests[0].Operation)
	require.Equal(t, appleOnboardingVerifySMS, apple.requests[1].Operation)
	require.Equal(t, appleOnboardingFetchManage, apple.requests[2].Operation)
}

func TestOwnedICloudImportRejectsUnsupportedInputs(t *testing.T) {
	s, _, _, _ := newOnboardingStateTest(t)
	s.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	tooMany := ""
	for index := 0; index < 101; index++ {
		tooMany += fmt.Sprintf("user%d@example.com----pw----https://codes.example.com/%d\n", index, index)
	}
	for _, content := range []string{
		"", "a@example.com----pw", "a@example.com----pw----12345678900",
		"a@example.com----pw----https://codes.example.com/code----invite",
		"美国区----否----a@example.com----pw----问题?(a)----问题?(a)----问题?(a)----2000-01-01----14155550001",
		"a@example.com----pw----http://127.0.0.1/code", "a@example.com----pw----http://10.1.2.3/code",
		"a@example.com----pw----https://localhost/code", "a@example.com----pw----https://[::1]/code", tooMany,
	} {
		_, _, err := s.AcceptOwnedICloudImport(context.Background(), 7, []byte(content), "invalid", "req", "/imports")
		require.ErrorIs(t, err, ErrICloudOnboardingInvalid)
	}
	admin, _, err := s.AcceptAdminICloudOnboardingImport(context.Background(), 1, 7, []byte("admin@example.com----pw----https://codes.example.com/code"), s.now().Add(time.Hour), "admin", "req", "/admin/imports")
	require.NoError(t, err)
	require.Equal(t, "icloud_prepare", admin.Tasks[0].Stage)
}

func TestOwnedICloudImportProgressScopesEveryTask(t *testing.T) {
	s, db, _, _ := newOnboardingStateTest(t)
	s.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	view, _, err := s.AcceptOwnedICloudImport(context.Background(), 7, []byte("first@example.com----pw----https://codes.example.com/1\nsecond@example.com----pw----https://codes.example.com/2"), "batch", "req", "/imports")
	require.NoError(t, err)
	_, err = s.GetOwnedICloudImport(context.Background(), 8, view.ImportID)
	require.ErrorIs(t, err, ErrICloudOnboardingNotFound)
	require.NoError(t, db.Model(&iCloudRootModel{}).Where("id = ?", view.Tasks[1].ID).Update("owner_user_id", 8).Error)
	owned, err := s.GetOwnedICloudImport(context.Background(), 7, view.ImportID)
	require.NoError(t, err)
	require.Equal(t, 1, owned.Accepted)
	require.Len(t, owned.Tasks, 1)
	require.Equal(t, "first@example.com", owned.Tasks[0].Email)
	body, err := json.Marshal(owned)
	require.NoError(t, err)
	for _, secret := range []string{"second@example.com", "password", "deviceCode", "cookie", "codes.example.com"} {
		require.NotContains(t, string(body), secret)
	}
}

func TestOwnedICloudReimportProtectsPublishedAndAllocatedResources(t *testing.T) {
	for _, test := range []struct {
		name       string
		forSale    bool
		allocation string
		failed     bool
		kind       string
		wantErr    error
	}{
		{name: "published", forSale: true, wantErr: ErrICloudResourceStatus},
		{name: "allocated private", allocation: "allocated", wantErr: ErrICloudResourceAllocation},
		{name: "published and allocated", forSale: true, allocation: "allocated", wantErr: ErrICloudResourceStatus},
		{name: "unused legacy account"},
		{name: "released allocation", allocation: "released"},
		{name: "failed import", failed: true},
		{name: "failed user import", failed: true, kind: iCloudUserImportTaskKind},
		{name: "failed user import still allocated", failed: true, kind: iCloudUserImportTaskKind, allocation: "allocated", wantErr: ErrICloudResourceAllocation},
		{name: "failed import still allocated", failed: true, allocation: "allocated", wantErr: ErrICloudResourceAllocation},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			s.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
			require.NoError(t, db.AutoMigrate(&iCloudAdminCommandAllocation{}))
			updates := map[string]any{"account_role": "unknown", "task_kind": "", "onboarding_status": "", "import_id": nil,
				"status": iCloudResourceNormal, "for_sale": test.forSale}
			if test.failed {
				updates["account_role"], updates["task_kind"] = "primary", "onboarding"
				if test.kind != "" {
					updates["task_kind"] = test.kind
				}
				updates["onboarding_status"], updates["status"] = iCloudOnboardingFailed, iCloudResourceAbnormal
			}
			require.NoError(t, db.Model(task).Updates(updates).Error)
			if test.allocation != "" {
				require.NoError(t, db.Create(&iCloudAdminCommandAllocation{ResourceID: task.ID, Status: test.allocation}).Error)
			}
			var before, after iCloudResourceModel
			var rootBefore, rootAfter iCloudRootModel
			var secretBefore, secretAfter iCloudResourceCredentialModel
			require.NoError(t, db.First(&before, task.ID).Error)
			require.NoError(t, db.First(&rootBefore, task.ID).Error)
			require.NoError(t, db.First(&secretBefore, task.ID).Error)
			content := "child@example.com----replacement-password----https://codes.example.com/new"
			if test.wantErr != nil {
				content = "new@example.com----secret----https://codes.example.com/new\n" + content
			}
			view, _, err := s.AcceptOwnedICloudImport(context.Background(), 1, []byte(content), "reimport", "req", "/imports")
			if test.wantErr == nil {
				require.NoError(t, err)
				require.Len(t, view.Tasks, 1)
				require.Equal(t, task.ID, view.Tasks[0].ID)
				require.Equal(t, "manage_prepare", view.Tasks[0].Stage)
				return
			}
			require.ErrorIs(t, err, test.wantErr)
			require.Nil(t, view)
			require.NoError(t, db.First(&after, task.ID).Error)
			require.NoError(t, db.First(&rootAfter, task.ID).Error)
			require.NoError(t, db.First(&secretAfter, task.ID).Error)
			require.Equal(t, before, after)
			require.Equal(t, rootBefore, rootAfter)
			require.Equal(t, secretBefore, secretAfter)
			var count int64
			require.NoError(t, db.Model(&iCloudResourceModel{}).Count(&count).Error)
			require.EqualValues(t, 1, count, "a rejected batch must not create its other accounts")
		})
	}
}

func TestOwnedICloudWorkflowCannotEnterOtherPhases(t *testing.T) {
	for _, stage := range []string{"accepted", "icloud_prepare", "family_prepare", "sms_send"} {
		t.Run(stage, func(t *testing.T) {
			s, db, task, apple := newOnboardingStateTest(t)
			require.NoError(t, db.Model(task).Updates(map[string]any{"task_kind": iCloudUserImportTaskKind, "stage": stage, "pending_sms_purpose": appleSMSPhoneEnrollment}).Error)
			processOnboardingStageForTest(t, s, db, task)
			require.Equal(t, iCloudOnboardingFailed, task.Status)
			require.Empty(t, apple.operations)
		})
	}
}

func TestOwnedICloudResourcesEnforceOwnershipAndMutationRules(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, t.Name())
	require.NoError(t, db.AutoMigrate(&iCloudAdminTestUser{}, &iCloudAdminTestGroup{}, &iCloudAdminTestPhone{}))
	now := time.Now().UTC()
	for _, id := range []uint{7, 8} {
		require.NoError(t, db.Create(&iCloudAdminTestUser{ID: id, Email: fmt.Sprintf("owner%d@example.com", id), Role: "user", Status: "active"}).Error)
	}
	for id := uint(1); id <= 4; id++ {
		createAdminICloudCommandResource(t, db, now, iCloudResourceModel{ID: id, PrimaryEmail: fmt.Sprintf("resource%d@example.com", id), Status: iCloudResourceNormal, CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour), DeviceCodeAPI: "https://secret.example.com/code"})
	}
	require.NoError(t, db.Model(&iCloudRootModel{}).Where("id = ?", 2).Update("owner_user_id", 8).Error)
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = ?", 3).Update("status", iCloudResourceDeleted).Error)
	require.NoError(t, db.Create(&iCloudAliasModel{ResourceID: 2, Email: "hidden@icloud.com", Status: iCloudResourceNormal}).Error)
	s := NewService(db, nil, nil)
	ctx := context.Background()
	list, err := s.ListOwnedICloudResources(ctx, 7, AdminICloudResourceListFilter{Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 2, list.Total)
	require.EqualValues(t, 2, list.Facets.Status.All)
	require.EqualValues(t, 2, list.Facets.ForSale.All)
	require.Equal(t, []OwnedICloudSuffixFacet{{Key: "example.com", Count: 2}}, list.Facets.Suffixes)
	body, err := json.Marshal(list)
	require.NoError(t, err)
	for _, secret := range []string{"resource2@", "resource3@", "secret.example.com", "deviceCode", "owner8@", "cookie"} {
		require.NotContains(t, string(body), secret)
	}
	_, err = s.ListOwnedICloudAliases(ctx, 7, 2, 0, 20)
	require.ErrorIs(t, err, ErrICloudResourceNotFound)
	for _, command := range []AdminICloudCommand{AdminICloudValidate, AdminICloudPublish, AdminICloudDelete} {
		_, err = s.ApplyOwnedICloudCommand(ctx, 7, command, 2, 1, string(command), "req", "/test")
		require.ErrorIs(t, err, ErrICloudResourceNotFound)
	}
	_, err = s.ApplyOwnedICloudCommand(ctx, 7, AdminICloudPublish, 1, 1, "publish", "req", "/test")
	require.ErrorIs(t, err, ErrICloudResourceOwner)
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Update("for_sale", true).Error)
	_, err = s.ApplyOwnedICloudCommand(ctx, 7, AdminICloudDelete, 1, 1, "delete-published", "req", "/test")
	require.ErrorIs(t, err, ErrICloudResourceStatus)
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{"for_sale": false, "task_kind": iCloudUserImportTaskKind, "onboarding_status": "processing"}).Error)
	_, err = s.ApplyOwnedICloudCommand(ctx, 7, AdminICloudValidate, 1, 1, "active", "req", "/test")
	require.ErrorIs(t, err, ErrICloudResourceStatus)
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Update("onboarding_status", "completed").Error)
	batch, err := s.ApplyOwnedICloudBatch(ctx, 7, AdminICloudValidate, []OwnedICloudCommandItem{{ID: 1, Version: 99}, {ID: 2, Version: 1}, {ID: 4, Version: 1}}, "batch", "req", "/test")
	require.NoError(t, err)
	require.Equal(t, []uint{4}, batch.AffectedResourceIDs)
	require.Equal(t, []uint{1, 2}, batch.SkippedResourceIDs)
	// Replaying a receipt must still check the current owner.
	require.NoError(t, db.Model(&iCloudRootModel{}).Where("id = ?", 4).Update("owner_user_id", 8).Error)
	_, err = s.ApplyOwnedICloudCommand(ctx, 7, AdminICloudValidate, 4, 1, "batch:4", "req", "/test")
	require.ErrorIs(t, err, ErrICloudResourceNotFound)
}

func TestOwnedICloudFilterBatchScopesAllPagesAndRejectsOversizedSelection(t *testing.T) {
	db := newAdminICloudCommandTestDB(t, t.Name())
	require.NoError(t, db.AutoMigrate(&iCloudAdminTestUser{}, &iCloudAdminTestGroup{}, &iCloudAdminTestPhone{}))
	now := time.Now().UTC()
	for _, id := range []uint{7, 8} {
		require.NoError(t, db.Create(&iCloudAdminTestUser{ID: id, Email: fmt.Sprintf("owner%d@example.com", id), Role: "user", Status: "active"}).Error)
	}
	for id := uint(1); id <= 104; id++ {
		email := fmt.Sprintf("resource%d@example.com", id)
		if id == 103 {
			email = "different@other.test"
		}
		createAdminICloudCommandResource(t, db, now, iCloudResourceModel{ID: id, PrimaryEmail: email, Status: iCloudResourceNormal, CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour)})
	}
	require.NoError(t, db.Model(&iCloudRootModel{}).Where("id = ?", 104).Update("owner_user_id", 8).Error)
	s := NewService(db, nil, nil)
	ctx := context.Background()
	filter := AdminICloudResourceListFilter{Suffix: "example.com", OwnerUserID: 8, Limit: 20}
	list, err := s.ListOwnedICloudResources(ctx, 7, filter)
	require.NoError(t, err)
	require.Len(t, list.Items, 20)
	require.EqualValues(t, 102, list.Total)
	require.Equal(t, []OwnedICloudSuffixFacet{{Key: "example.com", Count: 102}, {Key: "other.test", Count: 1}}, list.Facets.Suffixes)
	result, err := s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudDelete, filter, "filtered-delete", "req", "/test")
	require.NoError(t, err)
	require.Equal(t, 102, result.Affected)
	var untouched int64
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id IN ? AND status = ?", []uint{103, 104}, iCloudResourceNormal).Count(&untouched).Error)
	require.EqualValues(t, 2, untouched)

	roots := make([]iCloudRootModel, 0, adminICloudBatchMax+1)
	resources := make([]iCloudResourceModel, 0, adminICloudBatchMax+1)
	for id := uint(10000); id <= 10000+adminICloudBatchMax; id++ {
		roots = append(roots, iCloudRootModel{ID: id, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now})
		resources = append(resources, iCloudResourceModel{ID: id, ResourceType: "icloud", PrimaryEmail: fmt.Sprintf("resource%d@large.test", id), Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1})
	}
	require.NoError(t, db.CreateInBatches(roots, 100).Error)
	require.NoError(t, db.CreateInBatches(resources, 100).Error)
	_, err = s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudDelete, AdminICloudResourceListFilter{Suffix: "large.test"}, "oversized", "req", "/test")
	require.ErrorIs(t, err, ErrICloudResourceSelection)
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id >= ? AND status = ?", 10000, iCloudResourceNormal).Count(&untouched).Error)
	require.EqualValues(t, adminICloudBatchMax+1, untouched)
}
