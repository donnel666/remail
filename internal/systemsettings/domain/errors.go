package domain

import "errors"

var (
	ErrInvalidKey      = errors.New("invalid system setting key")
	ErrInvalidValue    = errors.New("invalid system setting value")
	ErrSettingNotFound = errors.New("system setting not found")
)
