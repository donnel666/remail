package msacl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// This file implements the "second" explicit-alias login mechanism: a PASSWORD
// login whose identity-verification step sends the OTP via the
// eOTT_OneTimePassword channel — an INDEPENDENT daily-quota channel from the
// eOTT_OtcLogin channel used by loginForExplicitAliasOTC (add_alias_otc.go).
//
// Verified via browser CDP capture + a headless Python reproduction on two
// accounts (91, 63): after an account's eOTT_OtcLogin daily limit (3 codes) is
// exhausted, this eOTT_OneTimePassword channel still returns State=201 and the
// recovery mailbox receives a fresh code — so the two channels together yield
// up to 6 codes/day. This login is used as a fallback when the OTC login fails
// with a code timeout (SyncAndAddExplicitAliases).
//
// Protocol differences vs the OTC login:
//   - reaches the OTP send via a password submit (checkpassword + submit creds)
//     landing on the "即将完成"/i5600 identity-verification page (hpgid=17)
//   - the proof token + PPFT come from that page's ServerData.arrUserProofs +
//     sFT (NOT from GetCredentialType.OtcLoginEligibleProofs)
//   - GetOneTimeCode uses purpose=eOTT_OneTimePassword & UIMode=11 (hpgid=17);
//     the response has no FlowToken (verify uses the page PPFT)
//   - the OTP is submitted with type=18 (a shorter body than the OTC type=27)

// arrUserProofsRE matches the ServerData.arrUserProofs array embedded in the
// identity-verification page. Non-greedy up to the closing "}]" of the array.
var arrUserProofsRE = regexp.MustCompile(`"arrUserProofs"\s*:\s*(\[.*?\}\s*\])`)

type userProof struct {
	Data    string `json:"data"`
	Display string `json:"display"`
	Type    int    `json:"type"`
}

// extractFirstUserProof pulls the first email proof (type==1 with an "@" in the
// display) from the identity page's ServerData.arrUserProofs. Returns the proof
// token (used as AltEmailE on send and SentProofIDE on verify) and its masked
// display. Empty strings if the page has no usable proof.
func extractFirstUserProof(page string) (data, display string) {
	m := arrUserProofsRE.FindStringSubmatch(page)
	if len(m) < 2 {
		return "", ""
	}
	var proofs []userProof
	if err := json.Unmarshal([]byte(m[1]), &proofs); err != nil {
		return "", ""
	}
	for _, p := range proofs {
		if p.Type == 1 && strings.Contains(p.Display, "@") {
			return p.Data, p.Display
		}
	}
	if len(proofs) > 0 {
		return proofs[0].Data, proofs[0].Display
	}
	return "", ""
}

// loginForExplicitAliasPassword performs the password login → eOTT_OneTimePassword
// verify flow and returns the alias management page (account.live.com/AddAssocId).
func loginForExplicitAliasPassword(session *Session, email, password, proxy, bindingAddress string) (string, string, error) {
	if strings.TrimSpace(bindingAddress) == "" {
		return "", "", fmt.Errorf("password login requires a binding mailbox address")
	}
	if strings.Contains(bindingAddress, "*") {
		return "", "", &AuthError{Message: "辅助邮箱为掩码/外部地址, 无法接收验证码", Status: AuthStatusAlreadyBound, BoundDisplay: bindingAddress}
	}
	if strings.TrimSpace(password) == "" {
		return "", "", newAuthError("password login requires the account password", AuthStatusPasswordError)
	}

	// ---- 步骤1-2: GET AddAssocId → 提交账号邮箱 (与 OTC 前段一致) ----
	logInfo("PWD 步骤1: 访问 AddAssocId")
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

	logInfo("PWD 步骤2: 提交账号邮箱")
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
	ppft = firstNonEmpty(extractPPFT(page), ppft)
	postURL = firstNonEmpty(extractPostURL(page), postURL)

	// ---- 步骤3: checkpassword → vanguardflowtoken ----
	logInfo("PWD 步骤3: checkpassword 验证密码")
	vanguardToken, err := checkExplicitAliasPassword(session, email, password, uaid, currentURL)
	if err != nil {
		return "", "", err
	}

	// ---- 步骤4: 提交凭据 (passwd) → i5600/身份验证页 ----
	logInfo("PWD 步骤4: 提交凭据 (passwd)")
	page, currentURL, err = submitExplicitAliasCredentials(session, email, password, ppft, postURL, vanguardToken, currentURL)
	if err != nil {
		return "", "", err
	}
	logDebug("PWD 提交凭据后落 %s pageID=%s", currentURL, extractPageID(page))

	// 身份验证页内嵌 arrUserProofs。若未直接出现 (JS 轮询页), 尝试推进一步再取。
	proofData, proofDisplay := extractFirstUserProof(page)
	if proofData == "" {
		logInfo("PWD 身份页无 arrUserProofs, 尝试 handleJSPollingPage")
		if p2, u2, jerr := handleJSPollingPage(session, page, currentURL); jerr == nil {
			page, currentURL = p2, u2
			proofData, proofDisplay = extractFirstUserProof(page)
		}
	}
	idPPFT := extractPPFT(page)
	verifyPostURL := firstNonEmpty(extractPostURL(page), currentURL)
	uaid = firstNonEmpty(getQueryParam(verifyPostURL, "uaid"), getQueryParam(currentURL, "uaid"), uaid)
	if proofData == "" || idPPFT == "" {
		_ = os.WriteFile("/tmp/msacl_pwd_identity_stuck.html", []byte("<!-- final="+currentURL+" pageID="+extractPageID(page)+" -->\n"+page), 0o644)
		return "", "", newExplicitAliasStageError(
			"密码登录未到达身份验证页 (缺 arrUserProofs/PPFT)",
			AuthStatusAuthTimeout,
			explicitAliasStageAccountPageIncomplete,
		)
	}
	logInfo("PWD 身份页 proof=%s 辅助=%s", proofDisplay, bindingAddress)

	// 记录发码前基线 (发码前, 避免秒到的码被吞入 seen)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	seen, err := snapshotMailboxKeys(ctx, bindingAddress, proxy)
	if err != nil {
		logWarning("PWD snapshotMailboxKeys 失败: %v", err)
	}

	// ---- 步骤5: GetOneTimeCode purpose=eOTT_OneTimePassword & UIMode=11 ----
	logInfo("PWD 步骤5: GetOneTimeCode (eOTT_OneTimePassword) 发码到 %s", bindingAddress)
	sendURL := fmt.Sprintf("https://login.live.com/GetOneTimeCode.srf?id=%s&client_id=%s", otcLoginID, otcLoginClientID)
	sendResp, err := session.Post(sendURL, requestOptions{
		Data: map[string]string{
			"login":                  email,
			"flowtoken":              idPPFT,
			"purpose":                "eOTT_OneTimePassword",
			"channel":                "Email",
			"ChallengeViewSupported": "1",
			"uaid":                   uaid,
			"AltEmailE":              proofData,
			"UIMode":                 "11",
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
			"hpgid":             "17",
		}),
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("GetOneTimeCode 请求异常: %s", err), AuthStatusRequestError, err)
	}
	var sendData map[string]any
	if err := sendResp.JSON(&sendData); err != nil {
		body := sendResp.Body
		if len(body) > 200 {
			body = body[:200]
		}
		return "", "", wrapAuthError(fmt.Sprintf("GetOneTimeCode 返回非 JSON: %s", body), AuthStatusRequestError, err)
	}
	if asInt(sendData["State"]) != 201 {
		return "", "", newAuthError(fmt.Sprintf("GetOneTimeCode 未返回 State=201: %v", sendData), AuthStatusRequestError)
	}
	logInfo("PWD 发码成功 State=201 (无 FlowToken, 验码用页面 PPFT)")

	// ---- 步骤6: 读辅助邮箱验证码 ----
	logInfo("PWD 步骤6: 读辅助邮箱验证码")
	code, err := mailWaitCode(ctx, bindingAddress, proxy, 90, seen)
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("读取验证码超时: %v", err), AuthStatusCodeTimeout, err)
	}
	if code == "" {
		return "", "", newAuthError("未收到辅助邮箱验证码", AuthStatusCodeTimeout)
	}
	logInfo("PWD 验证码=%s", code)

	// ---- 步骤7: 提交验证码 (type=18) ----
	logInfo("PWD 步骤7: 提交验证码 (type=18)")
	resp, err = session.Post(verifyPostURL, requestOptions{
		Data: map[string]string{
			"AddTD":              "true",
			"SentProofIDE":       proofData,
			"GeneralVerify":      "false",
			"PPFT":               idPPFT,
			"canary":             "",
			"sacxt":              "0",
			"hpgrequestid":       "",
			"hideSmsInMfaProofs": "false",
			"type":               "18",
			"login":              email,
			"infoPageShown":      "0",
			"ProofConfirmation":  bindingAddress,
			"otc":                code,
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
	logDebug("PWD type=18 验码后落 %s pageID=%s", currentURL, extractPageID(page))

	// ---- 步骤8: 收敛到 AddAssocId (KMSI/passkey 等中断) ----
	logInfo("PWD 步骤8: 收敛到 AddAssocId")
	return convergeExplicitAliasToAddAssocID(session, page, currentURL)
}

// convergeExplicitAliasToAddAssocID drives the post-verify interrupt chain
// (KMSI, passkey enrollment, chained KMSI, ...) to the alias management page
// account.live.com/AddAssocId. It loops declineKMSI → relay → follow and
// re-issues GET AddAssocId between rounds to converge. Shared by the OTC
// (add_alias_otc.go) and password logins.
func convergeExplicitAliasToAddAssocID(session *Session, page, currentURL string) (string, string, error) {
	logInfo("显式别名收敛 起点=%s pageID=%s", currentURL, extractPageID(page))
	for round := 0; round < 4; round++ {
		if strings.Contains(strings.ToLower(currentURL), "account.live.com/addassocid") {
			break
		}
		var err error
		page, currentURL, _, err = declineKMSI(session, page, currentURL, currentURL)
		if err != nil {
			return "", "", err
		}
		page, currentURL, err = continueExplicitAliasLoginRelay(session, page, currentURL, 6)
		if err != nil {
			return "", "", err
		}
		page, currentURL, err = followExplicitAliasTarget(session, page, currentURL, 10)
		if err != nil {
			return "", "", err
		}
		logInfo("显式别名收敛 第%d轮后=%s pageID=%s", round+1, currentURL, extractPageID(page))
		if strings.Contains(strings.ToLower(currentURL), "account.live.com/addassocid") {
			break
		}
		// 兜底: 跳过中断后回到已认证流程, 常经由下一轮处理的 KMSI 页。
		resp, gerr := session.Get(addAssocIDURL, requestOptions{
			Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
			AllowRedirects:    true,
			HasAllowRedirects: true,
		})
		if gerr != nil {
			return "", "", wrapAuthError(fmt.Sprintf("进入 AddAssocId 异常: %s", gerr), AuthStatusRequestError, gerr)
		}
		page, currentURL = resp.Body, resp.URL
		logInfo("显式别名收敛 第%d轮兜底GET后=%s pageID=%s", round+1, currentURL, extractPageID(page))
	}
	if !strings.Contains(strings.ToLower(currentURL), "account.live.com/addassocid") {
		_ = os.WriteFile("/tmp/msacl_converge_stuck.html", []byte("<!-- final="+currentURL+" pageID="+extractPageID(page)+" -->\n"+page), 0o644)
		logWarning("显式别名收敛卡住未到 addassocid: url=%s pageID=%s 已dump /tmp/msacl_converge_stuck.html", currentURL, extractPageID(page))
		return "", "", newExplicitAliasStageError(
			"未能进入 Microsoft 别名管理页",
			AuthStatusAuthTimeout,
			explicitAliasStageAccountPageIncomplete,
		)
	}
	return page, currentURL, nil
}
