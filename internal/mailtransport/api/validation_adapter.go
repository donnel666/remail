package api

import (
	"context"
	"fmt"
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

type microsoftOAuthProtocol interface {
	RefreshToken(ctx context.Context, req mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error)
	AcquireToken(ctx context.Context, req mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error)
}

type ResourceValidationAdapter struct {
	proxies   *proxyapp.ProxyUseCase
	microsoft microsoftOAuthProtocol
	fetcher   *mailinfra.MicrosoftMailFetchClient
	dns       *mailinfra.DomainDNSValidator
	bindings  *mailinfra.MicrosoftBindingRepo
	history   mailapp.HistoricalProjectMatcher
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
		proxies:   proxies,
		microsoft: mailinfra.NewMicrosoftOAuthClient(),
		fetcher:   mailinfra.NewMicrosoftMailFetchClient(),
		dns:       mailinfra.NewDomainDNSValidator(),
		bindings:  bindings,
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

	preferredBindingAddress, err := a.preferredBindingAddress(ctx, req.ResourceID)
	if err != nil {
		return coreapp.MicrosoftValidationResult{}, err
	}
	preferredBindingAddress, err = a.prepareBindingAddress(ctx, req, preferredBindingAddress)
	if err != nil {
		return coreapp.MicrosoftValidationResult{}, err
	}

	var last coreapp.MicrosoftValidationResult
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

		rawResult, err := a.runMicrosoftValidation(ctx, req, proxyURL, preferredBindingAddress)
		if err != nil {
			rawResult.Valid = false
			rawResult.Category = "request"
			rawResult.SafeMessage = "Microsoft mail service is temporarily unavailable."
			rawResult.ProxyFailure = proxyID != 0
		}
		if strings.TrimSpace(rawResult.ClientID) != "" {
			req.ClientID = strings.TrimSpace(rawResult.ClientID)
		}
		if strings.TrimSpace(rawResult.RefreshToken) != "" {
			req.RefreshToken = strings.TrimSpace(rawResult.RefreshToken)
		}
		last = toCoreMicrosoftResult(rawResult)
		_ = a.recordBindingResult(ctx, req, rawResult)
		if rawResult.Valid {
			if err := a.matchHistoricalProjects(ctx, req, rawResult); err != nil {
				slog.Warn(
					"microsoft validation history matching failed",
					"resource_id", req.ResourceID,
					"request_id", req.RequestID,
					"error", err,
				)
			}
			_ = a.reportProxySuccess(ctx, proxyID)
			return last, nil
		}
		if rawResult.ProxyFailure && proxyID != 0 {
			_ = a.reportProxyFailure(ctx, proxyID, rawResult.SafeMessage)
			continue
		}
		if rawResult.ProxyFailure && proxyID == 0 && attempt < maxMicrosoftProxyAttempts {
			continue
		}
		if proxyID != 0 {
			_ = a.reportProxySuccess(ctx, proxyID)
		}
		return last, nil
	}
	if last.SafeMessage == "" {
		last = coreapp.MicrosoftValidationResult{
			Valid:       false,
			Category:    "request",
			SafeMessage: "Microsoft mail service is temporarily unavailable.",
		}
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

// acquireBindingProxy obtains a binding-purpose proxy for a supplementary
// recovery-mailbox resolution login. It is best-effort: on any failure it
// returns an empty URL so the caller can fall back to its existing proxy.
func (a *ResourceValidationAdapter) acquireBindingProxy(ctx context.Context, req coreapp.MicrosoftValidationRequest) string {
	if a == nil || a.proxies == nil {
		return ""
	}
	cfg, err := a.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key: strings.ToLower(strings.TrimSpace(req.EmailAddress)),
		// Binding-resolution login MUST use IPv4 (see acquireProxy contract): only
		// mail receiving may use IPv6.
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeBinding,
		AllowSystemFallback: true,
		RequestID:           req.RequestID,
	})
	if err != nil || cfg == nil || cfg.Direct {
		return ""
	}
	return cfg.URL
}

func (a *ResourceValidationAdapter) runMicrosoftValidation(ctx context.Context, req coreapp.MicrosoftValidationRequest, proxyURL string, bindingAddress string) (mailinfra.MicrosoftOAuthResult, error) {
	oauthReq := mailinfra.MicrosoftOAuthRequest{
		EmailAddress:   req.EmailAddress,
		Password:       req.Password,
		ClientID:       req.ClientID,
		RefreshToken:   req.RefreshToken,
		BindingAddress: bindingAddress,
		ProxyURL:       proxyURL,
	}
	var result mailinfra.MicrosoftOAuthResult
	var err error
	isRefreshTokenAccount := strings.TrimSpace(req.ClientID) != "" && strings.TrimSpace(req.RefreshToken) != ""
	if isRefreshTokenAccount {
		result, err = a.microsoft.RefreshToken(ctx, oauthReq)
	} else {
		result, err = a.microsoft.AcquireToken(ctx, oauthReq)
	}
	if err != nil || !result.Valid {
		return result, err
	}
	// A valid refresh token proves the account works but never resolves the
	// recovery-mailbox binding — the token exchange does not touch it. Alias/OTP
	// tasks require a real, receivable binding_address, so complete the binding
	// relationship with a password login when it has not yet been resolved to a
	// verified project mailbox or a known external one. This augments only the
	// binding facts; the refresh-token validation result stays authoritative.
	if isRefreshTokenAccount {
		a.resolveBindingForRefreshedAccount(ctx, req, proxyURL, bindingAddress, &result)
	}
	if a.fetcher == nil {
		result.Valid = false
		result.Category = "request"
		result.SafeMessage = "Microsoft mail service is temporarily unavailable."
		return result, nil
	}
	fetchResult, err := a.fetcher.FetchAll(ctx, mailinfra.MicrosoftMailFetchRequest{
		EmailAddress: req.EmailAddress,
		ClientID:     result.ClientID,
		RefreshToken: result.RefreshToken,
		AccessToken:  result.AccessToken,
		ProxyURL:     proxyURL,
	})
	result.MailFetch = &fetchResult
	if err != nil {
		result.Valid = false
		result.Category = "request"
		result.SafeMessage = "Microsoft mail service is temporarily unavailable."
		result.ProxyFailure = strings.TrimSpace(proxyURL) != ""
		return result, err
	}
	if strings.TrimSpace(fetchResult.RefreshToken) != "" {
		result.RefreshToken = strings.TrimSpace(fetchResult.RefreshToken)
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
	result.SafeMessage = ""
	result.ProxyFailure = false
	return result, nil
}

// resolveBindingForRefreshedAccount completes the recovery-mailbox binding for a
// refresh-token account whose token exchange succeeded. The token path never
// touches the binding relationship, so a password login is required to discover
// (or bind) the real recovery mailbox. It is best-effort: any failure is logged
// and leaves the refresh-token validation result intact — the account is still
// valid, only its binding stays unresolved for a later retry. Only the binding
// facts (BindingAddress / BindingStatus / BoundDisplay) are merged in; the
// caller's recordBindingResult then persists them.
func (a *ResourceValidationAdapter) resolveBindingForRefreshedAccount(ctx context.Context, req coreapp.MicrosoftValidationRequest, proxyURL, bindingAddress string, result *mailinfra.MicrosoftOAuthResult) {
	if a == nil || a.bindings == nil || a.microsoft == nil || result == nil {
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		// Without a password we cannot reach the identity page to resolve the
		// binding; leave it for manual handling rather than fabricate one.
		return
	}
	resolved, err := a.bindings.BindingResolved(ctx, req.ResourceID)
	if err != nil {
		slog.Warn(
			"microsoft binding resolution state lookup failed",
			"resource_id", req.ResourceID,
			"request_id", req.RequestID,
			"error", err,
		)
		return
	}
	if resolved {
		return
	}
	// A password login that reaches the identity/binding page needs a
	// binding-purpose (residential) proxy to avoid risk control, exactly like the
	// non-refresh-token AcquireToken path. The refresh-token validation above used
	// an auth-purpose proxy, so acquire a dedicated one here and fall back to the
	// already-acquired proxy only if none is available.
	resolutionProxyURL := proxyURL
	if bindingProxyURL := a.acquireBindingProxy(ctx, req); bindingProxyURL != "" {
		resolutionProxyURL = bindingProxyURL
	}
	bindingResult, err := a.microsoft.AcquireToken(ctx, mailinfra.MicrosoftOAuthRequest{
		EmailAddress:   req.EmailAddress,
		Password:       req.Password,
		BindingAddress: bindingAddress,
		ProxyURL:       resolutionProxyURL,
	})
	if err != nil {
		slog.Warn(
			"microsoft binding resolution login failed",
			"resource_id", req.ResourceID,
			"request_id", req.RequestID,
			"error", err,
		)
		return
	}
	if display := strings.TrimSpace(bindingResult.BoundDisplay); display != "" {
		result.BoundDisplay = display
	}
	if addr := strings.TrimSpace(bindingResult.BindingAddress); addr != "" {
		result.BindingAddress = addr
	}
	if status := strings.TrimSpace(bindingResult.BindingStatus); status != "" {
		result.BindingStatus = status
	}
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
		Valid:          result.Valid,
		ClientID:       result.ClientID,
		RefreshToken:   result.RefreshToken,
		GraphAvailable: result.GraphAvailable,
		Category:       result.Category,
		SafeMessage:    result.SafeMessage,
	}
}

func (a *ResourceValidationAdapter) preferredBindingAddress(ctx context.Context, resourceID uint) (string, error) {
	if a == nil || a.bindings == nil {
		return "", nil
	}
	return a.bindings.PreferredAddress(ctx, resourceID)
}

func (a *ResourceValidationAdapter) prepareBindingAddress(ctx context.Context, req coreapp.MicrosoftValidationRequest, preferredBindingAddress string) (string, error) {
	if a == nil || a.bindings == nil {
		return preferredBindingAddress, nil
	}
	if strings.TrimSpace(req.ClientID) != "" && strings.TrimSpace(req.RefreshToken) != "" {
		return preferredBindingAddress, nil
	}
	bindingAddress := strings.TrimSpace(preferredBindingAddress)
	if bindingAddress == "" {
		generated, err := msacl.DeterministicAuxiliaryAddress(req.EmailAddress)
		if err != nil {
			return "", err
		}
		bindingAddress = generated
	}
	if err := a.bindings.EnsureForValidation(ctx, req.ResourceID, req.OwnerUserID, req.EmailAddress, bindingAddress); err != nil {
		return "", err
	}
	return bindingAddress, nil
}

func (a *ResourceValidationAdapter) recordBindingResult(ctx context.Context, req coreapp.MicrosoftValidationRequest, result mailinfra.MicrosoftOAuthResult) error {
	if a == nil || a.bindings == nil {
		return nil
	}
	// Bound to an external recovery mailbox (masked): record the fact and stop —
	// there is no real, receivable binding_address for us to upsert. The masked
	// address goes to both bound_display and the (admin-facing) last_safe_error.
	if display := strings.TrimSpace(result.BoundDisplay); display != "" {
		return a.bindings.RecordBoundDisplay(ctx, req.ResourceID, display,
			fmt.Sprintf("Microsoft account is already bound to recovery mailbox (%s).", display))
	}
	bindingAddress := strings.TrimSpace(result.BindingAddress)
	if bindingAddress == "" {
		return nil
	}
	if err := a.bindings.UpsertForResource(ctx, req.ResourceID, req.OwnerUserID, req.EmailAddress, bindingAddress); err != nil {
		return err
	}
	switch result.BindingStatus {
	case string(maildomain.MicrosoftBindingCodeSent):
		return a.bindings.MarkStatus(ctx, req.ResourceID, bindingAddress, maildomain.MicrosoftBindingCodeSent, "")
	case string(maildomain.MicrosoftBindingVerified):
		return a.bindings.MarkStatus(ctx, req.ResourceID, bindingAddress, maildomain.MicrosoftBindingVerified, "")
	case string(maildomain.MicrosoftBindingTimeout):
		return a.bindings.MarkStatus(ctx, req.ResourceID, bindingAddress, maildomain.MicrosoftBindingTimeout, result.SafeMessage)
	case string(maildomain.MicrosoftBindingFailed):
		return a.bindings.MarkStatus(ctx, req.ResourceID, bindingAddress, maildomain.MicrosoftBindingFailed, result.SafeMessage)
	default:
		if result.Valid {
			return a.bindings.MarkStatus(ctx, req.ResourceID, bindingAddress, maildomain.MicrosoftBindingVerified, "")
		}
	}
	return nil
}
