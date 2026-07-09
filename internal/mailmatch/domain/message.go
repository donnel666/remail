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
	FetchJobQueued    FetchJobStatus = "queued"
	FetchJobRunning   FetchJobStatus = "running"
	FetchJobSucceeded FetchJobStatus = "succeeded"
	FetchJobFailed    FetchJobStatus = "failed"
	FetchJobSkipped   FetchJobStatus = "skipped"
)

type Message struct {
	ID                uint
	EmailResourceID   uint
	ResourceType      ResourceType
	Recipient         string
	Recipients        []string
	Sender            string
	Subject           string
	RawBody           string
	RawSource         string
	ProviderPayload   string
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
	Sender           string
	Recipient        string
	ReceivedAt       time.Time
	Subject          string
	Body             string
	VerificationCode string
}

type OrderSnapshot struct {
	OrderNo          string
	Sender           string
	Recipient        string
	ReceivedAt       time.Time
	Subject          string
	Body             string
	VerificationCode string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type FetchJob struct {
	ID              uint
	OrderNo         string
	Purpose         FetchPurpose
	AllocationType  ResourceType
	AllocationID    uint
	ProjectID       uint
	EmailResourceID uint
	Recipient       string
	Status          FetchJobStatus
	Attempts        int
	MaxAttempts     int
	SinceAt         *time.Time
	UntilAt         *time.Time
	FetchedCount    int
	StoredCount     int
	MatchedCount    int
	LastSafeError   string
	RequestID       string
	StartedAt       *time.Time
	FinishedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type FetchState struct {
	OrderNo         string
	LastJobID       *uint
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
	ErrInvalidRequest         = errors.New("mailmatch: invalid request")
	ErrOrderNotFound          = errors.New("mailmatch: order not found")
	ErrOrderForbidden         = errors.New("mailmatch: order forbidden")
	ErrOrderUnavailable       = errors.New("mailmatch: order unavailable")
	ErrFetchQueueUnavailable  = errors.New("mailmatch: fetch queue unavailable")
	ErrFetchJobNotFound       = errors.New("mailmatch: fetch job not found")
	ErrFetchJobConflict       = errors.New("mailmatch: fetch job conflict")
	ErrMailServiceUnavailable = errors.New("mailmatch: mail service unavailable")
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
	switch status {
	case FetchJobSucceeded, FetchJobFailed, FetchJobSkipped:
		return true
	default:
		return false
	}
}
