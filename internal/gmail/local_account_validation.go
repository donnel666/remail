package gmail

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	localGmailLoginURL                = "https://accounts.google.com/v3/signin/identifier?continue=https%3A%2F%2Fmyaccount.google.com%2Fapppasswords&flowName=GlifWebSignIn&flowEntry=ServiceLogin"
	localGmailAuthenticatorURL        = "https://myaccount.google.com/two-step-verification/authenticator"
	localGmailAppPasswordsURL         = "https://myaccount.google.com/apppasswords"
	localGmailTOTPPeriod              = 30
	localGmailLoginTOTPAttempts       = 3
	localGmailChangeTOTPAttempts      = 5
	localGmailBrowserProxyAttempts    = 3
	localGmailBrowserMaxProxyAttempts = 20
)

const localGmailPasswordSelector = `input[name="Passwd"], input[type="password"], input[autocomplete="current-password"]`

var (
	localGmailAppPasswordPattern         = regexp.MustCompile(`(?i)(?:^|[^a-z])([a-z]{4}(?:\s+[a-z]{4}){3})(?:[^a-z]|$)`)
	localGmailAuthenticatorSecretPattern = regexp.MustCompile(`(?i)\b([a-z2-7]{4}(?:\s+[a-z2-7]{4}){7})\b`)
)

type localGmailBrowserFingerprint struct {
	Seed            uint64
	UserAgent       string
	Platform        string
	PlatformVersion string
}

type localGmailAccountRotateFunc func(
	context.Context,
	localGmailValidationInput,
	string,
	localGmailBrowserFingerprint,
) localGmailValidationResult

type localGmailAccountError struct {
	err          error
	safeError    string
	temporary    bool
	proxyFailure bool
}

func (e *localGmailAccountError) Error() string {
	if e == nil || e.err == nil {
		return "gmail account validation failed"
	}
	return e.err.Error()
}

func (e *localGmailAccountError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (s *Service) validateLocalGmailAccount(ctx context.Context, input localGmailValidationInput) localGmailValidationResult {
	return validateLocalGmailAccountWith(ctx, s.validationProxies, input, rotateLocalGmailAccount)
}

func validateLocalGmailAccountWith(
	ctx context.Context,
	proxies localGmailPickupProxyProvider,
	input localGmailValidationInput,
	rotate localGmailAccountRotateFunc,
) localGmailValidationResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if rotate == nil || input.ResourceID == 0 || input.ValidationGeneration == 0 ||
		strings.TrimSpace(input.Email) == "" || strings.TrimSpace(input.Password) == "" {
		return localGmailValidationResult{SafeError: "Gmail credentials are incomplete.", Err: ErrLocalValidationConflict}
	}
	maxProxyAttempts := min(
		runtimeconfig.Int("max_proxy_attempts", localGmailBrowserProxyAttempts, 1),
		localGmailBrowserMaxProxyAttempts,
	)
	var avoidServerIDs []uint
	var last localGmailValidationResult
	for attempt := 0; attempt <= maxProxyAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return localGmailValidationResult{SafeError: "Gmail validation was interrupted.", Temporary: true, Err: err}
		}
		config, err := acquireLocalGmailValidationProxy(ctx, proxies, input, attempt, avoidServerIDs)
		if err != nil {
			return localGmailValidationResult{
				SafeError: "Gmail validation proxy is temporarily unavailable.", Temporary: true, Err: err,
			}
		}
		proxyURL, proxyID := "", uint(0)
		if config != nil && !config.Direct {
			proxyURL, proxyID = config.URL, config.ID
		}
		last = rotate(ctx, input, proxyURL, newLocalGmailBrowserFingerprintAttempt(input, attempt))
		if last.Err == nil {
			reportLocalGmailValidationProxy(ctx, proxies, proxyID, true, "")
			return last
		}
		if ctx.Err() != nil {
			return last
		}
		// A successful remote credential mutation is irreversible from this
		// attempt's perspective. Persist the partial result before changing
		// proxies; retrying with the old secret can lock the account out.
		if last.TwoFactorAuthoritative || last.AppPasswordAuthoritative || last.TwoFactorRevoked || last.AppPasswordRevoked {
			if last.ProxyFailure {
				reportLocalGmailValidationProxy(ctx, proxies, proxyID, false, last.SafeError)
			} else {
				reportLocalGmailValidationProxy(ctx, proxies, proxyID, true, "")
			}
			return last
		}
		if last.ProxyFailure && proxyID != 0 {
			avoidServerIDs = proxyapp.AppendAvoidProxyServerID(avoidServerIDs, config)
			reportLocalGmailValidationProxy(ctx, proxies, proxyID, false, last.SafeError)
			continue
		}
		if last.ProxyFailure && proxyID == 0 && attempt < maxProxyAttempts {
			continue
		}
		reportLocalGmailValidationProxy(ctx, proxies, proxyID, true, "")
		return last
	}
	if last.Err == nil {
		last = localGmailValidationResult{
			SafeError: "Gmail validation is temporarily unavailable.", Temporary: true,
			Err: ErrLocalValidationDependency,
		}
	}
	return last
}

func acquireLocalGmailValidationProxy(
	ctx context.Context,
	proxies localGmailPickupProxyProvider,
	input localGmailValidationInput,
	attempt int,
	avoidServerIDs []uint,
) (*proxyapp.ProxyConfig, error) {
	if proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(input.Email)),
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeAuth,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           strings.TrimSpace(input.RequestID),
		AvoidProxyServerIDs: avoidServerIDs,
	})
}

func reportLocalGmailValidationProxy(
	ctx context.Context,
	proxies localGmailPickupProxyProvider,
	proxyID uint,
	success bool,
	safeError string,
) {
	if proxies == nil || proxyID == 0 {
		return
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if success {
		_ = proxies.ReportSuccess(reportCtx, proxyID)
		return
	}
	_ = proxies.ReportFailure(reportCtx, proxyID, strings.TrimSpace(safeError))
}

func newLocalGmailBrowserFingerprint(input localGmailValidationInput) localGmailBrowserFingerprint {
	return newLocalGmailBrowserFingerprintAttempt(input, 0)
}

func newLocalGmailBrowserFingerprintAttempt(input localGmailValidationInput, attempt int) localGmailBrowserFingerprint {
	raw := stableDigest(fmt.Sprintf("gmail-browser|%d|%d|%d|%s", input.ResourceID, input.ValidationGeneration, attempt, strings.ToLower(strings.TrimSpace(input.Email))))
	seed, _ := strconv.ParseUint(raw[:16], 16, 64)
	return localGmailBrowserFingerprint{
		Seed:            seed%2_000_000_000 + 1000,
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
		Platform:        "Windows",
		PlatformVersion: "10.0.0",
	}
}

func rotateLocalGmailAccount(
	ctx context.Context,
	input localGmailValidationInput,
	proxyURL string,
	fingerprint localGmailBrowserFingerprint,
) localGmailValidationResult {
	operationCtx, cancel := context.WithTimeout(ctx, localGmailValidationTimeout)
	defer cancel()
	browser, err := newLocalGmailBrowser(operationCtx, proxyURL, fingerprint)
	if err != nil {
		return localGmailValidationFailure(localGmailValidationResult{}, err)
	}
	defer browser.Close()

	if err := browser.login(input); err != nil {
		return localGmailValidationFailure(localGmailValidationResult{}, err)
	}
	result := localGmailValidationResult{}
	if strings.TrimSpace(input.TwoFactorSecret) == "" {
		secret, err := browser.replaceAuthenticator(input.Password, "")
		result.TwoFactorRevoked = browser.twoFactorRevoked
		if secret != "" {
			result.TwoFactorSecret = secret
			result.TwoFactorAuthoritative = true
		}
		if err != nil {
			return localGmailValidationFailure(result, err)
		}
		appPassword, err := browser.replaceAppPasswords(input.Password, secret)
		result.AppPasswordRevoked = browser.appPasswordRevoked
		if appPassword != "" {
			result.AppPassword = appPassword
			result.AppPasswordAuthoritative = true
		}
		if err != nil {
			return localGmailValidationFailure(result, err)
		}
	} else {
		appPassword, err := browser.replaceAppPasswords(input.Password, input.TwoFactorSecret)
		result.AppPasswordRevoked = browser.appPasswordRevoked
		if appPassword != "" {
			result.AppPassword = appPassword
			result.AppPasswordAuthoritative = true
		}
		if err != nil {
			return localGmailValidationFailure(result, err)
		}
		secret, err := browser.replaceAuthenticator(input.Password, input.TwoFactorSecret)
		result.TwoFactorRevoked = browser.twoFactorRevoked
		if secret != "" {
			result.TwoFactorSecret = secret
			result.TwoFactorAuthoritative = true
		}
		if err != nil {
			return localGmailValidationFailure(result, err)
		}
	}
	if err := verifyLocalGmailAppPassword(operationCtx, input.Email, result.AppPassword, proxyURL); err != nil {
		return localGmailValidationFailure(result, err)
	}
	return result
}

func localGmailValidationFailure(result localGmailValidationResult, err error) localGmailValidationResult {
	result.Err = err
	result.SafeError = "Gmail account validation is temporarily unavailable."
	result.Temporary = true
	result.ProxyFailure = isLocalGmailBrowserProxyFailure(err)
	var accountErr *localGmailAccountError
	if errors.As(err, &accountErr) {
		result.SafeError = strings.TrimSpace(accountErr.safeError)
		result.Temporary = accountErr.temporary
		result.ProxyFailure = accountErr.proxyFailure
	}
	if result.SafeError == "" {
		result.SafeError = "Gmail account validation failed."
	}
	return result
}

func validLocalGmailRotatedCredentials(secret, appPassword string) bool {
	return validLocalGmailTOTPSecret(secret) && validLocalGmailAppPassword(appPassword)
}

func extractLastLocalGmailAppPassword(body string) (string, bool) {
	matches := localGmailAppPasswordPattern.FindAllStringSubmatch(body, -1)
	for index := len(matches) - 1; index >= 0; index-- {
		if len(matches[index]) != 2 {
			continue
		}
		candidate := removeWhitespace(matches[index][1])
		if validLocalGmailAppPassword(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func verifyLocalGmailAppPassword(ctx context.Context, email, appPassword, proxyURL string) error {
	if !validLocalGmailAppPassword(appPassword) {
		return &localGmailAccountError{err: errors.New("generated Gmail app password is invalid"), safeError: "Gmail returned an invalid App Password."}
	}
	client, closeClient, err := openLocalGmailPickupIMAP(ctx, email, appPassword, proxyURL, newLocalGmailClientFingerprint())
	if err != nil {
		if errors.Is(err, errLocalGmailAuthentication) {
			return &localGmailAccountError{
				err: err, safeError: "Gmail rejected the new App Password.", temporary: true,
			}
		}
		return localGmailBrowserTransportError(err, strings.TrimSpace(proxyURL) != "")
	}
	defer closeClient()
	_ = client.Logout().Wait()
	return nil
}

type localGmailBrowser struct {
	ctx                context.Context
	cancel             context.CancelFunc
	allocCancel        context.CancelFunc
	proxyClose         func()
	email              string
	rapt               string
	bindingEmail       string
	usesProxy          bool
	twoFactorRevoked   bool
	appPasswordRevoked bool
}

func newLocalGmailBrowser(
	ctx context.Context,
	proxyURL string,
	fingerprint localGmailBrowserFingerprint,
) (*localGmailBrowser, error) {
	executable, err := localGmailBrowserExecutable()
	if err != nil {
		return nil, &localGmailAccountError{
			err: err, safeError: "Gmail validation browser is unavailable.", temporary: true,
		}
	}
	proxyServer, proxyUser, proxyPassword, err := localGmailBrowserProxy(proxyURL)
	if err != nil {
		return nil, &localGmailAccountError{
			err: err, safeError: "Gmail validation proxy configuration is invalid.", proxyFailure: true, temporary: true,
		}
	}
	var proxyClose func()
	if localGmailBrowserNeedsSOCKSBridge(proxyServer) {
		bridge, err := newLocalGmailSOCKSBridge(proxyServer, proxyUser, proxyPassword)
		if err != nil {
			return nil, &localGmailAccountError{
				err: err, safeError: "Gmail validation proxy configuration is invalid.", proxyFailure: true, temporary: true,
			}
		}
		proxyServer, proxyUser, proxyPassword = bridge.URL(), "", ""
		proxyClose = bridge.Close
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(executable),
		chromedp.NoSandbox,
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("fingerprint", strconv.FormatUint(fingerprint.Seed, 10)),
		chromedp.Flag("fingerprint-platform", "windows"),
		chromedp.WindowSize(1366, 768),
	)
	if proxyServer != "" {
		opts = append(opts, chromedp.ProxyServer(proxyServer))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	browser := &localGmailBrowser{ctx: browserCtx, cancel: browserCancel, allocCancel: allocCancel, proxyClose: proxyClose, usesProxy: proxyServer != ""}
	if proxyUser != "" {
		if err := browser.enableProxyAuthentication(proxyUser, proxyPassword); err != nil {
			browser.Close()
			return nil, localGmailBrowserTransportError(err, true)
		}
	}
	if err := chromedp.Run(browserCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`(() => {
				Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
				Object.defineProperty(navigator, 'platform', {get: () => 'Win32'});
			})()`).Do(ctx)
			if err != nil {
				return err
			}
			metadata := &emulation.UserAgentMetadata{
				Brands: []*emulation.UserAgentBrandVersion{
					{Brand: "Chromium", Version: "151"},
					{Brand: "Google Chrome", Version: "151"},
					{Brand: "Not(A:Brand", Version: "99"},
				},
				FullVersionList: []*emulation.UserAgentBrandVersion{
					{Brand: "Chromium", Version: "151.0.0.0"},
					{Brand: "Google Chrome", Version: "151.0.0.0"},
					{Brand: "Not(A:Brand", Version: "99.0.0.0"},
				},
				Platform:        fingerprint.Platform,
				PlatformVersion: fingerprint.PlatformVersion,
				Architecture:    "x86",
				Bitness:         "64",
			}
			return emulation.SetUserAgentOverride(fingerprint.UserAgent).
				WithAcceptLanguage("en-US,en;q=0.9").
				WithPlatform("Win32").
				WithUserAgentMetadata(metadata).Do(ctx)
		}),
	); err != nil {
		browser.Close()
		return nil, localGmailBrowserTransportError(err, proxyServer != "")
	}
	return browser, nil
}

func (b *localGmailBrowser) Close() {
	if b == nil {
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	if b.proxyClose != nil {
		b.proxyClose()
	}
}

func (b *localGmailBrowser) enableProxyAuthentication(username, password string) error {
	chromedp.ListenTarget(b.ctx, func(event any) {
		switch event := event.(type) {
		case *fetch.EventAuthRequired:
			go func() {
				_ = chromedp.Run(b.ctx, fetch.ContinueWithAuth(event.RequestID, localGmailProxyAuthResponse(event.AuthChallenge, username, password)))
			}()
		case *fetch.EventRequestPaused:
			go func() { _ = chromedp.Run(b.ctx, fetch.ContinueRequest(event.RequestID)) }()
		}
	})
	return chromedp.Run(b.ctx, fetch.Enable().WithHandleAuthRequests(true))
}

func localGmailProxyAuthResponse(challenge *fetch.AuthChallenge, username, password string) *fetch.AuthChallengeResponse {
	response := &fetch.AuthChallengeResponse{Response: fetch.AuthChallengeResponseResponseCancelAuth}
	if challenge != nil && challenge.Source == fetch.AuthChallengeSourceProxy {
		response.Response = fetch.AuthChallengeResponseResponseProvideCredentials
		response.Username = username
		response.Password = password
	}
	return response
}

func (b *localGmailBrowser) login(input localGmailValidationInput) error {
	b.email = strings.ToLower(strings.TrimSpace(input.Email))
	b.bindingEmail = strings.ToLower(strings.TrimSpace(input.BindingEmail))
	if err := b.navigate(localGmailLoginURL); err != nil {
		return err
	}
	if err := b.waitSelector(`input[type="email"], input[name="identifier"]`, 20*time.Second); err != nil {
		return b.classifyPageError(err)
	}
	if err := b.fill(`input[type="email"], input[name="identifier"]`, input.Email); err != nil {
		return err
	}
	if err := b.clickText([]string{"Next", "下一步"}, true); err != nil {
		return err
	}
	if err := b.waitSelector(`input[name="Passwd"]`, 20*time.Second); err != nil {
		return b.classifyPageError(err)
	}
	if err := b.fill(`input[name="Passwd"]`, input.Password); err != nil {
		return err
	}
	if err := b.clickText([]string{"Next", "下一步"}, true); err != nil {
		return err
	}
	if err := b.finishLogin(input.Password, input.TwoFactorSecret); err != nil {
		return err
	}
	location, _ := b.location()
	b.rapt = localGmailRAPT(location)
	return nil
}

func (b *localGmailBrowser) finishLogin(password, secret string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		location, _ := b.location()
		if localGmailOnAccountHost(location) {
			return nil
		}
		if visible, _ := b.selectorVisible(localGmailPasswordSelector); visible || strings.Contains(location, "/challenge/pwd") {
			if err := b.fill(localGmailPasswordSelector, password); err != nil {
				return err
			}
			if err := b.clickText([]string{"Next", "下一步"}, true); err != nil {
				return err
			}
			if err := sleepLocalGmailBrowser(b.ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if visible, _ := b.selectorVisible(`input[name="totpPin"], input[autocomplete="one-time-code"]`); visible || strings.Contains(location, "/challenge/totp") {
			if strings.TrimSpace(secret) == "" {
				return &localGmailAccountError{
					err:       errors.New("google requested an existing authenticator code"),
					safeError: "Gmail account requires its existing 2FA secret.",
				}
			}
			if err := b.submitTOTP(secret, []string{"Next", "下一步"}, `input[name="totpPin"], input[autocomplete="one-time-code"], input[type="tel"]`, localGmailLoginTOTPAttempts); err != nil {
				return err
			}
			continue
		}
		handled, err := b.completeBindingEmailChallenge(secret)
		if err != nil {
			return err
		}
		if handled {
			continue
		}
		if err := b.classifyPageError(nil); err != nil {
			return err
		}
		if err := sleepLocalGmailBrowser(b.ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
	return b.classifyPageError(errors.New("google login did not complete"))
}

func (b *localGmailBrowser) replaceAuthenticator(password, currentSecret string) (string, error) {
	if err := b.navigate(localGmailURLWithRAPT(localGmailAuthenticatorURL, b.rapt)); err != nil {
		return "", err
	}
	if err := b.completeReauthentication(password, currentSecret); err != nil {
		return "", err
	}
	clicked, err := b.clickAnyTextWithin([]string{
		"Change authenticator app", "更换身份验证器应用",
		"Set up authenticator", "设置身份验证器", "Add authenticator app", "添加身份验证器应用",
	}, 15*time.Second)
	if err != nil {
		return "", err
	}
	if !clicked {
		turnedOn, turnErr := b.clickAnyTextWithin([]string{"Turn on 2-Step Verification", "开启两步验证", "Get started", "开始使用"}, 12*time.Second)
		if turnErr != nil {
			return "", turnErr
		}
		if turnedOn {
			if err := b.completeReauthentication(password, currentSecret); err != nil {
				return "", err
			}
			if err := b.navigate(localGmailURLWithRAPT(localGmailAuthenticatorURL, b.rapt)); err != nil {
				return "", err
			}
			clicked, err = b.clickAnyTextWithin([]string{"Set up authenticator", "设置身份验证器", "Add authenticator app", "添加身份验证器应用"}, 15*time.Second)
			if err != nil {
				return "", err
			}
		}
	}
	if !clicked {
		return "", b.classifyPageError(errors.New("authenticator setup control was not found"))
	}

	manualLabels := []string{"Can't scan it", "Can’t scan it", "无法扫描", "无法扫码"}
	manualClicked, err := b.clickAnyTextWithin(manualLabels, 10*time.Second)
	if err != nil {
		return "", err
	}
	if !manualClicked {
		_, _ = b.clickAnyText([]string{"Next", "下一步"})
		manualClicked, err = b.clickAnyTextWithin(manualLabels, 7*time.Second)
		if err != nil {
			return "", err
		}
	}
	if !manualClicked {
		return "", b.classifyPageError(errors.New("manual authenticator key control was not found"))
	}
	body, err := b.waitBodyMatch(localGmailAuthenticatorSecretPattern, 10*time.Second)
	if err != nil {
		return "", b.classifyPageError(err)
	}
	match := localGmailAuthenticatorSecretPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		return "", b.classifyPageError(errors.New("new authenticator secret was not found"))
	}
	secret := strings.ToUpper(removeWhitespace(match[1]))
	if !validLocalGmailTOTPSecret(secret) {
		return "", b.classifyPageError(errors.New("new authenticator secret is invalid"))
	}
	if err := b.clickText([]string{"Next", "下一步"}, true); err != nil {
		return "", err
	}
	if err := b.submitTOTP(secret, []string{"Verify", "验证"}, `input[autocomplete="one-time-code"], input[type="tel"], input[type="text"]`, localGmailChangeTOTPAttempts); err != nil {
		return "", err
	}
	b.twoFactorRevoked = true
	if err := b.waitForText([]string{"Authenticator app has been changed", "身份验证器应用已更改", "Added just now", "刚刚添加"}, 15*time.Second); err != nil {
		// The TOTP form has already disappeared, so Google accepted the new
		// secret even if a localized success banner did not render.
		return secret, b.classifyPageError(err)
	}
	return secret, nil
}

func (b *localGmailBrowser) replaceAppPasswords(password, currentSecret string) (string, error) {
	if err := b.navigate(localGmailURLWithRAPT(localGmailAppPasswordsURL, b.rapt)); err != nil {
		return "", err
	}
	if err := b.completeReauthentication(password, currentSecret); err != nil {
		return "", err
	}
	if err := b.waitForAppPasswordPage(20 * time.Second); err != nil {
		return "", b.classifyPageError(err)
	}
	for revoked := 0; ; revoked++ {
		count, err := b.textControlCount([]string{"Revoke app password", "撤销应用专用密码", "撤销应用密码"})
		if err != nil {
			return "", err
		}
		if count == 0 {
			b.appPasswordRevoked = true
			break
		}
		if revoked > 100 {
			return "", b.classifyPageError(errors.New("too many app passwords"))
		}
		if err := b.clickText([]string{"Revoke app password", "撤销应用专用密码", "撤销应用密码"}, false); err != nil {
			return "", err
		}
		b.appPasswordRevoked = true
		if err := b.waitControlCountBelow([]string{"Revoke app password", "撤销应用专用密码", "撤销应用密码"}, count, 10*time.Second); err != nil {
			_, _ = b.clickAnyText([]string{"Revoke", "撤销", "Remove", "移除"})
			if err := b.waitControlCountBelow([]string{"Revoke app password", "撤销应用专用密码", "撤销应用密码"}, count, 5*time.Second); err != nil {
				return "", b.classifyPageError(err)
			}
		}
	}
	if err := b.fillLastTextInput("email"); err != nil {
		return "", b.classifyPageError(err)
	}
	if err := b.clickText([]string{"Create", "创建"}, true); err != nil {
		return "", err
	}
	body, err := b.waitBodyMatch(localGmailAppPasswordPattern, 15*time.Second)
	if err != nil {
		return "", b.classifyPageError(err)
	}
	appPassword, ok := extractLastLocalGmailAppPassword(body)
	if !ok {
		return "", b.classifyPageError(errors.New("new app password was not found"))
	}
	_, _ = b.clickAnyText([]string{"Done", "完成"})
	return appPassword, nil
}

func (b *localGmailBrowser) completeReauthentication(password, secret string) error {
	for step := 0; step < 80; step++ {
		location, _ := b.location()
		if visible, _ := b.selectorVisible(`input[type="email"], input[name="identifier"]`); visible || strings.Contains(location, "/signin/identifier") {
			if !validLocalGmailEmailAddress(b.email) {
				return &localGmailAccountError{
					err:       errors.New("google requested the account email again"),
					safeError: "Gmail account email is invalid.",
				}
			}
			if err := b.fill(`input[type="email"], input[name="identifier"]`, b.email); err != nil {
				return err
			}
			if err := b.clickText([]string{"Next", "下一步"}, true); err != nil {
				return err
			}
			if err := sleepLocalGmailBrowser(b.ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if visible, _ := b.selectorVisible(localGmailPasswordSelector); visible || strings.Contains(location, "/challenge/pwd") {
			if err := b.fill(localGmailPasswordSelector, password); err != nil {
				return err
			}
			if err := b.clickText([]string{"Next", "下一步"}, true); err != nil {
				return err
			}
			if err := sleepLocalGmailBrowser(b.ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if visible, _ := b.selectorVisible(`input[name="totpPin"], input[autocomplete="one-time-code"]`); visible || strings.Contains(location, "/challenge/totp") {
			if strings.TrimSpace(secret) == "" {
				return &localGmailAccountError{
					err:       errors.New("google requested an existing authenticator code"),
					safeError: "Gmail account requires its existing 2FA secret.",
				}
			}
			if err := b.submitTOTP(secret, []string{"Next", "下一步"}, `input[name="totpPin"], input[autocomplete="one-time-code"], input[type="tel"]`, localGmailLoginTOTPAttempts); err != nil {
				return err
			}
			continue
		}
		handled, err := b.completeBindingEmailChallenge(secret)
		if err != nil {
			return err
		}
		if handled {
			continue
		}
		if localGmailOnAccountHost(location) {
			if rapt := localGmailRAPT(location); rapt != "" {
				b.rapt = rapt
			}
			return nil
		}
		if err := b.classifyPageError(nil); err != nil {
			return err
		}
		if err := sleepLocalGmailBrowser(b.ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
	return b.classifyPageError(errors.New("google reauthentication did not complete"))
}

func (b *localGmailBrowser) completeBindingEmailChallenge(secret string) (bool, error) {
	location, _ := b.location()
	if strings.Contains(location, "/challenge/selection") {
		for attempt := 0; attempt < 20; attempt++ {
			if b.bindingEmail != "" {
				clicked, err := b.clickAnyText([]string{
					"Confirm your recovery email", "Enter your recovery email", "Recovery email",
					"确认您的辅助邮箱", "确认辅助邮箱", "辅助邮箱",
				})
				if err != nil || clicked {
					return clicked, err
				}
			}
			if strings.TrimSpace(secret) != "" {
				clicked, err := b.clickAnyText([]string{
					"Google Authenticator", "Get a verification code from the Google Authenticator app",
					"Google 身份验证器", "从 Google 身份验证器应用获取验证码",
				})
				if err != nil || clicked {
					return clicked, err
				}
			}
			if err := sleepLocalGmailBrowser(b.ctx, 250*time.Millisecond); err != nil {
				return true, err
			}
		}
		return true, &localGmailAccountError{
			err:       errors.New("google offered no supported login challenge"),
			safeError: "Gmail account has no supported binding email or authenticator challenge.",
		}
	}

	const selector = `input[name="knowledgePreregisteredEmailResponse"], input[type="email"]`
	visible, err := b.selectorVisible(selector)
	if err != nil {
		return false, err
	}
	if !visible && !strings.Contains(location, "/challenge/kpe") {
		return false, nil
	}
	if !validLocalGmailEmailAddress(b.bindingEmail) {
		return true, &localGmailAccountError{
			err:       errors.New("google requested the existing binding email"),
			safeError: "Gmail account requires its existing binding email.",
		}
	}
	if !visible {
		if err := b.waitSelector(selector, 10*time.Second); err != nil {
			return true, b.classifyPageError(err)
		}
	}
	if err := b.fill(selector, b.bindingEmail); err != nil {
		return true, err
	}
	if err := b.clickText([]string{"Next", "下一步"}, true); err != nil {
		return true, err
	}
	if changed, waitErr := b.waitSelectorHidden(selector, 12*time.Second); waitErr != nil || !changed {
		return true, &localGmailAccountError{
			err:       errors.New("google rejected the binding email"),
			safeError: "Gmail binding email was rejected.",
		}
	}
	return true, nil
}

func (b *localGmailBrowser) submitTOTP(secret string, buttonLabels []string, selector string, attempts int) error {
	if !validLocalGmailTOTPSecret(secret) {
		return &localGmailAccountError{err: errors.New("invalid TOTP secret"), safeError: "Gmail 2FA secret is invalid."}
	}
	if attempts <= 0 {
		attempts = localGmailLoginTOTPAttempts
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if remaining := localGmailTOTPPeriod - int(time.Now().Unix()%localGmailTOTPPeriod); remaining <= 5 {
			if err := sleepLocalGmailBrowser(b.ctx, time.Duration(remaining+1)*time.Second); err != nil {
				return err
			}
		}
		code, err := generateLocalGmailTOTP(secret, time.Now())
		if err != nil {
			return err
		}
		if err := b.waitSelector(selector, 10*time.Second); err != nil {
			return b.classifyPageError(err)
		}
		if err := b.fill(selector, code); err != nil {
			return err
		}
		if err := b.clickText(buttonLabels, true); err != nil {
			return err
		}
		changed, waitErr := b.waitSelectorHidden(selector, 12*time.Second)
		if waitErr == nil && changed {
			return nil
		}
		if pageErr := b.classifyPageError(nil); pageErr != nil {
			var accountErr *localGmailAccountError
			if !errors.As(pageErr, &accountErr) || !strings.Contains(strings.ToLower(accountErr.Error()), "code") {
				return pageErr
			}
		}
		remaining := localGmailTOTPPeriod - int(time.Now().Unix()%localGmailTOTPPeriod)
		if err := sleepLocalGmailBrowser(b.ctx, time.Duration(remaining+1)*time.Second); err != nil {
			return err
		}
	}
	return &localGmailAccountError{err: errors.New("TOTP code was rejected"), safeError: "Gmail 2FA code was rejected."}
}

func (b *localGmailBrowser) navigate(rawURL string) error {
	if err := chromedp.Run(b.ctx, chromedp.Navigate(rawURL)); err != nil {
		return localGmailBrowserTransportError(err, b.usesProxy)
	}
	return nil
}

func (b *localGmailBrowser) fill(selector, value string) error {
	selectorJSON, _ := json.Marshal(selector)
	valueJSON, _ := json.Marshal(value)
	expression := fmt.Sprintf(`(() => {
const nodes = Array.from(document.querySelectorAll(%s));
const el = nodes.find(e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length));
if (!el) return false;
el.focus();
const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')?.set;
if (setter) setter.call(el, %s); else el.value = %s;
el.dispatchEvent(new Event('input', {bubbles: true}));
el.dispatchEvent(new Event('change', {bubbles: true}));
return true;
})()`, selectorJSON, valueJSON, valueJSON)
	var ok bool
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(expression, &ok)); err != nil {
		return err
	}
	if !ok {
		return errors.New("gmail input was not found")
	}
	return sleepLocalGmailBrowser(b.ctx, 150*time.Millisecond)
}

func (b *localGmailBrowser) fillLastTextInput(value string) error {
	valueJSON, _ := json.Marshal(value)
	expression := fmt.Sprintf(`(() => {
const nodes = Array.from(document.querySelectorAll('input[type="text"], input:not([type])'))
  .filter(e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length));
const el = nodes[nodes.length - 1];
if (!el) return false;
el.focus();
const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')?.set;
if (setter) setter.call(el, %s); else el.value = %s;
el.dispatchEvent(new Event('input', {bubbles: true}));
el.dispatchEvent(new Event('change', {bubbles: true}));
return true;
})()`, valueJSON, valueJSON)
	var ok bool
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(expression, &ok)); err != nil {
		return err
	}
	if !ok {
		return errors.New("gmail app name input was not found")
	}
	return nil
}

func (b *localGmailBrowser) clickText(labels []string, exact bool) error {
	clicked, err := b.clickTextValue(labels, exact)
	if err != nil {
		return err
	}
	if !clicked {
		return errors.New("gmail page control was not found")
	}
	return sleepLocalGmailBrowser(b.ctx, 200*time.Millisecond)
}

func (b *localGmailBrowser) clickAnyText(labels []string) (bool, error) {
	clicked, err := b.clickTextValue(labels, false)
	if err != nil || !clicked {
		return clicked, err
	}
	return true, sleepLocalGmailBrowser(b.ctx, 200*time.Millisecond)
}

func (b *localGmailBrowser) clickAnyTextWithin(labels []string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		clicked, err := b.clickAnyText(labels)
		if err != nil || clicked || !time.Now().Before(deadline) {
			return clicked, err
		}
		if err := sleepLocalGmailBrowser(b.ctx, 250*time.Millisecond); err != nil {
			return false, err
		}
	}
}

func (b *localGmailBrowser) clickTextValue(labels []string, exact bool) (bool, error) {
	labelsJSON, _ := json.Marshal(labels)
	expression := fmt.Sprintf(`(() => {
const labels = %s.map(v => v.toLocaleLowerCase());
const visible = e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length);
const text = e => ((e.getAttribute('aria-label') || '') + ' ' + (e.innerText || e.textContent || '')).trim().replace(/\s+/g, ' ').toLocaleLowerCase();
const nodes = Array.from(document.querySelectorAll('button,[role="button"],a,[role="link"]')).filter(visible);
nodes.sort((a, b) => text(a).length - text(b).length);
const el = nodes.find(e => labels.some(label => %t ? text(e) === label : text(e).includes(label)));
if (!el) return false;
el.click();
return true;
})()`, labelsJSON, exact)
	var clicked bool
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(expression, &clicked)); err != nil {
		return false, err
	}
	return clicked, nil
}

func (b *localGmailBrowser) textControlCount(labels []string) (int, error) {
	labelsJSON, _ := json.Marshal(labels)
	expression := fmt.Sprintf(`(() => {
const labels = %s.map(v => v.toLocaleLowerCase());
const visible = e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length);
const text = e => ((e.getAttribute('aria-label') || '') + ' ' + (e.innerText || e.textContent || '')).trim().replace(/\s+/g, ' ').toLocaleLowerCase();
return Array.from(document.querySelectorAll('button,[role="button"]')).filter(e => visible(e) && labels.some(label => text(e).includes(label))).length;
})()`, labelsJSON)
	var count int
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(expression, &count)); err != nil {
		return 0, err
	}
	return count, nil
}

func (b *localGmailBrowser) waitControlCountBelow(labels []string, before int, timeout time.Duration) error {
	return b.wait(timeout, func() (bool, error) {
		count, err := b.textControlCount(labels)
		return count < before, err
	})
}

func (b *localGmailBrowser) waitForAppPasswordPage(timeout time.Duration) error {
	return b.wait(timeout, func() (bool, error) {
		count, err := b.textControlCount([]string{"Create", "创建"})
		if err != nil || count > 0 {
			return count > 0, err
		}
		return false, b.classifyPageError(nil)
	})
}

func (b *localGmailBrowser) waitSelector(selector string, timeout time.Duration) error {
	return b.wait(timeout, func() (bool, error) { return b.selectorVisible(selector) })
}

func (b *localGmailBrowser) waitSelectorHidden(selector string, timeout time.Duration) (bool, error) {
	err := b.wait(timeout, func() (bool, error) {
		visible, err := b.selectorVisible(selector)
		return !visible, err
	})
	return err == nil, err
}

func (b *localGmailBrowser) selectorVisible(selector string) (bool, error) {
	selectorJSON, _ := json.Marshal(selector)
	expression := fmt.Sprintf(`Array.from(document.querySelectorAll(%s)).some(e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length))`, selectorJSON)
	var visible bool
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(expression, &visible)); err != nil {
		return false, err
	}
	return visible, nil
}

func (b *localGmailBrowser) waitBodyMatch(pattern *regexp.Regexp, timeout time.Duration) (string, error) {
	var body string
	err := b.wait(timeout, func() (bool, error) {
		var err error
		body, err = b.bodyText()
		return pattern.MatchString(body), err
	})
	return body, err
}

func (b *localGmailBrowser) waitForText(labels []string, timeout time.Duration) error {
	return b.wait(timeout, func() (bool, error) {
		body, err := b.bodyText()
		if err != nil {
			return false, err
		}
		body = strings.ToLower(body)
		for _, label := range labels {
			if strings.Contains(body, strings.ToLower(label)) {
				return true, nil
			}
		}
		return false, nil
	})
}

func (b *localGmailBrowser) wait(timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ok, err := check()
		if ok {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastErr
			}
			return context.DeadlineExceeded
		}
		if err := sleepLocalGmailBrowser(b.ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
}

func (b *localGmailBrowser) bodyText() (string, error) {
	var body string
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ''`, &body)); err != nil {
		return "", err
	}
	return body, nil
}

func (b *localGmailBrowser) location() (string, error) {
	var location string
	if err := chromedp.Run(b.ctx, chromedp.Location(&location)); err != nil {
		return "", err
	}
	return location, nil
}

func (b *localGmailBrowser) classifyPageError(fallback error) error {
	location, _ := b.location()
	body, _ := b.bodyText()
	lower := strings.ToLower(location + "\n" + body)
	switch {
	case strings.Contains(lower, "wrong password"), strings.Contains(lower, "密码错误"), strings.Contains(lower, "incorrect password"):
		return &localGmailAccountError{err: errors.New("google rejected the password"), safeError: "Gmail account password is incorrect."}
	case strings.Contains(lower, "couldn’t find your google account"), strings.Contains(lower, "couldn't find your google account"), strings.Contains(lower, "找不到您的 google 账号"):
		return &localGmailAccountError{err: errors.New("google account was not found"), safeError: "Gmail account does not exist."}
	case strings.Contains(lower, "too many failed attempts"), strings.Contains(lower, "try again later"), strings.Contains(lower, "稍后再试"), strings.Contains(lower, "recaptcha"):
		return &localGmailAccountError{err: errors.New("google validation was rate limited"), safeError: "Gmail validation is temporarily rate limited.", temporary: true, proxyFailure: true}
	case strings.Contains(lower, "/challenge/kpe"), strings.Contains(lower, "knowledgepreregisteredemailresponse"):
		return &localGmailAccountError{err: errors.New("google requested the existing binding email"), safeError: "Gmail account requires its existing binding email."}
	case strings.Contains(lower, "/challenge/dp"), strings.Contains(lower, "/challenge/ipp"), strings.Contains(lower, "/challenge/pk"), strings.Contains(lower, "/challenge/phone"):
		return &localGmailAccountError{err: errors.New("google requires an unsupported verification challenge"), safeError: "Gmail account requires an unsupported phone, prompt, or passkey challenge."}
	case strings.Contains(lower, "account disabled"), strings.Contains(lower, "account has been disabled"), strings.Contains(lower, "账号已停用"):
		return &localGmailAccountError{err: errors.New("google account is disabled"), safeError: "Gmail account is disabled."}
	case strings.Contains(lower, "app passwords are not available"), strings.Contains(lower, "应用专用密码不可用"):
		return &localGmailAccountError{err: errors.New("google app passwords are unavailable"), safeError: "Gmail App Passwords are unavailable for this account."}
	}
	if fallback == nil {
		return nil
	}
	return localGmailBrowserTransportError(fallback, b.usesProxy)
}

func localGmailBrowserExecutable() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("GMAIL_BROWSER_BINARY")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return configured, nil
		}
		return "", fmt.Errorf("configured Gmail browser is not executable")
	}
	candidates := []string{
		"/opt/cloakbrowser/chrome",
		"/app/cloakbrowser-linux-x64/chrome",
		filepath.Join("cloakbrowser-linux-x64", "chrome"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	for _, name := range []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no Chrome-compatible browser was found")
}

func localGmailBrowserProxy(raw string) (server, username, password string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", "", "", errors.New("invalid proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks4", "socks5":
	case "socks4a":
		parsed.Scheme = "socks4"
	case "socks5h":
		parsed.Scheme = "socks5"
	default:
		return "", "", "", errors.New("unsupported proxy scheme")
	}
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
		parsed.User = nil
	}
	return parsed.String(), username, password, nil
}

func localGmailBrowserTransportError(err error, usesProxy bool) error {
	if err == nil {
		return nil
	}
	return &localGmailAccountError{
		err: err, safeError: "Gmail account validation is temporarily unavailable.",
		temporary: true, proxyFailure: usesProxy && isLocalGmailBrowserProxyFailure(err),
	}
}

func isLocalGmailBrowserProxyFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"err_proxy", "proxy connection", "proxyconnect", "tunnel connection", "socks", "407 proxy",
		"connection reset", "connection refused", "i/o timeout", "net::err_timed_out",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func localGmailRAPT(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("rapt"))
}

func localGmailOnAccountHost(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Hostname(), "myaccount.google.com") {
		return false
	}
	path := strings.ToLower(parsed.Path)
	return !strings.Contains(path, "/challenge/") && !strings.Contains(path, "/signin/")
}

func localGmailURLWithRAPT(rawURL, rapt string) string {
	if strings.TrimSpace(rapt) == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsed.Query()
	query.Set("rapt", rapt)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func generateLocalGmailTOTP(secret string, now time.Time) (string, error) {
	secret = strings.ToUpper(strings.TrimRight(removeWhitespace(secret), "="))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(key) == 0 {
		return "", errors.New("invalid Gmail TOTP secret")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/localGmailTOTPPeriod))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000), nil
}

func validLocalGmailTOTPSecret(secret string) bool {
	secret = strings.ToUpper(strings.TrimRight(removeWhitespace(secret), "="))
	if secret == "" {
		return false
	}
	_, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	return err == nil
}

func validLocalGmailAppPassword(value string) bool {
	value = removeWhitespace(value)
	if len(value) != 16 {
		return false
	}
	for _, r := range value {
		if r < 'A' || r > 'Z' && r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func sleepLocalGmailBrowser(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
