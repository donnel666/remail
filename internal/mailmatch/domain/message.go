package domain

import (
	"errors"
	"strings"
	"time"
)

type ResourceType string

const (
	ResourceTypeMicrosoft ResourceType = "microsoft"
	ResourceTypeDomain    ResourceType = "domain"
)

type MessageStatus string

const (
	MessageStatusReceived MessageStatus = "received"
	MessageStatusMatched  MessageStatus = "matched"
	MessageStatusIgnored  MessageStatus = "ignored"
)

type FetchPurpose string

const (
	FetchPurposeOrder       FetchPurpose = "order_fetch"
	FetchPurposeManual      FetchPurpose = "manual_fetch"
	FetchPurposeAutoRefresh FetchPurpose = "auto_refresh"
	FetchPurposeAfterSale   FetchPurpose = "aftersale_check"
	FetchPurposeInbound     FetchPurpose = "inbound_consume"
)

type FetchJobStatus string

const (
	FetchJobPending   FetchJobStatus = "pending"
	FetchJobQueued    FetchJobStatus = "processing"
	FetchJobRunning   FetchJobStatus = "processing"
	FetchJobSucceeded FetchJobStatus = "normal"
	FetchJobFailed    FetchJobStatus = "abnormal"
	FetchJobSkipped   FetchJobStatus = "normal"
)

type Message struct {
	ID                uint
	EmailResourceID   uint
	ResourceType      ResourceType
	MatchedOrderID    *uint
	Recipient         string
	Recipients        []string
	Sender            string
	Subject           string
	RawBody           string
	BodyPreview       string
	VerificationCode  string
	MessageIDHeader   string
	ProviderMessageID string
	DedupeKey         string
	Protocol          string
	Folder            string
	Status            MessageStatus
	MatchDiagnostic   string
	ReceivedAt        time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type MailContent struct {
	ID               uint
	Sender           string
	Recipient        string
	ReceivedAt       time.Time
	Subject          string
	Body             string
	BodyPreview      string
	VerificationCode string
}

type FetchJob struct {
	ID                         uint
	Generation                 uint64
	ExpectedCredentialRevision uint64
	OrderNo                    string
	Purpose                    FetchPurpose
	AllocationType             ResourceType
	AllocationID               uint
	ProjectID                  uint
	EmailResourceID            uint
	Recipient                  string
	Status                     FetchJobStatus
	Attempts                   int
	MaxAttempts                int
	SinceAt                    *time.Time
	UntilAt                    *time.Time
	FetchedCount               int
	StoredCount                int
	MatchedCount               int
	LastSafeError              string
	RequestID                  string
	StartedAt                  *time.Time
	FinishedAt                 *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type FetchState struct {
	EmailResourceID uint
	Generation      uint64
	Failures        int
	OperationKind   string
	OrderNo         string
	Purpose         FetchPurpose
	OperatorUserID  *uint
	CredentialRev   uint64
	SinceAt         *time.Time
	UntilAt         *time.Time
	FetchedCount    int
	StoredCount     int
	MatchedCount    int
	LastStatus      string
	LastSubmittedAt *time.Time
	LastSuccessAt   *time.Time
	LastReceivedAt  *time.Time
	CooldownUntil   *time.Time
	LastSafeError   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

var (
	ErrInvalidRequest               = errors.New("mailmatch: invalid request")
	ErrOrderNotFound                = errors.New("mailmatch: order not found")
	ErrOrderForbidden               = errors.New("mailmatch: order forbidden")
	ErrOrderUnavailable             = errors.New("mailmatch: order unavailable")
	ErrPickupCredentialInvalid      = errors.New("mailmatch: pickup credential invalid")
	ErrMessageNotFound              = errors.New("mailmatch: message not found")
	ErrAdminMessageResourceNotFound = errors.New("mailmatch: admin message resource not found")
	ErrFetchQueueUnavailable        = errors.New("mailmatch: fetch queue unavailable")
	ErrFetchJobConflict             = errors.New("mailmatch: fetch job conflict")
	ErrMailServiceUnavailable       = errors.New("mailmatch: mail service unavailable")
)

func NormalizeFetchPurpose(value string) FetchPurpose {
	switch FetchPurpose(strings.ToLower(strings.TrimSpace(value))) {
	case FetchPurposeManual:
		return FetchPurposeManual
	case FetchPurposeAutoRefresh:
		return FetchPurposeAutoRefresh
	case FetchPurposeAfterSale:
		return FetchPurposeAfterSale
	case FetchPurposeInbound:
		return FetchPurposeInbound
	default:
		return FetchPurposeOrder
	}
}

func IsTerminalFetchStatus(status FetchJobStatus) bool {
	return status == FetchJobSucceeded || status == FetchJobFailed
}
