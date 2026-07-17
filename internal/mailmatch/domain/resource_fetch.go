package domain

import (
	"errors"
	"time"
)

// ResourceFetchJobStatus is the durable lifecycle of an administrator-triggered
// Microsoft resource fetch. It is intentionally separate from FetchJobStatus:
// the latter belongs to the legacy order-scoped mailmatch_fetch_jobs table.
type ResourceFetchJobStatus string

type ResourceFetchJobKind string

const (
	ResourceFetchJobFetch   ResourceFetchJobKind = "fetch"
	ResourceFetchJobHistory ResourceFetchJobKind = "history"

	ResourceFetchJobQueued    ResourceFetchJobStatus = "queued"
	ResourceFetchJobRunning   ResourceFetchJobStatus = "running"
	ResourceFetchJobSucceeded ResourceFetchJobStatus = "succeeded"
	ResourceFetchJobFailed    ResourceFetchJobStatus = "failed"
	ResourceFetchJobCanceled  ResourceFetchJobStatus = "canceled"

	ResourceFetchDefaultMaxAttempts = 3
)

// ResourceFetchJob stores only safe task metadata. Credentials are read from
// Core immediately before the external call and are fenced by
// ExpectedCredentialRevision.
type ResourceFetchJob struct {
	ID                         uint
	Kind                       ResourceFetchJobKind
	ResourceID                 uint
	OperatorUserID             uint
	ExpectedCredentialRevision uint64
	Recipient                  string
	Status                     ResourceFetchJobStatus
	Attempts                   int
	MaxAttempts                int
	FetchedCount               int
	StoredCount                int
	MatchedCount               int
	SinceAt                    *time.Time
	UntilAt                    *time.Time
	ClaimToken                 string
	DispatchToken              string
	LastSafeError              string
	RequestID                  string
	Path                       string
	IdempotencyKey             string
	DispatchedAt               *time.Time
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
	Status             string
	EmailAddress       string
	ClientID           string
	RefreshToken       string
	CredentialRevision uint64
}

func IsTerminalResourceFetchStatus(status ResourceFetchJobStatus) bool {
	switch status {
	case ResourceFetchJobSucceeded, ResourceFetchJobFailed, ResourceFetchJobCanceled:
		return true
	default:
		return false
	}
}

var (
	ErrResourceFetchNotFound            = errors.New("mailmatch: resource fetch resource not found")
	ErrResourceFetchDeleted             = errors.New("mailmatch: resource fetch resource deleted")
	ErrResourceFetchCredentialsMissing  = errors.New("mailmatch: resource fetch credentials missing")
	ErrResourceFetchCredentialChanged   = errors.New("mailmatch: resource fetch credential revision changed")
	ErrResourceFetchJobNotFound         = errors.New("mailmatch: resource fetch job not found")
	ErrResourceFetchJobConflict         = errors.New("mailmatch: resource fetch job conflict")
	ErrResourceFetchInvalidClaim        = errors.New("mailmatch: resource fetch claim is no longer valid")
	ErrResourceFetchIdempotencyConflict = errors.New("mailmatch: resource fetch idempotency key conflict")
)
