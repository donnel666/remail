package app

import (
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestInboundParserUsesRuntimeLimits(t *testing.T) {
	runtimeconfig.Set("max_inbound_header_runes", "4")
	runtimeconfig.Set("max_inbound_preview_runes", "5")
	t.Cleanup(func() {
		runtimeconfig.Delete("max_inbound_header_runes")
		runtimeconfig.Delete("max_inbound_preview_runes")
	})

	parsed := parseInboundMessage([]byte("Subject: longer\r\n\r\nlonger body"), time.Now())
	require.Equal(t, "long", parsed.Summary.Subject)
	require.Equal(t, "longe", parsed.Summary.BodyPreview)
	require.True(t, strings.HasPrefix(parsed.Body, "longer"))
}
