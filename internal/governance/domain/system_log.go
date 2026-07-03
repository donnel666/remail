package domain

// SystemLog is a safe diagnostic record for system events and upstream failures.
type SystemLog struct {
	Level     string
	Module    string
	EventType string
	RequestID string
	BizType   string
	BizID     string
	Message   string
	Detail    string
}
