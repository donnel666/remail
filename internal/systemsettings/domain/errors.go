package domain

import "errors"

var (
	ErrInvalidKey      = errors.New("invalid system setting key")
	ErrInvalidValue    = errors.New("invalid system setting value")
	ErrSettingNotFound = errors.New("system setting not found")
)

// InvalidValueFieldsError identifies invalid setting keys without exposing
// their values (which may contain credentials).
type InvalidValueFieldsError struct {
	Fields map[string]string
}

func (e *InvalidValueFieldsError) Error() string { return ErrInvalidValue.Error() }
func (e *InvalidValueFieldsError) Unwrap() error { return ErrInvalidValue }
