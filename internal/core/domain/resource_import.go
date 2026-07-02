package domain

import (
	"strings"
	"time"
)

// ResourceImportStatus represents the lifecycle of a supplier resource import.
type ResourceImportStatus string

const (
	ResourceImportProcessing ResourceImportStatus = "processing"
	ResourceImportImported   ResourceImportStatus = "imported"
	ResourceImportFailed     ResourceImportStatus = "failed"
)

// ImportErrorStrategy controls how a TXT import handles row-level errors.
type ImportErrorStrategy string

const (
	ImportErrorStrategySkip  ImportErrorStrategy = "skip"
	ImportErrorStrategyAbort ImportErrorStrategy = "abort"
)

// NormalizeImportErrorStrategy returns the supported strategy. Empty defaults to skip.
func NormalizeImportErrorStrategy(value string) (ImportErrorStrategy, bool) {
	switch ImportErrorStrategy(strings.ToLower(strings.TrimSpace(value))) {
	case "", ImportErrorStrategySkip:
		return ImportErrorStrategySkip, true
	case ImportErrorStrategyAbort:
		return ImportErrorStrategyAbort, true
	default:
		return "", false
	}
}

// ResourceImport records private import artifacts without storing credentials in logs or responses.
type ResourceImport struct {
	ID               uint
	OwnerUserID      uint
	ResourceType     ResourceType
	SourceObjectKey  string
	FailureObjectKey string
	Status           ResourceImportStatus
	ImportedCount    int
	LastSafeError    string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ImportLineError is a safe parse/validation error for a single import line.
type ImportLineError struct {
	Line        int
	Email       string
	Category    string
	SafeMessage string
}

func (e *ImportLineError) Error() string {
	return e.SafeMessage
}

func (e *ImportLineError) Unwrap() error {
	return ErrInvalidImportFormat
}
