package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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

type refreshTrustedPhoneApple struct{}

func (refreshTrustedPhoneApple) Execute(context.Context, AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	return AppleOnboardingResponse{Session: json.RawMessage(`{"flow":"ok"}`), Next: appleSMSManageLogin, TrustedPhoneLastTwo: "01"}, nil
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
		&iCloudOnboardingImportModel{}, &iCloudOnboardingTaskModel{},
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

func TestEnsureICloudCookieRefreshRejectsUnrelatedInsertConflict(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 5, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
	if err := db.Exec(`CREATE TRIGGER ignore_refresh_insert BEFORE INSERT ON icloud_account_onboarding_tasks
		BEGIN SELECT RAISE(IGNORE); END`).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.EnsureICloudCookieRefresh(context.Background(), resource.ID); !errors.Is(err, ErrICloudOnboardingTemporary) {
		t.Fatalf("unrelated ignored insert error = %v", err)
	}
	var count int64
	if err := db.Model(&iCloudOnboardingTaskModel{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("refresh tasks=%d err=%v", count, err)
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
}

func TestICloudRefreshInfrastructureRetriesMarkResourceAbnormal(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	db := newICloudRefreshTestDB(t)
	resource := seedICloudRefreshResource(t, db, now)
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
	if resource.Status != iCloudResourceAbnormal || resource.NextValidationAt != nil || resource.NextProvisionAt != nil {
		t.Fatalf("refresh resource = %+v", resource)
	}
}
