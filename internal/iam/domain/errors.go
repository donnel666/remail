package domain

import "errors"

// Sentinel domain errors for IAM.
// These are used by use cases to produce typed failures
// that the API layer maps to HTTP status codes.
var (
	ErrEmailAlreadyExists         = errors.New("email already exists")
	ErrAccountOrPasswordIncorrect = errors.New("account or password is incorrect")
	ErrCaptchaIncorrect           = errors.New("captcha is incorrect or expired")
	ErrAuthenticationRequired     = errors.New("authentication is required")
	ErrPermissionDenied           = errors.New("permission denied")
	ErrActivationAlreadyDone      = errors.New("activation has already been completed")
	ErrUserDisabled               = errors.New("account has been disabled")
	ErrInvalidPassword            = errors.New("invalid password")
	ErrUserNotFound               = errors.New("user not found")
	ErrInvalidRoleLevel           = errors.New("invalid role level")
	ErrVerificationCodeIncorrect  = errors.New("verification code is incorrect or expired")
	ErrInviteAlreadyExists        = errors.New("invite already exists")
	ErrInviteNotFound             = errors.New("invite not found")
	ErrInviteInvalid              = errors.New("invite is invalid or expired")
	ErrInvalidPermissionPolicy    = errors.New("invalid permission policy")
)
