package msacl

import (
	"encoding/json"
	"errors"
	"testing"

	http "github.com/bogdanfinn/fhttp"
	"github.com/stretchr/testify/require"
)

const pendingRecoveryFixture = `<script>var t0={"email":"owner@outlook.com","apiCanary":"initial-canary","eipt":"fixture-ipt","uaid":"fixture-uaid","uiflvr":1001,"scid":100109,"hpgid":201030,"WLXAccount":{"recoveryCancel":{"startState":"landing","sessionId":"pending-session","viewContext":{"data":{"replacementProof":{"name":"10*****@qq.com"},"rawProofList":"[{\"name\":\"o***@old.test\",\"type\":\"Email\",\"epid\":\"current-proof\"}]"}}}}};</script>`

func TestCancellationRejectsChangedIdentityDestinationOrProofBeforeSending(t *testing.T) {
	previous := activeAuxiliaryDomains()
	SetAuxiliaryDomains([]string{"old.test"})
	defer SetAuxiliaryDomains(previous)
	for _, args := range [][3]string{
		{"different@outlook.com", "old@old.test", "10*****@qq.com"},
		{"owner@outlook.com", "old@old.test", "different@qq.com"},
		{"owner@outlook.com", "wrong@old.test", "10*****@qq.com"},
		{"owner@outlook.com", "old@external.test", "10*****@qq.com"},
	} {
		s, client := fastProofSession(t)
		result, err := cancelRecoveryWithSession(s, args[0], "", args[1], args[2], &unauthenticatedProofPage{pendingRecoveryFixture, "https://account.live.com/ar/cancel"})
		require.Error(t, err)
		require.False(t, result.Cancelled)
		client.requireDone()
	}
}

func TestCancellationVerifiesCurrentMailboxBeforeVetoAndPreservesRecoveryCode(t *testing.T) {
	previousDomains, previousReader := activeAuxiliaryDomains(), activeMailboxReader()
	SetAuxiliaryDomains([]string{"old.test"})
	defer SetAuxiliaryDomains(previousDomains)
	defer SetMailboxReader(previousReader)
	for _, rejectVerification := range []bool{false, true} {
		reader := &sequencedMailboxReader{watcherStarted: make(chan struct{}), sendStarted: make(chan struct{})}
		SetMailboxReader(reader)
		steps := []scriptedHTTPStep{
			func(req *http.Request, _ bool) (*http.Response, error) {
				require.Equal(t, "/API/Proofs/SendOtt", req.URL.Path)
				require.Equal(t, "fixture-ipt", req.Header.Get("eipt"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Equal(t, "VetoEA", body["purpose"])
				require.Equal(t, "old@old.test", body["confirmProof"])
				require.Equal(t, "current-proof", body["epid"])
				close(reader.sendStarted)
				return scriptedResponse(req, 200, req.URL.String(), `{"apiCanary":"fresh-canary"}`, nil), nil
			},
			func(req *http.Request, _ bool) (*http.Response, error) {
				require.Equal(t, "/API/Proofs/VerifyCode", req.URL.Path)
				require.Equal(t, "fresh-canary", req.Header.Get("canary"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Equal(t, "654321", body["code"])
				require.Equal(t, "IptVerify", body["action"])
				require.Equal(t, "EAVeto", body["purpose"])
				response := `{"token":"verified-current-proof"}`
				if rejectVerification {
					response = `{"error":{"code":"1234"}}`
				}
				return scriptedResponse(req, 200, req.URL.String(), response, nil), nil
			},
		}
		if !rejectVerification {
			steps = append(steps, func(req *http.Request, _ bool) (*http.Response, error) {
				require.Equal(t, "/API/Recovery/VetoEARequest", req.URL.Path)
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Equal(t, "verified-current-proof", body["token"])
				require.Equal(t, "pending-session", body["sessionId"])
				require.Equal(t, false, body["repComp"])
				require.Equal(t, false, body["usedTD"])
				return scriptedResponse(req, 200, req.URL.String(), `{"recoveryCode":"TEST-RECOVERY-CODE"}`, nil), nil
			})
		}
		s, client := fastProofSession(t, steps...)
		result, err := cancelRecoveryWithSession(s, "owner@outlook.com", "", "old@old.test", "10*****@qq.com", &unauthenticatedProofPage{pendingRecoveryFixture, "https://account.live.com/ar/cancel"})
		if rejectVerification {
			require.Error(t, err)
			require.False(t, result.Cancelled)
		} else {
			require.NoError(t, err)
			require.True(t, result.Cancelled)
			require.Equal(t, "TEST-RECOVERY-CODE", result.RecoveryCode)
			encoded, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "TEST-RECOVERY-CODE")
		}
		client.requireDone()
	}
}

func TestPendingPageInspectionDoesNotSendVerificationCodes(t *testing.T) {
	s, client := fastProofSession(t, func(req *http.Request, _ bool) (*http.Response, error) {
		return scriptedResponse(req, 200, "https://account.live.com/ar/cancel", pendingRecoveryFixture, nil), nil
	})
	_, err := loadManagedProofs(s)
	require.ErrorIs(t, err, errProofSessionUnauthenticated)
	var pending *unauthenticatedProofPage
	require.True(t, errors.As(err, &pending))
	require.NotContains(t, err.Error(), "fixture-ipt")
	client.requireDone()
}
