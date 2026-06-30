package domain

// OperationLog is the safe audit record for high-risk management commands.
type OperationLog struct {
	OperatorUserID uint
	OperationType  string
	ResourceType   string
	ResourceID     string
	Path           string
	Result         string
	SafeSummary    string
	RequestID      string
}
