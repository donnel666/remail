package icloud

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestICloudIMAPCursorAndRecipientBoundaries(t *testing.T) {
	if got := iCloudIMAPScanLimit(100, 30); got != 30 {
		t.Fatalf("purchase scan limit = %d, want 30", got)
	}
	if got := iCloudIMAPScanLimit(100, 0); got != 100 {
		t.Fatalf("default scan limit = %d, want 100", got)
	}

	uids := []imap.UID{11, 12, 13}
	latest := iCloudIMAPLatestUIDs(uids, 2)
	if len(latest) != 2 || latest[0] != 12 || latest[1] != 13 {
		t.Fatalf("latest UID window = %#v", latest)
	}
	read := map[imap.UID]struct{}{11: {}, 13: {}}
	if got := iCloudIMAPLastReadUID(10, uids, read); got != 11 {
		t.Fatalf("cursor crossed unread UID: got %d want 11", got)
	}

	raw := []byte("Delivered-To: Other@example.com\r\nTo: Alias One <ALIAS-1@icloud.com>, alias-2@icloud.com\r\n\r\nbody")
	if got := iCloudIMAPRecipient(raw, []string{"alias-1@icloud.com", "alias-2@icloud.com"}); got != "" {
		t.Fatalf("ambiguous recipients matched %q", got)
	}
	raw = []byte("Delivered-To: Alias One <ALIAS-1@icloud.com>\r\nTo: alias-2@icloud.com\r\n\r\nbody")
	if got := iCloudIMAPRecipient(raw, []string{"alias-1@icloud.com", "alias-2@icloud.com"}); got != "alias-1@icloud.com" {
		t.Fatalf("delivery recipient = %q", got)
	}
}
