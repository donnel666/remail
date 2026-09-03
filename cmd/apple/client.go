package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/appleweb"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
)

const (
	appleAccountURL  = "https://account.apple.com"
	appleIDMSAURL    = "https://idmsa.apple.com"
	appleIDURL       = "https://appleid.apple.com"
	appleAuthVersion = "8.0.2"
	appleSKVersion   = "7"
	appleUserAgent   = appleweb.UserAgent
)

var bootArgsPattern = regexp.MustCompile(`(?s)<script type="application/json" class="boot_args">\s*(.*?)</script>`)

var errPasswordRejected = errors.New("apple account password was rejected")

type passwordRejectedError struct {
	error
}

func (e *passwordRejectedError) Unwrap() error {
	return errPasswordRejected
}

type appleTransientError struct {
	status int
}

func (e *appleTransientError) Error() string {
	return fmt.Sprintf("Apple service returned HTTP %d", e.status)
}

func isAppleTransientError(err error) bool {
	var transient *appleTransientError
	return errors.As(err, &transient)
}

type appleFlow struct {
	ctx            context.Context
	session        *msacl.Session
	status         int
	location       string
	widgetKey      string
	serviceURL     string
	frameID        string
	scnt           map[string]string
	sessionID      string
	authAttributes string
	hashcashBits   int
	hashcash       string
	oauthContext   string
	repairToken    string
	skipHSA2       bool
	apiKey         string
	accountCountry string
}

func newAppleFlow(ctx context.Context, proxyURL string) (*appleFlow, error) {
	session, err := msacl.NewAppleAPISession(ctx, proxyURL, 30)
	if err != nil {
		return nil, err
	}
	frameID, err := makeFrameID()
	if err != nil {
		return nil, err
	}
	return &appleFlow{
		ctx:        ctx,
		session:    session,
		serviceURL: appleIDMSAURL + "/appleauth",
		frameID:    frameID,
		scnt:       make(map[string]string),
	}, nil
}

func processAppleAccount(ctx context.Context, proxyURL string, account accountInput, newAnswers [3]string) (accountOutput, error) {
	if account.NewPassword == "" || account.NewBirthday == "" {
		return accountOutput{}, fmt.Errorf("account change targets are missing")
	}
	result, err := processAppleAccountOnce(ctx, proxyURL, account, newAnswers, account.Password, false, false)
	if errors.Is(err, errPasswordRejected) && account.Recovering && account.NewPassword != account.Password {
		return processAppleAccountOnce(ctx, proxyURL, account, newAnswers, account.NewPassword, true, false)
	}
	return result, err
}

func processAppleAccountRegionOnly(ctx context.Context, proxyURL string, account accountInput) (accountOutput, error) {
	return processAppleAccountOnce(ctx, proxyURL, account, [3]string{}, account.Password, true, true)
}

func processAppleAccountOnce(ctx context.Context, proxyURL string, account accountInput, newAnswers [3]string, loginPassword string, passwordAlreadyChanged, regionOnly bool) (accountOutput, error) {
	flow, err := newAppleFlow(ctx, proxyURL)
	if err != nil {
		return accountOutput{}, err
	}
	portal, err := flow.getObject(appleAccountURL+"/bootstrap/portal", "bootstrap", false, false, "application/json, text/plain, */*")
	if err != nil {
		return accountOutput{}, err
	}
	flow.widgetKey = valueString(portal["serviceKey"])
	if serviceURL := strings.TrimRight(valueString(portal["serviceUrl"]), "/"); serviceURL != "" {
		flow.serviceURL = serviceURL
	}
	if flow.widgetKey == "" {
		return accountOutput{}, fmt.Errorf("bootstrap did not return serviceKey")
	}

	query := url.Values{
		"frame_id":      {flow.frameID},
		"skVersion":     {appleSKVersion},
		"iframeId":      {flow.frameID},
		"client_id":     {flow.widgetKey},
		"redirect_uri":  {appleAccountURL},
		"response_type": {"code"},
		"response_mode": {"web_message"},
		"state":         {flow.frameID},
		"authVersion":   {appleAuthVersion},
	}
	if _, err := flow.request(http.MethodGet, flow.serviceURL+"/auth/authorize/signin?"+query.Encode(), nil, true, false, false, "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"); err != nil {
		return accountOutput{}, err
	}
	if flow.sessionID == "" || flow.scnt["idmsa.apple.com"] == "" {
		return accountOutput{}, fmt.Errorf("authorize did not return session context")
	}

	federate, err := flow.postObject(flow.serviceURL+"/auth/federate?isRememberMeEnabled=true", map[string]any{
		"accountName": account.Email,
		"rememberMe":  false,
	}, "federate", true, false)
	if err != nil {
		return accountOutput{}, err
	}
	if valueBool(federate["federated"]) {
		return accountOutput{}, fmt.Errorf("federated Apple Account login is unsupported")
	}

	privateA, publicA, err := srpPublicA()
	if err != nil {
		return accountOutput{}, err
	}
	initResponse, err := flow.postObject(flow.serviceURL+"/auth/signin/init", map[string]any{
		"a":           base64.StdEncoding.EncodeToString(publicA),
		"accountName": account.Email,
		"protocols":   []string{"s2k", "s2k_fo"},
	}, "signin/init", true, false)
	if err != nil {
		return accountOutput{}, err
	}
	if flow.status != http.StatusOK || valueString(initResponse["b"]) == "" || valueString(initResponse["c"]) == "" {
		return accountOutput{}, providerError("signin/init failed", initResponse)
	}
	salt, err := base64.StdEncoding.DecodeString(valueString(initResponse["salt"]))
	if err != nil {
		return accountOutput{}, fmt.Errorf("signin/init returned invalid salt")
	}
	serverB, err := base64.StdEncoding.DecodeString(valueString(initResponse["b"]))
	if err != nil {
		return accountOutput{}, fmt.Errorf("signin/init returned invalid server key")
	}
	iterations, err := valueInt(initResponse["iteration"])
	if err != nil || iterations < 1 {
		return accountOutput{}, fmt.Errorf("signin/init returned invalid iteration count")
	}
	protocol := valueString(initResponse["protocol"])
	if protocol == "" {
		protocol = "s2k"
	}
	m1, m2, err := srpProofs(strings.ToLower(account.Email), loginPassword, salt, iterations, protocol, privateA, publicA, serverB)
	if err != nil {
		return accountOutput{}, err
	}
	complete, err := flow.postObject(flow.serviceURL+"/auth/signin/complete?isRememberMeEnabled=true", map[string]any{
		"accountName": account.Email,
		"rememberMe":  false,
		"m1":          base64.StdEncoding.EncodeToString(m1),
		"m2":          base64.StdEncoding.EncodeToString(m2),
		"c":           initResponse["c"],
	}, "signin/complete", true, false)
	if err != nil {
		return accountOutput{}, err
	}
	authType := valueString(complete["authType"])
	if authType == "hsa2" {
		return accountOutput{}, errTwoFactorEnabled
	}
	if flow.status == http.StatusUnauthorized || (serviceErrorText(complete) != "" && flow.status != http.StatusConflict && flow.status != http.StatusPreconditionFailed) {
		loginErr := providerError(fmt.Sprintf("password login rejected with HTTP %d", flow.status), complete)
		if flow.status == http.StatusUnauthorized {
			return accountOutput{}, &passwordRejectedError{loginErr}
		}
		return accountOutput{}, loginErr
	}

	switch {
	case flow.status == http.StatusConflict && authType == "sa":
		body, requestErr := flow.request(http.MethodGet, flow.serviceURL+"/auth", nil, true, false, false, "text/html")
		if requestErr != nil {
			return accountOutput{}, requestErr
		}
		boot := parseBootArgs(string(body))
		questions := questionList(asMap(asMap(boot["twoSV"])["securityQuestions"])["questions"])
		if len(questions) == 0 {
			return accountOutput{}, fmt.Errorf("security question page did not return questions")
		}
		answerAttempts, ok := securityQuestionAnswerAttempts(account.Current, newAnswers, questions)
		if !ok {
			return accountOutput{}, fmt.Errorf("current security answers do not match the requested questions")
		}
		for attempt, answers := range answerAttempts {
			payload := make([]map[string]any, 0, len(questions))
			for index, question := range questions {
				payload = append(payload, map[string]any{
					"question": question["question"],
					"answer":   answers[index],
					"id":       question["id"],
					"number":   question["number"],
				})
			}
			verified, verifyErr := flow.postObject(flow.serviceURL+"/auth/verify/questions", map[string]any{"questions": payload}, "verify/questions", false, false)
			if verifyErr != nil {
				return accountOutput{}, verifyErr
			}
			if flow.status == http.StatusOK || flow.status == http.StatusNoContent || flow.status == http.StatusConflict || flow.status == http.StatusPreconditionFailed {
				break
			}
			if attempt == len(answerAttempts)-1 {
				return accountOutput{}, providerError("security question verification failed", verified)
			}
		}
	case flow.status == http.StatusOK || flow.status == http.StatusNoContent:
	case flow.status != http.StatusPreconditionFailed:
		return accountOutput{}, fmt.Errorf("unexpected signin/complete HTTP %d", flow.status)
	}

	if flow.status == http.StatusPreconditionFailed || flow.repairToken != "" {
		repairURL := flow.location
		if strings.HasPrefix(repairURL, "/") {
			repairURL = appleIDURL + repairURL
		}
		if strings.Contains(repairURL, "widget/account/repair") {
			if _, err := flow.request(http.MethodGet, repairURL, nil, true, false, false, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8"); err != nil {
				return accountOutput{}, err
			}
		}
		options, err := flow.getObject(appleIDURL+"/account/manage/repair/options", "repair/options", false, false, "application/json, text/plain, */*")
		if err != nil {
			return accountOutput{}, err
		}
		attribute := valueString(options["repairAttribute"])
		if attribute == "hsa2_enrollment" {
			if _, err := flow.request(http.MethodGet, appleIDURL+"/account/security/upgrade", nil, false, false, false, "application/json, text/plain, */*"); err != nil {
				return accountOutput{}, err
			}
			if _, err := flow.request(http.MethodGet, appleIDURL+"/account/security/upgrade/setuplater", nil, false, false, false, "application/json, text/plain, */*"); err != nil {
				return accountOutput{}, err
			}
			flow.skipHSA2 = true
			options, err = flow.getObject(appleIDURL+"/account/manage/repair/options", "repair/options", false, false, "application/json, text/plain, */*")
			if err != nil {
				return accountOutput{}, err
			}
			attribute = valueString(options["repairAttribute"])
		}
		if attribute != "" && attribute != "complete" {
			return accountOutput{}, fmt.Errorf("apple account still requires repair: %s", attribute)
		}
		if _, err := flow.postObject(flow.serviceURL+"/auth/repair/complete", map[string]any{}, "repair/complete", false, false); err != nil {
			return accountOutput{}, err
		}
		if flow.status != http.StatusOK && flow.status != http.StatusNoContent {
			return accountOutput{}, fmt.Errorf("repair/complete failed with HTTP %d", flow.status)
		}
	}

	profile, err := fetchAppleAccount(flow)
	if err != nil {
		return accountOutput{}, err
	}
	if appleAccountUsesHSA2(profile) {
		return accountOutput{}, errTwoFactorEnabled
	}
	fallbackRegion := account.Region
	if regionOnly {
		fallbackRegion = ""
	}
	region, err := appleAccountRegion(profile, flow.accountCountry, fallbackRegion)
	if err != nil {
		return accountOutput{}, err
	}
	if regionOnly {
		return accountOutput{Region: region}, nil
	}
	questions, err := updateAppleQuestions(flow, profile, loginPassword, newAnswers)
	if err != nil {
		return accountOutput{}, err
	}
	if err := updateAppleBirthday(flow, account.NewBirthday, loginPassword); err != nil {
		return accountOutput{}, err
	}
	if !passwordAlreadyChanged {
		if err := updateApplePassword(flow, loginPassword, account.NewPassword); err != nil {
			return accountOutput{}, err
		}
	}
	return accountOutput{Region: region, Password: account.NewPassword, Birthday: account.NewBirthday, Questions: questions}, nil
}

func (f *appleFlow) request(method, rawURL string, body any, html, profile, sendHashcash bool, accept string) ([]byte, error) {
	headers, err := f.headers(rawURL, html, profile, sendHashcash, accept)
	if err != nil {
		return nil, err
	}
	response, err := f.session.Request(method, rawURL, headers, body, false)
	if err != nil {
		return nil, err
	}
	f.status = response.StatusCode
	host := ""
	if parsed, parseErr := url.Parse(rawURL); parseErr == nil {
		host = parsed.Hostname()
	}
	f.absorb(response.Header, host)
	if response.StatusCode >= http.StatusInternalServerError {
		return nil, &appleTransientError{status: response.StatusCode}
	}
	return []byte(response.Body), nil
}

func (f *appleFlow) getObject(rawURL, label string, html, profile bool, accept string) (map[string]any, error) {
	body, err := f.request(http.MethodGet, rawURL, nil, html, profile, false, accept)
	if err != nil {
		return nil, err
	}
	return decodeObject(body, label)
}

func (f *appleFlow) postObject(rawURL string, payload any, label string, sendHashcash, profile bool) (map[string]any, error) {
	body, err := f.request(http.MethodPost, rawURL, payload, false, profile, sendHashcash, "application/json, text/javascript, */*; q=0.01")
	if err != nil {
		return nil, err
	}
	return decodeObject(body, label)
}

func (f *appleFlow) putObject(rawURL string, payload any, label string, profile bool) (map[string]any, error) {
	body, err := f.request(http.MethodPut, rawURL, payload, false, profile, false, "application/json, text/javascript, */*; q=0.01")
	if err != nil {
		return nil, err
	}
	return decodeObject(body, label)
}

func (f *appleFlow) headers(rawURL string, html, profile, sendHashcash bool, accept string) (map[string]string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := parsed.Hostname()
	clientInfo, err := fdClientInfo(time.Now())
	if err != nil {
		return nil, err
	}
	if profile {
		headers := map[string]string{
			"User-Agent":                appleUserAgent,
			"Accept":                    "application/json, text/plain, */*",
			"Accept-Language":           "zh-CN,zh;q=0.9",
			"Content-Type":              "application/json",
			"Origin":                    appleAccountURL,
			"Referer":                   appleAccountURL + "/",
			"X-Apple-I-FD-Client-Info":  clientInfo,
			"X-Apple-I-Request-Context": "ca",
			"X-Apple-I-TimeZone":        "Asia/Shanghai",
		}
		if f.apiKey != "" {
			headers["X-Apple-Api-Key"] = f.apiKey
		}
		if f.scnt[host] != "" {
			headers["scnt"] = f.scnt[host]
		}
		return headers, nil
	}
	origin := appleIDMSAURL
	if host != "" {
		origin = "https://" + host
	}
	headers := map[string]string{
		"User-Agent":               appleUserAgent,
		"Accept":                   accept,
		"Accept-Language":          "zh-CN,zh;q=0.9",
		"Origin":                   origin,
		"Referer":                  origin + "/",
		"X-Apple-I-FD-Client-Info": clientInfo,
		"X-Apple-I-TimeZone":       "Asia/Shanghai",
		"X-Apple-Privacy-Consent":  "true",
	}
	if !html {
		headers["Content-Type"] = "application/json"
		headers["X-Requested-With"] = "XMLHttpRequest"
	}
	if f.sessionID != "" {
		headers["X-Apple-ID-Session-Id"] = f.sessionID
	}
	if f.scnt[host] != "" {
		headers["scnt"] = f.scnt[host]
	}
	if strings.HasSuffix(host, "idmsa.apple.com") {
		headers["X-Apple-Domain-Id"] = "11"
		headers["X-Apple-Frame-Id"] = f.frameID
		headers["X-Apple-OAuth-Client-Id"] = f.widgetKey
		headers["X-Apple-OAuth-Client-Type"] = "firstPartyAuth"
		headers["X-Apple-OAuth-Redirect-URI"] = appleAccountURL
		headers["X-Apple-OAuth-Response-Mode"] = "web_message"
		headers["X-Apple-OAuth-Response-Type"] = "code"
		headers["X-Apple-OAuth-State"] = f.frameID
		headers["X-Apple-Privacy-Consent-Accepted"] = "true"
		headers["X-Apple-Widget-Key"] = f.widgetKey
		if f.authAttributes != "" {
			headers["X-Apple-Auth-Attributes"] = f.authAttributes
		}
		if f.repairToken != "" {
			headers["X-Apple-Repair-Session-Token"] = f.repairToken
		}
		if sendHashcash && f.hashcash != "" && f.hashcashBits > 0 {
			stamp, err := solveHashcash(f.ctx, f.hashcash, f.hashcashBits)
			if err != nil {
				return nil, err
			}
			headers["X-APPLE-HC"] = stamp
		}
	}
	if strings.HasSuffix(host, "appleid.apple.com") {
		headers["X-Apple-Widget-Key"] = f.widgetKey
		skipAttributes := []string{}
		if f.skipHSA2 {
			skipAttributes = append(skipAttributes, "hsa2_enrollment")
		}
		encoded, _ := json.Marshal(skipAttributes)
		headers["X-Apple-Skip-Repair-Attributes"] = string(encoded)
		if f.oauthContext != "" {
			headers["X-Apple-OAuth-Context"] = f.oauthContext
		}
		if f.repairToken != "" {
			headers["X-Apple-Session-Token"] = f.repairToken
		}
		if f.skipHSA2 {
			headers["x-apple-i-cont-x-apple-i-two-factor-upsell-skipped"] = "true"
		}
	}
	return headers, nil
}

type headerGetter interface {
	Get(string) string
}

func (f *appleFlow) absorb(headers headerGetter, host string) {
	if value := strings.TrimSpace(headers.Get("scnt")); value != "" {
		f.scnt[host] = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-ID-Session-Id")); value != "" {
		f.sessionID = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-Auth-Attributes")); value != "" {
		f.authAttributes = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-ID-Account-Country")); value != "" {
		f.accountCountry = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-HC-Bits")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			f.hashcashBits = parsed
		}
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-HC-Challenge")); value != "" {
		f.hashcash = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-OAuth-Context")); value != "" {
		f.oauthContext = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-Repair-Session-Token")); value != "" {
		f.repairToken = value
	} else if value := strings.TrimSpace(headers.Get("X-Apple-Session-Token")); value != "" {
		f.repairToken = value
	}
	if strings.EqualFold(strings.TrimSpace(headers.Get("X-Apple-I-Cont-X-Apple-I-Two-Factor-Upsell-Skipped")), "true") {
		f.skipHSA2 = true
	}
	location := strings.TrimSpace(headers.Get("Location"))
	if strings.HasPrefix(location, "/") || strings.HasPrefix(location, "http") {
		f.location = location
	} else {
		f.location = ""
	}
}

func fetchAppleAccount(flow *appleFlow) (map[string]any, error) {
	delete(flow.scnt, "appleid.apple.com")
	if _, err := flow.request(http.MethodGet, appleIDURL+"/account/manage/gs/ws/token", nil, false, true, false, "application/json, text/plain, */*"); err != nil {
		return nil, err
	}
	profile, err := flow.getObject(appleIDURL+"/account/manage", "account/manage", false, true, "application/json, text/plain, */*")
	if err != nil {
		return nil, err
	}
	if flow.status != http.StatusOK || (asMap(profile["account"]) == nil && asMap(profile["appleID"]) == nil) {
		return nil, providerError(fmt.Sprintf("account/manage failed with HTTP %d", flow.status), profile)
	}
	flow.apiKey = valueString(profile["apiKey"])
	return profile, nil
}

func appleAccountRegion(profile map[string]any, sessionCountry, fallback string) (string, error) {
	for _, path := range [][]string{{"dsInfo", "countryCode"}, {"account", "countryCode"}, {"appleID", "countryCode"}, {"countryCode"}} {
		var current any = profile
		for _, key := range path {
			current = asMap(current)[key]
		}
		if code := normalizeAppleCountryCode(valueString(current)); code != "" {
			if label, ok := appleCountryRegionLabels[code]; ok {
				return label + "区", nil
			}
			return code, nil
		}
	}
	if code := normalizeAppleCountryCode(sessionCountry); code != "" {
		if label, ok := appleCountryRegionLabels[code]; ok {
			return label + "区", nil
		}
		return code, nil
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback, nil
	}
	return "", errors.New("account profile did not return countryCode")
}

func normalizeAppleCountryCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if code, ok := appleCountryCodeAliases[value]; ok {
		return code
	}
	switch value {
	case "UK":
		return "GB"
	default:
		return value
	}
}

var appleCountryCodeAliases = map[string]string{
	"USA": "US", "CAN": "CA", "CHN": "CN", "HKG": "HK", "TWN": "TW", "MAC": "MO",
	"JPN": "JP", "KOR": "KR", "GBR": "GB", "AUS": "AU", "NZL": "NZ", "SGP": "SG",
	"MYS": "MY", "THA": "TH", "VNM": "VN", "PHL": "PH", "IDN": "ID", "IND": "IN",
	"DEU": "DE", "FRA": "FR", "ITA": "IT", "ESP": "ES", "PRT": "PT", "NLD": "NL",
	"BEL": "BE", "AUT": "AT", "CHE": "CH", "SWE": "SE", "NOR": "NO", "DNK": "DK",
	"FIN": "FI", "POL": "PL", "IRL": "IE", "TUR": "TR", "MEX": "MX", "BRA": "BR",
	"ARG": "AR", "SAU": "SA", "ARE": "AE", "DOM": "DO", "KWT": "KW", "PAK": "PK",
	"VEN": "VE",
}

var appleCountryRegionLabels = map[string]string{
	"US": "美国", "CA": "加拿大", "CN": "中国", "HK": "香港", "TW": "台湾", "MO": "澳门",
	"JP": "日本", "KR": "韩国", "GB": "英国", "AU": "澳大利亚", "NZ": "新西兰", "SG": "新加坡",
	"MY": "马来西亚", "TH": "泰国", "VN": "越南", "PH": "菲律宾", "ID": "印度尼西亚", "IN": "印度",
	"DE": "德国", "FR": "法国", "IT": "意大利", "ES": "西班牙", "PT": "葡萄牙", "NL": "荷兰",
	"BE": "比利时", "AT": "奥地利", "CH": "瑞士", "SE": "瑞典", "NO": "挪威", "DK": "丹麦",
	"FI": "芬兰", "PL": "波兰", "IE": "爱尔兰", "TR": "土耳其", "MX": "墨西哥", "BR": "巴西",
	"AR": "阿根廷", "SA": "沙特", "AE": "阿联酋", "DO": "多米尼加", "KW": "科威特", "PK": "巴基斯坦", "VE": "委内瑞拉",
}

func updateAppleQuestions(flow *appleFlow, profile map[string]any, password string, replacements [3]string) ([3]securityAnswer, error) {
	var empty [3]securityAnswer
	questions := accountQuestionList(profile)
	if len(questions) != 3 {
		return empty, fmt.Errorf("account profile returned %d security questions, expected 3", len(questions))
	}
	entries := [3]securityAnswer{{Answer: replacements[0]}, {Answer: replacements[1]}, {Answer: replacements[2]}}
	answers, ok := matchQuestionAnswers(entries, questions)
	if !ok {
		return empty, fmt.Errorf("replacement security answers do not match account questions")
	}
	payload := make([]map[string]any, 0, len(questions))
	for index, question := range questions {
		payload = append(payload, map[string]any{
			"answer":   answers[index],
			"id":       question["id"],
			"question": question["question"],
		})
	}
	if _, err := putAppleManage(flow, "/account/manage/security/questions", map[string]any{"questions": payload}, "security/questions", password); err != nil {
		return empty, err
	}
	return orderQuestionOutput(questions, answers)
}

func updateAppleBirthday(flow *appleFlow, birthdayISO, password string) error {
	date, err := time.Parse("2006-01-02", birthdayISO)
	if err != nil {
		return fmt.Errorf("birthday is invalid")
	}
	payload := map[string]any{
		"dayOfMonth":  fmt.Sprintf("%02d", date.Day()),
		"monthOfYear": fmt.Sprintf("%02d", int(date.Month())),
		"year":        fmt.Sprintf("%04d", date.Year()),
	}
	verified, err := flow.postObject(appleIDURL+"/account/manage/security/birthday/verify", payload, "birthday/verify", false, true)
	if err != nil {
		return err
	}
	if flow.status == http.StatusUnavailableForLegalReasons {
		if err := authenticateApplePassword(flow, password); err != nil {
			return err
		}
		verified, err = flow.postObject(appleIDURL+"/account/manage/security/birthday/verify", payload, "birthday/verify", false, true)
		if err != nil {
			return err
		}
	}
	if flow.status != http.StatusOK {
		return providerError(fmt.Sprintf("birthday/verify failed with HTTP %d", flow.status), verified)
	}
	_, err = putAppleManage(flow, "/account/manage/security/birthday", payload, "security/birthday", password)
	return err
}

func updateApplePassword(flow *appleFlow, current, replacement string) error {
	_, err := putAppleManage(flow, "/account/manage/security/password", map[string]any{
		"currentPassword": current,
		"newPassword":     replacement,
	}, "security/password", current)
	return err
}

func putAppleManage(flow *appleFlow, path string, payload any, label, password string) (map[string]any, error) {
	result, err := flow.putObject(appleIDURL+path, payload, label, true)
	if err != nil {
		return nil, err
	}
	if flow.status == http.StatusUnavailableForLegalReasons {
		if err := authenticateApplePassword(flow, password); err != nil {
			return nil, err
		}
		result, err = flow.putObject(appleIDURL+path, payload, label, true)
		if err != nil {
			return nil, err
		}
	}
	if flow.status != http.StatusOK {
		return nil, providerError(fmt.Sprintf("%s failed with HTTP %d", label, flow.status), result)
	}
	return result, nil
}

func authenticateApplePassword(flow *appleFlow, password string) error {
	result, err := flow.postObject(appleIDURL+"/authenticate/password", map[string]any{"password": password}, "authenticate/password", false, true)
	if err != nil {
		return err
	}
	if flow.status != http.StatusOK && flow.status != http.StatusNoContent {
		return providerError(fmt.Sprintf("current password verification failed with HTTP %d", flow.status), result)
	}
	return nil
}

func accountQuestionList(profile map[string]any) []map[string]any {
	return questionList(asMap(asMap(profile["account"])["security"])["questions"])
}

func appleAccountUsesHSA2(profile map[string]any) bool {
	if valueBool(profile["isHsa"]) {
		return true
	}
	return strings.EqualFold(valueString(asMap(profile["account"])["type"]), "hsa2")
}

func matchQuestionAnswers(entries [3]securityAnswer, questions []map[string]any) ([]string, bool) {
	byQuestion := make(map[string]string, len(entries))
	for _, entry := range entries {
		if key := normalizeQuestion(entry.Question); key != "" && entry.Answer != "" {
			byQuestion[key] = entry.Answer
		}
	}
	if len(byQuestion) > 0 {
		answers := make([]string, len(questions))
		matched := true
		for index, question := range questions {
			answers[index] = byQuestion[normalizeQuestion(valueString(question["question"]))]
			matched = matched && answers[index] != ""
		}
		if matched {
			return answers, true
		}
	}

	answers := make([]string, len(questions))
	knownIDs := true
	for index, question := range questions {
		order, ok := appleQuestionOrder[valueString(question["id"])]
		if !ok || entries[order].Answer == "" {
			knownIDs = false
			break
		}
		answers[index] = entries[order].Answer
	}
	if knownIDs {
		return answers, true
	}
	if len(questions) != len(entries) {
		return nil, false
	}
	for index := range entries {
		if entries[index].Answer == "" {
			return nil, false
		}
		answers[index] = entries[index].Answer
	}
	return answers, true
}

func securityQuestionAnswerAttempts(current [3]securityAnswer, replacements [3]string, questions []map[string]any) ([][]string, bool) {
	currentAnswers, ok := matchQuestionAnswers(current, questions)
	if !ok {
		return nil, false
	}
	attempts := [][]string{currentAnswers}
	target := [3]securityAnswer{{Answer: replacements[0]}, {Answer: replacements[1]}, {Answer: replacements[2]}}
	targetAnswers, ok := matchQuestionAnswers(target, questions)
	if !ok {
		return attempts, true
	}
	same := len(currentAnswers) == len(targetAnswers)
	for index := range currentAnswers {
		same = same && currentAnswers[index] == targetAnswers[index]
	}
	if !same {
		attempts = append(attempts, targetAnswers)
	}
	return attempts, true
}

var appleQuestionOrder = map[string]int{"130": 0, "136": 1, "142": 2}

func orderQuestionOutput(questions []map[string]any, answers []string) ([3]securityAnswer, error) {
	var output [3]securityAnswer
	type ordered struct {
		order int
		value securityAnswer
	}
	values := make([]ordered, 0, len(questions))
	for index, question := range questions {
		order, ok := appleQuestionOrder[valueString(question["id"])]
		if !ok {
			order = len(appleQuestionOrder) + index
		}
		label := valueString(question["question"])
		if label == "" {
			label = valueString(question["id"])
		}
		values = append(values, ordered{order: order, value: securityAnswer{Question: label, Answer: answers[index]}})
	}
	sort.SliceStable(values, func(i, j int) bool { return values[i].order < values[j].order })
	if len(values) != len(output) {
		return output, fmt.Errorf("account returned %d security questions, expected 3", len(values))
	}
	for index := range output {
		output[index] = values[index].value
	}
	return output, nil
}

func normalizeBirthday(raw string) (string, error) {
	parts := strings.Split(strings.ReplaceAll(strings.TrimSpace(raw), "/", "-"), "-")
	if len(parts) != 3 {
		return "", errorsNewBirthday()
	}
	year, errYear := strconv.Atoi(parts[0])
	month, errMonth := strconv.Atoi(parts[1])
	day, errDay := strconv.Atoi(parts[2])
	if errYear != nil || errMonth != nil || errDay != nil || year < 1 {
		return "", errorsNewBirthday()
	}
	value := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if value.Year() != year || int(value.Month()) != month || value.Day() != day {
		return "", errorsNewBirthday()
	}
	return value.Format("2006-01-02"), nil
}

func errorsNewBirthday() error {
	return fmt.Errorf("birthday must use YYYY-MM-DD or YYYY/MM/DD")
}

func parseBootArgs(body string) map[string]any {
	for _, match := range bootArgsPattern.FindAllStringSubmatch(body, -1) {
		if len(match) != 2 {
			continue
		}
		data, err := decodeObject([]byte(match[1]), "boot_args")
		if err != nil {
			continue
		}
		direct := asMap(data["direct"])
		if direct == nil {
			direct = data
		}
		twoSV := asMap(direct["twoSV"])
		if asMap(twoSV["securityQuestions"]) != nil || valueString(direct["authType"]) != "" || valueString(direct["authInitialRoute"]) != "" {
			return direct
		}
	}
	return map[string]any{}
}

func decodeObject(body []byte, label string) (map[string]any, error) {
	if strings.TrimSpace(string(body)) == "" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("%s response is not JSON: %q", label, preview)
	}
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

func providerError(prefix string, data map[string]any) error {
	if detail := serviceErrorText(data); detail != "" {
		return fmt.Errorf("%s: %s", prefix, detail)
	}
	return fmt.Errorf("%s", prefix)
}

func serviceErrorText(data map[string]any) string {
	values := asSlice(data["serviceErrors"])
	if len(values) == 0 {
		values = asSlice(data["service_errors"])
	}
	messages := make([]string, 0, len(values))
	for _, value := range values {
		item := asMap(value)
		message := valueString(item["message"])
		if message == "" {
			message = valueString(item["code"])
		}
		if message != "" {
			messages = append(messages, message)
		}
	}
	return strings.Join(messages, "; ")
}

func questionList(value any) []map[string]any {
	values := asSlice(value)
	questions := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if question := asMap(value); question != nil {
			questions = append(questions, question)
		}
	}
	return questions
}

func asMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func asSlice(value any) []any {
	result, _ := value.([]any)
	return result
}

func valueString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}

func valueInt(value any) (int, error) {
	return strconv.Atoi(valueString(value))
}

func valueBool(value any) bool {
	result, _ := value.(bool)
	return result
}

func normalizeQuestion(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
