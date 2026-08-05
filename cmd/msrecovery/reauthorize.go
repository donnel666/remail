package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	reauthorizeBranchHard     = "hard_reauthorize"
	reauthorizeBranchExternal = "external_binding_refresh"
	graphVerificationAttempts = 3
)

var errReauthorizeAliasesIncomplete = errors.New("explicit alias creation did not complete")
var graphVerificationRetryDelay = 2 * time.Second

type reauthorizeProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

type reauthorizeProxyRoute struct {
	URL      string
	Label    string
	Attempts int
	Config   *proxyapp.ProxyConfig
	Managed  bool
}

type reauthorizeProtocol func(
	context.Context,
	string,
	string,
	string,
	string,
	[]string,
) (msacl.ReauthorizeResult, error)

func executeReauthorizationWithProxy(
	ctx context.Context,
	proxies reauthorizeProxyProvider,
	options commandOptions,
	snapshot recoverySnapshot,
	bindingAddress string,
	candidates []string,
	authorize reauthorizeProtocol,
) (msacl.ReauthorizeResult, reauthorizeProxyRoute, error) {
	if proxyURL := strings.TrimSpace(options.Proxy); proxyURL != "" {
		raw, err := authorize(ctx, snapshot.AccountEmail, snapshot.Password, proxyURL, bindingAddress, candidates)
		return raw, reauthorizeProxyRoute{URL: proxyURL, Label: "explicit", Attempts: 1}, err
	}
	if proxies == nil {
		return msacl.ReauthorizeResult{}, reauthorizeProxyRoute{}, errors.New("production proxy selection is unavailable")
	}

	maxProxyAttempts := runtimeconfig.Int("max_proxy_attempts", 3, 1)
	var avoidServerIDs []uint
	for attempt := 0; attempt <= maxProxyAttempts; attempt++ {
		config, err := proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
			Key: strings.ToLower(strings.TrimSpace(snapshot.AccountEmail)),
			// This flow performs password login, proof binding, and alias creation;
			// production requires one IPv4 binding-purpose route for all of it.
			IPVersion:           proxydomain.ProxyIPv4,
			Purpose:             proxydomain.ProxyPurposeBinding,
			AllowSystemFallback: true,
			Attempt:             attempt,
			RequestID:           options.RequestID,
			AvoidProxyServerIDs: avoidServerIDs,
		})
		route := newReauthorizeProxyRoute(config, attempt+1)
		if err != nil {
			return msacl.ReauthorizeResult{}, route, fmt.Errorf("acquire production proxy: %w", err)
		}

		raw, authorizeErr := authorize(
			ctx,
			snapshot.AccountEmail,
			snapshot.Password,
			route.URL,
			bindingAddress,
			candidates,
		)
		proxyFailure := raw.ProxyFailure || msacl.IsProxyTransportError(authorizeErr)
		if !proxyFailure || raw.CleanupAttempted || len(raw.AliasResults) != 0 || attempt == maxProxyAttempts {
			return raw, route, authorizeErr
		}
		if route.Managed {
			_ = proxies.ReportFailure(ctx, route.Config.ID, firstCommandValue(raw.SafeMessage, "Microsoft proxy request failed."))
			avoidServerIDs = proxyapp.AppendAvoidProxyServerID(avoidServerIDs, route.Config)
		}
	}
	return msacl.ReauthorizeResult{}, reauthorizeProxyRoute{}, errors.New("production proxy attempts were exhausted")
}

func newReauthorizeProxyRoute(config *proxyapp.ProxyConfig, attempts int) reauthorizeProxyRoute {
	route := reauthorizeProxyRoute{Label: "direct", Attempts: attempts, Config: config}
	if config == nil || config.Direct {
		return route
	}
	route.URL = strings.TrimSpace(config.URL)
	route.Label = strings.TrimSpace(string(config.Pool))
	if route.Label == "" {
		route.Label = "proxy"
	}
	route.Managed = config.ID != 0
	return route
}

func reportReauthorizeProxyOutcome(
	ctx context.Context,
	proxies reauthorizeProxyProvider,
	route reauthorizeProxyRoute,
	proxyFailure bool,
	safeError string,
) {
	if proxies == nil || !route.Managed || route.Config == nil || (ctx != nil && ctx.Err() != nil) {
		return
	}
	if proxyFailure {
		_ = proxies.ReportFailure(ctx, route.Config.ID, firstCommandValue(safeError, "Microsoft proxy request failed."))
		return
	}
	_ = proxies.ReportSuccess(ctx, route.Config.ID)
}

func executeHardReauthorize(
	ctx context.Context,
	runtime *recoveryRuntime,
	options commandOptions,
	snapshot recoverySnapshot,
	result *commandResult,
) (*commandResult, error) {
	result.AliasCandidates = options.AliasCount
	if options.Apply && !strings.EqualFold(snapshot.AccountEmail, options.ConfirmEmail) {
		return result, fmt.Errorf("confirmed email does not match the selected resource")
	}
	if err := runtime.store.preflightReauthorize(ctx, snapshot, options.OperatorUserID, options.Apply); err != nil {
		return result, err
	}
	if !options.Apply {
		return result, nil
	}

	candidates, err := msacl.GenerateExplicitAliasCandidates(options.AliasCount, snapshot.AccountEmail)
	if err != nil {
		return result, errors.New("microsoft alias candidates could not be generated")
	}
	raw, proxyRoute, err := executeReauthorizationWithProxy(
		ctx,
		runtime.proxies,
		options,
		snapshot,
		preferredReauthorizeBinding(snapshot, runtime.domains),
		candidates,
		msacl.ReauthorizeWithAliases,
	)
	result.ProxyRoute = proxyRoute.Label
	result.ProxyAttempts = proxyRoute.Attempts
	proxyURL := proxyRoute.URL
	aliasProxyFailure, aliasProxySafeError := reauthorizedAliasProxyFailure(raw.AliasResults)
	proxyFailure := raw.ProxyFailure || aliasProxyFailure || msacl.IsProxyTransportError(err)
	proxySafeError := firstCommandValue(raw.SafeMessage, aliasProxySafeError)
	defer func() {
		reportReauthorizeProxyOutcome(ctx, runtime.proxies, proxyRoute, proxyFailure, proxySafeError)
	}()
	if err != nil {
		return result, err
	}
	result.ExternalBinding = raw.ExternalBinding
	result.ConsentCleanupAttempted = raw.CleanupAttempted
	result.ConsentsBefore = raw.ConsentCleanup.Before
	result.ConsentsRemoved = raw.ConsentCleanup.Removed
	result.ConsentsRemaining = raw.ConsentCleanup.Remaining
	result.Category = strings.TrimSpace(raw.Category)

	oauthResult := mailinfra.MicrosoftOAuthResult{}
	confirmedAliases := confirmedReauthorizedAliases(raw.AliasResults)
	result.AliasAttempted, result.AliasConfirmed, _ = summarizeReauthorizedAliases(raw.AliasResults)
	switch {
	case raw.ExternalBinding:
		result.SecurityBranch = reauthorizeBranchExternal
		result.Category = "external_binding"
		if strings.TrimSpace(snapshot.ClientID) == "" || strings.TrimSpace(snapshot.RefreshToken) == "" {
			return result, errors.New("external-binding downgrade requires existing Microsoft OAuth credentials")
		}
		oauthResult, err = mailinfra.NewMicrosoftOAuthClient().RefreshToken(ctx, mailinfra.MicrosoftOAuthRequest{
			EmailAddress: snapshot.AccountEmail,
			ClientID:     snapshot.ClientID,
			RefreshToken: snapshot.RefreshToken,
			ProxyURL:     proxyURL,
		})
		proxyFailure = proxyFailure || oauthResult.ProxyFailure || msacl.IsProxyTransportError(err)
		proxySafeError = firstCommandValue(oauthResult.SafeMessage, proxySafeError)
		if err != nil {
			return result, errors.New("microsoft refresh-token exchange is temporarily unavailable")
		}
		if !oauthResult.Valid {
			result.Category = strings.TrimSpace(oauthResult.Category)
			return result, safeMicrosoftCommandError(oauthResult.SafeMessage)
		}
	case raw.Valid:
		result.SecurityBranch = reauthorizeBranchHard
		result.Category = ""
		oauthResult = mailinfra.MicrosoftOAuthResult{
			Valid:          true,
			ClientID:       raw.ClientID,
			AccessToken:    raw.AccessToken,
			RefreshToken:   raw.RefreshToken,
			BindingAddress: raw.BindingAddress,
			BindingStatus:  raw.BindingStatus,
		}
	default:
		return result, safeMicrosoftCommandError(raw.SafeMessage)
	}

	clientID := strings.TrimSpace(oauthResult.ClientID)
	refreshToken := strings.TrimSpace(oauthResult.RefreshToken)
	result.NewRefreshTokenObtained = clientID != "" && refreshToken != ""
	if !result.NewRefreshTokenObtained {
		return result, errors.New("microsoft reauthorization returned incomplete OAuth credentials")
	}
	revocationCheckErr := error(nil)
	revocationCategory := ""
	if result.SecurityBranch == reauthorizeBranchHard {
		oldClientID := strings.TrimSpace(snapshot.ClientID)
		oldRefreshToken := strings.TrimSpace(snapshot.RefreshToken)
		switch {
		case oldClientID != "" && oldRefreshToken != "":
			result.OldRefreshTokenChecked = true
			oldResult, oldErr := mailinfra.NewMicrosoftOAuthClient().RefreshToken(ctx, mailinfra.MicrosoftOAuthRequest{
				EmailAddress: snapshot.AccountEmail,
				ClientID:     oldClientID,
				RefreshToken: oldRefreshToken,
				ProxyURL:     proxyURL,
			})
			proxyFailure = proxyFailure || oldResult.ProxyFailure || msacl.IsProxyTransportError(oldErr)
			proxySafeError = firstCommandValue(oldResult.SafeMessage, proxySafeError)
			switch {
			case oldErr != nil:
				revocationCategory = "request"
				revocationCheckErr = errors.New("previous Microsoft refresh-token revocation could not be verified")
			case oldResult.Valid:
				revocationCategory = "old_rt_still_valid"
				revocationCheckErr = errors.New("previous Microsoft refresh token is still accepted after hard reauthorization")
			case isRejectedOldRefreshTokenCategory(oldResult.Category):
				result.OldRefreshTokenRejected = true
			default:
				revocationCategory = firstCommandValue(oldResult.Category, "request")
				revocationCheckErr = safeMicrosoftCommandError(oldResult.SafeMessage)
			}
		case oldClientID != "" || oldRefreshToken != "":
			revocationCategory = "old_rt_unverifiable"
			revocationCheckErr = errors.New("previous Microsoft refresh-token revocation could not be verified because stored OAuth credentials are incomplete")
		}
	}

	graphAvailable := false
	graphErr := error(nil)
	if ctx.Err() == nil {
		fetchClient := mailinfra.NewMicrosoftMailFetchClient()
		fetchResult, fetchErr := verifyReauthorizedGraph(ctx, mailinfra.MicrosoftMailFetchRequest{
			EmailAddress:   snapshot.AccountEmail,
			ClientID:       clientID,
			RefreshToken:   refreshToken,
			AccessToken:    strings.TrimSpace(oauthResult.AccessToken),
			ProxyURL:       proxyURL,
			MaxMessages:    1,
			StopAfterLimit: true,
		}, fetchClient.FetchAll)
		proxyFailure = proxyFailure || fetchResult.ProxyFailure || msacl.IsProxyTransportError(fetchErr)
		proxySafeError = firstCommandValue(fetchResult.SafeMessage, proxySafeError)
		if strings.TrimSpace(fetchResult.RefreshToken) != "" {
			refreshToken = strings.TrimSpace(fetchResult.RefreshToken)
		}
		graphAvailable = fetchErr == nil && fetchResult.Valid && strings.EqualFold(fetchResult.Protocol, "graph")
		if !graphAvailable {
			result.Category = firstCommandValue(fetchResult.Category, "request")
			graphErr = safeMicrosoftCommandError(fetchResult.SafeMessage)
		}
	} else {
		result.Category = "request"
		graphErr = context.Cause(ctx)
		if graphErr == nil {
			graphErr = ctx.Err()
		}
	}
	result.GraphAvailable = graphAvailable
	aliasesComplete := result.SecurityBranch != reauthorizeBranchHard || result.AliasConfirmed == len(candidates)
	resultErr := errors.Join(graphErr, revocationCheckErr)
	if revocationCheckErr != nil {
		result.Category = revocationCategory
	}
	if !aliasesComplete {
		_, _, aliasCategory := summarizeReauthorizedAliases(raw.AliasResults)
		if resultErr == nil {
			result.Category = firstCommandValue(aliasCategory, "alias_failed")
		}
		resultErr = errors.Join(resultErr, errReauthorizeAliasesIncomplete)
	}

	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	committed, commitErr := runtime.store.commitReauthorization(
		commitCtx,
		snapshot,
		clientID,
		refreshToken,
		graphAvailable,
		result.OldRefreshTokenChecked,
		result.OldRefreshTokenRejected,
		aliasesComplete,
		confirmedAliases,
		options.OperatorUserID,
		options.RequestID,
		result.SecurityBranch,
	)
	cancelCommit()
	if committed != nil {
		result.NewRefreshTokenPersisted = true
		result.CredentialRevision = committed.CredentialRevision
	}
	return result, errors.Join(resultErr, commitErr)
}

func verifyReauthorizedGraph(
	ctx context.Context,
	request mailinfra.MicrosoftMailFetchRequest,
	fetch func(context.Context, mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error),
) (result mailinfra.MicrosoftMailFetchResult, err error) {
	latestRefreshToken := strings.TrimSpace(request.RefreshToken)
	for attempt := 0; attempt < graphVerificationAttempts; attempt++ {
		result, err = fetch(ctx, request)
		if rotated := strings.TrimSpace(result.RefreshToken); rotated != "" {
			latestRefreshToken = rotated
		}
		if err == nil && result.Valid && strings.EqualFold(result.Protocol, "graph") {
			return result, nil
		}
		if ctx.Err() != nil || attempt+1 == graphVerificationAttempts || !retryableGraphVerification(result.Category) {
			break
		}
		if graphVerificationRetryDelay > 0 {
			timer := time.NewTimer(graphVerificationRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
		request.RefreshToken = latestRefreshToken
		request.AccessToken = ""
	}
	if strings.TrimSpace(result.RefreshToken) == "" {
		result.RefreshToken = latestRefreshToken
	}
	return result, err
}

func retryableGraphVerification(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "", "request", "auth_timeout", "rate_limited":
		return true
	default:
		return false
	}
}

func reauthorizedAliasProxyFailure(results []msacl.ExplicitAliasResult) (bool, string) {
	for _, result := range results {
		if result.ProxyFailure {
			return true, strings.TrimSpace(result.SafeMessage)
		}
	}
	return false, ""
}

func preferredReauthorizeBinding(snapshot recoverySnapshot, domains map[string]struct{}) string {
	if verified := snapshot.preferredVerifiedBinding(); verified != "" {
		return verified
	}
	if snapshot.Binding == nil || !isAllowedBindingAddress(snapshot.Binding.BindingAddress, domains) {
		return ""
	}
	return normalizeConcreteRecoveryBinding(snapshot.Binding.BindingAddress)
}

func isRejectedOldRefreshTokenCategory(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "oauth_invalid_grant", "refresh_token_expired", "oauth_refresh_token_expired":
		return true
	default:
		return false
	}
}

func summarizeReauthorizedAliases(results []msacl.ExplicitAliasResult) (attempted, confirmed int, category string) {
	confirmedSet := make(map[string]struct{})
	for _, item := range results {
		attempted += len(item.Attempted)
		if strings.TrimSpace(item.Category) != "" {
			category = strings.TrimSpace(item.Category)
		}
		for _, alias := range item.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias != "" {
				confirmedSet[alias] = struct{}{}
			}
		}
	}
	return attempted, len(confirmedSet), category
}

func confirmedReauthorizedAliases(results []msacl.ExplicitAliasResult) []string {
	seen := make(map[string]struct{})
	aliases := make([]string, 0)
	for _, item := range results {
		for _, alias := range item.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias == "" {
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

func safeMicrosoftCommandError(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Microsoft mail service is temporarily unavailable."
	}
	return errors.New(message)
}

func firstCommandValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
