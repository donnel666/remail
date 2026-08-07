package gmail

import (
	"context"
	"strings"

	proxyapp "github.com/donnel666/remail/internal/proxy/app"
)

// StandaloneCredential is the credential shape used by the one-account
// validation command. It deliberately reuses the production import parser.
type StandaloneCredential struct {
	Email           string
	Password        string
	BindingEmail    string
	TwoFactorSecret string
	AppPassword     string
}

type StandaloneRotationResult struct {
	TwoFactorSecret          string
	AppPassword              string
	TwoFactorAuthoritative   bool
	AppPasswordAuthoritative bool
	TwoFactorRevoked         bool
	AppPasswordRevoked       bool
	SafeError                string
	Temporary                bool
	ProxyFailure             bool
	Err                      error
}

// StandaloneProxyProvider is the same acquire/report contract used by the
// asynchronous validator and by the Microsoft command tools.
type StandaloneProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

func ParseStandaloneCredentialLine(raw string) (StandaloneCredential, bool) {
	line, ok := parseLocalResourceImportLine(raw)
	if !ok {
		return StandaloneCredential{}, false
	}
	return StandaloneCredential{
		Email: line.email, Password: line.password, BindingEmail: line.bindingEmail,
		TwoFactorSecret: line.twoFactorSecret, AppPassword: line.appPassword,
	}, true
}

func RotateStandaloneAccount(
	ctx context.Context,
	credential StandaloneCredential,
	proxyURL string,
	sourceLine int,
) StandaloneRotationResult {
	return rotateStandaloneAccount(ctx, credential, proxyURL, sourceLine, "", nil)
}

// RotateStandaloneAccountWithProxyProvider uses the production proxy pool,
// including IPv4 selection, proxy health reporting, and server failover.
func RotateStandaloneAccountWithProxyProvider(
	ctx context.Context,
	credential StandaloneCredential,
	proxies StandaloneProxyProvider,
	sourceLine int,
	requestID string,
) StandaloneRotationResult {
	return rotateStandaloneAccount(ctx, credential, "", sourceLine, requestID, proxies)
}

func rotateStandaloneAccount(
	ctx context.Context,
	credential StandaloneCredential,
	proxyURL string,
	sourceLine int,
	requestID string,
	proxies StandaloneProxyProvider,
) StandaloneRotationResult {
	if sourceLine <= 0 {
		return StandaloneRotationResult{SafeError: "Gmail source line is invalid.", Err: ErrLocalValidationConflict}
	}
	input := localGmailValidationInput{
		ResourceID: uint(sourceLine), ValidationGeneration: 1,
		Email: credential.Email, Password: credential.Password, BindingEmail: credential.BindingEmail,
		TwoFactorSecret: credential.TwoFactorSecret, AppPassword: credential.AppPassword,
		RequestID: strings.TrimSpace(requestID),
	}
	var result localGmailValidationResult
	if proxies != nil {
		result = validateLocalGmailAccountWith(ctx, proxies, input, rotateLocalGmailAccount)
	} else {
		result = rotateLocalGmailAccount(ctx, input, proxyURL, newLocalGmailBrowserFingerprint(input))
	}
	return StandaloneRotationResult(result)
}
