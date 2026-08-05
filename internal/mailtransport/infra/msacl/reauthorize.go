package msacl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
)

// ReauthorizeResult reports a validation-only account-wide consent cleanup,
// fresh OAuth grant, and same-session explicit-alias attempt.
type ReauthorizeResult struct {
	Result
	CleanupAttempted bool
	ConsentCleanup   ConsentCleanupResult
	AliasResults     []ExplicitAliasResult
	ExternalBinding  bool
}

type reauthorizeAccountResult struct {
	oauth            *AuthSuccess
	cleanupAttempted bool
	consentCleanup   ConsentCleanupResult
	aliasResults     []ExplicitAliasResult
}

// ReauthorizeWithAliases is intentionally separate from Authorize. Ordinary
// authorization and the independent alias worker must never revoke all grants.
func ReauthorizeWithAliases(
	ctx context.Context,
	email, password, proxy, preferredBindingAddress string,
	aliasCandidates []string,
) (ReauthorizeResult, error) {
	if err := ctx.Err(); err != nil {
		return ReauthorizeResult{Result: aclFailure("request", "Microsoft mail service is temporarily unavailable.", false)}, err
	}
	result, err := reauthorizeAccountImpl(ctx, email, password, proxy, preferredBindingAddress, aliasCandidates)
	public := ReauthorizeResult{}
	if result != nil {
		public.CleanupAttempted = result.cleanupAttempted
		public.ConsentCleanup = result.consentCleanup
		public.AliasResults = result.aliasResults
	}
	if err != nil {
		var authErr *AuthError
		if !errors.As(err, &authErr) {
			err = wrapAuthError(fmt.Sprintf("微软请求异常: %s", err), AuthStatusRequestError, err)
			_ = errors.As(err, &authErr)
		}
		public.Result = mapAuthError(err)
		public.ExternalBinding = authErr != nil && strings.TrimSpace(authErr.Status) == AuthStatusAlreadyBound
		return public, nil
	}
	if result == nil || result.oauth == nil || strings.TrimSpace(result.oauth.RefreshToken) == "" || strings.TrimSpace(result.oauth.AccessToken) == "" {
		public.Result = aclFailure("request", "Microsoft refresh token authorization is temporarily unavailable.", false)
		return public, nil
	}
	public.Result = Result{
		Valid:          true,
		ClientID:       strings.TrimSpace(result.oauth.ClientID),
		AccessToken:    strings.TrimSpace(result.oauth.AccessToken),
		RefreshToken:   strings.TrimSpace(result.oauth.RefreshToken),
		BindingAddress: strings.TrimSpace(result.oauth.BoundMailbox),
		BindingStatus:  string(maildomain.MicrosoftBindingVerified),
	}
	return public, nil
}

func reauthorizeAccountImpl(
	ctx context.Context,
	email, password, proxy, preferredBindingAddress string,
	aliasCandidates []string,
) (*reauthorizeAccountResult, error) {
	result := &reauthorizeAccountResult{}
	pending, err := beginAccountAuthorization(ctx, email, password, proxy, preferredBindingAddress)
	if err != nil {
		return result, err
	}
	consentPage, _, consentBinding, err := loginMicrosoftConsentManageWithBinding(
		pending.session,
		email,
		password,
		proxy,
		firstNonEmpty(pending.boundMailbox, preferredBindingAddress),
	)
	if err != nil {
		return result, err
	}
	pending.boundMailbox = firstNonEmpty(consentBinding, pending.boundMailbox)
	aliasSession := pending.session
	if !pending.hasEmailProof && strings.TrimSpace(pending.boundMailbox) == "" {
		pending.boundMailbox, aliasSession, err = bindMissingAuxiliaryEmail(
			pending.session,
			email,
			password,
			proxy,
			preferredBindingAddress,
		)
		if err != nil {
			return result, err
		}
		// Binding finishes on AddAssocId. Re-enter consent/Manage with the same
		// cookie jar before listing grants; some accounts otherwise stop on the
		// authenticated OAuth relay instead of returning the consent page.
		pending.session = aliasSession
		consentPage, _, consentBinding, err = loginMicrosoftConsentManageWithBinding(
			pending.session,
			email,
			password,
			proxy,
			firstNonEmpty(pending.boundMailbox, preferredBindingAddress),
		)
		if err != nil {
			return result, err
		}
		pending.boundMailbox = firstNonEmpty(consentBinding, pending.boundMailbox)
	}
	result.cleanupAttempted = true
	result.consentCleanup, err = removeAllMicrosoftConsents(pending.session, consentPage)
	if err != nil {
		return result, err
	}
	// Consent management and proof binding mutate the login.live.com context
	// even when there were no grants to remove. Always obtain the fresh grant in
	// a new browser session instead of reusing a stale account.live.com relay.
	pending, err = beginAccountAuthorization(
		ctx,
		email,
		password,
		proxy,
		firstNonEmpty(pending.boundMailbox, preferredBindingAddress),
	)
	if err != nil {
		return result, err
	}
	result.oauth, err = completeAccountAuthorization(pending)
	if err != nil {
		return result, err
	}
	bindingAddress := firstNonEmpty(result.oauth.BoundMailbox, preferredBindingAddress)
	result.aliasResults = addExplicitAliasCandidatesWithSession(
		aliasSession,
		email,
		proxy,
		bindingAddress,
		aliasCandidates,
	)
	return result, nil
}

func addExplicitAliasCandidatesWithSession(
	session *Session,
	email, proxy, preferredBindingAddress string,
	candidates []string,
) []ExplicitAliasResult {
	candidates = normalizeExplicitAliasCandidates(candidates)
	if session == nil || len(candidates) == 0 {
		return nil
	}
	_, currentURL, err := loginForExplicitAliasOTC(session, email, proxy, preferredBindingAddress)
	if err != nil {
		return []ExplicitAliasResult{mapExplicitAliasError(err)}
	}
	if err := verifyExplicitAliasManageSession(session, currentURL); err != nil {
		return []ExplicitAliasResult{mapExplicitAliasError(err)}
	}
	results := make([]ExplicitAliasResult, 0, len(candidates))
	for _, candidate := range candidates {
		if err := session.context().Err(); err != nil {
			results = append(results, explicitAliasFailure("request", "Microsoft alias service is temporarily unavailable.", false))
			break
		}
		alias, category, attempted, err := addSingleExplicitAlias(session, candidate, email, proxy, preferredBindingAddress)
		if err != nil {
			results = append(results, explicitAliasAttemptFailure(alias, attempted, err))
			continue
		}
		results = append(results, explicitAliasAddResult(alias, category, attempted))
	}
	return results
}

func verifyExplicitAliasManageSession(session *Session, referer string) error {
	const namesManageURL = "https://account.live.com/names/manage"
	resp, err := session.Get(namesManageURL, requestOptions{
		Headers:           navHeaders(session, map[string]string{"Referer": referer}),
		AllowRedirects:    true,
		HasAllowRedirects: true,
	})
	if err != nil {
		return wrapAuthError(fmt.Sprintf("加载 Microsoft 别名管理页异常: %s", err), AuthStatusRequestError, err)
	}
	page, currentURL := resp.Body, resp.URL
	for round := 0; round < 3; round++ {
		page, currentURL, err = continueExplicitAliasLoginRelay(session, page, currentURL, 6)
		if err != nil {
			return err
		}
		if !isExplicitAliasManageURL(currentURL) {
			page, currentURL, err = followExplicitAliasTarget(session, page, currentURL, 10)
			if err != nil {
				return err
			}
		}
		if isExplicitAliasManageURL(currentURL) {
			return nil
		}
		if round == 2 {
			break
		}
		resp, err = session.Get(namesManageURL, requestOptions{
			Headers:           navHeaders(session, map[string]string{"Referer": currentURL}),
			AllowRedirects:    true,
			HasAllowRedirects: true,
		})
		if err != nil {
			return wrapAuthError(fmt.Sprintf("重新加载 Microsoft 别名管理页异常: %s", err), AuthStatusRequestError, err)
		}
		page, currentURL = resp.Body, resp.URL
	}
	logWarning("Microsoft alias manage preflight did not converge: url=%s page_id=%s action=%s", currentURL, extractPageID(page), extractFormAction(page))
	return newExplicitAliasStageError(
		"Microsoft alias manage page is unavailable.",
		AuthStatusAuthTimeout,
		explicitAliasStageManageRedirected,
	)
}
