package domain

import "time"

type CandidateRefreshStatus string

const (
	CandidateRefreshPending    CandidateRefreshStatus = "pending"
	CandidateRefreshProcessing CandidateRefreshStatus = "processing"
	CandidateRefreshNormal     CandidateRefreshStatus = "normal"
	CandidateRefreshAbnormal   CandidateRefreshStatus = "abnormal"
)

type CandidateRefresh struct {
	ProjectID      uint
	Generation     uint64
	OperatorUserID uint
	Status         CandidateRefreshStatus
	Affected       int
	Failures       int
	LastSafeError  string
	RequestID      string
	Path           string
	RequestedAt    *time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	UpdatedAt      time.Time
}

func IsTerminalCandidateRefreshStatus(status CandidateRefreshStatus) bool {
	return status == CandidateRefreshNormal || status == CandidateRefreshAbnormal
}
