package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

const maxAliasProxyAttempts = 1

type MicrosoftAliasCreationAdapter struct {
	proxies                 *proxyapp.ProxyUseCase
	authorize               func(context.Context, string, string, string, string) (msacl.Result, error)
	probePasswordRecovery   func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error)
	confirmPasswordRecovery func(context.Context, string, string, msacl.PasswordRecoveryConfirmationOptions) (msacl.PasswordRecoveryConfirmationResult, error)
}

func NewMicrosoftAliasCreationAdapter(proxies *proxyapp.ProxyUseCase) *MicrosoftAliasCreationAdapter {
	return &MicrosoftAliasCreationAdapter{
		proxies:                 proxies,
		authorize:               msacl.Authorize,
		probePasswordRecovery:   msacl.ProbePasswordRecovery,
		confirmPasswordRecovery: msacl.ConfirmPasswordRecoveryBinding,
	}
}

func (a *MicrosoftAliasCreationAdapter) GenerateMicrosoftAliasCandidates(count int) ([]string, error) {
	return msacl.GenerateExplicitAliasCandidates(count)
}

func (a *MicrosoftAliasCreationAdapter) PrepareMicrosoftAliasBinding(ctx context.Context, req mailapp.MicrosoftAliasCreationRequest) (mailapp.MicrosoftAliasBindingPreparationResult, error) {
	address := strings.ToLower(strings.TrimSpace(req.BindingAddress))
	if isCompleteMicrosoftBindingAddress(address) {
		if !msacl.UsesActiveAuxiliaryDomain(address) {
			return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: address, Category: "external_binding"}, nil
		}
		return a.confirmCurrentAliasBinding(ctx, req, address)
	}
	if address == "" {
		generated, err := msacl.DeterministicAuxiliaryAddress(req.EmailAddress)
		if err != nil {
			return mailapp.MicrosoftAliasBindingPreparationResult{}, err
		}
		return a.authorizeAliasBinding(ctx, req, generated, "")
	}
	if !isMaskedMicrosoftBindingAddress(address) {
		generated, err := msacl.DeterministicAuxiliaryAddress(req.EmailAddress)
		if err != nil {
			return mailapp.MicrosoftAliasBindingPreparationResult{}, err
		}
		return a.authorizeAliasBinding(ctx, req, generated, "")
	}
	if !msacl.UsesActiveAuxiliaryDomain(address) {
		return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: address, Category: "external_binding"}, nil
	}
	if inferred := msacl.InferBindingAddress(req.EmailAddress, address); inferred != "" {
		return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: inferred}, nil
	}
	return a.recoverAliasBindingViaPasswordRecovery(ctx, req, address)
}

func (a *MicrosoftAliasCreationAdapter) authorizeAliasBinding(ctx context.Context, req mailapp.MicrosoftAliasCreationRequest, preferredAddress, recoveryMask string) (mailapp.MicrosoftAliasBindingPreparationResult, error) {
	ctx = msacl.WithRecoveryLeaseScope(ctx, req.ResourceID, recoveryMask)
	for attempt := 0; attempt <= maxAliasProxyAttempts; attempt++ {
		proxyConfig, err := a.acquireAliasProxy(ctx, req, attempt)
		if err != nil {
			continue
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		authorize := a.authorize
		if authorize == nil {
			authorize = msacl.Authorize
		}
		result, err := authorize(ctx, req.EmailAddress, req.Password, proxyURL, preferredAddress)
		if err != nil || result.ProxyFailure {
			a.reportAliasProxyFailure(ctx, proxyID, result.SafeMessage)
			continue
		}
		a.reportAliasProxySuccess(ctx, proxyID)
		address := strings.ToLower(strings.TrimSpace(result.BindingAddress))
		if (isCompleteMicrosoftBindingAddress(address) || isMaskedMicrosoftBindingAddress(address)) && !msacl.UsesActiveAuxiliaryDomain(address) {
			return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: address, Category: "external_binding"}, nil
		}
		if result.Valid && isCompleteMicrosoftBindingAddress(address) && msacl.UsesActiveAuxiliaryDomain(address) {
			return mailapp.MicrosoftAliasBindingPreparationResult{
				BindingAddress:       address,
				ReleaseRecoveryLease: msacl.RecoveryLeaseReleaser(ctx),
			}, nil
		}
		if isMaskedMicrosoftBindingAddress(address) && msacl.UsesActiveAuxiliaryDomain(address) {
			return a.recoverAliasBindingViaPasswordRecovery(ctx, req, address)
		}
		return mailapp.MicrosoftAliasBindingPreparationResult{Category: result.Category, SafeMessage: result.SafeMessage}, nil
	}
	return mailapp.MicrosoftAliasBindingPreparationResult{ProxyFailure: true}, nil
}

func (a *MicrosoftAliasCreationAdapter) recoverAliasBindingViaPasswordRecovery(ctx context.Context, req mailapp.MicrosoftAliasCreationRequest, maskedAddress string) (mailapp.MicrosoftAliasBindingPreparationResult, error) {
	ctx = msacl.WithRecoveryLeaseScope(ctx, req.ResourceID, maskedAddress)
	for attempt := 0; attempt <= maxAliasProxyAttempts; attempt++ {
		proxyConfig, err := a.acquireAliasProxy(ctx, req, attempt)
		if err != nil {
			continue
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		confirm := a.confirmPasswordRecovery
		if confirm == nil {
			confirm = msacl.ConfirmPasswordRecoveryBinding
		}
		confirmed, err := confirm(ctx, req.EmailAddress, proxyURL, msacl.PasswordRecoveryConfirmationOptions{
			ExpectedBindingAddress: maskedAddress,
		})
		if err != nil {
			// The recovery session may already have sent a code. Do not rotate the
			// proxy and start another session for the same masked proof. Return the
			// observed mask so the task can persist that Microsoft fact before retrying.
			return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: maskedAddress}, err
		}
		a.reportAliasProxySuccess(ctx, proxyID)
		address := strings.ToLower(strings.TrimSpace(confirmed.Probe.BindingAddress))
		if confirmed.BindingConfirmed && isCompleteMicrosoftBindingAddress(address) && msacl.UsesActiveAuxiliaryDomain(address) {
			return mailapp.MicrosoftAliasBindingPreparationResult{
				BindingAddress:       address,
				ReleaseRecoveryLease: msacl.RecoveryLeaseReleaser(ctx),
			}, nil
		}
		return mailapp.MicrosoftAliasBindingPreparationResult{Category: "request", SafeMessage: "Microsoft recovery mailbox relationship could not be resolved."}, nil
	}
	return mailapp.MicrosoftAliasBindingPreparationResult{ProxyFailure: true}, nil
}

func (a *MicrosoftAliasCreationAdapter) confirmCurrentAliasBinding(ctx context.Context, req mailapp.MicrosoftAliasCreationRequest, currentAddress string) (mailapp.MicrosoftAliasBindingPreparationResult, error) {
	probe := a.probePasswordRecovery
	if probe == nil {
		probe = msacl.ProbePasswordRecovery
	}
	for attempt := 0; attempt <= maxAliasProxyAttempts; attempt++ {
		proxyConfig, err := a.acquireAliasProxy(ctx, req, attempt)
		if err != nil {
			continue
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		result, err := probe(ctx, req.EmailAddress, proxyURL, "")
		if err != nil {
			if failure, permanent := aliasBindingProbeFailure(err); permanent {
				a.reportAliasProxySuccess(ctx, proxyID)
				return failure, nil
			}
			a.reportAliasProxyFailure(ctx, proxyID, "Microsoft recovery mailbox lookup failed.")
			continue
		}
		a.reportAliasProxySuccess(ctx, proxyID)

		proofs := microsoftAliasEmailProofs(result)
		for _, proof := range proofs {
			if resolved := msacl.ResolveBindingAddress(ctx, proof, req.EmailAddress, proxyURL, currentAddress); strings.EqualFold(resolved, currentAddress) {
				return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: currentAddress}, nil
			}
		}
		if len(proofs) != 1 {
			return mailapp.MicrosoftAliasBindingPreparationResult{
				Category:    "unknown_mailbox",
				SafeMessage: "Microsoft recovery mailbox proof is missing or ambiguous.",
			}, nil
		}
		proof := proofs[0]
		if !msacl.UsesActiveAuxiliaryDomain(proof) {
			return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: proof, Category: "external_binding"}, nil
		}
		if resolved := msacl.ResolveBindingAddress(ctx, proof, req.EmailAddress, proxyURL, ""); resolved != "" {
			return mailapp.MicrosoftAliasBindingPreparationResult{BindingAddress: resolved}, nil
		}
		return a.recoverAliasBindingViaPasswordRecovery(ctx, req, proof)
	}
	return mailapp.MicrosoftAliasBindingPreparationResult{ProxyFailure: true}, nil
}

func microsoftAliasEmailProofs(result msacl.PasswordRecoveryProbeResult) []string {
	seen := make(map[string]struct{}, len(result.Proofs))
	proofs := make([]string, 0, len(result.Proofs))
	for _, proof := range result.Proofs {
		address := strings.ToLower(strings.TrimSpace(proof.MaskedAddress))
		if !strings.EqualFold(proof.Type, "Email") || address == "" || !strings.Contains(address, "@") {
			continue
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		proofs = append(proofs, address)
	}
	if len(proofs) == 0 {
		if address := strings.ToLower(strings.TrimSpace(result.MaskedBindingAddress)); strings.Contains(address, "@") {
			proofs = append(proofs, address)
		}
	}
	return proofs
}

func aliasBindingProbeFailure(err error) (mailapp.MicrosoftAliasBindingPreparationResult, bool) {
	var authErr *msacl.AuthError
	if !errors.As(err, &authErr) {
		return mailapp.MicrosoftAliasBindingPreparationResult{}, false
	}
	switch strings.TrimSpace(authErr.Status) {
	case msacl.AuthStatusUnknownMailbox:
		return mailapp.MicrosoftAliasBindingPreparationResult{Category: "unknown_mailbox", SafeMessage: "Microsoft account recovery mailbox is unavailable."}, true
	case msacl.AuthStatusMFARequired:
		return mailapp.MicrosoftAliasBindingPreparationResult{Category: "mfa", SafeMessage: "Microsoft account requires additional verification."}, true
	case msacl.AuthStatusPasskeyRequired:
		return mailapp.MicrosoftAliasBindingPreparationResult{Category: "passkey", SafeMessage: "Microsoft account requires passkey verification."}, true
	case msacl.AuthStatusPhoneVerification:
		return mailapp.MicrosoftAliasBindingPreparationResult{Category: "phone", SafeMessage: "Microsoft account requires phone verification."}, true
	case msacl.AuthStatusAccountLocked:
		return mailapp.MicrosoftAliasBindingPreparationResult{Category: "locked", SafeMessage: "Microsoft account is locked."}, true
	case msacl.AuthStatusAccountAbnormal:
		return mailapp.MicrosoftAliasBindingPreparationResult{Category: "account_abnormal", SafeMessage: "Microsoft account is restricted or requires recovery."}, true
	default:
		return mailapp.MicrosoftAliasBindingPreparationResult{}, false
	}
}

// CreateMicrosoftAliases performs a single OTC-login session: lists all existing
// aliases (for reconciliation backfill) and creates the requested candidates.
// This replaces the old dual-path (Add vs Reconcile) which used separate logins.
func (a *MicrosoftAliasCreationAdapter) CreateMicrosoftAliases(ctx context.Context, req mailapp.MicrosoftAliasCreationRequest) (mailapp.MicrosoftAliasCreationResult, error) {
	ctx = msacl.WithRecoveryLeaseScope(ctx, req.ResourceID, req.RecoveryMask)
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
		var result *msacl.SyncAndAddExplicitAliasesResult
		if req.BindingMissing {
			result = msacl.SyncAndAddExplicitAliasesWithPasswordBinding(ctx, req.EmailAddress, req.Password, proxyURL, req.BindingAddress, req.Candidates)
		} else {
			result = msacl.SyncAndAddExplicitAliases(ctx, req.EmailAddress, req.Password, proxyURL, req.BindingAddress, req.Candidates)
		}

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
		if release := msacl.RecoveryLeaseReleaser(ctx); release != nil {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err := release(releaseCtx)
			cancel()
			if err != nil {
				slog.Warn("release microsoft alias code-mail lease failed", "resource_id", req.ResourceID, "error", err)
			}
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
		Key: strings.ToLower(strings.TrimSpace(req.EmailAddress)),
		// Proxy IP-version contract: the alias-creation task MUST use IPv4. Only
		// mail receiving (接码/收件) may use IPv6 — the explicit-alias login and
		// AddAlias calls require IPv4. Do not change this to IPv6.
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
