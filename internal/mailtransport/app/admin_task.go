package app

import governanceapp "github.com/donnel666/remail/internal/governance/app"

// AdminTaskAcceptedResult is the flat administrator command result shared by
// durable MailTransport commands. The task uses Governance's published safe
// TaskView language; no claim, dispatch, credential, or upstream token crosses
// this boundary.
type AdminTaskAcceptedResult struct {
	Task      governanceapp.AdminTaskView
	RequestID string
	Reused    bool
}
