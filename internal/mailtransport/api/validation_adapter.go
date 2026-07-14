package api

import (
	"context"
	"errors"
	"log/slog"
	stdmail "net/mail"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

const maxMicrosoftProxyAttempts = 3

type microsoftBindingRecoveryAction uint8

const (
	microsoftBindingRecoveryNone microsoftBindingRecoveryAction = iota
	// A valid refresh-token flow remains authoritative. The recovered candidate
	// still requires password/OTP confirmation, but that confirmation must not
	// replace the refreshed OAuth credentials.
	microsoftBindingRecoveryObserve
	// No usable RT exists. Once the read-only probe resolves a candidate, retry
	// password login exactly once and use its credentials only after it confirms
	// the same complete address as verified.
	microsoftBindingRecoveryRetryPassword
)

func (action microsoftBindingRecoveryAction) needed() bool {
	return action != microsoftBindingRecoveryNone
}

// microsoftBindingRecoveryCandidate is evidence discovered by the read-only
// password-recovery proof picker. It is deliberately not a Core recovered fact:
// the address must pass a normal password/identity OTP confirmation first.
type microsoftBindingRecoveryCandidate struct {
	Address                  string
	ExpectedBindingID        uint
	ExpectedBindingAddress   string
	ExpectedBindingUpdatedAt time.Time
}

func (candidate *microsoftBindingRecoveryCandidate) confirmedFact() *coreapp.MicrosoftRecoveredBinding {
	if candidate == nil {
		return nil
	}
	return &coreapp.MicrosoftRecoveredBinding{
		Address:                  candidate.Address,
		ExpectedBindingID:        candidate.ExpectedBindingID,
		ExpectedBindingAddress:   candidate.ExpectedBindingAddress,
		ExpectedBindingUpdatedAt: candidate.ExpectedBindingUpdatedAt,
	}
}

type microsoftOAuthProtocol interface {
	RefreshToken(ctx context.Context, req mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error)
	AcquireToken(ctx context.Context, req mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error)
}

type microsoftProxyProvider interface {
	Acquire(ctx context.Context, req proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(ctx context.Context, proxyID uint) error
	ReportFailure(ctx context.Context, proxyID uint, safeError string) error
}

type microsoftMailFetcher interface {
	FetchAll(ctx context.Context, req mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error)
}

type microsoftValidationBindingStore interface {
	FindByResourceIDs(ctx context.Context, resourceIDs []uint) (map[uint]maildomain.MicrosoftBindingMailbox, error)
}

type ResourceValidationAdapter struct {
	proxies                    microsoftProxyProvider
	microsoft                  microsoftOAuthProtocol
	fetcher                    microsoftMailFetcher
	dns                        *mailinfra.DomainDNSValidator
	bindings                   microsoftValidationBindingStore
	history                    mailapp.HistoricalProjectMatcher
	probePasswordRecovery      func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error)
	evaluateBindingEligibility func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility
}

// RefreshMicrosoftToken is the MailTransport ACL used by the durable
// administrator token task. Access tokens and raw upstream bodies are consumed
// inside this adapter and are never returned to the application service.
func (a *ResourceValidationAdapter) RefreshMicrosoftToken(
	ctx context.Context,
	request mailapp.MicrosoftTokenRefreshProtocolRequest,
) (mailapp.MicrosoftTokenRefreshProtocolResult, error) {
	if a == nil || a.microsoft == nil {
		return unavailableMicrosoftTokenRefreshResult(), nil
	}

	var last mailapp.MicrosoftTokenRefreshProtocolResult
	for attempt := 0; attempt <= maxMicrosoftProxyAttempts; attempt++ {
		proxyConfig, err := a.acquireMicrosoftTokenProxy(ctx, request, attempt)
		if err != nil {
			return unavailableMicrosoftTokenRefreshResult(), nil
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}

		raw, refreshErr := a.microsoft.RefreshToken(ctx, mailinfra.MicrosoftOAuthRequest{
			EmailAddress: request.EmailAddress,
			ClientID:     request.ClientID,
			RefreshToken: request.RefreshToken,
			ProxyURL:     proxyURL,
		})
		if refreshErr != nil {
			raw = mailinfra.MicrosoftOAuthResult{
				Category:     "request",
				SafeMessage:  "Microsoft mail service is temporarily unavailable.",
				ProxyFailure: proxyID != 0,
			}
		}
		last = safeMicrosoftTokenRefreshProtocolResult(raw)
		if raw.Valid {
			_ = a.reportProxySuccess(ctx, proxyID)
			return last, nil
		}
		if raw.ProxyFailure && proxyID != 0 {
			_ = a.reportProxyFailure(ctx, proxyID, last.SafeMessage)
			continue
		}
		if raw.ProxyFailure && proxyID == 0 && attempt < maxMicrosoftProxyAttempts {
			continue
		}
		if proxyID != 0 {
			_ = a.reportProxySuccess(ctx, proxyID)
		}
		return last, nil
	}
	if strings.TrimSpace(last.SafeMessage) == "" {
		last = unavailableMicrosoftTokenRefreshResult()
	}
	return last, nil
}

func (a *ResourceValidationAdapter) acquireMicrosoftTokenProxy(
	ctx context.Context,
	request mailapp.MicrosoftTokenRefreshProtocolRequest,
	attempt int,
) (*proxyapp.ProxyConfig, error) {
	if a == nil || a.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return a.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(request.EmailAddress)),
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeAuth,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           strings.TrimSpace(request.RequestID),
	})
}

func safeMicrosoftTokenRefreshProtocolResult(raw mailinfra.MicrosoftOAuthResult) mailapp.MicrosoftTokenRefreshProtocolResult {
	if raw.Valid {
		return mailapp.MicrosoftTokenRefreshProtocolResult{
			Valid:        true,
			ClientID:     strings.TrimSpace(raw.ClientID),
			RefreshToken: strings.TrimSpace(raw.RefreshToken),
			SafeMessage:  "Microsoft refresh-token diagnostic succeeded.",
		}
	}
	category := strings.ToLower(strings.TrimSpace(raw.Category))
	result := mailapp.MicrosoftTokenRefreshProtocolResult{Category: category}
	switch category {
	case "oauth_invalid_grant":
		result.SafeMessage = "Microsoft refresh token is invalid or expired."
	case "oauth_client":
		result.SafeMessage = "Microsoft OAuth client is invalid or not allowed."
	case "oauth_permission":
		result.SafeMessage = "Microsoft OAuth permission is not available."
	case "mfa":
		result.SafeMessage = "Microsoft account requires authenticator verification."
	case "passkey":
		result.SafeMessage = "Microsoft account requires passkey verification."
	case "phone":
		result.SafeMessage = "Microsoft account requires phone verification."
	case "password":
		result.SafeMessage = "Microsoft account password is incorrect."
	case "unknown_mailbox":
		result.SafeMessage = "Microsoft account does not exist or recovery mailbox is not supported."
	case "locked":
		result.SafeMessage = "Microsoft account is locked."
	case "rate_limited":
		result.SafeMessage = "Microsoft mail service is rate limited."
	case "auth_timeout", "request":
		result.SafeMessage = "Microsoft mail service is temporarily unavailable."
	default:
		result.Category = "request"
		result.SafeMessage = "Microsoft mail service is temporarily unavailable."
	}
	return result
}

func unavailableMicrosoftTokenRefreshResult() mailapp.MicrosoftTokenRefreshProtocolResult {
	return mailapp.MicrosoftTokenRefreshProtocolResult{
		Category:    "request",
		SafeMessage: "Microsoft mail service is temporarily unavailable.",
	}
}

func (a *ResourceValidationAdapter) SetHistoricalProjectMatcher(matcher mailapp.HistoricalProjectMatcher) {
	if a == nil {
		return
	}
	a.history = matcher
}

func NewResourceValidationAdapter(proxies *proxyapp.ProxyUseCase, bindings *mailinfra.MicrosoftBindingRepo) *ResourceValidationAdapter {
	return &ResourceValidationAdapter{
		proxies:                    proxies,
		microsoft:                  mailinfra.NewMicrosoftOAuthClient(),
		fetcher:                    mailinfra.NewMicrosoftMailFetchClient(),
		dns:                        mailinfra.NewDomainDNSValidator(),
		bindings:                   bindings,
		probePasswordRecovery:      msacl.ProbePasswordRecovery,
		evaluateBindingEligibility: msacl.EvaluateActiveBindingRecoveryEligibility,
	}
}

func (a *ResourceValidationAdapter) ValidateMicrosoft(ctx context.Context, req coreapp.MicrosoftValidationRequest) (coreapp.MicrosoftValidationResult, error) {
	if a == nil || a.microsoft == nil {
		return coreapp.MicrosoftValidationResult{
			Valid:       false,
			Category:    "request",
			SafeMessage: "Microsoft mail service is temporarily unavailable.",
		}, nil
	}

	bindingSnapshot, err := a.microsoftBindingSnapshot(ctx, req.ResourceID)
	if err != nil {
		return coreapp.MicrosoftValidationResult{}, err
	}
	preferredBindingAddress := bindingSnapshotPreferredAddress(bindingSnapshot, req.EmailAddress)
	effectiveBindingAddress := preferredBindingAddress
	bindingAlreadyVerified := bindingSnapshotHasCompleteVerifiedAddress(bindingSnapshot, req.EmailAddress)
	if bindingSnapshot != nil && bindingSnapshot.Status != maildomain.MicrosoftBindingExpired && preferredBindingAddress == "" {
		// Preserve a pending/failed operator or import input for the normal
		// validation fallback. It is deliberately not trusted as a preferred
		// proof-picker match unless it is already verified.
		effectiveBindingAddress = strings.TrimSpace(bindingSnapshot.BindingAddress)
	}
	effectiveBindingAddress, err = a.prepareBindingAddress(req, effectiveBindingAddress)
	if err != nil {
		return coreapp.MicrosoftValidationResult{}, err
	}

	var last coreapp.MicrosoftValidationResult
	var recoveredBinding *coreapp.MicrosoftRecoveredBinding
	bindingConfirmedThisRun := bindingAlreadyVerified
	var confirmedBindingObservation *mailinfra.MicrosoftOAuthResult
	var authoritativeClientID string
	var authoritativeRefreshToken string
	credentialsKnownAuthoritative := false
	recoveryAttempted := false
	for attempt := 0; attempt <= maxMicrosoftProxyAttempts; attempt++ {
		proxyConfig, err := a.acquireProxy(ctx, req, attempt)
		if err != nil {
			return coreapp.MicrosoftValidationResult{}, err
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		rawResult, recoveryAction, credentialsAuthoritative, err := a.runMicrosoftValidation(ctx, req, proxyURL, effectiveBindingAddress, bindingConfirmedThisRun || recoveredBinding != nil)
		if err != nil {
			if cancelErr := microsoftRecoveryContextError(ctx, err); cancelErr != nil {
				return coreapp.MicrosoftValidationResult{}, cancelErr
			}
			rawResult.Valid = false
			rawResult.Category = "request"
			rawResult.SafeMessage = "Microsoft mail service is temporarily unavailable."
			rawResult.ProxyFailure = proxyID != 0
		}
		if recoveryAction == microsoftBindingRecoveryNone &&
			recoveredBinding == nil &&
			confirmedBindingObservation != nil &&
			strings.TrimSpace(rawResult.BindingAddress) == "" {
			mergeSupplementaryBindingResult(&rawResult, *confirmedBindingObservation)
		}

		// The proof probe uses a different binding proxy and may itself be
		// cancelled. For observe-only RT flows the outer auth/fetch proxy has
		// already completed all of its work, so settle its health before probing.
		proxyReported := false
		if recoveryAction == microsoftBindingRecoveryObserve && proxyID != 0 {
			if rawResult.ProxyFailure {
				_ = a.reportProxyFailure(ctx, proxyID, rawResult.SafeMessage)
			} else {
				_ = a.reportProxySuccess(ctx, proxyID)
			}
			proxyReported = true
		}

		recoveryNeeded := recoveryAction.needed()
		var recoveryCandidate *microsoftBindingRecoveryCandidate
		recoveryProbeUnavailable := false
		if recoveryNeeded && !bindingAlreadyVerified && !recoveryAttempted {
			recoveryAttempted = true
			recoveryCandidate, recoveryProbeUnavailable, err = a.recoverBindingForValidation(ctx, req, bindingSnapshot)
			if err != nil {
				return coreapp.MicrosoftValidationResult{}, err
			}
			if recoveryCandidate != nil {
				effectiveBindingAddress = recoveryCandidate.Address
			}
		}
		if recoveryProbeUnavailable {
			rawResult = unavailableMicrosoftBindingRecoveryResult(rawResult, effectiveBindingAddress)
		}
		if recoveryCandidate != nil {
			var confirmed bool
			rawResult, credentialsAuthoritative, confirmed, err = a.confirmMicrosoftBindingRecoveryCandidate(
				ctx,
				req,
				proxyURL,
				rawResult,
				credentialsAuthoritative,
				recoveryAction,
				recoveryCandidate,
			)
			if err != nil {
				if cancelErr := microsoftRecoveryContextError(ctx, err); cancelErr != nil {
					return coreapp.MicrosoftValidationResult{}, cancelErr
				}
				rawResult.Valid = false
				rawResult.Category = "request"
				rawResult.SafeMessage = "Microsoft mail service is temporarily unavailable."
				// The retry login owns an independent binding proxy. Only FetchAll
				// still uses the outer proxy and may mark it failed.
				rawResult.ProxyFailure = false
			}
			if confirmed {
				recoveredBinding = recoveryCandidate.confirmedFact()
			}
		}
		if rawResult.Valid && recoveryNeeded && recoveredBinding == nil && strings.TrimSpace(rawResult.BoundDisplay) == "" {
			rawResult.Valid = false
			rawResult.Category = "request"
			rawResult.SafeMessage = "Microsoft recovery mailbox relationship could not be resolved."
			rawResult.ProxyFailure = false
		}
		if credentialsAuthoritative {
			credentialsKnownAuthoritative = true
			if strings.TrimSpace(rawResult.ClientID) != "" {
				authoritativeClientID = strings.TrimSpace(rawResult.ClientID)
			}
			if strings.TrimSpace(rawResult.RefreshToken) != "" {
				authoritativeRefreshToken = strings.TrimSpace(rawResult.RefreshToken)
			}
		} else if credentialsKnownAuthoritative {
			// A later attempt may fail before producing replacement credentials.
			// Keep the last credentials that this validation run obtained from a
			// successful RT/password OAuth exchange; the current failure category
			// and Valid flag remain authoritative for the validation outcome.
			rawResult.ClientID = authoritativeClientID
			rawResult.RefreshToken = authoritativeRefreshToken
			credentialsAuthoritative = true
		}
		if recoveredBinding == nil && credentialsAuthoritative && normalBindingHasCompleteVerifiedAddress(rawResult) {
			bindingConfirmedThisRun = true
			effectiveBindingAddress = strings.ToLower(strings.TrimSpace(rawResult.BindingAddress))
			confirmedBindingObservation = &mailinfra.MicrosoftOAuthResult{
				BindingAddress: effectiveBindingAddress,
				BindingStatus:  string(maildomain.MicrosoftBindingVerified),
			}
		}
		if strings.TrimSpace(rawResult.ClientID) != "" {
			req.ClientID = strings.TrimSpace(rawResult.ClientID)
		}
		if strings.TrimSpace(rawResult.RefreshToken) != "" {
			req.RefreshToken = strings.TrimSpace(rawResult.RefreshToken)
		}
		last = toCoreMicrosoftResult(rawResult)
		last.CredentialsAuthoritative = credentialsAuthoritative
		last.RecoveredBinding = recoveredBinding
		if recoveredBinding != nil {
			last.BindingObservation = nil
		} else if last.BindingObservation != nil {
			ensurePreparedBindingObservation(&last, rawResult, effectiveBindingAddress)
		}
		if rawResult.Valid {
			if err := a.matchHistoricalProjects(ctx, req, rawResult); err != nil {
				slog.Warn(
					"microsoft validation history matching failed",
					"resource_id", req.ResourceID,
					"request_id", req.RequestID,
					"error", err,
				)
			}
			if !proxyReported {
				_ = a.reportProxySuccess(ctx, proxyID)
			}
			return last, nil
		}
		if rawResult.ProxyFailure && proxyID != 0 {
			if !proxyReported {
				_ = a.reportProxyFailure(ctx, proxyID, rawResult.SafeMessage)
			}
			continue
		}
		if rawResult.ProxyFailure && proxyID == 0 && attempt < maxMicrosoftProxyAttempts {
			continue
		}
		if proxyID != 0 && !proxyReported {
			_ = a.reportProxySuccess(ctx, proxyID)
		}
		return last, nil
	}
	if last.SafeMessage == "" {
		last.Valid = false
		last.Category = "request"
		last.SafeMessage = "Microsoft mail service is temporarily unavailable."
	}
	last.RecoveredBinding = recoveredBinding
	if recoveredBinding != nil {
		last.BindingObservation = nil
	}
	return last, nil
}

func (a *ResourceValidationAdapter) matchHistoricalProjects(ctx context.Context, req coreapp.MicrosoftValidationRequest, result mailinfra.MicrosoftOAuthResult) error {
	if a == nil || a.history == nil || result.MailFetch == nil || len(result.MailFetch.Messages) == 0 {
		return nil
	}
	messages := make([]mailapp.HistoricalProjectMessage, 0, len(result.MailFetch.Messages))
	for _, item := range result.MailFetch.Messages {
		messages = append(messages, mailapp.HistoricalProjectMessage{
			Recipients:        historicalRecipients(item.To),
			Sender:            strings.TrimSpace(item.From),
			Subject:           strings.TrimSpace(item.Subject),
			Body:              strings.TrimSpace(item.Body),
			BodyPreview:       strings.TrimSpace(item.Preview),
			MessageIDHeader:   strings.TrimSpace(item.InternetMessageID),
			ProviderMessageID: strings.TrimSpace(item.ID),
			Protocol:          strings.TrimSpace(item.Protocol),
			Folder:            strings.TrimSpace(item.FolderLabel),
			ReceivedAt:        item.ReceivedAt.UTC(),
		})
	}
	return a.history.MatchMicrosoftHistory(ctx, mailapp.HistoricalProjectMatchRequest{
		ResourceID:   req.ResourceID,
		EmailAddress: strings.ToLower(strings.TrimSpace(req.EmailAddress)),
		Messages:     messages,
		ScannedAt:    time.Now().UTC(),
	})
}

func historicalRecipients(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	addresses, err := stdmail.ParseAddressList(value)
	if err == nil {
		result := make([]string, 0, len(addresses))
		for _, address := range addresses {
			if normalized := strings.ToLower(strings.TrimSpace(address.Address)); normalized != "" {
				result = append(result, normalized)
			}
		}
		return result
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalized := strings.ToLower(strings.TrimSpace(part)); normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

func (a *ResourceValidationAdapter) ValidateDomain(ctx context.Context, req coreapp.DomainValidationRequest) (coreapp.DomainValidationResult, error) {
	if a == nil || a.dns == nil {
		return coreapp.DomainValidationResult{
			Valid:       false,
			Category:    "request",
			SafeMessage: "Domain DNS service is temporarily unavailable.",
		}, nil
	}
	result, err := a.dns.Validate(ctx, mailinfra.DomainDNSRequest{
		Domain:   req.Domain,
		MXRecord: req.MXRecord,
	})
	return coreapp.DomainValidationResult{
		Valid:       result.Valid,
		Category:    result.Category,
		SafeMessage: result.SafeMessage,
	}, err
}

func (a *ResourceValidationAdapter) acquireProxy(ctx context.Context, req coreapp.MicrosoftValidationRequest, attempt int) (*proxyapp.ProxyConfig, error) {
	if a == nil || a.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	purpose := proxydomain.ProxyPurposeAuth
	if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.RefreshToken) == "" {
		purpose = proxydomain.ProxyPurposeBinding
	}
	return a.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key: strings.ToLower(strings.TrimSpace(req.EmailAddress)),
		// Proxy IP-version contract: the validation task MUST use IPv4. Only mail
		// receiving (接码/收件) may use IPv6 — every other Microsoft interaction
		// (login, RT refresh, binding) requires IPv4. Do not change this to IPv6.
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             purpose,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           req.RequestID,
	})
}

// acquireBindingRecoveryProxy gives proof discovery an independent
// binding-purpose IPv4 acquisition and retry lifecycle. The proxy repository
// may still choose the same healthy resource proxy for the same account key.
func (a *ResourceValidationAdapter) acquireBindingRecoveryProxy(
	ctx context.Context,
	req coreapp.MicrosoftValidationRequest,
	attempt int,
) (*proxyapp.ProxyConfig, error) {
	if a == nil || a.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return a.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(req.EmailAddress)),
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeBinding,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           req.RequestID,
	})
}

func (a *ResourceValidationAdapter) runMicrosoftValidation(
	ctx context.Context,
	req coreapp.MicrosoftValidationRequest,
	proxyURL string,
	bindingAddress string,
	bindingAlreadyVerified bool,
) (mailinfra.MicrosoftOAuthResult, microsoftBindingRecoveryAction, bool, error) {
	oauthReq := mailinfra.MicrosoftOAuthRequest{
		EmailAddress:   req.EmailAddress,
		Password:       req.Password,
		ClientID:       req.ClientID,
		RefreshToken:   req.RefreshToken,
		BindingAddress: bindingAddress,
		ProxyURL:       proxyURL,
	}
	if !microsoftRequestHasRefreshToken(req) {
		result, err := a.microsoft.AcquireToken(ctx, oauthReq)
		prepareMicrosoftPasswordBindingResult(&result, bindingAddress, bindingAlreadyVerified)
		recoveryAction := microsoftBindingRecoveryNone
		if normalBindingNeedsRecovery(result) {
			recoveryAction = microsoftBindingRecoveryRetryPassword
		}
		if err != nil || !result.Valid {
			return result, recoveryAction, false, err
		}
		if !normalBindingHasCompleteVerifiedAddress(result) {
			if recoveryAction.needed() {
				return result, recoveryAction, true, nil
			}
			return unresolvedMicrosoftBindingResult(result, bindingAddress), microsoftBindingRecoveryNone, true, nil
		}
		result, err = a.fetchMicrosoftValidation(ctx, req.EmailAddress, proxyURL, result)
		return result, microsoftBindingRecoveryNone, true, err
	}

	refreshed, err := a.microsoft.RefreshToken(ctx, oauthReq)
	if err != nil {
		// Some protocol adapters may return a structured OAuth rejection together
		// with a diagnostic error. An explicit invalid-grant/expired category is
		// still authoritative enough to take the password fallback; transport and
		// cancellation errors without that category must not do so.
		if refreshed.Valid || !shouldFallbackInvalidRefreshToken(refreshed) || strings.TrimSpace(req.Password) == "" {
			return refreshed, microsoftBindingRecoveryNone, false, err
		}
		if cancelErr := microsoftRecoveryContextError(ctx, err); cancelErr != nil {
			return refreshed, microsoftBindingRecoveryNone, false, cancelErr
		}
	}
	if !refreshed.Valid {
		if !shouldFallbackInvalidRefreshToken(refreshed) || strings.TrimSpace(req.Password) == "" {
			return refreshed, microsoftBindingRecoveryNone, false, nil
		}
		passwordResult, passwordErr := a.acquireTokenWithBindingProxy(ctx, req, bindingAddress)
		prepareMicrosoftPasswordBindingResult(&passwordResult, bindingAddress, bindingAlreadyVerified)
		recoveryAction := microsoftBindingRecoveryNone
		if normalBindingNeedsRecovery(passwordResult) {
			recoveryAction = microsoftBindingRecoveryRetryPassword
		}
		if passwordErr != nil || !passwordResult.Valid {
			return passwordResult, recoveryAction, false, passwordErr
		}
		if !normalBindingHasCompleteVerifiedAddress(passwordResult) {
			if recoveryAction.needed() {
				return passwordResult, recoveryAction, true, nil
			}
			return unresolvedMicrosoftBindingResult(passwordResult, bindingAddress), microsoftBindingRecoveryNone, true, nil
		}
		passwordResult, passwordErr = a.fetchMicrosoftValidation(ctx, req.EmailAddress, proxyURL, passwordResult)
		return passwordResult, microsoftBindingRecoveryNone, true, passwordErr
	}

	// Refresh proves the account and supplies the authoritative OAuth credentials,
	// but it never confirms the recovery mailbox relationship. Every unresolved
	// binding must therefore perform a fresh password binding flow in this run;
	// historical failed/masked observations are not accepted as current proof.
	if bindingAlreadyVerified {
		refreshed, err = a.fetchMicrosoftValidation(ctx, req.EmailAddress, proxyURL, refreshed)
		return refreshed, microsoftBindingRecoveryNone, true, err
	}
	if strings.TrimSpace(req.Password) == "" {
		return missingMicrosoftBindingPasswordResult(refreshed, bindingAddress), microsoftBindingRecoveryNone, true, nil
	}

	bindingResult, bindingErr := a.acquireTokenWithBindingProxy(ctx, req, bindingAddress)
	prepareMicrosoftBindingResult(&bindingResult, bindingAddress)
	if bindingErr != nil {
		preserveRefreshedCredentials(&bindingResult, refreshed)
		return bindingResult, microsoftBindingRecoveryNone, true, bindingErr
	}
	if normalBindingNeedsRecovery(bindingResult) {
		mergeSupplementaryBindingResult(&refreshed, bindingResult)
		refreshed, err = a.fetchMicrosoftValidation(ctx, req.EmailAddress, proxyURL, refreshed)
		return refreshed, microsoftBindingRecoveryObserve, true, err
	}
	if !bindingResult.Valid {
		preserveRefreshedCredentials(&bindingResult, refreshed)
		return bindingResult, microsoftBindingRecoveryNone, true, nil
	}
	if !normalBindingHasCompleteVerifiedAddress(bindingResult) {
		bindingResult = unresolvedMicrosoftBindingResult(bindingResult, bindingAddress)
		preserveRefreshedCredentials(&bindingResult, refreshed)
		return bindingResult, microsoftBindingRecoveryNone, true, nil
	}
	mergeSupplementaryBindingResult(&refreshed, bindingResult)
	refreshed, err = a.fetchMicrosoftValidation(ctx, req.EmailAddress, proxyURL, refreshed)
	return refreshed, microsoftBindingRecoveryNone, true, err
}

func (a *ResourceValidationAdapter) confirmMicrosoftBindingRecoveryCandidate(
	ctx context.Context,
	req coreapp.MicrosoftValidationRequest,
	proxyURL string,
	base mailinfra.MicrosoftOAuthResult,
	baseCredentialsAuthoritative bool,
	action microsoftBindingRecoveryAction,
	candidate *microsoftBindingRecoveryCandidate,
) (mailinfra.MicrosoftOAuthResult, bool, bool, error) {
	if candidate == nil {
		return base, baseCredentialsAuthoritative, false, nil
	}
	confirmation, err := a.acquireTokenWithBindingProxy(ctx, req, candidate.Address)
	confirmationWasValid := confirmation.Valid
	protocolConfirmationAddress := strings.ToLower(strings.TrimSpace(confirmation.BindingAddress))
	protocolConfirmedAddress := isCompleteMicrosoftBindingAddress(protocolConfirmationAddress) &&
		strings.EqualFold(strings.TrimSpace(confirmation.BindingStatus), string(maildomain.MicrosoftBindingVerified))
	prepareMicrosoftBindingResult(&confirmation, candidate.Address)
	if err != nil {
		if action == microsoftBindingRecoveryObserve || (baseCredentialsAuthoritative && !confirmationWasValid) {
			preserveRefreshedCredentials(&confirmation, base)
			return confirmation, true, false, err
		}
		return confirmation, confirmationWasValid, false, err
	}
	confirmed := confirmation.Valid &&
		protocolConfirmedAddress &&
		strings.EqualFold(protocolConfirmationAddress, strings.TrimSpace(candidate.Address))
	if !confirmed {
		if confirmation.Valid || normalBindingNeedsRecovery(confirmation) {
			confirmation = unresolvedMicrosoftBindingConfirmationResult(confirmation, candidate.Address)
		} else {
			prepareUnconfirmedMicrosoftBindingObservation(&confirmation, candidate.Address)
		}
		if action == microsoftBindingRecoveryObserve || (baseCredentialsAuthoritative && !confirmationWasValid) {
			preserveRefreshedCredentials(&confirmation, base)
			return confirmation, true, false, nil
		}
		return confirmation, confirmationWasValid, false, nil
	}
	if action == microsoftBindingRecoveryObserve {
		return base, true, true, nil
	}
	confirmation, err = a.fetchMicrosoftValidation(ctx, req.EmailAddress, proxyURL, confirmation)
	return confirmation, true, true, err
}

// acquireTokenWithBindingProxy owns the full proxy lifecycle for password
// authentication started from an RT flow. A binding-proxy failure must never be
// attributed to the outer auth proxy, so retryable failures are returned with
// ProxyFailure cleared after the corresponding binding proxy is reported.
func (a *ResourceValidationAdapter) acquireTokenWithBindingProxy(
	ctx context.Context,
	req coreapp.MicrosoftValidationRequest,
	bindingAddress string,
) (mailinfra.MicrosoftOAuthResult, error) {
	last := unavailableMicrosoftBindingResult()
	for attempt := 0; attempt <= maxMicrosoftProxyAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return mailinfra.MicrosoftOAuthResult{}, err
		}
		proxyConfig, err := a.acquireBindingRecoveryProxy(ctx, req, attempt)
		if err != nil {
			if cancelErr := microsoftRecoveryContextError(ctx, err); cancelErr != nil {
				return mailinfra.MicrosoftOAuthResult{}, cancelErr
			}
			continue
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		result, acquireErr := a.microsoft.AcquireToken(ctx, mailinfra.MicrosoftOAuthRequest{
			EmailAddress:   req.EmailAddress,
			Password:       req.Password,
			BindingAddress: bindingAddress,
			ProxyURL:       proxyURL,
		})
		if cancelErr := microsoftRecoveryContextError(ctx, acquireErr); cancelErr != nil {
			return mailinfra.MicrosoftOAuthResult{}, cancelErr
		}
		if acquireErr != nil && strings.TrimSpace(result.Category) == "" {
			result = unavailableMicrosoftBindingResult()
			result.ProxyFailure = proxyID != 0
		}
		if result.ProxyFailure {
			_ = a.reportProxyFailure(ctx, proxyID, result.SafeMessage)
			result.ProxyFailure = false
			last = result
			continue
		}
		if acquireErr != nil {
			result.ProxyFailure = false
			return result, nil
		}
		_ = a.reportProxySuccess(ctx, proxyID)
		result.ProxyFailure = false
		return result, nil
	}
	last.ProxyFailure = false
	return last, nil
}

func (a *ResourceValidationAdapter) fetchMicrosoftValidation(
	ctx context.Context,
	emailAddress string,
	proxyURL string,
	result mailinfra.MicrosoftOAuthResult,
) (mailinfra.MicrosoftOAuthResult, error) {
	if a.fetcher == nil {
		result.Valid = false
		result.Category = "request"
		result.SafeMessage = "Microsoft mail service is temporarily unavailable."
		return result, nil
	}
	fetchResult, err := a.fetcher.FetchAll(ctx, mailinfra.MicrosoftMailFetchRequest{
		EmailAddress: emailAddress,
		ClientID:     result.ClientID,
		RefreshToken: result.RefreshToken,
		AccessToken:  result.AccessToken,
		ProxyURL:     proxyURL,
	})
	result.MailFetch = &fetchResult
	if strings.TrimSpace(fetchResult.RefreshToken) != "" {
		// Token exchange may have succeeded and rotated the RT before a later
		// mailbox operation returned an error. Preserve that progress even though
		// the validation outcome remains retryable.
		result.RefreshToken = strings.TrimSpace(fetchResult.RefreshToken)
	}
	if err != nil {
		result.Valid = false
		result.Category = "request"
		result.SafeMessage = "Microsoft mail service is temporarily unavailable."
		result.ProxyFailure = strings.TrimSpace(proxyURL) != ""
		return result, err
	}
	result.Valid = fetchResult.Valid
	if !fetchResult.Valid {
		result.Category = fetchResult.Category
		result.SafeMessage = fetchResult.SafeMessage
		result.ProxyFailure = fetchResult.ProxyFailure
		return result, nil
	}
	result.GraphAvailable = strings.EqualFold(fetchResult.Protocol, "graph")
	result.Category = ""
	if strings.TrimSpace(result.BindingAddress) == "" && strings.TrimSpace(result.BoundDisplay) == "" {
		result.SafeMessage = ""
	}
	result.ProxyFailure = false
	return result, nil
}

func microsoftRequestHasRefreshToken(req coreapp.MicrosoftValidationRequest) bool {
	return strings.TrimSpace(req.ClientID) != "" && strings.TrimSpace(req.RefreshToken) != ""
}

func shouldFallbackInvalidRefreshToken(result mailinfra.MicrosoftOAuthResult) bool {
	switch strings.ToLower(strings.TrimSpace(result.Category)) {
	case "oauth_invalid_grant", "refresh_token_expired", "oauth_refresh_token_expired":
		return true
	default:
		return false
	}
}

func normalBindingNeedsRecovery(result mailinfra.MicrosoftOAuthResult) bool {
	return strings.EqualFold(strings.TrimSpace(result.Category), "already_bound") ||
		strings.TrimSpace(result.BoundDisplay) != "" ||
		isMaskedMicrosoftBindingAddress(result.BindingAddress)
}

func normalBindingHasCompleteVerifiedAddress(result mailinfra.MicrosoftOAuthResult) bool {
	if !isCompleteMicrosoftBindingAddress(result.BindingAddress) {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(result.BindingStatus))
	return status == string(maildomain.MicrosoftBindingVerified) || (result.Valid && status == "")
}

func prepareMicrosoftBindingResult(result *mailinfra.MicrosoftOAuthResult, candidate string) {
	if result == nil {
		return
	}
	result.BindingAddress = strings.ToLower(strings.TrimSpace(result.BindingAddress))
	result.BoundDisplay = strings.TrimSpace(result.BoundDisplay)
	protocolReturnedCompleteAddress := isCompleteMicrosoftBindingAddress(result.BindingAddress)
	if result.BindingAddress == "" && isCompleteMicrosoftBindingAddress(candidate) {
		result.BindingAddress = strings.ToLower(strings.TrimSpace(candidate))
	}
	if strings.EqualFold(strings.TrimSpace(result.BindingStatus), string(maildomain.MicrosoftBindingVerified)) &&
		(!result.Valid || !protocolReturnedCompleteAddress) {
		// A local candidate may be retained for a later OTP attempt or pending
		// observation, but a status-only protocol response does not prove that
		// Microsoft confirmed this exact address.
		result.BindingStatus = string(maildomain.MicrosoftBindingPending)
	}
	if strings.TrimSpace(result.BindingStatus) != "" || result.BindingAddress == "" {
		return
	}
	switch {
	case normalBindingNeedsRecovery(*result):
		result.BindingStatus = string(maildomain.MicrosoftBindingFailed)
	case result.Valid && protocolReturnedCompleteAddress:
		result.BindingStatus = string(maildomain.MicrosoftBindingVerified)
	default:
		result.BindingStatus = string(maildomain.MicrosoftBindingPending)
	}
}

func prepareMicrosoftPasswordBindingResult(result *mailinfra.MicrosoftOAuthResult, candidate string, preserveTrustedBinding bool) {
	if result == nil {
		return
	}
	protocolReturnedBindingEvidence := strings.TrimSpace(result.BindingAddress) != "" ||
		strings.TrimSpace(result.BindingStatus) != "" ||
		strings.TrimSpace(result.BoundDisplay) != "" ||
		strings.EqualFold(strings.TrimSpace(result.Category), "already_bound")
	prepareMicrosoftBindingResult(result, candidate)
	if preserveTrustedBinding && !protocolReturnedBindingEvidence {
		// A password/transport failure that says nothing about the recovery
		// mailbox must not turn a clean, previously verified relationship into a
		// locally manufactured pending observation merely because candidate was
		// supplied to the login flow.
		result.BindingAddress = ""
		result.BindingStatus = ""
		result.BoundDisplay = ""
	}
}

func mergeSupplementaryBindingResult(target *mailinfra.MicrosoftOAuthResult, binding mailinfra.MicrosoftOAuthResult) {
	if target == nil {
		return
	}
	target.BindingAddress = strings.TrimSpace(binding.BindingAddress)
	target.BindingStatus = strings.TrimSpace(binding.BindingStatus)
	target.BoundDisplay = strings.TrimSpace(binding.BoundDisplay)
	if strings.TrimSpace(binding.SafeMessage) != "" {
		target.SafeMessage = strings.TrimSpace(binding.SafeMessage)
	}
}

func preserveRefreshedCredentials(target *mailinfra.MicrosoftOAuthResult, refreshed mailinfra.MicrosoftOAuthResult) {
	if target == nil {
		return
	}
	target.ClientID = strings.TrimSpace(refreshed.ClientID)
	target.RefreshToken = strings.TrimSpace(refreshed.RefreshToken)
	target.AccessToken = strings.TrimSpace(refreshed.AccessToken)
}

func missingMicrosoftBindingPasswordResult(refreshed mailinfra.MicrosoftOAuthResult, bindingAddress string) mailinfra.MicrosoftOAuthResult {
	refreshed.Valid = false
	refreshed.Category = "password"
	refreshed.SafeMessage = "Microsoft account password is required to resolve the recovery mailbox relationship."
	refreshed.ProxyFailure = false
	prepareMicrosoftBindingResult(&refreshed, bindingAddress)
	return refreshed
}

func unresolvedMicrosoftBindingResult(result mailinfra.MicrosoftOAuthResult, bindingAddress string) mailinfra.MicrosoftOAuthResult {
	result.Valid = false
	result.Category = "request"
	result.SafeMessage = "Microsoft recovery mailbox relationship could not be resolved."
	result.ProxyFailure = false
	prepareMicrosoftBindingResult(&result, bindingAddress)
	return result
}

func unresolvedMicrosoftBindingConfirmationResult(result mailinfra.MicrosoftOAuthResult, candidateAddress string) mailinfra.MicrosoftOAuthResult {
	result.Valid = false
	result.Category = "request"
	result.SafeMessage = "Microsoft recovery mailbox confirmation did not match the resolved address."
	result.ProxyFailure = false
	result.BindingAddress = strings.ToLower(strings.TrimSpace(candidateAddress))
	result.BindingStatus = string(maildomain.MicrosoftBindingPending)
	result.BoundDisplay = ""
	return result
}

func prepareUnconfirmedMicrosoftBindingObservation(result *mailinfra.MicrosoftOAuthResult, candidateAddress string) {
	if result == nil {
		return
	}
	result.BindingAddress = strings.ToLower(strings.TrimSpace(candidateAddress))
	result.BoundDisplay = ""
	if strings.TrimSpace(result.BindingStatus) == "" ||
		strings.EqualFold(strings.TrimSpace(result.BindingStatus), string(maildomain.MicrosoftBindingVerified)) {
		result.BindingStatus = string(maildomain.MicrosoftBindingPending)
	}
}

func unavailableMicrosoftBindingResult() mailinfra.MicrosoftOAuthResult {
	return mailinfra.MicrosoftOAuthResult{
		Category:    "request",
		SafeMessage: "Microsoft authorization request failed temporarily.",
	}
}

func unavailableMicrosoftBindingRecoveryResult(result mailinfra.MicrosoftOAuthResult, bindingAddress string) mailinfra.MicrosoftOAuthResult {
	result.Valid = false
	result.Category = "request"
	result.SafeMessage = "Microsoft recovery mailbox lookup is temporarily unavailable."
	result.ProxyFailure = false
	result.BoundDisplay = ""
	if isCompleteMicrosoftBindingAddress(bindingAddress) {
		result.BindingAddress = strings.ToLower(strings.TrimSpace(bindingAddress))
		result.BindingStatus = string(maildomain.MicrosoftBindingPending)
	} else {
		result.BindingAddress = ""
		result.BindingStatus = ""
	}
	return result
}

func (a *ResourceValidationAdapter) reportProxySuccess(ctx context.Context, proxyID uint) error {
	if a == nil || a.proxies == nil || proxyID == 0 {
		return nil
	}
	return a.proxies.ReportSuccess(ctx, proxyID)
}

func (a *ResourceValidationAdapter) reportProxyFailure(ctx context.Context, proxyID uint, safeError string) error {
	if a == nil || a.proxies == nil || proxyID == 0 {
		return nil
	}
	return a.proxies.ReportFailure(ctx, proxyID, safeError)
}

func toCoreMicrosoftResult(result mailinfra.MicrosoftOAuthResult) coreapp.MicrosoftValidationResult {
	return coreapp.MicrosoftValidationResult{
		Valid:              result.Valid,
		ClientID:           result.ClientID,
		RefreshToken:       result.RefreshToken,
		GraphAvailable:     result.GraphAvailable,
		Category:           result.Category,
		SafeMessage:        result.SafeMessage,
		BindingObservation: bindingObservationFromOAuthResult(result),
	}
}

func bindingObservationFromOAuthResult(result mailinfra.MicrosoftOAuthResult) *coreapp.MicrosoftBindingObservation {
	address := strings.ToLower(strings.TrimSpace(result.BindingAddress))
	boundDisplay := strings.TrimSpace(result.BoundDisplay)
	if isMaskedMicrosoftBindingAddress(address) {
		if boundDisplay == "" {
			boundDisplay = address
		}
		address = ""
	} else if address != "" && !isCompleteMicrosoftBindingAddress(address) {
		address = ""
	}
	if address == "" && boundDisplay == "" {
		return nil
	}
	status := strings.TrimSpace(result.BindingStatus)
	if result.Valid && address != "" && status == "" {
		status = string(maildomain.MicrosoftBindingVerified)
	}
	return &coreapp.MicrosoftBindingObservation{
		Address:      address,
		Status:       status,
		BoundDisplay: boundDisplay,
		SafeMessage:  strings.TrimSpace(result.SafeMessage),
	}
}

func ensurePreparedBindingObservation(result *coreapp.MicrosoftValidationResult, raw mailinfra.MicrosoftOAuthResult, bindingAddress string) {
	if result == nil {
		return
	}
	address := strings.ToLower(strings.TrimSpace(bindingAddress))
	if !isCompleteMicrosoftBindingAddress(address) {
		address = ""
	}
	if observation := result.BindingObservation; observation != nil {
		if strings.TrimSpace(observation.BoundDisplay) != "" {
			// An already-bound response may echo Microsoft's masked proof in
			// BindingAddress. Never persist that mask as an active address: use
			// only the complete candidate prepared before the login. For an RT
			// account with no candidate this deliberately stays empty, so the
			// commit can update an existing row but cannot create a masked one.
			observation.Address = address
		} else if strings.TrimSpace(observation.Address) == "" && address != "" {
			observation.Address = address
		}
		return
	}
	if address == "" {
		return
	}
	status := strings.TrimSpace(raw.BindingStatus)
	if status == "" {
		status = string(maildomain.MicrosoftBindingPending)
	}
	result.BindingObservation = &coreapp.MicrosoftBindingObservation{
		Address:     address,
		Status:      status,
		SafeMessage: strings.TrimSpace(raw.SafeMessage),
	}
}

func (a *ResourceValidationAdapter) microsoftBindingSnapshot(ctx context.Context, resourceID uint) (*maildomain.MicrosoftBindingMailbox, error) {
	if a == nil || a.bindings == nil || resourceID == 0 {
		return nil, nil
	}
	items, err := a.bindings.FindByResourceIDs(ctx, []uint{resourceID})
	if err != nil {
		return nil, err
	}
	binding, ok := items[resourceID]
	if !ok {
		return nil, nil
	}
	copyBinding := binding
	return &copyBinding, nil
}

func bindingSnapshotPreferredAddress(binding *maildomain.MicrosoftBindingMailbox, accountEmail string) string {
	if !bindingSnapshotHasCompleteVerifiedAddress(binding, accountEmail) {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(binding.BindingAddress))
}

func shouldProbeBindingRecovery(binding *maildomain.MicrosoftBindingMailbox, accountEmail string) bool {
	return !bindingSnapshotHasCompleteVerifiedAddress(binding, accountEmail)
}

func bindingSnapshotHasCompleteVerifiedAddress(binding *maildomain.MicrosoftBindingMailbox, accountEmail string) bool {
	accountEmail = strings.ToLower(strings.TrimSpace(accountEmail))
	return binding != nil &&
		binding.Status == maildomain.MicrosoftBindingVerified &&
		isCompleteMicrosoftBindingAddress(binding.BindingAddress) &&
		accountEmail != "" &&
		strings.EqualFold(strings.TrimSpace(binding.AccountEmail), accountEmail) &&
		strings.TrimSpace(binding.BoundDisplay) == "" &&
		strings.TrimSpace(binding.CodeMessageID) == ""
}

func isCompleteMicrosoftBindingAddress(address string) bool {
	address = strings.TrimSpace(address)
	if address == "" || strings.Contains(address, "*") || strings.ContainsAny(address, "\r\n\t") {
		return false
	}
	parsed, err := stdmail.ParseAddress(address)
	if err != nil || !strings.EqualFold(strings.TrimSpace(parsed.Address), address) {
		return false
	}
	parts := strings.Split(parsed.Address, "@")
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

func isMaskedMicrosoftBindingAddress(address string) bool {
	address = strings.TrimSpace(address)
	return strings.Contains(address, "*") && strings.Contains(address, "@")
}

// recoverBindingForValidation performs only the side-effect-free Microsoft
// proof-picker lookup. It never sends an OTP and never invokes password reset.
// A candidate is returned only when the proof resolves uniquely to a configured
// project binding domain and the local mailbox reader is currently usable. It
// is never persisted directly: a normal password/OTP flow must confirm the same
// complete address before the adapter emits a fenced RecoveredBinding fact.
func (a *ResourceValidationAdapter) recoverBindingForValidation(
	ctx context.Context,
	req coreapp.MicrosoftValidationRequest,
	snapshot *maildomain.MicrosoftBindingMailbox,
) (*microsoftBindingRecoveryCandidate, bool, error) {
	if a == nil || a.bindings == nil || !shouldProbeBindingRecovery(snapshot, req.EmailAddress) {
		return nil, false, nil
	}
	probe := a.probePasswordRecovery
	if probe == nil {
		probe = msacl.ProbePasswordRecovery
	}
	evaluate := a.evaluateBindingEligibility
	if evaluate == nil {
		evaluate = msacl.EvaluateActiveBindingRecoveryEligibility
	}

	for attempt := 0; attempt <= maxMicrosoftProxyAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		proxyConfig, err := a.acquireBindingRecoveryProxy(ctx, req, attempt)
		if err != nil {
			if cancelErr := microsoftRecoveryContextError(ctx, err); cancelErr != nil {
				return nil, false, cancelErr
			}
			if attempt < maxMicrosoftProxyAttempts {
				continue
			}
			logMicrosoftBindingRecoverySkip(req, "proxy_unavailable")
			return nil, true, nil
		}

		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		// An unresolved row must never bias proof selection. In particular, a
		// deterministic/generated or historically masked address is only a login
		// candidate, not verified evidence that may short-circuit enumeration.
		result, err := probe(ctx, req.EmailAddress, proxyURL, "")
		if err != nil {
			if cancelErr := microsoftRecoveryContextError(ctx, err); cancelErr != nil {
				return nil, false, cancelErr
			}
			if !isTemporaryMicrosoftRecoveryProbeError(err) {
				_ = a.reportProxySuccess(ctx, proxyID)
				logMicrosoftBindingRecoverySkip(req, "probe_rejected")
				return nil, false, nil
			}
			if attempt < maxMicrosoftProxyAttempts {
				continue
			}
			logMicrosoftBindingRecoverySkip(req, "probe_unavailable")
			return nil, true, nil
		}

		_ = a.reportProxySuccess(ctx, proxyID)
		eligibility := evaluate(ctx, result)
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !eligibility.Allowed {
			logMicrosoftBindingRecoverySkip(req, string(eligibility.Reason))
			return nil, false, nil
		}
		recovered := &microsoftBindingRecoveryCandidate{
			Address: strings.ToLower(strings.TrimSpace(result.BindingAddress)),
		}
		if !isCompleteMicrosoftBindingAddress(recovered.Address) {
			logMicrosoftBindingRecoverySkip(req, "unresolved")
			return nil, false, nil
		}
		if snapshot != nil {
			recovered.ExpectedBindingID = snapshot.ID
			recovered.ExpectedBindingAddress = strings.ToLower(strings.TrimSpace(snapshot.BindingAddress))
			recovered.ExpectedBindingUpdatedAt = snapshot.UpdatedAt
		}
		return recovered, false, nil
	}
	return nil, true, nil
}

func microsoftRecoveryContextError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

func isTemporaryMicrosoftRecoveryProbeError(err error) bool {
	var authErr *msacl.AuthError
	if !errors.As(err, &authErr) {
		return false
	}
	switch strings.TrimSpace(authErr.Status) {
	case msacl.AuthStatusRequestError, msacl.AuthStatusAuthTimeout, msacl.AuthStatusRateLimited:
		return true
	default:
		return false
	}
}

func logMicrosoftBindingRecoverySkip(req coreapp.MicrosoftValidationRequest, reason string) {
	slog.Info(
		"microsoft binding recovery safeguard skipped",
		"resource_id", req.ResourceID,
		"request_id", req.RequestID,
		"reason", strings.TrimSpace(reason),
	)
}

// prepareBindingAddress is deliberately pure. A validation worker must not
// create or rewrite binding rows before Core checks its job and credential
// fences; the chosen address returns later as a BindingObservation instead.
func (a *ResourceValidationAdapter) prepareBindingAddress(req coreapp.MicrosoftValidationRequest, preferredBindingAddress string) (string, error) {
	bindingAddress := strings.TrimSpace(preferredBindingAddress)
	if !isCompleteMicrosoftBindingAddress(bindingAddress) {
		generated, err := msacl.DeterministicAuxiliaryAddress(req.EmailAddress)
		if err != nil {
			return "", err
		}
		bindingAddress = generated
	}
	return bindingAddress, nil
}
