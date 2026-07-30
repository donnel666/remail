package domain

import "errors"

// Sentinel domain errors for IAM.
// These are used by use cases to produce typed failures
// that the API layer maps to HTTP status codes.
var (
	ErrEmailAlreadyExists                  = errors.New("email already exists")
	ErrRegistrationDisabled                = errors.New("registration is disabled")
	ErrRegistrationEmailLocalInvalid       = errors.New("email local part must contain only letters and digits")
	ErrRegistrationEmailDomainBlocked      = errors.New("registration with this email domain is not allowed")
	ErrInvalidEmailAddress                 = errors.New("invalid email address")
	ErrAccountOrPasswordIncorrect          = errors.New("account or password is incorrect")
	ErrThirdPartyIdentityUnavailable       = errors.New("third-party identity is unavailable")
	ErrThirdPartyIdentityAlreadyBound      = errors.New("third-party identity is already bound")
	ErrGitHubAccountUnavailable            = errors.New("github account is unavailable")
	ErrGitHubVerifiedEmailUnavailable      = errors.New("github verified email is unavailable")
	ErrGitHubAccountTooNew                 = errors.New("github account is too new")
	ErrGitHubPendingExpired                = errors.New("github account verification expired")
	ErrLinuxDOAccountUnavailable           = errors.New("linuxdo account is unavailable")
	ErrLinuxDOIdentityAlreadyBound         = errors.New("linuxdo identity is already bound")
	ErrLinuxDOExistingAccountNotFound      = errors.New("existing account was not found")
	ErrLinuxDONewEmailAlreadyExists        = errors.New("email already belongs to an existing account")
	ErrLinuxDOPendingExpired               = errors.New("linuxdo account setup expired")
	ErrLinuxDOAccountModeInvalid           = errors.New("linuxdo account mode is invalid")
	ErrLinuxDOLegacyMergeUnsupported       = errors.New("legacy linuxdo account cannot be merged automatically")
	ErrLinuxDOTrustLevelTooLow             = errors.New("linuxdo trust level is too low")
	ErrTurnstileInvalid                    = errors.New("human verification failed")
	ErrTurnstileUnavailable                = errors.New("human verification is temporarily unavailable")
	ErrAuthenticationRequired              = errors.New("authentication is required")
	ErrPermissionDenied                    = errors.New("permission denied")
	ErrActivationAlreadyDone               = errors.New("activation has already been completed")
	ErrUserDisabled                        = errors.New("account has been disabled")
	ErrInvalidPassword                     = errors.New("invalid password")
	ErrUserNotFound                        = errors.New("user not found")
	ErrInvalidRole                         = errors.New("invalid role")
	ErrInvalidUserGroup                    = errors.New("invalid user group")
	ErrVerificationCodeIncorrect           = errors.New("verification code is incorrect or expired")
	ErrEmailCodeThrottled                  = errors.New("verification code was requested too recently")
	ErrInviteAlreadyExists                 = errors.New("invite already exists")
	ErrInviteNotFound                      = errors.New("invite not found")
	ErrInviteInvalid                       = errors.New("invite is invalid or expired")
	ErrInvalidPermissionPolicy             = errors.New("invalid permission policy")
	ErrSupplierApplicationNotFound         = errors.New("supplier application not found")
	ErrSupplierApplicationAlreadyReviewing = errors.New("supplier application is already reviewing")
	ErrInvalidSupplierApplication          = errors.New("invalid supplier application")
	ErrInvalidSupplierApplicationStatus    = errors.New("invalid supplier application status")
)

// EmailCodeThrottledError reports that a verification-code request hit the
// resend cooldown. RetryAfterSeconds is the remaining cooldown, surfaced to
// clients via the Retry-After header. It matches ErrEmailCodeThrottled under
// errors.Is, so existing sentinel checks keep working.
type EmailCodeThrottledError struct {
	RetryAfterSeconds int
}

func (e *EmailCodeThrottledError) Error() string {
	return ErrEmailCodeThrottled.Error()
}

func (e *EmailCodeThrottledError) Is(target error) bool {
	return target == ErrEmailCodeThrottled
}
