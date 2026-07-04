package msacl

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeLogMessageRedactsSensitiveValues(t *testing.T) {
	message := sanitizeLogMessage(`account=user@example.com access_token=abc refresh_token:"def" PPFT=secret canary=hidden iProofOptions=OTT||code@example.com||Email||0||c url=https://login.live.com/path?uaid=raw&route=secret code=123456`)

	for _, leaked := range []string{
		"user@example.com",
		"access_token=abc",
		`refresh_token:"def"`,
		"PPFT=secret",
		"canary=hidden",
		"code@example.com",
		"uaid=raw",
		"route=secret",
		"123456",
	} {
		require.NotContains(t, message, leaked)
	}
	require.Contains(t, message, "u***@example.com")
	require.GreaterOrEqual(t, strings.Count(message, "[redacted]"), 5)
	require.Contains(t, message, "[code]")
}
