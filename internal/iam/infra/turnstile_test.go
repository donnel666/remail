package infra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

func TestTurnstileVerifierValidatesTokenActionAndHostname(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "secret", r.Form.Get("secret"))
		require.Equal(t, "203.0.113.7", r.Form.Get("remoteip"))
		switch r.Form.Get("response") {
		case "valid":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":  true,
				"action":   "login",
				"hostname": turnstileExpectedHostname,
			})
		case "wrong-hostname":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":  true,
				"action":   "login",
				"hostname": "example.com",
			})
		case "testing-key":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":  true,
				"hostname": "example.com",
				"metadata": map[string]any{"result_with_testing_key": true},
			})
		case "provider-error":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error-codes": []string{"internal-error"}})
		case "bad-request":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error-codes": []string{"bad-request"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error-codes": []string{"timeout-or-duplicate"}})
		}
	}))
	t.Cleanup(server.Close)

	verifier := NewTurnstileVerifier("secret")
	verifier.verifyURL = server.URL

	require.NoError(t, verifier.Verify(context.Background(), "valid", "203.0.113.7", "login"))
	require.NoError(t, verifier.Verify(context.Background(), "testing-key", "203.0.113.7", "login"))
	require.ErrorIs(t, verifier.Verify(context.Background(), "valid", "203.0.113.7", "register_email_code"), domain.ErrTurnstileInvalid)
	require.ErrorIs(t, verifier.Verify(context.Background(), "wrong-hostname", "203.0.113.7", "login"), domain.ErrTurnstileInvalid)
	require.ErrorIs(t, verifier.Verify(context.Background(), "duplicate", "203.0.113.7", "login"), domain.ErrTurnstileInvalid)
	require.ErrorIs(t, verifier.Verify(context.Background(), "provider-error", "203.0.113.7", "login"), domain.ErrTurnstileUnavailable)
	require.ErrorIs(t, verifier.Verify(context.Background(), "bad-request", "203.0.113.7", "login"), domain.ErrTurnstileUnavailable)
}
