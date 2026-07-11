package msacl

import (
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
	candidates, err := GenerateExplicitAliasCandidates(2)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	assert.NotEqual(t, candidates[0], candidates[1])
	for _, candidate := range candidates {
		assert.Regexp(t, regexp.MustCompile(`^[a-z]{6,18}[0-9]{6}@outlook\.com$`), candidate)
	}
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
