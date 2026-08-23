package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/kitesim"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestParseICloudOnboardingImportTreatsEveryEntryAsChild(t *testing.T) {
	content := []byte(
		"美国区----是----primary@example.com----Secret1!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----2000-11-02----https://www.icloud.com/family/invite?inviteCode=abc\n" +
			"加拿大区----否----child@example.com----Secret2!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----1999-01-03----14165550001\n" +
			"日本区----否----child-no-phone@example.com----Secret3!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----1998-01-04\n" +
			"英国区----是----primary-with-phone@example.com----Secret4!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----1997-01-05----447700900001----family-token-123\n" +
			"美国区----是----primary-empty-phone@example.com----Secret5!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----1996-01-06--------https://www.icloud.com/family/invite?inviteCode=def\n",
	)
	lines, err := parseICloudOnboardingImport(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("lines = %d", len(lines))
	}
	if lines[0].AccountRole != "child" || lines[0].CountryCode != "US" || lines[0].PhoneNumber != "" || lines[0].FamilyInviteURL == "" || !lines[0].ICloudOpened {
		t.Fatalf("direct-invite child = %+v", lines[0])
	}
	if lines[1].AccountRole != "child" || lines[1].CountryCode != "CA" || lines[1].PhoneNumber != "14165550001" || lines[1].FamilyInviteURL != "" || lines[1].ICloudOpened {
		t.Fatalf("child = %+v", lines[1])
	}
	if lines[2].AccountRole != "child" || lines[2].CountryCode != "JP" || lines[2].PhoneNumber != "" || lines[2].FamilyInviteURL != "" {
		t.Fatalf("child without phone = %+v", lines[2])
	}
	if lines[3].AccountRole != "child" || lines[3].CountryCode != "GB" || lines[3].PhoneNumber != "447700900001" || lines[3].FamilyInviteURL != "family-token-123" {
		t.Fatalf("child with phone = %+v", lines[3])
	}
	if lines[4].AccountRole != "child" || lines[4].PhoneNumber != "" || lines[4].FamilyInviteURL == "" {
		t.Fatalf("child with empty phone field = %+v", lines[4])
	}
	if lines[0].Secret.SecurityAnswers[1].Answer != "remail2" || lines[0].Secret.Birthday != "2000-11-02" {
		t.Fatalf("secret parser = %+v", lines[0].Secret)
	}
	if _, err := parseICloudOnboardingImport([]byte("美国区----也许----bad@example.com----x----q(a)----q(b)----q(c)----2000-01-01")); !errors.Is(err, ErrICloudOnboardingInvalid) {
		t.Fatalf("invalid flag error = %v", err)
	}
	if lines, err := parseICloudOnboardingImport([]byte("美国区----否----child-invite@example.com----x----q(a)----q(b)----q(c)----2000-01-01----invite-token")); err != nil || len(lines) != 1 || lines[0].AccountRole != "child" {
		t.Fatalf("unopened direct-invite child = lines=%+v err=%v", lines, err)
	}
}

func TestHasICloudDirectFamilyInviteExcludesLegacyPrimaryTasks(t *testing.T) {
	cases := []struct {
		name     string
		taskKind string
		role     string
		invite   string
		expected bool
	}{
		{name: "direct child", taskKind: "onboarding", role: "child", invite: "invite", expected: true},
		{name: "legacy primary", taskKind: "onboarding", role: "primary", invite: "invite", expected: false},
		{name: "refresh child", taskKind: "refresh", role: "child", invite: "invite", expected: false},
		{name: "child without invite", taskKind: "onboarding", role: "child", expected: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasICloudDirectFamilyInvite(&iCloudOnboardingTaskModel{
				TaskKind: tc.taskKind, AccountRole: tc.role, FamilyInviteURL: tc.invite,
			})
			if got != tc.expected {
				t.Fatalf("hasICloudDirectFamilyInvite() = %t, want %t", got, tc.expected)
			}
		})
	}
}

func TestAcceptICloudOnboardingRejectsExistingResourceAfterIdempotencyLookup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudAppleIDReservationModel{}, &iCloudResourceModel{}, &iCloudResourceCredentialModel{},
		&governanceinfra.OperationLogModel{}, &coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 1, Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{ID: 1, PrimaryEmail: "existing@example.com", AccountRole: "primary", Status: iCloudResourceNormal}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(24 * time.Hour)
	content := []byte("美国区----是----existing@example.com----Secret1!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----2000-11-02")
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })

	if _, _, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, content, expiresAt, "new-key", "request", "/test"); !errors.Is(err, ErrICloudResourceIdentity) {
		t.Fatalf("duplicate resource error = %v", err)
	}
	var taskCount int64
	if err := db.Model(&iCloudOnboardingTaskModel{}).Where("task_kind = ?", "onboarding").Count(&taskCount).Error; err != nil || taskCount != 0 {
		t.Fatalf("task count = %d, err = %v", taskCount, err)
	}
	pendingContent := []byte("美国区----否----pending@example.com----Secret1!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----2000-11-02----14155550001")
	pendingView, reused, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, pendingContent, expiresAt, "pending-key", "request", "/test")
	if err != nil || reused || len(pendingView.Tasks) != 1 || pendingView.Tasks[0].ResourceID == nil {
		t.Fatalf("pending acceptance = view=%+v reused=%v err=%v", pendingView, reused, err)
	}
	pendingID := *pendingView.Tasks[0].ResourceID
	var pending iCloudResourceModel
	if err := db.First(&pending, pendingID).Error; err != nil || pending.PrimaryEmail != "pending@example.com" ||
		pending.AccountRole != "child" || pending.Status != iCloudResourcePending || pending.ForSale || pending.NextValidationAt != nil {
		t.Fatalf("pending resource = %+v err=%v", pending, err)
	}
	var credential iCloudResourceCredentialModel
	if err := db.First(&credential, pendingID).Error; err != nil || credential.ApplePassword != "Secret1!" || len(credential.SecurityAnswers) == 0 {
		t.Fatalf("pending credential = %+v err=%v", credential, err)
	}
	if err := db.Create(&iCloudRootModel{ID: 20, Type: "icloud", OwnerUserID: 1, Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{ID: 20, PrimaryEmail: "legacy@example.com", AccountRole: "unknown", Status: iCloudResourceNormal}).Error; err != nil {
		t.Fatal(err)
	}
	legacyContent := []byte("美国区----否----legacy@example.com----Secret1!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----2000-11-02----14155550001")
	legacyView, reused, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, legacyContent, expiresAt, "legacy-key", "request", "/test")
	if err != nil || reused || len(legacyView.Tasks) != 1 || legacyView.Tasks[0].ResourceID == nil || *legacyView.Tasks[0].ResourceID != 20 {
		t.Fatalf("legacy acceptance = view=%+v reused=%v err=%v", legacyView, reused, err)
	}
	var legacy iCloudResourceModel
	if err := db.First(&legacy, 20).Error; err != nil || legacy.AccountRole != "child" || legacy.Status != iCloudResourcePending || legacy.ForSale {
		t.Fatalf("legacy resource was not prepared: %+v err=%v", legacy, err)
	}
	var legacyRoot iCloudRootModel
	if err := db.First(&legacyRoot, 20).Error; err != nil || legacyRoot.Version != 2 {
		t.Fatalf("legacy resource version = %+v err=%v", legacyRoot, err)
	}

	replayContent := []byte("美国区----是----replay@example.com----Secret1!----问题一?(remail1)----问题二?(remail2)----问题三?(remail3)----2000-11-02")
	first, reused, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, replayContent, expiresAt, "replay-key", "request", "/test")
	if err != nil || reused || first == nil {
		t.Fatalf("initial idempotent import = view %+v, reused %v, err %v", first, reused, err)
	}
	view, reused, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, replayContent, expiresAt, "replay-key", "request", "/test")
	if err != nil || !reused || view.ImportID != first.ImportID {
		t.Fatalf("idempotent replay = view %+v, reused %v, err %v", view, reused, err)
	}
}

func TestAcceptICloudOnboardingRejectsActiveUnknownWorkflow(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Model(task).Updates(map[string]any{
		"account_role": "unknown", "task_kind": "onboarding", "onboarding_status": iCloudOnboardingProcessing,
		"dispatch_status": "running", "for_sale": false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	content := []byte("美国区----否----child@example.com----Secret2!----问题一?(a1)----问题二?(a2)----问题三?(a3)----2000-11-02")
	if _, _, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, content, service.now().Add(time.Hour), "active-unknown-key", "request", "/test"); !errors.Is(err, ErrICloudResourceIdentity) {
		t.Fatalf("active unknown workflow error = %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if resource.AccountRole != "unknown" || resource.WorkflowTaskKind != "onboarding" || resource.OnboardingStatus != iCloudOnboardingProcessing {
		t.Fatalf("active unknown workflow was overwritten: %+v", resource)
	}
}

func TestAcceptICloudOnboardingRejectsPermanentPhoneChangeBeforeUpdate(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	phoneID := uint(7)
	if err := db.Model(task).Updates(map[string]any{
		"account_role": "unknown", "task_kind": "", "onboarding_status": "", "kitesim_phone_id": phoneID,
		"bound_phone_number": "15488768536", "credential_revision": 2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	content := []byte("美国区----否----child@example.com----Secret2!----问题一?(a1)----问题二?(a2)----问题三?(a3)----2000-11-02----5488768537")
	if _, _, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, content, service.now().Add(time.Hour), "phone-conflict-key", "request", "/test"); !errors.Is(err, ErrICloudResourceIdentity) {
		t.Fatalf("permanent phone conflict error = %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if resource.BoundPhoneNumber != "15488768536" || resource.CredentialRevision != 2 || resource.KitesimPhoneID == nil || *resource.KitesimPhoneID != phoneID {
		t.Fatalf("permanent phone was overwritten: %+v", resource)
	}
}

func TestAcceptICloudOnboardingRejectsExclusivePhoneBeforeCreatingResource(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Exec("CREATE TABLE kitesim_phones (id INTEGER PRIMARY KEY, phone_code TEXT, phone_number TEXT, deleted_at DATETIME)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO kitesim_phones (id, phone_code, phone_number) VALUES (?, ?, ?)", 7, "1", "4155550001").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(task).Updates(map[string]any{
		"kitesim_phone_id": nil, "bound_phone_number": "14155550001", "alias_count": 10,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	content := []byte("美国区----否----new@example.com----Secret2!----问题一?(a1)----问题二?(a2)----问题三?(a3)----2000-11-02----14155550001")
	if _, _, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, content, service.now().Add(time.Hour), "exclusive-phone-key", "request", "/test"); !errors.Is(err, ErrICloudOnboardingPhoneExclusive) {
		t.Fatalf("exclusive phone error = %v", err)
	}
	var count int64
	if err := db.Model(&iCloudResourceModel{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("partial resource created: count=%d err=%v", count, err)
	}
}

func TestSameICloudPhoneNumberAcceptsCountryCodeVariants(t *testing.T) {
	for _, value := range []string{"+15488768536", "15488768536", "5488768536"} {
		if !sameICloudPhoneNumber("15488768536", value) {
			t.Fatalf("phone variant %q did not match", value)
		}
	}
	if sameICloudPhoneNumber("15488768536", "5488768537") {
		t.Fatal("different phone matched permanent binding")
	}
}

func TestICloudOnboardingClassifiesDuplicateResourceErrors(t *testing.T) {
	if !isICloudOnboardingDuplicateResourceError(fmt.Errorf("existing: %w", ErrICloudResourceIdentity)) {
		t.Fatal("resource identity error was not classified as duplicate")
	}
	if !isICloudOnboardingDuplicateResourceError(gorm.ErrDuplicatedKey) {
		t.Fatal("database duplicate key was not classified as duplicate")
	}
}

type onboardingFakeApple struct{ operations []string }

type onboardingRequestApple struct {
	request AppleOnboardingRequest
}

func (f *onboardingRequestApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	f.request = request
	return AppleOnboardingResponse{Next: "ready"}, nil
}

func TestICloudOnboardingSpecifiedPhoneDoesNotSkipTrustedPhoneEnrollment(t *testing.T) {
	for _, test := range []struct {
		name        string
		taskKind    string
		accountRole string
		wantSkip    bool
	}{
		{name: "ordinary onboarding", taskKind: "onboarding", accountRole: "child"},
		{name: "refresh", taskKind: "refresh", accountRole: "child", wantSkip: true},
		{name: "cookie recovery", taskKind: iCloudCookieRecoveryTaskKind, accountRole: "child", wantSkip: true},
		{name: "primary", taskKind: "onboarding", accountRole: "primary", wantSkip: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &onboardingRequestApple{}
			service := &Service{onboardingApple: provider}
			task := &iCloudOnboardingTaskModel{
				TaskKind: test.taskKind, AccountRole: test.accountRole, PrimaryEmail: "child@example.com",
				BoundPhoneNumber: "14155550001", BoundPhoneCountryCode: "US", BoundPhoneSource: "manual",
			}
			_, err := service.executeICloudOnboardingApple(context.Background(), task, iCloudOnboardingSecret{Password: "secret"}, AppleOnboardingRequest{Operation: appleOnboardingPrepareICloud})
			if err != nil {
				t.Fatal(err)
			}
			if provider.request.SkipPhoneEnrollment != test.wantSkip {
				t.Fatalf("SkipPhoneEnrollment = %t, want %t", provider.request.SkipPhoneEnrollment, test.wantSkip)
			}
			if provider.request.PhoneNumber != task.BoundPhoneNumber || provider.request.PhoneCountryCode != task.BoundPhoneCountryCode {
				t.Fatalf("phone context = %q/%q, want %q/%q", provider.request.PhoneNumber, provider.request.PhoneCountryCode, task.BoundPhoneNumber, task.BoundPhoneCountryCode)
			}
		})
	}
}

func (f *onboardingFakeApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	f.operations = append(f.operations, request.Operation+":"+request.SMSPurpose)
	session := json.RawMessage(`{"flow":"ok"}`)
	switch request.Operation {
	case appleOnboardingPrepareICloud:
		if request.SkipPhoneEnrollment {
			return AppleOnboardingResponse{Session: session, Next: "ready"}, nil
		}
		return AppleOnboardingResponse{Session: session, Next: appleSMSPhoneEnrollment}, nil
	case appleOnboardingSendSMS:
		return AppleOnboardingResponse{Session: session}, nil
	case appleOnboardingVerifySMS:
		if request.Code != "123456" {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_code", SafeMessage: "Invalid code.", CodeRejected: true}
		}
		return AppleOnboardingResponse{Session: session}, nil
	default:
		return AppleOnboardingResponse{Session: session, Next: "ready"}, nil
	}
}

type onboardingManagePhoneApple struct{ operations []string }

func (f *onboardingManagePhoneApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	f.operations = append(f.operations, request.Operation)
	return AppleOnboardingResponse{Session: json.RawMessage(`{"flow":"ok"}`), Next: "ready", CountryCode: "US", TrustedPhoneLastTwo: "01"}, nil
}

type onboardingProvidedPhone struct{}

func (onboardingProvidedPhone) BindICloudSMSPhone(context.Context, string, string) (kitesim.SMSPhoneBinding, error) {
	return kitesim.SMSPhoneBinding{PhoneID: 7, PhoneNumber: "14155550001", CountryCode: "US"}, nil
}
func (onboardingProvidedPhone) BindICloudSMSPhoneBySuffix(context.Context, string, string) (kitesim.SMSPhoneBinding, error) {
	return kitesim.SMSPhoneBinding{PhoneID: 7, PhoneNumber: "14155550001", CountryCode: "US"}, nil
}
func (onboardingProvidedPhone) CheckSMSPhoneAvailable(context.Context, uint) error { return nil }
func (onboardingProvidedPhone) ReserveSMSChallenge(context.Context, uint, string, string, time.Time) (kitesim.SMSReservation, error) {
	return kitesim.SMSReservation{}, errors.New("unexpected reservation")
}
func (onboardingProvidedPhone) MarkSMSAttemptSent(context.Context, uint64) error       { return nil }
func (onboardingProvidedPhone) ConfirmSMSAttemptSent(context.Context, uint64) error    { return nil }
func (onboardingProvidedPhone) MarkSMSAttemptSendFailed(context.Context, uint64) error { return nil }
func (onboardingProvidedPhone) MarkSMSAttemptInfrastructureFailed(context.Context, uint64) error {
	return nil
}
func (onboardingProvidedPhone) GetSMSChallengeByOwner(context.Context, string) (kitesim.SMSChallenge, error) {
	return kitesim.SMSChallenge{}, errors.New("unexpected challenge lookup")
}
func (onboardingProvidedPhone) ClaimAppleSMSMessage(context.Context, uint64) (*kitesim.MessageItem, error) {
	return nil, errors.New("unexpected polling")
}
func (onboardingProvidedPhone) CompleteSMSChallenge(context.Context, uint64) error { return nil }
func (onboardingProvidedPhone) CancelSMSChallenge(context.Context, uint64) error   { return nil }

type onboardingBindingConflictPhone struct{ onboardingProvidedPhone }

func (onboardingBindingConflictPhone) BindICloudSMSPhone(context.Context, string, string) (kitesim.SMSPhoneBinding, error) {
	return kitesim.SMSPhoneBinding{}, kitesim.ErrSMSPhoneBindingConflict
}

type onboardingMissingChallengePhone struct{ onboardingProvidedPhone }

func (onboardingMissingChallengePhone) GetSMSChallengeByOwner(context.Context, string) (kitesim.SMSChallenge, error) {
	return kitesim.SMSChallenge{}, kitesim.ErrSMSReservationNotFound
}

type onboardingCountingPhone struct {
	onboardingProvidedPhone
	bindCalls int
}

func (p *onboardingCountingPhone) BindICloudSMSPhone(ctx context.Context, email, phone string) (kitesim.SMSPhoneBinding, error) {
	p.bindCalls++
	return p.onboardingProvidedPhone.BindICloudSMSPhone(ctx, email, phone)
}

func (p *onboardingCountingPhone) BindICloudSMSPhoneBySuffix(ctx context.Context, email, suffix string) (kitesim.SMSPhoneBinding, error) {
	p.bindCalls++
	return p.onboardingProvidedPhone.BindICloudSMSPhoneBySuffix(ctx, email, suffix)
}

type onboardingSMSFailurePhone struct {
	onboardingProvidedPhone
	cooldown time.Time
	failed   bool
}

type onboardingSMSCoolingPhone struct {
	onboardingProvidedPhone
	retryAt time.Time
}

func (p onboardingSMSCoolingPhone) ReserveSMSChallenge(context.Context, uint, string, string, time.Time) (kitesim.SMSReservation, error) {
	return kitesim.SMSReservation{}, &kitesim.SMSPhoneUnavailableError{RetryAt: p.retryAt, Reason: "cooling down"}
}

func (p onboardingSMSCoolingPhone) CheckSMSPhoneAvailable(context.Context, uint) error {
	return &kitesim.SMSPhoneUnavailableError{RetryAt: p.retryAt, Reason: "cooling down"}
}

func (p *onboardingSMSFailurePhone) ReserveSMSChallenge(context.Context, uint, string, string, time.Time) (kitesim.SMSReservation, error) {
	return kitesim.SMSReservation{ID: 9, PhoneID: 7, Status: kitesim.SMSChallengeReserved, CooldownUntil: p.cooldown, ExpiresAt: p.cooldown.Add(time.Minute)}, nil
}

func (p *onboardingSMSFailurePhone) MarkSMSAttemptSendFailed(context.Context, uint64) error {
	p.failed = true
	return nil
}

type onboardingSMSSuccessPhone struct {
	onboardingProvidedPhone
	sentAt      time.Time
	expiresAt   time.Time
	confirmed   bool
	completeErr error
}

func (p *onboardingSMSSuccessPhone) ReserveSMSChallenge(context.Context, uint, string, string, time.Time) (kitesim.SMSReservation, error) {
	return kitesim.SMSReservation{ID: 10, PhoneID: 7, Status: kitesim.SMSChallengeReserved, ExpiresAt: p.expiresAt}, nil
}

func (p *onboardingSMSSuccessPhone) ConfirmSMSAttemptSent(context.Context, uint64) error {
	p.confirmed = true
	return nil
}

func (p *onboardingSMSSuccessPhone) GetSMSChallengeByOwner(context.Context, string) (kitesim.SMSChallenge, error) {
	return kitesim.SMSChallenge{ID: 10, PhoneID: 7, Status: kitesim.SMSChallengeSent, SentAt: &p.sentAt, ExpiresAt: p.expiresAt}, nil
}

func (p *onboardingSMSSuccessPhone) CompleteSMSChallenge(context.Context, uint64) error {
	return p.completeErr
}

type onboardingSMSUncertainPhone struct {
	onboardingProvidedPhone
	status               string
	sentAt               time.Time
	expiresAt            time.Time
	markErr              error
	infrastructureFailed bool
}

func (p *onboardingSMSUncertainPhone) ReserveSMSChallenge(context.Context, uint, string, string, time.Time) (kitesim.SMSReservation, error) {
	if p.status == "" {
		p.status = kitesim.SMSChallengeReserved
	}
	return kitesim.SMSReservation{ID: 11, PhoneID: 7, Status: p.status, ExpiresAt: p.expiresAt}, nil
}

func (p *onboardingSMSUncertainPhone) MarkSMSAttemptSent(context.Context, uint64) error {
	p.status = kitesim.SMSChallengeSent
	return p.markErr
}

func (p *onboardingSMSUncertainPhone) MarkSMSAttemptInfrastructureFailed(context.Context, uint64) error {
	p.infrastructureFailed = true
	p.status = kitesim.SMSChallengeInfrastructureFailed
	return nil
}

func (p *onboardingSMSUncertainPhone) GetSMSChallengeByOwner(context.Context, string) (kitesim.SMSChallenge, error) {
	return kitesim.SMSChallenge{ID: 11, PhoneID: 7, Status: p.status, SentAt: &p.sentAt, ExpiresAt: p.expiresAt}, nil
}

type onboardingSMSUncertainApple struct{ calls int }

func (p *onboardingSMSUncertainApple) Execute(context.Context, AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.calls++
	return AppleOnboardingResponse{}, errors.New("SMS send result is unknown")
}

type onboardingVerifyCountingApple struct{ calls int }

func (p *onboardingVerifyCountingApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if request.Operation == appleOnboardingVerifySMS {
		p.calls++
	}
	return AppleOnboardingResponse{Session: json.RawMessage(`{"verified":true}`)}, nil
}

type onboardingExportApple struct{ calls int }

func (p *onboardingExportApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.calls++
	if request.Operation != appleOnboardingExport {
		return AppleOnboardingResponse{}, fmt.Errorf("unexpected operation %s", request.Operation)
	}
	return AppleOnboardingResponse{NewChannel: &AppleOnboardingChannel{
		Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "myacinfo=new-cookie",
	}}, nil
}

type onboardingSendRejectedApple struct{}

func (onboardingSendRejectedApple) Execute(context.Context, AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "sms_send_rejected", SafeMessage: "Apple rejected the verification SMS request.", SendRejected: true}
}

type onboardingFamilyInvalidApple struct{}

func (onboardingFamilyInvalidApple) Execute(context.Context, AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "family_invite_invalid", SafeMessage: "The family invitation is expired or invalid."}
}

type onboardingFamilyRestartApple struct {
	requests []AppleOnboardingRequest
}

func (p *onboardingFamilyRestartApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.requests = append(p.requests, request)
	if request.Operation == appleOnboardingJoinFamily {
		return AppleOnboardingResponse{}, appleOnboardingRestart("family_prepare")
	}
	return AppleOnboardingResponse{Session: json.RawMessage(`{"flow":"family-reconcile"}`), Next: "ready"}, nil
}

type onboardingJoinedFamilyApple struct {
	requests []AppleOnboardingRequest
}

func (p *onboardingJoinedFamilyApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.requests = append(p.requests, request)
	return AppleOnboardingResponse{Next: "ready", FamilyChannel: &AppleOnboardingChannel{
		Kind: iCloudChannelFamilySession, Cookie: testICloudFamilyCookie, SetupCookie: testICloudFamilyCookie,
	}}, nil
}

type onboardingClosedICloudApple struct{}

func (onboardingClosedICloudApple) Execute(_ context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	session := json.RawMessage(`{"flow":"icloud-closed"}`)
	switch request.Operation {
	case appleOnboardingPrepareICloud, appleOnboardingPrepareICloudCookie:
		return AppleOnboardingResponse{Session: session, Next: "ready"}, nil
	case appleOnboardingFinishICloud, appleOnboardingFinishICloudCookie:
		opened := false
		return AppleOnboardingResponse{Session: session, Next: "ready", ICloudOpened: &opened}, nil
	default:
		return AppleOnboardingResponse{}, fmt.Errorf("unexpected operation %s", request.Operation)
	}
}

func newOnboardingStateTest(t *testing.T) (*Service, *gorm.DB, *iCloudOnboardingTaskModel, *onboardingFakeApple) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudAppleIDReservationModel{},
		&iCloudResourceModel{}, &iCloudResourceCredentialModel{},
		&governanceinfra.OperationLogModel{}, &coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	secret, _ := json.Marshal(iCloudOnboardingSecret{
		Password: "Secret1!", Birthday: "2000-11-02",
		SecurityAnswers: [3]iCloudSecurityAnswer{{Question: "q1", Answer: "a1"}, {Question: "q2", Answer: "a2"}, {Question: "q3", Answer: "a3"}},
	})
	answers, _ := json.Marshal([3]iCloudSecurityAnswer{{Question: "q1", Answer: "a1"}, {Question: "q2", Answer: "a2"}, {Question: "q3", Answer: "a3"}})
	root := iCloudRootModel{Type: "icloud", OwnerUserID: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	resourceID := root.ID
	resource := iCloudResourceModel{
		ID: root.ID, ResourceType: "icloud", PrimaryEmail: "child@example.com", AccountRole: "child",
		Region: "美国区", CountryCode: "US", BoundPhoneNumber: "14155550001", Status: iCloudResourcePending,
		ExpireAt: now.AddDate(0, 1, 0), CredentialRevision: 1, CredentialUpdatedAt: now, ValidationGeneration: 1,
		WorkflowImportID: &resourceID, WorkflowResourceID: &resourceID, WorkflowTaskKind: "onboarding", WorkflowLineNumber: 1,
		WorkflowSecretPayload: iCloudJSON(secret), OnboardingStatus: iCloudOnboardingProcessing, WorkflowStage: "accepted",
		WorkflowDispatchStatus: "pending", WorkflowGeneration: 1, WorkflowExpectedCredential: 1, WorkflowMaxAttempts: 5,
		WorkflowOperatorUserID: 1, WorkflowIdempotencyKey: "key", WorkflowRequestFingerprint: "fingerprint",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceCredentialModel{
		ResourceID: root.ID, ApplePassword: "Secret1!", SecurityAnswers: iCloudJSON(answers),
		Birthday: time.Date(2000, 11, 2, 0, 0, 0, 0, time.UTC), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	task := &iCloudOnboardingTaskModel{}
	if err := db.First(task, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	fakeApple := &onboardingFakeApple{}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.smsPhones = onboardingProvidedPhone{}
	service.onboardingApple = fakeApple
	return service, db, task, fakeApple
}

func processOnboardingStageForTest(t *testing.T, service *Service, db *gorm.DB, task *iCloudOnboardingTaskModel) {
	t.Helper()
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); err != nil {
		t.Fatal(err)
	}
	var stored iCloudOnboardingTaskModel
	if err := db.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	*task = stored
}

func TestICloudOnboardingProvidedPhoneSkipsEnrollmentAndFamily(t *testing.T) {
	service, db, task, fakeApple := newOnboardingStateTest(t)
	processOnboardingStageForTest(t, service, db, task)
	if task.ResourceID == nil || task.Stage != "manage_prepare" || task.BoundPhoneSource != "manual" || task.KitesimPhoneID == nil {
		t.Fatalf("assigned task = %+v", task)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "manage_profile" {
		t.Fatalf("prepared task = %+v", task)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "forwarding_prepare" {
		t.Fatalf("profile task = %+v", task)
	}
	if len(fakeApple.operations) != 2 || fakeApple.operations[0] != appleOnboardingPrepareManage+":" || fakeApple.operations[1] != appleOnboardingFetchManage+":" {
		t.Fatalf("Apple operations = %v", fakeApple.operations)
	}
}

func TestICloudOnboardingSpecifiedPhoneAndInviteStartsICloudBeforeFamily(t *testing.T) {
	service, db, task, fakeApple := newOnboardingStateTest(t)
	if err := db.Model(task).Update("family_invite_url", "https://setup.icloud.com/family/messages?inviteCode=test").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "icloud_prepare" || task.BoundPhoneSource != "manual" {
		t.Fatalf("specified phone + invite skipped iCloud stage: %+v", task)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "sms_send" || task.PendingSMSPurpose != appleSMSPhoneEnrollment || len(fakeApple.operations) != 1 || fakeApple.operations[0] != appleOnboardingPrepareICloud+":" {
		t.Fatalf("specified phone + invite did not start enrollment: task=%+v operations=%v", task, fakeApple.operations)
	}
}

func TestICloudOnboardingRejectsMissingResourceProjection(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", task.ID).Updates(map[string]any{
		"resource_id": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ensureICloudOnboardingAppleIDReservation(context.Background(), task); !errors.Is(err, ErrICloudResourceIdentity) {
		t.Fatalf("missing resource projection error = %v", err)
	}
}

func TestICloudOnboardingChildWithoutPhoneStartsEnrollment(t *testing.T) {
	service, db, task, fakeApple := newOnboardingStateTest(t)
	if err := db.Model(task).Update("bound_phone_number", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "icloud_prepare" || task.BoundPhoneSource != "kitesim" || task.KitesimPhoneID == nil {
		t.Fatalf("child phone assignment = %+v", task)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "sms_send" || task.PendingSMSPurpose != appleSMSPhoneEnrollment || len(fakeApple.operations) != 1 || fakeApple.operations[0] != appleOnboardingPrepareICloud+":" {
		t.Fatalf("child enrollment flow = task=%+v operations=%v", task, fakeApple.operations)
	}
}

func TestICloudOnboardingAcceptedRejectsResourceCreatedAfterImport(t *testing.T) {
	service, db, task, apple := newOnboardingStateTest(t)
	if err := db.Create(&iCloudResourceModel{ID: 99, PrimaryEmail: strings.ToUpper(task.PrimaryEmail)}).Error; err != nil {
		t.Fatal(err)
	}
	phone := &onboardingCountingPhone{}
	service.smsPhones = phone

	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingFailed || task.LastErrorCategory != "duplicate_resource" || phone.bindCalls != 0 || len(apple.operations) != 0 {
		t.Fatalf("duplicate accepted task reached side effects: task=%+v binds=%d operations=%v", task, phone.bindCalls, apple.operations)
	}
}

func TestICloudOnboardingAdvancedStageRejectsConflictingReservationBeforeApple(t *testing.T) {
	service, db, task, apple := newOnboardingStateTest(t)
	if err := db.Create(&iCloudAppleIDReservationModel{
		EmailKey: iCloudImportEmailKey(task.PrimaryEmail), OwnerKind: iCloudAppleIDReservationImport,
		OwnerID: 99, CreatedAt: service.now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(task).Update("stage", "manage_prepare").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingFailed || task.LastErrorCategory != "duplicate_resource" || len(apple.operations) != 0 {
		t.Fatalf("conflicting reservation reached Apple: task=%+v operations=%v", task, apple.operations)
	}
	var reservation iCloudAppleIDReservationModel
	if err := db.First(&reservation, "email_key = ?", iCloudImportEmailKey(task.PrimaryEmail)).Error; err != nil || reservation.OwnerKind != iCloudAppleIDReservationImport || reservation.OwnerID != 99 {
		t.Fatalf("conflicting reservation changed: %+v err=%v", reservation, err)
	}
}

func TestICloudOnboardingPrimaryWithoutChallengeRestoresPermanentPhone(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	apple := &onboardingManagePhoneApple{}
	service.onboardingApple = apple
	service.smsPhones = onboardingProvidedPhone{}
	if err := db.Model(task).Updates(map[string]any{
		"account_role": "primary", "icloud_opened": true, "stage": "manage_profile",
		"bound_phone_number": "", "bound_phone_source": "", "kitesim_phone_id": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "forwarding_prepare" || task.KitesimPhoneID == nil || task.BoundPhoneNumber != "14155550001" || len(apple.operations) != 1 || apple.operations[0] != appleOnboardingFetchManage {
		t.Fatalf("primary phone was not restored: task=%+v operations=%v", task, apple.operations)
	}
}

func TestICloudOnboardingClosedICloudSkipsActivationDependentSteps(t *testing.T) {
	for _, test := range []struct {
		name        string
		taskKind    string
		accountRole string
		phoneSource string
		declared    bool
		nextStage   string
	}{
		{name: "auto child declared closed", taskKind: "onboarding", accountRole: "child", phoneSource: "kitesim", nextStage: "family_select"},
		{name: "auto child declared open", taskKind: "onboarding", accountRole: "child", phoneSource: "kitesim", declared: true, nextStage: "family_select"},
		{name: "manual child", taskKind: "onboarding", accountRole: "child", phoneSource: "manual", nextStage: "manage_prepare"},
		{name: "primary", taskKind: "onboarding", accountRole: "primary", phoneSource: "kitesim", declared: true, nextStage: "manage_prepare"},
		{name: "refresh", taskKind: "refresh", accountRole: "child", phoneSource: "manual", declared: true, nextStage: "manage_prepare"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, db, task, _ := newOnboardingStateTest(t)
			service.onboardingApple = onboardingClosedICloudApple{}
			updates := map[string]any{
				"task_kind": test.taskKind, "account_role": test.accountRole, "bound_phone_source": test.phoneSource,
				"icloud_opened": test.declared, "stage": "icloud_finish",
			}
			if test.taskKind == "refresh" {
				phoneID := uint(7)
				updates["resource_id"] = task.ID
				updates["kitesim_phone_id"] = phoneID
				updates["expected_credential_revision"] = task.ExpectedCredentialRevision
			}
			if err := db.Model(task).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}

			processOnboardingStageForTest(t, service, db, task)
			if task.Status != iCloudOnboardingProcessing || task.DispatchStatus != "pending" || task.Stage != test.nextStage ||
				task.ICloudOpened || task.ICloudActivationConfirmedAt != nil {
				t.Fatalf("closed iCloud continuation = %+v", task)
			}
		})
	}
}

func TestICloudOnboardingClosedICloudAfterFamilyContinuesToManage(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	service.onboardingApple = onboardingClosedICloudApple{}
	if err := db.Model(task).Updates(map[string]any{
		"stage": "icloud_cookie_finish", "bound_phone_source": "kitesim", "icloud_opened": true,
		"family_reservation_confirmed": true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingProcessing || task.DispatchStatus != "pending" || task.Stage != "manage_prepare" || task.ICloudOpened {
		t.Fatalf("after-family closed iCloud continuation = %+v", task)
	}
}

func TestICloudOldCookieBackfillStillWaitsForActivation(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	service.onboardingApple = onboardingClosedICloudApple{}
	phoneID := uint(7)
	if err := db.Model(task).Updates(map[string]any{
		"task_kind": "refresh", "stage": "old_cookie_finish", "pending_sms_purpose": appleSMSOldCookieLogin,
		"icloud_opened": true, "kitesim_phone_id": phoneID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "waiting" || task.Stage != "waiting_icloud_activation" || task.ICloudOpened {
		t.Fatalf("old Cookie backfill activation wait = %+v", task)
	}
}

func TestICloudOnboardingResourceImportRejectsMissingPermanentPhone(t *testing.T) {
	service, db, task, apple := newOnboardingStateTest(t)
	if err := db.Model(task).Updates(map[string]any{
		"account_role": "primary", "icloud_opened": true, "stage": "resource_import",
		"bound_phone_number": "", "bound_phone_source": "", "kitesim_phone_id": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingFailed || task.LastErrorCategory != "phone_binding_missing" || len(apple.operations) != 0 {
		t.Fatalf("missing binding reached Apple export: task=%+v operations=%v", task, apple.operations)
	}
}

func TestICloudOnboardingReauthenticatesWhenSavedOldCookieIsMissing(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	phoneID := uint(7)
	if err := db.Model(task).Updates(map[string]any{
		"account_role": "primary", "icloud_opened": true, "stage": "resource_import",
		"bound_phone_number": "14155550001", "bound_phone_source": "kitesim", "kitesim_phone_id": phoneID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	apple := &onboardingExportApple{}
	service.onboardingApple = apple
	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingProcessing || task.Stage != "icloud_cookie_prepare" || task.DispatchStatus != "pending" || task.Attempts != 1 || apple.calls != 1 {
		t.Fatalf("missing old Cookie recovery = task=%+v Apple calls=%d", task, apple.calls)
	}
}

func TestICloudOnboardingUpgradesUnknownResourceInPlace(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.AutoMigrate(
		&iCloudAliasModel{},
	); err != nil {
		t.Fatal(err)
	}
	now := service.now().UTC()
	resourceID := task.ID
	if err := db.Model(task).Updates(map[string]any{
		"account_role": "unknown", "status": iCloudResourceNormal, "task_kind": "",
		"onboarding_status": "", "for_sale": false, "credential_revision": 2,
		"validation_generation": 4, "alias_count": 1, "import_id": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudAliasModel{ResourceID: resourceID, Email: "old@icloud.com", Status: iCloudResourceNormal, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	content := []byte("美国区----否----child@example.com----Secret2!----问题一?(a1)----问题二?(a2)----问题三?(a3)----2000-11-02----14155550001")
	view, reused, err := service.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, content, now.Add(time.Hour), "upgrade-key", "request", "/test")
	if err != nil || reused || view == nil || len(view.Tasks) != 1 || view.Tasks[0].ResourceID == nil || *view.Tasks[0].ResourceID != resourceID {
		t.Fatalf("upgrade acceptance = view=%+v reused=%v err=%v", view, reused, err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, resourceID).Error; err != nil {
		t.Fatal(err)
	}
	if resource.ID != resourceID || resource.AccountRole != "child" || resource.BoundPhoneNumber != "14155550001" ||
		resource.CredentialRevision != 3 || resource.ValidationGeneration != 5 || resource.Status != iCloudResourcePending || resource.ForSale {
		t.Fatalf("upgraded resource = %+v", resource)
	}
	var rootCount, reservationCount int64
	if err := db.Model(&iCloudRootModel{}).Count(&rootCount).Error; err != nil || rootCount != 1 {
		t.Fatalf("root count = %d err=%v", rootCount, err)
	}
	if err := db.Model(&iCloudAppleIDReservationModel{}).Count(&reservationCount).Error; err != nil || reservationCount != 1 {
		t.Fatalf("reservation count = %d err=%v", reservationCount, err)
	}
	var alias iCloudAliasModel
	if err := db.First(&alias).Error; err != nil || alias.Status != iCloudResourceNormal {
		t.Fatalf("alias = %+v err=%v", alias, err)
	}
}

func TestICloudOnboardingResourceImportCompletesAfterFamilyJoin(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceCredentialModel{}, &iCloudResourceChannelModel{}, &iCloudImportPreparationModel{},
	); err != nil {
		t.Fatal(err)
	}
	now := service.now().UTC()
	operatorID := uint(1)
	preparation := iCloudImportPreparationModel{
		OperatorUserID: &operatorID, ForwardToEmail: "relay@example.com", VerificationCode: "654321",
		VerifiedAt: &now, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&preparation).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ensureICloudOnboardingAppleIDReservation(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil || task.ResourceID == nil {
		t.Fatalf("placeholder resource = %+v err=%v", task, err)
	}
	placeholderID := *task.ResourceID
	phoneID, primaryID := uint(7), uint(99)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "resource_import", "bound_phone_source": "kitesim", "kitesim_phone_id": phoneID,
		"forward_preparation_id": preparation.ID, "family_primary_resource_id": primaryID,
		"family_reservation_confirmed": true, "icloud_opened": false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	apple := &onboardingExportApple{}
	service.onboardingApple = apple

	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingCompleted || task.Stage != "completed" || task.ResourceID == nil ||
		*task.ResourceID != placeholderID || apple.calls != 1 {
		t.Fatalf("resource import completion = %+v Apple calls=%d", task, apple.calls)
	}
	var count int64
	if err := db.Model(&iCloudAppleIDReservationModel{}).
		Where("email_key = ? AND owner_kind = ? AND owner_id = ?", iCloudImportEmailKey(task.PrimaryEmail), iCloudAppleIDReservationOnboarding, *task.ImportID).
		Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("onboarding reservation count after completion = %d err=%v", count, err)
	}
	if task.NextAttemptAt != nil || task.FinishedAt == nil {
		t.Fatalf("completed task retained retry state: %+v", task)
	}
}

func TestSubmitICloudOnboardingSMSCodeUsesMissingPoolBinding(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Model(task).Updates(map[string]any{
		"onboarding_status": iCloudOnboardingWaiting, "stage": "sms_wait", "dispatch_status": "waiting",
		"bound_phone_source": "kitesim", "kitesim_phone_id": nil, "pending_sms_purpose": appleSMSManageLogin,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SubmitICloudOnboardingSMSCode(context.Background(), task.ID, 9, "123456", "req-sms", "/test/sms"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.ManualVerificationCode != "123456" || task.DispatchStatus != "pending" || task.Status != iCloudOnboardingProcessing {
		t.Fatalf("manual code was not accepted for the task shown as manual: %+v", task)
	}
	var audit governanceinfra.OperationLogModel
	if err := db.Where("operation_type = ?", "icloud.admin_account_onboarding.sms_code_submit").First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.OperatorUserID != 9 || audit.RequestID != "req-sms" || audit.Path != "/test/sms" || strings.Contains(audit.SafeSummary, "123456") || strings.Contains(audit.OperationType, "123456") {
		t.Fatalf("unsafe SMS audit = %+v", audit)
	}

	if err := db.Model(task).Updates(map[string]any{
		"onboarding_status": iCloudOnboardingWaiting, "stage": "sms_wait", "dispatch_status": "waiting",
		"bound_phone_source": "manual", "kitesim_phone_id": 7, "manual_verification_code": "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SubmitICloudOnboardingSMSCode(context.Background(), task.ID, 9, "654321", "req-sms-2", "/test/sms"); !errors.Is(err, ErrICloudOnboardingInvalid) {
		t.Fatalf("manual code was accepted despite a Kitesim binding: %v", err)
	}
}

func TestICloudOnboardingManualConfirmationsAreNaturallyIdempotent(t *testing.T) {
	t.Run("activation", func(t *testing.T) {
		service, db, task, _ := newOnboardingStateTest(t)
		if err := db.Model(task).Updates(map[string]any{
			"onboarding_status": iCloudOnboardingWaiting, "stage": "waiting_icloud_activation", "dispatch_status": "waiting",
			"bound_phone_source": "kitesim", "icloud_opened": false,
		}).Error; err != nil {
			t.Fatal(err)
		}
		for _, requestID := range []string{"activation-1", "activation-2"} {
			if err := service.ConfirmICloudOnboardingActivation(context.Background(), task.ID, 9, requestID, "/test/activation"); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.First(task, task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if task.Stage != "family_select" || task.ICloudActivationConfirmedAt == nil || task.ICloudOpened {
			t.Fatalf("activation confirmation = %+v", task)
		}
		var count int64
		if err := db.Model(&governanceinfra.OperationLogModel{}).Where("operation_type = ?", "icloud.admin_account_onboarding.activation_confirm").Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("activation audit count = %d err=%v", count, err)
		}
	})

	t.Run("family_reset", func(t *testing.T) {
		service, db, task, _ := newOnboardingStateTest(t)
		resourceID := task.ID
		primaryID := uint(99)
		if err := db.Model(task).Updates(map[string]any{
			"resource_id": resourceID, "family_primary_resource_id": primaryID, "family_reservation_confirmed": true,
			"task_kind":         "",
			"onboarding_status": iCloudOnboardingWaiting, "stage": "waiting_family_reset", "dispatch_status": "waiting",
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&iCloudAppleIDReservationModel{
			EmailKey: iCloudImportEmailKey(task.PrimaryEmail), OwnerKind: iCloudAppleIDReservationOnboarding,
			OwnerID: *task.ImportID, CreatedAt: service.now().UTC(),
		}).Error; err != nil {
			t.Fatal(err)
		}
		for _, requestID := range []string{"family-1", "family-2"} {
			if err := service.ConfirmICloudOnboardingFamilyReset(context.Background(), task.ID, 9, requestID, "/test/family-reset"); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.First(task, task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if task.Status != iCloudOnboardingCompleted || task.Stage != "completed" {
			t.Fatalf("family confirmation = %+v", task)
		}
		var reservationCount int64
		if err := db.Model(&iCloudAppleIDReservationModel{}).Count(&reservationCount).Error; err != nil || reservationCount != 0 {
			t.Fatalf("reservation count after family confirmation = %d err=%v", reservationCount, err)
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			return reserveICloudAppleIDsTx(tx, []iCloudAppleIDReservationModel{{
				EmailKey: task.PrimaryEmail, OwnerKind: iCloudAppleIDReservationImport, OwnerID: 77, CreatedAt: service.now().UTC(),
			}})
		}); err != nil {
			t.Fatalf("Cookie import could not reserve completed Apple ID: %v", err)
		}
		var count int64
		if err := db.Model(&governanceinfra.OperationLogModel{}).Where("operation_type = ?", "icloud.admin_account_onboarding.family_reset_confirm").Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("family audit count = %d err=%v", count, err)
		}
	})

	t.Run("family_sharing", func(t *testing.T) {
		service, db, task, _ := newOnboardingStateTest(t)
		resourceID := task.ID
		primaryID := uint(99)
		if err := db.Model(task).Updates(map[string]any{
			"resource_id": resourceID, "family_primary_resource_id": primaryID, "family_reservation_confirmed": true,
			"onboarding_status": iCloudOnboardingWaiting, "stage": iCloudOnboardingStageFamilySharing, "dispatch_status": "waiting",
			"session_payload": []byte(`{"temporary":true}`), "next_attempt_at": nil,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&iCloudAppleIDReservationModel{
			EmailKey: iCloudImportEmailKey(task.PrimaryEmail), OwnerKind: iCloudAppleIDReservationOnboarding,
			OwnerID: *task.ImportID, CreatedAt: service.now().UTC(),
		}).Error; err != nil {
			t.Fatal(err)
		}
		for _, requestID := range []string{"sharing-1", "sharing-2"} {
			if err := service.ConfirmICloudOnboardingFamilyReset(context.Background(), task.ID, 9, requestID, "/test/family-sharing"); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.First(task, task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if task.Status != iCloudOnboardingProcessing || task.Stage != "manage_prepare" || task.DispatchStatus != "pending" ||
			task.Generation != 2 || task.SessionPayload != nil || task.FinishedAt != nil || !task.FamilyReservationConfirmed {
			t.Fatalf("family sharing confirmation = %+v", task)
		}
		var reservationCount int64
		if err := db.Model(&iCloudAppleIDReservationModel{}).Count(&reservationCount).Error; err != nil || reservationCount != 1 {
			t.Fatalf("reservation count after family sharing confirmation = %d err=%v", reservationCount, err)
		}
		var auditCount int64
		if err := db.Model(&governanceinfra.OperationLogModel{}).Where("operation_type = ?", "icloud.admin_account_onboarding.family_reset_confirm").Count(&auditCount).Error; err != nil || auditCount != 1 {
			t.Fatalf("family sharing audit count = %d err=%v", auditCount, err)
		}
	})
}

func TestICloudOnboardingInvalidFamilyReselectsAnotherPrimary(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}); err != nil {
		t.Fatal(err)
	}
	syncedAt := service.now().UTC()
	for _, resource := range []iCloudResourceModel{
		{ID: 10, PrimaryEmail: "disabled@example.com", AccountRole: "primary", Region: "美国区", CountryCode: "US", FamilyInviteURL: "old-token", Status: iCloudResourceDisabled},
		{ID: 11, PrimaryEmail: "available@example.com", AccountRole: "primary", Region: "美国区", CountryCode: "US", FamilyInviteURL: "new-token", Status: iCloudResourceNormal,
			FamilyID: "family-11", FamilyOrganizerDSID: "organizer-11", FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: &syncedAt},
	} {
		if err := db.Create(&iCloudRootModel{ID: resource.ID, Type: "icloud", OwnerUserID: 1, Version: 1}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&resource).Error; err != nil {
			t.Fatal(err)
		}
	}
	disabledID := uint(10)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "family_prepare", "family_primary_resource_id": disabledID, "bound_phone_source": "kitesim",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "family_select" || task.Status != iCloudOnboardingProcessing || task.Attempts != 1 || task.FamilyPrimaryResourceID != nil {
		t.Fatalf("invalid family reset = %+v", task)
	}
	var isolated iCloudResourceModel
	if err := db.First(&isolated, disabledID).Error; err != nil || isolated.FamilySyncErrorCategory != "" {
		t.Fatalf("disabled primary invitation state was mutated: resource=%+v err=%v", isolated, err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "family_prepare" || task.FamilyPrimaryResourceID == nil || *task.FamilyPrimaryResourceID != 11 {
		t.Fatalf("replacement family selection = %+v", task)
	}
}

func TestICloudOnboardingFamilySelectionRejectsUnresolvedCountry(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Model(task).Updates(map[string]any{"stage": "family_select", "country_code": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingFailed || task.DispatchStatus != "failed" || task.LastErrorCategory != "country_unresolved" || task.NextAttemptAt != nil {
		t.Fatalf("unresolved country did not terminate family selection: %+v", task)
	}
	if task.ResourceID == nil {
		t.Fatal("failed onboarding task lost its resource")
	}
	var failedResource iCloudResourceModel
	if err := db.First(&failedResource, *task.ResourceID).Error; err != nil || failedResource.Status != iCloudResourceAbnormal || failedResource.ForSale || failedResource.LastSafeError == "" {
		t.Fatalf("failed onboarding resource = %+v err=%v", failedResource, err)
	}
}

func TestICloudOnboardingInvalidFamilyInviteReselectsWithinBudget(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	service.onboardingApple = onboardingFamilyInvalidApple{}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudRootModel{ID: 10, Type: "icloud", OwnerUserID: 1, Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	syncedAt := service.now().Add(-time.Minute)
	if err := db.Create(&iCloudResourceModel{
		ID: 10, PrimaryEmail: "primary@example.com", AccountRole: "primary", Region: "美国区", CountryCode: "US",
		FamilyInviteURL: "expired-token", FamilyID: "family-10", FamilyOrganizerDSID: "organizer-10",
		FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: &syncedAt, Status: iCloudResourceNormal,
	}).Error; err != nil {
		t.Fatal(err)
	}
	primaryID := uint(10)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "family_prepare", "family_primary_resource_id": primaryID, "bound_phone_source": "kitesim", "attempts": 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "family_select" || task.Status != iCloudOnboardingProcessing || task.Attempts != 2 || len(task.SessionPayload) != 0 || task.FamilyPrimaryResourceID != nil {
		t.Fatalf("expired family reset = %+v", task)
	}
	var isolated iCloudResourceModel
	if err := db.First(&isolated, primaryID).Error; err != nil || isolated.FamilySyncStatus != iCloudFamilySyncReady || isolated.FamilySyncErrorCategory != "family_invite_invalid" {
		t.Fatalf("invalid invite primary was not isolated: resource=%+v err=%v", isolated, err)
	}
	now := service.now()
	if err := service.persistICloudFamilyState(context.Background(), primaryID, iCloudFamilyStateUpdate{
		FamilyID: "family-10", OrganizerDSID: "organizer-10", Status: iCloudFamilySyncReady,
		SyncedAt: &now, NextSyncAt: iCloudTimePointer(now.Add(time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&isolated, primaryID).Error; err != nil || isolated.FamilySyncStatus != iCloudFamilySyncReady || isolated.FamilySyncErrorCategory != "family_invite_invalid" {
		t.Fatalf("family sync removed invitation quarantine: resource=%+v err=%v", isolated, err)
	}
	selected, err := service.selectICloudFamilyPrimaryID(context.Background(), db, &iCloudOnboardingTaskModel{ID: 999, CountryCode: "US"}, now)
	if err != nil || selected != 0 {
		t.Fatalf("quarantined invitation was selected after family sync: selected=%d err=%v", selected, err)
	}
}

func TestICloudOnboardingDirectInviteFailureDoesNotReselectPrimary(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	service.onboardingApple = onboardingFamilyInvalidApple{}
	if err := db.Model(task).Updates(map[string]any{
		"family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=direct-token",
		"stage":             "family_prepare",
		"attempts":          1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingProcessing || task.DispatchStatus != "pending" || task.Stage != "family_prepare" ||
		task.Attempts != 2 || task.LastErrorCategory != "family_invite_invalid" || task.FamilyInviteURL == "" {
		t.Fatalf("direct invite failure was routed through primary selection: %+v", task)
	}

	for attempts := 0; attempts < 10 && task.Status == iCloudOnboardingProcessing; attempts++ {
		processOnboardingStageForTest(t, service, db, task)
	}
	if task.Status != iCloudOnboardingFailed || task.LastErrorCategory != "family_invite_invalid" || strings.Contains(task.LastSafeError, "primary family") || task.Stage == "family_select" {
		t.Fatalf("direct invite failure lost its category or entered primary selection: %+v", task)
	}
}

func TestICloudOnboardingFamilyApplyInviteFailurePreservesOriginalFamily(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	service.onboardingApple = onboardingFamilyInvalidApple{}
	if err := db.AutoMigrate(&iCloudRootModel{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{10, 11} {
		if err := db.Create(&iCloudRootModel{ID: id, Type: "icloud", OwnerUserID: 1, Version: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	syncedAt := service.now().Add(-time.Minute)
	primaries := []iCloudResourceModel{
		{ID: 10, PrimaryEmail: "original@example.com", AccountRole: "primary", CountryCode: "US", FamilyInviteURL: "expired-token", FamilyID: "family-10", FamilyOrganizerDSID: "organizer-10", FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: &syncedAt, Status: iCloudResourceNormal},
		{ID: 11, PrimaryEmail: "alternate@example.com", AccountRole: "primary", CountryCode: "US", FamilyInviteURL: "alternate-token", FamilyID: "family-11", FamilyOrganizerDSID: "organizer-11", FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: &syncedAt, Status: iCloudResourceNormal},
	}
	if err := db.Create(&primaries).Error; err != nil {
		t.Fatal(err)
	}
	primaryID := uint(10)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "family_join_apply", "family_primary_resource_id": primaryID,
		"session_payload": []byte(`{"mode":"family","inviteToken":"expired-token"}`), "attempts": 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	view := iCloudOnboardingTaskView(*task)
	if task.Stage != "family_reconcile_prepare" || task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "waiting" ||
		task.FamilyPrimaryResourceID == nil || *task.FamilyPrimaryResourceID != primaryID || len(task.SessionPayload) != 0 || !view.NeedsPostFamilyRecovery {
		t.Fatalf("uncertain family apply was reselected: task=%+v view=%+v", task, view)
	}
	if err := db.First(&primaries[0], primaryID).Error; err != nil || primaries[0].FamilySyncErrorCategory != "family_invite_invalid" {
		t.Fatalf("original invitation was not isolated: primary=%+v err=%v", primaries[0], err)
	}
	if err := db.First(&primaries[1], 11).Error; err != nil || primaries[1].FamilySyncErrorCategory != "" {
		t.Fatalf("alternate family was mutated: primary=%+v err=%v", primaries[1], err)
	}
}

func TestICloudOnboardingFamilyApplyRestartReauthenticatesSameFamily(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.AutoMigrate(&iCloudRootModel{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudRootModel{ID: 10, Type: "icloud", OwnerUserID: 1, Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	primary := iCloudResourceModel{
		ID: 10, PrimaryEmail: "primary@example.com", AccountRole: "primary", CountryCode: "US",
		FamilyInviteURL: "replacement-token", FamilyID: "family-10", FamilyOrganizerDSID: "organizer-10", Status: iCloudResourceNormal,
	}
	if err := db.Create(&primary).Error; err != nil {
		t.Fatal(err)
	}
	primaryID := primary.ID
	if err := db.Model(task).Updates(map[string]any{
		"stage": "family_join_apply", "family_primary_resource_id": primaryID,
		"session_payload": []byte(`{"mode":"family","inviteToken":"old-token"}`), "attempts": 4, "max_attempts": 5,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	apple := &onboardingFamilyRestartApple{}
	service.onboardingApple = apple

	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "family_reconcile_prepare" || task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "waiting" ||
		task.Attempts != 5 || len(task.SessionPayload) != 0 || task.FamilyPrimaryResourceID == nil || *task.FamilyPrimaryResourceID != primaryID {
		t.Fatalf("family restart did not persist recovery target: %+v", task)
	}
	if err := service.RetryICloudOnboardingPostFamily(context.Background(), task.ID, 9, "family-restart", "request", "/retry"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "family_join_apply" || task.Status != iCloudOnboardingProcessing || task.FamilyPrimaryResourceID == nil || *task.FamilyPrimaryResourceID != primaryID {
		t.Fatalf("family reconciliation login did not return to apply: %+v", task)
	}
	if len(apple.requests) != 2 || apple.requests[1].Operation != appleOnboardingPrepareFamilyReconcile ||
		apple.requests[1].FamilyInviteURL != "replacement-token" || apple.requests[1].FamilyOrganizerEmail != "primary@example.com" {
		t.Fatalf("family reconciliation did not use the persisted primary: %+v", apple.requests)
	}
}

func TestICloudOnboardingFamilyApplyReconcilesPersistedIntentBeforeCurrentInvite(t *testing.T) {
	db := openICloudFamilyTestDB(t, "onboarding-apply-intent")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	primary := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		FamilyInviteURL: "", FamilyID: "family-1", FamilyOrganizerDSID: "organizer-dsid",
		FamilySyncStatus: iCloudFamilySyncReady, Status: iCloudResourceNormal,
		ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&primary).Error; err != nil {
		t.Fatal(err)
	}
	primaryID := primary.ID
	session := []byte(`{"version":1,"mode":"family","inviteToken":"persisted-invite","familyOrganizerEmail":"primary@example.com"}`)
	task := iCloudOnboardingTaskModel{
		ID: 10, TaskKind: "onboarding", PrimaryEmail: "child@example.com", AccountRole: "child", FamilyPrimaryResourceID: &primaryID,
		Status: iCloudOnboardingProcessing, Stage: "family_join_apply", DispatchStatus: "running",
		Generation: 1, ClaimToken: "claim", MaxAttempts: 5, SessionPayload: session,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	apple := &onboardingJoinedFamilyApple{}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.onboardingApple = apple
	service.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(testICloudFamilyResponse))}, nil
	})})

	if err := service.joinICloudOnboardingFamily(context.Background(), &task, iCloudOnboardingSecret{}); err != nil {
		t.Fatal(err)
	}
	if len(apple.requests) != 1 || apple.requests[0].Operation != appleOnboardingJoinFamily || string(apple.requests[0].Session) != string(session) || apple.requests[0].FamilyOrganizerEmail != "" {
		t.Fatalf("persisted family intent was not reconciled first: %+v", apple.requests)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&primary, primary.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Stage != iCloudOnboardingStageFamilySharing || task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "waiting" ||
		task.SessionPayload != nil || !task.FamilyReservationConfirmed || task.FamilyPrimaryResourceID == nil || *task.FamilyPrimaryResourceID != primary.ID || task.Attempts != 0 {
		t.Fatalf("family apply did not confirm the original primary: task=%+v", task)
	}
	if primary.FamilyRemoteMemberCount != 2 {
		t.Fatalf("family reconciliation was not committed: primary=%+v", primary)
	}

	if err := db.Model(&task).Updates(map[string]any{
		"onboarding_status": iCloudOnboardingProcessing, "stage": "family_join_apply", "dispatch_status": "running",
		"claim_token": "claim-2", "session_payload": session,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.joinICloudOnboardingFamily(context.Background(), &task, iCloudOnboardingSecret{}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Stage != "manage_prepare" || task.Status != iCloudOnboardingProcessing || task.DispatchStatus != "pending" {
		t.Fatalf("confirmed family recovery re-entered manual sharing wait: task=%+v", task)
	}
}

func TestICloudOnboardingWriteStagesPersistIntentBeforeAppleCall(t *testing.T) {
	for stage, apply := range map[string]string{
		"family_join_intent":       "family_join_apply",
		"forwarding_add_intent":    "forwarding_add_apply",
		"forwarding_verify_intent": "forwarding_verify_apply",
	} {
		t.Run(stage, func(t *testing.T) {
			service, db, task, fakeApple := newOnboardingStateTest(t)
			if err := db.Model(task).Updates(map[string]any{"stage": stage, "bound_phone_source": "kitesim", "bound_phone_number": "14155550001"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			processOnboardingStageForTest(t, service, db, task)
			if task.Stage != apply || len(fakeApple.operations) != 0 {
				t.Fatalf("intent was not persisted before Apple call: task=%+v operations=%v", task, fakeApple.operations)
			}
		})
	}
}

func TestICloudOnboardingPhoneBindingConflictFailsPermanently(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	service.smsPhones = onboardingBindingConflictPhone{}
	sent := service.now().UTC().Add(-time.Minute)
	deadline := service.now().UTC().Add(time.Minute)
	if err := db.Model(task).Updates(map[string]any{
		"pending_sms_purpose": appleSMSManageLogin, "manual_verification_code": "123456",
		"sms_sent_at": sent, "sms_poll_deadline": deadline,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingFailed || task.DispatchStatus != "failed" || task.LastErrorCategory != "phone_binding_conflict" || task.FinishedAt == nil || task.PendingSMSPurpose != "" || task.ManualVerificationCode != "" || task.SMSSentAt != nil || task.SMSPollDeadline != nil {
		t.Fatalf("binding conflict task = %+v", task)
	}
}

func TestICloudOnboardingProvidedPhoneUsesSMSPoolFailurePolicy(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	cooldown := service.now().UTC().Add(75 * time.Second)
	phone := &onboardingSMSFailurePhone{cooldown: cooldown}
	service.smsPhones = phone
	service.onboardingApple = onboardingSendRejectedApple{}
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_send", "bound_phone_source": "manual", "kitesim_phone_id": 7,
		"pending_sms_purpose": appleSMSManageLogin, "stage_attempts": 2, "session_payload": []byte(`{"prepared":true}`),
		"sms_poll_deadline": service.now().UTC().Add(2 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "pending" || task.Stage != "manage_prepare" || task.StageAttempts != 3 || task.NextAttemptAt == nil || !task.NextAttemptAt.Equal(cooldown) || !phone.failed || len(task.SessionPayload) != 0 || task.PendingSMSPurpose != "" {
		t.Fatalf("SMS pool policy was bypassed: task=%+v markedFailed=%v", task, phone.failed)
	}
}

func TestICloudOnboardingMissingSMSChallengeRestartsAuthentication(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	service.smsPhones = onboardingMissingChallengePhone{}
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_wait", "bound_phone_source": "manual", "kitesim_phone_id": 7,
		"pending_sms_purpose": appleSMSManageLogin, "stage_attempts": 1, "session_payload": []byte(`{"stale":true}`),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "manage_prepare" || task.StageAttempts != 2 || len(task.SessionPayload) != 0 || task.PendingSMSPurpose != "" {
		t.Fatalf("missing challenge reused stale Apple transaction: %+v", task)
	}
}

func TestICloudOnboardingLegacySMSSendRestartsBeforeReservation(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	phone := &onboardingSMSUncertainPhone{}
	apple := &onboardingSMSUncertainApple{}
	service.smsPhones = phone
	service.onboardingApple = apple
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_send", "bound_phone_source": "manual", "kitesim_phone_id": 7,
		"pending_sms_purpose": appleSMSManageLogin, "session_payload": []byte(`{"stale":true}`),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "manage_prepare" || len(task.SessionPayload) != 0 || task.PendingSMSPurpose != "" || phone.status != "" || apple.calls != 0 {
		t.Fatalf("legacy SMS send used a stale Apple transaction: task=%+v phone_status=%q Apple_calls=%d", task, phone.status, apple.calls)
	}
}

func TestICloudOnboardingSMSCooldownDoesNotKeepPreparedTransaction(t *testing.T) {
	service, db, task, apple := newOnboardingStateTest(t)
	retryAt := service.now().UTC().Add(6 * time.Minute)
	service.smsPhones = onboardingSMSCoolingPhone{retryAt: retryAt}
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_send", "bound_phone_source": "manual", "kitesim_phone_id": 7,
		"pending_sms_purpose": appleSMSManageLogin, "stage_attempts": 2, "session_payload": []byte(`{"prepared":true}`),
		"sms_poll_deadline": service.now().UTC().Add(2 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "manage_prepare" || task.StageAttempts != 2 || task.NextAttemptAt == nil || !task.NextAttemptAt.Equal(retryAt) || len(task.SessionPayload) != 0 || task.PendingSMSPurpose != "" || len(apple.operations) != 0 {
		t.Fatalf("SMS cooldown reached Apple send: task=%+v operations=%v", task, apple.operations)
	}
}

func TestICloudOnboardingChecksSMSCooldownBeforeEveryPhase(t *testing.T) {
	for _, stage := range []string{"icloud_prepare", "family_select", "manage_prepare"} {
		t.Run(stage, func(t *testing.T) {
			service, db, task, apple := newOnboardingStateTest(t)
			retryAt := service.now().UTC().Add(6 * time.Minute)
			service.smsPhones = onboardingSMSCoolingPhone{retryAt: retryAt}
			if err := db.Model(task).Updates(map[string]any{
				"stage": stage, "bound_phone_source": "manual", "kitesim_phone_id": 7,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			processOnboardingStageForTest(t, service, db, task)
			if task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "pending" || task.Stage != stage ||
				task.NextAttemptAt == nil || !task.NextAttemptAt.Equal(retryAt) || len(apple.operations) != 0 {
				t.Fatalf("cooling phone reached phase %s: task=%+v operations=%v", stage, task, apple.operations)
			}
		})
	}
}

func TestICloudOnboardingSMSRetryBudgetSurvivesFreshPrepare(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "icloud_prepare", "bound_phone_source": "kitesim", "stage_attempts": 3,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "sms_send" || task.StageAttempts != 3 || task.PendingSMSPurpose != appleSMSPhoneEnrollment {
		t.Fatalf("fresh Apple prepare reset SMS retry budget: %+v", task)
	}
}

func TestICloudOnboardingConfirmsAcceptedSMSSend(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	phone := &onboardingSMSSuccessPhone{sentAt: service.now().UTC(), expiresAt: service.now().UTC().Add(2 * time.Minute)}
	service.smsPhones = phone
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_send", "bound_phone_source": "manual", "kitesim_phone_id": 7,
		"pending_sms_purpose": appleSMSManageLogin, "sms_poll_deadline": service.now().UTC().Add(2 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if !phone.confirmed || task.Status != iCloudOnboardingWaiting || task.Stage != "sms_wait" || task.DispatchStatus != "pending" {
		t.Fatalf("accepted SMS send was not confirmed: confirmed=%v task=%+v", phone.confirmed, task)
	}
}

func TestICloudOnboardingSMSVerifyRecoveryNeverReplaysCode(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	phone := &onboardingSMSSuccessPhone{
		sentAt: service.now().UTC(), expiresAt: service.now().UTC().Add(2 * time.Minute),
		completeErr: errors.New("forced local completion failure"),
	}
	apple := &onboardingVerifyCountingApple{}
	service.smsPhones = phone
	service.onboardingApple = apple
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_verify", "bound_phone_source": "manual", "kitesim_phone_id": 7,
		"pending_sms_purpose": appleSMSManageLogin, "manual_verification_code": "123456",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	payload := iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}
	if err := service.ProcessICloudOnboardingTask(context.Background(), payload); err != nil {
		t.Fatalf("first verification error = %v", err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Stage != "manage_profile" || task.ManualVerificationCode != "" || task.DispatchStatus != "pending" || apple.calls != 1 {
		t.Fatalf("verification was not advanced after Apple success: task=%+v calls=%d", task, apple.calls)
	}
	if err := service.ProcessICloudOnboardingTask(context.Background(), payload); err != nil {
		t.Fatalf("stale generation retry = %v", err)
	}
	if apple.calls != 1 {
		t.Fatalf("verification code replayed after local completion failure: calls=%d", apple.calls)
	}
}

func TestICloudOnboardingUnknownSMSSendResultDoesNotResend(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	phone := &onboardingSMSUncertainPhone{sentAt: service.now().UTC(), expiresAt: service.now().UTC().Add(2 * time.Minute)}
	apple := &onboardingSMSUncertainApple{}
	service.smsPhones = phone
	service.onboardingApple = apple
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_send", "bound_phone_source": "manual", "kitesim_phone_id": 7,
		"pending_sms_purpose": appleSMSManageLogin, "sms_poll_deadline": service.now().UTC().Add(2 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}); !errors.Is(err, ErrICloudOnboardingTemporary) {
		t.Fatalf("unknown send result error = %v", err)
	}
	if phone.status != kitesim.SMSChallengeSent || phone.infrastructureFailed || apple.calls != 1 {
		t.Fatalf("unknown send result released challenge: status=%s infrastructureFailed=%v calls=%d", phone.status, phone.infrastructureFailed, apple.calls)
	}
	if err := service.ReleaseICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}, "temporary SMS send failure"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "sms_wait" || task.DispatchStatus != "pending" || apple.calls != 1 {
		t.Fatalf("unknown send result was retried: task=%+v calls=%d", task, apple.calls)
	}
}

func TestICloudOnboardingSendClaimRaceResumesPolling(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	phone := &onboardingSMSUncertainPhone{
		sentAt: service.now().UTC(), expiresAt: service.now().UTC().Add(2 * time.Minute),
		markErr: kitesim.ErrSMSChallengeInactive,
	}
	apple := &onboardingSMSUncertainApple{}
	service.smsPhones = phone
	service.onboardingApple = apple
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_send", "bound_phone_source": "manual", "kitesim_phone_id": 7,
		"pending_sms_purpose": appleSMSManageLogin, "sms_poll_deadline": service.now().UTC().Add(2 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Stage != "sms_wait" || task.DispatchStatus != "pending" || apple.calls != 0 {
		t.Fatalf("claimed SMS send was not resumed safely: task=%+v calls=%d", task, apple.calls)
	}
}

func TestICloudOnboardingAppleRetryPathsConsumeMaxAttempts(t *testing.T) {
	for name, providerErr := range map[string]*AppleOnboardingError{
		"restart": {Category: "session_expired", SafeMessage: "Session expired.", RestartStage: "manage_prepare"},
		"retry_at": func() *AppleOnboardingError {
			retryAt := time.Date(2026, 8, 16, 12, 2, 0, 0, time.UTC)
			return &AppleOnboardingError{Category: "apple_unavailable", SafeMessage: "Apple unavailable.", Retryable: true, RetryAt: &retryAt}
		}(),
		"retry_without_retry_at": {Category: "apple_unavailable", SafeMessage: "Apple unavailable.", Retryable: true},
	} {
		t.Run(name, func(t *testing.T) {
			service, db, task, _ := newOnboardingStateTest(t)
			if err := db.Model(task).Updates(map[string]any{
				"stage": "manage_profile", "dispatch_status": "running", "claim_token": "claim",
				"attempts": 4, "max_attempts": 5,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.handleICloudOnboardingAppleError(context.Background(), task, providerErr); err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if task.Status != iCloudOnboardingFailed || task.DispatchStatus != "failed" || task.Attempts != 5 {
				t.Fatalf("retry budget was bypassed: %+v", task)
			}
		})
	}
}

func TestICloudOnboardingPostFamilyFailuresRemainRecoverable(t *testing.T) {
	for _, test := range []struct {
		name      string
		stage     string
		confirmed bool
	}{
		{name: "confirmed family", stage: "manage_profile", confirmed: true},
		{name: "join result uncertain", stage: "family_join_apply"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, db, task, _ := newOnboardingStateTest(t)
			if err := service.ensureICloudOnboardingAppleIDReservation(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			primaryID, preparationID := uint(88), uint(77)
			if err := db.Model(task).Updates(map[string]any{
				"stage": test.stage, "dispatch_status": "running", "claim_token": "claim",
				"family_primary_resource_id": primaryID, "family_reservation_confirmed": test.confirmed,
				"forward_preparation_id": preparationID, "session_payload": []byte(`{"flow":"post-family"}`),
				"attempts": 4, "max_attempts": 5,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.failICloudOnboardingTask(context.Background(), task, "provider_rejected", "Apple rejected the request."); err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			view := iCloudOnboardingTaskView(*task)
			if task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "waiting" || task.Attempts != 5 ||
				!view.NeedsPostFamilyRecovery || len(task.SecretPayload) == 0 || len(task.SessionPayload) == 0 ||
				task.FamilyPrimaryResourceID == nil || *task.FamilyPrimaryResourceID != primaryID ||
				task.ForwardPreparationID == nil || *task.ForwardPreparationID != preparationID || task.FinishedAt != nil {
				t.Fatalf("post-family failure was not recoverable: task=%+v view=%+v", task, view)
			}
			if test.stage == "family_join_apply" && iCloudPostFamilyRecoveryStage(*task) != "family_join_apply" {
				t.Fatalf("uncertain family join would not resume reconciliation: %+v", task)
			}
			var reservations int64
			if err := db.Model(&iCloudAppleIDReservationModel{}).Where("owner_kind = ? AND owner_id = ?", iCloudAppleIDReservationOnboarding, *task.ImportID).Count(&reservations).Error; err != nil || reservations != 1 {
				t.Fatalf("post-family reservation count=%d err=%v", reservations, err)
			}
		})
	}
}

func TestICloudOnboardingPreJoinFailuresTerminateAndReleaseReservation(t *testing.T) {
	for _, stage := range []string{"family_prepare", "family_join_intent"} {
		t.Run(stage, func(t *testing.T) {
			service, db, task, _ := newOnboardingStateTest(t)
			if err := service.ensureICloudOnboardingAppleIDReservation(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			primaryID := uint(88)
			if err := db.Model(task).Updates(map[string]any{
				"stage": stage, "dispatch_status": "running", "claim_token": "claim",
				"family_primary_resource_id": primaryID, "family_reservation_confirmed": false,
				"session_payload": []byte(`{"flow":"pre-family"}`), "attempts": 4, "max_attempts": 5,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.failICloudOnboardingTask(context.Background(), task, "signin_rejected", "Apple rejected the sign-in request."); err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			view := iCloudOnboardingTaskView(*task)
			if task.Status != iCloudOnboardingFailed || task.DispatchStatus != "failed" || task.Attempts != 5 ||
				view.NeedsPostFamilyRecovery || len(task.SecretPayload) != 0 || len(task.SessionPayload) != 0 {
				t.Fatalf("pre-join failure retained a false recovery task: task=%+v view=%+v", task, view)
			}
			var reservations int64
			if err := db.Model(&iCloudAppleIDReservationModel{}).
				Where("owner_kind = ? AND owner_id = ?", iCloudAppleIDReservationOnboarding, *task.ImportID).
				Count(&reservations).Error; err != nil || reservations != 0 {
				t.Fatalf("pre-join reservation count=%d err=%v", reservations, err)
			}
		})
	}
}

func TestICloudOnboardingPostFamilyLeaseExhaustionRemainsRecoverable(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := service.ensureICloudOnboardingAppleIDReservation(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	primaryID, preparationID := uint(88), uint(77)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "forwarding_add_apply", "dispatch_status": "running", "claim_token": "claim",
		"family_primary_resource_id": primaryID, "family_reservation_confirmed": true,
		"forward_preparation_id": preparationID, "session_payload": []byte(`{"flow":"post-family"}`),
		"attempts": 4, "max_attempts": 5,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ReleaseICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}, "worker stopped"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "waiting" || task.LastErrorCategory != "infrastructure_retries_exhausted" ||
		!iCloudOnboardingTaskView(*task).NeedsPostFamilyRecovery || len(task.SecretPayload) == 0 || len(task.SessionPayload) == 0 ||
		task.ForwardPreparationID == nil || *task.ForwardPreparationID != preparationID {
		t.Fatalf("post-family lease exhaustion was not recoverable: %+v", task)
	}
}

func TestRetryICloudOnboardingPostFamilyIsIdempotentAndRestoresReservation(t *testing.T) {
	service, db, task, apple := newOnboardingStateTest(t)
	primaryID, preparationID := uint(88), uint(77)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "sms_verify", "onboarding_status": iCloudOnboardingWaiting, "dispatch_status": "waiting",
		"family_primary_resource_id": primaryID, "family_reservation_confirmed": true,
		"forward_preparation_id": preparationID, "session_payload": []byte(`{"flow":"post-family"}`),
		"pending_sms_purpose": appleSMSManageLogin, "manual_verification_code": "123456",
		"attempts": 5, "max_attempts": 5, "last_error_category": "sms_rejected", "last_safe_error": "SMS failed.",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := service.RetryICloudOnboardingPostFamily(context.Background(), task.ID, 9, "post-family-retry", "request", "/retry"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != iCloudOnboardingProcessing || task.DispatchStatus != "pending" || task.Stage != "manage_prepare" ||
		task.Generation != 2 || task.Attempts != 0 || task.StageAttempts != 0 || task.PendingSMSPurpose != "" || task.ManualVerificationCode != "" ||
		len(task.SecretPayload) == 0 || len(task.SessionPayload) == 0 || task.FamilyPrimaryResourceID == nil ||
		*task.FamilyPrimaryResourceID != primaryID || task.ForwardPreparationID == nil || *task.ForwardPreparationID != preparationID {
		t.Fatalf("post-family retry did not restore safe stage: %+v", task)
	}
	var reservations, audits, receipts int64
	if err := db.Model(&iCloudAppleIDReservationModel{}).Where("owner_kind = ? AND owner_id = ?", iCloudAppleIDReservationOnboarding, *task.ImportID).Count(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&governanceinfra.OperationLogModel{}).Where("operation_type = ?", "icloud.admin_account_onboarding.post_family_retry").Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&coreinfra.AdminResourceCommandReceiptModel{}).Count(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if reservations != 1 || audits != 1 || receipts != 1 || len(apple.operations) != 0 || iCloudOnboardingTaskView(*task).NeedsPostFamilyRecovery {
		t.Fatalf("post-family retry idempotency: reservations=%d audits=%d receipts=%d task=%+v", reservations, audits, receipts, task)
	}
}

func TestRetryICloudOnboardingPostFamilyReplacesUnusableForwardingPreparation(t *testing.T) {
	for _, test := range []struct {
		stage    string
		category string
	}{
		{stage: "forwarding_add_apply", category: "forward_add_failed"},
		{stage: "forwarding_verify_apply", category: "forward_code_rejected"},
		{stage: "forwarding_wait", category: "forwarding_preparation_expired"},
	} {
		t.Run(test.category, func(t *testing.T) {
			service, db, task, apple := newOnboardingStateTest(t)
			primaryID, preparationID := uint(88), uint(77)
			if err := db.Model(task).Updates(map[string]any{
				"stage": test.stage, "onboarding_status": iCloudOnboardingWaiting, "dispatch_status": "waiting",
				"family_primary_resource_id": primaryID, "family_reservation_confirmed": true,
				"forward_preparation_id": preparationID, "attempts": 5, "max_attempts": 5,
				"last_error_category": test.category, "last_safe_error": "Apple rejected forwarding.",
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}

			if err := service.RetryICloudOnboardingPostFamily(context.Background(), task.ID, 9, test.category, "request", "/retry"); err != nil {
				t.Fatal(err)
			}
			if err := db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if task.Stage != "forwarding_prepare" || task.ForwardPreparationID != nil || task.Status != iCloudOnboardingProcessing ||
				task.DispatchStatus != "pending" || len(apple.operations) != 0 {
				t.Fatalf("rejected forwarding preparation was reused: task=%+v apple=%v", task, apple.operations)
			}
		})
	}
}

func TestRetryICloudOnboardingPostFamilyRejectsNonRecoveryState(t *testing.T) {
	service, db, task, apple := newOnboardingStateTest(t)
	generation := task.Generation
	if err := service.RetryICloudOnboardingPostFamily(context.Background(), task.ID, 9, "not-recoverable", "request", "/retry"); !errors.Is(err, ErrICloudOnboardingInvalid) {
		t.Fatalf("error=%v", err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	var receipts, audits int64
	if err := db.Model(&coreinfra.AdminResourceCommandReceiptModel{}).Count(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&governanceinfra.OperationLogModel{}).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if task.Generation != generation || receipts != 0 || audits != 0 || len(apple.operations) != 0 {
		t.Fatalf("non-recovery retry mutated state: task=%+v receipts=%d audits=%d apple=%v", task, receipts, audits, apple.operations)
	}
}

func TestRetryICloudOnboardingPostFamilyReservationConflictStopsBeforeApple(t *testing.T) {
	service, db, task, apple := newOnboardingStateTest(t)
	primaryID := uint(88)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "family_join_apply", "onboarding_status": iCloudOnboardingWaiting, "dispatch_status": "waiting",
		"family_primary_resource_id": primaryID, "attempts": 5, "max_attempts": 5,
		"last_error_category": "provider_unavailable", "last_safe_error": "Apple family state is uncertain.",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudAppleIDReservationModel{
		EmailKey: iCloudImportEmailKey(task.PrimaryEmail), OwnerKind: iCloudAppleIDReservationOnboarding,
		OwnerID: *task.ImportID + 100, CreatedAt: service.now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	generation := task.Generation
	if err := service.RetryICloudOnboardingPostFamily(context.Background(), task.ID, 9, "reservation-conflict", "request", "/retry"); !errors.Is(err, ErrICloudResourceIdentity) {
		t.Fatalf("error=%v", err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	var receipts, audits int64
	if err := db.Model(&coreinfra.AdminResourceCommandReceiptModel{}).Count(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&governanceinfra.OperationLogModel{}).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if task.Generation != generation || task.Status != iCloudOnboardingWaiting || receipts != 0 || audits != 0 || len(apple.operations) != 0 {
		t.Fatalf("reservation conflict mutated state: task=%+v receipts=%d audits=%d apple=%v", task, receipts, audits, apple.operations)
	}
}

func TestICloudOnboardingFamilyReconciliationConsumesMaxAttempts(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := service.ensureICloudOnboardingAppleIDReservation(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	primaryID := uint(99)
	service.onboardingApple = &onboardingJoinedFamilyApple{}
	service.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})
	if err := db.Model(task).Updates(map[string]any{
		"stage": "family_join_apply", "family_primary_resource_id": primaryID,
		"attempts": 4, "max_attempts": 5, "forward_preparation_id": 77,
		"session_payload": []byte(`{"flow":"family-join-uncertain"}`),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}

	processOnboardingStageForTest(t, service, db, task)
	view := iCloudOnboardingTaskView(*task)
	if task.Status != iCloudOnboardingWaiting || task.DispatchStatus != "waiting" || task.Attempts != 5 ||
		task.LastErrorCategory != "provider_unavailable" || !view.NeedsPostFamilyRecovery || view.NeedsManualCode ||
		len(task.SecretPayload) == 0 || len(task.SessionPayload) == 0 || task.ForwardPreparationID == nil {
		t.Fatalf("family reconciliation retry budget = %+v", task)
	}
	var reservations int64
	if err := db.Model(&iCloudAppleIDReservationModel{}).
		Where("owner_kind = ? AND owner_id = ?", iCloudAppleIDReservationOnboarding, *task.ImportID).
		Count(&reservations).Error; err != nil || reservations != 1 {
		t.Fatalf("family reconciliation reservation count=%d err=%v", reservations, err)
	}
}

func TestICloudOnboardingExpiredForwardPreparationConsumesMaxAttempts(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.AutoMigrate(&iCloudImportPreparationModel{}); err != nil {
		t.Fatal(err)
	}
	operatorID := uint(1)
	preparation := iCloudImportPreparationModel{
		OperatorUserID: &operatorID, ForwardToEmail: "expired@example.com",
		ExpiresAt: service.now().UTC().Add(-time.Minute), CreatedAt: service.now().UTC().Add(-time.Hour), UpdatedAt: service.now().UTC(),
	}
	if err := db.Create(&preparation).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(task).Updates(map[string]any{
		"stage": "forwarding_wait", "forward_preparation_id": preparation.ID, "attempts": 4, "max_attempts": 5,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	processOnboardingStageForTest(t, service, db, task)
	if task.Status != iCloudOnboardingFailed || task.Attempts != 5 || task.LastErrorCategory != "forwarding_preparation_expired" || task.ForwardPreparationID != nil {
		t.Fatalf("forward preparation retry budget = %+v", task)
	}
}

func TestICloudOnboardingInfrastructureRetriesTerminateAndRefreshImport(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Model(task).Updates(map[string]any{
		"attempts": 4, "max_attempts": 5, "dispatch_status": "running", "claim_token": "claim",
		"session_payload": []byte(`{"session":true}`), "manual_verification_code": "123456",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ReleaseICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: task.Generation}, "temporary failure"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != iCloudOnboardingFailed || task.DispatchStatus != "failed" || task.Attempts != 5 || task.FinishedAt == nil || len(task.SecretPayload) != 0 || len(task.SessionPayload) != 0 || task.ManualVerificationCode != "" {
		t.Fatalf("exhausted task = %+v", task)
	}
	batch, err := service.loadICloudOnboardingImport(context.Background(), *task.ImportID)
	if err != nil || batch.Status != iCloudOnboardingFailed || batch.FailedCount != 1 {
		t.Fatalf("import summary = %+v err=%v", batch, err)
	}
}

func TestReleaseICloudOnboardingTaskIgnoresAlreadyAdvancedDispatch(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "manage_profile", "dispatch_status": "pending", "attempts": 1,
		"session_payload": []byte(`{"session":true}`), "last_safe_error": "advanced",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	generation, attempts := task.Generation, task.Attempts
	if err := service.ReleaseICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: generation}, "stale worker failure"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Stage != "manage_profile" || task.DispatchStatus != "pending" || task.Generation != generation || task.Attempts != attempts || task.LastSafeError != "advanced" {
		t.Fatalf("advanced task was released by stale payload: %+v", task)
	}
}

func TestGetICloudOnboardingImportRepairsStaleSummary(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	if err := db.Model(task).Updates(map[string]any{
		"onboarding_status": iCloudOnboardingCompleted, "stage": "completed", "dispatch_status": "succeeded",
	}).Error; err != nil {
		t.Fatal(err)
	}

	view, err := service.GetAdminICloudOnboardingImport(context.Background(), *task.ImportID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != iCloudOnboardingCompleted || view.Completed != 1 || view.Failed != 0 || view.Waiting != 0 {
		t.Fatalf("reconciled import = %+v", view)
	}
}

func TestRecoverStaleICloudOnboardingTasksConsumesRetryBudget(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	now := service.now().UTC()
	queuedAt := now.Add(-3 * time.Minute)
	if err := db.Model(task).Updates(map[string]any{
		"attempts": 0, "max_attempts": 2, "dispatch_status": "queued", "next_attempt_at": queuedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.recoverStaleICloudOnboardingTasks(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Attempts != 1 || task.Generation != 2 || task.DispatchStatus != "pending" {
		t.Fatalf("queued recovery = %+v", task)
	}
	started := now.Add(-10 * time.Minute)
	if err := db.Model(task).Updates(map[string]any{
		"dispatch_status": "running", "started_at": started, "updated_at": now.Add(-6 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.recoverStaleICloudOnboardingTasks(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Attempts != 2 || task.Status != iCloudOnboardingFailed || task.DispatchStatus != "failed" || task.FinishedAt == nil {
		t.Fatalf("running recovery = %+v", task)
	}
}

func TestClaimedAppleSMSCodeDoesNotReparseProviderTime(t *testing.T) {
	message := kitesim.MessageItem{Content: "Apple code 222222", Time: "provider-specific-time"}
	if code := claimedAppleSMSCode(message); code != "222222" {
		t.Fatalf("code = %q", code)
	}
}
