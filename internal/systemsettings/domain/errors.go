package domain

import "errors"

var (
	ErrInvalidKey      = errors.New("invalid system setting key")
	ErrSettingNotFound = errors.New("system setting not found")
)
