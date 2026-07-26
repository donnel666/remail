package app

import (
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/require"
)

func TestSystemMessagesReuseVerificationFrameAndEscapeContent(t *testing.T) {
	messages := []domain.OutboundMessage{
		BalanceWarningMessage("user@example.com", "0.40", "0.50", 2),
		RechargeCreditedMessage("user@example.com", "RC1", "10.00", "10.40"),
		LoginNotificationMessage("user@example.com", "session-1", "203.0.113.8", "Browser", time.Unix(0, 0)),
		AnnouncementMessage("user@example.com", 7, "公告", "安全内容\n<script>alert(1)</script>"),
	}
	for _, message := range messages {
		require.Contains(t, message.HTMLBody, `class="remail-shine-bar"`)
		require.Contains(t, message.HTMLBody, "Remail，轻松收码")
		require.NotEmpty(t, message.TextBody)
		require.Len(t, message.IdempotencyKey, 64)
	}
	require.Equal(t, domain.PurposeSecurityNotice, messages[2].Purpose)
	require.NotContains(t, messages[3].HTMLBody, "<script>")
	require.Contains(t, messages[3].HTMLBody, "&lt;script&gt;")
	require.True(t, strings.HasPrefix(messages[3].Subject, "ReMail 系统公告："))
}
