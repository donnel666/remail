package api

import (
	"context"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailtransportapp "github.com/donnel666/remail/internal/mailtransport/app"
)

type HistoricalProjectMatcherAdapter struct {
	useCase *mailmatchapp.UseCase
}

func NewHistoricalProjectMatcherAdapter(useCase *mailmatchapp.UseCase) *HistoricalProjectMatcherAdapter {
	return &HistoricalProjectMatcherAdapter{useCase: useCase}
}

func (a *HistoricalProjectMatcherAdapter) MatchMicrosoftHistory(ctx context.Context, req mailtransportapp.HistoricalProjectMatchRequest) error {
	if a == nil || a.useCase == nil {
		return nil
	}
	messages := make([]mailmatchapp.HistoricalProjectMessage, len(req.Messages))
	for i := range req.Messages {
		messages[i] = mailmatchapp.HistoricalProjectMessage{
			Recipients:        req.Messages[i].Recipients,
			Sender:            req.Messages[i].Sender,
			Subject:           req.Messages[i].Subject,
			Body:              req.Messages[i].Body,
			BodyPreview:       req.Messages[i].BodyPreview,
			MessageIDHeader:   req.Messages[i].MessageIDHeader,
			ProviderMessageID: req.Messages[i].ProviderMessageID,
			Protocol:          req.Messages[i].Protocol,
			Folder:            req.Messages[i].Folder,
			ReceivedAt:        req.Messages[i].ReceivedAt,
		}
	}
	return a.useCase.MatchMicrosoftHistory(ctx, mailmatchapp.HistoricalProjectMatchRequest{
		ResourceID:   req.ResourceID,
		EmailAddress: req.EmailAddress,
		Messages:     messages,
		ScannedAt:    req.ScannedAt,
	})
}
