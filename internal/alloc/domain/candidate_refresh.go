package domain

import "time"

type CandidateRefreshStatus string

const (
	CandidateRefreshPending   CandidateRefreshStatus = "pending"
	CandidateRefreshQueued    CandidateRefreshStatus = "queued"
	CandidateRefreshRunning   CandidateRefreshStatus = "running"
	CandidateRefreshSucceeded CandidateRefreshStatus = "succeeded"
	CandidateRefreshFailed    CandidateRefreshStatus = "failed"
)

type CandidateRefreshJob struct {
	ID             uint
	ProjectID      uint
	OperatorUserID uint
	Status         CandidateRefreshStatus
	Affected       int
	Attempts       int
	MaxAttempts    int
	LastSafeError  string
	RequestID      string
	Path           string
	StartedAt      *time.Time
	FinishedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func IsTerminalCandidateRefreshStatus(status CandidateRefreshStatus) bool {
	return status == CandidateRefreshSucceeded || status == CandidateRefreshFailed
}
