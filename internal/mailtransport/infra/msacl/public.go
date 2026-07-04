package msacl

import (
	"context"
	"errors"
	"strings"

	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
)

type Result struct {
	Valid          bool
	ClientID       string
	AccessToken    string
	RefreshToken   string
	BindingAddress string
	BindingStatus  string
	Category       string
	SafeMessage    string
	ProxyFailure   bool
}

func Authorize(ctx context.Context, email, password, proxy string, preferredBindingAddress string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{
			Category:     "request",
			SafeMessage:  "Microsoft mail service is temporarily unavailable.",
			ProxyFailure: strings.TrimSpace(proxy) != "",
		}, err
	}
	success, err := authorizeAccount(ctx, email, password, proxy, preferredBindingAddress)
	if err != nil {
		return mapAuthError(err, strings.TrimSpace(proxy) != ""), nil
	}
	return Result{
		Valid:          true,
		ClientID:       success.ClientID,
		AccessToken:    success.AccessToken,
		RefreshToken:   success.RefreshToken,
		BindingAddress: strings.TrimSpace(success.BoundMailbox),
		BindingStatus:  string(maildomain.MicrosoftBindingVerified),
	}, nil
}

func mapAuthError(err error, proxyFailure bool) Result {
	var authErr *AuthError
	status := AuthStatusUnknownError
	if errors.As(err, &authErr) && strings.TrimSpace(authErr.Status) != "" {
		status = authErr.Status
	}
	boundMailbox := ""
	if authErr != nil {
		boundMailbox = strings.TrimSpace(authErr.BoundMailbox)
	}
	switch status {
	case AuthStatusPasswordError, AuthStatusUnknownMailbox:
		return aclFailure("password", "Microsoft account or password is incorrect.", false)
	case AuthStatusMFARequired, AuthStatusPasskeyRequired:
		return aclFailure("mfa", "Microsoft account requires additional verification.", false)
	case AuthStatusPhoneVerification:
		return aclFailure("phone", "Microsoft account requires additional verification.", false)
	case AuthStatusAccountLocked:
		return aclFailure("locked", "Microsoft account is currently unavailable.", false)
	case AuthStatusAccountAbnormal, AuthStatusAlreadyBound:
		result := aclFailure("abnormal", "Microsoft account requires manual review.", false)
		result.BindingAddress = boundMailbox
		if boundMailbox != "" {
			result.BindingStatus = string(maildomain.MicrosoftBindingFailed)
		}
		return result
	case AuthStatusCodeTimeout:
		result := aclFailure("code_timeout", "Verification code is incorrect or expired.", false)
		result.BindingAddress = boundMailbox
		if boundMailbox != "" {
			result.BindingStatus = string(maildomain.MicrosoftBindingTimeout)
		}
		return result
	case AuthStatusVerifyCodeError:
		result := aclFailure("code_error", "Verification code is incorrect or expired.", false)
		result.BindingAddress = boundMailbox
		if boundMailbox != "" {
			result.BindingStatus = string(maildomain.MicrosoftBindingFailed)
		}
		return result
	case AuthStatusAuthTimeout:
		return aclFailure("request", "Microsoft mail service is temporarily unavailable.", proxyFailure)
	case AuthStatusRequestError:
		return aclFailure("request", "Microsoft mail service is temporarily unavailable.", proxyFailure)
	default:
		return aclFailure("request", "Microsoft mail service is temporarily unavailable.", proxyFailure)
	}
}

func aclFailure(category, message string, proxyFailure bool) Result {
	return Result{
		Category:     category,
		SafeMessage:  message,
		ProxyFailure: proxyFailure,
	}
}
