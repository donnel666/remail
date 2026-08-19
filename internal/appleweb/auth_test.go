package appleweb

import (
	"encoding/json"
	"strconv"
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

func TestAutomatedBrowserProfilesUseOnlyWindowsAndLinux(t *testing.T) {
	platforms := map[string]bool{}
	for index := range 256 {
		key := "account-" + strconv.Itoa(index) + "@example.com"
		profile := AutomatedBrowserProfile(key)
		if again := AutomatedBrowserProfile(key); again != profile {
			t.Fatalf("profile changed for %q", key)
		}
		if strings.Contains(profile.UserAgent, "Macintosh") || strings.Contains(profile.UserAgent, "iPhone") || strings.Contains(profile.UserAgent, "iPad") {
			t.Fatalf("automated profile uses an Apple platform: %+v", profile)
		}
		switch profile.SecCHPlatform {
		case `"Windows"`, `"Linux"`:
			platforms[profile.SecCHPlatform] = true
		default:
			t.Fatalf("unexpected automated platform %q", profile.SecCHPlatform)
		}
		if resolved, ok := BrowserProfileForUserAgent(profile.UserAgent); !ok || resolved != profile {
			t.Fatalf("profile could not be resolved from its user agent: %+v", profile)
		}
	}
	if len(platforms) != 2 {
		t.Fatalf("automated profiles did not cover Windows and Linux: %v", platforms)
	}

	profile := AutomatedBrowserProfile("fd@example.com")
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
