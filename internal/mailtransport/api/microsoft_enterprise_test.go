package api

import (
	"context"
	"errors"
	"testing"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftEnterpriseValidationOnlyRefreshesAndReads(t *testing.T) {
	for _, mailAvailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "receive succeeds", false: "receive fails after rotation"}[mailAvailable], func(t *testing.T) {
			oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{Valid: true, ClientID: "client", AccessToken: "access", RefreshToken: "rotated"}}
			fetcher := &microsoftMailFetcherStub{result: mailinfra.MicrosoftMailFetchResult{Valid: mailAvailable, Protocol: "graph", RefreshToken: "latest"}}
			if !mailAvailable {
				fetcher.result.Category = "request"
				fetcher.result.SafeMessage = "temporarily unavailable"
			}
			adapter := &ResourceValidationAdapter{
				microsoft: oauth, fetcher: fetcher,
				bindings: &microsoftValidationBindingStoreStub{findErr: errors.New("binding must not be queried")},
				hardReauthorize: func(context.Context, string, string, string, string, []string) (msacl.ReauthorizeResult, error) {
					t.Fatal("enterprise must not use password reauthorization")
					return msacl.ReauthorizeResult{}, nil
				},
			}
			result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{EmailAddress: "user@tenant.onmicrosoft.com", ClientID: "client", RefreshToken: "original", Password: "unused"})
			require.NoError(t, err)
			require.Equal(t, mailAvailable, result.Valid)
			require.True(t, result.CredentialsAuthoritative)
			require.Equal(t, "latest", result.RefreshToken)
			require.Equal(t, "access", fetcher.request.AccessToken)
			require.Equal(t, "rotated", fetcher.request.RefreshToken)
			require.Zero(t, oauth.acquireCalls)
			require.Nil(t, result.BindingObservation)
			require.Empty(t, result.ConfirmedAliases)
		})
	}
}

func TestMicrosoftEnterpriseInvalidTokenDoesNotTryPassword(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{Category: "oauth_invalid_grant"}}
	fetcher := &microsoftMailFetcherStub{}
	adapter := &ResourceValidationAdapter{microsoft: oauth, fetcher: fetcher}
	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{EmailAddress: "user@tenant.onmicrosoft.com", ClientID: "client", RefreshToken: "expired", Password: "unused"})
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.False(t, result.CredentialsAuthoritative)
	require.Zero(t, oauth.acquireCalls)
	require.Zero(t, fetcher.calls)
}
