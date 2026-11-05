package domain

// LoginChallenge is a short-lived first-factor result waiting for TOTP.
type LoginChallenge struct {
	UserID       uint
	TokenVersion int
}
