package gmail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/stretchr/testify/require"
)

func TestValidateLocalGmailAppPasswordBoundsAndClassifiesProxyAttempts(t *testing.T) {
	input := localGmailValidationInput{Email: "owner@gmail.com", AppPassword: "abcd efgh ijkl mnop"}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "eof", err: io.EOF},
		{name: "closed connection", err: net.ErrClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			result := validateLocalGmailAppPasswordWith(context.Background(), input, "http://proxy.invalid:8080",
				func(ctx context.Context, email, appPassword, proxyURL string) error {
					deadline, ok := ctx.Deadline()
					require.True(t, ok)
					require.WithinDuration(t, started.Add(localGmailFetchTimeout), deadline, time.Second)
					require.Equal(t, "owner@gmail.com", email)
					require.Equal(t, "abcdefghijklmnop", appPassword)
					require.Equal(t, "http://proxy.invalid:8080", proxyURL)
					return &localGmailAccountError{err: test.err, safeError: "Gmail transport failed.", temporary: true}
				})

			require.ErrorIs(t, result.Err, test.err)
			require.True(t, result.Temporary)
			require.True(t, result.ProxyFailure)
		})
	}

	authentication := validateLocalGmailAppPasswordWith(context.Background(), input, "http://proxy.invalid:8080",
		func(context.Context, string, string, string) error {
			return &localGmailAccountError{
				err: fmt.Errorf("%w: rejected", errLocalGmailAuthentication), safeError: "Gmail rejected the App Password.",
			}
		})
	require.ErrorIs(t, authentication.Err, errLocalGmailAuthentication)
	require.False(t, authentication.Temporary)
	require.False(t, authentication.ProxyFailure)

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	timedOut := validateLocalGmailAppPasswordWith(expired, input, "http://proxy.invalid:8080",
		func(context.Context, string, string, string) error { return context.Canceled })
	require.ErrorIs(t, timedOut.Err, context.Canceled)
	require.ErrorIs(t, timedOut.Err, context.DeadlineExceeded)
	require.True(t, timedOut.ProxyFailure)
}

func TestStandaloneRotationRejectsAppPasswordOnlyCredential(t *testing.T) {
	result := RotateStandaloneAccount(context.Background(), StandaloneCredential{
		Email: "owner@gmail.com", AppPassword: "abcdefghijklmnop",
	}, "", 1)
	require.ErrorIs(t, result.Err, ErrLocalValidationConflict)
	require.Contains(t, result.SafeError, "login password is required")
}

func TestLocalGmailAccountValidationRotatesFingerprintAndFailedIPv4Proxy(t *testing.T) {
	setGmailRuntime(t, map[string]string{"max_proxy_attempts": "1"})
	proxies := &localGmailPickupProxyStub{configs: []*proxyapp.ProxyConfig{
		{ID: 11, ProxyServerID: 10, URL: "socks5://first.invalid:1080"},
		{ID: 21, ProxyServerID: 20, URL: "http://second.invalid:8080"},
	}}
	input := localGmailValidationInput{
		ResourceID: 7, ValidationGeneration: 3, Email: " Owner.Name@GMAIL.com ", Password: "password",
		BindingEmail: "binding@example.com",
		AppPassword:  "abcdefghijklmnop",
		RequestID:    "gmail-validation-request",
	}
	var proxyURLs []string
	var fingerprints []localGmailBrowserFingerprint
	result := validateLocalGmailAccountWith(context.Background(), proxies, input,
		func(_ context.Context, rotateInput localGmailValidationInput, proxyURL string, fingerprint localGmailBrowserFingerprint) localGmailValidationResult {
			require.Equal(t, "binding@example.com", rotateInput.BindingEmail)
			proxyURLs = append(proxyURLs, proxyURL)
			fingerprints = append(fingerprints, fingerprint)
			if len(proxyURLs) == 1 {
				return localGmailValidationResult{
					SafeError: "Gmail proxy transport failed.", Temporary: true, ProxyFailure: true,
					Err: errors.New("proxy connection reset"),
				}
			}
			return localGmailValidationResult{
				TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", AppPassword: "abcdefghijklmnop",
			}
		})

	require.NoError(t, result.Err)
	require.Equal(t, []string{"socks5://first.invalid:1080", "http://second.invalid:8080"}, proxyURLs)
	require.Len(t, fingerprints, 2)
	require.NotEqual(t, fingerprints[0].Seed, fingerprints[1].Seed)
	require.NotZero(t, fingerprints[0].Seed)
	require.Len(t, proxies.requests, 2)
	for attempt, request := range proxies.requests {
		require.Equal(t, "owner.name@gmail.com", request.Key)
		require.Equal(t, proxydomain.ProxyIPv4, request.IPVersion)
		require.Equal(t, proxydomain.ProxyPurposeAuth, request.Purpose)
		require.True(t, request.AllowSystemFallback)
		require.Equal(t, attempt, request.Attempt)
		require.Equal(t, "gmail-validation-request", request.RequestID)
	}
	require.Empty(t, proxies.requests[0].AvoidProxyServerIDs)
	require.Equal(t, []uint{10}, proxies.requests[1].AvoidProxyServerIDs)
	require.Equal(t, []uint{11}, proxies.failures)
	require.Equal(t, []uint{21}, proxies.successes)
}

func TestLocalGmailAccountValidationReturnsAuthoritativeCredentialsBeforeProxyRetry(t *testing.T) {
	setGmailRuntime(t, map[string]string{"max_proxy_attempts": "2"})
	proxies := &localGmailPickupProxyStub{configs: []*proxyapp.ProxyConfig{
		{ID: 11, ProxyServerID: 10, URL: "http://first.invalid:8080"},
		{ID: 21, ProxyServerID: 20, URL: "http://second.invalid:8080"},
	}}
	calls := 0
	result := validateLocalGmailAccountWith(context.Background(), proxies, localGmailValidationInput{
		ResourceID: 8, ValidationGeneration: 4, Email: "partial@gmail.com", Password: "password", AppPassword: "abcdefghijklmnop",
	}, func(context.Context, localGmailValidationInput, string, localGmailBrowserFingerprint) localGmailValidationResult {
		calls++
		return localGmailValidationResult{
			TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", TwoFactorAuthoritative: true,
			SafeError: "Gmail proxy transport failed.", Temporary: true, ProxyFailure: true,
			Err: errors.New("proxy connection reset"),
		}
	})

	require.Error(t, result.Err)
	require.True(t, result.TwoFactorAuthoritative)
	require.Equal(t, "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", result.TwoFactorSecret)
	require.Equal(t, 1, calls)
	require.Len(t, proxies.requests, 1)
	require.Equal(t, []uint{11}, proxies.failures)
	require.Empty(t, proxies.successes)
}

func TestLocalGmailAccountValidationReturnsRevocationBeforeProxyRetry(t *testing.T) {
	setGmailRuntime(t, map[string]string{"max_proxy_attempts": "2"})
	proxies := &localGmailPickupProxyStub{configs: []*proxyapp.ProxyConfig{
		{ID: 11, ProxyServerID: 10, URL: "http://first.invalid:8080"},
		{ID: 21, ProxyServerID: 20, URL: "http://second.invalid:8080"},
	}}
	calls := 0
	result := validateLocalGmailAccountWith(context.Background(), proxies, localGmailValidationInput{
		ResourceID: 9, ValidationGeneration: 5, Email: "revoked@gmail.com", Password: "password", AppPassword: "abcdefghijklmnop",
	}, func(context.Context, localGmailValidationInput, string, localGmailBrowserFingerprint) localGmailValidationResult {
		calls++
		return localGmailValidationResult{
			AppPasswordRevoked: true, SafeError: "Gmail proxy transport failed.", Temporary: true,
			ProxyFailure: true, Err: errors.New("proxy connection reset"),
		}
	})

	require.Error(t, result.Err)
	require.True(t, result.AppPasswordRevoked)
	require.Equal(t, 1, calls)
	require.Len(t, proxies.requests, 1)
	require.Equal(t, []uint{11}, proxies.failures)
}

func TestGenerateLocalGmailTOTPUsesRFC6238(t *testing.T) {
	code, err := generateLocalGmailTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	require.NoError(t, err)
	require.Equal(t, "287082", code)
}

func TestLocalGmailBrowserProxyStripsCredentials(t *testing.T) {
	server, username, password, err := localGmailBrowserProxy("socks5h://user:secret@proxy.example:1080")
	require.NoError(t, err)
	require.Equal(t, "socks5://proxy.example:1080", server)
	require.Equal(t, "user", username)
	require.Equal(t, "secret", password)
}

func TestLocalGmailProxyAuthenticationNeverSendsCredentialsToServerChallenge(t *testing.T) {
	server := localGmailProxyAuthResponse(&fetch.AuthChallenge{Source: fetch.AuthChallengeSourceServer}, "proxy-user", "proxy-password")
	require.Equal(t, fetch.AuthChallengeResponseResponseCancelAuth, server.Response)
	require.Empty(t, server.Username)
	require.Empty(t, server.Password)

	proxy := localGmailProxyAuthResponse(&fetch.AuthChallenge{Source: fetch.AuthChallengeSourceProxy}, "proxy-user", "proxy-password")
	require.Equal(t, fetch.AuthChallengeResponseResponseProvideCredentials, proxy.Response)
	require.Equal(t, "proxy-user", proxy.Username)
	require.Equal(t, "proxy-password", proxy.Password)
}

func TestExtractLastLocalGmailAppPassword(t *testing.T) {
	password, ok := extractLastLocalGmailAppPassword("old: abcd efgh ijkl mnop\nnew: qrst uvwx yzab cdef")
	require.True(t, ok)
	require.Equal(t, "qrstuvwxyzabcdef", password)
}

func TestLocalGmailOnAccountHostIgnoresContinueQuery(t *testing.T) {
	require.False(t, localGmailOnAccountHost(localGmailLoginURL))
	require.True(t, localGmailOnAccountHost("https://myaccount.google.com/apppasswords?rapt=opaque"))
	require.False(t, localGmailOnAccountHost("https://myaccount.google.com/challenge/pwd?continue=ok"))
}
