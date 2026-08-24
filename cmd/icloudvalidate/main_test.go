package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/icloud"
	"github.com/donnel666/remail/internal/kitesim"
)

func TestParseLine(t *testing.T) {
	line := "美国区----否----example@example.com----test-password----你少年时代最好的朋友叫什么名字？(remail1)----你的理想工作是什么？(remail2)----你的父母是在哪里认识的？(remail3)----1981-10-06----+1 (548) 876-8536----https://setup.icloud.com/family/messages?inviteCode=EFI_test"
	input, err := parseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if input.Email != "example@example.com" || input.ICloudOpened || input.CountryCode != "US" || input.PhoneNumber != "15488768536" {
		t.Fatalf("unexpected parsed account: %+v", input)
	}
	if input.FamilyInviteURL == "" || input.Secret.SecurityAnswers[0].Answer != "remail1" {
		t.Fatalf("invite or answers were not parsed: %+v", input)
	}
}

func TestParseLineRejectsInviteWithoutToken(t *testing.T) {
	line := "美国区----否----example@example.com----password----q1(a1)----q2(a2)----q3(a3)----1981-10-06----https://setup.icloud.com/family/messages"
	if _, err := parseLine(line); err == nil {
		t.Fatal("expected invalid family invitation")
	}
}

func TestSpecifiedPhoneDoesNotSkipEnrollment(t *testing.T) {
	input, err := parseLine("美国区----否----example@example.com----password----q1(a1)----q2(a2)----q3(a3)----1981-10-06----4385548384")
	if err != nil {
		t.Fatal(err)
	}
	input.PhoneCountryCode = "CA"
	provider := &recordingAppleProvider{}
	d := &debugger{ctx: context.Background(), input: input, runtime: &runtime{apple: provider}}
	if _, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingPrepareICloud}); err != nil {
		t.Fatal(err)
	}
	if provider.request.SkipPhoneEnrollment {
		t.Fatal("a specified pool phone must go through Apple phone enrollment")
	}
	if provider.request.PhoneNumber != input.PhoneNumber {
		t.Fatalf("phone = %q, want %q", provider.request.PhoneNumber, input.PhoneNumber)
	}
	if provider.request.PhoneCountryCode != "CA" {
		t.Fatalf("phone country = %q, want CA", provider.request.PhoneCountryCode)
	}
}

func TestExecuteLogsFailureWithoutSecrets(t *testing.T) {
	var output strings.Builder
	provider := &recordingAppleProvider{err: &icloud.AppleOnboardingError{
		Category: "sms_send_rejected", SafeMessage: "Apple rejected the verification SMS request.",
		HTTPStatus: 429, ProviderMessage: "verification 654321 rejected for password-secret and answer-secret", SendRejected: true,
	}}
	d := &debugger{
		ctx: context.Background(), stdout: &output,
		input: accountInput{Email: "example@example.com", CountryCode: "US", Secret: icloud.AppleOnboardingSecret{
			Password: "password-secret", SecurityAnswers: [3]icloud.AppleSecurityAnswer{{Answer: "answer-secret"}},
		}},
		runtime: &runtime{apple: provider}, session: json.RawMessage(`{"cookie":"cookie-secret"}`),
	}
	_, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingSendSMS, SMSPurpose: icloud.AppleSMSPhoneEnrollment, Code: "654321"})
	if err == nil {
		t.Fatal("expected provider failure")
	}
	log := output.String()
	for _, expected := range []string{"apple_operation=start", "operation=send_sms", `category="sms_send_rejected"`, "http_status=429", "send_rejected=true"} {
		if !strings.Contains(log, expected) {
			t.Fatalf("debug log missing %q: %s", expected, log)
		}
	}
	for _, secret := range []string{"password-secret", "answer-secret", "654321", "cookie-secret"} {
		if strings.Contains(log, secret) {
			t.Fatalf("debug log leaked %q: %s", secret, log)
		}
	}
}

func TestCheckpointRoundTripIsPrivateAndFingerprintChangesWithInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	state := checkpointFile{Version: 1, Accounts: map[string]accountCheckpoint{
		"example@example.com": {Fingerprint: "abc", Stage: "cookies_ready", Session: json.RawMessage(`{"private":"session"}`)},
	}}
	if err := saveCheckpoint(path, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode = %o, want 600", info.Mode().Perm())
	}
	loaded, err := loadCheckpoint(path)
	if err != nil || loaded.Accounts["example@example.com"].Stage != "cookies_ready" {
		t.Fatalf("checkpoint round trip = %+v err=%v", loaded, err)
	}
	base := accountInput{Region: "美国区", CountryCode: "US", Email: "example@example.com", Secret: icloud.AppleOnboardingSecret{Password: "one"}}
	first, err := accountFingerprint(base, options{forwardTo: "relay@example.com", ownerUserID: 1, expireDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	base.Secret.Password = "two"
	second, err := accountFingerprint(base, options{forwardTo: "relay@example.com", ownerUserID: 1, expireDays: 30})
	if err != nil || first == second {
		t.Fatalf("fingerprint did not change: first=%q second=%q err=%v", first, second, err)
	}
}

func TestRestartAtClearsOnlyTheAffectedAppleSession(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	cp := accountCheckpoint{
		Stage: "sms_verify_recover", PendingSMSPurpose: icloud.AppleSMSManageLogin,
		Session: json.RawMessage(`{"session":true}`), ICloudReady: true, FamilyJoined: true,
		ManageAuthenticated: true, ManageReady: true, ManageSessionExpiresAt: now.Add(temporaryManageSessionTTL), ForwardAdded: true,
	}
	d := &debugger{checkpoint: &cp, session: append(json.RawMessage(nil), cp.Session...)}
	if err := d.restartAt("manage_prepare"); err != nil {
		t.Fatal(err)
	}
	if cp.Stage != "manage_prepare" || cp.PendingSMSPurpose != "" || len(cp.Session) != 0 || len(d.session) != 0 {
		t.Fatalf("session state was not cleared: %+v", cp)
	}
	if cp.ManageAuthenticated || cp.ManageReady || !cp.ManageSessionExpiresAt.IsZero() || !cp.ICloudReady || !cp.FamilyJoined || !cp.ForwardAdded {
		t.Fatalf("restart cleared unrelated completed stages: %+v", cp)
	}
	if err := d.restartAt("unknown"); err == nil || cp.Stage != "manage_prepare" {
		t.Fatalf("unknown restart stage changed checkpoint: stage=%q err=%v", cp.Stage, err)
	}
}

func TestCompletedPhasesResumeAtManage(t *testing.T) {
	stop := errors.New("stop after observing the first operation")
	provider := &recordingAppleProvider{err: stop}
	cp := accountCheckpoint{
		ICloudReady: true, FamilyJoined: true,
		Session: json.RawMessage(`{"mode":"family"}`),
	}
	d := &debugger{
		ctx: context.Background(), input: accountInput{
			Email: "example@example.com", CountryCode: "US",
			FamilyInviteURL: "https://setup.icloud.com/family/messages?inviteCode=test",
		},
		runtime: &runtime{apple: provider}, checkpoint: &cp,
		session: append(json.RawMessage(nil), cp.Session...),
	}
	if err := d.run(nil, options{forwardTo: "relay@example.com"}); !errors.Is(err, stop) {
		t.Fatalf("run error = %v, want stop", err)
	}
	if provider.request.Operation != icloud.AppleOnboardingPrepareManage {
		t.Fatalf("first resumed operation = %q, want %q", provider.request.Operation, icloud.AppleOnboardingPrepareManage)
	}
}

func TestEachPhaseChecksPhoneBeforeAnyAppleOperation(t *testing.T) {
	for _, test := range []struct {
		name       string
		phase      string
		input      accountInput
		checkpoint accountCheckpoint
	}{
		{name: "icloud", phase: "icloud", input: accountInput{Email: "icloud@example.com", CountryCode: "US"}},
		{
			name: "family", phase: "family",
			input:      accountInput{Email: "family@example.com", CountryCode: "US", FamilyInviteURL: "https://setup.icloud.com/family/messages?inviteCode=test"},
			checkpoint: accountCheckpoint{ICloudReady: true},
		},
		{name: "manage", phase: "manage", input: accountInput{Email: "manage@example.com", CountryCode: "US"}, checkpoint: accountCheckpoint{ICloudReady: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &recordingAppleProvider{}
			checkpoint := test.checkpoint
			d := &debugger{
				ctx: context.Background(), input: test.input, checkpoint: &checkpoint,
				runtime: &runtime{apple: provider, sms: &kitesim.Service{}},
			}
			err := d.run(&kitesim.SMSPhoneBinding{PhoneID: 7}, options{forwardTo: "relay@example.com"})
			if err == nil || !strings.Contains(err.Error(), "check SMS phone before "+test.phase+" phase") {
				t.Fatalf("phase preflight error = %v", err)
			}
			if provider.calls != 0 {
				t.Fatalf("cooling phase reached Apple provider %d times", provider.calls)
			}
		})
	}
}

func TestManageSessionResumesForwardingBeforeFormalCookieExport(t *testing.T) {
	now := time.Now().UTC()
	cp := accountCheckpoint{
		ICloudReady: true, FamilyJoined: true, ManageAuthenticated: true, ManageReady: true,
		ManageSessionExpiresAt: now.Add(temporaryManageSessionTTL),
		Session:                json.RawMessage(`{"mode":"manage","temporary":true}`),
	}
	failed := errors.New("forwarding temporarily failed")
	first := &scriptedAppleProvider{steps: []appleProviderStep{{err: failed}}}
	binding := &kitesim.SMSPhoneBinding{PhoneID: 7}
	d := &debugger{
		ctx: context.Background(), input: accountInput{Email: "example@example.com", CountryCode: "US"},
		runtime: &runtime{apple: first, sms: &kitesim.Service{}}, checkpoint: &cp,
		session: append(json.RawMessage(nil), cp.Session...),
	}
	config := options{forwardTo: "relay@example.com", forwardCode: "123456"}
	if err := d.run(binding, config); !errors.Is(err, failed) {
		t.Fatalf("first run error = %v, want forwarding failure", err)
	}
	if len(first.requests) != 1 || first.requests[0].Operation != icloud.AppleOnboardingAddForward || len(cp.Session) == 0 || cp.CookiesReady || cp.NewChannel != nil {
		t.Fatalf("temporary forwarding checkpoint = %+v requests=%+v", cp, first.requests)
	}

	second := &scriptedAppleProvider{steps: []appleProviderStep{
		{response: icloud.AppleOnboardingResponse{Next: "pending", Session: json.RawMessage(`{"mode":"manage","step":"added"}`)}},
		{response: icloud.AppleOnboardingResponse{Next: "ready", Session: json.RawMessage(`{"mode":"manage","step":"verified"}`)}},
		{response: icloud.AppleOnboardingResponse{Next: "ready", Session: json.RawMessage(`{"mode":"manage","step":"exported"}`), NewChannel: &icloud.AppleOnboardingChannel{Cookie: "formal-cookie"}}},
	}}
	d.runtime.apple = second
	d.session = append(json.RawMessage(nil), cp.Session...)
	if err := d.run(binding, config); err == nil || !strings.Contains(err.Error(), "resource service is unavailable") {
		t.Fatalf("second run error = %v, want local commit dependency error", err)
	}
	want := []string{icloud.AppleOnboardingAddForward, icloud.AppleOnboardingVerifyForward, icloud.AppleOnboardingExport}
	if len(second.requests) != len(want) {
		t.Fatalf("resumed operations = %+v, want %v", second.requests, want)
	}
	for index := range want {
		if second.requests[index].Operation != want[index] {
			t.Fatalf("resumed operation %d = %q, want %q", index, second.requests[index].Operation, want[index])
		}
	}
	if !cp.CookiesReady || cp.NewChannel == nil || cp.NewChannel.Cookie != "formal-cookie" {
		t.Fatalf("formal Cookie was not checkpointed after forwarding: %+v", cp)
	}
}

func TestTemporaryManageSessionExpiresAfterTenMinutes(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	cp := &accountCheckpoint{Session: json.RawMessage(`{"mode":"manage"}`), ManageSessionExpiresAt: now.Add(temporaryManageSessionTTL)}
	if !temporaryManageSessionReady(cp, now.Add(temporaryManageSessionTTL-time.Nanosecond)) {
		t.Fatal("temporary management session expired too early")
	}
	if temporaryManageSessionReady(cp, now.Add(temporaryManageSessionTTL)) {
		t.Fatal("temporary management session remained reusable at its deadline")
	}
	cp.ManageSessionExpiresAt = time.Time{}
	if temporaryManageSessionReady(cp, now) {
		t.Fatal("temporary management session without an explicit deadline was reused")
	}
}

func TestReusableSMSCheckpointRequiresTheSameActiveChallenge(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cp := &accountCheckpoint{
		PendingSMSPurpose: icloud.AppleSMSPhoneEnrollment,
		Session:           json.RawMessage(`{"transaction":true}`),
		Binding:           &savedPhoneBinding{PhoneID: 7},
	}
	challenge := kitesim.SMSChallenge{
		PhoneID: 7, Purpose: icloud.AppleSMSPhoneEnrollment,
		Status: kitesim.SMSChallengeSent, ExpiresAt: now.Add(time.Minute),
	}
	if !reusableSMSCheckpoint(cp, challenge, now) {
		t.Fatal("matching active challenge was not resumable")
	}
	reserved := challenge
	reserved.Status = kitesim.SMSChallengeReserved
	if !reusableSMSCheckpoint(cp, reserved, now) {
		t.Fatal("matching reserved challenge was not resumable")
	}
	for name, mutate := range map[string]func(*kitesim.SMSChallenge){
		"expired":     func(value *kitesim.SMSChallenge) { value.ExpiresAt = now },
		"completed":   func(value *kitesim.SMSChallenge) { value.Status = kitesim.SMSChallengeCompleted },
		"wrong phone": func(value *kitesim.SMSChallenge) { value.PhoneID = 8 },
		"wrong purpose": func(value *kitesim.SMSChallenge) {
			value.Purpose = icloud.AppleSMSManageLogin
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := challenge
			mutate(&candidate)
			if reusableSMSCheckpoint(cp, candidate, now) {
				t.Fatalf("%s challenge was reused", name)
			}
		})
	}
}

func TestLegacyCheckpointSessionIsNeverReused(t *testing.T) {
	var checkpoint accountCheckpoint
	if err := json.Unmarshal([]byte(`{"session":{"stale":true},"stage":"sms_wait","pendingSmsPurpose":"phone_enrollment"}`), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Session) != 0 {
		t.Fatal("legacy unqualified Apple session was loaded as a reusable transaction")
	}
}

func TestICloudCheckpointReadyRequiresOldCookieForOpenedAccounts(t *testing.T) {
	closed := &accountCheckpoint{ICloudReady: true}
	if !iCloudCheckpointReady(closed, "child") {
		t.Fatal("closed iCloud checkpoint required an old Cookie")
	}
	checkpoint := &accountCheckpoint{ICloudReady: true, ICloudOpened: true}
	if iCloudCheckpointReady(checkpoint, "primary") {
		t.Fatal("primary checkpoint without an old Cookie was treated as complete")
	}
	if iCloudCheckpointReady(checkpoint, "child") {
		t.Fatal("child checkpoint without an old Cookie was treated as complete")
	}
	checkpoint.OldChannel = &icloud.AppleOnboardingChannel{Cookie: "old-cookie"}
	for _, role := range []string{"primary", "child"} {
		if !iCloudCheckpointReady(checkpoint, role) {
			t.Fatalf("%s checkpoint with an old Cookie was not reusable", role)
		}
	}
}

func TestAppleRestartStageCoversEverySMSPurpose(t *testing.T) {
	for purpose, want := range map[string]string{
		icloud.AppleSMSICloudLogin:          "icloud_prepare",
		icloud.AppleSMSICloudCookieLogin:    "icloud_cookie_prepare",
		icloud.AppleSMSPhoneEnrollment:      "icloud_prepare",
		icloud.AppleSMSFamilyLogin:          "family_prepare",
		icloud.AppleSMSFamilyReconcileLogin: "family_prepare",
		icloud.AppleSMSManageLogin:          "manage_prepare",
	} {
		if got := appleRestartStage(purpose); got != want {
			t.Fatalf("appleRestartStage(%q) = %q, want %q", purpose, got, want)
		}
	}
}

type recordingAppleProvider struct {
	request  icloud.AppleOnboardingRequest
	response icloud.AppleOnboardingResponse
	err      error
	calls    int
}

func (p *recordingAppleProvider) Execute(_ context.Context, request icloud.AppleOnboardingRequest) (icloud.AppleOnboardingResponse, error) {
	p.calls++
	p.request = request
	return p.response, p.err
}

type appleProviderStep struct {
	response icloud.AppleOnboardingResponse
	err      error
}

type scriptedAppleProvider struct {
	steps    []appleProviderStep
	requests []icloud.AppleOnboardingRequest
}

func (p *scriptedAppleProvider) Execute(_ context.Context, request icloud.AppleOnboardingRequest) (icloud.AppleOnboardingResponse, error) {
	p.requests = append(p.requests, request)
	if len(p.steps) == 0 {
		return icloud.AppleOnboardingResponse{}, errors.New("unexpected Apple operation")
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	return step.response, step.err
}
