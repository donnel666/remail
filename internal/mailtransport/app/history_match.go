package app

import (
	"context"
	"time"
)

type HistoricalProjectMessage struct {
	Recipients        []string
	Sender            string
	Subject           string
	Body              string
	BodyPreview       string
	MessageIDHeader   string
	ProviderMessageID string
	Protocol          string
	Folder            string
	ReceivedAt        time.Time
}

type HistoricalProjectMatchRequest struct {
	ResourceID   uint
	EmailAddress string
	Messages     []HistoricalProjectMessage
	ScannedAt    time.Time
}

type HistoricalProjectMatcher interface {
	MatchMicrosoftHistory(ctx context.Context, req HistoricalProjectMatchRequest) error
}
