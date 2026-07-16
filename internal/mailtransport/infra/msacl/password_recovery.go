package msacl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	passwordRecoveryEntryURL         = "https://account.live.com/password/reset"
	passwordRecoverySendOTTURL       = "https://account.live.com/api/Proofs/SendOtt"
	passwordRecoveryVerifyCodeURL    = "https://account.live.com/API/Proofs/VerifyCode"
	passwordRecoveryResetPasswordURL = "https://account.live.com/API/Recovery/ResetPassword"

	passwordRecoveryDefaultScenarioID = 100103
	passwordRecoveryDefaultUIFlavor   = 1001
	passwordRecoveryDefaultPageID     = 200284
	passwordRecoveryDefaultCodeWait   = 90 * time.Second
)

// PasswordRecoveryProofInfo is the non-secret part of a proof returned by the
// Microsoft password-reset proof picker. The encrypted proof id (epid),
// recovery token, and canaries intentionally never leave this package.
type PasswordRecoveryProofInfo struct {
	MaskedAddress   string
	Type            string
	Channel         string
	Used            bool
	RequiresReentry bool
}

// PasswordRecoveryProbeResult is safe for an operator-facing command. It
// contains the masked Microsoft proof and, when local evidence uniquely maps
// that proof to a mailbox, a recovered binding candidate. A caller must still
// confirm control of that exact proof through an OTP flow before treating it as
// verified. It contains no Microsoft session secrets.
type PasswordRecoveryProbeResult struct {
	Proofs               []PasswordRecoveryProofInfo
	MaskedBindingAddress string
	BindingAddress       string
	BindingResolved      bool
	BindingAmbiguous     bool
}

// PasswordRecoveryConfirmationOptions identifies either the exact locally
// recovered mailbox or Microsoft's masked proof that may receive one
// verification code. This flow never calls the password-reset endpoint and
// never changes account credentials.
type PasswordRecoveryConfirmationOptions struct {
	PreferredBindingAddress string
	ExpectedBindingAddress  string
	CodeTimeout             time.Duration
}

// PasswordRecoveryConfirmationResult contains no OTP, authorization token,
// recovery token, canary, password, or other Microsoft session secret.
type PasswordRecoveryConfirmationResult struct {
	Probe            PasswordRecoveryProbeResult
	BindingConfirmed bool
}

// PasswordRecoveryResetOptions keeps the destructive part of this flow
// explicitly gated. Its zero value cannot send an OTP or change a password.
// Callers should add their own operator/apply feature gates as well.
type PasswordRecoveryResetOptions struct {
	EnablePasswordReset       bool
	PreferredBindingAddress   string
	ExpectedBindingAddress    string
	CodeTimeout               time.Duration
	ExpirePasswordEvery72Days bool
}

// PasswordRecoveryResetResult deliberately omits the new password and all
// Microsoft proof/session tokens.
type PasswordRecoveryResetResult struct {
	Probe         PasswordRecoveryProbeResult
	PasswordReset bool
}

type passwordRecoveryProof struct {
	Name            string
	Type            string
	Channel         string
	EPID            string
	Used            bool
	RequiresReentry int
}

type passwordRecoveryFlow struct {
	accountEmail  string
	pageURL       string
	apiCanary     string
	recoveryToken string
	uaid          string
	scenarioID    int
	uiFlavor      int
	hostPageID    int
	proofs        []passwordRecoveryProof
}

type passwordRecoveryInitialPage struct {
	postURL   string
	returnURL string
	canary    string
	action    string
	pageURL   string
}

// ProbePasswordRecovery stops at Microsoft's proof picker. It does not call
// SendOtt, read an OTP, verify a code, or change the Microsoft account.
func ProbePasswordRecovery(ctx context.Context, email, proxy, preferredBindingAddress string) (PasswordRecoveryProbeResult, error) {
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return PasswordRecoveryProbeResult{}, wrapAuthError("创建 Microsoft 密码恢复会话失败", AuthStatusRequestError, err)
	}
	_, result, err := probePasswordRecoveryWithSession(session, email, proxy, preferredBindingAddress)
	return result, err
}

// ConfirmPasswordRecoveryBinding discovers the official recovery proof again,
// sends one OTP to the uniquely resolved expected mailbox, and verifies the
// code. It stops immediately after verification and cannot reset the password.
func ConfirmPasswordRecoveryBinding(ctx context.Context, email, proxy string, options PasswordRecoveryConfirmationOptions) (PasswordRecoveryConfirmationResult, error) {
	if normalizeExpectedRecoveryBinding(options.ExpectedBindingAddress) == "" {
		return PasswordRecoveryConfirmationResult{}, newAuthError("Microsoft recovery confirmation requires an expected recovery mailbox.", AuthStatusRequestError)
	}
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return PasswordRecoveryConfirmationResult{}, wrapAuthError("创建 Microsoft 密码恢复确认会话失败", AuthStatusRequestError, err)
	}
	return confirmPasswordRecoveryBindingWithSession(session, email, proxy, options)
}

func confirmPasswordRecoveryBindingWithSession(session *Session, email, proxy string, options PasswordRecoveryConfirmationOptions) (PasswordRecoveryConfirmationResult, error) {
	expectedBinding := normalizeExpectedRecoveryBinding(options.ExpectedBindingAddress)
	if expectedBinding == "" {
		return PasswordRecoveryConfirmationResult{}, newAuthError("Microsoft recovery confirmation requires an expected recovery mailbox.", AuthStatusRequestError)
	}
	verified, err := verifyPasswordRecoveryBindingWithSession(
		session,
		email,
		proxy,
		options.PreferredBindingAddress,
		expectedBinding,
		options.CodeTimeout,
	)
	result := PasswordRecoveryConfirmationResult{Probe: verified.probe}
	if err != nil {
		return result, err
	}
	result.BindingConfirmed = true
	return result, nil
}

// ResetPasswordViaRecovery performs the same proof-picker discovery, sends a
// single OTP to the uniquely recovered email proof, verifies it, and changes
// the Microsoft password. The operation is impossible with the zero-value
// options: EnablePasswordReset must be explicitly true.
func ResetPasswordViaRecovery(ctx context.Context, email, newPassword, proxy string, options PasswordRecoveryResetOptions) (PasswordRecoveryResetResult, error) {
	if !options.EnablePasswordReset {
		return PasswordRecoveryResetResult{}, newAuthError("Microsoft password reset is disabled.", AuthStatusRequestError)
	}
	if normalizeRecoveryMailbox(options.ExpectedBindingAddress) == "" {
		return PasswordRecoveryResetResult{}, newAuthError("Microsoft password reset requires an expected recovery mailbox.", AuthStatusRequestError)
	}
	if err := validateRecoveryPassword(newPassword); err != nil {
		return PasswordRecoveryResetResult{}, err
	}

	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return PasswordRecoveryResetResult{}, wrapAuthError("创建 Microsoft 密码恢复会话失败", AuthStatusRequestError, err)
	}
	return resetPasswordViaRecoveryWithSession(session, email, newPassword, proxy, options)
}

func probePasswordRecoveryWithSession(session *Session, email, proxy, preferredBindingAddress string) (*passwordRecoveryFlow, PasswordRecoveryProbeResult, error) {
	email = strings.TrimSpace(email)
	if session == nil {
		return nil, PasswordRecoveryProbeResult{}, newAuthError("Microsoft 密码恢复会话为空", AuthStatusRequestError)
	}
	if email == "" {
		return nil, PasswordRecoveryProbeResult{}, newAuthError("Microsoft account is required.", AuthStatusUnknownMailbox)
	}

	resp, err := session.Get(passwordRecoveryEntryURL, requestOptions{
		Headers:           navHeaders(session, nil),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return nil, PasswordRecoveryProbeResult{}, wrapAuthError("加载 Microsoft 密码恢复页失败", AuthStatusRequestError, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, PasswordRecoveryProbeResult{}, passwordRecoveryHTTPError("加载 Microsoft 密码恢复页", resp.StatusCode)
	}

	initialData, err := extractPasswordRecoveryServerData(resp.Body)
	if err != nil {
		return nil, PasswordRecoveryProbeResult{}, newAuthError("Microsoft 密码恢复页缺少有效配置", AuthStatusAuthTimeout)
	}
	initial, err := parsePasswordRecoveryInitialPage(initialData, resp.URL)
	if err != nil {
		return nil, PasswordRecoveryProbeResult{}, err
	}

	resp, err = session.Post(initial.postURL, requestOptions{
		Data: map[string]string{
			"iAction":           initial.action,
			"iRU":               initial.returnURL,
			"isSigninNamePhone": "false",
			"canary":            initial.canary,
			"iSigninName":       email,
		},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://account.live.com",
			"Referer":      initial.pageURL,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		// Do not automatically replay this form after a lost response. Restarting
		// the whole probe with a fresh session is safer than duplicating a
		// one-time recovery transition in the same session.
		return nil, PasswordRecoveryProbeResult{}, wrapAuthError("提交 Microsoft 密码恢复账号失败", AuthStatusRequestError, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, PasswordRecoveryProbeResult{}, passwordRecoveryHTTPError("提交 Microsoft 密码恢复账号", resp.StatusCode)
	}

	pickerData, err := extractPasswordRecoveryServerData(resp.Body)
	if err != nil {
		return nil, PasswordRecoveryProbeResult{}, newAuthError("Microsoft 密码恢复 proof picker 缺少有效配置", AuthStatusAuthTimeout)
	}
	flow, err := parsePasswordRecoveryPicker(pickerData, resp.URL, email)
	if err != nil {
		return nil, PasswordRecoveryProbeResult{}, err
	}
	result := buildPasswordRecoveryProbeResult(session.context(), flow.proofs, email, proxy, preferredBindingAddress)
	return flow, result, nil
}

func resetPasswordViaRecoveryWithSession(session *Session, email, newPassword, proxy string, options PasswordRecoveryResetOptions) (PasswordRecoveryResetResult, error) {
	if !options.EnablePasswordReset {
		return PasswordRecoveryResetResult{}, newAuthError("Microsoft password reset is disabled.", AuthStatusRequestError)
	}
	expectedBinding := normalizeRecoveryMailbox(options.ExpectedBindingAddress)
	if expectedBinding == "" {
		return PasswordRecoveryResetResult{}, newAuthError("Microsoft password reset requires an expected recovery mailbox.", AuthStatusRequestError)
	}
	if err := validateRecoveryPassword(newPassword); err != nil {
		return PasswordRecoveryResetResult{}, err
	}

	verified, err := verifyPasswordRecoveryBindingWithSession(
		session,
		email,
		proxy,
		options.PreferredBindingAddress,
		expectedBinding,
		options.CodeTimeout,
	)
	result := PasswordRecoveryResetResult{Probe: verified.probe}
	if err != nil {
		return result, err
	}
	if err := requireAuthorizedPasswordRecoveryToken(verified.token); err != nil {
		return result, err
	}
	if err := submitPasswordRecoveryReset(session, verified.flow, verified.proof, verified.token, newPassword, options.ExpirePasswordEvery72Days); err != nil {
		return result, err
	}
	result.PasswordReset = true
	return result, nil
}

type verifiedPasswordRecoveryBinding struct {
	flow  *passwordRecoveryFlow
	proof passwordRecoveryProof
	probe PasswordRecoveryProbeResult
	token string
}

func verifyPasswordRecoveryBindingWithSession(
	session *Session,
	email string,
	proxy string,
	preferredBindingAddress string,
	expectedBinding string,
	codeTimeout time.Duration,
) (verifiedPasswordRecoveryBinding, error) {
	flow, probe, err := probePasswordRecoveryWithSession(session, email, proxy, preferredBindingAddress)
	result := verifiedPasswordRecoveryBinding{flow: flow, probe: probe}
	if err != nil {
		return result, err
	}
	maskedProof := normalizeRecoveryMask(probe.MaskedBindingAddress)
	resolveByRecipient := !probe.BindingResolved || probe.BindingAddress == ""
	if expectedMask := normalizeRecoveryMask(expectedBinding); expectedMask != "" {
		maskedProof = expectedMask
		resolveByRecipient = true
	} else if expectedMailbox := normalizeRecoveryMailbox(expectedBinding); expectedMailbox != "" && resolveByRecipient {
		if maskedProof == "" || !mailboxMatchesMasked(maskedProof, expectedMailbox) {
			return result, newAuthError("Microsoft recovery mailbox changed before verification.", AuthStatusUnknownMailbox)
		}
		probe.BindingAddress = expectedMailbox
		probe.BindingResolved = true
		result.probe = probe
		resolveByRecipient = false
	}
	if resolveByRecipient && (maskedProof == "" || !UsesActiveAuxiliaryDomain(maskedProof)) {
		return result, newAuthError("Microsoft recovery mailbox could not be resolved uniquely.", AuthStatusUnknownMailbox)
	}
	if !resolveByRecipient {
		if normalizeRecoveryMailbox(probe.BindingAddress) != normalizeRecoveryMailbox(expectedBinding) {
			return result, newAuthError("Microsoft recovery mailbox changed before verification.", AuthStatusUnknownMailbox)
		}
		if eligibility := EvaluateActiveBindingRecoveryEligibility(session.context(), probe); !eligibility.Allowed {
			return result, newAuthError("Microsoft recovery mailbox is no longer eligible for verification.", AuthStatusUnknownMailbox)
		}
	}
	proofAddress := probe.BindingAddress
	if resolveByRecipient {
		proofAddress = maskedProof
	}
	proof, err := selectPasswordRecoveryEmailProof(flow.proofs, proofAddress)
	if err != nil {
		return result, err
	}
	result.proof = proof
	if flow.apiCanary == "" || flow.recoveryToken == "" || flow.uaid == "" {
		return result, newAuthError("Microsoft password recovery session is incomplete.", AuthStatusAuthTimeout)
	}
	lease, err := claimCodeMailLease(session.context(), proof.Name)
	if err != nil {
		return result, err
	}
	defer lease.releaseIfUnsent(session.context())

	var seenKeys map[string]struct{}
	if resolveByRecipient {
		seenKeys, err = snapshotMaskedMailboxKeys(session.context(), maskedProof, proxy)
	} else {
		seenKeys, err = snapshotMailboxKeys(session.context(), probe.BindingAddress, proxy)
	}
	if err != nil {
		return result, wrapAuthError("读取辅助邮箱基线失败", AuthStatusRequestError, err, proofAddress)
	}
	watchTimeout := codeTimeout
	if watchTimeout <= 0 {
		watchTimeout = passwordRecoveryDefaultCodeWait
	}
	watchSeconds := int(watchTimeout / time.Second)
	if watchSeconds <= 0 {
		watchSeconds = 1
	}
	var watcher *MailWatcher
	if !resolveByRecipient {
		watchCtx, cancelWatcher := context.WithCancel(session.context())
		defer cancelWatcher()
		watcher = startCodeWatcher(watchCtx, probe.BindingAddress, proxy, watchSeconds, seenKeys)
	}

	if err := lease.markSent(session.context()); err != nil {
		return result, err
	}
	if err := sendPasswordRecoveryOTT(session, flow, proof, proofAddress); err != nil {
		return result, err
	}
	var code string
	if resolveByRecipient {
		code, probe.BindingAddress, err = mailWaitMaskedCode(session.context(), maskedProof, proxy, watchSeconds, seenKeys)
		probe.BindingAddress = normalizeRecoveryMailbox(probe.BindingAddress)
		probe.BindingResolved = probe.BindingAddress != ""
		result.probe = probe
	} else {
		code, err = watcher.getCode(watchSeconds + normalizedMailPollInterval() + normalizedMailLateArrivalGrace() + 5)
	}
	if err != nil {
		return result, err
	}
	if resolveByRecipient {
		if eligibility := EvaluateActiveBindingRecoveryEligibility(session.context(), probe); !eligibility.Allowed {
			return result, newAuthError("Microsoft recovery mailbox is no longer eligible for verification.", AuthStatusUnknownMailbox)
		}
	}
	verification, err := verifyPasswordRecoveryCode(session, flow, proof, probe.BindingAddress, code)
	if err != nil {
		return result, err
	}
	if err := requireConfirmedPasswordRecoveryToken(verification.token); err != nil {
		return result, err
	}
	result.token = verification.token
	return result, nil
}

func parsePasswordRecoveryInitialPage(data map[string]any, pageURL string) (passwordRecoveryInitialPage, error) {
	postURL := resolveURL(pageURL, asString(data["urlPost"]))
	if !isAllowedPasswordRecoveryURL(postURL) {
		return passwordRecoveryInitialPage{}, newAuthError("Microsoft 密码恢复提交地址无效", AuthStatusAuthTimeout)
	}
	canary := asString(data["sCanary"])
	returnURL := asString(data["urlRU"])
	if canary == "" || returnURL == "" {
		return passwordRecoveryInitialPage{}, newAuthError("Microsoft 密码恢复页缺少 canary 或 return URL", AuthStatusAuthTimeout)
	}
	return passwordRecoveryInitialPage{
		postURL:   postURL,
		returnURL: returnURL,
		canary:    canary,
		action:    firstNonEmpty(asString(data["sResetPwdAction"]), "SignInName"),
		pageURL:   pageURL,
	}, nil
}

func parsePasswordRecoveryPicker(data map[string]any, pageURL, requestedEmail string) (*passwordRecoveryFlow, error) {
	pageID := asString(data["sPageId"])
	if pageID != "Account_ResetPwdPage_ProofPicker" {
		if pageID == "Account_ResetPwdSignInNamesPage" {
			return nil, newAuthError("Microsoft account was not accepted by password recovery.", AuthStatusUnknownMailbox)
		}
		return nil, newAuthError("Microsoft password recovery did not reach the proof picker.", AuthStatusAuthTimeout)
	}
	serverEmail := strings.TrimSpace(asString(data["sSigninName"]))
	if serverEmail != "" && !strings.EqualFold(serverEmail, strings.TrimSpace(requestedEmail)) {
		return nil, newAuthError("Microsoft password recovery returned a different account.", AuthStatusAuthTimeout)
	}

	rawProofs := asSlice(data["oProofList"])
	proofs := make([]passwordRecoveryProof, 0, len(rawProofs))
	for _, rawProof := range rawProofs {
		proofMap := asMap(rawProof)
		if proofMap == nil {
			continue
		}
		proof := passwordRecoveryProof{
			Name:            strings.TrimSpace(asString(proofMap["name"])),
			Type:            strings.TrimSpace(asString(proofMap["type"])),
			Channel:         strings.TrimSpace(asString(proofMap["channel"])),
			EPID:            asString(proofMap["epid"]),
			Used:            asInt(proofMap["used"]) != 0,
			RequiresReentry: asInt(proofMap["requiresReentry"]),
		}
		if proof.Name == "" || proof.Type == "" {
			continue
		}
		proofs = append(proofs, proof)
	}
	if len(proofs) == 0 {
		return nil, newAuthError("Microsoft password recovery returned no usable proof.", AuthStatusUnknownMailbox)
	}

	uaid := firstNonEmpty(asString(data["sUnauthSessionID"]), getQueryParam(asString(data["urlPost"]), "uaid"), getQueryParam(pageURL, "uaid"))
	return &passwordRecoveryFlow{
		accountEmail:  strings.TrimSpace(requestedEmail),
		pageURL:       pageURL,
		apiCanary:     asString(data["apiCanary"]),
		recoveryToken: asString(data["sRecoveryToken"]),
		uaid:          uaid,
		scenarioID:    asIntDefault(data["iScenarioId"], passwordRecoveryDefaultScenarioID),
		uiFlavor:      asIntDefault(data["iUiFlavor"], passwordRecoveryDefaultUIFlavor),
		hostPageID:    asIntDefault(data["hpgid"], passwordRecoveryDefaultPageID),
		proofs:        proofs,
	}, nil
}

func buildPasswordRecoveryProbeResult(ctx context.Context, proofs []passwordRecoveryProof, email, proxy, preferredBindingAddress string) PasswordRecoveryProbeResult {
	result := PasswordRecoveryProbeResult{Proofs: make([]PasswordRecoveryProofInfo, 0, len(proofs))}
	resolved := map[string]struct{}{}
	emailProofCount := 0
	for _, proof := range proofs {
		result.Proofs = append(result.Proofs, PasswordRecoveryProofInfo{
			MaskedAddress:   proof.Name,
			Type:            proof.Type,
			Channel:         proof.Channel,
			Used:            proof.Used,
			RequiresReentry: proof.RequiresReentry != 0,
		})
		if !isPasswordRecoveryEmailProof(proof) {
			continue
		}
		emailProofCount++
		if result.MaskedBindingAddress == "" {
			result.MaskedBindingAddress = proof.Name
		}
		mailbox := strings.ToLower(strings.TrimSpace(lookupRealMailbox(ctx, proof.Name, email, proxy, preferredBindingAddress)))
		if mailbox != "" && mailboxMatchesMasked(proof.Name, mailbox) {
			resolved[mailbox] = struct{}{}
		}
	}
	if emailProofCount != 1 {
		result.MaskedBindingAddress = ""
	}
	if emailProofCount == 1 && len(resolved) == 1 {
		for mailbox := range resolved {
			result.BindingAddress = mailbox
		}
		result.BindingResolved = true
	} else if emailProofCount > 1 || len(resolved) > 1 {
		result.BindingAmbiguous = true
	}
	return result
}

func selectPasswordRecoveryEmailProof(proofs []passwordRecoveryProof, bindingAddress string) (passwordRecoveryProof, error) {
	var matches []passwordRecoveryProof
	for _, proof := range proofs {
		if isPasswordRecoveryEmailProof(proof) && proof.EPID != "" &&
			(normalizeRecoveryMailbox(proof.Name) == normalizeRecoveryMailbox(bindingAddress) || mailboxMatchesMasked(proof.Name, bindingAddress)) {
			matches = append(matches, proof)
		}
	}
	if len(matches) != 1 {
		return passwordRecoveryProof{}, newAuthError("Microsoft recovery email proof is missing or ambiguous.", AuthStatusUnknownMailbox)
	}
	return matches[0], nil
}

func normalizeExpectedRecoveryBinding(value string) string {
	if mailbox := normalizeRecoveryMailbox(value); mailbox != "" {
		return mailbox
	}
	return normalizeRecoveryMask(value)
}

func isPasswordRecoveryEmailProof(proof passwordRecoveryProof) bool {
	return strings.EqualFold(proof.Type, "Email") && strings.EqualFold(firstNonEmpty(proof.Channel, "Email"), "Email") && strings.Contains(proof.Name, "@")
}

func sendPasswordRecoveryOTT(session *Session, flow *passwordRecoveryFlow, proof passwordRecoveryProof, bindingAddress string) error {
	payload := map[string]any{
		"associationType":      "Proof",
		"confirmProof":         bindingAddress,
		"epid":                 proof.EPID,
		"proofRequiredReentry": proof.RequiresReentry,
		"purpose":              "RecoverUser",
		"scid":                 flow.scenarioID,
		"token":                flow.recoveryToken,
		"uaid":                 flow.uaid,
		"uiflvr":               flow.uiFlavor,
	}
	_, err := postPasswordRecoveryAPI(session, flow, passwordRecoverySendOTTURL, payload, "Send recovery code")
	return err
}

type passwordRecoveryVerification struct {
	token string
}

func verifyPasswordRecoveryCode(session *Session, flow *passwordRecoveryFlow, proof passwordRecoveryProof, bindingAddress, code string) (passwordRecoveryVerification, error) {
	payload := map[string]any{
		"action":               "OTC",
		"confirmProof":         bindingAddress,
		"epid":                 proof.EPID,
		"proofRequiredReentry": proof.RequiresReentry,
		"purpose":              "RecoverUser",
		"scid":                 flow.scenarioID,
		"token":                flow.recoveryToken,
		"uaid":                 flow.uaid,
		"uiflvr":               flow.uiFlavor,
		"code":                 strings.TrimSpace(code),
	}
	data, err := postPasswordRecoveryAPI(session, flow, passwordRecoveryVerifyCodeURL, payload, "Verify recovery code")
	if err != nil {
		return passwordRecoveryVerification{}, err
	}
	token := asString(data["token"])
	if token == "" {
		return passwordRecoveryVerification{}, newAuthError("Microsoft recovery verification returned no authorization token.", AuthStatusVerifyCodeError)
	}
	return passwordRecoveryVerification{token: token}, nil
}

func passwordRecoveryTokenState(token string) (byte, error) {
	if len(token) < 3 || token[1] != ':' || strings.TrimSpace(token[2:]) == "" {
		return 0, newAuthError("Microsoft recovery returned an unknown authorization state.", AuthStatusAuthTimeout)
	}
	return token[0], nil
}

func requireConfirmedPasswordRecoveryToken(token string) error {
	state, err := passwordRecoveryTokenState(token)
	if err != nil {
		return err
	}
	switch state {
	case 'a', 'r':
		// Both states prove that Microsoft accepted the OTP for this mailbox.
		// The r: state only means a second proof would be required before a
		// destructive password reset.
		return nil
	default:
		return newAuthError("Microsoft recovery returned an unknown mailbox verification state.", AuthStatusAuthTimeout)
	}
}

func requireAuthorizedPasswordRecoveryToken(token string) error {
	state, err := passwordRecoveryTokenState(token)
	if err != nil {
		return err
	}
	switch state {
	case 'a':
		return nil
	case 'r':
		// The browser treats r: as a request for another proof. Do not guess a
		// second proof or proceed to ResetPassword with a partially authorized
		// token.
		return newAuthError("Microsoft password recovery requires a second proof.", AuthStatusMFARequired)
	default:
		return newAuthError("Microsoft password recovery is not authorized to change the password.", AuthStatusAccountAbnormal)
	}
}

func submitPasswordRecoveryReset(session *Session, flow *passwordRecoveryFlow, proof passwordRecoveryProof, authorizationToken, newPassword string, expirePassword bool) error {
	payload := map[string]any{
		"epid":          proof.EPID,
		"expiryEnabled": expirePassword,
		"scid":          flow.scenarioID,
		"signinName":    flow.accountEmail,
		"token":         authorizationToken,
		"uaid":          flow.uaid,
		"uiflvr":        flow.uiFlavor,
		"password":      newPassword,
	}
	_, err := postPasswordRecoveryAPI(session, flow, passwordRecoveryResetPasswordURL, payload, "Reset Microsoft password")
	return err
}

func postPasswordRecoveryAPI(session *Session, flow *passwordRecoveryFlow, rawURL string, payload map[string]any, label string) (map[string]any, error) {
	if session == nil || flow == nil {
		return nil, newAuthError(label+" session is incomplete.", AuthStatusRequestError)
	}
	if !isAllowedPasswordRecoveryAPIURL(rawURL) {
		return nil, newAuthError(label+" endpoint is invalid.", AuthStatusRequestError)
	}
	headers := corsHeaders(session, map[string]string{
		"Content-Type": "application/json; charset=utf-8",
		"Origin":       "https://account.live.com",
		"Referer":      flow.pageURL,
		"canary":       flow.apiCanary,
		"hpgid":        strconv.Itoa(flow.hostPageID),
		"hpgact":       "0",
	})
	resp, err := session.Post(rawURL, requestOptions{JSON: payload, Headers: headers})
	if err != nil {
		return nil, wrapAuthError(label+" request failed", AuthStatusRequestError, err)
	}
	var data map[string]any
	if err := resp.JSON(&data); err != nil {
		return nil, newAuthError(label+" returned an invalid response.", AuthStatusRequestError)
	}
	if nextCanary := asString(data["apiCanary"]); nextCanary != "" {
		flow.apiCanary = nextCanary
		delete(data, "apiCanary")
	}
	if rawError, exists := data["error"]; exists && rawError != nil {
		return nil, passwordRecoveryAPIError(label, resp.StatusCode, rawError)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, passwordRecoveryHTTPError(label, resp.StatusCode)
	}
	return data, nil
}

func passwordRecoveryAPIError(label string, statusCode int, rawError any) error {
	code := "unknown"
	if errorMap := asMap(rawError); errorMap != nil {
		code = safePasswordRecoveryErrorCode(asString(errorMap["code"]))
	} else if value := safePasswordRecoveryErrorCode(asString(rawError)); value != "" {
		code = value
	}
	status := AuthStatusRequestError
	switch code {
	case "1203", "1215", "1043":
		status = AuthStatusVerifyCodeError
	case "1204", "1221":
		status = AuthStatusRateLimited
	case "1339", "1340", "1060", "1346":
		status = AuthStatusAccountAbnormal
	}
	if statusCode == 429 {
		status = AuthStatusRateLimited
	}
	return newAuthError(fmt.Sprintf("%s failed (code=%s).", label, code), status)
}

func passwordRecoveryHTTPError(label string, statusCode int) error {
	status := AuthStatusRequestError
	if statusCode == 429 {
		status = AuthStatusRateLimited
	}
	return newAuthError(fmt.Sprintf("%s failed (HTTP %d).", label, statusCode), status)
}

func safePasswordRecoveryErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var out strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out.WriteRune(r)
		}
		if out.Len() >= 40 {
			break
		}
	}
	if out.Len() == 0 {
		return "unknown"
	}
	return out.String()
}

func validateRecoveryPassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < 8 || length > 113 {
		return newAuthError("New Microsoft password must be between 8 and 113 characters.", AuthStatusPasswordError)
	}
	return nil
}

func extractPasswordRecoveryServerData(page string) (map[string]any, error) {
	markers := []string{"var ServerData=", "window.ServerData=", "ServerData="}
	start := -1
	for _, marker := range markers {
		if index := strings.Index(page, marker); index >= 0 && (start < 0 || index < start) {
			start = index + len(marker)
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("ServerData marker not found")
	}
	relativeObjectStart := strings.IndexByte(page[start:], '{')
	if relativeObjectStart < 0 {
		return nil, fmt.Errorf("ServerData object not found")
	}
	objectStart := start + relativeObjectStart
	depth := 0
	inString := false
	escaped := false
	objectEnd := -1
	for index := objectStart; index < len(page); index++ {
		character := page[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				objectEnd = index + 1
				index = len(page)
			}
		}
	}
	if objectEnd <= objectStart {
		return nil, fmt.Errorf("ServerData object is incomplete")
	}
	decoder := json.NewDecoder(strings.NewReader(page[objectStart:objectEnd]))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func isAllowedPasswordRecoveryURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), "account.live.com") && strings.EqualFold(parsed.Path, "/password/reset")
}

func isAllowedPasswordRecoveryAPIURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "account.live.com") {
		return false
	}
	path := strings.ToLower(parsed.Path)
	return path == "/api/proofs/sendott" || path == "/api/proofs/verifycode" || path == "/api/recovery/resetpassword"
}
