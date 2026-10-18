package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// MessageFactFingerprint excludes transport IDs and clocks, which differ for
// the same original message fetched through IMAP, POP3 or Graph.
func MessageFactFingerprint(message Message) string {
	recipients := make([]string, 0, len(message.Recipients)+1)
	values := message.Recipients
	if len(values) == 0 {
		values = []string{message.Recipient}
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			recipients = append(recipients, value)
			seen[value] = true
		}
	}
	sort.Strings(recipients)
	content, _ := json.Marshal([]any{message.ResourceType, recipients, strings.ToLower(strings.TrimSpace(message.Sender)), strings.TrimSpace(message.Subject), strings.ReplaceAll(message.RawBody, "\r\n", "\n")})
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func MessageCollisionKey(message Message) string {
	digest := sha256.Sum256([]byte(message.DedupeKey + ":" + MessageFactFingerprint(message)))
	return hex.EncodeToString(digest[:])
}
