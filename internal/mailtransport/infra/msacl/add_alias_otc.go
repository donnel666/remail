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
	if strings.Contains(bindingAddress, "*") {
		// A masked binding address (e.g. a****b@qq.com) is a recorded external
		// recovery mailbox we cannot receive codes at — fail fast instead of
		// sending an OTP to an unroutable address.
		return "", "", &AuthError{Message: "辅助邮箱为掩码/外部地址, 无法接收验证码", Status: AuthStatusAlreadyBound, BoundMailbox: bindingAddress}
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
	var proofDataToken string
	var proofDisplay string
	for _, p := range proofData {
		if p.Type == 1 && strings.Contains(p.Display, "@") && mailboxMatchesMasked(p.Display, bindingAddress) {
			proofDataToken = p.Data
			proofDisplay = p.Display
			break
		}
	}
	if proofDataToken == "" {
		return "", "", &AuthError{Message: "Microsoft 当前辅助邮箱与本地 Binding 不一致", Status: AuthStatusAlreadyBound, BoundMailbox: firstEmailProofDisplay(proofData)}
	}
	logInfo("OTC 选中邮箱 proof 辅助=%s", bindingAddress)

	// ---- OTC-specific steps: GetOneTimeCode → read code → type=27 verify ----

	// Establish the mailbox baseline BEFORE sending the code, so a fast-arriving
	// OTP is not swallowed into the "seen" snapshot. This matches the Python
	// reference (records base_id before GetOneTimeCode) and the existing
	// bindAuxiliaryEmail pattern (starts the watcher before SendOtt).
	ctx, cancel := context.WithTimeout(session.context(), 100*time.Second)
	defer cancel()
	lease, err := claimCodeMailLease(ctx, firstNonEmpty(normalizeRecoveryMask(proofDisplay), recoveryMaskFromContext(ctx)))
	if err != nil {
		return "", "", err
	}
	defer lease.releaseIfUnsent(ctx)
	seen, err := snapshotMailboxKeys(ctx, bindingAddress, proxy)
	if err != nil {
		logWarning("OTC snapshotMailboxKeys 失败: %v", err)
		// Continue — seen may be partial or empty; mailWaitCode still polls.
	}

	// 3. Send OTP via GetOneTimeCode.srf
	logInfo("OTC 步骤4: GetOneTimeCode 发码到 %s", bindingAddress)
	sendURL := fmt.Sprintf("https://login.live.com/GetOneTimeCode.srf?id=%s&client_id=%s", otcLoginID, otcLoginClientID)
	if err := lease.markSent(ctx); err != nil {
		return "", "", err
	}
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
	page, currentURL, err = convergeExplicitAliasToAddAssocID(session, page, currentURL)
	releaseCompletedCodeMailLease(session.context(), lease)
	return page, currentURL, err
}

func firstEmailProofDisplay(proofs []ProofData) string {
	for _, proof := range proofs {
		if proof.Type == 1 && strings.Contains(proof.Display, "@") {
			return proof.Display
		}
	}
	return ""
}

// SyncAndAddExplicitAliasesResult contains the outcome of a single "login →
// list existing → add new aliases" session.
type SyncAndAddExplicitAliasesResult struct {
	// ExistingAliases lists approved Microsoft aliases found on the
	// account.live.com/names/manage page. The caller backfills these into the
	// local explicit_aliases read model.
	ExistingAliases []string
	// AddResults contains one entry per attempted candidate.
	AddResults []ExplicitAliasResult
	// OverallFailure is set when the login failed.
	OverallFailure *ExplicitAliasResult
}

// SyncAndAddExplicitAliases performs a single login session that:
//  1. Logs in via email OTC (mechanism 1, eOTT_OtcLogin).
//  2. GET account.live.com/names/manage and lists all existing aliases.
//  3. Creates each candidate in candidates (addSingleExplicitAlias, reusing session).
//
// It is the "one login does list+add" primitive required by the architecture
// constraint (max 3 OTP sends per channel per account per day). Only the
// SUCCEEDING login proceeds to add — a fallback replaces the failed login,
// it never doubles the work.
func SyncAndAddExplicitAliases(ctx context.Context, email, password, proxy, bindingAddress string, candidates []string) *SyncAndAddExplicitAliasesResult {
	return syncAndAddExplicitAliases(ctx, email, password, proxy, bindingAddress, candidates, false)
}

// SyncAndAddExplicitAliasesWithPasswordBinding uses the existing password
// login path when the local Binding was absent and Microsoft may need to add or
// confirm that proof before alias management can continue.
func SyncAndAddExplicitAliasesWithPasswordBinding(ctx context.Context, email, password, proxy, bindingAddress string, candidates []string) *SyncAndAddExplicitAliasesResult {
	return syncAndAddExplicitAliases(ctx, email, password, proxy, bindingAddress, candidates, true)
}

func syncAndAddExplicitAliases(ctx context.Context, email, password, proxy, bindingAddress string, candidates []string, forcePasswordBinding bool) *SyncAndAddExplicitAliasesResult {
	ctx = contextOrBackground(ctx)
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		wrapped := wrapAuthError(fmt.Sprintf("创建浏览器会话失败: %s", err), AuthStatusRequestError, err)
		mapped := mapExplicitAliasError(wrapped)
		return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
	}

	// Step 1: OTC login (mechanism 1). A sent-but-unprocessed code keeps the
	// normalized mask leased, so a timeout must end this attempt; a second channel
	// may not send another code to the same mask in the same recovery window.
	//
	// MSACL_FORCE_PASSWORD_LOGIN=1 skips OTC and uses the password login directly
	// (debug/ops override, e.g. when eOTT_OtcLogin is known-exhausted).
	var currentURL string
	if (forcePasswordBinding || os.Getenv("MSACL_FORCE_PASSWORD_LOGIN") == "1") && strings.TrimSpace(password) != "" {
		logWarning("MSACL_FORCE_PASSWORD_LOGIN=1: 跳过 OTC, 直接密码登录 (第二套 eOTT_OneTimePassword)")
		if _, currentURL, err = loginForExplicitAliasPassword(session, email, password, proxy, bindingAddress); err != nil {
			mapped := mapExplicitAliasError(err)
			return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
		}
	} else if _, currentURL, err = loginForExplicitAliasOTC(session, email, proxy, bindingAddress); err != nil {
		mapped := mapExplicitAliasError(err)
		return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
	}

	// List first so aliases created by an earlier attempt but lost locally are
	// recovered without sending another AddAssocId request.
	logInfo("列出已有别名 (names/manage)")
	const namesManageURL = "https://account.live.com/names/manage"
	var existingAliases []string
	resp, listErr := session.Get(namesManageURL, requestOptions{
		Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if listErr != nil {
		wrapped := wrapAuthError(fmt.Sprintf("获取 names/manage 异常: %s", listErr), AuthStatusRequestError, listErr)
		mapped := mapExplicitAliasError(wrapped)
		return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
	}
	page, finalURL := resp.Body, resp.URL
	for i := 0; i < 3 && !isExplicitAliasManageURL(finalURL); i++ {
		page, finalURL, err = continueExplicitAliasLoginRelay(session, page, finalURL, 6)
		if err != nil {
			mapped := mapExplicitAliasError(err)
			return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
		}
		if !isExplicitAliasManageURL(finalURL) {
			page, finalURL, err = followExplicitAliasTarget(session, page, finalURL, 10)
			if err != nil {
				mapped := mapExplicitAliasError(err)
				return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
			}
		}
		if !isExplicitAliasManageURL(finalURL) {
			next, getErr := session.Get(namesManageURL, requestOptions{
				Headers:           navHeaders(session, map[string]string{"Referer": finalURL}),
				AllowRedirects:    true,
				HasAllowRedirects: true,
			})
			if getErr != nil {
				wrapped := wrapAuthError(fmt.Sprintf("重新获取 names/manage 异常: %s", getErr), AuthStatusRequestError, getErr)
				mapped := mapExplicitAliasError(wrapped)
				return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
			}
			page, finalURL = next.Body, next.URL
		}
	}
	if !isExplicitAliasManageURL(finalURL) {
		mapped := mapExplicitAliasError(newExplicitAliasStageError(
			"Microsoft alias manage page is unavailable.", AuthStatusAuthTimeout, explicitAliasStageManageRedirected,
		))
		return &SyncAndAddExplicitAliasesResult{OverallFailure: &mapped}
	}
	existingAliases = explicitAliasesExceptPrimary(
		extractAllExplicitAliasesFromManagePage(page, finalURL), email,
	)
	logInfo("发现 %d 个已有别名", len(existingAliases))

	// Create candidates independently in the same session; one unavailable alias
	// must not prevent later best-effort candidates from being attempted.
	addResults := make([]ExplicitAliasResult, 0, len(candidates))
	for _, candidate := range normalizeExplicitAliasCandidates(candidates) {
		if err := ctx.Err(); err != nil {
			addResults = append(addResults, ExplicitAliasResult{
				Category:    "request",
				SafeMessage: "Microsoft alias service is temporarily unavailable.",
			})
			break
		}
		alias, category, attempted, err := addSingleExplicitAlias(session, candidate, email, proxy, bindingAddress)
		if err != nil {
			addResults = append(addResults, explicitAliasAttemptFailure(alias, attempted, err))
			continue
		}
		addResults = append(addResults, explicitAliasAddResult(alias, category, attempted))
	}

	return &SyncAndAddExplicitAliasesResult{ExistingAliases: existingAliases, AddResults: addResults}
}

func explicitAliasesExceptPrimary(aliases []string, primary string) []string {
	primary = strings.ToLower(strings.TrimSpace(primary))
	result := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if strings.ToLower(strings.TrimSpace(alias)) != primary {
			result = append(result, alias)
		}
	}
	return result
}

func explicitAliasAddResult(alias, category string, attempted bool) ExplicitAliasResult {
	result := ExplicitAliasResult{Category: category}
	if attempted {
		result.Attempted = []string{alias}
	}
	if category == aliasCategoryAdded {
		result.Aliases = []string{alias}
	}
	return result
}

func explicitAliasAttemptFailure(alias string, attempted bool, err error) ExplicitAliasResult {
	result := mapExplicitAliasError(err)
	if attempted && strings.TrimSpace(alias) != "" {
		result.Attempted = []string{alias}
	}
	return result
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
