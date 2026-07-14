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
	BoundDisplay   string
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
	boundDisplay := ""
	if authErr != nil {
		boundMailbox = strings.TrimSpace(authErr.BoundMailbox)
		boundDisplay = strings.TrimSpace(authErr.BoundDisplay)
	}
	switch status {
	case AuthStatusPasswordError:
		return aclFailure("password", "Microsoft account password is incorrect.", false)
	case AuthStatusUnknownMailbox:
		return aclFailure("unknown_mailbox", "Microsoft account does not exist or recovery mailbox is not supported.", false)
	case AuthStatusMFARequired:
		return aclFailure("mfa", "Microsoft account requires authenticator verification.", false)
	case AuthStatusPasskeyRequired:
		return aclFailure("passkey", "Microsoft account requires passkey verification.", false)
	case AuthStatusPhoneVerification:
		return aclFailure("phone", "Microsoft account requires phone verification.", false)
	case AuthStatusAccountLocked:
		return aclFailure("locked", "Microsoft account is locked.", false)
	case AuthStatusAccountAbnormal:
		return aclFailure("account_abnormal", "Microsoft account is restricted or requires recovery.", false)
	case AuthStatusRateLimited:
		return aclFailure("request", "Microsoft authorization is temporarily rate limited.", false)
	case AuthStatusAlreadyBound:
		// Keep the public SafeMessage generic (no masked-address leak); the
		// masked recovery mailbox is carried in the structured BoundDisplay field
		// (only ever the masked display, never a real address) for admin-visible
		// persistence (bound_display).
		result := aclFailure("already_bound", "Microsoft account is already bound to another recovery mailbox.", false)
		result.BindingAddress = boundMailbox
		result.BoundDisplay = boundDisplay
		if boundMailbox != "" {
			result.BindingStatus = string(maildomain.MicrosoftBindingFailed)
		}
		return result
	case AuthStatusCodeTimeout:
		result := aclFailure("code_timeout", "Auxiliary mailbox verification code was not received in time.", false)
		result.BindingAddress = boundMailbox
		if boundMailbox != "" {
			result.BindingStatus = string(maildomain.MicrosoftBindingTimeout)
		}
		return result
	case AuthStatusVerifyCodeError:
		result := aclFailure("code_error", "Auxiliary mailbox verification code is incorrect or expired.", false)
		result.BindingAddress = boundMailbox
		if boundMailbox != "" {
			result.BindingStatus = string(maildomain.MicrosoftBindingFailed)
		}
		return result
	case AuthStatusAuthTimeout:
		return aclFailure("auth_timeout", "Microsoft authorization timed out.", proxyFailure)
	case AuthStatusRequestError:
		return aclFailure("request", "Microsoft authorization request failed temporarily.", proxyFailure)
	default:
		return aclFailure("unknown", "Microsoft authorization failed with unknown status.", proxyFailure)
	}
}

func aclFailure(category, message string, proxyFailure bool) Result {
	return Result{
		Category:     category,
		SafeMessage:  message,
		ProxyFailure: proxyFailure,
	}
}
