package msacl

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/stretchr/testify/require"
)

const passwordRecoveryInitialFixture = `<!doctype html><script>
var ServerData={
  "sResetPwdAction":"SignInName",
  "sSigninName":"",
  "sCanary":"initial-form-canary",
  "apiCanary":"initial-api-canary",
  "sUnauthSessionID":"uaid-fixture",
  "urlRU":"https:\u002f\u002flogin.live.com\u002flogout.srf?fixture=1",
  "urlPost":"https://account.live.com/password/reset?uaid=uaid-fixture",
  "sPageId":"Account_ResetPwdSignInNamesPage",
  "oCaptchaInfo":{"nested":{"text":"a }; brace inside a string"}}
};window.$Do={};
</script>`

const passwordRecoveryPickerFixture = `<!doctype html><script>
var ServerData={
  "sPageId":"Account_ResetPwdPage_ProofPicker",
  "sSigninName":"owner@example.test",
  "sCanary":"picker-form-canary",
  "apiCanary":"picker-api-canary",
  "sRecoveryToken":"recovery-token-secret",
  "sUnauthSessionID":"uaid-fixture",
  "iScenarioId":100103,
  "iUiFlavor":1001,
  "hpgid":200284,
  "urlPost":"https://account.live.com/password/reset?uaid=uaid-fixture",
  "oProofList":[
    {"name":"qa*****@recovery.test","type":"Email","channel":"Email","used":0,"epid":"epid-secret","requiresReentry":1},
    {"name":"+86 ******1234","type":"Sms","channel":"SMS","used":0,"epid":"phone-epid-secret","requiresReentry":1}
  ]
};window.$Do={};
</script>`

func TestExtractPasswordRecoveryServerDataBalancesNestedObjectsAndStrings(t *testing.T) {
	data, err := extractPasswordRecoveryServerData(passwordRecoveryInitialFixture)
	require.NoError(t, err)
	require.Equal(t, "SignInName", asString(data["sResetPwdAction"]))
	require.Equal(t, "initial-form-canary", asString(data["sCanary"]))
	require.Equal(t, "https://login.live.com/logout.srf?fixture=1", asString(data["urlRU"]))
}

func TestProbePasswordRecoveryUsesInitialGETPOSTAndReturnsNoSecrets(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, passwordRecoveryEntryURL)
			require.True(t, follow)
			return scriptedResponse(req, 200, passwordRecoveryEntryURL, passwordRecoveryInitialFixture, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/password/reset?uaid=uaid-fixture")
			require.True(t, follow)
			require.Equal(t, "https://account.live.com", req.Header.Get("Origin"))
			require.Equal(t, passwordRecoveryEntryURL, req.Header.Get("Referer"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "SignInName", fields.Get("iAction"))
			require.Equal(t, "https://login.live.com/logout.srf?fixture=1", fields.Get("iRU"))
			require.Equal(t, "false", fields.Get("isSigninNamePhone"))
			require.Equal(t, "initial-form-canary", fields.Get("canary"))
			require.Equal(t, "owner@example.test", fields.Get("iSigninName"))
			return scriptedResponse(req, 200, req.URL.String(), passwordRecoveryPickerFixture, nil), nil
		},
	)

	flow, result, err := probePasswordRecoveryWithSession(
		session,
		"owner@example.test",
		"",
		"qalpha01@recovery.test",
	)

	require.NoError(t, err)
	require.NotNil(t, flow)
	require.Len(t, result.Proofs, 2)
	require.Equal(t, "qa*****@recovery.test", result.MaskedBindingAddress)
	require.Equal(t, "qalpha01@recovery.test", result.BindingAddress)
	require.True(t, result.BindingResolved)
	require.False(t, result.BindingAmbiguous)
	serialized, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "epid-secret")
	require.NotContains(t, string(serialized), "recovery-token-secret")
	require.NotContains(t, string(serialized), "picker-api-canary")
	client.requireDone()
}

func TestPasswordRecoveryMultipleEmailProofsStayAmbiguous(t *testing.T) {
	proofs := []passwordRecoveryProof{
		{Name: "qa*****@recovery.test", Type: "Email", Channel: "Email", EPID: "first"},
		{Name: "qa*****@recovery.test", Type: "Email", Channel: "Email", EPID: "second"},
	}

	result := buildPasswordRecoveryProbeResult(
		context.Background(),
		proofs,
		"owner@example.test",
		"",
		"qalpha01@recovery.test",
	)

	require.False(t, result.BindingResolved)
	require.True(t, result.BindingAmbiguous)
	require.Empty(t, result.BindingAddress)
	require.Empty(t, result.MaskedBindingAddress)
}

func TestSendPasswordRecoveryOTTRequestBoundaryAndCanaryRefresh(t *testing.T) {
	flow := testPasswordRecoveryFlow()
	proof := testPasswordRecoveryProof()
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, passwordRecoverySendOTTURL)
			requireRecoveryAPIHeaders(t, req, "picker-api-canary")
			payload := decodeJSONRequest(t, req)
			require.Equal(t, "Proof", payload["associationType"])
			require.Equal(t, "qalpha01@recovery.test", payload["confirmProof"])
			require.Equal(t, "epid-secret", payload["epid"])
			require.Equal(t, json.Number("1"), payload["proofRequiredReentry"])
			require.Equal(t, "RecoverUser", payload["purpose"])
			require.Equal(t, "recovery-token-secret", payload["token"])
			require.NotContains(t, payload, "password")
			return scriptedResponse(req, 200, req.URL.String(), `{"apiCanary":"refreshed-api-canary"}`, nil), nil
		},
	)

	err := sendPasswordRecoveryOTT(session, flow, proof, "qalpha01@recovery.test")

	require.NoError(t, err)
	require.Equal(t, "refreshed-api-canary", flow.apiCanary)
	client.requireDone()
}

func TestVerifyPasswordRecoveryCodeRequestBoundary(t *testing.T) {
	flow := testPasswordRecoveryFlow()
	proof := testPasswordRecoveryProof()
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, passwordRecoveryVerifyCodeURL)
			requireRecoveryAPIHeaders(t, req, "picker-api-canary")
			payload := decodeJSONRequest(t, req)
			require.Equal(t, "OTC", payload["action"])
			require.Equal(t, "654321", payload["code"])
			require.Equal(t, "qalpha01@recovery.test", payload["confirmProof"])
			require.Equal(t, "epid-secret", payload["epid"])
			require.Equal(t, "recovery-token-secret", payload["token"])
			return scriptedResponse(req, 200, req.URL.String(), `{"token":"a:authorized-secret","isPasswordExpiryEnabled":false}`, nil), nil
		},
	)

	verification, err := verifyPasswordRecoveryCode(session, flow, proof, "qalpha01@recovery.test", " 654321 ")

	require.NoError(t, err)
	require.Equal(t, "a:authorized-secret", verification.token)
	require.NoError(t, requireAuthorizedPasswordRecoveryToken(verification.token))
	client.requireDone()
}

func TestSubmitPasswordRecoveryResetRequestBoundary(t *testing.T) {
	flow := testPasswordRecoveryFlow()
	proof := testPasswordRecoveryProof()
	const newPassword = "Strong-Password-2026!"
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, passwordRecoveryResetPasswordURL)
			requireRecoveryAPIHeaders(t, req, "picker-api-canary")
			payload := decodeJSONRequest(t, req)
			require.Equal(t, "owner@example.test", payload["signinName"])
			require.Equal(t, "a:authorized-secret", payload["token"])
			require.Equal(t, newPassword, payload["password"])
			require.Equal(t, false, payload["expiryEnabled"])
			require.Equal(t, "epid-secret", payload["epid"])
			require.NotContains(t, payload, "needsSlt")
			require.NotContains(t, payload, "tfaEpid")
			return scriptedResponse(req, 200, req.URL.String(), `{"enableTfa":false,"eaAutoVetoed":false}`, nil), nil
		},
	)

	err := submitPasswordRecoveryReset(session, flow, proof, "a:authorized-secret", newPassword, false)

	require.NoError(t, err)
	client.requireDone()
}

func TestSecondProofTokenFailsBeforeResetPasswordRequest(t *testing.T) {
	flow := testPasswordRecoveryFlow()
	proof := testPasswordRecoveryProof()
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, passwordRecoveryVerifyCodeURL)
			return scriptedResponse(req, 200, req.URL.String(), `{"token":"r:second-proof-secret"}`, nil), nil
		},
	)

	verification, err := verifyPasswordRecoveryCode(session, flow, proof, "qalpha01@recovery.test", "654321")
	require.NoError(t, err)
	err = requireAuthorizedPasswordRecoveryToken(verification.token)
	require.Error(t, err)
	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, AuthStatusMFARequired, authErr.Status)
	// The scripted client has no ResetPassword step. Requiring it to be done
	// proves the r: state did not continue into the destructive endpoint.
	client.requireDone()
}

func TestResetPasswordViaRecoveryDisabledBeforeNetwork(t *testing.T) {
	result, err := ResetPasswordViaRecovery(
		context.Background(),
		"owner@example.test",
		"Strong-Password-2026!",
		"definitely-not-a-valid-proxy:1",
		PasswordRecoveryResetOptions{},
	)
	require.Error(t, err)
	require.False(t, result.PasswordReset)
}

type passwordRecoverySequenceReader struct {
	calls          atomic.Int32
	watcherStarted chan struct{}
	sendStarted    chan struct{}
}

func (r *passwordRecoverySequenceReader) List(ctx context.Context, mailbox string, _ int, _ bool) ([]EmailObj, error) {
	// The destructive path first rechecks recovery eligibility and then takes
	// the OTP watcher's baseline snapshot. Both reads must complete before the
	// watcher waits for SendOtt.
	if r.calls.Add(1) <= 2 {
		return []EmailObj{{ID: 1, To: mailbox, Subject: "Microsoft security code", Preview: "Security code 111111"}}, nil
	}
	select {
	case <-r.watcherStarted:
	default:
		close(r.watcherStarted)
	}
	select {
	case <-r.sendStarted:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []EmailObj{{
		ID:      2,
		To:      mailbox,
		From:    "account-security-noreply@accountprotection.microsoft.com",
		Subject: "Microsoft security code",
		Preview: "Security code 654321",
	}}, nil
}

func (*passwordRecoverySequenceReader) SearchByContent(context.Context, string, int) ([]EmailObj, error) {
	return nil, nil
}

func TestEnabledPasswordRecoveryResetRunsOneSendVerifyAndReset(t *testing.T) {
	previousReader := activeMailboxReader()
	previousDomains := activeAuxiliaryDomains()
	reader := &passwordRecoverySequenceReader{
		watcherStarted: make(chan struct{}),
		sendStarted:    make(chan struct{}),
	}
	SetMailboxReader(reader)
	defer SetMailboxReader(previousReader)
	SetAuxiliaryDomains([]string{"recovery.test"})
	defer SetAuxiliaryDomains(previousDomains)

	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, passwordRecoveryEntryURL, passwordRecoveryInitialFixture, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, req.URL.String(), passwordRecoveryPickerFixture, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, passwordRecoverySendOTTURL)
			select {
			case <-reader.watcherStarted:
			case <-time.After(time.Second):
				require.FailNow(t, "mail watcher did not start before recovery SendOtt")
			}
			close(reader.sendStarted)
			return scriptedResponse(req, 200, req.URL.String(), `{"apiCanary":"after-send"}`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, passwordRecoveryVerifyCodeURL)
			payload := decodeJSONRequest(t, req)
			require.Equal(t, "654321", payload["code"])
			require.Equal(t, "after-send", req.Header.Get("canary"))
			return scriptedResponse(req, 200, req.URL.String(), `{"apiCanary":"after-verify","token":"a:authorized-secret"}`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, passwordRecoveryResetPasswordURL)
			require.Equal(t, "after-verify", req.Header.Get("canary"))
			return scriptedResponse(req, 200, req.URL.String(), `{}`, nil), nil
		},
	)

	result, err := resetPasswordViaRecoveryWithSession(
		session,
		"owner@example.test",
		"Strong-Password-2026!",
		"",
		PasswordRecoveryResetOptions{
			EnablePasswordReset:     true,
			PreferredBindingAddress: "qalpha01@recovery.test",
			ExpectedBindingAddress:  "qalpha01@recovery.test",
			CodeTimeout:             time.Second,
		},
	)

	require.NoError(t, err)
	require.True(t, result.PasswordReset)
	require.True(t, result.Probe.BindingResolved)
	client.requireDone()
}

func TestEnabledPasswordRecoveryResetStopsWhenReprobeBindingDiffers(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, passwordRecoveryEntryURL, passwordRecoveryInitialFixture, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, req.URL.String(), passwordRecoveryPickerFixture, nil), nil
		},
	)

	result, err := resetPasswordViaRecoveryWithSession(
		session,
		"owner@example.test",
		"Strong-Password-2026!",
		"",
		PasswordRecoveryResetOptions{
			EnablePasswordReset:     true,
			PreferredBindingAddress: "qalpha01@recovery.test",
			ExpectedBindingAddress:  "qanother@recovery.test",
		},
	)

	require.Error(t, err)
	require.False(t, result.PasswordReset)
	require.ErrorContains(t, err, "changed before password reset")
	client.requireDone() // No SendOtt, VerifyCode, or ResetPassword request was made.
}

func TestPasswordRecoveryAPIMapsWrongCodeWithoutLeakingServerData(t *testing.T) {
	flow := testPasswordRecoveryFlow()
	proof := testPasswordRecoveryProof()
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 400, req.URL.String(), `{"error":{"code":"1215","data":"do-not-leak-this"}}`, nil), nil
		},
	)

	_, err := verifyPasswordRecoveryCode(session, flow, proof, "qalpha01@recovery.test", "000000")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "do-not-leak-this")
	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, AuthStatusVerifyCodeError, authErr.Status)
	client.requireDone()
}

func testPasswordRecoveryFlow() *passwordRecoveryFlow {
	return &passwordRecoveryFlow{
		accountEmail:  "owner@example.test",
		pageURL:       "https://account.live.com/password/reset?uaid=uaid-fixture#/proofs",
		apiCanary:     "picker-api-canary",
		recoveryToken: "recovery-token-secret",
		uaid:          "uaid-fixture",
		scenarioID:    passwordRecoveryDefaultScenarioID,
		uiFlavor:      passwordRecoveryDefaultUIFlavor,
		hostPageID:    passwordRecoveryDefaultPageID,
	}
}

func testPasswordRecoveryProof() passwordRecoveryProof {
	return passwordRecoveryProof{
		Name:            "qa*****@recovery.test",
		Type:            "Email",
		Channel:         "Email",
		EPID:            "epid-secret",
		RequiresReentry: 1,
	}
}

func requireRecoveryAPIHeaders(t *testing.T, req *http.Request, canary string) {
	t.Helper()
	require.Equal(t, "application/json; charset=utf-8", req.Header.Get("Content-Type"))
	require.Equal(t, "https://account.live.com", req.Header.Get("Origin"))
	require.Equal(t, canary, req.Header.Get("canary"))
	require.Equal(t, strconv.Itoa(passwordRecoveryDefaultPageID), req.Header.Get("hpgid"))
	require.Equal(t, "0", req.Header.Get("hpgact"))
}

func decodeJSONRequest(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(req.Body)
	decoder.UseNumber()
	var payload map[string]any
	require.NoError(t, decoder.Decode(&payload))
	return payload
}
