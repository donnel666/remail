package domain

import "time"

// ResourceValidationStatus is the durable lifecycle of one resource validation job.
type ResourceValidationStatus string

const (
	ResourceValidationQueued    ResourceValidationStatus = "queued"
	ResourceValidationRunning   ResourceValidationStatus = "running"
	ResourceValidationSucceeded ResourceValidationStatus = "succeeded"
	ResourceValidationFailed    ResourceValidationStatus = "failed"

	ResourceValidationDefaultMaxAttempts = 3
)

// ResourceValidation records one asynchronous validation request.
// The job stores only safe metadata; credentials stay in the resource sub-table.
type ResourceValidation struct {
	ID            uint
	ResourceID    uint
	ResourceType  ResourceType
	OwnerUserID   uint
	Status        ResourceValidationStatus
	Attempts      int
	MaxAttempts   int
	ClaimToken    string
	DispatchToken string
	LastSafeError string
	RequestID     string
	Path          string
	DispatchedAt  *time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func IsTerminalValidationStatus(status ResourceValidationStatus) bool {
	return status == ResourceValidationSucceeded || status == ResourceValidationFailed
}
