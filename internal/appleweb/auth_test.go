package appleweb

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFDClientInfoMatchesFixedMacOSFingerprint(t *testing.T) {
	if !strings.Contains(UserAgent, "Macintosh") || !strings.Contains(UserAgent, "Chrome/133.") {
		t.Fatalf("unexpected Apple user agent %q", UserAgent)
	}
	value, err := FDClientInfo(time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FDClientInfo: %v", err)
	}
	var payload struct {
		UserAgent string `json:"U"`
		Language  string `json:"L"`
		Zone      string `json:"Z"`
	}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		t.Fatalf("decode FD client info: %v", err)
	}
	if payload.UserAgent != UserAgent || payload.Language != Language || payload.Zone != TimeZoneOffset {
		t.Fatalf("FD client info does not match fixed fingerprint: %+v", payload)
	}
	if SecCHPlatform != `"macOS"` {
		t.Fatalf("unexpected client-hint platform %q", SecCHPlatform)
	}
}

func TestAutomatedBrowserProfileUsesWindows(t *testing.T) {
	profile := AutomatedBrowserProfile("account@example.com")
	if again := AutomatedBrowserProfile("different@example.com"); again != profile {
		t.Fatalf("automated profile changed between accounts: first=%+v second=%+v", profile, again)
	}
	if profile.SecCHPlatform != `"Windows"` || !strings.Contains(profile.UserAgent, "Windows NT") {
		t.Fatalf("automated profile is not Windows: %+v", profile)
	}
	if profile.UserAgent != AutomatedUserAgent || !strings.Contains(profile.UserAgent, "Chrome/151.") {
		t.Fatalf("automated profile is not aligned with the Apple onboarding script: %+v", profile)
	}
	if resolved, ok := BrowserProfileForUserAgent(profile.UserAgent); !ok || resolved != profile {
		t.Fatalf("profile could not be resolved from its user agent: %+v", profile)
	}
	legacyLinux := `Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36`
	if resolved, ok := BrowserProfileForUserAgent(legacyLinux); !ok || resolved.SecCHPlatform != `"Linux"` {
		t.Fatalf("legacy Linux profile could not be resolved: %+v", resolved)
	}

	profile = AutomatedBrowserProfile("fd@example.com")
	value, err := FDClientInfoFor(profile.UserAgent, time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FDClientInfoFor: %v", err)
	}
	var payload struct {
		UserAgent string `json:"U"`
	}
	if err := json.Unmarshal([]byte(value), &payload); err != nil || payload.UserAgent != profile.UserAgent {
		t.Fatalf("FD client info does not match automated profile: payload=%+v err=%v", payload, err)
	}
}
