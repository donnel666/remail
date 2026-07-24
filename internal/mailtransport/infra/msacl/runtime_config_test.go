package msacl

import (
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestTokenPollIntervalUsesRuntimeSettingAndUpstreamMinimum(t *testing.T) {
	runtimeconfig.Set("msacl_token_poll_interval_seconds", "7")
	t.Cleanup(func() { runtimeconfig.Delete("msacl_token_poll_interval_seconds") })

	require.Equal(t, 7, sessionDCInterval(&Session{dcInterval: 5}))
	require.Equal(t, 9, sessionDCInterval(&Session{dcInterval: 9}))
}

func TestOAuthBrowserTimeoutUsesRuntimeSetting(t *testing.T) {
	runtimeconfig.Set("oauth_validation_timeout_seconds", "9")
	t.Cleanup(func() { runtimeconfig.Delete("oauth_validation_timeout_seconds") })

	require.Equal(t, 9, oauthValidationTimeoutSeconds())
}
