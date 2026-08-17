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
