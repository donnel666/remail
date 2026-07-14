package msacl

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"html"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	addAliasClientID = "f6061517-4417-4749-a5b6-5bba57f9e6cc"
	addAssocIDURL    = "https://account.live.com/AddAssocId?ru=&cru=&fl="

	aliasCategoryAdded             = "added"
	aliasCategoryRateLimited       = "rate_limited"
	aliasCategoryExists            = "alias_exists"
	aliasCategoryFailed            = "alias_failed"
	aliasCategoryNeedsVerification = "needs_verification"

	explicitAliasStageLoginMissingPPFT        = "login_missing_ppft"
	explicitAliasStageLoginMissingPostURL     = "login_missing_post_url"
	explicitAliasStageAccountPageIncomplete   = "account_page_incomplete"
	explicitAliasStageManageRedirected        = "manage_redirected"
	explicitAliasStageAddAssocIDMissingCanary = "addassocid_missing_canary"
)

var (
	aliasFirstNames = []string{
		"james", "john", "robert", "michael", "david", "william", "richard",
		"joseph", "thomas", "charles", "daniel", "matthew", "anthony", "mark",
		"steven", "andrew", "joshua", "brian", "kevin", "george", "edward",
		"mary", "patricia", "jennifer", "linda", "sarah", "elizabeth", "barbara",
		"jessica", "susan", "karen", "lisa", "nancy", "betty", "margaret",
		"sandra", "ashley", "dorothy", "kimberly", "emily", "olivia", "emma",
		"sophia", "isabella", "ava", "mia", "charlotte", "amelia", "harper",
	}
	aliasLastNames = []string{
		"smith", "johnson", "williams", "brown", "jones", "garcia", "miller",
		"davis", "rodriguez", "martinez", "hernandez", "lopez", "gonzalez",
		"wilson", "anderson", "thomas", "taylor", "moore", "jackson", "martin",
		"lee", "perez", "thompson", "white", "harris", "sanchez", "clark",
		"ramirez", "lewis", "robinson", "walker", "young", "allen", "king",
		"wright", "scott", "torres", "nguyen", "hill", "flores", "green",
		"adams", "nelson", "baker", "hall", "rivera", "campbell", "mitchell",
	}
)

// ExplicitAliasResult is the safe result returned to the asynchronous alias
// workflow. Aliases contains only addresses confirmed by Microsoft.
type ExplicitAliasResult struct {
	Aliases      []string
	Attempted    []string
	Absent       []string
	Category     string
	Stage        string
	SafeMessage  string
	ProxyFailure bool
}

// AddExplicitAliases creates at most two Outlook aliases in one authenticated
// account.live.com session. Microsoft applies the authoritative service quota;
// the caller is still responsible for durable weekly and yearly scheduling.
func AddExplicitAliases(ctx context.Context, email, password, proxy, preferredBindingAddress string, count int) (ExplicitAliasResult, error) {
	candidates, err := GenerateExplicitAliasCandidates(count)
	if err != nil {
		return ExplicitAliasResult{
			Category:    "request",
			SafeMessage: "Microsoft alias service is temporarily unavailable.",
		}, nil
	}
	return AddExplicitAliasCandidates(ctx, email, password, proxy, preferredBindingAddress, candidates)
}

// GenerateExplicitAliasCandidates reserves stable candidates before any remote
// side effect so retries can reconcile the same addresses.
func GenerateExplicitAliasCandidates(count int) ([]string, error) {
	if count <= 0 {
		return nil, nil
	}
	if count > 2 {
		count = 2
	}
	candidates := make([]string, 0, count)
	seen := make(map[string]struct{}, count)
	for len(candidates) < count {
		prefix, err := generateExplicitAliasPrefix()
		if err != nil {
			return nil, err
		}
		alias := prefix + "@outlook.com"
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		candidates = append(candidates, alias)
	}
	return candidates, nil
}

// AddExplicitAliasCandidates creates or reconciles the supplied durable
// candidates. It never substitutes a different address during a retry.
func AddExplicitAliasCandidates(ctx context.Context, email, password, proxy, preferredBindingAddress string, candidates []string) (ExplicitAliasResult, error) {
	if err := contextOrBackground(ctx).Err(); err != nil {
		return ExplicitAliasResult{
			Category:    "request",
			SafeMessage: "Microsoft alias service is temporarily unavailable.",
		}, err
	}
	candidates = normalizeExplicitAliasCandidates(candidates)
	if len(candidates) == 0 {
		return ExplicitAliasResult{}, nil
	}
	if len(candidates) > 2 {
		candidates = candidates[:2]
	}

	result, err := addExplicitAliases(ctx, email, password, proxy, preferredBindingAddress, candidates)
	if err == nil {
		return result, nil
	}
	failure := mapExplicitAliasError(err)
	failure.Aliases = result.Aliases
	failure.Attempted = result.Attempted
	failure.Absent = result.Absent
	return failure, nil
}

// ReconcileExplicitAliasCandidates checks durable uncertain candidates without
// submitting AddAssocId again. A confirmed absence is therefore a negative
// observation of the original POST rather than a new side effect.
func ReconcileExplicitAliasCandidates(ctx context.Context, email, password, proxy, preferredBindingAddress string, candidates []string) (ExplicitAliasResult, error) {
	if err := contextOrBackground(ctx).Err(); err != nil {
		return ExplicitAliasResult{
			Category:    "request",
			SafeMessage: "Microsoft alias service is temporarily unavailable.",
		}, err
	}
	candidates = normalizeExplicitAliasCandidates(candidates)
	if len(candidates) == 0 {
		return ExplicitAliasResult{}, nil
	}
	if len(candidates) > 2 {
		candidates = candidates[:2]
	}

	result, err := reconcileExplicitAliases(ctx, email, password, proxy, preferredBindingAddress, candidates)
	if err == nil {
		return result, nil
	}
	failure := mapExplicitAliasError(err)
	failure.Aliases = result.Aliases
	failure.Absent = result.Absent
	return failure, nil
}

func reconcileExplicitAliases(ctx context.Context, email, password, proxy, preferredBindingAddress string, candidates []string) (ExplicitAliasResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return ExplicitAliasResult{}, newAuthError("账号或密码为空", AuthStatusPasswordError)
	}
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return ExplicitAliasResult{}, wrapAuthError(fmt.Sprintf("创建微软会话失败: %s", err), AuthStatusRequestError, err)
	}
	if _, _, err := loginForExplicitAlias(session, email, password, proxy, preferredBindingAddress); err != nil {
		return ExplicitAliasResult{}, err
	}
	return reconcileExplicitAliasesWithSession(session, candidates)
}

func reconcileExplicitAliasesWithSession(session *Session, candidates []string) (ExplicitAliasResult, error) {
	result := ExplicitAliasResult{
		Aliases: make([]string, 0, len(candidates)),
		Absent:  make([]string, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		present, err := confirmExplicitAliasPresent(session, candidate, addAssocIDURL)
		if err != nil {
			return result, err
		}
		if present {
			result.Aliases = append(result.Aliases, candidate)
		} else {
			result.Absent = append(result.Absent, candidate)
		}
	}
	if len(result.Absent) > 0 {
		result.Category = aliasCategoryFailed
		result.SafeMessage = "Microsoft alias candidate is not yet visible."
	}
	return result, nil
}

func addExplicitAliases(ctx context.Context, email, password, proxy, preferredBindingAddress string, candidates []string) (ExplicitAliasResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return ExplicitAliasResult{}, newAuthError("账号或密码为空", AuthStatusPasswordError)
	}

	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return ExplicitAliasResult{}, wrapAuthError(fmt.Sprintf("创建微软会话失败: %s", err), AuthStatusRequestError, err)
	}
	if _, _, err := loginForExplicitAlias(session, email, password, proxy, preferredBindingAddress); err != nil {
		return ExplicitAliasResult{}, err
	}

	aliases := make([]string, 0, len(candidates))
	attemptedAliases := make([]string, 0, len(candidates))
	lastCategory := ""
	for attempt, candidate := range candidates {
		if attempt > 0 {
			if err := session.sleep(2 * time.Second); err != nil {
				return ExplicitAliasResult{Aliases: aliases, Attempted: attemptedAliases}, wrapAuthError(fmt.Sprintf("创建别名取消: %s", err), AuthStatusRequestError, err)
			}
		}
		prefix := strings.TrimSuffix(candidate, "@outlook.com")
		alias, category, attempted, err := addSingleExplicitAlias(session, prefix, email, proxy, preferredBindingAddress)
		if attempted {
			attemptedAliases = append(attemptedAliases, candidate)
		}
		if err != nil {
			return ExplicitAliasResult{Aliases: aliases, Attempted: attemptedAliases}, err
		}
		lastCategory = category
		switch category {
		case aliasCategoryAdded:
			aliases = append(aliases, alias)
		case aliasCategoryExists:
			continue
		case aliasCategoryRateLimited:
			return ExplicitAliasResult{
				Aliases:     aliases,
				Attempted:   attemptedAliases,
				Category:    aliasCategoryRateLimited,
				SafeMessage: "Microsoft alias creation is rate limited.",
			}, nil
		case aliasCategoryFailed:
			continue
		default:
			return ExplicitAliasResult{Aliases: aliases, Attempted: attemptedAliases}, newAuthError("Microsoft alias response category is invalid.", AuthStatusRequestError)
		}
	}

	if len(aliases) == len(candidates) {
		return ExplicitAliasResult{Aliases: aliases, Attempted: attemptedAliases}, nil
	}
	if lastCategory == aliasCategoryExists {
		return ExplicitAliasResult{
			Aliases:     aliases,
			Attempted:   attemptedAliases,
			Category:    aliasCategoryExists,
			SafeMessage: "Generated Microsoft aliases are unavailable.",
		}, nil
	}
	return ExplicitAliasResult{
		Aliases:     aliases,
		Attempted:   attemptedAliases,
		Category:    aliasCategoryFailed,
		SafeMessage: "Microsoft alias creation did not reach the requested count.",
	}, nil
}

func generateExplicitAliasPrefix() (string, error) {
	random := make([]byte, 8)
	if _, err := crand.Read(random); err != nil {
		return "", err
	}
	first := aliasFirstNames[int(random[0])%len(aliasFirstNames)]
	last := aliasLastNames[int(random[1])%len(aliasLastNames)]
	var digits strings.Builder
	digits.Grow(6)
	for _, value := range random[2:] {
		digits.WriteByte('0' + value%10)
	}
	return first + last + digits.String(), nil
}

func normalizeExplicitAliasCandidates(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	pattern := regexp.MustCompile(`^[a-z]{2,24}[0-9]{6}@outlook\.com$`)
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !pattern.MatchString(value) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func loginForExplicitAlias(session *Session, email, password, proxy, preferredBindingAddress string) (string, string, error) {
	logInfo("AddAlias 步骤1: 访问 AddAssocId")
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
		if ppft == "" {
			return "", "", newExplicitAliasStageError(
				"OAuth 登录页缺少 PPFT 或提交地址",
				AuthStatusAuthTimeout,
				explicitAliasStageLoginMissingPPFT,
			)
		}
		return "", "", newExplicitAliasStageError(
			"OAuth 登录页缺少 PPFT 或提交地址",
			AuthStatusAuthTimeout,
			explicitAliasStageLoginMissingPostURL,
		)
	}
	uaid := firstNonEmpty(getQueryParam(postURL, "uaid"), getQueryParam(currentURL, "uaid"))

	logInfo("AddAlias 步骤2: 提交邮箱")
	resp, err = session.Post(postURL, requestOptions{
		Data: map[string]string{
			"login":        email,
			"loginfmt":     email,
			"type":         "11",
			"LoginOptions": "3",
			"lrt":          "",
			"lrtPartition": "",
			"hisRegion":    "",
			"hisScaleUnit": "",
			"PPFT":         ppft,
			"canary":       "",
			"i19":          "63791",
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
	postURL = extractPostURL(page)
	if postURL == "" {
		return "", "", newExplicitAliasStageError(
			"提交凭据时缺少 post_url",
			AuthStatusAuthTimeout,
			explicitAliasStageLoginMissingPostURL,
		)
	}

	opid := getQueryParam(currentURL, "opid")
	proofData, err := getExplicitAliasCredentialType(session, email, ppft, uaid, opid, currentURL)
	if err != nil {
		return "", "", err
	}

	vanguardToken, err := checkExplicitAliasPassword(session, email, password, uaid, currentURL)
	if err != nil {
		return "", "", err
	}
	page, currentURL, err = submitExplicitAliasCredentials(session, email, password, ppft, postURL, vanguardToken, currentURL)
	if err != nil {
		return "", "", err
	}
	page, currentURL, err = handleJSPollingPage(session, page, currentURL)
	if err != nil {
		return "", "", err
	}
	page, currentURL, _, err = handleAccountPagesWithOptions(
		session,
		page,
		currentURL,
		proxy,
		10,
		email,
		proofData,
		true,
		preferredBindingAddress,
	)
	if err != nil {
		return "", "", annotateExplicitAliasAuthTimeoutStage(err, explicitAliasStageAccountPageIncomplete)
	}
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

	if !strings.Contains(strings.ToLower(currentURL), "account.live.com/addassocid") {
		resp, err = session.Get(addAssocIDURL, requestOptions{
			Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
			AllowRedirects:    true,
			HasAllowRedirects: true,
		})
		if err != nil {
			return "", "", wrapAuthError(fmt.Sprintf("进入 AddAssocId 异常: %s", err), AuthStatusRequestError, err)
		}
		page, currentURL = resp.Body, resp.URL
	}
	if !strings.Contains(strings.ToLower(currentURL), "account.live.com/addassocid") {
		return "", "", newExplicitAliasStageError(
			"未能进入 Microsoft 别名管理页",
			AuthStatusAuthTimeout,
			explicitAliasStageAccountPageIncomplete,
		)
	}
	return page, currentURL, nil
}

func getExplicitAliasCredentialType(session *Session, email, ppft, uaid, opid, referer string) ([]ProofData, error) {
	if uaid == "" {
		return nil, nil
	}
	endpoint := fmt.Sprintf(
		"https://login.live.com/GetCredentialType.srf?opid=%s&id=293577&client_id=%s&mkt=ZH-CN&lc=2052&uaid=%s",
		url.QueryEscape(opid),
		url.QueryEscape(addAliasClientID),
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
		logWarning("AddAlias GetCredentialType 返回非 JSON")
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

func checkExplicitAliasPassword(session *Session, email, password, uaid, referer string) (string, error) {
	resp, err := session.Post("https://login.live.com/checkpassword.srf", requestOptions{
		JSON: map[string]any{
			"username":               email,
			"password":               password,
			"checkpasswordflowtoken": "",
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
		return "", wrapAuthError(fmt.Sprintf("密码验证请求异常: %s", err), AuthStatusRequestError, err)
	}
	if resp.StatusCode == 429 {
		retryAfter := 60
		if parsed, err := strconv.Atoi(resp.Header.Get("retry-after")); err == nil && parsed > 0 {
			retryAfter = parsed
		}
		return "", newAuthError(fmt.Sprintf("密码验证频率受限 (429), 请 %ds 后重试", retryAfter), AuthStatusRateLimited)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAuthError(fmt.Sprintf("密码验证请求失败 (HTTP %d)", resp.StatusCode), AuthStatusRequestError)
	}
	var result map[string]any
	if err := resp.JSON(&result); err != nil {
		return "", newAuthError(fmt.Sprintf("密码验证返回异常响应 (HTTP %d)", resp.StatusCode), AuthStatusRequestError)
	}
	if asString(result["validationresult"]) != "succeed" {
		return "", newAuthError("密码错误", AuthStatusPasswordError)
	}
	return asString(result["vanguardflowtoken"]), nil
}

func submitExplicitAliasCredentials(session *Session, email, password, ppft, postURL, vanguardToken, referer string) (string, string, error) {
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
			"Referer":      referer,
		}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", wrapAuthError(fmt.Sprintf("提交凭据请求异常: %s", err), AuthStatusRequestError, err)
	}
	return resp.Body, resp.URL, nil
}

func continueExplicitAliasLoginRelay(session *Session, page, currentURL string, maxRounds int) (string, string, error) {
	for round := 0; round < maxRounds; round++ {
		lowURL := strings.ToLower(currentURL)
		action := extractFormAction(page)
		if strings.Contains(lowURL, "login.live.com/login.srf") {
			if action == "" {
				wreply := queryValue(currentURL, "wreply")
				if !strings.HasPrefix(strings.ToLower(wreply), "https://") {
					break
				}
				resp, err := session.Get(wreply, requestOptions{
					Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
					AllowRedirects:    true,
					HasAllowRedirects: true,
				})
				if err != nil {
					return "", "", wrapAuthError(fmt.Sprintf("登录中继请求异常: %s", err), AuthStatusRequestError, err)
				}
				page, currentURL = resp.Body, resp.URL
				continue
			}
			action = resolveURL(currentURL, action)
			if !strings.Contains(strings.ToLower(action), "account.live.com") {
				break
			}
			resp, err := session.Post(action, requestOptions{
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
				return "", "", wrapAuthError(fmt.Sprintf("登录中继提交异常: %s", err), AuthStatusRequestError, err)
			}
			page, currentURL = resp.Body, resp.URL
			continue
		}

		if strings.Contains(lowURL, "login.live.com/ppsecure") && action != "" {
			action = resolveURL(currentURL, action)
			if strings.Contains(strings.ToLower(action), "account.live.com") && strings.Contains(page, "DoSubmit") {
				resp, err := session.Post(action, requestOptions{
					Data: extractHiddenInputs(page),
					Headers: navHeaders(session, map[string]string{
						"Content-Type": "application/x-www-form-urlencoded",
						"Origin":       "https://login.live.com",
						"Referer":      currentURL,
					}),
					AllowRedirects:    true,
					HasAllowRedirects: true,
				})
				if err != nil {
					return "", "", wrapAuthError(fmt.Sprintf("登录自动提交异常: %s", err), AuthStatusRequestError, err)
				}
				page, currentURL = resp.Body, resp.URL
				continue
			}
		}
		break
	}
	return page, currentURL, nil
}

func followExplicitAliasTarget(session *Session, page, currentURL string, maxRounds int) (string, string, error) {
	for round := 0; round < maxRounds; round++ {
		lowURL := strings.ToLower(currentURL)
		if isExplicitAliasTarget(lowURL) {
			break
		}
		if strings.Contains(lowURL, "account.live.com/auth/redirect") {
			resp, err := session.Get(currentURL, requestOptions{
				Headers:           navHeaders(session, nil),
				AllowRedirects:    true,
				HasAllowRedirects: true,
			})
			if err != nil {
				return "", "", wrapAuthError(fmt.Sprintf("OAuth 回调异常: %s", err), AuthStatusRequestError, err)
			}
			page, currentURL = resp.Body, resp.URL
			continue
		}

		if strings.Contains(lowURL, "consent") ||
			strings.Contains(page, "ucaccept") ||
			strings.Contains(page, "pprid") {
			nextPage, nextURL, err := handleConsent(session, page, currentURL)
			if err != nil {
				return "", "", err
			}
			if nextPage == page && nextURL == currentURL {
				break
			}
			page, currentURL = nextPage, nextURL
			continue
		}

		action := extractFormAction(page)
		if action != "" && strings.Contains(page, "DoSubmit") {
			action = resolveURL(currentURL, action)
			resp, err := session.Post(action, requestOptions{
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
				return "", "", wrapAuthError(fmt.Sprintf("OAuth 自动提交异常: %s", err), AuthStatusRequestError, err)
			}
			page, currentURL = resp.Body, resp.URL
			continue
		}
		if strings.Contains(lowURL, "/interrupt/") || strings.Contains(lowURL, "passkey") {
			skipURL := extractSkipURL(page)
			if skipURL == "" {
				skipURL = extractPasskeySkipURL(page, currentURL)
			}
			if strings.HasPrefix(skipURL, "http") {
				resp, err := session.Get(skipURL, requestOptions{
					Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
					AllowRedirects:    true,
					HasAllowRedirects: true,
				})
				if err != nil {
					return "", "", wrapAuthError(fmt.Sprintf("跳过中断页异常: %s", err), AuthStatusRequestError, err)
				}
				page, currentURL = resp.Body, resp.URL
				continue
			}
			logWarning("interrupt/passkey 无法提取 skip 链接: url=%s 候选=[%s]", currentURL, dumpInterruptCandidates(page))
		}
		break
	}
	return page, currentURL, nil
}

func addSingleExplicitAlias(session *Session, prefix, email, proxy, preferredBindingAddress string) (string, string, bool, error) {
	fullAlias := strings.ToLower(prefix + "@outlook.com")

	var page string
	var canary string
	options := "LIVE"
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := session.Get(addAssocIDURL, requestOptions{
			Headers:           navHeaders(session, nil),
			AllowRedirects:    true,
			HasAllowRedirects: true,
		})
		if err != nil {
			return "", "", false, wrapAuthError(fmt.Sprintf("加载 AddAssocId 异常: %s", err), AuthStatusRequestError, err)
		}
		page = resp.Body
		canary = extractExplicitAliasCanary(page)
		options = extractAddAssocIDOptions(page)
		if canary != "" {
			break
		}
	}
	if canary == "" {
		logExplicitAliasStage(explicitAliasStageAddAssocIDMissingCanary)
		return fullAlias, aliasCategoryFailed, false, nil
	}

	attempted := true
	resp, err := session.Post(addAssocIDURL, requestOptions{
		Data: map[string]string{
			"canary":            canary,
			"PostOption":        "NONE",
			"SingleDomain":      "outlook.com",
			"UpSell":            "",
			"AddAssocIdOptions": options,
			"AssociatedIdLive":  prefix,
		},
		Headers: navHeaders(session, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://account.live.com",
			"Referer":      "https://account.live.com/AddAssocId",
		}),
		AllowRedirects:    false,
		HasAllowRedirects: true,
	})
	if err != nil {
		return "", "", attempted, wrapAuthError(fmt.Sprintf("提交 AddAssocId 异常: %s", err), AuthStatusRequestError, err)
	}
	redirectURL := resp.Header.Get("Location")
	category := classifyAddAssocIDResponse(resp.StatusCode, redirectURL, resp.Body)
	switch category {
	case aliasCategoryAdded:
		return fullAlias, aliasCategoryAdded, attempted, nil
	case aliasCategoryRateLimited, aliasCategoryFailed:
		return fullAlias, category, attempted, nil
	case aliasCategoryExists:
		present, err := confirmExplicitAliasPresent(session, fullAlias, addAssocIDURL)
		if err != nil {
			return "", "", attempted, err
		}
		if present {
			return fullAlias, aliasCategoryAdded, attempted, nil
		}
		return fullAlias, aliasCategoryExists, attempted, nil
	case "request":
		return "", "", attempted, newAuthError(fmt.Sprintf("AddAssocId 请求失败 (HTTP %d)", resp.StatusCode), AuthStatusRequestError)
	}

	page, currentURL := resp.Body, resp.URL
	if redirectURL != "" {
		targetURL := resolveURL(resp.URL, redirectURL)
		redirectResp, err := session.Get(targetURL, requestOptions{
			Headers:           navHeaders(session, map[string]string{"Referer": addAssocIDURL}),
			AllowRedirects:    true,
			HasAllowRedirects: true,
		})
		if err != nil {
			return "", "", attempted, wrapAuthError(fmt.Sprintf("跟随 AddAssocId 跳转异常: %s", err), AuthStatusRequestError, err)
		}
		page, currentURL = redirectResp.Body, redirectResp.URL
	}
	if explicitAliasPresentOnManagePage(page, currentURL, fullAlias) {
		return fullAlias, aliasCategoryAdded, attempted, nil
	}

	if strings.Contains(page, "apiCanary") && strings.Contains(page, "rawProofList") {
		page, currentURL, _, err = handleOTPVerification(
			session,
			page,
			currentURL,
			email,
			proxy,
			nil,
			preferredBindingAddress,
		)
	} else {
		page, currentURL, _, err = handleAccountPagesWithOptions(
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
	}
	if err != nil {
		return "", "", attempted, annotateExplicitAliasAuthTimeoutStage(err, explicitAliasStageAccountPageIncomplete)
	}
	page, currentURL, err = continueExplicitAliasLoginRelay(session, page, currentURL, 6)
	if err != nil {
		return "", "", attempted, err
	}
	page, currentURL, err = followExplicitAliasTarget(session, page, currentURL, 10)
	if err != nil {
		return "", "", attempted, err
	}
	if explicitAliasPresentOnManagePage(page, currentURL, fullAlias) {
		return fullAlias, aliasCategoryAdded, attempted, nil
	}
	present, err := confirmExplicitAliasPresent(session, fullAlias, currentURL)
	if err != nil {
		return "", "", attempted, err
	}
	if present {
		return fullAlias, aliasCategoryAdded, attempted, nil
	}
	return fullAlias, aliasCategoryFailed, attempted, nil
}

func extractExplicitAliasCanary(page string) string {
	if canary := extractHiddenInputs(page)["canary"]; canary != "" {
		return canary
	}
	patterns := []string{
		`(?is)"canary"\s*:\s*"([^"]+)"`,
		`(?is)name="canary"[^>]*value="([^"]*)"`,
	}
	for _, pattern := range patterns {
		match := regexp.MustCompile(pattern).FindStringSubmatch(page)
		if len(match) > 1 {
			return strings.ReplaceAll(match[1], `\/`, "/")
		}
	}
	return ""
}

func extractAddAssocIDOptions(page string) string {
	patterns := []string{
		`(?is)name="AddAssocIdOptions"[^>]*value="([^"]*)"`,
		`(?is)<option[^>]*value="([^"]*)"[^>]*selected[^>]*>.*?</option>`,
	}
	for _, pattern := range patterns {
		match := regexp.MustCompile(pattern).FindStringSubmatch(page)
		if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
			return match[1]
		}
	}
	return "LIVE"
}

func classifyAddAssocIDResponse(statusCode int, redirectURL, body string) string {
	if statusCode >= 300 && statusCode < 400 {
		if isExplicitAliasSuccessRedirect(redirectURL) {
			return aliasCategoryAdded
		}
		return aliasCategoryNeedsVerification
	}
	if statusCode == 429 {
		return aliasCategoryRateLimited
	}
	if statusCode == 401 || statusCode == 403 || statusCode == 408 || statusCode == 425 || statusCode >= 500 {
		return "request"
	}
	if statusCode != 200 && statusCode != 409 {
		return aliasCategoryFailed
	}
	lowerBody := strings.ToLower(body)
	rateLimitMarkers := []string{
		"此服务暂时出现了问题", "暂时出现问题", "限制添加别名的频率",
		"there's a temporary problem", "temporary problem with the service",
		"limit the frequency", "limit how often you can add", "we limit the frequency",
		"try again later", "limitons la fréquence", "nous limitons",
		"wir beschränken die häufigkeit", "頻度を制限", "limitamos la frecuencia",
		"limitamos a frequência", "ограничиваем частоту",
	}
	for _, marker := range rateLimitMarkers {
		if strings.Contains(lowerBody, strings.ToLower(marker)) {
			return aliasCategoryRateLimited
		}
	}
	existsMarkers := []string{
		"已有人使用", "already taken", "email address isn't available",
		"email address is not available", "alias isn't available",
		"déjà pris", "bereits vergeben", "ya está en uso",
	}
	for _, marker := range existsMarkers {
		if strings.Contains(lowerBody, strings.ToLower(marker)) {
			return aliasCategoryExists
		}
	}
	if statusCode == 409 {
		return aliasCategoryExists
	}
	if strings.Contains(body, "apiCanary") && strings.Contains(body, "rawProofList") {
		return aliasCategoryNeedsVerification
	}
	if statusCode == 200 {
		return aliasCategoryNeedsVerification
	}
	return aliasCategoryFailed
}

func isExplicitAliasSuccessRedirect(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	if host := strings.ToLower(parsed.Hostname()); host != "" && host != "account.live.com" {
		return false
	}
	if !strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/names/manage") {
		return false
	}
	return strings.EqualFold(parsed.Query().Get("noteid"), "NOTE_AssociatedIdAddedWL")
}

func confirmExplicitAliasPresent(session *Session, alias, referer string) (bool, error) {
	resp, err := session.Get("https://account.live.com/names/manage", requestOptions{
		Headers:           navHeaders(session, map[string]string{"Referer": referer}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return false, wrapAuthError(fmt.Sprintf("查询 Microsoft 别名列表异常: %s", err), AuthStatusRequestError, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, newAuthError(fmt.Sprintf("查询 Microsoft 别名列表失败 (HTTP %d)", resp.StatusCode), AuthStatusRequestError)
	}
	if !isExplicitAliasManageURL(resp.URL) {
		return false, newExplicitAliasStageError(
			"Microsoft alias session is no longer authenticated.",
			AuthStatusAuthTimeout,
			explicitAliasStageManageRedirected,
		)
	}
	return explicitAliasPresentOnManagePage(resp.Body, resp.URL, alias), nil
}

func explicitAliasPresentOnManagePage(page, rawURL, alias string) bool {
	if !isExplicitAliasManageURL(rawURL) {
		return false
	}
	normalizedPage := strings.ToLower(html.UnescapeString(page))
	normalizedPage = strings.ReplaceAll(normalizedPage, `\u0040`, "@")
	normalizedPage = strings.ReplaceAll(normalizedPage, `\x40`, "@")
	alias = strings.ToLower(strings.TrimSpace(alias))
	boundary := `[^a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]`
	pattern := regexp.MustCompile(`(?:^|` + boundary + `)` + regexp.QuoteMeta(alias) + `(?:$|` + boundary + `)`)
	return pattern.MatchString(normalizedPage)
}

func isExplicitAliasManageURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Hostname(), "account.live.com") {
		return false
	}
	return strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/names/manage")
}

// extractAllExplicitAliasesFromManagePage extracts all @outlook.com, @hotmail.com,
// @live.com, and @msn.com addresses from a normalized names/manage page. It is
// the "list all existing aliases" counterpart of explicitAliasPresentOnManagePage.
func extractAllExplicitAliasesFromManagePage(page, rawURL string) []string {
	if !isExplicitAliasManageURL(rawURL) {
		return nil
	}
	normalizedPage := strings.ToLower(html.UnescapeString(page))
	normalizedPage = strings.ReplaceAll(normalizedPage, `@`, "@")
	normalizedPage = strings.ReplaceAll(normalizedPage, `\x40`, "@")
	// Match email-like patterns on the common Microsoft consumer domains.
	aliasRE := regexp.MustCompile(`[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@(?:outlook|hotmail|live|msn)\.(?:com|co\.uk|de|fr|it|es|jp|ca|au|cn|in|br)`)
	all := aliasRE.FindAllString(normalizedPage, -1)
	seen := make(map[string]struct{}, len(all))
	deduped := make([]string, 0, len(all))
	for _, a := range all {
		if _, ok := seen[a]; !ok {
			seen[a] = struct{}{}
			deduped = append(deduped, a)
		}
	}
	return deduped
}

func mapExplicitAliasError(err error) ExplicitAliasResult {
	authErr, _ := err.(*AuthError)
	status := AuthStatusUnknownError
	stage := ""
	if authErr != nil && strings.TrimSpace(authErr.Status) != "" {
		status = authErr.Status
		stage = strings.TrimSpace(authErr.Stage)
	}
	failure := func(category, message string, proxyFailure bool) ExplicitAliasResult {
		result := explicitAliasFailure(category, message, proxyFailure)
		if stage != "" {
			result.Stage = stage
			result.SafeMessage += " [stage=" + stage + "]"
		}
		return result
	}
	switch status {
	case AuthStatusPasswordError:
		return failure("password", "Microsoft account password is incorrect.", false)
	case AuthStatusUnknownMailbox:
		return failure("unknown_mailbox", "Microsoft account is unavailable for alias creation.", false)
	case AuthStatusMFARequired:
		return failure("mfa", "Microsoft account requires authenticator verification.", false)
	case AuthStatusPasskeyRequired:
		return failure("passkey", "Microsoft account requires passkey verification.", false)
	case AuthStatusPhoneVerification:
		return failure("phone", "Microsoft account requires phone verification.", false)
	case AuthStatusAccountLocked:
		return failure("locked", "Microsoft account is locked.", false)
	case AuthStatusAccountAbnormal:
		return failure("account_abnormal", "Microsoft account is restricted or requires recovery.", false)
	case AuthStatusRateLimited:
		return failure(aliasCategoryRateLimited, "Microsoft alias creation is rate limited.", false)
	case AuthStatusAlreadyBound:
		display := ""
		if authErr != nil {
			display = strings.TrimSpace(firstNonEmpty(authErr.BoundDisplay, authErr.BoundMailbox))
		}
		message := "Microsoft account recovery mailbox cannot be used."
		if display != "" {
			message = fmt.Sprintf("Microsoft account already bound to recovery mailbox (%s).", display)
		}
		return failure("already_bound", message, false)
	case AuthStatusCodeTimeout:
		return failure("code_timeout", "Microsoft recovery mailbox verification timed out.", false)
	case AuthStatusVerifyCodeError:
		return failure("code_error", "Microsoft recovery mailbox verification failed.", false)
	case AuthStatusAuthTimeout:
		return failure("auth_timeout", "Microsoft alias authorization timed out.", false)
	case AuthStatusRequestError:
		return failure("request", "Microsoft alias service is temporarily unavailable.", sessionTransportUsedProxy(err))
	default:
		return failure("alias_failed", "Microsoft alias creation failed.", false)
	}
}

func explicitAliasFailure(category, message string, proxyFailure bool) ExplicitAliasResult {
	return ExplicitAliasResult{
		Category:     category,
		SafeMessage:  message,
		ProxyFailure: proxyFailure,
	}
}

func annotateExplicitAliasAuthTimeoutStage(err error, stage string) error {
	authErr, ok := err.(*AuthError)
	if ok && authErr.Status == AuthStatusAuthTimeout {
		if authErr.Stage == "" {
			authErr.Stage = stage
		}
		logExplicitAliasStage(authErr.Stage)
	}
	return err
}

func newExplicitAliasStageError(message, status, stage string) *AuthError {
	err := newAuthError(message, status)
	err.Stage = stage
	logExplicitAliasStage(stage)
	return err
}

func logExplicitAliasStage(stage string) {
	logWarning("Microsoft explicit alias flow incomplete: stage=%s", stage)
}

func queryValue(rawURL, key string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Query().Get(key)
}

func originForURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func isExplicitAliasTarget(lowerURL string) bool {
	return strings.Contains(lowerURL, "account.live.com") &&
		(strings.Contains(lowerURL, "/addassocid") ||
			strings.Contains(lowerURL, "/names/manage") ||
			strings.Contains(lowerURL, "/proofs/") ||
			strings.Contains(lowerURL, "/identity/"))
}

// dumpInterruptCandidates extracts likely skip/continue targets from an
// interrupt page (passkey/security-info nag pages on account.live.com whose
// skip structure differs from the login.live.com ServerData "skip" block).
// It also writes the full page to /tmp for offline analysis. Debug aid.
// extractPasskeySkipURL handles the account.live.com/interrupt/passkey FIDO
// enrollment nag page. That page is a JS auto-submit form to fido/create with
// no ServerData "skip" block, so extractSkipURL can't handle it. To decline
// enrollment we follow the postBackUrl (which carries ru= back to the OAuth
// authorize continuation), falling back to the ru query param of the page URL.
func extractPasskeySkipURL(page, currentURL string) string {
	if m := regexp.MustCompile(`(?i)name=['"]postBackUrl['"]\s+value=['"]([^'"]+)['"]`).FindStringSubmatch(page); len(m) > 1 {
		u := html.UnescapeString(m[1])
		if strings.HasPrefix(strings.ToLower(u), "http") {
			return u
		}
	}
	if ru := getQueryParam(currentURL, "ru"); strings.HasPrefix(strings.ToLower(ru), "http") {
		return ru
	}
	return ""
}

func dumpInterruptCandidates(page string) string {
	_ = os.WriteFile("/tmp/msacl_passkey.html", []byte(page), 0o644)
	var parts []string
	add := func(label, pattern string, limit int) {
		re := regexp.MustCompile(pattern)
		ms := re.FindAllString(page, limit)
		if len(ms) > 0 {
			parts = append(parts, label+"="+strings.Join(ms, "¦"))
		}
	}
	add("skipKeys", `(?i)"[a-z]*skip[a-z]*"\s*:\s*("?[^",}]{0,120}"?|\{[^}]{0,160}\})`, 6)
	add("urlKeys", `(?i)"url[a-z]*"\s*:\s*"[^"]{0,120}"`, 8)
	add("hrefs", `(?i)href="[^"]{0,120}"`, 12)
	add("actions", `(?i)<form\b[^>]*action="[^"]{0,120}"`, 4)
	add("btnText", `(?i)>[^<]{0,24}(skip|later|not now|以后|跳过|稍后|取消|cancel)[^<]{0,24}<`, 6)
	s := strings.Join(parts, " ‖ ")
	if len(s) > 1800 {
		s = s[:1800]
	}
	return s
}
