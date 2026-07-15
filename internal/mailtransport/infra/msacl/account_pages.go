package msacl

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var transientRequestMarkers = []string{
	"Failed to perform",
	"curl: (18)",
	"curl: (23)",
	"curl: (28)",
	"curl: (52)",
	"curl: (56)",
}

type ProofData struct {
	Data        string
	Display     string
	Type        int
	ClearDigits string
}

func extractPPFT(page string) string {
	patterns := []string{
		`(?is)name="PPFT"[^>]*value="([^"]*)"`,
		`(?is)name=\\"PPFT\\"[^>]*?value=\\"([^\\"]+)`,
		`(?is)"sFT":"([^"]+)"`,
		`(?is)sFT["']?\s*[:=]\s*["']([^"']+)`,
	}
	for _, pattern := range patterns {
		if m := regexp.MustCompile(pattern).FindStringSubmatch(page); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func extractPostURL(page string) string {
	patterns := []string{
		`(?is)"urlPost":"([^"]+)"`,
		`(?is)urlPost\s*[:=]\s*['"]([^'"]+)`,
		`(?is)https://login\.live\.com/ppsecure/post\.srf[^"'\s\\]*`,
	}
	for _, pattern := range patterns {
		m := regexp.MustCompile(pattern).FindStringSubmatch(page)
		if len(m) > 1 {
			return strings.ReplaceAll(m[1], `\/`, `/`)
		}
		if len(m) == 1 {
			return m[0]
		}
	}
	return ""
}

func extractFormAction(page string) string {
	re := regexp.MustCompile(`(?is)<form\b[^>]*>`)
	for _, tag := range re.FindAllString(page, -1) {
		if action := extractTagAttrs(tag)["action"]; action != "" {
			return action
		}
	}
	return ""
}

func extractTagAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	re := regexp.MustCompile(`(?is)([:\w-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	for _, match := range re.FindAllStringSubmatch(tag, -1) {
		value := ""
		for _, group := range match[2:] {
			if group != "" {
				value = group
				break
			}
		}
		attrs[strings.ToLower(match[1])] = html.UnescapeString(value)
	}
	return attrs
}

func extractFormHTML(page, formID string) string {
	re := regexp.MustCompile(`(?is)<form\b[^>]*>.*?</form>`)
	for _, formHTML := range re.FindAllString(page, -1) {
		openTag := strings.SplitN(formHTML, ">", 2)[0]
		attrs := extractTagAttrs(openTag)
		if strings.EqualFold(attrs["id"], formID) {
			return formHTML
		}
	}
	return ""
}

func extractFormActionByID(page, formID string) string {
	formHTML := extractFormHTML(page, formID)
	if formHTML == "" {
		return ""
	}
	return extractFormAction(formHTML)
}

func extractHiddenInputs(page string) map[string]string {
	fields := map[string]string{}
	re := regexp.MustCompile(`(?is)<input\b[^>]*>`)
	for _, tag := range re.FindAllString(page, -1) {
		attrs := extractTagAttrs(tag)
		if strings.ToLower(attrs["type"]) != "hidden" {
			continue
		}
		name := attrs["name"]
		if name == "" {
			continue
		}
		fields[name] = attrs["value"]
	}
	return fields
}

func extractFormFields(page, formID string) map[string]string {
	formHTML := extractFormHTML(page, formID)
	if formHTML == "" {
		return map[string]string{}
	}
	fields := map[string]string{}
	re := regexp.MustCompile(`(?is)<input\b[^>]*>`)
	for _, tag := range re.FindAllString(formHTML, -1) {
		attrs := extractTagAttrs(tag)
		name := attrs["name"]
		if name == "" {
			continue
		}
		inputType := strings.ToLower(attrs["type"])
		if inputType == "" {
			inputType = "text"
		}
		if inputType == "hidden" {
			fields[name] = attrs["value"]
		} else if (inputType == "radio" || inputType == "checkbox") && strings.Contains(strings.ToLower(tag), "checked") {
			fields[name] = attrs["value"]
		}
	}
	return fields
}

func extractSkipURL(page string) string {
	m := regexp.MustCompile(`(?is)"skip"\s*:\s*\{\s*"url"\s*:\s*"([^"]+)"`).FindStringSubmatch(page)
	if len(m) < 2 {
		return ""
	}
	urlValue := m[1]
	urlValue = strings.ReplaceAll(urlValue, `\u002f`, `/`)
	urlValue = strings.ReplaceAll(urlValue, `\u0026`, "&")
	urlValue = strings.ReplaceAll(urlValue, `\/`, `/`)
	return urlValue
}

func getQueryParam(rawURL, key string) string {
	parts, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for name, values := range parts.Query() {
		if strings.EqualFold(name, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func postEmptyForm(session *Session, rawURL string, referer ...string) (*HTTPResponse, error) {
	headers := navHeaders(session, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if len(referer) > 0 && referer[0] != "" {
		headers["Referer"] = referer[0]
	}
	resp, err := session.Post(rawURL, requestOptions{
		Data:              map[string]string{},
		Headers:           headers,
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return nil, wrapAuthError(fmt.Sprintf("空表单提交请求异常: %s", err), AuthStatusRequestError, err)
	}
	return resp, nil
}

func isInterruptPage(rawURL string) bool {
	low := strings.ToLower(rawURL)
	return strings.Contains(low, "account.live.com") && (strings.Contains(low, "/proofs/") || strings.Contains(low, "/interrupt/") || strings.Contains(low, "/identity/") || strings.Contains(low, "/cancel"))
}

func accountVerificationPageKinds(page, rawURL, action string) (identity, proofs bool) {
	lowURL := strings.ToLower(rawURL)
	resolvedAction := action
	if resolvedAction != "" && resolvedAction != "#" {
		resolvedAction = resolveURL(rawURL, resolvedAction)
	}
	identity = strings.Contains(lowURL, "/identity/") ||
		(strings.Contains(page, "apiCanary") && strings.Contains(page, "rawProofList"))
	proofs = strings.Contains(lowURL, "/proofs/") ||
		strings.Contains(strings.ToLower(resolvedAction), "/proofs/") ||
		isAddEmailPage(page, resolvedAction)
	return identity, proofs
}

func isAccountRecoverURL(rawURL string) bool {
	low := strings.ToLower(rawURL)
	return strings.Contains(low, "account.live.com") && strings.Contains(low, "/recover")
}

func isAutoSubmitPage(page, action string) bool {
	return action != "" && strings.Contains(strings.ToLower(action), "account.live.com") && strings.Contains(page, "DoSubmit")
}

func identityBoundMailbox(ctx context.Context, page, email, proxy string, proofData []ProofData, preferredBindingAddress string) string {
	proofs := extractIdentityProofs(page)
	ourProof := findOurIdentityProof(proofs)
	maskedEmail := asString(ourProof["name"])
	if maskedEmail == "" {
		for _, proof := range proofData {
			if proof.Type == 1 {
				maskedEmail = proof.Display
				break
			}
		}
	}
	if maskedEmail == "" {
		maskedEmail = detectExistingProofInPage(page)
	}
	if maskedEmail != "" && isProjectMailboxAddress(maskedEmail) {
		if resolvedMailbox := lookupRealMailbox(ctx, maskedEmail, email, proxy, preferredBindingAddress); resolvedMailbox != "" {
			return resolvedMailbox
		}
		return maskedEmail
	}
	return ""
}

func identityBoundDisplay(page string, proofData []ProofData) string {
	proofs := extractIdentityProofs(page)
	ourProof := findOurIdentityProof(proofs)
	maskedEmail := asString(ourProof["name"])
	if maskedEmail == "" {
		for _, proof := range proofData {
			if proof.Type == 1 {
				maskedEmail = proof.Display
				break
			}
		}
	}
	if maskedEmail != "" {
		return maskedEmail
	}
	return detectExistingProofInPage(page)
}

func alreadyBoundError(display, boundMailbox string) *AuthError {
	display = strings.TrimSpace(firstNonEmpty(display, boundMailbox))
	message := "已绑定辅助邮箱"
	if display != "" {
		message = fmt.Sprintf("已绑定辅助邮箱(%s)", display)
	}
	return &AuthError{
		Message:      message,
		Status:       AuthStatusAlreadyBound,
		BoundMailbox: boundMailbox,
		BoundDisplay: display,
	}
}

func handleIdentityPage(session *Session, page, rawURL, proxy, email string, proofData []ProofData, allowExistingProofVerification bool, preferredBindingAddress string) (string, string, string, error) {
	skipURL := extractSkipURL(page)
	if skipURL != "" && strings.Contains(skipURL, "res=success") {
		resp, err := postEmptyForm(session, skipURL, rawURL)
		if err != nil {
			return "", "", "", err
		}
		return resp.Body, resp.URL, "", nil
	}
	otherProof := detectExistingProofInPage(page)
	if otherProof != "" && !isProjectMailboxAddress(otherProof) {
		logWarning("identity 页: 已绑定辅助邮箱 %s, 跳过", otherProof)
		return "", "", "", alreadyBoundError(otherProof, "")
	}

	if len(proofData) > 0 || len(extractIdentityProofs(page)) > 0 {
		if !allowExistingProofVerification {
			boundMailbox := identityBoundMailbox(session.context(), page, email, proxy, proofData, preferredBindingAddress)
			if boundMailbox != "" && isProjectMailboxAddress(boundMailbox) {
				logInfo("identity 页: 已绑定本项目辅助邮箱, 继续接码验证")
				return handleOTPVerification(session, page, rawURL, email, proxy, proofData, preferredBindingAddress)
			}
			display := firstNonEmpty(boundMailbox, identityBoundDisplay(page, proofData))
			logInfo("identity 页: 账号已绑定辅助邮箱, 跳过")
			return "", "", "", alreadyBoundError(display, boundMailbox)
		}
		return handleOTPVerification(session, page, rawURL, email, proxy, proofData, preferredBindingAddress)
	}
	if skipURL != "" && strings.Contains(skipURL, "res=cancel") {
		if allowExistingProofVerification {
			return "", "", "", newAuthError("Microsoft identity verification has no usable proof.", AuthStatusVerifyCodeError)
		}
		resp, err := postEmptyForm(session, skipURL, rawURL)
		if err != nil {
			return "", "", "", err
		}
		return resp.Body, resp.URL, "", nil
	}

	logWarning("identity 页无法处理 (无 proof_data), 页面关键词: %v", pageKeywordSubset(page, []string{"SendOtt", "VerifyCode", "AddProof", "EmailAddress", "iOttText", "验证", "代码", "安全代码"}))
	return "", "", "", newAuthError("需要辅助邮箱验证码 (identity 页无法处理)", AuthStatusUnknownMailbox)
}

func handleOTPVerification(session *Session, page, rawURL, email, proxy string, proofData []ProofData, preferredBindingAddress string) (string, string, string, error) {
	apiCanary := extractConfigString(page, "apiCanary")
	token := extractConfigString(page, "token")
	purpose := extractConfigString(page, "proofPurpose")
	if purpose == "" {
		purpose = "UnfamiliarLocationHard"
	}
	returnURL := firstNonEmpty(extractReturnURL(page), extractSkipURL(page))
	proofs := extractIdentityProofs(page)
	ourProof := findOurIdentityProof(proofs)
	if len(ourProof) == 0 {
		for _, proof := range proofs {
			if asString(proof["type"]) == "Email" && asString(proof["name"]) != "" {
				return "", "", "", alreadyBoundError(asString(proof["name"]), "")
			}
		}
		for _, proof := range proofData {
			if proof.Type == 1 && proof.Display != "" {
				return "", "", "", newAuthError(fmt.Sprintf("identity 页缺少可用 proof, GetCredentialType 中有 %s", proof.Display))
			}
		}
		return "", "", "", newAuthError("身份验证需要验证码但找不到可用的 proof, 跳过", AuthStatusUnknownMailbox)
	}

	maskedEmail := asString(ourProof["name"])
	epid := asString(ourProof["epid"])
	if apiCanary == "" || epid == "" || returnURL == "" {
		return "", "", "", newAuthError("OTP 验证: identity 页缺少 apiCanary/epid/return_url")
	}
	logInfo("OTP 验证: 找到可用辅助邮箱 proof")
	logDebug("OTP 验证: masked_email=%s", maskedEmail)

	realMailbox := lookupRealMailbox(session.context(), maskedEmail, email, proxy, preferredBindingAddress)
	if realMailbox == "" {
		return "", "", "", newAuthError(fmt.Sprintf("找不到 %s 对应的真实邮箱, 跳过", maskedEmail), AuthStatusUnknownMailbox)
	}
	logInfo("OTP 验证: 已匹配真实邮箱")
	logDebug("OTP 验证: real_mailbox=%s", realMailbox)

	seenKeys, err := snapshotMailboxKeys(session.context(), realMailbox, proxy)
	if err != nil {
		return "", "", "", wrapAuthError(fmt.Sprintf("读取验证码邮箱基线失败: %s", err), AuthStatusRequestError, err, realMailbox)
	}
	var code string
	var lastErr error
	for ottAttempt := 1; ottAttempt <= 3; ottAttempt++ {
		if ottAttempt > 1 {
			if err := session.sleep(2 * time.Second); err != nil {
				return "", "", "", wrapAuthError(fmt.Sprintf("OTP 重试取消: %s", err), AuthStatusRequestError, err)
			}
			seenKeys, err = snapshotMailboxKeys(session.context(), realMailbox, proxy)
			if err != nil {
				return "", "", "", wrapAuthError(fmt.Sprintf("刷新验证码邮箱基线失败: %s", err), AuthStatusRequestError, err, realMailbox)
			}
			logInfo("OTP 重试 #%d: 重新发送验证码 (排除旧邮件)", ottAttempt)
		}
		watcher := startCodeWatcher(session.context(), realMailbox, proxy, 0, seenKeys)
		resp, err := session.Post("https://account.live.com/API/Proofs/SendOtt", requestOptions{
			JSON: map[string]any{
				"token":                  token,
				"purpose":                purpose,
				"epid":                   epid,
				"autoVerification":       false,
				"autoVerificationFailed": false,
				"confirmProof":           realMailbox,
				"HFId":                   "",
				"HId":                    "",
				"HSId":                   "",
				"HSol":                   "",
				"HType":                  "",
				"HPId":                   "",
			},
			Headers: corsHeaders(session, map[string]string{
				"Content-Type": "application/json; charset=utf-8",
				"Origin":       "https://account.live.com",
				"Referer":      rawURL,
				"canary":       apiCanary,
			}),
		})
		if err != nil {
			return "", "", "", wrapAuthError(fmt.Sprintf("SendOtt 请求异常: %s", err), AuthStatusRequestError, err, realMailbox)
		}
		logInfo("SendOtt 响应: status=%d", resp.StatusCode)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", "", "", newAuthError(fmt.Sprintf("SendOtt 请求失败 (HTTP %d)", resp.StatusCode), AuthStatusRequestError)
		}
		var result map[string]any
		if err := resp.JSON(&result); err != nil {
			return "", "", "", newAuthError(fmt.Sprintf("SendOtt 返回非 JSON (HTTP %d)", resp.StatusCode))
		}
		if errMap := asMap(result["error"]); errMap != nil {
			return "", "", "", newAuthError(fmt.Sprintf("SendOtt 失败: code=%s", asString(errMap["code"])))
		}
		if value := asString(result["apiCanary"]); value != "" {
			apiCanary = value
		}
		logInfo("OTP 验证: 验证码已发送 (第%d次)", ottAttempt)

		code, err = watcher.getCode(0)
		if err == nil {
			logInfo("OTP 验证: 收到验证码")
			lastErr = nil
			break
		}
		lastErr = err
		authErr, _ := err.(*AuthError)
		if ottAttempt < 3 && authErr != nil && authErr.Status == AuthStatusCodeTimeout {
			logWarning("OTP 收码超时 (第%d次), 准备重试", ottAttempt)
			continue
		}
		status := AuthStatusRequestError
		if authErr != nil && authErr.Status != "" {
			status = authErr.Status
		}
		return "", "", "", newAuthError(fmt.Sprintf("OTP 验证收码失败 (%s): %s", maskedEmail, err), status, realMailbox)
	}
	if lastErr != nil {
		status := AuthStatusRequestError
		if authErr, ok := lastErr.(*AuthError); ok && authErr.Status != "" {
			status = authErr.Status
		}
		return "", "", "", newAuthError(fmt.Sprintf("OTP 验证收码失败 (%s): %s", maskedEmail, lastErr), status, realMailbox)
	}

	resp, err := session.Post("https://account.live.com/API/Proofs/VerifyCode", requestOptions{
		JSON: map[string]any{
			"code":         code,
			"action":       "IptVerify",
			"purpose":      purpose,
			"epid":         epid,
			"confirmProof": realMailbox,
		},
		Headers: corsHeaders(session, map[string]string{
			"Content-Type": "application/json; charset=utf-8",
			"Origin":       "https://account.live.com",
			"Referer":      rawURL,
			"canary":       apiCanary,
		}),
	})
	if err != nil {
		return "", "", "", wrapAuthError(fmt.Sprintf("VerifyCode 请求异常: %s", err), AuthStatusRequestError, err, realMailbox)
	}
	logInfo("OTP 验证码提交响应: status=%d", resp.StatusCode)
	logDebug("OTP 验证码提交响应 url=%s", resp.URL)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", newAuthError(fmt.Sprintf("VerifyCode 请求失败 (HTTP %d)", resp.StatusCode), AuthStatusRequestError, realMailbox)
	}
	var verifyResult map[string]any
	if err := resp.JSON(&verifyResult); err != nil {
		return "", "", "", newAuthError(fmt.Sprintf("VerifyCode 返回非 JSON (HTTP %d)", resp.StatusCode), AuthStatusRequestError, realMailbox)
	}
	if rawError, exists := verifyResult["error"]; exists && rawError != nil {
		errMap := asMap(rawError)
		code := asString(rawError)
		message := ""
		if errMap != nil {
			code = asString(errMap["code"])
			message = firstNonEmpty(asString(errMap["message"]), asString(errMap["description"]))
		}
		logWarning("VerifyCode 失败: code=%s message=%s", code, message)
		logDebug("VerifyCode 失败响应 keys=%v", sortedAnyKeys(verifyResult))
		return "", "", "", newAuthError(fmt.Sprintf("VerifyCode 失败: code=%s, message=%s", code, message), AuthStatusVerifyCodeError, realMailbox)
	}
	route := asString(verifyResult["route"])
	finalURL := returnURL
	if route != "" {
		finalURL = appendQueryParam(returnURL, "route", route)
	}
	logInfo("OTP 验证成功")
	logDebug("OTP 验证成功, return_url=%s", finalURL)
	resp, err = requestWithRetryGet(session, finalURL, "OTP 验证成功后跳转", realMailbox, requestOptions{
		Headers:           navHeaders(session, map[string]string{"Referer": rawURL}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", "", err
	}
	return resp.Body, resp.URL, realMailbox, nil
}

func extractConfigString(page, key string) string {
	pattern := fmt.Sprintf(`(?is)"%s"\s*:\s*"((?:\\.|[^"\\])*)"`, regexp.QuoteMeta(key))
	m := regexp.MustCompile(pattern).FindStringSubmatch(page)
	if len(m) < 2 {
		return ""
	}
	return jsonDecodeString(m[1])
}

func extractIdentityProofs(page string) []map[string]any {
	raw := extractConfigString(page, "rawProofList")
	if raw == "" {
		return nil
	}
	var data []map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		logWarning("identity 页 rawProofList 解析失败: %s", err)
		return nil
	}
	return data
}

func findOurIdentityProof(proofs []map[string]any) map[string]any {
	for _, proof := range proofs {
		if isProjectMailboxAddress(asString(proof["name"])) {
			return proof
		}
	}
	return nil
}

func extractReturnURL(page string) string {
	m := regexp.MustCompile(`(?is)"return"\s*:\s*\{\s*"url"\s*:\s*"((?:\\.|[^"\\])*)"`).FindStringSubmatch(page)
	if len(m) < 2 {
		return ""
	}
	return jsonDecodeString(m[1])
}

func appendQueryParam(rawURL, key, value string) string {
	parts, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parts.Query()
	query.Del(key)
	query.Add(key, value)
	parts.RawQuery = query.Encode()
	return parts.String()
}

func extractPageHint(page string, limit int) string {
	if limit <= 0 {
		limit = 220
	}
	var snippets []string
	if m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(page); len(m) > 1 {
		snippets = append(snippets, m[1])
	}
	lowered := strings.ToLower(page)
	for _, keyword := range []string{"验证码", "安全代码", "一次性代码", "验证", "错误", "不正确", "verify", "verification", "security code", "incorrect", "error"} {
		index := strings.Index(lowered, strings.ToLower(keyword))
		if index >= 0 {
			start := index - 90
			if start < 0 {
				start = 0
			}
			end := index + 180
			if end > len(page) {
				end = len(page)
			}
			snippets = append(snippets, page[start:end])
		}
	}
	if len(snippets) == 0 {
		return ""
	}
	text := strings.Join(snippets, " ")
	text = regexp.MustCompile(`(?is)<script\b.*?</script>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<style\b.*?</style>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = regexp.MustCompile(`https?://\S+`).ReplaceAllString(text, "[url]")
	text = regexp.MustCompile(`[\w.+-]+@[\w.-]+`).ReplaceAllString(text, "[email]")
	text = regexp.MustCompile(`\b\d{4,8}\b`).ReplaceAllString(text, "[code]")
	text = regexp.MustCompile(`[A-Za-z0-9+/_%=-]{80,}`).ReplaceAllString(text, "[token]")
	text = strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(text, " "))
	if len(text) > limit {
		return text[:limit]
	}
	return text
}

func accountEmailMask(accountEmail string) string {
	local, domain, ok := strings.Cut(accountEmail, "@")
	if !ok || local == "" || domain == "" {
		return ""
	}
	if len(local) >= 3 {
		return fmt.Sprintf("%s**%s@%s", local[:2], local[len(local)-1:], domain)
	}
	if len(local) == 2 {
		return fmt.Sprintf("%s**%s@%s", local[:1], local[1:], domain)
	}
	return local + "**@" + domain
}

func accountEmailMaskPattern(accountEmail string) *regexp.Regexp {
	local, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(accountEmail)), "@")
	if !ok || local == "" || domain == "" {
		return nil
	}
	prefix := local
	suffix := ""
	if len(local) >= 3 {
		prefix = local[:2]
		suffix = local[len(local)-1:]
	} else if len(local) == 2 {
		prefix = local[:1]
		suffix = local[1:]
	}
	return regexp.MustCompile(
		`(?i)` + regexp.QuoteMeta(prefix) + `\*+` + regexp.QuoteMeta(suffix+"@"+domain),
	)
}

func lookupRealMailbox(ctx context.Context, maskedEmail, accountEmail, proxy string, preferredBindingAddress string) string {
	// Preferred (import-supplied) binding address wins if it matches the mask —
	// domain-agnostic; may even be an operator-recorded external address.
	if preferred := strings.ToLower(strings.TrimSpace(preferredBindingAddress)); preferred != "" && mailboxMatchesMasked(maskedEmail, preferred) {
		logInfo("通过导入输入找到当前账号真实辅助邮箱")
		logDebug("导入输入匹配: account=%s, real_mailbox=%s", accountEmail, preferred)
		return preferred
	}
	// Decide which binding domains to resolve against. If the masked proof
	// already carries a domain, only that domain is relevant AND it must be one
	// of ours (∈ binding list) — an external domain returns "" so the caller
	// records the masked display instead. Otherwise MATCH against ALL binding
	// domains (the account's recovery could live on any of them).
	var domains []string
	if strings.Contains(maskedEmail, "@") {
		d := strings.ToLower(strings.SplitN(maskedEmail, "@", 2)[1])
		if !domainInProject(d) {
			return ""
		}
		domains = []string{d}
	} else {
		domains = activeAuxiliaryDomains()
	}
	var candidates []string
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		addr := strings.ToLower(strings.TrimSpace(resolveRealMailboxForDomain(ctx, maskedEmail, accountEmail, proxy, domain)))
		if addr == "" {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		candidates = append(candidates, addr)
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	if len(candidates) > 1 {
		logWarning("辅助邮箱掩码匹配到多个域候选, 保持未解析: account=%s, candidates=%v", accountEmail, firstN(candidates, 5))
	}
	return ""
}

// resolveRealMailboxForDomain resolves the account's real recovery mailbox on a
// SINGLE binding domain: deterministic rule → recorded output → content search
// by account mask → API search by masked prefix. Returns "" if none match.
func resolveRealMailboxForDomain(ctx context.Context, maskedEmail, accountEmail, proxy, domain string) string {
	prefix := ""
	if m := regexp.MustCompile(`(?i)^([a-z0-9]{1,5})\*+`).FindStringSubmatch(maskedEmail); len(m) > 1 {
		prefix = m[1]
	} else {
		prefix = strings.Split(maskedEmail, "*")[0]
	}
	prefix = regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(strings.ToLower(prefix), "")
	accountKey := strings.ToLower(strings.TrimSpace(accountEmail))

	if recorded := lookupRecordedMailboxForAccount(accountKey, domain); recorded != "" && mailboxMatchesMasked(maskedEmail, recorded) {
		logInfo("通过输出记录找到当前账号真实辅助邮箱")
		logDebug("输出记录匹配: account=%s, real_mailbox=%s", accountEmail, recorded)
		return recorded
	}

	// Recent imports use the Microsoft local part verbatim on the selected
	// binding domain. A broad account-mask search is often ambiguous because
	// many accounts share the same first/last characters (for example
	// br*****1@...). Prefer the exact account-local mailbox only when the local
	// receiving store already contains a message addressed to that mailbox whose
	// body names this exact account mask. This is evidence, not a guessed binding:
	// callers must still complete the normal password/OTP confirmation before
	// persisting the relationship as verified.
	if direct := accountLocalAuxiliaryAddressForDomain(accountEmail, domain); direct != "" &&
		mailboxMatchesMasked(maskedEmail, direct) &&
		mailboxHasAccountMaskEvidence(ctx, direct, accountEmail, proxy) {
		logInfo("通过账号同名辅助邮箱的精确收件证据找到真实辅助邮箱")
		logDebug("账号同名收件证据匹配: account=%s, real_mailbox=%s", accountEmail, direct)
		return direct
	}

	if mask := accountEmailMask(accountEmail); mask != "" {
		var found string
		ambiguous := false
		func() {
			defer func() {
				if recover() != nil {
					logWarning("按账号掩码搜索临时邮箱失败")
				}
			}()
			results := searchMailboxesByContent(ctx, mask, proxy)
			var candidates []string
			seenCandidates := make(map[string]struct{}, len(results))
			for _, addr := range results {
				addr = strings.ToLower(strings.TrimSpace(addr))
				matchesFullMask := !strings.Contains(maskedEmail, "@") || mailboxMatchesMasked(maskedEmail, addr)
				if addr != "" && strings.HasSuffix(addr, "@"+domain) &&
					(prefix == "" || strings.HasPrefix(addr, prefix)) && matchesFullMask {
					if _, exists := seenCandidates[addr]; exists {
						continue
					}
					seenCandidates[addr] = struct{}{}
					candidates = append(candidates, addr)
				}
			}
			if len(candidates) == 1 {
				logInfo("通过邮件正文账号掩码找到真实辅助邮箱")
				logDebug("正文掩码匹配: account=%s, mask=%s, real_mailbox=%s", accountEmail, mask, candidates[0])
				found = candidates[0]
			} else if len(candidates) > 1 {
				logWarning("正文掩码匹配到多个邮箱, 保持未解析: account=%s, mask=%s, candidates=%v", accountEmail, mask, firstN(candidates, 5))
				ambiguous = true
			}
		}()
		if ambiguous {
			return ""
		}
		if found != "" {
			return found
		}
	}

	if recorded := lookupRecordedMailboxByPrefix(prefix, domain); recorded != "" && mailboxMatchesMasked(maskedEmail, recorded) {
		logInfo("通过输出记录找到掩码邮箱对应真实地址")
		logDebug("输出记录前缀匹配: masked_email=%s, real_mailbox=%s", maskedEmail, recorded)
		return recorded
	}

	results := searchMailboxes(ctx, prefix, proxy)
	var candidates []string
	seenCandidates := make(map[string]struct{}, len(results))
	for _, result := range results {
		addr := strings.ToLower(strings.TrimSpace(result))
		matchesFullMask := !strings.Contains(maskedEmail, "@") || mailboxMatchesMasked(maskedEmail, addr)
		if strings.HasPrefix(addr, prefix) && strings.HasSuffix(addr, "@"+domain) && matchesFullMask {
			if _, exists := seenCandidates[addr]; exists {
				continue
			}
			seenCandidates[addr] = struct{}{}
			candidates = append(candidates, addr)
		}
	}
	if len(candidates) == 1 {
		logInfo("通过 API 找到掩码邮箱对应真实地址")
		logDebug("掩码邮箱匹配: masked_email=%s, real_mailbox=%s", maskedEmail, candidates[0])
		return candidates[0]
	}
	if len(candidates) > 1 {
		logWarning("掩码邮箱匹配到多个候选, 保持未解析: masked_email=%s, candidates=%v", maskedEmail, firstN(candidates, 5))
		return ""
	}

	// Deterministic naming is a useful last-resort candidate, but it is not
	// stronger than account-associated historical evidence. Returning it only
	// after the evidence searches prevents a guessed mask match from hiding a
	// different concrete mailbox already present in the receiving system. The
	// caller must still prove this candidate through the normal OTP login before
	// persisting it as verified.
	if generated, err := deterministicAuxiliaryAddressForDomain(accountEmail, domain); err == nil && mailboxMatchesMasked(maskedEmail, generated) {
		logInfo("通过辅助邮箱生成规则匹配候选地址")
		logDebug("生成规则候选: account=%s, masked_email=%s, candidate=%s", accountEmail, maskedEmail, generated)
		return generated
	}
	return ""
}

func accountLocalAuxiliaryAddressForDomain(accountEmail, domain string) string {
	accountEmail = strings.ToLower(strings.TrimSpace(accountEmail))
	domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
	local, sourceDomain, ok := strings.Cut(accountEmail, "@")
	if !ok || local == "" || sourceDomain == "" || domain == "" {
		return ""
	}
	return normalizeRecoveryMailbox(local + "@" + domain)
}

func mailboxHasAccountMaskEvidence(ctx context.Context, mailbox, accountEmail, proxy string) bool {
	mailbox = normalizeRecoveryMailbox(mailbox)
	maskPattern := accountEmailMaskPattern(accountEmail)
	if mailbox == "" || maskPattern == nil {
		return false
	}
	emails, err := mailList(ctx, mailbox, proxy, 10, false)
	if err != nil {
		return false
	}
	for _, email := range emails {
		if !recoveryMailboxEvidenceMatches(mailbox, email.To) {
			continue
		}
		if maskPattern.MatchString(email.Subject + " " + email.Preview) {
			return true
		}
	}
	return false
}

func resultRecordFiles() []string {
	return nil
}

func lookupRecordedMailboxForAccount(accountKey, domain string) string {
	if accountKey == "" {
		return ""
	}
	var matches []string
	for _, filePath := range resultRecordFiles() {
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		for _, rawLine := range strings.Split(string(data), "\n") {
			sourceEmail, mailbox := parseRecordedMailboxLine(rawLine, domain)
			if sourceEmail == accountKey && mailbox != "" {
				matches = append(matches, mailbox)
			}
		}
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func lookupRecordedMailboxByPrefix(prefix, domain string) string {
	if len(prefix) < 4 {
		return ""
	}
	var matches []string
	for _, filePath := range resultRecordFiles() {
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		for _, rawLine := range strings.Split(string(data), "\n") {
			_, mailbox := parseRecordedMailboxLine(rawLine, domain)
			if mailbox != "" && strings.HasPrefix(mailbox, prefix) {
				matches = append(matches, mailbox)
			}
		}
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func parseRecordedMailboxLine(line, domain string) (string, string) {
	line = stripProgressPrefix(strings.TrimSpace(line))
	if line == "" {
		return "", ""
	}
	if strings.Contains(line, ">") {
		line = strings.TrimSpace(strings.SplitN(line, ">", 2)[0])
	}
	parts := strings.Split(line, "----")
	if len(parts) < 3 {
		return "", ""
	}
	sourceEmail := strings.ToLower(strings.TrimSpace(parts[0]))
	candidates := []string{}
	if len(parts) >= 5 {
		candidates = append(candidates, parts[4])
	}
	candidates = append(candidates, parts[2])
	for _, candidate := range candidates {
		mailbox := strings.ToLower(strings.TrimSpace(candidate))
		if mailbox != "" && strings.HasSuffix(mailbox, "@"+domain) {
			return sourceEmail, mailbox
		}
	}
	return sourceEmail, ""
}

func cleanProofDisplay(value string) string {
	value = strings.TrimSpace(html.UnescapeString(value))
	value = regexp.MustCompile(`[<\s]`).Split(value, 2)[0]
	return strings.Trim(value, " \"'<>.,;:，。；：")
}

func detectExistingProofInPage(page string) string {
	patterns := []string{
		`我们将向\s+(\S+@\S+)`,
		`[Ww]e.{0,5}ll send a code to\s+(\S+@\S+)`,
		`(\S{1,3}\*{2,}\S{0,3}@\S+)`,
	}
	for _, pattern := range patterns {
		if m := regexp.MustCompile(`(?is)` + pattern).FindStringSubmatch(page); len(m) > 1 {
			return cleanProofDisplay(m[1])
		}
	}
	return ""
}

func handlePasskeyInterrupt(session *Session, page, rawURL string) (string, string, bool, error) {
	skipURL := extractSkipURL(page)
	if skipURL == "" {
		if m := regexp.MustCompile(`(?i)[?&]ru=([^&]+)`).FindStringSubmatch(rawURL); len(m) > 1 {
			if decoded, err := url.QueryUnescape(m[1]); err == nil {
				skipURL = decoded
			}
		}
	}
	if skipURL != "" && strings.HasPrefix(skipURL, "http") {
		resp, err := requestWithRetryGet(session, skipURL, "passkey 跳过", "", requestOptions{
			Headers: navHeaders(session, nil),
		})
		if err != nil {
			return "", "", false, err
		}
		return resp.Body, resp.URL, true, nil
	}
	return "", "", false, nil
}

func extractEmailProofValue(page, mailbox string) string {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	if mailbox == "" {
		return ""
	}
	formHTML := extractFormHTML(page, "frmVerifyProof")
	if formHTML == "" {
		formHTML = page
	}
	inputTags := regexp.MustCompile(`(?is)<input\b[^>]*>`).FindAllString(formHTML, -1)
	for _, preferredName := range []string{"iProofOptions", "proof"} {
		for _, tag := range inputTags {
			attrs := extractTagAttrs(tag)
			name := attrs["name"]
			value := attrs["value"]
			if name == preferredName && strings.Contains(strings.ToLower(value), mailbox) && strings.Contains(strings.ToLower(value), "||email||") {
				return value
			}
		}
	}
	return ""
}

func submitSLTFormIfPresent(session *Session, page, rawURL, boundMailbox, label string) (string, string, bool, error) {
	sltFields := extractFormFields(page, "frmSubmitSLT")
	sltAction := extractFormActionByID(page, "frmSubmitSLT")
	if sltFields["slt"] == "" || sltAction == "" {
		return page, rawURL, false, nil
	}
	sltAction = resolveURL(rawURL, sltAction)
	logInfo("%s accepted, submitting SLT form", label)
	logDebug("SLT form action=%s fields=%v", sltAction, sortedKeys(sltFields))
	resp, err := session.Post(sltAction, requestOptions{
		Data: sltFields,
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://account.live.com",
			"Referer":      "https://account.live.com/",
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", false, wrapAuthError(fmt.Sprintf("SLT submit 请求异常: %s", err), AuthStatusRequestError, err, boundMailbox)
	}
	logInfo("SLT submit response: status=%d", resp.StatusCode)
	logDebug("SLT submit response url=%s", resp.URL)
	return resp.Body, resp.URL, true, nil
}

func bindAuxiliaryEmail(session *Session, page, rawURL, proxy, accountEmail string, preferredBindingAddress string) (string, string, string, error) {
	action := extractFormAction(page)
	if action == "" || action == "#" {
		return "", "", "", newAuthError("绑定页无法解析 form")
	}
	action = resolveURL(rawURL, action)
	if !strings.HasPrefix(action, "http") {
		return "", "", "", newAuthError("绑定页无法解析 form")
	}
	canary := extractHiddenInputs(page)["canary"]
	if canary == "" {
		if m := regexp.MustCompile(`(?is)name="canary"[^>]*value="([^"]*)"`).FindStringSubmatch(page); len(m) > 1 {
			canary = m[1]
		}
	}

	tempMail, err := createTempMailbox(session.context(), accountEmail, preferredBindingAddress)
	if err != nil {
		return "", "", "", err
	}
	logDebug("已创建临时邮箱: %s (代理: %s)", tempMail, firstNonEmpty(proxy, "默认"))
	seenKeys, err := snapshotMailboxKeys(session.context(), tempMail, proxy)
	if err != nil {
		return "", "", "", wrapAuthError(fmt.Sprintf("读取辅助邮箱基线失败: %s", err), AuthStatusRequestError, err, tempMail)
	}
	watcher := startCodeWatcher(session.context(), tempMail, proxy, 0, seenKeys)
	logDebug("已启动后台收码线程, 监听邮箱: %s", tempMail)

	logInfo("提交临时邮箱到 AddProof")
	logDebug("AddProof: mailbox=%s, action=%s", tempMail, action)
	resp, err := session.Post(action, requestOptions{
		Data: map[string]string{
			"iProofOptions":          "Email",
			"DisplayPhoneCountryISO": "CN",
			"DisplayPhoneNumber":     "",
			"EmailAddress":           tempMail,
			"canary":                 canary,
			"action":                 "AddProof",
			"PhoneNumber":            "",
			"PhoneCountryISO":        "",
		},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://account.live.com",
			"Referer":      rawURL,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", "", wrapAuthError(fmt.Sprintf("AddProof 请求异常: %s", err), AuthStatusRequestError, err, tempMail)
	}
	verifyPage := resp.Body
	verifyURL := resp.URL
	logDebug("AddProof 响应 url=%s", verifyURL)

	verifyAction := extractFormAction(verifyPage)
	if verifyAction == "" || !strings.HasPrefix(verifyAction, "http") {
		verifyAction = verifyURL
	}
	verifyFields := extractFormFields(verifyPage, "frmVerifyProof")
	if len(verifyFields) == 0 {
		verifyFields = extractHiddenInputs(verifyPage)
	}
	delete(verifyFields, "slt")
	verifyCanary := firstNonEmpty(verifyFields["canary"], canary)
	if verifyCanary == "" {
		if m := regexp.MustCompile(`(?is)name="canary"[^>]*value="([^"]*)"`).FindStringSubmatch(verifyPage); len(m) > 1 {
			verifyCanary = firstNonEmpty(m[1], canary)
		}
	}

	code, err := watcher.getCode(0)
	if err != nil {
		logError("获取验证码失败: %s", err)
		logDebug("获取验证码失败邮箱: %s", tempMail)
		status := AuthStatusRequestError
		if authErr, ok := err.(*AuthError); ok && authErr.Status != "" {
			status = authErr.Status
		}
		return "", "", "", newAuthError(err.Error(), status, tempMail)
	}
	logInfo("获取到验证码")

	logInfo("提交验证码到 VerifyProof")
	logDebug("VerifyProof: action=%s", verifyAction)
	if proofValue := extractEmailProofValue(verifyPage, tempMail); proofValue != "" {
		verifyFields["iProofOptions"] = proofValue
	} else if verifyFields["iProofOptions"] == "" {
		verifyFields["iProofOptions"] = fmt.Sprintf("OTT||%s||Email||0||c", tempMail)
		logWarning("VerifyProof iProofOptions missing, using fallback")
	}
	delete(verifyFields, "proof")
	verifyFields["iOttText"] = code
	verifyFields["action"] = "VerifyProof"
	verifyFields["canary"] = verifyCanary
	if _, ok := verifyFields["GeneralVerify"]; !ok {
		verifyFields["GeneralVerify"] = "0"
	}
	logDebug("VerifyProof fields=%v", sortedKeys(verifyFields))
	resp, err = session.Post(verifyAction, requestOptions{
		Data: verifyFields,
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://account.live.com",
			"Referer":      verifyURL,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", "", wrapAuthError(fmt.Sprintf("VerifyProof 请求异常: %s", err), AuthStatusRequestError, err, tempMail)
	}
	verifyResponseURL := resp.URL
	logInfo("VerifyProof 响应: status=%d", resp.StatusCode)
	logDebug("VerifyProof 响应 url=%s", verifyResponseURL)
	sltPage, sltURL, sltSubmitted, err := submitSLTFormIfPresent(session, resp.Body, verifyResponseURL, tempMail, "VerifyProof")
	if err != nil {
		return "", "", "", err
	}
	if sltSubmitted {
		return sltPage, sltURL, tempMail, nil
	}
	if strings.Contains(strings.ToLower(verifyResponseURL), "/proofs/verify") {
		hint := extractPageHint(resp.Body, 220)
		if hint != "" {
			logWarning("VerifyProof 后仍停留验证页: %s", hint)
		} else {
			logWarning("VerifyProof 后仍停留验证页")
		}
		return "", "", "", newAuthError(fmt.Sprintf("VerifyProof failed: %s", firstNonEmpty(hint, "still on verify page")), AuthStatusCodeTimeout, tempMail)
	}
	return resp.Body, resp.URL, tempMail, nil
}

func isAddEmailPage(page, action string) bool {
	return (strings.Contains(page, "EmailAddress") || strings.Contains(page, "AddProof") || strings.Contains(page, "备用电子邮件")) && action != "" && action != "#" && strings.HasPrefix(action, "http")
}

func trySkipProofsPage(session *Session, page, rawURL, action string) (string, string, error) {
	fields := extractHiddenInputs(page)
	if fields["iProofOptions"] == "" {
		fields["iProofOptions"] = "Email"
	}
	fields["action"] = "Skip"
	if fields["PhoneNumber"] == "" {
		fields["PhoneNumber"] = ""
	}
	if fields["EmailAddress"] == "" {
		fields["EmailAddress"] = ""
	}
	resp, err := session.Post(action, requestOptions{
		Data: fields,
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://account.live.com",
			"Referer":      rawURL,
		}),
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("Skip proofs 请求异常: %s", err), AuthStatusRequestError, err)
	}
	return resp.Body, resp.URL, nil
}

func handleProofsPage(session *Session, page, rawURL, action, proxy string, alreadyBound bool, email string, preferredBindingAddress string) (string, string, string, error) {
	if action != "" && action != "#" {
		action = resolveURL(rawURL, action)
	}
	skipURL := extractSkipURL(page)
	if skipURL != "" && strings.Contains(skipURL, "res=success") {
		logInfo("尝试通过 skip URL 跳过安全证明页")
		resp, err := postEmptyForm(session, skipURL, rawURL)
		if err != nil {
			return "", "", "", err
		}
		return resp.Body, resp.URL, "", nil
	}

	isAddEmail := isAddEmailPage(page, action)
	isSPA := len(page) > 50000
	if !isSPA && action != "" && action != "#" && strings.HasPrefix(action, "http") {
		logInfo("尝试跳过安全证明页")
		skippedPage, skippedURL, err := trySkipProofsPage(session, page, rawURL, action)
		if err != nil {
			return "", "", "", err
		}
		skippedAction := extractFormAction(skippedPage)
		if skippedAction != "" && skippedAction != "#" {
			skippedAction = resolveURL(skippedURL, skippedAction)
		}
		if isAddEmail && !alreadyBound && isAddEmailPage(skippedPage, skippedAction) {
			logInfo("安全证明页无法跳过, 改为绑定辅助邮箱")
			return bindAuxiliaryEmail(session, skippedPage, skippedURL, proxy, email, preferredBindingAddress)
		}
		if skippedURL == rawURL && skippedPage == page {
			return "", "", "", newAuthError("Microsoft security proof skip was not accepted.", AuthStatusAuthTimeout)
		}
		return skippedPage, skippedURL, "", nil
	}

	if isAddEmail && !alreadyBound {
		return bindAuxiliaryEmail(session, page, rawURL, proxy, email, preferredBindingAddress)
	}
	if alreadyBound && isAddEmail {
		logInfo("已绑定过辅助邮箱, 跳过后续 proofs 页面 (Skip)")
	}
	if strings.Contains(page, "SendOtt") || strings.Contains(page, "VerifyCode") {
		if hint := extractPageHint(page, 220); hint != "" {
			logWarning("安全证明页仍要求验证码: %s", hint)
		}
		return "", "", "", newAuthError("安全证明页仍要求验证码", AuthStatusCodeTimeout)
	}
	if strings.Contains(page, "AddAlias") || strings.Contains(page, "AliasAccrual") {
		return "", "", "", newAuthError("需要绑定辅助邮箱")
	}
	if strings.Contains(page, "proofList") || strings.Contains(page, "ProofPickerControl") {
		return "", "", "", newAuthError("需要身份验证")
	}
	return "", "", "", newAuthError("需要身份验证")
}

func handleAutoSubmit(session *Session, page, rawURL, action string) (string, string, error) {
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
		return "", "", wrapAuthError(fmt.Sprintf("自动跳转请求异常: %s", err), AuthStatusRequestError, err)
	}
	return resp.Body, resp.URL, nil
}

func handleAccountPages(session *Session, page, rawURL, proxy string, maxRounds int, email string, proofData []ProofData, preferredBindingAddress string) (string, string, string, error) {
	return handleAccountPagesWithOptions(session, page, rawURL, proxy, maxRounds, email, proofData, false, preferredBindingAddress)
}

func handleAccountPagesWithOptions(session *Session, page, rawURL, proxy string, maxRounds int, email string, proofData []ProofData, allowExistingProofVerification bool, preferredBindingAddress string) (string, string, string, error) {
	if maxRounds <= 0 {
		maxRounds = 10
	}
	boundMailbox := ""
	for roundNum := 1; roundNum <= maxRounds; roundNum++ {
		low := strings.ToLower(rawURL)
		action := extractFormAction(page)
		isIdentity, isProofs := accountVerificationPageKinds(page, rawURL, action)
		isInterrupt := isInterruptPage(rawURL) || isIdentity || isProofs
		isAuto := isAutoSubmitPage(page, action)

		if isAccountRecoverURL(rawURL) || isAccountRecoverURL(action) {
			logWarning("检测到账号异常恢复页")
			return "", "", "", newAuthError("账号异常", AuthStatusAccountAbnormal)
		}
		if !isInterrupt && !isAuto {
			logDebug("handle_account_pages: 无可处理页, url=%s action=%s page_len=%d has_DoSubmit=%t has_form=%t", rawURL, action, len(page), strings.Contains(page, "DoSubmit"), strings.Contains(strings.ToLower(page), "<form"))
			break
		}

		pageKind := "登录后续页面"
		if isIdentity {
			pageKind = "身份验证页"
		} else if isProofs {
			pageKind = "安全证明页"
		} else if strings.Contains(low, "/interrupt/") || strings.Contains(low, "passkey") {
			pageKind = "中断跳过页"
		} else if isAuto {
			pageKind = "自动跳转页"
		}
		logInfo("处理%s #%d", pageKind, roundNum)
		logDebug("中断页循环 #%d: url=%s, is_interrupt=%t, is_auto=%t, action=%s", roundNum, rawURL, isInterrupt, isAuto, action)

		if isIdentity && strings.Contains(low, "account.live.com") {
			nextPage, nextURL, tempMail, err := handleIdentityPage(session, page, rawURL, proxy, email, proofData, allowExistingProofVerification, preferredBindingAddress)
			if err != nil {
				return "", "", "", err
			}
			page, rawURL = nextPage, nextURL
			if tempMail != "" {
				boundMailbox = tempMail
			}
			continue
		}

		if (strings.Contains(low, "/interrupt/") || strings.Contains(low, "passkey")) && strings.Contains(low, "account.live.com") {
			nextPage, nextURL, ok, err := handlePasskeyInterrupt(session, page, rawURL)
			if err != nil {
				return "", "", "", err
			}
			if ok {
				page, rawURL = nextPage, nextURL
				continue
			}
		}

		if isAuto && !isInterrupt {
			if action != "" && strings.Contains(strings.ToLower(action), "identity/confirm") {
				logInfo("自动跳转: 进入 identity/confirm 获取 OTP 上下文")
				nextPage, nextURL, err := handleAutoSubmit(session, page, rawURL, action)
				if err != nil {
					return "", "", "", err
				}
				page, rawURL = nextPage, nextURL
				continue
			}
			if action == "" {
				break
			}
			nextPage, nextURL, err := handleAutoSubmit(session, page, rawURL, action)
			if err != nil {
				return "", "", "", err
			}
			page, rawURL = nextPage, nextURL
			continue
		}

		if isProofs {
			nextPage, nextURL, tempMail, err := handleProofsPage(session, page, rawURL, action, proxy, boundMailbox != "", email, preferredBindingAddress)
			if err != nil {
				return "", "", "", err
			}
			page, rawURL = nextPage, nextURL
			if tempMail != "" {
				boundMailbox = tempMail
			}
			continue
		}
		break
	}
	action := extractFormAction(page)
	isIdentity, isProofs := accountVerificationPageKinds(page, rawURL, action)
	if isInterruptPage(rawURL) || isIdentity || isProofs || isAutoSubmitPage(page, action) {
		return "", "", "", newAuthError("Microsoft account page flow did not complete.", AuthStatusAuthTimeout, boundMailbox)
	}
	return page, rawURL, boundMailbox, nil
}

func resolveURL(baseURL, ref string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

func pageKeywordSubset(page string, keywords []string) []string {
	var found []string
	for _, keyword := range keywords {
		if strings.Contains(page, keyword) {
			found = append(found, keyword)
		}
	}
	return found
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstN(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}
