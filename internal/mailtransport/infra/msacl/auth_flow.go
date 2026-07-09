package msacl

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type AuthSuccess struct {
	ClientID     string
	AccessToken  string
	RefreshToken string
	BoundMailbox string
}

func requestDeviceCode(session *Session) (string, string, error) {
	resp, err := session.Post("https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode", requestOptions{
		Data: map[string]string{"client_id": clientID, "scope": scope},
		Headers: map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		},
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("获取 device code 请求异常: %s", err), AuthStatusRequestError, err)
	}
	var data map[string]any
	if err := resp.JSON(&data); err != nil {
		logError("devicecode 返回非 JSON: status=%d", resp.StatusCode)
		logDebug("devicecode 非 JSON 响应: body_len=%d", len(resp.Body))
		return "", "", newAuthError(fmt.Sprintf("获取 device code 失败 (HTTP %d)", resp.StatusCode))
	}
	userCode := asString(data["user_code"])
	deviceCode := asString(data["device_code"])
	if userCode == "" {
		return "", "", newAuthError(firstNonEmpty(asString(data["error_description"]), "获取 device code 失败"))
	}
	logInfo("步骤1: 获取设备码成功")
	sessionSetDC(session, asIntDefault(data["interval"], 5), asIntDefault(data["expires_in"], 900))
	return userCode, deviceCode, nil
}

func sessionSetDC(session *Session, interval, expiresIn int) {
	if session == nil {
		return
	}
	session.dcInterval = interval
	session.dcExpiresIn = expiresIn
}

func sessionDCInterval(session *Session) int {
	if session != nil && session.dcInterval > 0 {
		return session.dcInterval
	}
	return tokenPollInterval
}

func loadRemoteConnectPage(session *Session) (string, error) {
	logInfo("步骤2: 加载远程连接页")
	resp, err := postEmptyForm(session, "https://login.live.com/oauth20_remoteconnect.srf")
	if err != nil {
		return "", err
	}
	ppft := extractPPFT(resp.Body)
	if ppft == "" {
		return "", newAuthError("授权页未找到 PPFT")
	}
	logInfo("步骤2: 远程连接页加载成功 (status=%d)", resp.StatusCode)
	logDebug("步骤2: PPFT 长度=%d", len(ppft))
	return ppft, nil
}

func submitUserCode(session *Session, userCode, ppft string) (string, string, string, string, string, error) {
	logInfo("步骤3: 提交用户码")
	resp, err := session.Post("https://login.live.com/oauth20_remoteconnect.srf?lc=2052", requestOptions{
		Data: map[string]string{
			"otc":          userCode,
			"canary":       "",
			"PPFT":         ppft,
			"hpgrequestid": "",
			"i19":          "12262",
		},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://login.live.com",
			"Referer":      "https://login.live.com/oauth20_remoteconnect.srf",
		}),
	})
	if err != nil {
		return "", "", "", "", "", wrapAuthError(fmt.Sprintf("提交用户码请求异常: %s", err), AuthStatusRequestError, err)
	}
	page := resp.Body
	ppft2 := extractPPFT(page)
	postURL := extractPostURL(page)
	m1 := regexp.MustCompile(`(?is)"urlPost":"([^"]+)"`).FindStringSubmatch(page)
	m3 := regexp.MustCompile(`(?is)https://login\.live\.com/ppsecure/post\.srf[^"'\s\\]*`).FindStringSubmatch(page)
	urlPostJSON := "NONE"
	if len(m1) > 1 {
		urlPostJSON = left(m1[1], 80)
	}
	ppsecureRaw := "NONE"
	if len(m3) > 0 {
		ppsecureRaw = left(m3[0], 80)
	}
	logDebug("步骤3: ppft2_len=%d post_url=%s urlPost_json=%s ppsecure_raw=%s", len(ppft2), postURL, urlPostJSON, ppsecureRaw)
	if ppft2 == "" || postURL == "" {
		logError("步骤3失败: PPFT2=%t, post_url=%s, page_len=%d", ppft2 != "", postURL, len(page))
		return "", "", "", "", "", newAuthError("提交 device code 后解析失败")
	}
	uaid := getQueryParam(postURL, "uaid")
	opid := getQueryParam(postURL, "opid")
	logInfo("步骤3: 用户码提交成功")
	logDebug("步骤3: uaid=%s, opid=%s", uaid, opid)
	return page, ppft2, postURL, uaid, opid, nil
}

func getCredentialType(session *Session, email, ppft, uaid, opid string) ([]ProofData, error) {
	logInfo("步骤4: 检查账号登录方式")
	logDebug("步骤4: email=%s, uaid=%s, opid=%s", email, uaid, opid)
	resp, err := session.Post(fmt.Sprintf("https://login.live.com/GetCredentialType.srf?opid=%s&id=293577&client_id=0000000040C8F39E&mkt=ZH-CN&lc=2052&uaid=%s", opid, uaid), requestOptions{
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
			"Referer":           "https://login.live.com/oauth20_remoteconnect.srf?lc=2052",
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
		logWarning("步骤4: GetCredentialType 返回非 JSON")
		return nil, nil
	}
	if asInt(data["IfExistsResult"]) != 0 {
		return nil, newAuthError("账号不存在", AuthStatusUnknownMailbox)
	}
	var proofs []ProofData
	creds := asMap(data["Credentials"])
	for _, raw := range asSlice(creds["OtcLoginEligibleProofs"]) {
		p := asMap(raw)
		if p == nil {
			continue
		}
		proofs = append(proofs, ProofData{
			Data:        asString(p["data"]),
			Display:     asString(p["display"]),
			Type:        asInt(p["type"]),
			ClearDigits: asString(p["clearDigits"]),
		})
	}
	if asInt(creds["HasRemoteNGC"]) == 1 || asInt(creds["HasFido"]) == 1 {
		return nil, newAuthError("需要通行密钥或安全密钥", AuthStatusPasskeyRequired)
	}
	if len(proofs) > 0 {
		displays := make([]string, 0, len(proofs))
		for _, proof := range proofs {
			displays = append(displays, proof.Display)
		}
		logInfo("步骤4: 发现 %d 个 OTP proof", len(proofs))
		logDebug("步骤4: OTP proof=%v", displays)
	}
	return proofs, nil
}

func checkPassword(session *Session, email, password, uaid string, maxRetries int) (string, error) {
	if maxRetries <= 0 {
		maxRetries = 3
	}
	logInfo("步骤5: 验证密码")
	logDebug("步骤5: email=%s", email)
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := session.Post("https://login.live.com/checkpassword.srf", requestOptions{
			JSON: map[string]any{
				"username":               email,
				"password":               password,
				"checkpasswordflowtoken": "",
			},
			Headers: corsHeaders(session, map[string]string{
				"Content-Type":      "application/json; charset=utf-8",
				"Origin":            "https://login.live.com",
				"Referer":           "https://login.live.com/oauth20_remoteconnect.srf?lc=2052",
				"client-request-id": uaid,
				"correlationId":     uaid,
				"hpgact":            "0",
				"hpgid":             "33",
			}),
		})
		if err != nil {
			return "", wrapAuthError(fmt.Sprintf("checkpassword 请求异常: %s", err), AuthStatusRequestError, err)
		}
		if resp.StatusCode == 429 {
			retryAfter := 60
			if parsed, err := strconv.Atoi(resp.Header.Get("retry-after")); err == nil && parsed > 0 {
				retryAfter = parsed
			}
			logWarning("checkpassword 返回 429 (Too Many Requests), 等待 %ds 后重试 (%d/%d)", retryAfter, attempt, maxRetries)
			if attempt < maxRetries {
				if err := session.sleep(time.Duration(retryAfter) * time.Second); err != nil {
					return "", wrapAuthError(fmt.Sprintf("checkpassword 请求取消: %s", err), AuthStatusRequestError, err)
				}
				continue
			}
			return "", newAuthError(fmt.Sprintf("密码验证频率受限 (429), 请 %ds 后重试", retryAfter))
		}
		var result map[string]any
		if err := resp.JSON(&result); err != nil {
			logError("checkpassword 返回非 JSON 响应: status=%d", resp.StatusCode)
			logDebug("checkpassword 非 JSON 响应: body_len=%d", len(resp.Body))
			return "", newAuthError(fmt.Sprintf("密码验证返回异常响应 (HTTP %d)", resp.StatusCode))
		}
		logDebug("步骤5: checkpassword validation=%s error=%s", asString(result["validationresult"]), asString(result["error"]))
		if asString(result["validationresult"]) != "succeed" {
			return "", newAuthError("密码错误", AuthStatusPasswordError)
		}
		logInfo("步骤5: 密码验证成功")
		return asString(result["vanguardflowtoken"]), nil
	}
	return "", newAuthError("密码验证重试次数耗尽")
}

func submitCredentials(session *Session, email, password, ppft, postURL, vanguardToken string) (string, string, error) {
	logInfo("步骤6: 提交凭据")
	logDebug("步骤6: post_url=%s", postURL)
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
			"PPSX":                  "Passpo",
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
			"Referer":      "https://login.live.com/oauth20_remoteconnect.srf?lc=2052",
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("提交凭据请求异常: %s", err), AuthStatusRequestError, err)
	}
	logInfo("步骤6: 凭据提交完成 (status=%d)", resp.StatusCode)
	logDebug("步骤6: response_url=%s", resp.URL)
	return resp.Body, resp.URL, nil
}

func declineKMSI(session *Session, page, rawURL, postURL string) (string, string, string, error) {
	logDebug("KMSI 检查: 当前 url=%s", rawURL)
	isKMSI := strings.Contains(page, `"sPageId":"i5245"`) ||
		(strings.Contains(page, "LoginOptions") && strings.Contains(page, "type") && extractPPFT(page) != "") ||
		strings.Contains(page, "保持登录状态") ||
		strings.Contains(page, "保持登录")
	if !isKMSI {
		logInfo("KMSI: 无需处理")
		return page, rawURL, "", nil
	}
	ppft := extractPPFT(page)
	targetURL := extractPostURL(page)
	if ppft == "" || targetURL == "" {
		return page, rawURL, "", nil
	}
	logInfo("KMSI: 拒绝保持登录")
	logDebug("KMSI: target_url=%s", targetURL)
	resp, err := session.Post(targetURL, requestOptions{
		Data: map[string]string{
			"PPFT":         ppft,
			"canary":       "",
			"LoginOptions": "3",
			"type":         "28",
			"hpgrequestid": "",
			"ctx":          "",
		},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://login.live.com",
			"Referer":      postURL,
		}),
	})
	if err != nil {
		return "", "", "", wrapAuthError(fmt.Sprintf("KMSI 请求异常: %s", err), AuthStatusRequestError, err)
	}
	logInfo("KMSI: 已拒绝保持登录")
	logDebug("KMSI: 拒绝后 url=%s", resp.URL)
	return resp.Body, resp.URL, "", nil
}

func handleJSPollingPage(session *Session, page, rawURL string) (string, string, error) {
	if !strings.Contains(rawURL, "ppsecure/post.srf") {
		return page, rawURL, nil
	}
	if strings.Contains(page, "account.live.com/Abuse") {
		return "", "", newAuthError("账号已锁定", AuthStatusAccountLocked)
	}
	m := regexp.MustCompile(`(?is)var\s+ServerData\s*=\s*(\{.+?\});\s*\n?\s*</script>`).FindStringSubmatch(page)
	if len(m) < 2 {
		return page, rawURL, nil
	}
	var sd map[string]any
	if err := json.Unmarshal([]byte(m[1]), &sd); err != nil {
		return page, rawURL, nil
	}
	if asBool(sd["fHasError"]) {
		errCode := asString(sd["sErrorCode"])
		if errCode == "80041012" {
			return "", "", newAuthError("密码错误 (凭证提交后拒绝)", AuthStatusPasswordError)
		}
		if errCode == "CFFFFC15" {
			return "", "", newAuthError("账号不存在", AuthStatusUnknownMailbox)
		}
		logWarning("步骤6.5: 页面错误 code=%s", errCode)
	}
	if asBoolDefault(sd["fPollingDisabled"], true) {
		return page, rawURL, nil
	}
	for _, rawProof := range asSlice(sd["arrUserProofs"]) {
		proof := asMap(rawProof)
		ptype := asInt(proof["type"])
		display := asString(proof["display"])
		isSADef := asBool(proof["isSADef"])
		if ptype == 10 && isSADef {
			logWarning("检测到 2FA 强认证 (Authenticator)")
			return "", "", newAuthError("需要两步验证 (Authenticator)", AuthStatusMFARequired)
		}
		if (ptype == 2 || ptype == 3) && isSADef {
			return "", "", newAuthError(fmt.Sprintf("需要手机验证 (%s)", display), AuthStatusPhoneVerification)
		}
		if ptype == 1 && display != "" && strings.Contains(display, "@") {
			domain := strings.ToLower(strings.SplitN(display, "@", 2)[1])
			if !domainInProject(domain) {
				return "", "", &AuthError{Message: fmt.Sprintf("已绑定辅助邮箱(%s)", display), Status: AuthStatusAlreadyBound, BoundDisplay: display}
			}
		}
	}
	pollURL := asString(sd["urlPost"])
	sft := asString(sd["sFT"])
	ftName := firstNonEmpty(asString(sd["sFTName"]), "PPFT")
	timeout := asIntDefault(sd["iPollingTimeout"], 60)
	if timeout > 15 {
		timeout = 15
	}
	if pollURL == "" || sft == "" {
		logDebug("JS polling: 跳过 (urlPost=%t sFT_len=%d)", pollURL != "", len(sft))
		return page, rawURL, nil
	}
	logInfo("步骤6.5: 模拟 JS 轮询 (超时 %ds, sFT=%d chars)", timeout, len(sft))
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	pollCount := 0
	for time.Now().Before(deadline) {
		pollCount++
		if err := session.sleep(time.Second); err != nil {
			return "", "", wrapAuthError(fmt.Sprintf("JS polling 请求取消: %s", err), AuthStatusRequestError, err)
		}
		resp, err := session.Post(pollURL, requestOptions{
			Data: map[string]string{ftName: sft},
			Headers: navHeaders(session, map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
				"Origin":       "https://login.live.com",
				"Referer":      rawURL,
			}),
			AllowRedirects:    true,
			HasAllowRedirects: true,
		})
		if err != nil {
			return "", "", wrapAuthError(fmt.Sprintf("JS polling 请求异常: %s", err), AuthStatusRequestError, err)
		}
		newURL := resp.URL
		newPage := resp.Body
		if !strings.Contains(newURL, "ppsecure/post.srf") {
			logInfo("步骤6.5: 轮询完成, 重定向到 %s", left(newURL, 80))
			return newPage, newURL, nil
		}
		m2 := regexp.MustCompile(`(?is)var\s+ServerData\s*=\s*(\{.+?\});\s*\n?\s*</script>`).FindStringSubmatch(newPage)
		if len(m2) > 1 {
			var sd2 map[string]any
			if err := json.Unmarshal([]byte(m2[1]), &sd2); err != nil {
				continue
			}
			if asBoolDefault(sd2["fPollingDisabled"], true) {
				logInfo("步骤6.5: 轮询完成 (fPollingDisabled=True)")
				return newPage, newURL, nil
			}
			if newSFT := asString(sd2["sFT"]); newSFT != "" && newSFT != sft {
				sft = newSFT
			}
			if newPollURL := asString(sd2["urlPost"]); newPollURL != "" && newPollURL != pollURL {
				pollURL = newPollURL
			}
		}
	}
	logWarning("步骤6.5: JS 轮询超时 (%ds, %d 次)", timeout, pollCount)
	return page, rawURL, nil
}

func handleConsent(session *Session, page, rawURL string) (string, string, error) {
	logInfo("步骤8: Consent 检查")
	logDebug("步骤8: current_url=%s", rawURL)
	action := extractFormAction(page)
	if action != "" && strings.Contains(strings.ToLower(action), "account.live.com") && strings.Contains(page, "DoSubmit") {
		if !strings.HasPrefix(action, "http") {
			action = "https://account.live.com" + action
		}
		fields := extractHiddenInputs(page)
		if strings.Contains(strings.ToLower(action), "consent") {
			fields["ucaccept"] = "Yes"
		}
		resp, err := session.Post(action, requestOptions{
			Data: fields,
			Headers: navHeaders(session, map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
				"Origin":       "https://login.live.com",
				"Referer":      rawURL,
			}),
		})
		if err != nil {
			return "", "", wrapAuthError(fmt.Sprintf("Consent 自动跳转请求异常: %s", err), AuthStatusRequestError, err)
		}
		page, rawURL = resp.Body, resp.URL
	}
	if strings.Contains(page, "ucaccept") || strings.Contains(page, "pprid") || strings.Contains(strings.ToLower(rawURL), "consent") || strings.Contains(rawURL, "Consent") {
		action = extractFormAction(page)
		if action != "" && action != "#" {
			if !strings.HasPrefix(action, "http") {
				action = "https://account.live.com" + action
			}
			fields := extractHiddenInputs(page)
			fields["ucaccept"] = "Yes"
			resp, err := session.Post(action, requestOptions{
				Data: fields,
				Headers: navHeaders(session, map[string]string{
					"Content-Type": "application/x-www-form-urlencoded",
					"Origin":       "https://login.live.com",
					"Referer":      rawURL,
				}),
			})
			if err != nil {
				return "", "", wrapAuthError(fmt.Sprintf("Consent 请求异常: %s", err), AuthStatusRequestError, err)
			}
			page, rawURL = resp.Body, resp.URL
			logInfo("步骤8: Consent 已接受")
			logDebug("步骤8: Consent 后 url=%s", rawURL)
		} else {
			logWarning("步骤8: Consent 页面无有效 form action")
			logPageScene("Consent无action", page, rawURL)
		}
	}
	return page, rawURL, nil
}

func pollForToken(session *Session, deviceCode, boundMailbox string) (map[string]any, error) {
	pollInterval := sessionDCInterval(session)
	timeout := tokenPollTimeout
	if pollInterval*6 > timeout {
		timeout = pollInterval * 6
	}
	logInfo("步骤9: 开始轮询 token (超时 %ds, 间隔 %ds)", timeout, pollInterval)
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	pollCount := 0
	for time.Now().Before(deadline) {
		pollCount++
		if err := session.sleep(time.Duration(pollInterval) * time.Second); err != nil {
			return nil, wrapAuthError(fmt.Sprintf("token 轮询取消: %s", err), AuthStatusRequestError, err, boundMailbox)
		}
		resp, err := session.Post("https://login.microsoftonline.com/consumers/oauth2/v2.0/token", requestOptions{
			Data: map[string]string{
				"client_id":   clientID,
				"device_code": deviceCode,
				"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			},
			Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		})
		if err != nil {
			logWarning("token 轮询请求异常: %s", err)
			continue
		}
		var data map[string]any
		if err := resp.JSON(&data); err != nil {
			logWarning("token 轮询返回非 JSON: status=%d body_len=%d", resp.StatusCode, len(resp.Body))
			continue
		}
		if asString(data["access_token"]) != "" {
			logInfo("步骤9: 获取 token 成功 (轮询 %d 次)", pollCount)
			return data, nil
		}
		errorCode := asString(data["error"])
		logDebug("token 轮询 #%d: error=%s", pollCount, errorCode)
		if errorCode != "" && errorCode != "authorization_pending" && errorCode != "slow_down" {
			return nil, newAuthError(fmt.Sprintf("Token 错误: %s", firstNonEmpty(asString(data["error_description"]), errorCode)), AuthStatusAuthTimeout, boundMailbox)
		}
	}
	logError("步骤9: token 轮询超时 (%ds, %d 次)", tokenPollTimeout, pollCount)
	return nil, newAuthError("授权超时", AuthStatusAuthTimeout, boundMailbox)
}

func authorizeAccountImpl(ctx context.Context, email, password, proxy string, preferredBindingAddress string) (*AuthSuccess, error) {
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return nil, wrapAuthError(fmt.Sprintf("创建微软会话失败: %s", err), AuthStatusRequestError, err)
	}
	userCode, deviceCode, err := requestDeviceCode(session)
	if err != nil {
		return nil, err
	}
	ppft1, err := loadRemoteConnectPage(session)
	if err != nil {
		return nil, err
	}
	_, ppft2, postURL, uaid, opid, err := submitUserCode(session, userCode, ppft1)
	if err != nil {
		return nil, err
	}
	proofData, err := getCredentialType(session, email, ppft2, uaid, opid)
	if err != nil {
		return nil, err
	}
	for _, proof := range proofData {
		display := proof.Display
		if proof.Type == 1 && display != "" && strings.Contains(display, "@") {
			domain := strings.ToLower(strings.SplitN(display, "@", 2)[1])
			if !domainInProject(domain) {
				return nil, &AuthError{Message: fmt.Sprintf("已绑定辅助邮箱(%s)", display), Status: AuthStatusAlreadyBound, BoundDisplay: display}
			}
		} else if proof.Type == 2 || proof.Type == 3 {
			return nil, newAuthError(fmt.Sprintf("需要手机验证 (%s)", display), AuthStatusPhoneVerification)
		} else if proof.Type == 10 {
			return nil, newAuthError("需要两步验证 (Authenticator)", AuthStatusMFARequired)
		}
	}

	vanguardToken, err := checkPassword(session, email, password, uaid, 3)
	if err != nil {
		return nil, err
	}
	page, currentURL, err := submitCredentials(session, email, password, ppft2, postURL, vanguardToken)
	if err != nil {
		return nil, err
	}
	page, currentURL, err = handleJSPollingPage(session, page, currentURL)
	if err != nil {
		return nil, err
	}
	page, currentURL, bound1, err := handleAccountPages(session, page, currentURL, proxy, 10, email, proofData, preferredBindingAddress)
	if err != nil {
		return nil, err
	}
	page, currentURL, bound2, err := declineKMSI(session, page, currentURL, postURL)
	if err != nil {
		return nil, err
	}
	boundMailbox := firstNonEmpty(bound1, bound2)
	page, currentURL, err = handleConsent(session, page, currentURL)
	if err != nil {
		return nil, err
	}
	_ = page
	_ = currentURL
	tokens, err := pollForToken(session, deviceCode, boundMailbox)
	if err != nil {
		return nil, err
	}
	refreshToken := asString(tokens["refresh_token"])
	accessToken := asString(tokens["access_token"])
	return &AuthSuccess{
		ClientID:     clientID,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		BoundMailbox: boundMailbox,
	}, nil
}

func authorizeAccount(ctx context.Context, email, password, proxy string, preferredBindingAddress string) (*AuthSuccess, error) {
	result, err := authorizeAccountImpl(ctx, email, password, proxy, preferredBindingAddress)
	if err == nil {
		return result, nil
	}
	if _, ok := err.(*AuthError); ok {
		return nil, err
	}
	return nil, wrapAuthError(fmt.Sprintf("微软请求异常: %s", err), AuthStatusRequestError, err)
}

func domainInProject(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	for _, projectDomain := range mailDomains {
		if domain == strings.ToLower(projectDomain) {
			return true
		}
	}
	return false
}

func asIntDefault(value any, fallback int) int {
	if value == nil {
		return fallback
	}
	i := asInt(value)
	if i == 0 {
		return fallback
	}
	return i
}

func asBoolDefault(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return asBool(value)
}

func left(value string, n int) string {
	if n < 0 || len(value) <= n {
		return value
	}
	return value[:n]
}

func logPageScene(label, page, rawURL string) {
	hiddenNames := sortedKeys(extractHiddenInputs(page))
	if len(hiddenNames) > 40 {
		hiddenNames = hiddenNames[:40]
	}
	logDebug("%s现场: url=%s title=%s page_id=%s action=%s hidden=%v keywords=%v page_len=%d", label, rawURL, extractTitle(page), extractPageID(page), extractFormAction(page), hiddenNames, pageKeywords(page, rawURL), len(page))
}

func extractTitle(page string) string {
	m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(page)
	if len(m) < 2 {
		return ""
	}
	title := regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(m[1], " ")
	title = strings.Join(strings.Fields(html.UnescapeString(title)), " ")
	return left(title, 120)
}

func extractPageID(page string) string {
	patterns := []string{
		`(?is)"sPageId"\s*:\s*"([^"]+)"`,
		`(?is)sPageId\s*[:=]\s*['"]([^'"]+)`,
		`(?is)"pageId"\s*:\s*"([^"]+)"`,
		`(?is)<meta\b[^>]*\bname\s*=\s*['"]PageID['"][^>]*\bcontent\s*=\s*['"]([^'"]+)`,
		`(?is)<meta\b[^>]*\bcontent\s*=\s*['"]([^'"]+)['"][^>]*\bname\s*=\s*['"]PageID['"]`,
	}
	for _, pattern := range patterns {
		if m := regexp.MustCompile(pattern).FindStringSubmatch(page); len(m) > 1 {
			return left(m[1], 80)
		}
	}
	return ""
}

func pageKeywords(page, rawURL string) []string {
	haystack := strings.ToLower(page + " " + rawURL)
	var found []string
	for _, key := range []string{"oauth20_remoteconnect", "ppsecure/post.srf", "login.srf", "proofs/Verify", "proofs/Add", "identity/confirm", "Consent", "ucaccept", "LoginOptions", "authorization_pending", "DoSubmit", "fmHF", "urlPost", "urlLogin", "urlStaySignIn", "sFT", "PPFT", "sErrTxt", "fKMSIEnabled"} {
		if strings.Contains(haystack, strings.ToLower(key)) {
			found = append(found, key)
		}
	}
	return found
}
