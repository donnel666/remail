package msacl

import (
	"encoding/json"
	"testing"

	http "github.com/bogdanfinn/fhttp"
	"github.com/stretchr/testify/require"
)

func TestCredentialDiscoveryAllowsExplicitPasswordAlternative(t *testing.T) {
	for _, tc := range []struct {
		name      string
		password  any
		fido, ngc int
		blocked   bool
	}{
		{"password_and_passkey", true, 1, 0, false},
		{"numeric_password_and_passkey", json.Number("1"), 1, 0, false},
		{"password_and_approval", true, 0, 1, false},
		{"passkey_only", false, 1, 0, true},
		{"numeric_passkey_only", json.Number("0"), 1, 0, true},
		{"approval_only", false, 0, 1, true},
		{"unknown_password_capability", nil, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials := map[string]any{"HasFido": tc.fido, "HasRemoteNGC": tc.ngc}
			if tc.password != nil {
				credentials["HasPassword"] = tc.password
			}
			response, err := json.Marshal(map[string]any{"IfExistsResult": 0, "Credentials": credentials})
			require.NoError(t, err)
			s, client := newScriptedSession(t, func(req *http.Request, _ bool) (*http.Response, error) {
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, "/GetCredentialType.srf", req.URL.Path)
				request := decodeJSONRequest(t, req)
				require.Equal(t, true, request["isFidoSupported"])
				require.Equal(t, false, request["forceotclogin"])
				return scriptedResponse(req, 200, req.URL.String(), string(response), nil), nil
			})
			_, err = getCredentialType(s, "owner@outlook.com", "flow-token", "request-id", "operation-id")
			if tc.blocked {
				var auth *AuthError
				require.ErrorAs(t, err, &auth)
				require.Equal(t, AuthStatusPasskeyRequired, auth.Status)
			} else {
				require.NoError(t, err)
			}
			client.requireDone()
		})
	}
}

func TestJSONNumericBooleans(t *testing.T) {
	for value, expected := range map[string]bool{"0": false, "0.0": false, "1": true, "1.0": true, "invalid": false} {
		require.Equal(t, expected, asBool(json.Number(value)), value)
	}
}
