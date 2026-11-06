package msacl

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"

	http "github.com/bogdanfinn/fhttp"
	"github.com/stretchr/testify/require"
)

const deviceEmailCodePage = `<script>var ServerData={"sPageId":"i5600","iSAMode":2,"fAllowManualTFA":true,"sFT":"identity-token","urlPost":"https://login.live.com/ppsecure/post.srf?id=293577&uaid=device-request","iSAContext":4,"fHideSmsInMfaProofs":true,"fIsInfoPageShown":true,"arrUserProofs":[{"type":1,"display":"o*****r@aishop6.com","data":"email-proof","otcEnabled":true,"pushEnabled":false,"isLost":false}]};</script>`

func TestEmailApprovalOnlyUsesExplicitlyOfferedKnownMailbox(t *testing.T) {
	require.True(t, emailCodeApprovalOffered(deviceEmailCodePage, "owner@aishop6.com"))
	for _, page := range []string{
		strings.Replace(deviceEmailCodePage, `"fAllowManualTFA":true`, `"fAllowManualTFA":false`, 1),
		strings.Replace(deviceEmailCodePage, `"otcEnabled":true`, `"otcEnabled":false`, 1),
		strings.Replace(deviceEmailCodePage, `"pushEnabled":false`, `"pushEnabled":true`, 1),
		strings.Replace(deviceEmailCodePage, `"isLost":false`, `"isLost":true`, 1),
		strings.Replace(deviceEmailCodePage, `"type":1`, `"type":10`, 1),
		strings.Replace(deviceEmailCodePage, `"iSAMode":2`, `"iSAMode":1`, 1),
	} {
		require.False(t, emailCodeApprovalOffered(page, "owner@aishop6.com"))
	}
	require.False(t, emailCodeApprovalOffered(deviceEmailCodePage, "someone@aishop6.com"))
	require.False(t, emailCodeApprovalOffered(deviceEmailCodePage, "o*****r@aishop6.com"))
}

func TestPasswordEmailVerificationKeepsDeviceGrantContext(t *testing.T) {
	reader := &sequencedMailboxReader{watcherStarted: make(chan struct{}), sendStarted: make(chan struct{})}
	previous := activeMailboxReader()
	SetMailboxReader(reader)
	defer SetMailboxReader(previous)
	const sendURL = "https://login.live.com/GetOneTimeCode.srf?id=293577&client_id=0000000040C8F39E"
	const postURL = "https://login.live.com/ppsecure/post.srf?id=293577&uaid=device-request"
	const finishURL = "https://login.live.com/oauth20_remoteconnect.srf?completed=1"
	s, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, sendURL)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "eOTT_OneTimePassword", fields.Get("purpose"))
			require.Equal(t, "identity-token", fields.Get("flowtoken"))
			require.Equal(t, "owner@aishop6.com", fields.Get("ProofConfirmation"))
			close(reader.sendStarted)
			return scriptedResponse(req, 200, sendURL, `{"State":201}`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, postURL)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "18", fields.Get("type"))
			require.Equal(t, "654321", fields.Get("otc"))
			require.Equal(t, "4", fields.Get("sacxt"))
			require.Equal(t, "true", fields.Get("hideSmsInMfaProofs"))
			require.Equal(t, "1", fields.Get("infoPageShown"))
			return scriptedResponse(req, 200, finishURL, "device grant continuation", nil), nil
		},
	)
	s.ctx = WithConcreteRecoveryLeaseKeys(WithRecoveryLeaseScope(context.Background(), 42, ""))
	page, location, lease, err := submitPasswordEmailProof(s, deviceEmailCodePage, "https://login.live.com/login.srf", "original-request", "owner@outlook.com", "", "owner@aishop6.com", sendURL)
	require.NoError(t, err)
	require.Equal(t, finishURL, location)
	require.Equal(t, "device grant continuation", page)
	require.True(t, lease.sent)
	release := RecoveryLeaseReleaser(s.context())
	require.NotNil(t, release, "the sent-code lease must remain available for post-persistence cleanup")
	pending := &pendingAccountAuthorization{session: s, codeMailLease: lease}
	pending.releaseCodeMailLease(s.context())
	require.Nil(t, RecoveryLeaseReleaser(s.context()), "the authorization terminal path must release the sent-code lease")
	client.requireDone()
}

func TestPasswordEmailVerificationRejectsUntrustedDestination(t *testing.T) {
	const sendURL = "https://login.live.com/GetOneTimeCode.srf"
	for _, target := range []struct{ page, sendURL string }{
		{deviceEmailCodePage, "https://outside.test/GetOneTimeCode.srf"},
		{deviceEmailCodePage, "http://login.live.com/GetOneTimeCode.srf"},
		{deviceEmailCodePage, "https://login.live.com:8443/GetOneTimeCode.srf"},
		{strings.Replace(deviceEmailCodePage, "https://login.live.com/ppsecure", "https://outside.test/ppsecure", 1), sendURL},
		{strings.Replace(deviceEmailCodePage, "https://login.live.com/ppsecure/post.srf?id=293577&uaid=device-request", sendURL, 1), sendURL},
	} {
		s, client := newScriptedSession(t)
		_, _, _, err := submitPasswordEmailProof(s, target.page, "https://login.live.com/login.srf", "request", "owner@outlook.com", "", "owner@aishop6.com", target.sendURL)
		require.Error(t, err)
		client.requireDone()
	}
}
