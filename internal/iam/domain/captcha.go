package domain

import "time"

// Captcha represents a captcha challenge.
// The answer is stored server-side and never exposed to the client.
type Captcha struct {
	ID        string
	Answer    string
	ExpiresAt time.Time
}

// IsExpired returns true if the captcha has passed its expiration time.
func (c *Captcha) IsExpired() bool {
	return time.Now().After(c.ExpiresAt)
}
