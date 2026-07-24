package infra

import (
	"net/url"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftMailFetchUsesRuntimeSettings(t *testing.T) {
	runtimeconfig.Set("graph_message_page_top", "37")
	runtimeconfig.Set("mail_fetch_client_timeout_seconds", "9")
	t.Cleanup(func() {
		runtimeconfig.Delete("graph_message_page_top")
		runtimeconfig.Delete("mail_fetch_client_timeout_seconds")
	})

	rawURL := graphFolderMessagesURL(MicrosoftMailFolder{ID: "inbox"}, MicrosoftMailFetchRequest{})
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	require.Equal(t, "37", parsed.Query().Get("$top"))
	require.Equal(t, 9*time.Second, NewMicrosoftMailFetchClient().requestTimeout())
}
