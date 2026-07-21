package infra

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

func TestSystemLogModelSanitizesSharedDiagnosticBoundary(t *testing.T) {
	model := systemLogModel(&domain.SystemLog{
		Message: "Token failure password=message-secret",
		Detail:  `password=hunter2 refresh_token:"rt-secret" client_id=client-secret Authorization: Bearer bearer-secret https://alice:proxy-secret@proxy.test/path?access_token=at-secret mailtransport/inbound/2026/07/21/message.eml ` + strings.Repeat("界", 1200),
	})

	for _, secret := range []string{"message-secret", "hunter2", "rt-secret", "client-secret", "bearer-secret", "proxy-secret", "at-secret", "message.eml"} {
		require.NotContains(t, model.Message+model.Detail, secret)
	}
	require.Contains(t, model.Message, "password=***")
	require.Contains(t, model.Detail, "refresh_token:***")
	require.Contains(t, model.Detail, "https://***@proxy.test")
	require.Contains(t, model.Detail, "mailtransport/inbound/***")
	require.LessOrEqual(t, utf8.RuneCountInString(model.Detail), 1000)
}
