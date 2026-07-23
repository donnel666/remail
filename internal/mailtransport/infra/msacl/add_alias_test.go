package msacl

import (
	"context"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateExplicitAliasPrefix(t *testing.T) {
	prefix, err := generateExplicitAliasPrefix()
	require.NoError(t, err)
	matchedNamePair := false
	for _, first := range aliasFirstNames {
		for _, last := range aliasLastNames {
			if strings.HasPrefix(prefix, first+last) && regexp.MustCompile(`^[0-9]{6}$`).MatchString(strings.TrimPrefix(prefix, first+last)) {
				matchedNamePair = true
				break
			}
		}
		if matchedNamePair {
			break
		}
	}
	assert.True(t, matchedNamePair)
}

func TestGenerateExplicitAliasCandidates(t *testing.T) {
	for _, accountEmail := range []string{"owner@outlook.com", "owner@hotmail.com", "owner@outlook.fr"} {
		t.Run(accountEmail, func(t *testing.T) {
			candidates, err := GenerateExplicitAliasCandidates(2, accountEmail)
			require.NoError(t, err)
			require.Len(t, candidates, 2)
			assert.NotEqual(t, candidates[0], candidates[1])
			domain := strings.SplitN(accountEmail, "@", 2)[1]
			for _, candidate := range candidates {
				assert.Regexp(t, regexp.MustCompile(`^[a-z]{6,18}[0-9]{6}@`+regexp.QuoteMeta(domain)+`$`), candidate)
			}
		})
	}
	_, err := GenerateExplicitAliasCandidates(2, "owner@gmail.com")
	require.Error(t, err)
}

func TestNormalizeExplicitAliasCandidatesUsesMicrosoftWhitelist(t *testing.T) {
	require.Equal(t, []string{
		"david123456@hotmail.com",
		"mary654321@outlook.fr",
	}, normalizeExplicitAliasCandidates([]string{
		" David123456@Hotmail.com ",
		"mary654321@outlook.fr",
		"invalid123456@gmail.com",
	}))
}

func TestExplicitAliasAddResultOnlyConfirmsAddedCategory(t *testing.T) {
	for _, category := range []string{aliasCategoryFailed, aliasCategoryRateLimited, aliasCategoryExists} {
		result := explicitAliasAddResult("candidate@outlook.com", category, true)
		require.Empty(t, result.Aliases, category)
		require.Equal(t, []string{"candidate@outlook.com"}, result.Attempted, category)
		require.Equal(t, category, result.Category)
	}

	result := explicitAliasAddResult("confirmed@outlook.com", aliasCategoryAdded, true)
	require.Equal(t, []string{"confirmed@outlook.com"}, result.Aliases)
	require.Equal(t, []string{"confirmed@outlook.com"}, result.Attempted)
}

func TestExplicitAliasAttemptFailurePreservesPostSideEffect(t *testing.T) {
	result := explicitAliasAttemptFailure(
		"candidate@outlook.com",
		true,
		newAuthError("lost POST response", AuthStatusRequestError),
	)

	require.Equal(t, []string{"candidate@outlook.com"}, result.Attempted)
	require.Equal(t, "request", result.Category)
	require.False(t, result.ProxyFailure)

	result = explicitAliasAttemptFailure(
		"candidate@outlook.com",
		true,
		wrapAuthError("lost proxied POST response", AuthStatusRequestError, newSessionTransportError(io.ErrUnexpectedEOF, true)),
	)
	require.True(t, result.ProxyFailure)
}

func TestExtractExplicitAliasCanary(t *testing.T) {
	assert.Equal(t, "hidden-token", extractExplicitAliasCanary(
		`<form><input type="hidden" name="canary" value="hidden-token"></form>`,
	))
	assert.Equal(t, "json/token", extractExplicitAliasCanary(
		`<script>window.config={"canary":"json\/token"}</script>`,
	))
}

func TestExplicitAliasPresentRequiresManagePageAndExactAddress(t *testing.T) {
	page := `<div>David123456&#64;outlook.com</div>`
	assert.True(t, explicitAliasPresentOnManagePage(
		page,
		"https://account.live.com/names/manage",
		"david123456@outlook.com",
	))
	assert.False(t, explicitAliasPresentOnManagePage(
		page,
		"https://account.live.com/AddAssocId",
		"david123456@outlook.com",
	))
	assert.False(t, explicitAliasPresentOnManagePage(
		page,
		"https://account.live.com/names/manage",
		"other123456@outlook.com",
	))
	assert.False(t, explicitAliasPresentOnManagePage(
		`<div>xdavid123456@outlook.com</div>`,
		"https://account.live.com/names/manage",
		"david123456@outlook.com",
	))
}

func TestExtractAllExplicitAliasesFromManagePageUsesMicrosoftWhitelist(t *testing.T) {
	page := `<div>
		first&#64;outlook.com
		second@outlook.com.ar
		third@outlook.sa
		fourth\u0040hotmail.com
		recovery@gmail.com
		legacy@live.com
		excluded@outlook.co.uk
	</div>`

	assert.Equal(t, []string{
		"first@outlook.com",
		"second@outlook.com.ar",
		"third@outlook.sa",
		"fourth@hotmail.com",
	}, extractAllExplicitAliasesFromManagePage(page, "https://account.live.com/names/manage"))
}

func TestExplicitAliasesExceptPrimaryDoesNotBackfillMainMailbox(t *testing.T) {
	assert.Equal(t, []string{"alias@outlook.com"}, explicitAliasesExceptPrimary(
		[]string{"MAIN@OUTLOOK.COM", "alias@outlook.com"},
		"main@outlook.com",
	))
}

func TestIsKMSIPageRejectsOrdinaryLoginContinuation(t *testing.T) {
	assert.True(t, isKMSIPage(`{"sPageId":"i5245"} Stay signed in?`))
	assert.True(t, isKMSIPage(`<h1>保持登录状态?</h1>`))
	assert.False(t, isKMSIPage(`{"sPageId":"i5030","LoginOptions":"3","type":"11","PPFT":"token"}`))
	assert.False(t, isKMSIPage(`{"LoginOptions":"3","type":"11","PPFT":"token"}`))
}

func TestAliasIdentityFlowRejectsCancelledVerification(t *testing.T) {
	page := `{"skip":{"url":"https:\/\/account.live.com\/identity\/confirm?res=cancel"}}`
	_, _, _, err := handleIdentityPage(
		nil,
		page,
		"https://account.live.com/identity/confirm",
		"",
		"owner@outlook.com",
		nil,
		true,
		"",
	)
	require.Error(t, err)
	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	assert.Equal(t, AuthStatusVerifyCodeError, authErr.Status)
}

func TestTraditionalProofFormIsDetectedBehindAddAssocIDURL(t *testing.T) {
	identity, proofs := accountVerificationPageKinds(
		`<form action="/proofs/Add"><input name="EmailAddress"></form>`,
		addAssocIDURL,
		"/proofs/Add",
	)
	assert.False(t, identity)
	assert.True(t, proofs)
}

func TestClassifyAddAssocIDResponse(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		redirectURL string
		body        string
		expected    string
	}{
		{
			name:        "redirect is success",
			statusCode:  302,
			redirectURL: "https://account.live.com/names/manage?noteid=NOTE_AssociatedIdAddedWL",
			expected:    aliasCategoryAdded,
		},
		{
			name:        "unrelated redirect is not success",
			statusCode:  302,
			redirectURL: "https://login.live.com/login.srf",
			expected:    aliasCategoryNeedsVerification,
		},
		{
			name:        "manage redirect without success note is not success",
			statusCode:  302,
			redirectURL: "https://account.live.com/names/manage",
			expected:    aliasCategoryNeedsVerification,
		},
		{
			name:       "http rate limit",
			statusCode: 429,
			expected:   aliasCategoryRateLimited,
		},
		{
			name:       "server failure is retryable",
			statusCode: 503,
			expected:   "request",
		},
		{
			name:       "conflict with taken message",
			statusCode: 409,
			body:       "This email address is already taken.",
			expected:   aliasCategoryExists,
		},
		{
			name:       "empty conflict is unavailable candidate",
			statusCode: 409,
			expected:   aliasCategoryExists,
		},
		{
			name:       "expired authenticated session is retryable",
			statusCode: 401,
			expected:   "request",
		},
		{
			name:       "rate limited",
			statusCode: 200,
			body:       "We limit how often you can add an alias. Try again later.",
			expected:   aliasCategoryRateLimited,
		},
		{
			name:       "already taken",
			statusCode: 200,
			body:       "This email address is already taken.",
			expected:   aliasCategoryExists,
		},
		{
			name:       "verification required",
			statusCode: 200,
			body:       `{"apiCanary":"token","rawProofList":"[]"}`,
			expected:   aliasCategoryNeedsVerification,
		},
		{
			name:       "unknown response",
			statusCode: 400,
			expected:   aliasCategoryFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(
				t,
				test.expected,
				classifyAddAssocIDResponse(test.statusCode, test.redirectURL, test.body),
			)
		})
	}
}

func TestMapExplicitAliasErrorDoesNotBlameProxyForPageTimeout(t *testing.T) {
	result := mapExplicitAliasError(
		&AuthError{
			Message: "page flow incomplete",
			Status:  AuthStatusAuthTimeout,
			Stage:   explicitAliasStageLoginMissingPostURL,
		},
	)

	assert.Equal(t, "auth_timeout", result.Category)
	assert.Equal(t, explicitAliasStageLoginMissingPostURL, result.Stage)
	assert.Equal(t, "Microsoft alias authorization timed out. [stage=login_missing_post_url]", result.SafeMessage)
	assert.False(t, result.ProxyFailure)
}

func TestAnnotateExplicitAliasAuthTimeoutStagePreservesSpecificStage(t *testing.T) {
	err := &AuthError{
		Message: "specific page failure",
		Status:  AuthStatusAuthTimeout,
		Stage:   explicitAliasStageManageRedirected,
	}

	result := annotateExplicitAliasAuthTimeoutStage(err, explicitAliasStageAccountPageIncomplete)

	require.Same(t, err, result)
	assert.Equal(t, explicitAliasStageManageRedirected, err.Stage)
}

func TestMapExplicitAliasErrorMarksProxiedTransportFailure(t *testing.T) {
	result := mapExplicitAliasError(
		wrapAuthError(
			"proxy request failed",
			AuthStatusRequestError,
			newSessionTransportError(io.ErrUnexpectedEOF, true),
		),
	)

	assert.Equal(t, "request", result.Category)
	assert.Equal(t, "Microsoft alias service is temporarily unavailable.", result.SafeMessage)
	assert.True(t, result.ProxyFailure)
}

func TestMapExplicitAliasErrorDoesNotBlameProxyForHTTPFailure(t *testing.T) {
	result := mapExplicitAliasError(
		newAuthError("AddAssocId request failed (HTTP 503)", AuthStatusRequestError),
	)

	assert.Equal(t, "request", result.Category)
	assert.False(t, result.ProxyFailure)
}

func TestMapExplicitAliasErrorDoesNotBlameProxyForCancellation(t *testing.T) {
	result := mapExplicitAliasError(
		wrapAuthError(
			"request canceled",
			AuthStatusRequestError,
			newSessionTransportError(context.Canceled, true),
		),
	)

	assert.Equal(t, "request", result.Category)
	assert.False(t, result.ProxyFailure)
}
