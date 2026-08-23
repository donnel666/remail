package api

import (
	"context"
	"log/slog"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
)

type microsoftValidationFallbackOAuthCredentials struct {
	email        string
	clientID     string
	refreshToken string
}

type microsoftValidationFallbackOAuthCredentialsKey struct{}

// microsoftPendingSoftFallbackKey is deliberately opt-in. The existing
// luckmail validation command keeps requiring hard reauthorization; the
// pending-resource cleanup command may accept a read-only RT+mailbox check
// after the hard flow cannot complete.
type microsoftPendingSoftFallbackKey struct{}

// WithMicrosoftPendingSoftFallback enables the pending-resource cleanup
// fallback for this context only. It never changes the normal application or
// luckmail validation policy.
func WithMicrosoftPendingSoftFallback(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, microsoftPendingSoftFallbackKey{}, true)
}

func microsoftPendingSoftFallbackEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(microsoftPendingSoftFallbackKey{}).(bool)
	return enabled
}

// WithMicrosoftValidationFallbackOAuthCredentials supplies a CMD-only OAuth
// fallback without copying secrets into durable validation tasks or manifests.
func WithMicrosoftValidationFallbackOAuthCredentials(ctx context.Context, email, clientID, refreshToken string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	credentials := microsoftValidationFallbackOAuthCredentials{
		email:        strings.ToLower(strings.TrimSpace(email)),
		clientID:     strings.TrimSpace(clientID),
		refreshToken: strings.TrimSpace(refreshToken),
	}
	if credentials.email == "" || credentials.clientID == "" || credentials.refreshToken == "" {
		return ctx
	}
	return context.WithValue(ctx, microsoftValidationFallbackOAuthCredentialsKey{}, credentials)
}

func microsoftValidationFallbackOAuthCredentialsFromContext(ctx context.Context, email string) (string, string, bool) {
	if ctx == nil {
		return "", "", false
	}
	credentials, ok := ctx.Value(microsoftValidationFallbackOAuthCredentialsKey{}).(microsoftValidationFallbackOAuthCredentials)
	if !ok || credentials.email != strings.ToLower(strings.TrimSpace(email)) || credentials.clientID == "" || credentials.refreshToken == "" {
		return "", "", false
	}
	return credentials.clientID, credentials.refreshToken, true
}

const (
	microsoftValidationAliasCount = 2
	microsoftGraphVerifyAttempts  = 3
	microsoftValidationAliasSeed  = "validation@outlook.com"
)

var microsoftGraphVerifyRetryDelay = 2 * time.Second

func (a *ResourceValidationAdapter) validateMicrosoftHardReauthorize(
	ctx context.Context,
	req coreapp.MicrosoftValidationRequest,
) (coreapp.MicrosoftValidationResult, error) {
	binding, err := a.microsoftBindingSnapshot(ctx, req.ResourceID)
	if err != nil {
		return coreapp.MicrosoftValidationResult{}, err
	}
	bindingAddress, err := a.prepareBindingAddress(req, bindingSnapshotPreferredAddress(binding, req.EmailAddress))
	if err != nil {
		return coreapp.MicrosoftValidationResult{}, err
	}
	candidates, err := microsoftValidationAliasCandidates()
	if err != nil {
		return hardReauthorizeFailure("request", "Microsoft alias candidates could not be generated."), nil
	}

	maxAttempts := microsoftProxyAttemptLimit()
	var avoidServerIDs []uint
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		proxyConfig, acquireErr := a.acquireBindingRecoveryProxy(ctx, req, attempt, avoidServerIDs)
		if acquireErr != nil {
			if cancelErr := microsoftRecoveryContextError(ctx, acquireErr); cancelErr != nil {
				return coreapp.MicrosoftValidationResult{}, cancelErr
			}
			if attempt < maxAttempts {
				continue
			}
			return coreapp.MicrosoftValidationResult{}, acquireErr
		}
		proxyURL, proxyID := microsoftProxyRoute(proxyConfig)
		raw, authorizeErr := a.hardReauthorize(
			ctx,
			req.EmailAddress,
			req.Password,
			proxyURL,
			bindingAddress,
			candidates,
		)
		if cancelErr := microsoftRecoveryContextError(ctx, authorizeErr); cancelErr != nil {
			return coreapp.MicrosoftValidationResult{}, cancelErr
		}
		if authorizeErr != nil {
			raw.Valid = false
			if isMicrosoftRateLimitedError(authorizeErr) {
				raw.Category = "rate_limited"
				raw.SafeMessage = "Microsoft authorization is temporarily rate limited."
				raw.ProxyFailure = false
			} else if isMicrosoftRecoveryMailboxBusyError(authorizeErr) {
				raw.Category = "recovery_mailbox_busy"
				raw.SafeMessage = "Microsoft recovery mailbox is already processing another verification code."
				raw.ProxyFailure = false
			} else if strings.TrimSpace(raw.Category) == "" {
				raw.Category = "request"
				raw.SafeMessage = "Microsoft authorization request failed temporarily."
				raw.ProxyFailure = msacl.IsProxyTransportError(authorizeErr)
			}
		}
		proxyFailure := raw.ProxyFailure || msacl.IsProxyTransportError(authorizeErr)
		recoveryMailboxBusy := isMicrosoftRecoveryMailboxBusyCategory(raw.Category)
		rateLimited := isMicrosoftRateLimitedCategory(raw.Category)
		if rateLimited || recoveryMailboxBusy {
			// A Microsoft quota response is not a broken proxy. Keep it neutral so
			// the CMD can cool only its local lease. A local recovery-mailbox lease
			// conflict is likewise unrelated to proxy health.
			proxyFailure = false
		}
		sideEffectsStarted := raw.ConsentCleanup.Removed > 0 || raw.Valid
		if proxyFailure && !sideEffectsStarted && !raw.ExternalBinding && attempt < maxAttempts {
			avoidServerIDs = proxyapp.AppendAvoidProxyServerID(avoidServerIDs, proxyConfig)
			_ = a.reportProxyFailure(ctx, proxyID, raw.SafeMessage)
			continue
		}

		result, downstreamProxyFailure, finishErr := a.finishMicrosoftHardReauthorize(
			ctx,
			req,
			proxyURL,
			candidates,
			raw,
		)
		if finishErr != nil {
			return coreapp.MicrosoftValidationResult{}, finishErr
		}
		if recoveryMailboxBusy {
			result.Category = "recovery_mailbox_busy"
			result.SafeMessage = "Microsoft recovery mailbox is already processing another verification code."
			downstreamProxyFailure = false
		}
		proxyFailure = proxyFailure || downstreamProxyFailure
		rateLimited = rateLimited || isMicrosoftRateLimitedCategory(result.Category)
		if rateLimited {
			_ = a.reportProxyRateLimited(ctx, proxyID)
		} else if !recoveryMailboxBusy {
			if proxyFailure {
				_ = a.reportProxyFailure(ctx, proxyID, result.SafeMessage)
			} else {
				_ = a.reportProxySuccess(ctx, proxyID)
			}
		}
		// Sent-but-unconsumed OTP leases must survive failed validation until
		// their TTL expires. Successful protocol paths release a consumed OTP
		// immediately; this post-persistence hook is only a final success cleanup.
		if result.Valid {
			result.ReleaseRecoveryLease = msacl.RecoveryLeaseReleaser(ctx)
		}
		return result, nil
	}
	return hardReauthorizeFailure("request", "Microsoft proxy attempts were exhausted."), nil
}

func microsoftValidationAliasCandidates() ([]string, error) {
	// Microsoft accepts Hotmail as an account domain but no longer provisions
	// new @hotmail.com aliases. Keep this override validation-only so the
	// independent weekly alias workflow is unchanged.
	return msacl.GenerateExplicitAliasCandidates(microsoftValidationAliasCount, microsoftValidationAliasSeed)
}

func (a *ResourceValidationAdapter) finishMicrosoftHardReauthorize(
	ctx context.Context,
	req coreapp.MicrosoftValidationRequest,
	proxyURL string,
	candidates []string,
	raw msacl.ReauthorizeResult,
) (coreapp.MicrosoftValidationResult, bool, error) {
	if raw.ExternalBinding {
		return a.finishExternalBindingRefresh(ctx, req, proxyURL, raw.Result)
	}
	if !raw.Valid {
		result := toCoreMicrosoftResult(mailinfra.MicrosoftOAuthResult{
			Valid:          false,
			Category:       raw.Category,
			SafeMessage:    raw.SafeMessage,
			ProxyFailure:   raw.ProxyFailure,
			BindingAddress: raw.BindingAddress,
			BindingStatus:  raw.BindingStatus,
		})
		return result, raw.ProxyFailure, nil
	}

	oauth := mailinfra.MicrosoftOAuthResult{
		Valid:          true,
		ClientID:       strings.TrimSpace(raw.ClientID),
		AccessToken:    strings.TrimSpace(raw.AccessToken),
		RefreshToken:   strings.TrimSpace(raw.RefreshToken),
		BindingAddress: strings.TrimSpace(raw.BindingAddress),
		BindingStatus:  strings.TrimSpace(raw.BindingStatus),
	}
	result := toCoreMicrosoftResult(oauth)
	result.CredentialsAuthoritative = oauth.ClientID != "" && oauth.RefreshToken != ""
	result.ConfirmedAliases = confirmedHardReauthorizeAliases(raw.AliasResults, candidates)
	if !result.CredentialsAuthoritative {
		result.Valid = false
		result.Category = "hard_reauthorize_incomplete"
		result.SafeMessage = "Microsoft reauthorization returned incomplete OAuth credentials."
		return result, false, nil
	}
	if !raw.CleanupAttempted || raw.ConsentCleanup.Remaining != 0 {
		result.Valid = false
		result.Category = "consent_cleanup_incomplete"
		result.SafeMessage = "Microsoft account authorization cleanup did not complete."
		return result, false, nil
	}
	if oldClientID, oldRefreshToken := strings.TrimSpace(req.ClientID), strings.TrimSpace(req.RefreshToken); oldClientID != "" || oldRefreshToken != "" {
		if oldClientID == "" || oldRefreshToken == "" {
			result.Valid = false
			result.Category = "old_rt_unverified"
			result.SafeMessage = "Previous Microsoft refresh-token revocation could not be verified."
			return result, false, nil
		}
		oldResult, refreshErr := a.microsoft.RefreshToken(ctx, mailinfra.MicrosoftOAuthRequest{
			EmailAddress: req.EmailAddress,
			ClientID:     oldClientID,
			RefreshToken: oldRefreshToken,
			ProxyURL:     proxyURL,
		})
		if cancelErr := microsoftRecoveryContextError(ctx, refreshErr); cancelErr != nil {
			return coreapp.MicrosoftValidationResult{}, oldResult.ProxyFailure, cancelErr
		}
		if refreshErr != nil {
			if isMicrosoftRateLimitedError(refreshErr) || isMicrosoftRateLimitedCategory(oldResult.Category) {
				result.Valid = false
				result.Category = "rate_limited"
				result.SafeMessage = "Microsoft authorization is temporarily rate limited."
				return result, false, nil
			}
			result.Valid = false
			result.Category = "old_rt_unverified"
			result.SafeMessage = "Previous Microsoft refresh-token revocation could not be verified."
			return result, oldResult.ProxyFailure || msacl.IsProxyTransportError(refreshErr), nil
		}
		if isMicrosoftRateLimitedCategory(oldResult.Category) {
			result.Valid = false
			result.Category = "rate_limited"
			result.SafeMessage = firstHardReauthorizeValue(oldResult.SafeMessage, "Microsoft authorization is temporarily rate limited.")
			return result, false, nil
		}
		if oldResult.Valid {
			result.Valid = false
			result.Category = "old_rt_still_valid"
			result.SafeMessage = "Previous Microsoft refresh token is still accepted after reauthorization."
			return result, false, nil
		}
		if strings.TrimSpace(oldResult.Category) == "" {
			result.Valid = false
			result.Category = "old_rt_unverified"
			result.SafeMessage = "Previous Microsoft refresh-token revocation could not be verified."
			return result, oldResult.ProxyFailure, nil
		}
		if !hardReauthorizeOldRTRejected(oldResult.Category) {
			result.Valid = false
			result.Category = "old_rt_unverified"
			result.SafeMessage = "Previous Microsoft refresh-token revocation could not be verified."
			return result, oldResult.ProxyFailure, nil
		}
	}

	verified, proxyFailure, verifyErr := a.verifyMicrosoftHardReauthorizeGraph(ctx, req.EmailAddress, proxyURL, oauth)
	if verifyErr != nil {
		return coreapp.MicrosoftValidationResult{}, proxyFailure, verifyErr
	}
	result = toCoreMicrosoftResult(verified)
	result.CredentialsAuthoritative = true
	result.ConfirmedAliases = confirmedHardReauthorizeAliases(raw.AliasResults, candidates)
	if !verified.Valid || !verified.GraphAvailable {
		result.Valid = false
		if isMicrosoftRateLimitedCategory(verified.Category) {
			result.Category = "rate_limited"
			result.SafeMessage = firstHardReauthorizeValue(verified.SafeMessage, "Microsoft authorization is temporarily rate limited.")
		} else {
			result.Category = "hard_reauthorize_graph"
			result.SafeMessage = "Microsoft Graph verification did not complete after reauthorization."
		}
		return result, proxyFailure, nil
	}
	result.Valid = true
	result.Category = ""
	result.SafeMessage = "Microsoft resource validation succeeded."
	result.AfterValidationCommit = a.validationAliasContinuation(req.ResourceID, candidates, raw)
	return result, proxyFailure, nil
}

func (a *ResourceValidationAdapter) validationAliasContinuation(
	resourceID uint,
	candidates []string,
	raw msacl.ReauthorizeResult,
) func(context.Context) error {
	if raw.ContinueAliases == nil {
		return nil
	}
	return func(ctx context.Context) error {
		results := raw.ContinueAliases()
		confirmed := confirmedHardReauthorizeAliases(results, candidates)
		failedCategories := make([]string, 0, len(results))
		for _, result := range results {
			if len(result.Aliases) > 0 {
				continue
			}
			category := strings.TrimSpace(result.Category)
			if category == "" {
				category = "alias_incomplete"
			}
			failedCategories = append(failedCategories, category)
		}
		if len(failedCategories) > 0 {
			slog.Warn(
				"microsoft validation explicit alias attempt deferred",
				"resource_id", resourceID,
				"categories", strings.Join(failedCategories, ","),
			)
		}
		if len(confirmed) == 0 || a == nil || a.validationAliases == nil {
			return nil
		}
		return a.validationAliases.BackfillExistingAliases(ctx, resourceID, confirmed)
	}
}

func (a *ResourceValidationAdapter) finishExternalBindingRefresh(
	ctx context.Context,
	req coreapp.MicrosoftValidationRequest,
	proxyURL string,
	external msacl.Result,
) (coreapp.MicrosoftValidationResult, bool, error) {
	observation := bindingObservationFromOAuthResult(mailinfra.MicrosoftOAuthResult{
		BindingAddress: external.BindingAddress,
		BindingStatus:  external.BindingStatus,
		SafeMessage:    external.SafeMessage,
	})
	attachObservation := func(result coreapp.MicrosoftValidationResult) coreapp.MicrosoftValidationResult {
		result.BindingObservation = observation
		return result
	}
	refresh := func(clientID, refreshToken string) (mailinfra.MicrosoftOAuthResult, error) {
		refreshed, refreshErr := a.microsoft.RefreshToken(ctx, mailinfra.MicrosoftOAuthRequest{
			EmailAddress: req.EmailAddress,
			ClientID:     clientID,
			RefreshToken: refreshToken,
			ProxyURL:     proxyURL,
		})
		if refreshErr != nil {
			refreshed = normalizeMicrosoftOAuthErrorResult(refreshed, refreshErr, "Microsoft refresh-token exchange is temporarily unavailable.")
		}
		return refreshed, refreshErr
	}

	fallbackClientID, fallbackRefreshToken, fallbackAvailable := microsoftValidationFallbackOAuthCredentialsFromContext(ctx, req.EmailAddress)
	primaryAvailable := microsoftRequestHasRefreshToken(req)
	var refreshed mailinfra.MicrosoftOAuthResult
	var refreshErr error
	switch {
	case primaryAvailable:
		refreshed, refreshErr = refresh(req.ClientID, req.RefreshToken)
		if !refreshed.Valid && hardReauthorizeOldRTRejected(refreshed.Category) && fallbackAvailable &&
			strings.TrimSpace(req.RefreshToken) != fallbackRefreshToken {
			refreshed, refreshErr = refresh(fallbackClientID, fallbackRefreshToken)
		}
	case fallbackAvailable:
		refreshed, refreshErr = refresh(fallbackClientID, fallbackRefreshToken)
	default:
		result := hardReauthorizeFailure("already_bound", "External recovery-mailbox binding requires existing OAuth credentials.")
		return attachObservation(result), false, nil
	}
	if cancelErr := microsoftRecoveryContextError(ctx, refreshErr); cancelErr != nil {
		return coreapp.MicrosoftValidationResult{}, refreshed.ProxyFailure, cancelErr
	}
	if !refreshed.Valid {
		result := attachObservation(toCoreMicrosoftResult(refreshed))
		return result, refreshed.ProxyFailure, nil
	}
	verified, proxyFailure, verifyErr := a.verifyMicrosoftHardReauthorizeGraph(ctx, req.EmailAddress, proxyURL, refreshed)
	if verifyErr != nil {
		return coreapp.MicrosoftValidationResult{}, proxyFailure, verifyErr
	}
	verified.BindingAddress = ""
	verified.BindingStatus = ""
	result := attachObservation(toCoreMicrosoftResult(verified))
	result.CredentialsAuthoritative = true
	if result.Valid {
		result.SafeMessage = "Microsoft resource validation succeeded through refresh-token fallback."
	}
	return result, proxyFailure, nil
}

func (a *ResourceValidationAdapter) verifyMicrosoftHardReauthorizeGraph(
	ctx context.Context,
	emailAddress string,
	proxyURL string,
	oauth mailinfra.MicrosoftOAuthResult,
) (mailinfra.MicrosoftOAuthResult, bool, error) {
	current := oauth
	proxyFailure := false
	for attempt := 0; attempt < microsoftGraphVerifyAttempts; attempt++ {
		verified, err := a.fetchMicrosoftValidation(ctx, emailAddress, proxyURL, current)
		proxyFailure = proxyFailure || verified.ProxyFailure || msacl.IsProxyTransportError(err)
		if cancelErr := microsoftRecoveryContextError(ctx, err); cancelErr != nil {
			return verified, proxyFailure, cancelErr
		}
		if verified.Valid && verified.GraphAvailable {
			return verified, proxyFailure, nil
		}
		current = verified
		current.AccessToken = ""
		if attempt+1 == microsoftGraphVerifyAttempts || !hardReauthorizeGraphRetryable(verified.Category) {
			return verified, proxyFailure, nil
		}
		if microsoftGraphVerifyRetryDelay > 0 {
			timer := time.NewTimer(microsoftGraphVerifyRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return verified, proxyFailure, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return current, proxyFailure, nil
}

func microsoftProxyRoute(config *proxyapp.ProxyConfig) (string, uint) {
	if config == nil || config.Direct {
		return "", 0
	}
	return strings.TrimSpace(config.URL), config.ID
}

func confirmedHardReauthorizeAliases(results []msacl.ExplicitAliasResult, candidates []string) []string {
	allowed := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate = strings.ToLower(strings.TrimSpace(candidate)); candidate != "" {
			allowed[candidate] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	aliases := make([]string, 0, len(candidates))
	for _, item := range results {
		for _, alias := range item.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if _, ok := allowed[alias]; !ok {
				continue
			}
			if _, ok := seen[alias]; ok {
				continue
			}
			seen[alias] = struct{}{}
			aliases = append(aliases, alias)
		}
	}
	return aliases
}

func hardReauthorizeOldRTRejected(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "oauth_invalid_grant", "refresh_token_expired", "oauth_refresh_token_expired":
		return true
	default:
		return false
	}
}

func hardReauthorizeGraphRetryable(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "", "request", "auth_timeout", "rate_limited":
		return true
	default:
		return false
	}
}

func hardReauthorizeFailure(category, message string) coreapp.MicrosoftValidationResult {
	return coreapp.MicrosoftValidationResult{Valid: false, Category: strings.TrimSpace(category), SafeMessage: strings.TrimSpace(message)}
}

func firstHardReauthorizeValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "Microsoft mail service is temporarily unavailable."
}
