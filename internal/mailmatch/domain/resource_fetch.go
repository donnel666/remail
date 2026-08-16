package domain

import (
	"errors"
	"time"
)

// ResourceFetchJobStatus is the shared lifecycle vocabulary used by the
// independently persisted administrator-fetch and resource-history tasks.
type ResourceFetchJobStatus string

type ResourceFetchJobKind string

const (
	ResourceFetchJobFetch   ResourceFetchJobKind = "fetch"
	ResourceFetchJobHistory ResourceFetchJobKind = "history"

	ResourceFetchJobQueued    ResourceFetchJobStatus = "pending"
	ResourceFetchJobRunning   ResourceFetchJobStatus = "processing"
	ResourceFetchJobSucceeded ResourceFetchJobStatus = "normal"
	ResourceFetchJobFailed    ResourceFetchJobStatus = "abnormal"
	ResourceFetchJobCanceled  ResourceFetchJobStatus = "abnormal"

	ResourceFetchDefaultMaxAttempts = 3
)

// ResourceFetchJob is a task snapshot. Credentials are read from Core
// immediately before the external call and fenced by revision.
type ResourceFetchJob struct {
	ID                         uint
	Generation                 uint64
	Kind                       ResourceFetchJobKind
	ResourceType               ResourceType
	ResourceID                 uint
	OperatorUserID             uint
	ExpectedCredentialRevision uint64
	Recipient                  string
	OrderNo                    string
	Status                     ResourceFetchJobStatus
	Attempts                   int
	MaxAttempts                int
	FetchedCount               int
	StoredCount                int
	MatchedCount               int
	SinceAt                    *time.Time
	UntilAt                    *time.Time
	LastSafeError              string
	RequestID                  string
	Path                       string
	IdempotencyKey             string
	StartedAt                  *time.Time
	FinishedAt                 *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

func IsValidResourceFetchJobKind(kind ResourceFetchJobKind) bool {
	return kind == ResourceFetchJobFetch || kind == ResourceFetchJobHistory
}

// ResourceFetchScope is the private, in-process input for MailTransport. It is
// never serialized into an HTTP response, task payload, operation log, or
// system log.
type ResourceFetchScope struct {
	ResourceID         uint
	ResourceType       ResourceType
	Status             string
	EmailAddress       string
	OrderNo            string
	ClientID           string
	RefreshToken       string
	GraphAvailable     bool
	CredentialRevision uint64
}

func IsTerminalResourceFetchStatus(status ResourceFetchJobStatus) bool {
	return status == ResourceFetchJobSucceeded || status == ResourceFetchJobFailed
}

var (
	ErrResourceFetchNotFound            = errors.New("mailmatch: resource fetch resource not found")
	ErrResourceFetchDeleted             = errors.New("mailmatch: resource fetch resource deleted")
	ErrResourceFetchCredentialsMissing  = errors.New("mailmatch: resource fetch credentials missing")
	ErrResourceFetchCredentialChanged   = errors.New("mailmatch: resource fetch credential revision changed")
	ErrResourceFetchJobConflict         = errors.New("mailmatch: resource fetch job conflict")
	ErrResourceFetchInvalidClaim        = errors.New("mailmatch: resource fetch claim is no longer valid")
	ErrResourceFetchIdempotencyConflict = errors.New("mailmatch: resource fetch idempotency key conflict")
)
