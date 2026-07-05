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
	assert.Equal(t, "Microsoft account or password is missing.", result.SafeMessage)
	assert.False(t, result.ProxyFailure)
}

func TestClassifyMicrosoftTokenFailureKeepsActionableCategories(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        map[string]any
		category    string
		safeMessage string
		proxy       bool
	}{
		{
			name:        "invalid grant",
			statusCode:  400,
			body:        map[string]any{"error": "invalid_grant"},
			category:    "oauth_invalid_grant",
			safeMessage: "Microsoft refresh token is invalid or expired.",
		},
		{
			name:        "mfa",
			statusCode:  400,
			body:        map[string]any{"error_description": "AADSTS50076: multi-factor authentication required"},
			category:    "mfa",
			safeMessage: "Microsoft account requires authenticator verification.",
		},
		{
			name:        "phone",
			statusCode:  400,
			body:        map[string]any{"error_description": "phone verification required"},
			category:    "phone",
			safeMessage: "Microsoft account requires phone verification.",
		},
		{
			name:        "invalid client",
			statusCode:  400,
			body:        map[string]any{"error": "invalid_client"},
			category:    "oauth_client",
			safeMessage: "Microsoft OAuth client is invalid or not allowed.",
		},
		{
			name:        "permission",
			statusCode:  400,
			body:        map[string]any{"error_description": "permission missing"},
			category:    "oauth_permission",
			safeMessage: "Microsoft OAuth permission is not available.",
		},
		{
			name:        "rate limited",
			statusCode:  429,
			body:        map[string]any{},
			category:    "request",
			safeMessage: "Microsoft mail service is temporarily unavailable.",
			proxy:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category, message, proxy := classifyMicrosoftTokenFailure(tt.statusCode, tt.body)

			assert.Equal(t, tt.category, category)
			assert.Equal(t, tt.safeMessage, message)
			assert.Equal(t, tt.proxy, proxy)
		})
	}
}
