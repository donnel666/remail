package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

const privateEmailActiveFixture = `{"privateEmailList":[{"id":"old-id","emailAddress":"old@icloud.com","active":true,"forwardToEmail":"relay@example.com"},{"id":"other-id","emailAddress":"other@icloud.com","active":true,"forwardToEmail":"relay@example.com"}],"inactivePrivateEmailList":[]}`
const privateEmailInactiveFixture = `{"privateEmailList":[{"id":"other-id","emailAddress":"other@icloud.com","active":true,"forwardToEmail":"relay@example.com"}],"inactivePrivateEmailList":[{"id":"old-id","emailAddress":"old@icloud.com","active":false,"forwardToEmail":"relay@example.com"}]}`

func TestApplePrivateEmailDeletionMatchesHAR(t *testing.T) {
	for _, tc := range []struct{ operation, list, suffix string }{
		{AppleOnboardingStopPrivateEmail, privateEmailActiveFixture, "stop"},
		{AppleOnboardingRemovePrivateEmail, privateEmailInactiveFixture, "remove"},
	} {
		t.Run(tc.suffix, func(t *testing.T) {
			session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
				{status: 200, body: tc.list},
				{status: 200, body: `{"newToPrivateEmail":false,"useOsloStyle":false,"exists":false,"active":false}`},
			}}
			response, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{
				Operation: tc.operation, Email: "owner@example.com", PrivateEmailID: "old-id", PrivateEmailAddress: "old@icloud.com",
				Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" }),
			})
			require.NoError(t, err)
			require.Equal(t, tc.suffix, response.Next)
			require.Len(t, session.requests, 2)
			require.Contains(t, session.requests[1], "DELETE https://appleid.apple.com/account/manage/email/private/old-id/"+tc.suffix)
			require.Nil(t, session.requestBodies[1])
			require.Equal(t, "api-key", session.requestHeaders[1]["X-Apple-Api-Key"])
			require.Equal(t, "https://account.apple.com", session.requestHeaders[1]["Origin"])
			require.NotEmpty(t, response.Session)
		})
	}
}

func TestApplePrivateEmailDeletionGuardsIdentityAndState(t *testing.T) {
	for _, tc := range []struct {
		name, list, id, email, operation, category string
	}{
		{"active remove", privateEmailActiveFixture, "old-id", "old@icloud.com", AppleOnboardingRemovePrivateEmail, "alias_active"},
		{"different address", privateEmailActiveFixture, "old-id", "other@icloud.com", AppleOnboardingStopPrivateEmail, "alias_changed"},
		{"invalid ID", privateEmailActiveFixture, "../other-id", "old@icloud.com", AppleOnboardingStopPrivateEmail, "invalid_alias"},
		{"incomplete list", `{"privateEmailList":[]}`, "old-id", "old@icloud.com", AppleOnboardingRemovePrivateEmail, "alias_list_invalid"},
		{"duplicate ID", strings.ReplaceAll(privateEmailActiveFixture, "other-id", "old-id"), "old-id", "old@icloud.com", AppleOnboardingStopPrivateEmail, "alias_list_invalid"},
		{"unsafe remote ID", strings.ReplaceAll(privateEmailActiveFixture, "old-id", "../old-id"), "../old-id", "old@icloud.com", AppleOnboardingStopPrivateEmail, "alias_list_invalid"},
		{"conflicting activity", strings.ReplaceAll(privateEmailInactiveFixture, `"active":false`, `"active":true`), "old-id", "old@icloud.com", AppleOnboardingRemovePrivateEmail, "alias_list_invalid"},
		{"already inactive", privateEmailInactiveFixture, "old-id", "old@icloud.com", AppleOnboardingStopPrivateEmail, ""},
		{"already removed", `{"privateEmailList":[],"inactivePrivateEmailList":[]}`, "old-id", "old@icloud.com", AppleOnboardingRemovePrivateEmail, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: 200, body: tc.list}}}
			_, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{
				Operation: tc.operation, PrivateEmailID: tc.id, PrivateEmailAddress: tc.email,
				Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" }),
			})
			if tc.category == "" {
				require.NoError(t, err)
			} else {
				var failure *AppleOnboardingError
				require.ErrorAs(t, err, &failure)
				require.Equal(t, tc.category, failure.Category)
			}
			require.Len(t, session.requests, 1, "failed preflight must not mutate any address")
		})
	}
}

func TestApplePrivateEmailDeleteRejectsFailedAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{200, `{}`}, {200, `{"active":true}`}, {200, `invalid`}, {401, `{}`}, {500, `{}`},
	} {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: 200, body: privateEmailActiveFixture}, {status: tc.status, body: tc.body}}}
		_, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: AppleOnboardingStopPrivateEmail, PrivateEmailID: "old-id", PrivateEmailAddress: "old@icloud.com",
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" }),
		})
		require.Error(t, err)
		require.Len(t, session.requests, 2)
	}
}

func TestApplePrivateEmailCreatesExactSavedCandidate(t *testing.T) {
	session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
		{status: 200, body: `{"emailAddress":"new@icloud.com"}`},
		{status: 200, body: `{"emailAddress":"new@icloud.com","active":true,"id":"new-id"}`},
	}}
	client := appleOnboardingTestClient(time.Now(), session)
	generated, err := client.Execute(context.Background(), AppleOnboardingRequest{Operation: AppleOnboardingGeneratePrivateEmail,
		Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" })})
	require.NoError(t, err)
	require.Equal(t, "new@icloud.com", generated.PrivateEmailAddress)
	_, err = client.Execute(context.Background(), AppleOnboardingRequest{Operation: AppleOnboardingCompletePrivateEmail, PrivateEmailAddress: generated.PrivateEmailAddress, Session: generated.Session})
	require.NoError(t, err)
	require.Contains(t, session.requests[0], "POST https://appleid.apple.com/account/manage/email/private/add")
	require.Contains(t, session.requests[1], "PUT https://appleid.apple.com/account/manage/email/private/add/complete")
	encoded, err := json.Marshal(session.requestBodies[1])
	require.NoError(t, err)
	require.JSONEq(t, `{"emailAddress":"new@icloud.com","label":"ReMail","note":""}`, string(encoded))
}

func TestApplePrivateEmailCreationRejectsDifferentCandidate(t *testing.T) {
	session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: `{"emailAddress":"other@icloud.com"}`}}}
	_, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{Operation: AppleOnboardingCompletePrivateEmail,
		PrivateEmailAddress: "new@icloud.com", Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" })})
	var failure *AppleOnboardingError
	require.True(t, errors.As(err, &failure))
	require.Equal(t, "alias_create_invalid", failure.Category)
}

func TestApplePrivateEmailListExportsCurrentManagementCookie(t *testing.T) {
	session := &appleOnboardingScriptedSession{
		responses: []appleOnboardingScriptedResponse{{status: 200, body: privateEmailInactiveFixture}},
		cookies:   []msacl.SessionCookie{{Name: "myacinfo", Value: "current-session", Domain: ".apple.com", Path: "/"}},
	}
	response, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{
		Operation: AppleOnboardingListPrivateEmails,
		Session:   appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" }),
	})
	require.NoError(t, err)
	require.Len(t, response.PrivateEmails, 2)
	require.NotNil(t, response.NewChannel)
	require.Equal(t, "myacinfo=current-session", response.NewChannel.Cookie)
	require.Equal(t, "api-key", response.NewChannel.APIKey)
}

func TestApplePrivateEmailSessionRecoveryUsesManagementStage(t *testing.T) {
	for _, operation := range []string{AppleOnboardingListPrivateEmails, AppleOnboardingStopPrivateEmail, AppleOnboardingRemovePrivateEmail, AppleOnboardingGeneratePrivateEmail, AppleOnboardingCompletePrivateEmail} {
		_, err := appleOnboardingTestClient(time.Now(), &appleOnboardingScriptedSession{}).Execute(context.Background(), AppleOnboardingRequest{
			Operation: operation, Session: json.RawMessage(`{"version":-1}`),
		})
		var failure *AppleOnboardingError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, "manage_prepare", failure.RestartStage)
	}
	for _, operation := range []string{AppleOnboardingGeneratePrivateEmail, AppleOnboardingCompletePrivateEmail} {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: 401, body: `{}`}}}
		_, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: operation, PrivateEmailAddress: "new@icloud.com",
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" }),
		})
		var failure *AppleOnboardingError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, "manage_prepare", failure.RestartStage)
	}
}
