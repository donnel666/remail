package msacl

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	microsoftConsentManageURL     = "https://account.live.com/consent/Manage"
	microsoftConsentLoginURL      = "https://login.live.com/login.srf?wa=wsignin1.0&wreply=https://account.live.com/consent/Manage&id=38936"
	microsoftProofsAddURL         = "https://account.live.com/proofs/Add"
	microsoftConsentLoginID       = "38936"
	microsoftConsentFPTCustomerID = "33e01921-4d64-4f8c-a055-5bdaffd5e33d"
)

var consentEditLinkPattern = regexp.MustCompile(`(?is)href\s*=\s*["']([^"']*consent/Edit\?[^"']+)["']`)
var consentPasskeyPagePattern = regexp.MustCompile(`(?is)"pgid"\s*:\s*"CreateFido"`)
var consentPrivacyRedirectPattern = regexp.MustCompile(`(?is)(?:var\s+redirectUrl|ucis\.RedirectUrl)\s*=\s*['"]((?:\\.|[^'"\\])*)['"]`)

// ConsentCleanupResult is a secret-free summary of one account-wide consent cleanup.
type ConsentCleanupResult struct {
	Before    int
	Removed   int
	Remaining int
}

// loginMicrosoftConsentManage establishes the account.live.com WS-Fed session
// required by consent/Manage. Device-code and AddAssocId cookies alone do not
// authenticate this portal endpoint.
func loginMicrosoftConsentManage(session *Session, email, password, proxy, preferredBindingAddress string) (string, string, error) {
	page, currentURL, _, err := loginMicrosoftConsentManageWithBinding(session, email, password, proxy, preferredBindingAddress)
	return page, currentURL, err
}

func loginMicrosoftConsentManageWithBinding(session *Session, email, password, proxy, preferredBindingAddress string) (string, string, string, error) {
	if session == nil {
		return "", "", "", newAuthError("Microsoft authorization session is unavailable.", AuthStatusRequestError)
	}
	if strings.TrimSpace(email) == "" || strings.TrimSpace(password) == "" {
		return "", "", "", newAuthError("Microsoft account credentials are unavailable.", AuthStatusPasswordError)
	}

	resp, err := session.Get(microsoftConsentLoginURL, requestOptions{
		Headers:           navHeaders(session, nil),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", "", wrapAuthError(fmt.Sprintf("加载 Microsoft 授权管理登录页异常: %s", err), AuthStatusRequestError, err)
	}
	if err := microsoftConsentRateLimitError(session, resp.StatusCode, "manage_login"); err != nil {
		return "", "", "", err
	}
	page, currentURL := resp.Body, resp.URL
	if isVerifiedMicrosoftConsentManagePage(page, currentURL) {
		return page, currentURL, "", nil
	}
	// The preceding device-code password login may already authenticate
	// login.live.com. In that case WS-Fed returns an auto-submit relay instead
	// of another credential page.
	if extractFormAction(page) != "" {
		return convergeMicrosoftConsentManage(session, page, currentURL, email, proxy, preferredBindingAddress)
	}

	ppft := extractPPFT(page)
	postURL := extractPostURL(page)
	uaid := firstNonEmpty(getQueryParam(postURL, "uaid"), getQueryParam(currentURL, "uaid"))
	opid := firstNonEmpty(getQueryParam(postURL, "opid"), getQueryParam(currentURL, "opid"))
	if ppft == "" || postURL == "" || uaid == "" || opid == "" {
		logWarning("Microsoft 授权管理登录页缺少字段: url=%s page_id=%s ppft=%t post_url=%t uaid=%t opid=%t", currentURL, extractPageID(page), ppft != "", postURL != "", uaid != "", opid != "")
		return "", "", "", newAuthError("Microsoft account authorization login page is incomplete.", AuthStatusAuthTimeout)
	}

	if err := initializeMicrosoftConsentFingerprint(session, uaid); err != nil {
		return "", "", "", err
	}
	if err := getMicrosoftConsentCredentialType(session, email, ppft, uaid, opid, currentURL); err != nil {
		return "", "", "", err
	}
	vanguardToken, err := checkExplicitAliasPassword(session, email, password, uaid, currentURL)
	if err != nil {
		return "", "", "", err
	}
	page, currentURL, err = submitMicrosoftConsentCredentials(session, email, password, ppft, postURL, vanguardToken, currentURL)
	if err != nil {
		return "", "", "", err
	}
	page, currentURL, err = handleJSPollingPage(session, page, currentURL)
	if err != nil {
		return "", "", "", err
	}
	return convergeMicrosoftConsentManage(session, page, currentURL, email, proxy, preferredBindingAddress)
}

func bindMissingAuxiliaryEmail(session *Session, email, password, proxy, preferredBindingAddress string) (string, *Session, error) {
	bindingSession := session
	page, currentURL, err := loadMicrosoftProofsAddPage(session)
	if err != nil {
		logWarning("Microsoft 裸 AddProof 入口不可用, 改用新会话获取 AddAssocId 登录上下文")
		bindingSession, err = newBrowserSession(session.context(), proxy)
		if err != nil {
			return "", nil, wrapAuthError(fmt.Sprintf("创建 Microsoft 辅助邮箱绑定会话失败: %s", err), AuthStatusRequestError, err)
		}
		page, currentURL, err = loadMicrosoftProofsAddPageWithPassword(bindingSession, email, password)
		if err != nil {
			return "", nil, err
		}
	}
	page, currentURL, boundMailbox, err := bindAuxiliaryEmail(bindingSession, page, currentURL, proxy, email, preferredBindingAddress)
	if err != nil {
		return "", nil, err
	}
	if _, _, err = convergeExplicitAliasToAddAssocID(bindingSession, page, currentURL); err != nil {
		return "", nil, err
	}
	if boundMailbox = normalizeRecoveryMailbox(boundMailbox); boundMailbox == "" || !UsesActiveAuxiliaryDomain(boundMailbox) {
		return "", nil, newAuthError("Microsoft recovery mailbox binding could not be verified.", AuthStatusRequestError)
	}
	return boundMailbox, bindingSession, nil
}

func loadMicrosoftProofsAddPage(session *Session) (string, string, error) {
	if session == nil {
		return "", "", newAuthError("Microsoft authorization session is unavailable.", AuthStatusRequestError)
	}
	resp, err := session.Get(microsoftProofsAddURL, requestOptions{
		Headers:           navHeaders(session, map[string]string{"Referer": microsoftConsentManageURL}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("加载 Microsoft 辅助邮箱绑定页异常: %s", err), AuthStatusRequestError, err)
	}
	return convergeMicrosoftProofsAddPage(session, resp.Body, resp.URL)
}

func loadMicrosoftProofsAddPageWithPassword(session *Session, email, password string) (string, string, error) {
	page, currentURL, _, err := loadExplicitAliasPasswordIdentity(session, email, password)
	if err != nil {
		return "", "", err
	}
	return convergeMicrosoftProofsAddPage(session, page, currentURL)
}

func convergeMicrosoftProofsAddPage(session *Session, page, currentURL string) (string, string, error) {
	var err error
	for round := 0; round < 3; round++ {
		action := resolveURL(currentURL, extractFormAction(page))
		if !isMicrosoftProofsAddURL(action) {
			if msaclDebugLogs {
				_ = os.WriteFile("/tmp/msacl_proofs_add_stuck.html", []byte(page), 0o600)
			}
			current, _ := url.Parse(currentURL)
			target, _ := url.Parse(action)
			logWarning("Microsoft 辅助邮箱绑定页无效: round=%d current=%s%s action=%s%s", round+1, current.Hostname(), current.Path, target.Hostname(), target.Path)
			logPageScene("Microsoft 辅助邮箱绑定页无效", page, currentURL)
			return "", "", newAuthError("Microsoft recovery mailbox binding page is incomplete.", AuthStatusAuthTimeout)
		}
		fields := extractHiddenInputs(page)
		if isAutoSubmitPage(page, action) && fields["ipt"] != "" && fields["pprid"] != "" {
			page, currentURL, err = handleAutoSubmit(session, page, currentURL, action)
			if err != nil {
				return "", "", err
			}
			continue
		}
		return page, currentURL, nil
	}
	return "", "", newAuthError("Microsoft recovery mailbox binding relay did not complete.", AuthStatusAuthTimeout)
}

func isMicrosoftProofsAddURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil &&
		strings.EqualFold(parsed.Scheme, "https") &&
		strings.EqualFold(parsed.Hostname(), "account.live.com") &&
		strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/proofs/Add")
}

func initializeMicrosoftConsentFingerprint(session *Session, uaid string) error {
	urls := []string{
		fmt.Sprintf("https://fpt.live.com/?session_id=%s&CustomerId=%s&PageId=SI", url.QueryEscape(uaid), microsoftConsentFPTCustomerID),
		fmt.Sprintf("https://fpt.live.com/Images/Clear.PNG?ctx=jscb1.0&session_id=%s&CustomerId=%s", url.QueryEscape(uaid), microsoftConsentFPTCustomerID),
	}
	for _, rawURL := range urls {
		if _, err := session.Get(rawURL, requestOptions{
			Headers:           navHeaders(session, nil),
			AllowRedirects:    true,
			HasAllowRedirects: true,
		}); err != nil {
			return wrapAuthError(fmt.Sprintf("初始化 Microsoft 登录指纹异常: %s", err), AuthStatusRequestError, err)
		}
	}
	return nil
}

func getMicrosoftConsentCredentialType(session *Session, email, ppft, uaid, opid, referer string) error {
	endpoint := fmt.Sprintf(
		"https://login.live.com/GetCredentialType.srf?opid=%s&id=%s&mkt=ZH-CN&lc=2052&uaid=%s",
		url.QueryEscape(opid),
		microsoftConsentLoginID,
		url.QueryEscape(uaid),
	)
	resp, err := session.Post(endpoint, requestOptions{
		JSON: map[string]any{
			"checkPhones":                    true,
			"country":                        "",
			"federationFlags":                3,
			"flowToken":                      ppft,
			"forceotclogin":                  false,
			"isCookieBannerShown":            false,
			"isExternalFederationDisallowed": false,
			"isFederationDisabled":           false,
			"isFidoSupported":                true,
			"isOtherIdpSupported":            false,
			"isReactLoginRequest":            true,
			"isRemoteConnectSupported":       false,
			"isRemoteNGCSupported":           true,
			"isSignup":                       false,
			"originalRequest":                "",
			"otclogindisallowed":             false,
			"uaid":                           uaid,
			"username":                       email,
		},
		Headers: corsHeaders(session, map[string]string{
			"Content-Type":      "application/json; charset=utf-8",
			"Origin":            "https://login.live.com",
			"Referer":           referer,
			"client-request-id": uaid,
			"correlationId":     uaid,
			"hpgact":            "0",
			"hpgid":             "33",
		}),
	})
	if err != nil {
		return wrapAuthError(fmt.Sprintf("Microsoft 授权管理 GetCredentialType 异常: %s", err), AuthStatusRequestError, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAuthError(fmt.Sprintf("Microsoft account credential lookup failed (HTTP %d).", resp.StatusCode), AuthStatusRequestError)
	}
	var data map[string]any
	if err := resp.JSON(&data); err != nil {
		return newAuthError("Microsoft account credential lookup returned an invalid response.", AuthStatusRequestError)
	}
	if asInt(data["IfExistsResult"]) != 0 {
		return newAuthError("账号不存在", AuthStatusUnknownMailbox)
	}
	return nil
}

func submitMicrosoftConsentCredentials(session *Session, email, password, ppft, postURL, vanguardToken, referer string) (string, string, error) {
	resp, err := session.Post(postURL, requestOptions{
		Data: map[string]string{
			"ps":                    "2",
			"psRNGCDefaultType":     "",
			"psRNGCEntropy":         "",
			"psRNGCSLK":             "",
			"canary":                "",
			"ctx":                   "",
			"hpgrequestid":          "",
			"PPFT":                  ppft,
			"PPSX":                  "Passport",
			"NewUser":               "1",
			"FoundMSAs":             "",
			"fspost":                "0",
			"i21":                   "0",
			"CookieDisclosure":      "0",
			"IsFidoSupported":       "1",
			"isSignupPost":          "0",
			"isRecoveryAttemptPost": "0",
			"i13":                   "0",
			"login":                 email,
			"loginfmt":              email,
			"type":                  "11",
			"LoginOptions":          "3",
			"lrt":                   "",
			"lrtPartition":          "",
			"hisRegion":             "",
			"hisScaleUnit":          "",
			"cpr":                   "0",
			"passwd":                password,
			"vanguardflowtoken":     vanguardToken,
		},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://login.live.com",
			"Referer":      referer,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("提交 Microsoft 授权管理凭据异常: %s", err), AuthStatusRequestError, err)
	}
	return resp.Body, resp.URL, nil
}

func convergeMicrosoftConsentManage(session *Session, page, currentURL, email, proxy, preferredBindingAddress string) (string, string, string, error) {
	boundMailbox := ""
	for round := 0; round < 16; round++ {
		if isVerifiedMicrosoftConsentManagePage(page, currentURL) {
			return page, currentURL, boundMailbox, nil
		}
		logInfo("Microsoft 授权管理中继 #%d: url=%s page_id=%s action=%s", round+1, currentURL, extractPageID(page), extractFormAction(page))

		if nextPage, nextURL, handled, err := cancelMicrosoftConsentPasskey(session, page, currentURL); err != nil {
			return "", "", "", err
		} else if handled {
			page, currentURL = nextPage, nextURL
			continue
		}

		if isKMSIPage(page) && extractPostURL(page) != "" {
			nextPage, nextURL, _, err := declineKMSI(session, page, currentURL, currentURL)
			if err != nil {
				return "", "", "", err
			}
			page, currentURL = nextPage, nextURL
			continue
		}

		action := extractFormAction(page)
		isIdentity, isProofs := accountVerificationPageKinds(page, currentURL, action)
		resolvedAction := action
		if resolvedAction != "" && resolvedAction != "#" {
			resolvedAction = resolveURL(currentURL, resolvedAction)
		}
		if isProofs && isAddEmailPage(page, resolvedAction) {
			nextPage, nextURL, verifiedMailbox, err := bindAuxiliaryEmail(
				session,
				page,
				currentURL,
				proxy,
				email,
				preferredBindingAddress,
			)
			if err != nil {
				return "", "", "", err
			}
			boundMailbox = firstNonEmpty(verifiedMailbox, boundMailbox)
			preferredBindingAddress = firstNonEmpty(verifiedMailbox, preferredBindingAddress)
			page, currentURL = nextPage, nextURL
			continue
		}
		if isIdentity || isProofs {
			nextPage, nextURL, verifiedMailbox, err := handleAccountPagesWithOptions(
				session,
				page,
				currentURL,
				proxy,
				10,
				email,
				nil,
				true,
				preferredBindingAddress,
			)
			if err != nil {
				return "", "", "", err
			}
			boundMailbox = firstNonEmpty(verifiedMailbox, boundMailbox)
			preferredBindingAddress = firstNonEmpty(verifiedMailbox, preferredBindingAddress)
			page, currentURL = nextPage, nextURL
			continue
		}

		if redirectURL := extractMicrosoftPrivacyRedirectURL(page, currentURL); redirectURL != "" {
			resp, err := session.Get(redirectURL, requestOptions{
				Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
				AllowRedirects:    true,
				HasAllowRedirects: true,
			})
			if err != nil {
				logWarning("Microsoft 隐私声明中继请求失败: url=%s err=%s", redirectURL, err)
				return "", "", "", wrapAuthError(fmt.Sprintf("Microsoft 隐私声明中继异常: %s", err), AuthStatusRequestError, err)
			}
			page, currentURL = resp.Body, resp.URL
			continue
		}

		if strings.Contains(strings.ToLower(currentURL), "account.live.com/auth/redirect") && action == "" {
			resp, err := session.Get(currentURL, requestOptions{
				Headers:           navHeaders(session, nil),
				AllowRedirects:    true,
				HasAllowRedirects: true,
			})
			if err != nil {
				logWarning("Microsoft 授权管理回调请求失败: url=%s err=%s", currentURL, err)
				return "", "", "", wrapAuthError(fmt.Sprintf("Microsoft 授权管理回调异常: %s", err), AuthStatusRequestError, err)
			}
			page, currentURL = resp.Body, resp.URL
			continue
		}

		if action != "" {
			targetURL := resolveURL(currentURL, html.UnescapeString(action))
			if !isMicrosoftAccountRelayURL(targetURL) {
				return "", "", "", newAuthError("Microsoft account authorization relay target is invalid.", AuthStatusRequestError)
			}
			resp, err := session.Post(targetURL, requestOptions{
				Data: extractHiddenInputs(page),
				Headers: navHeaders(session, map[string]string{
					"Content-Type": "application/x-www-form-urlencoded",
					"Origin":       originForURL(currentURL),
					"Referer":      currentURL,
				}),
				AllowRedirects:    true,
				HasAllowRedirects: true,
			})
			if err != nil {
				logWarning("Microsoft 授权管理中继请求失败: url=%s err=%s", targetURL, err)
				return "", "", "", wrapAuthError(fmt.Sprintf("Microsoft 授权管理中继异常: %s", err), AuthStatusRequestError, err)
			}
			page, currentURL = resp.Body, resp.URL
			continue
		}

		if wreply := queryValue(currentURL, "wreply"); isMicrosoftConsentManageURL(wreply) {
			resp, err := session.Get(wreply, requestOptions{
				Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
				AllowRedirects:    true,
				HasAllowRedirects: true,
			})
			if err != nil {
				logWarning("进入 Microsoft 授权管理页失败: url=%s err=%s", wreply, err)
				return "", "", "", wrapAuthError(fmt.Sprintf("进入 Microsoft 授权管理页异常: %s", err), AuthStatusRequestError, err)
			}
			page, currentURL = resp.Body, resp.URL
			continue
		}
		break
	}
	if msaclDebugLogs {
		_ = os.WriteFile("/tmp/msacl_consent_stuck.html", []byte(page), 0o600)
	}
	logPageScene("Microsoft 授权管理登录未收敛", page, currentURL)
	logWarning("Microsoft 授权管理登录未收敛: url=%s page_id=%s action=%s", currentURL, extractPageID(page), extractFormAction(page))
	return "", "", "", newAuthError("Microsoft account authorization login did not complete.", AuthStatusAuthTimeout)
}

func cancelMicrosoftConsentPasskey(session *Session, page, currentURL string) (string, string, bool, error) {
	postURL := extractPostURL(page)
	if !strings.Contains(strings.ToLower(currentURL), "passkey") &&
		!strings.Contains(strings.ToLower(postURL), "/interrupt/passkey/") &&
		!consentPasskeyPagePattern.MatchString(page) {
		return page, currentURL, false, nil
	}
	if nextPage, nextURL, ok, err := handlePasskeyInterrupt(session, page, currentURL); err != nil || ok {
		return nextPage, nextURL, ok, err
	}

	action := extractFormAction(page)
	fields := extractHiddenInputs(page)
	if action == "" {
		action = postURL
		canary := firstNonEmpty(extractMicrosoftConfigString(page, "sCanary"), extractMicrosoftConfigString(page, "canary"))
		if action == "" || canary == "" {
			return page, currentURL, false, nil
		}
		fields["canary"] = canary
	}
	action = resolveURL(currentURL, html.UnescapeString(action))
	if !isMicrosoftAccountRelayURL(action) {
		return "", "", false, newAuthError("Microsoft passkey cancellation target is invalid.", AuthStatusRequestError)
	}
	for _, key := range []string{
		"authenticator", "transports", "aaguid", "credentialDeviceType", "credentialBackedUp",
		"attestationParseError", "suberror_code", "error_message", "mediation", "clientDataJson",
		"attestationObject", "credentialId", "clientExtensionResults", "i19",
	} {
		if _, ok := fields[key]; !ok {
			fields[key] = ""
		}
	}
	fields["error_code"] = "Cancel"
	resp, err := session.Post(action, requestOptions{
		Data: fields,
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       originForURL(currentURL),
			"Referer":      currentURL,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", false, wrapAuthError(fmt.Sprintf("跳过 Microsoft 通行密钥设置异常: %s", err), AuthStatusRequestError, err)
	}
	return resp.Body, resp.URL, true, nil
}

func extractMicrosoftConfigString(page, key string) string {
	match := regexp.MustCompile(`(?is)"` + regexp.QuoteMeta(key) + `"\s*:\s*"((?:\\.|[^"\\])*)"`).FindStringSubmatch(page)
	if len(match) < 2 {
		return ""
	}
	value, err := strconv.Unquote(`"` + match[1] + `"`)
	if err != nil {
		return ""
	}
	return html.UnescapeString(value)
}

func extractMicrosoftPrivacyRedirectURL(page, currentURL string) string {
	current, err := url.Parse(strings.TrimSpace(currentURL))
	if err != nil || !strings.EqualFold(current.Hostname(), "privacynotice.account.microsoft.com") {
		return ""
	}
	match := consentPrivacyRedirectPattern.FindStringSubmatch(page)
	if len(match) < 2 {
		return ""
	}
	value, err := strconv.Unquote(`"` + match[1] + `"`)
	if err != nil {
		return ""
	}
	target, err := url.Parse(html.UnescapeString(value))
	if err != nil || !strings.EqualFold(target.Scheme, "https") || !strings.EqualFold(target.Hostname(), "login.live.com") {
		return ""
	}
	return target.String()
}

func isMicrosoftAccountRelayURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := parsed.Hostname()
	return strings.EqualFold(host, "login.live.com") ||
		strings.EqualFold(host, "account.live.com") ||
		strings.EqualFold(host, "privacynotice.account.microsoft.com")
}

func isVerifiedMicrosoftConsentManagePage(page, rawURL string) bool {
	return isMicrosoftConsentManageURL(rawURL) && (isMicrosoftConsentManagePage(page) || len(parseMicrosoftConsentClientIDs(page)) > 0)
}

func removeAllMicrosoftConsents(session *Session, initialPage string) (ConsentCleanupResult, error) {
	var clientIDs []string
	var err error
	if strings.TrimSpace(initialPage) == "" {
		clientIDs, err = listMicrosoftConsentClientIDs(session)
	} else {
		clientIDs, err = verifiedMicrosoftConsentClientIDs(initialPage)
	}
	result := ConsentCleanupResult{Before: len(clientIDs), Remaining: len(clientIDs)}
	if err != nil {
		return result, err
	}
	for _, clientID := range clientIDs {
		if err := removeMicrosoftConsent(session, clientID); err != nil {
			remaining, listErr := listMicrosoftConsentClientIDs(session)
			if listErr == nil {
				result.Removed = result.Before - len(remaining)
				result.Remaining = len(remaining)
			}
			return result, errors.Join(err, listErr)
		}
	}
	remaining, err := listMicrosoftConsentClientIDs(session)
	result.Remaining = len(remaining)
	if err != nil {
		return result, err
	}
	result.Removed = result.Before - result.Remaining
	if len(remaining) != 0 {
		return result, newAuthError("Microsoft account authorization cleanup could not be verified.", AuthStatusRequestError)
	}
	return result, nil
}

func listMicrosoftConsentClientIDs(session *Session) ([]string, error) {
	resp, err := session.Get(microsoftConsentManageURL, requestOptions{
		Headers:           navHeaders(session, nil),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return nil, wrapAuthError(fmt.Sprintf("加载 Microsoft 授权列表异常: %s", err), AuthStatusRequestError, err)
	}
	if err := microsoftConsentRateLimitError(session, resp.StatusCode, "manage_list"); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !isMicrosoftConsentManageURL(resp.URL) {
		logWarning("Microsoft 授权列表未到目标页: status=%d url=%s page_id=%s action=%s", resp.StatusCode, resp.URL, extractPageID(resp.Body), extractFormAction(resp.Body))
		return nil, newAuthError("Microsoft account authorization list is unavailable.", AuthStatusAuthTimeout)
	}
	clientIDs, err := verifiedMicrosoftConsentClientIDs(resp.Body)
	if err != nil {
		if msaclDebugLogs {
			_ = os.WriteFile("/tmp/msacl_consent_stuck.html", []byte(resp.Body), 0o600)
		}
		logPageScene("Microsoft 授权列表未识别", resp.Body, resp.URL)
		logWarning("Microsoft 授权列表页面无法识别: url=%s page_id=%s action=%s", resp.URL, extractPageID(resp.Body), extractFormAction(resp.Body))
		return nil, err
	}
	return clientIDs, nil
}

func verifiedMicrosoftConsentClientIDs(page string) ([]string, error) {
	clientIDs := parseMicrosoftConsentClientIDs(page)
	if len(clientIDs) == 0 && !isMicrosoftConsentManagePage(page) {
		return nil, newAuthError("Microsoft account authorization list could not be verified.", AuthStatusRequestError)
	}
	return clientIDs, nil
}

func removeMicrosoftConsent(session *Session, clientID string) error {
	clientID = strings.TrimSpace(clientID)
	if !validMicrosoftConsentClientID(clientID) {
		return newAuthError("Microsoft account authorization entry is invalid.", AuthStatusRequestError)
	}
	editURL := "https://account.live.com/consent/Edit?client_id=" + url.QueryEscape(clientID)
	resp, err := session.Get(editURL, requestOptions{
		Headers:           navHeaders(session, map[string]string{"Referer": microsoftConsentManageURL}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return wrapAuthError(fmt.Sprintf("加载 Microsoft 授权项异常: %s", err), AuthStatusRequestError, err)
	}
	if err := microsoftConsentRateLimitError(session, resp.StatusCode, "edit_get"); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !isMicrosoftConsentEditURL(resp.URL) {
		return newAuthError("Microsoft account authorization entry is unavailable.", AuthStatusAuthTimeout)
	}
	canary := strings.TrimSpace(extractHiddenInputs(resp.Body)["canary"])
	if canary == "" {
		return newAuthError("Microsoft account authorization entry is missing its confirmation token.", AuthStatusRequestError)
	}
	action := strings.TrimSpace(extractFormAction(resp.Body))
	if action == "" || action == "#" {
		action = resp.URL
	} else {
		action = resolveURL(resp.URL, html.UnescapeString(action))
	}
	if !isMicrosoftConsentEditURL(action) {
		return newAuthError("Microsoft account authorization removal target is invalid.", AuthStatusRequestError)
	}
	resp, err = session.Post(action, requestOptions{
		Data: map[string]string{"canary": canary},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://account.live.com",
			"Referer":      resp.URL,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return wrapAuthError(fmt.Sprintf("删除 Microsoft 授权项异常: %s", err), AuthStatusRequestError, err)
	}
	if err := microsoftConsentRateLimitError(session, resp.StatusCode, "edit_post"); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAuthError("Microsoft account authorization removal is unavailable.", AuthStatusRequestError)
	}
	return nil
}

func microsoftConsentRateLimitError(session *Session, statusCode int, stage string) error {
	if statusCode != http.StatusTooManyRequests {
		return nil
	}
	logWarning("Microsoft authorization rate limited: stage=%s status=429", stage)
	usesProxy := session != nil && session.usesProxy
	return wrapAuthError(
		"Microsoft account authorization is temporarily rate limited.",
		AuthStatusRateLimited,
		newSessionTransportError(errors.New("microsoft consent HTTP 429"), usesProxy),
	)
}

func parseMicrosoftConsentClientIDs(page string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, match := range consentEditLinkPattern.FindAllStringSubmatch(html.UnescapeString(page), -1) {
		if len(match) < 2 {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(match[1]))
		if err != nil {
			continue
		}
		clientID := strings.TrimSpace(parsed.Query().Get("client_id"))
		key := strings.ToLower(clientID)
		if !validMicrosoftConsentClientID(clientID) {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, clientID)
	}
	return result
}

func validMicrosoftConsentClientID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}

func isMicrosoftConsentManageURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && strings.EqualFold(parsed.Hostname(), "account.live.com") &&
		strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/consent/Manage")
}

func isMicrosoftConsentManagePage(page string) bool {
	lowerPage := strings.ToLower(page)
	return strings.Contains(lowerPage, "consentmanageservice") ||
		(extractPageID(page) == "i6148" && strings.Contains(page, `id="iPageTitle"`) && strings.Contains(page, `class="modulerow"`)) ||
		strings.Contains(page, "你已授予访问权限") ||
		strings.Contains(page, "你还没有授予") ||
		strings.Contains(lowerPage, "you've given access") ||
		strings.Contains(lowerPage, "you have given access") ||
		strings.Contains(lowerPage, "you haven't given access") ||
		strings.Contains(lowerPage, "you have not given access")
}

func isMicrosoftConsentEditURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && strings.EqualFold(parsed.Hostname(), "account.live.com") &&
		strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/consent/Edit")
}
