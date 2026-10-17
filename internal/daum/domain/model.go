package domain

import "errors"

const (
	ResourceType       = "daum"
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
	ErrInvalidImportFormat = errors.New("daum: invalid import format")
	ErrInvalidResource     = errors.New("daum: invalid resource")
	ErrResourceMissing     = errors.New("daum: resource not found")
	ErrResourceBusy        = errors.New("daum: resource has active allocation")
	ErrResourceConflict    = errors.New("daum: resource conflict")
	ErrImportConflict      = errors.New("daum: import idempotency conflict")
	ErrVersionConflict     = errors.New("daum: resource version conflict")
	ErrDependency          = errors.New("daum: dependency unavailable")
	ErrInvalidClaim        = errors.New("daum: stale task claim")
	ErrResourceNotPrivate  = errors.New("daum: resource is not private")
)

type ImportLine struct {
	LineNumber     int
	Email          string
	Password       string `json:"-"`
	AppPassword    string `json:"-"`
	Protocol       string
	PickupURL      string `json:"-"`
	KakaoEmail     string
	BindingAddress string
}

type ImportLineError struct {
	Line        int    `json:"line"`
	Email       string `json:"email,omitempty"`
	Category    string `json:"category"`
	SafeMessage string `json:"lastSafeError"`
}

func (e *ImportLineError) Error() string { return e.SafeMessage }
func (e *ImportLineError) Unwrap() error { return ErrInvalidImportFormat }
