package icloud

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

func TestAppleOnboardingFamilyVerificationReusesAccountCookie(t *testing.T) {
	for _, test := range []struct {
		name, body, category string
		status               int
	}{
		{name: "confirmed member", status: http.StatusOK, body: testICloudFamilyResponse},
		{name: "not joined", status: http.StatusOK, body: `{"isLinkedToFamily":false,"isMemberOfFamily":false}`, category: "family_membership_unconfirmed"},
		{name: "wrong account", status: http.StatusOK, body: strings.ReplaceAll(testICloudFamilyResponse, "child@example.com", "other@example.com"), category: "family_membership_unconfirmed"},
		{name: "incomplete response", status: http.StatusOK, body: `{"isLinkedToFamily":true,"isMemberOfFamily":true}`, category: "family_membership_unconfirmed"},
		{name: "expired account session", status: http.StatusUnauthorized, body: `{}`, category: "family_session_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: test.status, body: test.body}}}
			state := appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.Mode, state.AccountWidgetKey, state.GSToken = "family", "account-widget", "invitation-token"
				state.Cookies = []msacl.SessionCookie{
					{Name: "myacinfo", Value: "account-cookie", Domain: ".apple.com"},
					{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "old-web-cookie", Domain: ".icloud.com"},
				}
			})
			response, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{
				Operation: appleOnboardingVerifyFamilyMembership, Email: "child@example.com", Session: state,
			})
			if test.category == "" {
				require.NoError(t, err)
				require.Equal(t, "family-1", response.FamilyID)
				require.Equal(t, "ready", response.Next)
			} else {
				var providerErr *AppleOnboardingError
				require.True(t, errors.As(err, &providerErr))
				require.Equal(t, test.category, providerErr.Category)
				if test.status == http.StatusUnauthorized {
					require.Equal(t, "family_membership_prepare", providerErr.RestartStage)
				}
			}
			require.Equal(t, []string{"GET " + iCloudFamilyMembersEndpoint}, session.requests)
			require.Contains(t, session.requestHeaders[0]["Cookie"], "myacinfo=account-cookie")
			require.NotContains(t, session.requestHeaders[0]["Cookie"], "old-web-cookie")
			require.Empty(t, session.requestHeaders[0]["X-Apple-GS-Token"])
		})
	}
}

func TestAdminFamilyMembershipMustPassBeforeWaitingForSharing(t *testing.T) {
	s, db, task, _ := newOnboardingStateTest(t)
	state := appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
		state.Mode, state.AccountWidgetKey = "family", "account-widget"
		state.InviteToken = "invitation-token"
		state.Cookies = []msacl.SessionCookie{{Name: "myacinfo", Value: "account-cookie", Domain: ".apple.com"}}
	})
	header := make(http.Header)
	header.Set("X-Apple-GS-Token", "family-token")
	session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
		{status: http.StatusOK, header: header, body: `{}`},
		{status: http.StatusBadRequest, body: `{}`},
		{status: http.StatusOK, body: `{"isLinkedToFamily":false,"isMemberOfFamily":false}`},
		{status: http.StatusOK, body: `{"isLinkedToFamily":false,"isMemberOfFamily":false}`},
		{status: http.StatusOK, body: strings.ReplaceAll(testICloudFamilyResponse, "child@example.com", task.PrimaryEmail)},
		{status: http.StatusOK, body: `{}`},
		{status: http.StatusOK, body: `{"apiKey":"api","countryCode":"US"}`},
		{status: http.StatusOK, body: `{"privateEmailList":[{"emailAddress":"ready@icloud.com"}],"inactivePrivateEmailList":[]}`},
	}}
	s.onboardingApple = appleOnboardingTestClient(s.now(), session)
	require.NoError(t, db.Model(task).Updates(map[string]any{
		"stage": "family_join_apply", "family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=test",
		"device_code_api": "https://device.example/code", "device_bind_status": "success",
		"session_payload": iCloudJSON(state), "max_attempts": 2,
	}).Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "family_membership_verify", task.Stage)
	require.Equal(t, 2, iCloudOnboardingTaskPhase(task))
	require.Empty(t, task.FamilyID)
	require.False(t, isICloudOnboardingFamilySharingWaitingTask(task))
	require.ErrorIs(t, s.ConfirmICloudOnboardingFamilyReset(context.Background(), task.ID, task.OperatorUserID, "too-early", "/test"), ErrICloudOnboardingInvalid)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "family_membership_verify", task.Stage)
	require.Equal(t, "pending", task.DispatchStatus)
	require.Equal(t, "family_membership_unconfirmed", task.LastErrorCategory)
	require.False(t, isICloudOnboardingFamilySharingWaitingTask(task))
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, iCloudOnboardingFailed, task.Status)
	require.NotEmpty(t, task.SecretPayload)
	require.True(t, isICloudPostFamilyRecoveryAvailable(*task))
	require.NoError(t, s.RetryICloudOnboardingPostFamily(context.Background(), task.ID, task.OperatorUserID, "membership-retry", "retry", "/test"))
	require.NoError(t, db.First(task, task.ID).Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, iCloudOnboardingStageFamilySharing, task.Stage)
	require.Equal(t, "waiting", task.DispatchStatus)
	require.Equal(t, "family-1", task.FamilyID)
	require.True(t, isICloudOnboardingFamilySharingWaitingTask(task))
	require.Len(t, session.requests, 5)
	for _, request := range session.requests[2:] {
		require.Equal(t, "GET "+iCloudFamilyMembersEndpoint, request, "membership retries must not accept the invitation again")
	}
	require.NoError(t, s.ConfirmICloudOnboardingFamilyReset(context.Background(), task.ID, task.OperatorUserID, "sharing-ready", "/test"))
	require.NoError(t, db.First(task, task.ID).Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "manage_profile", task.Stage)
	require.Len(t, session.requests, 5, "reuse the account login after sharing confirmation")
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "forwarding_prepare", task.Stage)
	require.Len(t, session.requests, 8, "new administrator imports must not repeat the membership check in phase 3")
}

func TestPhaseThreeFamilyCheckDependsOnImportKind(t *testing.T) {
	for _, test := range []struct {
		name, kind, role, familyID string
		wantCheck                  bool
	}{
		{name: "ordinary import", kind: iCloudUserImportTaskKind, role: "primary", wantCheck: true},
		{name: "ordinary reimport", kind: iCloudUserImportTaskKind, role: "primary", familyID: "previous-family", wantCheck: true},
		{name: "verified administrator child", kind: "onboarding", role: "child", familyID: "family-1"},
		{name: "legacy administrator child", kind: "onboarding", role: "child", wantCheck: true},
		{name: "administrator primary", kind: "onboarding", role: "primary"},
		{name: "cookie refresh", kind: "refresh", role: "child"},
		{name: "cookie recovery", kind: iCloudCookieRecoveryTaskKind, role: "child"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			provider := &onboardingRequestApple{}
			s.onboardingApple = provider
			require.NoError(t, db.Model(task).Updates(map[string]any{
				"task_kind": test.kind, "account_role": test.role, "family_id": test.familyID,
				"stage": "manage_profile", "device_code_api": "https://device.example/code",
				"dispatch_status": "running", "claim_token": "membership-test",
			}).Error)
			require.NoError(t, db.First(task, task.ID).Error)
			require.NoError(t, s.fetchICloudOnboardingManage(context.Background(), task, iCloudOnboardingSecret{Password: "test-password"}))
			require.Equal(t, test.wantCheck, provider.request.RequireFamilyMembership)
			require.Equal(t, test.kind == "onboarding" && test.role == "child" && test.familyID == "", provider.request.RequireFamilyChild)
			require.Empty(t, provider.request.FamilyInviteURL)
		})
	}
}

func TestAppleFamilyMembershipOrganizerRules(t *testing.T) {
	for _, test := range []struct {
		name, operation        string
		requireChild, rejected bool
	}{
		{name: "ordinary organizer", operation: appleOnboardingFetchManage},
		{name: "legacy administrator child", operation: appleOnboardingFetchManage, requireChild: true, rejected: true},
		{name: "administrator phase two", operation: appleOnboardingVerifyFamilyMembership, rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &appleOnboardingScriptedSession{}
			if test.operation == appleOnboardingFetchManage {
				session.responses = append(session.responses,
					appleOnboardingScriptedResponse{status: http.StatusOK, body: `{}`},
					appleOnboardingScriptedResponse{status: http.StatusOK, body: `{"apiKey":"api","countryCode":"US"}`})
			}
			session.responses = append(session.responses, appleOnboardingScriptedResponse{status: http.StatusOK, body: testICloudPrimaryFamilyResponse})
			response, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{
				Operation: test.operation, Email: "primary@example.com", RequireFamilyMembership: true, RequireFamilyChild: test.requireChild,
				SkipPrivateAlias: true, Session: appleOnboardingTestState(t, nil),
			})
			if test.rejected {
				var providerErr *AppleOnboardingError
				require.ErrorAs(t, err, &providerErr)
				require.Equal(t, "family_membership_unconfirmed", providerErr.Category)
			} else {
				require.NoError(t, err)
				require.Equal(t, "family-1", response.FamilyID)
			}
		})
	}
}

func TestFamilyMembershipExpiredSessionOnlyRestoresAccountLogin(t *testing.T) {
	s, db, task, _ := newOnboardingStateTest(t)
	state := appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
		state.Mode, state.AccountWidgetKey, state.InviteToken = "family", "account-widget", "expired-invite"
		state.Cookies = []msacl.SessionCookie{{Name: "myacinfo", Value: "expired-cookie", Domain: ".apple.com"}}
	})
	session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
		{status: http.StatusUnauthorized, body: `{}`},
		{status: http.StatusOK, body: `{"serviceKey":"portal-key"}`},
		{status: http.StatusOK, body: `<html></html>`, header: http.Header{"X-Apple-Id-Session-Id": {"session"}, "Scnt": {"signin-scnt"}}},
		{status: http.StatusOK, body: `{}`},
		{status: http.StatusOK, body: `{"b":"Ag==","c":"challenge","salt":"c2FsdA==","iteration":1,"protocol":"s2k"}`},
		{status: http.StatusConflict, body: `{"authType":"hsa2"}`},
		{status: http.StatusOK, body: `<html></html>`},
		{status: http.StatusNoContent},
		{status: http.StatusOK, body: testICloudFamilyResponse},
	}}
	s.onboardingApple = appleOnboardingTestClient(s.now(), session)
	require.NoError(t, db.Model(task).Updates(map[string]any{
		"stage": "family_membership_verify", "family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=expired",
		"family_reservation_confirmed": true, "device_code_api": "https://device.example/code", "device_bind_status": "success",
		"session_payload": iCloudJSON(state),
	}).Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "family_membership_prepare", task.Stage)
	require.Equal(t, 2, iCloudOnboardingTaskPhase(task))
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "sms_send", task.Stage)
	require.Equal(t, appleSMSFamilyMembershipLogin, task.PendingSMSPurpose)
	require.Equal(t, 2, iCloudOnboardingTaskPhase(task))
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "sms_wait", task.Stage)
	// Supply the result of the public device code poll without contacting a vendor.
	require.NoError(t, db.Model(task).Updates(map[string]any{"stage": "sms_verify", "manual_verification_code": "123456"}).Error)
	session.cookies = []msacl.SessionCookie{{Name: "myacinfo", Value: "fresh-cookie", Domain: ".apple.com"}}
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "family_membership_verify", task.Stage)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, iCloudOnboardingStageFamilySharing, task.Stage)
	require.Equal(t, "family-1", task.FamilyID)
	require.Empty(t, task.LastErrorCategory)
	require.Len(t, session.requests, 9)
	require.Contains(t, session.requestHeaders[8]["Cookie"], "myacinfo=fresh-cookie")
	for _, request := range session.requests {
		require.NotContains(t, request, "/family/invite")
		require.NotContains(t, request, "/family/messages")
	}
	require.NoError(t, s.ConfirmICloudOnboardingFamilyReset(context.Background(), task.ID, task.OperatorUserID, "sharing-ready", "/test"))
	require.NoError(t, db.First(task, task.ID).Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "manage_profile", task.Stage)
	require.Len(t, session.requests, 9, "phase three reuses the recovered Account Cookie")
}

func TestFamilyMembershipRetrySuccessClearsOldError(t *testing.T) {
	s, db, task, _ := newOnboardingStateTest(t)
	session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
		{status: http.StatusOK, body: `{"isLinkedToFamily":false,"isMemberOfFamily":false}`},
		{status: http.StatusOK, body: testICloudFamilyResponse},
	}}
	s.onboardingApple = appleOnboardingTestClient(s.now(), session)
	require.NoError(t, db.Model(task).Updates(map[string]any{
		"stage": "family_membership_verify", "family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=test",
		"family_reservation_confirmed": true, "device_code_api": "https://device.example/code", "device_bind_status": "success",
		"session_payload": iCloudJSON(appleOnboardingTestState(t, nil)),
	}).Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "family_membership_unconfirmed", task.LastErrorCategory)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, iCloudOnboardingStageFamilySharing, task.Stage)
	require.Empty(t, task.LastErrorCategory)
}

func TestPostFamilyLoginFailureRetainsRetry(t *testing.T) {
	for _, purpose := range []string{appleSMSFamilyLogin, appleSMSFamilyMembershipLogin} {
		t.Run(purpose, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			s.onboardingApple = appleOnboardingTestClient(s.now(), &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
				{status: http.StatusServiceUnavailable, body: `{}`},
			}})
			require.NoError(t, db.Model(task).Updates(map[string]any{
				"stage": "sms_verify", "pending_sms_purpose": purpose, "manual_verification_code": "123456",
				"family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=test", "family_reservation_confirmed": true,
				"device_code_api": "https://device.example/code", "device_bind_status": "success", "max_attempts": 1,
				"session_payload": iCloudJSON(appleOnboardingTestState(t, nil)),
			}).Error)
			processOnboardingStageForTest(t, s, db, task)
			require.Equal(t, iCloudOnboardingFailed, task.Status)
			require.Equal(t, "sms_verify_recover", task.Stage)
			require.NotEmpty(t, task.SecretPayload)
			require.True(t, isICloudPostFamilyRecoveryAvailable(*task))
			require.NoError(t, s.RetryICloudOnboardingPostFamily(context.Background(), task.ID, task.OperatorUserID, "membership-login-retry", "retry", "/test"))
			require.NoError(t, db.First(task, task.ID).Error)
			require.Equal(t, appleOnboardingRestartStage(purpose), task.Stage)
			require.Equal(t, 2, iCloudOnboardingTaskPhase(task))
			require.Empty(t, task.PendingSMSPurpose)
		})
	}
}

func TestFamilyMembershipInterruptedCodeVerificationRestartsAccountLogin(t *testing.T) {
	s, db, task, apple := newOnboardingStateTest(t)
	require.NoError(t, db.Model(task).Updates(map[string]any{
		"stage": "sms_verify_recover", "pending_sms_purpose": appleSMSFamilyMembershipLogin,
		"family_invite_url": "https://setup.icloud.com/family/messages?inviteCode=test", "family_reservation_confirmed": true,
		"device_code_api": "https://device.example/code", "device_bind_status": "success",
	}).Error)
	processOnboardingStageForTest(t, s, db, task)
	require.Equal(t, "family_membership_prepare", task.Stage)
	require.Empty(t, task.PendingSMSPurpose)
	require.Empty(t, apple.operations, "an uncertain verification result must not replay the code or invitation")
}
