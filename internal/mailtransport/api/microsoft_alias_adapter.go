package api

import (
	"context"
	"strings"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

type MicrosoftAliasCreationAdapter struct {
	proxies          *proxyapp.ProxyUseCase
	createAliases    func(context.Context, string, string, string, string, []string) (msacl.ExplicitAliasResult, error)
	reconcileAliases func(context.Context, string, string, string, string, []string) (msacl.ExplicitAliasResult, error)
}

func NewMicrosoftAliasCreationAdapter(proxies *proxyapp.ProxyUseCase) *MicrosoftAliasCreationAdapter {
	return &MicrosoftAliasCreationAdapter{
		proxies: proxies,
	}
}

func (a *MicrosoftAliasCreationAdapter) GenerateMicrosoftAliasCandidates(count int) ([]string, error) {
	return msacl.GenerateExplicitAliasCandidates(count)
}

func (a *MicrosoftAliasCreationAdapter) CreateMicrosoftAliases(ctx context.Context, req mailapp.MicrosoftAliasCreationRequest) (mailapp.MicrosoftAliasCreationResult, error) {
	var last mailapp.MicrosoftAliasCreationResult
	confirmed := make([]string, 0, len(req.Candidates))
	seenConfirmed := make(map[string]struct{}, len(req.Candidates))
	attempted := make([]string, 0, len(req.Candidates))
	seenAttempted := make(map[string]struct{}, len(req.Candidates))
	uncertain := make(map[string]struct{}, len(req.Candidates))
	absent := make(map[string]struct{}, len(req.Candidates))
	for attempt := 0; attempt <= maxMicrosoftProxyAttempts; attempt++ {
		proxyConfig, err := a.acquireAliasProxy(ctx, req, attempt)
		if err != nil {
			for _, alias := range attempted {
				_, confirmedAlias := seenConfirmed[alias]
				_, reconciledAbsent := absent[alias]
				if !confirmedAlias && !reconciledAbsent {
					uncertain[alias] = struct{}{}
				}
			}
			return mailapp.MicrosoftAliasCreationResult{
				Aliases:     append([]string(nil), confirmed...),
				Attempted:   append([]string(nil), attempted...),
				Uncertain:   aliasesFromSet(req.Candidates, uncertain),
				Absent:      aliasesFromSet(req.Candidates, absent),
				Category:    "request",
				SafeMessage: "Microsoft alias service is temporarily unavailable.",
			}, nil
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}

		createAliases := msacl.AddExplicitAliasCandidates
		if req.ReconcileOnly {
			createAliases = msacl.ReconcileExplicitAliasCandidates
			if a != nil && a.reconcileAliases != nil {
				createAliases = a.reconcileAliases
			}
		} else if a != nil && a.createAliases != nil {
			createAliases = a.createAliases
		}
		raw, err := createAliases(
			ctx,
			req.EmailAddress,
			req.Password,
			proxyURL,
			req.BindingAddress,
			req.Candidates,
		)
		if err != nil {
			raw = msacl.ExplicitAliasResult{
				Category:     "request",
				SafeMessage:  "Microsoft alias service is temporarily unavailable.",
				ProxyFailure: proxyID != 0,
			}
		}
		last = mailapp.MicrosoftAliasCreationResult{
			Category:     raw.Category,
			SafeMessage:  raw.SafeMessage,
			ProxyFailure: raw.ProxyFailure,
		}
		for _, alias := range raw.Aliases {
			key := strings.ToLower(strings.TrimSpace(alias))
			if key == "" {
				continue
			}
			if _, ok := seenConfirmed[key]; ok {
				continue
			}
			seenConfirmed[key] = struct{}{}
			confirmed = append(confirmed, key)
			delete(uncertain, key)
			delete(absent, key)
		}
		for _, alias := range raw.Attempted {
			key := strings.ToLower(strings.TrimSpace(alias))
			if key == "" {
				continue
			}
			if _, ok := seenAttempted[key]; !ok {
				seenAttempted[key] = struct{}{}
				attempted = append(attempted, key)
			}
			if _, ok := seenConfirmed[key]; ok {
				delete(uncertain, key)
			} else if raw.ProxyFailure || isAmbiguousAliasCategory(raw.Category) {
				uncertain[key] = struct{}{}
			} else {
				delete(uncertain, key)
			}
		}
		for _, alias := range raw.Absent {
			key := strings.ToLower(strings.TrimSpace(alias))
			if key == "" {
				continue
			}
			if _, ok := seenConfirmed[key]; ok {
				continue
			}
			absent[key] = struct{}{}
			delete(uncertain, key)
		}
		last.Aliases = append([]string(nil), confirmed...)
		last.Attempted = append([]string(nil), attempted...)
		last.Uncertain = aliasesFromSet(req.Candidates, uncertain)
		last.Absent = aliasesFromSet(req.Candidates, absent)
		if raw.ProxyFailure {
			if proxyID != 0 {
				a.reportAliasProxyFailure(ctx, proxyID, raw.SafeMessage)
			}
			if attempt < maxMicrosoftProxyAttempts {
				continue
			}
			return last, nil
		}
		a.reportAliasProxySuccess(ctx, proxyID)
		return last, nil
	}
	return last, nil
}

func isAmbiguousAliasCategory(category string) bool {
	switch strings.TrimSpace(category) {
	case "request", "auth_timeout", "code_timeout", "code_error":
		return true
	default:
		return false
	}
}

func aliasesFromSet(candidates []string, values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if _, ok := values[candidate]; ok {
			result = append(result, candidate)
		}
	}
	return result
}

func (a *MicrosoftAliasCreationAdapter) acquireAliasProxy(ctx context.Context, req mailapp.MicrosoftAliasCreationRequest, attempt int) (*proxyapp.ProxyConfig, error) {
	if a == nil || a.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return a.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(req.EmailAddress)),
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeBinding,
		AllowSystemFallback: true,
		Attempt:             attempt,
	})
}

func (a *MicrosoftAliasCreationAdapter) reportAliasProxySuccess(ctx context.Context, proxyID uint) {
	if a == nil || a.proxies == nil || proxyID == 0 {
		return
	}
	_ = a.proxies.ReportSuccess(ctx, proxyID)
}

func (a *MicrosoftAliasCreationAdapter) reportAliasProxyFailure(ctx context.Context, proxyID uint, safeError string) {
	if a == nil || a.proxies == nil || proxyID == 0 {
		return
	}
	_ = a.proxies.ReportFailure(ctx, proxyID, safeError)
}
