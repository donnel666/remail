package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"

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

func (a *MicrosoftAliasCreationAdapter) GenerateMicrosoftAliasCandidates(count int, accountEmail string) ([]string, error) {
	return msacl.GenerateExplicitAliasCandidates(count, accountEmail)
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

// CreateMicrosoftAliases lists remote aliases for local recovery and creates new
// candidates, or performs a read-only check for previously uncertain candidates.
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
		if req.ReconcileOnly {
			var raw msacl.ExplicitAliasResult
			var reconcileErr error
			if req.BindingMissing {
				raw, reconcileErr = msacl.ReconcileExplicitAliasCandidatesWithPasswordBinding(
					ctx, req.EmailAddress, req.Password, proxyURL, req.BindingAddress, req.Candidates,
				)
			} else {
				raw, reconcileErr = msacl.ReconcileExplicitAliasCandidates(
					ctx, req.EmailAddress, req.Password, proxyURL, req.BindingAddress, req.Candidates,
				)
			}
			if reconcileErr != nil {
				return mailapp.MicrosoftAliasCreationResult{
					Category:    "request",
					SafeMessage: "Microsoft alias service is temporarily unavailable.",
				}, nil
			}
			if raw.ProxyFailure {
				if proxyID != 0 {
					a.reportAliasProxyFailure(ctx, proxyID, raw.SafeMessage)
				}
				if attempt < maxAliasProxyAttempts {
					continue
				}
				return mailapp.MicrosoftAliasCreationResult{
					Aliases:      normalizeMicrosoftAliases(raw.Aliases),
					Absent:       normalizeMicrosoftAliases(raw.Absent),
					Category:     raw.Category,
					SafeMessage:  raw.SafeMessage,
					ProxyFailure: true,
				}, nil
			}
			if proxyID != 0 {
				a.reportAliasProxySuccess(ctx, proxyID)
			}
			return mailapp.MicrosoftAliasCreationResult{
				Aliases:     normalizeMicrosoftAliases(raw.Aliases),
				Absent:      normalizeMicrosoftAliases(raw.Absent),
				Category:    raw.Category,
				SafeMessage: raw.SafeMessage,
			}, nil
		}

		// SyncAndAddExplicitAliases does one login (OTC, falling back to password
		// on a code timeout), then lists and creates aliases in that session.
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
			} else if proxyID != 0 {
				a.reportAliasProxySuccess(ctx, proxyID)
			}
			return mailapp.MicrosoftAliasCreationResult{
				Aliases:     []string{},
				Attempted:   []string{},
				Category:    raw.Category,
				SafeMessage: raw.SafeMessage,
			}, nil
		}
		summary, proxyFailure := summarizeMicrosoftAliasAddResults(result.AddResults)
		summary.ExistingAliases = normalizeMicrosoftAliases(result.ExistingAliases)
		if proxyFailure {
			if proxyID != 0 {
				a.reportAliasProxyFailure(ctx, proxyID, summary.SafeMessage)
			}
			if len(summary.Attempted) == 0 && attempt < maxAliasProxyAttempts {
				continue
			}
			summary.ProxyFailure = true
			return summary, nil
		}
		if proxyID != 0 {
			a.reportAliasProxySuccess(ctx, proxyID)
		}
		return summary, nil
	}
	return mailapp.MicrosoftAliasCreationResult{
		Category:    "request",
		SafeMessage: "Microsoft alias service is temporarily unavailable.",
	}, nil
}

func summarizeMicrosoftAliasAddResults(results []msacl.ExplicitAliasResult) (mailapp.MicrosoftAliasCreationResult, bool) {
	result := mailapp.MicrosoftAliasCreationResult{
		Aliases: normalizeMicrosoftAliases(confirmedAddedAliases(results)),
	}
	proxyFailure := false
	for _, item := range results {
		result.Attempted = append(result.Attempted, item.Attempted...)
		if isUncertainMicrosoftAliasResult(item) {
			result.Uncertain = append(result.Uncertain, item.Attempted...)
		}
		if item.Category != "" {
			result.Category = item.Category
		}
		if item.SafeMessage != "" {
			result.SafeMessage = item.SafeMessage
		}
		proxyFailure = proxyFailure || item.ProxyFailure
	}
	result.Attempted = normalizeMicrosoftAliases(result.Attempted)
	result.Uncertain = normalizeMicrosoftAliases(result.Uncertain)
	return result, proxyFailure
}

func isUncertainMicrosoftAliasResult(result msacl.ExplicitAliasResult) bool {
	if len(result.Attempted) == 0 {
		return false
	}
	switch strings.TrimSpace(result.Category) {
	case "request", "auth_timeout", "code_timeout", "code_error":
		return true
	default:
		return false
	}
}

func confirmedAddedAliases(results []msacl.ExplicitAliasResult) []string {
	confirmed := make([]string, 0, len(results))
	for _, result := range results {
		if !strings.EqualFold(strings.TrimSpace(result.Category), "added") {
			continue
		}
		for _, alias := range result.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias != "" {
				confirmed = append(confirmed, alias)
			}
		}
	}
	return confirmed
}

func normalizeMicrosoftAliases(values []string) []string {
	aliases := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		aliases = append(aliases, value)
	}
	return aliases
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
