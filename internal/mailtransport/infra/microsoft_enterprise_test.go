package infra

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMicrosoftEnterpriseOAuthEndpoints(t *testing.T) {
	endpoint, scopes := microsoftOAuthEndpoint("user@tenant.onmicrosoft.com", false)
	require.Equal(t, "https://login.microsoftonline.com/organizations/oauth2/v2.0/token", endpoint)
	require.Empty(t, scopes)
	endpoint, scopes = microsoftOAuthEndpoint("user@tenant.onmicrosoft.com", true)
	require.Contains(t, endpoint, "/organizations/")
	require.Equal(t, "https://outlook.office.com/IMAP.AccessAsUser.All offline_access", scopes)
	endpoint, scopes = microsoftOAuthEndpoint("user@outlook.com", false)
	require.Equal(t, microsoftTokenURL, endpoint)
	require.Equal(t, defaultMicrosoftScopes, scopes)
	endpoint, scopes = microsoftOAuthEndpoint("user@outlook.com", true)
	require.Equal(t, microsoftIMAPTokenURL, endpoint)
	require.Empty(t, scopes)
}
