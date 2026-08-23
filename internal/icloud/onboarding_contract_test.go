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
