package icloud

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

func (f *appleOnboardingFlow) privateEmailOperation(request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if f.state.Mode != "manage" || f.state.APIKey == "" {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	switch request.Operation {
	case AppleOnboardingGeneratePrivateEmail:
		email, err := f.generatePrivateEmail()
		return AppleOnboardingResponse{PrivateEmailAddress: email}, err
	case AppleOnboardingCompletePrivateEmail:
		err := f.completePrivateEmail(request.PrivateEmailAddress)
		return AppleOnboardingResponse{PrivateEmailAddress: request.PrivateEmailAddress}, err
	}
	data, err := f.privateEmailList()
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if _, complete := data["inactivePrivateEmailList"].([]any); !complete {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "alias_list_invalid", SafeMessage: "Apple omitted the inactive private email list; deletion cannot be confirmed."}
	}
	aliases, err := parseApplePrivateEmails(data)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if request.Operation == AppleOnboardingListPrivateEmails {
		response, err := f.exportAccountChannel()
		response.PrivateEmails = aliases
		return response, err
	}
	if !validApplePrivateEmailID(request.PrivateEmailID) || !validICloudHMEEmail(request.PrivateEmailAddress) {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_alias", SafeMessage: "Invalid private email identity."}
	}
	var target *ApplePrivateEmail
	for i := range aliases {
		alias := &aliases[i]
		if alias.ID == request.PrivateEmailID || strings.EqualFold(alias.EmailAddress, request.PrivateEmailAddress) {
			if alias.ID != request.PrivateEmailID || !strings.EqualFold(alias.EmailAddress, request.PrivateEmailAddress) {
				return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "alias_changed", SafeMessage: "Private email identity changed; no mutation was sent."}
			}
			target = alias
		}
	}
	if target == nil {
		return AppleOnboardingResponse{Next: "removed"}, nil
	}
	suffix := "/stop"
	if request.Operation == AppleOnboardingRemovePrivateEmail {
		if *target.Active {
			return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "alias_active", SafeMessage: "Stop the private email before removing it."}
		}
		suffix = "/remove"
	} else if !*target.Active {
		return AppleOnboardingResponse{Next: "stopped"}, nil
	}
	body, err := f.request(http.MethodDelete, strings.TrimRight(f.endpoints.AppleID, "/")+appleAccountPrivateEmailPath+"/"+target.ID+suffix,
		nil, false, true, false, false, true, "application/json, text/plain, */*")
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	if f.state.Status == http.StatusUnauthorized || f.state.Status == http.StatusForbidden {
		return AppleOnboardingResponse{}, appleOnboardingRestart("manage_prepare")
	}
	if f.state.Status != http.StatusOK {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "alias_delete_failed", SafeMessage: "Apple rejected private email deletion."}
	}
	var acknowledgement struct {
		Active *bool `json:"active"`
	}
	if json.Unmarshal(body, &acknowledgement) != nil || acknowledgement.Active == nil || *acknowledgement.Active {
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "alias_delete_unconfirmed", SafeMessage: "Apple did not confirm private email deletion; rerun to reconcile the remote list."}
	}
	return AppleOnboardingResponse{Next: strings.TrimPrefix(suffix, "/")}, nil
}

func parseApplePrivateEmails(data map[string]any) ([]ApplePrivateEmail, error) {
	var aliases []ApplePrivateEmail
	ids, emails := make(map[string]bool), make(map[string]bool)
	for _, key := range []string{"privateEmailList", "inactivePrivateEmailList"} {
		for _, raw := range appleOnboardingSlice(data[key]) {
			encoded, err := json.Marshal(raw)
			var alias ApplePrivateEmail
			if err != nil || json.Unmarshal(encoded, &alias) != nil {
				return nil, &AppleOnboardingError{Category: "alias_list_invalid", SafeMessage: "Apple returned an invalid private email list."}
			}
			alias.EmailAddress = strings.ToLower(strings.TrimSpace(alias.EmailAddress))
			if !validApplePrivateEmailID(alias.ID) || !validICloudHMEEmail(alias.EmailAddress) || ids[alias.ID] || emails[alias.EmailAddress] {
				return nil, &AppleOnboardingError{Category: "alias_list_invalid", SafeMessage: "Apple returned invalid or duplicate private email identities."}
			}
			ids[alias.ID], emails[alias.EmailAddress] = true, true
			if alias.Active == nil {
				active := key == "privateEmailList"
				alias.Active = &active
			} else if *alias.Active != (key == "privateEmailList") {
				return nil, &AppleOnboardingError{Category: "alias_list_invalid", SafeMessage: "Apple returned conflicting private email activity states."}
			}
			if alias.ForwardToEmail == "" {
				alias.ForwardToEmail = appleOnboardingString(data["forwardToEmailAddress"])
			}
			aliases = append(aliases, alias)
		}
	}
	return aliases, nil
}

func validApplePrivateEmailID(id string) bool {
	return validICloudRelayID(id, iCloudHMEAnonymousIDMaxLength) && url.PathEscape(id) == id && id != "." && id != ".."
}
