package infra

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftOAuthAcquireTokenRejectsMissingCredentialsLocally(t *testing.T) {
	client := NewMicrosoftOAuthClient()

	result, err := client.AcquireToken(context.Background(), MicrosoftOAuthRequest{
		EmailAddress: "user@example.com",
		Password:     "",
		ProxyURL:     "http://127.0.0.1:8080",
	})

	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, "password", result.Category)
	assert.Equal(t, "Microsoft account or password is incorrect.", result.SafeMessage)
	assert.False(t, result.ProxyFailure)
}
