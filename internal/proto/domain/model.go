package domain

import "errors"

const (
	ResourceType       = "proto"
	StatusPending      = "pending"
	StatusValidating   = "validating"
	StatusIdentifying  = "identifying"
	StatusNormal       = "normal"
	StatusAbnormal     = "abnormal"
	StatusDisabled     = "disabled"
	StatusDeleted      = "deleted"
	ImportProcessing   = "processing"
	ImportImported     = "imported"
	ImportFailed       = "failed"
	ErrorStrategySkip  = "skip"
	ErrorStrategyAbort = "abort"
)

var (
	ErrInvalidImportFormat = errors.New("proto: invalid import format")
	ErrInvalidResource     = errors.New("proto: invalid resource")
	ErrResourceMissing     = errors.New("proto: resource not found")
	ErrResourceBusy        = errors.New("proto: resource has active allocation")
	ErrResourceConflict    = errors.New("proto: resource conflict")
	ErrImportConflict      = errors.New("proto: import idempotency conflict")
	ErrVersionConflict     = errors.New("proto: resource version conflict")
	ErrDependency          = errors.New("proto: dependency unavailable")
	ErrInvalidClaim        = errors.New("proto: stale task claim")
	ErrResourceNotPrivate  = errors.New("proto: resource is not private")
)

type ImportLine struct {
	LineNumber int
	Email      string
	Password   string
}

type ImportLineError struct {
	Line        int    `json:"line"`
	Email       string `json:"email,omitempty"`
	Category    string `json:"category"`
	SafeMessage string `json:"lastSafeError"`
}

func (e *ImportLineError) Error() string { return e.SafeMessage }
func (e *ImportLineError) Unwrap() error { return ErrInvalidImportFormat }
