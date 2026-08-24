package icloud

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/donnel666/remail/internal/appleweb"
	"github.com/donnel666/remail/internal/platform"
)

func (f *appleOnboardingFlow) prepareICloud(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if err := validateAppleOnboardingCredentials(request); err != nil {
		return AppleOnboardingResponse{}, err
	}
	mode := "icloud"
	smsPurpose := appleSMSICloudLogin
	if request.Operation == appleOnboardingPrepareICloudCookie {
		mode = "icloud_cookie"
		smsPurpose = appleSMSICloudCookieLogin
	}
	if err := f.reset(mode); err != nil {
		return AppleOnboardingResponse{}, err
	}
	f.state.RedirectURI = f.endpoints.ICloud
	f.state.DomainID = "3"
	f.state.RememberMe = true
	f.state.RequireGrant = true
	f.state.OfferUpgrade = true
	if err := f.loadICloudWeb(); err != nil {
		return AppleOnboardingResponse{}, err
	}
	if err := f.authorize(); err != nil {
		return AppleOnboardingResponse{}, err
	}
	complete, err := f.signin(request.Email, request.Secret.Password)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	status := f.state.Status
	authType := strings.ToLower(appleOnboardingString(complete["authType"]))
	needRepair := false
	switch {
	case status == http.StatusConflict && authType == "hsa2":
		if err := f.prepareTrustedPhone(request.PhoneNumber); err != nil {
			return AppleOnboardingResponse{}, err
		}
		return AppleOnboardingResponse{Next: smsPurpose, TrustedPhoneLastTwo: f.state.PendingPhoneLastTwo}, nil
	case status == http.StatusConflict && authType == "sa":
		if err := f.submitIDMSAQuestions(request.Secret); err != nil {
			return AppleOnboardingResponse{}, err
		}
		needRepair = true
	case status == http.StatusPreconditionFailed:
		needRepair = true
	case status == http.StatusOK || status == http.StatusNoContent:
		return AppleOnboardingResponse{Next: "ready"}, nil
	default:
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "unsupported_challenge", SafeMessage: "Apple returned an unsupported iCloud sign-in challenge."}
	}
	if needRepair {
		if err := f.openRepair(); err != nil {
			return AppleOnboardingResponse{}, err
		}
		options, err := f.repairOptions()
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		if attribute := appleOnboardingString(options["repairAttribute"]); attribute != "complete" {
			if request.SkipPhoneEnrollment {
				return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "phone_binding_missing", SafeMessage: "Apple does not recognize the permanently bound phone for this account."}
			}
			if err := f.prepareEnrollment(request.Secret); err != nil {
				return AppleOnboardingResponse{}, err
			}
			return AppleOnboardingResponse{Next: appleSMSPhoneEnrollment}, nil
		}
		if err := f.completeRepair(); err != nil {
			return AppleOnboardingResponse{}, err
		}
	}
	return AppleOnboardingResponse{Next: "ready"}, nil
}

func (f *appleOnboardingFlow) sendSMS(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	switch request.SMSPurpose {
	case appleSMSICloudLogin, appleSMSOldCookieLogin, appleSMSICloudCookieLogin, appleSMSFamilyLogin, appleSMSFamilyReconcileLogin, appleSMSManageLogin:
		phoneID, err := appleOnboardingRawValue(f.state.PendingTrustedPhoneID)
		if err != nil {
			return AppleOnboardingResponse{}, appleOnboardingRestart(appleOnboardingRestartStage(request.SMSPurpose))
		}
		data, err := f.putObject(strings.TrimRight(f.state.ServiceURL, "/")+"/auth/verify/phone", map[string]any{
			"phoneNumber": map[string]any{"id": phoneID}, "mode": "sms",
		}, "verify/phone", false, true, false)
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
			return AppleOnboardingResponse{}, appleOnboardingRestart(appleOnboardingRestartStage(request.SMSPurpose))
		}
		if f.state.Status != http.StatusOK {
			return AppleOnboardingResponse{}, appleOnboardingSendRejected(data)
		}
	case appleSMSPhoneEnrollment:
		if strings.TrimSpace(request.PhoneNumber) == "" || strings.TrimSpace(request.PhoneCountryCode) == "" {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "phone_missing", SafeMessage: "Trusted phone enrollment requires the permanently bound phone number."}
		}
		data, err := f.putObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/security/upgrade/verify/phone", map[string]any{
			"phoneNumberVerification": map[string]any{
				"mode": "sms", "phoneNumber": map[string]any{
					"countryCode": strings.ToUpper(strings.TrimSpace(request.PhoneCountryCode)),
					"number":      appleOnboardingFormatPhone(request.PhoneCountryCode, request.PhoneNumber),
				},
			},
		}, "upgrade/verify/phone", false, false, false)
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
			return AppleOnboardingResponse{}, appleOnboardingRestart("icloud_prepare")
		}
		phone := appleOnboardingMap(appleOnboardingMap(data["phoneNumberVerification"])["phoneNumber"])
		if f.state.Status != http.StatusOK || len(phone) == 0 {
			return AppleOnboardingResponse{}, appleOnboardingSendRejected(data)
		}
		encoded, err := json.Marshal(phone)
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		f.state.PendingEnrollmentPhone = encoded
	default:
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_sms_state", SafeMessage: "Apple SMS verification state is invalid."}
	}
	return AppleOnboardingResponse{}, nil
}

func (f *appleOnboardingFlow) verifySMS(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	code := strings.TrimSpace(request.Code)
	if code == "" {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "verification_code_missing", SafeMessage: "Apple verification code is missing.", CodeRejected: true}
	}
	switch request.SMSPurpose {
	case appleSMSICloudLogin, appleSMSOldCookieLogin, appleSMSICloudCookieLogin, appleSMSFamilyLogin, appleSMSFamilyReconcileLogin, appleSMSManageLogin:
		phoneID, err := appleOnboardingRawValue(f.state.PendingTrustedPhoneID)
		if err != nil {
			return AppleOnboardingResponse{}, appleOnboardingRestart(appleOnboardingRestartStage(request.SMSPurpose))
		}
		body, err := f.request(http.MethodPost, strings.TrimRight(f.state.ServiceURL, "/")+"/auth/verify/phone/securitycode", map[string]any{
			"phoneNumber":  map[string]any{"id": phoneID, "nonFTEU": true},
			"securityCode": map[string]any{"code": code}, "mode": "sms",
		}, false, false, false, true, false, "application/json, text/javascript, */*; q=0.01")
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		data, err := decodeAppleOnboardingObject(body, "verify/phone/securitycode")
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
			return AppleOnboardingResponse{}, appleOnboardingRestart(appleOnboardingRestartStage(request.SMSPurpose))
		}
		valid, present := appleOnboardingOptionalBool(appleOnboardingMap(data["securityCode"])["valid"])
		if f.state.Status != http.StatusOK || present && !valid {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "verification_code_rejected", SafeMessage: "Apple rejected the verification code.", CodeRejected: true}
		}
		if err := f.finishHSA2Login(request.SMSPurpose); err != nil {
			return AppleOnboardingResponse{}, err
		}
		f.state.PendingTrustedPhoneID = nil
		f.state.PendingPhoneLastTwo = ""
	case appleSMSPhoneEnrollment:
		phone, err := appleOnboardingRawValue(f.state.PendingEnrollmentPhone)
		if err != nil {
			return AppleOnboardingResponse{}, appleOnboardingRestart("icloud_prepare")
		}
		data, err := f.postObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/security/upgrade", map[string]any{
			"phoneNumberVerification": map[string]any{
				"securityCode": map[string]any{"code": code}, "phoneNumber": phone, "mode": "sms",
			},
		}, "security/upgrade", false, false, false)
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
			return AppleOnboardingResponse{}, appleOnboardingRestart("icloud_prepare")
		}
		if f.state.Status == http.StatusBadRequest || f.state.Status == http.StatusConflict {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "verification_code_rejected", SafeMessage: "Apple rejected the verification code.", CodeRejected: true}
		}
		if f.state.Status != http.StatusOK {
			return AppleOnboardingResponse{}, appleOnboardingPermanent("phone_enrollment_failed", "Apple trusted phone enrollment failed.", data)
		}
		f.rememberCountry(data)
		options, err := f.repairOptions()
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		if attribute := appleOnboardingString(options["repairAttribute"]); attribute != "" && attribute != "complete" {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "repair_incomplete", SafeMessage: "Apple Account still requires unsupported repair: " + safeICloudImportMessage(attribute)}
		}
		if err := f.completeRepair(); err != nil {
			return AppleOnboardingResponse{}, err
		}
		f.state.PendingEnrollmentPhone = nil
	default:
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_sms_state", SafeMessage: "Apple SMS verification state is invalid."}
	}
	return AppleOnboardingResponse{}, nil
}

func (f *appleOnboardingFlow) finishHSA2Login(purpose string) error {
	hasCookie, err := f.hasCookie("myacinfo")
	if err != nil {
		return err
	}
	switch purpose {
	case appleSMSICloudLogin, appleSMSOldCookieLogin, appleSMSICloudCookieLogin:
		if f.state.RepairToken != "" {
			return f.completeRepair()
		}
		return nil
	case appleSMSFamilyLogin, appleSMSFamilyReconcileLogin:
		if !hasCookie {
			return appleOnboardingRestart(appleOnboardingRestartStage(purpose))
		}
	case appleSMSManageLogin:
		if f.state.RepairToken != "" && !hasCookie {
			if err := f.completeRepair(); err != nil {
				return err
			}
			hasCookie, err = f.hasCookie("myacinfo")
			if err != nil {
				return err
			}
		}
		if !hasCookie {
			return appleOnboardingRestart(appleOnboardingRestartStage(purpose))
		}
	}
	return nil
}

func (f *appleOnboardingFlow) finishICloud(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	expectedMode := "icloud"
	restartStage := "icloud_prepare"
	if request.Operation == appleOnboardingFinishICloudCookie {
		expectedMode = "icloud_cookie"
		restartStage = "icloud_cookie_prepare"
	}
	if f.state.Mode != expectedMode {
		return AppleOnboardingResponse{}, appleOnboardingRestart(restartStage)
	}
	if err := f.acceptICloudTerms(); err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.HasQualifyingDevice == nil {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_response", SafeMessage: "iCloud did not return the mobile iCloud activation state.", Retryable: true}
	}
	opened := *f.state.HasQualifyingDevice
	if opened && !request.SkipOldChannel {
		channel, err := f.oldChannel()
		if err != nil {
			var providerErr *AppleOnboardingError
			if errors.As(err, &providerErr) && providerErr.Category == "old_cookie_missing" {
				return AppleOnboardingResponse{}, appleOnboardingRestart(restartStage)
			}
			return AppleOnboardingResponse{}, err
		}
		f.state.OldChannel = channel
	} else {
		f.state.OldChannel = nil
	}
	return AppleOnboardingResponse{Next: "ready", CountryCode: f.state.AccountCountry, ICloudOpened: &opened, OldChannel: f.state.OldChannel}, nil
}

func (f *appleOnboardingFlow) prepareFamily(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if err := validateAppleOnboardingCredentials(request); err != nil {
		return AppleOnboardingResponse{}, err
	}
	token, err := appleOnboardingExtractInvite(request.FamilyInviteURL)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if err := f.reset("family"); err != nil {
		return AppleOnboardingResponse{}, err
	}
	// All onboarding now uses the supplied invitation. Keep the legacy
	// operation accepted for old callers, but deliberately use the same login
	// purpose and restart stage so it cannot re-enter primary-family recovery.
	smsPurpose := appleSMSFamilyLogin
	restartStage := "family_prepare"
	f.state.RedirectURI = f.endpoints.Account
	f.state.RememberMe = false
	f.state.InviteToken = token
	f.state.FamilyOrganizerEmail = strings.ToLower(strings.TrimSpace(request.FamilyOrganizerEmail))
	landing := strings.TrimRight(f.endpoints.Setup, "/") + "/family/messages?" + url.Values{
		"aaaction": {"showFamilyInvite"}, "inviteCode": {token}, "clientAppContext": {"Preferences"}, "actionUrlKey": {"acceptFamilyInvite.v2"},
	}.Encode()
	_, html, err := f.follow(landing, 8)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.Status == http.StatusNotFound || f.state.Status == http.StatusGone {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "family_invite_expired", SafeMessage: "The family invitation is expired or invalid."}
	}
	if f.state.Status < http.StatusOK || f.state.Status >= http.StatusMultipleChoices {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "family_invite_unavailable", SafeMessage: "The family invitation is unavailable.", HTTPStatus: f.state.Status}
	}
	if err := f.loadFamilyWidget(html); err != nil {
		return AppleOnboardingResponse{}, err
	}
	if err := f.authorize(); err != nil {
		return AppleOnboardingResponse{}, err
	}
	complete, err := f.signin(request.Email, request.Secret.Password)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	status := f.state.Status
	authType := strings.ToLower(appleOnboardingString(complete["authType"]))
	switch {
	case status == http.StatusConflict && authType == "hsa2":
		if err := f.prepareTrustedPhone(request.PhoneNumber); err != nil {
			return AppleOnboardingResponse{}, err
		}
		return AppleOnboardingResponse{Next: smsPurpose, TrustedPhoneLastTwo: f.state.PendingPhoneLastTwo}, nil
	case status == http.StatusConflict && authType == "sa":
		if err := f.submitIDMSAQuestions(request.Secret); err != nil {
			return AppleOnboardingResponse{}, err
		}
	case status == http.StatusOK || status == http.StatusNoContent:
	default:
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "unsupported_challenge", SafeMessage: "Apple returned an unsupported family sign-in challenge."}
	}
	hasCookie, err := f.hasCookie("myacinfo")
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if !hasCookie {
		return AppleOnboardingResponse{}, appleOnboardingRestart(restartStage)
	}
	return AppleOnboardingResponse{Next: "ready"}, nil
}

func (f *appleOnboardingFlow) joinFamily(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if f.state.Mode != "family" {
		return AppleOnboardingResponse{}, appleOnboardingRestart("family_prepare")
	}
	if organizer := strings.ToLower(strings.TrimSpace(request.FamilyOrganizerEmail)); organizer != "" {
		f.state.FamilyOrganizerEmail = organizer
	}
	hasCookie, err := f.hasCookie("myacinfo")
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if !hasCookie {
		return AppleOnboardingResponse{}, appleOnboardingRestart("family_prepare")
	}
	_, err = f.request(http.MethodGet, strings.TrimRight(f.endpoints.Account, "/")+"/family/invite/gs/ws/token", nil, false, false, false, false, true, "application/json, text/plain, */*")
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if (f.state.Status != http.StatusOK && f.state.Status != http.StatusNoContent) || f.state.GSToken == "" {
		if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
			return AppleOnboardingResponse{}, appleOnboardingRestart("family_prepare")
		}
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "family_token_missing", SafeMessage: "Apple did not return a family authorization token.", Retryable: true}
	}
	current, err := f.postObject(strings.TrimRight(f.endpoints.Account, "/")+"/family/invite/accept/familysharing?token="+url.QueryEscape(f.state.InviteToken), map[string]any{}, "accept/familysharing", false, false, true)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.Status == http.StatusNotFound || f.state.Status == http.StatusGone {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "family_invite_expired", SafeMessage: "The family invitation is expired or invalid."}
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return AppleOnboardingResponse{}, appleOnboardingRestart("family_prepare")
	}
	if appleOnboardingFamilyJoinApplied(f.state.Status) {
		return f.familyJoinResponse()
	}
	if f.state.Status != http.StatusOK {
		return AppleOnboardingResponse{}, appleOnboardingPermanent("family_join_failed", "Apple rejected the family invitation.", current)
	}
	seen := make(map[string]struct{})
	for rounds := 0; !appleOnboardingBool(current["final"]); rounds++ {
		if rounds >= 8 {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "family_confirmation_loop", SafeMessage: "Apple family confirmation did not finish.", Retryable: true}
		}
		serverContext := appleOnboardingString(current["serverContext"])
		if serverContext == "" {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple family confirmation returned invalid state.", Retryable: true}
		}
		if _, exists := seen[serverContext]; exists {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "family_confirmation_loop", SafeMessage: "Apple family confirmation repeated the same state.", Retryable: true}
		}
		seen[serverContext] = struct{}{}
		query := url.Values{"token": {f.state.InviteToken}, "accept": {"false"}, "serverContext": {serverContext}}
		current, err = f.putObject(strings.TrimRight(f.endpoints.Account, "/")+"/family/invite/update/familysharing/LOCATION?"+query.Encode(), nil, "update/familysharing", false, false, true)
		if err != nil {
			return AppleOnboardingResponse{}, err
		}
		if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
			return AppleOnboardingResponse{}, appleOnboardingRestart("family_prepare")
		}
		// Apple can apply the membership and still return a stale 400.
		if appleOnboardingFamilyJoinApplied(f.state.Status) {
			return f.familyJoinResponse()
		}
		if f.state.Status != http.StatusOK {
			return AppleOnboardingResponse{}, appleOnboardingPermanent("family_join_failed", "Apple family confirmation failed.", current)
		}
	}
	return f.familyJoinResponse()
}

func (f *appleOnboardingFlow) familyJoinResponse() (AppleOnboardingResponse, error) {
	// Accept/update completion is the family-join success signal. The familyws
	// session is not needed by onboarding and must not gate the next stage.
	return AppleOnboardingResponse{Next: "ready"}, nil
}

func (f *appleOnboardingFlow) prepareManage(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if err := validateAppleOnboardingCredentials(request); err != nil {
		return AppleOnboardingResponse{}, err
	}
	if err := f.reset("manage"); err != nil {
		return AppleOnboardingResponse{}, err
	}
	f.state.RedirectURI = f.endpoints.Account
	f.state.DomainID = "11"
	f.state.RememberMe = true
	f.state.AuthVersion = appleOnboardingManageAuth
	smsPurpose, restartStage := appleSMSManageLogin, "manage_prepare"
	portal, err := f.getObject(strings.TrimRight(f.endpoints.Account, "/")+"/bootstrap/portal", "bootstrap", false, false, false, "application/json, text/plain, */*")
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	f.state.WidgetKey = appleOnboardingString(portal["serviceKey"])
	if serviceURL := strings.TrimRight(appleOnboardingString(portal["serviceUrl"]), "/"); serviceURL != "" {
		f.state.ServiceURL = serviceURL
	}
	if f.state.WidgetKey == "" {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple Account bootstrap did not return a service key.", Retryable: true}
	}
	if err := f.authorize(); err != nil {
		return AppleOnboardingResponse{}, err
	}
	complete, err := f.signin(request.Email, request.Secret.Password)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	status := f.state.Status
	authType := strings.ToLower(appleOnboardingString(complete["authType"]))
	switch {
	case status == http.StatusConflict && authType == "hsa2":
		if err := f.prepareTrustedPhone(request.PhoneNumber); err != nil {
			return AppleOnboardingResponse{}, err
		}
		return AppleOnboardingResponse{Next: smsPurpose, TrustedPhoneLastTwo: f.state.PendingPhoneLastTwo}, nil
	case status == http.StatusConflict && authType == "sa":
		if err := f.submitIDMSAQuestions(request.Secret); err != nil {
			return AppleOnboardingResponse{}, err
		}
	case status == http.StatusOK || status == http.StatusNoContent:
	default:
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "unsupported_challenge", SafeMessage: "Apple returned an unsupported account-management sign-in challenge."}
	}
	if err := f.ensureManageLogin(restartStage); err != nil {
		return AppleOnboardingResponse{}, err
	}
	return AppleOnboardingResponse{Next: "ready"}, nil
}

func (f *appleOnboardingFlow) ensureManageLogin(restartStage string) error {
	hasCookie, err := f.hasCookie("myacinfo")
	if err != nil {
		return err
	}
	if f.state.RepairToken != "" && !hasCookie {
		if err := f.completeRepair(); err != nil {
			return err
		}
		hasCookie, err = f.hasCookie("myacinfo")
		if err != nil {
			return err
		}
	}
	if !hasCookie {
		return appleOnboardingRestart(restartStage)
	}
	return nil
}

func (f *appleOnboardingFlow) fetchManage(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if f.state.Mode != "manage" {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	delete(f.state.Scnt, appleOnboardingHost(f.endpoints.AppleID))
	_, err := f.request(http.MethodGet, strings.TrimRight(f.endpoints.AppleID, "/")+"/account/manage/gs/ws/token", nil, false, true, false, false, true, "application/json, text/plain, */*")
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	data, err := f.getObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/manage", "account/manage", false, true, true, "application/json, text/plain, */*")
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	if f.state.Status != http.StatusOK {
		return AppleOnboardingResponse{}, appleOnboardingPermanent("manage_profile_failed", "Apple Account profile could not be loaded.", data)
	}
	f.state.APIKey = appleOnboardingString(data["apiKey"])
	f.rememberCountry(data)
	if f.state.APIKey == "" {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "api_key_missing", SafeMessage: "Apple Account profile did not return an API key.", Retryable: true}
	}
	_, lastTwo, _ := selectAppleOnboardingTrustedPhone(appleOnboardingTrustedPhones(data), request.PhoneNumber)
	return AppleOnboardingResponse{Next: "ready", CountryCode: f.state.AccountCountry, TrustedPhoneLastTwo: lastTwo}, nil
}

func (f *appleOnboardingFlow) addForward(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	address := strings.ToLower(strings.TrimSpace(request.ForwardToEmail))
	if f.state.Mode != "manage" || f.state.APIKey == "" {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	if address == "" {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "forward_address_missing", SafeMessage: "Forwarding address is missing."}
	}
	alternate, err := f.loadAlternateEmail(address)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if alternate.Present {
		if alternate.Vetted {
			f.state.ForwardVerificationID = ""
			return AppleOnboardingResponse{Next: "verified"}, nil
		}
		if alternate.VerificationID != "" {
			f.state.ForwardVerificationID = alternate.VerificationID
		}
		if f.state.ForwardVerificationID == "" {
			retryAt := f.now().UTC().Add(iCloudOnboardingForwardRetry)
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "forward_status_incomplete", SafeMessage: "Apple forwarding verification is not ready yet.", Retryable: true, RetryAt: &retryAt}
		}
		return AppleOnboardingResponse{Next: "pending"}, nil
	}
	data, err := f.postObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/manage/email/alternate/add/verification", map[string]any{"address": address}, "add/verification", false, true, true)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	if f.state.Status != http.StatusOK && f.state.Status != http.StatusCreated {
		return AppleOnboardingResponse{}, appleOnboardingPermanent("forward_add_failed", "Apple rejected the forwarding address.", data)
	}
	f.state.ForwardVerificationID = appleOnboardingString(data["verificationId"])
	if f.state.ForwardVerificationID == "" {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple did not return a forwarding verification identifier.", Retryable: true}
	}
	return AppleOnboardingResponse{Next: "pending"}, nil
}

func (f *appleOnboardingFlow) verifyForward(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	address := strings.ToLower(strings.TrimSpace(request.ForwardToEmail))
	code := strings.TrimSpace(request.ForwardCode)
	if f.state.Mode != "manage" || f.state.APIKey == "" {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	if address == "" || code == "" {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "forward_verification_missing", SafeMessage: "Forwarding verification state is incomplete."}
	}
	// Apple may not expose a newly-added pending address in account/manage
	// immediately. The add response already gave us the verification ID, so
	// keep the script-compatible path and submit with that ID when present.
	alternate, err := f.loadAlternateEmail(address)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if alternate.Present && alternate.Vetted {
		f.state.ForwardVerificationID = ""
		return AppleOnboardingResponse{Next: "ready"}, nil
	}
	if f.state.ForwardVerificationID == "" && alternate.VerificationID != "" {
		f.state.ForwardVerificationID = alternate.VerificationID
	}
	if f.state.ForwardVerificationID == "" {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "forward_verification_missing", SafeMessage: "Forwarding verification state is incomplete.", Retryable: true}
	}
	data, err := f.putObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/manage/email/alternate/verification", map[string]any{
		"address": address, "verificationInfo": map[string]any{"id": f.state.ForwardVerificationID, "answer": code},
	}, "alternate/verification", true, false, true)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	vetted, present := appleOnboardingOptionalBool(appleOnboardingMap(data["vettingStatus"])["vetted"])
	if f.state.Status != http.StatusOK || present && !vetted {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "forward_code_rejected", SafeMessage: "Apple rejected the forwarding verification code."}
	}
	if !present || !vetted {
		retryAt := f.now().UTC().Add(iCloudOnboardingForwardRetry)
		return AppleOnboardingResponse{}, &AppleOnboardingError{
			Category: "forward_code_unconfirmed", SafeMessage: "Apple did not confirm the forwarding address verification.",
			Retryable: true, RetryAt: &retryAt,
		}
	}
	f.state.ForwardVerificationID = ""
	return AppleOnboardingResponse{Next: "ready"}, nil
}

type appleOnboardingAlternateEmail struct {
	Present        bool
	Vetted         bool
	VerificationID string
}

func (f *appleOnboardingFlow) loadAlternateEmail(address string) (appleOnboardingAlternateEmail, error) {
	profile, err := f.loadAccountManage("manage_prepare")
	if err != nil {
		return appleOnboardingAlternateEmail{}, err
	}
	return appleOnboardingAlternateEmailStatus(profile, address), nil
}

func (f *appleOnboardingFlow) loadAccountManage(restartStage string) (map[string]any, error) {
	data, err := f.getObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/manage", "account/manage", false, true, true, "application/json, text/plain, */*")
	if err != nil {
		return nil, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return nil, appleOnboardingRestart(restartStage)
	}
	if f.state.Status != http.StatusOK {
		return nil, &AppleOnboardingError{Category: "manage_profile_unavailable", SafeMessage: "Apple Account profile could not be reconciled.", Retryable: true}
	}
	return data, nil
}

func appleOnboardingAlternateEmailStatus(data map[string]any, address string) appleOnboardingAlternateEmail {
	address = strings.ToLower(strings.TrimSpace(address))
	person := appleOnboardingMap(appleOnboardingMap(data["account"])["person"])
	options := appleOnboardingMap(person["reachableAtOptions"])
	for _, value := range appleOnboardingSlice(options["alternateEmailAddresses"]) {
		entry := appleOnboardingMap(value)
		if strings.ToLower(appleOnboardingString(entry["address"])) != address {
			continue
		}
		vetted, _ := appleOnboardingOptionalBool(entry["vetted"])
		if nested, present := appleOnboardingOptionalBool(appleOnboardingMap(entry["vettingStatus"])["vetted"]); present {
			vetted = nested
		}
		verificationID := firstNonEmpty(
			appleOnboardingString(entry["verificationId"]),
			appleOnboardingString(entry["id"]),
			appleOnboardingString(appleOnboardingMap(entry["verificationInfo"])["id"]),
		)
		return appleOnboardingAlternateEmail{Present: true, Vetted: vetted, VerificationID: verificationID}
	}
	return appleOnboardingAlternateEmail{}
}

func (f *appleOnboardingFlow) exportChannels(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if f.state.Mode != "manage" || f.state.APIKey == "" {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	if !request.SkipPrivateAlias {
		if err := f.ensurePrivateAlias(); err != nil {
			return AppleOnboardingResponse{}, err
		}
	}
	if address := strings.TrimSpace(request.ForwardToEmail); address != "" {
		if err := f.setDefaultForward(address); err != nil {
			return AppleOnboardingResponse{}, err
		}
	}
	data, err := f.getObject(strings.TrimRight(f.endpoints.AppleID, "/")+appleAccountPrivateEmailPath, "email/private", false, true, true, "application/json, text/plain, */*")
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	if f.state.Status != http.StatusOK {
		return AppleOnboardingResponse{}, appleOnboardingPermanent("new_cookie_unavailable", "Apple Account session could not be exported.", data)
	}
	cookies, err := f.http.SnapshotCookies(f.endpoints.Account, f.endpoints.IDMSA, f.endpoints.AppleID)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	cookie := appleOnboardingCookieString(cookies, "apple.com")
	if cookie == "" {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	browser := f.browserProfile()
	fdInfo, err := appleweb.FDClientInfoFor(browser.UserAgent, f.now())
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	expiresAt := f.now().UTC().Add(iCloudImportedAppleManageTTL)
	newChannel := &AppleOnboardingChannel{
		Kind: iCloudChannelAppleAccount, Host: appleOnboardingHost(f.endpoints.AppleID), Cookie: cookie,
		Origin: f.endpoints.Account, Referer: strings.TrimRight(f.endpoints.Account, "/") + "/",
		UserAgent: browser.UserAgent, FDClientInfo: fdInfo, Scnt: f.state.Scnt[appleOnboardingHost(f.endpoints.AppleID)],
		APIKey: f.state.APIKey, ManageExpiresAt: &expiresAt,
	}
	return AppleOnboardingResponse{Next: "ready", CountryCode: f.state.AccountCountry, OldChannel: f.state.OldChannel, NewChannel: newChannel}, nil
}

func (f *appleOnboardingFlow) ensurePrivateAlias() error {
	if f.state.PrivateAliasReady {
		return nil
	}
	data, err := f.privateEmailList()
	if err != nil {
		return err
	}
	if appleOnboardingPrivateEmailCount(data) > 0 {
		f.state.PrivateAliasReady = true
		return nil
	}
	if appleOnboardingBool(data["maxLimitReached"]) {
		return appleOnboardingPermanent("alias_limit_reached", "Apple Account cannot create a private email alias because its alias limit is reached.", data)
	}
	generated, err := f.postObject(strings.TrimRight(f.endpoints.AppleID, "/")+appleAccountPrivateEmailAddPath, map[string]any{}, "email/private/add", false, true, true)
	if err != nil {
		return err
	}
	if f.state.Status != http.StatusOK && f.state.Status != http.StatusCreated {
		return appleOnboardingPermanent("alias_create_failed", "Apple rejected private email alias creation.", generated)
	}
	email := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		appleOnboardingString(generated["emailAddress"]),
		appleOnboardingString(generated["address"]),
	)))
	if !validICloudHMEEmail(email) {
		return &AppleOnboardingError{Category: "alias_create_invalid", SafeMessage: "Apple returned an invalid private email alias candidate.", Retryable: true}
	}
	completed, err := f.putObject(strings.TrimRight(f.endpoints.AppleID, "/")+appleAccountPrivateEmailAddCompletePath, map[string]any{
		"emailAddress": email, "label": "ReMail", "note": "",
	}, "email/private/add/complete", true, false, true)
	if err != nil {
		return err
	}
	if f.state.Status != http.StatusOK && f.state.Status != http.StatusCreated && f.state.Status != http.StatusNoContent {
		return appleOnboardingPermanent("alias_create_failed", "Apple rejected private email alias completion.", completed)
	}
	completedEmail := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		appleOnboardingString(completed["emailAddress"]),
		appleOnboardingString(completed["address"]),
		email,
	)))
	if !validICloudHMEEmail(completedEmail) {
		return &AppleOnboardingError{Category: "alias_create_invalid", SafeMessage: "Apple did not confirm the created private email alias.", Retryable: true}
	}
	confirmed, err := f.privateEmailList()
	if err != nil {
		return err
	}
	if appleOnboardingPrivateEmailCount(confirmed) == 0 {
		retryAt := f.now().UTC().Add(iCloudOnboardingForwardRetry)
		return &AppleOnboardingError{Category: "alias_create_unconfirmed", SafeMessage: "Apple did not make the private email alias available yet.", Retryable: true, RetryAt: &retryAt}
	}
	f.state.PrivateAliasReady = true
	return nil
}

func (f *appleOnboardingFlow) privateEmailList() (map[string]any, error) {
	data, err := f.getObject(strings.TrimRight(f.endpoints.AppleID, "/")+appleAccountPrivateEmailPath, "email/private", false, true, true, "application/json, text/plain, */*")
	if err != nil {
		return nil, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return nil, appleOnboardingRestart("manage_prepare")
	}
	if f.state.Status != http.StatusOK {
		return nil, appleOnboardingPermanent("alias_list_failed", "Apple private email aliases could not be loaded.", data)
	}
	if _, ok := data["privateEmailList"]; !ok {
		return nil, &AppleOnboardingError{Category: "alias_list_invalid", SafeMessage: "Apple returned an incomplete private email alias list.", Retryable: true}
	}
	if _, ok := data["privateEmailList"].([]any); !ok {
		return nil, &AppleOnboardingError{Category: "alias_list_invalid", SafeMessage: "Apple returned an invalid private email alias list.", Retryable: true}
	}
	if inactive, exists := data["inactivePrivateEmailList"]; exists && inactive != nil {
		if _, ok := inactive.([]any); !ok {
			return nil, &AppleOnboardingError{Category: "alias_list_invalid", SafeMessage: "Apple returned an invalid inactive private email alias list.", Retryable: true}
		}
	}
	return data, nil
}

func appleOnboardingPrivateEmailCount(data map[string]any) int {
	count := len(appleOnboardingSlice(data["privateEmailList"]))
	return count + len(appleOnboardingSlice(data["inactivePrivateEmailList"]))
}

func (f *appleOnboardingFlow) setDefaultForward(address string) error {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return nil
	}
	data, err := f.putObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/manage/forwardemail", map[string]any{
		"forwardToEmail": address,
	}, "forwardemail", true, false, true)
	if err != nil {
		return err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return appleOnboardingRestart("manage_prepare")
	}
	if f.state.Status != http.StatusOK && f.state.Status != http.StatusNoContent {
		return appleOnboardingPermanent("forward_default_failed", "Apple rejected the default forwarding address.", data)
	}
	confirmed := appleOnboardingForwardAddress(data)
	if confirmed != address {
		retryAt := f.now().UTC().Add(iCloudOnboardingForwardRetry)
		return &AppleOnboardingError{
			Category: "forward_default_unconfirmed", SafeMessage: "Apple did not confirm the requested default forwarding address.", ProviderMessage: confirmed,
			Retryable: true, RetryAt: &retryAt,
		}
	}
	return nil
}

func appleOnboardingForwardAddress(data map[string]any) string {
	return appleOnboardingFindForwardAddress(data, 0)
}

func appleOnboardingFindForwardAddress(value any, depth int) string {
	if depth > 4 {
		return ""
	}
	if address := appleOnboardingString(value); address != "" && strings.Contains(address, "@") {
		return strings.ToLower(strings.TrimSpace(address))
	}
	data := appleOnboardingMap(value)
	if len(data) == 0 {
		return ""
	}
	for _, key := range []string{"forwardToEmail", "selectedForwardTo", "forwardToEmailAddress", "selectedForwardToEmail"} {
		candidate := data[key]
		if address := appleOnboardingString(candidate); address != "" && strings.Contains(address, "@") {
			return strings.ToLower(strings.TrimSpace(address))
		}
		if address := appleOnboardingString(appleOnboardingMap(candidate)["address"]); address != "" {
			return strings.ToLower(strings.TrimSpace(address))
		}
	}
	for _, key := range []string{"forwardToOptions", "result", "data", "payload", "forwarding", "defaultForward"} {
		if address := appleOnboardingFindForwardAddress(data[key], depth+1); address != "" {
			return address
		}
	}
	return ""
}

func (f *appleOnboardingFlow) loadICloudWeb() error {
	body, err := f.request(http.MethodGet, strings.TrimRight(f.endpoints.ICloud, "/")+"/", nil, true, false, false, false, false, "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if err != nil {
		return err
	}
	html := string(body)
	build := appleOnboardingHTMLAttr(html, "data-cw-private-build-number")
	mastering := appleOnboardingHTMLAttr(html, "data-cw-private-mastering-number")
	if build == "" {
		match := regexp.MustCompile(`/system/icloud\.com/([^/]+)/`).FindStringSubmatch(html)
		if len(match) == 2 {
			build = match[1]
		}
	}
	if build == "" {
		return &AppleOnboardingError{Category: "invalid_response", SafeMessage: "iCloud did not return a client build number.", Retryable: true}
	}
	lang := firstNonEmpty(appleOnboardingHTMLAttr(html, "lang"), "zh-cn")
	f.state.BuildNumber = build
	f.state.MasteringNumber = firstNonEmpty(mastering, build)
	f.state.PageLocale = strings.ToLower(lang)
	f.state.Locale = appleOnboardingLocale(lang)
	data, err := f.postObject(strings.TrimRight(f.endpoints.Setup, "/")+"/setup/ws/1/validate?"+f.icloudQuery(""), map[string]any{}, "validate", false, false, false)
	if err != nil {
		return err
	}
	urls := appleOnboardingMap(appleOnboardingMap(data["configBag"])["urls"])
	f.state.WidgetKey = firstNonEmpty(appleOnboardingWidgetFromURL(appleOnboardingString(urls["accountAuthorizeUI"])), appleOnboardingWidgetFromURL(appleOnboardingString(urls["accountLoginUI"])))
	if f.state.WidgetKey == "" {
		return &AppleOnboardingError{Category: "invalid_response", SafeMessage: "iCloud configuration did not return an Apple sign-in service key.", Retryable: true}
	}
	if value := appleOnboardingString(urls["accountLogin"]); value != "" {
		f.state.AccountLoginURL = value
	}
	if value := appleOnboardingString(urls["getICloudTerms"]); value != "" {
		f.state.GetTermsURL = value
	}
	if value := appleOnboardingString(urls["repairDone"]); value != "" {
		f.state.RepairDoneURL = value
	}
	return nil
}

func (f *appleOnboardingFlow) accountLogin(dsid string) (map[string]any, error) {
	restartStage := "icloud_prepare"
	if f.state.Mode == "icloud_cookie" {
		restartStage = "icloud_cookie_prepare"
	}
	if f.state.RepairToken == "" || f.state.AccountCountry == "" {
		return nil, appleOnboardingRestart(restartStage)
	}
	data, err := f.postObject(f.state.AccountLoginURL+"?"+f.icloudQuery(dsid), map[string]any{
		"dsWebAuthToken": f.state.RepairToken, "accountCountryCode": f.state.AccountCountry, "extended_login": true,
	}, "accountLogin", false, false, false)
	if err != nil {
		return nil, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return nil, appleOnboardingRestart(restartStage)
	}
	if f.state.Status != http.StatusOK {
		return nil, appleOnboardingPermanent("icloud_login_failed", "iCloud web sign-in failed.", data)
	}
	f.absorbICloudSession(data)
	return data, nil
}

func (f *appleOnboardingFlow) acceptICloudTerms() error {
	first, err := f.accountLogin("")
	if err != nil {
		return err
	}
	dsid := appleOnboardingString(appleOnboardingMap(first["dsInfo"])["dsid"])
	if appleOnboardingBool(first["termsUpdateNeeded"]) {
		terms, err := f.postObject(f.state.GetTermsURL+"?"+f.icloudQuery(dsid), map[string]any{"locale": f.state.PageLocale}, "getTerms", false, false, false)
		if err != nil {
			return err
		}
		version := appleOnboardingMap(terms["iCloudTerms"])["version"]
		if f.state.Status != http.StatusOK || appleOnboardingString(version) == "" {
			return &AppleOnboardingError{Category: "icloud_terms_failed", SafeMessage: "iCloud terms could not be loaded.", Retryable: true}
		}
		done, err := f.postObject(f.state.RepairDoneURL+"?"+f.icloudQuery(dsid), map[string]any{"acceptedICloudTerms": version}, "repairDone", false, false, false)
		if err != nil {
			return err
		}
		if f.state.Status != http.StatusOK || appleOnboardingExplicitFalse(done["success"]) {
			return appleOnboardingPermanent("icloud_terms_rejected", "iCloud terms acceptance failed.", done)
		}
		second, err := f.accountLogin(dsid)
		if err != nil {
			return err
		}
		if appleOnboardingBool(second["termsUpdateNeeded"]) {
			return &AppleOnboardingError{Category: "icloud_terms_incomplete", SafeMessage: "iCloud still requires terms acceptance.", Retryable: true}
		}
	}
	return nil
}

func (f *appleOnboardingFlow) absorbICloudSession(data map[string]any) {
	f.rememberCountry(data)
	if dsid := appleOnboardingString(appleOnboardingMap(data["dsInfo"])["dsid"]); dsid != "" {
		f.state.DSID = dsid
	}
	webservices := appleOnboardingMap(data["webservices"])
	if value := appleOnboardingString(appleOnboardingMap(webservices["premiummailsettings"])["url"]); value != "" {
		f.state.PremiumMailURL = value
	}
	if flag, ok := appleOnboardingOptionalBool(appleOnboardingMap(data["dsInfo"])["hasICloudQualifyingDevice"]); ok {
		f.state.HasQualifyingDevice = &flag
	} else if flag, ok := appleOnboardingOptionalBool(data["hasICloudQualifyingDevice"]); ok {
		f.state.HasQualifyingDevice = &flag
	}
}

func (f *appleOnboardingFlow) oldChannel() (*AppleOnboardingChannel, error) {
	host := appleOnboardingServiceBase(f.state.PremiumMailURL)
	if appleOnboardingHost(host) == "" || f.state.DSID == "" || f.state.SetupClientID == "" || f.state.BuildNumber == "" || f.state.MasteringNumber == "" {
		return nil, &AppleOnboardingError{Category: "old_cookie_missing", SafeMessage: "iCloud did not return the V2 mail session metadata."}
	}
	cookies, err := f.http.SnapshotCookies(f.endpoints.ICloud, f.endpoints.Setup, host)
	if err != nil {
		return nil, err
	}
	suffix := "icloud.com"
	if strings.HasSuffix(appleOnboardingHost(host), "icloud.com.cn") {
		suffix = "icloud.com.cn"
	}
	cookie := appleOnboardingCookieString(cookies, suffix)
	if cookie == "" {
		return nil, &AppleOnboardingError{Category: "old_cookie_missing", SafeMessage: "iCloud did not return a usable V2 session cookie."}
	}
	browser := f.browserProfile()
	fdInfo, err := appleweb.FDClientInfoFor(browser.UserAgent, f.now())
	if err != nil {
		return nil, err
	}
	return &AppleOnboardingChannel{
		Kind: iCloudChannelWeb, Host: appleOnboardingHost(host), Cookie: cookie, SetupCookie: cookie,
		Origin: f.endpoints.ICloud, Referer: strings.TrimRight(f.endpoints.ICloud, "/") + "/",
		UserAgent: browser.UserAgent, FDClientInfo: fdInfo, DSID: f.state.DSID, ClientID: f.state.SetupClientID,
		ClientBuildNumber: f.state.BuildNumber, ClientMasteringNumber: f.state.MasteringNumber,
	}, nil
}

func (f *appleOnboardingFlow) loadFamilyWidget(html string) error {
	boot := parseAppleOnboardingBootArgs(html)
	config := appleOnboardingMap(boot["authWidgetConfig"])
	f.state.WidgetKey = appleOnboardingString(config["serviceKey"])
	if f.state.WidgetKey == "" {
		return &AppleOnboardingError{Category: "family_invite_invalid", SafeMessage: "Family invitation did not return an Apple sign-in service key."}
	}
	if value := strings.TrimRight(appleOnboardingString(config["serviceURL"]), "/"); value != "" {
		f.state.ServiceURL = value
	}
	if value := appleOnboardingString(config["domainId"]); value != "" {
		f.state.DomainID = value
	}
	if value := appleOnboardingString(boot["isoLocale"]); value != "" {
		f.state.Locale = value
	}
	return nil
}

func (f *appleOnboardingFlow) follow(rawURL string, limit int) (string, string, error) {
	current := rawURL
	body := []byte(nil)
	for range limit {
		var err error
		body, err = f.request(http.MethodGet, current, nil, true, false, false, false, false, "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		if err != nil {
			return "", "", err
		}
		if f.state.Status >= 300 && f.state.Status < 400 && f.state.Location != "" {
			current = appleOnboardingAbsoluteURL(current, f.state.Location)
			continue
		}
		return current, string(body), nil
	}
	return current, string(body), &AppleOnboardingError{Category: "redirect_loop", SafeMessage: "Apple invitation redirect did not finish.", Retryable: true}
}

func (f *appleOnboardingFlow) icloudQuery(dsid string) string {
	query := url.Values{
		"requestId": {platform.NewUUIDV4String()}, "clientBuildNumber": {f.state.BuildNumber},
		"clientMasteringNumber": {f.state.MasteringNumber}, "clientId": {f.state.SetupClientID},
	}
	if dsid != "" {
		query.Set("dsid", dsid)
	}
	return query.Encode()
}

func validateAppleOnboardingCredentials(request AppleOnboardingRequest) error {
	if strings.TrimSpace(request.Email) == "" || strings.TrimSpace(request.Secret.Password) == "" {
		return &AppleOnboardingError{Category: "invalid_credentials", SafeMessage: "Stored Apple credentials are invalid."}
	}
	return nil
}

func appleOnboardingRestartStage(purpose string) string {
	switch purpose {
	case appleSMSICloudLogin, appleSMSPhoneEnrollment:
		return "icloud_prepare"
	case appleSMSOldCookieLogin:
		return "old_cookie_prepare"
	case appleSMSICloudCookieLogin:
		return "icloud_cookie_prepare"
	case appleSMSFamilyLogin:
		return "family_prepare"
	case appleSMSFamilyReconcileLogin:
		return "family_prepare"
	case appleSMSManageLogin:
		return "manage_prepare"
	default:
		return "icloud_prepare"
	}
}

func appleOnboardingSendRejected(data map[string]any) error {
	category := "sms_send_rejected"
	message := "Apple rejected the verification SMS request."
	providerMessage := safeICloudImportMessage(appleOnboardingServiceError(data))
	if appleOnboardingLooksLocked(data) {
		category = "account_locked"
		message = "The Apple Account is locked or disabled."
	}
	return &AppleOnboardingError{Category: category, SafeMessage: message, ProviderMessage: providerMessage, SendRejected: true}
}

func appleOnboardingOptionalBool(value any) (bool, bool) {
	result, ok := value.(bool)
	return result, ok
}

func appleOnboardingExplicitFalse(value any) bool {
	result, ok := appleOnboardingOptionalBool(value)
	return ok && !result
}

func appleOnboardingLocale(lang string) string {
	parts := strings.Split(strings.ReplaceAll(strings.TrimSpace(lang), "_", "-"), "-")
	if len(parts) >= 2 {
		return strings.ToLower(parts[0]) + "_" + strings.ToUpper(parts[1])
	}
	return "zh_CN"
}

func appleOnboardingWidgetFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return firstNonEmpty(parsed.Query().Get("client_id"), parsed.Query().Get("widgetKey"))
}
