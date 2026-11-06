package msacl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type RecoveryCancellationResult struct {
	Cancelled    bool            `json:"cancelled"`
	Stage        string          `json:"stage"`
	RecoveryCode string          `json:"-"`
	Cookies      []SessionCookie `json:"-"`
}

type recoveryCancellationPage struct {
	Email   string `json:"email"`
	Canary  string `json:"apiCanary"`
	EIPT    string `json:"eipt"`
	UAID    string `json:"uaid"`
	UIFLVR  int    `json:"uiflvr"`
	SCID    int    `json:"scid"`
	HPGID   int    `json:"hpgid"`
	Account struct {
		Cancel *struct {
			StartState  string `json:"startState"`
			SessionID   string `json:"sessionId"`
			ViewContext struct {
				Data struct {
					RawProofList string `json:"rawProofList"`
					Replacement  struct {
						Name string `json:"name"`
					} `json:"replacementProof"`
				} `json:"data"`
			} `json:"viewContext"`
		} `json:"recoveryCancel"`
		URLs struct {
			RecoveryCancel struct {
				ReturnURL string `json:"returnUrl"`
			} `json:"recoveryCancel"`
		} `json:"urls"`
	} `json:"WLXAccount"`
}

type cancellationEmailProof struct {
	Name string `json:"name"`
	Type string `json:"type"`
	EPID string `json:"epid"`
}

func parseRecoveryCancellation(page, rawURL, email, oldAddress, expectedReplacement string) (recoveryCancellationPage, cancellationEmailProof, error) {
	var cfg recoveryCancellationPage
	var selected cancellationEmailProof
	u, err := url.Parse(rawURL)
	location := managedProofConfigPattern.FindStringIndex(page)
	if err != nil || u.Scheme != "https" || u.Hostname() != "account.live.com" || u.Path != "/ar/cancel" || location == nil || json.NewDecoder(strings.NewReader(page[location[1]:])).Decode(&cfg) != nil || cfg.Account.Cancel == nil {
		return cfg, selected, errors.New("microsoft pending security-info cancellation page was not returned")
	}
	if !strings.EqualFold(cfg.Email, email) || expectedReplacement == "" || !strings.EqualFold(cfg.Account.Cancel.ViewContext.Data.Replacement.Name, expectedReplacement) {
		return cfg, selected, errors.New("pending security-info request differs from the approved account or destination")
	}
	if normalizeRecoveryMailbox(oldAddress) == "" || !UsesActiveAuxiliaryDomain(oldAddress) || cfg.Account.Cancel.StartState != "landing" || cfg.Canary == "" || cfg.EIPT == "" || cfg.Account.Cancel.SessionID == "" {
		return cfg, selected, errors.New("pending security-info cancellation authorization is incomplete")
	}
	var proofs []cancellationEmailProof
	if json.Unmarshal([]byte(cfg.Account.Cancel.ViewContext.Data.RawProofList), &proofs) != nil {
		return cfg, selected, errors.New("current recovery proof list is invalid")
	}
	count := 0
	for _, proof := range proofs {
		if proof.Type == "Email" && proof.EPID != "" && mailboxMatchesMasked(proof.Name, oldAddress) {
			selected = proof
			count++
		}
	}
	if count != 1 {
		return cfg, selected, errors.New("the current readable recovery mailbox is not uniquely confirmed")
	}
	return cfg, selected, nil
}

// CancelPendingSecurityInfo cancels only the explicitly approved pending destination.
func CancelPendingSecurityInfo(ctx context.Context, email, proxy, oldAddress, expectedReplacement string, cookies ...SessionCookie) (result RecoveryCancellationResult, resultErr error) {
	result.Stage = "inspect"
	if strings.Contains(oldAddress, "*") {
		oldAddress = ResolveBindingAddress(ctx, oldAddress, email, proxy, "")
	}
	session, err := newBrowserSession(ctx, proxy)
	if err != nil {
		return result, errors.New("cancellation session could not be created")
	}
	defer func() {
		result.Cookies, _ = session.SnapshotCookies("https://login.live.com/", "https://account.live.com/", proofsManageURL)
	}()
	if err := session.RestoreCookies(cookies); err != nil {
		return result, err
	}
	proofs, probeErr := loadManagedProofs(session)
	if probeErr == nil {
		if !strings.EqualFold(proofs.Email, email) {
			return result, errors.New("microsoft account identity differs")
		}
		return result, nil
	}
	var pending *unauthenticatedProofPage
	if !errors.As(probeErr, &pending) {
		return result, probeErr
	}
	parsedURL, _ := url.Parse(pending.url)
	if parsedURL.Hostname() != "account.live.com" || parsedURL.Path != "/ar/cancel" {
		_, _, loginErr := loginForExplicitAliasOTC(session, email, proxy, oldAddress)
		proofs, probeErr = loadManagedProofs(session)
		if probeErr == nil {
			if !strings.EqualFold(proofs.Email, email) {
				return result, errors.New("microsoft account identity differs")
			}
			return result, nil
		}
		if !errors.As(probeErr, &pending) {
			return result, errors.Join(loginErr, probeErr)
		}
	}
	return cancelRecoveryWithSession(session, email, proxy, oldAddress, expectedReplacement, pending)
}

func cancelRecoveryWithSession(session *Session, email, proxy, oldAddress, expectedReplacement string, pending *unauthenticatedProofPage) (result RecoveryCancellationResult, resultErr error) {
	result.Stage = "verify_current_proof"
	cfg, proof, err := parseRecoveryCancellation(pending.page, pending.url, email, oldAddress, expectedReplacement)
	if err != nil {
		return result, err
	}
	ctx := session.context()
	lease, err := claimCodeMailLease(ctx, proof.Name, oldAddress)
	if err != nil {
		return result, err
	}
	defer lease.releaseIfUnsent(ctx)
	seen, err := snapshotMailboxKeys(ctx, oldAddress, proxy)
	if err != nil {
		return result, errors.New("current recovery mailbox baseline could not be read")
	}
	if err := lease.markSent(ctx); err != nil {
		return result, err
	}
	_, err = recoveryCancellationAPI(session, &cfg, pending.url, "Proofs/SendOtt", map[string]any{
		"token": "", "purpose": "VetoEA", "epid": proof.EPID, "autoVerification": false, "autoVerificationFailed": false,
		"confirmProof": oldAddress, "HFId": "", "HId": "", "HSId": "", "HSol": "", "HType": "", "HPId": "",
	})
	if err != nil {
		return result, err
	}
	code, err := mailWaitCode(ctx, oldAddress, proxy, min(300, max(90, mailPollTimeout)), seen)
	if err != nil {
		return result, err
	}
	verified, err := recoveryCancellationAPI(session, &cfg, pending.url, "Proofs/VerifyCode", map[string]any{
		"code": code, "action": "IptVerify", "purpose": "EAVeto", "epid": proof.EPID, "confirmProof": oldAddress,
	})
	if err != nil {
		return result, err
	}
	token := asString(verified["token"])
	if token == "" {
		return result, errors.New("current recovery mailbox verification returned no authorization")
	}
	result.Stage = "cancel_pending_recovery"
	// Plain cancellation preserves the password; the separate compromise-report flow is not invoked.
	cancelled, err := recoveryCancellationAPI(session, &cfg, pending.url, "Recovery/VetoEARequest", map[string]any{
		"token": token, "sessionId": cfg.Account.Cancel.SessionID, "repComp": false, "usedTD": false,
	})
	if err != nil {
		return result, err
	}
	result.Cancelled, result.RecoveryCode = true, asString(cancelled["recoveryCode"])
	releaseCompletedCodeMailLease(ctx, lease)
	if next := cfg.Account.URLs.RecoveryCancel.ReturnURL; next != "" {
		if !isMicrosoftAccountRelayURL(next) {
			return result, errors.New("cancellation return URL is invalid")
		}
		if _, err := session.Get(next, requestOptions{Headers: navHeaders(session, nil), AllowRedirects: true, HasAllowRedirects: true}); err != nil {
			return result, errors.New("pending request was cancelled but the account return request failed")
		}
	}
	result.Stage = "pending_recovery_cancelled"
	return result, nil
}

var cancellationErrorCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func recoveryCancellationAPI(session *Session, cfg *recoveryCancellationPage, referer, path string, body map[string]any) (map[string]any, error) {
	body["uiflvr"], body["uaid"], body["scid"], body["hpgid"] = cfg.UIFLVR, cfg.UAID, cfg.SCID, cfg.HPGID
	resp, err := session.Post("https://account.live.com/API/"+path, requestOptions{JSON: body, Headers: corsHeaders(session, map[string]string{
		"Content-Type": "application/json; charset=utf-8", "Origin": "https://account.live.com", "Referer": referer,
		"canary": cfg.Canary, "eipt": cfg.EIPT, "x-ms-apiVersion": "2", "x-ms-apiTransport": "xhr",
		"x-ms-correlation-id": cfg.UAID, "client-request-id": cfg.UAID, "uaid": cfg.UAID,
		"uiflvr": strconv.Itoa(cfg.UIFLVR), "scid": strconv.Itoa(cfg.SCID), "hpgid": strconv.Itoa(cfg.HPGID),
	})})
	if err != nil {
		return nil, fmt.Errorf("microsoft %s request failed", path)
	}
	var response map[string]any
	if resp.StatusCode != http.StatusOK || resp.JSON(&response) != nil || response["error"] != nil {
		code := asString(asMap(response["error"])["code"])
		if !cancellationErrorCode.MatchString(code) {
			code = "unknown"
		}
		return nil, fmt.Errorf("microsoft %s rejected cancellation verification (HTTP %d, code %s)", path, resp.StatusCode, code)
	}
	if canary := asString(response["apiCanary"]); canary != "" {
		cfg.Canary = canary
	}
	return response, nil
}
