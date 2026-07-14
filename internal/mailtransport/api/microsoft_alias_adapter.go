package api

import (
	"context"
	"log/slog"
	"strings"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

const maxAliasProxyAttempts = 1

type MicrosoftAliasCreationAdapter struct {
	proxies *proxyapp.ProxyUseCase
}

func NewMicrosoftAliasCreationAdapter(proxies *proxyapp.ProxyUseCase) *MicrosoftAliasCreationAdapter {
	return &MicrosoftAliasCreationAdapter{
		proxies: proxies,
	}
}

func (a *MicrosoftAliasCreationAdapter) GenerateMicrosoftAliasCandidates(count int) ([]string, error) {
	return msacl.GenerateExplicitAliasCandidates(count)
}

// CreateMicrosoftAliases performs a single OTC-login session: lists all existing
// aliases (for reconciliation backfill) and creates the requested candidates.
// This replaces the old dual-path (Add vs Reconcile) which used separate logins.
func (a *MicrosoftAliasCreationAdapter) CreateMicrosoftAliases(ctx context.Context, req mailapp.MicrosoftAliasCreationRequest) (mailapp.MicrosoftAliasCreationResult, error) {
	for attempt := 0; attempt <= maxAliasProxyAttempts; attempt++ {
		proxyConfig, err := a.acquireAliasProxy(ctx, req, attempt)
		if err != nil {
			return mailapp.MicrosoftAliasCreationResult{
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

		// SyncAndAddExplicitAliases does a single login (OTC, falling back to
		// password on a code timeout) then lists + creates aliases, all in one
		// session.
		result := msacl.SyncAndAddExplicitAliases(ctx, req.EmailAddress, req.Password, proxyURL, req.BindingAddress, req.Candidates)

		if result.OverallFailure != nil {
			raw := *result.OverallFailure
			if raw.Stage != "" {
				slog.Warn("microsoft alias OTC login failed",
					"resource_id", req.ResourceID,
					"category", raw.Category,
					"stage", raw.Stage,
				)
			}
			if raw.ProxyFailure && proxyID != 0 {
				a.reportAliasProxyFailure(ctx, proxyID, raw.SafeMessage)
				if attempt < maxAliasProxyAttempts {
					continue
				}
			}
			return mailapp.MicrosoftAliasCreationResult{
				Aliases:     []string{},
				Attempted:   []string{},
				Category:    raw.Category,
				SafeMessage: raw.SafeMessage,
			}, nil
		}
		if proxyID != 0 {
			a.reportAliasProxySuccess(ctx, proxyID)
		}

		// Collect add results
		confirmed := make([]string, 0, len(result.AddResults))
		attempted := make([]string, 0, len(result.AddResults))
		var lastCategory string
		var lastSafeMsg string
		for _, r := range result.AddResults {
			for _, alias := range r.Aliases {
				confirmed = append(confirmed, strings.ToLower(strings.TrimSpace(alias)))
			}
			for _, alias := range r.Attempted {
				attempted = append(attempted, strings.ToLower(strings.TrimSpace(alias)))
			}
			if r.Category != "" {
				lastCategory = r.Category
			}
			if r.SafeMessage != "" {
				lastSafeMsg = r.SafeMessage
			}
		}

		// Map existing aliases from the account for backfill reconciliation
		existing := make([]string, 0, len(result.ExistingAliases))
		for _, a := range result.ExistingAliases {
			existing = append(existing, strings.ToLower(strings.TrimSpace(a)))
		}

		return mailapp.MicrosoftAliasCreationResult{
			Aliases:         confirmed,
			Attempted:       attempted,
			ExistingAliases: existing,
			Category:        lastCategory,
			SafeMessage:     lastSafeMsg,
		}, nil
	}
	return mailapp.MicrosoftAliasCreationResult{
		Category:    "request",
		SafeMessage: "Microsoft alias service is temporarily unavailable.",
	}, nil
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
