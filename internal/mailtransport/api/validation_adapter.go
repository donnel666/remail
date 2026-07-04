package api

import (
	"context"
	"strings"

	coreapp "github.com/donnel666/remail/internal/core/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

const maxMicrosoftProxyAttempts = 3

type ResourceValidationAdapter struct {
	proxies   *proxyapp.ProxyUseCase
	microsoft *mailinfra.MicrosoftOAuthClient
	fetcher   *mailinfra.MicrosoftMailFetchClient
	dns       *mailinfra.DomainDNSValidator
	bindings  *mailinfra.MicrosoftBindingRepo
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
			rawResult = mailinfra.MicrosoftOAuthResult{
				Valid:        false,
				Category:     "request",
				SafeMessage:  "Microsoft mail service is temporarily unavailable.",
				ProxyFailure: proxyID != 0,
			}
		}
		last = toCoreMicrosoftResult(rawResult)
		_ = a.recordBindingResult(ctx, req, rawResult)
		if rawResult.Valid {
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
	ipVersion := proxydomain.ProxyIPAuto
	purpose := proxydomain.ProxyPurposeAuth
	if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.RefreshToken) == "" {
		ipVersion = proxydomain.ProxyIPv4
		purpose = proxydomain.ProxyPurposeBinding
	}
	return a.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(req.EmailAddress)),
		IPVersion:           ipVersion,
		Purpose:             purpose,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           req.RequestID,
	})
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
	if strings.TrimSpace(req.ClientID) != "" && strings.TrimSpace(req.RefreshToken) != "" {
		result, err = a.microsoft.RefreshToken(ctx, oauthReq)
	} else {
		result, err = a.microsoft.AcquireToken(ctx, oauthReq)
	}
	if err != nil || !result.Valid {
		return result, err
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

func (a *ResourceValidationAdapter) recordBindingResult(ctx context.Context, req coreapp.MicrosoftValidationRequest, result mailinfra.MicrosoftOAuthResult) error {
	if a == nil || a.bindings == nil {
		return nil
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
