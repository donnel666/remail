package msacl

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// OTC login constants — the GetOneTimeCode endpoint uses a different client_id
// from the AddAssocId OAuth flow.
const (
	otcLoginClientID = "0000000044C8823E"
	otcLoginID       = "38936"
)

// loginForExplicitAliasOTC replaces the old password→i5600–dead-end flow
// (loginForExplicitAlias lines 391–402) with the email OTC flow validated
// via browser CDP capture and the Python reference implementation.
//
// It reuses the first ~386 lines of loginForExplicitAlias (GET AddAssocId →
// submit email → GetCredentialType) and then diverges:
//  3. GetOneTimeCode.srf  — send OTP to the recovery mailbox
//  4. read OTP from recovery mailbox
//  5. POST ppsecure/post.srf with type=27  — submit the OTP
//  6. declineKMSI → relay → follow (same as existing loginForExplicitAlias lines 417–448)
//
// bindingAddress is the full recovery (auxiliary) mailbox, e.g. ocom_...@aishop6.com.
func loginForExplicitAliasOTC(session *Session, email, proxy, bindingAddress string) (string, string, error) {
	if strings.TrimSpace(bindingAddress) == "" {
		return "", "", fmt.Errorf("OTC login requires a binding mailbox address")
	}

	// ---- reused lines 302–386: GET AddAssocId → submit email → GetCredentialType ----
	logInfo("OTC 步骤1: 访问 AddAssocId")
	resp, err := session.Get(addAssocIDURL, requestOptions{
		Headers:           navHeaders(session, nil),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("加载 AddAssocId 请求异常: %s", err), AuthStatusRequestError, err)
	}
	page, currentURL := resp.Body, resp.URL
	if strings.Contains(strings.ToLower(currentURL), "account.live.com/addassocid") {
		return page, currentURL, nil
	}

	ppft := extractPPFT(page)
	postURL := extractPostURL(page)
	if ppft == "" || postURL == "" {
		resp, err = session.Get("https://login.live.com/login.srf", requestOptions{
			Headers:           navHeaders(session, nil),
			AllowRedirects:    true,
			HasAllowRedirects: true,
		})
		if err != nil {
			return "", "", wrapAuthError(fmt.Sprintf("加载微软登录页异常: %s", err), AuthStatusRequestError, err)
		}
		page, currentURL = resp.Body, resp.URL
		ppft = extractPPFT(page)
		postURL = extractPostURL(page)
	}
	if ppft == "" || postURL == "" {
		stage := explicitAliasStageLoginMissingPPFT
		if ppft != "" {
			stage = explicitAliasStageLoginMissingPostURL
		}
		return "", "", newExplicitAliasStageError("OAuth 登录页缺少 PPFT 或提交地址", AuthStatusAuthTimeout, stage)
	}
	uaid := firstNonEmpty(getQueryParam(postURL, "uaid"), getQueryParam(currentURL, "uaid"))

	logInfo("OTC 步骤2: 提交账号邮箱")
	resp, err = session.Post(postURL, requestOptions{
		Data: map[string]string{
			"login": email, "loginfmt": email, "type": "11", "LoginOptions": "3",
			"lrt": "", "lrtPartition": "", "hisRegion": "", "hisScaleUnit": "",
			"PPFT": ppft, "canary": "", "i19": "63791",
		},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://login.live.com",
			"Referer":      currentURL,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("提交微软账号异常: %s", err), AuthStatusRequestError, err)
	}
	page, currentURL = resp.Body, resp.URL
	ppft = extractPPFT(page)
	verifyPostURL := extractPostURL(page)
	if verifyPostURL == "" {
		return "", "", newExplicitAliasStageError("提交凭据后缺少 post_url", AuthStatusAuthTimeout, explicitAliasStageLoginMissingPostURL)
	}
	opid := getQueryParam(currentURL, "opid")

	// GetCredentialType — using the OTC-login client_id (0000000044C8823E / 38936)
	logInfo("OTC 步骤3: GetCredentialType 取邮箱 proof")
	proofData, err := getOTCCredentialType(session, email, ppft, uaid, opid, currentURL)
	if err != nil {
		return "", "", err
	}
	if len(proofData) == 0 {
		return "", "", newExplicitAliasStageError("GetCredentialType 未返回邮箱 proof", AuthStatusAuthTimeout, explicitAliasStageAccountPageIncomplete)
	}
	// Pick the first type=1 email proof whose display looks like an email
	// (matches the Python reference: type==1 and "@" in display).
	var proofDataToken string
	for _, p := range proofData {
		if p.Type == 1 && strings.Contains(p.Display, "@") {
			proofDataToken = p.Data
			break
		}
	}
	if proofDataToken == "" {
		return "", "", newExplicitAliasStageError("GetCredentialType 未返回 type=1 邮箱 proof", AuthStatusAuthTimeout, explicitAliasStageAccountPageIncomplete)
	}
	logInfo("OTC 选中邮箱 proof 辅助=%s", bindingAddress)

	// ---- OTC-specific steps: GetOneTimeCode → read code → type=27 verify ----

	// Establish the mailbox baseline BEFORE sending the code, so a fast-arriving
	// OTP is not swallowed into the "seen" snapshot. This matches the Python
	// reference (records base_id before GetOneTimeCode) and the existing
	// bindAuxiliaryEmail pattern (starts the watcher before SendOtt).
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	seen, err := snapshotMailboxKeys(ctx, bindingAddress, proxy)
	if err != nil {
		logWarning("OTC snapshotMailboxKeys 失败: %v", err)
		// Continue — seen may be partial or empty; mailWaitCode still polls.
	}

	// 3. Send OTP via GetOneTimeCode.srf
	logInfo("OTC 步骤4: GetOneTimeCode 发码到 %s", bindingAddress)
	sendURL := fmt.Sprintf("https://login.live.com/GetOneTimeCode.srf?id=%s&client_id=%s", otcLoginID, otcLoginClientID)
	sendResp, err := session.Post(sendURL, requestOptions{
		Data: map[string]string{
			"login":                  email,
			"flowtoken":              ppft,
			"purpose":                "eOTT_OtcLogin",
			"channel":                "Email",
			"ChallengeViewSupported": "1",
			"uaid":                   uaid,
			"AltEmailE":              proofDataToken,
			"lcid":                   "2052",
			"ProofConfirmation":      bindingAddress,
		},
		Headers: corsHeaders(session, map[string]string{
			"Content-Type":      "application/x-www-form-urlencoded",
			"Accept":            "application/json",
			"Origin":            "https://login.live.com",
			"Referer":           currentURL,
			"client-request-id": uaid,
			"correlationid":     uaid,
			"hpgact":            "0",
			"hpgid":             "33",
		}),
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("GetOneTimeCode 请求异常: %s", err), AuthStatusRequestError, err)
	}
	var sendData map[string]any
	if err := sendResp.JSON(&sendData); err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("GetOneTimeCode 返回非 JSON: %s", sendResp.Body[:200]), AuthStatusRequestError, err)
	}
	if asInt(sendData["State"]) != 201 {
		return "", "", newAuthError(fmt.Sprintf("GetOneTimeCode 未返回 State=201: %v", sendData), AuthStatusRequestError)
	}
	flowToken := asString(sendData["FlowToken"])
	if flowToken == "" {
		return "", "", newAuthError("GetOneTimeCode 未返回 FlowToken", AuthStatusRequestError)
	}
	logInfo("OTC 发码成功 State=201")

	// 4. Read OTP from the recovery mailbox (baseline already snapshotted above)
	logInfo("OTC 步骤5: 读辅助邮箱验证码")
	code, err := mailWaitCode(ctx, bindingAddress, proxy, 90, seen)
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("读取验证码超时: %v", err), AuthStatusCodeTimeout, err)
	}
	if code == "" {
		return "", "", newAuthError("未收到辅助邮箱验证码", AuthStatusCodeTimeout)
	}
	logInfo("OTC 验证码=%s", code)

	// 5. Submit the OTP — type=27 (verified via CDP capture)
	logInfo("OTC 步骤6: 提交验证码 (type=27)")
	resp, err = session.Post(verifyPostURL, requestOptions{
		Data: map[string]string{
			"SentProofIDE":          proofDataToken,
			"ProofConfirmation":     bindingAddress,
			"ProofType":             "1",
			"otc":                   code,
			"ps":                    "3",
			"psRNGCDefaultType":     "",
			"psRNGCEntropy":         "",
			"psRNGCSLK":             "",
			"canary":                "",
			"ctx":                   "",
			"hpgrequestid":          "",
			"PPFT":                  flowToken,
			"PPSX":                  "Pass",
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
			"type":                  "27",
			"LoginOptions":          "3",
			"lrt":                   "",
			"lrtPartition":          "",
			"hisRegion":             "",
			"hisScaleUnit":          "",
			"cpr":                   "0",
		},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://login.live.com",
			"Referer":      currentURL,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("提交验证码异常: %s", err), AuthStatusRequestError, err)
	}
	page, currentURL = resp.Body, resp.URL
	logDebug("OTC type=27 验码后落 %s", currentURL)

	// ---- 收敛到 AddAssocId (KMSI/passkey 等中断) ----
	// type=27 验码后微软会串联若干中断 (KMSI, passkey 强制注册, 再 KMSI, ...);
	// 交给共享的收敛循环 (add_alias_password.go) 处理到别名管理页。
	logInfo("OTC 步骤7: 收敛到 AddAssocId (验码后落点=%s pageID=%s)", currentURL, extractPageID(page))
	return convergeExplicitAliasToAddAssocID(session, page, currentURL)
}

// SyncAndAddExplicitAliasesResult contains the outcome of a single "login →
// list existing → add new aliases" session.
type SyncAndAddExplicitAliasesResult struct {
	// ExistingAliases lists all @outlook.com/@hotmail/… aliases found on the
	// account.live.com/names/manage page. The caller should backfill these
	// into the local DB (explicit_aliases) for reconciliation.
	ExistingAliases []string
	// AddResults contains one entry per attempted candidate.
	AddResults []ExplicitAliasResult
	// OverallFailure is set when the login or list step itself failed.
	OverallFailure *ExplicitAliasResult
}

// SyncAndAddExplicitAliases performs a single login session that:
//  1. Logs in via email OTC (mechanism 1, eOTT_OtcLogin). If the code is not
//     received (eOTT_OtcLogin daily limit reached), it falls back to a password
//     login (mechanism 2, the INDEPENDENT eOTT_OneTimePassword channel).
//  2. GET account.live.com/names/manage and lists all existing aliases
//  3. Creates each candidate in candidates (addSingleExplicitAlias, reusing session)
//
// It is the "one login does list+add" primitive required by the architecture
// constraint (max 3 OTP sends per channel per account per day). Only the
// SUCCEEDING login proceeds to list+add — a fallback replaces the failed login,
// it never doubles the work.
func SyncAndAddExplicitAliases(ctx context.Context, email, password, proxy, bindingAddress string, candidates []string) *SyncAndAddExplicitAliasesResult {
	ctx = contextOrBackground(ctx)
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		f := ExplicitAliasResult{
			Category:    "request",
			SafeMessage: fmt.Sprintf("创建浏览器会话失败: %s", err),
		}
		return &SyncAndAddExplicitAliasesResult{
			OverallFailure: &f,
		}
	}

	// Step 1: OTC login (mechanism 1). On a code timeout — eOTT_OtcLogin's daily
	// 3-send limit reached — fall back to the password login (mechanism 2), which
	// uses the independent eOTT_OneTimePassword channel (another 3 codes/day). A
	// fresh session is required; the OTC session is mid-challenge. The password
	// login then does the SAME one-login list+add below (no redundant login).
	//
	// MSACL_FORCE_PASSWORD_LOGIN=1 skips OTC and uses the password login directly
	// (debug/ops override, e.g. when eOTT_OtcLogin is known-exhausted).
	var currentURL string
	if os.Getenv("MSACL_FORCE_PASSWORD_LOGIN") == "1" && strings.TrimSpace(password) != "" {
		logWarning("MSACL_FORCE_PASSWORD_LOGIN=1: 跳过 OTC, 直接密码登录 (第二套 eOTT_OneTimePassword)")
		if _, currentURL, err = loginForExplicitAliasPassword(session, email, password, proxy, bindingAddress); err != nil {
			mapped := mapExplicitAliasError(err)
			return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
		}
	} else if _, currentURL, err = loginForExplicitAliasOTC(session, email, proxy, bindingAddress); err != nil {
		mapped := mapExplicitAliasError(err)
		if mapped.Category != "code_timeout" || strings.TrimSpace(password) == "" {
			return &SyncAndAddExplicitAliasesResult{
				OverallFailure: &mapped,
			}
		}
		logWarning("OTC 登录收码失败(code_timeout), 切换密码登录(第二套 eOTT_OneTimePassword)")
		session, err = newBrowserSession(ctx, proxy)
		if err != nil {
			f := ExplicitAliasResult{
				Category:    "request",
				SafeMessage: fmt.Sprintf("创建浏览器会话失败: %s", err),
			}
			return &SyncAndAddExplicitAliasesResult{OverallFailure: &f}
		}
		if _, currentURL, err = loginForExplicitAliasPassword(session, email, password, proxy, bindingAddress); err != nil {
			mapped2 := mapExplicitAliasError(err)
			return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped2}
		}
	}

	// Step 2: List existing aliases from names/manage. That page is often
	// bounced to login.srf?wa=wsignin1.0; follow the relay to reach it.
	logInfo("列出已有别名 (names/manage)")
	const namesManageURL = "https://account.live.com/names/manage"
	var existingAliases []string
	resp, err := session.Get(namesManageURL, requestOptions{
		Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err == nil {
		p2, f2 := resp.Body, resp.URL
		for i := 0; i < 3 && !isExplicitAliasManageURL(f2); i++ {
			p2, f2, _ = continueExplicitAliasLoginRelay(session, p2, f2, 6)
			if !isExplicitAliasManageURL(f2) {
				p2, f2, _ = followExplicitAliasTarget(session, p2, f2, 10)
			}
			if !isExplicitAliasManageURL(f2) {
				r2, e2 := session.Get(namesManageURL, requestOptions{
					Headers:           navHeaders(session, map[string]string{"Referer": f2}),
					AllowRedirects:    true,
					HasAllowRedirects: true,
				})
				if e2 != nil {
					break
				}
				p2, f2 = r2.Body, r2.URL
			}
		}
		if isExplicitAliasManageURL(f2) {
			existingAliases = extractAllExplicitAliasesFromManagePage(p2, f2)
			logInfo("发现 %d 个已有别名", len(existingAliases))
		} else {
			logWarning("未能进入 names/manage 列出别名: url=%s", f2)
		}
	} else {
		logWarning("获取 names/manage 异常: %v", err)
	}

	// Step 3: Create new aliases one by one, using the same session
	addResults := make([]ExplicitAliasResult, 0, len(candidates))
	for _, candidate := range normalizeExplicitAliasCandidates(candidates) {
		if err := ctx.Err(); err != nil {
			addResults = append(addResults, ExplicitAliasResult{
				Category:    "request",
				SafeMessage: "Microsoft alias service is temporarily unavailable.",
			})
			break
		}
		prefix := strings.TrimSuffix(candidate, "@outlook.com")
		alias, category, attempted, err := addSingleExplicitAlias(session, prefix, email, proxy, bindingAddress)
		if err != nil {
			mapped := mapExplicitAliasError(err)
			mapped.ProxyFailure = true
			addResults = append(addResults, mapped)
			continue
		}
		res := ExplicitAliasResult{Aliases: []string{alias}, Category: category}
		res.Attempted = []string{}
		if attempted {
			res.Attempted = []string{alias}
		}
		addResults = append(addResults, res)
	}

	return &SyncAndAddExplicitAliasesResult{
		ExistingAliases: existingAliases,
		AddResults:      addResults,
	}
}

// the OTC-specific client_id/id (0000000044C8823E / 38936) instead of the
// alias-flow pair (f6061517-... / 293577). The JSON body is the same.
func getOTCCredentialType(session *Session, email, ppft, uaid, opid, referer string) ([]ProofData, error) {
	if uaid == "" {
		return nil, nil
	}
	endpoint := fmt.Sprintf(
		"https://login.live.com/GetCredentialType.srf?opid=%s&id=%s&client_id=%s&mkt=ZH-CN&lc=2052&uaid=%s",
		url.QueryEscape(opid),
		otcLoginID,
		url.QueryEscape(otcLoginClientID),
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
		return nil, wrapAuthError(fmt.Sprintf("GetCredentialType 请求异常: %s", err), AuthStatusRequestError, err)
	}
	var data map[string]any
	if err := resp.JSON(&data); err != nil {
		logWarning("OTC GetCredentialType 返回非 JSON")
		return nil, nil
	}
	if asInt(data["IfExistsResult"]) != 0 {
		return nil, newAuthError("账号不存在", AuthStatusUnknownMailbox)
	}
	credentials := asMap(data["Credentials"])
	if asInt(credentials["HasRemoteNGC"]) == 1 || asInt(credentials["HasFido"]) == 1 {
		return nil, newAuthError("需要通行密钥或安全密钥", AuthStatusPasskeyRequired)
	}
	proofs := make([]ProofData, 0)
	for _, rawProof := range asSlice(credentials["OtcLoginEligibleProofs"]) {
		proof := asMap(rawProof)
		if proof == nil {
			continue
		}
		proofs = append(proofs, ProofData{
			Data:        asString(proof["data"]),
			Display:     asString(proof["display"]),
			Type:        asInt(proof["type"]),
			ClearDigits: asString(proof["clearDigits"]),
		})
	}
	return proofs, nil
}
