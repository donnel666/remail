package api

import (
	"encoding/json"
	"testing"
)

// TestUpdateCardRequestExpireAtPresence pins the absent/null/value distinction
// that lets a card status toggle leave the expiry untouched while the edit modal
// can still clear it with an explicit null.
func TestUpdateCardRequestExpireAtPresence(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSet bool
		wantNil bool // meaningful only when wantSet: Value nil == clear
	}{
		{"absent leaves unchanged", `{"status":"enabled"}`, false, false},
		{"explicit null clears", `{"expireAt":null}`, true, true},
		{"value sets", `{"expireAt":"2026-01-02T03:04:05Z"}`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req UpdateCardRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if req.ExpireAt.Set != tc.wantSet {
				t.Fatalf("Set = %v, want %v", req.ExpireAt.Set, tc.wantSet)
			}
			if tc.wantSet && (req.ExpireAt.Value == nil) != tc.wantNil {
				t.Fatalf("Value==nil = %v, want %v", req.ExpireAt.Value == nil, tc.wantNil)
			}
		})
	}
}
