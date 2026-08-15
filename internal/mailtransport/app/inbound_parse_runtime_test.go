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

func TestInboundParserUsesConfiguredPatternForAppleVerificationMail(t *testing.T) {
	runtimeconfig.Set("verification_code_pattern", `["(?:^|[^\\d])(\\d{6,8})(?:[^\\d]|$)"]`)
	t.Cleanup(func() { runtimeconfig.Delete("verification_code_pattern") })
	raw := []byte("From: Apple <noreply@apple.com>\r\n" +
		"To: icloud_test@relay.example\r\n" +
		"Subject: 验证你的 Apple 账户电子邮件地址\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"你最近已添加 icloud_test@relay.example 作为你 Apple 账户的额外电子邮件地址。" +
		"为验证此电子邮件地址属于你，请在你的电子邮件验证页面输入下方验证码：\r\n\r\n" +
		"088556\r\n\r\nCopyright (c) 2026 Apple Inc.")

	summary := parseInboundMessage(raw, time.Now()).Summary
	require.Equal(t, "noreply@apple.com", summary.HeaderFrom)
	require.Equal(t, "088556", summary.VerificationCode)
}
