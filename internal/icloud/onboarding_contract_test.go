package icloud

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestICloudOnboardingTaskViewDoesNotExposeWriteOnlySecrets(t *testing.T) {
	task := iCloudOnboardingTaskModel{
		ID: 1, TaskKind: "onboarding", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		Region: "美国区", SecretPayload: []byte(`{"password":"secret-password","securityAnswers":["secret-answer"]}`),
		SessionPayload: []byte(`{"cookie":"secret-cookie"}`), ManualVerificationCode: "123456",
	}
	payload, err := json.Marshal(iCloudOnboardingTaskView(task))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("secret-password"), []byte("secret-answer"), []byte("secret-cookie"), []byte("123456"),
		[]byte(`"secretPayload"`), []byte(`"sessionPayload"`), []byte(`"manualVerificationCode"`),
	} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("onboarding read response exposed %q: %s", forbidden, payload)
		}
	}
}

func TestICloudCookieRecoveryTaskUsesRefreshViewContract(t *testing.T) {
	view := iCloudOnboardingTaskView(iCloudOnboardingTaskModel{TaskKind: iCloudCookieRecoveryTaskKind})
	if view.TaskKind != "refresh" {
		t.Fatalf("recovery task kind = %q, want refresh", view.TaskKind)
	}
}

func TestICloudOnboardingStatusOnlyAllowsKnownWaitingStates(t *testing.T) {
	for _, tc := range []struct {
		status, dispatch, stage, device, purpose, category, want string
	}{
		{"unknown", "pending", "accepted", "", "", "", iCloudOnboardingFailed},
		{"processing", "unknown", "accepted", "", "", "", iCloudOnboardingFailed},
		{"waiting", "waiting", "unknown", "", "", "", iCloudOnboardingFailed},
		{"waiting", "waiting", "manage_prepare", "failed", "", "", iCloudOnboardingFailed},
		{"waiting", "waiting", "sms_wait", "", "manage_login", "sms_verification_exhausted", iCloudOnboardingFailed},
		{"waiting", "waiting", "family_prepare", "binding", "", "", iCloudOnboardingWaiting},
		{"waiting", "waiting", "waiting_family_sharing", "", "", "", iCloudOnboardingWaiting},
		{"waiting", "waiting", "waiting_icloud_activation", "", "", "", iCloudOnboardingWaiting},
		{"waiting", "waiting", "sms_wait", "", "manage_login", "", iCloudOnboardingWaiting},
		{"waiting", "pending", "sms_wait", "", "manage_login", "", iCloudOnboardingWaiting},
	} {
		t.Run(tc.status+"/"+tc.dispatch+"/"+tc.stage+"/"+tc.device+"/"+tc.category, func(t *testing.T) {
			view := iCloudOnboardingTaskView(iCloudOnboardingTaskModel{
				Status: tc.status, DispatchStatus: tc.dispatch, Stage: tc.stage, DeviceBindStatus: tc.device,
				PendingSMSPurpose: tc.purpose, LastErrorCategory: tc.category,
			})
			if view.Status != tc.want {
				t.Fatalf("status = %q, want %q", view.Status, tc.want)
			}
			if tc.want == iCloudOnboardingFailed && (view.NeedsManualCode || view.NeedsFamilyReset || view.NeedsICloudActivation) {
				t.Fatalf("failed task still requests manual confirmation: %+v", view)
			}
		})
	}
}
