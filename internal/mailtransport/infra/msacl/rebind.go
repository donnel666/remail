package msacl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const proofsManageURL = "https://account.live.com/proofs/manage/additional"
const recoveryProofConfirmationReads = 14

type existingRecoveryProofOnlyKey struct{}
type recoveryCodeWaitSecondsKey struct{}
type retainOldRecoveryProofKey struct{}
type recoveryProofProgressKey struct{}

type recoveryProofProgress struct {
	target    string
	attempted bool
}

func beforeRecoveryMailboxAdd(ctx context.Context, address string) error {
	if existingOnly, _ := ctx.Value(existingRecoveryProofOnlyKey{}).(bool); existingOnly {
		return ErrRecoveryProofNotStarted
	}
	if progress, _ := ctx.Value(recoveryProofProgressKey{}).(*recoveryProofProgress); progress != nil {
		if !strings.EqualFold(address, progress.target) {
			return errors.New("recovery enrollment differs from the requested target")
		}
		progress.attempted = true
	}
	return nil
}

var ErrRecoveryProofNotStarted = errors.New("replacement recovery proof has not been started")

func WithExistingRecoveryProofOnly(ctx context.Context) context.Context {
	return context.WithValue(contextOrBackground(ctx), existingRecoveryProofOnlyKey{}, true)
}

func WithRetainedRecoveryProof(ctx context.Context) context.Context {
	return context.WithValue(contextOrBackground(ctx), retainOldRecoveryProofKey{}, true)
}

type RebindResult struct {
	Operation       string          `json:"operation"`
	BindingAddress  string          `json:"binding_address"`
	Verified        bool            `json:"verified"`
	OldProofRemoved bool            `json:"old_proof_removed"`
	Stage           string          `json:"stage"`
	Cookies         []SessionCookie `json:"-"`
}

type RecoveryMailboxState struct {
	Address  string `json:"address"`
	Verified bool   `json:"verified"`
}

// InspectRecoveryMailboxes reads an existing authenticated session without sending codes or changing proofs.
func InspectRecoveryMailboxes(ctx context.Context, email, proxy string, cookies []SessionCookie) ([]RecoveryMailboxState, error) {
	if len(cookies) == 0 {
		return nil, errors.New("no saved Microsoft session")
	}
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return nil, err
	}
	if err := session.RestoreCookies(cookies); err != nil {
		return nil, err
	}
	proofs, err := loadManagedProofs(session)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(proofs.Email, email) {
		return nil, errors.New("microsoft security page identity differs")
	}
	result := make([]RecoveryMailboxState, 0, len(proofs.Account.Manage.Emails))
	for _, proof := range proofs.Account.Manage.Emails {
		result = append(result, RecoveryMailboxState{Address: proof.Address, Verified: proof.Verified == 1})
	}
	return result, nil
}

type managedEmailProof struct {
	Address  string `json:"displayProofName"`
	ID       string `json:"proofId"`
	Verified int    `json:"isVerified"`
	Type     string `json:"proofType"`
}

type managedProofPage struct {
	Email     string `json:"email"`
	Canary    string `json:"apiCanary"`
	UIFLVR    int    `json:"uiflvr"`
	SCID      int    `json:"scid"`
	HPGID     int    `json:"hpgid"`
	Telemetry struct {
		UAID    string `json:"uaid"`
		Context string `json:"tcxt"`
	} `json:"clientTelemetry"`
	Account struct {
		Manage *struct {
			Emails []managedEmailProof `json:"emailProofs"`
		} `json:"manageProofs"`
	} `json:"WLXAccount"`
}

var managedProofConfigPattern = regexp.MustCompile(`\bvar\s+t0\s*=\s*`)
var errProofSessionUnauthenticated = errors.New("microsoft security proof session is not authenticated")

type unauthenticatedProofPage struct{ page, url string }

func (e *unauthenticatedProofPage) Error() string {
	u, err := url.Parse(e.url)
	location := managedProofConfigPattern.FindStringIndex(e.page)
	if err == nil && u.Scheme == "https" && u.Hostname() == "account.live.com" && u.Path == "/ar/cancel" && location != nil {
		var pending recoveryCancellationPage
		if json.NewDecoder(strings.NewReader(e.page[location[1]:])).Decode(&pending) == nil && pending.Account.Cancel != nil {
			return fmt.Sprintf("Microsoft security-info replacement is pending (%s)", pending.Account.Cancel.ViewContext.Data.Replacement.Name)
		}
	}
	return errProofSessionUnauthenticated.Error()
}
func (e *unauthenticatedProofPage) Unwrap() error { return errProofSessionUnauthenticated }

func parseManagedProofPage(page string) (managedProofPage, error) {
	var data managedProofPage
	location := managedProofConfigPattern.FindStringIndex(page)
	if location == nil || json.NewDecoder(strings.NewReader(page[location[1]:])).Decode(&data) != nil || data.Account.Manage == nil {
		return data, errors.New("microsoft security proof list was not returned")
	}
	for _, proof := range data.Account.Manage.Emails {
		if normalizeRecoveryMailbox(proof.Address) == "" || proof.ID == "" || proof.Type != "Email" {
			return data, errors.New("microsoft security proof list is incomplete")
		}
	}
	return data, nil
}

func (p managedProofPage) verified(address string) bool {
	for _, proof := range p.Account.Manage.Emails {
		if strings.EqualFold(proof.Address, address) && proof.Verified == 1 {
			return true
		}
	}
	return false
}

func loadManagedProofs(session *Session) (managedProofPage, error) {
	resp, err := session.Get(proofsManageURL, requestOptions{Headers: navHeaders(session, map[string]string{"Cache-Control": "no-cache", "Pragma": "no-cache"}), AllowRedirects: true, HasAllowRedirects: true})
	if err != nil {
		return managedProofPage{}, errors.New("microsoft security proof list request failed")
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			return managedProofPage{}, newAuthError("Microsoft security proof reads are rate limited", AuthStatusRateLimited)
		}
		return managedProofPage{}, fmt.Errorf("microsoft security proof list returned HTTP %d", resp.StatusCode)
	}
	page, currentURL := resp.Body, resp.URL
	for round := 0; round < 6; round++ {
		u, parseErr := url.Parse(currentURL)
		if parseErr != nil {
			return managedProofPage{}, errors.New("microsoft security proof session returned an invalid URL")
		}
		if strings.EqualFold(u.Hostname(), "account.live.com") && strings.Contains(strings.ToLower(u.Path), "/proofs/manage") {
			if result, parseErr := parseManagedProofPage(page); parseErr == nil {
				return result, nil
			}
		}
		if redirect := extractMicrosoftPrivacyRedirectURL(page, currentURL); redirect != "" {
			resp, err := session.Get(redirect, requestOptions{Headers: navHeaders(session, map[string]string{"Referer": currentURL}), AllowRedirects: true, HasAllowRedirects: true})
			if err != nil {
				return managedProofPage{}, errors.New("microsoft privacy notice relay request failed")
			}
			page, currentURL = resp.Body, resp.URL
			continue
		}
		fields := extractHiddenInputs(page)
		logInfo("Microsoft security proof relay: host=%s path=%s page_id=%s fields=%v", u.Hostname(), u.Path, extractPageID(page), sortedKeys(fields))
		action := resolveURL(currentURL, extractFormAction(page))
		if isAutoSubmitPage(page, action) || fields["t"] != "" || fields["code"] != "" || fields["ipt"] != "" {
			page, currentURL, err = handleAutoSubmit(session, page, currentURL, action)
			if err != nil {
				return managedProofPage{}, err
			}
			continue
		}
		if data, parseErr := extractPasswordRecoveryServerData(page); parseErr == nil {
			if proofErr := unsupportedPollingProof(data); proofErr != nil {
				return managedProofPage{}, proofErr
			}
		}
		return managedProofPage{}, &unauthenticatedProofPage{page: page, url: currentURL}
	}
	return managedProofPage{}, errors.New("microsoft security proof relay did not finish")
}

// RebindRecoveryMailbox verifies the replacement before removing the known old proof.
func RebindRecoveryMailbox(ctx context.Context, email, password, proxy, oldAddress, targetAddress string, cachedCookies ...SessionCookie) (result RebindResult, resultErr error) {
	ctx = context.WithValue(contextOrBackground(ctx), recoveryCodeWaitSecondsKey{}, min(300, max(90, mailPollTimeout)))
	progress := &recoveryProofProgress{target: targetAddress}
	ctx = context.WithValue(ctx, recoveryProofProgressKey{}, progress)
	defer func() {
		if progress.attempted {
			if result.Stage == "login" || result.Stage == "inspect" {
				result.Stage = "new_proof_pending"
			}
			if result.Operation == "already_bound" {
				result.Operation = "bind"
			}
		}
	}()
	result = RebindResult{BindingAddress: targetAddress, Stage: "login"}
	if targetAddress != normalizeRecoveryMailbox(targetAddress) || !UsesActiveAuxiliaryDomain(targetAddress) {
		return result, errors.New("replacement recovery mailbox is not configured for receipt")
	}
	if strings.Contains(oldAddress, "*") {
		oldAddress = ResolveBindingAddress(ctx, oldAddress, email, proxy, "")
	}
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return result, errors.New("microsoft session could not be created")
	}
	defer func() {
		result.Cookies, _ = session.SnapshotCookies("https://login.live.com/", "https://account.live.com/", "https://account.live.com/proofs/manage/additional")
	}()
	if len(cachedCookies) > 0 {
		if err := session.RestoreCookies(cachedCookies); err != nil {
			return result, errors.New("saved Microsoft session could not be restored")
		}
		_, probeErr := loadManagedProofs(session)
		var enrollment *unauthenticatedProofPage
		pageID := ""
		if errors.As(probeErr, &enrollment) {
			pageID = extractConfigString(enrollment.page, "sPageId")
		}
		if probeErr == nil || pageID == "Account_CredentialActionInterruptPage_Client" || pageID == "Account_TOUAccrualInterruptPage_Client" {
			return rebindRecoveryMailboxWithSession(session, email, proxy, oldAddress, targetAddress)
		}
		if !errors.Is(probeErr, errProofSessionUnauthenticated) {
			return result, probeErr
		}
		logInfo("Saved Microsoft session requires authentication")
		session, err = newBrowserSession(ctx, proxy)
		if err != nil {
			return result, err
		}
	}
	oldAddress = normalizeRecoveryMailbox(oldAddress)
	if !UsesActiveAuxiliaryDomain(oldAddress) {
		oldAddress = ""
	}
	if os.Getenv("MSACL_FORCE_PASSWORD_LOGIN") == "1" {
		_, _, err = loginForExplicitAliasPassword(session, email, password, proxy, oldAddress)
	} else {
		_, _, err = loginForExplicitAliasOTC(session, email, proxy, firstNonEmpty(oldAddress, targetAddress))
	}
	var auth *AuthError
	if errors.As(err, &auth) && auth.Status == AuthStatusAlreadyBound {
		resolved := ResolveBindingAddress(ctx, auth.BoundMailbox, email, proxy, targetAddress)
		if resolved != "" && UsesActiveAuxiliaryDomain(resolved) {
			oldAddress = resolved
			if os.Getenv("MSACL_FORCE_PASSWORD_LOGIN") == "1" {
				_, _, err = loginForExplicitAliasPassword(session, email, password, proxy, resolved)
			} else {
				_, _, err = loginForExplicitAliasOTC(session, email, proxy, resolved)
			}
		}
	}
	err = completeRebindLogin(session, email, password, proxy, targetAddress, err)
	if err != nil {
		return result, err
	}
	return rebindRecoveryMailboxWithSession(session, email, proxy, oldAddress, targetAddress)
}

func completeRebindLogin(session *Session, email, password, proxy, targetAddress string, loginErr error) error {
	var auth *AuthError
	passwordlessUnavailable := errors.Is(loginErr, errNoOTCEmailProof) || errors.As(loginErr, &auth) && auth.Status == AuthStatusUnknownMailbox
	if !passwordlessUnavailable || session.context().Err() != nil {
		return loginErr
	}
	if progress, _ := session.context().Value(recoveryProofProgressKey{}).(*recoveryProofProgress); progress != nil && progress.attempted {
		return loginErr
	}
	if existingOnly, _ := session.context().Value(existingRecoveryProofOnlyKey{}).(bool); existingOnly {
		return ErrRecoveryProofNotStarted
	}
	// Passwordless account discovery can fail when Microsoft requires email enrollment; password identity checks still apply.
	if auth != nil && auth.Status == AuthStatusUnknownMailbox {
		if _, _, _, err := loadExplicitAliasPasswordIdentity(session, email, password); err != nil {
			return err
		}
	}
	_, _, _, err := loginMicrosoftConsentManageWithBinding(session, email, password, proxy, targetAddress)
	return err
}

func rebindRecoveryMailboxWithSession(session *Session, email, proxy, oldAddress, targetAddress string) (RebindResult, error) {
	result := RebindResult{BindingAddress: targetAddress, Stage: "inspect"}
	var proofs managedProofPage
	var err error
	for round := 0; round < 3; round++ {
		proofs, err = loadManagedProofs(session)
		if err == nil {
			break
		}
		var enrollment *unauthenticatedProofPage
		if !errors.As(err, &enrollment) {
			return result, err
		}
		if _, _, handled, noticeErr := continueMicrosoftTermsNotice(session, enrollment.page, enrollment.url); noticeErr != nil {
			return result, noticeErr
		} else if handled {
			continue
		}
		if extractConfigString(enrollment.page, "sPageId") != "Account_CredentialActionInterruptPage_Client" {
			return result, err
		}
		if existingOnly, _ := session.context().Value(existingRecoveryProofOnlyKey{}).(bool); existingOnly {
			return result, ErrRecoveryProofNotStarted
		}
		u, parseErr := url.Parse(enrollment.url)
		if parseErr != nil || u.Scheme != "https" || u.Hostname() != "account.live.com" || u.Path != "/interrupt/credentialaction" || !strings.EqualFold(extractConfigString(enrollment.page, "sSigninName"), email) {
			return result, errors.New("microsoft credential enrollment identity or location differs")
		}
		if _, _, _, err := handleCredentialActionInterrupt(session, enrollment.page, enrollment.url, proxy, email, targetAddress); err != nil {
			return result, err
		}
	}
	if err != nil {
		return result, err
	}
	if !strings.EqualFold(proofs.Email, email) {
		return result, errors.New("microsoft security page belongs to a different account")
	}
	result.Operation = "bind"
	for _, proof := range proofs.Account.Manage.Emails {
		if !strings.EqualFold(proof.Address, targetAddress) {
			result.Operation = "rebind"
			break
		}
		result.Operation = "already_bound"
	}
	if existingOnly, _ := session.context().Value(existingRecoveryProofOnlyKey{}).(bool); existingOnly {
		found := false
		for _, proof := range proofs.Account.Manage.Emails {
			if strings.EqualFold(proof.Address, targetAddress) {
				found = true
			}
		}
		if !found {
			return result, ErrRecoveryProofNotStarted
		}
	}
	if oldAddress == "" {
		var existing []string
		for _, proof := range proofs.Account.Manage.Emails {
			if !strings.EqualFold(proof.Address, targetAddress) {
				existing = append(existing, proof.Address)
			}
		}
		if len(existing) == 1 && UsesActiveAuxiliaryDomain(existing[0]) {
			oldAddress = existing[0]
		}
	}
	// An unexpected proof must not be removed just because it differs from the target.
	for _, proof := range proofs.Account.Manage.Emails {
		if !strings.EqualFold(proof.Address, targetAddress) && !UsesActiveAuxiliaryDomain(proof.Address) {
			return result, alreadyBoundError(proof.Address, proof.Address)
		}
		if !strings.EqualFold(proof.Address, targetAddress) && !strings.EqualFold(proof.Address, oldAddress) {
			return result, errors.New("microsoft has an unexpected recovery email; no proof was removed")
		}
	}
	if proofs.verified(targetAddress) && len(proofs.Account.Manage.Emails) == 1 {
		result.Stage = "remote_confirmation"
		if _, err := confirmRecoveryProofState(session, email, targetAddress, true, proofs); err != nil {
			return result, err
		}
		result.Verified, result.OldProofRemoved, result.Stage = true, true, "remote_complete"
		return result, nil
	}
	targetExists := false
	for _, proof := range proofs.Account.Manage.Emails {
		if strings.EqualFold(proof.Address, targetAddress) {
			targetExists = true
		}
	}
	if !proofs.verified(targetAddress) && !targetExists {
		page, pageURL, loadErr := loadMicrosoftProofsAddPage(session)
		if loadErr != nil {
			return result, loadErr
		}
		result.Stage = "new_proof_pending"
		_, _, _, bindErr := bindAuxiliaryEmail(session, page, pageURL, proxy, email, targetAddress)
		proofs, err = confirmRecoveryProofState(session, email, targetAddress, false, managedProofPage{})
		if err != nil {
			return result, errors.Join(bindErr, err)
		}
	} else {
		result.Stage = "new_proof_pending"
		proofs, err = confirmRecoveryProofState(session, email, targetAddress, false, proofs)
		if err != nil {
			return result, err
		}
	}
	result.Stage = "new_proof_verified"
	if retain, _ := session.context().Value(retainOldRecoveryProofKey{}).(bool); retain {
		if oldAddress != "" && !proofs.verified(oldAddress) {
			return result, errors.New("the requested original recovery proof is no longer verified")
		}
		for _, proof := range proofs.Account.Manage.Emails {
			if !strings.EqualFold(proof.Address, oldAddress) && !strings.EqualFold(proof.Address, targetAddress) {
				return result, errors.New("unexpected recovery proof appeared while retaining the original")
			}
		}
		result.Verified, result.Stage = true, "remote_complete"
		result.OldProofRemoved = len(proofs.Account.Manage.Emails) == 1
		return result, nil
	}
	lastCode := ""
	for attempt := 0; attempt < 5; attempt++ {
		if !strings.EqualFold(proofs.Email, email) {
			return result, errors.New("microsoft security page identity changed")
		}
		if !proofs.verified(targetAddress) {
			return result, errors.New("replacement recovery proof state is not confirmed; no further deletion attempted")
		}
		var old *managedEmailProof
		for _, proof := range proofs.Account.Manage.Emails {
			if strings.EqualFold(proof.Address, targetAddress) {
				continue
			}
			if !strings.EqualFold(proof.Address, oldAddress) {
				return result, errors.New("unexpected recovery proof appeared")
			}
			proofCopy := proof
			old = &proofCopy
		}
		if old == nil {
			if _, err := confirmRecoveryProofState(session, email, targetAddress, true, proofs); err != nil {
				return result, err
			}
			result.Verified, result.OldProofRemoved, result.Stage = true, true, "remote_complete"
			return result, nil
		}
		if proofs.Canary == "" || proofs.Telemetry.UAID == "" {
			return result, errors.New("proof deletion authorization is missing")
		}
		result.Stage = "old_proof_delete_pending"
		resp, requestErr := session.Post("https://account.live.com/API/Proofs/DeleteProof", requestOptions{
			JSON:    map[string]any{"proofId": old.ID, "uiflvr": proofs.UIFLVR, "uaid": proofs.Telemetry.UAID, "scid": proofs.SCID, "hpgid": proofs.HPGID},
			Headers: corsHeaders(session, map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8", "canary": proofs.Canary, "Referer": proofsManageURL, "Origin": "https://account.live.com", "uaid": proofs.Telemetry.UAID, "tcxt": proofs.Telemetry.Context, "uiflvr": strconv.Itoa(proofs.UIFLVR), "scid": strconv.Itoa(proofs.SCID), "hpgid": strconv.Itoa(proofs.HPGID), "x-ms-apitransport": "xhr", "x-ms-apiversion": "2", "x-requested-with": "XMLHttpRequest"}),
		})
		if requestErr == nil && resp.StatusCode == http.StatusTooManyRequests {
			wait := time.Duration(30*(attempt+1)) * time.Second
			if seconds, parseErr := strconv.Atoi(resp.Header.Get("Retry-After")); parseErr == nil && seconds > 0 {
				wait = max(wait, time.Duration(seconds)*time.Second)
			}
			if wait > 5*time.Minute {
				return result, errors.New("microsoft requested a long proof deletion cooldown")
			}
			if err := session.sleep(wait); err != nil {
				return result, err
			}
		} else if requestErr == nil {
			var response map[string]any
			if resp.StatusCode != http.StatusOK || resp.JSON(&response) != nil || response["error"] != nil {
				lastCode = asString(asMap(response["error"])["code"])
				if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`).MatchString(lastCode) {
					lastCode = "unknown"
				}
				logWarning("DeleteProof response requires state reconciliation: http=%d error_code=%s", resp.StatusCode, lastCode)
			}
		} else {
			logWarning("DeleteProof transport response was uncertain; reconciling account state")
		}
		proofs, err = confirmRecoveryProofState(session, email, targetAddress, true, managedProofPage{})
		if err == nil {
			result.Verified, result.OldProofRemoved, result.Stage = true, true, "remote_complete"
			return result, nil
		}
		if proofs.Account.Manage == nil || !proofs.verified(targetAddress) {
			return result, err
		}
		if err := session.sleep(time.Duration(attempt+1) * 3 * time.Second); err != nil {
			return result, err
		}
	}
	return result, fmt.Errorf("old recovery proof remains after bounded retries (last error code %s)", lastCode)
}

// Microsoft may serve a stale proof list immediately after a successful write.
func confirmRecoveryProofState(session *Session, email, target string, onlyTarget bool, initial managedProofPage) (managedProofPage, error) {
	current := initial
	var lastErr error
	stable := 0
	for attempt := 0; attempt < recoveryProofConfirmationReads; attempt++ {
		if attempt > 0 {
			if err := session.sleep(time.Duration(min(1<<attempt, 10)) * time.Second); err != nil {
				return current, err
			}
		}
		if attempt > 0 || current.Account.Manage == nil {
			var err error
			current, err = loadManagedProofs(session)
			if err != nil {
				lastErr = err
				stable = 0
				var auth *AuthError
				if errors.Is(err, errProofSessionUnauthenticated) || errors.As(err, &auth) && auth.Status == AuthStatusRateLimited {
					return current, err
				}
				continue
			}
		}
		if !strings.EqualFold(current.Email, email) {
			return current, errors.New("microsoft security page identity changed")
		}
		lastErr = nil
		if current.verified(target) && (!onlyTarget || len(current.Account.Manage.Emails) == 1) {
			stable++
			if stable >= 2 {
				return current, nil
			}
		} else {
			stable = 0
		}
	}
	if lastErr != nil {
		return current, lastErr
	}
	return current, errors.New("microsoft recovery proof state did not stabilize before the confirmation limit")
}

func SafeRebindError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeLogMessage(err.Error())
}
