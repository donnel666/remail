package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/kitesim"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type refreshFakeApple struct{}

func (refreshFakeApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if request.Operation != appleOnboardingExport {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "unexpected_operation", SafeMessage: "unexpected operation"}
	}
	return AppleOnboardingResponse{
		CountryCode: "US",
		NewChannel: &AppleOnboardingChannel{
			Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "myacinfo=refreshed",
			Origin: "https://account.apple.com", Referer: "https://account.apple.com/", UserAgent: "ua", APIKey: "api", Scnt: "scnt",
		},
		OldChannel: &AppleOnboardingChannel{
			Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com", Cookie: "X-APPLE-WEBAUTH-TOKEN=refreshed",
			Origin: "https://www.icloud.com", Referer: "https://www.icloud.com/", UserAgent: "ua",
			DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "master",
		},
	}, nil
}

type refreshRequestCaptureApple struct {
	request AppleOnboardingRequest
}

func (p *refreshRequestCaptureApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.request = request
	return AppleOnboardingResponse{Session: json.RawMessage(`{"flow":"ok"}`)}, nil
}

type refreshStillInactiveApple struct{}

func (refreshStillInactiveApple) Execute(_ context.Context, _ AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	opened := false
	return AppleOnboardingResponse{ICloudOpened: &opened}, nil
}

type refreshMismatchedPhone struct {
	onboardingProvidedPhone
	reserveCalls int
}

func (p *refreshMismatchedPhone) BindICloudSMSPhone(context.Context, string, string) (kitesim.SMSPhoneBinding, error) {
	return kitesim.SMSPhoneBinding{PhoneID: 8, PhoneNumber: "14155550001", CountryCode: "US"}, nil
}

func (p *refreshMismatchedPhone) ReserveSMSChallenge(context.Context, uint, string, string, time.Time) (kitesim.SMSReservation, error) {
	p.reserveCalls++
	return kitesim.SMSReservation{}, errors.New("unexpected reservation")
}

type refreshCountingApple struct{ calls int }

func (p *refreshCountingApple) Execute(context.Context, AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.calls++
	return AppleOnboardingResponse{}, errors.New("unexpected Apple call")
}

type refreshMutatingApple struct {
	mutate   func()
	response AppleOnboardingResponse
}

func (p *refreshMutatingApple) Execute(context.Context, AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if p.mutate != nil {
		p.mutate()
	}
	return p.response, nil
}

type oldCookieBackfillApple struct {
	calls    int
	request  AppleOnboardingRequest
	response AppleOnboardingResponse
	err      error
}

func (p *oldCookieBackfillApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.calls++
	p.request = request
	return p.response, p.err
}

type refreshExactPhoneRequired struct {
	onboardingProvidedPhone
	exactCalls  int
	suffixCalls int
}

func (p *refreshExactPhoneRequired) BindICloudSMSPhone(context.Context, string, string) (kitesim.SMSPhoneBinding, error) {
	p.exactCalls++
	return kitesim.SMSPhoneBinding{}, kitesim.ErrPhoneMissing
}

func (p *refreshExactPhoneRequired) BindICloudSMSPhoneBySuffix(context.Context, string, string) (kitesim.SMSPhoneBinding, error) {
	p.suffixCalls++
	return kitesim.SMSPhoneBinding{PhoneID: 8, PhoneNumber: "16175559901", CountryCode: "US"}, nil
}

func newICloudRefreshTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudResourceCredentialModel{},
		&governanceinfra.OperationLogModel{}, &coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedICloudRefreshResource(t *testing.T, db *gorm.DB, now time.Time) iCloudResourceModel {
	t.Helper()
	root := iCloudRootModel{Type: "icloud", OwnerUserID: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	phoneID := uint(7)
	resource := iCloudResourceModel{
		ID: root.ID, ResourceType: "icloud", PrimaryEmail: "refresh@example.com", AccountRole: "primary",
		Region: "美国区", CountryCode: "US", ICloudOpened: true, BoundPhoneNumber: "14155550001",
		BoundPhoneCountryCode: "US", BoundPhoneSource: "manual", KitesimPhoneID: &phoneID, Status: iCloudResourcePending,
		CredentialRevision: 2, CredentialUpdatedAt: now, ValidationGeneration: 3,
		ExpireAt: now.AddDate(0, 1, 0), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	answers, _ := json.Marshal([3]iCloudSecurityAnswer{{Question: "q1", Answer: "a1"}, {Question: "q2", Answer: "a2"}, {Question: "q3", Answer: "a3"}})
	if err := db.Create(&iCloudResourceCredentialModel{
		ResourceID: resource.ID, ApplePassword: "Secret1!", SecurityAnswers: answers,
		Birthday: time.Date(2000, 11, 2, 0, 0, 0, 0, time.UTC), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for _, channel := range []iCloudResourceChannelModel{
		{ResourceID: resource.ID, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "expired", SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now},
		{ResourceID: resource.ID, Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com", Cookie: "expired", SetupCookie: "expired", SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&channel).Error; err != nil {
			t.Fatal(err)
		}
	}
	return resource
}

func TestEnsureICloudCookieRefreshCreatesOnePermanentPhoneTask(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	for range 2 {
		if err := service.EnsureICloudCookieRefresh(context.Background(), resource.ID); err != nil {
			t.Fatal(err)
		}
	}
	var tasks []iCloudOnboardingTaskModel
	if err := db.Where("task_kind = ? AND resource_id = ?", "refresh", resource.ID).Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Stage != "icloud_prepare" || tasks[0].BoundPhoneNumber != resource.BoundPhoneNumber || tasks[0].ExpectedCredentialRevision != resource.CredentialRevision {
		t.Fatalf("unexpected refresh tasks: %+v", tasks)
	}
}

func TestApplyAdminICloudActivationQueuesForcedOldCookieRefresh(t *testing.T) {
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"icloud_opened": false, "status": iCloudResourceNormal, "for_sale": true, "last_safe_error": "healthy",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("resource_id = ? AND kind = ?", resource.ID, iCloudChannelWeb).Delete(&iCloudResourceChannelModel{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&iCloudResourceChannelModel{}).Where("resource_id = ?", resource.ID).Update("session_status", iCloudSessionValid).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }

	result, err := service.ApplyAdminICloudCommand(
		context.Background(), AdminICloudActivate, resource.ID, 1, 9,
		"activate-icloud-1", "request-1", "/v1/admin/icloud/resources/1/icloud-activation",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Version != 2 {
		t.Fatalf("activation result = %+v", result)
	}
	var stored iCloudResourceModel
	if err := db.First(&stored, resource.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ICloudOpened || stored.Status != iCloudResourceNormal || !stored.ForSale || stored.LastSafeError != "healthy" {
		t.Fatalf("resource state changed while queuing old Cookie backfill: %+v", stored)
	}
	var tasks []iCloudOnboardingTaskModel
	if err := db.Where("task_kind = ? AND resource_id = ?", "refresh", resource.ID).Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Stage != "old_cookie_prepare" || tasks[0].PendingSMSPurpose != appleSMSOldCookieLogin || tasks[0].ICloudOpened {
		t.Fatalf("forced refresh task = %+v", tasks)
	}

	replayed, err := service.ApplyAdminICloudCommand(
		context.Background(), AdminICloudActivate, resource.ID, 1, 9,
		"activate-icloud-1", "request-1", "/v1/admin/icloud/resources/1/icloud-activation",
	)
	if err != nil || replayed.Version != result.Version {
		t.Fatalf("replayed activation = %+v, err = %v", replayed, err)
	}
	var count int64
	if err := db.Model(&iCloudOnboardingTaskModel{}).Where("task_kind = ? AND resource_id = ?", "refresh", resource.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("refresh task count = %d, err = %v", count, err)
	}
}

func TestICloudOldCookieBackfillPreservesNewCookieAndResourceState(t *testing.T) {
	now := time.Date(2026, 8, 17, 8, 10, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	nextValidationAt := now.Add(10 * time.Minute)
	nextProvisionAt := now.Add(20 * time.Minute)
	lastAllocatedAt := now.Add(-time.Hour)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"icloud_opened": false, "status": iCloudResourceNormal, "for_sale": true,
		"next_validation_at": nextValidationAt, "next_provision_at": nextProvisionAt,
		"validation_failures": 2, "alias_count": 4, "alias_provision_candidate": "pending@example.com", "alias_provision_reconcile": true,
		"last_allocated_at": lastAllocatedAt, "last_safe_error": "keep-resource-state",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&iCloudResourceChannelModel{}).
		Where("resource_id = ? AND kind = ?", resource.ID, iCloudChannelAppleAccount).
		Updates(map[string]any{"cookie": "myacinfo=healthy-new", "session_status": iCloudSessionValid}).Error; err != nil {
		t.Fatal(err)
	}
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	resourceID := resource.ID
	task := iCloudOnboardingTaskModel{
		ResourceID: &resourceID, TaskKind: "refresh", PrimaryEmail: resource.PrimaryEmail, AccountRole: "primary", ICloudOpened: true,
		BoundPhoneNumber: resource.BoundPhoneNumber, BoundPhoneCountryCode: "US", BoundPhoneSource: "manual", KitesimPhoneID: resource.KitesimPhoneID,
		SecretPayload: secret, PendingSMSPurpose: appleSMSOldCookieLogin,
		Status: iCloudOnboardingProcessing, Stage: "old_cookie_finish", DispatchStatus: "pending",
		Generation: 1, ExpectedCredentialRevision: resource.CredentialRevision, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	opened := true
	provider := &oldCookieBackfillApple{response: AppleOnboardingResponse{
		ICloudOpened: &opened,
		OldChannel: &AppleOnboardingChannel{
			Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com", Cookie: "X-APPLE-WEBAUTH-TOKEN=backfilled",
			DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "master",
		},
	}}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.onboardingApple = provider
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || provider.request.Operation != appleOnboardingFinishICloud {
		t.Fatalf("backfill requests = %d, last = %+v", provider.calls, provider.request)
	}
	var newChannel, oldChannel iCloudResourceChannelModel
	if err := db.Where("resource_id = ? AND kind = ?", resource.ID, iCloudChannelAppleAccount).First(&newChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("resource_id = ? AND kind = ?", resource.ID, iCloudChannelWeb).First(&oldChannel).Error; err != nil {
		t.Fatal(err)
	}
	if newChannel.Cookie != "myacinfo=healthy-new" || newChannel.SessionStatus != iCloudSessionValid || oldChannel.Cookie != "X-APPLE-WEBAUTH-TOKEN=backfilled" {
		t.Fatalf("channels after backfill: new=%+v old=%+v", newChannel, oldChannel)
	}
	var stored iCloudResourceModel
	if err := db.First(&stored, resource.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.ICloudOpened || stored.Status != iCloudResourceNormal || !stored.ForSale || stored.ValidationGeneration != resource.ValidationGeneration || stored.ValidationFailures != 2 || stored.AliasCount != 4 ||
		stored.AliasProvisionCandidate != "pending@example.com" || !stored.AliasProvisionReconcile || stored.LastAllocatedAt == nil || !stored.LastAllocatedAt.Equal(lastAllocatedAt) || stored.LastSafeError != "keep-resource-state" ||
		stored.NextValidationAt == nil || !stored.NextValidationAt.Equal(nextValidationAt) || stored.NextProvisionAt == nil || !stored.NextProvisionAt.Equal(nextProvisionAt) {
		t.Fatalf("resource state changed by backfill: %+v", stored)
	}
	if err := db.First(&task, task.ID).Error; err != nil || task.Status != iCloudOnboardingCompleted || task.PendingSMSPurpose != "" {
		t.Fatalf("backfill task = %+v err=%v", task, err)
	}
}

func TestICloudOldCookieBackfillFailuresDoNotMarkHealthyResourceAbnormal(t *testing.T) {
	for _, infrastructureFailure := range []bool{false, true} {
		name := "provider"
		if infrastructureFailure {
			name = "infrastructure"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 8, 17, 8, 20, 0, 0, time.UTC)
			db := newICloudRefreshTestDB(t)
			resource := seedICloudRefreshResource(t, db, now)
			nextProvisionAt := now.Add(30 * time.Minute)
			lastAllocatedAt := now.Add(-time.Hour)
			if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
				"status": iCloudResourceNormal, "for_sale": true, "next_provision_at": nextProvisionAt,
				"alias_provision_candidate": "pending@example.com", "alias_provision_reconcile": true,
				"last_allocated_at": lastAllocatedAt, "last_safe_error": "healthy",
			}).Error; err != nil {
				t.Fatal(err)
			}
			secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
			resourceID := resource.ID
			task := iCloudOnboardingTaskModel{
				ResourceID: &resourceID, TaskKind: "refresh", PrimaryEmail: resource.PrimaryEmail, AccountRole: "primary", ICloudOpened: true,
				BoundPhoneNumber: resource.BoundPhoneNumber, BoundPhoneCountryCode: "US", BoundPhoneSource: "manual", KitesimPhoneID: resource.KitesimPhoneID,
				SecretPayload: secret, PendingSMSPurpose: appleSMSOldCookieLogin,
				Status: iCloudOnboardingProcessing, Stage: "old_cookie_prepare", DispatchStatus: "pending",
				Generation: 1, ExpectedCredentialRevision: resource.CredentialRevision, MaxAttempts: 2, CreatedAt: now, UpdatedAt: now,
			}
			if infrastructureFailure {
				task.DispatchStatus = "running"
				task.ClaimToken = "claim"
				task.Attempts = 1
			}
			if err := db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			service := NewService(db, nil, nil)
			service.now = func() time.Time { return now }
			if infrastructureFailure {
				if err := service.ReleaseICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}, "temporary failure"); err != nil {
					t.Fatal(err)
				}
			} else {
				service.onboardingApple = &oldCookieBackfillApple{err: &AppleOnboardingError{Category: "provider_rejected", SafeMessage: "Apple rejected the request."}}
				if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.First(&task, task.ID).Error; err != nil || task.Status != iCloudOnboardingFailed {
				t.Fatalf("failed backfill task = %+v err=%v", task, err)
			}
			var stored iCloudResourceModel
			if err := db.First(&stored, resource.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != iCloudResourceNormal || !stored.ForSale || stored.NextProvisionAt == nil || !stored.NextProvisionAt.Equal(nextProvisionAt) ||
				stored.AliasProvisionCandidate != "pending@example.com" || !stored.AliasProvisionReconcile || stored.LastAllocatedAt == nil || !stored.LastAllocatedAt.Equal(lastAllocatedAt) || stored.LastSafeError != "healthy" {
				t.Fatalf("healthy resource changed after backfill failure: %+v", stored)
			}
		})
	}
}

func TestICloudOldCookieBackfillSelfRowFailurePreservesResourceSafeError(t *testing.T) {
	now := time.Date(2026, 8, 17, 8, 25, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"status": iCloudResourceNormal, "for_sale": true, "last_safe_error": "healthy",
		"task_kind": "refresh", "resource_id": resource.ID, "pending_sms_purpose": appleSMSOldCookieLogin,
		"onboarding_status": iCloudOnboardingProcessing, "stage": "old_cookie_prepare", "dispatch_status": "pending",
		"generation": uint64(1), "expected_credential_revision": resource.CredentialRevision,
		"secret_payload": iCloudJSON(secret), "max_attempts": 2, "updated_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.onboardingApple = &oldCookieBackfillApple{err: &AppleOnboardingError{
		Category: "provider_rejected", SafeMessage: "Apple rejected the request.",
	}}
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: resource.ID, Generation: 1}); err != nil {
		t.Fatal(err)
	}

	var stored iCloudResourceModel
	if err := db.First(&stored, resource.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != iCloudResourceNormal || !stored.ForSale || stored.LastSafeError != "healthy" {
		t.Fatalf("resource state changed after self-row backfill failure: %+v", stored)
	}
	if stored.OnboardingStatus != iCloudOnboardingFailed || stored.WorkflowLastErrorCategory != "provider_rejected" ||
		len(stored.WorkflowSecretPayload) != 0 || len(stored.WorkflowSessionPayload) != 0 || stored.WorkflowSMSPurpose != "" {
		t.Fatalf("self-row backfill workflow = %+v", stored)
	}
}

func TestICloudOldCookieSMSRecoveryKeepsBackfillMarker(t *testing.T) {
	now := time.Date(2026, 8, 17, 8, 30, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	task := iCloudOnboardingTaskModel{
		TaskKind: "refresh", PrimaryEmail: "backfill-recovery@example.com", AccountRole: "primary",
		PendingSMSPurpose: appleSMSOldCookieLogin, Status: iCloudOnboardingProcessing,
		Stage: "sms_verify_recover", DispatchStatus: "running", ClaimToken: "claim",
		Generation: 1, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.recoverICloudOnboardingSMSVerification(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil || task.Stage != "old_cookie_prepare" || task.PendingSMSPurpose != appleSMSOldCookieLogin {
		t.Fatalf("recovered backfill task = %+v err=%v", task, err)
	}
}

func TestEnsureICloudCookieRefreshUpdatesTheResourceWorkflowRow(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 5, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.EnsureICloudCookieRefresh(context.Background(), resource.ID); err != nil {
		t.Fatalf("resource workflow update error = %v", err)
	}
	var stored iCloudResourceModel
	if err := db.First(&stored, resource.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.WorkflowTaskKind != "refresh" || stored.OnboardingStatus != iCloudOnboardingProcessing || stored.WorkflowResourceID == nil || *stored.WorkflowResourceID != resource.ID {
		t.Fatalf("unexpected resource workflow: %+v", stored)
	}
}

func TestEnsureICloudCookieRefreshSkipsIncompleteCredentials(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 6, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	if err := db.Model(&iCloudResourceCredentialModel{}).Where("resource_id = ?", resource.ID).Update("security_answers", []byte(`[]`)).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.EnsureICloudCookieRefresh(context.Background(), resource.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&iCloudOnboardingTaskModel{}).Where("task_kind = ? AND resource_id = ?", "refresh", resource.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("incomplete credentials created %d refresh tasks: %v", count, err)
	}
}

func TestEnsureICloudCookieRefreshSkipsMissingPermanentPhone(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 6, 30, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Update("kitesim_phone_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.EnsureICloudCookieRefresh(context.Background(), resource.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&iCloudOnboardingTaskModel{}).Where("task_kind = ? AND resource_id = ?", "refresh", resource.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("missing permanent phone created %d refresh tasks: %v", count, err)
	}
}

func TestICloudRefreshPreflightRejectsChangedResourceBeforeExternalCalls(t *testing.T) {
	tests := []struct {
		name   string
		stage  string
		mutate func(*gorm.DB, iCloudResourceModel) error
	}{
		{name: "disabled", stage: "icloud_prepare", mutate: func(db *gorm.DB, resource iCloudResourceModel) error {
			return db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Update("status", iCloudResourceDisabled).Error
		}},
		{name: "credentials", stage: "icloud_prepare", mutate: func(db *gorm.DB, resource iCloudResourceModel) error {
			return db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Update("credential_revision", resource.CredentialRevision+1).Error
		}},
		{name: "phone number", stage: "sms_send", mutate: func(db *gorm.DB, resource iCloudResourceModel) error {
			return db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Update("bound_phone_number", "14155559999").Error
		}},
		{name: "phone id", stage: "sms_send", mutate: func(db *gorm.DB, resource iCloudResourceModel) error {
			return db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Update("kitesim_phone_id", 8).Error
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 8, 16, 12, 6, 45, 0, time.UTC)
			db := newICloudRefreshTestDB(t)
			resource := seedICloudRefreshResource(t, db, now)
			secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
			resourceID := resource.ID
			task := iCloudOnboardingTaskModel{
				ResourceID: &resourceID, TaskKind: "refresh", PrimaryEmail: resource.PrimaryEmail, AccountRole: "primary",
				Region: resource.Region, CountryCode: resource.CountryCode, ICloudOpened: true,
				BoundPhoneNumber: resource.BoundPhoneNumber, BoundPhoneCountryCode: "US", BoundPhoneSource: "manual", KitesimPhoneID: resource.KitesimPhoneID,
				SecretPayload: secret, PendingSMSPurpose: appleSMSManageLogin,
				Status: iCloudOnboardingProcessing, Stage: tt.stage, DispatchStatus: "pending",
				Generation: 1, ExpectedCredentialRevision: resource.CredentialRevision, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			if err := tt.mutate(db, resource); err != nil {
				t.Fatal(err)
			}
			apple := &refreshCountingApple{}
			phone := &onboardingCountingPhone{}
			service := NewService(db, nil, nil)
			service.now = func() time.Time { return now }
			service.onboardingApple = apple
			service.smsPhones = phone
			if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
				var current iCloudOnboardingTaskModel
				_ = db.First(&current, task.ID).Error
				t.Fatalf("process stale refresh: %v; current=%+v", err, current)
			}
			if err := db.First(&task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if task.Status != iCloudOnboardingFailed || task.LastErrorCategory != "refresh_stale" || apple.calls != 0 || phone.bindCalls != 0 {
				t.Fatalf("stale refresh was executed: task=%+v AppleCalls=%d phoneCalls=%d", task, apple.calls, phone.bindCalls)
			}
			var storedResource iCloudResourceModel
			if err := db.First(&storedResource, resource.ID).Error; err != nil {
				t.Fatal(err)
			}
			if storedResource.Status == iCloudResourceAbnormal {
				t.Fatalf("stale refresh marked the current resource abnormal: %+v", storedResource)
			}
		})
	}
}

func TestICloudRefreshRejectsStalePhoneSnapshotBeforeSMSReservation(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 7, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	resourceID := resource.ID
	task := iCloudOnboardingTaskModel{
		ResourceID: &resourceID, TaskKind: "refresh", PrimaryEmail: resource.PrimaryEmail, AccountRole: "primary",
		Region: resource.Region, CountryCode: resource.CountryCode, ICloudOpened: true,
		BoundPhoneNumber: resource.BoundPhoneNumber, BoundPhoneCountryCode: "US", BoundPhoneSource: "manual", KitesimPhoneID: resource.KitesimPhoneID,
		SecretPayload: secret, PendingSMSPurpose: appleSMSManageLogin,
		Status: iCloudOnboardingProcessing, Stage: "sms_send", DispatchStatus: "pending",
		Generation: 1, ExpectedCredentialRevision: resource.CredentialRevision, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	phone := &refreshMismatchedPhone{}
	apple := &refreshCountingApple{}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.smsPhones = phone
	service.onboardingApple = apple
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != iCloudOnboardingFailed || task.LastErrorCategory != "phone_binding_conflict" || phone.reserveCalls != 0 || apple.calls != 0 {
		t.Fatalf("stale phone snapshot was used: task=%+v reservations=%d AppleCalls=%d", task, phone.reserveCalls, apple.calls)
	}
}

func TestICloudTrustedPhoneNeverReplacesKnownPhoneWithSuffixMatch(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 8, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	task := iCloudOnboardingTaskModel{
		TaskKind: "onboarding", PrimaryEmail: "known-phone@example.com", AccountRole: "child",
		Region: "美国区", CountryCode: "US", ICloudOpened: true,
		BoundPhoneNumber: "14155550001", BoundPhoneCountryCode: "US", BoundPhoneSource: "manual",
		Status: iCloudOnboardingProcessing, Stage: "manage_prepare", DispatchStatus: "running", ClaimToken: "claim",
		Generation: 1, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	phone := &refreshExactPhoneRequired{}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.smsPhones = phone
	binding, err := service.bindICloudOnboardingTrustedPhone(context.Background(), &task, "01")
	if err != nil {
		t.Fatal(err)
	}
	if binding != nil {
		t.Fatalf("unexpected trusted phone binding: %+v", binding)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != iCloudOnboardingFailed || task.LastErrorCategory != "phone_not_in_pool" || phone.exactCalls != 1 || phone.suffixCalls != 0 {
		t.Fatalf("known phone fell back to suffix binding: task=%+v exact=%d suffix=%d", task, phone.exactCalls, phone.suffixCalls)
	}
}

func TestICloudOpenedChildRefreshRequestsOldChannel(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 10, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	resourceID := resource.ID
	task := iCloudOnboardingTaskModel{
		ResourceID: &resourceID, TaskKind: "refresh", PrimaryEmail: resource.PrimaryEmail, AccountRole: "child", ICloudOpened: true,
		BoundPhoneNumber: resource.BoundPhoneNumber, BoundPhoneCountryCode: "US", BoundPhoneSource: "kitesim", KitesimPhoneID: resource.KitesimPhoneID,
		SecretPayload: secret, Status: iCloudOnboardingProcessing, Stage: "icloud_finish", DispatchStatus: "pending",
		Generation: 1, ExpectedCredentialRevision: resource.CredentialRevision, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	provider := &refreshRequestCaptureApple{}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.onboardingApple = provider
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
		t.Fatal(err)
	}
	if provider.request.Operation != appleOnboardingFinishICloud || provider.request.SkipOldChannel {
		t.Fatalf("refresh finish request = %+v", provider.request)
	}
	if err := db.First(&task, task.ID).Error; err != nil || task.Stage != "manage_prepare" {
		t.Fatalf("refresh task = %+v err=%v", task, err)
	}
}

func TestICloudRefreshActivationConfirmationRestartsICloud(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 15, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	if err := db.AutoMigrate(&governanceinfra.OperationLogModel{}); err != nil {
		t.Fatal(err)
	}
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	phoneID := uint(7)
	task := iCloudOnboardingTaskModel{
		TaskKind: "refresh", PrimaryEmail: "child-refresh@example.com", AccountRole: "child",
		BoundPhoneNumber: "14155550001", BoundPhoneCountryCode: "US", BoundPhoneSource: "kitesim", KitesimPhoneID: &phoneID,
		SecretPayload: secret, Status: iCloudOnboardingWaiting, Stage: "waiting_icloud_activation", DispatchStatus: "waiting",
		Generation: 1, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.ConfirmICloudOnboardingActivation(context.Background(), task.ID, 9, "refresh-activation", "/test/activation"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil || task.Stage != "icloud_prepare" || task.DispatchStatus != "pending" {
		t.Fatalf("refresh activation task = %+v err=%v", task, err)
	}
}

func TestICloudRefreshInactiveReopensActivationConfirmation(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 20, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	if err := db.AutoMigrate(&governanceinfra.OperationLogModel{}); err != nil {
		t.Fatal(err)
	}
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	resourceID := resource.ID
	confirmedAt := now.Add(-time.Minute)
	task := iCloudOnboardingTaskModel{
		ResourceID: &resourceID, TaskKind: "refresh", PrimaryEmail: resource.PrimaryEmail, AccountRole: "child", ICloudOpened: true,
		BoundPhoneNumber: resource.BoundPhoneNumber, BoundPhoneCountryCode: "US", BoundPhoneSource: "kitesim", KitesimPhoneID: resource.KitesimPhoneID,
		SecretPayload: secret, Status: iCloudOnboardingProcessing, Stage: "icloud_finish", DispatchStatus: "pending",
		Generation: 2, ExpectedCredentialRevision: resource.CredentialRevision, MaxAttempts: 5, ICloudActivationConfirmedAt: &confirmedAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.onboardingApple = refreshStillInactiveApple{}
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
		t.Fatal(err)
	}
	var stored iCloudOnboardingTaskModel
	if err := db.First(&stored, task.ID).Error; err != nil || stored.Stage != "waiting_icloud_activation" || stored.ICloudActivationConfirmedAt != nil {
		t.Fatalf("inactive refresh task = %+v err=%v", stored, err)
	}
	if err := service.ConfirmICloudOnboardingActivation(context.Background(), task.ID, 9, "refresh-activation-retry", "/test/activation"); err != nil {
		t.Fatal(err)
	}
	stored = iCloudOnboardingTaskModel{}
	if err := db.First(&stored, task.ID).Error; err != nil || stored.Stage != "icloud_prepare" || stored.ICloudActivationConfirmedAt == nil {
		t.Fatalf("reconfirmed refresh task = %+v err=%v", stored, err)
	}
}

func TestICloudCookieRefreshAtomicallyReplacesChannels(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	nextProvisionAt := now.Add(15 * time.Minute)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"status": iCloudResourceNormal, "for_sale": true, "next_provision_at": nextProvisionAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	secret, _ := json.Marshal(iCloudOnboardingSecret{
		Password: "Secret1!", Birthday: "2000-11-02",
		SecurityAnswers: [3]iCloudSecurityAnswer{{Question: "q1", Answer: "a1"}, {Question: "q2", Answer: "a2"}, {Question: "q3", Answer: "a3"}},
	})
	resourceID := resource.ID
	task := iCloudOnboardingTaskModel{
		ResourceID: &resourceID, TaskKind: "refresh", PrimaryEmail: resource.PrimaryEmail, AccountRole: "primary",
		Region: resource.Region, CountryCode: resource.CountryCode, ICloudOpened: true,
		BoundPhoneNumber: resource.BoundPhoneNumber, BoundPhoneCountryCode: "US", BoundPhoneSource: "manual", KitesimPhoneID: resource.KitesimPhoneID,
		SecretPayload: secret, Status: iCloudOnboardingProcessing, Stage: "resource_refresh", DispatchStatus: "pending",
		Generation: 1, ExpectedCredentialRevision: resource.CredentialRevision, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.onboardingApple = refreshFakeApple{}
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
		t.Fatal(err)
	}
	var storedTask iCloudOnboardingTaskModel
	if err := db.First(&storedTask, task.ID).Error; err != nil || storedTask.Status != iCloudOnboardingCompleted || len(storedTask.SecretPayload) != 0 {
		t.Fatalf("refresh task not completed securely: %+v err=%v", storedTask, err)
	}
	var channels []iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", resource.ID).Order("kind").Find(&channels).Error; err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 || channels[0].Cookie != "myacinfo=refreshed" || channels[1].Cookie != "X-APPLE-WEBAUTH-TOKEN=refreshed" {
		t.Fatalf("channels were not refreshed: %+v", channels)
	}
	var storedResource iCloudResourceModel
	if err := db.First(&storedResource, resource.ID).Error; err != nil || storedResource.ValidationGeneration != resource.ValidationGeneration+1 || storedResource.NextValidationAt == nil {
		t.Fatalf("resource validation was not requeued: %+v err=%v", storedResource, err)
	}
	if storedResource.Status != iCloudResourceNormal || !storedResource.ForSale || storedResource.NextProvisionAt == nil || !storedResource.NextProvisionAt.Equal(nextProvisionAt) {
		t.Fatalf("refresh changed resource availability: %+v", storedResource)
	}
}

func TestICloudRefreshRejectsResourceChangedAfterAppleCall(t *testing.T) {
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{"status": iCloudResourceNormal, "for_sale": true}).Error; err != nil {
		t.Fatal(err)
	}
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"resource_id": resource.ID, "task_kind": "refresh", "onboarding_status": iCloudOnboardingProcessing,
		"stage": "resource_refresh", "dispatch_status": "pending", "generation": uint64(1),
		"expected_credential_revision": resource.CredentialRevision, "secret_payload": iCloudJSON(secret), "max_attempts": 5,
		"updated_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var task iCloudOnboardingTaskModel
	if err := db.First(&task, resource.ID).Error; err != nil {
		t.Fatal(err)
	}
	provider := &refreshMutatingApple{
		mutate: func() {
			if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Update("bound_phone_number", "15488768537").Error; err != nil {
				panic(err)
			}
		},
		response: AppleOnboardingResponse{
			CountryCode: "US",
			NewChannel:  &AppleOnboardingChannel{Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "myacinfo=should-not-save"},
			OldChannel:  &AppleOnboardingChannel{Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com", Cookie: "X-APPLE-WEBAUTH-TOKEN=should-not-save"},
		},
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.onboardingApple = provider
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
		t.Fatal(err)
	}
	var storedTask iCloudOnboardingTaskModel
	if err := db.First(&storedTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedTask.Status != iCloudOnboardingFailed || storedTask.LastErrorCategory != "refresh_stale" {
		t.Fatalf("stale refresh task = %+v", storedTask)
	}
	var channel iCloudResourceChannelModel
	if err := db.Where("resource_id = ? AND kind = ?", resource.ID, iCloudChannelAppleAccount).First(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if channel.Cookie != "expired" {
		t.Fatalf("stale refresh wrote a cookie: %+v", channel)
	}
}

func TestICloudOldCookieBackfillRejectsResourceChangedAfterAppleCall(t *testing.T) {
	now := time.Date(2026, 8, 17, 9, 5, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{"status": iCloudResourceNormal, "for_sale": true}).Error; err != nil {
		t.Fatal(err)
	}
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"resource_id": resource.ID, "task_kind": "refresh", "onboarding_status": iCloudOnboardingProcessing,
		"stage": "old_cookie_finish", "dispatch_status": "pending", "generation": uint64(1),
		"expected_credential_revision": resource.CredentialRevision, "secret_payload": iCloudJSON(secret),
		"pending_sms_purpose": appleSMSOldCookieLogin, "max_attempts": 5, "updated_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var task iCloudOnboardingTaskModel
	if err := db.First(&task, resource.ID).Error; err != nil {
		t.Fatal(err)
	}
	provider := &refreshMutatingApple{
		mutate: func() {
			if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Update("kitesim_phone_id", 8).Error; err != nil {
				panic(err)
			}
		},
		response: AppleOnboardingResponse{
			ICloudOpened: func() *bool { opened := true; return &opened }(),
			OldChannel:   &AppleOnboardingChannel{Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com", Cookie: "should-not-save"},
		},
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.onboardingApple = provider
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
		t.Fatal(err)
	}
	var storedTask iCloudOnboardingTaskModel
	if err := db.First(&storedTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedTask.Status != iCloudOnboardingFailed || storedTask.LastErrorCategory != "refresh_stale" {
		t.Fatalf("stale old-cookie task = %+v", storedTask)
	}
	var channel iCloudResourceChannelModel
	if err := db.Where("resource_id = ? AND kind = ?", resource.ID, iCloudChannelWeb).First(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if channel.Cookie != "expired" {
		t.Fatalf("stale old-cookie refresh wrote a cookie: %+v", channel)
	}
}

func TestICloudRefreshInfrastructureRetriesPreserveResourceState(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	nextProvisionAt := now.Add(20 * time.Minute)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"status": iCloudResourceNormal, "for_sale": true, "next_provision_at": nextProvisionAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	secret, _ := json.Marshal(iCloudOnboardingSecret{Password: "Secret1!", Birthday: "2000-11-02"})
	resourceID := resource.ID
	task := iCloudOnboardingTaskModel{
		ResourceID: &resourceID, TaskKind: "refresh", PrimaryEmail: resource.PrimaryEmail, AccountRole: "primary",
		Region: resource.Region, CountryCode: resource.CountryCode, ICloudOpened: true,
		BoundPhoneNumber: resource.BoundPhoneNumber, BoundPhoneCountryCode: "US", BoundPhoneSource: "manual",
		SecretPayload: secret, SessionPayload: []byte(`{"session":true}`), ManualVerificationCode: "123456",
		Status: iCloudOnboardingProcessing, Stage: "icloud_prepare", DispatchStatus: "running", ClaimToken: "claim",
		Generation: 1, ExpectedCredentialRevision: resource.CredentialRevision, Attempts: 1, MaxAttempts: 2,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.ReleaseICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}, "temporary failure"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != iCloudOnboardingFailed || len(task.SecretPayload) != 0 || len(task.SessionPayload) != 0 || task.ManualVerificationCode != "" {
		t.Fatalf("refresh task = %+v", task)
	}
	if resource.Status != iCloudResourceNormal || !resource.ForSale || resource.NextProvisionAt == nil || !resource.NextProvisionAt.Equal(nextProvisionAt) {
		t.Fatalf("refresh resource = %+v", resource)
	}
}
