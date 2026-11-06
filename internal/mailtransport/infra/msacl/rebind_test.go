package msacl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/stretchr/testify/require"
)

const oldProofFixture = `{"displayProofName":"old@aishop6.com","proofId":"old-id","proofType":"Email","isVerified":1}`

func TestRebindUnavailablePasswordlessLoginUsesPasswordPortalBeforeAnyMutation(t *testing.T) {
	for _, loginErr := range []error{errNoOTCEmailProof} {
		s, client := fastProofSession(t, func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, microsoftConsentLoginURL)
			return scriptedResponse(req, 200, microsoftConsentManageURL, `<script>consentManageService</script>`, nil), nil
		})
		require.NoError(t, completeRebindLogin(s, "owner@outlook.com", "password", "", "owner@new.test", loginErr))
		client.requireDone()
	}
	for _, status := range []string{AuthStatusCodeTimeout, AuthStatusMFARequired, AuthStatusPasskeyRequired, AuthStatusPhoneVerification, AuthStatusAccountLocked, AuthStatusVerifyCodeError, AuthStatusRateLimited} {
		s, client := fastProofSession(t)
		err := newAuthError("identity check incomplete", status)
		require.ErrorIs(t, completeRebindLogin(s, "owner@outlook.com", "password", "", "owner@new.test", err), err)
		client.requireDone()
	}
	for _, state := range []string{"mutation_started", "existing_only", "canceled"} {
		s, client := fastProofSession(t)
		switch state {
		case "mutation_started":
			s.ctx = context.WithValue(s.ctx, recoveryProofProgressKey{}, &recoveryProofProgress{attempted: true})
		case "existing_only":
			s.ctx = WithExistingRecoveryProofOnly(s.ctx)
		case "canceled":
			ctx, cancel := context.WithCancel(s.ctx)
			cancel()
			s.ctx = ctx
		}
		require.Error(t, completeRebindLogin(s, "owner@outlook.com", "password", "", "owner@new.test", errNoOTCEmailProof))
		client.requireDone()
	}
}

func TestRebindUnknownDiscoveryUsesAuthenticatedPasswordFormBeforePortal(t *testing.T) {
	s, client := fastProofSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, addAssocIDURL)
			return scriptedResponse(req, 200, addAssocIDURL, `<input name="canary" value="authenticated-session">`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, microsoftConsentLoginURL)
			return scriptedResponse(req, 200, microsoftConsentManageURL, `<script>consentManageService</script>`, nil), nil
		},
	)
	err := completeRebindLogin(s, "owner@outlook.com", "password", "", "owner@new.test", newAuthError("discovery unavailable", AuthStatusUnknownMailbox))
	require.NoError(t, err)
	client.requireDone()
	s, client = fastProofSession(t)
	err = completeRebindLogin(s, "owner@outlook.com", "", "", "owner@new.test", newAuthError("discovery unavailable", AuthStatusUnknownMailbox))
	var auth *AuthError
	require.ErrorAs(t, err, &auth)
	require.Equal(t, AuthStatusPasswordError, auth.Status)
	client.requireDone()
}

func TestRebindPasswordLoginOverrideRequiresPasswordWithoutSendingLoginCodes(t *testing.T) {
	t.Setenv("MSACL_FORCE_PASSWORD_LOGIN", "1")
	result, err := RebindRecoveryMailbox(context.Background(), "owner@outlook.com", "", "", "old@aishop6.com", "new@aishop6.com")
	var auth *AuthError
	require.ErrorAs(t, err, &auth)
	require.Equal(t, AuthStatusPasswordError, auth.Status)
	require.Equal(t, "login", result.Stage)
	require.False(t, result.Verified)
	require.False(t, result.OldProofRemoved)
}

func TestUnapprovedSessionClassifiesExternalMailboxAndAuthenticator(t *testing.T) {
	for _, test := range []struct {
		name   string
		state  int
		proofs string
		status string
	}{
		{"external", 3, `[{"type":10,"isSADef":true},{"type":1,"display":"u2*****@888910.xyz"}]`, AuthStatusAlreadyBound},
		{"authenticator", 3, `[{"type":10,"isSADef":true}]`, AuthStatusMFARequired},
		{"denied", 1, `[{"type":1,"display":"u2*****@888910.xyz"}]`, AuthStatusVerifyCodeError},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, client := fastProofSession(t, func(req *http.Request, _ bool) (*http.Response, error) {
				requireRequest(t, req, http.MethodPost, "https://login.live.com/GetSessionState.srf")
				return scriptedResponse(req, 200, req.URL.String(), fmt.Sprintf(`{"AuthorizationState":%d}`, test.state), nil), nil
			})
			var proofs []any
			require.NoError(t, json.Unmarshal([]byte(test.proofs), &proofs))
			_, _, err := handleSessionApprovalPolling(s, "", "https://login.live.com/oauth20_authorize.srf", map[string]any{"urlSessionState": "https://login.live.com/GetSessionState.srf", "iPollingTimeout": 1, "arrUserProofs": proofs})
			var auth *AuthError
			require.ErrorAs(t, err, &auth)
			require.Equal(t, test.status, auth.Status)
			client.requireDone()
		})
	}
}

func TestCachedSecurityChallengeReportsExternalProofWithoutAnotherLogin(t *testing.T) {
	page := `<script>var ServerData = {"sPageId":"i5600","arrUserProofs":[{"type":1,"display":"u2*****@888910.xyz"},{"type":10,"isSADef":true}]};</script>`
	s, client := fastProofSession(t, func(req *http.Request, _ bool) (*http.Response, error) {
		requireRequest(t, req, http.MethodGet, proofsManageURL)
		return scriptedResponse(req, 200, "https://login.live.com/login.srf", page, nil), nil
	})
	_, err := loadManagedProofs(s)
	var auth *AuthError
	require.ErrorAs(t, err, &auth)
	require.Equal(t, AuthStatusAlreadyBound, auth.Status)
	require.Equal(t, "u2*****@888910.xyz", auth.BoundMailbox)
	client.requireDone()
}

const rebindCredentialEnrollmentPage = `<script>var ServerData={"sPageId":"Account_CredentialActionInterruptPage_Client","sSigninName":"owner@outlook.com","acmaInitialConfig":{"canary":"safe-canary","navigation":{"baseRouteUri":"https://account.live.com"}},"acmaInitialResponse":{"continuationToken":"initial-token","state":"interactionRequired","action":"enroll","infoType":"email","_embedded":{"methods":[{"type":"email","_links":{"enroll":{"href":"/api/v1.0/auth/methods/email"}}}]}}};</script>`
const rebindTermsNotice = `<script>var ServerData={"sPageId":"Account_TOUAccrualInterruptPage_Client","urlRU":"https://account.live.com/tou/accrue?ru=proofs","sCanary":"notice-canary"};</script>`

func TestRebindCompletesRequiredEmailEnrollmentBeforeProofReadback(t *testing.T) {
	previousReader, previousDomains := activeMailboxReader(), activeAuxiliaryDomains()
	reader := &sequencedMailboxReader{watcherStarted: make(chan struct{}), sendStarted: make(chan struct{})}
	SetMailboxReader(reader)
	SetAuxiliaryDomains([]string{"new.test"})
	defer func() { SetMailboxReader(previousReader); SetAuxiliaryDomains(previousDomains) }()
	s, client := fastProofSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, proofsManageURL)
			return scriptedResponse(req, 200, "https://account.live.com/interrupt/credentialaction", rebindCredentialEnrollmentPage, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/api/v1.0/auth/methods/email")
			payload := decodeJSONRequest(t, req)
			require.Equal(t, "owner@new.test", payload["email"])
			select {
			case <-reader.watcherStarted:
			case <-time.After(time.Second):
				require.FailNow(t, "mail watcher must start before enrollment")
			}
			close(reader.sendStarted)
			return scriptedResponse(req, 200, req.URL.String(), `{"continuationToken":"activate-token","action":"activate","id":"method-id","type":"email","hint":"owner@new.test","_links":{"activate":{"href":"/api/v1.0/auth/methods/email/method-id/activate"}}}`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/api/v1.0/auth/methods/email/method-id/activate")
			require.Equal(t, "654321", decodeJSONRequest(t, req)["otp"])
			return scriptedResponse(req, 200, req.URL.String(), `{"continuationToken":"continue-token","_links":{"continue":{"href":"https://login.live.com/ppsecure/post.srf"}}}`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://login.live.com/ppsecure/post.srf")
			return scriptedResponse(req, 200, "https://account.live.com/tou/accrue?ru=proofs", rebindTermsNotice, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, proofsManageURL)
			return scriptedResponse(req, 200, "https://account.live.com/tou/accrue?ru=proofs", rebindTermsNotice, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/tou/accrue?ru=proofs")
			return scriptedResponse(req, 200, proofsManageURL, proofFixture(targetOnly), nil), nil
		},
		proofRead(t, targetOnly), proofRead(t, targetOnly),
	)
	progress := &recoveryProofProgress{target: "owner@new.test"}
	s.ctx = context.WithValue(s.ctx, recoveryProofProgressKey{}, progress)
	result, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@new.test", "owner@new.test")
	require.NoError(t, err)
	require.True(t, progress.attempted)
	require.True(t, result.Verified)
	require.True(t, result.OldProofRemoved)
	require.Equal(t, "remote_complete", result.Stage)
	client.requireDone()
}

func TestRebindEnrollmentRejectsDifferentIdentityAndExistingOnlyMode(t *testing.T) {
	for _, mode := range []string{"identity", "location", "existing_only"} {
		page, location := rebindCredentialEnrollmentPage, "https://account.live.com/interrupt/credentialaction"
		if mode == "identity" {
			page = strings.ReplaceAll(page, "owner@outlook.com", "someone@outlook.com")
		}
		if mode == "location" {
			location = "https://outside.test/interrupt/credentialaction"
		}
		s, client := fastProofSession(t, func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, location, page, nil), nil
		})
		if mode == "existing_only" {
			s.ctx = WithExistingRecoveryProofOnly(s.ctx)
		}
		result, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@new.test", "owner@new.test")
		require.Error(t, err)
		require.False(t, result.Verified)
		client.requireDone()
	}
}

const targetProofFixture = `{"displayProofName":"owner\u0040new.test","proofId":"new-id","proofType":"Email","isVerified":1}`
const bothProofs = `[` + oldProofFixture + `,` + targetProofFixture + `]`
const targetOnly = `[` + targetProofFixture + `]`
const oldOnly = `[` + oldProofFixture + `]`

func proofFixture(proofs string) string {
	return `<script>var t0={"email":"owner@outlook.com","apiCanary":"safe-canary","uiflvr":1001,"scid":100109,"hpgid":201030,"clientTelemetry":{"uaid":"safe-uaid"},"WLXAccount":{"manageProofs":{"emailProofs":` + proofs + `}}};</script>`
}
func proofRead(t *testing.T, proofs string) scriptedHTTPStep {
	return func(req *http.Request, _ bool) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		return scriptedResponse(req, 200, proofsManageURL, proofFixture(proofs), nil), nil
	}
}
func oldProofDelete(t *testing.T, body string) scriptedHTTPStep {
	return func(req *http.Request, _ bool) (*http.Response, error) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "/API/Proofs/DeleteProof", req.URL.Path)
		require.Equal(t, "safe-canary", req.Header.Get("canary"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
		require.Equal(t, "old-id", payload["proofId"])
		return scriptedResponse(req, 200, req.URL.String(), body, nil), nil
	}
}
func fastProofSession(t *testing.T, steps ...scriptedHTTPStep) (*Session, *scriptedHTTPClient) {
	s, c := newScriptedSession(t, steps...)
	s.sleepFn = func(time.Duration) error { return s.context().Err() }
	return s, c
}

func TestRebindParsesProofsAndRejectsIncompleteIdentity(t *testing.T) {
	data, err := parseManagedProofPage(proofFixture(targetOnly))
	require.NoError(t, err)
	require.True(t, data.verified("owner@new.test"))
	for _, body := range []string{`<html>login</html>`, `var t0={};`, proofFixture(`[{"displayProofName":"o***@new.test","proofId":"x","proofType":"Email"}]`)} {
		_, err := parseManagedProofPage(body)
		require.Error(t, err)
	}
}

func TestRebindDeletesOnlyOldProofAndConfirmsStableFinalState(t *testing.T) {
	s, c := fastProofSession(t, proofRead(t, bothProofs), proofRead(t, bothProofs), oldProofDelete(t, `{}`), proofRead(t, targetOnly), proofRead(t, targetOnly))
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.NoError(t, err)
	require.True(t, r.Verified)
	require.True(t, r.OldProofRemoved)
	require.Equal(t, "rebind", r.Operation)
	require.Equal(t, "remote_complete", r.Stage)
	c.requireDone()
}

func TestRebindAlreadyCompleteDoesNotMutate(t *testing.T) {
	s, c := fastProofSession(t, proofRead(t, targetOnly), proofRead(t, targetOnly))
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.NoError(t, err)
	require.True(t, r.Verified)
	require.Equal(t, "already_bound", r.Operation)
	c.requireDone()
}

func TestInitialBindingVerifiesNewAddressWithoutDeletingAnyProof(t *testing.T) {
	previousReader, previousDomains := activeMailboxReader(), activeAuxiliaryDomains()
	reader := &sequencedMailboxReader{watcherStarted: make(chan struct{}), sendStarted: make(chan struct{})}
	SetMailboxReader(reader)
	SetAuxiliaryDomains([]string{"new.test"})
	defer func() { SetMailboxReader(previousReader); SetAuxiliaryDomains(previousDomains) }()
	addPage := `<form action="https://account.live.com/proofs/Add"><input name="EmailAddress"><input type="hidden" name="canary" value="test-canary"></form>`
	s, client := fastProofSession(t, proofRead(t, `[]`),
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, microsoftProofsAddURL)
			return scriptedResponse(req, 200, microsoftProofsAddURL, addPage, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, microsoftProofsAddURL)
			require.NoError(t, req.ParseForm())
			require.Equal(t, "owner@new.test", req.PostForm.Get("EmailAddress"))
			close(reader.sendStarted)
			return scriptedResponse(req, 200, "https://account.live.com/proofs/Verify", `<form id="frmVerifyProof" action="https://account.live.com/proofs/Verify"><input type="hidden" name="canary" value="test-canary"><input name="iOttText"></form>`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/proofs/Verify")
			require.NoError(t, req.ParseForm())
			require.Equal(t, "654321", req.PostForm.Get("iOttText"))
			return scriptedResponse(req, 200, proofsManageURL, proofFixture(targetOnly), nil), nil
		}, proofRead(t, targetOnly), proofRead(t, targetOnly), proofRead(t, targetOnly))
	result, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "stale@aishop6.com", "owner@new.test")
	require.NoError(t, err)
	require.True(t, result.Verified)
	require.Equal(t, "bind", result.Operation)
	client.requireDone()
}

func TestEnrollmentDuringLoginTracksUncertainWritesAndFinishOnlyBlocksThem(t *testing.T) {
	previousReader, previousDomains := activeMailboxReader(), activeAuxiliaryDomains()
	SetMailboxReader(emptyMailboxReader{})
	SetAuxiliaryDomains([]string{"new.test"})
	defer func() { SetMailboxReader(previousReader); SetAuxiliaryDomains(previousDomains) }()
	page := `<form action="https://account.live.com/proofs/Add"><input name="EmailAddress"></form>`
	for _, finishOnly := range []bool{false, true} {
		progress := &recoveryProofProgress{target: "owner@new.test"}
		var steps []scriptedHTTPStep
		if !finishOnly {
			steps = append(steps, func(_ *http.Request, _ bool) (*http.Response, error) {
				require.True(t, progress.attempted)
				return nil, io.ErrUnexpectedEOF
			})
		}
		s, client := fastProofSession(t, steps...)
		s.ctx = context.WithValue(s.context(), recoveryProofProgressKey{}, progress)
		if finishOnly {
			s.ctx = WithExistingRecoveryProofOnly(s.ctx)
		}
		_, _, _, err := convergeMicrosoftConsentManage(s, page, microsoftProofsAddURL, "owner@outlook.com", "", progress.target)
		require.Error(t, err)
		require.Equal(t, !finishOnly, progress.attempted)
		if finishOnly {
			require.ErrorIs(t, err, ErrRecoveryProofNotStarted)
		}
		client.requireDone()
	}
}

func TestRebindRetainsOldProofWhenOnlyAdditionWasRequested(t *testing.T) {
	s, client := fastProofSession(t, proofRead(t, bothProofs), proofRead(t, bothProofs))
	s.ctx = WithRetainedRecoveryProof(s.ctx)
	result, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.NoError(t, err)
	require.True(t, result.Verified)
	require.False(t, result.OldProofRemoved)
	require.Equal(t, "remote_complete", result.Stage)
	client.requireDone()
}

func TestRetainingOldProofRejectsConcurrentProofChanges(t *testing.T) {
	unexpected := strings.Replace(bothProofs, "old@aishop6.com", "unexpected@aishop6.com", 1)
	for _, current := range []string{targetOnly, unexpected} {
		s, client := fastProofSession(t, proofRead(t, bothProofs), proofRead(t, current))
		s.ctx = WithRetainedRecoveryProof(s.ctx)
		result, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
		require.Error(t, err)
		require.False(t, result.Verified)
		client.requireDone()
	}
}

func TestRebindUnexpectedProofStopsBeforeMutation(t *testing.T) {
	s, c := fastProofSession(t, proofRead(t, bothProofs))
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "different@aishop6.com", "owner@new.test")
	require.Error(t, err)
	require.False(t, r.Verified)
	c.requireDone()
}

func TestExternalProofIsSkippedEvenWhenItsFullAddressWasStored(t *testing.T) {
	previous := activeAuxiliaryDomains()
	SetAuxiliaryDomains([]string{"new.test"})
	defer SetAuxiliaryDomains(previous)
	s, client := fastProofSession(t, proofRead(t, bothProofs))
	result, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	var auth *AuthError
	require.ErrorAs(t, err, &auth)
	require.Equal(t, AuthStatusAlreadyBound, auth.Status)
	require.Equal(t, "old@aishop6.com", auth.BoundMailbox)
	require.Equal(t, "inspect", result.Stage)
	client.requireDone()
}

func TestRebindUnverifiedReplacementNeverDeletesOldProof(t *testing.T) {
	steps := []scriptedHTTPStep{proofRead(t, oldOnly), func(req *http.Request, _ bool) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "/proofs/Add", req.URL.Path)
		return scriptedResponse(req, 200, req.URL.String(), `<html>not authenticated</html>`, nil), nil
	}}
	for i := 0; i < recoveryProofConfirmationReads; i++ {
		steps = append(steps, proofRead(t, oldOnly))
	}
	s, c := fastProofSession(t, steps...)
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.Error(t, err)
	require.False(t, r.Verified)
	c.requireDone()
}

func TestRebindPendingProofDoesNotAddDuplicateProof(t *testing.T) {
	pending := strings.Replace(targetProofFixture, `"isVerified":1`, `"isVerified":0`, 1)
	proofs := `[` + oldProofFixture + `,` + pending + `]`
	var steps []scriptedHTTPStep
	for i := 0; i < recoveryProofConfirmationReads; i++ {
		steps = append(steps, proofRead(t, proofs))
	}
	s, c := fastProofSession(t, steps...)
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.Error(t, err)
	require.False(t, r.Verified)
	require.Equal(t, "new_proof_pending", r.Stage)
	c.requireDone()
}

func TestFinishExistingDoesNotStartAnUntouchedAccount(t *testing.T) {
	s, c := fastProofSession(t, proofRead(t, oldOnly))
	s.ctx = WithExistingRecoveryProofOnly(s.ctx)
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.ErrorIs(t, err, ErrRecoveryProofNotStarted)
	require.False(t, r.Verified)
	require.Equal(t, "inspect", r.Stage)
	c.requireDone()
}

func TestMissingOTCSessionIsAnErrorRatherThanEvidenceOfNoRecoveryProof(t *testing.T) {
	s, client := fastProofSession(t)
	_, err := getOTCCredentialType(s, "owner@outlook.com", "ppft", "", "opid", "https://login.live.com/")
	var auth *AuthError
	require.ErrorAs(t, err, &auth)
	require.Equal(t, AuthStatusAuthTimeout, auth.Status)
	client.requireDone()
}

func TestPendingSecurityInfoErrorExposesOnlyDestination(t *testing.T) {
	page := `var t0={"apiCanary":"private-canary","eipt":"private-token","WLXAccount":{"recoveryCancel":{"viewContext":{"data":{"replacementProof":{"name":"10*****@qq.com"}}}}}};`
	err := &unauthenticatedProofPage{page: page, url: "https://account.live.com/ar/cancel"}
	require.ErrorIs(t, err, errProofSessionUnauthenticated)
	require.Equal(t, "Microsoft security-info replacement is pending (10*****@qq.com)", err.Error())
	err.url = "https://outside.test/ar/cancel"
	require.Equal(t, errProofSessionUnauthenticated.Error(), err.Error())
}

func TestRebindReconcilesStaleReadsAfterDeletion(t *testing.T) {
	s, c := fastProofSession(t, proofRead(t, bothProofs), proofRead(t, bothProofs), oldProofDelete(t, `{}`), proofRead(t, oldOnly), proofRead(t, targetOnly), proofRead(t, targetOnly))
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.NoError(t, err)
	require.True(t, r.Verified)
	c.requireDone()
}

func TestRebindWaitsThroughLongStaleReadbackWithoutRepeatingDeletion(t *testing.T) {
	steps := []scriptedHTTPStep{proofRead(t, bothProofs), proofRead(t, bothProofs), oldProofDelete(t, `{}`)}
	for i := 0; i < 9; i++ {
		steps = append(steps, proofRead(t, oldOnly))
	}
	steps = append(steps, proofRead(t, targetOnly), proofRead(t, targetOnly))
	s, c := fastProofSession(t, steps...)
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.NoError(t, err)
	require.True(t, r.Verified)
	require.True(t, r.OldProofRemoved)
	c.requireDone()
}

func TestRebindReconcilesErrorResponseWhenRemoteDeleteSucceeded(t *testing.T) {
	s, c := fastProofSession(t, proofRead(t, bothProofs), proofRead(t, bothProofs), oldProofDelete(t, `{"error":{"code":"6001"}}`), proofRead(t, targetOnly), proofRead(t, targetOnly))
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.NoError(t, err)
	require.True(t, r.Verified)
	c.requireDone()
}

func TestRebindRateLimitHonorsCancellationWithoutDeletingAgain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, c := fastProofSession(t, proofRead(t, bothProofs), proofRead(t, bothProofs), func(req *http.Request, _ bool) (*http.Response, error) {
		require.Equal(t, http.MethodPost, req.Method)
		cancel()
		return scriptedResponse(req, 429, req.URL.String(), `Too Many Requests`, map[string]string{"Retry-After": "60"}), nil
	})
	s.ctx = ctx
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "old@aishop6.com", "owner@new.test")
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, r.Verified)
	c.requireDone()
}

func TestProofManagementFollowsAuthenticatedFormRelay(t *testing.T) {
	s, c := fastProofSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, "https://login.live.com/login.srf", `<form method="post" action="https://account.live.com/proofs/manage/additional"><input type="hidden" name="t" value="relay-token"></form>`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "account.live.com", req.URL.Hostname())
			return scriptedResponse(req, 200, proofsManageURL, proofFixture(targetOnly), nil), nil
		},
	)
	proofs, err := loadManagedProofs(s)
	require.NoError(t, err)
	require.True(t, proofs.verified("owner@new.test"))
	c.requireDone()
}

func TestProofManagementFollowsPrivacyNoticeRedirect(t *testing.T) {
	privacyURL := "https://privacynotice.account.microsoft.com/notice"
	s, c := fastProofSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, privacyURL, `<script>var redirectUrl = 'https://login.live.com/login.srf?id=38936\u0026opid=relay';</script>`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "https://login.live.com/login.srf?id=38936&opid=relay", req.URL.String())
			require.Equal(t, privacyURL, req.Header.Get("Referer"))
			require.True(t, follow)
			return scriptedResponse(req, 200, proofsManageURL, proofFixture(targetOnly), nil), nil
		},
	)
	proofs, err := loadManagedProofs(s)
	require.NoError(t, err)
	require.True(t, proofs.verified("owner@new.test"))
	c.requireDone()
}

func TestRebindMissingLocalBindingUsesOnlyUniqueKnownSystemProof(t *testing.T) {
	previous := activeAuxiliaryDomains()
	SetAuxiliaryDomains([]string{"aishop6.com", "new.test"})
	defer SetAuxiliaryDomains(previous)
	s, c := fastProofSession(t, proofRead(t, bothProofs), proofRead(t, bothProofs), oldProofDelete(t, `{}`), proofRead(t, targetOnly), proofRead(t, targetOnly))
	r, err := rebindRecoveryMailboxWithSession(s, "owner@outlook.com", "", "", "owner@new.test")
	require.NoError(t, err)
	require.True(t, r.Verified)
	c.requireDone()
}
